"""Shared TOML I/O helpers used by registry.py and state.py: recursively
dropping None values (TOML has no null type) and atomic writes (temp file
+ rename) so a crash mid-write never leaves a corrupt config on disk."""

import tomllib

import pytest

from modelman._toml_io import atomic_write_toml, drop_none


def test_drop_none_strips_nested_none_values():
    payload = {"a": 1, "b": None, "c": {"d": None, "e": 2}, "f": [{"g": None, "h": 3}]}
    assert drop_none(payload) == {"a": 1, "c": {"e": 2}, "f": [{"h": 3}]}


def test_atomic_write_toml_round_trips_and_creates_parent_dirs(tmp_path):
    target = tmp_path / "nested" / "registry.toml"

    atomic_write_toml({"models": [{"id": "ollama/x"}]}, target)

    assert target.exists()
    with open(target, "rb") as f:
        assert tomllib.load(f) == {"models": [{"id": "ollama/x"}]}


def test_atomic_write_toml_leaves_no_tmp_file_on_dump_failure(tmp_path, monkeypatch):
    import modelman._toml_io as toml_io

    def _boom(*args, **kwargs):
        raise ValueError("dump failed")

    monkeypatch.setattr(toml_io.tomli_w, "dump", _boom)
    target = tmp_path / "registry.toml"

    with pytest.raises(ValueError, match="dump failed"):
        atomic_write_toml({"a": 1}, target)

    assert not target.exists()
    assert list(tmp_path.iterdir()) == []
