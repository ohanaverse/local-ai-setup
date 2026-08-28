import pytest

from modelman.app import ModelmanApp
from modelman.registry import (
    AuthConfig,
    ModelEntry,
    ProviderEntry,
    Registry,
    load_registry,
    save_registry,
)
from modelman.state import StateStore, save_state


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


@pytest.mark.asyncio
async def test_app_launches_into_family_screen():
    app = ModelmanApp()
    async with app.run_test():
        from modelman.screens.families import FamilyScreen

        assert isinstance(app.screen, FamilyScreen)


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


@pytest.mark.asyncio
async def test_app_with_initial_family_launches_into_model_screen(tmp_path, monkeypatch):
    """`ModelmanApp(family=...)` seeds registry.toml/modelman.toml and
    pushes ModelScreen pointing at them."""
    from modelman.screens.models import ModelScreen

    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        models=[
            ModelEntry(
                id="ollama/ornith:35b", family="ornith", provider_id="ollama",
                model_name="ornith:35b",
            ),
        ],
    )
    save_registry(reg, reg_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    from modelman.app import ModelmanApp

    app = ModelmanApp(family="ornith")
    async with app.run_test() as pilot:
        await pilot.pause()
        assert isinstance(app.screen, ModelScreen)


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
        table = app.screen.query_one("DataTable")
        assert table.row_count == 1

    from modelman.state import load_state

    reloaded = load_state(state_path)
    assert "mamba" in reloaded.families


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


# ---------------------------------------------------------------------------
# ModelScreen constructed directly (no FamilyScreen / app.py round-trip).
# ---------------------------------------------------------------------------


def _make_screen(tmp_path, monkeypatch, *, family: str = "ornith", entries=()):
    """Build a ModelScreen with registry.toml + modelman.toml in tmp_path
    and seed registry with the given ModelEntries. Returns
    (ms, registry_path, state_path)."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[
            ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none")),
            ProviderEntry(id="llamacpp", name="L", auth=AuthConfig(type="none")),
            ProviderEntry(id="omlx", name="X", auth=AuthConfig(type="omlx")),
        ],
        models=list(entries),
    )
    save_registry(reg, reg_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    from modelman.screens.models import ModelScreen

    ms = ModelScreen(
        registry=reg,
        state=StateStore(),
        family=family,
        registry_path=reg_path,
        state_path=state_path,
        available_providers=["ollama", "llamacpp", "omlx"],
    )
    return ms, reg_path, state_path


@pytest.mark.asyncio
async def test_model_screen_shows_all_providers_for_empty_family(
    tmp_path, monkeypatch,
):
    """The provider-table on the left of the model screen must show
    every configured provider, even when the family has zero entries."""
    from textual.widgets import DataTable

    ms, _reg, _state = _make_screen(tmp_path, monkeypatch)

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        pt = ms.query_one("#provider-table", DataTable)
        keys = sorted(str(rk.value) for rk in pt.rows)
        assert keys == ["llamacpp", "ollama", "omlx"], (
            f"provider table should list every configured provider "
            f"(got {keys}); an empty family must not hide them"
        )


@pytest.mark.asyncio
async def test_model_screen_provider_table_count_zero_for_empty(
    tmp_path, monkeypatch,
):
    """When the family has 0 entries, each provider row should show
    count '0' (not blank)."""
    from textual.widgets import DataTable

    from modelman.app import ModelmanApp
    from modelman.screens.models import ModelScreen

    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[
            ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none")),
            ProviderEntry(id="llamacpp", name="L", auth=AuthConfig(type="none")),
            ProviderEntry(id="omlx", name="X", auth=AuthConfig(type="omlx")),
        ],
        models=[],
    )
    save_registry(reg, reg_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    ms = ModelScreen(
        registry=reg,
        state=StateStore(),
        family="x",
        registry_path=reg_path,
        state_path=state_path,
        available_providers=["ollama", "llamacpp", "omlx"],
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        pt = ms.query_one("#provider-table", DataTable)
        provider_cells = list(pt.get_column_at(0))
        count_cells = [str(c) for c in pt.get_column_at(1)]
        assert sorted(str(c) for c in provider_cells) == [
            "llamacpp",
            "ollama",
            "omlx",
        ], provider_cells
        for c in count_cells:
            assert c == "0", f"empty family: count column must be 0, got {c!r}"


@pytest.mark.asyncio
async def test_model_screen_add_form_offers_all_providers_for_empty_family(
    tmp_path, monkeypatch,
):
    """The AddModel form's provider Label must reflect the full
    configured-provider list when the user presses 'a' from an empty
    family's model screen."""
    from modelman.app import ModelmanApp
    from modelman.screens.forms import ModelForm

    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[
            ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none")),
            ProviderEntry(id="llamacpp", name="L", auth=AuthConfig(type="none")),
            ProviderEntry(id="omlx", name="X", auth=AuthConfig(type="omlx")),
        ],
        models=[],
    )
    save_registry(reg, reg_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    from modelman.screens.models import ModelScreen

    ms = ModelScreen(
        registry=reg,
        state=StateStore(),
        family="x",
        registry_path=reg_path,
        state_path=state_path,
        available_providers=["ollama", "llamacpp", "omlx"],
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        captured: list[ModelForm] = []
        original_push = ms.app.push_screen

        def tracking_push(screen, *args, **kwargs):
            if isinstance(screen, ModelForm):
                captured.append(screen)
            return original_push(screen, *args, **kwargs)

        ms.app.push_screen = tracking_push
        ms.action_add_model()
        await pilot.pause()

    assert len(captured) == 1, f"expected ModelForm push; got {captured}"
    assert captured[0]._initial_provider == "ollama"


@pytest.mark.asyncio
async def test_model_screen_starts_with_cursor_on_first_provider(
    tmp_path, monkeypatch,
):
    """When the model screen mounts, the cursor must be on row 0 of
    the provider-table (the first configured provider)."""
    from textual.widgets import DataTable

    ms, _reg, _state = _make_screen(tmp_path, monkeypatch)

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        pt = ms.query_one("#provider-table", DataTable)
        assert pt.cursor_coordinate.row == 0
        assert pt.cursor_coordinate.column == 0


def test_entry_kwargs_preserves_nested_fetch_dataclass():
    """_entry_kwargs must deep-copy a ModelEntry so the snapshot's
    nested Fetch dataclass survives the round-trip through kwargs."""
    from modelman.registry import Fetch, ModelEntry
    from modelman.screens.models import _entry_kwargs, _model_entry_to_variant

    entry = ModelEntry(
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
    kwargs = _entry_kwargs(entry)
    restored = ModelEntry(**kwargs)
    assert isinstance(restored.fetch, Fetch)
    assert restored.fetch is not entry.fetch
    assert restored.fetch.repo == entry.fetch.repo
    assert restored.fetch.files == entry.fetch.files
    assert restored.fetch.quantizations == entry.fetch.quantizations
    # This is the path that crashes if fetch became a plain dict.
    variant = _model_entry_to_variant(restored)
    assert variant["quantizations"] == ["Q4_K_M"]


@pytest.mark.asyncio
async def test_model_screen_add_appends_model_entry_to_registry(
    tmp_path, monkeypatch,
):
    """Submitting ModelForm in add mode appends a ModelEntry to
    registry.models with the adapter's translation. The registry is
    not persisted until apply/exit, so we assert in-memory state."""
    from textual.widgets import Input

    ms, reg_path, _state = _make_screen(tmp_path, monkeypatch)

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        await pilot.press("a")
        await pilot.pause()
        app.screen.query_one("#model", Input).focus()
        for ch in "ornith:8b":
            await pilot.press(ch)
        await pilot.press("enter")
        await pilot.pause()

    ids = [m.id for m in ms.registry.models]
    assert "ollama/ornith:8b" in ids
    added = next(m for m in ms.registry.models if m.id == "ollama/ornith:8b")
    assert added.family == ms.family
    assert added.provider_id == "ollama"
    assert added.model_name == "ornith:8b"
    assert added.fetch is None
    # Disk should remain unchanged until the user applies.
    reloaded = load_registry(reg_path)
    assert "ollama/ornith:8b" not in [m.id for m in reloaded.models]


@pytest.mark.asyncio
async def test_model_screen_toggle_download_queues_variant(
    tmp_path, monkeypatch,
):
    """Pressing `x` on a not_downloaded row queues the variant for download."""
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry

    a = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b",
    )
    ms, _reg, _state = _make_screen(tmp_path, monkeypatch, entries=[a])

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = None
    monkeypatch.setattr(
        prov_registry.ProviderRegistry,
        "get",
        staticmethod(lambda name, cfg: stub),
    )

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        await pilot.press("x")
        await pilot.pause()
        assert "ollama/o35" in ms.queued_downloads


@pytest.mark.asyncio
async def test_model_screen_discard_restores_fetch_dataclass(tmp_path, monkeypatch):
    """Discarding pending changes must restore the snapshot without
    turning nested Fetch dataclasses into plain dicts."""
    from unittest.mock import MagicMock

    from textual.widgets import Button

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
    monkeypatch.setattr(
        prov_registry.ProviderRegistry,
        "get",
        staticmethod(lambda name, cfg: stub),
    )

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        # Switch from the default ollama provider to llamacpp so the
        # model row is visible and can be toggled.
        await pilot.press("down")
        await pilot.pause()
        assert ms.selected_provider == "llamacpp"
        # Queue a download so escape opens the exit dialog.
        await pilot.press("x")
        await pilot.pause()
        assert "llamacpp/ornith-q4" in ms.queued_downloads
        # Open the exit dialog and discard.
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
    assert ms.queued_downloads == {}


@pytest.mark.asyncio
async def test_model_screen_delete_only_for_downloaded(tmp_path, monkeypatch):
    """Pressing `d` on a not_downloaded row is a no-op (delete target
    must actually exist on disk before we remove it)."""
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry

    a = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b",
    )
    ms, _reg, _state = _make_screen(tmp_path, monkeypatch, entries=[a])

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = None
    monkeypatch.setattr(
        prov_registry.ProviderRegistry,
        "get",
        staticmethod(lambda name, cfg: stub),
    )

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        assert ms.queued_deletes == {}


# ---------------------------------------------------------------------------
# _variant_to_model_entry adapter
# ---------------------------------------------------------------------------


def test_variant_to_model_entry_ollama_no_fetch():
    """Ollama tags produce a ModelEntry with fetch=None (ollama resolves
    the tag server-side; no HF repo / files)."""
    from modelman.screens.models import _variant_to_model_entry

    variant = {"id": "ollama/x:7b", "provider": "ollama", "name": "x:7b"}
    reg = Registry(providers=[
        ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none")),
    ])
    entry = _variant_to_model_entry(variant, family="f", registry=reg)

    assert entry.id == "ollama/x:7b"
    assert entry.family == "f"
    assert entry.provider_id == "ollama"
    assert entry.model_name == "x:7b"
    assert entry.fetch is None


def test_variant_to_model_entry_llamacpp_with_repo_and_file():
    """llamacpp/omlx variants carry a Fetch with repo + single file."""
    from modelman.screens.models import _variant_to_model_entry

    variant = {
        "id": "llamacpp/o--r--x.gguf",
        "provider": "llamacpp",
        "name": "x.gguf",
        "repo": "o/r",
        "files": ["x.gguf"],
    }
    reg = Registry(providers=[
        ProviderEntry(id="llamacpp", name="L", auth=AuthConfig(type="none")),
    ])
    entry = _variant_to_model_entry(variant, family="f", registry=reg)

    assert entry.id == "llamacpp/o--r--x.gguf"
    assert entry.provider_id == "llamacpp"
    assert entry.model_name == "x.gguf"
    assert entry.fetch is not None
    assert entry.fetch.repo == "o/r"
    assert entry.fetch.files == ["x.gguf"]


def test_variant_to_model_entry_edit_mode_preserves_id():
    """Editing a variant must keep the original id (id is the stable key
    for queued_downloads / registry lookup)."""
    from modelman.screens.models import _variant_to_model_entry

    variant = {
        "id": "llamacpp/old",
        "provider": "llamacpp",
        "name": "new.gguf",
        "repo": "o/r",
        "files": ["new.gguf"],
    }
    reg = Registry(providers=[
        ProviderEntry(id="llamacpp", name="L", auth=AuthConfig(type="none")),
    ])
    entry = _variant_to_model_entry(variant, family="f", registry=reg)
    assert entry.id == "llamacpp/old"


def test_variant_to_model_entry_raises_for_unknown_provider():
    """A variant whose `provider` doesn't appear in registry.providers
    raises — caller must look up the provider entry to attribute the model."""
    from modelman.screens.models import _variant_to_model_entry

    variant = {"id": "bogus/x", "provider": "bogus", "name": "x"}
    reg = Registry(providers=[
        ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none")),
    ])
    with pytest.raises(KeyError):
        _variant_to_model_entry(variant, family="f", registry=reg)


def test_round_trip_preserves_quantizations():
    """`_variant_to_model_entry` followed by `_model_entry_to_variant`
    must preserve every field, including `quantizations`. Catches the
    silent round-trip loss documented in the PR 2 final review
    (Important #1: `_model_entry_to_variant` used to emit
    `quantizations: None` regardless of the entry's stored Fetch)."""
    from modelman.screens.models import (
        _model_entry_to_variant,
        _variant_to_model_entry,
    )

    quants = ["Q4_K_M", "Q5_K_M", "Q8_0"]
    variant = {
        "id": "llamacpp/o--r--x.gguf",
        "provider": "llamacpp",
        "name": "x.gguf",
        "repo": "o/r",
        "files": ["x.gguf"],
        "quantizations": list(quants),
    }
    reg = Registry(providers=[
        ProviderEntry(id="llamacpp", name="L", auth=AuthConfig(type="none")),
    ])
    entry = _variant_to_model_entry(variant, family="f", registry=reg)
    assert entry.fetch is not None
    assert entry.fetch.quantizations == quants

    roundtripped = _model_entry_to_variant(entry)
    assert roundtripped["quantizations"] == quants
    assert roundtripped["repo"] == "o/r"
    assert roundtripped["files"] == ["x.gguf"]
