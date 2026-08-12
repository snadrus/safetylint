package nosharing

import (
	"golang.org/x/tools/go/ssa"
)

// waitGroupExclusiveOK reports that root is a fan-out/join result local:
// written by at most one goroutine under a WaitGroup, the spawner does not
// touch it between the go and Wait, and post-Wait parent access is exclusive.
//
// When a worker writes the root, no sibling goroutine may read or write it
// (concurrent read+write is a race even if Wait joins the writer). Read-only
// fan-out (no worker writers) still allows sibling readers.
func waitGroupExclusiveOK(root ssa.Value, spawner *ssa.Function, g *ssa.Go, goro map[*ssa.Function]bool) bool {
	if root == nil || spawner == nil || g == nil {
		return false
	}
	wg := waitGroupOfGo(spawner, g)
	if wg == nil {
		return false
	}
	waitInstr := findWaitAfter(spawner, g, wg)
	if waitInstr == nil {
		return false
	}
	if spawnerTouchesBetween(root, spawner, g, waitInstr) {
		return false
	}
	writers := 0
	for fn := range goro {
		if fn == nil {
			continue
		}
		if isWrittenIn(root, map[*ssa.Function]bool{fn: true}, true) {
			writers++
			if writers > 1 {
				return false
			}
		}
	}
	if writers > 1 {
		return false
	}

	for _, b := range spawner.Blocks {
		for _, instr := range b.Instrs {
			other, ok := instr.(*ssa.Go)
			if !ok || other == g {
				continue
			}
			cal := staticCallee(other.Common())
			if cal == nil {
				return false
			}
			reach := reachableFuncs(cal, cal.Pkg)
			if writers >= 1 {
				if siblingTouchesRoot(root, spawner, cal, reach) {
					return false
				}
			} else if siblingWritesRoot(root, spawner, cal, reach) {
				// writers==0: sibling may read, but must not write the cell.
				return false
			}
		}
	}
	return true
}

// waitGroupMutexOK reports fan-out under a WaitGroup where every access that
// may run concurrent with workers is covered by one free-standing Mutex/RWMutex
// (package Global or stack Alloc). Post-Wait spawner accesses are exclusive and
// need not hold the lock. Multiple workers may write the same cell under the lock.
func waitGroupMutexOK(root ssa.Value, spawner *ssa.Function, g *ssa.Go, goro map[*ssa.Function]bool) bool {
	if root == nil || spawner == nil || g == nil {
		return false
	}
	wg := waitGroupOfGo(spawner, g)
	if wg == nil {
		return false
	}
	waitInstr := findWaitAfter(spawner, g, wg)
	if waitInstr == nil {
		return false
	}
	conc := concurrentFanoutFuncs(spawner, g, goro)
	if len(conc) == 0 {
		return false
	}
	accesses := fanoutConcurrentAccesses(root, spawner, g, waitInstr, conc)
	if len(accesses) == 0 {
		return false
	}
	if freeStandingMutexGuards(accesses, conc, false) {
		return true
	}
	return freeStandingMutexGuards(accesses, conc, true)
}

func fanoutConcurrentAccesses(root ssa.Value, spawner *ssa.Function, g *ssa.Go, wait ssa.Instruction, conc map[*ssa.Function]bool) []dataAccess {
	var accesses []dataAccess
	seen := map[ssa.Instruction]bool{}
	add := func(accs []dataAccess) {
		for _, acc := range accs {
			if acc.instr == nil || seen[acc.instr] {
				continue
			}
			seen[acc.instr] = true
			accesses = append(accesses, acc)
		}
	}
	add(collectDataAccessesDeep(root, conc, map[ssa.Value]bool{}))
	add(spawnerAccessesBetween(root, spawner, g, wait))

	cells := closureBindingCells(spawner, root)
	if len(cells) == 0 {
		if cell := stripToObject(root); cell != nil {
			cells = []ssa.Value{cell}
		}
	}
	for _, cell := range cells {
		add(collectDataAccessesDeep(cell, conc, map[ssa.Value]bool{}))
		add(spawnerAccessesBetween(cell, spawner, g, wait))
		for fn := range conc {
			if fn == nil || fn == spawner {
				continue
			}
			for _, fv := range closureFreeVarsBoundTo(spawner, fn, cell) {
				add(collectDataAccessesDeep(fv, map[*ssa.Function]bool{fn: true}, map[ssa.Value]bool{}))
			}
		}
	}
	return accesses
}

func concurrentFanoutFuncs(spawner *ssa.Function, g *ssa.Go, goro map[*ssa.Function]bool) map[*ssa.Function]bool {
	out := map[*ssa.Function]bool{}
	for fn := range goro {
		if fn != nil {
			out[fn] = true
		}
	}
	for _, b := range spawner.Blocks {
		for _, instr := range b.Instrs {
			other, ok := instr.(*ssa.Go)
			if !ok || other == g {
				continue
			}
			cal := staticCallee(other.Common())
			if cal == nil {
				// Unknown sibling body: cannot prove the lock covers it.
				return nil
			}
			for fn := range reachableFuncs(cal, cal.Pkg) {
				if fn != nil {
					out[fn] = true
				}
			}
		}
	}
	// Include spawner so Alloc mutexes discovered there are candidates; holds
	// are still checked per access (spawner accesses between go/Wait only).
	out[spawner] = true
	return out
}

func spawnerAccessesBetween(root ssa.Value, fn *ssa.Function, g *ssa.Go, wait ssa.Instruction) []dataAccess {
	if fn == nil {
		return nil
	}
	all := collectDataAccesses(root, map[*ssa.Function]bool{fn: true})
	var out []dataAccess
	for _, acc := range all {
		if acc.instr == nil || acc.instr.Parent() != fn {
			continue
		}
		if dominatesInstr(g, acc.instr) && dominatesInstr(acc.instr, wait) {
			out = append(out, acc)
		}
	}
	return out
}

// siblingTouchesRoot reports a read or write of root inside sibling (including
// via FreeVars that MakeClosure binds to the same heap/local cell as root).
func siblingTouchesRoot(root ssa.Value, spawner, sibling *ssa.Function, reach map[*ssa.Function]bool) bool {
	return siblingAccessRoot(root, spawner, sibling, reach, true)
}

func siblingWritesRoot(root ssa.Value, spawner, sibling *ssa.Function, reach map[*ssa.Function]bool) bool {
	return siblingAccessRoot(root, spawner, sibling, reach, false)
}

func siblingAccessRoot(root ssa.Value, spawner, sibling *ssa.Function, reach map[*ssa.Function]bool, reads bool) bool {
	if isWrittenIn(root, reach, true) {
		return true
	}
	if reads && len(collectDataAccesses(root, reach)) > 0 {
		return true
	}
	cells := closureBindingCells(spawner, root)
	if len(cells) == 0 {
		if cell := stripToObject(root); cell != nil {
			cells = []ssa.Value{cell}
		}
	}
	for _, cell := range cells {
		for _, fv := range closureFreeVarsBoundTo(spawner, sibling, cell) {
			if isWrittenIn(fv, reach, true) {
				return true
			}
			if reads && len(collectDataAccesses(fv, reach)) > 0 {
				return true
			}
		}
		// Sibling may write the Alloc cell directly (rare) or via its freevar.
		if isWrittenIn(cell, reach, true) {
			return true
		}
		if reads && len(collectDataAccesses(cell, reach)) > 0 {
			return true
		}
	}
	return false
}

// closureBindingCells finds Alloc/Global/etc. values that MakeClosure binds to
// root when root is a FreeVar of some closure created in spawner.
func closureBindingCells(spawner *ssa.Function, root ssa.Value) []ssa.Value {
	fv, ok := root.(*ssa.FreeVar)
	if !ok || spawner == nil || fv.Parent() == nil {
		return nil
	}
	parent := fv.Parent()
	var out []ssa.Value
	seen := map[ssa.Value]bool{}
	for _, b := range spawner.Blocks {
		for _, instr := range b.Instrs {
			mc, ok := instr.(*ssa.MakeClosure)
			if !ok {
				continue
			}
			cf, _ := mc.Fn.(*ssa.Function)
			if cf != parent {
				continue
			}
			for i, bind := range mc.Bindings {
				if i >= len(cf.FreeVars) || cf.FreeVars[i] != fv {
					continue
				}
				cell := stripToObject(bind)
				if cell == nil || seen[cell] {
					continue
				}
				seen[cell] = true
				out = append(out, cell)
			}
		}
	}
	return out
}

func closureFreeVarsBoundTo(spawner, closure *ssa.Function, cell ssa.Value) []*ssa.FreeVar {
	if spawner == nil || closure == nil || cell == nil {
		return nil
	}
	var out []*ssa.FreeVar
	for _, b := range spawner.Blocks {
		for _, instr := range b.Instrs {
			mc, ok := instr.(*ssa.MakeClosure)
			if !ok {
				continue
			}
			cf, _ := mc.Fn.(*ssa.Function)
			if cf != closure {
				continue
			}
			for i, bind := range mc.Bindings {
				if i >= len(cf.FreeVars) {
					continue
				}
				if stripToObject(bind) == cell || bind == cell {
					out = append(out, cf.FreeVars[i])
				}
			}
		}
	}
	return out
}

func waitGroupOfGo(fn *ssa.Function, g *ssa.Go) ssa.Value {
	var wg ssa.Value
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			call, ok := instr.(*ssa.Call)
			if !ok || !isWaitGroupMethod(call.Common(), "Add") {
				continue
			}
			recv := stripToObject(recvOfCall(call.Common()))
			if recv == nil || !dominatesInstr(instr, g) {
				continue
			}
			wg = recv
		}
	}
	return wg
}

func findWaitAfter(fn *ssa.Function, g *ssa.Go, wg ssa.Value) ssa.Instruction {
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			call, ok := instr.(*ssa.Call)
			if !ok || !isWaitGroupMethod(call.Common(), "Wait") {
				continue
			}
			recv := stripToObject(recvOfCall(call.Common()))
			if recv != wg {
				continue
			}
			if dominatesInstr(g, instr) {
				return instr
			}
		}
	}
	return nil
}

func isWaitGroupMethod(c *ssa.CallCommon, name string) bool {
	if c == nil {
		return false
	}
	cal := c.StaticCallee()
	if cal == nil || cal.Name() != name {
		return false
	}
	recv := recvOfCall(c)
	return recv != nil && isNamedSyncType(recv.Type(), "WaitGroup")
}

func dominatesInstr(a, b ssa.Instruction) bool {
	if a == nil || b == nil {
		return false
	}
	ab, bb := a.Block(), b.Block()
	if ab == nil || bb == nil {
		return false
	}
	if ab == bb {
		return instrIndex(ab, a) < instrIndex(bb, b)
	}
	return ab.Dominates(bb)
}

func spawnerTouchesBetween(root ssa.Value, fn *ssa.Function, g *ssa.Go, wait ssa.Instruction) bool {
	derived := deriveOwnAddrs(root, map[*ssa.Function]bool{fn: true})
	for v := range derived {
		refs := v.Referrers()
		if refs == nil {
			continue
		}
		for _, ref := range *refs {
			if ref.Parent() != fn || ref == g || ref == wait {
				continue
			}
			if call, ok := ref.(*ssa.Call); ok {
				n := ""
				if cal := call.Common().StaticCallee(); cal != nil {
					n = cal.Name()
				}
				if n == "Add" || n == "Done" || n == "Wait" {
					if isWaitGroupMethod(call.Common(), n) {
						continue
					}
				}
			}
			// Touch strictly after g and strictly before wait.
			if dominatesInstr(g, ref) && dominatesInstr(ref, wait) {
				return true
			}
		}
	}
	return false
}
