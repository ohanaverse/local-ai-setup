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
	return GatherInputs{CWD: f.Dir, OwnRepo: "o/r", Parallel: 2, OriginRows: true}
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
// (by hand, in the test) before rescanning each. Why: something removed between the list and the act is not a row to
// judge; ErrTargetGone lets the removal report it as already gone instead of
// acting on a stale row or calling it a git failure.
func TestRescanGoneTarget(t *testing.T) {
	f := newFixture(t)
	f.Worktree("topic")
	f.Branch("solo")
	noGh(t)
	o := Options{Now: later()}
	s := gather(t, f)
	r := Classify(s, o)
	w := findRow(t, r, KindWorktree, "topic")
	b := findRow(t, r, KindBranch, "solo")

	f.git("worktree", "remove", w.Path)
	f.git("branch", "-D", "solo")
	for _, row := range []Row{w, b} {
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
// showed, or a verdict the user saw would not be the one acted on.
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
// worktree, a detached worktree, a young detached worktree at the base tip
// (min-age reads its HEAD's reflog time) and a just-created branch (plus the
// primary), then rescans every worktree and branch row of the full run
// (remote rows are not rescannable in PR B). Why: Rescan reuses the
// per-worktree and per-ref pieces of Gather on a mini snapshot; any drift
// between the two paths (a missed proof input, the held-branch filter, the
// default-branch exclusion, the age, a detached HEAD's time) would make the
// re-check disagree with the list and refuse, or worse, pass a row the list
// did not.
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
	f.git("worktree", "add", "--detach", filepath.Join(filepath.Dir(f.Dir), "wt-at-base"), "refs/remotes/origin/main")
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
		if want.Kind == KindRemote {
			continue
		}
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

// TestRescanWorktreeIdentityIsEnforced lists a worktree on topic and a
// detached worktree, then switches the first to another branch and puts the
// second on a new branch before rescanning both; a listed branch left alone
// is rescanned too. Why: a worktree target is its path AND the branch (or
// detached HEAD) the user approved; a fresh row for a different branch would
// carry that branch's Ref and Tip, and a removal acting on it would delete a
// branch nobody approved. Rescan must refuse, saying what changed, while an
// unchanged target still rescans.
func TestRescanWorktreeIdentityIsEnforced(t *testing.T) {
	f := newFixture(t)
	wt := f.Worktree("topic")
	det := f.Worktree("loose")
	f.DetachOrphan(det)
	f.Branch("solo")
	noGh(t)
	o := Options{Now: later()}
	s := gather(t, f)
	r := Classify(s, o)
	w := findRow(t, r, KindWorktree, "topic")
	d := findRow(t, r, KindWorktree, "wt-loose")
	b := findRow(t, r, KindBranch, "solo")

	gitIn(t, wt, "switch", "-c", "other")
	gitIn(t, det, "switch", "-c", "later")
	for _, c := range []struct {
		row  Row
		want string
	}{{w, "now on refs/heads/other (was refs/heads/topic)"}, {d, "now on refs/heads/later (was detached)"}} {
		_, err := Rescan(Git{Dir: f.Dir}, s, rescanInputs(f), o, targetOf(c.row))
		if err == nil || errors.Is(err, ErrTargetGone) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Rescan(%s): error %v, want one saying %q", c.row.Name, err, c.want)
		}
	}
	if row := mustRescan(t, f, s, o, targetOf(b)); row.Ref != b.Ref || row.Tip != b.Tip {
		t.Errorf("unchanged branch rescanned as %q %q, want %q %q", row.Ref, row.Tip, b.Ref, b.Tip)
	}
}

// TestRescanMergesFollowTheMovedBase lists a branch whose merged PR's merge
// commit is in the base, then rewinds the base locally below the merge; and
// the other way round. Why: the PR rung is part of the ladder; with the base
// tip re-resolved, a merge-commit answer reused from the list would be about
// the old base, keeping a proof the current base no longer gives, or missing
// one it now gives.
func TestRescanMergesFollowTheMovedBase(t *testing.T) {
	t.Run("rewound", func(t *testing.T) {
		f := newFixture(t)
		tip, squash := squashThenEditSameLine(f)
		mergedPRStub(t, "topic", 7, tip, squash)
		o := Options{Now: later()}
		s := gather(t, f)
		before := findRow(t, Classify(s, o), KindBranch, "topic")
		if before.Evidence != EvPRTip {
			t.Fatalf("listed evidence %q, want %q", before.Evidence, EvPRTip)
		}
		f.git("update-ref", "refs/remotes/origin/main", squash+"^")
		row := mustRescan(t, f, s, o, targetOf(before))
		if row.Evidence == EvPRTip || row.Status == StatusProven {
			t.Fatalf("rescanned row %s evidence %q after the base lost the merge, want no pr-tip proof", row.Status, row.Evidence)
		}
	})
	t.Run("advanced", func(t *testing.T) {
		f := newFixture(t)
		tip, squash := squashThenEditSameLine(f)
		mergedPRStub(t, "topic", 7, tip, squash)
		head := f.git("rev-parse", "refs/remotes/origin/main")
		f.git("update-ref", "refs/remotes/origin/main", squash+"^")
		o := Options{Now: later()}
		s := gather(t, f)
		before := findRow(t, Classify(s, o), KindBranch, "topic")
		if before.Evidence == EvPRTip {
			t.Fatalf("listed evidence %q before the base holds the merge", before.Evidence)
		}
		f.git("update-ref", "refs/remotes/origin/main", head)
		row := mustRescan(t, f, s, o, targetOf(before))
		if row.Evidence != EvPRTip || row.Status != StatusProven {
			t.Fatalf("rescanned row %s evidence %q after the base gained the merge, want proven by %q", row.Status, row.Evidence, EvPRTip)
		}
	})
}

// TestRescanRejectsMalformedTarget rescans branch targets whose Ref is not a
// refs/heads/ name (a short name, an option-looking value, a tag, a remote
// ref), a remote target, and a branch name that is a symbolic ref. Why: Ref
// reaches git's argv and names what a removal deletes; anything but a local
// branch is a caller error, never "gone" (which reads as nothing to do), and
// a symbolic ref exists but is not a branch with its own tip.
func TestRescanRejectsMalformedTarget(t *testing.T) {
	f := newFixture(t)
	f.Branch("topic")
	f.Push("topic")
	f.git("symbolic-ref", "refs/heads/alias", "refs/heads/topic")
	noGh(t)
	o := Options{Now: later()}
	s := gather(t, f)
	for _, tg := range []Target{
		{Kind: KindBranch, Ref: "topic"},
		{Kind: KindBranch, Ref: "--all"},
		{Kind: KindBranch, Ref: "refs/tags/v1"},
		{Kind: KindBranch, Ref: "refs/remotes/origin/topic"},
		{Kind: KindRemote, Ref: "origin/topic"},
		{Kind: KindRemote, Ref: "refs/heads/topic"},
		{Kind: KindBranch, Ref: "refs/heads/alias"},
	} {
		_, err := Rescan(Git{Dir: f.Dir}, s, rescanInputs(f), o, tg)
		if err == nil || errors.Is(err, ErrTargetGone) {
			t.Errorf("Rescan(%+v): error %v, want a plain error", tg, err)
		}
	}
}

// remoteFixture is a repo whose origin branch "topic" is merged (a merged PR
// at its tip), listed with a clean fetch.
func remoteFixture(t *testing.T) (*fixture, Snapshot, Options, Target) {
	t.Helper()
	f := newFixture(t)
	f.Branch("topic")
	f.Push("topic")
	tip := f.git("rev-parse", "main")
	mergedPRStub(t, "topic", 8, tip, tip)
	stubGhBase(t, func(string, string) ([]byte, error) { return []byte("[]"), nil })
	in := rescanInputs(f)
	in.Fetch = true
	s, err := Gather(Git{Dir: f.Dir}, in)
	if err != nil {
		t.Fatal(err)
	}
	o := Options{Now: later()}
	row := findRow(t, Classify(s, o), KindRemote, "topic")
	if row.Status != StatusProven {
		t.Fatalf("fixture row is %s, want proven%s", row.Status, dumpRows(Classify(s, o).Rows))
	}
	return f, s, o, Target{Kind: KindRemote, Ref: row.Ref}
}

// TestRescanRemoteRunsGhLiveForThatBranchOnly rescans an origin row and counts
// the gh calls. What: exactly one head lookup and one base lookup, both for
// the target, and the result equals the full run's row. Why: the PR facts of
// the first gather are exactly what made the stacked-PR check blind; the
// delete is public, so its re-check asks gh again (one pair per item), while a
// worktree or branch rescan still runs none.
func TestRescanRemoteRunsGhLiveForThatBranchOnly(t *testing.T) {
	f, s, o, tg := remoteFixture(t)
	var heads, bases []string
	stubGh(t, func(_, b string) ([]byte, error) {
		heads = append(heads, b)
		tip := f.git("rev-parse", "main")
		return mustJSON(t, []any{map[string]any{"number": 8, "state": "MERGED", "headRefOid": tip, "baseRefName": "main",
			"mergeCommit": map[string]any{"oid": tip}, "isCrossRepository": false, "headRepository": map[string]any{"name": "r", "nameWithOwner": "o/r"}}}), nil
	})
	stubGhBase(t, func(_, b string) ([]byte, error) { bases = append(bases, b); return []byte("[]"), nil })
	row := mustRescan(t, f, s, o, tg)
	if row.Status != StatusProven || !slices.Equal(heads, []string{"topic"}) || !slices.Equal(bases, []string{"topic"}) {
		t.Fatalf("row %s, head calls %v, base calls %v", row.Status, heads, bases)
	}
	heads, bases = nil, nil
	mustRescan(t, f, s, o, Target{Kind: KindBranch, Ref: "refs/heads/topic"})
	f.Branch("loc")
	mustRescan(t, f, s, o, Target{Kind: KindBranch, Ref: "refs/heads/loc"})
	if len(heads)+len(bases) != 0 {
		t.Fatalf("a branch rescan ran gh: heads %v bases %v", heads, bases)
	}
}

// TestRescanRemoteSeesAPROpenedAfterTheList opens a dependent PR (base query)
// after the list was built. What: the fresh row is in_use/base_of_open_pr.
// Why: a PR opened in the window between the list and the delete would be
// closed by the delete; the rescan must see everything gh can tell.
func TestRescanRemoteSeesAPROpenedAfterTheList(t *testing.T) {
	f, s, o, tg := remoteFixture(t)
	stubGhBase(t, func(string, string) ([]byte, error) {
		return []byte(`[{"number":40,"headRefName":"c","isCrossRepository":false}]`), nil
	})
	row := mustRescan(t, f, s, o, tg)
	if row.Status != StatusInUse || !slices.Contains(reasonCodes(row.Blockers), BlockBaseOfOpenPR) {
		t.Fatalf("row %s %v", row.Status, reasonCodes(row.Blockers))
	}
}

// TestRescanRemoteHeadMoved answers the head lookup with a merged PR for a
// different head. What: the row is less_sure/pr_head_moved. Why: only the PR's
// exact head proves that the merge covers what the delete throws away.
func TestRescanRemoteHeadMoved(t *testing.T) {
	f, s, o, tg := remoteFixture(t)
	tip := f.git("rev-parse", "main")
	mergedPRStub(t, "topic", 8, strings.Repeat("9", 40), tip)
	row := mustRescan(t, f, s, o, tg)
	if row.Status != StatusLessSure || !slices.Contains(reasonCodes(row.Reasons), ReasonPRHeadMoved) {
		t.Fatalf("row %s %v", row.Status, reasonCodes(row.Reasons))
	}
}

// TestRescanRemoteGoneAndDefault rescans a vanished tracking ref and the
// default branch's. What: gone is ErrTargetGone; the default branch is a plain
// error that says so. Why: a vanished ref must read "skipped: no longer
// exists", never "is the default branch" (the old wording for any empty set).
func TestRescanRemoteGoneAndDefault(t *testing.T) {
	f, s, o, tg := remoteFixture(t)
	if _, err := Rescan(Git{Dir: f.Dir}, s, rescanInputs(f), o, Target{Kind: KindRemote, Ref: "refs/remotes/origin/main"}); err == nil ||
		errors.Is(err, ErrTargetGone) || !strings.Contains(err.Error(), "default branch") {
		t.Fatalf("default branch: %v", err)
	}
	f.git("update-ref", "-d", "refs/remotes/origin/topic")
	if _, err := Rescan(Git{Dir: f.Dir}, s, rescanInputs(f), o, tg); !errors.Is(err, ErrTargetGone) {
		t.Fatalf("vanished ref: %v, want ErrTargetGone", err)
	}
}
