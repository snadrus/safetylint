package nosharing

import (
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// checkGlobalFreeze enforces an init-then-freeze discipline for package
// globals in packages that spawn goroutines: writes to globals are allowed
// only where they provably happen before any goroutine could be running —
// in init functions, in main (for package main), and in helpers called only
// from such contexts before the first spawn. Reads of frozen globals remain
// legal. Spawn points are go statements, dynamic/interface calls, Fact-bearing
// or curated stdlib spawners, and same-package functions that spawn — not
// every Fact-less cross-package call.
func (a *analyzer) checkGlobalFreeze(reported map[string]bool) {
	if a.pkg == nil || !a.packageSpawns() {
		return
	}

	spawner := a.computeSpawners()
	roots := a.freezeRoots()
	sites, goBody, addrTaken := a.callSiteIndex()

	infoCache := map[*ssa.Function]*spawnInfo{}
	info := func(fn *ssa.Function) *spawnInfo {
		si, ok := infoCache[fn]
		if !ok {
			si = a.maySpawnInfo(fn, spawner)
			infoCache[fn] = si
		}
		return si
	}

	// Fixpoint: functions callable only from pre-spawn program points.
	preOnly := map[*ssa.Function]bool{}
	for changed := true; changed; {
		changed = false
		for _, fn := range a.funcs {
			if fn == nil || preOnly[fn] || roots[fn] {
				continue
			}
			if !a.freezeEligible(fn, goBody, addrTaken) {
				continue
			}
			ok := true
			for _, s := range sites[fn] {
				if !roots[s.caller] && !preOnly[s.caller] {
					ok = false
					break
				}
				ci := info(s.caller)
				if s.isDefer {
					if ci.postAtExit {
						ok = false
						break
					}
				} else if ci.post[s.instr] {
					ok = false
					break
				}
			}
			if ok {
				preOnly[fn] = true
				changed = true
			}
		}
	}

	allFuncs := map[*ssa.Function]bool{}
	for _, f := range a.funcs {
		if f != nil {
			allFuncs[f] = true
		}
	}
	// 0 = unknown, 1 = proven mutex-guarded, 2 = not guarded.
	mutexOK := map[*ssa.Global]int{}

	for _, fn := range a.funcs {
		if fn == nil {
			continue
		}
		allowedCtx := roots[fn] || preOnly[fn]
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				gls := a.globalWrites(instr)
				if len(gls) == 0 {
					continue
				}
				if allowedCtx {
					fi := info(fn)
					post := false
					switch instr.(type) {
					case *ssa.Go:
						post = true // the write happens inside the goroutine
					case *ssa.Defer:
						post = fi.postAtExit
					default:
						post = fi.post[instr]
					}
					if !post {
						continue
					}
				}
				for _, gl := range gls {
					// InitOnly helpers may write globals; callers are restricted
					// to init/var initializers via checkInitOnlyCalls.
					if a.isInitOnlyFunc(fn) {
						continue
					}
					if mutexOK[gl] == 0 {
						if mutexGuardsAccesses(gl, allFuncs) {
							mutexOK[gl] = 1
						} else {
							mutexOK[gl] = 2
						}
					}
					if mutexOK[gl] == 1 {
						continue
					}
					pos := instr.Pos()
					if !pos.IsValid() {
						pos = fn.Pos()
					}
					a.reportAt(reported, pos, "write to package global %s after goroutines may have started (globals are frozen once concurrency begins)", gl.Name())
				}
			}
		}
	}
}

func (a *analyzer) packageSpawns() bool {
	spawner := a.computeSpawners()
	for _, fn := range a.funcs {
		if fn == nil {
			continue
		}
		if spawner[fn] {
			return true
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				if a.isSpawnEvent(instr, spawner) {
					return true
				}
			}
		}
	}
	return false
}

// freezeRoots returns functions whose bodies start pre-spawn: init functions
// and, for package main, the main function.
func (a *analyzer) freezeRoots() map[*ssa.Function]bool {
	roots := map[*ssa.Function]bool{}
	isMainPkg := a.pkg.Pkg.Name() == "main"
	for _, fn := range a.funcs {
		if fn == nil || fn.Parent() != nil {
			continue
		}
		name := fn.Name()
		if name == "init" || strings.HasPrefix(name, "init#") {
			roots[fn] = true
		}
		if isMainPkg && name == "main" && fn.Signature.Recv() == nil {
			roots[fn] = true
		}
	}
	return roots
}

func (a *analyzer) freezeEligible(fn *ssa.Function, goBody, addrTaken map[*ssa.Function]bool) bool {
	if fn.Signature.Recv() != nil {
		// Methods may be reached via interfaces we cannot see.
		return false
	}
	if goBody[fn] || addrTaken[fn] {
		return false
	}
	if a.pkg.Pkg.Name() == "main" {
		return true
	}
	// In a library, exported functions can be called from other packages at
	// any time, including after that caller spawned goroutines.
	obj := fn.Object()
	return obj == nil || !obj.Exported()
}

type freezeSite struct {
	caller  *ssa.Function
	instr   ssa.Instruction
	isDefer bool
}

// callSiteIndex records, for every function in the package: its static call
// sites, whether it is used as a goroutine body, and whether its address is
// taken (stored, passed as a value, etc.).
func (a *analyzer) callSiteIndex() (map[*ssa.Function][]freezeSite, map[*ssa.Function]bool, map[*ssa.Function]bool) {
	sites := map[*ssa.Function][]freezeSite{}
	goBody := map[*ssa.Function]bool{}
	addrTaken := map[*ssa.Function]bool{}
	inPkg := func(f *ssa.Function) bool { return f != nil && f.Pkg == a.pkg }

	for _, fn := range a.funcs {
		if fn == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				switch in := instr.(type) {
				case *ssa.Call:
					if cal := in.Common().StaticCallee(); inPkg(cal) {
						sites[cal] = append(sites[cal], freezeSite{caller: fn, instr: instr, isDefer: false})
					}
				case *ssa.Defer:
					if cal := in.Common().StaticCallee(); inPkg(cal) {
						sites[cal] = append(sites[cal], freezeSite{caller: fn, instr: instr, isDefer: true})
					}
				case *ssa.Go:
					if cal := in.Common().StaticCallee(); inPkg(cal) {
						goBody[cal] = true
					}
				case *ssa.MakeClosure:
					f, _ := in.Fn.(*ssa.Function)
					if !inPkg(f) {
						break
					}
					refs := in.Referrers()
					if refs == nil {
						addrTaken[f] = true
						break
					}
					for _, r := range *refs {
						switch rc := r.(type) {
						case *ssa.Call:
							if rc.Common().Value != in {
								addrTaken[f] = true
							}
						case *ssa.Defer:
							if rc.Common().Value != in {
								addrTaken[f] = true
							}
						case *ssa.Go:
							if rc.Common().Value == in {
								goBody[f] = true
							} else {
								addrTaken[f] = true
							}
						default:
							addrTaken[f] = true
						}
					}
				}
				// Plain function values used outside callee position.
				if _, isMC := instr.(*ssa.MakeClosure); isMC {
					continue
				}
				for _, op := range instr.Operands(nil) {
					if op == nil || *op == nil {
						continue
					}
					f, ok := (*op).(*ssa.Function)
					if !ok || !inPkg(f) {
						continue
					}
					if isCalleePosition(instr, f) {
						continue
					}
					addrTaken[f] = true
				}
			}
		}
	}
	return sites, goBody, addrTaken
}

func isCalleePosition(instr ssa.Instruction, f *ssa.Function) bool {
	var c *ssa.CallCommon
	switch in := instr.(type) {
	case *ssa.Call:
		c = in.Common()
	case *ssa.Defer:
		c = in.Common()
	case *ssa.Go:
		c = in.Common()
	default:
		return false
	}
	return c.Value == f
}

// computeSpawners finds functions that may start concurrency when called:
// they contain a go statement, a dynamic/interface call, a Fact-bearing or
// curated spawner, or call another spawner.
func (a *analyzer) computeSpawners() map[*ssa.Function]bool {
	sp := map[*ssa.Function]bool{}
	for changed := true; changed; {
		changed = false
		for _, fn := range a.funcs {
			if fn == nil || sp[fn] {
				continue
			}
		scan:
			for _, b := range fn.Blocks {
				for _, instr := range b.Instrs {
					if a.isSpawnEvent(instr, sp) {
						sp[fn] = true
						changed = true
						break scan
					}
				}
			}
		}
	}
	return sp
}

func (a *analyzer) isSpawnEvent(instr ssa.Instruction, spawner map[*ssa.Function]bool) bool {
	if a.isAsyncRegistration(instr) {
		return true
	}
	var c *ssa.CallCommon
	switch in := instr.(type) {
	case *ssa.Go:
		return true
	case *ssa.Call:
		c = in.Common()
	case *ssa.Defer:
		c = in.Common()
	default:
		return false
	}
	if !c.IsInvoke() {
		if _, ok := c.Value.(*ssa.Builtin); ok {
			return false
		}
	}
	if c.IsInvoke() {
		// Interface method: callee unknown; may start concurrency.
		return true
	}
	callee := c.StaticCallee()
	if callee == nil {
		return true
	}
	if callee.Pkg == a.pkg {
		return spawner[callee]
	}
	// Cross-package: sync primitives cannot start user goroutines.
	switch calleePkgPath(callee) {
	case "sync", "sync/atomic":
		return false
	}
	// Fact-bearing spawners (MaySpawn / MayShareParams) are spawn points.
	if a.importMaySpawn(funcObject(callee)) {
		return true
	}
	// Curated stdlib APIs known to serve/accept in new goroutines.
	if isCuratedSpawn(callee) {
		return true
	}
	// Fact-less cross-package call: do not freeze. Absence of a Fact means
	// unknown retention for sharing, not "a goroutine must have started".
	return false
}

func calleePkgPath(fn *ssa.Function) string {
	if fn.Pkg != nil && fn.Pkg.Pkg != nil {
		return fn.Pkg.Pkg.Path()
	}
	if obj := fn.Object(); obj != nil && obj.Pkg() != nil {
		return obj.Pkg().Path()
	}
	return ""
}

type spawnInfo struct {
	post       map[ssa.Instruction]bool // concurrency may exist before instr
	postAtExit bool                     // concurrency may exist at function exit
}

// maySpawnInfo runs a forward may-analysis over fn's CFG: a program point is
// "post" once any path from entry to it passes a spawn event.
//
// If package init already started concurrency, non-init functions (including
// main) begin already post — init goroutines may still be running.
func (a *analyzer) maySpawnInfo(fn *ssa.Function, spawner map[*ssa.Function]bool) *spawnInfo {
	si := &spawnInfo{post: map[ssa.Instruction]bool{}}
	n := len(fn.Blocks)
	if n == 0 {
		return si
	}
	entryPost := a.initConcurrent && !isInitFunc(fn)
	in := make([]bool, n)
	out := make([]bool, n)
	for changed := true; changed; {
		changed = false
		for _, b := range fn.Blocks {
			newIn := false
			if len(b.Preds) == 0 {
				newIn = entryPost
			} else {
				for _, p := range b.Preds {
					if out[p.Index] {
						newIn = true
						break
					}
				}
			}
			if newIn != in[b.Index] {
				in[b.Index] = newIn
				changed = true
			}
			cur := newIn
			for _, instr := range b.Instrs {
				si.post[instr] = cur
				if a.isSpawnEvent(instr, spawner) {
					cur = true
				}
			}
			if cur != out[b.Index] {
				out[b.Index] = cur
				changed = true
			}
		}
	}
	for _, b := range fn.Blocks {
		if len(b.Instrs) == 0 {
			continue
		}
		switch b.Instrs[len(b.Instrs)-1].(type) {
		case *ssa.Return, *ssa.Panic:
			if out[b.Index] {
				si.postAtExit = true
			}
		}
	}
	if entryPost {
		si.postAtExit = true
	}
	return si
}

// globalWrites returns the package globals whose memory the instruction may
// mutate (direct store/map-update/append, or a same-package callee that writes
// them). Reading a frozen global — including passing a loaded copy to another
// function — is not a write. Passing the address of the global variable to an
// unknown callee is still treated as a potential write.
func (a *analyzer) globalWrites(instr ssa.Instruction) []*ssa.Global {
	var out []*ssa.Global
	globalOf := func(v ssa.Value) *ssa.Global {
		if v == nil {
			return nil
		}
		if gl, ok := stripToObject(v).(*ssa.Global); ok && gl.Pkg == a.pkg {
			return gl
		}
		return nil
	}
	switch in := instr.(type) {
	case *ssa.Store:
		if gl := globalOf(in.Addr); gl != nil {
			out = append(out, gl)
		}
	case *ssa.MapUpdate:
		if gl := globalOf(in.Map); gl != nil {
			out = append(out, gl)
		}
	case *ssa.Call:
		out = append(out, a.globalWritesCall(in.Common())...)
	case *ssa.Defer:
		out = append(out, a.globalWritesCall(in.Common())...)
	case *ssa.Go:
		out = append(out, a.globalWritesCall(in.Common())...)
	}
	return out
}

func (a *analyzer) globalWritesCall(c *ssa.CallCommon) []*ssa.Global {
	var out []*ssa.Global
	if c == nil {
		return out
	}

	if !c.IsInvoke() {
		if b, ok := c.Value.(*ssa.Builtin); ok {
			switch b.Name() {
			case "append", "copy", "clear", "delete":
				if len(c.Args) > 0 {
					if gl, ok := stripToObject(c.Args[0]).(*ssa.Global); ok && gl.Pkg == a.pkg {
						out = append(out, gl)
					}
				}
			}
			return out
		}
	}

	callee := c.StaticCallee()
	if callee == nil {
		// Interface/dynamic call: do not treat an invoke on a loaded interface
		// global (e.g. err.Error()) as writing the global binding. Map/slice
		// headers still count via globalWritesCallArgs(..., false).
		return a.globalWritesCallArgs(c, false)
	}
	if isSyncMutexMethod(callee) || isRWMutexMethod(callee) {
		return nil
	}
	if isWhitelistedSyncMethod(callee, recvOfCall(c)) {
		return nil
	}
	if isAtomicCallee(callee) || isStdlibReadOnlyCall(callee) {
		return nil
	}
	if callee.Pkg == a.pkg && len(callee.Blocks) > 0 {
		for i, arg := range c.Args {
			if !mayContainPointers(arg.Type()) {
				continue
			}
			gl, ok := stripToObject(arg).(*ssa.Global)
			if !ok || gl.Pkg != a.pkg || i >= len(callee.Params) {
				continue
			}
			if a.writtenIn(callee.Params[i], map[*ssa.Function]bool{callee: true}, true) {
				out = append(out, gl)
			}
		}
		return out
	}
	// Cross-package or bodyless: only args that expose the global cell or a
	// mutable map/slice header loaded from it.
	return a.globalWritesCallArgs(c, false)
}

// globalWritesCallArgs collects globals that may be mutated via call arguments.
// When conservative is true (dynamic callee), treat any pointer-ish derived
// global the same as address-of-global.
func (a *analyzer) globalWritesCallArgs(c *ssa.CallCommon, conservative bool) []*ssa.Global {
	var out []*ssa.Global
	seen := map[*ssa.Global]bool{}
	add := func(gl *ssa.Global) {
		if gl == nil || gl.Pkg != a.pkg || seen[gl] {
			return
		}
		seen[gl] = true
		out = append(out, gl)
	}
	consider := func(arg ssa.Value) {
		if arg == nil {
			return
		}
		if gl, ok := arg.(*ssa.Global); ok {
			add(gl)
			return
		}
		if u, ok := arg.(*ssa.UnOp); ok && u.Op == token.MUL {
			gl, ok := stripToObject(u.X).(*ssa.Global)
			if !ok || gl.Pkg != a.pkg {
				return
			}
			if conservative || isMapOrSliceType(u.Type()) {
				add(gl)
			}
			return
		}
		if conservative {
			if gl, ok := stripToObject(arg).(*ssa.Global); ok {
				add(gl)
			}
		}
	}
	for _, arg := range c.Args {
		consider(arg)
	}
	if c.IsInvoke() {
		consider(c.Value)
	}
	return out
}

func isMapOrSliceType(t types.Type) bool {
	if t == nil {
		return false
	}
	switch t.Underlying().(type) {
	case *types.Map, *types.Slice:
		return true
	}
	return false
}
