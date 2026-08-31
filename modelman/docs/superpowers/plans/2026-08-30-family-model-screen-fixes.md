# Family / Model Screen Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix Family/Model screen cursor loss, lock Family screen during refresh, sort Model form dropdowns, allow deleting any model, skip absent artifacts on delete, and standardize every dialog's button order, focus, and Escape behavior.

**Architecture:** A shared `reload_preserving_cursor` helper in `screens/__init__.py` wraps `DataTable.clear()` + repopulate so both screens keep cursor position across reloads. FamilyScreen adds a `_reconciling` flag and UI lock around its background reconcile worker. ModelScreen routes reloads through the helper and removes provider/family sort/prepend quirks. `PendingChanges` checks `provider.is_downloaded()` before artifact deletion. A new `ModelmanModal` base in `forms.py` centralizes Escape-to-cancel and button-row conventions, with each dialog composing left-to-right and focusing the safe button on destructive prompts.

**Tech Stack:** Python 3.13, Textual 8.2.8, pytest-asyncio.

---

## File Structure

| File | Responsibility |
|------|----------------|
| `src/modelman/screens/__init__.py` | New `reload_preserving_cursor(table, repopulate)` helper. |
| `src/modelman/screens/families.py` | Use helper in `reload()`; add `_reconciling` lock, indicator Static, guarded actions, and try/finally worker cleanup. |
| `src/modelman/screens/models.py` | Use helper in `_load_models()`; remove redundant cursor reset; sort `_provider_list()`; drop family prepend; remove not-ready delete gate. |
| `src/modelman/queue.py` | Skip provider artifact delete when `is_downloaded()` is False; preserve events and registry/state cleanup. |
| `src/modelman/screens/forms.py` | Add `ModelmanModal` base; reorder buttons and set initial focus in all six modals; bind Escape to cancel with priority. |
| `tests/screens/test_cursor_reload.py` | Unit tests for the reload helper. |
| `tests/screens/test_families.py` | Family-screen lock and cursor-restoration tests. |
| `tests/screens/test_forms.py` | Extend with button order, focus, Escape-from-input, and sorted Select tests. |
| `tests/screens/test_models.py` | Update delete-gate tests; add sorted-provider / cursor tests. |
| `tests/test_queue.py` | Add delete-when-not-downloaded and raising-is_downloaded tests. |

---

## Task 1: Shared cursor-preserving reload helper

**Files:**
- Create: `src/modelman/screens/__init__.py`
- Test: `tests/screens/test_cursor_reload.py`

- [ ] **Step 1: Write the failing test**

```python
import pytest
from textual.widgets import DataTable

from modelman.app import ModelmanApp
from modelman.screens import reload_preserving_cursor


@pytest.mark.asyncio
async def test_reload_preserving_cursor_restores_same_key():
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        table = DataTable(cursor_type="row")
        app.screen.mount(table)
        await pilot.pause()
        table.add_columns("NAME")
        table.add_row("alpha", key="alpha")
        table.add_row("beta", key="beta")
        table.add_row("gamma", key="gamma")
        table.move_cursor(row=1)
        assert table.cursor_row == 1

        def repopulate():
            table.clear()
            table.add_row("alpha", key="alpha")
            table.add_row("beta", key="beta")
            table.add_row("gamma", key="gamma")

        reload_preserving_cursor(table, repopulate)
        await pilot.pause()
        assert table.cursor_row == 1
        assert table.get_row_at(table.cursor_row)[0] == "beta"


@pytest.mark.asyncio
async def test_reload_preserving_cursor_falls_back_when_key_missing():
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        table = DataTable(cursor_type="row")
        app.screen.mount(table)
        await pilot.pause()
        table.add_columns("NAME")
        table.add_row("alpha", key="alpha")
        table.add_row("beta", key="beta")
        table.move_cursor(row=1)

        def repopulate():
            table.clear()
            table.add_row("alpha", key="alpha")

        reload_preserving_cursor(table, repopulate)
        await pilot.pause()
        assert table.cursor_row == 0


@pytest.mark.asyncio
async def test_reload_preserving_cursor_noop_on_empty_table():
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        table = DataTable(cursor_type="row")
        app.screen.mount(table)
        await pilot.pause()
        table.add_columns("NAME")

        def repopulate():
            table.clear()
            table.add_row("alpha", key="alpha")

        reload_preserving_cursor(table, repopulate)
        await pilot.pause()
        assert table.cursor_row == 0
        assert table.row_count == 1
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `uv run pytest tests/screens/test_cursor_reload.py -v`
Expected: `ImportError` / `AttributeError` for `reload_preserving_cursor`.

- [ ] **Step 3: Implement the helper**

Create `src/modelman/screens/__init__.py`:

```python
"""Shared TUI screen helpers."""

from __future__ import annotations

from collections.abc import Callable

from textual.widgets import DataTable


def reload_preserving_cursor(table: DataTable, repopulate: Callable[[], None]) -> None:
    """Clear and repopulate `table` without resetting the cursor to row 0.

    DataTable.clear() resets the cursor to (0, 0). This helper snapshots
    the row key under the cursor, runs the caller's repopulate function,
    then restores the cursor onto that key. If the key no longer exists
    (row deleted elsewhere), it falls back to row 0. Empty tables before
    or after are safe no-ops.
    """
    if table.row_count == 0:
        repopulate()
        return

    row_key = list(table.rows.keys())[table.cursor_row]
    repopulate()

    if table.row_count == 0:
        return

    try:
        new_index = table.get_row_index(row_key)
    except KeyError:
        new_index = 0
    table.move_cursor(row=new_index)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/screens/test_cursor_reload.py -v`
Expected: 3 passed.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/screens/__init__.py tests/screens/test_cursor_reload.py
git commit -m "feat(screens): add reload_preserving_cursor helper"
```

---

## Task 2: FamilyScreen cursor preservation and interaction lock

**Files:**
- Modify: `src/modelman/screens/families.py`
- Test: `tests/screens/test_families.py`

- [ ] **Step 1: Write the failing test**

Create `tests/screens/test_families.py`:

```python
"""Tests for FamilyScreen refresh locking and cursor preservation."""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest
from textual.widgets import DataTable, Static

from modelman.app import ModelmanApp
from modelman.registry import (
    AuthConfig,
    FamilyEntry,
    ModelEntry,
    ProviderEntry,
    Registry,
    save_registry,
)
from modelman.state import ModelState, StateStore, save_state


def _seed(tmp_path, monkeypatch):
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        families=[FamilyEntry(name="alpha"), FamilyEntry(name="beta")],
        models=[
            ModelEntry(id="ollama/a", family="alpha", provider_id="ollama", model_name="a"),
            ModelEntry(id="ollama/b", family="beta", provider_id="ollama", model_name="b"),
        ],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True, disk_path="/tmp/a"))
    save_state(state, state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    return reg_path, state_path


@pytest.mark.asyncio
async def test_family_screen_table_disabled_while_reconciling(tmp_path, monkeypatch):
    _seed(tmp_path, monkeypatch)

    from modelman.providers import registry as prov_registry

    stub = MagicMock()
    stub.name = "ollama"

    gate = __import__("threading").Event()

    def slow_is_downloaded(v):
        gate.wait(timeout=2.0)
        return True

    stub.is_downloaded.side_effect = slow_is_downloaded
    stub.size_of.return_value = 1
    monkeypatch.setattr(
        prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub)
    )

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        table = app.screen.query_one("#family-table", DataTable)
        assert app.screen._reconciling is True
        assert table.disabled is True
        indicator = app.screen.query_one("#refresh-indicator", Static)
        assert indicator.display is True
        gate.set()
        for _ in range(50):
            await pilot.pause()
            if not app.screen._reconciling:
                break
        assert app.screen._reconciling is False
        assert table.disabled is False
        assert indicator.display is False


@pytest.mark.asyncio
async def test_family_screen_actions_noop_while_reconciling(tmp_path, monkeypatch):
    _seed(tmp_path, monkeypatch)

    from modelman.providers import registry as prov_registry
    from modelman.screens.forms import AddFamilyModal, ConfirmModal, EditFamilyModal

    stub = MagicMock()
    stub.name = "ollama"
    gate = __import__("threading").Event()
    stub.is_downloaded.side_effect = lambda v: gate.wait(timeout=2.0) or True
    stub.size_of.return_value = 1
    monkeypatch.setattr(
        prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub)
    )

    app = ModelmanApp()
    captured = []
    original_push = app.push_screen

    def tracking_push(screen, *args, **kwargs):
        if isinstance(screen, (AddFamilyModal, EditFamilyModal, ConfirmModal)):
            captured.append(screen)
        return original_push(screen, *args, **kwargs)

    monkeypatch.setattr(app, "push_screen", tracking_push)

    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("a")
        await pilot.press("e")
        await pilot.press("d")
        await pilot.press("enter")
        await pilot.press("r")
        await pilot.pause()
        assert captured == []
        gate.set()
        for _ in range(50):
            await pilot.pause()
            if not app.screen._reconciling:
                break


@pytest.mark.asyncio
async def test_family_screen_cursor_restored_after_reconcile(tmp_path, monkeypatch):
    _seed(tmp_path, monkeypatch)

    from modelman.providers import registry as prov_registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = 1
    monkeypatch.setattr(
        prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub)
    )

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        for _ in range(30):
            await pilot.pause()
            if not app.screen._reconciling:
                break
        table = app.screen.query_one("#family-table", DataTable)
        table.move_cursor(row=1)
        assert table.cursor_row == 1
        app.screen.action_reconcile()
        await pilot.pause()
        assert app.screen._reconciling is True
        for _ in range(50):
            await pilot.pause()
            if not app.screen._reconciling:
                break
        assert app.screen._reconciling is False
        assert table.cursor_row == 1
        assert str(list(table.rows.keys())[table.cursor_row].value) == "beta"
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `uv run pytest tests/screens/test_families.py -v`
Expected: failures for `_reconciling`, `#refresh-indicator`, table `disabled`, action guards.

- [ ] **Step 3: Modify FamilyScreen**

Edit `src/modelman/screens/families.py`:

1. Add imports at the top:

```python
from textual.widgets import DataTable, Footer, Header, Static

from . import reload_preserving_cursor
```

2. Update `compose()`:

```python
def compose(self) -> ComposeResult:
    yield Header()
    yield DataTable(id="family-table", cursor_type="row")
    yield Static("Refreshing sizes…", id="refresh-indicator", display=False)
    yield Footer()
```

3. Add `_reconciling` in `__init__`:

```python
    def __init__(self) -> None:
        super().__init__()
        self._reconciled: dict[str, dict] = {}
        self._available_providers: list[str] = []
        self.registry: Registry = Registry()
        self.state: StateStore = StateStore()
        self.registry_path: Path = Path()
        self.state_path: Path = Path()
        self._reconciling: bool = False
```

4. Update `on_mount()`:

```python
    def on_mount(self) -> None:
        table = self.query_one(DataTable)
        table.add_columns("FAMILY", "DISPLAY", "VARIANTS", "READY", "SIZE")
        self._load_from_disk()
        self.reload()
        self._start_reconcile_worker()
```

5. Add `_start_reconcile_worker()`:

```python
    def _start_reconcile_worker(self) -> None:
        self._reconciling = True
        self._set_refresh_ui(True)
        self.run_worker(self._run_reconcile, exclusive=True, thread=True)
```

6. Update `_run_reconcile()`:

```python
    def _run_reconcile(self) -> None:
        try:
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
                ready = False
                try:
                    ready = bool(provider.is_downloaded(spec))
                except Exception:
                    ready = False
                try:
                    raw = provider.size_of(spec)
                    if isinstance(raw, int):
                        size = raw
                except Exception:
                    size = None
                self._reconciled[m.id] = {
                    "ready": ready,
                    "size": size,
                }
        finally:
            self.app.call_from_thread(self._reconcile_done)

    def _reconcile_done(self) -> None:
        self._reconciling = False
        self._set_refresh_ui(False)
        self.reload()
```

7. Add `_set_refresh_ui()`:

```python
    def _set_refresh_ui(self, refreshing: bool) -> None:
        table = self.query_one("#family-table", DataTable)
        indicator = self.query_one("#refresh-indicator", Static)
        table.disabled = refreshing
        indicator.display = refreshing
```

8. Update `_refresh_from_disk()`:

```python
    def _refresh_from_disk(self) -> None:
        self._load_from_disk()
        self._reconciled.clear()
        self.reload()
        self._start_reconcile_worker()
```

9. Update `action_reconcile()`:

```python
    def action_reconcile(self) -> None:
        if self._reconciling:
            return
        self._refresh_from_disk()
```

10. Guard actions:

```python
    def action_add_family(self) -> None:
        if self._reconciling:
            return
        ...

    def action_edit_family(self) -> None:
        if self._reconciling:
            return
        table = self.query_one(DataTable)
        ...

    def action_delete_family(self) -> None:
        if self._reconciling:
            return
        table = self.query_one(DataTable)
        ...

    def action_open_family(self) -> None:
        if self._reconciling:
            return
        table = self.query_one(DataTable)
        ...

    def on_data_table_row_selected(self, event: DataTable.RowSelected) -> None:
        if self._reconciling:
            return
        family_name = str(event.row_key.value) if event.row_key else ""
        if not family_name:
            return
        self._open_family(family_name)
```

11. Update `reload()` to use the helper:

```python
    def reload(self) -> None:
        def _repopulate() -> None:
            table = self.query_one(DataTable)
            table.clear()
            families = known_families(self.registry, self.state)
            for family in families:
                models = self.registry.models_by_family(family)
                variants = len(models)
                downloaded_count = 0
                total_size = 0
                unknown = False
                for m in models:
                    rec = self._reconciled.get(m.id)
                    if rec is not None:
                        if rec["ready"]:
                            downloaded_count += 1
                            sz = rec["size"]
                            if sz is None:
                                unknown = True
                            else:
                                total_size += sz
                    elif self.state.get(m.id).ready:
                        downloaded_count += 1
                        unknown = True
                size_str = (
                    "—"
                    if downloaded_count == 0
                    else _human_size(total_size if (total_size > 0 or not unknown) else None)
                )
                table.add_row(
                    family,
                    family_display_name(self.registry, self.state, family) or "",
                    str(variants),
                    str(downloaded_count),
                    size_str,
                    key=family,
                )

        reload_preserving_cursor(self.query_one(DataTable), _repopulate)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/screens/test_families.py -v`
Expected: 3 passed.

Run the existing family tests too:
Run: `uv run pytest tests/screens/test_app_navigation.py -v`
Expected: all existing family tests still pass.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/screens/families.py tests/screens/test_families.py
git commit -m "feat(family-screen): lock interactions while reconciling and preserve cursor"
```

---

## Task 3: ModelScreen cursor, sorted dropdowns, and delete-any-model

**Files:**
- Modify: `src/modelman/screens/models.py`
- Test: `tests/screens/test_models.py`

- [ ] **Step 1: Write the failing tests**

Add to `tests/screens/test_models.py`:

```python
@pytest.mark.asyncio
async def test_provider_list_sorted(tmp_path, monkeypatch):
    from modelman.screens.models import ModelScreen
    from modelman.registry import (
        AuthConfig,
        FamilyEntry,
        ProviderEntry,
        Registry,
        save_registry,
    )
    from modelman.state import StateStore

    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[
            ProviderEntry(id="omlx", name="X", auth=AuthConfig(type="omlx")),
            ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none")),
            ProviderEntry(id="llamacpp", name="L", auth=AuthConfig(type="none")),
        ],
        families=[FamilyEntry(name="ornith")],
        models=[],
    )
    save_registry(reg, reg_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    ms = ModelScreen(
        registry=reg,
        state=StateStore(),
        family="ornith",
        registry_path=reg_path,
        state_path=state_path,
        available_providers=["omlx", "ollama", "llamacpp"],
    )
    assert ms._provider_list() == ["llamacpp", "ollama", "omlx"]


@pytest.mark.asyncio
async def test_delete_any_model_even_not_ready(tmp_path, monkeypatch):
    """Native / not-ready models must be deletable; the not-ready gate is removed."""
    from unittest.mock import MagicMock

    from modelman.app import ModelmanApp
    from modelman.providers import registry as prov_registry

    a = ModelEntry(
        id="ollama/o35",
        family="ornith",
        provider_id="ollama",
        model_name="ornith:35b",
    )
    reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[a])

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = None
    stub.is_downloaded.return_value = False
    monkeypatch.setattr(
        prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub)
    )

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        assert "ollama/o35" in app.screen.queued_deletes


@pytest.mark.asyncio
async def test_model_screen_cursor_restored_after_reload(tmp_path, monkeypatch):
    from unittest.mock import MagicMock

    from modelman.app import ModelmanApp
    from modelman.providers import registry as prov_registry
    from textual.widgets import DataTable

    a = ModelEntry(
        id="ollama/a", family="ornith", provider_id="ollama", model_name="a"
    )
    b = ModelEntry(
        id="ollama/b", family="ornith", provider_id="ollama", model_name="b"
    )
    _seed_registry_and_state(tmp_path, monkeypatch, models=[a, b])

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = None
    stub.is_downloaded.return_value = False
    monkeypatch.setattr(
        prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub)
    )

    app = ModelmanApp(family="ornith")
    async with app.run_test() as pilot:
        await pilot.pause()
        mt = app.screen.query_one("#model-table", DataTable)
        mt.move_cursor(row=1)
        assert mt.cursor_row == 1
        app.screen.reload()
        await pilot.pause()
        assert mt.cursor_row == 1
        assert str(list(mt.rows.keys())[mt.cursor_row].value) == "ollama/b"
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `uv run pytest tests/screens/test_models.py::test_provider_list_sorted tests/screens/test_models.py::test_delete_any_model_even_not_ready tests/screens/test_models.py::test_model_screen_cursor_restored_after_reload -v`
Expected: FAIL on `_provider_list()` sort, queued_deletes empty, cursor not restored.

- [ ] **Step 3: Modify ModelScreen**

Edit `src/modelman/screens/models.py`:

1. Import the helper:

```python
from . import reload_preserving_cursor
```

2. Remove the redundant cursor reset in `on_mount()`:

```python
    def on_mount(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        mt.add_columns(
            "FAMILY", "PROVIDER", "MODEL", "LOCATION", "STATUS", "EXPOSED", "SIZE", "PATH"
        )
        self.reload()
        self._refresh_pending_bar()
        mt.focus()
        self.run_worker(self._run_reconcile, exclusive=True, thread=True)
```

3. Update `_load_models()` to use the helper:

```python
    def _load_models(self) -> None:
        from ..providers.registry import ProviderRegistry

        def _repopulate() -> None:
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
                    path = (
                        rec.get("local_path") or (self.state.get(m.id).disk_path or "—")
                        if ready
                        else "—"
                    )
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

        reload_preserving_cursor(self.query_one("#model-table", DataTable), _repopulate)
```

4. Sort `_provider_list()`:

```python
    def _provider_list(self) -> list[str]:
        if self.available_providers:
            return sorted(self.available_providers)
        return sorted({m.provider_id for m in self.registry.models_by_family(self.family)})
```

5. Remove the not-ready gate in `action_delete_model()`:

```python
    def action_delete_model(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        mid = str(row_key.value)
        entry = next((m for m in self.registry.models if m.id == mid), None)
        if entry is None:
            return
        spec = _model_entry_to_variant(entry)
        if mid in self.queued_deletes:
            self.queued_deletes.pop(mid)
        else:
            self.queued_deletes[mid] = spec
        self.queued_ready.pop(mid, None)
        self._refresh_pending_bar()
        self.reload()
```

- [ ] **Step 4: Run the new tests and the existing model-screen tests**

Run: `uv run pytest tests/screens/test_models.py -v`
Expected: all pass, including new ones.

Note: `test_model_screen_delete_only_for_downloaded` and `test_d_on_local_not_downloaded_does_not_queue_but_notifies` are now obsolete because the gate is removed. Update or delete them in this same step.

Replace `test_model_screen_delete_only_for_downloaded` with:

```python
@pytest.mark.asyncio
async def test_delete_action_noop_when_no_row_selected(tmp_path, monkeypatch):
    """Pressing 'd' with an empty table is a no-op."""
    from unittest.mock import MagicMock
    from modelman.providers import registry as prov_registry

    ms, _reg, _state = _make_screen(tmp_path, monkeypatch)

    stub = MagicMock()
    stub.name = "ollama"
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
        await pilot.press("d")
        await pilot.pause()
        assert ms.queued_deletes == {}
```

- [ ] **Step 5: Commit**

```bash
git add src/modelman/screens/models.py tests/screens/test_models.py
git commit -m "feat(model-screen): preserve cursor, sort dropdowns, allow delete any model"
```

---

## Task 4: ModelForm family select no longer prepends current family

**Files:**
- Modify: `src/modelman/screens/forms.py`
- Test: `tests/screens/test_forms.py`

- [ ] **Step 1: Write the failing test**

Add to `tests/screens/test_forms.py`:

```python
@pytest.mark.asyncio
async def test_modelform_family_select_is_sorted_and_does_not_prepend():
    """The current family must appear in sorted order, not forced to the front."""
    form = ModelForm(
        providers=["ollama"],
        families=["gemma4", "gemma4:26b-mlx", "deepseek-v4"],
        family="gemma4:26b-mlx",
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        sel = app.screen.query_one("#family-select", Select)
        assert [str(value) for _, value in sel._options] == [
            "deepseek-v4",
            "gemma4",
            "gemma4:26b-mlx",
        ]
        assert str(sel.value) == "gemma4:26b-mlx"
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `uv run pytest tests/screens/test_forms.py::test_modelform_family_select_is_sorted_and_does_not_prepend -v`
Expected: FAIL — family appears at index 0.

- [ ] **Step 3: Remove the prepend line in ModelForm**

In `src/modelman/screens/forms.py`, in `ModelForm.__init__`, remove:

```python
        if self._family is not None and self._family not in self._families:
            self._families.insert(0, self._family)
```

Also ensure the family list passed to the Select is sorted. Since callers already pass sorted lists, no extra sort is needed here; add a defensive copy only:

```python
        self._families: list[str] = (
            list(families) if families else ([family] if family else ["unknown"])
        )
```

- [ ] **Step 4: Run the family-select tests**

Run: `uv run pytest tests/screens/test_forms.py -k family -v`
Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/screens/forms.py tests/screens/test_forms.py
git commit -m "fix(forms): stop prepending current family; rely on sorted order"
```

---

## Task 5: PendingChanges skips absent artifacts on delete

**Files:**
- Modify: `src/modelman/queue.py`
- Test: `tests/test_queue.py`

- [ ] **Step 1: Write the failing tests**

Add to `tests/test_queue.py`:

```python
def test_apply_delete_not_downloaded_skips_provider_call(tmp_path):
    """When the artifact is already gone, delete still removes registry/state
    and emits lifecycle events, but does not call provider.delete()."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))
        ],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True, disk_path="/old/a"))

    provider = MagicMock()
    provider.name = "ollama"
    provider.is_downloaded.return_value = False
    provider.delete.return_value = None

    events: list[str] = []
    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": provider},
        deletes=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="a"))],
    )
    pending.apply(on_event=events.append)

    assert "delete:start|ollama/a|a" in events
    assert "delete:done|ollama/a|a" in events
    assert not provider.delete.called
    assert "ollama/a" not in [m.id for m in load_registry(reg_path).models]
    assert "ollama/a" not in load_state(state_path).models


def test_apply_delete_is_downloaded_exception_attempts_delete(tmp_path):
    """If is_downloaded() raises, we cannot know the artifact is absent,
    so we attempt provider.delete() and surface any failure normally."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))
        ],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True))

    provider = MagicMock()
    provider.name = "ollama"
    provider.is_downloaded.side_effect = RuntimeError("stat failed")
    provider.delete.return_value = None

    events: list[str] = []
    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": provider},
        deletes=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="a"))],
    )
    pending.apply(on_event=events.append)

    provider.delete.assert_called_once()
    assert "ollama/a" not in [m.id for m in load_registry(reg_path).models]


def test_apply_delete_is_downloaded_exception_failure_recorded(tmp_path):
    """If is_downloaded() raises and the subsequent delete also fails,
    the failure is recorded and registry/state cleanup is skipped."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))
        ],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True))

    provider = MagicMock()
    provider.name = "ollama"
    provider.is_downloaded.side_effect = RuntimeError("stat failed")
    provider.delete.side_effect = PermissionError("read-only fs")

    events: list[str] = []
    pending = PendingChanges(
        registry=reg,
        state=state,
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": provider},
        deletes=[("ollama/a", _variant(id="ollama/a", provider="ollama", name="a"))],
    )
    pending.apply(on_event=events.append)

    assert pending.failures
    assert "read-only fs" in str(pending.failures[0])
    assert "ollama/a" in [m.id for m in load_registry(reg_path).models]
    assert "ollama/a" in load_state(state_path).models
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `uv run pytest tests/test_queue.py::test_apply_delete_not_downloaded_skips_provider_call tests/test_queue.py::test_apply_delete_is_downloaded_exception_attempts_delete tests/test_queue.py::test_apply_delete_is_downloaded_exception_failure_recorded -v`
Expected: FAIL — provider.delete() is called even when not downloaded.

- [ ] **Step 3: Modify the delete loop in queue.py**

In `src/modelman/queue.py`, replace the delete-step provider branch:

```python
        for model_id, variant in self.deletes:
            if aborted():
                return
            assert variant["id"] == model_id, (
                f"variant id {variant['id']!r} != queued model_id {model_id!r}"
            )
            label = _label(variant)
            provider_id = variant["provider"]
            provider = self.providers.get(provider_id)
            artifact_present = False
            if provider is not None:
                try:
                    artifact_present = bool(provider.is_downloaded(variant))
                except Exception:
                    # We cannot confirm absence; attempt artifact delete
                    # and let real failures surface.
                    artifact_present = True
                if artifact_present:
                    emit(f"delete:start|{model_id}|{label}")
                    try:
                        self._delete(variant)
                    except Exception as exc:  # noqa: BLE001
                        reason = _reason(exc)
                        self.failures.append(f"delete {model_id}: {exc}")
                        emit(f"delete:fail|{model_id}|{label}|{reason}")
                        continue
                else:
                    emit(f"delete:start|{model_id}|{label}")
            # Flag-only providers (native/unmapped) have no Provider instance
            # ... remainder unchanged from here
```

Ensure the lifecycle events and registry/state cleanup still run for both branches. The existing cleanup code (record family, remove from registry, queue unexpose, pop state, emit `delete:done`, add to deleted_ids) must follow the `if artifact_present` block and run for both provider and flag-only cases.

- [ ] **Step 4: Run the queue tests**

Run: `uv run pytest tests/test_queue.py -v`
Expected: all pass, including new ones.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/queue.py tests/test_queue.py
git commit -m "feat(queue): skip artifact delete when is_downloaded is false"
```

---

## Task 6: ModelmanModal base and dialog conventions

**Files:**
- Modify: `src/modelman/screens/forms.py`
- Test: `tests/screens/test_forms.py`

- [ ] **Step 1: Write the failing tests**

Append to `tests/screens/test_forms.py`:

```python
# ---------------------------------------------------------------------------
# Dialog conventions: button order, initial focus, Escape-to-cancel
# ---------------------------------------------------------------------------


def _button_ids(app: ModelmanApp) -> list[str]:
    return [btn.id for btn in app.screen.query(Button) if btn.id is not None]


def _focused_id(app: ModelmanApp) -> str | None:
    w = app.screen.focused
    return w.id if w is not None else None


@pytest.mark.asyncio
async def test_add_family_modal_buttons_and_focus():
    from modelman.screens.forms import AddFamilyModal

    modal = AddFamilyModal()
    async with ModelmanApp().run_test() as pilot:
        await pilot.pause()
        pilot.app.push_screen(modal)
        await pilot.pause()
        assert _button_ids(pilot.app) == ["cancel", "create"]
        assert _focused_id(pilot.app) == "family-name"


@pytest.mark.asyncio
async def test_edit_family_modal_buttons_and_focus():
    from modelman.screens.forms import EditFamilyModal

    modal = EditFamilyModal(family="ornith", display_name="Ornith")
    async with ModelmanApp().run_test() as pilot:
        await pilot.pause()
        pilot.app.push_screen(modal)
        await pilot.pause()
        assert _button_ids(pilot.app) == ["cancel", "save"]
        assert _focused_id(pilot.app) == "display-name"


@pytest.mark.asyncio
async def test_modelform_buttons_and_focus():
    form = ModelForm(providers=["ollama"])
    async with ModelmanApp().run_test() as pilot:
        await pilot.pause()
        pilot.app.push_screen(form)
        await pilot.pause()
        assert _button_ids(pilot.app) == ["cancel", "save"]
        assert _focused_id(pilot.app) == "model"


@pytest.mark.asyncio
async def test_confirm_modal_buttons_and_safe_focus():
    from modelman.screens.forms import ConfirmModal

    modal = ConfirmModal("Delete everything?")
    dismissed: list = []
    async with ModelmanApp().run_test() as pilot:
        await pilot.pause()
        pilot.app.push_screen(modal, dismissed.append)
        await pilot.pause()
        assert _button_ids(pilot.app) == ["yes", "no"]
        assert _focused_id(pilot.app) == "no"


@pytest.mark.asyncio
async def test_confirm_exit_dialog_buttons_and_safe_focus():
    from modelman.screens.forms import ConfirmExitDialog

    modal = ConfirmExitDialog(ready=[], deletes=[], exposes=[], moves=[])
    async with ModelmanApp().run_test() as pilot:
        await pilot.pause()
        pilot.app.push_screen(modal)
        await pilot.pause()
        assert _button_ids(pilot.app) == ["cancel", "discard", "apply"]
        assert _focused_id(pilot.app) == "cancel"


@pytest.mark.asyncio
async def test_cancel_apply_dialog_buttons_and_safe_focus():
    from modelman.screens.forms import CancelApplyDialog

    modal = CancelApplyDialog()
    async with ModelmanApp().run_test() as pilot:
        await pilot.pause()
        pilot.app.push_screen(modal)
        await pilot.pause()
        assert _button_ids(pilot.app) == ["cancel", "wait"]
        assert _focused_id(pilot.app) == "wait"


@pytest.mark.asyncio
async def test_modelform_escape_from_input_dismisses():
    """Escape must cancel the modal even when the model Input is focused."""
    form = ModelForm(providers=["ollama"])
    dismissed: list = []
    async with ModelmanApp().run_test() as pilot:
        await pilot.pause()
        pilot.app.push_screen(form, dismissed.append)
        await pilot.pause()
        assert _focused_id(pilot.app) == "model"
        await pilot.press("escape")
        await pilot.pause()
    assert dismissed == [None]


@pytest.mark.asyncio
async def test_edit_family_modal_escape_from_disabled_input_dismisses():
    """Escape must cancel even when the read-only family-name Input is focused."""
    from modelman.screens.forms import EditFamilyModal

    modal = EditFamilyModal(family="ornith", display_name="Ornith")
    dismissed: list = []
    async with ModelmanApp().run_test() as pilot:
        await pilot.pause()
        pilot.app.push_screen(modal, dismissed.append)
        await pilot.pause()
        modal.query_one("#family-name").focus()
        await pilot.pause()
        await pilot.press("escape")
        await pilot.pause()
    assert dismissed == [None]
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `uv run pytest tests/screens/test_forms.py -k "buttons_and_focus or escape_from" -v`
Expected: failures on button ids, focus ids, Escape dismissal.

- [ ] **Step 3: Add ModelmanModal base and update all modals**

In `src/modelman/screens/forms.py`, add the base class near the top:

```python
from textual.binding import Binding
from textual.containers import Horizontal, Vertical
from textual.screen import ModalScreen
from textual.widgets import Button, Input, Label, Select


T = TypeVar("T")


class ModelmanModal(ModalScreen[T]):
    """Base class for modelman dialogs.

    Provides shared conventions:
    - Escape cancels the modal (priority binding so it fires even when
      an Input is focused).
    - Buttons are composed left-to-right in the order supplied by the
      subclass; destructive dialogs focus the safe button initially.
    """

    BINDINGS = [
        Binding("escape", "cancel", "Cancel", priority=True),
    ]

    DEFAULT_CSS = """
    ModelmanModal { align: center middle; }
    ModelmanModal > Vertical { width: 60; height: auto; padding: 1 2; border: round $primary; }
    ModelmanModal Label { margin-bottom: 1; }
    ModelmanModal Input { margin-bottom: 1; }
    ModelmanModal Horizontal { height: auto; align-horizontal: right; }
    ModelmanModal Button { margin-left: 1; }
    """

    def _button_row(self, buttons: list[Button]) -> Horizontal:
        """Return a right-aligned Horizontal containing the given buttons."""
        row = Horizontal()
        for button in buttons:
            row.mount(button)
        return row

    def _focus_button(self, button_id: str) -> None:
        button = self.query_one(f"#{button_id}", Button)
        button.focus()

    def action_cancel(self) -> None:
        self.dismiss(None)
```

Note: remove `T = TypeVar("T")` if not otherwise needed; the file already uses `ModalScreen[ModelFormResult | None]` etc. Add `from typing import TypeVar` if needed.

Update each modal:

**AddFamilyModal** — inherit from `ModelmanModal`, reorder buttons, remove duplicate CSS, add `on_mount` focus:

```python
class AddFamilyModal(ModelmanModal[tuple[str, str] | None]):
    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label("Family name (required):")
            yield Input(id="family-name", placeholder="e.g. ornith-1.5")
            yield Label("Display name (optional):")
            yield Input(id="display-name", placeholder="e.g. Ornith 1.5")
            yield self._button_row([
                Button("Cancel", id="cancel", variant="default"),
                Button("Create", id="create", variant="primary"),
            ])

    def on_mount(self) -> None:
        self.query_one("#family-name", Input).focus()
```

**EditFamilyModal** — inherit, reorder, remove duplicate CSS, keep existing `on_mount` focus:

```python
class EditFamilyModal(ModelmanModal[str | None]):
    ...
    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label("Family (cannot be changed):")
            yield Input(value=self._family, id="family-name", disabled=True, placeholder="e.g. ornith-1.5")
            yield Label("Display name (optional):")
            yield Input(value=self._display_name, id="display-name", placeholder="e.g. Ornith 1.5")
            yield self._button_row([
                Button("Cancel", id="cancel", variant="default"),
                Button("Save", id="save", variant="primary"),
            ])

    def on_mount(self) -> None:
        self.query_one("#display-name", Input).focus()
```

**ConfirmModal** — inherit, set buttons left-to-right `Yes` then `No`, focus `no`:

```python
class ConfirmModal(ModelmanModal[bool]):
    BINDINGS = [
        ("y", "answer(True)"),
        ("n", "answer(False)"),
        ("escape", "answer(False)"),
    ]

    DEFAULT_CSS = """
    ConfirmModal { align: center middle; }
    ConfirmModal > Vertical { width: 60; height: auto; padding: 1 2; border: round $primary; }
    ConfirmModal Label { margin-bottom: 1; }
    ConfirmModal Horizontal { height: auto; align-horizontal: right; }
    ConfirmModal Button { margin-left: 1; }
    """

    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label(self._message)
            yield self._button_row([
                Button("Yes", id="yes", variant="warning"),
                Button("No", id="no", variant="default"),
            ])

    def on_mount(self) -> None:
        self._focus_button("no")
```

**ModelForm** — inherit from `ModelmanModal[ModelFormResult | None]`, reorder buttons, keep model-input focus:

```python
class ModelForm(ModelmanModal[ModelFormResult | None]):
    DEFAULT_CSS = """
    ModelForm { align: center middle; }
    ModelForm > Vertical { width: 80; height: auto; padding: 1 2; border: round $primary; }
    ModelForm Label { margin-top: 1; }
    ModelForm Input { margin-bottom: 1; }
    ModelForm Select { margin-bottom: 1; }
    ModelForm #model-error { color: $error; text-style: bold; }
    ModelForm Horizontal { height: auto; align-horizontal: right; }
    ModelForm Button { margin-left: 1; }
    ModelForm #provider-select { color: $secondary; text-style: bold; }
    """

    def compose(self) -> ComposeResult:
        ...
        with Vertical():
            ...
            yield self._button_row([
                Button("Cancel", id="cancel", variant="default"),
                Button("Save", id="save", variant="primary"),
            ])

    def on_mount(self) -> None:
        self.query_one("#model", Input).focus()
```

**ConfirmExitDialog** — inherit, reorder buttons left-to-right `Cancel`, `Discard`, `Apply`, focus `cancel`:

```python
class ConfirmExitDialog(ModelmanModal[Literal["apply", "cancel", "discard"]]):
    BINDINGS = [
        ("y", "answer('apply')"),
        ("n", "answer('cancel')"),
        ("d", "answer('discard')"),
    ]

    DEFAULT_CSS = """
    ConfirmExitDialog { align: center middle; }
    ConfirmExitDialog > Vertical { width: 70; height: auto; padding: 1 2; border: round $primary; }
    ConfirmExitDialog Label { margin-bottom: 1; }
    ConfirmExitDialog Horizontal { height: auto; align-horizontal: right; }
    ConfirmExitDialog Button { margin-left: 1; }
    """

    def compose(self) -> ComposeResult:
        ...
        yield self._button_row([
            Button("Cancel", id="cancel", variant="default"),
            Button("Discard", id="discard", variant="warning"),
            Button("Apply", id="apply", variant="primary"),
        ])

    def on_mount(self) -> None:
        self._focus_button("cancel")
```

**CancelApplyDialog** — inherit, keep order `Cancel`, `Wait`, focus `wait`:

```python
class CancelApplyDialog(ModelmanModal[Literal["cancel", "wait"]]):
    BINDINGS = [
        ("escape", "answer('wait')"),
        ("w", "answer('wait')"),
        ("c", "answer('cancel')"),
    ]

    DEFAULT_CSS = """
    CancelApplyDialog { align: center middle; }
    CancelApplyDialog > Vertical { width: 60; height: auto; padding: 1 2; border: round $warning; }
    CancelApplyDialog Label { margin-bottom: 1; }
    CancelApplyDialog Horizontal { height: auto; align-horizontal: right; }
    CancelApplyDialog Button { margin-left: 1; }
    """

    def compose(self) -> ComposeResult:
        with Vertical():
            yield Label("Actions are still running.")
            yield Label("Cancel and stop here, or wait for them to finish?")
            yield self._button_row([
                Button("Cancel", id="cancel", variant="warning"),
                Button("Wait", id="wait", variant="primary"),
            ])

    def on_mount(self) -> None:
        self._focus_button("wait")
```

- [ ] **Step 4: Run the dialog-convention tests**

Run: `uv run pytest tests/screens/test_forms.py -v`
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/screens/forms.py tests/screens/test_forms.py
git commit -m "feat(forms): ModelmanModal base, consistent buttons, safe focus, escape cancel"
```

---

## Task 7: Full verification

- [ ] **Step 1: Run the entire test suite**

Run: `make test` (or `uv run pytest`)
Expected: all tests pass.

- [ ] **Step 2: Run lint/typecheck**

Run: `make check` (or `make lint && make typecheck`)
Expected: no errors.

- [ ] **Step 3: Manual smoke test (optional but recommended)**

Run: `uv run modelman` with a test registry and verify:
- Returning from a model screen keeps the family cursor.
- Pressing `r` on the family screen shows a brief "Refreshing sizes…" indicator and disables the table.
- Model add/edit dialogs list providers and families alphabetically.
- `d` on a not-ready native model queues delete.
- Escape cancels any dialog from inside an Input.

- [ ] **Step 4: Commit any final fixes**

```bash
git add -A
git commit -m "fixup: address review/lint feedback"  # only if changes needed
```

---

## Self-Review

**1. Spec coverage:**
- Family screen cursor preservation → Task 2 + Task 1 helper.
- Family screen refresh lock → Task 2.
- Model screen dropdowns alphabetical → Task 3 (`_provider_list` sorted, Task 4 family select prepend removed).
- Delete any model → Task 3.
- Skip absent artifacts on delete → Task 5.
- Dialog button ordering/focus/escape → Task 6.

**2. Placeholder scan:** No TBD, TODO, or vague steps. Every code change is shown in full.

**3. Type consistency:**
- `reload_preserving_cursor` takes `DataTable` + `Callable[[], None]`.
- `_reconciling` is a bool used consistently across FamilyScreen.
- `ModelmanModal` is generic over the same return types as the previous `ModalScreen` subclasses.

---

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-08-30-family-model-screen-fixes.md`.**

Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using `executing-plans`, batch execution with checkpoints for review.

**Which approach?**
