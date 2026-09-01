# Spec: `modelman sync` — modeldir reconcile (llamacpp/omlx)

**Date:** 2026-08-28
**Status:** in progress
**Branch:** feat/reconcile-modeldir

## Problem

PR #9 (`2026-08-28-modelman-sync-ollama-reconcile-design.md`) made `modelman
sync` reconcile-only for ollama: it updates the downloaded state of
configured ollama models against `ollama list`, never adding new models.
This spec extends the same reconcile-only principle to the two model-dir
providers — llamacpp (HF cache) and omlx (`model_dir`) — which currently
have no sync coverage at all.

The principle, per user direction: **all providers only list models that
have been added to modelman; never list all available models for any
provider.** For llamacpp/omlx, "downloaded" is determined per-model by
checking the HF cache / `model_dir` for the model's `fetch.repo`/
`fetch.files`.

## Goal

Extend `modelman sync` to reconcile the downloaded state of configured
llamacpp and omlx models (in addition to ollama), updating
`downloaded`/`disk_path`/`size_bytes` in `modelman.toml`. It never adds
new models.

## Non-goals

- openrouter — no-op (dropped).
- Deleting stale rows — sync remains additive (never deletes).
- Removing the vestigial `source` field — separate cleanup.
- Switching to the `huggingface_hub` API for listing — the providers
  already expose `is_downloaded`/`size_of`; sync reuses them.

## Design

### Reconcile, not discover

For each configured llamacpp/omlx model, sync checks whether the model's
files are present (HF cache for llamacpp, `model_dir` for omlx) and
updates `downloaded`/`disk_path`/`size_bytes` accordingly. Models not in
`registry.toml` are never added. `registry.toml` is read-only input.

### `path_of` on providers

`LlamaCppProvider` and `OMLXProvider` gain a `path_of(variant) ->
str | None` method, mirroring `size_of`, returning the on-disk path when
downloaded (or `None`):

- llamacpp: the primary file path in the first HF-cache snapshot that has
  it.
- omlx: the `model_dir/<basename-of-repo>` directory.

This keeps path logic in the provider (DRY) and matches what `download`
returns (llamacpp → primary file, omlx → directory).

### `list_modeldir`

`list_modeldir(registry, providers) -> dict[str, tuple[str, int]]` returns
`{model_id: (disk_path, size_bytes)}` for downloaded llamacpp/omlx models.
`providers` maps `provider_id -> Provider` instance (built from the
registry's provider entries via `provider_config`). For each configured
llamacpp/omlx model it builds a `VariantSpec` (from `fetch.repo`/
`fetch.files`), checks `is_downloaded`, and if downloaded records
`path_of`/`size_of`.

### `reconcile` refactor (provider-agnostic)

`reconcile` is generalized from ollama-only to provider-agnostic, keyed by
model id:

```python
RECONCILABLE_PROVIDERS = ("ollama", "llamacpp", "omlx")

def reconcile(registry, state, downloaded: dict[str, tuple[str, int]]) -> SyncResult:
    ...
```

`downloaded` maps `model_id -> (disk_path, size_bytes)`. For each
configured model in `RECONCILABLE_PROVIDERS`:

- `model_id` in `downloaded` → `downloaded=True`, `disk_path`,
  `size_bytes`.
- otherwise → `downloaded=False`, `disk_path=None`, `size_bytes=None`.

`litellm_exposed` is preserved (owned by the LiteLLM feature, not sync).
Non-reconcilable providers (e.g. openrouter) are untouched.

### `_ollama_downloaded` + `_model_entry_to_variant`

- `_model_entry_to_variant(entry) -> VariantSpec` builds the
  provider-facing dict from a `ModelEntry` (id, provider, name, repo,
  files, quantizations, model_info). Mirrors
  `screens/models.py::_model_entry_to_variant`; duplicated here so sync
  doesn't depend on the UI layer.
- `_ollama_downloaded(registry, sizes) -> dict[str, tuple[str, int]]` maps
  `ollama list`'s `{name: size}` to `{model_id: (disk_path, size)}`
  (disk_path = `ollama:<name>`).

### `_modeldir_providers`

`_modeldir_providers(registry) -> dict[str, Provider]` builds llamacpp/omlx
provider instances from the registry's provider entries (via
`ProviderRegistry.get` + `provider_config`), keyed by provider id.

### `sync`

```python
def sync(registry, state, runner=None) -> SyncResult:
    downloaded = _ollama_downloaded(registry, list_ollama(runner))
    downloaded.update(list_modeldir(registry, _modeldir_providers(registry)))
    return reconcile(registry, state, downloaded)
```

### Command

`modelman sync` prints:

```
Synced: 3 downloaded, 2 not downloaded.
```

(drops the "ollama" qualifier — now multi-provider).

## File map

- Modify: `src/modelman/providers/llamacpp.py` — add `path_of`.
- Modify: `src/modelman/providers/omlx.py` — add `path_of`.
- Modify: `src/modelman/sync.py` — refactor `reconcile`; add
  `_model_entry_to_variant`, `_ollama_downloaded`, `_modeldir_providers`,
  `list_modeldir`; update `sync`.
- Modify: `src/modelman/main.py` — new summary line.
- Modify: `tests/test_providers/test_llamacpp.py`,
  `tests/test_providers/test_omlx.py` — `path_of` tests.
- Modify: `tests/test_sync.py` — refactor reconcile tests; add
  `list_modeldir`/`sync` tests.
- Modify: `tests/commands/test_sync.py` — new summary.

Files NOT touched: `registry.py`, `state.py`, `screens/models.py`.

## Error handling

- `ollama list` fails → `SyncError` (unchanged).
- Missing provider entry for a modeldir model → `KeyError` from
  `registry.provider` (config error, surfaced).
- Save failures → propagate (existing atomic-write behavior).

## Testing strategy

TDD. Tests first.

1. `path_of` unit tests (llamacpp: primary file path; omlx: directory;
   `None` when absent).
2. `reconcile` unit tests (refactored: keyed by model_id,
   provider-agnostic).
3. `_ollama_downloaded` + `_model_entry_to_variant` unit tests.
4. `list_modeldir` unit tests (stub providers).
5. `sync` integration tests (ollama + modeldir).
6. CLI test (new summary).

## Risks

- **`is_downloaded` vs `path_of`/`size_of` inconsistency** — llamacpp's
  `is_downloaded` checks all files; `path_of`/`size_of` check the primary
  file. Pre-existing; accepted.
- **Stale rows** — sync never deletes, so a configured model removed from
  disk stays `downloaded=False` forever. Accepted.
- **`_model_entry_to_variant` duplication** — duplicated from
  `screens/models.py` to keep sync decoupled from the UI. Accepted (small).
