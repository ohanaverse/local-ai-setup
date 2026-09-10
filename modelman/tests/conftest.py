"""Shared pytest fixtures."""

import subprocess
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
