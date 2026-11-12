package rootclaim

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
)

// procStart returns pid's start time in epoch seconds — what a claim records
// (docs/contracts/session-claims.md: equal to the real start within 5 s). A
// fixture "other session" must read live under that rule, and rootclaim's
// tests cannot fake sessionclaim's process seams (unexported, and this task
// adds no exported ones), so the claim names a real child's real start time,
// read from ps's lstart in the local zone.
func procStart(t *testing.T, pid int) int64 {
	t.Helper()
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C") // English month names for the parse
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ps lstart for %d: %v", pid, err)
	}
	start, err := time.ParseInLocation(time.ANSIC, strings.TrimSpace(string(out)), time.Local)
	if err != nil {
		t.Fatalf("parse lstart %q: %v", out, err)
	}
	return start.Unix()
}

// holderClaim spawns a short-lived real process and claims path for it under
// commonDir — a fixture "other session" for HoldLaunch to report (or not),
// one that reads live under the claim contract's liveness rule. The child and
// its claim are cleaned up.
func holderClaim(t *testing.T, commonDir, path, branch, agent string) sessionclaim.Claim {
	t.Helper()
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	c := sessionclaim.Claim{PID: child.Process.Pid, Started: procStart(t, child.Process.Pid), Path: path, Branch: branch, Agent: agent}
	release, err := sessionclaim.Hold(commonDir, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return c
}

// parseClaim reads one claim file's key=value lines; the format is a
// cross-repo contract (docs/contracts/session-claims.md).
func parseClaim(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			fields[k] = v
		}
	}
	return fields
}

// TestHoldLaunchWritesClaimKeyedByWorktreePath checks HoldLaunch claims the
// worktree the launch directory sits in: a subdirectory of a linked worktree
// claims that worktree's top level — the path Holders keys by, so a toplevel
// launch and a nested one in one worktree report each other — carrying this
// process's pid and real start time and the agent's name. release (safe
// twice) takes exactly that claim down.
func TestHoldLaunchWritesClaimKeyedByWorktreePath(t *testing.T) {
	root, linked := linkedRepo(t)
	sub := filepath.Join(linked, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	release, others, err := HoldLaunch(sub, "claude")
	t.Cleanup(release)
	if err != nil || len(others) != 0 {
		t.Fatalf("HoldLaunch = others %v, err %v; want a release, no others, no error", others, err)
	}
	claimPath := filepath.Join(root, ".git", "wt-sessions", strconv.Itoa(os.Getpid()))
	fields := parseClaim(t, claimPath)
	if fields["pid"] != strconv.Itoa(os.Getpid()) || fields["path"] != linked || fields["branch"] != "feat" || fields["agent"] != "claude" {
		t.Errorf("claim = %v; want pid %d, path %s, branch feat, agent claude", fields, os.Getpid(), linked)
	}
	self, err := sessionclaim.Self("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	started, err := strconv.ParseInt(fields["started"], 10, 64)
	if err != nil {
		t.Fatalf("claim started = %q; want epoch seconds", fields["started"])
	}
	if d := started - self.Started; d < -5 || d > 5 {
		t.Errorf("claim started = %d, this process's start reads %d; want within the contract's 5 s", started, self.Started)
	}
	release()
	release() // safe twice; the claim must be gone after the first
	if _, err := os.Stat(claimPath); !os.IsNotExist(err) {
		t.Errorf("claim file still present after release (stat err %v)", err)
	}
}

// TestHoldLaunchReportsOthersOnSamePathOnly checks HoldLaunch reports exactly
// the live claims other sessions hold on this launch's worktree path: this
// session's own claim is not another session, and a claim on another
// worktree's path is another room's business — with the peer present and the
// elsewhere claim absent, one claim is exactly right. The foreign claims
// belong to real running processes, cleaned up after.
func TestHoldLaunchReportsOthersOnSamePathOnly(t *testing.T) {
	root, linked := linkedRepo(t)
	commonDir := filepath.Join(root, ".git")
	peer := holderClaim(t, commonDir, linked, "feat", "peer")
	elsewhere := holderClaim(t, commonDir, root, "main", "peer-elsewhere")
	release, others, err := HoldLaunch(linked, "claude")
	t.Cleanup(release)
	if err != nil {
		t.Fatalf("HoldLaunch = %v; want no error", err)
	}
	if len(others) != 1 {
		t.Fatalf("others = %+v; want exactly one claim, the peer %+v (the elsewhere claim %+v must not appear)", others, peer, elsewhere)
	}
	if others[0] != peer {
		t.Errorf("others[0] = %+v; want the peer %+v", others[0], peer)
	}
}

// TestHoldLaunchNotARepoIsNoop checks a launch with no worktree to claim —
// outside any repository, or in a bare one (git runs, but there is no work
// tree) — is a clean no-op: a non-nil twice-safe release, no others, no
// error, nothing written. Claims feed labels and warnings and must never
// block or fail a launch.
func TestHoldLaunchNotARepoIsNoop(t *testing.T) {
	bare := filepath.Join(t.TempDir(), "r.git")
	if out, err := exec.Command("git", "init", "--bare", "-q", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	for name, dir := range map[string]string{"outside any repository": t.TempDir(), "bare repository": bare} {
		release, others, err := HoldLaunch(dir, "claude")
		if release == nil {
			t.Fatalf("HoldLaunch(%s) returned a nil release", name)
		}
		release()
		release() // twice-safe no-op
		if others != nil || err != nil {
			t.Errorf("HoldLaunch(%s) = others %v, err %v; want nil others, nil error", name, others, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "wt-sessions")); !os.IsNotExist(err) {
			t.Errorf("HoldLaunch(%s) created wt-sessions (stat err %v)", name, err)
		}
	}
}
