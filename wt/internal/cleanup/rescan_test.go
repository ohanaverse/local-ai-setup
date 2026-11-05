package cleanup

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// rescanInputs are the GatherInputs gather(t, f) uses, so a rescan sees the
// same caller directory and own repo as the full run it is compared with.
func rescanInputs(f *fixture) GatherInputs {
	return GatherInputs{CWD: f.Dir, OwnRepo: "o/r", Parallel: 2}
}

// targetOf is the Target that names row.
func targetOf(row Row) Target {
	return Target{Kind: row.Kind, Path: row.Path, Ref: row.Ref}
}

// mustRescan rescans t and fails the test on an error.
func mustRescan(tb *testing.T, f *fixture, prior Snapshot, o Options, t Target) Row {
	tb.Helper()
	row, err := Rescan(Git{Dir: f.Dir}, prior, rescanInputs(f), o, t)
	if err != nil {
		tb.Fatalf("Rescan(%+v): %v", t, err)
	}
	return row
}

// sameRow compares two rows field by field, except the age (the clock may
// differ between a list and a rescan).
func sameRow(t *testing.T, got, want Row) {
	t.Helper()
	got.AgeDays, got.AgeKnown = 0, false
	want.AgeDays, want.AgeKnown = 0, false
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rescanned row differs from the full run:\n got  %+v\n want %+v", got, want)
	}
}

// TestRescanReflectsNewDirtyFile lists a clean worktree, then leaves an
// untracked file in it and rescans it. Why: the removal re-checks each item
// immediately before acting; a file written after the list was shown is
// work that exists only there, so the fresh row must be unique_work (dirty),
// never the stale verdict from the list.
func TestRescanReflectsNewDirtyFile(t *testing.T) {
	f := newFixture(t)
	wt := f.Worktree("topic")
	noGh(t)
	o := Options{Now: later()}
	s := gather(t, f)
	before := findRow(t, Classify(s, o), KindWorktree, "topic")
	if before.Status == StatusUniqueWork || before.Losses.Dirty {
		t.Fatalf("before the edit the row is already %s (dirty %v)", before.Status, before.Losses.Dirty)
	}

	f.DirtyIn(wt, "new.txt")
	row := mustRescan(t, f, s, o, targetOf(before))
	if row.Status != StatusUniqueWork || !row.Losses.Dirty || !slices.Contains(reasonCodes(row.Reasons), ReasonDirty) {
		t.Fatalf("rescanned row %s dirty=%v reasons %v, want unique_work with %q", row.Status, row.Losses.Dirty, reasonCodes(row.Reasons), ReasonDirty)
	}
}

// TestRescanReflectsMovedTip commits on a worktree's branch after the list
// and rescans it. Why: the removal's compare-and-swap deletes the branch only
// at the commit the re-check classified; a rescan that kept the listed tip
// would let a commit made in between be deleted unseen.
func TestRescanReflectsMovedTip(t *testing.T) {
	f := newFixture(t)
	wt := f.Worktree("topic")
	noGh(t)
	o := Options{Now: later()}
	s := gather(t, f)
	before := findRow(t, Classify(s, o), KindWorktree, "topic")

	moved := commitIn(t, wt, "more", "more", "more")
	row := mustRescan(t, f, s, o, targetOf(before))
	if row.Tip != moved || row.Head != moved || row.Tip == before.Tip {
		t.Fatalf("rescanned tip %q head %q, want the new commit %s (listed %s)", row.Tip, row.Head, moved, before.Tip)
	}
	if row.Status == StatusProven {
		t.Fatalf("a branch with an unpushed commit rescanned proven: %+v", row)
	}
}

// TestRescanGoneTarget removes a listed worktree and deletes a listed branch
// and a listed remote-tracking ref (by hand, in the test) before rescanning
// each. Why: something removed between the list and the act is not a row to
// judge; ErrTargetGone lets the removal report it as already gone instead of
// acting on a stale row or calling it a git failure.
func TestRescanGoneTarget(t *testing.T) {
	f := newFixture(t)
	f.Worktree("topic")
	f.Branch("solo")
	f.Branch("shared")
	f.Push("shared")
	noGh(t)
	o := Options{Now: later()}
	s := gather(t, f)
	r := Classify(s, o)
	w := findRow(t, r, KindWorktree, "topic")
	b := findRow(t, r, KindBranch, "solo")
	rm := findRow(t, r, KindRemote, "shared")

	f.git("worktree", "remove", w.Path)
	f.git("branch", "-D", "solo")
	f.git("update-ref", "-d", "refs/remotes/origin/shared")
	for _, row := range []Row{w, b, rm} {
		_, err := Rescan(Git{Dir: f.Dir}, s, rescanInputs(f), o, targetOf(row))
		if !errors.Is(err, ErrTargetGone) {
			t.Errorf("Rescan(%s %s) error = %v, want ErrTargetGone", row.Kind, row.Name, err)
		}
	}
}

// TestRescanDoesNotCallGhOrFetch rescans with Fetch asked for, after origin
// gained a commit, and with gh stubbed to fail the test. Why: the re-check is
// local only (spec: no new fetch); a fetch would move the base under the
// list the user approved and a gh call per item would make removal slow and
// network-dependent. The remote-tracking ref must still be where it was.
func TestRescanDoesNotCallGhOrFetch(t *testing.T) {
	f := newFixture(t)
	f.Worktree("topic")
	noGh(t)
	o := Options{Now: later()}
	s := gather(t, f)
	w := findRow(t, Classify(s, o), KindWorktree, "topic")

	other := filepath.Join(t.TempDir(), "other")
	if b, err := exec.Command("git", "clone", f.Origin, other).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, b)
	}
	gitIn(t, other, "config", "user.email", "t@example.com")
	gitIn(t, other, "config", "user.name", "T")
	commitIn(t, other, "upstream", "upstream", "upstream")
	gitIn(t, other, "push", "origin", "main")
	originMain := f.git("rev-parse", "refs/remotes/origin/main")

	stubGh(t, func(string, string) ([]byte, error) {
		t.Error("Rescan ran gh")
		return nil, errors.New("no gh in a rescan")
	})
	in := rescanInputs(f)
	in.Fetch = true
	if _, err := Rescan(Git{Dir: f.Dir}, s, in, o, targetOf(w)); err != nil {
		t.Fatalf("Rescan: %v", err)
	}
	if got := f.git("rev-parse", "refs/remotes/origin/main"); got != originMain {
		t.Fatalf("origin/main moved from %s to %s: Rescan fetched", originMain, got)
	}
}

// TestRescanMatchesGatherForUnchangedTarget dirties a different worktree
// after the list and rescans an untouched one. Why: a change elsewhere in
// the repository must not leak into the target's verdict, and with the
// target itself unchanged the rescan must give exactly the row the list
// showed: the removal compares the two and refuses on any difference.
func TestRescanMatchesGatherForUnchangedTarget(t *testing.T) {
	f := newFixture(t)
	f.Worktree("topic")
	busy := f.Worktree("busy")
	noGh(t)
	o := Options{Now: later()}
	s := gather(t, f)
	want := findRow(t, Classify(s, o), KindWorktree, "topic")

	f.DirtyIn(busy, "scratch.txt")
	sameRow(t, mustRescan(t, f, s, o, targetOf(want)), want)
}

// TestRescanMatchesGatherForEveryTarget builds a repository with a proven
// squash-merged worktree, a dirty worktree, an unmerged branch, a locked
// worktree, a detached worktree and a just-created branch (plus the primary
// and a remote row), then rescans every row of the full run. Why: Rescan
// reuses the per-worktree and per-ref pieces of Gather on a mini snapshot;
// any drift between the two paths (a missed proof input, the held-branch
// filter, the default-branch exclusion, the age) would make the re-check
// disagree with the list and refuse, or worse, pass a row the list did not.
func TestRescanMatchesGatherForEveryTarget(t *testing.T) {
	f := newFixture(t)
	merged := f.Worktree("merged")
	commitIn(t, merged, "m", "m", "m")
	gitIn(t, merged, "push", "-u", "origin", "merged")
	f.Squash("merged")
	f.DirtyIn(f.Worktree("dirty"), "scratch.txt")
	f.Branch("unmerged")
	f.Checkout("unmerged")
	f.Commit("u", "u", "u")
	f.Checkout("main")
	locked := f.Worktree("held")
	f.git("worktree", "lock", locked)
	f.DetachOrphan(f.Worktree("loose"))
	f.Branch("fresh")
	noGh(t)

	// Now is the real clock: "fresh" is minutes old, so the min-age rule
	// applies to it in both runs.
	o := Options{Now: time.Now()}
	s := gather(t, f)
	r := Classify(s, o)
	if len(r.Rows) < 8 {
		t.Fatalf("fixture gave only %d rows:%s", len(r.Rows), dumpRows(r.Rows))
	}
	statuses := map[Status]bool{}
	for _, want := range r.Rows {
		statuses[want.Status] = true
		sameRow(t, mustRescan(t, f, s, o, targetOf(want)), want)
	}
	for _, st := range []Status{StatusProven, StatusLessSure, StatusUniqueWork, StatusInUse} {
		if !statuses[st] {
			t.Errorf("fixture has no %s row, so that verdict was not compared:%s", st, dumpRows(r.Rows))
		}
	}
}

// TestRescanCostIsScoped counts the gate checks a rescan of one worktree
// runs. Why: the re-check runs once per item, immediately before acting; one
// that re-read every worktree's gates (status, ignored-file walk) would cost
// a full list per item, and the spec scopes it to the target.
func TestRescanCostIsScoped(t *testing.T) {
	f := newFixture(t)
	f.Worktree("topic")
	f.Worktree("other")
	f.Worktree("third")
	noGh(t)
	o := Options{Now: later()}
	s := gather(t, f)
	w := findRow(t, Classify(s, o), KindWorktree, "topic")

	var checked []string
	old := checkGates
	checkGates = func(g Git, wf WorktreeFact, in GateInputs) (Gates, error) {
		checked = append(checked, wf.Path)
		return old(g, wf, in)
	}
	t.Cleanup(func() { checkGates = old })
	mustRescan(t, f, s, o, targetOf(w))
	if len(checked) != 1 || resolve(checked[0]) != resolve(w.Path) {
		t.Fatalf("gates checked for %v, want only %s", checked, w.Path)
	}
}

// TestRescanBaseTipIsReResolved moves origin/main locally (no fetch: an
// update-ref, as another process's fetch would) between the list and the
// rescan. Why: the re-check must judge against the base as it is now; a
// fetch by another process between list and act that brings the branch into
// the base must be honoured, and one that moves the base away must not be
// hidden behind the listed tip.
func TestRescanBaseTipIsReResolved(t *testing.T) {
	f := newFixture(t)
	f.Branch("topic")
	f.Checkout("topic")
	tip := f.Commit("t", "t", "t")
	f.Push("topic")
	f.Checkout("main")
	noGh(t)
	o := Options{Now: later()}
	s := gather(t, f)
	before := findRow(t, Classify(s, o), KindBranch, "topic")
	if before.Evidence != "" {
		t.Fatalf("before the base moves the branch already has evidence %q", before.Evidence)
	}

	f.git("update-ref", "refs/remotes/origin/main", tip)
	row := mustRescan(t, f, s, o, targetOf(before))
	if row.Evidence != EvSameCommit || row.Status != StatusProven {
		t.Fatalf("rescanned row %s evidence %q, want proven by %q against the moved base", row.Status, row.Evidence, EvSameCommit)
	}
}

// TestRescanBaseGoneIsAnError deletes origin/main locally between the list
// and the rescan. Why: with no base nothing can be proven; the re-check must
// fail closed with an error (the removal refuses), never fall back to the
// listed base tip or call the target gone.
func TestRescanBaseGoneIsAnError(t *testing.T) {
	f := newFixture(t)
	f.Branch("topic")
	noGh(t)
	o := Options{Now: later()}
	s := gather(t, f)
	b := findRow(t, Classify(s, o), KindBranch, "topic")

	f.git("update-ref", "-d", "refs/remotes/origin/main")
	_, err := Rescan(Git{Dir: f.Dir}, s, rescanInputs(f), o, targetOf(b))
	if err == nil || errors.Is(err, ErrTargetGone) {
		t.Fatalf("Rescan with no base: error %v, want a non-gone error", err)
	}
	if !strings.Contains(err.Error(), "origin/main") {
		t.Fatalf("error %q does not name the base", err)
	}
}

// TestRescanPRTipStopsCountingWhenTipMoves proves a branch by its merged PR,
// then commits on it before the rescan. Why: the rescan reuses the listed PR
// facts (no gh call); a merged PR proves only the head it merged, so once the
// tip moves past it the PR must stop counting as evidence and the new commit
// must show as unique work.
func TestRescanPRTipStopsCountingWhenTipMoves(t *testing.T) {
	f := newFixture(t)
	tip, squash := squashThenEditSameLine(f)
	mergedPRStub(t, "topic", 7, tip, squash)
	o := Options{Now: later()}
	s := gather(t, f)
	before := findRow(t, Classify(s, o), KindBranch, "topic")
	if before.Evidence != EvPRTip || before.Status != StatusProven {
		t.Fatalf("listed row %s evidence %q, want proven by %q", before.Status, before.Evidence, EvPRTip)
	}

	f.Checkout("topic")
	f.Commit("y", "y", "after merge")
	f.Checkout("main")
	row := mustRescan(t, f, s, o, targetOf(before))
	if row.Evidence == EvPRTip || row.Status == StatusProven || !slices.Contains(reasonCodes(row.Reasons), ReasonPRHeadMoved) {
		t.Fatalf("rescanned row %s evidence %q reasons %v, want no pr-tip proof and %q", row.Status, row.Evidence, reasonCodes(row.Reasons), ReasonPRHeadMoved)
	}
}

// TestRescanUnreadableGatesIsAnUnknownRow deletes a listed worktree's
// directory (git still lists it, prunable) before the rescan. Why: a gate
// that cannot be read is Unknown, never clean; Rescan returns the row with
// gates_unavailable and GatesUnknown, exactly as the full run would, so the
// removal refuses on the row rather than on a lost error.
func TestRescanUnreadableGatesIsAnUnknownRow(t *testing.T) {
	f := newFixture(t)
	wt := f.Worktree("topic")
	noGh(t)
	o := Options{Now: later()}
	s := gather(t, f)
	w := findRow(t, Classify(s, o), KindWorktree, "topic")

	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	row := mustRescan(t, f, s, o, targetOf(w))
	if !row.Unknown || !row.Losses.GatesUnknown || row.Status == StatusProven || !slices.Contains(reasonCodes(row.Reasons), ReasonGatesUnavailable) {
		t.Fatalf("rescanned row %s unknown=%v losses %+v reasons %v, want Unknown with %q", row.Status, row.Unknown, row.Losses, reasonCodes(row.Reasons), ReasonGatesUnavailable)
	}
}

// TestRescanBranchNowCheckedOutIsAnError lists a free branch, then checks it
// out in a new worktree before the rescan. Why: a branch a worktree now holds
// has no branch row of its own (the full run would fold it into the
// worktree's); the re-check must refuse with an error, not report the
// branch gone or judge it as if nothing held it.
func TestRescanBranchNowCheckedOutIsAnError(t *testing.T) {
	f := newFixture(t)
	f.Branch("topic")
	noGh(t)
	o := Options{Now: later()}
	s := gather(t, f)
	b := findRow(t, Classify(s, o), KindBranch, "topic")

	f.git("worktree", "add", filepath.Join(filepath.Dir(f.Dir), "wt-topic"), "topic")
	_, err := Rescan(Git{Dir: f.Dir}, s, rescanInputs(f), o, targetOf(b))
	if err == nil || errors.Is(err, ErrTargetGone) || !strings.Contains(err.Error(), "checked out") {
		t.Fatalf("Rescan of a branch now checked out: error %v, want a \"checked out\" error", err)
	}
}
