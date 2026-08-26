package nosharing

import (
	"go/token"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// fieldIndexOf reports the outermost struct field of acc, if the access is
// through a FieldAddr of a named/anonymous struct. Whole-object accesses
// (the object pointer itself) return ok=false.
func fieldIndexOf(acc dataAccess) (int, bool) {
	return fieldIndexOfAddr(acc.addr)
}

func fieldIndexOfAddr(addr ssa.Value) (int, bool) {
	cur := addr
	for cur != nil {
		switch v := cur.(type) {
		case *ssa.FieldAddr:
			return outermostFieldIndex(v)
		case *ssa.UnOp:
			if v.Op == token.MUL {
				cur = v.X
				continue
			}
			return 0, false
		case *ssa.IndexAddr:
			cur = v.X
		case *ssa.Slice:
			cur = v.X
		default:
			return 0, false
		}
	}
	return 0, false
}

func outermostFieldIndex(fa *ssa.FieldAddr) (int, bool) {
	for fa != nil {
		switch x := fa.X.(type) {
		case *ssa.FieldAddr:
			fa = x
		case *ssa.UnOp:
			if x.Op == token.MUL {
				if inner, ok := x.X.(*ssa.FieldAddr); ok {
					fa = inner
					continue
				}
			}
			return fa.Field, true
		default:
			return fa.Field, true
		}
	}
	return 0, false
}

func fieldAddrOf(addr ssa.Value) *ssa.FieldAddr {
	cur := addr
	for cur != nil {
		switch v := cur.(type) {
		case *ssa.FieldAddr:
			return v
		case *ssa.UnOp:
			if v.Op == token.MUL {
				cur = v.X
				continue
			}
			return nil
		case *ssa.IndexAddr:
			cur = v.X
		case *ssa.Slice:
			cur = v.X
		default:
			return nil
		}
	}
	return nil
}

func isChanFieldAccess(acc dataAccess) bool {
	fa := fieldAddrOf(acc.addr)
	return fa != nil && isChanType(fa.Type())
}

func isInitFuncName(name string) bool {
	return name == "init" || strings.HasPrefix(name, "init#")
}

// dropInitFuncAccesses removes accesses that live in package init wrappers.
// Global freeze already allows those writes; they must not poison a later
// per-field mutex proof (composite-literal / map setup).
func dropInitFuncAccesses(accesses []dataAccess) []dataAccess {
	var out []dataAccess
	for _, acc := range accesses {
		fn := acc.instr.Parent()
		if fn != nil && isInitFuncName(fn.Name()) {
			continue
		}
		out = append(out, acc)
	}
	return out
}

// dropSetupAccesses removes whole-struct composite stores (object
// construction), which are not field mutation.
func dropSetupAccesses(accesses []dataAccess) []dataAccess {
	var out []dataAccess
	for _, acc := range accesses {
		if isObjectInitStore(acc) {
			continue
		}
		out = append(out, acc)
	}
	return out
}

// dropFrozenFieldReads removes unlocked reads of fields that are never
// written in accesses. Mutex/atomic/channel still protect mutable fields;
// ctor-frozen fields (db, cfg, index) must not poison the guard proof.
// A whole-object write means no field is frozen.
func dropFrozenFieldReads(accesses []dataAccess) []dataAccess {
	written := map[int]bool{}
	wholeWrite := false
	for _, acc := range accesses {
		if !acc.write {
			continue
		}
		if isObjectInitStore(acc) || isShareSafeFieldStore(acc) {
			continue
		}
		if k, ok := fieldIndexOf(acc); ok {
			written[k] = true
			continue
		}
		wholeWrite = true
	}
	if wholeWrite {
		return accesses
	}
	var out []dataAccess
	for _, acc := range accesses {
		if acc.write {
			out = append(out, acc)
			continue
		}
		if isChanFieldAccess(acc) {
			continue // channel publication reads (Promise done)
		}
		k, ok := fieldIndexOf(acc)
		if !ok {
			// Whole-object read of a pointer is identity, not a field load.
			continue
		}
		if written[k] {
			out = append(out, acc)
		}
	}
	return out
}

// dropChanFieldReads removes loads of channel-typed fields. Writes still
// require a guard; unlocked receive / load of a mutex-published done chan
// (promise.Val) must not poison the tied-mutex proof.
func dropChanFieldReads(accesses []dataAccess) []dataAccess {
	var out []dataAccess
	for _, acc := range accesses {
		if !acc.write && isChanFieldAccess(acc) {
			continue
		}
		out = append(out, acc)
	}
	return out
}

func prepareGuardAccesses(accesses []dataAccess) []dataAccess {
	accesses = dropSetupAccesses(accesses)
	accesses = dropFrozenFieldReads(accesses)
	accesses = dropChanFieldReads(accesses)
	return accesses
}

// fieldPartitionedGuards accepts when every field's accesses are covered by
// one consistent mutex (or are atomics-only / read-only). Different fields
// may use different mutexes. Whole-object writes cannot be partitioned.
func fieldPartitionedGuards(root ssa.Value, accesses []dataAccess, funcs map[*ssa.Function]bool) bool {
	if len(accesses) == 0 {
		return true
	}
	groups := map[int][]dataAccess{}
	for _, acc := range accesses {
		if k, ok := fieldIndexOf(acc); ok {
			groups[k] = append(groups[k], acc)
			continue
		}
		if acc.write && !isObjectInitStore(acc) {
			return false
		}
	}
	if len(groups) == 0 {
		return !accessesHaveWrite(accesses) || onlySetupWrites(accesses)
	}
	var cands []structuralGuard
	seen := map[string]bool{}
	add := func(list []structuralGuard) {
		for _, c := range list {
			key := c.structType.String() + "#" + itoa(c.field)
			if seen[key] {
				continue
			}
			seen[key] = true
			cands = append(cands, c)
		}
	}
	add(findStructuralGuards(root))
	add(findStructuralRWGuards(root))
	for _, r := range dataAccessRoots(accesses, root) {
		add(findStructuralGuards(r))
		add(findStructuralRWGuards(r))
	}
	for _, group := range groups {
		if !fieldGroupGuarded(group, cands, funcs) {
			return false
		}
	}
	return true
}

func dataAccessRoots(accesses []dataAccess, root ssa.Value) []ssa.Value {
	out := []ssa.Value{root}
	seen := map[ssa.Value]bool{root: true}
	for _, acc := range accesses {
		o := stripToObject(acc.addr)
		if o == nil || seen[o] {
			continue
		}
		seen[o] = true
		out = append(out, o)
	}
	return out
}

func fieldGroupGuarded(group []dataAccess, cands []structuralGuard, funcs map[*ssa.Function]bool) bool {
	if len(group) == 0 || !accessesHaveWrite(group) {
		return true
	}
	if hasTiedMutex(cands, group) {
		return true
	}
	if freeStandingMutexGuards(group, funcs, false) {
		return true
	}
	if freeStandingMutexGuards(group, funcs, true) {
		return true
	}
	return atomicsOnlyAccesses(group)
}

func looksLikeSetterName(name string) bool {
	if name == "" {
		return false
	}
	for _, p := range []string{"Set", "Put", "Store", "Update", "Insert", "Delete", "Remove", "Clear", "Reset", "Bind", "Register"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func invokeLooksLikeSetter(c *ssa.CallCommon) bool {
	if c == nil || !c.IsInvoke() || c.Method == nil {
		return false
	}
	return looksLikeSetterName(c.Method.Name())
}

// isSrcFirstSliceCopy reports Pad/Unpad-style helpers: first slice argument
// is the source (read-only). Used when the body/Fact is unavailable.
func isSrcFirstSliceCopy(fn *ssa.Function) bool {
	if fn == nil {
		return false
	}
	switch calleeBaseName(fn) {
	case "Pad", "Unpad":
		return true
	}
	return false
}

func firstSliceArgIndex(c *ssa.CallCommon) int {
	if c == nil {
		return -1
	}
	for i, arg := range c.Args {
		if arg != nil && isSliceOrArrayRoot(arg) {
			return i
		}
	}
	return -1
}
