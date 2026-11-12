package launchwarn

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/gittest"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

func cloneRepo(t *testing.T) string {
	t.Helper()
	_, repo := gittest.Seed(t, gittest.SeedOpts{ExcludeWorktrees: true}).Clone(t)
	r, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func addWorktree(t *testing.T, root, branch string) string {
	t.Helper()
	p := filepath.Join(root, ".worktrees", branch)
	git(t, root, "worktree", "add", "-q", "-b", branch, p)
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// liveOtherClaim writes a claim on path for a real, running process (a sleep
// child killed on cleanup), so the liveness check sees a live session.
func liveOtherClaim(t *testing.T, commonDir, path, agent string) int {
	t.Helper()
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	pid := child.Process.Pid
	return writeClaim(t, commonDir, path, agent, pid, time.Now().Unix())
}

func writeClaim(t *testing.T, commonDir, path, agent string, pid int, started int64) int {
	t.Helper()
	dir := filepath.Join(commonDir, "wt-sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("pid=%d\nstarted=%d\npath=%s\nbranch=main\nagent=%s\n", pid, started, path, agent)
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return pid
}

func processStartedAt(t *testing.T, pid int) int64 {
	t.Helper()
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatal(err)
	}
	started, err := time.ParseInLocation(time.ANSIC, strings.TrimSpace(string(out)), time.Local)
	if err != nil {
		t.Fatal(err)
	}
	return started.Unix()
}

// A clean clone reads as the root on the default branch with nothing to warn
// about — the baseline every other case deviates from.
func TestForDirRoot(t *testing.T) {
	repo := cloneRepo(t)
	tg, err := ForDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	if tg.Dir != repo || !tg.IsRoot || tg.Branch != "main" || tg.RootBranch != "main" || tg.Default != "main" {
		t.Errorf("target = %+v", tg)
	}
	if tg.Dirty || tg.Hint != "" || len(tg.Holders) != 0 || tg.Creation != nil {
		t.Errorf("target = %+v", tg)
	}
}

// A root left on a non-default branch records the drift in RootBranch and
// earns a root_drift warning, so a stray checkout cannot launch silently.
func TestForDirRootDrift(t *testing.T) {
	repo := cloneRepo(t)
	git(t, repo, "checkout", "-q", "-b", "topic")
	tg, err := ForDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	if tg.RootBranch != "topic" || tg.Branch != "topic" || tg.Hint == "" {
		t.Errorf("target = %+v", tg)
	}
	ws, _ := Collect(tg)
	if _, ok := has(ws, CodeRootDrift); !ok {
		t.Errorf("codes = %v, want root_drift", codes(ws))
	}
}

// A linked worktree is not the root and takes no Holders from a claim on the
// root — claims belong to one checkout, or every worktree would read shared.
func TestForDirLinkedWorktree(t *testing.T) {
	repo := cloneRepo(t)
	linked := addWorktree(t, repo, "feat")
	liveOtherClaim(t, filepath.Join(repo, ".git"), repo, "codex")
	git(t, repo, "checkout", "-q", "-b", "topic")
	tg, err := ForDir(linked)
	if err != nil {
		t.Fatal(err)
	}
	if tg.IsRoot || tg.Dir != linked || tg.Branch != "feat" || tg.RootBranch != "topic" {
		t.Errorf("target = %+v", tg)
	}
	if len(tg.Holders) != 0 {
		t.Errorf("holders = %v, want none (the claim is on the root)", tg.Holders)
	}
}

// Outside any repository ForDir returns a zero Target and no error, because
// --cwd launches there and a missing git context is not a failure to report.
func TestForDirNotARepo(t *testing.T) {
	tg, err := ForDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if tg.Dir != "" || tg.IsRoot || tg.Branch != "" {
		t.Errorf("target = %+v, want zero", tg)
	}
}

// A bare repository is a zero Target too: it has no checkout to launch into,
// and must never read as the root of a working tree.
func TestForDirBareRepoIsEmpty(t *testing.T) {
	origin, _ := gittest.Seed(t, gittest.SeedOpts{ExcludeWorktrees: true}).Clone(t)
	tg, err := ForDir(origin)
	if err != nil {
		t.Fatal(err)
	}
	if tg.Dir != "" || tg.IsRoot {
		t.Errorf("target = %+v, want zero", tg)
	}
}

// A subdirectory of the root is a non-root Target at that path: "root" means
// the checkout's toplevel, so root-only warnings cannot fire from a subdir.
func TestForDirSubdirOfRootIsNotRoot(t *testing.T) {
	repo := cloneRepo(t)
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	tg, err := ForDir(sub)
	if err != nil {
		t.Fatal(err)
	}
	if tg.IsRoot || tg.Dir != sub {
		t.Errorf("target = %+v, want a non-root at %s", tg, sub)
	}
}

// A symlinked spelling of the root (macOS /var vs /private/var) is the root.
func TestForDirSeesTheRootThroughASymlink(t *testing.T) {
	repo := cloneRepo(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	tg, err := ForDir(link)
	if err != nil {
		t.Fatal(err)
	}
	if !tg.IsRoot || tg.Dir != repo {
		t.Errorf("target = %+v, want the root at %s", tg, repo)
	}
}

// Holders lists only claims whose process is live and not this session, and
// ForDir never prunes: a stale claim must not block, and gathering must not
// mutate the claims store (it runs before the user has chosen anything).
func TestForDirHoldersAreLiveOthers(t *testing.T) {
	repo := cloneRepo(t)
	common := filepath.Join(repo, ".git")
	pid := liveOtherClaim(t, common, repo, "codex")
	// A dead pid: a process that has exited.
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	writeClaim(t, common, repo, "gone", dead.Process.Pid, time.Now().Unix())

	tg, err := ForDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(tg.Holders) != 1 || tg.Holders[0].PID != pid || tg.Holders[0].Agent != "codex" || tg.RootChosen {
		t.Errorf("holders = %+v chosen=%v, want only the live codex claim", tg.Holders, tg.RootChosen)
	}
	if _, err := os.Stat(filepath.Join(common, "wt-sessions", strconv.Itoa(dead.Process.Pid))); err != nil {
		t.Errorf("the dead claim was pruned (ForDir must be read-only): %v", err)
	}
}

// TestForDirAncestorHeldRootNamesTheUnrelatedHolders checks a nested launch
// under an ancestor that holds the root still names the root's other live
// sessions. The test this replaces (…NestedUnderAHeldRootHasNoHolders) named
// a nested case its body never built — it read at the root — and pinned the
// bug: an ancestor-held root dropped every stranger from Holders, so two
// agents at one root never warned about each other. This builds the real
// nested case (a read at a subdirectory under the held root) beside the root
// case: at both depths the ancestor is absent from Holders (Others skips
// ancestor pids, so the ancestor never self-warns) and the unrelated holder
// is named — Collect turns it into held, and dirty_held when the target is
// dirty. At the root the ancestor's consent (RootChosen) stays.
func TestForDirAncestorHeldRootNamesTheUnrelatedHolders(t *testing.T) {
	repo := cloneRepo(t)
	common := filepath.Join(repo, ".git")
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	ppid := os.Getppid()
	writeClaim(t, common, repo, "claude", ppid, processStartedAt(t, ppid))
	other := liveOtherClaim(t, common, repo, "codex")
	if err := os.WriteFile(filepath.Join(sub, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	onlyCodex := func(t *testing.T, tg Target) {
		t.Helper()
		if len(tg.Holders) != 1 || tg.Holders[0].PID != other || tg.Holders[0].Agent != "codex" {
			t.Errorf("holders = %+v, want only the unrelated codex session (never ancestor pid %d)", tg.Holders, ppid)
		}
	}
	nested, err := ForDir(sub)
	if err != nil {
		t.Fatal(err)
	}
	onlyCodex(t, nested)
	if !nested.Dirty {
		t.Fatal("the nested target is not dirty; the dirty_held half needs it")
	}
	ws, _ := Collect(nested)
	if _, ok := has(ws, CodeHeld); !ok {
		t.Errorf("codes = %v, want held", codes(ws))
	}
	if _, ok := has(ws, CodeDirtyHeld); !ok {
		t.Errorf("codes = %v, want dirty_held", codes(ws))
	}

	atRoot, err := ForDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	onlyCodex(t, atRoot)
	if !atRoot.RootChosen {
		t.Error("RootChosen = false at the root; the ancestor's hold is the consent")
	}
	if _, err := os.Stat(filepath.Join(common, "wt-sessions", strconv.Itoa(ppid))); err != nil {
		t.Errorf("the parent's claim was deleted: %v", err)
	}
}

// An untracked file marks the target Dirty — the input the warnings weigh
// before offering to move or clean the checkout.
func TestForDirDirty(t *testing.T) {
	repo := cloneRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tg, err := ForDir(repo)
	if err != nil || !tg.Dirty {
		t.Errorf("dirty=%v err=%v, want dirty", tg.Dirty, err)
	}
}

// TestForDirKnownDirtSkipsTheProbe checks the gathered-dirt form of ForDir
// trusts the caller's answer instead of probing: an actually dirty repository
// read with a known-clean answer comes back not dirty (the probe would say
// dirty), a known-dirty answer survives a clean one, and plain ForDir still
// probes. It matters because the probe is a full-tree `git status` at the
// commit point: a list pick's row already holds the dirt, and scanning only
// to throw the answer away makes the person wait for nothing — while every
// caller without a gathered answer must keep the probe.
func TestForDirKnownDirtSkipsTheProbe(t *testing.T) {
	repo := cloneRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tg, err := ForDirKnownDirt(repo, false)
	if err != nil {
		t.Fatal(err)
	}
	if tg.Dirty {
		t.Error("ForDirKnownDirt probed (the repository is dirty) instead of taking the caller's answer")
	}
	probed, err := ForDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !probed.Dirty {
		t.Error("ForDir stopped probing; a caller with no gathered answer must get a probed Target.Dirty")
	}
	tg, err = ForDirKnownDirt(cloneRepo(t), true)
	if err != nil {
		t.Fatal(err)
	}
	if !tg.Dirty {
		t.Error("ForDirKnownDirt dropped the caller's dirty answer")
	}
}

// ForNew carries the planned Creation (branch, tracks, new-branch, path) and
// a hint: the about-to-create worktree is the warning path's whole subject.
func TestForNewCarriesTheCreation(t *testing.T) {
	repo := cloneRepo(t)
	git(t, repo, "checkout", "-q", "-b", "topic")
	plan, err := worktree.Resolve(repo, "feat/x")
	if err != nil {
		t.Fatal(err)
	}
	tg, err := ForNew(repo, plan)
	if err != nil {
		t.Fatal(err)
	}
	want := Creation{Branch: "feat/x", Tracks: "", NewBranch: true, Path: plan.Path()}
	if tg.Creation == nil || *tg.Creation != want {
		t.Fatalf("creation = %+v, want %+v", tg.Creation, want)
	}
	if tg.Dir != plan.Path() || tg.Branch != "feat/x" || tg.IsRoot || tg.RootBranch != "topic" || tg.Default != "main" || tg.Hint == "" {
		t.Errorf("target = %+v", tg)
	}
	if !strings.HasSuffix(plan.Path(), filepath.Join(".worktrees", "feat-x")) {
		t.Errorf("plan.Path() = %q", plan.Path())
	}
}

// TestForDirAndForNewCarryTheGoneUpstreamFact checks the flag and typed
// launch paths read the upstream fact the list rows carry: ForDir and ForNew
// set UpstreamGone from pure git facts — the branch's configured upstream and
// whether its remote-tracking ref still exists — so -W, --root, --cwd and the
// typed path warn upstream_gone like a row pick does. No network and no gh
// (only the PR facts stay zero for a caller with a cleanup row); a live
// upstream ref warns nothing.
func TestForDirAndForNewCarryTheGoneUpstreamFact(t *testing.T) {
	for _, c := range []struct {
		name   string
		forNew bool
		gone   bool
	}{
		{name: "ForDir, live upstream"},
		{name: "ForDir, gone upstream", gone: true},
		{name: "ForNew, live upstream", forNew: true},
		{name: "ForNew, gone upstream", forNew: true, gone: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo := cloneRepo(t)
			git(t, repo, "checkout", "-q", "-b", "topic")
			git(t, repo, "config", "branch.topic.remote", "origin")
			git(t, repo, "config", "branch.topic.merge", "refs/heads/topic")
			git(t, repo, "update-ref", "refs/remotes/origin/topic", "HEAD")
			if c.gone {
				git(t, repo, "update-ref", "-d", "refs/remotes/origin/topic")
			}
			var tg Target
			var err error
			if c.forNew {
				plan, perr := worktree.Resolve(repo, "topic")
				if perr != nil {
					t.Fatal(perr)
				}
				tg, err = ForNew(repo, plan)
			} else {
				tg, err = ForDir(repo)
			}
			if err != nil {
				t.Fatal(err)
			}
			if tg.UpstreamGone != c.gone {
				t.Errorf("UpstreamGone = %v, want %v", tg.UpstreamGone, c.gone)
			}
			ws, _ := Collect(tg)
			if _, warned := has(ws, CodeUpstreamGone); warned != c.gone {
				t.Errorf("codes = %v, want upstream_gone warned=%v", codes(ws), c.gone)
			}
		})
	}
}

// TestForDirDetachedWarnsNothingAboutARealDetachedBranch checks the upstream
// probe never sees branchOr's "(detached)" display sentinel as a branch name:
// git accepts a branch literally named (detached), and without this its gone
// upstream would warn upstream_gone on every detached-HEAD launch that shares
// the repository. The same branch checked out (the raw name) is still probed
// and does warn.
func TestForDirDetachedWarnsNothingAboutARealDetachedBranch(t *testing.T) {
	repo := cloneRepo(t)
	git(t, repo, "checkout", "-q", "-b", "(detached)")
	git(t, repo, "config", "branch.(detached).remote", "origin")
	git(t, repo, "config", "branch.(detached).merge", "refs/heads/gone")
	tg, err := ForDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !tg.UpstreamGone {
		t.Errorf("checked-out branch named (detached): UpstreamGone = false; want true (its upstream is gone)")
	}
	git(t, repo, "checkout", "-q", "--detach")
	tg, err = ForDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	if tg.UpstreamGone {
		t.Errorf("detached HEAD: UpstreamGone = true; want false (a branch named (detached) must not lend its upstream)")
	}
	ws, _ := Collect(tg)
	if _, warned := has(ws, CodeUpstreamGone); warned {
		t.Errorf("codes = %v; want no upstream_gone for a detached HEAD", codes(ws))
	}
}

// breakRepoConfig makes git fail at the repository with a real error, not its
// "not a git repository" refusal: .git/config becomes a directory, so git
// exits "fatal: unknown error occurred while reading the configuration
// files". It is the forced PrimaryOf failure the read-failure tests need.
func breakRepoConfig(t *testing.T, repo string) {
	t.Helper()
	cfg := filepath.Join(repo, ".git", "config")
	if err := os.Remove(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestForDirRealGitFailureReturnsThePartialTargetAndError checks a git
// failure that is not git's own "not a repository" is surfaced, never
// swallowed as a silent zero Target: ForDir returns the partial Target (the
// directory it resolved) plus the error, which the callers print as one
// "wt: note:" and proceed on — warn but never prevent. The zero Target with
// no error is only for a directory outside a repository or a bare one.
func TestForDirRealGitFailureReturnsThePartialTargetAndError(t *testing.T) {
	repo := cloneRepo(t)
	breakRepoConfig(t, repo)
	tg, err := ForDir(repo)
	if err == nil {
		t.Fatalf("target = %+v, want a surfaced error for a real git failure", tg)
	}
	if tg.Dir != repo {
		t.Errorf("partial target = %+v, want the resolved dir %s kept", tg, repo)
	}
}

// TestForNewRealGitFailureKeepsTheCreationAndErrors checks ForNew's partial
// Target keeps the plan's Creation through a real git failure: its facts do
// not depend on PrimaryOf, so the frozen creation line still announces
// exactly what the commit point creates while the read failure surfaces as
// the note. Only a directory outside a repository or a bare one yields the
// zero Target with no error.
func TestForNewRealGitFailureKeepsTheCreationAndErrors(t *testing.T) {
	repo := cloneRepo(t)
	plan, err := worktree.Resolve(repo, "feat/x")
	if err != nil {
		t.Fatal(err)
	}
	breakRepoConfig(t, repo)
	tg, err := ForNew(repo, plan)
	if err == nil {
		t.Fatalf("target = %+v, want a surfaced error for a real git failure", tg)
	}
	want := Creation{Branch: "feat/x", Tracks: "", NewBranch: true, Path: plan.Path()}
	if tg.Creation == nil || *tg.Creation != want {
		t.Errorf("creation = %+v, want %+v kept in the partial Target", tg.Creation, want)
	}
	if tg.Dir != plan.Path() || tg.Branch != "feat/x" {
		t.Errorf("partial target = %+v, want the plan's dir and branch kept", tg)
	}
}
