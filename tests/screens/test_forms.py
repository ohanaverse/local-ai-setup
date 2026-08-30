"""Tests for the ModelForm modal (add / edit model).

The dialog asks for exactly one thing: the model name (provider is
static). Validation, parsing, id derivation, and edit-mode pre-fill
are covered here; the underlying parse_model() helper has its own
unit tests in test_parse_model.py.
"""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest
from textual.widgets import Input, Label, Select

from modelman.app import ModelmanApp
from modelman.providers.base import VariantSpec
from modelman.registry import (
    AuthConfig,
    FamilyEntry,
    ProviderEntry,
    Registry,
    save_registry,
)
from modelman.screens.forms import ModelForm, ModelFormResult
from modelman.screens.models import ModelScreen
from modelman.state import StateStore, save_state

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _rendered_provider(app: ModelmanApp) -> str:
    sel = app.screen.query_one("#provider-select", Select)
    return str(sel.value)


def _rendered_error(app: ModelmanApp) -> str:
    label = app.screen.query_one("#model-error", Label)
    return str(label.visual)


def _fill_model(app: ModelmanApp, text: str) -> None:
    inp = app.screen.query_one("#model", Input)
    inp.value = text


async def _mount_and_run(form: ModelForm):
    """Mount the form in a fresh app and return (app, pilot). Caller
    is responsible for pressing keys inside the `async with` block."""
    app = ModelmanApp()
    pilot_cm = app.run_test()
    pilot = await pilot_cm.__aenter__()
    await pilot.pause()
    app.push_screen(form)
    await pilot.pause()
    return app, pilot, pilot_cm


# ---------------------------------------------------------------------------
# Static provider label
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_modelform_add_mode_uses_default_provider():
    form = ModelForm(
        providers=["llamacpp", "ollama", "omlx"],
        variant=None,
        default_provider="omlx",
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        assert _rendered_provider(app) == "omlx"
        assert form._initial_provider == "omlx"


@pytest.mark.asyncio
async def test_modelform_add_mode_ignores_unknown_default_provider():
    form = ModelForm(
        providers=["llamacpp", "ollama", "omlx"],
        variant=None,
        default_provider="nonexistent",
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        assert _rendered_provider(app) == "llamacpp"
        assert form._initial_provider == "llamacpp"


@pytest.mark.asyncio
async def test_modelform_add_mode_without_default_falls_back_to_first():
    form = ModelForm(
        providers=["llamacpp", "ollama", "omlx"],
        variant=None,
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        assert _rendered_provider(app) == "llamacpp"
        assert form._initial_provider == "llamacpp"


@pytest.mark.asyncio
async def test_modelform_edit_mode_uses_variant_provider():
    variant: VariantSpec = {
        "id": "q4",
        "provider": "llamacpp",
        "name": "q4.gguf",
        "repo": "foo/bar",
        "files": ["q4.gguf"],
    }
    form = ModelForm(
        providers=["llamacpp", "ollama", "omlx"],
        variant=variant,
        default_provider="omlx",
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        assert _rendered_provider(app) == "llamacpp"
        assert form._initial_provider == "llamacpp"


# ---------------------------------------------------------------------------
# Placeholder is provider-aware
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_modelform_placeholder_for_ollama():
    form = ModelForm(
        providers=["ollama"],
        variant=None,
        default_provider="ollama",
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        inp = app.screen.query_one("#model", Input)
        assert "ornith-1.5:35b" in (inp.placeholder or "")


@pytest.mark.asyncio
async def test_modelform_placeholder_for_hf_provider():
    form = ModelForm(
        providers=["llamacpp", "ollama", "omlx"],
        variant=None,
        default_provider="llamacpp",
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        inp = app.screen.query_one("#model", Input)
        assert "org/repo" in (inp.placeholder or "")


# ---------------------------------------------------------------------------
# Edit mode pre-fill
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_modelform_edit_prefills_repo_plus_file():
    """Edit mode pre-fills the model input as 'repo/file' for HF
    providers so the user sees exactly the string they'd have typed
    to produce this variant."""
    variant: VariantSpec = {
        "id": "q4",
        "provider": "llamacpp",
        "name": "Ornith-1.5-35B-Q4_K_M.gguf",
        "repo": "ornith-ai/Ornith-1.5-35B-A3B-GGUF",
        "files": ["Ornith-1.5-35B-Q4_K_M.gguf"],
    }
    form = ModelForm(providers=["llamacpp"], variant=variant)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        inp = app.screen.query_one("#model", Input)
        assert inp.value == "ornith-ai/Ornith-1.5-35B-A3B-GGUF/Ornith-1.5-35B-Q4_K_M.gguf"


@pytest.mark.asyncio
async def test_modelform_edit_prefills_repo_only_when_no_file():
    variant: VariantSpec = {
        "id": "m8",
        "provider": "omlx",
        "name": "x-mlx",
        "repo": "foo/bar",
        "files": None,
    }
    form = ModelForm(providers=["omlx"], variant=variant)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        inp = app.screen.query_one("#model", Input)
        assert inp.value == "foo/bar"


@pytest.mark.asyncio
async def test_modelform_edit_prefills_ollama_name():
    variant: VariantSpec = {
        "id": "ollama-35b",
        "provider": "ollama",
        "name": "ornith-1.5:35b",
    }
    form = ModelForm(providers=["ollama"], variant=variant)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        inp = app.screen.query_one("#model", Input)
        assert inp.value == "ornith-1.5:35b"


# ---------------------------------------------------------------------------
# Submit behavior: spec shape per provider
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_submit_ollama_tag_produces_correct_spec():
    form = ModelForm(
        providers=["ollama"],
        variant=None,
        default_provider="ollama",
    )
    dismissed: list = []

    def _capture(result):
        dismissed.append(result)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, _capture)
        await pilot.pause()
        _fill_model(app, "ornith-1.5:35b")
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()

    assert dismissed, "form did not dismiss"
    result = dismissed[0]
    spec = result.spec
    assert isinstance(spec, dict)
    assert spec["provider"] == "ollama"
    assert spec["name"] == "ornith-1.5:35b"
    assert spec["repo"] is None
    assert spec["files"] is None
    assert spec["quantizations"] is None
    # id = provider/escaped-name
    assert spec["id"] == "ollama/ornith-1.5:35b"


@pytest.mark.asyncio
async def test_submit_hf_repo_only_produces_correct_spec():
    form = ModelForm(
        providers=["llamacpp"],
        variant=None,
        default_provider="llamacpp",
    )
    dismissed: list = []

    def _capture(result):
        dismissed.append(result)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, _capture)
        await pilot.pause()
        _fill_model(app, "unsloth/Ornith-1.5-35B-GGUF")
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()

    result = dismissed[0]
    spec = result.spec
    assert spec["provider"] == "llamacpp"
    assert spec["repo"] == "unsloth/Ornith-1.5-35B-GGUF"
    assert spec["files"] is None
    assert spec["id"] == "llamacpp/unsloth--Ornith-1.5-35B-GGUF"


@pytest.mark.asyncio
async def test_submit_hf_repo_and_file_produces_correct_spec():
    form = ModelForm(
        providers=["llamacpp"],
        variant=None,
        default_provider="llamacpp",
    )
    dismissed: list = []

    def _capture(result):
        dismissed.append(result)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, _capture)
        await pilot.pause()
        _fill_model(
            app,
            "unsloth/Ornith-1.5-35B-GGUF/Ornith-1.5-35B-Q8_0.gguf",
        )
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()

    result = dismissed[0]
    spec = result.spec
    assert spec["provider"] == "llamacpp"
    assert spec["repo"] == "unsloth/Ornith-1.5-35B-GGUF"
    assert spec["files"] == ["Ornith-1.5-35B-Q8_0.gguf"]
    # id escapes the slashes in the model so the key is unambiguous.
    assert spec["id"] == ("llamacpp/unsloth--Ornith-1.5-35B-GGUF--Ornith-1.5-35B-Q8_0.gguf")


# ---------------------------------------------------------------------------
# Validation: bad input shows an error and does NOT dismiss
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_submit_ollama_rejects_slash_with_inline_error():
    form = ModelForm(
        providers=["ollama"],
        variant=None,
        default_provider="ollama",
    )
    dismissed: list = []

    def _capture(result):
        dismissed.append(result)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, _capture)
        await pilot.pause()
        _fill_model(app, "someuser/some-model:tag")
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()

        # Inside the async with: the form is still mounted.
        assert dismissed == [], "form should not dismiss on invalid input"
        assert _rendered_error(app)  # non-empty error message


@pytest.mark.asyncio
async def test_submit_hf_rejects_single_segment_with_inline_error():
    form = ModelForm(
        providers=["llamacpp"],
        variant=None,
        default_provider="llamacpp",
    )
    dismissed: list = []

    def _capture(result):
        dismissed.append(result)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, _capture)
        await pilot.pause()
        _fill_model(app, "single-segment")
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()

        assert dismissed == []
        assert _rendered_error(app)


@pytest.mark.asyncio
async def test_submit_empty_model_does_not_dismiss():
    form = ModelForm(
        providers=["ollama"],
        variant=None,
        default_provider="ollama",
    )
    dismissed: list = []

    def _capture(result):
        dismissed.append(result)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, _capture)
        await pilot.pause()
        # Don't fill anything; just press enter.
        await pilot.press("enter")
        await pilot.pause()

        assert dismissed == []
        assert _rendered_error(app)


@pytest.mark.asyncio
async def test_submit_after_fix_clears_error_and_dismisses():
    """After a failed submit, fixing the input must dismiss the form."""
    form = ModelForm(
        providers=["ollama"],
        variant=None,
        default_provider="ollama",
    )
    dismissed: list = []

    def _capture(result):
        dismissed.append(result)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, _capture)
        await pilot.pause()
        # First attempt: invalid (slash).
        _fill_model(app, "bad/name")
        await pilot.press("enter")
        await pilot.pause()
        assert dismissed == []
        assert _rendered_error(app)
        # Fix it.
        _fill_model(app, "goodname:tag")
        await pilot.press("enter")
        await pilot.pause()

    assert len(dismissed) == 1
    assert dismissed[0].spec["name"] == "goodname:tag"


# ---------------------------------------------------------------------------
# Edit mode: id is preserved (immutable), spec fields can change
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_submit_in_edit_mode_preserves_id():
    """Editing an existing variant must keep the original id; only
    the parsed fields (repo, files, name) are recomputed."""
    variant: VariantSpec = {
        "id": "old-hand-rolled-id",
        "provider": "llamacpp",
        "name": "old.gguf",
        "repo": "foo/bar",
        "files": ["old.gguf"],
    }
    form = ModelForm(providers=["llamacpp"], variant=variant)
    dismissed: list = []

    def _capture(result):
        dismissed.append(result)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, _capture)
        await pilot.pause()
        # Replace with a new repo+file.
        _fill_model(app, "baz/quux/new.gguf")
        await pilot.press("enter")
        await pilot.pause()

    spec = dismissed[0].spec
    assert spec["id"] == "old-hand-rolled-id"  # preserved
    assert spec["provider"] == "llamacpp"  # preserved
    assert spec["repo"] == "baz/quux"
    assert spec["files"] == ["new.gguf"]


@pytest.mark.asyncio
async def test_submit_in_edit_mode_preserves_quantizations():
    """Editing an HF variant must carry over its existing quantizations;
    the single model input cannot express them, so losing them would be
    a silent data regression."""
    variant: VariantSpec = {
        "id": "q4",
        "provider": "llamacpp",
        "name": "Ornith-1.5-35B-Q4_K_M.gguf",
        "repo": "ornith-ai/Ornith-1.5-35B-A3B-GGUF",
        "files": ["Ornith-1.5-35B-Q4_K_M.gguf"],
        "quantizations": ["Q4_K_M"],
    }
    form = ModelForm(providers=["llamacpp"], variant=variant)
    dismissed: list = []

    def _capture(result):
        dismissed.append(result)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, _capture)
        await pilot.pause()
        # Keep the same repo+file; only the id/name must be preserved.
        _fill_model(
            app,
            "ornith-ai/Ornith-1.5-35B-A3B-GGUF/Ornith-1.5-35B-Q4_K_M.gguf",
        )
        await pilot.press("enter")
        await pilot.pause()

    spec = dismissed[0].spec
    assert spec["quantizations"] == ["Q4_K_M"]


# ---------------------------------------------------------------------------
# Integration: pressing 'a' on the model screen passes the selected
# provider to ModelForm (this part of the UX is unchanged by the
# dialog simplification; ensure it still works).
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_add_model_dialog_inherits_last_used_provider(tmp_path, monkeypatch):
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[
            ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none")),
            ProviderEntry(id="llamacpp", name="L", auth=AuthConfig(type="none")),
        ],
        families=[FamilyEntry(name="ornith")],
        models=[],
    )
    save_registry(reg, reg_path)
    save_state(StateStore(), state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    from modelman.providers import registry

    stub = MagicMock()
    stub.size_of.return_value = None
    stub.is_downloaded.return_value = False
    stub.name = "ollama"
    monkeypatch.setattr(registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()

        from modelman.screens.models import ModelScreen

        assert isinstance(app.screen, ModelScreen)

        # Add one llamacpp model first (sets _last_provider_used).
        app.screen._last_provider_used = "llamacpp"

        captured: list[ModelForm] = []
        original_push = app.push_screen

        def tracking_push(screen, *args, **kwargs):
            if isinstance(screen, ModelForm):
                captured.append(screen)
            return original_push(screen, *args, **kwargs)

        monkeypatch.setattr(app, "push_screen", tracking_push)
        await pilot.press("a")
        await pilot.pause()

        assert captured, "ModelForm was not pushed"
        assert captured[0]._default_provider == "llamacpp"


# ---------------------------------------------------------------------------
# Family selector
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_modelform_shows_family_select_with_all_families():
    """The Family Select lists every family passed by the caller and
    pre-selects the current family."""
    form = ModelForm(
        providers=["ollama"],
        families=["deepseek-v4", "gemma4", "gemma4:26b-mlx"],
        family="gemma4:26b-mlx",
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        sel = app.screen.query_one("#family-select", Select)
        assert str(sel.value) == "gemma4:26b-mlx"
        # Select._options stores (label, value) tuples (SelectOption is
        # that tuple type in Textual 8.2.8); private but stable and far
        # simpler than driving ArrowDown.
        assert [str(value) for _, value in sel._options] == [
            "deepseek-v4",
            "gemma4",
            "gemma4:26b-mlx",
        ]


@pytest.mark.asyncio
async def test_modelform_prepends_current_family_when_missing():
    """Defensive: if the current family isn't in the list (e.g. the
    caller's families list was stale), it must be prepended and chosen."""
    form = ModelForm(
        providers=["ollama"],
        families=["gemma4"],
        family="gemma4:26b-mlx",
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        sel = app.screen.query_one("#family-select", Select)
        assert [str(value) for _, value in sel._options] == ["gemma4:26b-mlx", "gemma4"]
        assert str(sel.value) == "gemma4:26b-mlx"


@pytest.mark.asyncio
async def test_modelform_family_select_defaults_when_no_families_passed():
    """Legacy direct callers (tests) pass nothing: the selector shows
    exactly one entry. The TUI always passes real values."""
    form = ModelForm(providers=["ollama"])
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        sel = app.screen.query_one("#family-select", Select)
        assert [str(value) for _, value in sel._options] == ["unknown"]


@pytest.mark.asyncio
async def test_submit_returns_modelformresult_with_selected_family():
    """Save dismisses ModelFormResult: the spec plus the family value
    from the Select (unchanged here because #model keeps focus)."""
    form = ModelForm(
        providers=["ollama"],
        families=["gemma4", "gemma4:26b-mlx"],
        family="gemma4:26b-mlx",
    )
    dismissed: list = []

    def _capture(result):
        dismissed.append(result)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, _capture)
        await pilot.pause()
        _fill_model(app, "gemma4:26b")
        await pilot.press("enter")
        await pilot.pause()

    result = dismissed[0]
    assert isinstance(result, ModelFormResult)
    assert result.family == "gemma4:26b-mlx"
    assert result.spec["provider"] == "ollama"
    assert result.spec["name"] == "gemma4:26b"


@pytest.mark.asyncio
async def test_submit_returns_family_switched_in_the_select():
    """Choosing a different family in the Select is what the result
    carries — that's the whole point of the family-move feature."""
    form = ModelForm(
        providers=["ollama"],
        families=["gemma4", "gemma4:26b-mlx"],
        family="gemma4:26b-mlx",
    )
    dismissed: list = []

    def _capture(result):
        dismissed.append(result)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, _capture)
        await pilot.pause()
        sel = app.screen.query_one("#family-select", Select)
        sel.value = "gemma4"
        _fill_model(app, "gemma4:26b")
        await pilot.press("enter")
        await pilot.pause()

    result = dismissed[0]
    assert isinstance(result, ModelFormResult)
    assert result.family == "gemma4"


@pytest.mark.asyncio
async def test_modelform_add_mode_provider_is_a_select():
    """Add mode offers a real provider dropdown, not a locked label."""
    form = ModelForm(
        providers=["ollama", "llamacpp", "claude"],
        provider_kinds={"claude": "native"},
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        sel = app.screen.query_one("#provider-select", Select)
        assert not sel.disabled
        assert [str(v) for _, v in sel._options] == ["ollama", "llamacpp", "claude"]


@pytest.mark.asyncio
async def test_modelform_edit_mode_provider_select_is_disabled():
    variant: VariantSpec = {
        "id": "q4",
        "provider": "llamacpp",
        "name": "q4.gguf",
        "repo": "foo/bar",
        "files": ["q4.gguf"],
    }
    form = ModelForm(providers=["llamacpp", "ollama"], variant=variant)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        sel = app.screen.query_one("#provider-select", Select)
        assert sel.disabled
        assert str(sel.value) == "llamacpp"


@pytest.mark.asyncio
async def test_modelform_location_locked_cloud_for_native_provider():
    form = ModelForm(
        providers=["claude"], default_provider="claude", provider_kinds={"claude": "native"}
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        sel = app.screen.query_one("#location-select", Select)
        assert sel.disabled
        assert str(sel.value) == "cloud"


@pytest.mark.asyncio
async def test_modelform_location_locked_local_for_omlx():
    form = ModelForm(
        providers=["omlx"], default_provider="omlx", provider_kinds={"omlx": "local-only"}
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        sel = app.screen.query_one("#location-select", Select)
        assert sel.disabled
        assert str(sel.value) == "local"


@pytest.mark.asyncio
async def test_modelform_location_editable_for_ollama():
    form = ModelForm(providers=["ollama"], default_provider="ollama")
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        sel = app.screen.query_one("#location-select", Select)
        assert not sel.disabled


@pytest.mark.asyncio
async def test_submit_native_blank_model_defaults_to_native_sentinel():
    form = ModelForm(
        providers=["claude"], default_provider="claude", provider_kinds={"claude": "native"}
    )
    dismissed: list = []
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, dismissed.append)
        await pilot.pause()
        await pilot.press("enter")  # blank #model input
        await pilot.pause()

    spec = dismissed[0].spec
    assert spec["name"] == "native"
    assert spec["id"] == "claude/native"


@pytest.mark.asyncio
async def test_submit_native_named_model():
    form = ModelForm(
        providers=["claude"], default_provider="claude", provider_kinds={"claude": "native"}
    )
    dismissed: list = []
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, dismissed.append)
        await pilot.pause()
        _fill_model(app, "opus")
        await pilot.press("enter")
        await pilot.pause()

    spec = dismissed[0].spec
    assert spec["name"] == "opus"
    assert spec["id"] == "claude/opus"


@pytest.mark.asyncio
async def test_modelform_provider_change_updates_placeholder_and_location():
    """Switching the provider Select in add mode must re-lock the location
    select and update the model input placeholder to match the new provider
    kind."""
    form = ModelForm(
        providers=["ollama", "claude", "llamacpp"],
        provider_kinds={"claude": "native"},
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        provider_sel = app.screen.query_one("#provider-select", Select)
        location_sel = app.screen.query_one("#location-select", Select)
        model_input = app.screen.query_one("#model", Input)

        # Default is the first provider (ollama): editable location, ollama placeholder.
        assert not location_sel.disabled
        assert "ornith-1.5:35b" in (model_input.placeholder or "")

        # Switch to native provider: location locked to cloud, placeholder changes.
        provider_sel.value = "claude"
        await pilot.pause()
        assert location_sel.disabled
        assert str(location_sel.value) == "cloud"
        assert "native" in (model_input.placeholder or "")

        # Switch to HF provider: location locked to local, placeholder changes.
        provider_sel.value = "llamacpp"
        await pilot.pause()
        assert location_sel.disabled
        assert str(location_sel.value) == "local"
        assert "org/repo" in (model_input.placeholder or "")


@pytest.mark.asyncio
async def test_add_model_dialog_locks_location_for_native_provider(tmp_path, monkeypatch):
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[
            ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none")),
            ProviderEntry(
                id="claude", name="Claude", location="cloud", auth=AuthConfig(type="native")
            ),
        ],
        families=[FamilyEntry(name="ornith")],
        models=[],
    )
    save_registry(reg, reg_path)
    save_state(StateStore(), state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    from modelman.app import ModelmanApp
    from modelman.screens.forms import ModelForm

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()
        assert isinstance(app.screen, ModelScreen)
        app.screen._last_provider_used = "claude"

        captured: list[ModelForm] = []
        original_push = app.push_screen

        def tracking_push(screen, *args, **kwargs):
            if isinstance(screen, ModelForm):
                captured.append(screen)
            return original_push(screen, *args, **kwargs)

        monkeypatch.setattr(app, "push_screen", tracking_push)
        await pilot.press("a")
        await pilot.pause()

    assert captured
    assert captured[0]._provider_kinds["claude"] == "native"
