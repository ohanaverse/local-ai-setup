from modelman.benchmark.runner import discover_targets
from modelman.registry import ModelEntry, ProviderEntry, Registry
from modelman.state import ModelState, StateStore


def test_discover_targets_defaults_to_exposed_local_models():
    """Exposed local models are benchmarked by default.

    Without explicit --model or --family filters, discover_targets should only
    return local models that are currently exposed through LiteLLM. Remote
    providers and unexposed local models must be skipped so the runner does not
    hit endpoints that are not configured.
    """
    registry = Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", location="local"),
            ProviderEntry(id="openrouter", name="OpenRouter", location="remote"),
        ],
        models=[
            ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a"),
            ModelEntry(id="ollama/b", family="f", provider_id="ollama", model_name="b"),
            ModelEntry(id="openrouter/c", family="f", provider_id="openrouter", model_name="c"),
        ],
    )
    state = StateStore()
    state.set("ollama/a", ModelState(litellm_exposed=True))
    state.set("ollama/b", ModelState(litellm_exposed=False))

    targets = discover_targets(registry, state)
    assert [t.model_id for t in targets] == ["ollama/a"]


def test_discover_targets_by_family_overrides_exposed():
    """--family selects every model in that family regardless of expose state.

    When a family is explicitly requested, all local models in that family
    become targets; the LiteLLM-exposed gate is bypassed because the user has
    narrowed the scope intentionally.
    """
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[
            ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a"),
            ModelEntry(id="ollama/b", family="g", provider_id="ollama", model_name="b"),
        ],
    )
    state = StateStore()
    targets = discover_targets(registry, state, family="f")
    assert [t.model_id for t in targets] == ["ollama/a"]


def test_discover_targets_by_model_ids():
    """--model selects specific local models regardless of expose state.

    Explicit model ids should override the default exposed-only filter so a
    user can benchmark a downloaded-but-unexposed model directly.
    """
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    state = StateStore()
    targets = discover_targets(registry, state, model_ids=["ollama/a"])
    assert [t.model_id for t in targets] == ["ollama/a"]


def test_discover_targets_remote_providers_excluded():
    """Remote providers are never benchmark targets.

    Even when explicitly requested by model id, remote/cloud providers lack a
    local isolation path and must be excluded to avoid unsupported direct
    routes.
    """
    registry = Registry(
        providers=[ProviderEntry(id="openrouter", name="OpenRouter", location="remote")],
        models=[
            ModelEntry(id="openrouter/a", family="f", provider_id="openrouter", model_name="a")
        ],
    )
    state = StateStore()
    targets = discover_targets(registry, state, model_ids=["openrouter/a"])
    assert targets == []
