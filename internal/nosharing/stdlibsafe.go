package nosharing

import (
	"strings"

	"golang.org/x/tools/go/ssa"
)

// stdlibReadOnly lists bodyless GOROOT helpers that only read map/slice
// arguments (passing a loaded global header is not a freeze write).
var stdlibReadOnly = map[string]map[string]bool{
	"maps": {
		"All": true, "Keys": true, "Values": true,
		"Equal": true, "EqualFunc": true,
	},
	"slices": {
		"All": true, "Values": true,
		"Contains": true, "ContainsFunc": true,
		"Index": true, "IndexFunc": true,
		"BinarySearch": true, "BinarySearchFunc": true,
		"Clone": true, "Concat": true,
		"Equal": true, "EqualFunc": true,
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
	name := fn.Name()
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.IndexByte(name, '['); i >= 0 {
		name = name[:i]
	}
	return byName[name]
}
