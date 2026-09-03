"""registry.toml is the canonical, shared model/provider registry per the
2026-08-27 shared-model-registry design spec. These tests cover the load/
save round trip and the minimal validation that catches a hand-edited file
missing a field code elsewhere assumes exists (id, provider_id, model_name).
"""

from pathlib import Path

import pytest

from modelman.registry import (
    AuthConfig,
    Cost,
    FamilyEntry,
    Fetch,
    ModelEntry,
    ProviderEntry,
    Registry,
    RegistryError,
    _default_registry_path,
    _default_wt_config_path,
    family_display_name,
    known_families,
    load_registry,
    model_has_local_artifact,
    provider_config,
    save_registry,
    sync_agent_providers,
)
from modelman.state import FamilyState, StateStore


def test_default_registry_path_honors_xdg(monkeypatch):
    monkeypatch.delenv("MODELMAN_REGISTRY", raising=False)
    monkeypatch.setenv("XDG_CONFIG_HOME", "/custom/xdg")
    assert _default_registry_path() == Path("/custom/xdg/local-ai/registry.toml")


def test_default_registry_path_modelman_registry_override_wins(monkeypatch):
    monkeypatch.setenv("MODELMAN_REGISTRY", "/custom/registry.toml")
    monkeypatch.setenv("XDG_CONFIG_HOME", "/custom/xdg")
    assert _default_registry_path() == Path("/custom/registry.toml")


def test_load_registry_missing_file_raises(tmp_path):
    with pytest.raises(RegistryError, match="not found"):
        load_registry(tmp_path / "nonexistent.toml")


def test_save_then_load_round_trips_providers_and_models(tmp_path):
    registry = Registry(
        providers=[
            ProviderEntry(
                id="ollama",
                name="Ollama",
                location="local",
                auth=AuthConfig(type="none", base_url="http://localhost:11434"),
            ),
            ProviderEntry(
                id="omlx",
                name="oMLX",
                location="local",
                model_dir="~/.omlx/models",
                auth=AuthConfig(type="none", base_url="http://localhost:8000"),
            ),
        ],
        models=[
            ModelEntry(
                id="ollama/qwen3.8:27b-mlx",
                family="qwen3.8",
                provider_id="ollama",
                model_name="qwen3.8:27b-mlx",
                location="local",
                source="discovered",
                tags=["code", "design"],
                cost=Cost(),
                model_info={"supports_function_calling": True},
            ),
            ModelEntry(
                id="llamacpp/qwen3.8-27b-q4",
                family="qwen3.8",
                provider_id="llamacpp",
                model_name="qwen3.8-27b-q4",
                location="local",
                tags=["code"],
                cost=Cost(),
                fetch=Fetch(
                    repo="unsloth/Qwen3.8-27B-GGUF",
                    files=["Qwen3.8-27B-UD-Q4_K_XL.gguf"],
                ),
            ),
        ],
    )
    path = tmp_path / "registry.toml"

    save_registry(registry, path)
    loaded = load_registry(path)

    assert loaded.provider("omlx").model_dir == "~/.omlx/models"
    assert loaded.model("ollama/qwen3.8:27b-mlx").tags == ["code", "design"]
    assert loaded.model("ollama/qwen3.8:27b-mlx").cost == Cost()
    assert loaded.model("llamacpp/qwen3.8-27b-q4").fetch == Fetch(
        repo="unsloth/Qwen3.8-27B-GGUF", files=["Qwen3.8-27B-UD-Q4_K_XL.gguf"]
    )


def test_load_registry_missing_provider_id_raises(tmp_path):
    path = tmp_path / "registry.toml"
    path.write_text('[[providers]]\nname = "Ollama"\n')
    with pytest.raises(RegistryError, match="missing required `id`"):
        load_registry(path)


def test_load_registry_missing_required_model_field_raises(tmp_path):
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
    )
    with pytest.raises(RegistryError, match="missing required fields"):
        load_registry(path)


def test_load_registry_non_dict_cost_raises(tmp_path):
    """A hand-edited cost value that isn't a TOML table must fail loudly
    with RegistryError rather than a TypeError from probing price keys."""
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
        'cost = "free"\n'
    )
    with pytest.raises(RegistryError, match="cost must be a table"):
        load_registry(path)


def test_load_registry_string_price_raises(tmp_path):
    """Numeric price fields must be numbers; a string price would otherwise
    crash the TUI cost formatter when it tries to format the value."""
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
        '[models.cost]\ninput_price_per_million = "abc"\n'
    )
    with pytest.raises(RegistryError, match="cost `input_price_per_million` must be a number"):
        load_registry(path)


def test_load_registry_non_finite_price_raises(tmp_path):
    """Prices must be finite; inf/nan cannot be rendered sensibly and are
    almost always a hand-editing mistake."""
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
        '[models.cost]\nsubscription_price = inf\nsubscription_period = "month"\n'
    )
    with pytest.raises(RegistryError, match="cost `subscription_price` must be finite"):
        load_registry(path)


@pytest.mark.parametrize(
    "field",
    [
        "input_price_per_million",
        "cache_price_per_million",
        "output_price_per_million",
        "subscription_price",
    ],
)
def test_load_registry_negative_price_raises(tmp_path, field):
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\n'
        'model_name = "x"\n'
        f'[models.cost]\n{field} = -1.0\n'
    )
    with pytest.raises(RegistryError, match=f"cost `{field}` must be non-negative"):
        load_registry(path)


def test_load_registry_invalid_subscription_period_raises(tmp_path):
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
        '[models.cost]\nsubscription_price = 20.0\nsubscription_period = "quarter"\n'
    )
    with pytest.raises(RegistryError, match="cost subscription_period must be one of"):
        load_registry(path)


def test_load_registry_missing_subscription_period_raises(tmp_path):
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
        "[models.cost]\nsubscription_price = 20.0\n"
    )
    with pytest.raises(RegistryError, match="subscription_period.*required"):
        load_registry(path)


def test_cost_direct_construction_rejects_negative_price():
    with pytest.raises(ValueError, match="Cost `input_price_per_million` must be non-negative"):
        Cost(input_price_per_million=-1.0)


def test_cost_direct_construction_rejects_non_finite_price():
    with pytest.raises(ValueError, match="Cost `subscription_price` must be finite"):
        Cost(subscription_price=float("inf"), subscription_period="month")


def test_cost_direct_construction_rejects_invalid_subscription_period():
    with pytest.raises(ValueError, match="Cost subscription_period must be one of"):
        Cost(subscription_price=20.0, subscription_period="quarter")


def test_registry_lookup_helpers_raise_keyerror_for_unknown_id():
    registry = Registry()
    with pytest.raises(KeyError):
        registry.provider("nope")
    with pytest.raises(KeyError):
        registry.model("nope")


def test_family_entry_round_trip(tmp_path):
    # A [[families]] entry with a display name must survive save/load,
    # since the family screen's DISPLAY column reads it back.
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))],
        families=[FamilyEntry(name="deepseek-v4", display_name="DeepSeek V4")],
    )
    path = tmp_path / "registry.toml"
    save_registry(registry, path)
    loaded = load_registry(path)
    assert loaded.family("deepseek-v4") == FamilyEntry(
        name="deepseek-v4", display_name="DeepSeek V4"
    )


def test_load_registry_missing_families_section_returns_empty(tmp_path):
    # A registry without a [[families]] section (every pre-existing file)
    # must load with families == [], not raise.
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n[providers.auth]\ntype = "none"\n'
    )
    assert load_registry(path).families == []


def test_load_registry_missing_family_name_raises(tmp_path):
    # A [[families]] entry without a name is malformed; mirror the
    # provider-id requirement so a hand-edited file fails loudly.
    path = tmp_path / "registry.toml"
    path.write_text('[[families]]\ndisplay_name = "No Name"\n')
    with pytest.raises(RegistryError, match="missing required `name`"):
        load_registry(path)


def test_family_entry_preserves_unknown_keys(tmp_path):
    # Hand-edited fields on a [[families]] entry must survive round-trip.
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[families]]\nname = "deepseek-v4"\ndisplay_name = "DeepSeek V4"\ncustom = "keep"\n'
    )
    registry = load_registry(path)
    save_registry(registry, path)
    assert load_registry(path).family("deepseek-v4").extra == {"custom": "keep"}


def test_family_lookup_returns_none_for_unknown():
    # family() returns None for an unknown name (absence is normal),
    # unlike provider()/model() which raise.
    assert Registry().family("nope") is None


def test_family_lookup_returns_first_match():
    # Duplicate names are tolerated (first match wins), matching the
    # duplicate-model-id tolerance.
    registry = Registry(
        families=[
            FamilyEntry(name="a", display_name="First"),
            FamilyEntry(name="a", display_name="Second"),
        ]
    )
    assert registry.family("a").display_name == "First"


def test_derived_families_returns_sorted_distinct_family_names():
    # The family list drives the TUI's left pane; it must be sorted and
    # deduplicated so a family spanning multiple providers appears once.
    registry = Registry(
        models=[
            ModelEntry(id="ollama/a", family="zeta", provider_id="ollama", model_name="a"),
            ModelEntry(id="ollama/b", family="alpha", provider_id="ollama", model_name="b"),
            ModelEntry(id="llamacpp/c", family="alpha", provider_id="llamacpp", model_name="c"),
        ]
    )
    assert registry.derived_families() == ["alpha", "zeta"]


def test_derived_families_on_empty_registry_returns_empty_list():
    # A fresh registry has no models; derived_families() must return []
    # rather than raise, so the TUI renders an empty family list on first run.
    assert Registry().derived_families() == []


def test_known_families_union_of_derived_entries_and_state():
    # known_families must include families from all three sources so an
    # emptied-by-move family (registry entry only) stays visible.
    registry = Registry(
        families=[FamilyEntry(name="entry-only")],
        models=[ModelEntry(id="ollama/a", family="derived", provider_id="ollama", model_name="a")],
    )
    state = StateStore(families={"legacy": FamilyState()})
    assert known_families(registry, state) == ["derived", "entry-only", "legacy"]


def test_family_display_name_resolution_order():
    # Registry entry wins over legacy state; both fall back to None.
    registry = Registry(families=[FamilyEntry(name="a", display_name="Registry Name")])
    state = StateStore(
        families={
            "a": FamilyState(display_name="Legacy Name"),
            "b": FamilyState(display_name="Only Legacy"),
        }
    )
    assert family_display_name(registry, state, "a") == "Registry Name"
    assert family_display_name(registry, state, "b") == "Only Legacy"
    assert family_display_name(registry, state, "c") is None


def test_family_display_name_ignores_empty_display_name():
    # An empty display_name is treated as unset (falls through to the
    # next source), matching state.family_display_name's truthiness check.
    registry = Registry(families=[FamilyEntry(name="a", display_name="")])
    state = StateStore(families={"a": FamilyState(display_name="Legacy")})
    assert family_display_name(registry, state, "a") == "Legacy"


def test_models_by_family_filters_and_preserves_order():
    # Grouping models by family must keep only that family's models and
    # preserve registry order, since the TUI renders them in that order.
    a = ModelEntry(id="ollama/a", family="alpha", provider_id="ollama", model_name="a")
    z = ModelEntry(id="ollama/z", family="zeta", provider_id="ollama", model_name="z")
    b = ModelEntry(id="llamacpp/b", family="alpha", provider_id="llamacpp", model_name="b")
    registry = Registry(models=[a, z, b])
    assert registry.models_by_family("alpha") == [a, b]


def test_models_by_family_unknown_family_returns_empty_list():
    # Asking for a family with no models must return [] rather than raise,
    # so callers can iterate the result without an existence check.
    assert Registry().models_by_family("nope") == []


def test_provider_config_includes_model_dir_when_set():
    # provider_config adapts a registry ProviderEntry into the kwargs the
    # provider constructor expects; model_dir must be passed through when
    # present or oMLX would download into the wrong directory.
    entry = ProviderEntry(
        id="omlx", name="oMLX", model_dir="~/.omlx/models", auth=AuthConfig(type="none")
    )
    assert provider_config(entry) == {"model_dir": "~/.omlx/models"}


def test_provider_config_omits_model_dir_when_unset():
    # Providers that don't take model_dir (e.g. ollama) must not receive it
    # as a kwarg, or their constructors would raise on an unexpected arg.
    entry = ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))
    assert provider_config(entry) == {}


def test_model_has_local_artifact_true_for_local_model_and_provider():
    """The common case: an unset/local model on an unset/local provider
    (llamacpp, omlx) is reconcile-syncable from the filesystem."""
    model = ModelEntry(id="llamacpp/a", family="f", provider_id="llamacpp", model_name="a")
    provider = ProviderEntry(id="llamacpp", name="llama.cpp", location="local")
    assert model_has_local_artifact(model, provider) is True


def test_model_has_local_artifact_true_when_locations_unset():
    """Legacy entries with no explicit location must default to local,
    matching is_local_location's existing legacy-default semantics."""
    model = ModelEntry(id="omlx/a", family="f", provider_id="omlx", model_name="a")
    provider = ProviderEntry(id="omlx", name="oMLX")
    assert model_has_local_artifact(model, provider) is True


def test_model_has_local_artifact_false_for_ollama_cloud_model():
    """The ollama-cloud case: provider is local, but the model itself is
    tagged location='cloud' (e.g. glm-5.3:cloud) — no local file to
    reconcile against."""
    model = ModelEntry(
        id="ollama/glm:cloud", family="f", provider_id="ollama", model_name="glm:cloud",
        location="cloud",
    )
    provider = ProviderEntry(id="ollama", name="Ollama", location="local")
    assert model_has_local_artifact(model, provider) is False


def test_model_has_local_artifact_false_for_cloud_provider():
    """A model on a cloud provider (openrouter, native agents) has no
    local artifact regardless of the model's own location field."""
    model = ModelEntry(id="openrouter/x", family="f", provider_id="openrouter", model_name="x")
    provider = ProviderEntry(id="openrouter", name="OpenRouter", location="cloud")
    assert model_has_local_artifact(model, provider) is False


def test_model_has_local_artifact_false_when_provider_missing():
    """A model referencing a provider id no longer in the registry has no
    reconciled source either way; treat as not having a local artifact
    rather than guessing."""
    model = ModelEntry(id="ghost/x", family="f", provider_id="ghost", model_name="x")
    assert model_has_local_artifact(model, None) is False


def test_load_registry_falls_back_to_legacy_location(tmp_path, monkeypatch):
    # A user who created a registry before the XDG alignment and has
    # XDG_CONFIG_HOME set should still load their existing ~/.config file
    # rather than getting "Registry file not found".
    home = tmp_path / "home"
    monkeypatch.setenv("HOME", str(home))
    monkeypatch.delenv("MODELMAN_REGISTRY", raising=False)
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    legacy = home / ".config" / "local-ai" / "registry.toml"
    save_registry(
        Registry(
            providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))]
        ),
        legacy,
    )
    loaded = load_registry()
    assert loaded.provider("ollama").id == "ollama"


def test_save_then_load_preserves_unknown_keys(tmp_path):
    # Hand-edited fields that aren't part of the typed schema must survive a
    # save/load round-trip, or a user's custom fields are silently dropped.
    path = tmp_path / "registry.toml"
    path.write_text(
        "[[providers]]\n"
        'id = "ollama"\n'
        'name = "Ollama"\n'
        'custom_field = "keep-me"\n'
        "[providers.auth]\n"
        'type = "none"\n'
        'base_url = "http://localhost:11434"\n'
        "auth_extra = true\n"
        "\n"
        "[[models]]\n"
        'id = "ollama/x"\n'
        'family = "x"\n'
        'provider_id = "ollama"\n'
        'model_name = "x"\n'
        'model_extra = "keep-too"\n'
    )
    registry = load_registry(path)
    save_registry(registry, path)
    loaded = load_registry(path)
    assert loaded.provider("ollama").extra == {"custom_field": "keep-me"}
    assert loaded.provider("ollama").auth.extra == {"auth_extra": True}
    assert loaded.model("ollama/x").extra == {"model_extra": "keep-too"}


def test_sync_agent_providers_adds_missing_agents(tmp_path, monkeypatch):
    wt_config = tmp_path / "config.toml"
    wt_config.write_text(
        "[[agents]]\n"
        'name = "claude"\n'
        'supported_providers = ["claude", "ollama"]\n'
        "\n"
        "[[agents]]\n"
        'name = "codex"\n'
        'supported_providers = ["codex"]\n'
    )
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))]
    )
    added = sync_agent_providers(registry, wt_config_path=wt_config)

    assert added == ["claude", "codex"]
    claude = registry.provider("claude")
    assert claude.auth.type == "native"
    assert claude.location == "cloud"
    assert claude.name == "Claude"
    registry.provider("codex")  # does not raise


def test_sync_agent_providers_is_idempotent(tmp_path):
    wt_config = tmp_path / "config.toml"
    wt_config.write_text('[[agents]]\nname = "claude"\n')
    registry = Registry()

    first = sync_agent_providers(registry, wt_config_path=wt_config)
    second = sync_agent_providers(registry, wt_config_path=wt_config)

    assert first == ["claude"]
    assert second == []
    assert len([p for p in registry.providers if p.id == "claude"]) == 1


def test_sync_agent_providers_tolerates_non_list_agents(tmp_path):
    """A hand-edited config where `agents` is a string or dict rather than
    a list of tables must not crash; it should be treated as empty."""
    wt_config = tmp_path / "config.toml"
    wt_config.write_text('agents = "claude"\n')
    registry = Registry()
    assert sync_agent_providers(registry, wt_config_path=wt_config) == []
    assert registry.providers == []


def test_sync_agent_providers_skips_non_dict_agent_entries(tmp_path):
    """Malformed entries inside the agents list (e.g. bare strings) are
    skipped without crashing."""
    wt_config = tmp_path / "config.toml"
    wt_config.write_text('agents = ["not-a-table"]\n')
    registry = Registry()
    assert sync_agent_providers(registry, wt_config_path=wt_config) == []
    assert registry.providers == []


def test_sync_agent_providers_missing_config_is_a_noop(tmp_path):
    registry = Registry()
    added = sync_agent_providers(registry, wt_config_path=tmp_path / "nonexistent.toml")
    assert added == []
    assert registry.providers == []


def test_default_wt_config_path_honors_override(monkeypatch):
    monkeypatch.setenv("MODELMAN_WT_DIR", "/custom/wt")
    assert _default_wt_config_path() == Path("/custom/wt/config.toml")


def test_default_wt_config_path_defaults_to_home(monkeypatch):
    monkeypatch.delenv("MODELMAN_WT_DIR", raising=False)
    assert _default_wt_config_path() == Path.home() / ".config" / "agent-wt" / "config.toml"


def test_registry_round_trips_flat_cost(tmp_path):
    """A ModelEntry with flat cost fields must survive save/load."""
    path = tmp_path / "registry.toml"
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        models=[
            ModelEntry(
                id="ollama/glm-5.3:cloud",
                family="glm",
                provider_id="ollama",
                model_name="glm-5.3:cloud",
                location="cloud",
                cost=Cost(
                    input_price_per_million=1.0,
                    cache_price_per_million=0.5,
                    output_price_per_million=2.0,
                ),
            ),
        ],
    )
    save_registry(registry, path)
    loaded = load_registry(path)
    m = loaded.model("ollama/glm-5.3:cloud")
    assert m.cost == Cost(
        input_price_per_million=1.0,
        cache_price_per_million=0.5,
        output_price_per_million=2.0,
    )


def test_registry_round_trips_subscription_cost(tmp_path):
    path = tmp_path / "registry.toml"
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        models=[
            ModelEntry(
                id="ollama/glm-5.3:cloud",
                family="glm",
                provider_id="ollama",
                model_name="glm-5.3:cloud",
                location="cloud",
                cost=Cost(subscription_price=20.0, subscription_period="month"),
            ),
        ],
    )
    save_registry(registry, path)
    loaded = load_registry(path)
    m = loaded.model("ollama/glm-5.3:cloud")
    assert m.cost == Cost(subscription_price=20.0, subscription_period="month")


def test_registry_omits_cost_keys_when_unset(tmp_path):
    """Unset cost must not leak keys into the saved TOML."""
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))],
        models=[
            ModelEntry(id="ollama/x", family="x", provider_id="ollama", model_name="x"),
        ],
    )
    path = tmp_path / "registry.toml"
    save_registry(registry, path)
    text = path.read_text()
    assert "cost" not in text
    loaded = load_registry(path)
    m = loaded.model("ollama/x")
    assert m.cost is None


def test_save_registry_writes_new_cost_keys_only(tmp_path):
    """Saved cost tables must use the new flat keys, never the legacy
    kind/price_per_million_tokens/price_per_period/period keys."""
    path = tmp_path / "registry.toml"
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        models=[
            ModelEntry(
                id="ollama/per-token",
                family="pt",
                provider_id="ollama",
                model_name="per-token",
                cost=Cost(input_price_per_million=1.0, output_price_per_million=2.0),
            ),
            ModelEntry(
                id="ollama/sub",
                family="sub",
                provider_id="ollama",
                model_name="sub",
                cost=Cost(subscription_price=20.0, subscription_period="month"),
            ),
            ModelEntry(
                id="ollama/free",
                family="free",
                provider_id="ollama",
                model_name="free",
                cost=Cost(),
            ),
        ],
    )
    save_registry(registry, path)
    text = path.read_text()
    assert "\nperiod = " not in text
    assert "\nkind = " not in text
    assert "price_per_million_tokens" not in text
    assert "price_per_period" not in text
    assert "input_price_per_million" in text
    assert "output_price_per_million" in text
    assert "subscription_price" in text
    assert "subscription_period" in text


def test_load_registry_migrates_legacy_free_cost(tmp_path):
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
        '[models.cost]\nkind = "free"\n'
    )
    loaded = load_registry(path)
    assert loaded.model("ollama/x").cost == Cost()


def test_load_registry_migrates_legacy_per_token_cost(tmp_path):
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
        '[models.cost]\nkind = "per_token"\nprice_per_million_tokens = 2.5\n'
    )
    loaded = load_registry(path)
    assert loaded.model("ollama/x").cost == Cost(
        input_price_per_million=2.5, output_price_per_million=2.5
    )


def test_load_registry_migrates_legacy_subscription_cost(tmp_path):
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
        '[models.cost]\nkind = "subscription"\nprice_per_period = 20.0\nperiod = "month"\n'
    )
    loaded = load_registry(path)
    assert loaded.model("ollama/x").cost == Cost(
        subscription_price=20.0, subscription_period="month"
    )


def test_legacy_cost_keys_do_not_leak_into_extra(tmp_path):
    """A legacy cost table with mixed old fields must migrate to the new
    flat keys; after save, no legacy cost keys may remain in the file."""
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
        '[models.cost]\nkind = "per_token"\nprice_per_million_tokens = 1.5\nperiod = "month"\n'
    )
    registry = load_registry(path)
    save_registry(registry, path)
    text = path.read_text()
    assert "kind" not in text
    assert "price_per_million_tokens" not in text
    assert "price_per_period" not in text
    assert "period" not in text


def test_load_registry_rejects_unknown_legacy_cost_kind(tmp_path):
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
        '[models.cost]\nkind = "enterprise"\n'
    )
    with pytest.raises(RegistryError, match="cost kind must be free/per_token/subscription"):
        load_registry(path)


def test_load_registry_ignores_usage_tier_field(tmp_path):
    """The usage_tier field is dropped from the schema; it must not be
    parsed, stored, or serialized, even when present in legacy files."""
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
        'usage_tier = "high"\n'
    )
    loaded = load_registry(path)
    assert not hasattr(loaded.model("ollama/x"), "usage_tier")
    save_registry(loaded, path)
    assert "usage_tier" not in path.read_text()


def test_load_registry_empty_cost_table_loads_as_default_cost(tmp_path):
    """An empty [models.cost] table must deserialize as Cost()."""
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
        "[models.cost]\n"
    )
    loaded = load_registry(path)
    assert loaded.model("ollama/x").cost == Cost()


def test_load_registry_boolean_price_raises(tmp_path):
    """Booleans are subclasses of int and must be rejected as prices."""
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
        "[models.cost]\ninput_price_per_million = true\n"
    )
    with pytest.raises(RegistryError, match="cost `input_price_per_million` must be a number"):
        load_registry(path)


def test_registry_round_trips_zero_subscription_price(tmp_path):
    """A zero subscription price must survive save/load unchanged."""
    path = tmp_path / "registry.toml"
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        models=[
            ModelEntry(
                id="ollama/x",
                family="x",
                provider_id="ollama",
                model_name="x",
                cost=Cost(subscription_price=0.0, subscription_period="month"),
            ),
        ],
    )
    save_registry(registry, path)
    loaded = load_registry(path)
    assert loaded.model("ollama/x").cost == Cost(
        subscription_price=0.0, subscription_period="month"
    )


def test_load_registry_usage_tier_not_preserved_in_extra(tmp_path):
    """The dropped usage_tier field must not leak into model.extra."""
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
        'usage_tier = "high"\n'
    )
    loaded = load_registry(path)
    assert loaded.model("ollama/x").extra.get("usage_tier") is None
