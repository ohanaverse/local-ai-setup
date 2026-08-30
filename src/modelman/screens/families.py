"""FamilyScreen — default view listing all configured families."""

from __future__ import annotations

from pathlib import Path

from textual.app import ComposeResult
from textual.screen import Screen
from textual.widgets import DataTable, Footer, Header

from ..providers.registry import ProviderRegistry
from ..registry import (
    FamilyEntry,
    Registry,
    RegistryError,
    family_display_name,
    known_families,
    load_registry,
    provider_config,
    save_registry,
)
from ..state import StateStore, load_state, save_state
from .forms import AddFamilyModal, ConfirmModal, EditFamilyModal
from .models import ModelScreen, _model_entry_to_variant


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


class FamilyScreen(Screen[None]):
    BINDINGS = [
        ("a", "add_family", "Add"),
        ("e", "edit_family", "Edit"),
        ("d", "delete_family", "Delete"),
        ("enter", "open_family", "Open"),
        ("r", "reconcile", "Reconcile"),
        ("q", "quit", "Quit"),
    ]

    def __init__(self) -> None:
        super().__init__()
        # Per-model-id reconcile overlay: {downloaded: bool, size: int|None}.
        # Populated by the background reconcile worker; read by reload() when
        # computing the downloaded count and size column. Keyed by
        # ModelEntry.id (globally unique), matching ModelScreen.reconciled.
        self._reconciled: dict[str, dict] = {}
        # Providers from registry.toml, loaded once on mount and forwarded
        # to ModelScreen on Open so the model screen's provider pane always
        # shows every configured provider, even when the family has zero
        # models.
        self._available_providers: list[str] = []
        # Registry/StateStore + the paths they were loaded from — set in
        # on_mount(), reloaded in _refresh_from_disk() so edits made on
        # ModelScreen (which saves on apply) are picked up on resume.
        self.registry: Registry = Registry()
        self.state: StateStore = StateStore()
        self.registry_path: Path = Path()
        self.state_path: Path = Path()

    def compose(self) -> ComposeResult:
        yield Header()
        yield DataTable(id="family-table", cursor_type="row")
        yield Footer()

    def on_mount(self) -> None:
        table = self.query_one(DataTable)
        table.add_columns("FAMILY", "DISPLAY", "VARIANTS", "DOWNLOADED", "SIZE")
        self._load_from_disk()
        self.reload()
        # Reconcile against provider state so the size and downloaded columns
        # reflect reality even when modelman.toml is stale. In-memory only.
        self.run_worker(self._run_reconcile, exclusive=True, thread=True)

    def _load_from_disk(self) -> None:
        """(Re)load registry.toml + modelman.toml and the derived
        available-providers list. A missing/invalid registry.toml is
        not fatal here (an empty Registry just means an empty family
        table) — ModelScreen's own on_mount has the same tolerance."""
        from ..registry import _default_registry_path
        from ..state import _default_state_path

        self.registry_path = _default_registry_path()
        self.state_path = _default_state_path()
        try:
            self.registry = load_registry(self.registry_path)
        except RegistryError:
            self.registry = Registry()
        self.state = load_state(self.state_path)
        # Preserve registry.toml's provider insertion order so the left
        # pane's column order matches what the user wrote there (mirrors
        # app.py's _configured_providers()).
        self._available_providers = [p.id for p in self.registry.providers]

    def _refresh_from_disk(self) -> None:
        """Reload registry.toml/modelman.toml, clear the cached reconcile
        overlay, and re-run reconcile.

        Used both on screen resume (after popping back from a child
        screen that may have mutated the on-disk files — ModelScreen
        saves on apply) and on explicit 'r'. Without clearing the
        overlay, stale entries for deleted models would remain in
        memory forever.
        """
        self._load_from_disk()
        self._reconciled.clear()
        self.reload()  # show state-truth immediately
        self.run_worker(self._run_reconcile, exclusive=True, thread=True)

    def on_screen_resume(self, event) -> None:
        """Re-reconcile when the screen becomes visible again.

        This screen sits at the bottom of the stack while the user
        edits / downloads / deletes a family. When StatusScreen pops
        and we resume, the manifest on disk and the provider state
        may have changed underneath us: variants may have been added,
        downloads may have completed, files may have been deleted.
        A fresh reconcile makes the SIZE and DOWNLOADED columns
        reflect that reality instead of the snapshot taken on mount.
        Without this, deleting a model leaves the row showing the
        pre-delete size until the next 'r' press or app restart.
        """
        self._refresh_from_disk()

    def _run_reconcile(self) -> None:
        """Ask each provider whether its models are on disk; cache
        results keyed by model id. Mirrors ModelScreen._run_reconcile
        but across every model in the registry (not one family)."""
        providers: dict[str, object] = {}
        for m in self.registry.models:
            pname = m.provider_id
            if pname not in providers:
                try:
                    entry = self.registry.provider(pname)
                    providers[pname] = ProviderRegistry.get(pname, provider_config(entry))
                except Exception:
                    continue
            provider = providers[pname]
            spec = _model_entry_to_variant(m)
            size: int | None = None
            try:
                raw = provider.size_of(spec)  # type: ignore[attr-defined]
                if isinstance(raw, int):
                    size = raw
            except Exception:
                size = None
            self._reconciled[m.id] = {
                "downloaded": size is not None,
                "size": size,
            }
        self.app.call_from_thread(self.reload)

    def reload(self) -> None:
        table = self.query_one(DataTable)
        table.clear()
        # A family is "known" if it has models in the registry, or was
        # explicitly touched in state (e.g. AddFamilyModal for a family
        # with zero models yet) — mirrors the legacy per-family manifest
        # file's existence, which didn't require any variants either.
        families = known_families(self.registry, self.state)
        for family in families:
            models = self.registry.models_by_family(family)
            variants = len(models)
            downloaded_count = 0
            total_size = 0
            unknown = False
            for m in models:
                rec = self._reconciled.get(m.id)
                if rec is not None:
                    if rec["downloaded"]:
                        downloaded_count += 1
                        sz = rec["size"]
                        if sz is None:
                            unknown = True
                        else:
                            total_size += sz
                    # If rec says not downloaded, don't fall through to
                    # state (it could be stale on either side).
                elif self.state.get(m.id).downloaded:
                    # No reconcile info yet; trust state for this model.
                    downloaded_count += 1
                    unknown = True  # size unknown until reconcile runs
            size_str = (
                "—"
                if downloaded_count == 0
                else _human_size(total_size if (total_size > 0 or not unknown) else None)
            )
            table.add_row(
                family,
                family_display_name(self.registry, self.state, family) or "",
                str(variants),
                str(downloaded_count),
                size_str,
                key=family,
            )

    def action_reconcile(self) -> None:
        self._refresh_from_disk()

    def action_add_family(self) -> None:
        def _on_close(result: tuple[str, str] | None) -> None:
            if result is None:
                return
            family, display_name = result
            self._upsert_family_entry(family, display_name)
            save_registry(self.registry, self.registry_path)
            save_state(self.state, self.state_path)
            self.reload()

        self.app.push_screen(AddFamilyModal(), _on_close)

    def action_edit_family(self) -> None:
        table = self.query_one(DataTable)
        if table.row_count == 0:
            return
        row_key = list(table.rows.keys())[table.cursor_row]
        family_name = str(row_key.value)

        def _on_close(display_name: str | None) -> None:
            if display_name is None:
                return
            self._upsert_family_entry(family_name, display_name)
            save_registry(self.registry, self.registry_path)
            save_state(self.state, self.state_path)
            # _refresh_from_disk also clears the reconcile overlay; model
            # ids didn't change so the keys stay valid, but matching
            # add/delete (which already do this) keeps behavior uniform.
            self._refresh_from_disk()

        self.app.push_screen(
            EditFamilyModal(
                family=family_name,
                display_name=family_display_name(self.registry, self.state, family_name)
                or family_name,
            ),
            _on_close,
        )

    def action_delete_family(self) -> None:
        table = self.query_one(DataTable)
        if table.row_count == 0:
            return
        row_key = list(table.rows.keys())[table.cursor_row]
        family_name = str(row_key.value)
        models = self.registry.models_by_family(family_name)
        variants_count = len(models)
        downloaded_count = sum(1 for m in models if self.state.get(m.id).downloaded)

        # Deletion is only safe when the family has nothing to lose.
        # Protect against any models, queued-download or downloaded.
        # The dialog messages spell out the current state so the user
        # knows which path they're on.
        if downloaded_count > 0:
            self.app.push_screen(
                ConfirmModal(
                    f"Cannot delete '{family_name}': {downloaded_count} "
                    f"downloaded model{'s' if downloaded_count != 1 else ''} "
                    f"of {variants_count} variant{'s' if variants_count != 1 else ''}. "
                    f"Remove downloads first."
                ),
                self._on_blocked_confirm,
            )
            return
        if variants_count > 0:
            # Family has model definitions but none have been
            # downloaded yet. Deleting would lose the model
            # definitions entirely; require explicit confirmation.
            self.app.push_screen(
                ConfirmModal(
                    f"Family '{family_name}' has {variants_count} variant"
                    f"{'s' if variants_count != 1 else ''} (none downloaded). "
                    f"Delete anyway? This loses the model definitions."
                ),
                self._on_delete_family_with_variants,
            )
            return
        self.app.push_screen(
            ConfirmModal(f"Family '{family_name}' is empty. Delete?"),
            self._on_delete_confirm,
        )

    def _on_delete_confirm(self, confirmed: bool | None) -> None:
        if not confirmed:
            return
        self._delete_family()

    def _on_delete_family_with_variants(self, confirmed: bool | None) -> None:
        if not confirmed:
            return
        self._delete_family()

    def _upsert_family_entry(self, name: str, display_name: str) -> None:
        """Write (or update) the first-class [[families]] entry for `name`
        and drop any legacy state.families entry (promotion)."""
        entry = self.registry.family(name)
        if entry is None:
            self.registry.families.append(FamilyEntry(name=name, display_name=display_name))
        else:
            entry.display_name = display_name
        self.state.forget_family(name)

    def _delete_family(self) -> None:
        """Remove every model in the currently-selected family from
        the registry, drop its state entries and its `families` state
        entry, save both files, then reload. Shared between the
        empty-family confirmation and the variants-no-download
        confirmation so both paths go through the same destructive
        code.

        The cursor_row is re-read here rather than cached at modal
        open: while the modal was up the table did not change, so
        the cursor points at the same row the user selected when
        they pressed 'd'. Reading again here keeps each action
        callback self-contained and avoids stale-key bugs if the
        modal was triggered from a context that mutated the table.
        """
        table = self.query_one(DataTable)
        row_key = list(table.rows.keys())[table.cursor_row]
        family_name = str(row_key.value)
        removed_ids = {m.id for m in self.registry.models if m.family == family_name}
        self.registry.models = [m for m in self.registry.models if m.family != family_name]
        # Remove the first-class [[families]] entry too, so an emptied
        # family is gone for good after an explicit delete.
        self.registry.families = [f for f in self.registry.families if f.name != family_name]
        for mid in removed_ids:
            self.state.models.pop(mid, None)
        self.state.forget_family(family_name)
        save_registry(self.registry, self.registry_path)
        save_state(self.state, self.state_path)
        self.reload()

    def _on_blocked_confirm(self, _confirmed: bool | None) -> None:
        return  # informational only

    def on_data_table_row_selected(self, event: DataTable.RowSelected) -> None:
        family_name = str(event.row_key.value) if event.row_key else ""
        if not family_name:
            return
        self._open_family(family_name)

    def action_open_family(self) -> None:
        table = self.query_one(DataTable)
        if table.row_count == 0:
            return
        row_key = list(table.rows.keys())[table.cursor_row]
        self._open_family(str(row_key.value))

    def _open_family(self, family_name: str) -> None:
        self.app.push_screen(
            ModelScreen(
                registry=self.registry,
                state=self.state,
                family=family_name,
                registry_path=self.registry_path,
                state_path=self.state_path,
                available_providers=self._available_providers,
            )
        )

    def action_quit(self) -> None:
        self.app.exit()
