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

    assert len(registry.models) == 3
    free_model = registry.model("ollama/contract-fixture:local")
    assert free_model.cost.kind == "free"

    cloud_model = registry.model("openrouter/contract-fixture:cloud")
    assert cloud_model.location == "cloud"
    assert cloud_model.cost.kind == "per_token"
    assert cloud_model.cost.price_per_million_tokens == 1.5
    assert cloud_model.model_info == {"supports_function_calling": True}

    sub_model = registry.model("ollama/contract-fixture:subscription")
    assert sub_model.cost.kind == "subscription"
    assert sub_model.cost.price_per_period == 20.0
    assert sub_model.cost.period == "month"
    assert sub_model.usage_tier == "medium"

    family = registry.family("contract-fixture")
    assert family is not None
    assert family.display_name == "Contract Fixture"
