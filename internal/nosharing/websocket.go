package nosharing

import (
	"go/types"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// websocketPairOK reports the documented one-reader one-writer contract:
// each conn has at most one reader goroutine and at most one writer
// goroutine. Two writers on the same conn fail. Applies to
// gorilla/websocket.Conn and local types named Conn with ReadMessage+WriteMessage.
func websocketPairOK(roots []sharedRoot, spawner *ssa.Function, g ssa.Instruction, goro map[*ssa.Function]bool) bool {
	conns := connRoots(roots)
	if len(conns) < 1 || spawner == nil {
		return false
	}
	type tally struct{ readers, writers int }
	per := map[ssa.Value]*tally{}
	for _, c := range conns {
		per[c] = &tally{}
	}
	used := false
	for _, gi := range gosIn(spawner) {
		for _, c := range conns {
			r, w := connOpsAtGo(gi, c)
			if r {
				per[c].readers++
				used = true
			}
			if w {
				per[c].writers++
				used = true
			}
		}
	}
	if !used {
		return false
	}
	writers := 0
	for _, t := range per {
		if t.writers > 1 || t.readers > 1 {
			return false
		}
		writers += t.writers
	}
	return writers > 0
}

func gosIn(fn *ssa.Function) []*ssa.Go {
	if fn == nil {
		return nil
	}
	var out []*ssa.Go
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if g, ok := instr.(*ssa.Go); ok {
				out = append(out, g)
			}
		}
	}
	return out
}

func connOpsAtGo(g *ssa.Go, conn ssa.Value) (reads, writes bool) {
	if g == nil || g.Common() == nil {
		return false, false
	}
	c := g.Common()
	cal := staticCallee(c)
	if cal == nil {
		return false, false
	}
	param := conn
	for i, arg := range c.Args {
		if sameConnVal(arg, conn) && i < len(cal.Params) {
			param = cal.Params[i]
			// Don't break: a function may take the same conn twice.
			r, w := connOpsIn(cal, param, map[*ssa.Function]bool{})
			reads = reads || r
			writes = writes || w
		}
	}
	if mc, ok := c.Value.(*ssa.MakeClosure); ok {
		for i, bind := range mc.Bindings {
			if sameConnVal(bind, conn) && i < len(cal.FreeVars) {
				r, w := connOpsIn(cal, cal.FreeVars[i], map[*ssa.Function]bool{})
				reads = reads || r
				writes = writes || w
			}
		}
	}
	return reads, writes
}

func sameConnVal(a, b ssa.Value) bool {
	if a == nil || b == nil {
		return false
	}
	if a == b {
		return true
	}
	return stripToObject(a) == stripToObject(b) && stripToObject(a) != nil
}

func connRoots(roots []sharedRoot) []ssa.Value {
	var out []ssa.Value
	seen := map[ssa.Value]bool{}
	for _, r := range roots {
		if r.val == nil || seen[r.val] || !isWSConnType(r.val.Type()) {
			continue
		}
		seen[r.val] = true
		out = append(out, r.val)
	}
	return out
}

func isWSConnType(t types.Type) bool {
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
	if obj == nil || obj.Name() != "Conn" {
		return false
	}
	if pkg := obj.Pkg(); pkg != nil && strings.Contains(pkg.Path(), "websocket") {
		return true
	}
	return namedHasMethods(n, "ReadMessage", "WriteMessage")
}

func namedHasMethods(n *types.Named, names ...string) bool {
	if n == nil {
		return false
	}
	have := map[string]bool{}
	for i := 0; i < n.NumMethods(); i++ {
		have[n.Method(i).Name()] = true
	}
	for _, name := range names {
		if !have[name] {
			return false
		}
	}
	return true
}

func connOpsIn(fn *ssa.Function, conn ssa.Value, visiting map[*ssa.Function]bool) (reads, writes bool) {
	if fn == nil || conn == nil || visiting[fn] {
		return false, false
	}
	visiting[fn] = true
	derived := deriveAddrs(conn, map[*ssa.Function]bool{fn: true})
	derived[conn] = true
	for _, b := range fn.Blocks {
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
			if c == nil || !callTouches(c, derived, conn) {
				continue
			}
			name := connMethodName(c)
			if isReadConnMethod(name) {
				reads = true
			}
			if isWriteConnMethod(name) {
				writes = true
			}
			if cal := c.StaticCallee(); cal != nil && len(cal.Blocks) > 0 && !isReadConnMethod(name) && !isWriteConnMethod(name) {
				next := connParam(cal, c, conn, derived)
				r, w := connOpsIn(cal, next, visiting)
				reads = reads || r
				writes = writes || w
			}
		}
	}
	return reads, writes
}

func callTouches(c *ssa.CallCommon, derived map[ssa.Value]bool, conn ssa.Value) bool {
	if c.IsInvoke() && (derived[c.Value] || c.Value == conn) {
		return true
	}
	for _, arg := range c.Args {
		if derived[arg] || arg == conn {
			return true
		}
	}
	return false
}

func connParam(cal *ssa.Function, c *ssa.CallCommon, conn ssa.Value, derived map[ssa.Value]bool) ssa.Value {
	for i, arg := range c.Args {
		if (derived[arg] || arg == conn) && i < len(cal.Params) {
			return cal.Params[i]
		}
	}
	if len(cal.Params) > 0 {
		return cal.Params[0]
	}
	return conn
}

func connMethodName(c *ssa.CallCommon) string {
	if c.IsInvoke() && c.Method != nil {
		return c.Method.Name()
	}
	if cal := c.StaticCallee(); cal != nil {
		return calleeBaseName(cal)
	}
	return ""
}

func isReadConnMethod(name string) bool {
	return strings.HasPrefix(name, "Read") || name == "NextReader" || name == "Recv"
}

func isWriteConnMethod(name string) bool {
	return strings.HasPrefix(name, "Write") || name == "NextWriter" || name == "Send"
}
