# wt as a Read-Only Registry Consumer — Design

**Status:** Approved in chat (2026-08-28). Not yet planned or implemented.

**Scope:** Phase 4 of the model-management consolidation effort, `agent-worktree`
side only. Builds on sub-project 1 (shared model registry): `modelman` owns
`registry.toml`; wt now reads it read-only.

## Goal

Make `wt` a read-only consumer of the shared model registry. wt stops owning
Providers/Models and instead reads them from `~/.config/local-ai/registry.toml`,
joining them in memory with its own `config.toml` (Agents + DefaultTag). wt
deletes its live-discovery package and its `wt config ollama` subcommand.

## Background

- `modelman` owns `registry.toml` (Providers/Models) and `modelman.toml`
  (per-machine state). wt is a consumer.
- wt's `config.toml` currently holds Providers, Models, Agents, DefaultTag.
  After this change it holds only Agents + DefaultTag.
- wt's `internal/registry` package does live discovery (Ollama `ollama list`,
  OpenRouter HTTP) + merge, used only by `wt config ollama` (via `ollamaconfig`)
  and `ollamacheck` (pre-launch availability). It is deleted.
- The launch path uses hardcoded `OllamaBaseURL`; it does **not** read
  per-provider auth/base_url from config, so wt only needs provider IDs +
  minimal model fields from `registry.toml`.

## Architecture (Approach B)

Two files, one in-memory `config.Config`:

- `~/.config/local-ai/registry.toml` — Providers + Models (**modelman-owned,
  read-only** for wt).
- `~/.config/agent-wt/config.toml` — Agents + DefaultTag (**wt-owned**).

`config.Config` keeps its current shape (Providers, Models, Agents,
DefaultTag), but:

- **Load** reads `config.toml` (Agents + DefaultTag) *and* `registry.toml`
  (Providers/Models) and joins them in memory.
- **Save** writes only `DefaultTag` + `Agents`; Providers/Models are never
  persisted (they are modelman's).

This keeps all model-selection call sites (`EligibleModels`, `ModelsForAgent`,
etc.) operating on `cfg.Models`/`cfg.Providers` unchanged.

## Loading & Fail-Closed Behavior

- `config.Load` reads `config.toml`, then reads `registry.toml` and parses
  Providers/Models into the same in-memory Config.
- If `registry.toml` is missing or malformed → **fail closed** (error). wt has
  no editor for this file; existing users seed it once via `modelman migrate`.
- `registry.toml` parsing reads only the fields wt needs into
  `config.Provider`/`config.Model`: id, name, location, auth.type for
  providers; id, family, provider_id, model_name, location, source, tags for
  models. Extra registry.toml fields (cost, model_info, fetch, model_dir,
  base_url, secret_ref) are ignored.

The loader is a fresh, small package `internal/catalog` that only
parses the TOML file — it does no discovery. The old `internal/registry`
package is deleted.

## Agent/Provider Validation

- Agents in `config.toml` still reference provider IDs via `supported_providers`
  / `default_provider`, now resolved against the joined in-memory catalog
  (registry.toml providers).
- `Validate`/`UpsertAgent` check agent provider references against the catalog,
  matching current behavior. An agent referencing a provider not in the catalog
  errors at load/launch.
- The error shape is unchanged: an agent with no valid providers / a
  `default_provider` not in the catalog yields the same "agent has no default
  provider configured" style error.
- `configeditor` edits agents only; the agent form's provider picker/validation
  reads from the catalog, so a user can't save an agent referencing a provider
  that doesn't exist in registry.toml.

## Deletions & Editor Reduction

**Delete:**
- `internal/registry` package (live discovery + merge).
- `wt config ollama` command + `internal/ollamaconfig` package.
- The `providers` and `models` sections from the `configeditor` TUI, including
  their add/edit/delete forms (`provider_form.go`, `model_form.go`,
  `providers_tab.go`, `models_tab.go`) and their tests.

**Keep but reduce:**
- `wt config` (no subcommand) → the TUI edits **agents only** plus DefaultTag.
  The `theme`/`path` subcommands are unchanged.
- `ollamacheck` → decoupled from `internal/registry`; runs `ollama list`
  directly (a small inline subprocess call) so the pre-launch "model not pulled
  — run ollama pull" guard still works.

**CLI surface change:** `wt config ollama` is gone — modelman owns
provider/model management, and `modelman sync` covers the ollama reconcile that
`wt config ollama` used to do.

## Testing

- **`internal/config` tests** — `Load` reads both files into one joined Config;
  missing `registry.toml` fails closed; `Save` writes only Agents + DefaultTag
  (assert no `providers`/`models` in output); agent provider-ref validation
  against the joined catalog.
- **`internal/catalog` (loader) tests** — registry.toml parse into
  `config.Provider`/`config.Model`; extra fields ignored; malformed file errors.
- **`configeditor` tests** — editors only expose agents; agent form validates
  against catalog providers.
- **`ollamacheck` tests** — decoupled availability check returns correct
  true/false via a mocked `ollama list`.
- **Launch-path tests** — `resolveModel`/`EligibleModels` behavior unchanged
  against the joined catalog (existing eligible-model tests reused, now backed
  by a registry.toml fixture).

## Explicitly Out of Scope

- Any change to `modelman` or `local-ai-setup`.
- Writing to `registry.toml` — wt is strictly read-only.
- Adding wt-side editor for providers/models — that's modelman's job.
- Benchmark tooling and usage/spend tracking (separate sub-projects).
