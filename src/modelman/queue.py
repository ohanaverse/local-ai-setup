"""In-memory change queue applied on exit of the TUI model screen."""

from __future__ import annotations

import contextlib
from collections.abc import Callable
from dataclasses import dataclass, field
from pathlib import Path
from typing import TYPE_CHECKING

from .manifest import save_manifest
from .providers._progress import DownloadCancelled

if TYPE_CHECKING:
    from .manifest import FamilyManifest
    from .providers.base import VariantSpec


# Event tags fired via the optional on_event callback during apply(). The
# StatusScreen consumes these to render live progress.
#
# Tags are pipe-delimited:
#   "verb:status|vid|label" for per-item events,
#   "verb:status" for global events (save:*, apply:*),
#   "verb:status|vid|label|reason" for per-item failures (carries the
#     first line of the exception so the StatusScreen can show WHY it
#     failed, not just THAT it failed),
#   "verb:status|reason" for global failures.
EventFn = Callable[[str], None]


def _label(variant: VariantSpec) -> str:
    """A short, human-readable label for a variant in progress logs.

    Falls back to the variant id if no name is set.
    """
    name = variant.get("name")
    return name if isinstance(name, str) and name else variant["id"]


def _reason(exc: BaseException) -> str:
    """First line of an exception, capped so a giant traceback doesn't
    drown the StatusScreen. Newlines inside the message would otherwise
    confuse the pipe-delimited tag format; take only the first line.
    """
    text = str(exc) or exc.__class__.__name__
    first = text.splitlines()[0] if text else ""
    if len(first) > 200:
        first = first[:197] + "…"
    return first


@dataclass
class PendingChanges:
    manifest: FamilyManifest
    manifest_path: Path
    providers: dict[str, object]
    downloads: list[VariantSpec] = field(default_factory=list)
    deletes: list[VariantSpec] = field(default_factory=list)
    failures: list[str] = field(default_factory=list)
    cancelled: bool = False

    def cancel(self) -> None:
        """Request cancellation of an in-progress apply().

        Sets the flag the apply loop checks between steps, and terminates
        any provider that exposes a cancel_current() hook (e.g. Ollama's
        tracked subprocess). Safe to call from any thread.

        Already-completed steps are NOT undone. The current step, if any,
        is killed; remaining steps are not started.
        """
        self.cancelled = True
        for provider in self.providers.values():
            cancel = getattr(provider, "cancel_current", None)
            if callable(cancel):
                with contextlib.suppress(Exception):
                    cancel()

    def apply(
        self,
        on_event: EventFn | None = None,
        on_progress: EventFn | None = None,
    ) -> None:
        """Apply deletes first, then downloads, then save the manifest once.

        On failure of any single step, capture it in self.failures and continue
        with the remaining steps. If `on_event` is provided, it is called for
        each lifecycle transition (start/done/fail) and at apply:done.

        If `on_progress` is provided, it is forwarded to each provider's
        download method as a line-emitting callback. Providers stream their
        native progress (ollama subprocess output, huggingface_hub tqdm
        bars) through it. Progress callbacks fire from worker threads;
        the StatusScreen marshals to the UI thread.

        If `self.cancelled` is set, the loop stops between steps; already-
        completed steps remain applied, the manifest is not saved, and the
        run ends with apply:cancelled.
        """

        def emit(tag: str) -> None:
            if on_event is not None:
                on_event(tag)

        def aborted() -> bool:
            if self.cancelled:
                emit("apply:cancelled")
                return True
            return False

        if not self.downloads and not self.deletes:
            emit("apply:done")
            return

        for variant in self.deletes:
            if aborted():
                return
            vid = variant["id"]
            label = _label(variant)
            emit(f"delete:start|{vid}|{label}")
            try:
                self._delete(variant)
            except Exception as exc:  # noqa: BLE001
                reason = _reason(exc)
                self.failures.append(f"delete {vid}: {exc}")
                emit(f"delete:fail|{vid}|{label}|{reason}")
                continue
            self.manifest.variants = [v for v in self.manifest.variants if v["id"] != vid]
            self.manifest.downloaded.pop(vid, None)
            emit(f"delete:done|{vid}|{label}")

        for variant in self.downloads:
            if aborted():
                return
            vid = variant["id"]
            label = _label(variant)
            emit(f"download:start|{vid}|{label}")
            try:
                local_path = self._download(variant, on_progress)
            except DownloadCancelled:
                # User pressed Cancel mid-download. Bubble out of the
                # loop; do not record this as a failure (the user
                # intended to abort).
                emit(f"download:cancelled|{vid}|{label}")
                emit("apply:cancelled")
                return
            except Exception as exc:  # noqa: BLE001
                reason = _reason(exc)
                self.failures.append(f"download {vid}: {exc}")
                emit(f"download:fail|{vid}|{label}|{reason}")
                continue
            self.manifest.mark_downloaded(vid, local_path)
            # Show the actual on-disk size in the success line so the
            # user has direct, trustworthy confirmation that the
            # download landed (a 0-byte "Downloaded" message looks
            # like an empty file; a "21.7 GB" message looks real).
            try:
                size = Path(local_path).stat().st_size
            except OSError:
                size = 0
            if size > 0:
                from .providers._progress import human_bytes
                emit(f"download:done|{vid}|{label}|{human_bytes(size)}")
            else:
                emit(f"download:done|{vid}|{label}")

        if aborted():
            return

        emit("save:start")
        try:
            save_manifest(self.manifest, self.manifest_path)
            emit("save:done")
        except Exception as exc:  # noqa: BLE001
            reason = _reason(exc)
            self.failures.append(f"save: {exc}")
            emit(f"save:fail|{reason}")

        emit("apply:done")

    def _download(self, variant: VariantSpec, on_progress: EventFn | None = None) -> str:
        provider = self.providers[variant["provider"]]
        # Providers expose an optional on_progress kwarg; if not accepted
        # (e.g. test stubs), fall back to the plain download() signature.
        try:
            return provider.download(variant, on_progress=on_progress)  # type: ignore[attr-defined]
        except TypeError:
            return provider.download(variant)  # type: ignore[attr-defined]

    def _delete(self, variant: VariantSpec) -> None:
        provider = self.providers[variant["provider"]]
        if hasattr(provider, "delete"):
            provider.delete(variant)  # type: ignore[attr-defined]
            return
        local_path = self.manifest.downloaded.get(variant["id"], {}).get("local_path")
        if local_path:
            from pathlib import Path as _P

            p = _P(local_path)
            if p.is_file():
                p.unlink()
            elif p.is_dir():
                # Fallback: only remove if empty. Providers that share directories
                # (like oMLX) must implement their own delete() to be safe.
                import os
                if not os.listdir(p):
                    import shutil
                    shutil.rmtree(p)
