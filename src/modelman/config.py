"""Load ~/.config/local-ai/config.yaml.

As of Phase 2 (see docs/superpowers/specs/2026-08-27-shared-model-
registry-design.md), this legacy config file and MODELMAN_CONFIG are
read only by `modelman migrate` (src/modelman/main.py, migrate.py) —
every TUI code path was migrated onto registry.toml (registry.py) by
Phase 2 PR 2/PR 3. Do not add a new caller of load_config() outside
the migrate path; add to registry.toml instead.
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import yaml


def default_config_path() -> Path:
    """Compute the config path lazily so env overrides work in tests."""
    return Path(os.environ.get("MODELMAN_CONFIG", "~/.config/local-ai/config.yaml")).expanduser()


class ConfigError(Exception):
    """Raised when the config file is missing or malformed."""


@dataclass
class Config:
    """Loaded configuration."""

    providers: dict[str, dict[str, Any]] = field(default_factory=dict)

    def provider(self, name: str) -> dict[str, Any]:
        if name not in self.providers:
            raise KeyError(f"Unknown provider in config: {name}")
        return self.providers[name]


def load_config(path: Path | None = None) -> Config:
    """Load and validate the config file."""
    config_path = Path(path) if path else default_config_path()
    if not config_path.exists():
        raise ConfigError(
            f"Config file not found: {config_path}. "
            "Create one with `modelman init-config` or see README."
        )

    with open(config_path) as f:
        raw = yaml.safe_load(f) or {}

    if "providers" not in raw or not raw["providers"]:
        raise ConfigError("Config must define at least one provider under `providers:`")

    providers = raw["providers"]
    for name, cfg in providers.items():
        if "type" not in cfg:
            raise ConfigError(f"Provider `{name}` missing required `type` field")

    return Config(providers=providers)
