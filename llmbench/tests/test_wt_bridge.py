"""The `wt warm` bridge: the one wt call llmbench makes."""

import subprocess

import pytest

from llmbench import wt_bridge


def _cp(stdout="", returncode=0, stderr=""):
    return subprocess.CompletedProcess(args=[], returncode=returncode, stdout=stdout, stderr=stderr)


@pytest.fixture
def wt_on_path(monkeypatch):
    monkeypatch.setattr(wt_bridge.shutil, "which", lambda name: "/usr/local/bin/wt")


def test_warm_runs_wt_warm_and_reports_its_failure(monkeypatch, wt_on_path):
    """`wt warm <provider> <model>` is the keyed warmup llmbench cannot do
    itself (#256). A failure raises with wt's own line, which names the fix,
    and the timeout outlasts wt's warmup budget."""
    seen = []
    result = {"cp": _cp("Qwen-4bit is loaded on omlx\n")}

    def run(argv, **kwargs):
        seen.append((argv, kwargs.get("timeout")))
        return result["cp"]

    monkeypatch.setattr(wt_bridge.subprocess, "run", run)

    wt_bridge.warm("omlx", "Qwen-4bit")
    assert seen == [(["wt", "warm", "omlx", "Qwen-4bit"], wt_bridge.WARM_TIMEOUT)]
    assert wt_bridge.WARM_TIMEOUT > 600

    result["cp"] = _cp(returncode=1, stderr="Error: x wants an API key\nwt: x wants an API key\n")
    with pytest.raises(wt_bridge.WtBridgeError, match="^x wants an API key$"):
        wt_bridge.warm("omlx", "Qwen-4bit")


def test_warm_without_wt_on_path(monkeypatch):
    monkeypatch.setattr(wt_bridge.shutil, "which", lambda name: None)
    with pytest.raises(wt_bridge.WtNotFoundError, match="make install"):
        wt_bridge.warm("omlx", "Qwen-4bit")


@pytest.mark.parametrize(
    ("exc", "want", "message"),
    [
        (
            subprocess.TimeoutExpired(cmd=["wt"], timeout=630),
            wt_bridge.WtBridgeTimeoutError,
            "wt warm timed out after 630s",
        ),
        (FileNotFoundError("wt"), wt_bridge.WtNotFoundError, "wt not found on PATH"),
        (PermissionError("denied"), wt_bridge.WtBridgeError, "wt warm could not run"),
    ],
    ids=["timeout", "vanished", "not-executable"],
)
def test_warm_turns_a_subprocess_failure_into_a_bridge_error(
    monkeypatch, wt_on_path, exc, want, message
):
    """Every way the subprocess can fail reaches the caller as a
    WtBridgeError: the omlx backend catches that one type and turns it into
    the lifecycle's error envelope instead of a traceback."""

    def run(argv, **kwargs):
        raise exc

    monkeypatch.setattr(wt_bridge.subprocess, "run", run)
    with pytest.raises(want, match=message):
        wt_bridge.warm("omlx", "Qwen-4bit")


def test_msg_falls_back_when_wt_printed_nothing():
    assert wt_bridge._msg(_cp(returncode=3), "wt exited 3") == "wt exited 3"
    assert wt_bridge._msg(_cp(stdout="only stdout\n", returncode=1), "x") == "only stdout"
