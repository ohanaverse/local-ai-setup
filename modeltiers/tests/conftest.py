"""Shared fixtures: the guards that keep the suite off the network, off wt and
out of the real config and cache directories, plus the fixture-file helpers."""

import subprocess
import urllib.error
import urllib.request
from collections.abc import Callable
from pathlib import Path
from typing import Any

import pytest

FIXTURES = Path(__file__).parent / "fixtures"


def _no_network(*args: Any, **kwargs: Any) -> Any:
    raise urllib.error.URLError("hermetic test: no network")


def _no_subprocess(*args: Any, **kwargs: Any) -> Any:
    raise AssertionError(f"hermetic test: subprocess.run called with {args!r}")


@pytest.fixture(autouse=True)
def _hermetic(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    """No test may fetch, run wt, or read or write the real output file or the
    real snapshot cache. `urllib.request` and `subprocess` are each one shared
    module object, so these two patches hold for the whole process. A test
    that needs either replaces it with its own fake."""
    monkeypatch.setattr(urllib.request, "urlopen", _no_network)
    monkeypatch.setattr(subprocess, "run", _no_subprocess)
    home = tmp_path / "home"
    home.mkdir()
    monkeypatch.setenv("HOME", str(home))
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg-config"))
    monkeypatch.setenv("XDG_CACHE_HOME", str(tmp_path / "xdg-cache"))
    monkeypatch.setenv("MODELTIERS_OUT", str(tmp_path / "out" / "tiers.toml"))
    monkeypatch.setenv("MODELTIERS_CACHE", str(tmp_path / "cache"))


@pytest.fixture
def read_fixture() -> Callable[[str], bytes]:
    """The bytes of one file under tests/fixtures/."""

    def read(name: str) -> bytes:
        return (FIXTURES / name).read_bytes()

    return read
