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
from .providers._progress import DownloadCancelled, human_bytes
from .providers.registry import ProviderRegistry
from .registry import (
    FamilyEntry,
    find_shared_artifact_owner,
    save_registry,
)
from .state import locked_state

if TYPE_CHECKING:
    from .providers.base import VariantSpec
    from .registry import Registry
    from .state import StateStore


# Event tags fired via the optional on_event callback during apply().
# main.py's print_event() renders these as plain terminal lines now that
# apply() runs after the TUI has exited (StatusScreen, the old in-TUI
# renderer, is gone). Format is unchanged from the legacy
# FamilyManifest-based implementation:
#   "verb:status|vid|label" for per-item events,
#   "verb:status" for global events (save:*, apply:*),
#   "verb:status|vid|label|reason" for per-item failures,
#   "verb:status|reason" for global failures.
EventFn = Callable[[str], None]


def _sanitize(text: str) -> str:
    """Strip the event-tag delimiter from user- or exception-controlled text.

    Tags are pipe-delimited ("verb:status|field|field"); a literal '|'
    in a field would shift the split in main.py's print_event() and
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
    """Format exception for display, preserving key details while capping length.

    Shows exception type and full first line for common errors (disk space,
    network, auth). Falls back to truncated message for very long tracebacks.
    """
    exc_type = exc.__class__.__name__
    text = str(exc) or exc_type
    lines = text.splitlines()
    first = lines[0] if lines else ""

    # Preserve full message for common actionable errors. Check the first
    # line only — that is what the user actually sees rendered, and a
    # keyword in a later line would otherwise prefix an unrelated first
    # line. Bare numeric tokens (e.g. "401") are intentionally absent: they
    # collide with errno codes and other unrelated numbers; the descriptive
    # phrases ("unauthorized", "not found") are what real errors include.
    actionable_keywords = [
        "disk space",
        "no space left",
        "ENOSPC",
        "permission denied",
        "EACCES",
        "connection",
        "timeout",
        "network",
        "authentication",
        "unauthorized",
        "not found",
        "certificate",
        "SSL",
    ]
    is_actionable = any(kw.lower() in first.lower() for kw in actionable_keywords)

    if is_actionable:
        # Show type + first line for actionable errors, but skip the
        # prefix when the class name is already in the message.
        if exc_type and exc_type.lower() not in first.lower():
            result = f"{exc_type}: {first}"
        else:
            result = first
    else:
        # Non-actionable: just the first line.
        result = first

    # Cap the final formatted string so it always fits the renderer's
    # 200-char bound (the RichLog has wrap=False, so overflow would
    # cause horizontal scrolling). The ellipsis is included in the cap.
    if len(result) > 200:
        result = result[:197] + "…"

    return _sanitize(result)


def _remove_local_artifact(state: StateStore, variant: VariantSpec) -> None:
    """Best-effort removal of the on-disk artifact recorded in state.

    Used when no provider delete() runs — flag-only providers (native or
    unmapped) on ready-off, or a provider class without a delete
    implementation: unlink the recorded disk_path file (or dangling
    symlink), or an empty directory at that path. A non-empty directory
    is left alone — it may hold content the registry doesn't know about.
    """
    import os
    import shutil

    local_path = state.get(variant["id"]).disk_path
    if not local_path:
        return
    p = Path(local_path)
    if p.is_symlink() or p.is_file():
        p.unlink()
    elif p.is_dir() and not os.listdir(p):
        shutil.rmtree(p)


@dataclass
class QueuedOps:
    """Everything the TUI queued, carried out on Apply as the app's exit
    value (see ModelmanApp/ModelScreen). No registry/state objects cross
    the TUI/terminal boundary — just this plain data; main.py's
    run_queued_ops() rebuilds a PendingChanges from fresh on-disk state.
    """

    ready: dict[str, bool] = field(default_factory=dict)
    deletes: dict[str, VariantSpec] = field(default_factory=dict)
    moves: dict[str, str] = field(default_factory=dict)
    exposes: dict[str, bool] = field(default_factory=dict)


@dataclass
class PendingChanges:
    registry: Registry
    state: StateStore
    registry_path: Path
    state_path: Path
    providers: dict[str, object]
    # Each queued item carries (model_id, VariantSpec, target_ready). For a
    # provider present in `providers`, target=True downloads / target=False
    # clears (rm) without touching the registry entry. For a provider absent
    # from `providers` (flag-only: native or unmapped), target=True just
    # flips state.ready (no provider call), while target=False flips it AND
    # removes the on-disk artifact recorded in state.disk_path.
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
    # Populated during apply(); used by _persist() (called at the normal
    # end of apply(), and again from its exception safety net) to merge
    # this run's changes onto a freshly-loaded on-disk StateStore instead of
    # overwriting the whole file from this object's in-memory snapshot,
    # which may be stale relative to modelman.toml if another modelman
    # process wrote to it concurrently.
    _touched_model_ids: set[str] = field(default_factory=set)
    _forgotten_families: set[str] = field(default_factory=set)

    def _remove_artifact_if_present(
        self,
        model_id: str,
        variant: VariantSpec,
        label: str,
        provider: object,
        fail_prefix: str,
        emit: EventFn,
    ) -> bool:
        """Remove `variant`'s on-disk artifact via `provider.delete()`,
        handling the shared-artifact-owner conflict check and the
        delete-exception failure path shared by the deletes loop and the
        ready-off loop's mapped-provider branch.

        First confirms the artifact is actually present (`is_downloaded`,
        defaulting to "assume present" if the check itself raises, so a
        real failure surfaces from the delete attempt rather than being
        swallowed as "already gone"). If another registry entry's
        `path_of()` resolves to the same on-disk target, the file is kept
        and a failure is recorded (but this is not a "hard" failure — the
        caller still proceeds to clear the registry/state row). If
        `provider.delete()` raises, that failure is recorded and reported
        as a hard failure.

        `fail_prefix` distinguishes the two callers' failure-message
        wording ("delete" for a full delete, "clear" for a ready-off
        clear) — the only difference between the two call sites.

        Returns True iff a hard failure (delete exception) occurred and
        the caller should `continue` past the rest of its per-model
        processing, matching both callers' original control flow.
        """
        try:
            artifact_present = bool(provider.is_downloaded(variant))  # type: ignore[attr-defined]
        except Exception:
            # Cannot confirm absence; fall back to "try the call" and
            # surface any failure normally.
            artifact_present = True
        if not artifact_present:
            return False
        conflict = find_shared_artifact_owner(self.registry, provider, variant)
        if conflict is not None:
            # Another registry entry resolves to the same on-disk
            # artifact; removing it would destroy that entry's weights.
            # Keep the file, still drop this entry's registry/state rows,
            # and surface why.
            reason = f"artifact shared with {conflict.id} — not removed"
            self.failures.append(f"{fail_prefix} {model_id}: {reason}")
            emit(f"delete:fail|{model_id}|{label}|{reason}")
            return False
        try:
            self._delete(variant)
        except Exception as exc:  # noqa: BLE001
            reason = _reason(exc)
            self.failures.append(f"{fail_prefix} {model_id}: {exc}")
            emit(f"delete:fail|{model_id}|{label}|{reason}")
            return True
        return False

    def _cleanup_partial_download(self, provider: object, variant: VariantSpec) -> None:
        """Best-effort removal of a cancelled/failed download's partial
        artifact, so the next reconcile doesn't silently promote it to
        ready=True. Mirrors the pre-existing DownloadManager._finish
        cleanup path: skips removal when another registry entry's
        artifact overlaps this variant's (the same guard
        find_shared_artifact_owner already provides elsewhere in this
        class), so a cancelled/failed download can't rmtree an artifact
        another entry still owns.
        """
        with contextlib.suppress(Exception):
            if find_shared_artifact_owner(self.registry, provider, variant) is None:
                provider.cleanup_partial_download(variant)  # type: ignore[attr-defined]

    def _finish_ready(
        self,
        emit: EventFn,
        model_id: str,
        *,
        ready: bool,
        disk_path: str | None = None,
        size_bytes: int | None = None,
        keep_existing_path: bool = False,
        done_tag: str,
    ) -> None:
        """Persist a ready-loop item's final state.ready/disk_path/size_bytes,
        mark it touched for _persist(), and emit its pre-built :done tag.

        Shared by all three ready-loop branches (flag-only flip, real
        download, clear) so a change to this sequence — a new field, a
        changed emit shape — needs one edit instead of three separately
        drifting copies. `keep_existing_path=True` is for the flag-only
        ready-ON flip, which must not clobber a disk_path/size_bytes it
        never itself set.
        """
        if keep_existing_path:
            self.state.set(model_id, replace(self.state.get(model_id), ready=ready))
        else:
            self.state.set(
                model_id,
                replace(
                    self.state.get(model_id),
                    ready=ready,
                    disk_path=disk_path,
                    size_bytes=size_bytes,
                ),
            )
        self._touched_model_ids.add(model_id)
        emit(done_tag)

    def _persist(self, emit: EventFn) -> None:
        """Save registry.toml and merge this run's touched state rows onto
        a freshly-loaded modelman.toml. Called once at the normal end of
        apply(), and again (best-effort) from the exception safety net
        below if a BaseException propagates out of apply() early — so an
        already-completed delete or move from this same batch is not lost
        just because a later step raised or was interrupted.
        """
        try:
            emit("save:start")
            save_registry(self.registry, self.registry_path)
            with locked_state(self.state_path) as fresh_state:
                for mid in self._touched_model_ids:
                    if mid in self.state.models:
                        fresh_state.set(mid, self.state.get(mid))
                    else:
                        # The deletes loop removed this id from self.state —
                        # "touching" it means removal, so mirror that on the
                        # fresh store. Writing self.state.get(mid) here would
                        # insert a default (ready=False) entry and resurrect
                        # the row this apply just deleted.
                        fresh_state.models.pop(mid, None)
                for family in self._forgotten_families:
                    fresh_state.forget_family(family)
            emit("save:done")
        except Exception as exc:  # noqa: BLE001
            reason = _reason(exc)
            self.failures.append(f"save: {exc}")
            emit(f"save:fail|{reason}")

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
            # Empty-queue fast path. Today's only call site is main.py's
            # run_queued_ops, which always has at least one queued item
            # (the TUI only returns a QueuedOps via Apply when the queue is
            # non-empty). A future programmatic caller would see nothing
            # printed beyond "apply:done" — fine for the current contract,
            # but if you reach this branch from elsewhere consider whether
            # the user is misled by zero attempted work.
            emit("apply:done")
            return

        try:
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
                emit(f"delete:start|{model_id}|{label}")
                if provider is not None and self._remove_artifact_if_present(
                    model_id, variant, label, provider, "delete", emit
                ):
                    continue
                # Either:
                # - flag-only provider (native/unmapped): no on-disk artifact,
                # - or reconcilable provider with no artifact: nothing to delete.
                # In both cases we still need to remove the registry/state rows.
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
                was_exposed = self.state.get(model_id).exposed
                self.exposes = [(mid, t) for mid, t in self.exposes if mid != model_id]
                if was_exposed:
                    self.exposes.append((model_id, False))
                # Clear any state entry so modelman.toml doesn't carry a
                # stale downloaded=True after the user removed the file.
                self.state.models.pop(model_id, None)
                self._touched_model_ids.add(model_id)
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
                self._forgotten_families.add(f)

            # Ids queued for deletion in this apply, whether or not the delete
            # succeeded. The ready loop must not touch any of them: a successful
            # delete already removed the rows, and a failed one has already
            # surfaced its failure — re-running _delete would only duplicate it.
            attempted_deletes = {mid for mid, _ in self.deletes}
            for model_id, variant, target in self.ready:
                if aborted():
                    return
                if model_id in attempted_deletes:
                    # Queued for deletion in this same apply (succeeded or
                    # failed); the ready toggle is moot either way.
                    continue
                assert variant["id"] == model_id, (
                    f"variant id {variant['id']!r} != queued model_id {model_id!r}"
                )
                label = _label(variant)
                provider_id = variant["provider"]
                provider = self.providers.get(provider_id)
                # A provider that manages its own cache (MTPLX via the mtplx
                # CLI) has no download for apply() to drive — its ready-on is
                # a queued flag flip (the user cached the weights themselves),
                # per manages_own_cache below. mtplx is
                # the first provider that is BOTH Provider-mapped and
                # flag-only: without this carve-out the assert below fires and
                # aborts the whole apply. Class-level lookup via
                # ProviderRegistry, not instance getattr — test mocks
                # auto-create any attribute touched on an instance, which
                # would misroute every mocked provider.
                provider_cls = ProviderRegistry.get_class(provider_id)
                manages_own_cache = bool(
                    provider_cls is not None and provider_cls.manages_own_cache
                )
                if provider is None or (manages_own_cache and target):
                    # Flag-only flip (native/unmapped provider, or a
                    # manages_own_cache ready-on): no provider call exists for
                    # the ready-on itself — but a provider-is-None ready-off
                    # still means "remove the artifact",
                    # so drop the file recorded in state.disk_path, mirroring
                    # what a mapped provider's delete() would do.
                    emit(f"ready:start|{model_id}|{label}")
                    if not target:
                        try:
                            _remove_local_artifact(self.state, variant)
                        except Exception as exc:  # noqa: BLE001
                            reason = _reason(exc)
                            self.failures.append(f"clear {model_id}: {reason}")
                            emit(f"delete:fail|{model_id}|{label}|{reason}")
                    self._finish_ready(
                        emit,
                        model_id,
                        ready=target,
                        keep_existing_path=target,
                        done_tag=f"ready:done|{model_id}|{label}",
                    )
                elif target:
                    # provider present, not manages_own_cache, target=True:
                    # a real download/pull. Restored from before DownloadManager
                    # existed — the TUI now queues every ready-on instead of
                    # backgrounding real ones, so apply() owns them again.
                    emit(f"download:start|{model_id}|{label}")
                    try:
                        local_path = self._download(variant, on_progress)
                    except DownloadCancelled:
                        self._cleanup_partial_download(provider, variant)
                        emit(f"download:cancelled|{model_id}|{label}")
                        # Persist before the terminal apply:cancelled event,
                        # matching every other exit path's ordering (the
                        # end-of-apply() `_persist(emit); emit("apply:done")`
                        # below, and the `except BaseException` safety net's
                        # `_persist(emit); raise`). A plain `return`, unlike
                        # those paths, does NOT reach that safety net on its
                        # own — persisted explicitly here so an already-
                        # completed delete/move earlier in this same apply()
                        # call is not silently dropped.
                        #
                        # Currently unreachable from main.py's run_queued_ops:
                        # the only caller of PendingChanges.cancel() (which
                        # sets a provider's _cancel_requested flag) is
                        # run_queued_ops's `except KeyboardInterrupt` handler,
                        # which only runs after apply() has already unwound —
                        # a real Ctrl+C interrupts via a raw KeyboardInterrupt
                        # instead (see _progress.py's module docstring). If a
                        # future caller does reach this path, note it returns
                        # normally with no failures recorded — unlike a
                        # KeyboardInterrupt, it produces no "Cancelled: N
                        # steps completed..." summary in run_queued_ops.
                        self._persist(emit)
                        emit("apply:cancelled")
                        return
                    except Exception as exc:  # noqa: BLE001
                        self._cleanup_partial_download(provider, variant)
                        reason = _reason(exc)
                        self.failures.append(f"download {model_id}: {exc}")
                        emit(f"download:fail|{model_id}|{label}|{reason}")
                        continue
                    except BaseException:
                        # KeyboardInterrupt (Ctrl+C during the post-exit
                        # runner) unwinds straight through here — clean up
                        # the partial artifact before propagating so the
                        # caller's cancellation handling still runs (and the
                        # next reconcile doesn't promote a truncated
                        # download to ready=True).
                        self._cleanup_partial_download(provider, variant)
                        raise
                    size_bytes = self._size_of(local_path)
                    if size_bytes is None:
                        try:
                            size_bytes = provider.size_of(variant)  # type: ignore[attr-defined]
                        except Exception:  # noqa: BLE001
                            size_bytes = None
                    suffix = f"|{human_bytes(size_bytes)}" if size_bytes is not None else ""
                    self._finish_ready(
                        emit,
                        model_id,
                        ready=True,
                        disk_path=local_path,
                        size_bytes=size_bytes,
                        done_tag=f"download:done|{model_id}|{label}{suffix}",
                    )
                else:
                    # provider present, target False: clear.
                    emit(f"delete:start|{model_id}|{label}")
                    # Mirror the deletes loop's guard: a stale-ready model whose
                    # artifact is already gone must clear cleanly, not fail and
                    # strand state.ready=True with the unexpose cascade skipped.
                    if self._remove_artifact_if_present(
                        model_id, variant, label, provider, "clear", emit
                    ):
                        continue
                    self._finish_ready(
                        emit,
                        model_id,
                        ready=False,
                        done_tag=f"delete:done|{model_id}|{label}",
                    )
                # Cascade: turning ready off (either branch) drops the model's
                # exposure — mirrors the full-delete step's was_exposed rule.
                # Flag-only providers have no LiteLLM config row, so just flip
                # the state flag directly instead of routing through the
                # config writer.
                if not target and self.state.get(model_id).exposed:
                    self.exposes = [(mid, t) for mid, t in self.exposes if mid != model_id]
                    if provider is None:
                        self.state.set(
                            model_id, replace(self.state.get(model_id), exposed=False)
                        )
                        self._touched_model_ids.add(model_id)
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
                    outcomes, warnings = apply_expose_queue(
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
                    # Proxy-restart notices (command unset or failed) are
                    # non-fatal; surface them through the event channel so the
                    # TUI renders them on the UI thread rather than a worker
                    # thread writing to stderr.
                    for warning in warnings:
                        emit(f"expose:warning|{_sanitize(warning)}")
                    self._touched_model_ids.update(mid for mid, _ in self.exposes)
        except BaseException:
            # A real exception (including a genuine Ctrl+C KeyboardInterrupt
            # raised inside a blocking provider call, see the download
            # branch's own except BaseException above) propagating out of
            # the loops above must not discard already-completed
            # irreversible steps (deletes, moves) from this same apply()
            # call. This does NOT apply to the aborted()/self.cancelled
            # early-return path above (a plain `return`, not an exception)
            # — that path's "nothing saved" semantics are intentional and
            # tested (see docs/superpowers/specs/2026-09-13-downloads-on-
            # exit-design.md and the cancellation tests in tests/test_queue.py).
            self._persist(emit)
            raise

        self._persist(emit)
        emit("apply:done")

    def _delete(self, variant: VariantSpec) -> None:
        provider = self.providers[variant["provider"]]
        if hasattr(provider, "delete"):
            provider.delete(variant)  # type: ignore[attr-defined]
            return
        # Fallback: providers without delete() just remove the recorded file.
        _remove_local_artifact(self.state, variant)

    def _download(self, variant: VariantSpec, on_progress: EventFn | None = None) -> str:
        provider = self.providers[variant["provider"]]
        try:
            return provider.download(variant, on_progress=on_progress)  # type: ignore[attr-defined]
        except TypeError as exc:
            # Only treat this as "provider.download() has no on_progress
            # parameter" when the TypeError was raised at the call site
            # itself (no further frames — the callee's body never started
            # executing). A TypeError raised from inside a provider's real
            # download logic has at least one additional frame and must
            # propagate as a real failure instead of triggering a silent,
            # work-doubling retry.
            if exc.__traceback__ is not None and exc.__traceback__.tb_next is not None:
                raise
            return provider.download(variant)  # type: ignore[attr-defined]

    @staticmethod
    def _size_of(local_path: str) -> int | None:
        # TypeError/ValueError alongside OSError: a non-path object (e.g. a
        # test stub's return value) must not crash apply().
        try:
            p = Path(local_path)
            return p.stat().st_size if p.is_file() else None
        except (OSError, TypeError, ValueError):
            return None
