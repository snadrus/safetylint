package nosharing

import (
	"go/token"

	"golang.org/x/tools/go/ssa"
)

// consistentMutexGuards accepts when one discovered mutex (any guardKey) is
// held at every access with a mode that covers the access. The mutex need not
// be a field of the accessed object (e.g. parent.lk guarding map entries).
func consistentMutexGuards(accesses []dataAccess, funcs map[*ssa.Function]bool) bool {
	if len(accesses) == 0 {
		return false
	}
	heldCache := map[*ssa.Function]map[ssa.Instruction]holdSet{}
	var cands []guardKey
	seen := map[guardKey]bool{}
	for _, acc := range accesses {
		fn := acc.instr.Parent()
		if fn == nil {
			return false
		}
		held, ok := heldCache[fn]
		if !ok {
			held = analyzeMustHold(fn)
			heldCache[fn] = held
		}
		for g := range held[acc.instr] {
			if !seen[g] {
				seen[g] = true
				cands = append(cands, g)
			}
		}
	}
	for _, g := range cands {
		ok := true
		rw := isNamedSyncType(g.base.Type(), "RWMutex")
		if g.field >= 0 {
			if st := structOf(g.base.Type()); st != nil && g.field < st.NumFields() {
				rw = isNamedSyncType(st.Field(g.field).Type(), "RWMutex")
			}
		}
		for _, acc := range accesses {
			fn := acc.instr.Parent()
			if fn == nil {
				ok = false
				break
			}
			held := heldCache[fn]
			mode := holdModeFor(held[acc.instr], g)
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

// sameGuard reports that two must-hold keys name the same lock. Struct-field
// mutexes match by type+field (FreeVar/Param/Alloc of the same object).
// Free-standing cells match after resolveGuardBase.
func sameGuard(a, b guardKey) bool {
	if a.field != b.field {
		return false
	}
	if a.field >= 0 {
		sa, sb := structOf(a.base.Type()), structOf(b.base.Type())
		return sa != nil && sb != nil && sa.String() == sb.String()
	}
	ra, rb := resolveGuardBase(a.base), resolveGuardBase(b.base)
	return ra != nil && rb != nil && ra == rb
}

func holdModeFor(held holdSet, g guardKey) holdMode {
	if held == nil {
		return 0
	}
	if m, ok := held[g]; ok {
		return m
	}
	for h, m := range held {
		if sameGuard(h, g) {
			return m
		}
	}
	return 0
}

// fieldMutexGuards allows different mutexes for different fields of root,
// requiring each field's accesses to be covered by one consistent mutex.
// Whole-object accesses (no field) must also be covered.
func fieldMutexGuards(root ssa.Value, accesses []dataAccess, funcs map[*ssa.Function]bool) bool {
	if root == nil || len(accesses) == 0 {
		return false
	}
	byField := map[int][]dataAccess{}
	for _, acc := range accesses {
		if !acc.write && isFrozenFieldAccess(root, acc, funcs) {
			continue
		}
		fi := accessFieldIndex(root, acc)
		byField[fi] = append(byField[fi], acc)
	}
	if len(byField) == 0 {
		return true
	}
	for _, group := range byField {
		if !consistentMutexGuards(group, funcs) && !freeStandingMutexGuards(group, funcs, false) && !freeStandingMutexGuards(group, funcs, true) {
			if !hasTiedMutex(findStructuralGuards(root), group) && !hasTiedMutex(findStructuralRWGuards(root), group) {
				return false
			}
		}
	}
	return true
}

func accessFieldIndex(root ssa.Value, acc dataAccess) int {
	cur := acc.addr
	rootObj := stripToObject(root)
	rootSt := structOf(root.Type())
	for cur != nil {
		if fa, ok := cur.(*ssa.FieldAddr); ok {
			if stripToObject(fa.X) == rootObj || fa.X == root {
				return fa.Field
			}
			// Same named/struct type as root (method receiver vs Alloc).
			if rootSt != nil && structOf(fa.X.Type()) != nil && structOf(fa.X.Type()).String() == rootSt.String() {
				return fa.Field
			}
			cur = fa.X
			continue
		}
		if u, ok := cur.(*ssa.UnOp); ok && u.Op == token.MUL {
			cur = u.X
			continue
		}
		break
	}
	return -1
}
