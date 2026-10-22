package gitenv

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// pkgFacts is what TestEveryPackageThatRunsGitIsolatesIt reads out of one
// package directory's source files.
type pkgFacts struct {
	prodImports []string // module packages the non-test files import, as directories relative to the module root
	testImports []string // the same for the _test.go files
	prodGit     string   // file:line of a "git" literal in a non-test file, or ""
	testGit     string   // the same for a _test.go file
	hasTests    bool
	isolates    bool // a TestMain here calls IsolateForTest
}

// moduleRoot returns the directory that holds go.mod, found by walking up
// from the working directory (a test runs in its package's directory), and
// the module path declared there.
func moduleRoot(t *testing.T) (dir, modPath string) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
					return dir, strings.TrimSpace(rest)
				}
			}
			t.Fatalf("%s/go.mod has no module line", dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

// namesGit reports whether a string literal's value is the program name git
// or a command line that starts with it.
func namesGit(value string) bool {
	return value == "git" || strings.HasPrefix(value, "git ")
}

// readPackages parses every .go file below root, whatever its build
// constraints, and returns the facts per package directory (relative to root,
// slash-separated). Directories the go tool ignores are skipped.
func readPackages(t *testing.T, root, modPath string) map[string]*pkgFacts {
	t.Helper()
	pkgs := map[string]*pkgFacts{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		facts := pkgs[rel]
		if facts == nil {
			facts = &pkgFacts{}
			pkgs[rel] = facts
		}
		isTest := strings.HasSuffix(name, "_test.go")
		facts.hasTests = facts.hasTests || isTest

		// The name this file calls the gitenv package by: its import name,
		// or none at all inside the package itself.
		gitenvName, inGitenv := "", rel == "internal/gitenv" && file.Name.Name == "gitenv"
		for _, imp := range file.Imports {
			importPath, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			dir, ok := strings.CutPrefix(importPath, modPath+"/")
			if !ok {
				continue
			}
			if isTest {
				facts.testImports = append(facts.testImports, dir)
			} else {
				facts.prodImports = append(facts.prodImports, dir)
			}
			if dir == "internal/gitenv" {
				gitenvName = "gitenv"
				if imp.Name != nil {
					gitenvName = imp.Name.Name
				}
			}
		}

		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil || !namesGit(value) {
				return true
			}
			at := fset.Position(lit.Pos())
			where := filepath.ToSlash(filepath.Join(rel, filepath.Base(at.Filename))) + ":" + strconv.Itoa(at.Line)
			if isTest && facts.testGit == "" {
				facts.testGit = where
			}
			if !isTest && facts.prodGit == "" {
				facts.prodGit = where
			}
			return true
		})

		if !isTest {
			return nil
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != "TestMain" || fn.Body == nil {
				continue
			}
			// A statement of TestMain's own body, so the call is not behind
			// a condition or inside a function TestMain may never run.
			for _, stmt := range fn.Body.List {
				expr, ok := stmt.(*ast.ExprStmt)
				if !ok {
					continue
				}
				call, ok := expr.X.(*ast.CallExpr)
				if !ok {
					continue
				}
				switch fun := call.Fun.(type) {
				case *ast.Ident:
					facts.isolates = facts.isolates || (inGitenv && fun.Name == "IsolateForTest")
				case *ast.SelectorExpr:
					pkg, ok := fun.X.(*ast.Ident)
					facts.isolates = facts.isolates || (ok && gitenvName != "" && pkg.Name == gitenvName && fun.Sel.Name == "IsolateForTest")
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return pkgs
}

// gitReason says why the test binary of the package in dir can run git, or ""
// when nothing found says it can: a file of the package names git, or a
// package it links, through any chain of imports, names git in a non-test
// file. (Another package's test files are not linked.)
func gitReason(pkgs map[string]*pkgFacts, dir string) string {
	facts := pkgs[dir]
	if facts.prodGit != "" {
		return "it names git at " + facts.prodGit
	}
	if facts.testGit != "" {
		return "its tests name git at " + facts.testGit
	}
	seen := map[string]bool{dir: true}
	queue := slices.Concat(facts.prodImports, facts.testImports)
	for len(queue) > 0 {
		dep := queue[0]
		queue = queue[1:]
		if seen[dep] || pkgs[dep] == nil {
			continue
		}
		seen[dep] = true
		if at := pkgs[dep].prodGit; at != "" {
			return "it links " + dep + ", which names git at " + at
		}
		queue = append(queue, pkgs[dep].prodImports...)
	}
	return ""
}

// TestEveryPackageThatRunsGitIsolatesIt reads the module's source and fails
// for a package whose test binary can run git and whose TestMain does not
// call IsolateForTest. Without the call, a suite run from a git hook
// (GIT_DIR exported) or with TMPDIR or GOTMPDIR inside a checkout runs
// `git init`, `git commit`, `git worktree add` and the commit-hook install
// against the developer's repository; a new package, or a new import of one
// that runs git, would otherwise bring that back without anyone noticing.
//
// "Can run git" is decided from the source, so it is wider than "does": a
// package counts when one of its files holds the string literal "git" (or a
// command line starting with it), or when it links a package whose non-test
// files do, however far down the imports. What this cannot see is git started
// under a name that is not written out: through a variable, a shell script, or
// a child process that runs git itself.
func TestEveryPackageThatRunsGitIsolatesIt(t *testing.T) {
	root, modPath := moduleRoot(t)
	pkgs := readPackages(t, root, modPath)

	var dirs, need []string
	for dir := range pkgs {
		dirs = append(dirs, dir)
	}
	slices.Sort(dirs)
	for _, dir := range dirs {
		facts := pkgs[dir]
		if !facts.hasTests {
			continue
		}
		reason := gitReason(pkgs, dir)
		if reason == "" {
			continue
		}
		need = append(need, dir)
		if !facts.isolates {
			t.Errorf("%s: its tests can run git (%s), and no TestMain there calls gitenv.IsolateForTest() as a statement of its own", dir, reason)
		}
	}

	// The packages known to run git must be among those found, or the reading
	// above has stopped seeing anything and the loop proves nothing.
	for _, dir := range []string{"cmd/wt", "internal/guard", "internal/worktree", "internal/smoke", "internal/tui", "internal/gitenv"} {
		if !slices.Contains(need, dir) {
			t.Errorf("%s was not found to run git; this test no longer reads the module's source correctly", dir)
		}
	}
	t.Logf("packages whose tests can run git: %s", strings.Join(need, ", "))
}
