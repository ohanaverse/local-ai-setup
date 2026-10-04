package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"
	"github.com/ohanaverse/local-ai-setup/wt/internal/session"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// startCall records one startModel invocation: what was asked to start and
// with which options.
type startCall struct {
	target lifecycle.Target
	opts   lifecycle.Options
}

// startCalls accumulates startModel invocations. The engine runs in a
// goroutine (runStart), so recording and reading are mutex-guarded — the
// race detector flags an unsynchronized slice shared with the test goroutine.
type startCalls struct {
	mu    sync.Mutex
	items []startCall
}

// recordAndCount appends one call and returns its 1-based number.
func (c *startCalls) recordAndCount(call startCall) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = append(c.items, call)
	return len(c.items)
}

// len reports how many times startModel was called.
func (c *startCalls) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// at returns the i-th call (0-based).
func (c *startCalls) at(i int) startCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.items[i]
}

// startCfg builds the picker config used by the start-flow fixtures: a cloud
// claude model plus one local model (provider/id/name).
func startCfg(provider, id, name string) *config.Config {
	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
			{ID: provider, Location: config.LocationLocal, Protocols: []config.Protocol{config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}},
		},
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Family: "opus", Tags: []string{"code"}},
			{ID: id, ProviderID: provider, ModelName: name, Family: name, Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"claude", provider}}},
	}
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-test"})
	return cfg
}

// startFixture builds the picker with a cloud claude model plus one local
// model (provider/id/name) that the inventory reports as NOT running, and
// enters the model phase. The caller marks the row with it.start = true (Task
// 4 wires this from action(); here it is set by hand) before pressing Enter.
func startFixture(t *testing.T, provider, id, name string) model {
	t.Helper()
	stubInventory(t, localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: provider, Artifact: name, ModelID: id, Registered: true},
	}})
	return flowEnter(t, model{cfg: startCfg(provider, id, name), agent: "claude", selectedPath: t.TempDir(), width: 80, height: 24}, "claude")
}

// enterStartRow selects the row for id, marks it as a start row (Task 4 wires
// this from action(); here it is set by hand), presses Enter and returns the
// next model.
func enterStartRow(t *testing.T, m model, id string) (model, tea.Cmd) {
	t.Helper()
	idx := indexOfID(m, id)
	if idx < 0 {
		t.Fatalf("no row %s in %v", id, itemIDs(m))
	}
	m.models.Select(idx)
	m.models.Items()[idx].(*modelItem).start = true
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return next.(model), cmd
}

// stubStartModel swaps the startModel seam to a fake that records every call
// (target + options) and scripts the result with behavior; behavior receives
// the 1-based call number, the context the flow handed the engine, and the
// options. The seam is restored on cleanup.
func stubStartModel(t *testing.T, behavior func(call int, ctx context.Context, target lifecycle.Target, opts lifecycle.Options) error) *startCalls {
	t.Helper()
	calls := &startCalls{}
	old := startModel
	startModel = func(ctx context.Context, cfg *config.Config, target lifecycle.Target, opts lifecycle.Options) error {
		n := calls.recordAndCount(startCall{target: target, opts: opts})
		return behavior(n, ctx, target, opts)
	}
	t.Cleanup(func() { startModel = old })
	return calls
}

// recvStart reads the next message off the in-flight start's channel with a
// 2s timeout, so a flow test fails instead of hanging when the engine never
// reports.
func recvStart(t *testing.T, m model) tea.Msg {
	t.Helper()
	select {
	case msg := <-m.start.ch:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a start message")
		return nil
	}
}

// waitStartCalls blocks until startModel has been called n times, failing the
// test if that count is not reached in time. The engine runs in the goroutine
// runStart spawns, so a test that asserts on what the engine did must wait for
// it to have done it: reading startCalls straight after the key that began the
// run asserts about a goroutine the test never synchronized with, which passes
// on a fast machine and fails on a 2-core CI runner (GOMAXPROCS=1 reproduces
// it every time). Recording happens at the top of stubStartModel, before
// runStart sends anything on m.start.ch, so a test that already drains a
// message is synchronized by that drain and does not need this — but a test
// that goes on to drain the channel itself cannot use recvStart as the barrier
// without consuming a message it still needs.
func waitStartCalls(t *testing.T, calls *startCalls, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for calls.len() < n {
		if time.Now().After(deadline) {
			// A timeout means the engine was never called, so report it as the
			// assertion failure it is rather than letting the test hang.
			t.Fatalf("startModel calls = %d, want %d", calls.len(), n)
		}
		// Yield rather than sleep: under GOMAXPROCS=1 a tight spin without a
		// scheduling point can starve the goroutine being waited on.
		runtime.Gosched()
	}
}

// updateMsg feeds msg to m.Update and returns the converted model, so flow
// tests stay on the concrete type.
func updateMsg(m model, msg tea.Msg) (model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(model), cmd
}

// TestEnterOnStartRowBeginsStart verifies Enter on a start row enters
// phaseStarting, calls startModel once with the row's target and
// AllowReplace false, streams the engine's stage into the state, and on
// success proceeds straight to launch — bypassing the ollama availability
// check (the model just loaded). This is the core of the start-on-select
// flow: without it Enter would show the old modelman hint again.
func TestEnterOnStartRowBeginsStart(t *testing.T) {
	requireBinary(t, "claude")
	m := startFixture(t, "ollama", "ollama/gemma4:9b", "gemma4:9b")
	calls := stubStartModel(t, func(call int, ctx context.Context, target lifecycle.Target, opts lifecycle.Options) error {
		if opts.Progress != nil {
			opts.Progress(lifecycle.StageWaiting)
		}
		return nil
	})

	got, cmd := enterStartRow(t, m, "ollama/gemma4:9b")
	if got.phase != phaseStarting {
		t.Fatalf("phase = %v, want phaseStarting", got.phase)
	}
	if cmd == nil {
		t.Fatal("beginStart returned a nil cmd; the flow would never report")
	}
	waitStartCalls(t, calls, 1)
	if calls.len() != 1 {
		t.Fatalf("startModel calls = %d, want 1", calls.len())
	}
	c := calls.at(0)
	if c.target != (lifecycle.Target{ProviderID: "ollama", ModelName: "gemma4:9b", ModelID: "ollama/gemma4:9b"}) {
		t.Errorf("target = %+v, want ollama/gemma4:9b pair", c.target)
	}
	if c.opts.AllowReplace {
		t.Error("first call must have AllowReplace == false")
	}

	// The streamed stage updates the state; then the done message proceeds
	// to launch.
	stage := recvStart(t, got)
	got, _ = updateMsg(got, stage)
	if got.start == nil || got.start.stage != lifecycle.StageWaiting {
		t.Fatalf("stage after startStageMsg = %v, want waiting-for-model", got.start)
	}
	done := recvStart(t, got)
	got, _ = updateMsg(got, done)
	if got.phase == phaseOllamaWarn {
		t.Error("a successful start must skip the ollama availability check")
	}
	if got.launchModel.ID != "ollama/gemma4:9b" {
		t.Errorf("launchModel.ID = %q, want the started row's id", got.launchModel.ID)
	}
	if got.start != nil {
		t.Errorf("start state lingers after the flow ended")
	}
}

// TestOccupiedOpensReplaceConfirmWithCancelDefault verifies an
// OccupiedError opens the replace-confirm dialog naming the occupant, with
// Cancel first and selected — refusing to destroy a running model without an
// explicit opt-in.
func TestOccupiedOpensReplaceConfirmWithCancelDefault(t *testing.T) {
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	calls := stubStartModel(t, func(call int, ctx context.Context, target lifecycle.Target, opts lifecycle.Options) error {
		return &lifecycle.OccupiedError{Occupant: localmodels.Entry{ModelID: "omlx/old"}}
	})

	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	done := recvStart(t, got)
	got, _ = updateMsg(got, done)

	if got.phase != phaseReplaceConfirm {
		t.Fatalf("phase = %v, want phaseReplaceConfirm", got.phase)
	}
	if !strings.Contains(got.replace.choices.Title, "omlx/old") {
		t.Errorf("dialog title %q must name the occupant", got.replace.choices.Title)
	}
	items := got.replace.choices.Items()
	if len(items) != 2 {
		t.Fatalf("choices = %d, want 2", len(items))
	}
	if c, _ := items[0].(choiceItem); c.title != "Cancel" {
		t.Errorf("first choice = %q, want Cancel", c.title)
	}
	if c, _ := items[1].(choiceItem); c.title != "Replace and start" {
		t.Errorf("second choice = %q, want Replace and start", c.title)
	}
	if got.replace.choices.Index() != 0 {
		t.Errorf("selected index = %d, want 0 (Cancel is the default)", got.replace.choices.Index())
	}
	if calls.len() != 1 {
		t.Errorf("startModel calls = %d, want 1 (no re-issue before confirming)", calls.len())
	}
}

// TestUnknownOccupancyOpensReplaceConfirm verifies an OccupancyUnknownError
// opens the same dialog, with the title naming the provider and origin it
// could not probe — the user must decide without a false claim of certainty.
func TestUnknownOccupancyOpensReplaceConfirm(t *testing.T) {
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	stubStartModel(t, func(call int, ctx context.Context, target lifecycle.Target, opts lifecycle.Options) error {
		return &lifecycle.OccupancyUnknownError{ProviderID: "omlx", Origin: "http://localhost:8000"}
	})

	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	done := recvStart(t, got)
	got, _ = updateMsg(got, done)

	if got.phase != phaseReplaceConfirm {
		t.Fatalf("phase = %v, want phaseReplaceConfirm", got.phase)
	}
	if !strings.Contains(got.replace.choices.Title, "omlx") || !strings.Contains(got.replace.choices.Title, "http://localhost:8000") {
		t.Errorf("dialog title %q must mention the provider and origin", got.replace.choices.Title)
	}
}

// TestReplaceConfirmProceedReissuesStartWithAllowReplace verifies Enter on
// "Replace and start" re-issues Start with AllowReplace true — the confirm
// dialog is the only path that sets it.
func TestReplaceConfirmProceedReissuesStartWithAllowReplace(t *testing.T) {
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	calls := stubStartModel(t, func(call int, ctx context.Context, target lifecycle.Target, opts lifecycle.Options) error {
		return &lifecycle.OccupiedError{Occupant: localmodels.Entry{ModelID: "omlx/old"}}
	})

	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, recvStart(t, got))
	got.replace.choices.Select(1)

	got, _ = updateMsg(got, tea.KeyMsg{Type: tea.KeyEnter})

	if got.phase != phaseStarting {
		t.Fatalf("phase = %v, want phaseStarting (the replace start is in flight)", got.phase)
	}
	waitStartCalls(t, calls, 2)
	if calls.len() != 2 {
		t.Fatalf("startModel calls = %d, want 2", calls.len())
	}
	if !calls.at(1).opts.AllowReplace {
		t.Error("second call must have AllowReplace == true")
	}
	c := calls.at(1)
	if c.target != (lifecycle.Target{ProviderID: "omlx", ModelName: "qwen3.8", ModelID: "omlx/qwen3.8"}) {
		t.Errorf("second target = %+v, want the same row", c.target)
	}
}

// TestReplaceConfirmCancelDoesNotStart verifies Enter on Cancel (the
// default) and esc both return to the picker with no second Start call —
// backing out must never destroy the running occupant.
func TestReplaceConfirmCancelDoesNotStart(t *testing.T) {
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	calls := stubStartModel(t, func(call int, ctx context.Context, target lifecycle.Target, opts lifecycle.Options) error {
		return &lifecycle.OccupiedError{Occupant: localmodels.Entry{ModelID: "omlx/old"}}
	})

	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, recvStart(t, got))
	if got.replace.choices.Index() != 0 {
		t.Fatalf("precondition: Cancel is the selected choice")
	}

	got, _ = updateMsg(got, tea.KeyMsg{Type: tea.KeyEnter})
	if got.phase != phaseModel {
		t.Errorf("Cancel: phase = %v, want phaseModel", got.phase)
	}
	if calls.len() != 1 {
		t.Errorf("Cancel: startModel calls = %d, want 1 (no re-issue)", calls.len())
	}

	// esc pops the dialog too.
	got2, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got2, _ = updateMsg(got2, recvStart(t, got2))
	got2, _ = updateMsg(got2, tea.KeyMsg{Type: tea.KeyEsc})
	if got2.phase != phaseModel {
		t.Errorf("esc: phase = %v, want phaseModel", got2.phase)
	}
}

// TestKeysDuringStartCancelThenOnlyCtrlCQuits verifies esc, q and ctrl+c all
// CANCEL an in-flight start (the context is cancelled, the view says
// Cancelling, and the engine's done message returns to the picker with status
// "cancelled") — but only ctrl+c quits wt. esc and q are ignored once
// cancellation is draining, because quitting then can orphan a server the
// engine spawned into its own session (an mtplx child holding port 8003), and
// those two are exactly the keys a user mashes when a start looks stuck.
// ctrl+c stays a deliberate escape hatch: a cancel that never returns would
// otherwise trap the user on the progress screen with no key that exits.
func TestKeysDuringStartCancelThenOnlyCtrlCQuits(t *testing.T) {
	keys := map[string]tea.KeyMsg{
		"esc":    {Type: tea.KeyEsc},
		"q":      {Type: tea.KeyRunes, Runes: []rune{'q'}},
		"ctrl+c": {Type: tea.KeyCtrlC},
	}
	for name, key := range keys {
		t.Run(name, func(t *testing.T) {
			m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
			calls := stubStartModel(t, func(call int, ctx context.Context, target lifecycle.Target, opts lifecycle.Options) error {
				<-ctx.Done()
				return ctx.Err()
			})

			got, _ := enterStartRow(t, m, "omlx/qwen3.8")
			if got.phase != phaseStarting {
				t.Fatalf("precondition: phase = %v, want phaseStarting", got.phase)
			}

			// First press cancels.
			got, _ = updateMsg(got, key)
			if got.start == nil || !got.start.cancelling {
				t.Fatalf("after %s: cancelling = false, want true", name)
			}
			if v := got.View(); !strings.Contains(v, "Cancelling") {
				t.Errorf("view after cancel press = %q, want it to say Cancelling", v)
			}
			waitStartCalls(t, calls, 1)
			if calls.len() != 1 {
				t.Errorf("startModel calls = %d, want 1 (cancel does not re-issue)", calls.len())
			}

			// A further press while cancelling: ctrl+c leaves, esc/q do not.
			got, quitCmd := updateMsg(got, key)
			if name == "ctrl+c" {
				if quitCmd == nil {
					t.Fatal("second ctrl+c press returned a nil cmd, want tea.Quit")
				}
				if msg := quitCmd(); msg != (tea.QuitMsg{}) {
					t.Errorf("second ctrl+c press cmd message = %T, want tea.QuitMsg", msg)
				}
			} else {
				if quitCmd != nil {
					t.Errorf("second %s press returned a cmd (%T); it must not quit while the engine tears down", name, quitCmd)
				}
				if got.phase != phaseStarting {
					t.Errorf("phase = %v after a second %s press, want to stay in phaseStarting", got.phase, name)
				}
				if v := got.View(); !strings.Contains(v, "ctrl+c") {
					t.Errorf("cancelling view = %q, want it to name the one key that does quit", v)
				}
			}

			// The engine's done message returns to the picker.
			got, _ = updateMsg(got, recvStart(t, got))
			if got.phase != phaseModel {
				t.Errorf("phase = %v, want phaseModel after the engine reported done", got.phase)
			}
			if got.status != "cancelled" {
				t.Errorf("status = %q, want cancelled", got.status)
			}
		})
	}
}

// TestKeysDuringStartIgnoreOtherKeys verifies every key that is not
// esc/q/ctrl+c leaves an in-flight start untouched — the progress screen
// must not respond to picker navigation or typing.
func TestKeysDuringStartIgnoreOtherKeys(t *testing.T) {
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	stubStartModel(t, func(call int, ctx context.Context, target lifecycle.Target, opts lifecycle.Options) error {
		<-ctx.Done()
		return ctx.Err()
	})

	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if got.start == nil || got.start.cancelling {
		t.Error("an unrelated keypress must not cancel the start")
	}
	if got.phase != phaseStarting {
		t.Errorf("phase = %v, want phaseStarting", got.phase)
	}
}

// TestStartFailureRefreshesTable verifies a failed start rebuilds the table
// from a fresh inventory (one more probe) and returns to the picker with the
// failure in the status line — a replace can stop the occupant and then
// fail, so the old table would be lying — while the confirm cases return to
// the dialog without probing again.
func TestStartFailureRefreshesTable(t *testing.T) {
	confirm := false

	// Count probes BEFORE entering the model phase, so enterModelPhase's
	// build is the first counted probe.
	probes := 0
	oldInv := runInventory
	snap := localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", Artifact: "qwen3.8", ModelID: "omlx/qwen3.8", Registered: true},
	}}
	runInventory = func(*config.Config) localmodels.Snapshot { probes++; return snap }
	t.Cleanup(func() { runInventory = oldInv })

	stubStartModel(t, func(call int, ctx context.Context, target lifecycle.Target, opts lifecycle.Options) error {
		if confirm {
			return &lifecycle.OccupiedError{Occupant: localmodels.Entry{ModelID: "omlx/old"}}
		}
		return errors.New("boom")
	})

	m := flowEnter(t, model{cfg: startCfg("omlx", "omlx/qwen3.8", "qwen3.8"), agent: "claude", selectedPath: t.TempDir(), width: 80, height: 24}, "claude")
	if probes != 1 {
		t.Fatalf("precondition: probes = %d after enterModelPhase, want 1", probes)
	}

	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, refreshCmd := updateMsg(got, recvStart(t, got))
	if got.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel after a non-confirm failure", got.phase)
	}
	if !strings.Contains(got.status, "failed to start omlx/qwen3.8: boom") {
		t.Errorf("status = %q, want the failure message", got.status)
	}
	// The refresh probe is deferred to the command the failure returned, so it
	// has not run yet — probing here would block the update loop.
	if probes != 1 {
		t.Errorf("runInventory calls = %d before draining, want 1 (the probe must be deferred)", probes)
	}
	got = drainCmds(t, got, refreshCmd)
	if probes != 2 {
		t.Errorf("runInventory calls = %d, want 2 (the failure refreshes the table)", probes)
	}

	// The confirm cases open the dialog without a refresh. m2's build is
	// the third probe.
	confirm = true
	m2 := flowEnter(t, model{cfg: startCfg("omlx", "omlx/qwen3.8", "qwen3.8"), agent: "claude", selectedPath: t.TempDir(), width: 80, height: 24}, "claude")
	got2, _ := enterStartRow(t, m2, "omlx/qwen3.8")
	got2, confirmCmd := updateMsg(got2, recvStart(t, got2))
	if got2.phase != phaseReplaceConfirm {
		t.Fatalf("phase = %v, want phaseReplaceConfirm", got2.phase)
	}
	if confirmCmd != nil {
		t.Errorf("confirm case returned a cmd, want none: the dialog decides, not a refresh")
	}
	if probes != 3 {
		t.Errorf("runInventory calls = %d, want 3 (the confirm case must not refresh)", probes)
	}
}

// TestSuccessfulStartThenFailedLaunchRefreshesTable verifies that when a model
// starts successfully but the launch built afterwards fails, the picker is
// rebuilt from a fresh inventory. The model is loaded and serving by then, so
// the table built before the attempt still reports RUNNING="-" — the user
// cannot tell the start worked, and nothing re-probes until some later start
// fails. The launch is made to fail the way launch_lifecycle_test.go does it,
// with an agent no driver is registered for.
func TestSuccessfulStartThenFailedLaunchRefreshesTable(t *testing.T) {
	// The first probe is the table built before the start: the model is not
	// loaded yet, so its row does not show run. Every later probe reports what
	// the successful start produced.
	probes := 0
	old := runInventory
	runInventory = func(*config.Config) localmodels.Snapshot {
		probes++
		return localmodels.Snapshot{Entries: []localmodels.Entry{{
			ProviderID: "omlx", Artifact: "qwen3.8", ModelID: "omlx/qwen3.8",
			Registered: true, Running: probes > 1,
		}}}
	}
	t.Cleanup(func() { runInventory = old })
	stubStartModel(t, func(int, context.Context, lifecycle.Target, lifecycle.Options) error { return nil })

	m := flowEnter(t, model{cfg: startCfg("omlx", "omlx/qwen3.8", "qwen3.8"), agent: "claude", selectedPath: t.TempDir(), width: 80, height: 24}, "claude")
	if probes != 1 {
		t.Fatalf("precondition: probes = %d after entering the model phase, want 1", probes)
	}
	// The start does not consult the agent; only the launch built after it
	// does, and an unregistered agent makes launchAgent fail.
	m.agent = "not-a-real-agent"

	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, refreshCmd := updateMsg(got, recvStart(t, got))
	if got.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel after the launch failed", got.phase)
	}
	if !strings.Contains(got.status, "launch failed") {
		t.Fatalf("status = %q, want the launch failure", got.status)
	}
	if refreshCmd == nil {
		t.Fatal("the failed launch returned a nil cmd, so the picker was never re-probed")
	}

	got = drainCmds(t, got, refreshCmd)
	if probes != 2 {
		t.Errorf("runInventory calls = %d, want 2 (the failed launch refreshes the table)", probes)
	}
	idx := indexOfID(got, "omlx/qwen3.8")
	if idx < 0 {
		t.Fatalf("omlx/qwen3.8 row missing after the refresh: %v", itemIDs(got))
	}
	if line := got.models.Items()[idx].(*modelItem).line; !runningCell(line) {
		t.Errorf("row %q does not show run after a successful start — the table still lies about RUNNING", line)
	}
}

// TestStaleStartMessagesIgnored verifies start messages carrying a run id
// that does not match the in-flight start change nothing — a late message
// from a cancelled run must not overwrite the current flow's stage or
// resolve it.
func TestStaleStartMessagesIgnored(t *testing.T) {
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	calls := stubStartModel(t, func(call int, ctx context.Context, target lifecycle.Target, opts lifecycle.Options) error {
		return nil
	})

	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	if got.start == nil {
		t.Fatal("precondition: no start in flight")
	}

	got, _ = updateMsg(got, startStageMsg{id: 99, stage: lifecycle.StageWarming})
	if got.start == nil || got.start.stage != "" {
		t.Errorf("stale stage message changed the stage to %q", got.start.stage)
	}
	if got.phase != phaseStarting {
		t.Errorf("stale stage message moved the phase to %v", got.phase)
	}

	got, _ = updateMsg(got, startDoneMsg{id: 99, err: nil})
	if got.start == nil {
		t.Error("stale done message resolved the in-flight start")
	}
	if got.phase != phaseStarting {
		t.Errorf("stale done message moved the phase to %v", got.phase)
	}
	waitStartCalls(t, calls, 1)
	if calls.len() != 1 {
		t.Errorf("startModel calls = %d, want 1", calls.len())
	}
}

// TestStartingViewShowsStageAndElapsed verifies the progress view names the
// model, the current engine stage and the elapsed time, offers [esc] cancel,
// and switches to Cancelling after a cancel press — the user must see that
// the start is real and that backing out is possible.
func TestStartingViewShowsStageAndElapsed(t *testing.T) {
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	stubStartModel(t, func(call int, ctx context.Context, target lifecycle.Target, opts lifecycle.Options) error {
		<-ctx.Done()
		return ctx.Err()
	})

	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got.start.stage = lifecycle.StageWaiting
	got.start.began = time.Now().Add(-12 * time.Second)

	v := got.View()
	for _, want := range []string{"omlx/qwen3.8", "waiting for the model to load", "12s", "[esc] cancel"} {
		if !strings.Contains(v, want) {
			t.Errorf("starting view %q must contain %q", v, want)
		}
	}

	// The route window after the engine finishes is its own stage: showing
	// "warming the model" while wt bounces LiteLLM would be a lie the user
	// stares at for seconds.
	got.start.stage = lifecycle.StageRouting
	if v := got.View(); !strings.Contains(v, "updating LiteLLM routes") {
		t.Errorf("routing view %q must name the LiteLLM route update", v)
	}

	got.start.cancelling = true
	if v := got.View(); !strings.Contains(v, "Cancelling") {
		t.Errorf("cancelling view %q must say Cancelling", v)
	}
}

// e2eStubStartModel is a startModel fake for end-to-end tests: it records the
// call and reports a successful start.
func e2eStubStartModel(t *testing.T) *startCalls {
	t.Helper()
	return stubStartModel(t, func(call int, ctx context.Context, target lifecycle.Target, opts lifecycle.Options) error {
		return nil
	})
}

// TestEndToEndNonRunningOmlxRowStartsThenLaunches drives the real path —
// stubbed inventory to enterModelPhase to Enter to the (stubbed) engine — to
// verify a non-running omlx row is a start row (start == true, no hint), that
// Enter begins a start with AllowReplace false (so the engine's occupant check,
// not the UI, decides whether to ask before stopping another model), and that
// success proceeds to launch. This is the behavior the whole plan exists for.
func TestEndToEndNonRunningOmlxRowStartsThenLaunches(t *testing.T) {
	requireBinary(t, "claude")
	stubInventory(t, localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", Artifact: "qwen3.8", ModelID: "omlx/qwen3.8", Registered: true, ArtifactKnown: true},
	}})
	calls := e2eStubStartModel(t)

	got := flowEnter(t, model{cfg: startCfg("omlx", "omlx/qwen3.8", "qwen3.8"), agent: "claude", selectedPath: t.TempDir(), width: 80, height: 24}, "claude")
	idx := indexOfID(got, "omlx/qwen3.8")
	if idx < 0 {
		t.Fatalf("no omlx row in %v", itemIDs(got))
	}
	it := got.models.Items()[idx].(*modelItem)
	if !it.start || it.blocked != "" {
		t.Fatalf("row start = %v blocked = %q, want a start row with no hint", it.start, it.blocked)
	}

	got.models.Select(idx)
	next, cmd := got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got = next.(model)
	if got.phase != phaseStarting || cmd == nil {
		t.Fatalf("phase = %v cmd = %v, want phaseStarting with a cmd", got.phase, cmd)
	}
	got, _ = updateMsg(got, recvStart(t, got))
	if got.launchModel.ID != "omlx/qwen3.8" {
		t.Errorf("launchModel.ID = %q, want the started row's id", got.launchModel.ID)
	}
	if calls.len() != 1 {
		t.Fatalf("startModel calls = %d, want 1", calls.len())
	}
	// The first attempt must refuse to replace a running occupant: the engine's
	// own OccupiedError is what drives the confirm dialog, and replacing without
	// asking would stop another model behind the user's back.
	if calls.at(0).opts.AllowReplace {
		t.Error("first startModel call had AllowReplace == true, want false")
	}
}

// TestEndToEndPulledOllamaRowStartsInsteadOfLaunchingCold verifies a pulled,
// not-loaded ollama row is a start row — the old launch exception is gone —
// and Enter begins a start through the engine instead of an ollama check and
// a cold launch.
func TestEndToEndPulledOllamaRowStartsInsteadOfLaunchingCold(t *testing.T) {
	stubInventory(t, localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "ollama", Artifact: "gemma4:9b", ModelID: "ollama/gemma4:9b", Registered: true, ArtifactKnown: true},
	}})
	calls := e2eStubStartModel(t)

	got := flowEnter(t, model{cfg: startCfg("ollama", "ollama/gemma4:9b", "gemma4:9b"), agent: "claude", selectedPath: t.TempDir(), width: 80, height: 24}, "claude")
	idx := indexOfID(got, "ollama/gemma4:9b")
	if idx < 0 {
		t.Fatalf("no ollama row in %v", itemIDs(got))
	}
	it := got.models.Items()[idx].(*modelItem)
	if !it.start || it.blocked != "" {
		t.Fatalf("row start = %v blocked = %q, want a start row", it.start, it.blocked)
	}

	got.models.Select(idx)
	next, _ := got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got = next.(model)
	if got.phase != phaseStarting {
		t.Errorf("phase = %v, want phaseStarting (a start, not the ollama-check/launch path)", got.phase)
	}
	waitStartCalls(t, calls, 1)
	if calls.len() != 1 {
		t.Errorf("startModel calls = %d, want 1", calls.len())
	}
}

// TestEndToEndAbsentRowIsHidden verifies a registered omlx model the probe
// confirmed is not on disk gets no picker row at all (#179 Phase B: local rows
// come only from the inventory), so it can neither be started nor selected —
// starting a model that is not there would just wait out the warmup timeout.
func TestEndToEndAbsentRowIsHidden(t *testing.T) {
	stubInventory(t, localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", ModelID: "omlx/qwen3.8", Registered: true, ArtifactKnown: true},
	}})
	calls := stubStartModel(t, func(call int, ctx context.Context, target lifecycle.Target, opts lifecycle.Options) error {
		t.Error("startModel must not be called for an absent model")
		return errors.New("must not start")
	})

	got := flowEnter(t, model{cfg: startCfg("omlx", "omlx/qwen3.8", "qwen3.8"), agent: "claude", selectedPath: t.TempDir(), width: 80, height: 24}, "claude")
	if idx := indexOfID(got, "omlx/qwen3.8"); idx >= 0 {
		t.Fatalf("absent omlx/qwen3.8 has a row at %d in %v, want none", idx, itemIDs(got))
	}
	if calls.len() != 0 {
		t.Errorf("startModel calls = %d, want 0", calls.len())
	}
}

// TestSingleRowShortcutDoesNotFireForStartRow verifies the one-row
// auto-launch shortcut does NOT fire for a start row: a lone non-running
// local model must show the picker and wait for Enter, not silently start a
// server the user never asked for.
func TestSingleRowShortcutDoesNotFireForStartRow(t *testing.T) {
	local := &config.Config{
		DefaultTag: "code",
		Providers:  []config.Provider{{ID: "omlx", Location: config.LocationLocal, Protocols: []config.Protocol{config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Models:     []config.Model{{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8", Family: "qwen3.8", Tags: []string{"code"}}},
		Agents:     []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	calls := stubStartModel(t, func(call int, ctx context.Context, target lifecycle.Target, opts lifecycle.Options) error {
		t.Error("enterModelPhase must not auto-start a lone start row")
		return errors.New("no auto-start")
	})
	tempStateDir(t)
	stubUsageStore(t)
	stubRefcountStore(t)
	m := model{cfg: local, width: 80, height: 24}
	models, _ := local.EligibleModels("pi", "", "")
	got, cmd := m.enterModelPhase("pi", models, "code")
	if got.phase != phaseModel {
		t.Errorf("phase = %v, want phaseModel (no auto-launch for a start row)", got.phase)
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil (no auto-launch, no auto-start)", cmd)
	}
	items := got.models.Items()
	if len(items) != 1 || !items[0].(*modelItem).start {
		t.Errorf("row start = %v (items = %d), want a single start row", items[0].(*modelItem).start, len(items))
	}
	// As in TestEndToEndAbsentRowIsBlocked: a start that never began cannot be
	// waited for, so assert the absent start state as well as the count.
	if got.start != nil {
		t.Error("the shortcut must not leave a start in flight")
	}
	if calls.len() != 0 {
		t.Errorf("startModel calls = %d, want 0", calls.len())
	}
}

// TestReplaceFlagOnlyCoversThePinnedRow verifies --replace grants AllowReplace
// to the -M pinned row only: Enter on any other start row still starts with
// AllowReplace false, so the occupied dialog appears. --replace is documented
// as a -M option; letting it silently stop a running model for an arbitrary
// row the user browsed to would be a surprise.
func TestReplaceFlagOnlyCoversThePinnedRow(t *testing.T) {
	requireBinary(t, "claude")
	m := startFixture(t, "ollama", "ollama/gemma4:9b", "gemma4:9b")
	m.allowReplace = true
	m.pinnedModel = "ollama/other"
	calls := stubStartModel(t, func(int, context.Context, lifecycle.Target, lifecycle.Options) error { return nil })

	got, _ := enterStartRow(t, m, "ollama/gemma4:9b")
	if got.start != nil && got.start.cancel != nil {
		t.Cleanup(got.start.cancel)
	}
	waitStartCalls(t, calls, 1)

	if calls.at(0).opts.AllowReplace {
		t.Error("a non-pinned row must not inherit --replace")
	}
}

// TestReplaceFlagGrantsPermissionToPinnedRow verifies the --replace plumbing
// end to end inside the TUI: with allowReplace set, Enter on the -M pinned
// start row reaches the engine with AllowReplace true (no dialog needed), and
// without it the same row starts with AllowReplace false so an occupied
// provider raises the replace dialog. The existing test pins only that a
// non-pinned row does NOT inherit the flag; this pins the positive path, so a
// dropped model.allowReplace → beginStart wire fails here. The Run(allowReplace)
// → model hop is covered separately by TestRunArgsReachModel.
func TestReplaceFlagGrantsPermissionToPinnedRow(t *testing.T) {
	requireBinary(t, "claude")
	for _, allow := range []bool{true, false} {
		m := startFixture(t, "ollama", "ollama/gemma4:9b", "gemma4:9b")
		m.allowReplace = allow
		m.pinnedModel = "ollama/gemma4:9b"
		calls := stubStartModel(t, func(int, context.Context, lifecycle.Target, lifecycle.Options) error { return nil })

		got, _ := enterStartRow(t, m, "ollama/gemma4:9b")
		if got.start != nil && got.start.cancel != nil {
			t.Cleanup(got.start.cancel)
		}
		waitStartCalls(t, calls, 1)

		if got := calls.at(0).opts.AllowReplace; got != allow {
			t.Errorf("allowReplace=%v: engine saw AllowReplace=%v, want %v", allow, got, allow)
		}
	}
}

// TestRunArgsReachModel verifies the Run(...) → model hand-off: the allowReplace
// argument (from --replace) and the -M pinned model land on the initial model.
// This is the one hop TestReplaceFlagGrantsPermissionToPinnedRow skips by
// assigning m.allowReplace directly; without it, a Run that stopped copying
// allowReplace would silently disable --replace while every other test passed.
func TestRunArgsReachModel(t *testing.T) {
	for _, allow := range []bool{true, false} {
		m := newRunModel(false, allow, "", "ollama/gemma4:9b", "", "", nil, themes.Theme{}, "", nil)
		if m.allowReplace != allow {
			t.Errorf("allowReplace=%v: model.allowReplace=%v", allow, m.allowReplace)
		}
		if m.pinnedModel != "ollama/gemma4:9b" {
			t.Errorf("model.pinnedModel=%q, want the -M pin", m.pinnedModel)
		}
	}
}

// TestSuccessfulStartWaitsForPendingRoutesBeforeLaunching verifies the start
// flow does not hand the model to an agent while the route hook's LiteLLM
// proxy restart is still in flight. That restart runs asynchronously now, and
// the agent launched here dials the model THROUGH the proxy: launching early
// means a refused connection (proxy mid-restart) or the route table from
// before this model existed. The launch is made to fail the way
// TestSuccessfulStartThenFailedLaunchRefreshesTable does, with an agent no
// driver is registered for, so the test can see exactly when the launch was
// attempted without starting a real process. That lifecycle.WaitPendingRoutes
// really blocks until the restart finishes is pinned in internal/lifecycle by
// TestWaitPendingRoutesBlocksUntilTheRestartFinishes.
func TestSuccessfulStartWaitsForPendingRoutesBeforeLaunching(t *testing.T) {
	m := startFixture(t, "ollama", "ollama/gemma4:9b", "gemma4:9b")
	stubStartModel(t, func(int, context.Context, lifecycle.Target, lifecycle.Options) error { return nil })
	m.agent = "not-a-real-agent" // makes the launch that follows fail, observably

	entered := make(chan struct{})
	release := make(chan struct{})
	oldWait := waitPendingRoutes
	waitPendingRoutes = func() { close(entered); <-release }
	t.Cleanup(func() { waitPendingRoutes = oldWait })

	got, _ := enterStartRow(t, m, "ollama/gemma4:9b")
	// A model value is far too large for a channel element, so the result is
	// handed back through a pointer the goroutine fills before closing done.
	var next model
	done := make(chan struct{})
	go func() {
		defer close(done)
		next, _ = updateMsg(got, recvStart(t, got))
	}()

	<-entered
	select {
	case <-done:
		t.Fatal("the start flow launched while the proxy restart was still pending")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)

	select {
	case <-done:
		// The launch was attempted only after the wait returned: its failure
		// is the proof it ran at all, and it could not have run earlier.
		if !strings.Contains(next.status, "launch failed") {
			t.Fatalf("status = %q, want the launch to have been attempted after the wait", next.status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the start flow never proceeded after the routes settled")
	}
}

// launchRowFixture is the model picker over modelTestConfig with omlx/qwen3.8
// running, the cursor on id, and the agent swapped for one no driver knows so
// that Enter runs proceedToLaunch and then fails the launch observably.
func launchRowFixture(t *testing.T, id string) model {
	t.Helper()
	tempStateDir(t)
	stubUsageStore(t)
	stubRefcountStore(t)
	stubInventory(t, runningOmlxSnapshot())
	m := flowEnter(t, model{cfg: modelTestConfig(), agent: "claude", selectedPath: t.TempDir(), width: 80, height: 24}, "claude")
	idx := indexOfID(m, id)
	if idx < 0 {
		t.Fatalf("no row %s in %v", id, itemIDs(m))
	}
	m.models.Select(idx)
	m.agent = "not-a-real-agent"
	return m
}

// routeUpdatedLine is the one line lifecycle prints when the launch-time check
// wrote a route; the tests script the stub to "print" it.
const routeUpdatedLine = "wt: LiteLLM route for omlx/qwen3.8 updated\n"

// routeWaitingLine is what Run() prints before it waits for a proxy restart
// the user quit out of.
const routeWaitingLine = "wt: waiting for the LiteLLM proxy restart…\n"

// TestLaunchOfRunningLocalRowEnsuresRoute pins #192 for the picker: Enter on
// a local row that is already running checks its LiteLLM route — such a model
// may never have been started by wt, so no start hook wrote its route — and,
// when the check had to write it, shows the routing phase instead of
// launching. The wait for the proxy restart must NOT have run by the time
// Update returns: that wait on the update goroutine is the 10–20 s frozen
// picker this phase exists to remove.
func TestLaunchOfRunningLocalRowEnsuresRoute(t *testing.T) {
	m := launchRowFixture(t, "omlx/qwen3.8")
	stub := stubEnsureRoute(t)
	stubRouteNotes(t)

	next, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	if next.phase != phaseRouting {
		t.Fatalf("phase = %v, want phaseRouting while the proxy restarts", next.phase)
	}
	if got := strings.Join(stub.events, ","); got != "ensure:omlx/qwen3.8" {
		t.Fatalf("events = %q, want only ensure:omlx/qwen3.8 — the wait belongs to the command, not the update goroutine", got)
	}
	if strings.Contains(next.status, "launch failed") {
		t.Fatalf("status = %q: the launch was attempted before the proxy restart finished", next.status)
	}
	if cmd == nil {
		t.Fatal("no command returned: nothing would ever end the routing phase")
	}
	if next.routing == nil || next.routing.modelID != "omlx/qwen3.8" {
		t.Fatalf("routing = %+v, want the in-flight check for omlx/qwen3.8", next.routing)
	}
}

// TestLaunchRouteCheckYieldsTheLockInsteadOfWaiting pins the picker's half of
// #192's freeze fix. When the check cannot take the config.yaml lock at once —
// another wt is mid-write — the update goroutine hands the whole thing to the
// routing command instead of waiting for it, so the screen keeps repainting and
// ctrl+c still works; the command retries the check with the lock wait it is
// allowed to pay there, then waits for the restart that retry may start. The
// retry is what shows up as a second "ensure:" event, and the routing screen is
// entered even though whether the proxy will restart is not yet known.
func TestLaunchRouteCheckYieldsTheLockInsteadOfWaiting(t *testing.T) {
	m := launchRowFixture(t, "omlx/qwen3.8")
	stub := stubEnsureRoute(t)
	stub.busy = true
	stubRouteNotes(t)

	routing, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	if routing.phase != phaseRouting {
		t.Fatalf("phase = %v, want phaseRouting", routing.phase)
	}
	if got := strings.Join(stub.events, ","); got != "ensure:omlx/qwen3.8" {
		t.Fatalf("events = %q, want only the non-blocking attempt: no wait, and no retry yet", got)
	}
	if cmd == nil {
		t.Fatal("no command returned: nothing would retry the check or end the routing phase")
	}

	next, _ := updateMsg(routing, routeDoneFrom(t, cmd))

	if got := strings.Join(stub.events, ","); got != "ensure:omlx/qwen3.8,ensure:omlx/qwen3.8,wait" {
		t.Fatalf("events = %q, want the check retried once behind the screen, then the wait", got)
	}
	if next.phase == phaseRouting || next.routing != nil {
		t.Fatalf("phase = %v routing = %+v, want the routing phase left behind", next.phase, next.routing)
	}
	if !strings.Contains(next.status, "launch failed") {
		t.Fatalf("status = %q, want the launch to have been attempted after the retry", next.status)
	}
	// One buffer for the whole check: what the retry prints has to reach the
	// notes the first attempt's would have.
	if len(stub.outs) != 2 || stub.outs[0] == nil || stub.outs[0] != stub.outs[1] {
		t.Fatalf("writers handed to the check = %v, want the retry given the first attempt's", stub.outs)
	}
}

// TestRoutingCommandWaitsThenLaunches pins the second half: the command waits
// for the proxy, and only its message launches the agent. Launching without
// that wait hands the agent a proxy that is mid-restart (refused connection)
// or still serving the old route table ("Invalid model name"). It also pins
// that the check's output is read only after the wait, so the restart's own
// warnings — printed until then — are in the notes the message carries.
func TestRoutingCommandWaitsThenLaunches(t *testing.T) {
	m := launchRowFixture(t, "omlx/qwen3.8")
	stub := stubEnsureRoute(t)
	stubRouteNotes(t)

	stub.ensureOutput, stub.waitOutput = routeUpdatedLine, "wt: LiteLLM proxy did not come back\n"

	routing, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	done := routeDoneFrom(t, cmd)
	next, _ := updateMsg(routing, done)

	if got := strings.Join(stub.events, ","); got != "ensure:omlx/qwen3.8,wait" {
		t.Fatalf("events = %q, want ensure:omlx/qwen3.8,wait", got)
	}
	if want := routeUpdatedLine + "wt: LiteLLM proxy did not come back\n"; done.notes != want {
		t.Fatalf("notes = %q, want %q: the buffer was read before the restart had finished printing", done.notes, want)
	}
	if next.phase == phaseRouting || next.routing != nil {
		t.Fatalf("phase = %v routing = %+v, want the routing phase left behind", next.phase, next.routing)
	}
	if !strings.Contains(next.status, "launch failed") {
		t.Fatalf("status = %q, want the launch to have been attempted after the wait", next.status)
	}
}

// TestLaunchWithRouteAlreadyPresentLaunchesAtOnce pins the common case: the
// route is already in config.yaml, nothing restarts, and the launch happens in
// the same Update as today — no routing screen for even a frame and no wait.
// A regression here would add a screen flash, or a needless wait, to every
// launch of a running local model.
func TestLaunchWithRouteAlreadyPresentLaunchesAtOnce(t *testing.T) {
	m := launchRowFixture(t, "omlx/qwen3.8")
	stub := stubEnsureRoute(t)
	stub.changed = false
	stubRouteNotes(t)

	next, _ := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	if next.phase == phaseRouting || next.routing != nil {
		t.Fatalf("phase = %v routing = %+v, want no routing phase when nothing changed", next.phase, next.routing)
	}
	if got := strings.Join(stub.events, ","); got != "ensure:omlx/qwen3.8" {
		t.Fatalf("events = %q, want ensure:omlx/qwen3.8 and no wait: this write started no restart", got)
	}
	if !strings.Contains(next.status, "launch failed") {
		t.Fatalf("status = %q, want the launch to have been attempted in the same Update", next.status)
	}
}

// TestRouteCheckOutputIsKept pins that what the route check prints is not
// lost under the alt screen: the captured text lands in pendingRouteNotes (for
// the real terminal) and its last line in the status line (for a user who ends
// up on a picker screen instead of in the agent). Both paths are covered — a
// changed route, whose output arrives with the routing command's message, and
// an unchanged one that still warned — plus a two-line capture, where the
// restart's warning is the line worth showing and the notes keep both. The
// flow is steered to the resume prompt so the status is the check's own and
// not a launch failure's.
func TestRouteCheckOutputIsKept(t *testing.T) {
	const restartWarning = "wt: LiteLLM proxy did not come back\n"
	const writeWarning = "wt: LiteLLM route not updated: boom\n"
	for _, tc := range []struct {
		name                     string
		changed                  bool
		ensureOutput, waitOutput string
		wantStatus               string
	}{
		{name: "changed", changed: true, ensureOutput: routeUpdatedLine, wantStatus: "wt: LiteLLM route for omlx/qwen3.8 updated"},
		{name: "changed, restart warned", changed: true, ensureOutput: routeUpdatedLine, waitOutput: restartWarning, wantStatus: "wt: LiteLLM proxy did not come back"},
		{name: "unchanged, write warned", changed: false, ensureOutput: writeWarning, wantStatus: "wt: LiteLLM route not updated: boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := launchRowFixture(t, "omlx/qwen3.8")
			stub := stubEnsureRoute(t)
			stub.changed, stub.ensureOutput, stub.waitOutput = tc.changed, tc.ensureOutput, tc.waitOutput
			stubRouteNotes(t)
			prev := resumeSession
			resumeSession = func(string, bool, string) (*session.Session, string) {
				return &session.Session{ID: "ses_prev"}, ""
			}
			t.Cleanup(func() { resumeSession = prev })

			next, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
			if tc.changed {
				next, _ = updateMsg(next, routeDoneFrom(t, cmd))
			}

			if next.phase != phaseResume {
				t.Fatalf("phase = %v, want the resume prompt the flow was steered to", next.phase)
			}
			if want := tc.ensureOutput + tc.waitOutput; pendingRouteNotes != want {
				t.Fatalf("pendingRouteNotes = %q, want %q", pendingRouteNotes, want)
			}
			if next.status != tc.wantStatus {
				t.Fatalf("status = %q, want the last captured line %q", next.status, tc.wantStatus)
			}
		})
	}
}

// TestRouteCheckSilentLeavesNotesAndStatusAlone pins the quiet case: a check
// that printed nothing adds nothing to the notes and does not touch the status
// line, so an ordinary launch looks exactly as it did before the capture.
func TestRouteCheckSilentLeavesNotesAndStatusAlone(t *testing.T) {
	m := launchRowFixture(t, "omlx/qwen3.8")
	stub := stubEnsureRoute(t)
	stub.changed = false
	stubRouteNotes(t)
	prev := resumeSession
	resumeSession = func(string, bool, string) (*session.Session, string) {
		return &session.Session{ID: "ses_prev"}, ""
	}
	t.Cleanup(func() { resumeSession = prev })

	next, _ := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	if pendingRouteNotes != "" || next.status != "" {
		t.Fatalf("pendingRouteNotes = %q status = %q, want both empty for a silent check", pendingRouteNotes, next.status)
	}
}

// TestRouteNoteKeptBesideResumeWarning pins that a failed resume lookup's
// warning does not push the route note out of the status line: both are shown,
// joined with "; ". Either one alone would hide the other's reason from a user
// looking at the picker.
func TestRouteNoteKeptBesideResumeWarning(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}
	m := launchRowFixture(t, "omlx/qwen3.8")
	t.Cleanup(agents.RegisterTest("wt-stub", func() agents.Driver { return stubDriver{path: truePath} }))
	m.agent = "wt-stub" // a launch that succeeds, so no "launch failed" replaces the status
	stub := stubEnsureRoute(t)
	stub.ensureOutput = routeUpdatedLine
	stubRouteNotes(t)
	prev := resumeSession
	resumeSession = func(string, bool, string) (*session.Session, string) {
		return nil, "resume check failed, starting fresh: boom"
	}
	t.Cleanup(func() { resumeSession = prev })

	routing, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	next, launch := updateMsg(routing, routeDoneFrom(t, cmd))

	if launch == nil {
		t.Fatalf("no launch command; status = %q", next.status)
	}
	if want := "wt: LiteLLM route for omlx/qwen3.8 updated; resume check failed, starting fresh: boom"; next.status != want {
		t.Fatalf("status = %q, want %q", next.status, want)
	}
}

// routingFixture is a model sitting in phaseRouting for omlx/qwen3.8: Enter
// was pressed on the running row and the check reported a change. It returns
// the model, the pending command and the stub.
func routingFixture(t *testing.T) (model, tea.Cmd, *routeStub) {
	t.Helper()
	m := launchRowFixture(t, "omlx/qwen3.8")
	stub := stubEnsureRoute(t)
	stubRouteNotes(t)
	routing, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if routing.phase != phaseRouting {
		t.Fatalf("phase = %v, want phaseRouting", routing.phase)
	}
	return routing, cmd, stub
}

// TestRoutingPhaseIgnoresKeysExceptCtrlC pins the keyboard while the proxy
// restarts. The route is already written and the restart cannot be cancelled,
// so Enter, esc, q and a movement key must do nothing — esc and q would
// otherwise quit wt or pop back to a picker whose launch is about to happen,
// and a movement key would wrap the cursor under the routing screen. ctrl+c is
// the one way out, so a restart that hangs cannot trap the user.
func TestRoutingPhaseIgnoresKeysExceptCtrlC(t *testing.T) {
	m, _, stub := routingFixture(t)
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeyEsc},
		{Type: tea.KeyRunes, Runes: []rune("q")},
		{Type: tea.KeyRunes, Runes: []rune("k")},
		{Type: tea.KeyUp},
		{Type: tea.KeyDown},
	} {
		next, cmd := updateMsg(m, key)
		if cmd != nil {
			t.Errorf("key %q returned a command, want it ignored", key.String())
		}
		if next.phase != phaseRouting || next.routing != m.routing || next.status != m.status ||
			selectedModelID(next) != selectedModelID(m) || next.launchModel.ID != m.launchModel.ID {
			t.Errorf("key %q changed the model: phase = %v status = %q cursor = %s", key.String(), next.phase, next.status, selectedModelID(next))
		}
	}
	if got := strings.Join(stub.events, ","); got != "ensure:omlx/qwen3.8" {
		t.Fatalf("events = %q: a key during routing ran the check or the wait again", got)
	}

	next, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c returned no command, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("ctrl+c command returned %T, want tea.QuitMsg", cmd())
	}
	if next.routing == nil {
		t.Fatal("ctrl+c dropped the in-flight check: Run() could no longer collect its output")
	}
}

// TestStaleRouteDoneMsgIsDropped pins that only the in-flight check's own
// message launches. A message with another run id, or one arriving when no
// check is in flight, must change nothing: acting on it would launch an agent
// from a screen where the user never pressed Enter.
func TestStaleRouteDoneMsgIsDropped(t *testing.T) {
	m, _, _ := routingFixture(t)

	next, cmd := updateMsg(m, routeDoneMsg{id: m.routing.id + 1, notes: routeUpdatedLine})
	if cmd != nil || next.phase != phaseRouting || next.routing != m.routing || next.status != "" || pendingRouteNotes != "" {
		t.Fatalf("a routeDoneMsg with another id was acted on: phase = %v status = %q notes = %q", next.phase, next.status, pendingRouteNotes)
	}

	// Outside phaseRouting: the picker, with no check in flight, and again
	// with a routing state left over but another phase on screen.
	picker := launchRowFixture(t, "omlx/qwen3.8")
	for name, idle := range map[string]model{
		"no check in flight": picker,
		"another phase":      func() model { p := m; p.phase = phaseModel; return p }(),
	} {
		next, cmd = updateMsg(idle, routeDoneMsg{id: 1, notes: routeUpdatedLine})
		if cmd != nil || next.phase != phaseModel || next.status != "" || pendingRouteNotes != "" {
			t.Fatalf("%s: a routeDoneMsg outside phaseRouting was acted on: phase = %v status = %q notes = %q", name, next.phase, next.status, pendingRouteNotes)
		}
	}
}

// TestRouteTickRearmsOnlyForTheInFlightCheck pins the elapsed-time redraw: the
// tick re-arms itself while its check is in flight, so the screen keeps
// counting, and stops once the check is over or superseded — a tick that kept
// re-arming would redraw forever behind the agent.
func TestRouteTickRearmsOnlyForTheInFlightCheck(t *testing.T) {
	m, _, _ := routingFixture(t)

	if _, cmd := updateMsg(m, routeTickMsg{id: m.routing.id}); cmd == nil {
		t.Fatal("the in-flight check's tick did not re-arm: elapsed time would freeze")
	}
	if _, cmd := updateMsg(m, routeTickMsg{id: m.routing.id + 1}); cmd != nil {
		t.Fatal("a tick with another id re-armed")
	}
	done := m
	done.routing, done.phase = nil, phaseModel
	if _, cmd := updateMsg(done, routeTickMsg{id: m.routing.id}); cmd != nil {
		t.Fatal("a tick re-armed after the check finished")
	}
}

// TestRoutingViewNamesTheModelAndOffersNoCancel pins the routing screen's
// text: it says what is happening and to which model, and it must not
// advertise esc or a cancel that does not exist — the write is done and the
// restart cannot be called off, so such a hint would be a lie.
func TestRoutingViewNamesTheModelAndOffersNoCancel(t *testing.T) {
	m, _, _ := routingFixture(t)

	view := m.View()

	for _, want := range []string{"Updating the LiteLLM route for omlx/qwen3.8", "restarting the proxy", "[ctrl+c] quit wt"} {
		if !strings.Contains(view, want) {
			t.Errorf("view = %q, want it to contain %q", view, want)
		}
	}
	lower := strings.ToLower(view)
	for _, banned := range []string{"esc", "cancel"} {
		if strings.Contains(lower, banned) {
			t.Errorf("view = %q, must not mention %q", view, banned)
		}
	}

	m.width, m.height = 0, 0
	if got := m.View(); !strings.Contains(got, "waiting for window size") {
		t.Errorf("view before a window size = %q, want the same guard the other phases have", got)
	}
}

// TestQuitDuringRoutingStillFlushesTheNotes pins the ctrl+c exit: the check's
// message is never handled, so Run() has to settle the check itself — wait for
// the restart and print what the check collected. Without it the "updated"
// line and any restart warning are dropped with the buffer they were written
// to, and wt exits under a restart that is still in flight.
func TestQuitDuringRoutingStillFlushesTheNotes(t *testing.T) {
	m := launchRowFixture(t, "omlx/qwen3.8")
	stub := stubEnsureRoute(t)
	stub.ensureOutput, stub.waitOutput = routeUpdatedLine, "wt: LiteLLM proxy did not come back\n"
	stubRouteNotes(t)
	routing, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	final, _ := updateMsg(routing, tea.KeyMsg{Type: tea.KeyCtrlC})

	var out bytes.Buffer
	flushRouteNotesAfterRun(final, &out)

	notes := routeUpdatedLine + "wt: LiteLLM proxy did not come back\n"
	// The restart was still ahead, so the wait is announced before the notes.
	if want := routeWaitingLine + notes; out.String() != want {
		t.Fatalf("printed = %q, want %q", out.String(), want)
	}
	// The command may still finish afterwards (it ran concurrently with the
	// quit): it must get the same text without waiting again.
	if done := routeDoneFrom(t, cmd); done.notes != notes {
		t.Fatalf("late routing command notes = %q, want %q", done.notes, notes)
	}
	if got := strings.Join(stub.events, ","); got != "ensure:omlx/qwen3.8,wait" {
		t.Fatalf("events = %q, want one wait in total", got)
	}
	if pendingRouteNotes != "" {
		t.Fatalf("pendingRouteNotes = %q, want cleared after the flush", pendingRouteNotes)
	}
}

// TestRouteDoneAfterCtrlCDoesNotLaunch pins the quit race: tea.Quit's QuitMsg
// comes back through the message channel, so the check's routeDoneMsg can be
// handled after ctrl+c and before the program stops. It must not launch — the
// user quit, and a launch would also record rotation and a refcount entry for
// a session that never happened, and have runAndWaitCmd print the notes while
// Run() flushes them too. The agent here is one that WOULD launch, so a
// dropped guard shows up as a launch command and written state. The notes are
// still printed, exactly once, by the after-run flush — with no "waiting" line,
// because the command had already finished the wait.
func TestRouteDoneAfterCtrlCDoesNotLaunch(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}
	m := launchRowFixture(t, "omlx/qwen3.8")
	t.Cleanup(agents.RegisterTest("wt-stub", func() agents.Driver { return stubDriver{path: truePath} }))
	m.agent = "wt-stub"
	stub := stubEnsureRoute(t)
	stub.ensureOutput = routeUpdatedLine
	stubRouteNotes(t)

	routing, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	quitting, _ := updateMsg(routing, tea.KeyMsg{Type: tea.KeyCtrlC})
	if routing.routing.quitting {
		t.Fatal("ctrl+c wrote through to the model Update was given")
	}
	done := routeDoneFrom(t, cmd) // the restart finishes in the gap before QuitMsg
	final, launch := updateMsg(quitting, done)

	if launch != nil {
		t.Fatal("a routeDoneMsg after ctrl+c returned a command: the agent would launch after the user quit")
	}
	if final.phase != phaseRouting || final.routing == nil {
		t.Fatalf("phase = %v routing = %+v, want the quitting check left for Run() to settle", final.phase, final.routing)
	}
	if last, ok := rotation.New().Last(); ok || last != "" {
		t.Fatalf("rotation recorded %q for a launch that must not happen", last)
	}
	if counts := refcount.NewStore().Counts([]string{"omlx/qwen3.8"}); counts["omlx/qwen3.8"] != 0 {
		t.Fatalf("refcount = %v for a launch that must not happen", counts)
	}
	if pendingRouteNotes != "" {
		t.Fatalf("pendingRouteNotes = %q: the dropped message recorded its notes, so they would print twice", pendingRouteNotes)
	}
	if _, tick := updateMsg(quitting, routeTickMsg{id: quitting.routing.id}); tick != nil {
		t.Fatal("the tick re-armed for a check the user quit out of")
	}

	var out bytes.Buffer
	flushRouteNotesAfterRun(final, &out)

	if out.String() != routeUpdatedLine {
		t.Fatalf("printed = %q, want the notes once and no waiting line (the wait was over)", out.String())
	}
	if got := strings.Join(stub.events, ","); got != "ensure:omlx/qwen3.8,wait" {
		t.Fatalf("events = %q, want one wait", got)
	}
}

// signalWriter is a mutex-guarded buffer that closes first on its first
// write, so a test can wait for a goroutine to have printed something.
type signalWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	once  sync.Once
	first chan struct{}
}

func (w *signalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	w.once.Do(func() { close(w.first) })
	return n, err
}

func (w *signalWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestQuitDuringRoutingSettlesAcrossGoroutines runs the path the other tests
// only simulate: the routing command on its own goroutine, blocked in the
// proxy wait, while Run()'s after-run flush settles the same check from
// another. This is what really happens on ctrl+c, and it is the only test in
// which -race can see the two sides. The wait is announced while it is still
// blocked, the notes are printed exactly once, the wait and the restore each
// ran once, and both callers got the same text.
func TestQuitDuringRoutingSettlesAcrossGoroutines(t *testing.T) {
	m := launchRowFixture(t, "omlx/qwen3.8")
	stub := stubEnsureRoute(t)
	stub.ensureOutput, stub.waitOutput = routeUpdatedLine, "wt: LiteLLM proxy did not come back\n"
	stub.waitEntered, stub.waitRelease = make(chan struct{}), make(chan struct{})
	stubRouteNotes(t)
	routing, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	final, _ := updateMsg(routing, tea.KeyMsg{Type: tea.KeyCtrlC})
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("command = %T, want the routing batch", batch)
	}

	timeout := time.After(10 * time.Second)
	await := func(ch <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-ch:
		case <-timeout:
			t.Fatalf("timed out waiting for %s", what)
		}
	}

	// The routing command, as the tea runtime runs it: on its own goroutine.
	cmdDone := make(chan struct{})
	var done tea.Msg
	go func() {
		defer close(cmdDone)
		done = batch[0]()
	}()
	await(stub.waitEntered, "the routing command to reach the proxy wait")

	// Run()'s flush, concurrently, while the restart is still in flight.
	out := &signalWriter{first: make(chan struct{})}
	flushDone := make(chan struct{})
	go func() {
		defer close(flushDone)
		flushRouteNotesAfterRun(final, out)
	}()
	await(out.first, "the flush to announce the wait")
	select {
	case <-flushDone:
		t.Fatal("the flush returned while the proxy restart was still in flight")
	case <-time.After(50 * time.Millisecond):
	}

	close(stub.waitRelease)
	await(cmdDone, "the routing command")
	await(flushDone, "the flush")

	notes := routeUpdatedLine + "wt: LiteLLM proxy did not come back\n"
	if got := out.String(); got != routeWaitingLine+notes {
		t.Fatalf("printed = %q, want the waiting line and then the notes exactly once", got)
	}
	if msg, ok := done.(routeDoneMsg); !ok || msg.notes != notes {
		t.Fatalf("routing command returned %#v, want the same notes", done)
	}
	if got := strings.Join(stub.events, ","); got != "ensure:omlx/qwen3.8,wait" {
		t.Fatalf("events = %q, want exactly one wait", got)
	}
}

// TestResumePromptShowsTheRouteNote pins that the route check's note is on
// screen at the resume prompt, where a launch with a prior session stops: the
// prompt used to render only its choices, so the "updated" line or a route
// warning was invisible until the user backed out. It covers a written route
// (through the routing phase) and an unchanged write that warned, a resume
// warning shown beside the note, and esc — which clears the rest of the status
// but must keep the note on the picker, since the route was written whether or
// not the launch goes ahead.
func TestResumePromptShowsTheRouteNote(t *testing.T) {
	for _, tc := range []struct {
		name          string
		changed       bool
		output        string
		resumeWarning string
		wantNote      string
	}{
		{name: "route written", changed: true, output: routeUpdatedLine, wantNote: "wt: LiteLLM route for omlx/qwen3.8 updated"},
		{name: "route written, resume warned", changed: true, output: routeUpdatedLine, resumeWarning: "resume check was slow", wantNote: "wt: LiteLLM route for omlx/qwen3.8 updated"},
		{name: "unchanged write warned", changed: false, output: "wt: LiteLLM route not updated: boom\n", wantNote: "wt: LiteLLM route not updated: boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := launchRowFixture(t, "omlx/qwen3.8")
			stub := stubEnsureRoute(t)
			stub.changed, stub.ensureOutput = tc.changed, tc.output
			stubRouteNotes(t)
			prev := resumeSession
			resumeSession = func(string, bool, string) (*session.Session, string) {
				return &session.Session{ID: "ses_prev"}, tc.resumeWarning
			}
			t.Cleanup(func() { resumeSession = prev })

			next, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
			if tc.changed {
				next, _ = updateMsg(next, routeDoneFrom(t, cmd))
			}
			if next.phase != phaseResume {
				t.Fatalf("phase = %v, want the resume prompt", next.phase)
			}

			view := next.View()
			if !strings.Contains(view, tc.wantNote) {
				t.Errorf("resume view = %q, want it to show the route note %q", view, tc.wantNote)
			}
			if tc.resumeWarning != "" && !strings.Contains(view, tc.resumeWarning) {
				t.Errorf("resume view = %q, want the resume warning %q beside the note", view, tc.resumeWarning)
			}
			if !strings.Contains(view, "Resume previous session?") {
				t.Errorf("resume view = %q, want the prompt itself still rendered", view)
			}

			back, _ := updateMsg(next, tea.KeyMsg{Type: tea.KeyEsc})
			if back.phase != phaseModel {
				t.Fatalf("phase after esc = %v, want the picker", back.phase)
			}
			if back.status != tc.wantNote {
				t.Errorf("status after esc = %q, want only the route note %q", back.status, tc.wantNote)
			}
			if picker := back.View(); !strings.Contains(picker, tc.wantNote) {
				t.Errorf("picker view after esc = %q, want it to still show %q", picker, tc.wantNote)
			}
		})
	}
}

// TestEscFromResumePromptStillClearsOtherStatus pins the other half of the esc
// rule: with no route note, esc from the resume prompt clears the status as it
// always did, and a note from an earlier launch attempt does not come back on
// a later one that printed nothing. Stale text following the user between
// screens is the bug the clearing exists to prevent.
func TestEscFromResumePromptStillClearsOtherStatus(t *testing.T) {
	m := launchRowFixture(t, "omlx/qwen3.8")
	stub := stubEnsureRoute(t)
	stub.changed, stub.ensureOutput = false, "wt: LiteLLM route not updated: boom\n"
	stubRouteNotes(t)
	prev := resumeSession
	resumeSession = func(string, bool, string) (*session.Session, string) {
		return &session.Session{ID: "ses_prev"}, "resume check was slow"
	}
	t.Cleanup(func() { resumeSession = prev })

	first, _ := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	back, _ := updateMsg(first, tea.KeyMsg{Type: tea.KeyEsc})
	if back.status != "wt: LiteLLM route not updated: boom" {
		t.Fatalf("status after esc = %q, want the first attempt's note", back.status)
	}

	stub.ensureOutput = "" // the second attempt's check is silent
	second, _ := updateMsg(back, tea.KeyMsg{Type: tea.KeyEnter})
	if second.phase != phaseResume || second.status != "resume check was slow" {
		t.Fatalf("phase = %v status = %q, want the resume prompt with only its own warning", second.phase, second.status)
	}
	back, _ = updateMsg(second, tea.KeyMsg{Type: tea.KeyEsc})
	if back.status != "" {
		t.Fatalf("status after esc = %q, want it cleared: no route note belongs to this attempt", back.status)
	}
}

// resumePromptFixture lands on the resume prompt for omlx/qwen3.8 in a window
// of the given size, after a route check that printed output ("" for a silent
// one) without changing config.yaml. A prior session always exists.
func resumePromptFixture(t *testing.T, width, height int, output string) (model, *routeStub) {
	t.Helper()
	m := launchRowFixture(t, "omlx/qwen3.8")
	m.width, m.height = width, height
	stub := stubEnsureRoute(t)
	stub.changed, stub.ensureOutput = false, output
	stubRouteNotes(t)
	prev := resumeSession
	resumeSession = func(string, bool, string) (*session.Session, string) {
		return &session.Session{ID: "ses_prev"}, ""
	}
	t.Cleanup(func() { resumeSession = prev })
	next, _ := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if next.phase != phaseResume {
		t.Fatalf("phase = %v, want the resume prompt", next.phase)
	}
	return next, stub
}

// TestResumeViewFitsTheTerminal pins that the resume prompt never renders more
// lines than the terminal has, with or without a status line. Bubble Tea drops
// lines from the top of a view that is too tall, and the status is the top
// line: a view one line over shows the prompt and silently loses the route
// note it was changed to show. Asserting on the View() string alone cannot see
// that, so the line count is checked against the window height.
func TestResumeViewFitsTheTerminal(t *testing.T) {
	const note = "wt: LiteLLM route not updated: boom"
	for _, height := range []int{12, 24, 50} {
		for _, output := range []string{"", note + "\n"} {
			m, _ := resumePromptFixture(t, 80, height, output)

			view := m.View()

			if got := lipgloss.Height(view); got > height {
				t.Errorf("height %d, status %q: view is %d lines, want at most %d", height, m.status, got, height)
			}
			if !strings.Contains(view, "[enter] choose") {
				t.Errorf("height %d: view = %q, want the key hint", height, view)
			}
			if output != "" && !strings.HasPrefix(view, ErrorStyle(m.theme).Render(note)+"\n") {
				t.Errorf("height %d: view = %q, want the note as its first line", height, view)
			}
			if output == "" && strings.Contains(view, "wt: LiteLLM") {
				t.Errorf("height %d: view = %q, want no note for a silent check", height, view)
			}
		}
	}
}

// TestResumeViewFitsAfterResizeAndNewStatus pins the two ways the fit can
// break while the prompt is already up: the window is resized, and a status
// appears that was not there on entry (a launch from the prompt fails). In
// both the list must be re-sized, or the top line — the status — is lost.
func TestResumeViewFitsAfterResizeAndNewStatus(t *testing.T) {
	const note = "wt: LiteLLM route not updated: boom"
	m, _ := resumePromptFixture(t, 80, 24, note+"\n")
	for _, height := range []int{12, 50, 24} {
		resized, _ := updateMsg(m, tea.WindowSizeMsg{Width: 100, Height: height})
		view := resized.View()
		if got := lipgloss.Height(view); got > height {
			t.Errorf("resized to %d: view is %d lines, want at most %d", height, got, height)
		}
		if !strings.Contains(view, note) {
			t.Errorf("resized to %d: view = %q, want the note still shown", height, view)
		}
	}

	// No status on entry; "Start fresh" (the default choice) then fails to
	// launch, because the fixture's agent has no driver.
	quiet, _ := resumePromptFixture(t, 80, 24, "")
	if before := lipgloss.Height(quiet.View()); before > 24 {
		t.Fatalf("view without a status is %d lines, want at most 24", before)
	}
	failed, _ := updateMsg(quiet, tea.KeyMsg{Type: tea.KeyEnter})
	if failed.phase != phaseResume || !strings.Contains(failed.status, "launch failed") {
		t.Fatalf("phase = %v status = %q, want a failed launch left on the resume prompt", failed.phase, failed.status)
	}
	view := failed.View()
	if got := lipgloss.Height(view); got > 24 {
		t.Errorf("view with the new status is %d lines, want at most 24", got)
	}
	if !strings.Contains(view, "launch failed") {
		t.Errorf("view = %q, want the launch failure shown", view)
	}
}

// TestLaunchAttemptStartsWithAClearStatus pins that nothing an earlier launch
// attempt left in the status line shows up on a later attempt's screens. The
// route note is kept across esc from the resume prompt, and the resume prompt
// renders the status — so without a reset at the start of each attempt the
// next prompt, for a different model or for the same one, would announce a
// route update that did not happen on this launch.
func TestLaunchAttemptStartsWithAClearStatus(t *testing.T) {
	const note = "wt: LiteLLM route for omlx/qwen3.8 updated"
	for name, nextRow := range map[string]string{
		"another row":  "claude/opus",
		"the same row": "omlx/qwen3.8",
	} {
		t.Run(name, func(t *testing.T) {
			first, stub := resumePromptFixture(t, 80, 24, routeUpdatedLine)
			back, _ := updateMsg(first, tea.KeyMsg{Type: tea.KeyEsc})
			if back.status != note {
				t.Fatalf("status after esc = %q, want the first attempt's note", back.status)
			}

			// The second attempt: a silent check (or none, for the native
			// row) and no resume warning.
			stub.ensureOutput = ""
			back.models.Select(indexOfID(back, nextRow))
			second, _ := updateMsg(back, tea.KeyMsg{Type: tea.KeyEnter})

			if second.phase != phaseResume || second.launchModel.ID != nextRow {
				t.Fatalf("phase = %v launchModel = %s, want the resume prompt for %s", second.phase, second.launchModel.ID, nextRow)
			}
			if second.status != "" || second.routeNote != "" {
				t.Fatalf("status = %q routeNote = %q, want both empty: the earlier attempt's note is stale", second.status, second.routeNote)
			}
			if view := second.View(); strings.Contains(view, "wt: LiteLLM") {
				t.Fatalf("resume view = %q, want no route note from the earlier attempt", view)
			}
			again, _ := updateMsg(second, tea.KeyMsg{Type: tea.KeyEsc})
			if again.status != "" {
				t.Fatalf("status after the second esc = %q, want empty", again.status)
			}
		})
	}
}

// TestRouteDoneSizesTheModelList pins that a window size arriving during the
// routing phase reaches the picker's list when the phase ends. The model list
// is fitted only while its own phase is showing, so without a fit at the end
// of routing a failed launch after it shows a picker laid out for the old size
// — or for no size at all, when a -M pin built the list before the terminal
// reported one. The expected size is the picker's own, not the bare window's:
// 120 columns minus the two columns of padding the picker puts on each side
// of the table, and 50 lines minus the six the picker prints around the table
// (top and bottom margin, agent, tag, the mode line, the key hints) and the
// two the "launch failed" status takes.
func TestRouteDoneSizesTheModelList(t *testing.T) {
	m, cmd, _ := routingFixture(t)

	resized, _ := updateMsg(m, tea.WindowSizeMsg{Width: 120, Height: 50})
	if resized.phase != phaseRouting {
		t.Fatalf("phase = %v, want a resize to leave the routing phase alone", resized.phase)
	}
	next, _ := updateMsg(resized, routeDoneFrom(t, cmd))

	if next.phase != phaseModel || !strings.Contains(next.status, "launch failed") {
		t.Fatalf("phase = %v status = %q, want the picker after the failed launch", next.phase, next.status)
	}
	if w, h := next.models.Width(), next.models.Height(); w != 116 || h != 42 {
		t.Fatalf("model list = %dx%d, want 116x42 (120 columns minus four of padding; 50 lines minus six of chrome and two of status)", w, h)
	}
	for i, line := range strings.Split(next.View(), "\n") {
		if got := lipgloss.Width(line); got > 120 {
			t.Fatalf("picker line %d is %d columns, want at most the terminal's 120", i, got)
		}
	}
	if got := lipgloss.Height(next.View()); got != 50 {
		t.Fatalf("picker view is %d lines, want exactly the terminal's 50", got)
	}
}

// TestFlushRouteNotesAfterRunPrintsWhatIsLeft pins Run()'s flush for a launch
// that never happened (the user backed out at the resume prompt, or quit from
// the picker): notes recorded by a finished check are printed once the alt
// screen is gone, exactly once — and with no "waiting" line, since no check is
// in flight and there is nothing to wait for.
func TestFlushRouteNotesAfterRunPrintsWhatIsLeft(t *testing.T) {
	stubRouteNotes(t)
	pendingRouteNotes = routeUpdatedLine

	var out bytes.Buffer
	flushRouteNotesAfterRun(model{}, &out)
	flushRouteNotesAfterRun(nil, &out) // p.Run() can fail and return no model

	if out.String() != routeUpdatedLine {
		t.Fatalf("printed = %q, want the pending line once", out.String())
	}
}

// TestFlushRouteNotesEndsWithNewline pins that the flush always ends the line,
// so the agent's first output never continues a route warning.
func TestFlushRouteNotesEndsWithNewline(t *testing.T) {
	stubRouteNotes(t)
	pendingRouteNotes = "wt: LiteLLM route not updated: boom"

	var out bytes.Buffer
	flushRouteNotes(&out)
	flushRouteNotes(&out) // nothing pending: prints nothing

	if out.String() != "wt: LiteLLM route not updated: boom\n" {
		t.Fatalf("printed = %q", out.String())
	}
}

// TestRunAndWaitCmdPrintsRouteNotesAboveTheAgent pins where the captured route
// output goes on a launch: to stderr once the terminal is released, before the
// agent prints anything, and only once. Printed any later it would be buried
// in or after the agent's session; not at all, and a failed proxy restart
// would be invisible from the picker.
func TestRunAndWaitCmdPrintsRouteNotesAboveTheAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh differs on windows")
	}
	stubRouteNotes(t)
	prevSummary, prevSurvey := pendingSummary, pendingSurveyState
	t.Cleanup(func() { pendingSummary, pendingSurveyState = prevSummary, prevSurvey })
	pendingRouteNotes = routeUpdatedLine

	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	// Registered before the swap, so even a panic inside runAndWaitCmd cannot
	// leave the test process's stderr pointing at a closed pipe.
	t.Cleanup(func() { os.Stderr = old })
	os.Stderr = w
	msg := runAndWaitCmd(exec.Command("sh", "-c", "echo agent-output >&2"), "shell", config.Model{})()
	w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)

	if done, ok := msg.(launchDoneMsg); !ok || done.err != nil {
		t.Fatalf("msg = %#v, want a clean launchDoneMsg", msg)
	}
	if want := routeUpdatedLine + "agent-output\n"; string(out) != want {
		t.Fatalf("stderr = %q, want %q", string(out), want)
	}
	if pendingRouteNotes != "" {
		t.Fatalf("pendingRouteNotes = %q, want cleared so Run() does not print it again", pendingRouteNotes)
	}
}

// TestLaunchOfNativeRowSkipsEnsureRoute pins that a model that does not go
// through LiteLLM (a native one never does) leaves config.yaml alone: no
// check and no routing phase.
func TestLaunchOfNativeRowSkipsEnsureRoute(t *testing.T) {
	m := launchRowFixture(t, "claude/opus")
	stub := stubEnsureRoute(t)

	next, _ := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(stub.events) != 0 {
		t.Fatalf("events = %v, want none for a native model", stub.events)
	}
	if next.phase == phaseRouting {
		t.Fatalf("phase = %v, want no routing phase", next.phase)
	}
	// Without this the test would pass on an Enter that never reached
	// proceedToLaunch at all.
	if !strings.Contains(next.status, "launch failed") {
		t.Fatalf("status = %q, want the launch to have been attempted", next.status)
	}
}

// TestLaunchAfterStartSkipsEnsureRoute pins that a row wt just started is not
// checked again: the start hook wrote its route, and finishStart already
// waited for the proxy. The only wait recorded is finishStart's, and the
// routing phase is never entered.
func TestLaunchAfterStartSkipsEnsureRoute(t *testing.T) {
	m := startFixture(t, "ollama", "ollama/gemma4:9b", "gemma4:9b")
	m.cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-test"})
	stubStartModel(t, func(int, context.Context, lifecycle.Target, lifecycle.Options) error { return nil })
	m.agent = "not-a-real-agent"
	stub := stubEnsureRoute(t)

	got, _ := enterStartRow(t, m, "ollama/gemma4:9b")
	next, _ := updateMsg(got, recvStart(t, got))

	if joined := strings.Join(stub.events, ","); joined != "wait" {
		t.Fatalf("events = %q, want only finishStart's wait", joined)
	}
	if next.phase == phaseRouting {
		t.Fatalf("phase = %v, want no routing phase", next.phase)
	}
	if !strings.Contains(next.status, "launch failed") {
		t.Fatalf("status = %q, want the launch to have been attempted", next.status)
	}
}
