package nosharing

import (
	"go/types"
	"sync"

	"golang.org/x/tools/go/ssa"
)

// The single-owner proof (scheduler-loop invariant): accesses are safe when
// they all run inside one owner goroutine — a closure (or method) spawned by
// exactly one go statement that executes at most once per object, referenced
// by nothing else — plus its same-package Call/Defer tree. Accesses outside
// the owner must be provably pre-spawn: in the spawning function before the
// go, or in a caller before every call that reaches the spawning function
// (constructor setup).
//
// Two conditions close aliasing holes:
//   - freshness: the owned object must be a locally allocated cell holding
//     locally allocated pointers. A pointer loaded from a shared map means
//     two dynamic spawns can own the same object (bad_curio_locks).
//   - sibling exclusion: no other goroutine that can reach the same cell
//     may touch the owned fields (two event loops are not one owner).
//
// All package-wide facts (spawn list, reachability, per-sibling touched
// fields) are computed once per package and cached: the proof runs per
// go-site × root × field-group and uncached recomputation is quadratic
// enough to exhaust memory on scheduler-sized packages.

// ownerCache holds per-package spawn structure for the single-owner proof.
type ownerCache struct {
	mu     sync.Mutex
	spawns []spawnSite
	owners map[*ssa.Function]*ssa.Go
	// reach memoizes callReachable per function.
	reach map[*ssa.Function]map[*ssa.Function]bool
	// touched memoizes the field indices a spawned function accesses on a
	// given cell (via bound FreeVars or passed args). -1 means a
	// whole-object or unclassifiable access.
	touched map[fnCell]map[int]bool
}

type spawnSite struct {
	g      *ssa.Go
	target *ssa.Function // nil for dynamic go
	cells  []ssa.Value   // canonical objects bound/passed into the goroutine
}

type fnCell struct {
	fn   *ssa.Function
	cell ssa.Value
}

var ownerCaches sync.Map // *ssa.Package → *ownerCache

func ownerCacheFor(pkg *ssa.Package, funcs map[*ssa.Function]bool) *ownerCache {
	if pkg == nil {
		return nil
	}
	if v, ok := ownerCaches.Load(pkg); ok {
		return v.(*ownerCache)
	}
	c := &ownerCache{
		reach:   map[*ssa.Function]map[*ssa.Function]bool{},
		touched: map[fnCell]map[int]bool{},
	}
	c.build(funcs)
	actual, _ := ownerCaches.LoadOrStore(pkg, c)
	return actual.(*ownerCache)
}

func (c *ownerCache) build(funcs map[*ssa.Function]bool) {
	spawnsByTarget := map[*ssa.Function][]*ssa.Go{}
	for fn := range funcs {
		if fn == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				g, ok := instr.(*ssa.Go)
				if !ok || g.Common() == nil {
					continue
				}
				site := spawnSite{g: g}
				if mc, isMC := g.Common().Value.(*ssa.MakeClosure); isMC {
					site.target, _ = mc.Fn.(*ssa.Function)
					for _, bind := range mc.Bindings {
						if cell := stripToObject(bind); cell != nil {
							site.cells = append(site.cells, cell)
						}
					}
				} else if cal := staticCallee(g.Common()); cal != nil {
					site.target = cal
				}
				for _, arg := range g.Common().Args {
					if cell := stripToObject(arg); cell != nil && mayContainPointers(arg.Type()) {
						site.cells = append(site.cells, cell)
					}
				}
				c.spawns = append(c.spawns, site)
				if site.target != nil {
					spawnsByTarget[site.target] = append(spawnsByTarget[site.target], g)
				}
			}
		}
	}

	c.owners = map[*ssa.Function]*ssa.Go{}
	for target, gos := range spawnsByTarget {
		if len(gos) != 1 {
			continue
		}
		g := gos[0]
		if g.Block() == nil || blockInCycle(g.Block()) {
			continue
		}
		if mc, ok := g.Common().Value.(*ssa.MakeClosure); ok {
			// The closure must have no other references (no stored copies
			// that could be spawned or called elsewhere).
			if refs := mc.Referrers(); refs != nil {
				only := true
				for _, r := range *refs {
					if r != g {
						only = false
						break
					}
				}
				if !only {
					continue
				}
			}
		} else if calledElsewhere(target, g, funcs) {
			continue
		}
		c.owners[target] = g
	}
}

func (c *ownerCache) reachable(fn *ssa.Function) map[*ssa.Function]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.reach[fn]; ok {
		return r
	}
	r := callReachable(fn)
	c.reach[fn] = r
	return r
}

// fieldsTouchedOn returns the top-level field indices of cell that the
// spawned function fn may access, through FreeVars bound to cell in spawner
// or through a parameter receiving cell. Cached per (fn, cell).
func (c *ownerCache) fieldsTouchedOn(g *ssa.Go, target *ssa.Function, spawner *ssa.Function, cell ssa.Value) map[int]bool {
	key := fnCell{fn: target, cell: cell}
	c.mu.Lock()
	if t, ok := c.touched[key]; ok {
		c.mu.Unlock()
		return t
	}
	c.mu.Unlock()

	out := map[int]bool{}
	reach := c.reachable(target)
	var roots []ssa.Value
	for _, fv := range closureFreeVarsBoundTo(spawner, target, cell) {
		roots = append(roots, fv)
	}
	if g != nil && g.Common() != nil {
		for i, arg := range g.Common().Args {
			if stripToObject(arg) == cell && i < len(target.Params) {
				roots = append(roots, target.Params[i])
			}
		}
	}
	for _, r := range roots {
		for _, acc := range collectDataAccesses(r, reach) {
			if acc.addr != nil && (isChanType(acc.addr.Type()) || isChanType(pointeeType(acc.addr.Type()))) {
				continue // channel fields carry their own synchronization
			}
			out[accessFieldIndex(r, acc)] = true
		}
	}

	c.mu.Lock()
	c.touched[key] = out
	c.mu.Unlock()
	return out
}

// singleOwnerGoroutineOK is the whole-object variant: the owner goroutine is
// the only toucher of every field (siblings must touch no field at all).
func singleOwnerGoroutineOK(root ssa.Value, accesses []dataAccess, funcs map[*ssa.Function]bool) bool {
	return singleOwnerFieldOK(root, accesses, funcs, -2)
}

// fieldSingleOwnerOK groups the same-object union of accesses per top-level
// field and accepts when each group is chan-only, atomics-only,
// mutex-consistent, or owned by a single goroutine. It must be called with
// the union across all sibling aliases — per-root access lists hide sibling
// goroutines and would make two loops look like one owner.
func fieldSingleOwnerOK(root ssa.Value, accesses []dataAccess, funcs map[*ssa.Function]bool) bool {
	if root == nil || len(accesses) == 0 {
		return false
	}
	if _, isGlobal := root.(*ssa.Global); isGlobal {
		return false
	}
	byField := map[int][]dataAccess{}
	for _, acc := range accesses {
		if !acc.write && isFrozenFieldAccess(root, acc, funcs) {
			continue
		}
		if acc.addr != nil && (isChanType(acc.addr.Type()) || isChanType(pointeeType(acc.addr.Type()))) {
			continue
		}
		byField[accessFieldIndex(root, acc)] = append(byField[accessFieldIndex(root, acc)], acc)
	}
	if len(byField) == 0 {
		return false
	}
	sawOwner := false
	for fi, group := range byField {
		if atomicsOnlyAccesses(group) {
			continue
		}
		if singleOwnerFieldOK(root, group, funcs, fi) {
			sawOwner = true
			continue
		}
		if consistentMutexGuards(group, funcs) || freeStandingMutexGuards(group, funcs, false) || freeStandingMutexGuards(group, funcs, true) {
			continue
		}
		return false
	}
	return sawOwner
}

// singleOwnerFieldOK proves single ownership of root (field >= 0: only that
// top-level field; field == -2: the whole object) over the given accesses.
func singleOwnerFieldOK(root ssa.Value, accesses []dataAccess, funcs map[*ssa.Function]bool, field int) bool {
	if root == nil || len(accesses) == 0 {
		return false
	}
	if _, isGlobal := root.(*ssa.Global); isGlobal {
		return false
	}
	pkg := pkgOfFuncs(funcs)
	if pkg == nil {
		return false
	}
	cache := ownerCacheFor(pkg, funcs)
	if cache == nil || len(cache.owners) == 0 {
		return false
	}
	rootCell := canonicalOwnedCell(root)
	if rootCell == nil {
		return false
	}
	for closure, g := range cache.owners {
		spawnFn := g.Parent()
		if spawnFn == nil {
			continue
		}
		if !ownedObjectFresh(root, rootCell) {
			continue
		}
		if siblingTouchesField(cache, g, rootCell, field) {
			continue
		}
		reach := cache.reachable(closure)
		sawOwner := false
		valid := true
		for _, acc := range accesses {
			fn := acc.instr.Parent()
			if fn == nil {
				valid = false
				break
			}
			if fn == closure || reach[fn] {
				sawOwner = true
				continue
			}
			anchor := acc.instr
			if anchor.Parent() != spawnFn && acc.via != nil && acc.via.Parent() == spawnFn {
				anchor = acc.via
			}
			if anchor.Parent() == spawnFn && instrBeforeInstr(anchor, g) {
				continue
			}
			if callerPreSpawn(acc.instr, acc.via, spawnFn) {
				continue
			}
			valid = false
			break
		}
		if valid && sawOwner {
			return true
		}
	}
	return false
}

// siblingTouchesField reports that a goroutine other than ownerGo may access
// the given field of cell (field == -2: any field). Dynamic spawns refuse.
func siblingTouchesField(cache *ownerCache, ownerGo *ssa.Go, cell ssa.Value, field int) bool {
	for _, site := range cache.spawns {
		if site.g == ownerGo {
			continue
		}
		bound := false
		for _, c := range site.cells {
			if c == cell {
				bound = true
				break
			}
		}
		if !bound {
			continue
		}
		if site.target == nil {
			return true // dynamic go touching the cell: unknown body
		}
		touched := cache.fieldsTouchedOn(site.g, site.target, site.g.Parent(), cell)
		if len(touched) == 0 {
			continue
		}
		if field == -2 || touched[field] || touched[-1] {
			return true
		}
	}
	return false
}

// canonicalOwnedCell resolves root to its Alloc cell (FreeVars via their
// unique closure binding).
func canonicalOwnedCell(root ssa.Value) ssa.Value {
	cell := stripToObject(root)
	if fv, ok := cell.(*ssa.FreeVar); ok {
		enclosing := fv.Parent()
		if enclosing == nil {
			return nil
		}
		outer := enclosing.Parent()
		if outer == nil {
			return nil
		}
		cells := closureBindingCells(outer, fv)
		if len(cells) != 1 {
			return nil
		}
		cell = cells[0]
	}
	if _, ok := cell.(*ssa.Alloc); !ok {
		return nil
	}
	return cell
}

// ownedObjectFresh reports that the canonical cell is an Alloc, and if it is
// a pointer cell every value stored into it is itself a local Alloc
// (constructor literal), never a pointer loaded from shared state.
func ownedObjectFresh(root ssa.Value, cell ssa.Value) bool {
	al, ok := cell.(*ssa.Alloc)
	if !ok {
		return false
	}
	t := types.Unalias(al.Type())
	p, ok := t.(*types.Pointer)
	if !ok {
		return false
	}
	if _, isPtrCell := types.Unalias(p.Elem()).(*types.Pointer); !isPtrCell {
		// Alloc of the object itself: fresh by construction.
		return true
	}
	fn := al.Parent()
	if fn == nil {
		return false
	}
	found := false
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			st, ok := instr.(*ssa.Store)
			if !ok || st.Addr != al {
				continue
			}
			if _, isAlloc := stripToObject(st.Val).(*ssa.Alloc); !isAlloc {
				return false
			}
			found = true
		}
	}
	return found
}

func pkgOfFuncs(funcs map[*ssa.Function]bool) *ssa.Package {
	for fn := range funcs {
		if fn != nil && fn.Pkg != nil {
			return fn.Pkg
		}
	}
	return nil
}

// calledElsewhere reports any call/defer/other-go of target besides g.
func calledElsewhere(target *ssa.Function, g *ssa.Go, funcs map[*ssa.Function]bool) bool {
	for fn := range funcs {
		if fn == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				if instr == g {
					continue
				}
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
				if staticCallee(c) == target {
					return true
				}
			}
		}
	}
	return false
}
