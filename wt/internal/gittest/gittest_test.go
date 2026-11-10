package gittest

import (
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
	err := lineageErr(repo)
	if err == nil {
		t.Fatal("lineageErr accepted a repo holding .worktrees/stray")
	}
	if !strings.Contains(err.Error(), "stray") {
		t.Errorf("lineageErr said %q; the message must name the poison (stray)", err)
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
