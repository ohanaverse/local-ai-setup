# `wt-model`

The model server: a second binary, built beside `wt`, that holds the model
side of wt (the registry, the live inventory of local models, start and stop,
the LiteLLM routes) and serves it over HTTP, so an agent session that runs
somewhere else needs no registry of its own. It has `wt-model serve`, the
host-admin commands (`model`, `cloud-sync`, `litellm`, `served`, `warm`,
`stop <provider>|--all`) and `wt-model setup`; [Commands](#commands) lists
them. (This page is the server and its command list; the `wt-model model`
registry commands are on [wt-model.md](wt-model.md).)

```bash
wt-model serve --listen 127.0.0.1:8787 --token-file ~/.config/wt-model/server.token   # loopback, same machine
wt-model serve --listen 0.0.0.0:8787 --token-file ~/.config/wt-model/server.token     # reachable from another host
wt-model serve --listen 127.0.0.1:0 --token-file TOKEN --idle-grace 30m        # the kernel picks the port; the banner prints it
wt-model --version                                                              # wt-model <version>
```

One port carries two surfaces:

- the control API under `/control/v1/`, which lists the models and starts and
  stops local ones for a client;
- the gateway under `/v1/`, a streaming reverse proxy to the LiteLLM proxy
  that `wt-model litellm set --url URL --api-key KEY` configured. It swaps the
  client's token for the LiteLLM key, so that key and every provider key stay
  on the host, and it serves only the inference paths (see
  [Endpoints](#endpoints)).

## Commands

Bare `wt-model` prints its help and exits 0.

| Command | What | Page |
|---|---|---|
| `wt-model serve` | Run the model server: the control API and the gateway on one port | this page ([Flags](#flags)) |
| `wt-model setup` | The Setup tab (a TUI): local setup on this machine, the `local.wt-model.serve` LaunchAgent, the token file, the LiteLLM pair and the LiteLLM plist actions | [below](#wt-model-setup) |
| `wt-model model` | Bare: the Models tab (a TUI) over this host's `registry.toml` | [below](#wt-model-model), [wt-model.md](wt-model.md) |
| `wt-model model init\|list\|add\|edit\|rm` | The registry commands, no terminal needed | [wt-model.md](wt-model.md) |
| `wt-model cloud-sync` | Refresh OpenRouter prices and mirror the ollama cloud catalog | [wt-cloud-sync.md](wt-cloud-sync.md) |
| `wt-model litellm sync\|list\|providers\|status\|set\|restart` | The LiteLLM routes (cloud routes in `config.yaml`, local routes in LiteLLM's database when the pair is set), the proxy pair, the proxy restart | [wt-agents/README.md](wt-agents/README.md#litellm-routes-are-wt-owned), [Proxy url and key](#proxy-url-and-key) |
| `wt-model served <provider>` | The model ids a local provider's server is serving now | [wt-start-stop.md](wt-start-stop.md#wt-model-served-provider) |
| `wt-model warm <provider> <model>` | Load a model into a running omlx server with one short request | [wt-start-stop.md](wt-start-stop.md#wt-model-warm-provider-model) |
| `wt-model stop <provider>\|--all` | Stop every running model of a provider, or every local model, asking first about a model client sessions use | [wt-start-stop.md](wt-start-stop.md) |

`wt-model stop` takes a provider or `--all`, never a model id: given one it
is a usage error (exit 1) naming `wt stop <model>`, the session command that
stops one model through the server.

## Setting it up

The way in is `wt-model setup` (the Setup tab: local or native;
[below](#wt-model-setup)), run on the machine that will serve. Choose `local`
there and its plan writes the token file, wt-model's LiteLLM pair and a LaunchAgent, `local.wt-model.serve`, that
runs `wt-model serve` on a loopback address (`127.0.0.1:8787` unless you name
another) at login; macOS only, and a plist wt did not write (no wt marker
comment) is never overwritten. The tab shows the plan and asks `Apply this
plan? [y/N]` (default no) before anything is written. The LiteLLM proxy
itself is a separate concern: the tab only checks it and offers narrow
actions on its plist ([guide 14](../../docs/guides/14-litellm-install-and-configuration.md)).

A session machine chooses `remote` (or `native`) on the Mode tab of
`wt config` ([wt-config.md](wt-config.md), "Mode tab"): `remote` points it at a server on
another machine, and `local` stores the default url and token file and points
at `wt-model setup` (offering to run it when `wt-model` is on `PATH`).

Running `wt-model serve` yourself, as the rest of this page describes, stays
the alternative: a server started by hand on the chosen port is refused by
`wt-model setup`, not adopted, so use one route or the other per port.

## Where it runs

The server runs on the machine that owns the models. Three setups use it the
same way and differ only in `--listen` and in what is allowed to reach it:

- **A VM sandbox to its host.** The agent runs in a VM; the models run on the
  host. Listen on an address the VM can reach (the host's VM-facing interface,
  not every interface if you can avoid it).
- **One machine to another.** The agent runs on a laptop; the models run on a
  workstation. Listen on the workstation's LAN address.
- **The same machine.** Listen on `127.0.0.1`. The token is still required.

By default the server speaks plain HTTP. It can speak TLS itself
([Native TLS](#native-tls)); without it, keep it on a network you trust, or
reach it through a tunnel. Either way the shared token is the only
authenticator: anyone who holds it can use the server, and the gateway's
header mode (a client's own claude.ai login forwarded) is accepted only from
a loopback peer or over a real TLS connection to `wt-model` itself, never
from another machine over plain HTTP. With TLS on a LAN, any holder of the
token can send header-mode requests from anywhere, forwarding whatever login
it carries; the owner-only rule is not something wt can enforce.

## Flags

`wt-model serve` takes only these flags and has no `--json`.

| Flag | Meaning |
|---|---|
| `--listen ADDR` | Address to listen on, `host:port`. Required; there is no default. Port `0` lets the kernel choose; the banner prints the bound port. |
| `--token-file PATH` | File holding the shared bearer token. Required on every address, loopback included. See [The token file](#the-token-file). |
| `--idle-grace DUR` | How long a model no live client session uses survives before auto-stop. Default `10m0s`; must be greater than zero. |

## Exit codes

| Code | Meaning |
|---|---|
| 0 | stopped cleanly on SIGINT or SIGTERM |
| 1 | any failure (a bad flag, an unreadable token file, an unconfigured LiteLLM proxy, a `config.toml` in the wt-model home that does not parse, a `[gateway]` TLS pair serve cannot use ([Native TLS](#native-tls)), an address that cannot be bound) |

A failure prints one line on stderr, `wt-model: <message>`, with the way out
last, for example `wt-model: token file PATH is accessible by group or other
(mode 0644); run 'chmod 600 PATH'`. An unconfigured proxy names
`wt-model litellm set --url URL --api-key KEY`.

## The token file

Every request needs the shared bearer token, on every listen address:
the control API starts and stops models and the gateway spends the host's
LiteLLM key, so neither is ever open.

- The path is whatever `--token-file` names. The server reads it once at
  startup. The tab's `local` mode writes `~/.config/wt-model/server.token`
  (unless you name another path); its `remote` mode, given a pasted token,
  writes the client's copy to `~/.config/agent-wt/remote-model-server.token`.
  Both are written 0600, and the tab never deletes a token file.
- The mode must be `0600` or stricter (no group or other bits). A looser mode
  is refused with `chmod 600 PATH`.
- After trimming surrounding whitespace the token must not be empty.
- Create one that nobody else can read:

  ```bash
  mkdir -p ~/.config/wt-model
  umask 077; head -c 32 /dev/urandom | base64 > ~/.config/wt-model/server.token
  ```

- To rotate, write a new file (the same command), restart `wt-model serve`
  and give every client the new token. The old token stops working when the
  old process ends.

A client sends the token as `Authorization: Bearer TOKEN`. The gateway also
accepts it as `x-api-key: TOKEN`; when an `Authorization: Bearer` header is
present it is the only credential checked, so a wrong one is refused even
beside a correct `x-api-key`.

## Proxy url and key

The pair the gateway forwards to LiteLLM with lives in the `wt-model` config
home: `~/.config/wt-model/config.toml` holds the url and a `key_file`
pointer under `[litellm]`, and the api key itself is in the 0600 file that
names (`litellm.key` when `key_file` is absent). The setup command is
`wt-model litellm set --url URL --api-key KEY` (its `wt litellm set` twin
writes the same home), and `wt-model litellm status` shows the pair as every
reader resolves it — the key is never printed, only masked.

- The key file's mode must be `0600` or stricter (no group or other bits).
  A looser mode is refused with `chmod 600 PATH`.
- The url holds no credentials: `litellm set --url` refuses a url with an `@`
  anywhere in it (exit 1, a fixed message that quotes none of it, nothing
  written); give the key with `--api-key`, and write an `@` a path needs as
  `%40`. A url with an `@` already stored (a hand edit, an older wt) is still
  what the gateway forwards to, but `litellm status`, the Setup tab of
  `wt-model setup` and every error print it only as
  `(hidden: the url contains an @)`, and the tab's LiteLLM checks do not
  request it (they read `not checked: the pair url contains an @`).
- A home that cannot be read (a loose or unreadable key file, a `config.toml`
  that does not parse, a dangling link) is never replaced by the legacy pair.
  Only what uses the pair refuses on it: `wt-model serve` at startup,
  `litellm status|set`, and `wt-model litellm restart` and the slot-profile proxy
  checks. Every other command, and every session, runs as usual.
- The same `config.toml` may hold a hand-edited `[gateway]` table, which
  `serve` reads once at start: `subscription_passthrough = true` turns on the
  gateway's header mode (off when absent or `false`; any other type is off
  with one `wt-model: warning:` line), and `tls_cert`/`tls_key` turn on
  [Native TLS](#native-tls). No command writes it, and `litellm set` keeps it
  as written; an older `wt-model litellm set` drops the table, which turns the
  mode off at the next `serve` restart (the safe direction) and brings serve
  back on plain HTTP (sessions configured with `https://` then fail at the
  handshake, never in clear).
- The key is the file's content with surrounding whitespace trimmed. A
  missing or empty key file is no key at all, and `--api-key ""` clears the
  value without deleting the file. wt never deletes the file.
- Create one that nobody else can read (the hand way, beside the setup
  command):

  ```bash
  mkdir -p ~/.config/wt-model
  umask 077; printf %s "$LITELLM_KEY" > ~/.config/wt-model/litellm.key
  ```
- The pair is read once, at startup: a running server keeps the pair it
  started with; restart it after `wt-model litellm set`.
- The same pair is the master key wt's route writes use for LiteLLM's model
  API (#598): with both halves set, a local model's route is a row in
  LiteLLM's database (`/model/new`, id `wt:<model-id>`, `wt_managed`), so a
  start or stop costs no proxy restart; with either half empty every route
  stays in `config.yaml`, as before. This covers the model server's start and
  stop jobs, `wt-model litellm sync` (and its `--dry-run`, which only lists
  the database), the sync after a registry write and `wt-model stop`. The key
  is sent only as the `Authorization` header and never printed.
- `wt-model litellm list` shows where each route lives, in its `STORE`
  column: `file` for a `config.yaml` row, `db` for a wt route in the
  database (`--json`: a `"store"` field per row,
  [`litellm-cli.sample.json`](../../docs/contracts/litellm-cli.sample.json)).
  The `OWNER` column says `wt` or `hand-written`. When the database cannot be
  listed (the proxy is down, say), one `wt-model: warning: database routes
  not listed (…)` line on stderr says so and the `config.yaml` rows are still
  printed. Illustrative:

  ```text
  ID                           STORE  OWNER
  openrouter/qwen/qwen3.8-27b  file   wt
  ollama/gemma4:9b             db     wt
  ```
- A home `config.toml` with no `[litellm]` table falls back to the legacy
  `[litellm]` table of wt's `~/.config/agent-wt/config.toml` — read-only,
  nothing writes it again. A `wt-model` run that resolves the pair there
  (`wt-model serve` or `wt-model litellm status`) prints one note on
  stderr (illustrative — the path is machine-specific):

  ```text
  wt-model: note: config.toml: [litellm] is a read-only fallback — 'wt-model litellm set' writes /Users/user_name/.config/wt-model/config.toml now
  ```
- While the legacy table holds the pair, the first set (the one that creates
  the home's `[litellm]` table) needs both `--url` and `--api-key`. Nothing
  is copied from the legacy table, so a first set with one flag would leave
  the other unset; it is refused and writes nothing.

Both commands answer `0` done and `1` any failure. A set that wrote the pair
prints `litellm: updated`; a set with no flag is refused with `nothing to
set: pass --url and/or --api-key`.

## Native TLS

`wt-model serve` speaks TLS itself when the wt-model home's `config.toml`
(`~/.config/wt-model/config.toml`) names a PEM certificate and its key under
the same hand-edited `[gateway]` table, read once at start (there is no
`serve` flag: `wt-model setup` re-renders the LaunchAgent's arguments, so the
table survives a re-render). Absent means plain HTTP, exactly as before.

```toml
[gateway]
tls_cert = "~/.config/wt-model/tls/server.crt"
tls_key  = "~/.config/wt-model/tls/server.key"
```

- **Both or neither, strings only.** A leading `~` is expanded. One key
  without the other, a value that is not a string or an empty value refuses
  the start, never a silent fall back to plain HTTP:
  `wt-model: [gateway] tls_cert and tls_key must be set together`,
  `wt-model: [gateway] tls_cert must be a string`. The same keys in agent-wt's
  `config.toml` are never read; a notice there names the wt-model home.
- **A value must be a path, never the file's contents.** A value holding a
  control character (a pasted PEM spans lines), a `-----BEGIN` armor line or
  `PRIVATE KEY` refuses with `wt-model: [gateway] tls_key must be a path to a
  PEM file, not its contents` (or `tls_cert`), naming the key and nothing of
  the value. A path that could not be printed safely (a control character,
  or longer than 200 bytes) is shown as `<unprintable path>` or
  `<path too long>`.
- **The key file is 0600 or stricter** (the token file's rule): a group or
  other bit refuses with
  `wt-model: [gateway] tls_cert/tls_key: TLS key PATH is accessible by group or other (mode 0644); run chmod 600 PATH`.
- **Everything else that makes the pair unusable refuses the start** with one
  line, `wt-model: [gateway] tls_cert/tls_key: <reason> (<path>)`, exit 1
  (every TLS refusal shares that prefix; the loose key's reason ends with
  its chmod hint instead of `(<path>)`),
  before listening and before `serve.json` is written: a missing or
  unreadable file, a directory, a certificate file that holds a private key
  (it would be recorded and copied as "the certificate"), `tls_cert` and
  `tls_key` naming one file, a file that is not a PEM certificate or key that
  parses, or a key that does not match the certificate. The reason is fixed
  per failure; no message ever holds certificate or key bytes.
- **An expired or not-yet-valid certificate is not a refusal** (a LaunchAgent
  that kept refusing would restart in a loop and take every local session
  down). The dates are checked last, so every other check passed. Serve
  starts on plain HTTP, exactly as if no certificate were configured, prints
  one warning after the listening line and records no `tls_cert` in
  `serve.json`:

  ```text
  wt-model: warning: TLS is off: the certificate <path> expired on <date>; serving plain http. Remote https clients fail and the subscription header mode is refused from non-loopback peers until it is renewed and serve restarted
  ```

  The ping says so (`tls_off`). Consequences until both files are replaced
  and serve restarted: every `https://` client (a local session whose url
  setup wrote as `https://` included) gets a connection error, the header
  mode is refused from every non-loopback peer, and ordinary token-swap
  sessions on `http://` keep working. A certificate that expires while serve
  runs keeps serving TLS until the next restart.
- **Protocol.** TLS 1.2 is the floor; for TLS 1.2 serve offers only the six
  ECDHE AES-GCM and ChaCha20-Poly1305 suites. HTTP/2 stays on, and streaming
  (server-sent events) is flushed per event over it as over HTTP/1.1. With
  TLS the server closes idle keep-alive connections after 120 seconds and
  writes TLS handshake errors (the client's address only) to its stderr;
  there is still no read or write timeout.
- **Report.** After the listening line (unchanged, still the bare address)
  serve prints `wt-model: TLS is on (tls_cert <path>, TLS 1.2+; clients that do
  not trust it need ca_file or a CA the system trusts)`, then a warning when
  the certificate expires within 14 days and one when it names neither
  `127.0.0.1` nor `localhost` (local sessions dial loopback).
- **Host commands.** `serve.json` records the certificate's absolute path as
  `tls_cert` while TLS is on (never the key's), so `wt-model stop` and
  `wt-model alias` reach the server over `https://`, trusting exactly that
  file, verified at the record's start time (serve validated it then and
  serves it until restarted, so one that expired since still works). When
  the server named by the record is running but its client sessions cannot
  be read, the count is unknown, never "none": `wt-model stop` asks before
  stopping anything (`--yes` answers; no terminal: exit 3) and
  `wt-model alias rm` needs `--force-in-use`. A record whose server is not
  running is one note and the command goes on, as before.
- **Setup and the Mode tab.** While TLS is on (`tls_cert` set and the
  certificate within its dates), `wt-model setup`'s local plan and the Mode
  tab's local plan write `https://` on the loopback listen address
  (`127.0.0.1` for an unspecified host), and setup's readiness wait trusts
  `tls_cert` directly. Neither writes any TLS key or `ca_file`: when the
  session trusts no CA file, the plan carries one note naming the line to add,
  `ca_file = "<tls_cert path>"` under `[model-server]` in
  `~/.config/agent-wt/config.toml`. With an expired certificate the plans
  write `http://`, the url serve will speak, and note the date and the
  renewal. TLS settings that cannot be read refuse a local plan. Serve reads
  `[gateway]` only at start, so when setup's local plan changes the url's
  scheme (or the running serve's record says it speaks the other one) and
  the LaunchAgent is loaded, the plan reloads it (bootout, then bootstrap)
  and waits on the new url. The Mode tab swaps only the scheme of a stored
  loopback url (`127.0.0.1`, `::1`, `localhost`, any port; a non-loopback or
  hand-written url is kept) and, since a session cannot restart serve, notes
  that it must be restarted on the host (`wt-model setup` does it).
- **Rotation and renewal.** Replace both files, then reload the LaunchAgent:
  `launchctl bootout gui/$(id -u)/local.wt-model.serve` followed by
  `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/local.wt-model.serve.plist`
  (not `launchctl kickstart -k`, which restarts the job launchd already
  holds). There is no hot reload, and a restart cuts the streams in flight.

## `wt-model setup`

`wt-model setup` opens the Setup tab, the same TUI as `wt-model model` on its
other tab (`tab` switches; a Models edit made there still gets its one route
sync when the screen closes). It sets up and controls wt-model on this
machine and offers two modes, `local` and `native`; `remote` is not offered
here, since a session's remote server is set on the Mode tab of `wt config`.
The choice says so under it, as the note "run `wt config` (Mode tab) to use a
server elsewhere (remote)", the mirror of the Mode tab's local pointer note.

- `local` plans the token file, wt-model's LiteLLM pair, the
  `local.wt-model.serve` LaunchAgent and the `[model-server]` table (see
  [Setting it up](#setting-it-up)). macOS only: elsewhere choose `native`.
  While config.toml stores `remote`, the choice carries the note
  "config.toml says remote: applying local replaces its url and token", and
  applying asks first (default no):
  ``config.toml says remote: applying local replaces its url and token.
  next: to use that server again, set remote on `wt config`'s Mode tab.
  Continue? [y/N]`` — the next step resolving the conflict goes with the
  question. `t` and an apply that replaces nothing ask only the plan's own
  confirm.
- `native` leaves local: its plan asks, default no, whether to boot out and
  remove the LaunchAgent wt wrote.
- The rows show the platform, the LaunchAgent, the LiteLLM pair, the listener
  and each LiteLLM check. Keys (the footer names the ones that apply):
  `left`/`right`/`space` choose the mode, `down`/`enter` move through its
  fields, `a` plans and applies (press it from the mode row: `enter` moves
  into the listen field, where letters are typed as text and `esc` leaves
  it), `c` checks, `r` re-reads, `s` / `x` / `R` start, stop or restart the
  local server — while config.toml stores `remote` or `native`, `s` and `R`
  ask first (default no):
  ``the mode is remote: no session uses the local server. next: stop it with
  x when done, or make the mode local on `wt config`'s Mode tab.
  Start it anyway? [y/N]`` — `x` never asks, and while the local server runs
  unused the choice carries the note "the local server runs but remote is in
  effect: nothing uses it. next: press x to stop it (choose local first), or
  apply native here to remove it". `tab` switches tabs and `q` quits. Two keys act on the
  LiteLLM LaunchAgent, each as a plan you
  confirm: `k` opens the master-key menu (use the key the plist already has,
  generate a new one, which rotates `LITELLM_MASTER_KEY` with a backup under
  `~/.config/wt-model/backups/`, or create the LiteLLM agent when it has no
  plist), and `t` tightens a LiteLLM plist looser than 0600 to 0600 and
  changes nothing else. A plan that restarts or unloads a running LiteLLM or
  wt-model (`launchctl kickstart -k`, `bootout`) says so: requests in flight
  are cut at once, unlike `wt-model litellm restart`, which lets them finish.
  A key rotation reloads LiteLLM (it unloads the job and bootstraps the edited
  plist; a `kickstart -k` would restart the definition launchd already holds,
  with the old key), so LiteLLM is down for the reload, about 15 seconds.
  One apply runs at a time on a machine: a second `wt-model setup` or
  `wt config` apply waits for the first to finish, on
  `~/.config/wt-model/setup.lock` (a plain lock file, never removed), and
  Create writes the LiteLLM plist only when none exists, even if one appears
  while the plan runs.
- It needs a terminal. Without one it exits 1 with `wt-model setup needs a
  terminal to open the Setup tab`.

## `wt-model model`

`wt-model model` opens the Models tab: the models in this host's
`registry.toml` and every local model found on it, with `n` to add, `enter`
to edit or register and `d` to remove. It is a host screen: run it on the
machine that holds the registry and `config.yaml`, not on a session machine.
Its keys, columns and form are in [wt-model.md](wt-model.md) ("The Models
tab"); `model` run bare under `wt` prints only `wt: model moved to wt-model —
run 'wt-model model'; nothing was changed` and exits 1.

- It needs a terminal. Without one it exits 1 and names the commands that
  need none: `wt-model model list`, `wt-model model add`, `wt-model model edit` and `wt-model model
  rm`.
- Each saved form and each removal is written to `registry.toml` at once.
  The LiteLLM routes are synced once, when the screen closes, and only if it
  wrote the registry (also when it ended with an error, since the write
  already happened). The sync prints what `wt-model model add|edit|rm` print after
  their write, its warning on stderr; it never changes the exit code.
- With the registry redirected (`WT_REGISTRY`) and nothing naming
  `config.yaml` (`WT_LITELLM_CONFIG`), that sync refuses to write the real
  routes and says so on stderr, as `wt-model model add|edit|rm` do.

## Endpoints

All control paths are under `/control/v1/` and answer JSON.

| Method and path | Meaning |
|---|---|
| `GET /control/v1/ping` | Liveness and version: `api_major`, `schema`, `server`, and `subscription_passthrough: true` when the server accepts the gateway's header mode (`x-wt-model-token` beside the client's own claude.ai login; `[gateway] subscription_passthrough = true` in the wt-model home's `config.toml`, read once at `serve` start). The key is omitted when the mode is off, so an off server and an older one answer alike. `tls_off` is set only when TLS was configured and serve fell back to plain HTTP on the certificate's dates ([Native TLS](#native-tls)), holding the fixed reason (`certificate expired 2026-11-01`, `certificate not valid until <date>`): a date, never certificate bytes; it is omitted otherwise. A client refuses a server whose `api_major` or `schema` differs from its own: a newer `schema` needs a newer wt, an older one is a `wt-model` still running from before wt was updated (`bin/update-tools` does not restart it), and the error says `restart wt-model`. |
| `GET /control/v1/catalog` | Every model and every registry alias: state, action, tier, price, protocols, and how many live client sessions use it. Slot-profile rows are not included. |
| `GET /control/v1/resolve?model=ID` | One catalog row, or a 404 `unknown_model` naming why. |
| `GET /control/v1/candidates` | The registry's models ranked for a slot, as `wt profile slots candidates --json` ranks them. Query: `open_only=BOOL`, `provider=a,b`, `near=ID`, `per_provider=N` (needs `near`, else 400 `bad_request`). Answers `{"tiers_generated", "stale_sources", "candidates"}`: `tiers_generated` is the RFC 3339 time of the tiers file and is absent when there is none; `stale_sources` and `candidates` are always present. A candidate carries `id`, `provider`, `location`, `flags` (always a list) and, where they apply, `tier`, `confidence`, `score` (absent when unscored), `rated`, `open_weights`, `context`, `output_per_million`, `subscription`, `subscription_period`. |
| `GET /control/v1/spend?start=T&end=T` | Per-model request, token and spend totals from LiteLLM's spend log, for the window between two RFC 3339 times (fractional seconds allowed; `wt stats` sends them, so its spend window ends at the report's exact instant; both required, else 400 `bad_request`; a `+` in an RFC 3339 offset must be URL-encoded as `%2B`, since a bare `+` decodes to a space and the request is a 400): `{"rows", "unattributed"}`, `rows` always present, `unattributed` the requests logged without a model. The database address is read on the host and never sent; see the 503 codes below. |
| `POST /control/v1/start` | Start a local model. Body `{"model", "allow_replace", "others"}`. Answers 202 with a job id. While a start of the same model is queued or running, the answer is that job's id instead of a new job (see [Jobs and client sessions](#jobs-and-client-sessions)). An alias id starts the alias's local target (below). |
| `POST /control/v1/stop` | Stop a model. Body `{"model", "confirm_in_use", "session"}`. Answers 202 with a job id. A model that is not running is a succeeded no-op; an alias id fails (`failed`, `alias/x has nothing to stop; stop <target>`). `session` is the requester's own client session id: it does not count as a user of the model, so a session stopping a model only it uses needs no `confirm_in_use`. |
| `GET /control/v1/jobs?model=ID` | The queued and running jobs of one model, or of every model without `model`, oldest first: `{"jobs": [...]}`. `jobs` is always present, `[]` when none is active. |
| `GET /control/v1/jobs/{id}` | A job's state, step text and, once finished, its result. |
| `POST /control/v1/jobs/{id}/cancel` | Cancel a job. 204. |
| `POST /control/v1/sessions` | Register a client session and the models it uses: `{"id", "models"}`. Registering again replaces the model set. 204. |
| `POST /control/v1/sessions/{id}/heartbeat` | Renew a client session and add models to its set: `{"models"}`. 204, or 410 `unknown_session` when it has lapsed. |
| `DELETE /control/v1/sessions/{id}` | End a client session. 204, also for an unknown id. |
| `/v1/*` | The gateway: an inference path is forwarded to LiteLLM, streaming, with the host's LiteLLM key; any other path is a 404 (below). |

The gateway serves these paths only, matched on the path (the query is
passed through untouched, so `/v1/messages?beta=true` is served):

| Path | Serves |
|---|---|
| `/v1/messages`, `/v1/messages/count_tokens` | Anthropic messages |
| `/v1/chat/completions`, `/v1/completions`, `/v1/embeddings` | OpenAI inference |
| `/v1/responses` and every path below `/v1/responses/` | OpenAI responses |
| `/v1/models` and every path below `/v1/models/` | the model list, one model |

LiteLLM's other `/v1` routes (files, batches, `/v1/model/info`) are not
served. A path that is not already clean (`..` or `.` segments, a doubled or
trailing slash) or that is percent-encoded where it need not be (`%2e%2e`, an
escaped `/`) is not served either, whatever it would decode to. The token is
checked first: a wrong one is a 401 on any path.

### Catalog rows

Every row carries `id`, `family`, `provider_id`, `model_name`, `location`,
`tags`, `registered`, `discovered`, `state`, `action`, `protocols` and
`sessions`. These fields need a note:

| Field | Meaning |
|---|---|
| `gateway_id` | The id to send through the gateway. Empty for a native row (`native: true`), which never goes through the gateway. |
| `order` | The model's registry index; discovered rows follow the registry's, by id. Rows are sorted by `id`, so a client that lists in registry order sorts on this. Always present. |
| `routed` | Whether LiteLLM lists the model. Three states: `true`, `false`, and **absent** when the server could not ask LiteLLM (a proxy restart). Absent means unknown, never "not routed". The same goes for `resolve`. The server asks LiteLLM at most once per 2 seconds. |
| `tier`, `tier_code`, `confidence` | Present for a rated model only. |
| `aligned`, `tier_sources` | The aligned score and how many scores it was made from; present for any model the tiers file has them for, rated or not. |
| `context` | The context window in tokens: the registry's `context_window` (any location), else, for a cloud model, the tiers file's window when it records the model's own host window (not the largest row of its key). Absent when unknown. |
| `price.time_priced` | True when the model has time-windowed prices, so the figures are those in force at `generated`. |
| `kind` | `"alias"` on an alias row, absent on a model row. |
| `targets` | An alias row's targets in the alias's order, each `{"id", "order", "provider_id", "location", "state", "context"}` (`context` absent when unknown). Absent on a model row. |

**Alias rows** (#596). Each registry `[[aliases]]` entry is one row:
`id` and `gateway_id` are the alias id, `provider_id` and `family` are
`alias`, `model_name` is the name after `alias/`, `registered` is true,
`order` follows every model's, there is no `price`, and the tier fields are
the lowest-order target's (the lowest tier among targets that share that
order). `context` is the smallest window over all targets, absent when any
target's window is unknown. `location` is `local` only when the lowest-order
targets include the alias's local target, else `cloud`. The action: `start`
when that local target must start, or runs while the alias id is not routed
(the start re-runs its route write, which adds the alias's rows); `launch`
when a target is servable and the alias is routed; `block` when a target is
servable but the alias is not routed (`run 'wt-model litellm sync'`); `start`
when any target can start; else `block`, naming each target's reason. The
state follows the action, as on every model row: `launch` is `running`,
`block` is `blocked` (with `block_reason`), and `start` is `stopped`, or the
local target's own state (`stopped`, `loading`, `unknown`) when the start is
that target's start. So a servable alias that is not routed reads `blocked`,
a local target running while the alias is not routed reads `stopped` with
`start`, and a `local` alias whose local target is blocked but whose routed
cloud target is servable reads `running` with `launch`. A malformed alias is a
`block` row naming its fault. Only an id under `alias/` is ever an alias on the
server: a hand-edited malformed alias whose id lacks the prefix gets no row,
and a start, stop or client session naming that id reaches the model with that
id, if any. An older client (from before aliases) ignores `kind` and
`targets` and reads an alias row as a model of provider `alias`: an agent
allowed every provider (each agent a session synthesizes) finds it eligible,
so a launch with no `-M` counts it among the launchable models (one model plus
an alias over it is then "multiple models match") and may rotate onto it, and
a launch of it goes to the gateway under the alias id, which LiteLLM routes;
an agent whose `config.toml` lists its providers never sees it. A server from before aliases sends a local row's
`context` from the tiers file, not the registry, so update the server before
its clients: a newer client against an older server may declare a window for
claude that no registry value set.

The catalog's `pricing` object is present exactly when some model is priced
from OpenRouter; its `updated` is the RFC 3339 time of the newest price stamp
and is absent when prices were never refreshed. A resident listed in a job's
`others` carries a `figure`: the memory it holds, as wt words it.

A request body may carry fields this server does not know (ignored), may be
empty (an empty object) and is limited to 1 MiB. A body that is not JSON, or
that carries data after its first JSON value, is a 400 `bad_request`.

## Errors

**Control API.** Every non-2xx answer is `{"error": {"message": "...",
"code": "..."}}`, and a failed job carries the same object under its result. A
path or method the control API does not route also answers this object, with
`Allow` set on a 405.

| Status | Code | When |
|---|---|---|
| 400 | `bad_request` | A body that is not valid JSON or has trailing data, a missing `model`, an empty client session id |
| 401 | `unauthorized` | The token is missing or wrong |
| 404 | `not_found` | No such control endpoint |
| 404 | `unknown_model` | `resolve` of an id nothing knows |
| 404 | `unknown_job` | A job id, polled or cancelled, the server does not hold: unknown, finished and dropped from the list, or the server restarted |
| 405 | `method_not_allowed` | The path exists for other methods |
| 410 | `unknown_session` | A heartbeat for a client session that is unknown or lapsed: register again |
| 503 | `spend_not_configured` | `spend`: nothing names the LiteLLM database (see `wt-model litellm` for where wt looks); the message is the fixed text `spend is not configured on the model server`, never the wrapped error, which can name host paths |
| 503 | `spend_unavailable` | `spend`: the database is named but could not be queried; the message is `spend unavailable: <reason>` and never carries the connection string |

A job that cannot do what was asked finishes `failed` with one of these
codes in `result.error`:

| Code | Meaning |
|---|---|
| `occupied` | Another model occupies the server; `occupants` names them. Post the start again with `allow_replace` to replace them. |
| `others` | Other resident models would be stopped; `others` lists them, `others_unknown` those it could not identify. Post again with an `others.stop` answer. |
| `occupancy_unknown` | It cannot tell what the provider is serving. |
| `in_use` | Live client sessions use the model (`sessions` names them). Post the stop again with `confirm_in_use`. |
| `blocked` | The model cannot start here; the message says why. |
| `stopped` | The model started, then was gone again (stopped by another wt, or died) while the LiteLLM routes were updated. |
| `unknown_model` | Nothing knows the id. |
| `failed` | The start or stop did not work; the message says why. A cloud model has nothing to start or stop and fails this way. |
| `cancelled` | The job was cancelled. |

A stop the provider accepted but has not finished (ollama) is a succeeded job
whose step text begins `still finishing: ` followed by the engine's text (the
constant `modelapi.StepStillFinishing`); it is not an error.

**Gateway.** An error the gateway itself writes is OpenAI-style, because
agents parse it: `{"error": {"message": "...", "type": "...", "code":
"..."}}`. Whatever LiteLLM answers passes through untouched. The three 403s
answer only a request that carries the `x-wt-model-token` header (the header
mode, which forwards the client's own claude.ai login; a request without the
header is served by the token swap exactly as before). Each message is fixed
text and never carries a header value.

| Status | Code | Type | Message | When |
|---|---|---|---|---|
| 401 | `unauthorized` | `authentication_error` | `missing or wrong bearer token` (the control API's wording) | The token is missing or wrong. In the header mode: the header is not exactly one value equal to the token (an empty or second value included, even beside a valid `Authorization: Bearer TOKEN`), or `Authorization` holds the token itself |
| 403 | `subscription_off` | `permission_error` | `subscription passthrough is off on this model server` | The header is valid but the host has not turned the mode on (off by default); a wrong header is the 401, so the mode is not disclosed |
| 403 | `subscription_insecure` | `permission_error` | `a Claude login is accepted only from a loopback peer or over TLS` | The peer is neither loopback (`127.0.0.0/8`, `::1`, `::ffff:127.x`) nor a TLS connection to wt-model itself, or a forwarding header refuses: any `X-Forwarded-Proto` value or `Forwarded` `proto=` element that is not `https`, or, from a loopback peer, any `X-Forwarded-For`, `X-Real-IP` or `Forwarded` `for=` element naming a non-loopback client while no proto indicator says `https`. Forwarding headers are only ever read to refuse |
| 403 | `subscription_no_login` | `permission_error` | `the Authorization header is not a Claude login (Bearer sk-ant-oat...)` | In the header mode, `Authorization` is present and is not one `Bearer sk-ant-oat…` value (an API key, any other string, two values). An absent `Authorization` is forwarded absent |
| 404 | `not_found` | `invalid_request_error` | `path is not served by the gateway` | A path the gateway does not serve (checked after the token, both modes) |
| 503 | `proxy_unavailable` | `api_error` | `model proxy is restarting` | LiteLLM cannot be reached; with `Retry-After: 5` |

In the header mode the upstream request loses `x-wt-model-token` and
`x-api-key`, keeps the client's `Authorization` (the login) and
`anthropic-beta` as sent, and gets `x-litellm-api-key` set to the host's
LiteLLM key (a client's own value is replaced).

## Jobs and client sessions

A start or stop is a job: the request answers 202 at once and the client polls
`GET /control/v1/jobs/{id}`. Jobs run one at a time, in memory; a job goes on
if its client disconnects, and after a server restart all jobs are gone and
state is re-derived from live probes. The server never asks a question: a
prompt becomes a typed error the client answers by posting again.

- **One start per model.** A start of a model whose start is already queued
  or running joins that job: the answer is its id, and it runs once. A start
  of an alias is keyed by its local target, so it joins a start of that model
  (and the reverse); the job's `model` is the target id. It waits for the
  target to answer through the gateway, then checks that LiteLLM lists the
  alias's row for it: a missing row fails the job only when the gateway does
  not list the alias id at all, otherwise it is one `warning:` step. An alias
  with no local target answers `failed` (nothing to start). The
  joining request's own `allow_replace` and `others` are ignored; the job keeps
  the answers of the request that created it, so a replace another client
  agreed to goes ahead for both. A start that has already been cancelled is
  not joined: the new request queues a job of its own. A stop is never joined.
- **Retention.** A finished job stays readable until 100 later jobs have
  finished; after that, or after a server restart, its id answers 404
  `unknown_job`, for a poll and a cancel alike.

A client session is a client-chosen id plus the models it uses.

- A client session lives while its last heartbeat is within the TTL of 60 seconds.
  The server enforces only the TTL; clients heartbeat every 20 seconds so two
  missed beats still leave it live.
- A heartbeat adds models; removing one is a new register.
- Ending a client session, or letting it lapse, starts the idle clock of the models
  only it used.
- A client session that names an alias also uses the alias's current local
  target, read from the registry at each scan and request: a re-point is seen
  without registering again. While the session is live the target counts it
  as a user (and the target's idle clock follows the session's heartbeats, so
  after a re-point the old target is stopped once idle past the grace);
  after it ends the target's idle clock is the alias's.

**Auto-stop.** Every 30 seconds the server scans for local models to stop. A
model stops when some client session registered it at some point, no live client session
uses it now, its idle clock is at least `--idle-grace` old, no job for it is
queued or running, wt can stop its provider and the probe of that provider is
trusted. A successful start restarts the model's idle clock. A cloud model, and a model started by
hand on the host that no client session ever registered, is never auto-stopped. After a
restart of the server the registry is empty, so nothing is auto-stopped until
a client session registers again.

The registry also forgets: a registered model that a trusted probe shows not
running, that no live client session uses, that has no job queued or running
and whose idle clock is at least `--idle-grace` old is dropped from it. A
model auto-stopped (or stopped any other way) and later started by hand on
the host, or by `llmbench provider isolate`, is then left alone like any
model no client session registered. A client session registers before it
asks for a start, and a job is kept however that session then fares: it
lapses after its TTL while the job may still be waiting, and a model
forgotten then would start unseen (a forgotten model never comes back) and
never be auto-stopped.

The stop is an ordinary stop job, so a client session that registers between the
scan and the stop still blocks it. On an Exclusive provider (a server that holds one
model at a time, such as mtplx) the stop takes the whole server, so every
client session on any model of that family counts as a user, both for auto-stop and
for the `in_use` check of an explicit stop.

## Startup and shutdown

Once it is listening the server prints one line on stderr, the only
machine-readable startup signal. `<addr>` is the bound address, so port `0`
prints the real port:

```text
wt-model: listening on <addr> (gateway /v1, control /control/v1)
```

With TLS the line is the same (the address, no scheme). The TLS state
([Native TLS](#native-tls)) and the header mode are reported on their own
lines after it, TLS first.

Right after that line it reconciles the LiteLLM routes once, as
`wt-model litellm sync` does, through the same pair (local routes in
LiteLLM's database): a database that was wiped is repopulated the next
time `wt-model serve` starts, without waiting for the next start or stop (a
proxy restart keeps its database rows and runs no sync). It runs beside the
serving, so sessions can reach the server meanwhile, and only when it can see
the database: it first waits up to 90 seconds for the proxy to answer (at
login the proxy and its database start after the server listens) and then
lists the database. A proxy that does not answer, or a listing that fails,
skips the whole sync with one line and changes nothing:

```text
wt-model: warning: LiteLLM routes not synced at start: <why>; nothing was changed (run 'wt-model litellm sync' once the proxy answers)
```

Otherwise each change is a `wt-model: start route sync: <id>: <action>` line
on stderr and each warning a `wt-model: warning: start route sync: …` line; a
sync that fails is one `wt-model: warning: LiteLLM routes not synced at
start: …` line. None of them stops the server, and no `config.yaml` at all
(LiteLLM not set up on this host) is silent. The sync never restarts the proxy
itself: when it changed `config.yaml` (the first `store_model_in_db`, the
move of local rows to the database, a cloud route) it hands the one restart it
owes to the same queue the start and stop jobs' restarts use, so two restarts
never run at once.

It prints nothing on stdout and nothing when it stops. On SIGINT or SIGTERM it
shuts down in order: the auto-stop loop, the HTTP server (up to 10 seconds
for open requests), the running job (up to 30 seconds), then every LiteLLM
restart a route write or the start-up sync handed off (each runs to its end,
one at a time; a restart can wait up to 3 minutes for a request in flight),
then the start-up sync if it is still running (its waits for the proxy end
with the signal, and it restarts nothing itself, so this costs at most the
config.yaml write in progress), and last any restart that sync handed off
meanwhile. It exits 0. A second signal during a hung
shutdown kills it.

While it runs the server keeps a runtime record, `~/.config/wt-model/serve.json`
(in the wt-model home, so `XDG_CONFIG_HOME` moves it), which is how a host
command finds the live server: it is read by `wt-model stop`, which asks the
server named there for its client sessions before stopping a model they use
([wt-start-stop.md](wt-start-stop.md#in-use-confirmation)). It holds the record version, the server's pid,
the bound listen address, the absolute path of the token file and the start
time; never the token itself or any key:

```json
{"version": 1, "pid": 4242, "listen": "127.0.0.1:8787", "token_file": "/Users/user_name/.config/wt-model/server.token", "started": "2026-11-19T10:00:00Z"}
```

| Key | Meaning |
|---|---|
| `version` | the record format, `1` |
| `pid` | the serving process |
| `listen` | the bound address the listening line printed |
| `token_file` | the token file's absolute path (never the token) |
| `started` | the start time, UTC |
| `tls_cert` | only while serve speaks TLS: the certificate's absolute path (never the key's). A host command then dials `https://`, trusting exactly this file. Absent for plain HTTP, the expired-certificate fallback included. Additive: the version stays `1` |

The file is mode 0600 (the home is made 0700 only when serve has to create it;
an existing home keeps its mode) and is written (atomically) before the
listening line, so whoever saw that line can read it. A record that cannot be
written is one `wt-model: note: could not write the runtime record (…)` line on
stderr and the server goes on serving; host commands then cannot see its
client sessions. On a clean stop (SIGINT or SIGTERM) the server removes the
record only when it still names its own pid; a second serve overwrites the
record, and the first one's exit then leaves the second's in place. A serve
that cannot bind its address neither writes nor removes it. A killed server
leaves its record behind. `wt-model stop <provider>|--all` reads the record to
ask the live server for its client sessions; a record whose pid is no longer
running is stale and is not asked (another process may hold the port now).
A stale record (or one that cannot be read) is one note on stderr and the
stop goes on without asking about any model; a server that is running but
cannot be asked makes the stop ask before stopping anything
([wt-start-stop.md](wt-start-stop.md#in-use-confirmation)):

```text
wt-model: note: could not read client sessions from the model server (the model server (pid 4242) is not running: stale record) — models they use are stopped without asking
```

No record at all (no server running) is silent: there is nothing to ask.

## Known limits

- **The gateway cannot scope agent traffic per client.** One shared token
  opens every inference path and every model LiteLLM routes. The gateway does
  not tell clients apart or limit which models a client may call.
- **The client uses no environment proxy.** The control and gateway calls ignore `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY`, so the bearer token and the prompts go straight to the `[model-server]` url and never to a third party. A session machine that can reach the server only through a proxy cannot use it.
- Config is read per request, job and scan, so `wt-model model …` and `wt-model cloud-sync`
  edits show without a restart.

## Wire contract

The request and response shapes are pinned by
[`docs/contracts/model-api.sample.json`](../../docs/contracts/model-api.sample.json),
held by the `internal/modelapi` tests. Design:
[the session-split design](../../docs/superpowers/specs/2026-11-15-wt-model-session-split-design.md).
