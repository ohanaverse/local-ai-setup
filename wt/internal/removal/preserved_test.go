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

// dirtyRescuedTopic is rescuedTopic with uncommitted work in the source when
// it is rescued: a tracked edit, a staged new file and an untracked file. The
// real rescue engine's record holds that state, so the row reads CoversDirt.
func dirtyRescuedTopic(t *testing.T, f *fixture) rescued {
	t.Helper()
	src := f.Worktree("topic")
	f.Commit(src, "topic.txt", "work", "topic work")
	f.write(src, "topic.txt", "edited")
	f.write(src, "staged.txt", "staged")
	gitIn(t, src, "add", "staged.txt")
	f.write(src, "scratch.txt", "untracked")
	return rescueWorktree(t, f, src)
}

// dirtyPreservedRow is the report's row of the dirty rescued original
// "topic", failing unless its record covers the dirt: less_sure, raw Dirty
// kept, effective Dirty cleared.
func dirtyPreservedRow(t *testing.T, f *fixture, r cleanup.Report) cleanup.Row {
	t.Helper()
	row := f.row(r, cleanup.KindWorktree, "topic")
	if row.Preserved == nil || !row.Preserved.CoversDirt || row.Status != cleanup.StatusLessSure ||
		!row.Losses.Dirty || row.EffectiveLosses().Dirty {
		t.Fatalf("precondition: row %s preserved=%+v raw dirty %v effective dirty %v; want a less_sure row whose record covers its dirt",
			row.Status, row.Preserved, row.Losses.Dirty, row.EffectiveLosses().Dirty)
	}
	return row
}

// worktreeRemoves are the `git worktree remove` calls among calls.
func worktreeRemoves(calls [][]string) [][]string {
	var out [][]string
	for _, c := range calls {
		if len(c) >= 2 && c[0] == "worktree" && c[1] == "remove" {
			out = append(out, c)
		}
	}
	return out
}

// TestDirtyPreservedItemRemovesWithAcceptLoss removes a rescued original
// whose record also holds its uncommitted work, through the real Preview ->
// Confirmed -> Run path: it needs exactly Confirm (--accept-loss, never
// --force), git is handed --force once (git refuses a dirty tree without
// it), the original's branch is deleted unasked, and the record and the copy
// survive. The run rescans twice (the gate, then the re-check right before
// git) and git runs straight after the second. Why: this is PR B2's case; the
// record makes the dirt recoverable, so it is not a loss to force, but git
// still needs --force and the last read must be right before it.
func TestDirtyPreservedItemRemovesWithAcceptLoss(t *testing.T) {
	f := newFixture(t)
	rs := dirtyRescuedTopic(t, f)
	s, r := f.report()
	row := dirtyPreservedRow(t, f, r)
	eng := f.engine(s, f.inputs(), Options{})
	p := previewOne(t, eng, ItemFromRow(row))
	wantApproval(t, p, ApprovalNeedsConfirm, Needs{Confirm: true})
	it, err := p.Confirmed(Needs{Confirm: true})
	if err != nil {
		t.Fatalf("Confirmed(Confirm): %v", err)
	}
	var order []string
	stubRescan(t, func(g cleanup.Git, prior cleanup.Snapshot, in cleanup.GatherInputs, o cleanup.Options, tg cleanup.Target) (cleanup.Row, error) {
		order = append(order, "rescan")
		return cleanup.Rescan(g, prior, in, o, tg)
	})
	var calls [][]string
	stubRunGit(t, func(g cleanup.Git, args ...string) (cleanup.Out, error) {
		calls = append(calls, append([]string(nil), args...))
		order = append(order, strings.Join(args[:min(2, len(args))], " "))
		return g.Run(args...)
	})
	o := one(t, eng, it)
	if o.Worktree != WtRemoved || o.Branch != BrDeleted {
		t.Fatalf("outcome %s/%s (%s), want removed/deleted", o.Worktree, o.Branch, o.Reason)
	}
	rm := worktreeRemoves(calls)
	if len(rm) != 1 || strings.Join(rm[0], " ") != "worktree remove --force -- "+row.Path {
		t.Errorf("worktree removes %q, want one `worktree remove --force -- %s`", rm, row.Path)
	}
	if len(order) < 3 || strings.Join(order[:3], ",") != "rescan,rescan,worktree remove" {
		t.Errorf("order %q, want rescan, rescan, then git worktree remove", order)
	}
	if exists(t, rs.Src) || f.registered(rs.Src) || f.refExists("refs/heads/topic") {
		t.Error("the original worktree or its branch is still there")
	}
	if !f.refExists(rs.Ref) || f.git("rev-parse", rs.Ref) != rs.RecordTip {
		t.Errorf("record %s gone or moved", rs.Ref)
	}
	if got := f.git("rev-parse", "refs/heads/"+rs.DestBr); got != rs.DestTip || !strings.HasPrefix(rs.DestBr, "rescued/") {
		t.Errorf("copy branch %s at %s, want rescued/... at %s", rs.DestBr, got, rs.DestTip)
	}
	if !exists(t, filepath.Join(rs.DestPath, "scratch.txt")) {
		t.Error("the copy lost the untracked file")
	}
}

// TestCleanPreservedItemRunsNoReCheck removes a clean rescued original and
// counts one rescan. Why: the re-check right before git is only for covered
// dirt (git's --force path); a clean original is protected by git itself, and
// B1's cost promise is that it hashes nothing more.
func TestCleanPreservedItemRunsNoReCheck(t *testing.T) {
	f := newFixture(t)
	rescuedTopic(t, f)
	s, r := f.report()
	eng := f.engine(s, f.inputs(), Options{})
	it, err := previewOne(t, eng, ItemFromRow(preservedRow(t, f, r))).Confirmed(Needs{})
	if err != nil {
		t.Fatal(err)
	}
	n := countRescans(t)
	if o := one(t, eng, it); o.Worktree != WtRemoved {
		t.Fatalf("outcome %s (%s), want removed", o.Worktree, o.Reason)
	}
	if *n != 1 {
		t.Errorf("%d rescans, want 1", *n)
	}
}

// TestDirtyPreservedItemLapses confirms a preview of a dirty rescued
// original, then changes the source between the preview and Run in each way
// the record can stop covering it (a tracked edit, an untracked edit, an
// exec-bit flip, a restage, a new untracked file, the record deleted): each
// is skipped_changed with the exact reason, its loss lines, and no git
// worktree remove. Why: --force would delete whatever changed; the record
// must still hold exactly what is in the tree when git runs.
func TestDirtyPreservedItemLapses(t *testing.T) {
	const reason = "status changed since it was listed (unique_work, was less_sure)"
	cases := []struct {
		name  string
		lapse func(t *testing.T, f *fixture, rs rescued)
	}{
		{"edit tracked", func(t *testing.T, f *fixture, rs rescued) { f.write(rs.Src, "topic.txt", "edited again") }},
		{"edit untracked", func(t *testing.T, f *fixture, rs rescued) { f.write(rs.Src, "scratch.txt", "untracked, edited") }},
		{"exec-bit flip", func(t *testing.T, f *fixture, rs rescued) {
			if err := os.Chmod(filepath.Join(rs.Src, "topic.txt"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"restage", func(t *testing.T, f *fixture, rs rescued) { gitIn(t, rs.Src, "add", "topic.txt") }},
		{"new untracked file", func(t *testing.T, f *fixture, rs rescued) { f.write(rs.Src, "new.txt", "new") }},
		{"record deleted", func(t *testing.T, f *fixture, rs rescued) { f.git("update-ref", "-d", rs.Ref) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			rs := dirtyRescuedTopic(t, f)
			s, r := f.report()
			eng := f.engine(s, f.inputs(), Options{})
			p := previewOne(t, eng, ItemFromRow(dirtyPreservedRow(t, f, r)))
			it, err := p.Confirmed(p.Needs)
			if err != nil {
				t.Fatalf("Confirmed(%+v): %v", p.Needs, err)
			}
			c.lapse(t, f, rs)
			calls := recordRemoves(t)
			o := one(t, eng, it)
			if o.Worktree != WtSkippedChanged || o.Reason != reason || o.Branch != BrNotAttempted {
				t.Fatalf("outcome %s/%s %q, want skipped_changed/not_attempted %q", o.Worktree, o.Branch, o.Reason, reason)
			}
			if !slices.ContainsFunc(o.LossLines, func(l string) bool { return strings.Contains(l, "uncommitted") }) {
				t.Errorf("loss lines %q, want the uncommitted-changes line", o.LossLines)
			}
			if rm := worktreeRemoves(*calls); len(rm) != 0 || !exists(t, rs.Src) || !f.registered(rs.Src) || !f.refExists("refs/heads/topic") {
				t.Errorf("worktree removes %q; want none and the original kept", rm)
			}
		})
	}
}

// TestEditThenRevertStillRemoves edits a covered file and writes its bytes
// back between the preview and Run: the tree is again what the record holds,
// so the item is removed. Why: coverage is by content, not by timestamps; a
// touch that changes nothing must not make the item unremovable.
func TestEditThenRevertStillRemoves(t *testing.T) {
	f := newFixture(t)
	rs := dirtyRescuedTopic(t, f)
	s, r := f.report()
	eng := f.engine(s, f.inputs(), Options{})
	it, err := previewOne(t, eng, ItemFromRow(dirtyPreservedRow(t, f, r))).Confirmed(Needs{Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	f.write(rs.Src, "topic.txt", "something else entirely")
	f.write(rs.Src, "topic.txt", "edited")
	f.write(rs.Src, "scratch.txt", "x")
	f.write(rs.Src, "scratch.txt", "untracked")
	if o := one(t, eng, it); o.Worktree != WtRemoved || o.Branch != BrDeleted {
		t.Fatalf("outcome %s/%s (%s), want removed/deleted", o.Worktree, o.Branch, o.Reason)
	}
	if exists(t, rs.Src) {
		t.Error("the original is still there")
	}
}

// TestPreviewDoesNotRunTheReCheck counts the rescans of a preview and of a
// dry run (Plan) of a dirty rescued original: one each. Why: the re-check
// belongs right before git, which neither runs; a preview reading twice
// would only cost time and could disagree with itself.
func TestPreviewDoesNotRunTheReCheck(t *testing.T) {
	f := newFixture(t)
	dirtyRescuedTopic(t, f)
	s, r := f.report()
	eng := f.engine(s, f.inputs(), Options{})
	row := dirtyPreservedRow(t, f, r)
	n := countRescans(t)
	p := previewOne(t, eng, ItemFromRow(row))
	if *n != 1 {
		t.Errorf("preview: %d rescans, want 1", *n)
	}
	it, err := p.Confirmed(p.Needs)
	if err != nil {
		t.Fatal(err)
	}
	*n = 0
	out := eng.Plan([]Item{it})
	if len(out) != 1 || out[0].Worktree != WtRemoved || !strings.HasPrefix(out[0].Reason, "dry run: would remove") {
		t.Fatalf("plan %+v, want a dry-run removal", out)
	}
	if *n != 1 {
		t.Errorf("dry run: %d rescans, want 1", *n)
	}
}

// TestPreviewNoteForCoveredDirt checks the preview of a dirty rescued
// original carries the one display note naming the record and the copy, that
// a clean rescued original's preview has none, and that the note is not part
// of what Confirmed trusts (editing it still confirms). Why: the user is
// asked to accept a dirty tree's removal and must see where the changes are
// kept; the note is display only, so it must not change the seal or the
// remove-result contract.
func TestPreviewNoteForCoveredDirt(t *testing.T) {
	f := newFixture(t)
	dirtyRescuedTopic(t, f)
	s, r := f.report()
	row := dirtyPreservedRow(t, f, r)
	p := previewOne(t, f.engine(s, f.inputs(), Options{}), ItemFromRow(row))
	// One sanitised line, capped like every display line (a long temp path
	// may be cut).
	want := cleanup.OneLine("uncommitted changes are kept in " + row.Preserved.Ref + " (copy: " + row.Preserved.DestPath + ")")
	if !slices.Equal(p.Notes, []string{want}) {
		t.Errorf("notes %q, want [%q]", p.Notes, want)
	}
	edited := p
	edited.Notes = []string{"something else"}
	if _, err := edited.Confirmed(Needs{Confirm: true}); err != nil {
		t.Errorf("a preview with edited notes is refused: %v", err)
	}

	g := newFixture(t)
	rescuedTopic(t, g)
	s, r = g.report()
	if p := previewOne(t, g.engine(s, g.inputs(), Options{}), ItemFromRow(preservedRow(t, g, r))); len(p.Notes) != 0 {
		t.Errorf("clean preserved notes %q, want none", p.Notes)
	}
}

// TestSubmoduleStillRefusedUnderForce checks the --force path's submodule
// refusal still holds around covered dirt: (1) a rescued original whose dirt
// the record covers gets a submodule repository forged into its admin dir:
// it needs only Confirm, yet git would be handed --force, so submodulesIn
// refuses it; (2) a submodule added and initialised after the rescue: the
// record no longer covers the tree (dirtsnap does not hash submodules), the
// row needs Force, and with Force it is still refused. Nothing is removed.
// Why: git skips its own submodule check under --force; covered dirt must not
// open a path around Ruling X.
func TestSubmoduleStillRefusedUnderForce(t *testing.T) {
	t.Run("forged modules dir, dirt covered", func(t *testing.T) {
		f := newFixture(t)
		rs := dirtyRescuedTopic(t, f)
		admin := gitIn(t, rs.Src, "rev-parse", "--path-format=absolute", "--git-dir")
		if err := os.MkdirAll(filepath.Join(admin, "modules", "x"), 0o755); err != nil {
			t.Fatal(err)
		}
		s, r := f.report()
		row := dirtyPreservedRow(t, f, r)
		if nd := needsOf(cleanup.KindWorktree, row); nd != (Needs{Confirm: true}) {
			t.Fatalf("needs %+v, want Confirm only", nd)
		}
		eng := f.engine(s, f.inputs(), Options{})
		if p := previewOne(t, eng, ItemFromRow(row)); p.Approval != ApprovalRefused || !strings.Contains(p.Reason, "contains submodules") {
			t.Errorf("preview %s %q, want refused: contains submodules", p.Approval, p.Reason)
		}
		if o := one(t, eng, itemOf(row)); o.Worktree != WtRefused || !strings.Contains(o.Reason, "contains submodules; remove by hand") {
			t.Errorf("outcome %s %q, want refused: contains submodules", o.Worktree, o.Reason)
		}
		if !exists(t, rs.Src) {
			t.Error("the original was removed")
		}
	})
	t.Run("submodule initialised after the rescue", func(t *testing.T) {
		f := newFixture(t)
		rs := dirtyRescuedTopic(t, f)
		sub := filepath.Join(t.TempDir(), "sub")
		gitIn(t, filepath.Dir(sub), "init", "-q", "-b", "main", sub)
		gitIn(t, sub, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init")
		gitIn(t, rs.Src, "-c", "protocol.file.allow=always", "submodule", "add", sub, "sub")
		s, r := f.report()
		row := f.row(r, cleanup.KindWorktree, "topic")
		if row.Preserved != nil && row.Preserved.CoversDirt {
			t.Fatalf("precondition: Preserved %+v covers a tree with a submodule", row.Preserved)
		}
		if nd := needsOf(cleanup.KindWorktree, row); !nd.Force {
			t.Fatalf("needs %+v, want Force", nd)
		}
		eng := f.engine(s, f.inputs(), Options{})
		if o := one(t, eng, approve(itemOf(row), true, false)); o.Worktree != WtRefused || !strings.Contains(o.Reason, "contains submodules") {
			t.Errorf("outcome %s %q, want refused: contains submodules", o.Worktree, o.Reason)
		}
		if !exists(t, rs.Src) {
			t.Error("the original was removed")
		}
	})
}

// TestMissingAcceptLossRefuses runs a dirty rescued original's item with no
// approval at all: it is refused for want of confirmation (the CLI's
// --accept-loss), and the reason never asks for --force. Confirmed(Needs{})
// refuses to build it too. Why: covered dirt is recoverable, so it must not
// demand --force, but removing an unproven, dirty tree still needs the typed
// confirmation.
func TestMissingAcceptLossRefuses(t *testing.T) {
	f := newFixture(t)
	rs := dirtyRescuedTopic(t, f)
	s, r := f.report()
	row := dirtyPreservedRow(t, f, r)
	eng := f.engine(s, f.inputs(), Options{})
	p := previewOne(t, eng, ItemFromRow(row))
	if _, err := p.Confirmed(Needs{}); err == nil {
		t.Error("Confirmed(Needs{}) built an item that needs Confirm")
	}
	o := one(t, eng, granted(ItemFromRow(row), Needs{}))
	if o.Worktree != WtRefused || !strings.HasPrefix(o.Reason, "needs confirmation: less_sure") || strings.Contains(o.Reason, "force") {
		t.Errorf("outcome %s %q, want refused: needs confirmation, no force", o.Worktree, o.Reason)
	}
	if !exists(t, rs.Src) {
		t.Error("the original was removed")
	}
}
