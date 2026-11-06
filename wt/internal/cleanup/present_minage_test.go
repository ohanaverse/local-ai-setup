package cleanup

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

// minAgeLine is the header's min-age note, or "" when there is none.
func minAgeLine(r Report) string {
	for _, l := range HeaderLines(r, time.Now()) {
		if strings.HasPrefix(l, "min-age:") {
			return l
		}
	}
	return ""
}

// TestHeaderNotesLoweredMinAge verifies the header says so when --min-age was
// lowered below DefaultMinAge (the note names the value and the default),
// and says nothing at the default or above; that Classify carries the
// MinAge it used (the default when none was given) into the Report; and that
// the JSON of a report is byte-identical with or without MinAge and Notes.
// Why: a lowered threshold lets a brand-new branch read as merged, which the
// user must see before trusting a "proven" row, while the JSON contract
// (docs/contracts/cleanup-list.*) must not grow a key.
func TestHeaderNotesLoweredMinAge(t *testing.T) {
	base := Report{Repo: "/r", Base: Base{Default: "main", Ref: "origin/main"}, GH: GHOK}

	lowered := base
	lowered.MinAge = 2 * time.Minute
	if got, want := minAgeLine(lowered), "min-age: lowered to 2m (default 10m): fresh branches may read as merged"; got != want {
		t.Errorf("lowered note = %q, want %q", got, want)
	}
	odd := base
	odd.MinAge = 90 * time.Second
	if got := minAgeLine(odd); !strings.Contains(got, "lowered to 1m30s") {
		t.Errorf("1m30s note = %q", got)
	}
	for _, d := range []time.Duration{0, DefaultMinAge, 2 * time.Hour} {
		r := base
		r.MinAge = d
		if got := minAgeLine(r); got != "" {
			t.Errorf("MinAge %s: note = %q, want none", d, got)
		}
	}

	if got := Classify(Snapshot{}, Options{MinAge: 3 * time.Minute}).MinAge; got != 3*time.Minute {
		t.Errorf("Classify MinAge = %s, want 3m", got)
	}
	if got := Classify(Snapshot{}, Options{}).MinAge; got != DefaultMinAge {
		t.Errorf("Classify default MinAge = %s, want %s", got, DefaultMinAge)
	}

	// The rows are the same; only the `notes` array differs, and it carries
	// exactly the header's caveats.
	withAll := lowered
	withAll.Notes = []string{"registry: could not be read; artifact protection is OFF and removal is refused"}
	if got, want := NoteLines(withAll), []string{minAgeLine(withAll), withAll.Notes[0]}; !slices.Equal(got, want) {
		t.Errorf("NoteLines = %q, want %q", got, want)
	}
	rowsOf := func(r Report) string {
		raw, err := Encode(r)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct{ Rows json.RawMessage }
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		return string(doc.Rows)
	}
	if rowsOf(withAll) != rowsOf(base) {
		t.Error("rows changed with MinAge/Notes set")
	}
}

// TestHeaderAppendsNotesOneLine verifies Report.Notes are appended to the
// header after the gh line, each kept on one sanitised line. Why: the
// command puts the registry warning there, and its cause is error text from
// the OS or a TOML parser that must not break the header or drive the
// terminal.
func TestHeaderAppendsNotesOneLine(t *testing.T) {
	r := Report{Repo: "/r", Base: Base{Ref: "origin/main"}, GH: GHOK,
		Notes: []string{"registry: could not be read (bad\nline \x1b[31m); artifact protection is OFF"}}
	lines := HeaderLines(r, time.Now())
	last := lines[len(lines)-1]
	if last != "registry: could not be read (bad line [31m); artifact protection is OFF" {
		t.Errorf("last header line = %q", last)
	}
	if !strings.HasPrefix(lines[len(lines)-2], "gh:") {
		t.Errorf("note is not after the gh line: %q", lines)
	}
}

// TestOneLineIsTheRowSanitiser verifies OneLine is the sanitiser row
// details use (flat and capped at gitErrMax runes). Why: cmd/wt shows error
// text from the registry load in the header and must not copy the helper.
func TestOneLineIsTheRowSanitiser(t *testing.T) {
	long := strings.Repeat("x", gitErrMax+50)
	if got := OneLine("a\n\tb\x1b"); got != "a b" {
		t.Errorf("OneLine = %q", got)
	}
	if got := OneLine(long); got != gitErrText(long) || len([]rune(got)) != gitErrMax {
		t.Errorf("OneLine does not cap like gitErrText: %d runes", len([]rune(got)))
	}
}
