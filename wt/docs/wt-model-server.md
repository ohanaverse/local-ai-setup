# `wt-model`

The model server: a second binary, built beside `wt`, that holds the model
side of wt (the registry, the live inventory of local models, start and stop,
the LiteLLM routes) and serves it over HTTP, so an agent session that runs
somewhere else needs no registry of its own. It has one command, `wt-model
serve`. (This page is the server. `wt model`, with a space, is the registry
command: [wt-model.md](wt-model.md).)

```bash
wt-model serve --listen 127.0.0.1:8787 --token-file ~/.config/wt-model/token   # loopback, same machine
wt-model serve --listen 0.0.0.0:8787 --token-file ~/.config/wt-model/token     # reachable from another host
wt-model serve --listen 127.0.0.1:0 --token-file TOKEN --idle-grace 30m        # the kernel picks the port; the banner prints it
wt-model --version                                                              # wt-model <version>
```

One port carries two surfaces:

- the control API under `/control/v1/`, which lists the models and starts and
  stops local ones for a client;
- the gateway under `/v1/`, a streaming reverse proxy to the LiteLLM proxy
  that `wt litellm set --url URL --api-key KEY` configured. It swaps the
  client's token for the LiteLLM key, so that key and every provider key stay
  on the host.

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
| 0 | Stopped cleanly on SIGINT or SIGTERM. |
| 1 | Any failure: a missing or bad flag, a token file that is missing, empty or too open, an unconfigured LiteLLM proxy, an address that cannot be bound. |

A failure prints one line on stderr, `wt-model: <message>`, with the way out
last, for example `wt-model: token file PATH is accessible by group or other
(mode 0644); run 'chmod 600 PATH'`. An unconfigured proxy names `wt litellm
set --url URL --api-key KEY`.

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
  umask 077; head -c 32 /dev/urandom | base64 > ~/.config/wt-model/token
  ```

- To rotate, write a new file (the same command), restart `wt-model serve`
  and give every client the new token. The old token stops working when the
  old process ends.

A client sends the token as `Authorization: Bearer TOKEN`. The gateway also
accepts it as `x-api-key: TOKEN`; when an `Authorization: Bearer` header is
present it is the only credential checked, so a wrong one is refused even
beside a correct `x-api-key`.

## Endpoints

All control paths are under `/control/v1/` and answer JSON.

| Method and path | Meaning |
|---|---|
| `GET /control/v1/ping` | Liveness and version: `api_major`, `schema`, `server`. A client refuses a server whose `api_major` differs from its own. |
| `GET /control/v1/catalog` | Every model: state, action, tier, price, protocols, and how many live client sessions use it. Slot-profile rows are not included. |
| `GET /control/v1/resolve?model=ID` | One catalog row, or a 404 `unknown_model` naming why. |
| `POST /control/v1/start` | Start a local model. Body `{"model", "allow_replace", "others"}`. Answers 202 with a job id. |
| `POST /control/v1/stop` | Stop a model. Body `{"model", "confirm_in_use"}`. Answers 202 with a job id. A model that is not running is a succeeded no-op. |
| `GET /control/v1/jobs/{id}` | A job's state, step text and, once finished, its result. |
| `POST /control/v1/jobs/{id}/cancel` | Cancel a job. 204. |
| `POST /control/v1/sessions` | Register a client session and the models it uses: `{"id", "models"}`. Registering again replaces the model set. 204. |
| `POST /control/v1/sessions/{id}/heartbeat` | Renew a session and add models to its set: `{"models"}`. 204, or 410 `unknown_session` when it has lapsed. |
| `DELETE /control/v1/sessions/{id}` | End a session. 204, also for an unknown id. |
| `/v1/*` | The gateway: every path is forwarded to LiteLLM, streaming, with the host's LiteLLM key. |

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
| 400 | `bad_request` | A body that is not valid JSON or has trailing data, a missing `model`, an empty session id |
| 401 | `unauthorized` | The token is missing or wrong |
| 404 | `not_found` | No such control endpoint |
| 404 | `unknown_model` | `resolve` of an id nothing knows |
| 404 | `unknown_job` | A job id the server does not hold (jobs are in memory and gone after a restart) |
| 405 | `method_not_allowed` | The path exists for other methods |
| 410 | `unknown_session` | A heartbeat for a session that is unknown or lapsed: register again |

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
whose step text reads `still finishing`; it is not an error.

**Gateway.** An error the gateway itself writes is OpenAI-style, because
agents parse it: `{"error": {"message": "...", "type": "...", "code":
"..."}}`. There are two: 401 `unauthorized` (type `authentication_error`,
the same message as the control API) and 503 `proxy_unavailable` (type
`api_error`, message `model proxy is restarting`, with `Retry-After: 5`) when
LiteLLM cannot be reached. Whatever LiteLLM answers passes through untouched.

## Jobs and client sessions

A start or stop is a job: the request answers 202 at once and the client polls
`GET /control/v1/jobs/{id}`. Jobs run one at a time, in memory; a job goes on
if its client disconnects, and after a server restart all jobs are gone and
state is re-derived from live probes. The server never asks a question: a
prompt becomes a typed error the client answers by posting again.

A client session is a client-chosen id plus the models it uses.

- A session lives while its last heartbeat is within the TTL of 60 seconds.
  The server enforces only the TTL; clients heartbeat every 20 seconds so two
  missed beats still leave it live.
- A heartbeat adds models; removing one is a new register.
- Ending a session, or letting it lapse, starts the idle clock of the models
  only it used.

**Auto-stop.** Every 30 seconds the server scans for local models to stop. A
model stops when some session registered it at some point, no live session
uses it now, its idle clock is at least `--idle-grace` old, no job for it is
queued or running, wt can stop its provider and the probe of that provider is
trusted. A successful start restarts the model's idle clock. A cloud model, and a model started by
hand on the host that no session ever registered, is never auto-stopped. After a
restart of the server the registry is empty, so nothing is auto-stopped until
a session registers again.

The stop is an ordinary stop job, so a session that registers between the
scan and the stop still blocks it. On an Exclusive provider (a server that holds one
model at a time, such as mtplx) the stop takes the whole server, so every
session on any model of that family counts as a user, both for auto-stop and
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

- **`/v1/*` forwards agent traffic that the gateway cannot scope per
  client.** One shared token opens every `/v1/*` path LiteLLM serves, the
  non-inference routes included, and every model it routes. The gateway does
  not tell clients apart or limit what a client may call.
- **A host-local `wt` is invisible to auto-stop.** `wt` on the host still
  counts its own sessions by process id, which the server cannot see, so a
  model such a `wt` session uses can be auto-stopped once any client session
  has registered it.
- Config is read per request, job and scan, so `wt model` and `wt cloud-sync`
  edits show without a restart.

## Wire contract

The request and response shapes are pinned by
[`docs/contracts/model-api.sample.json`](../../docs/contracts/model-api.sample.json),
held by the `internal/modelapi` tests. Design:
[the session-split design](../../docs/superpowers/specs/2026-11-15-wt-model-session-split-design.md).
