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
    Fetch,
    ModelEntry,
    ProviderEntry,
    Registry,
    RegistryError,
    _default_registry_path,
    load_registry,
    provider_config,
    save_registry,
)


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


def test_families_returns_sorted_distinct_family_names():
    # The family list drives the TUI's left pane; it must be sorted and
    # deduplicated so a family spanning multiple providers appears once.
    registry = Registry(
        models=[
            ModelEntry(id="ollama/a", family="zeta", provider_id="ollama", model_name="a"),
            ModelEntry(id="ollama/b", family="alpha", provider_id="ollama", model_name="b"),
            ModelEntry(id="llamacpp/c", family="alpha", provider_id="llamacpp", model_name="c"),
        ]
    )
    assert registry.families() == ["alpha", "zeta"]


def test_families_on_empty_registry_returns_empty_list():
    # A fresh registry has no models; families() must return [] rather than
    # raise, so the TUI renders an empty family list on first run.
    assert Registry().families() == []


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
