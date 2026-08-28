# Modelman Roadmap

Provider sync is **reconcile, not discover**: sync updates the state of
models already configured in `registry.toml`; it never adds new models.

## Phase 2 — provider reconcile (in progress)

| PR | Scope | Status |
|---|---|---|
| #8 | ollama discover (superseded by #9) | merged |
| #9 | ollama reconcile-only revision | merged |
| #10 | modeldir reconcile (llamacpp/omlx) | next |
| — | openrouter | dropped — no-op (models explicitly configured) |

Specs:

- `docs/superpowers/specs/2026-08-28-modelman-sync-ollama-reconcile-design.md` (PR #9)
- `docs/superpowers/specs/2026-08-28-modelman-sync-modeldir-reconcile-design.md` (PR #10)
- `docs/superpowers/specs/2026-08-27-modelman-sync-modeldir-design.md` (superseded)
- `docs/superpowers/specs/2026-08-27-modelman-sync-openrouter-design.md` (dropped)

Plans:

- `docs/superpowers/plans/2026-08-28-modelman-sync-ollama-reconcile.md` (PR #9)
- `docs/superpowers/plans/2026-08-28-modelman-sync-modeldir-reconcile.md` (PR #10)

## Phase 3 — LiteLLM exposure

Toggle `litellm_exposed`; write/remove `model_list` entries in LiteLLM's
`config.yaml` (keyed by registry model id, using the provider's
`base_url`). `general_settings` is never touched.

| PR | Scope | Status |
|---|---|---|
| — | `expose`/`unexpose` CLI + TUI `l` key; write/remove `model_list` entries | ✅ done |

Spec: `docs/superpowers/specs/2026-08-28-modelman-litellm-exposure-design.md`

## Phase 4 — wt consumer (cross-repo)

wt reads `registry.toml` read-only and joins it in memory with its own
`config.toml`; deletes its `internal/registry` package and
`wt config ollama` subcommand.

**Status:** ✅ merged — `agent-worktree` PR #94 (`79d620f`, 2026-08-28).

## Future cleanups

- Remove the vestigial `source` field — with no discovery, every model is
  user-configured, so `source` is always "curated"/None.
