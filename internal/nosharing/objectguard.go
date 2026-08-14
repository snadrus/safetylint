package nosharing

import (
	"go/constant"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ssa"
)

// dropConcurrentSafeFieldAccesses removes loads of, and method calls
// through, fields/cells whose type is ConcurrentSafe (anchor or imported
// Fact): a *sql.DB read or a mutex-guarded promise.Promise method call is
// safe wherever the value is shared. Stores and map updates that rebind the
// cell itself are kept — replacing t.db still races with concurrent readers.
func dropConcurrentSafeFieldAccesses(pass *analysis.Pass, accesses []dataAccess) []dataAccess {
	if pass == nil {
		return accesses
	}
	// A store that rebinds a ConcurrentSafe-typed cell (t.db = other) makes
	// concurrent loads of that cell racy again: keep everything when any
	// such rebind exists so the reads participate in the guard proofs.
	for _, acc := range accesses {
		switch acc.instr.(type) {
		case *ssa.Store, *ssa.MapUpdate:
			if acc.addr != nil && safeCellType(pass, acc.addr.Type()) {
				return accesses
			}
		}
	}
	out := accesses[:0:0]
	for _, acc := range accesses {
		if concurrentSafeFieldAccess(pass, acc) {
			continue
		}
		out = append(out, acc)
	}
	return out
}

func safeCellType(pass *analysis.Pass, t types.Type) bool {
	return isConcurrentSafeType(pass, t) || isConcurrentSafeType(pass, pointeeType(t))
}

// spawnerNonConcurrent reports a spawner-side access that cannot race with
// the goroutine g: it runs before the go within the iteration (pre-share
// construction — ReadFull into a leased buffer, conditional header trims),
// or after this go's WaitGroup join (post-Wait teardown — final hash of the
// apex buffer). Deep accesses are positioned at their triggering call site.
func spawnerNonConcurrent(acc dataAccess, spawner *ssa.Function, g *ssa.Go) bool {
	if spawner == nil || g == nil {
		return false
	}
	anchor := acc.instr
	if anchor == nil {
		return false
	}
	if anchor.Parent() != spawner {
		if acc.via != nil && acc.via.Parent() == spawner {
			anchor = acc.via
		} else {
			return callerPreSpawn(anchor, acc.via, spawner)
		}
	}
	if instrBeforeGo(anchor, g) {
		return true
	}
	return instrAfterJoin(anchor, spawner, g)
}

// callerPreSpawn reports a constructor-freeze shape: the access happens in a
// caller F of the spawner, before every call in F that (transitively, via
// same-package static calls) reaches the spawner. Such writes happen-before
// the call and therefore before the spawn inside it (TaskEngine field setup
// in New before startScheduler()).
func callerPreSpawn(anchor, via ssa.Instruction, spawner *ssa.Function) bool {
	if anchor == nil || spawner == nil {
		return false
	}
	fn := anchor.Parent()
	if via != nil && via.Parent() != nil {
		anchor = via
		fn = via.Parent()
	}
	if fn == nil || fn == spawner {
		return false
	}
	found := false
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			var c *ssa.CallCommon
			switch in := instr.(type) {
			case *ssa.Call:
				c = in.Common()
			case *ssa.Defer:
				c = in.Common()
			case *ssa.Go:
				// A go in the caller itself: the access must precede it too
				// (its goroutine may alias the same object).
				if !instrBeforeInstr(anchor, in) {
					return false
				}
				continue
			default:
				continue
			}
			cal := staticCallee(c)
			if cal == nil || len(cal.Blocks) == 0 {
				continue
			}
			if cal != spawner && !callReachable(cal)[spawner] {
				continue
			}
			found = true
			if !instrBeforeInstr(anchor, instr) {
				return false
			}
		}
	}
	return found
}

// instrAfterJoin reports that instr runs after a wg.Wait() paired with g
// (Add dominating the go; the Wait not preceding the go). Post-Wait code
// only runs once the counter drains; if the goroutine never calls Done,
// the Wait blocks and the access never executes — either way it cannot
// race with g. The go need not dominate the Wait (range-loop spawns may
// run zero times — then there is nothing to race with).
func instrAfterJoin(instr ssa.Instruction, spawner *ssa.Function, g *ssa.Go) bool {
	wg := waitGroupOfGo(spawner, g)
	if wg == nil {
		return false
	}
	for _, b := range spawner.Blocks {
		for _, in := range b.Instrs {
			call, ok := in.(*ssa.Call)
			if !ok || !isWaitGroupMethod(call.Common(), "Wait") {
				continue
			}
			if stripToObject(recvOfCall(call.Common())) != wg {
				continue
			}
			if dominatesInstr(in, g) {
				continue // a Wait before the spawn does not join it
			}
			if dominatesInstr(in, instr) {
				return true
			}
		}
	}
	return false
}

func concurrentSafeFieldAccess(pass *analysis.Pass, acc dataAccess) bool {
	if acc.addr == nil {
		return false
	}
	switch acc.instr.(type) {
	case *ssa.Store, *ssa.MapUpdate:
		return false
	}
	return safeCellType(pass, acc.addr.Type())
}

// objectGuarded reports whether every data access to root in funcs is proven
// safe by the north-star cascade:
//
//	tied Mutex → free-standing pkg Mutex → RWMutex discipline → atomics-only
//	→ const-index partitioned writers
func objectGuarded(root ssa.Value, funcs map[*ssa.Function]bool) bool {
	if root == nil {
		return true
	}
	accesses := collectDataAccessesDeep(root, funcs, map[ssa.Value]bool{})
	return accessesGuarded(root, accesses, funcs)
}

// accessesGuarded runs the guard cascade over a precomputed access list.
func accessesGuarded(root ssa.Value, accesses []dataAccess, funcs map[*ssa.Function]bool) bool {
	accesses = filterFrozenReads(root, accesses, funcs)
	if len(accesses) == 0 {
		return true
	}
	if hasTiedMutex(findStructuralGuards(root), accesses) {
		return true
	}
	if freeStandingMutexGuards(accesses, funcs, false) {
		return true
	}
	if rwMutexGuards(root, accesses, funcs) {
		return true
	}
	if atomicsOnlyAccesses(accesses) {
		return true
	}
	if constIndexPartitionOK(root, accesses) {
		return true
	}
	if consistentMutexGuards(accesses, funcs) {
		return true
	}
	if fieldMutexGuards(root, accesses, funcs) {
		return true
	}
	if stridePartitionOK(root, accesses) {
		return true
	}
	if leaseExclusiveOK(root, accesses, funcs) {
		return true
	}
	if rolePartitionOK(root, accesses, funcs) {
		return true
	}
	if singleOwnerGoroutineOK(root, accesses, funcs) {
		return true
	}
	return false
}

// accessesGuardedType is the ConcurrentSafe-derivation cascade: mutex,
// atomics, frozen fields, and nested ConcurrentSafe only. Site-local
// proofs (lease / stride / role) are not type-level properties.
func accessesGuardedType(root ssa.Value, accesses []dataAccess, funcs map[*ssa.Function]bool) bool {
	accesses = filterFrozenReads(root, accesses, funcs)
	if len(accesses) == 0 {
		return true
	}
	if hasTiedMutex(findStructuralGuards(root), accesses) {
		return true
	}
	if freeStandingMutexGuards(accesses, funcs, false) {
		return true
	}
	if rwMutexGuards(root, accesses, funcs) {
		return true
	}
	if atomicsOnlyAccesses(accesses) {
		return true
	}
	if consistentMutexGuards(accesses, funcs) {
		return true
	}
	if fieldMutexGuards(root, accesses, funcs) {
		return true
	}
	return false
}

// objectGuardedRoots is the multi-root variant used at go sites.
// Tied/free-standing mutex proofs may use the union of sibling-alias accesses
// (one consistent guard). Atomics and partition proofs are per written root so
// values loaded out of atomic cells (separate heap objects) do not poison the
// parent object's atomics-only proof.
func objectGuardedRoots(roots []sharedRoot, funcs map[*ssa.Function]bool) bool {
	return objectGuardedRootsAfter(nil, roots, funcs, nil, nil)
}

// mutexGuardsGoRootsAfter is mutexGuardsGoRoots ignoring spawner accesses that
// dominate the go (construction / init before the value is shared).
func mutexGuardsGoRootsAfter(pass *analysis.Pass, roots []sharedRoot, funcs map[*ssa.Function]bool, spawner *ssa.Function, g *ssa.Go) bool {
	return objectGuardedRootsAfter(pass, roots, funcs, spawner, g)
}

func objectGuardedRootsAfter(pass *analysis.Pass, roots []sharedRoot, funcs map[*ssa.Function]bool, spawner *ssa.Function, g *ssa.Go) bool {
	if len(roots) >= maxRootAliases {
		return false
	}
	var accesses []dataAccess
	seenAcc := map[ssa.Instruction]bool{}
	visiting := map[ssa.Value]bool{}
	var dataRoots []ssa.Value
	perRoot := map[ssa.Value][]dataAccess{}
	for _, root := range roots {
		if isChanType(root.val.Type()) || isWhitelistedSync(root.val) || isSyncMutex(root.val) || isSyncRWMutex(root.val) || isConcurrentSafeValue(root.val) {
			continue
		}
		// Package globals are covered by freeze analysis; including them here
		// (e.g. a logger touched inside the goro) poisons per-root proofs.
		if _, ok := root.val.(*ssa.Global); ok {
			continue
		}
		dataRoots = append(dataRoots, root.val)
		rootVisiting := map[ssa.Value]bool{}
		rAcc := filterFrozenReads(root.val, collectDataAccessesDeepPass(pass, root.val, funcs, rootVisiting), funcs)
		rAcc = dropConcurrentSafeFieldAccesses(pass, rAcc)
		perRoot[root.val] = rAcc
		for _, acc := range rAcc {
			if seenAcc[acc.instr] {
				continue
			}
			// Pre-share construction and post-join teardown in the spawner
			// are not concurrent accesses.
			if spawnerNonConcurrent(acc, spawner, g) {
				continue
			}
			seenAcc[acc.instr] = true
			accesses = append(accesses, acc)
		}
		_ = visiting
	}
	if len(accesses) == 0 {
		return true
	}

	var muCands []structuralGuard
	seenMu := map[string]bool{}
	for _, r := range dataRoots {
		for _, c := range findStructuralGuards(r) {
			key := c.structType.String() + "#" + itoa(c.field)
			if seenMu[key] {
				continue
			}
			seenMu[key] = true
			muCands = append(muCands, c)
		}
	}
	if hasTiedMutex(muCands, accesses) {
		return true
	}
	if freeStandingMutexGuards(accesses, funcs, false) {
		return true
	}

	var rwCands []structuralGuard
	seenRW := map[string]bool{}
	for _, r := range dataRoots {
		for _, c := range findStructuralRWGuards(r) {
			key := c.structType.String() + "#" + itoa(c.field)
			if seenRW[key] {
				continue
			}
			seenRW[key] = true
			rwCands = append(rwCands, c)
		}
	}
	if hasTiedMutex(rwCands, accesses) {
		return true
	}
	if freeStandingMutexGuards(accesses, funcs, true) {
		return true
	}
	// Partition must stay collective so sibling closure FreeVars of one buffer
	// are checked for disjoint indexes together.
	if len(dataRoots) > 0 && constIndexPartitionOK(dataRoots[0], accesses) {
		return true
	}
	if consistentMutexGuards(accesses, funcs) {
		return true
	}
	if len(dataRoots) > 0 && fieldMutexGuards(dataRoots[0], accesses, funcs) {
		return true
	}
	if len(dataRoots) > 0 && stridePartitionOK(dataRoots[0], accesses) {
		return true
	}
	if len(dataRoots) > 0 && leaseExclusiveOKAt(dataRoots[0], accesses, funcs, spawner, g) {
		return true
	}
	if len(dataRoots) > 0 && rolePartitionOKAt(dataRoots[0], accesses, funcs, g) {
		return true
	}
	// Single-owner runs on the same-object union only: per-root access
	// lists hide sibling goroutines touching the same memory.
	if len(dataRoots) > 0 && singleOwnerGoroutineOK(dataRoots[0], accesses, funcs) {
		return true
	}
	if len(dataRoots) > 0 && fieldSingleOwnerOK(dataRoots[0], accesses, funcs) {
		return true
	}

	// Atomics (and other per-object proofs) run per written root so values
	// loaded from atomic cells do not poison the parent object.
	for _, r := range dataRoots {
		rAcc := perRoot[r]
		if spawner != nil && g != nil {
			var live []dataAccess
			for _, acc := range rAcc {
				if spawnerNonConcurrent(acc, spawner, g) {
					continue
				}
				live = append(live, acc)
			}
			rAcc = live
		}
		if !accessesHaveWrite(rAcc) || onlySetupWrites(rAcc) {
			continue
		}
		if hasTiedMutex(findStructuralGuards(r), rAcc) {
			continue
		}
		if freeStandingMutexGuards(rAcc, funcs, false) {
			continue
		}
		if hasTiedMutex(findStructuralRWGuards(r), rAcc) {
			continue
		}
		if freeStandingMutexGuards(rAcc, funcs, true) {
			continue
		}
		if atomicsOnlyAccesses(rAcc) {
			continue
		}
		if consistentMutexGuards(rAcc, funcs) {
			continue
		}
		if fieldMutexGuards(r, rAcc, funcs) {
			continue
		}
		if stridePartitionOK(r, rAcc) {
			continue
		}
		if leaseExclusiveOKAt(r, rAcc, funcs, spawner, g) {
			continue
		}
		if rolePartitionOKAt(r, rAcc, funcs, g) {
			continue
		}
		// Ownership over the union (not rAcc): sibling closures' accesses
		// must be visible or two event loops would each look like one owner.
		if singleOwnerGoroutineOK(r, accesses, funcs) {
			continue
		}
		if fieldSingleOwnerOK(r, accesses, funcs) {
			continue
		}
		return false
	}
	return true
}

func accessesHaveWrite(accesses []dataAccess) bool {
	for _, acc := range accesses {
		if acc.write {
			return true
		}
	}
	return false
}

// onlySetupWrites reports accesses whose writes are solely object/header
// initialization (not element or field mutation of interest).
func onlySetupWrites(accesses []dataAccess) bool {
	sawWrite := false
	for _, acc := range accesses {
		if !acc.write {
			continue
		}
		sawWrite = true
		if isObjectInitStore(acc) || isSliceHeaderStore(acc) || isShareSafeFieldStore(acc) {
			continue
		}
		return false
	}
	return sawWrite
}

// freeStandingMutexGuards accepts when one free-standing Mutex (or RWMutex if
// rw) — package Global or local Alloc — is held at every access with the right
// mode. Local Alloc covers WaitGroup fan-out locks (bufLk / errLock / healthyLk).
func freeStandingMutexGuards(accesses []dataAccess, funcs map[*ssa.Function]bool, rw bool) bool {
	cands := freeStandingMutexCells(funcs, rw)
	if len(cands) == 0 {
		return false
	}
	heldCache := map[*ssa.Function]map[ssa.Instruction]holdSet{}
	// Candidates that appear held at least once.
	var used []ssa.Value
	for _, cell := range cands {
		key := guardKey{base: cell, field: -1}
		for _, acc := range accesses {
			fn := acc.instr.Parent()
			if fn == nil {
				continue
			}
			held, cached := heldCache[fn]
			if !cached {
				held = analyzeMustHold(fn)
				heldCache[fn] = held
			}
			if mode, ok := held[acc.instr][key]; ok && mode != 0 {
				used = append(used, cell)
				break
			}
		}
	}
	for _, cell := range used {
		key := guardKey{base: cell, field: -1}
		ok := true
		for _, acc := range accesses {
			fn := acc.instr.Parent()
			if fn == nil {
				ok = false
				break
			}
			held, cached := heldCache[fn]
			if !cached {
				held = analyzeMustHold(fn)
				heldCache[fn] = held
			}
			mode := held[acc.instr][key]
			if !modeOKForAccess(mode, acc.write, rw) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func freeStandingMutexCells(funcs map[*ssa.Function]bool, rw bool) []ssa.Value {
	seen := map[ssa.Value]bool{}
	var out []ssa.Value
	want := "Mutex"
	if rw {
		want = "RWMutex"
	}
	add := func(v ssa.Value) {
		if v == nil || seen[v] {
			return
		}
		if !isNamedSyncType(v.Type(), want) {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	for fn := range funcs {
		if fn == nil {
			continue
		}
		if fn.Pkg != nil {
			for _, mem := range fn.Pkg.Members {
				if gl, ok := mem.(*ssa.Global); ok {
					add(gl)
				}
			}
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				if al, ok := instr.(*ssa.Alloc); ok {
					add(al)
				}
			}
		}
	}
	return out
}

// rwMutexGuards accepts tied or free-standing RWMutex with Lock/RLock discipline.
func rwMutexGuards(root ssa.Value, accesses []dataAccess, funcs map[*ssa.Function]bool) bool {
	if hasTiedMutex(findStructuralRWGuards(root), accesses) {
		return true
	}
	return freeStandingMutexGuards(accesses, funcs, true)
}

// atomicsOnlyAccesses reports whether concurrent mutation of the object is
// only via sync/atomic. Plain reads are allowed (e.g. an immutable context
// field). Construction stores of share-safe values (context.Context) are
// allowed; any other plain write fails the proof.
func atomicsOnlyAccesses(accesses []dataAccess) bool {
	if len(accesses) == 0 {
		return false
	}
	sawAtomic := false
	for _, acc := range accesses {
		if isAtomicAccess(acc) {
			sawAtomic = true
			continue
		}
		if !acc.write {
			continue // plain read of a non-atomic field
		}
		if isShareSafeFieldStore(acc) || isObjectInitStore(acc) {
			continue
		}
		return false
	}
	return sawAtomic
}

func isShareSafeFieldStore(acc dataAccess) bool {
	st, ok := acc.instr.(*ssa.Store)
	if !ok {
		return false
	}
	if isContextType(st.Val.Type()) {
		return true
	}
	return isContextType(pointeeType(st.Addr.Type()))
}

// isObjectInitStore reports a whole-struct store through the object pointer
// (composite literal initialization), not a field store.
func isObjectInitStore(acc dataAccess) bool {
	st, ok := acc.instr.(*ssa.Store)
	if !ok {
		return false
	}
	if _, isFA := st.Addr.(*ssa.FieldAddr); isFA {
		return false
	}
	return structOf(st.Val.Type()) != nil && structOf(pointeeType(st.Addr.Type())) != nil
}

func isAtomicAccess(acc dataAccess) bool {
	var c *ssa.CallCommon
	switch in := acc.instr.(type) {
	case *ssa.Call:
		c = in.Common()
	case *ssa.Defer:
		c = in.Common()
	case *ssa.Go:
		c = in.Common()
	case *ssa.Store:
		// Direct store into an atomic.* cell (unusual; methods are typical).
		return isAtomicValueType(pointeeType(in.Addr.Type()))
	case *ssa.UnOp:
		if in.Op == token.MUL {
			return isAtomicValueType(pointeeType(in.X.Type()))
		}
		return false
	default:
		return false
	}
	if c == nil {
		return false
	}
	cal := c.StaticCallee()
	if isAtomicCallee(cal) {
		return true
	}
	// Method on atomic.Pointer/Bool/… receiver whose type is sync/atomic.
	if cal != nil && cal.Signature.Recv() != nil && isAtomicValueType(cal.Signature.Recv().Type()) {
		return true
	}
	recv := recvOfCall(c)
	return recv != nil && isAtomicValueType(recv.Type())
}

func pointeeType(t types.Type) types.Type {
	t = types.Unalias(t)
	if p, ok := t.(*types.Pointer); ok {
		return types.Unalias(p.Elem())
	}
	return t
}

func isAtomicValueType(t types.Type) bool {
	t = types.Unalias(t)
	if p, ok := t.(*types.Pointer); ok {
		t = types.Unalias(p.Elem())
	}
	// atomic.Pointer[T] is an instantiated named type.
	n, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := n.Obj()
	if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != "sync/atomic" {
		return false
	}
	switch obj.Name() {
	case "Bool", "Int32", "Int64", "Uint32", "Uint64", "Uintptr",
		"Value", "Pointer", "Float32", "Float64":
		return true
	}
	return false
}

// constIndexPartitionOK allows concurrent writers that each Store through a
// compile-time constant IndexAddr of a shared slice/array, with pairwise
// disjoint indexes across writer functions and no reads/other mutations.
// Loading the slice/array header solely to form an IndexAddr is ignored.
func constIndexPartitionOK(root ssa.Value, accesses []dataAccess) bool {
	if root == nil || len(accesses) == 0 {
		return false
	}
	// Accept if any alias root (or the given root) is a slice/array.
	if !isSliceOrArrayRoot(root) {
		ok := false
		for _, acc := range accesses {
			if isSliceOrArrayRoot(stripToObject(acc.addr)) || isSliceOrArrayRoot(acc.addr) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	// Functions that perform const-index element stores.
	indexFns := map[*ssa.Function]bool{}
	for _, acc := range accesses {
		if _, ok := constIndexStore(acc); ok {
			if fn := acc.instr.Parent(); fn != nil {
				indexFns[fn] = true
			}
		}
	}
	byFn := map[*ssa.Function]map[int64]bool{}
	sawStore := false
	for _, acc := range accesses {
		if isSliceHeaderLoad(acc) {
			continue
		}
		// Slice/array header stores in setup functions (no element stores)
		// are ignored — e.g. buf := make([]T, n) before go. A header store
		// inside a writer goroutine is a whole-buffer mutation and fails.
		if isSliceHeaderStore(acc) {
			fn := acc.instr.Parent()
			if fn != nil && !indexFns[fn] {
				continue
			}
			return false
		}
		if !acc.write {
			return false
		}
		idx, ok := constIndexStore(acc)
		if !ok {
			return false
		}
		sawStore = true
		fn := acc.instr.Parent()
		if fn == nil {
			return false
		}
		if byFn[fn] == nil {
			byFn[fn] = map[int64]bool{}
		}
		byFn[fn][idx] = true
	}
	if !sawStore {
		return false
	}
	fns := make([]*ssa.Function, 0, len(byFn))
	for fn := range byFn {
		fns = append(fns, fn)
	}
	for i := 0; i < len(fns); i++ {
		for j := i + 1; j < len(fns); j++ {
			for idx := range byFn[fns[i]] {
				if byFn[fns[j]][idx] {
					return false
				}
			}
		}
	}
	return true
}

// isSliceHeaderLoad reports a *load of a slice/array header (or *header) used
// as an addressing base, not an element read through IndexAddr.
func isSliceHeaderLoad(acc dataAccess) bool {
	u, ok := acc.instr.(*ssa.UnOp)
	if !ok || u.Op != token.MUL || acc.write {
		return false
	}
	if _, isIdx := u.X.(*ssa.IndexAddr); isIdx {
		return false // element load
	}
	return isSliceOrArrayRoot(u.X) || isSliceOrArrayRoot(stripToObject(u.X))
}

// isSliceHeaderStore reports a Store to a slice/array header cell (not an
// element through IndexAddr).
func isSliceHeaderStore(acc dataAccess) bool {
	st, ok := acc.instr.(*ssa.Store)
	if !ok {
		return false
	}
	if _, isIdx := st.Addr.(*ssa.IndexAddr); isIdx {
		return false
	}
	return isSliceOrArrayRoot(st.Addr) || isSliceOrArrayRoot(stripToObject(st.Addr))
}

func isSliceOrArrayRoot(root ssa.Value) bool {
	t := types.Unalias(root.Type())
	if p, ok := t.(*types.Pointer); ok {
		t = types.Unalias(p.Elem())
	}
	switch t.(type) {
	case *types.Slice, *types.Array:
		return true
	}
	return false
}

func constIndexStore(acc dataAccess) (int64, bool) {
	st, ok := acc.instr.(*ssa.Store)
	if !ok {
		return 0, false
	}
	ia, ok := st.Addr.(*ssa.IndexAddr)
	if !ok {
		return 0, false
	}
	// Index must be a compile-time constant.
	c, ok := ia.Index.(*ssa.Const)
	if !ok || c.Value == nil {
		return 0, false
	}
	val, ok := constant.Int64Val(c.Value)
	if !ok {
		return 0, false
	}
	return val, true
}
