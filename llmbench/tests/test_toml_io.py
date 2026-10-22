"""Atomic TOML writes: a crash mid-write never leaves a truncated file."""

import tomllib

import pytest

import llmbench._toml_io as toml_io
from llmbench._toml_io import atomic_write_toml


def test_atomic_write_toml_round_trips_and_creates_parent_dirs(tmp_path):
    target = tmp_path / "nested" / "latest.toml"

    atomic_write_toml({"last_run_dir": "/results"}, target)

    with open(target, "rb") as f:
        assert tomllib.load(f) == {"last_run_dir": "/results"}


def test_atomic_write_toml_leaves_no_tmp_file_on_dump_failure(tmp_path, monkeypatch):
    def _boom(*args, **kwargs):
        raise ValueError("dump failed")

    monkeypatch.setattr(toml_io.tomli_w, "dump", _boom)
    target = tmp_path / "latest.toml"

    with pytest.raises(ValueError, match="dump failed"):
        atomic_write_toml({"a": 1}, target)

    assert not target.exists()
    assert list(tmp_path.glob(f".{target.name}.*")) == []
