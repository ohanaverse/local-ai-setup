# modelman

A terminal UI for managing local LLM models across providers (Ollama,
llama.cpp, oMLX, OpenRouter). Models and providers live in a shared
`registry.toml`; per-machine state (downloads, LiteLLM exposure) lives in
`modelman.toml`. The TUI lets you browse families, drill into per-provider
model lists, and queue changes (add/edit/delete/download/expose) that are
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
| `modelman.toml` | Per-machine mutable state: download markers, family display names, LiteLLM exposure flags | `MODELMAN_STATE` |
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

[[models]]
id = "ollama/ornith:35b"     # globally unique, stable key
family = "ornith"
provider_id = "ollama"
model_name = "ornith:35b"    # provider-specific (ollama tag, HF repo, …)
model_info = { supports_function_calling = true }   # optional LiteLLM-style keys

[models.fetch]               # optional, for HF-backed providers
repo = "org/repo"
files = ["model.gguf"]
quantizations = ["Q4_K_M"]
```

`model_info` is freeform and copied into LiteLLM's `model_list` entry when
the model is exposed. For Ollama models it is auto-populated on add by
running `ollama show <name>` and translating known capabilities (e.g.
`tools` → `supports_function_calling: true`).

### `modelman.toml`

```toml
[model_state."ollama/ornith:35b"]
downloaded = true
disk_path = "ollama:ornith:35b"
size_bytes = 123456789
litellm_exposed = false

[families.ornith]
display_name = "Ornith"
```

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
  reconcile, `q` quit.
- **Model screen** — two-pane view: providers on the left, that provider's
  models on the right (columns: name · status ✓/○/↓/✗ · size · path ·
  exposed). Keys: `a` add model, `e` edit (id/provider fixed), `d` queue
  delete (downloaded variants only), `x` toggle download (not-downloaded
  variants only), `l` toggle LiteLLM exposure (downloaded or cloud variants
  only), `r` reconcile, `enter` edit, `escape` back / apply queue.
- **Status screen** — when you apply on exit, the model screen hands off to
  a status screen that streams per-item progress (`Deleting …`,
  `Downloaded …`, `Saving …`) into a scrollable log. Provider progress is
  forwarded live: Ollama's pull output (stripped of ANSI escapes) and
  huggingface_hub tqdm bars (per-file bytes/rate) appear as each line is
  emitted. `Escape` mid-run pops a Cancel-or-Wait dialog: `Cancel` kills any
  running subprocess (Ollama) and stops the queue; `Wait` keeps waiting.
  Once the run completes (or is cancelled), `Escape` returns to the family
  screen.

All model changes (adds, edits, deletes, downloads, exposure toggles) are
queued in memory. On exit, a confirmation dialog shows the pending set;
confirming runs **deletes first, then downloads, then exposure changes**,
and writes `registry.toml` + `modelman.toml` once.

### Expose models through LiteLLM

A downloaded model (or any cloud model) can be exposed to LiteLLM, which
writes a `model_list` entry into LiteLLM's `config.yaml` and flips the
model's `litellm_exposed` flag in `modelman.toml`.

```bash
modelman expose <model-id>    # add the model_list entry
modelman unexpose <model-id>  # remove it
```

In the TUI, press `l` on a model row to queue an exposure toggle; it applies
on exit alongside downloads/deletes. The EXPOSED column shows `L` when
exposed (or queued to expose) and `–` otherwise.

LiteLLM's `config.yaml` lives at `~/.config/litellm/config.yaml` by default
(override with `MODELMAN_LITELLM_CONFIG`). modelman only touches the
`model_list` section; `general_settings` and unrecognized rows are preserved.

### Sync

`modelman sync` reconciles the downloaded state of models already in
`registry.toml` against their providers — it never adds new models. Ollama
is reconciled via `ollama list`; llama.cpp via the Hugging Face cache;
oMLX via its `model_dir`. Cloud providers (OpenRouter) are configured
explicitly and are not reconciled.

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
- `src/modelman/screens/` — `families.py` (family list), `models.py` (two-pane model view), `forms.py` (modals), `status.py` (apply progress).
- `src/modelman/registry.py` — loads/saves `registry.toml` (`Registry`, `ProviderEntry`, `ModelEntry`).
- `src/modelman/state.py` — loads/saves `modelman.toml` (`StateStore`, `ModelState`, `FamilyState`).
- `src/modelman/queue.py` — `PendingChanges` orchestrates queued edits: deletes run before moves, then downloads, then exposure changes, failures are collected, then a single save.
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
5. (Optional) Override `size_of`/`path_of` to populate the size/path columns.
