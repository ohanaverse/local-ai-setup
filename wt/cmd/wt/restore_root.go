// wt restore-root — move the branch the repository's primary checkout (the
// root) has checked out, with its uncommitted work, into a linked worktree
// under .worktrees, and put the root back on the default branch. The engine
// is internal/rootmove; the one step that changes the root is
// internal/removal's ReleaseRoot, behind its own proofs. This file only
// gathers, asks and prints.
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

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rootmove"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
	"github.com/spf13/cobra"
)

// restoreRootTTY says whether the y/N prompt can be asked: both its input
// and the stream it is written to are terminals. Tests swap it.
var restoreRootTTY = func(in io.Reader, out io.Writer) bool {
	return isTerminalStream(in) && isTerminalStream(out)
}

// afterRestoreRootConfirm is a test seam, called after the confirmation (or
// --yes) and just before Execute: the point where the root can change, or
// another session can claim it, between Preflight and the move.
var afterRestoreRootConfirm = func() {}

type restoreRootFlags struct {
	to                         string
	yes, dryRun, json, noFetch bool
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
			"branch checked out in another worktree, held by another live wt session,\n" +
			"that does not ignore .worktrees/, with an ignored file the move would\n" +
			"overwrite, and every state `wt rescue` refuses (an operation in progress,\n" +
			"unmerged paths, skip-worktree or assume-unchanged entries, a changed\n" +
			"submodule, a nested repository, content filters, size limits and similar).\n" +
			"A failure part-way stops at that step and says where the root, the new\n" +
			"worktree and the work are; nothing is rolled back and the work stays\n" +
			"reachable. Git hooks do not run; the plan warns when a post-checkout hook\n" +
			"exists.\n\n" +
			"Without --yes it asks y/N on a terminal (default No) after printing what\n" +
			"moves, where, and what the root becomes; with no terminal it changes\n" +
			"nothing. A real run fetches every remote first, as `wt cleanup` does;\n" +
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
			return restoreRootRun(cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), f)
		},
	}
	c.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return restoreRootFail(cmd.OutOrStdout(), f, "", 1, err)
	})
	c.Flags().StringVar(&f.to, "to", "", "the new worktree's directory name under .worktrees/ (default: the branch name)")
	c.Flags().BoolVar(&f.yes, "yes", false, "move without asking")
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

// restoreRootFail ends the run with code before any outcome.
func restoreRootFail(out io.Writer, f restoreRootFlags, repo string, code int, err error) error {
	return failWithEnvelope(out, f.json, code, err, func() ([]byte, error) {
		return rootmove.EncodeError(rootmove.Meta{Repo: repo, DryRun: f.dryRun}, err)
	})
}

// restoreRootRun gathers the repository (fetching only on a real run without
// --no-fetch), checks the move, asks, moves and prints the result.
func restoreRootRun(answer io.Reader, out, errOut io.Writer, f restoreRootFlags) error {
	signal.Ignore(syscall.SIGPIPE)
	cwd, err := os.Getwd()
	if err != nil {
		return restoreRootFail(out, f, "", 1, err)
	}
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
	} else if w := fetchFailedNote(snap.Fetch); w != "" {
		notes = append(notes, w)
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
	if !f.yes {
		if !restoreRootTTY(answer, errOut) {
			return restoreRootFail(out, f, snap.Repo, 3, errors.New("approval missing: pass --yes (there is no terminal to ask); nothing was changed"))
		}
		if !promptRestoreRoot(errOut, answer, plan) {
			return restoreRootFail(out, f, snap.Repo, 3, errors.New("not confirmed; nothing was changed"))
		}
	} else {
		// Nothing was shown or asked since Preflight read the root, so its
		// fingerprint stands and Execute skips its start-of-run re-checks;
		// the checks after the copy and before the release still run. A
		// prompt keeps them.
		plan.SkipStartRecheck()
	}
	afterRestoreRootConfirm()
	return writeRestoreRootResult(out, errOut, f.json, meta, eng.Execute(plan))
}

// fetchFailedNote is the warning for a real run whose fetch of any remote
// failed ("" when none did): each failed remote with git's (capped) error.
func fetchFailedNote(fr cleanup.FetchResult) string {
	var failed []string
	seen := map[string]bool{}
	for _, r := range fr.Remotes {
		if r.Err != "" {
			failed = append(failed, r.Name+": "+cleanup.OneLine(r.Err))
			seen[r.Name] = true
		}
	}
	if fr.Err != "" && !seen["origin"] {
		failed = append([]string{"origin: " + cleanup.OneLine(fr.Err)}, failed...)
	}
	if len(failed) == 0 {
		return ""
	}
	return "a fetch failed (" + strings.Join(failed, "; ") + "); the fast-forward check used the last fetched state"
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
	line, _ := bufio.NewReader(answer).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
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
	code := restoreRootExitCode(o)
	notDone := errors.New("the branch was not moved")
	lost := func(err error) error {
		printRestoreRootText(errOut, meta, o)
		return &exitCodeError{code: 2, err: fmt.Errorf("could not write the result to stdout (it is on stderr): %w", err)}
	}
	if asJSON {
		b, err := rootmove.EncodeResult(o, meta)
		if err != nil {
			printRestoreRootText(errOut, meta, o)
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
	if err := printRestoreRootText(out, meta, o); err != nil {
		return lost(err)
	}
	if code != 0 {
		return &exitCodeError{code: code, err: notDone}
	}
	return nil
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
