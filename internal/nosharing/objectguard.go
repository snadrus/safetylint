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
	return false
}

// objectGuardedRoots is the multi-root variant used at go sites: one consistent
// tied/free/RW guard across all sibling aliases, else atomics/partition.
func objectGuardedRoots(roots []sharedRoot, funcs map[*ssa.Function]bool) bool {
	if len(roots) >= maxRootAliases {
		return false
	}
	var accesses []dataAccess
	seenAcc := map[ssa.Instruction]bool{}
	visiting := map[ssa.Value]bool{}
	var dataRoots []ssa.Value
	for _, root := range roots {
		if isChanType(root.val.Type()) || isWhitelistedSync(root.val) || isSyncMutex(root.val) || isSyncRWMutex(root.val) || isShareSafeStdlib(root.val) {
			continue
		}
		dataRoots = append(dataRoots, root.val)
		for _, acc := range collectDataAccessesDeep(root.val, funcs, visiting) {
			if seenAcc[acc.instr] {
				continue
			}
			seenAcc[acc.instr] = true
			accesses = append(accesses, acc)
		}
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
	if atomicsOnlyAccesses(accesses) {
		return true
	}
	// Partition over the union of sibling-alias accesses so all writer
	// goroutines are considered together.
	if len(dataRoots) > 0 && constIndexPartitionOK(dataRoots[0], accesses) {
		return true
	}
	return false
}

// freeStandingMutexGuards accepts when one package-level Mutex (or RWMutex if
// rw) Global is held at every access with the right mode.
func freeStandingMutexGuards(accesses []dataAccess, funcs map[*ssa.Function]bool, rw bool) bool {
	cands := packageMutexGlobals(funcs, rw)
	if len(cands) == 0 {
		return false
	}
	heldCache := map[*ssa.Function]map[ssa.Instruction]holdSet{}
	// Candidates that appear held at least once.
	var used []*ssa.Global
	for _, gl := range cands {
		key := guardKey{base: gl, field: -1}
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
				used = append(used, gl)
				break
			}
		}
	}
	for _, gl := range used {
		key := guardKey{base: gl, field: -1}
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

func packageMutexGlobals(funcs map[*ssa.Function]bool, rw bool) []*ssa.Global {
	seen := map[*ssa.Global]bool{}
	var out []*ssa.Global
	want := "Mutex"
	if rw {
		want = "RWMutex"
	}
	for fn := range funcs {
		if fn == nil || fn.Pkg == nil {
			continue
		}
		for _, mem := range fn.Pkg.Members {
			gl, ok := mem.(*ssa.Global)
			if !ok || seen[gl] {
				continue
			}
			if isNamedSyncType(gl.Type(), want) {
				seen[gl] = true
				out = append(out, gl)
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

// atomicsOnlyAccesses reports whether every access is via sync/atomic.
func atomicsOnlyAccesses(accesses []dataAccess) bool {
	if len(accesses) == 0 {
		return false
	}
	for _, acc := range accesses {
		if !isAtomicAccess(acc) {
			return false
		}
	}
	return true
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
