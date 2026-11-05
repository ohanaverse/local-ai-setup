package removal

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

// fileOpAllowed lists, by path relative to the module root, the pre-existing
// non-test files that may call a file-deleting or file-replacing function
// (os.Remove, os.RemoveAll, os.Rename, syscall.Unlink/Rmdir). Every one of
// them works on wt's own state files; none deletes a user's worktree, branch
// or ref. A new entry needs the same justification and a reviewer's nod:
// removing user data belongs in internal/removal.
var fileOpAllowed = map[string]string{
	"cmd/wt/cloudsync_catalog.go":           "saveFailedHTML: removes a same-named stale page in TMPDIR before writing a refused pricing page (O_EXCL)",
	"internal/config/config.go":             "atomic config write: removes its own temp file, renames it over the target",
	"internal/config/fortest.go":            "test helper's scratch config dir (RemoveAll of a temp dir it made)",
	"internal/guard/guard.go":               "guard uninstall: removes the block-main-commit pre-commit hook wt installed",
	"internal/lifecycle/pidproc.go":         "removes a process server's pidfile and start record",
	"internal/lifecycle/server_loading.go":  "pidfile and .wt start record: atomic write (temp + rename) and removal once the process is gone",
	"internal/litellm/configfile.go":        "atomic write of LiteLLM's config.yaml: temp file removed on failure, renamed over the target",
	"internal/profiles/configcontent.go":    "restores an agent config file a profile's config_content replaced and removes its own backups",
	"internal/rotation/rotation.go":         "removes legacy rotation-*.state files after migrating them into rotation.state",
	"internal/sessionclaim/sessionclaim.go": "removes this process's session claim and claims of dead pids under wt-sessions/",
	"internal/slotedit/apply.go":            "slot-profiles.toml edit: removes its own dated backup / temp file when a write fails",
	"internal/smoke/smoke.go":               "removes the temporary git repos and dirs wt smoke made for its rows",
	"internal/themes/load.go":               "themes.Unset removes wt's own themes.toml",
}

// fileOpNever are files no allow-list entry may cover: the read-only cleanup
// classifier and its command (TestCleanupCmdContainsNoDeletion holds the same
// line with a substring scan).
func fileOpNever(rel string) bool {
	return strings.HasPrefix(rel, "internal/cleanup/") ||
		(strings.HasPrefix(rel, "cmd/wt/cleanup") && !strings.Contains(strings.TrimPrefix(rel, "cmd/wt/"), "/"))
}

// fileOps are the package-qualified functions that delete or replace a file.
var fileOps = map[string][]string{
	"os":      {"Remove", "RemoveAll", "Rename"},
	"syscall": {"Unlink", "Unlinkat", "Rmdir"},
}

// finding is one banned use: where, and which token.
type finding struct {
	at    string // rel:line
	token string
}

// literalArgs returns the string literals among exprs, in order (non-literal
// arguments are skipped, so `"branch", name, "-D"` still reads as branch, -D).
func literalArgs(exprs []ast.Expr) []string {
	var out []string
	for _, e := range exprs {
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		if v, err := strconv.Unquote(lit.Value); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// gitDeletion names the banned git/gh argument shape in one argument list, or
// "" when there is none. args are the list's string literals in order.
func gitDeletion(args []string) string {
	has := func(s string) bool { return slices.Contains(args, s) }
	after := func(a string, bs ...string) bool {
		i := slices.Index(args, a)
		if i < 0 {
			return false
		}
		for _, b := range bs {
			if slices.Contains(args[i+1:], b) {
				return true
			}
		}
		return false
	}
	switch {
	case after("worktree", "remove"):
		return `"worktree", "remove"`
	case after("update-ref", "-d", "--delete"):
		return `"update-ref", "-d"`
	case after("branch", "-d", "-D"):
		return `"branch", "-d"/"-D"`
	case has("--delete"):
		return `"--delete"`
	case has("push") && slices.ContainsFunc(args, func(s string) bool { return strings.HasPrefix(s, ":") }):
		return `push refspec ":<branch>"`
	case has("api") && (after("-X", "DELETE") || after("--method", "DELETE") || has("-XDELETE") || has("--method=DELETE")):
		return "gh api -X DELETE"
	}
	return ""
}

// lineDeletion names a banned command line written as one string literal
// ("git worktree remove x" for a shell), or "".
func lineDeletion(s string) string {
	for _, bad := range []string{"worktree remove", "update-ref -d", "branch -d ", "branch -D ", "push --delete", "-X DELETE"} {
		if strings.Contains(s, bad) {
			return bad
		}
	}
	return ""
}

// scanFile returns the banned git/gh shapes and the file-op calls in one
// parsed file.
func scanFile(fset *token.FileSet, rel string, file *ast.File) (gitHits, fileHits []finding) {
	// The names this file imports os and syscall under.
	pkgName := map[string]string{} // local name -> import path
	for _, imp := range file.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		if _, ok := fileOps[path]; !ok {
			continue
		}
		name := path
		if imp.Name != nil {
			name = imp.Name.Name
		}
		pkgName[name] = path
	}
	at := func(n ast.Node) string { return rel + ":" + strconv.Itoa(fset.Position(n.Pos()).Line) }
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			if tok := gitDeletion(literalArgs(n.Args)); tok != "" {
				gitHits = append(gitHits, finding{at(n), tok})
			}
		case *ast.CompositeLit:
			if tok := gitDeletion(literalArgs(n.Elts)); tok != "" {
				gitHits = append(gitHits, finding{at(n), tok})
			}
		case *ast.BasicLit:
			if n.Kind == token.STRING {
				if v, err := strconv.Unquote(n.Value); err == nil {
					if tok := lineDeletion(v); tok != "" {
						gitHits = append(gitHits, finding{at(n), tok})
					}
				}
			}
		case *ast.SelectorExpr:
			// Any reference, called or not (f := os.Remove counts too).
			id, ok := n.X.(*ast.Ident)
			if !ok {
				return true
			}
			if path, ok := pkgName[id.Name]; ok && slices.Contains(fileOps[path], n.Sel.Name) {
				fileHits = append(fileHits, finding{at(n), path + "." + n.Sel.Name})
			}
		}
		return true
	})
	return gitHits, fileHits
}

// scanModule parses every non-test .go file under root (directories the go
// tool ignores are skipped; build constraints are ignored, so every platform's
// file is read) except those under internal/removal, and returns the findings.
func scanModule(t *testing.T, root string) (gitHits []finding, fileHits map[string][]finding, scanned int) {
	t.Helper()
	fileHits = map[string][]finding{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
		g, f := scanFile(fset, rel, file)
		gitHits = append(gitHits, g...)
		if len(f) > 0 {
			fileHits[rel] = f
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return gitHits, fileHits, scanned
}

// TestDeletionStringsOnlyInRemovalPackage reads every non-test .go file of
// the wt module outside internal/removal and fails on deletion code there:
// internal/removal is the one package allowed to remove a worktree, delete a
// branch or ref, or delete a remote branch (plan "Global Constraints"), so a
// deletion slipping in elsewhere would bypass its rescan, gates and CAS.
//
// It parses the source (go/ast), so a comment or a doc mentioning
// "worktree remove" does not trip it. It fails for:
//   - a call's arguments or a slice literal's elements whose string literals
//     hold, in order: "worktree" ... "remove"; "update-ref" ... "-d";
//     "branch" ... "-d"/"-D"; "--delete" anywhere; "push" with a refspec
//     starting ":" (deleting a remote branch); gh "api" with -X/--method
//     DELETE;
//   - one string literal holding such a command line ("git worktree remove");
//   - os.Remove/RemoveAll/Rename or syscall.Unlink/Unlinkat/Rmdir in a file
//     not on fileOpAllowed, and ever in internal/cleanup or cmd/wt/cleanup*.go;
//   - an allow-list entry whose file is gone or no longer uses such a
//     function (a stale entry would let a later deletion in unseen).
//
// What it cannot see: arguments built at runtime (strings joined, a variable
// holding "remove", an argv read from config), git or gh run through a shell
// script or another binary, deletion through a package other than os and
// syscall (golang.org/x/sys/unix, exec of rm), and test files.
func TestDeletionStringsOnlyInRemovalPackage(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root %s has no go.mod: %v", root, err)
	}
	gitHits, fileHits, scanned := scanModule(t, root)
	if scanned < 100 {
		t.Fatalf("scanned only %d files; the walk no longer reads the module", scanned)
	}
	for _, h := range gitHits {
		t.Errorf("%s: %s outside internal/removal (deleting worktrees, branches or refs belongs there)", h.at, h.token)
	}
	var files []string
	for rel := range fileHits {
		files = append(files, rel)
	}
	slices.Sort(files)
	for _, rel := range files {
		if _, ok := fileOpAllowed[rel]; ok && !fileOpNever(rel) {
			continue
		}
		for _, h := range fileHits[rel] {
			t.Errorf("%s: %s in a file not on fileOpAllowed; deleting user data belongs in internal/removal (wt's own state files: add the file with a reason)", h.at, h.token)
		}
	}
	for rel := range fileOpAllowed {
		if fileOpNever(rel) {
			t.Errorf("fileOpAllowed names %s, which must never delete anything", rel)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("fileOpAllowed names %s, which is gone (%v): drop the stale entry", rel, err)
			continue
		}
		if len(fileHits[rel]) == 0 {
			t.Errorf("fileOpAllowed names %s, which no longer removes or renames a file: drop the stale entry", rel)
		}
	}
}

// TestDeletionScanSeesEachShape feeds scanFile one snippet per banned shape,
// and harmless look-alikes, so the module scan above cannot pass because it
// stopped recognising something.
func TestDeletionScanSeesEachShape(t *testing.T) {
	cases := []struct {
		src       string
		git, file bool
	}{
		{`exec.Command("git", "-C", d, "worktree", "remove", "--force", p)`, true, false},
		{`g.Run("update-ref", "-d", ref, old)`, true, false},
		{`g.Run("branch", name, "-D")`, true, false},
		{`g.Run("branch", "-d", name)`, true, false},
		{`g.Run("push", "origin", "--delete", b)`, true, false},
		{`g.Run("push", "origin", ":topic")`, true, false},
		{`exec.Command("gh", "api", "-X", "DELETE", u)`, true, false},
		{`exec.Command("gh", "api", "--method", "DELETE", u)`, true, false},
		{`_ = []string{"worktree", "remove", p}`, true, false},
		{`exec.Command("sh", "-c", "git worktree remove x")`, true, false},
		{`os.RemoveAll(p)`, false, true},
		{`f := os.Remove; _ = f`, false, true},
		{`syscall.Unlink(p)`, false, true},
		// Look-alikes that must pass.
		{`g.Run("worktree", "list", "--porcelain")`, false, false},
		{`g.Run("branch", "--list", "-v")`, false, false},
		{`g.Run("push", "-u", "origin", "main")`, false, false},
		{`exec.Command("gh", "api", "repos/o/r")`, false, false},
		{`fmt.Println("a:b")`, false, false},
		{`// git worktree remove in a comment
		_ = 1`, false, false},
	}
	for _, c := range cases {
		src := "package p\nimport (\"os\"; \"os/exec\"; \"syscall\"; \"fmt\")\nvar _ = exec.Command\nvar _ = os.Getenv\nvar _ = syscall.Getpid\nvar _ = fmt.Sprint\nfunc f(g interface{ Run(...string) }) {\n" + c.src + "\n}\n"
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "x.go", src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("%s: %v", c.src, err)
		}
		g, f := scanFile(fset, "x.go", file)
		if (len(g) > 0) != c.git || (len(f) > 0) != c.file {
			t.Errorf("%s: git hits %v, file hits %v; want git=%v file=%v", c.src, g, f, c.git, c.file)
		}
	}
}
