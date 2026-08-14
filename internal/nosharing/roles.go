package nosharing

import (
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// oneReaderOneWriter is a curated API contract: at most one goroutine may
// call reader-role methods and at most one may call writer-role methods.
// Direct field access on the object (outside those methods) is refused.
type roleContract struct {
	pkgPath  string
	typeName string
	reader   map[string]bool
	writer   map[string]bool
	any      map[string]bool
}

var oneReaderOneWriter = []roleContract{
	{
		pkgPath:  "github.com/gorilla/websocket",
		typeName: "Conn",
		reader: map[string]bool{
			"ReadMessage": true, "NextReader": true, "SetReadDeadline": true,
		},
		writer: map[string]bool{
			"WriteMessage": true, "NextWriter": true, "SetWriteDeadline": true,
		},
		any: map[string]bool{
			"Close": true,
		},
	},
}

func roleContractFor(t types.Type) *roleContract {
	n := namedOf(t)
	if n == nil {
		return nil
	}
	obj := n.Obj()
	if obj == nil || obj.Pkg() == nil {
		return nil
	}
	for i := range oneReaderOneWriter {
		c := &oneReaderOneWriter[i]
		if obj.Pkg().Path() == c.pkgPath && obj.Name() == c.typeName {
			return c
		}
	}
	return nil
}

func namedOf(t types.Type) *types.Named {
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
	return n
}

func derefType(t types.Type) types.Type {
	if t == nil {
		return nil
	}
	t = types.Unalias(t)
	if p, ok := t.(*types.Pointer); ok {
		return types.Unalias(p.Elem())
	}
	return t
}

// rolePartitionOK accepts a curated one-reader/one-writer object when every
// access is inside a listed role method (or Close) and at most one concurrent
// goroutine uses each role on this object.
func rolePartitionOK(root ssa.Value, accesses []dataAccess, funcs map[*ssa.Function]bool) bool {
	c := roleContractFor(root.Type())
	if c == nil || len(accesses) == 0 {
		return false
	}
	var live []dataAccess
	for _, acc := range accesses {
		if roleOfAccess(acc, c) != "" {
			live = append(live, acc)
			continue
		}
		if isCaptureCellAccess(acc) {
			continue
		}
		return false
	}
	if len(live) == 0 {
		return false
	}
	readers, writers := 0, 0
	for fn := range funcs {
		if fn == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				g, ok := instr.(*ssa.Go)
				if !ok {
					continue
				}
				r, w := roleUsedByGo(g, root, c)
				if r {
					readers++
				}
				if w {
					writers++
				}
			}
		}
	}
	// The spawning function may also use one complementary role (typical
	// owner-as-writer + spawned reader, or vice versa).
	for fn := range funcs {
		if fn == nil || fn.Parent() != nil {
			continue
		}
		if goCalleeOfSomeGo(fn, funcs) {
			continue
		}
		r, w := rolesOnValueIn(fn, resolveCapture(root), c)
		if !r && !w {
			r, w = rolesOnValueIn(fn, root, c)
		}
		if r {
			readers++
		}
		if w {
			writers++
		}
	}
	return readers <= 1 && writers <= 1 && (readers+writers) > 0
}

// isCaptureCellAccess reports a load/store of a *T / **T cell that merely
// holds the role-partitioned object, not a field of T itself.
func isCaptureCellAccess(acc dataAccess) bool {
	if _, ok := acc.instr.(*ssa.Call); ok {
		return false
	}
	if acc.addr == nil {
		return false
	}
	if fa, ok := acc.addr.(*ssa.FieldAddr); ok {
		return roleContractFor(fa.X.Type()) == nil
	}
	t := types.Unalias(acc.addr.Type())
	for i := 0; i < 3; i++ {
		p, ok := t.(*types.Pointer)
		if !ok {
			break
		}
		if roleContractFor(p.Elem()) != nil {
			return true
		}
		t = types.Unalias(p.Elem())
	}
	return roleContractFor(acc.addr.Type()) != nil
}

func roleOfAccess(acc dataAccess, c *roleContract) string {
	if r := roleOfFunc(acc.instr.Parent(), c); r != "" {
		return r
	}
	call, ok := acc.instr.(*ssa.Call)
	if !ok || call.Common() == nil {
		return ""
	}
	cal := call.Common().StaticCallee()
	if cal == nil {
		return ""
	}
	if c.reader[cal.Name()] {
		return "reader"
	}
	if c.writer[cal.Name()] {
		return "writer"
	}
	if c.any[cal.Name()] {
		return "any"
	}
	return ""
}

func roleOfFunc(fn *ssa.Function, c *roleContract) string {
	for fn != nil {
		if c.reader[fn.Name()] {
			return "reader"
		}
		if c.writer[fn.Name()] {
			return "writer"
		}
		if c.any[fn.Name()] {
			return "any"
		}
		fn = fn.Parent()
	}
	return ""
}

func roleUsedByGo(g *ssa.Go, root ssa.Value, c *roleContract) (reader, writer bool) {
	if g == nil || g.Common() == nil {
		return false, false
	}
	cal := staticCallee(g.Common())
	if cal == nil {
		if mc, ok := g.Common().Value.(*ssa.MakeClosure); ok {
			cal, _ = mc.Fn.(*ssa.Function)
		}
	}
	if cal == nil {
		return false, false
	}
	// Args that alias root → corresponding params.
	for i, arg := range g.Common().Args {
		if i >= len(cal.Params) {
			continue
		}
		if !aliasesRoot(arg, root) && cal.Params[i] != root && !aliasesRoot(cal.Params[i], root) {
			continue
		}
		r, w := rolesOnValueIn(cal, cal.Params[i], c)
		reader = reader || r
		writer = writer || w
	}
	if mc, ok := g.Common().Value.(*ssa.MakeClosure); ok {
		cf, _ := mc.Fn.(*ssa.Function)
		if cf != nil {
			for i, bind := range mc.Bindings {
				if !aliasesRoot(bind, root) {
					continue
				}
				if i < len(cf.FreeVars) {
					r, w := rolesOnValueIn(cf, cf.FreeVars[i], c)
					reader = reader || r
					writer = writer || w
				}
			}
		}
	}
	return reader, writer
}

func rolesOnValueIn(fn *ssa.Function, v ssa.Value, c *roleContract) (reader, writer bool) {
	if fn == nil || v == nil {
		return false, false
	}
	reach := reachableFuncs(fn, fn.Pkg)
	derived := deriveAddrs(v, reach)
	for f := range reach {
		if f == nil {
			continue
		}
		for _, b := range f.Blocks {
			for _, instr := range b.Instrs {
				call, ok := instr.(*ssa.Call)
				if !ok {
					continue
				}
				com := call.Common()
				if com == nil {
					continue
				}
				uses := derived[com.Value]
				for _, arg := range com.Args {
					if derived[arg] {
						uses = true
					}
				}
				if !uses {
					continue
				}
				name := ""
				if cal := com.StaticCallee(); cal != nil {
					name = cal.Name()
				}
				if c.reader[name] {
					reader = true
				}
				if c.writer[name] {
					writer = true
				}
			}
		}
	}
	return reader, writer
}

func aliasesRoot(v, root ssa.Value) bool {
	if v == nil || root == nil {
		return false
	}
	if v == root {
		return true
	}
	sv, sr := stripToObject(v), stripToObject(root)
	if sv == sr {
		return true
	}
	a, b := sameObjectRoot(v), sameObjectRoot(root)
	if a != nil && b != nil && a == b {
		return true
	}
	cv, cr := resolveCapture(sv), resolveCapture(sr)
	if cv != nil && cr != nil && (cv == cr || cv == sr || cr == sv) {
		return true
	}
	return false
}

func resolveCapture(v ssa.Value) ssa.Value {
	if v == nil {
		return nil
	}
	if fv, ok := v.(*ssa.FreeVar); ok {
		return resolveGuardBase(fv)
	}
	return v
}

func goCalleeOfSomeGo(fn *ssa.Function, funcs map[*ssa.Function]bool) bool {
	for f := range funcs {
		if f == nil {
			continue
		}
		for _, b := range f.Blocks {
			for _, instr := range b.Instrs {
				g, ok := instr.(*ssa.Go)
				if !ok || g.Common() == nil {
					continue
				}
				cal := staticCallee(g.Common())
				if cal == nil {
					if mc, ok := g.Common().Value.(*ssa.MakeClosure); ok {
						cal, _ = mc.Fn.(*ssa.Function)
					}
				}
				if cal == fn || (cal != nil && reachableFuncs(cal, cal.Pkg)[fn]) {
					return true
				}
			}
		}
	}
	return false
}
