package gittest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGit runs one git command in dir and fails the test when git errors. The
// environment is left alone on purpose: a test that needs the kit's
// GIT_OPTIONAL_LOCKS=0 reads uses the kit, and TestArmIndexProbeMakesAWriteVisible
// must run a plain locking `git status` for its byte-diff to mean anything.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// combined returns one git command's combined output exactly as git wrote it
// (trailing newline included), for a want string that roundTripErr compares
// byte for byte.
func combined(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, _ := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	return string(out)
}

// lineageOf is lineageErr as Lineage calls it: judged against the pair Clone
// registered for repo.
func lineageOf(repo string) error {
	pairsMu.Lock()
	p, ok := pairs[repo]
	pairsMu.Unlock()
	if !ok {
		return fmt.Errorf("not a gittest.Clone pair")
	}
	return lineageErr(repo, p.origin, p.opts)
}

// TestSeedCloneIsolatesOrigins checks two Clone pairs of one Seed never share
// state: a commit pushed to pair A's own origin must be absent from pair B and
// from the shared template, and each repo's origin remote must name its own
// origin's absolute path. Why: every consumer clones from the one shared
// template, and origin cross-talk (a shared origin path, or a template mutated
// through a clone) would let one test's success or failure leak into every
// later test without any assertion naming the cause.
func TestSeedCloneIsolatesOrigins(t *testing.T) {
	tpl := Seed(t, SeedOpts{})
	originA, repoA := tpl.Clone(t)
	originB, repoB := tpl.Clone(t)
	Lineage(t, repoA)
	Lineage(t, repoB)

	// Commit on pair A's repo and push it to pair A's own origin.
	if err := os.WriteFile(filepath.Join(repoA, "pair-a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoA, "add", "pair-a.txt")
	runGit(t, repoA, "-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "pair a")
	runGit(t, repoA, "push", "-u", "origin", "main")
	sha := runGit(t, repoA, "rev-parse", "HEAD")

	// The push must land in pair A's origin (the commit exists somewhere),
	// and nowhere pair B or the template can see.
	if !strings.Contains(runGit(t, originA, "for-each-ref"), sha) {
		t.Errorf("pair A's origin does not hold the pushed commit %s; the isolation checks below would pass vacuously", sha)
	}
	if got := runGit(t, repoB, "for-each-ref"); strings.Contains(got, sha) {
		t.Errorf("pair B's refs hold pair A's commit %s:\n%s", sha, got)
	}
	if got := runGit(t, tpl.Origin, "for-each-ref"); strings.Contains(got, sha) {
		t.Errorf("the shared template origin holds pair A's commit %s; a clone mutated the template:\n%s", sha, got)
	}

	if originA == originB {
		t.Errorf("two pairs share origin %q", originA)
	}

	// Each repo's origin remote must name its own origin's absolute path.
	for repo, origin := range map[string]string{repoA: originA, repoB: originB} {
		if got := runGit(t, repo, "remote", "get-url", "origin"); got != origin {
			t.Errorf("repo %s points at origin %q, want its own %q", repo, got, origin)
		}
	}
}

// TestLineageGateNamesThePoison checks lineageErr refuses a repo holding an
// entry under .worktrees/ and that its message names the entry. Why: the gate
// exists so one test's wrong success (a worktree left behind, or a template
// mutated in place) fails loudly at the next clone instead of silently
// corrupting the exact-count assertions that trust a pristine repo; a verdict
// that does not name the poison sends the reader hunting.
func TestLineageGateNamesThePoison(t *testing.T) {
	tpl := Seed(t, SeedOpts{})
	_, repo := tpl.Clone(t)
	Lineage(t, repo)
	if err := os.MkdirAll(filepath.Join(repo, ".worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".worktrees", "stray"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := lineageOf(repo)
	if err == nil {
		t.Fatal("lineageErr accepted a repo holding .worktrees/stray")
	}
	if !strings.Contains(err.Error(), "stray") {
		t.Errorf("lineageErr said %q; the message must name the poison (stray)", err)
	}
}

// TestLineageGateNamesEachPoison checks lineageErr's remaining verdicts name
// their poison too: a surviving rescue record ref, a leftover topic branch, an
// appended info/exclude line (and a second .worktrees/ line, which only the
// ExcludeWorktrees lineage may carry once) and a HEAD moved off main. Why: the
// gate exists to fail loudly on exactly these cross-test leaks, and a verdict
// no test exercises can rot into accepting the poison it was written to refuse
// — the .worktrees/ entry above covers only one of the five shapes.
func TestLineageGateNamesEachPoison(t *testing.T) {
	appendExclude := func(t *testing.T, repo, line string) {
		t.Helper()
		ex := runGit(t, repo, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
		f, err := os.OpenFile(ex, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(line + "\n"); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name  string
		want  string
		setup func(t *testing.T, repo string)
	}{
		{"rescued record ref", "refs/wt/rescued/", func(t *testing.T, repo string) {
			runGit(t, repo, "update-ref", "refs/wt/rescued/0123456789abcdef", "HEAD")
		}},
		{"topic branch", "refs/heads/topic", func(t *testing.T, repo string) {
			runGit(t, repo, "branch", "topic")
		}},
		{"appended exclude line", "scratch/", func(t *testing.T, repo string) {
			appendExclude(t, repo, "scratch/")
		}},
		{"second worktrees line", ".worktrees/", func(t *testing.T, repo string) {
			appendExclude(t, repo, ".worktrees/")
			appendExclude(t, repo, ".worktrees/")
		}},
		{"origin of another pair", "not this pair's own", func(t *testing.T, repo string) {
			other, _ := Seed(t, SeedOpts{}).Clone(t)
			runGit(t, repo, "remote", "set-url", "origin", other)
		}},
		{"extra ref", "refs/heads/feature", func(t *testing.T, repo string) {
			runGit(t, repo, "update-ref", "refs/heads/feature", "HEAD")
		}},
		{"deleted origin/HEAD", "refs/remotes/origin/HEAD is missing", func(t *testing.T, repo string) {
			runGit(t, repo, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
		}},
		{"head moved", "not refs/heads/main", func(t *testing.T, repo string) {
			runGit(t, repo, "switch", "-q", "-c", "other")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tpl := Seed(t, SeedOpts{})
			_, repo := tpl.Clone(t)
			Lineage(t, repo)
			c.setup(t, repo)
			err := lineageOf(repo)
			if err == nil {
				t.Fatalf("lineageErr accepted a repo holding %s", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("lineageErr said %q; the message must name the poison (%s)", err, c.want)
			}
		})
	}
}

// TestLineageGateIsLineageAware checks the exclude verdict follows the pair's
// own lineage: the ExcludeWorktrees lineage must carry exactly one .worktrees/
// line and the plain lineage none, on the repo and on its origin.
func TestLineageGateIsLineageAware(t *testing.T) {
	truncate := func(t *testing.T, dir string) {
		t.Helper()
		ex := runGit(t, dir, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
		if err := os.WriteFile(ex, []byte("# none\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	appendLine := func(t *testing.T, dir string) {
		t.Helper()
		ex := runGit(t, dir, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
		f, err := os.OpenFile(ex, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(".worktrees/\n"); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("missing line on the exclude lineage", func(t *testing.T) {
		_, repo := Seed(t, SeedOpts{ExcludeWorktrees: true}).Clone(t)
		Lineage(t, repo)
		truncate(t, repo)
		if err := lineageOf(repo); err == nil || !strings.Contains(err.Error(), "lacks the .worktrees/ line") {
			t.Errorf("lineageErr said %v, want the missing .worktrees/ line named", err)
		}
	})
	t.Run("stray line on the plain lineage", func(t *testing.T) {
		_, repo := Seed(t, SeedOpts{}).Clone(t)
		Lineage(t, repo)
		appendLine(t, repo)
		if err := lineageOf(repo); err == nil || !strings.Contains(err.Error(), "must not have") {
			t.Errorf("lineageErr said %v, want the stray .worktrees/ line named", err)
		}
	})
	t.Run("origin exclude is checked too", func(t *testing.T) {
		origin, repo := Seed(t, SeedOpts{}).Clone(t)
		Lineage(t, repo)
		appendLine(t, origin)
		if err := lineageOf(repo); err == nil || !strings.Contains(err.Error(), origin) {
			t.Errorf("lineageErr said %v, want the origin's exclude named", err)
		}
	})
}

// TestLineageRefusesAStrangerRepo checks Lineage fails loud on a repo Clone did
// not make, naming why.
func TestLineageRefusesAStrangerRepo(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	msg := recordFatal(t, func(tb testing.TB) { Lineage(tb, dir) })
	if !strings.Contains(msg, "not a gittest.Clone pair") {
		t.Errorf("Lineage said %q, want it to refuse a repo Clone did not make", msg)
	}
}

// TestSeedRetriesAFailedBuild checks a failed build is neither memoized nor
// leaked: the next Seed for the same opts builds again and succeeds. Why: a
// memoized error would fail every later test in the process for one transient
// failure.
func TestSeedRetriesAFailedBuild(t *testing.T) {
	// The memo entry is saved and restored so other tests keep their template.
	opts := SeedOpts{ExcludeWorktrees: true}
	seedMu.Lock()
	saved, had := seeds[opts]
	delete(seeds, opts)
	seedMu.Unlock()
	t.Cleanup(func() {
		seedMu.Lock()
		defer seedMu.Unlock()
		if had {
			seeds[opts] = saved
		} else {
			delete(seeds, opts)
		}
	})
	calls := 0
	build := func(o SeedOpts) (*Template, error) {
		calls++
		if calls == 1 {
			return nil, fmt.Errorf("boom")
		}
		return buildTemplate(o)
	}
	if msg := recordFatal(t, func(tb testing.TB) { seedWith(tb, opts, build) }); !strings.Contains(msg, "boom") {
		t.Fatalf("first seed said %q, want the build error", msg)
	}
	tpl := seedWith(t, opts, build)
	if tpl == nil || calls != 2 {
		t.Fatalf("retry: tpl=%v after %d builds, want a template after exactly 2", tpl, calls)
	}
	if again := seedWith(t, opts, build); again != tpl || calls != 2 {
		t.Errorf("a successful build must be memoized: got %p after %d builds", again, calls)
	}
}

// TestArmIndexProbeMakesAWriteVisible checks arming turns the raw-index
// byte-diff proof from mute to biting: on a fresh copy a locking `git status`
// rewrite is byte-identical (the proof passes while a write happens), while on
// an armed copy the same rewrite lands in the raw bytes. Why: every "this
// reader wrote nothing" proof compares raw index bytes, and without the armed
// shape it cannot see the write it exists to catch (the flake history is in
// internal/cleanup/dirt_test.go's TestDirtCoveredRacyCleanEntry).
func TestArmIndexProbeMakesAWriteVisible(t *testing.T) {
	tpl := Seed(t, SeedOpts{})
	_, repo := tpl.Clone(t)
	Lineage(t, repo)
	before := RawIndex(t, repo)
	runGit(t, repo, "status")
	if after := RawIndex(t, repo); after != before {
		t.Fatalf("a first status rewrote the raw index (%d -> %d bytes) on a fresh copy; the muting that arms must restore is gone", len(before), len(after))
	}
	ArmIndexProbe(t, repo)
	AssertProbeArmed(t, repo)
	runGit(t, repo, "status")
	if after := RawIndex(t, repo); after == before {
		t.Fatal("the armed probe saw no write: a locked status rewrite must change the raw index bytes or the byte-diff proof stays mute")
	}
}

// TestRoundTripRejectsCorruptShape checks roundTripErr compares git's real
// output byte for byte: a hand-built .git/worktrees/ghost shape git reports
// differently must fail against the clean expectation and pass only against
// what git actually says now. Why: hand-built .git shapes encode the fixture
// author's guess of git's on-disk format, and a shape git treats as corrupt
// would otherwise test git's error path while the suite reads green (design
// doc, anti-goal 6: every hand-built shape round-trips through real git).
func TestRoundTripRejectsCorruptShape(t *testing.T) {
	tpl := Seed(t, SeedOpts{})
	_, repo := tpl.Clone(t)
	Lineage(t, repo)
	want := combined(t, repo, "worktree", "list", "--porcelain")
	ghost := filepath.Join(repo, ".git", "worktrees", "ghost")
	if err := os.MkdirAll(ghost, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ghost, "gitdir"), []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := roundTripErr(repo, want, "worktree", "list", "--porcelain"); err == nil {
		t.Fatal("roundTripErr accepted a repo carrying a corrupt .git/worktrees/ghost against the clean expectation")
	}
	if err := roundTripErr(repo, combined(t, repo, "worktree", "list", "--porcelain"), "worktree", "list", "--porcelain"); err != nil {
		t.Fatalf("roundTripErr rejected the want git actually reports: %v", err)
	}
}

// TestTemplateNeverCarriesWorktrees checks the seeded template origin is just
// the bare root: `git worktree list --porcelain` names one worktree and it is
// the bare repository itself. Why: a template holding linked worktrees carries
// copied .git/gitdir back-pointers that break every descendant copy (the
// builder-review hazard of Constraint 4); this asserts the hazard is
// impossible by construction, so no clone can inherit one.
func TestTemplateNeverCarriesWorktrees(t *testing.T) {
	tpl := Seed(t, SeedOpts{})
	out := combined(t, tpl.Origin, "worktree", "list", "--porcelain")
	var listed []string
	bare := false
	for _, line := range strings.Split(out, "\n") {
		if name, ok := strings.CutPrefix(line, "worktree "); ok {
			listed = append(listed, name)
		}
		if line == "bare" {
			bare = true
		}
	}
	if len(listed) != 1 || !bare {
		t.Fatalf("template origin reports %d worktrees (bare=%v), want just the bare root:\n%s", len(listed), bare, out)
	}
	// git prints the resolved path (/private/var vs /var on macOS), so the
	// comparison follows symlinks rather than the spelling.
	got, err := filepath.EvalSymlinks(listed[0])
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(tpl.Origin)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("template origin lists worktree %q, want its own root %q", got, want)
	}
}

// fatalRecorder stands in for a test whose Fatalf must be observed, not
// obeyed: Fatalf records the message and panics with fatalSentinel so the
// caller unwinds the way t.Fatalf's Goexit would, and recordFatal recovers it.
type fatalRecorder struct {
	testing.TB
	msg string
}

type fatalSentinel struct{}

func (r *fatalRecorder) Fatalf(format string, args ...any) {
	r.msg = fmt.Sprintf(format, args...)
	panic(fatalSentinel{})
}

// recordFatal runs fn against a recorder and returns what fn's Fatalf said, or
// "" when fn did not fail.
func recordFatal(t *testing.T, fn func(testing.TB)) string {
	t.Helper()
	r := &fatalRecorder{TB: t}
	func() {
		defer func() {
			if v := recover(); v != nil {
				if _, ok := v.(fatalSentinel); !ok {
					panic(v)
				}
			}
		}()
		fn(r)
	}()
	return r.msg
}

// TestGitReturnsTrimmedOutputAndFailsOnBadArgs checks Git hands back git's
// output with the trailing newline trimmed, and that a failing command fails
// the test with "git <args>: <err>" followed by git's own output. Why: every
// package's fixture helper delegates to it, so a Git that swallowed a failure
// would turn a broken setup into a green test.
func TestGitReturnsTrimmedOutputAndFailsOnBadArgs(t *testing.T) {
	_, repo := Seed(t, SeedOpts{}).Clone(t)
	if got := Git(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Errorf("Git rev-parse = %q, want %q (trimmed, no newline)", got, "main")
	}
	msg := recordFatal(t, func(tb testing.TB) { Git(tb, repo, "no-such-subcommand") })
	if !strings.HasPrefix(msg, "git no-such-subcommand: ") || !strings.Contains(msg, "not a git command") {
		t.Errorf("failure message = %q, want it to name the args, the error and git's output", msg)
	}
}

// TestNewRepoBuildsTheBaseRepository checks NewRepo's pair is the base
// repository every fixture starts from: origin/HEAD set, main tracking
// origin/main, identity T, one README commit "init". Why: the five packages
// that used to build this by hand now trust it, so the shape is pinned here
// once.
func TestNewRepoBuildsTheBaseRepository(t *testing.T) {
	origin, repo := NewRepo(t, false)
	if got := Git(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD"); got != "refs/remotes/origin/main" {
		t.Errorf("origin/HEAD = %q, want refs/remotes/origin/main", got)
	}
	if got := Git(t, repo, "rev-parse", "--abbrev-ref", "main@{upstream}"); got != "origin/main" {
		t.Errorf("main tracks %q, want origin/main", got)
	}
	if got := Git(t, repo, "config", "user.name"); got != "T" {
		t.Errorf("user.name = %q, want T", got)
	}
	if got := Git(t, repo, "log", "--format=%s", "main"); got != "init" {
		t.Errorf("log = %q, want the single seed commit \"init\"", got)
	}
	if b, err := os.ReadFile(filepath.Join(repo, "README")); err != nil || string(b) != "init" {
		t.Errorf("README = %q, %v; want \"init\"", b, err)
	}
	if got := Git(t, origin, "rev-parse", "main"); got != Git(t, repo, "rev-parse", "main") {
		t.Errorf("origin main %s differs from the repo's", got)
	}
	// The exclude flag picks the lineage.
	_, ex := NewRepo(t, true)
	if b, _ := os.ReadFile(filepath.Join(ex, ".git", "info", "exclude")); !strings.Contains(string(b), ".worktrees/") {
		t.Errorf("NewRepo(excludeWorktrees=true) info/exclude lacks .worktrees/:\n%s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude")); strings.Contains(string(b), ".worktrees/") {
		t.Errorf("NewRepo(excludeWorktrees=false) info/exclude has .worktrees/:\n%s", b)
	}
}
