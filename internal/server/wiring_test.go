package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every exported field on a page's view model reaches a template or is used in
// Go. A field computed on every request and shown nowhere is either a missing
// part of a page or dead code, and both are defects.
//
// Eleven had accumulated before this guard existed. One of them was a whole
// type — loopRow, declared with a Health field and referenced by nothing. Two
// were facts about whether the money figures could be trusted, computed on
// every request and printed on no page. Nothing failed, nothing was logged, and
// the only way to find them was to go looking.
//
// The check is deliberately loose about HOW a field is used: read in Go is
// fine, because a field can be an input to a headline rather than a cell in a
// table. What it refuses is a field that is written and never read at all.
func TestEveryViewFieldReachesAPage(t *testing.T) {
	const dir = "."
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Everything the package contains, templates included: they are Go string
	// constants in this package, so one read covers both.
	var all strings.Builder
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, rerr := os.ReadFile(f)
		if rerr != nil {
			t.Fatal(rerr)
		}
		all.Write(b)
	}
	src := all.String()
	if len(src) == 0 {
		t.Fatal("read no source at all, so this guard would pass vacuously")
	}

	checked := 0
	for _, p := range pkgs {
		for _, f := range p.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok {
					return true
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					return true
				}
				name := strings.ToLower(ts.Name.Name)
				// Only the types a page renders. A payload or a helper is not
				// a view model and is not this guard's business.
				if !strings.Contains(name, "view") && !strings.Contains(name, "row") &&
					!strings.Contains(name, "line") {
					return true
				}
				for _, fl := range st.Fields.List {
					for _, id := range fl.Names {
						if !id.IsExported() {
							continue
						}
						checked++
						// Two references: the declaration itself, and at least
						// one use. One means it is written and never read.
						if strings.Count(src, "."+id.Name) == 0 {
							t.Errorf("%s.%s is computed and reaches no page and no code",
								ts.Name.Name, id.Name)
						}
					}
				}
				return true
			})
		}
	}
	if checked == 0 {
		t.Fatal("found no view fields to check, so this guard would pass vacuously")
	}
}
