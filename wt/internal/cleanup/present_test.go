package cleanup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
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
