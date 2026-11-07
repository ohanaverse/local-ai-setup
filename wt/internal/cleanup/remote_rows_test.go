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

// rowByRef is the row with the given full refname.
func rowByRef(t *testing.T, r Report, ref string) Row {
	t.Helper()
	for _, row := range r.Rows {
		if row.Ref == ref {
			return row
		}
	}
	t.Fatalf("no row %s", ref)
	return Row{}
}

// TestNonOriginRemoteRowKeepsItsVerdict classifies an upstream/topic row that
// sits beside origin/topic while origin's checks all fail (open PR targeting
// topic, origin not fetched). What: origin's row is in use / unsure, the
// upstream row carries none of origin's blockers and is less sure only
// because nothing asks gh about another remote (not_checked). Why: the base
// query and the origin fetch are origin facts only; a bare-name map must not
// carry origin's answer onto another remote's row, and a --no-fetch run must
// not turn an upstream row into origin's verdict. Its own PR state is unknown,
// never "no PR", so it never reads proven.
func TestNonOriginRemoteRowKeepsItsVerdict(t *testing.T) {
	open := PRInfo{Number: 12, State: "OPEN", HeadRefOid: strings.Repeat("e", 40), BaseRef: "topic"}
	s := remoteSnap(func(s *Snapshot, tip string) {
		s.Remotes = append(s.Remotes, RefFact{Ref: "refs/remotes/upstream/topic", Name: "topic", Remote: "upstream", Tip: tip, Created: old})
		s.BasePRs = map[string]PRResult{"topic": {Lookup: LookupKnown, PRs: []PRInfo{open}}}
		s.Fetch = FetchResult{} // --no-fetch
	})
	rep := Classify(s, Options{Now: later()})
	up := rowByRef(t, rep, "refs/remotes/upstream/topic")
	if up.Status != StatusLessSure || !slices.Contains(reasonCodes(up.Reasons), ReasonNotChecked) || len(up.Blockers) != 0 {
		t.Fatalf("upstream row %s unknown=%v reasons %v blockers %v, want less_sure by not_checked alone", up.Status, up.Unknown, reasonCodes(up.Reasons), reasonCodes(up.Blockers))
	}
	if og := rowByRef(t, rep, "refs/remotes/origin/topic"); og.Status == StatusProven {
		t.Fatalf("origin row stayed proven: %v", reasonCodes(og.Reasons))
	}
}

// TestNonOriginRowWithoutBaseAnswerIsNotUnknown: a lone upstream row has no
// BasePRs entry (only origin gets the query); it must not fail on that missing
// answer. What: it is less sure only for not_checked (no gh lookup exists for
// another remote) and has no blocker. Why: it must never read proven, because
// its pull-request state is unknown, and must never inherit origin's answer.
func TestNonOriginRowWithoutBaseAnswerIsNotUnknown(t *testing.T) {
	s := remoteSnap(func(s *Snapshot, tip string) {
		s.Remotes[0] = RefFact{Ref: "refs/remotes/upstream/topic", Name: "topic", Remote: "upstream", Tip: tip, Created: old}
		s.BasePRs = nil
	})
	row := rowByRef(t, Classify(s, Options{Now: later()}), "refs/remotes/upstream/topic")
	if row.Status != StatusLessSure || len(row.Blockers) != 0 || !slices.Contains(reasonCodes(row.Reasons), ReasonNotChecked) {
		t.Fatalf("got %s unknown=%v reasons %v blockers %v, want less_sure by not_checked alone", row.Status, row.Unknown, reasonCodes(row.Reasons), reasonCodes(row.Blockers))
	}
}

// TestNonOriginRowReadsNoBareNamePRs classifies an upstream/topic row beside a
// bare-name PR lookup that describes origin/topic: an open PR whose head is
// "topic", a merged one, and an open PR from "feat" whose base is "topic".
// What: the upstream row gets no open_pr or base_of_open_pr blocker and
// carries none of those PRs; the origin row still sees all of them. Why: gh is
// asked about the own repository, which origin is, so a PR found for
// origin/topic must not inform upstream/topic's weak reason or dependants.
func TestNonOriginRowReadsNoBareNamePRs(t *testing.T) {
	s := remoteSnap(func(s *Snapshot, tip string) {
		s.Remotes = append(s.Remotes, RefFact{Ref: "refs/remotes/upstream/topic", Name: "topic", Remote: "upstream", Tip: tip, Created: old})
		s.PRs["topic"] = PRResult{Lookup: LookupKnown, PRs: []PRInfo{
			{Number: 9, State: "MERGED", HeadRefOid: tip, BaseRef: "main", MergeCommit: strings.Repeat("d", 40)},
			{Number: 11, State: "OPEN", HeadRefOid: tip, BaseRef: "main"},
		}}
		s.PRs["feat"] = PRResult{Lookup: LookupKnown, PRs: []PRInfo{{Number: 12, State: "OPEN", HeadRefOid: strings.Repeat("e", 40), BaseRef: "topic"}}}
	})
	rep := Classify(s, Options{Now: later()})
	up := rowByRef(t, rep, "refs/remotes/upstream/topic")
	if len(up.Blockers) != 0 || up.PR != nil {
		t.Fatalf("upstream row read origin's PRs: blockers %v pr %+v", reasonCodes(up.Blockers), up.PR)
	}
	og := rowByRef(t, rep, "refs/remotes/origin/topic")
	if !slices.Contains(reasonCodes(og.Blockers), BlockOpenPR) || !slices.Contains(reasonCodes(og.Blockers), BlockBaseOfOpenPR) {
		t.Fatalf("origin row lost its blockers: %v", reasonCodes(og.Blockers))
	}
}
