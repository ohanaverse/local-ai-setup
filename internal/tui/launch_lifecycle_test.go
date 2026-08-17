package tui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/agents"
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// TestRunAndWaitCmdBlocksUntilAgentExits asserts that runAndWaitCmd waits
// for the agent command to exit before returning. Without this, the TUI
// would launch the agent then immediately quit, killing the agent via
// process exit.
func TestRunAndWaitCmdBlocksUntilAgentExits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell sleep differs on windows")
	}
	cmd := exec.Command("sleep", "1")
	msg := runAndWaitCmd(cmd)()
	_, ok := msg.(launchDoneMsg)
	if !ok {
		t.Fatalf("msg = %T, want launchDoneMsg", msg)
	}
}

// TestRunAndWaitCmdReturnsErrorOnFailure asserts that a non-zero exit
// surfaces as a non-nil error in launchDoneMsg so the TUI can show it.
func TestRunAndWaitCmdReturnsErrorOnFailure(t *testing.T) {
	falsePath, err := exec.LookPath("false")
	if err != nil {
		t.Skip("`false` not available")
	}
	cmd := exec.Command(falsePath)
	msg := runAndWaitCmd(cmd)()
	done := msg.(launchDoneMsg)
	if done.err == nil {
		t.Fatal("expected error from 'false'")
	}
	var exitErr *exec.ExitError
	if !errors.As(done.err, &exitErr) {
		t.Errorf("err = %T, want *exec.ExitError", done.err)
	}
}

// TestEnterInModelPhaseDoesNotQuitImmediately asserts the lifecycle bug:
// pressing Enter in the model phase should NOT immediately quit the TUI.
// The previous design returned tea.Batch(tea.Quit, runAndWaitCmd(cmd)).
// tea.Quit fires immediately in a concurrent goroutine and the Bubble Tea
// program's Run() returns on QuitMsg — even though runAndWaitCmd is still
// in cmd.Run() running the agent. When Run() returns, the wt process exits
// and the agent is killed via SIGKILL.
//
// The fix: return only runAndWaitCmd(cmd) from the Enter handler. The
// launchDoneMsg returns when the agent exits; Update(launchDoneMsg) then
// issues tea.Quit. This keeps wt alive while the agent has the terminal.
//
// This test exercises runAndWaitCmd directly (the same function the Enter
// handler returns), with a stub agent command that takes ~300ms. The test
// fails if runAndWaitCmd returns launchDoneMsg before the agent finishes.
func TestEnterInModelPhaseDoesNotQuitImmediately(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell sleep differs on windows")
	}

	tmp := t.TempDir()
	marker := filepath.Join(tmp, "ran.flag")
	agentCmd := exec.Command("sh", "-c", "sleep 0.3 && touch "+shQuote(marker))

	// Drive the same code path the Enter handler uses.
	returnedCmd := runAndWaitCmd(agentCmd)
	if returnedCmd == nil {
		t.Fatal("runAndWaitCmd returned nil")
	}

	// The cmd must NOT have run yet — invoking it must block on the agent.
	// We can't trivially check "non-blocking" without timing, but we CAN
	// check that calling the cmd synchronously returns launchDoneMsg only
	// after the agent finishes, by verifying the marker exists after we
	// receive the message.
	doneMsg := returnedCmd()
	done, ok := doneMsg.(launchDoneMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want launchDoneMsg", doneMsg)
	}
	if done.err != nil {
		t.Fatalf("agent exited with error: %v", done.err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("agent did not run to completion: marker file missing: %v", err)
	}

	// Now drive the Enter handler and verify it returns ONLY runAndWaitCmd,
	// not a batched Quit. Use a model whose agent is unknown so launchAgent
	// errors out — we don't need a real agent here, just to assert the cmd
	// shape returned by the Enter handler.
	m := model{cfg: testConfig(), phase: phaseModel, agent: "not-a-real-agent", tag: "code",
		selectedPath: tmp, current: config.Model{ID: "ollama/gemma4:9b"}}
	res, enteredCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	// launchAgent errors for unknown agents, so Update returns nil cmd and
	// sets status on the returned model. Verify that:
	if enteredCmd != nil {
		t.Fatalf("expected nil cmd on launch failure, got %T", enteredCmd)
	}
	gotModel := res.(model)
	if gotModel.status == "" {
		t.Fatal("expected error status to be set on returned model")
	}
}

// TestEnterInModelPhaseReturnsRunAndWaitNotBatchedQuit asserts the fix:
// pressing Enter in the model phase must return ONLY runAndWaitCmd(cmd),
// not a tea.Batch that includes tea.Quit. If tea.Quit is in the batch,
// Bubble Tea fires QuitMsg in a concurrent goroutine and Program.Run()
// returns immediately — killing the agent via process exit.
//
// We register a stub driver via the agents package's exported RegisterTest
// helper. The stub driver builds a no-op `true` command. The returned cmd
// must NOT be a BatchMsg when the Enter handler is invoked.
func TestEnterInModelPhaseReturnsRunAndWaitNotBatchedQuit(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}
	cleanup := agents.RegisterTest("wt-stub", func() agents.Driver { return stubDriver{path: truePath} })
	t.Cleanup(cleanup)

	m := model{cfg: testConfig(), phase: phaseModel, agent: "wt-stub", tag: "code",
		selectedPath: t.TempDir(), current: config.Model{ID: "ollama/gemma4:9b"}}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if cmd == nil {
		t.Fatal("Enter returned nil cmd on success path")
	}
	msg := cmd()
	if msg == nil {
		t.Fatal("cmd invocation returned nil")
	}
	if _, ok := msg.(tea.BatchMsg); ok {
		t.Fatal("Enter returned a tea.Batch — Quit-before-agent-exit bug is back")
	}
	if _, ok := msg.(launchDoneMsg); !ok {
		t.Fatalf("cmd returned %T, want launchDoneMsg", msg)
	}
}

// shQuote quotes a path for inclusion in a single-quoted shell string.
func shQuote(s string) string {
	return "'" + s + "'"
}

// stubDriver is a minimal Driver used by lifecycle tests. It builds a
// LaunchCmd that runs the given path with no args, so tests can exercise
// the launch wiring without invoking a real agent.
type stubDriver struct {
	path string
}

func (stubDriver) YoloFlag() string { return "" }
func (d stubDriver) Build(_ config.Model, _ bool) agents.LaunchCmd {
	return agents.LaunchCmd{Bin: d.path}
}
