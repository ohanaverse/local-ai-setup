# Spec: `modelman sync` — ollama discovery

**Date:** 2026-08-27
**Status:** approved (in chat)
**Branch:** (created at implementation time)

## Problem

`registry.toml` is the canonical model/provider registry, but it's only
populated by `modelman migrate` (one-time import) and manual TUI edits.
There's no way to refresh it from what's actually available on the local
machine. The shared-model-registry design spec
(`2026-08-27-shared-model-registry-design.md`) assigns modelman the
"provider sync" capability — absorbing wt's `internal/registry` live
discovery (`ollama list`, OpenRouter API) — but none of it exists yet.

## Goal

Add a `modelman sync` CLI subcommand that discovers ollama models via
`ollama list` and merges them into `registry.toml` (curated-wins) while
updating `modelman.toml`'s downloaded state.

## Non-goals

- OpenRouter discovery — separate PR (placeholder spec
  `2026-08-27-modelman-sync-openrouter-design.md`).
- llamacpp/omlx `model_dir` scanning — separate PR (placeholder spec
  `2026-08-27-modelman-sync-modeldir-design.md`).
- Deleting registry rows — sync is purely additive.
- LiteLLM exposure — separate feature.
- Re-modeling the registry schema (`registry.py` is unchanged).

## Design

### Entry point

`modelman sync` — a Typer subcommand in `main.py`, mirroring `migrate`.
Loads `registry.toml` + `modelman.toml`, runs the sync, saves both, and
prints a one-line summary.

### Discovery

`discover_ollama(runner)` runs `ollama list` and parses each row into a
`ModelEntry` candidate plus a size. The SIZE column's first token is `-`
for cloud models (not pulled locally) and a number for local models.

Per row:

- `id = "ollama/<tag>"`
- `family = tag` (each ollama model is its own family)
- `provider_id = "ollama"`
- `model_name = tag`
- `location = "local"` if SIZE is a number, else `"cloud"`
- `source = "discovered"`

Returns `(models, sizes)` where `sizes` maps tag → size_bytes for local
models only.

### Merge (curated-wins)

`merge(registry, discovered)` mutates `registry.models` in place and
returns a `SyncResult`:

- id already present and `source == "curated"` → skip (record in
  `skipped`).
- id already present and `source == "discovered"` → refresh `location`
  (record in `refreshed`).
- id absent → append the new row (record in `added`).

`tags`, `cost`, and `family` are never touched by sync, for any row.

### model_info auto-detection

For new models only (id absent from the registry), run
`ollama_caps.auto_detect_model_info(tag, runner)` to populate
`model_info`. Existing rows keep their `model_info` (re-running
`ollama show` on every sync is wasted work for a field that rarely
changes).

### State update

`update_state(state, models, sizes)` sets, for each discovered model:

- local → `downloaded=True`, `disk_path="ollama:<tag>"`,
  `size_bytes=sizes[tag]`.
- cloud → `downloaded=False`, `disk_path=None`, `size_bytes=None`.

`litellm_exposed` is preserved (it's owned by the LiteLLM feature, not
sync).

### Promote on edit

The TUI's add/edit path (`_variant_to_model_entry` in
`screens/models.py`) sets `source="curated"` on the resulting
`ModelEntry`. This is the "flip to curated on edit" rule: once the user
touches a row, sync leaves it alone.

## File map

- Create: `src/modelman/sync.py` — `discover_ollama`, `merge`,
  `update_state`, `sync`, `SyncResult`.
- Modify: `src/modelman/main.py` — add the `sync` command.
- Modify: `src/modelman/screens/models.py` — set `source="curated"` in
  `_variant_to_model_entry`.

Files NOT touched: `registry.py`, `state.py`, `providers/*.py`,
`ollama_caps.py` (reused as-is).

## Error handling

- `ollama list` fails (ollama not installed / command error) → raise
  `SyncError`; the command exits non-zero with a clear message.
- `ollama show` fails → non-fatal; `auto_detect_model_info` returns `{}`.
- Save failures → propagate (existing atomic-write behavior).

## Testing strategy

TDD. Tests first.

1. `_parse_ollama_list` unit tests: local row, cloud row, header skip,
   malformed row skip.
2. `merge` unit tests: curated skip, add new, refresh discovered
   `location`, preserve `tags`/`cost`/`family`.
3. `update_state` unit tests: local vs cloud, preserve `litellm_exposed`.
4. `sync` integration test with a mock runner (discover + merge +
   state update + model_info for new only).
5. CLI test: `modelman sync` saves registry+state and prints a summary.
6. TUI test: add/edit sets `source="curated"`.

## Risks

- **Stale discovered rows** — sync never deletes, so a model removed
  from ollama leaves a stale `discovered` row. Accepted for this PR
  (deletion is a follow-up).
- **`ollama show` cost** — only run for new models, so a large existing
  registry doesn't pay it on every sync.
- **Family clutter** — each ollama tag becomes its own family, which
  can clutter the FamilyScreen. Accepted; the user can regroup in the
  TUI.
