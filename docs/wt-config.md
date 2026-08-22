# `wt config`

User-level preferences for the `wt` launcher.

- **`wt config`** (no subcommand) — interactive TUI for viewing and editing
  `config.toml`
- **`wt config theme`** — color theme management
- **`wt config path`** — print the config directory
- **`wt config ollama`** — sync ollama models with `config.toml`

## Config viewer (`wt config`)

Launching `wt config` with no subcommand opens a full-screen TUI that
lets you browse and edit the three main sections of `config.toml`:
**Agents**, **Providers**, and **Models**.

### Tabs

| Shortcut | Section |
|----------|---------|
| `1` | Agents |
| `2` | Providers |
| `3` | Models |
| `Tab` | Next tab |
| `Shift+Tab` | Previous tab |

Each tab shows a scrollable list sorted for readability:
- **Agents** — commands first, then regular agents, both groups sorted by
  name. Each row shows `✓ installed` or `✗ not installed`.
- **Providers** — sorted by ID.
- **Models** — sorted by provider, then family, then model name.

### Editing rows

Press `Enter` on any row to open an edit form for that entry. Forms vary
by section:

- **Providers** — editable fields: `ID`, `Name`, `Auth Type`, `Base URL`;
  toggle `Location` with `Space`.
- **Models** — editable fields: `ID`, `Family`, `Provider ID`, `Model Name`,
  `Tags`; toggle `Location` with `Space` (empty falls back to the provider's
  location and is shown as derived).
- **Agents** — editable fields: `Name`, `Supported Providers`
  (comma-separated), `Default Provider`; `Installed` is read-only.

`Tab` / `Shift+Tab` cycles form fields. `Ctrl+S` saves the changes and
returns to the list. `Esc` cancels without saving.

### Adding new entries

Press `n` on any tab to open the add form for that section. The same
validation rules apply as when editing:
- Empty IDs are rejected.
- Duplicate model IDs are rejected.
- Provider IDs must reference an existing provider.
- Agent default providers must be in the agent's supported providers list.

### Deleting entries

Press `d` on any row to open a delete confirmation prompt:
- `y` — confirm deletion
- `n` — cancel
- `Esc` — cancel

**Deletion rules:** Providers cannot be deleted while referenced by a
model or an agent. Models and agents can always be deleted. Attempting
to delete a referenced provider shows an error explaining which model or
agent holds the reference.

### Saving changes

`Ctrl+S` from the list view writes the in-memory config to disk
atomically (temp-file + rename). If validation fails, the save is
blocked and an error is shown. The config is only written when dirty —
pressing `Ctrl+S` on a clean config is a no-op.

### Quitting with unsaved changes

`q` or `Ctrl+C` exits immediately when the config is clean. When there
are unsaved changes, a prompt appears:
- `y` — save and quit
- `n` — discard changes and quit
- `c` or `Esc` — return to the list

## Where settings live

All `wt config` settings live in `~/.config/agent-wt/` (or
`$XDG_CONFIG_HOME/agent-wt/`). The theme subcommand writes
`themes.toml`; other subcommands will add their own files alongside it.

```
~/.config/agent-wt/
├── config.toml          # main agent / model registry (existing)
├── themes.toml          # active theme (this feature)
├── rotation-*.state     # per-slot last-launched model
└── ...                  # future subcommands land here
```

Print the resolved config directory at any time:

```bash
wt config path
```

## Themes

All `wt config` settings live in `~/.config/agent-wt/` (or
`$XDG_CONFIG_HOME/agent-wt/`). The theme subcommand writes
`themes.toml`; other subcommands will add their own files alongside it.

```
~/.config/agent-wt/
├── config.toml          # main agent / model registry (existing)
├── themes.toml          # active theme (this feature)
├── rotation-*.state     # per-slot last-launched model
└── ...                  # future subcommands land here
```

Print the resolved config directory at any time:

```bash
wt config path
```

## Themes

The `wt config theme` family manages the active color theme. Themes apply
to every TUI picker (worktree list, agent+command list, model list, session
resume prompt, ollama availability warning), CLI tables
(`wt models`, `wt agents`), and the `wt config theme list` output.
The TUI threads the active theme through every `list.Model` via
`internal/tui/delegate.go`'s `themedListDelegate` — replacing the
hardcoded `list.NewDefaultDelegate` so no picker silently bypasses the
theme.

### Built-in themes

| Name           | Description                          |
|----------------|--------------------------------------|
| `default`      | Subtle, terminal-native colors       |
| `solarized`    | Warm palette inspired by Solarized   |
| `mono`         | Grayscale with a single blue accent  |
| `tokyo-night`  | Enkia's Tokyo Night (Night/Day pair) |

Each theme carries dark and light variants of every token, so the same
choice works in either terminal mode without flipping a setting.

### Commands

```bash
wt config theme               # show the active theme + available names
wt config theme list          # list all built-in themes (table)
wt config theme show          # show the active theme's tokens
wt config theme show <name>   # show a specific theme's tokens
wt config theme set <name>    # activate a theme (effective on next wt launch)
wt config theme unset         # revert to the default theme
```

`show` output (the `dark` and `light` words render in the token's own
dark/light color so the hex values stay readable):

```
solarized — warm palette inspired by Solarized

  border       #93a1a1 / #93a1a1  dark light
  error        #dc322f / #dc322f  dark light
  header       #b58900 / #b58900  dark light
  ...
```

### The themes.toml file

`wt config theme set <name>` writes a single line to
`~/.config/agent-wt/themes.toml`:

```toml
theme = "tokyo-night"
```

The file is read once at startup (in `newApp()`); the active theme does
**not** hot-reload — relaunch `wt` to see changes. A missing or empty
file falls back to the `default` theme. Unknown theme names fall back
to `default` as well, so a typo never crashes the launcher.

## Ollama model sync

`wt config ollama` launches an interactive TUI for keeping
`config.toml` ollama models in sync with the local Ollama instance
(`ollama list`).

### What it shows

The TUI presents a union of both sources — models in `config.toml`
(where `provider_id = "ollama"`) and models returned by `ollama list`.
Each row is a single line:

```
gemma4 / gemma4:9b  SYNCED  local  code, design
kimi-k2.6 / kimi-k2.6:cloud  MISSING  cloud  code, design
llama3.2:3b / llama3.2:3b  UNTRACKED  local  -
```

Three states:

| Status     | Meaning | Actions on Enter |
|------------|---------|------------------|
| `SYNCED`   | In both `config.toml` and `ollama list` | Edit the config entry |
| `MISSING`  | In `config.toml` but not in `ollama list` | Pull via `ollama pull`, or delete from config |
| `UNTRACKED`| In `ollama list` but not in `config.toml` | Add to config (edit screen with pre-filled values) |

### Edit screen

The edit screen shows read-only fields (`id`, `model_name`, `provider`)
and three editable fields:

- **family** — free text (default: model name for untracked models)
- **location** — toggle between `local` and `cloud`
- **tags** — comma-delimited (e.g. `code, design`)

Tab / Shift+Tab cycles between fields. Enter saves to `config.toml`
(atomically). Esc cancels.

### Resolve prompt (missing models)

Pressing Enter on a `MISSING` entry shows three choices:

- **Pull with ollama** — runs `ollama pull <model_name>` with live progress
  output (the TUI releases the terminal during the pull, then returns)
- **Delete from config** — removes the model from `config.toml` (no
  confirmation; the model is already unusable since it's not pulled)
- **Cancel** — return to the list

### Keybindings

| Key | Action |
|-----|--------|
| `↑/↓` | Navigate the list |
| `enter` | Edit / resolve / add (depends on status) |
| `r` | Refresh (re-read config + `ollama list`) |
| `q` / `ctrl+c` | Quit |
| `esc` | Quit from list; back from sub-screens |

### When ollama is not installed

If the `ollama` binary is not on `$PATH`, the list shows config models
only (all `MISSING`) with a status note: "ollama not found — showing
config models only". Pull attempts will fail with a clear error.

### Adding custom themes (not yet supported)

The first release ships four built-in palettes only. A future release
will let users define themes in `themes.toml` (or a separate file) so
custom palettes can be versioned alongside `config.toml`.