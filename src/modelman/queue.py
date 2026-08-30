"""In-memory change queue applied on exit of the TUI model screen.

The on-disk targets are registry.toml (canonical model/provider/family
definitions) and modelman.toml (per-machine mutable state: download
markers). Queued family moves mutate registry.toml only. The
legacy families/<family>.yaml manifest is no longer
written by the TUI; it survives as a migrate-time input only.
"""

from __future__ import annotations

import contextlib
from collections.abc import Callable
from dataclasses import dataclass, field, replace
from pathlib import Path
from typing import TYPE_CHECKING

from .litellm import apply_expose_queue
from .providers._progress import DownloadCancelled
from .registry import FamilyEntry, save_registry
from .state import save_state

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


def _sanitize(text: str) -> str:
    """Strip the event-tag delimiter from user- or exception-controlled text.

    Tags are pipe-delimited ("verb:status|field|field"); a literal '|'
    in a field would shift the split in StatusScreen._handle_event and
    corrupt the fields after it.
    """
    return text.replace("|", "/")


def _label(variant: VariantSpec) -> str:
    """A short, human-readable label for a variant in progress logs.

    Falls back to the variant id if no name is set.
    """
    name = variant.get("name")
    return _sanitize(name if isinstance(name, str) and name else variant["id"])


def _reason(exc: BaseException) -> str:
    """First line of an exception, capped so a giant traceback doesn't
    drown the StatusScreen."""
    text = str(exc) or exc.__class__.__name__
    first = text.splitlines()[0] if text else ""
    if len(first) > 200:
        first = first[:197] + "…"
    return _sanitize(first)


@dataclass
class PendingChanges:
    registry: Registry
    state: StateStore
    family: str
    registry_path: Path
    state_path: Path
    providers: dict[str, object]
    # Each queued item carries (model_id, VariantSpec, target_ready). For a
    # provider present in `providers`, target=True downloads / target=False
    # clears (rm) without touching the registry entry. For a provider absent
    # from `providers` (flag-only: native or unmapped), either target just
    # flips state.ready — no provider call is made.
    ready: list[tuple[str, VariantSpec, bool]] = field(default_factory=list)
    deletes: list[tuple[str, VariantSpec]] = field(default_factory=list)
    # (model_id, target_exposed) pairs applied after downloads, before save.
    exposes: list[tuple[str, bool]] = field(default_factory=list)
    # (model_id, new_family) pairs. Pure registry metadata: applies right
    # after deletes (a same-apply delete wins; its queued move is moot),
    # needs no provider interaction. A move-only queue still triggers the
    # final save.
    moves: list[tuple[str, str]] = field(default_factory=list)
    litellm_path: Path = field(default_factory=Path)
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
        """Apply deletes first, then moves, then downloads, then exposes,
        then save registry+state once.

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

        if not self.ready and not self.deletes and not self.exposes and not self.moves:
            emit("apply:done")
            return

        # ids removed by this apply — moves referencing them are moot
        deleted_ids: set[str] = set()
        # Families whose membership changed this apply (a model deleted or
        # moved out). After all membership changes, any of these that now
        # has zero models lingers as a first-class [[families]] entry so it
        # stays visible until explicitly deleted (stickiness).
        recorded_families: set[str] = set()
        for model_id, variant in self.deletes:
            if aborted():
                return
            assert variant["id"] == model_id, (
                f"variant id {variant['id']!r} != queued model_id {model_id!r}"
            )
            label = _label(variant)
            provider_id = variant["provider"]
            provider = self.providers.get(provider_id)
            if provider is not None:
                # Reconcilable provider: ask it to remove the on-disk artifact.
                emit(f"delete:start|{model_id}|{label}")
                try:
                    self._delete(variant)
                except Exception as exc:  # noqa: BLE001
                    reason = _reason(exc)
                    self.failures.append(f"delete {model_id}: {exc}")
                    emit(f"delete:fail|{model_id}|{label}|{reason}")
                    continue
            # Flag-only providers (native/unmapped) have no Provider instance
            # and no on-disk artifact to delete; just remove registry/state.
            # Record the model's family before removing the entry, so a
            # family emptied by this delete lingers (stickiness). A failed
            # delete records nothing — the model stays.
            with contextlib.suppress(KeyError):
                recorded_families.add(self.registry.model(model_id).family)
            # Remove from in-memory registry.
            self.registry.models = [m for m in self.registry.models if m.id != model_id]
            # If the model was exposed through LiteLLM, queue an unexpose
            # so config.yaml doesn't keep routing to a model whose file
            # is gone. Any queued expose toggle for the same id is moot
            # now that the model is being removed.
            was_exposed = self.state.get(model_id).litellm_exposed
            self.exposes = [(mid, t) for mid, t in self.exposes if mid != model_id]
            if was_exposed:
                self.exposes.append((model_id, False))
            # Clear any state entry so modelman.toml doesn't carry a
            # stale downloaded=True after the user removed the file.
            self.state.models.pop(model_id, None)
            emit(f"delete:done|{model_id}|{label}")
            deleted_ids.add(model_id)

        for model_id, new_family in self.moves:
            if aborted():
                return
            try:
                entry = self.registry.model(model_id)
            except KeyError:
                if model_id in deleted_ids:
                    # Deleted earlier in this apply; its move is moot.
                    continue
                self.failures.append(f"move {model_id}: Unknown model: {model_id}")
                emit(f"move:fail|{model_id}|{model_id}|Unknown model")
                continue
            move_label = _sanitize(entry.model_name)
            move_family = _sanitize(new_family)
            emit(f"move:start|{model_id}|{move_label}|{move_family}")
            # Record the source family before reassignment (stickiness).
            recorded_families.add(entry.family)
            entry.family = new_family
            emit(f"move:done|{model_id}|{move_label}|{move_family}")

        # Stickiness: a family that had models and now has none (emptied by
        # a delete or move in this apply) lingers as a first-class
        # [[families]] entry so it stays visible until explicitly deleted.
        # Runs after all membership changes (deletes + moves) so a family
        # that gained a model in the same apply is not touched.
        for f in recorded_families:
            if self.registry.models_by_family(f):
                continue
            family_entry = self.registry.family(f)
            legacy = self.state.families.get(f)
            if family_entry is None:
                self.registry.families.append(
                    FamilyEntry(name=f, display_name=legacy.display_name if legacy else None)
                )
            elif family_entry.display_name is None and legacy is not None and legacy.display_name:
                family_entry.display_name = legacy.display_name
            self.state.forget_family(f)

        for model_id, variant, target in self.ready:
            if aborted():
                return
            assert variant["id"] == model_id, (
                f"variant id {variant['id']!r} != queued model_id {model_id!r}"
            )
            label = _label(variant)
            provider_id = variant["provider"]
            provider = self.providers.get(provider_id)
            if provider is None:
                # Flag-only provider (native or unmapped): no download/delete
                # mechanics exist. Just flip the flag.
                emit(f"ready:start|{model_id}|{label}")
                self.state.set(model_id, replace(self.state.get(model_id), ready=target))
                emit(f"ready:done|{model_id}|{label}")
            elif target:
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
                self.state.set(
                    model_id,
                    replace(self.state.get(model_id), ready=True, disk_path=local_path),
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
            else:
                emit(f"delete:start|{model_id}|{label}")
                try:
                    self._delete(variant)
                except Exception as exc:  # noqa: BLE001
                    reason = _reason(exc)
                    self.failures.append(f"clear {model_id}: {exc}")
                    emit(f"delete:fail|{model_id}|{label}|{reason}")
                    continue
                # Unlike the full-delete step above, the ModelEntry stays in
                # the registry — only its ready state and on-disk artifact
                # are cleared. Wipe the cached path/size so modelman.toml
                # doesn't keep pointing at a file that was just removed.
                self.state.set(
                    model_id,
                    replace(
                        self.state.get(model_id),
                        ready=False,
                        disk_path=None,
                        size_bytes=None,
                    ),
                )
                emit(f"delete:done|{model_id}|{label}")
            # Cascade: turning ready off (either branch) drops the model's
            # exposure — mirrors the full-delete step's was_exposed rule.
            # Flag-only providers have no LiteLLM config row, so just flip
            # the state flag directly instead of routing through the
            # config writer.
            if not target and self.state.get(model_id).litellm_exposed:
                provider = self.providers.get(provider_id)
                self.exposes = [(mid, t) for mid, t in self.exposes if mid != model_id]
                if provider is None:
                    self.state.set(
                        model_id, replace(self.state.get(model_id), litellm_exposed=False)
                    )
                else:
                    self.exposes.append((model_id, False))

        if aborted():
            return

        if self.exposes:
            # One config load + one atomic save for the whole queue:
            # per-model expose_model calls would reparse and re-rename
            # config.yaml once per model, and a crash between them would
            # leave it half-updated.
            for model_id, exposed in self.exposes:
                verb = "expose" if exposed else "unexpose"
                emit(f"{verb}:start|{model_id}|{model_id}")
            try:
                outcomes = apply_expose_queue(
                    self.registry, self.state, self.exposes, self.litellm_path
                )
            except Exception as exc:  # noqa: BLE001
                # Config-level failure (missing/unwritable config.yaml):
                # nothing in the queue applied.
                reason = _reason(exc)
                self.failures.append(f"expose batch: {exc}")
                for model_id, exposed in self.exposes:
                    verb = "expose" if exposed else "unexpose"
                    emit(f"{verb}:fail|{model_id}|{model_id}|{reason}")
            else:
                for model_id, exposed, error in outcomes:
                    verb = "expose" if exposed else "unexpose"
                    if error is None:
                        emit(f"{verb}:done|{model_id}|{model_id}")
                    else:
                        self.failures.append(f"{verb} {model_id}: {error}")
                        emit(f"{verb}:fail|{model_id}|{model_id}|{error}")

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
