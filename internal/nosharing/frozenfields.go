package nosharing

import (
	"go/token"

	"golang.org/x/tools/go/ssa"
)

// filterFrozenReads drops reads of unexported fields that are immutable
// after publish, so they do not poison mutex / partition proofs.
func filterFrozenReads(root ssa.Value, accesses []dataAccess, funcs map[*ssa.Function]bool) []dataAccess {
	if len(accesses) == 0 {
		return accesses
	}
	var out []dataAccess
	for _, acc := range accesses {
		if !acc.write && isFrozenFieldAccess(root, acc, funcs) {
			continue
		}
		out = append(out, acc)
	}
	return out
}

// isFrozenFieldAccess reports a read of an unexported field that is only
// stored during construction of a locally allocated object, before any spawn
// in that function.
func isFrozenFieldAccess(root ssa.Value, acc dataAccess, funcs map[*ssa.Function]bool) bool {
	if acc.write {
		return false
	}
	_, field, ok := fieldOfAccess(acc)
	if !ok {
		return false
	}
	st := structOf(root.Type())
	if st == nil || field < 0 || field >= st.NumFields() {
		return false
	}
	if token.IsExported(st.Field(field).Name()) {
		return false
	}
	return fieldStoresOnlyAtInit(root, field, funcs)
}

func fieldOfAccess(acc dataAccess) (base ssa.Value, field int, ok bool) {
	cur := acc.addr
	if cur == nil {
		if u, isU := acc.instr.(*ssa.UnOp); isU && u.Op == token.MUL {
			cur = u.X
		}
	}
	for cur != nil {
		if fa, isFA := cur.(*ssa.FieldAddr); isFA {
			return fa.X, fa.Field, true
		}
		if u, isU := cur.(*ssa.UnOp); isU && u.Op == token.MUL {
			cur = u.X
			continue
		}
		break
	}
	return nil, -1, false
}

func fieldStoresOnlyAtInit(root ssa.Value, field int, funcs map[*ssa.Function]bool) bool {
	st := structOf(root.Type())
	if st == nil {
		return false
	}
	want := st.String()
	var stores []ssa.Instruction
	seenFn := map[*ssa.Function]bool{}
	visit := func(fn *ssa.Function) {
		if fn == nil || seenFn[fn] {
			return
		}
		seenFn[fn] = true
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				stInstr, ok := instr.(*ssa.Store)
				if !ok {
					continue
				}
				fa, ok := stInstr.Addr.(*ssa.FieldAddr)
				if !ok || fa.Field != field {
					continue
				}
				if structOf(fa.X.Type()) == nil || structOf(fa.X.Type()).String() != want {
					continue
				}
				base := stripToObject(fa.X)
				if _, isAlloc := base.(*ssa.Alloc); !isAlloc {
					// Store on a Parameter/Global/FreeVar is a mutation of an
					// already-published or escaped object.
					stores = append(stores, stInstr)
					continue
				}
				if !storeDominatesPublishes(stInstr) {
					stores = append(stores, stInstr)
					continue
				}
				// Construction store on a local Alloc dominating every
				// publish in that function — not a post-publish write.
			}
		}
	}
	for fn := range funcs {
		visit(fn)
		if fn != nil {
			for _, anon := range fn.AnonFuncs {
				visit(anon)
			}
		}
	}
	if root != nil {
		if fn := parentOfValue(root); fn != nil {
			visit(fn)
			walkPkgFuncs(fn.Pkg, visit)
		}
	}
	// Any remaining store is a post-publish or non-local mutation.
	return len(stores) == 0
}

func storeDominatesPublishes(st ssa.Instruction) bool {
	fn := st.Parent()
	if fn == nil {
		return false
	}
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if !isPublishPoint(instr) {
				continue
			}
			if !dominatesInstr(st, instr) {
				return false
			}
		}
	}
	return true
}

func parentOfValue(v ssa.Value) *ssa.Function {
	if v == nil {
		return nil
	}
	if i, ok := v.(ssa.Instruction); ok {
		return i.Parent()
	}
	switch x := v.(type) {
	case *ssa.Parameter:
		return x.Parent()
	case *ssa.FreeVar:
		return x.Parent()
	}
	return nil
}

func walkPkgFuncs(pkg *ssa.Package, visit func(*ssa.Function)) {
	if pkg == nil {
		return
	}
	seen := map[*ssa.Function]bool{}
	var walk func(*ssa.Function)
	walk = func(f *ssa.Function) {
		if f == nil || seen[f] {
			return
		}
		seen[f] = true
		visit(f)
		for _, anon := range f.AnonFuncs {
			walk(anon)
		}
	}
	for _, mem := range pkg.Members {
		if f, ok := mem.(*ssa.Function); ok {
			walk(f)
		}
	}
}

func isPublishPoint(instr ssa.Instruction) bool {
	switch instr.(type) {
	case *ssa.Go:
		return true
	}
	var c *ssa.CallCommon
	switch in := instr.(type) {
	case *ssa.Call:
		c = in.Common()
	case *ssa.Defer:
		c = in.Common()
	default:
		return false
	}
	if c == nil {
		return false
	}
	if isCuratedSpawn(c.StaticCallee()) {
		return true
	}
	return false
}
