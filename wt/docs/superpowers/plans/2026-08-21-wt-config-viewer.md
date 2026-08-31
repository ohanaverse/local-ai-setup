# `wt config` Viewer-and-Editor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `wt models` and `wt agents` with a single `wt config` (no subcommand) TUI that views and edits the agents/providers/models sections of `config.toml`.

**Architecture:** New `internal/configeditor/` package (parallel to `internal/ollamaconfig/`). Bubble-tea program with three tabs (Agents/Providers/Models), per-row edit/add/delete forms, FK validation, atomic save via existing `config.Save()`. Reuses `tui.ThemedListDelegate` and `tui.ErrorStyle`. `modelsCmd` and `agentsCmd` removed from `cmd/wt/commands.go`; main.go drops them from root registration.

**Tech Stack:** Go 1.26.3, bubble-tea v0.x (`bubbles/list`, `bubbles/textinput`), `lipgloss`, existing `internal/config` and `internal/agents` packages.

**Spec:** `docs/superpowers/specs/2026-08-21-wt-config-viewer-design.md`

---

## File structure

| Path | Responsibility |
|---|---|
| `internal/configeditor/editor.go` | `Run(theme)` entry, top-level model struct, `Init`/`Update`/`View`, phase enum |
| `internal/configeditor/tabs.go` | Tab state + key handling (`Tab`/`Shift-Tab`/1/2/3) |
| `internal/configeditor/agents_tab.go` | Agents list rendering, sort (commands-first via `agents.IsCommand`) |
| `internal/configeditor/providers_tab.go` | Providers list rendering, sort (by ID) |
| `internal/configeditor/models_tab.go` | Models list rendering, sort (provider/family/name) |
| `internal/configeditor/provider_form.go` | Provider add/edit form |
| `internal/configeditor/model_form.go` | Model add/edit form |
| `internal/configeditor/agent_form.go` | Agent add/edit form |
| `internal/configeditor/delete.go` | Delete confirmation + FK check |
| `internal/configeditor/save.go` | Validate + atomic save + dirty tracking |
| `internal/configeditor/editor_test.go` | Run, tab nav, quit/dirty prompts |
| `internal/configeditor/agents_tab_test.go` | Sort, installed annotation |
| `internal/configeditor/providers_tab_test.go` | Sort |
| `internal/configeditor/models_tab_test.go` | Sort |
| `internal/configeditor/provider_form_test.go` | Add/edit/validation |
| `internal/configeditor/model_form_test.go` | Add/edit/FK picker |
| `internal/configeditor/agent_form_test.go` | Add/edit/multi-select/installed read-only |
| `internal/configeditor/delete_test.go` | FK block, unblocked deletes, cancel |
| `internal/configeditor/save_test.go` | Atomic write, validate-gates-save, dirty |
| `cmd/wt/commands.go` | Remove `modelsCmd`, `agentsCmd`; keep `rotateCmd` |
| `cmd/wt/main.go` | Drop `modelsCmd(a), agentsCmd(a)` from `AddCommand` (line 257) |
| `cmd/wt/commands_config.go` | `configCmd.RunE` now opens the TUI; updated `Long` |
| `cmd/wt/main_test.go` | New test: `wt config` (no subcommand) launches the TUI |
| `docs/wt-config.md` | Add "viewer" section + "deletion rules" subsection |
| `CLAUDE.md` | Update test-coverage table, drop `wt models`/`wt agents` references |

---

## Phases and shared types

```go
// internal/configeditor/editor.go
type phase int
const (
    phaseList    phase = iota // active tab's list view
    phaseForm                 // per-row edit/add form
    phaseDelete               // delete confirmation prompt
    phaseQuit                 // unsaved-changes quit prompt
)

type section int
const (
    sectionAgents section = iota
    sectionProviders
    sectionModels
)

type model struct {
    theme  themes.Theme
    cfg    *config.Config    // in-memory copy, mutated by edits
    dirty  bool
    width, height int
    ready  bool

    section section
    lists   [3]list.Model    // agents, providers, models — ThemedListDelegate

    // form state
    formPhase  formKind      // none | providerForm | modelForm | agentForm
    formIsNew  bool
    formRow    formRow       // interface{Apply(*config.Config)} etc.
    formError  string
    formCursor int

    // delete state
    deleteTarget deleteTarget // section + index
    deleteError  string

    // quit state
    quitPrompt bool
}
```

```go
// internal/configeditor/save.go
type saveMsg struct{ err error }

var saveCmd = func(cfg *config.Config) tea.Cmd {
    return func() tea.Msg { return saveMsg{err: config.Save(cfg)} }
}
```

```go
// internal/configeditor/delete.go
type deleteTarget struct {
    section section
    index   int  // index into cfg.Agents / cfg.Providers / cfg.Models
}
```

Test seam (mirrors `internal/themes`):

```go
// internal/configeditor/editor.go
var configDirFunc = config.Dir // tests override via SetConfigDirForTest
func SetConfigDirForTest(f func() string) (restore func()) { ... }
```

---

## Tasks

Each task is self-contained: write failing test → run → implement → run → commit. Tests use `t.TempDir()` + the `SetConfigDirForTest` seam, plus in-memory `*config.Config` construction (no on-disk fixtures).

### Task 1: Package skeleton + phase enum

**Files:**
- Create: `internal/configeditor/editor.go`
- Test: `internal/configeditor/editor_test.go`

- [ ] Test `TestRun_EmptyConfig_Launches` — build empty `*config.Config`, call `Run(themes.Get("default"))` in a goroutine that quits on first `tea.WindowSizeMsg`. Assert no panic.
- [ ] Run `go test ./internal/configeditor -run TestRun -v` → FAIL (package doesn't exist).
- [ ] Implement `editor.go` with `Run(theme) error`, `phase`/`section` enums, empty `model` struct, `Init` returning `tea.Quit` (test-only stub).
- [ ] Run `go test ./internal/configeditor -v` → PASS.
- [ ] Commit `feat(configeditor): scaffold package with phase/section types`.

### Task 2: Tab nav

**Files:**
- Modify: `internal/configeditor/editor.go`
- Test: `internal/configeditor/editor_test.go`

- [ ] Test `TestTab_Cycles` — send `Tab` key, assert `m.section == sectionProviders`; `Shift-Tab` from `sectionProviders` returns to `sectionAgents`.
- [ ] Test `TestTab_NumberKeys_Jump` — `1`/`2`/`3` jump to each section.
- [ ] Run → FAIL.
- [ ] Implement `Update` `tea.KeyMsg` handling for `tab`/`shift+tab`/`1`/`2`/`3`.
- [ ] Run → PASS.
- [ ] Commit `feat(configeditor): tab navigation`.

### Task 3: Agents tab — list rendering + sort

**Files:**
- Create: `internal/configeditor/agents_tab.go`
- Test: `internal/configeditor/agents_tab_test.go`

- [ ] Test `TestAgentsTab_Sort_CommandsFirst` — cfg with agents `[claude, shell, agy]` + `agents.RegisterTest` to mark `shell` as `IsCommand()`. Assert order is `[shell, agy, claude]`.
- [ ] Test `TestAgentsTab_InstalledAnnotation` — agent `agy` row renders `✓ installed` when `agents.Installed("agy")` is true; `✗ not installed` when false.
- [ ] Run → FAIL.
- [ ] Implement `agents_tab.go`: `agentItem` adapts `config.Agent` to `list.Item` with `Title()` returning `name + supported_providers + installed annotation`; `buildAgentsList(theme, w, h, cfg)` sorts commands-first then by name.
- [ ] Run → PASS.
- [ ] Commit `feat(configeditor): agents tab with sort and installed annotation`.

### Task 4: Providers tab — list rendering + sort

**Files:**
- Create: `internal/configeditor/providers_tab.go`
- Test: `internal/configeditor/providers_tab_test.go`

- [ ] Test `TestProvidersTab_SortByID` — providers `[zeta, alpha, mu]` → sorted to `[alpha, mu, zeta]`.
- [ ] Run → FAIL.
- [ ] Implement `providerItem` + `buildProvidersList` (sort by ID, render `id / location / auth.type / base_url`).
- [ ] Run → PASS.
- [ ] Commit `feat(configeditor): providers tab`.

### Task 5: Models tab — list rendering + sort

**Files:**
- Create: `internal/configeditor/models_tab.go`
- Test: `internal/configeditor/models_tab_test.go`

- [ ] Test `TestModelsTab_SortByProviderFamilyName` — models `[{p:ollama, family:gemma4, name:b}, {p:agy, family:-, name:a}, {p:ollama, family:gemma4, name:a}]` → sorted to `[agy/a, ollama/gemma4/a, ollama/gemma4/b]`.
- [ ] Run → FAIL.
- [ ] Implement `modelItem` + `buildModelsList` (sort by `provider_id, family, model_name`).
- [ ] Run → PASS.
- [ ] Commit `feat(configeditor): models tab`.

### Task 6: Wire tabs into the model

**Files:**
- Modify: `internal/configeditor/editor.go`
- Test: `internal/configeditor/editor_test.go`

- [ ] Test `TestSwitchSection_RebuildsList` — switch to `sectionProviders`, assert `m.lists[sectionProviders]` is populated from `m.cfg.Providers`.
- [ ] Run → FAIL.
- [ ] Implement `loadCmd()` (mirrors `ollamaconfig.loadCmd`): loads config, builds all three lists, returns `loadedMsg`.
- [ ] Wire `loadedMsg` into `Update`; add `tea.WindowSizeMsg` handling that resizes all three lists.
- [ ] Run → PASS.
- [ ] Commit `feat(configeditor): wire tabs into model with config load`.

### Task 7: Provider form

**Files:**
- Create: `internal/configeditor/provider_form.go`
- Test: `internal/configeditor/provider_form_test.go`

- [ ] Test `TestProviderForm_Add_Success` — open form, set `id=foo`, `location=local`, `auth.type=none`; `Ctrl-S`; assert `cfg.Providers` contains the new entry, `m.dirty` reset to `false`.
- [ ] Test `TestProviderForm_EditImmutableID` — editing an existing provider, attempt to change `id`; assert id is unchanged.
- [ ] Test `TestProviderForm_EmptyID_BlocksSave` — empty id; `Ctrl-S`; assert error `provider id is empty`, cursor jumps to id field, `m.dirty` still true.
- [ ] Run → FAIL.
- [ ] Implement `provider_form.go`: `formRow` interface, `providerForm` struct, `enterProviderForm(prov, isNew)`, `handleFormUpdate`, `handleFormSave` (validates, calls `applyToConfig`, sets `dirty=true`).
- [ ] Run → PASS.
- [ ] Commit `feat(configeditor): provider add/edit form`.

### Task 8: Model form

**Files:**
- Create: `internal/configeditor/model_form.go`
- Test: `internal/configeditor/model_form_test.go`

- [ ] Test `TestModelForm_Add_Success` — open form for new model, fill `id=agy/test`, `family=test`, `provider_id=agy`, `tags=code,design`; `Ctrl-S`; assert entry appended with parsed tags.
- [ ] Test `TestModelForm_ProviderPicker_OnlyExisting` — open form, open provider picker; assert only existing providers are listed (no "new" option).
- [ ] Test `TestModelForm_DuplicateID_BlocksSave` — try to add a model with an existing id; assert error.
- [ ] Test `TestModelForm_LocationDerived` — render the form with `provider_id=ollama`; assert `location` field shows `local` read-only.
- [ ] Run → FAIL.
- [ ] Implement `model_form.go` mirroring `provider_form.go`. Tags parsed via `config.ParseFilterList` (existing helper from `internal/config`). Location read-only, derived via `cfg.ResolveLocation(updated)`.
- [ ] Run → PASS.
- [ ] Commit `feat(configeditor): model add/edit form with provider picker`.

### Task 9: Agent form

**Files:**
- Create: `internal/configeditor/agent_form.go`
- Test: `internal/configeditor/agent_form_test.go`

- [ ] Test `TestAgentForm_Add_Success` — open form for new agent, fill `name=foo`, `supported_providers=[agy]`, `default_provider=agy`; `Ctrl-S`; assert entry appended.
- [ ] Test `TestAgentForm_DefaultProvider_Constrained` — pick `supported_providers=[agy, claude]`; assert `default_provider` picker only shows `agy, claude`.
- [ ] Test `TestAgentForm_NoProviders_BlocksSave` — empty `supported_providers`; `Ctrl-S`; assert error `agent must have at least one supported provider`.
- [ ] Test `TestAgentForm_InstalledReadOnly` — assert `installed` field has no editable cursor marker.
- [ ] Run → FAIL.
- [ ] Implement `agent_form.go`. Multi-select picker uses `bubbles/list` with toggle-on-space behavior. Default-provider picker rebuilds when supported_providers changes.
- [ ] Run → PASS.
- [ ] Commit `feat(configeditor): agent add/edit form`.

### Task 10: Delete confirmation

**Files:**
- Create: `internal/configeditor/delete.go`
- Test: `internal/configeditor/delete_test.go`

- [ ] Test `TestDelete_Agent_SucceedsAfterConfirm` — `d` on agent row → `y`; assert row removed, `m.dirty=true`.
- [ ] Test `TestDelete_Provider_BlockedByModelRef` — provider `agy` referenced by `agy/native` model; `d` then `y`; assert error `cannot delete provider "agy": referenced by model "agy/native"`, row NOT removed.
- [ ] Test `TestDelete_Provider_BlockedByAgentRef` — provider `claude` referenced by `claude` agent; same pattern.
- [ ] Test `TestDelete_Model_Succeeds` — delete any model; assert removed.
- [ ] Test `TestDelete_Cancel_PreservesRow` — `d` then `n`; assert row still in cfg.
- [ ] Run → FAIL.
- [ ] Implement `delete.go`: `enterDelete(target)` sets `phaseDelete` and renders inline prompt `delete <kind> "<id>"? [y/N]`. `handleDeleteConfirm(y)` runs `referencedBy(section, index)` check, then `removeFromConfig` + `dirty=true` or sets `deleteError`.
- [ ] Run → PASS.
- [ ] Commit `feat(configeditor): delete confirmation with FK check`.

### Task 11: Save + dirty tracking

**Files:**
- Create: `internal/configeditor/save.go`
- Test: `internal/configeditor/save_test.go`

- [ ] Test `TestSave_AtomicWrite` — make an edit; `Ctrl-S`; assert `config.toml` on disk contains the new entry; temp file does not remain (mirrors `themes.Save` test).
- [ ] Test `TestSave_ValidationFails_BlocksWrite` — make an edit that violates `config.Validate`; `Ctrl-S`; assert `config.toml` unchanged, error rendered.
- [ ] Test `TestSave_DirtyFlag_TogglesOn` — field edit flips `m.dirty=true`; successful save flips to `false`.
- [ ] Test `TestSave_FailureKeepsDirty` — stub `saveCmd` to return error; assert `m.dirty=true` remains, error rendered.
- [ ] Run → FAIL.
- [ ] Implement `save.go`: `saveCmd` (stub-able), `handleSave()` runs `cfg.ValidateAll` then dispatches `saveCmd`, `saveMsg` handler updates `dirty`.
- [ ] Run → PASS.
- [ ] Commit `feat(configeditor): atomic save with validation gating`.

### Task 12: Quit with unsaved prompt

**Files:**
- Modify: `internal/configeditor/editor.go`
- Test: `internal/configeditor/editor_test.go`

- [ ] Test `TestQuit_CleanExits` — `q` with `dirty=false`; assert `tea.Quit` returned.
- [ ] Test `TestQuit_DirtyPrompts` — `q` with `dirty=true`; assert `m.quitPrompt=true` and no quit yet.
- [ ] Test `TestQuitPrompt_Save` — `y` on prompt; assert save dispatched.
- [ ] Test `TestQuitPrompt_Discard` — `n` on prompt; assert quit.
- [ ] Test `TestQuitPrompt_Cancel` — `c` or `Esc`; assert returns to list, `m.quitPrompt=false`.
- [ ] Run → FAIL.
- [ ] Implement quit flow: `Ctrl-C`/`q` checks `dirty`; if dirty, sets `phaseQuit` and renders prompt; `y`/`n`/`c`/`esc` keys handle the three branches.
- [ ] Run → PASS.
- [ ] Commit `feat(configeditor): quit with unsaved-changes prompt`.

### Task 13: Wire `wt config` (no subcommand) to launch TUI

**Files:**
- Modify: `cmd/wt/commands_config.go`
- Modify: `cmd/wt/main.go` (line 257)
- Modify: `cmd/wt/main_test.go`

- [ ] Test `TestConfig_NoSubcommand_LaunchesEditor` — invoke `wt config` with no args; assert cobra exits 0 and `configeditor.Run` is called (use a test seam or assert by absence of error).
- [ ] Run → FAIL.
- [ ] Implement `configCmd.RunE = func(cmd, args) error { return configeditor.Run(a.theme) }`. Update `Long` description.
- [ ] Modify `cmd/wt/main.go` line 257: `cmd.AddCommand(rotateCmd(a), configCmd(a))`.
- [ ] Run `go test ./cmd/wt -v` → PASS.
- [ ] Commit `feat(wt): route wt config to the viewer-and-editor TUI`.

### Task 14: Remove `wt models` and `wt agents`

**Files:**
- Modify: `cmd/wt/commands.go` (delete `modelsCmd`, `agentsCmd`)

- [ ] Test `TestModelsCmd_Removed` — `wt models`; assert cobra returns "unknown command" error.
- [ ] Test `TestAgentsCmd_Removed` — `wt agents`; same.
- [ ] Run → FAIL (current behavior: commands work).
- [ ] Delete `modelsCmd` and `agentsCmd` functions and any imports they exclusively used (check: `registry`, `agents` package usage in `commands.go`).
- [ ] Run → PASS.
- [ ] Commit `refactor(wt): remove wt models and wt agents commands`.

### Task 15: Manual smoke + docs

**Files:**
- Modify: `docs/wt-config.md`
- Modify: `CLAUDE.md`

- [ ] Run `make build` → succeeds.
- [ ] Run `make install` → binary on PATH.
- [ ] Run `wt config` from a real terminal → TUI launches, three tabs render, `Tab`/`Shift-Tab` cycle, `1`/`2`/`3` jump.
- [ ] In a temp config, `Enter` on an agent row → form opens; edit `default_provider`; `Ctrl-S`; row updates; `q` quits cleanly.
- [ ] Try to delete a provider referenced by a model → error renders, row preserved.
- [ ] Verify `wt config theme`, `wt config path`, `wt config ollama` still work unchanged.
- [ ] Update `docs/wt-config.md`: add "viewer" section (tabs, sort orders, keybindings) + "deletion rules" subsection.
- [ ] Update `CLAUDE.md`: replace `wt models`/`wt agents` references with `wt config`; add `internal/configeditor/` row to the test-coverage table (~30-40 tests).
- [ ] Commit `docs: document wt config viewer and update CLAUDE.md`.

---

## Verification gates

Run all of these before declaring done:

```bash
go test ./...                              # all Go tests pass
go vet ./...                               # no static analysis errors
go build ./...                             # everything compiles
make check                                 # bash lint + format-check
make test                                  # bash smoke tests
```

Manual smoke (Task 15) covers the TUI itself.

---

## Implementation notes

**Bubble-tea idioms to mirror from `internal/ollamaconfig/`:**
- `saving bool` flag in the model prevents duplicate save dispatches during async round-trip.
- `isTyping()` guard (`bubbles/list.FilterState() == list.Filtering`) blocks global key shortcuts while the user is typing in the list filter.
- `currentProgram *tea.Program` package var so any future subprocess (none needed for this design) can `ReleaseTerminal`/`RestoreTerminal`. Not used in the viewer itself, but keep the var in case a future form spawns an editor.
- `Run(theme)` returns `error`; `cmd/wt/commands_config.go` calls it from `RunE`.

**Reuse, don't reinvent:**
- `config.ParseFilterList` for tags (used in `wt -T`).
- `config.Validate` / `ValidateAll` for save-time validation.
- `config.Save` / `config.WriteFileAtomic` for atomic writes.
- `agents.Installed(name)` for the `installed` annotation.
- `agents.IsCommand(name)` for commands-first sort.
- `tui.ThemedListDelegate(theme)` and `tui.ErrorStyle(theme)` for theming.
- `themes.Get("default")` to look up the default theme in tests.

**Test seams (mirroring `internal/themes` and `internal/ollamaconfig`):**
- `SetConfigDirForTest(func() string) (restore func())` — points `config.Dir` at a temp dir for save tests.
- `var saveCmd = func(cfg *config.Config) tea.Cmd { ... }` — overridable in tests for failure injection.
- `agents.RegisterTest(name, factory)` (existing) — for the commands-first sort test.

**Out of scope (follow-up if ever needed):**
- Folding `wt config ollama` into the viewer as a "filter to ollama models" view.
- Cascade delete.
- Editing `default_tag`.
- Concurrent-edit detection (file mtime check on `Run()` entry).
