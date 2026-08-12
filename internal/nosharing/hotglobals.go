package nosharing

import (
	"go/token"
	"sort"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// exportHotGlobals publishes a package Fact for globals that init-time
// goroutines may touch concurrently. Also records whether init started
// concurrency (used to mark main as already post-spawn).
func (a *analyzer) exportHotGlobals() {
	if a.pkg == nil {
		return
	}
	a.initConcurrent = a.computeInitConcurrent()
	hot := a.collectInitHotGlobals()
	a.localHot = hot
	if a.pass != nil && len(hot.Globals) > 0 {
		cp := *hot
		a.pass.ExportPackageFact(&cp)
	}
}

func isInitFunc(fn *ssa.Function) bool {
	if fn == nil || fn.Parent() != nil {
		return false
	}
	name := fn.Name()
	return name == "init" || strings.HasPrefix(name, "init#")
}

func (a *analyzer) computeInitConcurrent() bool {
	spawner := a.computeSpawners()
	for _, fn := range a.funcs {
		if !isInitFunc(fn) {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				if a.isSpawnEvent(instr, spawner) || a.isAsyncRegistration(instr) {
					return true
				}
			}
		}
	}
	return false
}

func (a *analyzer) collectInitHotGlobals() *HotGlobals {
	out := &HotGlobals{}
	byName := map[string]*HotGlobal{}
	allFuncs := map[*ssa.Function]bool{}
	for _, f := range a.funcs {
		if f != nil {
			allFuncs[f] = true
		}
	}

	for _, fn := range a.funcs {
		if !isInitFunc(fn) {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				g, ok := instr.(*ssa.Go)
				if !ok {
					continue
				}
				// Trace the (small) goroutine body for global touches.
				seeds := []*ssa.Function{}
				if cal := g.Common().StaticCallee(); cal != nil {
					seeds = append(seeds, cal)
				}
				if mc, ok := g.Common().Value.(*ssa.MakeClosure); ok {
					if clo, ok := mc.Fn.(*ssa.Function); ok {
						seeds = append(seeds, clo)
					}
				}
				goro := map[*ssa.Function]bool{}
				for _, s := range seeds {
					for f := range reachableFuncs(s, a.pkg) {
						goro[f] = true
					}
				}
				a.accumulateHotFromFuncs(goro, allFuncs, byName)
			}
			// Async registration in init also retains callbacks that may
			// touch globals — those captures are handled by async shares;
			// globals written inside those callbacks are still in this package.
			for _, instr := range b.Instrs {
				if !a.isAsyncRegistration(instr) {
					continue
				}
				var c *ssa.CallCommon
				switch in := instr.(type) {
				case *ssa.Call:
					c = in.Common()
				case *ssa.Defer:
					c = in.Common()
				}
				if c == nil {
					continue
				}
				for _, i := range asyncCallbackIndices(c.StaticCallee()) {
					if i < 0 || i >= len(c.Args) {
						continue
					}
					if mc, ok := c.Args[i].(*ssa.MakeClosure); ok {
						if clo, ok := mc.Fn.(*ssa.Function); ok {
							goro := reachableFuncs(clo, a.pkg)
							a.accumulateHotFromFuncs(goro, allFuncs, byName)
						}
					}
				}
			}
		}
	}

	for _, g := range byName {
		out.Globals = append(out.Globals, *g)
	}
	sort.Slice(out.Globals, func(i, j int) bool {
		return out.Globals[i].Name < out.Globals[j].Name
	})
	return out
}

func (a *analyzer) accumulateHotFromFuncs(goro, allFuncs map[*ssa.Function]bool, byName map[string]*HotGlobal) {
	if a.pkg == nil {
		return
	}
	for _, m := range a.pkg.Members {
		gl, ok := m.(*ssa.Global)
		if !ok || gl.Pkg != a.pkg {
			continue
		}
		written := a.writtenIn(gl, goro, true)
		accessed := len(collectDataAccesses(gl, goro)) > 0 || written
		if !accessed {
			// Also: FreeVar/param paths won't see Global via collect on empty
			// referrers — scan goro funcs for global operands.
			accessed = globalMentionedIn(gl, goro)
		}
		if !accessed {
			continue
		}
		mode := ShareRead
		if written {
			mode = ShareWrite
		}
		hg := &HotGlobal{Name: gl.Name(), Mode: mode}
		if tied, ok := tiedMutexForRoot(gl, allFuncs); ok {
			hg.Mutex = MutexField{StructPath: tied.structType.String(), Field: tied.field}
		} else if tied, ok := tiedMutexForRoot(gl, goro); ok {
			hg.Mutex = MutexField{StructPath: tied.structType.String(), Field: tied.field}
		}
		if prev, ok := byName[gl.Name()]; ok {
			if prev.Mode == ShareWrite || mode == ShareWrite {
				hg.Mode = ShareWrite
			} else {
				hg.Mode = prev.Mode
			}
			if !hg.Mutex.set() {
				hg.Mutex = prev.Mutex
			}
		}
		byName[gl.Name()] = hg
	}
}

func globalMentionedIn(gl *ssa.Global, funcs map[*ssa.Function]bool) bool {
	for fn := range funcs {
		if fn == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				for _, op := range instr.Operands(nil) {
					if op != nil && *op == gl {
						return true
					}
				}
			}
		}
	}
	return false
}

func tiedMutexForRoot(root ssa.Value, funcs map[*ssa.Function]bool) (structuralGuard, bool) {
	cands := findStructuralGuards(root)
	if len(cands) == 0 {
		return structuralGuard{}, false
	}
	acc := collectDataAccessesDeep(root, funcs, map[ssa.Value]bool{})
	if len(acc) == 0 {
		return structuralGuard{}, false
	}
	return findTiedMutex(cands, acc)
}

// checkHotGlobalAccesses refuses unsafe writes to hot globals (from this
// package's Fact). Also refuses init-goroutine writes (and unlocked reads of
// write-hot globals) to *other* packages' globals. Reads of frozen foreign
// globals (never listed as HotGlobals, or hot read-only) are allowed.
func (a *analyzer) checkHotGlobalAccesses(reported map[string]bool) {
	hot := a.localHot
	byName := map[string]HotGlobal{}
	if hot != nil {
		for _, g := range hot.Globals {
			byName[g.Name] = g
		}
	}
	allFuncs := map[*ssa.Function]bool{}
	for _, f := range a.funcs {
		if f != nil {
			allFuncs[f] = true
		}
	}
	// Local hot globals: require Fact mutex on post-init writers.
	for _, fn := range a.funcs {
		if fn == nil || isInitFunc(fn) {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				for _, gl := range a.globalWrites(instr) {
					if gl.Pkg != a.pkg {
						continue
					}
					hg, ok := byName[gl.Name()]
					if !ok || !hg.Mutex.set() {
						continue
					}
					roots := []sharedRoot{{val: gl, reason: "hot global"}}
					tied, ok := matchTiedGuard(roots, hg.Mutex)
					if ok {
						acc := []dataAccess{{instr: instr, addr: gl}}
						if hasTiedMutex([]structuralGuard{tied}, acc) {
							continue
						}
					}
					pos := instr.Pos()
					a.reportAt(reported, pos, "write to hot global %s (init-time concurrency) without its tied sync.Mutex guard", gl.Name())
				}
			}
		}
	}

	a.checkInitForeignGlobals(reported)
}

// checkInitForeignGlobals scans init-time goroutines (and init async
// callbacks) for accesses to globals defined in other packages.
//
// Reads of frozen foreign globals are OK: if the defining package never
// writes them after concurrency (no HotGlobals entry, or hot read-only),
// any reader may observe them. Writes, and reads of write-hot globals,
// require that package's HotGlobals Fact with a tied mutex held at the
// access — otherwise the sharing is untraced.
func (a *analyzer) checkInitForeignGlobals(reported map[string]bool) {
	if a.pass == nil || a.pkg == nil {
		return
	}
	for _, goro := range a.initGoroutineFuncs() {
		heldCache := map[*ssa.Function]map[ssa.Instruction]holdSet{}
		for fn := range goro {
			if fn == nil {
				continue
			}
			for _, b := range fn.Blocks {
				for _, instr := range b.Instrs {
					for _, gl := range foreignGlobalsTouched(instr, a.pkg) {
						if mutexOnlyTouch(instr, gl) {
							continue
						}
						a.refuseOrAllowForeignHot(gl, instr, heldCache, reported)
					}
				}
			}
		}
	}
}

func (a *analyzer) refuseOrAllowForeignHot(gl *ssa.Global, instr ssa.Instruction, heldCache map[*ssa.Function]map[ssa.Instruction]holdSet, reported map[string]bool) {
	pos := instr.Pos()
	name := gl.Name()
	pkgPath := ""
	if gl.Pkg != nil && gl.Pkg.Pkg != nil {
		pkgPath = gl.Pkg.Pkg.Path()
	}
	isWrite := instrWritesGlobal(instr, gl)
	// One diagnostic per foreign global (load+store of ++ would otherwise duplicate).
	once := func(format string, args ...any) {
		key := "foreignhot:" + pkgPath + "." + name
		if reported[key] {
			return
		}
		reported[key] = true
		a.reportAt(reported, pos, format, args...)
	}

	obj := gl.Object()
	if obj == nil || obj.Pkg() == nil {
		if !isWrite {
			return
		}
		once("init goroutine writes foreign global %s (%s) which is not traced by HotGlobals", name, pkgPath)
		return
	}
	if !token.IsExported(name) {
		if !isWrite {
			return
		}
		once("init goroutine writes unexported foreign global %s.%s", pkgPath, name)
		return
	}

	var hot HotGlobals
	imported := a.pass.ImportPackageFact(obj.Pkg(), &hot)
	var hg *HotGlobal
	if imported {
		for i := range hot.Globals {
			if hot.Globals[i].Name == name {
				hg = &hot.Globals[i]
				break
			}
		}
	}

	// Not hot in the defining package ⇒ init-then-freeze: reads OK from anywhere.
	if hg == nil {
		if !isWrite {
			return
		}
		once("init goroutine writes foreign global %s.%s (untraced cross-package write of a frozen global)", pkgPath, name)
		return
	}
	// Hot read-only: concurrent readers only; unlocked reads OK.
	if hg.Mode == ShareRead && !isWrite {
		return
	}
	if !hg.Mutex.set() {
		if isWrite {
			once("init goroutine writes foreign hot global %s.%s without a tied sync.Mutex in its HotGlobals Fact", pkgPath, name)
		} else {
			once("init goroutine reads foreign hot global %s.%s (write-mode) without a tied sync.Mutex in its HotGlobals Fact", pkgPath, name)
		}
		return
	}

	fn := instr.Parent()
	if fn == nil {
		once("init goroutine accesses foreign hot global %s.%s without a provable tied sync.Mutex hold", pkgPath, name)
		return
	}
	held, ok := heldCache[fn]
	if !ok {
		held = analyzeMustHold(fn)
		heldCache[fn] = held
	}
	at := held[instr]
	roots := []sharedRoot{{val: gl, reason: "foreign hot global"}}
	tied, ok := matchTiedGuard(roots, hg.Mutex)
	if !ok || at == nil {
		once("init goroutine accesses foreign hot global %s.%s without holding its tied sync.Mutex", pkgPath, name)
		return
	}
	acc := dataAccess{instr: instr, addr: globalAccessAddr(instr, gl), write: instrWritesGlobal(instr, gl)}
	if accessProtectedBy(acc, at, tied) {
		return
	}
	once("init goroutine accesses foreign hot global %s.%s without holding its tied sync.Mutex", pkgPath, name)
}

// instrWritesGlobal reports whether instr may mutate memory of gl.
func instrWritesGlobal(instr ssa.Instruction, gl *ssa.Global) bool {
	if gl == nil {
		return false
	}
	isGL := func(v ssa.Value) bool {
		return v != nil && (v == gl || stripToObject(v) == gl)
	}
	switch in := instr.(type) {
	case *ssa.Store:
		return isGL(in.Addr)
	case *ssa.MapUpdate:
		return isGL(in.Map)
	case *ssa.Call:
		return callWritesGlobal(in.Common(), isGL)
	case *ssa.Defer:
		return callWritesGlobal(in.Common(), isGL)
	case *ssa.Go:
		return callWritesGlobal(in.Common(), isGL)
	}
	return false
}

func callWritesGlobal(c *ssa.CallCommon, isGL func(ssa.Value) bool) bool {
	if c == nil {
		return false
	}
	if !c.IsInvoke() {
		if b, ok := c.Value.(*ssa.Builtin); ok {
			switch b.Name() {
			case "append", "copy", "clear", "delete":
				return len(c.Args) > 0 && isGL(c.Args[0])
			}
			return false
		}
	}
	if isMutexLockUnlockCall(c) {
		return false
	}
	for _, arg := range c.Args {
		if mayContainPointers(arg.Type()) && isGL(arg) {
			return true
		}
	}
	if c.IsInvoke() && mayContainPointers(c.Value.Type()) && isGL(c.Value) {
		return true
	}
	return false
}

func globalAccessAddr(instr ssa.Instruction, gl *ssa.Global) ssa.Value {
	switch in := instr.(type) {
	case *ssa.Store:
		if stripToObject(in.Addr) == gl || in.Addr == gl {
			return in.Addr
		}
	case *ssa.UnOp:
		if in.X == gl || stripToObject(in.X) == gl {
			return in.X
		}
	case *ssa.MapUpdate:
		if in.Map == gl || stripToObject(in.Map) == gl {
			return in.Map
		}
	}
	return gl
}

// mutexOnlyTouch reports whether instr only touches gl's sync.Mutex field
// for Lock/Unlock (the acquire itself must not require the lock to already
// be held).
func mutexOnlyTouch(instr ssa.Instruction, gl *ssa.Global) bool {
	switch in := instr.(type) {
	case *ssa.Call:
		return mutexLockUnlockOn(in.Common(), gl)
	case *ssa.Defer:
		return mutexLockUnlockOn(in.Common(), gl)
	case *ssa.Store:
		return isMutexFieldAddr(in.Addr) && stripToObject(in.Addr) == gl
	case *ssa.UnOp:
		return isMutexFieldAddr(in.X) && stripToObject(in.X) == gl
	case *ssa.FieldAddr:
		// Taking &g.mu to Lock/Unlock is not a data access of g.
		return isMutexFieldAddr(in) && stripToObject(in.X) == gl
	default:
		return false
	}
}

func mutexLockUnlockOn(c *ssa.CallCommon, gl *ssa.Global) bool {
	if c == nil || !isMutexLockUnlockCall(c) {
		return false
	}
	recv := mutexRecv(c)
	return recv != nil && stripToObject(recv) == gl
}

// foreignGlobalsTouched returns other-package globals that instr may read or write.
func foreignGlobalsTouched(instr ssa.Instruction, self *ssa.Package) []*ssa.Global {
	seen := map[*ssa.Global]bool{}
	var out []*ssa.Global
	add := func(v ssa.Value) {
		if v == nil {
			return
		}
		gl, ok := stripToObject(v).(*ssa.Global)
		if !ok || gl == nil {
			return
		}
		if gl.Pkg == self {
			return
		}
		if seen[gl] {
			return
		}
		seen[gl] = true
		out = append(out, gl)
	}
	switch in := instr.(type) {
	case *ssa.Store:
		add(in.Addr)
		add(in.Val)
	case *ssa.UnOp:
		add(in.X)
	case *ssa.MapUpdate:
		add(in.Map)
	case *ssa.Lookup:
		add(in.X)
	case *ssa.Range:
		add(in.X)
	case *ssa.Call, *ssa.Defer, *ssa.Go:
		var c *ssa.CallCommon
		switch x := instr.(type) {
		case *ssa.Call:
			c = x.Common()
		case *ssa.Defer:
			c = x.Common()
		case *ssa.Go:
			c = x.Common()
		}
		for _, arg := range c.Args {
			add(arg)
		}
		if c.IsInvoke() {
			add(c.Value)
		}
	default:
		for _, op := range instr.Operands(nil) {
			if op != nil {
				add(*op)
			}
		}
	}
	return out
}

// initGoroutineFuncs returns the sets of functions that may run in goroutines
// (or async callbacks) started from init.
func (a *analyzer) initGoroutineFuncs() []map[*ssa.Function]bool {
	var out []map[*ssa.Function]bool
	for _, fn := range a.funcs {
		if !isInitFunc(fn) {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				switch in := instr.(type) {
				case *ssa.Go:
					seeds := []*ssa.Function{}
					if cal := in.Common().StaticCallee(); cal != nil {
						seeds = append(seeds, cal)
					}
					if mc, ok := in.Common().Value.(*ssa.MakeClosure); ok {
						if clo, ok := mc.Fn.(*ssa.Function); ok {
							seeds = append(seeds, clo)
						}
					}
					goro := map[*ssa.Function]bool{}
					for _, s := range seeds {
						for f := range reachableFuncs(s, a.pkg) {
							goro[f] = true
						}
					}
					if len(goro) > 0 {
						out = append(out, goro)
					}
				default:
					if !a.isAsyncRegistration(instr) {
						continue
					}
					var c *ssa.CallCommon
					switch x := instr.(type) {
					case *ssa.Call:
						c = x.Common()
					case *ssa.Defer:
						c = x.Common()
					}
					if c == nil {
						continue
					}
					cal := c.StaticCallee()
					for _, i := range asyncCallbackIndices(cal) {
						if i < 0 || i >= len(c.Args) {
							continue
						}
						if mc, ok := c.Args[i].(*ssa.MakeClosure); ok {
							if clo, ok := mc.Fn.(*ssa.Function); ok {
								out = append(out, reachableFuncs(clo, a.pkg))
							}
						}
					}
				}
			}
		}
	}
	return out
}
