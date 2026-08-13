package main

import (
	"os"
	"testing"
)

func TestPeelBuildTags(t *testing.T) {
	args := []string{"safetylint", "-tags", "cunative nosupraseal skiff", "-test=false", "./cmd/skiff"}
	tags, ok := peelBuildTags(&args)
	if !ok || tags != "cunative nosupraseal skiff" {
		t.Fatalf("tags=%q ok=%v", tags, ok)
	}
	if got := args; len(got) != 3 || got[1] != "-test=false" || got[2] != "./cmd/skiff" {
		t.Fatalf("args after peel: %#v", got)
	}

	args = []string{"safetylint", "-tags=a,b", "./pkg"}
	tags, ok = peelBuildTags(&args)
	if !ok || tags != "a,b" {
		t.Fatalf("tags=%q ok=%v", tags, ok)
	}
}

func TestInjectGOFLAGSTags(t *testing.T) {
	t.Setenv("GOFLAGS", "-mod=mod -tags=old")
	if err := injectGOFLAGSTags("cunative nosupraseal skiff"); err != nil {
		t.Fatal(err)
	}
	got := os.Getenv("GOFLAGS")
	want := "-mod=mod -tags=cunative,nosupraseal,skiff"
	if got != want {
		t.Fatalf("GOFLAGS=%q want %q", got, want)
	}
}
