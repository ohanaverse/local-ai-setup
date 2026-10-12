"""Where modeltiers reads and writes.

Both functions take the environment as a mapping, so a test passes its own
and never depends on the process environment.
"""

import os
from collections.abc import Mapping
from pathlib import Path

OUT_ENV = "MODELTIERS_OUT"
CACHE_ENV = "MODELTIERS_CACHE"


def _base(env: Mapping[str, str], xdg_var: str, home_dir: str) -> Path:
    """The XDG base directory, or its default under HOME when the variable is
    unset or empty."""
    xdg = env.get(xdg_var, "")
    if xdg:
        return Path(xdg)
    home = env.get("HOME", "")
    return (Path(home) if home else Path.home()) / home_dir


def output_path(env: Mapping[str, str] | None = None) -> Path:
    """`tiers.toml`: MODELTIERS_OUT, else `$XDG_CONFIG_HOME/local-ai/tiers.toml`,
    else `~/.config/local-ai/tiers.toml`."""
    env = os.environ if env is None else env
    override = env.get(OUT_ENV, "")
    if override:
        return Path(override)
    return _base(env, "XDG_CONFIG_HOME", ".config") / "local-ai" / "tiers.toml"


def cache_dir(env: Mapping[str, str] | None = None) -> Path:
    """The snapshot directory: MODELTIERS_CACHE, else
    `$XDG_CACHE_HOME/local-ai/modeltiers`, else `~/.cache/local-ai/modeltiers`."""
    env = os.environ if env is None else env
    override = env.get(CACHE_ENV, "")
    if override:
        return Path(override)
    return _base(env, "XDG_CACHE_HOME", ".cache") / "local-ai" / "modeltiers"


def out_is_overridden(env: Mapping[str, str] | None = None) -> bool:
    """True when MODELTIERS_OUT names the output file."""
    env = os.environ if env is None else env
    return bool(env.get(OUT_ENV, ""))


def registry_is_beside_output(registry: str, env: Mapping[str, str] | None = None) -> bool:
    """True when `registry`, the path wt says it read, is `registry.toml` in
    the output file's directory. Directories are compared resolved, so a
    symlinked config directory still matches; the file itself is not
    resolved, because wt's registry.toml may be a symlink into another tree."""
    found = Path(registry).expanduser()
    expected = output_path(env).parent
    return found.name == "registry.toml" and found.parent.resolve() == expected.resolve()
