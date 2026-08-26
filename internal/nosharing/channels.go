package nosharing

import (
	"go/token"

	"golang.org/x/tools/go/ssa"
)

// checkChannelFreeze enforces freeze-after-send: once a pointer-carrying
// value is sent on a channel, neither the sender nor any receiver may write
// through those pointers. Pointer-free values are unrestricted.
func (a *analyzer) checkChannelFreeze(fn *ssa.Function, reported map[string]bool) {
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			switch in := instr.(type) {
			case *ssa.Send:
				a.checkSendFreeze(fn, in, reported)
			case *ssa.UnOp:
				if in.Op == token.ARROW {
					a.checkRecvFreeze(fn, in, reported)
				}
			}
		}
	}
}

func (a *analyzer) checkSendFreeze(fn *ssa.Function, send *ssa.Send, reported map[string]bool) {
	if !mayContainPointers(send.X.Type()) {
		return
	}
	root := send.X
	check := root
	if alloc := underlyingAlloc(root); alloc != nil {
		check = alloc
	}
	if freezeViolated(check, fn, send, a.funcs) {
		a.reportAt(reported, send.Pos(), "channel send of pointer-carrying value: memory is frozen after send but a write was found")
	}
}

func (a *analyzer) checkRecvFreeze(fn *ssa.Function, recv *ssa.UnOp, reported map[string]bool) {
	if !mayContainPointers(recv.Type()) {
		return
	}
	all := map[*ssa.Function]bool{}
	for _, f := range a.funcs {
		if f != nil {
			all[f] = true
		}
	}
	if isWrittenIn(recv, all, true) {
		a.reportAt(reported, recv.Pos(), "channel receive of pointer-carrying value: memory is frozen after send but receiver writes through it")
	}
}

// freezeViolated reports whether check's memory is written after the send,
// either later in fn or in any other function.
func freezeViolated(check ssa.Value, fn *ssa.Function, send *ssa.Send, funcs []*ssa.Function) bool {
	other := map[*ssa.Function]bool{}
	for _, f := range funcs {
		if f != nil && f != fn {
			other[f] = true
		}
	}
	if isWrittenIn(check, other, true) {
		return true
	}

	derived := deriveAddrs(check, map[*ssa.Function]bool{fn: true})
	sendBlock := send.Block()
	sendIdx := instrIndex(sendBlock, send)

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
			if rb == sendBlock && ri < sendIdx {
				continue
			}
			if rb != sendBlock && rb.Dominates(sendBlock) && !blockInCycle(rb) {
				continue
			}
			return true
		}
	}
	return false
}

func isWriteInstr(ref ssa.Instruction, v ssa.Value) bool {
	switch in := ref.(type) {
	case *ssa.Store:
		return in.Addr == v
	case *ssa.MapUpdate:
		return in.Map == v
	case *ssa.Call:
		return writeViaCall(nil, in.Common(), v, true)
	case *ssa.Defer:
		return writeViaCall(nil, in.Common(), v, true)
	}
	return false
}

func instrIndex(b *ssa.BasicBlock, target ssa.Instruction) int {
	for i, in := range b.Instrs {
		if in == target {
			return i
		}
	}
	return -1
}

// blockPostDominates reports that every path from a hits b (b post-dominates
// a). Cycles that avoid b fail closed. Used for "conditional write then go".
func blockPostDominates(b, a *ssa.BasicBlock) bool {
	if a == nil || b == nil {
		return false
	}
	if a == b {
		return true
	}
	var walk func(cur *ssa.BasicBlock, stack map[*ssa.BasicBlock]bool) bool
	walk = func(cur *ssa.BasicBlock, stack map[*ssa.BasicBlock]bool) bool {
		if cur == b {
			return true
		}
		if stack[cur] {
			return false
		}
		if len(cur.Succs) == 0 {
			return false
		}
		stack[cur] = true
		for _, s := range cur.Succs {
			if !walk(s, stack) {
				return false
			}
		}
		stack[cur] = false
		return true
	}
	return walk(a, map[*ssa.BasicBlock]bool{})
}

func blockInCycle(b *ssa.BasicBlock) bool {
	seen := map[*ssa.BasicBlock]bool{}
	var stack []*ssa.BasicBlock
	stack = append(stack, b.Succs...)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == b {
			return true
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		stack = append(stack, n.Succs...)
	}
	return false
}

func underlyingAlloc(v ssa.Value) *ssa.Alloc {
	switch v := v.(type) {
	case *ssa.Alloc:
		return v
	case *ssa.UnOp:
		return underlyingAlloc(v.X)
	case *ssa.FieldAddr:
		return underlyingAlloc(v.X)
	case *ssa.IndexAddr:
		return underlyingAlloc(v.X)
	case *ssa.Slice:
		return underlyingAlloc(v.X)
	default:
		return nil
	}
}
