"""Tests for modelman's read-only LiteLLM config helpers.

Row building, config.yaml editing, the launcher-required settings ensure
and the proxy restart all live in Go (`wt/internal/litellm`), and since
#179 modelman's only write path is `sync_routes` (tests/test_routes_sync.py).
What is left to test here is the read-only config helpers `modelman usage`
depends on.
"""

import pytest

from modelman.litellm import (
    LiteLLMConfigError,
    _database_url_from_config,
    _reverse_model_index,
    load_litellm_config,
)


def test_load_litellm_config_invalid_yaml_raises_config_error(tmp_path):
    # A hand-edited config with a YAML syntax error must surface as
    # LiteLLMConfigError (the CLI prints "error: ..."), not a raw
    # yaml.scanner.ScannerError traceback.
    path = tmp_path / "config.yaml"
    path.write_text("model_list:\n  - model_name: [unclosed\n broken: yaml:\n")
    with pytest.raises(LiteLLMConfigError, match="not valid YAML"):
        load_litellm_config(path)


def test_load_litellm_config_missing_raises(tmp_path):
    assert not (tmp_path / "nope.yaml").exists()
    with pytest.raises(LiteLLMConfigError):
        load_litellm_config(tmp_path / "nope.yaml")


def test_load_litellm_config_non_mapping_raises(tmp_path):
    # `modelman usage` indexes the loaded document by key; a config that
    # parses to a list or scalar must be refused with the same error type
    # rather than exploding later in _database_url_from_config.
    path = tmp_path / "config.yaml"
    path.write_text("- just\n- a list\n")
    with pytest.raises(LiteLLMConfigError, match="not a mapping"):
        load_litellm_config(path)


def test_load_litellm_config_reads_a_document_wt_wrote(tmp_path):
    # The read path must still understand the real file shape (comments
    # and all) now that wt, not modelman, produces it.
    path = tmp_path / "config.yaml"
    path.write_text(
        "# hand-written note\n"
        "model_list:\n"
        "- model_name: ollama/a\n"
        "  litellm_params:\n"
        "    model: ollama_chat/a\n"
        "general_settings:\n"
        "  database_url: postgresql://x\n"
    )
    config = load_litellm_config(path)
    assert [r["model_name"] for r in config["model_list"]] == ["ollama/a"]
    assert _database_url_from_config(config) == "postgresql://x"


def test_database_url_from_config_reads_general_settings():
    config = {
        "model_list": [],
        "general_settings": {"database_url": "postgresql://user@localhost/db"},
    }
    assert _database_url_from_config(config) == "postgresql://user@localhost/db"


def test_database_url_from_config_missing_returns_none():
    assert _database_url_from_config({"model_list": []}) is None


def test_reverse_model_index():
    # The reverse index must map each model_list entry's litellm_params.model
    # back to its registry model_name, since that's how NULL-model_name spend
    # rows get resolved.
    model_list = [
        {
            "model_name": "ollama/qwen3.8:27b-mlx",
            "litellm_params": {"model": "ollama_chat/qwen3.8:27b-mlx"},
        },
        {
            "model_name": "openrouter/qwen/qwen3.8-27b",
            "litellm_params": {"model": "openrouter/qwen/qwen3.8-27b"},
        },
        {
            "model_name": "omlx/Qwen3.8-27B-4bit",
            "litellm_params": {"model": "openai/Qwen3.8-27B-4bit"},
        },
    ]
    index = _reverse_model_index(model_list)
    assert index["ollama_chat/qwen3.8:27b-mlx"] == "ollama/qwen3.8:27b-mlx"
    assert index["openrouter/qwen/qwen3.8-27b"] == "openrouter/qwen/qwen3.8-27b"
    assert index["openai/Qwen3.8-27B-4bit"] == "omlx/Qwen3.8-27B-4bit"


def test_reverse_model_index_first_entry_wins_on_duplicate():
    # Two model_list entries can point at the same litellm_params.model; the
    # first entry must win deterministically.
    model_list = [
        {"model_name": "ollama/a", "litellm_params": {"model": "shared/target"}},
        {"model_name": "ollama/b", "litellm_params": {"model": "shared/target"}},
    ]
    index = _reverse_model_index(model_list)
    assert index["shared/target"] == "ollama/a"


def test_reverse_model_index_skips_non_dict_rows():
    # Hand-edited scalar rows in model_list must be ignored, not crash.
    model_list = [
        "just-a-string",
        {"model_name": "ollama/a", "litellm_params": {"model": "m"}},
        {"model_name": "ollama/b"},
    ]
    index = _reverse_model_index(model_list)
    assert index == {"m": "ollama/a"}
