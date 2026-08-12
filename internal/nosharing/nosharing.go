// Package nosharing refuses cross-goroutine memory sharing except via
// channels with freeze-after-send semantics for pointer-carrying values,
// or via a proven lock/atomic/partition guard (north-star cascade).
//
// Provably read-only sharing is allowed. sync.WaitGroup, sync.Once, and
// sync.Mutex (as a lock object) are whitelisted as pure synchronization.
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

	"safetylint/internal/toolver"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/ssa"
)

const Doc = `refuse memory shared between goroutines except via frozen channels

The nosharing analyzer proves the absence of data races under a channel-only
sharing discipline, with proven lock / atomic / partition exceptions:

  - Memory shared with a goroutine via capture, argument, or global must be
    provably read-only, be *sync.WaitGroup / *sync.Once / *sync.Mutex, be
    context.Context / *net/http.Server (stdlib concurrent protocols), or pass
    the guard cascade: tied sync.Mutex field; free-standing package
    Mutex/RWMutex held at every touch; RWMutex Lock writes / Lock|RLock reads;
    concurrent touches only via sync/atomic; or const-index partitioned
    slice/array writers. TryLock/TryRLock only count on proven-true paths.
  - Values may transfer between goroutines through channels. If a sent value
    contains pointers, those pointees are frozen after send: no further writes
    through the sender's or receiver's view of that memory are allowed.
  - In packages that spawn goroutines, package globals are frozen once
    concurrency may have begun: writes are allowed only in init functions,
    in main before the first spawn point (go, dynamic/interface call,
    Fact-bearing or curated stdlib spawner), in unexported helpers
    provably called only from such points, or under a proven guard.
    Reads of frozen globals remain legal.
  - Exported functions that spawn and retain parameters publish MayShareParams
    Facts. Call sites must treat those arguments as shared: no post-call
    writes unless under a proven guard. Wrappers re-export Facts.
  - Curated async stdlib APIs (time.AfterFunc, http.HandleFunc, …) share
    closure captures like Fact-bearing calls. Toolchains newer than this
    tool's verified Go version warn that new standard funcs may be unverified.
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
		new(HotGlobals),
		new(WritesParams),
	},
}

func init() {
	// SAFETYLINT_NO_FACTS=1 skips FactTypes so unitchecker only analyzes the
	// packages named on the command line (no SSA of the whole module cache).
	// Cross-package Facts are unavailable in this mode.
	if os.Getenv("SAFETYLINT_NO_FACTS") == "1" {
		Analyzer.FactTypes = nil
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
		pass:       pass,
		pkg:        ssainfo.Pkg,
		funcs:      ssainfo.SrcFuncs,
		localShare: map[*types.Func]*MayShareParams{},
		localSpawn: map[*types.Func]bool{},
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
	localHot       *HotGlobals
	initConcurrent bool
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
				goInstr, ok := instr.(*ssa.Go)
				if !ok {
					continue
				}
				a.checkGo(goInstr, fn, globals, reported)
			}
		}
	}

	for _, fn := range a.funcs {
		if fn == nil {
			continue
		}
		a.checkChannelFreeze(fn, reported)
	}

	if factsEnabled() {
		a.exportShareFacts()
		a.exportHotGlobals()
	}
	a.checkShareFactCalls(reported)
	a.checkAsyncCallbackShares(reported)
	a.checkGlobalFreeze(reported)
	a.checkHotGlobalAccesses(reported)
}

func factsEnabled() bool {
	return os.Getenv("SAFETYLINT_NO_FACTS") != "1"
}

func (a *analyzer) reportAt(reported map[string]bool, pos token.Pos, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	key := fmt.Sprintf("%d:%s", pos, msg)
	if reported[key] {
		return
	}
	reported[key] = true
	a.pass.Reportf(pos, "%s", msg)
}

func (a *analyzer) checkGo(g *ssa.Go, spawner *ssa.Function, globals map[*ssa.Global]bool, reported map[string]bool) {
	common := g.Common()
	callee := staticCallee(common)
	if callee == nil {
		a.reportAt(reported, g.Pos(), "goroutine with non-static callee: cannot prove memory safety")
		return
	}

	roots := collectRoots(g, callee, globals)
	allFuncs := map[*ssa.Function]bool{}
	for _, f := range a.funcs {
		if f != nil {
			allFuncs[f] = true
		}
	}
	roots = expandRootAliases(roots, allFuncs)
	// Explore the callee's package so cross-package go targets (e.g. s.Run)
	// still see writes inside the started goroutine.
	reachable := reachableFuncs(callee, callee.Pkg)
	seen := map[string]bool{}

	for _, root := range roots {
		if isChanType(root.val.Type()) {
			continue
		}
		if isWhitelistedSync(root.val) || isSyncMutex(root.val) || isSyncRWMutex(root.val) || isShareSafeStdlib(root.val) {
			// WaitGroup/Once/Mutex/RWMutex objects are pure synchronization.
			// context.Context and *http.Server are stdlib-safe to share.
			continue
		}
		if _, isGlobal := root.val.(*ssa.Global); isGlobal {
			// Globals are covered package-wide by the init-then-freeze
			// analysis (checkGlobalFreeze), which also reports writes in
			// functions not reachable from any go statement.
			continue
		}
		if !mayContainPointers(root.val.Type()) && !isAddressableShared(root.val) {
			continue
		}

		writtenInGoro := a.writtenIn(root.val, reachable, true)
		writtenAfter := isWrittenAfterGo(root.val, spawner, g, a.funcs, reachable)

		if writtenInGoro || writtenAfter {
			// Prove over all sibling aliases for this go via the guard cascade.
			if mutexGuardsGoRoots(roots, allFuncs) {
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
			a.reportAt(reported, g.Pos(), "shared memory %s written without channel transfer and no proven lock/atomic/partition guard (%s)",
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
