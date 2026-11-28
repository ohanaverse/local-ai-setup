# `wt config`

User-level preferences for the `wt` launcher.

- **`wt config`** (no subcommand) — interactive TUI with two tabs: Agents
  and Mode (the agents in `config.toml`, and how this machine's sessions
  reach a model server; the models in `registry.toml` are `wt-model model`'s,
  below)
- **`wt config theme`** — color theme management
- **`wt config path`** — print the config directory

## Config viewer (`wt config`)

Launching `wt config` with no subcommand opens a full-screen TUI with two
tabs, named on its first line; `Tab` switches between them.

- **Agents** — the agents in `config.toml`. Edits are held in memory and
  saved with `Ctrl+S`, from this tab. The sections below describe it.
- **Mode** — the mode this machine's sessions use (`native`, `remote`, or
  `local`, which points at `wt-model setup`). See "Mode tab" below.

`wt config` needs no model server and reads no `registry.toml`: it opens, and
edits `config.toml`, with none configured. Its save checks only the session
rules (no agent with an empty name, no two agents with one name); an agent
`wt` only infers (one with no entry) is never written.

The models in `registry.toml` are edited on the host, on the Models tab of
`wt-model model` ([`wt-model.md`](wt-model.md), "The Models tab"), and this
machine's local server is set up with `wt-model setup`
([`wt-model-server.md`](wt-model-server.md), "`wt-model setup`": the
LaunchAgent, the token file, the LiteLLM checks and key actions).

### Mode tab

The tab shows the stored mode and where it comes from (stored, or a
`WT_MODEL_MODE` override), the url, the token file (`ok`, `loose` or
`missing`), the server's answer and any problems, and the choice of mode. It
is where `wt` writes the `[model-server]` table of `config.toml` (`mode`,
`url`, `token_file`; [`wt-session.md`](wt-session.md)). The mode is chosen
from `native local remote`, in that order. Every change is a plan listed
first (`a` plans, `c` checks without planning): nothing is written until you
answer `y` to `Apply this plan? [y/N]` (default no).

- `native` stores `mode = "native"` and no url or token: wt launches agents
  with their own configuration and uses no model server.
- `remote` stores the url of a server elsewhere and its token, from a token
  file or a pasted token that the tab writes (0600) to
  `~/.config/agent-wt/remote-model-server.token`. The entered url is pinged
  with the entered token first; a failed check can be saved anyway. A url
  holding an `@` anywhere is refused, drawn only as
  `(hidden: the url contains an @)` and never pinged.
- `local` is a pointer, offered on every platform: it shows no url or token
  fields and stores `mode = "local"` with the default url
  (`http://127.0.0.1:8787`) and token file (`~/.config/wt-model/server.token`).
  A stored url and token file are kept only when the stored mode is already
  `local`: switching from remote writes the defaults, so a remote machine's
  url never becomes local's. It sets nothing
  up on the machine; the tab says "run `wt-model setup` to set up wt-model on
  this machine". When `wt-model` is on `PATH`, applying local then asks
  ``wt-model is on PATH: run `wt-model setup` now? [y/N]`` (default no): `y`
  runs `wt-model setup` once the editor has closed, on the same terminal.

Two keys of the table are hand-edited only. The tab keeps them as written
and never sets them:

- `ca_file` (or `WT_MODEL_CA_FILE`, which wins): a PEM certificate file this
  machine trusts for an `https://` server, such as a copy of a self-signed
  server's certificate. Write it absolute or as `~/...`: a relative
  `ca_file` in `config.toml` is listed as a problem and an `https://` url
  is not pinged. The tab pings an `https://` url trusting it; for an `http://` url
  it is ignored. When this machine's own wt-model serves TLS, the local plan
  writes `https://` and, if `ca_file` is unset, carries a note naming the
  line to add (the `tls_cert` path). The plan also swaps only the scheme of
  a stored loopback url when the host's TLS setting changed, and notes that
  serve must be restarted on the host.
- `subscription = true`: this machine's opt-in to sending its own claude.ai
  login to the model server for a Claude subscription model
  ([`wt-session.md`](wt-session.md#claude-subscription-header-mode)).
  The subscription is the user's own login only: never share or pool it
  through a model server. A remote server over plain `http://` cannot be
  used for it (the login would cross the network in clear); an `https://`
  url (the server's native TLS) or a tunnel arriving on loopback can.

Leaving local (for native or remote) from this tab never touches wt-model's
LaunchAgent: the plan notes it untouched. When the last check said the local
server answers, the note grows to name it and the step that resolves the
conflict (an illustrative example; `http://127.0.0.1:8787` there is the
answer's url):

``wt-model's LaunchAgent is untouched; the local server answers at
http://127.0.0.1:8787 and will be unused. next: stop it with x on
`wt-model setup`, or apply native there to remove the LaunchAgent``

Without an answering check, the short form
``wt-model's LaunchAgent is untouched; remove it with `wt-model setup` ``
stands.

Keys (the footer names the ones that apply): `left`/`right`/`space` choose the
mode, `down` and `enter` move through its fields, `a` plans and applies, `c`
checks (reads only), `r` re-reads, `tab` switches tabs and `q` quits. The tab
is a form, so the text fields take typed keys until `esc`.

When `config.toml` does not parse, the tab says so with the parser's message
and writes nothing; fix the file and press `r`. The same holds for a key that
does not decode, inside `[model-server]` or elsewhere in the file (a
wrong-typed agent key): the tab lists it and every write key says why nothing
was written. It also opens by itself on a terminal when a launch finds no
mode chosen (the first-run gate in [`wt-session.md`](wt-session.md));
quitting the editor there goes back to the launch, after `wt-model setup`
when the tab asked for it (a setup that fails prints a warning and the launch
goes on).

### Agent list

The scrollable list includes commands first, then regular agents, both
groups sorted by name. Each row shows the agent's name and one of
`✓ installed`, `✗ not installed`, or `✗ not configured`.

> **`wt config`'s "not configured" marker is not the same as launch
> eligibility.** This editor exists to add the missing `config.toml` entry,
> so a `✗ not configured` row here is exactly what it says. But a launch
> needs no entry: eligibility is derived from the model row, never from the
> entry (every non-native row goes to every agent but agy, an agent's own native row
> to it alone, a Claude-subscription row to claude only), and an `[[agents]]`
> entry marks an agent as configured and nothing else. Only an uninstalled
> agent is truly blocked in the launcher.

### Editing rows

Press `Enter` on any row to open an edit form for that entry. Forms vary
by entry:

- **Agents** — the editable field is `Name`; `Installed` is read-only. The
  form has no provider fields: an entry holds only its name. The
  `supported_providers` and `default_provider` keys that older builds wrote are
  no longer read or written; a `config.toml` that still has them loads, and the
  keys disappear at the next save of `config.toml` (this tab, the Mode tab or
  `wt-model setup`). A re-saved file has agent entries with only `name`, which a
  build older than the eligibility change (#763, PR 1) cannot load: run
  `make install` from a root on `main` on every machine before saving.

`Tab` / `Shift+Tab` cycles form fields. `Ctrl+S` saves the changes and
returns to the list. `Esc` cancels without saving.

### Adding new entries

Press `n` to open the add form for an agent. The same validation rules
apply as when editing:
- Empty agent names are rejected.
- Two agents with one name are rejected.

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

`Esc` on the Agents list does not quit: it clears a filter (`/`) and does
nothing else. Only `q` and `Ctrl+C` leave the editor.

## Where settings live

All `wt config` settings live in `~/.config/agent-wt/` (or
`$XDG_CONFIG_HOME/agent-wt/`). The theme subcommand writes
`themes.toml`; other subcommands will add their own files alongside it.

> **The Agents tab's save writes only the agents and `default_tag`.** The
> `[model-server]` table (`mode`, `url`, `token_file`;
> [`wt-session.md`](wt-session.md)) is written by the Mode tab and
> `wt-model setup`, as the plan each shows, and a save keeps it as it is,
> including the hand-edited `ca_file` and `subscription` keys.
> The proxy's url and key
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
