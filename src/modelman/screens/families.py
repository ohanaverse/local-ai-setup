"""FamilyScreen — default view listing all configured families."""

from __future__ import annotations

from pathlib import Path

from textual.app import ComposeResult
from textual.screen import Screen
from textual.widgets import DataTable, Footer, Header

from ..config import load_config
from ..manifest import FamilyManifest, get_family_dir, load_manifest
from ..providers.registry import ProviderRegistry
from .forms import AddFamilyModal, ConfirmModal, EditFamilyModal
from .models import ModelScreen


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
        # Per-(family, variant) reconcile overlay: {downloaded: bool, size: int|None}.
        # Populated by the background reconcile worker; read by reload() when
        # computing the downloaded count and size column.
        self._reconciled: dict[tuple[str, str], dict] = {}
        # Names of providers configured in ~/.config/local-ai/config.yaml,
        # loaded once on mount and forwarded to ModelScreen on Open so
        # the model screen's provider pane always shows every configured
        # provider, even when the family has zero variants.
        self._available_providers: list[str] = []

    def compose(self) -> ComposeResult:
        yield Header()
        yield DataTable(id="family-table", cursor_type="row")
        yield Footer()

    def on_mount(self) -> None:
        table = self.query_one(DataTable)
        table.add_columns("FAMILY", "DISPLAY", "VARIANTS", "DOWNLOADED", "SIZE")
        # Cache the configured providers so opening a family shows a
        # full provider pane even if the family is empty. Preserve
        # config-file insertion order so the left pane's column order
        # matches what the user wrote in config.yaml.
        try:
            cfg = load_config()
            self._available_providers = list(cfg.providers.keys())
        except Exception:
            self._available_providers = []
        self.reload()
        # Reconcile against provider state so the size and downloaded columns
        # reflect reality even when the manifest is stale. In-memory only.
        self.run_worker(self._run_reconcile, exclusive=True, thread=True)

    def _refresh_from_disk(self) -> None:
        """Clear the cached reconcile overlay and re-run reconcile.

        Used both on screen resume (after popping back from a child
        screen that may have mutated the on-disk manifest) and on
        explicit 'r'. Without clearing, stale (family, vid) entries
        from deleted variants would remain in memory forever and the
        reload() fallback path could pick them up if a new variant
        were later added with the same id.
        """
        self._reconciled.clear()
        self.reload()  # show manifest-truth immediately
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
        try:
            config = load_config()
        except Exception:
            return
        family_dir: Path = get_family_dir()
        if not family_dir.exists():
            return
        # Cache provider instances per provider name.
        providers: dict[str, object] = {}
        for path in sorted(family_dir.glob("*.yaml")):
            try:
                m = load_manifest(path.stem, family_dir=family_dir)
            except Exception:
                continue
            for v in m.variants:
                pname = v["provider"]
                if pname not in providers:
                    try:
                        providers[pname] = ProviderRegistry.get(pname, config.provider(pname))
                    except Exception:
                        continue
                provider = providers[pname]
                size: int | None = None
                try:
                    raw = provider.size_of(v)  # type: ignore[attr-defined]
                    if isinstance(raw, int):
                        size = raw
                except Exception:
                    size = None
                self._reconciled[(m.family, v["id"])] = {
                    "downloaded": size is not None,
                    "size": size,
                }
        self.app.call_from_thread(self.reload)

    def reload(self) -> None:
        table = self.query_one(DataTable)
        table.clear()
        family_dir: Path = get_family_dir()
        if not family_dir.exists():
            return

        for path in sorted(family_dir.glob("*.yaml")):
            try:
                m = load_manifest(path.stem, family_dir=family_dir)
            except Exception:
                continue
            variants = len(m.variants)
            # Compute downloaded count and total size from the reconcile
            # overlay if available, else from the manifest.
            downloaded_count = 0
            total_size = 0
            unknown = False
            for v in m.variants:
                rec = self._reconciled.get((m.family, v["id"]))
                if rec is not None:
                    if rec["downloaded"]:
                        downloaded_count += 1
                        sz = rec["size"]
                        if sz is None:
                            unknown = True
                        else:
                            total_size += sz
                    # If rec says not downloaded, don't fall through to the
                    # manifest (it could be stale on either side).
                else:
                    # No reconcile info yet; trust the manifest for this
                    # family/variant.
                    if v["id"] in m.downloaded:
                        downloaded_count += 1
                        lp = m.downloaded[v["id"]].get("local_path")
                        if lp:
                            # Heuristic: if the path is a real filesystem
                            # path that exists, leave size to the next
                            # reconcile. If we know it's a stub (e.g.
                            # "ollama:<name>"), count as 0 with unknown.
                            from ..providers.registry import ProviderRegistry as _PR

                            try:
                                cfg = load_config()
                                prov = _PR.get(v["provider"], cfg.provider(v["provider"]))
                                raw = prov.size_of(v)  # type: ignore[attr-defined]
                                if isinstance(raw, int) and raw > 0:
                                    total_size += raw
                                elif raw is None:
                                    unknown = True
                            except Exception:
                                unknown = True
            size_str = (
                "—"
                if downloaded_count == 0
                else _human_size(total_size if (total_size > 0 or not unknown) else None)
            )
            table.add_row(
                m.family,
                m.display_name or "",
                str(variants),
                str(downloaded_count),
                size_str,
                key=m.family,
            )

    def action_reconcile(self) -> None:
        self._refresh_from_disk()

    def action_add_family(self) -> None:
        def _on_close(result: FamilyManifest | None) -> None:
            if result is not None:
                self.reload()

        self.app.push_screen(AddFamilyModal(), _on_close)

    def action_edit_family(self) -> None:
        table = self.query_one(DataTable)
        if table.row_count == 0:
            return
        row_key = list(table.rows.keys())[table.cursor_row]
        family_name = str(row_key.value)
        try:
            m = load_manifest(family_name)
        except Exception:
            return

        def _on_close(result: FamilyManifest | None) -> None:
            if result is None:
                return
            # Reload from disk so the table picks up the new
            # display_name. _refresh_from_disk also clears the
            # reconcile overlay, which is correct because variant
            # ids didn't change — the keys stay valid — but matching
            # add/delete (which already do this) keeps behavior
            # uniform.
            self._refresh_from_disk()

        self.app.push_screen(
            EditFamilyModal(
                family=m.family,
                display_name=m.display_name,
            ),
            _on_close,
        )

    def action_delete_family(self) -> None:
        table = self.query_one(DataTable)
        if table.row_count == 0:
            return
        row_key = list(table.rows.keys())[table.cursor_row]
        family_name = str(row_key.value)
        try:
            m = load_manifest(family_name)
        except Exception:
            return

        variants_count = len(m.variants)
        downloaded_count = len(m.downloaded)

        # Deletion is only safe when the family has nothing to lose.
        # The previous check only protected against downloaded entries,
        # which silently dropped families with queued-but-not-yet-
        # downloaded variants when they got bulk-queued for delete and
        # then the user pressed d on the family row. Now we protect
        # against any variants, queued or downloaded. The dialog
        # messages spell out the current state so the user knows which
        # path they're on.
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
            # Family has variant definitions but none have been
            # downloaded yet. Deleting would lose the variant
            # definitions entirely; require explicit confirmation.
            self.app.push_screen(
                ConfirmModal(
                    f"Family '{family_name}' has {variants_count} variant"
                    f"{'s' if variants_count != 1 else ''} (none downloaded). "
                    f"Delete anyway? This loses the variant definitions."
                ),
                self._on_delete_family_with_variants,
            )
            return
        self.app.push_screen(
            ConfirmModal(
                f"Family '{family_name}' is empty. Delete?"
            ),
            self._on_delete_confirm,
        )

    def _on_delete_confirm(self, confirmed: bool | None) -> None:
        if not confirmed:
            return
        self._delete_family_file()

    def _on_delete_family_with_variants(self, confirmed: bool | None) -> None:
        if not confirmed:
            return
        self._delete_family_file()

    def _delete_family_file(self) -> None:
        """Unlink the manifest file for the family currently under
        the cursor, then reload. Shared between the empty-family
        confirmation and the variants-no-download confirmation so
        both paths go through the same destructive code.

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
        path = get_family_dir() / f"{family_name}.yaml"
        if path.exists():
            path.unlink()
        self.reload()

    def _on_blocked_confirm(self, _confirmed: bool | None) -> None:
        return  # informational only

    def on_data_table_row_selected(self, event: DataTable.RowSelected) -> None:
        family_name = str(event.row_key.value) if event.row_key else ""
        if not family_name:
            return
        m = load_manifest(family_name)
        path = get_family_dir() / f"{family_name}.yaml"
        self.app.push_screen(ModelScreen(m, path, self._available_providers))

    def action_open_family(self) -> None:
        table = self.query_one(DataTable)
        if table.row_count == 0:
            return
        row_key = list(table.rows.keys())[table.cursor_row]
        family_name = str(row_key.value)
        m = load_manifest(family_name)
        path = get_family_dir() / f"{family_name}.yaml"
        self.app.push_screen(ModelScreen(m, path, self._available_providers))

    def action_quit(self) -> None:
        self.app.exit()
