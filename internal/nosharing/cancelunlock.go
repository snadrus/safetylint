package nosharing

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// isCancelUnlocker reports a goroutine body that only waits on a channel
// (typically ctx.Done) and then unlocks a mutex. Such a helper does not
// write the shared object except the unlock, which is not a data write.
func isCancelUnlocker(fn *ssa.Function) bool {
	if fn == nil || len(fn.Blocks) == 0 {
		return false
	}
	sawRecv := false
	sawUnlock := false
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			switch in := instr.(type) {
			case *ssa.UnOp:
				if in.Op == token.ARROW {
					sawRecv = true
				}
			case *ssa.Send:
				return false
			case *ssa.Store:
				if !isMutexFieldAddr(in.Addr) && !isObjectInitStore(dataAccess{instr: in, addr: in.Addr, write: true}) {
					// Allow stores of the received value into a local.
					if isLocalAllocAddr(in.Addr) {
						continue
					}
					if isLockBookkeepingAddr(in.Addr) {
						continue
					}
					return false
				}
			case *ssa.MapUpdate:
				if isLockBookkeepingAddr(in.Map) {
					continue
				}
				return false
			case *ssa.Call:
				if cancelUnlockerCallOK(in.Common(), &sawRecv, &sawUnlock) {
					continue
				}
				return false
			case *ssa.Defer:
				if cancelUnlockerCallOK(in.Common(), &sawRecv, &sawUnlock) {
					continue
				}
				return false
			case *ssa.Go:
				return false
			}
		}
	}
	return sawRecv && sawUnlock
}

func isLocalAllocAddr(addr ssa.Value) bool {
	switch stripToObject(addr).(type) {
	case *ssa.Alloc:
		return true
	}
	return false
}

func cancelUnlockerCallOK(c *ssa.CallCommon, sawRecv, sawUnlock *bool) bool {
	if c == nil {
		return true
	}
	if isMutexGuardCall(c) {
		name := mutexMethodName(c)
		if name == "Unlock" || name == "RUnlock" {
			*sawUnlock = true
			return true
		}
		return false
	}
	if !c.IsInvoke() {
		if _, ok := c.Value.(*ssa.Builtin); ok {
			return true
		}
	}
	// context.Done() / err.Error() / log helpers: no data write.
	if c.IsInvoke() && c.Method != nil {
		switch c.Method.Name() {
		case "Done", "Err", "Error", "String":
			if c.Method.Name() == "Done" {
				*sawRecv = true // typically followed by <-ch; count as wait
			}
			return true
		}
	}
	cal := c.StaticCallee()
	if cal == nil {
		return c.IsInvoke() && !invokeLooksLikeSetter(c)
	}
	if isWhitelistedSyncMethod(cal, recvOfCall(c)) || isStdlibReadOnlyCall(cal) || calleeInGOROOT(cal) && !isCuratedWriter(cal) {
		return true
	}
	name := cal.Name()
	if name == "Unlock" || name == "RUnlock" || name == "unlock" {
		*sawUnlock = true
		return true
	}
	// Builtin delete of the lock map; lock-object unlock helpers.
	if !c.IsInvoke() {
		if b, ok := c.Value.(*ssa.Builtin); ok && b.Name() == "delete" {
			return true
		}
	}
	return false
}

func isLockBookkeepingAddr(addr ssa.Value) bool {
	if addr == nil {
		return false
	}
	if fa := fieldAddrOf(addr); fa != nil {
		st := structOf(fa.X.Type())
		if st != nil && fa.Field >= 0 && fa.Field < st.NumFields() {
			switch st.Field(fa.Field).Name() {
			case "refs", "ref", "Locks", "locks":
				return true
			}
		}
	}
	if typeIsMap(addr.Type()) {
		return true
	}
	return false
}

func typeIsMap(t types.Type) bool {
	if t == nil {
		return false
	}
	t = types.Unalias(t)
	if p, ok := t.(*types.Pointer); ok {
		t = types.Unalias(p.Elem())
	}
	_, ok := t.Underlying().(*types.Map)
	return ok
}

func cancelUnlockerOK(root ssa.Value, fn *ssa.Function) bool {
	if root == nil || !isCancelUnlocker(fn) {
		return false
	}
	// Bookkeeping writes (refs--, map delete, nested unlock) are allowed.
	return !hasNonBookkeepingWrite(root, fn)
}

func hasNonBookkeepingWrite(root ssa.Value, fn *ssa.Function) bool {
	for _, acc := range collectOwnDataAccesses(root, map[*ssa.Function]bool{fn: true}) {
		if !acc.write {
			continue
		}
		if isLockBookkeepingAddr(acc.addr) || isMutexFieldAddr(acc.addr) {
			continue
		}
		if isObjectInitStore(acc) {
			continue
		}
		return true
	}
	return false
}
