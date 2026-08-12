package nosharing

import (
	"go/token"

	"golang.org/x/tools/go/ssa"
)

// onceProtectsGlobal reports that every non-Once data access to gl is
// synchronized by sync.Once.Do on an Once that belongs to gl: either the
// access is inside such a Do callback, or it is dominated by a Do call on
// that Once in the same function (lazyValue get() pattern).
//
// A bare concurrent load of gl outside Do is enough to reject the proof —
// freeze only checks writers, so Once must not exempt writes unless readers
// are covered too.
func (a *analyzer) onceProtectsGlobal(gl *ssa.Global) bool {
	if gl == nil || a.pkg == nil {
		return false
	}
	if cached, ok := a.onceOK[gl]; ok {
		return cached
	}
	ok := a.computeOnceProtects(gl)
	if a.onceOK == nil {
		a.onceOK = map[*ssa.Global]bool{}
	}
	a.onceOK[gl] = ok
	return ok
}

func (a *analyzer) computeOnceProtects(gl *ssa.Global) bool {
	all := map[*ssa.Function]bool{}
	for _, fn := range a.funcs {
		if fn != nil {
			all[fn] = true
		}
	}
	accs := collectDataAccesses(gl, all)
	if len(accs) == 0 {
		return false
	}
	sawData := false
	for _, acc := range accs {
		if isOnceFieldAddrOf(acc.addr, gl) {
			continue
		}
		sawData = true
		if a.accessOnceSynced(gl, acc.instr) {
			continue
		}
		return false
	}
	return sawData
}

func isOnceFieldAddrOf(addr ssa.Value, gl *ssa.Global) bool {
	if addr == nil || gl == nil {
		return false
	}
	fa, ok := addr.(*ssa.FieldAddr)
	if !ok || stripToObject(fa.X) != gl {
		return false
	}
	return isNamedSyncType(fa.Type(), "Once")
}

func (a *analyzer) accessOnceSynced(gl *ssa.Global, instr ssa.Instruction) bool {
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
	return a.dominatedByOnceDo(gl, instr)
}

// dominatedByOnceDo reports a same-function Once.Do on gl that dominates instr.
func (a *analyzer) dominatedByOnceDo(gl *ssa.Global, instr ssa.Instruction) bool {
	fn := instr.Parent()
	if fn == nil {
		return false
	}
	for _, b := range fn.Blocks {
		for _, in := range b.Instrs {
			call, ok := in.(*ssa.Call)
			if !ok || !isOnceDoCall(call.Common()) {
				continue
			}
			if !a.onceRecvIsGlobal(recvOfCall(call.Common()), gl, nil) {
				continue
			}
			if dominatesInstr(call, instr) {
				return true
			}
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
	// Exported methods may be called from other packages with other receivers.
	if obj := parent.Object(); obj != nil && obj.Exported() {
		return false
	}
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
	saw := false
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
					if cal == nil || cal.Name() != parent.Name() || !calleeInPkg(cal, a.pkg) {
						continue
					}
				}
				if idx >= len(c.Args) {
					continue
				}
				saw = true
				if stripToObject(c.Args[idx]) != gl {
					return false
				}
			}
		}
	}
	return saw
}
