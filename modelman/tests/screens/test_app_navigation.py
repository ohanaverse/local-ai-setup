import threading

import pytest
from textual.widgets import Button, Checkbox, DataTable, Input, Label

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
from modelman.state import ModelState, StateStore, save_state

from .test_forms import _submit


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
async def test_app_launches_into_model_screen():
    """ModelScreen is the app's only/root screen — FamilyScreen was
    removed (family-list/rename/delete UI dropped)."""
    from modelman.screens.models import ModelScreen

    app = ModelmanApp()
    async with app.run_test():
        assert isinstance(app.screen, ModelScreen)


@pytest.mark.asyncio
async def test_direct_download_syncs_agent_providers(tmp_path, monkeypatch):
    """`ModelmanApp` must call sync_agent_providers on mount so the
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

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        assert isinstance(app.screen, ModelScreen)
        # Native provider synced from wt config should be
        # present in the in-memory registry passed to ModelScreen.
        assert app.screen.registry.provider("claude").auth.type == "native"
        # And it should be in the provider list used for the add-model
        # form's provider dropdown.
        assert "claude" in app.screen._provider_list()


@pytest.mark.asyncio
async def test_toggle_ready_queues_variant(tmp_path, monkeypatch):
    """Pressing `r` on a not-ready row queues a ready-on (every provider
    queues now, mapped or not)."""
    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
    _seed_registry_and_state(tmp_path, monkeypatch, models=[o35])

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("r")
        await pilot.pause()
        assert app.screen.queued_ready.get("ollama/o35") is True


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

        # Initial: dl ✓, missing ○
        mt = app.screen.query_one("#model-table", DataTable)
        rows = {r[2]: r for r in [mt.get_row_at(i) for i in range(mt.row_count)]}
        assert "✓" in rows["dl"][4]
        assert "○" in rows["missing"][4]

        # Toggle ready on missing → ↓ (a queued ready-on — nothing is ever
        # mid-download while the TUI is open — and the glyph reflects it).
        mt.cursor_coordinate = (1, 0)
        await pilot.press("r")
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
        await pilot.pause()
        await pilot.press("d")
        await pilot.pause()
        assert "ollama/o35" in app.screen.queued_deletes
        # The add now always queues its ready-on — nothing to stub.
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

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        from textual.widgets import DataTable

        mt = app.screen.query_one("#model-table", DataTable)
        row = mt.get_row_at(0)
        assert row[4] == "[green]✓[/green]"  # status
        assert row[7] == "22.0 GB"  # size


@pytest.mark.asyncio
async def test_reconcile_self_heal_clears_running_for_dead_model(tmp_path, monkeypatch):
    """A model left flagged running from a prior session, whose process
    died externally before the TUI reopens, must show as NOT running after
    the on-mount reconcile self-heals it — in the table and in the
    in-memory self.state used to render it, not just on disk."""
    from unittest.mock import MagicMock

    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
    reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[o35])

    store = StateStore()
    store.set("ollama/o35", ModelState(ready=True, running=True))
    save_state(store, state_path)

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = 1024
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))
    # The probe finds nothing actually serving — the process died outside
    # modelman's knowledge (crash, manual kill).
    monkeypatch.setattr("modelman.local_control._probe_running", lambda *a, **k: False)

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()

        mt = app.screen.query_one("#model-table", DataTable)
        row = mt.get_row_at(0)
        assert row[5] == "-"  # RUNNING column: "-", not "●"
        assert app.screen.state.get("ollama/o35").running is False

    from modelman.state import load_state as _load_state

    # And the disk-side flag (already written by _clear_stale_running_flag)
    # agrees.
    assert _load_state(state_path).get("ollama/o35").running is False


@pytest.mark.asyncio
async def test_reconcile_keeps_a_live_discovered_models_flag_and_clears_a_dead_one(
    tmp_path, monkeypatch
):
    """#179 Phase B: a model started without a registry entry is flagged
    under its discovered id only. The mount reconcile clears every running
    flag running_model_ids() does not return, so an unregistered id that was
    skipped instead of probed would lose its flag on every TUI open while
    still serving. A live one must survive (in memory and on disk); a dead
    one is cleared like any registered model's."""
    from unittest.mock import MagicMock

    _reg_path, state_path = _seed_registry_and_state(
        tmp_path, monkeypatch, providers=("ollama", "omlx")
    )
    store = StateStore()
    store.set("omlx/live-4bit", ModelState(running=True))
    store.set("omlx/gone-4bit", ModelState(running=True))
    save_state(store, state_path)

    from modelman.providers import registry

    stub = MagicMock()
    stub.list_local.return_value = []
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))
    monkeypatch.setattr(
        "modelman.local_control._probe_running",
        lambda provider_id, model_name, base: model_name == "live-4bit",
    )

    from modelman.app import ModelmanApp
    from modelman.state import load_state as _load_state

    app = ModelmanApp()
    async with app.run_test() as pilot:
        for _ in range(50):
            await pilot.pause()
            if not _load_state(state_path).get("omlx/gone-4bit").running:
                break
        await pilot.pause()
        assert app.screen.state.get("omlx/gone-4bit").running is False
        assert app.screen.state.get("omlx/live-4bit").running is True

    on_disk = _load_state(state_path)
    assert on_disk.get("omlx/gone-4bit").running is False
    assert on_disk.get("omlx/live-4bit").running is True


@pytest.mark.asyncio
async def test_reconcile_self_heal_syncs_routes_for_the_dead_model(tmp_path, monkeypatch, wt_calls):
    """Clearing a stale running flag must reach LiteLLM in the same breath:
    the route it leaves behind points at a dead backend, and nothing else
    notices — the flag lives in modelman.toml, which the TUI-exit sync (gated
    on registry.toml) reads past, and running_model_ids() is deliberately
    wt-free. Without this, one `wt litellm sync` from any later command was the
    only thing that could drop the stale route."""
    from unittest.mock import MagicMock

    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
    _reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[o35])

    store = StateStore()
    store.set("ollama/o35", ModelState(ready=True, running=True))
    save_state(store, state_path)

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = 1024
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))
    monkeypatch.setattr("modelman.local_control._probe_running", lambda *a, **k: False)

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        for _ in range(50):
            if any(call[:1] == ["sync"] for call in wt_calls):
                break
            await pilot.pause()

    assert any(call[:1] == ["sync"] for call in wt_calls), (
        f"no route sync after clearing a stale running flag: {wt_calls}"
    )


@pytest.mark.asyncio
async def test_reconcile_stops_an_ollama_model_that_is_gone(tmp_path, monkeypatch, wt_calls):
    """#233 on the screen: an ollama model flagged running whose artifact is
    gone kept its RUNNING ● — the mount reconcile clears only what the probe
    does not verify, and ollama's probe verifies everything. The flag must be
    cleared in the screen's state and in modelman.toml, with the route sync a
    cleared flag always brings."""
    from unittest.mock import MagicMock

    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
    _reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[o35])

    store = StateStore()
    store.set("ollama/o35", ModelState(ready=True, running=True))
    save_state(store, state_path)

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = False
    stub.list_local.return_value = []
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))
    # The daemon answers, and the model is not among what is pulled (#242).
    monkeypatch.setattr(
        "modelman.local_control._http_json", lambda url, timeout=2.0: {"models": []}
    )

    from modelman.app import ModelmanApp
    from modelman.state import load_state as _load_state

    app = ModelmanApp()
    async with app.run_test() as pilot:
        for _ in range(50):
            await pilot.pause()
            if any(call[:1] == ["sync"] for call in wt_calls):
                break
        assert app.screen.state.get("ollama/o35").running is False

    assert _load_state(state_path).get("ollama/o35").running is False
    assert any(call[:1] == ["sync"] for call in wt_calls), wt_calls


@pytest.mark.asyncio
async def test_reconcile_with_nothing_stale_runs_no_sync(tmp_path, monkeypatch, wt_calls):
    """The reconcile runs on every mount, so the sync above must be gated on
    actually having found a dead model: a healthy mount must not bounce the
    LiteLLM proxy."""
    from unittest.mock import MagicMock

    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
    _reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[o35])

    store = StateStore()
    store.set("ollama/o35", ModelState(ready=True, running=False))
    save_state(store, state_path)

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = 1024
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.pause()

    assert not any(call[:1] == ["sync"] for call in wt_calls), (
        f"a mount with no stale flag still synced: {wt_calls}"
    )


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

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        # No queue, so escape pops without dialog. State must stay untouched.
        await pilot.press("escape")
        await pilot.pause()

    from modelman.state import load_state

    assert not load_state(state_path).get("ollama/o35").ready


@pytest.mark.asyncio
async def test_apply_preserves_other_models_state_rows(tmp_path, monkeypatch):
    """Applying a queued change for one model must preserve other models'
    on-disk state rows — apply()'s final save merges only the touched ids
    onto a fresh on-disk store (merge-on-save), so unrelated rows are
    neither lost nor reverted to a stale snapshot. (The pre-async-downloads
    version of this test asserted the old wholesale-save behavior where
    reconciled-but-untouched entries were rewritten; those entries are now
    re-derived by reconcile on the next mount instead.)"""
    from unittest.mock import MagicMock

    from textual.widgets import DataTable

    from modelman.main import run_queued_ops
    from modelman.queue import QueuedOps

    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
    q8 = ModelEntry(id="ollama/q8", family="ornith", provider_id="ollama", model_name="ornith:8b")
    _reg_path, state_path = _seed_registry_and_state(
        tmp_path,
        monkeypatch,
        models=[o35, q8],
        downloaded={"ollama/o35": "/fake/o35", "ollama/q8": "/fake/q8"},
    )

    from modelman.providers import registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = True
    stub.path_of.side_effect = lambda v: f"/fake/{v['id']}"
    stub.artifact_paths.side_effect = lambda v: frozenset([stub.path_of(v)])
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.pause()  # let reconcile settle
        # Queue a delete for q8 so escape triggers the apply dialog.
        mt = app.screen.query_one("#model-table", DataTable)
        mt.cursor_coordinate = (1, 0)
        await pilot.press("d")
        await pilot.pause()
        await pilot.press("escape")
        await pilot.pause()
        await pilot.press("y")
        await pilot.pause()
        assert isinstance(app.return_value, QueuedOps)
        queued = app.return_value

    # App has exited; apply the queue the same way main.py's run_tui() does.
    failed = run_queued_ops(queued)
    assert failed is False

    from modelman.state import load_state

    loaded = load_state(state_path)
    assert loaded.get("ollama/o35").ready is True  # untouched row preserved
    assert "ollama/q8" not in loaded.models  # the delete landed


@pytest.mark.asyncio
async def test_escape_with_pending_shows_dialog_and_apply(tmp_path, monkeypatch):
    """Escape with a queued change shows the confirm dialog; Apply exits
    the app with a QueuedOps, and running it against fresh on-disk state
    (main.run_queued_ops, the same call run_tui() makes) persists the
    flip. Uses a cloud-provider model (no on-disk artifact, no backing
    Provider class) for the ready-on so this exercises the flag-only
    flip path end to end, from TUI keypress through to the on-disk
    state file."""

    or35 = ModelEntry(
        id="openrouter/o35",
        family="ornith",
        provider_id="openrouter",
        model_name="anthropic/claude-opus",
    )
    _reg_path, state_path = _seed_registry_and_state(
        tmp_path,
        monkeypatch,
        models=[or35],
        providers=("openrouter",),
    )
    # The seed helper writes ProviderEntry(auth=none) without a location;
    # give openrouter a cloud location so model_has_local_artifact is False
    # and the ready-on stays a queued flag flip.
    from modelman.registry import ProviderEntry, load_registry, save_registry

    reg = load_registry(_reg_path)
    reg.providers[0] = ProviderEntry(
        id="openrouter", name="OpenRouter", auth=AuthConfig(type="none"), location="cloud"
    )
    save_registry(reg, _reg_path)

    from modelman.app import ModelmanApp
    from modelman.main import run_queued_ops
    from modelman.queue import QueuedOps

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.pause()
        await pilot.press("r")  # queue a ready flip (flag-only provider)
        await pilot.pause()
        await pilot.press("escape")
        await pilot.pause()
        await pilot.press("y")
        await pilot.pause()
        assert isinstance(app.return_value, QueuedOps)
        queued = app.return_value

    # App has exited; apply the queue the same way main.py's run_tui() does.
    failed = run_queued_ops(queued)
    assert failed is False

    from modelman.state import load_state

    assert load_state(state_path).get("openrouter/o35").ready is True


@pytest.mark.asyncio
async def test_discard_pending_exits_without_applying(tmp_path, monkeypatch):
    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
    _reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[o35])

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.pause()
        await pilot.press("r")
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
        # Discard now exits the app too (ModelScreen was the app's only
        # screen; there's nothing left to pop back to).
        assert app.return_value is None
        # Only a queued toggle was discarded: registry.toml was never written
        # this session, so there is no route to put right and no sync is owed.
        assert app.route_sync_owed is False

    from modelman.state import load_state

    # State on disk should be unchanged: no downloaded entry for o35.
    assert not load_state(state_path).get("ollama/o35").ready


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
    `family_entries` seeds first-class Registry.families entries. `family`
    is unused by ModelScreen itself (it isn't family-scoped) but is kept
    here since several callers pass it purely to label their seeded
    ModelEntry — see each call site's own `family=` on the entry.
    Returns (ms, registry_path, state_path)."""
    del family
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
        registry_path=reg_path,
        state_path=state_path,
    )
    return ms, reg_path, state_path


@pytest.mark.asyncio
async def test_model_screen_add_form_offers_all_providers_for_empty_family(
    tmp_path,
    monkeypatch,
):
    """The AddModel form's provider Label must reflect the full
    configured-provider list when the user presses 'a' with an empty
    registry (no models yet to derive providers from)."""
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
        registry_path=reg_path,
        state_path=state_path,
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
    registry.models with the adapter's translation, persisted to disk
    immediately (the add queues a ready-on, but _on_add_model still
    persists the registry entry immediately regardless)."""
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
        # No models (so no cursor context) and no known families: the Add
        # dialog's family Select defaults to "+ New family...", so a real
        # name must be typed before the form will submit.
        app.screen.query_one("#new-family-input", Input).focus()
        for ch in "ornith":
            await pilot.press(ch)
        app.screen.query_one("#model", Input).focus()
        for ch in "ornith:8b":
            await pilot.press(ch)
        await pilot.press("enter")
        await pilot.pause()

    ids = [m.id for m in ms.registry.models]
    assert "ollama/ornith:8b" in ids
    added = next(m for m in ms.registry.models if m.id == "ollama/ornith:8b")
    assert added.family == "ornith"
    assert added.provider_id == "ollama"
    assert added.model_name == "ornith:8b"
    assert added.fetch is None
    # The add persists immediately now (mirrors edits): the post-exit
    # runner looks up queued model ids against a freshly-loaded-from-disk
    # registry, so an unpersisted add would be treated as unknown at
    # apply time — persisting now avoids that entirely.
    reloaded = load_registry(reg_path)
    assert "ollama/ornith:8b" in [m.id for m in reloaded.models]


@pytest.mark.asyncio
async def test_model_screen_toggle_ready_queues_variant(
    tmp_path,
    monkeypatch,
):
    """Pressing `r` on a not-ready row queues a ready-on (every provider
    queues now, mapped or not)."""
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
        await pilot.press("r")
        await pilot.pause()
        assert ms.queued_ready.get("ollama/o35") is True


@pytest.mark.asyncio
async def test_model_screen_discard_restores_fetch_dataclass(tmp_path, monkeypatch):
    """Discarding pending changes must restore the snapshot without
    turning nested Fetch dataclasses into plain dicts."""
    from unittest.mock import MagicMock

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
        await pilot.press("r")
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
        assert app.return_value is None

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


def test_model_entry_to_variant_preserves_location():
    """Editing a cloud-located ollama model must pass its location into
    the ModelForm so the Location select doesn't default back to local."""
    from modelman.registry import model_entry_to_variant

    entry = ModelEntry(
        id="ollama/glm-5.2:cloud",
        family="glm",
        provider_id="ollama",
        model_name="glm-5.2:cloud",
        location="cloud",
    )
    variant = model_entry_to_variant(entry)
    assert variant.get("location") == "cloud"


def test_round_trip_preserves_quantizations():
    """`_variant_to_model_entry` followed by `model_entry_to_variant`
    must preserve every field, including `quantizations`. Catches the
    silent round-trip loss documented in the PR 2 final review
    (Important #1: `model_entry_to_variant` used to emit
    `quantizations: None` regardless of the entry's stored Fetch)."""
    from modelman.registry import model_entry_to_variant
    from modelman.screens.models import _variant_to_model_entry

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

    roundtripped = model_entry_to_variant(entry)
    assert roundtripped["quantizations"] == quants
    assert roundtripped["repo"] == "o/r"
    assert roundtripped["files"] == ["x.gguf"]


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
        await _submit(app, pilot)

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
        await _submit(app, pilot)

    assert ms.queued_moves == {}


@pytest.mark.asyncio
async def test_edit_model_cost_change_persists_to_registry_on_back(tmp_path, monkeypatch):
    """Editing a model's cost (same family — empty queue) and returning
    to the family screen must persist the change to registry.toml.

    Regression: _on_edit_model mutated only the in-memory registry and
    queued nothing; FamilyScreen's on-screen-resume then reloaded the
    registry from disk, silently dropping the cost edit.
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

        # Edit the cost (location stays editable for corrections).
        app.screen.query_one("#subscription-checkbox", Checkbox).value = True
        await pilot.pause()
        app.screen.query_one("#subscription-price", Input).value = "20"
        await _submit(app, pilot)

        assert ms.registry.models[0].cost == Cost(
            subscription_price=20.0, subscription_period="month"
        )
        # Nothing else queued: escape pops straight back to the previous
        # screen (this is the path that used to drop the edit).
        await pilot.press("escape")
        await pilot.pause()

    reloaded = load_registry(reg_path)
    assert reloaded.model("ollama/gemma4:26b-mlx").cost == Cost(
        subscription_price=20.0, subscription_period="month"
    )


@pytest.mark.asyncio
async def test_edit_survives_app_relaunch(tmp_path, monkeypatch):
    """End-to-end user journey: open the model list -> edit a model's
    cost -> the edit is visible immediately (no navigation needed, since
    ModelScreen is the only screen) and survives a fresh app relaunch
    (proving it was actually saved to disk, not just held in memory)."""
    _reg_path, _state_path = _seed_registry_and_state(
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
    from modelman.screens.forms import ModelForm

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        mt = app.screen.query_one("#model-table", DataTable)
        mt.focus()
        mt.cursor_coordinate = (0, 0)
        await pilot.press("e")
        await pilot.pause()
        assert isinstance(app.screen, ModelForm)

        # Edit the cost (location stays editable for corrections).
        app.screen.query_one("#per-token-checkbox", Checkbox).value = True
        await pilot.pause()
        app.screen.query_one("#input-price", Input).value = "20"
        await _submit(app, pilot)
        await pilot.pause()

        # Visible immediately in the same (only) screen's table.
        mt = app.screen.query_one("#model-table", DataTable)
        rows = [mt.get_row_at(i) for i in range(mt.row_count)]
        assert rows[0][6] == "20.0000 ------- -------"

    # And it was actually persisted, not just held in this session's
    # in-memory registry: a fresh app relaunch sees it too.
    app2 = ModelmanApp()
    async with app2.run_test() as pilot:
        await pilot.pause()
        mt = app2.screen.query_one("#model-table", DataTable)
        rows = [mt.get_row_at(i) for i in range(mt.row_count)]
        assert rows, "model must still be listed after relaunch"
        assert rows[0][6] == "20.0000 ------- -------"


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
    from modelman.main import run_queued_ops
    from modelman.queue import QueuedOps
    from modelman.registry import load_registry

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
        await pilot.press("y")
        await pilot.pause()
        assert isinstance(app.return_value, QueuedOps)
        queued = app.return_value

    # App has exited; apply the queue the same way main.py's run_tui() does.
    failed = run_queued_ops(queued)
    assert failed is False

    reloaded = load_registry(reg_path)
    assert reloaded.model("ollama/gemma4:26b-mlx").family == "gemma4"


@pytest.mark.asyncio
async def test_apply_move_emptying_family_keeps_it_visible(tmp_path, monkeypatch):
    """After an apply that moves the last model out of a family, the
    emptied family must still be selectable (a first-class [[families]]
    entry survives) even though it's no longer visible in the model list
    itself — there's no separate family-list screen to show it on."""
    entry = ModelEntry(
        id="ollama/gemma4:26b-mlx",
        family="gemma4:26b-mlx",
        provider_id="ollama",
        model_name="gemma4:26b-mlx",
    )
    ms, reg_path, _state = _make_screen(
        tmp_path,
        monkeypatch,
        entries=[entry],
        families=["gemma4"],
    )

    from modelman.app import ModelmanApp
    from modelman.main import run_queued_ops
    from modelman.queue import QueuedOps
    from modelman.registry import load_registry

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        pilot.app.push_screen(ms)
        await pilot.pause()
        ms.queued_moves["ollama/gemma4:26b-mlx"] = "gemma4"
        await pilot.press("escape")
        await pilot.pause()
        await pilot.press("y")
        await pilot.pause()
        assert isinstance(app.return_value, QueuedOps)
        queued = app.return_value

    # App has exited; apply the queue the same way main.py's run_tui() does.
    failed = run_queued_ops(queued)
    assert failed is False

    reloaded = load_registry(reg_path)
    # The moved model no longer shows under its old family, even though
    # that family name is still selectable (asserted below).
    assert not any(m.family == "gemma4:26b-mlx" for m in reloaded.models)
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
        # Discard now exits the app too (ModelScreen was the app's only
        # screen; there's nothing left to pop back to).
        assert app.return_value is None

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
        # Out-of-family add (exact _on_add_model path). _on_add_model
        # queues a ready-on unconditionally now.
        ms._on_add_model(
            ModelFormResult(
                spec={"id": "ollama/newcomer", "provider": "ollama", "name": "newcomer:1b"},
                family="mamba",
            )
        )
        await pilot.press("escape")
        await pilot.pause()
        await pilot.press("d")  # discard
        await pilot.pause()
        # Discard now exits the app too (ModelScreen was the app's only
        # screen; there's nothing left to pop back to).
        assert app.return_value is None

    assert [m.id for m in ms.registry.models] == ["ollama/mover"]
    assert ms.registry.models[0].family == "ornith"
    assert ms.queued_moves == {}
    assert ms.queued_ready == {}
    # State must not have gained any session entries.
    assert dict(ms.state.models) == {}


@pytest.mark.asyncio
async def test_discard_removes_out_of_family_added_model(tmp_path, monkeypatch):
    """A model added into a *different* family this session must still be
    removed by discard — the snapshot restore spans the whole registry,
    not just one family, so there's no scoping gap for it to slip through."""
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
        # _on_add_model already queues ready=True unconditionally, so
        # the queue is non-empty and Escape opens the exit dialog without
        # any extra queuing needed.
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
        # Discard now exits the app too (ModelScreen was the app's only
        # screen; there's nothing left to pop back to).
        assert app.return_value is None

    ids = [m.id for m in ms.registry.models]
    assert ids == ["ollama/keep"]
    # The add was persisted to disk on entry (Task 14); the discard's
    # restore-and-resave must undo that too.
    assert "ollama/moved" not in [m.id for m in load_registry(_reg_path).models]


@pytest.mark.asyncio
async def test_app_mounts_without_live_daemon_or_proxy_restart():
    """With the hermetic autouse fixtures active, a bare ModelmanApp can
    mount, run reconcile, and render the model table without touching
    the live ollama daemon or restarting the LiteLLM proxy.

    Regression guard for the two interference issues: without the
    fixtures, this would load the real registry and shell out to ollama
    list/show thousands of times; a LiteLLM route change would also
    run launchctl kickstart against the live proxy.
    """
    from textual.widgets import DataTable

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.pause()  # let the reconcile worker settle

        table = app.screen.query_one("#model-table", DataTable)
        assert table.row_count >= 0


@pytest.mark.asyncio
async def test_ctrl_q_exits_immediately_with_no_active_downloads(tmp_path, monkeypatch):
    # ctrl+q with an empty queue exits the app directly (no confirmation
    # dialog, nothing to guard against — there's no more background
    # download to block on).
    _seed_registry_and_state(tmp_path, monkeypatch)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("ctrl+q")
        await pilot.pause()
    assert app.return_code == 0 or not app.is_running


@pytest.mark.asyncio
async def test_ctrl_q_on_model_screen_confirms_pending_queue_instead_of_dropping_it(
    tmp_path, monkeypatch
):
    # Regression for a review finding: request_quit() only checked
    # DownloadManager.has_active(), so ctrl+q on ModelScreen with a
    # queued-but-unapplied change (e.g. a queued delete) and no active
    # download exited the app immediately, silently discarding the
    # queue instead of giving the same apply/discard/cancel confirm
    # Escape would. It must now route through ModelScreen.action_back().
    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
    reg_path, _state_path = _seed_registry_and_state(
        tmp_path, monkeypatch, models=[o35], downloaded={"ollama/o35": str(tmp_path / "d")}
    )

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.pause()
        await pilot.press("d")  # queue a delete; no download involved
        await pilot.pause()
        assert app.screen.queued_deletes

        await pilot.press("ctrl+q")
        await pilot.pause()

        # The app must still be running, showing the exit-confirm dialog
        # rather than having exited with the queued delete dropped.
        assert app.is_running
        from modelman.screens.forms import ConfirmExitDialog

        assert isinstance(app.screen, ConfirmExitDialog)


@pytest.mark.asyncio
async def test_escape_with_empty_queue_exits_app(tmp_path, monkeypatch):
    """Escape is ModelScreen's own binding for the same action_back()
    ctrl+q delegates to — an empty queue must exit immediately, no
    dialog, mirroring the ctrl+q case above."""
    _seed_registry_and_state(tmp_path, monkeypatch)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.press("escape")
        await pilot.pause()
        assert app.return_value is None


# ---------------------------------------------------------------------------
# Force-quit while a background worker (model start/stop) is in flight.
#
# `s` runs start_local_model on a Textual thread=True worker (asyncio's
# default, non-daemon ThreadPoolExecutor). Python threads aren't
# preemptible and Textual cannot cancel a running thread worker, so a plain
# quit while one is still polling for a model to come up would hang the
# process — potentially for minutes — in stdlib shutdown (asyncio.run()'s
# executor join, then concurrent.futures.thread's untimed atexit thread
# join) long after the TUI itself looks closed. These tests pin the guard
# that was added to ModelScreen.action_back() to catch that instead of
# silently freezing the shell. See docs/superpowers/specs/
# 2026-09-14-local-model-lifecycle-design.md for why abandoning an
# in-flight start is safe (the `running` flag self-heals via live probe).
# ---------------------------------------------------------------------------


def _start_local_model_blocking_on(event: threading.Event, release: threading.Event):
    """A start_local_model fake that signals `event` once called and then
    blocks on `release` (bounded by a timeout so a test bug can't hang the
    suite) — used to keep the model-start worker in the RUNNING state long
    enough for the test to press escape/ctrl+q while it's still live."""
    from modelman.local_control import StartResult

    def fake_start(registry, model_id, state_path=None, **kwargs):
        event.set()
        release.wait(timeout=5)
        return StartResult(model_id=model_id, already_running=False, other_running=[])

    return fake_start


@pytest.mark.asyncio
async def test_escape_while_model_start_running_shows_force_quit_dialog(tmp_path, monkeypatch):
    # Regression for the reported bug: quitting right after 's' must warn
    # instead of silently hanging the shell for as long as the (real, slow)
    # model warmup takes. The dialog must name the in-flight operation so
    # the freeze is explained, not mysterious.
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry
    from modelman.screens.forms import ConfirmForceQuitDialog

    model = ModelEntry(
        id="omlx/a", family="ornith", provider_id="omlx", model_name="a", location="local"
    )
    reg_path, state_path = _seed_registry_and_state(
        tmp_path, monkeypatch, models=[model], providers=("omlx",)
    )
    state = StateStore()
    state.set("omlx/a", ModelState(ready=True, running=False))
    save_state(state, state_path)

    stub = MagicMock()
    stub.name = "omlx"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = None
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    started = threading.Event()
    release = threading.Event()
    monkeypatch.setattr(
        "modelman.screens.models.start_local_model",
        _start_local_model_blocking_on(started, release),
    )

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.pause()  # let on-mount reconcile settle first
        await pilot.press("s")
        await pilot.pause()
        await pilot.press("y")
        assert started.wait(timeout=2), "start_local_model was never called"

        await pilot.press("escape")
        await pilot.pause()

        assert isinstance(app.screen, ConfirmForceQuitDialog)
        labels = " ".join(str(label.visual) for label in app.screen.query(Label))
        assert "Starting omlx/a" in labels

        # Let the worker finish so no thread is left running past the
        # `run_test()` context (real modelman would hang on this exact
        # thread at process exit — see the docstring above).
        release.set()
        for _ in range(50):
            await pilot.pause()
            if app.screen.__class__.__name__ != "ConfirmForceQuitDialog":
                break


@pytest.mark.asyncio
async def test_escape_after_workers_settle_still_exits_immediately(tmp_path, monkeypatch):
    # The new in-flight-worker guard must not fire once the on-mount
    # reconcile/price-refresh workers have finished — otherwise every quit
    # would show a spurious "still running" prompt, not just one that
    # follows a real in-flight start/stop.
    from modelman.screens.forms import ConfirmForceQuitDialog

    model = ModelEntry(
        id="ollama/a", family="ornith", provider_id="ollama", model_name="a", location="local"
    )
    _seed_registry_and_state(tmp_path, monkeypatch, models=[model])

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.pause()  # let on-mount reconcile (and price refresh) settle

        await pilot.press("escape")
        await pilot.pause()

        assert not isinstance(app.screen, ConfirmForceQuitDialog)
        assert app.return_value is None


@pytest.mark.asyncio
async def test_force_quit_restores_terminal_before_hard_exit(tmp_path, monkeypatch):
    # Force-quitting must restore the terminal (leave alt-screen/raw mode)
    # BEFORE hard-exiting via os._exit — otherwise the user's shell is left
    # in a broken visual state even though the process itself returns
    # instantly. os._exit is patched to record the call instead of actually
    # running it, since a real call here would kill the pytest process.
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry
    from modelman.screens.forms import ConfirmForceQuitDialog

    model = ModelEntry(
        id="omlx/a", family="ornith", provider_id="omlx", model_name="a", location="local"
    )
    reg_path, state_path = _seed_registry_and_state(
        tmp_path, monkeypatch, models=[model], providers=("omlx",)
    )
    state = StateStore()
    state.set("omlx/a", ModelState(ready=True, running=False))
    save_state(state, state_path)

    stub = MagicMock()
    stub.name = "omlx"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = None
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    started = threading.Event()
    release = threading.Event()
    monkeypatch.setattr(
        "modelman.screens.models.start_local_model",
        _start_local_model_blocking_on(started, release),
    )

    exit_calls: list[int] = []
    monkeypatch.setattr("modelman.screens.models.os._exit", exit_calls.append)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.pause()
        await pilot.press("s")
        await pilot.pause()
        await pilot.press("y")
        assert started.wait(timeout=2)

        # Spy on the real (headless test) driver's teardown methods rather
        # than replacing the driver outright — Textual keeps using it for
        # input/shutdown bookkeeping (is_inline, send_message, close, ...)
        # until the app actually exits, so a stand-in without those breaks
        # the test harness itself, not just this assertion.
        driver_calls: list[str] = []
        driver = app._driver
        assert driver is not None
        monkeypatch.setattr(driver, "disable_input", lambda: driver_calls.append("disable_input"))
        monkeypatch.setattr(
            driver, "stop_application_mode", lambda: driver_calls.append("stop_application_mode")
        )
        monkeypatch.setattr(driver, "flush", lambda: driver_calls.append("flush"))

        await pilot.press("escape")
        await pilot.pause()
        assert isinstance(app.screen, ConfirmForceQuitDialog)

        await pilot.press("f")
        await pilot.pause()

        assert driver_calls == ["disable_input", "stop_application_mode", "flush"]
        assert exit_calls == [0]

        release.set()
        for _ in range(50):
            await pilot.pause()


@pytest.mark.asyncio
async def test_ctrl_q_while_force_quit_dialog_open_force_quits(tmp_path, monkeypatch):
    # Regression: ModelmanApp.request_quit() used to only special-case
    # ModelScreen, so pressing ctrl+q a second time while the force-quit
    # dialog was already open (self.screen is the dialog, not ModelScreen)
    # fell through to a plain self.exit() — silently reintroducing the
    # multi-minute hang this whole dialog exists to prevent. A second
    # ctrl+q here must behave like clicking "Force quit", not bypass it.
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry
    from modelman.screens.forms import ConfirmForceQuitDialog

    model = ModelEntry(
        id="omlx/a", family="ornith", provider_id="omlx", model_name="a", location="local"
    )
    reg_path, state_path = _seed_registry_and_state(
        tmp_path, monkeypatch, models=[model], providers=("omlx",)
    )
    state = StateStore()
    state.set("omlx/a", ModelState(ready=True, running=False))
    save_state(state, state_path)

    stub = MagicMock()
    stub.name = "omlx"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = None
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    started = threading.Event()
    release = threading.Event()
    monkeypatch.setattr(
        "modelman.screens.models.start_local_model",
        _start_local_model_blocking_on(started, release),
    )

    exit_calls: list[int] = []
    monkeypatch.setattr("modelman.screens.models.os._exit", exit_calls.append)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.pause()
        await pilot.press("s")
        await pilot.pause()
        await pilot.press("y")
        assert started.wait(timeout=2)

        await pilot.press("escape")
        await pilot.pause()
        assert isinstance(app.screen, ConfirmForceQuitDialog)

        await pilot.press("ctrl+q")
        await pilot.pause()

        assert exit_calls == [0]

        release.set()
        for _ in range(50):
            await pilot.pause()


@pytest.mark.asyncio
async def test_force_quit_dialog_warns_about_pending_changes(tmp_path, monkeypatch):
    # When a start/stop is in flight AND a queued change (ready/delete/
    # move) is pending, force-quit abandons both — the dialog must
    # say so, since it's a stronger warning than "just the operation".
    from unittest.mock import MagicMock

    from modelman.providers import registry as prov_registry
    from modelman.screens.forms import ConfirmForceQuitDialog

    running_model = ModelEntry(
        id="omlx/a", family="ornith", provider_id="omlx", model_name="a", location="local"
    )
    queued_model = ModelEntry(
        id="ollama/b", family="ornith", provider_id="ollama", model_name="b", location="local"
    )
    reg_path, state_path = _seed_registry_and_state(
        tmp_path,
        monkeypatch,
        models=[running_model, queued_model],
        downloaded={"ollama/b": str(tmp_path / "d")},
        providers=("omlx", "ollama"),
    )
    state = StateStore()
    state.set("omlx/a", ModelState(ready=True, running=False))
    save_state(state, state_path)

    stub = MagicMock()
    stub.name = "omlx"
    stub.is_downloaded.return_value = True
    stub.size_of.return_value = None

    def get_provider(name, cfg):
        return stub

    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(get_provider))

    started = threading.Event()
    release = threading.Event()
    monkeypatch.setattr(
        "modelman.screens.models.start_local_model",
        _start_local_model_blocking_on(started, release),
    )

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.pause()

        # Cursor to the omlx row (sorts after ollama within the shared
        # family/location) and start it — blocks on `release`.
        table = app.screen.query_one("#model-table", DataTable)
        table.move_cursor(row=table.get_row_index("omlx/a"))
        await pilot.press("s")
        await pilot.pause()
        await pilot.press("y")
        assert started.wait(timeout=2)

        # Cursor to the ollama row (rows are keyed by model id) and queue a
        # delete on it.
        table.move_cursor(row=table.get_row_index("ollama/b"))
        await pilot.press("d")
        await pilot.pause()
        assert app.screen.queued_deletes

        await pilot.press("escape")
        await pilot.pause()

        assert isinstance(app.screen, ConfirmForceQuitDialog)
        labels = " ".join(str(label.visual) for label in app.screen.query(Label))
        assert "1 pending change(s) will be lost" in labels

        release.set()
        for _ in range(50):
            await pilot.pause()
            if app.screen.__class__.__name__ != "ConfirmForceQuitDialog":
                break


_ONE_BAD_ENTRY = """\
providers = [{ id = "ollama", name = "Ollama", auth = { type = "none" } }]

[[models]]
id = "ollama/good"
family = "f"
provider_id = "ollama"
model_name = "good"

[[models]]
id = "ollama/no-family"
provider_id = "ollama"
model_name = "bad"
"""


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "content",
    [
        _ONE_BAD_ENTRY,
        "[[models]\nnot toml",
        "models = 3\n",
        # #247: valid TOML whose models are under a name modelman does not
        # read. It loaded as zero models without a message, and the first save
        # rewrote the file as `models = []`.
        """
[[model]]
id = "ollama/qwen3"
family = "qwen3"
provider_id = "ollama"
model_name = "qwen3:8b"
""",
    ],
    ids=["malformed-entry", "toml-syntax", "wrong-shape", "unknown-top-level-key"],
)
async def test_app_refuses_to_open_an_unreadable_registry(tmp_path, monkeypatch, content):
    """#240: only a missing registry is an empty one. A file that is there
    and cannot be read opened as an empty model list with no message, and the
    first save wrote that over every provider, family and model in it. The
    app must stop instead — saying which file and why — with nothing mounted
    that could save."""
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text(content)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))

    from modelman.app import ModelmanApp
    from modelman.screens.models import ModelScreen

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        assert not isinstance(app.screen, ModelScreen)

    assert app.return_code == 1
    assert str(registry_path) in app.registry_error
    assert "fix or move it aside" in app.registry_error
    assert registry_path.read_text() == content


@pytest.mark.asyncio
async def test_app_refuses_to_open_a_dangling_registry_symlink(tmp_path, monkeypatch):
    """#248: `Path.exists()` is False for a symlink to a missing file, so a
    registry that lives on an unmounted volume or in a checkout that moved
    took the missing-registry branch: an empty model list with no message, and
    the first save replaced the link with a regular file. The app must stop
    instead, naming the link and what it points at, with nothing mounted that
    could save over it."""
    registry_path = tmp_path / "registry.toml"
    target = tmp_path / "moved-away" / "registry.toml"
    registry_path.symlink_to(target)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))

    from modelman.app import ModelmanApp
    from modelman.screens.models import ModelScreen

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        assert not isinstance(app.screen, ModelScreen)

    assert app.return_code == 1
    assert str(registry_path) in app.registry_error
    assert str(target) in app.registry_error
    assert registry_path.is_symlink() and not target.exists()


@pytest.mark.asyncio
async def test_app_opens_empty_when_there_is_no_registry(tmp_path, monkeypatch):
    """The fresh install the empty-registry fallback is for still works."""
    monkeypatch.setenv("MODELMAN_REGISTRY", str(tmp_path / "registry.toml"))
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))

    from modelman.app import ModelmanApp
    from modelman.screens.models import ModelScreen

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        assert isinstance(app.screen, ModelScreen)
        assert app.screen.registry.models == []
    assert app.registry_error is None
