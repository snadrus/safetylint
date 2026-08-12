// Command safetylint proves a Go program is memory-safe under channel-only
// sharing discipline: no unsafe/cgo escape hatches, and no cross-goroutine
// memory sharing except via channels with freeze-after-send for pointers.
package main

import (
	"fmt"
	"os"
	"strings"

	"safetylint/internal/nosharing"
	"safetylint/internal/nounsafe"

	"golang.org/x/tools/go/analysis/multichecker"
)

func main() {
	// multichecker's -tags flag is a deprecated no-op. Honor -tags the way
	// `go build -tags` does by injecting into GOFLAGS so packages.Load's
	// underlying `go list` selects the same files as the project's build.
	if tags, ok := peelBuildTags(&os.Args); ok {
		if err := injectGOFLAGSTags(tags); err != nil {
			fmt.Fprintf(os.Stderr, "safetylint: %v\n", err)
			os.Exit(2)
		}
	}
	multichecker.Main(
		nounsafe.Analyzer,
		nosharing.Analyzer,
	)
}

// peelBuildTags removes -tags / -tags=... from args (in place) and returns
// the tag list. Space- or comma-separated forms are accepted, matching go build.
func peelBuildTags(args *[]string) (string, bool) {
	if args == nil || len(*args) == 0 {
		return "", false
	}
	in := *args
	out := in[:0:0]
	if cap(out) < len(in) {
		out = make([]string, 0, len(in))
	}
	var tags string
	found := false
	for i := 0; i < len(in); i++ {
		a := in[i]
		switch {
		case a == "-tags" && i+1 < len(in):
			tags = in[i+1]
			found = true
			i++
		case strings.HasPrefix(a, "-tags="):
			tags = strings.TrimPrefix(a, "-tags=")
			found = true
		default:
			out = append(out, a)
		}
	}
	*args = out
	return tags, found
}

func injectGOFLAGSTags(tags string) error {
	tags = strings.Join(strings.Fields(strings.ReplaceAll(tags, ",", " ")), ",")
	if tags == "" {
		return fmt.Errorf("empty -tags value")
	}
	tagFlag := "-tags=" + tags
	gf := strings.TrimSpace(os.Getenv("GOFLAGS"))
	if gf == "" {
		return os.Setenv("GOFLAGS", tagFlag)
	}
	// Drop any existing -tags=... so ours wins.
	parts := strings.Fields(gf)
	kept := parts[:0]
	for _, p := range parts {
		if p == "-tags" || strings.HasPrefix(p, "-tags=") {
			continue
		}
		kept = append(kept, p)
	}
	kept = append(kept, tagFlag)
	return os.Setenv("GOFLAGS", strings.Join(kept, " "))
}
