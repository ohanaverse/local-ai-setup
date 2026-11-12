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

func TestForDirNotARepo(t *testing.T) {
	tg, err := ForDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if tg.Dir != "" || tg.IsRoot || tg.Branch != "" {
		t.Errorf("target = %+v, want zero", tg)
	}
}

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

func TestForDirNestedUnderAHeldRootHasNoHolders(t *testing.T) {
	repo := cloneRepo(t)
	common := filepath.Join(repo, ".git")
	ppid := os.Getppid()
	writeClaim(t, common, repo, "claude", ppid, processStartedAt(t, ppid))
	liveOtherClaim(t, common, repo, "codex")

	tg, err := ForDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !tg.RootChosen || tg.Holders != nil {
		t.Errorf("chosen=%v holders=%v, want chosen and no holders", tg.RootChosen, tg.Holders)
	}
	if _, err := os.Stat(filepath.Join(common, "wt-sessions", strconv.Itoa(ppid))); err != nil {
		t.Errorf("the parent's claim was deleted: %v", err)
	}
}

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

func TestForNewCarriesTheCreation(t *testing.T) {
	repo := cloneRepo(t)
	git(t, repo, "checkout", "-q", "-b", "topic")
	plan, err := worktree.Resolve(repo, "feat/x")
	if err != nil {
		t.Fatal(err)
	}
	tg, err := ForNew(repo, "feat/x", plan)
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
