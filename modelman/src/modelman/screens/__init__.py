"""Shared TUI screen helpers."""

from __future__ import annotations

from collections import defaultdict
from collections.abc import Callable, Iterable
from dataclasses import replace

from textual.widgets import DataTable

from ..providers.base import Provider
from ..registry import (
    ModelEntry,
    ProviderEntry,
    Registry,
    model_entry_to_variant,
    model_has_local_artifact,
    provider_config,
)
from ..state import StateStore


def reload_preserving_cursor(table: DataTable, repopulate: Callable[[], None]) -> None:
    """Clear and repopulate `table` without resetting the cursor to row 0.

    DataTable.clear() resets the cursor to (0, 0). This helper snapshots
    the row key under the cursor, runs the caller's repopulate function,
    then restores the cursor onto that key. If the key no longer exists
    (row deleted elsewhere), it falls back to row 0. Empty tables before
    or after are safe no-ops.
    """
    if table.row_count == 0:
        repopulate()
        return

    row_key = list(table.rows.keys())[table.cursor_row]
    repopulate()

    if table.row_count == 0:
        return

    # Textual's DataTable.get_row_index raises RowDoesNotExist (a
    # Textual-internal exception, not a stdlib KeyError) when the key
    # is gone, so catch the broad Exception family and fall back.
    try:
        new_index = table.get_row_index(row_key)
    except Exception:
        new_index = 0
    table.move_cursor(row=new_index)


def reconcile_model_state(
    models: Iterable[ModelEntry], registry: Registry, state: StateStore
) -> None:
    """Ask each provider whether each of `models` is on disk and write the
    result straight into `state`: for local-artifact models (per
    `model_has_local_artifact`), files present -> ready=True + disk_path +
    size_bytes, absent -> ready=False + cleared path/size. Non-local-
    artifact models (cloud-located, or on a cloud provider) are never
    marked ready here — only disk_path/size_bytes are opportunistically
    updated when the provider reports them.

    Shared by FamilyScreen and ModelScreen's background reconcile workers
    so the write semantics can't drift between the two screens.

    Per provider, `resolve_local()` — the batch presence/path/size check —
    is tried first: providers that implement it (ollama's single `ollama
    list` answers everything) cost one subprocess for the whole batch
    instead of one per model. Providers without a batch implementation
    (resolve_local returns None) take the per-model path:
    `is_downloaded()`/`size_of()` per model, and `list_local()` at most
    once per provider and only when at least one model is ready (calling
    it inside the per-model loop, or unconditionally, turns every
    reconcile into an extra subprocess/filesystem scan even when nothing
    in the batch is downloaded).
    """
    # Deferred import: this module is imported by screens/models.py at
    # module load time, and models.py imports ProviderRegistry back —
    # importing the provider registry at the top would be circular.
    # model_entry_to_variant now lives in ..registry (imported at the top
    # here), so only the provider registry stays deferred.
    from ..providers.registry import ProviderRegistry

    by_provider: dict[str, list[ModelEntry]] = defaultdict(list)
    for m in models:
        by_provider[m.provider_id].append(m)

    for provider_name, entries in by_provider.items():
        try:
            provider_entry: ProviderEntry | None = registry.provider(provider_name)
        except KeyError:
            provider_entry = None
        provider: Provider | None = None
        if provider_entry is not None:
            try:
                provider = ProviderRegistry.get(provider_name, provider_config(provider_entry))
            except Exception:
                provider = None
        if provider is None:
            continue

        checked: list[tuple[ModelEntry, bool, int | None]] = []
        paths: dict[int, str] = {}
        specs = [model_entry_to_variant(m) for m in entries]
        try:
            resolved = provider.resolve_local(specs)
        except Exception:
            resolved = None
        if isinstance(resolved, list) and len(resolved) == len(specs):
            # Batch path: one provider call answered every variant. The
            # isinstance/length check is the batch contract: a None (not
            # implemented), a misaligned list (provider bug), or a
            # malformed result all degrade to the per-model path below
            # rather than silently dropping variants from reconcile.
            for i, (m, hit) in enumerate(zip(entries, resolved, strict=True)):
                if hit is None:
                    checked.append((m, False, None))
                    continue
                size = hit.get("size_bytes")
                checked.append((m, True, size if isinstance(size, int) else None))
                path = hit.get("path")
                if isinstance(path, str):
                    paths[i] = path
        else:
            # Per-model path (no batch support on this provider).
            any_ready = False
            for m, spec in zip(entries, specs, strict=True):
                try:
                    ready = bool(provider.is_downloaded(spec))
                except Exception:
                    ready = False
                model_size: int | None = None
                if ready:
                    try:
                        raw = provider.size_of(spec)
                        if isinstance(raw, int):
                            model_size = raw
                    except Exception:
                        model_size = None
                checked.append((m, ready, model_size))
                any_ready = any_ready or ready

            # list_local() at most once per provider, and only when at
            # least one model is ready (see docstring).
            local_by_name: dict[str, str] = {}
            if any_ready:
                try:
                    for lm in provider.list_local():
                        lm_name = lm.get("name") or lm.get("variant_id")  # type: ignore[attr-defined]
                        lp = lm.get("local_path") or lm.get("path")  # type: ignore[attr-defined]
                        if isinstance(lm_name, str) and isinstance(lp, str):
                            local_by_name[lm_name] = lp
                except Exception:
                    pass
            for i, (m, ready, _size) in enumerate(checked):
                if ready:
                    found = local_by_name.get(m.model_name) or local_by_name.get(m.id)
                    if found is not None:
                        paths[i] = found

        for i, (m, ready, size) in enumerate(checked):
            local_path = paths.get(i) if ready else None

            if model_has_local_artifact(m, provider_entry):
                existing = state.get(m.id)
                if ready:
                    state.set(
                        m.id,
                        replace(
                            existing,
                            ready=True,
                            # Only overwrite the path when reconcile
                            # actually found one; a blank local_path must
                            # not blank a known disk_path.
                            disk_path=(
                                local_path if local_path is not None else existing.disk_path
                            ),
                            size_bytes=size,
                        ),
                    )
                else:
                    state.set(
                        m.id,
                        replace(existing, ready=False, disk_path=None, size_bytes=None),
                    )
            elif local_path is not None or size is not None:
                existing = state.get(m.id)
                state.set(
                    m.id,
                    replace(
                        existing,
                        disk_path=local_path if local_path is not None else existing.disk_path,
                        size_bytes=size if size is not None else existing.size_bytes,
                    ),
                )
