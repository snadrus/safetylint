package nosharing

import (
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/tools/go/ssa"
)

// stdlibReadOnly lists bodyless GOROOT helpers that only read map/slice
// arguments (passing a loaded global header is not a freeze write).
var stdlibReadOnly = map[string]map[string]bool{
	"time": {
		"Since": true, "Until": true,
	},
	"maps": {
		"All": true, "Keys": true, "Values": true,
		"equal": true, "equalFunc": true,
	},
	"slices": {
		"All": true, "Values": true,
		"Contains": true, "ContainsFunc": true,
		"Index": true, "IndexFunc": true,
		"BinarySearch": true, "BinarySearchFunc": true,
		"Clone": true, "Concat": true,
		"equal": true, "equalFunc": true,
		"Compare": true, "CompareFunc": true,
		"IsSorted": true, "IsSortedFunc": true,
		"Max": true, "MaxFunc": true, "Min": true, "MinFunc": true,
	},
}

func init() {
	// Go's exported names are capitalized; accept both for robustness.
	for _, byName := range stdlibReadOnly {
		for name := range byName {
			if name != "" && name[0] >= 'a' && name[0] <= 'z' {
				cap := strings.ToUpper(name[:1]) + name[1:]
				byName[cap] = true
			}
		}
	}
}

func isAtomicCallee(fn *ssa.Function) bool {
	if fn == nil {
		return false
	}
	return calleePkgPath(fn) == "sync/atomic"
}

func isStdlibReadOnlyCall(fn *ssa.Function) bool {
	if fn == nil {
		return false
	}
	// Prefer the generic origin so instantiated maps.Values[...] matches.
	if orig := fn.Origin(); orig != nil {
		fn = orig
	}
	byName := stdlibReadOnly[calleePkgPath(fn)]
	if byName == nil {
		return false
	}
	return byName[calleeBaseName(fn)]
}

func calleeBaseName(fn *ssa.Function) string {
	if fn == nil {
		return ""
	}
	name := fn.Name()
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.IndexByte(name, '['); i >= 0 {
		name = name[:i]
	}
	return name
}

var stdlibPathCache sync.Map // string -> bool

// calleeInGOROOT reports whether fn is defined in the standard library.
// Testdata and module-cache packages must not match (no-dot paths are not
// enough — use GOROOT src existence, then the function's file).
func calleeInGOROOT(fn *ssa.Function) bool {
	if fn == nil {
		return false
	}
	if orig := fn.Origin(); orig != nil {
		fn = orig
	}
	if path := calleePkgPath(fn); path != "" && stdlibPkgPath(path) {
		return true
	}
	return fileInGOROOT(funcFilename(fn))
}

func stdlibPkgPath(path string) bool {
	if path == "" || strings.Contains(path, ".") {
		return false
	}
	if v, ok := stdlibPathCache.Load(path); ok {
		return v.(bool)
	}
	goroot := build.Default.GOROOT
	ok := false
	if goroot != "" {
		_, err := os.Stat(filepath.Join(goroot, "src", filepath.FromSlash(path)))
		ok = err == nil
	}
	stdlibPathCache.Store(path, ok)
	return ok
}

func fileInGOROOT(file string) bool {
	if file == "" {
		return false
	}
	goroot := build.Default.GOROOT
	if goroot != "" && strings.HasPrefix(file, goroot) {
		return true
	}
	return strings.Contains(file, "/go/src/")
}

func funcFilename(fn *ssa.Function) string {
	if fn == nil {
		return ""
	}
	prog := fn.Prog
	if prog == nil && fn.Pkg != nil {
		prog = fn.Pkg.Prog
	}
	if prog == nil || prog.Fset == nil {
		return ""
	}
	if fn.Pos().IsValid() {
		return prog.Fset.Position(fn.Pos()).Filename
	}
	if obj := fn.Object(); obj != nil && obj.Pos().IsValid() {
		return prog.Fset.Position(obj.Pos()).Filename
	}
	return ""
}
