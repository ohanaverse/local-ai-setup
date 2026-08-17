# List-Based Model Selection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the agent+model screen's "show one model, press `m` to browse" pattern with a single picker list filtered by the active agent's `SupportedProviders` and sourced from `config.toml` only. Eliminate the separate model browser screen, the `m` key, and the live model discovery that fed it.

**Architecture:** Two new helpers in `internal/config` (`ModelsForAgent`, `ModelsForAgentAndTag`) filter the catalog. A new `internal/tui/model_list.go` holds the `modelItem` type and the list builder (extracted from the deleted `model_browser.go`). `app.go`'s `model` struct loses browser-related fields and gains a `list.Model` for the picker. The `selectedEntryMsg` handler validates that the agent+tag has models and refuses to enter the picker with an empty list.

**Tech Stack:** Go 1.26.3, Bubble Tea (`bubbles/list`, `bubbles/textinput`), `bubble/list`'s default delegate, `lipgloss` for the surrounding style.

**Reference spec:** `docs/superpowers/specs/2026-08-17-list-based-model-selection-design.md`

---

## File Structure

| Path | Action | Responsibility |
|---|---|---|
| `internal/config/config.go` | Modify | Add `ModelsForAgent` and `ModelsForAgentAndTag` helpers. |
| `internal/tui/model_list.go` | Create | `modelItem` type, `buildModelList` helper, `phaseModelView` rendering function. |
| `internal/tui/app.go` | Modify | Reshape `model` struct, rewrite `selectedEntryMsg`, `r`/`d`/`enter`/`esc`/`WindowSize` handlers, drop `phaseBrowser` and `m` key. |
| `internal/tui/model_browser.go` | **Delete** | Replaced by `model_list.go`. |
| `internal/tui/agent_model_test.go` | Modify | Drop `(none)` placeholder tests, rewrite rotation/toggle tests for the new state shape, add new Bucket-C tests, add `phaseModelWithList` test helper. |
| `internal/tui/app_test.go` | Modify | Update the ~5 model-phase tests that reference removed fields or old View shape. |
| `internal/tui/model_browser_test.go` | **Delete** | All browser tests are gone with the browser. |

Files NOT touched: `internal/registry/` (still used by `wt models`), `docs/go-course/*` (historical record, frozen by user instruction), shell-agent path, ollama check, session resume.

---

## Conventions

- **Commits** are mandatory after each task. Branch name: `feat/list-based-model-selection` (created at Task 2).
- **TDD**: every behavioral change is preceded by a failing test.
- **Test isolation**: any test that triggers `rotation.Next` (i.e. presses `r`) must use `tempStateDir(t)` to isolate `XDG_CONFIG_HOME` from the host's real rotation state.
- **Helper signature** used in tests: `phaseModelWithList(t *testing.T, cfg *config.Config, agent, tag string) model` — defined in Task 7. All Bucket-A/C tests in `agent_model_test.go` that build a `phaseModel` state use this helper instead of constructing `model` literals.
- **`go test ./...` runs after each task.** Expected: green.
- **`docs/go-course/*` is never edited.**

---

## Task 1: Update `testConfig()` to declare supported providers

**Files:**
- Modify: `internal/tui/app_test.go`

The existing `testConfig()` helper has `Agents: []config.Agent{{Name: "claude"}}` with no `SupportedProviders`. The new `ModelsForAgent` helper returns no models for an agent with no supported providers, which would make the validation gate in `selectedEntryMsg` fire for every test using this config. Fix the test config first so the rest of the plan can build on a sound foundation.

- [ ] **Step 1: Update the `Agents` field in `testConfig()`**

In `internal/tui/app_test.go`, find:

```go
Agents: []config.Agent{
    {Name: "claude"},
},
```

Replace with:

```go
Agents: []config.Agent{
    {Name: "claude", SupportedProviders: []string{"ollama"}},
},
```

`testConfig()` predates this redesign and assumed no agent/provider filter. With the new `ModelsForAgent` helper, an agent without `SupportedProviders` returns no models, and the validation gate in `selectedEntryMsg` will fire for every test that uses this config. The fix is one line: declare that `claude` supports the `ollama` provider.

- [ ] **Step 2: Update the doc comment on `testConfig()` to reflect this**

The existing comment says "firstAgent/firstModel and the rotation have something to operate on." With the new shape, mention the agent/provider filter:

```go
// testConfig returns a Config with one agent (claude, supporting the
// ollama provider) and a two-model code tag group plus one design model.
// Tests across the picker, rotation, and launch need a real catalog so
// the agent filter, tag filter, and rotation all have something to
// operate on.
```

- [ ] **Step 3: Run the existing test suite to confirm no other config-related breakage**

Run: `go test ./internal/tui -v 2>&1 | tail -20`
Expected: most tests still pass (the `SupportedProviders` change is additive; the only tests that depend on `claude` having no provider filter are the ones this plan rewrites).

- [ ] **Step 4: Commit**

```bash
git add internal/tui/app_test.go
git commit -m "test(tui): declare ollama as claude's supported provider in testConfig"
```

---

## Task 2: Create the feature branch and add `ModelsForAgent`

**Files:**
- Modify: `internal/config/config_test.go`
- Modify: `internal/config/config.go`

- [ ] **Step 1: Create and check out the feature branch**

```bash
cd /Users/keith/github/ohanaverse/agent-worktree
git checkout main
git pull --ff-only
git checkout -b feat/list-based-model-selection
```

- [ ] **Step 2: Write the failing test for `ModelsForAgent`**

In `internal/config/config_test.go`, append at the end of the file (after the last `func Test…`):

```go
// TestModelsForAgentFiltersByProvider asserts ModelsForAgent returns only
// models whose ProviderID is in the agent's supported_providers list.
// Without this filter the TUI list would show models the active agent
// cannot drive (e.g. claude/native listed for the codex agent).
func TestModelsForAgentFiltersByProvider(t *testing.T) {
    cfg := &config.Config{
        Providers: []config.Provider{
            {ID: "ollama"},
            {ID: "claude"},
        },
        Models: []config.Model{
            {ID: "claude/native", ProviderID: "claude", ModelName: "native"},
            {ID: "ollama/a:9b", ProviderID: "ollama", Tags: []string{"code"}},
            {ID: "ollama/b:9b", ProviderID: "ollama", Tags: []string{"code"}},
        },
        Agents: []config.Agent{
            {Name: "codex", SupportedProviders: []string{"ollama"}},
        },
    }
    got, err := cfg.ModelsForAgent("codex")
    if err != nil {
        t.Fatalf("ModelsForAgent: %v", err)
    }
    if len(got) != 2 {
        t.Fatalf("len = %d, want 2 (claude/native filtered out)", len(got))
    }
    for _, m := range got {
        if m.ProviderID != "ollama" {
            t.Errorf("got model %q with provider %q, want only ollama", m.ID, m.ProviderID)
        }
    }
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/config -run TestModelsForAgentFiltersByProvider -v`
Expected: compile error (`ModelsForAgent` undefined) or runtime nil-pointer. The test cannot compile yet.

- [ ] **Step 4: Stub the helper with a minimal signature**

In `internal/config/config.go`, add at the end of the file (after the existing helpers like `ProvidersForAgent`):

```go
// ModelsForAgent returns the models whose ProviderID is in the named
// agent's supported_providers list. Order matches cfg.Models.
//
// Errors:
//   - agent not found in cfg.Agents
//   - agent references a provider not in cfg.Providers (only reachable
//     if Validate was bypassed)
func (c *Config) ModelsForAgent(agentName string) ([]Model, error) {
    a, err := c.AgentByName(agentName)
    if err != nil {
        return nil, err
    }
    allowed := map[string]bool{}
    for _, pid := range a.SupportedProviders {
        allowed[pid] = true
    }
    var out []Model
    for _, m := range c.Models {
        if allowed[m.ProviderID] {
            out = append(out, m)
        }
    }
    return out, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/config -run TestModelsForAgentFiltersByProvider -v`
Expected: PASS.

- [ ] **Step 6: Add the agent-not-found test**

Append to `internal/config/config_test.go`:

```go
// TestModelsForAgentUnknownAgent asserts that asking for a non-existent
// agent returns an error rather than panicking. The TUI uses this to
// surface a clear error if --agent points at a typo.
func TestModelsForAgentUnknownAgent(t *testing.T) {
    cfg := &config.Config{
        Agents: []config.Agent{{Name: "claude"}},
    }
    _, err := cfg.ModelsForAgent("nope")
    if err == nil {
        t.Fatal("expected error for unknown agent, got nil")
    }
}
```

- [ ] **Step 7: Run the full config test suite**

Run: `go test ./internal/config -v`
Expected: all PASS (this is in addition to the existing 38 config tests).

- [ ] **Step 8: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add ModelsForAgent helper"
```

---

## Task 3: `ModelsForAgentAndTag` helper

**Files:**
- Modify: `internal/config/config_test.go`
- Modify: `internal/config/config.go`

- [ ] **Step 1: Write the failing test for `ModelsForAgentAndTag`**

Append to `internal/config/config_test.go`:

```go
// TestModelsForAgentAndTagIntersectsBoth asserts the helper composes the
// agent filter and the tag filter. A model that passes the agent filter
// but is tagged with a different group must be excluded.
func TestModelsForAgentAndTagIntersectsBoth(t *testing.T) {
    cfg := &config.Config{
        Providers: []config.Provider{{ID: "ollama"}},
        Models: []config.Model{
            {ID: "ollama/code-1", ProviderID: "ollama", Tags: []string{"code"}},
            {ID: "ollama/design-1", ProviderID: "ollama", Tags: []string{"design"}},
            {ID: "ollama/both", ProviderID: "ollama", Tags: []string{"code", "design"}},
        },
        Agents: []config.Agent{
            {Name: "codex", SupportedProviders: []string{"ollama"}},
        },
    }
    got, err := cfg.ModelsForAgentAndTag("codex", "code")
    if err != nil {
        t.Fatalf("ModelsForAgentAndTag: %v", err)
    }
    if len(got) != 2 {
        t.Fatalf("len = %d, want 2 (ollama/code-1, ollama/both)", len(got))
    }
    for _, m := range got {
        if !m.HasTag("code") {
            t.Errorf("got %q without code tag", m.ID)
        }
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config -run TestModelsForAgentAndTagIntersectsBoth -v`
Expected: compile error.

- [ ] **Step 3: Implement the helper**

Append to `internal/config/config.go`:

```go
// ModelsForAgentAndTag intersects ModelsForAgent with HasTag(tag).
// tag == "" returns all agent-compatible models (no tag filter).
func (c *Config) ModelsForAgentAndTag(agentName, tag string) ([]Model, error) {
    ms, err := c.ModelsForAgent(agentName)
    if err != nil {
        return nil, err
    }
    var out []Model
    for _, m := range ms {
        if tag == "" || m.HasTag(tag) {
            out = append(out, m)
        }
    }
    return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config -run TestModelsForAgentAndTagIntersectsBoth -v`
Expected: PASS.

- [ ] **Step 5: Run the full config test suite**

Run: `go test ./internal/config -v`
Expected: all PASS (this is in addition to the existing 38 config tests plus the new TestsForAgent tests from Task 2).

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add ModelsForAgentAndTag helper"
```

---

## Task 4: `oppositeTag` helper

**Files:**
- Create: `internal/tui/rotation_helpers.go`
- Create: `internal/tui/rotation_helpers_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/tui/rotation_helpers_test.go`:

```go
package tui

import "testing"

// TestOppositeTagSwapsCodeAndDesign asserts the helper swaps the two
// known tag group names. The cross-tag skip in rotation calls this to
// know which other tag's last-used model to avoid.
func TestOppositeTagSwapsCodeAndDesign(t *testing.T) {
    cases := map[string]string{
        "code":          "design",
        "design":        "code",
        "":              "",
        "anything-else": "",
    }
    for in, want := range cases {
        if got := oppositeTag(in); got != want {
            t.Errorf("oppositeTag(%q) = %q, want %q", in, got, want)
        }
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui -run TestOppositeTagSwapsCodeAndDesign -v`
Expected: compile error (`oppositeTag` undefined).

- [ ] **Step 3: Implement the helper**

Create `internal/tui/rotation_helpers.go`:

```go
package tui

// oppositeTag returns the other rotation group name. For the current
// code/design setup that's a literal swap. Unknown tags return "" so
// rotation.ForTag.Next("") is a no-skip call rather than skipping the
// current tag's last-used model by accident.
func oppositeTag(tag string) string {
    switch tag {
    case "code":
        return "design"
    case "design":
        return "code"
    default:
        return ""
    }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tui -run TestOppositeTagSwapsCodeAndDesign -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/rotation_helpers.go internal/tui/rotation_helpers_test.go
git commit -m "feat(tui): add oppositeTag helper for cross-tag skip"
```

---

## Task 5: Move `modelItem` to `model_list.go` (drop source column)

**Files:**
- Create: `internal/tui/model_list.go`

The current `modelItem` in `model_browser.go` has a `Source` column in its description. After this change the list is sourced from `config.toml` only, so every model has `Source == SourceCurated` and the column is redundant. We move the type and drop the column.

- [ ] **Step 1: Create `model_list.go` with the trimmed item type**

Create `internal/tui/model_list.go`:

```go
package tui

import (
    "fmt"
    "strings"

    "github.com/charmbracelet/bubbles/list"
    "github.com/ohanaverse/agent-worktree/internal/config"
)

// modelItem adapts a config.Model to a list.Item for the model picker.
type modelItem struct {
    model config.Model
}

// FilterValue returns the model ID so the list's built-in fuzzy filter
// (currently unused) would narrow by ID.
func (m modelItem) FilterValue() string { return m.model.ID }

// Title renders the model ID — the primary identifier users scan for.
func (m modelItem) Title() string { return m.model.ID }

// Description renders the metadata columns: provider, location, tags.
// Source is omitted because the picker is sourced from config.toml only
// (every model is curated). The column is reserved for future use if
// the picker grows a source filter.
func (m modelItem) Description() string {
    tags := strings.Join(m.model.Tags, ",")
    if tags == "" {
        tags = "-"
    }
    return fmt.Sprintf("%-10s %-6s %s",
        m.model.ProviderID,
        string(m.model.Location),
        tags,
    )
}

// buildModelList builds a bubble/list from the given models. The caller
// passes the desired width/height. The list is created with the default
// delegate and a fixed title.
func buildModelList(models []config.Model, width, height int) list.Model {
    items := make([]list.Item, 0, len(models))
    for _, m := range models {
        items = append(items, modelItem{model: m})
    }
    l := list.New(items, list.NewDefaultDelegate(), width, height)
    l.Title = "Models"
    l.SetShowStatusBar(false)
    return l
}
```

- [ ] **Step 2: Verify the package still builds**

Run: `go build ./...`
Expected: success. `modelItem` now exists in two places (this file and `model_browser.go`); that's intentional — the browser file gets deleted in Task 11.

- [ ] **Step 3: Commit**

```bash
git add internal/tui/model_list.go
git commit -m "feat(tui): extract modelItem and buildModelList to model_list.go"
```

---

## Task 6: Add the picker fields to the `model` struct

**Files:**
- Modify: `internal/tui/app.go` (struct definition only)

- [ ] **Step 1: Add the picker fields**

In `internal/tui/app.go`, in the `model` struct, add the new fields right after the `current` field (the comment block above should read "model picker"):

```go
    // model picker (formerly phaseBrowser, now part of phaseModel)
    models    list.Model // bubble/list of agent+tag models
    modelsTag string     // tag the list was built for; rebuild on change
    modelsFor string     // agent the list was built for; rebuild on change
```

- [ ] **Step 2: Verify the package builds**

Run: `go build ./...`
Expected: success. Existing browser fields (`browser`, `browserCache`, `browserTag`, `sourceCycle`) are still there; we remove them in Task 11.

- [ ] **Step 3: Run the test suite to confirm no regression yet**

Run: `go test ./...`
Expected: PASS (no behavior change yet, just struct fields).

- [ ] **Step 4: Commit**

```bash
git add internal/tui/app.go
git commit -m "feat(tui): add picker list fields to model struct"
```

---

## Task 7: Test helper `phaseModelWithList` + initial list-build helper test

**Files:**
- Modify: `internal/tui/agent_model_test.go`

- [ ] **Step 1: Write the failing test for the helper**

At the very top of `internal/tui/agent_model_test.go` (after the `tempStateDir` and `seedState` helpers, before the first `Test…` function), add:

```go
// phaseModelWithList builds a model in phaseModel with m.models populated
// for the given agent+tag, m.current set to the rotation's next-to-use
// model, and the list cursor on that model. Tests that exercise 'r', 'd',
// or the View use this helper instead of constructing model literals
// (which would skip the list-build path the production code uses).
func phaseModelWithList(t *testing.T, cfg *config.Config, agent, tag string) model {
    t.Helper()
    models, err := cfg.ModelsForAgentAndTag(agent, tag)
    if err != nil {
        t.Fatalf("ModelsForAgentAndTag: %v", err)
    }
    if len(models) == 0 {
        t.Fatalf("phaseModelWithList: no models for agent %q tag %q", agent, tag)
    }
    m := model{
        cfg:       cfg,
        phase:     phaseModel,
        agent:     agent,
        tag:       tag,
        models:    buildModelList(models, 80, 24),
        modelsFor: agent,
        modelsTag: tag,
        width:     80,
        height:    24,
    }
    if next, ok := rotation.ForTag(cfg, tag).Next(""); ok {
        m.current = next
        m.models.Select(indexOfModel(models, next))
    }
    return m
}
```

Then, just before that helper, define the `indexOfModel` it uses:

```go
// indexOfModel returns the index of m in models, or -1 if not found.
// Used to position the list cursor on the rotation's next-to-use model.
func indexOfModel(models []config.Model, target config.Model) int {
    for i, m := range models {
        if m.ID == target.ID {
            return i
        }
    }
    return -1
}
```

- [ ] **Step 2: Run test compile to verify the helper exists**

Run: `go test ./internal/tui -run TestXxxx -v 2>&1 | head -20`
Expected: compile success. (No test by that name; we just want to see "no such test" not "undefined function".)

Actually, run: `go vet ./internal/tui`
Expected: success.

- [ ] **Step 3: Add a test that asserts the helper builds the list correctly**

Append to `internal/tui/agent_model_test.go`:

```go
// TestPhaseModelWithListBuildsAndPositionsCursor asserts the test
// helper populates m.models and positions the cursor on the rotation's
// next-to-use model. The production code uses the same list-build path
// (in selectedEntryMsg and on 'd'), so this is a smoke test of the
// shared infrastructure.
func TestPhaseModelWithListBuildsAndPositionsCursor(t *testing.T) {
    dir := tempStateDir(t)
    seedState(t, dir, "code", "0\nollama/gemma4:9b\n")
    cfg := testConfig()
    m := phaseModelWithList(t, cfg, "claude", "code")

    if got := len(m.models.Items()); got != 2 {
        t.Errorf("models items = %d, want 2 (testConfig code models)", got)
    }
    if m.current.ID != "ollama/gemma4:14b" {
        t.Errorf("current = %q, want ollama/gemma4:14b (rotation index 1)", m.current.ID)
    }
    if m.models.SelectedIndex() != 1 {
        t.Errorf("cursor index = %d, want 1", m.models.SelectedIndex())
    }
}
```

- [ ] **Step 4: Run the new test**

Run: `go test ./internal/tui -run TestPhaseModelWithListBuildsAndPositionsCursor -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/agent_model_test.go
git commit -m "test(tui): add phaseModelWithList helper and smoke test"
```

---

## Task 8: Rewrite `selectedEntryMsg` to use the picker (with validation gate)

**Files:**
- Modify: `internal/tui/app.go` (`Update` function, `selectedEntryMsg` case)

- [ ] **Step 1: Write the failing test for the empty-list case**

In `internal/tui/agent_model_test.go`, replace the existing `TestSelectedEntryMsgNoModelsShowsPlaceholder` (the test that asserts a `(none)` placeholder) with the new behavior:

Find the existing function:

```go
// TestSelectedEntryMsgNoModelsShowsPlaceholder asserts that picking a
// worktree when the active tag group is empty still lands on the model phase
// with a "(none)" model rather than panicking or hanging. The user sees a
// fallback screen even with a sparse catalog.
func TestSelectedEntryMsgNoModelsShowsPlaceholder(t *testing.T) {
    m := model{cfg: &config.Config{DefaultTag: "code", Agents: []config.Agent{{Name: "claude"}}}}
    got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
    gotModel := got.(model)
    if gotModel.phase != phaseModel {
        t.Fatalf("phase = %v, want phaseModel", gotModel.phase)
    }
    if gotModel.current.ID != "(none)" {
        t.Errorf("current = %q, want (none)", gotModel.current.ID)
    }
}
```

Replace it with two tests:

```go
// TestSelectedEntryMsgEmptyListStaysOnList asserts that when the agent+tag
// has no compatible models, picking a worktree does NOT enter the model
// phase. The validation gate at the phaseList → phaseModel boundary
// surfaces a status message and keeps the user in the picker.
func TestSelectedEntryMsgEmptyListStaysOnList(t *testing.T) {
    cfg := &config.Config{
        DefaultTag: "code",
        Agents:     []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
        Providers:  []config.Provider{{ID: "ollama"}},
        // No models with provider "ollama".
    }
    m := model{cfg: cfg, phase: phaseList}
    got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
    gotModel := got.(model)
    if gotModel.phase != phaseList {
        t.Errorf("phase = %v, want phaseList (no entry into picker)", gotModel.phase)
    }
    if gotModel.status == "" {
        t.Error("status = empty, want an error message")
    }
}

// TestSelectedEntryMsgEmptyListSetsActionableStatus asserts the status
// message names the agent and the tag so the user knows what to fix.
func TestSelectedEntryMsgEmptyListSetsActionableStatus(t *testing.T) {
    cfg := &config.Config{
        DefaultTag: "code",
        Agents:     []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
        Providers:  []config.Provider{{ID: "ollama"}},
    }
    m := model{cfg: cfg, phase: phaseList, agent: "claude", tag: "code"}
    got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
    gotModel := got.(model)
    for _, want := range []string{"claude", "code"} {
        if !strings.Contains(gotModel.status, want) {
            t.Errorf("status %q missing %q", gotModel.status, want)
        }
    }
}
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./internal/tui -run 'TestSelectedEntryMsgEmptyList' -v`
Expected: both FAIL. The current `selectedEntryMsg` handler unconditionally sets `phase = phaseModel` and assigns `m.current = firstModel(...)`, which doesn't error on empty.

- [ ] **Step 3: Rewrite the `selectedEntryMsg` case**

In `internal/tui/app.go`, find:

```go
case selectedEntryMsg:
    // Resolve the agent: --agent flag wins, else the config default.
    m.selectedPath = msg.entry.Path
    if m.initialAgent != "" {
        m.agent = m.initialAgent
    } else {
        m.agent = m.cfg.DefaultAgent()
    }
    // Shell agent: skip the model screen entirely — go straight to launch.
    if isShellAgent(m.agent) {
        return m.launchShell()
    }
    m.phase = phaseModel
    m.tag = m.cfg.DefaultTag
    m.current = firstModel(m.cfg, m.tag)
    return m, nil
```

Replace with:

```go
case selectedEntryMsg:
    // Resolve the agent: --agent flag wins, else the config default.
    m.selectedPath = msg.entry.Path
    if m.initialAgent != "" {
        m.agent = m.initialAgent
    } else {
        m.agent = m.cfg.DefaultAgent()
    }
    // Shell agent: skip the model screen entirely — go straight to launch.
    if isShellAgent(m.agent) {
        return m.launchShell()
    }
    m.tag = m.cfg.DefaultTag
    // Validation gate: refuse to enter the picker with an empty list.
    // This catches misconfigured catalogs (agent with no compatible models
    // in the default tag) before the user gets a confusing screen.
    models, err := m.cfg.ModelsForAgentAndTag(m.agent, m.tag)
    if err != nil {
        m.status = "config error: " + err.Error()
        return m, nil
    }
    if len(models) == 0 {
        m.status = fmt.Sprintf("no models for agent %q in tag %q — edit your config", m.agent, m.tag)
        return m, nil
    }
    m.phase = phaseModel
    m.models = buildModelList(models, m.width-2, m.height-2)
    m.modelsFor = m.agent
    m.modelsTag = m.tag
    if next, ok := rotation.ForTag(m.cfg, m.tag).Next(""); ok {
        m.current = next
        if idx := indexOfModel(models, next); idx >= 0 {
            m.models.Select(idx)
        }
    } else {
        // Fallback: list non-empty but rotation says no model. Pick index 0.
        m.current = models[0]
        m.models.Select(0)
    }
    return m, nil
```

- [ ] **Step 4: Run the new tests to verify they pass**

Run: `go test ./internal/tui -run 'TestSelectedEntryMsgEmptyList|TestPhaseModelWithList' -v`
Expected: PASS.

- [ ] **Step 5: Run the full test suite to find what else broke**

Run: `go test ./... 2>&1 | tail -40`
Expected: many failures in `internal/tui` because tests still assert the old "always enter phaseModel" and "(none) placeholder" behavior. List them, but don't fix yet — that's Tasks 8 and 9.

- [ ] **Step 6: Commit the rewrite**

```bash
git add internal/tui/app.go internal/tui/agent_model_test.go
git commit -m "feat(tui): validate and build picker list on selectedEntryMsg"
```

---

## Task 9: Update `selectedEntryMsg`-related tests in `agent_model_test.go` and `app_test.go`

**Files:**
- Modify: `internal/tui/agent_model_test.go`
- Modify: `internal/tui/app_test.go`

The previous task changed the behavior; this task updates the tests that asserted the old behavior.

- [ ] **Step 1: Rewrite `TestSelectedEntryMsgTransitionsToModelPhase` (in `app_test.go`)**

Find the test in `app_test.go`:

```go
func TestSelectedEntryMsgTransitionsToModelPhase(t *testing.T) {
    m := model{cfg: testConfig()}
    got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
    gotModel := got.(model)
    if gotModel.phase != phaseModel {
        t.Errorf("phase = %v, want phaseModel", gotModel.phase)
    }
    if gotModel.agent != "claude" {
        t.Errorf("agent = %q, want claude", gotModel.agent)
    }
    if gotModel.tag != "code" {
        t.Errorf("tag = %q, want code", gotModel.tag)
    }
    if gotModel.current.ID != "ollama/gemma4:9b" {
        t.Errorf("current = %q, want first code model", gotModel.current.ID)
    }
}
```

Replace it with:

```go
// TestSelectedEntryMsgTransitionsToModelPhase asserts that choosing a
// worktree moves the TUI into the model phase with a resolved agent,
// tag, and a populated picker list.
func TestSelectedEntryMsgTransitionsToModelPhase(t *testing.T) {
    dir := tempStateDir(t)
    seedState(t, dir, "code", "0\nollama/gemma4:9b\n")
    m := model{cfg: testConfig(), width: 80, height: 24}
    got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
    gotModel := got.(model)
    if gotModel.phase != phaseModel {
        t.Errorf("phase = %v, want phaseModel", gotModel.phase)
    }
    if gotModel.agent != "claude" {
        t.Errorf("agent = %q, want claude", gotModel.agent)
    }
    if gotModel.tag != "code" {
        t.Errorf("tag = %q, want code", gotModel.tag)
    }
    if gotModel.current.ID != "ollama/gemma4:9b" {
        t.Errorf("current = %q, want first code model (rotation index 0)", gotModel.current.ID)
    }
    if len(gotModel.models.Items()) == 0 {
        t.Error("models list is empty; expected populated picker")
    }
}
```

- [ ] **Step 2: Add the cursor-positioning test**

In `agent_model_test.go`, append:

```go
// TestSelectedEntryMsgPositionsCursorAtNextToUse asserts the cursor lands
// on the rotation's next-to-use model, not necessarily index 0.
func TestSelectedEntryMsgPositionsCursorAtNextToUse(t *testing.T) {
    dir := tempStateDir(t)
    seedState(t, dir, "code", "1\nollama/gemma4:9b\n")
    cfg := testConfig()
    m := model{cfg: cfg, width: 80, height: 24}
    got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
    gotModel := got.(model)
    if gotModel.current.ID != "ollama/gemma4:14b" {
        t.Errorf("current = %q, want ollama/gemma4:14b (rotation index 1)", gotModel.current.ID)
    }
    if gotModel.models.SelectedIndex() != 1 {
        t.Errorf("cursor index = %d, want 1", gotModel.models.SelectedIndex())
    }
}
```

- [ ] **Step 3: Run the affected tests**

Run: `go test ./internal/tui -run 'TestSelectedEntryMsg' -v`
Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/tui/agent_model_test.go internal/tui/app_test.go
git commit -m "test(tui): rewrite selectedEntryMsg tests for picker"
```

---

## Task 10: Replace `phaseModel` View with the list

**Files:**
- Modify: `internal/tui/model_list.go` (add `phaseModelView` function)
- Modify: `internal/tui/app.go` (View function)

- [ ] **Step 1: Write the failing test for the new View**

In `internal/tui/agent_model_test.go`, find the existing `TestViewModelPlaceholder` test (which asserts on the old single-line view). Replace it with:

```go
// TestViewModelPhase asserts the model-phase View renders the picker list
// with the agent and tag in the header and the keybind hints in the footer.
func TestViewModelPhase(t *testing.T) {
    dir := tempStateDir(t)
    seedState(t, dir, "code", "0\nollama/gemma4:9b\n")
    m := phaseModelWithList(t, testConfig(), "claude", "code")
    view := m.View()
    for _, want := range []string{"agent", "claude", "tag", "code", "ollama/gemma4:9b", "[r] rotate", "[d] switch tag", "[enter] launch"} {
        if !strings.Contains(view, want) {
            t.Errorf("View missing %q in:\n%s", want, view)
        }
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/tui -run TestViewModelPhase -v`
Expected: FAIL. The current `View` produces the old `agent : X\nmodel : Y` shape with the model ID, but the surrounding text and the list contents won't match.

- [ ] **Step 3: Add `phaseModelView` to `model_list.go`**

Append to `internal/tui/model_list.go`:

```go
// phaseModelView renders the model picker screen: the list of
// agent+tag-compatible models, an agent/tag header, and a footer
// describing the keybinds. The picker IS the agent+model screen —
// there is no separate browser.
func (m *model) phaseModelView() string {
    style := lipgloss.NewStyle().Padding(1, 2)
    header := fmt.Sprintf("agent : %s\ntag   : %s\n", m.agent, m.tag)
    footer := "\n[r] rotate   [d] switch tag   [enter] launch   [q] quit"
    return style.Render(header + m.models.View() + footer)
}
```

You will also need to add the `lipgloss` import to `model_list.go` if not already present. The current file has `fmt` and `strings`; add `"github.com/charmbracelet/lipgloss"`.

- [ ] **Step 4: Wire `phaseModelView` into `View`**

In `internal/tui/app.go`, find the `View` function's `phaseModel` branch:

```go
if m.phase == phaseModel {
    style := lipgloss.NewStyle().Padding(2, 2)
    return style.Render(
        fmt.Sprintf("agent : %s\nmodel : %s\n\ntag : %s\n\n"+
            "[r] rotate   [m] browse models   [enter] launch   [q] quit",
            m.agent, m.current.ID, m.tag))
}
```

Replace with:

```go
if m.phase == phaseModel {
    if m.width <= 0 || m.height <= 0 {
        return "model picker (waiting for window size)"
    }
    return m.phaseModelView()
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/tui -run TestViewModelPhase -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/app.go internal/tui/model_list.go internal/tui/agent_model_test.go
git commit -m "feat(tui): render model phase as a picker list"
```

---

## Task 11: Wire `r` and `d` keypress handlers to the new state

**Files:**
- Modify: `internal/tui/app.go` (`Update` function, `r` and `d` cases)

- [ ] **Step 1: Write the failing test for the `r` cursor update**

Append to `internal/tui/agent_model_test.go`:

```go
// TestModelScreenRotateMovesCursor asserts pressing 'r' updates both
// m.current (the rotation cursor) and m.models.SelectedIndex() (the
// visible list cursor). They are the same cursor, just two views of it.
func TestModelScreenRotateMovesCursor(t *testing.T) {
    dir := tempStateDir(t)
    seedState(t, dir, "code", "0\nollama/gemma4:9b\n")
    m := phaseModelWithList(t, testConfig(), "claude", "code")
    if m.current.ID != "ollama/gemma4:9b" {
        t.Fatalf("precondition: current = %q, want ollama/gemma4:9b", m.current.ID)
    }
    got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
    gotModel := got.(model)
    if gotModel.current.ID != "ollama/gemma4:14b" {
        t.Errorf("current = %q, want ollama/gemma4:14b (rotated)", gotModel.current.ID)
    }
    if gotModel.models.SelectedIndex() != 1 {
        t.Errorf("cursor index = %d, want 1", gotModel.models.SelectedIndex())
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/tui -run TestModelScreenRotateMovesCursor -v`
Expected: FAIL. The current `r` handler updates `m.current` but not the list cursor.

- [ ] **Step 3: Rewrite the `r` keypress handler**

In `internal/tui/app.go`, find:

```go
case "r":
    if m.phase == phaseModel {
        // Rotate to the next model in the active tag group, skipping
        // whatever the other tag group last used (cross-tag skip).
        rot := rotation.ForTag(m.cfg, m.tag)
        next, ok := rot.Next(m.otherTag)
        if ok {
            m.current = next
        }
    }
```

Replace with:

```go
case "r":
    if m.phase == phaseModel {
        // Advance the rotation cursor; the visible list cursor follows.
        next, ok := rotation.ForTag(m.cfg, m.tag).Next(oppositeTag(m.tag))
        if ok {
            m.current = next
            if items := m.models.Items(); len(items) > 0 {
                for i, it := range items {
                    if mi, ok := it.(modelItem); ok && mi.model.ID == next.ID {
                        m.models.Select(i)
                        break
                    }
                }
            }
        }
    }
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/tui -run TestModelScreenRotateMovesCursor -v`
Expected: PASS.

- [ ] **Step 5: Write the failing test for `d` rebuild**

Append to `internal/tui/agent_model_test.go`:

```go
// TestModelScreenToggleTagRebuildsList asserts pressing 'd' rebuilds
// m.models from the new tag's models and positions the cursor on the
// new rotation index.
func TestModelScreenToggleTagRebuildsList(t *testing.T) {
    dir := tempStateDir(t)
    seedState(t, dir, "design", "0\nollama/gemma4:design\n")
    m := phaseModelWithList(t, testConfig(), "claude", "code")
    if m.tag != "code" {
        t.Fatalf("precondition: tag = %q, want code", m.tag)
    }
    got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
    gotModel := got.(model)
    if gotModel.tag != "design" {
        t.Errorf("tag = %q, want design", gotModel.tag)
    }
    if len(gotModel.models.Items()) != 1 {
        t.Errorf("models items = %d, want 1 (only design model)", len(gotModel.models.Items()))
    }
    if gotModel.current.ID != "ollama/gemma4:design" {
        t.Errorf("current = %q, want ollama/gemma4:design", gotModel.current.ID)
    }
}
```

- [ ] **Step 6: Run the test to verify it fails**

Run: `go test ./internal/tui -run TestModelScreenToggleTagRebuildsList -v`
Expected: FAIL. The current `d` handler doesn't rebuild the list.

- [ ] **Step 7: Rewrite the `d` keypress handler**

In `internal/tui/app.go`, find:

```go
case "d":
    if m.phase == phaseModel {
        // Toggle the active tag group between code and design.
        if m.tag == "code" {
            m.tag, m.otherTag = "design", "code"
        } else {
            m.tag, m.otherTag = "code", "design"
        }
        // Re-resolve the shown model to the new group's first entry.
        m.current = firstModel(m.cfg, m.tag)
    }
```

Replace with:

```go
case "d":
    if m.phase == phaseModel {
        prevTag := m.tag
        m.tag = oppositeTag(m.tag)
        // Empty-tag defense: if the new tag has no models, restore.
        models, err := m.cfg.ModelsForAgentAndTag(m.agent, m.tag)
        if err != nil || len(models) == 0 {
            m.tag = prevTag
            m.status = fmt.Sprintf("tag %q has no models for agent %q", m.tag, m.agent)
            return m, nil
        }
        // Rebuild the list and reposition the cursor on the new tag's
        // rotation index. Cross-skip avoids the previous tag's last-used.
        m.models = buildModelList(models, m.width-2, m.height-2)
        m.modelsTag = m.tag
        if next, ok := rotation.ForTag(m.cfg, m.tag).Next(prevTag); ok {
            m.current = next
            if idx := indexOfModel(models, next); idx >= 0 {
                m.models.Select(idx)
            }
        } else {
            m.current = models[0]
            m.models.Select(0)
        }
    }
```

- [ ] **Step 8: Run the test to verify it passes**

Run: `go test ./internal/tui -run TestModelScreenToggleTagRebuildsList -v`
Expected: PASS.

- [ ] **Step 9: Add the empty-tag defense test**

Append to `internal/tui/agent_model_test.go`:

```go
// TestModelScreenToggleTagEmptyRestores asserts that toggling to a tag
// with no models reverts to the previous tag and surfaces a status
// message rather than leaving the user in a void.
func TestModelScreenToggleTagEmptyRestores(t *testing.T) {
    cfg := &config.Config{
        DefaultTag: "code",
        Providers:  []config.Provider{{ID: "ollama"}},
        Models: []config.Model{
            // Only code models, no design.
            {ID: "ollama/code-only", ProviderID: "ollama", Tags: []string{"code"}},
        },
        Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
    }
    m := phaseModelWithList(t, cfg, "claude", "code")
    got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
    gotModel := got.(model)
    if gotModel.tag != "code" {
        t.Errorf("tag = %q, want code (restored)", gotModel.tag)
    }
    if gotModel.status == "" {
        t.Error("status = empty, want error message about empty design tag")
    }
}
```

- [ ] **Step 10: Run all the new picker tests**

Run: `go test ./internal/tui -run 'TestModelScreen' -v`
Expected: all PASS.

- [ ] **Step 11: Commit**

```bash
git add internal/tui/app.go internal/tui/agent_model_test.go
git commit -m "feat(tui): wire rotation and tag toggle to picker"
```

---

## Task 12: Update Enter handler to use the highlighted item

**Files:**
- Modify: `internal/tui/app.go` (`Update` function, `enter` case in `phaseModel`)

- [ ] **Step 1: Write the failing test for "highlighted wins"**

Append to `internal/tui/agent_model_test.go`:

```go
// TestModelScreenEnterUsesHighlightedNotCurrent asserts that Enter launches
// the highlighted list item, even if m.current lags. This protects against
// a class of bugs where stale state could launch the wrong model after
// the user has navigated the cursor.
func TestModelScreenEnterUsesHighlightedNotCurrent(t *testing.T) {
    dir := tempStateDir(t)
    seedState(t, dir, "code", "0\nollama/gemma4:9b\n")
    m := phaseModelWithList(t, testConfig(), "claude", "code")
    // Move the list cursor to index 0 (gemma4:9b) without rotating.
    m.models.Select(0)
    // Stale m.current points at the second model.
    m.current = config.Model{ID: "stale", ProviderID: "ollama"}
    got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
    gotModel := got.(model)
    if gotModel.current.ID != "ollama/gemma4:9b" {
        t.Errorf("current after Enter = %q, want ollama/gemma4:9b (highlighted wins)", gotModel.current.ID)
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/tui -run TestModelScreenEnterUsesHighlightedNotCurrent -v`
Expected: FAIL. The current Enter handler uses `m.current` directly.

- [ ] **Step 3: Rewrite the `enter` handler in `phaseModel`**

In `internal/tui/app.go`, find:

```go
case phaseModel:
    // Check ollama availability before launching.
    if ollamacheck.IsOllamaModel(m.current) {
        ok, err := ollamacheck.Available(m.current.ModelName)
        if err != nil {
            m.status = "ollama check failed: " + err.Error()
            return m, nil
        }
        if !ok {
            m.ollamaWarnModel = list.New(buildOllamaChoices(), list.NewDefaultDelegate(), m.width-2, m.height-2)
            m.ollamaWarnModel.Title = "Model not available: " + m.current.ModelName
            m.phase = phaseOllamaWarn
            return m, nil
        }
    }

    return m.proceedToLaunch()
```

Replace with:

```go
case phaseModel:
    // The highlighted list item is what gets launched. Sync m.current
    // so subsequent code paths (ollama check, session, launch) all see
    // the user's choice.
    if item, ok := m.models.SelectedItem().(modelItem); ok {
        m.current = item.model
    }
    // Check ollama availability before launching.
    if ollamacheck.IsOllamaModel(m.current) {
        ok, err := ollamacheck.Available(m.current.ModelName)
        if err != nil {
            m.status = "ollama check failed: " + err.Error()
            return m, nil
        }
        if !ok {
            m.ollamaWarnModel = list.New(buildOllamaChoices(), list.NewDefaultDelegate(), m.width-2, m.height-2)
            m.ollamaWarnModel.Title = "Model not available: " + m.current.ModelName
            m.phase = phaseOllamaWarn
            return m, nil
        }
    }
    return m.proceedToLaunch()
```

- [ ] **Step 4: Run the new test and the existing launch tests**

Run: `go test ./internal/tui -run 'TestModelScreenEnter|TestEnterInModelPhase' -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/app.go internal/tui/agent_model_test.go
git commit -m "feat(tui): launch highlighted list item on Enter"
```

---

## Task 13: Window resize for the picker list

**Files:**
- Modify: `internal/tui/app.go` (`tea.WindowSizeMsg` case)

- [ ] **Step 1: Update the `WindowSizeMsg` handler**

In `internal/tui/app.go`, find:

```go
case tea.WindowSizeMsg:
    m.width, m.height = msg.Width, msg.Height
    if m.ready {
        m.list.SetSize(msg.Width-2, msg.Height-2)
    }
    if m.phase == phaseBrowser {
        m.refreshBrowser()
    }
```

Replace the `phaseBrowser` branch (and add the picker resize):

```go
case tea.WindowSizeMsg:
    m.width, m.height = msg.Width, msg.Height
    if m.ready {
        m.list.SetSize(msg.Width-2, msg.Height-2)
    }
    if m.phase == phaseModel {
        m.models.SetSize(msg.Width-2, msg.Height-2)
    }
```

(The `phaseBrowser` block is removed in Task 13.)

- [ ] **Step 2: Run all tests**

Run: `go test ./...`
Expected: all PASS (no behavior change yet, just a wiring update).

- [ ] **Step 3: Commit**

```bash
git add internal/tui/app.go
git commit -m "feat(tui): resize picker list on window resize"
```

---

## Task 14: Drop `phaseBrowser`, the `m` key, and the browser fields

**Files:**
- Modify: `internal/tui/app.go`

This task is the cleanup. After this, the TUI no longer has a model browser.

- [ ] **Step 1: Remove `phaseBrowser` from the phase enum**

In `internal/tui/app.go`, find:

```go
const (
    phaseList        phase = iota // worktree list (lesson 13)
    phaseModel                    // agent+model screen (lesson 14)
    phaseBrowser                  // model browser (lesson 15)
    phaseResume                   // resume prompt (lesson 16)
    phaseGuardWarn                // confirm before launching on default branch
    phaseOllamaWarn               // confirm before launching with unavailable ollama model
    phaseNewWorktree              // create-new-worktree prompt
)
```

Replace with:

```go
const (
    phaseList        phase = iota // worktree list (lesson 13)
    phaseModel                    // agent+model picker (lesson 14 + lesson 15 merged)
    phaseResume                   // resume prompt (lesson 16)
    phaseGuardWarn                // confirm before launching on default branch
    phaseOllamaWarn               // confirm before launching with unavailable ollama model
    phaseNewWorktree              // create-new-worktree prompt
)
```

- [ ] **Step 2: Remove the browser-related fields from the `model` struct**

In `internal/tui/app.go`, find the model-browser block:

```go
// model browser (lesson 15)
browser      list.Model     // browser list widget
browserCache []config.Model // snapshot of registry.Discover, per browser-open
browserTag   string         // "" = all models; otherwise a tag like "code"
sourceCycle  int            // 0=all, 1=curated, 2=discovered
```

Delete it.

- [ ] **Step 3: Remove the `m` keypress handler**

In `internal/tui/app.go`, find:

```go
case "m":
    if m.phase == phaseModel {
        // Open the model browser. Reset the cache so each open
        // re-discovers; filter toggles inside the browser reuse it.
        m.phase = phaseBrowser
        m.browserCache = nil
        m.refreshBrowser()
    }
```

Delete it.

- [ ] **Step 4: Remove the `esc` branch for `phaseBrowser`**

Find:

```go
case "esc":
    // esc is phase-aware: pop back from a nested screen, else quit.
    if m.phase == phaseBrowser {
        m.phase = phaseModel
        return m, nil
    }
```

Delete the `phaseBrowser` branch (the rest of the `esc` handler stays).

- [ ] **Step 5: Remove the `enter` branch for `phaseBrowser`**

Find:

```go
case phaseBrowser:
    if item, ok := m.browser.SelectedItem().(modelItem); ok {
        m.current = item.model
        m.phase = phaseModel
    }
```

Delete it.

- [ ] **Step 6: Remove the `f` and `c` keypress handlers**

These were browser filter toggles. Find and delete:

```go
case "f":
    if m.phase == phaseBrowser {
        ...
    }
case "c":
    if m.phase == phaseBrowser {
        ...
    }
```

- [ ] **Step 7: Remove the browser update path at the bottom of `Update`**

Find:

```go
if m.phase == phaseBrowser && m.width > 0 && m.height > 0 {
    var cmd tea.Cmd
    m.browser, cmd = m.browser.Update(msg)
    return m, cmd
}
```

Delete it.

- [ ] **Step 8: Remove the `phaseBrowser` View branch**

Find:

```go
if m.phase == phaseBrowser {
    return m.browserView()
}
```

Delete it.

- [ ] **Step 9: Try to build**

Run: `go build ./...`
Expected: success.

- [ ] **Step 10: Commit**

```bash
git add internal/tui/app.go
git commit -m "refactor(tui): drop model browser screen and 'm' key"
```

---

## Task 15: Delete `model_browser.go` and `model_browser_test.go`

**Files:**
- Delete: `internal/tui/model_browser.go`
- Delete: `internal/tui/model_browser_test.go`

- [ ] **Step 1: Delete the files**

```bash
git rm internal/tui/model_browser.go internal/tui/model_browser_test.go
```

- [ ] **Step 2: Verify the build and tests still pass**

Run: `go test ./...`
Expected: PASS. If anything still references the old `modelItem` or `refreshBrowser`, the build will fail and the references need to be updated to the new `model_list.go` types.

- [ ] **Step 3: Commit**

```bash
git commit -m "refactor(tui): delete model_browser files"
```

(If `git rm` did the commit in step 1, this is a no-op; verify with `git status`.)

---

## Task 16: Sweep stale tests in `agent_model_test.go` and `app_test.go`

**Files:**
- Modify: `internal/tui/agent_model_test.go`
- Modify: `internal/tui/app_test.go`

Several tests still assert on the old `(none)` placeholder or the old `m.otherTag` field. This task rewrites them.

- [ ] **Step 1: Run the full test suite to enumerate failures**

Run: `go test ./... 2>&1 | tee /tmp/test_output.txt`
Expected: a list of failing tests, mostly in `agent_model_test.go` and `app_test.go`.

- [ ] **Step 2: Rewrite `TestFirstModelPlaceholder`**

In `agent_model_test.go`, find the test that checks the `(none)` placeholder. Replace it with a test that asserts `ModelsForAgentAndTag` returns an empty list for an empty config (the placeholder is no longer a runtime concept):

```go
// TestFirstModelPlaceholderRemoved asserts the legacy (none) placeholder
// is gone. Validation now happens at the phaseList → phaseModel
// transition, not in a placeholder model. This test pins the helper
// behavior so a future refactor can't reintroduce the placeholder.
func TestFirstModelPlaceholderRemoved(t *testing.T) {
    cfg := &config.Config{DefaultTag: "code"}
    got, err := cfg.ModelsForAgentAndTag("claude", "code")
    if err != nil {
        t.Fatalf("ModelsForAgentAndTag: %v", err)
    }
    if len(got) != 0 {
        t.Errorf("len = %d, want 0 (validation gate, not placeholder)", len(got))
    }
}
```

- [ ] **Step 3: Rewrite `TestFirstModelPicksFirstInTag`**

Find:

```go
func TestFirstModelPicksFirstInTag(t *testing.T) {
    ...
    if got := firstModel(cfg, "code"); got.ID != "code-first" {
        ...
    }
}
```

Replace with a test on `ModelsForAgentAndTag` (the helper that replaces `firstModel` for picker purposes):

```go
// TestModelsForAgentAndTagReturnsFirstInTag asserts ModelsForAgentAndTag
// preserves config-order, so the picker can use index 0 as the
// "first model in this group" without further sorting.
func TestModelsForAgentAndTagReturnsFirstInTag(t *testing.T) {
    cfg := &config.Config{
        Providers: []config.Provider{{ID: "ollama"}},
        Models: []config.Model{
            {ID: "design-first", ProviderID: "ollama", Tags: []string{"design"}},
            {ID: "code-first", ProviderID: "ollama", Tags: []string{"code"}},
        },
        Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
    }
    got, err := cfg.ModelsForAgentAndTag("claude", "code")
    if err != nil {
        t.Fatalf("ModelsForAgentAndTag: %v", err)
    }
    if len(got) != 1 || got[0].ID != "code-first" {
        t.Errorf("got = %+v, want [code-first]", got)
    }
}
```

- [ ] **Step 4: Rewrite `TestToggleBackToCode` in `app_test.go`**

Find the test (in `app_test.go` per grep earlier):

```go
func TestToggleBackToCode(t *testing.T) {
    m := model{cfg: testConfig(), phase: phaseModel, tag: "code", otherTag: "",
        current: config.Model{ID: "ollama/gemma4:9b"}}
    got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
    got, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
    gotModel := got.(model)
    if gotModel.tag != "code" || gotModel.otherTag != "design" {
        t.Errorf("tag/otherTag = (%q, %q), want (code, design)", gotModel.tag, gotModel.otherTag)
    }
    ...
}
```

Replace with the picker-based version. The otherTag field is gone; we only assert `m.tag`:

```go
// TestToggleBackToCode asserts pressing 'd' twice returns to the code
// group. Toggling is a stable two-way switch, not a one-way trip.
// (otherTag is now computed via oppositeTag(m.tag), not stored on model.)
func TestToggleBackToCode(t *testing.T) {
    dir := tempStateDir(t)
    seedState(t, dir, "code", "0\nollama/gemma4:9b\n")
    m := phaseModelWithList(t, testConfig(), "claude", "code")
    got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
    got, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
    gotModel := got.(model)
    if gotModel.tag != "code" {
        t.Errorf("tag = %q, want code (after two toggles)", gotModel.tag)
    }
}
```

- [ ] **Step 5: Rewrite `TestToggleTagSwitchesGroup` in `app_test.go`**

Find the test (in `app_test.go` per grep earlier). Replace with the picker-based version. The otherTag assertion is dropped:

```go
// TestToggleTagSwitchesGroup asserts pressing 'd' in the model phase flips
// the active tag group (code <-> design) and rebuilds the picker from
// the new group's models. This powers cross-tag rotation from a single
// keystroke.
func TestToggleTagSwitchesGroup(t *testing.T) {
    dir := tempStateDir(t)
    seedState(t, dir, "design", "0\nollama/gemma4:design\n")
    m := phaseModelWithList(t, testConfig(), "claude", "code")
    got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
    gotModel := got.(model)
    if gotModel.tag != "design" {
        t.Errorf("tag = %q, want design", gotModel.tag)
    }
    if gotModel.current.ID != "ollama/gemma4:design" {
        t.Errorf("current = %q, want ollama/gemma4:design", gotModel.current.ID)
    }
}
```

- [ ] **Step 6: Rewrite `TestViewModelPhase` in `app_test.go`**

The new View shape is asserted in `agent_model_test.go`'s `TestViewModelPhase` (Task 9). Delete the old one in `app_test.go` (or repurpose it). Find:

```go
func TestViewModelPhase(t *testing.T) {
    m := model{phase: phaseModel, agent: "claude", tag: "code",
        current: config.Model{ID: "ollama/gemma4:9b"}}
    view := m.View()
    for _, want := range []string{"agent", "claude", "model", "ollama/gemma4:9b", "[r] rotate"} {
        ...
    }
}
```

Delete it (the equivalent test now lives in `agent_model_test.go` and uses the picker).

- [ ] **Step 7: Update `TestModelAndTagKeysIgnoredInListPhase`**

Find:

```go
func TestModelAndTagKeysIgnoredInListPhase(t *testing.T) {
    for _, key := range []rune{'m', 'd'} {
        ...
    }
}
```

Drop `'m'` from the loop (the `m` key no longer exists). The test now only checks `'d'`:

```go
// TestTagKeyIgnoredInListPhase asserts 'd' does nothing while still on
// the worktree list. The model-screen keybind must not fire before a
// worktree is chosen. (The 'm' key was removed when the browser was
// deleted; this test now covers only 'd'.)
func TestTagKeyIgnoredInListPhase(t *testing.T) {
    m := model{cfg: testConfig(), phase: phaseList, tag: "code",
        current: config.Model{ID: "ollama/gemma4:9b"}}
    got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
    gotModel := got.(model)
    if gotModel.status != "" {
        t.Errorf("status mutated in list phase: %q", gotModel.status)
    }
    if gotModel.tag != "code" {
        t.Errorf("tag mutated in list phase: %q", gotModel.tag)
    }
    if gotModel.current.ID != "ollama/gemma4:9b" {
        t.Errorf("current mutated in list phase: %q", gotModel.current.ID)
    }
}
```

- [ ] **Step 8: Update the `TestOllamaWarn*` tests in `app_test.go`**

These tests construct `model` literals with `phase: phaseModel` and `current: cfg.Models[0]`. They now need a populated `m.models` so the Enter handler can read the highlighted item. Find the two tests and update them to use `phaseModelWithList`:

```go
// TestOllamaWarnShownWhenUnavailable (existing, updated)
func TestOllamaWarnShownWhenUnavailable(t *testing.T) {
    cfg := &config.Config{
        DefaultTag: "code",
        Providers:  []config.Provider{{ID: "ollama"}},
        Models: []config.Model{
            {ID: "ollama/test-model-xyz-not-real", ProviderID: "ollama", ModelName: "test-model-xyz-not-real", Tags: []string{"code"}},
        },
        Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
    }
    m := phaseModelWithList(t, cfg, "claude", "code")
    m.selectedPath = "/repo"
    newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
    mm := newM.(model)
    if mm.phase != phaseOllamaWarn {
        t.Fatalf("expected phaseOllamaWarn, got %v", mm.phase)
    }
}
```

(Apply the same `phaseModelWithList` rewrite to the cancel-and-return test.)

- [ ] **Step 9: Run the full test suite**

Run: `go test ./... 2>&1 | tail -30`
Expected: all PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/tui/agent_model_test.go internal/tui/app_test.go
git commit -m "test(tui): rewrite stale tests for picker state shape"
```

---

## Task 17: Remove `firstModel` if unused, and clean up

**Files:**
- Modify: `internal/tui/app.go`

The `firstModel` helper is no longer used after Task 8's `selectedEntryMsg` rewrite. But it may be called from a test (it's package-private). Verify and remove.

- [ ] **Step 1: Search for any remaining `firstModel` references**

Run: `grep -rn 'firstModel' /Users/keith/github/ohanaverse/agent-worktree/internal/`
Expected: only the helper definition itself. If any test or production code still calls it, rewrite those call sites to use `ModelsForAgentAndTag` (and pick index 0) instead.

- [ ] **Step 2: Remove the `firstModel` helper if no callers remain**

In `internal/tui/app.go`, find and delete:

```go
// firstModel returns the first model in a tag group, or a "(none)" placeholder.
func firstModel(cfg *config.Config, tag string) config.Model {
    ...
}
```

- [ ] **Step 3: Run the full test suite**

Run: `go test ./...`
Expected: all PASS.

- [ ] **Step 4: Commit (or amend)**

```bash
git add internal/tui/app.go
git commit -m "refactor(tui): remove unused firstModel helper"
```

If this is the only change in the commit, that's fine. If other small cleanups were needed in step 1, commit them all together.

---

## Task 18: Add the `m` key removal test (the `m` key was dropped in Task 14; this task pins the removal with a positive test)

**Files:**
- Modify: `internal/tui/agent_model_test.go`

The `m` key was removed. A test that asserts "pressing `m` on the model screen does not crash and does not open a browser" pins the removal.

- [ ] **Step 1: Write the test**

Append to `internal/tui/agent_model_test.go`:

```go
// TestModelScreenMKeyIsNoOp asserts that pressing 'm' on the model
// screen does not open a browser (there is no browser anymore) and
// does not mutate any state. This pins the removal: if a future
// change re-introduces 'm' as a keybind, this test will fail.
func TestModelScreenMKeyIsNoOp(t *testing.T) {
    dir := tempStateDir(t)
    seedState(t, dir, "code", "0\nollama/gemma4:9b\n")
    m := phaseModelWithList(t, testConfig(), "claude", "code")
    before := m
    got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
    gotModel := got.(model)
    if gotModel.phase != before.phase {
        t.Errorf("phase changed: before %v, after %v", before.phase, gotModel.phase)
    }
    if gotModel.current.ID != before.current.ID {
        t.Errorf("current changed: before %q, after %q", before.current.ID, gotModel.current.ID)
    }
    if gotModel.tag != before.tag {
        t.Errorf("tag changed: before %q, after %q", before.tag, gotModel.tag)
    }
    if len(gotModel.models.Items()) != len(before.models.Items()) {
        t.Errorf("models items changed: before %d, after %d", len(before.models.Items()), len(gotModel.models.Items()))
    }
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/tui -run TestModelScreenMKeyIsNoOp -v`
Expected: PASS (the production code has no `m` case in `phaseModel`, so the handler is a no-op).

- [ ] **Step 3: Commit**

```bash
git add internal/tui/agent_model_test.go
git commit -m "test(tui): pin removal of m key on model screen"
```

---

## Task 19: Final verification — `go test`, `go vet`, and a build

**Files:** none modified

- [ ] **Step 1: Run `go vet`**

Run: `go vet ./...`
Expected: clean output.

- [ ] **Step 2: Run the full test suite**

Run: `go test ./... 2>&1 | tail -30`
Expected: all PASS. Compare test counts to the spec's expectation (122 baseline ± new tests).

- [ ] **Step 3: Build the binary**

Run: `go build -o /tmp/wt-check ./cmd/wt`
Expected: clean build. (We don't install it; the check is just that the build succeeds.)

- [ ] **Step 4: Spot-check the binary's --help output**

Run: `/tmp/wt-check --help 2>&1 | head -20`
Expected: same as before (no CLI flag changes). The visible changes are: (1) the `m` key in the TUI is gone, (2) the model screen renders a list, (3) the list is sourced from config.toml only.

- [ ] **Step 5: Clean up the spot-check binary**

```bash
rm /tmp/wt-check
```

- [ ] **Step 6: Final commit if any local changes**

```bash
git status
```

Expected: clean working tree.

- [ ] **Step 7: Push the branch**

```bash
git push -u origin feat/list-based-model-selection
```

Expected: branch pushed. (The branch is ready for review/merge via PR; the main-branch guard is still in place so this isn't merged automatically.)

---

## Self-Review

**1. Spec coverage:**

- § Goals: "Render the agent+model screen as a `bubble/list`…" — Task 10.
- § Goals: "Filter the list by agent via `agent.SupportedProviders`" — Tasks 2, 3 (helpers) and Task 8 (entry validation).
- § Goals: "Source the list from `config.toml` only" — Task 14 (browser/discovery removed), Task 15 (files deleted).
- § Goals: "Keep per-tag rotation state" — Task 11 (rotation still drives `r` and the cold-start cursor).
- § Goals: "Eliminate the separate browser screen and the `m` key" — Tasks 14, 15, 18.
- § Goals: "Validate at the `phaseList → phaseModel` boundary" — Task 8.
- § Architecture: `ModelsForAgent`/`ModelsForAgentAndTag` helpers — Tasks 2, 3.
- § Architecture: `oppositeTag` helper — Task 4.
- § Architecture: `modelItem` extraction — Task 5.
- § Architecture: picker fields on `model` — Task 6.
- § Architecture: phase enum update (no `phaseBrowser`) — Task 14.
- § Behavior: cold start flow — Tasks 7, 8, 9.
- § Behavior: rotation flow — Task 11.
- § Behavior: tag switch flow + empty-tag defense — Task 11.
- § Behavior: launch flow (highlighted wins) — Task 12.
- § Window resize — Task 13.
- § Quit keys — unchanged (no task needed; existing tests still pass).
- § Error handling — covered in Task 8 (empty-list status), Task 11 (empty-tag status).
- § Tests Bucket A (keep) — Tasks 9, 16.
- § Tests Bucket B (rewrite) — Tasks 9, 16.
- § Tests Bucket C (new) — Tasks 7, 8, 9, 11, 12, 18.
- § Migration / cleanup — Tasks 14, 15, 17.

**2. Placeholder scan:** No TBDs, no "implement later", no vague "add appropriate error handling". Every code block is concrete.

**3. Type consistency:**
- `modelItem` defined in Task 5 (`model_list.go`), used in Tasks 7, 10, 11, 12. ✓
- `buildModelList` defined in Task 5, used in Tasks 8, 11. ✓
- `oppositeTag` defined in Task 4, used in Tasks 11 (twice). ✓
- `indexOfModel` defined in Task 7, used in Tasks 8, 11. ✓
- `phaseModelWithList` defined in Task 7, used in Tasks 9, 11, 12, 16, 18. ✓
- `m.models`, `m.modelsFor`, `m.modelsTag` defined in Task 6, used in Tasks 7, 8, 9, 10, 11, 12, 13, 14, 16. ✓

**4. Scope:** One focused subsystem. Single plan. ✓

**5. Plan length:** 19 tasks, each ≤ 30 minutes. Reasonable for a non-trivial TUI refactor.
