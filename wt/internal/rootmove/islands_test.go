package rootmove

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// serialIslands is every test of this package that owns process-global state
// while it runs — here, the environment: each value says why the test can
// never call t.Parallel. These are permanently serial (Constraint 2: never
// convert t.Setenv to os.Setenv to force parallelism). Go's release semantics
// keep them sound: a top-level parallel test starts only after the whole
// serial scan completes, and a parallel subtest only after its parent's serial
// scan, so an island (and its tracing rungs) holds exclusive process env while
// it runs. A test listed here stays listed until it stops owning env;
// TestSerialIslandsAreExactlyTheEnvOwningTests holds the list to the facts.
var serialIslands = map[string]string{
	"TestExecuteRecoveryRows":                   "five recovery rungs call traceGit (t.Setenv GIT_TRACE2_EVENT; the trace file is written by every git child)",
	"TestExecuteRecordNotWritableBlocksRelease": "calls traceGit to prove no reset ran (t.Setenv GIT_TRACE2_EVENT)",
	"TestExecuteStartRecheck":                   "two skipped-recheck rungs call traceGit (t.Setenv GIT_TRACE2_EVENT)",
	"TestPreflightNeverFetches":                 `t.Setenv("GIT_TRACE") records every git process Preflight starts`,
}

// envOwningTests returns the top-level `func Test` names of file that reach
// process-global environment: a `t.Setenv(` call at any depth (a hook closure
// counts) or a `traceGit(` call (the one helper that owns
// t.Setenv("GIT_TRACE2_EVENT", ...) on a test's behalf). The scan is name-keyed
// (the losses_guard style, no go/types): a call outside a Test — the helper's
// own body — maps to no test and is not counted, because its callers are (the
// `traceGit(` rule).
func envOwningTests(file *ast.File) map[string]bool {
	out := map[string]bool{}
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil || !strings.HasPrefix(fd.Name.Name, "Test") {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				if fun.Name == "traceGit" {
					out[fd.Name.Name] = true
				}
			case *ast.SelectorExpr:
				if id, ok := fun.X.(*ast.Ident); ok && id.Name == "t" && fun.Sel.Name == "Setenv" {
					out[fd.Name.Name] = true
				}
			}
			return true
		})
	}
	return out
}

// TestSerialIslandsAreExactlyTheEnvOwningTests scans every *_test.go of this
// package for the two env-owning call forms (`t.Setenv(` and `traceGit(`) and
// requires the set of top-level tests enclosing them to equal serialIslands'
// keys exactly. Why: a t.Setenv owner may never call t.Parallel (the runtime
// panics), so these tests are permanently serial islands holding exclusive
// process-global env; a new one added without a registry entry would race its
// parallel siblings' git children through the shared environment (Review
// Focus 5), and a stale entry would keep a parallelizable test serial.
func TestSerialIslandsAreExactlyTheEnvOwningTests(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	got := map[string]bool{}
	parsed := 0
	for _, name := range files {
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		parsed++
		for k := range envOwningTests(file) {
			got[k] = true
		}
	}
	if parsed < 5 {
		t.Fatalf("parsed only %d test files; the glob is not reaching the sources", parsed)
	}
	var bad []string
	for name := range got {
		if _, ok := serialIslands[name]; !ok {
			bad = append(bad, fmt.Sprintf("%s owns env state (a t.Setenv( or traceGit( call) but is not in serialIslands; add it with a one-line reason and keep the test serial", name))
		}
	}
	for name := range serialIslands {
		if !got[name] {
			bad = append(bad, fmt.Sprintf("%s is in serialIslands but no t.Setenv( or traceGit( call maps to it; drop the entry (and t.Parallel() the test)", name))
		}
	}
	slices.Sort(bad)
	for _, b := range bad {
		t.Error(b)
	}
}
