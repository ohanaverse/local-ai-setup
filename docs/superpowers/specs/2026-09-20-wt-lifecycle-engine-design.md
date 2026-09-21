# wt: local model lifecycle engine (start, wait, warm, replace)

Date: 2026-09-20
Status: approved, pending implementation plan

## Context

Sub-project 4a of the "make wt's model selector look like modelman's" effort.
Long-term goal: migrate away from modelman, so start/stop/warmup is ported to
Go. Sub-projects 1-3 are merged (#110 per-agent stats, #111
`localmodels.Inventory`, #112 selector table).

| # | Sub-project | Depends on |
|---|---|---|
| 1 | Stats and surveys for any agent-model pair (done, #110) | none |
| 2 | Go discovery and running-state of local models (done, #111) | 1 |
| 3 | New selector table (done, #112) | 1, 2 |
| 4a | Lifecycle engine: Go start/stop/warm + replacement query (this spec) | 2 |
| 4b | TUI integration: replace-confirm, progress + cancel, launch after start, typed "blocked" reason | 4a |
| 5 | Launch-time smoke gate (exit non-zero on failure) | 4b |
| 4c | Non-TUI launch, `-M` pin check and `wt smoke` move from `localgate` to live Inventory, with start-on-select and a warn/confirm policy without a TUI; retires `localgate`'s flag dependence | 4a |

State scope decided in brainstorming: **live truth everywhere**. Wt writes
nothing to modelman-owned state (`modelman.toml` running/exposed flags,
LiteLLM config) and never restarts the LiteLLM proxy. A started model works in
LiteLLM mode only if modelman already exposed it there.

> Superseded 2026-09-21: `lifecycle.Start/Stop/StopModel` now update LiteLLM routes — see `2026-09-21-wt-litellm-ownership-design.md`.

## Problem

Selecting a non-running local model in the wt picker only shows a hint to run
`modelman start`. The lifecycle logic that actually starts a model lives in
Python (`modelman/.../providers/lifecycle`, `local_control.start_local_model`).
Wt needs its own start engine so it can start what it discovers, without
creating registry config for discovered models.

## Design

### Approach

A `Backend` interface per provider mirroring modelman's structure, plus one
generic orchestrator. Rejected: a single `Start` with a `switch` on provider
id (tangled spawn/poll branches, harder per-provider tests); shelling out to
`modelman start` (contradicts the port-to-Go decision).

### Package and API

New package `wt/internal/lifecycle`, no UI dependency.

- `Target{ProviderID, ModelName string}` — the provider-side name, i.e.
  `config.Model.ModelName`. For a discovered model this is the artifact name,
  so no registry entry is required.
- `Start(ctx, cfg, target, opts) error` — `opts` carries `AllowReplace bool`
  and `Progress func(Stage)`.
- `Occupant(cfg, target, snap localmodels.Snapshot) (localmodels.Entry, bool)`
  — what starting `target` would replace, from live Inventory: never anything
  for ollama (multi-tenant); one domain for `omlx` and `omlx-6bit`; any running
  mtplx model other than the target.
- `Stop(ctx, cfg, providerID) error` — internal, used for replacement. Wt gets
  no user-facing stop command.
- **Start never replaces silently.** With an occupant and `AllowReplace ==
  false` it returns `*OccupiedError{Occupant}` before touching anything.
  Callers (4b TUI, 4c non-TUI) decide how to confirm.
- **Idempotent.** If the target is already running per a live probe, `Start`
  is a no-op.

### Per provider

| Provider | Start | Stop (replace) |
|---|---|---|
| ollama | Daemon must answer `GET /api/tags`, else `*DaemonDownError` with a hint. Then a 1-token chat request to `/v1/chat/completions` loads the model (visible in `/api/ps`). | none (multi-tenant) |
| omlx | `omlx start` (binary via `exec.LookPath`), wait for `/v1/models`, then a 1-token warmup naming the artifact. | `omlx stop`, then poll until port 8000 closes |
| mtplx | Spawn `mtplx serve --model X --port 8003 --host 127.0.0.1 --model-id X` in its own session (survives the terminal closing) with the same pidfile and log paths as modelman (`/tmp/local-ai-setup-mtplx.pid`, `/tmp/local-ai-setup-mtplx.log`). Wait for `/v1/models` to list X while watching for early process death, then warm. On failure or cancel, tear the spawned process down. | `mtplx stop --port 8003 --grace-seconds 10`, then confirm the port closed |

Ports and origins come from the registry provider row (`auth.base_url`,
via `localmodels`' origin logic), falling back to 11434 / 8000 / 8003.
Timeouts ported unchanged from modelman: warmup up to 600s, model-load wait
up to 300s, post-stop port-close wait 6s. `omlx` and `omlx-6bit` are one
physical server. `mlx_lm_server` is out of scope (target+draft pairing read
from registry fields wt ignores; not discoverable).

### Progress, cancel, errors

- `Progress(Stage)` reports `stopping-occupant`, `starting`,
  `waiting-for-model`, `warming` so 4b can show a live status line during a
  start that can take minutes.
- Cancellation via `ctx`: cancelling an mtplx start kills the process it
  spawned; cancelling an omlx or ollama start just stops waiting (the daemon
  is not ours to kill and a half-loaded model is harmless).
- Typed errors: `*OccupiedError`, `*DaemonDownError`, `*BinaryMissingError`
  (e.g. "mtplx not found on PATH"), plus wrapped errors carrying the process
  log tail when a spawned process dies.
- **Wt does not kickstart the ollama daemon.** Modelman's benchmark path does,
  but starting a launchd job as a side effect of selecting a model is a
  surprise; wt reports and hints instead.

### Out of scope

Any UI (4b); non-TUI, `-M` pin and `wt smoke` paths (4c); mlx_lm_server;
writing modelman flags; LiteLLM config or proxy restarts; a user-facing stop
command; stopping models when wt exits.

## Testing

Fake HTTP servers for the probe and warmup endpoints; fake `omlx`/`mtplx`
shell scripts on a temp `PATH` for spawn and stop; pidfile and log paths
injectable with the modelman paths as the production default. Every `Test*`
carries a what/why comment. Cases:

- Happy path per provider (ollama warm, omlx start+warm, mtplx spawn+wait+warm).
- Already serving is a no-op (no spawn, no warmup traffic).
- `OccupiedError` without `AllowReplace`; replacement with it (stop then
  start, in that order, with progress stages reported in order).
- `Occupant` rules: none for ollama; omlx/omlx-6bit share a domain; mtplx
  excludes the target itself.
- Cancel mid-mtplx-start tears down the spawned process; a process dying during
  model load fails fast with the log tail; missing binary; daemon down.
- The mtplx spawn argv, pidfile and log paths match modelman's exactly, so
  modelman's `mtplx stop`/pidfile tracking interoperates.

Update `wt/CLAUDE.md` (package table, "never shells out for discovery" note —
lifecycle does exec provider binaries) when implemented.
