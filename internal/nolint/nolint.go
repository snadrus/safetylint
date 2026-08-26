// Package nolint honors //nolint comments for safetylint analyzers.
package nolint

import (
	"go/ast"
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// Umbrella is the meta-name that suppresses every safetylint analyzer.
const Umbrella = "safetylint"

// Suppressed reports whether a diagnostic at pos should be omitted for
// analyzer (for example "nosharing" or "nounsafe"). The umbrella name
// "safetylint" suppresses every analyzer. Bare //nolint with no names is
// not treat-as-all.
func Suppressed(pass *analysis.Pass, pos token.Pos, analyzer string) bool {
	if pass == nil || pass.Fset == nil || !pos.IsValid() || analyzer == "" {
		return false
	}
	target := pass.Fset.Position(pos)
	if target.Filename == "" || target.Line <= 0 {
		return false
	}
	f := fileFor(pass, target.Filename)
	if f == nil {
		return false
	}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			cl := pass.Fset.Position(c.Pos())
			if cl.Filename != target.Filename {
				continue
			}
			if cl.Line != target.Line && cl.Line != target.Line-1 {
				continue
			}
			if commentHas(c.Text, analyzer) {
				return true
			}
		}
	}
	return false
}

func fileFor(pass *analysis.Pass, filename string) *ast.File {
	for _, f := range pass.Files {
		if pass.Fset.Position(f.Pos()).Filename == filename {
			return f
		}
	}
	return nil
}

func commentHas(text, analyzer string) bool {
	for _, name := range parseNolint(text) {
		if name == analyzer || name == Umbrella {
			return true
		}
	}
	return false
}

// parseNolint returns named linters from a //nolint:a,b comment.
// Bare //nolint (no names) yields nothing.
func parseNolint(text string) []string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "//") {
		return nil
	}
	rest := strings.TrimSpace(text[2:])
	const prefix = "nolint"
	if !strings.HasPrefix(rest, prefix) {
		return nil
	}
	rest = rest[len(prefix):]
	if rest == "" {
		return nil
	}
	if rest[0] != ':' {
		// //nolint // explanation — not suppress-all
		return nil
	}
	rest = rest[1:]
	if i := strings.Index(rest, "//"); i >= 0 {
		rest = rest[:i]
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return nil
	}
	parts := strings.Split(rest, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if i := strings.IndexAny(p, " \t"); i >= 0 {
			p = p[:i]
		}
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
