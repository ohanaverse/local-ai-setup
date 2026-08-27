"""In-memory change queue applied on exit of the TUI model screen.

The on-disk targets are registry.toml (canonical model/provider definitions)
and modelman.toml (per-machine mutable state: download markers, family
display names). The legacy families/<family>.yaml manifest is no longer
written by the TUI; it survives as a migrate-time input only.
"""

from __future__ import annotations

import contextlib
from collections.abc import Callable
from dataclasses import dataclass, field
from pathlib import Path
from typing import TYPE_CHECKING

from .providers._progress import DownloadCancelled
from .registry import save_registry
from .state import ModelState, save_state

if TYPE_CHECKING:
    from .providers.base import VariantSpec
    from .registry import Registry
    from .state import StateStore


# Event tags fired via the optional on_event callback during apply(). The
# StatusScreen consumes these to render live progress. Format is unchanged
# from the legacy FamilyManifest-based implementation so StatusScreen can
# keep consuming pipe-delimited tags without modification:
#   "verb:status|vid|label" for per-item events,
#   "verb:status" for global events (save:*, apply:*),
#   "verb:status|vid|label|reason" for per-item failures,
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
    drown the StatusScreen."""
    text = str(exc) or exc.__class__.__name__
    first = text.splitlines()[0] if text else ""
    if len(first) > 200:
        first = first[:197] + "…"
    return first


@dataclass
class PendingChanges:
    registry: Registry
    state: StateStore
    family: str
    registry_path: Path
    state_path: Path
    providers: dict[str, object]
    # Each queued item carries (model_id, VariantSpec). model_id is the
    # ModelEntry.id used to key Registry/StateStore mutations; VariantSpec
    # is what the provider APIs (download, size_of, delete) still consume.
    downloads: list[tuple[str, VariantSpec]] = field(default_factory=list)
    deletes: list[tuple[str, VariantSpec]] = field(default_factory=list)
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
        """Apply deletes first, then downloads, then save registry+state once.

        On failure of any single step, capture it in self.failures and continue
        with the remaining steps. If `on_event` is provided, it is called for
        each lifecycle transition (start/done/fail) and at apply:done.

        If `on_progress` is provided, it is forwarded to each provider's
        download method as a line-emitting callback.

        If `self.cancelled` is set, the loop stops between steps; already-
        completed steps remain applied, the registry/state are not saved,
        and the run ends with apply:cancelled.
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

        for model_id, variant in self.deletes:
            if aborted():
                return
            assert variant["id"] == model_id, (
                f"variant id {variant['id']!r} != queued model_id {model_id!r}"
            )
            label = _label(variant)
            emit(f"delete:start|{model_id}|{label}")
            try:
                self._delete(variant)
            except Exception as exc:  # noqa: BLE001
                reason = _reason(exc)
                self.failures.append(f"delete {model_id}: {exc}")
                emit(f"delete:fail|{model_id}|{label}|{reason}")
                continue
            # Remove from in-memory registry.
            self.registry.models = [m for m in self.registry.models if m.id != model_id]
            # Clear any state entry so modelman.toml doesn't carry a
            # stale downloaded=True after the user removed the file.
            self.state.models.pop(model_id, None)
            emit(f"delete:done|{model_id}|{label}")

        for model_id, variant in self.downloads:
            if aborted():
                return
            assert variant["id"] == model_id, (
                f"variant id {variant['id']!r} != queued model_id {model_id!r}"
            )
            label = _label(variant)
            emit(f"download:start|{model_id}|{label}")
            try:
                local_path = self._download(variant, on_progress)
            except DownloadCancelled:
                emit(f"download:cancelled|{model_id}|{label}")
                emit("apply:cancelled")
                return
            except Exception as exc:  # noqa: BLE001
                reason = _reason(exc)
                self.failures.append(f"download {model_id}: {exc}")
                emit(f"download:fail|{model_id}|{label}|{reason}")
                continue
            # Record download in state.
            existing = self.state.get(model_id)
            self.state.set(
                model_id,
                ModelState(
                    downloaded=True,
                    disk_path=local_path,
                    size_bytes=existing.size_bytes,
                    litellm_exposed=existing.litellm_exposed,
                ),
            )
            try:
                size = Path(local_path).stat().st_size
            except OSError:
                size = 0
            if size > 0:
                from .providers._progress import human_bytes
                emit(f"download:done|{model_id}|{label}|{human_bytes(size)}")
            else:
                emit(f"download:done|{model_id}|{label}")

        if aborted():
            return

        emit("save:start")
        try:
            save_registry(self.registry, self.registry_path)
            save_state(self.state, self.state_path)
            emit("save:done")
        except Exception as exc:  # noqa: BLE001
            reason = _reason(exc)
            self.failures.append(f"save: {exc}")
            emit(f"save:fail|{reason}")

        emit("apply:done")

    def _download(self, variant: VariantSpec, on_progress: EventFn | None = None) -> str:
        provider = self.providers[variant["provider"]]
        try:
            return provider.download(variant, on_progress=on_progress)  # type: ignore[attr-defined]
        except TypeError:
            return provider.download(variant)  # type: ignore[attr-defined]

    def _delete(self, variant: VariantSpec) -> None:
        provider = self.providers[variant["provider"]]
        if hasattr(provider, "delete"):
            provider.delete(variant)  # type: ignore[attr-defined]
            return
        # Fallback: providers without delete() just unlink the file.
        # Locate the local path from state (the legacy code read
        # self.manifest.downloaded[vid]["local_path"]; the equivalent
        # here is state.models[vid].disk_path).
        import os
        import shutil
        from pathlib import Path as _P

        local_path = self.state.get(variant["id"]).disk_path
        if local_path:
            p = _P(local_path)
            if p.is_file():
                p.unlink()
            elif p.is_dir() and not os.listdir(p):
                shutil.rmtree(p)
