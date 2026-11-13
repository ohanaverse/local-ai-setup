// wt restore-root — move the branch the repository's primary checkout (the
// root) has checked out, with its uncommitted work, into a linked worktree
// under .worktrees, and put the root back on the default branch. The engine
// is internal/rootmove; the one step that changes the root is
// internal/removal's ReleaseRoot, behind its own proofs. This file only
// gathers, asks and prints.
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
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rootmove"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
	"github.com/spf13/cobra"
)

// restoreRootTTY says whether the y/N prompt can be asked: both its input
// and the stream it is written to are terminals. Tests swap it.
var restoreRootTTY = bothTerminals

// afterRestoreRootConfirm is a test seam, called after the confirmation (or
// --yes) and just before Execute: the point where the root can change, or
// another session can claim it, between Preflight and the move.
var afterRestoreRootConfirm = func() {}

type restoreRootFlags struct {
	to                                     string
	yes, acceptLoss, dryRun, json, noFetch bool
}

func restoreRootCmd(a *app) *cobra.Command {
	var f restoreRootFlags
	c := &cobra.Command{
		Use:   "restore-root",
		Short: "Move the root's branch and work into a worktree and put the root back on the default branch",
		Long: "When the repository's primary checkout (the root) is on a branch other than the\n" +
			"default, move that same branch into a new worktree under .worktrees/ (named\n" +
			"after the branch, or --to NAME) and put the root back on the default branch.\n" +
			"Runs from anywhere in the repository. The branch itself is not copied or\n" +
			"renamed: its ref, upstream and config stay as they are, and its commits come\n" +
			"with it. Staged, unstaged and untracked work is carried into the new worktree\n" +
			"and verified there (its `git status` and file contents match the root's), and\n" +
			"a record refs/wt/rescued/<id> holding that work is written, before the root\n" +
			"gives it up. Ignored files stay in the root (the count is reported). When\n" +
			"origin/<default> is strictly ahead of the default branch, the root's default\n" +
			"is fast-forwarded to it; never a merge.\n\n" +
			"It refuses, creating nothing, a root on a detached HEAD, already on the\n" +
			"default branch (exit 0, nothing to do), with no commit, with the default\n" +
			"branch checked out in another worktree, that does not ignore .worktrees/,\n" +
			"with an ignored file the move would overwrite, and every state `wt rescue`\n" +
			"refuses (an operation in progress, unmerged paths, skip-worktree or\n" +
			"assume-unchanged entries, a changed submodule, a nested repository, content\n" +
			"filters, size limits and similar).\n" +
			"A failure part-way stops at that step and says where the root, the new\n" +
			"worktree and the work are; nothing is rolled back and the work stays\n" +
			"reachable. Git hooks do not run; the plan warns when a post-checkout hook\n" +
			"exists. A live wt session of another process in the root is a warning, not a\n" +
			"refusal: a \"root_held: \" line names the holder, and the move goes on once\n" +
			"confirmed at the prompt. --yes alone does not confirm it (exit 3, nothing\n" +
			"changed): add --accept-loss. Nothing checks the claims again.\n\n" +
			"Without --yes it asks y/N on a terminal (default No) after printing what\n" +
			"moves, where, and what the root becomes; with no terminal it changes\n" +
			"nothing. A real run fetches every remote first, as `wt worktrees` does;\n" +
			"--dry-run says what would happen and changes nothing (no ref, index or\n" +
			"working file, and no fetch); --no-fetch skips the fetch.\n\n" +
			"Exit 0: moved (or, with --dry-run, would move; or already on the default).\n" +
			"1: could not start. 2: refused or failed. 3: approval missing (no --yes and\n" +
			"no terminal, or the prompt was declined). With --json the one result\n" +
			"document (docs/contracts/restore-root-result.schema.json) is printed, on\n" +
			"failure too.",
		Example:      "  wt restore-root --dry-run\n  wt restore-root\n  wt restore-root --to spike --yes",
		Args:         func(cmd *cobra.Command, args []string) error { return restoreRootArgs(cmd, f, args) },
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("to") {
				if err := checkRestoreRootTo(f.to); err != nil {
					return restoreRootFail(cmd.OutOrStdout(), f, "", 1, err)
				}
			}
			cwd, err := os.Getwd()
			if err != nil {
				return restoreRootFail(cmd.OutOrStdout(), f, "", 1, err)
			}
			return restoreRootRun(cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), f, cwd)
		},
	}
	c.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return restoreRootFail(cmd.OutOrStdout(), f, "", 1, err)
	})
	c.Flags().StringVar(&f.to, "to", "", "the new worktree's directory name under .worktrees/ (default: the branch name)")
	c.Flags().BoolVar(&f.yes, "yes", false, "move without asking")
	c.Flags().BoolVar(&f.acceptLoss, "accept-loss", false, "with --yes, also move a root another live wt session holds (the agent there is left running in a root that changes under it)")
	c.Flags().BoolVar(&f.dryRun, "dry-run", false, "say what would happen and change nothing, fetch included (needs no --yes)")
	c.Flags().BoolVar(&f.json, "json", false, "print the result as one JSON document (docs/contracts/restore-root-result.schema.json)")
	c.Flags().BoolVar(&f.noFetch, "no-fetch", false, "do not fetch the remotes first (a dry run never does)")
	return c
}

// restoreRootArgs takes no positional argument, through restoreRootFail so
// --json gets its error document.
func restoreRootArgs(cmd *cobra.Command, f restoreRootFlags, args []string) error {
	if len(args) != 0 {
		return restoreRootFail(cmd.OutOrStdout(), f, "", 1, fmt.Errorf("wt restore-root takes no arguments (got %q); it moves the root's own branch", cleanup.OneLine(strings.Join(args, " "))))
	}
	return nil
}

// checkRestoreRootTo refuses a --to that names no usable directory: empty
// or only whitespace, with leading or trailing whitespace, "." or "..", or .git or .worktrees in any case (the
// repository's own metadata or the worktrees directory itself). A usage
// error, like rescue's bad --min-age: nothing is read or created.
func checkRestoreRootTo(to string) error {
	name := strings.TrimSpace(to)
	switch {
	case name == "":
		return errors.New("--to names the new worktree's directory and must not be empty or only whitespace")
	case name != to:
		return fmt.Errorf("--to %q has leading or trailing whitespace, which would become part of the directory name", to)
	case name == "." || name == "..":
		return fmt.Errorf("--to %q is not a directory name", name)
	case strings.EqualFold(name, ".git"), strings.EqualFold(name, ".worktrees"):
		return fmt.Errorf("--to %q is reserved: it names the repository's metadata or the worktrees directory itself", cleanup.OneLine(to))
	}
	return nil
}

// restoreRootFail ends the run with code before any outcome.
func restoreRootFail(out io.Writer, f restoreRootFlags, repo string, code int, err error) error {
	return failWithEnvelope(out, f.json, code, err, func() ([]byte, error) {
		return rootmove.EncodeError(rootmove.Meta{Repo: repo, DryRun: f.dryRun}, err)
	})
}

// restoreRootRun gathers the repository (fetching only on a real run without
// --no-fetch), checks the move, asks, moves and prints the result, running
// from cwd. The command passes its own working directory and the cleanup
// list's `R` hand-off the directory the list was built from: one run path
// (rescueRow's shape — the command and the list's key both end here).
func restoreRootRun(answer io.Reader, out, errOut io.Writer, f restoreRootFlags, cwd string) error {
	signal.Ignore(syscall.SIGPIPE)
	root, err := worktree.PrimaryOf(cwd)
	if err != nil {
		return restoreRootFail(out, f, "", 1, fmt.Errorf("no primary checkout: %w", err))
	}
	// A dry run promises to write no ref: `git fetch` rewrites
	// remote-tracking refs, so it reads the last fetch instead.
	in := cleanupInputs(cwd, !f.noFetch && !f.dryRun, nil, "wt-restore-root")
	snap, err := cleanup.Gather(cleanup.Git{Dir: cwd}, in)
	// With no default branch the snapshot is still whole; Preflight refuses
	// it as no_default_branch.
	if err != nil && !errors.Is(err, cleanup.ErrNoDefaultBranch) {
		repo := snap.Repo
		if repo == "" {
			repo = root
		}
		return restoreRootFail(out, f, repo, 1, err)
	}
	if snap.Repo == "" {
		snap.Repo = root
	}
	in.Fetch = false // a re-check never fetches
	meta := rootmove.Meta{Repo: snap.Repo, DryRun: f.dryRun}
	eng := rootmove.Engine{Rescue: rescue.Engine{G: cleanup.Git{Dir: cwd}, Prior: snap, In: in,
		Classify: cleanup.Options{Now: removeNow()}, ForRoot: true}}

	var notes []string
	if f.dryRun {
		notes = append(notes, dryRunFetchNote)
	} else if failed := snap.Fetch.FailureSummary(); failed != "" {
		notes = append(notes, "a fetch failed ("+failed+"); the fast-forward check used the last fetched state")
	}

	plan, err := eng.Preflight(f.to)
	var refusal *rescue.Refusal
	switch {
	case errors.As(err, &refusal):
		return writeRestoreRootResult(out, errOut, f.json, meta, rootmove.Outcome{Status: rescue.StatusRefused,
			Code: refusal.Code, Reason: refusal.Message, Plan: rootmove.Plan{Repo: snap.Repo, Warnings: notes}})
	case err != nil:
		return restoreRootFail(out, f, snap.Repo, 1, err)
	}
	plan.Warnings = append(plan.Warnings, notes...)
	if f.dryRun {
		return writeRestoreRootResult(out, errOut, f.json, meta, rootmove.WouldMove(plan))
	}
	if f.yes && !f.acceptLoss && heldRootWarning(plan.Warnings) {
		return restoreRootFail(out, f, snap.Repo, 3, errors.New("approval missing: another wt session holds the root (see the root_held warning); --yes alone does not move it, add --accept-loss or end that session; nothing was changed"))
	}
	if !f.yes {
		if !restoreRootTTY(answer, errOut) {
			return restoreRootFail(out, f, snap.Repo, 3, errors.New("approval missing: pass --yes (there is no terminal to ask); nothing was changed"))
		}
		if !promptRestoreRoot(errOut, answer, plan) {
			return restoreRootFail(out, f, snap.Repo, 3, errors.New("not confirmed; nothing was changed"))
		}
	} else {
		// Nothing was shown or asked since Preflight read the root, so its
		// fingerprint stands and Execute skips its start-of-run fingerprint
		// re-check. A prompt keeps it.
		plan.SkipStartRecheck()
	}
	// From here on a signal must not end the process part-way through the
	// move: it is remembered and reported after the result. The prompt above
	// stays interruptible.
	// The hold lasts until the result and the interrupted line are written,
	// so a signal then cannot cut the output short.
	received, release := holdSignals()
	defer release()
	afterRestoreRootConfirm()
	o := eng.Execute(plan)
	err = writeRestoreRootResult(out, errOut, f.json, meta, o)
	if got := received(); len(got) > 0 {
		fmt.Fprintf(errOut, "interrupted (signal %v): the move was not stopped; see the result above\n", got[0])
	}
	return err
}

// heldRootWarning reports whether the plan's warnings carry rootmove's
// root_held line: a live wt session holds the root.
func heldRootWarning(warnings []string) bool {
	for _, w := range warnings {
		if strings.HasPrefix(w, rootmove.HeldWarningPrefix) {
			return true
		}
	}
	return false
}

// heldSignals are the signals holdSignals keeps from ending the process: an
// interrupt, a termination request and a closed terminal.
var heldSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}

// holdSignals catches heldSignals on a buffered channel, so one that
// arrives is remembered rather than lost or fatal, until release is called.
// received returns the signals caught so far, first first (it may be called
// before release). release stops the catching (signal.Stop on this channel
// only: other handlers in the process, a test's included, are left alone,
// which signal.Reset would not do); it is safe to call more than once.
// As in the cleanup list's removal run (internal/tui forwardSignals), only
// wt is held: a git child in the terminal's foreground process group still
// gets a Ctrl-C, and its failure then stops the move at that step with the
// recovery text printed. SIGKILL cannot be held.
func holdSignals() (received func() []os.Signal, release func()) {
	ch := make(chan os.Signal, 8)
	signal.Notify(ch, heldSignals...)
	var got []os.Signal
	received = func() []os.Signal {
		for {
			select {
			case s := <-ch:
				got = append(got, s)
			default:
				return got
			}
		}
	}
	release = func() { signal.Stop(ch) }
	return received, release
}

// ffText says whether the default branch will be fast-forwarded and, when
// not, why.
func ffText(p rootmove.Plan) string {
	d := cleanup.Flatten(p.Default)
	switch {
	case p.DefaultFF:
		return d + " will be fast-forwarded to origin/" + d
	case p.FFNote != "":
		return "no: " + cleanup.Flatten(p.FFNote)
	}
	return "no"
}

// promptRestoreRoot prints what the move will do and asks y/N (default No).
func promptRestoreRoot(ask io.Writer, answer io.Reader, p *rootmove.Plan) bool {
	var b strings.Builder
	fmt.Fprintf(&b, "wt restore-root will move branch %s out of the root %s:\n", cleanup.Flatten(p.Branch), cleanup.Flatten(p.Repo))
	fmt.Fprintf(&b, "  carries       %s\n", countsText(p.Counts))
	fmt.Fprintf(&b, "  left behind   %s in the root (not carried)\n", ignoredText(p.Counts.IgnoredLeft))
	fmt.Fprintf(&b, "  new worktree  %s, on %s\n", cleanup.Flatten(p.DestPath), cleanup.Flatten(p.Branch))
	fmt.Fprintf(&b, "  the root      goes back to %s, with no uncommitted changes\n", cleanup.Flatten(p.Default))
	fmt.Fprintf(&b, "  fast-forward  %s\n", ffText(*p))
	for _, w := range p.Warnings {
		b.WriteString("  warning: " + cleanup.Flatten(w) + "\n")
	}
	b.WriteString("The root gives up its copy of the work only after the new worktree is verified and the record is written.\nMove? [y/N] ")
	if _, err := io.WriteString(ask, b.String()); err != nil {
		return false
	}
	return confirmYN(answer)
}

// ignoredText is "1 ignored entry" or "N ignored entries".
func ignoredText(n int) string {
	if n == 1 {
		return "1 ignored entry"
	}
	return fmt.Sprintf("%d ignored entries", n)
}

// restoreRootExitCode is the status for an outcome: 0 moved or would move,
// and already_default (nothing to do); 2 any other refusal or a failure.
func restoreRootExitCode(o rootmove.Outcome) int {
	switch {
	case o.Status == rescue.StatusMoved, o.Status == rescue.StatusWouldMove:
		return 0
	case o.Status == rescue.StatusRefused && o.Code == rootmove.CodeAlreadyDefault:
		return 0
	}
	return 2
}

// writeRestoreRootResult prints the outcome (the JSON document under asJSON,
// else text on out) and returns the exit status as an error (nil for 0). A
// result stdout did not take goes to errOut: the move may have happened, so
// where the work is must not be lost.
func writeRestoreRootResult(out, errOut io.Writer, asJSON bool, meta rootmove.Meta, o rootmove.Outcome) error {
	return writeResult(out, errOut, asJSON, restoreRootExitCode(o), "the branch was not moved",
		func() ([]byte, bool, error) { b, err := rootmove.EncodeResult(o, meta); return b, false, err },
		func(w io.Writer) error { return printRestoreRootText(w, meta, o) })
}

// printRestoreRootText is the human result. Reasons, steps and warnings are
// printed whole (cleanup.Flatten: one line, control runes dropped, no length
// cap): a failure's reason ends with where the work is and how to finish.
func printRestoreRootText(w io.Writer, meta rootmove.Meta, o rootmove.Outcome) error {
	p := o.Plan
	var b strings.Builder
	if meta.DryRun {
		b.WriteString("dry run: nothing was changed\n")
	}
	switch {
	case o.Status == rescue.StatusWouldMove:
		fmt.Fprintf(&b, "would move branch %s from the root %s to %s; the root would go back to %s\n",
			cleanup.Flatten(p.Branch), cleanup.Flatten(p.Repo), cleanup.Flatten(p.DestPath), cleanup.Flatten(p.Default))
	case o.Status == rescue.StatusMoved:
		fmt.Fprintf(&b, "moved branch %s from the root %s to %s; the root is on %s\n",
			cleanup.Flatten(p.Branch), cleanup.Flatten(p.Repo), cleanup.Flatten(p.DestPath), cleanup.Flatten(p.Default))
	case o.Status == rescue.StatusFailed:
		fmt.Fprintf(&b, "failed (%s): %s\n", o.Code, cleanup.Flatten(o.Reason))
	case o.Code == rootmove.CodeAlreadyDefault:
		fmt.Fprintf(&b, "nothing to do: %s\n", cleanup.Flatten(o.Reason))
	default:
		fmt.Fprintf(&b, "refused (%s): %s\n", o.Code, cleanup.Flatten(o.Reason))
		// Once: a dry run said it above, and Execute's own refusals say
		// "nothing was created".
		if !o.Created && !meta.DryRun && !strings.Contains(o.Reason, "nothing was created") {
			b.WriteString("    nothing was changed\n")
		}
	}
	// What the branch carries: "would carry" for a dry run, "carried" once
	// the new worktree exists; a refusal that created nothing carried nothing.
	verb := ""
	switch {
	case o.Status == rescue.StatusWouldMove:
		verb = "would carry"
	case o.Status == rescue.StatusMoved || o.Created:
		verb = "carried"
	}
	if p.Branch != "" && verb != "" {
		fmt.Fprintf(&b, "    branch %s at %s %s %s\n", cleanup.Flatten(p.Branch), p.Head, verb, countsText(p.Counts))
		fmt.Fprintf(&b, "    left behind: %s in the root\n", ignoredText(p.Counts.IgnoredLeft))
		if o.Status == rescue.StatusWouldMove {
			fmt.Fprintf(&b, "    fast-forward: %s\n", ffText(p))
		}
	}
	for _, s := range o.Completed {
		b.WriteString("    done: " + cleanup.Flatten(s) + "\n")
	}
	if o.Record != nil {
		b.WriteString("    record: " + cleanup.Flatten(o.Record.Ref) + " (holds the commits and the working state)\n")
	}
	if o.FastForwarded {
		fmt.Fprintf(&b, "    fast-forward: %s now matches origin/%s\n", cleanup.Flatten(p.Default), cleanup.Flatten(p.Default))
	}
	for _, s := range p.Warnings {
		b.WriteString("    warning: " + cleanup.Flatten(s) + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}
