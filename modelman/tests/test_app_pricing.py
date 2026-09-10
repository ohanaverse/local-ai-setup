"""Tests for the daily-gated startup price refresh worker."""

from datetime import date, timedelta

from modelman.pricing import should_run_price_refresh
from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry
from modelman.state import StateStore, set_price_refresh_last_run


def test_should_run_when_cloud_model_present_and_never_run():
    state = StateStore()
    registry = Registry(
        providers=[
            ProviderEntry(
                id="openrouter", name="OpenRouter", location="cloud", auth=AuthConfig(type="api_key")
            )
        ],
        models=[
            ModelEntry(
                id="openrouter/x",
                family="x",
                provider_id="openrouter",
                model_name="x",
            )
        ],
    )
    assert should_run_price_refresh(state, registry) is True


def test_should_not_run_if_already_run_today():
    state = StateStore()
    set_price_refresh_last_run(state, date.today().isoformat())
    registry = Registry(
        providers=[
            ProviderEntry(
                id="openrouter", name="OpenRouter", location="cloud", auth=AuthConfig(type="api_key")
            )
        ],
        models=[
            ModelEntry(
                id="openrouter/x",
                family="x",
                provider_id="openrouter",
                model_name="x",
            )
        ],
    )
    assert should_run_price_refresh(state, registry) is False


def test_should_not_run_when_no_cloud_models():
    state = StateStore()
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local", auth=AuthConfig(type="none"))],
        models=[ModelEntry(id="ollama/x", family="x", provider_id="ollama", model_name="x")],
    )
    assert should_run_price_refresh(state, registry) is False


def test_should_run_after_date_changes():
    state = StateStore()
    yesterday = (date.today() - timedelta(days=1)).isoformat()
    set_price_refresh_last_run(state, yesterday)
    registry = Registry(
        providers=[
            ProviderEntry(
                id="openrouter", name="OpenRouter", location="cloud", auth=AuthConfig(type="api_key")
            )
        ],
        models=[
            ModelEntry(
                id="openrouter/x",
                family="x",
                provider_id="openrouter",
                model_name="x",
            )
        ],
    )
    assert should_run_price_refresh(state, registry) is True
