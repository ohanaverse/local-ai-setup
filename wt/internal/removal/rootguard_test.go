package removal

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// rootMutationAllowed lists, by "<file>#<function>", the only functions in
// internal/removal's non-test files that may hold a root-mutating shape: the
// argv words "reset", "--hard" or "switch", or a file-deleting call
// (os.Remove, os.RemoveAll, os.Rename, syscall/unix Unlink...). Every one of
// them is ReleaseRoot or a helper only it calls, behind its three proofs. A
// new entry needs the same justification: resetting, switching or deleting
// files in the user's root belongs behind ReleaseRoot's gates.
var rootMutationAllowed = map[string]string{
	"root.go#ReleaseRoot":   "the reset (`reset --hard HEAD`) and the switch to the default branch, after the three proofs",
	"root.go#removeCarried": "os.Remove of exactly the untracked paths the verified copy carried",
}

// rootMutationArgs are the argv words that reset, switch or otherwise rewrite
// a checkout's files (checkout, restore and stash discard or move work too).
var rootMutationArgs = []string{"reset", "--hard", "switch", "checkout", "restore", "stash"}

// rootMutations returns, per function of file ("<base>#<Recv.>Name"), the
// root-mutating shapes it holds: an argv word of rootMutationArgs among a
// call's or slice literal's fixed strings (read the way the deletion scan
// reads them: literals, constants, append(...) and nested literals), or a
// reference to a file-deleting function of os, syscall or x/sys/unix. Code
// outside any function (a package-level var) is keyed "<base>#".
func rootMutations(fset *token.FileSet, base string, file *ast.File) map[string][]string {
	out := map[string][]string{}
	pkgName := map[string]string{}
	for _, imp := range file.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		if !slices.Contains(fileOpPkgs, path) {
			continue
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		pkgName[name] = path
	}
	r := newArgReader(file)
	inspect := func(key string, n ast.Node) {
		at := func(n ast.Node) string { return strconv.Itoa(fset.Position(n.Pos()).Line) }
		hit := func(n ast.Node, args []string) {
			for _, a := range args {
				if slices.Contains(rootMutationArgs, a) {
					out[key] = append(out[key], "line "+at(n)+": "+strconv.Quote(a))
				}
			}
		}
		ast.Inspect(n, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				if !r.consumed[n] {
					hit(n, r.args(n.Args))
				}
			case *ast.CompositeLit:
				if !r.consumed[n] {
					hit(n, r.args(n.Elts))
				}
			case *ast.SelectorExpr:
				if id, ok := n.X.(*ast.Ident); ok {
					if path, ok := pkgName[id.Name]; ok && fileOp(path, n.Sel.Name) {
						out[key] = append(out[key], "line "+at(n)+": "+path+"."+n.Sel.Name)
					}
				}
			}
			return true
		})
	}
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok {
			inspect(base+"#", d)
			continue
		}
		name := fd.Name.Name
		if fd.Recv != nil && len(fd.Recv.List) == 1 {
			t := fd.Recv.List[0].Type
			if s, ok := t.(*ast.StarExpr); ok {
				t = s.X
			}
			if id, ok := t.(*ast.Ident); ok {
				name = id.Name + "." + name
			}
		}
		inspect(base+"#"+name, fd)
	}
	return out
}

// TestRootMutationFunctionsAreExactlyTheAllowList parses every non-test .go
// file of internal/removal and requires the functions holding a reset, a
// switch or a file deletion to be exactly rootMutationAllowed. Why: the
// root's reset is the one data-destroying step of `wt restore-root`, and its
// safety is the three proofs ReleaseRoot checks first (verified copy, record,
// unchanged fingerprint). A second function that resets, switches or deletes
// files would bypass them; a stale entry would let one in later unseen.
func TestRootMutationFunctionsAreExactlyTheAllowList(t *testing.T) {
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	got := map[string][]string{}
	scanned := 0
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		for k, v := range rootMutations(fset, name, file) {
			got[k] = v
		}
	}
	if scanned < 5 {
		t.Fatalf("scanned only %d files; the glob no longer reads the package", scanned)
	}
	var keys []string
	for k := range got {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if _, ok := rootMutationAllowed[k]; !ok {
			t.Errorf("%s resets, switches or deletes files (%s) but is not on rootMutationAllowed; the root may be changed only behind ReleaseRoot's proofs",
				k, strings.Join(got[k], "; "))
		}
	}
	for k := range rootMutationAllowed {
		if len(got[k]) == 0 {
			t.Errorf("rootMutationAllowed names %s, which no longer resets, switches or deletes a file: drop the stale entry", k)
		}
	}
}

// TestRootMutationScanSeesEachShape feeds rootMutations one function per
// shape, and harmless look-alikes, so the allow-list check above cannot pass
// because the scan stopped seeing a reset, a switch or a file deletion.
func TestRootMutationScanSeesEachShape(t *testing.T) {
	cases := []struct {
		body string
		hit  bool
	}{
		{`runGit(g, "reset", "--hard", "HEAD")`, true},
		{`runGit(g, "-c", "core.hooksPath=/dev/null", "switch", d)`, true},
		{`g.Run(append([]string{"re" + "set"}, x)...)`, true},
		{`const s = "switch"; g.Run(s, d)`, true},
		{`os.Remove(p)`, true},
		{`f := os.RemoveAll; _ = f`, true},
		{`syscall.Unlink(p)`, true},
		{`runGit(g, "checkout", "-f", d)`, true},
		{`runGit(g, "stash", "-u")`, true},
		{`switch x { case 1: }`, false},
		{`g.Run("status", "--porcelain=v2")`, false},
		{`fmt.Println("the root is reset and switched")`, false},
	}
	for _, c := range cases {
		src := "package p\nimport (\"os\"; \"syscall\"; \"fmt\")\nvar _ = os.Getpid\nvar _ = syscall.Getpid\nvar _ = fmt.Sprint\nfunc f() {\n" + c.body + "\n}\n"
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "x.go", src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("%s: %v", c.body, err)
		}
		got := rootMutations(fset, "x.go", file)
		if hit := len(got["x.go#f"]) > 0; hit != c.hit {
			t.Errorf("%s: hit=%v (%v), want %v", c.body, hit, got, c.hit)
		}
	}
}

// hardResetWords are the argv words no non-test file outside internal/removal
// may hold: a hard reset, a clean (`"clean"` also catches `clean -x`), and
// checkout, restore and stash, which discard or move uncommitted work too.
// None is used by any non-test source outside internal/removal.
var hardResetWords = []string{"--hard", "reset", "clean", "checkout", "restore", "stash"}

// hardResetLine matches a reset or clean written as one command-line literal
// ("git reset --hard", "git clean -fdx").
var hardResetLine = regexp.MustCompile(`\breset\s+--hard\b|\bgit\s+(reset|clean)\b|\bclean\s+-[A-Za-z]*[fdxX]`)

// hardResetExempt names, by path relative to the module root, files outside
// internal/removal allowed to hold such a word, each with why. It is empty:
// when the guard was written (Task 5 of the restore-root plan) a grep of the
// module's non-test sources found no "--hard", "reset" or "clean" literal
// outside internal/removal. An entry needs a controller ruling naming the
// file; the source is never edited to dodge the scan.
var hardResetExempt = map[string]string{}

// hardResetHits returns the hard-reset shapes in one parsed file: an argv word
// of hardResetWords among a call's or slice literal's fixed strings, or a
// string literal holding a reset/clean command line.
func hardResetHits(fset *token.FileSet, rel string, file *ast.File) []finding {
	var hits []finding
	at := func(n ast.Node) string { return rel + ":" + strconv.Itoa(fset.Position(n.Pos()).Line) }
	r := newArgReader(file)
	check := func(n ast.Node, args []string) {
		for _, a := range args {
			if slices.Contains(hardResetWords, a) {
				hits = append(hits, finding{at(n), strconv.Quote(a)})
			}
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			if !r.consumed[n] {
				check(n, r.args(n.Args))
			}
		case *ast.CompositeLit:
			if !r.consumed[n] {
				check(n, r.args(n.Elts))
			}
		case *ast.BasicLit:
			if v, ok := r.constant(n); ok && hardResetLine.MatchString(v) {
				hits = append(hits, finding{at(n), "command line " + strconv.Quote(v)})
			}
		}
		return true
	})
	return hits
}

// TestHardResetExistsNowhereOutsideRemoval reads every non-test .go file of
// the wt module outside internal/removal and fails on a hardResetWords argv
// word ("--hard", "reset", "clean", "checkout", "restore", "stash"), or a
// reset/clean command-line literal. Why: a hard reset
// destroys uncommitted work, and the only one wt may run is ReleaseRoot's,
// behind its three proofs; a reset or clean anywhere else (rootmove, a
// command, a helper) would destroy the root's work with no proof at all.
// Exemptions are by file name only (hardResetExempt) and it is empty.
func TestHardResetExistsNowhereOutsideRemoval(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root %s has no go.mod: %v", root, err)
	}
	fset := token.NewFileSet()
	scanned := 0
	hitFiles := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "vendor") {
				return filepath.SkipDir
			}
			if rel == "internal/removal" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		scanned++
		hits := hardResetHits(fset, rel, file)
		if len(hits) > 0 {
			hitFiles[rel] = true
		}
		if _, ok := hardResetExempt[rel]; ok {
			return nil
		}
		for _, h := range hits {
			t.Errorf("%s: %s outside internal/removal (the root's reset belongs in removal.ReleaseRoot, behind its proofs)", h.at, h.token)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 100 {
		t.Fatalf("scanned only %d files; the walk no longer reads the module", scanned)
	}
	for rel := range hardResetExempt {
		if !hitFiles[rel] {
			t.Errorf("hardResetExempt names %s, which holds no such word any more (or is gone): drop the stale entry", rel)
		}
	}
}

// TestHardResetScanSeesEachShape feeds hardResetHits one snippet per shape,
// and harmless look-alikes, so the module scan above cannot pass because it
// stopped recognising a reset or a clean.
func TestHardResetScanSeesEachShape(t *testing.T) {
	cases := []struct {
		src string
		hit bool
	}{
		{`g.Run("reset", "--hard", "HEAD")`, true},
		{`g.Run("-c", "x=y", "reset", "HEAD")`, true},
		{`g.Run("clean", "-fdx")`, true},
		{`g.Run("checkout", "--", p)`, true},
		{`g.Run("restore", "--staged", p)`, true},
		{`g.Run("stash", "push")`, true},
		{`_ = []string{"re" + "set", "--hard"}`, true},
		{`exec.Command("sh", "-c", "git reset --hard")`, true},
		{`exec.Command("sh", "-c", "git clean -fdx")`, true},
		{`fmt.Println("the root is clean")`, false},
		{`g.Run("status", "--porcelain=v2")`, false},
		{`fmt.Println("reset the timer")`, false},
	}
	for _, c := range cases {
		src := "package p\nfunc f() {\n" + c.src + "\n}\n"
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "x.go", src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("%s: %v", c.src, err)
		}
		if hit := len(hardResetHits(fset, "x.go", file)) > 0; hit != c.hit {
			t.Errorf("%s: hit=%v, want %v", c.src, hit, c.hit)
		}
	}
}
