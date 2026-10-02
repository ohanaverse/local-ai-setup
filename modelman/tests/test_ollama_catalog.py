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

    cost = Cost(
        input_price_per_million=1.0, cache_price_per_million=0.1, output_price_per_million=2.0
    )
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
    assert plan.removals == [] and plan.stray_tags == []


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
    assert plan.removals == []
    assert plan.stray_tags == []


def test_plan_pulls_page_models_not_in_ollama_list():
    from modelman.ollama_catalog import plan_sync

    reg = _registry(_entry("a:cloud"), _entry("b:cloud"))
    plan = plan_sync(reg, _catalog(_cm("a"), _cm("b"), _cm("c")), ["a:cloud"])
    assert plan.pulls == ["ollama/b:cloud", "ollama/c:cloud"]


def test_plan_pulls_entry_tag_when_matched_by_catalog_name():
    from modelman.ollama_catalog import plan_sync

    renamed = _entry("glm-old:cloud", extra={"catalog_name": "glm-5.3"})
    plan = plan_sync(_registry(renamed), _catalog(_cm("glm-5.3")), ["glm-old:cloud"])
    assert plan.pulls == []
    assert plan.removals == []


def test_plan_removes_off_page_cloud_entries_and_stray_stubs():
    from modelman.ollama_catalog import plan_sync

    reg = _registry(
        _entry("old:cloud"),
        _entry("gone:cloud"),
        _entry("keep:cloud"),
        _entry("ornith-1.5:35b", location="local"),
    )
    tags = ["old:cloud", "stray:cloud", "ornith-1.5:35b", "keep:cloud"]
    plan = plan_sync(reg, _catalog(_cm("keep")), tags)
    # Pulled or not, an off-page cloud entry goes; local models never do.
    assert plan.removals == ["ollama/old:cloud", "ollama/gone:cloud"]
    assert plan.stray_tags == ["stray:cloud"]
    assert plan.pulls == []


def test_plan_mass_removal_guard():
    from modelman.ollama_catalog import plan_sync

    reg = _registry(_entry("a:cloud"), _entry("b:cloud"), _entry("c:cloud"))
    assert not plan_sync(reg, _catalog(_cm("a"), _cm("b")), []).mass_removal()
    assert plan_sync(reg, _catalog(_cm("a")), []).mass_removal()


def test_plan_routes_every_page_model():
    from modelman.ollama_catalog import plan_sync

    reg = _registry(_entry("a:cloud"), _entry("gone:cloud"), _entry("x:7b", location="local"))
    plan = plan_sync(reg, _catalog(_cm("a"), _cm("b")), [])
    assert plan.routes == ["ollama/a:cloud", "ollama/b:cloud"]


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
    plan = plan_sync(reg, _catalog(_cm("a"), _cm("b")), ["old:cloud", "stray:cloud"])
    text = format_plan(plan, exposed={"ollama/gone:cloud"})
    for needle in ("ollama/a:cloud", "ollama/b:cloud", "ollama/old:cloud", "stray:cloud"):
        assert needle in text
    assert "ollama/gone:cloud (exposed — will be unexposed)" in text
    assert "LiteLLM routes — expose or refresh prices (2;" in text
    assert "ollama/old:cloud (exposed" not in text


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


def test_unrecognized_cell_keeps_existing_price():
    """A single column changing format (below the whole-page threshold) must
    not overwrite a known price with None — only a real `-` cell clears it."""
    from modelman.ollama_catalog import parse_pricing, plan_sync
    from modelman.registry import Cost

    html = _page(
        _row("zz", "$1.00", "$0.10 / 1M", "$2.00"),
        _row("yy", "$1.00", "-", "$2.00"),
    )
    catalog = parse_pricing(html)
    old = Cost(
        input_price_per_million=9.0, cache_price_per_million=0.5, output_price_per_million=9.0
    )
    reg = _registry(_entry("zz:cloud", cost=old), _entry("yy:cloud", cost=old))
    after = {u.model_id: u.after for u in plan_sync(reg, catalog, []).updates}
    assert after["ollama/zz:cloud"].cache_price_per_million == 0.5  # kept
    assert after["ollama/zz:cloud"].input_price_per_million == 1.0
    assert after["ollama/yy:cloud"].cache_price_per_million is None  # real "-"


def test_unrecognized_offpeak_cell_keeps_existing_offpeak_price():
    from modelman.ollama_catalog import PriceTriple, offpeak_time_price, parse_pricing, plan_sync
    from modelman.registry import Cost

    html = _page(
        _row("zz", "$1.00", "$0.10", "$2.00"),
        _row("zz (Off-Peak)", "$0.50", "$0.05*", "$1.00"),
    )
    catalog = parse_pricing(html)
    old = Cost(
        input_price_per_million=1.0, time_prices=[offpeak_time_price(PriceTriple(0.4, 0.04, 0.8))]
    )
    reg = _registry(_entry("zz:cloud", cost=old))
    (u,) = plan_sync(reg, catalog, []).updates
    (tp,) = u.after.time_prices
    assert (tp.input_price_per_million, tp.cache_price_per_million) == (0.5, 0.04)


@pytest.mark.parametrize(
    "cell", ["glm-5.3 (Off-peak hours)", "glm-5.3 off-peak", "glm-5.3*", "GLM 5.3"]
)
def test_unrecognized_model_name_raises(cell):
    from modelman.ollama_catalog import CatalogParseError, parse_pricing

    with pytest.raises(CatalogParseError, match="not an ollama tag"):
        parse_pricing(_page(_row(cell, "$1.00", "$0.10", "$2.00")))


def test_plan_catalog_name_match_keeps_canonical_tag_listed():
    from modelman.ollama_catalog import plan_sync

    reg = _registry(_entry("gpt-oss:120b-cloud-custom", extra={"catalog_name": "gpt-oss:120b"}))
    plan = plan_sync(
        reg, _catalog(_cm("gpt-oss:120b")), ["gpt-oss:120b-cloud", "gpt-oss:120b-cloud-custom"]
    )
    assert plan.removals == [] and plan.stray_tags == []


def test_apply_sync_stamps_utc():
    from modelman.ollama_catalog import apply_sync, plan_sync

    reg = _registry(_entry("a:cloud"))
    apply_sync(reg, plan_sync(reg, _catalog(_cm("a", 3.0)), []))
    assert reg.model("ollama/a:cloud").pricing_updated_at.endswith("+00:00")


def test_plan_offpeak_replaced_in_place_and_unchanged_when_same():
    from modelman.ollama_catalog import PriceTriple, offpeak_time_price, plan_sync
    from modelman.registry import Cost
    from modelman.time_pricing import TimePrice, Window

    holiday = TimePrice(
        label="holiday", timezone="UTC", windows=[Window(days=["sun"], start="00:00", end="24:00")]
    )
    page_offpeak = PriceTriple(0.5, 0.05, 1.0)
    cost = Cost(
        input_price_per_million=1.0,
        cache_price_per_million=0.1,
        output_price_per_million=2.0,
        time_prices=[offpeak_time_price(page_offpeak), holiday],
    )
    reg = _registry(_entry("a:cloud", cost=cost, extra={"catalog_name": "a"}))
    same = plan_sync(reg, _catalog(_cm("a", offpeak=page_offpeak)), [])
    assert same.updates == [] and same.unchanged == ["ollama/a:cloud"]

    changed = plan_sync(reg, _catalog(_cm("a", offpeak=PriceTriple(0.4, 0.04, 0.8))), [])
    (u,) = changed.updates
    assert [tp.label for tp in u.after.time_prices] == ["off-peak", "holiday"]
    assert u.after.time_prices[0].input_price_per_million == 0.4


def test_plan_collision_with_ollama_entry_names_model_name():
    from modelman.ollama_catalog import plan_sync
    from modelman.registry import ModelEntry

    old = ModelEntry(
        id="ollama/foo:cloud", family="f", provider_id="ollama", model_name="foo:cloud-old"
    )
    plan = plan_sync(_registry(old), _catalog(_cm("foo")), [])
    assert plan.additions == []
    (w,) = [w for w in plan.warnings if "ollama/foo:cloud" in w]
    assert "foo:cloud-old" in w and "another provider" not in w


def test_plan_never_adds_the_same_id_twice(monkeypatch):
    from modelman import ollama_catalog
    from modelman.ollama_catalog import plan_sync

    monkeypatch.setattr(ollama_catalog, "cloud_tag", lambda name: "same:cloud")
    plan = plan_sync(_registry(), _catalog(_cm("x"), _cm("y")), [])
    assert [e.id for e in plan.additions] == ["ollama/same:cloud"]
    assert any("earlier addition" in w for w in plan.warnings)


def _tags_page(name, *tags):
    links = "".join(f'<a href="/library/{name}:{t}">{name}:{t}</a>' for t in tags)
    return f'<html>{links}<a href="/library/{name}-other:cloud">x</a></html>'


def _http(pages):
    """Fake HTTP runner: url -> html, or an exception for missing pages."""

    class R:
        def __init__(self, text, status):
            self.text, self.status_code = text, status

        def raise_for_status(self):
            if self.status_code >= 400:
                raise RuntimeError(f"HTTP {self.status_code}")

    def run(url, **kw):
        return R(pages[url], 200) if url in pages else R("", 404)

    return run


def test_resolve_cloud_tags_prefers_alias_then_single_sized_tag():
    from modelman.ollama_catalog import resolve_cloud_tags

    base = "https://ollama.com/library/"
    runner = _http(
        {
            base + "glm-5.3/tags": _tags_page("glm-5.3", "cloud", "latest"),
            base + "mistral-large-3/tags": _tags_page("mistral-large-3", "675b-cloud", "latest"),
            base + "two/tags": _tags_page("two", "8b-cloud", "70b-cloud"),
            base + "none/tags": _tags_page("none", "latest", "8b"),
        }
    )
    names = ["glm-5.3", "mistral-large-3", "two", "none", "gone", "gpt-oss:120b"]
    resolved, warnings = resolve_cloud_tags(names, runner=runner)
    assert resolved == {
        "glm-5.3": "glm-5.3:cloud",
        "mistral-large-3": "mistral-large-3:675b-cloud",
        "two": None,
        "none": None,
        "gone": None,
        # A page name that already names a size pins its tag; no lookup.
        "gpt-oss:120b": "gpt-oss:120b-cloud",
    }
    assert len(warnings) == 3 and all("skipped" in w for w in warnings)


def test_plan_uses_resolved_tag_for_additions_and_pulls():
    from modelman.ollama_catalog import plan_sync

    plan = plan_sync(
        _registry(),
        _catalog(_cm("mistral-large-3")),
        [],
        resolved={"mistral-large-3": "mistral-large-3:675b-cloud"},
    )
    (added,) = plan.additions
    assert added.id == "ollama/mistral-large-3:675b-cloud"
    assert added.model_name == "mistral-large-3:675b-cloud"
    assert plan.pulls == plan.routes == ["ollama/mistral-large-3:675b-cloud"]


def test_plan_replaces_entry_whose_tag_does_not_resolve():
    """An entry matched by catalog_name but carrying a tag that doesn't exist
    (an earlier sync guessed `name:cloud`) is removed and re-added under the
    resolved tag."""
    from modelman.ollama_catalog import plan_sync

    wrong = _entry("mistral-large-3:cloud", extra={"catalog_name": "mistral-large-3"})
    plan = plan_sync(
        _registry(wrong),
        _catalog(_cm("mistral-large-3")),
        [],
        resolved={"mistral-large-3": "mistral-large-3:675b-cloud"},
    )
    assert plan.removals == ["ollama/mistral-large-3:cloud"]
    assert [e.id for e in plan.additions] == ["ollama/mistral-large-3:675b-cloud"]
    assert plan.updates == []


def test_plan_unresolved_model_keeps_entry_but_skips_pull_and_add():
    from modelman.ollama_catalog import plan_sync

    reg = _registry(_entry("x:cloud", extra={"catalog_name": "x"}))
    plan = plan_sync(reg, _catalog(_cm("x", 5.0), _cm("y")), [], resolved={"x": None, "y": None})
    assert plan.removals == [] and plan.additions == [] and plan.pulls == []
    # Its price is still known from the page, so the existing entry and route
    # are refreshed.
    assert [u.model_id for u in plan.updates] == ["ollama/x:cloud"]
    assert plan.routes == ["ollama/x:cloud"]
