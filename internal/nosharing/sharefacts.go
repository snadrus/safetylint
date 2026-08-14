package nosharing

import (
	"go/types"
	"sort"

	"golang.org/x/tools/go/ssa"
)

// exportShareFacts publishes MayShareParams / MaySpawn on functions in this
// package, including a fixpoint so wrappers re-export imported callee Facts.
func (a *analyzer) exportShareFacts() {
	if a.pkg == nil || a.pass == nil {
		return
	}
	allFuncs := map[*ssa.Function]bool{}
	for _, f := range a.funcs {
		if f != nil {
			allFuncs[f] = true
		}
	}
	spawner := a.computeSpawners()

	// Local Facts we are about to export (and re-read during the fixpoint).
	localShare := map[*types.Func]*MayShareParams{}
	localSpawn := map[*types.Func]bool{}

	lookupShare := func(obj *types.Func) (*MayShareParams, bool) {
		if obj == nil {
			return nil, false
		}
		obj = originFunc(obj)
		if f, ok := localShare[obj]; ok {
			return f, true
		}
		var fact MayShareParams
		if a.pass.ImportObjectFact(obj, &fact) {
			return &fact, true
		}
		return nil, false
	}
	lookupSpawn := func(obj *types.Func) bool {
		if obj == nil {
			return false
		}
		obj = originFunc(obj)
		if localSpawn[obj] {
			return true
		}
		var fact MaySpawn
		return a.pass.ImportObjectFact(obj, &fact)
	}

	const maxIter = 64
	changed := true
	for iter := 0; changed && iter < maxIter; iter++ {
		changed = false
		for _, fn := range a.funcs {
			if fn == nil || fn.Parent() != nil {
				continue
			}
			obj := funcObject(fn)
			if obj == nil {
				continue
			}
			maySpawn := spawner[fn] || fnMaySpawnViaFacts(fn, lookupSpawn, lookupShare)
			params := a.sharedParamsOf(fn, allFuncs, lookupShare)
			if len(params) > 0 {
				maySpawn = true
				next := &MayShareParams{Params: params}
				prev := localShare[obj]
				if !shareFactEqual(prev, next) {
					localShare[obj] = next
					changed = true
				}
			}
			if maySpawn {
				if !localSpawn[obj] {
					localSpawn[obj] = true
					changed = true
				}
			}
		}
	}

	for obj, fact := range localShare {
		a.localShare[obj] = fact
		// Publish only exported APIs that retain params. Bare MaySpawn is
		// unnecessary for freeze (Fact-less cross-pkg calls stay spawn points)
		// and would clutter analysistest expectations.
		if factsEnabled() && obj.Exported() && len(fact.Params) > 0 {
			a.pass.ExportObjectFact(obj, fact)
		}
	}
	for obj := range localSpawn {
		a.localSpawn[obj] = true
	}
}

func shareFactEqual(a, b *MayShareParams) bool {
	if a == nil || b == nil {
		return a == b
	}
	if len(a.Params) != len(b.Params) {
		return false
	}
	for i := range a.Params {
		if a.Params[i] != b.Params[i] {
			return false
		}
	}
	return true
}

func originFunc(obj *types.Func) *types.Func {
	if obj == nil {
		return nil
	}
	return obj.Origin()
}

func funcObject(fn *ssa.Function) *types.Func {
	if fn == nil {
		return nil
	}
	obj, _ := fn.Object().(*types.Func)
	return originFunc(obj)
}

func fnMaySpawnViaFacts(fn *ssa.Function, spawn func(*types.Func) bool, share func(*types.Func) (*MayShareParams, bool)) bool {
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			var c *ssa.CallCommon
			switch in := instr.(type) {
			case *ssa.Go:
				return true
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
			obj := funcObject(cal)
			if spawn(obj) {
				return true
			}
			if _, ok := share(obj); ok {
				return true
			}
		}
	}
	return false
}

// sharedParamsOf returns parameters/receiver of fn that may be retained by a
// goroutine after fn returns (directly via go, or via a MayShareParams callee).
func (a *analyzer) sharedParamsOf(fn *ssa.Function, allFuncs map[*ssa.Function]bool, lookupShare func(*types.Func) (*MayShareParams, bool)) []SharedParam {
	type key struct {
		recv  bool
		index int
	}
	found := map[key]SharedParam{}

	heapAlias := heapParamAliases(fn)
	goroFuncs := goroutineReachableFrom(fn, a.pkg)

	record := func(paramLike ssa.Value, extra ...ssa.Value) {
		recv, idx, ok := paramIndex(fn, paramLike)
		if !ok {
			if p := heapAlias[stripToObject(paramLike)]; p != nil {
				recv, idx, ok = paramIndex(fn, p)
			} else if p := heapAlias[paramLike]; p != nil {
				recv, idx, ok = paramIndex(fn, p)
			}
		}
		if !ok {
			return
		}
		k := key{recv: recv, index: idx}
		roots := []sharedRoot{{val: paramLike, reason: "param"}}
		for _, e := range extra {
			if e != nil {
				roots = append(roots, sharedRoot{val: e, reason: "alias"})
			}
		}
		for _, p := range fn.Params {
			pr, pi, ok := paramIndex(fn, p)
			if ok && pr == recv && pi == idx {
				roots = append(roots, sharedRoot{val: p, reason: "param"})
			}
		}
		roots = expandRootAliases(roots, allFuncs)

		mode := ShareRead
		written := false
		var candidates []structuralGuard
		seenCand := map[string]bool{}
		var accesses []dataAccess
		seenAcc := map[ssa.Instruction]bool{}
		visiting := map[ssa.Value]bool{}
		accFuncs := goroFuncs
		if len(accFuncs) == 0 {
			accFuncs = allFuncs
		}
		for _, r := range roots {
			// Writes only count in goroutine bodies, not capture setup in fn.
			if a.writtenIn(r.val, accFuncs, true) {
				written = true
			}
			for _, c := range findStructuralGuards(r.val) {
				ck := c.structType.String() + "#" + itoa(c.field)
				if seenCand[ck] {
					continue
				}
				seenCand[ck] = true
				candidates = append(candidates, c)
			}
			for _, acc := range collectDataAccessesDeep(r.val, accFuncs, visiting) {
				if seenAcc[acc.instr] {
					continue
				}
				seenAcc[acc.instr] = true
				accesses = append(accesses, acc)
			}
		}
		if written {
			mode = ShareWrite
		}
		sp := SharedParam{Index: idx, Recv: recv, Mode: mode}
		if tied, ok := findTiedMutex(candidates, accesses); ok {
			sp.Mutex = MutexField{
				StructPath: tied.structType.String(),
				Field:      tied.field,
			}
		}
		if prev, ok := found[k]; ok {
			if prev.Mode == ShareWrite {
				sp.Mode = ShareWrite
			}
			if prev.Mutex.set() && !sp.Mutex.set() {
				sp.Mutex = prev.Mutex
			}
		}
		found[k] = sp
	}

	recordPropagated := func(paramLike ssa.Value, from SharedParam) {
		recv, idx, ok := paramIndex(fn, paramLike)
		if !ok {
			if p := heapAlias[stripToObject(paramLike)]; p != nil {
				recv, idx, ok = paramIndex(fn, p)
			} else if p := heapAlias[paramLike]; p != nil {
				recv, idx, ok = paramIndex(fn, p)
			}
		}
		if !ok {
			return
		}
		k := key{recv: recv, index: idx}
		sp := SharedParam{Index: idx, Recv: recv, Mode: from.Mode, Mutex: from.Mutex}
		if prev, ok := found[k]; ok {
			if prev.Mode == ShareWrite || sp.Mode == ShareWrite {
				sp.Mode = ShareWrite
			}
			if !sp.Mutex.set() && prev.Mutex.set() {
				sp.Mutex = prev.Mutex
			}
		}
		found[k] = sp
	}

	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			switch in := instr.(type) {
			case *ssa.Go:
				c := in.Common()
				for _, arg := range c.Args {
					record(arg, stripToObject(arg))
				}
				recordClosureShares(c.Value, record)
			case *ssa.MakeClosure:
				// Closure may be stored then go'ed; if any referrer is Go,
				// its bindings are shared.
				if closureIsGoed(in) {
					recordClosureShares(in, record)
				}
			case *ssa.Call, *ssa.Defer:
				var c *ssa.CallCommon
				switch x := instr.(type) {
				case *ssa.Call:
					c = x.Common()
				case *ssa.Defer:
					c = x.Common()
				}
				cal := c.StaticCallee()
				if cal == nil {
					continue
				}
				fact, ok := lookupShare(funcObject(cal))
				if !ok || fact == nil {
					continue
				}
				for _, sp := range fact.Params {
					arg := argForSharedParam(c, cal, sp)
					if arg == nil {
						continue
					}
					recordPropagated(arg, sp)
				}
			}
		}
	}

	var out []SharedParam
	for _, sp := range found {
		out = append(out, sp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Recv != out[j].Recv {
			return out[i].Recv && !out[j].Recv
		}
		return out[i].Index < out[j].Index
	})
	return out
}

func recordClosureShares(v ssa.Value, record func(ssa.Value, ...ssa.Value)) {
	mc, ok := v.(*ssa.MakeClosure)
	if !ok {
		return
	}
	clo, _ := mc.Fn.(*ssa.Function)
	for i, bind := range mc.Bindings {
		var fv ssa.Value
		if clo != nil && i < len(clo.FreeVars) {
			fv = clo.FreeVars[i]
		}
		record(bind, stripToObject(bind), fv)
	}
}

func closureIsGoed(mc *ssa.MakeClosure) bool {
	refs := mc.Referrers()
	if refs == nil {
		return false
	}
	for _, r := range *refs {
		if g, ok := r.(*ssa.Go); ok && g.Common().Value == mc {
			return true
		}
	}
	return false
}

// goroutineReachableFrom returns functions that may run in goroutines started
// from fn (go callees and their same-package callees), excluding fn itself.
func goroutineReachableFrom(fn *ssa.Function, pkg *ssa.Package) map[*ssa.Function]bool {
	out := map[*ssa.Function]bool{}
	if fn == nil {
		return out
	}
	var seeds []*ssa.Function
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			g, ok := instr.(*ssa.Go)
			if !ok {
				continue
			}
			c := g.Common()
			if cal := c.StaticCallee(); cal != nil {
				seeds = append(seeds, cal)
				continue
			}
			if mc, ok := c.Value.(*ssa.MakeClosure); ok {
				if clo, ok := mc.Fn.(*ssa.Function); ok {
					seeds = append(seeds, clo)
				}
			}
		}
	}
	for _, s := range seeds {
		for f := range reachableFuncs(s, pkg) {
			out[f] = true
		}
	}
	return out
}

// heapParamAliases maps heap Allocs (and their addresses) that store a copy
// of a parameter — typical for closure captures of params. A store that is
// definitely replaced before every spawn/closure point no longer aliases the
// param there (EnableChangeDetection storing obj then overwriting the field
// with a private copy before go).
func heapParamAliases(fn *ssa.Function) map[ssa.Value]*ssa.Parameter {
	out := map[ssa.Value]*ssa.Parameter{}
	if fn == nil {
		return out
	}
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			st, ok := instr.(*ssa.Store)
			if !ok {
				continue
			}
			p, ok := st.Val.(*ssa.Parameter)
			if !ok {
				continue
			}
			if paramStoreSuperseded(fn, st) {
				continue
			}
			addr := st.Addr
			out[addr] = p
			out[stripToObject(addr)] = p
		}
	}
	return out
}

// paramStoreSuperseded reports that another store replaces the same cell
// with a non-parameter value before every Go and MakeClosure in fn.
func paramStoreSuperseded(fn *ssa.Function, st *ssa.Store) bool {
	var spawnish []ssa.Instruction
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			switch instr.(type) {
			case *ssa.Go, *ssa.MakeClosure:
				spawnish = append(spawnish, instr)
			}
		}
	}
	if len(spawnish) == 0 {
		return false
	}
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			st2, ok := instr.(*ssa.Store)
			if !ok || st2 == st || st2.Val == st.Val {
				continue
			}
			if _, isParam := st2.Val.(*ssa.Parameter); isParam {
				continue
			}
			if !sameCellAddr(st.Addr, st2.Addr) {
				continue
			}
			all := true
			for _, sp := range spawnish {
				if !dominatesInstr(st2, sp) {
					all = false
					break
				}
			}
			if all {
				return true
			}
		}
	}
	return false
}

// sameCellAddr matches two addresses that name the same memory cell: the
// identical SSA value, the same field of the same object, or the same
// canonical object.
func sameCellAddr(a, b ssa.Value) bool {
	if a == nil || b == nil {
		return false
	}
	if a == b {
		return true
	}
	fa, okA := a.(*ssa.FieldAddr)
	fb, okB := b.(*ssa.FieldAddr)
	if okA && okB {
		return fa.Field == fb.Field && stripToObject(fa.X) == stripToObject(fb.X)
	}
	if okA != okB {
		return false
	}
	return stripToObject(a) == stripToObject(b)
}

func argForSharedParam(c *ssa.CallCommon, cal *ssa.Function, sp SharedParam) ssa.Value {
	if c == nil {
		return nil
	}
	if sp.Recv {
		if c.IsInvoke() {
			return c.Value
		}
		if len(c.Args) > 0 {
			return c.Args[0]
		}
		return nil
	}
	off := 0
	if cal != nil && cal.Signature != nil && cal.Signature.Recv() != nil {
		off = 1
	}
	i := sp.Index + off
	if i < 0 || i >= len(c.Args) {
		return nil
	}
	return c.Args[i]
}

func paramIndex(fn *ssa.Function, v ssa.Value) (recv bool, index int, ok bool) {
	v = stripToObject(v)
	if v == nil || fn == nil {
		return false, 0, false
	}
	if len(fn.Params) == 0 {
		return false, 0, false
	}
	// Method: Params[0] is receiver.
	hasRecv := fn.Signature != nil && fn.Signature.Recv() != nil
	for i, p := range fn.Params {
		if p == v {
			if hasRecv && i == 0 {
				return true, 0, true
			}
			idx := i
			if hasRecv {
				idx = i - 1
			}
			return false, idx, true
		}
	}
	return false, 0, false
}

// checkShareFactCalls enforces synthetic share events at call sites of
// functions that export MayShareParams.
func (a *analyzer) checkShareFactCalls(reported map[string]bool) {
	if a.pass == nil {
		return
	}
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
				obj := funcObject(cal)
				if obj == nil {
					continue
				}
				var fact MayShareParams
				if cal.Pkg == a.pkg {
					if !a.localShareFact(obj, &fact) {
						continue
					}
				} else if !a.pass.ImportObjectFact(obj, &fact) {
					continue
				}
				for _, sp := range fact.Params {
					// Same-package write shares without a tied mutex are
					// already refused at the go site by checkGo.
					if cal.Pkg == a.pkg && sp.Mode == ShareWrite && !sp.Mutex.set() {
						continue
					}
					arg := argForSharedParam(c, cal, sp)
					if arg == nil {
						continue
					}
					a.checkShareEvent(fn, instr, arg, sp, allFuncs, reported)
				}
			}
		}
	}
}

// localShareFact reads a Fact we exported earlier in this same pass. The
// analysis driver may not round-trip Export→Import within one package.
func (a *analyzer) localShareFact(obj *types.Func, into *MayShareParams) bool {
	if a.localShare == nil || obj == nil {
		return false
	}
	f, ok := a.localShare[originFunc(obj)]
	if !ok || f == nil {
		return false
	}
	*into = *f
	return true
}

func (a *analyzer) checkShareEvent(callFn *ssa.Function, callInstr ssa.Instruction, arg ssa.Value, sp SharedParam, allFuncs map[*ssa.Function]bool, reported map[string]bool) {
	roots := []sharedRoot{{val: arg, reason: "shared via cross-package (or Fact) call"}}
	roots = expandRootAliases(roots, allFuncs)

	var accesses []dataAccess
	seen := map[ssa.Instruction]bool{}
	visiting := map[ssa.Value]bool{}
	for _, r := range roots {
		if isChanType(r.val.Type()) || isWhitelistedSync(r.val) || isSyncMutex(r.val) || a.isConcurrentSafeValue(r.val) {
			continue
		}
		for _, acc := range collectDataAccessesDeep(r.val, allFuncs, visiting) {
			if seen[acc.instr] {
				continue
			}
			if !accessAfterShare(acc.instr, callFn, callInstr) {
				continue
			}
			seen[acc.instr] = true
			accesses = append(accesses, acc)
		}
	}
	accesses = dropConcurrentSafeFieldAccesses(a.pass, accesses)
	if len(accesses) == 0 {
		return
	}

	pos := callInstr.Pos()
	if !pos.IsValid() {
		pos = callFn.Pos()
	}

	if sp.Mutex.set() {
		tied, ok := matchTiedGuard(roots, sp.Mutex)
		if !ok || !hasTiedMutex([]structuralGuard{tied}, accesses) {
			a.reportAt(reported, pos, "shared memory from Fact-bearing call written/accessed without its tied sync.Mutex guard")
		}
		return
	}

	// No Fact-tied mutex: allow if the cascade proves a guard (free-standing,
	// RWMutex, atomics-only, or partitioned writers) over post-share accesses.
	for _, r := range roots {
		if isChanType(r.val.Type()) || isWhitelistedSync(r.val) || isSyncMutex(r.val) || a.isConcurrentSafeValue(r.val) {
			continue
		}
		if accessesGuarded(r.val, accesses, allFuncs) {
			return
		}
	}

	// Refuse any write after the share event.
	for _, acc := range accesses {
		if isWriteAccess(acc) {
			a.reportAt(reported, pos, "shared memory from Fact-bearing call written after share (callee may retain it concurrently)")
			return
		}
	}
}

func matchTiedGuard(roots []sharedRoot, m MutexField) (structuralGuard, bool) {
	for _, r := range roots {
		for _, c := range findStructuralGuards(r.val) {
			if c.structType.String() == m.StructPath && c.field == m.Field {
				return c, true
			}
		}
	}
	return structuralGuard{}, false
}

func isWriteAccess(acc dataAccess) bool {
	return acc.write
}

// accessAfterShare reports whether instr may execute after the share call
// (same-function CFG reachability from the call, or any other function).
func accessAfterShare(instr ssa.Instruction, callFn *ssa.Function, callInstr ssa.Instruction) bool {
	fn := instr.Parent()
	if fn == nil {
		return true
	}
	if fn != callFn {
		return true
	}
	return reachableFromInstr(callInstr, instr)
}

func reachableFromInstr(from, to ssa.Instruction) bool {
	if from == nil || to == nil {
		return false
	}
	fb := from.Block()
	tb := to.Block()
	if fb == nil || tb == nil {
		return false
	}
	if fb == tb {
		// Same block: to must appear after from.
		seenFrom := false
		for _, instr := range fb.Instrs {
			if instr == from {
				seenFrom = true
				continue
			}
			if seenFrom && instr == to {
				return true
			}
		}
		return false
	}
	// Reachability from successors of from's block to to's block.
	seen := map[*ssa.BasicBlock]bool{}
	var work []*ssa.BasicBlock
	for _, s := range fb.Succs {
		if !seen[s] {
			seen[s] = true
			work = append(work, s)
		}
	}
	for len(work) > 0 {
		b := work[len(work)-1]
		work = work[:len(work)-1]
		if b == tb {
			return true
		}
		for _, s := range b.Succs {
			if !seen[s] {
				seen[s] = true
				work = append(work, s)
			}
		}
	}
	return false
}

// importMaySpawn reports whether obj has MaySpawn or MayShareParams
// (imported or local to this pass).
func (a *analyzer) importMaySpawn(obj *types.Func) bool {
	if obj == nil {
		return false
	}
	obj = originFunc(obj)
	if a.localSpawn != nil && a.localSpawn[obj] {
		return true
	}
	if a.localShare != nil {
		if _, ok := a.localShare[obj]; ok {
			return true
		}
	}
	if a.pass == nil {
		return false
	}
	var spawn MaySpawn
	if a.pass.ImportObjectFact(obj, &spawn) {
		return true
	}
	var share MayShareParams
	return a.pass.ImportObjectFact(obj, &share)
}
