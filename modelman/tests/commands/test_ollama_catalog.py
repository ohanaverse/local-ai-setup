"""Tests for `modelman ollama-catalog sync`."""

from __future__ import annotations

import re
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


@pytest.fixture
def ops(monkeypatch):
    """Records the pull/rm work instead of running it."""
    rec = {"queued": [], "removed": [], "queue_fails": False}
    monkeypatch.setattr(
        "modelman.main.run_queued_ops",
        lambda q: rec["queued"].append(q) or rec["queue_fails"],
    )
    monkeypatch.setattr("modelman.ollama_catalog_cli.remove_ollama_tag", rec["removed"].append)
    return rec


def _tags(monkeypatch, tags):
    monkeypatch.setattr("modelman.ollama_catalog_cli.list_ollama_tags", lambda: tags)
    # Tag resolution hits ollama.com/library; assume the canonical guess.
    from modelman.ollama_catalog import cloud_tag

    monkeypatch.setattr(
        "modelman.ollama_catalog_cli.resolve_cloud_tags",
        lambda names, known=None: ({n: cloud_tag(n) for n in names}, []),
    )


def _sync_yes(*extra):
    """`sync --yes` the way the skill runs it: dry run first, then apply
    under the removal digest it printed."""
    from modelman.main import app

    args = ["ollama-catalog", "sync", "--html", str(FIXTURE), *extra]
    dry = CliRunner().invoke(app, [*args, "--dry-run"])
    m = re.search(r"Removal digest: (\w+)", dry.output)
    approve = ["--approve-removals", m.group(1)] if m else []
    return CliRunner().invoke(app, [*args, "--yes", *approve])


def test_dry_run_writes_nothing(seeded, monkeypatch):
    from modelman.main import app

    _tags(monkeypatch, [])
    before = seeded.read_text()
    result = CliRunner().invoke(
        app, ["ollama-catalog", "sync", "--dry-run", "--html", str(FIXTURE)]
    )
    assert result.exit_code == 0, result.output
    assert "ollama/deepseek-v4-pro:cloud" in result.output
    assert seeded.read_text() == before


def test_sync_applies_updates_and_additions(seeded, monkeypatch, ops):
    from modelman.registry import load_registry

    _tags(monkeypatch, [])
    result = _sync_yes()
    assert result.exit_code == 0, result.output
    reg = load_registry(seeded)
    pro = reg.model("ollama/deepseek-v4-pro:cloud")
    assert pro.cost is not None and pro.cost.input_price_per_million == 1.32
    assert pro.cost.subscription_price == 100.0
    assert pro.cost.time_prices[0].input_price_per_million == 0.66
    added = reg.model("ollama/gpt-oss:120b-cloud")
    assert added.location == "cloud" and added.extra["catalog_name"] == "gpt-oss:120b"


def test_sync_pulls_missing_and_removes_off_page(seeded, monkeypatch, ops):

    _tags(monkeypatch, ["deepseek-v4-pro:cloud", "retired:cloud", "stray:cloud", "qwen3:8b"])
    result = _sync_yes()
    assert result.exit_code == 0, result.output
    (q,) = ops["queued"]
    assert "ollama/gpt-oss:120b-cloud" in q.ready  # added this run, then pulled
    assert "ollama/deepseek-v4-pro:cloud" not in q.ready  # already pulled
    assert all(q.ready.values())
    assert list(q.deletes) == ["ollama/retired:cloud"]
    assert q.deletes["ollama/retired:cloud"]["name"] == "retired:cloud"
    assert ops["removed"] == ["stray:cloud"]


def test_sync_routes_every_page_model(seeded, monkeypatch, ops):
    """LiteLLM mirrors the page too: every page model (re)exposed so its
    row carries current prices; removals are left to queue.py's unexpose
    cascade."""

    _tags(monkeypatch, [])
    result = _sync_yes()
    assert result.exit_code == 0, result.output
    (q,) = ops["queued"]
    assert len(q.exposes) == 17 and all(q.exposes.values())
    assert "ollama/deepseek-v4-pro:cloud" in q.exposes
    assert "ollama/gpt-oss:120b-cloud" in q.exposes  # added this run
    assert "ollama/retired:cloud" not in q.exposes


def test_sync_marks_exposed_removals(seeded, monkeypatch, ops):
    from modelman.main import app
    from modelman.state import ModelState, locked_state

    with locked_state() as state:
        state.set("ollama/retired:cloud", ModelState(exposed=True))
    _tags(monkeypatch, [])
    result = CliRunner().invoke(
        app, ["ollama-catalog", "sync", "--dry-run", "--html", str(FIXTURE)]
    )
    assert result.exit_code == 0, result.output
    assert "ollama/retired:cloud (exposed — will be unexposed)" in result.output


def test_sync_confirm_no_changes_nothing(seeded, monkeypatch, ops):
    from modelman.main import app

    _tags(monkeypatch, ["stray:cloud"])
    before = seeded.read_text()
    result = CliRunner().invoke(
        app, ["ollama-catalog", "sync", "--html", str(FIXTURE)], input="n\n"
    )
    assert result.exit_code == 0, result.output
    assert seeded.read_text() == before
    assert ops["queued"] == [] and ops["removed"] == []


def test_sync_applies_against_fresh_registry(seeded, monkeypatch, ops):
    """A model added to registry.toml after the plan was printed (e.g. by
    the TUI's price-refresh worker) must survive the apply."""
    from modelman.main import app
    from modelman.registry import ModelEntry, load_registry, save_registry

    _tags(monkeypatch, [])

    def confirm_and_race(*args, **kwargs):
        reg = load_registry(seeded)
        reg.models.append(
            ModelEntry(
                id="ollama/late:7b", family="late", provider_id="ollama", model_name="late:7b"
            )
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


def test_sync_ollama_down_exits_2_and_changes_nothing(seeded, monkeypatch, ops):

    _tags(monkeypatch, None)
    before = seeded.read_text()
    result = _sync_yes()
    assert result.exit_code == 2
    assert "ollama list" in result.output
    assert seeded.read_text() == before
    assert ops["queued"] == [] and ops["removed"] == []


def test_stray_rm_failure_exits_1(seeded, monkeypatch, ops):

    _tags(monkeypatch, ["stray:cloud"])

    def fail(tag):
        raise RuntimeError("`ollama rm stray:cloud` failed")

    monkeypatch.setattr("modelman.ollama_catalog_cli.remove_ollama_tag", fail)
    result = _sync_yes()
    assert result.exit_code == 1
    assert "failed" in result.output


def test_queued_ops_failure_exits_1(seeded, monkeypatch, ops):

    _tags(monkeypatch, [])
    ops["queue_fails"] = True
    result = _sync_yes()
    assert result.exit_code == 1


@pytest.fixture
def mostly_retired(seeded):
    from modelman.registry import ModelEntry, load_registry, save_registry

    reg = load_registry(seeded)
    reg.models.append(
        ModelEntry(
            id="ollama/retired2:cloud",
            family="retired",
            provider_id="ollama",
            model_name="retired2:cloud",
            location="cloud",
            source="curated",
        )
    )
    save_registry(reg, seeded)
    return seeded


def test_mass_removal_exits_4_before_writing(mostly_retired, monkeypatch, ops):

    _tags(monkeypatch, [])
    before = mostly_retired.read_text()
    result = _sync_yes()
    assert result.exit_code == 4, result.output
    assert "--force" in result.output
    assert mostly_retired.read_text() == before
    assert ops["queued"] == []


def test_mass_removal_force_applies(mostly_retired, monkeypatch, ops):

    _tags(monkeypatch, [])
    result = _sync_yes("--force")
    assert result.exit_code == 0, result.output
    (q,) = ops["queued"]
    assert sorted(q.deletes) == ["ollama/retired2:cloud", "ollama/retired:cloud"]


def test_no_tag_resolves_exits_2_and_changes_nothing(seeded, monkeypatch, ops):
    """ollama.com/library unreachable: refuse rather than plan every page
    model as unknown."""

    _tags(monkeypatch, [])
    monkeypatch.setattr(
        "modelman.ollama_catalog_cli.resolve_cloud_tags",
        lambda names, known=None: (dict.fromkeys(names), ["x: offline; skipped"]),
    )
    before = seeded.read_text()
    result = _sync_yes()
    assert result.exit_code == 2, result.output
    assert "could not resolve" in result.output
    assert seeded.read_text() == before
    assert ops["queued"] == []


def test_yes_without_approved_digest_refuses_removals(seeded, monkeypatch, ops):
    """--yes alone must not delete what nobody reviewed: retired:cloud is a
    removal, so the run needs the dry run's digest."""
    from modelman.main import app

    _tags(monkeypatch, [])
    before = seeded.read_text()
    for extra in ([], ["--approve-removals", "000000000000"]):
        result = CliRunner().invoke(
            app, ["ollama-catalog", "sync", "--yes", "--html", str(FIXTURE), *extra]
        )
        assert result.exit_code == 5, result.output
        assert "--dry-run" in result.output
    assert seeded.read_text() == before
    assert ops["queued"] == [] and ops["removed"] == []


def test_confirm_defaults_to_no_when_plan_deletes(seeded, monkeypatch, ops):
    from modelman.main import app

    _tags(monkeypatch, [])
    before = seeded.read_text()
    result = CliRunner().invoke(app, ["ollama-catalog", "sync", "--html", str(FIXTURE)], input="\n")
    assert result.exit_code == 0, result.output
    assert seeded.read_text() == before
    assert ops["queued"] == []


def test_replan_with_unreviewed_removal_changes_nothing(seeded, monkeypatch, ops):
    """A cloud entry that lands in registry.toml after the plan was printed
    would be removed by the re-plan without anyone seeing it: refuse."""
    from modelman.main import app
    from modelman.registry import ModelEntry, load_registry, save_registry

    _tags(monkeypatch, [])

    def confirm_and_race(*args, **kwargs):
        reg = load_registry(seeded)
        reg.models.append(
            ModelEntry(
                id="ollama/late:cloud",
                family="late",
                provider_id="ollama",
                model_name="late:cloud",
                location="cloud",
            )
        )
        save_registry(reg, seeded)
        return True

    monkeypatch.setattr("modelman.ollama_catalog_cli.typer.confirm", confirm_and_race)
    result = CliRunner().invoke(app, ["ollama-catalog", "sync", "--html", str(FIXTURE)])
    assert result.exit_code == 5, result.output
    assert "ollama/late:cloud" in result.output
    reg = load_registry(seeded)
    assert reg.model("ollama/late:cloud")
    assert reg.model("ollama/retired:cloud")
    assert ops["queued"] == []


def test_resolve_skips_verified_tags(seeded, monkeypatch, ops):
    """A catalog entry whose tag `ollama list` shows isn't looked up again."""
    from modelman.main import app
    from modelman.registry import load_registry, save_registry

    reg = load_registry(seeded)
    reg.model("ollama/deepseek-v4-pro:cloud").extra["catalog_name"] = "deepseek-v4-pro"
    save_registry(reg, seeded)
    _tags(monkeypatch, ["deepseek-v4-pro:cloud"])
    seen = {}

    def resolve(names, known=None):
        seen.update(known or {})
        return {n: (known or {}).get(n) for n in names}, []

    monkeypatch.setattr("modelman.ollama_catalog_cli.resolve_cloud_tags", resolve)
    result = CliRunner().invoke(
        app, ["ollama-catalog", "sync", "--dry-run", "--html", str(FIXTURE)]
    )
    assert result.exit_code == 0, result.output
    assert seen == {"deepseek-v4-pro": "deepseek-v4-pro:cloud"}
