package nosharing

import (
	"golang.org/x/tools/go/ssa"
)

// asyncCallbacks lists stdlib (and similar) APIs whose callback argument is
// invoked later, possibly concurrently. Those slots are spawn roots (like
// `go f()`): the callback body is analyzed as a goroutine, and passing the
// closure does not escape the caller's mutex hold. Values are 0-based indices
// into CallCommon.Args (receiver is Args[0] for methods).
var asyncCallbacks = map[string]map[string][]int{
	"time": {
		// AfterFunc(d, f) runs f on a timer goroutine (Args[1]).
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

// curatedRetains lists stdlib APIs that keep a pointer argument and may use
// it after return (GOROOT equivalent of MayShareParams). Indices are 0-based
// into CallCommon.Args (receiver is Args[0] for methods). Everything else in
// GOROOT is treated as not retaining. Grow this table only when a real API
// needs it — do not SSA-scan GOROOT.
var curatedRetains = map[string]map[string][]int{
	"net/http": {
		// Package ListenAndServe(addr, handler) retains handler (arg 1);
		// (*Server).ListenAndServe retains the receiver (arg 0).
		"ListenAndServe":    {0, 1},
		"ListenAndServeTLS": {0, 1, 3},
		"Serve":             {0, 1},
		"ServeTLS":          {0, 1},
		// Handle / HandleFunc callback slots are asyncCallbacks (spawn),
		// not retains that drop the caller's mutex hold.
	},
	"os/signal": {
		"Notify": {0},
		// NotifyFunc is an asyncCallbacks spawn, not a retain.
	},
	// time.AfterFunc is an asyncCallbacks spawn (NewTicker / NewTimer take
	// no callback and retain nothing).
}

// curatedWriters lists GOROOT APIs that mutate a pointer/slice argument.
// Bodyless GOROOT callees are otherwise not writes. Keep this short; add an
// entry only when a false negative would matter.
var curatedWriters = map[string]map[string]bool{
	"bytes": {
		"Write":       true,
		"WriteString": true,
		"WriteByte":   true,
		"WriteRune":   true,
	},
	"sort": {
		"Slice":       true,
		"SliceStable": true,
		"Sort":        true,
	},
	"sync": {
		"Store": true, // (*sync.Map).Store; Map itself is still sync-whitelisted
	},
}

func isCuratedSpawn(cal *ssa.Function) bool {
	if cal == nil {
		return false
	}
	byName := curatedSpawns[calleePkgPath(cal)]
	return byName[cal.Name()]
}

// checkAsyncSpawns treats curated async callback APIs as `go` of each
// resolvable callback. Unresolved func values (exported wrapper parameters)
// are left to MayShareParams at the wrapper's call sites.
func (a *analyzer) checkAsyncSpawns(spawner *ssa.Function, globals map[*ssa.Global]bool, reported map[string]bool) {
	if spawner == nil {
		return
	}
	for _, b := range spawner.Blocks {
		for _, instr := range b.Instrs {
			c := callCommonOf(instr)
			if c == nil {
				continue
			}
			idxs := asyncCallbackIndices(c.StaticCallee())
			if len(idxs) == 0 {
				continue
			}
			for _, i := range idxs {
				if i < 0 || i >= len(c.Args) {
					continue
				}
				arg := c.Args[i]
				callees := a.resolveFuncValue(arg, map[ssa.Value]bool{})
				if len(callees) == 0 {
					continue
				}
				for _, callee := range callees {
					a.checkGoCallee(instr, arg, nil, spawner, callee, globals, reported)
				}
			}
		}
	}
}

func asyncCallbackIndices(cal *ssa.Function) []int {
	if cal == nil {
		return nil
	}
	path := calleePkgPath(cal)
	byName := asyncCallbacks[path]
	if byName == nil {
		return nil
	}
	return byName[cal.Name()]
}

func curatedRetainIndices(cal *ssa.Function) []int {
	if cal == nil {
		return nil
	}
	if orig := cal.Origin(); orig != nil {
		cal = orig
	}
	byName := curatedRetains[calleePkgPath(cal)]
	if byName == nil {
		return nil
	}
	return byName[calleeBaseName(cal)]
}

func isCuratedWriter(fn *ssa.Function) bool {
	if fn == nil {
		return false
	}
	if orig := fn.Origin(); orig != nil {
		fn = orig
	}
	byName := curatedWriters[calleePkgPath(fn)]
	if byName == nil {
		return false
	}
	return byName[calleeBaseName(fn)]
}

func curatedRetainParams(cal *ssa.Function) []SharedParam {
	idxs := curatedRetainIndices(cal)
	if len(idxs) == 0 {
		return nil
	}
	asyncSlot := map[int]bool{}
	for _, i := range asyncCallbackIndices(cal) {
		asyncSlot[i] = true
	}
	hasRecv := cal.Signature != nil && cal.Signature.Recv() != nil
	var out []SharedParam
	seen := map[SharedParam]bool{}
	for _, i := range idxs {
		if asyncSlot[i] {
			continue // spawned as a goroutine, not retained-as-share
		}
		var sp SharedParam
		if hasRecv && i == 0 {
			sp = SharedParam{Recv: true, Mode: ShareWrite}
		} else {
			idx := i
			if hasRecv {
				idx = i - 1
			}
			if idx < 0 {
				continue
			}
			sp = SharedParam{Index: idx, Mode: ShareWrite}
		}
		if seen[sp] {
			continue
		}
		seen[sp] = true
		out = append(out, sp)
	}
	return out
}

func callCommonOf(instr ssa.Instruction) *ssa.CallCommon {
	switch in := instr.(type) {
	case *ssa.Call:
		return in.Common()
	case *ssa.Defer:
		return in.Common()
	case *ssa.Go:
		return in.Common()
	}
	return nil
}

func isAsyncCallbackCallee(cal *ssa.Function) bool {
	return len(asyncCallbackIndices(cal)) > 0
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
