package nosharing

import (
	"go/constant"
	"go/token"

	"golang.org/x/tools/go/ssa"
)

// stridePartitionOK recognizes disjoint sub-slice writers from a step-1
// spawning loop of the form:
//
//	start = w*k
//	end   = min((w+1)*k, n)   // or (w+1)*k
//
// with writes confined to [start, end) (IndexAddr or Slice affine in the
// loop index). Any bound not of this form is refused.
func stridePartitionOK(root ssa.Value, accesses []dataAccess) bool {
	if root == nil || len(accesses) == 0 {
		return false
	}
	if !isSliceOrArrayRoot(root) {
		ok := false
		for _, acc := range accesses {
			if isSliceOrArrayRoot(stripToObject(acc.addr)) || isSliceOrArrayRoot(acc.addr) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	var writes []dataAccess
	for _, acc := range accesses {
		if isSliceHeaderLoad(acc) {
			continue
		}
		if isSliceHeaderStore(acc) {
			return false
		}
		if !acc.write {
			return false
		}
		writes = append(writes, acc)
	}
	if len(writes) == 0 {
		return false
	}
	writers := map[*ssa.Function]bool{}
	for _, acc := range writes {
		fn := acc.instr.Parent()
		if fn == nil {
			return false
		}
		writers[fn] = true
	}
	if len(writers) != 1 {
		return false
	}
	var worker *ssa.Function
	for fn := range writers {
		worker = fn
	}
	start, end, ok := strideBoundsOf(worker, writes)
	if !ok {
		return false
	}
	for _, acc := range writes {
		if !writeInStride(acc, start, end) {
			return false
		}
	}
	return strideSpawnsDisjoint(worker, start, end)
}

func strideBoundsOf(fn *ssa.Function, writes []dataAccess) (start, end ssa.Value, ok bool) {
	if fn == nil {
		return nil, nil, false
	}
	// Prefer parameters named by the loop `for i := start; i < end; i++`.
	for _, acc := range writes {
		idx, ok := indexOfWrite(acc)
		if !ok {
			continue
		}
		s, e, ok := loopBoundsOfIndex(idx)
		if !ok {
			continue
		}
		if asBoundVar(s) == nil || asBoundVar(e) == nil {
			continue
		}
		return asBoundVar(s), asBoundVar(e), true
	}
	return nil, nil, false
}

func asBoundVar(v ssa.Value) ssa.Value {
	if v == nil {
		return nil
	}
	switch v.(type) {
	case *ssa.Parameter, *ssa.FreeVar:
		return v
	}
	return nil
}

func indexOfWrite(acc dataAccess) (ssa.Value, bool) {
	if ia, ok := indexAddrOf(acc); ok {
		return ia.Index, true
	}
	if sl, ok := sliceOf(acc); ok {
		if sl.Low != nil {
			return sl.Low, true
		}
	}
	return nil, false
}

func sliceOf(acc dataAccess) (*ssa.Slice, bool) {
	if sl, ok := acc.addr.(*ssa.Slice); ok {
		return sl, true
	}
	if c, ok := acc.instr.(*ssa.Call); ok && c.Common() != nil {
		for _, arg := range c.Common().Args {
			if sl, ok := arg.(*ssa.Slice); ok {
				return sl, true
			}
		}
	}
	return nil, false
}

// loopBoundsOfIndex reports start/end for a step-1 loop index
// `for i := start; i < end; i++` (phi +1, compared with end).
func loopBoundsOfIndex(idx ssa.Value) (start, end ssa.Value, ok bool) {
	// Peel affine i*c or (i+1)*c down to i.
	idx = peelAffineIndex(idx)
	phi, ok := idx.(*ssa.Phi)
	if !ok {
		// Direct use of start (single-element range).
		if asBoundVar(idx) != nil {
			return idx, nil, false
		}
		return nil, nil, false
	}
	var init ssa.Value
	var sawInc bool
	for _, e := range phi.Edges {
		if e == nil {
			continue
		}
		if x, isAdd := isAddConst(e, 1); isAdd && (x == phi || peelAffineIndex(x) == phi) {
			sawInc = true
			continue
		}
		init = e
	}
	if !sawInc || init == nil {
		return nil, nil, false
	}
	end = loopEndComparedTo(phi)
	if end == nil {
		return nil, nil, false
	}
	return init, end, true
}

func peelAffineIndex(v ssa.Value) ssa.Value {
	for v != nil {
		b, ok := v.(*ssa.BinOp)
		if !ok {
			return v
		}
		switch b.Op {
		case token.MUL:
			if isConstInt(b.Y) {
				v = b.X
				continue
			}
			if isConstInt(b.X) {
				v = b.Y
				continue
			}
			return v
		case token.ADD:
			if isConstInt(b.Y) {
				v = b.X
				continue
			}
			if isConstInt(b.X) {
				v = b.Y
				continue
			}
			return v
		default:
			return v
		}
	}
	return v
}

func isConstInt(v ssa.Value) bool {
	c, ok := v.(*ssa.Const)
	return ok && c.Value != nil && c.Value.Kind() == constant.Int
}

func isAddConst(v ssa.Value, n int64) (ssa.Value, bool) {
	b, ok := v.(*ssa.BinOp)
	if !ok || b.Op != token.ADD {
		return nil, false
	}
	if c, ok := b.Y.(*ssa.Const); ok && c.Value != nil {
		if iv, ok := constant.Int64Val(c.Value); ok && iv == n {
			return b.X, true
		}
	}
	if c, ok := b.X.(*ssa.Const); ok && c.Value != nil {
		if iv, ok := constant.Int64Val(c.Value); ok && iv == n {
			return b.Y, true
		}
	}
	return nil, false
}

func loopEndComparedTo(idx ssa.Value) ssa.Value {
	refs := idx.Referrers()
	if refs == nil {
		return nil
	}
	for _, ref := range *refs {
		b, ok := ref.(*ssa.BinOp)
		if !ok {
			continue
		}
		switch b.Op {
		case token.LSS, token.LEQ, token.GTR, token.GEQ:
			if b.X == idx {
				return b.Y
			}
			if b.Y == idx {
				return b.X
			}
		}
	}
	return nil
}

func writeInStride(acc dataAccess, start, end ssa.Value) bool {
	idx, ok := indexOfWrite(acc)
	if !ok {
		return false
	}
	s, e, ok := loopBoundsOfIndex(idx)
	if !ok {
		return false
	}
	return asBoundVar(s) == start && asBoundVar(e) == end
}

func strideSpawnsDisjoint(worker *ssa.Function, start, end ssa.Value) bool {
	if worker == nil {
		return false
	}
	// Find Go/MakeClosure sites that bind start/end onto worker (or a
	// closure whose outermost parent is worker).
	type spawn struct {
		g     *ssa.Go
		start ssa.Value
		end   ssa.Value
	}
	var spawns []spawn
	var walkFrom *ssa.Function
	if worker.Parent() != nil {
		walkFrom = worker.Parent()
	}
	// Also search the package of worker for Go targeting worker.
	seen := map[*ssa.Function]bool{}
	var consider func(*ssa.Function)
	consider = func(fn *ssa.Function) {
		if fn == nil || seen[fn] {
			return
		}
		seen[fn] = true
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				g, ok := instr.(*ssa.Go)
				if !ok || g.Common() == nil {
					continue
				}
				cal := staticCallee(g.Common())
				mc, _ := g.Common().Value.(*ssa.MakeClosure)
				if cal == nil && mc != nil {
					cal, _ = mc.Fn.(*ssa.Function)
				}
				if cal == nil {
					continue
				}
				top := cal
				for top.Parent() != nil {
					top = top.Parent()
				}
				if top != worker && cal != worker {
					continue
				}
				sv, ev := bindStartEnd(g, mc, cal, start, end)
				if sv == nil || ev == nil {
					continue
				}
				spawns = append(spawns, spawn{g: g, start: sv, end: ev})
			}
		}
		for _, anon := range fn.AnonFuncs {
			consider(anon)
		}
	}
	if walkFrom != nil {
		consider(walkFrom)
	}
	if worker.Pkg != nil {
		walkPkgFuncs(worker.Pkg, consider)
	}
	if len(spawns) == 0 {
		return false
	}
	// Every spawn's start/end must be w*k and min((w+1)*k, n) (or (w+1)*k)
	// from the same step-1 induction w, same k.
	var w0, k0 ssa.Value
	for _, sp := range spawns {
		w, k, ok := matchStrideStart(sp.start)
		if !ok {
			return false
		}
		if !matchStrideEnd(sp.end, w, k) {
			return false
		}
		if !isStep1Induction(w, sp.g) {
			return false
		}
		if w0 == nil {
			w0, k0 = w, k
			continue
		}
		if w != w0 || k != k0 {
			return false
		}
	}
	return true
}

func bindStartEnd(g *ssa.Go, mc *ssa.MakeClosure, cal *ssa.Function, start, end ssa.Value) (ssa.Value, ssa.Value) {
	var sv, ev ssa.Value
	bind := func(param, arg ssa.Value) {
		if param == start {
			sv = arg
		}
		if param == end {
			ev = arg
		}
	}
	if cal != nil {
		for i, arg := range g.Common().Args {
			if i < len(cal.Params) {
				bind(cal.Params[i], arg)
			}
		}
	}
	if mc != nil {
		cf, _ := mc.Fn.(*ssa.Function)
		if cf != nil {
			for i, b := range mc.Bindings {
				if i < len(cf.FreeVars) {
					bind(cf.FreeVars[i], b)
				}
			}
		}
	}
	return sv, ev
}

func matchStrideStart(v ssa.Value) (w, k ssa.Value, ok bool) {
	b, isMul := v.(*ssa.BinOp)
	if !isMul || b.Op != token.MUL {
		return nil, nil, false
	}
	return b.X, b.Y, true
}

func matchStrideEnd(v, w, k ssa.Value) bool {
	if v == nil {
		return false
	}
	// (w+1)*k  or  k*(w+1)
	if matchMulAdd1(v, w, k) {
		return true
	}
	// min((w+1)*k, n)
	a, b, ok := isMinCall(v)
	if !ok {
		return false
	}
	return matchMulAdd1(a, w, k) || matchMulAdd1(b, w, k)
}

func matchMulAdd1(v, w, k ssa.Value) bool {
	m, ok := v.(*ssa.BinOp)
	if !ok || m.Op != token.MUL {
		return false
	}
	// (w+1)*k or k*(w+1)
	if m.Y == k {
		x, ok := isAddConst(m.X, 1)
		return ok && x == w
	}
	if m.X == k {
		x, ok := isAddConst(m.Y, 1)
		return ok && x == w
	}
	// w and k may be swapped vs start (start was w*k or k*w).
	if m.X == w || m.Y == w {
		other := m.Y
		if m.Y == w {
			other = m.X
		}
		x, ok := isAddConst(other, 1)
		return ok && (x == k || x == w)
	}
	// (w+1)*k where start was k*w (operands flipped).
	if x, ok := isAddConst(m.X, 1); ok && x == w && (m.Y == k) {
		return true
	}
	if x, ok := isAddConst(m.Y, 1); ok && x == w && (m.X == k) {
		return true
	}
	return false
}

func isMinCall(v ssa.Value) (a, b ssa.Value, ok bool) {
	c, isC := v.(*ssa.Call)
	if !isC || c.Common() == nil {
		return nil, nil, false
	}
	builtin, isB := c.Common().Value.(*ssa.Builtin)
	if !isB || builtin.Name() != "min" || len(c.Common().Args) != 2 {
		return nil, nil, false
	}
	return c.Common().Args[0], c.Common().Args[1], true
}

func isStep1Induction(w ssa.Value, g *ssa.Go) bool {
	if w == nil || g == nil || g.Block() == nil {
		return false
	}
	if !blockInCycle(g.Block()) {
		return false
	}
	// Classic for-loop phi: w = phi(0, w+1)
	if phi, ok := w.(*ssa.Phi); ok {
		for _, e := range phi.Edges {
			if x, isAdd := isAddConst(e, 1); isAdd && x == phi {
				return true
			}
		}
	}
	// range integer: Extract of Next / range iter.
	if _, ok := w.(*ssa.Extract); ok {
		return true
	}
	// Range index stored to an Alloc each iteration (Go 1.22+).
	if u, ok := w.(*ssa.UnOp); ok && u.Op == token.MUL {
		if _, isAlloc := stripToObject(u.X).(*ssa.Alloc); isAlloc && blockInCycle(u.Block()) {
			return true
		}
	}
	return false
}
