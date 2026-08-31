# Rotation By Launch Design

## Overview

Remove the explicit `r` key from the model picker and replace it with implicit rotation driven by the launch action: every time the picker is entered, the cursor lands on the model *after* the one that was last launched in this rotation. Up/down navigates; Enter launches and records the choice; the next picker entry advances automatically. Fix the picker input-forwarding regression and the rotation/picker model-snapshot mismatch as part of this work.

## Motivation

Two bugs reported from the field, both rooted in the lesson-15 model-picker redesign:

1. **Up/down don't move the cursor.** Pressing up/down in the model picker is a no-op. The list visually exists and renders, but the cursor stays put.
2. **`r` "gets stuck" or "skips" models.** With the `pi` agent (which only supports the `ollama` provider) and a `code` tag catalog of 8 ollama models, the user can press `r` 7 times and see the cursor advance through 7 of them, then 3 more presses do nothing, then a 4th press wraps back to the start. The list shows 8 models; rotation advances internally but the cursor doesn't follow.

### Root cause of bug 1: keys not forwarded to the picker

`internal/tui/app.go` `Update()` forwards `tea.KeyMsg` to the worktree `m.list`, the resume `m.resume.choices`, the guard `m.guardWarnModel`, the ollama `m.ollamaWarnModel`, and the `m.newInput`. It does **not** forward keys to the model picker `m.models` when `m.phase == phaseModel`. The list's built-in up/down/enter handlers are never called. The other handlers (`r`, `d`) work because they're intercepted at the `app.go` level — every other key falls on the floor.

### Root cause of bug 2: rotation's view of "the code tag" is not the picker's view

`r` calls `rotation.ForTag(m.cfg, m.tag).Next(oppositeTag(m.tag))`. `ForTag` reads from `cfg.ModelsWithTag(tag)` — the **unfiltered** catalog of every model tagged with `code` (including `copilot/native`, `claude/native`, `codex/native`, etc.). The `pi` agent's picker, by contrast, is built from `cfg.ModelsForAgentAndTag("pi", "code")` — only the ollama cloud models. The two views differ in:

- **Order:** `ModelsWithTag` returns config-order; `ModelsForAgent` preserves `cfg.Models` order, which can differ.
- **Membership:** `ModelsWithTag` includes models the agent can't drive; the picker doesn't.

The `r` handler takes the rotation's chosen model and linearly scans `m.models.Items()` for a matching ID. If the rotation returns a model the picker can't display (e.g. `copilot/native` for the `pi` agent), the cursor stays put even though rotation advanced. Worse, with cross-skip active, the rotation can return models the picker doesn't show for several iterations in a row, and the user sees the cursor "stuck" or "skipping" when it finally lands.

### The fix: simpler model, single source of truth

Replace the manual `r` rotation with implicit "last-launched + 1" rotation:

- On every picker entry, position the cursor on the model *after* the one most recently launched in this rotation.
- Drop the `r` key. The user navigates with up/down.
- The picker's filtered model list becomes the rotation's model set — one snapshot, one order, no mismatch.

This also drops the `otherTag` cross-skip. The new rotation cycle is "after the last launch in this tag" — clean, deterministic, and obvious to the user.

## Goals

- Up/down moves the cursor in the model picker.
- `r` key is removed from the model picker. The footer reflects the new keys.
- Every picker entry positions the cursor on the model *after* the last one launched in this rotation (or index 0 if there is no prior launch, or the prior launch is no longer in the snapshot).
- Enter launches the highlighted model and records it as "last launched" in the rotation state.
- The rotation's model snapshot matches the picker's filtered list exactly — no more cross-view drift.
- The `rotation-<tag>.state` file format becomes a single line (just the model ID). Old 2-line files still read correctly via last-non-empty-line semantics.

## Non-Goals

- Per-agent rotation state. The `rotation-<tag>.state` file is shared across all agents using the same tag. (Per-agent memory would require either renaming the state file or moving the key; out of scope.)
- "Remember my manual pick for one cycle" behavior. The user-chosen model becomes the next entry's "after" point only when the user actually launches it.
- Changes to the worktree list, the resume prompt, the guard warning, the new-worktree prompt, or the shell agent path.
- Changes to `internal/config/`. The `ModelsForAgent` and `ModelsForAgentAndTag` helpers are correct as-is.
- Changes to `docs/go-course/`. Historical record, frozen by user convention.
- Changes to the `wt rotate <tag>` CLI subcommand surface. It still works; the new `LastLaunched`/`RecordLaunch` API gives it a useful read primitive.

## Architecture

### File-Level Changes

| File | Change |
|---|---|
| `internal/rotation/rotation.go` | Replace `Next`/`loadIndex`/`saveState` with `LastLaunched`/`RecordLaunch`/`FirstAfter`. State file = `<model_id>\n`. |
| `internal/rotation/rotation_test.go` | Rewrite for new API. Add: round-robin semantics, single-line file, backward-compat read of old 2-line files, snapshot mismatch. |
| `internal/tui/model_list.go` | Add `FindAfter(models []config.Model, target config.Model) (config.Model, bool)` helper. Wraps to `models[0]` if `target` is missing or last. |
| `internal/tui/app.go` | Add `phaseModel` key-forwarding branch in `Update()`. Remove the `r` handler in `phaseModel`. Remove the ollama-warn "rotate"/"skip" choice (now proceed/cancel only). Replace `m.current` reads with the highlighted `m.models` item. Capture a `rotation.Rotation` snapshot at picker entry and on `d` tag-toggle. Call `m.rotation.RecordLaunch(highlighted)` on Enter. Remove the `m.current` field from the `model` struct. |
| `internal/tui/agent_model_test.go` | Rewrite `r` tests for the new "last-launched" semantics. Add: up/down moves cursor; phaseModel forwards keys to the list; Enter records last-launched; next picker entry advances. |
| `internal/tui/launch.go` | Remove the `ollamaSkipChoice` enum value and the skip/rotate branch in the ollama-warn `enter` handler. The `ollamaWarnModel` choice set becomes `{proceed, cancel}`. |
| `internal/tui/app_test.go` | Update the few tests that reference `m.current` to use the highlighted list item. Update the ollama-warn choice-set test to assert only two choices. |

Files NOT touched: `internal/config/`, `internal/registry/`, `internal/worktree/`, `internal/session/`, `internal/initseed/`, `internal/guard/`, `internal/ollamacheck/`, `cmd/wt/*`, `docs/go-course/*`.

### New Rotation API

```go
// Rotation remembers which model was last launched in a tag group.
// Unlike the prior Next()-based API, the model set is fixed at
// construction time and rotation is driven by the launch action:
// every time the picker is entered, the cursor lands on the model
// after the last-launched one. The model set is the picker's
// filtered snapshot, so rotation's view always matches the picker's.
type Rotation struct {
    tag      string
    models   []config.Model
    stateDir string
}

// LastLaunched returns the model most recently recorded for this
// rotation, or (zero, false) if the state file is missing, empty,
// or references a model no longer in the snapshot. The state file
// may be the new single-line format or the legacy 2-line format
// ("<index>\n<model_id>\n"); either way, the last non-empty line
// is treated as the model ID.
func (r *Rotation) LastLaunched() (config.Model, bool)

// RecordLaunch writes the given model as the new last-launched.
// The write is atomic (temp file + rename). Errors are returned to
// the caller; the picker proceeds with the launch even if the write
// fails (the next picker entry will fall back to index 0).
func (r *Rotation) RecordLaunch(m config.Model) error

// FirstAfter returns the model that comes after target in the
// snapshot, wrapping to the first item if target is the last or
// not in the snapshot. Returns (zero, false) if the snapshot is
// empty. This is the "what to show on next picker entry" calculation.
func FirstAfter(models []config.Model, target config.Model) (config.Model, bool)
```

The `ForTag` constructor is removed (it built the rotation from `cfg.ModelsWithTag(tag)` — the unfiltered catalog, which is the source of the bug). The new construction site is `internal/tui/app.go`, which passes the picker's filtered list.

### State File Format

**Old (2 lines):**
```
5
ollama/minimax-m3:cloud
```

**New (1 line):**
```
ollama/minimax-m3:cloud
```

**Read backward-compat:** `LastLaunched` splits the file on newlines, trims each line, and returns the last non-empty line. Old 2-line files read as `ollama/minimax-m3:cloud`. The leading index line is ignored.

**Write:** `RecordLaunch` overwrites the file with a single line: `<model_id>\n`. The next launch normalizes the format.

### Picker Input Forwarding

Today, the relevant branch in `Update()` is:

```go
if m.ready && m.phase == phaseList {
    var cmd tea.Cmd
    m.list, cmd = m.list.Update(msg)
    return m, cmd
}
```

Add the analogous branch for `phaseModel`:

```go
if m.phase == phaseModel && m.width > 0 && m.height > 0 {
    var cmd tea.Cmd
    m.models, cmd = m.models.Update(msg)
    return m, cmd
}
```

This goes *before* the existing `m.resume.choices.Update`, etc. The order matters: the worktree list and the model picker are mutually exclusive phases, so the two branches don't conflict.

### Removed: `r` key in `phaseModel`

Delete the `case "r":` block in `Update()`. The footer text in `phaseModelView` drops `[r] rotate`. The user navigates with up/down (now working) and launches with Enter.

### Removed: ollama-warn "skip" choice

Today the ollama warning picker has three choices: proceed / skip (rotates to next model) / cancel. "Skip" was useful when the only way to advance rotation was the `r` key — but with implicit rotation-by-launch, the user can just press up/down on the picker and Enter on a different model. The warning becomes two choices: proceed / cancel.

The ollama-warn `enter` handler in `app.go` no longer needs the `ollamaSkipChoice` branch; the `ollamaWarnModel` is rebuilt with only the two remaining choices.

### Removed: `m.current` field

`m.current config.Model` is removed. The "what's about to launch" model is always `m.models.SelectedItem().(modelItem).model` (or an equivalent at the call site). No drift between cursor position and launch target. The launch flow reads the highlighted item at Enter, records it, and proceeds.

### Picker State: `m.rotation` replaces `m.current`

The `model` struct loses its `current config.Model` field and gains a `rotation *rotation.Rotation` field:

```go
type model struct {
    // ... existing fields ...
    // removed: current config.Model
    rotation *rotation.Rotation // snapshot rotation for the active (agent, tag); set on picker entry and on 'd' tag toggle
    // ... rest of existing fields ...
}
```

`m.rotation` is built from the picker's filtered list at entry (in `selectedEntryMsg`) and on every `d` tag-toggle. It is read by the picker-entry code (`LastLaunched` + `FirstAfter`) and written by the launch code (`RecordLaunch`). The rotation's model set is the picker's model set — no cross-view drift.

### Picker Entry: Position the Cursor

`selectedEntryMsg` handler today:

```go
m.phase = phaseModel
m.models = buildModelList(models, m.width-2, m.height-2)
// ...pick next model from rotation.ForTag(...)
```

New:

```go
m.phase = phaseModel
m.models = buildModelList(models, m.width-2, m.height-2)
m.rotation = rotation.New(m.tag, models, "")
m.modelsFor = m.agent
m.modelsTag = m.tag
// Position cursor on the model after the last-launched (or index 0
// if no last-launched or the saved ID is no longer in the snapshot).
if last, ok := m.rotation.LastLaunched(); ok {
    if next, ok := rotation.FirstAfter(models, last); ok {
        m.models.Select(indexOfModel(models, next))
    } else {
        m.models.Select(0)
    }
} else {
    m.models.Select(0)
}
```

The `d` tag-toggle gets the same treatment, rebuilding `m.rotation` from the new tag's filtered list.

### Launch Flow: Record Before Proceed

`enter` in `phaseModel` today:

```go
highlighted := m.models.SelectedItem().(modelItem).model
m.current = highlighted
if ollamacheck.IsOllamaModel(m.current) {
    // ... check, warn, launch
}
```

New:

```go
highlighted := m.models.SelectedItem().(modelItem).model
_ = m.rotation.RecordLaunch(highlighted) // best-effort
if ollamacheck.IsOllamaModel(highlighted) {
    // ... check, warn, launch using highlighted (not m.current)
}
```

`RecordLaunch` is best-effort: a state-file write failure does not block the launch. The user-facing behavior of the launch is unchanged; the rotation state is a side effect of "what the user picked." If the user cancels at the ollama warning or the resume prompt, the state has already been recorded for what they highlighted — that's the user's last *intent*, and the next picker entry will cycle to the next model anyway.

### Footer

`phaseModelView` footer today:

```
[r] rotate   [d] switch tag   [enter] launch   [q] quit
```

New:

```
[↑/↓] navigate   [d] switch tag   [enter] launch   [q] quit
```

## Behavior

### Cold Start: First-Ever Launch

```
worktree picker (Enter)
        │
        ▼
selectedEntryMsg
        │
        ▼
m.rotation = rotation.New("code", filteredModels, "")
LastLaunched() → (zero, false)   // state file missing
        │
        ▼
m.models.Select(0)               // cursor at first model
```

The first-ever launch records the highlighted model (whatever it is — index 0 by default) and proceeds.

### Subsequent Entry: After a Launch

```
worktree picker (Enter) — second time
        │
        ▼
selectedEntryMsg
        │
        ▼
m.rotation = rotation.New("code", filteredModels, "")
LastLaunched() → ollama/A:cloud   // from state file
        │
        ▼
FirstAfter([A, B, C], A) → B
m.models.Select(indexOfModel(filteredModels, B))   // cursor on B
```

If the user launched `B` last time, the next picker entry shows the cursor on `C` (or wraps to `A` if `B` was last in the snapshot).

### Config Changed: Last-Launched No Longer in Snapshot

```
worktree picker (Enter)
        │
        ▼
m.rotation = rotation.New("code", filteredModels, "")
LastLaunched():
    data = "ollama/removed-model:cloud\n"
    id = "ollama/removed-model:cloud"
    for _, m := range r.models: m.ID == "ollama/removed-model:cloud"  // not found
    return (zero, false)
        │
        ▼
m.models.Select(0)               // cursor at first model
```

A model that's been removed from the config no longer drives the cursor. The user lands on index 0 — the safest fallback.

### User Navigates Manually, Launches, Re-enters

```
Picker entry 1: cursor on B (after last launch was A)
User presses ↓ ↓ ↓: cursor on E
User presses Enter: launches E, records E
        │
        ▼
Picker entry 2: cursor on FirstAfter([...], E)
```

The user's manual pick becomes the new "last launched" because they pressed Enter. There's no "manual picks stick for one cycle" behavior — every picker entry advances from the last *launched* model.

### `d` Tag Toggle

```
phaseModel, user presses 'd'
        │
        ▼
prevTag := m.tag
m.tag = oppositeTag(m.tag)   // "code" → "design" or vice versa
        │
        ▼
models := cfg.ModelsForAgentAndTag(m.agent, m.tag)
        │
        ├── empty → m.tag = prevTag; m.status = "..."; return
        │
        └── non-empty
            m.models = buildModelList(models, ...)
            m.rotation = rotation.New(m.tag, models, "")   // fresh snapshot for the new tag
            m.modelsTag = m.tag
            if last, ok := m.rotation.LastLaunched(); ok {
                m.models.Select(indexOfModel(models, FirstAfter(models, last)))
            } else {
                m.models.Select(0)
            }
```

The new tag's state file is independent (`rotation-design.state` vs `rotation-code.state`), so the cursor lands on the design-tag's last-launched + 1.

### Up/Down

```
phaseModel, user presses 'down' (or 'j')
        │
        ▼
m.models, cmd = m.models.Update(msg)   // bubble/list moves cursor
return m, cmd
```

That's it. `bubble/list` handles up/down/left/right/page-up/page-down/home/end by default. We don't write any keypress handlers for arrow keys.

## Error Handling

| Surface | Behavior |
|---|---|
| `RecordLaunch` write fails | Log to status: "rotation state not saved: …"; proceed with launch. Next picker entry falls back to index 0. |
| State file corrupted (gibberish, no usable ID) | `LastLaunched` returns `(zero, false)`; picker lands on index 0. |
| Last-launched model no longer in the snapshot (config changed) | `LastLaunched` returns `(zero, false)`; picker lands on index 0. |
| `FirstAfter` with empty snapshot | Returns `(zero, false)`; caller falls back to `m.models.Select(0)` (unreachable given the validation gate at picker entry, but defended). |
| `d` toggles to empty tag | Existing behavior: restore previous tag, set `m.status`, no phase change. **Unchanged.** |
| Ollama unavailable | Existing `phaseOllamaWarn` with proceed/cancel. **Unchanged except for the removed skip choice.** |
| Session exists | Existing `phaseResume` with resume/fresh/cancel. **Unchanged.** |
| Config load error | `tui.Run` returns error before TUI starts. **Unchanged.** |
| Window size 0 before first `WindowSizeMsg` | `phaseModel` renders "model picker (waiting for window size)". **Unchanged.** |

## Tests

### `internal/rotation/rotation_test.go` (rewrite)

1. `TestLastLaunchedMissingFileReturnsFalse` — fresh state, no file, returns `(zero, false)`.
2. `TestLastLaunchedReadsSingleLineFile` — write `<id>\n`, read back same model.
3. `TestLastLaunchedReadsLegacyTwoLineFile` — write `5\n<id>\n`, read back the model (backward compat).
4. `TestLastLaunchedConfigChangedReturnsFalse` — saved ID not in current snapshot, returns `(zero, false)`.
5. `TestRecordLaunchOverwrites` — write A, write B, read returns B.
6. `TestRecordLaunchAtomic` — write, read, check file mode is 0600 and content is single line.
7. `TestFirstAfterMiddle` — target at index 2 of 5, returns index 3.
8. `TestFirstAfterLast` — target at last index, wraps to index 0.
9. `TestFirstAfterMissing` — target not in list, returns index 0.
10. `TestFirstAfterEmpty` — empty snapshot, returns `(zero, false)`.

### `internal/tui/agent_model_test.go` (additions and rewrites)

The existing `phaseModelWithList` helper builds a `model` in `phaseModel` with `m.current` set from the rotation. **Rewrite this helper** to use the new `m.rotation` field and position the cursor via `LastLaunched` + `FirstAfter`.

1. `TestUpDownMovesCursor` — `m.models.SelectedIndex()` changes after up/down `KeyMsg`s. **(Smoking-gun test for the up/down bug.)**
2. `TestPhaseModelForwardsKeyToList` — `Update(KeyMsg{down})` in `phaseModel` returns a non-nil `tea.Cmd` and changes the cursor. **(Catches the regression where `m.models.Update` was never called.)**
3. `TestSelectedEntryNoLastLaunchedStartsAtZero` — no state file, cursor at 0.
4. `TestSelectedEntryPositionsAfterLastLaunched` — state file has `ollama/A:cloud`, list is `[A, B, C]`, cursor lands on `B`.
5. `TestSelectedEntryLastLaunchedMissingFallsBackToZero` — state file has an ID not in the filtered list, cursor at 0.
6. `TestSelectedEntryLastLaunchedLastInListWrapsToZero` — state file has the last item, cursor wraps to 0.
7. `TestEnterInModelPhaseRecordsLastLaunched` — pressing Enter writes the highlighted model's ID to `rotation-<tag>.state`. **(Replaces the old "Enter launches without session" test's m.current check.)**
8. `TestNextEntryAfterLaunchAdvancesCursor` — picker entry 1 launches `B`, picker entry 2 lands on `C`.
9. `TestNextEntryAfterManualPickAdvancesFromManualPick` — picker entry 1, user navigates to `E` and presses Enter, picker entry 2 lands on `FirstAfter([...], E)`.
10. `TestToggleTagRebuildsRotation` — `d` rebuilds both the list and the rotation snapshot; the new tag's `LastLaunched` drives the cursor.
11. `TestNoRKeyInModelPhase` — pressing `r` in `phaseModel` is a no-op. **(Catches accidental re-introduction.)**
12. `TestNoCurrentField` — compile-time check that the `model` struct no longer has a `current` field. (Implemented by removing the field; the test is a comment-documented guardrail.)

### `internal/tui/app_test.go` (small updates)

The handful of tests that reference `m.current` get updated to use the highlighted `m.models.SelectedItem().(modelItem).model` instead. Most tests don't touch `m.current` and are unaffected.

### `internal/tui/ollama_warn_test.go` (if it exists)

1. `TestOllamaWarnHasProceedAndCancelOnly` — choice set is `{proceed, cancel}`; no "skip" choice.

## Migration / Cleanup

- The `m.current` field is removed; the struct shrinks by one.
- The `case "r"` block in `phaseModel` is removed.
- The ollama-warn "skip" choice is removed.
- The `case "r"` block in the launch path is gone (replaced by the picker-input-forwarding branch).
- The state file format changes on disk; the next launch normalizes. No explicit migration step; no user action required.
- The `internal/rotation/rotation_test.go` test that exercises `Next` is rewritten for the new API.
- The `wt rotate <tag>` CLI subcommand: its `Next` callsite (if any) is updated. If the subcommand is unused (it's a test helper), document that it now prints "next-to-use" via `LastLaunched` + `FirstAfter` and is read-only.
- `docs/go-course/lesson-14-agent-model-screen.md` and `lesson-15-model-browser.md` are **not** updated — historical record, frozen by user convention.

## Decisions (for reviewer objection)

1. **`r` key is removed, not aliased.** Some users may have muscle memory for `r`. Aliasing `r` to "advance to next" would reintroduce the snapshot-mismatch bug at the API level. Removing the key is the cleanest fix; the up/down arrow keys are now the only way to navigate, and the implicit rotation-by-launch replaces the explicit `r`.

2. **`RecordLaunch` happens before the ollama check, not after a successful launch.** The alternative — record only on actual launch — would let users hit ollama-unavailable, cancel, navigate to a different model, launch, and have the state still reflect the failed attempt. Recording on Enter matches the user's last *intent* and keeps the API simple. The cancellation case is rare and the next entry cycles anyway.

3. **No per-agent rotation state.** The `rotation-<tag>.state` file is shared. A `pi` user and a `claude` user on the same tag will see the same "next" model. Per-agent memory would require either a state-file-per-agent or moving the last-launched key into a different config. This is a follow-up if it becomes painful.

4. **No "remember my manual pick for one cycle."** The user's manual pick becomes the next entry's "after" point only when they press Enter. If they back out with `q` or `esc`, the state is unchanged. This is the simplest model and matches the "every launch advances" mental model.

5. **Cross-skip is gone.** The previous design used `oppositeTag(m.tag)` to avoid both tag groups landing on the same model. With the new model, each tag's rotation is independent (it advances from its own last-launched), and the user navigates each tag's picker with up/down. If both tags happen to be on the same model, the user can see that and move on. Cross-skip was solving a problem that's largely cosmetic.

6. **State file is single-line going forward.** Dropping the index line simplifies the read and the write. The index was only useful for the old `Next()`-with-cross-skip algorithm. New state file: `<id>\n`. Old state files: read as last-non-empty-line, no migration step.

## Open Questions

None at design time. The five "Decisions" above are choices with defaults; the reviewer can object to any of them.
