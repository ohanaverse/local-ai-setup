"""Tests for `modelman refresh-prices`."""

from __future__ import annotations

from pathlib import Path
from typing import Any
from unittest.mock import MagicMock, patch

import pytest
from typer.testing import CliRunner

from modelman.main import app
from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry, save_registry


class _FakeResponse:
    def __init__(self, payload: dict[str, Any] | None = None, exc: Exception | None = None):
        self._payload = payload
        self._exc = exc

    def raise_for_status(self) -> None:
        if self._exc:
            raise self._exc

    def json(self) -> dict[str, Any]:
        assert self._payload is not None
        return self._payload


def _runner(payload: dict[str, Any] | None = None, exc: Exception | None = None):
    def _run(_url: str, **_kwargs: Any) -> _FakeResponse:
        return _FakeResponse(payload=payload, exc=exc)
    return _run


def _seed_registry(tmp_path: Path, monkeypatch):
    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    registry = Registry(
        providers=[
            ProviderEntry(
                id="openrouter", name="OpenRouter", location="cloud", auth=AuthConfig(type="api_key")
            )
        ],
        models=[
            ModelEntry(
                id="openrouter/gpt-4o",
                family="x",
                provider_id="openrouter",
                model_name="openai/gpt-4o",
                location="cloud",
            )
        ],
    )
    save_registry(registry, registry_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    return registry_path, state_path


def test_refresh_prices_updates_registry_and_reports(tmp_path, monkeypatch):
    _seed_registry(tmp_path, monkeypatch)
    payload = {
        "data": [
            {"id": "openai/gpt-4o", "pricing": {"prompt": "0.0000025", "completion": "0.00001"}}
        ]
    }
    with patch("modelman.pricing._default_runner", side_effect=_runner(payload)):
        result = CliRunner().invoke(app, ["refresh-prices"])

    assert result.exit_code == 0
    assert "Refreshed prices for 1 model" in result.stdout


def test_refresh_prices_reports_api_error_and_exits(tmp_path, monkeypatch):
    _seed_registry(tmp_path, monkeypatch)
    with patch("modelman.pricing._default_runner", side_effect=_runner(exc=RuntimeError("offline"))):
        result = CliRunner().invoke(app, ["refresh-prices"])

    assert result.exit_code == 1
    assert "offline" in result.output
