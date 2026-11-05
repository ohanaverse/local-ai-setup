// wt cleanup — which worktrees and branches are safe to remove, and why.
// Read-only: this command and internal/cleanup never delete anything.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
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
	// runCleanupList shows the report as the tick list (view only: enter
	// prints what would be removed and removes nothing).
	runCleanupList = tui.RunCleanupList
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
			"It runs `git fetch --prune origin` first (--no-fetch skips it) and says in the\n" +
			"header how that went (a repository with no origin remote skips it). A failed\n" +
			"fetch, a missing gh and a row wt could not judge are shown, never an exit\n" +
			"status. Exit 0 means a list was produced; exit 1 means the repository could\n" +
			"not be classified at all (not a repository, no default branch). With --json\n" +
			"the one JSON document is printed on failure too.\n\n" +
			"On a terminal the list is interactive; --plain, --json and a pipe print text.\n" +
			"A branch younger than --min-age (default 10m, at least 1m) that sits at or\n" +
			"behind the base is never proven merged: a brand-new branch looks merged. A\n" +
			"value below 10m is named in the header, as is a registry wt could not read\n" +
			"(registered local_path artifacts are then not marked).",
		Example:      "  wt cleanup\n  wt cleanup --plain --no-fetch\n  wt cleanup --json",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		// main prints the error line; cobra's copy would repeat it.
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkMinAge(cmd, minAge); err != nil {
				return err
			}
			return executeCleanup(cmd, a, cleanupFlags{json: asJSON, noFetch: noFetch, plain: plain, minAge: minAge})
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print the report as one JSON document (docs/contracts/cleanup-list.schema.json)")
	c.Flags().BoolVar(&noFetch, "no-fetch", false, "do not run git fetch --prune origin first")
	c.Flags().BoolVar(&plain, "plain", false, "print text even on a terminal")
	c.Flags().DurationVar(&minAge, "min-age", 0, "how old a branch at or behind the base must be to count as merged (default 10m; at least 1m)")
	return c
}

// checkMinAge is the --min-age usage check `wt cleanup` and `wt remove`
// share: a value given must be at least cleanup.MinAgeFloor.
func checkMinAge(cmd *cobra.Command, minAge time.Duration) error {
	if !cmd.Flags().Changed("min-age") {
		return nil
	}
	if minAge <= 0 {
		return fmt.Errorf("--min-age must be a positive duration such as 10m, got %s (a zero threshold would let a brand-new branch look merged)", minAge)
	}
	if minAge < cleanup.MinAgeFloor {
		return fmt.Errorf("--min-age must be at least 1m, got %s (a shorter threshold lets a brand-new branch look merged)", minAge)
	}
	return nil
}

// cleanupInputs are the GatherInputs `wt cleanup` and `wt remove` gather
// with from cwd: this process's session claim (so its own root launch does
// not look like another's), the registered artifacts and four gh calls at a
// time.
func cleanupInputs(cwd string, fetch bool, protected []string) cleanup.GatherInputs {
	claim, err := sessionclaim.Self(cwd, "", "wt-cleanup")
	if err != nil {
		// Never joined, only used to tell this process from the others: a
		// claim that could not be timed at worst makes the root look held.
		claim = sessionclaim.Claim{PID: os.Getpid()}
	}
	return cleanup.GatherInputs{CWD: cwd, Fetch: fetch, ProtectedPaths: protected, Claim: claim, Parallel: 4}
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
	// Listing only reads, so a registry that cannot be read does not stop
	// it: the header says protection is off (and a removal refuses).
	protected, protErr := protectedPaths(a)
	snap, err := cleanup.Gather(cleanup.Git{Dir: cwd}, cleanupInputs(cwd, !f.noFetch, protected))
	if err != nil {
		repo := snap.Repo
		if repo == "" {
			repo = cwd
		}
		return cleanupFail(out, f.json, repo, err)
	}
	report := cleanupClassify(snap, cleanup.Options{Now: time.Now(), MinAge: f.minAge})
	switch {
	case protErr != nil:
		report.Notes = append(report.Notes, cleanupRegistryNote(protErr))
	case noRegistry(a):
		report.Notes = append(report.Notes, "registry: none at "+cleanup.OneLine(config.RegistryPath())+"; no model artifacts are registered")
	}

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

// errRegistryUnavailable is protectedPaths' error when the registry did not
// load: the artifacts it names are unknown, so none can be protected.
var errRegistryUnavailable = errors.New("the registry failed to load")

// cleanupRegistryNote is the header line `wt cleanup` adds when protectedPaths
// failed: the list still shows, but nothing is marked protected_artifact and
// a removal must refuse. The cause is the load error itself, without
// errRegistryUnavailable's own words in front of it.
func cleanupRegistryNote(err error) string {
	cause := strings.TrimPrefix(err.Error(), errRegistryUnavailable.Error()+": ")
	return "registry: could not be read: " + cleanup.OneLine(cause) + "; artifact protection is OFF and removal is refused"
}

// noRegistry reports whether the registry did not load only because there is
// no file at the default path: nothing is registered, so nothing needs
// protecting. It is a positive match: the home directory is known and
// absolute, and the registry path is exactly $HOME/.config/local-ai/
// registry.toml (XDG_CONFIG_HOME spelling out $HOME/.config is that same
// place). Any other location was chosen by a person or script (WT_REGISTRY,
// MODELMAN_REGISTRY, an XDG_CONFIG_HOME elsewhere) and may be a typo or an
// unmounted volume hiding a real registry, so its absence fails closed. With
// no home directory (launchd, cron) there is no default to match, so that
// fails closed too; config.RegistryRedirected answers false there and must
// not decide this.
func noRegistry(a *app) bool {
	if a == nil || !errors.Is(a.loadErr, config.ErrRegistryMissing) {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !filepath.IsAbs(home) {
		return false
	}
	return filepath.Clean(config.RegistryPath()) == filepath.Join(home, ".config", "local-ai", "registry.toml")
}

// protectedPaths are the registry's local_path artifacts (fetch and draft of
// every model) as absolute, cleaned paths, sorted and each once: wt never
// deletes them, so a worktree holding one, or inside one, is in use. "~" and
// "~/..." are expanded (config.ExpandHome). No file at the default registry
// path is nothing to protect (nil, nil). Everything else fails closed: a
// registry that did not load is errRegistryUnavailable, and a local_path the
// loader read as absent because it was malformed (config.ArtifactFaults: not
// a string, or its fetch/draft not a table), a "~" path that cannot be
// expanded, or a relative path is an error naming the model. A relative
// local_path has no agreed base (wt model add writes absolute paths, llmbench
// resolves from its own working directory, modeladmin from wt's), so wt
// cannot know what it names: a path wt cannot enumerate is a path it cannot
// protect.
func protectedPaths(a *app) ([]string, error) {
	if a == nil {
		return nil, errRegistryUnavailable
	}
	if noRegistry(a) {
		return nil, nil
	}
	if a.loadErr != nil {
		return nil, fmt.Errorf("%w: %w", errRegistryUnavailable, a.loadErr)
	}
	if a.cfg == nil {
		return nil, errRegistryUnavailable
	}
	var out []string
	for _, m := range a.cfg.Models {
		for _, side := range []struct {
			key string
			art config.ModelArtifact
		}{{"fetch", m.Fetch}, {"draft", m.Draft}} {
			switch {
			case side.art.Malformed.NotTable:
				return nil, fmt.Errorf("model %s: %s is not a table, so its local_path cannot be protected", m.ID, side.key)
			case side.art.Malformed.LocalPath:
				return nil, fmt.Errorf("model %s: %s.local_path is not a string, so it cannot be protected", m.ID, side.key)
			case side.art.LocalPath == "":
				continue
			}
			p, err := resolveLocalPath(side.art.LocalPath)
			if err != nil {
				return nil, fmt.Errorf("model %s: %s.local_path %q %w", m.ID, side.key, side.art.LocalPath, err)
			}
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// resolveLocalPath is one local_path as an absolute, cleaned path, home
// expanded. Its error completes "fetch.local_path "<value>" ...".
func resolveLocalPath(lp string) (string, error) {
	p, err := config.ExpandHome(lp)
	if err != nil {
		return "", fmt.Errorf("cannot be expanded: no home directory: %w", err)
	}
	if strings.HasPrefix(p, "~") {
		// ExpandHome left it: an empty $HOME, or a "~user" form wt does not
		// expand. This also refuses a literal relative directory named
		// "~foo": it is relative either way, and refusing fails closed.
		return "", errors.New("cannot be expanded (no home directory, or a ~user form); write it as an absolute path")
	}
	if !filepath.IsAbs(p) {
		return "", errors.New("is relative; write it as an absolute path")
	}
	return filepath.Clean(p), nil
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
