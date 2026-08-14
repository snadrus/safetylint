package nosharing

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ssa"
)

// concurrentSafeAnchors lists (pkg path, type name) pairs whose documented
// API is safe to share across goroutines. Used for GOROOT / module-cache
// types that are not Fact-analyzed.
var concurrentSafeAnchors = map[string]map[string]bool{
	"os": {
		"File": true,
	},
	"net/http": {
		"Client": true,
		"Server": true,
	},
	"database/sql": {
		"DB": true,
	},
	"context": {
		"Context": true,
	},
	"sync": {
		"Map": true,
	},
	"golang.org/x/sync/singleflight": {
		"Group": true,
	},
	"github.com/jackc/pgx/v5/pgxpool": {
		"Pool": true,
	},
	"github.com/hashicorp/golang-lru": {
		"Cache": true,
	},
	"github.com/hashicorp/golang-lru/v2": {
		"Cache": true,
	},
}

func isConcurrentSafeValue(v ssa.Value) bool {
	if v == nil {
		return false
	}
	return isConcurrentSafeType(nil, v.Type())
}

func (a *analyzer) isConcurrentSafeValue(v ssa.Value) bool {
	if v == nil {
		return false
	}
	// Anchors and imported Facts only. Same-package derived ConcurrentSafe
	// does not skip go-site analysis: unexported fields are still writable
	// by sibling functions in the defining package.
	var pass *analysis.Pass
	if a != nil {
		pass = a.pass
	}
	return isConcurrentSafeType(pass, v.Type())
}

func namedTypeName(t types.Type) *types.TypeName {
	if t == nil {
		return nil
	}
	t = types.Unalias(t)
	for {
		p, ok := t.(*types.Pointer)
		if !ok {
			break
		}
		t = types.Unalias(p.Elem())
	}
	n, ok := t.(*types.Named)
	if !ok {
		return nil
	}
	return n.Obj()
}

func isConcurrentSafeType(pass *analysis.Pass, t types.Type) bool {
	if t == nil {
		return false
	}
	t = types.Unalias(t)
	for {
		p, ok := t.(*types.Pointer)
		if !ok {
			break
		}
		t = types.Unalias(p.Elem())
	}
	n, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := n.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	if concurrentSafeAnchors[obj.Pkg().Path()][obj.Name()] {
		return true
	}
	if pass != nil && obj != nil {
		var fact ConcurrentSafe
		if pass.ImportObjectFact(obj, &fact) {
			return true
		}
	}
	return false
}

func (a *analyzer) exportConcurrentSafeFacts() {
	if a.pkg == nil || a.pass == nil || !factsEnabled() {
		return
	}
	if a.localConcurrentSafe == nil {
		a.localConcurrentSafe = map[*types.TypeName]bool{}
	}
	a.deriveConcurrentSafeTypes()
	for tn := range a.localConcurrentSafe {
		if tn != nil && tn.Exported() {
			a.pass.ExportObjectFact(tn, &ConcurrentSafe{})
		}
	}
}

// deriveConcurrentSafeTypes publishes ConcurrentSafe for named struct types
// whose methods prove every mutable field is guarded, frozen, atomic, a
// channel, or itself ConcurrentSafe. Fail closed: exported plain fields,
// unexported escape, or any unguarded access refuse the Fact.
func (a *analyzer) deriveConcurrentSafeTypes() {
	if a.pkg == nil || a.pass == nil {
		return
	}
	byType := map[*types.TypeName][]*ssa.Function{}
	for _, fn := range a.funcs {
		if fn == nil || fn.Signature == nil || fn.Signature.Recv() == nil {
			continue
		}
		tn := namedTypeName(fn.Signature.Recv().Type())
		if tn == nil || tn.Pkg() != a.pass.Pkg {
			continue
		}
		byType[tn] = append(byType[tn], fn)
	}
	changed := true
	for changed {
		changed = false
		for tn, methods := range byType {
			if a.localConcurrentSafe[tn] {
				continue
			}
			if a.typeIsConcurrentSafe(tn, methods) {
				a.localConcurrentSafe[tn] = true
				changed = true
			}
		}
	}
}

func (a *analyzer) typeIsConcurrentSafe(tn *types.TypeName, methods []*ssa.Function) bool {
	if tn == nil || len(methods) == 0 {
		return false
	}
	st := structOf(tn.Type())
	if st == nil {
		return false
	}
	if a.exportedPlainFields(st) {
		return false
	}
	funcs := map[*ssa.Function]bool{}
	for _, m := range methods {
		if m == nil {
			continue
		}
		funcs[m] = true
		for r := range reachableFuncs(m, m.Pkg) {
			funcs[r] = true
		}
	}
	if unexportedFieldEscapes(methods, st) {
		return false
	}
	var accesses []dataAccess
	seen := map[ssa.Instruction]bool{}
	var recv0 ssa.Value
	for _, m := range methods {
		if m == nil || len(m.Params) == 0 {
			continue
		}
		recv := m.Params[0]
		if recv0 == nil {
			recv0 = recv
		}
		visiting := map[ssa.Value]bool{}
		for _, acc := range collectDataAccessesDeep(recv, funcs, visiting) {
			if seen[acc.instr] {
				continue
			}
			seen[acc.instr] = true
			accesses = append(accesses, acc)
		}
		// Heap-escaped receivers (*t = recv) hide field writes from the
		// Parameter; collect the **T cell so ConcurrentSafe is not derived
		// from an empty access list.
		for _, cell := range escapeAliasCells(recv, funcs) {
			for _, acc := range collectDataAccessesDeep(cell, funcs, visiting) {
				if seen[acc.instr] {
					continue
				}
				seen[acc.instr] = true
				accesses = append(accesses, acc)
			}
		}
	}
	accesses = filterFrozenReads(recv0, accesses, funcs)
	if len(accesses) == 0 {
		return true
	}
	if a.allAccessesInherentlySafe(accesses) {
		return true
	}
	if recv0 == nil {
		return false
	}
	return accessesGuardedType(recv0, accesses, funcs)
}

func (a *analyzer) exportedPlainFields(st *types.Struct) bool {
	if st == nil {
		return false
	}
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		if !f.Exported() {
			continue
		}
		if a.fieldInherentlySafe(f.Type()) {
			continue
		}
		return true
	}
	return false
}

func (a *analyzer) fieldInherentlySafe(t types.Type) bool {
	if fieldInherentlySafe(t) {
		return true
	}
	tn := namedTypeName(t)
	return a != nil && tn != nil && a.localConcurrentSafe[tn]
}

func fieldInherentlySafe(t types.Type) bool {
	if t == nil {
		return false
	}
	if isNamedSyncType(t, "Mutex", "RWMutex", "WaitGroup", "Once", "Cond", "Map") {
		return true
	}
	if isChanType(t) || isAtomicValueType(t) {
		return true
	}
	return isConcurrentSafeType(nil, t)
}

func (a *analyzer) allAccessesInherentlySafe(accesses []dataAccess) bool {
	if len(accesses) == 0 {
		return true
	}
	for _, acc := range accesses {
		if isAtomicAccess(acc) {
			continue
		}
		if acc.addr != nil && (isChanType(acc.addr.Type()) || a.fieldInherentlySafe(acc.addr.Type()) || a.fieldInherentlySafe(pointeeType(acc.addr.Type()))) {
			continue
		}
		if !acc.write {
			continue
		}
		return false
	}
	return true
}

func unexportedFieldEscapes(methods []*ssa.Function, st *types.Struct) bool {
	want := ""
	if st != nil {
		want = st.String()
	}
	var escape bool
	var walk func(*ssa.Function)
	walk = func(fn *ssa.Function) {
		if fn == nil || escape {
			return
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				switch in := instr.(type) {
				case *ssa.Return:
					for _, r := range in.Results {
						if fieldAddrUnexported(r, want) {
							escape = true
							return
						}
					}
				case *ssa.Send:
					if fieldAddrUnexported(in.X, want) {
						escape = true
						return
					}
				case *ssa.Store:
					if fieldAddrUnexported(in.Val, want) {
						escape = true
						return
					}
				}
			}
		}
		for _, anon := range fn.AnonFuncs {
			walk(anon)
		}
	}
	for _, m := range methods {
		walk(m)
	}
	return escape
}

func fieldAddrUnexported(v ssa.Value, wantStruct string) bool {
	fa, ok := v.(*ssa.FieldAddr)
	if !ok {
		return false
	}
	st := structOf(fa.X.Type())
	if st == nil || (wantStruct != "" && st.String() != wantStruct) {
		return false
	}
	if fa.Field < 0 || fa.Field >= st.NumFields() {
		return false
	}
	f := st.Field(fa.Field)
	if token.IsExported(f.Name()) {
		return false
	}
	if fieldInherentlySafe(f.Type()) {
		return false
	}
	return true
}
