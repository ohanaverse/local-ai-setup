package cleanup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// later is a clock an hour ahead: every branch the fixture made is older than
// DefaultMinAge by then, so the min-age rule stays out of a case's way.
func later() time.Time { return time.Now().Add(time.Hour) }

// gather runs Gather from the fixture's primary checkout with own repo o/r,
// without a fetch, and fails the test on an error.
func gather(t *testing.T, f *fixture) Snapshot {
	t.Helper()
	s, err := Gather(Git{Dir: f.Dir}, GatherInputs{CWD: f.Dir, OwnRepo: "o/r", Parallel: 2})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return s
}

// findRow returns the row of kind k named name, failing when there is not
// exactly one.
func findRow(t *testing.T, r Report, k Kind, name string) Row {
	t.Helper()
	var got []Row
	for _, row := range r.Rows {
		if row.Kind == k && row.Name == name {
			got = append(got, row)
		}
	}
	if len(got) != 1 {
		t.Fatalf("%d rows of kind %s named %q, want 1; rows: %s", len(got), k, name, dumpRows(r.Rows))
	}
	return got[0]
}

func dumpRows(rows []Row) string {
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "\n  %s %q path=%q branch=%q status=%s ev=%q unknown=%v reasons=%v blockers=%v",
			r.Kind, r.Name, r.Path, r.Branch, r.Status, r.Evidence, r.Unknown, reasonCodes(r.Reasons), reasonCodes(r.Blockers))
	}
	return b.String()
}

func reasonCodes(rs []Reason) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Code)
	}
	return out
}

// noGh stubs gh with an answered, empty listing for every branch.
func noGh(t *testing.T) {
	stubGh(t, func(string, string) ([]byte, error) { return []byte("[]"), nil })
}

// mergedPRStub stubs gh so branch has one own-repo MERGED PR #n into main
// with the given head and merge commit; every other branch has none.
func mergedPRStub(t *testing.T, branch string, n int, head, merge string) {
	t.Helper()
	body := mustJSON(t, []any{map[string]any{
		"number": n, "state": "MERGED", "headRefOid": head, "baseRefName": "main",
		"mergeCommit": map[string]any{"oid": merge}, "isCrossRepository": false,
		"headRepository": map[string]any{"name": "r", "nameWithOwner": "o/r"},
	}})
	stubGh(t, func(_, b string) ([]byte, error) {
		if b == branch {
			return body, nil
		}
		return []byte("[]"), nil
	})
}

// squashThenEditSameLine squash-merges topic (which changes x) into main,
// then edits the same line on main, so merge-tree conflicts and no git rung
// proves the merge: only a merged PR can. It returns topic's tip and the
// squash commit.
func squashThenEditSameLine(f *fixture) (tip, squash string) {
	f.Commit("x", "base\n", "base x")
	f.Push("main")
	f.Branch("topic")
	f.Checkout("topic")
	tip = f.Commit("x", "topic\n", "topic x")
	f.Push("topic")
	f.Squash("topic")
	squash = f.git("rev-parse", "HEAD")
	f.Commit("x", "later\n", "later x")
	f.Push("main")
	return tip, squash
}

// TestClassify runs Gather and Classify over one fixture per scenario ported
// from repo-cleanup's setup_test_repo and checks the target row's kind,
// status, evidence and reason codes. Why: each scenario is a way real repos
// look merged or not; a wrong status on any of them is a verdict a user would
// act on, and squash-then-revert in particular must never read as proven.
func TestClassify(t *testing.T) {
	type want struct {
		kind     Kind
		name     string
		status   Status
		ev       Evidence
		unknown  bool
		reasons  []string // each must be present
		blockers []string // each must be present
	}
	cases := []struct {
		name  string
		now   func() time.Time
		gh    func(t *testing.T, f *fixture) // nil: no PRs anywhere
		setup func(t *testing.T, f *fixture)
		want  want
	}{
		{name: "true merge", setup: func(t *testing.T, f *fixture) {
			f.Branch("topic")
			f.Checkout("topic")
			f.Commit("a", "a", "a")
			f.Checkout("main")
			f.Commit("m", "m", "m")
			f.Merge("topic")
			f.Push("main")
		}, want: want{kind: KindBranch, name: "topic", status: StatusProven, ev: EvAncestor}},
		{name: "fast-forward", setup: func(t *testing.T, f *fixture) {
			f.Branch("topic")
			f.Checkout("topic")
			f.Commit("a", "a", "a")
			f.Checkout("main")
			f.FastForward("topic")
			f.Push("main")
		}, want: want{kind: KindBranch, name: "topic", status: StatusProven, ev: EvSameCommit}},
		{name: "1:1 rebase", setup: func(t *testing.T, f *fixture) {
			f.Branch("topic")
			f.Checkout("topic")
			a := f.Commit("a", "a", "a")
			b := f.Commit("b", "b", "b")
			f.Checkout("main")
			f.Commit("m", "m", "m")
			f.CherryPick(a, b)
			f.Push("main")
		}, want: want{kind: KindBranch, name: "topic", status: StatusProven, ev: EvMergeAddsNothing}},
		{name: "multi-commit squash, main advanced, never pushed", setup: func(t *testing.T, f *fixture) {
			f.Branch("topic")
			f.Checkout("topic")
			f.Commit("a", "a", "a")
			f.Commit("b", "b", "b")
			f.Squash("topic")
			f.Commit("m", "m", "m")
			f.Push("main")
		}, want: want{kind: KindBranch, name: "topic", status: StatusProven, ev: EvMergeAddsNothing}},
		{name: "squash then revert", setup: func(t *testing.T, f *fixture) {
			f.Branch("topic")
			f.Checkout("topic")
			f.Commit("a", "a", "a")
			f.Push("topic")
			f.Squash("topic")
			f.Revert("HEAD")
			f.Push("main")
		}, want: want{kind: KindBranch, name: "topic", status: StatusLessSure, reasons: []string{ReasonWeakSignal}}},
		{name: "squash, then main edits the same line, merged PR at the tip", gh: func(t *testing.T, f *fixture) {
			tip := f.git("rev-parse", "refs/heads/topic")
			mergedPRStub(t, "topic", 7, tip, f.git("rev-parse", "HEAD~1"))
		}, setup: func(t *testing.T, f *fixture) {
			squashThenEditSameLine(f)
		}, want: want{kind: KindBranch, name: "topic", status: StatusProven, ev: EvPRTip}},
		{name: "reused branch name with an old merged PR", gh: func(t *testing.T, f *fixture) {
			mergedPRStub(t, "topic", 5, strings.Repeat("1", 40), f.git("rev-parse", "main"))
		}, setup: func(t *testing.T, f *fixture) {
			f.Branch("topic")
			f.Checkout("topic")
			f.Commit("n", "new", "new work")
			f.Push("topic")
			f.Checkout("main")
		}, want: want{kind: KindBranch, name: "topic", status: StatusUniqueWork, reasons: []string{ReasonPRHeadMoved}}},
		{name: "fresh empty branch", now: time.Now, setup: func(t *testing.T, f *fixture) {
			f.Branch("topic")
		}, want: want{kind: KindBranch, name: "topic", status: StatusLessSure, ev: EvSameCommit, reasons: []string{ReasonFreshBranch}}},
		{name: "detached with unique commits", setup: func(t *testing.T, f *fixture) {
			p := f.Worktree("topic")
			f.DetachOrphan(p)
		}, want: want{kind: KindWorktree, name: "wt-topic", status: StatusUniqueWork, reasons: []string{ReasonDetachedUnique}}},
		{name: "dirty", setup: func(t *testing.T, f *fixture) {
			p := f.Worktree("topic")
			f.DirtyIn(p, "scratch.txt")
		}, want: want{kind: KindWorktree, name: "topic", status: StatusUniqueWork, ev: EvSameCommit, reasons: []string{ReasonDirty}}},
		{name: "ignored only", setup: func(t *testing.T, f *fixture) {
			p := f.Worktree("topic")
			f.IgnoredIn(p, ".env")
		}, want: want{kind: KindWorktree, name: "topic", status: StatusLessSure, ev: EvSameCommit, reasons: []string{ReasonIgnoredFiles}}},
		{name: "locked", setup: func(t *testing.T, f *fixture) {
			p := f.Worktree("topic")
			f.git("worktree", "lock", p)
		}, want: want{kind: KindWorktree, name: "topic", status: StatusInUse, ev: EvSameCommit, blockers: []string{BlockLocked}}},
		{name: "nested", setup: func(t *testing.T, f *fixture) {
			p := f.Worktree("topic")
			f.NestedWorktree(p, "inner")
		}, want: want{kind: KindWorktree, name: "topic", status: StatusInUse, ev: EvSameCommit, blockers: []string{BlockNested}}},
		{name: "merge commit with unique content", setup: func(t *testing.T, f *fixture) {
			f.Commit("x", "base\n", "base x")
			f.Push("main")
			f.Branch("topic")
			f.Checkout("topic")
			x := f.Commit("x", "topic\n", "topic x")
			f.Checkout("main")
			f.Commit("m", "m", "m")
			f.CherryPick(x)
			f.Commit("x", "main\n", "main x")
			f.Push("main")
			f.Checkout("topic")
			f.MergeResolving("main", "x", "resolved\n")
			f.Checkout("main")
		}, want: want{kind: KindBranch, name: "topic", status: StatusUniqueWork, reasons: []string{ReasonUnpushed}}},
		{name: "git failure is unknown", setup: func(t *testing.T, f *fixture) {
			f.Branch("topic")
			f.Checkout("topic")
			f.Commit("a", "a", "a")
			f.Push("topic")
			f.Checkout("main")
			f.git("config", "merge.keep.driver", "true")
		}, want: want{kind: KindBranch, name: "topic", status: StatusLessSure, unknown: true, reasons: []string{ReasonMergeProofUnavailable}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			c.setup(t, f)
			if c.gh != nil {
				c.gh(t, f)
			} else {
				noGh(t)
			}
			now := later
			if c.now != nil {
				now = c.now
			}
			r := Classify(gather(t, f), Options{Now: now(), MinAge: DefaultMinAge})
			row := findRow(t, r, c.want.kind, c.want.name)
			w := c.want
			if row.Status != w.status || row.Evidence != w.ev || row.Unknown != w.unknown {
				t.Fatalf("row = (%s, ev %q, unknown %v), want (%s, ev %q, unknown %v)%s",
					row.Status, row.Evidence, row.Unknown, w.status, w.ev, w.unknown, dumpRows(r.Rows))
			}
			for _, code := range w.reasons {
				if !slices.Contains(reasonCodes(row.Reasons), code) {
					t.Errorf("reasons %v lack %q", reasonCodes(row.Reasons), code)
				}
			}
			for _, code := range w.blockers {
				if !slices.Contains(reasonCodes(row.Blockers), code) {
					t.Errorf("blockers %v lack %q", reasonCodes(row.Blockers), code)
				}
			}
			for _, row := range r.Rows {
				if row.Kind != KindWorktree && row.Name == "main" {
					t.Errorf("default branch listed as a %s row%s", row.Kind, dumpRows(r.Rows))
				}
			}
			// The root on main is in use for two reasons, and nothing else.
			root := findRow(t, r, KindWorktree, "main")
			if root.Status != StatusInUse || !slices.Contains(reasonCodes(root.Blockers), BlockPrimary) ||
				!slices.Contains(reasonCodes(root.Blockers), BlockDefaultBranch) || len(root.Reasons) != 0 {
				t.Errorf("root row %s blockers %v reasons %v, want in_use (primary, default_branch), no reasons",
					root.Status, reasonCodes(root.Blockers), reasonCodes(root.Reasons))
			}
		})
	}
}

// TestClassifyNoDefaultBranchAllUnknown builds a repository with no
// origin/HEAD, main, master, origin/main or origin/master. Why: with no base
// every merge check is meaningless; Gather must say so (ErrNoDefaultBranch)
// and every row must be Unknown, none proven, so no guard is silently skipped.
func TestClassifyNoDefaultBranchAllUnknown(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	if b, err := exec.Command("git", "init", "-b", "trunk", dir).CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, b)
	}
	gitIn(t, dir, "config", "user.email", "t@example.com")
	gitIn(t, dir, "config", "user.name", "T")
	gitIn(t, dir, "config", "commit.gpgsign", "false")
	gitIn(t, dir, "commit", "--allow-empty", "-m", "init")
	gitIn(t, dir, "branch", "topic")
	noGh(t)

	s, err := Gather(Git{Dir: dir}, GatherInputs{CWD: dir, OwnRepo: "o/r"})
	if !errors.Is(err, ErrNoDefaultBranch) {
		t.Fatalf("Gather err = %v, want ErrNoDefaultBranch", err)
	}
	r := Classify(s, Options{Now: later()})
	if len(r.Rows) < 2 {
		t.Fatalf("want the root and topic rows, got %s", dumpRows(r.Rows))
	}
	for _, row := range r.Rows {
		if !row.Unknown || row.Status == StatusProven || !slices.Contains(reasonCodes(row.Reasons), ReasonNoDefaultBranch) {
			t.Errorf("row %s %q: status %s unknown %v reasons %v; want Unknown with %q, never proven",
				row.Kind, row.Name, row.Status, row.Unknown, reasonCodes(row.Reasons), ReasonNoDefaultBranch)
		}
	}
	if topic := findRow(t, r, KindBranch, "topic"); topic.Status != StatusLessSure {
		t.Errorf("topic status %s, want %s", topic.Status, StatusLessSure)
	}
}

// TestClassifyUnknownPRIsNeverProven makes a squash that only a merged PR can
// prove (main later edits the same line, so merge-tree conflicts) and makes
// gh fail. Why: a failed PR query must not read as "proof" or as "no PR"; the
// row is less sure with Unknown set and the cause named.
func TestClassifyUnknownPRIsNeverProven(t *testing.T) {
	f := newFixture(t)
	squashThenEditSameLine(f)
	stubGh(t, func(string, string) ([]byte, error) { return nil, errors.New("gh: rate limited") })

	r := Classify(gather(t, f), Options{Now: later()})
	row := findRow(t, r, KindBranch, "topic")
	if row.Status != StatusLessSure || !row.Unknown || row.Evidence != "" {
		t.Fatalf("row = (%s, unknown %v, ev %q), want less_sure, unknown, no evidence%s", row.Status, row.Unknown, row.Evidence, dumpRows(r.Rows))
	}
	if !slices.Contains(reasonCodes(row.Reasons), ReasonPRUnknown) {
		t.Fatalf("reasons %v lack %q", reasonCodes(row.Reasons), ReasonPRUnknown)
	}
	if r.GH != GHUnavailable {
		t.Fatalf("GH = %q, want %q", r.GH, GHUnavailable)
	}
}

// TestClassifyDirtyMergedIsUniqueWork modifies a tracked file in a worktree
// whose branch is proven merged. Why: the uncommitted edit exists nowhere
// else; a merged branch must not carry it into PROVEN.
func TestClassifyDirtyMergedIsUniqueWork(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	if err := os.WriteFile(filepath.Join(p, "README"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	noGh(t)
	r := Classify(gather(t, f), Options{Now: later()})
	row := findRow(t, r, KindWorktree, "topic")
	if row.Status != StatusUniqueWork || row.Evidence != EvSameCommit || !slices.Contains(reasonCodes(row.Reasons), ReasonDirty) {
		t.Fatalf("row = %s%s", row.Status, dumpRows(r.Rows))
	}
}

// TestClassifyFreshBranchNotProven makes a branch on the base commit and
// classifies it now, an hour later, and with no known creation time. Why: a
// brand-new branch looks merged (it is the base); only age tells it from a
// fast-forwarded one, and an unknown age must not count as old.
func TestClassifyFreshBranchNotProven(t *testing.T) {
	f := newFixture(t)
	f.Branch("topic")
	noGh(t)
	s := gather(t, f)

	if row := findRow(t, Classify(s, Options{Now: time.Now()}), KindBranch, "topic"); row.Status != StatusLessSure ||
		!slices.Contains(reasonCodes(row.Reasons), ReasonFreshBranch) {
		t.Fatalf("now: %s %v, want less_sure with %q", row.Status, reasonCodes(row.Reasons), ReasonFreshBranch)
	}
	if row := findRow(t, Classify(s, Options{Now: later()}), KindBranch, "topic"); row.Status != StatusProven {
		t.Fatalf("an hour later: %s %v, want proven", row.Status, reasonCodes(row.Reasons))
	}
	for i := range s.Heads {
		s.Heads[i].Created = time.Time{}
		s.Heads[i].TipTime = time.Time{}
	}
	if row := findRow(t, Classify(s, Options{Now: later()}), KindBranch, "topic"); row.Status != StatusLessSure {
		t.Fatalf("unknown age: %s, want less_sure", row.Status)
	}
}

// TestRowsMapWorktreeToBranchByPorcelain puts topic/x and topic-x in two
// worktrees whose directories share the basename topic-x; topic/x is merged,
// topic-x has an unpushed commit. Why: a mapping by directory name would give
// one worktree the other's branch and verdict; the porcelain list is the only
// truth, and neither checked-out branch may also get its own branch row.
func TestRowsMapWorktreeToBranchByPorcelain(t *testing.T) {
	f := newFixture(t)
	root := filepath.Dir(f.Dir)
	a := filepath.Join(root, "a", "topic-x")
	b := filepath.Join(root, "b", "topic-x")
	f.git("worktree", "add", "-b", "topic/x", a)
	f.git("worktree", "add", "-b", "topic-x", b)
	commitIn(t, b, "y", "y", "unique")
	noGh(t)

	r := Classify(gather(t, f), Options{Now: later()})
	byPath := map[string]Row{}
	for _, row := range r.Rows {
		if row.Kind == KindBranch && (row.Name == "topic/x" || row.Name == "topic-x") {
			t.Errorf("checked-out branch %q has its own branch row", row.Name)
		}
		if row.Kind == KindWorktree {
			byPath[resolve(row.Path)] = row
		}
	}
	ra, rb := byPath[resolve(a)], byPath[resolve(b)]
	if ra.Branch != "topic/x" || ra.Status != StatusProven {
		t.Errorf("worktree a: branch %q status %s, want topic/x proven%s", ra.Branch, ra.Status, dumpRows(r.Rows))
	}
	if rb.Branch != "topic-x" || rb.Status != StatusUniqueWork || !slices.Contains(reasonCodes(rb.Reasons), ReasonUnpushed) {
		t.Errorf("worktree b: branch %q status %s, want topic-x unique_work (unpushed)%s", rb.Branch, rb.Status, dumpRows(r.Rows))
	}
}

// handSnapshot is a resolved base with one known commit, for pure Classify
// tests that need no git.
func handSnapshot() Snapshot {
	return Snapshot{
		Repo:    "/r",
		Base:    Base{Default: "main", Ref: "origin/main"},
		BaseTip: strings.Repeat("b", 40),
		Proofs:  map[string]Proof{},
		PRs:     map[string]PRResult{},
		Merges:  map[string]Tri{},
	}
}

// TestClassifyOrderIsInUseFirst stacks every status's cause on one merged
// worktree and peels them off one at a time. Why: the precedence is first
// match wins, in use, then unique work, then less sure, then proven; a
// locked dirty worktree read as merely "unique" or a dirty one read as "less
// sure" offers the user a removal the spec forbids.
func TestClassifyOrderIsInUseFirst(t *testing.T) {
	tip := strings.Repeat("c", 40)
	build := func(g Gates) Snapshot {
		s := handSnapshot()
		s.Heads = []RefFact{{Ref: "refs/heads/topic", Name: "topic", Tip: tip}}
		s.Proofs[tip] = Proof{Evidence: EvAncestor, Verdict: Merged}
		s.PRs["topic"] = PRResult{Lookup: LookupNone}
		s.Worktrees = []WorktreeState{
			{Fact: WorktreeFact{Path: "/r", Branch: "refs/heads/main", Primary: true}, Gates: Gates{Blockers: []Reason{{BlockPrimary, "/r"}}}},
			{Fact: WorktreeFact{Path: "/w/topic", Branch: "refs/heads/topic", Head: tip}, Gates: g},
		}
		return s
	}
	locked := []Reason{{BlockLocked, "/w/topic"}}
	cases := []struct {
		name  string
		gates Gates
		want  Status
	}{
		{"locked, dirty, ignored", Gates{Blockers: locked, Dirty: true, IgnoredCount: 1}, StatusInUse},
		{"dirty, ignored", Gates{Dirty: true, IgnoredCount: 1}, StatusUniqueWork},
		{"ignored", Gates{IgnoredCount: 1}, StatusLessSure},
		{"clean", Gates{}, StatusProven},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Classify(build(c.gates), Options{Now: later()})
			if row := findRow(t, r, KindWorktree, "topic"); row.Status != c.want {
				t.Fatalf("status %s, want %s%s", row.Status, c.want, dumpRows(r.Rows))
			}
		})
	}
}

// TestClassifyRemoteRowRules checks the remote row rules on hand-built
// snapshots. Why: deleting an origin branch is visible to everyone and can
// close a dependent PR, so a remote row is proven only with a merged PR, no
// open PR on it or based on it, content in the base, and a complete gh answer.
func TestClassifyRemoteRowRules(t *testing.T) {
	tip := strings.Repeat("c", 40)
	merged := PRInfo{Number: 9, State: "MERGED", HeadRefOid: tip, BaseRef: "main", MergeCommit: strings.Repeat("d", 40)}
	build := func(topic PRResult, others map[string]PRResult) Snapshot {
		s := handSnapshot()
		s.Worktrees = []WorktreeState{{Fact: WorktreeFact{Path: "/r", Branch: "refs/heads/main", Primary: true}}}
		s.Remotes = []RefFact{{Ref: "refs/remotes/origin/topic", Name: "topic", Remote: "origin", Tip: tip}}
		s.Proofs[tip] = Proof{Evidence: EvAncestor, Verdict: Merged}
		s.PRs["topic"] = topic
		for k, v := range others {
			s.PRs[k] = v
		}
		return s
	}
	cases := []struct {
		name    string
		s       Snapshot
		status  Status
		unknown bool
		code    string // a reason or blocker the row must carry
	}{
		{"merged PR, no open PR", build(PRResult{Lookup: LookupKnown, PRs: []PRInfo{merged}}, nil), StatusProven, false, ""},
		{"merged into main without a PR", build(PRResult{Lookup: LookupNone}, nil), StatusLessSure, false, ReasonNoMergedPR},
		{"open PR uses it as base", build(PRResult{Lookup: LookupKnown, PRs: []PRInfo{merged}}, map[string]PRResult{
			"child": {Lookup: LookupKnown, PRs: []PRInfo{{Number: 12, State: "OPEN", HeadRefOid: strings.Repeat("e", 40), BaseRef: "topic"}}},
		}), StatusInUse, false, BlockBaseOfOpenPR},
		{"open PR on the head", build(PRResult{Lookup: LookupKnown, PRs: []PRInfo{merged, {Number: 11, State: "OPEN", HeadRefOid: tip, BaseRef: "main"}}}, nil), StatusInUse, false, BlockOpenPR},
		{"gh unavailable", build(PRResult{Lookup: LookupUnknown, Err: "boom"}, nil), StatusLessSure, true, ReasonCannotVerify},
		{"gh partial", build(PRResult{Lookup: LookupKnown, PRs: []PRInfo{merged}}, map[string]PRResult{
			"other": {Lookup: LookupUnknown, Err: "boom"},
		}), StatusLessSure, true, ReasonCannotVerify},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Classify(c.s, Options{Now: later()})
			row := findRow(t, r, KindRemote, "topic")
			if row.Status != c.status || row.Unknown != c.unknown || row.Remote != "origin" {
				t.Fatalf("row = (%s, unknown %v, remote %q), want (%s, unknown %v, origin)%s", row.Status, row.Unknown, row.Remote, c.status, c.unknown, dumpRows(r.Rows))
			}
			if c.code != "" && !slices.Contains(append(reasonCodes(row.Reasons), reasonCodes(row.Blockers)...), c.code) {
				t.Fatalf("reasons %v blockers %v lack %q", reasonCodes(row.Reasons), reasonCodes(row.Blockers), c.code)
			}
		})
	}
}

// TestClassifyGHState checks Report.GH from the lookups. Why: the header must
// say when gh answered for none or only some branches, never show "ok" then.
func TestClassifyGHState(t *testing.T) {
	cases := []struct {
		prs  map[string]PRResult
		want GHState
	}{
		{map[string]PRResult{"a": {Lookup: LookupNone}, "b": {Lookup: LookupKnown}}, GHOK},
		{map[string]PRResult{"a": {Lookup: LookupNone}, "b": {Lookup: LookupUnknown}}, GHPartial},
		{map[string]PRResult{"a": {Lookup: LookupUnknown}, "b": {Lookup: LookupUnknown}}, GHUnavailable},
	}
	for _, c := range cases {
		s := handSnapshot()
		s.PRs = c.prs
		if got := Classify(s, Options{Now: later()}).GH; got != c.want {
			t.Errorf("GH(%v) = %q, want %q", c.prs, got, c.want)
		}
	}
}
