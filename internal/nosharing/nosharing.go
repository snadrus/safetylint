// Package nosharing refuses cross-goroutine memory sharing except via
// channels with freeze-after-send semantics for pointer-carrying values,
// or via one consistent tied sync.Mutex field in the same/parent struct.
//
// Provably read-only sharing is allowed. sync.WaitGroup, sync.Once, and
// sync.Mutex (as a lock object) are whitelisted as pure synchronization.
// RWMutex-guarded data sharing is refused. TryLock only counts on paths
// where its boolean result is proven true.
//
// Cross-package: functions that spawn and retain parameters export
// MayShareParams Facts; callers must not write those values after the call
// unless under the Fact's tied mutex.
//
// Package globals follow an init-then-freeze discipline: in packages that
// spawn goroutines, globals may only be written before any goroutine could
// be running (init functions, main before the first spawn, and helpers
// provably called only from such points), or under a proven struct-embedded
// sync.Mutex guard.
package nosharing

import (
	"fmt"
	"go/build"
	"go/token"
	"go/types"
	"os"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/ssa"
)

const Doc = `refuse memory shared between goroutines except via frozen channels

The nosharing analyzer proves the absence of data races under a channel-only
sharing discipline, with a narrow exception for proven sync.Mutex guards:

  - Memory shared with a goroutine via capture, argument, or global must be
    provably read-only, be *sync.WaitGroup / *sync.Once / *sync.Mutex, or be
    guarded by one tied sync.Mutex field in the same (or parent) struct that
    is always locked at every access. TryLock only counts on paths where its
    boolean result is proven true.
  - Values may transfer between goroutines through channels. If a sent value
    contains pointers, those pointees are frozen after send: no further writes
    through the sender's or receiver's view of that memory are allowed.
  - sync.RWMutex-guarded sharing is refused.
  - In packages that spawn goroutines, package globals are frozen once
    concurrency may have begun: writes are allowed only in init functions,
    in main before the first spawn point (go, dynamic/interface call,
    Fact-bearing or curated stdlib spawner), in unexported helpers
    provably called only from such points, or under a proven struct-embedded
    sync.Mutex guard. Reads of frozen globals remain legal.
  - Exported functions that spawn and retain parameters publish MayShareParams
    Facts. Call sites must treat those arguments as shared: no post-call
    writes unless under the Fact's tied sync.Mutex. Wrappers re-export Facts.
  - Curated async stdlib APIs (time.AfterFunc, http.HandleFunc, …) share
    closure captures like Fact-bearing calls.
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

	a.exportShareFacts()
	a.exportHotGlobals()
	a.checkShareFactCalls(reported)
	a.checkAsyncCallbackShares(reported)
	a.checkGlobalFreeze(reported)
	a.checkHotGlobalAccesses(reported)
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
	reachable := reachableFuncs(callee, a.pkg)
	seen := map[string]bool{}

	for _, root := range roots {
		if isChanType(root.val.Type()) {
			continue
		}
		if isWhitelistedSync(root.val) || isSyncMutex(root.val) {
			// WaitGroup/Once/Mutex objects are pure synchronization.
			continue
		}
		if isSyncRWMutex(root.val) {
			key := "rwmutex:" + typeKey(root.val)
			if seen[key] {
				continue
			}
			seen[key] = true
			a.reportAt(reported, g.Pos(), "shared sync.RWMutex %s: RWMutex-guarded sharing refused (use channels or sync.Mutex)",
				root.describe())
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
			// Prove over all sibling aliases for this go: every touchpoint of
			// the shared object must use the same tied mutex.
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
			a.reportAt(reported, g.Pos(), "shared memory %s written without channel transfer and no always-locked sync.Mutex guard (%s)",
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
