package nosharing

import (
	"go/token"

	"golang.org/x/tools/go/ssa"
)

// leaseExclusiveOK reports that concurrent writes to root go through an
// exclusive lease: a channel token index into a buffer, or a value popped
// from a mutex-protected pool and pushed back under the same mutex.
func leaseExclusiveOK(root ssa.Value, accesses []dataAccess, funcs map[*ssa.Function]bool) bool {
	if root == nil || len(accesses) == 0 {
		return false
	}
	if channelTokenLeaseOK(root, accesses, funcs) {
		return true
	}
	return poolHandoffOK(root, accesses, funcs)
}

func channelTokenLeaseOK(root ssa.Value, accesses []dataAccess, funcs map[*ssa.Function]bool) bool {
	// Worker writes IndexAddr of root using an index that is a channel recv
	// of a basic integer, and the same function (or a nested defer) sends
	// that index back. Use after send-back is refused.
	saw := false
	for _, acc := range accesses {
		if isSliceHeaderLoad(acc) {
			continue
		}
		if isChanType(acc.addr.Type()) {
			continue
		}
		if !acc.write {
			// Reads of a leased slot are exclusive too if they use the token.
			if _, ok := indexAddrOf(acc); !ok {
				return false
			}
		}
		ia, ok := indexAddrOf(acc)
		if !ok {
			return false
		}
		idx := ia.Index
		if !isChanRecv(idx) && !isExtractOfChanRecv(idx) {
			return false
		}
		fn := acc.instr.Parent()
		if fn == nil || !sendsIndexBack(fn, idx) {
			return false
		}
		if writeAfterSendBack(fn, acc.instr, idx) {
			return false
		}
		saw = true
	}
	return saw
}

func indexAddrOf(acc dataAccess) (*ssa.IndexAddr, bool) {
	if ia, ok := peelIndexAddr(acc.addr); ok {
		return ia, true
	}
	if st, ok := acc.instr.(*ssa.Store); ok {
		return peelIndexAddr(st.Addr)
	}
	if c, ok := acc.instr.(*ssa.Call); ok {
		com := c.Common()
		if com == nil {
			return nil, false
		}
		if isBuiltin(com, "copy") && len(com.Args) > 0 {
			return peelIndexAddr(com.Args[0])
		}
		for _, arg := range com.Args {
			if ia, ok := peelIndexAddr(arg); ok {
				return ia, true
			}
		}
	}
	return nil, false
}

func peelIndexAddr(v ssa.Value) (*ssa.IndexAddr, bool) {
	for v != nil {
		switch x := v.(type) {
		case *ssa.IndexAddr:
			return x, true
		case *ssa.Slice:
			v = x.X
		case *ssa.UnOp:
			if x.Op == token.MUL {
				v = x.X
				continue
			}
			return nil, false
		case *ssa.FieldAddr:
			v = x.X
		case *ssa.ChangeType, *ssa.Convert:
			if ct, ok := v.(*ssa.ChangeType); ok {
				v = ct.X
				continue
			}
			v = v.(*ssa.Convert).X
		default:
			return nil, false
		}
	}
	return nil, false
}

func isChanRecv(v ssa.Value) bool {
	u, ok := v.(*ssa.UnOp)
	return ok && u.Op == token.ARROW
}

func isExtractOfChanRecv(v ssa.Value) bool {
	ex, ok := v.(*ssa.Extract)
	if !ok {
		return false
	}
	return isChanRecv(ex.Tuple)
}

func sendsIndexBack(fn *ssa.Function, idx ssa.Value) bool {
	if fn == nil {
		return false
	}
	found := false
	var walk func(*ssa.Function)
	walk = func(f *ssa.Function) {
		if f == nil || found {
			return
		}
		for _, b := range f.Blocks {
			for _, instr := range b.Instrs {
				s, ok := instr.(*ssa.Send)
				if !ok {
					continue
				}
				if s.X == idx || stripToObject(s.X) == stripToObject(idx) {
					found = true
					return
				}
			}
		}
		for _, anon := range f.AnonFuncs {
			walk(anon)
		}
	}
	walk(fn)
	return found
}

func writeAfterSendBack(fn *ssa.Function, write ssa.Instruction, idx ssa.Value) bool {
	if fn == nil || write == nil {
		return false
	}
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			s, ok := instr.(*ssa.Send)
			if !ok {
				continue
			}
			if s.X != idx && stripToObject(s.X) != stripToObject(idx) {
				continue
			}
			if dominatesInstr(s, write) {
				return true
			}
		}
	}
	return false
}

func poolHandoffOK(root ssa.Value, accesses []dataAccess, funcs map[*ssa.Function]bool) bool {
	// A value removed from a shared container under a mutex and re-added
	// under the same mutex is exclusively owned in between. Element writes
	// may occur in exactly one immediate worker function; the hand-off
	// (header store or append of the root) must hold one consistent mutex.
	if !isSliceOrArrayRoot(root) && !isSliceOrArrayRoot(stripToObject(root)) {
		return false
	}
	var elemWrites []dataAccess
	var handoff []dataAccess
	for _, acc := range accesses {
		if isSliceHeaderLoad(acc) {
			continue
		}
		if !acc.write {
			continue
		}
		if isSliceHeaderStore(acc) || isAppendOf(acc, root) {
			handoff = append(handoff, acc)
			continue
		}
		elemWrites = append(elemWrites, acc)
	}
	if len(elemWrites) == 0 || len(handoff) == 0 {
		return false
	}
	writers := map[*ssa.Function]bool{}
	for _, acc := range elemWrites {
		fn := acc.instr.Parent()
		if fn == nil {
			return false
		}
		writers[fn] = true
	}
	if len(writers) != 1 {
		return false
	}
	return consistentMutexGuards(handoff, funcs)
}

func isAppendOf(acc dataAccess, root ssa.Value) bool {
	c, ok := acc.instr.(*ssa.Call)
	if !ok || c.Common() == nil || !isBuiltin(c.Common(), "append") {
		return false
	}
	for _, arg := range c.Common().Args {
		if arg == root || stripToObject(arg) == stripToObject(root) {
			return true
		}
	}
	return false
}
