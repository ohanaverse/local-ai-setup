# Shared Model Registry — Phase 2, PR 3 (FamilyScreen on Registry/StateStore) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate `FamilyScreen` (and the `AddFamilyModal`/`EditFamilyModal` it drives) off globbing `families/*.yaml` via `manifest.py` onto `Registry`/`StateStore`, so the whole TUI (not just `ModelScreen`, migrated in PR 2) reads/writes `registry.toml` + `modelman.toml`.

**Architecture:** PR 2 (`docs/superpowers/plans/2026-08-27-shared-model-registry-phase2-pr2.md`, merged as `e535dc2`) rewrote `ModelScreen`'s constructor to require `registry: Registry, state: StateStore, family: str, registry_path: Path, state_path: Path, available_providers: list[str] | None`. `FamilyScreen` still constructs it with the old, now-nonexistent positional signature (`ModelScreen(m, path, self._available_providers)` — a `FamilyManifest`, a `Path`, and a `list[str]` bound to `registry`, `state`, and `family` respectively). **This is currently broken at runtime** — every "open family" action in the shipped TUI raises `TypeError`/`AttributeError` the moment `ModelScreen` touches `self.registry.models_by_family(...)`. `test_app_navigation.py` masks this because every test that exercises it is `@pytest.mark.skip(reason="FamilyScreen migrates to Registry in PR 3")`. This PR fixes that by finishing the migration:

- `families()`/`models_by_family()` (PR 1) enumerate families/models from `Registry`.
- `StateStore.family_display_name()`/`.touch_family()`/`.forget_family()` (PR 1) replace the manifest's `display_name` field and the "family exists but has zero models" case a bare `registry.families()` union can't express on its own — `FamilyScreen` must read the union of `registry.families()` and `state.families.keys()`.
- `AddFamilyModal`/`EditFamilyModal` stop writing `FamilyManifest` files themselves; they become dumb data-collection dialogs (`(family, display_name)` / `display_name` return values) and `FamilyScreen` performs the `StateStore` mutation + save, mirroring how `ModelScreen._on_add_model` already owns the `Registry` mutation after `ModelForm` returns.
- `FamilyScreen`'s reconcile logic drops its own bespoke "is this really on disk" heuristic (the old fallback that called `provider.size_of()` a second time reading `m.downloaded[...]["local_path"]`) and instead follows the simpler pattern `ModelScreen._run_reconcile`/`_is_downloaded` already established: reconcile overlay wins when present, else trust `StateStore`.

**Tech Stack:** Python 3.13, dataclasses (no pydantic), stdlib `tomllib`, `tomli-w` (Phase 1 dep), Textual (existing), pytest-asyncio.

**Spec:** `docs/superpowers/specs/2026-08-27-shared-model-registry-design.md` (canonical schema/ownership) + `docs/superpowers/plans/2026-08-27-shared-model-registry-phase2-pr2.md` (defines the `ModelScreen` constructor this PR must call correctly, and the migration map that named this PR's scope).

## Global Constraints

- `requires-python = "==3.13.*"` (pyproject.toml) — no syntax/stdlib beyond 3.13.
- Match `registry.py`/`state.py`/`queue.py`'s existing style: dataclasses, `load_*`/`save_*` functions, atomic write via `atomic_write_toml`, `drop_none` before every TOML write (already handled inside `save_registry`/`save_state` — call them, don't reimplement).
- `families/<family>.yaml` (`manifest.py`) is migrate-time-only after this PR: `FamilyScreen` and its modals must not import `FamilyManifest`, `save_manifest`, `load_manifest`, or `get_family_dir` when this PR is done. `manifest.py` itself is untouched (still needed by `modelman migrate`).
- `screens/status.py` — **NOT touched.** `StatusScreen` only consumes `PendingChanges`'s pipe-delimited event tags (unchanged since PR 2); this PR's family-level saves (add/edit/delete family) are synchronous, not routed through `StatusScreen`, matching the legacy `FamilyScreen` behavior (`save_manifest` was called inline, not queued).
- `app.py` — **NOT touched** in this PR. It already passes `registry=`/`state=`/... kwargs correctly for the `--initial-family` path (see `_configured_providers()` at `src/modelman/app.py:16`); its own remaining legacy-manifest cleanup (dropping `MODELMAN_CONFIG`/`MODELMAN_FAMILY_DIR` env-var reads elsewhere in the repo) is PR 4's scope, not this one's.
- `config.py`/`config.yaml` (`MODELMAN_CONFIG`) — after this PR, `families.py` no longer reads it (it switches to `registry.provider(...)`/`provider_config(...)`, same as `ModelScreen`). Do not delete `config.py`; other code (e.g. `modelman migrate`) may still use it — out of scope to check.
- Run `uv run pytest`, `uv run ruff check src/ tests/`, `uv run mypy src/` (Makefile targets `test`/`lint`/`typecheck`) before every commit in this plan that touches `src/` or `tests/`.

---

## File Structure

- Modify: `src/modelman/screens/forms.py` — `AddFamilyModal`, `EditFamilyModal` lose their `FamilyManifest`/manifest-file I/O; become pure dialogs.
- Modify: `src/modelman/screens/families.py` — `FamilyScreen` rewritten onto `Registry`/`StateStore`.
- Modify: `tests/screens/test_family_edit.py` — rewritten against the new modal return contracts and `registry.toml`/`modelman.toml` fixtures.
- Modify: `tests/screens/test_app_navigation.py` — the still-`FamilyManifest`-based `FamilyScreen` tests are rewritten onto registry/state fixtures; the `@pytest.mark.skip(reason="FamilyScreen migrates to Registry in PR 3")` tests are un-skipped and rewritten the same way.
- Not created: no new files. `manifest.py` is untouched.

---

### Task 1: `AddFamilyModal` returns `(family, display_name)`, no file I/O

**Files:**
- Modify: `src/modelman/screens/forms.py:1-15` (imports), `:62-101` (`AddFamilyModal`)
- Test: `tests/screens/test_family_edit.py` (Task 3 covers the rewritten assertions; this task only needs the modal-level behavior to compile and be importable — Task 3 exercises it)

**Interfaces:**
- Produces: `AddFamilyModal(ModalScreen[tuple[str, str] | None])` — `dismiss((family, display_name))` on Create (display defaults to `family` if blank), `dismiss(None)` on Cancel. No disk writes.

- [ ] **Step 1: Edit the import block**

In `src/modelman/screens/forms.py`, remove the manifest import (line 13) since after Task 2 nothing in this file needs it:

```python
from ..manifest import FamilyManifest, get_family_dir, save_manifest
```

Delete this line entirely. Leave every other import untouched for now (Task 2 removes `Path` once `EditFamilyModal` no longer needs it either).

- [ ] **Step 2: Rewrite `AddFamilyModal`**

Replace lines 62–101 (the whole class) with:

```python
class AddFamilyModal(ModalScreen[tuple[str, str] | None]):
    """Prompt for a family name and optional display name.

    Returns `(family, display_name)` on Create — display_name falls
    back to family when left blank. FamilyScreen owns the StateStore
    mutation + save after this dismisses; the modal itself performs
    no disk I/O (mirrors ModelForm returning a VariantSpec dict for
    ModelScreen to apply to the Registry).
    """

    DEFAULT_CSS = """
    AddFamilyModal { align: center middle; }
    AddFamilyModal > Vertical { width: 60; height: auto; padding: 1 2; border: round $primary; }
    AddFamilyModal Label { margin-bottom: 1; }
    AddFamilyModal Input { margin-bottom: 1; }
    AddFamilyModal Horizontal { height: auto; align-horizontal: right; }
    AddFamilyModal Button { margin-left: 1; }
    """

    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label("Family name (required):")
            yield Input(id="family-name", placeholder="e.g. ornith-1.5")
            yield Label("Display name (optional):")
            yield Input(id="display-name", placeholder="e.g. Ornith 1.5")
            with Horizontal():
                yield Button("Cancel", id="cancel", variant="default")
                yield Button("Create", id="create", variant="primary")

    def on_button_pressed(self, event: Button.Pressed) -> None:
        if event.button.id == "cancel":
            self.dismiss(None)
            return
        self._submit()

    def on_input_submitted(self, event: Input.Submitted) -> None:
        self._submit()

    def _submit(self) -> None:
        name = self.query_one("#family-name", Input).value.strip()
        display = self.query_one("#display-name", Input).value.strip()
        if not name:
            return
        self.dismiss((name, display or name))
```

- [ ] **Step 3: Confirm the module still imports**

Run: `cd /Users/keith/github/ohanaverse/modelman && uv run python -c "import modelman.screens.forms"`
Expected: no output, exit 0. (Task 2 still references `FamilyManifest` inside `EditFamilyModal`, which no longer exists as an import — this step is expected to currently pass only because Python doesn't check names inside untouched methods until they execute; if it fails with `NameError` at import time, that means Task 2 needs to happen first. If so, do Task 2's Step 1 now and continue; otherwise proceed.)

- [ ] **Step 4: Commit**

```bash
git add src/modelman/screens/forms.py
git commit -m "feat(families): AddFamilyModal returns (family, display) instead of writing FamilyManifest"
```

---

### Task 2: `EditFamilyModal` returns `display_name`, no file I/O

**Files:**
- Modify: `src/modelman/screens/forms.py:104-176` (`EditFamilyModal`), and remove the now-unused `Path` import (line 5) since after this step nothing in the file references it — confirm with the grep in Step 2 before deleting.

**Interfaces:**
- Produces: `EditFamilyModal(ModalScreen[str | None])` — `dismiss(display_name)` on Save (falls back to the family slug if blanked), `dismiss(None)` on Cancel. `__init__(family: str, display_name: str)` unchanged. No disk writes.

- [ ] **Step 1: Rewrite `EditFamilyModal`**

Replace lines 104–176 (the whole class) with:

```python
class EditFamilyModal(ModalScreen[str | None]):
    """Edit the display_name of an existing family.

    The family slug is intentionally NOT editable here — changing it
    would orphan cross-references from models keyed by family. The
    slug is shown read-only so the user knows which family they're
    editing.

    Returns the new display_name on Save (falls back to the family
    slug if blanked, matching AddFamilyModal); None on Cancel.
    FamilyScreen owns the StateStore mutation + save.
    """

    DEFAULT_CSS = """
    EditFamilyModal { align: center middle; }
    EditFamilyModal > Vertical { width: 60; height: auto; padding: 1 2; border: round $primary; }
    EditFamilyModal Label { margin-bottom: 1; }
    EditFamilyModal Input { margin-bottom: 1; }
    EditFamilyModal Horizontal { height: auto; align-horizontal: right; }
    EditFamilyModal Button { margin-left: 1; }
    """

    def __init__(self, family: str, display_name: str) -> None:
        super().__init__()
        self._family = family
        self._display_name = display_name

    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label("Family (cannot be changed):")
            yield Input(
                value=self._family,
                id="family-name",
                disabled=True,
                placeholder="e.g. ornith-1.5",
            )
            yield Label("Display name (optional):")
            yield Input(
                value=self._display_name,
                id="display-name",
                placeholder="e.g. Ornith 1.5",
            )
            with Horizontal():
                yield Button("Cancel", id="cancel", variant="default")
                yield Button("Save", id="save", variant="primary")

    def on_mount(self) -> None:
        self.query_one("#display-name", Input).focus()

    def on_button_pressed(self, event: Button.Pressed) -> None:
        if event.button.id == "cancel":
            self.dismiss(None)
            return
        self._submit()

    def on_input_submitted(self, event: Input.Submitted) -> None:
        self._submit()

    def _submit(self) -> None:
        display = self.query_one("#display-name", Input).value.strip()
        self.dismiss(display or self._family)
```

- [ ] **Step 2: Remove the now-unused `Path` import**

Run: `cd /Users/keith/github/ohanaverse/modelman && grep -n "Path" src/modelman/screens/forms.py`
Expected: no matches (both prior uses were in the code just deleted). If there are no matches, delete `from pathlib import Path` (line 5). If there *are* matches (e.g. `ModelForm` grew a `Path` reference since this plan was written), leave the import and note why in the commit message instead of deleting blindly.

- [ ] **Step 3: Verify the module compiles and ruff is clean**

Run: `cd /Users/keith/github/ohanaverse/modelman && uv run python -c "import modelman.screens.forms" && uv run ruff check src/modelman/screens/forms.py`
Expected: no `NameError`, no ruff findings (in particular no `F401 'pathlib.Path' imported but unused`).

- [ ] **Step 4: Commit**

```bash
git add src/modelman/screens/forms.py
git commit -m "feat(families): EditFamilyModal returns display_name instead of writing FamilyManifest"
```

---

### Task 3: Rewrite `test_family_edit.py` against the new modal contracts

**Files:**
- Modify: `tests/screens/test_family_edit.py` (full rewrite — every test in this file touches `FamilyManifest`/`save_manifest`/`load_manifest`, none survive as-is)

**Interfaces:**
- Consumes: `AddFamilyModal` (Task 1: dismiss `(family, display) | None`), `EditFamilyModal` (Task 2: dismiss `display | None`), `Registry`/`ProviderEntry`/`AuthConfig`/`ModelEntry`/`save_registry` (`modelman.registry`), `StateStore`/`FamilyState`/`load_state`/`save_state` (`modelman.state`).
- Produces: nothing consumed by later tasks — this is a leaf test file.

- [ ] **Step 1: Write the replacement file**

Replace the entire contents of `tests/screens/test_family_edit.py` with:

```python
"""Tests for editing a family's display name from the FamilyScreen.

Today the family screen supports add/delete/open/reconcile/quit but
not edit. The most useful edit on a family is its display_name (the
human-readable label shown in the table and the parent screen of
the ModelScreen). The family slug (the file key) is intentionally
NOT editable: changing it would orphan cross-references from models
keyed by family.

Migrated in PR 3 (2026-08-27-shared-model-registry-phase2-pr3) off
FamilyManifest/families/*.yaml onto Registry (registry.toml) +
StateStore (modelman.toml). AddFamilyModal/EditFamilyModal no longer
write to disk themselves — they return plain data and FamilyScreen
performs the StateStore mutation + save (see forms.py).
"""

from __future__ import annotations

import pytest

from modelman.app import ModelmanApp
from modelman.registry import AuthConfig, ProviderEntry, Registry, save_registry
from modelman.screens.forms import EditFamilyModal
from modelman.state import FamilyState, StateStore, load_state, save_state

# ---------------------------------------------------------------------------
# Modal-level tests
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_edit_family_modal_pre_fills_display_name():
    """Modal opens pre-filled with the existing display_name so the
    user doesn't have to retype from scratch."""
    modal = EditFamilyModal(
        family="ornith-1.5",
        display_name="Ornith 1.5 (preview)",
    )

    async with ModelmanApp().run_test() as pilot:
        result = []
        app = pilot.app
        app.push_screen(modal, lambda r: result.append(r))
        await pilot.pause()

        from textual.widgets import Input

        display_input = modal.query_one("#display-name", Input)
        assert display_input.value == "Ornith 1.5 (preview)"

        slug_input = modal.query_one("#family-name", Input)
        assert slug_input.value == "ornith-1.5"
        assert slug_input.disabled, (
            "Family slug must not be editable from the edit modal; "
            "changing it would orphan cross-references from models."
        )

        pilot.app.pop_screen()


@pytest.mark.asyncio
async def test_edit_family_modal_save_returns_new_display_name():
    """Submitting the form dismisses with the new display_name string.
    The modal itself performs no disk I/O — FamilyScreen (Task 5)
    is what persists it to modelman.toml."""
    modal = EditFamilyModal(
        family="ornith-1.5",
        display_name="Ornith 1.5",
    )

    async with ModelmanApp().run_test() as pilot:
        result = []
        app = pilot.app
        app.push_screen(modal, lambda r: result.append(r))
        await pilot.pause()

        from textual.widgets import Button, Input

        modal.query_one("#display-name", Input).value = "Ornith v1.5 (renamed)"
        modal.query_one("#save", Button).press()
        await pilot.pause()

    assert result == ["Ornith v1.5 (renamed)"]


@pytest.mark.asyncio
async def test_edit_family_modal_cancel_returns_none():
    """Cancel must dismiss with None."""
    modal = EditFamilyModal(family="ornith-1.5", display_name="Ornith 1.5")

    async with ModelmanApp().run_test() as pilot:
        result = []
        pilot.app.push_screen(modal, lambda r: result.append(r))
        await pilot.pause()

        from textual.widgets import Button, Input

        modal.query_one("#display-name", Input).value = "STRANGER CHANGES"
        modal.query_one("#cancel", Button).press()
        await pilot.pause()

    assert result == [None]


@pytest.mark.asyncio
async def test_edit_family_modal_empty_display_falls_back_to_family():
    """If the user blanks the display_name, fall back to using the
    family slug (matches AddFamilyModal's behavior)."""
    modal = EditFamilyModal(family="ornith-1.5", display_name="Ornith 1.5")

    async with ModelmanApp().run_test() as pilot:
        result = []
        pilot.app.push_screen(modal, lambda r: result.append(r))
        await pilot.pause()

        from textual.widgets import Button, Input

        modal.query_one("#display-name", Input).value = ""
        modal.query_one("#save", Button).press()
        await pilot.pause()

    assert result == ["ornith-1.5"]  # fell back to slug


# ---------------------------------------------------------------------------
# App-level: 'e' opens the edit modal and persists to modelman.toml
# ---------------------------------------------------------------------------


def _seed(tmp_path, monkeypatch, *, family_state: FamilyState | None = None):
    """Seed an empty registry.toml (no models needed for these tests)
    and a modelman.toml with one known family, and point the app's
    env vars at tmp_path. Returns (registry_path, state_path)."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    save_registry(
        Registry(providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))]),
        reg_path,
    )
    store = StateStore()
    if family_state is not None:
        store.families["ornith-1.5"] = family_state
    save_state(store, state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    return reg_path, state_path


@pytest.mark.asyncio
async def test_family_screen_e_opens_edit_modal_for_selected_row(tmp_path, monkeypatch):
    """Pressing 'e' on a row in the family table must push an
    EditFamilyModal pre-filled with that family's data."""
    _reg_path, _state_path = _seed(
        tmp_path, monkeypatch, family_state=FamilyState(display_name="Ornith 1.5")
    )

    app = ModelmanApp()
    captured: list[EditFamilyModal] = []
    original_push = app.push_screen

    def tracking_push(screen, *args, **kwargs):
        if isinstance(screen, EditFamilyModal):
            captured.append(screen)
        return original_push(screen, *args, **kwargs)

    monkeypatch.setattr(app, "push_screen", tracking_push)

    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("e")
        await pilot.pause()

    assert len(captured) == 1, f"expected EditFamilyModal push; got {captured}"
    modal = captured[0]
    assert modal._family == "ornith-1.5"
    assert modal._display_name == "Ornith 1.5"


@pytest.mark.asyncio
async def test_family_screen_e_with_no_rows_is_a_noop(tmp_path, monkeypatch):
    """If the table is empty, 'e' should not crash or push a modal."""
    _seed(tmp_path, monkeypatch)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("e")
        await pilot.pause()


@pytest.mark.asyncio
async def test_family_screen_edit_persists_display_name_to_state(tmp_path, monkeypatch):
    """Saving the edit modal must write the new display_name to
    modelman.toml (StateStore), not to any manifest file."""
    _reg_path, state_path = _seed(
        tmp_path, monkeypatch, family_state=FamilyState(display_name="Ornith 1.5")
    )

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("e")
        await pilot.pause()

        from textual.widgets import Button, Input

        app.screen.query_one("#display-name", Input).value = "Ornith v1.5 (renamed)"
        app.screen.query_one("#save", Button).press()
        await pilot.pause()

    reloaded = load_state(state_path)
    assert reloaded.family_display_name("ornith-1.5") == "Ornith v1.5 (renamed)"
```

- [ ] **Step 2: Run the file and verify it passes**

Run: `cd /Users/keith/github/ohanaverse/modelman && uv run pytest tests/screens/test_family_edit.py -v`
Expected: all 7 tests PASS. (This exercises Task 1/2's new modal contracts but not yet `FamilyScreen`'s own migration — `test_family_screen_e_opens_edit_modal_for_selected_row`, `_e_with_no_rows_is_a_noop`, and `_edit_persists_display_name_to_state` will fail until Task 5 lands, because `FamilyScreen` still reads `families/*.yaml` via `manifest.py` at this point. **That's expected** — leave those three red and move to Task 4/5, then return here.)

- [ ] **Step 3: Commit**

```bash
git add tests/screens/test_family_edit.py
git commit -m "test(families): migrate test_family_edit.py to registry/state fixtures"
```

---

### Task 4: Migrate `FamilyScreen`'s data loading to `Registry`/`StateStore`

**Files:**
- Modify: `src/modelman/screens/families.py` (imports, `__init__`, `on_mount`, `reload`, `_run_reconcile`, `_refresh_from_disk`, `on_screen_resume`)

**Interfaces:**
- Consumes: `Registry`/`ModelEntry`/`load_registry`/`save_registry`/`provider_config`/`RegistryError` (`..registry`), `StateStore`/`load_state`/`save_state` (`..state`), `_model_entry_to_variant` (`.models`), `ProviderRegistry` (`..providers.registry`).
- Produces: `self.registry: Registry`, `self.state: StateStore`, `self.registry_path: Path`, `self.state_path: Path`, `self._available_providers: list[str]` (now sourced from `registry.providers`, not `config.yaml`) — all consumed by Task 5's action methods and by the `ModelScreen(...)` construction.

- [ ] **Step 1: Replace the imports**

At the top of `src/modelman/screens/families.py`, replace:

```python
from ..config import load_config
from ..manifest import FamilyManifest, get_family_dir, load_manifest
from ..providers.registry import ProviderRegistry
from .forms import AddFamilyModal, ConfirmModal, EditFamilyModal
from .models import ModelScreen
```

with:

```python
from ..providers.registry import ProviderRegistry
from ..registry import (
    RegistryError,
    Registry,
    load_registry,
    provider_config,
    save_registry,
)
from ..state import StateStore, load_state, save_state
from .forms import AddFamilyModal, ConfirmModal, EditFamilyModal
from .models import ModelScreen, _model_entry_to_variant
```

`_human_size` (the module-level helper) and the `Path` import stay unchanged — both are still used.

- [ ] **Step 2: Replace `__init__`**

Replace:

```python
    def __init__(self) -> None:
        super().__init__()
        # Per-(family, variant) reconcile overlay: {downloaded: bool, size: int|None}.
        # Populated by the background reconcile worker; read by reload() when
        # computing the downloaded count and size column.
        self._reconciled: dict[tuple[str, str], dict] = {}
        # Names of providers configured in ~/.config/local-ai/config.yaml,
        # loaded once on mount and forwarded to ModelScreen on Open so
        # the model screen's provider pane always shows every configured
        # provider, even when the family has zero variants.
        self._available_providers: list[str] = []
```

with:

```python
    def __init__(self) -> None:
        super().__init__()
        # Per-model-id reconcile overlay: {downloaded: bool, size: int|None}.
        # Populated by the background reconcile worker; read by reload() when
        # computing the downloaded count and size column. Keyed by
        # ModelEntry.id (globally unique), matching ModelScreen.reconciled.
        self._reconciled: dict[str, dict] = {}
        # Providers from registry.toml, loaded once on mount and forwarded
        # to ModelScreen on Open so the model screen's provider pane always
        # shows every configured provider, even when the family has zero
        # models.
        self._available_providers: list[str] = []
        # Registry/StateStore + the paths they were loaded from — set in
        # on_mount(), reloaded in _refresh_from_disk() so edits made on
        # ModelScreen (which saves on apply) are picked up on resume.
        self.registry: Registry = Registry()
        self.state: StateStore = StateStore()
        self.registry_path: Path = Path()
        self.state_path: Path = Path()
```

- [ ] **Step 3: Replace `on_mount`**

Replace:

```python
    def on_mount(self) -> None:
        table = self.query_one(DataTable)
        table.add_columns("FAMILY", "DISPLAY", "VARIANTS", "DOWNLOADED", "SIZE")
        # Cache the configured providers so opening a family shows a
        # full provider pane even if the family is empty. Preserve
        # config-file insertion order so the left pane's column order
        # matches what the user wrote in config.yaml.
        try:
            cfg = load_config()
            self._available_providers = list(cfg.providers.keys())
        except Exception:
            self._available_providers = []
        self.reload()
        # Reconcile against provider state so the size and downloaded columns
        # reflect reality even when the manifest is stale. In-memory only.
        self.run_worker(self._run_reconcile, exclusive=True, thread=True)
```

with:

```python
    def on_mount(self) -> None:
        table = self.query_one(DataTable)
        table.add_columns("FAMILY", "DISPLAY", "VARIANTS", "DOWNLOADED", "SIZE")
        self._load_from_disk()
        self.reload()
        # Reconcile against provider state so the size and downloaded columns
        # reflect reality even when modelman.toml is stale. In-memory only.
        self.run_worker(self._run_reconcile, exclusive=True, thread=True)

    def _load_from_disk(self) -> None:
        """(Re)load registry.toml + modelman.toml and the derived
        available-providers list. A missing/invalid registry.toml is
        not fatal here (an empty Registry just means an empty family
        table) — ModelScreen's own on_mount has the same tolerance."""
        from ..registry import _default_registry_path
        from ..state import _default_state_path

        self.registry_path = _default_registry_path()
        self.state_path = _default_state_path()
        try:
            self.registry = load_registry(self.registry_path)
        except RegistryError:
            self.registry = Registry()
        self.state = load_state(self.state_path)
        # Preserve registry.toml's provider insertion order so the left
        # pane's column order matches what the user wrote there (mirrors
        # app.py's _configured_providers()).
        self._available_providers = [p.id for p in self.registry.providers]
```

- [ ] **Step 4: Replace `_refresh_from_disk` and `on_screen_resume`**

Replace:

```python
    def _refresh_from_disk(self) -> None:
        """Clear the cached reconcile overlay and re-run reconcile.

        Used both on screen resume (after popping back from a child
        screen that may have mutated the on-disk manifest) and on
        explicit 'r'. Without clearing, stale (family, vid) entries
        from deleted variants would remain in memory forever and the
        reload() fallback path could pick them up if a new variant
        were later added with the same id.
        """
        self._reconciled.clear()
        self.reload()  # show manifest-truth immediately
        self.run_worker(self._run_reconcile, exclusive=True, thread=True)
```

with:

```python
    def _refresh_from_disk(self) -> None:
        """Reload registry.toml/modelman.toml, clear the cached reconcile
        overlay, and re-run reconcile.

        Used both on screen resume (after popping back from a child
        screen that may have mutated the on-disk files — ModelScreen
        saves on apply) and on explicit 'r'. Without clearing the
        overlay, stale entries for deleted models would remain in
        memory forever.
        """
        self._load_from_disk()
        self._reconciled.clear()
        self.reload()  # show state-truth immediately
        self.run_worker(self._run_reconcile, exclusive=True, thread=True)
```

`on_screen_resume` itself (`self._refresh_from_disk()`) is unchanged — leave it as-is.

- [ ] **Step 5: Replace `_run_reconcile`**

Replace:

```python
    def _run_reconcile(self) -> None:
        try:
            config = load_config()
        except Exception:
            return
        family_dir: Path = get_family_dir()
        if not family_dir.exists():
            return
        # Cache provider instances per provider name.
        providers: dict[str, object] = {}
        for path in sorted(family_dir.glob("*.yaml")):
            try:
                m = load_manifest(path.stem, family_dir=family_dir)
            except Exception:
                continue
            for v in m.variants:
                pname = v["provider"]
                if pname not in providers:
                    try:
                        providers[pname] = ProviderRegistry.get(pname, config.provider(pname))
                    except Exception:
                        continue
                provider = providers[pname]
                size: int | None = None
                try:
                    raw = provider.size_of(v)  # type: ignore[attr-defined]
                    if isinstance(raw, int):
                        size = raw
                except Exception:
                    size = None
                self._reconciled[(m.family, v["id"])] = {
                    "downloaded": size is not None,
                    "size": size,
                }
        self.app.call_from_thread(self.reload)
```

with:

```python
    def _run_reconcile(self) -> None:
        """Ask each provider whether its models are on disk; cache
        results keyed by model id. Mirrors ModelScreen._run_reconcile
        but across every model in the registry (not one family)."""
        providers: dict[str, object] = {}
        for m in self.registry.models:
            pname = m.provider_id
            if pname not in providers:
                try:
                    entry = self.registry.provider(pname)
                    providers[pname] = ProviderRegistry.get(pname, provider_config(entry))
                except Exception:
                    continue
            provider = providers[pname]
            spec = _model_entry_to_variant(m)
            size: int | None = None
            try:
                raw = provider.size_of(spec)  # type: ignore[attr-defined]
                if isinstance(raw, int):
                    size = raw
            except Exception:
                size = None
            self._reconciled[m.id] = {
                "downloaded": size is not None,
                "size": size,
            }
        self.app.call_from_thread(self.reload)
```

- [ ] **Step 6: Replace `reload`**

Replace the whole `reload` method with:

```python
    def reload(self) -> None:
        table = self.query_one(DataTable)
        table.clear()
        # A family is "known" if it has models in the registry, or was
        # explicitly touched in state (e.g. AddFamilyModal for a family
        # with zero models yet) — mirrors the legacy per-family manifest
        # file's existence, which didn't require any variants either.
        families = sorted(set(self.registry.families()) | set(self.state.families.keys()))
        for family in families:
            models = self.registry.models_by_family(family)
            variants = len(models)
            downloaded_count = 0
            total_size = 0
            unknown = False
            for m in models:
                rec = self._reconciled.get(m.id)
                if rec is not None:
                    if rec["downloaded"]:
                        downloaded_count += 1
                        sz = rec["size"]
                        if sz is None:
                            unknown = True
                        else:
                            total_size += sz
                    # If rec says not downloaded, don't fall through to
                    # state (it could be stale on either side).
                elif self.state.get(m.id).downloaded:
                    # No reconcile info yet; trust state for this model.
                    downloaded_count += 1
                    unknown = True  # size unknown until reconcile runs
            size_str = (
                "—"
                if downloaded_count == 0
                else _human_size(total_size if (total_size > 0 or not unknown) else None)
            )
            table.add_row(
                family,
                self.state.family_display_name(family) if family in self.state.families else "",
                str(variants),
                str(downloaded_count),
                size_str,
                key=family,
            )
```

Note the `DISPLAY` column: the legacy manifest always had a `display_name` (defaulted to the slug on `load_manifest`), so the column showed something for every row. `StateStore.family_display_name()` also falls back to the slug — but `test_family_screen_lists_configured_families` (rewritten in Task 6) expects the column blank for a family that has models but was never `touch_family`'d (e.g. added directly to `registry.toml` outside the TUI). The `if family in self.state.families else ""` guard preserves that distinction; call `self.state.family_display_name(family)` only when there's an actual entry to read.

- [ ] **Step 7: Confirm the module imports cleanly**

Run: `cd /Users/keith/github/ohanaverse/modelman && uv run python -c "import modelman.screens.families"`
Expected: `NameError` or `ImportError` mentioning `AddFamilyModal`/`EditFamilyModal`/`ConfirmModal`'s constructors, `get_family_dir`, `load_manifest`, or `FamilyManifest` is expected here — Task 5 hasn't updated the action methods yet, so `action_add_family` etc. still reference the old modal contracts and `get_family_dir`. **This is fine**; the import itself (module-level code) should succeed since those names only get resolved when the methods run. If the import itself fails, re-check Steps 1–6 for a stray top-level reference.

- [ ] **Step 8: Commit**

```bash
git add src/modelman/screens/families.py
git commit -m "feat(families): FamilyScreen reads registry.toml/modelman.toml instead of families/*.yaml"
```

(This intentionally lands with `action_*` methods still broken — Task 5 is the next task and fixes them. Committing here keeps the diff reviewable in two chunks, same granularity PR 2 used for `queue.py` vs. `ModelScreen`.)

---

### Task 5: Migrate `FamilyScreen`'s mutating actions and `ModelScreen` construction

**Files:**
- Modify: `src/modelman/screens/families.py` (`action_add_family`, `action_edit_family`, `action_delete_family`, `_on_delete_confirm`, `_on_delete_family_with_variants`, `_delete_family_file` → `_delete_family`, `on_data_table_row_selected`, `action_open_family`)

**Interfaces:**
- Consumes: Task 4's `self.registry`/`self.state`/`self.registry_path`/`self.state_path`/`self._available_providers`; `save_registry`/`save_state`; `ModelScreen(registry=, state=, family=, registry_path=, state_path=, available_providers=)` (PR 2's signature, unchanged by this PR).
- Produces: nothing new consumed elsewhere — this is the last piece of `FamilyScreen`.

- [ ] **Step 1: Replace `action_add_family`**

Replace:

```python
    def action_add_family(self) -> None:
        def _on_close(result: FamilyManifest | None) -> None:
            if result is not None:
                self.reload()

        self.app.push_screen(AddFamilyModal(), _on_close)
```

with:

```python
    def action_add_family(self) -> None:
        def _on_close(result: tuple[str, str] | None) -> None:
            if result is None:
                return
            family, display_name = result
            self.state.touch_family(family, display_name)
            save_state(self.state, self.state_path)
            self.reload()

        self.app.push_screen(AddFamilyModal(), _on_close)
```

- [ ] **Step 2: Replace `action_edit_family`**

Replace:

```python
    def action_edit_family(self) -> None:
        table = self.query_one(DataTable)
        if table.row_count == 0:
            return
        row_key = list(table.rows.keys())[table.cursor_row]
        family_name = str(row_key.value)
        try:
            m = load_manifest(family_name)
        except Exception:
            return

        def _on_close(result: FamilyManifest | None) -> None:
            if result is None:
                return
            # Reload from disk so the table picks up the new
            # display_name. _refresh_from_disk also clears the
            # reconcile overlay, which is correct because variant
            # ids didn't change — the keys stay valid — but matching
            # add/delete (which already do this) keeps behavior
            # uniform.
            self._refresh_from_disk()

        self.app.push_screen(
            EditFamilyModal(
                family=m.family,
                display_name=m.display_name,
            ),
            _on_close,
        )
```

with:

```python
    def action_edit_family(self) -> None:
        table = self.query_one(DataTable)
        if table.row_count == 0:
            return
        row_key = list(table.rows.keys())[table.cursor_row]
        family_name = str(row_key.value)

        def _on_close(display_name: str | None) -> None:
            if display_name is None:
                return
            self.state.touch_family(family_name, display_name)
            save_state(self.state, self.state_path)
            # _refresh_from_disk also clears the reconcile overlay; model
            # ids didn't change so the keys stay valid, but matching
            # add/delete (which already do this) keeps behavior uniform.
            self._refresh_from_disk()

        self.app.push_screen(
            EditFamilyModal(
                family=family_name,
                display_name=self.state.family_display_name(family_name),
            ),
            _on_close,
        )
```

- [ ] **Step 3: Replace `action_delete_family`, its confirm callbacks, and `_delete_family_file`**

Replace:

```python
    def action_delete_family(self) -> None:
        table = self.query_one(DataTable)
        if table.row_count == 0:
            return
        row_key = list(table.rows.keys())[table.cursor_row]
        family_name = str(row_key.value)
        try:
            m = load_manifest(family_name)
        except Exception:
            return

        variants_count = len(m.variants)
        downloaded_count = len(m.downloaded)

        # Deletion is only safe when the family has nothing to lose.
        # The previous check only protected against downloaded entries,
        # which silently dropped families with queued-but-not-yet-
        # downloaded variants when they got bulk-queued for delete and
        # then the user pressed d on the family row. Now we protect
        # against any variants, queued or downloaded. The dialog
        # messages spell out the current state so the user knows which
        # path they're on.
        if downloaded_count > 0:
            self.app.push_screen(
                ConfirmModal(
                    f"Cannot delete '{family_name}': {downloaded_count} "
                    f"downloaded model{'s' if downloaded_count != 1 else ''} "
                    f"of {variants_count} variant{'s' if variants_count != 1 else ''}. "
                    f"Remove downloads first."
                ),
                self._on_blocked_confirm,
            )
            return
        if variants_count > 0:
            # Family has variant definitions but none have been
            # downloaded yet. Deleting would lose the variant
            # definitions entirely; require explicit confirmation.
            self.app.push_screen(
                ConfirmModal(
                    f"Family '{family_name}' has {variants_count} variant"
                    f"{'s' if variants_count != 1 else ''} (none downloaded). "
                    f"Delete anyway? This loses the variant definitions."
                ),
                self._on_delete_family_with_variants,
            )
            return
        self.app.push_screen(
            ConfirmModal(
                f"Family '{family_name}' is empty. Delete?"
            ),
            self._on_delete_confirm,
        )

    def _on_delete_confirm(self, confirmed: bool | None) -> None:
        if not confirmed:
            return
        self._delete_family_file()

    def _on_delete_family_with_variants(self, confirmed: bool | None) -> None:
        if not confirmed:
            return
        self._delete_family_file()

    def _delete_family_file(self) -> None:
        """Unlink the manifest file for the family currently under
        the cursor, then reload. Shared between the empty-family
        confirmation and the variants-no-download confirmation so
        both paths go through the same destructive code.

        The cursor_row is re-read here rather than cached at modal
        open: while the modal was up the table did not change, so
        the cursor points at the same row the user selected when
        they pressed 'd'. Reading again here keeps each action
        callback self-contained and avoids stale-key bugs if the
        modal was triggered from a context that mutated the table.
        """
        table = self.query_one(DataTable)
        row_key = list(table.rows.keys())[table.cursor_row]
        family_name = str(row_key.value)
        path = get_family_dir() / f"{family_name}.yaml"
        if path.exists():
            path.unlink()
        self.reload()
```

with:

```python
    def action_delete_family(self) -> None:
        table = self.query_one(DataTable)
        if table.row_count == 0:
            return
        row_key = list(table.rows.keys())[table.cursor_row]
        family_name = str(row_key.value)
        models = self.registry.models_by_family(family_name)
        variants_count = len(models)
        downloaded_count = sum(1 for m in models if self.state.get(m.id).downloaded)

        # Deletion is only safe when the family has nothing to lose.
        # Protect against any models, queued-download or downloaded.
        # The dialog messages spell out the current state so the user
        # knows which path they're on.
        if downloaded_count > 0:
            self.app.push_screen(
                ConfirmModal(
                    f"Cannot delete '{family_name}': {downloaded_count} "
                    f"downloaded model{'s' if downloaded_count != 1 else ''} "
                    f"of {variants_count} variant{'s' if variants_count != 1 else ''}. "
                    f"Remove downloads first."
                ),
                self._on_blocked_confirm,
            )
            return
        if variants_count > 0:
            # Family has model definitions but none have been
            # downloaded yet. Deleting would lose the model
            # definitions entirely; require explicit confirmation.
            self.app.push_screen(
                ConfirmModal(
                    f"Family '{family_name}' has {variants_count} variant"
                    f"{'s' if variants_count != 1 else ''} (none downloaded). "
                    f"Delete anyway? This loses the model definitions."
                ),
                self._on_delete_family_with_variants,
            )
            return
        self.app.push_screen(
            ConfirmModal(
                f"Family '{family_name}' is empty. Delete?"
            ),
            self._on_delete_confirm,
        )

    def _on_delete_confirm(self, confirmed: bool | None) -> None:
        if not confirmed:
            return
        self._delete_family()

    def _on_delete_family_with_variants(self, confirmed: bool | None) -> None:
        if not confirmed:
            return
        self._delete_family()

    def _delete_family(self) -> None:
        """Remove every model in the currently-selected family from
        the registry, drop its state entries and its `families` state
        entry, save both files, then reload. Shared between the
        empty-family confirmation and the variants-no-download
        confirmation so both paths go through the same destructive
        code.

        The cursor_row is re-read here rather than cached at modal
        open: while the modal was up the table did not change, so
        the cursor points at the same row the user selected when
        they pressed 'd'. Reading again here keeps each action
        callback self-contained and avoids stale-key bugs if the
        modal was triggered from a context that mutated the table.
        """
        table = self.query_one(DataTable)
        row_key = list(table.rows.keys())[table.cursor_row]
        family_name = str(row_key.value)
        removed_ids = {m.id for m in self.registry.models if m.family == family_name}
        self.registry.models = [m for m in self.registry.models if m.family != family_name]
        for mid in removed_ids:
            self.state.models.pop(mid, None)
        self.state.forget_family(family_name)
        save_registry(self.registry, self.registry_path)
        save_state(self.state, self.state_path)
        self.reload()
```

- [ ] **Step 4: Replace `on_data_table_row_selected` and `action_open_family`**

Replace:

```python
    def on_data_table_row_selected(self, event: DataTable.RowSelected) -> None:
        family_name = str(event.row_key.value) if event.row_key else ""
        if not family_name:
            return
        m = load_manifest(family_name)
        path = get_family_dir() / f"{family_name}.yaml"
        # FamilyScreen still uses the legacy manifest signature; PR 3 migrates it.
        self.app.push_screen(ModelScreen(m, path, self._available_providers))  # type: ignore[arg-type,call-arg]

    def action_open_family(self) -> None:
        table = self.query_one(DataTable)
        if table.row_count == 0:
            return
        row_key = list(table.rows.keys())[table.cursor_row]
        family_name = str(row_key.value)
        m = load_manifest(family_name)
        path = get_family_dir() / f"{family_name}.yaml"
        # FamilyScreen still uses the legacy manifest signature; PR 3 migrates it.
        self.app.push_screen(ModelScreen(m, path, self._available_providers))  # type: ignore[arg-type,call-arg]
```

with:

```python
    def on_data_table_row_selected(self, event: DataTable.RowSelected) -> None:
        family_name = str(event.row_key.value) if event.row_key else ""
        if not family_name:
            return
        self._open_family(family_name)

    def action_open_family(self) -> None:
        table = self.query_one(DataTable)
        if table.row_count == 0:
            return
        row_key = list(table.rows.keys())[table.cursor_row]
        self._open_family(str(row_key.value))

    def _open_family(self, family_name: str) -> None:
        self.app.push_screen(
            ModelScreen(
                registry=self.registry,
                state=self.state,
                family=family_name,
                registry_path=self.registry_path,
                state_path=self.state_path,
                available_providers=self._available_providers,
            )
        )
```

- [ ] **Step 5: Run the whole test suite and verify**

Run: `cd /Users/keith/github/ohanaverse/modelman && uv run pytest -v`
Expected: `tests/screens/test_family_edit.py` now fully PASSes (the 3 tests left red at the end of Task 3 turn green). `tests/screens/test_app_navigation.py` will show new failures — every test that still constructs a `FamilyManifest`/calls `save_manifest`/`monkeypatch.setenv("MODELMAN_FAMILY_DIR", ...)` now fails because `FamilyScreen` no longer reads that env var or that file format at all. **This is expected** — Task 6 rewrites those tests. Confirm the failures are all in `test_app_navigation.py` and are all related to `FamilyManifest`/`MODELMAN_FAMILY_DIR` (e.g. `ManifestError`, empty table where a row was expected, `AttributeError` on `FamilyManifest`) — if any failure is somewhere else (e.g. `test_family_edit.py`, `test_queue.py`, `test_status.py`), stop and investigate before continuing.

- [ ] **Step 6: Commit**

```bash
git add src/modelman/screens/families.py
git commit -m "feat(families): FamilyScreen add/edit/delete/open family operate on Registry/StateStore"
```

---

### Task 6: Migrate the currently-passing `FamilyScreen` tests in `test_app_navigation.py`

**Files:**
- Modify: `tests/screens/test_app_navigation.py` (the 9 tests below, currently un-skipped and passing against `FamilyManifest`/`families/*.yaml`)

Tests in scope for this task (identified by name, all currently between the top of the file and the `# ModelScreen constructed directly` section divider):
`test_family_screen_lists_configured_families`, `test_add_family_creates_manifest`, `test_delete_family_when_empty`, `test_delete_family_blocked_when_downloaded`, `test_delete_family_blocked_when_variants_present_even_without_downloads`, `test_delete_family_prompts_for_explicit_confirmation_with_variants`, `test_delete_family_cancel_keeps_empty_family`, `test_delete_family_cancel_keyword_preserves_file`, `test_family_screen_reconciles_size_from_provider`.

(`test_app_launches_into_family_screen`, `test_q_exits_app_from_family_screen`, and `test_app_with_initial_family_launches_into_model_screen` are already registry-based / don't touch families/*.yaml — leave them untouched.)

**Interfaces:**
- Consumes: `Registry`/`ProviderEntry`/`AuthConfig`/`ModelEntry`/`save_registry`/`load_registry` (`modelman.registry`), `StateStore`/`FamilyState`/`save_state`/`load_state` (`modelman.state`).

- [ ] **Step 1: Add a shared seeding helper near the top of the file**

Right after the existing imports (before `test_app_launches_into_family_screen`), add:

```python
def _seed_registry_and_state(
    tmp_path,
    monkeypatch,
    *,
    models: tuple[ModelEntry, ...] = (),
    downloaded: dict[str, str] | None = None,  # model_id -> disk_path
    providers: tuple[str, ...] = ("ollama",),
):
    """Seed registry.toml (with `models`) and modelman.toml (marking
    `downloaded` model ids as downloaded) in tmp_path, and point the
    app's env vars there. Returns (registry_path, state_path)."""
    from modelman.registry import AuthConfig, ProviderEntry
    from modelman.state import ModelState

    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[
            ProviderEntry(id=p, name=p, auth=AuthConfig(type="none")) for p in providers
        ],
        models=list(models),
    )
    save_registry(reg, reg_path)
    store = StateStore()
    for mid, path in (downloaded or {}).items():
        store.set(mid, ModelState(downloaded=True, disk_path=path))
    save_state(store, state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    return reg_path, state_path
```

This needs `ModelEntry` imported at module scope — it already is (see the existing top-of-file import block: `from modelman.registry import (AuthConfig, ModelEntry, ProviderEntry, Registry, load_registry, save_registry)`).

- [ ] **Step 2: Rewrite `test_family_screen_lists_configured_families`**

Replace:

```python
@pytest.mark.asyncio
async def test_family_screen_lists_configured_families(tmp_path, monkeypatch):
    from modelman.manifest import FamilyManifest, save_manifest

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        display_name="Ornith",
        variants=[{"id": "a", "provider": "ollama", "name": "o:35b"}],
    )
    m.mark_downloaded("a", str(tmp_path / "downloaded-a"))
    save_manifest(m, fam_dir / "ornith.yaml")

    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        table = app.screen.query_one("DataTable")
        assert table.row_count == 1
```

with:

```python
@pytest.mark.asyncio
async def test_family_screen_lists_configured_families(tmp_path, monkeypatch):
    a = ModelEntry(id="ollama/o35", family="ornith", provider_id="ollama", model_name="o:35b")
    _seed_registry_and_state(
        tmp_path, monkeypatch, models=[a], downloaded={"ollama/o35": str(tmp_path / "downloaded-a")}
    )

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        table = app.screen.query_one("DataTable")
        assert table.row_count == 1
```

- [ ] **Step 3: Rewrite `test_add_family_creates_manifest`**

Replace:

```python
@pytest.mark.asyncio
async def test_add_family_creates_manifest(tmp_path, monkeypatch):
    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("a")
        for ch in "mamba":
            await pilot.press(ch)
        await pilot.press("enter")
        await pilot.pause()
        assert (fam_dir / "mamba.yaml").exists()
```

with:

```python
@pytest.mark.asyncio
async def test_add_family_registers_state(tmp_path, monkeypatch):
    """Adding a family with no models yet must still make it appear
    in the family table (mirrors the legacy manifest file's
    existence) by recording it in modelman.toml's `families` table."""
    _reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch)

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("a")
        for ch in "mamba":
            await pilot.press(ch)
        await pilot.press("enter")
        await pilot.pause()

    from modelman.state import load_state

    reloaded = load_state(state_path)
    assert "mamba" in reloaded.families
    table = app.screen.query_one("DataTable")
    assert table.row_count == 1
```

- [ ] **Step 4: Rewrite the four `test_delete_family_*` tests**

Replace:

```python
@pytest.mark.asyncio
async def test_delete_family_when_empty(tmp_path, monkeypatch):
    from modelman.manifest import FamilyManifest, save_manifest

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    save_manifest(FamilyManifest(family="mamba"), fam_dir / "mamba.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        await pilot.press("y")
        await pilot.pause()
        assert not (fam_dir / "mamba.yaml").exists()
```

with:

```python
@pytest.mark.asyncio
async def test_delete_family_when_empty(tmp_path, monkeypatch):
    from modelman.state import FamilyState, StateStore, load_state, save_state

    reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch)
    store = StateStore(families={"mamba": FamilyState()})
    save_state(store, state_path)

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        await pilot.press("y")
        await pilot.pause()

    assert "mamba" not in load_state(state_path).families
```

Replace:

```python
@pytest.mark.asyncio
async def test_delete_family_blocked_when_downloaded(tmp_path, monkeypatch):
    from modelman.manifest import FamilyManifest, save_manifest

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[{"id": "a", "provider": "ollama", "name": "o:35b"}],
    )
    m.mark_downloaded("a", str(tmp_path / "downloaded"))
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        await pilot.press("n")
        await pilot.pause()
        assert (fam_dir / "ornith.yaml").exists()
```

with:

```python
@pytest.mark.asyncio
async def test_delete_family_blocked_when_downloaded(tmp_path, monkeypatch):
    a = ModelEntry(id="ollama/a", family="ornith", provider_id="ollama", model_name="o:35b")
    _reg_path, state_path = _seed_registry_and_state(
        tmp_path, monkeypatch, models=[a], downloaded={"ollama/a": str(tmp_path / "downloaded")}
    )

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        await pilot.press("n")
        await pilot.pause()

    from modelman.registry import load_registry

    assert "ollama/a" in [m.id for m in load_registry(_reg_path).models]
```

Replace:

```python
@pytest.mark.asyncio
async def test_delete_family_blocked_when_variants_present_even_without_downloads(
    tmp_path, monkeypatch,
):
    """A family with variant definitions but no completed downloads
    still has work-in-progress that the user might care about: at
    minimum, the variant spec (provider / repo / files / model name)
    would be lost on delete. The previous check only protected
    against downloaded entries, which silently dropped such families
    when the user pressed d. We now require explicit confirmation."""
    from modelman.manifest import FamilyManifest, save_manifest

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[
            {"id": "q4", "provider": "ollama", "name": "ornith:q4"},
            {"id": "q6", "provider": "ollama", "name": "ornith:q6"},
        ],
        # No mark_downloaded; m.downloaded is empty.
    )
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        # ConfirmModal opens with the variants warning. Say No.
        await pilot.press("n")
        await pilot.pause()
        assert (fam_dir / "ornith.yaml").exists()
        # And on disk, the variants are still there.
        from modelman.manifest import load_manifest

        on_disk = load_manifest("ornith", family_dir=fam_dir)
        assert len(on_disk.variants) == 2
```

with:

```python
@pytest.mark.asyncio
async def test_delete_family_blocked_when_variants_present_even_without_downloads(
    tmp_path, monkeypatch,
):
    """A family with model definitions but no completed downloads
    still has work-in-progress that the user might care about: at
    minimum, the model spec (provider / repo / files / model name)
    would be lost on delete. The check protects against any models,
    queued or downloaded, requiring explicit confirmation either way."""
    q4 = ModelEntry(id="ollama/q4", family="ornith", provider_id="ollama", model_name="ornith:q4")
    q6 = ModelEntry(id="ollama/q6", family="ornith", provider_id="ollama", model_name="ornith:q6")
    reg_path, _state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[q4, q6])

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        # ConfirmModal opens with the variants warning. Say No.
        await pilot.press("n")
        await pilot.pause()

    from modelman.registry import load_registry

    on_disk = load_registry(reg_path)
    assert len(on_disk.models_by_family("ornith")) == 2
```

Replace:

```python
@pytest.mark.asyncio
async def test_delete_family_prompts_for_explicit_confirmation_with_variants(
    tmp_path, monkeypatch,
):
    """The variants-but-no-downloads state must require the user to
    type Yes (not just any key) before the file is removed."""
    from modelman.manifest import FamilyManifest, save_manifest

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[{"id": "q4", "provider": "ollama", "name": "ornith:q4"}],
    )
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        # Confirm "lose the variant definitions"
        await pilot.press("y")
        await pilot.pause()
        assert not (fam_dir / "ornith.yaml").exists()
```

with:

```python
@pytest.mark.asyncio
async def test_delete_family_prompts_for_explicit_confirmation_with_variants(
    tmp_path, monkeypatch,
):
    """The variants-but-no-downloads state must require the user to
    type Yes (not just any key) before the models are removed."""
    q4 = ModelEntry(id="ollama/q4", family="ornith", provider_id="ollama", model_name="ornith:q4")
    reg_path, _state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[q4])

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        # Confirm "lose the model definitions"
        await pilot.press("y")
        await pilot.pause()

    from modelman.registry import load_registry

    assert load_registry(reg_path).models_by_family("ornith") == []
```

- [ ] **Step 5: Rewrite the two "cancel preserves state" tests**

Replace:

```python
@pytest.mark.asyncio
async def test_delete_family_cancel_keeps_empty_family(
    tmp_path, monkeypatch,
):
    """A truly empty family can be deleted, but only after explicit
    Yes. No must always be a no-op regardless of state.

    Pins down a previously-confusing UX bug where the 'No' button
    seemed to wipe the family anyway: it didn't actually wipe, but
    it looked like it did because the prompt text didn't explain
    why the family was empty (the user had previously deleted its
    models through the ModelScreen)."""
    from modelman.manifest import FamilyManifest, save_manifest

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    save_manifest(FamilyManifest(family="ornith"), fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        # Decide No.
        await pilot.press("n")
        await pilot.pause()
        assert (fam_dir / "ornith.yaml").exists(), (
            "Selecting 'No' on the empty-family delete prompt must "
            "preserve the manifest; only 'Yes' deletes."
        )


@pytest.mark.asyncio
async def test_delete_family_cancel_keyword_preserves_file(
    tmp_path, monkeypatch,
):
    """Same as above but using the 'n' keyword binding instead of
    the focused No button; both paths must preserve the file."""
    from modelman.manifest import FamilyManifest, save_manifest

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    save_manifest(FamilyManifest(family="ornith"), fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        # Use the binding-style keypress (not click).
        await pilot.press("escape")
        await pilot.pause()
        assert (fam_dir / "ornith.yaml").exists(), (
            "Escape on the delete-family confirm modal must dismiss "
            "with False and preserve the manifest."
        )
```

with:

```python
@pytest.mark.asyncio
async def test_delete_family_cancel_keeps_empty_family(
    tmp_path, monkeypatch,
):
    """A truly empty family can be deleted, but only after explicit
    Yes. No must always be a no-op regardless of state.

    Pins down a previously-confusing UX bug where the 'No' button
    seemed to wipe the family anyway: it didn't actually wipe, but
    it looked like it did because the prompt text didn't explain
    why the family was empty (the user had previously deleted its
    models through the ModelScreen)."""
    from modelman.state import FamilyState, StateStore, load_state, save_state

    _reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch)
    save_state(StateStore(families={"ornith": FamilyState()}), state_path)

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        # Decide No.
        await pilot.press("n")
        await pilot.pause()

    assert "ornith" in load_state(state_path).families, (
        "Selecting 'No' on the empty-family delete prompt must "
        "preserve the family; only 'Yes' deletes."
    )


@pytest.mark.asyncio
async def test_delete_family_cancel_keyword_preserves_file(
    tmp_path, monkeypatch,
):
    """Same as above but using Escape (dismiss with False) instead of
    the focused No button; both paths must preserve the family."""
    from modelman.state import FamilyState, StateStore, load_state, save_state

    _reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch)
    save_state(StateStore(families={"ornith": FamilyState()}), state_path)

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        # Use the binding-style keypress (not click).
        await pilot.press("escape")
        await pilot.pause()

    assert "ornith" in load_state(state_path).families, (
        "Escape on the delete-family confirm modal must dismiss "
        "with False and preserve the family."
    )
```

- [ ] **Step 6: Rewrite `test_family_screen_reconciles_size_from_provider`**

Replace:

```python
@pytest.mark.asyncio
async def test_family_screen_reconciles_size_from_provider(tmp_path, monkeypatch):
    """A family with no downloaded entries in the manifest should still show
    a non-zero size on the family screen if the provider reports the model
    is on disk."""
    from unittest.mock import MagicMock

    from modelman.manifest import FamilyManifest, save_manifest

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[{"id": "o35", "provider": "ollama", "name": "ornith:35b"}],
    )
    # No downloaded entry, but the model is actually on disk.
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = 22 * 1024**3
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        # Let the reconcile worker finish.
        await pilot.pause()
        await pilot.pause()
        table = app.screen.query_one("DataTable")
        row = table.get_row_at(0)
        # Family, Display, Variants, Downloaded, Size
        assert row[3] == "1"  # downloaded count reflects reality
        assert row[4] == "22.0 GB"
```

with:

```python
@pytest.mark.asyncio
async def test_family_screen_reconciles_size_from_provider(tmp_path, monkeypatch):
    """A family with no downloaded entry in modelman.toml should still
    show a non-zero size on the family screen if the provider reports
    the model is on disk."""
    from unittest.mock import MagicMock

    o35 = ModelEntry(id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b")
    # No downloaded entry in state, but the model is actually on disk
    # per the (stubbed) provider.
    _seed_registry_and_state(tmp_path, monkeypatch, models=[o35])

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = 22 * 1024**3
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        # Let the reconcile worker finish.
        await pilot.pause()
        await pilot.pause()
        table = app.screen.query_one("DataTable")
        row = table.get_row_at(0)
        # Family, Display, Variants, Downloaded, Size
        assert row[3] == "1"  # downloaded count reflects reality
        assert row[4] == "22.0 GB"
```

- [ ] **Step 7: Run the rewritten tests**

Run: `cd /Users/keith/github/ohanaverse/modelman && uv run pytest tests/screens/test_app_navigation.py -v -k "family_screen or add_family or delete_family"`
Expected: all rewritten tests PASS (9 tests, plus the 3 already-passing registry-based tests at the top of the file still pass). The still-`@pytest.mark.skip`ped tests further down report `SKIPPED`, not failed — Task 7 handles those.

- [ ] **Step 8: Commit**

```bash
git add tests/screens/test_app_navigation.py
git commit -m "test(families): migrate passing FamilyScreen tests to registry/state fixtures"
```

---

### Task 7: Un-skip and migrate the `FamilyScreen`→`ModelScreen` integration tests

**Files:**
- Modify: `tests/screens/test_app_navigation.py` (the 14 tests currently marked `@pytest.mark.skip(reason="FamilyScreen migrates to Registry in PR 3")`)

Tests in scope: `test_enter_opens_model_screen`, `test_model_screen_two_pane_lists_providers_and_models`, `test_toggle_download_queues_variant`, `test_status_shows_four_states`, `test_delete_action_noop_on_not_downloaded`, `test_add_then_delete_model_queues_changes`, `test_reconcile_shows_reality_when_manifest_out_of_date`, `test_reconcile_does_not_persist_to_disk_on_cancel`, `test_apply_merges_reconciled_state_into_manifest`, `test_escape_with_pending_shows_dialog_and_apply`, `test_discard_pending_exits_without_applying`, `test_family_screen_reconciles_on_resume_after_apply`, `test_enter_on_model_row_opens_edit_dialog`, `test_enter_on_provider_row_does_not_open_edit_dialog`.

These all exercise the same underlying behavior `ModelScreen`'s own direct-construction tests already cover (see the `_make_screen` helper and the tests below it, added in PR 2) — but going through `FamilyScreen`'s "press Enter to open" path instead of pushing `ModelScreen` directly. Each rewrite below removes the `@pytest.mark.skip` decorator, replaces the `FamilyManifest`/`save_manifest`/`MODELMAN_FAMILY_DIR` setup with `_seed_registry_and_state`, and keeps the rest of each test's interaction/assertions intact (translated to `ModelEntry`/model-id keys where the old code used `VariantSpec`/`variant["id"]`).

**Interfaces:**
- Consumes: `_seed_registry_and_state` (Task 6, Step 1), `ModelEntry`, `Registry`, `load_registry`.

- [ ] **Step 1: `test_enter_opens_model_screen`**

Replace:

```python
@pytest.mark.skip(reason="FamilyScreen migrates to Registry in PR 3")
@pytest.mark.asyncio
async def test_enter_opens_model_screen(tmp_path, monkeypatch):
    from modelman.manifest import FamilyManifest, save_manifest

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    save_manifest(FamilyManifest(family="ornith"), fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.app import ModelmanApp
    from modelman.screens.models import ModelScreen

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        assert isinstance(app.screen, ModelScreen)
```

with:

```python
@pytest.mark.asyncio
async def test_enter_opens_model_screen(tmp_path, monkeypatch):
    from modelman.state import FamilyState, StateStore, save_state

    _reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch)
    save_state(StateStore(families={"ornith": FamilyState()}), state_path)

    from modelman.app import ModelmanApp
    from modelman.screens.models import ModelScreen

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        assert isinstance(app.screen, ModelScreen)
```

- [ ] **Step 2: `test_model_screen_two_pane_lists_providers_and_models`**

Replace:

```python
@pytest.mark.skip(reason="FamilyScreen migrates to Registry in PR 3")
@pytest.mark.asyncio
async def test_model_screen_two_pane_lists_providers_and_models(tmp_path, monkeypatch):
    from modelman.manifest import FamilyManifest, save_manifest

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[
            {"id": "o35", "provider": "ollama", "name": "ornith:35b"},
            {"id": "q4", "provider": "llamacpp", "name": "q4", "repo": "o/r", "files": ["x.gguf"]},
        ],
    )
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text(
        "providers:\n  ollama:\n    type: ollama\n  llamacpp:\n    type: llamacpp\n"
    )

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        tables = app.screen.query("DataTable")
        assert len(tables) == 2
```

with:

```python
@pytest.mark.asyncio
async def test_model_screen_two_pane_lists_providers_and_models(tmp_path, monkeypatch):
    from modelman.registry import Fetch

    o35 = ModelEntry(id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b")
    q4 = ModelEntry(
        id="llamacpp/q4", family="ornith", provider_id="llamacpp", model_name="q4",
        fetch=Fetch(repo="o/r", files=["x.gguf"]),
    )
    _seed_registry_and_state(
        tmp_path, monkeypatch, models=[o35, q4], providers=["ollama", "llamacpp"]
    )

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        tables = app.screen.query("DataTable")
        assert len(tables) == 2
```

- [ ] **Step 3: `test_toggle_download_queues_variant`**

Replace:

```python
@pytest.mark.skip(reason="FamilyScreen migrates to Registry in PR 3")
@pytest.mark.asyncio
async def test_toggle_download_queues_variant(tmp_path, monkeypatch):
    from modelman.manifest import FamilyManifest, save_manifest

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[{"id": "o35", "provider": "ollama", "name": "ornith:35b"}],
    )
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.press("x")
        await pilot.pause()
        pending = app.screen.queued_downloads
        assert "o35" in pending
```

with:

```python
@pytest.mark.asyncio
async def test_toggle_download_queues_variant(tmp_path, monkeypatch):
    o35 = ModelEntry(id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b")
    _seed_registry_and_state(tmp_path, monkeypatch, models=[o35])

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.press("x")
        await pilot.pause()
        pending = app.screen.queued_downloads
        assert "ollama/o35" in pending
```

- [ ] **Step 4: `test_status_shows_four_states`**

Replace:

```python
@pytest.mark.skip(reason="FamilyScreen migrates to Registry in PR 3")
@pytest.mark.asyncio
async def test_status_shows_four_states(tmp_path, monkeypatch):
    """Status column reflects: downloaded, not downloaded, queued for
    download, queued for delete."""
    from unittest.mock import MagicMock

    from modelman.manifest import FamilyManifest, save_manifest

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[
            {"id": "downloaded", "provider": "ollama", "name": "dl"},
            {"id": "missing", "provider": "ollama", "name": "missing"},
        ],
    )
    m.mark_downloaded("downloaded", "/fake/path")
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    # Reconcile: only 'downloaded' reports a size.
    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"

    def fake_size_of(v):
        return 10 if v["id"] == "downloaded" else None

    stub.size_of.side_effect = fake_size_of
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    from textual.widgets import DataTable

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.pause()  # let reconcile settle
        await pilot.press("enter")
        await pilot.pause()

        # Initial: downloaded ✓, missing ○
        mt = app.screen.query_one("#model-table", DataTable)
        rows = {r[0]: r for r in [mt.get_row_at(i) for i in range(mt.row_count)]}
        assert "✓" in rows["dl"][1]
        assert "○" in rows["missing"][1]

        # Toggle download on missing → ↓
        # Cursor is on the first row (downloaded). Move down then x.
        mt.cursor_coordinate = (1, 0)
        await pilot.press("x")
        await pilot.pause()
        mt = app.screen.query_one("#model-table", DataTable)
        rows = {r[0]: r for r in [mt.get_row_at(i) for i in range(mt.row_count)]}
        assert "↓" in rows["missing"][1]

        # Toggle delete on downloaded → ✗
        mt.cursor_coordinate = (0, 0)
        await pilot.press("d")
        await pilot.pause()
        mt = app.screen.query_one("#model-table", DataTable)
        rows = {r[0]: r for r in [mt.get_row_at(i) for i in range(mt.row_count)]}
        assert "✗" in rows["dl"][1]
```

with:

```python
@pytest.mark.asyncio
async def test_status_shows_four_states(tmp_path, monkeypatch):
    """Status column reflects: downloaded, not downloaded, queued for
    download, queued for delete."""
    from unittest.mock import MagicMock

    dl = ModelEntry(id="ollama/dl", family="ornith", provider_id="ollama", model_name="dl")
    missing = ModelEntry(id="ollama/missing", family="ornith", provider_id="ollama", model_name="missing")
    _seed_registry_and_state(
        tmp_path, monkeypatch, models=[dl, missing], downloaded={"ollama/dl": "/fake/path"}
    )

    # Reconcile: only 'dl' reports a size.
    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"

    def fake_size_of(v):
        return 10 if v["id"] == "ollama/dl" else None

    stub.size_of.side_effect = fake_size_of
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    from textual.widgets import DataTable

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.pause()  # let reconcile settle
        await pilot.press("enter")
        await pilot.pause()

        # Initial: dl ✓, missing ○
        mt = app.screen.query_one("#model-table", DataTable)
        rows = {r[0]: r for r in [mt.get_row_at(i) for i in range(mt.row_count)]}
        assert "✓" in rows["dl"][1]
        assert "○" in rows["missing"][1]

        # Toggle download on missing → ↓
        # Cursor is on the first row (dl). Move down then x.
        mt.cursor_coordinate = (1, 0)
        await pilot.press("x")
        await pilot.pause()
        mt = app.screen.query_one("#model-table", DataTable)
        rows = {r[0]: r for r in [mt.get_row_at(i) for i in range(mt.row_count)]}
        assert "↓" in rows["missing"][1]

        # Toggle delete on dl → ✗
        mt.cursor_coordinate = (0, 0)
        await pilot.press("d")
        await pilot.pause()
        mt = app.screen.query_one("#model-table", DataTable)
        rows = {r[0]: r for r in [mt.get_row_at(i) for i in range(mt.row_count)]}
        assert "✗" in rows["dl"][1]
```

- [ ] **Step 5: `test_delete_action_noop_on_not_downloaded`**

Replace:

```python
@pytest.mark.skip(reason="FamilyScreen migrates to Registry in PR 3")
@pytest.mark.asyncio
async def test_delete_action_noop_on_not_downloaded(tmp_path, monkeypatch):
    """Pressing 'd' on a not-downloaded variant must not queue a delete."""
    from unittest.mock import MagicMock

    from modelman.manifest import FamilyManifest, save_manifest

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[{"id": "missing", "provider": "ollama", "name": "missing"}],
    )
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = None
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        assert app.screen.queued_deletes == {}
```

with:

```python
@pytest.mark.asyncio
async def test_delete_action_noop_on_not_downloaded(tmp_path, monkeypatch):
    """Pressing 'd' on a not-downloaded variant must not queue a delete."""
    from unittest.mock import MagicMock

    missing = ModelEntry(id="ollama/missing", family="ornith", provider_id="ollama", model_name="missing")
    _seed_registry_and_state(tmp_path, monkeypatch, models=[missing])

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = None
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        assert app.screen.queued_deletes == {}
```

- [ ] **Step 6: `test_add_then_delete_model_queues_changes`**

Replace:

```python
@pytest.mark.skip(reason="FamilyScreen migrates to Registry in PR 3")
@pytest.mark.asyncio
async def test_add_then_delete_model_queues_changes(tmp_path, monkeypatch):
    from unittest.mock import MagicMock

    from textual.widgets import Input

    from modelman.manifest import FamilyManifest, save_manifest

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[{"id": "o35", "provider": "ollama", "name": "ornith:35b"}],
    )
    m.mark_downloaded("o35", str(tmp_path / "downloaded"))
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = 10 * 1024**3
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        assert "o35" in app.screen.queued_deletes
        await pilot.press("a")
        await pilot.pause()
        # Single-input dialog: focus the model field and type the
        # ollama tag. New id scheme derives from provider+name.
        app.screen.query_one("#model", Input).focus()
        for ch in "ornith:8b":
            await pilot.press(ch)
        await pilot.press("enter")
        await pilot.pause()
        added_ids = [v["id"] for v in app.screen.manifest.variants]
        assert "ollama/ornith:8b" in added_ids
```

with:

```python
@pytest.mark.asyncio
async def test_add_then_delete_model_queues_changes(tmp_path, monkeypatch):
    from unittest.mock import MagicMock

    from textual.widgets import Input

    o35 = ModelEntry(id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b")
    _seed_registry_and_state(
        tmp_path, monkeypatch, models=[o35], downloaded={"ollama/o35": str(tmp_path / "downloaded")}
    )

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = 10 * 1024**3
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        assert "ollama/o35" in app.screen.queued_deletes
        await pilot.press("a")
        await pilot.pause()
        # Single-input dialog: focus the model field and type the
        # ollama tag. New id scheme derives from provider+name.
        app.screen.query_one("#model", Input).focus()
        for ch in "ornith:8b":
            await pilot.press(ch)
        await pilot.press("enter")
        await pilot.pause()
        added_ids = [m.id for m in app.screen.registry.models]
        assert "ollama/ornith:8b" in added_ids
```

- [ ] **Step 7: `test_reconcile_shows_reality_when_manifest_out_of_date`**

Replace:

```python
@pytest.mark.skip(reason="FamilyScreen migrates to Registry in PR 3")
@pytest.mark.asyncio
async def test_reconcile_shows_reality_when_manifest_out_of_date(tmp_path, monkeypatch):
    """If a model is actually downloaded on disk but the manifest doesn't know,
    the model screen should show it as downloaded (✓) and show its real size."""
    from unittest.mock import MagicMock

    from modelman.manifest import FamilyManifest, save_manifest

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[{"id": "o35", "provider": "ollama", "name": "ornith:35b"}],
    )
    # Note: manifest has NO downloaded entry, but the model is on disk.
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = 22 * 1024**3
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    from modelman.app import ModelmanApp

    app = ModelmanApp(family="ornith")
    async with app.run_test() as pilot:
        await pilot.pause()
        from textual.widgets import DataTable

        mt = app.screen.query_one("#model-table", DataTable)
        row = mt.get_row_at(0)
        assert row[1] == "[green]✓[/green]"  # status
        assert row[2] == "22.0 GB"  # size
```

with:

```python
@pytest.mark.asyncio
async def test_reconcile_shows_reality_when_manifest_out_of_date(tmp_path, monkeypatch):
    """If a model is actually downloaded on disk but modelman.toml
    doesn't know, the model screen should show it as downloaded (✓)
    and show its real size."""
    from unittest.mock import MagicMock

    o35 = ModelEntry(id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b")
    # Note: state has NO downloaded entry, but the model is on disk.
    _seed_registry_and_state(tmp_path, monkeypatch, models=[o35])

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = 22 * 1024**3
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    from modelman.app import ModelmanApp

    app = ModelmanApp(family="ornith")
    async with app.run_test() as pilot:
        await pilot.pause()
        from textual.widgets import DataTable

        mt = app.screen.query_one("#model-table", DataTable)
        row = mt.get_row_at(0)
        assert row[1] == "[green]✓[/green]"  # status
        assert row[2] == "22.0 GB"  # size
```

- [ ] **Step 8: `test_reconcile_does_not_persist_to_disk_on_cancel`**

Replace:

```python
@pytest.mark.skip(reason="FamilyScreen migrates to Registry in PR 3")
@pytest.mark.asyncio
async def test_reconcile_does_not_persist_to_disk_on_cancel(tmp_path, monkeypatch):
    """Reconcile is in-memory only until Apply. Cancelling out of the dialog
    (or having no queue at all) must not write the manifest."""
    from unittest.mock import MagicMock

    from modelman.manifest import FamilyManifest, load_manifest, save_manifest

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[{"id": "o35", "provider": "ollama", "name": "ornith:35b"}],
    )
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = 22 * 1024**3
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    from modelman.app import ModelmanApp

    app = ModelmanApp(family="ornith")
    async with app.run_test() as pilot:
        await pilot.pause()
        # No queue, so escape pops without dialog. Manifest must stay untouched.
        await pilot.press("escape")
        await pilot.pause()
    m2 = load_manifest("ornith")
    assert "o35" not in m2.downloaded
```

with:

```python
@pytest.mark.asyncio
async def test_reconcile_does_not_persist_to_disk_on_cancel(tmp_path, monkeypatch):
    """Reconcile is in-memory only until Apply. Cancelling out of the dialog
    (or having no queue at all) must not write modelman.toml."""
    from unittest.mock import MagicMock

    o35 = ModelEntry(id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b")
    _reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[o35])

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = 22 * 1024**3
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    from modelman.app import ModelmanApp

    app = ModelmanApp(family="ornith")
    async with app.run_test() as pilot:
        await pilot.pause()
        # No queue, so escape pops without dialog. State must stay untouched.
        await pilot.press("escape")
        await pilot.pause()

    from modelman.state import load_state

    assert not load_state(state_path).get("ollama/o35").downloaded
```

- [ ] **Step 9: `test_apply_merges_reconciled_state_into_manifest`**

Replace:

```python
@pytest.mark.skip(reason="FamilyScreen migrates to Registry in PR 3")
@pytest.mark.asyncio
async def test_apply_merges_reconciled_state_into_manifest(tmp_path, monkeypatch):
    """When the user Applies, reconciled entries that aren't yet in the manifest
    get written to disk. The existing apply path also runs queued downloads."""
    from unittest.mock import MagicMock

    from textual.widgets import Button, DataTable

    from modelman.manifest import FamilyManifest, load_manifest, save_manifest
    from modelman.screens.status import StatusScreen

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[
            # o35 is on disk per reconcile but not in the manifest.
            {"id": "o35", "provider": "ollama", "name": "ornith:35b"},
            # q8 is missing entirely — use this to trigger the queue + apply.
            {"id": "q8", "provider": "ollama", "name": "ornith:8b"},
        ],
    )
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"

    def fake_size_of(v):
        return 22 * 1024**3 if v["id"] == "o35" else None

    stub.size_of.side_effect = fake_size_of
    stub.download.return_value = "ollama:ornith:8b"
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    from modelman.app import ModelmanApp

    app = ModelmanApp(family="ornith")
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.pause()  # let reconcile settle
        # Queue a download for q8 (which is not downloaded), so escape triggers
        # the apply dialog.
        mt = app.screen.query_one("#model-table", DataTable)
        # Click on the q8 row.
        mt.cursor_coordinate = (1, 0)
        await pilot.press("x")
        await pilot.pause()
        await pilot.press("escape")
        await pilot.pause()
        for btn in app.screen.query(Button):
            if btn.id == "apply":
                btn.press()
                break
        # StatusScreen takes over; wait for it to finish applying.
        for _ in range(50):
            await pilot.pause()
            cur = app.screen
            if isinstance(cur, StatusScreen) and cur.done:
                break
    m2 = load_manifest("ornith")
    assert "o35" in m2.downloaded
```

with:

```python
@pytest.mark.asyncio
async def test_apply_merges_reconciled_state_into_manifest(tmp_path, monkeypatch):
    """When the user Applies, reconciled entries that aren't yet in
    modelman.toml get written to disk. The existing apply path also
    runs queued downloads."""
    from unittest.mock import MagicMock

    from textual.widgets import Button, DataTable

    from modelman.screens.status import StatusScreen

    o35 = ModelEntry(id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b")
    q8 = ModelEntry(id="ollama/q8", family="ornith", provider_id="ollama", model_name="ornith:8b")
    _reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[o35, q8])

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"

    def fake_size_of(v):
        return 22 * 1024**3 if v["id"] == "ollama/o35" else None

    stub.size_of.side_effect = fake_size_of
    stub.download.return_value = "ollama:ornith:8b"
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    from modelman.app import ModelmanApp

    app = ModelmanApp(family="ornith")
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.pause()  # let reconcile settle
        # Queue a download for q8 (which is not downloaded), so escape triggers
        # the apply dialog.
        mt = app.screen.query_one("#model-table", DataTable)
        # Click on the q8 row.
        mt.cursor_coordinate = (1, 0)
        await pilot.press("x")
        await pilot.pause()
        await pilot.press("escape")
        await pilot.pause()
        for btn in app.screen.query(Button):
            if btn.id == "apply":
                btn.press()
                break
        # StatusScreen takes over; wait for it to finish applying.
        for _ in range(50):
            await pilot.pause()
            cur = app.screen
            if isinstance(cur, StatusScreen) and cur.done:
                break

    from modelman.state import load_state

    assert load_state(state_path).get("ollama/o35").downloaded
```

- [ ] **Step 10: `test_escape_with_pending_shows_dialog_and_apply`**

Replace:

```python
@pytest.mark.skip(reason="FamilyScreen migrates to Registry in PR 3")
@pytest.mark.asyncio
async def test_escape_with_pending_shows_dialog_and_apply(tmp_path, monkeypatch):
    from unittest.mock import MagicMock

    from modelman.manifest import FamilyManifest, save_manifest

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[{"id": "o35", "provider": "ollama", "name": "ornith:35b"}],
    )
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.providers import registry

    original_get = registry.ProviderRegistry.get
    stub = MagicMock()
    stub.download.return_value = "/tmp/fake"
    stub.name = "ollama"

    def fake_get(name, cfg):
        if name == "ollama":
            return stub
        return original_get(name, cfg)

    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(fake_get))

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.press("x")
        await pilot.press("escape")
        await pilot.pause()
        from textual.widgets import Button

        for btn in app.screen.query(Button):
            if btn.id == "apply":
                btn.press()
                break
        # Wait for the StatusScreen worker to finish.
        from modelman.screens.status import StatusScreen
        for _ in range(50):
            await pilot.pause()
            cur = app.screen
            if isinstance(cur, StatusScreen) and cur.done:
                break
        stub.download.assert_called()
        from modelman.manifest import load_manifest

        m2 = load_manifest("ornith")
        assert "o35" in m2.downloaded
```

with:

```python
@pytest.mark.asyncio
async def test_escape_with_pending_shows_dialog_and_apply(tmp_path, monkeypatch):
    from unittest.mock import MagicMock

    o35 = ModelEntry(id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b")
    _reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[o35])

    from modelman.providers import registry

    original_get = registry.ProviderRegistry.get
    stub = MagicMock()
    stub.download.return_value = "/tmp/fake"
    stub.name = "ollama"

    def fake_get(name, cfg):
        if name == "ollama":
            return stub
        return original_get(name, cfg)

    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(fake_get))

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.press("x")
        await pilot.press("escape")
        await pilot.pause()
        from textual.widgets import Button

        for btn in app.screen.query(Button):
            if btn.id == "apply":
                btn.press()
                break
        # Wait for the StatusScreen worker to finish.
        from modelman.screens.status import StatusScreen
        for _ in range(50):
            await pilot.pause()
            cur = app.screen
            if isinstance(cur, StatusScreen) and cur.done:
                break
        stub.download.assert_called()

    from modelman.state import load_state

    assert load_state(state_path).get("ollama/o35").downloaded
```

- [ ] **Step 11: `test_discard_pending_exits_without_applying`**

Replace:

```python
@pytest.mark.skip(reason="FamilyScreen migrates to Registry in PR 3")
@pytest.mark.asyncio
async def test_discard_pending_exits_without_applying(tmp_path, monkeypatch):
    from modelman.manifest import FamilyManifest, load_manifest, save_manifest

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[{"id": "o35", "provider": "ollama", "name": "ornith:35b"}],
    )
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from textual.widgets import Button

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        # Queue a download on the existing variant.
        await pilot.press("x")
        await pilot.pause()
        assert "o35" in app.screen.queued_downloads
        # Open the exit dialog.
        await pilot.press("escape")
        await pilot.pause()
        # Press the Discard button.
        for btn in app.screen.query(Button):
            if btn.id == "discard":
                btn.press()
                break
        await pilot.pause()
        # We should be back on the family screen.
        from modelman.screens.families import FamilyScreen

        assert isinstance(app.screen, FamilyScreen)
        # Manifest on disk should be unchanged: no downloaded entry for o35.
        m2 = load_manifest("ornith")
        assert "o35" not in m2.downloaded
```

with:

```python
@pytest.mark.asyncio
async def test_discard_pending_exits_without_applying(tmp_path, monkeypatch):
    o35 = ModelEntry(id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b")
    _reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[o35])

    from textual.widgets import Button

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        # Queue a download on the existing variant.
        await pilot.press("x")
        await pilot.pause()
        assert "ollama/o35" in app.screen.queued_downloads
        # Open the exit dialog.
        await pilot.press("escape")
        await pilot.pause()
        # Press the Discard button.
        for btn in app.screen.query(Button):
            if btn.id == "discard":
                btn.press()
                break
        await pilot.pause()
        # We should be back on the family screen.
        from modelman.screens.families import FamilyScreen

        assert isinstance(app.screen, FamilyScreen)

    from modelman.state import load_state

    # State on disk should be unchanged: no downloaded entry for o35.
    assert not load_state(state_path).get("ollama/o35").downloaded
```

- [ ] **Step 12: `test_family_screen_reconciles_on_resume_after_apply`**

Replace:

```python
@pytest.mark.skip(reason="FamilyScreen migrates to Registry in PR 3")
@pytest.mark.asyncio
async def test_family_screen_reconciles_on_resume_after_apply(tmp_path, monkeypatch):
    """After popping back from StatusScreen (apply completed), the
    FamilyScreen should re-reconcile so the SIZE and DOWNLOADED columns
    reflect the new on-disk state. Without this, deleting a model left
    the family row showing the pre-delete size until the user pressed 'r'.
    """
    from unittest.mock import MagicMock

    from modelman.manifest import FamilyManifest, save_manifest

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    # Pre-condition: model is on disk per the manifest, with a size entry.
    m = FamilyManifest(
        family="ornith",
        variants=[
            {"id": "o35", "provider": "ollama", "name": "ornith:35b"},
            {"id": "o70", "provider": "ollama", "name": "ornith:70b"},
        ],
    )
    m.mark_downloaded("o35", "/tmp/ollama/ornith:35b")
    m.downloaded["o35"]["size_bytes"] = 22 * 1024**3
    m.mark_downloaded("o70", "/tmp/ollama/ornith:70b")
    m.downloaded["o70"]["size_bytes"] = 44 * 1024**3
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    # Initial stub: both models present, both at their original sizes.
    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"

    def fake_size_of(variant):
        if variant["id"] == "o35":
            return 22 * 1024**3
        if variant["id"] == "o70":
            return 44 * 1024**3
        return None

    stub.size_of.side_effect = fake_size_of
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        # Wait for the initial reconcile to complete on mount.
        for _ in range(15):
            await pilot.pause()
            table = app.screen.query_one("DataTable")
            if table.get_row_at(0)[4] != "—":
                break
        table = app.screen.query_one("DataTable")
        assert table.get_row_at(0)[4] == "66.0 GB"  # 22 + 44

        # Now simulate: o35 was deleted on disk. Reload the manifest to
        # match, and stub size_of to return None for o35.
        m_after = FamilyManifest(
            family="ornith",
            variants=[{"id": "o70", "provider": "ollama", "name": "ornith:70b"}],
        )
        m_after.mark_downloaded("o70", "/tmp/ollama/ornith:70b")
        m_after.downloaded["o70"]["size_bytes"] = 44 * 1024**3
        save_manifest(m_after, fam_dir / "ornith.yaml")

        def post_delete_size(variant):
            return 44 * 1024**3 if variant["id"] == "o70" else None

        stub.size_of.side_effect = post_delete_size

        # Push and pop a dummy screen to fire on_screen_resume on
        # FamilyScreen (mirrors what popping from StatusScreen does).
        from textual.screen import Screen
        from textual.widgets import Static

        class _Interstitial(Screen):
            def compose(self):
                yield Static("interstitial")

        app.push_screen(_Interstitial())
        await pilot.pause()
        app.pop_screen()
        # Reconcile is a worker; let it finish.
        for _ in range(40):
            await pilot.pause()
            table = app.screen.query_one("DataTable")
            if table.get_row_at(0)[3] == "1":
                break

        row = app.screen.query_one("DataTable").get_row_at(0)
        # downloaded count: 1 (o35 deleted, o70 still present)
        assert row[3] == "1"
        # size: only o70's 44 GB
        assert row[4] == "44.0 GB"
```

with:

```python
@pytest.mark.asyncio
async def test_family_screen_reconciles_on_resume_after_apply(tmp_path, monkeypatch):
    """After popping back from StatusScreen (apply completed), the
    FamilyScreen should re-reconcile so the SIZE and DOWNLOADED columns
    reflect the new on-disk state. Without this, deleting a model left
    the family row showing the pre-delete size until the user pressed 'r'.
    """
    from unittest.mock import MagicMock

    # Pre-condition: both models on disk per state.
    o35 = ModelEntry(id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b")
    o70 = ModelEntry(id="ollama/o70", family="ornith", provider_id="ollama", model_name="ornith:70b")
    reg_path, state_path = _seed_registry_and_state(
        tmp_path, monkeypatch, models=[o35, o70],
        downloaded={"ollama/o35": "/tmp/ollama/ornith:35b", "ollama/o70": "/tmp/ollama/ornith:70b"},
    )

    # Initial stub: both models present, both at their original sizes.
    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"

    def fake_size_of(variant):
        if variant["id"] == "ollama/o35":
            return 22 * 1024**3
        if variant["id"] == "ollama/o70":
            return 44 * 1024**3
        return None

    stub.size_of.side_effect = fake_size_of
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        # Wait for the initial reconcile to complete on mount.
        for _ in range(15):
            await pilot.pause()
            table = app.screen.query_one("DataTable")
            if table.get_row_at(0)[4] != "—":
                break
        table = app.screen.query_one("DataTable")
        assert table.get_row_at(0)[4] == "66.0 GB"  # 22 + 44

        # Now simulate: o35 was deleted on disk. Update the registry to
        # match (mirrors what PendingChanges.apply() does on delete —
        # remove the ModelEntry and its state), and stub size_of to
        # return None for o35.
        from modelman.registry import load_registry, save_registry
        from modelman.state import load_state, save_state

        reg = load_registry(reg_path)
        reg.models = [m for m in reg.models if m.id != "ollama/o35"]
        save_registry(reg, reg_path)
        state = load_state(state_path)
        state.models.pop("ollama/o35", None)
        save_state(state, state_path)

        def post_delete_size(variant):
            return 44 * 1024**3 if variant["id"] == "ollama/o70" else None

        stub.size_of.side_effect = post_delete_size

        # Push and pop a dummy screen to fire on_screen_resume on
        # FamilyScreen (mirrors what popping from StatusScreen does).
        from textual.screen import Screen
        from textual.widgets import Static

        class _Interstitial(Screen):
            def compose(self):
                yield Static("interstitial")

        app.push_screen(_Interstitial())
        await pilot.pause()
        app.pop_screen()
        # Reconcile is a worker; let it finish.
        for _ in range(40):
            await pilot.pause()
            table = app.screen.query_one("DataTable")
            if table.get_row_at(0)[3] == "1":
                break

        row = app.screen.query_one("DataTable").get_row_at(0)
        # downloaded count: 1 (o35 deleted, o70 still present)
        assert row[3] == "1"
        # size: only o70's 44 GB
        assert row[4] == "44.0 GB"
```

- [ ] **Step 13: `test_enter_on_model_row_opens_edit_dialog`**

Replace:

```python
@pytest.mark.skip(reason="FamilyScreen migrates to Registry in PR 3")
@pytest.mark.asyncio
async def test_enter_on_model_row_opens_edit_dialog(tmp_path, monkeypatch):
    """Pressing Enter while a model row is highlighted must open the
    add/edit dialog for that model, so the user can change the model
    name without first pressing 'e'."""
    from modelman.manifest import FamilyManifest, save_manifest
    from modelman.screens.forms import ModelForm

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[{"id": "o35", "provider": "ollama", "name": "ornith:35b"}],
    )
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text(
        "providers:\n  ollama: {type: ollama}\n"
    )

    from unittest.mock import MagicMock

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = None
    monkeypatch.setattr(
        registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub)
    )

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
        # Drill into the family.
        await pilot.press("enter")
        await pilot.pause()
        # By default the provider-table has focus on mount, so Tab
        # into the model pane; the first model row is already
        # highlighted. Press Enter to open the edit dialog for o35.
        await pilot.press("tab")
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()

        assert captured, "ModelForm was not pushed on Enter"
        form = captured[0]
        # Edit-mode pre-fill: model_input field has the ollama tag.
        from textual.widgets import Input
        model_input = form.query_one("#model", Input)
        assert model_input.value == "ornith:35b"
```

with:

```python
@pytest.mark.asyncio
async def test_enter_on_model_row_opens_edit_dialog(tmp_path, monkeypatch):
    """Pressing Enter while a model row is highlighted must open the
    add/edit dialog for that model, so the user can change the model
    name without first pressing 'e'."""
    from modelman.screens.forms import ModelForm

    o35 = ModelEntry(id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b")
    _seed_registry_and_state(tmp_path, monkeypatch, models=[o35])

    from unittest.mock import MagicMock

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = None
    monkeypatch.setattr(
        registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub)
    )

    from modelman.app import ModelmanApp

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
        # Drill into the family.
        await pilot.press("enter")
        await pilot.pause()
        # By default the provider-table has focus on mount, so Tab
        # into the model pane; the first model row is already
        # highlighted. Press Enter to open the edit dialog for o35.
        await pilot.press("tab")
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()

        assert captured, "ModelForm was not pushed on Enter"
        form = captured[0]
        # Edit-mode pre-fill: model_input field has the ollama tag.
        from textual.widgets import Input
        model_input = form.query_one("#model", Input)
        assert model_input.value == "ornith:35b"
```

- [ ] **Step 14: `test_enter_on_provider_row_does_not_open_edit_dialog`**

Replace:

```python
@pytest.mark.skip(reason="FamilyScreen migrates to Registry in PR 3")
@pytest.mark.asyncio
async def test_enter_on_provider_row_does_not_open_edit_dialog(tmp_path, monkeypatch):
    """Enter on a provider row (left pane) only changes the right
    pane; it must NOT open the edit dialog."""
    from textual.widgets import DataTable

    from modelman.manifest import FamilyManifest, save_manifest
    from modelman.screens.forms import ModelForm

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[
            {"id": "o35", "provider": "ollama", "name": "ornith:35b"},
            {"id": "q8", "provider": "llamacpp", "name": "x.gguf",
             "repo": "foo/bar", "files": ["x.gguf"]},
        ],
    )
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text(
        "providers:\n  ollama: {type: ollama}\n"
        "  llamacpp: {type: llamacpp}\n"
    )
    from unittest.mock import MagicMock

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = None
    monkeypatch.setattr(
        registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub)
    )

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
        await pilot.press("enter")  # into family
        await pilot.pause()
        # Tab to the provider-table (left pane) before pressing Enter,
        # so the test is checking what happens when Enter is pressed
        # while the provider-table has focus.
        provider_table = app.screen.query_one("#provider-table", DataTable)
        provider_table.focus()
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        assert captured == [], (
            "Enter on provider-table row should not open the edit dialog"
        )
```

with:

```python
@pytest.mark.asyncio
async def test_enter_on_provider_row_does_not_open_edit_dialog(tmp_path, monkeypatch):
    """Enter on a provider row (left pane) only changes the right
    pane; it must NOT open the edit dialog."""
    from textual.widgets import DataTable

    from modelman.registry import Fetch
    from modelman.screens.forms import ModelForm

    o35 = ModelEntry(id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b")
    q8 = ModelEntry(
        id="llamacpp/q8", family="ornith", provider_id="llamacpp", model_name="x.gguf",
        fetch=Fetch(repo="foo/bar", files=["x.gguf"]),
    )
    _seed_registry_and_state(
        tmp_path, monkeypatch, models=[o35, q8], providers=["ollama", "llamacpp"]
    )

    from unittest.mock import MagicMock

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = None
    monkeypatch.setattr(
        registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub)
    )

    from modelman.app import ModelmanApp

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
        await pilot.press("enter")  # into family
        await pilot.pause()
        # Tab to the provider-table (left pane) before pressing Enter,
        # so the test is checking what happens when Enter is pressed
        # while the provider-table has focus.
        provider_table = app.screen.query_one("#provider-table", DataTable)
        provider_table.focus()
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        assert captured == [], (
            "Enter on provider-table row should not open the edit dialog"
        )
```

- [ ] **Step 15: Run the whole suite**

Run: `cd /Users/keith/github/ohanaverse/modelman && uv run pytest -v`
Expected: every test PASSes, zero `SKIPPED` remaining that mention "FamilyScreen migrates to Registry in PR 3" (confirm with `uv run pytest -v 2>&1 | grep -c SKIPPED` — should no longer include those 14).

- [ ] **Step 16: Commit**

```bash
git add tests/screens/test_app_navigation.py
git commit -m "test(families): un-skip and migrate FamilyScreen-to-ModelScreen integration tests to registry/state"
```

---

### Task 8: Full verification and cleanup pass

**Files:** none new — verification only.

- [ ] **Step 1: Run the full test suite, lint, and typecheck**

Run: `cd /Users/keith/github/ohanaverse/modelman && uv run pytest -v && uv run ruff check src/ tests/ && uv run mypy src/`
Expected: all tests PASS, ruff reports no findings, mypy reports no errors. If mypy flags `families.py` (e.g. `self.registry_path: Path = Path()` placeholder in `__init__` before `on_mount` sets the real value — acceptable per the constructor pattern `ModelScreen` doesn't share since it takes these as required constructor args, but `FamilyScreen` takes none), fix the specific error rather than adding `# type: ignore`.

- [ ] **Step 2: Grep for any remaining `manifest.py` usage in the screens package**

Run: `cd /Users/keith/github/ohanaverse/modelman && grep -rn "FamilyManifest\|get_family_dir\|save_manifest\|load_manifest\|MODELMAN_FAMILY_DIR\|MODELMAN_CONFIG" src/modelman/screens/`
Expected: no matches. (`config.py`/`manifest.py` themselves are untouched and may still be referenced from `modelman/cli.py`'s `migrate` command or similar — that's out of scope; this grep only checks the `screens/` package this PR touched.)

- [ ] **Step 3: Manual smoke test**

Run: `cd /Users/keith/github/ohanaverse/modelman && uv run modelman` (or however the TUI entry point is invoked — check `pyproject.toml`'s `[project.scripts]` if `modelman` isn't on PATH) against a real or scratch `~/.config/local-ai/registry.toml` + `modelman.toml`. Confirm: family list renders, 'a' adds a family, Enter opens `ModelScreen` without crashing, 'e' edits a family's display name, 'd' on an empty family deletes it. This is the check that catches anything the test suite's mocked providers don't — e.g. a real `ProviderRegistry.get()` call path this plan didn't exercise.

- [ ] **Step 4: Final commit (only if Steps 1-3 required fixes)**

```bash
git add -A
git commit -m "fix(families): address PR 3 verification pass findings"
```

(Skip this step entirely if Steps 1–3 required no changes — don't create an empty commit.)
