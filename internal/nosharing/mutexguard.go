package nosharing

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// guardKey identifies a sync.Mutex / sync.RWMutex guard within one function's
// SSA. field >= 0 is a struct field of base; field == -1 is a free-standing
// package-level Mutex/RWMutex Global (base itself).
type guardKey struct {
	base  ssa.Value
	field int
}

// holdMode is the strength of a must-held guard.
type holdMode uint8

const (
	holdRead  holdMode = 1 // RLock (or better)
	holdWrite holdMode = 2 // Lock / exclusive
)

// structuralGuard is a mutex field of a named/anonymous struct type,
// independent of any particular SSA base. Used to record which field
// indices are candidate guards for a shared root.
type structuralGuard struct {
	structType types.Type // the struct (not pointer) containing the mutex
	field      int
	rw         bool // sync.RWMutex field (vs sync.Mutex)
}

// findStructuralGuards returns sync.Mutex fields in the struct pointed at
// by root, and in any parent structs visible via a FieldAddr chain.
func findStructuralGuards(root ssa.Value) []structuralGuard {
	return findStructuralGuardsKind(root, false)
}

// findStructuralRWGuards returns sync.RWMutex fields in the struct pointed at
// by root, and in any parent structs visible via a FieldAddr chain.
func findStructuralRWGuards(root ssa.Value) []structuralGuard {
	return findStructuralGuardsKind(root, true)
}

func findStructuralGuardsKind(root ssa.Value, rw bool) []structuralGuard {
	var out []structuralGuard
	seen := map[string]bool{} // type+field dedupe
	add := func(structT types.Type, field int) {
		key := structT.String() + "#" + itoa(field)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, structuralGuard{structType: structT, field: field, rw: rw})
	}
	fieldsOf := mutexFields
	if rw {
		fieldsOf = rwMutexFields
	}

	if st := structOf(root.Type()); st != nil {
		for _, fi := range fieldsOf(st) {
			add(st, fi)
		}
	}

	for cur := root; cur != nil; {
		switch v := cur.(type) {
		case *ssa.UnOp:
			if v.Op == token.MUL {
				cur = v.X
				continue
			}
			return out
		case *ssa.ChangeType:
			cur = v.X
			continue
		case *ssa.Convert:
			cur = v.X
			continue
		case *ssa.FieldAddr:
			if st := structOf(v.X.Type()); st != nil {
				for _, fi := range fieldsOf(st) {
					add(st, fi)
				}
			}
			cur = v.X
		case *ssa.IndexAddr, *ssa.Slice:
			return out
		default:
			return out
		}
	}
	return out
}

// mutexGuardsGoRoots proves that every data access through any of the go's
// shared roots is covered by the north-star guard cascade (tied Mutex,
// free-standing package Mutex/RWMutex, RWMutex discipline, atomics-only,
// or const-index partitioned writers).
func mutexGuardsGoRoots(roots []sharedRoot, funcs map[*ssa.Function]bool) bool {
	return objectGuardedRoots(roots, funcs)
}

// mutexGuardsAccesses reports whether every load/store of root's memory in
// funcs is covered by the north-star guard cascade.
func mutexGuardsAccesses(root ssa.Value, funcs map[*ssa.Function]bool) bool {
	return objectGuarded(root, funcs)
}

// hasTiedMutex reports whether there exists one structural mutex field that
// protects every access (must be the same tied guard across all touchpoints).
func hasTiedMutex(candidates []structuralGuard, accesses []dataAccess) bool {
	_, ok := findTiedMutex(candidates, accesses)
	return ok
}

// findTiedMutex returns one structural mutex field that protects every access.
func findTiedMutex(candidates []structuralGuard, accesses []dataAccess) (structuralGuard, bool) {
	heldCache := map[*ssa.Function]map[ssa.Instruction]holdSet{}
	for _, c := range candidates {
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
			at := held[acc.instr]
			if at == nil || !accessProtectedBy(acc, at, c) {
				ok = false
				break
			}
		}
		if ok {
			return c, true
		}
	}
	return structuralGuard{}, false
}

// holdSet is the must-held guards at a program point (mode = read or write).
type holdSet map[guardKey]holdMode

// collectDataAccessesDeep collects data accesses through root and through
// same-package callees that receive root-derived values, so a single tied
// mutex can be required across all touchpoints.
func collectDataAccessesDeep(root ssa.Value, funcs map[*ssa.Function]bool, visiting map[ssa.Value]bool) []dataAccess {
	if root == nil || visiting[root] {
		return nil
	}
	visiting[root] = true

	var out []dataAccess
	seen := map[ssa.Instruction]bool{}
	addAll := func(accs []dataAccess) {
		for _, acc := range accs {
			if seen[acc.instr] {
				continue
			}
			seen[acc.instr] = true
			out = append(out, acc)
		}
	}
	addAll(collectDataAccesses(root, funcs))

	derived := deriveAddrs(root, funcs)
	for fn := range funcs {
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
				case *ssa.Go:
					c = in.Common()
				default:
					continue
				}
				cal := c.StaticCallee()
				if cal == nil || len(cal.Blocks) == 0 || cal.Pkg != fn.Pkg {
					continue
				}
				if isSyncMutexMethod(cal) || isRWMutexMethod(cal) {
					continue
				}
				for i, arg := range c.Args {
					if !derived[arg] || isMutexFieldAddr(arg) {
						continue
					}
					if i >= len(cal.Params) {
						continue
					}
					calFuncs := reachableFuncs(cal, cal.Pkg)
					addAll(collectDataAccessesDeep(cal.Params[i], calFuncs, visiting))
				}
			}
		}
	}
	return out
}

type dataAccess struct {
	instr ssa.Instruction
	addr  ssa.Value // address or map value being accessed
	write bool
}

// collectDataAccesses finds loads and stores through root (excluding
// accesses that are solely the sync.Mutex/RWMutex field used for Lock/Unlock).
// It scans instructions directly so Global roots (which lack Referrers)
// are handled uniformly.
func collectDataAccesses(root ssa.Value, funcs map[*ssa.Function]bool) []dataAccess {
	var out []dataAccess
	seen := map[ssa.Instruction]bool{}
	derived := deriveAddrs(root, funcs)

	add := func(instr ssa.Instruction, addr ssa.Value, write bool) {
		if instr == nil || seen[instr] {
			return
		}
		if isMutexFieldAddr(addr) {
			return
		}
		seen[instr] = true
		out = append(out, dataAccess{instr: instr, addr: addr, write: write})
	}

	checkCall := func(instr ssa.Instruction, c *ssa.CallCommon) {
		if isMutexGuardCall(c) {
			return
		}
		if !c.IsInvoke() {
			if b, ok := c.Value.(*ssa.Builtin); ok {
				switch b.Name() {
				case "len", "cap":
					for _, arg := range c.Args {
						if derived[arg] {
							add(instr, arg, false)
							return
						}
					}
					return
				case "append", "copy", "clear", "delete":
					for _, arg := range c.Args {
						if derived[arg] {
							add(instr, arg, true)
							return
						}
					}
					return
				}
			}
		}
		if cal := c.StaticCallee(); cal != nil {
			if isWhitelistedSyncMethod(cal, recvOfCall(c)) || isRWMutexMethod(cal) || isSyncMutexMethod(cal) {
				return
			}
			if isStdlibReadOnlyCall(cal) {
				for _, arg := range c.Args {
					if derived[arg] && !isMutexFieldAddr(arg) {
						add(instr, arg, false) // value copy / read-only helper
						return
					}
				}
				return
			}
			if isAtomicCallee(cal) || (cal.Signature.Recv() != nil && isAtomicValueType(cal.Signature.Recv().Type())) {
				for _, arg := range c.Args {
					if derived[arg] && !isMutexFieldAddr(arg) {
						add(instr, arg, true)
						return
					}
				}
				return
			}
			if len(cal.Blocks) > 0 && instr.Parent() != nil && calleeInPkg(cal, instr.Parent().Pkg) {
				// Same-package callees are checked via their own parameters
				// in collectDataAccessesDeep.
				return
			}
		}
		for _, arg := range c.Args {
			if derived[arg] && !isMutexFieldAddr(arg) {
				// Value copies (Field extract / non-indirect load) are reads.
				if isValueCopyArg(arg) {
					add(instr, arg, false)
					return
				}
				add(instr, arg, true)
				return
			}
		}
		if c.IsInvoke() && derived[c.Value] {
			add(instr, c.Value, true)
		}
	}

	for fn := range funcs {
		if fn == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				switch in := instr.(type) {
				case *ssa.Store:
					if derived[in.Addr] {
						add(in, in.Addr, true)
					}
				case *ssa.UnOp:
					if in.Op == token.MUL && derived[in.X] {
						add(in, in.X, false)
					}
				case *ssa.MapUpdate:
					if derived[in.Map] {
						add(in, in.Map, true)
					}
				case *ssa.Lookup:
					if derived[in.X] {
						add(in, in.X, false)
					}
				case *ssa.Range:
					if derived[in.X] {
						add(in, in.X, false)
					}
				case *ssa.Call:
					checkCall(in, in.Common())
				case *ssa.Defer:
					checkCall(in, in.Common())
				case *ssa.Go:
					checkCall(in, in.Common())
				}
			}
		}
	}
	return out
}

func accessProtectedBy(acc dataAccess, held holdSet, tied structuralGuard) bool {
	tiedKey := tied.structType.String() + "#" + itoa(tied.field)
	for g, mode := range held {
		if g.field < 0 {
			continue
		}
		st := structOf(g.base.Type())
		if st == nil {
			continue
		}
		if st.String()+"#"+itoa(g.field) != tiedKey {
			continue
		}
		if !modeOKForAccess(mode, acc.write, tied.rw) {
			continue
		}
		if baseCoversAddr(g.base, acc.addr) {
			return true
		}
	}
	return false
}

func modeOKForAccess(mode holdMode, write, rw bool) bool {
	if write {
		return mode == holdWrite
	}
	if rw {
		return mode == holdRead || mode == holdWrite
	}
	// sync.Mutex: reads also require Lock.
	return mode == holdWrite
}

// baseCoversAddr reports whether base is the object identity of addr or of
// a FieldAddr parent of addr.
func baseCoversAddr(base, addr ssa.Value) bool {
	if stripToObject(addr) == base {
		return true
	}
	for cur := addr; cur != nil; {
		switch v := cur.(type) {
		case *ssa.FieldAddr:
			if stripToObject(v.X) == base {
				return true
			}
			cur = v.X
		case *ssa.UnOp:
			if v.Op == token.MUL {
				cur = v.X
				continue
			}
			return false
		case *ssa.IndexAddr:
			if stripToObject(v.X) == base {
				return true
			}
			cur = v.X
		default:
			return stripToObject(cur) == base
		}
	}
	return false
}

// tryAcq is a TryLock/TryRLock result that acquires a guard on the true path.
type tryAcq struct {
	g    guardKey
	mode holdMode
}

// analyzeMustHold returns, for each instruction, the set of guards that are
// definitely held just before that instruction executes.
//
// Lock/RLock acquire immediately. TryLock/TryRLock acquire only on CFG edges
// where their boolean result is proven true.
func analyzeMustHold(fn *ssa.Function) map[ssa.Instruction]holdSet {
	result := map[ssa.Instruction]holdSet{}
	if fn == nil || len(fn.Blocks) == 0 {
		return result
	}

	universe := discoverGuardsInFunc(fn)
	tryResults := discoverTryLockResults(fn)

	blockOut := make([]holdSet, len(fn.Blocks))
	blockIn := make([]holdSet, len(fn.Blocks))

	// TOP = full universe at exclusive strength for must-analysis init.
	top := cloneGuardSet(universe)
	for i := range fn.Blocks {
		blockIn[fn.Blocks[i].Index] = cloneGuardSet(top)
		blockOut[fn.Blocks[i].Index] = cloneGuardSet(top)
	}
	entry := fn.Blocks[0]
	// Unlock/RUnlock wrappers run while the caller still holds the lock;
	// model that hold on entry so bookkeeping under the critical section
	// (before the inner Unlock) is recognized.
	if init := wrapperEntryHold(fn); init != nil {
		blockIn[entry.Index] = init
	} else {
		blockIn[entry.Index] = holdSet{}
	}

	changed := true
	for changed {
		changed = false
		for _, b := range fn.Blocks {
			var in holdSet
			if len(b.Preds) == 0 {
				if b == entry {
					in = cloneGuardSet(blockIn[entry.Index])
				} else {
					in = holdSet{}
				}
			} else {
				in = edgeHold(b.Preds[0], b, blockOut, tryResults)
				for _, p := range b.Preds[1:] {
					in = intersectGuards(in, edgeHold(p, b, blockOut, tryResults))
				}
			}
			if !guardSetEqual(in, blockIn[b.Index]) {
				blockIn[b.Index] = in
				changed = true
			}

			cur := cloneGuardSet(in)
			for _, instr := range b.Instrs {
				result[instr] = cloneGuardSet(cur)
				cur = transferHold(cur, instr, universe)
			}
			if !guardSetEqual(cur, blockOut[b.Index]) {
				blockOut[b.Index] = cur
				changed = true
			}
		}
	}
	return result
}

// discoverTryLockResults maps SSA values that are the boolean result of
// TryLock/TryRLock to the guard+mode held on success.
func discoverTryLockResults(fn *ssa.Function) map[ssa.Value]tryAcq {
	out := map[ssa.Value]tryAcq{}
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			call, ok := instr.(*ssa.Call)
			if !ok {
				continue
			}
			name := mutexMethodName(call.Common())
			var mode holdMode
			switch name {
			case "TryLock":
				mode = holdWrite
			case "TryRLock":
				mode = holdRead
			default:
				continue
			}
			g, ok := lockUnlockGuard(call.Common())
			if !ok {
				continue
			}
			out[call] = tryAcq{g: g, mode: mode}
		}
	}
	return out
}

// edgeHold is the held set flowing from pred into succ, including TryLock
// acquisition on the branch where the TryLock result is proven true.
func edgeHold(pred, succ *ssa.BasicBlock, blockOut []holdSet, tryResults map[ssa.Value]tryAcq) holdSet {
	out := cloneGuardSet(blockOut[pred.Index])
	if len(pred.Instrs) == 0 {
		return out
	}
	iff, ok := pred.Instrs[len(pred.Instrs)-1].(*ssa.If)
	if !ok {
		return out
	}
	acq, heldOnTrue, ok := tryLockCond(iff.Cond, tryResults)
	if !ok {
		return out
	}
	isTrueSucc := len(pred.Succs) > 0 && pred.Succs[0] == succ
	isFalseSucc := len(pred.Succs) > 1 && pred.Succs[1] == succ
	if heldOnTrue && isTrueSucc {
		strengthenHold(out, acq.g, acq.mode)
	}
	if !heldOnTrue && isFalseSucc {
		strengthenHold(out, acq.g, acq.mode)
	}
	return out
}

// tryLockCond reports whether cond is a TryLock/TryRLock result (possibly negated).
func tryLockCond(cond ssa.Value, tryResults map[ssa.Value]tryAcq) (acq tryAcq, heldOnTrue bool, ok bool) {
	negated := false
	for cond != nil {
		if tg, found := tryResults[cond]; found {
			return tg, !negated, true
		}
		switch v := cond.(type) {
		case *ssa.UnOp:
			if v.Op == token.NOT {
				negated = !negated
				cond = v.X
				continue
			}
			return tryAcq{}, false, false
		case *ssa.ChangeType:
			cond = v.X
		case *ssa.Convert:
			cond = v.X
		default:
			return tryAcq{}, false, false
		}
	}
	return tryAcq{}, false, false
}

func discoverGuardsInFunc(fn *ssa.Function) holdSet {
	u := holdSet{}
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			var common *ssa.CallCommon
			switch in := instr.(type) {
			case *ssa.Call:
				common = in.Common()
			case *ssa.Defer:
				common = in.Common()
			}
			if common == nil {
				continue
			}
			if g, ok := lockUnlockGuard(common); ok {
				// Universe uses exclusive strength as TOP for each guard.
				u[g] = holdWrite
			}
		}
	}
	return u
}

func transferHold(in holdSet, instr ssa.Instruction, universe holdSet) holdSet {
	out := cloneGuardSet(in)
	switch in := instr.(type) {
	case *ssa.Call:
		applyCallHold(out, in.Common(), false, universe)
	case *ssa.Defer:
		// defer Unlock/RUnlock does not kill: it runs at return.
		// defer Lock is pathological; ignore as gen too.
		applyCallHold(out, in.Common(), true, universe)
	}
	return out
}

func applyCallHold(held holdSet, c *ssa.CallCommon, isDefer bool, universe holdSet) {
	if c == nil {
		return
	}
	_ = universe
	if g, ok := lockUnlockGuard(c); ok {
		name := mutexMethodName(c)
		switch name {
		case "Lock":
			if !isDefer {
				held[g] = holdWrite
			}
		case "RLock":
			if !isDefer {
				strengthenHold(held, g, holdRead)
			}
		case "TryLock", "TryRLock":
			// Acquisition is modeled on the CFG edge where the boolean
			// result is proven true; the call itself does not gen.
		case "Unlock", "RUnlock":
			if !isDefer {
				delete(held, g)
			}
		}
		return
	}

	// Deferred non-mutex calls run at function exit. A deferred closure that
	// does resp.Body.Close() must not drop freemu holds for the rest of the
	// body (filUsdPrice pattern).
	if isDefer {
		return
	}

	if !c.IsInvoke() {
		if _, ok := c.Value.(*ssa.Builtin); ok {
			return
		}
	}

	callee := c.StaticCallee()
	if callee != nil && len(callee.Blocks) > 0 {
		for g := range held {
			if calleeMayUnlock(callee, g, map[*ssa.Function]bool{}) {
				delete(held, g)
			}
		}
		return
	}
	if callee != nil {
		if isSyncMutexMethod(callee) || isRWMutexMethod(callee) ||
			isWhitelistedSyncMethod(callee, recvOfCall(c)) {
			return
		}
	}
	for _, arg := range c.Args {
		killEscaping(held, arg)
	}
	if c.IsInvoke() && c.Value != nil {
		killEscaping(held, c.Value)
	}
	for g := range held {
		if gl, ok := g.base.(*ssa.Global); ok && token.IsExported(gl.Name()) {
			delete(held, g)
		}
	}
}

func strengthenHold(held holdSet, g guardKey, mode holdMode) {
	if cur, ok := held[g]; ok && cur >= mode {
		return
	}
	held[g] = mode
}

// calleeMayUnlock reports whether fn (transitively, through same-program
// static calls) may unlock the guard g: an Unlock on the same base, an
// Unlock on the same struct type + field with an unprovable base, a dynamic
// call, or an escape of g's base to an unknown callee.
func calleeMayUnlock(fn *ssa.Function, g guardKey, visited map[*ssa.Function]bool) bool {
	if fn == nil || visited[fn] {
		return false
	}
	visited[fn] = true
	gStruct := structOf(g.base.Type())

	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			var c *ssa.CallCommon
			switch in := instr.(type) {
			case *ssa.Call:
				c = in.Common()
			case *ssa.Defer:
				c = in.Common() // deferred Unlock runs before caller resumes
			case *ssa.Go:
				c = in.Common()
			default:
				continue
			}
			if !c.IsInvoke() {
				if _, ok := c.Value.(*ssa.Builtin); ok {
					continue
				}
			}
			if isMutexGuardCall(c) {
				name := mutexMethodName(c)
				if name != "Unlock" && name != "RUnlock" {
					continue
				}
				ug, ok := lockUnlockGuard(c)
				if !ok {
					return true // unlock through an alias we cannot identify
				}
				if ug.base == g.base && ug.field == g.field {
					return true
				}
				if g.field >= 0 && ug.field == g.field && gStruct != nil {
					if st := structOf(ug.base.Type()); st != nil && st.String() == gStruct.String() {
						return true // same struct type+field, unknown base
					}
				}
				continue
			}
			if c.IsInvoke() {
				return true
			}
			cal := c.StaticCallee()
			if cal == nil {
				return true
			}
			if len(cal.Blocks) > 0 {
				if calleeMayUnlock(cal, g, visited) {
					return true
				}
				continue
			}
			if isSyncMutexMethod(cal) || isRWMutexMethod(cal) ||
				isWhitelistedSyncMethod(cal, recvOfCall(c)) {
				continue
			}
			// Bodyless cross-package callee: dangerous if given the guard's base.
			for _, arg := range c.Args {
				if !mayContainPointers(arg.Type()) {
					continue
				}
				if stripToObject(arg) == g.base {
					return true
				}
			}
		}
	}
	return false
}

func recvOfCall(c *ssa.CallCommon) ssa.Value {
	if c.IsInvoke() {
		return c.Value
	}
	if len(c.Args) > 0 {
		return c.Args[0]
	}
	return nil
}

func killEscaping(held holdSet, v ssa.Value) {
	obj := stripToObject(v)
	for g := range held {
		if g.base == obj || baseCoversAddr(g.base, v) || stripToObject(v) == g.base {
			delete(held, g)
		}
		// Also: if arg is the mutex FieldAddr itself.
		if fa, ok := v.(*ssa.FieldAddr); ok && fa.Field == g.field && stripToObject(fa.X) == g.base {
			delete(held, g)
		}
		// Free-standing global mutex passed by address.
		if g.field < 0 && obj == g.base {
			delete(held, g)
		}
	}
}

func lockUnlockGuard(c *ssa.CallCommon) (guardKey, bool) {
	name := mutexMethodName(c)
	switch name {
	case "Lock", "Unlock", "TryLock", "RLock", "RUnlock", "TryRLock":
	default:
		return guardKey{}, false
	}
	recv := mutexRecv(c)
	if recv == nil {
		return guardKey{}, false
	}
	if fa, ok := recv.(*ssa.FieldAddr); ok {
		isMu := isNamedSyncType(fa.Type(), "Mutex")
		isRW := isNamedSyncType(fa.Type(), "RWMutex")
		if !isMu && !isRW {
			return guardKey{}, false
		}
		if isMu && (name == "RLock" || name == "RUnlock" || name == "TryRLock") {
			return guardKey{}, false
		}
		return guardKey{base: stripToObject(fa.X), field: fa.Field}, true
	}
	// Free-standing package-level Mutex / *Mutex / RWMutex / *RWMutex.
	base := recv
	if u, ok := recv.(*ssa.UnOp); ok && u.Op == token.MUL {
		base = u.X
	}
	base = stripToObject(base)
	if gl, ok := base.(*ssa.Global); ok {
		isMu := isNamedSyncType(gl.Type(), "Mutex")
		isRW := isNamedSyncType(gl.Type(), "RWMutex")
		if isMu || isRW {
			if isMu && (name == "RLock" || name == "RUnlock" || name == "TryRLock") {
				return guardKey{}, false
			}
			return guardKey{base: gl, field: -1}, true
		}
	}
	// Custom Lock/Unlock/RLock/RUnlock methods that wrap a tied field.
	return wrapperLockGuard(c, name, base)
}

// wrapperEntryHold returns the must-held set at entry to an Unlock/RUnlock
// wrapper method (caller still holds the tied field).
func wrapperEntryHold(fn *ssa.Function) holdSet {
	if fn == nil || fn.Signature.Recv() == nil || len(fn.Params) == 0 {
		return nil
	}
	name := fn.Name()
	switch name {
	case "Unlock", "RUnlock":
	default:
		return nil
	}
	field, _, ok := mutexWrapperField(fn, name)
	if !ok {
		return nil
	}
	mode := holdWrite
	if name == "RUnlock" {
		mode = holdRead
	}
	return holdSet{guardKey{base: fn.Params[0], field: field}: mode}
}

// wrapperLockGuard recognizes user methods named Lock/Unlock/RLock/RUnlock
// whose body always acquires or releases one tied sync.Mutex/RWMutex field
// of the receiver (e.g. embedding sync.Mutex and redefining Lock to add
// bookkeeping). TryLock/TryRLock wrappers are not modeled.
func wrapperLockGuard(c *ssa.CallCommon, name string, base ssa.Value) (guardKey, bool) {
	switch name {
	case "Lock", "Unlock", "RLock", "RUnlock":
	default:
		return guardKey{}, false
	}
	if c.IsInvoke() || base == nil {
		return guardKey{}, false
	}
	cal := c.StaticCallee()
	if cal == nil || isSyncMutexMethod(cal) || isRWMutexMethod(cal) {
		return guardKey{}, false
	}
	field, isRW, ok := mutexWrapperField(cal, name)
	if !ok {
		return guardKey{}, false
	}
	if !isRW && (name == "RLock" || name == "RUnlock") {
		return guardKey{}, false
	}
	return guardKey{base: base, field: field}, true
}

type wrapperField struct {
	field int
	isRW  bool
}

// mutexWrapperField reports the tied Mutex/RWMutex field that cal always
// locks or unlocks on its receiver via a direct sync method. Fail closed
// on Try*, nested non-sync wrappers, or disagreeing fields/paths.
func mutexWrapperField(cal *ssa.Function, name string) (field int, isRW bool, ok bool) {
	if cal == nil || len(cal.Blocks) == 0 || cal.Signature.Recv() == nil || len(cal.Params) == 0 {
		return 0, false, false
	}
	switch name {
	case "Lock", "Unlock", "RLock", "RUnlock":
	default:
		return 0, false, false
	}
	recv := cal.Params[0]
	opsByBlock := make([]map[wrapperField]bool, len(cal.Blocks))
	releaseByBlock := make([]map[wrapperField]bool, len(cal.Blocks))
	candidates := map[wrapperField]bool{}

	for _, b := range cal.Blocks {
		opsByBlock[b.Index] = map[wrapperField]bool{}
		releaseByBlock[b.Index] = map[wrapperField]bool{}
		for _, instr := range b.Instrs {
			fk, opName, found := recvMutexFieldOp(instr, recv)
			if !found {
				continue
			}
			if opName == "TryLock" || opName == "TryRLock" {
				return 0, false, false
			}
			if opName == name {
				opsByBlock[b.Index][fk] = true
				candidates[fk] = true
			}
			if opName == "Unlock" || opName == "RUnlock" {
				releaseByBlock[b.Index][fk] = true
			}
		}
	}
	if len(candidates) == 0 {
		return 0, false, false
	}
	for fk := range candidates {
		if (name == "Lock" || name == "RLock") && wrapperBlockHas(releaseByBlock, fk) {
			continue // acquire wrapper must not release
		}
		if wrapperMustPerform(cal, recv, name, fk, opsByBlock) {
			return fk.field, fk.isRW, true
		}
	}
	return 0, false, false
}

func wrapperBlockHas(byBlock []map[wrapperField]bool, fk wrapperField) bool {
	for _, m := range byBlock {
		if m[fk] {
			return true
		}
	}
	return false
}

// wrapperMustPerform reports whether every path to a Return performs name on
// fk (direct sync op on the receiver field). Defer counts as performing the
// op before the function returns to its caller.
func wrapperMustPerform(cal *ssa.Function, recv ssa.Value, name string, fk wrapperField, opsByBlock []map[wrapperField]bool) bool {
	n := len(cal.Blocks)
	if n == 0 {
		return false
	}
	seenIn := make([]bool, n)
	seenOut := make([]bool, n)
	for i := range seenOut {
		seenOut[i] = true // TOP for must-analysis
	}
	entry := cal.Blocks[0]
	seenOut[entry.Index] = false

	changed := true
	for changed {
		changed = false
		for _, b := range cal.Blocks {
			var in bool
			if len(b.Preds) == 0 {
				in = false
			} else {
				in = true
				for _, p := range b.Preds {
					if !seenOut[p.Index] {
						in = false
						break
					}
				}
			}
			out := in || opsByBlock[b.Index][fk]
			if in != seenIn[b.Index] || out != seenOut[b.Index] {
				seenIn[b.Index] = in
				seenOut[b.Index] = out
				changed = true
			}
		}
	}

	hasRet := false
	for _, b := range cal.Blocks {
		seen := seenIn[b.Index]
		for _, instr := range b.Instrs {
			if _, isRet := instr.(*ssa.Return); isRet {
				hasRet = true
				if !seen {
					return false
				}
			}
			if fk2, opName, found := recvMutexFieldOp(instr, recv); found && opName == name && fk2 == fk {
				seen = true
			}
		}
	}
	return hasRet
}

// recvMutexFieldOp reports a direct sync.Mutex/RWMutex Lock/Unlock/… on a
// FieldAddr of recv. Nested user wrappers are ignored (fail closed).
func recvMutexFieldOp(instr ssa.Instruction, recv ssa.Value) (wrapperField, string, bool) {
	var common *ssa.CallCommon
	switch in := instr.(type) {
	case *ssa.Call:
		common = in.Common()
	case *ssa.Defer:
		common = in.Common()
	default:
		return wrapperField{}, "", false
	}
	opName := mutexMethodName(common)
	switch opName {
	case "Lock", "Unlock", "RLock", "RUnlock", "TryLock", "TryRLock":
	default:
		return wrapperField{}, "", false
	}
	sc := common.StaticCallee()
	if sc == nil || (!isSyncMutexMethod(sc) && !isRWMutexMethod(sc)) {
		return wrapperField{}, "", false
	}
	mrecv := mutexRecv(common)
	fa, isFA := mrecv.(*ssa.FieldAddr)
	if !isFA || stripToObject(fa.X) != recv {
		return wrapperField{}, "", false
	}
	isMu := isNamedSyncType(fa.Type(), "Mutex")
	isRWField := isNamedSyncType(fa.Type(), "RWMutex")
	if !isMu && !isRWField {
		return wrapperField{}, "", false
	}
	return wrapperField{field: fa.Field, isRW: isRWField}, opName, true
}

func mutexRecv(c *ssa.CallCommon) ssa.Value {
	if c.IsInvoke() {
		return c.Value
	}
	if len(c.Args) > 0 {
		return c.Args[0]
	}
	return nil
}

func mutexMethodName(c *ssa.CallCommon) string {
	if c.IsInvoke() {
		return c.Method.Name()
	}
	if fn := c.StaticCallee(); fn != nil {
		return fn.Name()
	}
	return ""
}

func isMutexLockUnlockCall(c *ssa.CallCommon) bool {
	name := mutexMethodName(c)
	switch name {
	case "Lock", "Unlock", "TryLock":
		recv := mutexRecv(c)
		return recv != nil && isNamedSyncType(recv.Type(), "Mutex")
	}
	return false
}

// isMutexGuardCall reports Lock/Unlock/RLock/RUnlock/Try* on Mutex or RWMutex.
func isMutexGuardCall(c *ssa.CallCommon) bool {
	_, ok := lockUnlockGuard(c)
	return ok
}

func isMutexFieldAddr(v ssa.Value) bool {
	fa, ok := v.(*ssa.FieldAddr)
	if !ok {
		return false
	}
	return isNamedSyncType(fa.Type(), "Mutex", "RWMutex")
}

// isValueCopyArg reports an SSA value that is a non-addressable copy of data
// (Field extract or load of a non-indirect type), not a pointer/header cell.
func isValueCopyArg(v ssa.Value) bool {
	if v == nil {
		return false
	}
	if _, ok := v.(*ssa.Field); ok {
		return true
	}
	if u, ok := v.(*ssa.UnOp); ok && u.Op == token.MUL {
		return !typeIsIndirect(u.Type())
	}
	return false
}

// stripToObject walks address computations down to Alloc/Param/FreeVar/Global.
func stripToObject(v ssa.Value) ssa.Value {
	for v != nil {
		switch x := v.(type) {
		case *ssa.FieldAddr:
			v = x.X
		case *ssa.IndexAddr:
			v = x.X
		case *ssa.UnOp:
			if x.Op == token.MUL {
				v = x.X
				continue
			}
			return v
		case *ssa.ChangeType:
			v = x.X
		case *ssa.Convert:
			v = x.X
		case *ssa.Slice:
			v = x.X
		case *ssa.Extract:
			return v
		case *ssa.Alloc, *ssa.Parameter, *ssa.FreeVar, *ssa.Global:
			return v
		default:
			return v
		}
	}
	return v
}

func structOf(t types.Type) *types.Struct {
	t = types.Unalias(t)
	if p, ok := t.(*types.Pointer); ok {
		t = types.Unalias(p.Elem())
	}
	switch t := t.(type) {
	case *types.Struct:
		return t
	case *types.Named:
		if s, ok := t.Underlying().(*types.Struct); ok {
			return s
		}
	}
	return nil
}

func mutexFields(st *types.Struct) []int {
	var out []int
	for i := 0; i < st.NumFields(); i++ {
		if isNamedSyncType(st.Field(i).Type(), "Mutex") {
			out = append(out, i)
		}
	}
	return out
}

func rwMutexFields(st *types.Struct) []int {
	var out []int
	for i := 0; i < st.NumFields(); i++ {
		if isNamedSyncType(st.Field(i).Type(), "RWMutex") {
			out = append(out, i)
		}
	}
	return out
}

// hasStructuralRWMutex reports whether root's pointed-to struct (or a FieldAddr
// parent) embeds a sync.RWMutex. Used to give RWMutex-specific refusal messaging
// when a tied sync.Mutex proof fails.
func hasStructuralRWMutex(root ssa.Value) bool {
	if root == nil {
		return false
	}
	if st := structOf(root.Type()); st != nil && len(rwMutexFields(st)) > 0 {
		return true
	}
	for cur := root; cur != nil; {
		switch v := cur.(type) {
		case *ssa.UnOp:
			if v.Op == token.MUL {
				cur = v.X
				continue
			}
			return false
		case *ssa.ChangeType:
			cur = v.X
			continue
		case *ssa.Convert:
			cur = v.X
			continue
		case *ssa.FieldAddr:
			if st := structOf(v.X.Type()); st != nil && len(rwMutexFields(st)) > 0 {
				return true
			}
			cur = v.X
		default:
			return false
		}
	}
	return false
}

func cloneGuardSet(s holdSet) holdSet {
	if s == nil {
		return holdSet{}
	}
	out := make(holdSet, len(s))
	for k, v := range s {
		if v != 0 {
			out[k] = v
		}
	}
	return out
}

func intersectGuards(a, b holdSet) holdSet {
	out := holdSet{}
	for k, am := range a {
		if bm, ok := b[k]; ok {
			m := am
			if bm < m {
				m = bm
			}
			if m != 0 {
				out[k] = m
			}
		}
	}
	return out
}

func guardSetEqual(a, b holdSet) bool {
	if len(a) != len(b) {
		return false
	}
	for k, am := range a {
		if b[k] != am {
			return false
		}
	}
	return true
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// isSyncMutex reports whether v's type is sync.Mutex or *sync.Mutex.
func isSyncMutex(v ssa.Value) bool {
	return isNamedSyncType(v.Type(), "Mutex")
}

// isSyncRWMutex reports whether v's type is sync.RWMutex or *sync.RWMutex.
func isSyncRWMutex(v ssa.Value) bool {
	return isNamedSyncType(v.Type(), "RWMutex")
}
