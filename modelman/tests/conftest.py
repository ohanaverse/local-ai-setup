"""Shared pytest fixtures."""

import os
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
def _default_litellm_config(monkeypatch, tmp_path):
    """start_local_model's auto-expose (2026-09-15 local-model visibility
    design) resolves an unset litellm_path via default_litellm_config_path(),
    which would otherwise read/write the developer's real
    ~/.config/litellm/config.yaml whenever a test starts a ready, non-cloud
    local model without passing its own litellm_path. Point the default at
    a scratch file instead; tests that pass litellm_path explicitly are
    unaffected — that argument always wins over this env-var default.
    """
    path = tmp_path / "auto-litellm-config.yaml"
    path.write_text("model_list: []\n")
    monkeypatch.setenv("MODELMAN_LITELLM_CONFIG", str(path))


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
    monkeypatch.setattr(
        "modelman.providers.lifecycle.backends.ollama._loaded_model_names",
        lambda: [],
    )


_real_subprocess_run = subprocess.run

# argv[0] basenames (plus the "mlx_lm.*" family) the suite must NEVER
# actually execute: every one of them either drives a live local model
# provider on this machine or bounces a real LaunchAgent. `launchctl`
# would restart the user's LiteLLM proxy; `omlx stop` / `mtplx stop` /
# `mlx_lm.server` would tear down or spawn a real multi-GB local model
# an agent may be using mid-request.
_FAKE_BINARIES = frozenset({"launchctl", "omlx", "mtplx", "ollama"})
_FAKE_BINARY_PREFIXES = ("mlx_lm.",)


def _should_fake(argv0: str) -> bool:
    """Match on the BASENAME, not the whole argv[0]: mtplx and mlx_lm.*
    are invoked through `binaries.require_binary()`/`resolve_mlx_lm_bin()`,
    which return absolute paths (e.g.
    /opt/homebrew/Cellar/omlx/0.10.0/libexec/bin/mlx_lm.server)."""
    name = os.path.basename(argv0)
    return name in _FAKE_BINARIES or name.startswith(_FAKE_BINARY_PREFIXES)


def _fake_provider_run(cmd, *args, **kwargs):
    """Intercept every live-provider binary invocation; delegate everything
    else to the real subprocess.run.

    `subprocess` is one shared module object — `monkeypatch.setattr` on
    ANY dotted path that resolves through it (e.g.
    "modelman.providers.lifecycle.launchd.subprocess.run") replaces
    `subprocess.run` globally for the whole interpreter, not just calls
    made from launchd.py. That cuts both ways:

    - A flat `MagicMock(return_value=...)` here would silently neuter
      every other module's real subprocess.run call for the duration of
      every test (git in benchmark/agent/workspace.py, `pi --version` in
      runner.py, gates.py's test runners, litellm.py's restart command,
      ...), which is exactly the regression a full-suite run caught —
      hence the real-delegating fallback below.
    - Conversely, ONE global patch is all the interception there is: a
      per-module patch of `backends.omlx.subprocess.run` would be
      overwritten by whichever autouse fixture patched the shared
      `subprocess.run` last. So the allow-list has to name every binary
      any backend shells out to, not just launchctl — otherwise
      omlx/mtplx/mlx_lm calls fall through to the real binary whenever a
      test forgets to stub them explicitly.

    `ollama` is routed through `_fake_ollama_runner` rather than the
    generic success below so it keeps the closed "not found" semantics
    `_never_call_real_ollama` established for the provider-level runners.
    """
    argv = list(cmd) if isinstance(cmd, list | tuple) else [cmd]
    argv0 = str(argv[0]) if argv else ""
    if _should_fake(argv0):
        if os.path.basename(argv0) == "ollama":
            return _fake_ollama_runner(argv, **kwargs)
        return subprocess.CompletedProcess(cmd, 0, stdout="", stderr="")
    return _real_subprocess_run(cmd, *args, **kwargs)


@pytest.fixture(autouse=True)
def _never_touch_live_providers(monkeypatch):
    """The full suite must never poll a real localhost port, bounce a real
    LaunchAgent, signal a real pid, or drive a real provider binary while
    exercising the lifecycle primitives (probe/launchd/pidproc) and the
    backends built on top of them."""
    # Like `subprocess` above, `urllib.request` and `os` are each one
    # shared module object — this patches urlopen/kill globally for the
    # whole interpreter, not just calls made from probe.py/pidproc.py.
    # Harmless today (probe.py is the only urlopen call site besides
    # local_process.py, which shares its fate intentionally, and
    # pidproc.py is the only os.kill call site in this codebase — grepped
    # to confirm), but a future module that calls the real urlopen/kill
    # directly would be silently neutered here too; if that ever bites,
    # give it the same real-delegating wrapper `_fake_provider_run` uses
    # above instead of widening this comment.
    monkeypatch.setattr(
        "modelman.providers.lifecycle.probe.urllib.request.urlopen",
        MagicMock(side_effect=urllib.error.URLError("hermetic test")),
    )
    # One global patch, one allow-list: see `_fake_provider_run`. The
    # dotted path only picks the module object to reach `subprocess`
    # through — the replacement is process-wide, so it covers
    # omlx/mtplx/mlx_lm calls made from their own backend modules too.
    monkeypatch.setattr(
        "modelman.providers.lifecycle.launchd.subprocess.run",
        _fake_provider_run,
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
