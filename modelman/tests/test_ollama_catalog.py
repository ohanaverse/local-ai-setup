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
        "<table><thead>"
        + head
        + "</thead><tbody>"
        + "".join(f"<tr><td>$2</td><td>m{i}</td><td>$0.1</td><td>$1</td></tr>" for i in range(5))
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
