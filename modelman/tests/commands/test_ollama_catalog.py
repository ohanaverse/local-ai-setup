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
    result = CliRunner().invoke(
        app, ["ollama-catalog", "sync", "--dry-run", "--html", str(FIXTURE)]
    )
    assert result.exit_code == 0, result.output
    assert "ollama/deepseek-v4-pro:cloud" in result.output
    assert seeded.read_text() == before


def test_sync_applies_updates_and_additions(seeded, monkeypatch):
    from modelman.main import app
    from modelman.registry import load_registry

    _tags(monkeypatch, [])
    result = CliRunner().invoke(app, ["ollama-catalog", "sync", "--yes", "--html", str(FIXTURE)])
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
