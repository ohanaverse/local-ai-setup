# Ollama Catalog Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add time-windowed (peak/off-peak) pricing to modelman's registry
schema, plus a `modelman ollama-catalog sync` command and an
`ollama-catalog` Claude skill. Together they keep modelman's ollama cloud
entries in line with <https://ollama.com/pricing>.

**Architecture:**
- **Schema.** A new `time_pricing.py` module owns the `TimePrice`/`Window`
  types, their validation and TOML (de)serialization, and `price_at()`.
  `registry.py`'s `Cost` gains `time_prices: list[TimePrice]`.
- **Sync logic.** A new `ollama_catalog.py` holds three pure pieces — the
  header-keyed HTML parser, `plan_sync()` and `apply_sync()` — plus I/O
  helpers with injectable runners.
- **CLI.** A new `ollama_catalog_cli.py` Typer sub-app is mounted as
  `modelman ollama-catalog`. Deletes of registered models reuse
  `main.run_queued_ops`.
- **Skill.** `SKILL.md` drives the CLI.

**Tech Stack:** Python 3.13, stdlib `html.parser`/`zoneinfo`, `requests`,
Typer, pytest; Go (BurntSushi/toml) for wt's decode-only mirror.

**Spec:** `docs/superpowers/specs/2026-09-28-ollama-catalog-sync-design.md`

## Global Constraints

- Python `==3.13.*`; no new runtime dependencies (stdlib `html.parser`,
  `zoneinfo`; `requests` is already a dependency).
- Off-peak prices are **stored only**. No TUI/wt display change. The
  existing flat `Cost` prices remain the default row that every current
  reader uses.
- Resolution rule: the first `time_prices` row with a window containing
  *t* (in that row's IANA `timezone`) wins. Each price field falls back to
  the default row when the row omits it. No match → the default row.
- A window is `days` ⊆ `mon tue wed thu fri sat sun` (non-empty), `start`
  `HH:MM` < `end` `HH:MM`. `end` may be `24:00`; start is inclusive, end
  exclusive; a window never crosses midnight.
- The Ollama off-peak window is fixed in code: UTC, weekdays outside
  12:00–18:00, and all day on weekends. Its label is `"off-peak"`.
- Page name `X` maps to the tag `X:cloud`, or `X-cloud` when `X` contains
  `:`. A matched entry records `catalog_name = X` in its model-level
  unknown keys.
- New entries: `id = "ollama/<tag>"`, `provider_id = "ollama"`,
  `location = "cloud"`, `source = "curated"` (never a new source value —
  wt treats `source` as a `curated`/`discovered` enum). They are never
  pulled, marked ready or exposed.
- Delete candidates are **only** cloud-stub tags (`:cloud`/`-cloud`) in
  `ollama list` that are not on the page. Every delete is prompted,
  default no. `--yes` never covers deletes.
- Registry ollama cloud entries that are not on the page and not pulled
  are **reported only** — never modified.
- Exit codes: `2` fetch failure, `3` parse failure (raw HTML saved to
  `$TMPDIR/ollama-pricing-<timestamp>.html`), `1` save/delete failure,
  `0` otherwise. Nothing is written on exit 2 or 3.
- No test touches the network or the live `ollama` daemon. New default
  runners get autouse guards in `tests/conftest.py`.
- Repo rules (`modelman/CLAUDE.md`):
  - run `make check` (ruff lint + `ruff format --check` + mypy) before
    calling any task done;
  - `zip()` needs `strict=`;
  - test files use function-local imports;
  - run `make format` before committing.

## Review Focus

1. **The page silently changes its price format** (e.g. `USD 0.30`).
   Expected: parsing fails with exit 3, not a sync that writes `None` over
   every price. Pinned in Task 3 (`test_majority_unrecognized_prices_raises`).
2. **A TUI edit of a synced model.** Expected: `time_prices` and
   `catalog_name` survive the edit. Pinned in Task 1
   (`test_edit_carryover_preserves_time_prices_and_extra`).
3. **Registry changed on disk between plan and apply** (e.g. the TUI's
   price-refresh worker). Expected: the apply recomputes against the
   freshly-locked registry rather than writing a stale snapshot. Pinned in
   Task 5 (`test_sync_applies_against_fresh_registry`).
4. **The `ollama` daemon is down.** Expected: prices still update, no
   delete prompts, a warning is printed, and every unmatched cloud entry is
   reported as unlisted. Pinned in Task 4 (`test_plan_without_ollama_list`)
   and Task 5 (`test_sync_ollama_down_still_updates`).
5. **A page model that shares its name with a local model**
   (`gpt-oss:20b` is on the page *and* is a local registry entry).
   Expected: the local entry is untouched, and `ollama/gpt-oss:20b-cloud`
   is added. Pinned in Task 4
   (`test_plan_does_not_touch_local_namesake`).

---

## File Structure

| File | Responsibility |
|---|---|
| `modelman/src/modelman/time_pricing.py` (new) | `Window`, `TimePrice`, validation, `parse_time_prices`, `time_price_to_dict`, `price_at` |
| `modelman/src/modelman/registry.py` (modify) | `Cost.time_prices`; parse/serialize it; whitelist key |
| `modelman/src/modelman/pricing.py` (modify) | `_merge_api_cost` carries `time_prices` |
| `modelman/src/modelman/screens/models.py` (modify) | TUI edit carries over `time_prices`, `cost.extra`, model `extra` |
| `docs/contracts/registry.sample.toml` (modify) | one `time_prices` row on the cloud fixture model |
| `wt/internal/config/config.go` (modify) | decode-only `TimePrices` on `ModelCost` |
| `modelman/src/modelman/ollama_catalog.py` (new) | fetch, parse, `ollama list`/`rm`, `plan_sync`, `apply_sync`, `format_plan` |
| `modelman/src/modelman/ollama_catalog_cli.py` (new) | `ollama_catalog_app` with `sync` command |
| `modelman/src/modelman/main.py` (modify) | mount `ollama-catalog` sub-app |
| `modelman/tests/fixtures/ollama_pricing.html` (new) | captured pricing page |
| `modelman/.claude/skills/ollama-catalog/SKILL.md` (new) | the skill |
| `modelman/README.md`, `modelman/CLAUDE.md`, `CLAUDE.md` (modify) | docs |

All `uv run`/`make` commands below run from `modelman/` unless shown
otherwise.

---

### Task 1: Time-windowed pricing schema

**Files:**
- Create: `modelman/src/modelman/time_pricing.py`
- Modify: `modelman/src/modelman/registry.py` (`Cost` ~L116, `_cost_to_dict` ~L469, `_cost_from_dict` ~L474, `_parse_cost` ~L724)
- Modify: `modelman/src/modelman/pricing.py` (`_merge_api_cost` ~L101)
- Modify: `modelman/src/modelman/screens/models.py` (`_on_edit_model` ~L1134)
- Test: `modelman/tests/test_time_pricing.py` (new), `modelman/tests/test_registry.py`, `modelman/tests/test_pricing.py`, `modelman/tests/screens/test_models.py`

**Interfaces:**
- Produces:
  - `time_pricing.DAYS: tuple[str, ...]`
  - `time_pricing.PRICE_FIELDS: tuple[str, str, str]`
  - `Window(days: list[str], start: str, end: str, extra: dict = {})` with `.contains(local: datetime) -> bool`
  - `TimePrice(timezone: str, windows: list[Window], label: str | None = None, input_price_per_million: float | None = None, cache_price_per_million: float | None = None, output_price_per_million: float | None = None, extra: dict = {})` with `.matches(at: datetime) -> bool`
  - `parse_time_prices(raw: Any) -> list[TimePrice]` (raises `ValueError`)
  - `time_price_to_dict(tp: TimePrice) -> dict[str, Any]`
  - `price_at(cost: Cost, at: datetime) -> tuple[float | None, float | None, float | None]`
  - `Cost.time_prices: list[TimePrice]` (default `[]`)
  - `screens.models._carry_over_unedited(old: ModelEntry, new: ModelEntry) -> None`

- [ ] **Step 1: Write failing tests for `time_pricing`**

Create `modelman/tests/test_time_pricing.py`:

```python
"""Tests for time-windowed pricing rows (time_pricing.py)."""

from __future__ import annotations

from datetime import UTC, datetime

import pytest

WEEKDAYS = ["mon", "tue", "wed", "thu", "fri"]


def _offpeak(**prices):
    from modelman.time_pricing import TimePrice, Window

    return TimePrice(
        label="off-peak",
        timezone="UTC",
        windows=[
            Window(days=WEEKDAYS, start="00:00", end="12:00"),
            Window(days=WEEKDAYS, start="18:00", end="24:00"),
            Window(days=["sat", "sun"], start="00:00", end="24:00"),
        ],
        **prices,
    )


def _cost(time_prices):
    from modelman.registry import Cost

    return Cost(
        input_price_per_million=1.32,
        cache_price_per_million=0.044,
        output_price_per_million=3.96,
        time_prices=time_prices,
    )


# 2026-09-28 is a Monday.
@pytest.mark.parametrize(
    ("at", "expected"),
    [
        (datetime(2026, 9, 28, 11, 59, tzinfo=UTC), (0.66, 0.022, 1.98)),
        (datetime(2026, 9, 28, 12, 0, tzinfo=UTC), (1.32, 0.044, 3.96)),
        (datetime(2026, 9, 28, 17, 59, 59, tzinfo=UTC), (1.32, 0.044, 3.96)),
        (datetime(2026, 9, 28, 18, 0, tzinfo=UTC), (0.66, 0.022, 1.98)),
        (datetime(2026, 10, 3, 14, 0, tzinfo=UTC), (0.66, 0.022, 1.98)),  # Saturday
    ],
)
def test_price_at_offpeak_boundaries(at, expected):
    from modelman.time_pricing import price_at

    cost = _cost(
        [
            _offpeak(
                input_price_per_million=0.66,
                cache_price_per_million=0.022,
                output_price_per_million=1.98,
            )
        ]
    )
    assert price_at(cost, at) == expected


def test_price_at_non_utc_timezone():
    from modelman.time_pricing import TimePrice, Window, price_at

    # 09:00-10:00 in Honolulu (UTC-10) is 19:00-20:00 UTC.
    tp = TimePrice(
        timezone="Pacific/Honolulu",
        windows=[Window(days=["mon"], start="09:00", end="10:00")],
        input_price_per_million=0.5,
    )
    cost = _cost([tp])
    assert price_at(cost, datetime(2026, 9, 28, 19, 30, tzinfo=UTC))[0] == 0.5
    assert price_at(cost, datetime(2026, 9, 28, 9, 30, tzinfo=UTC))[0] == 1.32


def test_price_at_per_field_fallback_to_default():
    from modelman.time_pricing import price_at

    cost = _cost([_offpeak(input_price_per_million=0.66)])  # cache/output omitted
    assert price_at(cost, datetime(2026, 10, 3, 1, 0, tzinfo=UTC)) == (0.66, 0.044, 3.96)


def test_price_at_first_matching_row_wins():
    from modelman.time_pricing import TimePrice, Window, price_at

    everything = [Window(days=["sat", "sun"], start="00:00", end="24:00")]
    first = TimePrice(timezone="UTC", windows=everything, input_price_per_million=0.1)
    second = TimePrice(timezone="UTC", windows=everything, input_price_per_million=0.2)
    cost = _cost([first, second])
    assert price_at(cost, datetime(2026, 10, 3, 1, 0, tzinfo=UTC))[0] == 0.1


def test_price_at_rejects_naive_datetime():
    from modelman.time_pricing import price_at

    with pytest.raises(ValueError, match="timezone-aware"):
        price_at(_cost([]), datetime(2026, 9, 28, 12, 0))


@pytest.mark.parametrize(
    ("kwargs", "match"),
    [
        ({"days": [], "start": "00:00", "end": "01:00"}, "non-empty"),
        ({"days": ["monday"], "start": "00:00", "end": "01:00"}, "drawn from"),
        ({"days": ["mon"], "start": "1:00", "end": "02:00"}, "HH:MM"),
        ({"days": ["mon"], "start": "24:00", "end": "24:00"}, "before 24:00"),
        ({"days": ["mon"], "start": "10:00", "end": "09:00"}, "before `end`"),
        ({"days": ["mon"], "start": "10:00", "end": "24:30"}, "HH:MM"),
    ],
)
def test_window_validation(kwargs, match):
    from modelman.time_pricing import Window

    with pytest.raises(ValueError, match=match):
        Window(**kwargs)


def test_time_price_validation():
    from modelman.time_pricing import TimePrice, Window

    w = [Window(days=["mon"], start="00:00", end="01:00")]
    with pytest.raises(ValueError, match="IANA"):
        TimePrice(timezone="Mars/Olympus", windows=w)
    with pytest.raises(ValueError, match="windows"):
        TimePrice(timezone="UTC", windows=[])
    with pytest.raises(ValueError, match="non-negative"):
        TimePrice(timezone="UTC", windows=w, input_price_per_million=-1.0)


def test_parse_and_serialize_round_trip_preserves_unknown_keys():
    from modelman.time_pricing import parse_time_prices, time_price_to_dict

    raw = [
        {
            "label": "off-peak",
            "timezone": "UTC",
            "input_price_per_million": 1,
            "note": "hand-edited",
            "windows": [{"days": ["sat"], "start": "00:00", "end": "24:00", "why": "x"}],
        }
    ]
    parsed = parse_time_prices(raw)
    assert parsed[0].input_price_per_million == 1.0
    out = time_price_to_dict(parsed[0])
    assert out["note"] == "hand-edited"
    assert out["windows"][0]["why"] == "x"
    assert "cache_price_per_million" not in out


def test_parse_time_prices_shape_errors():
    from modelman.time_pricing import parse_time_prices

    assert parse_time_prices(None) == []
    with pytest.raises(ValueError, match="array"):
        parse_time_prices({"timezone": "UTC"})
    with pytest.raises(ValueError, match=r"time_prices\[0\]"):
        parse_time_prices(["nope"])
    with pytest.raises(ValueError, match="windows"):
        parse_time_prices([{"timezone": "UTC"}])
```

- [ ] **Step 2: Run to verify failure**

Run: `uv run pytest tests/test_time_pricing.py -q`
Expected: FAIL — `ModuleNotFoundError: No module named 'modelman.time_pricing'`

- [ ] **Step 3: Implement `time_pricing.py`**

Create `modelman/src/modelman/time_pricing.py`:

```python
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


def price_at(
    cost: Cost, at: datetime
) -> tuple[float | None, float | None, float | None]:
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
```

(Leave the `# type: ignore` comments out if mypy reports them as unused.)

- [ ] **Step 4: Add `Cost.time_prices` to `registry.py`**

At the imports:

```python
from .time_pricing import TimePrice, parse_time_prices, time_price_to_dict
```

(`time_pricing` imports `Cost` only under `TYPE_CHECKING`, so there's no
import cycle.)

After `_LEGACY_COST_FIELDS`:

```python
# Keys handled explicitly (not flat scalars) that must never land in
# Cost.extra.
_COST_STRUCTURED_FIELDS = {"time_prices"}
```

In `class Cost`, add before `extra`:

```python
    time_prices: list[TimePrice] = field(default_factory=list)
```

In `_validate_cost`, after the subscription check:

```python
    for tp in cost.time_prices:
        if not isinstance(tp, TimePrice):
            raise ValueError(f"{source} `time_prices` entries must be TimePrice")
```

Replace `_cost_to_dict`:

```python
def _cost_to_dict(c: Cost) -> dict[str, Any]:
    d: dict[str, Any] = {field: getattr(c, field) for field in _COST_FIELDS}
    d["time_prices"] = [time_price_to_dict(tp) for tp in c.time_prices] or None
    return drop_none({**c.extra, **d})
```

In `_cost_from_dict`, add
`time_prices=parse_time_prices(d.get("time_prices")),` and change the
`extra=` line to
`extra=unknown_keys(d, _COST_FIELDS | _LEGACY_COST_FIELDS | _COST_STRUCTURED_FIELDS),`.

In `_parse_cost`, change **every** `unknown_keys(cost_raw, _COST_FIELDS | _LEGACY_COST_FIELDS)`
to `unknown_keys(cost_raw, _COST_FIELDS | _LEGACY_COST_FIELDS | _COST_STRUCTURED_FIELDS)`.
Then, in the final "New flat fields" `_build_cost(...)` call, add:

```python
        time_prices=_time_prices_or_error(model_id, cost_raw.get("time_prices")),
```

and define next to `_build_cost`:

```python
def _time_prices_or_error(model_id: str, raw: Any) -> list[TimePrice]:
    try:
        return parse_time_prices(raw)
    except ValueError as exc:
        raise RegistryError(f"Model `{model_id}` cost {exc}") from exc
```

- [ ] **Step 5: Add registry round-trip tests**

Append to `modelman/tests/test_registry.py`:

```python
def test_cost_time_prices_round_trip(tmp_path):
    from modelman.registry import load_registry, save_registry

    path = tmp_path / "registry.toml"
    path.write_text(
        """
[[providers]]
id = "ollama"
name = "Ollama"
[providers.auth]
type = "none"

[[models]]
id = "ollama/deepseek-v4-pro:cloud"
family = "deepseek"
provider_id = "ollama"
model_name = "deepseek-v4-pro:cloud"
location = "cloud"
[models.cost]
input_price_per_million = 1.32
output_price_per_million = 3.96
[[models.cost.time_prices]]
label = "off-peak"
timezone = "UTC"
input_price_per_million = 0.66
custom = "kept"
windows = [{ days = ["sat", "sun"], start = "00:00", end = "24:00" }]
"""
    )
    registry = load_registry(path)
    cost = registry.model("ollama/deepseek-v4-pro:cloud").cost
    assert cost is not None
    assert "time_prices" not in cost.extra
    assert cost.time_prices[0].input_price_per_million == 0.66
    assert cost.time_prices[0].windows[0].days == ["sat", "sun"]

    save_registry(registry, path)
    again = load_registry(path).model("ollama/deepseek-v4-pro:cloud").cost
    assert again is not None
    assert again.time_prices[0].extra == {"custom": "kept"}
    assert again.time_prices[0].label == "off-peak"


def test_cost_time_prices_invalid_is_registry_error(tmp_path):
    import pytest

    from modelman.registry import RegistryError, load_registry

    path = tmp_path / "registry.toml"
    path.write_text(
        """
[[models]]
id = "m"
family = "f"
provider_id = "ollama"
model_name = "m"
[models.cost]
[[models.cost.time_prices]]
timezone = "Nowhere/Nope"
windows = [{ days = ["sat"], start = "00:00", end = "24:00" }]
"""
    )
    with pytest.raises(RegistryError, match="Model `m` cost .*IANA"):
        load_registry(path)
```

(If `test_registry.py` uses a different provider-table shape in its
existing fixtures, copy that shape — the point is one ollama provider
plus one model.)

- [ ] **Step 6: Carry `time_prices` through the OpenRouter merge**

In `pricing.py::_merge_api_cost`'s returned `Cost(...)`, add
`time_prices=list(existing.time_prices),`. Append to
`modelman/tests/test_pricing.py`:

```python
def test_merge_api_cost_preserves_time_prices():
    from modelman.pricing import _merge_api_cost
    from modelman.registry import Cost
    from modelman.time_pricing import TimePrice, Window

    tp = TimePrice(
        timezone="UTC",
        windows=[Window(days=["sat"], start="00:00", end="24:00")],
        input_price_per_million=0.1,
    )
    existing = Cost(input_price_per_million=1.0, time_prices=[tp])
    merged = _merge_api_cost(existing, Cost(input_price_per_million=2.0))
    assert merged.input_price_per_million == 2.0
    assert merged.time_prices == [tp]
```

- [ ] **Step 7: Keep a TUI edit from dropping `time_prices`/extra**

In `screens/models.py`, add below `_cost_changed`:

```python
def _carry_over_unedited(old: ModelEntry, new: ModelEntry) -> None:
    """Copy fields the edit dialog cannot show back onto the rebuilt entry.

    ModelForm rebuilds Cost/ModelEntry from its inputs, which carry no
    time-windowed prices, unknown cost keys, or model-level unknown keys
    (e.g. `catalog_name`, written by `modelman ollama-catalog sync`).
    Without this, one TUI edit silently strips them.
    """
    new.extra = {**old.extra, **new.extra}
    if old.cost is not None and new.cost is not None:
        new.cost.time_prices = list(old.cost.time_prices)
        new.cost.extra = {**old.cost.extra, **new.cost.extra}
```

(Import `ModelEntry` from `..registry` if it isn't already imported there.)
In `_on_edit_model`, call `_carry_over_unedited(old_entry, new_entry)`
right after `new_entry = _variant_to_model_entry(...)`. Append to
`modelman/tests/screens/test_models.py`:

```python
def test_edit_carryover_preserves_time_prices_and_extra():
    from modelman.registry import Cost, ModelEntry
    from modelman.screens.models import _carry_over_unedited
    from modelman.time_pricing import TimePrice, Window

    tp = TimePrice(timezone="UTC", windows=[Window(days=["sun"], start="00:00", end="24:00")])
    old = ModelEntry(
        id="ollama/x:cloud",
        family="x",
        provider_id="ollama",
        model_name="x:cloud",
        cost=Cost(input_price_per_million=1.0, time_prices=[tp], extra={"k": 1}),
        extra={"catalog_name": "x"},
    )
    new = ModelEntry(
        id="ollama/x:cloud",
        family="x",
        provider_id="ollama",
        model_name="x:cloud",
        cost=Cost(input_price_per_million=2.0),
    )
    _carry_over_unedited(old, new)
    assert new.extra == {"catalog_name": "x"}
    assert new.cost is not None
    assert new.cost.time_prices == [tp]
    assert new.cost.extra == {"k": 1}
    assert new.cost.input_price_per_million == 2.0
```

- [ ] **Step 8: Run the focused tests and checks**

Run: `uv run pytest tests/test_time_pricing.py tests/test_registry.py tests/test_pricing.py tests/screens/test_models.py -q && make check`
Expected: all PASS; `make check` clean. Fix any mypy complaints (e.g.
unused `type: ignore`) before moving on.

- [ ] **Step 9: Commit**

```bash
make format
git add src/modelman/time_pricing.py src/modelman/registry.py src/modelman/pricing.py \
  src/modelman/screens/models.py tests/test_time_pricing.py tests/test_registry.py \
  tests/test_pricing.py tests/screens/test_models.py
git commit -m "feat(modelman): time-windowed pricing rows on Cost

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Cross-language contract for `time_prices`

**Files:**
- Modify: `docs/contracts/registry.sample.toml` (the `openrouter/contract-fixture:cloud` model, ~L80-93)
- Modify: `modelman/tests/contracts/test_registry_fixture.py`
- Modify: `wt/internal/config/config.go` (`ModelCost` ~L433)
- Modify: `wt/internal/config/registry_fixture_test.go` (`TestRegistryFixtureCost`)

**Interfaces:**
- Consumes: `Cost.time_prices` (Task 1).
- Produces: Go `config.CostWindow{Days []string; Start, End string}`, `config.TimePrice{Label, Timezone string; InputPricePerMillion, CachePricePerMillion, OutputPricePerMillion *float64; Windows []CostWindow}`, `ModelCost.TimePrices []TimePrice`.

- [ ] **Step 1: Add the fixture row**

In `docs/contracts/registry.sample.toml`, directly after the
`openrouter/contract-fixture:cloud` model's `subscription_period` line
(and before the next `[[models]]`), insert:

```toml
# Time-windowed price rows (2026-09-28 ollama-catalog-sync design): the
# flat prices above are the default; the first row whose window contains
# the instant (in its IANA timezone) overrides each field it sets.
[[models.cost.time_prices]]
label    = "off-peak"
timezone = "UTC"
input_price_per_million  = 0.25
cache_price_per_million  = 0.125
output_price_per_million = 0.50
windows = [
  { days = ["mon", "tue", "wed", "thu", "fri"], start = "00:00", end = "12:00" },
  { days = ["mon", "tue", "wed", "thu", "fri"], start = "18:00", end = "24:00" },
  { days = ["sat", "sun"], start = "00:00", end = "24:00" },
]
```

- [ ] **Step 2: Extend the Python contract test (failing until the fixture parses)**

In `test_load_registry_matches_shared_fixture`, after the existing
`cloud_model.cost` assertions, add:

```python
    (offpeak,) = cloud_model.cost.time_prices
    assert offpeak.label == "off-peak"
    assert offpeak.timezone == "UTC"
    assert offpeak.input_price_per_million == 0.25
    assert offpeak.cache_price_per_million == 0.125
    assert offpeak.output_price_per_million == 0.50
    assert [w.days for w in offpeak.windows][2] == ["sat", "sun"]
    assert (offpeak.windows[1].start, offpeak.windows[1].end) == ("18:00", "24:00")
```

Run: `uv run pytest tests/contracts/test_registry_fixture.py -q`
Expected: PASS (Task 1 already parses the shape).

- [ ] **Step 3: Write the failing Go assertion**

In `wt/internal/config/registry_fixture_test.go`, add a new test:

```go
// TestRegistryFixtureTimePrices pins the time-windowed pricing rows
// (docs/superpowers/specs/2026-09-28-ollama-catalog-sync-design.md) that
// modelman writes under [models.cost]. wt only decodes them today.
func TestRegistryFixtureTimePrices(t *testing.T) {
	t.Setenv("MODELMAN_REGISTRY", "../../../docs/contracts/registry.sample.toml")

	_, models, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry() error: %v", err)
	}
	var cloud *Model
	for i := range models {
		if models[i].ID == "openrouter/contract-fixture:cloud" {
			cloud = &models[i]
		}
	}
	if cloud == nil {
		t.Fatal("missing priced cloud model in fixture")
	}
	if len(cloud.Cost.TimePrices) != 1 {
		t.Fatalf("got %d time prices, want 1", len(cloud.Cost.TimePrices))
	}
	tp := cloud.Cost.TimePrices[0]
	if tp.Label != "off-peak" || tp.Timezone != "UTC" {
		t.Errorf("time price decoded wrong: %+v", tp)
	}
	if tp.InputPricePerMillion == nil || *tp.InputPricePerMillion != 0.25 {
		t.Errorf("input price = %v, want 0.25", tp.InputPricePerMillion)
	}
	if tp.OutputPricePerMillion == nil || *tp.OutputPricePerMillion != 0.50 {
		t.Errorf("output price = %v, want 0.50", tp.OutputPricePerMillion)
	}
	if len(tp.Windows) != 3 || tp.Windows[1].Start != "18:00" || tp.Windows[1].End != "24:00" {
		t.Errorf("windows decoded wrong: %+v", tp.Windows)
	}
	if got := tp.Windows[2].Days; len(got) != 2 || got[0] != "sat" || got[1] != "sun" {
		t.Errorf("weekend window days = %v", got)
	}
}
```

Run: `cd ../wt && go test ./internal/config -run TestRegistryFixture -v`
Expected: FAIL to compile — `cloud.Cost.TimePrices undefined`.

- [ ] **Step 4: Add the Go types**

In `wt/internal/config/config.go`, above `ModelCost`:

```go
// CostWindow is one [start, end) span on the listed weekdays, in its
// TimePrice's timezone. Days use "mon".."sun"; times are "HH:MM" and end
// may be "24:00".
type CostWindow struct {
	Days  []string `toml:"days"`
	Start string   `toml:"start"`
	End   string   `toml:"end"`
}

// TimePrice is a time-windowed override of a ModelCost's flat (default)
// per-token prices, written by modelman (e.g. ollama off-peak pricing).
// Decode-only in wt for now: the first row whose window contains an
// instant wins, per field, falling back to the flat prices — see
// modelman's time_pricing.price_at for the reference implementation.
type TimePrice struct {
	Label                 string       `toml:"label,omitempty"`
	Timezone              string       `toml:"timezone"`
	InputPricePerMillion  *float64     `toml:"input_price_per_million,omitempty"`
	CachePricePerMillion  *float64     `toml:"cache_price_per_million,omitempty"`
	OutputPricePerMillion *float64     `toml:"output_price_per_million,omitempty"`
	Windows               []CostWindow `toml:"windows"`
}
```

and add to `ModelCost`:

```go
	TimePrices            []TimePrice `toml:"time_prices,omitempty"`
```

- [ ] **Step 5: Run the wt and modelman contract tests**

Run: `cd ../wt && gofmt -l internal/config; go vet ./internal/config && go test ./internal/config ./cmd/wt -q`
Expected: PASS, and `gofmt -l` prints nothing.
Run: `uv run pytest tests/contracts -q`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd .. && git add docs/contracts/registry.sample.toml modelman/tests/contracts/test_registry_fixture.py \
  wt/internal/config/config.go wt/internal/config/registry_fixture_test.go
git commit -m "feat(contracts): time_prices rows in registry fixture; wt decodes them

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Pricing page fetch + parser

**Files:**
- Create: `modelman/src/modelman/ollama_catalog.py`
- Create: `modelman/tests/fixtures/ollama_pricing.html`
- Create: `modelman/tests/test_ollama_catalog.py`
- Modify: `modelman/tests/conftest.py` (`_never_call_real_ollama`)

**Interfaces:**
- Produces:
  - `PRICING_URL: str`
  - `OFFPEAK_LABEL = "off-peak"`
  - `MIN_ROWS = 5`
  - `PriceTriple(input: float | None, cache: float | None, output: float | None)` (frozen dataclass)
  - `CatalogModel(name: str, prices: PriceTriple, offpeak: PriceTriple | None = None)`
  - `Catalog(models: list[CatalogModel], warnings: list[str])`
  - `CatalogParseError(check: str)` (`.check` attribute)
  - `CatalogFetchError`
  - `fetch_pricing_html(runner=None) -> str`
  - `parse_pricing(html: str) -> Catalog`
  - `save_failed_html(html: str, directory: Path | None = None) -> Path`
  - `offpeak_time_price(p: PriceTriple) -> TimePrice`

- [ ] **Step 1: Capture the fixture**

```bash
curl -sSL https://ollama.com/pricing -o tests/fixtures/ollama_pricing.html
python3 - <<'EOF'
h = open("tests/fixtures/ollama_pricing.html").read()
t = h[h.find("<table"):h.find("</table>")]
print("rows incl header:", t.count("<tr"), "off-peak rows:", t.count("Off-Peak"))
EOF
```

Expected on 2026-09-28: `rows incl header: 20 off-peak rows: 2`, i.e. 17
base models, with the off-peak rows belonging to `deepseek-v4.1-flash`
and `deepseek-v4-pro`. **If the live counts differ, update the numbers
in the Step 2 happy-path test to match the captured file**. The tests
pin the fixture, not the live site.

- [ ] **Step 2: Write failing parser tests**

Create `modelman/tests/test_ollama_catalog.py`:

```python
"""Tests for ollama_catalog.py (ollama.com/pricing parser + sync plan)."""

from __future__ import annotations

from pathlib import Path

import pytest

FIXTURE = Path(__file__).parent / "fixtures" / "ollama_pricing.html"

_HEAD = "<tr><th>Model</th><th>Input</th><th>Cached input</th><th>Output</th></tr>"


def _row(name: str, inp: str, cache: str, out: str) -> str:
    return (
        f'<tr><td><a href="/library/{name}">{name}</a></td>'
        f"<td>{inp}</td><td>{cache}</td><td>{out}</td></tr>"
    )


def _page(*rows: str, head: str = _HEAD) -> str:
    base = [_row(f"m{i}", "$1.00", "$0.10", "$2.00") for i in range(5)]
    return f"<html><table><thead>{head}</thead><tbody>{''.join(base + list(rows))}</tbody></table></html>"


def test_parse_fixture_happy_path():
    from modelman.ollama_catalog import PriceTriple, parse_pricing

    catalog = parse_pricing(FIXTURE.read_text())
    by_name = {m.name: m for m in catalog.models}
    assert len(catalog.models) == 17
    assert by_name["deepseek-v4-pro"].prices == PriceTriple(1.32, 0.044, 3.96)
    assert by_name["deepseek-v4-pro"].offpeak == PriceTriple(0.66, 0.022, 1.98)
    assert by_name["deepseek-v4.1-flash"].offpeak == PriceTriple(0.15, 0.003, 0.60)
    assert by_name["gemma4"].offpeak is None
    assert by_name["gpt-oss:120b"].prices.input == 0.15
    assert by_name["mistral-large-3"].prices.cache is None  # "-" cell
    assert not any("Off-Peak" in m.name for m in catalog.models)
    assert catalog.warnings == []


def test_columns_found_by_header_not_position():
    from modelman.ollama_catalog import PriceTriple, parse_pricing

    head = "<tr><th>Output</th><th>Model</th><th>Cached Input</th><th>Input</th></tr>"
    extra = '<tr><td>$9.00</td><td><a href="#">zz</a></td><td>$0.50</td><td>$3.00</td></tr>'
    html = (
        "<table><thead>" + head + "</thead><tbody>"
        + "".join(
            f"<tr><td>$2</td><td>m{i}</td><td>$0.1</td><td>$1</td></tr>" for i in range(5)
        )
        + extra
        + "</tbody></table>"
    )
    catalog = parse_pricing(html)
    zz = next(m for m in catalog.models if m.name == "zz")
    assert zz.prices == PriceTriple(3.0, 0.5, 9.0)


def test_offpeak_suffix_variants_fold_into_base():
    from modelman.ollama_catalog import PriceTriple, parse_pricing

    html = _page(
        _row("zz (off-peak)", "$0.50", "-", "$1.00"),  # before its base row
        _row("zz", "$1.00", "$0.10", "$2.00"),
    )
    zz = next(m for m in parse_pricing(html).models if m.name == "zz")
    assert zz.offpeak == PriceTriple(0.5, None, 1.0)


def test_missing_header_raises_with_found_headers():
    from modelman.ollama_catalog import CatalogParseError, parse_pricing

    head = "<tr><th>Model</th><th>Prompt</th><th>Cached input</th><th>Output</th></tr>"
    with pytest.raises(CatalogParseError) as exc:
        parse_pricing(_page(head=head))
    assert "Prompt" in exc.value.check


def test_no_table_raises():
    from modelman.ollama_catalog import CatalogParseError, parse_pricing

    with pytest.raises(CatalogParseError, match="no <table>"):
        parse_pricing("<html><div>pricing moved</div></html>")


def test_too_few_rows_raises():
    from modelman.ollama_catalog import CatalogParseError, parse_pricing

    html = "<table>" + _HEAD + _row("a", "$1", "$1", "$1") + "</table>"
    with pytest.raises(CatalogParseError, match="expected at least 5"):
        parse_pricing(html)


def test_orphan_offpeak_row_raises():
    from modelman.ollama_catalog import CatalogParseError, parse_pricing

    with pytest.raises(CatalogParseError, match="no base row"):
        parse_pricing(_page(_row("ghost (Off-Peak)", "$1", "$1", "$1")))


def test_duplicate_base_row_raises():
    from modelman.ollama_catalog import CatalogParseError, parse_pricing

    with pytest.raises(CatalogParseError, match="duplicate"):
        parse_pricing(_page(_row("m0", "$1", "$1", "$1")))


def test_unknown_cell_text_warns_and_is_none():
    from modelman.ollama_catalog import parse_pricing

    catalog = parse_pricing(_page(_row("zz", "Free", "$0.10", "$2.00")))
    zz = next(m for m in catalog.models if m.name == "zz")
    assert zz.prices.input is None
    assert any("zz" in w and "Free" in w for w in catalog.warnings)


def test_majority_unrecognized_prices_raises():
    from modelman.ollama_catalog import CatalogParseError, parse_pricing

    rows = "".join(_row(f"m{i}", "USD 1", "USD 1", "USD 1") for i in range(6))
    html = "<table>" + _HEAD + rows + "</table>"
    with pytest.raises(CatalogParseError, match="unrecognized"):
        parse_pricing(html)


def test_fetch_wraps_errors():
    from modelman.ollama_catalog import CatalogFetchError, fetch_pricing_html

    def boom(url, **kw):
        raise OSError("offline")

    with pytest.raises(CatalogFetchError, match="offline"):
        fetch_pricing_html(runner=boom)


def test_fetch_returns_text():
    from modelman.ollama_catalog import PRICING_URL, fetch_pricing_html

    class Resp:
        text = "<html/>"

        def raise_for_status(self):
            return None

    seen = []

    def runner(url, **kw):
        seen.append(url)
        return Resp()

    assert fetch_pricing_html(runner=runner) == "<html/>"
    assert seen == [PRICING_URL]


def test_save_failed_html(tmp_path):
    from modelman.ollama_catalog import save_failed_html

    path = save_failed_html("<x/>", directory=tmp_path)
    assert path.parent == tmp_path
    assert path.name.startswith("ollama-pricing-") and path.suffix == ".html"
    assert path.read_text() == "<x/>"


def test_offpeak_time_price_window():
    from datetime import UTC, datetime

    from modelman.ollama_catalog import PriceTriple, offpeak_time_price

    tp = offpeak_time_price(PriceTriple(0.66, 0.022, 1.98))
    assert tp.label == "off-peak" and tp.timezone == "UTC"
    assert tp.matches(datetime(2026, 9, 28, 11, 0, tzinfo=UTC))  # Mon 11:00
    assert not tp.matches(datetime(2026, 9, 28, 12, 0, tzinfo=UTC))
    assert tp.matches(datetime(2026, 10, 4, 15, 0, tzinfo=UTC))  # Sunday
```

Run: `uv run pytest tests/test_ollama_catalog.py -q`
Expected: FAIL — `ModuleNotFoundError: No module named 'modelman.ollama_catalog'`

- [ ] **Step 3: Implement the fetch/parse half of `ollama_catalog.py`**

Create `modelman/src/modelman/ollama_catalog.py`:

```python
"""Sync modelman's ollama cloud entries with https://ollama.com/pricing.

`parse_pricing` is the ONLY code that knows the page's HTML shape. When
Ollama changes the page, `modelman ollama-catalog sync` exits 3 and saves
the raw HTML; update `parse_pricing` (and tests/fixtures/
ollama_pricing.html) — nothing else should need to change.
See docs/superpowers/specs/2026-09-28-ollama-catalog-sync-design.md.
"""

from __future__ import annotations

import re
import subprocess
import tempfile
from dataclasses import dataclass, field
from datetime import datetime
from html.parser import HTMLParser
from pathlib import Path
from typing import Any

import requests

from .time_pricing import TimePrice, Window

PRICING_URL = "https://ollama.com/pricing"
OFFPEAK_LABEL = "off-peak"
MIN_ROWS = 5

_WEEKDAYS = ["mon", "tue", "wed", "thu", "fri"]
_OFFPEAK_SUFFIX = re.compile(r"\s*\(\s*off[\s-]*peak\s*\)\s*$", re.IGNORECASE)
_PRICE = re.compile(r"^\$\s*(\d+(?:\.\d+)?)$")
_NO_PRICE = {"", "-", "—", "–"}


class CatalogFetchError(Exception):
    """The pricing page could not be downloaded."""


class CatalogParseError(Exception):
    """The pricing page no longer has the shape parse_pricing expects."""

    def __init__(self, check: str) -> None:
        super().__init__(check)
        self.check = check


@dataclass(frozen=True)
class PriceTriple:
    input: float | None
    cache: float | None
    output: float | None


@dataclass
class CatalogModel:
    name: str
    prices: PriceTriple
    offpeak: PriceTriple | None = None


@dataclass
class Catalog:
    models: list[CatalogModel]
    warnings: list[str] = field(default_factory=list)


def offpeak_time_price(p: PriceTriple) -> TimePrice:
    """Ollama's published off-peak window: outside 12:00-18:00 UTC on
    weekdays, and all day on weekends."""
    return TimePrice(
        label=OFFPEAK_LABEL,
        timezone="UTC",
        input_price_per_million=p.input,
        cache_price_per_million=p.cache,
        output_price_per_million=p.output,
        windows=[
            Window(days=list(_WEEKDAYS), start="00:00", end="12:00"),
            Window(days=list(_WEEKDAYS), start="18:00", end="24:00"),
            Window(days=["sat", "sun"], start="00:00", end="24:00"),
        ],
    )


def _default_http_runner(url: str, **kwargs: Any) -> requests.Response:
    return requests.get(url, timeout=30, **kwargs)


def fetch_pricing_html(runner: Any = None) -> str:
    try:
        response = (runner or _default_http_runner)(PRICING_URL)
        response.raise_for_status()
        return str(response.text)
    except Exception as exc:  # noqa: BLE001 — any fetch failure is exit 2
        raise CatalogFetchError(str(exc)) from exc


def save_failed_html(html: str, directory: Path | None = None) -> Path:
    stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    path = Path(directory or tempfile.gettempdir()) / f"ollama-pricing-{stamp}.html"
    path.write_text(html)
    return path


class _TableCollector(HTMLParser):
    """Every <table> as a list of rows; each row a list of cell texts."""

    def __init__(self) -> None:
        super().__init__(convert_charrefs=True)
        self.tables: list[list[list[str]]] = []
        self._open: list[int] = []
        self._row: list[str] | None = None
        self._cell: list[str] | None = None

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        if tag == "table":
            self.tables.append([])
            self._open.append(len(self.tables) - 1)
        elif tag == "tr" and self._open:
            self._row = []
        elif tag in ("td", "th") and self._row is not None:
            self._cell = []

    def handle_endtag(self, tag: str) -> None:
        if tag in ("td", "th") and self._cell is not None and self._row is not None:
            self._row.append(" ".join("".join(self._cell).split()))
            self._cell = None
        elif tag == "tr" and self._row is not None and self._open:
            if self._row:
                self.tables[self._open[-1]].append(self._row)
            self._row = None
        elif tag == "table" and self._open:
            self._open.pop()

    def handle_data(self, data: str) -> None:
        if self._cell is not None:
            self._cell.append(data)


def _map_columns(header: list[str]) -> dict[str, int] | None:
    lower = [h.lower() for h in header]

    def find(pred: Any) -> int | None:
        return next((i for i, h in enumerate(lower) if pred(h)), None)

    cols = {
        "model": find(lambda h: "model" in h),
        "cache": find(lambda h: "cache" in h),
        "input": find(lambda h: "input" in h and "cache" not in h),
        "output": find(lambda h: "output" in h),
    }
    if any(v is None for v in cols.values()):
        return None
    return {k: v for k, v in cols.items() if v is not None}


def parse_pricing(html: str) -> Catalog:
    collector = _TableCollector()
    collector.feed(html)
    if not collector.tables:
        raise CatalogParseError("no <table> found on the page")

    found = None
    for table in collector.tables:
        if table and (cols := _map_columns(table[0])) is not None:
            found = (cols, table[1:])
            break
    if found is None:
        headers = [t[0] for t in collector.tables if t]
        raise CatalogParseError(
            f"no table has Model/Input/Cached/Output headers (found headers: {headers})"
        )
    cols, rows = found
    if len(rows) < MIN_ROWS:
        raise CatalogParseError(f"found {len(rows)} model rows, expected at least {MIN_ROWS}")

    warnings: list[str] = []
    cells_seen = 0
    unrecognized = 0

    def price(cell: str, where: str) -> float | None:
        nonlocal cells_seen, unrecognized
        cells_seen += 1
        text = cell.strip().replace(",", "")
        if text in _NO_PRICE:
            return None
        if (m := _PRICE.match(text)) is not None:
            return float(m.group(1))
        unrecognized += 1
        warnings.append(f"{where}: unrecognized price {cell!r}; treated as unknown")
        return None

    width = max(cols.values()) + 1
    base: dict[str, PriceTriple] = {}
    offpeak: dict[str, PriceTriple] = {}
    order: list[str] = []
    for n, row in enumerate(rows, start=1):
        if len(row) < width:
            raise CatalogParseError(f"row {n} has {len(row)} cells, expected at least {width}")
        raw_name = row[cols["model"]]
        name = _OFFPEAK_SUFFIX.sub("", raw_name).strip()
        if not name:
            raise CatalogParseError(f"row {n} has an empty model name")
        triple = PriceTriple(
            price(row[cols["input"]], f"{name} input"),
            price(row[cols["cache"]], f"{name} cached input"),
            price(row[cols["output"]], f"{name} output"),
        )
        target = offpeak if name != raw_name.strip() else base
        if name in target:
            raise CatalogParseError(f"duplicate row for {raw_name!r}")
        target[name] = triple
        if target is base:
            order.append(name)

    if cells_seen and unrecognized * 2 > cells_seen:
        raise CatalogParseError(
            f"{unrecognized} of {cells_seen} price cells unrecognized — price format changed?"
        )
    orphans = sorted(set(offpeak) - set(base))
    if orphans:
        raise CatalogParseError(f"off-peak rows with no base row: {orphans}")
    models = [CatalogModel(name, base[name], offpeak.get(name)) for name in order]
    return Catalog(models=models, warnings=warnings)
```

(`subprocess` is imported now for Task 4's helpers. If ruff flags it as
unused at this task's `make check`, add it in Task 4 instead.)

- [ ] **Step 4: Guard the new default runner in `conftest.py`**

In `_never_call_real_ollama` (`tests/conftest.py`), append:

```python
    def _no_network(url, **kwargs):
        raise RuntimeError("tests must not fetch ollama.com; pass runner=")

    monkeypatch.setattr("modelman.ollama_catalog._default_http_runner", _no_network)
```

- [ ] **Step 5: Run tests and checks**

Run: `uv run pytest tests/test_ollama_catalog.py -q && make check`
Expected: PASS; checks clean.

- [ ] **Step 6: Commit**

```bash
make format
git add src/modelman/ollama_catalog.py tests/test_ollama_catalog.py \
  tests/fixtures/ollama_pricing.html tests/conftest.py
git commit -m "feat(modelman): header-keyed ollama.com/pricing parser

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Sync plan + apply

**Files:**
- Modify: `modelman/src/modelman/ollama_catalog.py`
- Modify: `modelman/tests/test_ollama_catalog.py`
- Modify: `modelman/tests/conftest.py`

**Interfaces:**
- Consumes: `Catalog`, `CatalogModel`, `PriceTriple`, `offpeak_time_price`, `OFFPEAK_LABEL` (Task 3); `Cost.time_prices` (Task 1).
- Produces:
  - `CATALOG_NAME_KEY = "catalog_name"`
  - `cloud_tag(name: str) -> str`
  - `is_cloud_tag(tag: str) -> bool`
  - `PriceUpdate(model_id: str, catalog_name: str, before: Cost | None, after: Cost)`
  - `DeleteCandidate(tag: str, model_id: str | None)`
  - `SyncPlan(catalog_size: int, updates: list[PriceUpdate], unchanged: list[str], additions: list[ModelEntry], delete_candidates: list[DeleteCandidate], unlisted: list[str], warnings: list[str])` with `.has_registry_changes() -> bool`
  - `plan_sync(registry: Registry, catalog: Catalog, ollama_tags: list[str] | None) -> SyncPlan`
  - `apply_sync(registry: Registry, plan: SyncPlan) -> None`
  - `format_plan(plan: SyncPlan) -> str`
  - `list_ollama_tags(runner=None) -> list[str] | None`
  - `remove_ollama_tag(tag: str, runner=None) -> None` (raises `RuntimeError`)

- [ ] **Step 1: Write failing plan tests**

Append to `modelman/tests/test_ollama_catalog.py`:

```python
def _registry(*models):
    from modelman.registry import AuthConfig, ProviderEntry, Registry

    return Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))],
        models=list(models),
    )


def _entry(tag, *, family="fam", location="cloud", cost=None, extra=None):
    from modelman.registry import ModelEntry

    return ModelEntry(
        id=f"ollama/{tag}",
        family=family,
        provider_id="ollama",
        model_name=tag,
        location=location,
        source="curated",
        cost=cost,
        extra=dict(extra or {}),
    )


def _catalog(*models):
    from modelman.ollama_catalog import Catalog

    return Catalog(models=list(models))


def _cm(name, inp=1.0, cache=0.1, out=2.0, offpeak=None):
    from modelman.ollama_catalog import CatalogModel, PriceTriple

    return CatalogModel(name, PriceTriple(inp, cache, out), offpeak)


def test_cloud_tag_derivation():
    from modelman.ollama_catalog import cloud_tag, is_cloud_tag

    assert cloud_tag("glm-5.3") == "glm-5.3:cloud"
    assert cloud_tag("gpt-oss:120b") == "gpt-oss:120b-cloud"
    assert is_cloud_tag("glm-5.3:cloud") and is_cloud_tag("gpt-oss:120b-cloud")
    assert not is_cloud_tag("gpt-oss:20b")


def test_plan_updates_existing_and_records_catalog_name():
    from modelman.ollama_catalog import plan_sync
    from modelman.registry import Cost

    old = Cost(input_price_per_million=9.0, subscription_price=100.0, subscription_period="month")
    reg = _registry(_entry("glm-5.3:cloud", cost=old))
    plan = plan_sync(reg, _catalog(_cm("glm-5.3", 1.4, 0.26, 4.4)), [])
    (u,) = plan.updates
    assert u.model_id == "ollama/glm-5.3:cloud"
    assert u.after.input_price_per_million == 1.4
    assert u.after.subscription_price == 100.0  # untouched
    assert plan.additions == []


def test_plan_unchanged_when_prices_match_and_name_recorded():
    from modelman.ollama_catalog import plan_sync
    from modelman.registry import Cost

    cost = Cost(input_price_per_million=1.0, cache_price_per_million=0.1, output_price_per_million=2.0)
    reg = _registry(_entry("glm-5.3:cloud", cost=cost, extra={"catalog_name": "glm-5.3"}))
    plan = plan_sync(reg, _catalog(_cm("glm-5.3")), [])
    assert plan.updates == []
    assert plan.unchanged == ["ollama/glm-5.3:cloud"]
    assert not plan.has_registry_changes()


def test_plan_offpeak_set_replace_remove_keeps_other_rows():
    from modelman.ollama_catalog import PriceTriple, plan_sync
    from modelman.registry import Cost
    from modelman.time_pricing import TimePrice, Window

    custom = TimePrice(
        label="mine", timezone="UTC", windows=[Window(days=["sun"], start="00:00", end="01:00")]
    )
    stale_offpeak = TimePrice(
        label="off-peak", timezone="UTC", windows=[Window(days=["sat"], start="00:00", end="24:00")]
    )
    cost = Cost(input_price_per_million=1.0, time_prices=[custom, stale_offpeak])
    reg = _registry(_entry("a:cloud", cost=cost), _entry("b:cloud", cost=cost))
    plan = plan_sync(
        reg,
        _catalog(_cm("a", offpeak=PriceTriple(0.5, None, 1.0)), _cm("b")),
        [],
    )
    after = {u.model_id: u.after for u in plan.updates}
    a_rows = after["ollama/a:cloud"].time_prices
    assert [tp.label for tp in a_rows] == ["mine", "off-peak"]
    assert a_rows[1].input_price_per_million == 0.5
    assert len(a_rows[1].windows) == 3
    assert [tp.label for tp in after["ollama/b:cloud"].time_prices] == ["mine"]


def test_plan_matches_by_catalog_name_after_rename():
    from modelman.ollama_catalog import plan_sync

    reg = _registry(_entry("glm-5.3-renamed:cloud", extra={"catalog_name": "glm-5.3"}))
    plan = plan_sync(reg, _catalog(_cm("glm-5.3")), ["glm-5.3-renamed:cloud"])
    assert [u.model_id for u in plan.updates] == ["ollama/glm-5.3-renamed:cloud"]
    assert plan.additions == []
    assert plan.delete_candidates == []


def test_plan_additions_family_and_subscription():
    from modelman.ollama_catalog import plan_sync
    from modelman.registry import Cost

    sub = Cost(subscription_price=100.0, subscription_period="month")
    reg = _registry(
        _entry("gpt-oss:20b", family="gpt-oss", location="local"),
        _entry("glm-5.2:cloud", family="glm", cost=sub),
    )
    plan = plan_sync(reg, _catalog(_cm("glm-5.2"), _cm("gpt-oss:120b"), _cm("kimi-k3")), [])
    added = {e.id: e for e in plan.additions}
    gpt = added["ollama/gpt-oss:120b-cloud"]
    assert gpt.family == "gpt-oss"  # reused from the local namesake's family
    assert gpt.location == "cloud" and gpt.source == "curated"
    assert gpt.extra == {"catalog_name": "gpt-oss:120b"}
    assert gpt.cost is not None and gpt.cost.subscription_price == 100.0
    assert added["ollama/kimi-k3:cloud"].family == "kimi-k3"


def test_plan_subscription_disagreement_warns():
    from modelman.ollama_catalog import plan_sync
    from modelman.registry import Cost

    reg = _registry(
        _entry("a:cloud", cost=Cost(subscription_price=100.0, subscription_period="month")),
        _entry("b:cloud", cost=Cost(subscription_price=20.0, subscription_period="month")),
    )
    plan = plan_sync(reg, _catalog(_cm("a"), _cm("b"), _cm("new")), [])
    (new,) = plan.additions
    assert new.cost is not None and new.cost.subscription_price is None
    assert any("subscription" in w for w in plan.warnings)


def test_plan_does_not_touch_local_namesake():
    from modelman.ollama_catalog import plan_sync

    local = _entry("gpt-oss:20b", family="gpt-oss", location="local")
    reg = _registry(local)
    plan = plan_sync(reg, _catalog(_cm("gpt-oss:20b")), ["gpt-oss:20b"])
    assert plan.updates == []
    assert [e.id for e in plan.additions] == ["ollama/gpt-oss:20b-cloud"]
    assert plan.delete_candidates == []
    assert plan.unlisted == []


def test_plan_delete_candidates_only_pulled_cloud_stubs():
    from modelman.ollama_catalog import DeleteCandidate, plan_sync

    reg = _registry(_entry("old:cloud"), _entry("gone:cloud"), _entry("keep:cloud"))
    tags = ["old:cloud", "stray-cloud-only:cloud", "ornith-1.5:35b", "keep:cloud"]
    plan = plan_sync(reg, _catalog(_cm("keep")), tags)
    assert plan.delete_candidates == [
        DeleteCandidate(tag="old:cloud", model_id="ollama/old:cloud"),
        DeleteCandidate(tag="stray-cloud-only:cloud", model_id=None),
    ]
    assert plan.unlisted == ["ollama/gone:cloud"]


def test_plan_without_ollama_list():
    from modelman.ollama_catalog import plan_sync

    reg = _registry(_entry("old:cloud"), _entry("keep:cloud"))
    plan = plan_sync(reg, _catalog(_cm("keep")), None)
    assert plan.delete_candidates == []
    assert plan.unlisted == ["ollama/old:cloud"]
    assert [u.model_id for u in plan.updates] == ["ollama/keep:cloud"]


def test_plan_id_collision_with_non_ollama_entry_warns():
    from modelman.ollama_catalog import plan_sync
    from modelman.registry import ModelEntry

    squatter = ModelEntry(id="ollama/x:cloud", family="f", provider_id="other", model_name="zzz")
    plan = plan_sync(_registry(squatter), _catalog(_cm("x")), [])
    assert plan.additions == []
    assert any("ollama/x:cloud" in w for w in plan.warnings)


def test_apply_sync_mutates_registry():
    from modelman.ollama_catalog import apply_sync, plan_sync

    reg = _registry(_entry("a:cloud"))
    plan = plan_sync(reg, _catalog(_cm("a", 3.0), _cm("b")), [])
    apply_sync(reg, plan)
    a = reg.model("ollama/a:cloud")
    assert a.cost is not None and a.cost.input_price_per_million == 3.0
    assert a.extra["catalog_name"] == "a"
    assert a.pricing_updated_at is not None
    assert reg.model("ollama/b:cloud").pricing_updated_at is not None


def test_format_plan_mentions_every_section():
    from modelman.ollama_catalog import format_plan, plan_sync

    reg = _registry(_entry("a:cloud"), _entry("gone:cloud"), _entry("old:cloud"))
    plan = plan_sync(reg, _catalog(_cm("a"), _cm("b")), ["old:cloud"])
    text = format_plan(plan)
    for needle in ("ollama/a:cloud", "ollama/b:cloud", "old:cloud", "ollama/gone:cloud"):
        assert needle in text


def test_list_ollama_tags_parses_and_handles_failure():
    import subprocess

    from modelman.ollama_catalog import list_ollama_tags

    out = "NAME                 ID      SIZE  MODIFIED\nglm-5.3:cloud  abc  -  2 days ago\nornith-1.5:35b  def  20 GB  1 week ago\n"

    def ok(args, **kw):
        return subprocess.CompletedProcess(args, 0, stdout=out, stderr="")

    def down(args, **kw):
        return subprocess.CompletedProcess(args, 1, stdout="", stderr="could not connect")

    def missing(args, **kw):
        raise FileNotFoundError("ollama")

    assert list_ollama_tags(runner=ok) == ["glm-5.3:cloud", "ornith-1.5:35b"]
    assert list_ollama_tags(runner=down) is None
    assert list_ollama_tags(runner=missing) is None


def test_remove_ollama_tag():
    import subprocess

    from modelman.ollama_catalog import remove_ollama_tag

    calls = []

    def ok(args, **kw):
        calls.append(args)
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

    remove_ollama_tag("x:cloud", runner=ok)
    assert calls == [["ollama", "rm", "x:cloud"]]

    def fail(args, **kw):
        return subprocess.CompletedProcess(args, 1, stdout="", stderr="nope")

    with pytest.raises(RuntimeError, match="x:cloud"):
        remove_ollama_tag("x:cloud", runner=fail)
```

Run: `uv run pytest tests/test_ollama_catalog.py -q`
Expected: the new tests FAIL with `ImportError` (e.g. `cannot import name 'plan_sync'`).

- [ ] **Step 2: Implement the plan/apply half**

Append to `ollama_catalog.py`. Add imports at the top:
`from dataclasses import replace` (merge into the existing dataclasses
import) and
`from .registry import Cost, ModelEntry, Registry, _cost_to_dict`.

```python
CATALOG_NAME_KEY = "catalog_name"
_OLLAMA = "ollama"


def cloud_tag(name: str) -> str:
    """Page name -> pulled ollama tag (`glm-5.3` -> `glm-5.3:cloud`,
    `gpt-oss:120b` -> `gpt-oss:120b-cloud`)."""
    return f"{name}-cloud" if ":" in name else f"{name}:cloud"


def is_cloud_tag(tag: str) -> bool:
    return tag.endswith(":cloud") or tag.endswith("-cloud")


@dataclass
class PriceUpdate:
    model_id: str
    catalog_name: str
    before: Cost | None
    after: Cost


@dataclass(frozen=True)
class DeleteCandidate:
    tag: str
    model_id: str | None  # registry id when the pulled stub is registered


@dataclass
class SyncPlan:
    catalog_size: int
    updates: list[PriceUpdate] = field(default_factory=list)
    unchanged: list[str] = field(default_factory=list)
    additions: list[ModelEntry] = field(default_factory=list)
    delete_candidates: list[DeleteCandidate] = field(default_factory=list)
    unlisted: list[str] = field(default_factory=list)
    warnings: list[str] = field(default_factory=list)

    def has_registry_changes(self) -> bool:
        return bool(self.updates or self.additions)


def _is_ollama_cloud(m: ModelEntry) -> bool:
    return m.provider_id == _OLLAMA and (m.location == "cloud" or is_cloud_tag(m.model_name))


def _find_entry(registry: Registry, name: str) -> ModelEntry | None:
    for m in registry.models:
        if m.provider_id == _OLLAMA and m.extra.get(CATALOG_NAME_KEY) == name:
            return m
    tag = cloud_tag(name)
    return next(
        (m for m in registry.models if m.provider_id == _OLLAMA and m.model_name == tag), None
    )


def _with_catalog_prices(existing: Cost | None, cm: CatalogModel) -> Cost:
    base = existing if existing is not None else Cost()
    rows = [tp for tp in base.time_prices if tp.label != OFFPEAK_LABEL]
    if cm.offpeak is not None:
        rows.append(offpeak_time_price(cm.offpeak))
    return replace(
        base,
        input_price_per_million=cm.prices.input,
        cache_price_per_million=cm.prices.cache,
        output_price_per_million=cm.prices.output,
        time_prices=rows,
        extra=dict(base.extra),
    )


def _family_for(registry: Registry, name: str) -> str:
    stem = name.split(":", 1)[0]
    for m in registry.models:
        if m.provider_id == _OLLAMA and m.model_name.split(":", 1)[0] == stem:
            return m.family
    return stem


def _shared_subscription(registry: Registry, warnings: list[str]) -> tuple[float | None, str | None]:
    subs = {
        (m.cost.subscription_price, m.cost.subscription_period)
        if m.cost is not None
        else (None, None)
        for m in registry.models
        if _is_ollama_cloud(m)
    }
    if len(subs) == 1:
        return next(iter(subs))
    if len(subs) > 1:
        warnings.append(
            "ollama cloud entries disagree on subscription pricing; new entries get none"
        )
    return (None, None)


def plan_sync(registry: Registry, catalog: Catalog, ollama_tags: list[str] | None) -> SyncPlan:
    """Pure: what a sync would change. ``ollama_tags`` is `ollama list`'s
    NAME column, or None when it could not be read (no delete check)."""
    plan = SyncPlan(catalog_size=len(catalog.models), warnings=list(catalog.warnings))
    matched: set[str] = set()
    now_listed_tags: set[str] = set()
    sub_price, sub_period = _shared_subscription(registry, plan.warnings)
    ids = {m.id for m in registry.models}

    for cm in catalog.models:
        entry = _find_entry(registry, cm.name)
        if entry is not None:
            matched.add(entry.id)
            now_listed_tags.add(entry.model_name)
            after = _with_catalog_prices(entry.cost, cm)
            before_d = _cost_to_dict(entry.cost) if entry.cost is not None else None
            if before_d == _cost_to_dict(after) and entry.extra.get(CATALOG_NAME_KEY) == cm.name:
                plan.unchanged.append(entry.id)
            else:
                plan.updates.append(PriceUpdate(entry.id, cm.name, entry.cost, after))
            continue
        tag = cloud_tag(cm.name)
        now_listed_tags.add(tag)
        new_id = f"{_OLLAMA}/{tag}"
        if new_id in ids:
            plan.warnings.append(f"{new_id} already exists on another provider; not adding")
            continue
        cost = _with_catalog_prices(
            Cost(subscription_price=sub_price, subscription_period=sub_period), cm
        )
        plan.additions.append(
            ModelEntry(
                id=new_id,
                family=_family_for(registry, cm.name),
                provider_id=_OLLAMA,
                model_name=tag,
                location="cloud",
                source="curated",
                cost=cost,
                extra={CATALOG_NAME_KEY: cm.name},
            )
        )

    by_tag = {m.model_name: m for m in registry.models if m.provider_id == _OLLAMA}
    pulled = set(ollama_tags or [])
    if ollama_tags is not None:
        for tag in ollama_tags:
            if is_cloud_tag(tag) and tag not in now_listed_tags:
                owner = by_tag.get(tag)
                plan.delete_candidates.append(DeleteCandidate(tag, owner.id if owner else None))
    for m in registry.models:
        if _is_ollama_cloud(m) and m.id not in matched and m.model_name not in pulled:
            plan.unlisted.append(m.id)
    return plan


def apply_sync(registry: Registry, plan: SyncPlan) -> None:
    """Apply ``plan`` (computed against this same registry) in place."""
    now = datetime.now().astimezone().replace(microsecond=0).isoformat()
    for update in plan.updates:
        entry = registry.model(update.model_id)
        entry.cost = update.after
        entry.extra[CATALOG_NAME_KEY] = update.catalog_name
        entry.pricing_updated_at = now
    for addition in plan.additions:
        addition.pricing_updated_at = now
        registry.models.append(addition)


def _fmt(cost: Cost | None) -> str:
    if cost is None:
        return "no cost"

    def p(v: float | None) -> str:
        return "-" if v is None else f"{v:g}"

    text = (
        f"{p(cost.input_price_per_million)}/{p(cost.cache_price_per_million)}/"
        f"{p(cost.output_price_per_million)}"
    )
    offpeak = next((tp for tp in cost.time_prices if tp.label == OFFPEAK_LABEL), None)
    if offpeak is not None:
        text += (
            f" (off-peak {p(offpeak.input_price_per_million)}/"
            f"{p(offpeak.cache_price_per_million)}/{p(offpeak.output_price_per_million)})"
        )
    return text


def format_plan(plan: SyncPlan) -> str:
    lines = [
        f"ollama.com/pricing: {plan.catalog_size} models "
        "(prices are input/cached/output per million tokens)"
    ]
    lines.append(f"Updates ({len(plan.updates)}):")
    lines += [f"  {u.model_id}: {_fmt(u.before)} -> {_fmt(u.after)}" for u in plan.updates]
    lines.append(f"Additions ({len(plan.additions)}):")
    lines += [f"  {e.id} [family {e.family}]: {_fmt(e.cost)}" for e in plan.additions]
    lines.append(f"Unchanged: {len(plan.unchanged)}")
    lines.append(f"Pulled but no longer listed — will ask to delete ({len(plan.delete_candidates)}):")
    lines += [
        f"  {c.tag}" + (f" ({c.model_id})" if c.model_id else " (not in registry)")
        for c in plan.delete_candidates
    ]
    lines.append(f"Registry entries no longer on ollama.com/pricing — left as-is ({len(plan.unlisted)}):")
    lines += [f"  {mid}" for mid in plan.unlisted]
    lines += [f"warning: {w}" for w in plan.warnings]
    return "\n".join(lines)


def _default_ollama_runner(args: list[str], **kwargs: Any) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, **kwargs)  # noqa: S603 — fixed argv


def list_ollama_tags(runner: Any = None) -> list[str] | None:
    """`ollama list` NAME column, or None if it can't be read."""
    try:
        r = (runner or _default_ollama_runner)(
            ["ollama", "list"], capture_output=True, text=True, timeout=30
        )
    except (OSError, subprocess.TimeoutExpired):
        return None
    if r.returncode != 0:
        return None
    tags = []
    for line in r.stdout.splitlines():
        line = line.strip()
        if line and not line.startswith("NAME"):
            tags.append(line.split()[0])
    return tags


def remove_ollama_tag(tag: str, runner: Any = None) -> None:
    r = (runner or _default_ollama_runner)(
        ["ollama", "rm", tag], capture_output=True, text=True, timeout=60
    )
    if r.returncode != 0:
        raise RuntimeError(f"`ollama rm {tag}` failed (exit {r.returncode}): {r.stderr.strip()}")
```

Notes for the implementer:
- `_cost_to_dict` is private to `registry.py` but is already imported by
  `screens/forms.py`, so importing it here follows existing practice.
- Check whether `registry.py` imports anything that imports
  `ollama_catalog`; nothing should, so there's no cycle.
- If ruff doesn't know the `S603` rule id (the rule set may not include
  bandit), drop the `noqa`.

- [ ] **Step 3: Guard the ollama runner in `conftest.py`**

In `_never_call_real_ollama`, append:

```python
    monkeypatch.setattr("modelman.ollama_catalog._default_ollama_runner", _fake_ollama_runner)
```

- [ ] **Step 4: Run tests and checks**

Run: `uv run pytest tests/test_ollama_catalog.py -q && make check`
Expected: PASS; checks clean.

- [ ] **Step 5: Commit**

```bash
make format
git add src/modelman/ollama_catalog.py tests/test_ollama_catalog.py tests/conftest.py
git commit -m "feat(modelman): plan/apply ollama catalog sync

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: `modelman ollama-catalog sync` CLI

**Files:**
- Create: `modelman/src/modelman/ollama_catalog_cli.py`
- Modify: `modelman/src/modelman/main.py` (imports + `app.add_typer` block ~L53-61)
- Test: `modelman/tests/commands/test_ollama_catalog.py` (new)

**Interfaces:**
- Consumes (Tasks 3–4):
  - `fetch_pricing_html`
  - `parse_pricing`
  - `save_failed_html`
  - `CatalogFetchError`
  - `CatalogParseError`
  - `list_ollama_tags`
  - `plan_sync`
  - `apply_sync`
  - `format_plan`
  - `remove_ollama_tag`
  - `DeleteCandidate`
- Consumes (existing):
  - `registry.load_registry`
  - `registry.locked_registry`
  - `registry.model_entry_to_variant`
  - `queue.QueuedOps(deletes=...)`
  - `main.run_queued_ops(queued) -> bool` (True = failures)
- Produces: `ollama_catalog_cli.ollama_catalog_app` (Typer) with command `sync(--dry-run, --html PATH, --yes)`.

- [ ] **Step 1: Write failing CLI tests**

Create `modelman/tests/commands/test_ollama_catalog.py`:

```python
"""Tests for `modelman ollama-catalog sync`."""

from __future__ import annotations

from pathlib import Path

import pytest
from typer.testing import CliRunner

FIXTURE = Path(__file__).resolve().parents[1] / "fixtures" / "ollama_pricing.html"


@pytest.fixture
def seeded(tmp_path, monkeypatch):
    from modelman.registry import (
        AuthConfig,
        Cost,
        ModelEntry,
        ProviderEntry,
        Registry,
        save_registry,
    )

    reg_path = tmp_path / "registry.toml"
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))
    # tempfile caches gettempdir() process-wide, so setting TMPDIR is not enough.
    monkeypatch.setattr("tempfile.tempdir", str(tmp_path))
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))],
        models=[
            ModelEntry(
                id="ollama/deepseek-v4-pro:cloud",
                family="deepseek",
                provider_id="ollama",
                model_name="deepseek-v4-pro:cloud",
                location="cloud",
                source="curated",
                cost=Cost(
                    input_price_per_million=9.0,
                    subscription_price=100.0,
                    subscription_period="month",
                ),
            ),
            ModelEntry(
                id="ollama/retired:cloud",
                family="retired",
                provider_id="ollama",
                model_name="retired:cloud",
                location="cloud",
                source="curated",
            ),
        ],
    )
    save_registry(registry, reg_path)
    return reg_path


def _tags(monkeypatch, tags):
    monkeypatch.setattr("modelman.ollama_catalog_cli.list_ollama_tags", lambda: tags)


def test_dry_run_writes_nothing(seeded, monkeypatch):
    from modelman.main import app

    _tags(monkeypatch, [])
    before = seeded.read_text()
    result = CliRunner().invoke(app, ["ollama-catalog", "sync", "--dry-run", "--html", str(FIXTURE)])
    assert result.exit_code == 0, result.output
    assert "ollama/deepseek-v4-pro:cloud" in result.output
    assert seeded.read_text() == before


def test_sync_applies_updates_and_additions(seeded, monkeypatch):
    from modelman.main import app
    from modelman.registry import load_registry

    _tags(monkeypatch, [])
    result = CliRunner().invoke(
        app, ["ollama-catalog", "sync", "--yes", "--html", str(FIXTURE)]
    )
    assert result.exit_code == 0, result.output
    reg = load_registry(seeded)
    pro = reg.model("ollama/deepseek-v4-pro:cloud")
    assert pro.cost is not None and pro.cost.input_price_per_million == 1.32
    assert pro.cost.subscription_price == 100.0
    assert pro.cost.time_prices[0].input_price_per_million == 0.66
    added = reg.model("ollama/gpt-oss:120b-cloud")
    assert added.location == "cloud" and added.extra["catalog_name"] == "gpt-oss:120b"
    assert reg.model("ollama/retired:cloud")  # report-only, untouched
    assert "retired:cloud" in result.output


def test_sync_confirm_no_leaves_registry(seeded, monkeypatch):
    from modelman.main import app

    _tags(monkeypatch, [])
    before = seeded.read_text()
    result = CliRunner().invoke(
        app, ["ollama-catalog", "sync", "--html", str(FIXTURE)], input="n\n"
    )
    assert result.exit_code == 0, result.output
    assert seeded.read_text() == before


def test_sync_applies_against_fresh_registry(seeded, monkeypatch):
    """A model added to registry.toml after the plan was printed (e.g. by
    the TUI's price-refresh worker) must survive the apply."""
    from modelman.main import app
    from modelman.registry import ModelEntry, load_registry, save_registry

    _tags(monkeypatch, [])

    def confirm_and_race(*args, **kwargs):
        reg = load_registry(seeded)
        reg.models.append(
            ModelEntry(id="ollama/late:7b", family="late", provider_id="ollama", model_name="late:7b")
        )
        save_registry(reg, seeded)
        return True

    monkeypatch.setattr("modelman.ollama_catalog_cli.typer.confirm", confirm_and_race)
    result = CliRunner().invoke(app, ["ollama-catalog", "sync", "--html", str(FIXTURE)])
    assert result.exit_code == 0, result.output
    reg = load_registry(seeded)
    assert reg.model("ollama/late:7b")
    assert reg.model("ollama/gpt-oss:120b-cloud")


def test_parse_failure_exits_3_and_saves_html(seeded, monkeypatch, tmp_path):
    from modelman.main import app

    _tags(monkeypatch, [])
    bad = tmp_path / "bad.html"
    bad.write_text("<html><p>moved</p></html>")
    before = seeded.read_text()
    result = CliRunner().invoke(app, ["ollama-catalog", "sync", "--html", str(bad)])
    assert result.exit_code == 3
    assert "no <table>" in result.output
    saved = list(tmp_path.glob("ollama-pricing-*.html"))
    assert len(saved) == 1 and str(saved[0]) in result.output
    assert seeded.read_text() == before


def test_fetch_failure_exits_2(seeded, monkeypatch):
    from modelman.main import app
    from modelman.ollama_catalog import CatalogFetchError

    def boom():
        raise CatalogFetchError("offline")

    monkeypatch.setattr("modelman.ollama_catalog_cli.fetch_pricing_html", boom)
    result = CliRunner().invoke(app, ["ollama-catalog", "sync"])
    assert result.exit_code == 2
    assert "offline" in result.output


def test_sync_ollama_down_still_updates(seeded, monkeypatch):
    from modelman.main import app
    from modelman.registry import load_registry

    _tags(monkeypatch, None)
    result = CliRunner().invoke(app, ["ollama-catalog", "sync", "--yes", "--html", str(FIXTURE)])
    assert result.exit_code == 0, result.output
    assert "ollama list" in result.output  # the skip warning
    reg = load_registry(seeded)
    assert reg.model("ollama/deepseek-v4-pro:cloud").cost.input_price_per_million == 1.32


def test_yes_never_confirms_deletes(seeded, monkeypatch):
    from modelman.main import app

    _tags(monkeypatch, ["retired:cloud", "stray:cloud"])
    queued, removed = [], []
    monkeypatch.setattr("modelman.main.run_queued_ops", lambda q: queued.append(q) or False)
    monkeypatch.setattr("modelman.ollama_catalog_cli.remove_ollama_tag", removed.append)
    result = CliRunner().invoke(
        app, ["ollama-catalog", "sync", "--yes", "--html", str(FIXTURE)], input="n\nn\n"
    )
    assert result.exit_code == 0, result.output
    assert queued == [] and removed == []


def test_delete_registered_and_unregistered(seeded, monkeypatch):
    from modelman.main import app

    _tags(monkeypatch, ["retired:cloud", "stray:cloud"])
    queued, removed = [], []
    monkeypatch.setattr("modelman.main.run_queued_ops", lambda q: queued.append(q) or False)
    monkeypatch.setattr("modelman.ollama_catalog_cli.remove_ollama_tag", removed.append)
    result = CliRunner().invoke(
        app, ["ollama-catalog", "sync", "--yes", "--html", str(FIXTURE)], input="y\ny\n"
    )
    assert result.exit_code == 0, result.output
    assert list(queued[0].deletes) == ["ollama/retired:cloud"]
    assert queued[0].deletes["ollama/retired:cloud"]["name"] == "retired:cloud"
    assert removed == ["stray:cloud"]


def test_delete_failure_exits_1(seeded, monkeypatch):
    from modelman.main import app

    _tags(monkeypatch, ["stray:cloud"])

    def fail(tag):
        raise RuntimeError("`ollama rm stray:cloud` failed")

    monkeypatch.setattr("modelman.ollama_catalog_cli.remove_ollama_tag", fail)
    result = CliRunner().invoke(
        app, ["ollama-catalog", "sync", "--yes", "--html", str(FIXTURE)], input="y\n"
    )
    assert result.exit_code == 1
    assert "failed" in result.output
```

Notes:
- `result.output` merges stderr in the repo's Click version. If
  `test_refresh_prices.py` constructs `CliRunner(mix_stderr=False)`,
  match its style and assert on `result.stderr` where the message goes to
  stderr.
- `modelman.main.run_queued_ops` is patched on the module because the CLI
  imports it function-locally (see Step 2).

Run: `uv run pytest tests/commands/test_ollama_catalog.py -q`
Expected: FAIL — `No such command 'ollama-catalog'` (exit code 2 on every invoke).

- [ ] **Step 2: Implement the CLI**

Create `modelman/src/modelman/ollama_catalog_cli.py`:

```python
"""`modelman ollama-catalog` — sync ollama cloud entries with ollama.com/pricing.

Exit codes: 0 ok, 1 save/delete failure, 2 page fetch failed, 3 page
shape changed (raw HTML saved; fix ollama_catalog.parse_pricing).
"""

from __future__ import annotations

from pathlib import Path

import typer

from .ollama_catalog import (
    CatalogFetchError,
    CatalogParseError,
    DeleteCandidate,
    apply_sync,
    fetch_pricing_html,
    format_plan,
    list_ollama_tags,
    parse_pricing,
    plan_sync,
    remove_ollama_tag,
    save_failed_html,
)
from .queue import QueuedOps
from .registry import load_registry, locked_registry, model_entry_to_variant

ollama_catalog_app = typer.Typer(
    help="Sync ollama cloud models and prices from ollama.com/pricing.",
    no_args_is_help=True,
)


def _delete(candidate: DeleteCandidate) -> bool:
    """Delete one pulled cloud stub. Returns True on failure."""
    if candidate.model_id is None:
        try:
            remove_ollama_tag(candidate.tag)
        except RuntimeError as exc:
            typer.echo(f"error: {exc}", err=True)
            return True
        typer.echo(f"Removed {candidate.tag}.")
        return False
    from .main import run_queued_ops  # function-local: main imports this module

    entry = load_registry().model(candidate.model_id)
    return run_queued_ops(QueuedOps(deletes={entry.id: model_entry_to_variant(entry)}))


@ollama_catalog_app.command("sync")
def sync(
    dry_run: bool = typer.Option(False, "--dry-run", help="Print the plan; change nothing."),
    html: Path | None = typer.Option(
        None, "--html", help="Parse a saved pricing page instead of fetching it."
    ),
    yes: bool = typer.Option(
        False, "--yes", help="Apply registry changes without confirming (never deletes)."
    ),
) -> None:
    """Add/update ollama cloud entries (incl. off-peak prices) and offer to
    delete pulled cloud stubs that ollama.com/pricing no longer lists."""
    if html is not None:
        try:
            text = html.read_text()
        except OSError as exc:
            typer.echo(f"error: cannot read {html}: {exc}", err=True)
            raise typer.Exit(2) from exc
    else:
        try:
            text = fetch_pricing_html()
        except CatalogFetchError as exc:
            typer.echo(f"error: could not fetch ollama.com/pricing: {exc}", err=True)
            raise typer.Exit(2) from exc

    try:
        catalog = parse_pricing(text)
    except CatalogParseError as exc:
        saved = save_failed_html(text)
        typer.echo(f"error: could not parse ollama.com/pricing: {exc.check}", err=True)
        typer.echo(f"raw HTML saved to {saved} — update ollama_catalog.parse_pricing", err=True)
        raise typer.Exit(3) from exc

    tags = list_ollama_tags()
    if tags is None:
        typer.echo("warning: could not run `ollama list`; skipping the delete check", err=True)

    plan = plan_sync(load_registry(), catalog, tags)
    typer.echo(format_plan(plan))
    if dry_run:
        return

    failed = False
    if plan.has_registry_changes() and (
        yes or typer.confirm("Apply these registry changes?", default=True)
    ):
        try:
            with locked_registry() as fresh:
                fresh_plan = plan_sync(fresh, catalog, tags)
                apply_sync(fresh, fresh_plan)
        except OSError as exc:
            typer.echo(f"error: failed to save registry: {exc}", err=True)
            raise typer.Exit(1) from exc
        typer.echo(
            f"Updated {len(fresh_plan.updates)} and added {len(fresh_plan.additions)} model(s)."
        )

    for candidate in plan.delete_candidates:
        prompt = f"{candidate.tag} is pulled but no longer on ollama.com/pricing. Delete it?"
        if typer.confirm(prompt, default=False):
            failed = _delete(candidate) or failed

    if failed:
        raise typer.Exit(1)
```

In `main.py`, add
`from .ollama_catalog_cli import ollama_catalog_app` next to the other
sub-app imports, and add
`app.add_typer(ollama_catalog_app, name="ollama-catalog")` beside the
existing `app.add_typer(...)` lines.

- [ ] **Step 3: Run tests and checks**

Run: `uv run pytest tests/commands/test_ollama_catalog.py tests/test_ollama_catalog.py -q && make check`
Expected: PASS; checks clean.

- [ ] **Step 4: Smoke-test offline against the fixture**

Run: `MODELMAN_REGISTRY=$(mktemp -d)/registry.toml sh -c 'cp ~/.config/local-ai/registry.toml "$MODELMAN_REGISTRY" && uv run modelman ollama-catalog sync --dry-run --html tests/fixtures/ollama_pricing.html'`
Expected: the plan lists updates to the existing `ollama/*:cloud`
entries, additions such as `ollama/gpt-oss:120b-cloud`, and exit 0. The
real registry is untouched, because this runs on a temp copy.

- [ ] **Step 5: Commit**

```bash
make format
git add src/modelman/ollama_catalog_cli.py src/modelman/main.py tests/commands/test_ollama_catalog.py
git commit -m "feat(modelman): modelman ollama-catalog sync command

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Skill, docs, full verification

**Files:**
- Create: `modelman/.claude/skills/ollama-catalog/SKILL.md`
- Modify: `modelman/README.md` (cost schema section ~L79-101)
- Modify: `modelman/CLAUDE.md` (subcommand table; architecture bullets)
- Modify: `CLAUDE.md` (root — Commands list)

**Interfaces:**
- Consumes: the `modelman ollama-catalog sync` CLI and its exit codes (Task 5); the `parse_pricing` and fixture location (Task 3).

- [ ] **Step 1: Write the skill**

Create `modelman/.claude/skills/ollama-catalog/SKILL.md`:

````markdown
---
name: ollama-catalog
description: Sync modelman's ollama cloud models and prices (including off-peak) from https://ollama.com/pricing, and offer to delete pulled cloud models Ollama no longer lists. Use when asked to update/refresh ollama cloud models or ollama pricing, or when the ollama pricing scrape breaks.
---

# Ollama catalog sync

Keeps `registry.toml`'s ollama cloud entries in line with
<https://ollama.com/pricing> via `modelman ollama-catalog sync`.

- **Off-peak prices.** These are stored as a `[[models.cost.time_prices]]`
  row labelled `off-peak`: UTC, weekdays outside 12:00–18:00, and all day
  on weekends.
- **Real local models are never touched.** `ornith-1.5:35b`, `*-mlx` and
  similar are left alone. Only pulled cloud stubs (`*:cloud` / `*-cloud`)
  can be offered for deletion.
- **Entries missing from the page are only reported.** Registry entries
  no longer on the page, and not pulled, are listed but never changed.

Run everything from `modelman/`.

## Steps

1. Snapshot the guide drift surface (root CLAUDE.md rule):
   `git -C .. grep -n "exposed = " docs/guides/ > /tmp/exposed-before.txt`
2. Dry run: `uv run modelman ollama-catalog sync --dry-run`
   - Summarize the plan for the user: updates (old → new prices), additions
     (id + family), delete candidates, and report-only entries.
   - Point out any `warning:` lines, e.g. a subscription disagreement or an
     unrecognized price cell.
   - Ask whether any added model's family should be changed. If so, edit
     `family` in registry.toml after the sync.
3. Go through each delete candidate with the user, one at a time, before
   running for real. Deleting a registered model also removes its registry
   entry and unexposes it.
4. Real run: `uv run modelman ollama-catalog sync`
   - Answer the registry-change confirm.
   - Answer each delete prompt exactly as the user decided; the default is
     no. Do not pass `--yes` unless the user asked, and note that `--yes`
     never answers the delete prompts.
5. Re-run the grep from step 1 and `diff` against the snapshot. The sync
   doesn't change `exposed`, but deletes of exposed models do, so report
   any drift in the six guides.
6. New entries are not pulled or exposed. To use one, run
   `uv run modelman expose ollama/<tag>`, or start it from wt.

## Exit codes

| Code | Meaning | Do |
|---|---|---|
| 0 | done | — |
| 1 | a delete or the registry save failed | read the error and retry the failed piece |
| 2 | page fetch failed | check network, retry; or pass `--html <saved page>` |
| 3 | page shape changed | follow "Repairing the parser" |

## Repairing the parser (exit 3)

The error names the failed check and a saved file
(`$TMPDIR/ollama-pricing-<timestamp>.html`).

1. Open the saved HTML and find the pricing table/markup.
2. Update **only** `src/modelman/ollama_catalog.py::parse_pricing` (and
   `_TableCollector`/`_map_columns` if the structure moved). Keep the
   sanity checks: header-keyed columns, ≥ `MIN_ROWS` rows, off-peak rows
   must have a base row, and most price cells must parse.
3. Replace `tests/fixtures/ollama_pricing.html` with the saved page, update
   `test_parse_fixture_happy_path`'s expected counts/prices, and run
   `uv run pytest tests/test_ollama_catalog.py -q && make check`.
4. Retry with `--html <saved page> --dry-run`, then run the steps above.

If the page's off-peak **window** wording changes (it's not parsed), update
`offpeak_time_price()` and its test to match the new window.
````

- [ ] **Step 2: Document the schema and command**

In `modelman/README.md`, directly after the paragraph that ends at
~L101 ("…leave cost unset (the TUI shows `—`)."), add:

````markdown
Optional time-windowed prices override the flat (default) per-token
prices during their windows — e.g. Ollama's off-peak pricing, written by
`modelman ollama-catalog sync`:

```toml
[[models.cost.time_prices]]
label    = "off-peak"          # informational
timezone = "UTC"               # IANA name
input_price_per_million  = 0.66
cache_price_per_million  = 0.022
output_price_per_million = 1.98
windows = [                    # union; start inclusive, end exclusive, end may be 24:00
  { days = ["mon","tue","wed","thu","fri"], start = "00:00", end = "12:00" },
  { days = ["mon","tue","wed","thu","fri"], start = "18:00", end = "24:00" },
  { days = ["sat","sun"],                   start = "00:00", end = "24:00" },
]
```

At an instant, the first row with a matching window supplies each price it
sets; omitted fields and non-matching times use the flat prices
(`time_pricing.price_at`). Stored only today — the TUI COST column and wt
show the flat prices.
````

In `modelman/CLAUDE.md`, add a row to the subcommand table:

```markdown
| `ollama-catalog sync [--dry-run] [--html F] [--yes]` | Sync ollama cloud entries + prices (incl. off-peak `time_prices`) from ollama.com/pricing; prompts per pulled cloud stub no longer listed (default no). Exit 2 fetch / 3 page-shape change (HTML saved). Driven by the `ollama-catalog` skill |
```

and, under "Registry and state", add these two bullets:

```markdown
- `src/modelman/time_pricing.py` — `Window`/`TimePrice` (`Cost.time_prices`, `[[models.cost.time_prices]]`): time-windowed overrides of the flat per-token prices, first-match-wins per field, reference resolver `price_at()`. Stored only (no TUI/wt reader yet); `_merge_api_cost` and the TUI edit (`screens/models.py::_carry_over_unedited`) must carry them through.
- `src/modelman/ollama_catalog.py` + `ollama_catalog_cli.py` — `modelman ollama-catalog sync`. `parse_pricing` is the only code that knows ollama.com/pricing's HTML (header-keyed; raises `CatalogParseError` rather than writing partial data); `plan_sync` is pure (page name `X` → tag `X:cloud`/`X-cloud`, matched entries get `catalog_name` in model extra); deletes of registered stubs reuse `main.run_queued_ops`. Tests use `tests/fixtures/ollama_pricing.html`; conftest guards its HTTP/ollama default runners.
```

In root `CLAUDE.md`'s Commands list, add after the `modelman provider stop` line:

```markdown
- `uv run --directory modelman modelman ollama-catalog sync [--dry-run]` — sync ollama cloud models + prices (incl. off-peak) from ollama.com/pricing; see the `ollama-catalog` skill in `modelman/.claude/skills/`
```

- [ ] **Step 3: Full verification**

Run (from repo root): `make test-all`
Expected: lint, check-links, modelman `make check` + `make test`, and wt
`go build`/`vet`/`test` all PASS. If `check-links` flags a new link, fix
it.

- [ ] **Step 4: Commit**

```bash
git add modelman/.claude/skills/ollama-catalog/SKILL.md modelman/README.md modelman/CLAUDE.md CLAUDE.md
git commit -m "docs(modelman): ollama-catalog skill + time_prices schema docs

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

- [ ] **Step 5: First real run (with the user)**

Invoke the `ollama-catalog` skill and follow it against the live page and
the real registry, pausing for the user's decisions on each delete
candidate. This is the acceptance check, not an automated test.
