"""The read-only registry reader: where it looks, and what it reads."""

import os
from pathlib import Path

import pytest

from llmbench.registry import (
    RegistryError,
    is_model_local,
    load_registry,
    registry_path,
    registry_read_path,
)

FIXTURE = Path(__file__).resolve().parents[2] / "docs" / "contracts" / "registry.sample.toml"

MINIMAL = """\
[[providers]]
id = "ollama"

[[models]]
id = "ollama/a"
family = "f"
provider_id = "ollama"
model_name = "a"
"""


@pytest.fixture
def home(monkeypatch, tmp_path):
    """A scratch home with no registry override in the environment."""
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    monkeypatch.delenv("MODELMAN_REGISTRY", raising=False)
    monkeypatch.delenv("XDG_CONFIG_HOME", raising=False)
    return tmp_path / "home"


def _write(path: Path, text: str = MINIMAL) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")
    return path


def test_registry_path_precedence(home, monkeypatch, tmp_path):
    """MODELMAN_REGISTRY > XDG_CONFIG_HOME > ~/.config: the precedence wt and
    modelman use, so the three tools never read three different files."""
    assert registry_path() == home / ".config" / "local-ai" / "registry.toml"
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    assert registry_path() == tmp_path / "xdg" / "local-ai" / "registry.toml"
    monkeypatch.setenv("MODELMAN_REGISTRY", str(tmp_path / "named.toml"))
    assert registry_path() == tmp_path / "named.toml"


def test_read_path_falls_back_to_the_pre_xdg_registry(home, monkeypatch, tmp_path):
    """A registry created before XDG_CONFIG_HOME was set still lives in
    ~/.config. modelman reads it there, so llmbench must too: otherwise
    `modelman start <mtplx model>` (which loads the registry through
    llmbench's mtplx backend) reports "no registry" on a machine modelman's
    own commands read fine."""
    legacy = _write(home / ".config" / "local-ai" / "registry.toml")
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    assert registry_read_path() == legacy
    assert [m.id for m in load_registry().models] == ["ollama/a"]

    canonical = _write(tmp_path / "xdg" / "local-ai" / "registry.toml")
    assert registry_read_path() == canonical


def test_read_path_does_not_fall_back_past_a_named_registry(home, monkeypatch, tmp_path):
    """MODELMAN_REGISTRY names the file outright: a missing one is missing,
    never "use the one in ~/.config" — a scratch run must not read the
    developer's real registry by accident."""
    _write(home / ".config" / "local-ai" / "registry.toml")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(tmp_path / "scratch.toml"))
    with pytest.raises(RegistryError, match="Registry file not found: .*scratch.toml"):
        registry_read_path()
    with pytest.raises(RegistryError, match="Registry file not found"):
        load_registry(tmp_path / "also-missing.toml")


def test_read_path_refuses_a_dangling_symlink(home, monkeypatch, tmp_path):
    """A link to a registry that is not there (a dotfiles checkout, an
    unmounted volume) is refused, not fallen back from: reading the pre-XDG
    file instead would benchmark a registry the user has replaced (#248)."""
    _write(home / ".config" / "local-ai" / "registry.toml")
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    link = tmp_path / "xdg" / "local-ai" / "registry.toml"
    link.parent.mkdir(parents=True)
    os.symlink(tmp_path / "gone.toml", link)
    with pytest.raises(RegistryError, match="is a symlink to .*gone.toml, which does not exist"):
        registry_read_path()


def test_read_path_refuses_a_dangling_legacy_symlink(home, monkeypatch, tmp_path):
    """The same refusal for the file being fallen back to. XDG_CONFIG_HOME is
    set and holds no registry, and the pre-XDG path is a link to nothing:
    modelman names the link, so llmbench must not say "not found" about the
    XDG path and send the user looking in the wrong directory."""
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    link = home / ".config" / "local-ai" / "registry.toml"
    link.parent.mkdir(parents=True)
    os.symlink(tmp_path / "gone.toml", link)
    with pytest.raises(RegistryError, match="is a symlink to .*gone.toml, which does not exist"):
        registry_read_path()


def test_load_registry_tolerates_unknown_top_level_keys(tmp_path):
    """llmbench never writes the registry, so a top-level table it does not
    know costs it nothing — unlike modelman, whose save would drop it (#247).
    A registry wt has extended must still benchmark."""
    path = _write(tmp_path / "registry.toml", MINIMAL + '\n[future]\nkey = "v"\n')
    assert [m.id for m in load_registry(path).models] == ["ollama/a"]


def test_load_registry_reports_a_file_it_cannot_parse(tmp_path):
    path = _write(tmp_path / "registry.toml", "[[models]\n")
    with pytest.raises(RegistryError, match=f"cannot read {path}"):
        load_registry(path)


def test_load_registry_names_a_row_missing_a_required_field(tmp_path):
    path = _write(tmp_path / "registry.toml", '[[models]]\nid = "x"\n')
    with pytest.raises(RegistryError, match="missing required fields"):
        load_registry(path)
    path = _write(tmp_path / "registry.toml", '[[providers]]\nname = "x"\n')
    with pytest.raises(RegistryError, match="missing required `id` field"):
        load_registry(path)


def test_load_registry_reads_the_shared_contract_fixture():
    """docs/contracts/registry.sample.toml is the schema wt's Go decoder and
    modelman's loader are pinned to. llmbench reads the same file, so a
    schema change that breaks a benchmark fails here in the same PR."""
    registry = load_registry(FIXTURE)

    # Membership, not the whole list: a row a later step adds to the fixture
    # is not this reader's business.
    assert {"ollama", "openrouter", "pinned-cloud", "mlx_lm_server", "mtplx"} <= {
        p.id for p in registry.providers
    }
    assert registry.provider("ollama").location == "local"
    assert registry.provider("openrouter").location == "cloud"
    assert len(registry.models) >= 7

    cloud = registry.model("openrouter/contract-fixture:cloud")
    assert (cloud.family, cloud.provider_id, cloud.model_name, cloud.location) == (
        "contract-fixture",
        "openrouter",
        "org/contract-fixture-cloud",
        "cloud",
    )
    pair = registry.model("mlx_lm_server/contract-fixture:pair")
    assert pair.fetch is not None and pair.fetch.repo == "org/contract-fixture-target"
    assert pair.draft is not None and pair.draft.repo == "org/contract-fixture-draft"
    assert pair.fetch.local_path is None and pair.draft.local_path is None
    assert registry.model("ollama/contract-fixture:local").fetch is None

    # Location: a model's own value wins, else its provider's.
    inherit = registry.model("pinned-cloud/contract-fixture:inherit")
    assert inherit.location is None
    assert not is_model_local(inherit.location, inherit.provider_id, registry)
    local = registry.model("mtplx/org--contract-fixture-dashed")
    assert is_model_local(local.location, local.provider_id, registry)
    assert not is_model_local(None, "no-such-provider", registry)
    assert is_model_local(None, "no-such-provider", registry, missing_provider_is_local=True)
