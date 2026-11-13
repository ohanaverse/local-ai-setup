package rootmove

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
)

// TestEncodeResultKeepsUnknownCountsOut encodes a refusal that carries no plan
// and checks the document has no record, no dest_path, no carried counts,
// created false and arrays that are [] not null. Why: a refusal knows nothing
// about a destination or a copy; printing zero counts would read as "nothing to
// carry".
func TestEncodeResultKeepsUnknownCountsOut(t *testing.T) {
	t.Parallel()
	b, err := EncodeResult(Outcome{Status: rescue.StatusRefused, Code: CodeDetachedRoot, Reason: "a\nb"}, Meta{Repo: "/r"})
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"record", "dest_path", "carried", "left_behind", "branch", "default", "default_ff", "fast_forwarded"} {
		if _, ok := v[k]; ok {
			t.Errorf("a plan-less refusal writes %q:\n%s", k, b)
		}
	}
	if v["created"] != false || v["outcome"] != "refused" || v["reason"] != "a b" {
		t.Errorf("created %v outcome %v reason %v", v["created"], v["outcome"], v["reason"])
	}
	if c, ok := v["completed"].([]any); !ok || len(c) != 0 {
		t.Errorf("completed %v, want []", v["completed"])
	}
	if w, ok := v["warnings"].([]any); !ok || len(w) != 0 {
		t.Errorf("warnings %v, want []", v["warnings"])
	}
}

// TestEncodeResultCarriesPlanFacts encodes a moved outcome and checks the plan's
// branch, default, destination, counts, default_ff and the record reach the
// document, and that a refusal made with a plan (created false) omits
// dest_path. Why: the keys are the contract; a refusal must not name a
// directory that does not exist.
func TestEncodeResultCarriesPlanFacts(t *testing.T) {
	t.Parallel()
	p := Plan{Repo: "/r", Branch: "feat", Default: "main", DestPath: "/r/.worktrees/feat", DefaultFF: true,
		Counts: rescue.Counts{Commits: 2, Staged: 1, Unstaged: 3, Untracked: 4, UntrackedBytes: 9, IgnoredLeft: 5}, Warnings: []string{"w"}}
	b, err := EncodeResult(Outcome{Status: rescue.StatusMoved, Plan: p, Created: true, Completed: []string{"s"},
		Record: &rescue.Record{Ref: "refs/wt/rescued/x", Commit: "c"}, FastForwarded: true}, Meta{Repo: "/r"})
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if v["branch"] != "feat" || v["default"] != "main" || v["dest_path"] != "/r/.worktrees/feat" || v["default_ff"] != true || v["fast_forwarded"] != true {
		t.Errorf("plan facts missing:\n%s", b)
	}
	if v["carried"].(map[string]any)["untracked_bytes"] != float64(9) || v["left_behind"].(map[string]any)["ignored_entries"] != float64(5) || v["record"] == nil {
		t.Errorf("counts or record missing:\n%s", b)
	}
	b, err = EncodeResult(Outcome{Status: rescue.StatusRefused, Code: CodeDetachedRoot, Plan: p}, Meta{Repo: "/r"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "dest_path") {
		t.Errorf("a refusal with created=false names a destination:\n%s", b)
	}
}

// TestCodesCoverEveryCodeRootmoveEmits scans the non-test sources of this
// package (and rescue's, for the values) and requires every Code constant
// declared here and every rescue.Code* it names to be in Codes, and Codes to
// hold nothing else except what rescue.Preflight can hand through: rescue's
// own list minus `primary` and `nothing_to_keep`, and minus the held word,
// which is only the word a HeldWarningPrefix warnings line starts with now,
// never a code. Why: the schema's code enum is built from Codes; a code
// rootmove can emit but the contract lacks makes a consumer's validator
// reject a real document, and a held root that came back as a code would
// contradict the warning it rides the plan on.
func TestCodesCoverEveryCodeRootmoveEmits(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	rescueVals := map[string]string{}
	rpkgs, err := parser.ParseDir(fset, "../rescue", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range rpkgs {
		for _, f := range pkg.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				vs, ok := n.(*ast.ValueSpec)
				if !ok {
					return true
				}
				for i, id := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING && strings.HasPrefix(id.Name, "Code") {
						rescueVals[id.Name], _ = strconv.Unquote(lit.Value)
					}
				}
				return true
			})
		}
	}
	emitted := map[string]string{} // code -> where
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.ValueSpec:
					for i, id := range x.Names {
						if !strings.HasPrefix(id.Name, "Code") || i >= len(x.Values) {
							continue
						}
						switch v := x.Values[i].(type) {
						case *ast.BasicLit:
							s, _ := strconv.Unquote(v.Value)
							emitted[s] = id.Name
						case *ast.SelectorExpr:
							emitted[rescueVals[v.Sel.Name]] = "rescue." + v.Sel.Name
						}
					}
				case *ast.SelectorExpr:
					if pk, ok := x.X.(*ast.Ident); ok && pk.Name == "rescue" && strings.HasPrefix(x.Sel.Name, "Code") {
						emitted[rescueVals[x.Sel.Name]] = "rescue." + x.Sel.Name
					}
				}
				return true
			})
		}
	}
	if len(emitted) < 15 {
		t.Fatalf("scan found only %d codes (%v); it is not reading the sources", len(emitted), emitted)
	}
	for code, where := range emitted {
		if code == "" || !slices.Contains(Codes, code) {
			t.Errorf("rootmove can emit %q (%s) but Codes lacks it", code, where)
		}
	}
	// What ForRoot's rescue preflight can hand through: all of rescue's codes
	// but the two a root never gets.
	for _, c := range rescue.Codes {
		if c == rescue.CodePrimary || c == rescue.CodeNothingToKeep {
			continue
		}
		if !slices.Contains(Codes, c) {
			t.Errorf("rescue code %q is missing from Codes", c)
		}
	}
	// The held word goes with the two: it is only the word a HeldWarningPrefix
	// warnings line starts with now, never a code a document carries.
	heldWord := strings.TrimSuffix(HeldWarningPrefix, ": ")
	for _, c := range []string{rescue.CodePrimary, rescue.CodeNothingToKeep, heldWord} {
		if slices.Contains(Codes, c) {
			t.Errorf("Codes holds %q, which a root move never answers", c)
		}
	}
	for _, c := range Codes {
		if !slices.Contains(rescue.Codes, c) && emitted[c] == "" {
			t.Errorf("Codes lists %q, which nothing in rootmove declares", c)
		}
	}
}
