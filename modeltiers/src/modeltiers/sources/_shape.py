"""Shape checks the parsers share. Each raises SourceParseError."""

import json
from typing import Any

from modeltiers.errors import SourceParseError


def load_json(source: str, data: bytes) -> Any:
    try:
        return json.loads(data)
    except ValueError as exc:
        raise SourceParseError(f"{source}: not JSON: {exc}") from exc


def number(source: str, where: str, value: Any) -> float:
    """A JSON number as a float. A bool, a string or null is not a number."""
    if isinstance(value, bool) or not isinstance(value, int | float):
        raise SourceParseError(f"{source}: {where} is not a number: {value!r}")
    return float(value)


def optional_number(source: str, where: str, value: Any) -> float | None:
    return None if value is None else number(source, where, value)


def require_rows(source: str, count: int, min_rows: int) -> None:
    if count < min_rows:
        raise SourceParseError(f"{source}: {count} rows, under the minimum of {min_rows}")
