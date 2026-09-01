"""Tests for LiteLLM model_list entry construction and config read/write."""

import pytest

from modelman.litellm import (
    LiteLLMConfigError,
    _database_url_from_config,
    _reverse_model_index,
    build_model_list_entry,
    ensure_litellm_settings,
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


def test_set_exposed_preserves_user_managed_additional_drop_params():
    # Re-exposing a row must not wipe a user-managed
    # additional_drop_params extension; ensure_litellm_settings should
    # then see the key is present and leave the value untouched (spec
    # Decision 3: presence-based, existing values of any kind are respected).
    config = {
        "model_list": [
            {
                "model_name": "ollama/a",
                "litellm_params": {
                    "model": "ollama_chat/a",
                    "api_base": "http://old:11434",
                    "additional_drop_params": ["reasoning_effort", "user_param"],
                },
            }
        ]
    }
    set_exposed(
        config,
        "ollama/a",
        {
            "model_name": "ollama/a",
            "litellm_params": {
                "model": "ollama_chat/a",
                "api_base": "http://new:11434",
            },
        },
    )
    ensure_litellm_settings(config)
    params = config["model_list"][0]["litellm_params"]
    assert params["api_base"] == "http://new:11434"
    assert params["additional_drop_params"] == ["reasoning_effort", "user_param"]


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
        {
            "model_name": "ollama/a",
            "litellm_params": {"model": "shared/target"},
        },
        {
            "model_name": "ollama/b",
            "litellm_params": {"model": "shared/target"},
        },
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


def test_default_restart_cmd_unset(monkeypatch):
    monkeypatch.delenv("MODELMAN_LITELLM_RESTART_CMD", raising=False)
    from modelman.litellm import default_litellm_restart_cmd

    assert default_litellm_restart_cmd() is None


def test_default_restart_cmd_set(monkeypatch):
    monkeypatch.setenv("MODELMAN_LITELLM_RESTART_CMD", "echo restart")
    from modelman.litellm import default_litellm_restart_cmd

    assert default_litellm_restart_cmd() == "echo restart"


def test_restart_proxy_noop_when_unset(monkeypatch):
    monkeypatch.delenv("MODELMAN_LITELLM_RESTART_CMD", raising=False)
    from modelman.litellm import restart_litellm_proxy

    # Returns a warning (not a stderr print) telling the user to restart
    # the proxy manually; must not raise.
    warnings = restart_litellm_proxy()
    assert len(warnings) == 1
    assert "restart" in warnings[0].lower()


def test_restart_proxy_runs_command(monkeypatch):
    monkeypatch.setenv("MODELMAN_LITELLM_RESTART_CMD", "echo restarted")
    import subprocess

    calls = []

    def fake_run(cmd, *, shell, check, timeout):
        calls.append((cmd, shell, check, timeout))

    monkeypatch.setattr(subprocess, "run", fake_run)

    from modelman.litellm import restart_litellm_proxy

    # No warnings on success.
    assert restart_litellm_proxy() == []
    assert calls == [("echo restarted", True, True, 30)]


def test_restart_proxy_failure_is_nonfatal(monkeypatch):
    monkeypatch.setenv("MODELMAN_LITELLM_RESTART_CMD", "false")
    from modelman.litellm import restart_litellm_proxy

    # Must not raise; returns a warning instead.
    warnings = restart_litellm_proxy()
    assert len(warnings) == 1
    assert "restart" in warnings[0].lower()


def test_roundtrip_preserves_comments_byte_identical(tmp_path):
    # ruamel round-trip must reproduce hand-written "why" comments and
    # layout byte-for-byte when modelman saves without touching them —
    # the settings-persistence spec's core guarantee (Decision 5).
    original = (
        "# Tolerate params the bridged provider does not support.\n"
        "model_list:\n"
        "- model_name: ollama/a\n"
        "  litellm_params:\n"
        "    model: ollama_chat/a\n"
        "litellm_settings:\n"
        "  drop_params: true\n"
    )
    path = tmp_path / "config.yaml"
    path.write_text(original)
    config = load_litellm_config(path)
    save_litellm_config(config, path)
    assert path.read_text() == original


def test_ensure_adds_drop_params_when_section_missing():
    config = {"model_list": []}
    assert ensure_litellm_settings(config) is True
    assert config["litellm_settings"] == {"drop_params": True}


def test_ensure_corrects_drop_params_false():
    # Value-enforced: any non-true value is a launcher break waiting to
    # happen (copilot 400s), so modelman corrects it, not just missing keys.
    config = {"litellm_settings": {"drop_params": False}}
    assert ensure_litellm_settings(config) is True
    assert config["litellm_settings"]["drop_params"] is True


def test_ensure_leaves_correct_drop_params_untouched():
    config = {"litellm_settings": {"drop_params": True}}
    assert ensure_litellm_settings(config) is False


def test_ensure_preserves_other_litellm_settings():
    config = {"litellm_settings": {"drop_params": False, "num_workers": 4}}
    assert ensure_litellm_settings(config) is True
    assert config["litellm_settings"] == {"drop_params": True, "num_workers": 4}


def test_ensure_adds_bridge_drop_params_to_ollama_chat_rows():
    config = {
        "model_list": [
            {"model_name": "a", "litellm_params": {"model": "ollama_chat/a"}},
        ]
    }
    assert ensure_litellm_settings(config) is True
    params = config["model_list"][0]["litellm_params"]
    assert params["additional_drop_params"] == ["reasoning_effort"]


def test_ensure_leaves_existing_bridge_drop_params_untouched():
    # Presence-based: an existing key — an extended list or a deliberate
    # empty list — marks the row user-managed; only missing keys are added.
    config = {
        "litellm_settings": {"drop_params": True},
        "model_list": [
            {
                "model_name": "a",
                "litellm_params": {
                    "model": "ollama_chat/a",
                    "additional_drop_params": ["reasoning_effort", "other"],
                },
            },
            {
                "model_name": "b",
                "litellm_params": {
                    "model": "ollama_chat/b",
                    "additional_drop_params": [],
                },
            },
        ],
    }
    assert ensure_litellm_settings(config) is False


def test_ensure_never_touches_non_bridge_rows():
    config = {
        "model_list": [
            {"model_name": "x", "litellm_params": {"model": "openai/x"}},
            {"model_name": "y", "litellm_params": {"model": "openrouter/y"}},
        ],
        "litellm_settings": {"drop_params": True},
    }
    assert ensure_litellm_settings(config) is False


def test_ensure_covers_rows_modelman_did_not_write():
    # The ensure runs over the whole parsed model_list, not just newly
    # created rows — hand-added ollama_chat rows are repaired too.
    config = {
        "litellm_settings": {"drop_params": True},
        "model_list": [
            {"model_name": "hand/row", "litellm_params": {"model": "ollama_chat/hand"}},
        ],
    }
    assert ensure_litellm_settings(config) is True


def test_ensure_skips_degenerate_shapes():
    # Hand-edited scalars aren't modelman's to fix; skip rather than
    # crash — the module's established tolerance pattern.
    config = {"litellm_settings": "broken", "model_list": ["just-a-string"]}
    assert ensure_litellm_settings(config) is False


def test_ensure_is_idempotent():
    # A second run over a healed document changes nothing (spec
    # Decision 3: no save, no proxy bounce).
    config = {
        "model_list": [
            {"model_name": "a", "litellm_params": {"model": "ollama_chat/a"}},
        ]
    }
    assert ensure_litellm_settings(config) is True
    assert ensure_litellm_settings(config) is False


def test_save_preserves_comments_when_other_rows_change(tmp_path):
    # Editing one row (set_exposed replaces it with a plain dict) must
    # not disturb comments or sections modelman didn't touch.
    original = (
        "# top-level why comment\n"
        "model_list:\n"
        "- model_name: ollama/a\n"
        "  litellm_params:\n"
        "    model: ollama_chat/a\n"
        "general_settings:\n"
        "  database_url: postgresql://x\n"
    )
    path = tmp_path / "config.yaml"
    path.write_text(original)
    config = load_litellm_config(path)
    set_exposed(
        config,
        "ollama/b",
        {"model_name": "ollama/b", "litellm_params": {"model": "ollama_chat/b"}},
    )
    save_litellm_config(config, path)
    text = path.read_text()
    assert "# top-level why comment" in text
    assert "database_url: postgresql://x" in text
    loaded = load_litellm_config(path)
    assert [r["model_name"] for r in loaded["model_list"]] == ["ollama/a", "ollama/b"]
