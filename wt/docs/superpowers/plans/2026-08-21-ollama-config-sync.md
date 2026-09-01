# `wt config ollama` — Ollama Model Sync TUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `wt config ollama` subcommand that launches an interactive TUI for syncing config.toml ollama models with `ollama list`.

**Architecture:** A new self-contained Bubble Tea program in `internal/ollamaconfig/`, separate from the launch TUI in `internal/tui/`. Exports `Run(theme)` called from `cmd/wt/commands_config.go`. Two screens: a union list with status indicators, and a field-editing form. Resolve actions (pull/delete) for missing models use a choice list.

**Tech Stack:** Go, Bubble Tea (bubbletea v1.3.10), bubbles (list, textinput), lipgloss, cobra, BurntSushi/toml

---

## File Structure

| File | Responsibility |
|------|----------------|
| `internal/tui/delegate.go` | **Modify:** export `themedListDelegate` → `ThemedListDelegate` |
| `internal/ollamaconfig/sync.go` | Union computation: takes config models + ollama models, returns `[]syncEntry` sorted by family then model_name |
| `internal/ollamaconfig/sync_test.go` | Tests for union computation, sorting, non-ollama exclusion, edge cases |
| `internal/ollamaconfig/edit.go` | Edit screen types, tags parsing, location toggle, save logic (update existing / add new) |
| `internal/ollamaconfig/edit_test.go` | Tests for tags parsing, location toggle, save logic |
| `internal/ollamaconfig/ollamaconfig.go` | The TUI: `model` struct, phases, `Init`, `Update`, `View`, `Run` entry point, load command, pull command |
| `internal/ollamaconfig/ollamaconfig_test.go` | Tests for phase transitions, key handling |
| `cmd/wt/commands_config.go` | **Modify:** add `configOllamaCmd(a)`, register under `configCmd`, update help text |

---

### Task 1: Export ThemedListDelegate from internal/tui

**Files:**
- Modify: `internal/tui/delegate.go`
- Modify: `internal/tui/app.go` (call sites)
- Modify: `internal/tui/launch.go` (call sites)
- Modify: `internal/tui/agent_picker.go` (call sites — none, but check)
- Modify: `internal/tui/model_list.go` (call sites)
- Modify: `internal/tui/new_worktree.go` (call sites — none, but check)

- [ ] **Step 1: Rename the function and update all call sites**

In `internal/tui/delegate.go`, rename `themedListDelegate` to `ThemedListDelegate`:

```go
// ThemedListDelegate returns a list.DefaultDelegate whose Normal/Selected
// styles are themed. This is the single styling point for every picker
// list in the TUI (worktree list, agent list, model list, resume/guard/
// ollama choice lists). Exported so internal/ollamaconfig can reuse it.
func ThemedListDelegate(theme themes.Theme) list.DefaultDelegate {
```

Update all call sites. Search for `themedListDelegate` across the `tui` package:

```bash
grep -rn 'themedListDelegate' internal/tui/
```

Replace every `themedListDelegate(` with `ThemedListDelegate(` in:
- `internal/tui/app.go` (multiple: `buildOllamaChoices`, `buildResumeChoices`, `buildGuardChoices`, `proceedFromSelectedPath`, `enterModelPhase`)
- `internal/tui/model_list.go` (`buildModelList`)

- [ ] **Step 2: Run tests to verify nothing broke**

Run: `go test ./internal/tui/... -count=1`
Expected: PASS (all existing tests still pass — pure rename)

- [ ] **Step 3: Commit**

```bash
git add internal/tui/delegate.go internal/tui/app.go internal/tui/model_list.go
git commit -m "refactor(tui): export ThemedListDelegate for reuse by ollamaconfig"
```

---

### Task 2: sync.go — Union Computation

**Files:**
- Create: `internal/ollamaconfig/sync.go`
- Create: `internal/ollamaconfig/sync_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/ollamaconfig/sync_test.go`:

```go
package ollamaconfig

import (
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// TestComputeUnionAllSynced verifies that models present in both config
// and ollama list are marked as synced, using the config model's values.
func TestComputeUnionAllSynced(t *testing.T) {
	curated := []config.Model{
		{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal, Tags: []string{"code"}},
	}
	discovered := []config.Model{
		{ID: "ollama/gemma4:9b", Family: "gemma4:9b", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal},
	}
	entries := computeUnion(curated, discovered)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if !e.config || !e.ollama {
		t.Errorf("entry should be synced (config=%v, ollama=%v)", e.config, e.ollama)
	}
	// Synced entries use the config model (which has tags).
	if len(e.model.Tags) != 1 || e.model.Tags[0] != "code" {
		t.Errorf("expected tags from config model, got %v", e.model.Tags)
	}
}

// TestComputeUnionMissingModel verifies that a model in config but not in
// ollama list is marked as missing.
func TestComputeUnionMissingModel(t *testing.T) {
	curated := []config.Model{
		{ID: "ollama/kimi:cloud", Family: "kimi", ProviderID: "ollama", ModelName: "kimi:cloud", Location: config.LocationCloud},
	}
	discovered := []config.Model{}
	entries := computeUnion(curated, discovered)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].config && !entries[0].ollama {
		return // correct: missing
	}
	t.Errorf("expected missing (config=true, ollama=false), got (config=%v, ollama=%v)", entries[0].config, entries[0].ollama)
}

// TestComputeUnionUntrackedModel verifies that a model in ollama list but
// not in config is marked as untracked, using the discovered model.
func TestComputeUnionUntrackedModel(t *testing.T) {
	curated := []config.Model{}
	discovered := []config.Model{
		{ID: "ollama/llama3.2:3b", Family: "llama3.2:3b", ProviderID: "ollama", ModelName: "llama3.2:3b", Location: config.LocationLocal},
	}
	entries := computeUnion(curated, discovered)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.config || !e.ollama {
		t.Errorf("expected untracked (config=false, ollama=true), got (config=%v, ollama=%v)", e.config, e.ollama)
	}
	if e.model.ModelName != "llama3.2:3b" {
		t.Errorf("expected model_name llama3.2:3b, got %q", e.model.ModelName)
	}
}

// TestComputeUnionAllThreeStates verifies that synced, missing, and
// untracked entries all appear in the same union.
func TestComputeUnionAllThreeStates(t *testing.T) {
	curated := []config.Model{
		{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal},
		{ID: "ollama/kimi:cloud", Family: "kimi", ProviderID: "ollama", ModelName: "kimi:cloud", Location: config.LocationCloud},
	}
	discovered := []config.Model{
		{ID: "ollama/gemma4:9b", Family: "gemma4:9b", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal},
		{ID: "ollama/llama3.2:3b", Family: "llama3.2:3b", ProviderID: "ollama", ModelName: "llama3.2:3b", Location: config.LocationLocal},
	}
	entries := computeUnion(curated, discovered)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	// gemma4:9b → synced, kimi:cloud → missing, llama3.2:3b → untracked
	byName := map[string]syncEntry{}
	for _, e := range entries {
		byName[e.model.ModelName] = e
	}
	if e := byName["gemma4:9b"]; !e.config || !e.ollama {
		t.Errorf("gemma4:9b should be synced, got config=%v ollama=%v", e.config, e.ollama)
	}
	if e := byName["kimi:cloud"]; !e.config || e.ollama {
		t.Errorf("kimi:cloud should be missing, got config=%v ollama=%v", e.config, e.ollama)
	}
	if e := byName["llama3.2:3b"]; e.config || !e.ollama {
		t.Errorf("llama3.2:3b should be untracked, got config=%v ollama=%v", e.config, e.ollama)
	}
}

// TestComputeUnionExcludesNonOllama verifies that non-ollama config models
// (e.g. openrouter) are excluded from the union.
func TestComputeUnionExcludesNonOllama(t *testing.T) {
	curated := []config.Model{
		{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal},
		{ID: "openrouter/google/gemma-4-9b", Family: "gemma4", ProviderID: "openrouter", ModelName: "google/gemma-4-9b"},
	}
	discovered := []config.Model{
		{ID: "ollama/gemma4:9b", Family: "gemma4:9b", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal},
	}
	entries := computeUnion(curated, discovered)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (openrouter excluded), got %d", len(entries))
	}
	if entries[0].model.ModelName != "gemma4:9b" {
		t.Errorf("expected gemma4:9b, got %q", entries[0].model.ModelName)
	}
}

// TestComputeUnionSorting verifies entries are sorted by family, then
// model_name.
func TestComputeUnionSorting(t *testing.T) {
	curated := []config.Model{
		{ID: "ollama/zzz:1b", Family: "zzz", ProviderID: "ollama", ModelName: "zzz:1b", Location: config.LocationLocal},
		{ID: "ollama/aaa:1b", Family: "aaa", ProviderID: "ollama", ModelName: "aaa:1b", Location: config.LocationLocal},
		{ID: "ollama/aaa:3b", Family: "aaa", ProviderID: "ollama", ModelName: "aaa:3b", Location: config.LocationLocal},
	}
	discovered := []config.Model{}
	entries := computeUnion(curated, discovered)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	want := []string{"aaa:1b", "aaa:3b", "zzz:1b"}
	for i, w := range want {
		if entries[i].model.ModelName != w {
			t.Errorf("entry[%d] = %q, want %q", i, entries[i].model.ModelName, w)
		}
	}
}

// TestComputeUnionEmptyBoth verifies that empty inputs produce an empty
// result (no panic).
func TestComputeUnionEmptyBoth(t *testing.T) {
	entries := computeUnion(nil, nil)
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

// TestSyncEntryStatus verifies the Status() method returns the correct
// string for each state.
func TestSyncEntryStatus(t *testing.T) {
	cases := []struct {
		entry  syncEntry
		status string
	}{
		{syncEntry{config: true, ollama: true}, "synced"},
		{syncEntry{config: true, ollama: false}, "missing"},
		{syncEntry{config: false, ollama: true}, "untracked"},
	}
	for _, c := range cases {
		if got := c.entry.Status(); got != c.status {
			t.Errorf("Status() = %q, want %q", got, c.status)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ollamaconfig/... -run TestComputeUnion -v`
Expected: FAIL — package doesn't exist / `computeUnion` undefined

- [ ] **Step 3: Write the implementation**

Create `internal/ollamaconfig/sync.go`:

```go
// Package ollamaconfig implements the `wt config ollama` TUI for syncing
// config.toml ollama models with `ollama list`.
package ollamaconfig

import (
	"sort"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// syncEntry represents one row in the union list. It tracks whether the
// model appears in config.toml, in ollama list, or both.
type syncEntry struct {
	model  config.Model // config.toml model (zero value if not in config)
	ollama bool         // true if model appears in ollama list
	config bool         // true if model appears in config.toml
}

// Status returns the human-readable status string for this entry.
func (e syncEntry) Status() string {
	switch {
	case e.config && e.ollama:
		return "synced"
	case e.config && !e.ollama:
		return "missing"
	default:
		return "untracked"
	}
}

// computeUnion builds the union of config.toml ollama models and
// ollama-discovered models, keyed by ModelName. Non-ollama config models
// are excluded. The result is sorted by family, then model_name.
func computeUnion(curated, discovered []config.Model) []syncEntry {
	// Build a map of config ollama models keyed by ModelName.
	byName := map[string]syncEntry{}
	for _, m := range curated {
		if m.ProviderID != "ollama" {
			continue
		}
		byName[m.ModelName] = syncEntry{
			model:  m,
			config: true,
		}
	}
	// Merge in discovered models.
	for _, m := range discovered {
		entry, exists := byName[m.ModelName]
		if !exists {
			byName[m.ModelName] = syncEntry{
				model:  m,
				ollama: true,
			}
			continue
		}
		entry.ollama = true
		byName[m.ModelName] = entry
	}
	// Collect and sort.
	entries := make([]syncEntry, 0, len(byName))
	for _, e := range byName {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].model.Family != entries[j].model.Family {
			return entries[i].model.Family < entries[j].model.Family
		}
		return entries[i].model.ModelName < entries[j].model.ModelName
	})
	return entries
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/ollamaconfig/... -v`
Expected: PASS (all 8 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/ollamaconfig/sync.go internal/ollamaconfig/sync_test.go
git commit -m "feat(ollamaconfig): union computation for config/ollama model sync"
```

---

### Task 3: edit.go — Tags Parsing and Location Toggle

**Files:**
- Create: `internal/ollamaconfig/edit.go`
- Create: `internal/ollamaconfig/edit_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/ollamaconfig/edit_test.go`:

```go
package ollamaconfig

import (
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// TestParseTags verifies that comma-delimited tag strings are parsed
// into slices with trimming and empty-drop.
func TestParseTags(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"code, design", []string{"code", "design"}},
		{"  code ,  design  ", []string{"code", "design"}},
		{"code", []string{"code"}},
		{"", nil},
		{" , , ", nil},
	}
	for _, c := range cases {
		got := parseTags(c.input)
		if len(got) != len(c.want) {
			t.Errorf("parseTags(%q) = %v, want %v", c.input, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parseTags(%q)[%d] = %q, want %q", c.input, i, got[i], c.want[i])
			}
		}
	}
}

// TestTagsToString verifies that a tag slice is joined into a
// comma-delimited string for display in the edit input.
func TestTagsToString(t *testing.T) {
	cases := []struct {
		tags []string
		want string
	}{
		{[]string{"code", "design"}, "code, design"},
		{[]string{"code"}, "code"},
		{nil, ""},
		{[]string{}, ""},
	}
	for _, c := range cases {
		got := tagsToString(c.tags)
		if got != c.want {
			t.Errorf("tagsToString(%v) = %q, want %q", c.tags, got, c.want)
		}
	}
}

// TestToggleLocation verifies that the location toggle cycles between
// local and cloud.
func TestToggleLocation(t *testing.T) {
	if got := toggleLocation(config.LocationLocal); got != config.LocationCloud {
		t.Errorf("toggleLocation(local) = %s, want cloud", got)
	}
	if got := toggleLocation(config.LocationCloud); got != config.LocationLocal {
		t.Errorf("toggleLocation(cloud) = %s, want local", got)
	}
	// Empty string defaults to local, then toggles to cloud.
	if got := toggleLocation(""); got != config.LocationLocal {
		t.Errorf("toggleLocation(\"\") = %s, want local", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ollamaconfig/... -run TestParseTags -v`
Expected: FAIL — `parseTags`, `tagsToString`, `toggleLocation` undefined

- [ ] **Step 3: Write the implementation**

Create `internal/ollamaconfig/edit.go`:

```go
package ollamaconfig

import (
	"strings"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// parseTags splits a comma-delimited tag string, trimming whitespace and
// dropping empty entries. Returns nil for empty/whitespace-only input.
// This mirrors config.ParseFilterList but is local to keep the edit
// screen self-contained.
func parseTags(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// tagsToString joins a tag slice into a comma-delimited display string.
// Returns "" for nil or empty slices.
func tagsToString(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	return strings.Join(tags, ", ")
}

// toggleLocation cycles between local and cloud. An empty location
// defaults to local (the first press on a fresh entry sets it to local).
func toggleLocation(loc config.Location) config.Location {
	switch loc {
	case config.LocationLocal:
		return config.LocationCloud
	case config.LocationCloud:
		return config.LocationLocal
	default:
		return config.LocationLocal
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/ollamaconfig/... -run "TestParseTags|TestTagsToString|TestToggleLocation" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/ollamaconfig/edit.go internal/ollamaconfig/edit_test.go
git commit -m "feat(ollamaconfig): tags parsing and location toggle helpers"
```

---

### Task 4: edit.go — Save Logic

**Files:**
- Modify: `internal/ollamaconfig/edit.go`
- Modify: `internal/ollamaconfig/edit_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/ollamaconfig/edit_test.go`:

```go
// TestSaveExistingModel verifies that saving an edit to a synced model
// updates the matching entry in cfg.Models and leaves others unchanged.
func TestSaveExistingModel(t *testing.T) {
	cfg := &config.Config{
		Models: []config.Model{
			{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal, Tags: []string{"code"}},
			{ID: "ollama/gemma4:27b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:27b", Location: config.LocationLocal, Tags: []string{"code"}},
		},
	}
	updated := config.Model{
		ID:         "ollama/gemma4:9b",
		Family:     "gemma4-renamed",
		ProviderID: "ollama",
		ModelName:  "gemma4:9b",
		Location:   config.LocationCloud,
		Tags:       []string{"code", "design"},
	}
	saveModelToConfig(cfg, updated, false)
	if len(cfg.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(cfg.Models))
	}
	m := cfg.Models[0]
	if m.Family != "gemma4-renamed" {
		t.Errorf("family = %q, want gemma4-renamed", m.Family)
	}
	if m.Location != config.LocationCloud {
		t.Errorf("location = %s, want cloud", m.Location)
	}
	if len(m.Tags) != 2 || m.Tags[0] != "code" || m.Tags[1] != "design" {
		t.Errorf("tags = %v, want [code design]", m.Tags)
	}
	// Second model unchanged.
	if cfg.Models[1].Family != "gemma4" {
		t.Errorf("second model family changed: %q", cfg.Models[1].Family)
	}
}

// TestSaveNewModel verifies that saving an untracked model appends a new
// entry with the correct auto-generated fields.
func TestSaveNewModel(t *testing.T) {
	cfg := &config.Config{
		Models: []config.Model{
			{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal, Tags: []string{"code"}},
		},
	}
	newModel := config.Model{
		ID:         "ollama/llama3.2:3b",
		Family:     "llama3.2",
		ProviderID: "ollama",
		ModelName:  "llama3.2:3b",
		Location:   config.LocationLocal,
		Tags:       []string{"code"},
	}
	saveModelToConfig(cfg, newModel, true)
	if len(cfg.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(cfg.Models))
	}
	m := cfg.Models[1]
	if m.ID != "ollama/llama3.2:3b" {
		t.Errorf("id = %q, want ollama/llama3.2:3b", m.ID)
	}
	if m.Source != config.SourceCurated {
		t.Errorf("source = %s, want curated", m.Source)
	}
}

// TestDeleteModelFromConfig verifies that deleting a model by ID removes
// it from cfg.Models without affecting other entries.
func TestDeleteModelFromConfig(t *testing.T) {
	cfg := &config.Config{
		Models: []config.Model{
			{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal},
			{ID: "ollama/kimi:cloud", Family: "kimi", ProviderID: "ollama", ModelName: "kimi:cloud", Location: config.LocationCloud},
		},
	}
	deleteModelFromConfig(cfg, "ollama/kimi:cloud")
	if len(cfg.Models) != 1 {
		t.Fatalf("expected 1 model after delete, got %d", len(cfg.Models))
	}
	if cfg.Models[0].ID != "ollama/gemma4:9b" {
		t.Errorf("remaining model = %q, want ollama/gemma4:9b", cfg.Models[0].ID)
	}
}

// TestDeleteModelFromConfigNotFound verifies that deleting a non-existent
// ID is a no-op (no error, no change).
func TestDeleteModelFromConfigNotFound(t *testing.T) {
	cfg := &config.Config{
		Models: []config.Model{
			{ID: "ollama/gemma4:9b", ProviderID: "ollama", ModelName: "gemma4:9b"},
		},
	}
	deleteModelFromConfig(cfg, "ollama/nonexistent")
	if len(cfg.Models) != 1 {
		t.Fatalf("expected 1 model (no-op), got %d", len(cfg.Models))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ollamaconfig/... -run "TestSave|TestDelete" -v`
Expected: FAIL — `saveModelToConfig`, `deleteModelFromConfig` undefined

- [ ] **Step 3: Write the implementation**

Append to `internal/ollamaconfig/edit.go`:

```go
// saveModelToConfig writes m into cfg.Models. If isNew is true, m is
// appended. If isNew is false, the existing entry with matching ID is
// updated in place. Source is set to curated for new models.
func saveModelToConfig(cfg *config.Config, m config.Model, isNew bool) {
	if isNew {
		m.Source = config.SourceCurated
		cfg.Models = append(cfg.Models, m)
		return
	}
	for i := range cfg.Models {
		if cfg.Models[i].ID == m.ID {
			cfg.Models[i] = m
			return
		}
	}
}

// deleteModelFromConfig removes the model with the given ID from
// cfg.Models. No-op if the ID is not found.
func deleteModelFromConfig(cfg *config.Config, id string) {
	for i, m := range cfg.Models {
		if m.ID == id {
			cfg.Models = append(cfg.Models[:i], cfg.Models[i+1:]...)
			return
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/ollamaconfig/... -v`
Expected: PASS (all tests)

- [ ] **Step 5: Commit**

```bash
git add internal/ollamaconfig/edit.go internal/ollamaconfig/edit_test.go
git commit -m "feat(ollamaconfig): save and delete model config operations"
```

---

### Task 5: ollamaconfig.go — TUI Model, Phases, and List Screen

**Files:**
- Create: `internal/ollamaconfig/ollamaconfig.go`
- Create: `internal/ollamaconfig/ollamaconfig_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/ollamaconfig/ollamaconfig_test.go`:

```go
package ollamaconfig

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/list"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/themes"
)

// testTheme returns the default theme for tests.
func testTheme() themes.Theme {
	t, _ := themes.Get("default")
	return t
}

// TestInitReturnsLoadCmd verifies that Init starts the data loading
// command.
func TestInitReturnsLoadCmd(t *testing.T) {
	m := newModel(testTheme())
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init returned nil cmd; expected load command")
	}
}

// TestUpdateWindowSizeMsg verifies that the model records terminal
// dimensions.
func TestUpdateWindowSizeMsg(t *testing.T) {
	m := newModel(testTheme())
	got, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if m2.width != 80 || m2.height != 24 {
		t.Errorf("dimensions = (%d, %d), want (80, 24)", m2.width, m2.height)
	}
}

// TestLoadedMsgBuildsList verifies that a loadedMsg with entries
// transitions the model to the list phase with a populated list.
func TestLoadedMsgBuildsList(t *testing.T) {
	m := newModel(testTheme())
	m.width, m.height = 80, 24
	entries := []syncEntry{
		{model: config.Model{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal, Tags: []string{"code"}}, config: true, ollama: true},
		{model: config.Model{ID: "ollama/kimi:cloud", Family: "kimi", ProviderID: "ollama", ModelName: "kimi:cloud", Location: config.LocationCloud}, config: true, ollama: false},
	}
	got, _ := m.Update(loadedMsg{entries: entries, ollamaFound: true})
	m2, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if m2.phase != phaseList {
		t.Fatalf("phase = %v, want phaseList", m2.phase)
	}
	if m2.list.Items() == nil || len(m2.list.Items()) != 2 {
		t.Fatalf("expected 2 list items, got %v", m2.list.Items())
	}
}

// TestLoadedMsgOllamaNotFound verifies that when ollama is not found,
// a status message is shown.
func TestLoadedMsgOllamaNotFound(t *testing.T) {
	m := newModel(testTheme())
	m.width, m.height = 80, 24
	entries := []syncEntry{
		{model: config.Model{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal}, config: true, ollama: false},
	}
	got, _ := m.Update(loadedMsg{entries: entries, ollamaFound: false})
	m2, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if m2.status == "" {
		t.Error("expected status message when ollama is not found")
	}
}

// TestEnterOnSyncedGoesToEdit verifies that pressing Enter on a synced
// entry transitions to the edit phase.
func TestEnterOnSyncedGoesToEdit(t *testing.T) {
	m := newModel(testTheme())
	m.width, m.height = 80, 24
	entries := []syncEntry{
		{model: config.Model{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal, Tags: []string{"code"}}, config: true, ollama: true},
	}
	m.list = buildSyncList(entries, testTheme(), 78, 22)
	m.phase = phaseList
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if m2.phase != phaseEdit {
		t.Fatalf("phase = %v, want phaseEdit", m2.phase)
	}
}

// TestEnterOnMissingGoesToResolve verifies that pressing Enter on a
// missing entry transitions to the resolve phase.
func TestEnterOnMissingGoesToResolve(t *testing.T) {
	m := newModel(testTheme())
	m.width, m.height = 80, 24
	entries := []syncEntry{
		{model: config.Model{ID: "ollama/kimi:cloud", Family: "kimi", ProviderID: "ollama", ModelName: "kimi:cloud", Location: config.LocationCloud}, config: true, ollama: false},
	}
	m.list = buildSyncList(entries, testTheme(), 78, 22)
	m.phase = phaseList
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if m2.phase != phaseResolve {
		t.Fatalf("phase = %v, want phaseResolve", m2.phase)
	}
}

// TestEnterOnUntrackedGoesToEdit verifies that pressing Enter on an
// untracked entry transitions to the edit phase with pre-filled values.
func TestEnterOnUntrackedGoesToEdit(t *testing.T) {
	m := newModel(testTheme())
	m.width, m.height = 80, 24
	entries := []syncEntry{
		{model: config.Model{ID: "ollama/llama3.2:3b", Family: "llama3.2:3b", ProviderID: "ollama", ModelName: "llama3.2:3b", Location: config.LocationLocal}, config: false, ollama: true},
	}
	m.list = buildSyncList(entries, testTheme(), 78, 22)
	m.phase = phaseList
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if m2.phase != phaseEdit {
		t.Fatalf("phase = %v, want phaseEdit", m2.phase)
	}
	if !m2.editIsNew {
		t.Error("editIsNew should be true for untracked model")
	}
}

// TestEscFromEditReturnsToList verifies that Esc in the edit phase
// returns to the list phase.
func TestEscFromEditReturnsToList(t *testing.T) {
	m := newModel(testTheme())
	m.width, m.height = 80, 24
	m.phase = phaseEdit
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if m2.phase != phaseList {
		t.Fatalf("phase = %v, want phaseList", m2.phase)
	}
}

// TestEscFromResolveReturnsToList verifies that Esc in the resolve
// phase returns to the list phase.
func TestEscFromResolveReturnsToList(t *testing.T) {
	m := newModel(testTheme())
	m.width, m.height = 80, 24
	m.phase = phaseResolve
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if m2.phase != phaseList {
		t.Fatalf("phase = %v, want phaseList", m2.phase)
	}
}

// TestRKeyTriggersRefresh verifies that pressing 'r' in the list phase
// returns a command (the load command).
func TestRKeyTriggersRefresh(t *testing.T) {
	m := newModel(testTheme())
	m.width, m.height = 80, 24
	m.phase = phaseList
	m.ready = true
	// Need a list to avoid nil panics.
	m.list = list.New(nil, list.NewDefaultDelegate(), 78, 22)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd == nil {
		t.Fatal("expected load command from 'r' key, got nil")
	}
}

// TestQuitKeys verifies that q and ctrl+c quit the program.
func TestQuitKeys(t *testing.T) {
	cases := []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
		{Type: tea.KeyCtrlC},
	}
	for _, c := range cases {
		m := newModel(testTheme())
		m.phase = phaseList
		m.ready = true
		m.list = list.New(nil, list.NewDefaultDelegate(), 78, 22)
		_, cmd := m.Update(c)
		if cmd == nil {
			t.Errorf("key %q: got nil cmd, want tea.Quit", c.String())
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ollamaconfig/... -run "TestInit|TestUpdate|TestLoaded|TestEnter|TestEsc|TestRKey|TestQuit" -v`
Expected: FAIL — `model`, `newModel`, `phaseList`, `phaseEdit`, `phaseResolve`, `loadedMsg`, `buildSyncList` undefined

- [ ] **Step 3: Write the implementation**

Create `internal/ollamaconfig/ollamaconfig.go`:

```go
package ollamaconfig

import (
	"fmt"
	"os/exec"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/registry"
	"github.com/ohanaverse/agent-worktree/internal/themes"
	"github.com/ohanaverse/agent-worktree/internal/tui"
)

// phase identifies which screen the TUI is showing.
type phase int

const (
	phaseList    phase = iota // union list with status indicators
	phaseEdit                 // field-editing form
	phaseResolve              // pull / delete / cancel choice for missing models
)

// resolveChoice identifies a choice in the resolve prompt.
type resolveChoice int

const (
	resolvePullChoice   resolveChoice = iota
	resolveDeleteChoice
	resolveCancelChoice
)

// resolveItem adapts a resolve choice to list.Item.
type resolveItem struct {
	choice resolveChoice
	title  string
	desc   string
}

func (r resolveItem) FilterValue() string { return r.title }
func (r resolveItem) Title() string       { return r.title }
func (r resolveItem) Description() string { return r.desc }

// syncItem adapts a syncEntry to list.Item for the union list.
type syncItem struct {
	entry syncEntry
}

func (s syncItem) FilterValue() string { return s.entry.model.ModelName }
func (s syncItem) Title() string       { return s.entry.model.ModelName }

// Description renders the status + family + location + tags columns.
func (s syncItem) Description() string {
	family := s.entry.model.Family
	if family == "" {
		family = "-"
	}
	loc := string(s.entry.model.Location)
	if loc == "" {
		loc = "-"
	}
	tags := tagsToString(s.entry.model.Tags)
	if tags == "" {
		tags = "-"
	}
	return fmt.Sprintf("%-10s %-12s %-8s %-6s %s",
		s.entry.Status(),
		family,
		loc,
		tags,
		statusBadge(s.entry),
	)
}

// statusBadge returns the colored status symbol for an entry.
func statusBadge(e syncEntry) string {
	switch e.Status() {
	case "synced":
		return "●"
	case "missing":
		return "○"
	default:
		return "+"
	}
}

// buildSyncList constructs the bubbles/list for the union list.
func buildSyncList(entries []syncEntry, theme themes.Theme, width, height int) list.Model {
	items := make([]list.Item, 0, len(entries))
	for _, e := range entries {
		items = append(items, syncItem{entry: e})
	}
	l := list.New(items, tui.ThemedListDelegate(theme), width, height)
	l.Title = "Ollama Model Sync"
	l.SetShowStatusBar(false)
	return l
}

// buildResolveChoices creates the resolve prompt list items.
func buildResolveChoices(modelName string) []list.Item {
	return []list.Item{
		resolveItem{choice: resolvePullChoice, title: "Pull with ollama", desc: "download " + modelName + " via `ollama pull`"},
		resolveItem{choice: resolveDeleteChoice, title: "Delete from config", desc: "remove this model from config.toml"},
		resolveItem{choice: resolveCancelChoice, title: "Cancel", desc: "return to list"},
	}
}

// loadedMsg carries the union entries to Update.
type loadedMsg struct {
	entries     []syncEntry
	ollamaFound bool
	err         error
}

// pulledMsg is emitted after an ollama pull completes.
type pulledMsg struct {
	err error
}

// savedMsg is emitted after a config save completes.
type savedMsg struct {
	err error
}

// model holds the entire UI state.
type model struct {
	theme  themes.Theme
	width  int
	height int

	phase phase
	ready bool

	list    list.Model // union list
	resolve list.Model // resolve choice list

	// edit screen state
	editModel    config.Model // model being edited
	editIsNew    bool          // true when adding an untracked model
	editCursor   int           // 0=family, 1=location, 2=tags
	familyInput  textinput.Model
	tagsInput    textinput.Model
	editLocation config.Location // current location value in edit screen
	editError    string

	// status messages
	status    string // shown above the list
	listError string // shown above the list after a failed action

	// current entries (for refresh and lookup)
	entries []syncEntry
}

// newModel constructs the initial model.
func newModel(theme themes.Theme) model {
	return model{
		theme:        theme,
		familyInput:  textinput.New(),
		tagsInput:    textinput.New(),
		editLocation: config.LocationLocal,
	}
}

// Init returns the initial command: load config + ollama list.
func (m model) Init() tea.Cmd {
	return loadCmd()
}

// loadCmd reads config.toml and runs ollama list, returning a loadedMsg
// with the computed union.
// ollamaInstalled reports whether the ollama binary is on $PATH.
func ollamaInstalled() bool {
	_, err := exec.LookPath("ollama")
	return err == nil
}

func loadCmd() tea.Cmd {
	return func() tea.Msg {
		cfg, err := config.Load()
		if err != nil {
			return loadedMsg{err: err}
		}
		discovered, err := registry.Ollama{}.Discover()
		if err != nil {
			return loadedMsg{err: err}
		}
		entries := computeUnion(cfg.Models, discovered)
		return loadedMsg{
			entries:     entries,
			ollamaFound: len(discovered) > 0 || ollamaInstalled(),
		}
	}
}

// pullCmd runs `ollama pull <modelName>` and returns a pulledMsg.
// The actual terminal release/restore is handled in Update via the
// program's ReleaseTerminal/RestoreTerminal methods.
var pullCmd = func(modelName string) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("ollama", "pull", modelName)
		cmd.Stdin = nil
		cmd.Stdout = nil
		cmd.Stderr = nil
		err := cmd.Run()
		return pulledMsg{err: err}
	}
}

// saveCmd writes the config to disk and returns a savedMsg.
func saveCmd(cfg *config.Config) tea.Cmd {
	return func() tea.Msg {
		return savedMsg{err: config.Save(cfg)}
	}
}

// Update handles messages and returns the new state plus optional commands.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.ready {
			m.list.SetSize(msg.Width-2, msg.Height-2)
		}
		if m.phase == phaseResolve {
			m.resolve.SetSize(msg.Width-2, msg.Height-2)
		}
		if m.phase == phaseEdit {
			m.familyInput.Width = msg.Width - 20
			m.tagsInput.Width = msg.Width - 20
		}
		return m, nil

	case loadedMsg:
		if msg.err != nil {
			m.listError = msg.err.Error()
			if !m.ready {
				m.status = "error: " + msg.err.Error()
			}
			return m, nil
		}
		m.listError = ""
		m.entries = msg.entries
		m.list = buildSyncList(msg.entries, m.theme, m.width-2, m.height-2)
		m.ready = true
		m.phase = phaseList
		if !msg.ollamaFound {
			m.status = "ollama not found — showing config models only"
		} else {
			m.status = ""
		}
		return m, nil

	case pulledMsg:
		if msg.err != nil {
			m.listError = "pull failed: " + msg.err.Error()
		}
		return m, loadCmd()

	case savedMsg:
		if msg.err != nil {
			m.editError = "save failed: " + msg.err.Error()
			return m, nil
		}
		return m, loadCmd()

	case tea.KeyMsg:
		switch msg.String() {
		case "q":
			if m.phase == phaseList && m.ready {
				return m, tea.Quit
			}
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.phase == phaseEdit {
				m.phase = phaseList
				m.editError = ""
				return m, nil
			}
			if m.phase == phaseResolve {
				m.phase = phaseList
				return m, nil
			}
			if m.phase == phaseList && m.ready {
				return m, tea.Quit
			}
		case "enter":
			return m.handleEnter()
		case "r":
			if m.phase == phaseList && m.ready {
				return m, loadCmd()
			}
		case "tab":
			if m.phase == phaseEdit {
				m.editCursor = (m.editCursor + 1) % 3
				m.focusEditInput()
				return m, nil
			}
		case "shift+tab":
			if m.phase == phaseEdit {
				m.editCursor = (m.editCursor + 2) % 3
				m.focusEditInput()
				return m, nil
			}
		}
	}

	// Delegate to the active list/textinput.
	if m.phase == phaseList && m.ready {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}
	if m.phase == phaseResolve {
		var cmd tea.Cmd
		m.resolve, cmd = m.resolve.Update(msg)
		return m, cmd
	}
	if m.phase == phaseEdit {
		return m.handleEditUpdate(msg)
	}
	return m, nil
}

// handleEnter dispatches Enter based on the current phase and the
// selected entry's status.
func (m model) handleEnter() (tea.Model, tea.Cmd) {
	switch m.phase {
	case phaseList:
		if !m.ready {
			return m, nil
		}
		item, ok := m.list.SelectedItem().(syncItem)
		if !ok {
			return m, nil
		}
		entry := item.entry
		switch entry.Status() {
		case "synced":
			m.enterEdit(entry.model, false)
			return m, nil
		case "missing":
			m.enterResolve(entry.model)
			return m, nil
		case "untracked":
			m.enterEdit(entry.model, true)
			return m, nil
		}
	case phaseResolve:
		item, ok := m.resolve.SelectedItem().(resolveItem)
		if !ok {
			return m, nil
		}
		switch item.choice {
		case resolvePullChoice:
			modelName := m.editModel.ModelName
			m.phase = phaseList
			return m, pullCmd(modelName)
		case resolveDeleteChoice:
			return m.handleDelete()
		case resolveCancelChoice:
			m.phase = phaseList
			return m, nil
		}
	case phaseEdit:
		return m.handleEditSave()
	}
	return m, nil
}

// enterResolve sets up the resolve choice list for a missing model.
func (m *model) enterResolve(mod config.Model) {
	m.editModel = mod
	m.resolve = list.New(buildResolveChoices(mod.ModelName), tui.ThemedListDelegate(m.theme), m.width-2, m.height-2)
	m.resolve.Title = "Resolve: " + mod.ModelName + " (not in ollama list)"
	m.resolve.SetShowStatusBar(false)
	m.phase = phaseResolve
}

// enterEdit sets up the edit screen for a model. isNew is true for
// untracked models being added to config.
func (m *model) enterEdit(mod config.Model, isNew bool) {
	m.editModel = mod
	m.editIsNew = isNew
	m.editError = ""
	m.editCursor = 0

	// Pre-fill inputs.
	m.familyInput = textinput.New()
	m.familyInput.SetValue(mod.Family)
	m.familyInput.Focus()
	m.familyInput.Width = m.width - 20

	m.tagsInput = textinput.New()
	m.tagsInput.SetValue(tagsToString(mod.Tags))
	m.tagsInput.Width = m.width - 20

	m.editLocation = mod.Location
	if m.editLocation == "" {
		m.editLocation = config.LocationLocal
	}

	m.phase = phaseEdit
}

// focusEditInput focuses/blurs textinputs based on editCursor.
func (m *model) focusEditInput() {
	switch m.editCursor {
	case 0:
		m.familyInput.Focus()
		m.tagsInput.Blur()
	case 2:
		m.familyInput.Blur()
		m.tagsInput.Focus()
	default:
		m.familyInput.Blur()
		m.tagsInput.Blur()
	}
}

// handleEditUpdate delegates key handling to the focused textinput or
// handles the location toggle.
func (m model) handleEditUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.editCursor == 1 {
		// Location field: any key toggles. Tab/enter/esc are handled
		// by the parent Update, so we only get here for other keys.
		if km, ok := msg.(tea.KeyMsg); ok {
			// Ignore navigation keys that bubbles doesn't handle.
			switch km.String() {
			case "up", "down", "left", "right":
				return m, nil
			}
			m.editLocation = toggleLocation(m.editLocation)
			return m, nil
		}
		return m, nil
	}
	// Delegate to the focused textinput.
	if m.editCursor == 0 {
		var cmd tea.Cmd
		m.familyInput, cmd = m.familyInput.Update(msg)
		return m, cmd
	}
	if m.editCursor == 2 {
		var cmd tea.Cmd
		m.tagsInput, cmd = m.tagsInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

// handleEditSave validates and saves the edited model to config.toml.
func (m model) handleEditSave() (tea.Model, tea.Cmd) {
	cfg, err := config.Load()
	if err != nil {
		m.editError = "config load failed: " + err.Error()
		return m, nil
	}
	updated := m.editModel
	updated.Family = m.familyInput.Value()
	updated.Tags = parseTags(m.tagsInput.Value())
	updated.Location = m.editLocation
	if m.editIsNew {
		updated.ID = "ollama/" + updated.ModelName
		updated.ProviderID = "ollama"
		updated.Source = config.SourceCurated
	}
	saveModelToConfig(cfg, updated, m.editIsNew)
	if err := cfg.Validate(); err != nil {
		m.editError = "validation: " + err.Error()
		return m, nil
	}
	return m, saveCmd(cfg)
}

// handleDelete removes the current model from config.toml.
func (m model) handleDelete() (tea.Model, tea.Cmd) {
	cfg, err := config.Load()
	if err != nil {
		m.listError = "config load failed: " + err.Error()
		return m, loadCmd()
	}
	deleteModelFromConfig(cfg, m.editModel.ID)
	return m, saveCmd(cfg)
}

// View renders the screen as a string.
func (m model) View() string {
	if !m.ready && m.phase == phaseList {
		if m.status != "" {
			return m.status
		}
		return "loading ollama models..."
	}
	switch m.phase {
	case phaseEdit:
		return m.viewEdit()
	case phaseResolve:
		if m.width <= 0 || m.height <= 0 {
			return "resolve prompt (waiting for window size)"
		}
		return m.resolve.View() + "\n[enter] choose   [esc] back"
	default:
		return m.viewList()
	}
}

// viewList renders the union list screen.
func (m model) viewList() string {
	if m.width <= 0 || m.height <= 0 {
		return "ollama model sync (waiting for window size)"
	}
	var header string
	if m.listError != "" {
		header = errorStyle(m.theme).Render("error: "+m.listError) + "\n"
	}
	footer := "\n[enter] edit / resolve   [r] refresh   [q] quit"
	return header + m.list.View() + footer
}

// viewEdit renders the edit screen.
func (m model) viewEdit() string {
	if m.width <= 0 || m.height <= 0 {
		return "edit model (waiting for window size)"
	}
	dimStyle := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	accentStyle := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenAccent))
	readOnly := func(label, value string) string {
		return fmt.Sprintf("  %-12s %s  %s", label, value, dimStyle.Render("(read-only)"))
	}
	editable := func(label, value string, cursor bool) string {
		marker := ""
		if cursor {
			marker = "  " + accentStyle.Render("← cursor")
		}
		return fmt.Sprintf("  %-12s [%s]%s", label, value, marker)
	}
	var locDisplay string
	if m.editCursor == 1 {
		locDisplay = editable("location", string(m.editLocation), true)
	} else {
		locDisplay = editable("location", string(m.editLocation), false)
	}
	s := fmt.Sprintf("  Edit Model: %s\n\n", m.editModel.ModelName)
	idDisplay := m.editModel.ID
	if m.editIsNew {
		idDisplay = "ollama/" + m.editModel.ModelName + "  (auto-generated)"
	}
	s += readOnly("id", idDisplay) + "\n"
	s += readOnly("model_name", m.editModel.ModelName) + "\n"
	s += readOnly("provider", "ollama") + "\n"
	s += editable("family", m.familyInput.View(), m.editCursor == 0) + "\n"
	s += locDisplay + "\n"
	s += editable("tags", m.tagsInput.View(), m.editCursor == 2) + "\n"
	if m.editError != "" {
		s += "\n" + errorStyle(m.theme).Render(m.editError) + "\n"
	}
	s += "\n  [tab/shift+tab] next/prev field   [enter] save   [esc] cancel"
	return s
}

// errorStyle returns the lipgloss style for errors.
func errorStyle(theme themes.Theme) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Token(themes.TokenError))
}

// currentProgram holds the running tea.Program so pullCmd can
// release/restore the terminal.
var currentProgram *tea.Program

// Run starts the TUI in alternate-screen mode and returns when it quits.
func Run(theme themes.Theme) error {
	m := newModel(theme)
	p := tea.NewProgram(m, tea.WithAltScreen())
	currentProgram = p
	_, err := p.Run()
	return err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/ollamaconfig/... -v`
Expected: PASS (all tests)

- [ ] **Step 5: Commit**

```bash
git add internal/ollamaconfig/ollamaconfig.go internal/ollamaconfig/ollamaconfig_test.go
git commit -m "feat(ollamaconfig): TUI model with list, edit, and resolve screens"
```

---

### Task 6: Terminal Release/Restore for Pull

**Files:**
- Modify: `internal/ollamaconfig/ollamaconfig.go`

The `pullCmd` currently runs `ollama pull` without terminal access. It needs to release the terminal so the user sees native progress output, then restore it — same pattern as `internal/tui/launch.go`'s `runAndWaitCmd`.

- [ ] **Step 1: Update pullCmd to release/restore terminal**

Replace the `pullCmd` function variable in `internal/ollamaconfig/ollamaconfig.go`:

```go
// pullCmd runs `ollama pull <modelName>`. It releases the terminal so
// the user sees native ollama progress output, then restores the TUI.
// Tests override this variable to avoid real subprocess calls.
var pullCmd = func(modelName string) tea.Cmd {
	return func() tea.Msg {
		if currentProgram != nil {
			currentProgram.ReleaseTerminal()
		}
		cmd := exec.Command("ollama", "pull", modelName)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if currentProgram != nil {
			currentProgram.RestoreTerminal()
		}
		return pulledMsg{err: err}
	}
}
```

Add `"os"` to the import block in `ollamaconfig.go`.

- [ ] **Step 2: Run tests to verify they pass**

Run: `go test ./internal/ollamaconfig/... -v`
Expected: PASS (pullCmd is a variable, tests don't call it directly — the phase transition tests verify the command is returned, not executed)

- [ ] **Step 3: Commit**

```bash
git add internal/ollamaconfig/ollamaconfig.go
git commit -m "feat(ollamaconfig): release/restore terminal for ollama pull"
```

---

### Task 7: Cobra Wiring

**Files:**
- Modify: `cmd/wt/commands_config.go`

- [ ] **Step 1: Add the configOllamaCmd function and register it**

In `cmd/wt/commands_config.go`, add the import for the new package and the command function.

Add to imports:

```go
	"github.com/ohanaverse/agent-worktree/internal/ollamaconfig"
```

Add the command function (after `configPathCmd`):

```go
// configOllamaCmd returns the `wt config ollama` command. Launches an
// interactive TUI for syncing config.toml ollama models with `ollama list`.
func configOllamaCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "ollama",
		Short: "Sync ollama models between config.toml and `ollama list`",
		Long: "Launch an interactive TUI to sync ollama models between config.toml\n" +
			"and the local Ollama instance. Shows a union of both sources with\n" +
			"status indicators, and lets you edit, add, pull, or delete entries.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return ollamaconfig.Run(a.theme)
		},
	}
}
```

Register it in `configCmd`:

```go
	cmd.AddCommand(configPathCmd(a), configThemeCmd(a), configOllamaCmd(a))
```

Update the `configCmd` Long help to mention `ollama`:

```go
		Long: "Manage wt user preferences. Subcommands configure specific concerns:\n" +
			"  theme   active color theme\n" +
			"  path    print the config directory\n" +
			"  ollama  sync ollama models with config.toml",
```

- [ ] **Step 2: Build to verify compilation**

Run: `go build ./cmd/wt/`
Expected: No errors

- [ ] **Step 3: Run all tests to verify nothing broke**

Run: `go test ./... -count=1`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add cmd/wt/commands_config.go
git commit -m "feat(config): wt config ollama subcommand launches sync TUI"
```

---

### Task 8: Integration Smoke Test

**Files:**
- No new files — manual verification

- [ ] **Step 1: Build the binary**

Run: `go build -o bin/wt ./cmd/wt/`
Expected: No errors

- [ ] **Step 2: Verify the command appears in help**

Run: `./bin/wt config --help`
Expected: Help text lists `ollama` subcommand

- [ ] **Step 3: Verify the ollama subcommand help**

Run: `./bin/wt config ollama --help`
Expected: Shows the Long description about syncing ollama models

- [ ] **Step 4: Run the full test suite one final time**

Run: `go test ./... -count=1`
Expected: PASS

- [ ] **Step 5: Commit any final fixes**

If any issues were found and fixed during smoke testing, commit them. Otherwise, no commit needed.