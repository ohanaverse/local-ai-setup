package tui

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/ollamacheck"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// testConfig returns a Config with one agent (claude, supporting the
// ollama provider) and a two-model code tag group plus one design model.
// Tests across the picker, rotation, and launch need a real catalog so
// the agent filter, tag filter, and rotation all have something to
// operate on.
func testConfig() *config.Config {
	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal, Protocols: []config.Protocol{config.ProtocolAnthropic, config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}},
		},
		Models: []config.Model{
			{ID: "ollama/gemma4:9b", ModelName: "gemma4:9b", ProviderID: "ollama", Tags: []string{"code"}},
			{ID: "ollama/gemma4:14b", ModelName: "gemma4:14b", ProviderID: "ollama", Tags: []string{"code"}},
			{ID: "ollama/gemma4:design", ModelName: "gemma4:design", ProviderID: "ollama", Tags: []string{"design"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"ollama"}},
		},
	}
	return cfg
}

// TestInitLoadsEntries asserts Init starts background worktree enumeration.
// Without this command, the TUI would sit forever at the loading screen.
func TestInitLoadsEntries(t *testing.T) {
	m := model{status: "loading worktrees..."}
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init returned nil cmd; expected loadPickerCmd")
	}
}

// TestUpdateWindowSizeMsg asserts the model records the terminal's reported
// dimensions on a WindowSizeMsg. Without this, the list would lay out at zero
// size and render nothing usable.
func TestUpdateWindowSizeMsg(t *testing.T) {
	m := model{status: "ready"}
	got, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.width != 80 || gotModel.height != 24 {
		t.Errorf("dimensions = (%d, %d), want (80, 24)", gotModel.width, gotModel.height)
	}
}

// TestUpdateQuitKeys asserts that q, esc, and ctrl+c all return tea.Quit.
// These three are the universal TUI exit affordances; if any one is missing
// the user can get stuck in the alternate screen with no way back to the
// shell.
func TestUpdateQuitKeys(t *testing.T) {
	cases := []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
		{Type: tea.KeyEsc},
		{Type: tea.KeyCtrlC},
	}
	for _, c := range cases {
		m := model{status: "ready"}
		_, cmd := m.Update(c)
		if cmd == nil {
			t.Errorf("key %q: got nil cmd, want tea.Quit", c.String())
		}
	}
}

// TestEscFromAgentPickerClearsStatus verifies that pressing Esc from
// phaseAgent (after e.g. an invalid pinned-model error set m.status) clears
// the status before transitioning back to phaseList. Without this, the
// stale error text follows the user back to the worktree list and there is
// no way to dismiss it — a stranded user has only Ctrl+C.
func TestEscFromAgentPickerClearsStatus(t *testing.T) {
	m := model{phase: phaseAgent, status: "model \"ollama/missing\" is not in the eligible list for agent \"claude\""}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase != phaseList {
		t.Errorf("phase = %d, want phaseList (%d)", gotModel.phase, phaseList)
	}
	if gotModel.status != "" {
		t.Errorf("status = %q, want empty (Esc must clear stale errors before transitioning)", gotModel.status)
	}
}

// TestInvalidPinFromProceedFromSelectedPathRoutesToAgentPicker verifies
// that when --agent is pinned via CLI (-A) and -M is also pinned but the
// model is not in the agent's eligible list, proceedFromSelectedPath routes
// to phaseAgent rather than leaving the user stranded on phaseList. The
// pre-fix behavior was: m.phase stayed at whatever the caller had (phaseList
// after a worktree pick), m.status showed the error, and the user had no
// way to recover except Ctrl+C.
func TestInvalidPinFromProceedFromSelectedPathRoutesToAgentPicker(t *testing.T) {
	requireBinary(t, "claude")
	tempStateDir(t)
	cfg := testConfig()
	m := model{cfg: cfg, initialAgent: "claude", pinnedModel: "ollama/missing", phase: phaseList, width: 80, height: 24}
	got, _ := m.proceedFromSelectedPath()
	if got.phase != phaseAgent {
		t.Errorf("phase = %d, want phaseAgent (%d) so the user can pick a different agent", got.phase, phaseAgent)
	}
	if !strings.Contains(got.status, "not in the eligible list") {
		t.Errorf("status = %q, want it to contain \"not in the eligible list\"", got.status)
	}
	if got.pinnedModel != "" {
		t.Errorf("pinnedModel = %q, want empty (clear the bad pin so re-entry validates fresh)", got.pinnedModel)
	}
}

// TestUpdateOtherKeyIgnored asserts that pressing a non-quit key returns nil
// (no quit) and leaves state untouched while the list is not yet ready. If
// unknown keys accidentally quit or mutate state, behavior is
// non-deterministic across terminals.
func TestUpdateOtherKeyIgnored(t *testing.T) {
	m := model{status: "ready", width: 80, height: 24}
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd != nil {
		t.Errorf("key 'x': got non-nil cmd, want nil")
	}
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.status != "ready" || gotModel.width != 80 || gotModel.height != 24 {
		t.Errorf("state mutated by non-quit key: %+v", gotModel)
	}
}

// TestViewLoading asserts View shows a loading message while worktrees are
// being enumerated. This is the first feedback the user sees after launching.
func TestViewLoading(t *testing.T) {
	m := model{status: "loading worktrees..."}
	view := m.View()
	if !strings.Contains(view, "loading worktrees") {
		t.Errorf("View missing loading message: %q", view)
	}
}

// TestViewNotReady asserts View renders the status string when the list is
// not ready. This covers error states and any pre-load messages.
func TestViewNotReady(t *testing.T) {
	m := model{status: "ready"}
	view := m.View()
	if !strings.Contains(view, "ready") {
		t.Errorf("View missing status %q: %q", "ready", view)
	}
}

// TestViewBeforeWindowSizeDoesNotPanic asserts View is safe to call when no
// WindowSizeMsg has been received yet. The model's width/height start at
// zero; the simple string views must not panic.
func TestViewBeforeWindowSizeDoesNotPanic(t *testing.T) {
	m := model{status: "ready"}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("View() panicked with no WindowSizeMsg: %v", r)
		}
	}()
	_ = m.View()
}

// TestRowsLoadedMsgBuildsTheList asserts a picker load builds the row list in
// launch mode and marks the model ready: "+ New worktree…" first, then the
// report's rows. Without this the TUI would never leave the loading screen.
func TestRowsLoadedMsgBuildsTheList(t *testing.T) {
	m := pickerReady(t, pickerFixture())
	if !m.rows.launch() {
		t.Error("the picker's list is not in launch mode")
	}
	if !m.rows.items[0].newWorktree {
		t.Errorf("items[0] = %+v, want the new-worktree item", m.rows.items[0])
	}
	if m.rows.rows != len(pickerFixture().Rows) {
		t.Errorf("rows = %d, want %d", m.rows.rows, len(pickerFixture().Rows))
	}
}

// TestViewReady asserts the rendered picker shows the list's title line and
// its rows once loaded.
func TestViewReady(t *testing.T) {
	m := pickerReady(t, pickerFixture())
	view := m.View()
	for _, want := range []string{"enter launch", "WORKTREES", "loose"} {
		if !strings.Contains(view, want) {
			t.Errorf("View missing %q:\n%s", want, view)
		}
	}
}

// TestRowsLoadedCursorOnRootWithNoDefaultBranch is the regression guard for
// a repository whose default branch cannot be determined: the cursor still
// lands on the checkout the user stands in (the root here), not on
// "+ New worktree…", so a bare Enter relaunches there.
func TestRowsLoadedCursorOnRootWithNoDefaultBranch(t *testing.T) {
	rep := pickerFixture()
	rep.Base = cleanup.Base{}
	rep.Rows = rep.Rows[:1]
	rep.Rows[0].Branch, rep.Rows[0].Name = "work", "work"
	m := pickerReady(t, rep)
	if row, ok := m.rows.selectedRow(); !ok || row.Name != "work" {
		t.Errorf("cursor on %+v (ok %v), want the root on work", row, ok)
	}
}

// TestEnterSelectsEntry asserts that pressing Enter on a picker row emits a
// selectedEntryMsg carrying that row's worktree. This is the primary
// selection affordance for the worktree picker.
func TestEnterSelectsEntry(t *testing.T) {
	m := pickerCursorTo(t, pickerReady(t, pickerFixture()), "other")
	_, sel := pickerEnter(t, m)
	if sel.entry.Branch != "other" || sel.entry.Path != "/repo/.worktrees/other" {
		t.Errorf("selected %+v, want the other worktree", sel.entry)
	}
}

// TestEnterOnHeadingIsIgnored asserts that Enter on a group heading does
// nothing rather than forwarding a zero-value row: its entry would launch
// the agent in wt's CWD with cmd.Dir="". The cursor never stops on a heading
// by itself; this pins the guard should it get there.
func TestEnterOnHeadingIsIgnored(t *testing.T) {
	m := pickerReady(t, pickerFixture())
	m.rows.cursor = 1
	if m.rows.items[1].heading == "" {
		t.Fatal("item 1 is not a heading")
	}
	m, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != phaseList {
		t.Fatalf("phase = %v, want phaseList", m.phase)
	}
	if cmd != nil {
		if msg := cmd(); msg != nil {
			t.Errorf("heading Enter produced %T, want nothing", msg)
		}
	}
}

// TestSelectedEntryMsgTransitionsToAgentPhase asserts that choosing a
// worktree moves the TUI into the agent+command picker phase, with the
// agent list populated and selectedPath recorded. The model-tag resolution
// and list build used to happen here, but moved to phaseAgent's Enter
// handler in PR 2 — full transition tests for that handler are added in
// the follow-up task.
func TestSelectedEntryMsgTransitionsToAgentPhase(t *testing.T) {
	tempStateDir(t) // isolate XDG_CONFIG_HOME so rotation state is fresh
	m := model{cfg: testConfig(), width: 80, height: 24}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature", Path: "/tmp/feature"}})
	gotModel := got.(model)
	if gotModel.phase != phaseAgent {
		t.Errorf("phase = %v, want phaseAgent", gotModel.phase)
	}
	if gotModel.selectedPath != "/tmp/feature" {
		t.Errorf("selectedPath = %q, want /tmp/feature", gotModel.selectedPath)
	}
	if len(gotModel.agentList.Items()) == 0 {
		t.Error("agentList is empty; expected at least one configured agent")
	}
}

// TestSelectedEntryMsgBareBranchPlansTheCreation asserts that a bare-branch
// pick binds the creation it owes — a read-only plan whose path the agent
// will launch in, rather than wt's CWD (cmd.Dir="") — and proceeds to the
// agent picker, creating nothing yet: the commit point's launch gate makes
// the worktree only once the person continues, so a declined gate creates
// nothing.
func TestSelectedEntryMsgBareBranchPlansTheCreation(t *testing.T) {
	tempStateDir(t) // isolate XDG_CONFIG_HOME so rotation state is fresh
	root := t.TempDir()
	gitInit(t, root)
	if out, err := exec.Command("git", "-C", root, "branch", "feature").CombinedOutput(); err != nil {
		t.Fatalf("git branch feature: %v\n%s", err, out)
	}
	m := model{cfg: testConfig(), width: 80, height: 24, repoRoot: root}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Type: worktree.TypeBranch, Branch: "feature"}})
	gotModel := got.(model)
	if gotModel.creation == nil {
		t.Fatal("creation = nil; the pick owes a worktree and must carry its plan")
	}
	if gotModel.creation.name != "feature" {
		t.Errorf("creation.name = %q, want feature", gotModel.creation.name)
	}
	if gotModel.selectedPath != gotModel.creation.plan.Path() {
		t.Errorf("selectedPath = %q, want the plan path %q", gotModel.selectedPath, gotModel.creation.plan.Path())
	}
	if gotModel.phase != phaseAgent {
		t.Errorf("phase = %v, want phaseAgent", gotModel.phase)
	}
	if _, err := os.Stat(gotModel.selectedPath); err == nil {
		t.Errorf("%s exists at pick time; the creation belongs to the commit point", gotModel.selectedPath)
	}
}

// TestSelectedEntryMsgBareBranchResolveErrorReturnsToList asserts that a
// bare-branch pick whose plan cannot be resolved surfaces the error and
// returns to the list. A silent failure would leave the user on a stale
// screen with no feedback.
func TestSelectedEntryMsgBareBranchResolveErrorReturnsToList(t *testing.T) {
	m := model{phase: phaseList, width: 80, height: 24, repoRoot: t.TempDir()} // not a git repo
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Type: worktree.TypeBranch, Branch: "feature"}})
	gotModel := got.(model)
	if gotModel.phase != phaseList {
		t.Errorf("phase = %v, want phaseList", gotModel.phase)
	}
	if gotModel.listError == "" {
		t.Error("listError = empty, want the resolve error")
	}
}

// TestToggleTagSwitchesGroup was removed in PR 3b Task 3 along with
// the `d` key handler. The cross-tag switch is no longer a TUI
// keybinding; tag filtering happens at launch via the -T flag
// (threaded through tui.Run in Task 4, exercised by
// TestLaunchFilteredUsesEligibleAndSlot in cmd/wt/launch_test.go) and
// the resulting picker list is covered by TestPhaseModelHonorsFilters
// in agent_picker_test.go.

// (TestViewModelPhase was rewritten in agent_model_test.go using the
// new picker-based View.)

// ollamaCheckConfig is a registry with one direct-route ollama model for the
// claude agent, whose ollama provider row has the given base_url.
func ollamaCheckConfig(baseURL string) *config.Config {
	return &config.Config{
		DefaultTag: "code",
		Providers:  []config.Provider{{ID: "ollama", Location: config.LocationLocal, Protocols: []config.Protocol{config.ProtocolAnthropic, config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: baseURL}}},
		Models: []config.Model{
			{ID: "ollama/test-model-xyz-not-real", ModelName: "test-model-xyz-not-real", ProviderID: "ollama", Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
	}
}

// ollamaCheckPicker is the model picker on ollamaCheckConfig's one model,
// with usage (seam) and rotation (XDG) state isolated: phaseModelWithList
// reads both via config.Dir(), so without isolation the fixture's cursor and
// counts depend on the host's real state files.
func ollamaCheckPicker(t *testing.T, baseURL string) model {
	t.Helper()
	tempStateDir(t)
	stubUsageStore(t)
	m := phaseModelWithList(t, ollamaCheckConfig(baseURL), "claude", "code")
	m.selectedPath = "/repo"
	return m
}

// TestOllamaWarnShownWhenUnavailable asserts that when the highlighted model
// is an ollama model its daemon does not list, Enter moves to phaseOllamaWarn
// — and that the daemon asked is the one the registry's ollama provider row
// names. The list is stubbed (#317): the test used to pass only because the
// developer's own ollama happened not to have the model, and failed whenever
// `ollama list` exited non-zero there.
func TestOllamaWarnShownWhenUnavailable(t *testing.T) {
	m := ollamaCheckPicker(t, "http://127.0.0.1:9/v1")
	origins := stubOllamaList(t, "some-other-model")

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := newM.(model)

	if mm.phase != phaseOllamaWarn {
		t.Fatalf("expected phaseOllamaWarn, got %d (status %q)", mm.phase, mm.status)
	}
	if !strings.Contains(mm.ollamaWarnModel.Title, "test-model-xyz-not-real") {
		t.Fatalf("expected title to contain model name, got %q", mm.ollamaWarnModel.Title)
	}
	if len(*origins) != 1 || (*origins)[0] != "http://127.0.0.1:9" {
		t.Errorf("ollama list was pinned to %v, want [http://127.0.0.1:9], the registry row's daemon", *origins)
	}
}

// TestNoOllamaWarnWhenModelIsListed is the mirror of
// TestOllamaWarnShownWhenUnavailable: the daemon lists the model, so Enter
// shows no warning and goes on to the launch of that model. Without it the
// "available" answer of the check had no test in this package.
func TestNoOllamaWarnWhenModelIsListed(t *testing.T) {
	m := ollamaCheckPicker(t, "http://127.0.0.1:9/v1")
	stubOllamaList(t, "some-other-model", "test-model-xyz-not-real")

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := newM.(model)

	if mm.phase == phaseOllamaWarn {
		t.Fatal("expected no ollama warn for a model the daemon lists")
	}
	if strings.Contains(mm.status, "ollama check failed") {
		t.Errorf("status = %q, want no failed check", mm.status)
	}
	if mm.launchModel.ID != "ollama/test-model-xyz-not-real" {
		t.Errorf("launchModel = %q, want the highlighted model on its way to launch", mm.launchModel.ID)
	}
}

// TestOllamaCheckFailureStaysOnThePicker pins what Enter does when the check
// cannot tell: the picker stays up with the reason as its status, and nothing
// is launched and no "not available" warning is shown. Three ways to get
// there: `ollama list` failed (the daemon is down), the registry's ollama
// row names no daemon to ask (a path, or an address with no scheme) — where
// no command may run at all, since ollama would fall back to its default
// daemon — and this package's TestMain
// default, which is what keeps a test that forgot to stub the list from
// running the developer's ollama. The status must fit every terminal size,
// and at 80 columns and wider it must be readable through to what to do: the
// view cuts a status line at the terminal's width, and the first wording of
// the no-daemon reason lost its value and its remedy there.
func TestOllamaCheckFailureStaysOnThePicker(t *testing.T) {
	failing := func(t *testing.T) *[]string {
		var origins []string
		t.Cleanup(ollamacheck.StubListForTest(func(origin string) ([]string, bool, error) {
			origins = append(origins, origin)
			return nil, true, errors.New("exit status 1")
		}))
		return &origins
	}
	for _, tc := range []struct {
		name, baseURL string
		stub          func(*testing.T) *[]string
		want          string // in the status
		at80          string // on screen, uncut, at 80 columns and wider
		calls         int
	}{
		{"list failed", "http://127.0.0.1:9/v1", failing, "ollama check failed: ollama list: exit status 1",
			"ollama check failed: ollama list: exit status 1", 1},
		{"base_url is a path", "/v1", failing, "ollama check failed: ollama row's base_url must be http://host:port",
			`ollama check failed: ollama row's base_url must be http://host:port, not ""`, 0},
		{"base_url has no scheme", "localhost:11434", failing, `must be http://host:port, not "localhost:11434"`,
			"ollama check failed: ollama row's base_url must be http://host:port, not ", 0},
		{"unstubbed", "http://127.0.0.1:9/v1", nil, "ollamacheck list not stubbed in this test",
			"ollama check failed: ollama list: ollamacheck list not stubbed in this test", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := ollamaCheckPicker(t, tc.baseURL)
			origins := &[]string{}
			if tc.stub != nil {
				origins = tc.stub(t)
			}

			newM, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			mm := newM.(model)

			if mm.phase != phaseModel {
				t.Errorf("phase = %d, want the model picker", mm.phase)
			}
			if !strings.Contains(mm.status, tc.want) {
				t.Errorf("status = %q, want it to contain %q", mm.status, tc.want)
			}
			if cmd != nil || mm.launchModel.ID != "" {
				t.Errorf("a launch was begun (cmd %v, launchModel %q); want none", cmd != nil, mm.launchModel.ID)
			}
			if len(*origins) != tc.calls {
				t.Errorf("ollama list ran %d times (%v), want %d", len(*origins), *origins, tc.calls)
			}
			for _, width := range layoutWidths {
				for _, height := range layoutHeights {
					sized := resized(t, mm, width, height)
					view := sized.View()
					assertFits(t, tc.name, view, width, height)
					if !strings.Contains(view, "ollama check failed") {
						t.Errorf("%s at %dx%d: the status is not on screen:\n%s", tc.name, width, height, view)
					}
					if width >= 80 && !strings.Contains(view, tc.at80) {
						t.Errorf("%s at %dx%d: the status is cut before the end of %q:\n%s", tc.name, width, height, tc.at80, view)
					}
				}
			}
		})
	}
}

// TestNoOllamaWarnForNonOllamaModel asserts that non-ollama models skip the
// availability check and proceed directly to launch.
func TestNoOllamaWarnForNonOllamaModel(t *testing.T) {
	requireBinary(t, "claude")
	cfg := &config.Config{
		DefaultTag: "code",
		// claude×openrouter has no protocol overlap, so the launch resolves to a
		// forced-litellm route; a gateway URL is required for it to succeed.
		Providers: []config.Provider{{ID: "openrouter", Auth: config.AuthConfig{Type: "secret_ref", BaseURL: "https://openrouter.ai/api/v1", SecretRef: "sk-or"}}},
		Models: []config.Model{
			{ID: "openrouter/gpt-4", ProviderID: "openrouter", ModelName: "gpt-4", Tags: []string{"code"}},
		},
	}
	cfg.SetLitellmForTest(config.LitellmState{URL: "http://localhost:4000", APIKey: "sk-litellm"})
	m := model{cfg: cfg, phase: phaseModel, width: 80, height: 24, agent: "claude", tag: "code", selectedPath: "/repo",
		models: singleModelList(cfg.Models[0])}

	newM, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("enter")})
	mm := newM.(model)

	if mm.phase == phaseOllamaWarn {
		t.Fatal("expected no ollama warn for non-ollama model")
	}
	// Should have produced the launch command.
	if cmd == nil {
		t.Fatal("expected a command from enter on non-ollama model")
	}
}

// TestNoOllamaWarnInLitellmMode asserts that the ollama availability check is
// skipped when the gateway is in litellm mode. In gateway mode models may be
// served by any upstream behind LiteLLM, so a model absent from `ollama list`
// must not trigger the unavailable-model warning.
func TestNoOllamaWarnInLitellmMode(t *testing.T) {
	// Isolate usage (seam) and rotation (XDG) state, as in
	// TestOllamaWarnShownWhenUnavailable.
	tempStateDir(t)
	stubUsageStore(t)
	cfg := ollamaCheckConfig("http://localhost:11434")
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-litellm"})
	m := phaseModelWithList(t, cfg, "claude", "code")
	m.selectedPath = "/repo"

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := newM.(model)

	if mm.phase == phaseOllamaWarn {
		t.Fatal("expected no ollama warn in litellm mode")
	}
}

// TestOllamaWarnCancel returns to phaseModel.
func TestOllamaWarnCancel(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Models: []config.Model{
			{ID: "ollama/test-model-xyz-not-real", ModelName: "test-model-xyz-not-real", ProviderID: "ollama", Tags: []string{"code"}},
		},
	}
	m := model{cfg: cfg, phase: phaseOllamaWarn, width: 80, height: 24, agent: "claude", tag: "code", selectedPath: "/repo"}
	// Build a stub picker with the unavailable model as the highlighted
	// item, so ollamaWarn has a model to reference if proceedToLaunch
	// is ever called from this test.
	items := []list.Item{modelItem{model: cfg.Models[0]}}
	m.models = list.New(items, list.NewDefaultDelegate(), 78, 22)
	m.ollamaWarnModel = list.New(buildOllamaChoices(), list.NewDefaultDelegate(), 78, 22)

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("esc")})
	mm := newM.(model)

	if mm.phase != phaseModel {
		t.Fatalf("expected phaseModel after cancel, got %d", mm.phase)
	}
}

// TestOllamaWarnHasProceedAndCancelOnly asserts the ollama
// availability warning has two choices: proceed and cancel.
// The "skip" choice (which rotated to the next model) is gone
// because rotation now advances via rotation.RecordFor at the
// launchAndRecord commit point, not on Enter.
func TestOllamaWarnHasProceedAndCancelOnly(t *testing.T) {
	choices := buildOllamaChoices()
	if len(choices) != 2 {
		t.Fatalf("ollama choices = %d, want 2 (proceed + cancel)", len(choices))
	}
	// Verify the two surviving choices are the ones we expect, and
	// no entry references the removed "skip" wording.
	for _, c := range choices {
		oi, ok := c.(choiceItem)
		if !ok {
			t.Fatalf("choice is %T, want choiceItem", c)
		}
		if oi.choice == ollamaProceedChoice {
			if oi.title != "Proceed anyway" {
				t.Errorf("proceed title = %q, want %q", oi.title, "Proceed anyway")
			}
		} else if oi.choice == ollamaCancelChoice {
			if oi.title != "Cancel" {
				t.Errorf("cancel title = %q, want %q", oi.title, "Cancel")
			}
		} else {
			t.Errorf("unexpected choice %v (%q)", oi.choice, oi.title)
		}
		if strings.Contains(strings.ToLower(oi.title), "skip") {
			t.Errorf("choice title %q still mentions 'skip'", oi.title)
		}
	}
}

// TestNKeyOpensNewWorktreePhase asserts pressing `n` on the picker
// transitions to phaseNewWorktree. Without this wiring, the keyboard
// shortcut is dead and only the "+ New worktree…" item is reachable.
func TestNKeyOpensNewWorktreePhase(t *testing.T) {
	m := pickerReady(t, pickerFixture())
	m, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = drainCmds(t, m, cmd)
	if m.phase != phaseNewWorktree {
		t.Errorf("phase = %v, want phaseNewWorktree", m.phase)
	}
}

// TestNKeyIgnoredWhileLoading asserts `n` does not transition
// before the list is ready. A user mashing `n` while the picker is
// still loading would otherwise jump into an empty prompt and
// have to esc out.
func TestNKeyIgnoredWhileLoading(t *testing.T) {
	m := model{phase: phaseList, ready: false, width: 80, height: 24}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase == phaseNewWorktree {
		t.Errorf("phase = phaseNewWorktree, want phaseList (loading not complete)")
	}
}

// TestEnterOnSentinelOpensNewWorktreePhase asserts Enter on
// "+ New worktree…" transitions to phaseNewWorktree. The item must be
// Enter-able, not just `n`-able.
func TestEnterOnSentinelOpensNewWorktreePhase(t *testing.T) {
	m := pickerReady(t, pickerFixture())
	m.rows.cursor = 0
	m, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if m.phase != phaseNewWorktree {
		t.Errorf("phase = %v, want phaseNewWorktree", m.phase)
	}
}

// TestEnterOnSentinelIgnoredWhileLoading asserts Enter does nothing before
// the picker is ready. Consistent with the `n` keypress guard.
func TestEnterOnSentinelIgnoredWhileLoading(t *testing.T) {
	m := model{phase: phaseList, ready: false, width: 80, height: 24}
	got, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got.phase == phaseNewWorktree {
		t.Errorf("phase = phaseNewWorktree, want phaseList (loading not complete)")
	}
	if cmd != nil {
		t.Errorf("Enter while loading returned a command")
	}
}

// TestEscOnNewWorktreeReturnsToList asserts pressing esc on the
// new-worktree prompt returns to the picker. Without this, the
// user is stuck in the prompt with no way out.
func TestEscOnNewWorktreeReturnsToList(t *testing.T) {
	m := model{
		phase:    phaseNewWorktree,
		width:    80,
		height:   24,
		newInput: newInputModel(80),
		newError: "previous error",
	}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase != phaseList {
		t.Errorf("phase = %v, want phaseList", gotModel.phase)
	}
	if gotModel.newError != "" {
		t.Errorf("newError = %q, want empty (esc clears it)", gotModel.newError)
	}
}

// TestEnterOnNewWorktreeEmptyNameKeepsPhase asserts an empty name
// does not dispatch a create command. The user must see the inline
// error and try again; an empty-name dispatch would race a
// `worktree add` for a malformed path.
func TestEnterOnNewWorktreeEmptyNameKeepsPhase(t *testing.T) {
	m := model{
		phase:    phaseNewWorktree,
		width:    80,
		height:   24,
		repoRoot: "/tmp/repo",
		newInput: newInputModel(80),
	}
	// newInput is empty by default.
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase != phaseNewWorktree {
		t.Errorf("phase = %v, want phaseNewWorktree", gotModel.phase)
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil (empty name should not dispatch)", cmd)
	}
	if gotModel.newError == "" {
		t.Error("newError = empty, want validation error message")
	}
}

// (The typed-name Enter's contract lives in new_worktree_test.go:
// TestTypedNameDefersCreationToTheCommitPoint and its neighbours. The old
// ensureNewWorktreeCmd/newWorktreeCreatedMsg machinery died with the
// deferral: the commit point creates now.)

// TestQTypesInNewWorktreePrompt asserts that pressing 'q' in the
// new-worktree prompt types the character instead of quitting the TUI.
// Before the phase-gated quit fix, any branch name containing 'q'
// (e.g. quick-fix) killed the app mid-typing and lost the input.
func TestQTypesInNewWorktreePrompt(t *testing.T) {
	m := model{
		phase:    phaseNewWorktree,
		width:    80,
		height:   24,
		newInput: newInputModel(80),
	}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if !strings.Contains(gotModel.newInput.Value(), "q") {
		t.Errorf("input value = %q, want to contain q (quit hijacked the key)", gotModel.newInput.Value())
	}
}

// TestQDoesNotQuitWhileFiltering asserts that 'q' types into the picker's
// filter rather than quitting. The filter is opened with '/'; a query like
// "quality" would otherwise quit the TUI on the first 'q'.
func TestQDoesNotQuitWhileFiltering(t *testing.T) {
	m := pickerReady(t, pickerFixture())
	m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if !m.rows.filtering {
		t.Fatal("'/' did not open the filter")
	}
	m, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd != nil {
		t.Errorf("'q' while filtering returned a command (quit?)")
	}
	if !strings.Contains(m.rows.filter, "q") {
		t.Errorf("filter = %q, want to contain q (quit hijacked the key)", m.rows.filter)
	}
}

// TestQDoesNotQuitWhileFilteringAgentList asserts that 'q' types into the
// agent+command picker's list filter rather than quitting the TUI. PR 2's
// phaseAgent screen reuses bubbles/list (filter opened with '/'), but
// isTyping() had no phaseAgent case, so a query like "quality" killed the
// app on the first 'q' while the worktree-list screen guarded it fine.
func TestQDoesNotQuitWhileFilteringAgentList(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
	}
	m := model{phase: phaseAgent, cfg: cfg, width: 80, height: 24}
	m.agentList = list.New(buildAgentList(cfg), list.NewDefaultDelegate(), 78, 22)
	// Open the filter exactly like a user pressing '/'.
	m.agentList, _ = m.agentList.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if m.agentList.FilterState() != list.Filtering {
		t.Fatalf("filter state = %v, want Filtering", m.agentList.FilterState())
	}

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if !strings.Contains(gotModel.agentList.FilterInput.Value(), "q") {
		t.Errorf("filter input = %q, want to contain q (quit hijacked the key)", gotModel.agentList.FilterInput.Value())
	}
}

// TestQDoesNotQuitWhileFilteringModelList asserts that 'q' types into the
// model picker's filter query instead of quitting the TUI. isTyping() had no
// phaseModel case (only phaseList and phaseAgent), so a query like "qwen"
// quit the app on the first 'q' — even though the model picker's '/' filter
// is enabled by default and advertised in the wrap-around/filter-key tests
// above. This is the isTyping() half of the PR-review finding covering
// mishandled filter keystrokes in the model picker.
func TestQDoesNotQuitWhileFilteringModelList(t *testing.T) {
	// Isolate the usage store: buildTable scans it for the usage
	// counts.
	stubUsageStore(t)
	models := []config.Model{
		{ID: "ollama/qwen3.8:27b", ModelName: "qwen3.8:27b", ProviderID: "ollama", Family: "qwen3.8"},
		{ID: "ollama/other", ModelName: "other", ProviderID: "ollama"},
	}
	m := model{phase: phaseModel, width: 80, height: 24}
	m.models = compactModelList(t, models)
	// Open the filter exactly like a user pressing '/'.
	m.models, _ = m.models.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if m.models.FilterState() != list.Filtering {
		t.Fatalf("filter state = %v, want Filtering", m.models.FilterState())
	}

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if !strings.Contains(gotModel.models.FilterInput.Value(), "q") {
		t.Errorf("filter input = %q, want to contain q (quit hijacked the key)", gotModel.models.FilterInput.Value())
	}
}

// TestEnterWhileFilteringAppliesFilterNotLaunch asserts that Enter, while
// the model picker's filter input is focused (FilterState == Filtering),
// is handled by bubbles/list's own AcceptWhileFiltering binding rather than
// being intercepted by the phaseModel Enter case, which runs the ollama
// check and launches the highlighted model. Before the fix, the top-level
// "enter" case switched on m.phase and ran the launch path unconditionally,
// so a keystroke meant to commit the filter query instead launched (and,
// via launchAndRecord, could commit rotation/usage state for) whatever
// model happened to be highlighted underneath the filter overlay.
func TestEnterWhileFilteringAppliesFilterNotLaunch(t *testing.T) {
	stubUsageStore(t) // buildTable scans the usage store
	models := []config.Model{
		{ID: "ollama/qwen3.8:27b", ModelName: "qwen3.8:27b", ProviderID: "ollama", Family: "qwen3.8"},
		{ID: "ollama/other", ModelName: "other", ProviderID: "ollama"},
	}
	m := model{cfg: testConfig(), phase: phaseModel, width: 80, height: 24}
	m.models = compactModelList(t, models)
	m.models, _ = m.models.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if m.models.FilterState() != list.Filtering {
		t.Fatalf("filter state = %v, want Filtering", m.models.FilterState())
	}

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase != phaseModel {
		t.Errorf("phase = %v, want phaseModel (Enter while filtering must not advance past the picker)", gotModel.phase)
	}
	if cmd != nil {
		t.Errorf("expected nil cmd (no launch) for Enter while filtering, got %v", cmd)
	}
	if gotModel.status != "" {
		t.Errorf("status = %q, want empty (Enter while filtering must not run the ollama check)", gotModel.status)
	}
}

// TestModelPickerFilterReceivesJKKeys verifies that "j"/"k" keystrokes typed
// into the model picker's filter query reach the filter text input instead
// of being intercepted as wrap-around navigation. bubbles/list's Filter key
// calls GoToStart(), which resets the cursor to index 0 every time
// filtering begins, and the wrap-around handler treated index 0 as
// "top of list, wrap on up" for any "up"/"k" keypress — matching "k"/"j"
// before bubbles/list's own filter input ever saw them. A query like
// "kimi" would silently drop its leading "k". Flagged in PR #82 review.
func TestModelPickerFilterReceivesJKKeys(t *testing.T) {
	stubUsageStore(t) // buildTable scans the usage store
	models := []config.Model{
		{ID: "ollama/kimi", ModelName: "kimi", ProviderID: "ollama"},
		{ID: "ollama/other", ModelName: "other", ProviderID: "ollama"},
	}
	m := model{phase: phaseModel, width: 80, height: 24}
	m.models = compactModelList(t, models)

	// Open the filter exactly like a user pressing '/'.
	m.models, _ = m.models.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if m.models.FilterState() != list.Filtering {
		t.Fatalf("filter state = %v, want Filtering", m.models.FilterState())
	}
	if idx := m.models.Index(); idx != 0 {
		t.Fatalf("cursor index = %d, want 0 (bubbles/list resets to start on filter open)", idx)
	}

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if !strings.Contains(gotModel.models.FilterInput.Value(), "k") {
		t.Errorf("filter input = %q, want to contain k (wrap-around hijacked the key)", gotModel.models.FilterInput.Value())
	}
}

// TestModelPickerWrapsFromTopToBottom verifies pressing up/k at the first
// model wraps the cursor to the last model, since bubbles/list does not
// wrap by default. Locks down the wrap-around behavior added in PR #82,
// which previously had no direct test (flagged in review).
func TestModelPickerWrapsFromTopToBottom(t *testing.T) {
	stubUsageStore(t) // buildTable scans the usage store
	models := []config.Model{
		{ID: "ollama/a", ModelName: "a", ProviderID: "ollama"},
		{ID: "ollama/b", ModelName: "b", ProviderID: "ollama"},
		{ID: "ollama/c", ModelName: "c", ProviderID: "ollama"},
	}
	m := model{phase: phaseModel, width: 80, height: 24}
	// Compact production layout: a(0), b(1), c(2) — no dividers.
	m.models = compactModelList(t, models)
	m.models.Select(0) // first model row

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if got := gotModel.models.Index(); got != 2 {
		t.Errorf("index after up-wrap = %d, want 2 (last model row)", got)
	}
}

// TestModelPickerWrapsFromBottomToTop verifies pressing down/j at the last
// model wraps the cursor back to the first model. Locks down the
// wrap-around behavior added in PR #82, which previously had no direct
// test (flagged in review).
func TestModelPickerWrapsFromBottomToTop(t *testing.T) {
	stubUsageStore(t) // buildTable scans the usage store
	models := []config.Model{
		{ID: "ollama/a", ModelName: "a", ProviderID: "ollama"},
		{ID: "ollama/b", ModelName: "b", ProviderID: "ollama"},
		{ID: "ollama/c", ModelName: "c", ProviderID: "ollama"},
	}
	m := model{phase: phaseModel, width: 80, height: 24}
	// Compact production layout: a(0), b(1), c(2) — no dividers.
	m.models = compactModelList(t, models)
	m.models.Select(2) // last model row

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if got := gotModel.models.Index(); got != 0 {
		t.Errorf("index after down-wrap = %d, want 0 (first model row)", got)
	}
}

// TestNNotHijackedWhileFiltering asserts that 'n' types into the picker's
// filter instead of opening the new-worktree prompt, so branches with 'n' in
// their name stay filterable by typing.
func TestNNotHijackedWhileFiltering(t *testing.T) {
	m := pickerReady(t, pickerFixture())
	m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = drainCmds(t, m, cmd)
	if m.phase == phaseNewWorktree {
		t.Error("phase = phaseNewWorktree, want phaseList (n hijacked the filter)")
	}
	if !strings.Contains(m.rows.filter, "n") {
		t.Errorf("filter = %q, want to contain n", m.rows.filter)
	}
}

// TestRePickClearsAnAbandonedCreation checks every selection rebinds the
// launch: a later pick of an existing worktree must clear the creation plan
// an earlier pick left behind. Without this, esc back to the list and a
// re-pick still drove the launch — and at the commit point created — the
// abandoned pick's worktree. The typed name's plan goes with it: it is owed
// the same commit-point Ensure, and an abandoned one must never run it for a
// later pick.
func TestRePickClearsAnAbandonedCreation(t *testing.T) {
	stubShellLaunch(t)
	m := model{
		cfg:          &config.Config{},
		phase:        phaseList,
		width:        80,
		height:       24,
		initialAgent: "opencode",
		creation:     &launchCreation{repoRoot: "/repo", name: "abandoned"},
		typedPlan:    &worktree.Plan{},
	}
	m, _ = updateMsg(m, selectedEntryMsg{entry: worktree.Entry{Type: worktree.TypeWorktree, Branch: "kept", Path: "/repo/.worktrees/kept"}})
	if m.creation != nil {
		t.Errorf("creation = %+v, want nil after picking an existing worktree", m.creation)
	}
	if m.typedPlan != nil {
		t.Errorf("typedPlan = %+v, want nil after picking an existing worktree", m.typedPlan)
	}
	if m.selectedPath != "/repo/.worktrees/kept" {
		t.Errorf("selectedPath = %q, want the re-pick's path", m.selectedPath)
	}
}

// TestSecondEnterAtTheCommitPointIsIgnored checks the commit point serializes
// the launch in flight: a second Enter pressed before the terminal is released
// must not start a second run command — with it a second creation binding, a
// second gate and a second agent. A session can still commit again after the
// slot applier's failure returns it to the picker (TestRetryAfterASlotAbort…);
// the quit path holds the guard to process exit (TestLaunchDoneKeeps…). The
// old m.creating guard that serialized selections went with the create flow it
// guarded.
func TestSecondEnterAtTheCommitPointIsIgnored(t *testing.T) {
	m, first, _ := pickRow(t, "/repo/.worktrees/feat")
	if first == nil {
		t.Fatal("the first Enter did not start a run command")
	}
	_, second := updateMsg(m, selectedEntryMsg{entry: worktree.Entry{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo/.worktrees/feat"}})
	if second != nil {
		t.Error("a second Enter started a second run command; the commit point takes one launch")
	}
}

// TestSecondEnterAtLaunchAndRecordIsIgnored pins the same guard at the model
// path's own commit point (launchAndRecord), where a session with a model
// actually launches: two Enters there must produce one run command, never two
// recordings, two gates and two agents.
func TestSecondEnterAtLaunchAndRecordIsIgnored(t *testing.T) {
	m := model{cfg: &config.Config{}, agent: "claude", launchModel: config.Model{ID: "prov/m"}}
	m1, first := m.launchAndRecord(exec.Command("true"))
	if first == nil {
		t.Fatal("the first Enter did not start a run command")
	}
	if _, second := m1.launchAndRecord(exec.Command("true")); second != nil {
		t.Error("a second Enter started a second run command at launchAndRecord; the commit point takes one launch")
	}
}

// TestRetryAfterASlotAbortDispatchesARunCommand checks the commit-point guard
// is an in-flight guard, not a latch: the slot applier's failure (slotAbortMsg;
// the flow returns to the model picker) releases it, so the deliberate retry
// can commit again. The guard releases on exactly that one outcome — the quit
// path holds it to process exit (TestLaunchDoneKeepsTheCommitGuardLatched). A
// guard that never released left the session silently dead — every later Enter
// dispatched nothing.
func TestRetryAfterASlotAbortDispatchesARunCommand(t *testing.T) {
	m, first, _ := pickRow(t, "/repo/.worktrees/feat")
	if first == nil {
		t.Fatal("the first Enter did not start a run command")
	}
	// The abort returns to the model picker, whose list the fixture must
	// carry for Update's final fit (the real picker always has one).
	m.models = compactModelList(t, nil)
	// The run command's outcome came back as a slotAbortMsg and returned to
	// the picker; the retry's Enter must start a run command of its own.
	m, _ = updateMsg(m, slotAbortMsg{err: errMock("no slot-profile applier")})
	if _, retry := updateMsg(m, selectedEntryMsg{entry: worktree.Entry{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo/.worktrees/feat"}}); retry == nil {
		t.Error("the retry's Enter dispatched nothing; the guard latched instead of guarding the launch in flight")
	}
}

// TestLaunchDoneKeepsTheCommitGuardLatched checks the two run-command outcomes
// treat the commit-point guard differently on purpose: the slot applier's
// abort is the one retry path and releases it
// (TestRetryAfterASlotAbortDispatchesARunCommand), while the agent's own exit
// quits and keeps the guard latched through teardown. It matters because the
// run command's closure hands the terminal back (input restarts at
// RestoreTerminal) just before launchDoneMsg is processed, so a KeyMsg already
// queued is handled before the QuitMsg and would commit a duplicate run
// command — a second agent, a second rotation/usage record — against a
// released guard. What this pin observes: the guard's state after each
// outcome, and that a selection fed during teardown dispatches nothing. What
// it cannot observe: the queued-key race itself — that ordering is
// bubbletea's, around tea.Quit, and is not reachable from an Update-level
// test; the pin holds the precondition that makes the race harmless.
func TestLaunchDoneKeepsTheCommitGuardLatched(t *testing.T) {
	m, first, _ := pickRow(t, "/repo/.worktrees/feat")
	if first == nil {
		t.Fatal("the first Enter did not start a run command")
	}
	// The agent exited and the program is quitting: the guard must still be
	// held when the quit is processed.
	done, _ := updateMsg(m, launchDoneMsg{err: errMock("exit status 1")})
	if !done.committed {
		t.Error("launchDoneMsg released the guard; a key queued at RestoreTerminal can commit a duplicate run command before the QuitMsg")
	}
	// The observable consequence: a selection already queued when the quit is
	// processed must dispatch no run command.
	if _, queued := updateMsg(done, selectedEntryMsg{entry: worktree.Entry{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo/.worktrees/feat"}}); queued != nil {
		t.Error("a selection fed during teardown dispatched a run command; the quit path holds the guard")
	}
	// Contrast: the slot applier's abort returns to the model picker and
	// releases the guard so the retry's Enter can commit
	// (TestRetryAfterASlotAbortDispatchesARunCommand drives the dispatch).
	m.models = compactModelList(t, nil)
	abort, _ := updateMsg(m, slotAbortMsg{err: errMock("no slot-profile applier")})
	if abort.committed {
		t.Error("slotAbortMsg kept the guard; the return to the model picker must be able to commit a retry")
	}
}

// TestReloadErrorVisibleAboveList asserts that a reload failure after
// a successful create renders above the list. Before the fix the error
// was stored in m.status, which View only shows while !m.ready, so the
// user saw a stale list with no explanation after the reload failed.
func TestReloadErrorVisibleAboveList(t *testing.T) {
	m := pickerReady(t, pickerFixture())
	m, _ = updateMsg(m, rowsLoadedMsg{err: errMock("git worktree list: boom")})
	if m.listError == "" {
		t.Fatal("listError = empty, want reload error")
	}
	if view := m.View(); !strings.Contains(view, "boom") {
		t.Errorf("View missing reload error, got:\n%s", view)
	}
}

// errMock is a small helper to make error literals readable in tests.
type errMock string

func (e errMock) Error() string { return string(e) }

// TestPrePathInitSkipsWorktreePicker asserts that when a worktree path is
// pre-resolved (Run's prePath, from -W/--cwd/outside-repo), Init skips
// worktree enumeration and proceeds straight to the agent/command picker
// when no agent is pinned. Without this, `wt -W feat` (no -A) could not
// prompt for an agent.
func TestPrePathInitSkipsWorktreePicker(t *testing.T) {
	m := model{prePath: "/repo/.worktrees/feat", cfg: testConfig()}
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init returned nil cmd")
	}
	msg := cmd()
	sel, ok := msg.(selectedEntryMsg)
	if !ok {
		t.Fatalf("Init cmd returned %T, want selectedEntryMsg", msg)
	}
	if sel.entry.Path != "/repo/.worktrees/feat" {
		t.Errorf("path = %q, want prePath", sel.entry.Path)
	}
	// Feeding it proceeds to the agent picker (unpinned).
	got, _ := m.Update(sel)
	nm := got.(model)
	if nm.phase != phaseAgent {
		t.Errorf("phase = %v, want phaseAgent (skip worktree picker, show agent picker)", nm.phase)
	}
	if len(nm.agentList.Items()) == 0 {
		t.Error("agentList empty; expected agent+command picker rows")
	}
}

// TestPrePathInitPinnedGoesToModel asserts the pre-path shortcut still honors
// a pinned --agent: the worktree picker is skipped and the model phase opens
// directly.
func TestPrePathInitPinnedGoesToModel(t *testing.T) {
	// Isolate usage (seam) and rotation (XDG) state: the pinned-agent path
	// runs enterModelPhase, which scans usage.jsonl for the list build and
	// reads rotation.state for cursor positioning.
	tempStateDir(t)
	stubUsageStore(t)
	requireBinary(t, "claude")
	m := model{prePath: "/repo/.worktrees/feat", cfg: testConfig(), initialAgent: "claude"}
	msg := m.Init()()
	sel, ok := msg.(selectedEntryMsg)
	if !ok {
		t.Fatalf("Init cmd returned %T, want selectedEntryMsg", msg)
	}
	got, _ := m.Update(sel)
	nm := got.(model)
	if nm.phase != phaseModel {
		t.Errorf("phase = %v, want phaseModel (pinned agent with prePath)", nm.phase)
	}
}

// TestPrePathInitPinnedCommandLaunches asserts that `wt -W feat -A shell`
// (prePath + command agent) routes through launchCommand instead of the model
// phase. Without this test, a regression in proceedFromSelectedPath's command
// path could silently route shell through enterModelPhase — failing because
// shell has no models — or skip launchCommand entirely. The assertion is
// phase- and agent-based (not on the launch outcome) so the test is robust to
// bash being absent from PATH: when bash is missing, m.status holds an error
// and runAndWaitCmd is never returned, but the phase/agent transitions still
// prove the right code path was taken.
func TestPrePathInitPinnedCommandLaunches(t *testing.T) {
	m := model{prePath: "/repo/.worktrees/feat", cfg: testConfig(), initialAgent: "shell"}
	msg := m.Init()()
	sel, ok := msg.(selectedEntryMsg)
	if !ok {
		t.Fatalf("Init cmd returned %T, want selectedEntryMsg", msg)
	}
	got, _ := m.Update(sel)
	nm := got.(model)
	if nm.agent != "shell" {
		t.Errorf("agent = %q, want %q (pinned command agent must be honored)", nm.agent, "shell")
	}
	if nm.phase == phaseModel {
		t.Errorf("phase = phaseModel, want command path (shell has no models)")
	}
	// selectedPath must be threaded to launchCommand even when the path is
	// pre-resolved by -W. Verify it's set; the launch itself may or may not
	// succeed depending on bash being on PATH, but the path argument is
	// part of the launchCommand contract and must be populated.
	if nm.selectedPath != "/repo/.worktrees/feat" {
		t.Errorf("selectedPath = %q, want prePath (%q)", nm.selectedPath, "/repo/.worktrees/feat")
	}
}

// TestPrintPendingSummaryAndSurveyOrder verifies Run()'s post-p.Run() logic
// invokes the survey and prints the summary and pricing notice, and clears
// both package vars so a later launch in the same process doesn't replay
// stale state. Full ordering is pinned by TestPostExitOrder.
func TestPrintPendingSummaryAndSurveyOrder(t *testing.T) {
	prevSummary, prevSurvey, prevRunSurvey, prevNotice := pendingSummary, pendingSurveyState, runSurvey, emitPriceNotice
	t.Cleanup(func() {
		pendingSummary = prevSummary
		pendingSurveyState = prevSurvey
		runSurvey = prevRunSurvey
		emitPriceNotice = prevNotice
	})

	pendingSummary = "wt: claude · claude/sonnet · 1s"
	pendingSurveyState = pendingSurvey{agent: "claude", m: config.Model{ID: "claude/sonnet"}}

	var calledWith pendingSurvey
	called := false
	runSurvey = func(agent string, m config.Model) string {
		called = true
		calledWith = pendingSurvey{agent: agent, m: m}
		return ""
	}
	noticeCalled := false
	emitPriceNotice = func(*config.Config) { noticeCalled = true }

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	printPendingSummaryAndSurvey(&config.Config{})
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)

	if !strings.Contains(string(out), "wt: claude · claude/sonnet · 1s") {
		t.Errorf("stdout = %q, want the summary line", string(out))
	}
	if !called {
		t.Fatal("runSurvey was not invoked")
	}
	if calledWith.agent != "claude" || calledWith.m.ID != "claude/sonnet" {
		t.Fatalf("runSurvey called with %+v, want agent=claude model=claude/sonnet", calledWith)
	}
	if pendingSummary != "" {
		t.Errorf("pendingSummary = %q, want cleared", pendingSummary)
	}
	if pendingSurveyState.agent != "" {
		t.Errorf("pendingSurveyState = %+v, want cleared", pendingSurveyState)
	}
	if !noticeCalled {
		t.Error("emitPriceNotice was not invoked after the summary")
	}
}

// TestPrintPendingSummaryAndSurveySkipsWhenNoLaunch verifies runSurvey is
// never invoked when no launch happened (pendingSurveyState.agent == ""),
// e.g. the user quit from the worktree picker without launching anything.
func TestPrintPendingSummaryAndSurveySkipsWhenNoLaunch(t *testing.T) {
	prevSurvey, prevRunSurvey, prevNotice := pendingSurveyState, runSurvey, emitPriceNotice
	t.Cleanup(func() {
		pendingSurveyState = prevSurvey
		runSurvey = prevRunSurvey
		emitPriceNotice = prevNotice
	})
	pendingSurveyState = pendingSurvey{}
	called := false
	runSurvey = func(agent string, m config.Model) string { called = true; return "" }
	noticeCalled := false
	emitPriceNotice = func(*config.Config) { noticeCalled = true }

	printPendingSummaryAndSurvey(&config.Config{})
	if called {
		t.Fatal("runSurvey was invoked with no pending launch")
	}
	if noticeCalled {
		t.Error("emitPriceNotice invoked with no launch")
	}
}

// TestPrintPendingSummaryAndSurveySkipsPriceNoticeForCommandAgent verifies
// the pricing reminder does NOT fire for a command-agent launch (e.g.
// shell), which records pendingSurveyState with m.ID == "" — the reminder
// is meaningless there and was previously firing unconditionally alongside
// the summary line.
func TestPrintPendingSummaryAndSurveySkipsPriceNoticeForCommandAgent(t *testing.T) {
	prevSummary, prevSurvey, prevRunSurvey, prevNotice := pendingSummary, pendingSurveyState, runSurvey, emitPriceNotice
	t.Cleanup(func() {
		pendingSummary = prevSummary
		pendingSurveyState = prevSurvey
		runSurvey = prevRunSurvey
		emitPriceNotice = prevNotice
	})

	pendingSummary = "wt: shell · 1s"
	pendingSurveyState = pendingSurvey{agent: "shell", m: config.Model{}}
	runSurvey = func(agent string, m config.Model) string { return "" }
	noticeCalled := false
	emitPriceNotice = func(*config.Config) { noticeCalled = true }

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	printPendingSummaryAndSurvey(&config.Config{})
	w.Close()
	os.Stdout = old
	_, _ = io.ReadAll(r)

	if noticeCalled {
		t.Error("emitPriceNotice was invoked for a command agent (m.ID == \"\")")
	}
}
