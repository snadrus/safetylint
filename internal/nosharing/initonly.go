package nosharing

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// exportInitOnlyFacts publishes InitOnly on exported registration helpers:
// they mutate only map/slice package globals (registry tables), do not spawn,
// and have no non-init callers inside this package (zero local callers is OK —
// typical var _ = Reg(…) from other packages).
func (a *analyzer) exportInitOnlyFacts() {
	if a.pkg == nil || a.pass == nil || !factsEnabled() {
		return
	}
	a.localInitOnly = map[*types.Func]bool{}
	initCtx := a.initContextFuncs()

	for _, fn := range a.funcs {
		if fn == nil || fn.Parent() != nil {
			continue
		}
		obj := funcObject(fn)
		if obj == nil || !obj.Exported() {
			continue
		}
		// Only refuse real concurrency starts — not interface calls (Reg calls
		// t.TypeDetails()), which computeSpawners treats as freeze spawn points.
		if fnStartsGoroutine(fn) {
			continue
		}
		if !a.fnIsRegistryInitHelper(fn) {
			continue
		}
		if !a.onlyInitCallers(fn, initCtx) {
			continue
		}
		a.localInitOnly[obj] = true
		a.pass.ExportObjectFact(obj, &InitOnly{})
	}
}

func fnStartsGoroutine(fn *ssa.Function) bool {
	if fn == nil {
		return false
	}
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if _, ok := instr.(*ssa.Go); ok {
				return true
			}
		}
	}
	return false
}

// fnIsRegistryInitHelper reports that fn performs at least one map update (or
// map/slice header store) on a package global, and never stores a plain
// (non-map/slice) package global. Call-attributed soft writes (e.g. metrics)
// are ignored so helpers like Reg() + stats.Record can still qualify.
func (a *analyzer) fnIsRegistryInitHelper(fn *ssa.Function) bool {
	if fn == nil {
		return false
	}
	saw := false
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			switch in := instr.(type) {
			case *ssa.MapUpdate:
				// Require the map cell itself to be a package Global of map
				// type. Do not strip through FieldAddr — MapUpdate of
				// dynamicLocker.notifier must not qualify NewDynamic as InitOnly.
				gl, ok := in.Map.(*ssa.Global)
				if !ok {
					if u, isLoad := in.Map.(*ssa.UnOp); isLoad && u.Op == token.MUL {
						gl, ok = u.X.(*ssa.Global)
					}
				}
				if !ok || gl.Pkg != a.pkg || !isMapOrSliceType(gl.Type()) {
					continue
				}
				saw = true
			case *ssa.Store:
				gl, ok := in.Addr.(*ssa.Global)
				if !ok {
					if u, isLoad := in.Addr.(*ssa.UnOp); isLoad && u.Op == token.MUL {
						gl, ok = u.X.(*ssa.Global)
					}
				}
				if !ok {
					// FieldAddr store into a struct global is not a registry table.
					continue
				}
				if gl.Pkg != a.pkg {
					continue
				}
				if !isMapOrSliceType(gl.Type()) {
					return false // plain global store ⇒ not a registry helper
				}
				saw = true
			}
		}
	}
	return saw
}

// onlyInitCallers reports that every same-package static call site is from
// an init-context function (or there are no local callers).
func (a *analyzer) onlyInitCallers(fn *ssa.Function, initCtx map[*ssa.Function]bool) bool {
	sites, _, addrTaken := a.callSiteIndex()
	if addrTaken[fn] {
		return false // address taken ⇒ may escape to unknown callers
	}
	for _, s := range sites[fn] {
		if s.caller == nil || !initCtx[s.caller] {
			return false
		}
	}
	return true
}

// initContextFuncs returns functions that run only as part of package
// initialization: init/init#N and (fixpoint) helpers only called from those.
func (a *analyzer) initContextFuncs() map[*ssa.Function]bool {
	sites, goBody, addrTaken := a.callSiteIndex()
	ctx := map[*ssa.Function]bool{}
	for _, fn := range a.funcs {
		if fn == nil || fn.Parent() != nil {
			continue
		}
		if isInitFunc(fn) {
			ctx[fn] = true
		}
	}
	// Closures nested in init-context parents count as init-context.
	for _, fn := range a.funcs {
		if fn == nil {
			continue
		}
		if parentInit(fn, ctx) {
			ctx[fn] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, fn := range a.funcs {
			if fn == nil || ctx[fn] || fn.Parent() != nil {
				continue
			}
			if goBody[fn] || addrTaken[fn] {
				continue
			}
			obj := fn.Object()
			if obj != nil && obj.Exported() {
				continue // exported ⇒ callable outside init from other packages
			}
			ss := sites[fn]
			if len(ss) == 0 {
				continue
			}
			ok := true
			for _, s := range ss {
				if s.caller == nil || !ctx[s.caller] {
					ok = false
					break
				}
			}
			if ok {
				ctx[fn] = true
				changed = true
			}
		}
	}
	return ctx
}

func parentInit(fn *ssa.Function, ctx map[*ssa.Function]bool) bool {
	for p := fn; p != nil; p = p.Parent() {
		if isInitFunc(p) || ctx[p] {
			return true
		}
	}
	return false
}

func (a *analyzer) isInitOnlyFunc(fn *ssa.Function) bool {
	obj := funcObject(fn)
	if obj == nil {
		return false
	}
	if a.localInitOnly[obj] {
		return true
	}
	if a.pass == nil {
		return false
	}
	var fact InitOnly
	return a.pass.ImportObjectFact(obj, &fact)
}

// checkInitOnlyCalls refuses calls to InitOnly functions outside package
// initialization (init funcs / var initializers / init-only helpers).
func (a *analyzer) checkInitOnlyCalls(reported map[string]bool) {
	if a.pkg == nil {
		return
	}
	initCtx := a.initContextFuncs()
	for _, fn := range a.funcs {
		if fn == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				var c *ssa.CallCommon
				var pos token.Pos
				switch in := instr.(type) {
				case *ssa.Call:
					c = in.Common()
					pos = in.Pos()
				case *ssa.Defer:
					c = in.Common()
					pos = in.Pos()
				default:
					continue
				}
				cal := c.StaticCallee()
				if cal == nil || !a.isInitOnlyFunc(cal) {
					continue
				}
				if initCtx[fn] || parentInit(fn, initCtx) || a.isInitOnlyFunc(fn) {
					continue
				}
				name := cal.Name()
				if obj := funcObject(cal); obj != nil {
					name = obj.Name()
				}
				a.reportAt(reported, pos, "InitOnly function %s called outside init/var initializer", name)
			}
		}
	}
}
