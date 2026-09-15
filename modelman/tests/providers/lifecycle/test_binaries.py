from unittest.mock import patch

import pytest

from modelman.providers.lifecycle.binaries import require_binary, resolve_mlx_lm_bin
from modelman.providers.lifecycle.envelope import LifecycleError


def test_require_binary_returns_which_result():
    """require_binary must return shutil.which's resolved path verbatim."""
    with patch(
        "modelman.providers.lifecycle.binaries.shutil.which",
        return_value="/usr/local/bin/mtplx",
    ):
        assert require_binary("mtplx") == "/usr/local/bin/mtplx"


def test_require_binary_raises_when_not_on_path():
    """A missing binary must raise LifecycleError naming the binary, not
    return None for a caller to forget to check."""
    with (
        patch("modelman.providers.lifecycle.binaries.shutil.which", return_value=None),
        pytest.raises(LifecycleError, match="mtplx binary not found on PATH"),
    ):
        require_binary("mtplx")


def test_resolve_mlx_lm_bin_prefers_env_override(monkeypatch, tmp_path):
    """MLX_LM_BIN_DIR must win over the Cellar glob when set and the
    binary it names exists and is executable."""
    override = tmp_path / "mlx_lm.server"
    override.write_text("#!/bin/sh\n")
    override.chmod(0o755)
    monkeypatch.setenv("MLX_LM_BIN_DIR", str(tmp_path))
    with patch("modelman.providers.lifecycle.binaries.glob.glob") as mock_glob:
        result = resolve_mlx_lm_bin("server")
    assert result == str(override)
    mock_glob.assert_not_called()


def test_resolve_mlx_lm_bin_falls_back_to_cellar_glob_without_env(monkeypatch):
    """Without MLX_LM_BIN_DIR (or with it unset/unusable), resolution must
    fall back to globbing the Cellar path."""
    monkeypatch.delenv("MLX_LM_BIN_DIR", raising=False)
    matches = ["/opt/homebrew/Cellar/omlx/0.9.0/libexec/bin/mlx_lm.server"]
    with (
        patch("modelman.providers.lifecycle.binaries.glob.glob", return_value=matches),
        patch("modelman.providers.lifecycle.binaries.os.access", return_value=True),
    ):
        assert resolve_mlx_lm_bin("server") == matches[0]


def test_resolve_mlx_lm_bin_sorts_versions_numerically_not_lexicographically(monkeypatch):
    """This is the whole reason a hand-rolled version key is used instead
    of a plain sort: a lexicographic string sort would put '0.10.0'
    BEFORE '0.9.0' (since '1' < '9' as characters), which would silently
    resolve to an older omlx install than the one actually newest. The
    numeric-tuple key must sort 0.10.0 after 0.9.0."""
    monkeypatch.delenv("MLX_LM_BIN_DIR", raising=False)
    older = "/opt/homebrew/Cellar/omlx/0.9.0/libexec/bin/mlx_lm.server"
    newer = "/opt/homebrew/Cellar/omlx/0.10.0/libexec/bin/mlx_lm.server"
    with (
        # Glob order deliberately does NOT match version order, so a test
        # that accidentally relied on glob's own ordering would not pass.
        patch("modelman.providers.lifecycle.binaries.glob.glob", return_value=[newer, older]),
        patch("modelman.providers.lifecycle.binaries.os.access", return_value=True),
    ):
        assert resolve_mlx_lm_bin("server") == newer


def test_resolve_mlx_lm_bin_skips_non_executable_matches(monkeypatch):
    """A glob match that isn't executable (e.g. a stale/partial install)
    must be skipped rather than returned."""
    monkeypatch.delenv("MLX_LM_BIN_DIR", raising=False)
    matches = ["/opt/homebrew/Cellar/omlx/0.9.0/libexec/bin/mlx_lm.server"]
    with (
        patch("modelman.providers.lifecycle.binaries.glob.glob", return_value=matches),
        patch("modelman.providers.lifecycle.binaries.os.access", return_value=False),
        pytest.raises(LifecycleError, match="omlx not found"),
    ):
        resolve_mlx_lm_bin("server")


def test_resolve_mlx_lm_bin_raises_exact_bash_message_when_nothing_found(monkeypatch):
    """No env override and no Cellar match must raise LifecycleError with
    bash's exact message text, since this is user-facing install
    guidance."""
    monkeypatch.delenv("MLX_LM_BIN_DIR", raising=False)
    with (
        patch("modelman.providers.lifecycle.binaries.glob.glob", return_value=[]),
        pytest.raises(
            LifecycleError,
            match=(
                r"omlx not found — install via 'brew install omlx', or set "
                r"MLX_LM_BIN_DIR to a directory containing mlx_lm\.\* tools"
            ),
        ),
    ):
        resolve_mlx_lm_bin("server")
