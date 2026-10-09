# Spec: `modelman sync` — ollama reconcile-only revision

**Date:** 2026-08-28
**Status:** in progress
**Branch:** feat/reconcile-ollama

## Problem

PR #8 (`2026-08-27-modelman-sync-ollama-design.md`) implemented `modelman
sync` as a *discovery* mechanism: it ran `ollama list`, parsed every model
(local and cloud), and added any not already in `registry.toml` as
`source="discovered"` rows. That contradicts the intended model: sync
should *reconcile* the models already configured in modelman against
provider reality, never add new ones.

The principle, per user direction: **all providers only list models that
have been added to modelman; never list all available models for any
provider.** `ollama list` tells us what's actually downloaded; modelman's
config may hold a different set; sync resolves the difference.

## Goal

Revise `modelman sync` so it only updates the downloaded state of ollama
models already present in `registry.toml`. It never adds new models.

## Non-goals

- model_dir reconcile (llamacpp/omlx) — separate PR (placeholder spec
  `2026-08-27-modelman-sync-modeldir-design.md`).
- openrouter — no-op; models are explicitly configured, no sync (see
  `2026-08-27-modelman-sync-openrouter-design.md`).
- Deleting stale rows — sync remains additive (never deletes).
- Removing the now-vestigial `source` field — separate cleanup.
- Switching from the ollama CLI to the `ollama` Python package — decided
  against (see "CLI, not the `ollama` package" below).

## Design

### Reconcile, not discover

`sync` runs `ollama list` to learn which models are actually downloaded
(local), then updates `modelman.toml`'s `downloaded`/`disk_path`/
`size_bytes` for each configured ollama model. Models in `ollama list`
that are not in `registry.toml` are ignored. `registry.toml` is never
written by sync — it is read-only input.

### `list_ollama`

`list_ollama(runner)` runs `ollama list` and returns
`dict[str, int]` mapping `model_name -> size_bytes` for downloaded
(local) models only. Cloud rows (SIZE column `-`) are skipped. Raises
`SyncError` if the command fails.

### `reconcile`

`reconcile(registry, state, downloaded)` iterates configured ollama models
(`provider_id == "ollama"`) and, for each:

- `model_name` in `downloaded` → `downloaded=True`,
  `disk_path="ollama:<name>"`, `size_bytes=downloaded[name]`.
- otherwise → `downloaded=False`, `disk_path=None`, `size_bytes=None`.

`litellm_exposed` is preserved (owned by the LiteLLM feature, not sync).
Non-ollama models are untouched. Returns a `SyncResult`.

### `SyncResult`

```python
@dataclass
class SyncResult:
    downloaded: list[str] = field(default_factory=list)
    not_downloaded: list[str] = field(default_factory=list)
```

Replaces the old `added`/`refreshed`/`skipped` fields (merge semantics,
removed). Provider-agnostic, so the modeldir PR can reuse it.

### `sync`

`sync(registry, state, runner)` = `list_ollama(runner)` then
`reconcile(registry, state, downloaded)`.

### Command

`modelman sync` loads `registry.toml` + `modelman.toml`, runs sync, saves
`modelman.toml` only (registry is read-only), and prints:

```
Synced ollama: 3 downloaded, 2 not downloaded.
```

### CLI, not the `ollama` package

Explored switching to the official `ollama` Python client. Decided
against:

- **Download flow** — `ollama pull` uses a Popen runner for live progress
  streaming and cancel-on-ESC; the package's `pull()` is blocking with no
  clean cancellation. Switching would regress a real feature.
- **Server requirement** — `ollama list` reads the manifest from disk and
  works even when the server is down; the package's `list()`/`show()` are
  HTTP calls that fail without a running server. For "reconcile what's
  downloaded", that robustness matters.
- **Dependency** — no new dep; the `ollama` binary is already required for
  download anyway.

The package's structured-data benefit is real but the text parsing is
already written and tested, so the win is marginal. Revisit only if the
parsing becomes a maintenance burden.

## File map

- Modify: `src/modelman/sync.py` — replace `discover_ollama`/`merge`/
  `update_state` with `list_ollama`/`reconcile`; redefine `SyncResult`;
  replace `_parse_ollama_list` with `_parse_ollama_list_sizes`.
- Modify: `src/modelman/main.py` — drop `save_registry`, new summary line.
- Modify: `tests/test_sync.py` — rewrite for reconcile-only.
- Modify: `tests/commands/test_sync.py` — new summary, no registry save.

Files NOT touched: `registry.py`, `state.py`, `providers/*.py`,
`screens/models.py` (the `source="curated"` TUI change stays).

## Error handling

- `ollama list` fails (ollama not installed / command error) → raise
  `SyncError`; the command exits non-zero with a clear message.
- Save failures → propagate (existing atomic-write behavior).

## Testing strategy

TDD. Tests first.

1. `_parse_ollama_list_sizes` unit tests: local row, cloud row skip,
   header skip, malformed row skip.
2. `list_ollama` unit tests: returns sizes; raises `SyncError` on failure.
3. `reconcile` unit tests: downloaded, not-downloaded, non-ollama skipped,
   preserve `litellm_exposed`.
4. `sync` integration test with a mock runner.
5. CLI test: saves state (not registry), prints the new summary.

## Risks

- **Stale rows** — sync never deletes, so a configured model removed from
  ollama stays `downloaded=False` forever. Accepted (deletion is a
  follow-up).
- **`source` vestigial** — with no discovery, `source` is always
  "curated"/None. Left in place; removal is a separate cleanup.
