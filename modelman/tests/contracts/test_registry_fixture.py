from pathlib import Path

from modelman.registry import load_registry

FIXTURE = Path(__file__).resolve().parents[3] / "docs" / "contracts" / "registry.sample.toml"


def test_load_registry_matches_shared_fixture():
    """Guards modelman's registry.toml schema against wt's Go decoder
    (wt/internal/config/registry_fixture_test.go reads the same file). A
    schema change not reflected in both tests fails both CI jobs in the
    same PR instead of drifting silently.
    """
    registry = load_registry(path=FIXTURE)

    assert len(registry.providers) == 2
    ollama = registry.provider("ollama")
    assert ollama.auth.type == "none"
    assert ollama.auth.base_url == "http://localhost:11434"
    openrouter = registry.provider("openrouter")
    assert openrouter.auth.type == "api_key"
    assert openrouter.auth.secret_ref == "OPENROUTER_API_KEY"

    assert len(registry.models) == 2

    free_model = registry.model("ollama/contract-fixture:local")
    assert free_model.cost is None

    cloud_model = registry.model("openrouter/contract-fixture:cloud")
    assert cloud_model.location == "cloud"
    assert cloud_model.model_info == {"supports_function_calling": True}
    assert cloud_model.cost is not None
    assert cloud_model.cost.input_price_per_million == 0.50
    assert cloud_model.cost.cache_price_per_million == 0.25
    assert cloud_model.cost.output_price_per_million == 1.00
    assert cloud_model.cost.subscription_price == 19.99
    assert cloud_model.cost.subscription_period == "month"

    family = registry.family("contract-fixture")
    assert family is not None
    assert family.display_name == "Contract Fixture"
