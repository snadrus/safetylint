package nosharing

import (
	"go/token"
	"go/types"
	"strconv"
	"sync"

	"golang.org/x/tools/go/ssa"
)

// lockerProof caches fail-closed answers to "every store into this Locker
// cell is a *sync.Mutex". Keyed by package path + struct identity + field
// (or cell name for free-standing Alloc/Global).
var lockerProof sync.Map // string → bool

func lockerProofKey(pkg *ssa.Package, id string) string {
	path := ""
	if pkg != nil && pkg.Pkg != nil {
		path = pkg.Pkg.Path()
	}
	return path + "\x00" + id
}

// peelLockerRecv walks Load/convert so invoke t3.Lock() on a loaded
// sync.Locker field reaches the FieldAddr (or free-standing cell).
func peelLockerRecv(v ssa.Value) ssa.Value {
	seen := map[ssa.Value]bool{}
	for v != nil && !seen[v] {
		seen[v] = true
		switch x := v.(type) {
		case *ssa.UnOp:
			if x.Op == token.MUL {
				v = x.X
				continue
			}
			return v
		case *ssa.ChangeInterface:
			v = x.X
		case *ssa.ChangeType:
			v = x.X
		case *ssa.Convert:
			v = x.X
		case *ssa.MakeInterface:
			v = x.X
		default:
			return v
		}
	}
	return v
}

// lockerFieldSatisfiedByMutex reports that every package store into this
// sync.Locker field is a *sync.Mutex. No stores, unknown stores, custom
// Locker implementations, and *sync.RWMutex / RLocker() fail closed.
func lockerFieldSatisfiedByMutex(fa *ssa.FieldAddr) bool {
	if fa == nil || fa.Parent() == nil {
		return false
	}
	st := structOf(fa.X.Type())
	if st == nil || fa.Field < 0 || fa.Field >= st.NumFields() {
		return false
	}
	if !isNamedSyncType(st.Field(fa.Field).Type(), "Locker") {
		return false
	}
	return lockerStructFieldIsMutex(fa.Parent().Pkg, st, fa.Field)
}

func lockerStructFieldIsMutex(pkg *ssa.Package, st *types.Struct, field int) bool {
	if pkg == nil || st == nil || field < 0 || field >= st.NumFields() {
		return false
	}
	if !isNamedSyncType(st.Field(field).Type(), "Locker") {
		return false
	}
	key := lockerProofKey(pkg, st.String()+"#"+strconv.Itoa(field))
	if v, ok := lockerProof.Load(key); ok {
		return v.(bool)
	}
	// Fail closed while computing (breaks store↔param recursion).
	lockerProof.Store(key, false)
	ok := lockerFieldStoresAreMutex(pkg, st, field)
	lockerProof.Store(key, ok)
	return ok
}

func lockerFieldStoresAreMutex(pkg *ssa.Package, st *types.Struct, field int) bool {
	found := false
	ok := true
	walkPkgSrcFuncs(pkg, func(fn *ssa.Function) {
		if fn == nil || !ok {
			return
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				stInstr, isStore := instr.(*ssa.Store)
				if !isStore || stInstr.Addr == nil {
					continue
				}
				if !storeAddrIsLockerField(stInstr.Addr, st, field) {
					continue
				}
				found = true
				if !valueIsStarMutexLocker(stInstr.Val) {
					ok = false
					return
				}
			}
		}
	})
	return found && ok
}

func storeAddrIsLockerField(addr ssa.Value, st *types.Struct, field int) bool {
	addr = peelLockerRecv(addr)
	fa, ok := addr.(*ssa.FieldAddr)
	if !ok || fa.Field != field {
		return false
	}
	ost := structOf(fa.X.Type())
	return ost != nil && ost.String() == st.String()
}

// lockerCellSatisfiedByMutex reports that every store into a free-standing
// Locker Alloc/Global is a *sync.Mutex.
func lockerCellSatisfiedByMutex(cell ssa.Value) bool {
	if cell == nil || !isNamedSyncType(cell.Type(), "Locker") {
		return false
	}
	var pkg *ssa.Package
	var id string
	switch c := cell.(type) {
	case *ssa.Global:
		if c.Pkg == nil {
			return false
		}
		pkg = c.Pkg
		id = "global:" + c.Name()
	case *ssa.Alloc:
		if c.Parent() == nil || c.Parent().Pkg == nil {
			return false
		}
		pkg = c.Parent().Pkg
		id = "alloc:" + c.Parent().String() + ":" + c.Name()
	default:
		return false
	}
	key := lockerProofKey(pkg, id)
	if v, ok := lockerProof.Load(key); ok {
		return v.(bool)
	}
	lockerProof.Store(key, false)
	ok := lockerCellStoresAreMutex(pkg, cell)
	lockerProof.Store(key, ok)
	return ok
}

func lockerCellStoresAreMutex(pkg *ssa.Package, cell ssa.Value) bool {
	found := false
	ok := true
	walkPkgSrcFuncs(pkg, func(fn *ssa.Function) {
		if fn == nil || !ok {
			return
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				stInstr, isStore := instr.(*ssa.Store)
				if !isStore || stInstr.Addr == nil {
					continue
				}
				if peelLockerRecv(stInstr.Addr) != cell && stInstr.Addr != cell {
					continue
				}
				found = true
				if !valueIsStarMutexLocker(stInstr.Val) {
					ok = false
					return
				}
			}
		}
	})
	return found && ok
}

func freeStandingLockerCell(v ssa.Value) ssa.Value {
	if v == nil || !isNamedSyncType(v.Type(), "Locker") {
		return nil
	}
	switch v.(type) {
	case *ssa.Global, *ssa.Alloc:
		return v
	}
	return nil
}

// valueIsStarMutexLocker reports that v, stored into a Locker cell, is a
// *sync.Mutex (MakeInterface of *Mutex, or an unexported Locker parameter
// whose every same-package call site passes *Mutex).
func valueIsStarMutexLocker(v ssa.Value) bool {
	return valueIsStarMutexLockerVisiting(v, map[ssa.Value]bool{})
}

func valueIsStarMutexLockerVisiting(v ssa.Value, visiting map[ssa.Value]bool) bool {
	if v == nil || visiting[v] {
		return false
	}
	visiting[v] = true
	switch x := v.(type) {
	case *ssa.MakeInterface:
		return isStarSyncMutex(x.X.Type())
	case *ssa.ChangeInterface:
		return valueIsStarMutexLockerVisiting(x.X, visiting)
	case *ssa.ChangeType:
		return valueIsStarMutexLockerVisiting(x.X, visiting)
	case *ssa.Convert:
		return valueIsStarMutexLockerVisiting(x.X, visiting)
	case *ssa.Phi:
		if len(x.Edges) == 0 {
			return false
		}
		for _, e := range x.Edges {
			if !valueIsStarMutexLockerVisiting(e, visiting) {
				return false
			}
		}
		return true
	case *ssa.Parameter:
		return lockerParamAlwaysMutex(x)
	case *ssa.UnOp:
		if x.Op == token.MUL {
			if fa, ok := peelLockerRecv(x.X).(*ssa.FieldAddr); ok {
				return lockerFieldSatisfiedByMutex(fa)
			}
			if cell := freeStandingLockerCell(peelLockerRecv(x.X)); cell != nil {
				return lockerCellSatisfiedByMutex(cell)
			}
		}
	}
	return isStarSyncMutex(v.Type())
}

func lockerParamAlwaysMutex(p *ssa.Parameter) bool {
	if p == nil || !isNamedSyncType(p.Type(), "Locker") {
		return false
	}
	fn := p.Parent()
	if fn == nil || fn.Pkg == nil {
		return false
	}
	// Exported functions can be called from other packages with any Locker.
	if obj := funcObject(fn); obj != nil && obj.Exported() && fn.Parent() == nil {
		return false
	}
	idx := -1
	for i, param := range fn.Params {
		if param == p {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false
	}
	found := false
	ok := true
	walkPkgSrcFuncs(fn.Pkg, func(caller *ssa.Function) {
		if caller == nil || !ok {
			return
		}
		for _, b := range caller.Blocks {
			for _, instr := range b.Instrs {
				var c *ssa.CallCommon
				switch in := instr.(type) {
				case *ssa.Call:
					c = in.Common()
				case *ssa.Go:
					c = in.Common()
				case *ssa.Defer:
					c = in.Common()
				default:
					continue
				}
				if c == nil || c.StaticCallee() != fn {
					continue
				}
				if idx >= len(c.Args) || c.Args[idx] == nil {
					ok = false
					return
				}
				found = true
				if !valueIsStarMutexLocker(c.Args[idx]) {
					ok = false
					return
				}
			}
		}
	})
	return found && ok
}

func isStarSyncMutex(t types.Type) bool {
	if t == nil {
		return false
	}
	t = types.Unalias(t)
	p, ok := t.(*types.Pointer)
	if !ok {
		return false
	}
	n, ok := types.Unalias(p.Elem()).(*types.Named)
	if !ok {
		return false
	}
	obj := n.Obj()
	return obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == "sync" && obj.Name() == "Mutex"
}

// isLockPathAddr reports a FieldAddr that is a Mutex/RWMutex, a Locker
// proven to be *sync.Mutex, or a pointer field whose element is a lock
// wrapper (ctxCond.L). Reads of those addresses are the lock path, not
// data accesses that must themselves be guarded.
func isLockPathAddr(v ssa.Value) bool {
	fa, ok := v.(*ssa.FieldAddr)
	if !ok {
		return false
	}
	if isNamedSyncType(fa.Type(), "Mutex", "RWMutex") {
		return true
	}
	if isNamedSyncType(fa.Type(), "Locker") {
		return lockerFieldSatisfiedByMutex(fa)
	}
	st := structOf(fa.X.Type())
	if st == nil || fa.Field < 0 || fa.Field >= st.NumFields() {
		return false
	}
	inner := structOf(st.Field(fa.Field).Type())
	if inner == nil || fa.Parent() == nil {
		return false
	}
	pkg := fa.Parent().Pkg
	for i := 0; i < inner.NumFields(); i++ {
		if isNamedSyncType(inner.Field(i).Type(), "Locker") && lockerStructFieldIsMutex(pkg, inner, i) {
			return true
		}
	}
	return false
}

// walkPkgSrcFuncs visits package-level functions, methods of package types,
// and nested closures. Used by the Locker-store scan so a missed store
// cannot fail-open the mutex proof.
func walkPkgSrcFuncs(pkg *ssa.Package, visit func(*ssa.Function)) {
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
	if pkg.Prog == nil {
		return
	}
	for _, mem := range pkg.Members {
		t, ok := mem.(*ssa.Type)
		if !ok || t.Type() == nil {
			continue
		}
		for _, recv := range []types.Type{t.Type(), types.NewPointer(t.Type())} {
			mset := types.NewMethodSet(recv)
			for i := 0; i < mset.Len(); i++ {
				obj, _ := mset.At(i).Obj().(*types.Func)
				if obj == nil {
					continue
				}
				if fn := pkg.Prog.FuncValue(obj); fn != nil {
					walk(fn)
				}
			}
		}
	}
}
