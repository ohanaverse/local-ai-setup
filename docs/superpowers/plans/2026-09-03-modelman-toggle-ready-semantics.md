# modelman toggle-ready semantics — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the "toggle ready does nothing" bug for local-artifact models (llamacpp/omlx files already on disk) by making reconcile write `state` directly instead of an in-memory overlay, implementing the three-state model (configured → ready → exposed) with a cascade from expose to ready, and reassigning the `r`/`x` keys so reconcile is automatic and the two toggles are `r` (ready) and `x` (exposed).

**Architecture:** Add a config-driven `model_has_local_artifact()` classifier in `registry.py`. Both `FamilyScreen._run_reconcile` and `ModelScreen._run_reconcile` stop building a `reconciled: dict` overlay and instead write straight into `StateStore` for local-artifact models (leaving `state.ready` alone for cloud/native models, since only local providers can be reconcile-verified against a filesystem). The UI reads `state` everywhere; `PendingChanges.apply()` needs no functional change since its ready-then-expose ordering already gives the cascade its correct effect.

**Tech Stack:** Python 3.13, Textual (TUI), `uv run pytest`, `tomllib`/`tomli_w`.

**Spec:** `docs/superpowers/specs/2026-09-03-modelman-toggle-ready-semantics-design.md`

## Global Constraints

- Python 3.13 only; run everything through `uv run` (per repo convention — a pyenv-managed interpreter will not resolve project deps).
- Every mutating dataclass update on `StateStore` entries goes through `dataclasses.replace(existing, ...)` — never construct a bare `ModelState(...)` when other fields (notably `litellm_exposed`) must survive.
- `model_has_local_artifact()` is the *only* place that decides whether reconcile may write `state.ready`; `is_local_location()` (existing helper) stays scoped to display-only filtering (FamilyScreen's DOWNLOADED/SIZE columns) and is not touched.
- `sync.py` (the `modelman sync` CLimand) is explicitly **out of scope** — the design's Non-goals section excludes changing how reconcile discovers files, and `sync.py` is not listed in the spec's "Files changed" table. Its `RECONCILABLE_PROVIDERS` hard-coded tuple stays as-is.
- **Resolved ambiguity (idempotent toggle guard, confirmed with the user):** for both `action_toggle_ready` and `action_toggle_expose`, "target matches state" is evaluated queue-aware: `displayed = queued.get(mid, persisted)`, `target = not displayed`. If `target == persisted`, the toggle is a repeated keypress that would just cancel a still-unapplied queued flip — pop the queue entry and notify instead of re-queuing. This is a new implementation decision not spelled out verbatim in the design doc; it reuses the queue-aware `current` pattern `action_toggle_expose` already has today.
- Section 8/9 of the design doc has a key-label inconsistency (it describes the local-artifact-target-False no-op using the *old* `x`=ready binding). This plan uses the *correct*, final key mapping throughout: `r` = toggle ready, `x` = toggle exposed (per design §6).
- Run focused test files per task (`uv run pytest tests/...`), not the full suite, except the final task which runs `make all`.

---

## File Structure

| File | Responsibility |
|---|---|
| `src/modelman/registry.py` | Add `model_has_local_artifact(model, provider) -> bool` |
| `src/modelman/screens/families.py` | `_run_reconcile` writes `state` directly for local-artifact models; drop `_reconciled` overlay; drop `r`-reconcile binding/action |
| `src/modelman/screens/models.py` | Same reconcile-writes-state change; `BINDINGS` swap (`r`=ready, `x`=exposed, drop reconcile); `_is_ready()` simplified; `_refresh_details_panel()` reads `state.disk_path`; new `action_toggle_ready`/`action_toggle_expose` semantics |
| `src/modelman/queue.py` | No functional change (existing ready-before-expose ordering already gives the cascade its effect) — add a regression test only |
| `tests/test_registry.py` | Unit tests for `model_has_local_artifact` |
| `tests/screens/test_families.py` | Reconcile-writes-state tests; binding-removal test; update the test that called `action_reconcile()` directly |
| `tests/screens/test_models.py` | BINDINGS/footer tests, reconcile-writes-state tests, new toggle-ready/toggle-expose semantics tests; update tests referencing `_is_ready`/old keys |
| `tests/test_queue.py` | One new test pinning ready-before-expose cascade ordering |
| `modelman/CLAUDE.md`, `modelman/README.md`, `docs/guides/02-providers-and-models.md`, `docs/guides/03-model-families.md`, `docs/guides/04-litellm-config.md` | Update keybinding descriptions to match the new `r`/`x` mapping and drop references to manual reconcile |

---

### Task 1: `model_has_local_artifact` classifier

**Files:**
- Modify: `modelman/src/modelman/registry.py` (add near `is_local_location`, ~line 296)
- Test: `modelman/tests/test_registry.py`

**Interfaces:**
- Produces: `model_has_local_artifact(model: ModelEntry, provider: ProviderEntry | None) -> bool` — consumed by Tasks 2 and 4 (`screens/families.py`, `screens/models.py`).

- [ ] **Step 1: Write the failing tests**

Append to `modelman/tests/test_registry.py` (near the other helper tests, e.g. after `test_provider_config_includes_model_dir_when_set`):

```python
from modelman.registry import model_has_local_artifact


def test_model_has_local_artifact_true_for_local_model_and_provider():
    """The common case: an unset/local model on an unset/local provider
    (llamacpp, omlx) is reconcile-syncable from the filesystem."""
    model = ModelEntry(id="llamacpp/a", family="f", provider_id="llamacpp", model_name="a")
    provider = ProviderEntry(id="llamacpp", name="llama.cpp", location="local")
    assert model_has_local_artifact(model, provider) is True


def test_model_has_local_artifact_true_when_locations_unset():
    """Legacy entries with no explicit location must default to local,
    matching is_local_location's existing legacy-default semantics."""
    model = ModelEntry(id="omlx/a", family="f", provider_id="omlx", model_name="a")
    provider = ProviderEntry(id="omlx", name="oMLX")
    assert model_has_local_artifact(model, provider) is True


def test_model_has_local_artifact_false_for_ollama_cloud_model():
    """The ollama-cloud case: provider is local, but the model itself is
    tagged location='cloud' (e.g. glm-5.3:cloud) — no local file to
    reconcile against."""
    model = ModelEntry(
        id="ollama/glm:cloud", family="f", provider_id="ollama", model_name="glm:cloud",
        location="cloud",
    )
    provider = ProviderEntry(id="ollama", name="Ollama", location="local")
    assert model_has_local_artifact(model, provider) is False


def test_model_has_local_artifact_false_for_cloud_provider():
    """A model on a cloud provider (openrouter, native agents) has no
    local artifact regardless of the model's own location field."""
    model = ModelEntry(id="openrouter/x", family="f", provider_id="openrouter", model_name="x")
    provider = ProviderEntry(id="openrouter", name="OpenRouter", location="cloud")
    assert model_has_local_artifact(model, provider) is False


def test_model_has_local_artifact_false_when_provider_missing():
    """A model referencing a provider id no longer in the registry has no
    reconciled source either way; treat as not having a local artifact
    rather than guessing."""
    model = ModelEntry(id="ghost/x", family="f", provider_id="ghost", model_name="x")
    assert model_has_local_artifact(model, None) is False
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd modelman && uv run pytest tests/test_registry.py -k model_has_local_artifact -v`
Expected: `ImportError: cannot import name 'model_has_local_artifact'`

- [ ] **Step 3: Implement the helper**

In `modelman/src/modelman/registry.py`, add directly after `is_local_location` (currently ending at line 296):

```python
def model_has_local_artifact(
    model: ModelEntry, provider: ProviderEntry | None
) -> bool:
    """True when reconcile can sync `state.ready` from the filesystem.

    Classification is config-driven (ModelEntry.location,
    ProviderEntry.location), never a hard-coded provider id list, so a
    newly added local provider opts into reconcile-sync automatically.
    A model tagged location="cloud" (the ollama-cloud case: local
    provider, cloud model) or living on a cloud provider (openrouter,
    native agents) has no on-disk artifact reconcile could observe. A
    model referencing a provider missing from the registry is treated
    the same way — there is no reconciled source either way.
    """
    if model.location == "cloud":
        return False
    if provider is not None and provider.location == "cloud":
        return False
    if provider is None:
        return False
    return True
```

Note: the design doc's truth table lists a "provider is None" row as falling through to `True` via the general `return True` at the bottom of its sketch, but its own prose says "we treat it as not having a local artifact" — implement the prose (`return False` when `provider is None`), which is what the test above (`test_model_has_local_artifact_false_when_provider_missing`) pins.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd modelman && uv run pytest tests/test_registry.py -k model_has_local_artifact -v`
Expected: 5 passed

- [ ] **Step 5: Commit**

```bash
cd modelman
git add src/modelman/registry.py tests/test_registry.py
git commit -m "$(cat <<'EOF'
feat(modelman): add model_has_local_artifact classifier - completes plan item #1

Config-driven (ModelEntry.location / ProviderEntry.location), not a
hard-coded provider id list, so future local providers opt in for free.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_016a1L2LG8cvJt7vnbgAnnLC
EOF
)"
```

---

### Task 2: FamilyScreen — reconcile writes `state` directly

**Files:**
- Modify: `modelman/src/modelman/screens/families.py`
- Test: `modelman/tests/screens/test_families.py`

**Interfaces:**
- Consumes: `model_has_local_artifact(model, provider)` from Task 1.
- Produces: `FamilyScreen` no longer has a `_reconciled` attribute or an `action_reconcile`/`r`-reconcile binding; `reload()` reads `DOWNLOADED`/`SIZE` from `self.state` only.

- [ ] **Step 1: Write the failing tests**

Add to `modelman/tests/screens/test_families.py` (after the existing reconcile tests, e.g. after `test_family_screen_downloaded_excludes_cloud`'s block — check its tail with `grep -n "test_family_screen_downloaded_excludes_cloud" -A 40 tests/screens/test_families.py` if you need the exact insertion point):

```python
@pytest.mark.asyncio
async def test_family_screen_reconcile_sets_state_ready_for_local_artifact(tmp_path, monkeypatch):
    """Regression for the reported bug: a local-artifact model (llamacpp/
    omlx) whose files are on disk but whose modelman.toml still says
    ready=False must have state.ready flipped to True by the automatic
    reconcile that runs on mount — no manual 'r' press required, because
    reconcile now writes state.py directly instead of a display-only
    overlay the toggle could never see."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="omlx", name="oMLX", location="local")],
        families=[FamilyEntry(name="ornith")],
        models=[
            ModelEntry(id="omlx/a", family="ornith", provider_id="omlx", model_name="a"),
        ],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("omlx/a", ModelState(ready=False))  # stale: files exist but flag says no
    save_state(state, state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    from modelman.providers import registry as prov_registry

    stub = MagicMock()
    stub.name = "omlx"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = 19530941006
    stub.list_local.return_value = [{"variant_id": "a", "local_path": "/models/omlx/a"}]
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _wait_for_reconcile(pilot, app.screen)

        assert app.screen.state.get("omlx/a").ready is True
        assert app.screen.state.get("omlx/a").disk_path == "/models/omlx/a"
        assert app.screen.state.get("omlx/a").size_bytes == 19530941006


@pytest.mark.asyncio
async def test_family_screen_reconcile_clears_state_when_files_removed(tmp_path, monkeypatch):
    """The complementary direction: a local-artifact model marked ready
    in modelman.toml whose files are gone must be flipped back to
    ready=False with disk_path/size_bytes cleared."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="omlx", name="oMLX", location="local")],
        families=[FamilyEntry(name="ornith")],
        models=[ModelEntry(id="omlx/a", family="ornith", provider_id="omlx", model_name="a")],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("omlx/a", ModelState(ready=True, disk_path="/models/omlx/a", size_bytes=123))
    save_state(state, state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    from modelman.providers import registry as prov_registry

    stub = MagicMock()
    stub.name = "omlx"
    stub.is_downloaded.return_value = False
    stub.size_of.return_value = None
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _wait_for_reconcile(pilot, app.screen)

        st = app.screen.state.get("omlx/a")
        assert st.ready is False
        assert st.disk_path is None
        assert st.size_bytes is None


@pytest.mark.asyncio
async def test_family_screen_reconcile_leaves_ready_alone_for_ollama_cloud(tmp_path, monkeypatch):
    """An ollama-cloud model (provider local, model location='cloud') is
    not a local artifact: reconcile must not touch state.ready for it,
    even though `ollama show` (is_downloaded) reports it present. This
    intentionally reverses the pre-change TUI behavior for this specific
    case — it is out of scope for this bug fix (see design doc Non-goals
    and the ollama-cloud row of the model_has_local_artifact truth
    table); readiness for these models is driven by the ready toggle's
    apply-time download/pull, not by reconcile."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        families=[FamilyEntry(name="glm")],
        models=[
            ModelEntry(
                id="ollama/glm:cloud", family="glm", provider_id="ollama",
                model_name="glm:cloud", location="cloud",
            ),
        ],
    )
    save_registry(reg, reg_path)
    save_state(StateStore(), state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    from modelman.providers import registry as prov_registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = True  # `ollama show` succeeds: pulled
    stub.size_of.return_value = None
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _wait_for_reconcile(pilot, app.screen)

        assert app.screen.state.get("ollama/glm:cloud").ready is False


def test_family_screen_bindings_has_no_reconcile_key():
    """Manual reconcile is gone from the family screen: reconcile now
    runs automatically on mount and on_screen_resume."""
    from modelman.screens.families import FamilyScreen

    keys = [b[0] if isinstance(b, tuple) else b.key for b in FamilyScreen.BINDINGS]
    assert "r" not in keys
    assert not hasattr(FamilyScreen, "action_reconcile")
```

Update the existing `test_family_screen_cursor_restored_after_reconcile` test (it calls `app.screen.action_reconcile()`, which this task removes): replace that one line with a direct call to the worker-starting method it used to dispatch to:

```python
        app.screen.action_reconcile()
```
becomes
```python
        app.screen._start_reconcile_worker()
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd modelman && uv run pytest tests/screens/test_families.py -k "reconcile_sets_state or reconcile_clears_state or leaves_ready_alone or bindings_has_no_reconcile" -v`
Expected: FAIL — `state.get("omlx/a").ready` is still `False`/unchanged (overlay-only write), or `AttributeError`/binding still present.

- [ ] **Step 3: Implement**

In `modelman/src/modelman/screens/families.py`:

1. Drop the `_reconciled` attribute from `__init__` (delete the block at lines 53–57: the comment + `self._reconciled: dict[str, dict] = {}`).

2. Update the import block to add `model_has_local_artifact`:

```python
from ..registry import (
    FamilyEntry,
    Registry,
    RegistryError,
    family_display_name,
    is_local_location,
    known_families,
    load_registry,
    model_has_local_artifact,
    provider_config,
    save_registry,
)
```

3. Replace `_run_reconcile` entirely:

```python
    def _run_reconcile(self, generation: int) -> None:
        """Ask each provider whether its models are on disk; write the
        result straight into `state` for local-artifact models (files
        present -> ready=True + disk_path + size_bytes; absent ->
        ready=False + cleared path/size). Non-local-artifact models
        (cloud-located, or on a cloud provider) are never marked ready
        by reconcile — only disk_path/size_bytes are opportunistically
        updated when the provider reports them, mirroring
        ModelScreen._run_reconcile.

        The UI-toggle step (unlock + reload) must run on the main
        thread; `app.call_from_thread` handles that. `generation` is
        this worker's token from _start_reconcile_worker, forwarded to
        _reconcile_done so a superseded worker can be told apart from
        the current one.
        """
        from dataclasses import replace

        try:
            providers: dict[str, object] = {}
            provider_entries: dict[str, object] = {}
            for m in self.registry.models:
                pname = m.provider_id
                if pname not in provider_entries:
                    try:
                        provider_entries[pname] = self.registry.provider(pname)
                    except KeyError:
                        provider_entries[pname] = None
                provider_entry = provider_entries[pname]
                if pname not in providers:
                    if provider_entry is None:
                        continue
                    try:
                        providers[pname] = ProviderRegistry.get(
                            pname, provider_config(provider_entry)
                        )
                    except Exception:
                        continue
                provider = providers.get(pname)
                if provider is None:
                    continue
                spec = _model_entry_to_variant(m)
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
                if model_has_local_artifact(m, provider_entry):
                    existing = self.state.get(m.id)
                    if ready:
                        self.state.set(
                            m.id,
                            replace(existing, ready=True, disk_path=local_path, size_bytes=size),
                        )
                    else:
                        self.state.set(
                            m.id,
                            replace(existing, ready=False, disk_path=None, size_bytes=None),
                        )
                elif local_path is not None or size is not None:
                    existing = self.state.get(m.id)
                    self.state.set(
                        m.id,
                        replace(
                            existing,
                            disk_path=local_path if local_path is not None else existing.disk_path,
                            size_bytes=size if size is not None else existing.size_bytes,
                        ),
                    )
        finally:
            self.app.call_from_thread(self._reconcile_done, generation)
```

4. In `_refresh_from_disk`, drop the overlay-clear line and its stale comment:

```python
    def _refresh_from_disk(self) -> None:
        """Reload registry.toml/modelman.toml and re-run reconcile.

        Used both on screen resume (after popping back from a child
        screen that may have mutated the on-disk files — ModelScreen
        saves on apply) and by the family list's mount path. Reconcile
        now writes state.py directly, so nothing needs clearing between
        runs the way the old in-memory overlay did.
        """
        self._load_from_disk()
        self.reload()  # show state-truth immediately
        self._start_reconcile_worker()
```

5. In `reload()`'s `_repopulate`, replace the overlay-vs-state branch with a single state read:

```python
                for m in models:
                    # Non-local entries hold no disk weights and cannot be
                    # duplicates — never count them toward DOWNLOADED/SIZE.
                    # Legacy entries with location=None still count as local.
                    if not is_local_location(m.location):
                        continue
                    st = self.state.get(m.id)
                    if not st.ready:
                        continue
                    downloaded_count += 1
                    if st.size_bytes is None:
                        unknown = True
                    else:
                        total_size += st.size_bytes
```

6. Remove `action_reconcile` entirely (the `("r", "reconcile", "Reconcile")` binding and the method):

Delete this binding line from `BINDINGS`:
```python
        ("r", "reconcile", "Reconcile"),
```

Delete this method:
```python
    def action_reconcile(self) -> None:
        if self._reconciling:
            return
        self._refresh_from_disk()
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd modelman && uv run pytest tests/screens/test_families.py -v`
Expected: all pass (this runs the full families screen test file, since the overlay removal touches most of its tests' plumbing — worth confirming nothing else broke).

- [ ] **Step 5: Commit**

```bash
cd modelman
git add src/modelman/screens/families.py tests/screens/test_families.py
git commit -m "$(cat <<'EOF'
fix(modelman): FamilyScreen reconcile writes state.py directly - completes plan item #2

Drops the in-memory reconciled overlay reload() used to prefer over
state; reconcile now sets state.ready/disk_path/size_bytes for
local-artifact models so the DOWNLOADED/SIZE columns reflect what a
later toggle-ready press will actually see. Manual 'r' reconcile is
removed — reconcile already runs automatically on mount and resume.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_016a1L2LG8cvJt7vnbgAnnLC
EOF
)"
```

---

### Task 3: ModelScreen — BINDINGS swap and footer text

**Files:**
- Modify: `modelman/src/modelman/screens/models.py`
- Test: `modelman/tests/screens/test_models.py`

**Interfaces:**
- Produces: `ModelScreen.BINDINGS` has `r`→`toggle_ready`/"Toggle ready", `x`→`toggle_expose`/"Toggle exposed", no reconcile binding; `action_reconcile` method removed. (`action_toggle_ready`/`action_toggle_expose` method *bodies* are untouched by this task — Tasks 5/6 change their logic; this task only changes which key calls which method and what the footer says.)

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/screens/test_models.py` (near the other structural tests, e.g. after `test_provider_list_sorted`):

```python
def test_model_screen_bindings_use_new_key_mapping():
    """r = toggle ready, x = toggle exposed, no manual reconcile binding
    (reconcile is automatic on mount/resume)."""
    from modelman.screens.models import ModelScreen

    binding_map = {
        (b[0] if isinstance(b, tuple) else b.key): (
            b[1] if isinstance(b, tuple) else b.action
        )
        for b in ModelScreen.BINDINGS
    }
    assert binding_map["r"] == "toggle_ready"
    assert binding_map["x"] == "toggle_expose"
    assert "l" not in binding_map
    assert not any(action == "reconcile" for action in binding_map.values())
    assert not hasattr(ModelScreen, "action_reconcile")

    descriptions = {
        (b[0] if isinstance(b, tuple) else b.key): (
            b[2] if isinstance(b, tuple) else b.description
        )
        for b in ModelScreen.BINDINGS
    }
    assert descriptions["x"] == "Toggle exposed"
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -k bindings_use_new_key_mapping -v`
Expected: FAIL — `binding_map["r"] == "reconcile"`, not `"toggle_ready"`.

- [ ] **Step 3: Implement**

In `modelman/src/modelman/screens/models.py`, replace `BINDINGS`:

```python
    BINDINGS = [
        ("escape", "back", "Back"),
        ("a", "add_model", "Add"),
        ("d", "delete_model", "Delete"),
        ("e", "edit_model", "Edit"),
        Binding("enter", "select_row", "Edit", priority=True),
        ("r", "toggle_ready", "Toggle ready"),
        ("x", "toggle_expose", "Toggle exposed"),
    ]
```

Remove `action_reconcile` entirely:

```python
    def action_reconcile(self) -> None:
        self.run_worker(self._run_reconcile, exclusive=True, thread=True)
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -k bindings_use_new_key_mapping -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd modelman
git add src/modelman/screens/models.py tests/screens/test_models.py
git commit -m "$(cat <<'EOF'
feat(modelman): swap ModelScreen r/x bindings for ready/exposed - completes plan item #3

r now toggles ready (was reconcile — reconcile is automatic), x now
toggles exposed (was ready), l is dropped. Toggle action bodies are
unchanged in this commit; only the key/label wiring moves.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_016a1L2LG8cvJt7vnbgAnnLC
EOF
)"
```

---

### Task 4: ModelScreen — reconcile writes `state` directly; UI reads `state`

**Files:**
- Modify: `modelman/src/modelman/screens/models.py`
- Test: `modelman/tests/screens/test_models.py`

**Interfaces:**
- Consumes: `model_has_local_artifact` from Task 1.
- Produces: `ModelScreen.reconciled` attribute removed; `_is_ready(model_id) -> bool` simplified to a pure `state.ready` read (signature unchanged — still consumed by Tasks 5/6); `_refresh_details_panel` reads `state.disk_path`.

- [ ] **Step 1: Write the failing tests**

Add to `modelman/tests/screens/test_models.py`:

```python
@pytest.mark.asyncio
async def test_reconcile_sets_state_ready_for_local_artifact_omlx_model(tmp_path, monkeypatch):
    """Regression for the reported bug: an omlx model whose files are on
    disk but whose modelman.toml still says ready=False must reconcile
    to ready=True automatically on mount — this is the exact scenario
    from the design doc's bug report (ornith-1.5/omlx)."""
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry

    model = ModelEntry(
        id="omlx/ornith-1.5", family="ornith", provider_id="omlx", model_name="ornith-1.5",
    )
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="omlx", name="oMLX", auth=AuthConfig(type="none"), location="local")],
        families=[FamilyEntry(name="ornith")],
        models=[model],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("omlx/ornith-1.5", ModelState(ready=False))
    save_state(state, state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    stub = MagicMock()
    stub.name = "omlx"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = 19530941006
    stub.list_local.return_value = [
        {"variant_id": "ornith-1.5", "local_path": "/Users/keith/.omlx/models/ornith-1.5"}
    ]
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.pause()  # let the reconcile worker settle

        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)
        assert app.screen.state.get("omlx/ornith-1.5").ready is True
        assert (
            app.screen.state.get("omlx/ornith-1.5").disk_path
            == "/Users/keith/.omlx/models/ornith-1.5"
        )
        assert app.screen.state.get("omlx/ornith-1.5").size_bytes == 19530941006
        # No overlay attribute survives the rewrite.
        assert not hasattr(app.screen, "reconciled")
```

Update the existing `test_pulled_cloud_model_reconciles_as_ready` test — its premise (reconcile marks an ollama-cloud model ready) is reversed by this design (ollama-cloud is a non-local-artifact model; reconcile intentionally leaves its `ready` flag alone — see design §1 truth table and Task 2's `test_family_screen_reconcile_leaves_ready_alone_for_ollama_cloud`). Replace the whole test body:

```python
@pytest.mark.asyncio
async def test_pulled_cloud_ollama_model_reconcile_leaves_ready_alone(tmp_path, monkeypatch):
    """An ollama `:cloud` model (location='cloud' on the local ollama
    provider) is not a local artifact per model_has_local_artifact:
    reconcile must NOT set state.ready for it even though `ollama show`
    (is_downloaded) succeeds. This is an intentional behavior change from
    the pre-fix TUI (see design doc Non-goals) — ollama-cloud readiness
    is driven by the ready toggle's apply-time pull, not by reconcile."""
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
        assert app.screen._is_ready("ollama/glm-5.2:cloud") is False
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -k "reconcile_sets_state_ready_for_local_artifact_omlx or pulled_cloud_ollama_model_reconcile_leaves_ready_alone" -v`
Expected: FAIL — state untouched by the old overlay-only reconcile, or `hasattr(app.screen, "reconciled")` is True, or (for the second test) `_is_ready` returns `True` under the old overlay-preferring logic.

- [ ] **Step 3: Implement**

In `modelman/src/modelman/screens/models.py`:

1. Add `model_has_local_artifact` to the registry import block:

```python
from ..registry import (
    DEFAULT_PROVIDER_IDS,
    Cost,
    Fetch,
    ModelEntry,
    Registry,
    _cost_from_dict,
    _cost_to_dict,
    known_families,
    model_has_local_artifact,
    provider_config,
    save_registry,
)
```

2. In `__init__`, delete the overlay attribute and its comment:

```python
        # Reconcile overlay: per-model-id reality from the provider.
        self.reconciled: dict[str, dict] = {}
```

3. Replace `_run_reconcile` entirely:

```python
    def _run_reconcile(self) -> None:
        """Ask each provider whether its models are on disk; write the
        result straight into `state` for local-artifact models. Files
        present -> ready=True + disk_path + size_bytes; absent ->
        ready=False + cleared path/size. Non-local-artifact models
        (cloud-located, or on a cloud provider) are left alone by this
        step — only disk_path/size_bytes are opportunistically updated
        when the provider reports them; their ready flag is driven by
        the ready-toggle's apply-time download/pull instead.
        """
        # `replace` is already imported at module level (top of this file).
        from ..providers.registry import ProviderRegistry

        family_models = self.registry.models_by_family(self.family)
        by_provider: dict[str, list[ModelEntry]] = defaultdict(list)
        for m in family_models:
            by_provider[m.provider_id].append(m)
        for provider_name, entries in by_provider.items():
            try:
                provider_entry = self.registry.provider(provider_name)
                provider = ProviderRegistry.get(provider_name, provider_config(provider_entry))
            except Exception:
                continue
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
                if model_has_local_artifact(m, provider_entry):
                    existing = self.state.get(m.id)
                    if ready:
                        self.state.set(
                            m.id,
                            replace(existing, ready=True, disk_path=local_path, size_bytes=size),
                        )
                    else:
                        self.state.set(
                            m.id,
                            replace(existing, ready=False, disk_path=None, size_bytes=None),
                        )
                elif local_path is not None or size is not None:
                    existing = self.state.get(m.id)
                    self.state.set(
                        m.id,
                        replace(
                            existing,
                            disk_path=local_path if local_path is not None else existing.disk_path,
                            size_bytes=size if size is not None else existing.size_bytes,
                        ),
                    )
        # Re-render on the main thread.
        self.app.call_from_thread(self.reload)
```

4. Simplify `_load_models`'s `_repopulate` to read `state` only (replace the `rec = self.reconciled.get(m.id)` branch):

```python
            for m in models:
                ready = self._is_ready(m.id)
                size_str = _human_size(self.state.get(m.id).size_bytes) if ready else "—"
```

(this replaces the previous ~14-line `if rec is not None: ... else: ...` block that fell back to a live `prov.size_of()` call — no longer needed since `state.size_bytes` is always kept current by reconcile.)

5. Simplify `_is_ready`:

```python
    def _is_ready(self, model_id: str) -> bool:
        """Truth about whether a model is ready to use — a pure read of
        state.ready. Reconcile (on mount/resume) is the only writer of
        this flag for local-artifact models; the ready toggle's apply
        step is the writer for cloud/native models."""
        return self.state.get(model_id).ready
```

6. Simplify `_refresh_details_panel` to drop the overlay lookup:

```python
    def _refresh_details_panel(self, cursor_row: int) -> None:
        """Show the on-disk path of the row under the cursor, from
        state.disk_path; renders an em dash when the model isn't ready
        or its path is unknown.
        """
        try:
            details = self.query_one("#details-panel", Static)
            mt = self.query_one("#model-table", DataTable)
        except NoMatches:
            return  # not mounted (e.g. screen teardown race)
        if cursor_row < 0 or cursor_row >= mt.row_count:
            details.update("path: —")
            return
        row_key = list(mt.rows.keys())[cursor_row]
        mid = str(row_key.value)
        path = self.state.get(mid).disk_path if self._is_ready(mid) else None
        details.update(Text(f"path: {path or '—'}"))
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -v`
Expected: all pass (this file's tests are heavily coupled to the overlay; run the whole file to confirm nothing else broke — e.g. `test_model_screen_columns_and_details_panel`, `test_details_panel_updates_on_cursor_move`).

- [ ] **Step 5: Commit**

```bash
cd modelman
git add src/modelman/screens/models.py tests/screens/test_models.py
git commit -m "$(cat <<'EOF'
fix(modelman): ModelScreen reconcile writes state.py directly - completes plan item #4

Same fix as FamilyScreen (previous commit): drops the reconciled
overlay _is_ready() used to prefer, so a toggle applied after reconcile
sees the same truth the table renders. Local-artifact models (llamacpp/
omlx files, unlocated ollama) get ready/disk_path/size_bytes synced
from the filesystem; ollama-cloud and other cloud-located models are
left alone (ready is driven by the toggle's apply-time pull instead).

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_016a1L2LG8cvJt7vnbgAnnLC
EOF
)"
```

---

### Task 5: `action_toggle_ready` — new semantics

**Files:**
- Modify: `modelman/src/modelman/screens/models.py`
- Test: `modelman/tests/screens/test_models.py`

**Interfaces:**
- Consumes: `model_has_local_artifact` (Task 1), `self._is_ready` (Task 4, now a pure `state.ready` read), `self.queued_ready: dict[str, bool]` (existing).
- Produces: `action_toggle_ready()` behavior — bound to `r` since Task 3.

- [ ] **Step 1: Write the failing tests**

Add to `modelman/tests/screens/test_models.py`:

```python
@pytest.mark.asyncio
async def test_r_on_not_ready_local_artifact_model_queues_download(tmp_path, monkeypatch):
    """r on a not-ready llamacpp/omlx model queues a download — the
    reconcilable-provider path through PendingChanges.apply()."""
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry

    model = ModelEntry(id="omlx/a", family="ornith", provider_id="omlx", model_name="a")
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="omlx", name="oMLX", location="local")],
        families=[FamilyEntry(name="ornith")],
        models=[model],
    )
    save_registry(reg, reg_path)
    save_state(StateStore(), state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    stub = MagicMock()
    stub.name = "omlx"
    stub.is_downloaded.return_value = False
    stub.size_of.return_value = None
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.press("r")
        await pilot.pause()

        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)
        assert app.screen.queued_ready == {"omlx/a": True}


@pytest.mark.asyncio
async def test_r_on_ready_local_artifact_model_noops_with_notification(tmp_path, monkeypatch):
    """r on an already-ready local-artifact model must not queue a
    delete — that used to be invisible (state flipped to ready=False,
    but the next reconcile silently flipped it back). Reconcile is now
    the only writer of ready=False for local-artifact models."""
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry

    model = ModelEntry(id="omlx/a", family="ornith", provider_id="omlx", model_name="a")
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="omlx", name="oMLX", location="local")],
        families=[FamilyEntry(name="ornith")],
        models=[model],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("omlx/a", ModelState(ready=True, disk_path="/models/a", size_bytes=10))
    save_state(state, state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    stub = MagicMock()
    stub.name = "omlx"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = 10
    stub.list_local.return_value = [{"variant_id": "a", "local_path": "/models/a"}]
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.pause()  # let reconcile settle so state.ready is confirmed True
        await pilot.press("r")
        await pilot.pause()

        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)
        assert app.screen.queued_ready == {}


@pytest.mark.asyncio
async def test_r_on_not_ready_cloud_model_queues_download(tmp_path, monkeypatch):
    """r on a not-ready non-local-artifact (ollama-cloud) model still
    queues a download — the target-False no-op rule is local-artifact-
    only; cloud models go through the reconcilable-provider ready loop
    (ollama pull) exactly like before."""
    stub = _seed_cloud_family(tmp_path, monkeypatch, location="cloud")
    stub.is_downloaded.return_value = False

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.press("r")
        await pilot.pause()

        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)
        assert app.screen.queued_ready == {"ollama/glm-5.2:cloud": True}


@pytest.mark.asyncio
async def test_r_twice_cancels_queued_flip_with_notification(tmp_path, monkeypatch):
    """Pressing r twice in a row on a not-ready model queues, then
    cancels back to nothing (the second press's target, False, matches
    the persisted state.ready, False — a no-op relative to disk)."""
    stub = _seed_cloud_family(tmp_path, monkeypatch, location="cloud")
    stub.is_downloaded.return_value = False

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.press("r")
        await pilot.pause()
        assert app.screen.queued_ready == {"ollama/glm-5.2:cloud": True}
        await pilot.press("r")
        await pilot.pause()
        assert app.screen.queued_ready == {}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -k "r_on_not_ready_local_artifact or r_on_ready_local_artifact or r_on_not_ready_cloud or r_twice_cancels" -v`
Expected: FAIL — `test_r_on_ready_local_artifact_model_noops_with_notification` sees `queued_ready == {"omlx/a": False}` (old unconditional toggle); `test_r_twice_cancels_queued_flip_with_notification` may already pass by accident under the old pop-cancel logic without a notification, but the no-op-for-local-artifact-target-False test is the real failure to fix.

- [ ] **Step 3: Implement**

In `modelman/src/modelman/screens/models.py`, replace `action_toggle_ready`:

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
        persisted_ready = self.state.get(mid).ready
        displayed_ready = self.queued_ready.get(mid, persisted_ready)
        target = not displayed_ready
        if target == persisted_ready:
            # Repeated keypress: this target is exactly what's already on
            # disk once any queued flip is dropped. Cancel it instead of
            # re-queuing a no-op.
            self.queued_ready.pop(mid, None)
            self.app.notify(f"Model already {'ready' if target else 'not ready'}")
            self._refresh_pending_bar()
            self.reload()
            return
        try:
            provider_entry = self.registry.provider(entry.provider_id)
        except KeyError:
            provider_entry = None
        if target is False and model_has_local_artifact(entry, provider_entry):
            # Reconcile is the only writer of ready=False for
            # local-artifact models — the file is still on disk, so
            # flipping the flag here would be invisible the moment the
            # next reconcile re-syncs it back to True (the original
            # reported bug).
            self.app.notify(
                "Reconcile controls local-model ready state; delete the file to mark not ready."
            )
            return
        self.queued_ready[mid] = target
        self._last_provider_used = entry.provider_id
        self._refresh_pending_bar()
        self.reload()
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -v`
Expected: all pass. Pay attention to `test_discard_reverts_immediately_saved_registry_edit` (it presses `x` expecting `queued_ready` to be populated — that test predates the binding swap and must already have been updated to press `r` in Task 3/4's runs; if it still presses `x`, update it now to press `r`, since `x` is toggle-expose as of Task 3).

- [ ] **Step 5: Commit**

```bash
cd modelman
git add src/modelman/screens/models.py tests/screens/test_models.py
git commit -m "$(cat <<'EOF'
feat(modelman): toggle-ready no-ops for local-artifact target=False - completes plan item #5

Toggling ready off on a model whose files are still on disk used to be
invisible: the flag flipped to False, but the very next reconcile
(now a state writer, per the previous two commits) would flip it right
back. Local-artifact models now refuse that direction with a
notification pointing at reconcile/delete-the-file instead. A repeated
keypress that would cancel a still-unapplied queued flip now notifies
instead of silently toggling.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_016a1L2LG8cvJt7vnbgAnnLC
EOF
)"
```

---

### Task 6: `action_toggle_expose` — cascade to ready

**Files:**
- Modify: `modelman/src/modelman/screens/models.py`
- Test: `modelman/tests/screens/test_models.py`

**Interfaces:**
- Consumes: `provider_policy` (existing import from `..litellm`), `self._is_ready` (Task 4), `self.queued_ready`/`self.queued_exposes` (existing).
- Produces: `action_toggle_expose()` behavior — bound to `x` since Task 3.

- [ ] **Step 1: Write the failing tests**

Add to `modelman/tests/screens/test_models.py`:

```python
@pytest.mark.asyncio
async def test_x_on_not_ready_model_cascades_ready_and_expose(tmp_path, monkeypatch):
    """x on a not-ready model must queue BOTH ready=True and
    exposed=True — the cascade that replaces the old 'must be ready
    before exposing' refusal."""
    stub = _seed_cloud_family(tmp_path, monkeypatch, location="cloud")
    stub.is_downloaded.return_value = False

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.press("x")
        await pilot.pause()

        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)
        assert app.screen.queued_exposes == {"ollama/glm-5.2:cloud": True}
        assert app.screen.queued_ready == {"ollama/glm-5.2:cloud": True}


@pytest.mark.asyncio
async def test_x_on_ready_model_queues_only_expose(tmp_path, monkeypatch):
    """x on an already-ready model must not touch queued_ready at all —
    no gratuitous re-download."""
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry

    model = ModelEntry(
        id="ollama/a", family="ornith", provider_id="ollama", model_name="a", location="cloud",
    )
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        families=[FamilyEntry(name="ornith")],
        models=[model],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True))
    save_state(state, state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = None
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.pause()  # let reconcile settle
        await pilot.press("x")
        await pilot.pause()

        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)
        assert app.screen.queued_exposes == {"ollama/a": True}
        assert app.screen.queued_ready == {}


@pytest.mark.asyncio
async def test_x_twice_cancels_queued_expose_with_notification(tmp_path, monkeypatch):
    """Pressing x twice cancels the queued expose flip (target returns to
    the persisted litellm_exposed value) with a notification, mirroring
    the ready toggle's repeated-keypress behavior."""
    stub = _seed_cloud_family(tmp_path, monkeypatch, location="cloud")
    stub.is_downloaded.return_value = True

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.press("x")
        await pilot.pause()
        assert app.screen.queued_exposes == {"ollama/glm-5.2:cloud": True}
        await pilot.press("x")
        await pilot.pause()
        assert app.screen.queued_exposes == {}


@pytest.mark.asyncio
async def test_x_on_provider_with_no_litellm_mapping_notifies(tmp_path, monkeypatch):
    """The 'no LiteLLM mapping' gate is unchanged by this task — only the
    'must be ready' gate is dropped."""
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry

    model = ModelEntry(
        id="mystery/a", family="ornith", provider_id="mystery", model_name="a",
    )
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="mystery", name="Mystery", auth=AuthConfig(type="none"))],
        families=[FamilyEntry(name="ornith")],
        models=[model],
    )
    save_registry(reg, reg_path)
    save_state(StateStore(), state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    stub = MagicMock()
    stub.name = "mystery"
    stub.is_downloaded.return_value = False
    stub.size_of.return_value = None
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.press("x")
        await pilot.pause()

        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)
        assert app.screen.queued_exposes == {}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -k "cascades_ready_and_expose or queues_only_expose or cancels_queued_expose or no_litellm_mapping_notifies" -v`
Expected: FAIL — `test_x_on_not_ready_model_cascades_ready_and_expose` sees `app.screen.queued_exposes == {}` (old code notifies "must be ready" and returns before queuing anything).

- [ ] **Step 3: Implement**

In `modelman/src/modelman/screens/models.py`, replace `action_toggle_expose`:

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
        if provider_policy(entry.provider_id) is None:
            self.app.notify("Provider has no LiteLLM mapping — cannot expose")
            return
        persisted_exposed = self.state.get(mid).litellm_exposed
        displayed_exposed = self.queued_exposes.get(mid, persisted_exposed)
        target = not displayed_exposed
        if target == persisted_exposed:
            self.queued_exposes.pop(mid, None)
            self.app.notify(f"Model already {'exposed' if target else 'not exposed'}")
            self._refresh_pending_bar()
            self.reload()
            return
        self.queued_exposes[mid] = target
        if target and not self._is_ready(mid):
            # Cascade: exposing a not-ready model must download/pull it
            # first. apply() already runs the ready loop before the
            # expose loop, so queuing both here gives the right order.
            self.queued_ready[mid] = True
        self._refresh_pending_bar()
        self.reload()
```

Remove the now-obsolete "Model must be ready before exposing" gate (it was the `if not self._is_ready(mid): self.app.notify(...); return` block that used to sit right after the entry lookup — confirm it's gone from the replaced method above).

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -v`
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
cd modelman
git add src/modelman/screens/models.py tests/screens/test_models.py
git commit -m "$(cat <<'EOF'
feat(modelman): toggle-expose cascades to ready when needed - completes plan item #6

Exposing a not-ready model now queues a ready=True flip alongside the
expose flip instead of refusing with 'Model must be ready before
exposing' — apply() already runs the ready loop before the expose loop
(download/pull, then add to litellm), so the cascade needs no queue.py
change. The no-LiteLLM-mapping gate is unchanged.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_016a1L2LG8cvJt7vnbgAnnLC
EOF
)"
```

---

### Task 7: Pin the ready-before-expose cascade ordering in `queue.py`'s tests

**Files:**
- Test: `modelman/tests/test_queue.py`

**Interfaces:**
- Consumes: `PendingChanges` (unchanged — this task adds coverage, no source change).

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/test_queue.py` (near the other ready/expose tests, e.g. after `test_apply_runs_expose_changes`):

```python
def test_apply_cascade_downloads_before_exposing(tmp_path):
    """The toggle-expose cascade (ModelScreen.action_toggle_expose) relies
    on apply() running the ready loop before the expose loop so a
    not-ready model is downloaded/pulled before its LiteLLM model_list
    entry is written. Pin that ordering here at the PendingChanges level
    so a future reorder in apply() cannot silently break the cascade."""
    reg, state, reg_path, state_path, providers, a, b = _setup_apply_test(tmp_path)
    providers["ollama"].download.return_value = str(tmp_path / "new-a")
    litellm_path = tmp_path / "litellm.yaml"
    litellm_path.write_text("model_list: []\n")

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers=providers,
        ready=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="f:a"), True)],
        exposes=[("ollama/a", True)],
        litellm_path=litellm_path,
    )
    events: list[str] = []
    pending.apply(on_event=events.append)

    download_done = next(i for i, e in enumerate(events) if e.startswith("download:done"))
    expose_start = next(i for i, e in enumerate(events) if e.startswith("expose:start"))
    assert download_done < expose_start
    assert state.models["ollama/a"].ready is True
```

- [ ] **Step 2: Run the test**

Run: `cd modelman && uv run pytest tests/test_queue.py -k cascade_downloads_before_exposing -v`
Expected: PASS immediately — `apply()` already orders ready before exposes (queue.py needs no change per design §5). This step confirms the plan's "no functional change" claim rather than driving new implementation; if it fails, that is a signal `apply()`'s step ordering has drifted from what Tasks 5/6 assume, and `queue.py` needs a real fix before proceeding — stop and re-examine `PendingChanges.apply()`'s step order.

- [ ] **Step 3: Commit**

```bash
cd modelman
git add tests/test_queue.py
git commit -m "$(cat <<'EOF'
test(modelman): pin ready-before-expose cascade ordering - completes plan item #7

No PendingChanges.apply() change needed — it already runs the ready
loop before the expose loop. This test makes that ordering an explicit
contract the toggle-expose cascade (previous commit) depends on.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_016a1L2LG8cvJt7vnbgAnnLC
EOF
)"
```

---

### Task 8: Update documentation for the new keybindings

**Files:**
- Modify: `modelman/CLAUDE.md`
- Modify: `modelman/README.md`
- Modify: `docs/guides/02-providers-and-models.md`
- Modify: `docs/guides/03-model-families.md`
- Modify: `docs/guides/04-litellm-config.md`

**Interfaces:** None — prose only, no code.

- [ ] **Step 1: Update `modelman/CLAUDE.md`**

In the "Textual TUI" section's `src/modelman/screens/models.py` bullet, replace every mention of the old keybindings and the reconciled-overlay description. Find:

```
`d` queue delete (works on any model — the old not-ready gate is gone; apply skips the on-disk removal if `provider.is_downloaded()` reports False), `x` toggle ready, `l` toggle LiteLLM expose, `r` reconcile, `escape` back/apply.
```

Replace with:

```
`d` queue delete (works on any model — the old not-ready gate is gone; apply skips the on-disk removal if `provider.is_downloaded()` reports False), `r` toggle ready (no-op with a notification for a local-artifact model already on disk — reconcile is the only writer of `ready=False` for those; delete the file instead), `x` toggle exposed (cascades a ready=True queue when the model isn't ready yet), `escape` back/apply.
```

Find the sentence after that about the background worker:

```
Both `FamilyScreen` and `ModelScreen` run a background worker (`_run_reconcile`, `thread=True`) on mount, on `on_screen_resume`, and via the `r` binding: it asks each provider whether a variant is actually on disk and overlays fresh size/downloaded values into an in-memory cache, independent of `modelman.toml` and the `sync` CLI command. This is why the SIZE/DOWNLOADED columns can differ from `state.py`'s stored values until a reconcile runs.
```

Replace with:

```
Both `FamilyScreen` and `ModelScreen` run a background worker (`_run_reconcile`, `thread=True`) on mount and on `on_screen_resume` — there is no manual reconcile binding; it runs automatically. For a local-artifact model (per `registry.model_has_local_artifact` — driven by `ModelEntry.location`/`ProviderEntry.location`, not a hard-coded provider list), the worker writes `state.ready`/`disk_path`/`size_bytes` directly from what the provider reports; for a cloud-located or cloud-provider model, `ready` is left alone (only `disk_path`/`size_bytes` are opportunistically updated) since reconcile cannot verify a remote model against a local filesystem.
```

Update the "Key Gotchas" bullet on guide staleness (`litellm_exposed` snapshots) — unaffected, leave as-is.

- [ ] **Step 2: Update `modelman/README.md`**

Update the Family screen bullet (find `r` reconcile, `q` quit — 4 occurrences of `` `r` `` reconcile across the file):

```
- **Family screen** — table of families with columns: family · display ·
  variants · downloaded · size. Keys: `a` add, `e` edit display name,
  `d` delete (blocked if anything is downloaded), `enter` open, `r`
  reconcile, `q` quit. While the background size refresh runs after
  mount/resume/`r`, the table is briefly disabled and a
  "Refreshing sizes…" indicator is shown — actions (`a`, `e`, `d`,
  `enter`, `r`) are no-ops during that window so the user can't click
  a row whose contents are about to mutate. The cursor survives the
  refresh: returning from a model screen leaves you on the same family.
```

becomes:

```
- **Family screen** — table of families with columns: family · display ·
  variants · downloaded · size. Keys: `a` add, `e` edit display name,
  `d` delete (blocked if anything is downloaded), `enter` open, `q` quit.
  Reconcile runs automatically on mount and on returning from a model
  screen — there is no manual reconcile key. While the background size
  refresh runs, the table is briefly disabled and a "Refreshing sizes…"
  indicator is shown — actions (`a`, `e`, `d`, `enter`) are no-ops during
  that window so the user can't click a row whose contents are about to
  mutate. The cursor survives the refresh: returning from a model screen
  leaves you on the same family.
```

Update the Model screen bullet:

```
  Keys: `a` add model, `e` edit (id/provider fixed; changing family queues
  a move), `d` queue delete (works on any model — apply skips the on-disk
  removal if the artifact is already gone, but still cleans
  registry/state), `x` toggle ready (queues download/pull for
  reconcilable providers, or a flag flip for cloud/native providers),
  `l` toggle LiteLLM exposure (ready or cloud models), `r` reconcile,
  `enter` edit, `escape` back / apply queue. The cursor survives every
  reload — reconciling or toggling ready on a row leaves you on that
  row. Provider and family dropdowns list options alphabetically; the
  family Select keeps the caller's order when the current family is
  already in the list.
```

becomes:

```
  Keys: `a` add model, `e` edit (id/provider fixed; changing family queues
  a move), `d` queue delete (works on any model — apply skips the on-disk
  removal if the artifact is already gone, but still cleans
  registry/state), `r` toggle ready (queues download/pull for
  reconcilable providers, or a flag flip for cloud/native providers; a
  no-op with a notification if the model is a local artifact that's
  already on disk — reconcile is the only writer of ready=False for
  those, so delete the file instead), `x` toggle exposed (cascades a
  ready=True queue first if the model isn't ready yet), `enter` edit,
  `escape` back / apply queue. Reconcile runs automatically on mount and
  resume — there is no manual reconcile key. The cursor survives every
  reload — reconciling or toggling a row leaves you on that row. Provider
  and family dropdowns list options alphabetically; the family Select
  keeps the caller's order when the current family is already in the
  list.
```

Update the "Expose models through LiteLLM" section:

```
In the TUI, press `l` on a model row to queue an exposure toggle; it applies
on exit alongside downloads/deletes. The EXPOSED column shows `Y` when
exposed (or queued to expose) and `–` otherwise.
```

becomes:

```
In the TUI, press `x` on a model row to queue an exposure toggle; it applies
on exit alongside downloads/deletes (a not-ready model is downloaded/pulled
first). The EXPOSED column shows `Y` when exposed (or queued to expose) and
`–` otherwise.
```

Update the "Native providers" section:

```
Providers whose `auth.type` is `"native"` (or whose id matches an agent in
`~/.config/agent-wt/config.toml`) represent models handled by external
agents (e.g. `claude`, `codex`). They have no download mechanics:
pressing `x` simply toggles the `ready` flag, and there is no disk path or
size. These providers are synced into `registry.toml` automatically on TUI
launch from the wt config.
```

becomes:

```
Providers whose `auth.type` is `"native"` (or whose id matches an agent in
`~/.config/agent-wt/config.toml`) represent models handled by external
agents (e.g. `claude`, `codex`). They have no download mechanics:
pressing `r` simply toggles the `ready` flag, and there is no disk path or
size. These providers are synced into `registry.toml` automatically on TUI
launch from the wt config.
```

- [ ] **Step 3: Update `docs/guides/02-providers-and-models.md`**

Replace the Family/Model screen bullets:

```
- **Family screen** — table of families with columns: family · display · variants · downloaded · size. Keys: `a` add, `e` edit display name, `d` delete (blocked if anything is downloaded), `enter` open, `r` reconcile, `q` quit. The `downloaded` column counts only local models; cloud entries are excluded from both the count and the `size` column.
- **Model screen** — two-pane view: providers on the left, that provider's models on the right (columns: name · status ✓/○/↓/✗ · size · path · exposed). Keys: `a` add model, `e` edit (id/provider fixed), `d` queue delete (downloaded variants only), `x` toggle download (not-downloaded variants only), `l` toggle LiteLLM exposure (downloaded or cloud variants only), `r` reconcile, `enter` edit, `escape` back / apply queue.
```

becomes:

```
- **Family screen** — table of families with columns: family · display · variants · downloaded · size. Keys: `a` add, `e` edit display name, `d` delete (blocked if anything is downloaded), `enter` open, `q` quit. Reconcile runs automatically on mount/resume — no manual key. The `downloaded` column counts only local models; cloud entries are excluded from both the count and the `size` column.
- **Model screen** — single table scoped to one family (columns: family · provider · model · loc · status · exposed · cost · sub · size), with a details panel below showing the row's on-disk path. Keys: `a` add model, `e` edit (id/provider fixed), `d` queue delete (any model — apply skips on-disk removal if the artifact is already gone), `r` toggle ready (queues download/pull; a no-op with a notification for a local-artifact model already on disk — delete the file instead), `x` toggle exposed (cascades a ready toggle first if needed), `enter` edit, `escape` back / apply queue.
```

Update the expose-toggle callout:

```
On success `expose` writes a `model_list` entry into `~/.config/litellm/config.yaml` and flips the model's `litellm_exposed` flag in `modelman.toml`; `unexpose` removes the entry and clears the flag. modelman only touches the `model_list` section — `general_settings` and unrecognized rows are preserved — and restarts LiteLLM itself right after (`MODELMAN_LITELLM_RESTART_CMD`, falling back to `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`; see [01-initial-setup](01-initial-setup.md) §7). In the TUI the same toggle is `l` on a model row (queued, applied on exit; the EXPOSED column shows `L`).
```

becomes:

```
On success `expose` writes a `model_list` entry into `~/.config/litellm/config.yaml` and flips the model's `litellm_exposed` flag in `modelman.toml`; `unexpose` removes the entry and clears the flag. modelman only touches the `model_list` section — `general_settings` and unrecognized rows are preserved — and restarts LiteLLM itself right after (`MODELMAN_LITELLM_RESTART_CMD`, falling back to `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`; see [01-initial-setup](01-initial-setup.md) §7). In the TUI the same toggle is `x` on a model row (queued, applied on exit — downloads/pulls first if the model isn't ready yet; the EXPOSED column shows `Y`).
```

- [ ] **Step 4: Update `docs/guides/03-model-families.md`**

Update the bash comment:

```
uv run modelman    # family screen: a add, e edit display name, d delete, enter open, r reconcile, q quit
```

becomes:

```
uv run modelman    # family screen: a add, e edit display name, d delete, enter open, q quit (reconcile is automatic)
```

Update the keybindings table (drop the `r`/Reconcile row):

```
| Key | Action |
|-----|--------|
| `a` | Add family |
| `e` | Edit display name |
| `d` | Delete (blocked if anything is downloaded) |
| `enter` | Open family |
| `r` | Reconcile |
| `q` | Quit |
```

becomes:

```
| Key | Action |
|-----|--------|
| `a` | Add family |
| `e` | Edit display name |
| `d` | Delete (blocked if anything is downloaded) |
| `enter` | Open family |
| `q` | Quit |

Reconcile runs automatically on mount and when returning from a model screen — there is no manual key.
```

- [ ] **Step 5: Update `docs/guides/04-litellm-config.md`**

```
TUI — same toggle from the interactive UI (bare `uv run modelman`): press `l` on a model row; it queues the change and applies it on exit; the EXPOSED column shows `L` (see [02-providers-and-models](02-providers-and-models.md)).
```

becomes:

```
TUI — same toggle from the interactive UI (bare `uv run modelman`): press `x` on a model row; it queues the change and applies it on exit (downloading/pulling first if the model isn't ready yet); the EXPOSED column shows `Y` (see [02-providers-and-models](02-providers-and-models.md)).
```

- [ ] **Step 6: Verify links and grep for stragglers**

Run: `grep -rn "toggle LiteLLM\|Toggle LiteLLM\|\`l\` toggle\|\`x\` toggle ready\|\`x\` toggle download\|action_reconcile" modelman/README.md modelman/CLAUDE.md docs/guides/*.md`
Expected: no output (all stale references updated).

Run: `cd modelman && make check-links` (or `bin/check-links` from repo root, per root `CLAUDE.md`)
Expected: no broken links reported.

- [ ] **Step 7: Commit**

```bash
git add modelman/CLAUDE.md modelman/README.md docs/guides/02-providers-and-models.md docs/guides/03-model-families.md docs/guides/04-litellm-config.md
git commit -m "$(cat <<'EOF'
docs(modelman): update keybinding docs for r/x toggle swap

Reconcile is automatic now (no manual key); r toggles ready, x toggles
exposed (was x/l respectively). Matches the previous six commits'
behavior change.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_016a1L2LG8cvJt7vnbgAnnLC
EOF
)"
```

---

### Task 9: Full verification

**Files:** None (verification only).

- [ ] **Step 1: Run the full modelman test suite**

Run: `cd modelman && make test` (or `uv run pytest -k "not screen" -q` for the fast ~3 min subset, then a separate `uv run pytest -k screen -q` pass for the Textual screen tests per `modelman/CLAUDE.md`'s testing-patterns guidance — screen tests are slow and best run standalone).
Expected: 0 failures.

- [ ] **Step 2: Run lint + typecheck**

Run: `cd modelman && make check`
Expected: 0 errors.

- [ ] **Step 3: Run the repo-wide aggregate**

Run: `make test-all` (from the repo root — lints shell scripts, runs modelman `make check`/`make test`, runs `wt`'s Go build/vet/test).
Expected: 0 failures. `wt` reads `registry.toml`/`modelman.toml` read-only and this plan adds no new registry/state fields, so no `wt`-side change is expected — this step confirms that assumption.

- [ ] **Step 4: Manual smoke check (optional but recommended given this is a TUI bug fix)**

If a local llamacpp/omlx model with files on disk and a stale `ready=false` in `~/.config/local-ai/modelman.toml` is available, run `uv run modelman`, open its family, and confirm the row shows `✓` immediately on mount (no `r` press needed) — this is the exact bug from the design doc's problem statement.

- [ ] **Step 5: Final commit (only if Steps 1-3 required fixes)**

If any fixes were needed to pass `make test-all`, commit them separately with a scope tag (not a plan-item reference, since this task is verification, not new plan work):

```bash
git add -A
git commit -m "$(cat <<'EOF'
fix(modelman): address make test-all regressions from toggle-ready rework

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_016a1L2LG8cvJt7vnbgAnnLC
EOF
)"
```

If Steps 1-3 pass cleanly with no fixes needed, this task requires no commit — the plan is complete as of Task 8's commit.
