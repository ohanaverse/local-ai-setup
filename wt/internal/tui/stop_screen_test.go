package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTUIStopsNoModel pins that the picker stops no model itself (#379,
// Task 19): the post-exit stop phase is gone, and the model server stops a
// model after its idle grace. A stop from inside the program would also print
// its wait notice ("waiting for ollama to finish a request in flight") onto
// the picker's alt screen, the bug #381's review caught for the proxy
// restart's notice. So no file of this package calls a stop entry point; a
// new one must route its notice into the screen and change this test on
// purpose.
func TestTUIStopsNoModel(t *testing.T) {
	stopCalls := map[string]bool{
		"hoststop.StopEntries": true,
		"lifecycle.BeginStops": true, "lifecycle.Stop": true,
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && stopCalls[x.Name+"."+sel.Sel.Name] {
				t.Errorf("%s: %s.%s: the picker stops a model", fset.Position(sel.Pos()), x.Name, sel.Sel.Name)
			}
			return true
		})
	}
}
