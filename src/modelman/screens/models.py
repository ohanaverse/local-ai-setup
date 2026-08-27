"""ModelScreen — drill into a family's models grouped by provider."""

from __future__ import annotations

from collections import defaultdict
from collections.abc import Callable
from pathlib import Path
from typing import TYPE_CHECKING, cast

from textual.app import ComposeResult
from textual.binding import Binding
from textual.containers import Horizontal, Vertical
from textual.coordinate import Coordinate
from textual.screen import Screen
from textual.widgets import DataTable, Footer, Header, Static

from ..config import load_config
from ..providers.registry import ProviderRegistry
from ..queue import PendingChanges

if TYPE_CHECKING:
    from ..manifest import FamilyManifest
    from ..providers.base import VariantSpec


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


class ModelScreen(Screen[None]):
    BINDINGS = [
        ("escape", "back", "Back"),
        ("x", "toggle_download", "Toggle download"),
        ("a", "add_model", "Add"),
        ("d", "delete_model", "Delete"),
        ("e", "edit_model", "Edit"),
        Binding("enter", "select_row", "Edit", priority=True),
        ("r", "reconcile", "Reconcile"),
    ]

    def __init__(
        self,
        manifest: FamilyManifest,
        manifest_path: Path,
        available_providers: list[str] | None = None,
    ) -> None:
        super().__init__()
        self.manifest = manifest
        self.manifest_path = manifest_path
        # The list of providers configured in ~/.config/local-ai/config.yaml.
        # The provider-table on the left always shows every entry here, even
        # when the family has no variants at all — otherwise an empty family
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
        self.queued_downloads: dict[str, VariantSpec] = {}
        self.queued_deletes: dict[str, VariantSpec] = {}
        # Reconcile overlay: per-variant reality from the provider, populated by
        # action_reconcile / on_mount. Rendering prefers this over manifest.
        self.reconciled: dict[str, dict] = {}
        # Snapshot for discard: restore if the user exits without applying.
        self._snapshot_variants: list[VariantSpec] = [
            cast("VariantSpec", dict(v)) for v in manifest.variants
        ]
        self._snapshot_downloaded: dict[str, dict] = {
            k: dict(v) for k, v in manifest.downloaded.items()
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
        mt.add_columns("NAME", "STATUS", "SIZE", "PATH")
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
        # manifest is stale. In-memory only; nothing is written to disk
        # until the user applies.
        self.run_worker(self._run_reconcile, exclusive=True, thread=True)

    def _run_reconcile(self) -> None:
        """Ask each provider whether its variants are on disk; cache results.

        Uses size_of (cheap; queries ollama list / HF cache / model dir) as
        the source of truth for "downloaded", avoiding the slower is_downloaded
        path which loads the model.
        """
        try:
            config = load_config()
        except Exception:
            return
        # Group variants by provider so we can reuse a single size_of call.
        by_provider: dict[str, list[VariantSpec]] = defaultdict(list)
        for v in self.manifest.variants:
            by_provider[v["provider"]].append(v)
        for provider_name, variants in by_provider.items():
            try:
                provider = ProviderRegistry.get(provider_name, config.provider(provider_name))
            except Exception:
                continue
            # Build a name -> size map per provider when possible. Not all
            # providers expose this (ollama does, llamacpp/omlx work per-variant).
            for v in variants:
                size: int | None = None
                try:
                    raw = provider.size_of(v)
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
                            if lm_name == v.get("name") or lm_name == v["id"]:
                                lp = lm.get("local_path") or lm.get("path")
                                if isinstance(lp, str):
                                    local_path = lp
                                break
                    except Exception:
                        pass
                self.reconciled[v["id"]] = {
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
        # the user has somewhere to add their first variant. Counts
        # come from the variants list (0 when none).
        counts: dict[str, int] = defaultdict(int)
        for v in self.manifest.variants:
            counts[v["provider"]] += 1
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

    def _is_downloaded(self, vid: str) -> bool:
        """Truth about whether a variant is on disk.

        Prefers the reconcile overlay (reality); falls back to the manifest
        when reconcile hasn't run for this variant yet.
        """
        rec = self.reconciled.get(vid)
        if rec is not None:
            return bool(rec.get("downloaded"))
        return vid in self.manifest.downloaded

    def _load_models_for_provider(self, provider: str) -> None:
        mt = self.query_one("#model-table", DataTable)
        mt.clear()
        for v in self.manifest.variants:
            if v["provider"] != provider:
                continue
            rec = self.reconciled.get(v["id"])
            if rec is not None:
                downloaded = bool(rec.get("downloaded"))
                size_str = _human_size(rec.get("size")) if downloaded else "—"
                path = rec.get("local_path") or (
                    self.manifest.downloaded.get(v["id"], {}).get("local_path", "—")
                )
            else:
                downloaded = v["id"] in self.manifest.downloaded
                size_str = "—"
                path = self.manifest.downloaded.get(v["id"], {}).get("local_path", "—")
                if downloaded:
                    try:
                        cfg = load_config()
                        p = ProviderRegistry.get(provider, cfg.provider(provider))
                        size_str = _human_size(p.size_of(v))
                    except Exception:
                        pass
            # Four-state status: takes precedence over current disk state.
            if v["id"] in self.queued_deletes:
                status = "[red]✗[/red]"
            elif v["id"] in self.queued_downloads:
                status = "[yellow]↓[/yellow]"
            elif downloaded:
                status = "[green]✓[/green]"
            else:
                status = "[dim]○[/dim]"
            mt.add_row(v["name"], status, size_str, path, key=v["id"])

    def _refresh_pending_bar(self) -> None:
        bar = self.query_one("#pending-bar", Static)
        bar.update(
            f"Pending: download {len(self.queued_downloads)} · delete {len(self.queued_deletes)}"
        )

    def action_toggle_download(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        vid = str(row_key.value)
        variant = self.manifest.variant_by_id(vid)
        if variant is None:
            return
        if self._is_downloaded(vid):
            return  # already downloaded
        if vid in self.queued_downloads:
            self.queued_downloads.pop(vid)
        else:
            self.queued_downloads[vid] = variant
        self._refresh_pending_bar()
        if self.selected_provider is not None:
            self._load_models_for_provider(self.selected_provider)

    def _provider_list(self) -> list[str]:
        # Use the full configured-provider list, not just the providers
        # currently used by variants, so the user can add the first
        # model for a fresh family via the 'a' dialog.
        if self.available_providers:
            return list(self.available_providers)
        return sorted({v["provider"] for v in self.manifest.variants})

    def action_add_model(self) -> None:
        from .forms import ModelForm

        providers = self._provider_list() or ["ollama", "llamacpp", "omlx"]
        # Pre-select the provider the user is currently looking at, so
        # adding "another llamacpp model" doesn't make them switch the
        # dropdown back. Fall back to None if no provider is selected.
        default_provider = (
            self.selected_provider
            if self.selected_provider in providers
            else None
        )
        self.app.push_screen(
            ModelForm(providers=providers, default_provider=default_provider),
            self._on_add_model,
        )

    def _on_add_model(self, variant) -> None:
        if variant is None:
            return
        if self.manifest.variant_by_id(variant["id"]):
            self.app.notify("Model ID already exists")
            return
        self.manifest.variants.append(variant)
        self.queued_downloads[variant["id"]] = variant
        self.reload()
        self._refresh_pending_bar()

    def action_delete_model(self) -> None:
        mt = self.query_one("#model-table", DataTable)
        if mt.row_count == 0:
            return
        row_key = list(mt.rows.keys())[mt.cursor_row]
        vid = str(row_key.value)
        variant = self.manifest.variant_by_id(vid)
        if variant is None:
            return
        # Only allow queuing a delete for variants that are actually on
        # disk; otherwise the delete has nothing to remove.
        if not self._is_downloaded(vid):
            return
        if vid in self.queued_deletes:
            self.queued_deletes.pop(vid)
        else:
            self.queued_deletes[vid] = variant
        self.queued_downloads.pop(vid, None)
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
        vid = str(row_key.value)
        variant = self.manifest.variant_by_id(vid)
        if variant is None:
            return
        from .forms import ModelForm

        self.app.push_screen(
            ModelForm(providers=self._provider_list(), variant=variant),
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


    def _on_edit_model(self, updated) -> None:
        if updated is None:
            return
        for i, v in enumerate(self.manifest.variants):
            if v["id"] == updated["id"]:
                self.manifest.variants[i] = updated
                break
        if updated["id"] in self.queued_downloads:
            self.queued_downloads[updated["id"]] = updated
        self.reload()

    def action_back(self) -> None:
        if not self.queued_downloads and not self.queued_deletes:
            self.app.pop_screen()
            return
        from .forms import ConfirmExitDialog

        self.app.push_screen(
            ConfirmExitDialog(
                downloads=list(self.queued_downloads.values()),
                deletes=list(self.queued_deletes.values()),
            ),
            self._on_exit_confirm,
        )

    def _on_exit_confirm(self, choice: str | None) -> None:
        if choice == "apply":
            self._push_status_screen()
            return
        if choice == "discard":
            self._restore_snapshot()
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
        self.app.push_screen(StatusScreen(family=self.manifest.family, run_apply=self._run_apply))

    def _run_apply(
        self,
        on_event: Callable[[str], None],
        on_progress: Callable[[str], None],
        register: Callable[[PendingChanges], None],
    ) -> None:
        """Construct a PendingChanges and drive apply().

        Passed as a closure to StatusScreen. Mutates self.manifest in place
        (mark_downloaded, remove deleted variants). The on_event callable
        receives lifecycle tags; on_progress receives per-line provider
        output forwarded verbatim into the log.
        """
        try:
            config = load_config()
        except Exception:
            on_event("apply:done")
            return
        providers: dict[str, object] = {}
        for v in list(self.queued_downloads.values()) + list(self.queued_deletes.values()):
            try:
                providers[v["provider"]] = ProviderRegistry.get(
                    v["provider"], config.provider(v["provider"])
                )
            except Exception:
                continue
        # Merge any reconciled entries that the manifest didn't know about,
        # so the saved manifest reflects reality. Never remove existing
        # entries; the user can queue a delete (d) for that.
        for vid, rec in self.reconciled.items():
            if rec.get("downloaded") and vid not in self.manifest.downloaded:
                local_path = rec.get("local_path") or ""
                self.manifest.mark_downloaded(vid, local_path)
        pending = PendingChanges(
            manifest=self.manifest,
            manifest_path=self.manifest_path,
            providers=providers,
            downloads=list(self.queued_downloads.values()),
            deletes=list(self.queued_deletes.values()),
        )
        register(pending)
        pending.apply(on_event=on_event, on_progress=on_progress)
        # The closure runs on the StatusScreen's worker thread; mutate
        # in-memory queue state from here too so subsequent opens of this
        # screen see an empty queue.
        self.queued_downloads.clear()
        self.queued_deletes.clear()

    def _restore_snapshot(self) -> None:
        self.manifest.variants = [cast("VariantSpec", dict(v)) for v in self._snapshot_variants]
        self.manifest.downloaded = {k: dict(v) for k, v in self._snapshot_downloaded.items()}
