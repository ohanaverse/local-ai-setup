"""Tests for editing a family's display name from the FamilyScreen.

Today the family screen supports add/delete/open/reconcile/quit but
not edit. The most useful edit on a family is its display_name (the
human-readable label shown in the table and the parent screen of
the ModelScreen). The family slug (the file key) is intentionally
NOT editable: changing it would orphan the on-disk manifest or
break every cross-reference.
"""

from __future__ import annotations

import pytest

from modelman.app import ModelmanApp
from modelman.manifest import FamilyManifest, save_manifest
from modelman.screens.forms import EditFamilyModal

# ---------------------------------------------------------------------------
# Modal-level tests
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_edit_family_modal_pre_fills_display_name():
    """Modal opens pre-filled with the existing display_name so the
    user doesn't have to retype from scratch."""
    modal = EditFamilyModal(
        family="ornith-1.5",
        display_name="Ornith 1.5 (preview)",
    )

    async with ModelmanApp().run_test() as pilot:
        result = []
        app = pilot.app
        app.push_screen(modal, lambda r: result.append(r))
        await pilot.pause()

        # The display_name input should be pre-filled.
        from textual.widgets import Input

        display_input = modal.query_one("#display-name", Input)
        assert display_input.value == "Ornith 1.5 (preview)"

        # The family slug input (if shown) must be disabled / read-only.
        slug_input = modal.query_one("#family-name", Input)
        assert slug_input.value == "ornith-1.5"
        assert slug_input.disabled, (
            "Family slug must not be editable from the edit modal; "
            "changing it would orphan or break the manifest file."
        )

        pilot.app.pop_screen()


@pytest.mark.asyncio
async def test_edit_family_modal_save_persists_new_display_name(
    tmp_path, monkeypatch
):
    """Submitting the form writes the new display_name back to the
    manifest on disk and dismisses with the updated manifest."""
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(tmp_path))
    m = FamilyManifest(family="ornith-1.5", display_name="Ornith 1.5")
    save_manifest(m, tmp_path / "ornith-1.5.yaml")

    modal = EditFamilyModal(
        family="ornith-1.5",
        display_name="Ornith 1.5",
    )

    async with ModelmanApp().run_test() as pilot:
        result = []
        app = pilot.app
        app.push_screen(modal, lambda r: result.append(r))
        await pilot.pause()

        from textual.widgets import Input

        modal.query_one("#display-name", Input).value = "Ornith v1.5 (renamed)"
        # Trigger submit via Enter on the Input (most common path):
        # emulate by pressing the Save button.
        from textual.widgets import Button

        modal.query_one("#save", Button).press()
        await pilot.pause()

    assert len(result) == 1
    returned = result[0]
    assert returned is not None
    assert returned.family == "ornith-1.5"          # slug unchanged
    assert returned.display_name == "Ornith v1.5 (renamed)"

    # On-disk updated.
    from modelman.manifest import load_manifest

    on_disk = load_manifest("ornith-1.5", family_dir=tmp_path)
    assert on_disk.display_name == "Ornith v1.5 (renamed)"


@pytest.mark.asyncio
async def test_edit_family_modal_cancel_does_not_write(tmp_path, monkeypatch):
    """Cancel must dismiss with None and leave the manifest untouched."""
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(tmp_path))
    m = FamilyManifest(family="ornith-1.5", display_name="Ornith 1.5")
    save_manifest(m, tmp_path / "ornith-1.5.yaml")

    modal = EditFamilyModal(family="ornith-1.5", display_name="Ornith 1.5")

    async with ModelmanApp().run_test() as pilot:
        result = []
        pilot.app.push_screen(modal, lambda r: result.append(r))
        await pilot.pause()

        from textual.widgets import Button, Input

        modal.query_one("#display-name", Input).value = "STRANGER CHANGES"
        modal.query_one("#cancel", Button).press()
        await pilot.pause()

    assert result == [None]
    from modelman.manifest import load_manifest

    on_disk = load_manifest("ornith-1.5", family_dir=tmp_path)
    assert on_disk.display_name == "Ornith 1.5"


@pytest.mark.asyncio
async def test_edit_family_modal_empty_display_falls_back_to_family(
    tmp_path, monkeypatch
):
    """If the user blanks the display_name, fall back to using the
    family slug (matches AddFamilyModal's behavior)."""
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(tmp_path))
    modal = EditFamilyModal(family="ornith-1.5", display_name="Ornith 1.5")

    async with ModelmanApp().run_test() as pilot:
        result = []
        pilot.app.push_screen(modal, lambda r: result.append(r))
        await pilot.pause()

        from textual.widgets import Button, Input

        modal.query_one("#display-name", Input).value = ""
        modal.query_one("#save", Button).press()
        await pilot.pause()

    assert len(result) == 1 and result[0] is not None
    assert result[0].display_name == "ornith-1.5"  # fell back to slug


# ---------------------------------------------------------------------------
# App-level: 'e' opens the edit modal pre-filled
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_family_screen_e_opens_edit_modal_for_selected_row(
    tmp_path, monkeypatch
):
    """Pressing 'e' on a row in the family table must push an
    EditFamilyModal pre-filled with that family's data."""

    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(tmp_path))
    m = FamilyManifest(family="ornith-1.5", display_name="Ornith 1.5")
    save_manifest(m, tmp_path / "ornith-1.5.yaml")

    # Minimal provider config so reconcile doesn't error.
    cfg_path = tmp_path / "config.yaml"
    cfg_path.write_text("providers:\n  ollama:\n    type: ollama\n")
    monkeypatch.setenv("MODELMAN_CONFIG", str(cfg_path))

    from unittest.mock import MagicMock

    from modelman.providers import registry
    from modelman.screens.forms import EditFamilyModal

    stub = MagicMock()
    stub.name = "ollama"
    stub.size_of.return_value = None
    monkeypatch.setattr(
        registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub)
    )

    app = ModelmanApp()
    captured: list[EditFamilyModal] = []
    original_push = app.push_screen

    def tracking_push(screen, *args, **kwargs):
        if isinstance(screen, EditFamilyModal):
            captured.append(screen)
        return original_push(screen, *args, **kwargs)

    monkeypatch.setattr(app, "push_screen", tracking_push)

    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("e")
        await pilot.pause()

    assert len(captured) == 1, f"expected EditFamilyModal push; got {captured}"
    modal = captured[0]
    assert modal._family == "ornith-1.5"
    assert modal._display_name == "Ornith 1.5"


@pytest.mark.asyncio
async def test_family_screen_e_with_no_rows_is_a_noop(tmp_path, monkeypatch):
    """If the table is empty, 'e' should not crash or push a modal."""
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(tmp_path))
    cfg_path = tmp_path / "config.yaml"
    cfg_path.write_text("providers:\n  ollama:\n    type: ollama\n")
    monkeypatch.setenv("MODELMAN_CONFIG", str(cfg_path))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        # Pressing 'e' with empty table must not raise.
        await pilot.press("e")
        await pilot.pause()
