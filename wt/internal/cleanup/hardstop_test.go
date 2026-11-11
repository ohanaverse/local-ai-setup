package cleanup

import (
	"strings"
	"testing"
)

// TestHardStopCodesAreExactlyThese pins which blocker codes refuse a removal
// outright. Why: held and default_branch used to be hard blockers; they are
// now verdict floors the user may accept, and a stray addition here would
// make a consumer refuse (or allow) something the spec says otherwise.
func TestHardStopCodesAreExactlyThese(t *testing.T) {
	hard := map[string]bool{
		BlockPrimary: true, BlockCurrent: true, BlockLocked: true, BlockNested: true,
		BlockOperation: true, BlockProtectedArtifact: true, BlockOpenPR: true, BlockBaseOfOpenPR: true,
	}
	all := []string{BlockPrimary, BlockCurrent, BlockHeld, BlockLocked, BlockNested, BlockOperation,
		BlockProtectedArtifact, BlockDefaultBranch, BlockOpenPR, BlockBaseOfOpenPR}
	for _, code := range all {
		for _, k := range []Kind{KindWorktree, KindBranch, KindRemote} {
			if got := IsHardStop(code, k); got != hard[code] {
				t.Errorf("IsHardStop(%q, %s) = %v, want %v", code, k, got, hard[code])
			}
		}
	}
	if IsHardStop("made_up", KindWorktree) {
		t.Error("an unknown code must not be a hard stop")
	}
}

func hardStopSnapshot(g Gates, fact WorktreeFact) (Snapshot, string) {
	tip := strings.Repeat("c", 40)
	s := handSnapshot()
	s.Heads = []RefFact{{Ref: "refs/heads/topic", Name: "topic", Tip: tip, Created: old}}
	s.Proofs[tip] = Proof{Evidence: EvAncestor, Verdict: Merged}
	s.PRs["topic"] = PRResult{Lookup: LookupNone}
	fact.Path, fact.Branch, fact.Head = "/w/topic", "refs/heads/topic", tip
	s.Worktrees = []WorktreeState{
		{Fact: WorktreeFact{Path: "/r", Branch: "refs/heads/main", Primary: true}, Gates: Gates{Blockers: []Reason{{BlockPrimary, "/r"}}}},
		{Fact: fact, Gates: g},
	}
	return s, tip
}

func TestHeldLinkedWorktreeIsLessSureNotInUse(t *testing.T) {
	s, _ := hardStopSnapshot(Gates{Blockers: []Reason{{BlockHeld, "/w/topic"}}}, WorktreeFact{})
	r := Classify(s, Options{Now: later()})
	row := findRow(t, r, KindWorktree, "topic")
	if row.Status != StatusLessSure || row.HardStop != "" {
		t.Fatalf("status %s hardstop %q, want less_sure and none%s", row.Status, row.HardStop, dumpRows(r.Rows))
	}
	if !strings.Contains(strings.Join(reasonCodes(row.Blockers), ","), BlockHeld) {
		t.Fatalf("blockers %v, want held kept", reasonCodes(row.Blockers))
	}
	if row.Evidence != EvAncestor {
		t.Fatalf("evidence %q, want the proof kept", row.Evidence)
	}
}

func TestPrimaryAndCurrentRowsKeepAHardStop(t *testing.T) {
	s, _ := hardStopSnapshot(Gates{Blockers: []Reason{{BlockCurrent, "/w/topic"}}}, WorktreeFact{Current: true})
	r := Classify(s, Options{Now: later()})
	root := findRow(t, r, KindWorktree, "main")
	if root.Status != StatusInUse || root.HardStop != BlockPrimary || !root.Primary || root.Current {
		t.Fatalf("root: %s %q primary=%v current=%v", root.Status, root.HardStop, root.Primary, root.Current)
	}
	cur := findRow(t, r, KindWorktree, "topic")
	if cur.Status != StatusInUse || cur.HardStop != BlockCurrent || !cur.Current || cur.Primary {
		t.Fatalf("current: %s %q primary=%v current=%v", cur.Status, cur.HardStop, cur.Primary, cur.Current)
	}
}

func TestDefaultBranchWorktreeIsAFloorNotAHardStop(t *testing.T) {
	s := handSnapshot()
	tip := strings.Repeat("c", 40)
	s.Heads = []RefFact{{Ref: "refs/heads/main", Name: "main", Tip: tip, Created: old}}
	s.Proofs[tip] = Proof{Evidence: EvSameCommit, Verdict: Merged}
	s.PRs["main"] = PRResult{Lookup: LookupNone}
	s.Worktrees = []WorktreeState{
		{Fact: WorktreeFact{Path: "/w/main", Branch: "refs/heads/main", Head: tip}},
	}
	r := Classify(s, Options{Now: later()})
	row := findRow(t, r, KindWorktree, "main")
	if row.Status != StatusLessSure || row.HardStop != "" || !strings.Contains(strings.Join(reasonCodes(row.Blockers), ","), BlockDefaultBranch) {
		t.Fatalf("status %s hardstop %q blockers %v, want less_sure, none, default_branch", row.Status, row.HardStop, reasonCodes(row.Blockers))
	}
}

func TestRowsThatCanNeverBeProven(t *testing.T) {
	tip := strings.Repeat("c", 40)
	cases := map[string]Row{}
	{
		s, _ := hardStopSnapshot(Gates{Blockers: []Reason{{BlockHeld, "/w/topic"}}}, WorktreeFact{})
		r := Classify(s, Options{Now: later()})
		cases["primary"] = findRow(t, r, KindWorktree, "main")
		cases["held"] = findRow(t, r, KindWorktree, "topic")
	}
	{
		s, _ := hardStopSnapshot(Gates{Blockers: []Reason{{BlockCurrent, "/w/topic"}}}, WorktreeFact{Current: true})
		cases["current"] = findRow(t, Classify(s, Options{Now: later()}), KindWorktree, "topic")
	}
	{
		s := handSnapshot()
		s.Heads = []RefFact{{Ref: "refs/heads/main", Name: "main", Tip: tip, Created: old}}
		s.Proofs[tip] = Proof{Evidence: EvSameCommit, Verdict: Merged}
		s.PRs["main"] = PRResult{Lookup: LookupNone}
		s.Worktrees = []WorktreeState{{Fact: WorktreeFact{Path: "/w/main", Branch: "refs/heads/main", Head: tip}}}
		cases["default-branch worktree"] = findRow(t, Classify(s, Options{Now: later()}), KindWorktree, "main")
	}
	{
		// Gather does not list default branches yet, so build the row by hand.
		s := handSnapshot()
		s.Heads = []RefFact{{Ref: "refs/heads/main", Name: "main", Tip: tip, Created: old}}
		s.Proofs[tip] = Proof{Evidence: EvSameCommit, Verdict: Merged}
		s.PRs["main"] = PRResult{Lookup: LookupNone}
		cases["default-branch branch row"] = findRow(t, Classify(s, Options{Now: later()}), KindBranch, "main")
	}
	for name, row := range cases {
		if row.Status == StatusProven || row.Tickable() {
			t.Errorf("%s: status %s tickable %v, want neither proven nor tickable", name, row.Status, row.Tickable())
		}
	}
}
