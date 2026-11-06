package cleanup

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// TestClassifyFillsRefTipHeadLosses gathers a repository with a dirty
// worktree holding two ignored entries, a local branch with three unpushed
// commits, a pushed branch (so a remote row) and a detached worktree, and
// checks the removal facts each row carries. Why: the removal engine deletes
// by the exact full refname and compares the tip and HEAD it was shown
// against what git says at removal time; a short name, an abbreviated sha or
// a missing loss would let it delete the wrong thing or hide what is lost.
func TestClassifyFillsRefTipHeadLosses(t *testing.T) {
	f := newFixture(t)
	wt := f.Worktree("topic")
	f.DirtyIn(wt, "scratch.txt")
	f.IgnoredIn(wt, ".env")
	f.IgnoredIn(wt, ".cache")

	f.Branch("solo")
	f.Checkout("solo")
	f.Commit("a", "a", "a")
	f.Commit("b", "b", "b")
	f.Commit("c", "c", "c")
	f.Checkout("main")

	f.Branch("shared")
	f.Push("shared")

	det := f.Worktree("loose")
	head := f.DetachOrphan(det)
	noGh(t)
	r := Classify(gather(t, f), Options{Now: later()})

	w := findRow(t, r, KindWorktree, "topic")
	topicTip := f.git("rev-parse", "refs/heads/topic")
	if w.Ref != "refs/heads/topic" || w.Tip != topicTip || w.Head != topicTip || !fullCommitID.MatchString(w.Tip) {
		t.Errorf("worktree ref/tip/head = %q %q %q, want refs/heads/topic and %s twice", w.Ref, w.Tip, w.Head, topicTip)
	}
	if want := (Losses{Dirty: true, IgnoredEntries: 2, Unpushed: 0}); w.Losses.Dirty != want.Dirty ||
		w.Losses.IgnoredEntries != want.IgnoredEntries || w.Losses.IgnoredBytes <= 0 || w.Losses.Unpushed != 0 || w.Losses.DetachedUnique {
		t.Errorf("worktree losses = %+v, want dirty, 2 ignored entries with bytes, nothing unpushed", w.Losses)
	}

	b := findRow(t, r, KindBranch, "solo")
	if b.Ref != "refs/heads/solo" || b.Tip != f.git("rev-parse", "refs/heads/solo") || b.Head != "" {
		t.Errorf("branch ref/tip/head = %q %q %q", b.Ref, b.Tip, b.Head)
	}
	if b.Losses != (Losses{Unpushed: 3}) {
		t.Errorf("branch losses = %+v, want 3 unpushed only", b.Losses)
	}

	rm := findRow(t, r, KindRemote, "shared")
	if rm.Ref != "refs/remotes/origin/shared" || rm.Tip != f.git("rev-parse", "refs/remotes/origin/shared") || rm.Head != "" {
		t.Errorf("remote ref/tip/head = %q %q %q", rm.Ref, rm.Tip, rm.Head)
	}
	if rm.Losses != (Losses{}) {
		t.Errorf("remote losses = %+v, want none", rm.Losses)
	}

	d := findRow(t, r, KindWorktree, "wt-loose")
	if d.Ref != "" || d.Tip != "" || d.Head != head {
		t.Errorf("detached ref/tip/head = %q %q %q, want \"\" \"\" %s", d.Ref, d.Tip, d.Head, head)
	}
	if !d.Losses.DetachedUnique {
		t.Errorf("detached losses = %+v, want DetachedUnique", d.Losses)
	}
}

// TestClassifyUnknownUnpushedIsMinusOne classifies a branch whose unpushed
// count failed, and one whose tip has no proof at all. Why: Losses.Unpushed
// 0 means "nothing only here"; an unknown count read as 0 would let a removal
// say nothing is lost when it could not tell.
func TestClassifyUnknownUnpushedIsMinusOne(t *testing.T) {
	tip := strings.Repeat("c", 40)
	s := handSnapshot()
	s.Worktrees = []WorktreeState{{Fact: WorktreeFact{Path: "/r", Branch: "refs/heads/main", Primary: true}}}
	s.Heads = []RefFact{
		{Ref: "refs/heads/topic", Name: "topic", Tip: tip, Created: old},
		{Ref: "refs/heads/noproof", Name: "noproof", Tip: strings.Repeat("e", 40), Created: old},
	}
	s.Proofs[tip] = Proof{Verdict: NotMerged, Unpushed: -1, UnpushedErr: "boom"}
	r := Classify(s, Options{Now: later()})
	for _, name := range []string{"topic", "noproof"} {
		if got := findRow(t, r, KindBranch, name).Losses.Unpushed; got != -1 {
			t.Errorf("%s: Losses.Unpushed = %d, want -1", name, got)
		}
	}
}

// TestClassifyWorktreeRefIsForEachRefSpelling builds snapshots whose worktree
// spells its branch differently from the head for-each-ref listed, one with
// a slashed name, and one whose branch has no head at all. Why: the removal
// engine deletes the branch with a compare-and-swap on Row.Ref; on a
// case-insensitive volume the worktree's spelling is not the ref git holds,
// and a branch with no head has no tip to compare, so its unknowns must stay
// unknown.
func TestClassifyWorktreeRefIsForEachRefSpelling(t *testing.T) {
	tip := strings.Repeat("c", 40)
	primary := WorktreeState{Fact: WorktreeFact{Path: "/r", Branch: "refs/heads/main", Primary: true}}
	build := func(wtBranch string, heads ...RefFact) Snapshot {
		s := handSnapshot()
		s.Worktrees = []WorktreeState{primary, {Fact: WorktreeFact{Path: "/w/x", Branch: wtBranch, Head: tip}}}
		s.Heads = heads
		s.Proofs[tip] = Proof{Evidence: EvMergeAddsNothing, Verdict: Merged}
		for _, h := range heads {
			s.PRs[h.Name] = PRResult{Lookup: LookupNone}
		}
		return s
	}
	cases := []struct {
		name, wtBranch string
		heads          []RefFact
		rowName        string
		wantRef        string
		wantTip        string
		wantUnpushed   int
		wantProven     bool
	}{
		{"case differs", "refs/heads/Topic", []RefFact{{Ref: "refs/heads/topic", Name: "topic", Tip: tip, Created: old}},
			"Topic", "refs/heads/topic", tip, 0, true},
		{"slashed name, case differs", "refs/heads/Feature/Topic", []RefFact{{Ref: "refs/heads/feature/topic", Name: "feature/topic", Tip: tip, Created: old}},
			"Feature/Topic", "refs/heads/feature/topic", tip, 0, true},
		{"no matching head", "refs/heads/ghost", nil, "ghost", "refs/heads/ghost", "", -1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Classify(build(c.wtBranch, c.heads...), Options{Now: later()})
			row := findRow(t, r, KindWorktree, c.rowName)
			if row.Ref != c.wantRef || row.Tip != c.wantTip || row.Losses.Unpushed != c.wantUnpushed || row.BranchProven != c.wantProven {
				t.Fatalf("row ref %q tip %q unpushed %d branchProven %v, want %q %q %d %v%s",
					row.Ref, row.Tip, row.Losses.Unpushed, row.BranchProven, c.wantRef, c.wantTip, c.wantUnpushed, c.wantProven, dumpRows(r.Rows))
			}
		})
	}
}

// TestClassifyGatesUnknownLoss classifies a worktree whose gates failed to
// read and one whose gates read clean. Why: with a gates error Dirty and the
// ignored counts are zero only because nothing was read; the removal approval
// must see "unknown", not "nothing to lose", or a possibly dirty worktree
// would be removed without --force.
func TestClassifyGatesUnknownLoss(t *testing.T) {
	tip := strings.Repeat("c", 40)
	build := func(gatesErr string) Snapshot {
		s := handSnapshot()
		s.Heads = []RefFact{{Ref: "refs/heads/topic", Name: "topic", Tip: tip, Created: old}}
		s.Proofs[tip] = Proof{Evidence: EvMergeAddsNothing, Verdict: Merged}
		s.PRs["topic"] = PRResult{Lookup: LookupNone}
		s.Worktrees = []WorktreeState{
			{Fact: WorktreeFact{Path: "/r", Branch: "refs/heads/main", Primary: true}},
			{Fact: WorktreeFact{Path: "/w/topic", Branch: "refs/heads/topic", Head: tip}, GatesErr: gatesErr},
		}
		return s
	}
	row := findRow(t, Classify(build("boom"), Options{Now: later()}), KindWorktree, "topic")
	if !row.Losses.GatesUnknown {
		t.Errorf("gates error: Losses = %+v, want GatesUnknown", row.Losses)
	}
	if !slices.Contains(LossLines(row), "could not read the worktree's state; uncommitted or ignored files may be lost") {
		t.Errorf("gates error: LossLines = %q, want the could-not-read line", LossLines(row))
	}
	row = findRow(t, Classify(build(""), Options{Now: later()}), KindWorktree, "topic")
	if row.Losses != (Losses{}) || len(LossLines(row)) != 0 {
		t.Errorf("clean gates: Losses = %+v, LossLines = %q, want none", row.Losses, LossLines(row))
	}
}

// TestClassifyBranchProven checks Row.BranchProven on hand-built snapshots.
// Why (Ruling A): a worktree removal keeps the branch unless the branch alone
// is proven; deciding from the worktree's combined status would keep a
// squash-merged branch only because its worktree was dirty, and deciding
// from anything looser would delete an unmerged branch.
func TestClassifyBranchProven(t *testing.T) {
	tip := strings.Repeat("c", 40)
	primary := WorktreeState{Fact: WorktreeFact{Path: "/r", Branch: "refs/heads/main", Primary: true}}
	snap := func(p Proof, created time.Time, wt *Gates) Snapshot {
		s := handSnapshot()
		s.Worktrees = []WorktreeState{primary}
		s.Heads = []RefFact{{Ref: "refs/heads/topic", Name: "topic", Tip: tip, Created: created, TipTime: created}}
		s.Proofs[tip] = p
		s.PRs["topic"] = PRResult{Lookup: LookupNone}
		if wt != nil {
			s.Worktrees = append(s.Worktrees, WorktreeState{Fact: WorktreeFact{Path: "/w/topic", Branch: "refs/heads/topic", Head: tip}, Gates: *wt})
		}
		return s
	}
	squashed := Proof{Evidence: EvMergeAddsNothing, Verdict: Merged}
	sameCommit := Proof{Evidence: EvSameCommit, Verdict: Merged}
	netZero := Proof{Evidence: EvNoAddedChanges, Verdict: Merged, NetZero: TriYes, Unpushed: 2}
	cases := []struct {
		name       string
		s          Snapshot
		now        time.Time
		kind       Kind
		rowName    string
		wantStatus Status
		want       bool
	}{
		{"dirty worktree on a squash-merged branch", snap(squashed, old, &Gates{Dirty: true, IgnoredCount: 1, IgnoredBytes: 5}),
			later(), KindWorktree, "topic", StatusUniqueWork, true},
		{"locked worktree on a squash-merged branch", snap(squashed, old, &Gates{Blockers: []Reason{{BlockLocked, "/w/topic"}}}),
			later(), KindWorktree, "topic", StatusInUse, true},
		{"clean worktree on an unmerged branch", snap(Proof{Verdict: NotMerged}, old, &Gates{}),
			later(), KindWorktree, "topic", StatusLessSure, false},
		{"worktree on a fresh branch", snap(sameCommit, time.Now(), &Gates{}),
			time.Now(), KindWorktree, "topic", StatusLessSure, false},
		{"worktree on a net-zero unpushed branch", snap(netZero, old, &Gates{}),
			later(), KindWorktree, "topic", StatusLessSure, false},
		{"proven branch row", snap(squashed, old, nil), later(), KindBranch, "topic", StatusProven, true},
		{"net-zero unpushed branch row", snap(netZero, old, nil), later(), KindBranch, "topic", StatusLessSure, false},
		{"fresh branch row", snap(sameCommit, time.Now(), nil), time.Now(), KindBranch, "topic", StatusLessSure, false},
		{"branch row on a local fallback base", func() Snapshot {
			s := snap(squashed, old, nil)
			s.Base = Base{Default: "main", Ref: "main", Fallback: true}
			return s
		}(), later(), KindBranch, "topic", StatusLessSure, false},
		{"worktree on a local fallback base", func() Snapshot {
			s := snap(squashed, old, &Gates{})
			s.Base = Base{Default: "main", Ref: "main", Fallback: true}
			return s
		}(), later(), KindWorktree, "topic", StatusLessSure, false},
		{"proven remote row", func() Snapshot {
			s := handSnapshot()
			s.Worktrees = []WorktreeState{primary}
			s.Remotes = []RefFact{{Ref: "refs/remotes/origin/topic", Name: "topic", Remote: "origin", Tip: tip, Created: old}}
			s.Proofs[tip] = squashed
			s.PRs["topic"] = PRResult{Lookup: LookupKnown, PRs: []PRInfo{{Number: 9, State: "MERGED", HeadRefOid: tip, BaseRef: "main"}}}
			return s
		}(), later(), KindRemote, "topic", StatusProven, false},
		{"detached worktree with a proven HEAD", func() Snapshot {
			s := handSnapshot()
			s.Worktrees = []WorktreeState{primary, {Fact: WorktreeFact{Path: "/w/loose", Head: tip, Detached: true}}}
			s.Proofs[tip] = squashed
			return s
		}(), later(), KindWorktree, "loose", StatusProven, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Classify(c.s, Options{Now: c.now})
			row := findRow(t, r, c.kind, c.rowName)
			if row.Status != c.wantStatus || row.BranchProven != c.want {
				t.Fatalf("row = (%s, BranchProven %v), want (%s, %v)%s", row.Status, row.BranchProven, c.wantStatus, c.want, dumpRows(r.Rows))
			}
		})
	}
}

// TestLossLinesWording pins the sentences a removal confirmation shows for
// what would be lost: singular and plural, sizes in human units (never raw
// bytes), unknowns said as unknown, and nothing at all for a clean row. Why:
// the confirmation is the user's last look before data is deleted; "1
// commits", a byte count or a silent unknown would misstate the loss.
func TestLossLinesWording(t *testing.T) {
	cases := []struct {
		name string
		row  Row
		want []string
	}{
		{"clean worktree", Row{Kind: KindWorktree}, nil},
		{"clean branch", Row{Kind: KindBranch}, nil},
		{"dirty", Row{Losses: Losses{Dirty: true}}, []string{"uncommitted changes will be lost"}},
		{"one ignored entry", Row{Losses: Losses{IgnoredEntries: 1, IgnoredBytes: 1}}, []string{"1 ignored entry, 1 B will be deleted"}},
		{"ignored entries in MB", Row{Losses: Losses{IgnoredEntries: 2, IgnoredBytes: 2_400_000}}, []string{"2 ignored entries, 2 MB will be deleted"}},
		{"ignored entries of no size", Row{Losses: Losses{IgnoredEntries: 3}}, []string{"3 ignored entries will be deleted"}},
		{"one unpushed", Row{Losses: Losses{Unpushed: 1}}, []string{"1 commit exists only on this machine"}},
		{"three unpushed", Row{Losses: Losses{Unpushed: 3}}, []string{"3 commits exist only on this machine"}},
		{"unpushed unknown", Row{Losses: Losses{Unpushed: -1}}, []string{"could not count the commits that exist only on this machine; some may be lost"}},
		{"detached", Row{Losses: Losses{DetachedUnique: true}}, []string{"the detached HEAD's commits are on no branch and will be lost"}},
		{"gates unreadable", Row{Losses: Losses{GatesUnknown: true}},
			[]string{"could not read the worktree's state; uncommitted or ignored files may be lost"}},
		{"everything, in order", Row{Losses: Losses{Dirty: true, IgnoredEntries: 2, IgnoredBytes: 1_300_000_000, Unpushed: 2, DetachedUnique: true}}, []string{
			"uncommitted changes will be lost",
			"2 ignored entries, 1.3 GB will be deleted",
			"2 commits exist only on this machine",
			"the detached HEAD's commits are on no branch and will be lost",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := LossLines(c.row)
			if len(got) != len(c.want) || strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Fatalf("LossLines = %q, want %q", got, c.want)
			}
		})
	}
}
