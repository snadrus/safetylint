package nosharing

import (
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ssa"
)

// exportParamWriteFacts publishes WritesParams on functions in this package
// so other packages can decide whether a pointer call mutates its argument.
func (a *analyzer) exportParamWriteFacts() {
	if a.pass == nil || !factsEnabled() {
		return
	}
	for _, fn := range a.funcs {
		if fn == nil || fn.Parent() != nil || len(fn.Blocks) == 0 {
			continue
		}
		obj := funcObject(fn)
		if obj == nil || !obj.Exported() {
			continue
		}
		fact := a.computeWritesParams(fn)
		a.exportObjectFact(obj, fact)
	}
}

func (a *analyzer) computeWritesParams(fn *ssa.Function) *WritesParams {
	fact := &WritesParams{}
	funcs := map[*ssa.Function]bool{fn: true}
	for i, p := range fn.Params {
		if p == nil || !mayContainPointers(p.Type()) {
			continue
		}
		if a.writtenIn(p, funcs, true) {
			if fn.Signature.Recv() != nil && i == 0 {
				fact.Recv = true
			} else {
				idx := i
				if fn.Signature.Recv() != nil {
					idx = i - 1
				}
				fact.Params = append(fact.Params, idx)
			}
		}
	}
	return fact
}

// writesParamsFact reports whether a WritesParams Fact answers whether c
// writes through v. known=false means no Fact (stay pessimistic).
func writesParamsFact(pass *analysis.Pass, c *ssa.CallCommon, v ssa.Value) (known, writes bool) {
	if pass == nil || c == nil || v == nil || !factsEnabled() {
		return false, false
	}
	callee := c.StaticCallee()
	if callee == nil {
		return false, false
	}
	obj := funcObject(callee)
	if obj == nil {
		return false, false
	}
	var fact WritesParams
	if !pass.ImportObjectFact(obj, &fact) {
		return false, false
	}
	for i, arg := range c.Args {
		if arg != v {
			continue
		}
		recv := callee.Signature.Recv() != nil && i == 0
		idx := i
		if recv {
			idx = 0
		} else if callee.Signature.Recv() != nil {
			idx = i - 1
		}
		return true, fact.writesArg(recv, idx)
	}
	if c.IsInvoke() && c.Value == v {
		return true, fact.Recv
	}
	return true, false
}
