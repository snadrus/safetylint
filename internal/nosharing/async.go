package nosharing

import (
	"golang.org/x/tools/go/ssa"
)

// asyncCallbacks lists stdlib (and similar) APIs that retain a callback and
// may invoke it concurrently after returning. Values are 0-based indices into
// CallCommon.Args (receiver is Args[0] for methods).
var asyncCallbacks = map[string]map[string][]int{
	"time": {
		"AfterFunc": {1},
	},
	"net/http": {
		// Package-level HandleFunc(pattern, handler) → arg 1;
		// (*ServeMux).HandleFunc → receiver + pattern + handler → arg 2.
		"HandleFunc": {1, 2},
		"Handle":     {1, 2},
	},
	"os/signal": {
		"NotifyFunc": {0},
	},
}

// curatedSpawns lists Fact-less stdlib APIs that start serving goroutines
// (GOROOT packages are not Fact-analyzed). Used as freeze spawn points.
var curatedSpawns = map[string]map[string]bool{
	"net/http": {
		"ListenAndServe":    true,
		"ListenAndServeTLS": true,
		"Serve":             true,
		"ServeTLS":          true,
	},
}

func isCuratedSpawn(cal *ssa.Function) bool {
	if cal == nil {
		return false
	}
	byName := curatedSpawns[calleePkgPath(cal)]
	return byName[cal.Name()]
}

// checkAsyncCallbackShares treats curated async APIs as share events for
// closure captures (and pointer-ish callback args) passed to them.
func (a *analyzer) checkAsyncCallbackShares(reported map[string]bool) {
	allFuncs := map[*ssa.Function]bool{}
	for _, f := range a.funcs {
		if f != nil {
			allFuncs[f] = true
		}
	}

	for _, fn := range a.funcs {
		if fn == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				var c *ssa.CallCommon
				switch in := instr.(type) {
				case *ssa.Call:
					c = in.Common()
				case *ssa.Defer:
					c = in.Common()
				default:
					continue
				}
				cal := c.StaticCallee()
				if cal == nil {
					continue
				}
				idxs := asyncCallbackIndices(cal)
				if len(idxs) == 0 {
					continue
				}
				for _, i := range idxs {
					if i < 0 || i >= len(c.Args) {
						continue
					}
					a.shareCallbackArg(fn, instr, c.Args[i], allFuncs, reported)
				}
			}
		}
	}
}

func asyncCallbackIndices(cal *ssa.Function) []int {
	path := calleePkgPath(cal)
	byName := asyncCallbacks[path]
	if byName == nil {
		return nil
	}
	return byName[cal.Name()]
}

func (a *analyzer) shareCallbackArg(callFn *ssa.Function, callInstr ssa.Instruction, arg ssa.Value, allFuncs map[*ssa.Function]bool, reported map[string]bool) {
	if arg == nil {
		return
	}
	if mc, ok := arg.(*ssa.MakeClosure); ok {
		clo, _ := mc.Fn.(*ssa.Function)
		goro := map[*ssa.Function]bool{}
		if clo != nil {
			for f := range reachableFuncs(clo, a.pkg) {
				goro[f] = true
			}
		}
		for i, bind := range mc.Bindings {
			mode := ShareRead
			var check ssa.Value = bind
			if clo != nil && i < len(clo.FreeVars) {
				check = clo.FreeVars[i]
				if a.writtenIn(clo.FreeVars[i], goro, true) {
					mode = ShareWrite
				}
			} else if a.writtenIn(bind, goro, true) {
				mode = ShareWrite
			}
			sp := SharedParam{Mode: mode}
			a.checkShareEvent(callFn, callInstr, bind, sp, allFuncs, reported)
			if check != bind {
				a.checkShareEvent(callFn, callInstr, check, sp, allFuncs, reported)
			}
		}
		return
	}
	// Non-closure callback (e.g. http.Handler value): share the arg itself.
	if mayContainPointers(arg.Type()) {
		a.checkShareEvent(callFn, callInstr, arg, SharedParam{Mode: ShareWrite}, allFuncs, reported)
	}
}

// isAsyncRegistration reports whether instr registers a callback that the
// standard library may invoke concurrently after returning.
func (a *analyzer) isAsyncRegistration(instr ssa.Instruction) bool {
	var c *ssa.CallCommon
	switch in := instr.(type) {
	case *ssa.Call:
		c = in.Common()
	case *ssa.Defer:
		c = in.Common()
	default:
		return false
	}
	cal := c.StaticCallee()
	if cal == nil {
		return false
	}
	return len(asyncCallbackIndices(cal)) > 0
}
