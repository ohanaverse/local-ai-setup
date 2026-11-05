package cleanup

import (
	"strings"
	"testing"
	"time"
)

// ghDownReport is a report whose gh is unavailable and whose rows carry the
// given PR-lookup causes, one cause per row (a cause may repeat).
func ghDownReport(causes ...string) Report {
	r := Report{Base: Base{Ref: "origin/main"}, GH: GHUnavailable}
	for _, c := range causes {
		r.Rows = append(r.Rows, Row{Kind: KindBranch, Name: "topic", Status: StatusLessSure, Unknown: true,
			Reasons: []Reason{{Code: ReasonPRUnknown, Detail: c}, {Code: ReasonCannotVerify, Detail: c}}})
	}
	return r
}

func ghLine(t *testing.T, r Report) string {
	t.Helper()
	for _, l := range HeaderLines(r, time.Now()) {
		if strings.HasPrefix(l, "gh:") {
			return l
		}
	}
	t.Fatalf("no gh line in %v", HeaderLines(r, time.Now()))
	return ""
}

// TestHeaderGHUnavailableNamesTheCause pins that the gh line says why gh
// answered nothing, for the three realistic causes (not logged in, not
// installed, timed out). Why: rows read only "PR lookup failed", so without
// this the plain list and the TUI never told the user what to fix.
func TestHeaderGHUnavailableNamesTheCause(t *testing.T) {
	for _, cause := range []string{
		"gh pr list: exit status 4: To get started with GitHub CLI, please run: gh auth login",
		`exec: "gh": executable file not found in $PATH`,
		"gh pr list timed out after 15s",
	} {
		want := "gh:    unavailable (" + cause + "); PR evidence is missing and rows that need it say so"
		if got := ghLine(t, ghDownReport(cause)); got != want {
			t.Errorf("gh line = %q, want %q", got, want)
		}
	}
}

// TestHeaderGHCausesAreDistinctFirstSeenAndCapped pins that the same cause on
// many rows is shown once, two distinct causes both show in first-seen
// order, a third becomes "(+1 more)", and one cause is cut at 100 runes.
// Why: the line must stay short however many rows fail differently.
func TestHeaderGHCausesAreDistinctFirstSeenAndCapped(t *testing.T) {
	if got := ghLine(t, ghDownReport("a", "a", "a")); !strings.Contains(got, "unavailable (a);") {
		t.Errorf("repeated cause: %q", got)
	}
	if got := ghLine(t, ghDownReport("b", "a")); !strings.Contains(got, "unavailable (b; a);") {
		t.Errorf("two causes: %q", got)
	}
	if got := ghLine(t, ghDownReport("b", "a", "c")); !strings.Contains(got, "unavailable (b; a (+1 more));") {
		t.Errorf("three causes: %q", got)
	}
	got := ghLine(t, ghDownReport(strings.Repeat("x", 300)))
	if want := "(" + strings.Repeat("x", 99) + "…);"; !strings.Contains(got, want) {
		t.Errorf("long cause not cut at 100 runes: %q", got)
	}
}

// TestHeaderGHWithoutCauseAndOtherStatesUnchanged pins that an unavailable gh
// with no cause text keeps the old line, and that ok and partial headers do
// not take a cause. Why: only the unavailable line was changed.
func TestHeaderGHWithoutCauseAndOtherStatesUnchanged(t *testing.T) {
	const old = "gh:    unavailable; PR evidence is missing and rows that need it say so"
	if got := ghLine(t, ghDownReport()); got != old {
		t.Errorf("no rows: %q", got)
	}
	if got := ghLine(t, ghDownReport("")); got != old {
		t.Errorf("empty cause: %q", got)
	}
	r := ghDownReport("boom")
	r.GH = GHPartial
	if got, want := ghLine(t, r), "gh:    partial; some branches could not be checked against their PRs"; got != want {
		t.Errorf("partial: %q", got)
	}
	r.GH = GHOK
	if got := ghLine(t, r); got != "gh:    ok" {
		t.Errorf("ok: %q", got)
	}
}

// TestEncodeKeepsGHCauseOnRowsOnly pins that the JSON of an unavailable-gh
// report still carries the cause in the row reason detail and holds no header
// text. Why: the cause moved into the human header only; the JSON contract
// is unchanged.
func TestEncodeKeepsGHCauseOnRowsOnly(t *testing.T) {
	b, err := Encode(ghDownReport("gh pr list timed out after 15s"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "gh pr list timed out after 15s") {
		t.Errorf("row detail lost the cause:\n%s", s)
	}
	if strings.Contains(s, "PR evidence is missing") || strings.Contains(s, "unavailable (") {
		t.Errorf("header text leaked into JSON:\n%s", s)
	}
}
