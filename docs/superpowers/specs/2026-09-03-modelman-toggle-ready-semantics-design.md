# modelman toggle-ready semantics

**Status:** design (awaiting user review)
**Date:** 2026-09-03
**Author:** brainstorming session
**Scope:** Bug 1 of three ornith-1.5 readiness bugs (toggle does nothing, omlx). Bug 2 (llamacpp `files=None` probe) is a separate spec. Bug 3 (orphan HF cache blob) is deferred — handle via `huggingface-cli delete-cache`.

## Problem

Toggling ready (`x`) on a modelman-managed model whose files are already on disk has no observable effect:

1. `ModelScreen.on_mount` runs `_run_reconcile`, which sets `reconciled[mid]["ready"] = True` because the provider's `is_downloaded` returns True (files are on disk).
2. User presses `x` → `action_toggle_ready` sets `queued_ready[mid] = False`.
3. Status flips to `[yellow]↑[/yellow]` (queued to mark not ready).
4. User applies the queue. `PendingChanges.apply()` runs the ready loop, target=False branch:
   - Calls `_delete(variant)`, which falls back to `state.get(mid).disk_path` (often `None` for omlx → no-op file unlink).
   - Sets `state.ready=False, disk_path=None, size_bytes=None`.
   - Saves state.
5. User returns to the family and reopens it. `ModelScreen.on_mount` runs `_run_reconcile` again. Files are still on disk → `reconciled[mid]["ready"] = True`.
6. `_is_ready()` prefers the overlay → row shows `[green]✓[/green]` + 18.2 GB. The toggle is invisible.

State ends up with `ready = False` while the UI shows ready. The two diverge; the UI wins; the user's intent is lost.

A complementary symptom: toggling ready on a model whose files have appeared externally (e.g., user downloaded via another tool) also does nothing — the state flag never gets set because no one writes `state.ready = True` from reconcile; the existing `_run_apply` merge only runs on apply.

## Three-state model

modelman-managed models have three explicit states, in order:

1. **Configured** — entry exists in `registry.toml`. Not usable locally.
2. **Ready** — entry exists AND usable locally (file present for local providers; pulled/loaded for cloud).
3. **Exposed** — entry exists, ready, AND wired into LiteLLM's `model_list`.

`state.ready` captures (1)↔(2). `state.litellm_exposed` captures (2)↔(3). Strict progression: exposed requires ready; you cannot expose something that isn't ready. (`action_toggle_expose` already enforces this — the new design replaces the "refuse" path with a cascade.)

Toggle semantics per the user's spec:

- **Toggle exposed on** — adds model to litellm AND toggles ready on (which downloads or `ollama pull`).
- **Toggle exposed off** — removes model from litellm only; does not affect ready.
- **Toggle ready on** — sets flag and downloads or runs `ollama pull`.
- **Toggle ready off** — unsets flag and removes downloads or runs `ollama rm`.
- **Delete** — removes model from litellm; cleans up downloads or `ollama rm`; deletes model config.

## Goals

1. Implement the three-state model cleanly.
2. Eliminate the "toggle does nothing" bug for local-artifact models.
3. Make the cascade `expose → ready → download` work as specified.
4. Keep toggle ready (`x`) and toggle expose (`l`) as separate, meaningful actions.
5. Drive classification from config (`ModelEntry.location`, `ProviderEntry.location`), not hard-coded provider ids — providers change over time.

## Non-goals

- Changing how reconcile discovers files (filesystem-truth for local providers; `ollama list` / `ollama show` for ollama).
- Changing LiteLLM config format, registry/state file formats.
- Touching Bug 2 (llamacpp `files=None` probe) — separate spec.
- Touching Bug 3 (orphan HF cache blob) — handle via `huggingface-cli delete-cache` or manual `rm -rf`.
- Removing toggle semantics — they remain, but their effect changes for local-artifact models.

## Design

### 1. Config-driven local-artifact classification

A model has a **local artifact** when reconcile can sync `state.ready` from the filesystem. Classification is driven entirely by `ModelEntry.location` and `ProviderEntry.location`:

```python
def model_has_local_artifact(
    model: ModelEntry, provider: ProviderEntry | None
) -> bool:
    """True when reconcile can sync state.ready from the filesystem."""
    if model.location == "cloud":
        return False
    if provider is not None and provider.location == "cloud":
        return False
    return True
```

Truth table:

| Provider.location | Model.location | model_has_local_artifact |
|-------------------|----------------|--------------------------|
| `local` (or unset) | `local` (or unset) | True |
| `local` | `cloud` | **False** (ollama-cloud case) |
| `cloud` | any | False (native, openrouter) |

The `provider is None` branch handles the rare case of a model referencing a provider that has been removed from `registry.toml`. Such a model has no reconciled source either way; we treat it as not having a local artifact.

Adding a new provider to `registry.toml` with `location="local"` automatically opts it into reconcile-sync. No code change required.

The `ProviderPolicy.cloud` flag (in `litellm.py`) keeps its existing semantic — "must be ready before exposing" gate. It does not control reconcile behavior. The two concepts overlap but are not the same: ollama has `cloud=False` but ollama-cloud models aren't reconcile-synced; openrouter has `cloud=True` and isn't reconcile-synced.

### 2. Reconcile writes state for local-artifact models

Today `FamilyScreen._run_reconcile` and `ModelScreen._run_reconcile` build an in-memory `reconciled: dict[str, dict]` overlay that the UI prefers over `state.ready`. After this change, the overlay goes away; reconcile writes `state` directly.

For local-artifact models, reconcile sets:

- Files present → `state.ready = True`, `state.disk_path = <path>`, `state.size_bytes = <size>`.
- Files absent → `state.ready = False`, `state.disk_path = None`, `state.size_bytes = None`.

For non-local-artifact models, reconcile leaves `state.ready` alone. It may still update `state.disk_path` and `state.size_bytes` when the provider reports them (e.g., ollama cloud models have a `ollama:<name>` synthetic path once pulled).

Reconcile runs automatically on every `ModelScreen.on_mount` and `FamilyScreen.on_screen_resume`. The existing `action_reconcile` binding (`r`) is removed — manual refresh is unnecessary because reconcile is automatic. The `modelman sync` CLI command continues to call `sync.reconcile()`, which already writes to `state`; no change there.

### 3. UI reads state, not overlay

`ModelScreen._is_ready()` simplifies to `return self.state.get(model_id).ready`. The `reconciled` overlay and `_is_ready()` helper are removed.

`_refresh_details_panel()` (the `path: <...>` line) reads from `state.disk_path`.

The `STATUS` glyphs remain unchanged: queued glyphs (`✗` `→` `↓` `↑`) for pending changes; `✓`/`○` for current state. The reconcile-then-reload cycle makes these glyphs reflect reality: when files appear, next reconcile sets `state.ready=True` and the row shows `✓`; when files disappear, `state.ready=False` and the row shows `○`.

### 4. Toggle semantics

#### `r` — toggle ready (was reconcile)

For local-artifact models:

- **Target True:** queue a download (`provider.download(variant)`). On success, `state.ready=True`, `state.disk_path=<local_path>`, `state.size_bytes=<size>`.
- **Target False:** no-op with notification. "Reconcile controls local-model ready state; delete the file to mark not ready." This avoids the original bug (where the toggle's effect was invisible).

For non-local-artifact models:

- **Target True:** queue `provider.download(variant)`. For ollama cloud this is `ollama pull`; for openrouter (no provider class registered), it's a flag flip via `queue.py`'s `provider is None` branch.
- **Target False:** queue cleanup. For ollama cloud this is `provider.delete(variant)` → `ollama rm`. For openrouter, flag flip.

If the toggle target matches `state.ready` (idempotent), show a notification and no-op. This covers repeated keypresses on a model that's already in the target state.

#### `x` — toggle expose (was toggle ready)

For any model:

- **Target True:** queue `exposes[mid] = True`. **Also queue `queued_ready[mid] = True` if `state.ready == False`** (the cascade). This causes `apply()` to download/pull first, then add to litellm.
- **Target False:** queue `exposes[mid] = False` only. Does not touch `state.ready`.

If the toggle target matches `state.litellm_exposed` (idempotent), show a notification and no-op.

The "Model must be ready before exposing" notification in `action_toggle_expose` is removed — the cascade makes it unnecessary. The "Provider has no LiteLLM mapping" check remains (unchanged).

#### `e` — edit model (unchanged)

Same binding, same behavior. No conflict with the new key assignments.

### 5. Apply-time semantics

`PendingChanges.apply()` requires minimal changes:

- The ready loop's `target=True` branch already calls `provider.download()` and writes `state.ready=True, disk_path=<local_path>`. Unchanged.
- The ready loop's `target=False` branch (for local models) already handles file cleanup and writes `state.ready=False, disk_path=None, size_bytes=None`. Unchanged. The toggle's no-op-for-target-False rule means this branch is never entered for local models via `action_toggle_ready`; the `action_delete_model` path still reaches it.
- The expose loop runs after the ready loop, which already gives the cascade its correct order (download then expose).

`provider is None` routing in `queue.py` (for unmapped providers like openrouter) handles the flag-flip-only case. No new provider classes required.

### 6. Keybindings

Updated `ModelScreen.BINDINGS`:

```python
BINDINGS = [
    ("escape", "back", "Back"),
    ("a", "add_model", "Add"),
    ("d", "delete_model", "Delete"),
    ("e", "edit_model", "Edit"),
    Binding("enter", "select_row", "Edit", priority=True),
    ("r", "toggle_ready", "Toggle ready"),       # was "reconcile"
    ("x", "toggle_expose", "Toggle exposed"),    # was "toggle_ready"; description changed
]
```

`r` reconcile binding removed (reconcile is automatic). `x` is reused for expose; `l` is dropped from the binding table. Footer label changes from "Toggle LiteLLM" to "Toggle exposed" to remove the hard-coded LiteLLM reference.

The `action_toggle_expose` method name stays the same; only its `BINDINGS` description changes.

### 7. Files changed

| File | Change |
|------|--------|
| `src/modelman/registry.py` | Add `model_has_local_artifact(model, provider)` helper. |
| `src/modelman/screens/families.py` | Reconcile worker writes `state.ready`/`disk_path`/`size_bytes` for local-artifact models. Drop the `reconciled` overlay; reload reads from `state`. Remove the `reconcile` action binding (was `r`). |
| `src/modelman/screens/models.py` | Reconcile worker writes `state` for local-artifact models. Drop the `reconciled` overlay. `_is_ready()` → `state.get(mid).ready`. `_refresh_details_panel()` reads from `state.disk_path`. Update `BINDINGS` (drop `r` reconcile, rename `l` → `x` for expose, rename `x` → `r` for ready, change footer text). Update `action_toggle_ready` (no-op + notification for local models with target=False). Update `action_toggle_expose` to cascade `queued_ready[mid] = True` when target=True and `state.ready == False`. Drop the "Model must be ready" notification. |
| `src/modelman/queue.py` | No functional change; the existing `provider is None` branch and the ready/expose ordering already match the new design. |
| `tests/test_sync.py` | Add tests for reconcile-sync behavior: files appear/disappear between mounts, `state` updates correctly. |
| `tests/test_queue.py` | Add tests for cascade behavior: toggling expose on a not-ready model queues both ready and expose. |
| `tests/screens/test_model_screen.py` | Add tests for new toggle semantics: ready toggle no-op-with-notification for local models; expose toggle cascades. Update existing tests that read `reconciled` overlay to read `state` directly. |
| `tests/test_providers/test_*.py` | No change. Provider tests already cover `is_downloaded`/`download`/`delete`/`size_of`/`list_local` independently of state. |

### 8. Migration / backward compatibility

None required. Existing state files reconcile correctly on next mount:

- `state.ready = True` with `disk_path = "ollama:<name>"` → reconciles to `state.ready = True` if `ollama show` still succeeds; demotes if not.
- `state.ready = True` with `disk_path = "<hf_cache_path>"` → reconciles to `state.ready = True` if files still present; demotes if not.
- `state.ready = False` with no `disk_path` → reconciles to `state.ready = True` if files appear externally; no-op otherwise.

The user-reported bug case (omlx/ornith-1.5 with files on disk but `state.ready = false`) auto-fixes on next mount: reconcile sees files, sets `state.ready = True`, `state.disk_path = "/Users/keith/.omlx/models/Ornith-1.5-35B-A3B-MLX-4bit"`, `state.size_bytes = 19530941006`.

### 9. Testing strategy

#### Unit tests

`test_sync.py`:
- Reconcile sets `state.ready=True, disk_path=<path>, size_bytes=<size>` when files appear (local-artifact model).
- Reconcile sets `state.ready=False, disk_path=None, size_bytes=None` when files disappear.
- Reconcile leaves `state.ready` alone for non-local-artifact models.
- `model_has_local_artifact` returns False for ollama-cloud models.
- `model_has_local_artifact` returns False for native providers regardless of `ModelEntry.location`.
- `model_has_local_artifact` returns True for llamacpp/omlx regardless of `ModelEntry.location`.
- Reconcile writes via `call_from_thread` in TUI (existing pattern).

`test_queue.py`:
- Apply with `queued_ready[mid] = True` and `queued_exposes[mid] = True` runs download first, then expose.
- Apply with `queued_ready[mid] = False` for a local-artifact model cleans up files (existing tests, unchanged).

#### Screen tests

`test_model_screen.py`:
- Press `x` on a ready local-artifact model → no-op + notification.
- Press `r` on a not-ready local-artifact model → queues download.
- Press `r` on a not-ready cloud model → queues download.
- Press `x` on a not-ready model → queues both ready and expose.
- Press `x` on a ready model → queues only expose.
- Footer reads "Toggle exposed" not "Toggle LiteLLM".
- `BINDINGS` no longer contains `("r", "reconcile", ...)`.
- `_is_ready()` reads from `state.ready`, not overlay.

#### Provider tests

`test_providers/test_*.py`: unchanged. Provider contract doesn't change.

### 10. Out of scope

Bug 2 (llamacpp `files=None` probe): separate spec. The fix is for `LlamaCppProvider.is_downloaded`/`size_of`/`path_of` to probe the HF cache for blobs even when `variant.files` is `None`/`[]`. Independent of this spec.

Bug 3 (orphan HF cache blob at `~/.cache/huggingface/hub/models--ornith-ai--Ornith-1.5-35B-A3B-GGUF`): user can `huggingface-cli delete-cache` or `rm -rf` the orphan directory. modelman does not need to detect or clean orphans in this spec.

## Open questions

None at design time. Implementation questions are deferred to the writing-plans skill.

## Implementation order

1. Add `model_has_local_artifact` helper in `registry.py` with unit tests.
2. Update `FamilyScreen._run_reconcile` to write `state` via `call_from_thread`. Drop `_reconciled` overlay. Update `reload()` to read from `state`. Update screen tests.
3. Update `ModelScreen._run_reconcile` and dependents identically.
4. Update `action_toggle_ready`: idempotent for matching state, no-op + notification for local models with target=False. Add screen tests.
5. Update `action_toggle_expose`: cascade `queued_ready[mid] = True` when target=True and not ready. Drop the "Model must be ready" notification. Add screen tests.
6. Update `BINDINGS`: drop `r` reconcile, swap `r`/`x`/`l`. Update footer text. Add screen tests.
7. Add tests in `test_sync.py` and `test_queue.py` for cascade and reconcile-sync behavior.
8. Run full test suite (`make all`) to confirm no regressions.