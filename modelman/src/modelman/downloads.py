"""DownloadManager — background model downloads, independent of the
apply-on-exit queue in queue.py.

Owned by ModelmanApp (created once, survives screen navigation:
self.app.downloads). Each download runs on its own daemon thread against
a fresh provider instance (ProviderRegistry.get()), so per-download
cancellation state (Ollama's _current_proc, oMLX/llamacpp's
_cancel_requested) never crosses between simultaneous downloads.
"""

from __future__ import annotations

import contextlib
import threading
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
from typing import TYPE_CHECKING, Literal

from .providers._progress import DownloadCancelled
from .providers.registry import ProviderRegistry
from .registry import find_shared_artifact_owner
from .state import ModelState, locked_state

if TYPE_CHECKING:
    from .providers.base import Provider, VariantSpec
    from .registry import Registry

Status = Literal["downloading", "done", "failed", "cancelled"]


@dataclass
class DownloadState:
    """Snapshot of one download for the UI (DownloadScreen, ModelScreen's
    STATUS glyph, the quit guard). `progress` is the latest raw line
    forwarded from the provider's on_progress callback."""

    model_id: str
    variant_id: str
    provider: str
    status: Status
    progress: str = ""
    error: str | None = None


class DownloadManager:
    def __init__(self, app: object) -> None:
        self._app = app
        self._lock = threading.Lock()
        self._states: dict[str, DownloadState] = {}
        self._providers: dict[str, Provider] = {}
        self._variants: dict[str, VariantSpec] = {}
        self._cancel_requested: set[str] = set()
        self._post_download: dict[str, list[Callable[[], None]]] = {}
        # Registry snapshot for a download in flight, used only by the
        # cancel/fail cleanup path (_finish) to check whether another
        # registry entry shares this variant's on-disk target before
        # deleting it. None when the caller didn't pass one (e.g. tests).
        self._registries: dict[str, Registry | None] = {}

    def start(
        self,
        model_id: str,
        variant: VariantSpec,
        provider_config: dict,
        on_complete: Callable[[str], None] | None = None,
        registry: Registry | None = None,
    ) -> None:
        with self._lock:
            if model_id in self._states and self._states[model_id].status == "downloading":
                return  # already in flight; no-op (guards double-keypress races)
            provider = ProviderRegistry.get(variant["provider"], provider_config)
            self._providers[model_id] = provider
            self._variants[model_id] = variant
            self._registries[model_id] = registry
            self._cancel_requested.discard(model_id)
            self._states[model_id] = DownloadState(
                model_id=model_id,
                variant_id=variant["id"],
                provider=variant["provider"],
                status="downloading",
            )
        thread = threading.Thread(
            target=self._run, args=(model_id, variant, provider, on_complete), daemon=True
        )
        thread.start()

    def cancel(self, model_id: str) -> None:
        with self._lock:
            self._cancel_requested.add(model_id)
            provider = self._providers.get(model_id)
        if provider is None:
            return
        cancel_fn = getattr(provider, "cancel_current", None)
        if callable(cancel_fn):
            cancel_fn()

    def clear_state(self, model_id: str) -> None:
        """Remove a download's state from tracking. Used when discarding
        changes to prevent cancelled/finished downloads from persisting in
        the DownloadScreen."""
        with self._lock:
            self._states.pop(model_id, None)
            self._providers.pop(model_id, None)
            self._variants.pop(model_id, None)
            self._registries.pop(model_id, None)
            self._cancel_requested.discard(model_id)
            self._post_download.pop(model_id, None)

    def is_downloading(self, model_id: str) -> bool:
        with self._lock:
            state = self._states.get(model_id)
            return state is not None and state.status == "downloading"

    def has_active(self) -> bool:
        with self._lock:
            return any(s.status == "downloading" for s in self._states.values())

    def states(self) -> list[DownloadState]:
        with self._lock:
            return list(self._states.values())

    def clear_finished(self) -> None:
        """Remove all non-downloading states (done/failed/cancelled) from
        tracking. Useful for cleaning up the DownloadScreen without losing
        visibility into active downloads."""
        with self._lock:
            to_remove = [
                mid for mid, state in self._states.items() if state.status != "downloading"
            ]
            for model_id in to_remove:
                self._states.pop(model_id, None)
                self._providers.pop(model_id, None)
                self._variants.pop(model_id, None)
                self._registries.pop(model_id, None)
                self._cancel_requested.discard(model_id)
                self._post_download.pop(model_id, None)

    def register_post_download(self, model_id: str, action: Callable[[], None]) -> None:
        """Run `action` once, only if this model_id's current (or next)
        download finishes successfully. Used to defer a queued expose
        against a model that's still downloading until it's actually
        ready — see ModelScreen._run_apply."""
        with self._lock:
            self._post_download.setdefault(model_id, []).append(action)

    def _run(
        self,
        model_id: str,
        variant: VariantSpec,
        provider: Provider,
        on_complete: Callable[[str], None] | None,
    ) -> None:
        def _on_progress(line: str) -> None:
            with self._lock:
                state = self._states.get(model_id)
                if state is not None:
                    state.progress = line

        try:
            try:
                local_path = provider.download(variant, on_progress=_on_progress)  # type: ignore[call-arg]
            except TypeError:
                local_path = provider.download(variant)
        except Exception as exc:  # noqa: BLE001
            was_cancelled = model_id in self._cancel_requested
            # A cancelled provider can raise DownloadCancelled (oMLX/
            # llamacpp, via ProgressTqdm's should_cancel) or a generic
            # exception (Ollama's killed subprocess exits non-zero, which
            # download() reports as RuntimeError, not DownloadCancelled).
            # Either way, if cancel() was called for this model_id, the
            # user asked for this — classify it as cancelled, not failed.
            if was_cancelled or isinstance(exc, DownloadCancelled):
                self._finish(model_id, "cancelled", cleanup=(provider, variant))
            else:
                self._finish(model_id, "failed", error=str(exc) or exc.__class__.__name__)
            return

        size_bytes = self._size_of(local_path)
        # Persist via locked_state (merge onto fresh disk state). A failed
        # write must not wedge the thread (and this model) in "downloading"
        # forever — that would block the quit guard — and it self-heals:
        # reconcile re-derives ready/disk_path from the artifact on disk on
        # the next mount.
        with contextlib.suppress(Exception), locked_state() as state:
            state.set(model_id, ModelState(ready=True, disk_path=local_path, size_bytes=size_bytes))
        self._finish(model_id, "done", local_path=local_path)
        if on_complete is not None:
            self._app.call_from_thread(on_complete, local_path)  # type: ignore[attr-defined]

    @staticmethod
    def _size_of(local_path: str) -> int | None:
        # TypeError/ValueError alongside OSError: a non-path object (e.g. a
        # test stub's return value) must not kill the download thread.
        try:
            p = Path(local_path)
            return p.stat().st_size if p.is_file() else None
        except (OSError, TypeError, ValueError):
            return None

    def _finish(
        self,
        model_id: str,
        status: Status,
        *,
        local_path: str | None = None,
        error: str | None = None,
        cleanup: tuple[Provider, VariantSpec] | None = None,
    ) -> None:
        if cleanup is not None:
            provider, variant = cleanup
            with self._lock:
                registry = self._registries.pop(model_id, None)
            conflict = None
            if registry is not None:
                # e.g. two registry entries whose repos share the same
                # omlx basename resolve to one on-disk directory; if the
                # other entry's download already completed there, this
                # cancelled/failed download's cleanup must not rmtree it.
                with contextlib.suppress(Exception):
                    conflict = find_shared_artifact_owner(registry, provider, variant)
            if conflict is None:
                # Best-effort: a failed cleanup must not mask the cancel.
                with contextlib.suppress(Exception):
                    provider.cleanup_partial_download(variant)
        with self._lock:
            state = self._states.get(model_id)
            if state is not None:
                state.status = status
                state.error = error
            self._cancel_requested.discard(model_id)
            self._providers.pop(model_id, None)
            self._registries.pop(model_id, None)
            actions = self._post_download.pop(model_id, [])
        if status == "done":
            for action in actions:
                # A failed deferred action must not crash the download thread.
                with contextlib.suppress(Exception):
                    action()
        # status in ("failed", "cancelled"): actions are discarded,
        # never run — the model never became ready, so an action that
        # assumed it did (e.g. writing a LiteLLM route) would be wrong.
        if status == "failed":
            # Surfaced via notification, not just the DownloadScreen
            # table — the user may be on a completely different screen
            # when a background download fails.
            notify = getattr(self._app, "notify", None)
            if callable(notify):
                self._app.call_from_thread(notify, f"Download failed: {model_id}: {error}")  # type: ignore[attr-defined]
