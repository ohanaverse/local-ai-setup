# `wt config`

User-level preferences for the `wt` launcher. Shipped subcommands:
color themes and ollama model sync; future subcommands (registry editing,
etc.) slot in here without breaking changes.

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