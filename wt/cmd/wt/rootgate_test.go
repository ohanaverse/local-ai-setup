package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// gateWorktree adds a linked worktree named name to the repository at root and
// returns its path.
func gateWorktree(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if out, err := exec.Command("git", "-C", root, "worktree", "add", "-q", "-b", name, path).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	return path
}

// TestGateIgnoresNonRoots checks the gate only concerns the root checkout: a
// linked worktree, a subdirectory of the root and a directory outside any
// repository pass without a read, so --cwd there stays silent as before.
func TestGateIgnoresNonRoots(t *testing.T) {
	root := sharedRootRepo(t)
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{
		"linked":  gateWorktree(t, root, "feat"),
		"subdir":  sub,
		"outside": t.TempDir(),
	} {
		var out bytes.Buffer
		if err := gateRoot(dir, "claude", failReader{t}, &out, false); err != nil {
			t.Errorf("%s: gateRoot = %v, want nil", name, err)
		}
		if out.Len() != 0 {
			t.Errorf("%s: unexpected output %q", name, out.String())
		}
	}
}

// TestGateNoTerminalRefusesNamingRoot checks a script at the root gets a
// refusal that names --root, the way to say yes without a prompt.
func TestGateNoTerminalRefusesNamingRoot(t *testing.T) {
	root := sharedRootRepo(t)
	err := gateRoot(root, "claude", failReader{t}, &bytes.Buffer{}, false)
	if err == nil || !strings.Contains(err.Error(), "--root") {
		t.Fatalf("err = %v, want a refusal naming --root", err)
	}
}

// TestGateTerminalAnswers checks y lets the launch go ahead and n cancels it
// quietly (errCancelled), so a "no" is a choice and not a failure.
func TestGateTerminalAnswers(t *testing.T) {
	root := sharedRootRepo(t)
	if err := gateRoot(root, "claude", strings.NewReader("y"), &bytes.Buffer{}, true); err != nil {
		t.Errorf("y: err = %v, want nil", err)
	}
	if err := gateRoot(root, "claude", strings.NewReader("n"), &bytes.Buffer{}, true); !errors.Is(err, errCancelled) {
		t.Errorf("n: err = %v, want errCancelled", err)
	}
}

// TestGateLeavesTheQuestionToTheSharedMenu checks that when another live
// session holds the root the gate stays silent, so the person sees one
// question (the shared-root menu) and not two.
func TestGateLeavesTheQuestionToTheSharedMenu(t *testing.T) {
	root := sharedRootRepo(t)
	liveOtherClaim(t, root, "codex")
	if err := gateRoot(root, "claude", failReader{t}, &bytes.Buffer{}, true); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

// TestGateAllowsANestedLaunchUnderAHeldRoot checks an agent at the root that
// runs a nested wt --cwd is not newly refused: a claim by a parent process on
// the root means the root was already agreed to.
func TestGateAllowsANestedLaunchUnderAHeldRoot(t *testing.T) {
	root := sharedRootRepo(t)
	dir := filepath.Join(root, ".git", "wt-sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ppid := os.Getppid()
	body := fmt.Sprintf("pid=%d\nstarted=%d\npath=%s\nbranch=main\nagent=claude\n", ppid, time.Now().Unix(), root)
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(ppid)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := gateRoot(root, "claude", failReader{t}, &bytes.Buffer{}, false); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

// TestGateSeesTheRootThroughASymlink checks a symlinked spelling of the root
// (macOS /var vs /private/var) is still the root, so the gate cannot be
// walked around by how the path is written.
func TestGateSeesTheRootThroughASymlink(t *testing.T) {
	root := sharedRootRepo(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	err := gateRoot(link, "claude", failReader{t}, &bytes.Buffer{}, false)
	if err == nil || !strings.Contains(err.Error(), "--root") {
		t.Fatalf("err = %v, want a refusal naming --root", err)
	}
}

// TestRefuseRootBranch checks -W resolving to the root is refused with a
// pointer at --root, while a linked worktree path is let through.
func TestRefuseRootBranch(t *testing.T) {
	root := sharedRootRepo(t)
	if err := refuseRootBranch("main", root); err == nil || !strings.Contains(err.Error(), "--root") {
		t.Errorf("root: err = %v, want a refusal naming --root", err)
	}
	if err := refuseRootBranch("feat", gateWorktree(t, root, "feat")); err != nil {
		t.Errorf("linked worktree: err = %v, want nil", err)
	}
}
