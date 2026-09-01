# First-Class Families in registry.toml — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make families first-class `[[families]]` entries in `registry.toml` so a family that had models lingers as a 0-variant row after its models are moved out or deleted, until explicitly deleted.

**Architecture:** Add a `FamilyEntry` dataclass and a `families` list field to `Registry` (replacing the derived `families()` method, renamed `derived_families()`). Two module-level helpers — `known_families()` and `family_display_name()` — unify the visibility rule across `FamilyScreen` and `ModelScreen`. `PendingChanges.apply()` records families emptied by deletes/moves and writes a lingering entry; the family screen's Add/Edit/Delete actions write/remove registry entries and drain the legacy `state.families` table via promotion.

**Tech Stack:** Python 3.13, Textual TUI, `tomllib`/`tomli_w` (no external config libs), pytest + pytest-asyncio.

**Spec:** `docs/superpowers/specs/2026-08-29-modelman-first-class-families-design.md`

## Global Constraints

- Python 3.13 only (`requires-python = "==3.13.*"`); run everything with `uv run`.
- No new dependencies — TOML I/O stays on `tomllib`/`tomli_w` via `_toml_io.py`.
- `registry.toml` must remain readable by agent-worktree's non-strict BurntSushi decode: the `[[families]]` section is purely additive; never reorder or rename existing sections.
- Unknown keys on every entry are preserved round-trip (`extra` + `unknown_keys` + `drop_none` + `atomic_write_toml`).
- `state.families` schema is unchanged: entries stay loadable; TUI writers stop creating them and drain them via promotion (`forget_family`).
- Duplicate family names are tolerated (first match wins), matching the existing duplicate-model-id tolerance.

---

### Task 1: Registry schema — `FamilyEntry`, `families` field, `family()`, parse/serialize, rename `families()` → `derived_families()`

**Files:**
- Modify: `src/modelman/registry.py` (add `FamilyEntry`; add `Registry.families` field; add `Registry.family()`; rename `families()` → `derived_families()`; add `_parse_family`/`_family_to_dict`; update `load_registry`/`save_registry`)
- Modify: `src/modelman/screens/families.py:165` (`self.registry.families()` → `self.registry.derived_families()`)
- Modify: `src/modelman/screens/models.py:411` (`self.registry.families()` → `self.registry.derived_families()`)
- Test: `tests/test_registry.py`

**Interfaces:**
- Consumes: nothing new (existing `Registry`, `_toml_io` helpers).
- Produces: `FamilyEntry(name: str, display_name: str | None = None, extra: dict[str, Any])`; `Registry.families: list[FamilyEntry]`; `Registry.family(name) -> FamilyEntry | None`; `Registry.derived_families() -> list[str]` (renamed from `families()`).

- [ ] **Step 1: Write the failing tests**

Add to `tests/test_registry.py`. First extend the import block at the top:

```python
from modelman.registry import (
    AuthConfig,
    Cost,
    FamilyEntry,
    Fetch,
    ModelEntry,
    ProviderEntry,
    Registry,
    RegistryError,
    _default_registry_path,
    load_registry,
    provider_config,
    save_registry,
)
```

Then add these tests (and rename the two existing `families()` tests):

```python
def test_family_entry_round_trip(tmp_path):
    # A [[families]] entry with a display name must survive save/load,
    # since the family screen's DISPLAY column reads it back.
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))],
        families=[FamilyEntry(name="deepseek-v4", display_name="DeepSeek V4")],
    )
    path = tmp_path / "registry.toml"
    save_registry(registry, path)
    loaded = load_registry(path)
    assert loaded.family("deepseek-v4") == FamilyEntry(
        name="deepseek-v4", display_name="DeepSeek V4"
    )


def test_load_registry_missing_families_section_returns_empty(tmp_path):
    # A registry without a [[families]] section (every pre-existing file)
    # must load with families == [], not raise.
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n[providers.auth]\ntype = "none"\n'
    )
    assert load_registry(path).families == []


def test_load_registry_missing_family_name_raises(tmp_path):
    # A [[families]] entry without a name is malformed; mirror the
    # provider-id requirement so a hand-edited file fails loudly.
    path = tmp_path / "registry.toml"
    path.write_text('[[families]]\ndisplay_name = "No Name"\n')
    with pytest.raises(RegistryError, match="missing required `name`"):
        load_registry(path)


def test_family_entry_preserves_unknown_keys(tmp_path):
    # Hand-edited fields on a [[families]] entry must survive round-trip.
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[families]]\nname = "deepseek-v4"\ndisplay_name = "DeepSeek V4"\ncustom = "keep"\n'
    )
    registry = load_registry(path)
    save_registry(registry, path)
    assert load_registry(path).family("deepseek-v4").extra == {"custom": "keep"}


def test_family_lookup_returns_none_for_unknown():
    # family() returns None for an unknown name (absence is normal),
    # unlike provider()/model() which raise.
    assert Registry().family("nope") is None


def test_family_lookup_returns_first_match():
    # Duplicate names are tolerated (first match wins), matching the
    # duplicate-model-id tolerance.
    registry = Registry(
        families=[
            FamilyEntry(name="a", display_name="First"),
            FamilyEntry(name="a", display_name="Second"),
        ]
    )
    assert registry.family("a").display_name == "First"
```

Rename the two existing tests (lines 138 and 151) and switch their call:

```python
def test_derived_families_returns_sorted_distinct_family_names():
    # The family list drives the TUI's left pane; it must be sorted and
    # deduplicated so a family spanning multiple providers appears once.
    registry = Registry(
        models=[
            ModelEntry(id="ollama/a", family="zeta", provider_id="ollama", model_name="a"),
            ModelEntry(id="ollama/b", family="alpha", provider_id="ollama", model_name="b"),
            ModelEntry(id="llamacpp/c", family="alpha", provider_id="llamacpp", model_name="c"),
        ]
    )
    assert registry.derived_families() == ["alpha", "zeta"]


def test_derived_families_on_empty_registry_returns_empty_list():
    # A fresh registry has no models; derived_families() must return []
    # rather than raise, so the TUI renders an empty family list on first run.
    assert Registry().derived_families() == []
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_registry.py -v`
Expected: FAIL — `ImportError: cannot import name 'FamilyEntry'` (and `Registry` has no `family`/`derived_families`).

- [ ] **Step 3: Write the implementation**

In `src/modelman/registry.py`, add `FamilyEntry` just before `Registry`:

```python
@dataclass
class FamilyEntry:
    name: str
    display_name: str | None = None
    extra: dict[str, Any] = field(default_factory=dict, repr=False)
```

Change `Registry` to add the field and the new/renamed methods:

```python
@dataclass
class Registry:
    providers: list[ProviderEntry] = field(default_factory=list)
    families: list[FamilyEntry] = field(default_factory=list)
    models: list[ModelEntry] = field(default_factory=list)

    def provider(self, provider_id: str) -> ProviderEntry:
        for p in self.providers:
            if p.id == provider_id:
                return p
        raise KeyError(f"Unknown provider: {provider_id}")

    def model(self, model_id: str) -> ModelEntry:
        for m in self.models:
            if m.id == model_id:
                return m
        raise KeyError(f"Unknown model: {model_id}")

    def family(self, name: str) -> FamilyEntry | None:
        for f in self.families:
            if f.name == name:
                return f
        return None

    def derived_families(self) -> list[str]:
        return sorted({m.family for m in self.models})

    def models_by_family(self, family: str) -> list[ModelEntry]:
        return [m for m in self.models if m.family == family]
```

Add `_family_to_dict` next to `_model_to_dict`:

```python
def _family_to_dict(f: FamilyEntry) -> dict[str, Any]:
    d = {"name": f.name, "display_name": f.display_name}
    return drop_none({**f.extra, **d})
```

Add `_parse_family` next to `_parse_model`:

```python
def _parse_family(raw: dict[str, Any]) -> FamilyEntry:
    if "name" not in raw:
        raise RegistryError(f"Family entry missing required `name` field: {raw}")
    return FamilyEntry(
        name=raw["name"],
        display_name=raw.get("display_name"),
        extra=unknown_keys(raw, {"name", "display_name"}),
    )
```

Update `load_registry`'s return:

```python
    return Registry(
        providers=[_parse_provider(p) for p in raw.get("providers", [])],
        families=[_parse_family(f) for f in raw.get("families", [])],
        models=[_parse_model(m) for m in raw.get("models", [])],
    )
```

Update `save_registry`'s payload (order: providers, families, models):

```python
    payload = {
        "providers": [_provider_to_dict(p) for p in registry.providers],
        "families": [_family_to_dict(f) for f in registry.families],
        "models": [_model_to_dict(m) for m in registry.models],
    }
```

Update the two screen callers:

`src/modelman/screens/families.py:165`:
```python
        families = sorted(set(self.registry.derived_families()) | set(self.state.families.keys()))
```

`src/modelman/screens/models.py:411`:
```python
        return sorted(set(self.registry.derived_families()) | set(self.state.families))
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_registry.py -v`
Expected: PASS (all registry tests, including the renamed `derived_families` tests).

- [ ] **Step 5: Commit**

```bash
git add src/modelman/registry.py src/modelman/screens/families.py src/modelman/screens/models.py tests/test_registry.py
git commit -m "feat(registry): add first-class [[families]] entries — completes plan item #1"
```

---

### Task 2: `known_families()` and `family_display_name()` helpers

**Files:**
- Modify: `src/modelman/registry.py` (add `TYPE_CHECKING` import + two module-level helpers)
- Test: `tests/test_registry.py`

**Interfaces:**
- Consumes: `Registry.derived_families()`, `Registry.family()`, `Registry.families` (Task 1); `StateStore.families` (duck-typed).
- Produces: `known_families(registry: Registry, state: StateStore) -> list[str]`; `family_display_name(registry: Registry, state: StateStore, family: str) -> str | None`.

- [ ] **Step 1: Write the failing tests**

Add to `tests/test_registry.py`. Extend the import block with `known_families`, `family_display_name`, and add a `modelman.state` import:

```python
from modelman.registry import (
    AuthConfig,
    Cost,
    FamilyEntry,
    Fetch,
    ModelEntry,
    ProviderEntry,
    Registry,
    RegistryError,
    _default_registry_path,
    family_display_name,
    known_families,
    load_registry,
    provider_config,
    save_registry,
)
from modelman.state import FamilyState, StateStore
```

Then add:

```python
def test_known_families_union_of_derived_entries_and_state():
    # known_families must include families from all three sources so an
    # emptied-by-move family (registry entry only) stays visible.
    registry = Registry(
        families=[FamilyEntry(name="entry-only")],
        models=[ModelEntry(id="ollama/a", family="derived", provider_id="ollama", model_name="a")],
    )
    state = StateStore(families={"legacy": FamilyState()})
    assert known_families(registry, state) == ["derived", "entry-only", "legacy"]


def test_family_display_name_resolution_order():
    # Registry entry wins over legacy state; both fall back to None.
    registry = Registry(families=[FamilyEntry(name="a", display_name="Registry Name")])
    state = StateStore(
        families={
            "a": FamilyState(display_name="Legacy Name"),
            "b": FamilyState(display_name="Only Legacy"),
        }
    )
    assert family_display_name(registry, state, "a") == "Registry Name"
    assert family_display_name(registry, state, "b") == "Only Legacy"
    assert family_display_name(registry, state, "c") is None


def test_family_display_name_ignores_empty_display_name():
    # An empty display_name is treated as unset (falls through to the
    # next source), matching state.family_display_name's truthiness check.
    registry = Registry(families=[FamilyEntry(name="a", display_name="")])
    state = StateStore(families={"a": FamilyState(display_name="Legacy")})
    assert family_display_name(registry, state, "a") == "Legacy"
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_registry.py -v`
Expected: FAIL — `ImportError: cannot import name 'known_families'`.

- [ ] **Step 3: Write the implementation**

In `src/modelman/registry.py`, change the typing import and add a `TYPE_CHECKING` block:

```python
from typing import TYPE_CHECKING, Any, Literal
```

After the `_toml_io` import (or anywhere at module top level), add:

```python
if TYPE_CHECKING:
    from .state import StateStore
```

Add the two helpers after the `Registry` class (e.g. just before `provider_config`):

```python
def known_families(registry: Registry, state: StateStore) -> list[str]:
    """Sorted union of every family the TUI should show: families derived
    from models, first-class [[families]] entry names, and legacy
    state.families keys (read-side fallback)."""
    return sorted(
        set(registry.derived_families())
        | {f.name for f in registry.families}
        | set(state.families.keys())
    )


def family_display_name(registry: Registry, state: StateStore, family: str) -> str | None:
    """Registry entry display_name, else legacy state display_name, else
    None. Callers decide the fallback (table column: ""; edit prefill:
    the family name)."""
    entry = registry.family(family)
    if entry is not None and entry.display_name:
        return entry.display_name
    legacy = state.families.get(family)
    if legacy is not None and legacy.display_name:
        return legacy.display_name
    return None
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_registry.py -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/registry.py tests/test_registry.py
git commit -m "feat(registry): add known_families/family_display_name helpers — completes plan item #2"
```

---

### Task 3: Visibility rule — `FamilyScreen.reload()` and `ModelScreen._families_list()` use `known_families()`; DISPLAY column uses `family_display_name()`

**Files:**
- Modify: `src/modelman/screens/families.py` (imports; `reload()`; DISPLAY column)
- Modify: `src/modelman/screens/models.py` (import; `_families_list()`)
- Test: `tests/screens/test_app_navigation.py` (extend `_make_screen`; add a `_families_list` test)

**Interfaces:**
- Consumes: `known_families`, `family_display_name` (Task 2).
- Produces: `FamilyScreen.reload()` renders every `known_families` name; `ModelScreen._families_list()` returns `known_families(...)`.

- [ ] **Step 1: Write the failing test**

In `tests/screens/test_app_navigation.py`, add `FamilyEntry` to the top import:

```python
from modelman.registry import (
    AuthConfig,
    FamilyEntry,
    ModelEntry,
    ProviderEntry,
    Registry,
    load_registry,
    save_registry,
)
```

Extend `_make_screen` (line 1056) with a `family_entries` parameter:

```python
def _make_screen(
    tmp_path, monkeypatch, *, family: str = "ornith", entries=(), families=(), family_entries=()
):
    """Build a ModelScreen with registry.toml + modelman.toml in tmp_path
    and seed registry with the given ModelEntries. `families` seeds
    StateStore.families (explicitly created, possibly empty families);
    `family_entries` seeds first-class Registry.families entries.
    Returns (ms, registry_path, state_path)."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[
            ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none")),
            ProviderEntry(id="llamacpp", name="L", auth=AuthConfig(type="none")),
            ProviderEntry(id="omlx", name="X", auth=AuthConfig(type="omlx")),
        ],
        families=list(family_entries),
        models=list(entries),
    )
    save_registry(reg, reg_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    from modelman.screens.models import ModelScreen
    from modelman.state import FamilyState

    ms = ModelScreen(
        registry=reg,
        state=StateStore(families={f: FamilyState() for f in families}),
        family=family,
        registry_path=reg_path,
        state_path=state_path,
        available_providers=["ollama", "llamacpp", "omlx"],
    )
    return ms, reg_path, state_path
```

Add the test right after `test_families_list_includes_state_only_families` (line 1728):

```python
def test_families_list_includes_registry_entry_only_families(tmp_path, monkeypatch):
    """`_families_list()` must include a family that exists only as a
    first-class [[families]] entry (no models, no state entry) — the
    emptied-by-move case that must stay visible."""
    entry = ModelEntry(
        id="ollama/gemma4:26b-mlx",
        family="gemma4:26b-mlx",
        provider_id="ollama",
        model_name="gemma4:26b-mlx",
    )
    ms, _reg, _state = _make_screen(
        tmp_path,
        monkeypatch,
        family="gemma4:26b-mlx",
        entries=[entry],
        family_entries=[FamilyEntry(name="deepseek-v4")],
    )
    assert ms._families_list() == ["deepseek-v4", "gemma4:26b-mlx"]
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/screens/test_app_navigation.py::test_families_list_includes_registry_entry_only_families -v`
Expected: FAIL — `_families_list()` returns only `["gemma4:26b-mlx"]` (the registry entry is ignored).

- [ ] **Step 3: Write the implementation**

In `src/modelman/screens/families.py`, extend the `..registry` import:

```python
from ..registry import (
    Registry,
    RegistryError,
    family_display_name,
    known_families,
    load_registry,
    provider_config,
    save_registry,
)
```

Change `reload()` (line 165) and the DISPLAY column (line 195):

```python
        families = known_families(self.registry, self.state)
```

```python
            table.add_row(
                family,
                family_display_name(self.registry, self.state, family) or "",
                str(variants),
                str(downloaded_count),
                size_str,
                key=family,
            )
```

In `src/modelman/screens/models.py`, extend the `..registry` import and change `_families_list()`:

```python
from ..registry import Fetch, ModelEntry, Registry, known_families, provider_config
```

```python
    def _families_list(self) -> list[str]:
        """Every family the add/edit dialogs may target: families with
        models in the registry, first-class [[families]] entries, and
        legacy state.families keys, sorted."""
        return known_families(self.registry, self.state)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/screens/test_app_navigation.py -v`
Expected: PASS (the new test plus the existing `test_families_list_includes_state_only_families`, which is unchanged because state-only families remain in the union).

- [ ] **Step 5: Commit**

```bash
git add src/modelman/screens/families.py src/modelman/screens/models.py tests/screens/test_app_navigation.py
git commit -m "feat(tui): derive family visibility from known_families — completes plan item #3"
```

---

### Task 4: Family screen Add/Edit/Delete write registry entries + promotion

**Files:**
- Modify: `src/modelman/screens/families.py` (import `FamilyEntry`; add `_upsert_family_entry`; rewrite Add/Edit/Delete)
- Test: `tests/screens/test_family_edit.py` (Edit), `tests/screens/test_app_navigation.py` (Add, Delete)

**Interfaces:**
- Consumes: `FamilyEntry` (Task 1), `family_display_name` (Task 2), `save_registry`/`save_state`.
- Produces: `FamilyScreen._upsert_family_entry(name, display_name)` (private); Add/Edit/Delete persist registry entries and drain `state.families`.

- [ ] **Step 1: Write the failing tests**

In `tests/screens/test_family_edit.py`, update `test_family_screen_edit_persists_display_name_to_state` (line 192) to assert the registry entry is written and the state entry dropped:

```python
@pytest.mark.asyncio
async def test_family_screen_edit_persists_display_name_to_registry(tmp_path, monkeypatch):
    """Saving the edit modal must write the new display_name to a
    first-class [[families]] entry in registry.toml and drop the legacy
    state.families entry (promotion)."""
    reg_path, state_path = _seed(
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

    from modelman.registry import load_registry

    reloaded_reg = load_registry(reg_path)
    assert reloaded_reg.family("ornith-1.5").display_name == "Ornith v1.5 (renamed)"
    assert "ornith-1.5" not in load_state(state_path).families
```

In `tests/screens/test_app_navigation.py`, update `test_add_family_registers_state` (line 119) to assert the registry entry:

```python
@pytest.mark.asyncio
async def test_add_family_registers_registry_entry(tmp_path, monkeypatch):
    """Adding a family with no models yet must still make it appear in
    the family table by recording a first-class [[families]] entry in
    registry.toml (the legacy state.families entry is no longer created)."""
    reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch)

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("a")
        for ch in "mamba":
            await pilot.press(ch)
        await pilot.press("enter")
        await pilot.pause()
        table = app.screen.query_one("DataTable")
        assert table.row_count == 1

    from modelman.registry import load_registry
    from modelman.state import load_state

    assert load_registry(reg_path).family("mamba") is not None
    assert "mamba" not in load_state(state_path).families
```

Add a delete test right after `test_delete_family_when_empty` (line 145):

```python
@pytest.mark.asyncio
async def test_delete_family_removes_registry_entry(tmp_path, monkeypatch):
    """Deleting an emptied family must remove its first-class
    [[families]] entry from registry.toml, not just the state entry."""
    from modelman.registry import FamilyEntry, load_registry, save_registry

    reg_path, _state_path = _seed_registry_and_state(tmp_path, monkeypatch)
    # Seed a first-class entry (the emptied-by-move residue) with no models.
    reg = load_registry(reg_path)
    reg.families.append(FamilyEntry(name="mamba"))
    save_registry(reg, reg_path)

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        await pilot.press("y")
        await pilot.pause()

    assert load_registry(reg_path).family("mamba") is None
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/screens/test_family_edit.py::test_family_screen_edit_persists_display_name_to_registry tests/screens/test_app_navigation.py::test_add_family_registers_registry_entry tests/screens/test_app_navigation.py::test_delete_family_removes_registry_entry -v`
Expected: FAIL — the registry entry is never written (Add/Edit still write `state.families`; Delete never removes a registry entry).

- [ ] **Step 3: Write the implementation**

In `src/modelman/screens/families.py`, add `FamilyEntry` to the `..registry` import:

```python
from ..registry import (
    FamilyEntry,
    Registry,
    RegistryError,
    family_display_name,
    known_families,
    load_registry,
    provider_config,
    save_registry,
)
```

Add a private helper method (place it near `_delete_family`):

```python
    def _upsert_family_entry(self, name: str, display_name: str) -> None:
        """Write (or update) the first-class [[families]] entry for `name`
        and drop any legacy state.families entry (promotion)."""
        entry = self.registry.family(name)
        if entry is None:
            self.registry.families.append(FamilyEntry(name=name, display_name=display_name))
        else:
            entry.display_name = display_name
        self.state.forget_family(name)
```

Rewrite `action_add_family` (line 205):

```python
    def action_add_family(self) -> None:
        def _on_close(result: tuple[str, str] | None) -> None:
            if result is None:
                return
            family, display_name = result
            self._upsert_family_entry(family, display_name)
            save_registry(self.registry, self.registry_path)
            save_state(self.state, self.state_path)
            self.reload()

        self.app.push_screen(AddFamilyModal(), _on_close)
```

Rewrite `action_edit_family` (line 216):

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
            self._upsert_family_entry(family_name, display_name)
            save_registry(self.registry, self.registry_path)
            save_state(self.state, self.state_path)
            # _refresh_from_disk also clears the reconcile overlay; model
            # ids didn't change so the keys stay valid, but matching
            # add/delete (which already do this) keeps behavior uniform.
            self._refresh_from_disk()

        self.app.push_screen(
            EditFamilyModal(
                family=family_name,
                display_name=family_display_name(self.registry, self.state, family_name)
                or family_name,
            ),
            _on_close,
        )
```

In `_delete_family` (line 294), add the registry-entry removal after the models removal:

```python
        removed_ids = {m.id for m in self.registry.models if m.family == family_name}
        self.registry.models = [m for m in self.registry.models if m.family != family_name]
        # Remove the first-class [[families]] entry too, so an emptied
        # family is gone for good after an explicit delete.
        self.registry.families = [f for f in self.registry.families if f.name != family_name]
        for mid in removed_ids:
            self.state.models.pop(mid, None)
        self.state.forget_family(family_name)
        save_registry(self.registry, self.registry_path)
        save_state(self.state, self.state_path)
        self.reload()
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/screens/test_family_edit.py tests/screens/test_app_navigation.py -v`
Expected: PASS. Note the existing `test_delete_family_when_empty` (state-seeded) still passes unchanged — `_delete_family` removes a registry entry that isn't there (no-op) and still drops the state entry.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/screens/families.py tests/screens/test_family_edit.py tests/screens/test_app_navigation.py
git commit -m "feat(tui): write family display names to registry entries — completes plan item #4"
```

---

### Task 5: `PendingChanges.apply()` — stickiness (the core fix)

**Files:**
- Modify: `src/modelman/queue.py` (import `FamilyEntry`; record emptied families; add stickiness block; touch module docstring)
- Test: `tests/test_queue.py` (6 tests), `tests/screens/test_app_navigation.py` (end-to-end)

**Interfaces:**
- Consumes: `FamilyEntry`, `Registry.family()`, `Registry.models_by_family()`, `Registry.families` (Task 1); `StateStore.families`/`forget_family`.
- Produces: after `apply()`, a family emptied by a delete or move gains a `FamilyEntry` (with the legacy display name promoted) and its `state.families` entry is dropped.

- [ ] **Step 1: Write the failing tests**

In `tests/test_queue.py`, add `FamilyEntry` to the import block:

```python
from modelman.registry import (
    AuthConfig,
    FamilyEntry,
    ModelEntry,
    ProviderEntry,
    Registry,
    load_registry,
    save_registry,
)
```

Add these tests (place them after the existing moves tests, near line 963):

```python
def test_apply_move_emptying_family_creates_family_entry(tmp_path):
    """Moving the last model out of a family must leave a first-class
    [[families]] entry with the legacy display name promoted, and drop
    the legacy state.families entry."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="m1", family="gemma4:26b-mlx", provider="ollama", name="gemma4:26b-mlx"),
    )
    state = _make_state()
    state.touch_family("gemma4:26b-mlx", display_name="Gemma4 26B MLX")

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="gemma4:26b-mlx",
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={},
        moves=[("m1", "gemma4")],
    )
    pending.apply()

    reloaded = load_registry(reg_path)
    assert reloaded.family("gemma4:26b-mlx").display_name == "Gemma4 26B MLX"
    assert "gemma4:26b-mlx" not in load_state(tmp_path / "modelman.toml").families


def test_apply_delete_emptying_family_creates_family_entry(tmp_path):
    """Deleting the last model of a family must also leave a lingering
    [[families]] entry (no display name — there was none to promote)."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="m1", family="solo", provider="ollama", name="solo"),
    )
    state = _make_state()
    provider = MagicMock()
    provider.name = "ollama"
    provider.delete.return_value = None

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="solo",
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={"ollama": provider},
        deletes=[("m1", _variant(id="m1", provider="ollama", name="solo"))],
    )
    pending.apply()

    reloaded = load_registry(reg_path)
    assert reloaded.family("solo") is not None
    assert reloaded.family("solo").display_name is None
    assert reloaded.models == []


def test_apply_move_does_not_create_entry_when_family_survives(tmp_path):
    """A family that still has models after the apply must NOT gain an
    entry — only families emptied to zero models linger."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="m1", family="a", provider="ollama", name="x"),
        _entry(id="m2", family="a", provider="ollama", name="y"),
    )
    state = _make_state()

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="a",
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={},
        moves=[("m1", "b")],
    )
    pending.apply()

    assert load_registry(reg_path).family("a") is None


def test_apply_move_emptying_family_does_not_duplicate_existing_entry(tmp_path):
    """If a [[families]] entry already exists, emptying the family must
    not append a duplicate — the existing entry is left untouched."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="m1", family="a", provider="ollama", name="x"),
    )
    reg.families.append(FamilyEntry(name="a", display_name="Already"))
    save_registry(reg, reg_path)
    state = _make_state()

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="a",
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={},
        moves=[("m1", "b")],
    )
    pending.apply()

    entries = [f for f in load_registry(reg_path).families if f.name == "a"]
    assert len(entries) == 1
    assert entries[0].display_name == "Already"


def test_apply_promotes_legacy_display_into_existing_entry_without_display(tmp_path):
    """An existing entry with no display name gains the legacy state
    display name before the state entry is dropped."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="m1", family="a", provider="ollama", name="x"),
    )
    reg.families.append(FamilyEntry(name="a", display_name=None))
    save_registry(reg, reg_path)
    state = _make_state()
    state.touch_family("a", display_name="Legacy Name")

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="a",
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={},
        moves=[("m1", "b")],
    )
    pending.apply()

    assert load_registry(reg_path).family("a").display_name == "Legacy Name"
    assert "a" not in load_state(tmp_path / "modelman.toml").families


def test_apply_cancelled_persists_no_family_entry(tmp_path):
    """A cancelled apply saves nothing, so the stickiness entry is not
    written and the move is not applied."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="m1", family="a", provider="ollama", name="x"),
    )
    state = _make_state()

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="a",
        registry_path=reg_path,
        state_path=tmp_path / "modelman.toml",
        providers={},
        moves=[("m1", "b")],
    )
    pending.cancel()
    pending.apply()

    reloaded = load_registry(reg_path)
    assert reloaded.family("a") is None
    assert reloaded.model("m1").family == "a"
```

In `tests/screens/test_app_navigation.py`, add the end-to-end test right after `test_exit_dialog_lists_move_and_apply_persists_it` (line 1805):

```python
@pytest.mark.asyncio
async def test_apply_move_emptying_family_keeps_it_visible(tmp_path, monkeypatch):
    """After an apply that moves the last model out of a family, the
    emptied family must still appear on the FamilyScreen (0 variants)
    because apply recorded a first-class [[families]] entry."""
    entry = ModelEntry(
        id="ollama/gemma4:26b-mlx",
        family="gemma4:26b-mlx",
        provider_id="ollama",
        model_name="gemma4:26b-mlx",
    )
    ms, reg_path, _state = _make_screen(
        tmp_path,
        monkeypatch,
        family="gemma4:26b-mlx",
        entries=[entry],
        families=["gemma4"],
    )

    from modelman.app import ModelmanApp
    from modelman.registry import load_registry
    from modelman.screens.families import FamilyScreen
    from modelman.screens.status import StatusScreen

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        pilot.app.push_screen(ms)
        await pilot.pause()
        ms.queued_moves["ollama/gemma4:26b-mlx"] = "gemma4"
        await pilot.press("escape")
        await pilot.pause()
        from textual.widgets import Button

        for btn in app.screen.query(Button):
            if btn.id == "apply":
                btn.press()
                break
        for _ in range(50):
            await pilot.pause()
            cur = app.screen
            if isinstance(cur, StatusScreen) and cur.done:
                break
        # Pop back to FamilyScreen and confirm the emptied family lingers.
        await pilot.press("escape")
        await pilot.pause()
        assert isinstance(app.screen, FamilyScreen)
        table = app.screen.query_one(DataTable)
        rows = {r[0]: r for r in [table.get_row_at(i) for i in range(table.row_count)]}
        assert "gemma4:26b-mlx" in rows
        assert rows["gemma4:26b-mlx"][2] == "0"  # VARIANTS column

    reloaded = load_registry(reg_path)
    assert reloaded.family("gemma4:26b-mlx") is not None
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `uv run pytest tests/test_queue.py -k "family_entry or surviving or duplicate or promotes or cancelled" tests/screens/test_app_navigation.py::test_apply_move_emptying_family_keeps_it_visible -v`
Expected: FAIL — no `[[families]]` entry is created (stickiness not implemented).

- [ ] **Step 3: Write the implementation**

In `src/modelman/queue.py`, change the runtime import:

```python
from .registry import FamilyEntry, save_registry
```

In `apply()`, add a `recorded_families` set next to `deleted_ids` (line 145):

```python
        # ids removed by this apply — moves referencing them are moot
        deleted_ids: set[str] = set()
        # Families whose membership changed this apply (a model deleted or
        # moved out). After all membership changes, any of these that now
        # has zero models lingers as a first-class [[families]] entry so it
        # stays visible until explicitly deleted (stickiness).
        recorded_families: set[str] = set()
```

In the deletes loop, record the family before removing the entry (insert between the `except` block and the `# Remove from in-memory registry.` line):

```python
            # Record the model's family before removing the entry, so a
            # family emptied by this delete lingers (stickiness). A failed
            # delete records nothing — the model stays.
            try:
                recorded_families.add(self.registry.model(model_id).family)
            except KeyError:
                pass
            # Remove from in-memory registry.
            self.registry.models = [m for m in self.registry.models if m.id != model_id]
```

In the moves loop, record the source family before reassignment (insert before `entry.family = new_family`):

```python
            move_label = _sanitize(entry.model_name)
            move_family = _sanitize(new_family)
            emit(f"move:start|{model_id}|{move_label}|{move_family}")
            # Record the source family before reassignment (stickiness).
            recorded_families.add(entry.family)
            entry.family = new_family
            emit(f"move:done|{model_id}|{move_label}|{move_family}")
```

After the moves loop (before the downloads loop), add the stickiness block:

```python
        # Stickiness: a family that had models and now has none (emptied by
        # a delete or move in this apply) lingers as a first-class
        # [[families]] entry so it stays visible until explicitly deleted.
        # Runs after all membership changes (deletes + moves) so a family
        # that gained a model in the same apply is not touched.
        for f in recorded_families:
            if self.registry.models_by_family(f):
                continue
            entry = self.registry.family(f)
            legacy = self.state.families.get(f)
            if entry is None:
                self.registry.families.append(
                    FamilyEntry(name=f, display_name=legacy.display_name if legacy else None)
                )
            elif entry.display_name is None and legacy is not None and legacy.display_name:
                entry.display_name = legacy.display_name
            self.state.forget_family(f)
```

Update the module docstring's stale "family display names" mention (line 4):

```python
"""In-memory change queue applied on exit of the TUI model screen.

The on-disk targets are registry.toml (canonical model/provider/family
definitions) and modelman.toml (per-machine mutable state: download
markers). Queued family moves mutate registry.toml only. The
legacy families/<family>.yaml manifest is no longer
written by the TUI; it survives as a migrate-time input only.
"""
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `uv run pytest tests/test_queue.py tests/screens/test_app_navigation.py -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/modelman/queue.py tests/test_queue.py tests/screens/test_app_navigation.py
git commit -m "feat(queue): linger emptied families as first-class entries — completes plan item #5"
```

---

### Task 6: README + docstrings

**Files:**
- Modify: `README.md` (config-table row; `registry.toml` example; `modelman.toml` example)
- Modify: `src/modelman/registry.py` (module docstring)
- Modify: `src/modelman/state.py` (module docstring)

**Interfaces:**
- Consumes: nothing (docs only).
- Produces: accurate user-facing schema docs.

- [ ] **Step 1: Update `README.md`**

Line 27 — drop "family display names" from the `modelman.toml` row:

```markdown
| `modelman.toml` | Per-machine mutable state: download markers, LiteLLM exposure flags | `MODELMAN_STATE` |
```

In the `registry.toml` example, insert a `[[families]]` block between the providers and the `[[models]]` block (after line 48):

```toml
[[families]]
name = "ornith"
display_name = "Ornith"       # optional; omitted when unset

[[models]]
id = "ollama/ornith:35b"     # globally unique, stable key
```

In the `modelman.toml` example, remove the `[families.ornith]` block (lines 77-78) and add a note after the example:

```toml
[model_state."ollama/ornith:35b"]
downloaded = true
disk_path = "ollama:ornith:35b"
size_bytes = 123456789
litellm_exposed = false
```

```markdown
Family display names now live in `registry.toml`'s `[[families]]` section.
The legacy `[families.*]` table here is still loaded as a read-side
fallback, but the TUI no longer writes it.
```

- [ ] **Step 2: Update `src/modelman/registry.py` module docstring**

```python
"""registry.toml — the canonical, shared model/provider/family registry.

Owned exclusively by modelman (see docs/superpowers/specs/2026-08-27-
shared-model-registry-design.md). agent-worktree reads this file
read-only; it never writes it. Families are first-class [[families]]
entries here (see docs/superpowers/specs/2026-08-29-modelman-first-class-
families-design.md); their display names live in the entry, not in
modelman.toml.
"""
```

- [ ] **Step 3: Update `src/modelman/state.py` module docstring**

```python
"""modelman.toml — modelman's per-machine mutable state overlay.

Owner: modelman only. See registry.py for the canonical, shared model/
provider/family definitions this state is keyed against, and
docs/superpowers/specs/2026-08-27-shared-model-registry-design.md for the
ownership split.

The `families` table is a legacy read-side fallback: family display names
now live in registry.toml's first-class [[families]] entries (see
docs/superpowers/specs/2026-08-29-modelman-first-class-families-design.md).
Writers no longer create entries here; they drain them via promotion.
Entries stay loadable so pre-existing modelman.toml files keep working.
"""
```

- [ ] **Step 4: Verify nothing broke**

Run: `uv run pytest -q` (full suite) and `uv run ruff check src tests` (or `make check`).
Expected: PASS / clean.

- [ ] **Step 5: Commit**

```bash
git add README.md src/modelman/registry.py src/modelman/state.py
git commit -m "docs: document [[families]] and legacy state.families fallback — completes plan item #6"
```

---

## Self-Review Notes

- **Spec coverage:** Every spec section maps to a task — §1 (schema) → Task 1; §1 helpers → Task 2; §2 (visibility) → Task 3; §3 (screen actions) → Task 4; §4 (stickiness) → Task 5; §5 (edge cases) → covered by Task 1 (missing section/name, unknown keys) and Task 5 (cancelled apply, surviving family, no-duplicate, promotion); §Testing → Tasks 1–6; README/docstrings → Task 6.
- **File-reference correction:** the spec's testing section names `tests/screens/test_models.py` for the end-to-end apply test, but the apply pilot tests actually live in `tests/screens/test_app_navigation.py` (the spec's "existing apply pilot test" is `test_exit_dialog_lists_move_and_apply_persists_it` there). The plan places the end-to-end test in the correct file.
- **Type consistency:** `FamilyEntry`, `Registry.family()`, `Registry.derived_families()`, `known_families()`, `family_display_name()` are defined once (Tasks 1–2) and referenced identically in Tasks 3–5.
