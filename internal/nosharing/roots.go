package nosharing

import (
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// collectRoots finds values shared between the spawning goroutine and the
// callee: closure free variables, pointer-ish arguments, and package globals
// accessed from goroutine-reachable code.
func collectRoots(value ssa.Value, args []ssa.Value, callee *ssa.Function, globals map[*ssa.Global]bool) []sharedRoot {
	var roots []sharedRoot
	seen := map[ssa.Value]bool{}

	add := func(v ssa.Value, reason string) {
		if v == nil || seen[v] {
			return
		}
		seen[v] = true
		roots = append(roots, sharedRoot{val: v, reason: reason})
	}

	// Closure free variables (captures). Prefer the FreeVar inside the
	// callee for write detection; also keep the binding so spawner-side
	// writes after go are visible. Package globals are freeze-owned: do
	// not also treat a FreeVar cell that merely names a global as a
	// separate shared heap root.
	if mc, ok := value.(*ssa.MakeClosure); ok {
		for i, bind := range mc.Bindings {
			name := "?"
			if callee != nil && i < len(callee.FreeVars) {
				name = callee.FreeVars[i].Name()
			}
			add(bind, "captured free variable "+name)
			if callee != nil && i < len(callee.FreeVars) && !isGlobalObject(bind) {
				add(callee.FreeVars[i], "captured free variable "+name)
			}
		}
	}

	// Arguments at the go call site (empty for AfterFunc-style callback slots).
	for i, arg := range args {
		if !mayContainPointers(arg.Type()) && !isAddressTakenLocal(arg) {
			continue
		}
		add(arg, "pointer-ish argument")
		if callee != nil && i < len(callee.Params) {
			add(callee.Params[i], "pointer-ish parameter")
		}
	}

	if callee == nil {
		return roots
	}

	// Package globals accessed from goroutine-reachable functions.
	reachable := reachableFuncs(callee, callee.Pkg)
	for fn := range reachable {
		if fn == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				for _, op := range instr.Operands(nil) {
					if op == nil || *op == nil {
						continue
					}
					if gl, ok := (*op).(*ssa.Global); ok {
						if len(globals) == 0 || globals[gl] {
							add(gl, "package global accessed from goroutine")
						}
					}
				}
			}
		}
	}

	return roots
}

// sharePeers returns focus plus non-global roots that refer to the same
// shared object identity: same stripToObject, or the same pointer/element
// type (so Alloc/Param/FreeVar views of one value stay together for tied
// mutex and partition proofs without pulling in unrelated aliases).
func sharePeers(focus ssa.Value, roots []sharedRoot) []sharedRoot {
	if focus == nil {
		return nil
	}
	focusObj := stripToObject(focus)
	focusTy := focus.Type().String()
	seen := map[ssa.Value]bool{}
	var out []sharedRoot
	add := func(r sharedRoot) {
		if r.val == nil || seen[r.val] {
			return
		}
		if _, ok := r.val.(*ssa.Global); ok {
			return
		}
		seen[r.val] = true
		out = append(out, r)
	}
	add(sharedRoot{val: focus, reason: "focus"})
	for _, r := range roots {
		if r.val == nil {
			continue
		}
		if r.val == focus || stripToObject(r.val) == focusObj {
			add(r)
			continue
		}
		if r.val.Type().String() == focusTy {
			add(r)
		}
	}
	return out
}

// sameObjectPeersGo returns roots that alias the same cell as focus (Alloc /
// Global / FreeVar / Parameter bindings), excluding unrelated same-type locals.
func sameObjectPeersGo(focus ssa.Value, roots []sharedRoot, spawner *ssa.Function, g ssa.Instruction, allFuncs map[*ssa.Function]bool) []sharedRoot {
	if focus == nil {
		return nil
	}
	funcs := allFuncs
	if funcs == nil {
		funcs = map[*ssa.Function]bool{}
		if spawner != nil {
			funcs[spawner] = true
		}
		for _, r := range roots {
			if r.val != nil && r.val.Parent() != nil {
				funcs[r.val.Parent()] = true
			}
		}
	}
	cells := map[ssa.Value]bool{focus: true}
	if obj := stripToObject(focus); obj != nil {
		cells[obj] = true
	}
	// The go callee's own parameters are bound only via this go (below).
	// argsPassedAs on those params would pull in every *T that ever called
	// the same method, merging unrelated objects.
	goCalleeParam := map[ssa.Value]bool{}
	if g != nil {
		if common := callCommonOf(g); common != nil {
			if cal := staticCallee(common); cal != nil {
				for _, p := range cal.Params {
					goCalleeParam[p] = true
				}
			}
		}
	}
	// Fixpoint: chase Alloc↔Parameter↔FreeVar aliases through call/go sites.
	for changed := true; changed; {
		changed = false
		add := func(v ssa.Value) {
			if v == nil || cells[v] {
				return
			}
			cells[v] = true
			changed = true
		}
		snapshot := make([]ssa.Value, 0, len(cells))
		for c := range cells {
			snapshot = append(snapshot, c)
		}
		for _, c := range snapshot {
			for _, x := range closureBindingCells(spawner, c) {
				add(x)
			}
			for _, x := range goParamBindings(g, c) {
				add(x)
			}
			for _, p := range paramsReceivingCell(c, funcs) {
				add(p)
			}
			// Method receivers share one SSA Parameter across every *T that
			// called the method. Chasing Alloc args would merge unrelated
			// objects (every TaskEngine). Parameter/FreeVar args (dst.WriteMessage)
			// still connect the method to the caller param; non-method params
			// (spawn(s *S)) still chase to the concrete Alloc.
			if !goCalleeParam[c] {
				for _, x := range argsPassedAs(c, funcs) {
					if isMethodRecvParam(c) && isDistinctHeapAlloc(x, cells) {
						continue
					}
					add(x)
					add(stripToObject(x))
				}
			}
			if g != nil {
				if common := callCommonOf(g); common != nil {
					for i, arg := range common.Args {
						if arg == c || stripToObject(arg) == c || stripToObject(arg) == stripToObject(c) {
							add(arg)
							add(stripToObject(arg))
							if cal := staticCallee(common); cal != nil && i < len(cal.Params) {
								add(cal.Params[i])
							}
						}
					}
				}
			}
		}
		for _, r := range roots {
			if r.val == nil {
				continue
			}
			if fv, ok := r.val.(*ssa.FreeVar); ok {
				for _, b := range closureBindingCells(spawner, fv) {
					if cells[b] {
						add(fv)
					}
				}
			}
			if cells[stripToObject(r.val)] {
				add(r.val)
			}
		}
	}
	seen := map[ssa.Value]bool{}
	var out []sharedRoot
	addRoot := func(r sharedRoot) {
		if r.val == nil || seen[r.val] {
			return
		}
		if _, ok := r.val.(*ssa.Global); ok {
			return
		}
		seen[r.val] = true
		out = append(out, r)
	}
	addRoot(sharedRoot{val: focus, reason: "focus"})
	for _, r := range roots {
		if r.val == nil {
			continue
		}
		if cells[r.val] || cells[stripToObject(r.val)] {
			addRoot(r)
		}
	}
	// Also synthesize peers for aliased Parameters/Allocs not already in roots
	// so their accesses join the mutex proof.
		for c := range cells {
		switch c.(type) {
		case *ssa.Parameter, *ssa.Alloc, *ssa.FreeVar:
			addRoot(sharedRoot{val: c, reason: "alias"})
		}
	}
	return out
}

func isMethodRecvParam(v ssa.Value) bool {
	p, ok := v.(*ssa.Parameter)
	if !ok || p.Parent() == nil || p.Parent().Signature == nil || p.Parent().Signature.Recv() == nil {
		return false
	}
	params := p.Parent().Params
	return len(params) > 0 && params[0] == p
}

func isDistinctHeapAlloc(x ssa.Value, cells map[ssa.Value]bool) bool {
	obj := stripToObject(x)
	if obj == nil {
		return false
	}
	if cells[x] || cells[obj] {
		return false
	}
	_, ok := obj.(*ssa.Alloc)
	return ok
}

// argsPassedAs returns concrete arguments passed to Parameter cell at call sites.
func argsPassedAs(cell ssa.Value, funcs map[*ssa.Function]bool) []ssa.Value {
	p, ok := cell.(*ssa.Parameter)
	if !ok || p.Parent() == nil {
		return nil
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
		return nil
	}
	var out []ssa.Value
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
				if cal != parent && (cal == nil || cal.Origin() != parent) {
					continue
				}
				if idx >= len(c.Args) {
					continue
				}
				out = append(out, c.Args[idx])
			}
		}
	}
	return out
}

// paramsReceivingCell finds Parameters of callees in funcs that are passed cell
// at a static Call/Go/Defer site (same heap identity).
func paramsReceivingCell(cell ssa.Value, funcs map[*ssa.Function]bool) []*ssa.Parameter {
	if cell == nil {
		return nil
	}
	seen := map[*ssa.Parameter]bool{}
	var out []*ssa.Parameter
	add := func(p *ssa.Parameter) {
		if p == nil || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
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
				if cal == nil {
					continue
				}
				for i, arg := range c.Args {
					if stripToObject(arg) != cell || i >= len(cal.Params) {
						continue
					}
					add(cal.Params[i])
				}
			}
		}
	}
	return out
}

// goParamBindings returns Alloc/Global cells passed as the go argument that
// becomes Parameter focus (or focus itself when not a Parameter).
func goParamBindings(g ssa.Instruction, focus ssa.Value) []ssa.Value {
	if g == nil || focus == nil {
		return nil
	}
	common := callCommonOf(g)
	if common == nil {
		return nil
	}
	p, ok := focus.(*ssa.Parameter)
	if !ok || p.Parent() == nil {
		return nil
	}
	cal := staticCallee(common)
	if cal == nil {
		return nil
	}
	parent := p.Parent()
	if cal != parent && (cal.Origin() == nil || cal.Origin() != parent) {
		if cal.Name() != parent.Name() {
			return nil
		}
	}
	idx := -1
	for i, param := range parent.Params {
		if param == p {
			idx = i
			break
		}
	}
	if idx < 0 || idx >= len(common.Args) {
		return nil
	}
	cell := stripToObject(common.Args[idx])
	if cell == nil {
		return nil
	}
	return []ssa.Value{cell}
}

// methodValueAliases returns the bound-method FreeVar and the underlying
// method receiver when a MakeClosure binds cell (t.onHead retained by
// AddWatcher). Bound wrappers are synthetic (Pkg==nil) and must be chased
// to the real method Parameter.
func methodValueAliases(cell ssa.Value, funcs map[*ssa.Function]bool) []ssa.Value {
	if cell == nil {
		return nil
	}
	obj := stripToObject(cell)
	if obj == nil {
		obj = cell
	}
	var out []ssa.Value
	seen := map[ssa.Value]bool{}
	add := func(v ssa.Value) {
		if v == nil || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	scan := func(fn *ssa.Function) {
		if fn == nil {
			return
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				mc, ok := instr.(*ssa.MakeClosure)
				if !ok {
					continue
				}
				if !closureBindsValue(mc, cell, obj) {
					continue
				}
				cal, _ := mc.Fn.(*ssa.Function)
				if cal == nil {
					continue
				}
				for _, fv := range cal.FreeVars {
					add(fv)
				}
				isBound := strings.HasSuffix(cal.Name(), "$bound")
				isMethodVal := cal.Signature != nil && cal.Signature.Recv() != nil && len(mc.Bindings) > 0
				if !isBound && !isMethodVal {
					continue
				}
				if cal.Signature != nil && cal.Signature.Recv() != nil && len(cal.Params) > 0 {
					add(cal.Params[0])
				}
				// Bound wrappers are synthetic: chase the immediate method
				// they call, not the whole reachable *T method set.
				if !isBound {
					continue
				}
				for _, b := range cal.Blocks {
					for _, in := range b.Instrs {
						call, ok := in.(*ssa.Call)
						if !ok {
							continue
						}
						f := call.Common().StaticCallee()
						if f == nil || f.Signature == nil || f.Signature.Recv() == nil || len(f.Params) == 0 {
							continue
						}
						add(f.Params[0])
					}
				}
			}
		}
	}
	for fn := range funcs {
		scan(fn)
	}
	return out
}

func closureBindsValue(mc *ssa.MakeClosure, cell, obj ssa.Value) bool {
	if mc == nil {
		return false
	}
	for _, bind := range mc.Bindings {
		if bind == cell || bind == obj || stripToObject(bind) == cell || stripToObject(bind) == obj {
			return true
		}
	}
	return false
}

// returnValueAliases reports Call results of functions that return cell
// (start() returning the spawned *Task; New() returning *Replacer).
func returnValueAliases(cell ssa.Value, funcs map[*ssa.Function]bool) []ssa.Value {
	if cell == nil {
		return nil
	}
	obj := stripToObject(cell)
	if obj == nil {
		return nil
	}
	retFns := map[*ssa.Function]bool{}
	for fn := range funcs {
		if fn == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				ret, ok := instr.(*ssa.Return)
				if !ok {
					continue
				}
				for _, r := range ret.Results {
					if r == cell || r == obj || stripToObject(r) == cell || stripToObject(r) == obj {
						if functionSpawnsCell(fn, cell, obj) {
							retFns[fn] = true
						}
					}
				}
			}
		}
	}
	if len(retFns) == 0 {
		return nil
	}
	var out []ssa.Value
	seen := map[ssa.Value]bool{}
	add := func(v ssa.Value) {
		if v == nil || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	for fn := range funcs {
		if fn == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				call, ok := instr.(*ssa.Call)
				if !ok {
					continue
				}
				cal := call.Common().StaticCallee()
				if cal == nil || !retFns[cal] {
					if cal != nil && cal.Origin() != nil && retFns[cal.Origin()] {
						add(call)
					}
					continue
				}
				add(call)
			}
		}
	}
	return out
}

func functionSpawnsCell(fn *ssa.Function, cell, obj ssa.Value) bool {
	if fn == nil {
		return false
	}
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			g, ok := instr.(*ssa.Go)
			if !ok || g.Common() == nil {
				continue
			}
			c := g.Common()
			for _, arg := range c.Args {
				if arg == cell || arg == obj || stripToObject(arg) == cell || stripToObject(arg) == obj {
					return true
				}
			}
			if mc, ok := c.Value.(*ssa.MakeClosure); ok {
				if closureBindsValue(mc, cell, obj) {
					return true
				}
			}
		}
	}
	return false
}

// expandRootAliases grows roots to every same-package SSA name for the same
// shareable objects: allocation sites, parameters that may receive them,
// free vars and their bindings. Needed so a tied-mutex proof sees every
// touchpoint after memory enters a shareable state (not only the go-site
// Param/FreeVar).
//
// Alias growth is capped: beyond maxRootAliases the mutex proof fails closed
// (incomplete alias set) rather than exploding on large packages.
const maxRootAliases = 128

func expandRootAliases(roots []sharedRoot, funcs map[*ssa.Function]bool) []sharedRoot {
	seen := map[ssa.Value]bool{}
	var out []sharedRoot
	capped := false
	add := func(v ssa.Value, reason string) {
		if v == nil || seen[v] || capped {
			return
		}
		if len(out) >= maxRootAliases {
			capped = true
			return
		}
		seen[v] = true
		out = append(out, sharedRoot{val: v, reason: reason})
	}
	for _, r := range roots {
		add(r.val, r.reason)
		add(stripToObject(r.val), r.reason)
	}

	for changed := true; changed && !capped; {
		changed = false
		before := len(seen)
		for fn := range funcs {
			if fn == nil || capped {
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
					case *ssa.MakeClosure:
						f, _ := in.Fn.(*ssa.Function)
						if f == nil {
							continue
						}
						for i, bind := range in.Bindings {
							if i >= len(f.FreeVars) {
								continue
							}
							fv := f.FreeVars[i]
							// Expand from binding→freevar or freevar→binding so
							// sibling closures capturing the same object share
							// one alias set (needed for partition proofs).
							if !seen[bind] && !seen[stripToObject(bind)] && !seen[fv] {
								continue
							}
							add(fv, "alias of shared capture")
							add(bind, "alias of shared capture")
							add(stripToObject(bind), "alias of shared capture")
						}
						// Method-value: MakeClosure binds recv to the method
						// Parameter, not a FreeVar (t.processHeadChange).
						if f.Signature != nil && f.Signature.Recv() != nil && len(in.Bindings) > 0 && len(f.Params) > 0 {
							bind := in.Bindings[0]
							if seen[bind] || seen[stripToObject(bind)] {
								add(f.Params[0], "method-value receiver")
								add(bind, "method-value receiver")
								add(stripToObject(bind), "method-value receiver")
							}
						}
						continue
					default:
						continue
					}
					cal := c.StaticCallee()
					if cal == nil || cal.Pkg != fn.Pkg {
						continue
					}
					for i, arg := range c.Args {
						// Do not collapse pointer-field projections to the parent
						// object: o.Cli is not an alias of o for sharing purposes.
						argObj := sameObjectRoot(arg)
						argSeen := seen[arg] || (argObj != nil && seen[argObj])
						var param *ssa.Parameter
						if i < len(cal.Params) {
							param = cal.Params[i]
						}
						paramSeen := param != nil && seen[param]
						// Method receivers / params that are only a package
						// global under another SSA name are freeze-owned.
						// Expanding them here causes shared_mem false positives
						// at unrelated go sites and can merge distinct heap
						// objects with the global through a shared Param.
						argIsGlobal := isGlobalObject(arg) || (argObj != nil && isGlobalObject(argObj))
						if argSeen {
							if !argIsGlobal {
								if param != nil {
									add(param, "alias of shared argument")
								}
								add(arg, "alias of shared argument")
								if argObj != nil {
									add(argObj, "alias of shared argument")
								}
							}
						}
						if paramSeen && !argIsGlobal {
							add(arg, "alias of shared parameter")
							if argObj != nil {
								add(argObj, "alias of shared parameter")
							}
						}
					}
				}
			}
		}
		if len(seen) != before {
			changed = true
		}
	}
	return out
}

// isGlobalObject reports whether v names a package global (possibly through
// FieldAddr / load / conversion stripping).
func isGlobalObject(v ssa.Value) bool {
	if v == nil {
		return false
	}
	if _, ok := v.(*ssa.Global); ok {
		return true
	}
	if _, ok := stripToObject(v).(*ssa.Global); ok {
		return true
	}
	return false
}

// sameObjectRoot strips conversions/loads that stay within one allocated
// object. It does not walk through pointer/map/slice/chan/interface fields:
// those refer to separately allocated objects.
func sameObjectRoot(v ssa.Value) ssa.Value {
	for v != nil {
		switch x := v.(type) {
		case *ssa.FieldAddr:
			if fieldAddrOfIndirect(x) {
				return nil
			}
			v = x.X
		case *ssa.IndexAddr:
			v = x.X
		case *ssa.UnOp:
			if x.Op != token.MUL {
				return v
			}
			if fieldAddrOfIndirect(x.X) {
				return nil
			}
			v = x.X
		case *ssa.ChangeType:
			v = x.X
		case *ssa.Convert:
			v = x.X
		case *ssa.Slice:
			v = x.X
		case *ssa.Extract:
			// Multi-return extracts are distinct values (e.g. ctx vs cancel
			// from context.WithCancel); the SSA tuple is not one object.
			return v
		case *ssa.Alloc, *ssa.Parameter, *ssa.FreeVar, *ssa.Global:
			return v
		default:
			return v
		}
	}
	return nil
}

// reachableFuncs returns the set of functions in pkg statically reachable
// from start (including start).
func reachableFuncs(start *ssa.Function, pkg *ssa.Package) map[*ssa.Function]bool {
	seen := map[*ssa.Function]bool{}
	var work []*ssa.Function
	add := func(f *ssa.Function) {
		if f == nil || seen[f] {
			return
		}
		if pkg != nil && f.Pkg != nil && f.Pkg != pkg {
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
				case *ssa.Go:
					add(staticCallee(in.Common()))
				case *ssa.MakeClosure:
					if fn, ok := in.Fn.(*ssa.Function); ok {
						add(fn)
					}
				}
			}
		}
	}
	return seen
}

func mayContainPointers(t types.Type) bool {
	t = types.Unalias(t)
	switch t := t.(type) {
	case *types.Pointer, *types.Map, *types.Chan, *types.Slice, *types.Signature:
		return true
	case *types.Interface:
		return true
	case *types.Array:
		return mayContainPointers(t.Elem())
	case *types.Struct:
		for i := 0; i < t.NumFields(); i++ {
			if mayContainPointers(t.Field(i).Type()) {
				return true
			}
		}
		return false
	case *types.Named:
		return mayContainPointers(t.Underlying())
	case *types.TypeParam, *types.Union:
		return true
	case *types.Basic:
		return t.Kind() == types.UnsafePointer
	default:
		return true
	}
}

func isAddressTakenLocal(v ssa.Value) bool {
	_, ok := v.(*ssa.Alloc)
	return ok
}

func isAddressableShared(v ssa.Value) bool {
	switch v.(type) {
	case *ssa.Alloc, *ssa.Global, *ssa.FreeVar:
		return true
	default:
		return false
	}
}
