"""Shape checks the parsers share. Each raises SourceParseError."""

import json
import math
from typing import Any

from modeltiers.errors import SourceParseError


def load_json(source: str, data: bytes) -> Any:
    try:
        return json.loads(data)
    except ValueError as exc:
        raise SourceParseError(f"{source}: not JSON: {exc}") from exc


def number(source: str, where: str, value: Any) -> float:
    """A JSON number as a float. A bool, a string or null is not a number, and
    neither is a non-finite float (`NaN`, `Infinity`): `json.loads` accepts
    those spellings, and a score that is not a number would poison every
    average or vote taken over it downstream."""
    if isinstance(value, bool) or not isinstance(value, int | float):
        raise SourceParseError(f"{source}: {where} is not a number: {value!r}")
    result = float(value)
    if not math.isfinite(result):
        raise SourceParseError(f"{source}: {where} is not a finite number: {value!r}")
    return result


def optional_number(source: str, where: str, value: Any) -> float | None:
    return None if value is None else number(source, where, value)


def require_rows(source: str, count: int, min_rows: int) -> None:
    if count < min_rows:
        raise SourceParseError(f"{source}: {count} rows, under the minimum of {min_rows}")
