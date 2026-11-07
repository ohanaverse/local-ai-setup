package cleanup

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// remoteSnap is a hand-built snapshot with one origin branch "topic" whose
// content is in the base, a clean origin fetch, an answered base query with no
// dependants, and a merged PR into main whose head is the tip and whose merge
// commit is in the base: a proven row. Each case edits one thing.
func remoteSnap(edit func(s *Snapshot, tip string)) Snapshot {
	tip := strings.Repeat("c", 40)
	merge := strings.Repeat("d", 40)
	s := handSnapshot()
	s.Worktrees = []WorktreeState{{Fact: WorktreeFact{Path: "/r", Branch: "refs/heads/main", Primary: true}}}
	s.Remotes = []RefFact{{Ref: "refs/remotes/origin/topic", Name: "topic", Remote: "origin", Tip: tip, Created: old}}
	s.Proofs[tip] = Proof{Evidence: EvTreesMatch, Verdict: Merged}
	s.PRs["topic"] = PRResult{Lookup: LookupKnown, PRs: []PRInfo{{Number: 9, State: "MERGED", HeadRefOid: tip, BaseRef: "main", MergeCommit: merge}}}
	s.Merges[merge] = TriYes
	s.BasePRs = map[string]PRResult{"topic": {Lookup: LookupNone}}
	s.Fetch = FetchResult{OK: true, At: time.Now()}
	if edit != nil {
		edit(&s, tip)
	}
	return s
}

// TestRemoteRowNeedsACleanOriginFetch classifies an otherwise proven origin
// row under each fetch outcome. What: only a clean origin fetch of this run
// keeps it proven; a failed, skipped (--no-fetch) or origin-less fetch makes
// it less_sure with cannot_verify. Why: a PR opened against this branch since
// the last fetch is invisible without the fetch (the base_of_open_pr blind
// spot), and the delete is public and irreversible by this tool.
func TestRemoteRowNeedsACleanOriginFetch(t *testing.T) {
	cases := []struct {
		name  string
		fetch FetchResult
		want  Status
	}{
		{"ok", FetchResult{OK: true, At: time.Now()}, StatusProven},
		{"failed", FetchResult{Err: "no network", At: time.Now()}, StatusLessSure},
		{"not run (--no-fetch)", FetchResult{}, StatusLessSure},
		{"no origin", FetchResult{Skipped: "no origin remote"}, StatusLessSure},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := remoteSnap(func(s *Snapshot, _ string) { s.Fetch = c.fetch })
			row := findRow(t, Classify(s, Options{Now: later()}), KindRemote, "topic")
			if row.Status != c.want {
				t.Fatalf("status %s, want %s (reasons %v)", row.Status, c.want, reasonCodes(row.Reasons))
			}
			if c.want != StatusProven && (!row.Unknown || !slices.Contains(reasonCodes(row.Reasons), ReasonCannotVerify)) {
				t.Fatalf("want unknown with %s, got unknown=%v %v", ReasonCannotVerify, row.Unknown, reasonCodes(row.Reasons))
			}
		})
	}
	// Local rows are not affected by the origin fetch.
	s := handSnapshot()
	tip := strings.Repeat("a", 40)
	s.Worktrees = []WorktreeState{{Fact: WorktreeFact{Path: "/r", Branch: "refs/heads/main", Primary: true}}}
	s.Heads = []RefFact{{Ref: "refs/heads/topic", Name: "topic", Tip: tip, Created: old}}
	s.Proofs[tip] = Proof{Evidence: EvAncestor, Verdict: Merged}
	s.PRs["topic"] = PRResult{Lookup: LookupNone}
	if row := findRow(t, Classify(s, Options{Now: later()}), KindBranch, "topic"); row.Status != StatusProven {
		t.Fatalf("local branch with no fetch: %s, want proven", row.Status)
	}
}

// TestRemoteRowBaseQuery varies the live "open PRs based on this branch"
// answer. What: a dependent PR blocks (even a fork's, from a branch never
// fetched), an unknown or missing answer is cannot_verify. Why: head lookups
// only cover branches already in the refs, so a dependent PR whose head lives
// only on origin was invisible; the delete would close it.
func TestRemoteRowBaseQuery(t *testing.T) {
	open := PRInfo{Number: 12, State: "OPEN", HeadRefOid: strings.Repeat("e", 40), BaseRef: "topic"}
	cases := []struct {
		name    string
		base    map[string]PRResult
		status  Status
		unknown bool
		code    string
	}{
		{"none", map[string]PRResult{"topic": {Lookup: LookupNone}}, StatusProven, false, ""},
		{"dependent PR from an unfetched branch", map[string]PRResult{"topic": {Lookup: LookupKnown, PRs: []PRInfo{open}}}, StatusInUse, false, BlockBaseOfOpenPR},
		{"query failed", map[string]PRResult{"topic": {Lookup: LookupUnknown, Err: "boom"}}, StatusLessSure, true, ReasonCannotVerify},
		{"query missing", nil, StatusLessSure, true, ReasonCannotVerify},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := remoteSnap(func(s *Snapshot, _ string) { s.BasePRs = c.base })
			row := findRow(t, Classify(s, Options{Now: later()}), KindRemote, "topic")
			if row.Status != c.status || row.Unknown != c.unknown {
				t.Fatalf("got (%s, unknown %v), want (%s, %v)%s", row.Status, row.Unknown, c.status, c.unknown, reasonCodes(row.Reasons))
			}
			if c.code != "" && !slices.Contains(append(reasonCodes(row.Reasons), reasonCodes(row.Blockers)...), c.code) {
				t.Fatalf("lacks %s: %v %v", c.code, reasonCodes(row.Reasons), reasonCodes(row.Blockers))
			}
		})
	}
}

// TestRemoteRowPRRungMatchesTheEngine varies the merged PR. What: proven
// needs a MERGED PR into the default branch whose head is the tip and whose
// merge commit is in the base. Why: the removal engine re-verifies exactly
// that before pushing; the list must not show PROVEN rows it will refuse.
func TestRemoteRowPRRungMatchesTheEngine(t *testing.T) {
	merge := strings.Repeat("d", 40)
	cases := []struct {
		name string
		edit func(s *Snapshot, tip string)
		want Status
		code string
	}{
		{"head moved after the merge", func(s *Snapshot, tip string) {
			s.PRs["topic"] = PRResult{Lookup: LookupKnown, PRs: []PRInfo{{Number: 9, State: "MERGED", HeadRefOid: strings.Repeat("9", 40), BaseRef: "main", MergeCommit: merge}}}
		}, StatusLessSure, ReasonPRHeadMoved},
		{"merge commit not in the base", func(s *Snapshot, tip string) { s.Merges[merge] = TriNo }, StatusLessSure, ReasonNoMergedPR},
		{"merge commit unknown", func(s *Snapshot, tip string) { delete(s.Merges, merge) }, StatusLessSure, ReasonPRMergeUnknown},
		{"merged into another branch", func(s *Snapshot, tip string) {
			s.PRs["topic"] = PRResult{Lookup: LookupKnown, PRs: []PRInfo{{Number: 9, State: "MERGED", HeadRefOid: tip, BaseRef: "release", MergeCommit: merge}}}
		}, StatusLessSure, ReasonNoMergedPR},
		{"two merged PRs, one matches", func(s *Snapshot, tip string) {
			s.PRs["topic"] = PRResult{Lookup: LookupKnown, PRs: []PRInfo{
				{Number: 8, State: "MERGED", HeadRefOid: strings.Repeat("9", 40), BaseRef: "main", MergeCommit: strings.Repeat("8", 40)},
				{Number: 9, State: "MERGED", HeadRefOid: tip, BaseRef: "main", MergeCommit: merge}}}
		}, StatusProven, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			row := findRow(t, Classify(remoteSnap(c.edit), Options{Now: later()}), KindRemote, "topic")
			if row.Status != c.want {
				t.Fatalf("status %s, want %s (%v)", row.Status, c.want, reasonCodes(row.Reasons))
			}
			if c.code != "" && !slices.Contains(reasonCodes(row.Reasons), c.code) {
				t.Fatalf("reasons %v lack %s", reasonCodes(row.Reasons), c.code)
			}
		})
	}
}
