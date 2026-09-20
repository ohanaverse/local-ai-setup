package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
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
			{ID: provider, Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
		},
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Family: "opus", Tags: []string{"code"}},
			{ID: id, ProviderID: provider, ModelName: name, Family: name, Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"claude", provider}}},
	}
	cfg.ExposeAllForTest()
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
	if calls.len() != 1 {
		t.Fatalf("startModel calls = %d, want 1", calls.len())
	}
	c := calls.at(0)
	if c.target != (lifecycle.Target{ProviderID: "ollama", ModelName: "gemma4:9b"}) {
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
	if calls.len() != 2 {
		t.Fatalf("startModel calls = %d, want 2", calls.len())
	}
	if !calls.at(1).opts.AllowReplace {
		t.Error("second call must have AllowReplace == true")
	}
	c := calls.at(1)
	if c.target != (lifecycle.Target{ProviderID: "omlx", ModelName: "qwen3.8"}) {
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

// TestKeysDuringStartCancelThenQuit verifies esc, q and ctrl+c all CANCEL an
// in-flight start (the context is cancelled, the view shows Cancelling, and
// the engine's done message returns to the picker with status "cancelled"),
// and a further press while cancelling quits the program — the user must
// always be able to back out, but a quit needs a second deliberate press so
// a stray key cannot orphan a half-started server.
func TestKeysDuringStartCancelThenQuit(t *testing.T) {
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
			if calls.len() != 1 {
				t.Errorf("startModel calls = %d, want 1 (cancel does not re-issue)", calls.len())
			}

			// A further press while cancelling quits the program.
			_, quitCmd := updateMsg(got, key)
			if quitCmd == nil {
				t.Fatalf("second %s press returned a nil cmd, want tea.Quit", name)
			}
			if msg := quitCmd(); msg != (tea.QuitMsg{}) {
				t.Errorf("second press cmd message = %T, want tea.QuitMsg", msg)
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

// TestStartErrorsMapToMessages verifies each engine failure produces the
// status line the spec promises: a down daemon names the origin, the
// engine's own typed errors are shown verbatim, and anything else is
// prefixed with the model — a user must be able to tell what to fix.
func TestStartErrorsMapToMessages(t *testing.T) {
	cases := []struct {
		name string
		err  error
		id   string
		want []string // substrings the status must contain
	}{
		{"daemon down", &lifecycle.DaemonDownError{Provider: "omlx", Origin: "http://localhost:8000"}, "omlx/q", []string{"is not answering at", "http://localhost:8000"}},
		{"binary missing", &lifecycle.BinaryMissingError{Binary: "omlx"}, "omlx/q", []string{"omlx"}},
		{"port busy", &lifecycle.PortBusyError{Port: 8000}, "omlx/q", []string{"8000"}},
		{"generic", errors.New("boom"), "omlx/q", []string{"failed to start omlx/q: boom"}},
	}
	for _, tc := range cases {
		got := startErrorMessage(tc.id, tc.err)
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s: status = %q, want it to contain %q", tc.name, got, want)
			}
		}
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
// Enter begins a start, and that success proceeds to launch. This is the
// behavior the whole plan exists for.
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
	if calls.len() != 1 || !calls.at(0).opts.AllowReplace == true {
		if calls.len() != 1 {
			t.Errorf("startModel calls = %d, want 1", calls.len())
		}
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
	if calls.len() != 1 {
		t.Errorf("startModel calls = %d, want 1", calls.len())
	}
}

// TestEndToEndAbsentRowIsBlocked verifies an absent omlx row carries the
// "not on disk" hint, is not a start row, and Enter shows the status without
// calling the engine — starting a model that is not there would just wait
// out the warmup timeout.
func TestEndToEndAbsentRowIsBlocked(t *testing.T) {
	stubInventory(t, localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", ModelID: "omlx/qwen3.8", Registered: true, ArtifactKnown: true},
	}})
	calls := stubStartModel(t, func(call int, ctx context.Context, target lifecycle.Target, opts lifecycle.Options) error {
		t.Error("startModel must not be called for an absent row")
		return errors.New("must not start")
	})

	got := flowEnter(t, model{cfg: startCfg("omlx", "omlx/qwen3.8", "qwen3.8"), agent: "claude", selectedPath: t.TempDir(), width: 80, height: 24}, "claude")
	idx := indexOfID(got, "omlx/qwen3.8")
	if idx < 0 {
		t.Fatalf("no omlx row in %v", itemIDs(got))
	}
	it := got.models.Items()[idx].(*modelItem)
	if it.start {
		t.Error("absent row must not be a start row")
	}
	if !strings.Contains(it.blocked, "not on disk") {
		t.Errorf("blocked = %q, want the not-on-disk reason", it.blocked)
	}

	got.models.Select(idx)
	next, _ := got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got = next.(model)
	if got.phase != phaseModel {
		t.Errorf("phase = %v, want phaseModel (stays on the picker)", got.phase)
	}
	if !strings.Contains(got.status, "not on disk") {
		t.Errorf("status = %q, want the not-on-disk reason", got.status)
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
		Providers:  []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models:     []config.Model{{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8", Family: "qwen3.8", Tags: []string{"code"}}},
		Agents:     []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	local.ExposeAllForTest()
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
	if calls.len() != 0 {
		t.Errorf("startModel calls = %d, want 0", calls.len())
	}
}
