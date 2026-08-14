package nosharing

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// leaseExclusiveOK reports that concurrent writes to root go through an
// exclusive lease: a channel token index into a buffer, or a value popped
// from a mutex-protected pool and pushed back under the same mutex.
func leaseExclusiveOK(root ssa.Value, accesses []dataAccess, funcs map[*ssa.Function]bool) bool {
	return leaseExclusiveOKAt(root, accesses, funcs, nil, nil)
}

func leaseExclusiveOKAt(root ssa.Value, accesses []dataAccess, funcs map[*ssa.Function]bool, spawner *ssa.Function, g *ssa.Go) bool {
	if root == nil || len(accesses) == 0 {
		return false
	}
	if channelTokenLeaseOK(root, accesses, funcs) {
		return true
	}
	if poolHandoffOK(root, accesses, funcs, spawner, g) {
		return true
	}
	return fieldTokenLeaseOK(root, accesses, funcs, spawner)
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
		if !isTokenIndex(idx) {
			return false
		}
		fn := acc.instr.Parent()
		if fn == nil {
			return false
		}
		if !sendsIndexBack(fn, idx) && !tokenReturnedAnywhere(idx, funcs) {
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
	var found *ssa.IndexAddr
	for v != nil {
		switch x := v.(type) {
		case *ssa.IndexAddr:
			// Keep walking toward the object so tbufs[idx][0] yields idx,
			// the leased slot, not the inner element index.
			found = x
			v = x.X
			continue
		case *ssa.Slice:
			v = x.X
		case *ssa.UnOp:
			if x.Op == token.MUL {
				v = x.X
				continue
			}
			if found != nil {
				return found, true
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
			if found != nil {
				return found, true
			}
			return nil, false
		}
	}
	return found, found != nil
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

// isTokenIndex reports a channel-received integer, including a FreeVar
// bound to that receive (parent `idx := <-ch` captured by the worker).
func isTokenIndex(v ssa.Value) bool {
	seen := map[ssa.Value]bool{}
	for i := 0; i < 8 && v != nil && !seen[v]; i++ {
		seen[v] = true
		if isChanRecv(v) || isExtractOfChanRecv(v) {
			return true
		}
		if stored := storedChanRecv(v); stored != nil {
			v = stored
			continue
		}
		if u, ok := v.(*ssa.UnOp); ok && u.Op == token.MUL {
			v = u.X
			continue
		}
		next := resolveCapture(v)
		if next == nil || next == v {
			break
		}
		v = next
	}
	return false
}

// storedChanRecv reports the channel receive stored into a local int cell
// (`idx := <-ch` lowered as `t = new int; *t = <-ch`).
func storedChanRecv(v ssa.Value) ssa.Value {
	if v == nil {
		return nil
	}
	if u, ok := v.(*ssa.UnOp); ok && u.Op == token.MUL {
		v = u.X
	}
	cell := stripToObject(v)
	if fv, ok := cell.(*ssa.FreeVar); ok {
		cell = resolveCapture(fv)
	}
	al, ok := cell.(*ssa.Alloc)
	if !ok {
		return nil
	}
	refs := al.Referrers()
	if refs == nil {
		return nil
	}
	for _, ref := range *refs {
		st, ok := ref.(*ssa.Store)
		if !ok || stripToObject(st.Addr) != al {
			continue
		}
		if isChanRecv(st.Val) || isExtractOfChanRecv(st.Val) {
			return st.Val
		}
	}
	return nil
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
				if tokenValueMatches(s.X, idx) {
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
			if !tokenValueMatches(s.X, idx) {
				continue
			}
			if dominatesInstr(s, write) {
				return true
			}
		}
	}
	return false
}

func tokenReturnedAnywhere(idx ssa.Value, funcs map[*ssa.Function]bool) bool {
	for fn := range funcs {
		if sendsIndexBack(fn, idx) {
			return true
		}
	}
	return false
}

func tokenValueMatches(v, idx ssa.Value) bool {
	if v == nil || idx == nil {
		return false
	}
	if v == idx || stripToObject(v) == stripToObject(idx) {
		return true
	}
	a, b := tokenCell(v), tokenCell(idx)
	return a != nil && b != nil && a == b
}

// tokenCell peels loads and closure FreeVars down to the stack cell that
// holds a channel-received index (`new int` / captured *int).
func tokenCell(v ssa.Value) ssa.Value {
	seen := map[ssa.Value]bool{}
	for i := 0; i < 8 && v != nil && !seen[v]; i++ {
		seen[v] = true
		if u, ok := v.(*ssa.UnOp); ok && u.Op == token.MUL {
			v = u.X
			continue
		}
		if fv, ok := v.(*ssa.FreeVar); ok {
			next := resolveCapture(fv)
			if next == nil || next == v {
				return v
			}
			v = next
			continue
		}
		return stripToObject(v)
	}
	return stripToObject(v)
}

// fieldTokenLeaseOK accepts a struct whose fields are either channels,
// token-leased slots, atomics, or written only by the spawning function
// (parent-exclusive state concurrent with a leased-field worker).
func fieldTokenLeaseOK(root ssa.Value, accesses []dataAccess, funcs map[*ssa.Function]bool, spawner *ssa.Function) bool {
	if root == nil || namedStructOf(root.Type()) == nil || len(accesses) == 0 {
		return false
	}
	byField := map[int][]dataAccess{}
	for _, acc := range accesses {
		if acc.addr != nil && isChanType(acc.addr.Type()) {
			continue
		}
		if isSliceHeaderLoad(acc) {
			continue
		}
		fi := accessFieldIndex(root, acc)
		if fi < 0 {
			// Pointer-cell load/store of *T / **T is not a field of T.
			if !acc.write || isObjectInitStore(acc) {
				continue
			}
			return false
		}
		byField[fi] = append(byField[fi], acc)
	}
	if len(byField) == 0 {
		return false
	}
	sawLease := false
	for _, group := range byField {
		if channelTokenLeaseOK(root, tokenSlotAccesses(group, spawner), funcs) {
			sawLease = true
			continue
		}
		if atomicsOnlyAccesses(group) {
			continue
		}
		if spawner == nil {
			return false
		}
		reach := callReachable(spawner)
		for _, acc := range group {
			fn := acc.instr.Parent()
			if fn == nil {
				return false
			}
			if fn != spawner && !reach[fn] {
				return false
			}
		}
	}
	return sawLease
}

// tokenSlotAccesses drops slice-header setup in the spawner so a one-time
// `tbufs = make(...)` does not poison slot-index token matching.
func tokenSlotAccesses(group []dataAccess, spawner *ssa.Function) []dataAccess {
	var out []dataAccess
	reach := map[*ssa.Function]bool{}
	if spawner != nil {
		reach = callReachable(spawner)
		reach[spawner] = true
	}
	for _, acc := range group {
		if isSliceHeaderLoad(acc) {
			continue
		}
		if isSliceHeaderStore(acc) {
			fn := acc.instr.Parent()
			if fn != nil && (fn == spawner || reach[fn]) {
				continue
			}
		}
		out = append(out, acc)
	}
	return out
}

func poolHandoffOK(root ssa.Value, accesses []dataAccess, funcs map[*ssa.Function]bool, spawner *ssa.Function, g *ssa.Go) bool {
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
		if isSliceHeaderStore(acc) || isAppendOf(acc, root) || isAppendUsing(acc, root) {
			handoff = append(handoff, acc)
			continue
		}
		// The spawner holds the lease from pop to go: its writes that cannot
		// run after the go within one iteration (ReadFull into the leased
		// buffer, conditional header trims) are exclusive, not concurrent.
		if spawner != nil && g != nil && acc.instr.Parent() == spawner && instrBeforeGo(acc.instr, g) {
			continue
		}
		elemWrites = append(elemWrites, acc)
	}
	if len(handoff) == 0 {
		handoff = appendHandoffs(root, funcs)
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
	if !oneWorkerCluster(writers) {
		return false
	}
	return consistentMutexGuards(handoff, funcs)
}

// oneWorkerCluster reports that every writer is a single function or a
// same-package callee of that function (hashChunk called from one worker).
func oneWorkerCluster(writers map[*ssa.Function]bool) bool {
	if len(writers) == 0 {
		return false
	}
	if len(writers) == 1 {
		return true
	}
	for w := range writers {
		if w == nil {
			continue
		}
		reach := callReachable(w)
		ok := true
		for o := range writers {
			if o != w && !reach[o] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// instrBeforeGo reports that instr cannot execute after g within the same
// loop iteration: same block with a smaller index, or a block that g's
// block cannot reach without traversing a loop back edge. Writes in that
// position happen-before the goroutine sees the value.
func instrBeforeGo(instr ssa.Instruction, g *ssa.Go) bool {
	return instrBeforeInstr(instr, g)
}

func instrBeforeInstr(instr, at ssa.Instruction) bool {
	if instr == nil || at == nil {
		return false
	}
	ib, ab := instr.Block(), at.Block()
	if ib == nil || ab == nil || ib.Parent() != ab.Parent() {
		return false
	}
	if ib == ab {
		return instrIndex(ib, instr) < instrIndex(ab, at)
	}
	// Forward-reachability from at avoiding back edges (succ dominates pred).
	seen := map[*ssa.BasicBlock]bool{ab: true}
	work := []*ssa.BasicBlock{ab}
	for len(work) > 0 {
		b := work[len(work)-1]
		work = work[:len(work)-1]
		for _, s := range b.Succs {
			if s.Dominates(b) {
				continue // back edge: next iteration
			}
			if s == ib {
				return false // instr may run after at in this iteration
			}
			if !seen[s] {
				seen[s] = true
				work = append(work, s)
			}
		}
	}
	return true
}

// callReachable is same-package Call/Defer closure (not Go / MakeClosure).
func callReachable(start *ssa.Function) map[*ssa.Function]bool {
	seen := map[*ssa.Function]bool{}
	var work []*ssa.Function
	add := func(f *ssa.Function) {
		if f == nil || seen[f] {
			return
		}
		if start != nil && start.Pkg != nil && f.Pkg != nil && f.Pkg != start.Pkg {
			return
		}
		seen[f] = true
		work = append(work, f)
	}
	add(start)
	for len(work) > 0 {
		f := work[len(work)-1]
		work = work[:len(work)-1]
		for _, b := range f.Blocks {
			for _, instr := range b.Instrs {
				switch in := instr.(type) {
				case *ssa.Call:
					add(staticCallee(in.Common()))
				case *ssa.Defer:
					add(staticCallee(in.Common()))
				}
			}
		}
	}
	return seen
}

func isAppendUsing(acc dataAccess, root ssa.Value) bool {
	return isAppendOf(acc, root)
}

func appendHandoffs(root ssa.Value, funcs map[*ssa.Function]bool) []dataAccess {
	if root == nil {
		return nil
	}
	derived := deriveAddrs(root, funcs)
	var out []dataAccess
	for fn := range funcs {
		if fn == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				c, ok := instr.(*ssa.Call)
				if !ok || c.Common() == nil || !isBuiltin(c.Common(), "append") {
					continue
				}
				if appendMentionsRoot(c, root, derived) {
					out = append(out, dataAccess{instr: c, addr: root, write: true})
				}
			}
		}
	}
	return out
}

func isAppendOf(acc dataAccess, root ssa.Value) bool {
	c, ok := acc.instr.(*ssa.Call)
	if !ok || c.Common() == nil || !isBuiltin(c.Common(), "append") {
		return false
	}
	return appendMentionsRoot(c, root, nil)
}

func appendMentionsRoot(c *ssa.Call, root ssa.Value, derived map[ssa.Value]bool) bool {
	if c == nil || c.Common() == nil || root == nil {
		return false
	}
	for _, arg := range c.Common().Args {
		if derived[arg] || arg == root || stripToObject(arg) == stripToObject(root) || aliasesRoot(arg, root) {
			return true
		}
		if sl, ok := arg.(*ssa.Slice); ok && varargsPackedFrom(sl, root, derived) {
			return true
		}
	}
	return false
}

// varargsPackedFrom reports SSA `append(pool, x)` lowered as
// `t = new [1]T (varargs); t[0] = x; append(pool, t[:]...)`.
func varargsPackedFrom(sl *ssa.Slice, root ssa.Value, derived map[ssa.Value]bool) bool {
	if sl == nil || sl.X == nil || root == nil {
		return false
	}
	refs := sl.X.Referrers()
	if refs == nil {
		return false
	}
	for _, ref := range *refs {
		ia, ok := ref.(*ssa.IndexAddr)
		if !ok || ia.X != sl.X {
			continue
		}
		iaRefs := ia.Referrers()
		if iaRefs == nil {
			continue
		}
		for _, r2 := range *iaRefs {
			st, ok := r2.(*ssa.Store)
			if !ok || st.Addr != ia {
				continue
			}
			if derived[st.Val] || aliasesRoot(st.Val, root) || stripToObject(st.Val) == stripToObject(root) {
				return true
			}
		}
	}
	return false
}

// namedStructOf peels pointers (*T / **T capture cells) down to a struct.
func namedStructOf(t types.Type) *types.Struct {
	for i := 0; i < 4 && t != nil; i++ {
		if st := structOf(t); st != nil {
			return st
		}
		t = pointeeType(t)
	}
	return nil
}
