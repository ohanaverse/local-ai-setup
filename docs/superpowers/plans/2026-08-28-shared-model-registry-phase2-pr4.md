# Shared Model Registry — Phase 2, PR 4 (finish the manifest.py cleanup) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Finish what PR 2/PR 3 started: un-skip and migrate the remaining tests still tagged "migrates in PR 3" (three stray ones PR 3's plan didn't discover), and strip the now-dead `MODELMAN_CONFIG`/`MODELMAN_FAMILY_DIR` env-var setup left over in tests whose production code paths no longer read them.

**Architecture:** PR 2's plan described this PR's scope as "`app.py` cleanup: dropping legacy manifest reads and `MODELMAN_CONFIG`/`MODELMAN_FAMILY_DIR` env-vars." **Re-verified against the actual code (not that plan's 2026-08-27 description, which is now stale): `app.py` already has zero `manifest.py`/`config.py` usage** — its `--initial-family` path was already rewritten onto `Registry`/`StateStore` by the time PR 3 was written (confirmed by reading `src/modelman/app.py` directly; there is no `load_manifest`/`get_family_dir`/`load_config` import anywhere in it). So there is no `app.py` code left to touch.

What PR 3 *didn't* finish, discovered by grepping the whole repo for `MODELMAN_CONFIG`/`MODELMAN_FAMILY_DIR`/`FamilyManifest` after it merged:

1. Three tests still carry `@pytest.mark.skip(reason="...PR 3")` / `@pytest.mark.skip(reason="...rewrites in PR 3")` that PR 3's plan never found, because it only looked at `tests/screens/test_family_edit.py` and `tests/screens/test_app_navigation.py`:
   - `tests/test_app_settings.py::test_app_loads_saved_theme_on_startup`
   - `tests/screens/test_forms.py::test_add_model_dialog_inherits_selected_provider`
   - `tests/commands/test_download.py::test_download_launches_tui_at_family`
2. Several already-passing tests still `monkeypatch.setenv("MODELMAN_CONFIG", ...)` / `monkeypatch.setenv("MODELMAN_FAMILY_DIR", ...)` and write a scratch `config.yaml`, even though nothing in the code path they exercise reads either variable anymore (confirmed per-file below). This is dead test setup, not a correctness bug, but it's exactly the "drop the env-vars" cleanup PR 4 was meant to do — scoped to what's actually left rather than the stale original description.
3. `MODELMAN_CONFIG` (`config.py`'s `default_config_path()`) and `MODELMAN_FAMILY_DIR` (`manifest.py`'s `get_family_dir()`) are **still genuinely load-bearing** in exactly two places, and must not be touched: `src/modelman/main.py`'s `migrate` command and `src/modelman/migrate.py` (the one-time `modelman migrate` importer reads the *legacy* file locations by design — that's its whole job). `tests/commands/test_migrate.py` exercises that command and is correctly untouched by this plan.

**Tech Stack:** Python 3.13, pytest-asyncio, Textual (existing) — no new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-27-shared-model-registry-design.md` (canonical schema/ownership) + `docs/superpowers/plans/2026-08-27-shared-model-registry-phase2-pr3.md` (the immediately-preceding PR, merged as `9ba6714`, whose scope this one completes).

## Global Constraints

- `requires-python = "==3.13.*"` (pyproject.toml) — no syntax/stdlib beyond 3.13.
- **`config.py` and `manifest.py` are not touched by this plan and must keep working exactly as they do today.** They remain the on-disk format `modelman migrate` reads *from* (legacy `config.yaml` + `families/*.yaml`) to populate `registry.toml`/`modelman.toml`. `MODELMAN_CONFIG`/`MODELMAN_FAMILY_DIR` must keep working as overrides of those two functions' default paths — do not remove the env-var reads inside `config.py`/`manifest.py` themselves, only the now-unnecessary `monkeypatch.setenv(...)` calls in tests whose code path doesn't touch those functions.
- `tests/commands/test_migrate.py` — **not touched.** It legitimately sets `MODELMAN_CONFIG`/`MODELMAN_FAMILY_DIR` to exercise the migrate command's actual job.
- Run `uv run pytest`, `uv run ruff check src/ tests/`, `uv run mypy src/` (Makefile targets `test`/`lint`/`typecheck`) before every commit in this plan that touches `src/` or `tests/`.

---

## File Structure

- Modify: `tests/test_app_settings.py` — un-skip `test_app_loads_saved_theme_on_startup`, drop its now-nonexistent `modelman.app.load_manifest`/`modelman.app.get_family_dir` mocks.
- Modify: `tests/screens/test_forms.py` — un-skip and migrate `test_add_model_dialog_inherits_selected_provider` onto `Registry`/`StateStore`; drop the now-unused `FamilyManifest`/`save_manifest` import.
- Modify: `tests/commands/test_download.py` — un-skip and migrate `test_download_launches_tui_at_family`; drop the vestigial env-var setup in `test_no_args_launches_tui_at_family_list`.
- Modify: `tests/screens/test_app_navigation.py` — drop vestigial `MODELMAN_FAMILY_DIR`/`MODELMAN_CONFIG` setup in `test_q_exits_app_from_family_screen` and `test_app_with_initial_family_launches_into_model_screen`; update the stale "PR 3 migrates..." section comment.
- Modify: `tests/screens/test_status.py` — drop vestigial `MODELMAN_CONFIG` setup in the `app_with_apply` fixture and two inline tests.
- Modify: `src/modelman/config.py`, `src/modelman/manifest.py` — add a one-line module-docstring note marking them migrate-time-only, matching the note `queue.py` already carries.
- Not created: no new files.

---

### Task 1: Un-skip `test_app_loads_saved_theme_on_startup`

**Files:**
- Modify: `tests/test_app_settings.py:15-32`

**Interfaces:** none — leaf test, self-contained.

- [ ] **Step 1: Replace the test**

Replace:

```python
@pytest.mark.skip(reason="app.py no longer imports load_manifest/get_family_dir — rewrites in PR 3")
@pytest.mark.asyncio
async def test_app_loads_saved_theme_on_startup(tmp_path, monkeypatch):
    """If settings.yaml has a theme, the app should set self.theme
    to that value during construction."""
    from modelman.app import ModelmanApp
    from modelman.settings import Settings, save_settings

    settings_path = tmp_path / "settings.yaml"
    save_settings(Settings(theme="nord"), settings_path)
    monkeypatch.setenv("MODELMAN_SETTINGS", str(settings_path))

    # Mock load_manifest so we don't need a real family.
    monkeypatch.setattr(
        "modelman.app.load_manifest", lambda *a, **kw: MagicMock()
    )
    monkeypatch.setattr(
        "modelman.app.get_family_dir", lambda: tmp_path / "families"
    )

    app = ModelmanApp()
    assert app.theme == "nord"
```

with:

```python
@pytest.mark.asyncio
async def test_app_loads_saved_theme_on_startup(tmp_path, monkeypatch):
    """If settings.yaml has a theme, the app should set self.theme
    to that value during construction.

    ModelmanApp.__init__ only loads settings (theme) — it does not
    touch registry.toml/modelman.toml at all (that's on_mount, which
    this test never triggers by calling .run()), so no
    registry/family mocking is needed here."""
    from modelman.app import ModelmanApp
    from modelman.settings import Settings, save_settings

    settings_path = tmp_path / "settings.yaml"
    save_settings(Settings(theme="nord"), settings_path)
    monkeypatch.setenv("MODELMAN_SETTINGS", str(settings_path))

    app = ModelmanApp()
    assert app.theme == "nord"
```

- [ ] **Step 2: Check whether `MagicMock` is still used elsewhere in the file**

Run: `cd /Users/keith/github/ohanaverse/modelman && grep -n "MagicMock" tests/test_app_settings.py`
Expected: no matches (the deleted mocking was the only use). If none, remove the now-unused `from unittest.mock import MagicMock` import at the top of the file. If there *is* another use, leave the import.

- [ ] **Step 3: Run the file**

Run: `cd /Users/keith/github/ohanaverse/modelman && uv run pytest tests/test_app_settings.py -v`
Expected: all tests PASS, zero skipped.

- [ ] **Step 4: Commit**

```bash
git add tests/test_app_settings.py
git commit -m "test(app): un-skip test_app_loads_saved_theme_on_startup — no manifest mocking needed"
```

---

### Task 2: Un-skip and migrate `test_add_model_dialog_inherits_selected_provider`

**Files:**
- Modify: `tests/screens/test_forms.py:17` (import), `:529-604` (the test)

**Interfaces:** none new — exercises `FamilyScreen`/`ModelScreen` as already migrated by PR 3.

- [ ] **Step 1: Replace the import**

Replace line 17:

```python
from modelman.manifest import FamilyManifest, save_manifest
```

with:

```python
from modelman.registry import AuthConfig, Fetch, ModelEntry, ProviderEntry, Registry, save_registry
from modelman.state import StateStore, save_state
```

(Confirmed by grep in the planning session that `FamilyManifest`/`save_manifest` are used nowhere else in this file — safe to remove entirely rather than keep alongside the new imports.)

- [ ] **Step 2: Replace the test**

Replace:

```python
@pytest.mark.skip(reason="FamilyScreen migrates to Registry in PR 3")
@pytest.mark.asyncio
async def test_add_model_dialog_inherits_selected_provider(tmp_path, monkeypatch):
    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[
            {"id": "o35", "provider": "ollama", "name": "ornith:35b"},
            {"id": "q8", "provider": "llamacpp", "name": "x.gguf",
             "repo": "foo/bar", "files": ["x.gguf"]},
            {"id": "m8", "provider": "omlx", "name": "x-mlx",
             "repo": "foo/bar"},
        ],
    )
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text(
        "providers:\n  ollama: {type: ollama}\n"
        "  llamacpp: {type: llamacpp}\n"
        "  omlx: {type: omlx}\n"
    )

    from modelman.providers import registry

    stub = MagicMock()
    stub.size_of.return_value = None
    stub.name = "ollama"
    monkeypatch.setattr(
        registry.ProviderRegistry,
        "get",
        staticmethod(lambda name, cfg: stub),
    )

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()

        from modelman.screens.models import ModelScreen
        assert isinstance(app.screen, ModelScreen)

        from textual.widgets import DataTable

        provider_table = app.screen.query_one("#provider-table", DataTable)
        llamacpp_idx = None
        for i in range(provider_table.row_count):
            if provider_table.get_row_at(i)[0] == "llamacpp":
                llamacpp_idx = i
                break
        assert llamacpp_idx is not None
        provider_table.focus()
        await pilot.pause()
        for _ in range(llamacpp_idx):
            await pilot.press("down")
        await pilot.pause()
        assert app.screen.selected_provider == "llamacpp"

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
        form = captured[0]
        assert form._default_provider == "llamacpp"
```

with:

```python
@pytest.mark.asyncio
async def test_add_model_dialog_inherits_selected_provider(tmp_path, monkeypatch):
    o35 = ModelEntry(id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b")
    q8 = ModelEntry(
        id="llamacpp/q8", family="ornith", provider_id="llamacpp", model_name="x.gguf",
        fetch=Fetch(repo="foo/bar", files=["x.gguf"]),
    )
    m8 = ModelEntry(
        id="omlx/m8", family="ornith", provider_id="omlx", model_name="x-mlx",
        fetch=Fetch(repo="foo/bar"),
    )
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[
            ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none")),
            ProviderEntry(id="llamacpp", name="L", auth=AuthConfig(type="none")),
            ProviderEntry(id="omlx", name="X", auth=AuthConfig(type="omlx")),
        ],
        models=[o35, q8, m8],
    )
    save_registry(reg, reg_path)
    save_state(StateStore(), state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    from modelman.providers import registry

    stub = MagicMock()
    stub.size_of.return_value = None
    stub.name = "ollama"
    monkeypatch.setattr(
        registry.ProviderRegistry,
        "get",
        staticmethod(lambda name, cfg: stub),
    )

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()

        from modelman.screens.models import ModelScreen
        assert isinstance(app.screen, ModelScreen)

        from textual.widgets import DataTable

        provider_table = app.screen.query_one("#provider-table", DataTable)
        llamacpp_idx = None
        for i in range(provider_table.row_count):
            if provider_table.get_row_at(i)[0] == "llamacpp":
                llamacpp_idx = i
                break
        assert llamacpp_idx is not None
        provider_table.focus()
        await pilot.pause()
        for _ in range(llamacpp_idx):
            await pilot.press("down")
        await pilot.pause()
        assert app.screen.selected_provider == "llamacpp"

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
        form = captured[0]
        assert form._default_provider == "llamacpp"
```

Note: `FamilyScreen` only lists a family in its table if it has models or a `StateStore.families` entry (see `families.py::reload()`, PR 3). This test seeds the family purely via `models` (three `ModelEntry`s with `family="ornith"`), which is sufficient — `Registry.families()` derives "ornith" from those, no explicit `touch_family` needed.

- [ ] **Step 3: Run the file**

Run: `cd /Users/keith/github/ohanaverse/modelman && uv run pytest tests/screens/test_forms.py -v`
Expected: all tests PASS, zero skipped.

- [ ] **Step 4: Commit**

```bash
git add tests/screens/test_forms.py
git commit -m "test(forms): un-skip test_add_model_dialog_inherits_selected_provider — migrate to registry/state"
```

---

### Task 3: Un-skip and migrate `test_download_launches_tui_at_family`

**Files:**
- Modify: `tests/commands/test_download.py` (the skipped test, plus the vestigial env-var setup in the test right below it)

**Interfaces:** none new.

- [ ] **Step 1: Replace both tests**

Replace:

```python
@pytest.mark.skip(reason="FamilyScreen migrates in PR 3")
def test_download_launches_tui_at_family(tmp_path, monkeypatch):
    from modelman.manifest import FamilyManifest, save_manifest

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    save_manifest(FamilyManifest(family="ornith"), fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    with patch("modelman.main.run_tui") as run_tui:
        runner = CliRunner()
        result = runner.invoke(app, ["download", "ornith"])
        assert result.exit_code == 0
        run_tui.assert_called_once_with("ornith")


def test_no_args_launches_tui_at_family_list(tmp_path, monkeypatch):
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(tmp_path / "f"))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "c"))
    (tmp_path / "f").mkdir()
    (tmp_path / "c").write_text("providers:\n  ollama:\n    type: ollama\n")

    with patch("modelman.main.run_tui") as run_tui:
        runner = CliRunner()
        result = runner.invoke(app, [])
        assert result.exit_code == 0
        run_tui.assert_called_once_with(None)
```

with:

```python
def test_download_launches_tui_at_family(tmp_path, monkeypatch):
    """`modelman download <family>` is a thin CLI wrapper around
    run_tui(family); it never touches registry.toml/modelman.toml
    itself (ModelmanApp.on_mount does, later, when .run() is called —
    which run_tui does, but run_tui is mocked here). No registry/state
    fixture is needed."""
    with patch("modelman.main.run_tui") as run_tui:
        runner = CliRunner()
        result = runner.invoke(app, ["download", "ornith"])
        assert result.exit_code == 0
        run_tui.assert_called_once_with("ornith")


def test_no_args_launches_tui_at_family_list(tmp_path, monkeypatch):
    with patch("modelman.main.run_tui") as run_tui:
        runner = CliRunner()
        result = runner.invoke(app, [])
        assert result.exit_code == 0
        run_tui.assert_called_once_with(None)
```

- [ ] **Step 2: Confirm `tmp_path`/`monkeypatch` params are still needed**

Run: `cd /Users/keith/github/ohanaverse/modelman && grep -n "tmp_path\|monkeypatch" tests/commands/test_download.py`
Expected: neither rewritten test body references `tmp_path` or `monkeypatch` anymore (both were only used for the deleted env-var/file setup). Remove the now-unused `tmp_path, monkeypatch` parameters from both function signatures (`def test_download_launches_tui_at_family(tmp_path, monkeypatch):` → `def test_download_launches_tui_at_family():`, same for the other), since pytest fixtures that go unused are dead-weight and ruff/flake8-style conventions in this repo flag unused params in reviewed code (matches the "No Placeholders"/tidy-diff spirit — don't leave dead parameters a reader has to wonder about).

- [ ] **Step 3: Check whether the `FamilyManifest`/`save_manifest` import (module-scoped inside the old test body) leaves anything else broken**

Run: `cd /Users/keith/github/ohanaverse/modelman && grep -n "FamilyManifest\|save_manifest\|manifest" tests/commands/test_download.py`
Expected: no matches (the import was local to the deleted test body, not module-level — nothing to clean up at the top of the file).

- [ ] **Step 4: Run the file**

Run: `cd /Users/keith/github/ohanaverse/modelman && uv run pytest tests/commands/test_download.py -v`
Expected: all 3 tests PASS, zero skipped.

- [ ] **Step 5: Commit**

```bash
git add tests/commands/test_download.py
git commit -m "test(download): un-skip test_download_launches_tui_at_family; drop dead manifest/env-var setup"
```

---

### Task 4: Drop vestigial `MODELMAN_FAMILY_DIR`/`MODELMAN_CONFIG` setup in `test_app_navigation.py`

**Files:**
- Modify: `tests/screens/test_app_navigation.py:56-63` (`test_q_exits_app_from_family_screen`), `:91-97` (`test_app_with_initial_family_launches_into_model_screen`), `:945-948` (stale section comment), `:968` (`_make_screen` helper), `:1037` (`test_model_screen_provider_table_count_zero_for_empty_family`), `:1092` (`test_model_screen_add_form_offers_all_providers_for_empty_family`)

**Interfaces:** none — cleanup only, no behavior change (these tests already pass; the env vars they set are read by nothing in `FamilyScreen`/`ModelmanApp.on_mount` or directly-constructed `ModelScreen` since PR 3).

- [ ] **Step 1: Clean `test_q_exits_app_from_family_screen`**

Replace:

```python
@pytest.mark.asyncio
async def test_q_exits_app_from_family_screen(tmp_path, monkeypatch):
    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("q")
        await pilot.pause()
        # After action_quit, the app should no longer be running.
        assert not app.is_running
```

with:

```python
@pytest.mark.asyncio
async def test_q_exits_app_from_family_screen():
    """No registry.toml/modelman.toml fixture needed: FamilyScreen
    tolerates a missing registry (falls back to an empty Registry, see
    families.py::_load_from_disk), and this test only exercises quit."""
    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("q")
        await pilot.pause()
        # After action_quit, the app should no longer be running.
        assert not app.is_running
```

- [ ] **Step 2: Clean `test_app_with_initial_family_launches_into_model_screen`**

Replace:

```python
    save_registry(reg, reg_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama: {type: ollama}\n")

    from modelman.app import ModelmanApp

    app = ModelmanApp(family="ornith")
```

with:

```python
    save_registry(reg, reg_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    from modelman.app import ModelmanApp

    app = ModelmanApp(family="ornith")
```

(This is inside `test_app_with_initial_family_launches_into_model_screen`; match on this exact 6-line block since `save_registry(reg, reg_path)` also appears in other tests in this file — the surrounding `MODELMAN_STATE`/`config.yaml` lines make it unique to this one.)

- [ ] **Step 3: Update the stale section comment**

Replace:

```python
# ---------------------------------------------------------------------------
# ModelScreen constructed directly (no FamilyScreen / app.py round-trip).
# PR 3 migrates the integration tests that go through FamilyScreen.
# ---------------------------------------------------------------------------
```

with:

```python
# ---------------------------------------------------------------------------
# ModelScreen constructed directly (no FamilyScreen / app.py round-trip).
# ---------------------------------------------------------------------------
```

- [ ] **Step 4: Clean the `_make_screen` helper and its callers**

`_make_screen` still writes a scratch `config.yaml` and points `MODELMAN_CONFIG` at it, but the helper constructs `ModelScreen` directly with `registry=reg` and `state=StateStore()` — nothing on that path reads `config.py`. Remove the env var and config write from the helper:

```python
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text(
        "providers:\n"
        "  ollama: {type: ollama}\n"
        "  llamacpp: {type: llamacpp}\n"
        "  omlx: {type: omlx}\n"
    )
```

→

```python
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
```

This single helper edit removes the dead setup for both `test_model_screen_provider_table_count_zero_for_empty_family` and `test_model_screen_add_form_offers_all_providers_for_empty_family`, since they both call `_make_screen`.

- [ ] **Step 5: Verify the file is clean**

Run: `cd /Users/keith/github/ohanaverse/modelman && grep -n "MODELMAN_CONFIG\|MODELMAN_FAMILY_DIR" tests/screens/test_app_navigation.py`
Expected: no matches.

- [ ] **Step 6: Run the file**

Run: `cd /Users/keith/github/ohanaverse/modelman && uv run pytest tests/screens/test_app_navigation.py -v`
Expected: all tests PASS (same count as before this task — this is a pure cleanup, no test added/removed/skipped).

- [ ] **Step 7: Commit**

```bash
git add tests/screens/test_app_navigation.py
git commit -m "test(app_navigation): drop dead MODELMAN_CONFIG/MODELMAN_FAMILY_DIR setup, stale PR 3 comment"
```

---

### Task 5: Drop vestigial `MODELMAN_CONFIG` setup in `test_status.py`

**Files:**
- Modify: `tests/screens/test_status.py:65-77` (`app_with_apply` fixture), `:153-157` (`test_status_screen_esc_opens_cancel_dialog_and_cancel_stops`), `:235-239` (`test_status_screen_cancel_writes_immediate_feedback`)

**Interfaces:** none — cleanup only. `PendingChanges`/`StatusScreen` (both already fully migrated to `Registry`/`StateStore` since PR 2) never read `config.py`; these three sites write a scratch `config.yaml` and point `MODELMAN_CONFIG` at it for no reason anything in the test's own call path uses.

- [ ] **Step 1: Clean the `app_with_apply` fixture**

Replace:

```python
@pytest.fixture
def app_with_apply(monkeypatch, tmp_path):
    """Spin up a ModelmanApp with registry seeded, ready for a
    StatusScreen to drive a PendingChanges apply run.

    Yields (registry, state, reg_path, state_path).
    """
    reg, state, reg_path, state_path, _ = _setup(tmp_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")
    yield reg, state, reg_path, state_path
```

with:

```python
@pytest.fixture
def app_with_apply(monkeypatch, tmp_path):
    """Spin up a ModelmanApp with registry seeded, ready for a
    StatusScreen to drive a PendingChanges apply run.

    Yields (registry, state, reg_path, state_path).
    """
    reg, state, reg_path, state_path, _ = _setup(tmp_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    yield reg, state, reg_path, state_path
```

- [ ] **Step 2: Clean `test_status_screen_esc_opens_cancel_dialog_and_cancel_stops`**

Replace:

```python
    reg, state, reg_path, state_path, [o35, q8] = _setup(tmp_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    gate = __import__("threading").Event()
```

with:

```python
    reg, state, reg_path, state_path, [o35, q8] = _setup(tmp_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    gate = __import__("threading").Event()
```

- [ ] **Step 3: Clean `test_status_screen_cancel_writes_immediate_feedback`**

Replace:

```python
    reg, state, reg_path, state_path, [o35, _q8] = _setup(tmp_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    gate = threading.Event()
```

with:

```python
    reg, state, reg_path, state_path, [o35, _q8] = _setup(tmp_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    gate = threading.Event()
```

- [ ] **Step 4: Confirm no other `MODELMAN_CONFIG` references remain in this file**

Run: `cd /Users/keith/github/ohanaverse/modelman && grep -n "MODELMAN_CONFIG\|config.yaml" tests/screens/test_status.py`
Expected: no matches.

- [ ] **Step 5: Run the file**

Run: `cd /Users/keith/github/ohanaverse/modelman && uv run pytest tests/screens/test_status.py -v`
Expected: all tests PASS (same count as before — pure cleanup).

- [ ] **Step 6: Commit**

```bash
git add tests/screens/test_status.py
git commit -m "test(status): drop dead MODELMAN_CONFIG setup — PendingChanges never reads config.py"
```

---

### Task 6: Document `config.py`/`manifest.py` as migrate-time-only

**Files:**
- Modify: `src/modelman/config.py:1` (module docstring)
- Modify: `src/modelman/manifest.py:1` (module docstring)

**Interfaces:** none — docstring-only change, matches the pattern `queue.py` already established ("The legacy families/<family>.yaml manifest is no longer written by the TUI; it survives as a migrate-time input only.").

- [ ] **Step 1: Update `config.py`'s docstring**

Replace:

```python
"""Load ~/.config/local-ai/config.yaml."""
```

with:

```python
"""Load ~/.config/local-ai/config.yaml.

As of Phase 2 (see docs/superpowers/specs/2026-08-27-shared-model-
registry-design.md), this legacy config file and MODELMAN_CONFIG are
read only by `modelman migrate` (src/modelman/main.py, migrate.py) —
every TUI code path was migrated onto registry.toml (registry.py) by
Phase 2 PR 2/PR 3. Do not add a new caller of load_config() outside
the migrate path; add to registry.toml instead.
"""
```

- [ ] **Step 2: Update `manifest.py`'s docstring**

Replace:

```python
"""Family manifest: list of variants and which are downloaded."""
```

with:

```python
"""Family manifest: list of variants and which are downloaded.

As of Phase 2 (see docs/superpowers/specs/2026-08-27-shared-model-
registry-design.md), families/*.yaml and MODELMAN_FAMILY_DIR are read
only by `modelman migrate` (src/modelman/main.py, migrate.py) — every
TUI code path was migrated onto registry.toml/modelman.toml
(registry.py/state.py) by Phase 2 PR 2/PR 3. Do not add a new caller
of load_manifest()/get_family_dir() outside the migrate path.
"""
```

- [ ] **Step 3: Confirm both modules still import cleanly**

Run: `cd /Users/keith/github/ohanaverse/modelman && uv run python -c "import modelman.config, modelman.manifest"`
Expected: no output, exit 0.

- [ ] **Step 4: Commit**

```bash
git add src/modelman/config.py src/modelman/manifest.py
git commit -m "docs(config,manifest): mark migrate-time-only, matching queue.py's existing note"
```

---

### Task 7: Full verification

**Files:** none new — verification only.

- [ ] **Step 1: Run the full test suite, lint, and typecheck**

Run: `cd /Users/keith/github/ohanaverse/modelman && uv run pytest -v && uv run ruff check src/ tests/ && uv run mypy src/`
Expected: all tests PASS, zero `SKIPPED`, ruff reports no findings, mypy reports no errors.

- [ ] **Step 2: Confirm no stray "PR 3" references remain**

Run: `cd /Users/keith/github/ohanaverse/modelman && grep -rn "PR 3\|migrates in PR 3\|migrates to Registry" src/ tests/`
Expected: at most `tests/screens/test_family_edit.py`'s module docstring (a historical note about when that file was migrated, not a skip reason — leave it) and `docs/superpowers/plans/2026-08-27-shared-model-registry-phase2-pr3.md` (a plan document, not code). No `@pytest.mark.skip` reasons should remain.

- [ ] **Step 3: Confirm `MODELMAN_CONFIG`/`MODELMAN_FAMILY_DIR` usage is now isolated to the migrate path**

Run: `cd /Users/keith/github/ohanaverse/modelman && grep -rln "MODELMAN_CONFIG\|MODELMAN_FAMILY_DIR" src/ tests/`
Expected exactly these files, no others:
- `src/modelman/config.py`, `src/modelman/manifest.py` (the env-var reads themselves — untouched)
- `src/modelman/main.py`, `src/modelman/migrate.py` (the migrate command — untouched)
- `tests/commands/test_migrate.py` (tests the migrate command — untouched)

If any other file appears, one of Tasks 1–5 missed a spot — go back and clean it before considering this plan done.

- [ ] **Step 4: Final commit (only if Steps 1-3 required fixes)**

```bash
git add -A
git commit -m "fix(cleanup): address PR 4 verification pass findings"
```

(Skip this step entirely if Steps 1–3 required no changes — don't create an empty commit.)
