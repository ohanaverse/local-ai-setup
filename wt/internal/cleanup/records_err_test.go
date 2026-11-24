package cleanup

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// recordsFailGit is a Git on the fixture whose rescue-record listing (the
// for-each-ref over RecordRefPrefix) fails while every other git call runs
// for real. The failure is a per-call seam: a stub `git` first on PATH for the test (t.Setenv), so no package variable and no change to the machine's git.
func recordsFailGit(t *testing.T, f *fixture) Git {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in " + RecordRefPrefix + "*) echo 'injected: ref store unreadable' >&2; exit 128;; esac; done\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// exec resolves "git" against this process's PATH, not the child's Env.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return Git{Dir: f.Dir}
}

const recordsNoteWant = "rescue records could not be read ("

func recordsNote(lines []string) string {
	for _, l := range lines {
		if strings.HasPrefix(l, recordsNoteWant) {
			return l
		}
	}
	return ""
}

// TestUnreadableRecordsAreANote verifies a failed rescue-record listing in
// Gather sets Snapshot.RecordsErr and leaves no records, Classify carries it
// into the Report, and both the text header and the JSON `notes` array show
// the same line; a healthy gather has neither the error nor the line (#548).
// Why: an unreadable listing reads as no records, so rescued originals show
// as unique work; the reader must be told why.
func TestUnreadableRecordsAreANote(t *testing.T) {
	f := newFixture(t)
	f.Worktree("topic")
	noGh(t)
	o := Options{Now: later()}

	// A healthy gather: no error, no line.
	ok := gather(t, f)
	if ok.RecordsErr != "" || recordsNote(NoteLines(Classify(ok, o))) != "" {
		t.Errorf("healthy gather: RecordsErr %q", ok.RecordsErr)
	}

	// From here on every record listing fails (the stub stays on PATH).
	g := recordsFailGit(t, f)
	s, err := Gather(g, GatherInputs{CWD: f.Dir, OwnRepo: "o/r", Parallel: 2})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if s.RecordsErr == "" || !strings.Contains(s.RecordsErr, "injected") || len(s.Records) != 0 {
		t.Fatalf("RecordsErr %q, Records %v; want the error and no records", s.RecordsErr, s.Records)
	}
	r := Classify(s, o)
	note := recordsNote(NoteLines(r))
	if !strings.HasSuffix(note, "); rescued originals are listed as unique work") || strings.Contains(note, "\n") {
		t.Fatalf("note = %q", note)
	}
	if got := recordsNote(HeaderLines(r, time.Now())); got != note {
		t.Errorf("header note = %q, want %q", got, note)
	}
	raw, err := Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct{ Notes []string }
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(doc.Notes, note) {
		t.Errorf("JSON notes %q lack %q", doc.Notes, note)
	}

	// A facts-only snapshot has not read records yet: no note.
	facts, err := GatherFacts(g, GatherInputs{CWD: f.Dir, OwnRepo: "o/r", Parallel: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := recordsNote(NoteLines(Classify(facts, o))); got != "" {
		t.Errorf("facts-only snapshot has note %q", got)
	}
}

// TestRescanIgnoresUnreadableRecords verifies Rescan still returns a row, not
// an error, when the record listing fails. Why: a Rescan error means
// "refused" in the removal engine; the listing failure is shown by the list's
// note, not by refusing every removal.
func TestRescanIgnoresUnreadableRecords(t *testing.T) {
	f := newFixture(t)
	f.Worktree("topic")
	noGh(t)
	o := Options{Now: later()}
	s := gather(t, f)
	w := findRow(t, Classify(s, o), KindWorktree, "topic")
	if _, err := Rescan(recordsFailGit(t, f), s, rescanInputs(f), o, targetOf(w)); err != nil {
		t.Fatalf("Rescan with unreadable records: %v", err)
	}
}
