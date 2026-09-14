# modelman

A terminal UI for managing LLM models across providers (Ollama,
oMLX, OpenRouter, and native agent providers like `claude`, `codex`; the
llama.cpp provider is retired — see `../docs/reference/provider-artifacts.md`). Models and providers live in a shared `registry.toml`;
per-machine state (ready markers, LiteLLM exposure) lives in
`modelman.toml`. The TUI lets you browse families, drill into a family's
model list, and queue changes (add/edit/delete/ready/expose) that are
applied on exit.

## Install

modelman is a `uv` project nested inside the monorepo. Run all commands from
the `modelman/` directory:

```bash
cd modelman
uv sync
uv run modelman
```

Because the repo root has no `pyproject.toml`, running `uv run modelman` from the
root will fail. Always `cd modelman` first.

## Configuration

modelman reads three files under `~/.config/local-ai/` (each overridable
with an env var), plus one LiteLLM-specific override:

| File / setting | Purpose | Env override |
|----------------|---------|------------|
| `registry.toml` | Canonical model/provider definitions (shared, read-only by other tools) | `MODELMAN_REGISTRY` |
| `modelman.toml` | Per-machine mutable state: download markers, LiteLLM exposure flags (also read by `wt`, read-only, for the exposure flags) | `MODELMAN_STATE` |
| `settings.yaml` | User preferences (theme) | `MODELMAN_SETTINGS` |
| LiteLLM `config.yaml` | Path to the LiteLLM config file modelman writes | `MODELMAN_LITELLM_CONFIG` |
| LiteLLM proxy restart | Shell command to restart the running proxy after expose changes | `MODELMAN_LITELLM_RESTART_CMD` |

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
the model is exposed. For Ollama models it is auto-populated on add by
running `ollama show <name>` and translating known capabilities (e.g.
`tools` → `supports_function_calling: true`).

`cost` is validated on load: each price field must be a non-negative
finite number; `subscription_period` must be `month` or `year` when
`subscription_price` is set. Unknown keys inside `[models.cost]` are
preserved on round-trip so hand-edited fields survive. Omit the whole
`[models.cost]` table to leave cost unset (the TUI shows `—`).

The old `kind = "free" | "per_token" | "subscription"` schema is still
accepted on read and is silently migrated to the flat fields above on
save. `usage_tier` has been removed; use the pricing fields directly.

### `modelman.toml`

```toml
[model_state."ollama/ornith:35b"]
ready = true
disk_path = "ollama:ornith:35b"
size_bytes = 123456789
litellm_exposed = false
```

`ready` is the provider-agnostic readiness flag. For reconcilable providers
(Ollama, oMLX; llama.cpp was retired 2026-09-07) it means "the model is present on this machine".
For flag-only providers (OpenRouter, native agents like `claude`) it means
"the user has marked this model as available"; there is nothing to download
or delete on disk.

Family display names now live in `registry.toml`'s `[[families]]` section.
The legacy `[families.*]` table here is still loaded as a read-side
fallback, but the TUI no longer writes it.

This file is optional — a fresh install starts with an empty store.

## Usage

### CLI

```bash
modelman                        # open the TUI (model list)
modelman sync                   # reconcile configured models against providers
modelman expose <model-id>      # expose a model through LiteLLM
modelman unexpose <model-id>    # remove a model's LiteLLM exposure
modelman migrate                # one-time import of legacy config (see below)
```

### TUI

The TUI has a single screen:

- **Model screen** — the app's only/root screen: a single table of every
  model across every family, sorted family · location (local before
  cloud) · provider · model name (columns: family · provider · model ·
  loc · status ✓/○/↓/↑/✗/→ · exposed · cost · size). LOC is an icon
  (↗ cloud / ▤ local / `—` when unknown) and EXPOSED shows `Y`/`–`; COST
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
  exposed from this dialog), `d` queue delete (works on any model —
  apply skips the on-disk removal if the artifact is already gone, but
  still cleans registry/state), `r`
  toggle ready (queues download/pull for reconcilable providers, or a
  flag flip for cloud/native providers; a no-op with a notification if
  the model is a local artifact that's already on disk — reconcile is
  the only writer of ready=False for those, so delete the file instead),
  `x` toggle exposed (cascades a ready=True queue first if the model
  isn't ready yet), `l` toggle LiteLLM routing on/off, `enter` edit,
  `escape` shows the apply/discard/cancel dialog if anything is queued,
  otherwise quits the app. Reconcile runs automatically on mount — there
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

All model changes (adds, edits, deletes, ready toggles, exposure toggles,
moves) are queued in memory — nothing downloads or writes to disk while
the TUI is open (add/edit are the one exception: registry.toml is
persisted immediately, so a discarded session doesn't lose a
concurrently-typed edit). `Escape`/`Ctrl+Q` with a pending queue shows a
confirmation dialog listing the pending set; `Apply` or `Discard` both
exit the app. On Apply, `main.py` runs the queue in the plain terminal
after the TUI closes: **deletes, then moves, then ready changes
(downloads/clears/flag flips), then exposure changes**, printing
provider progress and a thin lifecycle line per operation to stdout,
then writes `registry.toml` + `modelman.toml` once. A failed operation
is reported in an error summary at the end and the process exits
non-zero; `Ctrl+C` mid-run cancels the remaining queue (already-applied
steps are not undone, nothing is saved for the interrupted run). A
delete for a not-on-disk model is legal: the on-disk removal is
skipped, but the registry/state cleanup, lifecycle events, and any
cascade-unexpose still run.

All dialogs share a layout convention: the cancel/default button is
rightmost, the primary action is to its left, and pressing `Escape`
cancels (this works even when an Input is focused). Destructive prompts
(`ConfirmModal`, `ConfirmExitDialog`) focus the safe button on open so a
reflexive `Enter` is never destructive.

### Expose models through LiteLLM

A downloaded model (or any cloud model) can be exposed to LiteLLM, which
writes a `model_list` entry into LiteLLM's `config.yaml` and flips the
model's `litellm_exposed` flag in `modelman.toml`.

```bash
modelman expose <model-id>    # add the model_list entry
modelman unexpose <model-id>  # remove it
```

In the TUI, press `x` on a model row to queue an exposure toggle; it
applies after you exit via Apply, alongside every other queued change (a
not-ready model is downloaded/pulled first, in the terminal, once the
TUI has closed). The EXPOSED column shows `Y` when exposed (or queued
to expose) and `–` otherwise.

LiteLLM's `config.yaml` lives at `~/.config/litellm/config.yaml` by default
(override with `MODELMAN_LITELLM_CONFIG`). Writes are read-modify-write with
ruamel round-trip, so sections modelman doesn't own (`general_settings`,
`router_settings`, unknown keys, hand-written comments) survive untouched
(comments attached to a specific `model_list` row are best-effort — one
positioned next to a row modelman removes or replaces can be dropped).
modelman additionally owns two launcher-required settings and ensures them on
every write: `litellm_settings.drop_params: true` (without it, copilot's
`parallel_tool_calls` gets rejected with `400 UnsupportedParamsError`) and
`additional_drop_params: ["reasoning_effort"]` on every `model_list` entry
routed through the `ollama_chat/` bridge (a workaround for a LiteLLM 1.98.x
responses-bridge crash hit by codex, BerriAI/litellm#37452; an entry already
carrying the key, whatever its value, is left as-is). A write that changes
nothing saves nothing and does not restart the proxy.

By default the running LiteLLM proxy is **not** automatically restarted
after an expose/unexpose change. Set `MODELMAN_LITELLM_RESTART_CMD` to a
shell command that restarts your proxy (e.g. `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`) to have modelman run it automatically after each config write that changes the model list. The restart runs only when the change actually took effect (a no-op unexpose of an already-removed model does not bounce the proxy), and is bounded by a 30-second timeout. When the command is unset, modelman surfaces a warning that a manual restart is needed — in the TUI it appears in the apply log, in the CLI it is printed to stderr. A failed restart does not fail the expose operation.

### Native providers

Providers whose `auth.type` is `"native"` (or whose id matches an agent in
`~/.config/agent-wt/config.toml`) represent models handled by external
agents (e.g. `claude`, `codex`). They have no download mechanics:
pressing `r` simply toggles the `ready` flag, and there is no disk path or
size. These providers are synced into `registry.toml` automatically on TUI
launch from the wt config.

### Sync

`modelman sync` reconciles the ready state of models already in
`registry.toml` against their providers — it never adds new models. Ollama
is reconciled via `ollama show`; llama.cpp via the Hugging Face cache;
oMLX via its `model_dir`. Cloud providers (OpenRouter, native agents) are
configured explicitly and are not reconciled. Sync intentionally does not
propagate `cost` to providers; it is registry metadata only.

### One-time migration

`modelman migrate` imports the legacy `~/.config/local-ai/config.yaml` and
`~/.config/local-ai/families/*.yaml` (and, optionally, wt's
`config.toml` via `--wt-config`) into `registry.toml` + `modelman.toml`.
The legacy files are read-only inputs and are not written by the TUI.

### Benchmarking

Compare local model backends side-by-side:

```bash
modelman benchmark run
modelman benchmark run --workload short
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
make check       # lint + typecheck (no auto-fixes)
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
- `src/modelman/queue.py` — `PendingChanges` orchestrates queued edits: deletes run before moves, then downloads, then exposure changes, failures are collected, then a single save. Deletes check `provider.is_downloaded()` first: when the artifact is already gone (e.g. queued from the TUI on a not-ready row, or removed by hand), the provider's `delete()` is skipped but registry/state cleanup, lifecycle events, and the cascade-unexpose still run. A raising `is_downloaded()` is treated conservatively — the artifact delete is attempted and real failures surface normally.
- `src/modelman/litellm.py` — owns all LiteLLM knowledge: provider→`model` prefix mapping, `model_list` entry construction, atomic `config.yaml` read/write (ruamel round-trip so comments and foreign sections survive byte-identically; PyYAML remains for the legacy modules), the `ensure_litellm_settings()` owned-settings pass, and the `expose_model`/`unexpose_model`/`apply_expose_queue` orchestration used by both the CLI and TUI.
- `src/modelman/sync.py` — reconciles configured models against provider state.
- `src/modelman/migrate.py` — one-time import of legacy config into the registry/state.
- `src/modelman/settings.py` — user preferences (`settings.yaml`).
- `src/modelman/providers/` — one module per backend (`ollama.py`, `llamacpp.py`, `omlx.py`). Each registers itself with `ProviderRegistry` at import time and implements `is_downloaded`, `download`, `list_local`, and optionally `size_of`/`path_of`.
- `src/modelman/manifest.py` / `config.py` — legacy `families/*.yaml` / `config.yaml` loaders, read only by `migrate`.

To add a new provider (e.g. vLLM):

1. Create `src/modelman/providers/vllm.py` with a class extending `Provider`.
2. Call `ProviderRegistry.register(VLLMProvider)` at module bottom.
3. Add the provider to `registry.toml` under `[[providers]]`.
4. Use `provider_id: vllm` on models in `registry.toml`.
5. (Optional) Override `size_of`/`path_of`; sizes populate the SIZE column, and on-disk paths show in the details panel under the table.
