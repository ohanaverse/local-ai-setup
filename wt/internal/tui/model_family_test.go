package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// modelFamilies returns a small catalog spanning two named families plus an
// empty-family model, for group/divider/sort tests.
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
// contiguous), so withFamilyDividers would emit a duplicate "◈ gemma4"
// header split across two non-adjacent runs — contradicting both the sort's
// own doc comment ("Same-family models end up adjacent") and the picker's
// family-grouping contract. The tie-break is each family's first-occurrence
// position in the input slice, so ties resolve to registry order at the
// family level (gemma4 first, since it appears first) while still keeping
// every family's models contiguous.
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

// TestWithFamilyDividersInsertsHeaderPerGroup verifies a divider row precedes
// each distinct family group, model rows carry their family's 30d count, and
// the empty family group is labeled otherFamily.
func TestWithFamilyDividersInsertsHeaderPerGroup(t *testing.T) {
	models := modelFamilies()
	familyCounts := map[string]usage.UsageCounts{
		"gemma4": {ThirtyDay: 7},
		"":       {ThirtyDay: 2},
	}
	modelCounts := map[string]usage.UsageCounts{}
	items := withFamilyDividers(models, modelCounts, familyCounts)

	type row struct {
		divider bool
		label   string
		id      string
		fam     int
	}
	var rows []row
	for _, it := range items {
		switch v := it.(type) {
		case dividerItem:
			rows = append(rows, row{divider: true, label: v.label})
		case modelItem:
			rows = append(rows, row{id: v.model.ID, fam: v.familyCount})
		}
	}
	if len(rows) != 7 {
		t.Fatalf("got %d rows, want 7 (3 dividers + 4 models)", len(rows))
	}
	if !rows[0].divider || !strings.Contains(rows[0].label, "gemma4") || !strings.Contains(rows[0].label, "30d:7") {
		t.Fatalf("rows[0] = %+v, want gemma4 divider with 30d:7", rows[0])
	}
	if rows[1].id != "ollama/gemma4:9b" || rows[1].fam != 7 {
		t.Fatalf("rows[1] = %+v, want first gemma4 model with familyCount 7", rows[1])
	}
	var lastDivider string
	for _, r := range rows {
		if r.divider {
			lastDivider = r.label
		}
	}
	if !strings.Contains(lastDivider, otherFamily) {
		t.Fatalf("empty-family divider label %q does not contain %q", lastDivider, otherFamily)
	}
}

// TestModelListDelegateRenderHasNoTrailingNewline verifies dividerItem.Render
// does not emit a trailing newline. bubbles/list's populatedView joins rows
// with strings.Repeat("\n", Spacing()+1) and DefaultDelegate.Render writes
// "title\ndesc" with no trailing newline; a divider's Render writing
// label+"\n" therefore doubles the blank-line gap under every family header
// (one newline from the row's own content, plus Spacing()+1 from the join)
// compared to the single-newline gap between model rows.
func TestModelListDelegateRenderHasNoTrailingNewline(t *testing.T) {
	base := ThemedListDelegate(themes.Default)
	dl := modelListDelegate{DefaultDelegate: base, headerStyle: base.Styles.NormalTitle}
	var buf strings.Builder
	dl.Render(&buf, list.Model{}, 0, dividerItem{label: "◈ gemma4 · 30d:7"})
	if strings.HasSuffix(buf.String(), "\n") {
		t.Fatalf("divider render = %q, must not end in a newline (would double-space under populatedView's own join separator)", buf.String())
	}
}

// TestModelListDelegateRendersDivider verifies the divider row renders its
// header label (not a model row).
func TestModelListDelegateRendersDivider(t *testing.T) {
	base := ThemedListDelegate(themes.Default)
	dl := modelListDelegate{DefaultDelegate: base, headerStyle: base.Styles.NormalTitle}
	var buf strings.Builder
	dl.Render(&buf, list.Model{}, 0, dividerItem{label: "◈ gemma4 · 30d:7"})
	if !strings.Contains(buf.String(), "◈ gemma4 · 30d:7") {
		t.Fatalf("divider render = %q, want its label", buf.String())
	}
}

// TestBuildModelListWithFamiliesIdIndex verifies the returned modelID→list
// index map accounts for inserted divider rows and always points at model
// rows.
func TestBuildModelListWithFamiliesIdIndex(t *testing.T) {
	old := newUsageStore
	newUsageStore = func() *usage.Store { return usage.NewStoreAt(t.TempDir()) }
	defer func() { newUsageStore = old }()

	models := modelFamilies()
	ml, idIndex := buildModelListWithFamilies(models, familyOfFor(), themes.Default, 80, 24)
	if got := idIndex["ollama/qwen3.8:27b"]; got != 4 {
		t.Fatalf("qwen3.8 index = %d, want 4 (leading divider + gemma4 pair = 3 items before)", got)
	}
	if len(idIndex) != 4 {
		t.Fatalf("idIndex has %d entries, want 4", len(idIndex))
	}
	for id, idx := range idIndex {
		if _, ok := ml.Items()[idx].(modelItem); !ok {
			t.Fatalf("idIndex[%s]=%d points at non-model item %T", id, idx, ml.Items()[idx])
		}
	}
	if first, ok := ml.Items()[0].(dividerItem); !ok || first.label == "" {
		t.Fatal("first list item should be a non-empty family divider")
	}
}

// TestSnapSelectionOnModelMovesOffDivider verifies a cursor resting on a
// divider is snapped to a model row (forward preferred when the direction is
// unknown). The assertion inspects m.models because list.Model stores its
// cursor by value and snapSelectionOnModel mutates the model's own list copy.
func TestSnapSelectionOnModelMovesOffDivider(t *testing.T) {
	old := newUsageStore
	newUsageStore = func() *usage.Store { return usage.NewStoreAt(t.TempDir()) }
	defer func() { newUsageStore = old }()

	ml, _ := buildModelListWithFamilies(modelFamilies(), familyOfFor(), themes.Default, 80, 24)
	ml.Select(3) // index 3 is the qwen3.8 divider
	m := model{models: ml}
	m.snapSelectionOnModel(-1) // unknown direction: forward preferred
	if got := m.models.Index(); got != 4 {
		t.Fatalf("after snap index = %d, want 4 (first model of qwen3.8 group)", got)
	}
}

// TestSnapSelectionOnModelContinuesUpwardAcrossDivider verifies pressing UP
// onto a divider continues up into the previous family group, so a model
// right after a divider is never stuck (up is a real move, not a no-op).
// Fixture layout: [div gemma4(0), 9b(1), 14b(2), div qwen3.8(3), qwen3.8(4),
// div other(5), loose(6)].
func TestSnapSelectionOnModelContinuesUpwardAcrossDivider(t *testing.T) {
	old := newUsageStore
	newUsageStore = func() *usage.Store { return usage.NewStoreAt(t.TempDir()) }
	defer func() { newUsageStore = old }()

	ml, _ := buildModelListWithFamilies(modelFamilies(), familyOfFor(), themes.Default, 80, 24)
	// Up from qwen3.8(4) lands on divider(3).
	ml.Select(3)
	m := model{models: ml}
	m.snapSelectionOnModel(4) // moved backward 4→3
	if got := m.models.Index(); got != 2 {
		t.Fatalf("after up-snap index = %d, want 2 (last model of gemma4 group)", got)
	}
}

// TestSnapSelectionOnModelContinuesDownwardAcrossDivider verifies pressing
// DOWN onto a divider continues down into the next family group, so a model
// right before a divider is never stuck.
func TestSnapSelectionOnModelContinuesDownwardAcrossDivider(t *testing.T) {
	old := newUsageStore
	newUsageStore = func() *usage.Store { return usage.NewStoreAt(t.TempDir()) }
	defer func() { newUsageStore = old }()

	ml, _ := buildModelListWithFamilies(modelFamilies(), familyOfFor(), themes.Default, 80, 24)
	// Down from 14b(2) lands on divider(3).
	ml.Select(3)
	m := model{models: ml}
	m.snapSelectionOnModel(2) // moved forward 2→3
	if got := m.models.Index(); got != 4 {
		t.Fatalf("after down-snap index = %d, want 4 (first model of qwen3.8 group)", got)
	}
}

// TestEnterModelPhaseCursorNeverOnDivider verifies that entering the model
// phase (no rotation history) lands the cursor on a model row, not the
// leading family divider. testConfig() models share an empty family, so the
// first list item is the "— other" divider.
func TestEnterModelPhaseCursorNeverOnDivider(t *testing.T) {
	old := newUsageStore
	newUsageStore = func() *usage.Store { return usage.NewStoreAt(t.TempDir()) }
	defer func() { newUsageStore = old }()

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
	if _, ok := got.models.Items()[got.models.Index()].(modelItem); !ok {
		t.Fatalf("cursor at %d is %T; want a modelItem", got.models.Index(), got.models.Items()[got.models.Index()])
	}
}
