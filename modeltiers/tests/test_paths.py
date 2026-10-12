from pathlib import Path

from modeltiers import paths


def test_output_defaults_to_config_home() -> None:
    assert paths.output_path({"HOME": "/h"}) == Path("/h/.config/local-ai/tiers.toml")


def test_output_follows_xdg_config_home() -> None:
    env = {"HOME": "/h", "XDG_CONFIG_HOME": "/x"}
    assert paths.output_path(env) == Path("/x/local-ai/tiers.toml")


def test_output_override_wins() -> None:
    env = {"HOME": "/h", "XDG_CONFIG_HOME": "/x", "MODELTIERS_OUT": "/o/t.toml"}
    assert paths.output_path(env) == Path("/o/t.toml")


def test_cache_defaults_to_cache_home() -> None:
    assert paths.cache_dir({"HOME": "/h"}) == Path("/h/.cache/local-ai/modeltiers")


def test_cache_follows_xdg_cache_home() -> None:
    env = {"HOME": "/h", "XDG_CACHE_HOME": "/c"}
    assert paths.cache_dir(env) == Path("/c/local-ai/modeltiers")


def test_cache_override_wins() -> None:
    env = {"HOME": "/h", "XDG_CACHE_HOME": "/c", "MODELTIERS_CACHE": "/m"}
    assert paths.cache_dir(env) == Path("/m")


def test_an_empty_variable_counts_as_unset() -> None:
    env = {"HOME": "/h", "XDG_CONFIG_HOME": "", "XDG_CACHE_HOME": "", "MODELTIERS_OUT": ""}
    assert paths.output_path(env) == Path("/h/.config/local-ai/tiers.toml")
    assert paths.cache_dir(env) == Path("/h/.cache/local-ai/modeltiers")


def test_the_process_environment_is_the_default(tmp_path: Path) -> None:
    # conftest points both overrides under tmp_path.
    assert paths.output_path() == tmp_path / "out" / "tiers.toml"
    assert paths.cache_dir() == tmp_path / "cache"


def test_home_falls_back_to_the_user_database_entry(tmp_path: Path) -> None:
    # No HOME in the mapping: Path.home() is asked, and conftest's HOME answers.
    assert paths.output_path({}) == tmp_path / "home" / ".config" / "local-ai" / "tiers.toml"
