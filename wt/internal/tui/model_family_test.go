package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// drainFilterMatches recursively executes cmd (and any nested tea.BatchMsg)
// looking for a list.FilterMatchesMsg, mirroring how the real bubbletea
// runtime resolves a Cmd tree before delivering the resulting Msg back to
// Update. bubbles/list's handleFiltering only QUEUES a filterItems Cmd on
// each keystroke — the visible/filtered item set is not narrowed until that
// Cmd's message is fed back through Update, exactly as the real runtime loop
// would do it. Returns nil if no FilterMatchesMsg is found.
func drainFilterMatches(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	switch v := msg.(type) {
	case list.FilterMatchesMsg:
		return v
	case tea.BatchMsg:
		for _, c := range v {
			if found := drainFilterMatches(c); found != nil {
				return found
			}
		}
	}
	return nil
}

// typeFilterQuery drives m through the model picker's '/' incremental
// filter one rune at a time, draining each keystroke's filterItems Cmd back
// through Update so m.models.VisibleItems() actually narrows — matching
// what a real bubbletea session does. Returns the updated model.
func typeFilterQuery(t *testing.T, m model, query string) model {
	t.Helper()
	for _, r := range query {
		got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		var ok bool
		m, ok = got.(model)
		if !ok {
			t.Fatalf("Update returned %T, want model", got)
		}
		if msg := drainFilterMatches(cmd); msg != nil {
			got, _ = m.Update(msg)
			m, ok = got.(model)
			if !ok {
				t.Fatalf("Update(FilterMatchesMsg) returned %T, want model", got)
			}
		}
	}
	return m
}

// modelFamilies returns a small catalog spanning two named families plus an
// empty-family model, for group/sort/filter tests.
func modelFamilies() []config.Model {
	return []config.Model{
		{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4"},
		{ID: "ollama/gemma4:14b", ProviderID: "ollama", Family: "gemma4"},
		{ID: "ollama/qwen3.8:27b", ProviderID: "ollama", Family: "qwen3.8"},
		{ID: "ollama/loose", ProviderID: "ollama", Family: ""},
	}
}

// TestClampOnFilterNarrow exercises the bubbles v1.0.0 filter-narrowing
// hazard through clampModelSelection: once a filter narrows the visible set,
// bubbles' Select still operates in UNFILTERED coordinates (clamping to
// len(Items()), not len(VisibleItems())), so a stale cursor can point past
// the filtered view — where SelectedItem() silently returns nil and Enter
// no-ops. Narrow the filter to one match, force the cursor out of the
// filtered range, then confirm the clamp pulls it back to the single match.
func TestClampOnFilterNarrow(t *testing.T) {
	stubUsageStore(t)

	m := model{models: compactModelList(t, modelFamilies()), phase: phaseModel, width: 80, height: 24}
	// Open the filter like a user pressing '/', then narrow to exactly one
	// match ("qwen" only matches ollama/qwen3.8:27b).
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = got.(model)
	if m.models.FilterState() != list.Filtering {
		t.Fatalf("filter state = %v, want Filtering", m.models.FilterState())
	}
	m = typeFilterQuery(t, m, "qwen")
	if got := len(m.models.VisibleItems()); got != 1 {
		t.Fatalf("visible items after filter = %d, want 1 (precondition: query narrowed to one match)", got)
	}

	// Force the out-of-range state: Select clamps to len(Items()) (4), not
	// len(VisibleItems()) (1), so index 3 is accepted while only one item
	// is visible.
	m.models.Select(3)
	clampModelSelection(&m)

	if got := m.models.Index(); got != 0 {
		t.Fatalf("after clamp index = %d, want 0 (the only visible row)", got)
	}
	item, ok := m.models.SelectedItem().(*modelItem)
	if !ok || item.model.ID != "ollama/qwen3.8:27b" {
		t.Fatalf("selected item = %v (%T), want the single filtered match ollama/qwen3.8:27b", m.models.SelectedItem(), m.models.SelectedItem())
	}
}

// TestFilterToSingleMatchKeepsSelectionValid verifies that filtering the
// model picker down to exactly one match leaves SelectedItem() pointing at
// that match, not nil. bubbles' GoToStart on filter open resets the cursor,
// and a cursor left outside the narrowed visible set would make
// SelectedItem() silently return nil so Enter no-ops — clampModelSelection
// (run after every models.Update) keeps the cursor inside the filtered view.
func TestFilterToSingleMatchKeepsSelectionValid(t *testing.T) {
	stubUsageStore(t)

	m := model{models: compactModelList(t, modelFamilies()), phase: phaseModel, width: 80, height: 24}

	// Open the filter like a user pressing '/', then type a query matching
	// exactly one model ("qwen" only matches ollama/qwen3.8:27b).
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = got.(model)
	if m.models.FilterState() != list.Filtering {
		t.Fatalf("filter state = %v, want Filtering", m.models.FilterState())
	}
	m = typeFilterQuery(t, m, "qwen")

	if got := len(m.models.VisibleItems()); got != 1 {
		t.Fatalf("visible items after filter = %d, want 1 (precondition: query narrowed to one match)", got)
	}
	item, ok := m.models.SelectedItem().(*modelItem)
	if !ok {
		t.Fatalf("SelectedItem() = %v (%T), want the single filtered match (*modelItem)", m.models.SelectedItem(), m.models.SelectedItem())
	}
	if item.model.ID != "ollama/qwen3.8:27b" {
		t.Fatalf("selected model = %q, want ollama/qwen3.8:27b", item.model.ID)
	}
}

// TestWrapAroundStaysValidAfterFilterApplied verifies that the phaseModel
// wrap-around handler (app.go's tea.KeyMsg case) stays inside the currently
// filtered view. The handler computes its edges from VisibleItems(), which —
// like Index()/Select() once a filter is applied (FilterApplied, not just
// Filtering) — is filtered-coordinate. Wrapping from the top of a filtered
// view must land on the filtered view's last row (a real *modelItem), not an
// out-of-range index whose SelectedItem() would silently be nil.
func TestWrapAroundStaysValidAfterFilterApplied(t *testing.T) {
	stubUsageStore(t)

	m := model{models: compactModelList(t, modelFamilies()), phase: phaseModel, width: 80, height: 24}

	// "ollama" is a substring of every model ID (all IDs are
	// "ollama/<name>"), so this query keeps all 4 models visible — narrowing
	// nothing, so a wrap is meaningful to test.
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = got.(model)
	m = typeFilterQuery(t, m, "ollama")
	if got := len(m.models.VisibleItems()); got != 4 {
		t.Fatalf("visible items after filter = %d, want 4 (precondition: all models still match)", got)
	}
	// Accept the filter (bubbles/list's AcceptWhileFiltering, bound to
	// Enter) so FilterState becomes FilterApplied — the wrap handler's
	// guard only skips Filtering, not FilterApplied.
	got, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = got.(model)
	if m.models.FilterState() != list.FilterApplied {
		t.Fatalf("filter state = %v, want FilterApplied", m.models.FilterState())
	}

	// Up-wrap from the first filtered row: the cursor must land on the
	// filtered view's last row, which stays a valid *modelItem.
	m.models.Select(0)
	got, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = got.(model)

	item, ok := m.models.SelectedItem().(*modelItem)
	if !ok {
		t.Fatalf("after 'k' at filtered index 0: SelectedItem() = %v (%T), want a valid *modelItem (wrap landed on the filtered view's last row)", m.models.SelectedItem(), m.models.SelectedItem())
	}
	// The table sorts rows itself, so the last row is whatever the list's
	// final item is (not a hard-coded id).
	all := m.models.Items()
	if want := all[len(all)-1].(*modelItem).model.ID; item.model.ID != want {
		t.Errorf("wrapped to %q, want %q (last row of the 4-model set)", item.model.ID, want)
	}
}

// TestWrapAroundNoOpWhenZeroOrOneVisibleItem verifies that wrap-around
// keybinds (k/j/up/down) do not panic or select negative indices when
// the visible list contains 0 or 1 item.
func TestWrapAroundNoOpWhenZeroOrOneVisibleItem(t *testing.T) {
	stubUsageStore(t)

	// Single-item list:
	m := model{models: compactModelList(t, []config.Model{
		{ID: "ollama/single", ProviderID: "ollama", Family: "single"},
	}), phase: phaseModel, width: 80, height: 24}

	for _, key := range []rune{'k', 'j'} {
		got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
		m = got.(model)
		if gotIdx := m.models.Index(); gotIdx != 0 {
			t.Errorf("single item: index after %c = %d, want 0", key, gotIdx)
		}
	}

	// Zero-item list:
	m2 := model{models: compactModelList(t, []config.Model{}), phase: phaseModel, width: 80, height: 24}
	if got := len(m2.models.VisibleItems()); got != 0 {
		t.Fatalf("visible items = %d, want 0", got)
	}
	for _, key := range []rune{'k', 'j'} {
		got, _ := m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
		m2 = got.(model)
		if gotIdx := m2.models.Index(); gotIdx < 0 {
			t.Errorf("empty visible items: index after %c = %d, want >= 0", key, gotIdx)
		}
	}
}

// TestEnterModelPhaseCursorOnFirstModel verifies that entering the model
// phase (no rotation history) lands the cursor on the first launchable row —
// index 0, ollama/gemma4:14b in the id-sorted table — not the registry-first
// model. Pins the cold-start position so rotation cannot displace it.
func TestEnterModelPhaseCursorOnFirstModel(t *testing.T) {
	stubUsageStore(t)
	tempStateDir(t)

	cfg := testConfig()
	m := model{
		cfg:          cfg,
		agent:        "claude",
		tag:          "code",
		activeTags:   "code",
		activeFamily: "",
		theme:        themes.Default,
		width:        80,
		height:       24,
	}
	fullCatalog, err := cfg.ModelsForAgent("claude")
	if err != nil {
		t.Fatalf("ModelsForAgent: %v", err)
	}
	models, err := cfg.EligibleModelsIn("claude", fullCatalog, "code", "")
	if err != nil {
		t.Fatalf("EligibleModelsIn: %v", err)
	}
	got, _ := m.enterModelPhase("claude", models, "code")
	if got.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel", got.phase)
	}
	it, ok := got.models.Items()[got.models.Index()].(*modelItem)
	if !ok {
		t.Fatalf("cursor at %d is %T; want a *modelItem", got.models.Index(), got.models.Items()[got.models.Index()])
	}
	if got.models.Index() != 0 || it.model.ID != "ollama/gemma4:14b" {
		t.Errorf("cursor at %d on %q, want index 0 on ollama/gemma4:14b", got.models.Index(), it.model.ID)
	}
}
