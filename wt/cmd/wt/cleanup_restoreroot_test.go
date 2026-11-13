package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/gittest"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rootmove"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
)

// TestHandOffResultSeparatesWhatChangedNothing pins the closure boundary the
// session loop reads (final review I2): declined (exit 3) and refused before
// anything was created (exit 2, outcome refused) come back as notes that
// re-enter the list, carrying the command's own error for a caller that
// cannot; a failure midway, a could-not-start (exit 1) and an unrecognised
// error stay plain errors that end the session.
func TestHandOffResultSeparatesWhatChangedNothing(t *testing.T) {
	exit := func(code int, msg string) error { return &exitCodeError{code: code, err: errors.New(msg)} }
	asNote := func(err error) *tui.HandOffNote {
		var n *tui.HandOffNote
		if errors.As(err, &n) {
			return n
		}
		return nil
	}
	declined := exit(3, "not confirmed; nothing was changed")
	if n := asNote(restoreRootHandOffResult(declined, nil)); n == nil || n.Err != declined || n.Text != "restore root: not confirmed; nothing was changed" {
		t.Errorf("a declined R: %+v", n)
	}
	if n := asNote(rescueHandOffResult(exit(3, "not confirmed; nothing was created"), nil)); n == nil || n.Text != "rescue: not confirmed; nothing was created" {
		t.Errorf("a declined r: %+v", n)
	}
	refusedRR := &rootmove.Outcome{Status: rescue.StatusRefused, Code: "default_in_worktree", Reason: "main is checked out in /x"}
	if n := asNote(restoreRootHandOffResult(exit(2, "the branch was not moved"), refusedRR)); n == nil || n.Text != "restore root refused: main is checked out in /x" || n.Err == nil {
		t.Errorf("a refused R: %+v", n)
	}
	refusedR := &rescue.Outcome{Status: rescue.StatusRefused, Code: "nested", Reason: "it has a nested repository; nothing was created"}
	if n := asNote(rescueHandOffResult(exit(2, "the work was not copied"), refusedR)); n == nil || !strings.HasPrefix(n.Text, "rescue refused: it has a nested repository") {
		t.Errorf("a refused r: %+v", n)
	}
	// Failures and real errors end the session.
	for name, err := range map[string]error{
		"R failed":     restoreRootHandOffResult(exit(2, "the branch was not moved"), &rootmove.Outcome{Status: rescue.StatusFailed, Code: "carry", Reason: "x"}),
		"R created":    restoreRootHandOffResult(exit(2, "x"), &rootmove.Outcome{Status: rescue.StatusRefused, Created: true}),
		"R no outcome": restoreRootHandOffResult(exit(2, "x"), nil),
		"R exit 1":     restoreRootHandOffResult(exit(1, "could not start"), nil),
		"r failed":     rescueHandOffResult(exit(2, "x"), &rescue.Outcome{Status: rescue.StatusFailed}),
		"r kept":       rescueHandOffResult(exit(2, "x"), &rescue.Outcome{Status: rescue.StatusRefused, BranchKept: true}),
		"r exit 1":     rescueHandOffResult(exit(1, "x"), nil),
		"r plain":      rescueHandOffResult(errors.New("boom"), nil),
	} {
		if err == nil || asNote(err) != nil {
			t.Errorf("%s: %v, want a plain error that ends the session", name, err)
		}
	}
	// A root already on the default is a finished hand-off with a status.
	if n := asNote(restoreRootHandOffResult(nil, &rootmove.Outcome{Status: rescue.StatusRefused, Code: rootmove.CodeAlreadyDefault, Reason: "the root is already on main"})); n == nil || n.Err != nil || !strings.Contains(n.Text, "already on main") {
		t.Errorf("already on the default: %+v", n)
	}
}

// TestCleanupRestoreRootFuncDeclinedIsANote runs the real `R` hand-off with
// "n" at its prompt: the root is untouched and the closure returns a note
// carrying the command's exit-3 error.
func TestCleanupRestoreRootFuncDeclinedIsANote(t *testing.T) {
	stubCleanupGH(t)
	tpl := gittest.Seed(t, gittest.SeedOpts{ExcludeWorktrees: true})
	_, repo := tpl.Clone(t)
	gittest.Lineage(t, repo)
	cgit(t, repo, "switch", "-q", "-c", "topic")
	cleanupCommit(t, repo, "one.txt", "one")
	in := cleanupListInputs(repo, false, nil, "wt-cleanup")
	snap, err := cleanupGather(cleanup.Git{Dir: repo}, in)
	if err != nil {
		t.Fatal(err)
	}
	rm := cleanupRemoveFuncs(nil, snap, in, 0, nil)
	oldTTY := restoreRootTTY
	var out, errOut bytes.Buffer
	restoreRootTTY = func(io.Reader, io.Writer) bool { return true }
	t.Cleanup(func() { restoreRootTTY = oldTTY })
	var note *tui.HandOffNote
	err = rm.RestoreRoot(strings.NewReader("n\n"), &out, &errOut)
	if !errors.As(err, &note) || handOffExit(note.Err) != 3 || !strings.Contains(note.Text, "not confirmed") {
		t.Fatalf("RestoreRoot declined: %v, want a note carrying exit 3", err)
	}
	if got := rootBranch(t, repo); got != "topic" {
		t.Errorf("a declined move left the root on %s, want topic", got)
	}
}

// TestCleanupRestoreRootFuncMovesTheRoot calls the RestoreRoot func the cleanup
// list is handed on a root sitting on topic with commits and uncommitted work,
// and requires the root to end on main and clean with the branch and its work
// carried into .worktrees/topic — after the y/N question on the terminal and
// with the move's result printed. Why: `R` is only a hand-off; the producer
// side must drive `wt restore-root`'s real run path (its prompt, its gates,
// its result), or the root's drift survives while the list claims the action
// ran.
func TestCleanupRestoreRootFuncMovesTheRoot(t *testing.T) {
	stubCleanupGH(t)
	// The fixture is a gittest template-seeded clone pair whose lineage
	// ignores .worktrees/ (the state restore-root requires): every mutation
	// stays inside this test's own temp clone, never near a real repository.
	tpl := gittest.Seed(t, gittest.SeedOpts{ExcludeWorktrees: true})
	_, repo := tpl.Clone(t)
	gittest.Lineage(t, repo)
	cgit(t, repo, "switch", "-q", "-c", "topic")
	cleanupCommit(t, repo, "one.txt", "one")
	cleanupCommit(t, repo, "two.txt", "two")
	if err := os.WriteFile(filepath.Join(repo, "staged.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	cgit(t, repo, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The list's own inputs (only the directory reaches the hand-off: the run
	// path gathers its own snapshot) and the RemoveFuncs the list would get.
	// No app: the app is read only by Refresh and Refetch, which this test
	// never calls.
	in := cleanupListInputs(repo, false, nil, "wt-cleanup")
	snap, err := cleanupGather(cleanup.Git{Dir: repo}, in)
	if err != nil {
		t.Fatal(err)
	}
	rm := cleanupRemoveFuncs(nil, snap, in, 0, nil)
	if rm.RestoreRoot == nil {
		t.Fatal("RemoveFuncs.RestoreRoot is not wired; the list's R can do nothing")
	}
	// The hand-off asks on the terminal streams it is passed (the alt screen
	// is gone): name them (a "y" at the prompt) and let the prompt be asked.
	oldTTY := restoreRootTTY
	var out, errOut bytes.Buffer
	restoreRootTTY = func(io.Reader, io.Writer) bool { return true }
	t.Cleanup(func() { restoreRootTTY = oldTTY })

	// A finished move is a note: the list re-enters with a status line.
	var note *tui.HandOffNote
	if err := rm.RestoreRoot(strings.NewReader("y\n"), &out, &errOut); !errors.As(err, &note) || note.Err != nil || !strings.Contains(note.Text, "root restored to main") {
		t.Fatalf("RestoreRoot: %v, want a done note \"root restored to main\"\n%s", err, errOut.String())
	}

	if !strings.Contains(errOut.String(), "Move? [y/N] ") {
		t.Errorf("the hand-off never asked on the terminal:\n%s", errOut.String())
	}
	if got := rootBranch(t, repo); got != "main" {
		t.Errorf("root on %s, want main (restore-root puts it back on the default)", got)
	}
	if st := cgit(t, repo, "status", "--porcelain"); st != "" {
		t.Errorf("the root was left dirty: %q", st)
	}
	dest := filepath.Join(repo, ".worktrees", "topic")
	if got := rootBranch(t, dest); got != "topic" {
		t.Errorf("the new worktree %s is on %s, want topic", dest, got)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "new.txt")); string(b) != "n" {
		t.Errorf("the untracked work was not carried into %s: %q", dest, b)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "staged.txt")); string(b) != "s" {
		t.Errorf("the staged work was not carried into %s: %q", dest, b)
	}
	if !strings.Contains(out.String(), "moved") {
		t.Errorf("no move result was printed:\n%s", out.String())
	}
}

// TestCleanupRemoveFuncsRestoreRootWiredEvenWhenRegistryOff requires the
// restore-root hand-off to be wired on the removal-off path too (the registry
// did not load). Why: restore-root removes nothing — the registry's artifact
// protection is about removals — so a user whose registry is broken must still
// be able to move the root back to the default from the list (Rescue and
// Refetch are wired either way for the same reason).
func TestCleanupRemoveFuncsRestoreRootWiredEvenWhenRegistryOff(t *testing.T) {
	if off := cleanupRemoveFuncs(nil, cleanup.Snapshot{}, cleanup.GatherInputs{}, 0, errRegistryUnavailable); off.RestoreRoot == nil {
		t.Error("removal off took the restore-root hand-off away")
	}
}
