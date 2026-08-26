package nounsafe_test

import (
	"testing"

	"safetylint/internal/nounsafe"

	"golang.org/x/tools/go/analysis/analysistest"
)

func TestNounsafe(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, nounsafe.Analyzer, "a", "asm", "cgo", "clean", "linkname", "refl", "good_nolint", "bad_nolint_nosharing")
}
