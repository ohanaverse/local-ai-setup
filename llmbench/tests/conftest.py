"""Shared pytest fixtures: the guards that keep the suite off this machine's
live providers and real config."""

import os
import subprocess
import urllib.error
from typing import Any
from unittest.mock import MagicMock

import pytest


def _fake_ollama_runner(args: list[str], **kwargs: Any) -> subprocess.CompletedProcess[str]:
    """Closed, deterministic `ollama`: "not found", as on a runner with no
    ollama binary installed."""
    return subprocess.CompletedProcess(
        args=args,
        returncode=1,
        stdout="",
        stderr="Error: model not found",
    )


_real_subprocess_run = subprocess.run

# argv[0] basenames (plus the "mlx_lm.*" family) the suite must NEVER
# actually execute: every one of them either drives a live local model
# provider on this machine or bounces a real LaunchAgent. `launchctl`
# would restart the user's LiteLLM proxy; `omlx stop` / `mtplx stop` /
# `mlx_lm.server` would tear down or spawn a real multi-GB local model
# an agent may be using mid-request; `wt warm` would load one.
_FAKE_BINARIES = frozenset({"launchctl", "omlx", "mtplx", "ollama", "wt"})
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

    `subprocess` is one shared module object, so `monkeypatch.setattr` on ANY
    dotted path that resolves through it replaces `subprocess.run` for the
    whole interpreter. That cuts both ways:

    - A flat `MagicMock(return_value=...)` here would silently neuter every
      other module's real subprocess.run call (git in
      benchmark/agent/workspace.py, `pi --version` in runner.py, gates.py's
      test runners), hence the real-delegating fallback below.
    - ONE global patch is all the interception there is, so the allow-list
      has to name every binary any backend shells out to, not just launchctl.

    `ollama` gets the closed "not found" answer instead of the generic
    success.
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
    """The suite must never poll a real localhost port, bounce a real
    LaunchAgent, signal a real pid, or drive a real provider binary."""
    # `urllib.request`, `subprocess` and `os` are each one shared module
    # object: these three patches are process-wide, whatever dotted path
    # reaches them.
    monkeypatch.setattr(
        "llmbench.providers.lifecycle.probe.urllib.request.urlopen",
        MagicMock(side_effect=urllib.error.URLError("hermetic test")),
    )
    monkeypatch.setattr(
        "llmbench.providers.lifecycle.launchd.subprocess.run",
        _fake_provider_run,
    )
    monkeypatch.setattr(
        "llmbench.providers.lifecycle.pidproc.os.kill",
        lambda *a, **k: None,
    )
    monkeypatch.setattr(
        "llmbench.providers.lifecycle.backends.ollama._loaded_model_names",
        lambda: [],
    )


@pytest.fixture(autouse=True)
def _no_real_config(monkeypatch, tmp_path):
    """Keep the registry and the latest-run pointers off the developer's real
    config home. A test that needs either sets the variable itself and wins."""
    monkeypatch.delenv("XDG_CONFIG_HOME", raising=False)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(tmp_path / "no-registry.toml"))
    monkeypatch.setenv("LLMBENCH_LATEST", str(tmp_path / "no-latest.toml"))
