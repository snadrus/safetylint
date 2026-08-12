package nosharing

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// guardKey identifies a sync.Mutex field of a particular base object within
// one function's SSA. Two sites refer to the same guard only when their
// bases are the same Alloc/Param/FreeVar/Global (after stripping).
type guardKey struct {
	base  ssa.Value
	field int
}

// structuralGuard is a mutex field of a named/anonymous struct type,
// independent of any particular SSA base. Used to record which field
// indices are candidate guards for a shared root.
type structuralGuard struct {
	structType types.Type // the struct (not pointer) containing the mutex
	field      int
}

// findStructuralGuards returns sync.Mutex fields in the struct pointed at
// by root, and in any parent structs visible via a FieldAddr chain.
func findStructuralGuards(root ssa.Value) []structuralGuard {
	var out []structuralGuard
	seen := map[string]bool{} // type+field dedupe
	add := func(structT types.Type, field int) {
		key := structT.String() + "#" + itoa(field)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, structuralGuard{structType: structT, field: field})
	}

	// Mutexes in the type root points to.
	if st := structOf(root.Type()); st != nil {
		for _, fi := range mutexFields(st) {
			add(st, fi)
		}
	}

	// Walk outward through FieldAddr parents.
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
				for _, fi := range mutexFields(st) {
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
// shared roots is covered by one consistent tied sync.Mutex field in the
// same/parent struct (the same structural guard at every touchpoint,
// including same-package callees).
func mutexGuardsGoRoots(roots []sharedRoot, funcs map[*ssa.Function]bool) bool {
	var candidates []structuralGuard
	seenCand := map[string]bool{}
	var accesses []dataAccess
	seenAcc := map[ssa.Instruction]bool{}
	visiting := map[ssa.Value]bool{}

	for _, root := range roots {
		if isChanType(root.val.Type()) || isWhitelistedSync(root.val) || isSyncMutex(root.val) || isSyncRWMutex(root.val) {
			continue
		}
		for _, c := range findStructuralGuards(root.val) {
			key := c.structType.String() + "#" + itoa(c.field)
			if seenCand[key] {
				continue
			}
			seenCand[key] = true
			candidates = append(candidates, c)
		}
		for _, acc := range collectDataAccessesDeep(root.val, funcs, visiting) {
			if seenAcc[acc.instr] {
				continue
			}
			seenAcc[acc.instr] = true
			accesses = append(accesses, acc)
		}
	}
	if len(candidates) == 0 {
		return false
	}
	if len(accesses) == 0 {
		return true
	}
	return hasTiedMutex(candidates, accesses)
}

// mutexGuardsAccesses reports whether every load/store of root's memory in
// funcs happens while one consistent tied sync.Mutex (same or parent struct
// field) is held at every touchpoint.
func mutexGuardsAccesses(root ssa.Value, funcs map[*ssa.Function]bool) bool {
	return mutexGuardsAccessesRec(root, funcs, map[ssa.Value]bool{})
}

func mutexGuardsAccessesRec(root ssa.Value, funcs map[*ssa.Function]bool, visiting map[ssa.Value]bool) bool {
	if root == nil || visiting[root] {
		return true
	}
	candidates := findStructuralGuards(root)
	accesses := collectDataAccessesDeep(root, funcs, visiting)
	if len(candidates) == 0 {
		return len(accesses) == 0
	}
	if len(accesses) == 0 {
		return true
	}
	return hasTiedMutex(candidates, accesses)
}

// hasTiedMutex reports whether there exists one structural mutex field that
// protects every access (must be the same tied guard across all touchpoints).
func hasTiedMutex(candidates []structuralGuard, accesses []dataAccess) bool {
	_, ok := findTiedMutex(candidates, accesses)
	return ok
}

// findTiedMutex returns one structural mutex field that protects every access.
func findTiedMutex(candidates []structuralGuard, accesses []dataAccess) (structuralGuard, bool) {
	heldCache := map[*ssa.Function]map[ssa.Instruction]map[guardKey]bool{}
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
}

// collectDataAccesses finds loads and stores through root (excluding
// accesses that are solely the sync.Mutex field used for Lock/Unlock).
// It scans instructions directly so Global roots (which lack Referrers)
// are handled uniformly.
func collectDataAccesses(root ssa.Value, funcs map[*ssa.Function]bool) []dataAccess {
	var out []dataAccess
	seen := map[ssa.Instruction]bool{}
	derived := deriveAddrs(root, funcs)

	add := func(instr ssa.Instruction, addr ssa.Value) {
		if instr == nil || seen[instr] {
			return
		}
		if isMutexFieldAddr(addr) {
			return
		}
		seen[instr] = true
		out = append(out, dataAccess{instr: instr, addr: addr})
	}

	checkCall := func(instr ssa.Instruction, c *ssa.CallCommon) {
		if isMutexLockUnlockCall(c) {
			return
		}
		if cal := c.StaticCallee(); cal != nil {
			if isWhitelistedSyncMethod(cal, recvOfCall(c)) || isRWMutexMethod(cal) {
				return
			}
			if len(cal.Blocks) > 0 && instr.Parent() != nil && cal.Pkg == instr.Parent().Pkg {
				// Same-package callees are checked via their own parameters
				// in calleeParamsGuarded.
				return
			}
		}
		for _, arg := range c.Args {
			if derived[arg] && !isMutexFieldAddr(arg) {
				add(instr, arg)
				return
			}
		}
		if c.IsInvoke() && derived[c.Value] {
			add(instr, c.Value)
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
						add(in, in.Addr)
					}
				case *ssa.UnOp:
					if in.Op == token.MUL && derived[in.X] {
						add(in, in.X)
					}
				case *ssa.MapUpdate:
					if derived[in.Map] {
						add(in, in.Map)
					}
				case *ssa.Lookup:
					if derived[in.X] {
						add(in, in.X)
					}
				case *ssa.Range:
					if derived[in.X] {
						add(in, in.X)
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

func accessProtectedBy(acc dataAccess, held map[guardKey]bool, tied structuralGuard) bool {
	tiedKey := tied.structType.String() + "#" + itoa(tied.field)
	for g := range held {
		st := structOf(g.base.Type())
		if st == nil {
			continue
		}
		if st.String()+"#"+itoa(g.field) != tiedKey {
			continue
		}
		if baseCoversAddr(g.base, acc.addr) {
			return true
		}
	}
	return false
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

// analyzeMustHold returns, for each instruction, the set of guards that are
// definitely held just before that instruction executes.
//
// Lock acquires immediately. TryLock acquires only on CFG edges where its
// boolean result is proven true (e.g. the true branch of `if mu.TryLock()`,
// or the false branch of `if !ok` after `ok := mu.TryLock()`).
func analyzeMustHold(fn *ssa.Function) map[ssa.Instruction]map[guardKey]bool {
	result := map[ssa.Instruction]map[guardKey]bool{}
	if fn == nil || len(fn.Blocks) == 0 {
		return result
	}

	universe := discoverGuardsInFunc(fn)
	tryResults := discoverTryLockResults(fn)

	// blockOut[b] = held set after the last instruction of b.
	blockOut := make([]map[guardKey]bool, len(fn.Blocks))
	blockIn := make([]map[guardKey]bool, len(fn.Blocks))

	// TOP = full universe for must-analysis initialization.
	top := cloneGuardSet(universe)
	for i := range fn.Blocks {
		blockIn[fn.Blocks[i].Index] = cloneGuardSet(top)
		blockOut[fn.Blocks[i].Index] = cloneGuardSet(top)
	}
	// Ensure entry is empty.
	entry := fn.Blocks[0]
	blockIn[entry.Index] = map[guardKey]bool{}

	changed := true
	for changed {
		changed = false
		for _, b := range fn.Blocks {
			// IN[b] = intersection of edge-outs from preds (TryLock may gen
			// on the true/false edge after an If on its result).
			var in map[guardKey]bool
			if len(b.Preds) == 0 {
				in = map[guardKey]bool{}
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
				// Record held-before instruction.
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

// discoverTryLockResults maps SSA values that are the boolean result of a
// structural sync.Mutex.TryLock to the guard that would be held on success.
func discoverTryLockResults(fn *ssa.Function) map[ssa.Value]guardKey {
	out := map[ssa.Value]guardKey{}
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			call, ok := instr.(*ssa.Call)
			if !ok {
				continue
			}
			if mutexMethodName(call.Common()) != "TryLock" {
				continue
			}
			g, ok := lockUnlockGuard(call.Common())
			if !ok {
				continue
			}
			out[call] = g
		}
	}
	return out
}

// edgeHold is the held set flowing from pred into succ, including TryLock
// acquisition on the branch where the TryLock result is proven true.
func edgeHold(pred, succ *ssa.BasicBlock, blockOut []map[guardKey]bool, tryResults map[ssa.Value]guardKey) map[guardKey]bool {
	out := cloneGuardSet(blockOut[pred.Index])
	if len(pred.Instrs) == 0 {
		return out
	}
	iff, ok := pred.Instrs[len(pred.Instrs)-1].(*ssa.If)
	if !ok {
		return out
	}
	g, heldOnTrue, ok := tryLockCond(iff.Cond, tryResults)
	if !ok {
		return out
	}
	isTrueSucc := len(pred.Succs) > 0 && pred.Succs[0] == succ
	isFalseSucc := len(pred.Succs) > 1 && pred.Succs[1] == succ
	if heldOnTrue && isTrueSucc {
		out[g] = true
	}
	if !heldOnTrue && isFalseSucc {
		out[g] = true
	}
	return out
}

// tryLockCond reports whether cond is a TryLock result (possibly negated).
// heldOnTrue means the If-true successor acquires the guard; otherwise the
// If-false successor does (cond is a negation of the TryLock result).
func tryLockCond(cond ssa.Value, tryResults map[ssa.Value]guardKey) (g guardKey, heldOnTrue bool, ok bool) {
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
			return guardKey{}, false, false
		case *ssa.ChangeType:
			cond = v.X
		case *ssa.Convert:
			cond = v.X
		default:
			return guardKey{}, false, false
		}
	}
	return guardKey{}, false, false
}

func discoverGuardsInFunc(fn *ssa.Function) map[guardKey]bool {
	u := map[guardKey]bool{}
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
				u[g] = true
			}
		}
	}
	return u
}

func transferHold(in map[guardKey]bool, instr ssa.Instruction, universe map[guardKey]bool) map[guardKey]bool {
	out := cloneGuardSet(in)
	switch in := instr.(type) {
	case *ssa.Call:
		applyCallHold(out, in.Common(), false, universe)
	case *ssa.Defer:
		// defer mu.Unlock() does not kill: it runs at return.
		// defer mu.Lock() is pathological; ignore as gen too (would be wrong
		// for the deferred region). Only immediate Lock gens.
		applyCallHold(out, in.Common(), true, universe)
	}
	return out
}

func applyCallHold(held map[guardKey]bool, c *ssa.CallCommon, isDefer bool, universe map[guardKey]bool) {
	if c == nil {
		return
	}
	_ = universe
	if g, ok := lockUnlockGuard(c); ok {
		name := mutexMethodName(c)
		switch name {
		case "Lock":
			if !isDefer {
				held[g] = true
			}
		case "TryLock":
			// Acquisition is modeled on the CFG edge where the boolean
			// result is proven true; the call itself does not gen.
		case "Unlock":
			if !isDefer {
				delete(held, g)
			}
		}
		return
	}

	if !c.IsInvoke() {
		if _, ok := c.Value.(*ssa.Builtin); ok {
			return
		}
	}

	callee := c.StaticCallee()
	if callee != nil && len(callee.Blocks) > 0 {
		// Same-program callee with a body: kill only guards it may unlock.
		for g := range held {
			if calleeMayUnlock(callee, g, map[*ssa.Function]bool{}) {
				delete(held, g)
			}
		}
		return
	}
	if callee != nil {
		// Bodyless sync primitives cannot unlock our data guards.
		if isSyncMutexMethod(callee) || isRWMutexMethod(callee) ||
			isWhitelistedSyncMethod(callee, recvOfCall(c)) {
			return
		}
	}
	// Unknown or cross-package callee: kill guards whose base escapes as an
	// argument, and guards rooted at exported globals (reachable anywhere).
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
			if isMutexLockUnlockCall(c) {
				if mutexMethodName(c) != "Unlock" {
					continue
				}
				recv := mutexRecv(c)
				fa, ok := recv.(*ssa.FieldAddr)
				if !ok {
					return true // unlock through an alias we cannot identify
				}
				base := stripToObject(fa.X)
				if base == g.base {
					return true
				}
				if gStruct != nil {
					if st := structOf(fa.X.Type()); st != nil && st.String() == gStruct.String() && fa.Field == g.field {
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

func killEscaping(held map[guardKey]bool, v ssa.Value) {
	obj := stripToObject(v)
	for g := range held {
		if g.base == obj || baseCoversAddr(g.base, v) || stripToObject(v) == g.base {
			delete(held, g)
		}
		// Also: if arg is the mutex FieldAddr itself.
		if fa, ok := v.(*ssa.FieldAddr); ok && fa.Field == g.field && stripToObject(fa.X) == g.base {
			delete(held, g)
		}
	}
}

func lockUnlockGuard(c *ssa.CallCommon) (guardKey, bool) {
	name := mutexMethodName(c)
	switch name {
	case "Lock", "Unlock", "TryLock":
	default:
		return guardKey{}, false
	}
	recv := mutexRecv(c)
	if recv == nil {
		return guardKey{}, false
	}
	fa, ok := recv.(*ssa.FieldAddr)
	if !ok {
		// Could be a *sync.Mutex local, not a struct field — not a structural guard.
		return guardKey{}, false
	}
	if !isNamedSyncType(fa.Type(), "Mutex") {
		return guardKey{}, false
	}
	base := stripToObject(fa.X)
	return guardKey{base: base, field: fa.Field}, true
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

func isMutexFieldAddr(v ssa.Value) bool {
	fa, ok := v.(*ssa.FieldAddr)
	if !ok {
		return false
	}
	return isNamedSyncType(fa.Type(), "Mutex")
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
			v = x.Tuple
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

func cloneGuardSet(s map[guardKey]bool) map[guardKey]bool {
	if s == nil {
		return map[guardKey]bool{}
	}
	out := make(map[guardKey]bool, len(s))
	for k, v := range s {
		if v {
			out[k] = true
		}
	}
	return out
}

func intersectGuards(a, b map[guardKey]bool) map[guardKey]bool {
	out := map[guardKey]bool{}
	for k := range a {
		if b[k] {
			out[k] = true
		}
	}
	return out
}

func guardSetEqual(a, b map[guardKey]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
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
