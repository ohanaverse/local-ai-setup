"""Migration from legacy modelman config.yaml + families/*.yaml (and,
optionally, wt's config.toml) into registry.toml +
modelman.toml. Exercises the "One-time migration" collision policy from
docs/superpowers/specs/2026-08-27-shared-model-registry-design.md: wt's
curated tags/cost/family/location always win for a model that exists in
both sources; modelman's legacy data only adds new entries or fills in
fields it uniquely owns (fetch, model_info, download state)."""

from pathlib import Path

from modelman.manifest import FamilyManifest, save_manifest
from modelman.migrate import migrate, migrate_wt_gateway_to_litellm
from modelman.providers.base import VariantSpec
from modelman.state import load_state, locked_state


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
    omlx = result.registry.provider("omlx")
    assert omlx.name == "oMLX"
    assert omlx.model_dir == "~/.omlx/models"  # still read from legacy config


def test_migrate_imports_wt_gateway_into_litellm_table(tmp_path, monkeypatch):
    """wt cannot write modelman.toml, so the one-time move of an existing
    [gateway] block (mode/url/api_key) into modelman's [litellm] table has
    to happen from modelman's side — otherwise every existing wt install
    loses its LiteLLM configuration the moment this feature ships."""
    wt_config = tmp_path / "wt-config.toml"
    wt_config.write_text(
        '[gateway]\nmode = "litellm"\nurl = "http://localhost:4000"\n'
        'api_key = "sk-litellm-existing"\n'
    )
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    imported = migrate_wt_gateway_to_litellm(wt_config_path=wt_config)
    assert imported is True

    state = load_state(path=state_path)
    assert state.litellm.enabled is True
    assert state.litellm.url == "http://localhost:4000"
    assert state.litellm.api_key == "sk-litellm-existing"


def test_migrate_gateway_import_is_idempotent(tmp_path, monkeypatch):
    """A second run (e.g. modelman migrate invoked twice) must not clobber
    a value the user has since changed via `modelman litellm set`."""
    wt_config = tmp_path / "wt-config.toml"
    wt_config.write_text(
        '[gateway]\nmode = "litellm"\nurl = "http://old:4000"\napi_key = "old-key"\n'
    )
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    migrate_wt_gateway_to_litellm(wt_config_path=wt_config)
    with locked_state(path=state_path) as state:
        state.litellm.url = "http://new:4000"  # user changed it since

    imported_again = migrate_wt_gateway_to_litellm(wt_config_path=wt_config)
    assert imported_again is False
    assert load_state(path=state_path).litellm.url == "http://new:4000"
