from pathlib import Path

from modelman.litellm import is_effectively_exposed
from modelman.registry import load_registry
from modelman.state import ModelState, StateStore

FIXTURE = Path(__file__).resolve().parents[3] / "docs" / "contracts" / "registry.sample.toml"


def test_load_registry_matches_shared_fixture():
    """Guards modelman's registry.toml schema against wt's Go decoder
    (wt/internal/config/registry_fixture_test.go reads the same file). A
    schema change not reflected in both tests fails both CI jobs in the
    same PR instead of drifting silently.
    """
    registry = load_registry(path=FIXTURE)

    assert len(registry.providers) == 5
    ollama = registry.provider("ollama")
    assert ollama.auth.type == "none"
    assert ollama.auth.base_url == "http://localhost:11434"
    openrouter = registry.provider("openrouter")
    assert openrouter.auth.type == "api_key"
    assert openrouter.auth.secret_ref == "OPENROUTER_API_KEY"
    agy = registry.provider("agy")
    assert agy.auth.type == "native"
    # The native provider's location must match what production writers
    # emit (modelman's sync_agent_providers, wt's migrate.go) — a fixture
    # pinned to a shape modelman never writes lets location-keyed logic
    # pass CI while breaking on real registries.
    assert agy.location == "cloud"

    pinned = registry.provider("pinned-cloud")
    assert pinned.auth.type == "api_key"
    assert pinned.location == "cloud"
    assert pinned.auth.secret_ref == "PINNED_CLOUD_API_KEY"

    # The mlx_lm_server provider must decode with the shape modelman's
    # default template writes (auth.type "none", OpenAI-compatible
    # base_url, location "local") — a fixture pinned to a shape modelman
    # never writes lets location-keyed logic pass CI while breaking on
    # real registries.
    mlx = registry.provider("mlx_lm_server")
    assert mlx.auth.type == "none"
    assert mlx.auth.base_url == "http://localhost:8001/v1"
    assert mlx.location == "local"

    assert len(registry.models) == 5

    free_model = registry.model("ollama/contract-fixture:local")
    assert free_model.cost is None
    assert free_model.native is False

    cloud_model = registry.model("openrouter/contract-fixture:cloud")
    assert cloud_model.location == "cloud"
    assert cloud_model.model_info == {"supports_function_calling": True}
    assert cloud_model.cost is not None
    assert cloud_model.cost.input_price_per_million == 0.50
    assert cloud_model.cost.cache_price_per_million == 0.25
    assert cloud_model.cost.output_price_per_million == 1.00
    assert cloud_model.cost.subscription_price == 19.99
    assert cloud_model.cost.subscription_period == "month"
    assert cloud_model.native is False

    native_model = registry.model("agy/contract-fixture:native")
    assert native_model.provider_id == "agy"
    assert native_model.native is True

    inherit_model = registry.model("pinned-cloud/contract-fixture:inherit")
    assert inherit_model.location is None  # inherits provider location
    assert inherit_model.native is False

    # The mlx_lm_server pairing model must decode its target+draft
    # sources (fetch/draft) — the shape modelman writes for speculative
    # decoding. A fixture that drops these lets the provider's download
    # path pass CI while breaking on real registries.
    pair = registry.model("mlx_lm_server/contract-fixture:pair")
    assert pair.provider_id == "mlx_lm_server"
    assert pair.fetch is not None
    assert pair.fetch.repo == "org/contract-fixture-target"
    assert pair.draft is not None
    assert pair.draft.repo == "org/contract-fixture-draft"

    family = registry.family("contract-fixture")
    assert family is not None
    assert family.display_name == "Contract Fixture"


def test_fixture_pins_provider_location_inheritance():
    """Issue #46: a model with no location of its own on a
    location="cloud" provider must be exposed on the Python side exactly
    as wt's ResolveLocation-based IsExposed treats it."""
    registry = load_registry(path=FIXTURE)
    state = StateStore()
    state.set(
        "pinned-cloud/contract-fixture:inherit",
        ModelState(ready=False, litellm_exposed=True),
    )
    model = registry.model("pinned-cloud/contract-fixture:inherit")
    assert is_effectively_exposed(model, state, registry=registry) is True
