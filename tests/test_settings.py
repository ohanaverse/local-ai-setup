"""Tests for the user-preferences settings module.

Settings persist user preferences (currently just the theme) across
TUI launches. The file lives at ~/.config/local-ai/settings.yaml by
default, overridable with MODELMAN_SETTINGS for tests.
"""

from __future__ import annotations

import pytest

from modelman.settings import Settings, load_settings, save_settings


def test_settings_default_when_file_missing(tmp_path, monkeypatch):
    """load_settings() returns defaults when the file doesn't exist;
    no exception raised. This matches the 'first run' UX."""
    monkeypatch.setenv("MODELMAN_SETTINGS", str(tmp_path / "settings.yaml"))
    s = load_settings()
    assert s.theme is None


def test_settings_roundtrip(tmp_path, monkeypatch):
    path = tmp_path / "settings.yaml"
    monkeypatch.setenv("MODELMAN_SETTINGS", str(path))

    s = Settings(theme="nord")
    save_settings(s, path)
    assert path.exists()
    assert "nord" in path.read_text()

    s2 = load_settings()
    assert s2.theme == "nord"


def test_settings_partial_file_uses_defaults(tmp_path, monkeypatch):
    """A settings file with unknown keys is left alone; known keys
    are loaded. Missing keys default."""
    path = tmp_path / "settings.yaml"
    path.write_text("future_setting: 42\n")
    monkeypatch.setenv("MODELMAN_SETTINGS", str(path))

    s = load_settings()
    assert s.theme is None
    # Unknown keys are preserved on round-trip.
    save_settings(s, path)
    text = path.read_text()
    assert "future_setting" in text
    assert "42" in text


def test_settings_empty_file_returns_defaults(tmp_path, monkeypatch):
    path = tmp_path / "settings.yaml"
    path.write_text("")
    monkeypatch.setenv("MODELMAN_SETTINGS", str(path))

    s = load_settings()
    assert s.theme is None


def test_settings_invalid_yaml_raises(tmp_path, monkeypatch):
    """A corrupted settings file is a real error; user must fix it
    or delete the file. We don't silently fall back to defaults \u2014
    that would mask a problem and lose the user's intended theme."""
    path = tmp_path / "settings.yaml"
    path.write_text("this is: not: valid: yaml: ::\n  - [unclosed")
    monkeypatch.setenv("MODELMAN_SETTINGS", str(path))

    import yaml

    with pytest.raises(yaml.YAMLError):
        load_settings()


def test_settings_theme_can_be_cleared(tmp_path, monkeypatch):
    """Setting theme to None persists as an explicit null (so a
    previously-set theme can be cleared without deleting the file)."""
    path = tmp_path / "settings.yaml"
    monkeypatch.setenv("MODELMAN_SETTINGS", str(path))

    save_settings(Settings(theme="nord"), path)
    save_settings(Settings(theme=None), path)

    s = load_settings()
    assert s.theme is None


def test_settings_default_path(monkeypatch):
    """Without MODELMAN_SETTINGS env, the default path is
    ~/.config/local-ai/settings.yaml."""
    monkeypatch.delenv("MODELMAN_SETTINGS", raising=False)
    from modelman import settings as settings_mod

    p = settings_mod._default_settings_path()
    assert str(p).endswith("settings.yaml")
    assert "local-ai" in str(p)


def test_settings_explicit_path_arg_overrides_env(tmp_path, monkeypatch):
    """save_settings / load_settings accept an explicit path that
    overrides the env-derived default."""
    monkeypatch.setenv("MODELMAN_SETTINGS", str(tmp_path / "ignored.yaml"))
    custom = tmp_path / "custom.yaml"

    save_settings(Settings(theme="gruvbox"), custom)
    assert custom.exists()

    s = load_settings(custom)
    assert s.theme == "gruvbox"
