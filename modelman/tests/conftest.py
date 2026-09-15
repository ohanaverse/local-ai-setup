"""Shared pytest fixtures."""

import subprocess
import urllib.error
from typing import Any
from unittest.mock import MagicMock

import pytest


def _fake_ollama_runner(args: list[str], **kwargs: Any) -> subprocess.CompletedProcess[str]:
    """Closed, deterministic runner for tests that don't inject a runner.

    Returns "not found" so is_downloaded() returns False, list_local()
    returns [], size_of() returns None, and auto_detect_model_info()
    returns {} — exactly the hermetic behavior CI sees when no ollama
    binary is installed. Tests that assert on runner behavior pass an
    explicit runner= and never call this default.
    """
    return subprocess.CompletedProcess(
        args=args,
        returncode=1,
        stdout="",
        stderr="Error: model not found",
    )


@pytest.fixture(autouse=True)
def _never_restart_live_proxy(monkeypatch):
    """Tests that apply exposes must not bounce the user's live LiteLLM
    proxy: restart_litellm_proxy() runs `launchctl kickstart -k
    gui/$(id -u)/local.litellm.proxy` on macOS, which kills in-flight LLM
    requests from agents (pi, Claude) that route through localhost:4000.
    Point it at a no-op shell command; tests that specifically exercise
    restart behavior (test_litellm.py) monkeypatch the env var themselves."""
    monkeypatch.setenv("MODELMAN_LITELLM_RESTART_CMD", "true")


@pytest.fixture(autouse=True)
def _never_call_real_ollama(monkeypatch):
    """The full suite must never shell out to the user's live `ollama`
    daemon. Redirect the module-level default runners in
    providers/ollama.py and ollama_caps.py to a closed 'not found'
    result."""
    monkeypatch.setattr("modelman.providers.ollama._default_runner", _fake_ollama_runner)
    monkeypatch.setattr("modelman.ollama_caps._default_runner", _fake_ollama_runner)
    # local_control's availability probe (issue #65) would `ollama ps` and
    # HTTP-probe localhost:8000/8001 in the marker-matches-idempotent path;
    # a False probe just means "full restart", which is what tests mocking
    # stop/isolate expect anyway. Tests of the probe itself patch
    # _probe_running/_ollama_loaded_names explicitly.
    monkeypatch.setattr("modelman.local_control._ollama_loaded_names", lambda: [])
    monkeypatch.setattr("modelman.local_control._http_models_ids", lambda url, timeout=2.0: [])
    # TODO(next task): drop this try/except once backends/ollama.py exists
    # and patch it unconditionally like the lines above. `raising=False`
    # (as the brief for this task originally specified) only suppresses a
    # missing *attribute* on an existing module — pytest's monkeypatch
    # still unconditionally imports the dotted module path before that
    # check runs, so a module that doesn't exist yet raises ImportError
    # regardless of raising=False. Confirmed against this repo's pytest
    # (9.1.1): `_pytest.monkeypatch.derive_importpath` calls `resolve()`
    # unguarded, and `raising` only gates the later `getattr` call;
    # `resolve()` itself re-wraps the ModuleNotFoundError as a plain
    # ImportError, so the except clause must catch ImportError (the base
    # class), not just ModuleNotFoundError.
    try:
        monkeypatch.setattr(
            "modelman.providers.lifecycle.backends.ollama._loaded_model_names",
            lambda: [],
        )
        monkeypatch.setattr(
            "modelman.providers.lifecycle.backends.ollama.subprocess.run",
            _fake_ollama_runner,
        )
    except ImportError:
        pass


_real_subprocess_run = subprocess.run


def _fake_launchctl_run(cmd, *args, **kwargs):
    """Intercept only launchctl invocations; delegate everything else to
    the real subprocess.run.

    `subprocess` is one shared module object — `monkeypatch.setattr` on
    ANY dotted path that resolves through it (e.g.
    "modelman.providers.lifecycle.launchd.subprocess.run") replaces
    `subprocess.run` globally for the whole interpreter, not just calls
    made from launchd.py. A flat `MagicMock(return_value=...)` here would
    silently neuter every other module's real subprocess.run call for the
    duration of every test (git in benchmark/agent/workspace.py, `pi
    --version` in runner.py, gates.py's test runners, litellm.py's
    restart command, ...), which is exactly the regression a full-suite
    run caught. Only launchctl calls need to be fake here — nothing else
    the test suite exercises should ever shell out to it.
    """
    argv = cmd if isinstance(cmd, list) else [cmd]
    if argv and argv[0] == "launchctl":
        return subprocess.CompletedProcess(cmd, 0)
    return _real_subprocess_run(cmd, *args, **kwargs)


@pytest.fixture(autouse=True)
def _never_touch_live_providers(monkeypatch):
    """The full suite must never poll a real localhost port, bounce a real
    LaunchAgent, or signal a real pid while exercising the new lifecycle
    primitives (probe/launchd/pidproc) or the backends a later task builds
    on top of them. probe/launchd/pidproc already exist after this task,
    so their patches are hermetic now; the rest target modules a later
    task creates."""
    # Like `subprocess` above, `urllib.request` and `os` are each one
    # shared module object — this patches urlopen/kill globally for the
    # whole interpreter, not just calls made from probe.py/pidproc.py.
    # Harmless today (probe.py is the only urlopen call site besides
    # local_process.py, which shares its fate intentionally, and
    # pidproc.py is the only os.kill call site in this codebase — grepped
    # to confirm), but a future module that calls the real urlopen/kill
    # directly would be silently neutered here too; if that ever bites,
    # give it the same real-delegating wrapper `_fake_launchctl_run` uses
    # above instead of widening this comment.
    monkeypatch.setattr(
        "modelman.providers.lifecycle.probe.urllib.request.urlopen",
        MagicMock(side_effect=urllib.error.URLError("hermetic test")),
    )
    monkeypatch.setattr(
        "modelman.providers.lifecycle.launchd.subprocess.run",
        _fake_launchctl_run,
    )
    monkeypatch.setattr(
        "modelman.providers.lifecycle.pidproc.os.kill",
        lambda *a, **k: None,
    )


@pytest.fixture
def stub_ollama_caps(monkeypatch):
    """Make ModelForm submit / model-screen flows headless-portable.

    The form submit path calls auto_detect_model_info(name), which shells
    out to `ollama show <name>` to populate LiteLLM model_info. A clean CI
    runner has no ollama binary, so these pure form-submit tests fail on
    FileNotFoundError. None of the tests that hit this path assert on the
    model_info content, so stubbing it to {} keeps them meaningful without
    requiring ollama to be installed.
    """
    monkeypatch.setattr("modelman.screens.forms.auto_detect_model_info", lambda name: {})


@pytest.fixture
def mock_runner():
    """A factory that returns a fake subprocess runner.

    Usage:
        def test_x(mock_runner):
            runner = mock_runner(returncode=0, stdout="hello")
            # ... call code that uses `runner` ...
            runner.assert_called_with(["some", "command"])
    """

    def _factory(returncode: int = 0, stdout: str = "", stderr: str = ""):
        runner = MagicMock()
        result = MagicMock()
        result.returncode = returncode
        result.stdout = stdout
        result.stderr = stderr
        runner.return_value = result
        return runner

    return _factory
