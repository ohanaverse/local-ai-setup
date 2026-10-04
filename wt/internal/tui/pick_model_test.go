package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// TestPickModelEnterSelectsHighlightedModel asserts that Enter on the
// standalone model picker (used by callers like wt smoke that don't run the
// full worktree->agent->model TUI state machine) selects the currently
// highlighted row and signals completion via tea.Quit. wt smoke needs this
// exact selection semantics so the model a user sees highlighted is the one
// that gets smoke-tested.
func TestPickModelEnterSelectsHighlightedModel(t *testing.T) {
	stubUsageStore(t)
	stubRefcountStore(t)
	models := []config.Model{
		{ID: "ollama/gemma4:9b", ModelName: "gemma4:9b", ProviderID: "ollama", Family: "gemma4"},
		{ID: "ollama/qwen3.8:27b", ModelName: "qwen3.8:27b", ProviderID: "ollama", Family: "qwen3.8"},
	}
	m := newPickModel(nil, models, themes.Default, false)

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gm := got.(pickModel)

	if cmd == nil {
		t.Fatalf("cmd = nil, want tea.Quit")
	}
	if gm.canceled {
		t.Errorf("canceled = true, want false")
	}
	want := gm.list.Items()[0].(*modelItem).model.ID
	if gm.selected.ID != want {
		t.Errorf("selected = %q, want %q (the highlighted row)", gm.selected.ID, want)
	}
}

// TestPickStartModelDefaultsToNativeRow pins the disclosed consequence of
// #172 on the standalone pickers (wt smoke, wt start): newPickModel never
// calls Select, so the highlighted row is index 0 — the first sorted row —
// and native-first therefore makes a native model what a bare Enter picks.
// The cursor has no other default, so a sort change moves this silently: the
// test is what makes the behavior deliberate rather than incidental.
func TestPickStartModelDefaultsToNativeRow(t *testing.T) {
	stubUsageStore(t)
	stubRefcountStore(t)
	models := []config.Model{
		{ID: "openrouter/cheap", ModelName: "cheap", ProviderID: "openrouter", Family: "cheap", Cost: config.ModelCost{InputPricePerMillion: f64(0.1), OutputPricePerMillion: f64(0.1)}},
		{ID: "claude/native", ModelName: "native", ProviderID: "claude", Family: "claude", Native: true},
	}
	m := newPickModel(nil, models, themes.Default, true)

	if got := m.list.Index(); got != 0 {
		t.Fatalf("initial cursor = %d, want 0 (no explicit default is set)", got)
	}
	it, ok := m.list.Items()[0].(*modelItem)
	if !ok {
		t.Fatalf("item 0 is %T, want *modelItem", m.list.Items()[0])
	}
	if it.model.ID != "claude/native" {
		t.Errorf("highlighted row = %q, want claude/native — the native row must open highlighted", it.model.ID)
	}

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if gm := got.(pickModel); gm.selected.ID != "claude/native" {
		t.Errorf("bare Enter selected %q, want claude/native", gm.selected.ID)
	}
}

// TestPickModelEscCancels asserts that Esc cancels the standalone picker
// without a selection, so a caller can distinguish "user picked nothing"
// from "user picked the first model" and must not silently launch a default.
func TestPickModelEscCancels(t *testing.T) {
	stubUsageStore(t)
	stubRefcountStore(t)
	models := []config.Model{{ID: "ollama/gemma4:9b", Family: "gemma4"}}
	m := newPickModel(nil, models, themes.Default, false)

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	gm := got.(pickModel)

	if cmd == nil {
		t.Fatalf("cmd = nil, want tea.Quit")
	}
	if !gm.canceled {
		t.Errorf("canceled = false, want true")
	}
}

// TestPickModelCtrlCCancels mirrors the whole-TUI convention (app_test.go's
// TestUpdateQuitKeys) that Ctrl+C always quits immediately, even though this
// picker is a separate, standalone Bubble Tea program from the main app.
func TestPickModelCtrlCCancels(t *testing.T) {
	stubUsageStore(t)
	stubRefcountStore(t)
	models := []config.Model{{ID: "ollama/gemma4:9b", Family: "gemma4"}}
	m := newPickModel(nil, models, themes.Default, false)

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	gm := got.(pickModel)

	if cmd == nil {
		t.Fatalf("cmd = nil, want tea.Quit")
	}
	if !gm.canceled {
		t.Errorf("canceled = false, want true")
	}
}

// TestPickModelQCancelsWhenIdle asserts 'q' cancels the picker outside
// filter mode, matching the main TUI's universal quit-key convention.
func TestPickModelQCancelsWhenIdle(t *testing.T) {
	stubUsageStore(t)
	stubRefcountStore(t)
	models := []config.Model{{ID: "ollama/gemma4:9b", Family: "gemma4"}}
	m := newPickModel(nil, models, themes.Default, false)

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	gm := got.(pickModel)

	if cmd == nil {
		t.Fatalf("cmd = nil, want tea.Quit")
	}
	if !gm.canceled {
		t.Errorf("canceled = false, want true")
	}
}

// TestPickModelQTypesIntoFilterInsteadOfQuitting asserts that 'q' while the
// list's incremental filter is open types into the query instead of
// canceling the picker — the same filter-key mishandling class of bug fixed
// for the main TUI's model phase (TestQDoesNotQuitWhileFilteringModelList)
// must not reappear in this standalone picker.
func TestPickModelQTypesIntoFilterInsteadOfQuitting(t *testing.T) {
	stubUsageStore(t)
	stubRefcountStore(t)
	models := []config.Model{
		{ID: "ollama/qwen3.8:27b", ModelName: "qwen3.8:27b", ProviderID: "ollama", Family: "qwen3.8"},
		{ID: "ollama/other", ModelName: "other", ProviderID: "ollama"},
	}
	m := newPickModel(nil, models, themes.Default, false)
	m.list, _ = m.list.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if m.list.FilterState() != list.Filtering {
		t.Fatalf("filter state = %v, want Filtering", m.list.FilterState())
	}

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	gm := got.(pickModel)

	if gm.canceled {
		t.Errorf("canceled = true, want false (quit hijacked the filter keystroke)")
	}
	if !strings.Contains(gm.list.FilterInput.Value(), "q") {
		t.Errorf("filter input = %q, want to contain q", gm.list.FilterInput.Value())
	}
}

// TestNewPickModelUsesTableHeaderAndRunningState verifies wt smoke's picker
// shows the same column header as the agent flow and reads RUNNING from the live
// inventory (a running local model shows "run"), with no per-agent counts since
// the list is not scoped to one agent. Without it the two pickers would drift
// and smoke would show stale or missing running state.
func TestNewPickModelUsesTableHeaderAndRunningState(t *testing.T) {
	stubUsageStore(t)
	stubRefcountStore(t)
	stubInventory(t, localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", ModelID: "omlx/a", Artifact: "a", Registered: true, Running: true},
	}})
	cfg := &config.Config{Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}}}
	models := []config.Model{{ID: "omlx/a", ProviderID: "omlx", ModelName: "a"}}

	pm := newPickModel(cfg, models, themes.Default, false)

	if !strings.Contains(pm.list.Title, "RUNNING") {
		t.Errorf("title = %q, want the table header (with RUNNING)", pm.list.Title)
	}
	items := pm.list.Items()
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if line := items[0].(*modelItem).line; !strings.Contains(line, "run") {
		t.Errorf("line = %q, want it to show the running state (run)", line)
	}
}

// TestPickModelViewHeaderAlignsWithRows renders the standalone picker's real
// list view and checks the header's FAMILY and MODEL columns start at the same
// rune offset as the first row's cells. Both the title style and the list's
// TitleBar padding must be cleared (shared styleTableTitle helper), otherwise
// the header shifts relative to the rows in wt smoke's picker.
func TestPickModelViewHeaderAlignsWithRows(t *testing.T) {
	stubUsageStore(t)
	stubRefcountStore(t)
	models := []config.Model{{ID: "ollama/gemma4:9b", ModelName: "gemma4:9b", ProviderID: "ollama", Family: "gemma4"}}
	pm := newPickModel(nil, models, themes.Default, false)

	var header, row string
	for _, ln := range strings.Split(ansiRE.ReplaceAllString(pm.View(), ""), "\n") {
		if strings.Contains(ln, "FAMILY") && header == "" {
			header = ln
		}
		if strings.Contains(ln, "ollama/gemma4:9b") && row == "" {
			row = ln
		}
	}
	if header == "" || row == "" {
		t.Fatalf("header/row not found in view:\n%s", pm.View())
	}
	off := func(s, sub string) int { return len([]rune(s[:strings.Index(s, sub)])) }
	if off(header, "MODEL") != off(row, "ollama/gemma4:9b") {
		t.Errorf("MODEL offset %d != row id offset %d\n%q\n%q", off(header, "MODEL"), off(row, "ollama/gemma4:9b"), header, row)
	}
	if off(header, "FAMILY") != off(row, "gemma4") {
		t.Errorf("FAMILY offset %d != row family offset %d\n%q\n%q", off(header, "FAMILY"), off(row, "gemma4"), header, row)
	}
}

// TestNewPickModelHidesDiscoveredRows verifies wt smoke's picker only shows the
// models it was given: an unregistered (discovered) local model present in the
// live inventory must not appear as a row. Without hideDiscovered, smoke would
// offer models it has not verified as eligible for any agent.
func TestNewPickModelHidesDiscoveredRows(t *testing.T) {
	stubUsageStore(t)
	stubRefcountStore(t)
	stubInventory(t, localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", ModelID: "omlx/a", Artifact: "a", Registered: true, Running: true},
		{ProviderID: "omlx", ModelID: "omlx/stray", Artifact: "stray", Registered: false, Running: true},
	}})
	cfg := &config.Config{Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}}}
	models := []config.Model{{ID: "omlx/a", ProviderID: "omlx", ModelName: "a"}}

	pm := newPickModel(cfg, models, themes.Default, false)

	var ids []string
	for _, it := range pm.list.Items() {
		ids = append(ids, it.(*modelItem).model.ID)
	}
	if len(ids) != 1 || ids[0] != "omlx/a" {
		t.Errorf("items = %v, want only the passed model omlx/a (discovered omlx/stray must be hidden)", ids)
	}
}

// TestPickModelEnterOnBlockedRowDoesNotSelect verifies pressing Enter on a
// blocked row (e.g. a model missing from disk) keeps the picker open and shows
// the reason, while Enter on a normal row selects it. `wt start` lists blocked
// rows for visibility; letting Enter pick one would start a doomed model.
func TestPickModelEnterOnBlockedRowDoesNotSelect(t *testing.T) {
	items := []list.Item{
		&modelItem{model: config.Model{ID: "ollama/gone"}, blocked: "ollama/gone is not on disk — pull or download it first"},
		&modelItem{model: config.Model{ID: "ollama/ok"}},
	}
	m := pickModel{list: list.New(items, list.NewDefaultDelegate(), 80, 24)}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pm := next.(pickModel)
	if cmd != nil || pm.selected.ID != "" || !strings.Contains(pm.notice, "not on disk") {
		t.Fatalf("blocked Enter: selected=%q notice=%q cmd=%v, want no selection and a notice", pm.selected.ID, pm.notice, cmd)
	}
	pm.list.CursorDown()
	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := next.(pickModel).selected.ID; got != "ollama/ok" {
		t.Fatalf("selected = %q, want ollama/ok", got)
	}
}

// TestPickStartModelIgnoresLaunchRoutes verifies the start-only picker does
// not apply launch-route rules: with LiteLLM on, a discovered row (not in the
// proxy's model_list) and a start row whose route cannot resolve stay
// selectable start rows, a model that is not on disk has no row, and plain
// PickModel behaviour (route blocking) is unchanged. Without this, `wt start`'s
// picker would refuse models that `wt start <id>` starts fine.
func TestPickStartModelIgnoresLaunchRoutes(t *testing.T) {
	stubUsageStore(t)
	stubRefcountStore(t)
	stubInventory(t, localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", ModelName: "disc", ModelID: "omlx/disc", Artifact: "disc", Registered: false},
		{ProviderID: "omlx", ModelName: "idle", ModelID: "omlx/idle", Artifact: "idle", ArtifactKnown: true, Registered: true},
		{ProviderID: "omlx", ModelName: "gone", ModelID: "omlx/gone", ArtifactKnown: true, Registered: true},
	}})
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Protocols: []config.Protocol{config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
	}
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true}) // unconfigured: routes error
	models := []config.Model{
		{ID: "omlx/disc", ProviderID: "omlx", ModelName: "disc", Location: config.LocationLocal, Source: config.SourceDiscovered},
		{ID: "omlx/idle", ProviderID: "omlx", ModelName: "idle", Location: config.LocationLocal},
		{ID: "omlx/gone", ProviderID: "omlx", ModelName: "gone", Location: config.LocationLocal},
	}
	byID := func(pm pickModel) map[string]*modelItem {
		out := map[string]*modelItem{}
		for _, it := range pm.list.Items() {
			mi := it.(*modelItem)
			out[mi.model.ID] = mi
		}
		return out
	}

	start := byID(newPickModel(cfg, models, themes.Default, true))
	for _, id := range []string{"omlx/disc", "omlx/idle"} {
		if it := start[id]; it == nil || !it.start || it.blocked != "" {
			t.Errorf("start-only %s: %+v, want a selectable start row", id, it)
		}
	}
	if it, ok := start["omlx/gone"]; ok {
		t.Errorf("start-only omlx/gone: %+v, want no row (not on disk)", it)
	}

	plain := byID(newPickModel(cfg, models, themes.Default, false))
	if it := plain["omlx/idle"]; it == nil || it.blocked == "" {
		t.Errorf("plain PickModel omlx/idle: %+v, want blocked by the unresolvable route", it)
	}
}

// rowsPickModel is the standalone picker with the given number of rows, before
// the terminal has reported a size.
func rowsPickModel(t *testing.T, rows int) pickModel {
	t.Helper()
	stubUsageStore(t)
	stubRefcountStore(t)
	stubInventory(t, runningOmlxSnapshot())
	cfg := rowsConfig(rows)
	pm := newPickModel(cfg, cfg.Models, themes.Default, false)
	if got := len(pm.list.Items()); got != rows {
		t.Fatalf("standalone picker has %d rows, want %d", got, rows)
	}
	return pm
}

// TestStandalonePickerFitsWithHelpOpenAndANotice pins that the picker
// `wt start` and `wt smoke` use stays within the terminal when `?` opens the
// full help and Enter on a blocked row then adds its one-line notice — for
// every row count and height, and with the window size reported again while
// the help is open. The expanded help can tip the list onto a second page, and
// the pagination line that appears takes one of the two lines the picker keeps
// spare; the notice needs the other. A view one line over loses the table's
// header line off the top.
//
// A terminal too short for the expanded help itself is not counted: the list
// keeps its least height there, as in the agent flow's picker.
func TestStandalonePickerFitsWithHelpOpenAndANotice(t *testing.T) {
	var helpOnly, withNotice, resizedOpen tooTall
	for rows := 2; rows <= 49; rows++ {
		base := rowsPickModel(t, rows)
		// Enter on the highlighted row must show a notice, not select it.
		base.list.SelectedItem().(*modelItem).blocked = "claude/opus cannot be started here"
		for height := 12; height <= 60; height++ {
			name := fmt.Sprintf("%d rows, height %d", rows, height)
			size := tea.WindowSizeMsg{Width: 80, Height: height}
			next, _ := base.Update(size)
			next, _ = next.Update(helpKey)
			open := next.(pickModel)
			if !open.list.Help.ShowAll {
				t.Fatalf("%s: the full help did not open", name)
			}
			if _, floor := listExtent(open.list, 80); floor > height-2 {
				continue
			}
			helpOnly.check(name, open.View(), height)

			enter := tea.KeyMsg{Type: tea.KeyEnter}
			next, cmd := open.Update(enter)
			noticed := next.(pickModel)
			if cmd != nil || noticed.notice == "" {
				t.Fatalf("%s: Enter on the blocked row: notice = %q cmd = %v, want a notice and no selection", name, noticed.notice, cmd)
			}
			withNotice.check(name, noticed.View(), height)

			next, _ = open.Update(size)
			next, _ = next.Update(enter)
			resizedOpen.check(name, next.View(), height)
		}
	}
	helpOnly.report(t, "standalone picker, help open")
	withNotice.report(t, "standalone picker, help open and a notice showing")
	resizedOpen.report(t, "standalone picker, help open, size reported again, and a notice showing")
}

// TestStandalonePickerNeverSizesItsListToNothing pins that a terminal
// reporting no columns or no lines (some do, briefly, while a window is being
// created) does not hand bubbles a width of zero: the agent flow's picker
// floors the width at one for the same reason, and the two pickers size the
// same table. The view must still render.
func TestStandalonePickerNeverSizesItsListToNothing(t *testing.T) {
	for _, size := range [][2]int{{0, 0}, {0, 24}, {80, 0}} {
		next, _ := rowsPickModel(t, 3).Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		pm := next.(pickModel)
		if pm.list.Width() < 1 {
			t.Errorf("terminal %dx%d: list width = %d, want at least 1", size[0], size[1], pm.list.Width())
		}
		_ = pm.View() // must not panic
	}
}
