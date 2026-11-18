# wt sessions and the model server

Every `wt` run that needs a model is a **client session** of a model server
(`wt-model serve`, [wt-model-server.md](wt-model-server.md)): the catalog the
pickers show, the `-M` pin, a start or stop, `wt smoke` and the spend in
`wt stats` all come from that server, and every agent reaches its model
through the server's gateway. The session reads `config.toml` only, never
`registry.toml`, LiteLLM's `config.yaml` or a local provider, and it never
reads the LiteLLM key.

A wt session never starts a model server, never guesses an address and never
falls back to anything local. It works from a stored **mode**: `native` (no
model server), `local` (a `wt-model` on this machine) or `remote` (one
elsewhere), chosen on the Model server tab of `wt config`
([wt-config.md](wt-config.md)). With no mode chosen anywhere, a command that
needs a model refuses before anything starts, and on a terminal the first
launch that needs a model opens `wt config` on that tab once (the "first-run
gate", below). With native mode chosen, a launch that asks for no model runs
the agent natively instead (see "Native launch" below). ("Client session" is
the server's record of one wt run; a "wt session" is a launch that claims a
worktree.)

## Configuration: `[model-server]`

The model server is named in `~/.config/agent-wt/config.toml` (or
`$XDG_CONFIG_HOME/agent-wt/config.toml`). The Model server tab of `wt config`
writes the table, and you can still edit it by hand:

```toml
[model-server]
mode = "remote"
url = "http://127.0.0.1:7801"
token_file = "~/.config/agent-wt/remote-model-server.token"
```

| Key | Meaning |
|---|---|
| `mode` | `native`, `local` or `remote` (case-insensitive). Absent means no mode is chosen: a command that needs a model refuses (below). A stored `local` or `remote` still needs `url` and `token_file`; with `url` or `token_file` set and no `mode` the mode reads as `remote`. Any other value refuses: `mode under [model-server] is "X"; it must be one of native, local, remote`. |
| `url` | The server's address, as `wt-model serve`'s banner prints it. |
| `token_file` | A file holding the server's bearer token, mode `0600` (a file group or other can read is refused). `~` is expanded. |
| `token` | Never used for auth. Its presence is not inert: while it is set a save writes `config.toml` 0600 (and never loosens an existing stricter mode), and a command that needs a model refuses with the inline-token problem (below) until the token moves into a `token_file` (in native mode it is ignored, with one `wt: note:` asking you to remove it). |

What the tab writes: for `remote`, the table above, with the token in a
0600 file (`~/.config/agent-wt/remote-model-server.token` unless you name
another, or paste the token and the tab writes it there); for `local`, the
same table pointing at `http://<listen>` with the token file
`~/.config/wt-model/server.token` that the `wt-model` LaunchAgent
(`local.wt-model.serve`) also uses, and the LaunchAgent itself; for
`native`, `mode = "native"` with no url or token. Doing it by hand, make a
token file once and give the server the same file (`--token-file`):

```bash
umask 077; head -c 32 /dev/urandom | base64 > ~/.config/agent-wt/remote-model-server.token
```

The server side keeps its own copy at `~/.config/wt-model/server.token`; the
split is explained in
[../../docs/guides/13-deployment-use-cases.md](../../docs/guides/13-deployment-use-cases.md).

### Environment overrides

| Variable | Overrides |
|---|---|
| `WT_MODEL_MODE` | `mode`: `native`, `local` or `remote` (case-insensitive); anything else refuses: `WT_MODEL_MODE is "X"; it must be one of native, local, remote`. Wins over everything below. |
| `WT_MODEL_URL` | `url` |
| `WT_MODEL_TOKEN_FILE` | `token_file` (and `WT_MODEL_TOKEN`) |
| `WT_MODEL_TOKEN` | the token itself, as a literal; used when `WT_MODEL_TOKEN_FILE` is unset, and wins over `token_file` |

The environment wins over the file, highest first: `WT_MODEL_URL`, then
`WT_MODEL_TOKEN_FILE`, then `WT_MODEL_TOKEN`.

The effective mode is, in order: a valid `WT_MODEL_MODE`; else `remote` when
`WT_MODEL_URL` and a token (`WT_MODEL_TOKEN_FILE` or `WT_MODEL_TOKEN`) are
both set (the pair overrides a stored `mode`, native included); else the
stored `mode`; else `remote` when any url or token is set anywhere (a
half-configured server, which refuses naming the missing setting); else
unset.

## The refusals

A command that needs a model and finds no model server refuses with one line
and exit 1. With no mode chosen anywhere (no `mode` under `[model-server]`, no
`WT_MODEL_MODE`, no url or token) it begins `no model server is configured`
and names `wt config` and `WT_MODEL_MODE`:

```
wt: no model server is configured — choose a mode: run wt config (Model server tab) or set WT_MODEL_MODE to native, local or remote; nothing was launched
```

On a terminal a launch that needs a model does not refuse at first: this is
the **first-run gate**. The first launch with the mode unset opens `wt config`
on the Model server tab, once per run and before anything is created; when
you quit with a mode chosen the launch goes on, and with none still chosen it
ends with `no model-server mode chosen — nothing was launched` (exit 1). The
gate never opens for a stored or environment mode, even an invalid one, and
never without a terminal: a script gets the refusal above.

In native mode it is `wt: no model server is configured — native mode has no
model server; change the mode in wt config; …`. In local or remote mode (or
with only a url or a token set, which reads as remote) with a setting missing,
the line starts `no model server is configured; [litellm] is no longer used by
wt`, `the model server url is not configured` or `the model server token is not
configured`, then `— set url and token_file under [model-server] in config.toml
(or WT_MODEL_URL and WT_MODEL_TOKEN_FILE), or choose the mode in wt config
(Model server tab): native, local or remote`. A server that does not answer its
ping adds the mode's way out: for local, `start it from wt config (Model
server tab) or run launchctl kickstart -k gui/<uid>/local.wt-model.serve`; for
remote, the url it tried. The last clause says what did not happen, per command:
`nothing was launched` (a launch that asks for a model: `-M`, `-T`, `-F`), `nothing was started`
(`wt start`), `nothing was stopped` (`wt stop MODEL`), `the job was not read`
or `nothing was cancelled` (`wt job`), `nothing was run` (`wt smoke`),
`nothing was printed` (`wt rotate TAG`), `nothing was listed` (`wt model
list --server`), `nothing was changed` (`wt profile slots add|set`), `no
candidates were listed` (`wt profile slots candidates`) and `nothing was
shown` (`wt profile show -M ...`).

The other refusals of a configuration that cannot be used, each exit 1 with
the same last clause:

- An inline token: `[model-server] token is not used for auth — while it is set wt writes config.toml 0600, and no save loosens an existing stricter mode — put the token in a file with mode 0600 and set token_file`.
- A token file group or other can read: `token file PATH is accessible by
  group or other (mode MODE); run 'chmod 600 PATH'`, then `check token_file
  under [model-server] in config.toml` (or `check the file
  WT_MODEL_TOKEN_FILE names` when the variable named it). A missing, empty
  or unreadable file names the path and the reason the same way.
- A server that does not answer, refuses the token, or speaks another API
  version: the address tried and the reason, never a fallback.

A `config.toml` that does not parse is the launch's `config error: ... (run
`wt config` to repair)`, also exit 1.

## Exit codes

| Code | When |
|---|---|
| 0 | done; a job that succeeded (a still-finishing stop included) |
| 1 | could not start, nothing asked and nothing changed: not configured, unreachable before anything started, wrong token, API or schema skew, unknown model, a pin that is blocked, unknown job id |
| 2 | a started thing did not finish: a failed, blocked, stopped or cancelled job, a lost server, a lost job, a detach (Ctrl-C during a start: the job keeps running), `wt job ID` of a failed or cancelled job |
| 3 | approval missing: no terminal and no flag, or the prompt was declined; nothing changed |
| agent status | a launch ends with the agent's own exit status; `wt smoke` exits 1 on a FAIL row |

A `--json` command's failure envelope carries the same code as the plain
command's failure: 1 for one that could not start, 2 for `wt job ID --json`
of a failed or cancelled job (or a cancel sent whose outcome is unknown).

## What needs the model server

These refuse, as above, when none is configured, before anything starts:

- a launch of a model-driven agent (`claude`, `codex`, `copilot`, `pi`,
  `agy`, `opencode`) that asks for a model: `-M` (`-M profile/NAME`
  included), `-T` or `-F`;
- the model picker (an agent with no `-M` on a terminal when a server is
  configured is the only case that shows one);
- `wt start`, `wt stop MODEL` (and `wt stop` with no argument), `wt job`;
- `wt smoke`, `wt rotate TAG`, `wt model list --server`;
- `wt profile slots add|set|candidates` and `wt profile show -M ...`.

A model-driven agent with no `[[agents]]` entry in `config.toml` is not an
exception: on a session every driver has an agent, eligible for every row
the server lists except another agent's native row, and an entry only
narrows that (`supported_providers`).

## Native launch: native mode

With native mode chosen (`mode = "native"` under `[model-server]`, or
`WT_MODEL_MODE=native`; a `WT_MODEL_URL` plus a token in the environment
overrides a stored native, `WT_MODEL_MODE` overrides everything), `wt -A <agent>`
and the agent picker launch a model-driven agent **natively** (#615): no model
prompt, no catalog, no client session; the agent's own configuration decides
the model (for example org-managed settings that supply the gateway and
credentials). `-W`, `--root`, `--cwd`, the launch warnings and the guard, the
worktree claim, `--yolo`, arguments after `--` and the summary line work as
for any launch. It prints one line on stderr, so a native launch is never
silent:

```
wt: native mode — launching claude natively, with its own configuration; to choose models through wt-model, change the mode in wt config
```

The agent gets no gateway origin or token (the zero route), no rotation or
usage record is written (there is no model id), and `profiles.toml` overlays
do not apply to a native launch; slot profiles cannot apply either, so a
problem in `slot-profiles.toml` is not reported on one. In the picker the line
is printed once the launch gate is past, so declining the launch says nothing.

Native is explicit: it is stored (`mode = "native"`) or asked for with
`WT_MODEL_MODE=native`, and a stored or environment native ignores any
`url`, `token_file` or `WT_MODEL_TOKEN*` value left lying around (only an
inline `token` earns one `wt: note:`, asking you to remove it). With no mode
chosen a launch refuses (above). A
**half-configured** server (url without a token, or the reverse, and no mode)
still refuses and names the missing setting, an
unreachable server or a refused token still fails fast, and a launch that asks
for a model (`-M`, `-T`, `-F`, a slot profile) still refuses with nothing
launched. A configured server is never bypassed. `wt start`, `stop`, `job`,
`smoke`, `rotate`, `model list --server` and the slot-profile commands that
need a server refuse as before. This is not the local fallback the design
rules out: a native launch uses no wt-model route and no server at all. A
bare `wt` still needs a terminal for its picker.

## What does not

These run with no model server configured:

- a command agent's launch (`-A shell`, `shell-wt`), the worktree picker for
  it included;
- a model-driven agent's launch that asks for no model, in native mode (above);
- `wt --init`, `--check-guard`, `--no-guard`, `--version`;
- `wt worktrees` (`wt cleanup`), `wt remove`, `wt rescue`, `wt restore-root`;
- `wt config` (and `wt config theme|path`);
- `wt profile list|status|on|off`, `wt profile slots rm|usage`;
- `wt stats`: the survey table and the launch counts print, the spend cells
  show `-`, and one note says why (spend status `not_configured`);
- every host command (below).

## Host commands

These act on the machine they run on, reading `registry.toml` (and
`config.yaml`) there, never the model server: `wt model init|add|edit|rm`,
`wt model list` (the registry listing; `--json` is what modeltiers reads),
`wt cloud-sync`, `wt litellm sync|list|providers|status|set|restart`, `wt
served`, `wt warm`, `wt stop <provider>` and `wt stop --all`, and the Models
tab of `wt config`. `wt worktrees` and `wt remove` read the registry too,
for the model artifacts they must never delete; with no registry at the
default path there is nothing to protect.

## `[litellm]` on a session

The proxy's `url` and `api_key` live in the `wt-model` config home
(`~/.config/wt-model/config.toml` and the 0600 file its `key_file` names):
host state `wt-model serve` reads to reach LiteLLM, written by
`wt-model litellm set --url URL --api-key KEY` and shown by
`wt-model litellm status` (the `wt litellm set|status` twins read and write
the same home). The `[litellm]` table in `config.toml` is a read-only
fallback: consulted only when the home holds no `[litellm]` table, and never
written again. A session never reads the pair, and every save of
`config.toml` keeps the table as it is. A `wt litellm status` run that resolved the pair from the fallback prints
one note (illustrative — the path is machine-specific):

```
wt: note: config.toml: [litellm] is a read-only fallback — 'wt litellm set' writes /Users/user_name/.config/wt-model/config.toml now
```

Its `enabled` key is retired: every session routes through the model
server, so nothing reads it. A `config.toml` with `enabled = true` prints,
once per process:

```
wt: note: config.toml: [litellm] enabled is no longer used and is ignored
```

and the key is kept as written. `wt litellm on` and `wt litellm off` are
hidden stubs that change nothing and exit 1:

```
wt: litellm on is retired — every session routes through the model server; set url and token_file under [model-server] in config.toml; nothing was changed
```

Nothing is migrated, and `wt` itself never starts a server or writes
`[model-server]` on a launch: only the Model server tab of `wt config` does,
on your confirmation.

## Interim limits

- **Host commands still run here.** Until the admin commands move to
  `wt-model` (Phase 4), this binary still runs the host commands above,
  reading and writing `registry.toml` and `config.yaml` on whatever machine
  runs them, and the proxy url and key live in the `wt-model` config home of
  whichever machine runs them (above; the `[litellm]` table of `config.toml`
  is its read-only fallback). On a session-only machine
  they read that machine's files, not the server's.
- **A killed wt cannot end its client session.** wt does not trap `SIGHUP`
  or `SIGTERM` sent to itself, so a terminal closed under a running agent, or
  a `kill`, ends wt before it can tell the server. The server forgets the
  client session when its heartbeats stop, after its 60 s TTL; until then
  the session's models count as in use, and the idle grace starts after.
- **A configured but unreachable server is found only at the launch's ping**,
  after `-W <name>` has already created its worktree and branch. Known limit:
  the worktree and branch then need manual cleanup.

## See also

- [wt-model-server.md](wt-model-server.md) — the server: flags, token file,
  endpoints, client sessions, auto-stop.
- [wt-start-stop.md](wt-start-stop.md), [wt-job.md](wt-job.md),
  [wt-smoke.md](wt-smoke.md), [wt-stats.md](wt-stats.md),
  [wt-config.md](wt-config.md).
- [../../docs/guides/00-config-map.md](../../docs/guides/00-config-map.md)
  (who owns which file) and
  [../../docs/guides/04-litellm-config.md](../../docs/guides/04-litellm-config.md)
  (the proxy and its routes).
