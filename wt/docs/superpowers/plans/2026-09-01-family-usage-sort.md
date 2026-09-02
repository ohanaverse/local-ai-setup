# Family-Usage Sort for the Model Picker — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Count model-launch usage by family in addition to model, and sort the interactive model picker descending by family usage then model usage (recency-weighted composite) so the user can consciously vary families.

**Architecture:** Two layers. (1) `internal/usage` gains `CompositeScore` (a recency-weighted integer sort key) and `Store.FamilyCounts` (aggregates `usage.jsonl` events by family via a model-id→family map). (2) `internal/tui` becomes family-aware: eligible models are stably sorted by family-then-model composite score, family header rows (divider items) are inserted between groups, a wrapping list delegate renders dividers as unhighlighted headers, selection/cursor logic snaps off dividers, and `rotation.Next` still positions the cursor on the next-to-use model. No schema change to `usage.jsonl`; non-TUI `resolve.go` path unchanged.

**Tech Stack:** Go, Bubble Tea (`github.com/charmbracelet/bubbles/list`), `github.com/charmbracelet/lipgloss`. Run tests from `wt/`.

---

### Task 1: Add `CompositeScore` and `Store.FamilyCounts` to `internal/usage`

**Files:**
- Modify: `wt/internal/usage/usage.go` (append to file)
- Test: `wt/internal/usage/usage_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `wt/internal/usage/usage_test.go`:

```go
// TestCompositeScore verifies the recency weights: recent launches dominate.
// Formula: 3*OneDay + SevenDay + 2*ThirtyDay.
func TestCompositeScore(t *testing.T) {
	c := UsageCounts{OneDay: 2, SevenDay: 5, ThirtyDay: 10}
	got := CompositeScore(c)
	if got != 31 { // 3*2 + 5 + 2*10 = 31
		t.Fatalf("CompositeScore = %d, want 31", got)
	}
	older := UsageCounts{OneDay: 1, SevenDay: 5, ThirtyDay: 10}
	if got <= CompositeScore(older) {
		t.Fatal("more recent launches should score strictly higher")
	}
}

// TestFamilyCountsAggregatesByFamily verifies events are grouped by family
// via the familyOf map, that all catalog models in one family aggregate
// together, and that events for models absent from the catalog are skipped.
func TestFamilyCountsAggregatesByFamily(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	fixed := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return fixed }
	defer func() { now = time.Now }()

	var data []byte
	for _, e := range []event{
		{ModelID: "ollama/gemma4:9b", Timestamp: fixed.Add(-30 * time.Minute)},
		{ModelID: "ollama/gemma4:14b", Timestamp: fixed.Add(-2 * 24 * time.Hour)},
		{ModelID: "ollama/qwen3.8:27b", Timestamp: fixed.Add(-30 * time.Minute)},
		{ModelID: "ollama/notincatalog:1", Timestamp: fixed.Add(-30 * time.Minute)},
	} {
		line, _ := json.Marshal(e)
		data = append(data, append(line, '\n')...)
	}
	_ = os.WriteFile(store.path(), data, 0o600)

	familyOf := map[string]string{
		"ollama/gemma4:9b":  "gemma4",
		"ollama/gemma4:14b": "gemma4",
		"ollama/qwen3.8:27b": "qwen3.8",
	}
	got := store.FamilyCounts(familyOf)
	if got["gemma4"] != (UsageCounts{OneDay: 1, SevenDay: 2, ThirtyDay: 2}) {
		t.Fatalf("gemma4 = %+v, want {1,2,2}", got["gemma4"])
	}
	if got["qwen3.8"] != (UsageCounts{OneDay: 1, SevenDay: 1, ThirtyDay: 1}) {
		t.Fatalf("qwen3.8 = %+v, want {1,1,1}", got["qwen3.8"])
	}
	if _, ok := got["notincatalog"]; ok {
		t.Fatalf("family for a model outside the catalog should be skipped, got %v", got["notincatalog"])
	}
}

// TestFamilyCountsMissingFile returns an empty map when usage.jsonl is absent.
func TestFamilyCountsMissingFile(t *testing.T) {
	dir := t.TempDir()
	got := NewStoreAt(dir).FamilyCounts(map[string]string{"a": "fam"})
	if len(got) != 0 {
		t.Fatalf("FamilyCounts = %v, want empty", got)
	}
}
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./internal/usage -run 'TestCompositeScore|TestFamilyCounts' -v`
Expected: FAIL (undefined: `CompositeScore`, `FamilyCounts`).

- [ ] **Step 3: Implement `CompositeScore` and `FamilyCounts`**

Append to `wt/internal/usage/usage.go` (after the `Counts` method):

```go
// CompositeScore is a recency-weighted sort key for a UsageCounts bucket.
// Each launch is counted exactly once, weighted by how fresh it is:
// today's launches ≈3x, 1-7 days ≈1.5x, 8-30 days ≈1x. Expressed as
// integer math (x2), 6*OneDay + 3*(SevenDay-OneDay) + 2*(ThirtyDay-SevenDay)
// simplifies to 3*OneDay + SevenDay + 2*ThirtyDay.
func CompositeScore(c UsageCounts) int {
	return 3*c.OneDay + c.SevenDay + 2*c.ThirtyDay
}

// FamilyCounts returns 1d/7d/30d launch counts aggregated by model family.
// familyOf maps every known model ID to its family and is built from the
// full registry catalog (not the currently-eligible subset) so a family's
// total usage is accurate even when a tag/family filter exposes only some of
// its models. Events whose model ID is absent from familyOf are skipped. A
// missing or unreadable usage file yields an empty map.
func (s *Store) FamilyCounts(familyOf map[string]string) map[string]UsageCounts {
	out := map[string]UsageCounts{}
	f, err := os.Open(s.path())
	if err != nil {
		return out
	}
	defer f.Close()

	today := now().UTC()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var ev event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		family, found := familyOf[ev.ModelID]
		if !found {
			continue
		}
		c := out[family]
		age := today.Sub(ev.Timestamp.UTC())
		if age < 24*time.Hour {
			c.OneDay++
		}
		if age < 7*24*time.Hour {
			c.SevenDay++
		}
		if age < retentionWindow {
			c.ThirtyDay++
		}
		out[family] = c
	}
	return out
}
```

- [ ] **Step 4: Run the new tests to verify they pass**

Run: `go test ./internal/usage -run 'TestCompositeScore|TestFamilyCounts' -v`
Expected: PASS.

- [ ] **Step 5: Run the full usage package tests**

Run: `go test ./internal/usage`
Expected: PASS (existing tests unaffected).

- [ ] **Step 6: Commit**

```bash
git add internal/usage/usage.go internal/usage/usage_test.go
git commit -m "feat(usage): family counts and composite score"
```

---

### Task 2: Add sort/group helpers, divider rendering, and the family builder to `internal/tui`

**Files:**
- Modify: `wt/internal/tui/model_list.go`
- Create (test): `wt/internal/tui/model_family_test.go`

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/tui/model_family_test.go`:

```go
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
		"ollama/gemma4:9b": "gemma4",
		"ollama/gemma4:14b": "gemma4",
		"ollama/qwen3.8:27b": "qwen3.8",
		"ollama/loose": "",
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
		"ollama/gemma4:9b":   {ThirtyDay: 6},  // composite 12
		"ollama/gemma4:14b":  {ThirtyDay: 4},  // composite 8
		"ollama/qwen3.8:27b": {ThirtyDay: 5},  // composite 10
		"ollama/loose":       {ThirtyDay: 1},  // composite 2
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

// TestModelListDelegateRendersDivider verifies the divider row renders its
// header label (not a model row).
func TestModelListDelegateRendersDivider(t *testing.T) {
	base := ThemedListDelegate(themes.Default)
	dl := modelListDelegate{DefaultDelegate: base, headerStyle: base.NormalTitle}
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
// divider is snapped to the nearest model row (forward preferred).
func TestSnapSelectionOnModelMovesOffDivider(t *testing.T) {
	ml, _ := buildModelListWithFamilies(modelFamilies(), familyOfFor(), themes.Default, 80, 24)
	ml.Select(3) // index 3 is the qwen3.8 divider
	m := model{models: ml}
	m.snapSelectionOnModel()
	if _, ok := ml.Items()[ml.Index()].(modelItem); !ok {
		t.Fatalf("selection not on a model after snap: index %d is %T", ml.Index(), ml.Items()[ml.Index()])
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui -run 'TestSortModelsByUsage|TestWithFamilyDividers|TestModelListDelegateRendersDivider|TestBuildModelListWithFamilies|TestSnapSelectionOnModel' -v`
Expected: FAIL (compile errors: `sortModelsByUsage`, `dividerItem`, `modelListDelegate`, `buildModelListWithFamilies`, `familyOfFor`, `snapSelectionOnModel`, `familyDividerLabel`, `otherFamily` undefined).

- [ ] **Step 3: Implement the helpers in `model_list.go`**

Add `"io"` and `"sort"` to the import block in `wt/internal/tui/model_list.go` (current imports: `fmt`, `strings`, `list`, `lipgloss`, `config`, `themes`, `usage`). Then append:

```go
// otherFamily is the display label for models whose Family is empty.
const otherFamily = "— other"

// familyDividerLabel renders a family-group header: the family name (or
// otherFamily when empty) plus its 30-day launch count.
func familyDividerLabel(family string, thirtyDay int) string {
	if family == "" {
		family = otherFamily
	}
	return fmt.Sprintf("◈ %s · 30d:%d", family, thirtyDay)
}

// dividerItem is a non-selectable family header row in the model picker. It
// renders the family name plus its 30-day count, separating model rows of
// different families. It is never a launch target.
type dividerItem struct {
	label string
}

// FilterValue returns "" so dividers never match the list's fuzzy filter
// (filtering shows only model rows).
func (d dividerItem) FilterValue() string { return "" }

// modelListDelegate wraps ThemedListDelegate so dividerItem rows render as
// unhighlighted family headers regardless of the cursor position. Model rows
// fall through to the themed DefaultDelegate. Embedding the value (not a
// pointer) preserves Height/Spacing/Update/ShortHelp/FullHelp from
// DefaultDelegate, all of which have value receivers.
type modelListDelegate struct {
	list.DefaultDelegate
	headerStyle lipgloss.Style
}

func (d modelListDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	if dv, ok := item.(dividerItem); ok {
		_, _ = w.Write([]byte(d.headerStyle.Render(dv.label) + "\n"))
		return
	}
	d.DefaultDelegate.Render(w, m, index, item)
}

// sortModelsByUsage stably sorts models descending by family composite score,
// then model composite score. Same-family models end up adjacent and
// higher-usage families float to the top. familyCounts and modelCounts come
// from usage.Store (missing entries read as zero, scoring 0 for a stable
// tie-break that preserves registry order).
func sortModelsByUsage(models []config.Model, familyCounts, modelCounts map[string]usage.UsageCounts) {
	sort.SliceStable(models, func(i, j int) bool {
		fi := usage.CompositeScore(familyCounts[models[i].Family])
		fj := usage.CompositeScore(familyCounts[models[j].Family])
		if fi != fj {
			return fi > fj
		}
		return usage.CompositeScore(modelCounts[models[i].ID]) >
			usage.CompositeScore(modelCounts[models[j].ID])
	})
}

// withFamilyDividers returns list items for the (already usage-sorted) models,
// inserting a dividerItem before each distinct family group. Each model row
// carries its own 1d/7d/30d counts plus its family's 30-day count for display.
func withFamilyDividers(models []config.Model, modelCounts, familyCounts map[string]usage.UsageCounts) []list.Item {
	items := make([]list.Item, 0, len(models)*2)
	prevFamily := ""
	havePrev := false
	for _, m := range models {
		fam := m.Family
		if !havePrev || fam != prevFamily {
			items = append(items, dividerItem{label: familyDividerLabel(fam, familyCounts[fam].ThirtyDay)})
			prevFamily = fam
			havePrev = true
		}
		items = append(items, modelItem{
			model:       m,
			counts:      modelCounts[m.ID],
			familyCount: familyCounts[fam].ThirtyDay,
		})
	}
	return items
}
```

- [ ] **Step 4: Add the `familyCount` field to `modelItem` and update `Description`**

In `model_list.go`, change the `modelItem` struct to add a field, and update `Description` to surface it:

```go
type modelItem struct {
	model       config.Model
	counts      usage.UsageCounts
	familyCount int // 30-day launches for the model's family
}
```

Replace the `Description` method body with:

```go
// Description renders the metadata columns: provider, location, tags, family
// usage, and 1d/7d/30d model usage. Family usage helps the user see the sort
// basis and spot family-selection bias.
func (m modelItem) Description() string {
	tags := strings.Join(m.model.Tags, ",")
	if tags == "" {
		tags = "-"
	}
	return fmt.Sprintf("%-10s %-6s %-20s fam:%-3d 1d:%-3d 7d:%-3d 30d:%-3d",
		m.model.ProviderID,
		string(m.model.Location),
		tags,
		m.familyCount,
		m.counts.OneDay,
		m.counts.SevenDay,
		m.counts.ThirtyDay,
	)
}
```

- [ ] **Step 5: Add `buildModelListWithFamilies`, `firstModelIndex`, `lastModelIndex`**

Append to `model_list.go`:

```go
// buildModelListWithFamilies builds the usage-sorted, family-grouped model
// picker list. It sorts eligible models by family-then-model usage, inserts
// family divider headers, and returns the list plus a modelID→list-index map
// so callers can position the cursor (bypassing divider rows). familyOf maps
// the FULL catalog's model IDs to their families for family-count
// aggregation, so a family's total usage is accurate even when a tag/family
// filter narrows the eligible set.
func buildModelListWithFamilies(models []config.Model, familyOf map[string]string, theme themes.Theme, width, height int) (list.Model, map[string]int) {
	store := usage.NewStore()
	modelCounts := store.Counts(modelIDs(models))
	familyCounts := store.FamilyCounts(familyOf)

	sortModelsByUsage(models, familyCounts, modelCounts)
	items := withFamilyDividers(models, modelCounts, familyCounts)

	delegate := modelListDelegate{
		DefaultDelegate: ThemedListDelegate(theme),
		headerStyle: lipgloss.NewStyle().
			Foreground(theme.Token(themes.TokenHeader)).
			Bold(true),
	}
	ml := list.New(items, delegate, width, height)
	ml.Title = "Models"
	ml.SetShowStatusBar(false)

	idIndex := make(map[string]int, len(models))
	for i, it := range items {
		if mi, ok := it.(modelItem); ok {
			idIndex[mi.model.ID] = i
		}
	}
	return ml, idIndex
}

// firstModelIndex returns the index of the first model row in items (skipping
// leading family dividers), or -1 if there are no model rows.
func firstModelIndex(items []list.Item) int {
	for i, it := range items {
		if _, ok := it.(modelItem); ok {
			return i
		}
	}
	return -1
}

// lastModelIndex returns the index of the last model row in items, or -1.
func lastModelIndex(items []list.Item) int {
	for i := len(items) - 1; i >= 0; i-- {
		if _, ok := items[i].(modelItem); ok {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 6: Run the focused tests to verify they pass**

Run: `go test ./internal/tui -run 'TestSortModelsByUsage|TestWithFamilyDividers|TestModelListDelegateRendersDivider|TestBuildModelListWithFamilies|TestSnapSelectionOnModel' -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/model_list.go internal/tui/model_family_test.go
git commit -m "feat(tui): family-sorted, divider-grouped model list helpers"
```

---

### Task 3: Wire the family builder into `enterModelPhase` and keep the cursor off dividers

**Files:**
- Modify: `wt/internal/tui/app.go` (`enterModelPhase`, model-phase `Update` wrap-around, model-phase `Update` block)
- Test: `wt/internal/tui/model_family_test.go` (additions)

- [ ] **Step 1: Write the failing integration test**

Append to `wt/internal/tui/model_family_test.go`:

```go
// TestEnterModelPhaseCursorNeverOnDivider verifies that entering the model
// phase (no rotation history) lands the cursor on a model row, not the
// leading family divider. testConfig() models share an empty family, so the
// first list item is the "— other" divider.
func TestEnterModelPhaseCursorNeverOnDivider(t *testing.T) {
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/tui -run TestEnterModelPhaseCursorNeverOnDivider -v`
Expected: FAIL — the initial list index is 0, which is the leading divider, so the cursor lands on `dividerItem`, not `modelItem`.

- [ ] **Step 3: Rewrite `enterModelPhase`**

Replace the whole existing `enterModelPhase` function in `wt/internal/tui/app.go` (currently lines ~633–667) with:

```go
func (m model) enterModelPhase(agent string, models []config.Model, firstTag string) (model, tea.Cmd) {
	m.tag = firstTag

	// Validate a -M pin BEFORE any list construction: a bad pin routes back to
	// the agent picker without scanning usage.jsonl or wrapping models. The
	// eligible slice is the source of truth; a pin that matches at all is one
	// of these models.
	if m.pinnedModel != "" && indexOfModelID(models, m.pinnedModel) < 0 {
		m.status = fmt.Sprintf("model %q is not in the eligible list for agent %q", m.pinnedModel, agent)
		m.pinnedModel = ""
		items := buildAgentList(m.cfg)
		m.agentList = list.New(items, ThemedListDelegate(m.theme), m.width-2, m.height-2)
		m.agentList.Title = "Pick an agent or command"
		m.agentList.SetShowStatusBar(false)
		m.phase = phaseAgent
		return m, nil
	}

	// Build the sorted, family-grouped model list once. Family counts come
	// from the full catalog (m.cfg.Models), so a family's total usage is
	// accurate even when the -T/-F filters narrow the eligible models.
	familyOf := make(map[string]string, len(m.cfg.Models))
	for _, mm := range m.cfg.Models {
		familyOf[mm.ID] = mm.Family
	}
	ml, idIndex := buildModelListWithFamilies(models, familyOf, m.theme, m.width-2, m.height-2)
	m.models = ml

	// Pinned model: select its true list index (divider-aware) and launch.
	if m.pinnedModel != "" {
		m.models.Select(idIndex[m.pinnedModel])
		return m.proceedToLaunch()
	}

	// Unpinned: prefer the rotation's next-to-use model; otherwise start at
	// the first model row (skipping leading family dividers).
	posSet := false
	if next, ok := rotation.New().Next(m.cfg, agent, m.activeTags, m.activeFamily); ok {
		if idx, ok := idIndex[next.ID]; ok {
			m.models.Select(idx)
			posSet = true
		}
	}
	if !posSet {
		if fi := firstModelIndex(m.models.Items()); fi >= 0 {
			m.models.Select(fi)
		}
	}

	if len(models) == 1 {
		return m.proceedToLaunch()
	}
	m.phase = phaseModel
	return m, nil
}
```

- [ ] **Step 4: Add the `snapSelectionOnModel` method**

Append to `wt/internal/tui/model_list.go`:

```go
// snapSelectionOnModel keeps the model-picker cursor on a model row, never on
// a family divider. Called after any navigation so a divider can't be
// highlighted or launched. Prefers the next row, then the previous.
func (m *model) snapSelectionOnModel() {
	items := m.models.Items()
	idx := m.models.Index()
	if idx < 0 || idx >= len(items) {
		return
	}
	if _, ok := items[idx].(dividerItem); ok {
		for i := idx + 1; i < len(items); i++ {
			if _, ok := items[i].(modelItem); ok {
				m.models.Select(i)
				return
			}
		}
		for i := idx - 1; i >= 0; i-- {
			if _, ok := items[i].(modelItem); ok {
				m.models.Select(i)
				return
			}
		}
	}
}
```

- [ ] **Step 5: Make the model-phase wrap-around divider-aware**

In `wt/internal/tui/app.go`, replace the existing wrap-around block:

```go
		if m.phase == phaseModel && m.models.FilterState() != list.Filtering {
			switch msg.String() {
			case "up", "k":
				if m.models.Index() == 0 {
					m.models.Select(len(m.models.Items()) - 1)
					return m, nil
				}
			case "down", "j":
				if m.models.Index() == len(m.models.Items())-1 {
					m.models.Select(0)
					return m, nil
				}
			}
		}
```

with:

```go
		if m.phase == phaseModel && m.models.FilterState() != list.Filtering {
			items := m.models.Items()
			switch msg.String() {
			case "up", "k":
				if fi := firstModelIndex(items); fi >= 0 && m.models.Index() == fi {
					if li := lastModelIndex(items); li >= 0 {
						m.models.Select(li)
						return m, nil
					}
				}
			case "down", "j":
				if li := lastModelIndex(items); li >= 0 && m.models.Index() == li {
					if fi := firstModelIndex(items); fi >= 0 {
						m.models.Select(fi)
						return m, nil
					}
				}
			}
		}
```

- [ ] **Step 6: Snap the selection after every model-phase list update**

In `wt/internal/tui/app.go`, replace:

```go
	if m.phase == phaseModel && m.width > 0 && m.height > 0 {
		var cmd tea.Cmd
		m.models, cmd = m.models.Update(msg)
		return m, cmd
	}
```

with:

```go
	if m.phase == phaseModel && m.width > 0 && m.height > 0 {
		var cmd tea.Cmd
		m.models, cmd = m.models.Update(msg)
		m.snapSelectionOnModel()
		return m, cmd
	}
```

- [ ] **Step 7: Run the focused tests to verify they pass**

Run: `go test ./internal/tui -run 'TestEnterModelPhaseCursorNeverOnDivider|TestSnapSelectionOnModel' -v`
Expected: PASS.

- [ ] **Step 8: Run the full TUI + usage test suites**

Run: `go test ./internal/usage ./internal/tui`
Expected: PASS. If an existing test that builds a model list and asserts an exact cursor index fails, update its expected index by +1 (the leading divider shifts rows) and re-run.

- [ ] **Step 9: Commit**

```bash
git add internal/tui/app.go internal/tui/model_list.go internal/tui/model_family_test.go
git commit -m "feat(tui): family-usage model picker ordering with divider-safe cursor"
```

---

### Task 4: Full verification and docs

**Files:**
- Modify: `wt/CLAUDE.md`

- [ ] **Step 1: Run the whole wt suite**

Run: `go test ./... && go vet ./... && go build ./...` (from `wt/`)
Expected: all PASS, vet clean, build succeeds.

- [ ] **Step 2: Run the monorepo lint**

From the repo root run: `make lint`
Expected: PASS (shell + link checks).

- [ ] **Step 3: Update the usage docs**

In `wt/CLAUDE.md`, under the **Rotation (Go)** section, extend the line about usage history to mention family counts. Change:

```markdown
- Usage history (1d/7d/30d per-model counts) lives at `~/.config/agent-wt/usage.jsonl` (JSONL, appended by `usage.Store.Record`, consumed by the model's picker footer — see `internal/usage`).
```

to:

```markdown
- Usage history (1d/7d/30d per-model counts) lives at `~/.config/agent-wt/usage.jsonl` (JSONL, appended by `usage.Store.Record`, consumed by the model's picker footer — see `internal/usage`). `Store.FamilyCounts` aggregates the same events by model family (via a model-id→family map from the registry), and the model picker sorts eligible models descending by family-then-model `CompositeScore` (a recency-weighted integer key) with non-selectable family header rows between groups.
```

- [ ] **Step 4: Commit**

```bash
git add wt/CLAUDE.md
git commit -m "docs(wt): document family-usage sort in the model picker"
```

- [ ] **Step 5: Report verification evidence**

Run `go test ./...` once more and paste the result line into your report (e.g. `ok github.com/ohanaverse/local-ai-setup/wt/internal/tui 2.412s`). Do not claim completion without this.
