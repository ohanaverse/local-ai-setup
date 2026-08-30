# modelman

A terminal UI for managing LLM models across providers (Ollama,
llama.cpp, oMLX, OpenRouter, and native agent providers like `claude`,
`codex`). Models and providers live in a shared `registry.toml`;
per-machine state (ready markers, LiteLLM exposure) lives in
`modelman.toml`. The TUI lets you browse families, drill into a family's
model list, and queue changes (add/edit/delete/ready/expose) that are
applied on exit.

## Install

Run from the repo with `uv`:

```bash
uv sync
uv run modelman
```

## Configuration

modelman reads three files under `~/.config/local-ai/` (each overridable
with an env var):

| File | Purpose | Env override |
|------|---------|--------------|
| `registry.toml` | Canonical model/provider definitions (shared, read-only by other tools) | `MODELMAN_REGISTRY` |
| `modelman.toml` | Per-machine mutable state: download markers, LiteLLM exposure flags | `MODELMAN_STATE` |
| `settings.yaml` | User preferences (theme) | `MODELMAN_SETTINGS` |

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
kind = "per_token"           # "free" | "per_token" | "subscription"
price_per_million_tokens = 0.50

# [models.cost]
# kind = "subscription"
# price_per_period = 20.0
# period = "month"

usage_tier = "medium"        # optional; "low" | "medium" | "high" (Ollama cloud tier style)

[models.fetch]               # optional, for HF-backed providers
repo = "org/repo"
files = ["model.gguf"]
quantizations = ["Q4_K_M"]
```

`model_info` is freeform and copied into LiteLLM's `model_list` entry when
the model is exposed. For Ollama models it is auto-populated on add by
running `ollama show <name>` and translating known capabilities (e.g.
`tools` → `supports_function_calling: true`).

`cost` is validated on load: `kind` must be one of the three supported
values; price fields must be finite numbers; `period` must be a string.
Unknown keys inside `[models.cost]` are preserved on round-trip so
hand-edited fields survive. Omit the whole `[models.cost]` table to leave
cost unset (the TUI shows `—`).

`usage_tier` is optional and freeform at the registry level, but the TUI's
Ollama add/edit dialog only offers `low`/`medium`/`high` to match
Ollama.com's cloud subscription tiers.

### `modelman.toml`

```toml
[model_state."ollama/ornith:35b"]
ready = true
disk_path = "ollama:ornith:35b"
size_bytes = 123456789
litellm_exposed = false
```

`ready` is the provider-agnostic readiness flag. For reconcilable providers
(Ollama, llama.cpp, oMLX) it means "the model is present on this machine".
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
modelman                        # open the TUI (family list)
modelman download <family>      # open the TUI at a family's model screen
modelman sync                   # reconcile configured models against providers
modelman expose <model-id>      # expose a model through LiteLLM
modelman unexpose <model-id>    # remove a model's LiteLLM exposure
modelman migrate                # one-time import of legacy config (see below)
```

### TUI

The TUI has three screens:

- **Family screen** — table of families with columns: family · display ·
  variants · downloaded · size. Keys: `a` add, `e` edit display name,
  `d` delete (blocked if anything is downloaded), `enter` open, `r`
  reconcile, `q` quit. While the background size refresh runs after
  mount/resume/`r`, the table is briefly disabled and a
  "Refreshing sizes…" indicator is shown — actions (`a`, `e`, `d`,
  `enter`, `r`) are no-ops during that window so the user can't click
  a row whose contents are about to mutate. The cursor survives the
  refresh: returning from a model screen leaves you on the same family.
- **Model screen** — single table scoped to one family (columns:
  provider · model · loc · status ✓/○/↓/↑/✗/→ · exposed · cost ·
  tier · size). LOC is an icon (↗ cloud / ▤ local / `—` when unknown) and
  EXPOSED shows `Y`/`–`; COST shows `free`, a per-token price, a
  subscription price, or `—` when unset; TIER shows the usage tier or `—`.
  The row's on-disk path appears in a details panel below the table
  (`path: —` when unknown). Rows are sorted by provider then model name.
  Keys: `a` add model, `e` edit (id/provider fixed; changing family queues
  a move), `d` queue delete (works on any model — apply skips the on-disk
  removal if the artifact is already gone, but still cleans
  registry/state), `x` toggle ready (queues download/pull for
  reconcilable providers, or a flag flip for cloud/native providers),
  `l` toggle LiteLLM exposure (ready or cloud models), `r` reconcile,
  `enter` edit, `escape` back / apply queue. The cursor survives every
  reload — reconciling or toggling ready on a row leaves you on that
  row. Provider and family dropdowns list options alphabetically; the
  family Select keeps the caller's order when the current family is
  already in the list.

  Add/Edit dialogs include a cost section: pick `free`,
  `per_token` (price per million tokens), or `subscription` (price +
  period). Choose `—` in the cost-kind dropdown to leave cost unset.
  Subscription periods default to `month`/`year`, but any custom string
  you type is preserved on untouched edits. Ollama models also get a
  usage-tier dropdown (`low`/`medium`/`high`).
- **Status screen** — when you apply on exit, the model screen hands off to
  a status screen that streams per-item progress (`Deleting …`,
  `Downloaded …`, `Saving …`) into a scrollable log. Provider progress is
  forwarded live: Ollama's pull output (stripped of ANSI escapes) and
  huggingface_hub tqdm bars (per-file bytes/rate) appear as each line is
  emitted. `Escape` mid-run pops a Cancel-or-Wait dialog: `Cancel` kills any
  running subprocess (Ollama) and stops the queue; `Wait` keeps waiting.
  Once the run completes (or is cancelled), `Escape` returns to the family
  screen.

All dialogs share a layout convention: the cancel/default button is
rightmost, the primary action is to its left, and pressing `Escape`
cancels (this works even when an Input is focused). Destructive prompts
(`ConfirmModal`, `ConfirmExitDialog`, `CancelApplyDialog`) focus the
safe button on open so a reflexive `Enter` is never destructive.

All model changes (adds, edits, deletes, ready toggles, exposure toggles,
moves) are queued in memory. On exit, a confirmation dialog shows the
pending set; confirming runs **deletes, then moves, then ready changes
(downloads/clears/flag flips), then exposure changes**, and writes
`registry.toml` + `modelman.toml` once. A delete for a not-on-disk model
is legal: the on-disk removal is skipped, but the registry/state
cleanup, lifecycle events, and any cascade-unexpose still run.

### Expose models through LiteLLM

A downloaded model (or any cloud model) can be exposed to LiteLLM, which
writes a `model_list` entry into LiteLLM's `config.yaml` and flips the
model's `litellm_exposed` flag in `modelman.toml`.

```bash
modelman expose <model-id>    # add the model_list entry
modelman unexpose <model-id>  # remove it
```

In the TUI, press `l` on a model row to queue an exposure toggle; it applies
on exit alongside downloads/deletes. The EXPOSED column shows `Y` when
exposed (or queued to expose) and `–` otherwise.

LiteLLM's `config.yaml` lives at `~/.config/litellm/config.yaml` by default
(override with `MODELMAN_LITELLM_CONFIG`). modelman only touches the
`model_list` section; `general_settings` and unrecognized rows are preserved.

### Native providers

Providers whose `auth.type` is `"native"` (or whose id matches an agent in
`~/.config/agent-wt/config.toml`) represent models handled by external
agents (e.g. `claude`, `codex`). They have no download mechanics:
pressing `x` simply toggles the `ready` flag, and there is no disk path or
size. These providers are synced into `registry.toml` automatically on TUI
launch from the agent-worktree config.

### Sync

`modelman sync` reconciles the ready state of models already in
`registry.toml` against their providers — it never adds new models. Ollama
is reconciled via `ollama show`; llama.cpp via the Hugging Face cache;
oMLX via its `model_dir`. Cloud providers (OpenRouter, native agents) are
configured explicitly and are not reconciled. Sync intentionally does not
propagate `cost` or `usage_tier` to providers; those fields are registry
metadata only.

### One-time migration

`modelman migrate` imports the legacy `~/.config/local-ai/config.yaml` and
`~/.config/local-ai/families/*.yaml` (and, optionally, agent-worktree's
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

- `src/modelman/app.py` — `ModelmanApp` (Textual `App`), launches into `FamilyScreen`.
- `src/modelman/screens/__init__.py` — `reload_preserving_cursor` helper used by both list screens so `DataTable.clear()` doesn't reset the cursor to row 0.
- `src/modelman/screens/` — `families.py` (family list, locks interactions while reconciling), `models.py` (single-table model view, cursor-preserving reload, alphabetical dropdowns, delete-any-model), `forms.py` (modals on a shared `ModelmanModal` base with consistent button order, Escape-to-cancel, and safe-default focus on destructive dialogs), `status.py` (apply progress).
- `src/modelman/registry.py` — loads/saves `registry.toml` (`Registry`, `ProviderEntry`, `ModelEntry`).
- `src/modelman/state.py` — loads/saves `modelman.toml` (`StateStore`, `ModelState`, `FamilyState`).
- `src/modelman/queue.py` — `PendingChanges` orchestrates queued edits: deletes run before moves, then downloads, then exposure changes, failures are collected, then a single save. Deletes check `provider.is_downloaded()` first: when the artifact is already gone (e.g. queued from the TUI on a not-ready row, or removed by hand), the provider's `delete()` is skipped but registry/state cleanup, lifecycle events, and the cascade-unexpose still run. A raising `is_downloaded()` is treated conservatively — the artifact delete is attempted and real failures surface normally.
- `src/modelman/litellm.py` — owns all LiteLLM knowledge: provider→`model` prefix mapping, `model_list` entry construction, atomic `config.yaml` read/write, and the `expose_model`/`unexpose_model` orchestration used by both the CLI and TUI.
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
