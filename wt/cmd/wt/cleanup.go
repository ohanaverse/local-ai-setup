// wt cleanup — which worktrees and branches are safe to remove, and why.
// Read-only: this command and internal/cleanup never delete anything.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/spf13/cobra"
)

// Seams: tests swap them.
var (
	// cleanupClassify is cleanup.Classify, so a test can see the Options.
	cleanupClassify = cleanup.Classify
	// isTerminalWriter says whether w is a terminal.
	isTerminalWriter = func(w io.Writer) bool {
		f, ok := w.(*os.File)
		return ok && term.IsTerminal(f.Fd())
	}
	// runCleanupList shows the report as the tick list. The list itself is a
	// later task, which replaces this; until then it prints the plain output.
	runCleanupList = func(r cleanup.Report, _ themes.Theme) error {
		return printCleanupPlain(os.Stdout, r, time.Now())
	}
)

func cleanupCmd(a *app) *cobra.Command {
	var asJSON, noFetch, plain bool
	var minAge time.Duration
	c := &cobra.Command{
		Use:   "cleanup",
		Short: "Show which worktrees and branches are safe to remove, with the proof",
		Long: "List the repository's worktrees, local branches and origin branches in four\n" +
			"groups, by how sure wt is that each can go: PROVEN MERGED, LOOKS MERGED (LESS\n" +
			"SURE), HAS WORK THAT EXISTS NOWHERE ELSE, and IN USE / LOCKED. Each row says\n" +
			"why. This command only reads: it removes nothing.\n\n" +
			"It runs `git fetch --prune` first (--no-fetch skips it) and says in the header\n" +
			"how that went. A failed fetch, a missing gh and a row wt could not judge are\n" +
			"shown, never an exit status. Exit 0 means a list was produced; exit 1 means the\n" +
			"repository could not be classified at all (not a repository, no default\n" +
			"branch). With --json the one JSON document is printed on failure too.\n\n" +
			"On a terminal the list is interactive; --plain, --json and a pipe print text.\n" +
			"A branch younger than --min-age (default 10m) that sits at or behind the base\n" +
			"is never proven merged: a brand-new branch looks merged.",
		Example:      "  wt cleanup\n  wt cleanup --plain --no-fetch\n  wt cleanup --json",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		// main prints the error line; cobra's copy would repeat it.
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("min-age") && minAge <= 0 {
				return fmt.Errorf("--min-age must be a positive duration such as 10m, got %s (a zero threshold would let a brand-new branch look merged)", minAge)
			}
			return executeCleanup(cmd, a, cleanupFlags{json: asJSON, noFetch: noFetch, plain: plain, minAge: minAge})
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print the report as one JSON document (docs/contracts/cleanup-list.schema.json)")
	c.Flags().BoolVar(&noFetch, "no-fetch", false, "do not run git fetch --prune first")
	c.Flags().BoolVar(&plain, "plain", false, "print text even on a terminal")
	c.Flags().DurationVar(&minAge, "min-age", 0, "how old a branch at or behind the base must be to count as merged (default 10m; must be positive)")
	return c
}

type cleanupFlags struct {
	json, noFetch, plain bool
	minAge               time.Duration
}

func executeCleanup(cmd *cobra.Command, a *app, f cleanupFlags) error {
	out := cmd.OutOrStdout()
	cwd, err := os.Getwd()
	if err != nil {
		return cleanupFail(out, f.json, "", err)
	}
	claim, err := sessionclaim.Self(cwd, "", "wt-cleanup")
	if err != nil {
		// Never joined, only used to tell this process from the others: a
		// claim that could not be timed at worst makes the root look held.
		claim = sessionclaim.Claim{PID: os.Getpid()}
	}
	snap, err := cleanup.Gather(cleanup.Git{Dir: cwd}, cleanup.GatherInputs{
		CWD:            cwd,
		Fetch:          !f.noFetch,
		ProtectedPaths: protectedPaths(a),
		Claim:          claim,
		Parallel:       4,
	})
	if err != nil {
		repo := snap.Repo
		if repo == "" {
			repo = cwd
		}
		return cleanupFail(out, f.json, repo, err)
	}
	report := cleanupClassify(snap, cleanup.Options{Now: time.Now(), MinAge: f.minAge})

	switch {
	case f.json:
		b, err := cleanup.Encode(report)
		if err != nil {
			return cleanupFail(out, true, report.Repo, err)
		}
		_, err = out.Write(b)
		return err
	case !f.plain && isTerminalWriter(out):
		return runCleanupList(report, a.theme)
	}
	return printCleanupPlain(out, report, time.Now())
}

// cleanupFail ends the run with exit 1. Under --json it first prints the one
// error envelope and stays quiet on stderr; otherwise main prints the error.
func cleanupFail(out io.Writer, asJSON bool, repo string, err error) error {
	if !asJSON {
		return &exitCodeError{code: 1, err: err}
	}
	b, encErr := cleanup.EncodeError(repo, err)
	if encErr != nil {
		return &exitCodeError{code: 1, err: errors.Join(err, encErr)}
	}
	if _, werr := out.Write(b); werr != nil {
		return &exitCodeError{code: 1, err: errors.Join(err, werr)}
	}
	return &exitCodeError{code: 1, err: err, quiet: true}
}

// protectedPaths are the registry's local_path artifacts as absolute paths:
// wt never deletes them, so a worktree holding one is in use. A registry that
// did not load simply names none.
func protectedPaths(a *app) []string {
	if a == nil || a.cfg == nil {
		return nil
	}
	var out []string
	for _, m := range a.cfg.Models {
		for _, p := range []string{m.Fetch.LocalPath, m.Draft.LocalPath} {
			if p == "" {
				continue
			}
			if rest, ok := strings.CutPrefix(p, "~/"); ok {
				if home, err := os.UserHomeDir(); err == nil {
					p = filepath.Join(home, rest)
				}
			}
			if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
			out = append(out, p)
		}
	}
	return out
}

// printCleanupPlain writes the header and the groups as text. Same groups,
// order and wording as the list (internal/cleanup/present.go).
func printCleanupPlain(w io.Writer, r cleanup.Report, now time.Time) error {
	var b strings.Builder
	for _, l := range cleanup.HeaderLines(r, now) {
		b.WriteString(l + "\n")
	}
	groups := cleanup.Groups(r)
	if len(groups) == 0 {
		b.WriteString("\nnothing to clean up: no other worktree or branch\n")
	}
	nameW := 0
	for _, row := range r.Rows {
		nameW = max(nameW, len(cleanup.DisplayName(row)))
	}
	for _, g := range groups {
		b.WriteString("\n" + g.Heading + "\n")
		for _, row := range g.Rows {
			line := fmt.Sprintf("  %-8s  %-*s  %s", row.Kind, nameW, cleanup.DisplayName(row), cleanup.RowText(r, row))
			if age := cleanup.AgeText(row); age != "" {
				line += "  [" + age + "]"
			}
			b.WriteString(strings.TrimRight(line, " ") + "\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}
