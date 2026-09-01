# Modelman Roadmap

Provider sync is **reconcile, not discover**: sync updates the state of
models already configured in `registry.toml`; it never adds new models.

## Phase 2 — provider reconcile (complete)

| PR | Scope | Status |
|---|---|---|
| #8 | ollama discover (superseded by #9) | merged |
| #9 | ollama reconcile-only revision | merged |
| #10 | modeldir reconcile (llamacpp/omlx) | merged |
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
| — | reconcile running proxy after expose changes (restart via `MODELMAN_LITELLM_RESTART_CMD`) | ✅ done |

Spec: `docs/superpowers/specs/2026-08-28-modelman-litellm-exposure-design.md`

Spec (reconcile): `docs/superpowers/specs/2026-08-31-modelman-litellm-proxy-reconcile-design.md`

## Phase 4 — wt consumer (cross-repo)

wt reads `registry.toml` read-only and joins it in memory with its own
`config.toml`; deletes its `internal/registry` package and
`wt config ollama` subcommand.

**Status:** ✅ merged — `agent-worktree` PR #94 (`79d620f`, 2026-08-28).

## Phase 4b — family & model screen redesign (complete)

Replace `ModelScreen`'s two-pane provider/model layout with a single sorted
table scoped to one family, generalize `ModelState.downloaded` into a
provider-agnostic `ready` flag (and load the legacy `downloaded` key), make
`FamilyScreen` always block delete when any models exist, and add native
provider support synced from `agent-worktree`'s `[[agents]]` list.

| PR | Scope | Status |
|---|---|---|
| #25 | single-table model screen, `ready` rename, native providers | ✅ merged (2026-08-30) |

Spec: `docs/superpowers/specs/2026-08-29-family-model-screen-redesign-design.md`

Plan: `docs/superpowers/plans/2026-08-29-family-model-screen-redesign.md`

## Phase 4c — family/model screen fixes (complete)

Polish pass on the post-redesign TUI: a shared cursor-preserving reload
helper, an interaction lock on `FamilyScreen` during its background
size refresh, alphabetically sorted provider/family dropdowns, delete
allowed on any model (with `PendingChanges` skipping the artifact call
when `is_downloaded()` is false), and a shared `ModelmanModal` base
class that standardizes every dialog's button order, initial focus,
and Escape-to-cancel behavior.

| PR | Scope | Status |
|---|---|---|
| — | cursor helper, family refresh lock, sorted dropdowns, delete-any-model, dialog conventions, skip-absent-artifact delete | ✅ merged (this branch) |

Spec: `docs/superpowers/specs/2026-08-30-family-model-screen-fixes-design.md`

Plan: `docs/superpowers/plans/2026-08-30-family-model-screen-fixes.md`

## Phase 5 — usage/spend tracking (consolidation sub-project 3)

`modelman usage report` joins `wt`'s local launch history (`usage.jsonl`
+ `rotation.state`) with LiteLLM's Postgres spend logs and prints a
Markdown report: per-model summary, matched / WT-only launches /
LiteLLM-only spend reconciliation, and last launch. Read-only on both
sources.

| PR | Scope | Status |
|---|---|---|
| #17 | design spec | merged |
| #18 | implementation plan | merged |
| #19 | `usage` subcommand (`src/modelman/usage/`), `psycopg2-binary`, `__main__.py` | ✅ merged (`033674b`) |

Spec: `docs/superpowers/specs/2026-08-28-modelman-usage-design.md`

Plan: `docs/superpowers/plans/2026-08-28-modelman-usage.md`

Cross-repo status:
`agent-worktree/docs/superpowers/plans/2026-08-27-model-management-consolidation-status.md`

## Future cleanups

- Remove the vestigial `source` field — with no discovery, every model is
  user-configured, so `source` is always "curated"/None.
