package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// TestLaunchGateRootNoOthersPrintsInfoAndClaims checks the gate's common root
// case costs no keystroke: a root nobody else holds prints the one
// informational line (the shared-root screen's replacement) and claims the
// root for the agent's run, asking nothing. It matters because every plain
// root launch goes through it — a question here would tax the routine path.
func TestLaunchGateRootNoOthersPrintsInfoAndClaims(t *testing.T) {
	t.Cleanup(releaseRootClaim)
	root := rootRepo(t)
	stderr := answerOnStdio(t, "")
	m := model{agent: "claude", selectedPath: root}
	tg, err := m.launchTarget()
	if err != nil {
		t.Fatal(err)
	}
	if !m.launchGate(&tg) {
		t.Fatal("launchGate said no to a root nobody else holds")
	}
	if got := stderr(); got != "wt: launching in "+root+"\n" {
		t.Errorf("gate printed %q; want the one informational line only (no question)", got)
	}
	if !claimExists(root) {
		t.Error("the root was not claimed for the agent's run")
	}
}

// TestLaunchGateLinkedWorktreeIsSilentAndUnclaimed checks a linked-worktree
// pick is deliberate and costs nothing: the gate prints nothing, asks nothing
// and claims nothing — even while another session holds the root — because a
// linked worktree is the ordinary launch and claims are root-only.
func TestLaunchGateLinkedWorktreeIsSilentAndUnclaimed(t *testing.T) {
	t.Cleanup(releaseRootClaim)
	root := rootRepo(t)
	linked := linkedWorktree(t, root)
	liveOther(t, root, "codex")
	stderr := answerOnStdio(t, "")
	m := model{agent: "claude", selectedPath: linked}
	tg, err := m.launchTarget()
	if err != nil {
		t.Fatal(err)
	}
	if !m.launchGate(&tg) {
		t.Fatal("launchGate said no to a linked worktree")
	}
	if got := stderr(); got != "" {
		t.Errorf("gate printed %q; want silence", got)
	}
	if claimExists(root) {
		t.Error("a linked-worktree launch claimed the root")
	}
}

// TestLaunchGateRootHeldWarnsAndContinues checks the warned root case asks
// exactly one default-yes question naming the holder and continues on yes,
// claiming the root beside the other session — the override the warnings flow
// exists for.
func TestLaunchGateRootHeldWarnsAndContinues(t *testing.T) {
	t.Cleanup(releaseRootClaim)
	root := rootRepo(t)
	pid := liveOther(t, root, "codex")
	stderr := answerOnStdio(t, "y")
	m := model{agent: "claude", selectedPath: root}
	tg, err := m.launchTarget()
	if err != nil {
		t.Fatal(err)
	}
	if !m.launchGate(&tg) {
		t.Fatal("launchGate said no after a yes")
	}
	got := stderr()
	if !strings.Contains(got, "in use by another session") || !strings.Contains(got, "codex") {
		t.Errorf("gate did not warn about the holder: %q", got)
	}
	if !strings.Contains(got, "pid "+fmt.Sprint(pid)) {
		t.Errorf("gate did not name the holding session: %q", got)
	}
	if !strings.Contains(got, "wt: continue? [Y/n]: ") {
		t.Errorf("gate did not ask the one question: %q", got)
	}
	if strings.Count(got, "continue?") != 1 {
		t.Errorf("gate asked %d times, want once: %q", strings.Count(got, "continue?"), got)
	}
	if !claimExists(root) {
		t.Error("the root was not claimed after a yes")
	}
}

// TestLaunchGateNoQuitsQuietly checks a "no" is a choice, not a failure: the
// gate returns false and the run ends with nothing after the question — no
// message, no claim, nothing launched — as quitting the picker does.
func TestLaunchGateNoQuitsQuietly(t *testing.T) {
	t.Cleanup(releaseRootClaim)
	root := rootRepo(t)
	liveOther(t, root, "codex")
	stderr := answerOnStdio(t, "n")
	m := model{agent: "claude", selectedPath: root}
	tg, err := m.launchTarget()
	if err != nil {
		t.Fatal(err)
	}
	if m.launchGate(&tg) {
		t.Fatal("launchGate said go after a no")
	}
	got := stderr()
	if !strings.HasSuffix(got, "wt: continue? [Y/n]: ") {
		t.Errorf("gate printed %q after the question; want nothing (a quiet quit)", got)
	}
	if claimExists(root) {
		t.Error("a declined gate claimed the root")
	}
}

// TestLaunchGateClaimFailureIsANote checks a claim that cannot be written is
// one "wt: note:" line and never a refusal: the launch is the person's to
// make, and a filesystem hiccup must not stop it.
func TestLaunchGateClaimFailureIsANote(t *testing.T) {
	t.Cleanup(releaseRootClaim)
	root := rootRepo(t)
	sessions := root + "/.git/wt-sessions"
	if err := os.MkdirAll(sessions, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sessions, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sessions, 0o755) })
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	stderr := answerOnStdio(t, "")
	m := model{agent: "claude", selectedPath: root}
	tg, err := m.launchTarget()
	if err != nil {
		t.Fatal(err)
	}
	if !m.launchGate(&tg) {
		t.Fatal("a claim failure stopped the launch")
	}
	got := stderr()
	if strings.Count(got, "wt: note:") != 1 || !strings.Contains(got, "could not check other sessions") {
		t.Errorf("gate printed %q; want a one-line note about the claim", got)
	}
}

// TestLaunchGateStatesTheCreationInTheInfoLine checks a bare-branch or
// remote-only pick states what it will create in the one informational line —
// launchwarn's frozen creation wording, before anything exists — and makes
// the worktree only once the gate says go. A declined gate creates nothing
// (ForNew's contract), so the pick resolves its plan read-only and the commit
// point creates; and the branch is always the pick's, never a generated
// session name.
func TestLaunchGateStatesTheCreationInTheInfoLine(t *testing.T) {
	cases := []struct {
		name   string
		local  string // local branch to seed with no worktree (the bare-branch row)
		ref    string // remote-tracking ref to seed (the remote-only row)
		pick   string // the picker row's branch spelling
		branch string // the branch the worktree must check out
		tracks string // the ref it must track ("" for none)
	}{
		{name: "bare branch", local: "feature", pick: "feature", branch: "feature"},
		{name: "remote-only", ref: "refs/remotes/origin/topic", pick: "origin/topic", branch: "topic", tracks: "origin/topic"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(releaseRootClaim)
			root := rootRepo(t)
			if tc.local != "" {
				if out, err := exec.Command("git", "-C", root, "branch", tc.local).CombinedOutput(); err != nil {
					t.Fatalf("git branch: %v\n%s", err, out)
				}
			}
			if tc.ref != "" {
				// remoteRefParts reads the configured remote's name, so the
				// fixture needs one; only its name matters here.
				if out, err := exec.Command("git", "-C", root, "remote", "add", "origin", "https://example.invalid/origin.git").CombinedOutput(); err != nil {
					t.Fatalf("git remote add: %v\n%s", err, out)
				}
				if out, err := exec.Command("git", "-C", root, "update-ref", tc.ref, "HEAD").CombinedOutput(); err != nil {
					t.Fatalf("git update-ref: %v\n%s", err, out)
				}
			}
			plan, err := worktree.Resolve(root, tc.pick)
			if err != nil {
				t.Fatal(err)
			}
			m := model{agent: "claude", creation: &launchCreation{repoRoot: root, name: tc.pick, plan: plan}}
			tg, err := m.launchTarget()
			if err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("creating worktree %s for branch %s", plan.Path(), tc.branch)
			if tc.tracks != "" {
				want = fmt.Sprintf("creating worktree %s for branch %s tracking %s", plan.Path(), tc.branch, tc.tracks)
			}

			// Declined (a warning forces the question): the line stated the
			// creation, and nothing was created.
			tg.Holders = []sessionclaim.Claim{{PID: 4242, Started: time.Now().Unix(), Path: root, Branch: "main", Agent: "codex"}}
			declined := answerOnStdio(t, "n")
			if m.launchGate(&tg) {
				t.Fatal("launchGate said go after a no")
			}
			if got := declined(); !strings.Contains(got, "wt: "+want+"\n") {
				t.Errorf("gate printed %q; want the creation line %q", got, want)
			}
			if _, err := os.Stat(plan.Path()); err == nil {
				t.Errorf("%s exists after a declined gate; a decline creates nothing", plan.Path())
			}

			// Continued: the worktree exists at the plan's path, on the
			// pick's branch (never a session-<pid> name), tracking what the
			// row said.
			accepted := answerOnStdio(t, "y")
			if !m.launchGate(&tg) {
				t.Fatal("launchGate said no after a yes")
			}
			if got := accepted(); !strings.Contains(got, "wt: "+want+"\n") {
				t.Errorf("gate printed %q; want the creation line %q", got, want)
			}
			if _, err := os.Stat(plan.Path()); err != nil {
				t.Fatalf("worktree %s not created after a yes: %v", plan.Path(), err)
			}
			out, err := exec.Command("git", "-C", plan.Path(), "branch", "--show-current").Output()
			if br := strings.TrimSpace(string(out)); err != nil || br != tc.branch {
				t.Errorf("worktree is on %q (%v); want %q", br, err, tc.branch)
			}
		})
	}
}

// TestRootLaunchClaimsUntilTheAgentExits checks the run command claims the
// root for exactly the agent's run through the gate: the agent sees this
// session's claim, and it is gone once the agent exits. It matters because
// the held warning other launches show is only honest while the claim is
// scoped to the run.
func TestRootLaunchClaimsUntilTheAgentExits(t *testing.T) {
	keepPendingPostExit(t)
	t.Cleanup(releaseRootClaim)
	root := rootRepo(t)
	agent := exec.Command("sh", "-c", "grep -q '^agent=claude$' '"+myClaim(root)+"'")
	agent.Dir = root
	m := model{agent: "claude", selectedPath: root}
	msg := runAndWaitCmd(agent, "claude", config.Model{}, m.gateFor())()
	if done, ok := msg.(launchDoneMsg); !ok || done.err != nil {
		t.Fatalf("msg = %#v; the agent did not see the claim while it ran", msg)
	}
	if claimExists(root) {
		t.Error("the claim outlived the agent")
	}
}

// TestSlotRootLaunchClaimsUntilTheAgentExits is the same for a slot-profile
// launch, which has its own run command and must scope its claim identically.
func TestSlotRootLaunchClaimsUntilTheAgentExits(t *testing.T) {
	keepPendingPostExit(t)
	t.Cleanup(releaseRootClaim)
	stubSlotApplier(t, func(*exec.Cmd, string, catalog.Row) (func() error, error) { return nil, nil })
	root := rootRepo(t)
	agent := exec.Command("sh", "-c", "test -f '"+myClaim(root)+"'")
	agent.Dir = root
	row := catalog.Row{Model: config.Model{ID: "profile/cloud"}, Profile: &catalog.ProfileRow{}}
	m := model{agent: "claude", selectedPath: root}
	msg := runSlotAndWaitCmd(agent, "claude", row, m.gateFor())()
	if done, ok := msg.(launchDoneMsg); !ok || done.err != nil {
		t.Fatalf("msg = %#v; the agent did not see the claim while it ran", msg)
	}
	if claimExists(root) {
		t.Error("the claim outlived the agent")
	}
}

// TestLinkedLaunchWritesNoClaim checks a launch in a linked worktree never
// writes a claim through the gate or the run command: claims are for the root
// only, so a session in a worktree is invisible to other launches.
func TestLinkedLaunchWritesNoClaim(t *testing.T) {
	keepPendingPostExit(t)
	t.Cleanup(releaseRootClaim)
	root := rootRepo(t)
	linked := linkedWorktree(t, root)
	agent := exec.Command("sh", "-c", "test ! -e '"+myClaim(root)+"'")
	agent.Dir = linked
	m := model{agent: "claude", selectedPath: linked}
	if done, ok := runAndWaitCmd(agent, "claude", config.Model{}, m.gateFor())().(launchDoneMsg); !ok || done.err != nil {
		t.Fatal("a claim was written for a linked-worktree launch")
	}
}

// TestNamedRootBranchIsCheckedAndClaimed checks a preset path (the -W root
// branch cmd/wt hands in) is judged by that directory and claimed for the
// agent's run — and is asked about at most once per launch: RunE's
// confirmLaunch already asked, so the gate must never pose its question
// again, however the picture changed since. Without this a held root on a
// flag path is asked twice (the transient Task 205 left behind).
func TestNamedRootBranchIsCheckedAndClaimed(t *testing.T) {
	keepPendingPostExit(t)
	t.Cleanup(releaseRootClaim)
	root := rootRepo(t)
	liveOther(t, root, "codex")
	stderr := answerOnStdio(t, "")
	m := model{agent: "claude", prePath: root, selectedPath: root}
	tg, err := m.launchTarget()
	if err != nil {
		t.Fatal(err)
	}
	if !m.launchGate(&tg) {
		t.Fatal("launchGate said no to a preset path RunE confirmed")
	}
	if got := stderr(); strings.Contains(got, "continue?") || strings.Contains(got, "in use by another session") {
		t.Errorf("gate asked again for a preset path: %q", got)
	}
	agent := exec.Command("sh", "-c", "test -f '"+myClaim(root)+"'")
	agent.Dir = root
	if done, ok := runAndWaitCmd(agent, "claude", config.Model{}, m.gateFor())().(launchDoneMsg); !ok || done.err != nil {
		t.Fatal("the agent did not see the claim while it ran")
	}
	if claimExists(root) {
		t.Error("the claim outlived the agent")
	}
}
