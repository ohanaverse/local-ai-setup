"""Tests for the apply-status screen."""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from modelman.manifest import FamilyManifest
from modelman.queue import PendingChanges


def _manifest_unmodified(path) -> bool:
    """Return True if the manifest on disk still looks like the initial save.

    We use the loaded variant count as a proxy; cancellation must not have
    added a downloaded entry that wasn't there before.
    """
    from modelman.manifest import load_manifest

    m = load_manifest(path.stem, family_dir=path.parent)
    return len(m.downloaded) == 0 and all(v["id"] not in m.downloaded for v in m.variants)


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
    """Spin up a ModelmanApp with a small manifest and queue a delete+download.

    Yields (app, family, pending_factory). The factory returns a fresh
    PendingChanges that the StatusScreen can drive.
    """
    from modelman.manifest import save_manifest

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[
            {"id": "o35", "provider": "ollama", "name": "ornith:35b"},
            {"id": "q8", "provider": "ollama", "name": "ornith:8b"},
        ],
    )
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    yield m, fam_dir / "ornith.yaml"


@pytest.mark.asyncio
async def test_pending_changes_cancel_stops_loop(tmp_path):
    """Cancel flag set before apply() must stop the loop and emit apply:cancelled."""
    from modelman.manifest import FamilyManifest

    m = FamilyManifest(
        family="ornith",
        variants=[
            {"id": "a", "provider": "ollama", "name": "a"},
            {"id": "b", "provider": "ollama", "name": "b"},
        ],
    )
    fam_path = tmp_path / "fam.yaml"
    seen: list[str] = []

    provider = MagicMock()
    provider.name = "ollama"
    provider.download.return_value = "/tmp/new"
    provider.delete.return_value = None

    pending = PendingChanges(
        manifest=m,
        manifest_path=fam_path,
        providers={"ollama": provider},
        downloads=[m.variants[0], m.variants[1]],
    )
    # Pre-cancel so the loop exits before any download starts.
    pending.cancel()
    pending.apply(on_event=seen.append)

    assert seen == ["apply:cancelled"]
    assert not fam_path.exists()
    assert not provider.download.called


@pytest.mark.asyncio
async def test_pending_changes_fires_lifecycle_events(app_with_apply, tmp_path):
    """apply() must call on_event at start/end/fail for each step plus a final apply:done."""

    m, fam_path = app_with_apply
    events: list[str] = []

    p = _provider_with_events(tmp_path, events)
    pending = PendingChanges(
        manifest=m,
        manifest_path=fam_path,
        providers={"ollama": p},
        downloads=[m.variants[1]],  # q8
        deletes=[m.variants[0]],  # o35
    )

    seen: list[str] = []
    pending.apply(on_event=seen.append)

    # Tags are pipe-delimited: "verb:status|vid|label".
    assert "delete:start|o35|ornith:35b" in seen
    assert "delete:done|o35|ornith:35b" in seen
    assert "download:start|q8|ornith:8b" in seen
    assert "download:done|q8|ornith:8b" in seen
    assert "save:start" in seen
    assert "save:done" in seen
    assert seen[-1] == "apply:done"


@pytest.mark.asyncio
async def test_status_screen_esc_opens_cancel_dialog_and_cancel_stops(tmp_path, monkeypatch):
    """While the apply is still running, Escape must open the cancel-or-wait
    dialog; choosing Cancel must set the cancellation flag on the
    PendingChanges so the loop stops between items."""
    from textual.widgets import Button

    from modelman.app import ModelmanApp
    from modelman.manifest import save_manifest
    from modelman.screens.status import StatusScreen

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[
            {"id": "q8", "provider": "ollama", "name": "ornith:8b"},
            {"id": "q4", "provider": "ollama", "name": "ornith:4b"},
        ],
    )
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

    # Provider whose download blocks on an Event until we cancel it, so the
    # apply is mid-flight when we drive Esc.
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
            manifest=m,
            manifest_path=fam_dir / "ornith.yaml",
            providers={"ollama": provider},
            downloads=[m.variants[0], m.variants[1]],
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
        # Wait for the worker to register the pending and start the first
        # download. The download is blocked on `gate`, so we have a window.
        for _ in range(30):
            await pilot.pause()
            if captured_pending:
                break
        assert captured_pending, "worker did not register pending"
        # Press Escape; the cancel dialog should appear.
        await pilot.press("escape")
        await pilot.pause()
        # Click Cancel.
        for btn in app.screen.query(Button):
            if btn.id == "cancel":
                btn.press()
                break
        await pilot.pause()
        # The cancellation flag should now be set; release the gate so the
        # blocked download returns and the loop notices.
        gate.set()
        for _ in range(50):
            await pilot.pause()
            if screen.done:
                break
        assert captured_pending[0].cancelled is True
        assert screen.cancelled is True
        assert screen.done is True
        # Manifest must NOT have been saved (cancellation aborts before save).
        assert not (fam_dir / "ornith.yaml").exists() or _manifest_unmodified(
            fam_dir / "ornith.yaml"
        )


@pytest.mark.asyncio
async def test_status_screen_cancel_writes_immediate_feedback(tmp_path, monkeypatch):
    """Clicking Cancel must write a 'Cancelling…' line to the log
    immediately, before the worker thread has had time to catch up."""
    import threading

    from textual.widgets import Button, RichLog

    from modelman.app import ModelmanApp
    from modelman.manifest import save_manifest
    from modelman.screens.status import StatusScreen

    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[{"id": "q8", "provider": "ollama", "name": "ornith:8b"}],
    )
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text("providers:\n  ollama:\n    type: ollama\n")

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
            manifest=m,
            manifest_path=fam_dir / "ornith.yaml",
            providers={"ollama": provider},
            downloads=[m.variants[0]],
        )
        captured_pending.append(pending)
        register(pending)
        pending.apply(on_event=log_event, on_progress=on_progress)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        screen = StatusScreen(family="ornith", run_apply=run_apply)
        app.push_screen(screen)
        # Wait for the worker to register and start the download.
        for _ in range(30):
            await pilot.pause()
            if captured_pending:
                break
        # Escape -> CancelApplyDialog -> Cancel
        await pilot.press("escape")
        await pilot.pause()
        for btn in app.screen.query(Button):
            if btn.id == "cancel":
                btn.press()
                break
        await pilot.pause()
        # At this moment the worker is still blocked in slow_download.
        # The screen should have already written a 'Cancelling…' line.
        log = screen.query_one(RichLog)
        text = "\n".join(line.text for line in log.lines)
        assert "Cancelling" in text
        # Now release the download so the worker can finish.
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

    m, fam_path = app_with_apply
    provider = _provider_with_events(tmp_path, [])

    def run_apply(log_event, on_progress, _register):
        # Emit two progress lines as if the provider were streaming.
        on_progress("pulling manifest")
        on_progress("pulling abcdef... 45%")
        pending = PendingChanges(
            manifest=m,
            manifest_path=fam_path,
            providers={"ollama": provider},
            downloads=[m.variants[1]],
            deletes=[m.variants[0]],
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

    m, fam_path = app_with_apply
    p = _provider_with_events(tmp_path, [])

    def run_apply(log_event, _progress, _register):
        pending = PendingChanges(
            manifest=m,
            manifest_path=fam_path,
            providers={"ollama": p},
            downloads=[m.variants[1]],
            deletes=[m.variants[0]],
        )
        pending.apply(on_event=log_event)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        screen = StatusScreen(family="ornith", run_apply=run_apply)
        app.push_screen(screen)
        await pilot.pause()
        # Wait for the worker to finish.
        for _ in range(20):
            await pilot.pause()
            if not screen._worker or not screen._worker.is_running:
                break
        assert screen.done is True
        log = screen.query_one(RichLog)
        text = "\n".join(line.text for line in log.lines)
        # Apply events include the human label (variant name) in the log.
        assert "ornith:35b" in text
        assert "ornith:8b" in text
        assert "Saved" in text or "saving" in text.lower()
        assert "Done" in text


@pytest.mark.asyncio
async def test_status_screen_renders_failure_reason(app_with_apply, tmp_path):
    """When a download fails, the exception reason should appear in
    the log under the 'Failed to download X' marker. Otherwise the
    user just sees the failure with no clue why."""
    from textual.widgets import RichLog

    from modelman.app import ModelmanApp
    from modelman.screens.status import StatusScreen

    m, fam_path = app_with_apply

    provider = MagicMock()
    provider.name = "ollama"
    provider.delete.return_value = None
    provider.download.side_effect = ConnectionError("dial tcp: i/o timeout")

    def run_apply(log_event, _progress, _register):
        pending = PendingChanges(
            manifest=m,
            manifest_path=fam_path,
            providers={"ollama": provider},
            downloads=[m.variants[1]],  # q8
            deletes=[m.variants[0]],    # o35
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
        # The first line of the underlying error must reach the user.
        assert "i/o timeout" in text


@pytest.mark.asyncio
async def test_status_screen_shows_size_on_download_done(app_with_apply, tmp_path):
    """The 'Downloaded X' success marker should include the actual
    file size so users can verify the download produced real bytes
    rather than a 0-byte empty file."""
    from textual.widgets import RichLog

    from modelman.app import ModelmanApp
    from modelman.screens.status import StatusScreen

    m, fam_path = app_with_apply
    provider = MagicMock()
    provider.name = "ollama"
    provider.delete.return_value = None

    real_path = tmp_path / "downloaded-q8.bin"
    real_path.write_bytes(b"x" * (2 * 1024 * 1024 * 1024))  # ~ 2 GB
    provider.download.return_value = str(real_path)

    def run_apply(log_event, _progress, _register):
        pending = PendingChanges(
            manifest=m,
            manifest_path=fam_path,
            providers={"ollama": provider},
            downloads=[m.variants[1]],  # q8
            deletes=[m.variants[0]],    # o35
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
