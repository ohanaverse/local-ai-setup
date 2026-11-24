package cleanup

import (
	"os"
	"strings"
	"testing"
	"time"
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
	// Created is the commit date: set, and just now.
	if r.Created.IsZero() || time.Since(r.Created) > time.Hour {
		t.Fatalf("Created = %v, want about now", r.Created)
	}
	want.Created = r.Created
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

// recSnap is a hand-built snapshot for RecordRows: one record taken from
// /src on branch refs/heads/b at head h, created at created.
func recSnap(created time.Time) (Snapshot, RescueRecord) {
	r := RescueRecord{
		Ref: RecordRefPrefix + "0123456789abcdef", Commit: strings.Repeat("a", 40),
		SourcePath: "/src", SourceRef: "refs/heads/b", SourceHead: strings.Repeat("b", 40),
		DestBranch: "rescue/b", DestPath: "/dest", Created: created,
	}
	return Snapshot{Records: []RescueRecord{r}}, r
}

// TestRecordRowsStatuses checks each status RecordRows gives: in_use when a
// worktree matches by identity or reads Preserved through the ref, recent
// under RecordMinAge or with an unknown age, unused otherwise, and that a
// source that exists but moved, and a missing destination, are reported.
// Why: wt remove --record trusts exactly this classification to refuse
// pruning a record a worktree still leans on.
func TestRecordRowsStatuses(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	old := now.Add(-30 * 24 * time.Hour)
	match := func(r RescueRecord) WorktreeFact {
		return WorktreeFact{Path: "/src", Branch: r.SourceRef, Head: r.SourceHead}
	}
	cases := []struct {
		name    string
		created time.Time
		mut     func(s *Snapshot, r RescueRecord)
		status  string
		inUse   bool
		exists  bool
		matches bool
		dest    bool
		age     int
	}{
		{"source gone, old", old, func(*Snapshot, RescueRecord) {}, RecordUnused, false, false, false, false, 30},
		{"matching worktree", old, func(s *Snapshot, r RescueRecord) {
			s.Worktrees = []WorktreeState{{Fact: match(r)}}
		}, RecordInUse, true, true, true, false, 30},
		{"preserved through the ref", old, func(s *Snapshot, r RescueRecord) {
			s.Worktrees = []WorktreeState{{Fact: WorktreeFact{Path: "/elsewhere", Branch: "refs/heads/x", Head: "c"}, Preserved: &Preserved{Ref: r.Ref}}}
		}, RecordInUse, true, false, false, false, 30},
		{"source exists but moved", old, func(s *Snapshot, r RescueRecord) {
			s.Worktrees = []WorktreeState{{Fact: WorktreeFact{Path: "/src", Branch: r.SourceRef, Head: strings.Repeat("c", 40)}}}
		}, RecordUnused, false, true, false, false, 30},
		{"recent", now.Add(-2 * 24 * time.Hour), func(*Snapshot, RescueRecord) {}, RecordRecent, false, false, false, false, 2},
		{"age unknown", time.Time{}, func(*Snapshot, RescueRecord) {}, RecordRecent, false, false, false, false, -1},
		{"destination branch present", old, func(s *Snapshot, r RescueRecord) {
			s.Heads = []RefFact{{Ref: "refs/heads/rescue/b", Name: "rescue/b"}}
		}, RecordUnused, false, false, false, true, 30},
		{"recent but in use stays in use", now.Add(-time.Hour), func(s *Snapshot, r RescueRecord) {
			s.Worktrees = []WorktreeState{{Fact: match(r)}}
		}, RecordInUse, true, true, true, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, r := recSnap(c.created)
			c.mut(&s, r)
			rows := RecordRows(s, now)
			if len(rows) != 1 {
				t.Fatalf("rows = %+v", rows)
			}
			g := rows[0]
			if g.Status != c.status || g.InUse != c.inUse || g.SourceExists != c.exists ||
				g.SourceMatches != c.matches || g.DestExists != c.dest || g.AgeDays != c.age {
				t.Fatalf("row = %+v, want status %s inUse %v exists %v matches %v dest %v age %d",
					g, c.status, c.inUse, c.exists, c.matches, c.dest, c.age)
			}
			if g.ID != "0123456789abcdef" || g.Reason == "" {
				t.Fatalf("id/reason: %+v", g)
			}
		})
	}
}

// TestRecordRowsIgnoresMalformedRecords checks a malformed record never
// reaches the snapshot (ListRecords drops it), so no row is made for it.
// Why: a row is an offer to prune; an ambiguous ref must not be offered.
func TestRecordRowsIgnoresMalformedRecords(t *testing.T) {
	f, src := recordTarget(t)
	f.addRecord(t, src, recordOpts{Omit: []string{"Wt-Source-Head"}})
	recs, err := ListRecords(Git{Dir: f.Dir})
	if err != nil {
		t.Fatal(err)
	}
	if rows := RecordRows(Snapshot{Records: recs}, time.Now()); len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
}

// TestRecordUsersReadsWorktreesFresh checks RecordUsers names the worktree a
// record was taken from while HEAD is unchanged and nothing once it moved.
// Why: the prune engine's in-use check must follow the live worktree, not
// the snapshot the list was built from.
func TestRecordUsersReadsWorktreesFresh(t *testing.T) {
	f, src := recordTarget(t)
	ref := f.addRecord(t, src, recordOpts{})
	recs, err := ListRecords(Git{Dir: f.Dir})
	if err != nil || len(recs) != 1 || recs[0].Ref != ref {
		t.Fatalf("records = %+v, %v", recs, err)
	}
	users, err := RecordUsers(Git{Dir: f.Dir}, f.Dir, recs[0])
	if err != nil || len(users) != 1 {
		t.Fatalf("users = %v, %v; want the source worktree", users, err)
	}
	f.git("commit", "--allow-empty", "-m", "moved on")
	users, err = RecordUsers(Git{Dir: f.Dir}, f.Dir, recs[0])
	if err != nil || len(users) != 0 {
		t.Fatalf("after the head moved: users = %v, %v; want none", users, err)
	}
	if _, err := RecordUsers(Git{Dir: t.TempDir() + "/missing"}, f.Dir, recs[0]); err == nil {
		t.Fatal("an unreadable worktree list must be an error")
	}
}

// TestRecordLinesAndJSON checks the plain block and the JSON section for a
// report with records, and that a report without them prints and encodes
// neither. Why: the section is additive: nothing changes for a repository
// with no rescue.
func TestRecordLinesAndJSON(t *testing.T) {
	now := time.Now()
	s, _ := recSnap(now.Add(-10 * 24 * time.Hour))
	rep := Report{Repo: "/r", Records: RecordRows(s, now)}
	lines := strings.Join(RecordLines(rep), "\n")
	for _, want := range []string{"Rescue records", "0123456789abcdef", "unused", "10d old", "from /src", "rescue/b (branch gone)", "wt remove --record"} {
		if !strings.Contains(lines, want) {
			t.Errorf("lines lack %q:\n%s", want, lines)
		}
	}
	b, err := Encode(rep)
	if err != nil || !strings.Contains(string(b), `"records"`) || !strings.Contains(string(b), `"age_days": 10`) {
		t.Fatalf("json = %s, %v", b, err)
	}
	empty := Report{Repo: "/r"}
	if RecordLines(empty) != nil {
		t.Fatal("empty report printed a block")
	}
	if b, _ := Encode(empty); strings.Contains(string(b), `"records"`) {
		t.Fatalf("empty report carries records: %s", b)
	}
}
