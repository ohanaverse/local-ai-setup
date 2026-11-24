package gitenv

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// singlePExclusions lists packages whose tests wait on a goroutine, a sleep
// or a timer but are deliberately left out of SINGLE_P_PKGS. Each entry needs
// a reason. It is empty: rootmove's git-heavy tests match none of the
// patterns, and cleanup is listed.
var singlePExclusions = map[string]string{}

// singlePList reads the package list from the SINGLE_P_PKGS line of the
// module's Makefile, as directories relative to the module root.
func singlePList(t *testing.T, root string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(line, "SINGLE_P_PKGS :=")
		if !ok {
			continue
		}
		var dirs []string
		for _, f := range strings.Fields(rest) {
			dirs = append(dirs, strings.TrimPrefix(f, "./"))
		}
		if len(dirs) == 0 {
			t.Fatal("the SINGLE_P_PKGS line in Makefile is empty")
		}
		return dirs
	}
	t.Fatal("Makefile has no SINGLE_P_PKGS := line")
	return nil
}

// goroutineWaits returns, per package directory, the file:line of the first
// place a _test.go file there starts a goroutine, calls time.Sleep or
// receives from time.After. The AST is read, so strings and comments never
// match; the time package is matched by selector name.
func goroutineWaits(t *testing.T, root string) map[string]string {
	t.Helper()
	found := map[string]string{}
	pkgs := readPackages(t, root, "") // only the package directories are used
	fset := token.NewFileSet()
	for dir := range pkgs {
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			file, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(dir), e.Name()), nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				hit := false
				switch n := n.(type) {
				case *ast.GoStmt:
					hit = true
				case *ast.CallExpr:
					hit = isTimeSel(n.Fun, "Sleep")
				case *ast.UnaryExpr:
					if call, ok := n.X.(*ast.CallExpr); ok && n.Op == token.ARROW {
						hit = isTimeSel(call.Fun, "After")
					}
				}
				if hit && found[dir] == "" {
					found[dir] = dir + "/" + e.Name() + ":" + strconv.Itoa(fset.Position(n.Pos()).Line)
				}
				return true
			})
		}
	}
	return found
}

func isTimeSel(fun ast.Expr, name string) bool {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "time"
}

// TestEveryGoroutineWaitingPackageIsInTheSingleP list fails for a package
// whose tests start a goroutine, call time.Sleep or receive from time.After
// and that is not in SINGLE_P_PKGS (wt/Makefile), the list `make single-p`
// and bin/go-test-sharded run on GOMAXPROCS=1. A test that waits on a
// goroutine can pass on many Ps and hang on one; the pass only helps if the
// package is in it. .Wait() and sync.Mutex are deliberately not scanned: they
// match nearly every package and say little. The list stays an allow-list:
// one P over ./... would run the whole module on one P.
func TestEveryGoroutineWaitingPackageIsInTheSingleP(t *testing.T) {
	root, _ := moduleRoot(t)
	list := singlePList(t, root)
	found := goroutineWaits(t, root)

	var dirs []string
	for dir := range found {
		dirs = append(dirs, dir)
	}
	slices.Sort(dirs)
	for _, dir := range dirs {
		if slices.Contains(list, dir) {
			continue
		}
		if reason, ok := singlePExclusions[dir]; ok {
			if reason == "" {
				t.Errorf("%s: excluded from the single-P list without a reason", dir)
			}
			continue
		}
		t.Errorf("%s: its tests wait on a goroutine or timer (%s) but it is not in SINGLE_P_PKGS; add ./%s to SINGLE_P_PKGS in wt/Makefile", dir, found[dir], dir)
	}

	// The scan must still see the packages known to wait.
	for _, dir := range []string{"internal/tui", "internal/litellm", "internal/smoke", "cmd/wt-session"} {
		if found[dir] == "" {
			t.Errorf("%s was not found to wait on a goroutine; this test no longer reads the module's source correctly", dir)
		}
	}
	// Every list entry must be a real package with tests (catches stale entries).
	for _, dir := range list {
		matches, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(dir), "*_test.go"))
		if len(matches) == 0 {
			t.Errorf("SINGLE_P_PKGS lists ./%s, which has no _test.go files", dir)
		}
	}
	for dir := range singlePExclusions {
		if found[dir] == "" {
			t.Errorf("%s is excluded but no longer waits on a goroutine; drop the exclusion", dir)
		}
	}
}
