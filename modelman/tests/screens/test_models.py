"""Tests for ModelScreen helpers and per-key actions."""

import pytest
from textual.widgets import DataTable, Input, Select, Static

from modelman.app import ModelmanApp
from modelman.registry import (
    AuthConfig,
    Cost,
    FamilyEntry,
    ModelEntry,
    ProviderEntry,
    Registry,
    load_registry,
    save_registry,
)
from modelman.screens.models import (
    _format_location,
    _format_per_token,
    _format_price,
    _format_subscription,
    _model_entry_to_variant,
    _variant_to_model_entry,
)
from modelman.state import ModelState, StateStore, load_state, save_state


def _seed_registry_and_state(tmp_path, monkeypatch, *, models=()):
    """Seed registry.toml/modelman.toml in tmp_path and redirect the
    env vars. Mirrors the helper in tests/screens/test_app_navigation.py."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        families=[FamilyEntry(name="ornith")],
        models=list(models),
    )
    save_registry(reg, reg_path)
    save_state(StateStore(), state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    return reg_path, state_path


def test_variant_to_model_entry_sets_source_curated():
    """Models added through the TUI dialog are marked as curated so they are
    distinguished from sync-discovered models."""
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))]
    )
    variant = {"id": "ollama/x", "provider": "ollama", "name": "x"}
    entry = _variant_to_model_entry(variant, family="x", registry=registry)
    assert entry.source == "curated"


def test_variant_to_model_entry_passes_through_cost():
    """The adapter must carry cost from the dialog result into the registry
    ModelEntry, accepting either a Cost object or a plain dict."""
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))]
    )
    variant = {
        "id": "ollama/glm-5.3:cloud",
        "provider": "ollama",
        "name": "glm-5.3:cloud",
        "cost": Cost(subscription_price=20.0, subscription_period="month"),
    }
    entry = _variant_to_model_entry(variant, family="glm", registry=registry)
    assert entry.cost == Cost(subscription_price=20.0, subscription_period="month")


def test_variant_to_model_entry_defaults_cost_to_none():
    """When the dialog result omits cost, the ModelEntry must keep it as
    None rather than inventing a default."""
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))]
    )
    variant = {"id": "ollama/x", "provider": "ollama", "name": "x"}
    entry = _variant_to_model_entry(variant, family="ornith", registry=registry)
    assert entry.cost is None


def test_model_entry_to_variant_carries_cost():
    """The provider-facing VariantSpec must carry cost as a plain dict, not
    the registry Cost dataclass, so providers can JSON-serialize it."""
    entry = ModelEntry(
        id="ollama/glm-5.3:cloud",
        family="glm",
        provider_id="ollama",
        model_name="glm-5.3:cloud",
        location="cloud",
        cost=Cost(subscription_price=20.0, subscription_period="month"),
    )
    spec = _model_entry_to_variant(entry)
    assert spec["cost"] == {
        "subscription_price": 20.0,
        "subscription_period": "month",
    }


# ---------------------------------------------------------------------------
# Location formatter (LOC column)
# ---------------------------------------------------------------------------


def test_format_location_cloud():
    assert _format_location("cloud") == "↗"


def test_format_location_local():
    assert _format_location("local") == "▤"


def test_format_location_none():
    """A model with no location must not be misreported as local; the LOC
    column should show an em dash so the user knows the value is unknown."""
    assert _format_location(None) == "—"


def test_format_location_empty_string():
    """An empty location string is also unknown; it must not fall back to the
    local icon."""
    assert _format_location("") == "—"


def test_format_location_unexpected_value():
    """Unexpected location values are surfaced as raw text rather than forced
    into a binary cloud/local icon, making bad data visible."""
    assert _format_location("unknown") == "unknown"


# ---------------------------------------------------------------------------
# Cost / subscription formatters (COST and SUB table columns)
# ---------------------------------------------------------------------------


def test_format_price_none():
    assert _format_price(None) == "-"


def test_format_price_whole():
    assert _format_price(2.0) == "$2.00"


def test_format_price_fraction():
    assert _format_price(2.5) == "$2.50"


def test_format_price_fractional_cents():
    """Prices below a cent are preserved, not rounded to two decimals."""
    assert _format_price(0.0025) == "$0.0025"


def test_format_price_strips_trailing_zeros_beyond_two_decimals():
    assert _format_price(1.2300) == "$1.23"
    assert _format_price(1.200) == "$1.20"


def test_format_per_token_all_fields():
    c = Cost(
        input_price_per_million=2.0,
        cache_price_per_million=1.0,
        output_price_per_million=3.0,
    )
    assert _format_per_token(c) == "$2.00/1.00/3.00"


def test_format_per_token_partial():
    c = Cost(input_price_per_million=2.0, output_price_per_million=3.0)
    assert _format_per_token(c) == "$2.00/-/3.00"


def test_format_per_token_none():
    assert _format_per_token(None) == "-"
    assert _format_per_token(Cost()) == "-"


def test_format_subscription_month():
    c = Cost(subscription_price=20.0, subscription_period="month")
    assert _format_subscription(c) == "$20.00/mo"


def test_format_subscription_year():
    c = Cost(subscription_price=200.0, subscription_period="year")
    assert _format_subscription(c) == "$200.00/yr"


def test_format_subscription_none():
    assert _format_subscription(None) == "-"
    assert _format_subscription(Cost()) == "-"


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
async def test_d_on_local_not_downloaded_still_queues_delete(tmp_path, monkeypatch):
    """A local model that is not on disk can still be deleted: the
    apply step skips the on-disk removal (artifact already absent) but
    still updates registry/state. This makes the queue symmetric with
    cloud models — the not-ready gate that used to block this is
    removed (queue-only operations don't need a present artifact)."""
    _seed_cloud_family(tmp_path, monkeypatch, location=None)

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
    """When reconcile reports a model as not on disk, the details panel must
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
        details = app.screen.query_one("#details-panel", Static)
        rendered = str(details.render())
        # The details panel must show '—', not the stale disk_path.
        assert "/stale/path" not in rendered
        assert rendered.strip() == "path: —", f"expected 'path: —' but got {rendered!r}"


# ---------------------------------------------------------------------------
# Cursor preservation across reload
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_model_screen_cursor_restored_after_reload(tmp_path, monkeypatch):
    from unittest.mock import MagicMock

    from textual.widgets import DataTable

    from modelman.app import ModelmanApp
    from modelman.providers import registry as prov_registry

    a = ModelEntry(id="ollama/a", family="ornith", provider_id="ollama", model_name="a")
    b = ModelEntry(id="ollama/b", family="ornith", provider_id="ollama", model_name="b")
    _seed_registry_and_state(tmp_path, monkeypatch, models=[a, b])

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = None
    stub.is_downloaded.return_value = False
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

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


# ---------------------------------------------------------------------------
# Provider list is alphabetically sorted
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_provider_list_sorted(tmp_path, monkeypatch):
    from modelman.registry import (
        AuthConfig,
        FamilyEntry,
        ProviderEntry,
        Registry,
        save_registry,
    )
    from modelman.screens.models import ModelScreen
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


# ---------------------------------------------------------------------------
# Delete gating is removed: any model can be queued for delete
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_delete_any_model_even_not_ready(tmp_path, monkeypatch):
    """Native / not-ready models must be deletable; the not-ready gate is
    removed (apply() handles the absent-artifact case)."""
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
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        assert "ollama/o35" in app.screen.queued_deletes


# ---------------------------------------------------------------------------
# Table layout: 9 columns (COST + SUB) + details panel with the disk path
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_model_screen_columns_and_details_panel(tmp_path, monkeypatch):
    """Table shows FAMILY/PROVIDER/MODEL/LOC/STATUS/EXPOSED/COST/SUB/SIZE
    (no PATH column) and a details-panel Static exists below."""
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry

    model = ModelEntry(
        id="ollama/ornith-1.5:35b",
        family="ornith",
        provider_id="ollama",
        model_name="ornith-1.5:35b",
        location="local",
        cost=Cost(subscription_price=20.0, subscription_period="month"),
    )
    _reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[model])
    state = load_state(state_path)
    state.set("ollama/ornith-1.5:35b", ModelState(ready=True, disk_path="/tmp/ornith"))
    save_state(state, state_path)

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = 22 * 1024**3
    stub.list_local.return_value = []  # reconcile finds no path; panel falls back to state
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")  # open the only family's ModelScreen
        await pilot.pause()
        await pilot.pause()  # let the reconcile worker settle

        mt = app.screen.query_one("#model-table", DataTable)
        labels = [mt.columns[col].label.plain for col in mt.columns]
        assert labels == [
            "FAMILY",
            "PROVIDER",
            "MODEL",
            "LOC",
            "STATUS",
            "EXPOSED",
            "COST",
            "SUB",
            "SIZE",
        ]
        assert "PATH" not in labels

        details = app.screen.query_one("#details-panel", Static)
        assert "/tmp/ornith" in str(details.render())

        row0 = [str(c) for c in mt.get_row_at(0)]
        assert "▤" in row0  # local icon
        assert "$20.00/mo" in row0  # SUB column
        assert "-" in row0  # COST column (no per-token prices)
        assert "Y" not in row0 and "–" in row0  # exposed off renders as "–"


@pytest.mark.asyncio
async def test_model_screen_renders_per_token_and_subscription_pricing(tmp_path, monkeypatch):
    """The COST column renders per-token prices and the SUB column renders
    subscription prices using the new flat Cost fields."""
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry

    per_token = ModelEntry(
        id="ollama/per-token",
        family="ornith",
        provider_id="ollama",
        model_name="per-token",
        location="cloud",
        cost=Cost(
            input_price_per_million=1.0,
            cache_price_per_million=0.5,
            output_price_per_million=2.0,
        ),
    )
    subscription = ModelEntry(
        id="ollama/subscription",
        family="ornith",
        provider_id="ollama",
        model_name="subscription",
        location="cloud",
        cost=Cost(subscription_price=20.0, subscription_period="month"),
    )
    _reg_path, state_path = _seed_registry_and_state(
        tmp_path, monkeypatch, models=[per_token, subscription]
    )
    save_state(load_state(state_path), state_path)

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = False
    stub.size_of.return_value = None
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")  # open the only family's ModelScreen
        await pilot.pause()

        mt = app.screen.query_one("#model-table", DataTable)
        labels = [mt.columns[col].label.plain for col in mt.columns]
        assert labels == [
            "FAMILY",
            "PROVIDER",
            "MODEL",
            "LOC",
            "STATUS",
            "EXPOSED",
            "COST",
            "SUB",
            "SIZE",
        ]

        rows = [[str(c) for c in mt.get_row_at(r)] for r in range(mt.row_count)]
        per_token_row = next(r for r in rows if "per-token" in r)
        subscription_row = next(r for r in rows if "subscription" in r)

        assert per_token_row[6] == "$1.00/0.50/2.00"
        assert per_token_row[7] == "-"
        assert subscription_row[6] == "-"
        assert subscription_row[7] == "$20.00/mo"


@pytest.mark.asyncio
async def test_details_panel_updates_on_cursor_move(tmp_path, monkeypatch):
    """Cursor move updates the details panel with the new row's path."""
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry

    models = [
        ModelEntry(
            id="ollama/a",
            family="ornith",
            provider_id="ollama",
            model_name="a",
            location="local",
        ),
        ModelEntry(
            id="ollama/b",
            family="ornith",
            provider_id="ollama",
            model_name="b",
            location="local",
        ),
    ]
    _reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=models)
    state = load_state(state_path)
    state.set("ollama/a", ModelState(ready=True, disk_path="/data/a"))
    state.set("ollama/b", ModelState(ready=True, disk_path="/data/b"))
    save_state(state, state_path)

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = 1024
    stub.list_local.return_value = []
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")  # open the only family's ModelScreen
        await pilot.pause()
        await pilot.pause()  # let the reconcile worker settle

        details = app.screen.query_one("#details-panel", Static)
        assert "/data/a" in str(details.render())

        mt = app.screen.query_one("#model-table", DataTable)
        mt.move_cursor(row=1)
        await pilot.pause()
        assert "/data/b" in str(app.screen.query_one("#details-panel", Static).render())


# ---------------------------------------------------------------------------
# Discard reverts registry file (same-family edits save immediately)
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_discard_reverts_immediately_saved_registry_edit(tmp_path, monkeypatch):
    """Same-family edits call save_registry immediately. If the user then
    queues another action and chooses Discard, the registry file must be
    reverted alongside the in-memory snapshot."""
    from unittest.mock import MagicMock

    from modelman.app import ModelmanApp
    from modelman.providers import registry as prov_registry

    entry = ModelEntry(
        id="ollama/glm-5.3:cloud",
        family="glm",
        provider_id="ollama",
        model_name="glm-5.3:cloud",
        location="cloud",
    )
    reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[entry])

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = None
    stub.is_downloaded.return_value = False
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")  # open ModelScreen
        await pilot.pause()

        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)

        # Edit the model: open ModelForm, change location, submit.
        await pilot.press("e")
        await pilot.pause()
        app.screen.query_one("#location-select", Select).value = "local"
        await pilot.pause()
        app.screen.query_one("#model", Input).focus()
        await pilot.press("enter")
        await pilot.pause()

        # The edit saved to disk immediately; confirm the file has the new location.
        reg_after_edit = load_registry(reg_path)
        assert reg_after_edit.model("ollama/glm-5.3:cloud").location == "local"

        # Queue another action so the exit dialog offers Discard.
        await pilot.press("r")
        await pilot.pause()
        assert app.screen.queued_ready

        # Escape opens ConfirmExitDialog; 'd' chooses Discard.
        await pilot.press("escape")
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()

    # After discarding, the registry file must be reverted to the original location.
    reg_after_discard = load_registry(reg_path)
    assert reg_after_discard.model("ollama/glm-5.3:cloud").location == "cloud"
