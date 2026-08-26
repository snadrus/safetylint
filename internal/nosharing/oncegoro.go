package nosharing

import "golang.org/x/tools/go/ssa"

// singleGoroOwnOK reports that every own field this goroutine writes is
// written only from this goroutine (plus constructor / pre-share). A
// Once.Do that wraps the go plus a one-time rewrite that dominates the
// rest of the goroutine body is treated as freeze-after-start.
func singleGoroOwnOK(root ssa.Value, spawner *ssa.Function, g ssa.Instruction, goro, allFuncs map[*ssa.Function]bool, ctor *ssa.Function, preShare map[*ssa.Function]bool) bool {
	if root == nil || len(goro) == 0 || !onceWrapsGo(spawner, g) {
		return false
	}
	goroAcc := collectOwnDataAccesses(root, goro)
	written := map[int]bool{}
	whole := false
	for _, acc := range goroAcc {
		if !acc.write {
			continue
		}
		if k, ok := fieldIndexOf(acc); ok {
			written[k] = true
		} else if !isObjectInitStore(acc) && !isShareSafeFieldStore(acc) {
			whole = true
		}
	}
	if !whole && len(written) == 0 {
		return false
	}
	if siblingGoWritesFields(root, g, allFuncs, written, whole) {
		return false
	}
	for f := range allFuncs {
		if f == nil || goro[f] || f == ctor || preShare[f] {
			continue
		}
		if f == spawner {
			continue
		}
		for _, acc := range collectOwnDataAccesses(root, map[*ssa.Function]bool{f: true}) {
			if !acc.write {
				continue
			}
			if skipSpawnPreShare(acc, spawner, g, preShare, ctor) {
				continue
			}
			if whole {
				return false
			}
			k, isField := fieldIndexOf(acc)
			if isField && written[k] {
				return false
			}
			if !isField && !isObjectInitStore(acc) {
				return false
			}
		}
	}
	// Once.Do wrapping go: one-time rewrite at goro start is freeze.
	if onceWrapsGo(spawner, g) {
		return goroWritesDominateBody(goroAcc, goro)
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

func goroWritesDominateBody(accs []dataAccess, goro map[*ssa.Function]bool) bool {
	// Every write is in a block that dominates all other accesses of that
	// field in the goroutine (one-time rewrite at start).
	var writes []dataAccess
	for _, acc := range accs {
		if acc.write {
			writes = append(writes, acc)
		}
	}
	if len(writes) == 0 {
		return true
	}
	for _, w := range writes {
		wb := w.instr.Block()
		if wb == nil || blockInCycle(wb) {
			return false
		}
		for _, acc := range accs {
			if acc.instr == w.instr {
				continue
			}
			ab := acc.instr.Block()
			if ab == nil {
				return false
			}
			if ab != wb && !wb.Dominates(ab) {
				return false
			}
		}
	}
	return true
}

func siblingGoWritesFields(root ssa.Value, g ssa.Instruction, allFuncs map[*ssa.Function]bool, written map[int]bool, whole bool) bool {
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
				for _, acc := range collectOwnDataAccesses(root, reach) {
					if !acc.write {
						continue
					}
					if whole {
						return true
					}
					if k, ok := fieldIndexOf(acc); ok && written[k] {
						return true
					}
				}
			}
		}
	}
	return false
}
