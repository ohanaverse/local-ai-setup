import pytest
from pathlib import Path
from modelman.config import Config, load_config, ConfigError


def test_load_config_from_file(tmp_path):
    config_file = tmp_path / "config.yaml"
    config_file.write_text(Path("tests/fixtures/sample_config.yaml").read_text())

    config = load_config(config_file)

    assert "ollama" in config.providers
    assert "llamacpp" in config.providers
    assert "omlx" in config.providers
    assert config.providers["omlx"]["model_dir"] == "~/.omlx/models"


def test_load_config_missing_file(tmp_path):
    with pytest.raises(ConfigError):
        load_config(tmp_path / "nonexistent.yaml")


def test_load_config_empty_providers(tmp_path):
    config_file = tmp_path / "config.yaml"
    config_file.write_text("providers: {}\n")
    with pytest.raises(ConfigError, match="at least one provider"):
        load_config(config_file)


def test_load_config_missing_type(tmp_path):
    config_file = tmp_path / "config.yaml"
    config_file.write_text("providers:\n  ollama:\n    foo: bar\n")
    with pytest.raises(ConfigError, match="missing required `type`"):
        load_config(config_file)