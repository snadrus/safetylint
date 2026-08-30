// Package nosharing refuses cross-goroutine memory sharing except via
// channels with freeze-after-send semantics for pointer-carrying values,
// or via a proven lock/atomic/partition guard (north-star cascade).
//
// Provably read-only sharing is allowed. sync.WaitGroup, sync.Once,
// sync.Mutex (as a lock object), and sync/atomic cells are whitelisted as
// pure synchronization.
// TryLock/TryRLock only count on paths where their boolean result is proven true.
//
// Cross-package: functions that spawn and retain parameters export
// MayShareParams Facts; callers must not write those values after the call
// unless under a proven guard.
//
// Package globals follow an init-then-freeze discipline: in packages that
// spawn goroutines, globals may only be written before any goroutine could
// be running (init functions, main before the first spawn, and helpers
// provably called only from such points), or under a proven guard.
package nosharing

import (
	"fmt"
	"go/build"
	"go/token"
	"go/types"
	"os"
	"strings"

	"safetylint/internal/nolint"
	"safetylint/internal/toolver"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/ssa"
)

const Doc = `refuse memory shared between goroutines except via frozen channels

The nosharing analyzer proves the absence of data races under a channel-only
sharing discipline, with proven lock / atomic / partition exceptions:

  - Memory shared with a goroutine via capture, argument, or global must be
    provably read-only,     be *sync.WaitGroup / *sync.Once / *sync.Mutex / sync/atomic, be
    context.Context / *net/http.Server (stdlib concurrent protocols), or pass
    the guard cascade: tied sync.Mutex field (including Lock/Unlock/RLock/
    RUnlock methods that always wrap that field); free-standing package
    Mutex/RWMutex held at every touch; RWMutex Lock writes / Lock|RLock reads;
    concurrent touches only via sync/atomic; const-index or disjoint-range
    partitioned slice writers; field-partitioned mutexes (one consistent
    mutex per field); or exclusive buffer-pool checkout via a token channel.
    Unlocked reads of fields never written after construction/first-share
    do not poison the proof. Sharing an object is also OK when every access
    in the goroutine (and proven callees) is a read of init-frozen data, a
    tied-mutex access, an atomic, a channel send/recv, or a WaitGroup/Once/
    Mutex op — init writes before the share do not count. Passing *T into
    an interface method is not a write of *T unless the method looks like a
    setter. A method call is not a write of the object if intra-package
    analysis (or WritesParams) shows the method does not write the receiver
    except under its mutex / via atomics / channels. TryLock/TryRLock only
    count on proven-true paths. Unlock-on-cancel helpers and websocket
    one-reader one-writer pairs are recognized narrowly.
  - Values may transfer between goroutines through channels. If a sent value
    contains pointers, those pointees are frozen after send: no further writes
    through the sender's or receiver's view of that memory are allowed.
  - In packages that spawn goroutines, package globals are frozen once
    concurrency may have begun: writes are allowed only in init functions,
    in main before the first spawn point (go, dynamic/interface call,
    Fact-bearing or curated stdlib spawner), in unexported helpers
    provably called only from such points, under a proven guard, or inside
    InitOnly registration helpers (callers must be init/var initializers).
    Reads of frozen globals remain legal.
  - Exported functions that spawn and retain parameters publish MayShareParams
    Facts. Call sites must treat those arguments as shared: no post-call
    writes unless under a proven guard. Wrappers re-export Facts.
  - Curated async stdlib APIs (time.AfterFunc, http.HandleFunc, …) spawn
    their callback like go: the body is a goroutine (existing share/lock
    rules) and passing the closure does not drop the caller's mutex hold.
    GOROOT callees are not writes unless listed in curatedWriters;
    curatedRetains is the GOROOT equivalent of MayShareParams (callback
    slots are spawns, not retains). Toolchains newer than this tool's
    verified Go version warn that new standard funcs may be unverified.
  - If init starts concurrency, main and other non-init code are already
    post-spawn for global freeze. Globals touched by init goroutines are
    published as package HotGlobals Facts (optional tied mutex). Init
    goroutines may read other packages' frozen globals; writes and reads of
    write-hot globals require that package's HotGlobals tied mutex held.
`

var Analyzer = &analysis.Analyzer{
	Name:     "nosharing",
	Doc:      Doc,
	Requires: []*analysis.Analyzer{buildssa.Analyzer},
	Run:      run,
	FactTypes: []analysis.Fact{
		new(MayShareParams),
		new(MaySpawn),
		new(InitOnly),
		new(HotGlobals),
		new(WritesParams),
	},
}

// factsOff is set when FactTypes is cleared (SAFETYLINT_NO_FACTS=1). Kept
// separate from Analyzer to avoid an initialization cycle (Analyzer.Run
// eventually calls factsEnabled).
var factsOff bool

func init() {
	// SAFETYLINT_NO_FACTS=1 skips FactTypes so unitchecker only analyzes the
	// packages named on the command line (no SSA of the whole module cache).
	// Cross-package Facts are unavailable in this mode.
	if os.Getenv("SAFETYLINT_NO_FACTS") == "1" {
		Analyzer.FactTypes = nil
		factsOff = true
	}
}

func run(pass *analysis.Pass) (any, error) {
	// FactTypes causes this analyzer to run on all transitive imports.
	// Skip GOROOT packages entirely. Module-cache packages only export
	// WritesParams Facts so callers can evaluate third-party pointer calls
	// without running the full (pathological) sharing analysis there.
	if packageInGOROOT(pass) {
		return nil, nil
	}
	if only := os.Getenv("SAFETYLINT_ONLY"); only != "" {
		path := pass.Pkg.Path()
		if path != only && !strings.HasPrefix(path, only+"/") {
			return nil, nil
		}
	}
	toolver.WarnIfTooNew(pass)
	if os.Getenv("SAFETYLINT_DEBUG") == "1" {
		fmt.Fprintf(os.Stderr, "nosharing: analyzing %s facts=%v\n", pass.Pkg.Path(), factsEnabled())
	}
	ssainfo := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA)
	a := &analyzer{
		pass:          pass,
		pkg:           ssainfo.Pkg,
		funcs:         ssainfo.SrcFuncs,
		localShare:    map[*types.Func]*MayShareParams{},
		localSpawn:    map[*types.Func]bool{},
		localInitOnly: map[*types.Func]bool{},
	}
	if packageInModuleCache(pass) {
		a.exportParamWriteFacts()
		return nil, nil
	}
	a.run()
	if os.Getenv("SAFETYLINT_DEBUG") == "1" {
		fmt.Fprintf(os.Stderr, "nosharing: done %s\n", pass.Pkg.Path())
	}
	return nil, nil
}

func packageInGOROOT(pass *analysis.Pass) bool {
	if pass == nil || len(pass.Files) == 0 {
		return false
	}
	file := pass.Fset.Position(pass.Files[0].Pos()).Filename
	if file == "" {
		return false
	}
	goroot := build.Default.GOROOT
	if goroot == "" {
		return false
	}
	// Normalize for prefix check.
	return strings.HasPrefix(file, goroot) || strings.Contains(file, "/go/src/")
}

func packageInModuleCache(pass *analysis.Pass) bool {
	if pass == nil || len(pass.Files) == 0 {
		return false
	}
	file := pass.Fset.Position(pass.Files[0].Pos()).Filename
	if file == "" {
		return false
	}
	// GOPATH/pkg/mod and the module cache under $HOME/go/pkg/mod, etc.
	return strings.Contains(file, "/pkg/mod/") || strings.Contains(file, `\pkg\mod\`)
}

type analyzer struct {
	pass           *analysis.Pass
	pkg            *ssa.Package
	funcs          []*ssa.Function
	localShare     map[*types.Func]*MayShareParams
	localSpawn     map[*types.Func]bool
	localInitOnly  map[*types.Func]bool
	localHot       *HotGlobals
	initConcurrent bool
	onceOK         map[*ssa.Global]bool
}

func (a *analyzer) run() {
	globals := make(map[*ssa.Global]bool)
	if a.pkg != nil {
		for _, m := range a.pkg.Members {
			if g, ok := m.(*ssa.Global); ok {
				globals[g] = true
			}
		}
	}

	reported := map[string]bool{}

	for _, fn := range a.funcs {
		if fn == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				if goInstr, ok := instr.(*ssa.Go); ok {
					a.checkGo(goInstr, fn, globals, reported)
				}
			}
		}
	}

	for _, fn := range a.funcs {
		if fn == nil {
			continue
		}
		a.checkChannelFreeze(fn, reported)
	}

	// Discover InitOnly helpers even when Facts are off (SAFETYLINT_NO_FACTS):
	// same-package registry writers like Reg() must still be allowed to
	// mutate frozen globals. Cross-package InitOnly still needs Facts.
	a.discoverInitOnlyHelpers()
	if factsEnabled() {
		a.exportShareFacts()
		a.exportInitOnlyFacts()
		a.exportHotGlobals()
	}
	a.checkShareFactCalls(reported)
	for _, fn := range a.funcs {
		if fn == nil {
			continue
		}
		a.checkAsyncSpawns(fn, globals, reported)
	}
	a.checkInitOnlyCalls(reported)
	a.checkGlobalFreeze(reported)
	a.checkHotGlobalAccesses(reported)
}

func factsEnabled() bool {
	return !factsOff && os.Getenv("SAFETYLINT_NO_FACTS") != "1"
}

func (a *analyzer) exportObjectFact(obj types.Object, fact analysis.Fact) {
	if a == nil || a.pass == nil || obj == nil || fact == nil || !factsEnabled() {
		return
	}
	a.pass.ExportObjectFact(obj, fact)
}

func (a *analyzer) exportPackageFact(fact analysis.Fact) {
	if a == nil || a.pass == nil || fact == nil || !factsEnabled() {
		return
	}
	a.pass.ExportPackageFact(fact)
}

func (a *analyzer) reportAt(reported map[string]bool, pos token.Pos, format string, args ...any) {
	if a == nil || a.pass == nil {
		return
	}
	if nolint.Suppressed(a.pass, pos, "nosharing") {
		return
	}
	msg := fmt.Sprintf(format, args...)
	key := fmt.Sprintf("%d:%s", pos, msg)
	if reported[key] {
		return
	}
	reported[key] = true
	a.pass.Reportf(pos, "%s", msg)
}

func (a *analyzer) checkGo(g *ssa.Go, spawner *ssa.Function, globals map[*ssa.Global]bool, reported map[string]bool) {
	callees := a.goCallees(g)
	if len(callees) == 0 {
		a.reportAt(reported, g.Pos(), "goroutine with non-static callee: cannot prove memory safety")
		return
	}
	var value ssa.Value
	var args []ssa.Value
	if c := g.Common(); c != nil {
		value = c.Value
		args = c.Args
	}
	for _, callee := range callees {
		a.checkGoCallee(g, value, args, spawner, callee, globals, reported)
	}
}

func (a *analyzer) checkGoCallee(instr ssa.Instruction, value ssa.Value, args []ssa.Value, spawner, callee *ssa.Function, globals map[*ssa.Global]bool, reported map[string]bool) {
	origRoots := collectRoots(value, args, callee, globals)
	var kept []sharedRoot
	for _, r := range origRoots {
		if isChanValueCopy(r.val) {
			continue
		}
		kept = append(kept, r)
	}
	origRoots = kept
	// Alias expansion is spawn-scoped so every *T in the package does not
	// merge. Access collection still scans the package (allFuncs) so a
	// later unlocked write of this cell (bad_trylock) is not missed —
	// but only own fields, and sibling fields this spawn never touches
	// are filtered (considerWork must not poison keepalive).
	allFuncs := map[*ssa.Function]bool{}
	for _, f := range a.funcs {
		if f != nil {
			allFuncs[f] = true
		}
	}
	scope := a.spawnShareScope(spawner, callee, instr, origRoots, globals)
	roots := expandRootAliases(origRoots, scope)
	thisShare := map[ssa.Value]bool{}
	for _, o := range origRoots {
		for _, p := range sameObjectPeersGo(o.val, roots, spawner, instr, allFuncs) {
			thisShare[p.val] = true
		}
	}
	preShare := a.funcsHappensBeforeGo(spawner, instr)
	reachable := reachableFuncs(callee, callee.Pkg)
	seen := map[string]bool{}

	for _, root := range roots {
		if root.val != nil && len(thisShare) > 0 && !thisShare[root.val] {
			continue
		}
		if isChanType(root.val.Type()) || isChanValueCopy(root.val) {
			continue
		}
		if isWhitelistedSync(root.val) || isSyncMutex(root.val) || isSyncRWMutex(root.val) || isShareSafeStdlib(root.val) || isHarmonyDBType(root.val.Type()) {
			// WaitGroup/Once/Mutex/RWMutex objects are pure synchronization.
			// context.Context and *http.Server are stdlib-safe to share.
			continue
		}
		if isGlobalObject(root.val) {
			// Globals are covered package-wide by the init-then-freeze
			// analysis (checkGlobalFreeze), which also reports writes in
			// functions not reachable from any go statement.
			continue
		}
		if !mayContainPointers(root.val.Type()) && !isAddressableShared(root.val) {
			continue
		}

		ctor := allocParentOf(root.val, spawner, instr)
		writtenInGoro := a.writtenIn(root.val, reachable, true)
		writtenAfter := isWrittenAfterGoScoped(root.val, spawner, instr, allFuncs, reachable, ctor)
		// Struct/array value snapshots captured into a goroutine and only
		// read there: ignore spawner reassignment of the capture cell
		// (loop-local map lookups under a lock, Go 1.22+ per-iteration vars).
		if writtenAfter && !writtenInGoro && valueSnapshotReadOnly(root.val, spawner, instr) {
			writtenAfter = false
		}
		// WaitGroup fan-out/join: exclusive ownership of result locals after Wait
		// (also read-only captures while workers run). Only this root's cell —
		// not same-type sharePeers — may prove the join (same-type peers are
		// unrelated locals and would unsoundly silence races).
		if (writtenInGoro || writtenAfter) && waitGroupExclusiveOK(root.val, spawner, instr, reachable) {
			continue
		}
		// WaitGroup + free-standing/local mutex during the fan-out; post-Wait
		// parent access is exclusive (treed_build / apiinfo healthyLk).
		if (writtenInGoro || writtenAfter) && waitGroupMutexOK(root.val, spawner, instr, reachable) {
			continue
		}
		if cancelUnlockerOK(root.val, callee) {
			continue
		}
		if bufferPoolCheckoutOK(root.val, spawner, instr, reachable) {
			continue
		}
		if lockedStackCheckoutOK(root.val, spawner, instr, reachable) {
			continue
		}
		if singleGoroOwnOK(root.val, spawner, instr, reachable, allFuncs, ctor, preShare) {
			continue
		}
		if websocketPairOK(sameObjectPeersGo(root.val, roots, spawner, instr, allFuncs), spawner, instr, reachable) {
			continue
		}

		if writtenInGoro || writtenAfter {
			// Atomics-only first on this root alone (denylist Filter): peer
			// unions must not demand a mutex for pure atomic.Pointer cells.
			if atomicsOnlyAccesses(collectOwnDataAccessesDeep(root.val, allFuncs, map[ssa.Value]bool{})) {
				for _, r := range roots {
					seen["write:"+typeKey(r.val)] = true
				}
				continue
			}
			// Same-object peers only (not same-type). Own-field accesses;
			// constructor / pre-go stores and other-field sibling writes ignored.
			peers := sameObjectPeersGo(root.val, roots, spawner, instr, allFuncs)
			peers = appendShareAliases(peers, root.val, allFuncs)
			if objectGuardedOwnRootsAfter(peers, allFuncs, spawner, instr, preShare, reachable) {
				for _, r := range roots {
					seen["write:"+typeKey(r.val)] = true
				}
				continue
			}
			key := "write:" + typeKey(root.val)
			if seen[key] {
				continue
			}
			seen[key] = true
			a.reportAt(reported, instr.Pos(), "shared memory %s written without channel transfer and no proven lock/atomic/partition guard (%s)",
				root.describe(), root.reason)
		}
	}
}

type sharedRoot struct {
	val    ssa.Value
	reason string
}

func (r sharedRoot) describe() string {
	if r.val == nil {
		return "<nil>"
	}
	if n, ok := r.val.(interface{ Name() string }); ok {
		return fmt.Sprintf("%s (%s)", n.Name(), r.val.Type())
	}
	return r.val.String()
}

func appendShareAliases(peers []sharedRoot, focus ssa.Value, funcs map[*ssa.Function]bool) []sharedRoot {
	seen := map[ssa.Value]bool{}
	for _, p := range peers {
		if p.val != nil {
			seen[p.val] = true
		}
	}
	for _, x := range siblingShareAliases(focus, funcs) {
		if x == nil || seen[x] {
			continue
		}
		seen[x] = true
		peers = append(peers, sharedRoot{val: x, reason: "share alias"})
	}
	return peers
}

func staticCallee(c *ssa.CallCommon) *ssa.Function {
	if c.IsInvoke() {
		return nil
	}
	return c.StaticCallee()
}

func typeKey(v ssa.Value) string {
	return v.Type().String()
}

func isChanType(t types.Type) bool {
	t = types.Unalias(t)
	if p, ok := t.(*types.Pointer); ok {
		t = types.Unalias(p.Elem())
	}
	_, ok := t.(*types.Chan)
	return ok
}
