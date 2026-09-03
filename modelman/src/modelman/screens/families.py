"""FamilyScreen — default view listing all configured families."""

from __future__ import annotations

from pathlib import Path

from textual.app import ComposeResult
from textual.screen import Screen
from textual.widgets import DataTable, Footer, Header, Static

from ..providers.registry import ProviderRegistry
from ..registry import (
    FamilyEntry,
    Registry,
    RegistryError,
    family_display_name,
    is_local_location,
    known_families,
    load_registry,
    model_has_local_artifact,
    provider_config,
    save_registry,
)
from ..state import StateStore, load_state, save_state
from . import reload_preserving_cursor
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
        ("q", "quit", "Quit"),
    ]

    def __init__(self) -> None:
        super().__init__()
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
        # True while a background reconcile worker is running; used to
        # gate destructive actions and disable the table so the user
        # can't click a row while the row contents are about to mutate.
        self._reconciling: bool = False
        # Monotonic counter identifying the current reconcile worker.
        # _start_reconcile_worker bumps it and hands each worker its own
        # generation; _reconcile_done only clears _reconciling when the
        # finishing worker's generation is still current. This is what
        # makes "a superseded worker does not clear the flag" actually
        # true: Textual thread workers run their finally block even after
        # exclusive=True cancels them, so without the generation check the
        # first (cancelled) worker would unlock the UI while the second is
        # still scanning.
        self._reconcile_generation: int = 0

    def compose(self) -> ComposeResult:
        yield Header()
        yield DataTable(id="family-table", cursor_type="row")
        yield Static("Refreshing sizes…", id="refresh-indicator")
        yield Footer()

    def on_mount(self) -> None:
        # Hide the refresh indicator until _set_refresh_ui flips it on;
        # Static doesn't accept `display` as a constructor kwarg.
        self.query_one("#refresh-indicator", Static).display = False
        table = self.query_one(DataTable)
        table.add_columns("FAMILY", "DISPLAY", "VARIANTS", "DOWNLOADED", "SIZE")
        self._load_from_disk()
        self.reload()
        # Reconcile against provider state so the size and downloaded columns
        # reflect reality even when modelman.toml is stale. In-memory only.
        self._start_reconcile_worker()

    def _start_reconcile_worker(self) -> None:
        """Mark the screen as reconciling, lock the table, show a
        visible indicator, and dispatch the worker. Called on mount,
        on resume, and on the `r` binding."""
        self._reconciling = True
        self._set_refresh_ui(True)
        self._reconcile_generation += 1
        generation = self._reconcile_generation
        self.run_worker(
            lambda: self._run_reconcile(generation),
            exclusive=True,
            thread=True,
        )

    def _set_refresh_ui(self, refreshing: bool) -> None:
        """Toggle the table disabled state and the indicator widget.

        Disabling the DataTable prevents the user from selecting a row
        that's about to mutate out from under their cursor while a
        background reconcile worker is running. Textual blurs a focused
        widget when it is disabled, so when the table is re-enabled we
        restore focus to it (unless the user has focused something else
        meanwhile) — otherwise the screen is left with nothing focused
        and keys act on it only after a manual Tab.
        """
        table = self.query_one("#family-table", DataTable)
        indicator = self.query_one("#refresh-indicator", Static)
        table.disabled = refreshing
        indicator.display = refreshing
        if not refreshing and table.screen.focused is None:
            table.focus()

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
        from ..registry import sync_agent_providers

        sync_agent_providers(self.registry)
        # Preserve registry.toml's provider insertion order so the left
        # pane's column order matches what the user wrote there (mirrors
        # app.py's _configured_providers()).
        self._available_providers = [p.id for p in self.registry.providers]

    def _refresh_from_disk(self) -> None:
        """Reload registry.toml/modelman.toml and re-run reconcile.

        Used both on screen resume (after popping back from a child
        screen that may have mutated the on-disk files — ModelScreen
        saves on apply) and by the family list's mount path. Reconcile
        now writes state.py directly, so nothing needs clearing between
        runs the way the old in-memory overlay did.
        """
        self._load_from_disk()
        self.reload()  # show state-truth immediately
        self._start_reconcile_worker()

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

    def _run_reconcile(self, generation: int) -> None:
        """Ask each provider whether its models are on disk; write the
        result straight into `state` for local-artifact models (files
        present -> ready=True + disk_path + size_bytes; absent ->
        ready=False + cleared path/size). Non-local-artifact models
        (cloud-located, or on a cloud provider) are never marked ready
        by reconcile — only disk_path/size_bytes are opportunistically
        updated when the provider reports them, mirroring
        ModelScreen._run_reconcile.

        The UI-toggle step (unlock + reload) must run on the main
        thread; `app.call_from_thread` handles that. `generation` is
        this worker's token from _start_reconcile_worker, forwarded to
        _reconcile_done so a superseded worker can be told apart from
        the current one.
        """
        from dataclasses import replace

        try:
            providers: dict[str, object] = {}
            provider_entries: dict[str, object] = {}
            for m in self.registry.models:
                pname = m.provider_id
                if pname not in provider_entries:
                    try:
                        provider_entries[pname] = self.registry.provider(pname)
                    except KeyError:
                        provider_entries[pname] = None
                provider_entry = provider_entries[pname]
                if pname not in providers:
                    if provider_entry is None:
                        continue
                    try:
                        providers[pname] = ProviderRegistry.get(
                            pname, provider_config(provider_entry)
                        )
                    except Exception:
                        continue
                provider = providers.get(pname)
                if provider is None:
                    continue
                spec = _model_entry_to_variant(m)
                size: int | None = None
                ready = False
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
                if model_has_local_artifact(m, provider_entry):
                    existing = self.state.get(m.id)
                    if ready:
                        self.state.set(
                            m.id,
                            replace(existing, ready=True, disk_path=local_path, size_bytes=size),
                        )
                    else:
                        self.state.set(
                            m.id,
                            replace(existing, ready=False, disk_path=None, size_bytes=None),
                        )
                elif local_path is not None or size is not None:
                    existing = self.state.get(m.id)
                    self.state.set(
                        m.id,
                        replace(
                            existing,
                            disk_path=local_path if local_path is not None else existing.disk_path,
                            size_bytes=size if size is not None else existing.size_bytes,
                        ),
                    )
        finally:
            self.app.call_from_thread(self._reconcile_done, generation)

    def _reconcile_done(self, generation: int) -> None:
        """Main-thread side of _run_reconcile: unlock the UI and reload.

        Only the latest worker clears the flag. A superseded worker (one
        whose generation no longer matches) is a no-op — its finally block
        still runs after exclusive=True cancels it, but it must not unlock
        the UI while a newer worker is still scanning.
        """
        if generation != self._reconcile_generation:
            return
        self._reconciling = False
        self._set_refresh_ui(False)
        self.reload()

    def reload(self) -> None:
        def _repopulate() -> None:
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
                    # Non-local entries hold no disk weights and cannot be
                    # duplicates — never count them toward DOWNLOADED/SIZE.
                    # Legacy entries with location=None still count as local.
                    if not is_local_location(m.location):
                        continue
                    st = self.state.get(m.id)
                    if not st.ready:
                        continue
                    downloaded_count += 1
                    if st.size_bytes is None:
                        unknown = True
                    else:
                        total_size += st.size_bytes
                size_str = (
                    "—"
                    if downloaded_count == 0 or (unknown and total_size == 0)
                    else _human_size(total_size)
                )
                table.add_row(
                    family,
                    family_display_name(self.registry, self.state, family) or "",
                    str(variants),
                    str(downloaded_count),
                    size_str,
                    key=family,
                )

        reload_preserving_cursor(self.query_one(DataTable), _repopulate)

    def action_add_family(self) -> None:
        if self._reconciling:
            return

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
        if self._reconciling:
            return
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
        if self._reconciling:
            return
        table = self.query_one(DataTable)
        if table.row_count == 0:
            return
        row_key = list(table.rows.keys())[table.cursor_row]
        family_name = str(row_key.value)
        models = self.registry.models_by_family(family_name)
        variants_count = len(models)

        # Any model — ready or not — blocks delete outright. No
        # confirm-anyway override: the user must remove or move the
        # models first.
        if variants_count > 0:
            self.app.push_screen(
                ConfirmModal(
                    f"Family '{family_name}' has {variants_count} model"
                    f"{'s' if variants_count != 1 else ''}. Remove or move "
                    f"them before deleting this family."
                ),
                self._on_blocked_confirm,
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
        if self._reconciling:
            return
        family_name = str(event.row_key.value) if event.row_key else ""
        if not family_name:
            return
        self._open_family(family_name)

    def action_open_family(self) -> None:
        if self._reconciling:
            return
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
