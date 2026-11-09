// wt remove — remove the worktrees and local branches named on the command
// line, through the same engine (internal/removal) the cleanup list uses. This
// file holds no deletion code of its own: every removal is the engine's.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/spf13/cobra"
)

// Seams: tests swap them.
var (
	// removeNow is the clock the classification and the engine's re-checks
	// use.
	removeNow = time.Now
	// removeTTY says whether the y/N prompt can be asked: both its input and
	// the stream it is written to are terminals.
	removeTTY = bothTerminals
	// removeProcs is the live-process finder (a warning, never a refusal).
	removeProcs = func() removal.ProcFinder { return removal.LsofFinder() }
)

type removeFlags struct {
	yes, force, deleteBranch, acceptLoss, dryRun, json, noFetch, remote bool
	minAge                                                              time.Duration
}

func removeCmd(a *app) *cobra.Command {
	var f removeFlags
	c := &cobra.Command{
		Use:   "remove <worktree-path-or-branch>...",
		Short: "Remove the named worktrees, local branches and origin branches, re-checking each first",
		Long: "Remove each named worktree (by its path), local branch (by its exact name) or\n" +
			"origin branch (origin/<name>), and a removed worktree's branch when it is\n" +
			"proven merged. Each target is classified as `wt cleanup` lists it, then\n" +
			"re-checked immediately before it is touched; one that changed since is skipped.\n\n" +
			"Approvals are per item and each flag means one thing:\n" +
			"  --yes          go ahead without the y/N prompt (a proven item needs nothing else)\n" +
			"  --accept-loss  approve the losses --dry-run lists (not proven, ignored files,\n" +
			"                 commits only on this machine...)\n" +
			"  --force        also lose a worktree's uncommitted changes, or a detached\n" +
			"                 HEAD's commits\n" +
			"  -D             delete a branch not proven merged: a named branch, or the\n" +
			"                 branch of a named worktree (the prompt names each one)\n" +
			"  --remote       delete a branch ON ORIGIN (origin/<name>): visible to everyone\n" +
			"                 with access to the repository, and closing any PR that targets\n" +
			"                 it. Only a proven row qualifies (merged PR with this exact\n" +
			"                 head, no open PR on it or targeting it, origin fetched this run,\n" +
			"                 so --no-fetch refuses it); none of the other flags, --yes\n" +
			"                 included, grants it. The delete is a lease push on the sha\n" +
			"                 listed, so a branch someone pushed to since is left alone, and\n" +
			"                 the result prints the sha and the command that restores it.\n" +
			"An item whose flag is missing is refused and the others go on. No flag\n" +
			"overrides a hard blocker (primary or current checkout, locked, in use,\n" +
			"a registered model artifact, the default branch...).\n\n" +
			"Without --yes it asks y/N on a terminal (default No) after printing every\n" +
			"item's losses and running processes; with no terminal it removes nothing.\n\n" +
			"Exit 0: everything named was removed (a worktree's unmerged branch kept is\n" +
			"not a failure). 1: could not start (an argument matching no target or more\n" +
			"than one, no default branch, a registry that did not load). 2: some item was\n" +
			"refused, skipped or failed. 3: approval missing (no --yes and no terminal, or\n" +
			"the prompt was declined). With --json the one result document\n" +
			"(docs/contracts/remove-result.schema.json) is printed first, on failure too\n" +
			"(a usage error as well, when --json comes before the bad flag).",
		Example: "  wt remove ../repo-wt/topic\n  wt remove topic --dry-run\n  wt remove topic old-spike --yes --accept-loss -D\n  wt remove origin/merged-topic --remote --yes",
		// Both usage failures below go through removeFail, so --json gets its
		// error document (exit 1) as the help promises.
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 {
				return removeFail(cmd.OutOrStdout(), f, "", 1,
					errors.New("name at least one worktree path or branch to remove"))
			}
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkMinAge(cmd, f.minAge); err != nil {
				return removeFail(cmd.OutOrStdout(), f, "", 1, err)
			}
			return executeRemove(cmd, a, f, args)
		},
	}
	// A bad flag: --json is honoured when it came before the bad one (the
	// parser stops at the first error, so a later --json is unseen).
	c.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return removeFail(cmd.OutOrStdout(), f, "", 1, err)
	})
	c.Flags().BoolVar(&f.yes, "yes", false, "remove without asking (only what the other flags approve)")
	c.Flags().BoolVar(&f.acceptLoss, "accept-loss", false, "approve the losses --dry-run lists for items that are not proven or lose something")
	c.Flags().BoolVar(&f.force, "force", false, "also lose uncommitted changes or a detached HEAD's commits")
	c.Flags().BoolVarP(&f.deleteBranch, "delete-unmerged", "D", false, "delete branches not proven merged (named ones, and named worktrees' branches)")
	c.Flags().BoolVar(&f.dryRun, "dry-run", false, "say what would happen and change nothing (needs no --yes)")
	c.Flags().BoolVar(&f.json, "json", false, "print the result as one JSON document (docs/contracts/remove-result.schema.json)")
	c.Flags().BoolVar(&f.remote, "remote", false, "also approve deleting the origin branches named (origin/<name>); no other flag does")
	c.Flags().BoolVar(&f.noFetch, "no-fetch", false, "do not run git fetch --prune on every remote first (origin branches are then not removable)")
	c.Flags().DurationVar(&f.minAge, "min-age", 0, "how old a branch at or behind the base must be to count as merged (default 10m; at least 1m)")
	return c
}

// removeFail ends the run with code before anything was removed. Under
// --json it first prints the one error envelope and stays quiet on stderr;
// otherwise main prints the error.
func removeFail(out io.Writer, f removeFlags, repo string, code int, err error) error {
	return failWithEnvelope(out, f.json, code, err, func() ([]byte, error) {
		return removal.EncodeError(removal.Meta{Repo: repo, DryRun: f.dryRun}, err)
	})
}

// plannedItem is one named target on its way through the command: its row,
// its preview, and either the confirmed Item the engine will run or the
// outcome it already has (refused before the engine).
type plannedItem struct {
	row      cleanup.Row
	preview  removal.ItemPreview
	run      *removal.Item
	deleting string // the offered unmerged branch -D deletes ("" none)
	outcome  removal.Outcome
}

func executeRemove(cmd *cobra.Command, a *app, f removeFlags, args []string) error {
	// A reader that goes away (`wt remove ... | head`) must not kill wt with
	// SIGPIPE after a removal: the write fails instead and the result goes
	// to stderr (writeRemoveResult), restore ids included.
	signal.Ignore(syscall.SIGPIPE)
	out := cmd.OutOrStdout()
	// The registry first: without it no artifact can be protected, so
	// nothing is read or touched (plan call 7).
	protected, protErr := protectedPaths(a)
	if protErr != nil {
		return removeFail(out, f, "", 1, fmt.Errorf("removal refused: %w", protErr))
	}
	cwd, err := os.Getwd()
	if err != nil {
		return removeFail(out, f, "", 1, err)
	}
	in := cleanupInputs(cwd, !f.noFetch, protected, "wt-remove")
	// Only a named origin/<branch> needs the per-branch gh --base query; a
	// local branch or worktree never reads an origin row.
	in.OriginRows = slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "origin/") })
	snap, err := cleanup.Gather(cleanup.Git{Dir: cwd}, in)
	if err != nil {
		repo := snap.Repo
		if repo == "" {
			repo = cwd
		}
		return removeFail(out, f, repo, 1, err)
	}
	copts := cleanup.Options{Now: removeNow(), MinAge: f.minAge}
	report := cleanup.Classify(snap, copts)
	rows, err := resolveRemoveArgs(report, cwd, args)
	if err != nil {
		return removeFail(out, f, snap.Repo, 1, err)
	}

	in.Fetch = false // a re-check never fetches
	in.Fetched = snap.Fetch
	eng := removal.Engine{G: cleanup.Git{Dir: cwd}, Prior: snap, In: in, Classify: cleanup.Options{MinAge: f.minAge},
		Opts: removal.Options{DryRun: f.dryRun, Now: removeNow, Procs: removeProcs()}}
	items := planRemove(eng, rows, f)

	runnable := 0
	for _, p := range items {
		if p.run != nil {
			runnable++
		}
	}
	if runnable > 0 && !f.dryRun && !f.yes {
		ask, answer := cmd.ErrOrStderr(), cmd.InOrStdin()
		if !removeTTY(answer, ask) {
			return removeFail(out, f, snap.Repo, 3, errors.New("approval missing: pass --yes (there is no terminal to ask); nothing was removed"))
		}
		if !promptRemove(ask, answer, items, runnable) {
			return removeFail(out, f, snap.Repo, 3, errors.New("not confirmed; nothing was removed"))
		}
	}

	var batch []removal.Item
	for _, p := range items {
		if p.run != nil {
			batch = append(batch, *p.run)
		}
	}
	results := eng.Run(batch, nil)
	outcomes := make([]removal.Outcome, 0, len(items))
	for _, p := range items {
		if p.run != nil {
			o := results[0]
			results = results[1:]
			if f.deleteBranch && o.Item.Kind == cleanup.KindWorktree && o.Item.Ref != "" &&
				o.Worktree == removal.WtRemoved && o.Branch != removal.BrDeleted {
				o.AddWarning(fmt.Sprintf("-D had no effect on branch %s: %s",
					strings.TrimPrefix(o.Item.Ref, "refs/heads/"), o.Branch))
			}
			outcomes = append(outcomes, o)
			continue
		}
		outcomes = append(outcomes, p.outcome)
	}
	return writeRemoveResult(out, cmd.ErrOrStderr(), f.json, removal.Meta{Repo: snap.Repo, DryRun: f.dryRun}, outcomes)
}

// resolveRemoveArgs maps each argument to its one row: a worktree row whose
// path is the argument (relative to cwd; both compared after resolving
// symlinks), a worktree or branch row whose full refname is
// refs/heads/<argument> exactly, or an origin row when the argument is
// exactly origin/<name> (refs/remotes/origin/<name>; a local branch literally
// named origin/<name> makes it ambiguous, as for any two matches). No match, or
// more than one, is a usage error naming the argument. An argument naming a
// row an earlier one already named adds nothing.
func resolveRemoveArgs(r cleanup.Report, cwd string, args []string) ([]cleanup.Row, error) {
	var rows []cleanup.Row
	seen := map[int]bool{}
	for _, arg := range args {
		if strings.TrimSpace(arg) == "" {
			// Joined to cwd it would name the current checkout.
			return nil, fmt.Errorf("%q names no worktree path or local branch (an empty argument)", arg)
		}
		p := arg
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		p = cleanup.ResolvePath(p)
		var hits []int
		for i, row := range r.Rows {
			byPath := row.Kind == cleanup.KindWorktree && row.Path != "" && cleanup.ResolvePath(row.Path) == p
			byRef := (row.Kind == cleanup.KindWorktree || row.Kind == cleanup.KindBranch) && row.Ref != "" && row.Ref == "refs/heads/"+arg
			byOrigin := row.Kind == cleanup.KindRemote && row.Remote == "origin" && row.Ref != "" && row.Ref == "refs/remotes/"+arg
			if byPath || byRef || byOrigin {
				hits = append(hits, i)
			}
		}
		switch {
		case len(hits) == 0:
			return nil, fmt.Errorf("%q names no worktree path, local branch or origin/<branch> of this repository", cleanup.OneLine(arg))
		case len(hits) > 1:
			return nil, fmt.Errorf("%q matches more than one target (for example a worktree path and a branch); name it unambiguously", cleanup.OneLine(arg))
		}
		if !seen[hits[0]] {
			seen[hits[0]] = true
			rows = append(rows, r.Rows[hits[0]])
		}
	}
	return rows, nil
}

// planRemove previews every row through the engine and decides, per item,
// what its flags approve (Rulings K, L, M): an item is confirmed with exactly
// what it needs when the flags cover that, through ConfirmedDeletingBranch
// when -D opts in to deleting the unmerged branch its preview offers;
// otherwise it is refused naming the missing flags. A row listed in use is
// refused without a preview.
func planRemove(eng removal.Engine, rows []cleanup.Row, f removeFlags) []plannedItem {
	granted := removal.Needs{Confirm: f.acceptLoss, Force: f.force, Delete: f.deleteBranch, Remote: f.remote}
	out := make([]plannedItem, len(rows))
	var inputs []removal.Item
	var at []int
	for i, row := range rows {
		out[i].row = row
		if row.Status == cleanup.StatusInUse {
			o := removal.NewOutcome(removal.ItemFromRow(row), removal.WtRefused, removal.BrNotAttempted, inUseReason(row))
			out[i].outcome = o
			continue
		}
		inputs = append(inputs, removal.ItemFromRow(row))
		at = append(at, i)
	}
	for k, p := range eng.Preview(inputs) {
		pi := &out[at[k]]
		pi.preview = p
		if p.Approval == removal.ApprovalRefused {
			pi.outcome = p.Plan
			continue
		}
		if missing := missingFlags(p.Needs, granted); missing != "" {
			o := removal.NewOutcome(p.Item, removal.WtRefused, removal.BrNotAttempted, "needs "+missing)
			o.LossLines = p.LossLines
			pi.outcome = o
			continue
		}
		var (
			it  removal.Item
			err error
		)
		if f.deleteBranch && p.CanDeleteBranch {
			it, err = p.ConfirmedDeletingBranch(withDelete(p.Needs))
			pi.deleting = strings.TrimPrefix(p.Fresh.Ref, "refs/heads/")
		} else {
			it, err = p.Confirmed(p.Needs)
		}
		if err != nil {
			pi.deleting = ""
			o := removal.NewOutcome(p.Item, removal.WtRefused, removal.BrNotAttempted, err.Error())
			o.LossLines = p.LossLines
			pi.outcome = o
			continue
		}
		pi.run = &it
	}
	return out
}

// withDelete is n with Delete set.
func withDelete(n removal.Needs) removal.Needs { n.Delete = true; return n }

// missingFlags names the flags need asks for that granted lacks ("" none).
func missingFlags(need, granted removal.Needs) string {
	var m []string
	if need.Force && !granted.Force {
		m = append(m, "--force")
	}
	if need.Delete && !granted.Delete {
		m = append(m, "-D")
	}
	if need.Confirm && !granted.Confirm {
		m = append(m, "--accept-loss")
	}
	if need.Remote && !granted.Remote {
		m = append(m, "--remote (deleting an origin branch is visible to everyone with access to the repository)")
	}
	return strings.Join(m, ", ")
}

// inUseReason is the refusal for a row listed in use: its blockers' codes.
func inUseReason(row cleanup.Row) string {
	var codes []string
	for _, b := range row.Blockers {
		codes = append(codes, b.Code)
	}
	if len(codes) == 0 {
		return "in use"
	}
	return "in use: " + strings.Join(codes, ", ")
}

// promptRemove prints, for every item, what removing it would lose, its live
// processes, warnings and notes (and, under -D, the unmerged branch it would
// delete), then the items that will not run, then asks y/N on ask (default
// No). It reports whether the answer was yes.
func promptRemove(ask io.Writer, answer io.Reader, items []plannedItem, runnable int) bool {
	var b strings.Builder
	b.WriteString("wt remove will:\n")
	for _, p := range items {
		if p.run == nil {
			continue
		}
		pv := p.preview
		if pv.Item.Kind == cleanup.KindRemote {
			fmt.Fprintf(&b, "  DELETE origin/%s on origin [%s]", cleanup.OneLine(pv.Item.Name), pv.Fresh.Status)
			if pv.Fresh.PR != nil {
				fmt.Fprintf(&b, " (merged PR #%d)", pv.Fresh.PR.Number)
			}
			b.WriteString("\n      visible to everyone with access to the repository; restore: git push origin " + cleanup.OneLine(pv.Fresh.Tip) + ":refs/heads/" + cleanup.OneLine(pv.Item.Name) + "\n")
			continue
		}
		fmt.Fprintf(&b, "  remove %s %s", pv.Item.Kind, cleanup.OneLine(pv.Item.Name))
		if pv.Item.Kind == cleanup.KindWorktree {
			fmt.Fprintf(&b, " (%s)", cleanup.OneLine(pv.Item.Path))
		}
		fmt.Fprintf(&b, " [%s]\n", pv.Fresh.Status)
		for _, l := range pv.LossLines {
			b.WriteString("      will lose: " + cleanup.OneLine(l) + "\n")
		}
		for _, pr := range pv.Procs {
			fmt.Fprintf(&b, "      running in it: pid %d %s\n", pr.PID, cleanup.OneLine(pr.Command))
		}
		for _, w := range pv.Warnings {
			b.WriteString("      warning: " + cleanup.OneLine(w) + "\n")
		}
		switch {
		case p.deleting != "":
			// The worktree's kept-branch note no longer holds: -D deletes it.
			b.WriteString("      -D: will also delete unmerged branch " + cleanup.OneLine(p.deleting) + "\n")
		default:
			if pv.Item.Kind == cleanup.KindBranch && pv.Needs.Delete {
				b.WriteString("      -D: deletes unmerged branch " + cleanup.OneLine(pv.Item.Name) + "\n")
			}
			for _, n := range pv.Notes {
				b.WriteString("      note: " + cleanup.OneLine(n) + "\n")
			}
		}
	}
	for _, p := range items {
		if p.run != nil {
			continue
		}
		fmt.Fprintf(&b, "  not removed: %s %s: %s\n", p.outcome.Item.Kind, cleanup.OneLine(p.outcome.Item.Name), cleanup.OneLine(p.outcome.Reason))
	}
	word := "items"
	if runnable == 1 {
		word = "item"
	}
	if n := originCount(items); n > 0 {
		bw := "branches"
		if n == 1 {
			bw = "branch"
		}
		fmt.Fprintf(&b, "%d %s on origin (visible to everyone with access to the repository).\n", n, bw)
	}
	fmt.Fprintf(&b, "Remove %d %s? [y/N] ", runnable, word)
	if _, err := io.WriteString(ask, b.String()); err != nil {
		return false
	}
	return confirmYN(answer)
}

// originCount is how many of the items that will run delete an origin branch.
func originCount(items []plannedItem) int {
	n := 0
	for _, p := range items {
		if p.run != nil && p.run.Kind == cleanup.KindRemote {
			n++
		}
	}
	return n
}

// removeExitCode is the status for a finished run's outcomes: 0 when every
// item was removed (a worktree whose unmerged, checked-out-elsewhere or
// default branch was kept by rule counts as removed), 2 otherwise.
func removeExitCode(outcomes []removal.Outcome) int {
	for _, o := range outcomes {
		if !removeOK(o) {
			return 2
		}
	}
	return 0
}

func removeOK(o removal.Outcome) bool {
	switch o.Item.Kind {
	case cleanup.KindBranch:
		return o.Worktree == removal.WtNotRequested && o.Branch == removal.BrDeleted
	case cleanup.KindRemote:
		return o.Worktree == removal.WtNotRequested && o.Branch == removal.BrDeleted
	case cleanup.KindWorktree:
		if o.Worktree != removal.WtRemoved {
			return false
		}
		switch o.Branch {
		case removal.BrDeleted, removal.BrNotAttempted, removal.BrRetainedUnmerged,
			removal.BrRetainedCheckedOut, removal.BrRetainedDefault:
			return true
		}
	}
	return false
}

// writeRemoveResult prints the outcomes (the JSON envelope under asJSON,
// else one text block per item on out) and returns the run's exit status as
// an error (nil for 0). A JSON envelope that carries an error beside its
// items is a partial report (Ruling O): printed whole, exit 2.
func writeRemoveResult(out, errOut io.Writer, asJSON bool, meta removal.Meta, outcomes []removal.Outcome) error {
	code := removeExitCode(outcomes)
	notRemoved := errors.New("some items were not removed")
	if meta.DryRun {
		notRemoved = errors.New("some items would not be removed")
	}
	// lost is a result stdout did not take (a closed pipe): the removals
	// happened, so the text, restore ids included, goes to stderr instead.
	lost := func(err error) error {
		printRemoveText(errOut, meta, outcomes)
		return &exitCodeError{code: 2, err: fmt.Errorf("could not write the result to stdout (it is on stderr): %w", err)}
	}
	if asJSON {
		b, partial, err := removal.EncodeReport(outcomes, meta)
		if err != nil {
			// The removals happened; say so in text rather than lose them.
			printRemoveText(errOut, meta, outcomes)
			return &exitCodeError{code: 2, err: fmt.Errorf("could not write the JSON result: %w", err)}
		}
		if _, err := out.Write(b); err != nil {
			return lost(err)
		}
		if partial {
			code = 2
		}
		if code != 0 {
			return &exitCodeError{code: code, err: notRemoved, quiet: true}
		}
		return nil
	}
	if err := printRemoveText(out, meta, outcomes); err != nil {
		return lost(err)
	}
	if code != 0 {
		return &exitCodeError{code: code, err: notRemoved}
	}
	return nil
}

// printRemoveText is the human result: one line per item, then its losses,
// warnings and, for a deleted branch, the commit it pointed at. It returns
// the write's error.
func printRemoveText(w io.Writer, meta removal.Meta, outcomes []removal.Outcome) error {
	var b strings.Builder
	if meta.DryRun {
		b.WriteString("dry run: nothing was changed\n")
	}
	for _, o := range outcomes {
		b.WriteString(removal.Headline(o))
		if o.Reason != "" {
			b.WriteString(": " + cleanup.OneLine(o.Reason))
		}
		b.WriteString("\n")
		for _, l := range removal.DetailLines(o) {
			b.WriteString("    " + l + "\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}
