package tui

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// rootRepo makes a repository with one commit on main and returns its root
// with symlinks resolved, the spelling claims are written with.
func rootRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", dir},
		{"-C", dir, "config", "user.email", "test@test"},
		{"-C", dir, "config", "user.name", "Test"},
		{"-C", dir, "symbolic-ref", "HEAD", "refs/heads/main"},
		{"-C", dir, "commit", "--allow-empty", "-q", "-m", "init"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// linkedWorktree adds a linked worktree of root on a new branch and returns
// its path.
func linkedWorktree(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, ".worktrees", "feature")
	if out, err := exec.Command("git", "-C", root, "worktree", "add", "-q", "-b", "feature", path).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	return path
}

// liveOther writes a claim on root for a real, running process (a sleep
// child killed on cleanup), so the liveness check sees a live session.
func liveOther(t *testing.T, root, agent string) int {
	t.Helper()
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Unix()
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	pid := child.Process.Pid
	dir := filepath.Join(root, ".git", "wt-sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("pid=%d\nstarted=%d\npath=%s\nbranch=main\nagent=%s\n", pid, started, root, agent)
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return pid
}

// myClaim is the path of this process's claim file on root.
func myClaim(root string) string {
	return filepath.Join(root, ".git", "wt-sessions", strconv.Itoa(os.Getpid()))
}

func claimExists(root string) bool { _, err := os.Stat(myClaim(root)); return err == nil }

// setRootCheck sets cmd/wt's word on the check for one test.
func setRootCheck(t *testing.T, rc RootCheck) {
	t.Helper()
	old := SharedRoot
	SharedRoot = rc
	t.Cleanup(func() { SharedRoot = old })
}

// stubShellLaunch swaps the shell launch for one that records the directory
// it was built for and runs nothing, so a test sees whether, and where, the
// command launch happened.
func stubShellLaunch(t *testing.T) *[]string {
	t.Helper()
	var dirs []string
	old := buildPassthrough
	buildPassthrough = func(agent, worktreePath string, yolo bool, extraArgs []string) (*exec.Cmd, error) {
		dirs = append(dirs, worktreePath)
		return exec.Command("true"), nil
	}
	t.Cleanup(func() { buildPassthrough = old })
	t.Cleanup(stubInstalled("opencode"))
	return &dirs
}

// pickRow is the picker after Enter on a row at path, with an installed but
// unconfigured agent pinned so a launch is one buildPassthrough call.
func pickRow(t *testing.T, path string) (model, tea.Cmd, *[]string) {
	t.Helper()
	launched := stubShellLaunch(t)
	m := model{cfg: &config.Config{}, phase: phaseList, width: 80, height: 24, initialAgent: "opencode"}
	m, cmd := updateMsg(m, selectedEntryMsg{entry: worktree.Entry{Type: worktree.TypeCurrent, Branch: "main", Path: path}})
	return m, cmd, launched
}

// TestPickerRootRowWithLiveOtherShowsScreen checks Enter on the root row while
// another live session holds the root shows the shared-root screen naming that
// session, launches nothing and claims nothing until the person chooses.
func TestPickerRootRowWithLiveOtherShowsScreen(t *testing.T) {
	root := rootRepo(t)
	pid := liveOther(t, root, "codex")
	m, cmd, launched := pickRow(t, root)
	if m.phase != phaseSharedRoot {
		t.Fatalf("phase = %v; want the shared-root screen", m.phase)
	}
	if cmd != nil || len(*launched) != 0 {
		t.Fatalf("launched %v (cmd %v) before the person chose", *launched, cmd != nil)
	}
	view := m.View()
	// The screen wraps at the terminal's width; a long root path may be
	// broken across lines.
	if flat := strings.Join(strings.Fields(view), ""); !strings.Contains(flat, root) {
		t.Errorf("screen %q does not show the root %s", view, root)
	}
	for _, want := range []string{"codex", "pid " + strconv.Itoa(pid), "running", "started on main", "continue here", "new worktree", "quit (Esc)"} {
		if !strings.Contains(view, want) {
			t.Errorf("screen %q does not show %q", view, want)
		}
	}
	if claimExists(root) {
		t.Error("a claim was written before the person chose")
	}
}

// TestPickerLinkedWorktreeRowSkipsScreen checks picking a linked worktree is
// deliberate: no screen even while the root is held, and the launch goes
// ahead there.
func TestPickerLinkedWorktreeRowSkipsScreen(t *testing.T) {
	root := rootRepo(t)
	linked := linkedWorktree(t, root)
	liveOther(t, root, "codex")
	m, cmd, launched := pickRow(t, linked)
	if m.phase == phaseSharedRoot {
		t.Fatal("the shared-root screen was shown for a linked worktree")
	}
	if cmd == nil || len(*launched) != 1 || (*launched)[0] != linked {
		t.Fatalf("launched %v; want one launch in %s", *launched, linked)
	}
}

// TestPickerRootRowNoOthersLaunchesDirectly checks the common case costs
// nothing: a root nobody else holds launches with no screen.
func TestPickerRootRowNoOthersLaunchesDirectly(t *testing.T) {
	root := rootRepo(t)
	m, cmd, launched := pickRow(t, root)
	if m.phase == phaseSharedRoot {
		t.Fatal("the shared-root screen was shown with no other session")
	}
	if cmd == nil || len(*launched) != 1 || (*launched)[0] != root {
		t.Fatalf("launched %v; want one launch in %s", *launched, root)
	}
}

// TestSharedRootKeys checks the screen's keys: c launches in the root, w
// creates a new worktree (never the default branch) and launches there, q and
// Esc quit without launching. Any other key does nothing.
func TestSharedRootKeys(t *testing.T) {
	keys := map[string]tea.KeyMsg{
		"c":   {Type: tea.KeyRunes, Runes: []rune{'c'}},
		"w":   {Type: tea.KeyRunes, Runes: []rune{'w'}},
		"q":   {Type: tea.KeyRunes, Runes: []rune{'q'}},
		"esc": {Type: tea.KeyEsc},
		"x":   {Type: tea.KeyRunes, Runes: []rune{'x'}},
	}
	screen := func(t *testing.T) (string, model, *[]string) {
		root := rootRepo(t)
		liveOther(t, root, "codex")
		m, _, launched := pickRow(t, root)
		if m.phase != phaseSharedRoot {
			t.Fatalf("phase = %v; want the shared-root screen", m.phase)
		}
		return root, m, launched
	}

	t.Run("c", func(t *testing.T) {
		root, m, launched := screen(t)
		m, cmd := updateMsg(m, keys["c"])
		if cmd == nil || len(*launched) != 1 || (*launched)[0] != root {
			t.Fatalf("launched %v; want one launch in the root %s", *launched, root)
		}
		if m.phase == phaseSharedRoot || m.sharedRoot != nil {
			t.Error("the screen stayed up after continue")
		}
	})
	t.Run("w", func(t *testing.T) {
		root, m, launched := screen(t)
		m, cmd := updateMsg(m, keys["w"])
		if cmd == nil || len(*launched) != 0 {
			t.Fatalf("w launched %v at once; want the worktree created first", *launched)
		}
		if !strings.Contains(m.View(), "creating") {
			t.Errorf("screen %q does not say the worktree is being created", m.View())
		}
		m, cmd = updateMsg(m, cmd())
		if cmd == nil || len(*launched) != 1 {
			t.Fatalf("launched %v; want one launch in the new worktree", *launched)
		}
		dir := (*launched)[0]
		if !strings.HasPrefix(dir, filepath.Join(root, ".worktrees")+string(filepath.Separator)) {
			t.Fatalf("launched in %q; want a new worktree under %s/.worktrees/", dir, root)
		}
		out, err := exec.Command("git", "-C", dir, "branch", "--show-current").Output()
		if br := strings.TrimSpace(string(out)); err != nil || br == "" || br == "main" {
			t.Errorf("new worktree is on %q (%v); want a new branch, never the default", br, err)
		}
		if m.selectedPath != dir || m.phase == phaseSharedRoot {
			t.Errorf("selectedPath=%q phase=%v; want the new worktree, screen down", m.selectedPath, m.phase)
		}
	})
	for _, k := range []string{"q", "esc"} {
		t.Run(k, func(t *testing.T) {
			root, m, launched := screen(t)
			_, cmd := updateMsg(m, keys[k])
			if cmd == nil {
				t.Fatal("no command; want quit")
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Fatal("the command is not tea.Quit")
			}
			if len(*launched) != 0 || claimExists(root) {
				t.Errorf("launched %v, claim %v; want neither", *launched, claimExists(root))
			}
		})
	}
	t.Run("other key", func(t *testing.T) {
		_, m, launched := screen(t)
		m, cmd := updateMsg(m, keys["x"])
		if cmd != nil || len(*launched) != 0 || m.phase != phaseSharedRoot {
			t.Errorf("x did something: cmd %v, launched %v, phase %v", cmd != nil, *launched, m.phase)
		}
	})
}

// TestSharedRootScreenPrecedesEveryLaunch checks every kind of launch the TUI
// makes meets the screen first when it resolves to a held root, and never in a
// linked worktree: a command (shell), an unconfigured passthrough agent, a
// model agent (whose model picker holds the model and slot-profile launches),
// the unpinned agent picker, and a root handed in as the preset path (--cwd
// through the model picker).
func TestSharedRootScreenPrecedesEveryLaunch(t *testing.T) {
	stubUsageStore(t)
	stubRefcountStore(t)
	t.Cleanup(stubInstalled("claude", "opencode"))
	old := buildPassthrough
	buildPassthrough = func(string, string, bool, []string) (*exec.Cmd, error) { return exec.Command("true"), nil }
	t.Cleanup(func() { buildPassthrough = old })

	enter := func(m model, path string) (model, tea.Cmd) {
		return updateMsg(m, selectedEntryMsg{entry: worktree.Entry{Path: path}})
	}
	preset := func(m model, path string) (model, tea.Cmd) {
		m.prePath = path
		return updateMsg(m, m.Init()())
	}
	// after is the phase continue lands on: the launch's own next screen, or
	// -1 for a launch that runs at once.
	cases := []struct {
		name  string
		agent string
		reach func(model, string) (model, tea.Cmd)
		after phase
	}{
		{"command", "shell", enter, -1},
		{"passthrough", "opencode", enter, -1},
		{"model and slot", "claude", enter, phaseModel},
		{"agent picker", "", enter, phaseAgent},
		{"preset root", "claude", preset, phaseModel},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := rootRepo(t)
			linked := linkedWorktree(t, root)
			liveOther(t, root, "codex")
			base := model{cfg: testConfig(), width: 80, height: 24, initialAgent: c.agent}

			m, cmd := c.reach(base, root)
			if m.phase != phaseSharedRoot || cmd != nil {
				t.Fatalf("root: phase %v, cmd %v; want the screen and nothing started", m.phase, cmd != nil)
			}
			m, cmd = updateMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
			if c.after >= 0 && m.phase != c.after {
				t.Errorf("root, continue: phase %v; want %v", m.phase, c.after)
			}
			if c.after < 0 && cmd == nil {
				t.Errorf("root, continue: nothing launched (status %q)", m.status)
			}
			m, cmd = c.reach(base, linked)
			if c.after >= 0 && m.phase != c.after {
				t.Errorf("linked worktree: phase %v; want %v with no screen", m.phase, c.after)
			}
			if c.after < 0 && cmd == nil {
				t.Errorf("linked worktree: nothing launched (status %q)", m.status)
			}
		})
	}
}

// TestRootLaunchClaimsUntilTheAgentExits checks the run command claims the
// root for exactly the agent's run: the agent sees this session's claim, and
// it is gone once the agent exits.
func TestRootLaunchClaimsUntilTheAgentExits(t *testing.T) {
	keepPendingPostExit(t)
	root := rootRepo(t)
	agent := exec.Command("sh", "-c", "grep -q '^agent=claude$' '"+myClaim(root)+"'")
	agent.Dir = root
	msg := runAndWaitCmd(agent, "claude", config.Model{})()
	if done, ok := msg.(launchDoneMsg); !ok || done.err != nil {
		t.Fatalf("msg = %#v; the agent did not see the claim while it ran", msg)
	}
	if claimExists(root) {
		t.Error("the claim outlived the agent")
	}
}

// TestSlotRootLaunchClaimsUntilTheAgentExits is the same for a slot-profile
// launch, which has its own run command.
func TestSlotRootLaunchClaimsUntilTheAgentExits(t *testing.T) {
	keepPendingPostExit(t)
	stubSlotApplier(t, func(*exec.Cmd, string, catalog.Row) (func() error, error) { return nil, nil })
	root := rootRepo(t)
	agent := exec.Command("sh", "-c", "test -f '"+myClaim(root)+"'")
	agent.Dir = root
	row := catalog.Row{Model: config.Model{ID: "profile/cloud"}, Profile: &catalog.ProfileRow{}}
	msg := runSlotAndWaitCmd(agent, "claude", row)()
	if done, ok := msg.(launchDoneMsg); !ok || done.err != nil {
		t.Fatalf("msg = %#v; the agent did not see the claim while it ran", msg)
	}
	if claimExists(root) {
		t.Error("the claim outlived the agent")
	}
}

// TestLinkedLaunchWritesNoClaim checks a launch in a linked worktree never
// writes a claim: claims are for the root only.
func TestLinkedLaunchWritesNoClaim(t *testing.T) {
	keepPendingPostExit(t)
	root := rootRepo(t)
	linked := linkedWorktree(t, root)
	agent := exec.Command("sh", "-c", "test ! -e '"+myClaim(root)+"'")
	agent.Dir = linked
	if done, ok := runAndWaitCmd(agent, "claude", config.Model{})().(launchDoneMsg); !ok || done.err != nil {
		t.Fatal("a claim was written for a linked-worktree launch")
	}
}

// TestNamedRootBranchIsCheckedAndClaimed checks a -W that resolves to the
// root (the preset path cmd/wt hands in for -W <root's branch>) is judged by
// that directory: the screen while another session holds it, and a claim for
// the agent's run, as for --cwd.
func TestNamedRootBranchIsCheckedAndClaimed(t *testing.T) {
	keepPendingPostExit(t)
	t.Cleanup(stubInstalled("claude"))
	root := rootRepo(t)
	liveOther(t, root, "codex")
	m := model{cfg: testConfig(), width: 80, height: 24, initialAgent: "claude", prePath: root}
	m, cmd := updateMsg(m, m.Init()())
	if m.phase != phaseSharedRoot || cmd != nil {
		t.Fatalf("phase %v, cmd %v; want the screen and nothing started", m.phase, cmd != nil)
	}
	agent := exec.Command("sh", "-c", "test -f '"+myClaim(root)+"'")
	agent.Dir = root
	if done, ok := runAndWaitCmd(agent, "claude", config.Model{})().(launchDoneMsg); !ok || done.err != nil {
		t.Fatal("the agent did not see the claim while it ran")
	}
	if claimExists(root) {
		t.Error("the claim outlived the agent")
	}
}

// TestAllowSharedRootSkipsScreenAndClaims checks --allow-shared-root
// (RootCheck.Allow) launches in a held root with no screen, and still claims
// it for the agent's run.
func TestAllowSharedRootSkipsScreenAndClaims(t *testing.T) {
	keepPendingPostExit(t)
	setRootCheck(t, RootCheck{Allow: true})
	root := rootRepo(t)
	liveOther(t, root, "codex")
	m, cmd, launched := pickRow(t, root)
	if m.phase == phaseSharedRoot || cmd == nil || len(*launched) != 1 {
		t.Fatal("--allow-shared-root met the shared-root screen")
	}
	agent := exec.Command("sh", "-c", "test -f '"+myClaim(root)+"'")
	agent.Dir = root
	if done, ok := runAndWaitCmd(agent, "claude", config.Model{})().(launchDoneMsg); !ok || done.err != nil {
		t.Fatal("the agent did not see the claim while it ran")
	}
}

// TestClaimFailureIsANoteNotARefusal checks an unwritable wt-sessions
// directory never blocks the launch: one note, and the agent runs.
func TestClaimFailureIsANoteNotARefusal(t *testing.T) {
	root := rootRepo(t)
	sessions := filepath.Join(root, ".git", "wt-sessions")
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
	m, cmd, _ := pickRow(t, root)
	if m.phase == phaseSharedRoot || cmd == nil {
		t.Fatal("an unwritable claims directory stopped the launch")
	}
	var out bytes.Buffer
	release := holdRoot(root, "claude", &out)
	release()
	if s := out.String(); !strings.Contains(s, "note: could not check other sessions") || strings.Count(s, "\n") != 1 {
		t.Errorf("output %q; want a one-line note", s)
	}
}
