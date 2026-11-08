package removal

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
)

// rescued is a worktree wt rescue has copied: the source, its record and the
// copy rescue made.
type rescued struct {
	Src, Ref, Tip      string // the original worktree, the record ref, the original's branch tip
	DestPath, DestBr   string // the copy's worktree and branch (short name)
	DestTip, RecordTip string // the copy's branch tip and the record's commit, as made
}

// rescuedTopic is a clean linked worktree "topic" with one unpushed commit,
// rescued by the real internal/rescue engine.
func rescuedTopic(t *testing.T, f *fixture) rescued {
	t.Helper()
	src := f.Worktree("topic")
	f.Commit(src, "topic.txt", "work", "topic work")
	return rescueWorktree(t, f, src)
}

// rescueWorktree runs the real internal/rescue engine (Preflight then
// Execute) on the worktree at src, so the record and the destination are
// exactly what wt rescue leaves.
func rescueWorktree(t *testing.T, f *fixture, src string) rescued {
	t.Helper()
	noGh(t)
	s, r := f.report()
	eng := rescue.Engine{G: cleanup.Git{Dir: f.Dir}, Prior: s, In: f.inputs(), Classify: cleanup.Options{Now: later()}}
	plan, err := eng.Preflight(rowAt(t, r, src), "")
	if err != nil {
		t.Fatalf("rescue Preflight: %v", err)
	}
	o := eng.Execute(plan)
	if o.Status != rescue.StatusMoved || o.Record == nil {
		t.Fatalf("rescue = %s %s %s, record %v; want moved with a record", o.Status, o.Code, o.Reason, o.Record)
	}
	return rescued{
		Src: src, Ref: o.Record.Ref, Tip: gitIn(t, src, "rev-parse", "HEAD"),
		DestPath: plan.DestPath, DestBr: plan.Branch,
		DestTip: f.git("rev-parse", "refs/heads/"+plan.Branch), RecordTip: f.git("rev-parse", o.Record.Ref),
	}
}

// rowAt is the report's worktree row at path.
func rowAt(t *testing.T, r cleanup.Report, path string) cleanup.Row {
	t.Helper()
	for _, row := range r.Rows {
		if row.Kind == cleanup.KindWorktree && cleanup.ResolvePath(row.Path) == cleanup.ResolvePath(path) {
			return row
		}
	}
	t.Fatalf("no worktree row at %s", path)
	return cleanup.Row{}
}

// preservedRow is the report's row of the rescued original "topic", failing
// unless a record preserves it and its raw losses still count the commit.
func preservedRow(t *testing.T, f *fixture, r cleanup.Report) cleanup.Row {
	t.Helper()
	row := f.row(r, cleanup.KindWorktree, "topic")
	if row.Preserved == nil || row.Evidence != cleanup.EvRescued || row.Losses.Unpushed != 1 {
		t.Fatalf("precondition: row %s ev=%s preserved=%v unpushed=%d; want a preserved original with 1 raw unpushed",
			row.Status, row.Evidence, row.Preserved, row.Losses.Unpushed)
	}
	return row
}

// refExists reports whether git resolves ref, asked through for-each-ref (an
// exact listing, never a guess).
func (f *fixture) refExists(ref string) bool {
	f.t.Helper()
	return f.git("for-each-ref", "--format=%(refname)", ref) == ref
}

// TestEngineRemovesAPreservedOriginalWithAPlainItem removes a rescued, clean
// original through the real Preview -> Confirmed(Needs{}) -> Run path: it
// needs no approval at all, the worktree goes, its branch is deleted unasked
// with no gc caveat (the copy's branch still reaches the tip), and the copy,
// its branch and the record are untouched. Why: this is what the record is
// for; raw Losses still count the commit, so any removal site reading them
// raw would demand --accept-loss or skip the item as changed.
func TestEngineRemovesAPreservedOriginalWithAPlainItem(t *testing.T) {
	f := newFixture(t)
	rs := rescuedTopic(t, f)
	s, r := f.report()
	row := preservedRow(t, f, r)
	if row.Status != cleanup.StatusProven || !row.BranchProven {
		t.Fatalf("precondition: status %s branchProven %v", row.Status, row.BranchProven)
	}
	eng := f.engine(s, f.inputs(), Options{})
	p := previewOne(t, eng, ItemFromRow(row))
	if p.Needs != (Needs{}) {
		t.Fatalf("Needs = %+v, want none", p.Needs)
	}
	it, err := p.Confirmed(Needs{})
	if err != nil {
		t.Fatalf("Confirmed(Needs{}): %v", err)
	}
	o := one(t, eng, it)
	if o.Worktree != WtRemoved || o.Branch != BrDeleted {
		t.Fatalf("outcome %s/%s (%s), want removed/deleted", o.Worktree, o.Branch, o.Reason)
	}
	if exists(t, rs.Src) || f.registered(rs.Src) || f.refExists("refs/heads/topic") {
		t.Error("the original worktree or its branch is still there")
	}
	if slices.Contains(o.Warnings, gcCaveat) {
		t.Errorf("warnings %q carry the gc caveat", o.Warnings)
	}
	if len(o.LossLines) != 0 {
		t.Errorf("loss lines %q, want none", o.LossLines)
	}
	if !exists(t, filepath.Join(rs.DestPath, "topic.txt")) || !f.registered(rs.DestPath) {
		t.Error("the copy's worktree was touched")
	}
	if got := f.git("rev-parse", "refs/heads/"+rs.DestBr); got != rs.DestTip {
		t.Errorf("copy branch at %s, want %s", got, rs.DestTip)
	}
	if !f.refExists(rs.Ref) || f.git("rev-parse", rs.Ref) != rs.RecordTip {
		t.Errorf("record %s gone or moved", rs.Ref)
	}
}

// TestEngineRemovesAPreservedDetachedOriginal removes a rescued detached
// worktree (raw DetachedUnique true) through Preview -> Confirmed(Needs{}) ->
// Run. Why: needsOf's Force reads DetachedUnique; read raw, a rescued
// detached original would still demand --force for commits its record keeps.
func TestEngineRemovesAPreservedDetachedOriginal(t *testing.T) {
	f := newFixture(t)
	src := filepath.Join(f.Dir, ".worktrees", "scratch")
	f.git("worktree", "add", "--detach", src)
	f.Commit(src, "a.txt", "a", "detached work")
	rs := rescueWorktree(t, f, src)
	s, r := f.report()
	row := rowAt(t, r, src)
	if row.Preserved == nil || !row.Losses.DetachedUnique || row.Status != cleanup.StatusProven {
		t.Fatalf("precondition: status %s preserved %v detachedUnique %v", row.Status, row.Preserved, row.Losses.DetachedUnique)
	}
	eng := f.engine(s, f.inputs(), Options{})
	p := previewOne(t, eng, ItemFromRow(row))
	if p.Needs != (Needs{}) || p.Approval != ApprovalProven {
		t.Fatalf("needs %+v approval %s (%s), want none and proven", p.Needs, p.Approval, p.Reason)
	}
	it, err := p.Confirmed(Needs{})
	if err != nil {
		t.Fatal(err)
	}
	if o := one(t, eng, it); o.Worktree != WtRemoved {
		t.Fatalf("outcome %s (%s), want removed", o.Worktree, o.Reason)
	}
	if exists(t, src) || !f.refExists(rs.Ref) {
		t.Error("the original is still there, or the record is gone")
	}
}

// TestItemFromRowSealsTheEffectiveLosses checks ItemFromRow seals a preserved
// row's effective losses (0 unpushed), not the raw count, and that an item
// sealed so (itemOf, which is how the tests hand rows over) is removed: the
// gate compares the fresh effective losses with it. Why: a raw seal against
// an effective fresh value (or the reverse) would mis-judge every preserved
// item as changed or hide a lapse in the numbers.
func TestItemFromRowSealsTheEffectiveLosses(t *testing.T) {
	f := newFixture(t)
	rescuedTopic(t, f)
	s, r := f.report()
	row := preservedRow(t, f, r)
	if got := ItemFromRow(row).SawLosses; got != row.EffectiveLosses() || got.Unpushed != 0 {
		t.Fatalf("ItemFromRow sealed %+v, want the effective %+v", got, row.EffectiveLosses())
	}
	if o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(row)); o.Worktree != WtRemoved || o.Branch != BrDeleted {
		t.Fatalf("outcome %s/%s (%s), want removed/deleted", o.Worktree, o.Branch, o.Reason)
	}
}

// TestPreviewOfAPreservedOriginalIsProven checks the preview of a rescued,
// clean original: ApprovalProven, no loss line and no "branch will be kept"
// note (the branch is deleted with it). Why: the confirmation the TUI and
// wt remove show is built from this preview; a raw read would list the
// covered commit as a loss.
func TestPreviewOfAPreservedOriginalIsProven(t *testing.T) {
	f := newFixture(t)
	rescuedTopic(t, f)
	s, r := f.report()
	p := previewOne(t, f.engine(s, f.inputs(), Options{}), ItemFromRow(preservedRow(t, f, r)))
	if p.Approval != ApprovalProven {
		t.Errorf("approval %s (%s), want proven", p.Approval, p.Reason)
	}
	if len(p.LossLines) != 0 {
		t.Errorf("loss lines %q, want none", p.LossLines)
	}
	if len(p.Notes) != 0 || p.CanDeleteBranch {
		t.Errorf("notes %q, canDeleteBranch %v; want none (the branch is deleted)", p.Notes, p.CanDeleteBranch)
	}
	if p.Plan.Branch != BrDeleted {
		t.Errorf("planned branch outcome %s, want deleted", p.Plan.Branch)
	}
}

// TestPreviewOfAPreservedOriginalWithAnIgnoredFile adds an ignored file to a
// rescued original (a record does not keep ignored files): the item needs
// Confirm and nothing else, and its one loss line is the ignored entry.
// Why: the view clears only the covered commits; what the record does not
// hold must still be declared and confirmed.
func TestPreviewOfAPreservedOriginalWithAnIgnoredFile(t *testing.T) {
	f := newFixture(t)
	rs := rescuedTopic(t, f)
	f.Ignored(rs.Src, "cache.bin")
	s, r := f.report()
	p := previewOne(t, f.engine(s, f.inputs(), Options{}), ItemFromRow(preservedRow(t, f, r)))
	if p.Approval != ApprovalNeedsConfirm || p.Needs != (Needs{Confirm: true}) {
		t.Errorf("approval %s needs %+v, want needs_confirm with Confirm only", p.Approval, p.Needs)
	}
	if len(p.LossLines) != 1 || !strings.Contains(p.LossLines[0], "ignored") {
		t.Errorf("loss lines %q, want the one ignored-files line", p.LossLines)
	}
}

// TestPreservedItemLapses seals a preview of a rescued original, then breaks
// the preservation before Run in each way it can lapse; each is
// skipped_changed (or refused, for a rescan that fails) with nothing removed.
// Why: the seal holds the effective losses; when the record no longer
// covers the worktree, Run must notice instead of deleting the commits. A
// failing Rescan is refused, not skipped_changed: the engine refuses any
// re-check error other than "target gone" (controller ruling 1); a record
// listing failure inside Rescan degrades to "no records" and reads as the
// status change instead.
func TestPreservedItemLapses(t *testing.T) {
	cases := []struct {
		name   string
		lapse  func(t *testing.T, f *fixture, rs rescued)
		want   WorktreeOutcome
		reason string
	}{
		{"record deleted", func(t *testing.T, f *fixture, rs rescued) {
			f.git("update-ref", "-d", rs.Ref)
		}, WtSkippedChanged, "status changed since it was listed (unique_work, was proven)"},
		{"new commit", func(t *testing.T, f *fixture, rs rescued) {
			f.Commit(rs.Src, "more.txt", "more", "more work")
		}, WtSkippedChanged, "status changed since it was listed (unique_work, was proven)"},
		{"branch moved", func(t *testing.T, f *fixture, rs rescued) {
			// Back to the base: proven again (same-commit), but not the tip
			// the user saw.
			gitIn(t, rs.Src, "reset", "--hard", "main")
		}, WtSkippedChanged, "branch moved since it was listed"},
		{"record commit unreadable", func(t *testing.T, f *fixture, rs rescued) {
			obj := f.git("rev-parse", "--path-format=absolute", "--git-path", "objects/"+rs.RecordTip[:2]+"/"+rs.RecordTip[2:])
			if err := os.Remove(obj); err != nil {
				t.Fatalf("remove the record's object: %v", err)
			}
		}, WtSkippedChanged, "status changed since it was listed (unique_work, was proven)"},
		{"rescan fails", func(t *testing.T, f *fixture, rs rescued) {
			old := rescan
			rescan = func(cleanup.Git, cleanup.Snapshot, cleanup.GatherInputs, cleanup.Options, cleanup.Target) (cleanup.Row, error) {
				return cleanup.Row{}, errors.New("listing broke")
			}
			t.Cleanup(func() { rescan = old })
		}, WtRefused, "could not re-check: listing broke"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			rs := rescuedTopic(t, f)
			s, r := f.report()
			eng := f.engine(s, f.inputs(), Options{})
			p := previewOne(t, eng, ItemFromRow(preservedRow(t, f, r)))
			it, err := p.Confirmed(p.Needs)
			if err != nil || p.Needs != (Needs{}) {
				t.Fatalf("Confirmed(%+v): %v; want a plain item", p.Needs, err)
			}
			c.lapse(t, f, rs)
			o := one(t, eng, it)
			if o.Worktree != c.want || o.Reason != c.reason || o.Branch != BrNotAttempted {
				t.Fatalf("outcome %s/%s %q, want %s/not_attempted %q", o.Worktree, o.Branch, o.Reason, c.want, c.reason)
			}
			if !exists(t, rs.Src) || !f.registered(rs.Src) || !f.refExists("refs/heads/topic") {
				t.Error("something was removed")
			}
		})
	}
}

// TestPreservedItemLapsesUnderAPRReason seals a preview of a rescued original
// whose status a PR reason already holds below proven (an open PR on its
// branch: unique_work; a PR lookup that failed: less_sure), deletes the
// record before Run and expects skipped_changed with nothing removed.
//
// Open PR: the status stays unique_work and the tip does not move, so the
// only comparison that sees the record go is the effective lossesWorse (the
// sealed effective Unpushed 0 against the fresh raw 1); this is the lapse no
// TestPreservedItemLapses case reaches. A raw seal (ItemFromRow sealing
// Losses) would let it through: raw 1 against fresh 1 is not worse.
//
// PR lookup failed: the record carried the row from unique_work to
// less_sure, so losing it drops the status back to unique_work and the
// status comparison fires first (before the tip and losses checks).
//
// Each case also runs once with the record left in place: the same sealed
// item is removed (worktree removed, its unproven branch kept). That control
// is what makes a raw read at the gate visible: compared raw, the fresh
// Unpushed 1 is "worse" than the sealed 0 even with the record intact, so
// the preview itself (and the control's Run) would read skipped_changed.
//
// Why: a preserved original that also has a PR reason still needs only the
// record to cover its commits; when the record goes, Run must not delete them.
func TestPreservedItemLapsesUnderAPRReason(t *testing.T) {
	cases := []struct {
		name   string
		gh     func(t *testing.T)
		status cleanup.Status
		code   string
		reason string
	}{
		{"open PR", func(t *testing.T) {
			t.Cleanup(cleanup.StubPRListForTest(func(_, b string) ([]byte, error) {
				if b == "topic" {
					return []byte(`[{"number":9,"state":"OPEN","headRefOid":"0000000000000000000000000000000000000000","baseRefName":"main","isCrossRepository":false,"headRepository":{"name":"r","nameWithOwner":"o/r"}}]`), nil
				}
				return []byte("[]"), nil
			}))
		}, cleanup.StatusUniqueWork, cleanup.ReasonOpenPR, "what would be lost changed since it was listed"},
		{"PR lookup failed", func(t *testing.T) {
			t.Cleanup(cleanup.StubPRListForTest(func(string, string) ([]byte, error) { return nil, errors.New("gh: offline") }))
		}, cleanup.StatusLessSure, cleanup.ReasonPRUnknown, "status changed since it was listed (unique_work, was less_sure)"},
	}
	for _, c := range cases {
		for _, lapse := range []bool{false, true} {
			name := c.name + "/record kept"
			if lapse {
				name = c.name + "/record deleted"
			}
			t.Run(name, func(t *testing.T) { preservedPRCase(t, c.gh, c.status, c.code, c.reason, lapse) })
		}
	}
}

// preservedPRCase is one TestPreservedItemLapsesUnderAPRReason run: with
// lapse the record is deleted between Confirmed and Run and the item must be
// skipped as reason with nothing removed; without it the item is removed.
func preservedPRCase(t *testing.T, gh func(*testing.T), status cleanup.Status, code, reason string, lapse bool) {
	f := newFixture(t)
	rs := rescuedTopic(t, f)
	gh(t)
	s, r := f.report()
	row := preservedRow(t, f, r)
	if row.Status != status || !hasReason(row, code) || row.EffectiveLosses().Unpushed != 0 {
		t.Fatalf("precondition: status %s reasons %+v effective unpushed %d; want %s with %s and 0",
			row.Status, row.Reasons, row.EffectiveLosses().Unpushed, status, code)
	}
	eng := f.engine(s, f.inputs(), Options{})
	p := previewOne(t, eng, ItemFromRow(row))
	if p.Plan.Worktree != WtRemoved {
		t.Fatalf("preview plans %s (%s), want removed", p.Plan.Worktree, p.Plan.Reason)
	}
	if !p.Needs.Confirm {
		t.Fatalf("Needs = %+v, want Confirm", p.Needs)
	}
	it, err := p.Confirmed(p.Needs)
	if err != nil {
		t.Fatalf("Confirmed(%+v): %v", p.Needs, err)
	}
	if !lapse {
		if o := one(t, eng, it); o.Worktree != WtRemoved {
			t.Fatalf("outcome %s/%s %q, want removed with the record kept", o.Worktree, o.Branch, o.Reason)
		}
		if exists(t, rs.Src) || !f.refExists("refs/heads/topic") {
			t.Error("the original worktree is still there, or its unproven branch is gone")
		}
		return
	}
	f.git("update-ref", "-d", rs.Ref)
	o := one(t, eng, it)
	if o.Worktree != WtSkippedChanged || o.Reason != reason || o.Branch != BrNotAttempted {
		t.Fatalf("outcome %s/%s %q, want skipped_changed/not_attempted %q", o.Worktree, o.Branch, o.Reason, reason)
	}
	if !exists(t, rs.Src) || !f.registered(rs.Src) || !f.refExists("refs/heads/topic") {
		t.Error("something was removed")
	}
}

// TestTipHeldByTheRecordAlone deletes the copy's worktree and branch first,
// so only the record still reaches the original's tip, then removes the
// original: its branch is deleted with no gc caveat. Why: the record holds
// the commit against gc, so warning that the commit survives only until the
// next gc would be false.
func TestTipHeldByTheRecordAlone(t *testing.T) {
	f := newFixture(t)
	rs := rescuedTopic(t, f)
	f.git("worktree", "remove", "--force", rs.DestPath)
	f.git("branch", "-D", rs.DestBr)
	s, r := f.report()
	row := preservedRow(t, f, r)
	if row.Status != cleanup.StatusProven {
		t.Fatalf("precondition: status %s", row.Status)
	}
	eng := f.engine(s, f.inputs(), Options{})
	it, err := previewOne(t, eng, ItemFromRow(row)).Confirmed(Needs{})
	if err != nil {
		t.Fatal(err)
	}
	o := one(t, eng, it)
	if o.Worktree != WtRemoved || o.Branch != BrDeleted {
		t.Fatalf("outcome %s/%s (%s), want removed/deleted", o.Worktree, o.Branch, o.Reason)
	}
	if slices.Contains(o.Warnings, gcCaveat) {
		t.Errorf("warnings %q carry the gc caveat; the record holds the tip", o.Warnings)
	}
	if !f.refExists(rs.Ref) {
		t.Error("record gone")
	}
}

// TestTipHeldElsewhereCountsARecordRef asks tipHeldElsewhere about a commit
// only a refs/wt/rescued/ ref reaches (yes), and about one nothing reaches
// (no). Why: the record namespace must count as a holder, and only it was
// added; an unreachable tip must still get the caveat.
func TestTipHeldElsewhereCountsARecordRef(t *testing.T) {
	f := newFixture(t)
	tree := f.git("rev-parse", "HEAD^{tree}")
	held := f.git("commit-tree", tree, "-p", "HEAD", "-m", "held")
	loose := f.git("commit-tree", tree, "-p", "HEAD", "-m", "loose")
	f.git("update-ref", cleanup.RecordRefPrefix+held[:16], held)
	e := f.engine(cleanup.Snapshot{}, f.inputs(), Options{})
	if !e.tipHeldElsewhere("gone", held) {
		t.Error("a commit a record ref holds reads as held by nothing")
	}
	if e.tipHeldElsewhere("gone", loose) {
		t.Error("an unreachable commit reads as held")
	}
}

// TestBranchStageKnowsThePreservedCode checks that onlyVerdictUnknown reads a
// row whose only Unknown is about the merge verdict as such even when it also
// carries the informational preserved reason. Why: an unlisted code fails
// closed as an Unknown, so a preserved row that is also Unknown and approved
// with -D would keep its branch only for carrying preserved.
func TestBranchStageKnowsThePreservedCode(t *testing.T) {
	r := cleanup.Row{Unknown: true, Reasons: []cleanup.Reason{
		{Code: cleanup.ReasonPreserved, Detail: "preserved by refs/wt/rescued/x (copy: /tmp/c)"},
		{Code: cleanup.ReasonMergeProofUnavailable},
	}}
	if !onlyVerdictUnknown(r) {
		t.Error("preserved is read as an Unknown")
	}
	if !knownNotUnknown[cleanup.ReasonPreserved] {
		t.Error("knownNotUnknown lacks preserved")
	}
}
