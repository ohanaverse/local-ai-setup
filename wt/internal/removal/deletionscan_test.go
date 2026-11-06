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

// allowedFileOps is one pre-existing non-test file's licence to call a
// file-deleting or file-replacing function (fileOp), and how many such
// references it holds.
type allowedFileOps struct {
	count  int    // exact number of fileOp references in the file
	reason string // why it may: what wt-owned file it removes or replaces
}

// fileOpAllowed lists, by path relative to the module root, the pre-existing
// non-test files that may call a file-deleting or file-replacing function
// (os.Remove, os.RemoveAll, os.Rename, syscall/unix Unlink, Rmdir, Rename...).
// Every one of them works on wt's own state files; none deletes a user's
// worktree, branch or ref. The count is pinned per file, so a new call added
// to a listed file fails too: a reviewer must change the count deliberately,
// with the reason still true. A new entry needs the same justification:
// removing user data belongs in internal/removal.
var fileOpAllowed = map[string]allowedFileOps{
	"cmd/wt/cloudsync_catalog.go":           {1, "saveFailedHTML: removes a same-named stale page in TMPDIR before writing a refused pricing page (O_EXCL)"},
	"internal/config/config.go":             {2, "atomic config write: removes its own temp file, renames it over the target"},
	"internal/config/fortest.go":            {1, "test helper's scratch config dir (RemoveAll of a temp dir it made)"},
	"internal/guard/guard.go":               {2, "guard uninstall: removes the block-main-commit pre-commit hook wt installed"},
	"internal/lifecycle/pidproc.go":         {3, "removes a process server's pidfile and start record"},
	"internal/lifecycle/server_loading.go":  {5, "pidfile and .wt start record: atomic write (temp + rename) and removal once the process is gone"},
	"internal/litellm/configfile.go":        {5, "atomic write of LiteLLM's config.yaml: temp file removed on failure, renamed over the target"},
	"internal/profiles/configcontent.go":    {12, "restores an agent config file a profile's config_content replaced and removes its own backups"},
	"internal/rotation/rotation.go":         {1, "removes legacy rotation-*.state files after migrating them into rotation.state"},
	"internal/sessionclaim/sessionclaim.go": {2, "removes this process's session claim and claims of dead pids under wt-sessions/"},
	"internal/slotedit/apply.go":            {2, "slot-profiles.toml edit: removes its own dated backup / temp file when a write fails"},
	"internal/smoke/smoke.go":               {2, "removes the temporary git repos and dirs wt smoke made for its rows"},
	"internal/themes/load.go":               {1, "themes.Unset removes wt's own themes.toml"},
}

// fileOpNever are files no allow-list entry may cover: the read-only cleanup
// classifier and its command (TestCleanupCmdContainsNoDeletion holds the same
// line with a substring scan).
func fileOpNever(rel string) bool {
	return strings.HasPrefix(rel, "internal/cleanup/") ||
		(strings.HasPrefix(rel, "cmd/wt/cleanup") && !strings.Contains(strings.TrimPrefix(rel, "cmd/wt/"), "/"))
}

// fileOpPkgs are the packages whose file-deleting or file-replacing functions
// the scan knows.
var fileOpPkgs = []string{"os", "syscall", "golang.org/x/sys/unix"}

// fileOp reports whether pkg.name deletes or replaces a file.
func fileOp(pkg, name string) bool {
	switch pkg {
	case "os":
		return name == "Remove" || name == "RemoveAll" || name == "Rename"
	case "syscall", "golang.org/x/sys/unix":
		return name == "Unlink" || name == "Unlinkat" || name == "Rmdir" || strings.HasPrefix(name, "Rename")
	}
	return false
}

// finding is one banned use: where, and which token.
type finding struct {
	at    string // rel:line
	token string
}

// dyn stands for the part of a string built at runtime: ":"+b reads as
// ":" + dyn, so a refspec whose branch is a variable is still seen.
const dyn = "\x00"

// argReader flattens one file's argument expressions into the strings the
// source fixes: literals, constant concatenations ("re"+"move") and string
// constants declared in the same file. A concatenation with a runtime part
// keeps its constant prefix followed by dyn.
type argReader struct {
	consts   map[string]string // same-file string constants, by name
	consumed map[ast.Node]bool // append calls and slice literals already read as part of an outer call
}

func newArgReader(file *ast.File) *argReader {
	r := &argReader{consts: map[string]string{}, consumed: map[ast.Node]bool{}}
	// Two passes, so a constant defined from another (const b = a + "x")
	// resolves whichever is declared first.
	for range 2 {
		ast.Inspect(file, func(n ast.Node) bool {
			gd, ok := n.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				return true
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						if v, ok := r.constant(vs.Values[i]); ok {
							r.consts[name.Name] = v
						}
					}
				}
			}
			return true
		})
	}
	return r
}

// constant is e's value when the source fixes it entirely.
func (r *argReader) constant(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(e.Value)
		return v, err == nil
	case *ast.Ident:
		v, ok := r.consts[e.Name]
		return v, ok
	case *ast.ParenExpr:
		return r.constant(e.X)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		l, ok1 := r.constant(e.X)
		rr, ok2 := r.constant(e.Y)
		return l + rr, ok1 && ok2
	}
	return "", false
}

// prefix is the constant start of a concatenation with a runtime part,
// followed by dyn; ok is false when nothing at its start is constant.
func (r *argReader) prefix(e ast.Expr) (string, bool) {
	be, ok := e.(*ast.BinaryExpr)
	if !ok || be.Op != token.ADD {
		return "", false
	}
	if l, ok := r.constant(be.X); ok {
		return l + dyn, true
	}
	return r.prefix(be.X)
}

// args flattens exprs, in order, into the strings the source fixes: nested
// append(...) calls and slice literals are read in place (and marked
// consumed, so they are not reported again on their own); any other
// expression is skipped, so `"branch", name, "-D"` still reads as branch, -D.
func (r *argReader) args(exprs []ast.Expr) []string {
	var out []string
	for _, e := range exprs {
		if v, ok := r.constant(e); ok {
			out = append(out, v)
			continue
		}
		if v, ok := r.prefix(e); ok {
			out = append(out, v)
			continue
		}
		switch e := e.(type) {
		case *ast.CallExpr:
			if id, ok := e.Fun.(*ast.Ident); ok && id.Name == "append" {
				r.consumed[e] = true
				out = append(out, r.args(e.Args)...)
			}
		case *ast.CompositeLit:
			r.consumed[e] = true
			out = append(out, r.args(e.Elts)...)
		}
	}
	return out
}

// shortFlagWith reports whether s is a single-dash flag cluster (-d, -Df,
// -fd) holding one of the runes in set.
func shortFlagWith(s, set string) bool {
	return len(s) > 1 && s[0] == '-' && s[1] != '-' && !strings.Contains(s, dyn) && strings.ContainsAny(s[1:], set)
}

// refLike matches what may follow a refspec's ":" to delete a remote ref.
var refLike = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/-]*$`)

// deleteRefspec reports whether s is a push refspec that deletes a remote
// ref: ":<ref>" or "+:<ref>", the ref fixed or built at runtime (":"+b).
// ":" alone (push matching branches) and ":%d"-like text are not.
func deleteRefspec(s string) bool {
	s = strings.TrimPrefix(s, "+")
	rest, ok := strings.CutPrefix(s, ":")
	return ok && (strings.HasPrefix(rest, dyn) || refLike.MatchString(rest))
}

// gitDeletion names the banned git/gh argument shape in one argument list, or
// "" when there is none. args are the list's fixed strings in order.
func gitDeletion(args []string) string {
	has := func(s string) bool { return slices.Contains(args, s) }
	afterAny := func(a string, match func(string) bool) bool {
		i := slices.Index(args, a)
		return i >= 0 && slices.ContainsFunc(args[i+1:], match)
	}
	after := func(a string, bs ...string) bool {
		return afterAny(a, func(s string) bool { return slices.Contains(bs, s) })
	}
	switch {
	case after("worktree", "remove"):
		return `"worktree", "remove"`
	case after("update-ref", "-d", "--delete"):
		return `"update-ref", "-d"`
	case afterAny("branch", func(s string) bool { return shortFlagWith(s, "dD") }):
		return `"branch", "-d"/"-D"`
	case has("--delete"):
		return `"--delete"`
	case afterAny("push", func(s string) bool { return shortFlagWith(s, "d") }):
		return `"push", "-d"`
	case afterAny("push", deleteRefspec):
		return `push refspec ":<branch>"`
	case has("api") && (after("-X", "DELETE") || after("--method", "DELETE") || has("-XDELETE") || has("--method=DELETE")):
		return "gh api -X DELETE"
	}
	return ""
}

// lineDeletions are the banned command lines written as one string literal
// ("git worktree remove x" for a shell).
var lineDeletions = []struct {
	re    *regexp.Regexp
	token string
}{
	{regexp.MustCompile(`\bworktree\s+remove\b`), "worktree remove"},
	{regexp.MustCompile(`\bupdate-ref\s+(-d|--delete)\b`), "update-ref -d"},
	{regexp.MustCompile(`\bbranch\s+(-[A-Za-z]*[dD][A-Za-z]*|--delete)\b`), "branch -d/-D"},
	{regexp.MustCompile(`\bpush\b.*\s(-[A-Za-z]*d[A-Za-z]*|--delete)(\s|$)`), "push -d/--delete"},
	{regexp.MustCompile(`\bpush\b.*\s\+?:[A-Za-z0-9_][A-Za-z0-9._/-]*(\s|$)`), "push :<branch>"},
	{regexp.MustCompile(`-X\s*DELETE\b|--method[=\s]DELETE\b`), "-X DELETE"},
}

// lineDeletion names a banned command line in s, or "".
func lineDeletion(s string) string {
	for _, l := range lineDeletions {
		if l.re.MatchString(s) {
			return l.token
		}
	}
	return ""
}

// scanFile returns the banned shapes (hard: never allowed outside
// internal/removal) and the file-op references (allowed per fileOpAllowed)
// in one parsed file.
func scanFile(fset *token.FileSet, rel string, file *ast.File) (gitHits, fileHits []finding) {
	at := func(n ast.Node) string { return rel + ":" + strconv.Itoa(fset.Position(n.Pos()).Line) }
	// The names this file imports the file-op packages under. A dot-import
	// would make RemoveAll(p) a bare call no selector match sees: refused.
	pkgName := map[string]string{} // local name -> import path
	for _, imp := range file.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		if !slices.Contains(fileOpPkgs, path) {
			continue
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "." {
			gitHits = append(gitHits, finding{at(imp), "dot-import of " + path})
			continue
		}
		pkgName[name] = path
	}
	r := newArgReader(file)
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			// An append or slice literal already read as an outer call's
			// argument is not reported twice; the outer call is.
			if !r.consumed[n] {
				if tok := gitDeletion(r.args(n.Args)); tok != "" {
					gitHits = append(gitHits, finding{at(n), tok})
				}
			}
		case *ast.CompositeLit:
			if !r.consumed[n] {
				if tok := gitDeletion(r.args(n.Elts)); tok != "" {
					gitHits = append(gitHits, finding{at(n), tok})
				}
			}
		case *ast.BasicLit:
			if v, ok := r.constant(n); ok {
				if tok := lineDeletion(v); tok != "" {
					gitHits = append(gitHits, finding{at(n), tok})
				}
			}
		case *ast.SelectorExpr:
			// Any reference, called or not (f := os.Remove counts too).
			id, ok := n.X.(*ast.Ident)
			if !ok {
				return true
			}
			if path, ok := pkgName[id.Name]; ok && fileOp(path, n.Sel.Name) {
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
// "worktree remove" does not trip it. Each call's arguments are flattened
// into one ordered list of the strings the source fixes: literals, nested
// append(...) calls and slice literals, constant concatenations ("re"+"move"),
// string constants declared in the same file, and the constant prefix of a
// runtime concatenation (":"+b). It fails for:
//   - such a list holding, in order: "worktree" ... "remove"; "update-ref" ...
//     "-d"; "branch" ... a short-flag cluster with d or D (-d, -D, -Df, -dr);
//     "--delete" anywhere; "push" ... a short-flag cluster with d (-d, -fd);
//     "push" ... a refspec ":<ref>" or "+:<ref>" (deleting a remote branch;
//     ":" alone and ":%d"-like text do not count); gh "api" with -X/--method
//     DELETE;
//   - one string literal holding such a command line ("git worktree remove x",
//     "git push -d origin x", "git branch -Df x");
//   - a dot-import of os, syscall or golang.org/x/sys/unix;
//   - os.Remove/RemoveAll/Rename or syscall/unix Unlink/Unlinkat/Rmdir/Rename*
//     in a file not on fileOpAllowed, ever in internal/cleanup or
//     cmd/wt/cleanup*.go, or a listed file whose count of them changed;
//   - an allow-list entry whose file is gone or no longer uses such a
//     function (a stale entry would let a later deletion in unseen).
//
// What it cannot see: strings built at runtime beyond a constant prefix (a
// variable holding "remove", strings.Fields of a command line, an argv read
// from config, a constant from another file or package), git or gh run
// through `sh -c` with a non-literal script or through another binary,
// deletion through exec of rm or a package other than os, syscall and
// x/sys/unix, and test files.
//
// Carry for later tasks: the command-line check matches every string literal
// module-wide, so a file outside internal/removal (notably cmd/wt/remove.go,
// Task 9) must word its help text and error messages around phrases like
// "git branch -d", "git worktree remove" or "push -d"; keep the git argv in
// internal/removal.
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
		allowed, ok := fileOpAllowed[rel]
		if ok && !fileOpNever(rel) {
			if got := len(fileHits[rel]); got != allowed.count {
				var where []string
				for _, h := range fileHits[rel] {
					where = append(where, h.at+" "+h.token)
				}
				t.Errorf("%s holds %d file deletions/renames, fileOpAllowed says %d (%s); review each and update the count:\n  %s",
					rel, got, allowed.count, allowed.reason, strings.Join(where, "\n  "))
			}
			continue
		}
		for _, h := range fileHits[rel] {
			t.Errorf("%s: %s in a file not on fileOpAllowed; deleting user data belongs in internal/removal (wt's own state files: add the file with a count and reason)", h.at, h.token)
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
		// Fix round 1: shapes the first version missed.
		{`g.Run("push", "-d", "origin", b)`, true, false},
		{`g.Run("push", "-fd", "origin", b)`, true, false},
		{`g.Run("push", "origin", "+:"+b)`, true, false},
		{`g.Run("push", "origin", "+:topic")`, true, false},
		{`g.Run("push", "origin", ":"+b)`, true, false},
		{`g.Run("branch", "-Df", b)`, true, false},
		{`g.Run("branch", "-dr", b)`, true, false},
		{`g.Run("branch", "-fD", b)`, true, false},
		{`exec.Command("git", append([]string{"branch"}, "-D", x)...)`, true, false},
		{`g.Run([]string{"worktree", "remove", p}...)`, true, false},
		{`g.Run("worktree", "re"+"move", p)`, true, false},
		{`const sub = "remove"; g.Run("worktree", sub, p)`, true, false},
		{`exec.Command("sh", "-c", "git push -d origin topic")`, true, false},
		{`exec.Command("sh", "-c", "git push origin +:topic")`, true, false},
		{`exec.Command("sh", "-c", "git branch -Df topic")`, true, false},
		{`unix.Unlinkat(fd, p, 0)`, false, true},
		{`unix.Renameat(fd, a, fd, b)`, false, true},
		// Look-alikes that must pass.
		{`g.Run("branch", "-a")`, false, false},
		{`g.Run("push", "origin", "topic")`, false, false},
		{`g.Run("push", "--force-with-lease", "origin", "topic")`, false, false},
		{`g.Run("push", "origin", ":%d")`, false, false},
		{`g.Run("push", "origin", ":")`, false, false},
		{`exec.Command("sh", "-c", "git branch --list topic")`, false, false},
		{`g.Run("worktree", "list", "--porcelain")`, false, false},
		{`g.Run("branch", "--list", "-v")`, false, false},
		{`g.Run("push", "-u", "origin", "main")`, false, false},
		{`exec.Command("gh", "api", "repos/o/r")`, false, false},
		{`fmt.Println("a:b")`, false, false},
		{`// git worktree remove in a comment
		_ = 1`, false, false},
	}
	for _, c := range cases {
		src := "package p\nimport (\"os\"; \"os/exec\"; \"syscall\"; \"fmt\"; \"golang.org/x/sys/unix\")\nvar _ = exec.Command\nvar _ = os.Getenv\nvar _ = syscall.Getpid\nvar _ = fmt.Sprint\nvar _ = unix.Getpid\nfunc f(g interface{ Run(...string) }) {\n" + c.src + "\n}\n"
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

// TestDeletionScanRefusesDotImports verifies a dot-import of os, syscall or
// x/sys/unix is reported: `import . "os"` makes RemoveAll(p) a bare call the
// selector match above cannot see.
func TestDeletionScanRefusesDotImports(t *testing.T) {
	for _, pkg := range []string{"os", "syscall", "golang.org/x/sys/unix"} {
		src := "package p\nimport . \"" + pkg + "\"\nvar _ = Getpid\n"
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "x.go", src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		if g, _ := scanFile(fset, "x.go", file); len(g) == 0 {
			t.Errorf("dot-import of %s not reported", pkg)
		}
	}
}
