package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// TestLaunchGateRootNoOthersWarnsAndClaims checks the root's routine case is
// the one warned default-yes confirmation R1 rules: picking the root row is
// not consent (only the --root flag is), so a root nobody else holds still
// carries the root warning into the ONE question — named there, so the person
// reads why — and a yes claims the root for the agent's run. It matters
// because the root row's warning must never be silently absorbed: sharing the
// repository root is exactly what other sessions need to know about.
func TestLaunchGateRootNoOthersWarnsAndClaims(t *testing.T) {
	t.Cleanup(releaseRootClaim)
	root := rootRepo(t)
	stderr := answerOnStdio(t, "\n")
	m := model{agent: "claude", selectedPath: root}
	tg, err := m.launchTarget()
	if err != nil {
		t.Fatal(err)
	}
	if ok, gerr := m.launchGate(&tg, true); !ok || gerr != nil {
		t.Fatalf("launchGate = (%v, %v); a default-yes Enter continues", ok, gerr)
	}
	got := stderr()
	if strings.Count(got, "wt: continue? [Y/n]: ") != 1 {
		t.Errorf("gate asked %d times, want the one confirmation: %q", strings.Count(got, "wt: continue? [Y/n]: "), got)
	}
	if !strings.Contains(got, tg.Dir+" is the repository root") {
		t.Errorf("gate did not name the root warning in the confirm: %q", got)
	}
	if !claimExists(root) {
		t.Error("the root was not claimed for the agent's run")
	}
}

// TestLaunchGateLinkedWorktreePrintsInfoAndClaimsNothing checks R1b's rule
// for the ordinary pick: every pick with a directory states where it launches
// (the one informational line) and, when nothing warns, asks nothing and
// claims nothing — even while another session holds the root. The line is
// the pick's acknowledgement, cmd/wt confirmLaunch's parity; claims stay
// root-only.
func TestLaunchGateLinkedWorktreePrintsInfoAndClaimsNothing(t *testing.T) {
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
	if ok, gerr := m.launchGate(&tg, true); !ok || gerr != nil {
		t.Fatalf("launchGate = (%v, %v); a linked worktree proceeds", ok, gerr)
	}
	if got, want := stderr(), "wt: launching in "+tg.Dir+"\n"; got != want {
		t.Errorf("gate printed %q; want the one informational line %q and no question", got, want)
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
	if ok, gerr := m.launchGate(&tg, true); !ok || gerr != nil {
		t.Fatalf("launchGate = (%v, %v); want go after a yes", ok, gerr)
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
	if ok, gerr := m.launchGate(&tg, true); ok || gerr != nil {
		t.Fatalf("launchGate = (%v, %v); want a quiet no", ok, gerr)
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
	// The root warning asks now (R1); the newline answers the default yes.
	stderr := answerOnStdio(t, "\n")
	m := model{agent: "claude", selectedPath: root}
	tg, err := m.launchTarget()
	if err != nil {
		t.Fatal(err)
	}
	if ok, gerr := m.launchGate(&tg, true); !ok || gerr != nil {
		t.Fatalf("launchGate = (%v, %v); a claim failure must not stop the launch", ok, gerr)
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
			if ok, gerr := m.launchGate(&tg, true); ok || gerr != nil {
				t.Fatalf("launchGate = (%v, %v); want a quiet no", ok, gerr)
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
			if ok, gerr := m.launchGate(&tg, true); !ok || gerr != nil {
				t.Fatalf("launchGate = (%v, %v); want go after a yes", ok, gerr)
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
	// Redirected stdin is no TTY: the gate warns and proceeds without
	// reading it, which is what a non-interactive launch must do.
	answerOnStdio(t, "")
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
	answerOnStdio(t, "")
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
	answerOnStdio(t, "")
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
	if ok, gerr := m.launchGate(&tg, true); !ok || gerr != nil {
		t.Fatalf("launchGate = (%v, %v); a preset path RunE confirmed must proceed", ok, gerr)
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

// TestLaunchGateNonTTYWarnsAndProceedsWithoutReading pins the no-TTY
// contract at the gate: every warning prints and the launch proceeds, and
// stdin is never read — a redirected "n" sitting in stdin must not decline
// anything. Without this a piped or scripted launch either blocks on a
// question nobody can see or silently obeys bytes meant for the agent.
func TestLaunchGateNonTTYWarnsAndProceedsWithoutReading(t *testing.T) {
	t.Cleanup(releaseRootClaim)
	root := rootRepo(t)
	liveOther(t, root, "codex")
	stderr := answerOnStdio(t, "n\n")
	m := model{agent: "claude", selectedPath: root}
	tg, err := m.launchTarget()
	if err != nil {
		t.Fatal(err)
	}
	if ok, gerr := m.launchGate(&tg, false); !ok || gerr != nil {
		t.Fatalf("launchGate = (%v, %v); no TTY means print the warnings and proceed", ok, gerr)
	}
	got := stderr()
	if strings.Contains(got, "continue?") {
		t.Errorf("gate asked a question with no TTY: %q", got)
	}
	if !strings.Contains(got, "is the repository root") || !strings.Contains(got, "in use by another session") {
		t.Errorf("gate printed %q; want every warning named", got)
	}
	if !claimExists(root) {
		t.Error("the launch did not proceed (no claim written)")
	}
}

// TestEnsureFailureAfterAContinueIsAnErrorNotAQuietDecline checks a creation
// that fails AFTER the person continued is a failure, not a choice: the error
// travels out of the gate as the run's error (so wt prints it once at main and
// exits non-zero, exactly as cmd/wt's -W path does through refuse) instead of
// sharing the decline's quiet exit 0. A quiet exit here hid every git failure
// at the commit point.
func TestEnsureFailureAfterAContinueIsAnErrorNotAQuietDecline(t *testing.T) {
	keepPendingPostExit(t)
	profileApplier = nil
	t.Cleanup(releaseRootClaim)
	dir := t.TempDir()
	gitInit(t, dir)
	plan, err := worktree.Resolve(dir, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	// A plain file where the worktree must go makes git's `worktree add`
	// fail at the commit point, after the confirm passed.
	if err := os.MkdirAll(filepath.Dir(plan.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan.Path(), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr := answerOnStdio(t, "")
	m := model{agent: "claude", creation: &launchCreation{repoRoot: dir, name: "fresh", plan: plan}}
	msg := runAndWaitCmd(exec.Command("true"), "claude", config.Model{ID: "prov/m"}, m.gateFor())()
	done, ok := msg.(launchDoneMsg)
	if !ok || done.err == nil {
		t.Fatalf("msg = %#v; want the creation error carried out, not a quiet decline", msg)
	}
	if got := stderr(); strings.Contains(got, done.err.Error()) {
		t.Errorf("gate printed the error (%q); main prints it once, never here", got)
	}
	mm, _ := updateMsg(model{cfg: testConfig()}, msg)
	if err := runResult(mm, nil); err == nil {
		t.Error("runResult = nil; a creation failure after a continue must end non-zero")
	}
}

// TestDeclinedGateKeepsTheStopTail checks a gate decline still runs the
// standard post-exit tail (ruling: the stop picker, nothing auto-stops): the
// person declined the launch, but a model the start flow already loaded and
// the routes it wrote stay, and the tail is what offers to stop them. Before
// this a decline left a loaded model with no stop picker at all.
func TestDeclinedGateKeepsTheStopTail(t *testing.T) {
	keepPendingPostExit(t)
	profileApplier = nil
	msg := runAndWaitCmd(exec.Command("true"), "claude", config.Model{ID: "prov/m"}, func() (bool, error) { return false, nil })()
	if done, ok := msg.(launchDoneMsg); !ok || done.err != nil {
		t.Fatalf("msg = %#v; a decline is a choice: quiet, exit 0", msg)
	}
	if pendingSurveyState.agent != "claude" || pendingSurveyState.m.ID != "prov/m" {
		t.Errorf("pendingSurveyState = %+v; the decline left no stop-tail state", pendingSurveyState)
	}
}

// TestDeclinedSlotGateKeepsTheStopTail is the same for a slot-profile
// decline, whose run command has its own decline path and must keep the
// profile tail (stop picker, no survey) just as a real exit does.
func TestDeclinedSlotGateKeepsTheStopTail(t *testing.T) {
	keepPendingPostExit(t)
	stubSlotApplier(t, func(*exec.Cmd, string, catalog.Row) (func() error, error) { return nil, nil })
	row := catalog.Row{Model: config.Model{ID: "profile/cloud"}, Profile: &catalog.ProfileRow{}}
	msg := runSlotAndWaitCmd(exec.Command("true"), "claude", row, func() (bool, error) { return false, nil })()
	if done, ok := msg.(launchDoneMsg); !ok || done.err != nil {
		t.Fatalf("msg = %#v; a decline is a choice: quiet, exit 0", msg)
	}
	if !pendingSurveyState.profile || pendingSurveyState.agent != "claude" {
		t.Errorf("pendingSurveyState = %+v; the slot decline left no stop-tail state", pendingSurveyState)
	}
}

// TestPickRowTargetCarriesPRAndUpstream checks a pick from the unified list
// reaches the launch gate as that row's target: the PR state and number, the
// folded/gone upstream and the raw dirt — facts only the list gathered — ride
// into the Target the gate asks about and so into its warnings. The dirt
// assertion pins that the row's raw losses reach the gate's Target.Dirty and
// win over any probe (the worktree itself is clean, so a probed answer would
// be false). Without it a row's merged PR and dead upstream never warn at
// launch, and its uncommitted work is judged by a probe the row's facts
// should decide.
func TestPickRowTargetCarriesPRAndUpstream(t *testing.T) {
	t.Cleanup(releaseRootClaim)
	stubShellLaunch(t)
	root := rootRepo(t)
	linked := linkedWorktree(t, root)
	row := cleanup.Row{
		Kind:     cleanup.KindWorktree,
		Name:     "feature",
		Branch:   "feature",
		Path:     linked,
		Ref:      "refs/heads/feature",
		PRTag:    &cleanup.PRInfo{Number: 42, State: "MERGED"},
		Upstream: &cleanup.Upstream{Ref: "refs/remotes/origin/feature", Remote: "origin", Name: "feature", Gone: true},
		Losses:   cleanup.Losses{Dirty: true},
	}
	m := model{cfg: &config.Config{}, phase: phaseList, width: 80, height: 24, initialAgent: "opencode"}
	m, cmd := updateMsg(m, rowChosenMsg{Row: row}) // the row list's Enter
	if cmd == nil {
		t.Fatal("enter on a picker row emitted no selection")
	}
	m, _ = updateMsg(m, cmd()) // selectedEntryMsg binds the row to the launch

	tg, err := m.launchTarget()
	if err != nil {
		t.Fatal(err)
	}
	if tg.PRState != "merged" || tg.PRNumber != 42 || !tg.UpstreamGone {
		t.Errorf("target = %+v; want the row's merged PR #42 and its gone upstream", tg)
	}
	if !tg.Dirty {
		t.Errorf("target = %+v; want Dirty from the row's raw losses (the worktree is clean, so a probe would say false and must lose)", tg)
	}
	stderr := answerOnStdio(t, "y")
	if ok, gerr := m.launchGate(&tg, true); !ok || gerr != nil {
		t.Fatalf("launchGate = (%v, %v); want go after a yes", ok, gerr)
	}
	got := stderr()
	if !strings.Contains(got, "PR #42 is merged") || !strings.Contains(got, "its upstream branch is gone") {
		t.Errorf("gate printed %q; want the row's PR and upstream warnings", got)
	}
}

// TestPickRemoteRowCarriesPRIntoTheCreationTarget checks a remote-only row's
// pick keeps the plan's creation at the gate AND carries the row's PR facts
// with it: the launch creates the tracking worktree exactly as the pick
// planned (one creation path, its frozen wording), and a merged PR still
// warns before it does.
func TestPickRemoteRowCarriesPRIntoTheCreationTarget(t *testing.T) {
	t.Cleanup(releaseRootClaim)
	stubShellLaunch(t)
	root := rootRepo(t)
	if out, err := exec.Command("git", "-C", root, "remote", "add", "origin", "https://example.invalid/origin.git").CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", root, "update-ref", "refs/remotes/origin/qa", "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("git update-ref: %v\n%s", err, out)
	}
	row := cleanup.Row{
		Kind:   cleanup.KindRemote,
		Name:   "qa",
		Branch: "qa",
		Remote: "origin",
		Ref:    "refs/remotes/origin/qa",
		PRTag:  &cleanup.PRInfo{Number: 5, State: "MERGED"},
	}
	m := model{cfg: &config.Config{}, phase: phaseList, width: 80, height: 24, initialAgent: "opencode", repoRoot: root}
	m, cmd := updateMsg(m, rowChosenMsg{Row: row}) // the row list's Enter
	if cmd == nil {
		t.Fatal("enter on a picker row emitted no selection")
	}
	m, _ = updateMsg(m, cmd()) // selectedEntryMsg binds the row to the launch

	tg, err := m.launchTarget()
	if err != nil {
		t.Fatal(err)
	}
	if tg.Creation == nil || tg.Creation.Branch != "qa" || tg.Creation.Tracks != "origin/qa" {
		t.Fatalf("creation = %+v; want the pick's planned tracking worktree", tg.Creation)
	}
	if tg.PRState != "merged" || tg.PRNumber != 5 {
		t.Errorf("target = %+v; want the row's merged PR #5 carried in", tg)
	}
	stderr := answerOnStdio(t, "y")
	if ok, gerr := m.launchGate(&tg, true); !ok || gerr != nil {
		t.Fatalf("launchGate = (%v, %v); want go after a yes", ok, gerr)
	}
	got := stderr()
	if !strings.Contains(got, "creating worktree "+tg.Dir+" for branch qa tracking origin/qa") {
		t.Errorf("gate printed %q; want the planned creation line", got)
	}
	if !strings.Contains(got, "PR #5 is merged") {
		t.Errorf("gate printed %q; want the row's PR warning", got)
	}
	if _, err := os.Stat(tg.Dir); err != nil {
		t.Errorf("worktree %s not created after a yes: %v", tg.Dir, err)
	}
}
