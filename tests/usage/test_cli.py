from __future__ import annotations

from datetime import UTC, datetime, timedelta
from pathlib import Path
from unittest.mock import MagicMock

import yaml
from typer.testing import CliRunner

from modelman.main import app

runner = CliRunner()


# The `usage` subcommand and its `report` action must be registered on the
# main Typer app, confirming main.py's wiring didn't silently drop it.
def test_usage_report_command_exists() -> None:
    result = runner.invoke(app, ["usage", "--help"])
    assert result.exit_code == 0
    assert "report" in result.output


# End-to-end smoke test: with wt/registry/LiteLLM config files on disk and
# PostgresSpendStore mocked out, `modelman usage report` must run and print a
# report. No MODELMAN_LITELLM_DATABASE_URL is set, so this exercises the
# config-file path (load config -> database_url -> model_list).
def test_usage_report_runs_with_mocked_dependencies(monkeypatch, tmp_path: Path) -> None:
    now = datetime.now(UTC)
    recent = (now - timedelta(hours=1)).isoformat()
    usage_jsonl = tmp_path / "usage.jsonl"
    usage_jsonl.write_text(f'{{"model_id":"ollama/a","timestamp":"{recent}"}}\n')
    rotation_state = tmp_path / "rotation.state"
    rotation_state.write_text("ollama/a\n")

    registry_path = tmp_path / "registry.toml"
    registry_path.write_text(
        '[[models]]\nid = "ollama/a"\nfamily = "fam"\nprovider_id = "ollama"\nmodel_name = "a"\ntags = []\n'
    )

    config_path = tmp_path / "config.yaml"
    config_path.write_text(
        yaml.safe_dump({"model_list": [], "general_settings": {"database_url": "fake"}})
    )

    monkeypatch.setenv("MODELMAN_WT_DIR", str(tmp_path))
    monkeypatch.setenv("MODELMAN_LITELLM_CONFIG", str(config_path))
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))

    fake_store = MagicMock()
    fake_store.query.return_value = []
    monkeypatch.setattr("modelman.usage.cli.PostgresSpendStore", lambda dsn: fake_store)

    result = runner.invoke(app, ["usage", "report", "--days", "1"])
    assert result.exit_code == 0, result.output
    assert "Usage Report" in result.output
    assert "ollama/a" in result.output


# Regression test: MODELMAN_LITELLM_DATABASE_URL must let the report run
# without a LiteLLM config file. The env var is the primary database source
# and the config file is only a fallback, so its absence must not hard-fail
# the command (the refactor briefly made config loading unconditional).
def test_usage_report_runs_with_env_var_only(monkeypatch, tmp_path: Path) -> None:
    now = datetime.now(UTC)
    recent = (now - timedelta(hours=1)).isoformat()
    usage_jsonl = tmp_path / "usage.jsonl"
    usage_jsonl.write_text(f'{{"model_id":"ollama/a","timestamp":"{recent}"}}\n')
    rotation_state = tmp_path / "rotation.state"
    rotation_state.write_text("ollama/a\n")

    registry_path = tmp_path / "registry.toml"
    registry_path.write_text(
        '[[models]]\nid = "ollama/a"\nfamily = "fam"\nprovider_id = "ollama"\nmodel_name = "a"\ntags = []\n'
    )

    monkeypatch.setenv("MODELMAN_WT_DIR", str(tmp_path))
    monkeypatch.setenv("MODELMAN_LITELLM_DATABASE_URL", "fake")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    # Point MODELMAN_LITELLM_CONFIG at a path that does not exist — the env
    # var alone must be enough, so the config file is never read.
    monkeypatch.setenv("MODELMAN_LITELLM_CONFIG", str(tmp_path / "missing-config.yaml"))

    fake_store = MagicMock()
    fake_store.query.return_value = []
    monkeypatch.setattr("modelman.usage.cli.PostgresSpendStore", lambda dsn: fake_store)

    result = runner.invoke(app, ["usage", "report", "--days", "1"])
    assert result.exit_code == 0, result.output
    assert "Usage Report" in result.output
    assert "ollama/a" in result.output
