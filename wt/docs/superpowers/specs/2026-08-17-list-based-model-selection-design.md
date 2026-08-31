# List-Based Model Selection Design

## Overview

Replace the agent+model screen's "show one model, press `m` to browse" pattern with a single picker list that shows all models the active agent can drive in the active tag. Eliminate the separate model browser screen and the live model discovery that fed it.

## Motivation

After picking a worktree, the user lands on a screen that shows one model at a time. To choose a different model they must:

1. Press `r` repeatedly until they happen upon it, or
2. Press `m` to open a separate `bubble/list` browser that calls `registry.Discover` — which shells out to `ollama list` and HTTPs `https://openrouter.ai/api/v1/models` on every open.

This is two screens for one decision, and the browser shows models the active agent can't drive (e.g. `claude/native` listed for the `codex` agent). The "current model" is invisible; the user can't tell from the main screen what's about to launch.

The fix is to make the post-worktree screen itself a list picker, filtered to what the agent can actually use, sourced from the on-disk config (no network, no subprocess).

## Goals

- Render the agent+model screen as a `bubble/list` of agent-compatible models in the active tag, with a visible cursor on the chosen model.
- Filter the list by agent via `agent.SupportedProviders`, so cross-agent models (e.g. `claude/native`) only appear for the agent they apply to.
- Source the list from `config.toml` only. No `registry.Discover` from the TUI, no `ollama list`, no OpenRouter HTTP.
- Keep the existing per-tag rotation state (`rotation-<tag>.state`). Cold start lands the cursor on the rotation's "next to use" model. `r` advances. Enter launches and the cursor position drives the launched model.
- Eliminate the separate browser screen and the `m` key.
- Validate at the `phaseList → phaseModel` boundary so the picker is never entered with an empty list. Show an actionable error and stay in the picker.

## Non-Goals

- Live model discovery in the TUI. The `wt models` subcommand still uses `registry.Discover`; that's unchanged.
- Rewriting `docs/go-course/`. Those are historical records of how the lessons were built; they are not updated by subsequent redesigns.
- Changes to the shell agent path. The shell agent still skips the model screen entirely (`launchShell()`).
- Adding new config fields or changing the `config.toml` schema.
- Fuzzy filter inside the model list. The list is short (one tag's worth); `/` filtering is YAGNI for this change.
- Tag columns or multi-tag selection in the list view. The list shows one tag group at a time; `d` switches groups.
- Renaming or restructuring `internal/rotation/`. The state file format stays identical.

## Architecture

### File-Level Changes

| File | Change |
|---|---|
| `internal/config/config.go` | Add `ModelsForAgent(name)` and `ModelsForAgentAndTag(name, tag)` helpers. No other changes. |
| `internal/tui/app.go` | Reshape `model` struct. Replace `phaseModel` View/Update with a list. Drop `phaseBrowser`. Drop `m` key handler. Remove browser-related fields. |
| `internal/tui/model_browser.go` | **Deleted.** |
| `internal/tui/agent_model_test.go` | Rewritten: tests target the new list-based screen. Test helper added for building a `phaseModel` state. |
| `internal/tui/model_browser_test.go` | **Deleted.** |
| `internal/registry/` | **Unchanged.** Still used by `wt models`. |

### New Helpers in `internal/config`

```go
// ModelsForAgent returns the models whose ProviderID is in the named
// agent's supported_providers list. Order matches cfg.Models.
//
// Errors:
//   - agent not found in cfg.Agents
//   - agent references a provider not in cfg.Providers
//     (this is also caught by Validate, so the error is only reachable
//      if Validate was bypassed)
func (c *Config) ModelsForAgent(agentName string) ([]Model, error)

// ModelsForAgentAndTag intersects ModelsForAgent with HasTag(tag).
// tag == "" returns all agent-compatible models (no tag filter).
func (c *Config) ModelsForAgentAndTag(agentName, tag string) ([]Model, error)
```

The helpers mirror the existing `ProvidersForAgent`. Two helpers instead of one with an optional tag, because each has a single clear purpose and a one-line body; the call sites read better.

### Reshaped `model` Struct

```go
type model struct {
    status string
    width  int
    height int

    entries []worktree.Entry
    list    list.Model  // worktree picker
    ready   bool

    phase phase
    agent string       // resolved agent for this launch
    tag   string       // active rotation tag group (code/design)
    current config.Model // last-advanced rotation cursor; the chosen model

    // model picker (formerly phaseBrowser, now part of phaseModel)
    models    list.Model  // bubble/list of agent+tag models
    modelsTag string      // tag the list was built for; rebuild on change
    modelsFor string      // agent the list was built for; rebuild on change

    cfg *config.Config

    // launch state (unchanged)
    selectedPath string
    yolo         bool
    extraArgs    []string
    initialAgent string
    resume       resumeModel

    // default-branch guard warning (unchanged)
    defaultBranch  string
    guardWarnModel list.Model
    guardWarnEntry worktree.Entry

    // ollama availability warning (unchanged)
    ollamaWarnModel list.Model

    // new-worktree prompt (unchanged)
    newInput         textinput.Model
    newError         string
    pendingHighlight string
    repoRoot         string
    creating         bool
    listError        string
}
```

**Removed fields** (relative to today): `browser`, `browserCache`, `browserTag`, `sourceCycle`, `otherTag`.

`otherTag` is no longer model state because the cross-tag skip becomes a local to the rotation call site. A tiny helper:

```go
// oppositeTag returns the other rotation group name. For the current
// code/design setup that's a literal swap. Future tag groups would
// extend this switch.
func oppositeTag(tag string) string {
    if tag == "code" { return "design" }
    if tag == "design" { return "code" }
    return ""
}
```

The cross-tag skip behavior is preserved; it just doesn't need a field. `m.tag` is the only rotation state on the model struct.

### Phase Enum

```go
const (
    phaseList        phase = iota  // worktree list
    phaseModel                     // picker list (formerly phaseModel + phaseBrowser merged)
    phaseResume                    // resume prompt (lesson 16)
    phaseGuardWarn                 // confirm before launching on default branch
    phaseOllamaWarn                // confirm before launching with unavailable ollama model
    phaseNewWorktree               // create-new-worktree prompt
)
// phaseBrowser is REMOVED.
```

### Keybindings in `phaseModel`

| Key | Action |
|---|---|
| `up` / `down` (or `k` / `j`) | Move cursor. Provided by `bubble/list`; we don't write these handlers. |
| `r` | Advance the rotation cursor: `rotation.ForTag(m.cfg, m.tag).Next(oppositeTag(m.tag))`. List cursor jumps to the new index. State file updated by `Next`. |
| `d` | Toggle `tag` between `code` and `design`. Rebuild the list from the new tag group; cursor on the new rotation index. If the new tag is empty, restore the previous tag and set `m.status`. |
| `enter` | Launch the highlighted model. If ollama → `phaseOllamaWarn`. Else `proceedToLaunch()` (resume prompt if a prior session exists, else launch). |
| `esc` | Quit. No nested screen to pop back to. |
| `q` | Quit, gated by `m.isTyping()` (returns false in `phaseModel`). |

`m` is gone. `/` is not wired (no list filter). The list's built-in filter can be enabled later by passing `list.WithDefaultFeatures` differently; that's a follow-up.

### View Sketch

```
agent : claude
tag   : code

  > ollama/gemma4:9b       ollama  local   code
    ollama/qwen2.5-coder    ollama  cloud   code
    ollama/llama3.1:70b     ollama  cloud   code

[r] rotate   [d] switch tag   [enter] launch   [q] quit
```

The list widget draws rows; the surrounding code draws the agent/tag header and the footer. The `>` cursor on the left is the `bubble/list` default delegate's selection indicator.

The `Description()` on each item renders the metadata columns: provider, location (local/cloud), tags. The `Title()` is the model ID. This matches the existing `modelItem` shape from `model_browser.go`, which is preserved as a private type in `app.go` (or a new `model_list.go` file).

## Behavior

### Cold Start: `phaseList` → `phaseModel`

```
worktree picker (Enter)
        │
        ▼
selectedEntryMsg{entry}
        │
        ▼  Resolve agent: --agent flag wins, else cfg.DefaultAgent()
        │  If shell → launchShell() (unchanged)
        ▼
ModelsForAgentAndTag(agent, tag) → []Model
        │
        ├── err or empty → m.status = "no models for agent 'claude' in tag 'code' — edit your config"
        │                  stay in phaseList
        │
        └── non-empty
            │
            ▼
        buildModelList(models, m.width-2, m.height-2)
            │
            ▼
        next, _ := rotation.ForTag(m.cfg, m.tag).Next(oppositeTag(m.tag))
            │  Returns the next-to-use model (advances the state file).
            │  Cold start = the entry at index 0 of the state file.
            ▼
        m.models.Select(indexOf(listItems, next))
            │
            ▼
        m.phase = phaseModel; m.current = next
        m.modelsFor = m.agent; m.modelsTag = m.tag
        return m, nil
```

The cursor lands on the rotation's next-to-use model. If the state file is missing (fresh user), `Next` returns index 0.

If the helper returns an error (agent not found, provider not found), `m.status` is set with the error text and the phase stays at `phaseList`. The user can press `q` to quit or back out of the picker.

### Rotation: `r` in `phaseModel`

```
'r' keypress
        │
        ▼
next, ok := rotation.ForTag(m.cfg, m.tag).Next(oppositeTag(m.tag))
        │  ok=false → no-op (unreachable given entry validation)
        ▼
m.current = next
m.models.Select(indexOf(listItems, next))
        │
        ▼
return m, nil  (state file written by rotation.Next)
```

No list rebuild. Rotation stays within the active tag group.

### Tag Switch: `d` in `phaseModel`

```
'd' keypress
        │
        ▼
prevTag := m.tag
m.tag = oppositeTag(m.tag)   // toggle
        │
        ▼
models := cfg.ModelsForAgentAndTag(m.agent, m.tag)
        │
        ├── empty → m.tag = prevTag (restore)
        │           m.status = "tag 'X' has no models for agent Y"
        │           return m, nil
        │
        └── non-empty
            │
            ▼
        m.models = buildModelList(models, m.width-2, m.height-2)
        m.modelsTag = m.tag
            │
            ▼
        next, _ := rotation.ForTag(m.cfg, m.tag).Next(prevTag /* cross-skip the previous tag's last-used */)
        m.current = next
        m.models.Select(indexOf(items, next))
            │
            ▼
        return m, nil
```

The new `m.models = list.New(...)` rebuild discards any scroll position. Acceptable: the list is short and the user is mid-action; resuming where the cursor is matters more than scroll offset.

**Decision: empty tag defense.** When `d` toggles to a tag group that has no agent-compatible models, the previous tag is restored and `m.status` is set with a one-line message. The phase does not change; the user remains in the picker with their previous selection intact. This protects the user from toggling into a void mid-flow. If a future preference is "always show the empty state", this is a one-line change in the keypress handler.

The cross-skip target during the new tag's `Next` call is `prevTag` (the tag we're toggling away from) — its last-used model is what we want to avoid landing on in the new group. This is the same cross-tag skip as rotation, just rebased for the new active group.

### Launch: `enter` in `phaseModel`

```
'enter' keypress
        │
        ▼
highlighted := m.models.SelectedItem().(modelItem).model
m.current = highlighted   (sync current with cursor before launch)
        │
        ▼
if ollamacheck.IsOllamaModel(highlighted) →
        Available(highlighted.ModelName) →
            err     → m.status = "ollama check failed: …"; return
            !ok     → phaseOllamaWarn (proceed / skip / cancel)
            ok      → proceedToLaunch()
        │
        └── else → proceedToLaunch()
                │
                ▼
        session.LatestForAgent(agent, selectedPath) →
            err  → m.status = "session check failed: …"
            nil  → launchAgent(…) → runAndWaitCmd
            sess → phaseResume (resume / fresh / cancel)
```

The launch flow is unchanged from lessons 16 and 17. Only the source of the launched model changes: today it's `m.current` (a single model field), now it's the highlighted item from `m.models`.

### Window Resize

`tea.WindowSizeMsg` handler:

- If `m.phase == phaseModel`, call `m.models.SetSize(msg.Width-2, msg.Height-2)`. The list reflows.
- All other size handlers unchanged (worktree list, resume prompt, ollama/guard warnings, new-worktree prompt).

### Quit Keys

`esc` in `phaseModel` quits (no nested screen to pop). `q` quits, gated by `m.isTyping()`. Both unchanged.

## Error Handling

| Surface | Behavior |
|---|---|
| `selectedEntryMsg` with no models for agent+tag | `m.status` set with actionable message; stay in `phaseList`. |
| `ModelsForAgent` / `ModelsForAgentAndTag` error (agent not found, provider not found) | `m.status` set with the error; stay in `phaseList`. |
| `d` toggles to an empty tag | Restore previous tag; `m.status` set. No phase change. |
| `r` with empty group (unreachable given entry validation) | `rotation.Next` returns `false`; no-op. Safety net only. |
| Ollama unavailable (existing) | `phaseOllamaWarn` with 3 choices. **Unchanged.** |
| Session exists (existing) | `phaseResume` with 3 choices. **Unchanged.** |
| `rotation.StateFile` write fails | Out of scope; current behavior preserved. |
| Window size 0 before first `WindowSizeMsg` | `phaseModel` renders "model picker (waiting for window size)". |
| Config load error (existing) | `tui.Run` returns error before TUI starts. **Unchanged.** |

The status-line pattern (set `m.status`, stay in current phase) is the convention from lessons 12-16. All new error surfaces follow it.

## Tests

The 47 tests in `agent_model_test.go` (27) and `model_browser_test.go` (20) split into three buckets.

### Bucket A — Keep (~12 tests)

Tests that don't depend on the screen shape. May need a small adjustment to set up the list widget instead of bare `m.current`.

- `TestFirstAgentDefaultsToClaude`
- `TestFirstAgentPicksFirst`
- `TestRotatePersistsState`
- `TestRotateSkipsOtherTagLastUsed`
- `TestRotateSingleModelStaysPut`
- `TestEnterInModelPhaseLaunchesWithoutSession`
- `TestEnterInModelPhaseShowsResumePrompt`
- `TestResumePromptCancelReturnsToModel`
- `TestEscInResumePromptReturnsToModel`
- `TestLaunchDoneMsgRecordsError`
- `TestModelAndTagKeysIgnoredInListPhase` (drop the `m` key, keep `d`)
- `TestToggleBackToCode` — **rewritten**. Today it asserts on `m.otherTag`; after the change, `otherTag` is computed via `oppositeTag(m.tag)` and only `m.tag` is asserted.

### Bucket B — Rewrite (~26 tests)

Tests that assert on the old single-line view, on `m.current.ID` as the displayed model, or on browser state. Also the `(none)` placeholder tests, since the placeholder is now an entry-time error rather than a runtime screen.

- All `phaseModel` View tests that check for the `"agent : X\nmodel : Y\n"` literal string → rewritten to check the list widget.
- `TestFirstModelPlaceholder` → rewritten to assert `m.status` and unchanged phase.
- `TestSelectedEntryMsgNoModelsShowsPlaceholder` → rewritten to assert the empty-list error and unchanged phase.
- `TestToggleBackToCode` → rewritten to assert only `m.tag` (the `otherTag` field is gone; it's a function call now).
- `TestViewModelPlaceholder` → rewritten to check the "waiting for window size" placeholder.
- All `phaseBrowser` tests (20 in `model_browser_test.go`) → deleted with the file.

### Bucket C — New (~10 tests)

Each gets a top-of-function `// what / why` block per the lesson 18 convention.

1. `TestModelsForAgentFiltersByProvider` — only models whose `ProviderID` is in `SupportedProviders`.
2. `TestModelsForAgentAndTagIntersectsBoth` — agent filter and tag filter compose.
3. `TestModelsForAgentUnknownAgent` — error returned, not panic.
4. `TestSelectedEntryMsgEmptyListStaysOnList` — `m.status` set, phase unchanged.
5. `TestSelectedEntryMsgEmptyListSetsActionableStatus` — message mentions agent name and tag.
6. `TestSelectedEntryMsgPositionsCursorAtNextToUse` — `m.models` cursor index matches rotation's next model.
7. `TestModelScreenEnterUsesHighlightedNotCurrent` — the highlighted list item is what gets launched, even if `m.current` lags behind.
8. `TestModelScreenRotateMovesCursor` — pressing `r` updates both `m.current` and `m.models.SelectedIndex()`.
9. `TestModelScreenToggleTagRebuildsList` — pressing `d` rebuilds `m.models` with the new tag's items; cursor on the new rotation index.
10. `TestModelScreenToggleTagEmptyRestores` — toggling to an empty tag reverts and sets `m.status`.
11. `TestModelScreenFilterNotEnabled` — typing `/` in the list does not enter filter mode (YAGNI assertion).

Final count settled in the implementation plan.

### Test Infrastructure

- `testConfig()` in `app_test.go`: unchanged. Returns a small representative config.
- `tempStateDir(t)` and `seedState(t, …)` in `agent_model_test.go`: unchanged.
- New helper `phaseModelWithList(t *testing.T, cfg *config.Config, agent, tag string) model` at the top of `agent_model_test.go` — builds a model in `phaseModel` with `m.models` populated, `m.modelsFor`/`m.modelsTag` set, and the cursor at the rotation's next-to-use model. About 8 lines.
- The 75 tests in `app_test.go`, `launch_*_test.go`, `worktree_list_test.go`, `new_worktree_test.go` should be untouched. If any break due to struct field removals (`otherTag`, `browser`, etc.), the breakage is the signal to update them — no behavioral rewrites.

## Migration / Cleanup

- Delete `internal/tui/model_browser.go`.
- Delete `internal/tui/model_browser_test.go`.
- `internal/registry/` stays; `wt models` still uses it.
- `docs/go-course/lesson-15-model-browser.md` and `docs/go-course/lesson-14-agent-model-screen.md` are **not** updated. They are historical records of the original lesson implementations.
- No CLI flag changes. The visible user-facing changes are: (1) no `m` key, (2) the model screen now shows a list with a cursor, (3) the model list is sourced from `config.toml` only.

## Decisions (for reviewer objection)

1. **`d` empty-tag fallback: restore previous tag and set status.** The alternative — switching anyway and showing "no models" in the list — was rejected because it would silently put the user in a state where the next Enter does nothing useful. Restoring preserves the user's previous selection and surfaces a clear reason.

2. **`otherTag` removed from `model` struct.** The cross-tag skip still happens, computed locally at the rotation call site as `oppositeTag(m.tag)`. This avoids storing redundant state and matches today's behavior. If a future change needs `otherTag` as state, the field can be added back without a behavior change.

3. **No `/` filter in the model list.** The list is short (one tag group at a time, typically 2-5 items). Adding filter now is speculative; `bubble/list` supports it and can be wired later by passing `list.WithDefaultFeatures` differently.

4. **`m` key removed entirely.** A help screen (`?`) was considered and rejected as out of scope. The footer line lists all keybinds; that's enough discoverability for four keys.

5. **Test count is a target, not a hard rule.** The plan may end up with 8 or 13 new tests depending on how helpers shake out. The list above is the *intent*, not a contract.

## Open Questions

None at design time. The five "Decisions" above are choices with defaults; reviewer can object to any of them.
