# "New Worktree" Option in the Picker — Design

## Overview

Add a sentinel item at the top of the worktree/branch picker that lets the user create a new worktree from inside the TUI. The user is prompted for a branch name; on success the picker refreshes and the new entry is highlighted. A keyboard shortcut (`n`) provides the same action without selecting the sentinel.

## Motivation

The TUI's worktree/branch picker (`internal/tui/worktree_list.go`, driven by `internal/worktree.Enumerate`) is the primary way users navigate between worktrees. Today the only way to create a new worktree from a TUI session is to drop out, run `wt -w <name>` (or `git worktree add` by hand), then re-enter the TUI. This is friction — the picker is the natural place to manage worktrees, and `wt` already has a fully-tested `EnsureForName(dir, name)` that does the work.

The flag path (`-w`) is a "fast path" for users who already know what they want. The picker path should support the same operation, but picker-native: the user is in the picker because they're *managing* worktrees, not necessarily committing to launch.

## Goals

- Let the user create a new worktree from inside the TUI picker, without dropping out
- Reuse the existing `worktree.EnsureForName` (idempotent, well-tested) so we don't duplicate git logic
- Stay consistent with the existing phase model (`phaseResume`, `phaseGuardWarn`, `phaseOllamaWarn`)
- Make the action discoverable (sentinel item) *and* keyboard-driven (`n` shortcut) — both, matching how `m`/`r`/`d` are used on the model screen
- After creation, return to the refreshed picker with the new entry highlighted

## Non-Goals

- Base branch selection ("create from main" vs "create from current") — out of scope; default is `git worktree add -b <name>` (new branch from current HEAD)
- Picking an existing remote branch to base the new branch on — already supported by the existing picker entries; this feature is only about *new* branches
- Worktree deletion / pruning — separate feature
- Per-agent default branch policy — unchanged

## Architecture

### One new file: `internal/tui/new_worktree.go`

Owns the new-worktree phase: the `textinput` widget, validation, the `tea.Cmd` that calls `worktree.EnsureForName`, and the message it emits. Mirrors the separation of `model_browser.go` (one screen per file).

### Changes to `internal/tui/worktree_list.go`

Extend `entryItem` with a `kind` discriminator so the list can render a sentinel row alongside real entries. No new type — same `list.Item` interface.

### Changes to `internal/tui/app.go`

- Add `phaseNewWorktree` to the `phase` enum
- New fields on `model`: `newInput textinput.Model`, `newError string`, `pendingHighlight string`, `repoRoot string`
- Wire `n` keypress on `phaseList` and Enter on the sentinel → transition to `phaseNewWorktree`
- Handle `newWorktreeCreatedMsg` (error → stay; success → reload + highlight)
- Extend `entriesLoadedMsg` to cache `m.repoRoot` and apply `m.pendingHighlight` after the list rebuilds
- Render the new phase in `View`
- Wire `esc` to pop back to `phaseList` from `phaseNewWorktree`

### No changes to `internal/worktree/`

`EnsureForName` already handles every case we need: brand-new branches, existing local branches, and the idempotent reuse path. The TUI is a consumer of that API.

## Components

### `entryItem` extension (`worktree_list.go`)

```go
type entryKind int

const (
    kindEntry       entryKind = iota // a real worktree or branch
    kindNewWorktree                  // "+ Create new worktree…" sentinel
)

type entryItem struct {
    kind  entryKind
    entry worktree.Entry
}
```

`Title()`, `Description()`, `FilterValue()` switch on `kind`:

- `kindEntry`: existing behavior, unchanged
- `kindNewWorktree`:
  - `Title()` → `"+ New worktree…"`
  - `Description()` → `"create a new branch and worktree"`
  - `FilterValue()` → `""` (so the filter hides the sentinel; reappears when filter is cleared)

`buildList` prepends the sentinel as the first item:

```go
items := make([]list.Item, 0, len(entries)+1)
items = append(items, entryItem{kind: kindNewWorktree})
for _, e := range entries {
    items = append(items, entryItem{kind: kindEntry, entry: e})
}
```

### `new_worktree.go`

```go
package tui

import (
    "strings"

    "github.com/charmbracelet/bubbles/textinput"
    tea "github.com/charmbracelet/bubbletea"
    "github.com/ohanaverse/agent-worktree/internal/worktree"
)

const newWorktreePlaceholder = "branch-or-worktree-name"

// newWorktreeCreatedMsg is emitted after a create attempt.
type newWorktreeCreatedMsg struct {
    path string
    name string
    err  error
}

// ensureNewWorktreeCmd returns a tea.Cmd that creates a worktree via
// EnsureForName and reports the result.
func ensureNewWorktreeCmd(root, name string) tea.Cmd {
    return func() tea.Msg {
        path, err := worktree.EnsureForName(root, name)
        return newWorktreeCreatedMsg{path: path, name: name, err: err}
    }
}

// newInputModel builds a focused textinput sized to the terminal.
func newInputModel(width int) textinput.Model {
    ti := textinput.New()
    ti.Placeholder = newWorktreePlaceholder
    ti.Focus()
    ti.CharLimit = 100
    ti.Width = width - 4
    return ti
}

// validateNewWorktreeName returns an error string if name is invalid
// (empty / whitespace-only), or "" if OK. Deeper validation is delegated
// to EnsureForName.
func validateNewWorktreeName(name string) string {
    if strings.TrimSpace(name) == "" {
        return "name cannot be empty"
    }
    return ""
}
```

### `app.go` additions

```go
const phaseNewWorktree phase = iota // after the existing phases

type model struct {
    // ...existing fields...
    newInput         textinput.Model
    newError         string
    pendingHighlight string
    repoRoot         string
}
```

**`Update` (key handling):**

- `case "n":` under `phaseList && m.ready` → transition to `phaseNewWorktree`, init `m.newInput`, clear `m.newError`
- `enter` under `phaseList`: if the selected item has `kind == kindNewWorktree`, same transition (instead of treating it as a normal selection)
- `enter` under `phaseNewWorktree`: validate; on error, set `m.newError`; on OK, dispatch `ensureNewWorktreeCmd(m.repoRoot, m.newInput.Value())`
- `esc` under `phaseNewWorktree`: clear `m.newError`, transition back to `phaseList`
- `case newWorktreeCreatedMsg:` on `err` → set `m.newError`; on success → set `m.pendingHighlight = msg.name`, dispatch `loadEntriesCmd()`, transition to `phaseList`
- `case entriesLoadedMsg:` after rebuilding the list, if `m.pendingHighlight != ""`, find the matching index by branch and `m.list.Select(idx)`; clear `m.pendingHighlight`
- `case tea.WindowSizeMsg:` under `phaseNewWorktree` → update `m.newInput.Width = m.width - 4`

**`View` (new phase):**

```go
if m.phase == phaseNewWorktree {
    if m.width <= 0 || m.height <= 0 {
        return "new worktree prompt (waiting for window size)"
    }
    body := m.newInput.View()
    if m.newError != "" {
        body += "\n" + newWorktreeErrorStyle.Render(m.newError)
    }
    return body + "\n[enter] create   [esc] cancel"
}
```

Define a package-level `var newWorktreeErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))` (ANSI red) in `new_worktree.go` and use it to render `m.newError`.

**`repoRoot` caching:** `loadEntriesCmd` already calls `worktree.RepoRoot()`. The result is currently discarded after the entries are built. The new `entriesLoadedMsg` carries `repoRoot` (or we add a field) so the model can stash it without a second `git rev-parse` call per new-worktree creation.

## Data Flow

1. **List built.** `buildList(entries, width, height)` returns a list with `[sentinel, entry1, entry2, ...]`. The list is rendered as normal.
2. **User triggers.** Two paths converge on the same transition:
   - `n` keypress on `phaseList` (gated on `m.ready`)
   - Enter on the sentinel item (the existing `phaseList` Enter handler discriminates by `kind` first)
3. **Name prompt.** `phaseNewWorktree` owns the `textinput`. `esc` cancels back to `phaseList`. `enter` validates and dispatches `ensureNewWorktreeCmd`.
4. **Command runs.** `EnsureForName(root, name)` runs in a `tea.Cmd` goroutine, returns `newWorktreeCreatedMsg`.
5. **Result handled.**
   - `err != nil` → `m.newError = err.Error()`; stay on `phaseNewWorktree`; user can fix and retry.
   - `err == nil` → set `m.pendingHighlight = msg.name`; dispatch `loadEntriesCmd()`; transition to `phaseList`.
6. **Entries reload.** `entriesLoadedMsg` rebuilds the list (now with the new worktree as a real entry). After rebuilding, if `m.pendingHighlight` is set, iterate the rebuilt items, find the index where the `entryItem`'s `entry.Branch` equals `m.pendingHighlight`, and call `m.list.Select(idx)`. If no match is found (shouldn't happen, but defensively), leave the list cursor at index 0. Then clear `m.pendingHighlight`. The user is back on the picker, on the row they just created.

## Error Handling

- **Empty / whitespace-only name** → `validateNewWorktreeName` catches it before the git call. Inline error in red below the input.
- **Git failure** (invalid branch name, disk full, permission denied) → `EnsureForName` returns an error; we set `m.newError = err.Error()` and stay on the prompt. The error string is informative enough to debug from the TUI.
- **Esc during prompt** → clear `m.newError`, return to `phaseList`. No state cleanup needed; the input is overwritten on the next entry.
- **Re-trigger `n` while already on `phaseNewWorktree`** → the `n` keybind is gated to `m.phase == phaseList`, so this is a no-op. Same as how `r` is gated to `phaseModel`.
- **Pick sentinel while loading** → `m.ready` is false; the `enter` handler's existing `if !m.ready { return m, nil }` already short-circuits. The `n` keypress also checks `m.ready`. Consistent.
- **Filter typing in the list** → sentinel's `FilterValue()` is `""`, so a non-empty filter hides it. (Standard `bubbles/list` convention: items with empty `FilterValue` are hidden during filtering.) The sentinel reappears when the filter is cleared.
- **Window resize on `phaseNewWorktree`** → `tea.WindowSizeMsg` updates `m.newInput.Width` to `m.width - 4`.
- **Long pasted name** → `CharLimit = 100` caps it. Realistic branch names fit.
- **Slashed name (`feature/x`)** → allowed. `EnsureForName` uses `filepath.Base` to derive the worktree dir (`.worktrees/x`) and the branch stays `feature/x`. We surface `EnsureForName`'s error if its internal guard fires (defensive).
- **Existing branch name** → `EnsureForName` is idempotent: reuses the existing worktree or creates a new worktree for the branch. No special TUI handling needed; the user gets the expected behavior.

## Testing

All tests follow the existing `//` what/why comment convention (formalized in lesson 18).

### `internal/tui/worktree_list_test.go` — extend

- `TestSentinelItemRendersAsNew` — `entryItem{kind: kindNewWorktree}.Title()` contains `"new"` (case-insensitive). The sentinel must be visually distinct so users understand it isn't a regular branch.
- `TestSentinelItemHasEmptyFilterValue` — `FilterValue()` returns `""` so the filter hides the sentinel. (This is *why* we set it to `""` — non-empty filter would match a single-character sentinel and clutter results.)
- `TestBuildListPrependsSentinel` — `buildList` returns N+1 items for N entries; first item has `kind == kindNewWorktree`. Without the sentinel, users would have to know the `n` shortcut.
- `TestBuildListSentinelFirstWhenEmpty` — with zero real entries, the list has one item (the sentinel). The empty-repo case must still let users create worktrees from the picker.

### `internal/tui/new_worktree_test.go` — new file

- `TestValidateNewWorktreeNameRejectsEmpty` — `validateNewWorktreeName("")` returns non-empty error. Empty names would create a malformed `.worktrees/`, which git rejects but the TUI shouldn't attempt.
- `TestValidateNewWorktreeNameRejectsWhitespace` — `"   "` returns non-empty error. Pure whitespace is also malformed.
- `TestValidateNewWorktreeNameAcceptsValid` — non-empty trimmed names (including `"feature/x"`) return `""`. The validator must not over-restrict; `EnsureForName` handles deeper validation.
- `TestNewInputModelIsFocused` — `newInputModel(80).Focused() == true`. An unfocused input would require an extra tab/click, breaking the muscle-memory contract that Enter submits.
- `TestNewInputModelHasPlaceholder` — placeholder matches the documented constant. Without it, the prompt is a blank rectangle.
- `TestNewInputModelWidth` — `newInputModel(80).Width == 76`. The input must fit the terminal.
- `TestEnsureNewWorktreeCmdSuccess` — integration: in a temp git repo, run the command; assert `err == nil` and the returned path is under `.worktrees/`. This is the happy path; a regression means the feature silently does nothing.
- `TestEnsureNewWorktreeCmdGitError` — call with a name `EnsureForName` rejects (e.g. traversal); assert `err != nil` and is surfaced. This proves we propagate git errors instead of swallowing them.

### `internal/tui/app_test.go` — extend

- `TestNKeyOpensNewWorktreePhase` — `Update` with `n` keypress on `phaseList` (and `m.ready == true`) transitions to `phaseNewWorktree`. Without this, the keyboard shortcut is dead.
- `TestNKeyIgnoredWhileLoading` — same keypress with `m.ready == false` does *not* transition. The shortcut must not fire before the list is ready.
- `TestEnterOnSentinelOpensNewWorktreePhase` — `Update` with Enter when the selected item is the sentinel transitions to `phaseNewWorktree`. The sentinel must be Enter-able, not just `n`-able.
- `TestEnterOnSentinelIgnoredWhileLoading` — same with `m.ready == false` does nothing. Consistent with the existing list-unready guard.
- `TestEscOnNewWorktreeReturnsToList` — `esc` on `phaseNewWorktree` transitions back to `phaseList` and clears `m.newError`. Without this, the user is stuck in the prompt.
- `TestNewWorktreeCreatedErrorStaysOnPrompt` — `newWorktreeCreatedMsg{err: ...}` keeps `phaseNewWorktree` and sets `m.newError`. Git failures must be visible, not silent.
- `TestNewWorktreeCreatedSuccessTriggersReload` — `newWorktreeCreatedMsg{path, name}` sets `m.pendingHighlight` and dispatches `loadEntriesCmd`. After the reload, the new entry is selected.

### `internal/worktree/create_test.go` — no changes

`EnsureForName` is already well-covered. We're a consumer, not a maintainer, of that API.

## Migration / Compatibility

This is a purely additive feature. No flags change, no config schema change, no existing behavior changes. Existing TUI users who don't use `n` and don't pick the sentinel see no difference. The new sentinel item is the only visible change for default users, and it sits at the top of the list (sorted, easy to scroll past).
