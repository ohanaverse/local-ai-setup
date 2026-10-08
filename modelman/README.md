# modelman

A CLI for managing LLM models across providers (Ollama,
oMLX, MTPLX, mlx_lm_server, OpenRouter, and native agent providers like
`claude`, `codex`; the llama.cpp provider is retired — see
`../docs/reference/provider-artifacts.md`). Models and providers live in a
shared `registry.toml`; per-machine state (ready markers, running hints) lives
in `modelman.toml`.

**The terminal UI is disabled.** It showed every model in one table, queued
changes (delete/ready/move) applied on exit, and started/stopped local models.
wt writes `registry.toml` now, so bare `modelman` prints where to go and exits
1 — see [TUI](#tui) for what replaces each thing it did. The subcommands below
still work.

**Requires `wt` on PATH.** LiteLLM management is owned by the `wt` launcher
(since 2026-09-21): modelman never edits LiteLLM's `config.yaml` or restarts
the proxy itself — it asks `wt` to reconcile the routes, via `wt litellm ...`.
Install `wt` with `make install` from the repo root (that installs both
components). Without `wt`, route syncs and the `modelman litellm ...` commands
fail with a clear error before changing any state.

## Install

modelman is a `uv` project nested inside the monorepo. Run all commands from
the `modelman/` directory:

```bash
cd modelman
uv sync
uv run modelman --help
```

Because the repo root has no `pyproject.toml`, running `uv run modelman` from the
root will fail. Always `cd modelman` first.

## Configuration

modelman reads three files under `~/.config/local-ai/` (each overridable
with an env var). LiteLLM's own `config.yaml`, the proxy restart and the routing
on/off state are owned by `wt` (see below):

| File / setting | Purpose | Env override |
|----------------|---------|------------|
| `registry.toml` | Canonical model/provider definitions (shared; `wt model init` also writes it) | `WT_REGISTRY` (legacy alias `MODELMAN_REGISTRY`) |
| `modelman.toml` | Per-machine mutable state: download markers and the `running` hint (`wt` reads none of the per-model state, and not `price_refresh_last_run` — only the legacy `[litellm]` table, read-only) | `MODELMAN_STATE` |
| `settings.yaml` | User preferences (theme) | `MODELMAN_SETTINGS` |
| LiteLLM `config.yaml` | Path to the LiteLLM config file. **wt writes it**; modelman only reads it for `modelman usage`, and skips its route sync when the file is missing. Both resolve it the same way: `WT_LITELLM_CONFIG`, then legacy `MODELMAN_LITELLM_CONFIG`, then the default | `WT_LITELLM_CONFIG` (legacy `MODELMAN_LITELLM_CONFIG`) |
| LiteLLM proxy restart | Done by wt after a route change: `WT_LITELLM_RESTART_CMD` (legacy alias `MODELMAN_LITELLM_RESTART_CMD`), else `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy` | (wt-owned) |

### `registry.toml`

```toml
[[providers]]
id = "ollama"
name = "Ollama"
location = "local"          # "local" | "cloud"
[providers.auth]
type = "none"                # "none" | "api_key" | "oauth" | "native"
base_url = "http://localhost:11434"

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"
[providers.auth]
type = "api_key"
base_url = "https://openrouter.ai/api/v1"
secret_ref = "sk-or-v1-..."

[[families]]
name = "ornith"
display_name = "Ornith"       # optional; omitted when unset

[[models]]
id = "ollama/ornith:35b"     # globally unique, stable key
family = "ornith"
provider_id = "ollama"
model_name = "ornith:35b"    # provider-specific (ollama tag, HF repo, …)
location = "local"           # optional override; "local" | "cloud". Falls back to the provider's location.
model_info = { supports_function_calling = true }   # optional LiteLLM-style keys

[models.cost]                # optional; omit entirely when unknown/unset
input_price_per_million  = 0.50
cache_price_per_million  = 0.25
output_price_per_million = 1.00
subscription_price       = 19.99
subscription_period      = "month"   # "month" | "year"

[models.fetch]               # optional, for HF-backed providers
repo = "org/repo"
files = ["model.gguf"]
quantizations = ["Q4_K_M"]
```

`model_info` is freeform and copied into LiteLLM's `model_list` entry when
`wt` routes the model. `wt model add` and the Models tab's form fill it for
Ollama models from `ollama show <name>`, as the disabled TUI's add dialog
did; in a hand edit, read the capabilities
`ollama show <name>` lists and set the matching keys yourself (e.g.
`tools` → `supports_function_calling = true`, `vision` →
`supports_vision = true`).

`cost` is validated on load: each price field must be a non-negative
finite number; `subscription_period` must be `month` or `year` when
`subscription_price` is set. Unknown keys inside `[models.cost]` are
preserved on round-trip so hand-edited fields survive. Omit the whole
`[models.cost]` table to leave cost unset (the TUI shows `—`).

Optional time-windowed prices override the flat (default) per-token
prices during their windows — e.g. Ollama's off-peak pricing, written by
`modelman ollama-catalog sync`:

```toml
[[models.cost.time_prices]]
label    = "off-peak"          # informational
timezone = "UTC"               # IANA name
input_price_per_million  = 0.66
cache_price_per_million  = 0.022
output_price_per_million = 1.98
windows = [                    # union; start inclusive, end exclusive, end may be 24:00
  { days = ["mon","tue","wed","thu","fri"], start = "00:00", end = "12:00" },
  { days = ["mon","tue","wed","thu","fri"], start = "18:00", end = "24:00" },
  { days = ["sat","sun"],                   start = "00:00", end = "24:00" },
]
```

At an instant, the first row with a matching window supplies each price it
sets; omitted fields and non-matching times use the flat prices
(`time_pricing.price_at`). Stored only today — the TUI COST column and wt
show the flat prices.

The old `kind = "free" | "per_token" | "subscription"` schema is still
accepted on read and is silently migrated to the flat fields above on
save. `usage_tier` has been removed; use the pricing fields directly.

### `modelman.toml`

```toml
[model_state."ollama/ornith:35b"]
ready = true
disk_path = "ollama:ornith:35b"
size_bytes = 123456789
running = false
```

There is no `exposed` flag (removed in #179). Every cloud model configured in
`registry.toml` is routed, and a local model while it runs (Ollama: while it
is pulled), with or without an entry: modelman ignores a legacy `exposed`/`litellm_exposed` key on read and
drops it on the next save, and `wt` derives the actual routes from the registry
plus live probes. To see what is routed, ask `wt litellm list`.

`ready` is the provider-agnostic readiness flag. For reconcilable providers
(Ollama, oMLX, MTPLX, mlx_lm_server; llama.cpp was retired 2026-09-07) it means "the model is present on this machine".
For flag-only providers (OpenRouter, native agents like `claude`) it means
"the user has marked this model as available"; there is nothing to download
or delete on disk.

`running` (local models only; defaults to `false` when absent) records that
`modelman start` started this model and has
not stopped it. It is a hint, not ground truth: modelman and `wt` both
confirm it with a live probe of the provider before treating the model as
running, and a flag whose probe fails is treated as stopped (and
opportunistically cleared). Several local models may be `running = true` at
once; oMLX is a pool (starting a model loads it beside the others; modelman hands the
start and stop to `wt`), while single-model-per-process providers (MTPLX,
mlx_lm_server) still replace their own occupant when a different model on that
provider starts.

Family display names now live in `registry.toml`'s `[[families]]` section.
The legacy `[families.*]` table here is still loaded as a read-side
fallback; nothing writes it.

This file is optional — a fresh install starts with an empty store.

## Usage

### CLI

```bash
modelman                        # TUI disabled: prints where to go in wt, exits 1
modelman sync                   # reconcile configured models against providers, then sync LiteLLM routes
modelman litellm status|on|off|set   # passthroughs to `wt litellm ...`
modelman start                  # list local models: registered+on-disk, registered-but-missing, discovered (each under its id, running ones marked)
modelman start <model>          # start (and route) a local model — a registry entry or an on-disk artifact discovered by probe
modelman stop <model-id>        # stop one local model — registry id or discovered id (its route goes too, except a pulled Ollama model's)
modelman stop --all             # stop every running local model
modelman provider isolate|stop|stop-all|restore|list   # low-level provider lifecycle (benchmark isolation)
modelman refresh-prices         # refresh OpenRouter-priced models' per-token prices
modelman delete-family <name>   # remove an empty family's leftover registry entry
modelman usage report           # wt launch history joined with LiteLLM spend
modelman migrate                # one-time import of legacy config (see below)
```

`start <model>` accepts a registry id, a model's provider-side name, or an
on-disk artifact no registry entry claims (its discovered id or bare artifact
name) — started as-is, no registration needed. The discovered id is
`<family>/<artifact>`: `ollama/<name:tag>`, `omlx/<model directory name>`,
`mtplx/<org>/<name>` (MTPLX keeps the `/`). On a registry whose only
omlx-family row is `omlx-6bit`, an unregistered omlx artifact is still listed
and started as `omlx/<dir>`, through that row. It is the id wt lists and routes
the model under, the id `modelman stop` takes, and the id `modelman start`
with no argument prints in its `Discovered` section — with `(running)` beside
a model modelman started that a live probe confirms. A registry entry for a
local model is an optional overlay (family, tags, cost, `model_info`) on what
is on disk; the convention for a new one is `id = "<provider family>/<model_name>"`
(`omlx` for an `omlx-6bit` model)
(see `../docs/guides/02-providers-and-models.md` Step 3). Several
local models can run at once; oMLX holds several loaded models (`modelman
start`/`stop` on one go through `wt start`/`wt stop` and touch only that model;
`modelman stop --all` stops the service), while MTPLX and
mlx_lm_server serve one model per process, so starting another model on one
of them replaces its current model. `modelman provider isolate
<ollama|omlx|omlx-6bit|mtplx>` stops the other providers and starts one (used
for benchmarking); `restore` brings them all back.

### TUI

> **Disabled.** Bare `modelman` no longer opens the TUI: it prints where to go
> in wt and exits 1. wt writes `registry.toml` now. What replaces the screen
> is wt:
>
> - **create the registry and its provider rows** — `wt model init`
> - **add, edit or remove a model** — `wt model` (the Models tab of
>   `wt config`), or `wt model add`, `wt model edit`, `wt model rm`; the
>   routes are synced for you (`../docs/guides/02-providers-and-models.md`
>   Step 1)
> - **download a model** — the provider's own tool, since wt downloads
>   nothing: `ollama pull <name:tag>`;
>   `hf download <org>/<repo> --local-dir ~/.omlx/models/<repo>` for oMLX;
>   `mtplx pull <org>/<name>` for MTPLX (guide 02 Step 5)
> - **start or stop a local model** — `wt start` / `wt stop` (or
>   `modelman start` / `stop`, which still work)
> - **see what is on disk and running** — `wt start` (its picker), or
>   `wt served <provider>` for what a server is serving
>
> The rest of this section describes the screen as it was; none of its keys
> can be pressed today.

The TUI has a single screen:

- **Model screen** — the app's only/root screen: a single table of every
  model across every family, sorted family · location (local before
  cloud) · provider · model name (columns: family · provider · model ·
  loc · status ✓/○/↓/↑/✗/→/+ · running · cost · size). An on-disk
  artifact with no `registry.toml` entry shows up as an extra row with
  status `+`; pressing `enter`/`e` on it opens a registration dialog
  (Provider/Model prefilled and locked) instead of the normal edit
  dialog, so you just pick a family to register it — the explicit way to
  give a discovered model an overlay; nothing registers one implicitly. The
  `+` row shows RUNNING `●` while the model runs (started with
  `modelman start <artifact>`; `s` does not act on a `+` row). The
  `+`-row form registers the model under its discovered id
  (`<family>/<artifact>`, e.g. `mtplx/org/name`), so registering it changes
  neither its route nor its usage history. LOC is an icon
  (↗ cloud / ▤ local / `—` when unknown), and
  RUNNING shows `●` for a local model modelman started (verified by a
  live probe when the TUI opens) and `-` otherwise; COST
  renders per-token input/cache/output prices per million tokens as
  three space-delimited values, each a leading-space-padded 2-digit
  integer part and 4 decimal places (e.g. ` 2.0000`, `12.5000`) so prices
  over $10/million stay aligned; a missing individual price shows as
  `-------`; no pricing at all shows a single `-`. There is no
  SUB (subscription) column, though subscription pricing can still be
  set via the Add/Edit dialogs. The row's on-disk path appears in a
  details panel below the table (`path: —` when unknown), and a LiteLLM
  on/off status line sits below the pending-changes bar. Keys: `a` add
  model (family Select offers every known family plus a "+ New family…"
  option that reveals a text field for a brand-new one), `e` edit
  (id/provider/location/family all fixed — re-homing a model isn't
  offered in this dialog), `d` queue delete (works on any model —
  apply skips the on-disk removal if the artifact is already gone, but
  still cleans registry/state), `r`
  toggle ready (ready-on queues a download/pull, or a flag flip for
  cloud/native providers and MTPLX, which manages its own cache;
  ready-off queues removal of the on-disk artifact; pressing `r` again
  cancels the queued change),
  `s` start/stop a ready local model (always asks for
  confirmation; runs immediately, not queued, and syncs the LiteLLM routes
  afterwards), `l` toggle LiteLLM routing on/off, `enter` edit,
  `escape` shows the apply/discard/cancel dialog if anything is queued,
  otherwise quits the app (if a start/stop or other background task is
  still running, you are offered "Keep waiting" or "Force quit"). Reconcile runs automatically on mount — there
  is no manual reconcile key. The cursor survives every reload —
  reconciling or toggling a row leaves you on that row. Provider and
  family dropdowns list options alphabetically.

  Add/Edit dialogs include a cost section with two independent
  checkboxes: per-token pricing (input, cache, and output price per
  million tokens) and subscription pricing (price + `month`/`year`
  period). Enable either, both, or none to leave cost unset. At least
  one per-token price is required when per-token pricing is enabled;
  both price and period are required when subscription pricing is
  enabled.

All model changes (adds, edits, deletes, ready toggles, moves) are queued in
memory — nothing downloads or writes to disk while
the TUI is open (add/edit are the one exception: registry.toml is
persisted immediately, so a discarded session doesn't lose a
concurrently-typed edit). `Escape`/`Ctrl+Q` with a pending queue shows a
confirmation dialog listing the pending set; `Apply` or `Discard` both
exit the app. On Apply, `main.py` runs the queue in the plain terminal
after the TUI closes: **deletes, then moves, then ready changes
(downloads/clears/flag flips)**, printing
provider progress and a thin lifecycle line per operation to stdout,
then writes `registry.toml` + `modelman.toml` once and runs one
`wt litellm sync`, so the routes follow whatever the queue changed. A failed operation
is reported in an error summary at the end and the process exits
non-zero. `Ctrl+C` mid-run stops the queue: every step that had already
finished (deletes, moves, completed downloads and ready flips) is saved to
`registry.toml`/`modelman.toml`, a partially downloaded artifact is
removed, the remaining steps are skipped, and modelman prints
`Cancelled: N steps completed, M remaining skipped.` and exits non-zero. A
delete for a not-on-disk model is legal: the on-disk removal is
skipped, but the registry/state cleanup, lifecycle events, and the
closing route sync still run.

All dialogs share a layout convention: the cancel/default button is
rightmost, the primary action is to its left, and pressing `Escape`
cancels (this works even when an Input is focused). Destructive prompts
(`ConfirmModal`, `ConfirmExitDialog`) focus the safe button on open so a
reflexive `Enter` is never destructive.

### LiteLLM routes

There is no expose/unexpose step. **What is configured is what is routed**
(#179): `wt` reads `registry.toml` and the live providers and keeps
`config.yaml` in step — configured cloud models always, a local model while
it runs or, for Ollama, while it is pulled. A local model needs no registry
entry for that: one with none is routed under its discovered id,
`<family>/<artifact>`. modelman changes state and then asks wt to reconcile:

```bash
wt litellm list      # what is routed right now — the authoritative answer
wt litellm sync      # reconcile by hand; modelman runs this itself after a change
```

modelman runs one `wt litellm sync` after each subcommand that can affect
routing: `modelman sync`, `migrate`, `refresh-prices`, `ollama-catalog sync`
and every `start`/`stop`. (The disabled TUI did too: on an exit that changed
`registry.toml`, after a queue it applied, and on a mount that found a
`running` flag gone stale.) A hand edit of `registry.toml` is seen by no
command — run `wt litellm sync` after it yourself. wt restarts the proxy only when `config.yaml`
actually changed, so a no-op sync costs nothing. Warnings — a provider whose
probe couldn't be trusted, routes deliberately left alone — are printed to
stderr and never fail the command.

A route wt wrote carries a `model_info.wt_managed` marker, and a hand-written
`model_list` entry (no marker, and a name that is not a registry id) is never
touched by wt — a discovered model's route never replaces one of the same
name; `wt litellm list` marks those `(hand-written)`. Preview what a
sync would change with `wt litellm sync --dry-run`. To stop routing a model,
remove a cloud model from `registry.toml`, or stop a local model (an omlx
model drops only its own route; a pulled Ollama model stays routed until it is
removed from Ollama) rather than editing `config.yaml` — deleting a local
model's registry entry alone does not unroute it while it runs. Details: `../docs/guides/04-litellm-config.md` §2.

LiteLLM's `config.yaml` lives at `~/.config/litellm/config.yaml` by default
(wt honors `WT_LITELLM_CONFIG`, legacy alias `MODELMAN_LITELLM_CONFIG`). wt's
writes are comment-preserving (yaml.v3): sections wt doesn't own and comments
survive, but list indentation is normalized and blank lines are dropped on the
first wt write. wt also enforces the launcher-required settings on every write
(`litellm_settings.drop_params`, the anthropic-messages chat-completions
switch, and the per-row `ollama_chat`/`openai` bridge parameters) — see
`../wt/CLAUDE.md` and `../wt/docs/wt-agents/litellm-troubleshooting.md`.

The proxy restart is wt's too: `WT_LITELLM_RESTART_CMD` (legacy alias
`MODELMAN_LITELLM_RESTART_CMD`), else `launchctl kickstart -k
gui/$(id -u)/local.litellm.proxy`. The LiteLLM routing on/off state
(`modelman litellm status|on|off|set`) lives in wt's
`~/.config/agent-wt/config.toml`; modelman.toml's `[litellm]` table is only a
legacy read-only fallback that modelman round-trips untouched.

### Native providers

Providers whose `auth.type` is `"native"` (or whose id matches an agent in
`~/.config/agent-wt/config.toml`) represent models handled by external
agents (e.g. `claude`, `codex`). They have no download mechanics: there is
no disk path or size. `wt model init` adds a row for each agent in the wt
config (the TUI used to do that on launch).

### Sync

`modelman sync` reconciles the ready state of models already in
`registry.toml` against their providers — it never adds new models. Ollama
is reconciled via `ollama list`; oMLX, MTPLX and mlx_lm_server (and the retired
llama.cpp) via their on-disk model directories. Cloud providers (OpenRouter, native agents) are
configured explicitly and are not reconciled. Sync intentionally does not
propagate `cost` to providers; it is registry metadata only.

### One-time migration

`modelman migrate` imports the legacy `~/.config/local-ai/config.yaml` and
`~/.config/local-ai/families/*.yaml` (and, optionally, wt's
`config.toml` via `--wt-config`) into `registry.toml` + `modelman.toml`.
The legacy files are read-only inputs and are not written by the TUI.

It is safe to re-run as a repair: an existing `registry.toml` is read first,
and only providers, families and models it does not already have are added.
An entry already there is kept as it is, and so is the state `modelman.toml`
already holds for a model. A `registry.toml` that cannot be read stops the
command, and nothing is written.

### Benchmarking

Compare local model backends side-by-side. The benchmarks now live in the `llmbench` package (`../llmbench/`); `modelman benchmark ...` still runs them, and `uv run --directory llmbench llmbench ...` is the same commands without the `benchmark` word:

```bash
modelman benchmark run --family <family>          # no default targets: name --model or --family, or it exits 2
modelman benchmark run --family <family> --workload short
modelman benchmark run --model ollama/ornith-1.5:35b --direct
modelman benchmark list-workloads
modelman benchmark show-results --latest
```

Results are written to `~/.config/local-ai/benchmarks/<run_id>/` as JSON and
Markdown.

## Development

The project uses a Makefile to wrap the common dev tasks:

```bash
make help        # list all targets
make install     # uv sync (install dev deps)
make test        # run the test suite
make lint        # ruff check
make format      # ruff format + ruff check --fix
make typecheck   # mypy
make check       # lint + format check + typecheck (no auto-fixes)
make all         # format + test + check
make clean       # remove caches
```

`uv run` is also fine for ad-hoc commands (e.g. `uv run pytest tests/foo.py`).

## Architecture

- `src/modelman/app.py` — `ModelmanApp` (Textual `App`), launches directly into `ModelScreen` (its only screen — there's no separate family list).
- `src/modelman/screens/__init__.py` — `reload_preserving_cursor` helper used by `ModelScreen` so `DataTable.clear()` doesn't reset the cursor to row 0.
- `src/modelman/screens/` — `models.py` (single table of every model across every family, sorted family/location/provider/name, cursor-preserving reload, alphabetical dropdowns, delete-any-model), `forms.py` (modals on a shared `ModelmanModal` base with consistent button order, Escape-to-cancel, and safe-default focus on destructive dialogs — `ModelForm`'s add-mode family Select includes a "+ New family…" option).
- `src/modelman/registry.py` — loads/saves `registry.toml` (`Registry`, `ProviderEntry`, `ModelEntry`).
- `src/modelman/state.py` — loads/saves `modelman.toml` (`StateStore`, `ModelState`, `FamilyState`).
- `src/modelman/queue.py` — `PendingChanges` orchestrates queued edits: deletes run before moves, then downloads, failures are collected, then a single save. It touches no LiteLLM route: the caller's `wt litellm sync` afterwards is what drops a removed model's route. Deletes check `provider.is_downloaded()` first: when the artifact is already gone (e.g. queued from the TUI on a not-ready row, or removed by hand), the provider's `delete()` is skipped but registry/state cleanup and lifecycle events still run. A raising `is_downloaded()` is treated conservatively — the artifact delete is attempted and real failures surface normally.
- `src/modelman/litellm.py` — modelman's only route-write path is `sync_routes()`, which asks wt to reconcile and returns its warnings (skipping wt when no LiteLLM config file exists). Everything else here reads: read-only `config.yaml` helpers for `modelman usage`. All `model_list` construction, the config write, the owned-settings pass and the proxy restart live in `wt/internal/litellm`.
- `src/modelman/wt_bridge.py` — the subprocess wrapper around `wt litellm ...` (JSON parsing, error mapping).
- `src/modelman/sync.py` — reconciles configured models against provider state.
- `src/modelman/migrate.py` — one-time import of legacy config into the registry/state.
- `src/modelman/settings.py` — user preferences (`settings.yaml`).
- `src/modelman/providers/` — one module per backend (`ollama.py`, `omlx.py`, `mtplx.py`, `mlx_lm_server.py`, retired `llamacpp.py`). Each registers itself with `ProviderRegistry` at import time and implements `is_downloaded`, `download`, `list_local`, and optionally `size_of`/`path_of`/`resolve_local`.
- `../llmbench/src/llmbench/providers/lifecycle/` — start/stop/isolate/restore of local provider servers (`llmbench provider ...`, still mounted as `modelman provider ...`); it moved to the `llmbench` package, which modelman depends on. `src/modelman/local_control.py` — `modelman start`/`stop` and the TUI `s` key.
- `src/modelman/pricing.py` — OpenRouter price refresh (`modelman refresh-prices`; the daily check ran when the TUI started, so it no longer runs — wt's stale-pricing notice says when to refresh).
- `src/modelman/manifest.py` / `config.py` — legacy `families/*.yaml` / `config.yaml` loaders, read only by `migrate`.

To add a new provider (e.g. vLLM) — the full checklist is the
`adding-a-provider` skill in `.claude/skills/adding-a-provider/SKILL.md`:

1. Create `src/modelman/providers/vllm.py` with a class extending `Provider`.
2. Call `ProviderRegistry.register(VLLMProvider)` at module bottom.
3. Add the provider to `registry.toml` under `[[providers]]`.
4. Use `provider_id: vllm` on models in `registry.toml`.
5. (Optional) Override `size_of`/`path_of`; sizes populate the SIZE column, and on-disk paths show in the details panel under the table.
