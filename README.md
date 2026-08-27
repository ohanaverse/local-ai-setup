# modelman

A terminal UI for managing local LLM model families across providers (Ollama, llama.cpp, oMLX). A *family* is a YAML manifest that groups related model variants; the TUI lets you browse, edit, queue downloads, and queue deletions across families and providers — changes are applied on exit.

## Install

Run from the repo with `uv`:

```bash
uv sync
uv run modelman
```

Or invoke without syncing first:

```bash
uv run modelman
```

## Configure

Create `~/.config/local-ai/config.yaml`:

```yaml
providers:
  ollama:
    type: ollama
  llamacpp:
    type: llamacpp
  omlx:
    type: omlx
    model_dir: ~/.omlx/models
```

## Usage

### Open the TUI

```bash
modelman                 # family list
modelman download <fam>  # jumps straight to that family's models
```

The TUI has two screens:

- **Family screen** — table of families with columns: family · display · variants · downloaded · size. Keys: `a` add, `d` delete (blocked if anything is downloaded), `enter` open, `q` quit.
- **Model screen** — two-pane view: providers on the left, that provider's models on the right (columns: name · status ✓/○/↓/✗ · size · path). Keys: `a` add model, `e` edit (id/provider fixed), `d` queue delete (downloaded variants only), `x` toggle download (not-downloaded variants only), `escape` back / apply queue.
- **Status screen** — when you apply on exit, the model screen hands off to a status screen that streams per-item progress (`Deleting …`, `Downloaded …`, `Saving manifest …`) into a scrollable log. Provider progress is forwarded live: Ollama's pull output (stripped of ANSI escapes) and huggingface_hub tqdm bars (per-file bytes/rate) appear as each line is emitted. `Escape` mid-run pops a Cancel-or-Wait dialog: `Cancel` kills any running subprocess (Ollama) and stops the queue; `Wait` keeps waiting. Once the run completes (or is cancelled), `Escape` returns to the family screen.

All model changes (adds, edits, deletes, downloads) are queued in memory. On exit, a confirmation dialog shows the pending set; confirming runs **deletes first, then downloads**, and writes the manifest once.

### Add a family

In the TUI press `a` from the family screen. Or create the file directly:

```yaml
# ~/.config/local-ai/families/<name>.yaml
family: <name>
display_name: "<Display Name>"
variants:
  - id: <variant-id>
    provider: ollama
    name: <ollama-tag>
  - id: <another-id>
    provider: llamacpp
    repo: <org>/<hf-repo>
    files: [<file.gguf>]
  - id: <omlx-id>
    provider: omlx
    repo: <org>/<hf-repo>
```

### Variant fields

| Field            | Applies to         | Notes                                                              |
|------------------|--------------------|--------------------------------------------------------------------|
| `id`             | all                | Stable identifier (immutable after create).                        |
| `provider`       | all                | One of `ollama`, `llamacpp`, `omlx` (immutable after create).      |
| `name`           | ollama             | Ollama tag, e.g. `ornith-1.5:35b`.                                 |
| `repo`           | llamacpp, omlx     | Hugging Face `org/repo`.                                           |
| `files`          | llamacpp           | List of GGUF filenames within the repo.                            |
| `quantizations`  | omlx               | List of quantizations to download.                                 |
| `model_info`     | all (optional)     | Freeform LiteLLM-style keys (e.g. `supports_function_calling`).    |

For Ollama variants, `model_info` is auto-populated on add by running `ollama show <name>` and translating known capabilities (e.g. `tools` → `supports_function_calling: true`). For other providers, set `model_info` manually.

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
- `src/modelman/screens/` — `families.py` (family list), `models.py` (two-pane model view), `forms.py` (modals: add family, confirm, model add/edit, exit confirm).
- `src/modelman/queue.py` — `PendingChanges` orchestrates queued model edits: deletes run before downloads, failures are collected, then a single `save_manifest()`.
- `src/modelman/providers/` — one module per backend (`ollama.py`, `llamacpp.py`, `omlx.py`). Each registers itself with `ProviderRegistry` at import time and implements `is_downloaded`, `download`, `list_local`, and `size_of` (default `None`).
- `src/modelman/config.py` — loads `~/.config/local-ai/config.yaml`. `Config` is a dataclass with a `providers: dict[str, dict]` field.
- `src/modelman/manifest.py` — loads/saves `~/.config/local-ai/families/*.yaml`. `FamilyManifest` is a dataclass with `variants: list[VariantSpec]` and `downloaded: dict[str, dict]`. `VariantSpec` is a `total=False` TypedDict; `model_info` is freeform.

To add a new provider (e.g. vLLM):
1. Create `src/modelman/providers/vllm.py` with a class extending `Provider`
2. Call `ProviderRegistry.register(VLLMProvider)` at module bottom
3. Add `vllm: { type: vllm }` to `~/.config/local-ai/config.yaml`
4. Use `provider: vllm` in any family manifest variant
5. (Optional) Override `size_of` to report the on-disk size for the size column