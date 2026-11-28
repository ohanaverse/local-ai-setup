package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// tempStateDir creates an isolated agent-wt state directory under a temp
// XDG_CONFIG_HOME. The picker's "> " marker reads last-picked.state from
// config.Dir(), so tests touching the picker must isolate state or they
// become dependent on the host's real ~/.config/agent-wt files.
func tempStateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "agent-wt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Dir(dir))
	return dir
}

// seedState records a launch of modelID in dir (last-picked.state and a usage
// event), as a prior launch would. Tests use this to simulate prior launches.
func seedState(t *testing.T, dir, modelID string) {
	t.Helper()
	if err := usage.NewStoreAt(dir).RecordLaunch("", modelID); err != nil {
		t.Fatalf("seed last-picked state: %v", err)
	}
}

// singleModelList builds a one-item list.Model with m highlighted at
// index 0. It replaces the pre-refactor pattern of setting m.current
// directly: tests that need "the picker has X highlighted" now build
// a single-item list and select index 0. m.models is the single
// source of truth for the picked model in production code; this
// helper lets tests mirror that contract.
func singleModelList(m config.Model) list.Model {
	items := []list.Item{&modelItem{model: m}}
	ml := list.New(items, list.NewDefaultDelegate(), 80, 24)
	ml.Select(0)
	return ml
}

// phaseModelWithList builds a model in phaseModel with m.models populated
// for the given agent+tag, with the cursor where the catalog command puts it: the row
// last-picked.state names when it is shown and not blocked, else the first row
// that is not blocked (row 0 when every row is). Tests that exercise the list or the
// View use this helper instead of constructing model literals. It routes
// through the production list-build path (buildTable, the table renderer
// the catalog load uses too) so tests exercise the compact, divider-free
// item layout the picker actually renders. The usage counts come through
// the newUsageStore seam so callers that stub the seam (or isolate
// XDG_CONFIG_HOME via tempStateDir) never read the host's real usage.jsonl.
// Requires tempStateDir(t) isolation: the helper reads last-picked.state
// (the "> " marker) from config.Dir(), and asserts that isolation is
// actually in place — calling it without tempStateDir fails fast instead of
// silently reading the host's real last-picked.state.
func phaseModelWithList(t *testing.T, cfg *config.Config, agent, tag string) model {
	t.Helper()
	if os.Getenv("XDG_CONFIG_HOME") == "" {
		t.Fatal("phaseModelWithList: XDG_CONFIG_HOME is not isolated; call tempStateDir(t) first or the marker reads the host's real last-picked.state")
	}
	// The tag and catalog-membership filters the model picker applies; the
	// agent's own list no longer filters models.
	var models []config.Model
	for _, m := range cfg.Models {
		if m.HasTag(tag) && cfg.InCatalog(m) {
			models = append(models, m)
		}
	}
	if len(models) == 0 {
		t.Fatalf("phaseModelWithList: no models for agent %q tag %q", agent, tag)
	}
	// Mirror the catalog command: the marker's row is last-picked.state's, and
	// the cursor goes through the production list builder with the same
	// remembered-row rule (usableRowID), so this helper cannot drift from it.
	lastID, _ := usage.NewStore().LastPicked()
	tbl := buildTable(tableInput{models: models, usage: newUsageStore()}, lastID)
	m := model{
		cfg:    cfg,
		phase:  phaseModel,
		agent:  agent,
		tag:    tag,
		theme:  themes.Default,
		width:  80,
		height: 24,
	}
	m.models = m.newModelList(tbl, usableRowID(tbl, lastID))
	return m
}

// drivePhaseAgentEnter simulates the full PR-2 picker flow: fire
// selectedEntryMsg (which builds m.agentList and transitions to
// phaseAgent), select the agent row matching `agentName`, then fire
// Enter on the picker. The returned model is whatever phaseAgent
// Enter produces once the model server's catalog (catalogFor m.cfg, when
// the test attached no server) has loaded — phaseModel on the happy path, or
// phaseAgent with m.status set if the catalog is empty for that agent+tag.
// Tests that used to drive only selectedEntryMsg and assert
// phaseModel state now call this helper to land at the same
// post-transition state the production code reaches when the
// user picks an agent from the picker.
func drivePhaseAgentEnter(t *testing.T, m model, agentName string) model {
	t.Helper()
	requireBinary(t, "claude")
	if m.client == nil {
		m = withServer(t, m)
	}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	m = got.(model)
	if m.phase != phaseAgent {
		t.Fatalf("after selectedEntryMsg: phase = %v, want phaseAgent", m.phase)
	}
	selectAgentRow(&m, agentName)
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	next, _ := settleCatalog(t, got.(model), cmd)
	return next
}

// TestSelectedEntryMsgEmptyListStaysOnList asserts that when the agent+tag
// has no compatible models, pressing Enter on the agent row in the new
// phaseAgent picker does NOT enter the model phase. The validation gate
// fires when the user picks the agent (not when the worktree is picked,
// since PR 2 split the entry into two transitions), surfaces a status
// message, and keeps the user in phaseAgent so they can fix the config.
func TestSelectedEntryMsgEmptyListStaysOnList(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Agents:     []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
		Providers:  []config.Provider{{ID: "ollama", Protocols: []config.Protocol{config.ProtocolAnthropic, config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}}},
		// No models with provider "ollama".
	}
	m := model{cfg: cfg, phase: phaseList, width: 80, height: 24}
	gotModel := drivePhaseAgentEnter(t, m, "claude")
	if gotModel.phase != phaseAgent {
		t.Errorf("phase = %v, want phaseAgent (no entry into model picker)", gotModel.phase)
	}
	if gotModel.status == "" {
		t.Error("status = empty, want an error message")
	}
	if view := gotModel.View(); !strings.Contains(view, gotModel.status) {
		t.Errorf("View missing status %q:\n%s", gotModel.status, view)
	}
}

// TestSelectedEntryMsgEmptyListSetsActionableStatus asserts the status
// message names the agent and the tag so the user knows what to fix.
// PR 2 moved the empty-catalog validation into the phaseAgent Enter
// handler, so this test drives that path (selecting the agent row in
// the new picker and pressing Enter) rather than the bare worktree pick.
func TestSelectedEntryMsgEmptyListSetsActionableStatus(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Agents:     []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
		Providers:  []config.Provider{{ID: "ollama", Protocols: []config.Protocol{config.ProtocolAnthropic, config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}}},
	}
	m := model{cfg: cfg, phase: phaseList, width: 80, height: 24}
	gotModel := drivePhaseAgentEnter(t, m, "claude")
	for _, want := range []string{"claude", "code"} {
		if !strings.Contains(gotModel.status, want) {
			t.Errorf("status %q missing %q", gotModel.status, want)
		}
	}
}

// TestPinnedAgentEmptyListStatusUsesFirstActiveTag asserts that in the pinned
// --agent path the empty-catalog status uses the first active -T tag rather
// than the config default tag.
func TestPinnedAgentEmptyListStatusUsesFirstActiveTag(t *testing.T) {
	requireBinary(t, "claude")
	cfg := &config.Config{
		DefaultTag: "code",
		Agents:     []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
		Providers:  []config.Provider{{ID: "ollama", Protocols: []config.Protocol{config.ProtocolAnthropic, config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}}},
	}
	m := model{
		cfg:          cfg,
		initialAgent: "claude",
		activeTags:   "design,code",
		width:        80,
		height:       24,
	}
	m = withServer(t, m)
	got, _ := m.Update(rowsLoadedMsg{report: pickerFixture(), repoRoot: "/repo"})
	m = got.(model)
	got, cmd := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature", Path: "/tmp/feature"}})
	gotModel, _ := settleCatalog(t, got.(model), cmd)
	if !strings.Contains(gotModel.status, `tag "design"`) {
		t.Fatalf("status = %q, want first active tag design", gotModel.status)
	}
}

// TestPinnedAgentErrorRenderedOnList asserts that a pinned --agent whose
// model catalog is empty surfaces its error on screen (the agent picker the
// catalog load routes back to) instead of swallowing it: `wt --agent <agent>`
// with an empty catalog once failed with zero on-screen feedback. The
// installed probe is stubbed so the pinned path reaches the catalog load on
// any runner — without it the flow stops at "not installed" wherever the
// host has no `claude` binary (CI), and the test asserts nothing.
func TestPinnedAgentErrorRenderedOnList(t *testing.T) {
	defer stubInstalled("claude")()
	cfg := &config.Config{
		DefaultTag: "code",
		Agents:     []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
		Providers:  []config.Provider{{ID: "ollama", Protocols: []config.Protocol{config.ProtocolAnthropic, config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}}},
		// No models — the picker's list is empty.
	}
	m := model{cfg: cfg, initialAgent: "claude", width: 80, height: 24}
	// Load the worktree list first so m.ready is true; the pinned-path
	// error then must render on the ready list, not via the loading branch.
	m = withServer(t, m)
	got, _ := m.Update(rowsLoadedMsg{report: pickerFixture(), repoRoot: "/repo"})
	m = got.(model)
	got, cmd := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature", Path: "/tmp/feature"}})
	gotModel, _ := settleCatalog(t, got.(model), cmd)
	if gotModel.phase != phaseAgent {
		t.Fatalf("phase = %v, want phaseAgent (an empty catalog routes back to the agent picker)", gotModel.phase)
	}
	if gotModel.status == "" {
		t.Fatal("status = empty, want a config error")
	}
	if view := gotModel.View(); !strings.Contains(view, gotModel.status) {
		t.Errorf("View missing status %q:\n%s", gotModel.status, view)
	}
}

// TestToggleBackToCode and TestTagKeyIgnoredInListPhase were removed in PR 3b
// Task 3 along with the `d` key handler. The toggle's two-way switch behavior
// and the list-phase gating are no longer meaningful once the keybind itself
// is gone. Coverage of the `-T` filter mechanism lives in
// TestPhaseModelHonorsFilters (agent_picker_test.go) and
// TestLaunchFilteredUsesEligibleAndSlot (cmd/wt-session/launch_test.go).

// TestModelScreenMKeyIsNoOp asserts that pressing 'm' on the model
// screen does not open a browser (there is no browser anymore) and
// does not mutate any state. This pins the removal: if a future
// change re-introduces 'm' as a keybind, this test will fail.
func TestModelScreenMKeyIsNoOp(t *testing.T) {
	dir := tempStateDir(t)
	seedState(t, dir, "ollama/gemma4:9b")
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	before := m
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	gotModel := got.(model)
	if gotModel.phase != before.phase {
		t.Errorf("phase changed: before %v, after %v", before.phase, gotModel.phase)
	}
	if gotModel.tag != before.tag {
		t.Errorf("tag changed: before %q, after %q", before.tag, gotModel.tag)
	}
	if len(gotModel.models.Items()) != len(before.models.Items()) {
		t.Errorf("models items changed: before %d, after %d", len(before.models.Items()), len(gotModel.models.Items()))
	}
}

// TestViewModelPhase asserts the model-phase View renders the picker list
// with the agent and tag in the header and the keybind hints in the footer.
// The Enter hint is pinned whole ([enter] launch or start): a bare
// "[enter] launch" substring would still match a footer that never mentions
// starting, which is the drift this asserts against.
func TestViewModelPhase(t *testing.T) {
	dir := tempStateDir(t)
	seedState(t, dir, "ollama/gemma4:9b")
	// Sized through Update, as the terminal does it: View draws the frame the
	// list was sized for and measures nothing itself (layout.go), so a list
	// still at its construction size gets the frame that leaves that much.
	m := resized(t, phaseModelWithList(t, testConfig(), "claude", "code"), 80, 24)
	view := m.View()
	for _, want := range []string{"agent", "claude", "tag", "code", "ollama/gemma4:9b", "[↑/↓] navigate", "[enter] launch or start"} {
		if !strings.Contains(view, want) {
			t.Errorf("View missing %q in:\n%s", want, view)
		}
	}
}

// TestEnterInModelPhaseLaunchesWithoutSession asserts that pressing Enter
// when there is no prior session quits the TUI and runs the agent. We test
// the immediate-launch path by inspecting the returned command batch.
func TestEnterInModelPhaseLaunchesWithoutSession(t *testing.T) {
	requireBinary(t, "claude")
	m := model{cfg: testConfig(), phase: phaseModel, agent: "claude", tag: "code",
		selectedPath: t.TempDir(), models: singleModelList(config.Model{ID: "ollama/gemma4:9b"})}
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel := got.(model)
	if gotModel.status != "" {
		t.Errorf("status = %q, want empty", gotModel.status)
	}
	if cmd == nil {
		t.Fatal("expected launch command batch, got nil")
	}
}

// TestLaunchDoneMsgRecordsError asserts that when the agent subprocess
// exits with an error, the model stores the error so it can be surfaced.
func TestLaunchDoneMsgRecordsError(t *testing.T) {
	m := model{cfg: testConfig(), phase: phaseModel}
	got, _ := m.Update(launchDoneMsg{err: nil})
	gotModel := got.(model)
	if gotModel.status != "" {
		t.Errorf("status = %q, want empty for nil error", gotModel.status)
	}

	got, cmd := got.Update(launchDoneMsg{err: nil})
	_ = got.(model)
	if cmd == nil {
		t.Errorf("cmd for launchDoneMsg = nil, want non-nil")
	}
}

// TestSelectedEntryMsgSetsSelectedPath asserts picking a worktree stores its
// path so the launch command can cd into it.
func TestSelectedEntryMsgSetsSelectedPath(t *testing.T) {
	m := model{cfg: testConfig()}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature", Path: "/tmp/feature"}})
	gotModel := got.(model)
	if gotModel.selectedPath != "/tmp/feature" {
		t.Errorf("selectedPath = %q, want /tmp/feature", gotModel.selectedPath)
	}
}

// TestYoloFlagPassedToLaunch asserts that when the model has yolo=true, the
// launch command builder propagates it to the agent driver.
func TestYoloFlagPassedToLaunch(t *testing.T) {
	requireBinary(t, "claude")
	m := model{cfg: testConfig(), phase: phaseModel, agent: "claude", tag: "code",
		selectedPath: t.TempDir(), models: singleModelList(config.Model{ID: "ollama/gemma4:9b"}), yolo: true}
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel := got.(model)
	if gotModel.status != "" {
		t.Errorf("status = %q, want empty", gotModel.status)
	}
	if cmd == nil {
		t.Fatal("expected launch command batch, got nil")
	}
}

// TestUnknownAgentLaunchShowsError asserts that pressing Enter with an
// unregistered agent surfaces a readable error instead of launching.
func TestUnknownAgentLaunchShowsError(t *testing.T) {
	m := remotePickerFor(t, "not-an-agent", wireRow("openrouter/gemma4", "gemma4", nil))
	got, prep := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if prep == nil {
		t.Fatal("Enter started no launch prep")
	}
	got, cmd := got.(model).Update(prep())
	gotModel := got.(model)
	if cmd != nil {
		t.Errorf("expected nil cmd for unknown agent, got %v", cmd)
	}
	if !strings.Contains(gotModel.status, "unknown agent") {
		t.Errorf("status = %q, want 'unknown agent' error", gotModel.status)
	}
}

// TestSelectedEntryMsgPositionsCursorOnTheLastPickedRow asserts the cursor
// lands on the row the last launch picked (last-picked.state), not on the
// model after it and not on the first row: the picker remembers the previous
// selection. It drives the phaseAgent step into phaseModel: selectEntryMsg
// lands in phaseAgent, the phaseAgent Enter handler builds m.models and
// positions the cursor.
func TestSelectedEntryMsgPositionsCursorOnTheLastPickedRow(t *testing.T) {
	dir := tempStateDir(t)
	// State: last=ollama/gemma4:9b. The selector table sorts its unscored
	// rows by id (14b, 9b, design) and none is blocked, so the first row
	// would be 14b at index 0 and the remembered row is 9b at index 1 —
	// assert on the selected model, not an index.
	stubUsageStore(t)
	seedState(t, dir, "ollama/gemma4:9b")
	cfg := testConfig()
	m := model{cfg: cfg, phase: phaseList, width: 80, height: 24}
	gotModel := drivePhaseAgentEnter(t, m, "claude")
	if got := selectedModelID(gotModel); got != "ollama/gemma4:9b" {
		t.Errorf("cursor on %q (index %d), want the last-picked row ollama/gemma4:9b", got, gotModel.models.Index())
	}
}

// TestPhaseModelWithListBuildsAndPositionsCursor asserts the test
// helper populates m.models and positions the cursor on the row last
// launched. The helper routes through the production list-build path
// (buildTable and newModelList with usableRowID, the same as the catalog
// load), so this is a smoke test of the shared infrastructure.
func TestPhaseModelWithListBuildsAndPositionsCursor(t *testing.T) {
	dir := tempStateDir(t)
	// last-picked.state names gemma4:9b, which is not blocked, so the
	// cursor lands on it rather than on the sorted first row, gemma4:14b.
	seedState(t, dir, "ollama/gemma4:9b")
	cfg := testConfig()
	m := phaseModelWithList(t, cfg, "claude", "code")

	// testConfig has two eligible code models. No dividers.
	if got := len(m.models.Items()); got != 2 {
		t.Errorf("models items = %d, want 2", got)
	}
	// The table sorts rows itself, so assert on the model under the cursor
	// rather than a hard-coded list position.
	if got := selectedModelID(m); got != "ollama/gemma4:9b" {
		t.Errorf("cursor on %q (index %d), want the last-launched row ollama/gemma4:9b", got, m.models.Index())
	}
}

// TestModelPickerMarksLastLaunchedRow drives the production
// phaseAgent → catalog command path with a seeded last-picked.state and
// asserts the last-launched marker lands on the last-launched row and that the
// cursor opens on that same row (not the sorted first row, 14b), so the
// previous selection is the current one.
func TestModelPickerMarksLastLaunchedRow(t *testing.T) {
	dir := tempStateDir(t)
	stubUsageStore(t) // hermetic usage counts: stable sort
	seedState(t, dir, "ollama/gemma4:9b")
	cfg := testConfig()
	m := model{cfg: cfg, phase: phaseList, width: 80, height: 24}
	gotModel := drivePhaseAgentEnter(t, m, "claude")

	markerIdx := -1
	for i, it := range gotModel.models.Items() {
		title := it.(*modelItem).Title()
		// Strip the 2-rune ref column so the marker check matches the
		// pre-#73 shape; the ref column is always 2 ASCII runes.
		if len(title) < 2 {
			continue
		}
		if strings.HasPrefix(title[2:], markerMarked) {
			if markerIdx != -1 {
				t.Fatalf("marker on more than one row")
			}
			markerIdx = i
		}
	}
	// The table sorts its unscored rows by id: 14b (0), 9b (1), design (2).
	if markerIdx != 1 {
		t.Errorf("marker row = %d, want 1 (ollama/gemma4:9b)", markerIdx)
	}
	if got := selectedModelID(gotModel); got != "ollama/gemma4:9b" {
		t.Errorf("cursor on %q, want the last-launched row ollama/gemma4:9b", got)
	}
}

// TestModelPickerNoMarkerWithoutLastPicked drives the production
// phaseAgent → catalog command path with NO last-picked.state present and
// asserts no row carries the last-launched marker — a regression that
// substituted a non-empty fallback when the file is missing would
// otherwise pass the seeded-state test while fabricating a "last used"
// signal.
func TestModelPickerNoMarkerWithoutLastPicked(t *testing.T) {
	tempStateDir(t) // isolate: no seedState — last-picked.state must be absent
	stubUsageStore(t)
	cfg := testConfig()
	m := model{cfg: cfg, phase: phaseList, width: 80, height: 24}
	gotModel := drivePhaseAgentEnter(t, m, "claude")

	for i, it := range gotModel.models.Items() {
		title := it.(*modelItem).Title()
		if len(title) < 2 {
			continue
		}
		if strings.HasPrefix(title[2:], markerMarked) {
			t.Errorf("row %d unexpectedly marked with no last-picked.state: %q", i, it.(*modelItem).Title())
		}
	}
}

// TestModelScreenToggleTagRebuildsList was removed in PR 3b Task 3
// along with the `d` tag-toggle key. The cross-tag switch from inside
// the TUI is gone: tag filtering is now driven by `-T` (cmd/wt-session flags
// threaded into tui.Run in Task 4), and the resulting picker list is
// covered by TestPhaseModelHonorsFilters (agent_picker_test.go).
//
// TestModelScreenToggleTagEmptyRestores likewise — its job was to
// surface a status message when toggling to a tag with no models. The
// new -T filter path returns the empty catalog through the
// selectedEntryMsg empty-catalog status (TestSelectedEntryMsgEmptyListSetsActionableStatus);
// no in-TUI fallback is needed.

// TestPhaseModelUpDownMovesCursor asserts the picker forwards
// arrow keys to bubble/list. Before this fix, Update() never called
// m.models.Update(msg) in phaseModel, so up/down were dead keys.
// This is the regression guard for the up/down bug.
func TestPhaseModelUpDownMovesCursor(t *testing.T) {
	// Isolate the state directory so the host's real ~/.config/agent-wt
	// files cannot reach the picker.
	tempStateDir(t)
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	// Pin the cursor to the top row explicitly so the up/down assertions
	// are about movement only.
	m.models.Select(0)
	if m.models.Index() != 0 {
		t.Fatalf("precondition: cursor = %d, want 0", m.models.Index())
	}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = got.(model)
	if m.models.Index() != 1 {
		t.Errorf("cursor after down = %d, want 1", m.models.Index())
	}
	got, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = got.(model)
	if m.models.Index() != 0 {
		t.Errorf("cursor after up = %d, want 0", m.models.Index())
	}
}

// TestNoRKeyInModelPhase asserts pressing 'r' in phaseModel is a
// no-op: a launch is recorded only when it is committed, never on a key,
// so the picker must ignore the standalone 'r' key. This test guards
// against accidental re-introduction of an in-picker model-cycling key.
func TestNoRKeyInModelPhase(t *testing.T) {
	dir := tempStateDir(t)
	seedState(t, dir, "ollama/gemma4:9b")
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	beforeCursor := m.models.Index()
	beforeItems := len(m.models.Items())
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	gotModel := got.(model)
	if gotModel.models.Index() != beforeCursor {
		t.Errorf("cursor moved on 'r': before=%d after=%d", beforeCursor, gotModel.models.Index())
	}
	if len(gotModel.models.Items()) != beforeItems {
		t.Errorf("list size changed on 'r': before=%d after=%d", beforeItems, len(gotModel.models.Items()))
	}
}

// TestSelectedEntryNoLastStartsAtZero asserts the picker lands the cursor on
// the first launchable table row (index 0, ollama/gemma4:14b) when nothing
// was launched before, so a fresh install starts on the sorted top row and
// not on the registry-first model. This is the cold-start path.
// PR 2 added a phaseAgent step between worktree selection and the model
// picker, so the test now drives both transitions to reach phaseModel.
func TestSelectedEntryNoLastStartsAtZero(t *testing.T) {
	tempStateDir(t) // isolates state; file doesn't exist
	stubUsageStore(t)
	cfg := testConfig()
	m := model{cfg: cfg, phase: phaseList, width: 80, height: 24}
	gotModel := drivePhaseAgentEnter(t, m, "claude")
	if gotModel.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel", gotModel.phase)
	}
	if got := selectedModelID(gotModel); got != "ollama/gemma4:14b" || gotModel.models.Index() != 0 {
		t.Errorf("cursor on %q at index %d, want ollama/gemma4:14b at 0 (first launchable row)", got, gotModel.models.Index())
	}
}

// TestPinnedModelWithoutAgentValidatesAfterAgentPick drives the full picker
// flow (worktree pick → agent pick) with a pinned -M model and an unpinned
// agent, asserting the invalid pin keeps the user on the agent picker with an
// inline error. This locks the plumbing of pinnedModel through the picker.
func TestPinnedModelWithoutAgentValidatesAfterAgentPick(t *testing.T) {
	requireBinary(t, "claude")
	tempStateDir(t)
	cfg := testConfig()
	m := model{cfg: cfg, pinnedModel: "ollama/missing", phase: phaseList, width: 80, height: 24}
	gotModel := drivePhaseAgentEnter(t, m, "claude")
	if gotModel.phase != phaseAgent {
		t.Errorf("phase = %v, want phaseAgent (invalid pinned model stays on agent picker)", gotModel.phase)
	}
	if !strings.Contains(gotModel.status, `unknown model "ollama/missing"`) {
		t.Errorf("status = %q, want the model server's unknown-model refusal", gotModel.status)
	}
}

// buildTestConfigWithModels returns a minimal config with claude supporting
// the ollama provider and the supplied models. Tests that exercise the
// catalog filter use this helper so the fixture exposes exactly the models
// the test intends.
func buildTestConfigWithModels(models ...config.Model) *config.Config {
	return &config.Config{
		DefaultTag: "code",
		Providers:  []config.Provider{{ID: "ollama", Location: config.LocationLocal, Protocols: []config.Protocol{config.ProtocolAnthropic, config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}}},
		Models:     models,
		Agents:     []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
	}
}
