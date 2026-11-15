package cleanup

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestEveryCodeHasWording fails when any Reason*/Block* string constant in
// this package has no entry in the wording table. It reads the package's own
// source, so a code added later without a sentence fails here instead of
// reaching the user as a bare code.
func TestEveryCodeHasWording(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, spec := range gd.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if !strings.HasPrefix(name.Name, "Reason") && !strings.HasPrefix(name.Name, "Block") {
							continue
						}
						if i >= len(vs.Values) {
							continue
						}
						lit, ok := vs.Values[i].(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						code, _ := strconv.Unquote(lit.Value)
						seen++
						if reasonWording[code] == "" {
							t.Errorf("%s (%q) has no wording in reasonWording", name.Name, code)
						}
					}
				}
			}
		}
	}
	if seen < 20 {
		t.Errorf("found only %d code constants; the scan is not reading the package", seen)
	}
}

// TestGroupsOrderAndRowOrder pins the four-group order and that each group
// keeps Classify's row order, which the plain output and the list both rely
// on to look the same.
func TestGroupsOrderAndRowOrder(t *testing.T) {
	r := Report{Rows: []Row{
		{Name: "a", Status: StatusInUse},
		{Name: "b", Status: StatusProven},
		{Name: "c", Status: StatusUniqueWork},
		{Name: "d", Status: StatusProven},
		{Name: "e", Status: StatusLessSure},
	}}
	gs := Groups(r)
	var heads, names []string
	for _, g := range gs {
		heads = append(heads, g.Heading)
		for _, row := range g.Rows {
			names = append(names, row.Name)
		}
	}
	wantHeads := "PROVEN MERGED|LOOKS MERGED, LESS SURE|HAS WORK THAT EXISTS NOWHERE ELSE|IN USE / LOCKED (cannot remove)"
	if strings.Join(heads, "|") != wantHeads {
		t.Errorf("headings = %v", heads)
	}
	if strings.Join(names, "") != "bdeca" {
		t.Errorf("row order = %v", names)
	}
}

// TestTickableIsProvenOnly pins that only a proven row without blockers can
// be ticked: a less-sure, unique or in-use row never, a remote row included.
func TestTickableIsProvenOnly(t *testing.T) {
	for _, st := range []Status{StatusLessSure, StatusUniqueWork, StatusInUse} {
		if (Row{Kind: KindRemote, Status: st}).Tickable() {
			t.Errorf("a %s row is tickable", st)
		}
	}
	if !(Row{Kind: KindRemote, Status: StatusProven}).Tickable() {
		t.Error("a proven remote row is not tickable")
	}
	if (Row{Status: StatusProven, Blockers: []Reason{{Code: BlockLocked}}}).Tickable() {
		t.Error("a proven row with a blocker is tickable")
	}
}

// TestRowTextSentences pins the evidence sentences for the cases the design
// mockup shows, and that "you are here" goes to the innermost current
// worktree only (every worktree containing the cwd is Current).
func TestRowTextSentences(t *testing.T) {
	rep := Report{Base: Base{Ref: "origin/main"}, Rows: []Row{
		{Kind: KindWorktree, Path: "/r", Blockers: []Reason{{Code: BlockCurrent}, {Code: BlockPrimary}}},
		{Kind: KindWorktree, Path: "/r/.wt/x", Blockers: []Reason{{Code: BlockCurrent}}},
	}}
	if got := RowText(rep, rep.Rows[1]); got != "you are here" {
		t.Errorf("inner = %q", got)
	}
	if got := RowText(rep, rep.Rows[0]); got != "contains the directory you are in; primary checkout" {
		t.Errorf("outer = %q", got)
	}
	pr := Row{Evidence: EvPRTip, PR: &PRInfo{Number: 517, State: "MERGED"}}
	if got := RowText(rep, pr); got != "squash merged, PR #517" {
		t.Errorf("pr = %q", got)
	}
	anc := Row{Evidence: EvAncestor}
	if got := RowText(rep, anc); got != "ancestor of origin/main" {
		t.Errorf("ancestor = %q", got)
	}
	u := Row{Reasons: []Reason{{Code: ReasonUnpushed, Detail: "4 commits on no remote-tracking ref"}, {Code: ReasonCannotVerify, Detail: "gh answered for no branch"}}}
	if got := RowText(rep, u); got != "4 commits on no remote-tracking ref; needs gh (gh answered for no branch)" {
		t.Errorf("unique = %q", got)
	}
}

// TestHeaderLinesTellFetchAndFallback pins that a failed fetch, a skipped
// fetch, a local-fallback base and a gh outage each show in the header, so
// none reads as a clean run.
func TestHeaderLinesTellFetchAndFallback(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	join := func(r Report) string { return strings.Join(HeaderLines(r, now), "\n") }
	ok := join(Report{Base: Base{Ref: "origin/main"}, Fetch: FetchResult{OK: true, At: now.Add(-3 * time.Minute)}, GH: GHOK})
	if !strings.Contains(ok, "fetch: ok, 3m ago") || !strings.Contains(ok, "gh:    ok") {
		t.Errorf("ok header:\n%s", ok)
	}
	bad := join(Report{Base: Base{Ref: "main", Fallback: true}, Fetch: FetchResult{Err: "boom", At: now}, GH: GHUnavailable})
	for _, want := range []string{"FAILED (boom)", "local fallback", "gh:    unavailable"} {
		if !strings.Contains(bad, want) {
			t.Errorf("header lacks %q:\n%s", want, bad)
		}
	}
	if sk := join(Report{Base: Base{Ref: "origin/main"}}); !strings.Contains(sk, "fetch: skipped") {
		t.Errorf("skipped header:\n%s", sk)
	}
}

// TestRowTextWeakSignalSaidOnce pins that a row whose only signal is cherry
// or patch-id equivalence says "not proven" once: the evidence wording and
// the weak-signal reason say the same thing, and the list showed both.
func TestRowTextWeakSignalSaidOnce(t *testing.T) {
	rep := Report{Base: Base{Ref: "origin/main"}}
	for _, ev := range []Evidence{EvPatchID, EvCherryEquivalent} {
		row := Row{Status: StatusLessSure, Evidence: ev, Reasons: []Reason{{Code: ReasonWeakSignal}}}
		if got, want := RowText(rep, row), "patches appear in origin/main, not proven"; got != want {
			t.Errorf("%s: RowText = %q, want %q", ev, got, want)
		}
	}
	// With no weak evidence the reason still speaks for itself.
	row := Row{Status: StatusLessSure, Reasons: []Reason{{Code: ReasonWeakSignal}}}
	if got := RowText(rep, row); got != "patches appear in the base, not proven" {
		t.Errorf("reason alone: RowText = %q", got)
	}
}

// TestRowTextPutsWhatLowersItFirst pins the order blockers, then reasons,
// then the proof: the first words are the ones read on a narrow screen, and
// a dirty worktree must say it has uncommitted files before it says "same
// commit as origin/main".
func TestRowTextPutsWhatLowersItFirst(t *testing.T) {
	rep := Report{Base: Base{Ref: "origin/main"}}
	row := Row{Kind: KindWorktree, Path: "/r/x", Status: StatusUniqueWork, Evidence: EvSameCommit,
		Reasons: []Reason{{Code: ReasonDirty, Detail: "1 untracked"}}}
	if got, want := RowText(rep, row), "uncommitted or untracked files (1 untracked); same commit as origin/main"; got != want {
		t.Errorf("RowText = %q, want %q", got, want)
	}
	locked := Row{Kind: KindWorktree, Path: "/r/y", Status: StatusInUse, Evidence: EvSameCommit,
		Blockers: []Reason{{Code: BlockLocked}}, Reasons: []Reason{{Code: ReasonIgnoredFiles, Detail: ".env"}}}
	if got, want := RowText(rep, locked), "locked; ignored files (.env); same commit as origin/main"; got != want {
		t.Errorf("RowText = %q, want %q", got, want)
	}
}

// TestReasonTextDetailSameAsWording pins that a detail repeating its code's
// wording is said once: the classifier's dirty detail is the dirty wording,
// and the real 80x24 screen read "uncommitted or untracked files
// (uncommitted or untracked files)".
func TestReasonTextDetailSameAsWording(t *testing.T) {
	if got := ReasonText(ReasonDirty, "uncommitted or untracked files"); got != "uncommitted or untracked files" {
		t.Errorf("ReasonText = %q", got)
	}
	if got := ReasonText(ReasonDirty, "2 modified"); got != "uncommitted or untracked files (2 modified)" {
		t.Errorf("ReasonText with a real detail = %q", got)
	}
}

// TestRowTextShortPRDetailWhenGHUnavailable pins that, when gh answered for
// no branch, a row's PR reasons read "PR lookup failed" without gh's own
// text, and that with gh partial the cause is still shown. Why: the header
// already says "gh: unavailable"; gh's error under every row repeated it
// (an unauthenticated gh's login speech, once per row).
func TestRowTextShortPRDetailWhenGHUnavailable(t *testing.T) {
	row := Row{Kind: KindBranch, Status: StatusLessSure, Unknown: true, Reasons: []Reason{
		{Code: ReasonPRUnknown, Detail: "gh pr list: exit status 4: To get started with GitHub CLI"},
		{Code: ReasonCannotVerify, Detail: "gh pr list: exit status 4: To get started with GitHub CLI"},
	}}
	rep := Report{Base: Base{Ref: "origin/main"}, GH: GHUnavailable}
	if got, want := RowText(rep, row), "PR lookup failed; needs gh (PR lookup failed)"; got != want {
		t.Errorf("unavailable: RowText = %q, want %q", got, want)
	}
	rep.GH = GHPartial
	if got := RowText(rep, row); !strings.Contains(got, "To get started") {
		t.Errorf("partial: RowText = %q, want gh's cause kept", got)
	}
}

// nestedPrimary is a primary-checkout row holding n other worktrees, each
// a nested blocker naming its path, as CheckGates makes them.
func nestedPrimary(n int) Row {
	row := Row{Kind: KindWorktree, Name: "main", Path: "/r", Branch: "main", Status: StatusInUse,
		Blockers: []Reason{{Code: BlockPrimary, Detail: "/r"}, {Code: BlockCurrent}}}
	for i := 0; i < n; i++ {
		row.Blockers = append(row.Blockers, Reason{Code: BlockNested, Detail: fmt.Sprintf("/r/.claude/worktrees/agent-%02d", i)})
	}
	row.Blockers = append(row.Blockers, Reason{Code: BlockDefaultBranch, Detail: "main"})
	return row
}

// TestRowTextCollapsesNestedWorktrees pins that the nested-worktree
// blockers are one phrase with a count: the primary checkout holds every
// agent worktree, and one full path per worktree made its row grow past the
// screen. The paths stay in the JSON (TestEncodeKeepsEveryNestedPath).
func TestRowTextCollapsesNestedWorktrees(t *testing.T) {
	for _, c := range []struct {
		n    int
		want string
	}{
		{30, "primary checkout (/r); you are here; contains 30 other worktrees; the default branch (main)"},
		{1, "primary checkout (/r); you are here; contains 1 other worktree; the default branch (main)"},
	} {
		row := nestedPrimary(c.n)
		rep := Report{Base: Base{Ref: "origin/main"}, Rows: []Row{row}}
		if got := RowText(rep, row); got != c.want {
			t.Errorf("n=%d: RowText = %q\nwant           %q", c.n, got, c.want)
		}
	}
}

func tagTexts(row Row) []string {
	var out []string
	for _, t := range RowTags(row) {
		out = append(out, t.Text)
	}
	return out
}

// hasPRTag reports whether row still carries a PR badge ("PR ?" or
// "#N …"): the absence TestRowTagsCarriesNoPRInfo pins after the PR moved
// into the PR# column.
func hasPRTag(row Row) bool {
	return slices.ContainsFunc(RowTags(row), func(t Tag) bool {
		return t.Text == "PR ?" || strings.HasPrefix(t.Text, "#")
	})
}

// TestRowTagsCarriesNoPRInfo pins that no badge carries PR info: "PR ?" and
// "#N state" read from the PR# column alone (RowTags' old PR badges).
// Why: the verdict line keeps only risk and fact badges.
func TestRowTagsCarriesNoPRInfo(t *testing.T) {
	for _, row := range []Row{
		{PRUnknown: true},
		{PRTag: &PRInfo{Number: 7, State: "MERGED"}},
	} {
		if got := tagTexts(row); len(got) != 0 {
			t.Errorf("row %+v: tags %v, want no PR badge", row, got)
		}
	}
	if RowTags(Row{}) != nil {
		t.Errorf("an empty row carried tags %v", RowTags(Row{}))
	}
}

func TestRowTagsEveryFact(t *testing.T) {
	cases := []struct {
		name string
		row  Row
		want string
		sev  TagSeverity
	}{
		{"root", Row{Primary: true}, "root", TagInfo},
		{"current", Row{Current: true}, "you are here", TagInfo},
		{"current hard stop", Row{HardStop: BlockCurrent}, "you are here", TagInfo},
		{"held", Row{Blockers: []Reason{{BlockHeld, "abc123"}}}, "held by session abc123", TagWarn},
		{"dirty", Row{Losses: Losses{Dirty: true}}, "dirty", TagWarn},
		{"unpushed", Row{Losses: Losses{Unpushed: 2}}, "unpushed", TagWarn},
		{"upstream gone", Row{Upstream: &Upstream{Gone: true}}, "upstream gone", TagWarn},
		{"locked", Row{HardStop: BlockLocked, Blockers: []Reason{{BlockLocked, ""}}}, "locked", TagWarn},
		{"+remote", Row{Upstream: &Upstream{Ref: "refs/remotes/origin/x"}}, "+remote", TagInfo},
	}
	for _, c := range cases {
		got := RowTags(c.row)
		if len(got) != 1 || got[0].Text != c.want || got[0].Severity != c.sev {
			t.Errorf("%s: got %+v, want one %q (%d)", c.name, got, c.want, c.sev)
		}
	}
	if got := RowTags(Row{Upstream: &Upstream{Gone: true}}); slices.ContainsFunc(got, func(t Tag) bool { return t.Text == "+remote" }) {
		t.Errorf("gone upstream must not read +remote: %+v", got)
	}
}

// TestListOrder pins the flat list's one order: the root first whatever its
// name sorts as, then every local worktree and local branch together by
// display name, then the remote-only rows by display name. Why: the list has
// no headings, so kind grouping is now an ordering rule and the root must
// stay where the user's eye starts.
func TestListOrder(t *testing.T) {
	rep := Report{Rows: []Row{
		{Kind: KindRemote, Remote: "origin", Name: "b"},
		{Kind: KindWorktree, Name: "zeta"},
		{Kind: KindBranch, Name: "mid"},
		{Kind: KindWorktree, Name: "main", Primary: true},
		{Kind: KindBranch, Name: "beta"},
		{Kind: KindWorktree, Name: "alpha"},
		{Kind: KindRemote, Remote: "origin", Name: "a"},
	}, Folded: []Row{{Kind: KindRemote, Remote: "origin", Name: "folded"}}}
	got := names(ListOrder(rep))
	want := []string{"main", "alpha", "beta", "mid", "zeta", "origin/a", "origin/b"}
	if !slices.Equal(got, want) {
		t.Fatalf("ListOrder names %v, want %v", got, want)
	}
	// The root leads by being the root, not by sorting first: a folded remote
	// ref is its local row's upstream and is never a row of its own here.
	zzz := Report{Rows: []Row{
		{Kind: KindWorktree, Name: "aaa"},
		{Kind: KindWorktree, Name: "zzz", Primary: true},
	}}
	if got := names(ListOrder(zzz)); !slices.Equal(got, []string{"zzz", "aaa"}) {
		t.Errorf("root named zzz: %v, want [zzz aaa]", got)
	}
	if got := ListOrder(Report{}); len(got) != 0 {
		t.Errorf("empty report gave %v", got)
	}
}

// TestListOrderStableAcrossFill pins that the facts-only report and the
// filled one order the rows identically. Why: the fill lands under the
// cursor after first paint; an order that moved with the verdicts would move
// the rows beneath the selection (spec §5 / the anti-goal).
func TestListOrderStableAcrossFill(t *testing.T) {
	rows := []Row{
		{Kind: KindWorktree, Name: "beta"},
		{Kind: KindWorktree, Name: "root", Primary: true},
		{Kind: KindBranch, Name: "alpha"},
		{Kind: KindRemote, Remote: "origin", Name: "zzz"},
	}
	facts := Report{Rows: slices.Clone(rows)}
	for i := range facts.Rows {
		facts.Rows[i].Pending = true
		facts.Rows[i].Status = StatusLessSure
	}
	full := Report{Rows: slices.Clone(rows)}
	full.Rows[0].Status = StatusUniqueWork
	full.Rows[2].Status = StatusProven
	full.Rows[3].Status = StatusInUse
	if a, b := names(ListOrder(facts)), names(ListOrder(full)); !slices.Equal(a, b) {
		t.Fatalf("order changed across stages:\n facts %v\n full  %v", a, b)
	}
}

// names is the display names of rows, in order.
func names(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = DisplayName(r)
	}
	return out
}

// TestRowTextDropsRepeatedReasonWording pins that one sentence never says the
// same thing twice: several PR-lookup reasons (one per lookup that failed)
// read as one "needs gh (PR lookup failed)".
func TestRowTextDropsRepeatedReasonWording(t *testing.T) {
	row := Row{Kind: KindRemote, Name: "origin/x", Reasons: []Reason{
		{Code: ReasonCannotVerify, Detail: "gh exited 1"},
		{Code: ReasonCannotVerify, Detail: "gh exited 1"},
		{Code: ReasonCannotVerify, Detail: "gh exited 1"},
	}}
	got := RowText(Report{GH: GHUnavailable}, row)
	if n := strings.Count(got, "needs gh"); n != 1 {
		t.Errorf("%q says needs gh %d times, want once", got, n)
	}
}
