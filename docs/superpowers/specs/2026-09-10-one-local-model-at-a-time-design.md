# Design: One Local Model at a Time (issue #65)

## Problem

The local machine is resource-constrained (Apple Silicon GPU/RAM). modelman and
wt should only ever have **one local model running at a time**. Today there is
no such gate for normal (non-benchmark) usage: a user can start a model on
`ollama`, another on `omlx`, and another on `mlx_lm_server` simultaneously,
each competing for the same unified memory.

The only existing "one at a time" enforcement is `bin/llm-isolate-provider`,
which is **benchmark-only** — it stops all local providers except the one being
benchmarked. It does not gate normal wt launches.

## Goal

- modelman owns the local-model lifecycle: starting and stopping a local model
  is an **explicit step** taken in modelman.
- wt's model picker surfaces **only the single running local model** alongside
  the cloud models — a hard block by construction, since a non-running local
  model is simply not offered.
- wt verifies the marked local model is actually available; if not, it exits
  with a message telling the user to start the model in modelman.

## Approach

**A — modelman CLI commands delegating to the existing isolation helper.**
`modelman start`/`stop` reuse `bin/llm-isolate-provider` (which already stops
all other local providers, then starts+warmups the target — exactly the
single-model enforcement). The genuinely new code is the marker in
`modelman.toml` and the wt picker filter + availability verification.

The bash scripts remain necessary for **benchmark isolation** (used by both
`modelman benchmark` and the legacy `benchmarks/*-benchmark` scripts), so they
are not retired. Consolidating benchmark isolation into native modelman
start/stop is a possible follow-up, out of scope here.

## Marker schema

`~/.config/local-ai/modelman.toml` gains a `[local]` table (wt already reads
this file read-only via `loadModelmanState`):

```toml
[local]
running_model = "ollama/qwen3.8:27b-mlx"   # registry model id; absent/empty = none running
```

The value is the **full registry model id** in `<provider_id>/<model_name>`
form. Both sides split on the **first** `/` only (the model name itself may
contain a `/`, e.g. `openrouter/z-ai/glm-5.3-flash`):
- provider = everything before the first `/`
- model name = everything after

## modelman CLI

### `modelman start <model_id>`

1. Validate the id exists in the registry and is a **local** model (reject
   cloud models — they cannot be "running locally").
2. Stop any currently-running local model (delegate to the isolation helper's
   "stop all" path).
3. Start the requested model: `llm-isolate-provider <provider>` with the
   `LLM_ISOLATE_*_MODEL` env var set to the specific model name.
4. Write the marker (`[local].running_model = <model_id>`) to `modelman.toml`.

Idempotent: starting an already-running model is a no-op (or a clear "already
running" message).

### `modelman stop`

1. Stop the running local model (delegate to the isolation helper's "stop all"
   path).
2. Clear the marker.

No-op when nothing is running (marker already clear).

> **Note:** the isolation helper currently exposes only per-provider modes (no
> "stop all" mode). `modelman stop` therefore needs a new "stop all local
> providers" capability — either a new mode in `bin/llm-isolate-provider` or
> modelman invoking the helper's stop logic per provider. This is an
> implementation detail for the plan, not a design blocker.

## wt picker filter + verification

At launch / picker build, wt applies the gate:

1. **Read the marker.** If `running_model` is absent/empty → no local model is
   running → wt shows **only cloud models**. A local model cannot be selected
   because it is not offered.

2. **If a marker is present**, wt resolves the provider from the id prefix and
   **verifies the marked model is actually available** by probing that
   provider's endpoint:
   - `ollama` → `ollamacheck.Check` (already exists)
   - `omlx` → probe `http://localhost:8000/v1/models`
   - `mlx_lm_server` → probe `http://localhost:8001/v1/models`
   - (future `mtplx` → probe `http://localhost:8000/v1/models`)

3. **Verification outcome:**
   - **Available** → wt shows the marked local model **plus** all cloud models.
   - **Not available** (stale marker — model stopped outside modelman, or
     crashed) → wt **exits** with a message like:
     *"local model `ollama/qwen3.8:27b-mlx` is not running — start it with
     `modelman start ollama/qwen3.8:27b-mlx`"*.

**Pinned-model guard:** if a local model is pinned via `-M`/`--model` and it
does not match the marked running model, wt rejects it with the same "start it
in modelman" message.

**Cloud models are unaffected** — the gate only filters local models.

## Edge cases

- **No marker, user wants a local model** → wt shows only cloud models; the
  user must run `modelman start <id>` first. Clear, by construction.
- **Stale marker** (model stopped/crashed outside modelman) → wt's availability
  probe fails → exit with "start it in modelman" message.
- **`modelman start` on a cloud model** → rejected.
- **`modelman start` on an already-running model** → idempotent no-op.
- **`modelman stop` with nothing running** → no-op, marker already clear.
- **Marker points to a model that no longer exists in the registry** → wt
  treats it as unavailable → same exit message.
- **wt launched with `-M` pinning a local model ≠ marked model** → rejected
  with the "start it in modelman" message.

## Testing

- **modelman:** unit tests for `start`/`stop` — marker write/clear, cloud-model
  rejection, idempotency, delegation to the isolation helper (mocked). Contract
  test for the `[local]` table in `modelman.toml`.
- **wt:** unit tests for the picker filter (marker present/absent,
  available/unavailable), the pinned-model guard, and the availability probe
  per provider (mocked endpoints). Contract test that wt reads the `[local]`
  marker from the shared fixture.
- **Cross-language contract:** extend the existing `docs/contracts/` fixtures
  so both modelman (Python) and wt (Go) agree on the `[local].running_model`
  schema.

## Out of scope

- Adding the `mtplx` provider (issue #66) — a separate follow-up; the gate is
  designed to accommodate it (a new probe branch).
- Consolidating benchmark isolation into native modelman start/stop.
