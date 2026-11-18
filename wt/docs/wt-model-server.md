# `wt-model`

The model server: a second binary, built beside `wt`, that holds the model
side of wt (the registry, the live inventory of local models, start and stop,
the LiteLLM routes) and serves it over HTTP, so an agent session that runs
somewhere else needs no registry of its own. It has `wt-model serve` and
`wt-model litellm set|status`, the commands for the proxy pair the gateway
forwards with ([Proxy url and key](#proxy-url-and-key)). (This page is the
server. `wt model`, with a space, is the registry command:
[wt-model.md](wt-model.md).)

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

## Where it runs

The server runs on the machine that owns the models. Three setups use it the
same way and differ only in `--listen` and in what is allowed to reach it:

- **A VM sandbox to its host.** The agent runs in a VM; the models run on the
  host. Listen on an address the VM can reach (the host's VM-facing interface,
  not every interface if you can avoid it).
- **One machine to another.** The agent runs on a laptop; the models run on a
  workstation. Listen on the workstation's LAN address.
- **The same machine.** Listen on `127.0.0.1`. The token is still required.

The server speaks plain HTTP and has no TLS. Keep it on a network you trust,
or reach it through a tunnel.

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
| 1 | any failure (a bad flag, an unreadable token file, an unconfigured LiteLLM proxy, an address that cannot be bound) |

A failure prints one line on stderr, `wt-model: <message>`, with the way out
last, for example `wt-model: token file PATH is accessible by group or other
(mode 0644); run 'chmod 600 PATH'`. An unconfigured proxy names
`wt-model litellm set --url URL --api-key KEY`.

## The token file

Every request needs the shared bearer token, on every listen address:
the control API starts and stops models and the gateway spends the host's
LiteLLM key, so neither is ever open.

- The path is whatever `--token-file` names. The server reads it once at
  startup.
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
- A home that cannot be read (a loose or unreadable key file, a `config.toml`
  that does not parse, a dangling link) is never replaced by the legacy pair.
  Only what uses the pair refuses on it: `wt-model serve` at startup,
  `litellm status|set`, and `wt litellm restart` and the slot-profile proxy
  checks. Every other command, and every session, runs as usual.
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

## Endpoints

All control paths are under `/control/v1/` and answer JSON.

| Method and path | Meaning |
|---|---|
| `GET /control/v1/ping` | Liveness and version: `api_major`, `schema`, `server`. A client refuses a server whose `api_major` or `schema` differs from its own: a newer `schema` needs a newer wt, an older one is a `wt-model` still running from before wt was updated (`bin/update-tools` does not restart it), and the error says `restart wt-model`. |
| `GET /control/v1/catalog` | Every model: state, action, tier, price, protocols, and how many live client sessions use it. Slot-profile rows are not included. |
| `GET /control/v1/resolve?model=ID` | One catalog row, or a 404 `unknown_model` naming why. |
| `GET /control/v1/candidates` | The registry's models ranked for a slot, as `wt profile slots candidates --json` ranks them. Query: `open_only=BOOL`, `provider=a,b`, `near=ID`, `per_provider=N` (needs `near`, else 400 `bad_request`). Answers `{"tiers_generated", "stale_sources", "candidates"}`: `tiers_generated` is the RFC 3339 time of the tiers file and is absent when there is none; `stale_sources` and `candidates` are always present. A candidate carries `id`, `provider`, `location`, `flags` (always a list) and, where they apply, `tier`, `confidence`, `score` (absent when unscored), `rated`, `open_weights`, `context`, `output_per_million`, `subscription`, `subscription_period`. |
| `GET /control/v1/spend?start=T&end=T` | Per-model request, token and spend totals from LiteLLM's spend log, for the window between two RFC 3339 times (fractional seconds allowed; `wt stats` sends them, so its spend window ends at the report's exact instant; both required, else 400 `bad_request`; a `+` in an RFC 3339 offset must be URL-encoded as `%2B`, since a bare `+` decodes to a space and the request is a 400): `{"rows", "unattributed"}`, `rows` always present, `unattributed` the requests logged without a model. The database address is read on the host and never sent; see the 503 codes below. |
| `POST /control/v1/start` | Start a local model. Body `{"model", "allow_replace", "others"}`. Answers 202 with a job id. While a start of the same model is queued or running, the answer is that job's id instead of a new job (see [Jobs and client sessions](#jobs-and-client-sessions)). |
| `POST /control/v1/stop` | Stop a model. Body `{"model", "confirm_in_use", "session"}`. Answers 202 with a job id. A model that is not running is a succeeded no-op. `session` is the requester's own client session id: it does not count as a user of the model, so a session stopping a model only it uses needs no `confirm_in_use`. |
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
| `context` | The context window in tokens, present only when the tiers file records the model's own host window (not the largest row of its key). |
| `price.time_priced` | True when the model has time-windowed prices, so the figures are those in force at `generated`. |

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
| 503 | `spend_not_configured` | `spend`: nothing names the LiteLLM database (see `wt litellm` for where wt looks); the message is the fixed text `spend is not configured on the model server`, never the wrapped error, which can name host paths |
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
"..."}}`. There are three: 401 `unauthorized` (type `authentication_error`,
the same message as the control API), 404 `not_found` (type
`invalid_request_error`, message `path is not served by the gateway`) for a
path the gateway does not serve, and 503 `proxy_unavailable` (type
`api_error`, message `model proxy is restarting`, with `Retry-After: 5`) when
LiteLLM cannot be reached. Whatever LiteLLM answers passes through untouched.

## Jobs and client sessions

A start or stop is a job: the request answers 202 at once and the client polls
`GET /control/v1/jobs/{id}`. Jobs run one at a time, in memory; a job goes on
if its client disconnects, and after a server restart all jobs are gone and
state is re-derived from live probes. The server never asks a question: a
prompt becomes a typed error the client answers by posting again.

- **One start per model.** A start of a model whose start is already queued
  or running joins that job: the answer is its id, and it runs once. The
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

It prints nothing on stdout and nothing when it stops. On SIGINT or SIGTERM it
shuts down in order: the auto-stop loop, the HTTP server (up to 10 seconds
for open requests), the running job (up to 30 seconds), then any LiteLLM
restart a route write started. It exits 0. A second signal during a hung
shutdown kills it.

## Known limits

- **The gateway cannot scope agent traffic per client.** One shared token
  opens every inference path and every model LiteLLM routes. The gateway does
  not tell clients apart or limit which models a client may call.
- **The client uses no environment proxy.** The control and gateway calls ignore `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY`, so the bearer token and the prompts go straight to the `[model-server]` url and never to a third party. A session machine that can reach the server only through a proxy cannot use it.
- Config is read per request, job and scan, so `wt model` and `wt cloud-sync`
  edits show without a restart.

## Wire contract

The request and response shapes are pinned by
[`docs/contracts/model-api.sample.json`](../../docs/contracts/model-api.sample.json),
held by the `internal/modelapi` tests. Design:
[the session-split design](../../docs/superpowers/specs/2026-11-15-wt-model-session-split-design.md).
