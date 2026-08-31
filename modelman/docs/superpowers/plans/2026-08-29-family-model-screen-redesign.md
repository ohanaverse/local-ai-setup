# Family & Model Screen Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Replace "downloaded" with a provider-agnostic "ready" concept (fixing a real ollama `:cloud` reconcile bug), collapse the model screen's two-pane layout into one sorted table, hard-block family delete whenever a family has any models, and add native-provider support (agent-worktree agents as model providers with no download mechanics).

**Architecture:** `ModelState.downloaded` becomes `ModelState.ready`; both screens' reconcile loops switch their truth signal from `provider.size_of()` to `provider.is_downloaded()`. A new `sync_agent_providers()` helper mirrors the existing `DEFAULT_PROVIDER_IDS` repair pattern in `sync.py`, reading agent names from agent-worktree's `config.toml` and registering them as `auth.type="native"` providers. `ModelScreen` drops its provider/model two-table split for one table and replaces `queued_downloads` with `queued_ready` (a target-state dict); `PendingChanges.downloads` becomes `PendingChanges.ready`, whose apply step branches on provider reconcilability (real download/delete call vs. a bare state-flag flip) and provider readiness (skip a no-op toggle).

**Tech Stack:** Python 3.13, Textual (TUI), tomllib/tomli_w, pytest + pytest-asyncio.

**Spec:** `docs/superpowers/specs/2026-08-29-family-model-screen-redesign-design.md`

## Global Constraints

- `modelman.toml`'s legacy `downloaded` key must still load correctly after the `ready` rename — no migration script, no data loss for existing installs.
- The `AuthConfig.type` value `"native"` (already documented in `registry.py`) is what marks a provider as flag-only/native; native providers must never gain a `ProviderRegistry`-registered Python class.
- Native providers' sentinel model name is exactly `"native"` (not `"default"`) — this must match `agent-worktree`'s just-landed `Model.Native` convention (`ID: agent + "/native"`) so registry.toml stays a valid shared source for `wt`.
- `openrouter` gets no new `Provider` Python class — it stays flag-only, same mechanism as native providers.
- Every existing test file this plan touches must still pass in full (`make test`) after each task — no regressions in unrelated coverage.

---

## Task 1: Rename `ModelState.downloaded` → `ready`

**Files:**
- Modify: `src/modelman/state.py:41-46` (dataclass field), `src/modelman/state.py:99-108` (`load_state`), `src/modelman/state.py:126-135` (`save_state`)
- Modify: `src/modelman/litellm.py:272` (`_validated_entry`'s downloaded gate)
- Modify: `src/modelman/sync.py:198-224` (`reconcile()`'s two `ModelState(downloaded=...)` constructions)
- Modify: `tests/test_state.py` (every `ModelState(downloaded=...)` / `.downloaded` reference)
- Modify: `tests/test_queue.py` (every `ModelState(downloaded=...)` / `.downloaded` reference — the list attribute `PendingChanges.downloads` is untouched in this task; only the dataclass field renames)
- Test: `tests/test_state.py` (new legacy-key test)

**Interfaces:**
- Produces: `ModelState.ready: bool` (replaces `.downloaded`). `load_state(path)` accepts either TOML key (`ready` if present, else legacy `downloaded`). `save_state` always writes `ready`.
- Consumes: nothing from other tasks (this is the foundation task).

- [x] **Step 1: Write the failing legacy-key-fallback test**

Add to `tests/test_state.py` (near the existing round-trip test):

```python
def test_load_state_accepts_legacy_downloaded_key(tmp_path):
    # Existing installs have modelman.toml files with the old `downloaded`
    # key. Renaming the field must not silently drop their ready state.
    path = tmp_path / "modelman.toml"
    path.write_text(
        '[model_state."ollama/x"]\n
> **Merged in PR #25** (`feat/family-model-screen-redesign` → `main`, 2026-08-30).
'
        "downloaded = true\n"
        'disk_path = "/models/x"\n'
    )
    store = load_state(path)
    assert store.get("ollama/x").ready is True
    assert store.get("ollama/x").disk_path == "/models/x"


def test_save_writes_ready_key_not_legacy_downloaded(tmp_path):
    # Going forward, only the new key should ever be written.
    path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/x", ModelState(ready=True))
    save_state(store, path)
    raw_text = path.read_text()
    assert "ready = true" in raw_text
    assert "downloaded" not in raw_text
```

- [x] **Step 2: Run the new tests to verify they fail**

Run: `uv run pytest tests/test_state.py -k "legacy_downloaded or writes_ready" -v`
Expected: FAIL — `AttributeError: 'ModelState' object has no attribute 'ready'` (field doesn't exist yet).

- [x] **Step 3: Rename the field and fix load/save**

In `src/modelman/state.py`, change the dataclass:

```python
@dataclass
class ModelState:
    ready: bool = False
    disk_path: str | None = None
    size_bytes: int | None = None
    litellm_exposed: bool = False
    extra: dict[str, Any] = field(default_factory=dict, repr=False)
```

In `load_state`, change the `ModelState(...)` construction inside the `models` dict comprehension:

```python
    models = {
        model_id: ModelState(
            ready=entry.get("ready", entry.get("downloaded", False)),
            disk_path=entry.get("disk_path"),
            size_bytes=entry.get("size_bytes"),
            litellm_exposed=entry.get("litellm_exposed", False),
            extra=unknown_keys(
                entry, {"ready", "downloaded", "disk_path", "size_bytes", "litellm_exposed"}
            ),
        )
        for model_id, entry in raw.get("model_state", {}).items()
    }
```

In `save_state`, change the per-model payload dict:

```python
        "model_state": {
            model_id: drop_none(
                {
                    **s.extra,
                    "ready": s.ready,
                    "disk_path": s.disk_path,
                    "size_bytes": s.size_bytes,
                    "litellm_exposed": s.litellm_exposed,
                }
            )
            for model_id, s in store.models.items()
        },
```

- [x] **Step 4: Fix the two direct dependents outside state.py**

In `src/modelman/litellm.py`, change line 272:

```python
    if not policy.cloud and not state.get(model_id).ready:
        raise ExposeError(f"model {model_id!r} is not downloaded")
```

In `src/modelman/sync.py`, in `reconcile()`, change both `ModelState(downloaded=...)` constructions to `ModelState(ready=...)` (keep every other kwarg identical):

```python
            state.set(
                m.id,
                ModelState(
                    ready=True,
                    disk_path=disk_path,
                    size_bytes=size,
                    litellm_exposed=existing.litellm_exposed,
                ),
            )
            result.downloaded.append(m.id)
        else:
            state.set(
                m.id,
                ModelState(
                    ready=False,
                    disk_path=None,
                    size_bytes=None,
                    litellm_exposed=existing.litellm_exposed,
                ),
            )
            result.not_downloaded.append(m.id)
```

(`SyncResult.downloaded`/`.not_downloaded` field *names* are untouched — only the `ModelState` kwarg renames. Renaming `SyncResult`'s own fields is out of scope for this plan.)

- [x] **Step 5: Mechanically rename every other `ModelState(downloaded=...)` / `.downloaded` reference in the two test files**

Run this to find every remaining call site in the two files this task owns:

```bash
grep -n "ModelState(downloaded=\|\.downloaded\b" tests/test_state.py tests/test_queue.py
```

For each match, apply the same two substitutions:
- `ModelState(downloaded=` → `ModelState(ready=`
- `<expr>.downloaded` (where `<expr>` resolves to a `ModelState` — i.e. `state.get(...).downloaded`, `store.get(...).downloaded`, or a `ModelState(...)` variable's `.downloaded`) → `<expr>.ready`

Do **not** touch `PendingChanges(downloads=[...])` or `pending.failures`/`queue.py`'s `downloads` list attribute — those are a different, unrelated name (`downloads`, not `downloaded`) and are out of scope until Task 7.

In `tests/test_state.py` this affects `test_save_then_load_round_trips_model_state` (both the `ModelState(downloaded=True, ...)` construction and the `ModelState(downloaded=True, ...)` expected-value assertion) and `test_save_then_load_preserves_unknown_keys` (its raw TOML fixture already writes `downloaded = true` under `[model_state."ollama/x"]` — leave that raw TOML line as-is, since it's specifically testing that a *legacy* on-disk file still loads; only its assertion `loaded.get("ollama/x").extra == {"custom_field": "keep-me"}` needs no change, but add `assert loaded.get("ollama/x").ready is True` to confirm the legacy key was picked up).

In `tests/test_queue.py`, this affects `test_apply_clear_state_for_deleted_model` (`ModelState(downloaded=True, disk_path="/old/path")`) and any other `ModelState(downloaded=...)` construction the grep turns up.

- [x] **Step 6: Run the full test file suite for this task**

Run: `uv run pytest tests/test_state.py tests/test_queue.py tests/test_registry.py -v`
Expected: all PASS. (`test_queue.py`'s `downloads=[...]` list-based tests still pass unchanged — Task 7 rewrites those.)

- [x] **Step 7: Run the full suite to catch any other `.downloaded` reference this task missed**

Run: `uv run pytest 2>&1 | tail -40`
Expected: failures only in files this plan will touch in later tasks (`screens/families.py`, `screens/models.py`, `queue.py`, and their tests) — every failure should trace back to `AttributeError: ... no attribute 'downloaded'` in one of those not-yet-updated files. If a failure appears in a file *not* on that list, fix it here (it means this task's grep missed a call site).

- [x] **Step 8: Commit**

```bash
git add src/modelman/state.py src/modelman/litellm.py src/modelman/sync.py tests/test_state.py tests/test_queue.py
git commit -m "feat(state): rename ModelState.downloaded to ready, accept legacy key - completes plan item #1"
```

---

## Task 2: Reconcile fix — use `is_downloaded()` as the ready signal

**Files:**
- Modify: `src/modelman/screens/families.py:133-159` (`_run_reconcile`), `src/modelman/screens/families.py:161-203` (`reload`, which reads `rec["downloaded"]`)
- Modify: `src/modelman/screens/models.py:221-265` (`_run_reconcile`), `src/modelman/screens/models.py:299-308` (`_is_downloaded`, renamed `_is_ready`), `src/modelman/screens/models.py:310-350` (`_load_models_for_provider`, reads `rec["downloaded"]`), `src/modelman/screens/models.py:445-473` (`action_delete_model`, drops the `location != "cloud"` special-case)
- Modify: every call site of `ModelScreen._is_downloaded` (grep for it) to `_is_ready`
- Test: `tests/screens/test_models.py` (update `_seed_cloud_family`'s stub + all three tests that use it)
- Test: `tests/screens/test_app_navigation.py` (update every `stub.size_of.return_value = None`/`.side_effect` pattern used to control reconcile readiness — grep list below)

**Interfaces:**
- Consumes: `ModelState.ready` from Task 1.
- Produces: `FamilyScreen._reconciled[model_id]["ready"]` (renamed from `["downloaded"]`), `ModelScreen.reconciled[model_id]["ready"]` (renamed from `["downloaded"]`), `ModelScreen._is_ready(model_id) -> bool` (renamed from `_is_downloaded`).

- [x] **Step 1: Write the failing ollama `:cloud`-now-ready test**

Add to `tests/screens/test_models.py`, reusing the file's existing `_seed_cloud_family` helper shape but stubbing `is_downloaded` instead of `size_of`:

```python
@pytest.mark.asyncio
async def test_pulled_cloud_model_reconciles_as_ready(tmp_path, monkeypatch):
    """A pulled ollama `:cloud` model has no local size (size_of() -> None)
    but `ollama show` (is_downloaded()) succeeds. Reconcile must report it
    ready — today it never can, because reconcile's truth signal is
    size_of(), not is_downloaded()."""
    from unittest.mock import MagicMock

    from modelman.app import ModelmanApp

    _seed_cloud_family(tmp_path, monkeypatch, location="cloud")

    from modelman.providers import registry as provider_registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = None  # cloud rows have no SIZE
    stub.is_downloaded.return_value = True  # but `ollama show` succeeds
    monkeypatch.setattr(
        provider_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub)
    )

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.pause()  # let the reconcile worker settle

        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)
        assert app.screen._is_ready("ollama/glm-5.2:cloud") is True
```

- [x] **Step 2: Run it to verify it fails**

Run: `uv run pytest tests/screens/test_models.py -k pulled_cloud_model_reconciles_as_ready -v`
Expected: FAIL — either `AttributeError: 'ModelScreen' object has no attribute '_is_ready'` or (once renamed) `assert False is True` (still reading `size_of`).

- [x] **Step 3: Fix `FamilyScreen._run_reconcile` and `reload`**

In `src/modelman/screens/families.py`, `_run_reconcile` (around line 148-158), change the loop body:

```python
            size: int | None = None
            ready = False
            try:
                ready = bool(provider.is_downloaded(spec))  # type: ignore[attr-defined]
            except Exception:
                ready = False
            try:
                raw = provider.size_of(spec)  # type: ignore[attr-defined]
                if isinstance(raw, int):
                    size = raw
            except Exception:
                size = None
            self._reconciled[m.id] = {
                "ready": ready,
                "size": size,
            }
```

In `reload()` (around line 175-190), rename every `rec["downloaded"]` to `rec["ready"]`:

```python
                if rec is not None:
                    if rec["ready"]:
                        downloaded_count += 1
                        ...
```

- [x] **Step 4: Fix `ModelScreen._run_reconcile`, `_is_downloaded`→`_is_ready`, `_load_models_for_provider`**

In `src/modelman/screens/models.py`, `_run_reconcile` (around line 236-263):

```python
            for m in entries:
                size: int | None = None
                spec = _model_entry_to_variant(m)
                try:
                    ready = bool(provider.is_downloaded(spec))  # type: ignore[attr-defined]
                except Exception:
                    ready = False
                try:
                    raw = provider.size_of(spec)  # type: ignore[attr-defined]
                    if isinstance(raw, int):
                        size = raw
                except Exception:
                    size = None
                local_path: str | None = None
                if ready and hasattr(provider, "list_local"):
                    try:
                        for lm in provider.list_local():
                            lm_name = lm.get("name") or lm.get("variant_id")
                            if lm_name == m.model_name or lm_name == m.id:
                                lp = lm.get("local_path") or lm.get("path")
                                if isinstance(lp, str):
                                    local_path = lp
                                break
                    except Exception:
                        pass
                self.reconciled[m.id] = {
                    "ready": ready,
                    "size": size,
                    "local_path": local_path,
                }
```

Rename `_is_downloaded` to `_is_ready` (around line 299-308):

```python
    def _is_ready(self, model_id: str) -> bool:
        """Truth about whether a model is ready to use.

        Prefers the reconcile overlay (reality); falls back to state
        when reconcile hasn't run for this model yet.
        """
        rec = self.reconciled.get(model_id)
        if rec is not None:
            return bool(rec.get("ready"))
        return self.state.get(model_id).ready
```

In `_load_models_for_provider` (around line 310-350), replace every `rec.get("downloaded")` / `downloaded` local variable with `rec.get("ready")` / `ready`, and every call site of `self._is_downloaded(...)` in the file with `self._is_ready(...)` (grep confirms these are `action_toggle_download`, `action_toggle_expose`, `action_delete_model` — Tasks 5/6 rewrite those bodies further, but the rename must land now so the file imports cleanly).

- [x] **Step 5: Remove the `location != "cloud"` special-case in `action_delete_model`**

In `src/modelman/screens/models.py`, `action_delete_model` (around line 461), change:

```python
        if not self._is_ready(mid):
            self.app.notify("Model not downloaded — nothing to delete")
            return
```

(drops the `entry.location != "cloud" and` prefix — `_is_ready` now already reports `True` for a pulled cloud model via `is_downloaded()`, so the special-case is redundant.)

- [x] **Step 6: Update the three existing `_seed_cloud_family`-based tests in `tests/screens/test_models.py`**

`_seed_cloud_family`'s stub currently only sets `stub.size_of.return_value = None`. Add `stub.is_downloaded.return_value = False` as the default (matching "not ready" for the not-downloaded cases), and have the one test that simulates a real download override it:

```python
def _seed_cloud_family(tmp_path, monkeypatch, *, location: str | None):
    ...  # unchanged setup
    stub = MagicMock()
    stub.size_of.return_value = None  # cloud rows print SIZE '-'
    stub.is_downloaded.return_value = False
    stub.name = "ollama"
    monkeypatch.setattr(...)
    return stub
```

In `test_d_on_cloud_model_queues_delete`, after calling `_seed_cloud_family(..., location="cloud")`, add `stub = _seed_cloud_family(...)` capture and set `stub.is_downloaded.return_value = True` (a cloud model that's actually pulled) so the delete gate's `_is_ready` check passes:

```python
@pytest.mark.asyncio
async def test_d_on_cloud_model_queues_delete(tmp_path, monkeypatch):
    stub = _seed_cloud_family(tmp_path, monkeypatch, location="cloud")
    stub.is_downloaded.return_value = True  # pulled: `ollama show` succeeds

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()

        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)
        await pilot.press("d")
        await pilot.pause()
        assert "ollama/glm-5.2:cloud" in app.screen.queued_deletes
```

`test_d_on_local_not_downloaded_does_not_queue_but_notifies` needs no change beyond the new default (`is_downloaded.return_value = False` from the helper already matches "not ready").

`test_d_on_downloaded_local_model_still_queues` sets `stub.size_of.return_value = 22 * 1024**3` — add `stub.is_downloaded.return_value = True` alongside it (both are now consulted; `size_of` for the SIZE column, `is_downloaded` for readiness).

- [x] **Step 7: Update every `stub.size_of`-only reconcile stub in `tests/screens/test_app_navigation.py`**

Run this to find every stub in the file that sets `size_of` without also setting `is_downloaded` (these are the ones controlling readiness, now bypassed):

```bash
grep -n "stub.size_of.return_value\|stub.size_of.side_effect" tests/screens/test_app_navigation.py
```

For each, add a matching `stub.is_downloaded.return_value = ...` (or `.side_effect = ...` mirroring the same predicate) so the test's intended ready/not-ready state is preserved under the new truth signal. Concretely:

- `test_status_shows_four_states`: `fake_size_of` returns `10` for `ollama/dl`, `None` for `ollama/missing`. Add `stub.is_downloaded.side_effect = lambda v: v["id"] == "ollama/dl"`.
- `test_delete_action_noop_on_not_downloaded`: `stub.size_of.return_value = None`. Add `stub.is_downloaded.return_value = False`.
- `test_add_then_delete_model_queues_changes`: `stub.size_of.return_value = 10 * 1024**3`. Add `stub.is_downloaded.return_value = True`.
- `test_reconcile_shows_reality_when_manifest_out_of_date`: already sets `stub.is_downloaded.return_value = True` — no change needed (this test already anticipated the fix).
- `test_expose_after_reconcile_survives_stale_state`: `stub.size_of.return_value = 22 * 1024**3` (no `is_downloaded` set). Add `stub.is_downloaded.return_value = True`.
- `test_apply_merges_reconciled_state_into_manifest`: `fake_size_of` returns `22 * 1024**3` for `ollama/o35`, `None` for `ollama/q8`. Add `stub.is_downloaded.side_effect = lambda v: v["id"] == "ollama/o35"`.
- `test_escape_with_pending_shows_dialog_and_apply`: uses `original_get`/`fake_get` dispatch with no `size_of`/`is_downloaded` set on `stub` at all (the model starts not-ready by default, which is what the test wants) — no change needed, but confirm `MagicMock()`'s default truthy return doesn't break it: `stub.is_downloaded.return_value = False` (a `MagicMock()` call returns a truthy `MagicMock` by default, which would wrongly make this model "ready" from the start) — add this line explicitly.
- `test_family_screen_reconciles_size_from_provider`: `stub.size_of.return_value = 22 * 1024**3`. Add `stub.is_downloaded.return_value = True`.
- `test_family_screen_reconciles_on_resume_after_apply`: `fake_size_of` distinguishes `ollama/o35` (present) / `ollama/o70` (present) initially, then a `post_delete_size` where only `o70` remains. Add matching `is_downloaded.side_effect` functions alongside each `size_of.side_effect` assignment (same id-based predicate, before and after the simulated delete).
- `test_enter_on_model_row_opens_edit_dialog`, `test_enter_on_provider_row_does_not_open_edit_dialog`, `test_model_screen_toggle_download_queues_variant`, `test_model_screen_discard_restores_fetch_dataclass`, `test_model_screen_delete_only_for_downloaded`: each sets `stub.size_of.return_value = None` with no `is_downloaded` — add `stub.is_downloaded.return_value = False` next to each (any bare `MagicMock()` provider stub anywhere in this file that never explicitly sets `is_downloaded` must get an explicit `False` default, since an unconfigured `MagicMock` call returns a truthy value and would silently flip these fixtures to "ready").

- [x] **Step 8: Run the full reconcile-affected test files**

Run: `uv run pytest tests/screens/test_models.py tests/screens/test_app_navigation.py -v`
Expected: all PASS.

- [x] **Step 9: Commit**

```bash
git add src/modelman/screens/families.py src/modelman/screens/models.py tests/screens/test_models.py tests/screens/test_app_navigation.py
git commit -m "fix(reconcile): use is_downloaded() as the ready signal, not size_of() - completes plan item #2"
```

---

## Task 3: Native providers via `sync_agent_providers()`

**Files:**
- Modify: `src/modelman/registry.py` (add `_default_wt_config_path`, `sync_agent_providers`)
- Modify: `src/modelman/sync.py:224-235` (`sync()`, call `sync_agent_providers` and merge into `providers_added`)
- Modify: `src/modelman/screens/families.py:83-102` (`_load_from_disk`, call `sync_agent_providers`)
- Test: `tests/test_registry.py`, `tests/test_sync.py` (if it exists — check `find tests -iname "test_sync*"`; if absent, add the sync-level test to `tests/test_registry.py` instead and note the CLI-level coverage gap is out of scope)

**Interfaces:**
- Produces: `sync_agent_providers(registry: Registry, wt_config_path: Path | None = None) -> list[str]` (ids of newly-added native providers). `_default_wt_config_path() -> Path` (private helper, `MODELMAN_WT_DIR` override else `~/.config/agent-wt`, joined with `config.toml`).
- Consumes: `ProviderEntry`, `AuthConfig` from `registry.py` (existing).

- [x] **Step 1: Write the failing test**

Add to `tests/test_registry.py`:

```python
def test_sync_agent_providers_adds_missing_agents(tmp_path, monkeypatch):
    wt_config = tmp_path / "config.toml"
    wt_config.write_text(
        "[[agents]]\n"
        'name = "claude"\n'
        'supported_providers = ["claude", "ollama"]\n'
        "\n"
        "[[agents]]\n"
        'name = "codex"\n'
        'supported_providers = ["codex"]\n'
    )
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))]
    )
    added = sync_agent_providers(registry, wt_config_path=wt_config)

    assert added == ["claude", "codex"]
    claude = registry.provider("claude")
    assert claude.auth.type == "native"
    assert claude.location == "cloud"
    assert claude.name == "Claude"
    registry.provider("codex")  # does not raise


def test_sync_agent_providers_is_idempotent(tmp_path):
    wt_config = tmp_path / "config.toml"
    wt_config.write_text('[[agents]]\nname = "claude"\n')
    registry = Registry()

    first = sync_agent_providers(registry, wt_config_path=wt_config)
    second = sync_agent_providers(registry, wt_config_path=wt_config)

    assert first == ["claude"]
    assert second == []
    assert len([p for p in registry.providers if p.id == "claude"]) == 1


def test_sync_agent_providers_missing_config_is_a_noop(tmp_path):
    registry = Registry()
    added = sync_agent_providers(registry, wt_config_path=tmp_path / "nonexistent.toml")
    assert added == []
    assert registry.providers == []


def test_default_wt_config_path_honors_override(monkeypatch):
    monkeypatch.setenv("MODELMAN_WT_DIR", "/custom/wt")
    assert _default_wt_config_path() == Path("/custom/wt/config.toml")


def test_default_wt_config_path_defaults_to_home(monkeypatch):
    monkeypatch.delenv("MODELMAN_WT_DIR", raising=False)
    assert _default_wt_config_path() == Path.home() / ".config" / "agent-wt" / "config.toml"
```

Add the two new names (`sync_agent_providers`, `_default_wt_config_path`) to the file's existing `from modelman.registry import (...)` block.

- [x] **Step 2: Run to verify it fails**

Run: `uv run pytest tests/test_registry.py -k sync_agent_providers -v`
Expected: FAIL with `ImportError: cannot import name 'sync_agent_providers'`.

- [x] **Step 3: Implement in `registry.py`**

Add near `default_provider_entry` (after line 199):

```python
def _default_wt_config_path() -> Path:
    """agent-worktree's config.toml, whose `[[agents]]` list names the
    native-provider agents (claude, codex, ...). Precedence: MODELMAN_WT_DIR
    > ~/.config/agent-wt, matching the usage subsystem's existing
    MODELMAN_WT_DIR convention (usage/wt_state.py)."""
    override = os.environ.get("MODELMAN_WT_DIR")
    base = Path(override).expanduser() if override else Path.home() / ".config" / "agent-wt"
    return base / "config.toml"


def sync_agent_providers(registry: Registry, wt_config_path: Path | None = None) -> list[str]:
    """Register every agent-worktree agent name missing from
    `registry.providers` as a native provider (auth.type="native",
    location="cloud"). Mutates `registry` in place; returns the ids added.

    A missing or unreadable wt config is not fatal — returns [] — matching
    migrate.py's existing tolerance for an absent agent-worktree install.
    """
    path = wt_config_path if wt_config_path is not None else _default_wt_config_path()
    if not path.exists():
        return []
    with open(path, "rb") as f:
        raw = tomllib.load(f)
    existing = {p.id for p in registry.providers}
    added: list[str] = []
    for agent in raw.get("agents", []):
        name = agent.get("name")
        if not name or name in existing:
            continue
        registry.providers.append(
            ProviderEntry(
                id=name,
                name=name.title(),
                location="cloud",
                auth=AuthConfig(type="native"),
            )
        )
        existing.add(name)
        added.append(name)
    return added
```

- [x] **Step 4: Run the new tests to verify they pass**

Run: `uv run pytest tests/test_registry.py -k sync_agent_providers -v`
Expected: PASS.

- [x] **Step 5: Wire into `sync.py`'s `sync()`**

In `src/modelman/sync.py`, import `sync_agent_providers` alongside the existing `registry` imports, and in `sync()`:

```python
def sync(
    registry: Registry,
    state: StateStore,
    runner: _Runner | None = None,
) -> SyncResult:
    """Reconcile configured ollama and model-dir models against their providers."""
    providers_added = _ensure_provider_entries(registry)
    providers_added += sync_agent_providers(registry)
    downloaded = _ollama_downloaded(registry, list_ollama(runner))
    downloaded.update(list_modeldir(registry, _modeldir_providers(registry)))
    result = reconcile(registry, state, downloaded)
    result.providers_added = providers_added
    return result
```

`tests/test_sync.py` already exists and covers `sync()` end-to-end; add the test there. Its top-level import (`from modelman.registry import Fetch, ModelEntry, ProviderEntry, Registry`) is missing `AuthConfig` — add it to that import line, since the test below constructs an `AuthConfig` directly:

```python
def test_sync_registers_agent_providers(tmp_path, monkeypatch):
    # _default_wt_config_path() joins <MODELMAN_WT_DIR>/config.toml exactly
    # — the fixture file must be named "config.toml", not anything else.
    wt_config = tmp_path / "config.toml"
    wt_config.write_text('[[agents]]\nname = "claude"\n')
    monkeypatch.setenv("MODELMAN_WT_DIR", str(tmp_path))
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))]
    )
    state = StateStore()

    def fake_runner(args, **kwargs):
        from unittest.mock import MagicMock

        result = MagicMock()
        result.returncode = 0
        result.stdout = "NAME    ID    SIZE    MODIFIED\n"
        return result

    result = sync(registry, state, runner=fake_runner)

    assert "claude" in result.providers_added
    registry.provider("claude")  # does not raise
```

- [x] **Step 6: Wire into `FamilyScreen._load_from_disk`**

In `src/modelman/screens/families.py`, `_load_from_disk` (around line 93-101), after `self.state = load_state(self.state_path)`, add:

```python
        from ..registry import sync_agent_providers

        sync_agent_providers(self.registry)
```

(In-memory only — no `save_registry` call here. The synced entries persist opportunistically the next time anything saves the registry: add/edit family, or a `ModelScreen` apply.)

- [x] **Step 7: Run the full affected test set**

Run: `uv run pytest tests/test_registry.py tests/screens/test_app_navigation.py -v`
Expected: all PASS.

- [x] **Step 8: Commit**

```bash
git add src/modelman/registry.py src/modelman/sync.py src/modelman/screens/families.py tests/test_registry.py
git commit -m "feat(registry): sync native providers from agent-worktree's agent list - completes plan item #3"
```

---

## Task 4: Family delete always blocked when models exist; READY column

**Files:**
- Modify: `src/modelman/screens/families.py:76` (column header), `src/modelman/screens/families.py:247-341` (`action_delete_family` and its confirm handlers)
- Modify: `tests/screens/test_app_navigation.py` (family-delete tests — see below)

**Interfaces:**
- Consumes: nothing new (uses existing `models_by_family`, `ConfirmModal`).
- Produces: no new public interface — `action_delete_family`'s behavior change is internal.

- [x] **Step 1: Write the failing test**

Add to `tests/screens/test_app_navigation.py`:

```python
@pytest.mark.asyncio
async def test_delete_family_blocked_with_undownloaded_models_no_override(tmp_path, monkeypatch):
    """A family with model definitions but nothing downloaded must be
    blocked outright now — no confirm-anyway override exists any more."""
    q4 = ModelEntry(id="ollama/q4", family="ornith", provider_id="ollama", model_name="ornith:q4")
    reg_path, _state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[q4])

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        # There is no confirm dialog to answer any more — pressing y must
        # do nothing, since the block is informational-only.
        await pilot.press("y")
        await pilot.pause()

    from modelman.registry import load_registry

    assert len(load_registry(reg_path).models_by_family("ornith")) == 1
```

- [x] **Step 2: Run it to verify it fails**

Run: `uv run pytest tests/screens/test_app_navigation.py -k delete_family_blocked_with_undownloaded_models_no_override -v`
Expected: FAIL — today, pressing `y` on the "delete anyway?" confirm removes the model.

- [x] **Step 3: Rewrite `action_delete_family`**

In `src/modelman/screens/families.py`, replace `action_delete_family` (lines 247-288) with:

```python
    def action_delete_family(self) -> None:
        table = self.query_one(DataTable)
        if table.row_count == 0:
            return
        row_key = list(table.rows.keys())[table.cursor_row]
        family_name = str(row_key.value)
        models = self.registry.models_by_family(family_name)
        variants_count = len(models)

        # Any model — ready or not — blocks delete outright. No
        # confirm-anyway override: the user must remove or move the
        # models first.
        if variants_count > 0:
            self.app.push_screen(
                ConfirmModal(
                    f"Family '{family_name}' has {variants_count} model"
                    f"{'s' if variants_count != 1 else ''}. Remove or move "
                    f"them before deleting this family."
                ),
                self._on_blocked_confirm,
            )
            return
        self.app.push_screen(
            ConfirmModal(f"Family '{family_name}' is empty. Delete?"),
            self._on_delete_confirm,
        )
```

Delete `_on_delete_family_with_variants` (lines 295-298) — it's now dead code (no caller).

Also delete the `downloaded_count` computation that preceded the old two-tier check (it's no longer read anywhere in this method).

- [x] **Step 4: Rename the DOWNLOADED column to READY**

In `src/modelman/screens/families.py`, `on_mount` (line 76):

```python
        table.add_columns("FAMILY", "DISPLAY", "VARIANTS", "READY", "SIZE")
```

- [x] **Step 5: Update the existing family-delete tests**

In `tests/screens/test_app_navigation.py`:

- `test_delete_family_blocked_when_downloaded` — unchanged behavior (still blocked); no edit needed, but rename is cosmetic only if it references "DOWNLOADED" text (grep confirms it doesn't).
- `test_delete_family_blocked_when_variants_present_even_without_downloads` — delete this test; it's superseded by the new `test_delete_family_blocked_with_undownloaded_models_no_override` from Step 1 (same seed, opposite expectation — the old one expected a confirm dialog to exist).
- `test_delete_family_prompts_for_explicit_confirmation_with_variants` — **delete this test entirely**. The "confirm anyway, lose the definitions" path no longer exists.
- `test_delete_family_cancel_keeps_empty_family`, `test_delete_family_cancel_keyword_preserves_file`, `test_delete_family_when_empty`, `test_delete_family_removes_registry_entry`, `test_delete_family_blocked_when_downloaded` — unchanged, still pass as-is (all exercise the empty-family or downloaded-block paths, neither of which changed).
- `test_family_screen_reconciles_size_from_provider` — its comment `# Family, Display, Variants, Downloaded, Size` and index-based assertions (`row[3]`, `row[4]`) are unaffected by the header text rename; no code change needed, but update the comment to `# Family, Display, Variants, Ready, Size` for accuracy.

- [x] **Step 6: Run the full family-screen test set**

Run: `uv run pytest tests/screens/test_app_navigation.py -k "family" -v`
Expected: all PASS (with the two tests removed per Step 5).

- [x] **Step 7: Commit**

```bash
git add src/modelman/screens/families.py tests/screens/test_app_navigation.py
git commit -m "feat(families): always block delete when a family has models - completes plan item #4"
```

---

## Task 5: Model screen — single-pane layout

**Files:**
- Modify: `src/modelman/screens/models.py` (`compose`, `on_mount`, `reload`, delete `on_data_table_row_highlighted`, `_provider_list` default logic, `on_data_table_row_selected`, `action_select_row`)
- Modify: `tests/screens/test_app_navigation.py` (every test touching `#provider-table` / `selected_provider` / the two-table assumption — see enumerated list)
- Modify: `tests/screens/test_forms.py` (`test_add_model_dialog_inherits_selected_provider`)

**Interfaces:**
- Consumes: `_is_ready` from Task 2.
- Produces: single `DataTable#model-table` with columns `FAMILY, PROVIDER, MODEL, LOCATION, STATUS, EXPOSED, SIZE, PATH`, rows sorted by `(provider_id, model_name)`. `ModelScreen._last_provider_used: str | None` (new attribute — tracks the provider of the last add/edit this session, used as the Add dialog's default instead of `selected_provider`).

- [x] **Step 1: Write the failing layout test**

Add to `tests/screens/test_app_navigation.py`:

```python
@pytest.mark.asyncio
async def test_model_screen_is_single_table_sorted_by_provider_then_name(tmp_path, monkeypatch):
    b_model = ModelEntry(
        id="ollama/b", family="ornith", provider_id="ollama", model_name="b:tag"
    )
    a_model = ModelEntry(
        id="llamacpp/a", family="ornith", provider_id="llamacpp", model_name="a.gguf",
    )
    _seed_registry_and_state(
        tmp_path, monkeypatch, models=[b_model, a_model], providers=["ollama", "llamacpp"]
    )

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()

        tables = app.screen.query("DataTable")
        assert len(tables) == 1, "the two-pane provider/model split must be gone"

        mt = app.screen.query_one("#model-table", DataTable)
        rows = [mt.get_row_at(i) for i in range(mt.row_count)]
        # FAMILY, PROVIDER, MODEL, LOCATION, STATUS, EXPOSED, SIZE, PATH
        providers_then_names = [(r[1], r[2]) for r in rows]
        assert providers_then_names == [("llamacpp", "a.gguf"), ("ollama", "b:tag")]
        assert rows[0][0] == "ornith"  # FAMILY column constant per row
```

- [x] **Step 2: Run it to verify it fails**

Run: `uv run pytest tests/screens/test_app_navigation.py -k single_table_sorted -v`
Expected: FAIL — two `DataTable`s exist today.

- [x] **Step 3: Rewrite `compose`, `on_mount`, `reload`**

In `src/modelman/screens/models.py`, replace `compose` (lines 188-196):

```python
    def compose(self) -> ComposeResult:
        yield Header()
        yield DataTable(id="model-table", cursor_type="row")
        yield Static("Pending: ready 0 · delete 0", id="pending-bar")
        yield Footer()
```

Replace `on_mount` (lines 198-219), dropping every provider-table reference:

```python
    def on_mount(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        mt.add_columns(
            "FAMILY", "PROVIDER", "MODEL", "LOCATION", "STATUS", "EXPOSED", "SIZE", "PATH"
        )
        self.reload()
        self._refresh_pending_bar()
        mt.focus()
        if mt.row_count > 0:
            mt.cursor_coordinate = Coordinate(0, 0)
        self.run_worker(self._run_reconcile, exclusive=True, thread=True)
```

Replace `reload` (lines 270-290) and delete `on_data_table_row_highlighted` (lines 292-297) entirely — the single table needs no provider switch:

```python
    def reload(self) -> None:
        self._load_models()

    def _load_models(self) -> None:
        from ..providers.registry import ProviderRegistry

        mt = self.query_one("#model-table", DataTable)
        mt.clear()
        models = sorted(
            self.registry.models_by_family(self.family),
            key=lambda m: (m.provider_id, m.model_name),
        )
        for m in models:
            rec = self.reconciled.get(m.id)
            if rec is not None:
                ready = bool(rec.get("ready"))
                size_str = _human_size(rec.get("size")) if ready else "—"
                path = rec.get("local_path") or (self.state.get(m.id).disk_path or "—")
            else:
                state_entry = self.state.get(m.id)
                ready = state_entry.ready
                size_str = "—"
                path = state_entry.disk_path or "—"
                if ready:
                    try:
                        entry = self.registry.provider(m.provider_id)
                        prov = ProviderRegistry.get(m.provider_id, provider_config(entry))
                        size_str = _human_size(prov.size_of(_model_entry_to_variant(m)))
                    except Exception:
                        pass
            if m.id in self.queued_deletes:
                status = "[red]✗[/red]"
            elif m.id in self.queued_ready:
                status = "[yellow]↓[/yellow]" if self.queued_ready[m.id] else "[yellow]↑[/yellow]"
            elif m.id in self.queued_moves:
                status = "[magenta]→[/magenta]"
            elif ready:
                status = "[green]✓[/green]"
            else:
                status = "[dim]○[/dim]"
            exposed = self.state.get(m.id).litellm_exposed
            if m.id in self.queued_exposes:
                exposed = self.queued_exposes[m.id]
            exposed_str = "L" if exposed else "–"
            mt.add_row(
                m.family,
                m.provider_id,
                m.model_name,
                m.location or "—",
                status,
                exposed_str,
                size_str,
                path,
                key=m.id,
            )
```

(`self.queued_ready` replaces `self.queued_downloads` — introduced fully in Task 6; this task only needs the attribute to exist as an empty dict so `reload()` doesn't crash. In `__init__`, rename `self.queued_downloads: dict[str, VariantSpec] = {}` to `self.queued_ready: dict[str, bool] = {}` now — Task 6 fills in the toggle logic that populates it.)

- [x] **Step 4: Update `_provider_list`'s default and add `_last_provider_used`**

In `__init__` (near line 156-160), remove `self.selected_provider` entirely and add:

```python
        # Provider of the last model added or edited this session; used to
        # default the Add dialog's provider dropdown now that there's no
        # provider pane to inherit a selection from.
        self._last_provider_used: str | None = None
```

In `action_add_model` (around line 413-429), change the default-provider line:

```python
        default_provider = (
            self._last_provider_used if self._last_provider_used in providers else None
        )
```

**`_on_add_model` and `_on_edit_model` both reference `self.queued_downloads`, which no longer exists after this step's `__init__` rename — leaving them untouched crashes with `AttributeError` on every add/edit/move, not just ready-toggle tests.** Replace both in full:

```python
    def _on_add_model(self, result) -> None:
        if result is None:
            return
        variant = result.spec
        if any(m.id == variant["id"] for m in self.registry.models):
            self.app.notify("Model ID already exists")
            return
        entry = _variant_to_model_entry(variant, family=result.family, registry=self.registry)
        self.registry.models.append(entry)
        self._added_ids.add(variant["id"])
        self.queued_ready[variant["id"]] = True
        self._last_provider_used = variant["provider"]
        self.reload()
        self._refresh_pending_bar()
```

```python
    def _on_edit_model(self, result) -> None:
        if result is None:
            return
        updated = result.spec
        new_entry = _variant_to_model_entry(updated, family=self.family, registry=self.registry)
        for i, m in enumerate(self.registry.models):
            if m.id == updated["id"]:
                self.registry.models[i] = new_entry
                break
        if result.family != self.family:
            self.queued_moves[updated["id"]] = result.family
        else:
            self.queued_moves.pop(updated["id"], None)
        self._last_provider_used = updated["provider"]
        self.reload()
        self._refresh_pending_bar()
```

(`_on_edit_model` drops the old `if updated["id"] in self.queued_downloads: self.queued_downloads[updated["id"]] = updated` block entirely — `queued_ready` stores a bool target, not a spec, and Task 7 rederives the spec fresh from `self.registry.models` at apply time, so there is nothing to refresh here.)

- [x] **Step 5: Simplify `on_data_table_row_selected` / `action_select_row`**

Replace both (lines 500-550) with:

```python
    def on_data_table_row_selected(self, event: DataTable.RowSelected) -> None:
        self.action_edit_model()

    def action_select_row(self) -> None:
        """Screen-level Enter handler: always edits the row under the
        cursor now that there's only one table."""
        self.action_edit_model()
```

(Drop the `Binding("enter", ..., priority=True)` special-casing rationale in the docstring since there's no longer a second table whose focus it needs to out-prioritize — the binding itself can stay as a plain non-priority binding: change `Binding("enter", "select_row", "Edit", priority=True)` to `("enter", "select_row", "Edit")` in `BINDINGS`.)

- [x] **Step 6: Delete the provider-table-specific tests in `test_app_navigation.py`**

Delete entirely (all assume the two-pane split, now gone):
- `test_model_screen_two_pane_lists_providers_and_models`
- `test_model_screen_shows_all_providers_for_empty_family`
- `test_model_screen_provider_table_count_zero_for_empty`
- `test_model_screen_starts_with_cursor_on_first_provider`
- `test_enter_on_provider_row_does_not_open_edit_dialog`

- [x] **Step 7: Rewrite the tests that drove the provider pane to reach a specific provider's models**

`test_model_screen_discard_restores_fetch_dataclass` used `await pilot.press("down")` on the provider table to switch from ollama to llamacpp, then asserted `ms.selected_provider == "llamacpp"`. With one table sorted by `(provider, name)`, the llamacpp row is simply wherever it sorts — replace the provider-switch with a direct cursor move to that row:

```python
@pytest.mark.asyncio
async def test_model_screen_discard_restores_fetch_dataclass(tmp_path, monkeypatch):
    from unittest.mock import MagicMock

    from textual.widgets import Button, DataTable

    from modelman.providers import registry as prov_registry
    from modelman.registry import Fetch

    a = ModelEntry(
        id="llamacpp/ornith-q4",
        family="ornith",
        provider_id="llamacpp",
        model_name="ornith-q4.gguf",
        fetch=Fetch(
            repo="ornith-ai/Ornith-1.5-35B-GGUF",
            files=["ornith-q4.gguf"],
            quantizations=["Q4_K_M"],
        ),
    )
    ms, _reg, _state = _make_screen(tmp_path, monkeypatch, entries=[a])

    stub = MagicMock()
    stub.name = "llamacpp"
    stub.size_of.return_value = None
    stub.is_downloaded.return_value = False
    monkeypatch.setattr(
        prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub)
    )

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        mt = ms.query_one("#model-table", DataTable)
        mt.cursor_coordinate = (0, 0)  # only one model in this family
        await pilot.press("x")
        await pilot.pause()
        assert ms.queued_ready == {"llamacpp/ornith-q4": True}
        await pilot.press("escape")
        await pilot.pause()
        for btn in app.screen.query(Button):
            if btn.id == "discard":
                btn.press()
                break
        await pilot.pause()

    restored = next(m for m in ms.registry.models if m.id == "llamacpp/ornith-q4")
    assert isinstance(restored.fetch, Fetch)
    assert restored.fetch.repo == "ornith-ai/Ornith-1.5-35B-GGUF"
    assert restored.fetch.quantizations == ["Q4_K_M"]
    assert ms.queued_ready == {}
```

`test_enter_on_model_row_opens_edit_dialog` pressed `tab` to move from the (default-focused) provider table into the model table before `enter`; with one table already focused on mount, drop the `await pilot.press("tab")` line — the rest of the test is unchanged.

- [x] **Step 8: Rewrite `test_add_model_dialog_inherits_selected_provider` in `tests/screens/test_forms.py`**

Replace it with a version that drives the cursor to the llamacpp row directly instead of the (now-gone) provider table, and asserts against `_last_provider_used` semantics — add a model as llamacpp first (so `_last_provider_used` is set), then confirm the next Add defaults to it:

```python
@pytest.mark.asyncio
async def test_add_model_dialog_inherits_last_used_provider(tmp_path, monkeypatch):
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[
            ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none")),
            ProviderEntry(id="llamacpp", name="L", auth=AuthConfig(type="none")),
        ],
        models=[],
    )
    save_registry(reg, reg_path)
    save_state(StateStore(), state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    from modelman.providers import registry

    stub = MagicMock()
    stub.size_of.return_value = None
    stub.is_downloaded.return_value = False
    stub.name = "ollama"
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()

        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)

        # Add one llamacpp model first (sets _last_provider_used).
        app.screen._last_provider_used = "llamacpp"

        captured: list[ModelForm] = []
        original_push = app.push_screen

        def tracking_push(screen, *args, **kwargs):
            if isinstance(screen, ModelForm):
                captured.append(screen)
            return original_push(screen, *args, **kwargs)

        monkeypatch.setattr(app, "push_screen", tracking_push)
        await pilot.press("a")
        await pilot.pause()

        assert captured, "ModelForm was not pushed"
        assert captured[0]._default_provider == "llamacpp"
```

- [x] **Step 9: Run the affected test files**

Run: `uv run pytest tests/screens/test_app_navigation.py tests/screens/test_forms.py tests/screens/test_models.py -v`
Expected: add/edit/move-related tests now pass cleanly (the `_on_add_model`/`_on_edit_model` fix above prevents the `AttributeError` that would otherwise break them). Remaining failures should be confined to tests that assert on `ms.queued_downloads`/`app.screen.queued_downloads` directly (that attribute no longer exists — Task 6 renames every remaining reference) and the `EXPOSED`-column-index test (`len(mt.columns) == 5`, now 8) — both are Task 6's job. Any failure outside those two categories must be fixed now.

- [x] **Step 10: Commit**

```bash
git add src/modelman/screens/models.py tests/screens/test_app_navigation.py tests/screens/test_forms.py
git commit -m "feat(models): collapse the model screen to one sorted table - completes plan item #5"
```

---

## Task 6: Ready toggle (`queued_ready`), delete/expose gate simplification

**Files:**
- Modify: `src/modelman/screens/models.py` (`action_toggle_download`→`action_toggle_ready`, `action_toggle_expose`, `_refresh_pending_bar`, `action_back`/`_on_exit_confirm` wording)
- Modify: `src/modelman/screens/forms.py` (`ConfirmExitDialog` — ready-on/ready-off split)
- Modify: `tests/screens/test_app_navigation.py` (every `queued_downloads` reference — enumerated below)

**Interfaces:**
- Consumes: `provider_policy` from `litellm.py` (existing import), `_is_ready` from Task 2.
- Produces: `ModelScreen.queued_ready: dict[str, bool]` (target ready state per model id — already declared as an empty dict in Task 5's `__init__` edit; this task fills in the toggle logic and the pending-bar/exit-dialog wiring). `action_toggle_ready` (renamed from `action_toggle_download`).

- [x] **Step 1: Write the failing ready-toggle tests**

Add to `tests/screens/test_app_navigation.py`:

```python
@pytest.mark.asyncio
async def test_ready_toggle_on_queues_download_for_reconcilable_provider(tmp_path, monkeypatch):
    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
    _seed_registry_and_state(tmp_path, monkeypatch, models=[o35])

    from unittest.mock import MagicMock

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = None
    stub.is_downloaded.return_value = False
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.press("x")
        await pilot.pause()
        assert app.screen.queued_ready == {"ollama/o35": True}


@pytest.mark.asyncio
async def test_ready_toggle_off_queues_clear_without_removing_registry_entry(tmp_path, monkeypatch):
    """Toggling ready off on an already-ready model queues a clear (rm)
    but must NOT remove the ModelEntry — only the 'd' action does that."""
    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
    _seed_registry_and_state(
        tmp_path, monkeypatch, models=[o35], downloaded={"ollama/o35": "/tmp/o35"}
    )

    from unittest.mock import MagicMock

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = 22 * 1024**3
    stub.is_downloaded.return_value = True
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.press("x")
        await pilot.pause()
        assert app.screen.queued_ready == {"ollama/o35": False}
        assert "ollama/o35" in [m.id for m in app.screen.registry.models]


@pytest.mark.asyncio
async def test_ready_toggle_on_flag_only_provider_sets_no_provider_call(tmp_path, monkeypatch):
    """A native/flag-only provider has no ProviderRegistry class; toggling
    ready must still queue, with no reconcile-derived readiness involved."""
    native_model = ModelEntry(
        id="claude/native", family="agents", provider_id="claude", model_name="native"
    )
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[
            ProviderEntry(id="claude", name="Claude", location="cloud", auth=AuthConfig(type="native")),
        ],
        models=[native_model],
    )
    save_registry(reg, reg_path)
    save_state(StateStore(), state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.press("x")
        await pilot.pause()
        assert app.screen.queued_ready == {"claude/native": True}
```

- [x] **Step 2: Run to verify they fail**

Run: `uv run pytest tests/screens/test_app_navigation.py -k "ready_toggle" -v`
Expected: FAIL — `action_toggle_ready` doesn't exist yet (the `x` binding still calls `action_toggle_download`).

- [x] **Step 3: Rewrite `action_toggle_download` → `action_toggle_ready`**

In `src/modelman/screens/models.py`, replace `action_toggle_download` (lines 359-377) and update the `BINDINGS` entry (`("x", "toggle_download", "Toggle download")` → `("x", "toggle_ready", "Toggle ready")`):

```python
    def action_toggle_ready(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        mid = str(row_key.value)
        entry = next((m for m in self.registry.models if m.id == mid), None)
        if entry is None:
            return
        currently_ready = self._is_ready(mid)
        target = not currently_ready
        if mid in self.queued_ready:
            # Toggling again cancels the queued flip.
            self.queued_ready.pop(mid)
        else:
            self.queued_ready[mid] = target
        self._last_provider_used = entry.provider_id
        self._refresh_pending_bar()
        self.reload()
```

(Note: unlike the old download-only toggle, this always flips relative to current readiness — pressing `x` on a ready model queues ready-off, on a not-ready model queues ready-on. There is no separate "already ready, do nothing" early return; the no-op guard from the spec is naturally satisfied because pressing `x` twice in a row cancels the queued flip via the `pop`, landing back at "nothing queued.")

- [x] **Step 4: Simplify `action_toggle_expose`**

Replace (lines 379-397):

```python
    def action_toggle_expose(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        mid = str(row_key.value)
        entry = next((m for m in self.registry.models if m.id == mid), None)
        if entry is None:
            return
        if not self._is_ready(mid):
            self.app.notify("Model must be ready before exposing")
            return
        if provider_policy(entry.provider_id) is None:
            self.app.notify("Provider has no LiteLLM mapping — cannot expose")
            return
        current = self.state.get(mid).litellm_exposed
        if mid in self.queued_exposes:
            current = self.queued_exposes[mid]
        self.queued_exposes[mid] = not current
        self._refresh_pending_bar()
        self.reload()
```

Add `provider_policy` to the existing `from ..litellm import default_litellm_config_path, is_cloud` line — change it to `from ..litellm import default_litellm_config_path, provider_policy` (drop `is_cloud`, now unused in this file; confirm with `grep -n is_cloud src/modelman/screens/models.py` that nothing else references it before removing the import).

- [x] **Step 5: Update `_refresh_pending_bar` wording**

Replace (lines 352-357):

```python
    def _refresh_pending_bar(self) -> None:
        bar = self.query_one("#pending-bar", Static)
        bar.update(
            f"Pending: ready {len(self.queued_ready)} · delete {len(self.queued_deletes)}"
            f" · move {len(self.queued_moves)} · expose {len(self.queued_exposes)}"
        )
```

- [x] **Step 6: Update `action_back`'s exit-condition check**

In `action_back` (lines 569-588), change `not self.queued_downloads` to `not self.queued_ready`, and the `ConfirmExitDialog(downloads=list(self.queued_downloads.values()), ...)` call — since `queued_ready` values are now bools (target state) rather than `VariantSpec`s, pass the dict's items instead:

```python
        if (
            not self.queued_ready
            and not self.queued_deletes
            and not self.queued_moves
            and not self.queued_exposes
        ):
            self.app.pop_screen()
            return
        from .forms import ConfirmExitDialog

        self.app.push_screen(
            ConfirmExitDialog(
                ready=list(self.queued_ready.items()),
                deletes=list(self.queued_deletes.values()),
                exposes=list(self.queued_exposes.items()),
                moves=list(self.queued_moves.items()),
            ),
            self._on_exit_confirm,
        )
```

- [x] **Step 7: Update `_on_exit_confirm`'s discard branch**

Replace `self.queued_downloads.clear()` with `self.queued_ready.clear()` in both the `discard` branch of `_on_exit_confirm` (around line 596) and at the end of `_run_apply` (around line 676-680).

- [x] **Step 8: Update `ConfirmExitDialog` in `forms.py`**

In `src/modelman/screens/forms.py`, change the constructor and `compose` (lines 413-445):

```python
    def __init__(
        self,
        ready: list[tuple[str, bool]],
        deletes: list,
        exposes: list[tuple[str, bool]] | None = None,
        moves: list[tuple[str, str]] | None = None,
    ) -> None:
        super().__init__()
        self._ready = ready
        self._deletes = deletes
        self._exposes = exposes or []
        self._moves = moves or []

    def compose(self) -> ComposeResult:
        ready_on = [mid for mid, target in self._ready if target]
        ready_off = [mid for mid, target in self._ready if not target]
        with Vertical():
            yield Label(
                f"Pending: ready {len(self._ready)} · delete {len(self._deletes)}"
                f" · move {len(self._moves)} · expose {len(self._exposes)}"
            )
            for mid in ready_on:
                yield Label(f"  ↓ {mid}")
            for mid in ready_off:
                yield Label(f"  ↑ {mid}")
            for v in self._deletes:
                yield Label(f"  × {v['id']} ({v['provider']})")
            for model_id, target in self._moves:
                yield Label(f"  → {model_id} → {target}")
            for model_id, exposed in self._exposes:
                mark = "L" if exposed else "–"
                yield Label(f"  {mark} {model_id}")
            yield Label("Apply, cancel, or discard these changes?")
            with Horizontal():
                yield Button("Cancel", id="cancel", variant="default")
                yield Button("Discard", id="discard", variant="warning")
                yield Button("Apply", id="apply", variant="primary")
```

- [x] **Step 9: Mechanically rename remaining `queued_downloads` references in `tests/screens/test_app_navigation.py`**

Run:

```bash
grep -n "queued_downloads" tests/screens/test_app_navigation.py
```

For each match, the transform depends on context:
- `assert "id" in app.screen.queued_downloads` → the model was queued for ready-on, so replace with `assert app.screen.queued_ready.get("id") is True` (list the affected tests: `test_toggle_download_queues_variant`, `test_model_screen_toggle_download_queues_variant`).
- `ms.queued_downloads == {}` → `ms.queued_ready == {}` (`test_model_screen_discard_restores_fetch_dataclass` already rewritten in Task 5 Step 7; `test_discard_combined_move_add_and_download`, `test_discard_pending_exits_without_applying`).
- `ms.queued_downloads["ollama/newcomer"] = {...}` (a `VariantSpec` dict) in `test_discard_combined_move_add_and_download` → `ms.queued_ready["ollama/newcomer"] = True` (the value is now a bool target, not a spec — the model's spec already lives in `ms.registry.models` from the preceding `_on_add_model` call).
- `stub.download.return_value = ...` / `stub.download.assert_called()` in `test_escape_with_pending_shows_dialog_and_apply` — unaffected (these exercise `PendingChanges`/provider dispatch, covered by Task 7, not `ModelScreen` state directly); leave as-is for now, re-verify after Task 7.

Rename `test_toggle_download_queues_variant` and `test_model_screen_toggle_download_queues_variant` to `test_toggle_ready_queues_variant` and `test_model_screen_toggle_ready_queues_variant` respectively (function name only; body per the bullet above).

- [x] **Step 10: Update the EXPOSED-column-index test**

`test_l_key_queues_expose_and_column_renders` asserts `len(mt.columns) == 5` (NAME, STATUS, SIZE, PATH, EXPOSED). With the new 8-column layout, change to `assert len(mt.columns) == 8`.

- [x] **Step 11: Run the full model-screen test set**

Run: `uv run pytest tests/screens/test_app_navigation.py tests/screens/test_models.py -v`
Expected: remaining failures should be confined to `queue.py`/`PendingChanges`-level assertions (Task 7's job — e.g. `stub.download.assert_called()` after a full apply cycle, which still works today since `queued_ready` items still flow into a `downloads`-shaped list until Task 7 renames that too). If any `ModelScreen`-level assertion still fails, fix it now.

- [x] **Step 12: Commit**

```bash
git add src/modelman/screens/models.py src/modelman/screens/forms.py tests/screens/test_app_navigation.py
git commit -m "feat(models): replace download toggle with a ready toggle - completes plan item #6"
```

---

## Task 7: `queue.py` — `PendingChanges.ready` replaces `.downloads`

**Files:**
- Modify: `src/modelman/queue.py` (dataclass field, `apply()`'s download loop → ready loop, cascade-unexpose on ready-off)
- Modify: `src/modelman/screens/models.py:_run_apply` (build `ready=[...]` triples instead of `downloads=[...]` pairs)
- Modify: `src/modelman/screens/status.py` (`_handle_event`'s tag-prefix guard and elif chain — add `ready:*`)
- Modify: `tests/test_queue.py` (every `downloads=[...]` construction and `download:*` event assertion tied to it)
- Test: new flag-only-provider apply tests

**Interfaces:**
- Produces: `PendingChanges.ready: list[tuple[str, VariantSpec, bool]]` (replaces `.downloads: list[tuple[str, VariantSpec]]`). Apply-time behavior per `(model_id, spec, target)`:
  - `target=True`, provider registered (`self.providers[spec["provider"]]` present) → `provider.download()`, emits `download:start/done/fail` (unchanged).
  - `target=True`, provider not in `self.providers` (flag-only) → `state.ready = True`, emits `ready:start/done`.
  - `target=False`, provider registered → provider delete/rm (reusing `_delete`), **does not** touch `self.registry.models`, emits `delete:start/done/fail`.
  - `target=False`, provider not in `self.providers` → `state.ready = False`, emits `ready:start/done`.
  - Either `target=False` branch: if `state.get(model_id).litellm_exposed`, force-queue `(model_id, False)` into `self.exposes` (same cascade rule the existing full-delete step already has).
- Consumes: `ModelState.ready` from Task 1, `_is_ready`/`queued_ready` semantics from Tasks 2/6.

- [x] **Step 1: Write the failing tests**

Add to `tests/test_queue.py`:

```python
def test_apply_ready_true_reconcilable_downloads(tmp_path):
    """target=True for a provider present in self.providers behaves
    exactly like today's download step."""
    reg, state, reg_path, state_path, providers, a, b = _setup_apply_test(tmp_path)
    providers["ollama"].download.return_value = str(tmp_path / "new-a")

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers=providers,
        ready=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"), True)],
    )
    pending.apply()

    assert state.models["ollama/a"].ready is True
    assert state.models["ollama/a"].disk_path == str(tmp_path / "new-a")
    providers["ollama"].download.assert_called_once()


def test_apply_ready_false_reconcilable_clears_without_removing_registry_entry(tmp_path):
    """target=False for a reconcilable, currently-ready model calls
    provider.delete() but must NOT remove the ModelEntry — only the
    full `deletes` list does that."""
    reg, state, reg_path, state_path, providers, a, b = _setup_apply_test(tmp_path)
    state.set("ollama/a", ModelState(ready=True, disk_path="/old/a"))
    save_state(state, state_path)

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers=providers,
        ready=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"), False)],
    )
    pending.apply()

    providers["ollama"].delete.assert_called_once()
    assert state.models["ollama/a"].ready is False
    reloaded = load_registry(reg_path)
    assert reloaded.model("ollama/a") is not None  # NOT removed


def test_apply_ready_true_flag_only_sets_flag_no_provider_call(tmp_path):
    """A provider with no entry in self.providers (flag-only: native or
    openrouter) just flips the state flag; no download call is made."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    native_model = ModelEntry(
        id="claude/native", family="f", provider_id="claude", model_name="native"
    )
    reg = Registry(
        providers=[
            ProviderEntry(id="claude", name="Claude", location="cloud", auth=AuthConfig(type="native"))
        ],
        models=[native_model],
    )
    save_registry(reg, reg_path)
    state = StateStore()

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers={},  # no Provider instance for "claude" — flag-only
        ready=[("claude/native", _variant(id="claude/native", provider="claude", name="native"), True)],
    )
    pending.apply()

    assert state.get("claude/native").ready is True
    assert pending.failures == []


def test_apply_ready_false_flag_only_clears_flag_and_cascades_unexpose(tmp_path):
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    native_model = ModelEntry(
        id="claude/native", family="f", provider_id="claude", model_name="native"
    )
    reg = Registry(
        providers=[
            ProviderEntry(id="claude", name="Claude", location="cloud", auth=AuthConfig(type="native"))
        ],
        models=[native_model],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("claude/native", ModelState(ready=True, litellm_exposed=True))

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers={},
        ready=[("claude/native", _variant(id="claude/native", provider="claude", name="native"), False)],
    )
    pending.apply()

    assert state.get("claude/native").ready is False
    # Cascade: was exposed, so an unexpose must have been queued and run.
    assert state.get("claude/native").litellm_exposed is False
```

Add `provider_config` isn't needed here since flag-only providers never construct a `Provider` instance.

- [x] **Step 2: Run to verify they fail**

Run: `uv run pytest tests/test_queue.py -k "apply_ready" -v`
Expected: FAIL — `PendingChanges.__init__() got an unexpected keyword argument 'ready'`.

- [x] **Step 3: Rewrite `PendingChanges`'s dataclass fields and `apply()`**

In `src/modelman/queue.py`, change the field (line 80):

```python
    # Each queued item carries (model_id, VariantSpec, target_ready). For a
    # provider present in `providers`, target=True downloads / target=False
    # clears (rm) without touching the registry entry. For a provider absent
    # from `providers` (flag-only: native or unmapped), either target just
    # flips state.ready — no provider call is made.
    ready: list[tuple[str, VariantSpec, bool]] = field(default_factory=list)
```

(keep `deletes` as-is — full-removal semantics are unchanged.)

Replace the early-return guard (line 140):

```python
        if not self.ready and not self.deletes and not self.exposes and not self.moves:
            emit("apply:done")
            return
```

Replace the download loop (lines 225-258) with a ready loop:

```python
        for model_id, variant, target in self.ready:
            if aborted():
                return
            assert variant["id"] == model_id, (
                f"variant id {variant['id']!r} != queued model_id {model_id!r}"
            )
            label = _label(variant)
            provider_id = variant["provider"]
            provider = self.providers.get(provider_id)
            if provider is None:
                # Flag-only provider (native or unmapped): no download/delete
                # mechanics exist. Just flip the flag.
                emit(f"ready:start|{model_id}|{label}")
                self.state.set(model_id, replace(self.state.get(model_id), ready=target))
                emit(f"ready:done|{model_id}|{label}")
            elif target:
                emit(f"download:start|{model_id}|{label}")
                try:
                    local_path = self._download(variant, on_progress)
                except DownloadCancelled:
                    emit(f"download:cancelled|{model_id}|{label}")
                    emit("apply:cancelled")
                    return
                except Exception as exc:  # noqa: BLE001
                    reason = _reason(exc)
                    self.failures.append(f"download {model_id}: {exc}")
                    emit(f"download:fail|{model_id}|{label}|{reason}")
                    continue
                self.state.set(
                    model_id,
                    replace(self.state.get(model_id), ready=True, disk_path=local_path),
                )
                try:
                    size = Path(local_path).stat().st_size
                except OSError:
                    size = 0
                if size > 0:
                    from .providers._progress import human_bytes

                    emit(f"download:done|{model_id}|{label}|{human_bytes(size)}")
                else:
                    emit(f"download:done|{model_id}|{label}")
            else:
                emit(f"delete:start|{model_id}|{label}")
                try:
                    self._delete(variant)
                except Exception as exc:  # noqa: BLE001
                    reason = _reason(exc)
                    self.failures.append(f"clear {model_id}: {exc}")
                    emit(f"delete:fail|{model_id}|{label}|{reason}")
                    continue
                # Unlike the full-delete step above, the ModelEntry stays in
                # the registry — only its ready state and on-disk artifact
                # are cleared.
                self.state.set(model_id, replace(self.state.get(model_id), ready=False))
                emit(f"delete:done|{model_id}|{label}")
            # Cascade: turning ready off (either branch) drops the model's
            # exposure — mirrors the full-delete step's was_exposed rule.
            if not target and self.state.get(model_id).litellm_exposed:
                self.exposes = [(mid, t) for mid, t in self.exposes if mid != model_id]
                self.exposes.append((model_id, False))
```

- [x] **Step 4: Rewrite the `downloads=[...]`-based tests in `tests/test_queue.py`**

Every test constructing `PendingChanges(..., downloads=[(mid, spec)])` needs `ready=[(mid, spec, True)]` instead. Run:

```bash
grep -n "downloads=\[" tests/test_queue.py
```

Apply the transform `downloads=[(mid, spec)]` → `ready=[(mid, spec, True)]` (and for the multi-item case, each tuple in the list gets the same `, True` appended) to: `test_apply_deletes_before_downloads`, `test_apply_collects_failures`, `test_apply_download_cancelled_is_not_a_failure`, `test_apply_download_fail_includes_reason_in_event`, `test_apply_save_fail_includes_reason_in_event`, `test_apply_download_done_includes_size_of_file`, `test_apply_download_done_omits_size_when_zero_or_unreadable`, `test_apply_writes_state_for_each_downloaded_model`, `test_apply_asserts_download_twin_keys_agree`.

For each, also update any assertion reading `.downloaded` on the resulting `ModelState` to `.ready` (per Task 1's rename — these should already be fixed by Task 1, but re-verify: `test_apply_writes_state_for_each_downloaded_model`'s two `assert reloaded_state.get(...).downloaded is True` lines must read `.ready is True`).

`test_apply_asserts_download_twin_keys_agree`'s assertion message references "download" generically (`with pytest.raises(AssertionError, match="wrong-id")`) — no wording change needed, but rename the test to `test_apply_asserts_ready_twin_keys_agree` for clarity and update its `downloads=[("wrong-id", ...)]` to `ready=[("wrong-id", ..., True)]`.

- [x] **Step 5: Update `ModelScreen._run_apply` to build `ready=[...]` triples**

In `src/modelman/screens/models.py`, `_run_apply` (lines 617-680), change the `providers` dict comprehension's source (was `self.queued_downloads.values()` for specs — now specs live in `self.registry.models`, keyed by the ids in `self.queued_ready`):

```python
        providers: dict[str, object] = {}
        specs_by_id = {
            m.id: _model_entry_to_variant(m) for m in self.registry.models if m.id in self.queued_ready
        }
        for spec in list(specs_by_id.values()) + list(self.queued_deletes.values()):
            try:
                entry = self.registry.provider(spec["provider"])
                providers[spec["provider"]] = ProviderRegistry.get(
                    spec["provider"], provider_config(entry)
                )
            except Exception:
                continue
```

And change the `PendingChanges(...)` construction's `downloads=` kwarg to:

```python
            ready=[(mid, specs_by_id[mid], target) for mid, target in self.queued_ready.items()],
```

(leave `deletes=[(mid, spec) for mid, spec in self.queued_deletes.items()]` unchanged. `_run_apply`'s end-of-method `self.queued_ready.clear()` was already fixed in Task 6 Step 7 — nothing left to rename here.)

Note: for a flag-only provider (`ProviderRegistry.get` raises inside the `try/except Exception: continue` above), `providers` simply won't have an entry for that provider id — this is exactly the signal `PendingChanges.apply()`'s new ready-loop uses (`self.providers.get(provider_id)` is `None`) to take the flag-only branch. No extra wiring needed.

- [x] **Step 6: Add `ready:*` tag handling to `StatusScreen`**

In `src/modelman/screens/status.py`, `_handle_event` (lines 100-107), add `ready:` to the prefix guard:

```python
        if (
            not tag.startswith("delete:")
            and not tag.startswith("download:")
            and not tag.startswith("ready:")
            and not tag.startswith("move:")
            and not tag.startswith("save:")
            and not tag.startswith("apply:")
        ):
            log.write(f"    [dim]{tag}[/dim]")
            return
```

Add two `elif` branches near the `download:*` handlers (after the `download:cancelled` branch, around line 167):

```python
        elif verb == "ready:start":
            log.write(f"· Marking {label} ready...")
        elif verb == "ready:done":
            log.write(f"  [green]✓[/green] Ready: {label}")
```

- [x] **Step 7: Run the full test suite**

Run: `uv run pytest -v 2>&1 | tail -60`
Expected: all PASS. Investigate and fix any remaining `queued_downloads`/`.downloads`/`.downloaded` stragglers this task's grep-based steps missed.

- [x] **Step 8: Commit**

```bash
git add src/modelman/queue.py src/modelman/screens/models.py src/modelman/screens/status.py tests/test_queue.py
git commit -m "feat(queue): replace downloads list with a ready list (download vs clear vs flag) - completes plan item #7"
```

---

## Task 8: `ModelForm` — provider dropdown, native/openrouter parsing, location select

**Files:**
- Modify: `src/modelman/screens/forms.py` (`parse_model`, `ModelForm.__init__`/`compose`/`_submit`)
- Modify: `tests/screens/test_forms.py` (every test using `#provider-label` / `_rendered_provider`)
- Modify: `tests/screens/test_parse_model.py` (new native/openrouter branches)

**Interfaces:**
- Consumes: `sync_agent_providers`-registered native `ProviderEntry`s (Task 3) via the caller-supplied `providers` list; `location` per-provider lock rules require the caller to pass provider *kind* info — since `ModelForm` today only receives a flat `providers: list[str]`, this task adds an optional `provider_kinds: dict[str, str] | None` parameter (`"native"`, `"cloud-only"`, `"local-only"`, or `"ollama"` — used only to drive the Location select's lock rules; defaults to treating every provider as `"ollama"`-like/editable when not supplied, so existing direct callers/tests that don't pass it keep today's behavior for the providers that don't care about location).
- Produces: `ModelForm` add-mode `#provider-select` (`Select`, replaces `#provider-label` `Label`), `#location-select` (`Select`, options `cloud`/`local`).

- [x] **Step 1: Write the failing native/openrouter `parse_model` tests**

Add to `tests/screens/test_parse_model.py` (read the file first to match its existing style — it's a plain unit-test file for `parse_model`, no Textual app needed):

```python
def test_parse_model_native_blank_defaults_to_native_sentinel():
    name, repo, filename = parse_model("claude", "")
    assert name == "native"
    assert repo is None
    assert filename is None


def test_parse_model_native_named_is_verbatim():
    name, repo, filename = parse_model("claude", "opus")
    assert name == "opus"
    assert repo is None
    assert filename is None


def test_parse_model_openrouter_plain_string_no_split():
    name, repo, filename = parse_model("openrouter", "anthropic/claude-opus")
    assert name == "anthropic/claude-opus"
    assert repo is None
    assert filename is None
```

Note: `parse_model`'s current signature dispatches on `provider == "ollama"` vs. "everything else is HF-shaped." This task adds explicit native/openrouter branches, so this test file's existing HF-provider tests (`llamacpp`, `omlx`) must keep passing unchanged — verify by running the full file after the implementation step.

- [x] **Step 2: Run to verify they fail**

Run: `uv run pytest tests/screens/test_parse_model.py -k "native or openrouter" -v`
Expected: FAIL — today, `parse_model("claude", "opus")` falls into the HF branch and raises `ValueError` (no `/` in "opus" → "must be 'org/repo'").

- [x] **Step 3: Add native/openrouter branches to `parse_model`**

`parse_model` needs to know which providers are native. Since it's a pure function today (no registry access), add an explicit `is_native: bool = False` parameter — callers (the two `ModelForm._submit` call sites this task adds) pass it based on the provider's `auth.type`. In `src/modelman/screens/forms.py`:

```python
def parse_model(
    provider: str, model: str, *, is_native: bool = False
) -> tuple[str | None, str | None, str | None]:
    """..." (existing docstring, plus:)

    Native providers (is_native=True): `model` is used verbatim as the
    model name; blank input defaults to the sentinel "native". No
    slash-splitting — `id` becomes f"{provider}/{model_name}" regardless
    of any '/' the user types.

    openrouter and any other provider that is neither ollama, an HF
    provider (llamacpp/omlx), nor native: `model` is stored whole as the
    model name (e.g. "anthropic/claude-opus") with no repo/files split.
    """
    model = model.strip()
    if is_native:
        return (model or "native", None, None)
    if provider == "ollama":
        if "/" in model:
            raise ValueError("ollama tags must not contain '/'")
        if not model:
            raise ValueError("ollama tag is required")
        return (model, None, None)
    if provider not in ("llamacpp", "omlx"):
        # openrouter and any other non-HF, non-native provider: plain
        # string, no repo/files split.
        if not model:
            raise ValueError(f"{provider} model is required")
        return (model, None, None)
    # HF providers (llamacpp / omlx)
    if not model:
        raise ValueError(f"{provider} model is required")
    parts = model.split("/")
    if len(parts) < 2:
        raise ValueError(f"{provider} model must be 'org/repo' (or 'org/repo/file')")
    if not parts[0]:
        raise ValueError("repo org must not be empty")
    repo_id = "/".join(parts[:2])
    filename = "/".join(parts[2:])  # empty string if len == 2
    return (model, repo_id, filename)
```

- [x] **Step 4: Run to verify the new tests pass and the existing file still passes**

Run: `uv run pytest tests/screens/test_parse_model.py -v`
Expected: all PASS.

- [x] **Step 5: Write the failing `ModelForm` provider-Select and location-Select tests**

Add to `tests/screens/test_forms.py`:

```python
@pytest.mark.asyncio
async def test_modelform_add_mode_provider_is_a_select():
    """Add mode offers a real provider dropdown, not a locked label."""
    form = ModelForm(
        providers=["ollama", "llamacpp", "claude"],
        provider_kinds={"claude": "native"},
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        sel = app.screen.query_one("#provider-select", Select)
        assert not sel.disabled
        assert [str(v) for _, v in sel._options] == ["ollama", "llamacpp", "claude"]


@pytest.mark.asyncio
async def test_modelform_edit_mode_provider_select_is_disabled():
    variant: VariantSpec = {"id": "q4", "provider": "llamacpp", "name": "q4.gguf", "repo": "foo/bar", "files": ["q4.gguf"]}
    form = ModelForm(providers=["llamacpp", "ollama"], variant=variant)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        sel = app.screen.query_one("#provider-select", Select)
        assert sel.disabled
        assert str(sel.value) == "llamacpp"


@pytest.mark.asyncio
async def test_modelform_location_locked_cloud_for_native_provider():
    form = ModelForm(
        providers=["claude"], default_provider="claude", provider_kinds={"claude": "native"}
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        sel = app.screen.query_one("#location-select", Select)
        assert sel.disabled
        assert str(sel.value) == "cloud"


@pytest.mark.asyncio
async def test_modelform_location_locked_local_for_omlx():
    form = ModelForm(
        providers=["omlx"], default_provider="omlx", provider_kinds={"omlx": "local-only"}
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        sel = app.screen.query_one("#location-select", Select)
        assert sel.disabled
        assert str(sel.value) == "local"


@pytest.mark.asyncio
async def test_modelform_location_editable_for_ollama():
    form = ModelForm(providers=["ollama"], default_provider="ollama")
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        sel = app.screen.query_one("#location-select", Select)
        assert not sel.disabled


@pytest.mark.asyncio
async def test_submit_native_blank_model_defaults_to_native_sentinel():
    form = ModelForm(
        providers=["claude"], default_provider="claude", provider_kinds={"claude": "native"}
    )
    dismissed: list = []
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, dismissed.append)
        await pilot.pause()
        await pilot.press("enter")  # blank #model input
        await pilot.pause()

    spec = dismissed[0].spec
    assert spec["name"] == "native"
    assert spec["id"] == "claude/native"


@pytest.mark.asyncio
async def test_submit_native_named_model():
    form = ModelForm(
        providers=["claude"], default_provider="claude", provider_kinds={"claude": "native"}
    )
    dismissed: list = []
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, dismissed.append)
        await pilot.pause()
        _fill_model(app, "opus")
        await pilot.press("enter")
        await pilot.pause()

    spec = dismissed[0].spec
    assert spec["name"] == "opus"
    assert spec["id"] == "claude/opus"
```

- [x] **Step 6: Run to verify they fail**

Run: `uv run pytest tests/screens/test_forms.py -k "provider_is_a_select or location_locked or location_editable or submit_native or edit_mode_provider_select" -v`
Expected: FAIL — `#provider-select` / `#location-select` don't exist yet; `ModelForm.__init__` doesn't accept `provider_kinds`.

- [x] **Step 7: Rewrite `ModelForm`**

In `src/modelman/screens/forms.py`, change `__init__` (add `provider_kinds`):

```python
    def __init__(
        self,
        providers: list[str],
        variant: VariantSpec | None = None,
        default_provider: str | None = None,
        families: list[str] | None = None,
        family: str | None = None,
        provider_kinds: dict[str, str] | None = None,
    ) -> None:
        super().__init__()
        self._providers = providers
        self._variant = variant  # None for add
        self._default_provider = default_provider
        self._family = family
        self._provider_kinds = provider_kinds or {}
        self._families: list[str] = (
            list(families) if families else ([family] if family else ["unknown"])
        )
        if self._family is not None and self._family not in self._families:
            self._families.insert(0, self._family)
```

Replace `compose` (lines 271-313):

```python
    def compose(self) -> ComposeResult:
        editing = self._variant is not None
        v: VariantSpec = self._variant if self._variant is not None else cast("VariantSpec", {})
        if editing:
            initial_provider = v.get("provider") or self._providers[0]
        elif self._default_provider and self._default_provider in self._providers:
            initial_provider = self._default_provider
        else:
            initial_provider = self._providers[0]
        self._initial_provider: str = initial_provider

        model_val = self._reconstruct_model(v) if editing else ""
        kind = self._provider_kinds.get(initial_provider, "ollama")
        placeholder = (
            "e.g. ornith-1.5:35b"
            if kind == "ollama"
            else "leave blank for 'native', or a model name"
            if kind == "native"
            else "org/repo[/path/to/file]"
        )
        location_value = "cloud" if kind in ("native", "cloud-only") else "local" if kind == "local-only" else v.get("location") or "local"
        location_locked = kind in ("native", "cloud-only", "local-only")

        with Vertical():
            yield Label("Provider:")
            yield Select(
                options=[(p, p) for p in self._providers],
                value=initial_provider,
                allow_blank=False,
                disabled=editing,
                id="provider-select",
            )
            yield Label("Family:")
            yield Select(
                options=[(f, f) for f in self._families],
                value=(self._family if self._family in self._families else self._families[0]),
                allow_blank=False,
                id="family-select",
            )
            yield Label("Model:")
            yield Input(
                value=model_val,
                placeholder=placeholder,
                id="model",
            )
            yield Label("", id="model-error")
            yield Label("Location:")
            yield Select(
                options=[("cloud", "cloud"), ("local", "local")],
                value=location_value,
                allow_blank=False,
                disabled=location_locked,
                id="location-select",
            )
            with Horizontal():
                yield Button("Cancel", id="cancel", variant="default")
                yield Button("Save", id="save", variant="primary")
```

(Drops the old `#provider-label` static `Label(f"Provider: {initial_provider}")` entirely — every test referencing `_rendered_provider`/`#provider-label` must be updated per Step 9 below.)

Replace `_submit` (lines 349-388):

```python
    def _submit(self) -> None:
        provider = str(self.query_one("#provider-select", Select).value)
        kind = self._provider_kinds.get(provider, "ollama")
        raw = self.query_one("#model", Input).value
        try:
            name, repo, filename = parse_model(provider, raw, is_native=(kind == "native"))
        except ValueError as exc:
            self._show_error(str(exc))
            return
        self._clear_error()

        if self._variant is not None:
            vid = self._variant["id"]
        elif kind == "native":
            vid = f"{provider}/{name}"
        else:
            vid = f"{provider}/{name.replace('/', '--')}"  # type: ignore[union-attr]

        existing_quantizations = (
            (self._variant or {}).get("quantizations") if self._variant is not None else None
        )
        location = str(self.query_one("#location-select", Select).value)
        spec: VariantSpec = {
            "id": vid,
            "provider": provider,
            "name": name or vid,
            "repo": repo,
            "files": [filename] if filename else None,
            "quantizations": existing_quantizations,
            "location": location,
        }
        if provider == "ollama" and self._variant is None and name:
            spec["model_info"] = auto_detect_model_info(name)
        else:
            spec["model_info"] = (self._variant or {}).get("model_info")
        family = str(self.query_one("#family-select", Select).value)
        self.dismiss(ModelFormResult(spec=spec, family=family))
```

Note: `VariantSpec` (the TypedDict in `providers/base.py`) doesn't currently declare a `location` key — since it's `total=False`, adding an extra key at runtime doesn't break anything, but add `location: str | None` to `VariantSpec` in `src/modelman/providers/base.py` for type-checker cleanliness, and thread it through `_variant_to_model_entry` in `screens/models.py` (`location=variant.get("location")` in the `ModelEntry(...)` construction) so the form's location choice actually reaches the registry.

- [x] **Step 8: Add `location` to `VariantSpec` and `_variant_to_model_entry`**

In `src/modelman/providers/base.py`, add to `VariantSpec`:

```python
    location: str | None  # "local" | "cloud"
```

In `src/modelman/screens/models.py`, `_variant_to_model_entry` (around line 54-62), add `location=variant.get("location")` to the `ModelEntry(...)` construction.

- [x] **Step 9: Wire `provider_kinds` into `ModelScreen`'s Add/Edit calls**

`ModelForm`'s new `provider_kinds` parameter is meaningless until something actually computes it from the registry and passes it in — without this step, every real Add/Edit dialog defaults every provider to `"ollama"` (editable location, no native placeholder), silently defeating the location-lock rules this task just built. In `src/modelman/screens/models.py`, add a helper near `_provider_list`:

```python
    def _provider_kinds(self) -> dict[str, str]:
        """Map each registered provider id to the ModelForm 'kind' that
        drives its Location-select lock rule: native providers and
        llamacpp/omlx are locked (cloud and local respectively); ollama
        is the one provider where location is genuinely editable;
        everything else (openrouter, any other unmapped provider) locks
        to cloud."""
        kinds: dict[str, str] = {}
        for p in self.registry.providers:
            if p.auth.type == "native":
                kinds[p.id] = "native"
            elif p.id in ("llamacpp", "omlx"):
                kinds[p.id] = "local-only"
            elif p.id == "ollama":
                kinds[p.id] = "ollama"
            else:
                kinds[p.id] = "cloud-only"
        return kinds
```

In `action_add_model`, add `provider_kinds=self._provider_kinds(),` to the `ModelForm(...)` construction. In `action_edit_model`, do the same.

Add a test to `tests/screens/test_app_navigation.py` pinning the wiring end-to-end:

```python
@pytest.mark.asyncio
async def test_add_model_dialog_locks_location_for_native_provider(tmp_path, monkeypatch):
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[
            ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none")),
            ProviderEntry(
                id="claude", name="Claude", location="cloud", auth=AuthConfig(type="native")
            ),
        ],
        models=[],
    )
    save_registry(reg, reg_path)
    save_state(StateStore(), state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    from modelman.app import ModelmanApp
    from modelman.screens.forms import ModelForm

    app = ModelmanApp()
    captured: list[ModelForm] = []
    original_push = app.push_screen

    def tracking_push(screen, *args, **kwargs):
        if isinstance(screen, ModelForm):
            captured.append(screen)
        return original_push(screen, *args, **kwargs)

    monkeypatch.setattr(app, "push_screen", tracking_push)

    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        app.screen._last_provider_used = "claude"
        await pilot.press("a")
        await pilot.pause()

    assert captured
    assert captured[0]._provider_kinds["claude"] == "native"
```

Run: `uv run pytest tests/screens/test_app_navigation.py -k locks_location_for_native_provider -v`
Expected: PASS once the wiring above is in place.

- [x] **Step 10: Update every existing `test_forms.py` test that used `_rendered_provider`/`#provider-label`**

`_rendered_provider` (the helper at the top of the file) queried `#provider-label` as a `Label`. Replace it:

```python
def _rendered_provider(app: ModelmanApp) -> str:
    sel = app.screen.query_one("#provider-select", Select)
    return str(sel.value)
```

And every assertion of the form `assert _rendered_provider(app) == "Provider: omlx"` becomes `assert _rendered_provider(app) == "omlx"` (drop the `"Provider: "` prefix) in: `test_modelform_add_mode_uses_default_provider`, `test_modelform_add_mode_ignores_unknown_default_provider`, `test_modelform_add_mode_without_default_falls_back_to_first`, `test_modelform_edit_mode_uses_variant_provider`.

- [x] **Step 11: Run the full forms test file**

Run: `uv run pytest tests/screens/test_forms.py -v`
Expected: all PASS.

- [x] **Step 12: Run the full suite one final time**

Run: `uv run pytest -v 2>&1 | tail -60`
Expected: all PASS. Also run `make lint` and `make typecheck` (per this project's `Makefile`) and fix any new violations (unused `is_cloud` import removed in Task 6, unused `Fetch`/typing adjustments from this task's `location` addition, etc.).

- [x] **Step 13: Commit**

```bash
git add src/modelman/screens/forms.py src/modelman/screens/models.py src/modelman/providers/base.py tests/screens/test_forms.py tests/screens/test_parse_model.py
git commit -m "feat(forms): provider/location selects, native and openrouter model-name parsing - completes plan item #8"
```
