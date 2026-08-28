# Model Management Consolidation — Phase Status

**Not an execution plan.** This is a cross-repo status tracker for a
multi-repo effort spanning `agent-worktree` (this repo), `modelman`, and
`local-ai-setup`. Each sub-project below gets its own
brainstorm → spec → plan cycle in the repo it lands in; this doc just
tracks where each one stands so a fresh session doesn't have to
re-derive it from `git log` across three repos.

**Last verified:** 2026-08-28, against `modelman`'s `gh pr view` directly
(not from memory — status here goes stale fast; re-verify before trusting
it).

## Why this effort exists

Each repo grew its own model/provider metadata model independently:
`wt`'s `config.toml` (Provider/Model/Agent), `modelman`'s `config.yaml` +
`families/*.yaml`, and LiteLLM's `model_list` in `local-ai-setup`. That
causes drift. Model management, per the original framing, has three areas:

1. Provider/model registry across ollama/llama.cpp/omlx/openrouter
2. Which models are exposed via LiteLLM
3. Configuring an agent to use a chosen model

The full scope was too large for one spec, so it's split into sub-projects.

## Sub-project 1 — Shared model registry

Areas 1+2. `modelman` becomes the sole owner of the registry; `wt` becomes
a read-only consumer of it. This is the foundation the other sub-projects
build on.

Spec: `modelman/docs/superpowers/specs/2026-08-27-shared-model-registry-design.md`

| Step | What it does | Status |
|---|---|---|
| Phase 1 | Schema + migration (`registry.py`/`state.py`/`migrate.py`) | ✅ Merged — `modelman` PR #2 (`e880aa6`) |
| Phase 2 PR 1 | Additive `Registry.families()`/`models_by_family()`/`provider_config()` + `modelman.toml` family-display-name overlay; zero consumers changed | ✅ Merged — `modelman` PR #3 (`5959bc0`) |
| Phase 2 PR 2 | `queue.py` `PendingChanges` + `screens/models.py` `ModelScreen` rewritten onto `Registry`/`StateStore` (the big one) | ✅ Merged — `modelman` PR #4 (`e535dc2`) |
| Phase 2 PR 3 | Migrate `FamilyScreen` + `AddFamilyModal`/`EditFamilyModal` off globbing `families/*.yaml` onto `Registry`/`StateStore` | ✅ Merged — `modelman` PR #5 (`9ba6714`, 2026-08-28) |
| Phase 2 PR 4 | Clean up `app.py`'s `--initial-family` path off the legacy manifest; drop `MODELMAN_CONFIG`/`MODELMAN_FAMILY_DIR` env-vars | ⬜ Not yet planned or written — **next open step** |

Phase 2 PR 3 also fixed a live regression: `FamilyScreen` was still
constructing `ModelScreen` with PR 2's removed legacy positional
signature, so every "open family" action was broken at runtime (masked
by tests that were marked skip pending this PR) until it landed.

Side spec (deferred out of Phase 2 PR 2's scope, tracked separately):
`modelman/docs/superpowers/specs/2026-09-02-modelman-add-dialog-simplification.md`
— collapse the 6-field add-model dialog to one HF-paste-style `model`
field. Status: approved in chat, no plan written, not implemented.

`agent-worktree`'s own side of sub-project 1 (becoming a read-only registry
consumer) has no work started yet — it's blocked on this sub-project
reaching at least Phase 2 PR 4.

## Sub-project 2 — Benchmark tooling

Formalize `local-ai-setup`'s ad hoc bash scripts (LiteLLM proxy +
benchmarking) into an actual tool. **Not yet brainstormed.**

Note: `local-ai-setup` has two one-off benchmark runs landing in parallel
(`2026-08-26-ornith-1.5-benchmark`, `2026-08-27-qwen3.8-openrouter-benchmark`)
that use the *existing* ad hoc scripts — that's normal usage, not this
formalization effort. Don't conflate the two when checking status.

## Sub-project 3 — Usage/spend tracking

Reconcile `wt`'s `usage.jsonl`/rotation state with LiteLLM's spend DB.
**Not yet brainstormed.**

## Conventions for this effort

- Specs/plans for this consolidation work are **committed to git** in
  whichever repo they land in (not left as untracked PR-comment
  attachments) — an explicit override of this repo's global default, agreed
  with the user when the shared-model-registry spec was written.
- When picking up follow-up work, verify status directly (`git log`,
  `gh pr list --state all`, `find docs/superpowers -type f`) in the repo in
  question rather than trusting this doc's checkmarks without a re-check.
