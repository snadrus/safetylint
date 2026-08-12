package nosharing

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// collectRoots finds values shared between the spawning goroutine and the
// callee: closure free variables, pointer-ish arguments, and package globals
// accessed from goroutine-reachable code.
func collectRoots(g *ssa.Go, callee *ssa.Function, globals map[*ssa.Global]bool) []sharedRoot {
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
	// writes after go are visible.
	if common := g.Common(); common != nil {
		if mc, ok := common.Value.(*ssa.MakeClosure); ok {
			for i, bind := range mc.Bindings {
				name := "?"
				if i < len(callee.FreeVars) {
					name = callee.FreeVars[i].Name()
				}
				add(bind, "captured free variable "+name)
				if i < len(callee.FreeVars) {
					add(callee.FreeVars[i], "captured free variable "+name)
				}
			}
		}
	}

	// Arguments at the go call site.
	for i, arg := range g.Common().Args {
		if !mayContainPointers(arg.Type()) && !isAddressTakenLocal(arg) {
			continue
		}
		add(arg, "pointer-ish argument")
		if i < len(callee.Params) {
			add(callee.Params[i], "pointer-ish parameter")
		}
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

// nonGlobalRoots filters out package globals (handled by freeze analysis).
func nonGlobalRoots(roots []sharedRoot) []sharedRoot {
	var out []sharedRoot
	for _, r := range roots {
		if _, ok := r.val.(*ssa.Global); ok {
			continue
		}
		out = append(out, r)
	}
	return out
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
						if argSeen {
							if param != nil {
								add(param, "alias of shared argument")
							}
							add(arg, "alias of shared argument")
							if argObj != nil {
								add(argObj, "alias of shared argument")
							}
						}
						if paramSeen {
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
