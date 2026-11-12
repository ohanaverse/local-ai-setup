// wt worktrees (alias cleanup) — which worktrees and branches are safe to remove, and why.
// This file and internal/cleanup hold no deletion code: the tick list
// removes what the user confirms through internal/removal's engine (wired
// in cleanupRemoveFuncs), the one package allowed to delete.
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

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
	"github.com/spf13/cobra"
)

// Seams: tests swap them.
var (
	// cleanupClassify is cleanup.Classify, so a test can see the Options.
	cleanupClassify = cleanup.Classify
	// isTerminalWriter says whether w is a terminal.
	isTerminalWriter = func(w io.Writer) bool { return isTerminalStream(w) }
	// runCleanupList shows the report as the tick list; enter removes the
	// ticked rows through the RemoveFuncs after a confirmation.
	runCleanupList = tui.RunCleanupList
	// cleanupGather is cleanup.Gather, so a test can see the GatherInputs.
	cleanupGather = cleanup.Gather
)

func worktreesCmd(a *app) *cobra.Command {
	var asJSON, fetch, noFetch, plain bool
	var minAge time.Duration
	c := &cobra.Command{
		Use:     "worktrees",
		Aliases: []string{"cleanup"},
		Short:   "List worktrees and branches, and which are safe to remove, with the proof (alias: cleanup)",
		Long: "`wt cleanup` is an alias of this command.\n\n" +
			"List the repository's worktrees, local branches and remote branches (a terminal list shows every remote's), by how\n" +
			"sure wt is that each can go (on a terminal grouped by kind; --plain groups by\n" +
			"verdict): PROVEN MERGED, LOOKS MERGED (LESS\n" +
			"SURE), HAS WORK THAT EXISTS NOWHERE ELSE, and IN USE / LOCKED. Each row says\n" +
			"why. Apart from the fetch's remote-tracking refs (--no-fetch skips it),\n" +
			"--plain and --json write no ref, index or working file (the merge proof\n" +
			"may leave unreachable tree objects that git gc collects). On a\n" +
			"terminal, enter removes the ticked\n" +
			"rows after a confirmation that lists what each would lose: y when every\n" +
			"row is proven, the typed word delete when any is not proven or loses\n" +
			"something. Origin rows there read less sure (without --fetch the list has not\n" +
			"fetched, so it cannot prove them); remove an origin branch with `wt remove origin/<name>\n" +
			"--remote`, which fetches first and needs its own approval. r copies the work of\n" +
			"the row under the cursor into a new branch and worktree first (`wt rescue`).\n\n" +
			"It runs `git fetch --prune <remote>` for every remote first (origin first, the\n" +
			"others at the same time), never prompting (git's prompts are off and ssh uses\n" +
			"BatchMode unless you set GIT_SSH_COMMAND or core.sshCommand, which is then\n" +
			"left alone)\n" +
			"(--no-fetch skips it; an origin branch is then not removable) and says in the\n" +
			"header how that went (a repository with no remote skips it). A failed\n" +
			"fetch, a missing gh and a row wt could not judge are shown, never an exit\n" +
			"status. Exit 0 means a list was produced; exit 1 means the repository could\n" +
			"not be classified at all (not a repository, no default branch). With --json\n" +
			"the one JSON document is printed on failure too.\n\n" +
			"On a terminal the list is interactive and does not fetch when it opens (every\n" +
			"remote's branches are listed from the refs already there; --fetch fetches every\n" +
			"remote once first and the header says how it went); --plain, --json and\n" +
			"a pipe fetch first and print text.\n" +
			"A branch younger than --min-age (default 10m, at least 1m) that sits at or\n" +
			"behind the base is never proven merged: a brand-new branch looks merged. A\n" +
			"value below 10m is named in the header, as is a registry wt could not read\n" +
			"(registered local_path artifacts are then not marked).",
		Example:      "  wt worktrees\n  wt worktrees --fetch\n  wt worktrees --plain --no-fetch\n  wt worktrees --json",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		// main prints the error line; cobra's copy would repeat it.
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkMinAge(cmd, minAge); err != nil {
				return err
			}
			if fetch && noFetch {
				return fmt.Errorf("--fetch and --no-fetch contradict: --fetch fetches every remote before listing, --no-fetch skips that fetch")
			}
			return executeCleanup(cmd, a, cleanupFlags{json: asJSON, fetch: fetch, noFetch: noFetch, plain: plain, minAge: minAge})
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print the report as one JSON document (docs/contracts/cleanup-list.schema.json)")
	c.Flags().BoolVar(&fetch, "fetch", false, "fetch every remote once before listing (a terminal list fetches nothing without it; --json and --plain already fetch)")
	c.Flags().BoolVar(&noFetch, "no-fetch", false, "do not run git fetch --prune on every remote first")
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
// time. label names the command in the claim ("wt-cleanup", "wt-remove").
func cleanupInputs(cwd string, fetch bool, protected []string, label string) cleanup.GatherInputs {
	claim, err := sessionclaim.Self(cwd, "", label)
	if err != nil {
		// Never joined, only used to tell this process from the others: a
		// claim that could not be timed at worst makes the root look held.
		claim = sessionclaim.Claim{PID: os.Getpid(), Agent: label}
	}
	return cleanup.GatherInputs{CWD: cwd, Fetch: fetch, ProtectedPaths: protected, Claim: claim, Parallel: 4}
}

// cleanupListInputs is cleanupInputs for a caller that shows or acts on origin
// rows (the list, `wt remove origin/...`): it also makes the per-branch
// `gh pr list --base` query.
func cleanupListInputs(cwd string, fetch bool, protected []string, label string) cleanup.GatherInputs {
	in := cleanupInputs(cwd, fetch, protected, label)
	in.OriginRows = true
	return in
}

type cleanupFlags struct {
	json, fetch, noFetch, plain bool
	minAge                      time.Duration
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
	interactive := !f.json && !f.plain && isTerminalWriter(out)
	in := cleanupListInputs(cwd, !f.noFetch, protected, "wt-cleanup")
	if interactive {
		// The list never fetches on open and shows every remote's branches.
		// PRs stay synchronous here: RunCleanupList takes a finished report.
		in.Fetch, in.AllRemotes = false, true
		switch {
		case f.fetch:
			// The user's explicit fetch: every remote once at the root the
			// report is built from, recorded the way the header's fetch note
			// reads it (and, through LastFetched there, the refreshed line).
			in.Fetched = fetchRemotesRecord(in.FetchRemotes, listRoot(cwd))
		case !f.noFetch:
			// Not --no-fetch: the list just does not fetch. Origin rows
			// say so instead of naming a flag the user did not pass.
			in.Fetched = cleanup.FetchResult{NotAttempted: true}
		}
	}
	snap, err := cleanupGather(cleanup.Git{Dir: cwd}, in)
	if err != nil {
		repo := snap.Repo
		if repo == "" {
			repo = cwd
		}
		return cleanupFail(out, f.json, repo, err)
	}
	report := cleanupReport(a, snap, f.minAge, protErr)

	switch {
	case f.json:
		b, err := cleanup.Encode(report)
		if err != nil {
			return cleanupFail(out, true, report.Repo, err)
		}
		_, err = out.Write(b)
		return err
	case interactive:
		return runCleanupList(report, a.theme, cleanupRemoveFuncs(a, snap, in, f.minAge, protErr))
	}
	return printCleanupPlain(out, report, time.Now())
}

// cleanupReport classifies snap now, with the registry's header note.
func cleanupReport(a *app, snap cleanup.Snapshot, minAge time.Duration, protErr error) cleanup.Report {
	report := cleanupClassify(snap, cleanup.Options{Now: time.Now(), MinAge: minAge})
	switch {
	case protErr != nil:
		report.Notes = append(report.Notes, cleanupRegistryNote(protErr))
	case noRegistry(a):
		report.Notes = append(report.Notes, "registry: none at "+cleanup.OneLine(config.RegistryPath())+"; no model artifacts are registered")
	}
	return report
}

// fetchRemotesRecord runs one explicit fetch — the call's own hook (fn, the
// per-call GatherInputs.FetchRemotes seam) or worktree.FetchRemotes — at root
// and records the outcome the way the header's fetch note reads it: ok, or
// failed with git's line (the wording contract the picker's `F` established).
// A failure is a value, never an error: the list still opens and the header
// says how the fetch went.
func fetchRemotesRecord(fn func(string) error, root string) cleanup.FetchResult {
	if fn == nil {
		fn = worktree.FetchRemotes
	}
	if err := fn(root); err != nil {
		return cleanup.FetchResult{At: time.Now(), Err: err.Error()}
	}
	return cleanup.FetchResult{OK: true, At: time.Now()}
}

// listRoot is the root the report is built from — the primary worktree's
// path, exactly what Gather reports as Repo — so an explicit fetch there is
// what the header's refreshed line counts (LastFetched reads that worktree's
// own fetch stamp). cwd answers only when the worktree list cannot be read:
// the gather then fails with the real error anyway.
func listRoot(cwd string) string {
	if facts, err := cleanup.ListWorktrees(cleanup.Git{Dir: cwd}, cwd); err == nil && len(facts) > 0 {
		return facts[0].Path
	}
	return cwd
}

// cleanupRemoveFuncs connects the tick list to the removal engine, with the
// settings `wt remove` uses (its clock and process finder): Preview turns
// the ticked rows into preview inputs (ItemFromRow) and previews them; Run
// removes the items the list confirmed; Refresh re-reads the repository (no
// fetch) for the rebuilt list and points the engine at the new snapshot;
// Refetch (the list's `F`) fetches every remote once at the report's root and
// re-reads the same way. A registry that did not load turns removal off (plan
// call 7): enter then
// says so and previews nothing. Rescue (the `r` key) runs `wt rescue`'s
// executor on the list's current snapshot and is wired either way: it never
// removes anything, so the registry does not matter to it. Refetch is wired
// either way for the same reason.
func cleanupRemoveFuncs(a *app, snap cleanup.Snapshot, in cleanup.GatherInputs, minAge time.Duration, protErr error) tui.RemoveFuncs {
	in.Fetch = false // a re-check never fetches
	in.Fetched = snap.Fetch
	// cur is the snapshot the list currently stands on: Refresh replaces it,
	// and `r` rescues against it.
	cur := &snap
	rescueRowFn := func(row cleanup.Row) error {
		answer, out, errOut := rescueStreams()
		return rescueRow(answer, out, errOut, rescueFlags{minAge: minAge}, *cur, in, row)
	}
	// reGather re-reads the repository (never fetching) and stands the list
	// on the fresh snapshot.
	reGather := func() (cleanup.Snapshot, error) {
		fresh, err := cleanup.Gather(cleanup.Git{Dir: in.CWD}, in)
		if err != nil {
			return cleanup.Snapshot{}, err
		}
		*cur = fresh
		return fresh, nil
	}
	// refetch is the list's `F`: every remote fetched once at the report's
	// root (the same root this report was built from), then the re-read. A
	// fetch failure is not this error — it travels in the report's Fetch
	// field, which the header renders; this error is the re-read's own.
	refetch := func() (cleanup.Report, error) {
		in.Fetched = fetchRemotesRecord(in.FetchRemotes, snap.Repo)
		fresh, err := reGather()
		if err != nil {
			return cleanup.Report{}, err
		}
		return cleanupReport(a, fresh, minAge, protErr), nil
	}
	if protErr != nil {
		return tui.RemoveFuncs{Off: "removal refused: " + protErr.Error() + "; nothing was removed", Refetch: refetch, Rescue: rescueRowFn}
	}
	eng := &removal.Engine{G: cleanup.Git{Dir: in.CWD}, Prior: snap, In: in, Classify: cleanup.Options{MinAge: minAge},
		Opts: removal.Options{Now: removeNow, Procs: removeProcs()}}
	return tui.RemoveFuncs{
		Rescue: rescueRowFn,
		Preview: func(rows []cleanup.Row) []removal.ItemPreview {
			items := make([]removal.Item, len(rows))
			for i, r := range rows {
				items[i] = removal.ItemFromRow(r)
			}
			return eng.Preview(items)
		},
		Run: func(items []removal.Item, progress func(removal.Outcome)) []removal.Outcome {
			return eng.Run(items, progress)
		},
		Refresh: func() (cleanup.Report, error) {
			fresh, err := reGather()
			if err != nil {
				return cleanup.Report{}, err
			}
			eng.Prior = fresh
			return cleanupReport(a, fresh, minAge, nil), nil
		},
		Refetch: refetch,
	}
}

// cleanupFail ends the run with exit 1. Under --json it first prints the one
// error envelope and stays quiet on stderr; otherwise main prints the error.
func cleanupFail(out io.Writer, asJSON bool, repo string, err error) error {
	return failWithEnvelope(out, asJSON, 1, err, func() ([]byte, error) { return cleanup.EncodeError(repo, err) })
}

// failWithEnvelope ends a run with code and err. Under asJSON it first
// prints the document encode builds and stays quiet on stderr (the document
// carries the error); if that cannot be written, main prints the error
// beside the write problem. Without asJSON main prints the error.
func failWithEnvelope(out io.Writer, asJSON bool, code int, err error, encode func() ([]byte, error)) error {
	if !asJSON {
		return &exitCodeError{code: code, err: err}
	}
	b, encErr := encode()
	if encErr != nil {
		return &exitCodeError{code: code, err: errors.Join(err, encErr)}
	}
	if _, werr := out.Write(b); werr != nil {
		return &exitCodeError{code: code, err: errors.Join(err, werr)}
	}
	return &exitCodeError{code: code, err: err, quiet: true}
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
	for _, row := range r.AllRows() {
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
