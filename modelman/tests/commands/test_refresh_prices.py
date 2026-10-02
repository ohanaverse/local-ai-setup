"""Tests for `modelman refresh-prices`."""

from __future__ import annotations

from pathlib import Path
from typing import Any
from unittest.mock import patch

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
                id="openrouter",
                name="OpenRouter",
                location="cloud",
                auth=AuthConfig(type="api_key"),
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


def test_refresh_prices_syncs_routes(tmp_path, monkeypatch, wt_calls):
    """#179: new prices only reach LiteLLM through a sync."""
    _seed_registry(tmp_path, monkeypatch)
    payload = {
        "data": [
            {"id": "openai/gpt-4o", "pricing": {"prompt": "0.0000025", "completion": "0.00001"}}
        ]
    }
    with patch("modelman.pricing._default_runner", side_effect=_runner(payload)):
        result = CliRunner().invoke(app, ["refresh-prices"])
    assert result.exit_code == 0
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]]


def test_refresh_prices_reports_api_error_and_exits(tmp_path, monkeypatch):
    _seed_registry(tmp_path, monkeypatch)
    with patch(
        "modelman.pricing._default_runner", side_effect=_runner(exc=RuntimeError("offline"))
    ):
        result = CliRunner().invoke(app, ["refresh-prices"])

    assert result.exit_code == 1
    assert "offline" in result.output


def _last_run(state_path: Path) -> str | None:
    from modelman.state import get_price_refresh_last_run, load_state

    return get_price_refresh_last_run(load_state(state_path))


def test_refresh_prices_records_last_run_on_success(tmp_path, monkeypatch):
    """Issue #151: the CLI must stamp price_refresh_last_run like the TUI's
    background refresh does — wt's stale-pricing notice reads it, so without
    the stamp the notice tells the user to run this command forever."""
    from datetime import date

    _, state_path = _seed_registry(tmp_path, monkeypatch)
    payload = {
        "data": [
            {"id": "openai/gpt-4o", "pricing": {"prompt": "0.0000025", "completion": "0.00001"}}
        ]
    }
    with patch("modelman.pricing._default_runner", side_effect=_runner(payload)):
        result = CliRunner().invoke(app, ["refresh-prices"])

    assert result.exit_code == 0
    assert _last_run(state_path) == date.today().isoformat()


def test_refresh_prices_leaves_last_run_alone_on_api_error(tmp_path, monkeypatch):
    """A failed fetch refreshed nothing, so it must not claim pricing is fresh."""
    _, state_path = _seed_registry(tmp_path, monkeypatch)
    with patch(
        "modelman.pricing._default_runner", side_effect=_runner(exc=RuntimeError("offline"))
    ):
        result = CliRunner().invoke(app, ["refresh-prices"])

    assert result.exit_code == 1
    assert _last_run(state_path) is None


def test_refresh_prices_no_match_leaves_last_run_alone(tmp_path, monkeypatch):
    """A fetch that matched nothing updated nothing, so it must not claim
    pricing is fresh — the TUI's background refresh rule (stamp only when
    updated > 0). Without this gate, a renamed or dropped OpenRouter id
    would stamp the day and wt's stale-pricing notice would stay quiet even
    though no price was ever refreshed."""
    _, state_path = _seed_registry(tmp_path, monkeypatch)
    payload = {
        "data": [
            {"id": "renamed/other", "pricing": {"prompt": "0.0000025", "completion": "0.00001"}}
        ]
    }
    with patch("modelman.pricing._default_runner", side_effect=_runner(payload)):
        result = CliRunner().invoke(app, ["refresh-prices"])

    assert result.exit_code == 0
    assert "No OpenRouter match for openrouter/gpt-4o" in result.output
    assert _last_run(state_path) is None


def test_refresh_prices_ollama_only_registry_is_quiet(tmp_path, monkeypatch):
    """Issue #151: with only ollama cloud models there is nothing to fetch —
    no warnings, no network call. The date is NOT stamped: wt's notice is
    gated by agents.HasOpenRouterPricedModel already, so stamping would have
    no consumer, and it would suppress same-day pricing for an OpenRouter
    model exposed later the same day."""
    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    save_registry(
        Registry(
            providers=[
                ProviderEntry(
                    id="ollama", name="Ollama", location="local", auth=AuthConfig(type="none")
                )
            ],
            models=[
                ModelEntry(
                    id="ollama/glm:cloud",
                    family="x",
                    provider_id="ollama",
                    model_name="glm:cloud",
                    location="cloud",
                )
            ],
        ),
        registry_path,
    )
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    with patch("modelman.pricing._default_runner", side_effect=AssertionError("must not fetch")):
        result = CliRunner().invoke(app, ["refresh-prices"])

    assert result.exit_code == 0
    assert "warning" not in result.output
    assert _last_run(state_path) is None
