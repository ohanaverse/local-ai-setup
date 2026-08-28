"""Tests for the apply-status screen.

PendingChanges now operates on Registry/StateStore; the manifest-shaped
fixtures are gone. The pipe-delimited event-tag format is unchanged so
StatusScreen can consume the new events without modification.
"""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from modelman.queue import PendingChanges
from modelman.registry import (
    AuthConfig,
    ModelEntry,
    ProviderEntry,
    Registry,
)
from modelman.state import StateStore


def _entry(*, id: str, family: str, provider: str, name: str) -> ModelEntry:
    return ModelEntry(id=id, family=family, provider_id=provider, model_name=name)


def _variant(*, id: str, provider: str, name: str) -> dict:
    return {"id": id, "provider": provider, "name": name}


def _setup(tmp_path):
    """Build a Registry/StateStore pair in tmp_path; return them plus the
    target paths the apply loop will save to. The on-disk files are NOT
    pre-written — cancel-path tests assert they remain absent.
    """
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    o35 = _entry(id="o35", family="ornith", provider="ollama", name="ornith:35b")
    q8 = _entry(id="q8", family="ornith", provider="ollama", name="ornith:8b")
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        models=[o35, q8],
    )
    state = StateStore()
    return reg, state, reg_path, state_path, [o35, q8]


def _provider_with_events(tmp_path, events: list[str]):
    p = MagicMock()
    p.name = "ollama"

    def fake_delete(v):
        events.append(f"delete:{v['id']}")

    def fake_download(v):
        events.append(f"download:{v['id']}")
        return str(tmp_path / f"new-{v['id']}")

    p.delete.side_effect = fake_delete
    p.download.side_effect = fake_download
    return p


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


@pytest.mark.asyncio
async def test_pending_changes_cancel_stops_loop(tmp_path):
    """Cancel flag set before apply() must stop the loop and emit apply:cancelled."""
    reg, state, reg_path, state_path, [o35, q8] = _setup(tmp_path)
    seen: list[str] = []

    provider = MagicMock()
    provider.name = "ollama"
    provider.download.return_value = "/tmp/new"
    provider.delete.return_value = None

    pending = PendingChanges(
        registry=reg,
        state=state,
        family="ornith",
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": provider},
        downloads=[
            ("o35", _variant(id="o35", provider="ollama", name="ornith:35b")),
            ("q8", _variant(id="q8", provider="ollama", name="ornith:8b")),
        ],
    )
    pending.cancel()
    pending.apply(on_event=seen.append)

    assert seen == ["apply:cancelled"]
    assert not reg_path.exists()
    assert not state_path.exists()
    assert not provider.download.called


@pytest.mark.asyncio
async def test_pending_changes_fires_lifecycle_events(app_with_apply, tmp_path):
    """apply() must call on_event at start/end/fail for each step plus a final apply:done."""
    reg, state, reg_path, state_path = app_with_apply
    events: list[str] = []

    p = _provider_with_events(tmp_path, events)
    pending = PendingChanges(
        registry=reg,
        state=state,
        family="ornith",
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": p},
        downloads=[("q8", _variant(id="q8", provider="ollama", name="ornith:8b"))],
        deletes=[("o35", _variant(id="o35", provider="ollama", name="ornith:35b"))],
    )
    seen: list[str] = []
    pending.apply(on_event=seen.append)

    assert "delete:start|o35|ornith:35b" in seen
    assert "delete:done|o35|ornith:35b" in seen
    assert "download:start|q8|ornith:8b" in seen
    assert "download:done|q8|ornith:8b" in seen
    assert "save:start" in seen
    assert "save:done" in seen
    assert seen[-1] == "apply:done"


@pytest.mark.asyncio
async def test_status_screen_esc_opens_cancel_dialog_and_cancel_stops(
    tmp_path,
    monkeypatch,
):
    """While the apply is still running, Escape must open the cancel-or-wait
    dialog; choosing Cancel must set the cancellation flag on the
    PendingChanges so the loop stops between items."""
    from textual.widgets import Button

    from modelman.app import ModelmanApp
    from modelman.screens.status import StatusScreen

    reg, state, reg_path, state_path, [o35, q8] = _setup(tmp_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    gate = __import__("threading").Event()
    captured_pending: list = []

    def make_provider():
        p = MagicMock()
        p.name = "ollama"
        p.cancel_current = MagicMock()

        def slow_download(v):
            gate.wait(timeout=2.0)
            return str(tmp_path / f"new-{v['id']}")

        p.download.side_effect = slow_download
        return p

    provider = make_provider()

    def run_apply(log_event, _progress, register):
        pending = PendingChanges(
            registry=reg,
            state=state,
            family="ornith",
            registry_path=reg_path,
            state_path=state_path,
            providers={"ollama": provider},
            downloads=[
                ("o35", _variant(id="o35", provider="ollama", name="ornith:35b")),
                ("q8", _variant(id="q8", provider="ollama", name="ornith:8b")),
            ],
        )
        captured_pending.append(pending)
        register(pending)
        pending.apply(on_event=log_event)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        screen = StatusScreen(family="ornith", run_apply=run_apply)
        app.push_screen(screen)
        await pilot.pause()
        for _ in range(30):
            await pilot.pause()
            if captured_pending:
                break
        assert captured_pending, "worker did not register pending"
        await pilot.press("escape")
        await pilot.pause()
        for btn in app.screen.query(Button):
            if btn.id == "cancel":
                btn.press()
                break
        await pilot.pause()
        gate.set()
        for _ in range(50):
            await pilot.pause()
            if screen.done:
                break
        assert captured_pending[0].cancelled is True
        assert screen.cancelled is True
        assert screen.done is True
        # Neither registry nor state was saved on cancel.
        assert not reg_path.exists()
        assert not state_path.exists()


@pytest.mark.asyncio
async def test_status_screen_cancel_writes_immediate_feedback(tmp_path, monkeypatch):
    """Clicking Cancel must write a 'Cancelling…' line to the log
    immediately, before the worker thread has had time to catch up."""
    import threading

    from textual.widgets import Button, RichLog

    from modelman.app import ModelmanApp
    from modelman.screens.status import StatusScreen

    reg, state, reg_path, state_path, [o35, _q8] = _setup(tmp_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    gate = threading.Event()
    captured_pending: list = []

    provider = MagicMock()
    provider.name = "ollama"
    provider.cancel_current = MagicMock()

    def slow_download(v):
        gate.wait(timeout=5.0)
        return str(tmp_path / f"new-{v['id']}")

    provider.download.side_effect = slow_download

    def run_apply(log_event, on_progress, register):
        pending = PendingChanges(
            registry=reg,
            state=state,
            family="ornith",
            registry_path=reg_path,
            state_path=state_path,
            providers={"ollama": provider},
            downloads=[("o35", _variant(id="o35", provider="ollama", name="ornith:35b"))],
        )
        captured_pending.append(pending)
        register(pending)
        pending.apply(on_event=log_event, on_progress=on_progress)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        screen = StatusScreen(family="ornith", run_apply=run_apply)
        app.push_screen(screen)
        for _ in range(30):
            await pilot.pause()
            if captured_pending:
                break
        await pilot.press("escape")
        await pilot.pause()
        for btn in app.screen.query(Button):
            if btn.id == "cancel":
                btn.press()
                break
        await pilot.pause()
        log = screen.query_one(RichLog)
        text = "\n".join(line.text for line in log.lines)
        assert "Cancelling" in text
        gate.set()
        for _ in range(50):
            await pilot.pause()
            if screen.done:
                break
        assert screen.cancelled is True


@pytest.mark.asyncio
async def test_status_screen_renders_provider_progress(app_with_apply, tmp_path):
    """Progress lines emitted via on_progress must appear in the log."""
    from textual.widgets import RichLog

    from modelman.app import ModelmanApp
    from modelman.screens.status import StatusScreen

    reg, state, reg_path, state_path = app_with_apply
    provider = _provider_with_events(tmp_path, [])

    def run_apply(log_event, on_progress, _register):
        on_progress("pulling manifest")
        on_progress("pulling abcdef... 45%")
        pending = PendingChanges(
            registry=reg,
            state=state,
            family="ornith",
            registry_path=reg_path,
            state_path=state_path,
            providers={"ollama": provider},
            downloads=[("q8", _variant(id="q8", provider="ollama", name="ornith:8b"))],
            deletes=[("o35", _variant(id="o35", provider="ollama", name="ornith:35b"))],
        )
        pending.apply(on_event=log_event, on_progress=on_progress)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        screen = StatusScreen(family="ornith", run_apply=run_apply)
        app.push_screen(screen)
        for _ in range(20):
            await pilot.pause()
            if screen.done:
                break
        log = screen.query_one(RichLog)
        text = "\n".join(line.text for line in log.lines)
        assert "pulling manifest" in text
        assert "pulling abcdef" in text


@pytest.mark.asyncio
async def test_status_screen_runs_apply_in_background(app_with_apply, tmp_path):
    """StatusScreen pushes, runs apply in a worker, and pops to FamilyScreen on done."""
    from textual.widgets import RichLog

    from modelman.app import ModelmanApp
    from modelman.screens.status import StatusScreen

    reg, state, reg_path, state_path = app_with_apply
    p = _provider_with_events(tmp_path, [])

    def run_apply(log_event, _progress, _register):
        pending = PendingChanges(
            registry=reg,
            state=state,
            family="ornith",
            registry_path=reg_path,
            state_path=state_path,
            providers={"ollama": p},
            downloads=[("q8", _variant(id="q8", provider="ollama", name="ornith:8b"))],
            deletes=[("o35", _variant(id="o35", provider="ollama", name="ornith:35b"))],
        )
        pending.apply(on_event=log_event)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        screen = StatusScreen(family="ornith", run_apply=run_apply)
        app.push_screen(screen)
        await pilot.pause()
        for _ in range(20):
            await pilot.pause()
            if not screen._worker or not screen._worker.is_running:
                break
        assert screen.done is True
        log = screen.query_one(RichLog)
        text = "\n".join(line.text for line in log.lines)
        assert "ornith:35b" in text
        assert "ornith:8b" in text
        assert "Saved" in text or "saving" in text.lower()
        assert "Done" in text


@pytest.mark.asyncio
async def test_status_screen_renders_failure_reason(app_with_apply, tmp_path):
    """When a download fails, the exception reason should appear in the log."""
    from textual.widgets import RichLog

    from modelman.app import ModelmanApp
    from modelman.screens.status import StatusScreen

    reg, state, reg_path, state_path = app_with_apply

    provider = MagicMock()
    provider.name = "ollama"
    provider.delete.return_value = None
    provider.download.side_effect = ConnectionError("dial tcp: i/o timeout")

    def run_apply(log_event, _progress, _register):
        pending = PendingChanges(
            registry=reg,
            state=state,
            family="ornith",
            registry_path=reg_path,
            state_path=state_path,
            providers={"ollama": provider},
            downloads=[("q8", _variant(id="q8", provider="ollama", name="ornith:8b"))],
            deletes=[("o35", _variant(id="o35", provider="ollama", name="ornith:35b"))],
        )
        pending.apply(on_event=log_event)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        screen = StatusScreen(family="ornith", run_apply=run_apply)
        app.push_screen(screen)
        for _ in range(20):
            await pilot.pause()
            if screen.done:
                break
        log = screen.query_one(RichLog)
        text = "\n".join(line.text for line in log.lines)
        assert "Failed to download" in text
        assert "i/o timeout" in text


@pytest.mark.asyncio
async def test_status_screen_shows_size_on_download_done(app_with_apply, tmp_path):
    """The 'Downloaded X' success marker should include the actual file size."""
    from textual.widgets import RichLog

    from modelman.app import ModelmanApp
    from modelman.screens.status import StatusScreen

    reg, state, reg_path, state_path = app_with_apply

    provider = MagicMock()
    provider.name = "ollama"
    provider.delete.return_value = None

    real_path = tmp_path / "downloaded-q8.bin"
    real_path.write_bytes(b"x" * (2 * 1024 * 1024 * 1024))
    provider.download.return_value = str(real_path)

    def run_apply(log_event, _progress, _register):
        pending = PendingChanges(
            registry=reg,
            state=state,
            family="ornith",
            registry_path=reg_path,
            state_path=state_path,
            providers={"ollama": provider},
            downloads=[("q8", _variant(id="q8", provider="ollama", name="ornith:8b"))],
            deletes=[("o35", _variant(id="o35", provider="ollama", name="ornith:35b"))],
        )
        pending.apply(on_event=log_event)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        screen = StatusScreen(family="ornith", run_apply=run_apply)
        app.push_screen(screen)
        for _ in range(20):
            await pilot.pause()
            if screen.done:
                break
        log = screen.query_one(RichLog)
        text = "\n".join(line.text for line in log.lines)
        assert "Downloaded ornith:8b" in text
        assert "2.0 GB" in text, text
