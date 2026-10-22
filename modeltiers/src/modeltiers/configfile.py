"""What reading a hand-kept file takes, shared by `aliases.py`, `contracts.py`
and `inventory.py`: the same checks give the same message whichever file the
mistake is in, as `sources/_shape.py` does for the parsers."""

import tomllib
from collections.abc import Callable, Mapping
from importlib import resources
from pathlib import Path
from typing import Any

from modeltiers.errors import ConfigError, ModelTiersError


def read_text(path: Path, label: str, error: Callable[[str], ModelTiersError] = ConfigError) -> str:
    """The file's text. `label` starts the message; `error` is its class."""
    try:
        return path.read_text(encoding="utf-8")
    except UnicodeDecodeError as exc:
        raise error(f"{label}: {path} is not UTF-8 text: {exc}") from exc
    except OSError as exc:
        raise error(f"{label}: cannot read {path}: {exc.strerror}") from exc


def load[T](path: Path | None, name: str, parse: Callable[..., T]) -> T:
    """`parse(text, origin=...)` over `path`, or over the committed
    `data/<name>.toml` when it is None."""
    if path is None:
        text = resources.files("modeltiers").joinpath(f"data/{name}.toml").read_text("utf-8")
        return parse(text, origin=f"{name}.toml")
    return parse(read_text(path, name), origin=path.name)


def parse_toml(origin: str, text: str) -> dict[str, Any]:
    try:
        return tomllib.loads(text)
    except tomllib.TOMLDecodeError as exc:
        raise ConfigError(f"{origin}: not TOML: {exc}") from exc


def text(where: str, entry: Mapping[str, Any], key: str) -> str:
    value = entry.get(key)
    if not isinstance(value, str) or not value.strip():
        raise ConfigError(f"{where}: {key} is missing, empty or not a string")
    return value


def known_keys(where: str, entry: Mapping[str, Any], allowed: frozenset[str]) -> None:
    unknown = sorted(set(entry) - allowed)
    if unknown:
        raise ConfigError(f"{where}: unknown key {unknown[0]!r}")


def tables(origin: str, doc: Mapping[str, Any], key: str) -> list[dict[str, Any]]:
    """The `[[key]]` array; an absent one is empty."""
    value = doc.get(key, [])
    if not isinstance(value, list) or not all(isinstance(v, dict) for v in value):
        raise ConfigError(f"{origin}: {key} is not an array of tables; write [[{key}]]")
    return value
