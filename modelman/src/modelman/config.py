"""Legacy ~/.config/local-ai/config.yaml path resolution.

As of Phase 2 (see docs/superpowers/specs/2026-08-27-shared-model-
registry-design.md), this legacy config file and MODELMAN_CONFIG are
read only by `modelman migrate` (src/modelman/main.py, migrate.py) —
every TUI code path was migrated onto registry.toml (registry.py) by
Phase 2 PR 2/PR 3. migrate.py parses the YAML directly; only the path
helper lives here.
"""

from __future__ import annotations

import os
from pathlib import Path


def default_config_path() -> Path:
    """Compute the config path lazily so env overrides work in tests."""
    return Path(os.environ.get("MODELMAN_CONFIG", "~/.config/local-ai/config.yaml")).expanduser()
