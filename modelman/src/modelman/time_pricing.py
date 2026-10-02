"""Time-windowed price rows for a model's Cost.

registry.toml shape (under a model's [models.cost] table)::

    [[models.cost.time_prices]]
    label = "off-peak"
    timezone = "UTC"
    input_price_per_million = 0.66
    windows = [{ days = ["sat", "sun"], start = "00:00", end = "24:00" }]

A Cost's flat prices are the default row. At an instant, the first
TimePrice with a window containing it supplies each price it sets;
omitted fields fall back to the default row. Stored only for now — no
reader besides price_at() (the reference implementation wt will mirror).
See docs/superpowers/specs/2026-09-28-ollama-catalog-sync-design.md.
"""

from __future__ import annotations

import math
import re
from dataclasses import dataclass, field
from datetime import datetime
from typing import TYPE_CHECKING, Any
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

from ._toml_io import drop_none, unknown_keys

if TYPE_CHECKING:
    from .registry import Cost

DAYS = ("mon", "tue", "wed", "thu", "fri", "sat", "sun")
PRICE_FIELDS = (
    "input_price_per_million",
    "cache_price_per_million",
    "output_price_per_million",
)
_DAY_MINUTES = 24 * 60
_HHMM = re.compile(r"^(\d{2}):(\d{2})$")
_TIME_PRICE_KEYS = {"label", "timezone", "windows", *PRICE_FIELDS}
_WINDOW_KEYS = {"days", "start", "end"}


def _minutes(value: Any, what: str) -> int:
    match = _HHMM.match(value) if isinstance(value, str) else None
    if match is None:
        raise ValueError(f"{what} must be HH:MM, got {value!r}")
    hours, minutes = int(match.group(1)), int(match.group(2))
    total = hours * 60 + minutes
    if minutes > 59 or total > _DAY_MINUTES:
        raise ValueError(f"{what} must be HH:MM between 00:00 and 24:00, got {value!r}")
    return total


def _check_price(name: str, value: Any) -> None:
    if value is None:
        return
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ValueError(f"time price `{name}` must be a number")
    if not math.isfinite(value):
        raise ValueError(f"time price `{name}` must be finite")
    if value < 0:
        raise ValueError(f"time price `{name}` must be non-negative")


@dataclass
class Window:
    days: list[str]
    start: str
    end: str
    extra: dict[str, Any] = field(default_factory=dict, repr=False)

    def __post_init__(self) -> None:
        if not isinstance(self.days, list) or not self.days:
            raise ValueError("window `days` must be a non-empty list")
        bad = [d for d in self.days if d not in DAYS]
        if bad:
            raise ValueError(f"window `days` must be drawn from {DAYS}, got {bad}")
        start = _minutes(self.start, "window `start`")
        end = _minutes(self.end, "window `end`")
        if start >= _DAY_MINUTES:
            raise ValueError("window `start` must be before 24:00")
        if start >= end:
            raise ValueError(f"window `start` {self.start} must be before `end` {self.end}")

    def contains(self, local: datetime) -> bool:
        """True when ``local`` (already in this window's timezone) falls in
        [start, end) on one of ``days``."""
        if DAYS[local.weekday()] not in self.days:
            return False
        minute = local.hour * 60 + local.minute
        return _minutes(self.start, "start") <= minute < _minutes(self.end, "end")


@dataclass
class TimePrice:
    timezone: str
    windows: list[Window]
    label: str | None = None
    input_price_per_million: float | None = None
    cache_price_per_million: float | None = None
    output_price_per_million: float | None = None
    extra: dict[str, Any] = field(default_factory=dict, repr=False)

    def __post_init__(self) -> None:
        try:
            ZoneInfo(self.timezone)
        except (ZoneInfoNotFoundError, ValueError, TypeError) as exc:
            raise ValueError(
                f"time price `timezone` {self.timezone!r} is not a known IANA timezone"
            ) from exc
        if not self.windows:
            raise ValueError("time price `windows` must be non-empty")
        for name in PRICE_FIELDS:
            _check_price(name, getattr(self, name))

    def matches(self, at: datetime) -> bool:
        local = at.astimezone(ZoneInfo(self.timezone))
        return any(w.contains(local) for w in self.windows)


def _number(value: Any) -> Any:
    """ints from TOML become floats; anything else is left for validation."""
    if isinstance(value, int) and not isinstance(value, bool):
        return float(value)
    return value


def parse_time_prices(raw: Any) -> list[TimePrice]:
    """Build TimePrice rows from a raw TOML value. Raises ValueError."""
    if raw is None:
        return []
    if not isinstance(raw, list):
        raise ValueError("`time_prices` must be an array of tables")
    rows: list[TimePrice] = []
    for i, row in enumerate(raw):
        if not isinstance(row, dict):
            raise ValueError(f"`time_prices[{i}]` must be a table")
        windows_raw = row.get("windows")
        if not isinstance(windows_raw, list):
            raise ValueError(f"`time_prices[{i}].windows` must be an array of tables")
        windows = []
        for w in windows_raw:
            if not isinstance(w, dict):
                raise ValueError(f"`time_prices[{i}].windows` entries must be tables")
            windows.append(
                Window(
                    days=w.get("days"),  # type: ignore[arg-type]  # validated in __post_init__
                    start=w.get("start"),  # type: ignore[arg-type]
                    end=w.get("end"),  # type: ignore[arg-type]
                    extra=unknown_keys(w, _WINDOW_KEYS),
                )
            )
        try:
            rows.append(
                TimePrice(
                    timezone=row.get("timezone"),  # type: ignore[arg-type]
                    windows=windows,
                    label=row.get("label"),
                    input_price_per_million=_number(row.get("input_price_per_million")),
                    cache_price_per_million=_number(row.get("cache_price_per_million")),
                    output_price_per_million=_number(row.get("output_price_per_million")),
                    extra=unknown_keys(row, _TIME_PRICE_KEYS),
                )
            )
        except ValueError as exc:
            raise ValueError(f"`time_prices[{i}]`: {exc}") from exc
    return rows


def _window_to_dict(w: Window) -> dict[str, Any]:
    return {**w.extra, "days": list(w.days), "start": w.start, "end": w.end}


def time_price_to_dict(tp: TimePrice) -> dict[str, Any]:
    d = {
        "label": tp.label,
        "timezone": tp.timezone,
        "input_price_per_million": tp.input_price_per_million,
        "cache_price_per_million": tp.cache_price_per_million,
        "output_price_per_million": tp.output_price_per_million,
        "windows": [_window_to_dict(w) for w in tp.windows],
    }
    return drop_none({**tp.extra, **d})


def price_at(cost: Cost, at: datetime) -> tuple[float | None, float | None, float | None]:
    """(input, cache, output) price per million tokens in effect at ``at``."""
    if at.tzinfo is None:
        raise ValueError("price_at needs a timezone-aware datetime")
    default = (
        cost.input_price_per_million,
        cost.cache_price_per_million,
        cost.output_price_per_million,
    )
    for tp in cost.time_prices:
        if tp.matches(at):
            overrides = (getattr(tp, name) for name in PRICE_FIELDS)
            inp, cache, out = (
                o if o is not None else d for o, d in zip(overrides, default, strict=True)
            )
            return inp, cache, out
    return default
