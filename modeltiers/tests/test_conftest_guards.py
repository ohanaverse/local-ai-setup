"""The autouse guard in conftest.py does what the suite relies on."""

import os
import subprocess
import urllib.error
import urllib.request
from pathlib import Path

import pytest

from modeltiers import paths


def test_urlopen_is_blocked() -> None:
    with pytest.raises(urllib.error.URLError, match="hermetic"):
        urllib.request.urlopen("https://models.dev/api.json")


def test_subprocess_run_is_blocked() -> None:
    with pytest.raises(AssertionError, match="hermetic"):
        subprocess.run(["wt", "model", "list", "--json"])


def test_every_path_is_under_tmp_path(tmp_path: Path) -> None:
    for name in ("HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "MODELTIERS_OUT", "MODELTIERS_CACHE"):
        assert Path(os.environ[name]).is_relative_to(tmp_path), name
    assert paths.output_path().is_relative_to(tmp_path)
    assert paths.cache_dir().is_relative_to(tmp_path)
    assert Path.home().is_relative_to(tmp_path)
