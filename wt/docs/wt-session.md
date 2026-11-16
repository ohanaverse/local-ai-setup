# wt sessions and the model server

Every `wt` run that needs a model is a **client session** of a model server
(`wt-model serve`, [wt-model-server.md](wt-model-server.md)): the catalog the
pickers show, the `-M` pin, a start or stop, `wt smoke` and the spend in
`wt stats` all come from that server, and every agent reaches its model
through the server's gateway. The session reads `config.toml` only, never
`registry.toml`, LiteLLM's `config.yaml` or a local provider, and it never
reads the LiteLLM key.

wt never starts a model server, never guesses an address and never falls
back to anything local. With none configured, a command that needs a model
refuses before anything starts. ("Client session" is the server's record of
one wt run; a "wt session" is a launch that claims a worktree.)

## Configuration: `[model-server]`

The model server is named in `~/.config/agent-wt/config.toml` (or
`$XDG_CONFIG_HOME/agent-wt/config.toml`), by hand:

```toml
[model-server]
url = "http://127.0.0.1:7801"
token_file = "~/.config/agent-wt/model-server.token"
```

| Key | Meaning |
|---|---|
| `url` | The server's address, as `wt-model serve`'s banner prints it. |
| `token_file` | A file holding the server's bearer token, mode `0600` (a file group or other can read is refused). `~` is expanded. |
| `token` | Not read. `config.toml` is not private, so a token here is a problem the session reports instead of using. |

Make a token file once and give the server the same file
(`--token-file`):

```bash
umask 077; head -c 32 /dev/urandom | base64 > ~/.config/agent-wt/model-server.token
```

### Environment overrides

| Variable | Overrides |
|---|---|
| `WT_MODEL_URL` | `url` |
| `WT_MODEL_TOKEN_FILE` | `token_file` (and `WT_MODEL_TOKEN`) |
| `WT_MODEL_TOKEN` | the token itself, as a literal; used when `WT_MODEL_TOKEN_FILE` is unset, and wins over `token_file` |

The environment wins over the file, highest first: `WT_MODEL_URL`, then
`WT_MODEL_TOKEN_FILE`, then `WT_MODEL_TOKEN`.

## The refusals

A command that needs a model and finds no endpoint refuses with one line and
exit 1. With neither the url nor the token set:

```
wt: no model server is configured; [litellm] is no longer used by wt — set url and token_file under [model-server] in config.toml (or WT_MODEL_URL and WT_MODEL_TOKEN_FILE); start a server yourself with 'wt-model serve --listen ADDR --token-file PATH', because wt never starts one; nothing was launched
```

With one of them set, the line starts `the model server url is not
configured` or `the model server token is not configured` instead, and goes
on the same way. The last clause says what did not happen, per command:
`nothing was launched` (a launch, `-M`, the pickers), `nothing was started`
(`wt start`), `nothing was stopped` (`wt stop MODEL`), `the job was not read`
or `nothing was cancelled` (`wt job`), `nothing was run` (`wt smoke`),
`nothing was printed` (`wt rotate TAG`), `nothing was listed` (`wt model
list --server`), `nothing was changed` (`wt profile slots add|set`), `no
candidates were listed` (`wt profile slots candidates`) and `nothing was
shown` (`wt profile show -M ...`).

The other refusals of a configuration that cannot be used, each exit 1 with
the same last clause:

- An inline token: `[model-server] token is not read from config.toml, which
  is not private — put the token in a file with mode 0600 and set
  token_file`.
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
  `agy`, `opencode`), with or without `-M` (`-M profile/NAME` included);
- the pickers (`wt` with no `-A`, or an agent with no `-M` on a terminal);
- `wt start`, `wt stop MODEL` (and `wt stop` with no argument), `wt job`;
- `wt smoke`, `wt rotate TAG`, `wt model list --server`;
- `wt profile slots add|set|candidates` and `wt profile show -M ...`.

A model-driven agent with no `[[agents]]` entry in `config.toml` is not an
exception: on a session every driver has an agent, eligible for every row
the server lists except another agent's native row, and an entry only
narrows that (`supported_providers`). There is no bare launch without a
model any more.

## What does not

These run with no model server configured:

- a command agent's launch (`-A shell`, `shell-wt`), the worktree picker for
  it included;
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

The `[litellm]` table in `config.toml` holds the proxy's `url` and
`api_key`: host state `wt-model serve` reads to reach LiteLLM and `wt
litellm set|status` manage. A session never reads them, and every save of
`config.toml` keeps them as they are.

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

Nothing is migrated: wt does not write `[model-server]` for you and does not
start a server.

## Interim limits

- **Host commands still run here.** Until the admin commands move to
  `wt-model` (Phase 4), this binary still runs the host commands above,
  reading and writing `registry.toml` and `config.yaml` on whatever machine
  runs them, and the `[litellm]` url and key stay in the `config.toml` the
  session also reads (as a table it leaves alone). On a session-only machine
  they read that machine's files, not the server's.
- **A killed wt cannot end its client session.** wt does not trap `SIGHUP`
  or `SIGTERM` sent to itself, so a terminal closed under a running agent, or
  a `kill`, ends wt before it can tell the server. The server forgets the
  client session when its heartbeats stop, after its 60 s TTL; until then
  the session's models count as in use, and the idle grace starts after.

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
