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
				mode = inheritHoldFromCallers(fn, g, funcs, heldCache)
				if !modeOKForAccess(mode, acc.write, rw) {
					ok = false
					break
				}
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

// inheritHoldFromCallers reports a must-hold inherited from every same-package
// call site when fn itself never releases that guard (caller-held lock across
// a helper such as slk.unlock()).
func inheritHoldFromCallers(fn *ssa.Function, g guardKey, funcs map[*ssa.Function]bool, heldCache map[*ssa.Function]map[ssa.Instruction]holdSet) holdMode {
	if fn == nil || fnReleasesGuard(fn, g) {
		return 0
	}
	var mode holdMode
	found := false
	for caller := range funcs {
		if caller == nil || caller == fn {
			continue
		}
		held, ok := heldCache[caller]
		if !ok {
			held = analyzeMustHold(caller)
			heldCache[caller] = held
		}
		for _, b := range caller.Blocks {
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
				if c == nil || c.StaticCallee() != fn {
					continue
				}
				m := holdModeFor(held[instr], g)
				if m == 0 {
					return 0
				}
				if !found || m < mode {
					mode = m
				}
				found = true
			}
		}
	}
	if !found {
		return 0
	}
	return mode
}

func fnReleasesGuard(fn *ssa.Function, g guardKey) bool {
	if fn == nil {
		return true
	}
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			call, ok := instr.(*ssa.Call)
			if !ok || call.Common() == nil {
				continue
			}
			gk, ok := lockUnlockGuard(call.Common())
			if !ok {
				continue
			}
			name := mutexMethodName(call.Common())
			if name != "Unlock" && name != "RUnlock" {
				continue
			}
			if sameGuard(gk, g) {
				return true
			}
		}
	}
	return false
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
	if i := fieldIndexFrom(root, acc.addr); i >= 0 {
		return i
	}
	if st, ok := acc.instr.(*ssa.Store); ok {
		if i := fieldIndexFrom(root, st.Addr); i >= 0 {
			return i
		}
	}
	if c, ok := acc.instr.(*ssa.Call); ok && c.Common() != nil {
		for _, arg := range c.Common().Args {
			if i := fieldIndexFrom(root, arg); i >= 0 {
				return i
			}
		}
	}
	return -1
}

func fieldIndexFrom(root, cur ssa.Value) int {
	rootObj := stripToObject(root)
	rootSt := namedStructOf(root.Type())
	for cur != nil {
		switch x := cur.(type) {
		case *ssa.FieldAddr:
			if stripToObject(x.X) == rootObj || x.X == root {
				return x.Field
			}
			// Same named/struct type as root (method receiver vs Alloc).
			if rootSt != nil && structOf(x.X.Type()) != nil && structOf(x.X.Type()).String() == rootSt.String() {
				return x.Field
			}
			cur = x.X
		case *ssa.UnOp:
			if x.Op != token.MUL {
				return -1
			}
			cur = x.X
		case *ssa.IndexAddr:
			cur = x.X
		case *ssa.Slice:
			cur = x.X
		case *ssa.ChangeType:
			cur = x.X
		case *ssa.Convert:
			cur = x.X
		default:
			return -1
		}
	}
	return -1
}
