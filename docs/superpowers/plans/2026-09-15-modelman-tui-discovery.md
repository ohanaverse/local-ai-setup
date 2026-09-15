# modelman TUI Discovered-Model Surfacing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make modelman's `models` TUI screen show on-disk-but-unregistered local models (e.g. an mtplx or omlx model downloaded outside modelman) and let the user register one in place, instead of it being silently invisible.

**Architecture:** Reuse `local_control.py`'s existing CLI-only discovery machinery (`_provider_local_models`/`_discovered_models`) behind one new public function. `ModelScreen`'s existing on-mount background worker calls it and renders extra synthetic table rows. Selecting a discovered row opens the existing `ModelForm` add dialog in a new locked-prefill mode that writes `ready` state directly instead of queuing a download.

**Tech Stack:** Python 3.13, Textual (TUI), `uv run pytest` / `pytest-asyncio` (`ModelmanApp.run_test()` + pilot).

**Spec:** `docs/superpowers/specs/2026-09-15-modelman-tui-discovery-design.md`

## Global Constraints

- Discovery only covers providers where `Provider.supports_discovery` is `True` — today `ollama`, `omlx`, `omlx-6bit`, `mtplx`. `mlx_lm_server` and retired `llamacpp` are already excluded by that flag; no new provider-id logic needed.
- No batch "register everything" action and no new CLI command — registration stays one-model-at-a-time, user-initiated, exactly like the existing `modelman start <name>` auto-register flow.
- Registering a discovered model must never populate `ModelScreen.queued_ready` — it must write `state.ready/disk_path/size_bytes` directly (the artifact is already on disk; queuing a ready-on would call `provider.download()` at apply time, which fails for omlx's bare-basename discovered names since HF rejects a repo id with no org segment).
- A `ModelEntry` created via discovered-registration must have `source="discovered"` (not the `"curated"` every other add produces), matching the CLI's `_register_discovered_model` convention.
- Discovered-registration's Model id is always `f"{provider_id}/{variant_id.replace('/', '--')}"` (escaped, uniformly — no mtplx exception), matching `local_control.py`'s existing `_register_discovered_model`.

---

## File Structure

- Modify `modelman/src/modelman/local_control.py`: add `discover_unregistered_models(registry) -> list[DiscoveredModel]`, a thin public wrapper around the existing `_provider_local_models`/`_discovered_models` helpers.
- Modify `modelman/src/modelman/screens/forms.py`: `ModelForm` gains a `discovered: DiscoveredModel | None` constructor parameter that locks the Provider/Model fields and bypasses `parse_model()` on submit; `ModelFormResult` gains a `source: str = "curated"` field.
- Modify `modelman/src/modelman/screens/models.py`: `ModelScreen` gains `self.discovered`/`self._discovered_by_key`, the on-mount reconcile worker also calls `discover_unregistered_models`, the table renders synthetic discovered rows, and a new register-dialog flow (`_current_discovered`, `_open_register_dialog`, `_on_register_discovered`) wires into the existing Enter/`e` edit binding. `_variant_to_model_entry` gains a `source` parameter; `_on_add_model` is refactored to share a new `_append_new_model_entry` helper with `_on_register_discovered`.
- Tests: `modelman/tests/test_local_control.py`, `modelman/tests/screens/test_forms.py`, `modelman/tests/screens/test_models.py` — extend each with the new behavior's tests, following each file's existing fixture conventions.

---

### Task 1: `discover_unregistered_models()` — public discovery entry point

**Files:**
- Modify: `modelman/src/modelman/local_control.py` (add function after `_discovered_models`, which ends around line 492)
- Test: `modelman/tests/test_local_control.py`

**Interfaces:**
- Produces: `discover_unregistered_models(registry: Registry) -> list[DiscoveredModel]` — every in-scope local provider's on-disk artifact with no matching `registry.toml` entry. Reused by Task 3 (`ModelScreen`'s discovery worker).

- [ ] **Step 1: Write the failing tests**

Add to `modelman/tests/test_local_control.py`. First, add `discover_unregistered_models` to the existing `from modelman.local_control import (...)` block near the top of the file (alongside `DiscoveredModel`, `inventory_local_models`, etc.). Then add these two tests near the existing `test_inventory_discovered_bucket_excludes_already_registered` (they reuse that test's `_listing_registry()`/`_patch_provider_local_models()` helpers, already defined in this file):

```python
def test_discover_unregistered_models_excludes_already_registered():
    # Mirrors inventory_local_models's discovered bucket, but through the
    # standalone entry point the TUI calls (it doesn't need the registered-
    # presence half of a full inventory, only the discovery half).
    registry = _listing_registry()
    mapping = {
        "ollama": [
            {"variant_id": "exposed-model", "path": "ollama:exposed-model", "size_bytes": 1},
            {
                "variant_id": "brand-new-model",
                "path": "ollama:brand-new-model",
                "size_bytes": 2_000_000_000,
            },
        ]
    }
    with _patch_provider_local_models(mapping):
        discovered = discover_unregistered_models(registry)
    assert discovered == [
        DiscoveredModel(
            provider_id="ollama",
            variant_id="brand-new-model",
            path="ollama:brand-new-model",
            size_bytes=2_000_000_000,
        )
    ]


def test_discover_unregistered_models_empty_when_nothing_new():
    # Every provider-reported artifact already has a registry.toml entry —
    # nothing should be offered for registration.
    registry = _listing_registry()
    mapping = {
        "ollama": [
            {"variant_id": "exposed-model", "path": "ollama:exposed-model", "size_bytes": 1},
        ]
    }
    with _patch_provider_local_models(mapping):
        discovered = discover_unregistered_models(registry)
    assert discovered == []
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd modelman && uv run pytest tests/test_local_control.py -k discover_unregistered -v`
Expected: FAIL with `ImportError: cannot import name 'discover_unregistered_models'`

- [ ] **Step 3: Implement `discover_unregistered_models`**

In `modelman/src/modelman/local_control.py`, add this function immediately after `_discovered_models` (which currently ends right before `_find_discovered`):

```python
def discover_unregistered_models(registry: Registry) -> list[DiscoveredModel]:
    """Every on-disk artifact from an in-scope local provider with no
    matching registry.toml entry — the standalone entry point the TUI's
    models screen uses to surface discovered models. Reuses the same
    enumeration and name-matching logic `modelman start`'s no-arg
    inventory listing already relies on
    (_provider_local_models/_discovered_models), so the TUI never needs
    its own provider-scanning code.
    """
    local_map, _unqueryable = _provider_local_models(registry)
    return _discovered_models(registry, local_map)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd modelman && uv run pytest tests/test_local_control.py -k discover_unregistered -v`
Expected: PASS (2 tests)

- [ ] **Step 5: Commit**

```bash
cd modelman
git add src/modelman/local_control.py tests/test_local_control.py
git commit -m "feat(modelman): add discover_unregistered_models() public entry point"
```

---

### Task 2: `ModelForm` discovered-registration mode

**Files:**
- Modify: `modelman/src/modelman/screens/forms.py`
- Test: `modelman/tests/screens/test_forms.py`

**Interfaces:**
- Consumes: `DiscoveredModel` (from `modelman.local_control`, fields `provider_id: str`, `variant_id: str`, `path: str`, `size_bytes: int | None`) — Task 1's output type.
- Produces: `ModelForm(..., discovered: DiscoveredModel | None = None)` constructor parameter; `ModelFormResult` gains `source: str = "curated"`. Consumed by Task 4 (`ModelScreen._open_register_dialog`/`_on_register_discovered`).

- [ ] **Step 1: Write the failing tests**

Add to `modelman/tests/screens/test_forms.py`, near the other "Submit behavior" tests (after `test_submit_hf_repo_only_produces_correct_spec` is a good spot). These use the file's existing `ModelmanApp()` + `app.push_screen(form, _capture)` + `_submit(app, pilot)` pattern (the `_submit` helper already handles a disabled `#model` Input by focusing `#save` instead):

```python
@pytest.mark.asyncio
async def test_modelform_discovered_mode_locks_provider_and_model():
    from modelman.local_control import DiscoveredModel

    discovered = DiscoveredModel(
        provider_id="omlx",
        variant_id="Qwen3.8-27B-4bit",
        path="/Users/keith/.omlx/models/Qwen3.8-27B-4bit",
        size_bytes=123,
    )
    form = ModelForm(
        providers=["omlx", "ollama"],
        discovered=discovered,
        families=["qwen3.8"],
        family=None,
        provider_kinds={"omlx": "local-only", "ollama": "ollama"},
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        provider_sel = app.screen.query_one("#provider-select", Select)
        model_input = app.screen.query_one("#model", Input)
        assert provider_sel.value == "omlx"
        assert provider_sel.disabled is True
        assert model_input.value == "Qwen3.8-27B-4bit"
        assert model_input.disabled is True


@pytest.mark.asyncio
async def test_modelform_discovered_mode_submits_without_parse_model():
    # The regression this guards: omlx's discovered variant_id has no
    # '/' (a bare directory basename), which parse_model() rejects for
    # omlx outright ("model must be 'org/repo'"). Registering a
    # discovered model must never route that value through parse_model().
    from modelman.local_control import DiscoveredModel

    discovered = DiscoveredModel(
        provider_id="omlx",
        variant_id="Qwen3.8-27B-4bit",
        path="/Users/keith/.omlx/models/Qwen3.8-27B-4bit",
        size_bytes=123,
    )
    form = ModelForm(
        providers=["omlx"],
        discovered=discovered,
        families=["qwen3.8"],
        family="qwen3.8",
        provider_kinds={"omlx": "local-only"},
    )
    dismissed: list = []

    def _capture(result):
        dismissed.append(result)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, _capture)
        await pilot.pause()
        await _submit(app, pilot)
        await pilot.pause()

    assert dismissed, "form did not dismiss"
    result = dismissed[0]
    assert result is not None
    assert result.spec["id"] == "omlx/Qwen3.8-27B-4bit"
    assert result.spec["provider"] == "omlx"
    assert result.spec["name"] == "Qwen3.8-27B-4bit"
    assert result.spec["repo"] == "Qwen3.8-27B-4bit"
    assert result.spec["location"] == "local"
    assert result.family == "qwen3.8"
    assert result.source == "discovered"


@pytest.mark.asyncio
async def test_modelform_normal_add_still_defaults_source_curated():
    # Every existing add/edit path must keep producing "curated" — only
    # the new discovered path should ever produce "discovered".
    form = ModelForm(providers=["ollama"], variant=None, default_provider="ollama")
    dismissed: list = []

    def _capture(result):
        dismissed.append(result)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, _capture)
        await pilot.pause()
        _fill_model(app, "ornith-1.5:35b")
        await pilot.pause()
        await _submit(app, pilot)
        await pilot.pause()

    assert dismissed[0].source == "curated"
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd modelman && uv run pytest tests/screens/test_forms.py -k discovered_mode -v`
Expected: FAIL — `ModelForm.__init__()` raises `TypeError: unexpected keyword argument 'discovered'`

- [ ] **Step 3: Implement the `discovered` mode**

In `modelman/src/modelman/screens/forms.py`:

Add the import (with the other relative imports near the top of the file, after `from ..ollama_caps import auto_detect_model_info`):

```python
from ..local_control import DiscoveredModel
```

Add `source` to `ModelFormResult`:

```python
class ModelFormResult(NamedTuple):
    """ModelForm's dismiss payload: the VariantSpec plus the family the
    user chose. Family is deliberately separate from VariantSpec — the
    spec dict is the provider-facing contract and has no family field;
    ModelScreen maps family onto ModelEntry.family.
    """

    spec: VariantSpec
    family: str
    pricing_updated_at: str | None = None
    source: str = "curated"
```

In `ModelForm.__init__`, add the `discovered` parameter and store it:

```python
    def __init__(
        self,
        providers: list[str],
        variant: VariantSpec | None = None,
        default_provider: str | None = None,
        families: list[str] | None = None,
        family: str | None = None,
        provider_kinds: dict[str, str] | None = None,
        pricing_updated_at: str | None = None,
        discovered: DiscoveredModel | None = None,
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
        self._no_real_families = families is not None and not families and family is None
        self._pricing_updated_at = pricing_updated_at
        # Set only for "register this on-disk-but-unregistered artifact"
        # mode: locks Provider/Model to what the provider's own
        # filesystem scan reported (see _submit_discovered) instead of
        # the normal free-text add flow.
        self._discovered = discovered
```

(Only the last line and the new parameter are additions — the rest of `__init__`'s body is unchanged; shown in full so the diff context is unambiguous.)

In `compose()`, change the `initial_provider`/`model_val` derivation (the existing `if editing: ... elif self._default_provider ...` block near the top of the method) to:

```python
        editing = self._variant is not None
        v: VariantSpec = self._variant if self._variant is not None else cast("VariantSpec", {})
        if editing:
            initial_provider = v.get("provider") or self._providers[0]
        elif self._discovered is not None:
            initial_provider = self._discovered.provider_id
        elif self._default_provider and self._default_provider in self._providers:
            initial_provider = self._default_provider
        else:
            initial_provider = self._providers[0]
        self._initial_provider: str = initial_provider

        model_val = (
            self._reconstruct_model(v)
            if editing
            else self._discovered.variant_id
            if self._discovered is not None
            else ""
        )
```

Further down in `compose()`, change the Provider and Model field `disabled=` expressions from `disabled=editing` to lock for discovered mode too:

```python
            yield Select(
                options=[(p, p) for p in self._providers],
                value=initial_provider,
                allow_blank=False,
                disabled=editing or self._discovered is not None,
                id="provider-select",
            )
            yield Label("Model:", id="model-label")
            yield Input(
                value=model_val,
                placeholder=placeholder,
                disabled=editing or self._discovered is not None,
                id="model",
            )
```

(These are the same `Select`/`Input` blocks already in `compose()` — only the `disabled=` line of each changes, from `disabled=editing` to `disabled=editing or self._discovered is not None`.)

In `_modal_on_mount()`, add a branch so the initial focus lands on a real editable field (the Provider Select is disabled in discovered mode, so focusing it is a no-op):

```python
    def _modal_on_mount(self) -> None:
        new_family_selected = (
            self._variant is None
            and self.query_one("#family-select", Select).value == NEW_FAMILY_VALUE
        )
        if new_family_selected:
            self.query_one("#new-family-input", Input).focus()
        elif self._discovered is not None:
            self.query_one("#family-select", Select).focus()
        elif self._variant is None:
            self.query_one("#provider-select", Select).focus()
        else:
            self.query_one("#per-token-checkbox", Checkbox).focus()
        # (rest of the method unchanged)
```

In `on_select_changed()`, extend the existing edit-mode guard so a discovered-mode mount doesn't run the provider-change side effects either (harmless today since it would just re-derive the same already-pinned provider, but this keeps the "provider is locked" invariant explicit rather than incidental):

```python
        if event.select.id != "provider-select":
            return
        if self._variant is not None or self._discovered is not None:
            return
```

(This replaces the existing `if self._variant is not None: return` line — same location, in `on_select_changed`.)

Change `_submit()` to dispatch to a new discovered-mode submit path first:

```python
    def _submit(self) -> None:
        if self._discovered is not None:
            self._submit_discovered()
            return
        provider = str(self.query_one("#provider-select", Select).value)
        # (rest of the method unchanged)
```

Add the new method (place it right after `_submit`, before `_submit_dual_model`):

```python
    def _submit_discovered(self) -> None:
        """Register a discovered (on-disk, unregistered) artifact.

        Provider and Model are pinned to what the provider's own
        filesystem scan reported (DiscoveredModel) rather than routed
        through parse_model()'s free-text 'org/repo' validation — that
        scan already verified the artifact's identity, and for omlx in
        particular the reported name is a bare directory basename with
        no HF org segment, which parse_model() would reject outright.
        Id/repo derivation mirrors local_control.py's
        _register_discovered_model exactly, so a model registered from
        the TUI resolves identically to one registered via `modelman
        start <name>`.
        """
        assert self._discovered is not None
        discovered = self._discovered

        try:
            cost = self._parse_cost_from_fields()
        except ValueError as exc:
            self._show_error(str(exc))
            return
        self._clear_error()

        quantization = self.query_one("#quantization", Input).value.strip() or None
        vid = f"{discovered.provider_id}/{discovered.variant_id.replace('/', '--')}"
        spec: VariantSpec = {
            "id": vid,
            "provider": discovered.provider_id,
            "name": discovered.variant_id,
            "repo": discovered.variant_id,
            "local_path": None,
            "files": None,
            "quantizations": None,
            "location": "local",
            "cost": _cost_to_dict(cost) if cost is not None else None,
            "quantization": quantization,
            "model_info": None,
        }
        family = self._resolve_family()
        if family is None:
            return
        self.dismiss(
            ModelFormResult(
                spec=spec, family=family, pricing_updated_at=None, source="discovered"
            )
        )
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd modelman && uv run pytest tests/screens/test_forms.py -k "discovered_mode or normal_add_still_defaults" -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Run the full forms test file to check for regressions**

Run: `cd modelman && uv run pytest tests/screens/test_forms.py -q`
Expected: PASS, no regressions from the `disabled=`/`on_select_changed`/`_submit` edits

- [ ] **Step 6: Commit**

```bash
cd modelman
git add src/modelman/screens/forms.py tests/screens/test_forms.py
git commit -m "feat(modelman): add ModelForm discovered-registration mode"
```

---

### Task 3: `ModelScreen` — discovery worker + rendering

**Files:**
- Modify: `modelman/src/modelman/screens/models.py`
- Test: `modelman/tests/screens/test_models.py`

**Interfaces:**
- Consumes: `discover_unregistered_models` (Task 1), `DiscoveredModel` (Task 1's type).
- Produces: `ModelScreen.discovered: list[DiscoveredModel]`, `ModelScreen._discovered_by_key: dict[str, DiscoveredModel]` (rebuilt on every `_load_models()` call, row key format `f"discovered:{provider_id}:{variant_id}"`). Consumed by Task 4.

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/screens/test_models.py`, near `test_reconcile_sets_state_ready_for_local_artifact_omlx_model` (same file already imports `DataTable`, `ModelmanApp`, the registry/state helpers, and has the `_seed_registry_and_state`/`_open_model_screen` helpers used below):

```python
@pytest.mark.asyncio
async def test_discovered_model_renders_as_synthetic_row(tmp_path, monkeypatch):
    """An on-disk artifact with no registry.toml entry (e.g. an mtplx
    model pulled outside modelman) must show up in the models screen so
    the user can see and register it, instead of being silently
    invisible — this is the bug the feature exists to fix."""
    from modelman.local_control import DiscoveredModel
    from modelman.screens import models as models_module

    _seed_registry_and_state(tmp_path, monkeypatch)
    monkeypatch.setattr(
        models_module,
        "discover_unregistered_models",
        lambda registry: [
            DiscoveredModel(
                provider_id="mtplx",
                variant_id="Youssofal/Qwen3.8-27B-MTPLX",
                path="/Users/keith/.mtplx/models/Youssofal--Qwen3.8-27B-MTPLX",
                size_bytes=5_000_000_000,
            )
        ],
    )

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        await pilot.pause()  # let the reconcile+discover worker settle

        row_key = "discovered:mtplx:Youssofal/Qwen3.8-27B-MTPLX"
        assert row_key in app.screen._discovered_by_key

        mt = app.screen.query_one("#model-table", DataTable)
        last_row = [str(c) for c in mt.get_row_at(mt.row_count - 1)]
        assert last_row[1] == "mtplx"
        assert last_row[2] == "Youssofal/Qwen3.8-27B-MTPLX"

        # Cursor on the discovered row must show its on-disk path.
        mt.move_cursor(row=mt.row_count - 1)
        await pilot.pause()
        details = app.screen.query_one("#details-panel", Static)
        assert "Youssofal--Qwen3.8-27B-MTPLX" in str(details.render())
        assert "unregistered" in str(details.render())
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -k discovered_model_renders -v`
Expected: FAIL — `AttributeError: 'ModelScreen' object has no attribute '_discovered_by_key'` (or an `ImportError`/`AttributeError` on `discover_unregistered_models` not existing in `models_module`)

- [ ] **Step 3: Wire discovery into `ModelScreen`**

In `modelman/src/modelman/screens/models.py`:

Update the `from ..local_control import (...)` block (near the top of the file) to add `discover_unregistered_models`:

```python
from ..local_control import (
    LocalControlError,
    discover_unregistered_models,
    running_model_ids,
    same_provider_occupant,
    start_local_model,
    stop_local_model,
)
```

Update the `if TYPE_CHECKING:` block to also import `DiscoveredModel` for typing:

```python
if TYPE_CHECKING:
    from ..local_control import DiscoveredModel
    from ..providers.base import VariantSpec
```

In `ModelScreen.__init__`, add the new state right after the existing `self._ready_cascade_for_expose: set[str] = set()` line (before `self.queued_moves`):

```python
        # On-disk artifacts an in-scope local provider reports with no
        # matching registry.toml entry (populated by the on-mount
        # discovery worker — see _run_reconcile). Rendered as extra
        # synthetic rows in the table; _discovered_by_key is rebuilt
        # from scratch on every _repopulate() call, keyed by a synthetic
        # row key ("discovered:<provider>:<variant>"), never a real
        # ModelEntry id.
        self.discovered: list[DiscoveredModel] = []
        self._discovered_by_key: dict[str, DiscoveredModel] = {}
```

Extend `_run_reconcile` to also populate `self.discovered`, right before the final `self.app.call_from_thread(self.reload)` line:

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

        Delegates to the shared reconcile_model_state (screens/__init__.py).

        Also asks each in-scope local provider what's on disk that has
        no registry.toml entry at all (discover_unregistered_models,
        local_control.py) and stores the result on self.discovered —
        the same background worker, not a second one, since both calls
        are read-only provider queries with no reason to run
        concurrently.
        """
        reconcile_model_state(self.registry.models, self.registry, self.state)
        # Self-heal the running flag the same way ready/disk_path already
        # are: a model flagged running whose process actually died (crash,
        # manual kill outside modelman) must not keep showing RUNNING=●
        # forever. running_model_ids() re-probes every flagged model and
        # clears any stale flag as a side effect; anything it doesn't
        # return is not verified running, so it gets cleared here too.
        verified = set(running_model_ids(self.registry, self.state, self.state_path))
        for model_id, model_state in list(self.state.models.items()):
            if model_state.running and model_id not in verified:
                self.state.models[model_id] = replace(model_state, running=False)
        self.discovered = discover_unregistered_models(self.registry)
        # Re-render on the main thread.
        self.app.call_from_thread(self.reload)
```

In `_load_models()`'s inner `_repopulate()` function, add the discovered rows after the existing `for m in self._sorted_models(): ... mt.add_row(...)` loop, and before `reload_preserving_cursor(mt, _repopulate)`:

```python
        def _repopulate() -> None:
            mt.clear()
            for m in self._sorted_models():
                # (existing loop body unchanged — ready/status/exposed_str/
                # running_str computation and the mt.add_row(..., key=m.id)
                # call stay exactly as they are today)
                ...

            self._discovered_by_key = {}
            for d in sorted(self.discovered, key=lambda d: (d.provider_id, d.variant_id)):
                row_key = f"discovered:{d.provider_id}:{d.variant_id}"
                self._discovered_by_key[row_key] = d
                mt.add_row(
                    "—",
                    d.provider_id,
                    d.variant_id,
                    _format_location(LOCATION_LOCAL),
                    "[cyan]+[/cyan]",
                    "–",
                    "-",
                    "-",
                    format_size(d.size_bytes) if d.size_bytes else "—",
                    key=row_key,
                )

        reload_preserving_cursor(mt, _repopulate)
        self._refresh_details_panel(mt.cursor_row)
```

(The `...` above stands for the existing, unmodified loop body — do not delete or rewrite it; only append the new `self._discovered_by_key = {}` block and its `for d in ...` loop after it, still inside `_repopulate()`.)

Update `_refresh_details_panel` to show a discovered row's path:

```python
    def _refresh_details_panel(self, cursor_row: int) -> None:
        """Show the on-disk path of the row under the cursor, from
        state.disk_path; renders an em dash when the model isn't ready
        or its path is unknown. A discovered (unregistered) row shows
        its provider-reported path directly, tagged "(unregistered)" —
        it has no state.disk_path yet since it isn't in the registry.
        """
        try:
            details = self.query_one("#details-panel", Static)
            mt = self.query_one("#model-table", DataTable)
        except NoMatches:
            return  # not mounted (e.g. screen teardown race)
        mid = row_key_at(mt, cursor_row)
        if mid is None:
            details.update("path: —")
            return
        discovered = self._discovered_by_key.get(mid)
        if discovered is not None:
            details.update(Text(f"path: {discovered.path} (unregistered)"))
            return
        path = self.state.get(mid).disk_path if self._is_ready(mid) else None
        details.update(Text(f"path: {path or '—'}"))
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -k discovered_model_renders -v`
Expected: PASS

- [ ] **Step 5: Run the existing reconcile tests to check for regressions**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -k reconcile -v`
Expected: PASS — `discover_unregistered_models` runs against the same patched `ProviderRegistry` these tests already use; for stubs whose `list_local()` only returns already-registered models, the discovered list comes back empty and every existing assertion is unaffected.

- [ ] **Step 6: Commit**

```bash
cd modelman
git add src/modelman/screens/models.py tests/screens/test_models.py
git commit -m "feat(modelman): render discovered-but-unregistered models in the TUI"
```

---

### Task 4: Registration flow — wire the register dialog into `ModelScreen`

**Files:**
- Modify: `modelman/src/modelman/screens/models.py`
- Test: `modelman/tests/screens/test_models.py`

**Interfaces:**
- Consumes: `ModelForm(discovered=...)` and `ModelFormResult.source` (Task 2); `ModelScreen.discovered`/`_discovered_by_key` (Task 3).
- Produces: `ModelScreen._current_discovered() -> DiscoveredModel | None`, `ModelScreen._append_new_model_entry(variant, family, source) -> ModelEntry | None`. Both are screen-internal — no other task depends on them.

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/screens/test_models.py`, near `test_add_model_saves_registry_immediately_and_queues_ready_on`:

```python
@pytest.mark.asyncio
async def test_register_discovered_model_writes_ready_state_without_queuing_download(
    tmp_path, monkeypatch
):
    """Registering a discovered omlx model (bare directory basename, no
    HF org segment) must not queue a ready-on: apply() would call
    provider.download() with that basename as an HF repo id, which HF
    rejects outright — and the artifact needs no download at all, since
    it's already on disk. This is the regression the design doc calls
    out explicitly."""
    from modelman.local_control import DiscoveredModel
    from modelman.registry import load_registry
    from modelman.screens import models as models_module

    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[
            ProviderEntry(id="omlx", name="oMLX", auth=AuthConfig(type="none"), location="local")
        ],
        families=[FamilyEntry(name="qwen3.8")],
        models=[],
    )
    save_registry(reg, reg_path)
    save_state(StateStore(), state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    discovered = DiscoveredModel(
        provider_id="omlx",
        variant_id="Qwen3.8-27B-4bit",
        path="/Users/keith/.omlx/models/Qwen3.8-27B-4bit",
        size_bytes=19_530_941_006,
    )
    monkeypatch.setattr(
        models_module, "discover_unregistered_models", lambda registry: [discovered]
    )

    app = ModelmanApp()
    entry_id = "omlx/Qwen3.8-27B-4bit"
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        await pilot.pause()  # let the reconcile+discover worker settle

        mt = app.screen.query_one("#model-table", DataTable)
        mt.move_cursor(row=mt.row_count - 1)
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()

        provider_sel = app.screen.query_one("#provider-select", Select)
        model_input = app.screen.query_one("#model", Input)
        assert provider_sel.value == "omlx"
        assert model_input.value == "Qwen3.8-27B-4bit"

        app.screen.query_one("#save", Button).focus()
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()

        assert entry_id not in app.screen.queued_ready
        model_state = app.screen.state.get(entry_id)
        assert model_state.ready is True
        assert model_state.disk_path == "/Users/keith/.omlx/models/Qwen3.8-27B-4bit"
        assert model_state.size_bytes == 19_530_941_006
        assert app.screen._discovered_by_key == {}

    reloaded = load_registry(reg_path)
    entry = next(m for m in reloaded.models if m.provider_id == "omlx")
    assert entry.id == entry_id
    assert entry.source == "discovered"
    assert entry.fetch is not None
    assert entry.fetch.repo == "Qwen3.8-27B-4bit"
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -k register_discovered_model -v`
Expected: FAIL — pressing enter on the discovered row falls through to `action_edit_model`'s existing `entry = self._current_entry()` / `if entry is None: return`, so no `ModelForm` ever opens (`query_one("#provider-select", ...)` raises `NoMatches`)

- [ ] **Step 3: Implement the registration flow**

In `modelman/src/modelman/screens/models.py`:

Give `_variant_to_model_entry` a `source` parameter (default keeps every existing caller producing `"curated"` unchanged):

```python
def _variant_to_model_entry(
    variant: dict, *, family: str, registry: Registry, source: str = "curated"
) -> ModelEntry:
    """Convert a ModelForm VariantSpec-shaped dict to a ModelEntry.

    The dialog still emits the legacy TypedDict shape (provider, name,
    repo, files, model_info); the screen needs a ModelEntry to insert
    into registry.models. This adapter keeps the form simple and
    isolates the shape translation here.

    Edit mode preserves `variant["id"]` (the immutable key the user
    sees in the picker); add mode derives the same `provider/name`
    shape ModelForm produced. We don't need a separate "id derivation"
    step — the form already gave us one. `source` defaults to
    "curated" (every normal add/edit); the discovered-registration flow
    passes "discovered" explicitly so provenance survives into
    registry.toml, matching the CLI's own auto-register convention.
    """
    provider_id = variant["provider"]
    provider = registry.provider(provider_id)  # raises KeyError if unknown

    name = variant.get("name") or variant["id"]
    repo = variant.get("repo")
    files = variant.get("files")
    quantizations = variant.get("quantizations")
    local_path = variant.get("local_path")
    fetch = None
    if repo or files or quantizations or local_path:
        fetch = Fetch(repo=repo, files=files, quantizations=quantizations, local_path=local_path)

    draft_repo = variant.get("draft_repo")
    draft_local_path = variant.get("draft_local_path")
    draft = None
    if draft_repo or draft_local_path:
        draft = DraftSpec(repo=draft_repo, local_path=draft_local_path)

    model_info = dict(variant.get("model_info") or {})
    cost_raw = variant.get("cost")
    cost: Cost | None = None
    if cost_raw is not None:
        cost = cost_raw if isinstance(cost_raw, Cost) else _cost_from_dict(cost_raw)
    return ModelEntry(
        id=variant["id"],
        family=family,
        provider_id=provider_id,
        model_name=name,
        location=variant.get("location"),
        source=source,
        cost=cost,
        model_info=model_info,
        fetch=fetch,
        draft=draft,
        quantization=variant.get("quantization"),
        native=is_native_provider(provider),
    )
```

(Only the signature and the docstring's last sentence, plus `source=source` replacing the old hardcoded `source="curated"`, actually change — the body is otherwise identical to today's version, shown in full for diff clarity.)

Add a shared helper right before `action_add_model` (after `_current_family`):

```python
    def _append_new_model_entry(
        self, variant: VariantSpec, family: str, source: str
    ) -> ModelEntry | None:
        """Build and persist a ModelEntry for a freshly submitted add or
        discovered-registration dialog result. Returns None (after
        notifying) on an id collision. Shared by _on_add_model and
        _on_register_discovered so the collision guard and the
        immediate save_registry() can't drift between the two — every
        add path persists the registry entry right away (the post-exit
        runner looks up models by id against a freshly-loaded-from-disk
        registry).
        """
        if any(m.id == variant["id"] for m in self.registry.models):
            self.app.notify("Model ID already exists")
            return None
        entry = _variant_to_model_entry(
            variant, family=family, registry=self.registry, source=source
        )
        if entry.cost is not None:
            entry.pricing_updated_at = _now_iso()
        self.registry.models.append(entry)
        save_registry(self.registry, self.registry_path)
        self._last_provider_used = variant["provider"]
        return entry
```

Add `_current_discovered` right after `_current_entry`:

```python
    def _current_discovered(self) -> DiscoveredModel | None:
        """DiscoveredModel under the cursor, or None (cursor is on a real
        model row, or the table is empty)."""
        mid = self._current_model_id()
        if mid is None:
            return None
        return self._discovered_by_key.get(mid)
```

Replace `_on_add_model`'s body to use the new helper:

```python
    def _on_add_model(self, result) -> None:
        if result is None:
            return
        entry = self._append_new_model_entry(result.spec, result.family, result.source)
        if entry is None:
            return
        self.queued_ready[entry.id] = True
        self.reload()
        self._refresh_pending_bar()
```

Add `_open_register_dialog` and `_on_register_discovered` right after `_on_add_model`:

```python
    def _open_register_dialog(self, discovered: DiscoveredModel) -> None:
        from .forms import ModelForm

        self.app.push_screen(
            ModelForm(
                providers=self._provider_list(),
                discovered=discovered,
                families=self._families_list(),
                family=None,
                provider_kinds=self._provider_kinds(),
            ),
            lambda result: self._on_register_discovered(result, discovered),
        )

    def _on_register_discovered(self, result, discovered: DiscoveredModel) -> None:
        if result is None:
            return
        entry = self._append_new_model_entry(result.spec, result.family, result.source)
        if entry is None:
            return
        # Already on disk — the artifact came from the provider's own
        # filesystem scan, so record it as ready directly (mirroring
        # what the reconcile worker already does for known models)
        # instead of queuing a ready-on: a queued ready-on would call
        # provider.download() at apply time, and for omlx that would
        # try to fetch discovered.variant_id (a bare directory
        # basename) as an HF repo id, which HF rejects outright.
        self.state.set(
            entry.id,
            ModelState(ready=True, disk_path=discovered.path, size_bytes=discovered.size_bytes),
        )
        self.discovered = [d for d in self.discovered if d is not discovered]
        self.reload()
        self._refresh_pending_bar()
```

Update `action_edit_model` to route a discovered row to the new dialog first:

```python
    def action_edit_model(self) -> None:
        discovered = self._current_discovered()
        if discovered is not None:
            self._open_register_dialog(discovered)
            return
        entry = self._current_entry()
        if entry is None:
            return
        mid = entry.id
        from .forms import ModelForm

        spec = model_entry_to_variant(entry)
        self.app.push_screen(
            ModelForm(
                providers=self._provider_list(),
                variant=spec,
                families=self._families_list(),
                family=self.queued_moves.get(mid, entry.family),
                provider_kinds=self._provider_kinds(),
                pricing_updated_at=entry.pricing_updated_at,
            ),
            self._on_edit_model,
        )
```

(Only the new `discovered = ...` / `if discovered is not None: ...` block at the top is new — the rest of the method is unchanged, shown in full for diff clarity. `on_data_table_row_selected` and `action_select_row` already call `self.action_edit_model()`, so both Enter and `e` pick up the new branch with no further changes.)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -k register_discovered_model -v`
Expected: PASS

- [ ] **Step 5: Run the full add/edit test coverage to check for regressions**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -k "add_model or edit_model or variant_to_model_entry" -v`
Expected: PASS — `_variant_to_model_entry`'s new `source` parameter defaults to `"curated"`, and `_on_add_model`'s externally-visible behavior (`queued_ready`, immediate `save_registry`, id-collision notify) is preserved by `_append_new_model_entry`.

- [ ] **Step 6: Commit**

```bash
cd modelman
git add src/modelman/screens/models.py tests/screens/test_models.py
git commit -m "feat(modelman): register discovered models from the TUI models screen"
```

---

### Task 5: Full regression pass

**Files:** none (verification only)

**Interfaces:** none

- [ ] **Step 1: Run the full modelman test suite**

Run: `cd modelman && uv run pytest -q`
Expected: PASS, no failures. (Per `modelman/CLAUDE.md`, the full suite runs in ~1.5 min on this host; numbers drift, re-measure if it feels slow.)

- [ ] **Step 2: Run lint + typecheck**

Run: `cd modelman && make check`
Expected: PASS (ruff lint + typecheck clean — watch especially for the `B905 zip() strict=` rule and consistent typing on the new `discovered: DiscoveredModel | None` / `list[DiscoveredModel]` annotations)

- [ ] **Step 3: Fix any failures found in Steps 1-2, re-run until clean**

If lint or a test fails, fix the specific issue in the relevant task's file (do not silence with `--no-verify` or a lint-disable comment) and re-run both commands until both are clean.

- [ ] **Step 4: Commit any fixes**

```bash
cd modelman
git add -A
git commit -m "fix(modelman): address lint/typecheck/test findings from discovery feature"
```

(Skip this step entirely if Steps 1-2 were already clean — no empty commit.)

---

## Self-Review Notes

- **Spec coverage:** "Discovery data flow" → Task 1 + Task 3's `_run_reconcile` change. "Rendering" → Task 3. "Registration" (locked Provider/Model, `parse_model()` bypass, no queued download, `source="discovered"`) → Task 2 + Task 4. "Staleness after registration" → Task 4's `self.discovered = [d for d in self.discovered if d is not discovered]`. "Testing" section's three bullets → Task 1 Step 1, Task 3 Step 1, Task 4 Step 1 respectively.
- **Placeholder scan:** no TBD/TODO; every step has literal code, not a description of code.
- **Type consistency:** `DiscoveredModel` (provider_id, variant_id, path, size_bytes) used identically across Tasks 1/2/3/4. `discover_unregistered_models(registry: Registry) -> list[DiscoveredModel]` signature matches its one caller (`ModelScreen._run_reconcile`) and its test calls. `ModelFormResult.source` flows from Task 2's `_submit_discovered`/`_submit` through to Task 4's `_on_add_model`/`_on_register_discovered` unchanged. `_append_new_model_entry`'s return type (`ModelEntry | None`) is checked with `is None` in both of its two callers.
