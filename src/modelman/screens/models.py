"""ModelScreen — drill into a family's models grouped by provider."""

from __future__ import annotations

from collections import defaultdict
from collections.abc import Callable
from dataclasses import replace
from pathlib import Path
from typing import TYPE_CHECKING

from textual.app import ComposeResult
from textual.binding import Binding
from textual.screen import Screen
from textual.widgets import DataTable, Footer, Header, Static

from ..litellm import default_litellm_config_path, provider_policy
from ..queue import PendingChanges
from ..registry import Fetch, ModelEntry, Registry, known_families, provider_config
from ..state import ModelState, StateStore
from . import reload_preserving_cursor

if TYPE_CHECKING:
    from ..providers.base import VariantSpec


def _variant_to_model_entry(variant: dict, *, family: str, registry: Registry) -> ModelEntry:
    """Convert a ModelForm VariantSpec-shaped dict to a ModelEntry.

    The dialog still emits the legacy TypedDict shape (provider, name,
    repo, files, model_info); the screen needs a ModelEntry to insert
    into registry.models. This adapter keeps the form simple and
    isolates the shape translation here.

    Edit mode preserves `variant["id"]` (the immutable key the user
    sees in the picker); add mode derives the same `provider/name`
    shape ModelForm produced. We don't need a separate "id derivation"
    step — the form already gave us one.
    """
    provider_id = variant["provider"]
    # Sanity: provider must exist in the registry. Defends against a
    # malformed dialog result that snuck past form validation.
    registry.provider(provider_id)  # raises KeyError if unknown

    name = variant.get("name") or variant["id"]
    repo = variant.get("repo")
    files = variant.get("files")
    quantizations = variant.get("quantizations")
    fetch = None
    if repo or files or quantizations:
        fetch = Fetch(repo=repo, files=files, quantizations=quantizations)

    model_info = dict(variant.get("model_info") or {})
    return ModelEntry(
        id=variant["id"],
        family=family,
        provider_id=provider_id,
        model_name=name,
        location=variant.get("location"),
        source="curated",
        model_info=model_info,
        fetch=fetch,
    )


def _human_size(n) -> str:
    if n is None:
        return "—"
    if n < 1024:
        return f"{n} B"
    for unit in ("KB", "MB", "GB", "TB"):
        n /= 1024
        if n < 1024:
            return f"{n:.1f} {unit}"
    return f"{n:.1f} PB"


def _entry_kwargs(m: ModelEntry) -> dict:
    """Deep-copy a ModelEntry to kwargs so snapshot copies don't share
    nested Fetch/Cost objects with the live registry."""
    from copy import deepcopy

    return deepcopy(m).__dict__


def _state_kwargs(s: ModelState) -> dict:
    from dataclasses import asdict

    return asdict(s)


def _model_entry_to_variant(entry: ModelEntry) -> VariantSpec:
    """Build a VariantSpec-shaped dict from a ModelEntry for provider APIs.

    Providers still consume the legacy TypedDict (provider, name, repo,
    files, model_info). ModelEntry stores repo/files in `fetch`. We
    don't carry `model_info` from the registry into the provider call
    (providers read what they need from their own state).
    """
    repo = entry.fetch.repo if entry.fetch else None
    files = entry.fetch.files if entry.fetch else None
    quantizations = entry.fetch.quantizations if entry.fetch else None
    return {
        "id": entry.id,
        "provider": entry.provider_id,
        "name": entry.model_name,
        "repo": repo,
        "files": files,
        "quantizations": quantizations,
        "location": entry.location,
        "model_info": dict(entry.model_info),
    }


class ModelScreen(Screen[None]):
    BINDINGS = [
        ("escape", "back", "Back"),
        ("x", "toggle_ready", "Toggle ready"),
        ("a", "add_model", "Add"),
        ("d", "delete_model", "Delete"),
        ("e", "edit_model", "Edit"),
        Binding("enter", "select_row", "Edit", priority=True),
        ("r", "reconcile", "Reconcile"),
        ("l", "toggle_expose", "Toggle LiteLLM"),
    ]

    def __init__(
        self,
        registry: Registry,
        state: StateStore,
        family: str,
        registry_path: Path,
        state_path: Path,
        available_providers: list[str] | None = None,
    ) -> None:
        super().__init__()
        self.registry = registry
        self.state = state
        self.family = family
        self.registry_path = registry_path
        self.state_path = state_path
        # The list of providers configured in ~/.config/local-ai/config.yaml.
        # The provider-table on the left always shows every entry here, even
        # when the family has no models at all — otherwise an empty family
        # would show no providers and the user would have nowhere to click
        # 'a' to add the first one. Order is preserved as given (config
        # insertion order) and falls back to a stable default only if no
        # list was provided, so the cursor lands on the user's "first
        # choice" rather than alphabetical.
        if available_providers is not None:
            self.available_providers: list[str] = list(available_providers)
        else:
            self.available_providers = ["ollama", "llamacpp", "omlx"]
        # Default selection: ollama if configured, otherwise the first
        # configured provider. This keeps the cursor on the most
        # common starting point for empty families.
        # Default selection: ollama if configured, otherwise the first
        # configured provider. This keeps the cursor on the most
        # common starting point for empty families.
        # Provider of the last model added or edited this session; used to
        # default the Add dialog's provider dropdown now that there's no
        # provider pane to inherit a selection from.
        self._last_provider_used: str | None = None
        # queued_ready / queued_deletes map model_id -> target state.
        # queued_ready values are bools: True to ready, False to clear.
        self.queued_ready: dict[str, bool] = {}
        self.queued_deletes: dict[str, VariantSpec] = {}
        # model_id -> target exposed state (True to expose, False to unexpose).
        self.queued_exposes: dict[str, bool] = {}
        # model_id -> target family. Applied to the registry at apply()
        # time; the in-memory family is untouched until then so the row
        # stays visible in this family's table with a → glyph.
        self.queued_moves: dict[str, str] = {}
        # Ids of models created this session via the add dialog. Used by
        # _restore_snapshot: a model added into a *different* family
        # isn't caught by the family-scoped restore filter.
        self._added_ids: set[str] = set()
        # Reconcile overlay: per-model-id reality from the provider.
        self.reconciled: dict[str, dict] = {}
        # Snapshot for discard: restore if the user exits without applying.
        self._snapshot_models: list[ModelEntry] = [
            ModelEntry(**_entry_kwargs(m)) for m in registry.models if m.family == family
        ]
        self._snapshot_state_entries: dict[str, ModelState] = {
            mid: ModelState(**_state_kwargs(s))
            for mid, s in state.models.items()
            if any(m.id == mid and m.family == family for m in registry.models)
        }

    def compose(self) -> ComposeResult:
        yield Header()
        yield DataTable(id="model-table", cursor_type="row")
        yield Static("Pending: ready 0 · delete 0", id="pending-bar")
        yield Footer()

    def on_mount(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        mt.add_columns(
            "FAMILY", "PROVIDER", "MODEL", "LOCATION", "STATUS", "EXPOSED", "SIZE", "PATH"
        )
        self.reload()
        self._refresh_pending_bar()
        mt.focus()
        self.run_worker(self._run_reconcile, exclusive=True, thread=True)

    def _run_reconcile(self) -> None:
        """Ask each provider whether its models are on disk; cache results."""
        from ..providers.registry import ProviderRegistry

        family_models = self.registry.models_by_family(self.family)
        by_provider: dict[str, list[ModelEntry]] = defaultdict(list)
        for m in family_models:
            by_provider[m.provider_id].append(m)
        for provider_name, entries in by_provider.items():
            try:
                entry = self.registry.provider(provider_name)
                provider = ProviderRegistry.get(provider_name, provider_config(entry))
            except Exception:
                continue
            for m in entries:
                size: int | None = None
                spec = _model_entry_to_variant(m)
                try:
                    ready = bool(provider.is_downloaded(spec))  # type: ignore[attr-defined]
                except Exception:
                    ready = False
                try:
                    raw = provider.size_of(spec)  # type: ignore[attr-defined]
                    if isinstance(raw, int):
                        size = raw
                except Exception:
                    size = None
                local_path: str | None = None
                if ready and hasattr(provider, "list_local"):
                    try:
                        for lm in provider.list_local():
                            lm_name = lm.get("name") or lm.get("variant_id")
                            if lm_name == m.model_name or lm_name == m.id:
                                lp = lm.get("local_path") or lm.get("path")
                                if isinstance(lp, str):
                                    local_path = lp
                                break
                    except Exception:
                        pass
                self.reconciled[m.id] = {
                    "ready": ready,
                    "size": size,
                    "local_path": local_path,
                }
        # Re-render on the main thread.
        self.app.call_from_thread(self.reload)

    def action_reconcile(self) -> None:
        self.run_worker(self._run_reconcile, exclusive=True, thread=True)

    def reload(self) -> None:
        self._load_models()

    def _load_models(self) -> None:
        from ..providers.registry import ProviderRegistry

        def _repopulate() -> None:
            mt = self.query_one("#model-table", DataTable)
            mt.clear()
            models = sorted(
                self.registry.models_by_family(self.family),
                key=lambda m: (m.provider_id, m.model_name),
            )
            for m in models:
                rec = self.reconciled.get(m.id)
                if rec is not None:
                    ready = bool(rec.get("ready"))
                    size_str = _human_size(rec.get("size")) if ready else "—"
                    # When reconcile says the model is no longer on disk, don't
                    # fall back to the stale disk_path stored in state.
                    path = (
                        rec.get("local_path") or (self.state.get(m.id).disk_path or "—")
                        if ready
                        else "—"
                    )
                else:
                    state_entry = self.state.get(m.id)
                    ready = state_entry.ready
                    size_str = "—"
                    path = state_entry.disk_path or "—"
                    if ready:
                        try:
                            entry = self.registry.provider(m.provider_id)
                            prov = ProviderRegistry.get(m.provider_id, provider_config(entry))
                            size_str = _human_size(prov.size_of(_model_entry_to_variant(m)))
                        except Exception:
                            pass
                if m.id in self.queued_deletes:
                    status = "[red]✗[/red]"
                elif m.id in self.queued_ready:
                    status = "[yellow]↓[/yellow]" if self.queued_ready[m.id] else "[yellow]↑[/yellow]"
                elif m.id in self.queued_moves:
                    status = "[magenta]→[/magenta]"
                elif ready:
                    status = "[green]✓[/green]"
                else:
                    status = "[dim]○[/dim]"
                exposed = self.state.get(m.id).litellm_exposed
                if m.id in self.queued_exposes:
                    exposed = self.queued_exposes[m.id]
                exposed_str = "L" if exposed else "–"
                mt.add_row(
                    m.family,
                    m.provider_id,
                    m.model_name,
                    m.location or "—",
                    status,
                    exposed_str,
                    size_str,
                    path,
                    key=m.id,
                )

        reload_preserving_cursor(self.query_one("#model-table", DataTable), _repopulate)

    def _is_ready(self, model_id: str) -> bool:
        """Truth about whether a model is ready to use.

        Prefers the reconcile overlay (reality); falls back to state
        when reconcile hasn't run for this model yet.
        """
        rec = self.reconciled.get(model_id)
        if rec is not None:
            return bool(rec.get("ready"))
        return self.state.get(model_id).ready

    def _refresh_pending_bar(self) -> None:
        bar = self.query_one("#pending-bar", Static)
        bar.update(
            f"Pending: ready {len(self.queued_ready)} · delete {len(self.queued_deletes)}"
            f" · move {len(self.queued_moves)} · expose {len(self.queued_exposes)}"
        )

    def action_toggle_ready(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        mid = str(row_key.value)
        entry = next((m for m in self.registry.models if m.id == mid), None)
        if entry is None:
            return
        currently_ready = self._is_ready(mid)
        target = not currently_ready
        if mid in self.queued_ready:
            # Toggling again cancels the queued flip.
            self.queued_ready.pop(mid)
        else:
            self.queued_ready[mid] = target
        self._last_provider_used = entry.provider_id
        self._refresh_pending_bar()
        self.reload()

    def action_toggle_expose(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        mid = str(row_key.value)
        entry = next((m for m in self.registry.models if m.id == mid), None)
        if entry is None:
            return
        if not self._is_ready(mid):
            self.app.notify("Model must be ready before exposing")
            return
        if provider_policy(entry.provider_id) is None:
            self.app.notify("Provider has no LiteLLM mapping — cannot expose")
            return
        current = self.state.get(mid).litellm_exposed
        if mid in self.queued_exposes:
            current = self.queued_exposes[mid]
        self.queued_exposes[mid] = not current
        self._refresh_pending_bar()
        self.reload()

    def _provider_list(self) -> list[str]:
        # Use the full configured-provider list, not just the providers
        # currently used by models in the family, so the user can add
        # the first model for a fresh family via the 'a' dialog.
        # The list is sorted alphabetically so the Add dialog's
        # provider Select and any code that iterates providers sees
        # a stable, predictable order.
        if self.available_providers:
            return sorted(self.available_providers)
        return sorted({m.provider_id for m in self.registry.models_by_family(self.family)})

    def _provider_kinds(self) -> dict[str, str]:
        """Map each registered provider id to the ModelForm 'kind' that
        drives its Location-select lock rule: native providers and
        llamacpp/omlx are locked (cloud and local respectively); ollama
        is the one provider where location is genuinely editable;
        everything else (openrouter, any other unmapped provider) locks
        to cloud."""
        kinds: dict[str, str] = {}
        for p in self.registry.providers:
            if p.auth.type == "native":
                kinds[p.id] = "native"
            elif p.id in ("llamacpp", "omlx"):
                kinds[p.id] = "local-only"
            elif p.id == "ollama":
                kinds[p.id] = "ollama"
            else:
                kinds[p.id] = "cloud-only"
        return kinds

    def _families_list(self) -> list[str]:
        """Every family the add/edit dialogs may target: families with
        models in the registry, first-class [[families]] entries, and
        legacy state.families keys, sorted."""
        return known_families(self.registry, self.state)

    def action_add_model(self) -> None:
        from .forms import ModelForm

        providers = self._provider_list() or ["ollama", "llamacpp", "omlx"]
        # Pre-select the provider the user is currently looking at, so
        # adding "another llamacpp model" doesn't make them switch the
        # dropdown back. Fall back to None if no provider is selected.
        default_provider = (
            self._last_provider_used if self._last_provider_used in providers else None
        )
        self.app.push_screen(
            ModelForm(
                providers=providers,
                default_provider=default_provider,
                families=self._families_list(),
                family=self.family,
                provider_kinds=self._provider_kinds(),
            ),
            self._on_add_model,
        )

    def _on_add_model(self, result) -> None:
        if result is None:
            return
        variant = result.spec
        if any(m.id == variant["id"] for m in self.registry.models):
            self.app.notify("Model ID already exists")
            return
        entry = _variant_to_model_entry(variant, family=result.family, registry=self.registry)
        self.registry.models.append(entry)
        self._added_ids.add(variant["id"])
        self.queued_ready[variant["id"]] = True
        self._last_provider_used = variant["provider"]
        self.reload()
        self._refresh_pending_bar()

    def action_delete_model(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        mid = str(row_key.value)
        entry = next((m for m in self.registry.models if m.id == mid), None)
        if entry is None:
            return
        # No not-ready gate: any model can be queued for delete.
        # Apply() handles the absent-artifact case (skips the on-disk
        # removal when provider.is_downloaded() reports False).
        spec = _model_entry_to_variant(entry)
        if mid in self.queued_deletes:
            self.queued_deletes.pop(mid)
        else:
            self.queued_deletes[mid] = spec
        self.queued_ready.pop(mid, None)
        self._refresh_pending_bar()
        self.reload()

    def action_edit_model(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        # cursor_row can briefly exceed row_count during a reload race;
        # bail instead of crashing.
        if mt.cursor_row >= mt.row_count:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        mid = str(row_key.value)
        entry = next((m for m in self.registry.models if m.id == mid), None)
        if entry is None:
            return
        from .forms import ModelForm

        spec = _model_entry_to_variant(entry)
        self.app.push_screen(
            ModelForm(
                providers=self._provider_list(),
                variant=spec,
                families=self._families_list(),
                family=self.queued_moves.get(mid, self.family),
                provider_kinds=self._provider_kinds(),
            ),
            self._on_edit_model,
        )

    def on_data_table_row_selected(self, event: DataTable.RowSelected) -> None:
        self.action_edit_model()

    def action_select_row(self) -> None:
        """Screen-level Enter handler: always edits the row under the
        cursor now that there's only one table."""
        self.action_edit_model()

    def _on_edit_model(self, result) -> None:
        if result is None:
            return
        updated = result.spec
        new_entry = _variant_to_model_entry(updated, family=self.family, registry=self.registry)
        for i, m in enumerate(self.registry.models):
            if m.id == updated["id"]:
                self.registry.models[i] = new_entry
                break
        if result.family != self.family:
            self.queued_moves[updated["id"]] = result.family
        else:
            self.queued_moves.pop(updated["id"], None)
        self._last_provider_used = updated["provider"]
        self.reload()
        self._refresh_pending_bar()

    def action_back(self) -> None:
        if (
            not self.queued_ready
            and not self.queued_deletes
            and not self.queued_moves
            and not self.queued_exposes
        ):
            self.app.pop_screen()
            return
        from .forms import ConfirmExitDialog

        self.app.push_screen(
            ConfirmExitDialog(
                ready=list(self.queued_ready.items()),
                deletes=list(self.queued_deletes.values()),
                exposes=list(self.queued_exposes.items()),
                moves=list(self.queued_moves.items()),
            ),
            self._on_exit_confirm,
        )

    def _on_exit_confirm(self, choice: str | None) -> None:
        if choice == "apply":
            self._push_status_screen()
            return
        if choice == "discard":
            self._restore_snapshot()
            self.queued_ready.clear()
            self.queued_deletes.clear()
            self.queued_moves.clear()
            self.queued_exposes.clear()
            self._added_ids.clear()
            self.app.pop_screen()
            return
        # "cancel" or None: stay on the model screen, queue preserved.
        return

    def _push_status_screen(self) -> None:
        """Hand off the apply run to StatusScreen for live progress.

        ModelScreen pops itself; StatusScreen then runs apply() on a
        worker thread and pops back to FamilyScreen when done.
        """
        from .status import StatusScreen

        self.app.pop_screen()
        self.app.push_screen(StatusScreen(family=self.family, run_apply=self._run_apply))

    def _run_apply(
        self,
        on_event: Callable[[str], None],
        on_progress: Callable[[str], None],
        register: Callable[[PendingChanges], None],
    ) -> None:
        """Construct a PendingChanges and drive apply().

        Passed as a closure to StatusScreen. Mutates self.registry and
        self.state in place (mark_ready, remove deleted models).
        The on_event callable receives lifecycle tags; on_progress
        receives per-line provider output forwarded verbatim into the
        log.
        """
        from ..providers.registry import ProviderRegistry

        providers: dict[str, object] = {}
        specs_by_id = {
            m.id: _model_entry_to_variant(m)
            for m in self.registry.models
            if m.id in self.queued_ready
        }
        for spec in list(specs_by_id.values()) + list(self.queued_deletes.values()):
            try:
                entry = self.registry.provider(spec["provider"])
                providers[spec["provider"]] = ProviderRegistry.get(
                    spec["provider"], provider_config(entry)
                )
            except KeyError:
                # Provider not in registry or not mapped to a Provider
                # class: treat as flag-only (native/unmapped) and let
                # PendingChanges flip state flags without a provider call.
                continue
            # Other exceptions (bad config, import failure, etc.) are
            # real errors and must not be silently treated as flag-only.
        # Merge any reconciled entries that state didn't know about,
        # so the saved state reflects reality. Never remove existing
        # entries; the user can queue a delete (d) for that. This also
        # bridges the expose gate: the TUI accepts an expose based on
        # the reconcile overlay, while the apply-time check reads
        # state.ready — without this merge a model on disk with a
        # stale ready=False would fail at apply with a spurious
        # "not ready".
        for mid, rec in self.reconciled.items():
            if rec.get("ready") and not self.state.get(mid).ready:
                updated = replace(self.state.get(mid), ready=True)
                # Only overwrite the path when reconcile actually found
                # one; a blank local_path must not blank a known one.
                if rec.get("local_path"):
                    updated.disk_path = rec["local_path"]
                self.state.set(mid, updated)
        pending = PendingChanges(
            registry=self.registry,
            state=self.state,
            family=self.family,
            registry_path=self.registry_path,
            state_path=self.state_path,
            providers=providers,
            ready=[(mid, specs_by_id[mid], target) for mid, target in self.queued_ready.items()],
            deletes=[(mid, spec) for mid, spec in self.queued_deletes.items()],
            moves=list(self.queued_moves.items()),
            exposes=list(self.queued_exposes.items()),
            litellm_path=default_litellm_config_path(),
        )
        register(pending)
        pending.apply(on_event=on_event, on_progress=on_progress)
        # The closure runs on the StatusScreen's worker thread; mutate
        # in-memory queue state from here too so subsequent opens of this
        # screen see an empty queue.
        self.queued_ready.clear()
        self.queued_deletes.clear()
        self.queued_moves.clear()
        self.queued_exposes.clear()
        self._added_ids.clear()

    def _restore_snapshot(self) -> None:
        """Restore the in-memory registry/state to the snapshot taken on
        mount, dropping any queued mutations.

        Restore is keyed by model id, not family: models with queued
        (unapplied) moves still belong to this family in the registry
        (edits always write back with family=self.family; only
        queued_moves tracks the pending target), so every live model
        with family == self.family is already in restore_ids via
        _snapshot_models or _added_ids. _added_ids covers the remaining
        gap: a model added into a *different* family this session, which
        the id check alone would otherwise let survive discard.
        """
        restore_ids = {m.id for m in self._snapshot_models} | self._added_ids
        keep = [m for m in self.registry.models if m.id not in restore_ids]
        self.registry.models = keep + self._snapshot_models
        # Replace state entries that were in the snapshot.
        for mid in self._snapshot_state_entries:
            self.state.set(mid, self._snapshot_state_entries[mid])
        # Defensive: drop state entries that somehow leaked in during this
        # session but weren't in the snapshot, scoped to this family. (No
        # session path writes state.models outside apply(), so this is a
        # no-op under normal discard flows.)
        for mid in list(self.state.models):
            if mid not in self._snapshot_state_entries and any(
                m.id == mid and m.family == self.family for m in self.registry.models
            ):
                self.state.models.pop(mid, None)
