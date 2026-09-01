# `wt config ollama` — Ollama Model Sync TUI

## Overview

A new `wt config ollama` subcommand launches an interactive TUI that keeps
`config.toml` ollama models in sync with models pulled in the local Ollama
instance (`ollama list`). The TUI presents a union of both sources, lets the
user edit config entries, pull missing models, delete stale entries, and add
untracked ollama models to config.

## Architecture

### New package: `internal/ollamaconfig/`

A self-contained Bubble Tea program, separate from the launch TUI in
`internal/tui/`. Exports a single entry point:

```go
func Run(theme themes.Theme) error
```

**Files:**
- `ollamaconfig.go` — the `model` struct, `Init`, `Update`, `View`, and `Run`
- `sync.go` — data loading: reads config.toml ollama models, runs `ollama list`,
  computes the union with statuses
- `edit.go` — the edit screen: field inputs, Tab navigation, save logic
- `ollamaconfig_test.go` — phase transition tests
- `sync_test.go` — union computation and sorting tests
- `edit_test.go` — edit screen state tests

### Cobra wiring

`cmd/wt/commands_config.go` gains a `configOllamaCmd(a *app)` command
registered under `configCmd`. The command has no subcommands — `wt config ollama`
launches the TUI directly. The `configCmd` Long help is updated to mention the
`ollama` subcommand.

### Imports

- `internal/config` — `Load`, `Save`, `Model`, `Provider`
- `internal/registry` — `Ollama.Discover()` (existing `ollama list` parser)
- `internal/themes` — themed rendering
- `github.com/charmbracelet/bubbletea`, `bubbles/textinput`, `bubbles/list`

## Data Flow

### Loading on startup

1. `config.Load()` reads `config.toml` into `*config.Config`
2. `registry.Ollama{}.Discover()` runs `ollama list`, returns `[]config.Model`
3. Both loaded in a single `tea.Cmd` returning a `loadedMsg` with config and
   ollama model list

### Union computation

A `syncEntry` struct represents each row:

```go
type syncEntry struct {
    model  config.Model // config.toml model (zero value if not in config)
    ollama bool         // true if in ollama list
    config bool         // true if in config.toml
}
```

Keyed by `model_name` (the ollama-side identifier, e.g. `gemma4:9b`):
- Build a map from config models where `ProviderID == "ollama"`, keyed by
  `ModelName`
- Build a map from ollama list models, keyed by `ModelName`
- For each key in the union of both maps:
  - **synced** — in both. Use the config model (has user's family/tags/location).
  - **missing** — in config only. Use the config model.
  - **untracked** — in ollama only. Use the ollama-discovered model.

### Sorting

`family` ascending, then `model_name` ascending.

### Refreshing

After any action (edit, add, delete, pull), re-run the load command to get a
fresh snapshot. Config is re-read from disk; `ollama list` is re-run.

### Non-ollama models

Only models with `ProviderID == "ollama"` from config.toml are considered.
Other providers (openrouter, claude, copilot) are ignored.

## List Screen

### Layout

```
  MODEL               STATUS      FAMILY    LOCATION   TAGS
▸ gemma4:9b          ● synced    gemma4    local      code, design
  gemma4:27b         ● synced    gemma4    local      code
  kimi-k2.6:cloud     ○ missing   kimi-k2.6 cloud      code, design
  llama3.2:3b         + untracked —         —          —

[enter] edit / resolve   [r] refresh   [q] quit
```

### Implementation

A `bubbles/list` with a custom `syncItem` wrapping each `syncEntry`. Uses
`themedListDelegate` for consistent theming — this function is currently
unexported in `internal/tui`, so it will be exported (capitalized to
`ThemedListDelegate`) so both packages share a single implementation. Column
alignment via `fmt.Sprintf` with `%-Ns` padding in `Description()` (same
pattern as `internal/tui/model_list.go`).

### `syncItem` fields

- `FilterValue()` — model name
- `Title()` — model name
- `Description()` — status + family + location + tags columns via `fmt.Sprintf`
- `statusBadge()` — colored symbol: `●` (accent for synced), `○` (warning for
  missing), `+` (dim for untracked)

### Keybindings

- `↑/↓` — navigate
- `enter` — action depends on status:
  - **synced** → edit screen
  - **missing** → resolve choice list (pull / delete / cancel)
  - **untracked** → edit screen (pre-filled from ollama model)
- `r` — refresh (re-run load command)
- `q` / `ctrl+c` — quit
- `esc` — quit from list; back from sub-screens

### Status colors

- synced → `themes.TokenAccent`
- missing → `themes.TokenWarning`
- untracked → `themes.TokenDim`

## Edit Screen

### Layout

```
  Edit Model: gemma4:9b

  id          ollama/gemma4:9b          (read-only)
  model_name  gemma4:9b                 (read-only)
  provider    ollama                    (read-only)
▸ family      [gemma4]                  ← cursor
  location    [local]
  tags        [code, design]

  [tab/shift+tab] next/prev field   [enter] save   [esc] cancel
```

### State

Three `bubbles/textinput.Model` instances for `family`, `tags`, and `location`.
An `editCursor` int (0, 1, 2) tracks the focused field. Read-only fields
rendered as dimmed plain text.

### Field behaviors

- **family** — free text. Default: current family (or model name for untracked,
  matching `parseOllamaList` which sets `Family = name`).
- **location** — toggle between `local` and `cloud`. Cycles on any key press;
  Tab skips to next field. Avoids typing errors.
- **tags** — comma-delimited text. Default: current tags joined by `, ` (or
  empty for untracked). On save: split by comma, trim, drop empties (reuses
  `config.ParseFilterList` logic).

### Navigation

- `tab` — next field (wraps from tags → family)
- `shift+tab` — previous field
- `enter` — save, return to list with refresh
- `esc` — cancel, return to list

### Save logic

- **synced** models: update existing model in `cfg.Models` (matched by `ID`),
  then `config.Save(cfg)`.
- **untracked** models: construct new `config.Model` with
  `ID = "ollama/" + ModelName`, `ProviderID = "ollama"`,
  `Source = "curated"`, and user-entered family/location/tags. Append to
  `cfg.Models`, then `config.Save(cfg)`.
- After save, re-run load command to refresh list.

## Resolve Actions (Missing Models)

When Enter is pressed on a missing model, a choice list appears:

```
  Resolve: kimi-k2.6:cloud (not in ollama list)

▸ Pull with ollama     download the model via `ollama pull`
  Delete from config   remove this model from config.toml
  Cancel               return to list

  [enter] choose   [esc] back
```

### Implementation

A `bubbles/list` with `choiceItem` entries (same pattern as existing
resume/guard/ollama warning screens). A `phaseResolve` phase tracks state.

### Pull action

1. Release terminal via `tea.Program.ReleaseTerminal()`
2. Run `exec.Command("ollama", "pull", modelName)` with stdio to terminal
3. Wait for completion
4. Restore terminal via `tea.Program.RestoreTerminal()`
5. Re-run load command — model should appear as "synced"
6. On failure, show error in list screen status area

### Delete action

1. Remove model from `cfg.Models` (matched by `ID`)
2. `config.Save(cfg)` — atomic write
3. Re-run load command — model disappears from union
4. No confirmation prompt

### Cancel action

Return to list screen, no changes.

## Error Handling

- **Ollama not installed**: `Ollama.Discover()` returns `nil, nil`. List shows
  config models only, all "missing". Status message: "ollama not found —
  showing config models only". Pull action fails with clear error.
- **Config load error**: `Run()` returns error before launching TUI. Cobra
  surfaces to stderr as `wt: config error: ...`.
- **Config validation after save**: run `cfg.Validate()` before writing. If
  validation fails, show error in edit screen, don't save.
- **`ollama pull` failure**: error shown in list screen status area. Model
  remains "missing". User can retry or delete.
- **`ollama list` failure**: error shown in list screen. User can press `r` to
  retry.
- **Stale state**: every action re-reads config from disk and re-runs
  `ollama list`, ensuring the list always reflects current state.

## Testing

### `sync_test.go` — union computation and sorting

- Various combinations: all synced, some missing, some untracked, all three
  states present.
- Sorting: verify family then model_name ordering.
- Non-ollama config models excluded from union.
- Empty config and empty ollama list edge cases.
- Union function is pure — takes `[]config.Model` inputs, no I/O.

### `edit_test.go` — edit screen state

- Tab navigation cycles through fields.
- Location toggle cycles local → cloud → local.
- Tags parsing: `"code, design"` → `["code", "design"]`, empty → `[]`,
  `"  code ,  design  "` → `["code", "design"]`.
- Save for synced: existing model updated, others unchanged.
- Save for untracked: new model appended with correct ID, ProviderID, Source.
- Cancel: no config changes.

### `ollamaconfig_test.go` — phase transitions

- Enter on synced → edit phase.
- Enter on missing → resolve phase with correct choices.
- Enter on untracked → edit phase with pre-filled values.
- Esc from edit/resolve → list phase.
- `r` key → triggers refresh command.

### Mocking strategy

- Union computation: pure function, test directly with model slices.
- TUI transitions: construct `model` struct, call `Update` with key messages
  (same pattern as existing `internal/tui/*_test.go`).
- `ollama pull`: behind a function variable that tests override to avoid real
  subprocess calls.