package nosharing

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// bufferPoolCheckoutOK reports exclusive use of a slice/array slot whose
// index was received from a token channel (in this goroutine or the
// spawner just before go) and is sent back after use.
func bufferPoolCheckoutOK(root ssa.Value, spawner *ssa.Function, g ssa.Instruction, goro map[*ssa.Function]bool) bool {
	if root == nil || len(goro) == 0 {
		return false
	}
	if !isSliceOrArrayRoot(root) && !sliceFieldRoot(root) {
		if structOf(root.Type()) == nil {
			return false
		}
	}
	idx, ch, ok := tokenAtGo(spawner, g)
	if !ok {
		idx, ch, ok = tokenInFuncs(goro)
	}
	if !ok {
		return false
	}
	for fn := range goro {
		if fn == nil || fn == spawner {
			continue
		}
		if !fnUsesTokenExclusive(fn, root, idx, ch) {
			return false
		}
	}
	return true
}

func sliceFieldRoot(root ssa.Value) bool {
	st := structOf(root.Type())
	if st == nil {
		return false
	}
	for i := 0; i < st.NumFields(); i++ {
		if typeIsSliceOrArray(st.Field(i).Type()) {
			return true
		}
	}
	return false
}

func tokenAtGo(spawner *ssa.Function, g ssa.Instruction) (idx, ch ssa.Value, ok bool) {
	if spawner == nil || g == nil {
		return nil, nil, false
	}
	var binds []ssa.Value
	if goInstr, yes := g.(*ssa.Go); yes && goInstr.Common() != nil {
		if mc, yes := goInstr.Common().Value.(*ssa.MakeClosure); yes {
			binds = mc.Bindings
		}
		binds = append(binds, goInstr.Common().Args...)
	}
	for _, b := range binds {
		if c, yes := chanRecvOf(b); yes && dominatesInstr(recvInstr(b), g) {
			return b, c, true
		}
		// idx := <-ch; go uses idx (idx may be a load of a local store).
		if src, c, yes := recvStoredTo(spawner, b, g); yes {
			return src, c, true
		}
	}
	return nil, nil, false
}

func recvInstr(v ssa.Value) ssa.Instruction {
	if in, ok := v.(ssa.Instruction); ok {
		return in
	}
	return nil
}

func recvStoredTo(fn *ssa.Function, captured ssa.Value, g ssa.Instruction) (idx, ch ssa.Value, ok bool) {
	cell := stripToObject(captured)
	if cell == nil {
		return nil, nil, false
	}
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			st, yes := instr.(*ssa.Store)
			if !yes || stripToObject(st.Addr) != cell {
				continue
			}
			if !dominatesInstr(st, g) {
				continue
			}
			if c, yes := chanRecvOf(st.Val); yes {
				return captured, c, true
			}
		}
	}
	return nil, nil, false
}

func tokenInFuncs(funcs map[*ssa.Function]bool) (idx, ch ssa.Value, ok bool) {
	for fn := range funcs {
		if fn == nil {
			continue
		}
		if i, c, yes := checkoutToken(fn); yes {
			if ok && (idx != i || ch != c) {
				return nil, nil, false
			}
			idx, ch, ok = i, c, true
		}
	}
	return idx, ch, ok
}

func fnUsesTokenExclusive(fn *ssa.Function, root, idx, ch ssa.Value) bool {
	if fn == nil {
		return false
	}
	idx = resolveTokenIn(fn, idx)
	ch = resolveChanIn(fn, ch)
	if !sendsTokenBack(fn, idx, ch) {
		return false
	}
	derived := deriveAddrs(root, map[*ssa.Function]bool{fn: true})
	sawIndex := false
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			switch in := instr.(type) {
			case *ssa.Store:
				if !derived[in.Addr] {
					continue
				}
				if isSliceHeaderStore(dataAccess{instr: in, addr: in.Addr, write: true}) {
					return false
				}
				if !indexUsesToken(in.Addr, idx) {
					return false
				}
				sawIndex = true
			case *ssa.UnOp:
				if in.Op != token.MUL || !derived[in.X] {
					continue
				}
				if isSliceHeaderLoad(dataAccess{instr: in, addr: in.X, write: false}) {
					continue
				}
				if isReceiverPointerLoad(in) {
					continue
				}
				if !indexUsesToken(in.X, idx) && !isChanType(in.X.Type()) {
					// Load of a captured *T / slice header for indexing.
					if typeIsSliceOrArray(in.Type()) || structOf(in.Type()) != nil {
						continue
					}
					return false
				}
			case *ssa.Call:
				if !callWritesDerived(in.Common(), derived) {
					continue
				}
				if !callIndexUsesToken(in.Common(), derived, idx) {
					return false
				}
				sawIndex = true
			}
		}
	}
	return sawIndex
}

func resolveTokenIn(fn *ssa.Function, idx ssa.Value) ssa.Value {
	if fn == nil || idx == nil {
		return idx
	}
	for _, fv := range fn.FreeVars {
		if fv == idx || tokenValue(fv, idx) {
			return fv
		}
	}
	return idx
}

func resolveChanIn(fn *ssa.Function, ch ssa.Value) ssa.Value {
	if fn == nil || ch == nil {
		return ch
	}
	cell := stripToObject(ch)
	for _, fv := range fn.FreeVars {
		if fv == ch || stripToObject(fv) == cell {
			return fv
		}
	}
	return ch
}

func checkoutToken(fn *ssa.Function) (idx, ch ssa.Value, ok bool) {
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			v, isVal := instr.(ssa.Value)
			if !isVal {
				continue
			}
			if c, yes := chanRecvOf(v); yes {
				if idx != nil && idx != v {
					return nil, nil, false
				}
				idx, ch, ok = v, c, true
			}
		}
	}
	return idx, ch, ok
}

func chanRecvOf(v ssa.Value) (ch ssa.Value, ok bool) {
	switch x := v.(type) {
	case *ssa.UnOp:
		if x.Op == token.ARROW {
			return x.X, true
		}
		if x.Op == token.MUL {
			return chanRecvOf(x.X)
		}
	case *ssa.Extract:
		if u, yes := x.Tuple.(*ssa.UnOp); yes && u.Op == token.ARROW {
			return u.X, true
		}
	}
	return nil, false
}

func sendsTokenBack(fn *ssa.Function, idx, ch ssa.Value) bool {
	if fn == nil || idx == nil || ch == nil {
		return false
	}
	same := sameChan(ch)
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if s, ok := instr.(*ssa.Send); ok && same(s.Chan) && tokenValue(s.X, idx) {
				return true
			}
			if d, ok := instr.(*ssa.Defer); ok {
				if cal := d.Common().StaticCallee(); cal != nil && sendsTokenBack(cal, idx, ch) {
					return true
				}
			}
			if c, ok := instr.(*ssa.Call); ok {
				if cal := c.Common().StaticCallee(); cal != nil && cal.Parent() == fn && sendsTokenBack(cal, idx, ch) {
					return true
				}
			}
			if mc, ok := instr.(*ssa.MakeClosure); ok {
				if cf, _ := mc.Fn.(*ssa.Function); cf != nil && sendsTokenBackBound(cf, mc, idx, ch) {
					return true
				}
			}
		}
	}
	return false
}

func sendsTokenBackBound(cf *ssa.Function, mc *ssa.MakeClosure, idx, ch ssa.Value) bool {
	var idxFV, chFV ssa.Value
	for i, bind := range mc.Bindings {
		if i >= len(cf.FreeVars) {
			continue
		}
		if tokenValue(bind, idx) || bind == idx {
			idxFV = cf.FreeVars[i]
		}
		if sameChan(ch)(bind) || bind == ch {
			chFV = cf.FreeVars[i]
		}
	}
	if idxFV == nil {
		idxFV = idx
	}
	if chFV == nil {
		chFV = ch
	}
	return sendsTokenBack(cf, idxFV, chFV)
}

func sameChan(ch ssa.Value) func(ssa.Value) bool {
	cell := stripToObject(ch)
	return func(v ssa.Value) bool {
		if v == ch || stripToObject(v) == cell {
			return true
		}
		if u, ok := v.(*ssa.UnOp); ok && u.Op == token.MUL {
			return stripToObject(u.X) == cell || u.X == ch
		}
		return false
	}
}

func tokenValue(v, idx ssa.Value) bool {
	if v == nil || idx == nil {
		return false
	}
	if v == idx {
		return true
	}
	switch x := v.(type) {
	case *ssa.UnOp:
		if x.Op == token.MUL {
			return tokenValue(x.X, idx) || x.X == idx
		}
	case *ssa.Extract:
		return tokenValue(x.Tuple, idx)
	case *ssa.ChangeType:
		return tokenValue(x.X, idx)
	case *ssa.Convert:
		return tokenValue(x.X, idx)
	case *ssa.FreeVar, *ssa.Parameter:
		return stripToObject(v) == stripToObject(idx)
	}
	return isIntish(v.Type()) && stripToObject(v) == stripToObject(idx)
}

func isIntish(t types.Type) bool {
	if t == nil {
		return false
	}
	b, ok := t.Underlying().(*types.Basic)
	return ok && (b.Info()&types.IsInteger) != 0
}

func indexUsesToken(addr, idx ssa.Value) bool {
	cur := addr
	for cur != nil {
		switch v := cur.(type) {
		case *ssa.IndexAddr:
			return tokenValue(v.Index, idx)
		case *ssa.UnOp:
			if v.Op == token.MUL {
				cur = v.X
				continue
			}
			return false
		case *ssa.FieldAddr:
			cur = v.X
		case *ssa.Slice:
			return tokenValue(v.Low, idx) || tokenValue(v.High, idx)
		default:
			return false
		}
	}
	return false
}

func callWritesDerived(c *ssa.CallCommon, derived map[ssa.Value]bool) bool {
	if c == nil {
		return false
	}
	if !c.IsInvoke() {
		if b, ok := c.Value.(*ssa.Builtin); ok {
			switch b.Name() {
			case "copy", "append", "clear":
				return len(c.Args) > 0 && derived[c.Args[0]]
			}
		}
	}
	for _, arg := range c.Args {
		if derived[arg] && !isValueCopyArg(arg) {
			return true
		}
	}
	return false
}

func callIndexUsesToken(c *ssa.CallCommon, derived map[ssa.Value]bool, idx ssa.Value) bool {
	if c == nil {
		return false
	}
	for _, arg := range c.Args {
		if derived[arg] && indexUsesToken(arg, idx) {
			return true
		}
	}
	return false
}
