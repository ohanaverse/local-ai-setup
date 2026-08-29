"""ModelScreen — drill into a family's models grouped by provider."""

from __future__ import annotations

from collections import defaultdict
from collections.abc import Callable
from dataclasses import replace
from pathlib import Path
from typing import TYPE_CHECKING

from textual.app import ComposeResult
from textual.binding import Binding
from textual.containers import Horizontal, Vertical
from textual.coordinate import Coordinate
from textual.screen import Screen
from textual.widgets import DataTable, Footer, Header, Static

from ..litellm import default_litellm_config_path, is_cloud
from ..queue import PendingChanges
from ..registry import Fetch, ModelEntry, Registry, provider_config
from ..state import ModelState, StateStore

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
        "model_info": dict(entry.model_info),
    }


class ModelScreen(Screen[None]):
    BINDINGS = [
        ("escape", "back", "Back"),
        ("x", "toggle_download", "Toggle download"),
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
        if "ollama" in self.available_providers:
            self.selected_provider: str | None = "ollama"
        elif self.available_providers:
            self.selected_provider = self.available_providers[0]
        else:
            self.selected_provider = None
        # queued_downloads / queued_deletes map model_id -> VariantSpec
        # (the VariantSpec is what provider APIs still consume; model_id
        # is the registry/state key).
        self.queued_downloads: dict[str, VariantSpec] = {}
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
        with Horizontal(id="panes"):
            with Vertical(id="provider-pane"):
                yield DataTable(id="provider-table", cursor_type="row")
            with Vertical(id="model-pane"):
                yield DataTable(id="model-table", cursor_type="row")
        yield Static("Pending: download 0 · delete 0", id="pending-bar")
        yield Footer()

    def on_mount(self) -> None:
        pt = self.query_one("#provider-table", DataTable)
        pt.add_columns("PROVIDER", "MODELS")
        mt = self.query_one("#model-table", DataTable)
        mt.add_columns("NAME", "STATUS", "SIZE", "PATH", "EXPOSED")
        self.reload()
        self._refresh_pending_bar()
        # Put the cursor and focus on the first provider row so the
        # user sees an immediate visible cursor highlight on ollama
        # (or whatever the first configured provider is) and can
        # navigate the left pane with arrow keys without an extra
        # Tab. Enter on a model row still opens the edit dialog
        # via a screen-level binding (see action_select_row); the
        # DataTable's built-in select_cursor binding fires only when
        # the table is focused, so the screen binding fills the gap.
        if pt.row_count > 0:
            pt.cursor_coordinate = Coordinate(0, 0)
        pt.focus()
        # Reconcile with the world so the UI shows reality even when the
        # registry is stale. In-memory only; nothing is written to disk
        # until the user applies.
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
                # Providers consume VariantSpec; build a minimal one
                # from the ModelEntry's stored Fetch.
                spec = _model_entry_to_variant(m)
                try:
                    raw = provider.size_of(spec)  # type: ignore[attr-defined]
                    if isinstance(raw, int):
                        size = raw
                except Exception:
                    size = None
                downloaded = size is not None
                local_path: str | None = None
                if downloaded and hasattr(provider, "list_local"):
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
                    "downloaded": downloaded,
                    "size": size,
                    "local_path": local_path,
                }
        # Re-render on the main thread.
        self.app.call_from_thread(self.reload)

    def action_reconcile(self) -> None:
        self.run_worker(self._run_reconcile, exclusive=True, thread=True)

    def reload(self) -> None:
        pt = self.query_one("#provider-table", DataTable)
        pt.clear()
        # Always render a row for every configured provider so an empty
        # family still shows ollama/llamacpp/omlx in the left pane and
        # the user has somewhere to add their first model. Counts
        # come from the registry.models filtered to this family (0
        # when none).
        counts: dict[str, int] = defaultdict(int)
        for m in self.registry.models_by_family(self.family):
            counts[m.provider_id] += 1
        for provider in self.available_providers:
            pt.add_row(provider, str(counts.get(provider, 0)), key=provider)
        # If the previously selected provider disappeared from the
        # config (e.g. user removed ollama from config.yaml), fall
        # back to the first available one so the right pane doesn't
        # stay blank.
        if self.selected_provider not in self.available_providers and self.available_providers:
            self.selected_provider = self.available_providers[0]
        if self.selected_provider:
            self._load_models_for_provider(self.selected_provider)

    def on_data_table_row_highlighted(self, event: DataTable.RowHighlighted) -> None:
        if event.control.id == "provider-table":
            row_key = event.row_key
            if row_key is not None:
                self.selected_provider = str(row_key.value)
                self._load_models_for_provider(self.selected_provider)

    def _is_downloaded(self, model_id: str) -> bool:
        """Truth about whether a model is on disk.

        Prefers the reconcile overlay (reality); falls back to state
        when reconcile hasn't run for this model yet.
        """
        rec = self.reconciled.get(model_id)
        if rec is not None:
            return bool(rec.get("downloaded"))
        return self.state.get(model_id).downloaded

    def _load_models_for_provider(self, provider: str) -> None:
        from ..providers.registry import ProviderRegistry

        mt = self.query_one("#model-table", DataTable)
        mt.clear()
        for m in self.registry.models_by_family(self.family):
            if m.provider_id != provider:
                continue
            rec = self.reconciled.get(m.id)
            if rec is not None:
                downloaded = bool(rec.get("downloaded"))
                size_str = _human_size(rec.get("size")) if downloaded else "—"
                path = rec.get("local_path") or (self.state.get(m.id).disk_path or "—")
            else:
                state_entry = self.state.get(m.id)
                downloaded = state_entry.downloaded
                size_str = "—"
                path = state_entry.disk_path or "—"
                if downloaded:
                    try:
                        entry = self.registry.provider(provider)
                        prov = ProviderRegistry.get(provider, provider_config(entry))
                        size_str = _human_size(prov.size_of(_model_entry_to_variant(m)))
                    except Exception:
                        pass
            # Pending ops take precedence over disk state:
            if m.id in self.queued_deletes:
                status = "[red]✗[/red]"
            elif m.id in self.queued_downloads:
                status = "[yellow]↓[/yellow]"
            elif m.id in self.queued_moves:
                status = "[magenta]→[/magenta]"
            elif downloaded:
                status = "[green]✓[/green]"
            else:
                status = "[dim]○[/dim]"
            exposed = self.state.get(m.id).litellm_exposed
            if m.id in self.queued_exposes:
                exposed = self.queued_exposes[m.id]
            exposed_str = "L" if exposed else "–"
            mt.add_row(m.model_name, status, size_str, path, exposed_str, key=m.id)

    def _refresh_pending_bar(self) -> None:
        bar = self.query_one("#pending-bar", Static)
        bar.update(
            f"Pending: download {len(self.queued_downloads)} · delete {len(self.queued_deletes)}"
            f" · move {len(self.queued_moves)} · expose {len(self.queued_exposes)}"
        )

    def action_toggle_download(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        mid = str(row_key.value)
        entry = next((m for m in self.registry.models if m.id == mid), None)
        if entry is None:
            return
        if self._is_downloaded(mid):
            return  # already downloaded
        spec = _model_entry_to_variant(entry)
        if mid in self.queued_downloads:
            self.queued_downloads.pop(mid)
        else:
            self.queued_downloads[mid] = spec
        self._refresh_pending_bar()
        if self.selected_provider is not None:
            self._load_models_for_provider(self.selected_provider)

    def action_toggle_expose(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        mid = str(row_key.value)
        entry = next((m for m in self.registry.models if m.id == mid), None)
        if entry is None:
            return
        if not is_cloud(entry.provider_id) and not self._is_downloaded(mid):
            self.app.notify("Model must be downloaded before exposing")
            return
        current = self.state.get(mid).litellm_exposed
        if mid in self.queued_exposes:
            current = self.queued_exposes[mid]
        self.queued_exposes[mid] = not current
        self._refresh_pending_bar()
        if self.selected_provider is not None:
            self._load_models_for_provider(self.selected_provider)

    def _provider_list(self) -> list[str]:
        # Use the full configured-provider list, not just the providers
        # currently used by models in the family, so the user can add
        # the first model for a fresh family via the 'a' dialog.
        if self.available_providers:
            return list(self.available_providers)
        return sorted({m.provider_id for m in self.registry.models_by_family(self.family)})

    def _families_list(self) -> list[str]:
        """Every family the add/edit dialogs may target: families with
        models in the registry plus explicitly created-but-empty ones
        (state.families), sorted."""
        return sorted(set(self.registry.families()) | set(self.state.families))

    def action_add_model(self) -> None:
        from .forms import ModelForm

        providers = self._provider_list() or ["ollama", "llamacpp", "omlx"]
        # Pre-select the provider the user is currently looking at, so
        # adding "another llamacpp model" doesn't make them switch the
        # dropdown back. Fall back to None if no provider is selected.
        default_provider = self.selected_provider if self.selected_provider in providers else None
        self.app.push_screen(
            ModelForm(
                providers=providers,
                default_provider=default_provider,
                families=self._families_list(),
                family=self.family,
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
        self.queued_downloads[variant["id"]] = variant
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
        # Only allow queuing a delete for models that are actually on
        # disk; otherwise the delete has nothing to remove.
        if not self._is_downloaded(mid):
            return
        spec = _model_entry_to_variant(entry)
        if mid in self.queued_deletes:
            self.queued_deletes.pop(mid)
        else:
            self.queued_deletes[mid] = spec
        self.queued_downloads.pop(mid, None)
        self._refresh_pending_bar()
        if self.selected_provider is not None:
            self._load_models_for_provider(self.selected_provider)

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
            ),
            self._on_edit_model,
        )

    def on_data_table_row_selected(self, event: DataTable.RowSelected) -> None:
        """Enter on a model-table row opens the edit dialog. Enter
        on a provider-table row (the focused one on mount) is a
        no-op here — arrow keys already switch providers via the
        RowHighlighted handler.

        The screen-level 'enter' binding (action_select_row) takes
        priority over DataTable.select_cursor so this handler is
        rarely fired in practice; we keep it as a fallback path.
        """
        if event.data_table.id == "model-table":
            self.action_edit_model()

    def action_select_row(self) -> None:
        """Screen-level Enter handler. Wins over DataTable's
        built-in select_cursor binding because the binding is
        marked priority=True.

        - If the model table has focus, Enter opens the edit dialog
          for the highlighted model (preserving the earlier
          "Enter on a model row opens edit" UX).
        - If the provider table has focus (the default on mount),
          Enter is a no-op: arrows already switch the right pane via
          RowHighlighted, and we don't want to invoke an action the
          user didn't explicitly choose.

        Why this lives at the screen level instead of relying on
        DataTable.select_cursor: provider-table now has focus on
        mount (so the user sees a cursor highlight on the first
        configured provider row, per recent UX feedback). With
        provider-table focused, DataTable's built-in select_cursor
        binding for Enter fires on the provider table, not the
        model table. priority=True on the screen binding routes
        Enter through this action so Enter-on-model still works
        after the user tabs into the model pane.
        """
        try:
            mt = self.query_one("#model-table", DataTable)
            pt = self.query_one("#provider-table", DataTable)
        except Exception:
            return
        if mt.has_focus:
            self.action_edit_model()
        # If the provider table has focus, Enter is intentionally a
        # no-op. The visual cursor is on the provider row; the user
        # can switch providers with arrows or Tab to the model pane.
        elif pt.has_focus:
            return
        # Fallback for rare states where neither table has focus:
        # do nothing rather than guessing.

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
        if updated["id"] in self.queued_downloads:
            self.queued_downloads[updated["id"]] = updated
        self.reload()
        self._refresh_pending_bar()

    def action_back(self) -> None:
        if (
            not self.queued_downloads
            and not self.queued_deletes
            and not self.queued_moves
            and not self.queued_exposes
        ):
            self.app.pop_screen()
            return
        from .forms import ConfirmExitDialog

        self.app.push_screen(
            ConfirmExitDialog(
                downloads=list(self.queued_downloads.values()),
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
            self.queued_downloads.clear()
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
        self.state in place (mark_downloaded, remove deleted models).
        The on_event callable receives lifecycle tags; on_progress
        receives per-line provider output forwarded verbatim into the
        log.
        """
        from ..providers.registry import ProviderRegistry

        providers: dict[str, object] = {}
        for spec in list(self.queued_downloads.values()) + list(self.queued_deletes.values()):
            try:
                entry = self.registry.provider(spec["provider"])
                providers[spec["provider"]] = ProviderRegistry.get(
                    spec["provider"], provider_config(entry)
                )
            except Exception:
                continue
        # Merge any reconciled entries that state didn't know about,
        # so the saved state reflects reality. Never remove existing
        # entries; the user can queue a delete (d) for that. This also
        # bridges the expose gate: the TUI accepts an expose based on
        # the reconcile overlay, while the apply-time check reads
        # state.downloaded — without this merge a model on disk with a
        # stale downloaded=False would fail at apply with a spurious
        # "not downloaded".
        for mid, rec in self.reconciled.items():
            if rec.get("downloaded") and not self.state.get(mid).downloaded:
                updated = replace(self.state.get(mid), downloaded=True)
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
            downloads=[(mid, spec) for mid, spec in self.queued_downloads.items()],
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
        self.queued_downloads.clear()
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
