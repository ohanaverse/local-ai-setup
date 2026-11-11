package cleanup

import (
	"bytes"
	"strings"
	"testing"
)

// foldSnap is a hand-built snapshot with the primary checkout on main and the
// given local heads and remote-tracking refs, every tip merged by ancestry.
func foldSnap(heads, remotes []RefFact) Snapshot {
	s := handSnapshot()
	s.Worktrees = []WorktreeState{{Fact: WorktreeFact{Path: "/r", Branch: "refs/heads/main", Primary: true}}}
	for i := range heads {
		if heads[i].Tip == "" {
			heads[i].Tip = strings.Repeat("a", 40)
		}
		heads[i].Created = old
		s.PRs[heads[i].Name] = PRResult{Lookup: LookupNone}
	}
	for i := range remotes {
		if remotes[i].Tip == "" {
			remotes[i].Tip = strings.Repeat("c", 40)
		}
		remotes[i].Created = old
	}
	s.Heads, s.Remotes = heads, remotes
	s.Proofs[strings.Repeat("a", 40)] = Proof{Evidence: EvAncestor, Verdict: Merged}
	s.Proofs[strings.Repeat("c", 40)] = Proof{Evidence: EvAncestor, Verdict: Merged}
	return s
}

func foldHead(name, upstream string) RefFact {
	h := RefFact{Ref: "refs/heads/" + name, Name: name, Upstream: upstream}
	if rest, ok := strings.CutPrefix(upstream, "refs/remotes/"); ok {
		h.Remote, _, _ = strings.Cut(rest, "/")
	}
	return h
}

func foldRemote(rm, name string) RefFact {
	return RefFact{Ref: "refs/remotes/" + rm + "/" + name, Name: name, Remote: rm}
}

// listedRemotes is the "<remote>/<name>" of every KindRemote row in rows.
func listedRemotes(rows []Row) []string {
	var out []string
	for _, r := range rows {
		if r.Kind == KindRemote {
			out = append(out, r.Remote+"/"+r.Name)
		}
	}
	return out
}

func wantUpstream(t *testing.T, row Row, ref string) {
	t.Helper()
	if row.Upstream == nil {
		t.Fatalf("%s %q: no Upstream, want %s", row.Kind, row.Name, ref)
	}
	if row.Upstream.Ref != ref || row.Upstream.Gone {
		t.Fatalf("%s %q: Upstream %+v, want present %s", row.Kind, row.Name, *row.Upstream, ref)
	}
}

// TestRemoteRefFoldsIntoSameNamedLocalBranch: origin/topic, the upstream of
// local topic, is not a row of its own but a field on topic's row; it stays
// classified in Folded and RemoteRows. A remote-only ref stays a row. Why:
// one row per worktree or branch (spec, "Row model and JSON").
func TestRemoteRefFoldsIntoSameNamedLocalBranch(t *testing.T) {
	s := foldSnap(
		[]RefFact{foldHead("topic", "refs/remotes/origin/topic")},
		[]RefFact{foldRemote("origin", "topic"), foldRemote("origin", "only")},
	)
	r := Classify(s, Options{Now: later()})
	if got := listedRemotes(r.Rows); len(got) != 1 || got[0] != "origin/only" {
		t.Fatalf("remote rows in Rows %v, want only origin/only", got)
	}
	if got := listedRemotes(r.Folded); len(got) != 1 || got[0] != "origin/topic" {
		t.Fatalf("Folded %v, want origin/topic", got)
	}
	if got := listedRemotes(r.RemoteRows()); len(got) != 2 {
		t.Fatalf("RemoteRows %v, want origin/only and origin/topic", got)
	}
	br := findRow(t, r, KindBranch, "topic")
	wantUpstream(t, br, "refs/remotes/origin/topic")
	folded := findRow(t, r, KindRemote, "topic")
	u := br.Upstream
	if u.Remote != "origin" || u.Name != "topic" || u.Tip != folded.Tip || u.Status != folded.Status || u.Evidence != folded.Evidence {
		t.Fatalf("Upstream %+v does not carry the folded row (%s %s %s)", *u, folded.Tip, folded.Status, folded.Evidence)
	}
	if only := findRow(t, r, KindRemote, "only"); only.Remote != "origin" {
		t.Fatalf("remote-only row %+v", only)
	}
}

// TestFoldFollowsTheHeadsUpstreamRemote: topic tracks fork, so fork/topic
// folds into it and origin/topic stays its own row.
func TestFoldFollowsTheHeadsUpstreamRemote(t *testing.T) {
	s := foldSnap(
		[]RefFact{foldHead("topic", "refs/remotes/fork/topic")},
		[]RefFact{foldRemote("origin", "topic"), foldRemote("fork", "topic")},
	)
	r := Classify(s, Options{Now: later()})
	if got := listedRemotes(r.Rows); len(got) != 1 || got[0] != "origin/topic" {
		t.Fatalf("remote rows in Rows %v, want origin/topic", got)
	}
	if got := listedRemotes(r.Folded); len(got) != 1 || got[0] != "fork/topic" {
		t.Fatalf("Folded %v, want fork/topic", got)
	}
	wantUpstream(t, findRow(t, r, KindBranch, "topic"), "refs/remotes/fork/topic")
}

// TestFoldFallsBackToOrigin: a head with no upstream folds origin/<name>,
// never another remote's same-named ref.
func TestFoldFallsBackToOrigin(t *testing.T) {
	s := foldSnap(
		[]RefFact{foldHead("topic", "")},
		[]RefFact{foldRemote("fork", "topic"), foldRemote("origin", "topic")},
	)
	r := Classify(s, Options{Now: later()})
	if got := listedRemotes(r.Rows); len(got) != 1 || got[0] != "fork/topic" {
		t.Fatalf("remote rows in Rows %v, want fork/topic", got)
	}
	if got := listedRemotes(r.Folded); len(got) != 1 || got[0] != "origin/topic" {
		t.Fatalf("Folded %v, want origin/topic", got)
	}
	u := findRow(t, r, KindBranch, "topic").Upstream
	if u == nil || u.Ref != "refs/remotes/origin/topic" || u.Remote != "origin" {
		t.Fatalf("Upstream %+v, want origin/topic", u)
	}
}

// TestGoneUpstreamIsAFieldNotARow: an upstream that no longer exists is
// Upstream{Gone}, with no remote row; it does not fall back to origin, whose
// same-named ref stays its own row.
func TestGoneUpstreamIsAFieldNotARow(t *testing.T) {
	gone := foldHead("topic", "refs/remotes/fork/topic")
	gone.Gone = true
	s := foldSnap([]RefFact{gone, func() RefFact { h := foldHead("lone", "refs/remotes/origin/lone"); h.Gone = true; return h }()},
		[]RefFact{foldRemote("origin", "topic")})
	r := Classify(s, Options{Now: later()})
	u := findRow(t, r, KindBranch, "topic").Upstream
	if u == nil || !u.Gone || u.Ref != "refs/remotes/fork/topic" || u.Remote != "fork" || u.Name != "topic" || u.Tip != "" {
		t.Fatalf("Upstream %+v, want gone fork/topic", u)
	}
	if u := findRow(t, r, KindBranch, "lone").Upstream; u == nil || !u.Gone || u.Ref != "refs/remotes/origin/lone" {
		t.Fatalf("lone Upstream %+v, want gone origin/lone", u)
	}
	if len(r.Folded) != 0 {
		t.Fatalf("Folded %v, want none", listedRemotes(r.Folded))
	}
	if got := listedRemotes(r.Rows); len(got) != 1 || got[0] != "origin/topic" {
		t.Fatalf("remote rows %v, want origin/topic alone", got)
	}
}

// TestFoldedWorktreeRowCarriesUpstream: a worktree on topic gets the same
// Upstream a branch row would, and origin/topic is folded.
func TestFoldedWorktreeRowCarriesUpstream(t *testing.T) {
	s := foldSnap(
		[]RefFact{foldHead("topic", "refs/remotes/origin/topic")},
		[]RefFact{foldRemote("origin", "topic")},
	)
	s.Worktrees = append(s.Worktrees, WorktreeState{Fact: WorktreeFact{Path: "/w/topic", Branch: "refs/heads/topic", Head: strings.Repeat("a", 40)}})
	r := Classify(s, Options{Now: later()})
	wantUpstream(t, findRow(t, r, KindWorktree, "topic"), "refs/remotes/origin/topic")
	if got := listedRemotes(r.Rows); len(got) != 0 {
		t.Fatalf("remote rows in Rows %v, want none", got)
	}
	if got := listedRemotes(r.Folded); len(got) != 1 {
		t.Fatalf("Folded %v, want origin/topic", got)
	}
	// The primary checkout on main, with no origin/main listed, has none.
	if u := findRow(t, r, KindWorktree, "main").Upstream; u != nil {
		t.Fatalf("main Upstream %+v, want nil", *u)
	}
}

// TestFoldSlashAndCaseBranchNames (Review Focus 3): a branch name with
// slashes folds its own remote ref; two local branches differing only by case
// each fold only the exactly-named ref, and a differently-cased name never
// folds another's ref.
func TestFoldSlashAndCaseBranchNames(t *testing.T) {
	s := foldSnap(
		[]RefFact{foldHead("feature/x", ""), foldHead("Topic", ""), foldHead("topic", "refs/remotes/origin/topic"), foldHead("Other", "")},
		[]RefFact{foldRemote("origin", "feature/x"), foldRemote("origin", "feature"), foldRemote("origin", "topic"), foldRemote("origin", "other")},
	)
	r := Classify(s, Options{Now: later()})
	wantUpstream(t, findRow(t, r, KindBranch, "feature/x"), "refs/remotes/origin/feature/x")
	wantUpstream(t, findRow(t, r, KindBranch, "topic"), "refs/remotes/origin/topic")
	if u := findRow(t, r, KindBranch, "Topic").Upstream; u != nil {
		t.Fatalf("Topic Upstream %+v, want nil (origin/topic belongs to topic)", *u)
	}
	if u := findRow(t, r, KindBranch, "Other").Upstream; u != nil {
		t.Fatalf("Other Upstream %+v, want nil (origin/other differs by case)", *u)
	}
	got := listedRemotes(r.Rows)
	if len(got) != 2 || got[0] != "origin/feature" || got[1] != "origin/other" {
		t.Fatalf("remote rows in Rows %v, want origin/feature and origin/other", got)
	}
	if got := listedRemotes(r.Folded); len(got) != 2 {
		t.Fatalf("Folded %v, want origin/feature/x and origin/topic", got)
	}
}

// TestFoldKeepsTheListedOutput: until the row model is rendered (Task 105+),
// Encode and Groups still list a folded remote ref as its own row, in the
// same place as before folding.
func TestFoldKeepsTheListedOutput(t *testing.T) {
	s := foldSnap(
		[]RefFact{foldHead("topic", "refs/remotes/origin/topic")},
		[]RefFact{foldRemote("origin", "topic"), foldRemote("origin", "only")},
	)
	r := Classify(s, Options{Now: later()})
	all := r.AllRows()
	if got := listedRemotes(all); len(got) != 2 || got[0] != "origin/only" || got[1] != "origin/topic" {
		t.Fatalf("AllRows remotes %v", got)
	}
	unfolded := r
	unfolded.Rows, unfolded.Folded = all, nil
	a, err := Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Encode(unfolded)
	if !bytes.Equal(a, b) {
		t.Fatalf("Encode differs once folded:\n%s\nvs\n%s", a, b)
	}
	n := 0
	for _, g := range Groups(r) {
		n += len(g.Rows)
	}
	if n != len(all) {
		t.Fatalf("Groups list %d rows, want %d", n, len(all))
	}
}
