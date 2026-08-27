"""registry.toml is the canonical, shared model/provider registry per the
2026-08-27 shared-model-registry design spec. These tests cover the load/
save round trip and the minimal validation that catches a hand-edited file
missing a field code elsewhere assumes exists (id, provider_id, model_name,
cost.kind)."""

import pytest

from modelman.registry import (
    AuthConfig,
    Cost,
    Fetch,
    ModelEntry,
    ProviderEntry,
    Registry,
    RegistryError,
    load_registry,
    save_registry,
)


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
