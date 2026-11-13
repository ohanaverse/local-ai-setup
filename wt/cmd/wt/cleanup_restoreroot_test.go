package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/gittest"
)

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
	// The hand-off asks on the real terminal once the alt screen is gone:
	// name those streams (a "y" at the prompt) and let the prompt be asked.
	oldStreams, oldTTY := rescueStreams, restoreRootTTY
	var out, errOut bytes.Buffer
	rescueStreams = func() (io.Reader, io.Writer, io.Writer) { return strings.NewReader("y\n"), &out, &errOut }
	restoreRootTTY = func(io.Reader, io.Writer) bool { return true }
	t.Cleanup(func() { rescueStreams, restoreRootTTY = oldStreams, oldTTY })

	if err := rm.RestoreRoot(); err != nil {
		t.Fatalf("RestoreRoot: %v\n%s", err, errOut.String())
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
