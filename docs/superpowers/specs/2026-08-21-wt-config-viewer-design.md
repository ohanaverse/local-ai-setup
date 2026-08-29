# `wt config` viewer-and-editor — design

## Summary

Replace `wt models` and `wt agents` with a single interactive viewer-and-editor
launched by `wt config` (no subcommand). The new tool shows the contents of
`config.toml` as three browseable, editable lists: **Agents**, **Providers**,
**Models**. Each row supports view, edit, add, and delete. Foreign-key
references between sections are validated on save and on delete.

`wt models` and `wt agents` are removed. All other `wt config` subcommands
(`theme`, `path`, `ollama`) are unchanged.

## Goals

- One place to inspect and edit `config.toml`'s registry sections
  (agents/providers/models).
- Reuse the existing theme + list-delegate infrastructure (`ThemedListDelegate`
  in `internal/tui/delegate.go`) so the viewer fits visually with the main
  worktree/agent/model picker and `wt config ollama`.
- Atomic, validated saves via the existing `config.Save()` /
  `config.WriteFileAtomic()` plumbing.
- Match the per-agent `installed` annotation already shown in the agent+command
  picker (`wt config ollama` and `internal/tui/phaseAgent`).

## Non-goals

- **No live model discovery in the viewer.** The viewer is a strict config.toml
  view. The merged `registry.Discover(...)` output (Ollama + OpenRouter) is
  not overlaid. `wt config ollama` remains the place to manage discovered
  entries.
- **No `source` column, no "pulled locally" status.** The model rows show
  only what's in `config.toml`. Ollama lifecycle (pull, delete, sync) stays
  in `wt config ollama`.
- **No cascade delete.** Deleting a provider that is referenced by any model
  or agent fails with a clear error. Users must remove the references first.
- **No editing of non-registry fields in `config.toml`** (e.g. `default_tag`).
- **No editing of `wt config ollama`'s behavior.** That subcommand is
  unchanged.
- **No editing of bash shim behavior.** `bin/*-wt` continue to forward to
  `wt --agent <name>`; the shims never invoked `wt models` / `wt agents`.

## Command surface

| Command                | Behavior                                                                                       |
| ---------------------- | ---------------------------------------------------------------------------------------------- |
| `wt config`            | Launches the viewer-and-editor TUI.                                                            |
| `wt config theme`      | Unchanged — prints active theme + available names.                                             |
| `wt config theme list/show/set/unset` | Unchanged.                                                                       |
| `wt config path`       | Unchanged — prints the config directory.                                                       |
| `wt config ollama`     | Unchanged — launches the existing sync TUI.                                                    |
| `wt models`            | **Removed.**                                                                                   |
| `wt agents`            | **Removed.**                                                                                   |
| `wt rotate <tag>`      | Unchanged (hidden debug).                                                                      |

### Removed code

- `cmd/wt/commands.go` — `modelsCmd` and `agentsCmd` are deleted.
- `wt models`'s alias `model` is removed (no alias existed for `wt agents`).
- `cmd/wt/main.go` — `modelsCmd(a), agentsCmd(a)` removed from the cobra root
  command's `AddCommand` call. The line currently reads
  `cmd.AddCommand(modelsCmd(a), agentsCmd(a), rotateCmd(a), configCmd(a))` and
  becomes `cmd.AddCommand(rotateCmd(a), configCmd(a))`.
- `registry.Discover` is still used by `wt config ollama` (via
  `internal/ollamaconfig/sync.go`); the `internal/registry` package is
  unchanged.

### Help text

`wt config`'s `Long` field gains a sentence: "With no subcommand, opens an
interactive viewer for the agents, providers, and models sections of
`config.toml`."

## TUI shape

The viewer-and-editor is a single bubble-tea program in a new package
`internal/configeditor/`, parallel to `internal/ollamaconfig/`.

### Top-level chrome

```
┌─ wt config — ~/.config/agent-wt/config.toml ───────────────┐
│                                                              │
│  [1 Agents (5)]   [2 Providers (4)]   [3 Models (12)]       │  ← tabs
│                                                              │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  agy         ollama                       ✓ installed │   │  ← row
│  │  claude      claude, agy                  ✓ installed │   │
│  │  codex       codex, agy                   ✗ not installed
│  │  pi          ollama                       ✓ installed │
│  │  shell       -                            ✓ installed │
│  └──────────────────────────────────────────────────────┘   │
│                                                              │
│  ↑/↓ select · enter edit · a add · d delete · tab next · q quit
└──────────────────────────────────────────────────────────────┘
```

- The tab bar is highlighted with the active theme's accent token.
- Each tab shows the section name and a count of rows in that section.
- The list uses `ThemedListDelegate` so the active theme applies consistently.

### Sort orders

- **Agents:** commands first — names for which `agents.IsCommand(name)` is
  true (currently `shell`) — then configured agents alphabetically by name.
- **Providers:** by `id`, ascending.
- **Models:** by `(provider_id, family, model_name)`, ascending.

### Keybindings (top level)

| Key                     | Action                                                              |
| ----------------------- | ------------------------------------------------------------------- |
| `Tab` / `→`             | Next tab.                                                           |
| `Shift-Tab` / `←`       | Previous tab.                                                       |
| `1` / `2` / `3`         | Jump to Agents / Providers / Models.                                |
| `↑` / `↓` / `j` / `k`   | Move cursor within the active tab's list.                           |
| `Enter`                 | Open edit screen for the highlighted row.                           |
| `a`                     | Open add screen for the active section (empty row template).        |
| `d`                     | Open delete-confirmation prompt for the highlighted row.            |
| `Ctrl-S`                | Save all in-memory changes to disk (atomic).                        |
| `?`                     | Full help view (mirrors `bubbles/list` convention).                 |
| `q` / `Ctrl-C`          | Quit (prompts on dirty; see Persistence).                           |

### Edit / add screens

Each section has a per-row form. All forms share the same chrome:

1. Render a vertical list of fields with the current value.
2. Highlight the active field.
3. `Tab` / `Shift-Tab` / `↑` / `↓` move between fields.
4. `Enter` on a text field opens a small textinput modal (themed).
5. `Enter` on a choice field opens a list picker.
6. `Ctrl-S` saves the row, `Esc` cancels back to the list.

#### Provider form

| Field          | Input                                                | Notes                                              |
| -------------- | ---------------------------------------------------- | -------------------------------------------------- |
| `id`           | text                                                 | Required, unique. Immutable on edit (read-only).   |
| `location`     | choice: `local` / `cloud`                            |                                                    |
| `auth.type`    | choice: `none` / `api_key` / …                       | Whatever the TOML schema supports today.           |
| `auth.base_url`| text                                                 | Required for cloud providers; empty for local.     |

#### Model form

| Field          | Input                                                | Notes                                              |
| -------------- | ---------------------------------------------------- | -------------------------------------------------- |
| `id`           | text                                                 | Required, unique. Immutable on edit.               |
| `family`       | text                                                 | Free-form (e.g. `gemma4`).                         |
| `provider_id`  | picker over existing providers                       | FK; required.                                      |
| `location`     | read-only (derived from `provider_id` via `config.ResolveLocation`) | Not directly editable.              |
| `tags`         | comma-separated text input, parsed into `[]string`   | Existing parser handles this.                      |

#### Agent form

| Field                  | Input                                                       | Notes                                            |
| ---------------------- | ----------------------------------------------------------- | ------------------------------------------------ |
| `name`                 | text                                                        | Required, unique. Immutable on edit.             |
| `supported_providers`  | multi-select picker over existing providers                 | At least one required (mirrors `validate()`).    |
| `default_provider`     | picker over the chosen `supported_providers`                | Optional; empty allowed.                         |
| `installed` (read-only)| derived from `agents.Installed(name)`                       | `✓ installed` / `✗ not installed`. Informational.|

#### Validation

- Each field validates on blur and on save attempt.
- Errors render in the form footer (e.g. `provider id is empty`,
  `provider "agy" not found`).
- Saving with errors is blocked; the cursor jumps to the first invalid
  field.

#### Add vs edit

- Add opens the form with empty fields and a placeholder `id`/`name`
  (e.g. `new-agent-3`).
- Edit opens the form pre-populated with the row's current values.
- Both go through the same form component with an `isNew bool`.

### Delete

`d` on a highlighted row opens a small inline confirmation at the bottom of
the list:

```
delete agent "agy"? [y/N]
```

- `y` runs the FK check (below) and either saves or prints an error in the
  footer.
- Any other key (including `n` and `Esc`) cancels.

### Persistence

- All edits live in an in-memory `*config.Config` (`cfg *config.Config` field
  on the TUI model, initially loaded via `config.Load()`).
- The TUI tracks a `dirty bool`, flipped to `true` on any field change, back
  to `false` on successful save or after discarding changes.
- On save:
  1. Run `config.ValidateAll()`. If errors, render them and abort.
  2. Call `cfg.Save()` (uses `config.WriteFileAtomic` — temp file + rename).
  3. On success, clear `dirty`, return to the list with a `saved` flash in
     the footer.
  4. On error (disk full, permission denied, etc.), keep `dirty=true`, render
     the error in the footer, stay on the form.
- On quit with `dirty=true`: prompt `unsaved changes — save? [y/N/cancel]`:
  - `y` — same as `Ctrl-S`.
  - `N` — discard, exit.
  - `c` / `Esc` — return to the viewer.

Atomic write is non-negotiable: `config.Save()` already does this; the TUI
just calls it.

### Concurrent edits

Out of scope. If another process edits `config.toml` while the TUI is open,
the TUI's in-memory copy wins on save (overwriting the other edit). This
matches `wt config ollama`'s current behavior.

### Theming

The viewer reuses `ThemedListDelegate` from `internal/tui/delegate.go` for
all three section lists (mirrors `internal/ollamaconfig/edit.go` and
`internal/tui/model_list.go`). The tab bar, form chrome, and footer all use
the active theme's tokens via `lipgloss.AdaptiveColor`.

## FK validation and deletion rules

### On save (any row)

- Run `config.Validate()` on the in-memory config.
- If validation fails, render the error message in the form footer and refuse
  to save the row (cursor jumps to the first invalid field).

### On delete

- **Provider delete:** check the in-memory config for any model whose
  `provider_id` matches, or any agent whose `supported_providers` or
  `default_provider` references it. If any references exist, refuse with
  `cannot delete provider "<id>": referenced by model "<m>" and agent "<a>"`
  (full list). No force / cascade.
- **Agent delete:** no other rows reference agents today (models reference
  providers, not agents). Deletion always succeeds after the confirmation
  prompt.
- **Model delete:** no other rows reference models today. Deletion always
  succeeds after the confirmation prompt.

### On FK picker

- The picker only lists providers / agents currently in the in-memory config.
- If the user is editing an existing row that references a provider they're
  about to delete, the delete is blocked until the row is updated.
- New rows can't reference providers that don't exist (the picker doesn't
  offer them).

### Cascade

Explicitly not supported. If a user wants to remove a provider and its
dependents, they edit each dependent row first, then delete the provider.
Documented in the delete-confirmation prompt when a reference is found.

## Package layout

```
internal/configeditor/
  editor.go         # Run(theme) entry point, model struct, top-level Update/View
  tabs.go           # tab state, key handling (Tab/Shift-Tab/1/2/3)
  agents_tab.go     # agents list rendering, sort (commands-first)
  providers_tab.go  # providers list rendering, sort (by ID)
  models_tab.go     # models list rendering, sort (provider, family, name)
  provider_form.go  # add/edit form for providers
  model_form.go     # add/edit form for models
  agent_form.go     # add/edit form for agents
  delete.go         # delete-confirmation prompt + FK check
  save.go           # validate + atomic save + dirty tracking
  editor_test.go    # package tests (mirrors ollamaconfig tests)
  agents_tab_test.go
  providers_tab_test.go
  models_tab_test.go
  provider_form_test.go
  model_form_test.go
  agent_form_test.go
  delete_test.go
  save_test.go
```

This mirrors `internal/ollamaconfig/`'s shape (one file per concern, tests
colocated).

### `installed` annotation

For each row in the agents tab, the TUI looks up `agents.Installed(name)`
(from `internal/agents/agents.go`) and renders `✓ installed` / `✗ not
installed` next to the name. Commands (`shell`) are always `installed` (the
shell driver has no binary to check — same as today's `wt agents`).

### Removal of `commands.go` content

`modelsCmd` and `agentsCmd` are deleted from `cmd/wt/commands.go`.
`rotateCmd` stays (it's the hidden debug command). The `modelsCmd` function
and its `registry.Discover(a.cfg)` call are removed — `registry.Discover` is
still used by `wt config ollama`'s sync logic, so the `internal/registry`
package is unchanged.

### Main.go wiring

`cmd/wt/main.go` line 257 currently registers `modelsCmd(a), agentsCmd(a),
rotateCmd(a), configCmd(a)`. It becomes `rotateCmd(a), configCmd(a)`.
`configCmd` is unchanged — its `Long` description gets a sentence added:
"with no subcommand, opens an interactive viewer for agents, providers,
and models."

### Helpers

`renderTable` (already in `cmd/wt/helpers.go`) is used by `wt config theme
list` today — the new TUI doesn't need it (it uses bubble-tea components
directly). No new shared helpers.

## Testing

The project's test convention (per CLAUDE.md and
`docs/go-course/lesson-18-testing.md`): every `Test*` function has a
top-level `// what/why` block explaining what it tests and the user-facing
consequence of a regression.

### Test files (in `internal/configeditor/`)

- `editor_test.go` — `Run()` entry point, tab navigation, quit-without-changes,
  quit-with-unsaved prompt.
- `agents_tab_test.go` — sort order (commands-first), `installed` annotation,
  list rendering.
- `providers_tab_test.go` — sort order, list rendering.
- `models_tab_test.go` — sort order (provider/family/name), list rendering.
- `provider_form_test.go` — add, edit, validation errors, save success.
- `model_form_test.go` — add, edit, FK picker shows only existing providers,
  validation errors.
- `agent_form_test.go` — add, edit, multi-select for `supported_providers`,
  `default_provider` constrained to selected providers, `installed`
  read-only.
- `delete_test.go` — provider FK block with full list, agent/model unblocked
  deletes, confirmation prompt cancel.
- `save_test.go` — atomic write, validation gates save, dirty tracking,
  save-fail keeps dirty.

### Fixture pattern

Tests build a `*config.Config` in-memory by constructing providers, models,
and agents directly (no on-disk fixtures for unit tests). Save tests use
`t.TempDir()` plus a stubbed `config.Dir`, mirroring how
`internal/themes/themes_test.go` and `internal/ollamaconfig/sync_test.go`
handle filesystem dependencies.

### Coverage targets

Match the existing per-package coverage (CLAUDE.md table). `internal/ollamaconfig/`
ships 33 tests across 4 files; the new package should hit similar density per
concern. Every public behavior gets a test: tab nav, sort, FK validation,
delete blocks, atomic save, dirty prompt.

### Manual smoke test

Document a manual test in `docs/wt-config.md`. The current `make test` is
bash-only and doesn't cover the TUI; keep that as-is.

## Documentation updates

- **`docs/wt-config.md`** — add a "viewer" section: `wt config` (no
  subcommand) opens the interactive viewer; describe the three tabs, sort
  orders, keybindings (Tab/Shift-Tab, Enter, a, d, Ctrl-S, q), and the
  relationship to `wt config ollama` (which remains the ollama-specific edit
  tool). Add a "deletion rules" subsection: providers can't be deleted while
  referenced by a model or agent; agents and models always can.
- **`CLAUDE.md`** — update the test-coverage table row for the new
  `internal/configeditor/` package (estimated ~30-40 tests); update the
  `wt models` / `wt agents` references in the "Key flags" / "Architecture"
  sections to point to `wt config` instead.
- **`docs/go-course/`** — no changes. This is a feature add, not a new
  teaching lesson.
- **`docs/superpowers/plans/`** — the implementation plan lands here (per
  the brainstorming process, the next step after spec approval is
  `writing-plans`).
- **`docs/superpowers/specs/`** — this file.

## Migration

No data migration. Users run `wt config` instead of `wt models` / `wt agents`.
The output differs:

- Per Q1-B: `installed` annotation is added inline to agent rows.
- Per Q3-A: no live discovery merge; discovered-only models don't appear.
- Per Q7-A: no `source` column, no `ollama list` overlay.

If any users had `wt models` or `wt agents` scripted, they'll get
`unknown command` errors from cobra and need to switch to `wt config`.

## Open follow-ups (out of scope for this design)

- Folding `wt config ollama` into the new viewer as a "filter to ollama
  models" view, or a tab. The ollama TUI's pull/delete operations could be
  reused via `internal/ollamaconfig` as a library.
- Cascade delete (rare; dangerous; documented as not supported).
- Editing `default_tag` and other non-registry fields.
- Concurrent-edit detection (file mtime check on `Run()` entry, warn if
  config.toml changed since the TUI opened).
