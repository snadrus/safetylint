package nosharing

import (
	"go/token"

	"golang.org/x/tools/go/ssa"
)

// lockedStackCheckoutOK reports exclusive use of a buffer popped from a
// mutex-protected stack/slice: lock, pop, unlock, use, lock, push.
func lockedStackCheckoutOK(root ssa.Value, spawner *ssa.Function, g ssa.Instruction, goro map[*ssa.Function]bool) bool {
	if root == nil || spawner == nil || g == nil || len(goro) == 0 {
		return false
	}
	stack, mu, ok := poppedUnderLock(spawner, g, root)
	if !ok {
		return false
	}
	for fn := range goro {
		if fn == nil || fn == spawner {
			continue
		}
		if !pushesBackUnderLock(fn, root, stack, mu) {
			return false
		}
		if writesStackUnlocked(fn, stack, mu) {
			return false
		}
	}
	return true
}

func poppedUnderLock(spawner *ssa.Function, g ssa.Instruction, root ssa.Value) (stack, mu ssa.Value, ok bool) {
	cell := stripToObject(root)
	if cell == nil {
		cell = root
	}
	held := analyzeMustHold(spawner)
	for _, b := range spawner.Blocks {
		for _, instr := range b.Instrs {
			if !dominatesInstr(instr, g) {
				continue
			}
			st, yes := instr.(*ssa.Store)
			if !yes || stripToObject(st.Addr) != cell && st.Addr != cell {
				// Also: root is a FreeVar bound to the popped value.
				continue
			}
			if !storeIsPop(st.Val) {
				continue
			}
			at := held[instr]
			if at == nil || len(at) == 0 {
				continue
			}
			if sl, m, yes := stackAndMutexFromPop(st.Val, at); yes {
				return sl, m, true
			}
		}
	}
	// Closure binding: the captured value is the pop result (Index / Slice).
	for _, bind := range goBindings(g) {
		if !sameIndexVal(bind, root) && stripToObject(bind) != stripToObject(root) && bind != root {
			if !isPopResult(bind) {
				continue
			}
		} else if !isPopResult(bind) {
			continue
		}
		src := bind
		if !isPopResult(src) {
			continue
		}
		def := bind
		if in, yes := def.(ssa.Instruction); yes {
			at := held[in]
			if at == nil || len(at) == 0 {
				// Look for a dominating store of this value under a lock.
				if sl, m, yes := popAssignedUnderLock(spawner, g, def, held); yes {
					return sl, m, true
				}
				continue
			}
			if sl, m, yes := stackAndMutexFromPop(def, at); yes {
				return sl, m, true
			}
		}
		if sl, m, yes := popAssignedUnderLock(spawner, g, def, held); yes {
			return sl, m, true
		}
	}
	return nil, nil, false
}

func goBindings(g ssa.Instruction) []ssa.Value {
	var out []ssa.Value
	if goInstr, ok := g.(*ssa.Go); ok && goInstr.Common() != nil {
		if mc, ok := goInstr.Common().Value.(*ssa.MakeClosure); ok {
			out = append(out, mc.Bindings...)
		}
		out = append(out, goInstr.Common().Args...)
	}
	return out
}

func isPopResult(v ssa.Value) bool {
	if v == nil {
		return false
	}
	switch x := v.(type) {
	case *ssa.Index, *ssa.IndexAddr:
		return true
	case *ssa.UnOp:
		if x.Op == token.MUL {
			return isPopResult(x.X)
		}
	case *ssa.Slice:
		// stack = stack[:len-1] is the remainder, not the popped elem.
		return false
	}
	return storeIsPop(v)
}

func storeIsPop(v ssa.Value) bool {
	if v == nil {
		return false
	}
	switch x := v.(type) {
	case *ssa.Index:
		return true
	case *ssa.UnOp:
		if x.Op == token.MUL {
			if _, ok := x.X.(*ssa.IndexAddr); ok {
				return true
			}
		}
	}
	return false
}

func stackAndMutexFromPop(v ssa.Value, held holdSet) (stack, mu ssa.Value, ok bool) {
	cur := v
	for cur != nil {
		switch x := cur.(type) {
		case *ssa.Index:
			stack = stripToObject(x.X)
			mu = anyHeldMutex(held)
			return stack, mu, stack != nil && mu != nil
		case *ssa.IndexAddr:
			stack = stripToObject(x.X)
			mu = anyHeldMutex(held)
			return stack, mu, stack != nil && mu != nil
		case *ssa.UnOp:
			if x.Op == token.MUL {
				cur = x.X
				continue
			}
			return nil, nil, false
		default:
			return nil, nil, false
		}
	}
	return nil, nil, false
}

func anyHeldMutex(held holdSet) ssa.Value {
	for g := range held {
		if g.base != nil {
			return g.base
		}
	}
	return nil
}

func popAssignedUnderLock(spawner *ssa.Function, g ssa.Instruction, popped ssa.Value, held map[ssa.Instruction]holdSet) (stack, mu ssa.Value, ok bool) {
	for _, b := range spawner.Blocks {
		for _, instr := range b.Instrs {
			st, yes := instr.(*ssa.Store)
			if !yes || !dominatesInstr(st, g) {
				continue
			}
			if st.Val != popped && !sameIndexVal(st.Val, popped) {
				continue
			}
			at := held[st]
			if at == nil || len(at) == 0 {
				continue
			}
			return stackAndMutexFromPop(st.Val, at)
		}
	}
	return nil, nil, false
}

func pushesBackUnderLock(fn *ssa.Function, root, stack, mu ssa.Value) bool {
	if fn == nil {
		return false
	}
	held := analyzeMustHold(fn)
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			call, ok := instr.(*ssa.Call)
			if !ok {
				continue
			}
			c := call.Common()
			if c == nil || c.IsInvoke() {
				continue
			}
			biv, ok := c.Value.(*ssa.Builtin)
			if !ok || biv.Name() != "append" || len(c.Args) < 2 {
				continue
			}
			if stripToObject(c.Args[0]) != stack && !sameIndexVal(c.Args[0], stack) {
				// append of a load of the stack cell
				if !stackValue(c.Args[0], stack) {
					continue
				}
			}
			if !valueIsRoot(c.Args[1], root) && !valueIsRoot(c.Args[len(c.Args)-1], root) {
				continue
			}
			if at := held[instr]; at != nil && mutexHeld(at, mu) {
				return true
			}
		}
	}
	return false
}

func stackValue(v, stack ssa.Value) bool {
	if v == nil || stack == nil {
		return false
	}
	if stripToObject(v) == stack || v == stack {
		return true
	}
	if u, ok := v.(*ssa.UnOp); ok && u.Op == token.MUL {
		return stripToObject(u.X) == stack
	}
	return false
}

func valueIsRoot(v, root ssa.Value) bool {
	if v == nil || root == nil {
		return false
	}
	if v == root || stripToObject(v) == stripToObject(root) {
		return true
	}
	if fv, ok := v.(*ssa.FreeVar); ok {
		return fv == root || stripToObject(fv) == stripToObject(root)
	}
	return false
}

func mutexHeld(held holdSet, mu ssa.Value) bool {
	if held == nil || mu == nil {
		return false
	}
	for g := range held {
		if g.base == mu || stripToObject(g.base) == stripToObject(mu) {
			return true
		}
	}
	return false
}

func writesStackUnlocked(fn *ssa.Function, stack, mu ssa.Value) bool {
	if fn == nil || stack == nil {
		return false
	}
	held := analyzeMustHold(fn)
	derived := deriveOwnAddrs(stack, map[*ssa.Function]bool{fn: true})
	derived[stack] = true
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			st, ok := instr.(*ssa.Store)
			if !ok || !derived[st.Addr] {
				continue
			}
			if at := held[instr]; at != nil && mutexHeld(at, mu) {
				continue
			}
			return true
		}
	}
	return false
}
