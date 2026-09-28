"""Sync modelman's ollama cloud entries with https://ollama.com/pricing.

`parse_pricing` is the ONLY code that knows the page's HTML shape. When
Ollama changes the page, `modelman ollama-catalog sync` exits 3 and saves
the raw HTML; update `parse_pricing` (and tests/fixtures/
ollama_pricing.html) — nothing else should need to change.
See docs/superpowers/specs/2026-09-28-ollama-catalog-sync-design.md.
"""

from __future__ import annotations

import re
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
