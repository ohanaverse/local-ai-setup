"""Tests for the ModelForm modal (add / edit model).

The dialog asks for exactly one thing: the model name (provider is
static). Validation, parsing, id derivation, and edit-mode pre-fill
are covered here; the underlying parse_model() helper has its own
unit tests in test_parse_model.py.
"""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest
from textual.widgets import Button, Checkbox, Input, Label, Select

from modelman.app import ModelmanApp
from modelman.providers.base import VariantSpec
from modelman.registry import (
    AuthConfig,
    Cost,
    FamilyEntry,
    ProviderEntry,
    Registry,
    save_registry,
)
from modelman.screens.forms import (
    ModelForm,
    ModelFormResult,
    _price_str,
    parse_cost_fields,
    parse_subscription_fields,
)
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
async def test_submit_ollama_tag_produces_correct_spec(stub_ollama_caps):
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
async def test_submit_after_fix_clears_error_and_dismisses(stub_ollama_caps):
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
    """Defensive: if the current family isn't in the list (e.g. a
    queued-move target family that doesn't exist yet), it must be
    prepended and chosen."""
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
async def test_modelform_family_select_is_sorted_and_does_not_prepend():
    """When the current family IS in the sorted list, the Select keeps
    the caller's order — it doesn't shove the pre-selected entry to
    the front just to make it the default."""
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
        assert [str(value) for _, value in sel._options] == [
            "deepseek-v4",
            "gemma4",
            "gemma4:26b-mlx",
        ]
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
async def test_submit_returns_modelformresult_with_selected_family(stub_ollama_caps):
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
async def test_submit_returns_family_switched_in_the_select(stub_ollama_caps):
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


# ---------------------------------------------------------------------------
# Dialog conventions: button order, initial focus, Escape-to-cancel
# ---------------------------------------------------------------------------


def _button_ids(app: ModelmanApp) -> list[str]:
    return [btn.id for btn in app.screen.query(Button) if btn.id is not None]


def _focused_id(app: ModelmanApp) -> str | None:
    w = app.screen.focused
    return w.id if w is not None else None


@pytest.mark.asyncio
async def test_add_family_modal_buttons_and_focus():
    from modelman.screens.forms import AddFamilyModal

    modal = AddFamilyModal()
    async with ModelmanApp().run_test() as pilot:
        await pilot.pause()
        pilot.app.push_screen(modal)
        await pilot.pause()
        assert _button_ids(pilot.app) == ["cancel", "create"]
        assert _focused_id(pilot.app) == "family-name"


@pytest.mark.asyncio
async def test_edit_family_modal_buttons_and_focus():
    from modelman.screens.forms import EditFamilyModal

    modal = EditFamilyModal(family="ornith", display_name="Ornith")
    async with ModelmanApp().run_test() as pilot:
        await pilot.pause()
        pilot.app.push_screen(modal)
        await pilot.pause()
        assert _button_ids(pilot.app) == ["cancel", "save"]
        assert _focused_id(pilot.app) == "display-name"


@pytest.mark.asyncio
async def test_modelform_buttons_and_focus():
    form = ModelForm(providers=["ollama"])
    async with ModelmanApp().run_test() as pilot:
        await pilot.pause()
        pilot.app.push_screen(form)
        await pilot.pause()
        assert _button_ids(pilot.app) == ["cancel", "save"]
        assert _focused_id(pilot.app) == "model"


@pytest.mark.asyncio
async def test_confirm_modal_buttons_and_safe_focus():
    from modelman.screens.forms import ConfirmModal

    modal = ConfirmModal("Delete everything?")
    dismissed: list = []
    async with ModelmanApp().run_test() as pilot:
        await pilot.pause()
        pilot.app.push_screen(modal, dismissed.append)
        await pilot.pause()
        # Order: Yes (warning) then No (default) — the dangerous option is
        # on the left, the safe option is on the right where Enter /
        # mouse defaults land. Initial focus is the safe (No) button.
        assert _button_ids(pilot.app) == ["yes", "no"]
        assert _focused_id(pilot.app) == "no"


@pytest.mark.asyncio
async def test_confirm_exit_dialog_buttons_and_safe_focus():
    from modelman.screens.forms import ConfirmExitDialog

    modal = ConfirmExitDialog(ready=[], deletes=[], exposes=[], moves=[])
    async with ModelmanApp().run_test() as pilot:
        await pilot.pause()
        pilot.app.push_screen(modal)
        await pilot.pause()
        # Order left-to-right: Cancel, Discard (warning), Apply (primary).
        # Initial focus is Cancel (safe default; Enter does nothing
        # destructive).
        assert _button_ids(pilot.app) == ["cancel", "discard", "apply"]
        assert _focused_id(pilot.app) == "cancel"


@pytest.mark.asyncio
async def test_cancel_apply_dialog_buttons_and_safe_focus():
    from modelman.screens.forms import CancelApplyDialog

    modal = CancelApplyDialog()
    async with ModelmanApp().run_test() as pilot:
        await pilot.pause()
        pilot.app.push_screen(modal)
        await pilot.pause()
        # Order left-to-right: Cancel (warning), Wait (primary).
        # Initial focus is Wait (safe default: keep the apply running).
        assert _button_ids(pilot.app) == ["cancel", "wait"]
        assert _focused_id(pilot.app) == "wait"


@pytest.mark.asyncio
async def test_modelform_escape_from_input_dismisses():
    """Escape must cancel the modal even when the model Input is focused."""
    form = ModelForm(providers=["ollama"])
    dismissed: list = []
    async with ModelmanApp().run_test() as pilot:
        await pilot.pause()
        pilot.app.push_screen(form, dismissed.append)
        await pilot.pause()
        assert _focused_id(pilot.app) == "model"
        await pilot.press("escape")
        await pilot.pause()
    assert dismissed == [None]


# ---------------------------------------------------------------------------
# _price_str: edit-dialog price prefill (shortest round-trip, no float noise)
# ---------------------------------------------------------------------------


def test_price_str_whole_number_drops_decimal():
    """Whole-number prices must prefill without a noisy '.0' so an untouched
    edit-save does not flip '20' to '20.0' in the stored registry."""
    assert _price_str(20.0) == "20"


def test_price_str_binary_inexact_uses_shortest_round_trip():
    """Decimal prices that are not exact in binary must still round-trip
    through a short repr without truncation."""
    assert _price_str(9.99) == "9.99"
    assert _price_str(0.1) == "0.1"
    assert _price_str(0.35) == "0.35"


def test_price_str_round_trips_exactly():
    """Whatever string _price_str produces must parse back to the same float."""
    for value in (20.0, 2.5, 9.99, 0.1, 0.35, 123.456):
        assert float(_price_str(value)) == value


def test_price_str_non_finite_survives():
    """Hand-edited TOML can hold inf/nan; repr keeps them parseable."""
    import math

    assert _price_str(float("inf")) == "inf"
    assert math.isnan(float(_price_str(float("nan"))))


# ---------------------------------------------------------------------------
# ---------------------------------------------------------------------------
# parse_cost_fields / parse_subscription_fields helpers
# ---------------------------------------------------------------------------


def test_parse_cost_fields_per_token_one_required():
    """A single per-token price is enough to build a Cost."""
    assert parse_cost_fields(input_price="0.50", cache_price="", output_price="") == Cost(
        input_price_per_million=0.50
    )


def test_parse_cost_fields_per_token_all_present():
    assert parse_cost_fields(input_price="0.50", cache_price="0.25", output_price="1.00") == Cost(
        input_price_per_million=0.50,
        cache_price_per_million=0.25,
        output_price_per_million=1.00,
    )


def test_parse_cost_fields_per_token_empty_raises():
    with pytest.raises(ValueError, match="at least one per-token price"):
        parse_cost_fields(input_price="", cache_price="", output_price="")


def test_parse_cost_fields_bad_number_raises():
    """Non-numeric per-token input must surface a clear ValueError."""
    with pytest.raises(ValueError, match="input_price"):
        parse_cost_fields(input_price="abc", cache_price="", output_price="")


def test_parse_cost_fields_negative_raises():
    """Negative prices are not valid costs and must be rejected."""
    with pytest.raises(ValueError, match="cache_price"):
        parse_cost_fields(input_price="", cache_price="-1", output_price="")


def test_parse_cost_fields_non_finite_raises():
    """inf/nan are not valid prices; reject them at parse time."""
    with pytest.raises(ValueError, match="output_price"):
        parse_cost_fields(input_price="", cache_price="", output_price="inf")


def test_parse_subscription_fields_required():
    assert parse_subscription_fields(price="19.99", period="month") == Cost(
        subscription_price=19.99, subscription_period="month"
    )


def test_parse_subscription_fields_missing_price_raises():
    with pytest.raises(ValueError, match="subscription price is required"):
        parse_subscription_fields(price="", period="month")


def test_parse_subscription_fields_missing_period_raises():
    with pytest.raises(ValueError, match="subscription period is required"):
        parse_subscription_fields(price="19.99", period="")


def test_parse_subscription_fields_bad_period_raises():
    with pytest.raises(ValueError, match="subscription period must be month or year"):
        parse_subscription_fields(price="19.99", period="week")


def test_parse_subscription_fields_negative_price_raises():
    with pytest.raises(ValueError, match="subscription_price"):
        parse_subscription_fields(price="-5", period="month")


@pytest.mark.asyncio
async def test_edit_family_modal_escape_from_disabled_input_dismisses():
    """Escape must cancel even when the read-only family-name Input is focused."""
    from modelman.screens.forms import EditFamilyModal

    modal = EditFamilyModal(family="ornith", display_name="Ornith")
    dismissed: list = []
    async with ModelmanApp().run_test() as pilot:
        await pilot.pause()
        pilot.app.push_screen(modal, dismissed.append)
        await pilot.pause()
        modal.query_one("#family-name").focus()
        await pilot.pause()
        await pilot.press("escape")
        await pilot.pause()
    assert dismissed == [None]


# ---------------------------------------------------------------------------
# Cost section (per-token + subscription checkboxes). Visibility is
# .display-based; the widgets are always mounted, so tests can query ids
# unconditionally.
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_model_form_cost_section_default_hidden():
    """Add mode has both pricing checkboxes unchecked and their inputs hidden."""
    form = ModelForm(
        providers=["ollama"],
        default_provider="ollama",
        families=["ornith"],
        family="ornith",
        provider_kinds={"ollama": "ollama"},
    )
    app, pilot, _pilot_cm = await _mount_and_run(form)
    try:
        assert app.screen.query_one("#per-token-checkbox", Checkbox).value is False
        assert app.screen.query_one("#subscription-checkbox", Checkbox).value is False
        assert app.screen.query_one("#input-price", Input).display is False
        assert app.screen.query_one("#input-price-label", Label).display is False
        assert app.screen.query_one("#cache-price", Input).display is False
        assert app.screen.query_one("#cache-price-label", Label).display is False
        assert app.screen.query_one("#output-price", Input).display is False
        assert app.screen.query_one("#output-price-label", Label).display is False
        assert app.screen.query_one("#subscription-price", Input).display is False
        assert app.screen.query_one("#subscription-price-label", Label).display is False
        assert app.screen.query_one("#subscription-period-select", Select).display is False
        assert app.screen.query_one("#subscription-period-label", Label).display is False
    finally:
        await _pilot_cm.__aexit__(None, None, None)


@pytest.mark.asyncio
async def test_model_form_cost_section_per_token_shows_inputs():
    """Checking the per-token checkbox reveals the three price Inputs."""
    form = ModelForm(
        providers=["ollama"],
        default_provider="ollama",
        families=["ornith"],
        family="ornith",
        provider_kinds={"ollama": "ollama"},
    )
    app, pilot, _pilot_cm = await _mount_and_run(form)
    try:
        app.screen.query_one("#per-token-checkbox", Checkbox).value = True
        await pilot.pause()
        assert app.screen.query_one("#input-price", Input).display is True
        assert app.screen.query_one("#input-price-label", Label).display is True
        assert app.screen.query_one("#cache-price", Input).display is True
        assert app.screen.query_one("#cache-price-label", Label).display is True
        assert app.screen.query_one("#output-price", Input).display is True
        assert app.screen.query_one("#output-price-label", Label).display is True
        assert app.screen.query_one("#subscription-price", Input).display is False
        assert app.screen.query_one("#subscription-price-label", Label).display is False
        assert app.screen.query_one("#subscription-period-select", Select).display is False
        assert app.screen.query_one("#subscription-period-label", Label).display is False
    finally:
        await _pilot_cm.__aexit__(None, None, None)


@pytest.mark.asyncio
async def test_model_form_cost_section_subscription_shows_inputs():
    """Checking the subscription checkbox reveals price and period widgets."""
    form = ModelForm(
        providers=["ollama"],
        default_provider="ollama",
        families=["ornith"],
        family="ornith",
        provider_kinds={"ollama": "ollama"},
    )
    app, pilot, _pilot_cm = await _mount_and_run(form)
    try:
        app.screen.query_one("#subscription-checkbox", Checkbox).value = True
        await pilot.pause()
        assert app.screen.query_one("#subscription-price", Input).display is True
        assert app.screen.query_one("#subscription-price-label", Label).display is True
        assert app.screen.query_one("#subscription-period-select", Select).display is True
        assert app.screen.query_one("#subscription-period-label", Label).display is True
        assert str(app.screen.query_one("#subscription-period-select", Select).value) == "month"
        assert app.screen.query_one("#input-price", Input).display is False
        assert app.screen.query_one("#input-price-label", Label).display is False
    finally:
        await _pilot_cm.__aexit__(None, None, None)


@pytest.mark.asyncio
async def test_model_form_edit_prefills_cost():
    """Edit mode pre-fills the per-token and subscription fields from the variant."""
    spec = {
        "id": "ollama/glm-5.3:cloud",
        "provider": "ollama",
        "name": "glm-5.3:cloud",
        "location": "cloud",
        "cost": Cost(
            input_price_per_million=0.50,
            cache_price_per_million=0.25,
            output_price_per_million=1.00,
            subscription_price=20.0,
            subscription_period="month",
        ),
        "model_info": None,
        "repo": None,
        "files": None,
        "quantizations": None,
    }
    form = ModelForm(
        providers=["ollama"],
        variant=spec,
        families=["glm"],
        family="glm",
        provider_kinds={"ollama": "ollama"},
    )
    app, pilot, _pilot_cm = await _mount_and_run(form)
    try:
        assert app.screen.query_one("#per-token-checkbox", Checkbox).value is True
        assert app.screen.query_one("#input-price", Input).value == "0.5"
        assert app.screen.query_one("#cache-price", Input).value == "0.25"
        assert app.screen.query_one("#output-price", Input).value == "1"
        assert app.screen.query_one("#subscription-checkbox", Checkbox).value is True
        assert app.screen.query_one("#subscription-price", Input).value == "20"
        assert str(app.screen.query_one("#subscription-period-select", Select).value) == "month"
        assert app.screen.query_one("#input-price", Input).display is True
        assert app.screen.query_one("#input-price-label", Label).display is True
        assert app.screen.query_one("#subscription-price", Input).display is True
        assert app.screen.query_one("#subscription-price-label", Label).display is True
    finally:
        await _pilot_cm.__aexit__(None, None, None)


@pytest.mark.asyncio
async def test_model_form_submit_carries_cost(stub_ollama_caps):
    """Save dismisses ModelFormResult whose spec includes the flat cost fields."""
    form = ModelForm(
        providers=["ollama"],
        default_provider="ollama",
        families=["ornith"],
        family="ornith",
        provider_kinds={"ollama": "ollama"},
    )
    dismissed: list = []

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, dismissed.append)
        await pilot.pause()
        app.screen.query_one("#subscription-checkbox", Checkbox).value = True
        await pilot.pause()
        price = app.screen.query_one("#subscription-price", Input)
        price.value = "20"
        _fill_model(app, "glm-5.3:cloud")
        await pilot.press("enter")  # Input.Submitted on #model triggers _submit
        await pilot.pause()

    assert len(dismissed) == 1
    result = dismissed[0]
    assert result.spec["cost"] == {
        "subscription_price": 20.0,
        "subscription_period": "month",
    }
    assert result.family == "ornith"


@pytest.mark.asyncio
async def test_model_form_untouched_edit_preserves_cost_and_model_info():
    """Edit prefill → save WITHOUT touching any widget → cost and other
    edit-only metadata survive the round trip intact."""
    spec = {
        "id": "ollama/glm-5.3:cloud",
        "provider": "ollama",
        "name": "glm-5.3:cloud",
        "location": "cloud",
        "repo": None,
        "files": None,
        "quantizations": None,
        "model_info": {"supports_function_calling": True},
        "cost": Cost(subscription_price=20.0, subscription_period="month"),
    }
    form = ModelForm(
        providers=["ollama"],
        variant=spec,
        families=["glm"],
        family="glm",
        provider_kinds={"ollama": "ollama"},
    )
    dismissed: list = []
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, dismissed.append)
        await pilot.pause()
        model_input = app.screen.query_one("#model", Input)
        assert model_input.value == "glm-5.3:cloud"
        await pilot.press("enter")
        await pilot.pause()

    assert dismissed, "form did not dismiss"
    result = dismissed[0]
    assert result.spec["cost"] == {
        "subscription_price": 20.0,
        "subscription_period": "month",
    }
    assert result.spec["model_info"] == {"supports_function_calling": True}
    assert result.spec["id"] == "ollama/glm-5.3:cloud"
    assert result.spec["location"] == "cloud"
    assert result.spec["quantizations"] is None
    assert result.family == "glm"


@pytest.mark.asyncio
async def test_model_form_llamacpp_edit_preserves_no_cost():
    """A llamacpp variant with no cost submits with cost=None."""
    spec = {
        "id": "llamacpp/unsloth/Qwen3.8-27B-GGUF",
        "provider": "llamacpp",
        "name": "unsloth/Qwen3.8-27B-GGUF",
        "location": "local",
        "repo": "unsloth/Qwen3.8-27B-GGUF",
        "files": ["Qwen3.8-27B-Q4_K_M.gguf"],
        "quantizations": None,
        "model_info": None,
        "cost": None,
    }
    form = ModelForm(
        providers=["llamacpp"],
        variant=spec,
        families=["ornith"],
        family="ornith",
        provider_kinds={"llamacpp": "local-only"},
    )
    dismissed: list = []
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, dismissed.append)
        await pilot.pause()
        # No touches: the pre-filled repo/file string is already valid.
        await pilot.press("enter")
        await pilot.pause()

    assert dismissed, "form did not dismiss"
    result = dismissed[0]
    assert result.spec["cost"] is None


@pytest.mark.asyncio
async def test_model_form_edit_preserves_unset_cost():
    """A model with no configured cost must stay cost=None when the edit
    dialog is saved without touching the cost section."""
    spec = {
        "id": "ollama/glm-5.3:cloud",
        "provider": "ollama",
        "name": "glm-5.3:cloud",
        "location": "cloud",
        "repo": None,
        "files": None,
        "quantizations": None,
        "model_info": None,
        "cost": None,
    }
    form = ModelForm(
        providers=["ollama"],
        variant=spec,
        families=["glm"],
        family="glm",
        provider_kinds={"ollama": "ollama"},
    )
    dismissed: list = []
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, dismissed.append)
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()

    assert dismissed, "form did not dismiss"
    result = dismissed[0]
    assert result.spec["cost"] is None


@pytest.mark.asyncio
async def test_model_form_empty_per_token_section_shows_error():
    """Enabling per-token pricing without entering any price keeps the form open."""
    form = ModelForm(
        providers=["ollama"],
        default_provider="ollama",
        families=["ornith"],
        family="ornith",
        provider_kinds={"ollama": "ollama"},
    )
    dismissed: list = []
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, dismissed.append)
        await pilot.pause()
        app.screen.query_one("#per-token-checkbox", Checkbox).value = True
        await pilot.pause()
        _fill_model(app, "test:1b")
        await pilot.press("enter")
        await pilot.pause()

        assert dismissed == [], "form must not dismiss on empty per-token section"
        assert _rendered_error(app) == "at least one per-token price is required"


@pytest.mark.asyncio
async def test_model_form_missing_subscription_price_shows_error():
    """Enabling subscription pricing without a price keeps the form open."""
    form = ModelForm(
        providers=["ollama"],
        default_provider="ollama",
        families=["ornith"],
        family="ornith",
        provider_kinds={"ollama": "ollama"},
    )
    dismissed: list = []
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, dismissed.append)
        await pilot.pause()
        app.screen.query_one("#subscription-checkbox", Checkbox).value = True
        await pilot.pause()
        _fill_model(app, "test:1b")
        await pilot.press("enter")
        await pilot.pause()

        assert dismissed == []
        assert _rendered_error(app) == "subscription price is required"


@pytest.mark.asyncio
async def test_model_form_both_sections_combined(stub_ollama_caps):
    """Both per-token and subscription pricing can be enabled together."""
    form = ModelForm(
        providers=["ollama"],
        default_provider="ollama",
        families=["ornith"],
        family="ornith",
        provider_kinds={"ollama": "ollama"},
    )
    dismissed: list = []
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, dismissed.append)
        await pilot.pause()
        app.screen.query_one("#per-token-checkbox", Checkbox).value = True
        app.screen.query_one("#subscription-checkbox", Checkbox).value = True
        await pilot.pause()
        _fill_model(app, "test:1b")
        app.screen.query_one("#input-price", Input).value = "0.50"
        app.screen.query_one("#subscription-price", Input).value = "20"
        await pilot.press("enter")
        await pilot.pause()

    assert len(dismissed) == 1
    result = dismissed[0]
    assert result.spec["cost"] == {
        "input_price_per_million": 0.5,
        "subscription_price": 20.0,
        "subscription_period": "month",
    }


@pytest.mark.asyncio
async def test_model_form_bad_price_shows_error_and_stays_open():
    """Invalid price input surfaces the message inline and cancels the dismiss."""
    form = ModelForm(
        providers=["ollama"],
        default_provider="ollama",
        families=["ornith"],
        family="ornith",
        provider_kinds={"ollama": "ollama"},
    )
    dismissed: list = []
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form, dismissed.append)
        await pilot.pause()
        app.screen.query_one("#input-price", Input)  # ensure mounted
        app.screen.query_one("#per-token-checkbox", Checkbox).value = True
        await pilot.pause()
        _fill_model(app, "test:1b")  # valid tag so the ONLY failure is the price
        app.screen.query_one("#input-price", Input).value = "abc"
        # Focus stays on #model, whose value is valid: Enter submits, and the
        # submit must fail on the cost field rather than the model name.
        await pilot.press("enter")
        await pilot.pause()

        assert dismissed == [], "form must not dismiss on invalid price"
        assert _rendered_error(app) == "input_price must be a number"
