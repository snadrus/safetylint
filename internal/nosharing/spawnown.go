package nosharing

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// spawnShareScope is the function set used at a go site for write and guard
// collection: this goroutine's reachable funcs, the spawner, and other gos
// that share the same Alloc/Global/binding cells. Package-wide allFuncs is
// not used — methods that never touch this spawn's captures must not poison
// the proof.
func (a *analyzer) spawnShareScope(spawner, callee *ssa.Function, g ssa.Instruction, origRoots []sharedRoot, globals map[*ssa.Global]bool) map[*ssa.Function]bool {
	scope := reachableFuncs(callee, callee.Pkg)
	if spawner != nil {
		scope[spawner] = true
	}
	cells := spawnCells(origRoots, spawner, g)
	a.addRetainedMethodValues(scope, cells)
	for _, fn := range a.funcs {
		if fn == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				gi, ok := instr.(*ssa.Go)
				if !ok || gi == g {
					continue
				}
				cals := a.goCallees(gi)
				if len(cals) == 0 {
					continue
				}
				var value ssa.Value
				var args []ssa.Value
				if c := gi.Common(); c != nil {
					value = c.Value
					args = c.Args
				}
				for _, cal := range cals {
					roots := collectRoots(value, args, cal, globals)
					if !spawnCellsOverlap(cells, spawnCells(roots, fn, gi)) {
						continue
					}
					scope[fn] = true
					for f := range reachableFuncs(cal, cal.Pkg) {
						scope[f] = true
					}
				}
			}
		}
	}
	return scope
}

func spawnCells(roots []sharedRoot, spawner *ssa.Function, g ssa.Instruction) map[ssa.Value]bool {
	cells := map[ssa.Value]bool{}
	add := func(v ssa.Value) {
		if v == nil {
			return
		}
		cells[v] = true
		if obj := stripToObject(v); obj != nil {
			cells[obj] = true
		}
	}
	for _, r := range roots {
		add(r.val)
		for _, x := range closureBindingCells(spawner, r.val) {
			add(x)
		}
		for _, x := range goParamBindings(g, r.val) {
			add(x)
		}
	}
	return cells
}

// addRetainedMethodValues treats a method-value of a shared cell passed to a
// call as a sibling share (AddWatcher(t.onHead)). The method body joins the
// spawn scope; it is not itself a new go.
func (a *analyzer) addRetainedMethodValues(scope map[*ssa.Function]bool, cells map[ssa.Value]bool) {
	if a == nil || len(cells) == 0 {
		return
	}
	for _, fn := range a.funcs {
		if fn == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				var args []ssa.Value
				switch in := instr.(type) {
				case *ssa.Call:
					if c := in.Common(); c != nil {
						args = c.Args
					}
				case *ssa.Defer:
					if c := in.Common(); c != nil {
						args = c.Args
					}
				case *ssa.Store:
					args = []ssa.Value{in.Val}
				default:
					continue
				}
				for _, arg := range args {
					mc, ok := arg.(*ssa.MakeClosure)
					if !ok {
						continue
					}
					cal, _ := mc.Fn.(*ssa.Function)
					if cal == nil {
						continue
					}
					if !closureBindsCells(mc, cells) {
						continue
					}
					scope[cal] = true
					for f := range reachableFuncs(cal, cal.Pkg) {
						scope[f] = true
					}
				}
			}
		}
	}
}

func closureBindsCells(mc *ssa.MakeClosure, cells map[ssa.Value]bool) bool {
	if mc == nil || len(cells) == 0 {
		return false
	}
	for _, bind := range mc.Bindings {
		if cells[bind] || cells[stripToObject(bind)] {
			return true
		}
	}
	return false
}

func spawnCellsOverlap(a, b map[ssa.Value]bool) bool {
	for v := range a {
		if b[v] {
			return true
		}
	}
	return false
}

// isChanValueCopy reports a channel-received or channel-sent struct value
// copy. Those are not shared heap roots. Structs with map/slice fields are
// accepted when the alloc is only stored from recv (pointees stay frozen
// via checkChannelFreeze).
func isChanValueCopy(v ssa.Value) bool {
	if v == nil {
		return false
	}
	if recvOfValueCopy(v) {
		return true
	}
	obj := stripToObject(v)
	alloc, ok := obj.(*ssa.Alloc)
	if !ok {
		return false
	}
	if typeIsIndirect(alloc.Type()) && !isPointerToValueStruct(alloc.Type()) {
		return false
	}
	return storedOnlyFromChanRecv(alloc)
}

func isPointerToValueStruct(t types.Type) bool {
	if t == nil {
		return false
	}
	t = types.Unalias(t)
	if p, ok := t.(*types.Pointer); ok {
		t = types.Unalias(p.Elem())
	}
	_, ok := t.Underlying().(*types.Struct)
	return ok && !typeIsIndirect(t)
}

func recvOfValueCopy(v ssa.Value) bool {
	switch x := v.(type) {
	case *ssa.UnOp:
		if x.Op == token.ARROW {
			return !chanElemIndirect(x.X)
		}
		if x.Op == token.MUL {
			return recvOfValueCopy(x.X)
		}
	case *ssa.Extract:
		if u, ok := x.Tuple.(*ssa.UnOp); ok && u.Op == token.ARROW {
			return !chanElemIndirect(u.X)
		}
	}
	return false
}

func chanElemIndirect(ch ssa.Value) bool {
	if ch == nil {
		return true
	}
	t := types.Unalias(ch.Type())
	c, ok := t.Underlying().(*types.Chan)
	if !ok {
		return true
	}
	return typeIsIndirect(c.Elem())
}

func storedOnlyFromChanRecv(alloc *ssa.Alloc) bool {
	refs := alloc.Referrers()
	if refs == nil {
		return false
	}
	saw := false
	for _, ref := range *refs {
		st, ok := ref.(*ssa.Store)
		if !ok || stripToObject(st.Addr) != alloc {
			continue
		}
		if !recvOfValueCopy(st.Val) && !isCompositeOfRecv(st.Val) {
			return false
		}
		saw = true
	}
	return saw
}

func isCompositeOfRecv(v ssa.Value) bool {
	switch x := v.(type) {
	case *ssa.ChangeType:
		return recvOfValueCopy(x.X)
	case *ssa.Convert:
		return recvOfValueCopy(x.X)
	case *ssa.MakeInterface:
		return recvOfValueCopy(x.X)
	}
	return recvOfValueCopy(v)
}

// allocParentOf returns the function that allocated root's cell, if it is a
// same-package Alloc (constructor / New).
func allocParentOf(root ssa.Value, spawner *ssa.Function, g ssa.Instruction) *ssa.Function {
	for _, cell := range []ssa.Value{root, stripToObject(root)} {
		if a, ok := cell.(*ssa.Alloc); ok && a.Parent() != nil {
			return a.Parent()
		}
	}
	for _, x := range closureBindingCells(spawner, root) {
		if a, ok := stripToObject(x).(*ssa.Alloc); ok && a.Parent() != nil {
			return a.Parent()
		}
	}
	for _, x := range goParamBindings(g, root) {
		if a, ok := stripToObject(x).(*ssa.Alloc); ok && a.Parent() != nil {
			return a.Parent()
		}
		if p, ok := x.(*ssa.Parameter); ok && p.Parent() != nil {
			for _, arg := range argsPassedAs(p, map[*ssa.Function]bool{spawner: true}) {
				if a, ok := stripToObject(arg).(*ssa.Alloc); ok && a.Parent() != nil {
					return a.Parent()
				}
			}
		}
	}
	return nil
}

func ctorWriteHappensBefore(acc dataAccess, ctor, spawner *ssa.Function, g ssa.Instruction) bool {
	if ctor == nil || acc.instr == nil {
		return false
	}
	fn := acc.instr.Parent()
	if fn != ctor {
		return false
	}
	if ctor == spawner {
		return dominatesInstr(acc.instr, g)
	}
	if spawner == nil || g == nil {
		return false
	}
	saw := false
	for _, b := range ctor.Blocks {
		for _, instr := range b.Instrs {
			var c *ssa.CallCommon
			switch in := instr.(type) {
			case *ssa.Call:
				c = in.Common()
			case *ssa.Go:
				c = in.Common()
			default:
				continue
			}
			cal := c.StaticCallee()
			if cal == nil {
				continue
			}
			if cal != spawner && !reachableFuncs(cal, cal.Pkg)[spawner] {
				continue
			}
			saw = true
			if !dominatesInstr(acc.instr, instr) {
				return false
			}
		}
	}
	return saw
}

func skipSpawnPreShare(acc dataAccess, spawner *ssa.Function, g ssa.Instruction, preShare map[*ssa.Function]bool, ctor *ssa.Function) bool {
	if skipPreShareAccess(acc, spawner, g, preShare) {
		return true
	}
	return ctorWriteHappensBefore(acc, ctor, spawner, g)
}

// collectOwnDataAccessesDeep is collectDataAccessesDeep using deriveOwnAddrs
// so pointer/interface fields (peering, index, db, Max) are not followed.
func collectOwnDataAccessesDeep(root ssa.Value, funcs map[*ssa.Function]bool, visiting map[ssa.Value]bool) []dataAccess {
	if root == nil || visiting[root] {
		return nil
	}
	visiting[root] = true

	var out []dataAccess
	seen := map[ssa.Instruction]bool{}
	addAll := func(accs []dataAccess) {
		for _, acc := range accs {
			if seen[acc.instr] {
				continue
			}
			seen[acc.instr] = true
			out = append(out, acc)
		}
	}
	addAll(collectOwnDataAccesses(root, funcs))

	derived := deriveOwnAddrs(root, funcs)
	for fn := range funcs {
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
				cal := c.StaticCallee()
				if cal == nil || len(cal.Blocks) == 0 || cal.Pkg != fn.Pkg {
					continue
				}
				if isSyncMutexMethod(cal) || isRWMutexMethod(cal) {
					continue
				}
				for i, arg := range c.Args {
					if !derived[arg] || isMutexFieldAddr(arg) {
						continue
					}
					if i >= len(cal.Params) {
						continue
					}
					calFuncs := reachableFuncs(cal, cal.Pkg)
					addAll(collectOwnDataAccessesDeep(cal.Params[i], calFuncs, visiting))
				}
			}
		}
	}
	return out
}

func collectOwnDataAccesses(root ssa.Value, funcs map[*ssa.Function]bool) []dataAccess {
	var out []dataAccess
	seen := map[ssa.Instruction]bool{}
	derived := deriveOwnAddrs(root, funcs)

	add := func(instr ssa.Instruction, addr ssa.Value, write bool) {
		if instr == nil || seen[instr] {
			return
		}
		if isMutexFieldAddr(addr) {
			return
		}
		seen[instr] = true
		out = append(out, dataAccess{instr: instr, addr: addr, write: write})
	}

	checkCall := func(instr ssa.Instruction, c *ssa.CallCommon) {
		if isMutexGuardCall(c) {
			return
		}
		if !c.IsInvoke() {
			if b, ok := c.Value.(*ssa.Builtin); ok {
				switch b.Name() {
				case "len", "cap":
					for _, arg := range c.Args {
						if derived[arg] {
							add(instr, arg, false)
							return
						}
					}
					return
				case "append", "copy", "clear", "delete":
					for _, arg := range c.Args {
						if derived[arg] {
							add(instr, arg, true)
							return
						}
					}
					return
				}
			}
		}
		if cal := c.StaticCallee(); cal != nil {
			if isDBClientCallee(cal) || (cal.Signature != nil && cal.Signature.Recv() != nil && isHarmonyDBType(cal.Signature.Recv().Type())) {
				for _, arg := range c.Args {
					if derived[arg] && !isMutexFieldAddr(arg) {
						add(instr, arg, false)
						return
					}
				}
				return
			}
			if isSrcFirstSliceCopy(cal) {
				first := firstSliceArgIndex(c)
				for i, arg := range c.Args {
					if derived[arg] && !isMutexFieldAddr(arg) {
						add(instr, arg, i != first && i >= 0)
						return
					}
				}
				return
			}
			if isWhitelistedSyncMethod(cal, recvOfCall(c)) || isRWMutexMethod(cal) || isSyncMutexMethod(cal) {
				return
			}
			if isStdlibReadOnlyCall(cal) {
				for _, arg := range c.Args {
					if derived[arg] && !isMutexFieldAddr(arg) {
						add(instr, arg, false)
						return
					}
				}
				return
			}
			if isAtomicCallee(cal) || isAtomicSyncMethod(cal, recvOfCall(c)) {
				for _, arg := range c.Args {
					if derived[arg] && !isMutexFieldAddr(arg) {
						add(instr, arg, false)
						return
					}
				}
				return
			}
			if calleeInGOROOT(cal) {
				write := isCuratedWriter(cal)
				for _, arg := range c.Args {
					if derived[arg] && !isMutexFieldAddr(arg) {
						add(instr, arg, write)
						return
					}
				}
				return
			}
			if len(cal.Blocks) > 0 && instr.Parent() != nil && calleeInPkg(cal, instr.Parent().Pkg) {
				return
			}
		}
		for _, arg := range c.Args {
			if derived[arg] && !isMutexFieldAddr(arg) {
				if isValueCopyArg(arg) {
					add(instr, arg, false)
					return
				}
				if c.IsInvoke() && !invokeLooksLikeSetter(c) {
					return
				}
				add(instr, arg, true)
				return
			}
		}
		if c.IsInvoke() && derived[c.Value] {
			add(instr, c.Value, false)
		}
	}

	for fn := range funcs {
		if fn == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				switch in := instr.(type) {
				case *ssa.Store:
					if derived[in.Addr] {
						add(in, in.Addr, true)
					}
				case *ssa.UnOp:
					if in.Op == token.MUL && derived[in.X] {
						if isReceiverPointerLoad(in) {
							continue
						}
						add(in, in.X, false)
					}
				case *ssa.MapUpdate:
					if derived[in.Map] {
						add(in, in.Map, true)
					} else if fa := mapFieldAddr(in.Map); fa != nil && derived[fa] {
						add(in, fa, true)
					}
				case *ssa.Lookup:
					if derived[in.X] {
						add(in, in.X, false)
					} else if fa := mapFieldAddr(in.X); fa != nil && derived[fa] {
						add(in, fa, false)
					}
				case *ssa.Range:
					if derived[in.X] {
						add(in, in.X, false)
					} else if fa := mapFieldAddr(in.X); fa != nil && derived[fa] {
						add(in, fa, false)
					}
				case *ssa.Call:
					checkCall(in, in.Common())
				case *ssa.Defer:
					checkCall(in, in.Common())
				case *ssa.Go:
					checkCall(in, in.Common())
				}
			}
		}
	}
	return out
}

func mapFieldAddr(m ssa.Value) *ssa.FieldAddr {
	if m == nil {
		return nil
	}
	if u, ok := m.(*ssa.UnOp); ok && u.Op == token.MUL {
		if fa, ok := u.X.(*ssa.FieldAddr); ok {
			return fa
		}
		return fieldAddrOf(u.X)
	}
	return fieldAddrOf(m)
}

func objectGuardedOwnRootsAfter(roots []sharedRoot, funcs map[*ssa.Function]bool, spawner *ssa.Function, g ssa.Instruction, preShare map[*ssa.Function]bool, goro map[*ssa.Function]bool) bool {
	if len(roots) >= maxRootAliases {
		return false
	}
	var accesses []dataAccess
	seenAcc := map[ssa.Instruction]bool{}
	var dataRoots []ssa.Value
	perRoot := map[ssa.Value][]dataAccess{}
	thisFields := map[int]bool{}
	thisWhole := false
	for _, root := range roots {
		if isChanType(root.val.Type()) || isWhitelistedSync(root.val) || isSyncMutex(root.val) || isSyncRWMutex(root.val) || isShareSafeStdlib(root.val) || isHarmonyDBType(root.val.Type()) {
			continue
		}
		if _, ok := root.val.(*ssa.Global); ok {
			continue
		}
		dataRoots = append(dataRoots, root.val)
		ctor := allocParentOf(root.val, spawner, g)
		rAcc := collectOwnDataAccessesDeep(root.val, funcs, map[ssa.Value]bool{})
		var filtered []dataAccess
		for _, acc := range rAcc {
			if skipSpawnPreShare(acc, spawner, g, preShare, ctor) {
				continue
			}
			fn := acc.instr.Parent()
			if fn != nil && goro[fn] {
				if k, ok := fieldIndexOf(acc); ok {
					thisFields[k] = true
				} else if !isObjectInitStore(acc) && !isShareSafeFieldStore(acc) && !isNestedShareSafeAccess(acc) {
					thisWhole = true
				}
			}
			filtered = append(filtered, acc)
		}
		perRoot[root.val] = filtered
	}
	for _, root := range dataRoots {
		for _, acc := range perRoot[root] {
			fn := acc.instr.Parent()
			if fn != nil && !goro[fn] && !thisWhole {
				if k, ok := fieldIndexOf(acc); ok && !thisFields[k] {
					continue // other go writes a field this spawn never touches
				}
			}
			if seenAcc[acc.instr] {
				continue
			}
			seenAcc[acc.instr] = true
			accesses = append(accesses, acc)
		}
		perRoot[root] = prepareGuardAccesses(filterRelevantOwnAccesses(perRoot[root], goro, thisFields, thisWhole))
	}
	accesses = prepareGuardAccesses(accesses)
	if len(accesses) == 0 {
		return true
	}

	var muCands []structuralGuard
	seenMu := map[string]bool{}
	for _, r := range dataRoots {
		for _, c := range findStructuralGuards(r) {
			key := c.structType.String() + "#" + itoa(c.field)
			if seenMu[key] {
				continue
			}
			seenMu[key] = true
			muCands = append(muCands, c)
		}
	}
	if hasTiedMutex(muCands, accesses) {
		return true
	}
	if freeStandingMutexGuards(accesses, funcs, false) {
		return true
	}

	var rwCands []structuralGuard
	seenRW := map[string]bool{}
	for _, r := range dataRoots {
		for _, c := range findStructuralRWGuards(r) {
			key := c.structType.String() + "#" + itoa(c.field)
			if seenRW[key] {
				continue
			}
			seenRW[key] = true
			rwCands = append(rwCands, c)
		}
	}
	if hasTiedMutex(rwCands, accesses) {
		return true
	}
	if freeStandingMutexGuards(accesses, funcs, true) {
		return true
	}
	if len(dataRoots) > 0 && constIndexPartitionOK(dataRoots[0], accesses) {
		return true
	}
	if len(dataRoots) > 0 && rangePartitionOK(dataRoots[0], accesses) {
		return true
	}
	if wait, ok := waitWindowOf(spawner, g); ok {
		partAcc := waitWindowAccesses(accesses, spawner, g, wait, goro)
		if len(dataRoots) > 0 && affineRangePartitionOK(dataRoots[0], partAcc) {
			return true
		}
	}
	if len(dataRoots) > 0 && fieldPartitionedGuards(dataRoots[0], accesses, funcs) {
		return true
	}

	for _, r := range dataRoots {
		rAcc := perRoot[r]
		if !accessesHaveWrite(rAcc) || onlySetupWrites(rAcc) {
			continue
		}
		if hasTiedMutex(findStructuralGuards(r), rAcc) {
			continue
		}
		if freeStandingMutexGuards(rAcc, funcs, false) {
			continue
		}
		if hasTiedMutex(findStructuralRWGuards(r), rAcc) {
			continue
		}
		if freeStandingMutexGuards(rAcc, funcs, true) {
			continue
		}
		if atomicsOnlyAccesses(rAcc) {
			continue
		}
		if wait, ok := waitWindowOf(spawner, g); ok {
			rPart := waitWindowAccesses(rAcc, spawner, g, wait, goro)
			if affineRangePartitionOK(r, rPart) {
				continue
			}
		}
		if fieldPartitionedGuards(r, rAcc, funcs) {
			continue
		}
		return false
	}
	return true
}

func filterRelevantOwnAccesses(accesses []dataAccess, goro map[*ssa.Function]bool, thisFields map[int]bool, thisWhole bool) []dataAccess {
	if thisWhole {
		return accesses
	}
	var out []dataAccess
	for _, acc := range accesses {
		fn := acc.instr.Parent()
		if fn != nil && goro[fn] {
			out = append(out, acc)
			continue
		}
		if k, ok := fieldIndexOf(acc); ok && thisFields[k] {
			out = append(out, acc)
		}
	}
	return out
}

func isWrittenAfterGoScoped(root ssa.Value, spawner *ssa.Function, g ssa.Instruction, share, goro map[*ssa.Function]bool, ctor *ssa.Function) bool {
	preShare := map[*ssa.Function]bool{}
	for f := range share {
		if f == nil || goro[f] || f == spawner {
			continue
		}
		if ctor != nil && f == ctor {
			// Only skip the constructor when every own-field write there
			// happens-before start/go. A write after New returns is concurrent.
			if !ctorHasPostShareWrite(root, ctor, spawner, g) {
				continue
			}
		}
		if isWrittenIn(root, map[*ssa.Function]bool{f: true}, true) {
			return true
		}
		for _, alias := range siblingShareAliases(root, share) {
			if isWrittenIn(alias, map[*ssa.Function]bool{f: true}, true) {
				return true
			}
		}
	}
	_ = preShare
	return hasWriteNotBefore(root, spawner, g)
}

// siblingShareAliases is the method-value / returned-pointer views of root
// that may be written after go without naming root's SSA value.
func siblingShareAliases(root ssa.Value, funcs map[*ssa.Function]bool) []ssa.Value {
	if root == nil {
		return nil
	}
	seen := map[ssa.Value]bool{root: true}
	var out []ssa.Value
	add := func(v ssa.Value) {
		if v == nil || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	for _, x := range methodValueAliases(root, funcs) {
		add(x)
	}
	for _, x := range returnValueAliases(root, funcs) {
		add(x)
	}
	return out
}

func ctorHasPostShareWrite(root ssa.Value, ctor, spawner *ssa.Function, g ssa.Instruction) bool {
	if ctor == nil {
		return false
	}
	accs := collectOwnDataAccesses(root, map[*ssa.Function]bool{ctor: true})
	for _, acc := range accs {
		if !acc.write {
			continue
		}
		if ctorWriteHappensBefore(acc, ctor, spawner, g) {
			continue
		}
		if ctor == spawner && dominatesInstr(acc.instr, g) {
			continue
		}
		return true
	}
	return false
}
