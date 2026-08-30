package nosharing

import (
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// singleGoroOwnOK reports that every own field this goroutine writes is
// exclusive to this goroutine (plus constructor / pre-share). A Once.Do
// that wraps the go is required. Loops are allowed: exclusivity is
// "no sibling go / other post-share func reads or writes those fields",
// not dominate-body.
func singleGoroOwnOK(root ssa.Value, spawner *ssa.Function, g ssa.Instruction, goro, allFuncs map[*ssa.Function]bool, ctor *ssa.Function, preShare map[*ssa.Function]bool) bool {
	if root == nil || len(goro) == 0 || !onceWrapsGo(spawner, g) {
		return false
	}
	var goroAcc []dataAccess
	for f := range goro {
		goroAcc = append(goroAcc, collectOwnAndRecvAccesses(root, f)...)
	}
	written := map[int]bool{}
	whole := false
	for _, acc := range goroAcc {
		if !acc.write {
			continue
		}
		if k, ok := fieldIndexOf(acc); ok {
			written[k] = true
		} else if !isObjectInitStore(acc) && !isShareSafeFieldStore(acc) && !isNestedShareSafeAccess(acc) {
			whole = true
		}
	}
	if !whole && len(written) == 0 {
		return false
	}
	if siblingGoTouchesFields(root, g, allFuncs, written, whole) {
		return false
	}
	for f := range allFuncs {
		if f == nil || goro[f] || f == ctor || preShare[f] {
			continue
		}
		if f == spawner {
			continue
		}
		for _, acc := range collectOwnAndRecvAccesses(root, f) {
			if skipSpawnPreShare(acc, spawner, g, preShare, ctor) {
				continue
			}
			if isNestedShareSafeAccess(acc) || isObjectInitStore(acc) || isShareSafeFieldStore(acc) {
				continue
			}
			if whole {
				return false
			}
			k, isField := fieldIndexOf(acc)
			if isField && written[k] {
				return false
			}
			if !isField && acc.write {
				return false
			}
		}
	}
	return true
}

func onceWrapsGo(spawner *ssa.Function, g ssa.Instruction) bool {
	if spawner == nil || g == nil {
		return false
	}
	// The go lives in an anonymous callback of Once.Do, or the spawner
	// itself is that callback.
	if onceDoCallback(spawner) {
		return true
	}
	// go instruction dominated by Once.Do in the same function.
	for _, b := range spawner.Blocks {
		for _, instr := range b.Instrs {
			call, ok := instr.(*ssa.Call)
			if !ok {
				continue
			}
			if !isOnceDoCall(call.Common()) {
				continue
			}
			if dominatesInstr(call, g) {
				return true
			}
		}
	}
	return false
}

func onceDoCallback(fn *ssa.Function) bool {
	if fn == nil {
		return false
	}
	enc := fn.Parent()
	if enc == nil {
		return false
	}
	for _, b := range enc.Blocks {
		for _, instr := range b.Instrs {
			call, ok := instr.(*ssa.Call)
			if !ok || !isOnceDoCall(call.Common()) {
				continue
			}
			c := call.Common()
			if c == nil {
				continue
			}
			for _, arg := range c.Args {
				if mc, ok := arg.(*ssa.MakeClosure); ok {
					if cal, _ := mc.Fn.(*ssa.Function); cal == fn {
						return true
					}
				}
				if f, ok := arg.(*ssa.Function); ok && f == fn {
					return true
				}
			}
		}
	}
	return false
}

func siblingGoTouchesFields(root ssa.Value, g ssa.Instruction, allFuncs map[*ssa.Function]bool, written map[int]bool, whole bool) bool {
	if root == nil {
		return false
	}
	for fn := range allFuncs {
		if fn == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				gi, ok := instr.(*ssa.Go)
				if !ok || gi == g {
					continue
				}
				c := gi.Common()
				if c == nil {
					continue
				}
				cal := c.StaticCallee()
				if cal == nil {
					if mc, ok := c.Value.(*ssa.MakeClosure); ok {
						cal, _ = mc.Fn.(*ssa.Function)
					}
				}
				if cal == nil {
					continue
				}
				reach := reachableFuncs(cal, cal.Pkg)
				for rf := range reach {
					for _, acc := range collectOwnAndRecvAccesses(root, rf) {
					if isNestedShareSafeAccess(acc) || isObjectInitStore(acc) || isShareSafeFieldStore(acc) {
						continue
					}
					if whole {
						return true
					}
					if k, ok := fieldIndexOf(acc); ok && written[k] {
						return true
					}
					if !acc.write {
						continue
					}
					if _, isField := fieldIndexOf(acc); !isField {
						return true
					}
					}
				}
			}
		}
	}
	return false
}

func collectOwnAndRecvAccesses(root ssa.Value, fn *ssa.Function) []dataAccess {
	if fn == nil {
		return nil
	}
	funcs := map[*ssa.Function]bool{fn: true}
	out := collectOwnDataAccesses(root, funcs)
	if len(fn.Params) == 0 {
		return out
	}
	recv := fn.Params[0]
	if recv == nil || recv == root || !sameElemType(recv.Type(), root.Type()) {
		return out
	}
	return append(out, collectOwnDataAccesses(recv, funcs)...)
}

func sameElemType(a, b types.Type) bool {
	if a == nil || b == nil {
		return false
	}
	return types.Identical(derefNamed(a), derefNamed(b))
}

func derefNamed(t types.Type) types.Type {
	t = types.Unalias(t)
	for {
		p, ok := t.(*types.Pointer)
		if !ok {
			return types.Unalias(t)
		}
		t = types.Unalias(p.Elem())
	}
}
