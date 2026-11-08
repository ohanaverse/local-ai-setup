package cleanup

import (
	"os"
	"strings"
	"testing"
)

// recordTarget is the worktree record tests rescue from: the fixture's main
// checkout, which has a branch and a HEAD commit.
func recordTarget(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t)
	return f, f.Dir
}

// TestListRecordsParsesWhatRescueWrites checks that a hand-built chain, laid
// out as wt rescue writes it, comes back with its ref, commit and all six
// trailer values. Why: every later preservation decision reads these fields.
func TestListRecordsParsesWhatRescueWrites(t *testing.T) {
	f, src := recordTarget(t)
	ref := f.addRecord(t, src, recordOpts{DestBranch: "rescue/b", DestPath: "/tmp/copy"})
	got, err := ListRecords(Git{Dir: f.Dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("records = %+v, want 1", got)
	}
	r := got[0]
	head := f.git("rev-parse", "HEAD")
	want := RescueRecord{
		Ref: ref, Commit: f.git("rev-parse", ref),
		SourcePath: ResolvePath(src), SourceRef: "refs/heads/main", SourceHead: head,
		DestBranch: "rescue/b", DestPath: "/tmp/copy",
	}
	if r != want {
		t.Fatalf("record = %+v, want %+v", r, want)
	}
	if !strings.HasPrefix(r.Ref, RecordRefPrefix) {
		t.Fatalf("ref %q lacks prefix", r.Ref)
	}
}

// TestListRecordsIgnoresMalformedRecords checks each way a record can be
// unreadable (duplicate or missing trailer, another version, a control
// character, no trailers, a non-commit object) is dropped while a valid
// record beside it is kept. Why: an ambiguous record must never preserve a
// worktree, and one bad ref must not hide the good ones.
func TestListRecordsIgnoresMalformedRecords(t *testing.T) {
	empty := "no trailers here\n"
	cases := []struct {
		name string
		o    recordOpts
	}{
		{"duplicate source path", recordOpts{Extra: []string{"Wt-Source-Path: /other"}}},
		{"missing source head", recordOpts{Omit: []string{"Wt-Source-Head"}}},
		{"version 2", recordOpts{Version: "2"}},
		{"control character", recordOpts{DestPath: "/tmp/a\x01b"}},
		{"no trailers", recordOpts{Message: &empty}},
		{"non-commit object", recordOpts{Object: "HEAD^{tree}"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, src := recordTarget(t)
			if c.o.Object != "" {
				c.o.Object = f.git("rev-parse", "HEAD^{tree}")
			}
			good := f.addRecord(t, src, recordOpts{DestPath: "/tmp/good"})
			f.git("commit", "--allow-empty", "-m", "other")
			bad := f.addRecord(t, src, c.o)
			got, err := ListRecords(Git{Dir: f.Dir})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].Ref != good {
				t.Fatalf("records = %+v, want only %s (dropping %s)", got, good, bad)
			}
		})
	}
}

// TestListRecordsFailsClosed checks that a failing git returns an error and
// no records. Why: a listing that cannot run must read as "unknown", never as
// an empty answer that looks complete.
func TestListRecordsFailsClosed(t *testing.T) {
	got, err := ListRecords(Git{Dir: t.TempDir() + "/missing"})
	if err == nil || len(got) != 0 {
		t.Fatalf("got %+v, %v; want an error and no records", got, err)
	}
}

// TestRecordConstantsAvoidDeletionWords checks records.go carries none of the
// strings the deletion scan bans. Why: TestCleanupCmdContainsNoDeletion scans
// this package's text, comments included, and cleanup must stay read-only.
func TestRecordConstantsAvoidDeletionWords(t *testing.T) {
	b, err := os.ReadFile("records.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"update-" + "ref", `"pu` + `sh"`, "--del" + "ete", "worktree re" + "move", "os.Rem" + "ove(", "os.RemoveA" + "ll"} {
		if strings.Contains(string(b), w) {
			t.Errorf("records.go contains %q", w)
		}
	}
}
