"""Shared pytest fixtures."""

import os
import subprocess
import urllib.error
from typing import Any
from unittest.mock import MagicMock

import pytest


def write_litellm_config(config: dict, path) -> None:
    """Seed a LiteLLM config.yaml for tests.

    modelman no longer writes this file — wt owns every route write since
    2026-09-21 — but tests still need one on disk for the read-only helpers
    (`modelman usage`) and for call sites that resolve a `litellm_path`.
    Replaces the deleted `litellm.save_litellm_config` as a test fixture
    writer only; nothing in src/ writes YAML any more.
    """
    from ruamel.yaml import YAML

    with open(path, "w") as f:
        YAML(typ="safe").dump(config, f)


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
def _no_inherited_registry_override(monkeypatch):
    """Clear both names that point modelman at a registry outright.

    WT_REGISTRY outranks MODELMAN_REGISTRY, so a developer who exports it (to
    aim wt at a scratch registry) would otherwise send every test that sets
    MODELMAN_REGISTRY to that scratch file instead of the test's own. Clearing
    MODELMAN_REGISTRY too makes the starting point the same on every machine.
    A test that sets either name still wins.
    """
    monkeypatch.delenv("WT_REGISTRY", raising=False)
    monkeypatch.delenv("MODELMAN_REGISTRY", raising=False)


@pytest.fixture(autouse=True)
def _default_litellm_config(monkeypatch, tmp_path):
    """Point default_litellm_config_path() at a scratch config.yaml.

    Two readers resolve an unset litellm_path through it: `modelman usage`,
    and `sync_routes`, which skips wt entirely when that file is missing.
    Seeding an existing scratch file keeps every write path's sync observable
    through `wt_calls` and keeps tests off the developer's real
    ~/.config/litellm/config.yaml. WT_LITELLM_CONFIG is cleared because it
    outranks MODELMAN_LITELLM_CONFIG (wt's precedence). Tests that pass
    litellm_path explicitly are unaffected — that argument always wins.
    """
    scratch_dir = tmp_path / "_default_litellm_config"
    scratch_dir.mkdir()
    path = scratch_dir / "config.yaml"
    path.write_text("model_list: []\n")
    monkeypatch.delenv("WT_LITELLM_CONFIG", raising=False)
    monkeypatch.setenv("MODELMAN_LITELLM_CONFIG", str(path))


@pytest.fixture(autouse=True)
def _no_real_wt_config(monkeypatch, tmp_path):
    """Point modelman at an empty wt config directory.

    modelman reads wt's ~/.config/agent-wt for three things: the agent list
    (the model screen registers a native provider row per agent on mount,
    registry.sync_agent_providers), the usage/rotation state, and the
    config.toml `modelman migrate` imports from. Left at the default, a
    test's outcome depended on the developer's own wt setup — on a machine
    with agents configured, mounting the model screen rewrote the test's
    registry.toml, which CI (no wt config) never saw. A test that needs a wt
    config sets MODELMAN_WT_DIR (or, for migrate, MODELMAN_WT_CONFIG) itself
    and wins.
    """
    wt_dir = tmp_path / "no-agent-wt"
    monkeypatch.setenv("MODELMAN_WT_DIR", str(wt_dir))
    # `migrate --wt-config` has its own default and does not follow
    # MODELMAN_WT_DIR.
    monkeypatch.setenv("MODELMAN_WT_CONFIG", str(wt_dir / "config.toml"))


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
    # ...and the omlx probe's reads of /v1/models/status and /health (and the
    # ollama /api/tags read). None = no answer from any of them, so the omlx
    # probe goes on to `wt served` (stubbed in _never_call_real_wt) and then
    # to "was the connection refused?" (stubbed below): by default it cannot
    # say, and clears no flag.
    monkeypatch.setattr("modelman.local_control._http_json", lambda url, timeout=2.0: None)
    # ...and start_local_model's "is this ollama model pulled?" check, which
    # would otherwise GET the developer's real /api/tags — machine-dependent,
    # like the daemon check below. The tests of the check itself patch the
    # real function back in, with _http_json answering the tags.
    monkeypatch.setattr(
        "modelman.local_control._require_ollama_pulled", lambda provider, model: None
    )
    # ...and sync's "which local providers are installed here?" PATH lookup,
    # which would make provider rows depend on the machine running the suite.
    monkeypatch.setattr("modelman.sync._installed_local_providers", lambda: [])
    # ...and start_local_model's ollama daemon-reachability check, which would
    # otherwise GET the developer's real localhost:11434 — a daemon that is up
    # on the dev machine and down in CI, i.e. machine-dependent tests. Stubbed
    # to "answering"; the tests of the down path patch it to False.
    monkeypatch.setattr("modelman.local_control._http_answers", lambda url, timeout=2.0: True)
    # ...and the "is omlx positively down?" check stop_local_model and the
    # omlx probe make (#213), which would otherwise dial the developer's real
    # omlx port. Stubbed to "not refused" — the answer that clears no flag;
    # the tests of the check patch it, and tests/test_local_process.py tests
    # the real function.
    monkeypatch.setattr(
        "modelman.local_control._connection_refused", lambda url, timeout=2.0: False
    )
    monkeypatch.setattr(
        "llmbench.providers.lifecycle.backends.ollama._loaded_model_names",
        lambda: [],
    )

    def _no_network(url, **kwargs):
        raise RuntimeError("tests must not fetch ollama.com; pass runner=")

    monkeypatch.setattr("modelman.ollama_catalog._default_http_runner", _no_network)
    monkeypatch.setattr("modelman.ollama_catalog._default_ollama_runner", _fake_ollama_runner)


@pytest.fixture(autouse=True)
def _never_run_real_price_refresh(monkeypatch):
    """The suite must never let the on-mount price-refresh worker reach the
    OpenRouter pricing API. `_never_touch_live_providers` blocks
    `urllib.request`, but pricing.py fetches with `requests`, so that path is
    a separate gap: with a cloud model seeded, `should_run_price_refresh` is
    True and the worker does a real `requests.get(..., timeout=30)`. While
    that call is in flight, ModelScreen's Escape guard sees a running worker
    and shows ConfirmForceQuitDialog instead of ConfirmExitDialog, which made
    `test_escape_with_pending_shows_dialog_and_apply` fail on ~11 of 12 runs
    (issue #166). Stubbing the daily gate to False keeps the worker's code
    path but returns before the fetch, so no network I/O happens and the
    worker settles like any worker with no work."""
    monkeypatch.setattr(
        "modelman.pricing.should_run_price_refresh",
        lambda state, registry: False,
    )


@pytest.fixture(autouse=True)
def _never_call_real_wt(monkeypatch):
    """The suite must never run the real `wt` binary: it would rewrite the
    developer's real LiteLLM config.yaml and bounce their live proxy. Every
    bridge call goes through wt_bridge._run; replace it with a fake that
    answers the verbs modelman still uses. Tests that assert on the bridge
    itself monkeypatch _run again.

    Limits of this fake: `sync` succeeds with no outcomes and changed=False;
    `list` is a static empty routed set; `status` reports routing off;
    on/off/set succeed (bare `set` exits 1 like real wt); there is no
    partial-failure / exit-1 / file-level-failure case. Any other verb
    (e.g. the expose/unexpose removed in #179) fails the test loudly. Tests
    exercising error paths must override wt_bridge._run. Every argv tail is
    recorded and yielded (see the `wt_calls` fixture). The start/stop calls go
    through wt_bridge._run_wt, which fails the test loudly unless the test
    stubs it. wt_bridge.served_ids (`wt served`, the omlx probe's keyed read)
    goes through neither seam, so it is stubbed itself, to None — "wt cannot
    say" — and the tests that need an answer patch it."""
    import json

    from modelman import wt_bridge

    calls: list[list[str]] = []

    def fake(args, env=None, timeout=120):
        calls.append(list(args))
        if args[:1] == ["list"]:
            out = {"routed": []}
        elif args[:1] == ["status"]:
            if "--json" in args:
                out = {"enabled": False, "url": "", "api_key_set": False}
            else:
                return subprocess.CompletedProcess(
                    args=[],
                    returncode=0,
                    stdout="litellm: off\n  url: (unset)\n  api_key: (unset)\n",
                    stderr="",
                )
        elif args == ["set"]:
            # Mirrors real wt: `set` with neither --url nor --api-key errors.
            return subprocess.CompletedProcess(
                args=[],
                returncode=1,
                stdout="",
                stderr="Error: nothing to set: pass --url and/or --api-key\n",
            )
        elif args[:1] in (["on"], ["off"], ["set"]):
            return subprocess.CompletedProcess(args=[], returncode=0, stdout="", stderr="")
        elif args[:1] == ["sync"]:
            out = {"outcomes": [], "changed": False, "warnings": []}
        else:
            raise AssertionError(f"unexpected wt litellm call in tests: {args[:1]}")
        return subprocess.CompletedProcess(args=[], returncode=0, stdout=json.dumps(out), stderr="")

    monkeypatch.setattr(wt_bridge, "_run", fake)

    def no_wt(argv, timeout):
        raise AssertionError(
            f"unexpected `wt {' '.join(argv[:1])}` in tests: stub wt_bridge.start / "
            "start_plan / stop (or wt_bridge._run_wt) in the test that needs it"
        )

    monkeypatch.setattr(wt_bridge, "_run_wt", no_wt)
    monkeypatch.setattr(wt_bridge, "served_ids", lambda provider, timeout=0.0: None)
    return calls


@pytest.fixture
def wt_calls(_never_call_real_wt):
    """argv tails (after `wt litellm`) the autouse fake received this test."""
    return _never_call_real_wt


_real_subprocess_run = subprocess.run

# argv[0] basenames (plus the "mlx_lm.*" family) the suite must NEVER
# actually execute: every one of them either drives a live local model
# provider on this machine or bounces a real LaunchAgent. `launchctl`
# would restart the user's LiteLLM proxy; `omlx stop` / `mtplx stop` /
# `mlx_lm.server` would tear down or spawn a real multi-GB local model
# an agent may be using mid-request.
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

    `subprocess` is one shared module object — `monkeypatch.setattr` on
    ANY dotted path that resolves through it (e.g.
    "llmbench.providers.lifecycle.launchd.subprocess.run") replaces
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
        "llmbench.providers.lifecycle.probe.urllib.request.urlopen",
        MagicMock(side_effect=urllib.error.URLError("hermetic test")),
    )
    # One global patch, one allow-list: see `_fake_provider_run`. The
    # dotted path only picks the module object to reach `subprocess`
    # through — the replacement is process-wide, so it covers
    # omlx/mtplx/mlx_lm calls made from their own backend modules too.
    monkeypatch.setattr(
        "llmbench.providers.lifecycle.launchd.subprocess.run",
        _fake_provider_run,
    )
    monkeypatch.setattr(
        "llmbench.providers.lifecycle.pidproc.os.kill",
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


# ---------------------------------------------------------------------------
# Shared-artifact / path-spelling helpers (#251)
#
# These live in the root conftest, not tests/test_providers/conftest.py where
# they started, because neither is provider-specific: the delete guard and the
# spelling table below are about the registry and the filesystem, and every
# reader that resolves a path (queue.py, the provider delete paths) uses them.
# A fixture is only visible to tests whose directory conftest pytest has
# loaded, so keeping them beside one provider's tests would make their
# availability depend on the argv spelling of a run. In the root conftest they
# are loaded for every collection, whatever order the arguments come in.
# ---------------------------------------------------------------------------


@pytest.fixture
def shared_owner():
    """Ask the shared-artifact guard, as the delete queue does: the id of the
    entry that owns something deleting `deleting` would remove, or None.

    `deleting` and `other` are the only two models in the registry, with one
    default provider row for each provider id they use. `provider` answers
    for `deleting`'s row; an entry on another row is asked through a provider
    built from that row."""

    def owner(provider, deleting, other):
        from modelman.registry import (
            ProviderEntry,
            Registry,
            find_shared_artifact_owner,
            model_entry_to_variant,
        )

        registry = Registry(
            providers=[
                ProviderEntry(id=provider_id, name=provider_id, location="local")
                for provider_id in dict.fromkeys((deleting.provider_id, other.provider_id))
            ],
            models=[deleting, other],
        )
        found = find_shared_artifact_owner(registry, provider, model_entry_to_variant(deleting))
        return found.id if found else None

    return owner


def _tilde(tmp_path, directory, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    return "~/" + str(directory.relative_to(tmp_path))


def _symlink(tmp_path, directory, monkeypatch):
    link = tmp_path / "link-to-model"
    link.symlink_to(directory, target_is_directory=True)
    return str(link)


def _other_case(tmp_path, directory, monkeypatch):
    swapped = directory.with_name(directory.name.swapcase())
    if not swapped.exists():
        pytest.skip("case-sensitive filesystem: another letter case is another directory")
    return str(swapped)


# #235: ways to name one directory that are not the string the provider
# derives for it. Each takes (tmp_path, directory, monkeypatch) and returns
# the spelling; `directory` is under tmp_path and already exists.
SPELLINGS = {
    "other-case": _other_case,
    "tilde": _tilde,
    "trailing-slash": lambda tmp_path, directory, monkeypatch: f"{directory}/",
    "dot-dot": lambda tmp_path, directory, monkeypatch: str(
        directory.parent / "x" / ".." / directory.name
    ),
    "symlink": _symlink,
}


@pytest.fixture(params=sorted(SPELLINGS))
def respell(request, tmp_path, monkeypatch):
    """A function giving another spelling of a directory under tmp_path."""
    return lambda directory: SPELLINGS[request.param](tmp_path, directory, monkeypatch)
