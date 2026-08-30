package nosharing

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// dropNestedShareSafeAccesses removes touches of a nested value field that
// is independently synchronized: Promise shape (Set/Val + mu + done) or a
// nested struct whose own tied mutex covers the access. Replacing the
// nested field itself (t.TF = ...) is still a parent write.
func dropNestedShareSafeAccesses(accesses []dataAccess) []dataAccess {
	var out []dataAccess
	for _, acc := range accesses {
		if isNestedShareSafeAccess(acc) {
			continue
		}
		out = append(out, acc)
	}
	return out
}

func isNestedShareSafeAccess(acc dataAccess) bool {
	nest := nestedValueFieldAddr(acc.addr)
	if nest == nil {
		return false
	}
	if isWholeNestedFieldStore(acc, nest) {
		return false
	}
	t := pointeeType(nest.Type())
	if t == nil {
		t = nest.Type()
	}
	if isPromiseShape(t) {
		return true
	}
	guards := findStructuralGuards(nest)
	if len(guards) == 0 {
		return false
	}
	return hasTiedMutex(guards, []dataAccess{acc})
}

// nestedValueFieldAddr returns the FieldAddr of a nested struct-value field
// (not a pointer field) that acc walks through.
func nestedValueFieldAddr(addr ssa.Value) *ssa.FieldAddr {
	var nest *ssa.FieldAddr
	cur := addr
	for cur != nil {
		switch v := cur.(type) {
		case *ssa.FieldAddr:
			elem := pointeeType(v.Type())
			if elem == nil {
				elem = v.Type()
			}
			if structOf(v.Type()) != nil && !typeIsIndirect(elem) {
				nest = v
			}
			cur = v.X
		case *ssa.UnOp:
			if v.Op == token.MUL {
				cur = v.X
				continue
			}
			return nest
		case *ssa.IndexAddr:
			cur = v.X
		case *ssa.Slice:
			cur = v.X
		default:
			return nest
		}
	}
	return nest
}

func isWholeNestedFieldStore(acc dataAccess, nest *ssa.FieldAddr) bool {
	if nest == nil {
		return false
	}
	st, ok := acc.instr.(*ssa.Store)
	if !ok {
		return false
	}
	return st.Addr == nest || fieldAddrOf(st.Addr) == nest
}

// isPromiseShape reports Set + Val methods and struct fields mu (Mutex) and
// done (chan). Not "any struct with a mu field".
func isPromiseShape(t types.Type) bool {
	if t == nil {
		return false
	}
	t = types.Unalias(t)
	if p, ok := t.(*types.Pointer); ok {
		t = types.Unalias(p.Elem())
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	st := structOf(t)
	if st == nil {
		return false
	}
	hasMu, hasDone := false, false
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		if isNamedSyncType(f.Type(), "Mutex") && (f.Name() == "mu" || f.Name() == "Mu") {
			hasMu = true
		}
		if isChanType(f.Type()) && (f.Name() == "done" || f.Name() == "Done") {
			hasDone = true
		}
	}
	if !hasMu || !hasDone {
		return false
	}
	hasSet, hasVal := false, false
	for i := 0; i < named.NumMethods(); i++ {
		switch named.Method(i).Name() {
		case "Set":
			hasSet = true
		case "Val":
			hasVal = true
		}
	}
	return hasSet && hasVal
}
