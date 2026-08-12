package toolver_test

import (
	"go/version"
	"runtime"
	"testing"

	"safetylint/internal/toolver"
)

func TestMaxVerifiedGoParses(t *testing.T) {
	if !version.IsValid(toolver.MaxVerifiedGo) {
		t.Fatalf("MaxVerifiedGo %q is not a valid Go version", toolver.MaxVerifiedGo)
	}
}

func TestCurrentToolchainNotNewerThanVerified(t *testing.T) {
	// CI / local toolchain used to develop this tool should not be ahead of
	// MaxVerifiedGo without bumping the constant (and curated stdlib lists).
	running := runtime.Version()
	if version.Compare(running, toolver.MaxVerifiedGo) > 0 {
		t.Fatalf("running %s is newer than MaxVerifiedGo %s; bump MaxVerifiedGo and review curated stdlib coverage",
			running, toolver.MaxVerifiedGo)
	}
}
