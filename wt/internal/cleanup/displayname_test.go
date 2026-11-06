package cleanup

import (
	"path/filepath"
	"strings"
	"testing"
)

// hasControl reports an ESC, BEL or other C0 control in s.
func hasControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// TestDisplayNameSanitisesDetachedWorktreeName: a detached worktree is listed
// under its directory's base name, which may hold any byte; DisplayName fed
// it raw to the plain output, the tick list and the confirmation, so a
// directory named with an OSC sequence retitled (or worse) the user's
// terminal. DisplayName is one printable line; Row.Name stays raw, so
// matching an argument to a row is unaffected.
func TestDisplayNameSanitisesDetachedWorktreeName(t *testing.T) {
	f := newFixture(t)
	evil := "x\x1b]0;pwned\x07y"
	p := filepath.Join(f.Dir, ".worktrees", evil)
	f.git("worktree", "add", "--detach", p)
	r := Classify(gather(t, f), Options{Now: later()})
	var row Row
	for _, x := range r.Rows {
		if x.Kind == KindWorktree && x.Name == evil {
			row = x
		}
	}
	if row.Name != evil {
		t.Fatalf("no detached worktree row named %q", evil)
	}
	if got := DisplayName(row); hasControl(got) || !strings.Contains(got, "pwned") {
		t.Errorf("DisplayName = %q, want the name with its control bytes dropped", got)
	}
}

// TestDisplayNameSanitisesRemoteRow: a remote row's name (remote/branch)
// goes through the same flattening as a local one.
func TestDisplayNameSanitisesRemoteRow(t *testing.T) {
	row := Row{Kind: KindRemote, Remote: "origin", Name: "a\x1b]0;pwned\x07"}
	if got := DisplayName(row); hasControl(got) || !strings.HasPrefix(got, "origin/a") {
		t.Errorf("DisplayName = %q, want origin/a... with no control bytes", got)
	}
}

// TestNestedPathsSanitised: the nested worktrees' paths the tick list names
// come from the filesystem and are printed as is; they are one printable
// line each.
func TestNestedPathsSanitised(t *testing.T) {
	row := Row{Blockers: []Reason{{Code: BlockNested, Detail: "/r/.worktrees/a\x1b]0;pwned\x07"}}}
	for _, p := range NestedPaths(row) {
		if hasControl(p) {
			t.Errorf("NestedPaths gave %q", p)
		}
	}
}
