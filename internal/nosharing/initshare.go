package nosharing

import "golang.org/x/tools/go/ssa"

// funcsOnlyCalledBefore returns same-package functions whose every static
// call site happens-before g in spawner (or is inside another such helper).
// Exported, address-taken, and goroutine-body functions are never included
// (they may run after the share). Used to ignore init-only writes when
// proving a go-share is share-safe.
func (a *analyzer) funcsOnlyCalledBefore(spawner *ssa.Function, g ssa.Instruction) map[*ssa.Function]bool {
	out := map[*ssa.Function]bool{}
	if a == nil || spawner == nil || g == nil {
		return out
	}
	sites, goBody, addrTaken := a.callSiteIndex()
	for _, fn := range a.funcs {
		if fn == nil || fn == spawner {
			continue
		}
		if a.onlyCalledBeforeShare(fn, spawner, g, sites, goBody, addrTaken, map[*ssa.Function]int{}) {
			out[fn] = true
		}
	}
	return out
}

// funcsHappensBeforeGo is funcsOnlyCalledBefore but also includes exported
// functions when every in-package call site happens-before g. Used for
// AddHandler-style registration that is exported yet occurs before go Run.
// Exported functions with no in-package sites are excluded (other packages
// may call them after the share).
func (a *analyzer) funcsHappensBeforeGo(spawner *ssa.Function, g ssa.Instruction) map[*ssa.Function]bool {
	out := a.funcsOnlyCalledBefore(spawner, g)
	if a == nil || spawner == nil || g == nil {
		return out
	}
	sites, goBody, addrTaken := a.callSiteIndex()
	for _, fn := range a.funcs {
		if fn == nil || fn == spawner || out[fn] {
			continue
		}
		obj := fn.Object()
		if obj == nil || !obj.Exported() {
			continue
		}
		if goBody[fn] || addrTaken[fn] {
			continue
		}
		sl := sites[fn]
		if len(sl) == 0 {
			continue
		}
		ok := true
		for _, s := range sl {
			if s.caller == spawner && !s.isDefer && dominatesInstr(s.instr, g) {
				continue
			}
			if out[s.caller] || a.onlyCalledBeforeShare(s.caller, spawner, g, sites, goBody, addrTaken, map[*ssa.Function]int{}) {
				continue
			}
			ok = false
			break
		}
		if ok {
			out[fn] = true
		}
	}
	return out
}

// onlyCalledBeforeShare reports that fn cannot run concurrently with the
// goroutine started at g: it is unexported, not address-taken, not a go
// body, and every static call dominates g or is in another pre-share helper.
func (a *analyzer) onlyCalledBeforeShare(fn, spawner *ssa.Function, g ssa.Instruction, sites map[*ssa.Function][]freezeSite, goBody, addrTaken map[*ssa.Function]bool, visiting map[*ssa.Function]int) bool {
	if fn == nil || spawner == nil || g == nil || fn == spawner {
		return false
	}
	if goBody[fn] || addrTaken[fn] {
		return false
	}
	if obj := fn.Object(); obj != nil && obj.Exported() {
		return false
	}
	switch visiting[fn] {
	case 1:
		return true // cycle among helpers still under consideration
	case 2:
		return true
	case 3:
		return false
	}
	visiting[fn] = 1
	sl := sites[fn]
	if len(sl) == 0 {
		// No static sites, unexported, not address-taken: dead or only
		// reached as a go callee (already excluded via goBody).
		visiting[fn] = 2
		return true
	}
	for _, s := range sl {
		if s.caller == spawner && !s.isDefer && dominatesInstr(s.instr, g) {
			continue
		}
		if a.onlyCalledBeforeShare(s.caller, spawner, g, sites, goBody, addrTaken, visiting) {
			continue
		}
		visiting[fn] = 3
		return false
	}
	visiting[fn] = 2
	return true
}

func skipPreShareAccess(acc dataAccess, spawner *ssa.Function, g ssa.Instruction, preShare map[*ssa.Function]bool) bool {
	fn := acc.instr.Parent()
	if fn == nil {
		return false
	}
	if preShare[fn] {
		return true
	}
	return spawner != nil && g != nil && fn == spawner && dominatesInstr(acc.instr, g)
}
