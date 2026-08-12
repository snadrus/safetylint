package nosharing

import (
	"golang.org/x/tools/go/ssa"
)

// waitGroupExclusiveOK reports that root is a fan-out/join result local:
// written by at most one goroutine under a WaitGroup, the spawner does not
// touch it between the go and Wait, and post-Wait parent access is exclusive.
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
	// writers==0: read-only capture during fan-out; parent may write after Wait.
	// writers==1: classic result-local handoff.
	// No sibling go in this function writes the same root.

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
			if isWrittenIn(root, reachableFuncs(cal, cal.Pkg), true) {
				return false
			}
		}
	}
	return true
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
