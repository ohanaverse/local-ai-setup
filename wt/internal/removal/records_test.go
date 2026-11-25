package removal

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// addRecord writes the chain a rescue writes (a commit with the six trailers
// whose parent is srcPath's HEAD) and points refs/wt/rescued/<16 hex> at it.
// It returns the ref and the commit. srcPath need not exist as a worktree: it
// is only the trailer value (a record whose source is gone).
func (f *fixture) addRecord(srcPath, srcRef, srcHead string) (ref, commit string) {
	f.t.Helper()
	msg := "wt rescue record\n\n" + strings.Join([]string{
		"Wt-Version: 1", "Wt-Source-Path: " + srcPath, "Wt-Source-Ref: " + srcRef,
		"Wt-Source-Head: " + srcHead, "Wt-Dest-Branch: rescue/x", "Wt-Dest-Path: /tmp/dest",
	}, "\n") + "\n"
	cmd := exec.Command("git", "-C", f.Dir, "commit-tree", srcHead+"^{tree}", "-p", srcHead, "-F", "-")
	cmd.Stdin = strings.NewReader(msg)
	b, err := cmd.Output()
	if err != nil {
		f.t.Fatalf("commit-tree: %v", err)
	}
	commit = strings.TrimSpace(string(b))
	ref = cleanup.RecordRefPrefix + commit[:16]
	f.git("update-ref", ref, commit)
	return ref, commit
}

// recordFixture is a repo with one record whose source worktree is gone.
func recordFixture(t *testing.T) (*fixture, string, string) {
	t.Helper()
	noGh(t)
	f := newFixture(t)
	ref, commit := f.addRecord("/nonexistent/source", "refs/heads/old", f.git("rev-parse", "HEAD"))
	return f, ref, commit
}

// recordPrune previews the repo's record ref with the clock age ahead and
// confirms it with approved, returning the engine and the item.
func recordPrune(t *testing.T, f *fixture, ref string, age time.Duration, approved Needs, o Options) (Engine, RecordItem) {
	t.Helper()
	s, _ := f.report()
	// The row is as the list showed it with the clock age ahead; the engine
	// judges the age again at prune time with the same clock.
	var row *cleanup.RecordRow
	rows := cleanup.RecordRows(s, time.Now().Add(age))
	for i := range rows {
		if rows[i].Ref == ref {
			row = &rows[i]
		}
	}
	if row == nil {
		t.Fatalf("no record %s: %+v", ref, rows)
	}
	o.Now = func() time.Time { return time.Now().Add(age) }
	e := f.engine(s, f.inputs(), o)
	p := e.PreviewRecord(*row)
	it, err := p.Confirmed(approved)
	if err != nil {
		t.Fatalf("Confirmed: %v", err)
	}
	return e, it
}

const day = 24 * time.Hour

// TestPruneRecordHappyPath checks an old record whose source is gone is
// pruned: the ref no longer exists, the commit is kept in the outcome and the
// warning carries the command that restores it. Why: this is the one
// supported way to drop a record, and the commit id is the only way back.
func TestPruneRecordHappyPath(t *testing.T) {
	f, ref, commit := recordFixture(t)
	e, it := recordPrune(t, f, ref, 30*day, Needs{}, Options{})
	o := e.PruneRecord(it)
	if o.Outcome != RecPruned || o.Commit != commit {
		t.Fatalf("outcome = %+v", o)
	}
	if _, ok := f.refTip(ref); ok {
		t.Fatal("the record ref still exists")
	}
	if !strings.Contains(strings.Join(o.Warnings, "\n"), "git update-ref "+ref+" "+commit) {
		t.Fatalf("warnings = %v, want the restore command", o.Warnings)
	}
	if f.git("cat-file", "-t", commit) != "commit" {
		t.Fatal("the commit itself must stay until gc")
	}
}

// TestPruneRecordInUseIsRefused checks a record a live worktree still matches
// is refused whatever the approvals, and its ref stays. Why: that record is
// the only copy of the worktree's rescued state (the hard rule of #543).
func TestPruneRecordInUseIsRefused(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	ref, _ := f.addRecord(cleanup.ResolvePath(f.Dir), "refs/heads/main", f.git("rev-parse", "HEAD"))
	s, rep := f.report()
	if len(rep.Records) != 1 || rep.Records[0].Status != cleanup.RecordInUse {
		t.Fatalf("records = %+v, want one in_use", rep.Records)
	}
	e := f.engine(s, f.inputs(), Options{})
	p := e.PreviewRecord(rep.Records[0])
	if _, err := p.Confirmed(Needs{Confirm: true}); err == nil {
		t.Fatal("an in-use preview must not confirm")
	}
	if _, ok := f.refTip(ref); !ok {
		t.Fatal("the in-use record ref is gone")
	}
}

// TestPruneRecordRechecksWorktreesAtDeleteTime checks the engine re-reads the
// worktrees when it prunes: an item previewed as unused is refused once a
// worktree matches the record. Why: the snapshot the user saw can be stale.
func TestPruneRecordRechecksWorktreesAtDeleteTime(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	head := f.git("rev-parse", "HEAD")
	// Source: a branch worktree that does not exist yet.
	src := cleanup.ResolvePath(f.Dir + "/.worktrees/late")
	ref, _ := f.addRecord(src, "refs/heads/late", head)
	e, it := recordPrune(t, f, ref, 30*day, Needs{}, Options{})
	f.git("worktree", "add", "-b", "late", src, head)
	o := e.PruneRecord(it)
	if o.Outcome != RecRefusedInUse || o.Commit != "" {
		t.Fatalf("outcome = %+v, want refused_in_use", o)
	}
	if _, ok := f.refTip(ref); !ok {
		t.Fatal("the record ref was removed")
	}
}

// TestPruneRecordRecentNeedsConfirm checks a record under the age floor is
// retained without Confirm and pruned with it, and that Confirm is not
// accepted for an old record. Why: a fresh record is usually the only copy of
// a just-rescued worktree's changes (decision of #543).
func TestPruneRecordRecentNeedsConfirm(t *testing.T) {
	f, ref, _ := recordFixture(t)
	s, rep := f.report()
	e := f.engine(s, f.inputs(), Options{Now: func() time.Time { return time.Now().Add(time.Hour) }})
	p := e.PreviewRecord(rep.Records[0])
	if rep.Records[0].Status != cleanup.RecordRecent || !p.Needs.Confirm {
		t.Fatalf("row %+v needs %+v, want recent and Confirm", rep.Records[0], p.Needs)
	}
	if _, err := p.Confirmed(Needs{}); err == nil {
		t.Fatal("a recent record must not confirm without Confirm")
	}
	// A preview shown as old, run while the record is young: retained.
	e30, it := recordPrune(t, f, ref, 30*day, Needs{}, Options{})
	e30.Opts.Now = func() time.Time { return time.Now().Add(time.Hour) }
	if o := e30.PruneRecord(it); o.Outcome != RecRetainedRecent {
		t.Fatalf("outcome = %+v, want retained_recent", o)
	}
	if _, ok := f.refTip(ref); !ok {
		t.Fatal("the ref went")
	}
	ec, itc := recordPrune(t, f, ref, time.Hour, Needs{Confirm: true}, Options{})
	if o := ec.PruneRecord(itc); o.Outcome != RecPruned {
		t.Fatalf("with Confirm: %+v", o)
	}
	// An old record takes no Confirm.
	f2, ref2, _ := recordFixture(t)
	s2, rep2 := f2.report()
	rep2.Records[0].Status, rep2.Records[0].AgeDays = cleanup.RecordUnused, 40
	p2 := f2.engine(s2, f2.inputs(), Options{}).PreviewRecord(rep2.Records[0])
	if _, err := p2.Confirmed(Needs{Confirm: true}); err == nil {
		t.Fatalf("Confirm on an old record (%s) must be refused as unrequired", ref2)
	}
}

// TestPruneRecordRefusesAForeignRefOrShortCommit checks the naming gate: a
// ref outside refs/wt/rescued/, one not named by its commit's first 16
// digits, and a short commit id never reach git. Why: update-ref -d must
// never be pointed at a branch or tag through this path.
func TestPruneRecordRefusesAForeignRefOrShortCommit(t *testing.T) {
	f, ref, commit := recordFixture(t)
	calls := recordRefGit(t)
	s, rep := f.report()
	e := f.engine(s, f.inputs(), Options{Now: func() time.Time { return time.Now().Add(30 * day) }})
	cases := map[string]cleanup.RecordRow{
		"branch ref":      {Ref: "refs/heads/main", ID: "main", Commit: commit, Status: cleanup.RecordUnused},
		"wrong id":        {Ref: cleanup.RecordRefPrefix + strings.Repeat("0", 16), ID: "x", Commit: commit, Status: cleanup.RecordUnused},
		"short commit":    {Ref: ref, ID: "x", Commit: commit[:16], Status: cleanup.RecordUnused},
		"prefix only ref": {Ref: cleanup.RecordRefPrefix, ID: "", Commit: commit, Status: cleanup.RecordUnused},
	}
	_ = rep
	for name, row := range cases {
		it, err := e.PreviewRecord(row).Confirmed(Needs{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if o := e.PruneRecord(it); o.Outcome != RecFailed || !strings.Contains(o.Reason, "not a rescue record ref") {
			t.Errorf("%s: outcome = %+v", name, o)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("git deletion calls = %v, want none", *calls)
	}
	if _, ok := f.refTip(ref); !ok {
		t.Fatal("the record went")
	}
}

// TestPruneRecordDryRunChangesNothing checks a dry run decides and reports
// pruned ("would prune") without a delete. Why: --dry-run must be safe.
func TestPruneRecordDryRunChangesNothing(t *testing.T) {
	f, ref, _ := recordFixture(t)
	calls := recordRefGit(t)
	e, it := recordPrune(t, f, ref, 30*day, Needs{}, Options{DryRun: true})
	o := e.PruneRecord(it)
	if o.Outcome != RecPruned || !strings.HasPrefix(o.Reason, "would prune") || o.Commit != "" {
		t.Fatalf("outcome = %+v", o)
	}
	if len(*calls) != 0 {
		t.Fatalf("calls = %v", *calls)
	}
	if _, ok := f.refTip(ref); !ok {
		t.Fatal("dry run removed the ref")
	}
}

// TestPruneRecordSkipsAMovedOrGoneRecord checks a record deleted or moved to
// another commit since the user saw it is skipped_changed. Why: the item must
// act on what was shown, never on whatever the ref names now.
func TestPruneRecordSkipsAMovedOrGoneRecord(t *testing.T) {
	f, ref, _ := recordFixture(t)
	e, it := recordPrune(t, f, ref, 30*day, Needs{}, Options{})
	f.git("update-ref", ref, f.git("rev-parse", "HEAD")) // now a non-record commit
	if o := e.PruneRecord(it); o.Outcome != RecSkippedChanged {
		t.Fatalf("moved: %+v", o)
	}
	f.git("update-ref", "-d", ref)
	if o := e.PruneRecord(it); o.Outcome != RecSkippedChanged || !strings.Contains(o.Reason, "no longer exists") {
		t.Fatalf("gone: %+v", o)
	}
}

// TestPruneRecordCompareAndSwap checks that a ref that moves between the
// fresh listing and update-ref is left alone (git's compare-and-swap fails)
// and reported skipped_changed. Why: the delete is on the commit the user saw.
func TestPruneRecordCompareAndSwap(t *testing.T) {
	f, ref, _ := recordFixture(t)
	head := f.git("rev-parse", "HEAD")
	stubRefGit(t, func(g cleanup.Git, args ...string) (cleanup.Out, error) {
		f.git("update-ref", ref, head) // moved after the checks
		return g.Run(args...)
	})
	e, it := recordPrune(t, f, ref, 30*day, Needs{}, Options{})
	o := e.PruneRecord(it)
	if o.Outcome != RecSkippedChanged {
		t.Fatalf("outcome = %+v", o)
	}
	if tip, ok := f.refTip(ref); !ok || tip != head {
		t.Fatalf("ref = %q, %v; want it left at the new commit", tip, ok)
	}
}

// TestPruneRecordReadBackCatchesARecreatedRef checks a ref that git deleted
// and someone re-created at another commit is not reported pruned, and the
// deleted commit is kept in the outcome. Why: git's exit status alone is
// never taken for the result.
func TestPruneRecordReadBackCatchesARecreatedRef(t *testing.T) {
	f, ref, commit := recordFixture(t)
	head := f.git("rev-parse", "HEAD")
	stubRefGit(t, func(g cleanup.Git, args ...string) (cleanup.Out, error) {
		o, err := g.Run(args...)
		f.git("update-ref", ref, head)
		return o, err
	})
	e, it := recordPrune(t, f, ref, 30*day, Needs{}, Options{})
	o := e.PruneRecord(it)
	if o.Outcome != RecSkippedChanged || o.Commit != commit {
		t.Fatalf("outcome = %+v, want skipped_changed keeping %s", o, commit)
	}
}

// TestPruneRecordRefusesUnsealedItems checks a hand-built item and an edited
// confirmed one never reach git. Why: only what the user confirmed may run.
func TestPruneRecordRefusesUnsealedItems(t *testing.T) {
	f, ref, commit := recordFixture(t)
	calls := recordRefGit(t)
	e, it := recordPrune(t, f, ref, 30*day, Needs{}, Options{})
	hand := RecordItem{Ref: ref, Commit: commit, Name: "x"}
	edited := it
	edited.Approved.Confirm = true
	for name, bad := range map[string]RecordItem{"hand-built": hand, "edited": edited} {
		if o := e.PruneRecord(bad); o.Outcome != RecFailed || !strings.Contains(o.Reason, "not confirmed") {
			t.Errorf("%s: %+v", name, o)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("calls = %v", *calls)
	}
	// A hand-built preview does not confirm either.
	if _, err := (RecordPreview{Row: cleanup.RecordRow{Ref: ref, Commit: commit}}).Confirmed(Needs{}); err == nil {
		t.Fatal("hand-built preview confirmed")
	}
}

// TestRecordOutcomesEncode checks the additive records section: the enum
// strings, the commit only when set, and that an outcome outside the contract
// is written invalid with a partial-report error. Why: the JSON is a frozen
// contract held by the schema.
func TestRecordOutcomesEncode(t *testing.T) {
	commit := strings.Repeat("a", 40)
	it := RecordItem{Ref: cleanup.RecordRefPrefix + commit[:16], Commit: commit, Name: commit[:16]}
	good := RecordOutcome{Item: it, Outcome: RecPruned, Reason: "pruned", Commit: commit, Warnings: []string{"restore with: x"}}
	skipped := RecordOutcome{Item: it, Outcome: RecRetainedRecent, Reason: "young"}
	b, partial, err := EncodeReport(nil, Meta{Repo: "/r", Records: []RecordOutcome{good, skipped}})
	if err != nil || partial {
		t.Fatalf("encode: %v partial=%v", err, partial)
	}
	var env struct {
		Items   []any `json:"items"`
		Records []struct {
			Outcome, Commit string
			Warnings        []string
		} `json:"records"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Items) != 0 || len(env.Records) != 2 || env.Records[0].Outcome != "pruned" ||
		env.Records[0].Commit != commit || env.Records[1].Commit != "" || env.Records[1].Warnings == nil {
		t.Fatalf("doc = %s", b)
	}
	bad := RecordOutcome{Item: it, Outcome: "bogus"}
	_, partial, _ = EncodeReport(nil, Meta{Records: []RecordOutcome{bad}})
	if !partial {
		t.Fatal("an off-contract record outcome must make a partial report")
	}
	// No records: the key is absent.
	plain, _, _ := EncodeReport(nil, Meta{Repo: "/r"})
	if strings.Contains(string(plain), `"records"`) {
		t.Fatalf("plain run carries records: %s", plain)
	}
	if h := RecordHeadline(good); h != fmt.Sprintf("record %s: pruned", it.Name) {
		t.Fatalf("headline = %q", h)
	}
	if d := RecordDetailLines(good); len(d) != 2 || d[1] != "record commit was "+commit {
		t.Fatalf("details = %v", d)
	}
}
