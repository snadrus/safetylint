// Command safetylint proves a Go program is memory-safe under channel-only
// sharing discipline: no unsafe/cgo escape hatches, and no cross-goroutine
// memory sharing except via channels with freeze-after-send for pointers.
package main

import (
	"safetylint/internal/nosharing"
	"safetylint/internal/nounsafe"

	"golang.org/x/tools/go/analysis/multichecker"
)

func main() {
	multichecker.Main(
		nounsafe.Analyzer,
		nosharing.Analyzer,
	)
}
