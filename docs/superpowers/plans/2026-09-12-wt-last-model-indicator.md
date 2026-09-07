# wt Last-Model Indicator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Mark the last genuinely-launched model's row in the `wt` model picker with a `▶` prefix, read from the existing `rotation.state` file — no new state.

**Architecture:** Pure rendering change in `buildModelItems` (a fixed 2-rune prefix baked into each pre-formatted row line), plus a read-only wiring change in `enterModelPhase` that fetches `rotation.New().Last()` alongside the `Next()` call it already makes for cursor positioning. Spec: `docs/superpowers/specs/2026-09-12-wt-last-model-indicator-design.md`.

**Tech Stack:** Go 1.26 (module root `wt/`), Bubble Tea/lipgloss TUI, existing test seams (`tempStateDir`, `seedState`, `stubUsageStore`, `drivePhaseAgentEnter`, `testConfig`).

**All commands run from `wt/`** (the Go module root), not the monorepo root.

---

### Task 1: Prefix marker in `buildModelItems`

**Files:**
- Modify: `wt/internal/tui/model_list.go:150-230` (signature + line format)
- Modify: all `buildModelItems` call sites (exact list in Step 3)
- Test: `wt/internal/tui/model_list_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `wt/internal/tui/model_list_test.go` (also add `"unicode/utf8"` to the file's import block):

```go
// TestBuildModelItemsMarksLastLaunchedRow verifies that exactly one row —
// the model matching the rotation's last-launched ID — carries the ▶ prefix
// and every other row carries a blank 2-rune prefix, so the marker pins the
// "where I left off" row without shifting any columns (all lines stay
// rune-equal in length).
func TestBuildModelItemsMarksLastLaunchedRow(t *testing.T) {
	store := &mockStore{counts: map[string]usage.UsageCounts{}}
	models := []config.Model{
		{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Location: config.LocationLocal},
		{ID: "ollama/gemma4:14b", ProviderID: "ollama", Family: "gemma4", Location: config.LocationLocal},
	}
	familyOf := map[string]string{
		"ollama/gemma4:9b":  "gemma4",
		"ollama/gemma4:14b": "gemma4",
	}
	items := buildModelItems(models, familyOf, store, "ollama/gemma4:14b")
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	// Equal scores + stable sort = registry order: 9b first, 14b second.
	for i, want := range []bool{false, true} {
		line := items[i].Title()
		if got := strings.HasPrefix(line, "▶"); got != want {
			t.Errorf("row %d (%q): ▶ prefix = %v, want %v", i, line, got, want)
		}
	}
	wantLen := utf8.RuneCountInString(items[0].Title())
	if gotLen := utf8.RuneCountInString(items[1].Title()); gotLen != wantLen {
		t.Errorf("marked row length %d != unmarked row length %d (columns would misalign)", gotLen, wantLen)
	}
}

// TestBuildModelItemsNoMarkerWithoutLastLaunched verifies that an empty
// last-launched ID (no rotation.state) or an ID outside the eligible slice
// (different agent, -T/-F filter, deleted model) leaves every row unmarked —
// the picker must not fabricate a "last used" signal.
func TestBuildModelItemsNoMarkerWithoutLastLaunched(t *testing.T) {
	store := &mockStore{counts: map[string]usage.UsageCounts{}}
	models := []config.Model{
		{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Location: config.LocationLocal},
	}
	familyOf := map[string]string{"ollama/gemma4:9b": "gemma4"}
	for _, lastID := range []string{"", "ollama/gone"} {
		items := buildModelItems(models, familyOf, store, lastID)
		for i, it := range items {
			if strings.HasPrefix(it.Title(), "▶") {
				t.Errorf("lastID %q: row %d unexpectedly marked: %q", lastID, i, it.Title())
			}
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/tui -run 'TestBuildModelItemsMarksLastLaunchedRow|TestBuildModelItemsNoMarkerWithoutLastLaunched' -v
```

Expected: FAIL with a compile error — `too many arguments in call to buildModelItems` (the new `lastID` parameter does not exist yet).

- [ ] **Step 3: Implement the signature change and marker, update every call site**

In `wt/internal/tui/model_list.go`, change the signature (line ~155):

```go
func buildModelItems(models []config.Model, familyOf map[string]string, s usage.Store, lastID string) []*modelItem {
```

Then inside the item loop, replace the existing `line :=` construction (line ~208):

```go
		// 2-rune prefix on every row: "▶ " marks the rotation's
		// last-launched model, two spaces keep unmarked rows aligned.
		marker := "  "
		if lastID != "" && m.ID == lastID {
			marker = "▶ "
		}
		line := fmt.Sprintf("%s%-*s  %3d  %-*s  %-5s  %-*s  %-*s  %-*s",
			marker, famWidth, famDisp, fam30d, idWidth, m.ID, string(m.Location), 11, countsStr,
			ptWidth, pricing[i].perToken, subWidth, pricing[i].subscription)
```

Every other call site gains a trailing `""` argument (passing empty = no marker; Task 2 wires the real value). Update all of these:

- `internal/tui/app.go:696` — `items := buildModelItems(models, familyOf, newUsageStore(), "")`
- `internal/tui/testhelpers_test.go:22` — `items := buildModelItems(models, familyOf, newUsageStore(), "")`
- `internal/tui/model_line_test.go:86` — `items := buildModelItems(tt.models, familyOf, store, "")`
- `internal/tui/agent_model_test.go:84` — `items := buildModelItems(models, familyOf, newUsageStore(), "")`
- `internal/tui/model_family_test.go:158` — `items := buildModelItems(modelFamilies(), familyOfFor(), newUsageStore(), "")`
- `internal/tui/model_family_test.go:193` — `items := buildModelItems(models, familyOfFor(), store, "")`
- `internal/tui/model_family_test.go:227` — `items := buildModelItems(models, familyOfFor(), store, "")`
- `internal/tui/model_family_test.go:252` — `items := buildModelItems(modelFamilies(), familyOfFor(), store, "")`
- `internal/tui/model_list_test.go:44` — add `, ""`
- `internal/tui/model_list_test.go:105` — add `, ""`
- `internal/tui/model_list_test.go:165` — `items := buildModelItems(models, map[string]string{"partial": "test"}, store, "")`

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/tui -v
```

Expected: PASS — including the two new tests and every pre-existing picker test (lines now carry a 2-rune prefix; existing assertions use `strings.Contains`, which is prefix-insensitive).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model_list.go internal/tui/app.go internal/tui/testhelpers_test.go internal/tui/model_line_test.go internal/tui/agent_model_test.go internal/tui/model_family_test.go internal/tui/model_list_test.go
git commit -m "feat(wt): mark last-launched row in model picker lines

buildModelItems gains a lastID param and bakes a fixed 2-rune prefix
(▶ / two spaces) into every row line so columns stay aligned. Wiring
the real rotation value comes next."
```

---

### Task 2: Wire `rotation.Last()` into the picker

**Files:**
- Modify: `wt/internal/tui/app.go:693-696` (`enterModelPhase`)
- Modify: `wt/internal/tui/agent_model_test.go:84` (`phaseModelWithList` — mirrors production)
- Test: `wt/internal/tui/agent_model_test.go`

- [ ] **Step 1: Write the failing test**

Add to `wt/internal/tui/agent_model_test.go` (after `TestPhaseModelWithListBuildsAndPositionsCursor`):

```go
// TestModelPickerMarksLastLaunchedRow drives the production
// phaseAgent → enterModelPhase path with a seeded rotation.state and
// asserts the ▶ marker lands on the last-launched row while the cursor
// lands on the rotation's next-to-use row — the two are different rows,
// and confusing them would mean the picker highlighted what to reuse
// instead of what was just used.
func TestModelPickerMarksLastLaunchedRow(t *testing.T) {
	dir := tempStateDir(t)
	stubUsageStore(t) // hermetic usage counts: stable sort = registry order
	seedState(t, dir, "ollama/gemma4:9b")
	cfg := testConfig()
	m := model{cfg: cfg, phase: phaseList, width: 80, height: 24}
	gotModel := drivePhaseAgentEnter(t, m, "claude")

	markerIdx := -1
	for i, it := range gotModel.models.Items() {
		if strings.HasPrefix(it.(*modelItem).Title(), "▶") {
			if markerIdx != -1 {
				t.Fatalf("▶ marker on more than one row")
			}
			markerIdx = i
		}
	}
	if markerIdx != 0 {
		t.Errorf("marker row = %d, want 0 (ollama/gemma4:9b)", markerIdx)
	}
	if gotModel.models.Index() != 1 {
		t.Errorf("cursor index = %d, want 1 (rotation-next row ollama/gemma4:14b)", gotModel.models.Index())
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/tui -run TestModelPickerMarksLastLaunchedRow -v
```

Expected: FAIL — `marker row = -1, want 0` (Task 1 wired `""` through `enterModelPhase`, so nothing is marked).

- [ ] **Step 3: Read the real rotation value in production and its test mirror**

In `wt/internal/tui/app.go` inside `enterModelPhase`, replace:

```go
	// Build the sorted, compact model list.
	items := buildModelItems(models, familyOf, newUsageStore())
```

with:

```go
	// Last-launched model ID for the ▶ row marker. Same construction site
	// as the Next() cursor call below; a missing/unreadable rotation.state
	// yields "" and leaves every row unmarked.
	lastID, _ := rotation.New().Last()
	// Build the sorted, compact model list.
	items := buildModelItems(models, familyOf, newUsageStore(), lastID)
```

In `wt/internal/tui/agent_model_test.go` inside `phaseModelWithList` (line ~84), replace:

```go
	items := buildModelItems(models, familyOf, newUsageStore())
```

with:

```go
	// Mirror production enterModelPhase: read the last-launched ID from
	// rotation state so tests exercise the same marker wiring.
	lastID, _ := rotation.New().Last()
	items := buildModelItems(models, familyOf, newUsageStore(), lastID)
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/tui -v
```

Expected: PASS — the new test plus all existing tests (existing `tempStateDir` tests are rotation-isolated, so host state cannot leak in).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/app.go internal/tui/agent_model_test.go
git commit -m "feat(wt): read rotation.last for the picker's last-used marker

enterModelPhase fetches rotation.Last() alongside its Next() cursor
call; phaseModelWithList mirrors the production read so tests exercise
the real wiring."
```

---

### Task 3: Docs and full verification

**Files:**
- Modify: `wt/CLAUDE.md` (Rotation section)

- [ ] **Step 1: Document the marker in `wt/CLAUDE.md`**

In the `## Rotation (Go)` section, after the bullet describing the state file and usage history (the paragraph starting "State file: `~/.config/agent-wt/rotation.state`..."), add:

```markdown
- The model picker marks the last-launched row with a `▶` prefix
  (`buildModelItems` in `internal/tui/model_list.go`, value from
  `rotation.Last()`); the cursor still lands on the rotation's
  next-to-use model.
```

- [ ] **Step 2: Run the full Go gate**

```bash
go build ./...
go vet ./...
go test ./...
```

Expected: all three exit 0, no failures.

- [ ] **Step 3: Commit**

```bash
git add wt/CLAUDE.md
git commit -m "docs(wt): document the picker's last-launched row marker"
```

- [ ] **Step 4: Manual smoke (requires TTY + installed binary)**

```bash
cd wt && make install
wt   # pick a model, launch, quit, relaunch
```

Expected: the previously used model's row shows the `▶` prefix; the cursor rests on the next rotation candidate; no prior launch (fresh `XDG_CONFIG_HOME`) shows no marker.

---

## Self-Review Notes

- Spec coverage: behavior (Task 1+2), rendering/alignment (Task 1 test asserts rune-length equality), edge cases — no rotation state, ineligible last model (Task 1 `TestBuildModelItemsNoMarkerWithoutLastLaunched`), corrupt state file reads as absent via `rotation.Last()`'s read-error path. Docs: Task 3. No contract fixtures or new state — none needed.
- Type consistency: `buildModelItems(models []config.Model, familyOf map[string]string, s usage.Store, lastID string)` used identically in every task; test helpers (`tempStateDir`, `seedState`, `stubUsageStore`, `drivePhaseAgentEnter`, `testConfig`, `mockStore`) all exist today.
- Known ordering fact used by tests: with all-zero usage counts, `sortModelsByUsage` is stable, so items keep registry order (`gemma4:9b` before `gemma4:14b`).