"""User-preferences persistence.

Settings persist user preferences (currently just the theme) across
TUI launches. The file lives at ~/.config/local-ai/settings.yaml by
default, overridable with MODELMAN_SETTINGS for tests.

The file is OPTIONAL: missing file returns defaults. A corrupted
file raises so the user can fix it (we never silently fall back,
since that would mask a problem and lose the user's intended theme).
Unknown keys are preserved on round-trip so future settings can be
added without losing older data.
"""

from __future__ import annotations

import os
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any

import yaml


def _default_settings_path() -> Path:
    """Compute the settings path lazily so env overrides work in tests."""
    return Path(
        os.environ.get("MODELMAN_SETTINGS", "~/.config/local-ai/settings.yaml")
    ).expanduser()


@dataclass
class Settings:
    """User preferences. Add fields here as new settings are added."""

    theme: str | None = None


def load_settings(path: Path | None = None) -> Settings:
    """Read settings from disk. Missing file returns defaults; a
    present-but-empty file also returns defaults. Corrupt YAML raises.
    """
    settings_path = Path(path) if path else _default_settings_path()
    if not settings_path.exists():
        return Settings()
    with open(settings_path) as f:
        raw: dict[str, Any] = yaml.safe_load(f) or {}
    # Build a Settings from known fields; ignore unknown keys.
    return Settings(
        theme=raw.get("theme"),  # None if absent or null
    )


def save_settings(settings: Settings, path: Path | None = None) -> None:
    """Write settings to disk. Creates parent dirs if missing.

    Uses merge semantics: unknown keys already in the file are
    preserved. This way future settings added on disk by hand are
    not silently dropped when we save.
    """
    settings_path = Path(path) if path else _default_settings_path()
    settings_path.parent.mkdir(parents=True, exist_ok=True)

    existing: dict[str, Any] = {}
    if settings_path.exists():
        with open(settings_path) as f:
            existing = yaml.safe_load(f) or {}

    merged = {**existing, **asdict(settings)}
    with open(settings_path, "w") as f:
        yaml.safe_dump(merged, f, sort_keys=False)
