"""Tests for LiteLLM model_list entry construction and config read/write."""

import pytest

from modelman.litellm import (
    LiteLLMConfigError,
    build_model_list_entry,
    load_litellm_config,
    remove_exposed,
    save_litellm_config,
    set_exposed,
)
from modelman.registry import AuthConfig, ModelEntry, ProviderEntry


def _provider(pid, *, base_url=None, secret_ref=None, auth_type="none"):
    return ProviderEntry(
        id=pid,
        name=pid,
        auth=AuthConfig(type=auth_type, base_url=base_url, secret_ref=secret_ref),
    )


def _model(mid, provider_id, model_name, model_info=None):
    return ModelEntry(
        id=mid,
        family="f",
        provider_id=provider_id,
        model_name=model_name,
        model_info=model_info or {},
    )


def test_build_entry_ollama():
    entry = build_model_list_entry(
        _model("ollama/qwen3.8:27b-mlx", "ollama", "qwen3.8:27b-mlx"),
        _provider("ollama", base_url="http://localhost:11434"),
    )
    assert entry == {
        "model_name": "ollama/qwen3.8:27b-mlx",
        "litellm_params": {
            "model": "ollama_chat/qwen3.8:27b-mlx",
            "api_base": "http://localhost:11434",
        },
    }


def test_build_entry_omlx():
    entry = build_model_list_entry(
        _model("omlx/Qwen3.8-27B-4bit", "omlx", "Qwen3.8-27B-4bit"),
        _provider("omlx", base_url="http://localhost:8000/v1"),
    )
    assert entry["litellm_params"]["model"] == "openai/Qwen3.8-27B-4bit"
    assert entry["litellm_params"]["api_key"] == "not-needed"


def test_build_entry_llamacpp_uses_fixed_model():
    entry = build_model_list_entry(
        _model("llamacpp/ornith-1.5-35b", "llamacpp", "ornith-1.5-35b"),
        _provider("llamacpp", base_url="http://localhost:8080/v1"),
    )
    assert entry["litellm_params"]["model"] == "openai/local-model"
    assert entry["litellm_params"]["api_key"] == "dummy-key"


def test_build_entry_openrouter_uses_secret_ref():
    entry = build_model_list_entry(
        _model("openrouter/qwen/qwen3.8-27b", "openrouter", "qwen/qwen3.8-27b"),
        _provider(
            "openrouter",
            base_url="https://openrouter.ai/api/v1",
            secret_ref="sk-or-v1-abc",
            auth_type="api_key",
        ),
    )
    assert entry["litellm_params"]["model"] == "openrouter/qwen/qwen3.8-27b"
    assert entry["litellm_params"]["api_key"] == "sk-or-v1-abc"


def test_build_entry_copies_model_info():
    entry = build_model_list_entry(
        _model(
            "ollama/x",
            "ollama",
            "x",
            model_info={"supports_function_calling": True},
        ),
        _provider("ollama", base_url="http://localhost:11434"),
    )
    assert entry["model_info"] == {"supports_function_calling": True}


def test_build_entry_unknown_provider_raises():
    from modelman.litellm import ExposeError

    with pytest.raises(ExposeError):
        build_model_list_entry(_model("foo/x", "foo", "x"), _provider("foo"))


def test_is_cloud_reads_the_policy_table():
    # The TUI's expose gate and the config writer must agree on which
    # providers are cloud-exempt; is_cloud() reads the same table the
    # writer uses, so a new provider only needs one PROVIDER_POLICIES edit.
    from modelman.litellm import is_cloud

    assert is_cloud("openrouter") is True
    assert is_cloud("ollama") is False
    # Unknown providers are treated as local (conservative) — the writer
    # rejects them anyway.
    assert is_cloud("some-new-provider") is False


def test_set_exposed_adds_new_row():
    config = {"model_list": [], "general_settings": {"database_url": "x"}}
    set_exposed(config, "ollama/a", {"model_name": "ollama/a"})
    assert config["model_list"] == [{"model_name": "ollama/a"}]
    assert config["general_settings"] == {"database_url": "x"}


def test_set_exposed_replaces_existing_row():
    config = {"model_list": [{"model_name": "ollama/a", "old": True}]}
    set_exposed(config, "ollama/a", {"model_name": "ollama/a", "new": True})
    assert config["model_list"] == [{"model_name": "ollama/a", "new": True}]


def test_remove_exposed_removes_row():
    config = {"model_list": [{"model_name": "ollama/a"}, {"model_name": "ollama/b"}]}
    remove_exposed(config, "ollama/a")
    assert config["model_list"] == [{"model_name": "ollama/b"}]


def test_remove_exposed_noop_when_absent():
    config = {"model_list": [{"model_name": "ollama/b"}]}
    remove_exposed(config, "ollama/a")
    assert config["model_list"] == [{"model_name": "ollama/b"}]


def test_load_litellm_config_invalid_yaml_raises_config_error(tmp_path):
    # A hand-edited config with a YAML syntax error must surface as
    # LiteLLMConfigError (the CLI prints "error: ..."), not a raw
    # yaml.scanner.ScannerError traceback.
    path = tmp_path / "config.yaml"
    path.write_text("model_list:\n  - model_name: [unclosed\n broken: yaml:\n")
    with pytest.raises(LiteLLMConfigError, match="not valid YAML"):
        load_litellm_config(path)


def test_set_exposed_skips_non_dict_rows():
    # Hand-edited scalar rows aren't modelman's; they must be preserved
    # on write (module docstring guarantee) instead of crashing on
    # row.get with AttributeError.
    config = {"model_list": ["just-a-string", {"model_name": "ollama/b"}]}
    set_exposed(config, "ollama/a", {"model_name": "ollama/a"})
    assert config["model_list"] == [
        "just-a-string",
        {"model_name": "ollama/b"},
        {"model_name": "ollama/a"},
    ]


def test_remove_exposed_preserves_non_dict_rows():
    config = {"model_list": ["just-a-string", {"model_name": "ollama/a"}]}
    remove_exposed(config, "ollama/a")
    assert config["model_list"] == ["just-a-string"]


def test_set_exposed_initializes_null_model_list():
    # `model_list:` (explicit null) in a hand-edited YAML becomes an
    # empty list rather than a TypeError on iteration.
    config = {"model_list": None}
    set_exposed(config, "ollama/a", {"model_name": "ollama/a"})
    assert config["model_list"] == [{"model_name": "ollama/a"}]


def test_set_exposed_refuses_scalar_model_list():
    # A scalar model_list can't be edited in place; refuse loudly (the
    # CLI reports it) instead of crashing, and leave the section intact.
    config = {"model_list": "not-a-list"}
    with pytest.raises(LiteLLMConfigError, match="not a list"):
        set_exposed(config, "ollama/a", {"model_name": "ollama/a"})
    assert config["model_list"] == "not-a-list"


def test_remove_exposed_noop_when_model_list_is_scalar():
    config = {"model_list": "not-a-list"}
    remove_exposed(config, "ollama/a")
    assert config["model_list"] == "not-a-list"


def test_load_litellm_config_missing_raises(tmp_path):
    with pytest.raises(LiteLLMConfigError):
        load_litellm_config(tmp_path / "nope.yaml")


def test_save_roundtrip_preserves_general_settings(tmp_path):
    path = tmp_path / "config.yaml"
    config = {
        "model_list": [{"model_name": "ollama/a", "litellm_params": {"model": "x"}}],
        "general_settings": {"database_url": "postgresql://x"},
    }
    save_litellm_config(config, path)
    loaded = load_litellm_config(path)
    assert loaded == config


def test_save_preserves_existing_file_permissions(tmp_path):
    # mkstemp creates 0600 temp files and os.replace preserves that mode;
    # without the chmod, the first write would silently tighten a config
    # that a LiteLLM service running as another user needs to read.
    path = tmp_path / "config.yaml"
    path.write_text("model_list: []\n")
    path.chmod(0o644)
    save_litellm_config({"model_list": []}, path)
    assert path.stat().st_mode & 0o777 == 0o644


def test_save_creates_new_file_with_mkstemp_mode(tmp_path):
    # A brand-new config has no prior mode to inherit; the 0600 mkstemp
    # default is correct for a file that may hold secret_refs.
    path = tmp_path / "config.yaml"
    save_litellm_config({"model_list": []}, path)
    assert path.stat().st_mode & 0o777 == 0o600
