"""Migration from legacy modelman config.yaml + families/*.yaml (and,
optionally, wt's config.toml) into registry.toml +
modelman.toml. Exercises the "One-time migration" collision policy from
docs/superpowers/specs/2026-08-27-shared-model-registry-design.md: wt's
curated tags/cost/family/location always win for a model that exists in
both sources; modelman's legacy data only adds new entries or fills in
fields it uniquely owns (fetch, model_info, download state)."""

from pathlib import Path

from modelman.manifest import FamilyManifest, save_manifest
from modelman.migrate import migrate
from modelman.providers.base import VariantSpec


def _write_modelman_config(path: Path) -> None:
    path.write_text(
        "providers:\n"
        "  ollama:\n"
        "    type: ollama\n"
        "  llamacpp:\n"
        "    type: llamacpp\n"
        "  omlx:\n"
        "    type: omlx\n"
        "    model_dir: ~/.omlx/models\n"
    )


def _write_wt_config(path: Path) -> None:
    path.write_text(
        "[[providers]]\n"
        '  id = "ollama"\n'
        '  name = "Ollama"\n'
        '  location = "local"\n'
        "  [providers.auth]\n"
        '    type = "none"\n'
        '    base_url = "http://localhost:11434"\n'
        "\n"
        "[[models]]\n"
        '  id = "ollama/qwen3.8:27b-mlx"\n'
        '  family = "qwen3.8"\n'
        '  provider_id = "ollama"\n'
        '  model_name = "qwen3.8:27b-mlx"\n'
        '  location = "local"\n'
        '  tags = ["code", "design"]\n'
    )


def test_migrate_modelman_only_when_wt_config_absent(tmp_path):
    config_path = tmp_path / "config.yaml"
    family_dir = tmp_path / "families"
    family_dir.mkdir()
    _write_modelman_config(config_path)
    save_manifest(
        FamilyManifest(
            family="qwen3.8",
            variants=[
                VariantSpec(
                    id="q1",
                    provider="llamacpp",
                    name="qwen3.8-27b-q4",
                    repo="unsloth/Qwen3.8-27B-GGUF",
                    files=["Qwen3.8-27B-UD-Q4_K_XL.gguf"],
                ),
            ],
        ),
        family_dir / "qwen3.8.yaml",
    )

    result = migrate(config_path, family_dir, wt_config_path=tmp_path / "no-such-wt-config.toml")

    assert any("wt config not found" in w for w in result.warnings)
    assert result.registry.provider("llamacpp").id == "llamacpp"
    model = result.registry.model("llamacpp/qwen3.8-27b-q4")
    assert model.tags == []
    assert model.fetch.repo == "unsloth/Qwen3.8-27B-GGUF"


def test_migrate_imports_wt_providers_and_models(tmp_path):
    config_path = tmp_path / "config.yaml"
    family_dir = tmp_path / "families"
    family_dir.mkdir()
    _write_modelman_config(config_path)
    wt_config_path = tmp_path / "wt-config.toml"
    _write_wt_config(wt_config_path)

    result = migrate(config_path, family_dir, wt_config_path=wt_config_path)

    model = result.registry.model("ollama/qwen3.8:27b-mlx")
    assert model.tags == ["code", "design"]
    assert model.family == "qwen3.8"
    assert model.location == "local"


def test_migrate_merges_wt_tags_with_modelman_model_info(tmp_path):
    config_path = tmp_path / "config.yaml"
    family_dir = tmp_path / "families"
    family_dir.mkdir()
    _write_modelman_config(config_path)
    wt_config_path = tmp_path / "wt-config.toml"
    _write_wt_config(wt_config_path)
    save_manifest(
        FamilyManifest(
            family="qwen3.8",
            variants=[
                VariantSpec(
                    id="q2",
                    provider="ollama",
                    name="qwen3.8:27b-mlx",
                    model_info={"supports_function_calling": True},
                ),
            ],
        ),
        family_dir / "qwen3.8.yaml",
    )

    result = migrate(config_path, family_dir, wt_config_path=wt_config_path)

    model = result.registry.model("ollama/qwen3.8:27b-mlx")
    assert model.tags == ["code", "design"]  # untouched — came from wt
    assert model.model_info == {"supports_function_calling": True}  # filled in by modelman


def test_migrate_records_downloaded_state_from_modelman_manifest(tmp_path):
    config_path = tmp_path / "config.yaml"
    family_dir = tmp_path / "families"
    family_dir.mkdir()
    _write_modelman_config(config_path)
    manifest = FamilyManifest(
        family="qwen3.8",
        variants=[
            VariantSpec(
                id="q1",
                provider="llamacpp",
                name="qwen3.8-27b-q4",
                repo="unsloth/Qwen3.8-27B-GGUF",
                files=["Qwen3.8-27B-UD-Q4_K_XL.gguf"],
            ),
        ],
    )
    manifest.downloaded["q1"] = {
        "downloaded_at": "2026-08-31T00:00:00",
        "local_path": "/models/qwen3.8-27b-q4.gguf",
    }
    save_manifest(manifest, family_dir / "qwen3.8.yaml")

    result = migrate(config_path, family_dir, wt_config_path=tmp_path / "absent.toml")

    state = result.state.get("llamacpp/qwen3.8-27b-q4")
    assert state.ready is True
    assert state.disk_path == "/models/qwen3.8-27b-q4.gguf"


def test_migrate_uses_canonical_provider_defaults(tmp_path):
    # A migrated reconcilable provider must carry the same display name and
    # auth base_url as a sync-repaired one, or exposing an ollama model after
    # migrate would emit api_base: None and produce a broken LiteLLM route.
    config_path = tmp_path / "config.yaml"
    family_dir = tmp_path / "families"
    family_dir.mkdir()
    _write_modelman_config(config_path)

    result = migrate(config_path, family_dir, wt_config_path=tmp_path / "absent.toml")

    ollama = result.registry.provider("ollama")
    assert ollama.name == "Ollama"
    assert ollama.auth.base_url == "http://localhost:11434"
    assert result.registry.provider("llamacpp").name == "llama.cpp"
    omlx = result.registry.provider("omlx")
    assert omlx.name == "oMLX"
    assert omlx.model_dir == "~/.omlx/models"  # still read from legacy config
