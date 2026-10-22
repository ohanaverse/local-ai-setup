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


def test_out_is_overridden_only_by_a_non_empty_variable() -> None:
    assert paths.out_is_overridden({"MODELTIERS_OUT": "/o/t.toml"})
    assert not paths.out_is_overridden({"MODELTIERS_OUT": ""})
    assert not paths.out_is_overridden({"HOME": "/h"})
    assert paths.out_is_overridden()  # conftest sets it


def test_the_registry_beside_the_output_file_is_recognised() -> None:
    env = {"HOME": "/h"}
    assert paths.registry_is_beside_output("/h/.config/local-ai/registry.toml", env)
    assert not paths.registry_is_beside_output("/tmp/scratch/registry.toml", env)
    # The right directory and another file name is still a redirect.
    assert not paths.registry_is_beside_output("/h/.config/local-ai/scratch.toml", env)
    xdg = {"HOME": "/h", "XDG_CONFIG_HOME": "/x"}
    assert paths.registry_is_beside_output("/x/local-ai/registry.toml", xdg)
    assert not paths.registry_is_beside_output("/h/.config/local-ai/registry.toml", xdg)


def test_a_symlinked_config_directory_still_matches(tmp_path: Path) -> None:
    real = tmp_path / "dotfiles" / "local-ai"
    real.mkdir(parents=True)
    config = tmp_path / "home" / ".config"
    config.mkdir()
    (config / "local-ai").symlink_to(real)
    env = {"HOME": str(tmp_path / "home")}
    assert paths.registry_is_beside_output(str(real / "registry.toml"), env)
    assert paths.registry_is_beside_output(str(config / "local-ai" / "registry.toml"), env)


def test_a_registry_path_with_a_tilde_is_expanded(tmp_path: Path) -> None:
    # conftest's HOME is tmp_path/home; expanduser reads the process environment.
    env = {"HOME": str(tmp_path / "home")}
    assert paths.registry_is_beside_output("~/.config/local-ai/registry.toml", env)
