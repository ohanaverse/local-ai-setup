"""modelman and llmbench must read the same registry.toml.

`modelman start <mtplx model>` loads the registry twice: modelman's loader
picks the model, then llmbench's mtplx backend loads it again to resolve the
name. If the two resolve different files, the start acts on one registry and
serves a model from another. Deleted with modelman.
"""

import os
import re
from pathlib import Path

import pytest
from llmbench import registry as bench_registry

from modelman import registry as modelman_registry

REGISTRY = '[[models]]\nid = "mtplx/a"\nfamily = "f"\nprovider_id = "mtplx"\nmodel_name = "a"\n'


@pytest.fixture
def home(monkeypatch, tmp_path):
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    monkeypatch.delenv("WT_REGISTRY", raising=False)
    monkeypatch.delenv("MODELMAN_REGISTRY", raising=False)
    monkeypatch.delenv("XDG_CONFIG_HOME", raising=False)
    return tmp_path / "home"


def _write(path: Path) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(REGISTRY, encoding="utf-8")
    return path


def _both_read() -> Path:
    """The file both loaders read; fails when they differ."""
    ours = modelman_registry._registry_read_path()
    theirs = bench_registry.registry_read_path()
    assert theirs == ours
    assert bench_registry.registry_path() == modelman_registry._default_registry_path()
    assert [m.id for m in bench_registry.load_registry().models] == ["mtplx/a"]
    return ours


def test_both_read_the_xdg_registry_when_it_is_there(home, monkeypatch, tmp_path):
    _write(home / ".config" / "local-ai" / "registry.toml")
    xdg = _write(tmp_path / "xdg" / "local-ai" / "registry.toml")
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    assert _both_read() == xdg


def test_both_fall_back_to_the_pre_xdg_registry(home, monkeypatch, tmp_path):
    legacy = _write(home / ".config" / "local-ai" / "registry.toml")
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    assert _both_read() == legacy


def test_both_read_the_default_registry_without_xdg(home):
    default = _write(home / ".config" / "local-ai" / "registry.toml")
    assert _both_read() == default


def test_both_report_no_registry_at_the_xdg_path(home, monkeypatch, tmp_path):
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    wanted = tmp_path / "xdg" / "local-ai" / "registry.toml"
    with pytest.raises(modelman_registry.RegistryNotFoundError, match=re.escape(str(wanted))):
        modelman_registry._registry_read_path()
    with pytest.raises(bench_registry.RegistryError, match=re.escape(str(wanted))):
        bench_registry.registry_read_path()


def test_neither_falls_back_past_a_named_registry(home, monkeypatch, tmp_path):
    _write(home / ".config" / "local-ai" / "registry.toml")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(tmp_path / "scratch.toml"))
    with pytest.raises(modelman_registry.RegistryNotFoundError):
        modelman_registry._registry_read_path()
    with pytest.raises(bench_registry.RegistryError, match="Registry file not found"):
        bench_registry.registry_read_path()


def test_both_refuse_a_dangling_symlink(home, monkeypatch, tmp_path):
    _write(home / ".config" / "local-ai" / "registry.toml")
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    link = tmp_path / "xdg" / "local-ai" / "registry.toml"
    link.parent.mkdir(parents=True)
    os.symlink(tmp_path / "gone.toml", link)
    with pytest.raises(modelman_registry.RegistryPathError):
        modelman_registry._registry_read_path()
    with pytest.raises(bench_registry.RegistryError, match="is a symlink to"):
        bench_registry.registry_read_path()


def test_both_refuse_a_dangling_legacy_symlink(home, monkeypatch, tmp_path):
    """The same refusal for the file being fallen back to: XDG_CONFIG_HOME
    holds no registry and the pre-XDG path is a link to nothing. If only one
    reader named the link, the other would report the XDG path as missing."""
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    link = home / ".config" / "local-ai" / "registry.toml"
    link.parent.mkdir(parents=True)
    os.symlink(tmp_path / "gone.toml", link)
    with pytest.raises(modelman_registry.RegistryPathError):
        modelman_registry._registry_read_path()
    with pytest.raises(bench_registry.RegistryError, match="is a symlink to"):
        bench_registry.registry_read_path()


def test_both_read_the_registry_wt_registry_names(home, monkeypatch, tmp_path):
    """WT_REGISTRY outranks MODELMAN_REGISTRY in both readers, as it does in
    wt. If one reader still preferred the older name, wt would write one file
    while that tool read another."""
    _write(home / ".config" / "local-ai" / "registry.toml")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(_write(tmp_path / "old.toml")))
    named = _write(tmp_path / "new.toml")
    monkeypatch.setenv("WT_REGISTRY", str(named))
    assert _both_read() == named


def test_neither_falls_back_past_wt_registry(home, monkeypatch, tmp_path):
    """A WT_REGISTRY that names no file is an error in both readers, not a
    reason to read the default registry: a scratch run with a mistyped path
    would otherwise act on the real one."""
    _write(home / ".config" / "local-ai" / "registry.toml")
    monkeypatch.setenv("WT_REGISTRY", str(tmp_path / "scratch.toml"))
    with pytest.raises(modelman_registry.RegistryNotFoundError):
        modelman_registry._registry_read_path()
    with pytest.raises(bench_registry.RegistryError, match="Registry file not found"):
        bench_registry.registry_read_path()
