package nosharing

import (
	"go/token"

	"golang.org/x/tools/go/ssa"
)

// affineRangePartitionOK accepts concurrent writers of dst[i*K:(i+1)*K]
// (or dst[w*C:min((w+1)*C,n)]) where K/C is loop-invariant and each
// goroutine owns a pairwise-disjoint [start,end) of the index.
func affineRangePartitionOK(root ssa.Value, accesses []dataAccess) bool {
	if root == nil || len(accesses) == 0 {
		return false
	}
	byFn := map[*ssa.Function]ownedAffine{}
	saw := false
	for _, acc := range accesses {
		if isSliceHeaderLoad(acc) {
			continue
		}
		if isSliceHeaderStore(acc) {
			return false
		}
		if !acc.write {
			continue
		}
		sl := sliceOfAccess(acc)
		if sl == nil {
			return false
		}
		idx, k, ok := affineMulBounds(sl)
		if !ok || k <= 0 {
			return false
		}
		fn := acc.instr.Parent()
		if fn == nil {
			return false
		}
		start, end, ok := loopRangeOfIndex(fn, idx)
		if !ok {
			return false
		}
		prev, exists := byFn[fn]
		if exists && (prev.start != start || prev.end != end || prev.k != k) {
			return false
		}
		byFn[fn] = ownedAffine{start: start, end: end, k: k}
		saw = true
	}
	if !saw || len(byFn) < 1 {
		return false
	}
	var spans []ownedAffine
	for _, o := range byFn {
		spans = append(spans, o)
	}
	return affineSpansDisjoint(spans)
}

// affineMulBounds reports sl is idx*K : (idx+1)*K or idx*K : min((idx+1)*K, n).
func affineMulBounds(sl *ssa.Slice) (idx ssa.Value, k int64, ok bool) {
	if sl == nil || sl.Low == nil || sl.High == nil {
		return nil, 0, false
	}
	base, stride, ok := asMulConst(sl.Low)
	if !ok || stride <= 0 {
		return nil, 0, false
	}
	if hi, k2, ok := asMulConst(peelMin(sl.High)); ok && k2 == stride && isPlusOneOf(hi, base) {
		return peelIndex(base), stride, true
	}
	if hi, k2, ok := asMulConst(sl.High); ok && k2 == stride && isPlusOneOf(hi, base) {
		return peelIndex(base), stride, true
	}
	// high = low + K
	if add, ok := sl.High.(*ssa.BinOp); ok && add.Op == token.ADD {
		if c, ok := constIntVal(add.Y); ok && c == stride && sameIndexVal(add.X, sl.Low) {
			return peelIndex(base), stride, true
		}
		if c, ok := constIntVal(add.X); ok && c == stride && sameIndexVal(add.Y, sl.Low) {
			return peelIndex(base), stride, true
		}
	}
	return nil, 0, false
}

func peelMin(v ssa.Value) ssa.Value {
	if v == nil {
		return nil
	}
	if e, ok := v.(*ssa.Extract); ok {
		v = e.Tuple
	}
	call, ok := v.(*ssa.Call)
	if !ok {
		return v
	}
	c := call.Common()
	if c == nil {
		return v
	}
	if !c.IsInvoke() {
		if b, ok := c.Value.(*ssa.Builtin); ok && b.Name() == "min" && len(c.Args) >= 1 {
			return c.Args[0]
		}
	}
	if cal := c.StaticCallee(); cal != nil && cal.Name() == "min" && len(c.Args) >= 1 {
		return c.Args[0]
	}
	return v
}

func asMulConst(v ssa.Value) (idx ssa.Value, k int64, ok bool) {
	v = peelConv(v)
	bin, ok := v.(*ssa.BinOp)
	if !ok || bin.Op != token.MUL {
		return nil, 0, false
	}
	if c, yes := constIntVal(bin.Y); yes && c > 0 {
		return peelConv(bin.X), c, true
	}
	if c, yes := constIntVal(bin.X); yes && c > 0 {
		return peelConv(bin.Y), c, true
	}
	return nil, 0, false
}

func isPlusOneOf(hi, base ssa.Value) bool {
	if hi == nil || base == nil {
		return false
	}
	if sameIndexVal(hi, plusOne(base)) {
		return true
	}
	add, ok := peelConv(hi).(*ssa.BinOp)
	if !ok || add.Op != token.ADD {
		return false
	}
	if c, yes := constIntVal(add.Y); yes && c == 1 && sameIndexVal(add.X, base) {
		return true
	}
	if c, yes := constIntVal(add.X); yes && c == 1 && sameIndexVal(add.Y, base) {
		return true
	}
	return false
}

func plusOne(v ssa.Value) ssa.Value {
	v = peelConv(v)
	bin, ok := v.(*ssa.BinOp)
	if !ok || bin.Op != token.ADD {
		return nil
	}
	if c, yes := constIntVal(bin.Y); yes && c == 1 {
		return peelConv(bin.X)
	}
	if c, yes := constIntVal(bin.X); yes && c == 1 {
		return peelConv(bin.Y)
	}
	return nil
}

func peelConv(v ssa.Value) ssa.Value {
	for v != nil {
		switch x := v.(type) {
		case *ssa.Convert:
			v = x.X
		case *ssa.ChangeType:
			v = x.X
		case *ssa.Extract:
			v = x.Tuple
		default:
			return v
		}
	}
	return v
}

func peelIndex(v ssa.Value) ssa.Value {
	return peelConv(v)
}

func sameIndexVal(a, b ssa.Value) bool {
	if a == nil || b == nil {
		return false
	}
	if peelConv(a) == peelConv(b) {
		return true
	}
	return stripToObject(a) != nil && stripToObject(a) == stripToObject(b)
}

// loopRangeOfIndex reports i comes from for i := start; i < end; i++
// with start/end params or free vars (captured worker range).
func loopRangeOfIndex(fn *ssa.Function, idx ssa.Value) (start, end ssa.Value, ok bool) {
	if fn == nil || idx == nil {
		return nil, nil, false
	}
	idx = peelConv(idx)
	// Phi of (start, i+1) in a loop header.
	phi, ok := idx.(*ssa.Phi)
	if !ok {
		// idx may be a load of a local. Try unique def.
		if u, yes := idx.(*ssa.UnOp); yes && u.Op == token.MUL {
			return loopRangeOfIndex(fn, uniqueStoreVal(u.X))
		}
		if p, yes := idx.(*ssa.Parameter); yes {
			return p, siblingEndParam(p), p != nil && siblingEndParam(p) != nil
		}
		if fv, yes := idx.(*ssa.FreeVar); yes {
			return fv, siblingEndFree(fv), siblingEndFree(fv) != nil
		}
		return nil, nil, false
	}
	var inc *ssa.BinOp
	for _, e := range phi.Edges {
		e = peelConv(e)
		if b, yes := e.(*ssa.BinOp); yes && b.Op == token.ADD {
			if c, yes := constIntVal(b.Y); yes && c == 1 && sameIndexVal(b.X, phi) {
				inc = b
				continue
			}
			if c, yes := constIntVal(b.X); yes && c == 1 && sameIndexVal(b.Y, phi) {
				inc = b
				continue
			}
		}
		start = e
	}
	if inc == nil || start == nil {
		return nil, nil, false
	}
	end = loopEndAgainst(fn, phi)
	if end == nil {
		return nil, nil, false
	}
	return peelRangeBound(start), peelRangeBound(end), true
}

func uniqueStoreVal(addr ssa.Value) ssa.Value {
	cell := stripToObject(addr)
	if cell == nil {
		return nil
	}
	refs := cell.Referrers()
	if refs == nil {
		return nil
	}
	var found ssa.Value
	for _, ref := range *refs {
		st, ok := ref.(*ssa.Store)
		if !ok || stripToObject(st.Addr) != cell {
			continue
		}
		if found != nil {
			return nil
		}
		found = st.Val
	}
	return found
}

func loopEndAgainst(fn *ssa.Function, idx ssa.Value) ssa.Value {
	if fn == nil || idx == nil {
		return nil
	}
	for _, b := range fn.Blocks {
		if b.Comment != "for.loop" && len(b.Instrs) > 0 {
			// Fall through: any If comparing idx < end.
		}
		for _, instr := range b.Instrs {
			bin, ok := instr.(*ssa.BinOp)
			if !ok {
				continue
			}
			switch bin.Op {
			case token.LSS, token.LEQ:
			default:
				continue
			}
			if sameIndexVal(peelConv(bin.X), idx) {
				return peelConv(bin.Y)
			}
			if sameIndexVal(peelConv(bin.Y), idx) {
				return peelConv(bin.X)
			}
		}
	}
	return nil
}

func peelRangeBound(v ssa.Value) ssa.Value {
	v = peelConv(v)
	if u, ok := v.(*ssa.UnOp); ok && u.Op == token.MUL {
		if p, ok := stripToObject(u.X).(*ssa.Parameter); ok {
			return p
		}
		if fv, ok := stripToObject(u.X).(*ssa.FreeVar); ok {
			return fv
		}
	}
	if p, ok := v.(*ssa.Parameter); ok {
		return p
	}
	if fv, ok := v.(*ssa.FreeVar); ok {
		return fv
	}
	return v
}

func siblingEndParam(start *ssa.Parameter) ssa.Value {
	if start == nil || start.Parent() == nil {
		return nil
	}
	params := start.Parent().Params
	for i, p := range params {
		if p == start && i+1 < len(params) && isIntish(params[i+1].Type()) {
			return params[i+1]
		}
	}
	return nil
}

func siblingEndFree(start *ssa.FreeVar) ssa.Value {
	if start == nil || start.Parent() == nil {
		return nil
	}
	fvs := start.Parent().FreeVars
	for i, fv := range fvs {
		if fv == start && i+1 < len(fvs) && isIntish(fvs[i+1].Type()) {
			return fvs[i+1]
		}
	}
	return nil
}

func affineSpansDisjoint(spans []ownedAffine) bool {
	// Fail closed unless every (start,end) binding recovers as an affine
	// worker split. One function/closure is not enough by itself.
	if len(spans) == 0 {
		return false
	}
	k := spans[0].k
	var pairs []affinePair
	for _, s := range spans {
		if s.k != k {
			return false
		}
		b := parseAffineBind(s.start, s.end)
		if !b.ok {
			return false
		}
		pairs = append(pairs, affinePair{s.start, s.end})
	}
	if len(pairs) == 1 {
		return true
	}
	return capturedRangesDisjoint(pairs)
}

type ownedAffine struct {
	start, end ssa.Value
	k          int64
}

type affinePair struct{ start, end ssa.Value }

func capturedRangesDisjoint(pairs []affinePair) bool {
	// Each pair is a (param/freevar) from a different closure. Look at the
	// MakeClosure / Go bindings: start=w*C, end=min((w+1)*C,n) with distinct w.
	type bind struct {
		w, c int64
		ok   bool
		symW ssa.Value
		symC int64
	}
	var parsed []bind
	seenW := map[int64]bool{}
	for _, p := range pairs {
		b := parseAffineBind(p.start, p.end)
		if !b.ok {
			return false
		}
		if b.symW == nil {
			if seenW[b.w] {
				return false
			}
			seenW[b.w] = true
		}
		parsed = append(parsed, b)
	}
	c0 := parsed[0].symC
	if c0 <= 0 {
		c0 = 0
	}
	for _, b := range parsed[1:] {
		if b.symC != parsed[0].symC && (b.symC <= 0 || parsed[0].symC <= 0) {
			// both const worker ids with implicit C=1 is OK
			if b.symW != nil || parsed[0].symW != nil {
				return false
			}
		}
		if b.symC > 0 && parsed[0].symC > 0 && b.symC != parsed[0].symC {
			return false
		}
	}
	// Distinct const w with the same C are pairwise disjoint [w*C,(w+1)*C).
	if len(seenW) == len(parsed) {
		return true
	}
	// Symbolic w (loop var): different closures must bind different w.
	// Fail closed unless every pair used a distinct const w.
	return len(seenW) == len(parsed)
}

func parseAffineBind(start, end ssa.Value) (out struct {
	w, c   int64
	ok     bool
	symW   ssa.Value
	symC   int64
}) {
	// Resolve FreeVar/Parameter to the binding expression at MakeClosure/Go.
	sv := bindDef(start)
	ev := bindDef(end)
	if sv == nil || ev == nil {
		return out
	}
	// start = w * C (C const or loop-invariant)
	wval, stride, c, ok := asMulStride(sv)
	if !ok {
		// start may be the loop index itself (C=1): start=w, end=w+1 or min(w+1,n)
		if wc, yes := constIntVal(sv); yes {
			out.w, out.c, out.ok = wc, 1, true
			if hc, yes := constIntVal(peelMin(ev)); yes && hc == wc+1 {
				return out
			}
			if plusOne(peelMin(ev)) != nil && sameIndexVal(plusOne(peelMin(ev)), sv) {
				return out
			}
			if add, yes := peelMin(ev).(*ssa.BinOp); yes && add.Op == token.ADD {
				if c, yes := constIntVal(add.Y); yes && c == 1 && sameIndexVal(add.X, sv) {
					return out
				}
			}
			out.ok = false
			return out
		}
		return out
	}
	out.symC = c
	if wc, yes := constIntVal(wval); yes {
		out.w = wc
		out.ok = true
		want, _, c2, ok2 := asMulStride(peelMin(ev))
		if ok2 && sameStride(stride, c, c2) && sameIndexVal(want, plusOne(wval)) {
			return out
		}
		if hc, yes := constIntVal(peelMin(ev)); yes && c > 0 && hc == (wc+1)*c {
			return out
		}
		out.ok = false
		return out
	}
	out.symW = peelConv(wval)
	want, _, c2, ok2 := asMulStride(peelMin(ev))
	if !ok2 || !sameStride(stride, c, c2) {
		out.ok = false
		return out
	}
	if !sameIndexVal(want, plusOne(wval)) && plusOne(want) != peelConv(wval) && !sameIndexVal(plusOne(peelConv(wval)), want) {
		out.ok = false
		return out
	}
	out.ok = true
	return out
}

func sameStride(stride ssa.Value, c1, c2 int64) bool {
	if c1 > 0 && c2 > 0 {
		return c1 == c2
	}
	if stride == nil {
		return c1 == c2
	}
	return true
}

// asMulStride reports v is idx*stride. stride may be a positive const (k>0)
// or a loop-invariant SSA value (k==0).
func asMulStride(v ssa.Value) (idx, stride ssa.Value, k int64, ok bool) {
	if idx0, c, yes := asMulConst(v); yes {
		return idx0, nil, c, true
	}
	v = peelConv(v)
	bin, yes := v.(*ssa.BinOp)
	if !yes || bin.Op != token.MUL {
		return nil, nil, 0, false
	}
	x, y := peelConv(bin.X), peelConv(bin.Y)
	if indexLike(x) {
		return x, y, 0, true
	}
	if indexLike(y) {
		return y, x, 0, true
	}
	return x, y, 0, true
}

func indexLike(v ssa.Value) bool {
	v = peelConv(v)
	switch v.(type) {
	case *ssa.Phi, *ssa.Parameter, *ssa.FreeVar:
		return true
	}
	return plusOne(v) != nil
}

func bindDef(v ssa.Value) ssa.Value {
	v = peelRangeBound(v)
	switch x := v.(type) {
	case *ssa.FreeVar:
		return uniqueBindingOf(x)
	case *ssa.Parameter:
		return uniqueArgOf(x)
	}
	return v
}

func uniqueBindingOf(fv *ssa.FreeVar) ssa.Value {
	if fv == nil || fv.Parent() == nil {
		return nil
	}
	fn := fv.Parent()
	enc := fn.Parent()
	if enc == nil {
		return nil
	}
	idx := -1
	for i, f := range fn.FreeVars {
		if f == fv {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	var found ssa.Value
	for _, b := range enc.Blocks {
		for _, instr := range b.Instrs {
			mc, ok := instr.(*ssa.MakeClosure)
			if !ok {
				continue
			}
			cal, _ := mc.Fn.(*ssa.Function)
			if cal != fn || idx >= len(mc.Bindings) {
				continue
			}
			if found != nil && found != mc.Bindings[idx] {
				return nil
			}
			found = mc.Bindings[idx]
		}
	}
	return found
}

func uniqueArgOf(p *ssa.Parameter) ssa.Value {
	if p == nil || p.Parent() == nil {
		return nil
	}
	args := argsPassedAs(p, map[*ssa.Function]bool{})
	// argsPassedAs needs funcs; scan the enclosing function only.
	fn := p.Parent()
	enc := fn.Parent()
	if enc == nil {
		return nil
	}
	idx := -1
	for i, param := range fn.Params {
		if param == p {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	var found ssa.Value
	for _, b := range enc.Blocks {
		for _, instr := range b.Instrs {
			var c *ssa.CallCommon
			switch in := instr.(type) {
			case *ssa.Go:
				c = in.Common()
			case *ssa.Call:
				c = in.Common()
			default:
				continue
			}
			if c == nil || c.StaticCallee() != fn || idx >= len(c.Args) {
				continue
			}
			if found != nil && found != c.Args[idx] {
				return nil
			}
			found = c.Args[idx]
		}
	}
	_ = args
	return found
}
