# Design: Multi-model local lifecycle in the modelman TUI

## Problem

Issue #65 gave modelman a CLI-only local-model lifecycle (`modelman start`/
`modelman stop`) built around a single global marker
(`[local].running_model` in `modelman.toml`) and a hard "one local model at a
time" rule: starting any local model stops every other local provider first.
wt's picker (`internal/localgate`) offers cloud models plus, at most, that one
verified-running local model; a stale marker (model stopped/crashed outside
modelman) is fatal for every wt launch until the user re-runs `modelman
start`.

This has three problems:

1. **No TUI surface.** Start/stop only exists as a CLI command; there's no
   way to see or control what's running from the model screen.
2. **Exclusivity is stricter than the hardware requires.** Some local
   providers (ollama) are genuinely multi-tenant — they can serve several
   models from one process. Others (omlx, mtplx, mlx_lm_server) are
   single-model-per-process by construction. The current design treats all
   of them the same way: starting anything stops everything.
3. **One drifted marker breaks every launch.** With a single marker, any
   staleness (crash, manual stop, a benchmark run tearing things down
   without clearing the marker) makes wt exit fatally for every agent
   launch, not just ones that need the dead model.

## Goal

- Local-model start/stop is available from the modelman TUI's model screen,
  not just the CLI.
- Cloud model visibility in wt's picker continues to be controlled by the
  existing `exposed` flag (unchanged).
- Local model visibility in wt's picker is additionally gated on a new
  `running` flag, verified live — "only running local models appear in the
  picker, after other filters are applied."
- The "one local model at a time" rule is relaxed to match what the
  hardware/process model actually requires: providers that can only run one
  model (omlx, mtplx, mlx_lm_server) still enforce exclusivity *within
  themselves*; providers that can run several (ollama) don't. Cross-provider
  concurrency is allowed, with an advisory (non-blocking in the TUI,
  print-only at the CLI) warning about resource contention.
- Both modelman and wt determine "is this model actually running?" by
  probing the provider live, not by trusting a persisted flag alone — this
  also gives "reset to stopped after a restart" for free, with no explicit
  reset step.

## Approach

### 1. Data model

`modelman.toml`'s per-model `[model_state."<id>"]` block gains a `running:
bool` field (default `false`), alongside the existing `ready`/`exposed`
fields:

```toml
[model_state."ollama/qwen3.8:27b-mlx"]
ready = true
disk_path = "ollama:qwen3.8:27b-mlx"
size_bytes = 19327352832
exposed = true
running = true
```

The singular `[local].running_model` marker from issue #65 (`state.py`'s
`LocalState`) is retired. There is no migration step: on upgrade, every
model reads `running = false` (the field is simply absent), which is the
correct state to start from — see "Restart handling" below for why this is
safe even mid-migration.

**Semantics — `running` is a hint, never ground truth by itself.** Every
consumer (modelman's TUI indicator, modelman's own start/stop bookkeeping,
wt's picker filter) treats it as "modelman believes this model was started
and not yet stopped," and confirms it with a live probe before treating the
model as actually running:

- `running = true` **and** probe succeeds → treated as running.
- `running = true` **and** probe fails → treated as **not** running (the
  flag is stale); the consumer may opportunistically clear it.
- `running = false` → treated as not running, **regardless of what any probe
  would report.** A model actually serving because someone started it
  outside modelman (e.g. a hand-run `ollama run x`) is *not* surfaced as
  running by this design — the flag records modelman's own intent, and a
  probe can only demote a stale `true`, never promote a `false`.

This mirrors the existing `_probe_running`/marker-verification pattern from
issue #65, generalized from one marker to a per-model flag on every local
model.

### 2. Per-provider start/stop semantics

| Provider | `start` action | `stop` action | Concurrency |
|---|---|---|---|
| ollama | flip `running = true` only — no process action. Ollama lazy-loads a model on its first inference request, so there's nothing to pre-warm | `ollama stop <name>` (unloads just that model; daemon stays up) | Already multi-tenant — `ollama ps` can list several loaded models at once; many ollama models may be `running = true` simultaneously |
| omlx | `omlx start` (idempotent — no-op if already up) + warmup request naming the target model | `omlx stop` (stops the whole server) | One long-lived server process; it has no per-model unload, only whole-server stop. Starting a *different* omlx model while one is already running **auto-replaces** it (stops the old one, starts the new one) — see below |
| mtplx | `mtplx serve --model <repo> --model-id <repo> --port 8003` (existing `lifecycle.py` path) | `mtplx stop --port 8003 --grace-seconds 10` | Single-model-per-process on a fixed port; a second mtplx model auto-replaces the first, same as omlx |
| mlx_lm_server | existing `mlx_lm_server_start <target> <draft> 8001` | existing `mlx_lm_server_stop` | Single target+draft pairing per process on a fixed port; a second pairing auto-replaces the first, same as omlx |

**Same-provider replacement.** For omlx/mtplx/mlx_lm_server, these
processes can only ever serve one model. Starting a new model on a provider
that's already running a *different* model on that same provider silently
stops the old one first — there is no "run two omlx models at once" option,
because the omlx server itself has no per-model unload primitive (only
`omlx stop`, which takes the whole server down). The confirmation dialog
(§4) calls this out explicitly ("this will also stop `<old model>`") so it's
never a silent surprise to the user, even though the implementation doesn't
block on it.

**Cross-provider concurrency** is unrestricted at the software level: an
ollama model, an omlx model, and an mtplx model can all be `running = true`
at once. The only guardrail is the advisory dialog/warning in §3–§4 — this
is a policy relaxation, not a claim that the hardware has more headroom than
before; the user is trusted to judge that tradeoff per the design goal.

`local_control.start_local_model()` changes from "stop *every* local
provider via `stop_all_local_providers()`, then start" (issue #65's
behavior) to "stop only a currently-running model **on the same provider**
(if any), then start; leave every other provider's running model alone."

**Benchmarking is unaffected.** `modelman benchmark`'s isolation path
(`bin/llm-isolate-provider`'s `stop-all`/per-provider modes,
`benchmark/isolation.py`'s `isolate_provider`/`stop_all_local_providers`)
keeps its full stop-everything-else behavior — clean, single-model
measurement is still a hard requirement for benchmark accuracy, and that
code path does not go through `local_control.py`'s new same-provider-only
logic at all.

### 3. modelman CLI

- **`modelman start <model_id>`**: resolves same-provider conflicts per §2
  (auto-stops a same-provider model if one is running), then starts.
  Before starting, if any *other* local model (any provider) is currently
  running, prints a warning to stderr listing them by id and proceeds —
  never blocks, stays script/cron/agent-friendly.
- **`modelman stop <model_id>`**: stops exactly one running local model.
- **`modelman stop --all`**: stops every currently-running local model
  (the full blast radius `modelman stop` has today).
- Bare `modelman stop` with neither a model id nor `--all` is a usage
  error asking the caller to pick one — this prevents a script that used to
  write `modelman stop` (meaning "stop the one thing that might be
  running") from silently gaining a "stop everything" side effect it never
  asked for as concurrency was introduced.
- `modelman start`'s existing no-arg inventory listing (issue #65's
  three-way registered/missing/discovered view) extends its `running`
  column from "is this the one marked model, probed" to "is this model's
  flag `true` and probe-verified" for every local model, not just one.

### 4. modelman TUI (`ModelScreen`)

- New keybinding `s` toggles `running` for the cursor row, when the row is
  a local model that is `ready`. No-op (or a clear notification) on cloud
  rows and non-ready local rows — cloud visibility stays exclusively
  `x`/`exposed`, unchanged.
- Turning a model **on** while any other local model is currently verified
  running shows a confirm dialog (styled like `ConfirmExitDialog`) listing
  the currently-running models by id, with an explicit note when the
  target's own provider will auto-replace one of them ("starting this will
  also stop `<id>`, since omlx can only serve one model at a time"). `Yes`
  proceeds, `No` cancels — this is the TUI's only gate; there is no
  "maximum concurrent models" hard limit.
- A new indicator in the model table (a glyph column, alongside EXPOSED)
  shows live verified-running state for local rows — computed the same way
  §1 describes, extended from checking one marker to checking every local
  model's flag+probe.
- Toggling a model **off** stops it per §2's per-provider stop action and
  clears its `running` flag.

### 5. wt (`internal/localgate`)

- `Resolve`/`Apply` become plural: instead of resolving one marker, build
  the set of local model ids that are (a) `running = true` in
  `modelman.toml` and (b) confirmed by a live probe — the same per-provider
  probes issue #65 already has (`ollamacheck.Loaded` / `ollama ps` for
  ollama; name-checked `/v1/models` for omlx/omlx-6bit, mlx_lm_server, and
  the new mtplx branch), just run once per candidate instead of once for a
  single marker.
- The picker offers: cloud models under the existing exposed/ready
  predicate, **union** local models under that same predicate **and** the
  new verified-running check — "only running local models appear, after
  other filters are applied," matching cloud's existing filter ordering.
- **A flagged-but-unverified local model is silently excluded from the
  picker, not fatal.** This is a deliberate relaxation of issue #65's
  behavior (today, any stale marker exits wt fatally for every launch):
  with several local models potentially running, one drifting out from
  under modelman (crash, manual stop, a benchmark run) should not block
  launches that don't need it.
- **Exception: a `-M`/`--model` pin naming a local model that fails its
  probe still errors** with the existing "start it with `modelman start
  <id>`" message — a pin is an explicit request for that specific model, so
  silently dropping it and falling through to a picker (or an unrelated
  model) would be surprising. This exactly matches issue #65's existing
  pinned-model-guard behavior, just no longer coupled to the *global* marker
  state.
- Emergent fix: today, a `modelman benchmark` run tears down whatever was
  running via the bash isolation helper without touching the
  `[local].running_model` marker, leaving it stale until wt hits the fatal
  exit and the user notices. Under this design, that stale flag simply
  fails its probe and quietly drops out of the picker — no code change
  needed beyond what's already described above.

### 6. Restart handling

No explicit "reset everything to stopped on restart" step exists or is
needed. After a reboot, nothing is actually loaded, so every live probe
fails regardless of what any `running` flag says on disk — §1's rule means
every consumer already treats a probe-failing flag as not-running. modelman
may opportunistically clear stale `running = true` flags it notices (e.g.
during the TUI's existing background reconcile worker), as a housekeeping
nicety — not required for correctness, since every read path already
self-heals.

## Edge cases

- **Starting an already-running model** (flag true, probe succeeds) is a
  no-op / "already running" message, both CLI and TUI — matches issue #65's
  existing idempotency guarantee, now per-model instead of global.
- **Starting a model whose flag is true but whose probe fails** (crashed,
  stopped outside modelman) clears the stale flag and performs a full
  start, exactly as issue #65's `start_local_model` already does for the
  single marker.
- **Stopping a model that isn't running** (flag false, or flag true but
  probe already fails) is a no-op.
- **`modelman stop --all` with nothing running** is a no-op.
- **Starting a cloud model via `modelman start`** is still rejected — this
  command only ever manages local models.
- **omlx/mtplx/mlx_lm_server same-provider replacement failing mid-swap**
  (new model fails to start after the old one was stopped) leaves that
  provider's slot empty — matches issue #65's existing failure handling for
  the isolate step (clear the stale marker, surface the error), just scoped
  to one provider's flag instead of the global one.
- **wt launched with no local model running at all** → cloud models only,
  same as today.
- **A model registered under a provider with no start/stop support** (a
  provider id outside `SUPPORTED_PROVIDER_IDS`) is rejected by `modelman
  start`, same as today.

## Testing

- **modelman:** unit tests for the new per-model `running` field
  (read/write/default), same-provider-only stop-and-replace logic (mocked
  isolation calls), CLI `stop <id>` vs `stop --all` vs bare `stop` (usage
  error), the TUI's `s` keybinding and confirm dialog (including the
  same-provider-replacement notice), and the extended inventory listing.
  Contract test update for the `running` field in the shared
  `modelman.sample.toml` fixture (retiring the `[local]` table from the
  fixture).
- **wt:** unit tests for `localgate` resolving a set instead of one marker
  (multiple verified-running models, a mix of verified/unverified, all
  unverified → cloud-only), the pinned-model guard's exception behavior,
  and the per-provider probes extended to run over a candidate set. Update
  the shared contract fixture the same way as the modelman side.
- **Cross-language contract:** `docs/contracts/modelman.sample.toml` and its
  loaders in both `modelman/tests/contracts/` and `wt/internal/config`
  contract tests must agree on the new per-model `running` field and the
  absence of `[local]`.

## Out of scope

- A true "run N omlx models at once" capability — would require the omlx
  binary itself to support serving multiple models from one process with
  independent unload, which it doesn't today.
- A configurable maximum-concurrent-local-models limit — the design is
  advisory-only (a warning dialog/message), not an enforced cap.
- Changing `modelman benchmark`'s isolation behavior or consolidating it
  with this new lifecycle path (tracked separately, as it was in issue
  #65's design).
