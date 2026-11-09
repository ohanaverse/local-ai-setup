package rootmove

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// rescueBanned is cmd/wt's rescueBanned (rescue_test.go), copied: a test
// file of another package cannot be imported. The two lists must agree.
var rescueBanned = []string{"worktree remove", `"worktree", "remove"`, `"worktree", "prune"`,
	"update-ref -d", "update-ref --delete", `"update-ref", "-d"`, `"update-ref", "--delete"`,
	`branch", "-d`, `branch", "-D`, "--delete", "os.RemoveAll", "os.Remove(", "os.Rename(",
	`":"+`, `":" +`, `"push"`, "-X DELETE"}

// rootmoveBanned are the shapes no rootmove source may hold: rescue's, plus
// the reset and clean argv elements (the root's reset and the removal of its
// carried files are removal.ReleaseRoot's alone) and their shell forms. A
// bare "clean" is not banned ("the root is clean" is a message), nor
// "switch" (rootmove switches the destination). Any "update-ref" is banned
// too: rootmove writes no ref (the record is rescue's).
var rootmoveBanned = append(slices.Clone(rescueBanned), `"--hard"`, `"reset"`, `"clean"`,
	"reset --hard", "clean -", `"update-ref"`)

// riskyRootmoveSource lists each banned shape src holds.
func riskyRootmoveSource(name, src string) []string {
	var bad []string
	for _, s := range rootmoveBanned {
		if strings.Contains(src, s) {
			bad = append(bad, fmt.Sprintf("%s contains %q", name, s))
		}
	}
	return bad
}

// TestRootmoveContainsNoDeletion scans every non-test source of
// internal/rootmove and cmd/wt/restore_root*.go (none until Task 9) for
// rootmoveBanned. Why: rootmove composes a destructive operation, but the
// one reset, file removal and root switch must stay in removal.ReleaseRoot
// behind its proofs; rootmove only names that function.
func TestRootmoveContainsNoDeletion(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	more, _ := filepath.Glob("../../cmd/wt/restore_root*.go")
	scanned := 0
	for _, f := range append(files, more...) {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		scanned++
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range riskyRootmoveSource(f, string(b)) {
			t.Error(m)
		}
	}
	if scanned < 3 {
		t.Errorf("scanned only %d sources", scanned)
	}
}

// TestRiskyRootmoveSourceBites plants each banned shape and the harmless
// look-alikes rootmove does hold. Why: a guard that matched nothing would
// pass forever; one that matched the cleanup import or a "root is clean"
// message could not pass at all.
func TestRiskyRootmoveSourceBites(t *testing.T) {
	cases := []struct {
		name, src string
		bad       bool
	}{
		{"hard flag", `g.Run("reset", "--hard", "HEAD")`, true},
		{"reset argv", `args := []string{"reset"}`, true},
		{"clean argv", `g.Run("clean", "-x")`, true},
		{"clean shell", `exec.Command("sh", "-c", "git clean -fd")`, true},
		{"reset shell", `exec.Command("sh", "-c", "git reset --hard")`, true},
		{"remove all", `os.RemoveAll(dir)`, true},
		{"ref write", `g.Run("update-ref", ref, c, "")`, true},
		{"push", `g.Run("push", "origin", ":x")`, true},
		{"cleanup import and call", "import \"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup\"\nb, _ := cleanup.ResolveBase(g)", false},
		{"root is clean message", `msg := "the root is clean on main"`, false},
		{"release by name", `var releaseRoot = removal.ReleaseRoot`, false},
		{"destination switch", `stepGit(g, "switch", b)`, false},
	}
	for _, c := range cases {
		if got := riskyRootmoveSource("x.go", c.src); (len(got) > 0) != c.bad {
			t.Errorf("%s: reported %q, want bad=%v", c.name, got, c.bad)
		}
	}
}
