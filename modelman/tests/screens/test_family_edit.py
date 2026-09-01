"""Tests for editing a family's display name from the FamilyScreen.

Today the family screen supports add/delete/open/reconcile/quit but
not edit. The most useful edit on a family is its display_name (the
human-readable label shown in the table and the parent screen of
the ModelScreen). The family slug (the file key) is intentionally
NOT editable: changing it would orphan cross-references from models
keyed by family.

Migrated in PR 3 (2026-08-27-shared-model-registry-phase2-pr3) off
FamilyManifest/families/*.yaml onto Registry (registry.toml) +
StateStore (modelman.toml). AddFamilyModal/EditFamilyModal no longer
write to disk themselves — they return plain data and FamilyScreen
performs the StateStore mutation + save (see forms.py).
"""

from __future__ import annotations

import pytest

from modelman.app import ModelmanApp
from modelman.registry import AuthConfig, ProviderEntry, Registry, save_registry
from modelman.screens.forms import EditFamilyModal
from modelman.state import FamilyState, StateStore, load_state, save_state

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

        from textual.widgets import Input

        display_input = modal.query_one("#display-name", Input)
        assert display_input.value == "Ornith 1.5 (preview)"

        slug_input = modal.query_one("#family-name", Input)
        assert slug_input.value == "ornith-1.5"
        assert slug_input.disabled, (
            "Family slug must not be editable from the edit modal; "
            "changing it would orphan cross-references from models."
        )

        pilot.app.pop_screen()


@pytest.mark.asyncio
async def test_edit_family_modal_save_returns_new_display_name():
    """Submitting the form dismisses with the new display_name string.
    The modal itself performs no disk I/O — FamilyScreen (Task 5)
    is what persists it to modelman.toml."""
    modal = EditFamilyModal(
        family="ornith-1.5",
        display_name="Ornith 1.5",
    )

    async with ModelmanApp().run_test() as pilot:
        result = []
        app = pilot.app
        app.push_screen(modal, lambda r: result.append(r))
        await pilot.pause()

        from textual.widgets import Button, Input

        modal.query_one("#display-name", Input).value = "Ornith v1.5 (renamed)"
        modal.query_one("#save", Button).press()
        await pilot.pause()

    assert result == ["Ornith v1.5 (renamed)"]


@pytest.mark.asyncio
async def test_edit_family_modal_cancel_returns_none():
    """Cancel must dismiss with None."""
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


@pytest.mark.asyncio
async def test_edit_family_modal_empty_display_falls_back_to_family():
    """If the user blanks the display_name, fall back to using the
    family slug (matches AddFamilyModal's behavior)."""
    modal = EditFamilyModal(family="ornith-1.5", display_name="Ornith 1.5")

    async with ModelmanApp().run_test() as pilot:
        result = []
        pilot.app.push_screen(modal, lambda r: result.append(r))
        await pilot.pause()

        from textual.widgets import Button, Input

        modal.query_one("#display-name", Input).value = ""
        modal.query_one("#save", Button).press()
        await pilot.pause()

    assert result == ["ornith-1.5"]  # fell back to slug


# ---------------------------------------------------------------------------
# App-level: 'e' opens the edit modal and persists to modelman.toml
# ---------------------------------------------------------------------------


def _seed(tmp_path, monkeypatch, *, family_state: FamilyState | None = None):
    """Seed an empty registry.toml (no models needed for these tests)
    and a modelman.toml with one known family, and point the app's
    env vars at tmp_path. Returns (registry_path, state_path)."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    save_registry(
        Registry(providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))]),
        reg_path,
    )
    store = StateStore()
    if family_state is not None:
        store.families["ornith-1.5"] = family_state
    save_state(store, state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    return reg_path, state_path


@pytest.mark.asyncio
async def test_family_screen_e_opens_edit_modal_for_selected_row(tmp_path, monkeypatch):
    """Pressing 'e' on a row in the family table must push an
    EditFamilyModal pre-filled with that family's data."""
    _reg_path, _state_path = _seed(
        tmp_path, monkeypatch, family_state=FamilyState(display_name="Ornith 1.5")
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
    _seed(tmp_path, monkeypatch)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("e")
        await pilot.pause()


@pytest.mark.asyncio
async def test_family_screen_edit_persists_display_name_to_registry(tmp_path, monkeypatch):
    """Saving the edit modal must write the new display_name to a
    first-class [[families]] entry in registry.toml and drop the legacy
    state.families entry (promotion)."""
    reg_path, state_path = _seed(
        tmp_path, monkeypatch, family_state=FamilyState(display_name="Ornith 1.5")
    )

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("e")
        await pilot.pause()

        from textual.widgets import Button, Input

        app.screen.query_one("#display-name", Input).value = "Ornith v1.5 (renamed)"
        app.screen.query_one("#save", Button).press()
        await pilot.pause()

    from modelman.registry import load_registry

    reloaded_reg = load_registry(reg_path)
    assert reloaded_reg.family("ornith-1.5").display_name == "Ornith v1.5 (renamed)"
    assert "ornith-1.5" not in load_state(state_path).families
