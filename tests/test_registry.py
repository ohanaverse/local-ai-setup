"""registry.toml is the canonical, shared model/provider registry per the
2026-08-27 shared-model-registry design spec. These tests cover the load/
save round trip and the minimal validation that catches a hand-edited file
missing a field code elsewhere assumes exists (id, provider_id, model_name,
cost.kind)."""

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
    family_display_name,
    known_families,
    load_registry,
    provider_config,
    save_registry,
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
                cost=Cost(kind="free"),
                model_info={"supports_function_calling": True},
            ),
            ModelEntry(
                id="llamacpp/qwen3.8-27b-q4",
                family="qwen3.8",
                provider_id="llamacpp",
                model_name="qwen3.8-27b-q4",
                location="local",
                tags=["code"],
                cost=Cost(kind="free"),
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
    assert loaded.model("ollama/qwen3.8:27b-mlx").cost == Cost(kind="free")
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


def test_load_registry_missing_cost_kind_raises(tmp_path):
    path = tmp_path / "registry.toml"
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n'
        '[providers.auth]\ntype = "none"\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
        "[models.cost]\nprice_per_million_tokens = 1.0\n"
    )
    with pytest.raises(RegistryError, match="cost missing required `kind`"):
        load_registry(path)


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
