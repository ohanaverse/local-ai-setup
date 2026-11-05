package removal

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// TestOutcomeEnumStrings pins every worktree and branch outcome to its wire
// string (plan call 3). They are the removal JSON contract and what scripts
// match on; renaming one silently breaks every reader.
func TestOutcomeEnumStrings(t *testing.T) {
	wt := map[WorktreeOutcome]string{
		WtRemoved:        "removed",
		WtSkippedChanged: "skipped_changed",
		WtRefused:        "refused",
		WtFailed:         "failed",
		WtNotRequested:   "not_requested",
	}
	for v, want := range wt {
		if string(v) != want {
			t.Errorf("worktree outcome %q, want %q", v, want)
		}
	}
	br := map[BranchOutcome]string{
		BrDeleted:            "deleted",
		BrNotAttempted:       "not_attempted",
		BrRetainedUnmerged:   "retained_unmerged",
		BrRetainedMoved:      "retained_moved",
		BrRetainedCheckedOut: "retained_checked_out",
		BrRetainedDefault:    "retained_default",
		BrRetainedUnknown:    "retained_unknown",
	}
	for v, want := range br {
		if string(v) != want {
			t.Errorf("branch outcome %q, want %q", v, want)
		}
	}
	if len(wt) != 5 || len(br) != 7 {
		t.Errorf("got %d worktree and %d branch outcomes, want 5 and 7 distinct values", len(wt), len(br))
	}
}

// TestOutcomeReasonIsOneLine verifies an Outcome's Reason and Warnings are
// one sanitised line whichever way they are set: they carry git's stderr and
// OS errors to the terminal and the JSON, where a newline breaks the one-line
// layout and an ESC or bidi control could drive the user's terminal.
func TestOutcomeReasonIsOneLine(t *testing.T) {
	raw := "fatal: cannot remove\n\x1b]0;pwned\x07 worktree\t‮exe.txt\r\nsecond line"
	check := func(name, got string) {
		t.Helper()
		if strings.ContainsAny(got, "\n\r\t\x1b\x07‮") {
			t.Errorf("%s = %q, holds a control character", name, got)
		}
		if !strings.Contains(got, "fatal: cannot remove") || !strings.Contains(got, "second line") {
			t.Errorf("%s = %q, lost the text", name, got)
		}
	}
	o := NewOutcome(Item{Name: "topic"}, WtFailed, BrNotAttempted, raw)
	check("NewOutcome Reason", o.Reason)
	if o.Worktree != WtFailed || o.Branch != BrNotAttempted || o.Item.Name != "topic" {
		t.Errorf("NewOutcome = %+v, lost a field", o)
	}
	var p Outcome
	p.SetReason(raw)
	check("SetReason", p.Reason)
	p.AddWarning(raw)
	if len(p.Warnings) != 1 {
		t.Fatalf("Warnings = %q, want one", p.Warnings)
	}
	check("AddWarning", p.Warnings[0])
}

// TestFixtureBuildsItems runs the slim fixture end to end the way the command
// will: a squash-merged, pushed worktree branch gathered and classified like
// `wt cleanup` does comes out proven, and the Item built from its row names the
// full ref and tip the engine must re-check. Later tasks build on this; a
// fixture that cannot produce a proven row would make every removal test
// vacuous.
func TestFixtureBuildsItems(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.Worktree("topic")
	tip := f.Commit(p, "topic.txt", "work", "topic work")
	f.Push("topic")
	f.SquashMerge("topic")
	f.Ignored(p, ".env")
	f.Dirty(f.Dir, "unrelated.txt")

	_, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	if !strings.HasSuffix(row.Path, "/.worktrees/topic") {
		t.Errorf("path %q, want it under .worktrees", row.Path)
	}
	if !row.BranchProven {
		t.Errorf("branch of a squash-merged worktree not proven: %+v", row)
	}
	it := itemOf(row)
	if it.Kind != cleanup.KindWorktree || it.Ref != "refs/heads/topic" || it.SawTip != tip || it.Saw != row.Status {
		t.Errorf("item %+v, want worktree refs/heads/topic at %s", it, tip)
	}
	if row.Losses.IgnoredEntries == 0 {
		t.Errorf("Ignored left no ignored entry in the row's losses: %+v", row.Losses)
	}

	locked := f.Worktree("held")
	f.Lock(locked)
	f.Branch("other")
	f.Switch(locked, "other")
	_, r = f.report()
	if row := f.row(r, cleanup.KindWorktree, "other"); row.Status != cleanup.StatusInUse {
		t.Errorf("locked worktree status %s, want in_use", row.Status)
	}
}

// TestFixtureIgnoresHostileGlobalConfig points every global git config git
// could read (GIT_CONFIG_GLOBAL, ~/.gitconfig, $XDG_CONFIG_HOME/git/config) at
// a core.hooksPath whose pre-commit hook always fails, checks that this config
// does fail a plain commit, then applies TestMain's isolateGitConfig and
// builds a fixture, which commits. A developer's global config must not reach
// the fixture: a hook, template or merge driver there would make removal
// tests fail, or pass, for reasons of their machine.
func TestFixtureIgnoresHostileGlobalConfig(t *testing.T) {
	hooks := t.TempDir()
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	hostile := "[core]\n\thooksPath = " + hooks + "\n"
	home := t.TempDir()
	global := filepath.Join(t.TempDir(), "gitconfig")
	for _, p := range []string{global, filepath.Join(home, ".gitconfig"), filepath.Join(home, "git", "config")} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(hostile), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", global)

	// Control: the hostile config is in force, or this test proves nothing.
	dir := t.TempDir()
	if b, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, b)
	}
	if b, err := exec.Command("git", "-C", dir, "-c", "user.name=T", "-c", "user.email=t@example.com",
		"commit", "-q", "--allow-empty", "-m", "x").CombinedOutput(); err == nil {
		t.Fatalf("commit under the hostile hooksPath succeeded; the control is broken\n%s", b)
	}

	if err := isolateGitConfig(home); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	f.Commit(f.Dir, "after.txt", "x", "commit after isolation")
}
