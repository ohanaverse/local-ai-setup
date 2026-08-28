"""Tests for download progress streaming (ollama + HF)."""

from __future__ import annotations

import contextlib
from unittest.mock import MagicMock

from modelman.providers._progress import ProgressTqdm, human_bytes
from modelman.providers.base import VariantSpec


def test_human_bytes():
    assert human_bytes(0) == "0 B"
    assert human_bytes(512) == "512 B"
    assert human_bytes(1024) == "1.0 KB"
    assert human_bytes(1024 * 1024) == "1.0 MB"
    assert human_bytes(1024**3) == "1.0 GB"


def test_progress_tqdm_fires_callback_on_update():
    """Each display() call must invoke on_progress with a formatted line."""
    import io

    seen: list[str] = []

    # display() / refresh() no-op when disable=True, so use a stderr sink
    # to silence the bar's actual rendering while still firing callbacks.
    bar = ProgressTqdm(
        total=1024 * 1024,
        desc="weights.bin",
        unit="B",
        on_progress=seen.append,
        file=io.StringIO(),
    )
    bar.update(512 * 1024)
    bar.refresh()
    bar.close()

    assert any("weights.bin" in line and "KB" in line for line in seen), seen
    assert any("done" in line for line in seen), seen


def test_progress_tqdm_no_callback_is_noop():
    """ProgressTqdm without on_progress must not crash."""
    bar = ProgressTqdm(total=100, desc="x", disable=True)
    bar.update(50)
    bar.close()  # no exception


def test_ollama_strips_ansi_codes():
    """_strip_ansi removes cursor/color/CR/BS but keeps plain text."""
    from modelman.providers.ollama import _strip_ansi

    raw = "\x1b[?25l\x1b[1Gpulling manifest \x1b[K\x1b[?25h\x1b[?2026l"
    assert _strip_ansi(raw) == "pulling manifest"

    # Carriage returns and stray spaces should be cleaned.
    assert _strip_ansi("\r\n  hello world  \r\n") == "hello world"


def test_progress_tqdm_raises_on_should_cancel():
    """If should_cancel returns True, display() must raise DownloadCancelled."""
    import io

    from modelman.providers._progress import DownloadCancelled

    # should_cancel is a flag toggled after construction so the parent's
    # auto-refresh inside __init__ doesn't immediately abort the bar.
    cancelled = {"flag": False}

    bar = ProgressTqdm(
        total=1024,
        desc="weights.bin",
        should_cancel=lambda: cancelled["flag"],
        file=io.StringIO(),
    )
    cancelled["flag"] = True
    # display() is the entry point ProgressTqdm checks on every refresh.
    try:
        bar.display()
    except DownloadCancelled:
        # Close() raises again because the bar still has _should_cancel
        # set; ignore the second raise from __del__.
        with contextlib.suppress(DownloadCancelled):
            bar.close()
        return
    raise AssertionError("DownloadCancelled was not raised")


def test_progress_tqdm_picks_up_active_context():
    """When the provider sets the class-level active context before
    instantiating a bar (the huggingface_hub path), every bar created
    during that call inherits the callbacks without needing them as
    constructor kwargs."""
    import io

    from modelman.providers._progress import DownloadCancelled

    captured: list[str] = []
    cancelled = {"flag": False}

    ProgressTqdm.set_active_context(
        on_progress=captured.append,
        should_cancel=lambda: cancelled["flag"],
    )
    try:
        # No on_progress / should_cancel kwargs — bar must pick them up
        # from the class-level context.
        bar = ProgressTqdm(total=1024, desc="ctx-test.bin", file=io.StringIO())
        # Cancelling after construction doesn't re-fire display, so flip
        # the flag and explicitly call display() to verify the abort path.
        cancelled["flag"] = True
        try:
            bar.display()
        except DownloadCancelled:
            return
        raise AssertionError("DownloadCancelled was not raised via active context")
    finally:
        ProgressTqdm.clear_active_context()


def test_snapshot_download_real_call_no_typeerror(tmp_path, monkeypatch):
    """Regression test: snapshot_download() doesn't accept tqdm_kwargs,
    so the providers must NOT pass it. This test calls the real HF API
    (for a tiny public repo) to verify no TypeError is raised on the
    provider's download() path."""
    from modelman.providers.llamacpp import LlamaCppProvider

    # Use an isolated HF cache so we don't trample on the user's.
    hf_cache = tmp_path / "hf-cache"
    hf_cache.mkdir()
    monkeypatch.setenv("HF_HOME", str(hf_cache))

    p = LlamaCppProvider({})
    variant: VariantSpec = {
        "id": "tiny",
        "provider": "llamacpp",
        "name": "config.json",
        "repo": "hf-internal-testing/tiny-random-bert",
        "files": ["config.json"],
    }
    progress_lines: list[str] = []

    # If tqdm_kwargs is still being passed, this raises TypeError.
    path = p.download(variant, on_progress=progress_lines.append)
    assert path.endswith("config.json")


def test_pending_changes_forwards_on_progress(tmp_path):
    """apply() must pass on_progress to provider.download()."""
    from modelman.queue import PendingChanges
    from modelman.registry import (
        AuthConfig,
        ModelEntry,
        ProviderEntry,
        Registry,
        save_registry,
    )
    from modelman.state import StateStore

    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    entry = ModelEntry(
        id="x",
        family="ornith",
        provider_id="ollama",
        model_name="x:7b",
    )
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        models=[entry],
    )
    save_registry(reg, reg_path)

    provider = MagicMock()
    provider.name = "ollama"
    provider.download.return_value = "/tmp/new"
    provider.delete.return_value = None

    progress_lines: list[str] = []

    pending = PendingChanges(
        registry=reg,
        state=StateStore(),
        family="ornith",
        registry_path=reg_path,
        state_path=state_path,
        providers={"ollama": provider},
        downloads=[(entry.id, {"id": entry.id, "provider": "ollama", "name": "x:7b"})],
    )
    pending.apply(on_progress=progress_lines.append)

    provider.download.assert_called_once()
    args, kwargs = provider.download.call_args
    assert "on_progress" in kwargs
    assert kwargs["on_progress"] is not None
    assert callable(kwargs["on_progress"])


def test_progress_tqdm_init_does_not_fork_when_stderr_invalid(monkeypatch):
    """Constructing a ProgressTqdm from a worker thread while sys.stderr
    is a Textual-style capture (fileno() == -1) must NOT raise
    'bad value(s) in fds_to_keep'.

    Background: tqdm's default TqdmDefaultWriteLock builds an
    multiprocessing.RLock the first time a bar is instantiated. On
    macOS, that means spawning a tracker subprocess via fork_exec,
    which iterates over inherited file descriptors and bails when
    fd 2 (stderr) is invalid. HuggingFace's _local_folder.pdf_work
    workaround (https://github.com/huggingface/huggingface_hub/issues/4065)
    is to swap the lock for a threading.RLock. We do the same for
    ProgressTqdm so HF downloads work under the model's TUI.
    """
    import sys
    import threading

    class _FakeStderr:
        def write(self, _s):
            return None

        def flush(self):
            return None

        def fileno(self):
            return -1

    real_stderr = sys.stderr
    monkeypatch.setattr(sys, "stderr", _FakeStderr())

    result: list[str] = []

    def worker():
        try:
            inst = ProgressTqdm(total=10)
            inst.close()
            result.append("ok")
        except BaseException as e:
            result.append(f"err: {type(e).__name__}: {e}")

    t = threading.Thread(target=worker)
    t.start()
    t.join(timeout=10)
    monkeypatch.setattr(sys, "stderr", real_stderr)
    assert result == ["ok"], (
        "ProgressTqdm construction in a worker thread with a Textual-"
        "style stderr (fileno=-1) must succeed; got " + repr(result)
    )


def test_progress_tqdm_uses_threading_lock(monkeypatch):
    """After the workaround, ProgressTqdm's write lock should be a
    threading.RLock instance, NOT a TqdmDefaultWriteLock (which would
    attempt to fork a tracker subprocess on first use)."""
    from tqdm.std import TqdmDefaultWriteLock

    lock = ProgressTqdm.get_lock()
    assert not isinstance(lock, TqdmDefaultWriteLock), (
        "ProgressTqdm must not use TqdmDefaultWriteLock; that path "
        "spawns a fork_exec subprocess that fails under Textual stderr"
    )
    # threading.RLock() returns an instance whose class is _thread.RLock
    # in CPython; just check it is callable and not the default.
    assert callable(getattr(lock, "acquire", None))
    assert callable(getattr(lock, "release", None))


def test_progress_tqdm_suppresses_zero_zero_lines():
    """A bar that is 0/0 (cache-hit scenario) must NOT spam the
    on_progress log with useless '0 B / 0 B' lines. The user already
    saw the bar in their terminal; we don't need to mirror that
    noise in our log."""
    import io

    seen: list[str] = []

    bar = ProgressTqdm(
        total=0,
        desc="Downloading bytes",
        unit="B",
        on_progress=seen.append,
        file=io.StringIO(),
    )
    # Multiple display ticks at zero state.
    bar.refresh()
    bar.refresh()
    bar.refresh()
    assert seen == [], f"expected no progress lines for 0/0 bar; got {seen!r}"

    # Now the bar actually has data.
    bar.total = 1024 * 1024
    bar.update(512 * 1024)
    bar.refresh()
    assert seen and any("Downloading bytes" in line and "KB" in line for line in seen), seen

    bar.close()


def test_progress_tqdm_close_marks_cached_when_n_zero():
    """If a bar starts at 0 and never has bytes added during this
    snapshot_download call, close() should emit a 'cached, no fetch'
    marker instead of a generic 'done', so users can tell their
    re-trigger of a download after a failure wasn't actually a
    network attempt (vs. a silent resume that completed)."""
    import io

    seen: list[str] = []

    bar = ProgressTqdm(
        total=0,
        desc="Fetching 1 files",
        unit="files",
        on_progress=seen.append,
        file=io.StringIO(),
    )
    bar.refresh()
    bar.close()

    # No zero-noise lines; close() says "cached".
    assert not any("Fetching 1 files: 0" in line for line in seen)
    assert any("cached" in line.lower() for line in seen), seen


def test_progress_tqdm_close_done_when_n_positive():
    """The happy-path bar (started at total, advanced) closes with
    a 'done' line as before."""
    import io

    seen: list[str] = []

    bar = ProgressTqdm(
        total=1024 * 1024,
        desc="weights.bin",
        unit="B",
        on_progress=seen.append,
        file=io.StringIO(),
    )
    bar.update(1024 * 1024)
    bar.close()

    # Normal close path: a 'done' line, not a 'cached' line.
    assert any("done" in line.lower() for line in seen)
    assert not any("cached" in line.lower() for line in seen)


def test_progress_tqdm_uses_unit_field_not_hardcoded_bytes():
    """Bars that count things other than bytes (e.g. 'files') must
    not format the count as 'X B'. Byte bars (unit='B') still use
    human_bytes; everything else uses integer counts since the bar's
    description already names what's being counted."""
    import io

    seen: list[str] = []

    bar = ProgressTqdm(
        total=3,
        desc="Fetching 3 files",
        unit="files",
        on_progress=seen.append,
        file=io.StringIO(),
    )
    bar.update(1)
    bar.refresh()
    bar.update(1)  # now 2/3
    bar.refresh()
    bar.close()

    # Integer counts, no spurious B suffix.
    assert any("Fetching 3 files: 1 / 3" in line for line in seen), seen
    assert any("Fetching 3 files: 2 / 3" in line for line in seen), seen
    # No byte-style formatting leaks in.
    for line in seen:
        assert "1 / 3 B" not in line
        assert "1.0 B" not in line
        assert "2.0 B" not in line
