"""Tests for ModelScreen helpers and per-key actions."""

import pytest
from textual.widgets import DataTable

from modelman.app import ModelmanApp
from modelman.registry import (
    AuthConfig,
    FamilyEntry,
    ModelEntry,
    ProviderEntry,
    Registry,
    save_registry,
)
from modelman.screens.models import _variant_to_model_entry
from modelman.state import StateStore, save_state


def test_variant_to_model_entry_sets_source_curated():
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))]
    )
    variant = {"id": "ollama/x", "provider": "ollama", "name": "x"}
    entry = _variant_to_model_entry(variant, family="x", registry=registry)
    assert entry.source == "curated"


# ---------------------------------------------------------------------------
# Delete gating (regression: 'd' on an ollama cloud model did nothing)
# ---------------------------------------------------------------------------


def _seed_cloud_family(tmp_path, monkeypatch, *, location: str | None):
    """Seed a single-family registry with one not-downloaded ollama model
    (glm-5.2:cloud) whose registry entry carries `location`, and stub
    ProviderRegistry.get so reconcile never shells out to real ollama.
    size_of -> None mimics ollama cloud rows (SIZE column '-')."""
    from unittest.mock import MagicMock

    entry = ModelEntry(
        id="ollama/glm-5.2:cloud",
        family="glm",
        provider_id="ollama",
        model_name="glm-5.2:cloud",
        location=location,
        source="discovered",
    )
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        families=[FamilyEntry(name="glm")],
        models=[entry],
    )
    save_registry(reg, reg_path)
    save_state(StateStore(), state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    from modelman.providers import registry as provider_registry

    stub = MagicMock()
    stub.size_of.return_value = None  # cloud rows print SIZE '-'
    stub.is_downloaded.return_value = False
    stub.name = "ollama"
    monkeypatch.setattr(
        provider_registry.ProviderRegistry,
        "get",
        staticmethod(lambda name, cfg: stub),
    )
    return stub
    return stub


@pytest.mark.asyncio
async def test_d_on_cloud_model_queues_delete(tmp_path, monkeypatch):
    """'d' on a cloud-located ollama model must queue a delete even though
    it reconciles as not-downloaded (cloud entries never have a local size).
    Regression: the action silently returned and no delete could be queued.
    """
    stub = _seed_cloud_family(tmp_path, monkeypatch, location="cloud")
    stub.is_downloaded.return_value = True  # pulled: `ollama show` succeeds

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")  # open the only family's ModelScreen
        await pilot.pause()

        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)
        await pilot.press("d")
        await pilot.pause()
        assert "ollama/glm-5.2:cloud" in app.screen.queued_deletes


@pytest.mark.asyncio
async def test_d_on_local_not_downloaded_does_not_queue_but_notifies(tmp_path, monkeypatch):
    """A local model that is not on disk still can't be deleted (nothing to
    remove), but the action must say so instead of silently doing nothing.
    """
    from textual.app import Notify

    _seed_cloud_family(tmp_path, monkeypatch, location=None)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()

        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)
        notes: list[Notify] = []
        monkeypatch.setattr(app, "notify", lambda *a, **kw: notes.append(a and a[0]))
        await pilot.press("d")
        await pilot.pause()
        assert app.screen.queued_deletes == {}
        assert notes, "expected a notification explaining the blocked delete"


@pytest.mark.asyncio
async def test_d_on_downloaded_local_model_still_queues(tmp_path, monkeypatch):
    """Existing behavior pinned: a downloaded local ollama model deletes as
    before (reconcile overlay reports a size)."""
    stub = _seed_cloud_family(tmp_path, monkeypatch, location=None)
    stub.size_of.return_value = 22 * 1024**3  # simulate a real local blob
    stub.is_downloaded.return_value = True  # and `ollama show` succeeds

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


@pytest.mark.asyncio
async def test_not_ready_model_does_not_show_stale_disk_path(tmp_path, monkeypatch):
    """When reconcile reports a model as not on disk, the PATH column must
    show '—' even if modelman.toml still stores an old disk_path."""
    from unittest.mock import MagicMock

    from modelman.app import ModelmanApp

    _seed_cloud_family(tmp_path, monkeypatch, location=None)

    from modelman.providers import registry as provider_registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = None
    stub.is_downloaded.return_value = False
    monkeypatch.setattr(
        provider_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub)
    )

    # Pre-seed state with a stale disk_path.
    from modelman.state import ModelState, load_state, save_state

    state_path = tmp_path / "modelman.toml"
    state = load_state(state_path)
    state.set("ollama/glm-5.2:cloud", ModelState(ready=True, disk_path="/stale/path"))
    save_state(state, state_path)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.pause()

        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)
        mt = app.screen.query_one("#model-table", DataTable)
        row = mt.get_row("ollama/glm-5.2:cloud")
        # Column order: family, provider, name, location, status, exposed, size, path
        assert row[7] == "—", f"expected '—' but got {row[7]!r}"
