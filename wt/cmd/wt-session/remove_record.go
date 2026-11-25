// wt remove --record — prune the rescue records named on the command line
// (#543). This file holds no deletion code of its own: the engine
// (internal/removal's PruneRecord) is the only thing that touches a ref.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/spf13/cobra"
)

// plannedRecord is one named record on its way through the command: its
// preview and either the confirmed item the engine will run or the outcome
// it already has (refused before the engine).
type plannedRecord struct {
	row     cleanup.RecordRow
	preview removal.RecordPreview
	run     *removal.RecordItem
	outcome removal.RecordOutcome
}

// resolveRecordArgs maps each argument to its one record row: the id (the
// first 16 hex of the commit, which is the ref's last component) or the full
// refs/wt/rescued/<id> ref. An argument matching none is a usage error that
// says what --record takes; one an earlier argument already named adds
// nothing.
func resolveRecordArgs(rows []cleanup.RecordRow, args []string) ([]cleanup.RecordRow, error) {
	var out []cleanup.RecordRow
	seen := map[string]bool{}
	for _, arg := range args {
		var hit *cleanup.RecordRow
		for i := range rows {
			if arg != "" && (arg == rows[i].ID || arg == rows[i].Ref) {
				hit = &rows[i]
			}
		}
		if hit == nil {
			return nil, fmt.Errorf("%q names no rescue record: with --record every argument is a record id or refs/wt/rescued/<id> ref (`wt worktrees --plain` lists them); worktree and branch targets cannot be combined with --record", cleanup.OneLine(arg))
		}
		if !seen[hit.Ref] {
			seen[hit.Ref] = true
			out = append(out, *hit)
		}
	}
	return out, nil
}

// planRecords previews every record through the engine and decides, per
// item, what its flags approve: a record is confirmed with exactly what it
// needs when the flags cover that; an in-use one is refused without a
// confirmation; a young one without --accept-loss is retained.
func planRecords(eng removal.Engine, rows []cleanup.RecordRow, f removeFlags) []plannedRecord {
	out := make([]plannedRecord, len(rows))
	for i, row := range rows {
		p := eng.PreviewRecord(row)
		pr := &out[i]
		pr.row, pr.preview = row, p
		it := removal.RecordItem{Ref: row.Ref, Commit: row.Commit, Name: row.ID}
		switch {
		case p.Refused:
			pr.outcome = removal.RecordOutcome{Item: it, Outcome: removal.RecRefusedInUse, Reason: cleanup.OneLine(p.Reason)}
		case p.Needs.Confirm && !f.acceptLoss:
			pr.outcome = removal.RecordOutcome{Item: it, Outcome: removal.RecRetainedRecent,
				Reason: cleanup.OneLine("needs --accept-loss: " + row.Reason + "; it may be the only copy of a just-rescued worktree")}
		default:
			ci, err := p.Confirmed(p.Needs)
			if err != nil {
				pr.outcome = removal.RecordOutcome{Item: it, Outcome: removal.RecFailed, Reason: cleanup.OneLine(err.Error())}
				continue
			}
			pr.run = &ci
		}
	}
	return out
}

func executeRemoveRecords(cmd *cobra.Command, f removeFlags, args []string) error {
	signal.Ignore(syscall.SIGPIPE)
	out := cmd.OutOrStdout()
	switch {
	case f.force, f.deleteBranch, f.remote:
		return removeFail(out, f, "", 1, errors.New("--record takes only --yes, --accept-loss, --dry-run and --json; --force, -D and --remote name worktrees, branches and origin branches"))
	}
	cwd, err := os.Getwd()
	if err != nil {
		return removeFail(out, f, "", 1, err)
	}
	in := cleanupInputs(cwd, false, nil, "wt-remove")
	in.SkipPRs = true // records need no pull-request state, so gh is never asked
	snap, err := cleanup.Gather(cleanup.Git{Dir: cwd}, in)
	// No default branch only means nothing can be proven merged; a record's
	// state does not depend on it.
	if err != nil && !errors.Is(err, cleanup.ErrNoDefaultBranch) {
		repo := snap.Repo
		if repo == "" {
			repo = cwd
		}
		return removeFail(out, f, repo, 1, err)
	}
	if snap.RecordsErr != "" {
		return removeFail(out, f, snap.Repo, 1, fmt.Errorf("the rescue records could not be read: %s", snap.RecordsErr))
	}
	now := removeNow()
	rows, err := resolveRecordArgs(cleanup.RecordRows(snap, now), args)
	if err != nil {
		return removeFail(out, f, snap.Repo, 1, err)
	}
	eng := removal.Engine{G: cleanup.Git{Dir: cwd}, Prior: snap, In: in, Opts: removal.Options{DryRun: f.dryRun, Now: removeNow}}
	items := planRecords(eng, rows, f)

	runnable := 0
	for _, p := range items {
		if p.run != nil {
			runnable++
		}
	}
	if runnable > 0 && !f.dryRun && !f.yes {
		ask, answer := cmd.ErrOrStderr(), cmd.InOrStdin()
		if !removeTTY(answer, ask) {
			return removeFail(out, f, snap.Repo, 3, errors.New("approval missing: pass --yes (there is no terminal to ask); nothing was pruned"))
		}
		if !promptRecords(ask, answer, items, runnable) {
			return removeFail(out, f, snap.Repo, 3, errors.New("approval missing: declined; nothing was pruned"))
		}
	}

	var batch []removal.RecordItem
	for _, p := range items {
		if p.run != nil {
			batch = append(batch, *p.run)
		}
	}
	results := eng.PruneRecords(batch)
	outcomes := make([]removal.RecordOutcome, 0, len(items))
	for _, p := range items {
		if p.run != nil {
			outcomes = append(outcomes, results[0])
			results = results[1:]
			continue
		}
		outcomes = append(outcomes, p.outcome)
	}
	return writeRecordResult(out, cmd.ErrOrStderr(), f.json, removal.Meta{Repo: snap.Repo, DryRun: f.dryRun, Records: outcomes})
}

// promptRecords prints, for every record that will run, its source, its
// destination branch and its age, then the ones that will not run, then asks
// y/N on ask (default No). It reports whether the answer was yes.
func promptRecords(ask io.Writer, answer io.Reader, items []plannedRecord, runnable int) bool {
	var b strings.Builder
	b.WriteString("wt remove --record will:\n")
	for _, p := range items {
		if p.run == nil {
			continue
		}
		r := p.row
		age := "age unknown"
		if r.AgeDays >= 0 {
			age = fmt.Sprintf("%d days old", r.AgeDays)
		}
		fmt.Fprintf(&b, "  PRUNE record %s (%s): taken from %s, copy on branch %s", cleanup.OneLine(r.ID), age,
			cleanup.OneLine(r.SourcePath), cleanup.OneLine(r.DestBranch))
		if !r.DestExists {
			b.WriteString(" (that branch is gone)")
		}
		b.WriteString("\n")
		if p.preview.Needs.Confirm {
			b.WriteString("      " + cleanup.OneLine(r.Reason) + " (--accept-loss)\n")
		}
		b.WriteString("      the commits stay until git gc; restore: git update-ref " + cleanup.OneLine(r.Ref) + " " + cleanup.OneLine(r.Commit) + "\n")
	}
	for _, p := range items {
		if p.run != nil {
			continue
		}
		fmt.Fprintf(&b, "  not pruned: record %s: %s\n", cleanup.OneLine(p.outcome.Item.Name), cleanup.OneLine(p.outcome.Reason))
	}
	word := "records"
	if runnable == 1 {
		word = "record"
	}
	fmt.Fprintf(&b, "Prune %d %s? [y/N] ", runnable, word)
	if _, err := io.WriteString(ask, b.String()); err != nil {
		return false
	}
	return confirmYN(answer)
}

// recordExitCode is 0 when every record was pruned (or, in a dry run, would
// be), 2 otherwise.
func recordExitCode(outcomes []removal.RecordOutcome) int {
	for _, o := range outcomes {
		if o.Outcome != removal.RecPruned {
			return 2
		}
	}
	return 0
}

// writeRecordResult prints the record outcomes (the JSON envelope's
// `records` section under asJSON, else one text block per record) and
// returns the exit status as an error, as writeRemoveResult does.
func writeRecordResult(out, errOut io.Writer, asJSON bool, meta removal.Meta) error {
	notDone := "some records were not pruned"
	if meta.DryRun {
		notDone = "some records would not be pruned"
	}
	return writeResult(out, errOut, asJSON, recordExitCode(meta.Records), notDone,
		func() ([]byte, bool, error) { return removal.EncodeReport(nil, meta) },
		func(w io.Writer) error { return printRecordText(w, meta) })
}

// printRecordText is the human result: one line per record, then its
// warnings and, when the run issued the delete, the commit it pointed at.
func printRecordText(w io.Writer, meta removal.Meta) error {
	var b strings.Builder
	if meta.DryRun {
		b.WriteString("dry run: nothing was changed\n")
	}
	for _, o := range meta.Records {
		b.WriteString(removal.RecordHeadline(o))
		if o.Reason != "" {
			b.WriteString(": " + cleanup.OneLine(o.Reason))
		}
		b.WriteString("\n")
		for _, l := range removal.RecordDetailLines(o) {
			b.WriteString("    " + l + "\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}
