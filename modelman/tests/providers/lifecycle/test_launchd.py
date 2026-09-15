import subprocess
from pathlib import Path
from unittest.mock import patch

from modelman.providers.lifecycle.launchd import kickstart, load, unload


def test_kickstart_calls_launchctl_with_gui_uid_label():
    """kickstart must build the exact `launchctl kickstart -k
    gui/<uid>/<label>` argv bash's helpers use — a wrong argv shape would
    silently no-op against the wrong service."""
    with (
        patch("modelman.providers.lifecycle.launchd.subprocess.run") as mock_run,
        patch("modelman.providers.lifecycle.launchd.os.getuid", return_value=501),
    ):
        mock_run.return_value = subprocess.CompletedProcess([], 0)
        result = kickstart("com.ollama.ollama")
    mock_run.assert_called_once()
    args = mock_run.call_args.args[0]
    assert args == ["launchctl", "kickstart", "-k", "gui/501/com.ollama.ollama"]
    assert result is True


def test_kickstart_swallows_nonzero_exit():
    """A non-zero launchctl exit (e.g. the service isn't loaded) must come
    back as False, matching bash's `2>/dev/null || true` — never raise."""
    with patch("modelman.providers.lifecycle.launchd.subprocess.run") as mock_run:
        mock_run.return_value = subprocess.CompletedProcess([], 1)
        assert kickstart("com.ollama.ollama") is False


def test_kickstart_swallows_oserror():
    """A missing launchctl binary (OSError/FileNotFoundError) must also
    come back as False rather than propagating a traceback."""
    with patch(
        "modelman.providers.lifecycle.launchd.subprocess.run",
        side_effect=FileNotFoundError("launchctl not found"),
    ):
        assert kickstart("com.ollama.ollama") is False


def test_load_calls_launchctl_load_dash_w():
    """load must call `launchctl load -w <plist>` with the plist path as a
    plain string argument."""
    plist = Path("/tmp/example.plist")
    with patch("modelman.providers.lifecycle.launchd.subprocess.run") as mock_run:
        mock_run.return_value = subprocess.CompletedProcess([], 0)
        result = load(plist)
    args = mock_run.call_args.args[0]
    assert args == ["launchctl", "load", "-w", str(plist)]
    assert result is True


def test_load_swallows_failure():
    """A failed load (nonzero exit or OSError) must return False."""
    plist = Path("/tmp/example.plist")
    with patch(
        "modelman.providers.lifecycle.launchd.subprocess.run",
        side_effect=OSError("boom"),
    ):
        assert load(plist) is False


def test_unload_calls_launchctl_unload():
    """unload must call `launchctl unload <plist>` (no -w flag, matching
    bash's unload usage)."""
    plist = Path("/tmp/example.plist")
    with patch("modelman.providers.lifecycle.launchd.subprocess.run") as mock_run:
        mock_run.return_value = subprocess.CompletedProcess([], 0)
        result = unload(plist)
    args = mock_run.call_args.args[0]
    assert args == ["launchctl", "unload", str(plist)]
    assert result is True


def test_unload_swallows_failure():
    """A failed unload (nonzero exit or OSError) must return False, never
    raise."""
    plist = Path("/tmp/example.plist")
    with patch("modelman.providers.lifecycle.launchd.subprocess.run") as mock_run:
        mock_run.return_value = subprocess.CompletedProcess([], 1)
        assert unload(plist) is False
