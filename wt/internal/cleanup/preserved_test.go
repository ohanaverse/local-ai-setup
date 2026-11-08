package cleanup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// preservedWT is a linked worktree "topic" holding one unpushed commit.
func preservedWT(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t)
	wt := f.Worktree("topic")
	commitIn(t, wt, "a.txt", "a", "a")
	return f, wt
}

// stateOf returns the snapshot's state of the worktree at path, and the
// snapshot's records.
func stateOf(t *testing.T, f *fixture, path string) (WorktreeState, []RescueRecord) {
	t.Helper()
	noGh(t)
	s := gather(t, f)
	for _, ws := range s.Worktrees {
		if resolve(ws.Fact.Path) == resolve(path) {
			return ws, s.Records
		}
	}
	t.Fatalf("no worktree %s in %+v", path, s.Worktrees)
	return WorktreeState{}, nil
}

// TestPreservedOfMatchesTheRescuedWorktree checks a clean worktree with an
// unpushed commit and a record written from it is preserved, with the
// destination values from the trailers. Why: this is the positive case the
// whole signal exists for; the exact values feed the row's reason text.
func TestPreservedOfMatchesTheRescuedWorktree(t *testing.T) {
	f, wt := preservedWT(t)
	ref := f.addRecord(t, wt, recordOpts{DestBranch: "rescued/topic", DestPath: "/tmp/copy"})
	ws, recs := stateOf(t, f, wt)
	want := &Preserved{
		Ref: ref, Commit: f.git("rev-parse", ref), SourceHead: ws.Fact.Head,
		DestBranch: "rescued/topic", DestPath: "/tmp/copy",
	}
	got := preservedOf(Git{Dir: f.Dir}, ws, recs)
	if got == nil || *got != *want {
		t.Fatalf("preservedOf = %+v, want %+v", got, want)
	}
	if ws.Preserved == nil || *ws.Preserved != *want {
		t.Fatalf("Gather left Preserved = %+v, want %+v", ws.Preserved, want)
	}
}

// TestPreservedOfMismatchesAreNil walks each way a record can fail to match
// its worktree. Why: the record is a claim about one exact state; any
// difference, or a precondition that fails, must leave the row as today.
func TestPreservedOfMismatchesAreNil(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T, f *fixture, wt string)
	}{
		{"different source path", func(t *testing.T, f *fixture, wt string) {
			f.addRecord(t, wt, recordOpts{SourcePath: "/elsewhere/topic"})
		}},
		{"different source ref", func(t *testing.T, f *fixture, wt string) {
			f.addRecord(t, wt, recordOpts{SourceRef: "refs/heads/other"})
		}},
		{"detached ref on a branch worktree", func(t *testing.T, f *fixture, wt string) {
			f.addRecord(t, wt, recordOpts{SourceRef: "detached"})
		}},
		{"different source head", func(t *testing.T, f *fixture, wt string) {
			f.addRecord(t, wt, recordOpts{SourceHead: f.git("rev-parse", "main")})
		}},
		{"commit added after the record", func(t *testing.T, f *fixture, wt string) {
			f.addRecord(t, wt, recordOpts{})
			commitIn(t, wt, "b.txt", "b", "b")
		}},
		{"record parent object missing", func(t *testing.T, f *fixture, wt string) {
			ref := f.addRecord(t, wt, recordOpts{})
			parent := f.git("rev-parse", ref+"^")
			p := filepath.Join(f.Dir, ".git", "objects", parent[:2], parent[2:])
			if err := os.Chmod(p, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
		}},
		{"dirty worktree", func(t *testing.T, f *fixture, wt string) {
			f.addRecord(t, wt, recordOpts{})
			f.DirtyIn(wt, "scratch.txt")
		}},
		{"operation in progress", func(t *testing.T, f *fixture, wt string) {
			f.StoppedRebase(wt)
			f.addRecord(t, wt, recordOpts{})
		}},
		{"primary worktree", func(t *testing.T, f *fixture, wt string) {
			f.Commit("p.txt", "p", "p")
			f.addRecord(t, f.Dir, recordOpts{})
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, wt := preservedWT(t)
			c.build(t, f, wt)
			path := wt
			if c.name == "primary worktree" {
				path = f.Dir
			}
			ws, recs := stateOf(t, f, path)
			if len(recs) == 0 {
				t.Fatalf("no record listed, the case tests nothing")
			}
			if got := preservedOf(Git{Dir: f.Dir}, ws, recs); got != nil {
				t.Fatalf("preservedOf = %+v, want nil", got)
			}
			if ws.Preserved != nil {
				t.Fatalf("Gather left Preserved = %+v", ws.Preserved)
			}
			if c.name == "operation in progress" {
				found := false
				for _, b := range ws.Gates.Blockers {
					found = found || b.Code == BlockOperation
				}
				if !found {
					t.Fatalf("blockers = %+v, want operation_in_progress", ws.Gates.Blockers)
				}
			}
		})
	}

	t.Run("gates error", func(t *testing.T) {
		f, wt := preservedWT(t)
		f.addRecord(t, wt, recordOpts{})
		ws, recs := stateOf(t, f, wt)
		if preservedOf(Git{Dir: f.Dir}, ws, recs) == nil {
			t.Fatal("control: the clean worktree should be preserved")
		}
		ws.GatesErr = "boom"
		if got := preservedOf(Git{Dir: f.Dir}, ws, recs); got != nil {
			t.Fatalf("preservedOf with GatesErr = %+v, want nil", got)
		}
	})

	t.Run("detached worktree with a branch ref", func(t *testing.T) {
		f := newFixture(t)
		wt := f.Worktree("loose")
		f.DetachOrphan(wt)
		f.addRecord(t, wt, recordOpts{SourceRef: "refs/heads/x"})
		ws, recs := stateOf(t, f, wt)
		if got := preservedOf(Git{Dir: f.Dir}, ws, recs); got != nil {
			t.Fatalf("preservedOf = %+v, want nil", got)
		}
	})

	t.Run("detached worktree with the detached ref matches", func(t *testing.T) {
		f := newFixture(t)
		wt := f.Worktree("loose")
		f.DetachOrphan(wt)
		f.addRecord(t, wt, recordOpts{})
		ws, recs := stateOf(t, f, wt)
		if got := preservedOf(Git{Dir: f.Dir}, ws, recs); got == nil {
			t.Fatal("preservedOf = nil, want a match")
		}
	})
}

// TestPreservedOfCoveredByARemoteStillCounts pushes the rescued commit, so a
// remote-tracking ref also holds it. Why: the check is that the record covers
// HEAD (count 0); a remote copy must not make the record look uncovering.
func TestPreservedOfCoveredByARemoteStillCounts(t *testing.T) {
	f, wt := preservedWT(t)
	gitIn(t, wt, "push", "-u", "origin", "topic")
	f.addRecord(t, wt, recordOpts{})
	ws, recs := stateOf(t, f, wt)
	if got := preservedOf(Git{Dir: f.Dir}, ws, recs); got == nil {
		t.Fatal("preservedOf = nil, want a match")
	}
}

// TestPreservedOfUsesTheMatchingRecordAmongSeveral keeps a stale record (older
// head) beside the right one. Why: any one matching record suffices, and a
// stale one must neither hide the match nor be chosen.
func TestPreservedOfUsesTheMatchingRecordAmongSeveral(t *testing.T) {
	f, wt := preservedWT(t)
	f.addRecord(t, wt, recordOpts{DestPath: "/tmp/stale"})
	commitIn(t, wt, "b.txt", "b", "b")
	good := f.addRecord(t, wt, recordOpts{DestPath: "/tmp/good"})
	ws, recs := stateOf(t, f, wt)
	if len(recs) != 2 {
		t.Fatalf("records = %+v, want 2", recs)
	}
	got := preservedOf(Git{Dir: f.Dir}, ws, recs)
	if got == nil || got.Ref != good || got.DestPath != "/tmp/good" {
		t.Fatalf("preservedOf = %+v, want the record %s", got, good)
	}
}

// TestPreservedOfIgnoresAForgedRecord feeds hand-made RescueRecord values:
// a ref that is not the prefix plus the commit's first 16 digits, a nested
// ref, and a commit id shorter than 16 characters. Why: only a record at the
// ref wt rescue would have made may preserve anything; the rest are inert and
// must not panic.
func TestPreservedOfIgnoresAForgedRecord(t *testing.T) {
	f, wt := preservedWT(t)
	ref := f.addRecord(t, wt, recordOpts{})
	ws, recs := stateOf(t, f, wt)
	if len(recs) != 1 {
		t.Fatalf("records = %+v", recs)
	}
	good := recs[0]
	forge := func(mut func(r *RescueRecord)) []RescueRecord {
		r := good
		mut(&r)
		return []RescueRecord{r}
	}
	for name, rs := range map[string][]RescueRecord{
		"wrong ref name": forge(func(r *RescueRecord) { r.Ref = RecordRefPrefix + "0000000000000000" }),
		"nested ref":     forge(func(r *RescueRecord) { r.Ref = ref + "/x" }),
		"short commit":   forge(func(r *RescueRecord) { r.Commit, r.Ref = "abc", RecordRefPrefix+"abc" }),
		"empty":          forge(func(r *RescueRecord) { *r = RescueRecord{} }),
		"nil":            nil,
	} {
		if got := preservedOf(Git{Dir: f.Dir}, ws, rs); got != nil {
			t.Errorf("%s: preservedOf = %+v, want nil", name, got)
		}
	}
	// Garbage trailers on a real ref never list, so they never reach it.
	bad := "x\n\nWt-Version: 1\nWt-Source-Path: /a\nWt-Source-Path: /b\nWt-Source-Ref: r\x01\n"
	f.addRecord(t, wt, recordOpts{Message: &bad})
	_, recs = stateOf(t, f, wt)
	if len(recs) != 1 {
		t.Fatalf("records = %+v, want only the good one", recs)
	}
}

// gitLog puts a logging git in front of PATH and returns a func reading the
// calls made since.
func gitLog(t *testing.T) func() []string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		b, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

// coverageCalls counts the git calls that check a record's coverage.
func coverageCalls(lines []string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, "--not --remotes "+RecordRefPrefix) {
			n++
		}
	}
	return n
}

// TestGatherAndRescanAgreeOnPreserved checks Gather marks the rescued
// worktree and not an unrescued one, that Rescan runs the same record check
// (seen as the coverage git call) and its row, Preserved included, equals the
// full run's. Why: both paths must go through
// worktreeState, or the removal re-check would judge a different state than
// the list showed.
func TestGatherAndRescanAgreeOnPreserved(t *testing.T) {
	f, wt := preservedWT(t)
	other := f.Worktree("other")
	commitIn(t, other, "o.txt", "o", "o")
	f.addRecord(t, wt, recordOpts{DestBranch: "rescued/topic"})
	noGh(t)
	o := Options{Now: later()}
	s := gather(t, f)
	byPath := map[string]WorktreeState{}
	for _, ws := range s.Worktrees {
		byPath[resolve(ws.Fact.Path)] = ws
	}
	if byPath[resolve(wt)].Preserved == nil {
		t.Fatal("Gather did not mark the rescued worktree")
	}
	if byPath[resolve(other)].Preserved != nil {
		t.Fatal("Gather marked an unrescued worktree")
	}
	topic := findRow(t, Classify(s, o), KindWorktree, "topic")
	otherRow := findRow(t, Classify(s, o), KindWorktree, "other")

	calls := gitLog(t)
	sameRow(t, mustRescan(t, f, s, o, targetOf(topic)), topic)
	if n := coverageCalls(calls()); n != 1 {
		t.Fatalf("rescan of the rescued worktree ran %d coverage checks, want 1", n)
	}
	sameRow(t, mustRescan(t, f, s, o, targetOf(otherRow)), otherRow)
	if n := coverageCalls(calls()); n != 1 {
		t.Fatalf("rescan of the unrescued worktree ran a coverage check (total %d)", n)
	}
}

// TestRescanReadsRecordsAfresh deletes the record between Gather and Rescan.
// Why: a rescan must never take records from the prior snapshot; the removal
// engine acts on the fresh state. Seen as the absence of the coverage check.
func TestRescanReadsRecordsAfresh(t *testing.T) {
	f, wt := preservedWT(t)
	ref := f.addRecord(t, wt, recordOpts{})
	noGh(t)
	o := Options{Now: later()}
	s := gather(t, f)
	if len(s.Records) != 1 {
		t.Fatalf("records = %+v", s.Records)
	}
	row := findRow(t, Classify(s, o), KindWorktree, "topic")
	f.git("update-ref", "-d", ref)
	calls := gitLog(t)
	mustRescan(t, f, s, o, targetOf(row))
	if n := coverageCalls(calls()); n != 0 {
		t.Fatalf("rescan checked coverage of %d records, want 0 (the record is gone)", n)
	}
}

// TestNoRecordsMeansNoExtraGitWork rescans through a logging git with no
// record ref present. Why: the common case must cost one for-each-ref over the
// record prefix and no coverage git work.
func TestNoRecordsMeansNoExtraGitWork(t *testing.T) {
	f, _ := preservedWT(t)
	noGh(t)
	o := Options{Now: later()}
	s := gather(t, f)
	row := findRow(t, Classify(s, o), KindWorktree, "topic")
	calls := gitLog(t)
	mustRescan(t, f, s, o, targetOf(row))
	lines := calls()
	n := 0
	for _, l := range lines {
		if strings.Contains(l, RecordRefPrefix) {
			n++
		}
		if strings.Contains(l, "cat-file -e") {
			t.Errorf("record coverage git call without a record: %s", l)
		}
	}
	if n != 1 || coverageCalls(lines) != 0 {
		t.Fatalf("%d calls name %s, %d coverage calls; log: %q", n, RecordRefPrefix, coverageCalls(lines), lines)
	}
}
