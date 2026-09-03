import asyncio

import pytest
from textual.widgets import DataTable

from modelman.app import ModelmanApp
from modelman.registry import (
    AuthConfig,
    FamilyEntry,
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
        providers=[ProviderEntry(id=p, name=p, auth=AuthConfig(type="none")) for p in providers],
        models=list(models),
    )
    save_registry(reg, reg_path)
    store = StateStore()
    for mid, path in (downloaded or {}).items():
        store.set(mid, ModelState(ready=True, disk_path=path))
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
async def test_direct_download_syncs_agent_providers(tmp_path, monkeypatch):
    """`ModelmanApp(family=...)` must call sync_agent_providers so the
    ModelScreen sees native-agent providers imported from wt's
    config, not just the providers already written to registry.toml."""
    from modelman.screens.models import ModelScreen

    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    wt_dir = tmp_path / "wt"
    wt_dir.mkdir()
    wt_config = wt_dir / "config.toml"
    wt_config.write_text('[[agents]]\nname = "claude"\n')
    monkeypatch.setenv("MODELMAN_WT_DIR", str(wt_dir))

    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        models=[
            ModelEntry(
                id="ollama/ornith:35b",
                family="ornith",
                provider_id="ollama",
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
        # Native provider synced from wt config should be
        # present in the in-memory registry passed to ModelScreen.
        assert app.screen.registry.provider("claude").auth.type == "native"
        # And it should be in the available-providers list used for the
        # provider pane / add-model form.
        assert "claude" in app.screen.available_providers


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
                id="ollama/ornith:35b",
                family="ornith",
                provider_id="ollama",
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
    tmp_path,
    monkeypatch,
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


@pytest.mark.asyncio
async def test_delete_family_cancel_keeps_empty_family(
    tmp_path,
    monkeypatch,
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
    tmp_path,
    monkeypatch,
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
        "Escape on the delete-family confirm modal must dismiss with False and preserve the family."
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
async def test_toggle_ready_queues_variant(tmp_path, monkeypatch):
    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
    _seed_registry_and_state(tmp_path, monkeypatch, models=[o35])

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.press("x")
        await pilot.pause()
        pending = app.screen.queued_ready
        assert pending.get("ollama/o35") is True


@pytest.mark.asyncio
async def test_status_shows_four_states(tmp_path, monkeypatch):
    """Status column reflects: ready, not ready, queued for
    download, queued for delete."""
    from unittest.mock import MagicMock

    dl = ModelEntry(id="ollama/dl", family="ornith", provider_id="ollama", model_name="dl")
    missing = ModelEntry(
        id="ollama/missing", family="ornith", provider_id="ollama", model_name="missing"
    )
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
    stub.is_downloaded.side_effect = lambda v: v["id"] == "ollama/dl"
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
        rows = {r[2]: r for r in [mt.get_row_at(i) for i in range(mt.row_count)]}
        assert "✓" in rows["dl"][4]
        assert "○" in rows["missing"][4]

        # Toggle download on missing → ↓
        # Cursor is on the first row (dl). Move down then x.
        mt.cursor_coordinate = (1, 0)
        await pilot.press("x")
        await pilot.pause()
        mt = app.screen.query_one("#model-table", DataTable)
        rows = {r[2]: r for r in [mt.get_row_at(i) for i in range(mt.row_count)]}
        assert "↓" in rows["missing"][4]

        # Toggle delete on dl → ✗
        mt.cursor_coordinate = (0, 0)
        await pilot.press("d")
        await pilot.pause()
        mt = app.screen.query_one("#model-table", DataTable)
        rows = {r[2]: r for r in [mt.get_row_at(i) for i in range(mt.row_count)]}
        assert "✗" in rows["dl"][4]


@pytest.mark.asyncio
async def test_delete_action_queues_even_when_not_downloaded(tmp_path, monkeypatch):
    """Pressing 'd' on a not-downloaded variant still queues a delete —
    the gate was removed; apply() handles the absent-artifact case."""
    from unittest.mock import MagicMock

    missing = ModelEntry(
        id="ollama/missing", family="ornith", provider_id="ollama", model_name="missing"
    )
    _seed_registry_and_state(tmp_path, monkeypatch, models=[missing])

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = None
    stub.is_downloaded.return_value = False
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
        assert "ollama/missing" in app.screen.queued_deletes


@pytest.mark.asyncio
async def test_add_then_delete_model_queues_changes(tmp_path, monkeypatch, stub_ollama_caps):
    from unittest.mock import MagicMock

    from textual.widgets import Input

    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
    _seed_registry_and_state(
        tmp_path, monkeypatch, models=[o35], downloaded={"ollama/o35": str(tmp_path / "downloaded")}
    )

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = 10 * 1024**3
    stub.is_downloaded.return_value = True
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
        # Note: the provider select is sorted alphabetically now,
        # so the initial provider may not be "ollama" if other
        # providers (e.g. wt's "agy", "claude", etc.)
        # are synced into the registry. Force the provider back to
        # ollama to keep this test focused on the add flow.
        from textual.widgets import Select

        provider_sel = app.screen.query_one("#provider-select", Select)
        provider_sel.value = "ollama"
        await pilot.pause()
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

    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
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
        assert row[4] == "[green]✓[/green]"  # status
        assert row[8] == "22.0 GB"  # size (col 8 after COST/TIER were added)


@pytest.mark.asyncio
async def test_reconcile_does_not_persist_to_disk_on_cancel(tmp_path, monkeypatch):
    """Reconcile is in-memory only until Apply. Cancelling out of the dialog
    (or having no queue at all) must not write modelman.toml."""
    from unittest.mock import MagicMock

    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
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

    assert not load_state(state_path).get("ollama/o35").ready


@pytest.mark.asyncio
async def test_expose_after_reconcile_survives_stale_state(tmp_path, monkeypatch):
    """Model on disk but modelman.toml says ready=False (stale): the
    expose gate accepts via the reconcile overlay, and the apply-time
    check (which reads state.ready) must not then fail with a
    spurious 'not ready'. The apply-time merge of reconciled
    entries into state is what bridges the two gates."""
    from unittest.mock import MagicMock

    from textual.widgets import Button, DataTable

    from modelman.screens.status import StatusScreen

    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
    _reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[o35])
    # Stale state: the model is on disk but modelman.toml claims otherwise.
    from modelman.state import ModelState

    store = StateStore()
    store.set("ollama/o35", ModelState(ready=False))
    save_state(store, state_path)
    # LiteLLM config for the expose step.
    litellm_path = tmp_path / "litellm" / "config.yaml"
    from modelman.litellm import save_litellm_config

    save_litellm_config({"model_list": [], "general_settings": {}}, litellm_path)
    monkeypatch.setenv("MODELMAN_LITELLM_CONFIG", str(litellm_path))

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = 22 * 1024**3  # reconcile: on disk
    stub.is_downloaded.return_value = True
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    from modelman.app import ModelmanApp

    app = ModelmanApp(family="ornith")
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.pause()  # let reconcile settle
        mt = app.screen.query_one("#model-table", DataTable)
        mt.cursor_coordinate = (0, 0)
        # Reconcile writes state.ready directly, so the expose gate sees
        # a ready model; toggle expose with `x`.
        await pilot.press("x")  # toggle expose
        await pilot.pause()
        assert "expose 1" in str(app.screen.query_one("#pending-bar").render())
        await pilot.press("escape")
        await pilot.pause()
        for btn in app.screen.query(Button):
            if btn.id == "apply":
                btn.press()
                break
        for _ in range(50):
            await pilot.pause()
            cur = app.screen
            if isinstance(cur, StatusScreen) and cur.done:
                break

    from modelman.state import load_state

    final = load_state(state_path).get("ollama/o35")
    # The merge promoted the reconciled download into state, so the
    # expose succeeded instead of failing with 'not ready'.
    assert final.ready is True
    assert final.litellm_exposed is True
    from modelman.litellm import load_litellm_config

    config = load_litellm_config(litellm_path)
    assert [r["model_name"] for r in config["model_list"]] == ["ollama/o35"]


@pytest.mark.asyncio
async def test_apply_merges_reconciled_state_into_manifest(tmp_path, monkeypatch):
    """When the user Applies, reconciled entries that aren't yet in
    modelman.toml get written to disk. The existing apply path also
    runs queued downloads."""
    from unittest.mock import MagicMock

    from textual.widgets import Button, DataTable

    from modelman.screens.status import StatusScreen

    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
    q8 = ModelEntry(id="ollama/q8", family="ornith", provider_id="ollama", model_name="ornith:8b")
    _reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[o35, q8])

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"

    def fake_size_of(v):
        return 22 * 1024**3 if v["id"] == "ollama/o35" else None

    stub.size_of.side_effect = fake_size_of
    stub.is_downloaded.side_effect = lambda v: v["id"] == "ollama/o35"
    stub.download.return_value = "ollama:ornith:8b"
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    from modelman.app import ModelmanApp

    app = ModelmanApp(family="ornith")
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.pause()  # let reconcile settle
        # Queue a download for q8 (which is not ready), so escape triggers
        # the apply dialog. `r` toggles ready (download); `x` is now expose.
        mt = app.screen.query_one("#model-table", DataTable)
        # Click on the q8 row.
        mt.cursor_coordinate = (1, 0)
        await pilot.press("r")
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

    assert load_state(state_path).get("ollama/o35").ready


@pytest.mark.asyncio
async def test_escape_with_pending_shows_dialog_and_apply(tmp_path, monkeypatch):
    from unittest.mock import MagicMock

    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
    _reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[o35])

    from modelman.providers import registry

    original_get = registry.ProviderRegistry.get
    stub = MagicMock()
    stub.download.return_value = "/tmp/fake"
    stub.is_downloaded.return_value = False
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
        await pilot.press("r")  # queue a download (ready) — `x` is now expose
        await pilot.pause()
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

    assert load_state(state_path).get("ollama/o35").ready


@pytest.mark.asyncio
async def test_family_screen_reconciles_size_from_provider(tmp_path, monkeypatch):
    """A family with no downloaded entry in modelman.toml should still
    show a non-zero size on the family screen if the provider reports
    the model is on disk."""
    from unittest.mock import MagicMock

    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
    # No downloaded entry in state, but the model is actually on disk
    # per the (stubbed) provider.
    _seed_registry_and_state(tmp_path, monkeypatch, models=[o35])

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = 22 * 1024**3
    stub.is_downloaded.return_value = True
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
    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
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
        assert app.screen.queued_ready.get("ollama/o35") is True
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
    assert not load_state(state_path).get("ollama/o35").ready


@pytest.mark.asyncio
async def test_family_screen_reconciles_on_resume_after_apply(tmp_path, monkeypatch):
    """After popping back from StatusScreen (apply completed), the
    FamilyScreen should re-reconcile so the SIZE and DOWNLOADED columns
    reflect the new on-disk state. Without this, deleting a model left
    the family row showing the pre-delete size until the user pressed 'r'.
    """
    from unittest.mock import MagicMock

    # Pre-condition: both models on disk per state.
    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
    o70 = ModelEntry(
        id="ollama/o70", family="ornith", provider_id="ollama", model_name="ornith:70b"
    )
    reg_path, state_path = _seed_registry_and_state(
        tmp_path,
        monkeypatch,
        models=[o35, o70],
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
    stub.is_downloaded.side_effect = lambda v: v["id"] in {"ollama/o35", "ollama/o70"}
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
        stub.is_downloaded.side_effect = lambda v: v["id"] == "ollama/o70"

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
        # The single model table has focus on mount; the first model
        # row is already highlighted. Press Enter to open the edit
        # dialog for o35.
        await pilot.press("enter")
        await pilot.pause()
        # Give the modal screen time to mount.
        for _ in range(10):
            if isinstance(app.screen, ModelForm):
                break
            await pilot.pause()

        assert captured, "ModelForm was not pushed on Enter"
        assert isinstance(app.screen, ModelForm)
        form = app.screen
        # Edit-mode pre-fill: model_input field has the ollama tag.
        from textual.widgets import Input

        model_input = form.query_one("#model", Input)
        assert model_input.value == "ornith:35b"


# ---------------------------------------------------------------------------
# ModelScreen constructed directly (no FamilyScreen / app.py round-trip).
# ---------------------------------------------------------------------------


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


@pytest.mark.asyncio
async def test_model_screen_add_form_offers_all_providers_for_empty_family(
    tmp_path,
    monkeypatch,
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
    # Providers are now sorted alphabetically: llamacpp comes first.
    assert captured[0]._initial_provider == "llamacpp"


@pytest.mark.asyncio
async def test_model_screen_is_single_table_sorted_by_provider_then_name(tmp_path, monkeypatch):
    b_model = ModelEntry(id="ollama/b", family="ornith", provider_id="ollama", model_name="b:tag")
    a_model = ModelEntry(
        id="llamacpp/a",
        family="ornith",
        provider_id="llamacpp",
        model_name="a.gguf",
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
        # FAMILY, PROVIDER, MODEL, LOC, STATUS, EXPOSED, COST, TIER, SIZE
        providers_then_names = [(r[1], r[2]) for r in rows]
        assert providers_then_names == [("llamacpp", "a.gguf"), ("ollama", "b:tag")]
        assert rows[0][0] == "ornith"  # FAMILY column constant per row


@pytest.mark.asyncio
async def test_model_screen_add_appends_model_entry_to_registry(
    tmp_path,
    monkeypatch,
    stub_ollama_caps,
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
        # Force the provider select to ollama (alphabetical sort means
        # the default may differ if other providers are configured).
        from textual.widgets import Select

        provider_sel = app.screen.query_one("#provider-select", Select)
        provider_sel.value = "ollama"
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
async def test_model_screen_toggle_ready_queues_variant(
    tmp_path,
    monkeypatch,
):
    """Pressing `x` on a not-ready row queues the variant for ready-on."""
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry

    a = ModelEntry(
        id="ollama/o35",
        family="ornith",
        provider_id="ollama",
        model_name="ornith:35b",
    )
    ms, _reg, _state = _make_screen(tmp_path, monkeypatch, entries=[a])

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = None
    stub.is_downloaded.return_value = False
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
        assert ms.queued_ready.get("ollama/o35") is True


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
    stub.is_downloaded.return_value = False
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
        mt = ms.query_one("#model-table", DataTable)
        mt.cursor_coordinate = (0, 0)  # only one model in this family
        await pilot.press("x")
        await pilot.pause()
        assert ms.queued_ready == {"llamacpp/ornith-q4": True}
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
    assert ms.queued_ready == {}


@pytest.mark.asyncio
async def test_delete_action_noop_when_no_row_selected(tmp_path, monkeypatch):
    """Pressing 'd' with an empty table is a no-op (no row to act on)."""
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry

    ms, _reg, _state = _make_screen(tmp_path, monkeypatch)

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = None
    stub.is_downloaded.return_value = False
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
    reg = Registry(
        providers=[
            ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none")),
        ]
    )
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
    reg = Registry(
        providers=[
            ProviderEntry(id="llamacpp", name="L", auth=AuthConfig(type="none")),
        ]
    )
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
    reg = Registry(
        providers=[
            ProviderEntry(id="llamacpp", name="L", auth=AuthConfig(type="none")),
        ]
    )
    entry = _variant_to_model_entry(variant, family="f", registry=reg)
    assert entry.id == "llamacpp/old"


def test_variant_to_model_entry_raises_for_unknown_provider():
    """A variant whose `provider` doesn't appear in registry.providers
    raises — caller must look up the provider entry to attribute the model."""
    from modelman.screens.models import _variant_to_model_entry

    variant = {"id": "bogus/x", "provider": "bogus", "name": "x"}
    reg = Registry(
        providers=[
            ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none")),
        ]
    )
    with pytest.raises(KeyError):
        _variant_to_model_entry(variant, family="f", registry=reg)


def test_run_apply_does_not_swallow_provider_instantiation_errors(tmp_path, monkeypatch):
    """A provider that exists in the registry but fails to instantiate with
    a non-KeyError exception must not be silently treated as flag-only.
    The error should propagate out of _run_apply so the caller (StatusScreen)
    can surface it instead of masking it."""
    from modelman.providers import registry as prov_registry
    from modelman.screens.models import ModelScreen

    o35 = ModelEntry(
        id="ollama/o35",
        family="ornith",
        provider_id="ollama",
        model_name="ornith:35b",
    )
    reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[o35])

    real_get = prov_registry.ProviderRegistry.get

    def failing_get(name, cfg):
        if name == "ollama":
            raise TypeError("missing base_url")
        return real_get(name, cfg)

    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(failing_get))

    ms = ModelScreen(
        registry=load_registry(reg_path),
        state=StateStore(),
        family="ornith",
        registry_path=reg_path,
        state_path=state_path,
        available_providers=["ollama"],
    )
    ms.queued_ready["ollama/o35"] = True

    events: list[str] = []
    progress: list[str] = []
    registered: list = []

    with pytest.raises(TypeError, match="missing base_url"):
        ms._run_apply(events.append, progress.append, registered.append)


def test_model_entry_to_variant_preserves_location():
    """Editing a cloud-located ollama model must pass its location into
    the ModelForm so the Location select doesn't default back to local."""
    from modelman.screens.models import _model_entry_to_variant

    entry = ModelEntry(
        id="ollama/glm-5.2:cloud",
        family="glm",
        provider_id="ollama",
        model_name="glm-5.2:cloud",
        location="cloud",
    )
    variant = _model_entry_to_variant(entry)
    assert variant.get("location") == "cloud"


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
    reg = Registry(
        providers=[
            ProviderEntry(id="llamacpp", name="L", auth=AuthConfig(type="none")),
        ]
    )
    entry = _variant_to_model_entry(variant, family="f", registry=reg)
    assert entry.fetch is not None
    assert entry.fetch.quantizations == quants

    roundtripped = _model_entry_to_variant(entry)
    assert roundtripped["quantizations"] == quants
    assert roundtripped["repo"] == "o/r"
    assert roundtripped["files"] == ["x.gguf"]


@pytest.mark.asyncio
async def test_x_key_queues_expose_and_column_renders(tmp_path, monkeypatch):
    """Pressing `x` on a downloaded model queues an exposure change and the
    EXPOSED column reflects the queued target state."""
    from textual.widgets import DataTable

    from modelman.app import ModelmanApp
    from modelman.registry import ModelEntry
    from modelman.screens.models import ModelScreen

    reg_path, state_path = _seed_registry_and_state(
        tmp_path,
        monkeypatch,
        models=(
            ModelEntry(
                id="ollama/a",
                family="f",
                provider_id="ollama",
                model_name="a",
            ),
        ),
        downloaded={"ollama/a": "ollama:a"},
    )
    # Stub the provider so reconcile reports the model as on disk (size
    # non-None); otherwise the real ollama provider marks it not-downloaded
    # and `x` refuses to queue.
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = 10
    stub.is_downloaded.return_value = True
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    from modelman.registry import load_registry
    from modelman.state import load_state

    ms = ModelScreen(
        registry=load_registry(),
        state=load_state(),
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        available_providers=["ollama"],
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        mt = ms.query_one("#model-table", DataTable)
        # Focus the model table so `x` targets a model row.
        mt.focus()
        await pilot.press("x")
        await pilot.pause()
        assert "ollama/a" in ms.queued_exposes
        assert ms.queued_exposes["ollama/a"] is True
        # EXPOSED column exists (FAMILY, PROVIDER, MODEL, LOC, STATUS,
        # EXPOSED, COST, TIER, SIZE).
        assert len(mt.columns) == 9
        # pending bar reflects the queued expose
        bar = ms.query_one("#pending-bar")
        assert "expose 1" in bar.content


# ---------------------------------------------------------------------------
# Family moves (queued)
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_edit_model_changing_family_queues_move(tmp_path, monkeypatch):
    """Editing a model and picking a different family queues a move:
    pending bar shows it, the row shows a → glyph, and the in-memory
    registry family is untouched until apply."""
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
        families=["gemma4"],
    )

    from modelman.app import ModelmanApp
    from modelman.screens.forms import ModelForm

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        ms.query_one("#model-table", DataTable).focus()
        mt = ms.query_one("#model-table", DataTable)
        mt.cursor_coordinate = (0, 0)
        await pilot.press("e")
        await pilot.pause()
        assert isinstance(app.screen, ModelForm)

        from textual.widgets import Select

        sel = app.screen.query_one("#family-select", Select)
        assert str(sel.value) == "gemma4:26b-mlx"  # re-edit preselect = current family
        sel.value = "gemma4"
        await pilot.press("enter")  # submit the (prefilled) edit form
        await pilot.pause()

        assert ms.queued_moves == {"ollama/gemma4:26b-mlx": "gemma4"}
        # Registry NOT mutated in memory — move applies at apply time.
        assert ms.registry.models[0].family == "gemma4:26b-mlx"
        mt = ms.query_one("#model-table", DataTable)
        rows = {r[2]: r for r in [mt.get_row_at(i) for i in range(mt.row_count)]}
        assert "→" in rows["gemma4:26b-mlx"][4]  # STATUS column
        bar = ms.query_one("#pending-bar")
        assert "move 1" in bar.content


@pytest.mark.asyncio
async def test_edit_model_same_family_drops_queued_move(tmp_path, monkeypatch):
    """Editing a model back to the screen family cancels a pending move
    instead of queueing a no-op."""
    entry = ModelEntry(
        id="ollama/gemma4:26b-mlx",
        family="gemma4:26b-mlx",
        provider_id="ollama",
        model_name="gemma4:26b-mlx",
    )
    ms, _reg, _state = _make_screen(tmp_path, monkeypatch, family="gemma4:26b-mlx", entries=[entry])
    ms.queued_moves["ollama/gemma4:26b-mlx"] = "gemma4"

    from modelman.app import ModelmanApp
    from modelman.screens.forms import ModelForm

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        mt = ms.query_one("#model-table", DataTable)
        mt.focus()
        mt.cursor_coordinate = (0, 0)
        await pilot.press("e")
        await pilot.pause()
        assert isinstance(app.screen, ModelForm)
        # The re-edit Select honors the already-queued move's target…
        from textual.widgets import Select

        sel = app.screen.query_one("#family-select", Select)
        assert str(sel.value) == "gemma4"
        sel.value = "gemma4:26b-mlx"  # ...moved back to the screen family
        await pilot.press("enter")
        await pilot.pause()

    assert ms.queued_moves == {}


@pytest.mark.asyncio
async def test_edit_model_location_change_persists_to_registry_on_back(tmp_path, monkeypatch):
    """Editing a model's location (same family — empty queue) and returning
    to the family screen must persist the change to registry.toml.

    Regression: _on_edit_model mutated only the in-memory registry and
    queued nothing; FamilyScreen's on-screen-resume then reloaded the
    registry from disk, silently dropping the location edit.
    """
    entry = ModelEntry(
        id="ollama/gemma4:26b-mlx",
        family="gemma4:26b-mlx",
        provider_id="ollama",
        model_name="gemma4:26b-mlx",
        location="local",
    )
    ms, reg_path, _state = _make_screen(
        tmp_path, monkeypatch, family="gemma4:26b-mlx", entries=[entry]
    )

    from modelman.app import ModelmanApp
    from modelman.screens.forms import ModelForm

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        mt = ms.query_one("#model-table", DataTable)
        mt.focus()
        mt.cursor_coordinate = (0, 0)
        await pilot.press("e")
        await pilot.pause()
        assert isinstance(app.screen, ModelForm)

        from textual.widgets import Select

        # Ollama is the editable-location provider; flip local -> cloud.
        loc = app.screen.query_one("#location-select", Select)
        assert not loc.disabled
        assert str(loc.value) == "local"
        loc.value = "cloud"
        await pilot.press("enter")  # submit the (prefilled) edit form
        await pilot.pause()

        assert ms.registry.models[0].location == "cloud"
        # Nothing else queued: escape pops straight back to the previous
        # screen (this is the path that used to drop the edit).
        await pilot.press("escape")
        await pilot.pause()

    reloaded = load_registry(reg_path)
    assert reloaded.model("ollama/gemma4:26b-mlx").location == "cloud"


@pytest.mark.asyncio
async def test_location_edit_survives_family_screen_round_trip(tmp_path, monkeypatch):
    """End-to-end user journey: family screen -> open family -> edit a
    model's location -> escape back -> reopen the family. The LOCATION
    column must show the new value, not the pre-edit one."""
    reg_path, _state_path = _seed_registry_and_state(
        tmp_path,
        monkeypatch,
        models=(
            ModelEntry(
                id="ollama/gemma4:26b-mlx",
                family="gemma4:26b-mlx",
                provider_id="ollama",
                model_name="gemma4:26b-mlx",
                location="local",
            ),
        ),
    )
    del reg_path

    # Stub the provider so reconcile is fast and deterministic (this test
    # previously relied on the real ollama provider, which made it
    # environment-dependent and flaky).
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = False
    stub.size_of.return_value = None
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    from modelman.app import ModelmanApp
    from modelman.screens.families import FamilyScreen
    from modelman.screens.forms import ModelForm

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        assert isinstance(app.screen, FamilyScreen)
        fs = app.screen
        await _wait_reconcile_done(fs)
        await pilot.press("enter")  # open the family
        await pilot.pause()
        ms = app.screen
        assert ms.family == "gemma4:26b-mlx"

        mt = ms.query_one("#model-table", DataTable)
        mt.focus()
        mt.cursor_coordinate = (0, 0)
        await pilot.press("e")
        await pilot.pause()
        assert isinstance(app.screen, ModelForm)

        from textual.widgets import Select

        loc = app.screen.query_one("#location-select", Select)
        loc.value = "cloud"
        await pilot.press("enter")
        await pilot.pause()

        await pilot.press("escape")  # back to the family screen
        await pilot.pause()
        assert isinstance(app.screen, FamilyScreen)
        await _wait_reconcile_done(app.screen)

        await pilot.press("enter")  # reopen the family
        await pilot.pause()
        mt = app.screen.query_one("#model-table", DataTable)
        rows = [mt.get_row_at(i) for i in range(mt.row_count)]
        assert rows, "family should still list its model"
        # LOC column renders the cloud icon for cloud-located models.
        assert rows[0][3] == "↗"


async def _wait_reconcile_done(screen) -> None:
    """Yield until the screen's background reconcile worker has cleared
    its lock (the family table is disabled and blocks `enter` until then)."""
    for _ in range(200):
        if not screen._reconciling:
            return
        # Yield to the event loop so the worker's call_from_thread
        # callback (which clears _reconciling) can run.
        await asyncio.sleep(0)


def test_families_list_includes_state_only_families(tmp_path, monkeypatch):
    """`_families_list()` = registry families ∪ state.families — so a
    family created empty with `a` (like gemma4/deepseek-v4) is targetable
    before it has any models."""
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
        families=["gemma4", "deepseek-v4"],
    )
    # Both state-only families are targetable alongside the registry's
    # families (sorted). The plan's literal assert omitted deepseek-v4,
    # contradicting this test's name/docstring and the implementation.
    assert ms._families_list() == ["deepseek-v4", "gemma4", "gemma4:26b-mlx"]


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


# ---------------------------------------------------------------------------
# Exit path: exit dialog + apply hand-off + discard safety
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_exit_dialog_lists_move_and_apply_persists_it(tmp_path, monkeypatch):
    """Escape with a queued move opens the exit dialog (move N + a
    listed line), and apply persists the new family to registry.toml."""
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
    from modelman.screens.status import StatusScreen

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        # Queue the move the same way _on_edit_model does (the dialog
        # flow itself is covered by Task 4's tests).
        ms.queued_moves["ollama/gemma4:26b-mlx"] = "gemma4"
        await pilot.press("escape")
        await pilot.pause()
        from textual.widgets import Label

        labels = "\n".join(str(label.visual) for label in app.screen.query(Label))
        assert "move 1" in labels
        assert "→ ollama/gemma4:26b-mlx → gemma4" in labels
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

    reloaded = load_registry(reg_path)
    assert reloaded.model("ollama/gemma4:26b-mlx").family == "gemma4"


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


@pytest.mark.asyncio
async def test_discard_after_move_reverts_without_duplicates(tmp_path, monkeypatch):
    """`d` at the exit dialog must restore the registry exactly as at
    mount, keyed by model id. With a simulated in-memory family drift,
    the old family-scoped restore logic would leave the drifted live
    entry in place (family != self.family now) AND re-add the snapshot
    entry — duplicating the id. Only the id-keyed restore reverts it."""
    entry = ModelEntry(
        id="ollama/gemma4:26b-mlx",
        family="gemma4:26b-mlx",
        provider_id="ollama",
        model_name="gemma4:26b-mlx",
    )
    ms, reg_path, _state = _make_screen(
        tmp_path, monkeypatch, family="gemma4:26b-mlx", entries=[entry]
    )

    from modelman.app import ModelmanApp
    from modelman.registry import load_registry

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        ms.queued_moves["ollama/gemma4:26b-mlx"] = "gemma4"
        # Simulate in-memory family drift (e.g. a future registry-mutation
        # path): only an id-keyed restore can revert this cleanly — a
        # family-scoped filter would leave the drifted live entry in place
        # AND re-add the snapshot entry, duplicating the id.
        ms.registry.models[0].family = "gemma4"
        await pilot.press("escape")
        await pilot.pause()
        await pilot.press("d")  # discard
        await pilot.pause()

    ids = [m.id for m in ms.registry.models]
    assert ids == ["ollama/gemma4:26b-mlx"]
    assert ms.registry.models[0].family == "gemma4:26b-mlx"
    # Disk untouched.
    assert load_registry(reg_path).model("ollama/gemma4:26b-mlx").family == "gemma4:26b-mlx"


@pytest.mark.asyncio
async def test_discard_combined_move_add_and_download(tmp_path, monkeypatch):
    """The full discard invariant: move + out-of-family add + queued
    download in one session, then `d`. Registry ids, families, and the
    state.models keys must all be exactly as at mount."""
    keep_entry = ModelEntry(
        id="ollama/mover",
        family="ornith",
        provider_id="ollama",
        model_name="mover",
    )
    ms, _reg_path, _state = _make_screen(
        tmp_path, monkeypatch, family="ornith", entries=[keep_entry]
    )

    from modelman.app import ModelmanApp
    from modelman.screens.forms import ModelFormResult

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        # Move the existing model out of this family.
        ms.queued_moves["ollama/mover"] = "mamba"
        # Out-of-family add (exact _on_add_model path).
        ms._on_add_model(
            ModelFormResult(
                spec={"id": "ollama/newcomer", "provider": "ollama", "name": "newcomer:1b"},
                family="mamba",
            )
        )
        # Queued ready-on for a session-added model.
        ms.queued_ready["ollama/newcomer"] = True
        await pilot.press("escape")
        await pilot.pause()
        await pilot.press("d")  # discard
        await pilot.pause()

    assert [m.id for m in ms.registry.models] == ["ollama/mover"]
    assert ms.registry.models[0].family == "ornith"
    assert ms.queued_moves == {}
    assert ms.queued_ready == {}
    assert ms._added_ids == set()
    # State must not have gained any session entries.
    assert dict(ms.state.models) == {}


@pytest.mark.asyncio
async def test_discard_removes_out_of_family_added_model(tmp_path, monkeypatch):
    """A model added into a *different* family this session isn't caught
    by the family-scoped restore filter; discard must remove it."""
    entry = ModelEntry(
        id="ollama/keep",
        family="ornith",
        provider_id="ollama",
        model_name="keep",
    )
    ms, _reg_path, _state = _make_screen(tmp_path, monkeypatch, family="ornith", entries=[entry])

    from modelman.app import ModelmanApp
    from modelman.screens.forms import ModelFormResult

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        # Out-of-family add, applied exactly as _on_add_model does.
        ms._on_add_model(
            ModelFormResult(
                spec={"id": "ollama/moved", "provider": "ollama", "name": "moved:9b"},
                family="mamba",
            )
        )
        assert "ollama/moved" in [m.id for m in ms.registry.models]
        await pilot.press("escape")
        await pilot.pause()
        await pilot.press("d")  # discard
        await pilot.pause()

    ids = [m.id for m in ms.registry.models]
    assert ids == ["ollama/keep"]
