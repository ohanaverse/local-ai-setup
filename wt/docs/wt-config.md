# `wt config`

User-level preferences for the `wt` launcher.

- **`wt config`** (no subcommand) — interactive TUI with three tabs: the agents
  in `config.toml`, the models in `registry.toml`, and the model server
- **`wt config theme`** — color theme management
- **`wt config path`** — print the config directory

## Config viewer (`wt config`)

Launching `wt config` with no subcommand opens a full-screen TUI with three
tabs, named on its first line; `Tab` switches between them.

- **Agents** — the agents in `config.toml`. Edits are held in memory and
  saved with `Ctrl+S`, from this tab. The sections below describe it.
- **Models** — the models in `registry.toml`: every registry model and every
  local model found on this machine. `n` adds a model, `Enter` edits the
  selected one (or registers a `new` row) in a form, and `d` removes it.
  Each change is written at once, and the LiteLLM routes are synced once
  when you quit. `wt model` opens the editor on this tab; its keys, columns
  and form are in [`wt-model.md`](wt-model.md). This tab is a host screen:
  it reads and writes this machine's registry and `config.yaml`, not the
  model server's, until it moves to `wt-model` ([`wt-session.md`](wt-session.md),
  "Interim limits").
- **Model server** — the mode this machine's sessions use (`native`, `local`
  or `remote`), checked against the `wt-model` server and its LiteLLM proxy.
  See "Model server tab" below.

`wt config` needs no model server: it opens, and edits `config.toml`, with
none configured.

### Model server tab

The tab shows the stored mode, what the machine actually has (the server's
answer, the LiteLLM proxy for a local server, the token file) and the choice
of mode. It is the one place `wt` writes the `[model-server]` table of
`config.toml` (`mode`, `url`, `token_file`; [`wt-session.md`](wt-session.md)),
and every change is a plan listed first: nothing is written until you answer
`y` to "Apply this plan?" (default no).

- `native` stores `mode = "native"` and no url or token: wt launches agents
  with their own configuration and uses no model server.
- `local` sets up a `wt-model` on this machine: a 0600 token file
  (`~/.config/wt-model/server.token` unless you name another), wt-model's
  LiteLLM pair, the `local.wt-model.serve` LaunchAgent listening on a loopback
  address (`127.0.0.1:8787` unless you name another), then the table. macOS
  only; a plist wt did not write is never overwritten.
- `remote` stores the url of a server elsewhere and its token, from a token
  file or a pasted token that the tab writes (0600) to
  `~/.config/agent-wt/remote-model-server.token`. The entered url is pinged
  first; a failed check can be saved anyway.

Keys (the footer names the ones that apply): `left`/`right`/`space` choose the
mode, `down` and `enter` move through its fields, `a` plans and applies, `c`
checks (reads only), `r` re-reads, `s` / `x` / `R` start, stop or restart the
local server, `tab` switches tabs and `q` quits. The tab is a form, so the
text fields take typed keys until `esc`.

When `config.toml` does not parse, the tab says so with the parser's message
and writes nothing; fix the file and press `r`. It also opens by itself on a
terminal when a launch finds no mode chosen (the first-run gate in
[`wt-session.md`](wt-session.md)); quitting the editor there goes back to the
launch.

### Agent list

The scrollable list includes commands first, then regular agents, both
groups sorted by name. Each row shows the agent's supported providers and
one of `✓ installed`, `✗ not installed`, or `✗ not configured`.

> **`wt config`'s "not configured" marker is not the same as launch
> eligibility.** This editor exists to add the missing `config.toml` entry,
> so a `✗ not configured` row here is exactly what it says. But a launch
> needs no entry: every model-driven agent with none is eligible for every
> model the model server lists except another agent's native row, and an
> entry only narrows that (its `supported_providers`). Only an uninstalled
> agent is truly blocked in the launcher.

### Editing rows

Press `Enter` on any row to open an edit form for that entry. Forms vary
by entry:

- **Agents** — editable fields: `Name`, `Supported Providers`
  (comma-separated), `Default Provider`; `Installed` is read-only.

`Tab` / `Shift+Tab` cycles form fields. `Ctrl+S` saves the changes and
returns to the list. `Esc` cancels without saving.

### Adding new entries

Press `n` to open the add form for an agent. The same validation rules
apply as when editing:
- Empty agent names are rejected.
- At least one supported provider is required.
- Agent default providers must be in the agent's supported providers list.

### Deleting entries

Press `d` on any row to open a delete confirmation prompt:
- `y` — confirm deletion
- `n` — cancel
- `Esc` — cancel

Only configured regular agents can be deleted. Command agents and
registered-but-unconfigured agents are ignored by the delete action.

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

If that save fails (validation, or the write itself), the editor does not
quit: the prompt goes and the Agents tab is shown, with the reason on its
status line, whichever tab `q` was typed on.

The prompt is about agent edits only: a change made on the Models tab is
already in `registry.toml` when it is made.

`Esc` on the Agents or Models list does not quit: it clears a filter (`/`) and does
nothing else. Only `q` and `Ctrl+C` leave the editor.

## Where settings live

All `wt config` settings live in `~/.config/agent-wt/` (or
`$XDG_CONFIG_HOME/agent-wt/`). The theme subcommand writes
`themes.toml`; other subcommands will add their own files alongside it.

> **The Agents tab's save writes only the agents and `default_tag`.** The
> `[model-server]` table (`mode`, `url`, `token_file`;
> [`wt-session.md`](wt-session.md)) is written by the Model server tab alone,
> as the plan it shows, and a save keeps it as it is. The proxy's url and key
> are not `config.toml` settings: they live in the `wt-model` config home
> (`~/.config/wt-model/`), which the model server reads; `wt-model litellm
> status` / `wt-model litellm set` show and change them (the `[litellm]`
> table here is their read-only fallback). Its `enabled` key is retired and
> ignored (one note per process when it is `true`) and `wt litellm on` /
> `off` are retired stubs that exit 1.

> **The `[launch]` table is not a `wt config` setting either.** Its
> `exit_prompt` key in `config.toml` is tolerated and ignored — the exit
> prompt it once configured is removed — and a non-empty value prints one
> note per process: `wt: note: config.toml: [launch] exit_prompt is no
> longer used and is ignored`. The table is edited by hand; the editor saves
> only the agents and `default_tag`, and wt keeps the table across a save. See
> [configuration.md](./configuration.md#exit-prompt-setting).

```
~/.config/agent-wt/
├── config.toml          # main agent registry (existing)
├── themes.toml          # active theme (this feature)
├── rotation.state       # the last single-model launch: the rotation position
├── last-picked.state    # the row launched last, model or slot profile: the picker's marker
├── slot-profiles.toml   # slot profiles; hand-editable, written by wt only via `wt profile slots`
└── ...                  # future subcommands land here
```

Print the resolved config directory at any time:

```bash
wt config path
```

## Themes

The `wt config theme` family manages the active color theme. Themes apply
to every TUI picker (worktree list, agent+command list, model list,
ollama availability warning), CLI tables, and the `wt config theme list` output.
The TUI and the `wt config` editor thread the active theme through every
`list.Model` via `themes.ListDelegate` (`internal/themes/styles.go`) — replacing the
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

### Adding custom themes (not yet supported)

The first release ships four built-in palettes only. A future release
will let users define themes in `themes.toml` (or a separate file) so
custom palettes can be versioned alongside `config.toml`.
