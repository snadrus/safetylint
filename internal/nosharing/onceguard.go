package nosharing

import (
	"go/token"

	"golang.org/x/tools/go/ssa"
)

// onceGuardsGlobalWrite reports that instr writes gl only inside a sync.Once.Do
// callback whose Once receiver belongs to gl — the lazyValue pattern.
func (a *analyzer) onceGuardsGlobalWrite(gl *ssa.Global, instr ssa.Instruction) bool {
	if gl == nil || instr == nil {
		return false
	}
	fn := instr.Parent()
	if fn == nil {
		return false
	}
	for cur := fn; cur != nil; cur = cur.Parent() {
		if a.closurePassedToOnceOn(gl, cur) {
			return true
		}
	}
	return false
}

func (a *analyzer) closurePassedToOnceOn(gl *ssa.Global, body *ssa.Function) bool {
	if body == nil || a.pkg == nil {
		return false
	}
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
				if cf != body {
					continue
				}
				refs := mc.Referrers()
				if refs == nil {
					continue
				}
				for _, r := range *refs {
					var c *ssa.CallCommon
					switch in := r.(type) {
					case *ssa.Call:
						c = in.Common()
					case *ssa.Defer:
						c = in.Common()
					default:
						continue
					}
					if !isOnceDoCall(c) {
						continue
					}
					if a.onceRecvIsGlobal(recvOfCall(c), gl, mc) {
						return true
					}
				}
			}
		}
	}
	return false
}

func isOnceDoCall(c *ssa.CallCommon) bool {
	if c == nil {
		return false
	}
	cal := c.StaticCallee()
	if cal == nil || cal.Name() != "Do" {
		return false
	}
	recv := recvOfCall(c)
	if recv != nil && isNamedSyncType(recv.Type(), "Once") {
		return true
	}
	return cal.Signature.Recv() != nil && isNamedSyncType(cal.Signature.Recv().Type(), "Once")
}

func (a *analyzer) onceRecvIsGlobal(recv ssa.Value, gl *ssa.Global, mc *ssa.MakeClosure) bool {
	if recv == nil || gl == nil {
		return false
	}
	base := stripToObject(recv)
	if base == gl {
		return true
	}
	if fa, ok := recv.(*ssa.FieldAddr); ok && stripToObject(fa.X) == gl {
		return true
	}
	// Method receiver Param / FreeVar that aliases the global at call sites.
	switch b := base.(type) {
	case *ssa.Parameter:
		return a.paramReceivesGlobal(b, gl)
	case *ssa.FreeVar:
		if mc == nil || b.Parent() == nil {
			return false
		}
		for i, fv := range b.Parent().FreeVars {
			if fv != b || i >= len(mc.Bindings) {
				continue
			}
			bind := stripToObject(mc.Bindings[i])
			if bind == gl {
				return true
			}
			if p, ok := bind.(*ssa.Parameter); ok {
				return a.paramReceivesGlobal(p, gl)
			}
		}
	}
	// Load of *Global.
	if u, ok := recv.(*ssa.UnOp); ok && u.Op == token.MUL {
		return stripToObject(u.X) == gl
	}
	if fa, ok := recv.(*ssa.FieldAddr); ok {
		return a.onceRecvIsGlobal(fa.X, gl, mc)
	}
	return false
}

func (a *analyzer) paramReceivesGlobal(p *ssa.Parameter, gl *ssa.Global) bool {
	if p == nil || p.Parent() == nil {
		return false
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
		return false
	}
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
				default:
					continue
				}
				cal := c.StaticCallee()
				if cal != parent && (cal == nil || cal.Origin() != parent) {
					// Also match generic instances of parent.
					if cal == nil || cal.Name() != parent.Name() {
						continue
					}
					if !calleeInPkg(cal, a.pkg) {
						continue
					}
				}
				if idx >= len(c.Args) {
					continue
				}
				if stripToObject(c.Args[idx]) == gl {
					return true
				}
			}
		}
	}
	return false
}
