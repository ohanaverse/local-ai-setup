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
	"github.com/ohanaverse/agent-worktree/internal/worktree"
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
		selectedPath: tmp, models: singleModelList(config.Model{ID: "ollama/gemma4:9b"})}
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
		selectedPath: t.TempDir(), models: singleModelList(config.Model{ID: "ollama/gemma4:9b"})}
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

// TestPhaseAgentEnterLaunchesPickedCommand asserts that pressing Enter on a
// command row in the agent+command picker launches THAT driver, not a
// hardcoded "shell". PR 2's launch site called launchShell() which passed
// "shell" regardless of the picked item, so a second command driver would
// have silently launched the shell instead. The stub driver writes its own
// name to a marker file so the test can see which driver actually ran.
func TestPhaseAgentEnterLaunchesPickedCommand(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched.txt")
	cleanup := agents.RegisterTest("wt-cmd-stub", func() agents.Driver {
		return commandStubDriver{name: "wt-cmd-stub", marker: marker}
	})
	t.Cleanup(cleanup)

	cfg := &config.Config{
		DefaultTag: "code",
		Agents:     []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
	}
	m := model{cfg: cfg, width: 80, height: 24}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature", Path: t.TempDir()}})
	m = got.(model)
	if m.phase != phaseAgent {
		t.Fatalf("phase = %v, want phaseAgent", m.phase)
	}
	// Select the command row (not claude) and press Enter.
	for i, it := range m.agentList.Items() {
		if ai, ok := it.(agentItem); ok && ai.command && ai.name == "wt-cmd-stub" {
			m.agentList.Select(i)
			break
		}
	}
	got, entered := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel := got.(model)
	if gotModel.phase != phaseAgent {
		t.Errorf("phase = %v, want phaseAgent (command launch has no model phase)", gotModel.phase)
	}
	if entered == nil {
		t.Fatal("Enter returned nil cmd for command row")
	}
	if _, ok := entered().(launchDoneMsg); !ok {
		t.Fatalf("Enter cmd did not produce launchDoneMsg")
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if string(data) != "wt-cmd-stub" {
		t.Errorf("launched name = %q, want wt-cmd-stub (launch site hardcoded shell?)", string(data))
	}
}

// TestPhaseAgentEnterIgnoresYoloForCommands asserts that command launches do
// not inherit the TUI's yolo state. Commands are documented to skip yolo; if
// this regresses, a future command driver that honors yolo would unexpectedly
// change behavior based on unrelated UI state.
func TestPhaseAgentEnterIgnoresYoloForCommands(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "yolo.txt")
	cleanup := agents.RegisterTest("wt-cmd-yolo", func() agents.Driver {
		return yoloCommandStubDriver{marker: marker}
	})
	t.Cleanup(cleanup)

	cfg := &config.Config{
		DefaultTag: "code",
		Agents:     []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
	}
	m := model{cfg: cfg, width: 80, height: 24, yolo: true}
	if !m.yolo {
		t.Fatal("test precondition failed: model yolo must be true")
	}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature", Path: t.TempDir()}})
	m = got.(model)
	if m.phase != phaseAgent {
		t.Fatalf("phase = %v, want phaseAgent", m.phase)
	}
	for i, it := range m.agentList.Items() {
		if ai, ok := it.(agentItem); ok && ai.command && ai.name == "wt-cmd-yolo" {
			m.agentList.Select(i)
			break
		}
	}
	_, entered := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if entered == nil {
		t.Fatal("Enter returned nil cmd for command row")
	}
	if _, ok := entered().(launchDoneMsg); !ok {
		t.Fatalf("Enter cmd did not produce launchDoneMsg")
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if string(data) != "false" {
		t.Errorf("command received yolo=%q, want false", string(data))
	}
}

// TestPinnedCommandAgentLaunchesDirectly asserts that `wt --agent <command>`
// (pinned) launches the command directly with no model screen, detected via
// agents.IsCommand rather than a string equality with "shell". Before the
// fix the pinned path compared agent == "shell", so a future command driver
// fell through to the model path and errored "agent not found".
func TestPinnedCommandAgentLaunchesDirectly(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched.txt")
	cleanup := agents.RegisterTest("wt-cmd-stub", func() agents.Driver {
		return commandStubDriver{name: "wt-cmd-stub", marker: marker}
	})
	t.Cleanup(cleanup)

	cfg := testConfig()
	m := model{cfg: cfg, initialAgent: "wt-cmd-stub", width: 80, height: 24}
	got, cmd := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature", Path: t.TempDir()}})
	gotModel := got.(model)
	if cmd == nil {
		t.Fatalf("selectedEntryMsg returned nil cmd for pinned command agent; status=%q", gotModel.status)
	}
	if gotModel.phase == phaseModel {
		t.Fatal("pinned command agent entered phaseModel (should skip the model screen)")
	}
	if _, ok := cmd().(launchDoneMsg); !ok {
		t.Fatalf("cmd did not produce launchDoneMsg")
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if string(data) != "wt-cmd-stub" {
		t.Errorf("launched name = %q, want wt-cmd-stub", string(data))
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

// commandStubDriver is a Driver that also implements Commanded
// (IsCommand() == true), so agents.IsCommand reports it as a command with no
// model layer. Build runs `sh -c` that writes the driver's name to marker,
// letting tests assert WHICH driver the TUI actually launched — the fix for
// the hardcoded-"shell" launch site.
type commandStubDriver struct {
	name   string
	marker string
}

func (commandStubDriver) YoloFlag() string { return "" }
func (d commandStubDriver) Build(_ config.Model, _ bool) agents.LaunchCmd {
	return agents.LaunchCmd{
		Bin:  "sh",
		Args: []string{"-c", "printf %s \"$0\" > " + shQuote(d.marker), d.name},
	}
}

func (d commandStubDriver) IsCommand() bool { return true }

type yoloCommandStubDriver struct {
	marker string
}

func (yoloCommandStubDriver) YoloFlag() string { return "" }
func (d yoloCommandStubDriver) Build(_ config.Model, yolo bool) agents.LaunchCmd {
	return agents.LaunchCmd{
		Bin:  "sh",
		Args: []string{"-c", "printf %s \"$0\" > " + shQuote(d.marker), boolString(yolo)},
	}
}

func (d yoloCommandStubDriver) IsCommand() bool { return true }

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
