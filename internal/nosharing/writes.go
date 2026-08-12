package nosharing

import (
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ssa"
)

// isWrittenIn reports whether the object identified by root is written inside
// any of the given functions. Writes to separately heap-allocated objects
// reached only by loading pointer/map/slice/chan/interface fields of root do
// not count — those are different objects (evaluate them if they are shared
// roots themselves). If pessimisticCall is true, passing root's own address
// to an unknown/cross-package function counts as a write unless a WritesParams
// Fact says otherwise.
func isWrittenIn(root ssa.Value, funcs map[*ssa.Function]bool, pessimisticCall bool) bool {
	return isWrittenInPass(nil, root, funcs, pessimisticCall)
}

func (a *analyzer) writtenIn(root ssa.Value, funcs map[*ssa.Function]bool, pessimisticCall bool) bool {
	var pass *analysis.Pass
	if a != nil {
		pass = a.pass
	}
	return isWrittenInPass(pass, root, funcs, pessimisticCall)
}

func isWrittenInPass(pass *analysis.Pass, root ssa.Value, funcs map[*ssa.Function]bool, pessimisticCall bool) bool {
	return isWrittenInVisiting(pass, root, funcs, pessimisticCall, map[*ssa.Function]bool{})
}

func isWrittenInVisiting(pass *analysis.Pass, root ssa.Value, funcs map[*ssa.Function]bool, pessimisticCall bool, visiting map[*ssa.Function]bool) bool {
	// Globals do not track Referrers; scan instructions directly.
	if gl, ok := root.(*ssa.Global); ok {
		return globalWrittenIn(pass, gl, funcs, pessimisticCall)
	}
	derived := deriveOwnAddrs(root, funcs)
	for v := range derived {
		if valueWrittenVisiting(pass, v, funcs, pessimisticCall, visiting) {
			return true
		}
	}
	return false
}

func globalWrittenIn(pass *analysis.Pass, gl *ssa.Global, funcs map[*ssa.Function]bool, pessimisticCall bool) bool {
	derived := deriveOwnAddrs(gl, funcs)
	for fn := range funcs {
		if fn == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				switch in := instr.(type) {
				case *ssa.Store:
					if derived[in.Addr] {
						return true
					}
				case *ssa.MapUpdate:
					if derived[in.Map] {
						return true
					}
				case *ssa.Call:
					for _, arg := range in.Common().Args {
						if derived[arg] && writeViaCall(pass, in.Common(), arg, pessimisticCall) {
							return true
						}
					}
					if argOrRecvIs(in.Common(), gl) && writeViaCall(pass, in.Common(), gl, pessimisticCall) {
						return true
					}
					if pessimisticCall && argOrRecvIs(in.Common(), gl) && in.Common().StaticCallee() == nil {
						return true
					}
				case *ssa.Defer:
					if pessimisticCall && argOrRecvIs(in.Common(), gl) {
						return true
					}
				}
			}
		}
	}
	return false
}

// isWrittenAfterGo reports whether root may be written concurrently from
// outside the goroutine: any write in a non-goroutine function other than
// the spawner, or a write in the spawner that is not proven to happen
// before the go statement.
func isWrittenAfterGo(root ssa.Value, spawner *ssa.Function, g *ssa.Go, all []*ssa.Function, goro map[*ssa.Function]bool) bool {
	for _, f := range all {
		if f == nil || goro[f] {
			continue
		}
		if f == spawner {
			continue
		}
		if isWrittenIn(root, map[*ssa.Function]bool{f: true}, true) {
			return true
		}
	}
	// Within the spawner: only writes that may run after / concurrent with go.
	return hasWriteNotBefore(root, spawner, g)
}

func hasWriteNotBefore(root ssa.Value, fn *ssa.Function, g *ssa.Go) bool {
	if fn == nil {
		return false
	}
	goBlock := g.Block()
	goIdx := instrIndex(goBlock, g)

	// Globals: scan instructions (no Referrers).
	if gl, ok := root.(*ssa.Global); ok {
		for _, b := range fn.Blocks {
			for i, instr := range b.Instrs {
				if !isWriteInstr(instr, gl) {
					continue
				}
				if b == goBlock && i < goIdx {
					continue
				}
				if b != goBlock && b.Dominates(goBlock) && !blockInCycle(b) {
					continue
				}
				return true
			}
		}
		return false
	}

	derived := deriveOwnAddrs(root, map[*ssa.Function]bool{fn: true})
	for v := range derived {
		refs := v.Referrers()
		if refs == nil {
			continue
		}
		for _, ref := range *refs {
			if ref.Parent() != fn {
				continue
			}
			if !isWriteInstr(ref, v) {
				continue
			}
			rb := ref.Block()
			ri := instrIndex(rb, ref)
			if rb == goBlock && ri < goIdx {
				continue // clearly before go
			}
			if rb != goBlock && rb.Dominates(goBlock) && !blockInCycle(rb) {
				continue // dominates go and not in a loop
			}
			return true
		}
	}
	return false
}

// deriveAddrs collects addresses/values derived from root within funcs.
// It scans instructions directly (rather than walking Referrers) so that
// Global roots, which do not track referrers, are handled uniformly.
// Growth is capped to avoid pathological alias explosion on large packages.
const maxDeriveAddrs = 4096

func deriveAddrs(root ssa.Value, funcs map[*ssa.Function]bool) map[ssa.Value]bool {
	out := map[ssa.Value]bool{root: true}
	for changed := true; changed; {
		changed = false
		if len(out) >= maxDeriveAddrs {
			break
		}
		for fn := range funcs {
			if fn == nil {
				continue
			}
			for _, b := range fn.Blocks {
				for _, instr := range b.Instrs {
					v, ok := instr.(ssa.Value)
					if !ok || out[v] {
						continue
					}
					if derivesFrom(instr, out) {
						out[v] = true
						changed = true
						if len(out) >= maxDeriveAddrs {
							return out
						}
					}
				}
			}
		}
	}
	return out
}

func derivesFrom(instr ssa.Instruction, set map[ssa.Value]bool) bool {
	switch in := instr.(type) {
	case *ssa.FieldAddr:
		return set[in.X]
	case *ssa.IndexAddr:
		return set[in.X]
	case *ssa.Slice:
		return set[in.X]
	case *ssa.UnOp:
		return set[in.X]
	case *ssa.Field:
		return set[in.X]
	case *ssa.Index:
		return set[in.X]
	case *ssa.ChangeType, *ssa.Convert, *ssa.MakeInterface, *ssa.TypeAssert:
		for _, op := range instr.Operands(nil) {
			if op != nil && *op != nil && set[*op] {
				return true
			}
		}
	case *ssa.Extract:
		return set[in.Tuple]
	}
	return false
}

// deriveOwnAddrs is like deriveAddrs but stops at pointer indirection: loading
// a pointer/map/slice/chan/interface field yields a different heap object and
// is not attributed as part of root. Field addresses themselves stay in-set so
// stores that replace the pointer/header still count as writes to root.
func deriveOwnAddrs(root ssa.Value, funcs map[*ssa.Function]bool) map[ssa.Value]bool {
	out := map[ssa.Value]bool{root: true}
	for changed := true; changed; {
		changed = false
		for fn := range funcs {
			if fn == nil {
				continue
			}
			for _, b := range fn.Blocks {
				for _, instr := range b.Instrs {
					v, ok := instr.(ssa.Value)
					if !ok || out[v] {
						continue
					}
					if derivesFromOwn(instr, out) {
						out[v] = true
						changed = true
					}
				}
			}
		}
	}
	return out
}

func derivesFromOwn(instr ssa.Instruction, set map[ssa.Value]bool) bool {
	switch in := instr.(type) {
	case *ssa.FieldAddr:
		return set[in.X]
	case *ssa.IndexAddr:
		return set[in.X]
	case *ssa.Field:
		return set[in.X]
	case *ssa.Index:
		return set[in.X]
	case *ssa.UnOp:
		if in.Op != token.MUL {
			return set[in.X]
		}
		// Load of an in-object address of an indirect field → foreign object.
		if fieldAddrOfIndirect(in.X) {
			return false
		}
		return set[in.X]
	case *ssa.Slice:
		// Slicing a slice/string header in-object stays related; the backing
		// array is separate but header mutation is via the slice value.
		return set[in.X]
	case *ssa.ChangeType, *ssa.Convert, *ssa.MakeInterface, *ssa.TypeAssert:
		for _, op := range instr.Operands(nil) {
			if op != nil && *op != nil && set[*op] {
				return true
			}
		}
	case *ssa.Extract:
		return set[in.Tuple]
	}
	return false
}

func fieldAddrOfIndirect(v ssa.Value) bool {
	fa, ok := v.(*ssa.FieldAddr)
	if !ok || fa.X == nil {
		return false
	}
	st, ok := underlyingStruct(fa.X.Type())
	if !ok || fa.Field < 0 || fa.Field >= st.NumFields() {
		return false
	}
	return typeIsIndirect(st.Field(fa.Field).Type())
}

func underlyingStruct(t types.Type) (*types.Struct, bool) {
	if t == nil {
		return nil, false
	}
	if p, ok := t.Underlying().(*types.Pointer); ok {
		t = p.Elem()
	}
	st, ok := t.Underlying().(*types.Struct)
	return st, ok
}

func typeIsIndirect(t types.Type) bool {
	if t == nil {
		return false
	}
	switch t.Underlying().(type) {
	case *types.Pointer, *types.Map, *types.Slice, *types.Chan, *types.Interface:
		return true
	}
	return false
}

func valueWritten(v ssa.Value, funcs map[*ssa.Function]bool, pessimisticCall bool) bool {
	return valueWrittenVisiting(nil, v, funcs, pessimisticCall, map[*ssa.Function]bool{})
}

func valueWrittenVisiting(pass *analysis.Pass, v ssa.Value, funcs map[*ssa.Function]bool, pessimisticCall bool, visiting map[*ssa.Function]bool) bool {
	refs := v.Referrers()
	if refs == nil {
		return false
	}
	for _, ref := range *refs {
		fn := ref.Parent()
		if fn != nil && funcs != nil && !funcs[fn] {
			continue
		}
		switch in := ref.(type) {
		case *ssa.Store:
			if in.Addr == v {
				return true
			}
		case *ssa.MapUpdate:
			if in.Map == v {
				return true
			}
		case *ssa.Call:
			if writeViaCallVisiting(pass, in.Common(), v, pessimisticCall, visiting) {
				return true
			}
		case *ssa.Defer:
			if writeViaCallVisiting(pass, in.Common(), v, pessimisticCall, visiting) {
				return true
			}
		case *ssa.Go:
			if writeViaCallVisiting(pass, in.Common(), v, pessimisticCall, visiting) {
				return true
			}
		}
	}
	return false
}

func writeViaCall(pass *analysis.Pass, c *ssa.CallCommon, v ssa.Value, pessimistic bool) bool {
	return writeViaCallVisiting(pass, c, v, pessimistic, map[*ssa.Function]bool{})
}

func writeViaCallVisiting(pass *analysis.Pass, c *ssa.CallCommon, v ssa.Value, pessimistic bool, visiting map[*ssa.Function]bool) bool {
	if isBuiltin(c, "append") {
		return len(c.Args) > 0 && c.Args[0] == v
	}
	if isBuiltin(c, "copy") {
		return len(c.Args) > 0 && c.Args[0] == v
	}
	if isBuiltin(c, "clear") {
		return len(c.Args) > 0 && c.Args[0] == v
	}
	if isBuiltin(c, "delete") {
		return len(c.Args) > 0 && c.Args[0] == v
	}
	// Channel / non-mutating builtins.
	if isBuiltin(c, "close") || isBuiltin(c, "len") || isBuiltin(c, "cap") ||
		isBuiltin(c, "make") || isBuiltin(c, "new") {
		return false
	}

	callee := c.StaticCallee()
	if callee != nil {
		if isShareSafeStdlib(v) {
			return false
		}
		if isAtomicCallee(callee) {
			return false
		}
		if isStdlibReadOnlyCall(callee) {
			return false
		}
		if isWhitelistedSyncMethod(callee, v) {
			return false
		}
		if isSyncMutexMethod(callee) && receiverIs(c, v) {
			return false
		}
		if isRWMutexMethod(callee) && receiverIs(c, v) {
			return true
		}
		// Any callee with a body: look through (including cross-package when
		// SSA bodies are present).
		if len(callee.Blocks) > 0 {
			return calleeWritesParam(pass, c, v, callee, visiting)
		}
		// Bodyless: consult WritesParams Fact from the defining package.
		if known, writes := writesParamsFact(pass, c, v); known {
			return writes
		}
		// Still unknown: pessimistic.
		if pessimistic && argOrRecvIs(c, v) {
			return true
		}
		return false
	}

	if isShareSafeStdlib(v) {
		return false
	}
	if pessimistic && argOrRecvIs(c, v) {
		return true
	}
	return false
}

func calleeWritesParam(pass *analysis.Pass, c *ssa.CallCommon, v ssa.Value, callee *ssa.Function, visiting map[*ssa.Function]bool) bool {
	if callee == nil || visiting[callee] {
		return false
	}
	visiting[callee] = true
	for i, arg := range c.Args {
		if arg != v {
			continue
		}
		if i >= len(callee.Params) {
			continue
		}
		if isWrittenInVisiting(pass, callee.Params[i], map[*ssa.Function]bool{callee: true}, true, visiting) {
			return true
		}
	}
	return false
}

func isBuiltin(c *ssa.CallCommon, name string) bool {
	if c.Value == nil {
		return false
	}
	b, ok := c.Value.(*ssa.Builtin)
	return ok && b.Name() == name
}

func argOrRecvIs(c *ssa.CallCommon, v ssa.Value) bool {
	if c.IsInvoke() && c.Value == v {
		return true
	}
	for _, a := range c.Args {
		if a == v {
			return true
		}
	}
	return false
}

func receiverIs(c *ssa.CallCommon, v ssa.Value) bool {
	if len(c.Args) == 0 {
		return false
	}
	return c.Args[0] == v || (c.IsInvoke() && c.Value == v)
}

func isWhitelistedSync(v ssa.Value) bool {
	return isNamedSyncType(v.Type(), "WaitGroup", "Once", "Map")
}

// isShareSafeStdlib reports values whose stdlib type is designed for safe
// concurrent sharing without a caller-held sync.Mutex (context.Context and
// *net/http.Server's Serve/Shutdown protocol).
func isShareSafeStdlib(v ssa.Value) bool {
	if v == nil {
		return false
	}
	return isContextType(v.Type()) || isHTTPServerType(v.Type())
}

func isContextType(t types.Type) bool {
	t = types.Unalias(t)
	// Closure captures of interfaces are *Context.
	if p, ok := t.(*types.Pointer); ok {
		t = types.Unalias(p.Elem())
	}
	n, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := n.Obj()
	return obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == "context" && obj.Name() == "Context"
}

func isHTTPServerType(t types.Type) bool {
	t = types.Unalias(t)
	// FreeVar of *Server is **Server.
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
	return obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == "net/http" && obj.Name() == "Server"
}

func isMutexLike(v ssa.Value) bool {
	return isNamedSyncType(v.Type(), "Mutex", "RWMutex")
}

func isNamedSyncType(t types.Type, names ...string) bool {
	t = types.Unalias(t)
	if p, ok := t.(*types.Pointer); ok {
		t = types.Unalias(p.Elem())
	}
	n, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := n.Obj()
	if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != "sync" {
		return false
	}
	for _, name := range names {
		if obj.Name() == name {
			return true
		}
	}
	return false
}

func isWhitelistedSyncMethod(fn *ssa.Function, recv ssa.Value) bool {
	if fn == nil || fn.Signature.Recv() == nil {
		return false
	}
	name := fn.Name()
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	recvMatches := func(typeName string) bool {
		return recv != nil && isNamedSyncType(recv.Type(), typeName)
	}
	switch name {
	case "Add", "Done", "Wait":
		return recvMatches("WaitGroup") || recvTypeIs(fn, "WaitGroup")
	case "Do":
		return recvMatches("Once") || recvTypeIs(fn, "Once")
	case "Load", "Store", "LoadOrStore", "LoadAndDelete", "Delete", "Swap", "CompareAndSwap", "Range", "Clear":
		return recvMatches("Map") || recvTypeIs(fn, "Map")
	}
	return false
}

func recvTypeIs(fn *ssa.Function, name string) bool {
	if fn.Signature.Recv() == nil {
		return false
	}
	return isNamedSyncType(fn.Signature.Recv().Type(), name)
}

func isSyncMutexMethod(fn *ssa.Function) bool {
	if fn == nil || fn.Signature.Recv() == nil {
		return false
	}
	switch fn.Name() {
	case "Lock", "Unlock", "TryLock":
		return isNamedSyncType(fn.Signature.Recv().Type(), "Mutex")
	}
	return false
}

func isRWMutexMethod(fn *ssa.Function) bool {
	if fn == nil || fn.Signature.Recv() == nil {
		return false
	}
	switch fn.Name() {
	case "Lock", "Unlock", "TryLock", "RLock", "RUnlock", "TryRLock":
		return isNamedSyncType(fn.Signature.Recv().Type(), "RWMutex")
	}
	return false
}

func isMutexMethod(fn *ssa.Function) bool {
	return isSyncMutexMethod(fn) || isRWMutexMethod(fn)
}
