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

// TestModelStopsRunOnlyAfterTheAltScreen pins where the TUI stops models
// (#379): only in the post-exit stop phase, once p.Run() has returned and the
// alt screen is gone. A stop can now wait up to three minutes for ollama and
// print a notice while it waits ("waiting for ollama to finish a request in
// flight"); run from inside the program, that line would be printed onto the
// picker's alt screen — the bug #381's review caught for the proxy restart's
// notice. So the stop loop's entry points are called from realRunStopPhase
// alone, runStopPhase from printPendingSummaryAndSurvey alone, and Run calls
// that only after p.Run(). A new stop from a screen must route its notice into
// the screen (lifecycle.WithStopNotice) and change this test on purpose.
func TestModelStopsRunOnlyAfterTheAltScreen(t *testing.T) {
	stopCalls := map[string]bool{
		"survey.Picker": true, "survey.PickerWith": true, "survey.StopEntries": true,
		"lifecycle.BeginStops": true,
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	callers := map[string][]string{} // callee -> enclosing functions
	var runOrder []string            // in Run: the calls of interest, in source order
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
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := ""
				switch c := call.Fun.(type) {
				case *ast.Ident:
					name = c.Name
				case *ast.SelectorExpr:
					if x, ok := c.X.(*ast.Ident); ok {
						name = x.Name + "." + c.Sel.Name
					}
				}
				if stopCalls[name] || name == "runStopPhase" {
					callers[name] = append(callers[name], fn.Name.Name)
				}
				if fn.Name.Name == "Run" && fn.Recv == nil && (name == "p.Run" || name == "printPendingSummaryAndSurvey") {
					runOrder = append(runOrder, name)
				}
				return true
			})
		}
	}
	for callee, fns := range callers {
		want := "realRunStopPhase"
		if callee == "runStopPhase" {
			want = "printPendingSummaryAndSurvey"
		}
		for _, fn := range fns {
			if fn != want {
				t.Errorf("%s is called from %s, want only from %s: a stop run while the alt screen is up prints its wait notice onto it", callee, fn, want)
			}
		}
	}
	if len(callers["survey.Picker"]) == 0 || len(callers["runStopPhase"]) == 0 {
		t.Fatalf("callers = %v: the post-exit stop phase was not found, so this test checks nothing", callers)
	}
	if strings.Join(runOrder, ",") != "p.Run,printPendingSummaryAndSurvey" {
		t.Errorf("Run calls %v, want p.Run before printPendingSummaryAndSurvey: the stop phase runs once the alt screen is gone", runOrder)
	}
}
