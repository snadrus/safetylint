// Package toolver records which Go toolchain this linter's stdlib model
// was verified against, and warns when running on a newer version.
package toolver

import (
	"go/version"
	"runtime"
	"sync"

	"golang.org/x/tools/go/analysis"
)

// MaxVerifiedGo is the newest Go toolchain whose standard library this
// tool's curated spawn/async/share model is known to cover.
const MaxVerifiedGo = "go1.26.5"

var warnOnce sync.Once

// WarnIfTooNew reports once per process if the running toolchain is newer
// than MaxVerifiedGo.
func WarnIfTooNew(pass *analysis.Pass) {
	if pass == nil || len(pass.Files) == 0 {
		return
	}
	running := runtime.Version()
	if version.Compare(running, MaxVerifiedGo) <= 0 {
		return
	}
	warnOnce.Do(func() {
		pass.Reportf(pass.Files[0].Package,
			"running on %s, a version too new for this tool (verified through %s); faults via new standard funcs may be possible",
			running, MaxVerifiedGo)
	})
}
