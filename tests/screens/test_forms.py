"""Tests for the ModelForm modal (add / edit model).

The dialog asks for exactly one thing: the model name (provider is
static). Validation, parsing, id derivation, and edit-mode pre-fill
are covered here; the underlying parse_model() helper has its own
unit tests in test_parse_model.py.
"""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest
from textual.widgets import Input, Label

from modelman.app import ModelmanApp
from modelman.manifest import FamilyManifest, save_manifest
from modelman.providers.base import VariantSpec
from modelman.screens.forms import ModelForm

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _rendered_provider(app: ModelmanApp) -> str:
    label = app.screen.query_one("#provider-label", Label)
    return str(label.visual)


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
        assert _rendered_provider(app) == "Provider: omlx"
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
        assert _rendered_provider(app) == "Provider: llamacpp"
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
        assert _rendered_provider(app) == "Provider: llamacpp"
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
        assert _rendered_provider(app) == "Provider: llamacpp"
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
    spec = dismissed[0]
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

    spec = dismissed[0]
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

    spec = dismissed[0]
    assert spec["provider"] == "llamacpp"
    assert spec["repo"] == "unsloth/Ornith-1.5-35B-GGUF"
    assert spec["files"] == ["Ornith-1.5-35B-Q8_0.gguf"]
    # id escapes the slashes in the model so the key is unambiguous.
    assert spec["id"] == (
        "llamacpp/unsloth--Ornith-1.5-35B-GGUF--Ornith-1.5-35B-Q8_0.gguf"
    )


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
    assert dismissed[0]["name"] == "goodname:tag"


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

    spec = dismissed[0]
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

    spec = dismissed[0]
    assert spec["quantizations"] == ["Q4_K_M"]


# ---------------------------------------------------------------------------
# Integration: pressing 'a' on the model screen passes the selected
# provider to ModelForm (this part of the UX is unchanged by the
# dialog simplification; ensure it still works).
# ---------------------------------------------------------------------------


@pytest.mark.skip(reason="FamilyScreen migrates to Registry in PR 3")
@pytest.mark.asyncio
async def test_add_model_dialog_inherits_selected_provider(tmp_path, monkeypatch):
    fam_dir = tmp_path / "families"
    fam_dir.mkdir()
    m = FamilyManifest(
        family="ornith",
        variants=[
            {"id": "o35", "provider": "ollama", "name": "ornith:35b"},
            {"id": "q8", "provider": "llamacpp", "name": "x.gguf",
             "repo": "foo/bar", "files": ["x.gguf"]},
            {"id": "m8", "provider": "omlx", "name": "x-mlx",
             "repo": "foo/bar"},
        ],
    )
    save_manifest(m, fam_dir / "ornith.yaml")
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(fam_dir))
    monkeypatch.setenv("MODELMAN_CONFIG", str(tmp_path / "config.yaml"))
    (tmp_path / "config.yaml").write_text(
        "providers:\n  ollama: {type: ollama}\n"
        "  llamacpp: {type: llamacpp}\n"
        "  omlx: {type: omlx}\n"
    )

    from modelman.providers import registry

    stub = MagicMock()
    stub.size_of.return_value = None
    stub.name = "ollama"
    monkeypatch.setattr(
        registry.ProviderRegistry,
        "get",
        staticmethod(lambda name, cfg: stub),
    )

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("enter")
        await pilot.pause()

        from modelman.screens.models import ModelScreen
        assert isinstance(app.screen, ModelScreen)

        from textual.widgets import DataTable

        provider_table = app.screen.query_one("#provider-table", DataTable)
        llamacpp_idx = None
        for i in range(provider_table.row_count):
            if provider_table.get_row_at(i)[0] == "llamacpp":
                llamacpp_idx = i
                break
        assert llamacpp_idx is not None
        provider_table.focus()
        await pilot.pause()
        for _ in range(llamacpp_idx):
            await pilot.press("down")
        await pilot.pause()
        assert app.screen.selected_provider == "llamacpp"

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
        form = captured[0]
        assert form._default_provider == "llamacpp"
