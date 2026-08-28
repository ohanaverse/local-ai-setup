
"""App integration: load + persist the saved theme."""

from __future__ import annotations

import pytest

# ---------------------------------------------------------------------------
# App integration: theme load + persistence
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_app_loads_saved_theme_on_startup(tmp_path, monkeypatch):
    """If settings.yaml has a theme, the app should set self.theme
    to that value during construction.

    ModelmanApp.__init__ only loads settings (theme) — it does not
    touch registry.toml/modelman.toml at all (that's on_mount, which
    this test never triggers by calling .run()), so no
    registry/family mocking is needed here."""
    from modelman.app import ModelmanApp
    from modelman.settings import Settings, save_settings

    settings_path = tmp_path / "settings.yaml"
    save_settings(Settings(theme="nord"), settings_path)
    monkeypatch.setenv("MODELMAN_SETTINGS", str(settings_path))

    app = ModelmanApp()
    assert app.theme == "nord"


@pytest.mark.asyncio
async def test_app_no_settings_uses_default_theme(tmp_path, monkeypatch):
    """No settings file -> default theme (whatever Textual picks)."""
    from modelman.app import ModelmanApp

    monkeypatch.setenv("MODELMAN_SETTINGS", str(tmp_path / "missing.yaml"))

    app = ModelmanApp()
    # Default theme is 'textual-dark' in Textual; whatever it is, we
    # just want to confirm load_settings() didn't set anything bogus.
    assert app.theme == "textual-dark"


@pytest.mark.asyncio
async def test_app_corrupt_settings_falls_back_to_defaults(tmp_path, monkeypatch):
    """A corrupted settings file should NOT prevent the app from
    starting. Falls back to defaults."""
    from modelman.app import ModelmanApp

    path = tmp_path / "settings.yaml"
    path.write_text("[unclosed")
    monkeypatch.setenv("MODELMAN_SETTINGS", str(path))

    app = ModelmanApp()  # should not raise
    assert app.theme == "textual-dark"


@pytest.mark.asyncio
async def test_app_watch_theme_persists_change(tmp_path, monkeypatch):
    """When self.theme is changed at runtime, the new value should
    be written to settings.yaml."""
    from modelman.app import ModelmanApp
    from modelman.settings import load_settings

    settings_path = tmp_path / "settings.yaml"
    monkeypatch.setenv("MODELMAN_SETTINGS", str(settings_path))

    app = ModelmanApp()
    assert app.theme == "textual-dark"
    assert not settings_path.exists()

    # Simulate the user picking a theme via Ctrl+P (Textual sets
    # self.theme; watch_theme fires).
    app.theme = "gruvbox"
    # Yield to the event loop so watch_theme runs.
    import asyncio

    await asyncio.sleep(0.05)

    assert settings_path.exists(), "watch_theme should have written settings"
    s = load_settings()
    assert s.theme == "gruvbox"


@pytest.mark.asyncio
async def test_app_startup_does_not_rewrite_unrelated_settings(
    tmp_path, monkeypatch
):
    """On startup, watch_theme fires once with the loaded theme. The
    dedup check should keep us from rewriting the file when nothing
    has actually changed."""
    from modelman.app import ModelmanApp
    from modelman.settings import Settings, load_settings, save_settings

    settings_path = tmp_path / "settings.yaml"
    save_settings(Settings(theme="nord"), settings_path)
    # Put some non-theme content there to verify it's preserved.
    settings_path.write_text("theme: nord\ncustom_user_key: keep-me\n")
    monkeypatch.setenv("MODELMAN_SETTINGS", str(settings_path))

    ModelmanApp()
    import asyncio

    await asyncio.sleep(0.05)  # let watch_theme run if it would

    text = settings_path.read_text()
    assert "keep-me" in text, "watch_theme must not clobber unrelated keys"
    s = load_settings()
    assert s.theme == "nord"
