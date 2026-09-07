"""Tests for DownloadManager: background download lifecycle, cancellation,
parallel isolation, and status reporting."""

from __future__ import annotations

import threading
import time
from unittest.mock import MagicMock

from modelman.downloads import DownloadManager
from modelman.providers._progress import DownloadCancelled
from modelman.providers.registry import ProviderRegistry


class _FakeApp:
    """Minimal stand-in for ModelmanApp in unit tests: call_from_thread
    just calls the function immediately (tests join every worker thread
    before asserting, so no real cross-thread marshaling is needed)."""

    def __init__(self) -> None:
        self.notifications: list[str] = []

    def call_from_thread(self, func, *args):
        return func(*args)

    def notify(self, message: str, **kwargs) -> None:
        self.notifications.append(message)


def _register_stub_provider(monkeypatch, provider: MagicMock) -> None:
    monkeypatch.setattr(ProviderRegistry, "get", staticmethod(lambda name, cfg: provider))


def test_start_runs_download_and_marks_done(monkeypatch):
    # The core happy path: start() must invoke provider.download() on a
    # background thread and end with status "done".
    provider = MagicMock()
    provider.download.return_value = "/models/x"
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    done = threading.Event()
    mgr.start(
        "ollama/x",
        {"id": "ollama/x", "provider": "ollama", "name": "x:7b"},
        {},
        on_complete=lambda path: done.set(),
    )
    assert done.wait(timeout=2), "on_complete was never called"

    states = {s.model_id: s for s in mgr.states()}
    assert states["ollama/x"].status == "done"
    assert mgr.is_downloading("ollama/x") is False
    assert mgr.has_active() is False


def test_is_downloading_true_while_in_progress(monkeypatch):
    # Locking (Task 11) depends on is_downloading() being accurate for
    # the duration of the provider.download() call, not just before/after.
    release = threading.Event()
    provider = MagicMock()
    provider.download.side_effect = lambda *a, **k: (release.wait(2), "/models/x")[1]
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    mgr.start("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x:7b"}, {})
    try:
        assert mgr.is_downloading("ollama/x") is True
        assert mgr.has_active() is True
    finally:
        release.set()


def test_cancel_calls_provider_cancel_current(monkeypatch):
    # cancel() must delegate to the fresh provider instance's own
    # cancel_current() hook — the actual interruption mechanism is
    # provider-specific (subprocess kill for Ollama, a flag for HF).
    cancel_called = threading.Event()
    release = threading.Event()

    def _download(*a, **k):
        release.wait(2)
        raise DownloadCancelled("x")

    provider = MagicMock()
    provider.download.side_effect = _download
    provider.cancel_current.side_effect = lambda: (cancel_called.set(), release.set())
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    mgr.start("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x:7b"}, {})
    while not mgr.is_downloading("ollama/x"):
        time.sleep(0.01)
    mgr.cancel("ollama/x")

    assert cancel_called.wait(timeout=2)
    deadline = time.time() + 2
    while mgr.is_downloading("ollama/x") and time.time() < deadline:
        time.sleep(0.01)
    states = {s.model_id: s for s in mgr.states()}
    assert states["ollama/x"].status == "cancelled"
    provider.cleanup_partial_download.assert_called_once()


def test_a_generic_exception_after_cancel_is_still_classified_cancelled(monkeypatch):
    # Ollama's cancel_current() kills the pull subprocess via SIGTERM,
    # which makes download() raise a plain RuntimeError (non-zero exit),
    # not DownloadCancelled. Without explicit cancel-tracking this would
    # misreport as "failed" instead of "cancelled" for exactly the
    # provider users hit most often.
    release = threading.Event()

    def _download(*a, **k):
        release.wait(2)
        raise RuntimeError("`ollama pull x` failed (exit -15)")

    provider = MagicMock()
    provider.download.side_effect = _download
    provider.cancel_current.side_effect = lambda: release.set()
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    mgr.start("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x:7b"}, {})
    while not mgr.is_downloading("ollama/x"):
        time.sleep(0.01)
    mgr.cancel("ollama/x")

    deadline = time.time() + 2
    while mgr.is_downloading("ollama/x") and time.time() < deadline:
        time.sleep(0.01)
    states = {s.model_id: s for s in mgr.states()}
    assert states["ollama/x"].status == "cancelled"


def test_download_failure_without_cancel_is_classified_failed(monkeypatch):
    # A genuine failure (network error, disk full, ...) with no
    # cancel() call must be reported as "failed", not "cancelled".
    provider = MagicMock()
    provider.download.side_effect = RuntimeError("no space left on device")
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    mgr.start(
        "ollama/x",
        {"id": "ollama/x", "provider": "ollama", "name": "x:7b"},
        {},
        on_complete=lambda path: None,
    )
    deadline = time.time() + 2
    while mgr.is_downloading("ollama/x") and time.time() < deadline:
        time.sleep(0.01)

    states = {s.model_id: s for s in mgr.states()}
    assert states["ollama/x"].status == "failed"
    assert "no space left" in (states["ollama/x"].error or "")


def test_failure_notifies_the_app(monkeypatch):
    # Spec requires a failure be "surfaced ... via notification" — the
    # DownloadScreen table alone isn't enough, since the user may be on
    # a completely different screen when a background download fails.
    provider = MagicMock()
    provider.download.side_effect = RuntimeError("no space left on device")
    _register_stub_provider(monkeypatch, provider)

    app = _FakeApp()
    mgr = DownloadManager(app)
    mgr.start("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x:7b"}, {})

    deadline = time.time() + 2
    while mgr.is_downloading("ollama/x") and time.time() < deadline:
        time.sleep(0.01)

    assert any("ollama/x" in n and "no space left" in n for n in app.notifications), (
        app.notifications
    )


def test_parallel_downloads_get_isolated_provider_instances(monkeypatch):
    # Two simultaneous downloads must each get their OWN provider
    # instance from ProviderRegistry.get(), so per-download cancellation
    # state (_current_proc / _cancel_requested) can't cross-contaminate.
    seen_instances: list[object] = []

    def _factory(name, cfg):
        instance = MagicMock()
        instance.download.return_value = "/models/x"
        seen_instances.append(instance)
        return instance

    monkeypatch.setattr(ProviderRegistry, "get", staticmethod(_factory))

    mgr = DownloadManager(_FakeApp())
    done_a = threading.Event()
    done_b = threading.Event()
    mgr.start(
        "ollama/a",
        {"id": "ollama/a", "provider": "ollama", "name": "a"},
        {},
        on_complete=lambda p: done_a.set(),
    )
    mgr.start(
        "ollama/b",
        {"id": "ollama/b", "provider": "ollama", "name": "b"},
        {},
        on_complete=lambda p: done_b.set(),
    )

    assert done_a.wait(timeout=2)
    assert done_b.wait(timeout=2)
    assert len(seen_instances) == 2
    assert seen_instances[0] is not seen_instances[1]


def test_start_is_a_noop_if_already_downloading(monkeypatch):
    # A double 'r' press (or a race between the poll and a keypress)
    # must not spawn a second thread against the same model_id.
    release = threading.Event()
    provider = MagicMock()
    call_count = {"n": 0}

    def _download(*a, **k):
        call_count["n"] += 1
        release.wait(2)
        return "/models/x"

    provider.download.side_effect = _download
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    mgr.start("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x"}, {})
    mgr.start("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x"}, {})
    release.set()
    time.sleep(0.1)
    assert call_count["n"] == 1


def test_success_persists_ready_and_disk_path_to_state(tmp_path, monkeypatch):
    # DownloadManager must own persisting completion, since its caller
    # (whichever ModelScreen instance started the download) may no
    # longer exist by the time a download finishes.
    from modelman.state import _default_state_path, load_state

    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    assert _default_state_path() == state_path

    (tmp_path / "weights.gguf").write_bytes(b"x" * 1024)
    provider = MagicMock()
    provider.download.return_value = str(tmp_path / "weights.gguf")
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    done = threading.Event()
    mgr.start(
        "ollama/x",
        {"id": "ollama/x", "provider": "ollama", "name": "x:7b"},
        {},
        on_complete=lambda path: done.set(),
    )
    assert done.wait(timeout=2)

    loaded = load_state(state_path)
    entry = loaded.get("ollama/x")
    assert entry.ready is True
    assert entry.disk_path == str(tmp_path / "weights.gguf")
    assert entry.size_bytes == 1024


def test_success_persistence_is_a_merge_not_an_overwrite(tmp_path, monkeypatch):
    # Regression for the exact race Task 1/4 exist to close: an unrelated
    # model's state already on disk must survive a download completion
    # for a *different* model.
    from modelman.state import (
        ModelState,
        StateStore,
        _default_state_path,
        load_state,
        save_state,
    )

    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    assert _default_state_path() == state_path
    existing = StateStore()
    existing.set("ollama/other", ModelState(ready=True, disk_path="ollama:other"))
    save_state(existing, state_path)

    (tmp_path / "weights.gguf").write_bytes(b"x")
    provider = MagicMock()
    provider.download.return_value = str(tmp_path / "weights.gguf")
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    done = threading.Event()
    mgr.start(
        "ollama/x",
        {"id": "ollama/x", "provider": "ollama", "name": "x"},
        {},
        on_complete=lambda p: done.set(),
    )
    assert done.wait(timeout=2)

    loaded = load_state(state_path)
    assert loaded.get("ollama/other").ready is True  # untouched
    assert loaded.get("ollama/x").ready is True  # this download's own write


def test_progress_visible_while_downloading(monkeypatch):
    # Progress lines forwarded from the provider must be recorded on the
    # DownloadState while the download is still running — the
    # DownloadScreen's live progress column reads exactly this.
    release = threading.Event()
    seen_progress = threading.Event()

    def _download(variant, on_progress=None, **kwargs):
        on_progress("50 MB / 100 MB (50%)")
        seen_progress.set()
        release.wait(2)
        return "/models/x"

    provider = MagicMock()
    provider.download.side_effect = _download
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    mgr.start("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x"}, {})
    assert seen_progress.wait(timeout=2)
    states = {s.model_id: s for s in mgr.states()}
    assert "50%" in states["ollama/x"].progress
    release.set()
