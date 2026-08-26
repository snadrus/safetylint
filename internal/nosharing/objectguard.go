package nosharing

import (
	"go/constant"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

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
	accesses = prepareGuardAccesses(accesses)
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
	if rangePartitionOK(root, accesses) {
		return true
	}
	if fieldPartitionedGuards(root, accesses, funcs) {
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
	return objectGuardedRootsAfter(roots, funcs, nil, nil, nil)
}

// mutexGuardsGoRootsAfter is mutexGuardsGoRoots ignoring spawner accesses that
// dominate the go (construction / init before the value is shared) and
// accesses in unexported helpers only called before that go.
func mutexGuardsGoRootsAfter(roots []sharedRoot, funcs map[*ssa.Function]bool, spawner *ssa.Function, g ssa.Instruction, preShare map[*ssa.Function]bool) bool {
	return objectGuardedRootsAfter(roots, funcs, spawner, g, preShare)
}

func objectGuardedRootsAfter(roots []sharedRoot, funcs map[*ssa.Function]bool, spawner *ssa.Function, g ssa.Instruction, preShare map[*ssa.Function]bool) bool {
	if len(roots) >= maxRootAliases {
		return false
	}
	var accesses []dataAccess
	seenAcc := map[ssa.Instruction]bool{}
	visiting := map[ssa.Value]bool{}
	var dataRoots []ssa.Value
	perRoot := map[ssa.Value][]dataAccess{}
	for _, root := range roots {
		if isChanType(root.val.Type()) || isWhitelistedSync(root.val) || isSyncMutex(root.val) || isSyncRWMutex(root.val) || isShareSafeStdlib(root.val) || isHarmonyDBType(root.val.Type()) {
			continue
		}
		// Package globals are covered by freeze analysis; including them here
		// (e.g. a logger touched inside the goro) poisons per-root proofs.
		if _, ok := root.val.(*ssa.Global); ok {
			continue
		}
		dataRoots = append(dataRoots, root.val)
		rootVisiting := map[ssa.Value]bool{}
		rAcc := collectDataAccessesDeep(root.val, funcs, rootVisiting)
		var filtered []dataAccess
		for _, acc := range rAcc {
			if skipPreShareAccess(acc, spawner, g, preShare) {
				continue
			}
			filtered = append(filtered, acc)
			if seenAcc[acc.instr] {
				continue
			}
			seenAcc[acc.instr] = true
			accesses = append(accesses, acc)
		}
		perRoot[root.val] = prepareGuardAccesses(filtered)
		_ = visiting
	}
	accesses = prepareGuardAccesses(accesses)
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
	if len(dataRoots) > 0 && rangePartitionOK(dataRoots[0], accesses) {
		return true
	}
	if len(dataRoots) > 0 && fieldPartitionedGuards(dataRoots[0], accesses, funcs) {
		return true
	}

	// Atomics (and other per-object proofs) run per written root so values
	// loaded from atomic cells do not poison the parent object.
	for _, r := range dataRoots {
		rAcc := perRoot[r]
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
		if fieldPartitionedGuards(r, rAcc, funcs) {
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
	if root == nil {
		return false
	}
	return typeIsSliceOrArray(root.Type())
}

func typeIsSliceOrArray(t types.Type) bool {
	t = types.Unalias(t)
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

type indexRange struct{ lo, hi int64 }

// rangePartitionOK accepts concurrent writers of disjoint [start,end)
// slice ranges (const bounds). Reads and overlapping ranges fail.
func rangePartitionOK(root ssa.Value, accesses []dataAccess) bool {
	if root == nil || len(accesses) == 0 {
		return false
	}
	if !isSliceOrArrayRoot(root) {
		ok := false
		for _, acc := range accesses {
			if isSliceOrArrayRoot(stripToObject(acc.addr)) || isSliceOrArrayRoot(acc.addr) {
				ok = true
				break
			}
			if sl := sliceOfAccess(acc); sl != nil {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	rangeFns := map[*ssa.Function]bool{}
	for _, acc := range accesses {
		if _, ok := writeRangeOf(acc); ok {
			if fn := acc.instr.Parent(); fn != nil {
				rangeFns[fn] = true
			}
		}
	}
	byFn := map[*ssa.Function][]indexRange{}
	saw := false
	for _, acc := range accesses {
		if isSliceHeaderLoad(acc) {
			continue
		}
		if isSliceHeaderStore(acc) {
			fn := acc.instr.Parent()
			if fn != nil && !rangeFns[fn] {
				continue
			}
			return false
		}
		if !acc.write {
			return false
		}
		r, ok := writeRangeOf(acc)
		if !ok {
			return false
		}
		fn := acc.instr.Parent()
		if fn == nil {
			return false
		}
		byFn[fn] = append(byFn[fn], r)
		saw = true
	}
	if !saw || len(byFn) == 0 {
		return false
	}
	merged := map[*ssa.Function]indexRange{}
	for fn, rs := range byFn {
		m, ok := mergeRanges(rs)
		if !ok {
			return false
		}
		merged[fn] = m
	}
	fns := make([]*ssa.Function, 0, len(merged))
	for fn := range merged {
		fns = append(fns, fn)
	}
	for i := 0; i < len(fns); i++ {
		for j := i + 1; j < len(fns); j++ {
			if rangesOverlap(merged[fns[i]], merged[fns[j]]) {
				return false
			}
		}
	}
	return true
}

func writeRangeOf(acc dataAccess) (indexRange, bool) {
	// A write through dst[lo:hi] owns the whole [lo,hi), not just the
	// element actually stored (overlapping slices are a race).
	if sl := sliceOfAccess(acc); sl != nil {
		return constSliceBounds(sl)
	}
	if idx, ok := constIndexStore(acc); ok {
		return indexRange{idx, idx + 1}, true
	}
	return indexRange{}, false
}

func sliceOfAccess(acc dataAccess) *ssa.Slice {
	if sl, ok := acc.addr.(*ssa.Slice); ok {
		return sl
	}
	cur := acc.addr
	if st, ok := acc.instr.(*ssa.Store); ok {
		cur = st.Addr
	}
	for cur != nil {
		switch v := cur.(type) {
		case *ssa.Slice:
			return v
		case *ssa.IndexAddr:
			if sl, ok := v.X.(*ssa.Slice); ok {
				return sl
			}
			if sl := sliceDef(v.X); sl != nil {
				return sl
			}
			cur = v.X
		case *ssa.UnOp:
			if v.Op == token.MUL {
				cur = v.X
				continue
			}
			return nil
		default:
			return sliceDef(cur)
		}
	}
	return nil
}

func sliceDef(v ssa.Value) *ssa.Slice {
	if sl, ok := v.(*ssa.Slice); ok {
		return sl
	}
	if u, ok := v.(*ssa.UnOp); ok && u.Op == token.MUL {
		if sl := sliceDef(u.X); sl != nil {
			return sl
		}
		return uniqueSliceStore(u.X)
	}
	return uniqueSliceStore(v)
}

func uniqueSliceStore(addr ssa.Value) *ssa.Slice {
	cell := stripToObject(addr)
	if cell == nil {
		return nil
	}
	refs := cell.Referrers()
	if refs == nil {
		return nil
	}
	var found *ssa.Slice
	for _, ref := range *refs {
		st, ok := ref.(*ssa.Store)
		if !ok || stripToObject(st.Addr) != cell {
			continue
		}
		sl, ok := st.Val.(*ssa.Slice)
		if !ok {
			return nil
		}
		if found != nil && found != sl {
			return nil
		}
		found = sl
	}
	return found
}

func constSliceBounds(sl *ssa.Slice) (indexRange, bool) {
	if sl == nil {
		return indexRange{}, false
	}
	lo, ok := constIntOrZero(sl.Low)
	if !ok {
		return indexRange{}, false
	}
	if sl.High == nil {
		return indexRange{}, false
	}
	hi, ok := constIntVal(sl.High)
	if !ok || hi <= lo {
		return indexRange{}, false
	}
	return indexRange{lo, hi}, true
}

func constIntOrZero(v ssa.Value) (int64, bool) {
	if v == nil {
		return 0, true
	}
	return constIntVal(v)
}

func constIntVal(v ssa.Value) (int64, bool) {
	c, ok := v.(*ssa.Const)
	if !ok || c.Value == nil {
		return 0, false
	}
	return constant.Int64Val(c.Value)
}

func mergeRanges(rs []indexRange) (indexRange, bool) {
	if len(rs) == 0 {
		return indexRange{}, false
	}
	m := rs[0]
	for _, r := range rs[1:] {
		if r.lo < m.lo {
			m.lo = r.lo
		}
		if r.hi > m.hi {
			m.hi = r.hi
		}
	}
	// Reject holes that would hide an overlap with a sibling's interior.
	covered := make([]bool, int(m.hi-m.lo))
	for _, r := range rs {
		for i := r.lo; i < r.hi; i++ {
			if i < m.lo || i >= m.hi {
				return indexRange{}, false
			}
			covered[i-m.lo] = true
		}
	}
	for _, ok := range covered {
		if !ok {
			return indexRange{}, false
		}
	}
	return m, true
}

func rangesOverlap(a, b indexRange) bool {
	return a.lo < b.hi && b.lo < a.hi
}
