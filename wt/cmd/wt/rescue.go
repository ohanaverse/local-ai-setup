// wt rescue — copy a worktree's local-only work (commits that exist nowhere
// else, staged and unstaged changes, untracked files) into a new branch and a
// new worktree. This file holds no deletion code and the engine
// (internal/rescue) only creates: the source worktree is read, never changed.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
	"github.com/spf13/cobra"
)

// rescueTTY says whether the y/N prompt can be asked: both its input and the
// stream it is written to are terminals. Tests swap it.
var rescueTTY = func(in io.Reader, out io.Writer) bool {
	return isTerminalStream(in) && isTerminalStream(out)
}

// rescueStreams are the terminal streams the cleanup list's `r` key hands to
// the rescue once the alt screen is gone. Tests swap them.
var rescueStreams = func() (io.Reader, io.Writer, io.Writer) { return os.Stdin, os.Stdout, os.Stderr }

// dryRunFetchNote is the warning a dry run carries: it ran no fetch.
const dryRunFetchNote = "dry run: nothing was fetched, so remote-tracking refs (and what counts as unpushed or merged) are as of the last fetch"

type rescueFlags struct {
	to                         string
	yes, dryRun, json, noFetch bool
	minAge                     time.Duration
}

func rescueCmd(a *app) *cobra.Command {
	var f rescueFlags
	c := &cobra.Command{
		Use:   "rescue <worktree-path-or-branch>",
		Short: "Copy a worktree's local-only work into a new branch and worktree",
		Long: "Keep the work that exists only in one worktree by copying it somewhere safe:\n" +
			"a new branch rescued/<name> starting at the worktree's exact HEAD (so every\n" +
			"commit comes with it), in a new worktree under .worktrees/, carrying the\n" +
			"staged changes, the unstaged changes and the untracked files. Ignored files\n" +
			"are not copied (the count left behind is reported).\n\n" +
			"The original is only read and is never changed, switched or removed. It is\n" +
			"not made removable either: its commits and files still exist only there as\n" +
			"far as `wt cleanup` can tell, so removing it still takes the approvals\n" +
			"`wt remove` names. Before it says the work moved, wt checks that the new\n" +
			"worktree's `git status` and file contents match the original's.\n\n" +
			"It refuses, creating nothing, a worktree with nothing to keep, the primary\n" +
			"checkout, one with a merge, rebase or similar in progress or unmerged\n" +
			"paths, a file marked skip-worktree or assume-unchanged, a changed\n" +
			"submodule, a file both staged and modified, an intent-to-add file, a nested\n" +
			"git repository, a changed file under a content filter (git-lfs or any\n" +
			"clean/smudge filter), a changed file git would write with different line\n" +
			"endings, more than 20000 untracked files or 512 MiB of untracked and changed\n" +
			"tracked files together, and a name that\n" +
			"is taken (pass --to NAME, the full branch name). A failure after the new\n" +
			"worktree exists keeps it and says where; nothing is rolled back. Git hooks\n" +
			"(post-checkout) do not run in the new worktree; the plan warns when one exists.\n\n" +
			"Without --yes it asks y/N on a terminal (default No) after printing what\n" +
			"it will carry; with no terminal it creates nothing. --dry-run says what\n" +
			"would happen and changes nothing (no ref, index or working file); it\n" +
			"skips the fetch and classifies against the last one.\n\n" +
			"Exit 0: the work was copied and verified (or, with --dry-run, would be).\n" +
			"1: could not start (no match or an ambiguous argument, --min-age below 1m).\n" +
			"2: refused or failed. 3: approval missing (no --yes and no terminal, or the\n" +
			"prompt was declined). With --json the one result document\n" +
			"(docs/contracts/rescue-result.schema.json) is printed first, on failure too.",
		Example:      "  wt rescue ../repo-wt/spike --dry-run\n  wt rescue spike --yes\n  wt rescue spike --to keep/spike",
		Args:         func(cmd *cobra.Command, args []string) error { return rescueArgs(cmd, f, args) },
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkMinAge(cmd, f.minAge); err != nil {
				return rescueFail(cmd.OutOrStdout(), f, "", 1, err)
			}
			return executeRescue(cmd, a, f, args[0])
		},
	}
	c.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return rescueFail(cmd.OutOrStdout(), f, "", 1, err)
	})
	c.Flags().StringVar(&f.to, "to", "", "the new branch's full name (default rescued/<branch or directory name>)")
	c.Flags().BoolVar(&f.yes, "yes", false, "copy without asking")
	c.Flags().BoolVar(&f.dryRun, "dry-run", false, "say what would happen and change nothing, fetch included (needs no --yes)")
	c.Flags().BoolVar(&f.json, "json", false, "print the result as one JSON document (docs/contracts/rescue-result.schema.json)")
	c.Flags().BoolVar(&f.noFetch, "no-fetch", false, "do not run git fetch --prune origin first (a dry run never does)")
	c.Flags().DurationVar(&f.minAge, "min-age", 0, "how old a branch at or behind the base must be to count as merged (default 10m; at least 1m)")
	return c
}

// rescueArgs requires exactly one target, through rescueFail so --json gets
// its error document.
func rescueArgs(cmd *cobra.Command, f rescueFlags, args []string) error {
	if len(args) != 1 {
		return rescueFail(cmd.OutOrStdout(), f, "", 1, errors.New("name exactly one worktree path or branch to rescue"))
	}
	return nil
}

// rescueFail ends the run with code before anything was created.
func rescueFail(out io.Writer, f rescueFlags, repo string, code int, err error) error {
	return failWithEnvelope(out, f.json, code, err, func() ([]byte, error) {
		return rescue.EncodeError(rescue.Meta{Repo: repo, DryRun: f.dryRun}, err)
	})
}

func executeRescue(cmd *cobra.Command, a *app, f rescueFlags, target string) error {
	out := cmd.OutOrStdout()
	cwd, err := os.Getwd()
	if err != nil {
		return rescueFail(out, f, "", 1, err)
	}
	// A dry run promises to write no ref: `git fetch` rewrites remote-tracking
	// refs, so it classifies against the last fetch instead.
	in := cleanupInputs(cwd, !f.noFetch && !f.dryRun, nil, "wt-rescue")
	snap, err := cleanup.Gather(cleanup.Git{Dir: cwd}, in)
	if err != nil {
		repo := snap.Repo
		if repo == "" {
			repo = cwd
		}
		return rescueFail(out, f, repo, 1, err)
	}
	report := cleanup.Classify(snap, cleanup.Options{Now: removeNow(), MinAge: f.minAge})
	rows, err := resolveRemoveArgs(report, cwd, []string{target})
	if err != nil {
		return rescueFail(out, f, snap.Repo, 1, err)
	}
	in.Fetch = false // a re-check never fetches
	return rescueRow(cmd.InOrStdin(), out, cmd.ErrOrStderr(), f, snap, in, rows[0])
}

// rescueRow runs the rescue of one listed row: the check, the confirmation,
// the copy and the result. The command and the cleanup list's `r` key both
// end here.
func rescueRow(answer io.Reader, out, errOut io.Writer, f rescueFlags, snap cleanup.Snapshot, in cleanup.GatherInputs, row cleanup.Row) error {
	signal.Ignore(syscall.SIGPIPE)
	meta := rescue.Meta{Repo: snap.Repo, DryRun: f.dryRun}
	eng := rescue.Engine{G: cleanup.Git{Dir: in.CWD}, Prior: snap, In: in, Classify: cleanup.Options{Now: removeNow(), MinAge: f.minAge}}

	var notes []string
	if f.dryRun {
		notes = []string{dryRunFetchNote}
	}
	plan, err := eng.Preflight(row, f.to)
	var refusal *rescue.Refusal
	switch {
	case errors.As(err, &refusal):
		return writeRescueResult(out, errOut, f.json, meta, rescue.Outcome{Status: rescue.StatusRefused, Code: refusal.Code,
			Reason: refusal.Message + "; nothing was created", Plan: rescue.Plan{Source: rescue.Source{Path: row.Path, Ref: row.Ref, Head: row.Head}, Warnings: notes}})
	case err != nil:
		return rescueFail(out, f, snap.Repo, 1, err)
	}
	if f.dryRun {
		plan.Warnings = append(plan.Warnings, notes...)
		return writeRescueResult(out, errOut, f.json, meta, rescue.Outcome{Status: rescue.StatusWouldMove, Plan: *plan})
	}
	if !f.yes {
		if !rescueTTY(answer, errOut) {
			return rescueFail(out, f, snap.Repo, 3, errors.New("approval missing: pass --yes (there is no terminal to ask); nothing was created"))
		}
		if !promptRescue(errOut, answer, plan) {
			return rescueFail(out, f, snap.Repo, 3, errors.New("not confirmed; nothing was created"))
		}
	}
	if f.yes {
		// Nothing was shown or asked since Preflight read the source, so its
		// fingerprint stands and Execute skips its own start-of-run re-read; the
		// comparison after the copy still runs. A prompt keeps the re-read.
		plan.SkipStartRecheck()
	}
	return writeRescueResult(out, errOut, f.json, meta, executeRescuePlan(eng, plan))
}

// executeRescuePlan is the test seam over the executor: a test swaps it to see
// the plan as the command hands it over (which path ran: --yes or a prompt).
var executeRescuePlan = func(eng rescue.Engine, plan *rescue.Plan) rescue.Outcome {
	return eng.Execute(plan)
}

// promptRescue prints what the rescue will do and asks y/N (default No).
func promptRescue(ask io.Writer, answer io.Reader, p *rescue.Plan) bool {
	var b strings.Builder
	fmt.Fprintf(&b, "wt rescue will copy the work in %s:\n", cleanup.OneLine(p.Source.Path))
	fmt.Fprintf(&b, "  new branch    %s, starting at %s (the source's HEAD)\n", cleanup.OneLine(p.Branch), p.Start)
	fmt.Fprintf(&b, "  new worktree  %s\n", cleanup.OneLine(p.DestPath))
	fmt.Fprintf(&b, "  carries       %s\n", countsText(p.Counts))
	if p.Counts.IgnoredLeft > 0 {
		fmt.Fprintf(&b, "  left behind   %d ignored entries (not copied)\n", p.Counts.IgnoredLeft)
	}
	for _, w := range p.Warnings {
		b.WriteString("  warning: " + cleanup.OneLine(w) + "\n")
	}
	b.WriteString("The source is only read; nothing in it changes.\nCopy it? [y/N] ")
	if _, err := io.WriteString(ask, b.String()); err != nil {
		return false
	}
	line, _ := bufio.NewReader(answer).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// countsText is "2 commits, 1 staged, ..." for what a rescue carries.
func countsText(c rescue.Counts) string {
	parts := []string{
		plural(c.Commits, "commit"),
		fmt.Sprintf("%d staged", c.Staged),
		fmt.Sprintf("%d unstaged", c.Unstaged),
		fmt.Sprintf("%s (%d bytes)", plural(c.Untracked, "untracked file"), c.UntrackedBytes),
	}
	return strings.Join(parts, ", ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// rescueExitCode is the status for an outcome: 0 moved or would move, 2 else.
func rescueExitCode(o rescue.Outcome) int {
	if o.Status == rescue.StatusMoved || o.Status == rescue.StatusWouldMove {
		return 0
	}
	return 2
}

// writeRescueResult prints the outcome (the JSON document under asJSON, else
// text on out) and returns the exit status as an error (nil for 0). A result
// stdout did not take goes to errOut: the copy happened, so its location must
// not be lost.
func writeRescueResult(out, errOut io.Writer, asJSON bool, meta rescue.Meta, o rescue.Outcome) error {
	code := rescueExitCode(o)
	notDone := errors.New("the work was not copied")
	lost := func(err error) error {
		printRescueText(errOut, meta, o)
		return &exitCodeError{code: 2, err: fmt.Errorf("could not write the result to stdout (it is on stderr): %w", err)}
	}
	if asJSON {
		b, err := rescue.EncodeResult(o, meta)
		if err != nil {
			printRescueText(errOut, meta, o)
			return &exitCodeError{code: 2, err: fmt.Errorf("could not write the JSON result: %w", err)}
		}
		if _, err := out.Write(b); err != nil {
			return lost(err)
		}
		if code != 0 {
			return &exitCodeError{code: code, err: notDone, quiet: true}
		}
		return nil
	}
	if err := printRescueText(out, meta, o); err != nil {
		return lost(err)
	}
	if code != 0 {
		return &exitCodeError{code: code, err: notDone}
	}
	return nil
}

// printRescueText is the human result.
func printRescueText(w io.Writer, meta rescue.Meta, o rescue.Outcome) error {
	p := o.Plan
	var b strings.Builder
	if meta.DryRun {
		b.WriteString("dry run: nothing was changed\n")
	}
	switch o.Status {
	case rescue.StatusWouldMove:
		fmt.Fprintf(&b, "would copy %s to branch %s in %s\n", cleanup.OneLine(p.Source.Path), cleanup.OneLine(p.Branch), cleanup.OneLine(p.DestPath))
	case rescue.StatusMoved:
		fmt.Fprintf(&b, "copied %s to branch %s in %s\n", cleanup.OneLine(p.Source.Path), cleanup.OneLine(p.Branch), cleanup.OneLine(o.Plan.DestPath))
	case rescue.StatusFailed:
		fmt.Fprintf(&b, "failed: %s\n", cleanup.OneLine(o.Reason))
	default:
		fmt.Fprintf(&b, "refused (%s): %s\n", o.Code, cleanup.OneLine(o.Reason))
	}
	if p.Branch != "" {
		fmt.Fprintf(&b, "    starts at %s; carries %s\n", p.Start, countsText(p.Counts))
		if p.Counts.IgnoredLeft > 0 {
			fmt.Fprintf(&b, "    left behind: %d ignored entries\n", p.Counts.IgnoredLeft)
		}
	}
	for _, s := range o.Completed {
		b.WriteString("    done: " + cleanup.OneLine(s) + "\n")
	}
	for _, s := range p.Warnings {
		b.WriteString("    warning: " + cleanup.OneLine(s) + "\n")
	}
	if o.Status == rescue.StatusMoved {
		fmt.Fprintf(&b, "    the original is untouched and still holds work only it has; removing it takes `wt remove %s` with the approvals it names\n", cleanup.OneLine(p.Source.Path))
	}
	if o.Status == rescue.StatusFailed && o.BranchKept && !o.Created {
		fmt.Fprintf(&b, "    the new branch %s was kept (creating its worktree failed after git made it); a rerun needs --to NAME or `wt remove %s`\n", cleanup.OneLine(p.Branch), cleanup.OneLine(p.Branch))
	}
	if o.Status == rescue.StatusFailed && o.Created {
		fmt.Fprintf(&b, "    the new branch %s and worktree %s were kept; `wt remove` can clear them\n", cleanup.OneLine(p.Branch), cleanup.OneLine(o.Plan.DestPath))
	}
	_, err := io.WriteString(w, b.String())
	return err
}
