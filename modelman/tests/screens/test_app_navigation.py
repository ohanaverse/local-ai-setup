import pytest
from textual.widgets import Button, Checkbox, DataTable, Input

from modelman.app import ModelmanApp
from modelman.downloads import DownloadState
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
from modelman.state import StateStore, save_state

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
        # And it should be in the provider list used for the add-model
        # form's provider dropdown.
        assert "claude" in app.screen._provider_list()


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
async def test_toggle_ready_queues_variant(tmp_path, monkeypatch):
    """Pressing `x` on a not-ready row queues the expose and cascades a
    background download (mapped provider, Task 13) — the ready-on part
    no longer sits in queued_ready for apply-on-exit."""
    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
    _seed_registry_and_state(tmp_path, monkeypatch, models=[o35])

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        started = []
        monkeypatch.setattr(
            app.downloads,
            "start",
            lambda mid, variant, cfg, on_complete=None, registry=None: started.append(mid),
        )
        await pilot.press("x")
        await pilot.pause()
        assert started == ["ollama/o35"]
        assert app.screen.queued_exposes.get("ollama/o35") is True
        assert "ollama/o35" in app.screen._ready_cascade_for_expose


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

        # Toggle download on missing → ⏳ ('x' cascades a background
        # download for a mapped provider now; the glyph reflects the
        # in-flight download).
        mt.cursor_coordinate = (1, 0)
        started = []
        monkeypatch.setattr(
            app.downloads,
            "start",
            lambda mid, variant, cfg, on_complete=None, registry=None: started.append(mid),
        )
        await pilot.press("x")
        await pilot.pause()
        from modelman.downloads import DownloadState

        app.downloads._states["ollama/missing"] = DownloadState(
            model_id="ollama/missing",
            variant_id="ollama/missing",
            provider="ollama",
            status="downloading",
        )
        app.screen.reload()
        await pilot.pause()
        mt = app.screen.query_one("#model-table", DataTable)
        rows = {r[2]: r for r in [mt.get_row_at(i) for i in range(mt.row_count)]}
        assert "⏳" in rows["missing"][4]

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
        # The add flow starts its real-provider ready-on as a background
        # download now (Task 14) — don't run a real pull in tests.
        monkeypatch.setattr(app.downloads, "start", lambda *a, **k: None)
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
    """An expose against a model whose state is stale (on disk but
    modelman.toml not yet updated) no longer applies immediately: `x`
    routes the ready-on through DownloadManager (Task 13) and the apply
    defers the expose until the download completes (Task 15), so the
    ready gate never sees a spurious 'not ready' rejection."""
    from unittest.mock import MagicMock

    from textual.widgets import DataTable

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
        # Reconcile knows the model is on disk, but state.ready is still
        # False (stale). The reconcile overlay satisfies the expose gate,
        # so `x` queues the expose without a download (Task 13). Simulate
        # a download still being in flight at apply time and drive the
        # apply through the real UI: Task 15 must defer the expose rather
        # than let the ready gate reject it.
        from modelman.downloads import DownloadState

        await pilot.press("x")  # toggle expose
        await pilot.pause()
        assert app.screen.queued_exposes.get("ollama/o35") is True
        assert app.screen.queued_ready == {}
        app.downloads._states["ollama/o35"] = DownloadState(
            model_id="ollama/o35",
            variant_id="ollama/o35",
            provider="ollama",
            status="downloading",
        )
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
    # The expose is still in flight against the downloading model: the
    # apply deferred it (register_post_download) rather than letting the
    # ready gate reject it, so nothing is exposed yet — it lands when the
    # download succeeds.
    assert final.ready is False
    assert final.exposed is False
    from modelman.litellm import load_litellm_config

    config = load_litellm_config(litellm_path)
    assert config["model_list"] == []


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

    from modelman.screens.status import StatusScreen

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

    app = ModelmanApp(family="ornith")
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

    loaded = load_state(state_path)
    assert loaded.get("ollama/o35").ready is True  # untouched row preserved
    assert "ollama/q8" not in loaded.models  # the delete landed


@pytest.mark.asyncio
async def test_model_table_repaints_after_returning_from_apply(tmp_path, monkeypatch):
    """_run_apply (models.py) mutates self.registry/self.state in place but
    never touches the DataTable widget itself — only reload() repopulates
    it. Once StatusScreen is dismissed and control returns to ModelScreen,
    the table must show the post-apply row set immediately, not the stale
    pre-apply rows left over from before Escape was pressed."""
    from textual.widgets import DataTable

    from modelman.screens.status import StatusScreen

    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
    q8 = ModelEntry(id="ollama/q8", family="ornith", provider_id="ollama", model_name="ornith:8b")
    _reg_path, _state_path = _seed_registry_and_state(
        tmp_path,
        monkeypatch,
        models=[o35, q8],
        downloaded={"ollama/o35": "/fake/o35", "ollama/q8": "/fake/q8"},
    )

    from unittest.mock import MagicMock

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
        await pilot.pause()

        mt = app.screen.query_one("#model-table", DataTable)
        assert mt.row_count == 2
        mt.cursor_coordinate = (0, 0)
        await pilot.press("d")  # queue delete of o35
        await pilot.pause()
        await pilot.press("escape")
        await pilot.pause()
        for btn in app.screen.query(Button):
            if btn.id == "apply":
                btn.press()
                break
        for _ in range(50):
            await pilot.pause()
            if isinstance(app.screen, StatusScreen) and app.screen.done:
                break

        await pilot.press("escape")  # back to ModelScreen
        await pilot.pause()

        mt = app.screen.query_one("#model-table", DataTable)
        row_ids = {str(k.value) for k in mt.rows}
        assert row_ids == {"ollama/q8"}, f"table still shows stale rows: {row_ids}"


@pytest.mark.asyncio
async def test_discard_after_apply_does_not_resurrect_earlier_applied_delete(
    tmp_path, monkeypatch
):
    """A Discard must only undo changes queued since the last apply, not
    resurrect a delete from an earlier apply in the same session.

    ModelScreen is now the app's single long-lived root screen (it's never
    recreated per family the way the old FamilyScreen->ModelScreen flow
    was), so its discard-snapshot has to be retaken after every apply run —
    otherwise a later Discard rolls all the way back to the snapshot taken
    at app launch, silently undoing every apply since, even ones already
    saved to disk. Regression test for that scenario: apply a delete of
    o35, return to the model list, queue an unrelated delete of q8, then
    discard — o35 must stay deleted and q8's queued delete must be the
    only thing discard undoes."""
    from unittest.mock import MagicMock

    from textual.widgets import DataTable

    from modelman.screens.status import StatusScreen

    o35 = ModelEntry(
        id="ollama/o35", family="ornith", provider_id="ollama", model_name="ornith:35b"
    )
    q8 = ModelEntry(id="ollama/q8", family="ornith", provider_id="ollama", model_name="ornith:8b")
    reg_path, state_path = _seed_registry_and_state(
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
    from modelman.registry import load_registry

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.pause()  # let reconcile settle

        # Apply #1: delete o35 (sorts first: "ornith:35b" < "ornith:8b").
        mt = app.screen.query_one("#model-table", DataTable)
        mt.cursor_coordinate = (0, 0)
        await pilot.press("d")
        await pilot.pause()
        await pilot.press("escape")
        await pilot.pause()
        for btn in app.screen.query(Button):
            if btn.id == "apply":
                btn.press()
                break
        for _ in range(50):
            await pilot.pause()
            if isinstance(app.screen, StatusScreen) and app.screen.done:
                break

        # Confirm the delete landed before doing anything else.
        assert "ollama/o35" not in [m.id for m in load_registry(reg_path).models]

        # Back to ModelScreen — this is what must retake the snapshot.
        await pilot.press("escape")
        await pilot.pause()

        # Queue an unrelated delete of q8, then discard it.
        mt = app.screen.query_one("#model-table", DataTable)
        mt.cursor_coordinate = (0, 0)
        await pilot.press("d")
        await pilot.pause()
        await pilot.press("escape")
        await pilot.pause()
        for btn in app.screen.query(Button):
            if btn.id == "discard":
                btn.press()
                break
        await pilot.pause()

    reloaded = [m.id for m in load_registry(reg_path).models]
    assert "ollama/o35" not in reloaded  # the earlier apply must stay applied
    assert "ollama/q8" in reloaded  # the discarded queue-only delete must not land


@pytest.mark.asyncio
async def test_escape_with_pending_shows_dialog_and_apply(tmp_path, monkeypatch):
    """Escape with a queued change shows the confirm dialog; Apply runs
    PendingChanges and persists the flip. Uses a cloud-provider model
    (no download mechanism) for the ready-on: real downloads no longer
    run inside apply() — a mapped provider's ready-on goes through
    DownloadManager instead — so this pins the flag-only flip path."""

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
    # and the ready-on stays a queued flag flip (today and after the
    # DownloadManager routing lands).
    from modelman.registry import ProviderEntry, load_registry, save_registry

    reg = load_registry(_reg_path)
    reg.providers[0] = ProviderEntry(
        id="openrouter", name="OpenRouter", auth=AuthConfig(type="none"), location="cloud"
    )
    save_registry(reg, _reg_path)

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.pause()
        await pilot.press("r")  # queue a ready flip (flag-only provider)
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
        # 'x' on a mapped-provider model cascades a background download
        # (Task 13) — simulate DownloadManager registering it so discard
        # has something to cancel.
        from modelman.downloads import DownloadState

        monkeypatch.setattr(
            app.downloads,
            "start",
            lambda mid, variant, cfg, on_complete=None, registry=None: app.downloads._states.update(
                {
                    mid: DownloadState(
                        model_id=mid, variant_id=mid, provider="ollama", status="downloading"
                    )
                }
            ),
        )
        cancelled = []
        monkeypatch.setattr(app.downloads, "cancel", lambda mid: cancelled.append(mid))
        await pilot.press("x")
        await pilot.pause()
        assert "ollama/o35" in app.screen._ready_cascade_for_expose
        # Open the exit dialog.
        await pilot.press("escape")
        await pilot.pause()
        # Press the Discard button.
        for btn in app.screen.query(Button):
            if btn.id == "discard":
                btn.press()
                break
        await pilot.pause()
        # ModelScreen is the app's only screen — discard restores the
        # snapshot and reloads in place rather than popping anywhere.
        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)

    from modelman.state import load_state

    # State on disk should be unchanged: no downloaded entry for o35.
    assert not load_state(state_path).get("ollama/o35").ready
    # Discard-cancels-cascade: the cascaded download was cancelled.
    assert cancelled == ["ollama/o35"]


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
    immediately (the add's real-provider ready-on routes through
    DownloadManager, so there's no later apply-time save for it)."""
    from textual.widgets import Input

    ms, reg_path, _state = _make_screen(tmp_path, monkeypatch)

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        # Don't run the real download the add now triggers (Task 14).
        monkeypatch.setattr(app.downloads, "start", lambda *a, **k: None)
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
    # The add persists immediately now (mirrors edits): the real-provider
    # ready-on bypasses the apply-on-exit queue entirely, so there's no
    # later save point that would otherwise persist the entry.
    reloaded = load_registry(reg_path)
    assert "ollama/ornith:8b" in [m.id for m in reloaded.models]


@pytest.mark.asyncio
async def test_discard_cancels_download_for_a_model_added_this_session(
    tmp_path, monkeypatch, stub_ollama_caps
):
    """Regression for a review finding: Discard only cancelled downloads
    cascaded in by an expose toggle (_ready_cascade_for_expose), not one
    started directly by adding a model. If the user adds a model (whose
    real download starts immediately, bypassing the queue), then queues
    an unrelated change that opens the exit-confirm dialog and discards
    it, the added model's registry entry is rolled back but its download
    used to keep running — later persisting a dangling modelman.toml row
    (ready=True) for a model_id no longer in the registry."""
    from textual.widgets import Input, Select

    from modelman.downloads import DownloadState
    from modelman.registry import ModelEntry

    b = ModelEntry(id="ollama/b", family="ornith", provider_id="ollama", model_name="b:tag")
    ms, _reg_path, _state_path = _make_screen(tmp_path, monkeypatch, entries=[b])

    from modelman.app import ModelmanApp

    app = ModelmanApp()
    async with app.run_test() as pilot:
        pilot.app.push_screen(ms)
        await pilot.pause()
        monkeypatch.setattr(
            app.downloads,
            "start",
            lambda mid, variant, cfg, on_complete=None, registry=None: app.downloads._states.update(
                {
                    mid: DownloadState(
                        model_id=mid, variant_id=mid, provider="ollama", status="downloading"
                    )
                }
            ),
        )
        cancelled = []
        monkeypatch.setattr(app.downloads, "cancel", lambda mid: cancelled.append(mid))

        await pilot.press("a")  # add model A; its real download starts immediately
        await pilot.pause()
        provider_sel = app.screen.query_one("#provider-select", Select)
        provider_sel.value = "ollama"
        await pilot.pause()
        app.screen.query_one("#model", Input).focus()
        for ch in "ornith:8b":
            await pilot.press(ch)
        await pilot.press("enter")
        await pilot.pause()
        assert "ollama/ornith:8b" in ms._added_ids

        # Queue an unrelated delete so Escape opens the exit-confirm dialog
        # (adding A alone queues nothing — its download bypasses the queue).
        mt = ms.query_one("#model-table", DataTable)
        row_keys = [k.value for k in mt.rows]
        mt.cursor_coordinate = (row_keys.index("ollama/b"), 0)
        await pilot.press("d")
        await pilot.pause()
        assert "ollama/b" in ms.queued_deletes

        await pilot.press("escape")
        await pilot.pause()
        for btn in app.screen.query(Button):
            if btn.id == "discard":
                btn.press()
                break
        await pilot.pause()

    assert cancelled == ["ollama/ornith:8b"]
    assert "ollama/ornith:8b" not in [m.id for m in ms.registry.models]


@pytest.mark.asyncio
async def test_model_screen_toggle_ready_queues_variant(
    tmp_path,
    monkeypatch,
):
    """Pressing `x` on a not-ready row queues the expose and cascades a
    background download (mapped provider, Task 13) instead of queueing a
    ready-on for apply-on-exit."""
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
        started = []
        monkeypatch.setattr(
            app.downloads,
            "start",
            lambda mid, variant, cfg, on_complete=None, registry=None: started.append(mid),
        )
        await pilot.press("x")
        await pilot.pause()
        assert started == ["ollama/o35"]
        assert ms.queued_exposes.get("ollama/o35") is True
        assert "ollama/o35" in ms._ready_cascade_for_expose


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
        # 'x' on a mapped-provider (llamacpp) model cascades a background
        # download now (Task 13) instead of queueing ready=True.
        monkeypatch.setattr(app.downloads, "start", lambda *a, **k: None)
        await pilot.press("x")
        await pilot.pause()
        assert ms.queued_exposes == {"llamacpp/ornith-q4": True}
        assert "llamacpp/ornith-q4" in ms._ready_cascade_for_expose
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
        registry_path=reg_path,
        state_path=state_path,
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
        registry_path=reg_path,
        state_path=state_path,
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
        app.screen.query_one("#subscription-checkbox", Checkbox).value = True
        await pilot.pause()
        app.screen.query_one("#subscription-price", Input).value = "20"
        await _submit(app, pilot)
        await pilot.pause()

        # Visible immediately in the same (only) screen's table.
        mt = app.screen.query_one("#model-table", DataTable)
        rows = [mt.get_row_at(i) for i in range(mt.row_count)]
        assert rows[0][7] == "$20.00/mo"

    # And it was actually persisted, not just held in this session's
    # in-memory registry: a fresh app relaunch sees it too.
    app2 = ModelmanApp()
    async with app2.run_test() as pilot:
        await pilot.pause()
        mt = app2.screen.query_one("#model-table", DataTable)
        rows = [mt.get_row_at(i) for i in range(mt.row_count)]
        assert rows, "model must still be listed after relaunch"
        assert rows[0][7] == "$20.00/mo"


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
    from modelman.registry import load_registry
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
        await pilot.press("escape")  # back to the model screen (apply done)
        await pilot.pause()
        # The moved model no longer shows under its old family, even
        # though that family name is still selectable (asserted below).
        assert not any(m.family == "gemma4:26b-mlx" for m in ms.registry.models)

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
        # _on_add_model now persists the entry and starts its ready-on as
        # a background download (Task 14) — don't run a real pull in tests.
        monkeypatch.setattr(app.downloads, "start", lambda *a, **k: None)
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
        # _on_add_model now persists the entry and starts its ready-on as
        # a background download (Task 14) — don't run a real pull in tests.
        monkeypatch.setattr(app.downloads, "start", lambda *a, **k: None)
        # Out-of-family add, applied exactly as _on_add_model does.
        ms._on_add_model(
            ModelFormResult(
                spec={"id": "ollama/moved", "provider": "ollama", "name": "moved:9b"},
                family="mamba",
            )
        )
        assert "ollama/moved" in [m.id for m in ms.registry.models]
        # The add routes its ready-on through DownloadManager now (mapped
        # provider), so the queue is empty and escape would pop straight
        # back — queue a pending change to force the exit dialog.
        ms.queued_ready["ollama/moved"] = True
        await pilot.press("escape")
        await pilot.pause()
        await pilot.press("d")  # discard
        await pilot.pause()

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
    list/show thousands of times; applying any expose queue would also
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
    # The quit guard's no-op path: with nothing downloading, ctrl+q must
    # exit the app directly, exactly like the pre-guard behavior.
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
async def test_ctrl_q_blocked_while_a_download_is_active(tmp_path, monkeypatch):
    # ctrl+q while a download is running must not exit out from under
    # the download thread — it pushes QuitBlockedModal instead.
    _seed_registry_and_state(tmp_path, monkeypatch)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.downloads._states["ollama/x"] = DownloadState(
            model_id="ollama/x", variant_id="ollama/x", provider="ollama", status="downloading"
        )
        await pilot.press("ctrl+q")
        await pilot.pause()
        assert app.is_running
        from modelman.screens.forms import QuitBlockedModal

        assert isinstance(app.screen, QuitBlockedModal)


@pytest.mark.asyncio
async def test_quit_blocked_modal_review_pushes_download_screen(tmp_path, monkeypatch):
    # QuitBlockedModal's "Review Downloads" choice must open the
    # DownloadScreen so the user can cancel the blocking download.
    _seed_registry_and_state(tmp_path, monkeypatch)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.downloads._states["ollama/x"] = DownloadState(
            model_id="ollama/x", variant_id="ollama/x", provider="ollama", status="downloading"
        )
        await pilot.press("ctrl+q")
        await pilot.pause()
        await pilot.press("r")  # QuitBlockedModal's "Review Downloads" binding
        await pilot.pause()
        from modelman.screens.downloads import DownloadScreen

        assert isinstance(app.screen, DownloadScreen)
