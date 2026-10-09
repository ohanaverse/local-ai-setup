# Modelman LiteLLM Exposure — Design

**Status:** Approved in chat (2026-08-28). Not yet planned or implemented.

**Scope:** Phase 3 of the shared-model-registry effort, modelman side only.
`wt`'s consumer work (Phase 4) is a separate future PR.

## Goal

Let a user toggle a model's `litellm_exposed` flag. Toggling on writes the
corresponding `model_list` entry into LiteLLM's `config.yaml`; toggling off
removes it. The flag lives in `modelman.toml`; the entry lives in
`~/.config/litellm/config.yaml`. `general_settings` is never touched.

## Background

- `modelman.toml` already carries a per-model `litellm_exposed: bool`
  (all `false` today), preserved by sync.
- `registry.toml` holds providers (`AuthConfig.base_url` / `secret_ref`) and
  models (`model_info`).
- LiteLLM's `config.yaml` has a `model_list` keyed by the registry model id
  as `model_name`, with provider-specific `litellm_params` and optional
  `model_info`.

## Architecture

**New module `src/modelman/litellm.py`** — the single owner of all LiteLLM
knowledge.

- `LITELLM_MODEL_PREFIXES` — provider → `model` field prefix mapping:
  - `ollama` → `ollama_chat/`
  - `omlx` → `openai/`
  - `llamacpp` → `openai/local-model` (fixed, ignores model name)
  - `openrouter` → `openrouter/`
- `build_model_list_entry(model, provider) -> dict` — constructs a
  `model_list` row: `model_name` = model id, `litellm_params.model` =
  prefix + name, `api_base` = provider `AuthConfig.base_url`, `api_key` =
  from `secret_ref` (cloud) or `"not-needed"`/`"dummy-key"` (local),
  `model_info` copied from the model.
- `load_litellm_config(path) -> dict` — reads the YAML; errors if missing.
- `set_exposed(config, model_id, entry)` / `remove_exposed(config, model_id)`
  — add/remove the `model_list` row keyed by model id, preserving all other
  rows and `general_settings` verbatim.
- `save_litellm_config(config, path)` — atomic write (temp file + rename),
  preserving comments/formatting where feasible.

**Orchestration** (in `litellm.py`) — used by both CLI and TUI:

- `expose_model(registry, state, model_id)` / `unexpose_model(...)` —
  validate (downloaded-or-cloud), update `modelman.toml` flag +
  `config.yaml`, atomic writes.

**CLI (`main.py`):** `expose <model_id>` / `unexpose <model_id>` flat
subcommands.

**TUI (`screens/models.py`):** `l` key queues an exposure change; new
"exposed" column; applied on exit alongside downloads/deletes.

## Data Flow

**Toggle flow (CLI):**
1. `modelman expose <id>` → load `registry.toml` + `modelman.toml`.
2. Validate: model exists; provider is reconcilable; model is downloaded
   **or** provider is cloud (openrouter). Error otherwise.
3. Build the `model_list` entry from the model + provider.
4. Read `~/.config/litellm/config.yaml` (error if missing).
5. Add/remove the row keyed by model id; preserve everything else verbatim.
6. Atomic-write `config.yaml` and `modelman.toml` (flip `litellm_exposed`).

**Toggle flow (TUI):** `l` on `ModelScreen` queues the change (same
validation at apply time). On exit, the apply step runs the same
`expose_model`/`unexpose_model` orchestration, then the exit-confirmation
dialog shows the pending exposure changes alongside downloads/deletes.

**Entry construction example** (for `ollama/qwen3.8:27b-mlx`, downloaded):
```yaml
- model_name: ollama/qwen3.8:27b-mlx
  litellm_params:
    model: ollama_chat/qwen3.8:27b-mlx
    api_base: http://localhost:11434
  model_info:
    supports_function_calling: true
```

**`unexpose`** removes the row by `model_name` match; if the id isn't
present, it's a no-op (idempotent).

## Configuration

- LiteLLM `config.yaml` path: `~/.config/litellm/config.yaml` by default,
  overridable via `MODELMAN_LITELLM_CONFIG` env var (matching the existing
  `MODELMAN_REGISTRY`/`MODELMAN_STATE` pattern).

## Error Handling

- **Missing `config.yaml`** — `expose`/`unexpose` error out (don't create
  the file). `general_settings`/DB/Redis are outside modelman's authority.
- **Malformed / hand-edited `config.yaml`** — parse and update only
  `model_list` rows keyed by ids modelman recognizes; preserve unrecognized
  rows, `general_settings`, and comments verbatim. Unparseable YAML errors
  rather than clobbering.
- **Expose a non-downloaded local model** — error: "model not downloaded"
  (must be downloaded, or a cloud provider).
- **Expose an unknown model id** — error: "model not found in registry".
- **Write failure** — atomic write (temp + rename); on failure neither file
  is left half-written. `modelman.toml` and `config.yaml` are written
  independently; if the second write fails after the first succeeds, report
  the partial state clearly rather than silently.
- **Idempotency** — `expose` on an already-exposed model and `unexpose` on
  a non-exposed one are no-ops (no duplicate rows, no error).
- **Concurrency** — modelman is the sole writer of `config.yaml`'s
  `model_list`; atomic writes guard against mid-write interruption.

## Testing

- **`tests/test_litellm.py`** — unit tests for `build_model_list_entry`
  (per-provider prefix mapping, api_key handling, model_info copy),
  `set_exposed`/`remove_exposed` (add/remove by id, preserve unrelated rows
  + `general_settings` verbatim), and config.yaml read/write round-trip.
- **`tests/test_expose.py`** — `expose_model`/`unexpose_model` orchestration:
  validation (downloaded-or-cloud, unknown id, missing config.yaml),
  idempotency, atomic-write behavior, partial-write error reporting.
- **`tests/commands/test_expose.py`** — CLI wiring for `expose`/`unexpose`
  (load → validate → write → report), using `MODELMAN_LITELLM_CONFIG` to
  redirect the path.
- **`tests/screens/test_models.py`** — TUI: `l` key queues an exposure
  change, the "exposed" column renders, and the exit-confirmation dialog
  lists pending exposure changes.
- **`tests/test_state.py`** — `litellm_exposed` flag round-trips through
  `modelman.toml` (already partially covered; extend for the toggle path).

## Explicitly Out of Scope

- `wt` consumer work (Phase 4) — reading `registry.toml` read-only, deleting
  `internal/registry` and `wt config ollama`.
- Any change to LiteLLM's `general_settings` (DB/Redis/auth).
- Benchmark tooling and usage/spend tracking (separate sub-projects).
