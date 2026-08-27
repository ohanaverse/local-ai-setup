"""Shared progress-callback helpers for provider downloads.

Providers expose an `on_progress` callable that streams human-readable
lines describing what the underlying tool (ollama, huggingface_hub, ...)
is doing. The StatusScreen consumes these and writes them into a RichLog.

`on_progress` may be invoked from any thread; the StatusScreen is
responsible for marshalling to the UI thread.

For HuggingFace downloads, `snapshot_download` runs synchronously and
does not natively support cancellation. To make it interruptible, the
`ProgressTqdm` bar accepts an optional `should_cancel` callable; if it
returns True on any `display()` update, the bar raises
`DownloadCancelled`, which bubbles out of `snapshot_download` and out
of the apply loop. This is what makes the StatusScreen's Cancel button
actually stop a HF download instead of waiting for it to finish.
"""

from __future__ import annotations

import contextlib
import threading
from collections.abc import Callable

from tqdm.auto import tqdm as _tqdm

_MISSING = object()


def human_bytes(n: float) -> str:
    """Format a byte count as a short human-readable string."""
    units = ("B", "KB", "MB", "GB", "TB")
    for unit in units:
        if n < 1024 or unit == units[-1]:
            return f"{n:.1f} {unit}" if unit != "B" else f"{int(n)} {unit}"
        n /= 1024
    return f"{n:.1f} TB"


class DownloadCancelled(Exception):
    """Raised by ProgressTqdm when `should_cancel` returns True.

    Caught by `PendingChanges.apply()` and treated as a cancellation,
    not a download failure.
    """


class ProgressTqdm(_tqdm):
    """tqdm subclass that fires a callback on each display update.

    Used to stream huggingface_hub snapshot_download progress into the
    StatusScreen log. Multiple bars may run in parallel; the callback
    fires for each one independently. If `should_cancel` is provided and
    ever returns True, the bar raises `DownloadCancelled` to abort the
    download immediately (snapshot_download propagates the exception).

    The huggingface_hub package does not let callers pass kwargs to the
    user-supplied `tqdm_class`; it just calls `cls(disable=..., name=...,
    **other_kwargs)`. So we can't plumb `on_progress`/`should_cancel`
    through the constructor directly. Instead, the calling provider sets
    a class-level "active context" before calling snapshot_download and
    clears it in a finally. Bars created during that call inherit the
    callbacks from the class-level slot.
    """

    # Class-level active context. Set by `set_active_context()` before
    # a huggingface_hub download begins; cleared by `clear_active_context()`
    # after it returns. While set, every ProgressTqdm instance uses it as
    # the default for its on_progress / should_cancel hooks.
    _active_on_progress: Callable[[str], None] | None = None
    _active_should_cancel: Callable[[], bool] | None = None

    # Instance-level overrides. The parent class's __init__ calls
    # self.refresh() -> self.display() before our __init__ body finishes
    # assigning attributes, so both class-level and instance-level slots
    # are pre-populated.
    _on_progress: Callable[[str], None] | None = None
    _should_cancel: Callable[[], bool] | None = None

    @classmethod
    def set_active_context(
        cls,
        on_progress: Callable[[str], None] | None,
        should_cancel: Callable[[], bool] | None,
    ) -> None:
        cls._active_on_progress = on_progress
        cls._active_should_cancel = should_cancel

    @classmethod
    def clear_active_context(cls) -> None:
        cls._active_on_progress = None
        cls._active_should_cancel = None

    def __init__(  # type: ignore[no-untyped-def]
        self,
        *args: object,
        on_progress: Callable[[str], None] | None = None,
        should_cancel: Callable[[], bool] | None = None,
        **kwargs: object,
    ):
        # Use explicit kwargs if provided. Otherwise fall back to the
        # class-level active context (the huggingface_hub path). We store
        # the callbacks on the instance only when the caller provided them
        # explicitly; otherwise `display()` reads from the class-level
        # active context. This avoids Python turning the stored lambda
        # into a bound method.
        if on_progress is not None:
            self._on_progress = on_progress
        if should_cancel is not None:
            self._should_cancel = should_cancel
        super().__init__(*args, **kwargs)

    def _active_on_progress_or_default(self) -> Callable[[str], None] | None:
        # Access via type(self) (not self) so a class-level lambda is
        # retrieved as a plain function instead of a bound method.
        instance_value = self.__dict__.get("_on_progress", _MISSING)
        if instance_value is not _MISSING:
            return instance_value  # type: ignore[return-value]
        return type(self).__dict__.get("_active_on_progress")

    def _active_should_cancel_or_default(self) -> Callable[[], bool] | None:
        instance_value = self.__dict__.get("_should_cancel", _MISSING)
        if instance_value is not _MISSING:
            return instance_value  # type: ignore[return-value]
        return type(self).__dict__.get("_active_should_cancel")

    def display(self, msg=None, pos=None):  # type: ignore[override]
        # Cancellation check first: if the user pressed Cancel, abort the
        # bar immediately rather than formatting progress that will be
        # discarded anyway.
        sc = self._active_should_cancel_or_default()
        if sc is not None:
            try:
                if sc():
                    raise DownloadCancelled(self.desc or "file")
            except DownloadCancelled:
                raise
            except Exception:
                # Cancellation predicate itself failed; treat as no-cancel
                # rather than crashing the bar.
                pass
        super().display(msg, pos)
        cb = self._active_on_progress_or_default()
        if cb is None:
            return
        fmt = self.format_dict
        total = fmt.get("total") or 0
        n = fmt.get("n") or 0
        rate = fmt.get("rate") or 0
        desc = fmt.get("desc") or self.desc or "file"
        unit = self.unit or ""

        # Suppress zero-of-zero noise: huggingface_hub emits initial /
        # synthetic bars (e.g. "Downloading bytes: 0 B / 0 B" or
        # "Fetching 1 files: 0 / 1") before anything has actually
        # happened. Mirroring those into our log just makes the user
        # think their download produced no bytes. Skip until the bar
        # has something to say.
        if total == 0 and n == 0:
            return
        # Per-instance flag tracks whether this bar ever had real
        # progress; close() reads it to decide between "done" and
        # "already cached".
        self._emitted_progress = True  # type: ignore[attr-defined]

        # Format n / total respecting the bar's unit field. For byte
        # bars we use human_bytes. For other units (HF uses "files"
        # for the per-file counter, or "" for synthetic aggregate
        # bars) we keep the integer count; the bar's description
        # already names the thing ("Fetching 3 files" vs "Downloading
        # bytes") so adding unit again would be redundant.
        def fmt_count(value: float) -> str:
            if unit == "B":
                return human_bytes(value)
            return str(int(value))

        if total:
            pct = n / total * 100
            rate_str = (
                f" | {human_bytes(rate)}/s"
                if unit == "B" and rate
                else (f" | {rate:.1f}/s" if rate else "")
            )
            line = f"{desc}: {fmt_count(n)} / {fmt_count(total)} ({pct:.0f}%){rate_str}"
        else:
            line = f"{desc}: {fmt_count(n)}"
        with contextlib.suppress(Exception):
            cb(line)

    def close(self) -> None:  # type: ignore[override]
        cb = self._active_on_progress_or_default()
        if cb is not None:
            desc = self.desc or "file"
            # If this bar existed but never produced a progress line,
            # it's a synthetic / cache-hit bar: every byte was already
            # on disk before this run. Say so explicitly so the user
            # knows the "Downloaded X" success marker reflects an
            # existing file, not zero bytes freshly fetched.
            if not getattr(self, "_emitted_progress", False):
                with contextlib.suppress(Exception):
                    cb(f"{desc}: already cached, no bytes fetched")
            else:
                with contextlib.suppress(Exception):
                    cb(f"{desc}: done")
        super().close()


# Replace ProgressTqdm's default write lock with a pure-threading
# RLock so the first bar construction doesn't try to fork a
# multiprocessing resource tracker. The default TqdmDefaultWriteLock
# builds an multiprocessing.RLock, which on macOS spawns a tracker
# subprocess via fork_exec() that iterates over inherited file
# descriptors and raises ValueError("bad value(s) in fds_to_keep")
# when stderr's fileno is invalid (which happens under a Textual
# TUI: sys.stderr is replaced by a capture object whose fileno()
# returns -1). HuggingFace hits the same issue and applies the same
# workaround on its own tqdm subclass; we mirror it here for
# ProgressTqdm because our workers construct bars in a non-main
# thread where stderr is still the TUI's capture.
ProgressTqdm.set_lock(threading.RLock())
