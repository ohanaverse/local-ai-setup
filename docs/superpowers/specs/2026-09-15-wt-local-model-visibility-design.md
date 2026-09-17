# Design: Decouple local-model visibility in wt's picker from `exposed`

## Problem

The 2026-09-14 local-model lifecycle design ("Multi-model local lifecycle in
the modelman TUI") made a local model's visibility in wt's picker require
**both** the existing exposed/ready predicate **and** a live-verified
`running` flag. In practice this means starting a local model with
`modelman start <id>` is not enough to make it appear in wt — the model must
*also* have been separately exposed (`modelman expose <id>`, or the TUI's `x`
key) at some point. A model that was registered without ever being exposed
(e.g. via the discovered-model auto-register path predating a later manual
`unexpose`, or simply never exposed at all) shows the running indicator in
modelman's own TUI (which only checks `running`+probe) while remaining
invisible in every `wt`-based agent launcher (`claude-wt`, `pi-wt`, ...),
because wt's `IsExposed` (`wt/internal/config/config.go`) rejects it before
the running-verification stage ever runs.

This was surfaced by a concrete case: `mtplx/Youssofal--Qwen3.6-...` was
started via `modelman start` (`running = true`) but had `exposed = false` in
`modelman.toml`, so it never appeared in `claude-wt`/`pi-wt`'s model list
despite showing as running in modelman.

## Goal

- For **local** models, visibility in wt's picker is governed **solely** by
  the existing live-verified `running` gate (`internal/localgate`) — no
  separate exposure step required. `exposed`/`ready` no longer gate local
  models at all in wt.
- For **cloud** and **native** models, the existing `exposed`(+`ready`)
  predicate is **unchanged**.
- Agents whose route to a local provider is forced through LiteLLM (protocol
  mismatch — e.g. `claude` only speaks `anthropic`, `mtplx` only serves
  `openai-chat`) must still work once a model is visible: LiteLLM can only
  route to what's in its `model_list`, which the `exposed` flag controls.
  `modelman start` therefore keeps that flag (and the LiteLLM config) in sync
  automatically, so the common "start it, then launch an agent against it"
  workflow needs no separate manual expose step.

## Approach

### 1. wt: `IsExposed` gains a local-model bypass

`wt/internal/config/config.go`'s `IsExposed` becomes:

- `m.Native` → `true` (unchanged).
- Location resolves to `local` (`ResolveLocation(m) == LocationLocal`, no
  error) → `true` **unconditionally**. Visibility is deferred entirely to
  the existing Stage-2 running-gate (`internal/localgate.Apply` →
  `Config.FilterToRunningLocal`), which already implements "only
  live-verified-running local models pass" — no changes needed there.
- Location unresolvable (registry data gap) → fall through to the existing
  exposed+ready check, fail-closed, matching today's conservative handling
  of unresolvable locations elsewhere in this predicate.
- Cloud → unchanged: requires `exposed = true`, exempt from the `ready`
  gate.

Net effect: `EligibleModelsIn` (Stage 1: provider/tag/family/exposed filter)
no longer excludes any local model on `exposed`/`ready` grounds; only
`localgate.Apply`'s live probe (Stage 2) decides local-model membership in
the final eligible list. The two-stage pipeline itself is unchanged — only
what Stage 1 checks for local models changes.

### 2. modelman: `start_local_model` keeps LiteLLM in sync

`modelman/src/modelman/local_control.py`'s `start_local_model()` calls
`expose_model(registry, state, resolved_id, litellm_path)` (the same helper
the discovered-model auto-register path already uses) immediately alongside
its existing final `running=True` flag write, wrapped in try/except so a
failure to expose (e.g. a transient LiteLLM config write error) downgrades
to a warning on the `StartResult` rather than aborting an otherwise-successful
start — mirroring the existing pattern at the discovery-path's own
`expose_model` call.

This is idempotent: `expose_model` only writes the LiteLLM config and
restarts the proxy when something actually changed, so starting an
already-exposed model is a no-op on this front.

> **Superseded 2026-09-17:** the paragraph below no longer reflects the
> code. `stop_local_model()`/`stop_all_local_models()` now un-expose a
> stopped model instead of leaving `exposed` sticky — see
> `modelman/CLAUDE.md`'s "Local-model lifecycle" section for the current
> contract.

`stop_local_model()` is **not** changed. `exposed` stays sticky after a
stop — consistent with how `ready`/other flags already behave when a model
is merely not currently running (a model can be `exposed` and `ready` while
`running = false`, its normal idle state; this is symmetric).

### 3. Accepted edge case

If a user explicitly unexposes (`modelman unexpose <id>`, or the TUI's `x`
key) a model that is still `running = true`, wt's picker still shows it
(local visibility no longer checks `exposed`), but an agent whose route to
it is forced through LiteLLM will fail at request time (LiteLLM has no
`model_list` entry for it). modelman's own TUI still shows the accurate
state (`–` in EXPOSED, running indicator still lit), so the signal isn't
hidden, just not surfaced inside wt's picker itself. No guardrail (refusing
or warning on `unexpose` of a running model) is added for this — it is a
narrow, self-inflicted case and out of scope here.

## Edge cases

- **Model started, never exposed** (the motivating case): now visible in
  wt immediately after `modelman start`, because start now exposes it too.
- **Model running via `ollama` (already multi-tenant)**: same rule applies —
  any `running=true`, live-probe-verified ollama model is visible
  regardless of its `exposed` flag; `start_local_model` exposes it the same
  way as any other provider.
- **Model exposed but not running**: unchanged — not visible in wt (the
  running-verification gate still excludes it), same as today.
- **Registry data gap (unresolvable location)**: falls back to the old
  exposed+ready check, fail-closed — unchanged from today's behavior.
- **Explicit unexpose of a still-running model**: see "Accepted edge case"
  above.

## Testing

- **wt** (`wt/internal/config`):
  - Update `TestIsExposedPredicate`'s local-model cases: a local model with
    `exposed=false` (or `ready=false`) now returns `true` from `IsExposed`
    — rewrite the case previously named "flag + not-ready (local) = not
    exposed" to reflect the new contract, and add an explicit
    `exposed=false` local case.
  - Add/extend an integration-style test combining `EligibleModelsIn` and
    `localgate.Apply` (or the existing `Apply` test suite) showing: (a) a
    `running=true, exposed=false` local model is eligible end-to-end for an
    agent that supports its provider, and (b) a `running=false` local model
    is excluded regardless of `exposed`.
  - Update the shared contract fixture (`docs/contracts/modelman.sample.toml`)
    and its wt-side contract test if either encodes the old expectation.
- **modelman** (`tests/test_local_control.py`):
  - New test: a successful `start_local_model()` on a previously-unexposed,
    ready local model sets `exposed=True` in state (assert via the
    persisted `StateStore`, mocking `expose_model` or checking its real
    effect against a `tmp_path`-based LiteLLM config, consistent with this
    test file's existing patterns).
  - New test: `stop_local_model()` does not clear `exposed`.
  - New test: a start whose `expose_model` call raises surfaces a warning
    on `StartResult` rather than raising/aborting.

## Out of scope

- Any guardrail on `unexpose`/`x` for a currently-running model (see
  "Accepted edge case").
- Changing modelman's own TUI EXPOSED column semantics or its relationship
  to `exposed` — this design only changes what **wt** treats as visible for
  local models; modelman's own display stays keyed on `exposed` as today.
- Re-litigating the live-probe running-verification mechanics themselves
  (`internal/localgate.ResolveAll`/`Apply`, per-provider probes) — unchanged
  by this design.
