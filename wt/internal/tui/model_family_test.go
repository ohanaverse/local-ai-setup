package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
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

// familyOfFor returns the full familyOf map for modelFamilies().
func familyOfFor() map[string]string {
	return map[string]string{
		"ollama/gemma4:9b":   "gemma4",
		"ollama/gemma4:14b":  "gemma4",
		"ollama/qwen3.8:27b": "qwen3.8",
		"ollama/loose":       "",
	}
}

// TestSortModelsByUsageGroupsByFamilyScoreThenModelScore verifies the sort
// key: family composite desc, then model composite desc within a family.
func TestSortModelsByUsageGroupsByFamilyScoreThenModelScore(t *testing.T) {
	models := modelFamilies()
	familyCounts := map[string]usage.UsageCounts{
		"gemma4":  {ThirtyDay: 10}, // composite 20
		"qwen3.8": {ThirtyDay: 5},  // composite 10
		"":        {ThirtyDay: 1},  // composite 2
	}
	modelCounts := map[string]usage.UsageCounts{
		"ollama/gemma4:9b":   {ThirtyDay: 6}, // composite 12
		"ollama/gemma4:14b":  {ThirtyDay: 4}, // composite 8
		"ollama/qwen3.8:27b": {ThirtyDay: 5}, // composite 10
		"ollama/loose":       {ThirtyDay: 1}, // composite 2
	}
	sortModelsByUsage(models, familyCounts, modelCounts)

	var families []string
	for _, m := range models {
		families = append(families, m.Family)
	}
	if got := strings.Join(families, ","); got != "gemma4,gemma4,qwen3.8," {
		t.Fatalf("family order = %q, want %q", got, "gemma4,gemma4,qwen3.8,")
	}
	if models[0].ID != "ollama/gemma4:9b" || models[1].ID != "ollama/gemma4:14b" {
		t.Fatalf("gemma4 models not internally sorted by model score: %s, %s", models[0].ID, models[1].ID)
	}
	if models[2].ID != "ollama/qwen3.8:27b" {
		t.Fatalf("third model = %s, want qwen3.8", models[2].ID)
	}
}

// TestSortModelsByUsageKeepsTiedFamiliesAdjacent verifies that when two
// families have equal composite scores (the common case: a fresh install
// with no usage history, where every score is 0), the sort still groups each
// family's models adjacently instead of leaving them interleaved in whatever
// order the registry lists them. Without a tie-break beyond composite score,
// sort.SliceStable's stability preserves registry order on a tie, and the
// real registry.toml interleaves families (e.g. gemma4 models are not
// contiguous) — so the compact picker's leftmost family column would
// alternate between families row to row instead of grouping visually, and
// the old divider layout would have emitted a duplicate "◈ gemma4" header
// split across two non-adjacent runs. The tie-break is each family's
// first-occurrence position in the input slice, so ties resolve to registry
// order at the family level (gemma4 first, since it appears first) while
// still keeping every family's models contiguous.
func TestSortModelsByUsageKeepsTiedFamiliesAdjacent(t *testing.T) {
	// Registry order interleaves gemma4 and qwen3.8; both families and all
	// models are tied at zero usage (fresh install).
	models := []config.Model{
		{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4"},
		{ID: "ollama/qwen3.8:27b", ProviderID: "ollama", Family: "qwen3.8"},
		{ID: "ollama/gemma4:14b", ProviderID: "ollama", Family: "gemma4"},
	}
	sortModelsByUsage(models, nil, nil)

	var families []string
	for _, m := range models {
		families = append(families, m.Family)
	}
	// gemma4 appears first in the input, so its tie-break wins; both its
	// models must be contiguous rather than split around qwen3.8.
	if got := strings.Join(families, ","); got != "gemma4,gemma4,qwen3.8" {
		t.Fatalf("family order = %q, want gemma4,gemma4,qwen3.8 (tied families kept adjacent, in first-occurrence order)", got)
	}
}

// TestAdjacentModelsShareFamilyColumn verifies the compact picker keeps the
// family-grouping invariant without divider rows: adjacent same-family
// models carry the same leftmost family column on their line (the family
// column is what the deleted divider headers used to communicate).
func TestAdjacentModelsShareFamilyColumn(t *testing.T) {
	stubUsageStore(t) // all-zero usage: registry order preserved

	items := buildModelItems(modelFamilies(), newUsageStore())
	if len(items) != 4 {
		t.Fatalf("got %d items, want 4", len(items))
	}
	famCol := func(line string) string {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			t.Fatalf("line %q has no family column", line)
		}
		return fields[0]
	}
	// With all scores tied at zero, items[0] and items[1] are the gemma4
	// pair in registry order; both lines must lead with the same family.
	if got := famCol(items[0].line); got != "gemma4" {
		t.Fatalf("items[0] family column = %q, want gemma4", got)
	}
	if got := famCol(items[1].line); got != "gemma4" {
		t.Fatalf("items[1] family column = %q, want gemma4 (adjacent rows of the same family share the family column)", got)
	}
}

// TestBuildModelItemsFamilyColumnShowsFamilyTotal verifies the family 30d
// column aggregates usage across the family's models, not just the row's own
// model: only gemma4:14b has a recorded launch, but every gemma4 row's
// family column must show the family total (1).
func TestBuildModelItemsFamilyColumnShowsFamilyTotal(t *testing.T) {
	store := stubUsageStore(t)
	if err := store.Record("ollama/gemma4:14b"); err != nil {
		t.Fatalf("seed usage event: %v", err)
	}

	models := []config.Model{
		{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4"},
		{ID: "ollama/gemma4:14b", ProviderID: "ollama", Family: "gemma4"},
	}
	items := buildModelItems(models, store)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	for _, it := range items {
		fields := strings.Fields(it.line)
		if len(fields) < 2 {
			t.Fatalf("line %q too short to hold a family column + 30d count", it.line)
		}
		if fields[0] != "gemma4" || fields[1] != "1" {
			t.Fatalf("line = %q, want family column %q with 30d aggregate %q (family total must include sibling launches)", it.line, "gemma4", "1")
		}
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
	if item.model.ID != "ollama/loose" {
		t.Errorf("wrapped to %q, want ollama/loose (last row of the 4-model set)", item.model.ID)
	}
}

// TestEnterModelPhaseCursorOnFirstModel verifies that entering the model
// phase (no rotation history) lands the cursor on a model row — index 0 in
// the compact, divider-free layout. (There are no divider rows to skip
// anymore; this pins that the cursor starts on a launchable *modelItem.)
func TestEnterModelPhaseCursorOnFirstModel(t *testing.T) {
	stubUsageStore(t)

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
	models, err := cfg.EligibleModels("claude", "code", "")
	if err != nil {
		t.Fatalf("EligibleModels: %v", err)
	}
	got, _ := m.enterModelPhase("claude", models, "code")
	if got.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel", got.phase)
	}
	if _, ok := got.models.Items()[got.models.Index()].(*modelItem); !ok {
		t.Fatalf("cursor at %d is %T; want a *modelItem", got.models.Index(), got.models.Items()[got.models.Index()])
	}
}
