package nosharing

import (
	"go/token"

	"golang.org/x/tools/go/ssa"
)

// goCallees returns the static functions that may run as the body of g.
// A single static callee is the common case. For a dynamic func value, if
// every same-package assignment to that value is a known local function or
// closure, all of those bodies are returned. Otherwise nil (caller reports
// dyn_go).
func (a *analyzer) goCallees(g *ssa.Go) []*ssa.Function {
	common := g.Common()
	if common == nil {
		return nil
	}
	if cal := staticCallee(common); cal != nil {
		return []*ssa.Function{cal}
	}
	if common.Value == nil {
		return nil
	}
	return a.resolveFuncValue(common.Value, map[ssa.Value]bool{})
}

// resolveFuncValue tries to find every same-package function that may flow
// into v. Returns nil if the set is incomplete (escaped / foreign / unknown).
func (a *analyzer) resolveFuncValue(v ssa.Value, visiting map[ssa.Value]bool) []*ssa.Function {
	v = stripFuncValue(v)
	if v == nil || visiting[v] {
		return nil
	}
	visiting[v] = true

	switch x := v.(type) {
	case *ssa.Function:
		if x.Pkg == a.pkg {
			return []*ssa.Function{x}
		}
		return nil
	case *ssa.MakeClosure:
		fn, _ := x.Fn.(*ssa.Function)
		if fn != nil && fn.Pkg == a.pkg {
			return []*ssa.Function{fn}
		}
		return nil
	case *ssa.ChangeType:
		return a.resolveFuncValue(x.X, visiting)
	case *ssa.Convert:
		return a.resolveFuncValue(x.X, visiting)
	case *ssa.Parameter:
		return a.resolveFuncParam(x, visiting)
	case *ssa.FreeVar:
		return a.resolveFuncFreeVar(x, visiting)
	case *ssa.UnOp:
		if x.Op == token.MUL {
			return a.resolveFuncAddr(x.X, visiting)
		}
	}
	return a.resolveFuncAddr(v, visiting)
}

func stripFuncValue(v ssa.Value) ssa.Value {
	for v != nil {
		switch x := v.(type) {
		case *ssa.ChangeType:
			v = x.X
		case *ssa.Convert:
			v = x.X
		default:
			return v
		}
	}
	return v
}

func (a *analyzer) resolveFuncFreeVar(fv *ssa.FreeVar, visiting map[ssa.Value]bool) []*ssa.Function {
	if fv == nil || fv.Parent() == nil {
		return nil
	}
	var out []*ssa.Function
	seen := map[*ssa.Function]bool{}
	for _, fn := range a.funcs {
		if fn == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				mc, ok := instr.(*ssa.MakeClosure)
				if !ok {
					continue
				}
				cf, _ := mc.Fn.(*ssa.Function)
				if cf != fv.Parent() {
					continue
				}
				for i, bind := range mc.Bindings {
					if i >= len(cf.FreeVars) || cf.FreeVars[i] != fv {
						continue
					}
					part := a.resolveFuncValue(bind, visiting)
					if part == nil {
						return nil
					}
					for _, f := range part {
						if !seen[f] {
							seen[f] = true
							out = append(out, f)
						}
					}
				}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (a *analyzer) resolveFuncParam(p *ssa.Parameter, visiting map[ssa.Value]bool) []*ssa.Function {
	if p == nil || p.Parent() == nil || p.Parent().Pkg != a.pkg {
		return nil
	}
	parent := p.Parent()
	idx := -1
	for i, param := range parent.Params {
		if param == p {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	var out []*ssa.Function
	seen := map[*ssa.Function]bool{}
	complete := false
	for _, fn := range a.funcs {
		if fn == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				var c *ssa.CallCommon
				switch in := instr.(type) {
				case *ssa.Call:
					c = in.Common()
				case *ssa.Defer:
					c = in.Common()
				case *ssa.Go:
					c = in.Common()
				default:
					continue
				}
				if c.StaticCallee() != parent {
					continue
				}
				complete = true
				if idx >= len(c.Args) {
					return nil
				}
				part := a.resolveFuncValue(c.Args[idx], visiting)
				if part == nil {
					return nil
				}
				for _, f := range part {
					if !seen[f] {
						seen[f] = true
						out = append(out, f)
					}
				}
			}
		}
	}
	if !complete || len(out) == 0 {
		return nil
	}
	return out
}

// resolveFuncAddr chases stores into a func-typed address (FieldAddr / Alloc).
func (a *analyzer) resolveFuncAddr(addr ssa.Value, visiting map[ssa.Value]bool) []*ssa.Function {
	addr = stripFuncAddr(addr)
	if addr == nil {
		return nil
	}
	var out []*ssa.Function
	seen := map[*ssa.Function]bool{}
	foundStore := false

	for _, fn := range a.funcs {
		if fn == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				st, ok := instr.(*ssa.Store)
				if !ok {
					continue
				}
				if !funcStoreMatches(st.Addr, addr) {
					continue
				}
				foundStore = true
				part := a.resolveFuncValue(st.Val, visiting)
				if part == nil {
					return nil
				}
				for _, f := range part {
					if !seen[f] {
						seen[f] = true
						out = append(out, f)
					}
				}
			}
		}
	}
	if !foundStore || len(out) == 0 {
		return nil
	}
	return out
}

func funcStoreMatches(storeAddr, loadAddr ssa.Value) bool {
	storeAddr = stripFuncAddr(storeAddr)
	loadAddr = stripFuncAddr(loadAddr)
	if storeAddr == nil || loadAddr == nil {
		return false
	}
	if storeAddr == loadAddr {
		return true
	}
	sf, sok := storeAddr.(*ssa.FieldAddr)
	lf, lok := loadAddr.(*ssa.FieldAddr)
	if !sok || !lok || sf.Field != lf.Field {
		return false
	}
	st := structOf(sf.X.Type())
	lt := structOf(lf.X.Type())
	return st != nil && lt != nil && st.String() == lt.String()
}

func stripFuncAddr(v ssa.Value) ssa.Value {
	for v != nil {
		switch x := v.(type) {
		case *ssa.UnOp:
			if x.Op == token.MUL {
				v = x.X
				continue
			}
			return v
		case *ssa.ChangeType:
			v = x.X
		case *ssa.Convert:
			v = x.X
		default:
			return v
		}
	}
	return v
}
