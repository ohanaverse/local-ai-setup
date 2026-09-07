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


def test_cancel_cleanup_skips_rmtree_when_artifact_is_shared(monkeypatch):
    # Regression for a review finding on the omlx provider: two registry
    # entries can share one on-disk target directory (omlx keys storage on
    # the repo basename), so cancelling a download for one entry must not
    # blow away the other entry's already-completed weights. start() must
    # be given the registry so _finish's cleanup step can detect the
    # conflict via find_shared_artifact_owner and skip cleanup_partial_download.
    from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry

    release = threading.Event()

    def _download(*a, **k):
        release.wait(2)
        raise DownloadCancelled("x")

    provider = MagicMock()
    provider.download.side_effect = _download
    provider.cancel_current.side_effect = lambda: release.set()
    # Both variants resolve to the same on-disk path — the collision
    # find_shared_artifact_owner is meant to detect.
    provider.path_of.return_value = "/models/shared-basename"
    _register_stub_provider(monkeypatch, provider)

    registry = Registry(
        providers=[ProviderEntry(id="omlx", name="X", auth=AuthConfig(type="omlx"))],
        models=[
            ModelEntry(id="omlx/a", family="f", provider_id="omlx", model_name="orgA/model"),
            ModelEntry(id="omlx/b", family="f", provider_id="omlx", model_name="orgB/model"),
        ],
    )

    mgr = DownloadManager(_FakeApp())
    mgr.start(
        "omlx/a",
        {"id": "omlx/a", "provider": "omlx", "repo": "orgA/model"},
        {},
        registry=registry,
    )
    while not mgr.is_downloading("omlx/a"):
        time.sleep(0.01)
    mgr.cancel("omlx/a")

    deadline = time.time() + 2
    while mgr.is_downloading("omlx/a") and time.time() < deadline:
        time.sleep(0.01)

    provider.cleanup_partial_download.assert_not_called()


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


def test_post_download_action_runs_on_success(monkeypatch):
    # A deferred action (e.g. a queued expose) registered against a
    # downloading model must run exactly once, after the download's
    # state persistence, when that download succeeds.
    provider = MagicMock()
    provider.download.return_value = "/models/x"
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    ran = threading.Event()
    mgr.register_post_download("ollama/x", ran.set)
    done = threading.Event()
    mgr.start(
        "ollama/x",
        {"id": "ollama/x", "provider": "ollama", "name": "x"},
        {},
        on_complete=lambda p: done.set(),
    )

    assert done.wait(timeout=2)
    assert ran.wait(timeout=2)


def test_post_download_action_dropped_on_cancel(monkeypatch):
    # Actions registered for a download that gets cancelled are
    # discarded, never run — the model never became ready, so an action
    # that assumed it did (e.g. writing a LiteLLM route) would be wrong.
    release = threading.Event()

    def _download(*a, **k):
        release.wait(2)
        raise DownloadCancelled("x")

    provider = MagicMock()
    provider.download.side_effect = _download
    provider.cancel_current.side_effect = lambda: release.set()
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    ran = threading.Event()
    mgr.register_post_download("ollama/x", ran.set)
    mgr.start("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x"}, {})
    while not mgr.is_downloading("ollama/x"):
        time.sleep(0.01)
    mgr.cancel("ollama/x")

    deadline = time.time() + 2
    while mgr.is_downloading("ollama/x") and time.time() < deadline:
        time.sleep(0.01)
    time.sleep(0.05)  # let a wrongly-firing action run, if any
    assert not ran.is_set()


def test_post_download_action_dropped_on_failure(monkeypatch):
    # Same discard rule for a download that fails on its own (network
    # error, disk full, ...) with no cancel() call.
    provider = MagicMock()
    provider.download.side_effect = RuntimeError("network error")
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    ran = threading.Event()
    mgr.register_post_download("ollama/x", ran.set)
    mgr.start("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x"}, {})

    deadline = time.time() + 2
    while mgr.is_downloading("ollama/x") and time.time() < deadline:
        time.sleep(0.01)
    time.sleep(0.05)
    assert not ran.is_set()


def test_clear_state_removes_all_tracking_for_model(monkeypatch):
    # clear_state() should remove all tracking data for a model_id,
    # including states, providers, variants, registries, cancel requests,
    # and post-download actions.
    provider = MagicMock()
    provider.download.return_value = "/models/x"
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    ran = threading.Event()
    mgr.register_post_download("ollama/x", ran.set)
    done = threading.Event()
    mgr.start(
        "ollama/x",
        {"id": "ollama/x", "provider": "ollama", "name": "x"},
        {},
        on_complete=lambda p: done.set(),
    )
    assert done.wait(timeout=2)

    # Verify state exists before clearing
    states = mgr.states()
    assert any(s.model_id == "ollama/x" for s in states)

    # Clear the state
    mgr.clear_state("ollama/x")

    # Verify all tracking is removed
    states = mgr.states()
    assert not any(s.model_id == "ollama/x" for s in states)
    assert mgr.is_downloading("ollama/x") is False
    # Post-download action already ran on success (that's expected)
    assert ran.is_set()


def test_clear_finished_removes_only_non_downloading_states(monkeypatch):
    # clear_finished() should only remove done/failed/cancelled states,
    # leaving active downloads untouched.
    release = threading.Event()
    provider = MagicMock()
    provider.download.side_effect = lambda *a, **k: (release.wait(2), "/models/x")[1]
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    # Start an active download
    mgr.start("ollama/active", {"id": "ollama/active", "provider": "ollama", "name": "active"}, {})
    # Add finished states manually
    from modelman.downloads import DownloadState
    mgr._states["ollama/done"] = DownloadState(
        model_id="ollama/done", variant_id="ollama/done", provider="ollama", status="done"
    )
    mgr._states["ollama/failed"] = DownloadState(
        model_id="ollama/failed", variant_id="ollama/failed", provider="ollama", status="failed"
    )
    mgr._states["ollama/cancelled"] = DownloadState(
        model_id="ollama/cancelled", variant_id="ollama/cancelled", provider="ollama", status="cancelled"
    )

    # Verify all states exist
    assert len(mgr.states()) == 4
    assert mgr.is_downloading("ollama/active") is True

    # Clear finished states
    mgr.clear_finished()

    # Only the active download should remain
    states = mgr.states()
    assert len(states) == 1
    assert states[0].model_id == "ollama/active"
    assert states[0].status == "downloading"

    release.set()


def test_clear_state_mid_download_keeps_registry_for_shared_artifact_guard(monkeypatch):
    # Regression for a review finding: clear_state() on an in-flight
    # download used to pop the registry snapshot too, so when the download
    # thread later unwound through _finish's cleanup, the
    # find_shared_artifact_owner conflict check was skipped (registry
    # popped → None) and cleanup_partial_download ran unconditionally —
    # for omlx that's an rmtree of the shared basename-keyed dir holding
    # ANOTHER registry entry's completed weights (the exact case
    # test_cancel_cleanup_skips_rmtree_when_artifact_is_shared guards).
    # The discard path cancels and immediately clear_state's while the
    # thread is still inside provider.download(), so the snapshot must
    # survive until the unwind itself reaps it.
    from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry

    release = threading.Event()

    def _download(*a, **k):
        release.wait(2)
        raise DownloadCancelled("x")

    provider = MagicMock()
    provider.download.side_effect = _download
    provider.cancel_current.side_effect = lambda: release.set()
    provider.path_of.return_value = "/models/shared-basename"
    _register_stub_provider(monkeypatch, provider)

    registry = Registry(
        providers=[ProviderEntry(id="omlx", name="X", auth=AuthConfig(type="omlx"))],
        models=[
            ModelEntry(id="omlx/a", family="f", provider_id="omlx", model_name="orgA/model"),
            ModelEntry(id="omlx/b", family="f", provider_id="omlx", model_name="orgB/model"),
        ],
    )

    mgr = DownloadManager(_FakeApp())
    mgr.start(
        "omlx/a",
        {"id": "omlx/a", "provider": "omlx", "repo": "orgA/model"},
        {},
        registry=registry,
    )
    while not mgr.is_downloading("omlx/a"):
        time.sleep(0.01)
    mgr.cancel("omlx/a")
    # Simulate the discard path: clear while the thread is still unwinding.
    mgr.clear_state("omlx/a")
    assert mgr.states() == []  # user-visible row is gone immediately

    deadline = time.time() + 2
    while provider.cleanup_partial_download.call_count == 0 and time.time() < deadline:
        time.sleep(0.01)

    # The registry snapshot survived the mid-flight clear, the conflict
    # check saw the other owner, and the rmtree was skipped.
    assert provider.cleanup_partial_download.call_count == 0
    with mgr._lock:
        assert "omlx/a" not in mgr._registries  # unwind reaped the retained snapshot


def test_clear_state_mid_download_keeps_cancel_flag_for_cancelled_classification(
    monkeypatch,
):
    # Regression for a review finding: clear_state() on an in-flight
    # download discarded model_id from _cancel_requested while the download
    # thread was still unwinding. Ollama's killed pull exits non-zero and
    # download() raises a plain RuntimeError (not DownloadCancelled), so
    # with the flag gone the raise was misclassified as "failed" and the
    # user got a spurious "Download failed" notification for a download
    # they deliberately cancelled. The flag must survive until the unwind
    # reads it (same discard-cancel-then-clear ordering as the shared-
    # artifact case above).
    release = threading.Event()

    def _download(*a, **k):
        release.wait(2)
        raise RuntimeError("`ollama pull x` failed (exit -15)")

    provider = MagicMock()
    provider.download.side_effect = _download
    provider.cancel_current.side_effect = lambda: release.set()
    _register_stub_provider(monkeypatch, provider)

    app = _FakeApp()
    mgr = DownloadManager(app)
    mgr.start("ollama/x", {"id": "ollama/x", "provider": "ollama", "name": "x:7b"}, {})
    while not mgr.is_downloading("ollama/x"):
        time.sleep(0.01)
    mgr.cancel("ollama/x")
    mgr.clear_state("ollama/x")
    assert mgr.states() == []  # row dropped while the thread unwinds

    deadline = time.time() + 2
    while not app.notifications and time.time() < deadline:
        time.sleep(0.01)

    # No "Download failed" notification fired, and the unwind classified
    # the raise as a user-requested cancel (silently reaping the state).
    assert app.notifications == []
    with mgr._lock:
        assert "ollama/x" not in mgr._cancel_requested  # unwind reaped the retained flag


def test_clear_finished_keeps_active_download_tracking(monkeypatch):
    # clear_finished() routes through the same _drop_locked teardown as
    # clear_state() (V5 dedupe) but must never drop an in-flight
    # download's execution context — only non-downloading rows.
    release = threading.Event()
    provider = MagicMock()
    provider.download.side_effect = lambda *a, **k: (release.wait(2), "/models/x")[1]
    _register_stub_provider(monkeypatch, provider)

    mgr = DownloadManager(_FakeApp())
    mgr.start("ollama/active", {"id": "ollama/active", "provider": "ollama", "name": "active"}, {})
    while not mgr.is_downloading("ollama/active"):
        time.sleep(0.01)
    mgr._cancel_requested.add("ollama/active")

    mgr.clear_finished()

    with mgr._lock:
        assert "ollama/active" in mgr._cancel_requested
        assert mgr._registries.get("ollama/active") is None

    release.set()
