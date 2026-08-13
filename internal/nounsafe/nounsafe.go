// Package nounsafe reports uses of language escape hatches that break
// Go's memory-safety guarantees even in single-threaded programs.
package nounsafe

import (
	"bytes"
	"go/ast"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"safetylint/internal/toolver"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

const Doc = `report unsafe, cgo, assembly, and pointer-laundering escape hatches

The nounsafe analyzer refuses constructs that exit Go's type-safe memory model:

  - import "unsafe"
  - import "C" (cgo / extern C)
  - .s assembly files in the package
  - //go:linkname and //go:cgo_* compiler directives
  - reflect.Value.UnsafePointer/UnsafeAddr and reflect.SliceHeader/StringHeader

Escape-hatch diagnostics state that the code is not verified: check its
safety and the adapter's safety yourself.
`

var Analyzer = &analysis.Analyzer{
	Name:     "nounsafe",
	Doc:      Doc,
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

func run(pass *analysis.Pass) (any, error) {
	toolver.WarnIfTooNew(pass)

	userFiles := make([]*ast.File, 0, len(pass.Files))
	for _, f := range pass.Files {
		if isCgoGenerated(pass.Fset.Position(f.Pos()).Filename) {
			continue
		}
		userFiles = append(userFiles, f)
	}
	if len(userFiles) == 0 {
		return nil, nil
	}

	// Assembly files are a trust hole equivalent to cgo.
	for _, other := range pass.OtherFiles {
		if strings.HasSuffix(strings.ToLower(other), ".s") {
			pass.Reportf(userFiles[0].Package, "assembly file %q is not verified. Check its safety and the adapter's safety yourself", filepath.Base(other))
			break
		}
	}

	// Detect cgo: generated files present, or source still contains import "C".
	usesCgo := false
	for _, f := range pass.Files {
		if isCgoGenerated(pass.Fset.Position(f.Pos()).Filename) {
			usesCgo = true
			break
		}
	}
	for _, f := range userFiles {
		filename := pass.Fset.Position(f.Pos()).Filename
		if fileContainsImportC(filename) {
			usesCgo = true
			break
		}
	}
	if usesCgo {
		pass.Reportf(userFiles[0].Package, `import "C" (cgo/extern C) is not verified. Check its safety and the adapter's safety yourself`)
	}

	for _, f := range userFiles {
		filename := pass.Fset.Position(f.Pos()).Filename
		cgoSource := fileContainsImportC(filename)
		checkDirectives(pass, f)
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			switch path {
			case "unsafe":
				// cgo rewrites import "C" files to also pull in unsafe; the
				// package-level cgo diagnostic already covers that.
				if cgoSource {
					continue
				}
				pass.Reportf(imp.Pos(), `import "unsafe" is not verified. Check its safety and the adapter's safety yourself`)
			case "C":
				// Already reported at package level when usesCgo; if the AST
				// still has import "C", report on the import itself instead
				// only when we didn't already fire the package diagnostic.
				// Package diagnostic is preferred for position stability.
			}
		}
	}

	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	nodeFilter := []ast.Node{
		(*ast.SelectorExpr)(nil),
		(*ast.Ident)(nil),
	}
	insp.Nodes(nodeFilter, func(n ast.Node, push bool) bool {
		if !push {
			return false
		}
		filename := pass.Fset.Position(n.Pos()).Filename
		if isCgoGenerated(filename) {
			return false
		}
		switch n := n.(type) {
		case *ast.SelectorExpr:
			checkReflectObj(pass, n.Sel)
			return false
		case *ast.Ident:
			checkReflectObj(pass, n)
		}
		return true
	})

	return nil, nil
}

func isCgoGenerated(filename string) bool {
	base := filepath.Base(filename)
	switch {
	case strings.HasPrefix(base, "_cgo_"):
		return true
	case strings.Contains(base, ".cgo1."), strings.Contains(base, ".cgo2."):
		return true
	case strings.Contains(filename, string(filepath.Separator)+"go-build"+string(filepath.Separator)):
		return true
	}
	return false
}

func fileContainsImportC(filename string) bool {
	data, err := os.ReadFile(filename)
	if err != nil {
		return false
	}
	return bytes.Contains(data, []byte(`import "C"`)) ||
		bytes.Contains(data, []byte("import `C`"))
}

func checkDirectives(pass *analysis.Pass, f *ast.File) {
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			text := strings.TrimSpace(c.Text)
			if !strings.HasPrefix(text, "//go:") {
				continue
			}
			dir := strings.TrimPrefix(text, "//go:")
			name := dir
			if i := strings.IndexAny(dir, " \t"); i >= 0 {
				name = dir[:i]
			}
			switch {
			case name == "linkname":
				pass.Reportf(c.Pos(), "//go:linkname is not verified. Check its safety and the adapter's safety yourself")
			case strings.HasPrefix(name, "cgo_"):
				pass.Reportf(c.Pos(), "//go:%s is not verified. Check its safety and the adapter's safety yourself", name)
			}
		}
	}
}

func checkReflectObj(pass *analysis.Pass, id *ast.Ident) {
	obj := pass.TypesInfo.Uses[id]
	if obj == nil {
		return
	}
	pkg := obj.Pkg()
	if pkg == nil || pkg.Path() != "reflect" {
		return
	}
	switch obj.Name() {
	case "UnsafePointer", "UnsafeAddr", "SliceHeader", "StringHeader":
		pass.Reportf(id.Pos(), "reflect.%s is not verified. Check its safety and the adapter's safety yourself", obj.Name())
	}
}
