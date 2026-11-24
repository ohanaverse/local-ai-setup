// Package gittest is wt's shared fixture kit for tests that drive real git:
// immutable seeded templates (a bare origin.git with one README commit on
// main), per-test clone pairs of them, the lineage gate a clone must pass
// before a test body trusts it, and the raw-index probe and real-git
// round-trip assert the read-only proofs are built on.
//
// One rule shapes every helper: fixture state is created by real git (init,
// clone --bare, clone, commit, push -u origin <branch>, switch -c, config)
// and never hand-built or deleted, so the repo's deletion guards
// (TestDeletionStringsOnlyInRemovalPackage, TestHardResetExistsNowhereOutsideRemoval)
// stay green and a shape git treats as corrupt cannot hide behind a fixture
// author's guess of git's on-disk format. The one deletion call the kit holds
// is os.RemoveAll of its own MkdirTemp template directories in
// CleanupTemplates (the allow-list entry names it); a test's clones live in
// t.TempDir and die with the test.
package gittest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// SeedOpts is the only template dimension; each value is its own immutable
// lineage, seeded once per process and never mutated after seeding.
type SeedOpts struct {
	// ExcludeWorktrees seeds a lineage whose info/exclude ignores .worktrees/
	// (a repository that does not ignore its own worktree directory is a
	// state a real user can be in, so both shapes get fixtures).
	ExcludeWorktrees bool
}

// Template is one seeded bare origin.git. It is shared by every test in the
// process and immutable: a test mutates only its own Clone.
type Template struct {
	// Origin is the absolute path of the template's bare origin.git.
	Origin string

	// opts is the lineage the template was seeded with; Clone registers it.
	opts SeedOpts
}

var (
	// seeds memoizes one finished template per SeedOpts. It is filled under
	// seedMu only after a build succeeds: a failed build is not memoized (a
	// sync.OnceValues would replay the first error to every later caller for
	// the life of the process), so the next Seed call retries it.
	seedMu sync.Mutex
	seeds  = map[SeedOpts]*Template{}

	// tplDirs records every template directory made, for CleanupTemplates.
	tplDirsMu sync.Mutex
	tplDirs   []string

	// pairs records every Clone pair by its repo path, so Lineage can judge a
	// repo against the origin and lineage it was made with.
	pairsMu sync.Mutex
	pairs   = map[string]pair{}
)

// pair is what Clone registers for Lineage: the pair's own bare origin and the
// lineage its template was seeded with.
type pair struct {
	origin string
	opts   SeedOpts
}

// Seed returns the shared template for opts, seeding it on the first call.
// The template directory is process-scoped (os.MkdirTemp under the temp root),
// never a test's t.TempDir, which would delete the shared template when its
// first caller ends; the directories are cleaned by CleanupTemplates. Only a
// successful build is kept: a failed one removes its half-made directory, fails
// this call, and leaves the next call to retry.
func Seed(t testing.TB, opts SeedOpts) *Template {
	t.Helper()
	return seedWith(t, opts, buildTemplate)
}

// seedWith is Seed with the build step passed in, so a test can make one build
// fail without any package variable.
func seedWith(t testing.TB, opts SeedOpts, build func(SeedOpts) (*Template, error)) *Template {
	t.Helper()
	// Held across the build so concurrent first callers do not seed twice.
	seedMu.Lock()
	defer seedMu.Unlock()
	if tpl, ok := seeds[opts]; ok {
		return tpl
	}
	tpl, err := build(opts)
	if err != nil {
		t.Fatalf("gittest: seeding the template: %v", err)
		return nil
	}
	seeds[opts] = tpl
	return tpl
}

// CleanupTemplates removes every seeded template directory, best effort.
// Each consuming package's TestMain calls it just before os.Exit; the removal
// is os.RemoveAll of directories this package made itself under the process
// temp root (never a user's files — deletion of user data belongs in
// internal/removal).
func CleanupTemplates() {
	tplDirsMu.Lock()
	dirs := tplDirs
	tplDirs = nil
	tplDirsMu.Unlock()
	for _, dir := range dirs {
		_ = os.RemoveAll(dir)
	}
}

// buildTemplate seeds one lineage: a bare origin.git with one README commit
// on main (HEAD -> main), and, for ExcludeWorktrees, .worktrees/ appended to
// its info/exclude. The seed commit carries its identity on the command line
// (-c user.name=T -c user.email=t@example.com -c commit.gpgsign=false) so the
// template needs no config state and the developer's config cannot reach it.
func buildTemplate(opts SeedOpts) (tpl *Template, err error) {
	dir, err := os.MkdirTemp("", "gittest-tpl-")
	if err != nil {
		return nil, err
	}
	// A failed build removes its half-made directory (the kit's own MkdirTemp
	// directory, the one deletion the allow-list names) and is not recorded.
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
			return
		}
		tplDirsMu.Lock()
		tplDirs = append(tplDirs, dir)
		tplDirsMu.Unlock()
	}()
	origin := filepath.Join(dir, "origin.git")
	if out, err := gitRun(dir, "init", "--bare", "-b", "main", origin); err != nil {
		return nil, fmt.Errorf("init bare origin: %w\n%s", err, out)
	}
	seedRepo := filepath.Join(dir, "seed")
	if out, err := gitRun(dir, "clone", origin, seedRepo); err != nil {
		return nil, fmt.Errorf("clone the empty origin: %w\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(seedRepo, "README"), []byte("init"), 0o644); err != nil {
		return nil, err
	}
	if out, err := gitRun(seedRepo, "add", "README"); err != nil {
		return nil, fmt.Errorf("stage README: %w\n%s", err, out)
	}
	seed := []string{"-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "init"}
	if out, err := gitRun(seedRepo, seed...); err != nil {
		return nil, fmt.Errorf("seed commit: %w\n%s", err, out)
	}
	if out, err := gitRun(seedRepo, "push", "-u", "origin", "main"); err != nil {
		return nil, fmt.Errorf("push the seed commit: %w\n%s", err, out)
	}
	if opts.ExcludeWorktrees {
		ex, err := gitRun(origin, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
		if err != nil {
			return nil, fmt.Errorf("resolve the template's info/exclude: %w", err)
		}
		f, err := os.OpenFile(strings.TrimSpace(ex), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
		if err != nil {
			return nil, err
		}
		_, err = f.WriteString(".worktrees/\n")
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, err
		}
	}
	return &Template{Origin: origin, opts: opts}, nil
}

// Git runs git -C dir args... and returns its trimmed combined output; a
// non-zero exit fails the test with "git <args>: <err>" and git's output. It
// is the one runner behind every package's fixture helper (gitIn, cgit, rmGit
// delegate here). It keeps the process environment as is, with no
// GIT_OPTIONAL_LOCKS=0: a test that runs `git status` or `git diff` means the
// locking behavior, and the kit's own read-only reads use gitRun instead.
//
// What stays inline in tests instead of using this kit: one-off repositories
// that are deliberately odd (nested repos, sha256 object format, bare-only,
// a trunk-default name, no-commit repos), because the kit builds the one
// ordinary shape and bending it per case would hide the shape under test.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	b, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, b)
	}
	return strings.TrimSpace(string(b))
}

// NewRepo is the base repository every fixture starts from: Seed, Clone and
// Lineage in one call. It returns the pair's bare origin and its clone (main
// pushed with upstream origin/main, origin/HEAD set, identity T, one README
// commit "init"); excludeWorktrees picks the lineage (see SeedOpts). A package
// keeps its own fixture type and wraps the pair; the repeated bootstrap lives
// here. See Git for the shapes that stay inline.
func NewRepo(t testing.TB, excludeWorktrees bool) (origin, repo string) {
	t.Helper()
	origin, repo = Seed(t, SeedOpts{ExcludeWorktrees: excludeWorktrees}).Clone(t)
	Lineage(t, repo)
	return origin, repo
}

// Clone makes a per-test pair under t.TempDir: a bare origin cloned from the
// template and a worktree repo cloned from that origin, with the repo's
// commit identity configured (user.email=t@example.com, user.name=T,
// commit.gpgsign=false). The lineage's info/exclude travels with the pair —
// git clone copies neither exclude file — so a clone carries exactly what its
// template lineage dictates. Both paths are absolute.
func (tpl *Template) Clone(t testing.TB) (origin, repo string) {
	t.Helper()
	tmp := t.TempDir()
	origin = filepath.Join(tmp, "origin.git")
	repo = filepath.Join(tmp, "repo")
	if out, err := gitRun(tmp, "clone", "--bare", tpl.Origin, origin); err != nil {
		t.Fatalf("gittest: clone --bare of the template: %v\n%s", err, out)
	}
	if out, err := gitRun(tmp, "clone", origin, repo); err != nil {
		t.Fatalf("gittest: clone of the pair's origin: %v\n%s", err, out)
	}
	for _, kv := range [][2]string{{"user.email", "t@example.com"}, {"user.name", "T"}, {"commit.gpgsign", "false"}} {
		if out, err := gitRun(repo, "config", kv[0], kv[1]); err != nil {
			t.Fatalf("gittest: repo config %s: %v\n%s", kv[0], err, out)
		}
	}
	exclude, err := gitRun(tpl.Origin, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if err != nil {
		t.Fatalf("gittest: resolve the template's info/exclude: %v", err)
	}
	carried, err := os.ReadFile(strings.TrimSpace(exclude))
	if err != nil {
		t.Fatalf("gittest: read the template's info/exclude: %v", err)
	}
	for _, dst := range []string{origin, repo} {
		to, err := gitRun(dst, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
		if err != nil {
			t.Fatalf("gittest: resolve %s's info/exclude: %v", dst, err)
		}
		if err := os.WriteFile(strings.TrimSpace(to), carried, 0o644); err != nil {
			t.Fatalf("gittest: carry the lineage's info/exclude into %s: %v", dst, err)
		}
	}
	pairsMu.Lock()
	pairs[repo] = pair{origin: origin, opts: tpl.opts}
	pairsMu.Unlock()
	return origin, repo
}

// Lineage fails the test naming the poison when repo does not read as a fresh
// clone of a pristine template. Call it on a clone before the test body: an
// exact-count assertion trusts a pristine repo, and a poisoned clone (or
// template) would flip it without any assertion naming the cause. The repo
// must be one Clone made (it is judged against its own pair's origin and
// lineage); any other path fails loud. Ref names are compared exactly, never
// case-folded: on a case-insensitive volume show-ref resolves Feature/x
// through feature/x and would say yes.
func Lineage(t testing.TB, repo string) {
	t.Helper()
	pairsMu.Lock()
	p, ok := pairs[repo]
	pairsMu.Unlock()
	if !ok {
		t.Fatalf("gittest: Lineage(%s): not a gittest.Clone pair", repo)
		return
	}
	if err := lineageErr(repo, p.origin, p.opts); err != nil {
		t.Fatalf("gittest: poisoned lineage in %s: %v", repo, err)
	}
}

// pristineRefs is the exact ref set of a fresh pair's repo.
var pristineRefs = []string{"refs/heads/main", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"}

// lineageErr is Lineage's verdict for the pair (repo, origin, opts): nil for a
// pristine clone, an error naming the poison otherwise (an entry under
// .worktrees/, a ref under refs/wt/rescued/, a refs/heads/topic ref, any other
// ref beyond exactly refs/heads/main, refs/remotes/origin/main and
// refs/remotes/origin/HEAD, a remote other than origin or an origin URL that is
// not the pair's own origin.git, a repo or origin info/exclude that is not the
// lineage's exact shape, or a HEAD not on main). The reads it makes
// (for-each-ref, rev-parse, symbolic-ref, remote) are ones git never refreshes
// the index's stat cache for, so the gate judges a repo it does not change. The
// GIT_OPTIONAL_LOCKS=0 gitRun adds is belt and braces only: it suppresses
// `git status`'s refresh but NOT `git diff`'s (see gitRun).
func lineageErr(repo, origin string, opts SeedOpts) error {
	ents, err := os.ReadDir(filepath.Join(repo, ".worktrees"))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading .worktrees: %w", err)
	}
	if len(ents) > 0 {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		return fmt.Errorf(".worktrees/ holds %s (a wrong success left worktree state behind)", strings.Join(names, ", "))
	}
	out, err := gitRun(repo, "for-each-ref")
	if err != nil {
		return fmt.Errorf("for-each-ref: %w\n%s", err, out)
	}
	have := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		// "<oid> <type>\t<refname>": the refname is everything past the tab.
		_, ref, ok := strings.Cut(line, "\t")
		if !ok {
			return fmt.Errorf("for-each-ref reported an unparsable line %q", line)
		}
		if strings.HasPrefix(ref, "refs/wt/rescued/") {
			return fmt.Errorf("ref %s survived from another test's rescue record", ref)
		}
		if ref == "refs/heads/topic" {
			return fmt.Errorf("ref refs/heads/topic survived from another test's branch")
		}
		have[ref] = true
	}
	head, err := gitRun(repo, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		return fmt.Errorf("HEAD is not a symbolic ref (%v); a clone must sit on main", err)
	}
	if got := strings.TrimSpace(head); got != "refs/heads/main" {
		return fmt.Errorf("HEAD is %s, not refs/heads/main", got)
	}
	// The catch-all after the named checks: exactly the pristine set.
	want := map[string]bool{}
	for _, r := range pristineRefs {
		want[r] = true
		if !have[r] {
			return fmt.Errorf("ref %s is missing; a pristine clone holds exactly %s", r, strings.Join(pristineRefs, ", "))
		}
	}
	for r := range have {
		if !want[r] {
			return fmt.Errorf("ref %s is not in a pristine clone, which holds exactly %s", r, strings.Join(pristineRefs, ", "))
		}
	}
	remotes, err := gitRun(repo, "remote")
	if err != nil {
		return fmt.Errorf("git remote: %w\n%s", err, remotes)
	}
	if got := strings.TrimSpace(remotes); got != "origin" {
		return fmt.Errorf("remotes are %q, want exactly origin", got)
	}
	url, err := gitRun(repo, "remote", "get-url", "origin")
	if err != nil {
		return fmt.Errorf("remote get-url origin: %w\n%s", err, url)
	}
	if got := strings.TrimSpace(url); filepath.Clean(got) != filepath.Clean(origin) {
		return fmt.Errorf("origin points at %q, not this pair's own %q (another pair's origin would let one test's push leak into another)", got, origin)
	}
	for _, dir := range []string{repo, origin} {
		if err := excludeErr(dir, opts); err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
	}
	return nil
}

// excludeErr checks dir's info/exclude is the lineage's exact shape: git's
// default comment block, plus exactly one .worktrees/ line for the
// ExcludeWorktrees lineage and none for the plain one. Anything else is an
// append a test left behind (the append-only IgnoredIn hazard) or a clone that
// lost the lineage's line.
func excludeErr(dir string, opts SeedOpts) error {
	exclude, err := gitRun(dir, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if err != nil {
		return fmt.Errorf("resolve info/exclude: %w", err)
	}
	b, err := os.ReadFile(strings.TrimSpace(exclude))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading info/exclude: %w", err)
	}
	n := 0
	for i, line := range strings.Split(string(b), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == ".worktrees/" {
			n++
			if n > 1 {
				return fmt.Errorf("info/exclude line %d repeats .worktrees/; the lineage carries it at most once", i+1)
			}
			continue
		}
		return fmt.Errorf("info/exclude line %d is %q; the lineage's expected content is the default comments plus the .worktrees/ line only when ExcludeWorktrees", i+1, line)
	}
	if opts.ExcludeWorktrees && n != 1 {
		return fmt.Errorf("info/exclude lacks the .worktrees/ line the ExcludeWorktrees lineage requires")
	}
	if !opts.ExcludeWorktrees && n != 0 {
		return fmt.Errorf("info/exclude carries a .worktrees/ line the plain lineage must not have")
	}
	return nil
}

// RawIndex returns the raw .git/index bytes ("absent" when there is no index
// file), read without optional locks the way rootmove.rootState reads them:
// a proof that compares these bytes must see every write, and must cause none.
func RawIndex(t testing.TB, repo string) string {
	t.Helper()
	b, err := os.ReadFile(indexFile(t, repo))
	if os.IsNotExist(err) {
		return "absent"
	}
	if err != nil {
		t.Fatalf("gittest: reading the raw index of %s: %v", repo, err)
	}
	return string(b)
}

// ArmIndexProbe makes the raw-index byte-diff proof able to see a write. A
// fresh copy mutes it: its index stat cache is fresh, so a buggy reader's
// rewrite is byte-identical and the detector passes while the write happens
// (the flake history is internal/cleanup/dirt_test.go's
// TestDirtCoveredRacyCleanEntry, via its comment). Arming re-times the repo's
// tracked worktree files and the raw index file to one past second — that
// test's set() shape — so the entries' recorded stat is stale and racy, and
// the next locking refresh rewrites them into the raw bytes. The repo still
// reads clean: only the timestamps move.
func ArmIndexProbe(t testing.TB, repo string) {
	t.Helper()
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	out, err := gitRun(repo, "ls-files", "-z")
	if err != nil {
		t.Fatalf("gittest: listing the tracked files of %s: %v\n%s", repo, err, out)
	}
	for _, p := range strings.Split(out, "\x00") {
		if p == "" {
			continue
		}
		fi, err := os.Lstat(filepath.Join(repo, p))
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if err := os.Chtimes(filepath.Join(repo, p), at, at); err != nil {
			t.Fatalf("gittest: re-timing %s: %v", p, err)
		}
	}
	if err := os.Chtimes(indexFile(t, repo), at, at); err != nil {
		t.Fatalf("gittest: re-timing the raw index of %s: %v", repo, err)
	}
}

// AssertProbeArmed fails unless the raw index file's mtime reads older than 30
// minutes: a byte-diff proof may only count when its own precondition held
// (Constraint 3), and this is the check that fails loudly when a copy cannot
// satisfy it.
func AssertProbeArmed(t testing.TB, repo string) {
	t.Helper()
	fi, err := os.Stat(indexFile(t, repo))
	if err != nil {
		t.Fatalf("gittest: stat the raw index of %s: %v", repo, err)
	}
	if fi.ModTime().After(time.Now().Add(-30 * time.Minute)) {
		t.Fatalf("gittest: the raw index of %s reads mtime %v, not armed; run ArmIndexProbe before the byte-diff proof counts", repo, fi.ModTime())
	}
}

// RoundTrip runs git -C repo args... and fails unless git's combined output
// equals want byte for byte, whatever the exit code: every hand-built .git
// shape must round-trip through the real git read the engine makes (design
// doc constraint 6), and a shape real git reports differently fails setup
// loudly instead of testing git's error path green.
func RoundTrip(t testing.TB, repo, want string, args ...string) {
	t.Helper()
	if err := roundTripErr(repo, want, args...); err != nil {
		t.Fatal(err)
	}
}

// roundTripErr is RoundTrip's verdict: the mismatch instead of a t.Fatal, so
// tests assert it directly.
func roundTripErr(repo, want string, args ...string) error {
	out, _ := gitRun(repo, args...)
	if out != want {
		return fmt.Errorf("git %s reported\n%q\nwant\n%q", strings.Join(args, " "), out, want)
	}
	return nil
}

// indexFile resolves the raw index path the way rootmove.rootState does
// (rev-parse --path-format=absolute --git-dir, then index beside it).
func indexFile(t testing.TB, repo string) string {
	t.Helper()
	out, err := gitRun(repo, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		t.Fatalf("gittest: resolve the git dir of %s: %v\n%s", repo, err, out)
	}
	return filepath.Join(strings.TrimSpace(out), "index")
}

// gitRun runs git -C dir args... and returns its combined output. The kit
// promises to inspect a repository without rewriting the raw index a byte-diff
// proof is watching, and keeps that promise because the reads it makes
// (for-each-ref, ls-files, rev-parse, symbolic-ref) are ones git never
// refreshes the index's stat cache for — not because of GIT_OPTIONAL_LOCKS=0,
// which is set too but only suppresses `git status`'s refresh. It does NOT
// suppress `git diff`'s, which rewrites stale-stat entries even with it off, so
// never add a diff-shaped read here on the strength of that variable. LC_ALL=C
// pins git's messages so the byte-compared outputs are locale-stable.
func gitRun(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	return string(out), err
}
