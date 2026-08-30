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
		return
	}
	for _, p := range sentPointees(send.X) {
		if freezeViolated(p, fn, send, a.funcs) || isWrittenIn(p, otherFuncs(fn, a.funcs), true) || writeAfterSendInFn(p, fn, send) {
			a.reportAt(reported, send.Pos(), "channel send of pointer-carrying value: memory is frozen after send but a write was found")
			return
		}
	}
}

func writeAfterSendInFn(p ssa.Value, fn *ssa.Function, send *ssa.Send) bool {
	if p == nil || fn == nil || send == nil {
		return false
	}
	derived := deriveAddrs(p, map[*ssa.Function]bool{fn: true})
	derived[p] = true
	if obj := stripToObject(p); obj != nil {
		derived[obj] = true
	}
	sendBlock := send.Block()
	sendIdx := instrIndex(sendBlock, send)
	for _, b := range fn.Blocks {
		for i, instr := range b.Instrs {
			if b == sendBlock && i <= sendIdx {
				continue
			}
			if b != sendBlock && b.Dominates(sendBlock) && !blockInCycle(b) {
				continue
			}
			switch in := instr.(type) {
			case *ssa.Store:
				if derived[in.Addr] || derived[stripToObject(in.Addr)] {
					return true
				}
			case *ssa.MapUpdate:
				if derived[in.Map] || derived[stripToObject(in.Map)] {
					return true
				}
			}
		}
	}
	return false
}

func otherFuncs(fn *ssa.Function, funcs []*ssa.Function) map[*ssa.Function]bool {
	out := map[*ssa.Function]bool{}
	for _, f := range funcs {
		if f != nil && f != fn {
			out[f] = true
		}
	}
	return out
}

func sentPointees(v ssa.Value) []ssa.Value {
	var out []ssa.Value
	seen := map[ssa.Value]bool{}
	add := func(x ssa.Value) {
		if x == nil || seen[x] || !typeIsIndirect(x.Type()) {
			return
		}
		seen[x] = true
		out = append(out, x)
	}
	cur := v
	if u, ok := cur.(*ssa.UnOp); ok && u.Op == token.MUL {
		cur = u.X
	}
	alloc, ok := stripToObject(cur).(*ssa.Alloc)
	if !ok {
		add(v)
		return out
	}
	if refs := alloc.Referrers(); refs != nil {
		for _, ref := range *refs {
			st, ok := ref.(*ssa.Store)
			if ok {
				fa := fieldAddrOf(st.Addr)
				if fa != nil && stripToObject(fa.X) == alloc {
					add(st.Val)
				}
				continue
			}
			fa, ok := ref.(*ssa.FieldAddr)
			if !ok || stripToObject(fa.X) != alloc {
				continue
			}
			if faRefs := fa.Referrers(); faRefs != nil {
				for _, r := range *faRefs {
					st, ok := r.(*ssa.Store)
					if !ok || st.Addr != fa {
						continue
					}
					add(st.Val)
				}
			}
		}
	}
	return out
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
		return
	}
	if alloc := recvStoredAlloc(recv); alloc != nil && recvPointeeWritten(alloc, recv, all) {
		a.reportAt(reported, recv.Pos(), "channel receive of pointer-carrying value: memory is frozen after send but receiver writes through it")
	}
}

func recvPointeeWritten(alloc *ssa.Alloc, recv ssa.Value, funcs map[*ssa.Function]bool) bool {
	if alloc == nil {
		return false
	}
	derived := deriveOwnAddrs(alloc, funcs)
	for v := range derived {
		if v == alloc || v == recv {
			continue
		}
		if valueWritten(v, funcs, true) {
			return true
		}
	}
	if refs := alloc.Referrers(); refs != nil {
		for _, ref := range *refs {
			st, ok := ref.(*ssa.Store)
			if !ok {
				continue
			}
			if st.Val == recv || stripToObject(st.Addr) == alloc && st.Val == recv {
				continue
			}
			if derived[st.Addr] && st.Val != recv {
				return true
			}
		}
	}
	// Write-through of pointer/map/slice fields of the received struct
	// (e.D.N = …, e.Tasks[k] = …). Those objects are not in deriveOwnAddrs.
	for fn := range funcs {
		if fn == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				switch in := instr.(type) {
				case *ssa.Store:
					if storeWritesThroughRecv(in.Addr, alloc, derived) {
						return true
					}
				case *ssa.MapUpdate:
					if storeWritesThroughRecv(in.Map, alloc, derived) {
						return true
					}
				}
			}
		}
	}
	return false
}

func storeWritesThroughRecv(addr ssa.Value, alloc *ssa.Alloc, derived map[ssa.Value]bool) bool {
	if addr == nil || alloc == nil {
		return false
	}
	cur := addr
	for i := 0; i < 8 && cur != nil; i++ {
		if derived[cur] || stripToObject(cur) == alloc {
			return cur != alloc
		}
		switch v := cur.(type) {
		case *ssa.FieldAddr:
			cur = v.X
		case *ssa.IndexAddr:
			cur = v.X
		case *ssa.UnOp:
			if v.Op != token.MUL {
				return false
			}
			cur = v.X
		case *ssa.Slice:
			cur = v.X
		default:
			return false
		}
	}
	return false
}

func recvStoredAlloc(recv ssa.Value) *ssa.Alloc {
	if recv == nil || recv.Referrers() == nil {
		return nil
	}
	for _, ref := range *recv.Referrers() {
		st, ok := ref.(*ssa.Store)
		if !ok {
			continue
		}
		if a, ok := stripToObject(st.Addr).(*ssa.Alloc); ok {
			return a
		}
	}
	return nil
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
	afterSend := func(ref ssa.Instruction) bool {
		if ref == nil || ref.Parent() != fn {
			return false
		}
		rb := ref.Block()
		ri := instrIndex(rb, ref)
		if rb == sendBlock && ri < sendIdx {
			return false
		}
		if rb != sendBlock && rb.Dominates(sendBlock) && !blockInCycle(rb) {
			return false
		}
		return true
	}

	for v := range derived {
		refs := v.Referrers()
		if refs == nil {
			continue
		}
		for _, ref := range *refs {
			if !isWriteInstr(ref, v) {
				continue
			}
			if afterSend(ref) {
				return true
			}
		}
	}
	for _, acc := range collectOwnDataAccesses(check, map[*ssa.Function]bool{fn: true}) {
		if acc.write && afterSend(acc.instr) {
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
