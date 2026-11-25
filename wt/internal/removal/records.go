package removal

import (
	"fmt"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// Pruning a rescue record (`wt remove --record`, #543) deletes the one ref
// under refs/wt/rescued/ that pins a rescue's commits. Like every deletion in
// wt it lives here: the ref goes through update-ref's compare-and-swap on the
// commit the user saw, after a fresh re-check that no worktree still matches
// the record and that it is old enough (cleanup.RecordMinAge) or was
// approved with Needs.Confirm. The commits stay in the object database until
// git's gc, so the warning carries the commit and the command that brings the
// ref back.

// RecordOutcomeKind is what became of one record. The values are wire strings
// of the removal JSON contract's `records` section; never rename one.
type RecordOutcomeKind string

const (
	RecPruned         RecordOutcomeKind = "pruned"          // the ref is gone (compare-and-swap on the commit held)
	RecRefusedInUse   RecordOutcomeKind = "refused_in_use"  // a worktree still matches the record; no approval overrides it
	RecSkippedChanged RecordOutcomeKind = "skipped_changed" // the record moved or vanished since the user saw it
	RecRetainedRecent RecordOutcomeKind = "retained_recent" // younger than cleanup.RecordMinAge and --accept-loss was not given
	RecFailed         RecordOutcomeKind = "failed"          // git failed, the state could not be read, or the ref is not a record ref
)

// RecordItem is one record the user picked, with the approvals they gave it.
// Run only receives items built by RecordPreview.Confirmed: Ref and Commit are
// what the user was shown, and a hand-built or edited item is refused (the
// seal, as for Item).
type RecordItem struct {
	Ref    string // the full ref, refs/wt/rescued/<id>
	Commit string // the full commit id the user saw it at
	Name   string // the record's short id
	// Approved is what the user granted: Confirm lets a record younger than
	// cleanup.RecordMinAge (or of unknown age) go. Nothing lets an in-use one.
	Approved Needs
	seal     recordSeal
}

type recordSeal struct {
	set               bool
	ref, commit, name string
	approved          Needs
}

func recordSealOf(it RecordItem) recordSeal {
	return recordSeal{set: true, ref: it.Ref, commit: it.Commit, name: it.Name, approved: it.Approved}
}

func (it RecordItem) confirmed() bool { return it.seal.set && it.seal == recordSealOf(it) }

// RecordPreview is what a confirmation shows for one record. PreviewRecord is
// its only constructor; Confirmed refuses a hand-built or edited one.
type RecordPreview struct {
	Row cleanup.RecordRow
	// Needs: Confirm for a record younger than the age floor.
	Needs Needs
	// Refused is set (with Reason) for a record no approval can prune (in
	// use); Confirmed then fails.
	Refused bool
	Reason  string
	basis   recordBasis
}

type recordBasis struct {
	set    bool
	ref    string
	commit string
	needs  Needs
	refuse bool
}

func (p RecordPreview) basisOf() recordBasis {
	return recordBasis{set: true, ref: p.Row.Ref, commit: p.Row.Commit, needs: p.Needs, refuse: p.Refused}
}

// PreviewRecord decides, from the row the user was shown, what pruning it
// needs. It reads nothing and changes nothing; PruneRecord re-checks.
func (e Engine) PreviewRecord(row cleanup.RecordRow) RecordPreview {
	p := RecordPreview{Row: row}
	switch row.Status {
	case cleanup.RecordInUse:
		p.Refused, p.Reason = true, "in use: "+cleanup.OneLine(row.Reason)
	case cleanup.RecordRecent:
		p.Needs.Confirm = true
	}
	p.basis = p.basisOf()
	return p
}

// Confirmed is the sealed RecordItem PruneRecord accepts, once approved
// covers what the preview needs and holds nothing more.
func (p RecordPreview) Confirmed(approved Needs) (RecordItem, error) {
	name := cleanup.OneLine(p.Row.ID)
	if !p.basis.set || p.basis != p.basisOf() {
		return RecordItem{}, fmt.Errorf("%s: preview was not produced by PreviewRecord or was edited", name)
	}
	if p.Refused {
		return RecordItem{}, fmt.Errorf("%s: refused: %s", name, p.Reason)
	}
	if !approved.covers(p.Needs) {
		return RecordItem{}, fmt.Errorf("%s: approvals %+v do not cover what it needs %+v", name, approved, p.Needs)
	}
	if !p.Needs.covers(approved) {
		return RecordItem{}, fmt.Errorf("%s: approval not required for this record: %+v given, %+v allowed", name, approved, p.Needs)
	}
	it := RecordItem{Ref: p.Row.Ref, Commit: p.Row.Commit, Name: p.Row.ID, Approved: approved}
	it.seal = recordSealOf(it)
	return it, nil
}

// RecordOutcome is the result for one record. Reason and every Warnings entry
// are one sanitised line. Commit is the record's full commit id whenever this
// run issued the delete (or may have), never capped: it is the one reliable
// record of what the ref pinned.
type RecordOutcome struct {
	Item     RecordItem
	Outcome  RecordOutcomeKind
	Reason   string
	Warnings []string
	Commit   string
}

func (e Engine) recOutcome(it RecordItem, k RecordOutcomeKind, reason string) RecordOutcome {
	return RecordOutcome{Item: it, Outcome: k, Reason: cleanup.OneLine(reason)}
}

// recordRestore is the command that brings a pruned record ref back while the
// commit still exists (until git's gc).
func recordRestore(ref, commit string) string {
	return fmt.Sprintf("git update-ref %s %s", ref, commit)
}

// PruneRecord prunes one confirmed record. The gates, in order, each ending
// the item with its own outcome: the item is sealed; the ref is a record ref
// (refs/wt/rescued/ plus the first 16 digits of its full commit id); a fresh
// listing still holds the ref at that commit (else skipped_changed); a fresh
// read of the worktrees finds none that matches it (else refused_in_use,
// failed when they cannot be read); the record is old enough or was approved
// with Confirm (else retained_recent). Then `update-ref --no-deref -d ref
// commit` (a compare-and-swap) and a re-read that proves the ref is gone. A
// dry run stops after the gates and says it would prune.
func (e Engine) PruneRecord(it RecordItem) RecordOutcome {
	if !it.confirmed() {
		return e.recOutcome(it, RecFailed, "invalid target: not confirmed")
	}
	const idLen = 16
	if !strings.HasPrefix(it.Ref, cleanup.RecordRefPrefix) || !cleanup.IsFullCommitID(it.Commit) ||
		it.Ref != cleanup.RecordRefPrefix+it.Commit[:idLen] {
		return e.recOutcome(it, RecFailed, "refused: not a rescue record ref named by its commit")
	}
	recs, err := cleanup.ListRecords(e.G)
	if err != nil {
		return e.recOutcome(it, RecFailed, "the rescue records could not be listed: "+err.Error())
	}
	var rec *cleanup.RescueRecord
	for i := range recs {
		if recs[i].Ref == it.Ref {
			rec = &recs[i]
		}
	}
	switch {
	case rec == nil:
		return e.recOutcome(it, RecSkippedChanged, "the record no longer exists (or is no longer a readable record)")
	case rec.Commit != it.Commit:
		return e.recOutcome(it, RecSkippedChanged, fmt.Sprintf("the record moved to %s since you saw it at %s", abbrev(rec.Commit), abbrev(it.Commit)))
	}
	users, err := cleanup.RecordUsers(e.G, e.In.CWD, *rec)
	switch {
	case err != nil:
		return e.recOutcome(it, RecFailed, "the worktrees could not be read to check the record is unused: "+err.Error())
	case len(users) > 0:
		return e.recOutcome(it, RecRefusedInUse, "worktree "+cleanup.OneLine(users[0])+" still matches this record; no approval overrides that")
	}
	now := time.Now()
	if e.Opts.Now != nil {
		now = e.Opts.Now()
	}
	if (rec.Created.IsZero() || now.Sub(rec.Created) < cleanup.RecordMinAge) && !it.Approved.Confirm {
		why := "younger than 7 days"
		if rec.Created.IsZero() {
			why = "of unknown age"
		}
		return e.recOutcome(it, RecRetainedRecent, why+": it may be the only copy of a just-rescued worktree; --accept-loss approves pruning it")
	}
	if e.Opts.DryRun {
		return e.recOutcome(it, RecPruned, "would prune "+it.Ref)
	}

	out, err := runRefGit(e.G, "update-ref", "--no-deref", "-d", it.Ref, it.Commit)
	if err != nil || out.Code != 0 {
		o := e.recOutcome(it, RecFailed, "git update-ref failed: "+gitErrText(err, out))
		// git may have deleted it before failing: say how to read it back.
		if now, exists, rerr := e.readRef(it.Ref); rerr == nil && exists && now != it.Commit {
			o.Outcome, o.Reason = RecSkippedChanged, cleanup.OneLine(fmt.Sprintf("the record moved to %s since you saw it", abbrev(now)))
		} else if rerr == nil && exists {
			o.Reason = cleanup.OneLine(o.Reason + "; the record is still there")
		} else {
			o.Commit = it.Commit
		}
		return o
	}
	now2, exists, rerr := e.readRef(it.Ref)
	o := e.recOutcome(it, RecPruned, fmt.Sprintf("pruned %s (was %s)", it.Ref, it.Commit))
	o.Commit = it.Commit
	switch {
	case rerr != nil:
		o.Outcome = RecFailed
		o.setReason("git reported " + it.Ref + " deleted but it could not be re-read: " + rerr.Error())
	case exists && now2 != it.Commit:
		o.Outcome = RecSkippedChanged
		o.setReason(fmt.Sprintf("git deleted %s (was %s) but it exists again at %s", it.Ref, it.Commit, abbrev(now2)))
	case exists:
		o.Outcome = RecFailed
		o.setReason(fmt.Sprintf("%s still exists although git reported it deleted", it.Ref))
	default:
		o.Warnings = append(o.Warnings, "restore with: "+recordRestore(it.Ref, it.Commit)+" (until git gc)")
	}
	return o
}

// setReason sets Reason to s as one sanitised, capped line.
func (o *RecordOutcome) setReason(s string) { o.Reason = cleanup.OneLine(s) }

// PruneRecords runs PruneRecord for each item in order; one item's failure
// never stops the others, and a panic in one is that item's failed outcome.
func (e Engine) PruneRecords(items []RecordItem) []RecordOutcome {
	out := make([]RecordOutcome, 0, len(items))
	for _, it := range items {
		out = append(out, e.safeRecord(it))
	}
	return out
}

func (e Engine) safeRecord(it RecordItem) (o RecordOutcome) {
	defer func() {
		if r := recover(); r != nil {
			o = e.recOutcome(it, RecFailed, fmt.Sprintf("internal error: %v", r))
			if it.confirmed() && cleanup.IsFullCommitID(it.Commit) {
				o.Commit = it.Commit // the ref may be gone
			}
		}
	}()
	return e.PruneRecord(it)
}
