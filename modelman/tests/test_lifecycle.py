import json
from unittest.mock import MagicMock, patch

import pytest

from modelman.providers.lifecycle import LifecycleResult, isolate, stop, stop_all
from modelman.providers.lifecycle.backends.base import StartPlan

# NOTE: mtplx's own internals (resolve()'s registry-ambiguity logic, the
# --model-id pinning, the solo-restart early-raise in
# replace_own_occupant(), and the stop_and_wait() port-closed bug fix) now
# live in modelman.providers.lifecycle.backends.mtplx and are tested in
# tests/providers/lifecycle/backends/test_mtplx.py. The tests below cover
# only what still genuinely belongs to __init__.py: isolate()'s/stop()'s
# own dispatch control flow (delegating to MTPLX's methods for mtplx, to
# the bash helper for every other provider) and _stop_others/
# _delegate_isolate/_delegate_stop_all/stop_all/_main, all unchanged by
# this task.


def _mtplx_plan(model: str = "Some/Model") -> StartPlan:
    return StartPlan(model=model, direct_url="http://localhost:8003/v1/chat/completions")


# --- isolate("mtplx", ...) control flow ------------------------------------


def test_isolate_mtplx_keep_path_warms_without_restarting():
    """isolate() must keep semantics: when MTPLX is already serving the
    resolved model, it must (unless solo) stop every OTHER provider via
    _stop_others(keep="mtplx") and only warm the existing server — never
    call start()/wait_ready(). This is __init__.py's own control-flow
    contract, now expressed as calls into MTPLX's backend methods instead
    of the deleted private functions, and must stay byte-for-byte
    identical to today's behavior."""
    plan = _mtplx_plan()
    with (
        patch("modelman.providers.lifecycle.MTPLX.resolve", return_value=plan),
        patch("modelman.providers.lifecycle.MTPLX.already_serving", return_value=True),
        patch("modelman.providers.lifecycle._stop_others") as mock_stop_others,
        patch("modelman.providers.lifecycle.MTPLX.start") as mock_start,
        patch("modelman.providers.lifecycle.MTPLX.wait_ready") as mock_wait_ready,
        patch("modelman.providers.lifecycle.MTPLX.warm") as mock_warm,
    ):
        result = isolate("mtplx", "Some/Model")
    mock_stop_others.assert_called_once_with(keep="mtplx")
    mock_start.assert_not_called()
    mock_wait_ready.assert_not_called()
    mock_warm.assert_called_once_with(plan)
    assert result.ok is True
    assert result.provider == "mtplx"
    assert result.model == "Some/Model"
    assert result.direct_url == "http://localhost:8003/v1/chat/completions"


def test_isolate_mtplx_different_model_restarts():
    """A different model (or no server) must take the full restart path:
    mtplx is single-model-per-process, so serving model B means a stop +
    respawn, and (non-solo) _stop_others must tear mtplx down too (no
    keep)."""
    plan = _mtplx_plan()
    with (
        patch("modelman.providers.lifecycle.MTPLX.resolve", return_value=plan),
        patch("modelman.providers.lifecycle.MTPLX.already_serving", return_value=False),
        patch("modelman.providers.lifecycle._stop_others") as mock_stop_others,
        patch("modelman.providers.lifecycle.MTPLX.replace_own_occupant") as mock_replace,
        patch("modelman.providers.lifecycle.MTPLX.start") as mock_start,
        patch("modelman.providers.lifecycle.MTPLX.wait_ready") as mock_wait_ready,
        patch("modelman.providers.lifecycle.MTPLX.warm") as mock_warm,
    ):
        result = isolate("mtplx", "Some/Model")
    mock_stop_others.assert_called_once_with()
    mock_replace.assert_not_called()
    mock_start.assert_called_once_with(plan)
    mock_wait_ready.assert_called_once_with(plan)
    mock_warm.assert_called_once_with(plan)
    assert result.ok is True


def test_isolate_solo_different_model_stops_only_mtplx():
    """solo=True on a different-model isolate must stop only mtplx's own
    occupant (MTPLX.replace_own_occupant), never the sibling providers via
    _stop_others — this is the same-provider-only local-model lifecycle's
    whole point: starting mtplx must not tear down an unrelated
    ollama/omlx instance running alongside it."""
    plan = _mtplx_plan("org/new")
    with (
        patch("modelman.providers.lifecycle.MTPLX.resolve", return_value=plan),
        patch("modelman.providers.lifecycle.MTPLX.already_serving", return_value=False),
        patch("modelman.providers.lifecycle.MTPLX.replace_own_occupant") as mock_replace,
        patch("modelman.providers.lifecycle._stop_others") as mock_stop_others,
        patch("modelman.providers.lifecycle.MTPLX.start") as mock_start,
        patch("modelman.providers.lifecycle.MTPLX.wait_ready"),
        patch("modelman.providers.lifecycle.MTPLX.warm"),
    ):
        result = isolate("mtplx", "org/new", solo=True)
    mock_replace.assert_called_once_with(plan)
    mock_stop_others.assert_not_called()
    mock_start.assert_called_once_with(plan)
    assert result.ok is True


def test_isolate_solo_replace_own_occupant_failure_prevents_start():
    """A replace_own_occupant() failure (the solo-restart early-raise,
    tested directly in test_mtplx.py) must propagate immediately out of
    isolate(), which must never call start() afterward — matching today's
    behavior where a failed solo stop never falls through to spawning a
    new server."""
    from modelman.providers.lifecycle import LifecycleError

    plan = _mtplx_plan("org/new")
    with (
        patch("modelman.providers.lifecycle.MTPLX.resolve", return_value=plan),
        patch("modelman.providers.lifecycle.MTPLX.already_serving", return_value=False),
        patch(
            "modelman.providers.lifecycle.MTPLX.replace_own_occupant",
            side_effect=LifecycleError("mtplx binary not found on PATH"),
        ),
        patch("modelman.providers.lifecycle.MTPLX.start") as mock_start,
    ):
        result = isolate("mtplx", "org/new", solo=True)
    mock_start.assert_not_called()
    assert result.ok is False
    assert result.error == "mtplx binary not found on PATH"


def test_isolate_mtplx_keep_path_stop_others_failure_does_not_teardown():
    """A _stop_others(keep='mtplx') failure on the keep path must NOT
    trigger MTPLX.stop_and_wait() teardown: the mtplx server itself is
    healthy and untouched — only stopping the OTHER providers failed,
    which has nothing to do with mtplx's health and must not tear it
    down."""
    from modelman.providers.lifecycle import LifecycleError

    plan = _mtplx_plan()
    with (
        patch("modelman.providers.lifecycle.MTPLX.resolve", return_value=plan),
        patch("modelman.providers.lifecycle.MTPLX.already_serving", return_value=True),
        patch(
            "modelman.providers.lifecycle._stop_others",
            side_effect=LifecycleError("failed to stop other providers"),
        ),
        patch("modelman.providers.lifecycle.MTPLX.warm") as mock_warm,
        patch("modelman.providers.lifecycle.MTPLX.stop_and_wait") as mock_cleanup,
    ):
        result = isolate("mtplx", "Some/Model")
    mock_warm.assert_not_called()
    mock_cleanup.assert_not_called()
    assert result.ok is False


def test_isolate_mtplx_keep_path_tears_down_wedged_server_on_warmup_failure():
    """A keep-path warmup failure (server answers /v1/models but hangs or
    errors on chat completions — a wedged server) must tear the server
    down via MTPLX.stop_and_wait(), not leave it running.

    Without this, the keep branch never sets `started`, so the except
    block's `if started: MTPLX.stop_and_wait()` teardown never fires — the
    wedged server stays up forever, and every future isolate() call just
    re-probes it as 'serving', retries the same doomed warmup, and fails
    again with no path to recovery short of a manual `mtplx stop`."""
    from modelman.providers.lifecycle import LifecycleError

    plan = _mtplx_plan()
    with (
        patch("modelman.providers.lifecycle.MTPLX.resolve", return_value=plan),
        patch("modelman.providers.lifecycle.MTPLX.already_serving", return_value=True),
        patch("modelman.providers.lifecycle._stop_others") as mock_stop_others,
        patch(
            "modelman.providers.lifecycle.MTPLX.warm",
            side_effect=LifecycleError("failed to warm up"),
        ),
        patch("modelman.providers.lifecycle.MTPLX.stop_and_wait") as mock_cleanup,
    ):
        result = isolate("mtplx", "Some/Model")
    mock_stop_others.assert_called_once_with(keep="mtplx")
    mock_cleanup.assert_called_once()
    assert result.ok is False
    assert "failed to warm up" in (result.error or "")


def test_isolate_stops_serve_when_wait_ready_fails():
    """A half-started isolate (serve up, model load/warmup failed) must
    stop the spawned mtplx serve via MTPLX.stop_and_wait().

    Otherwise the orphan holds port 8003 and GPU/RAM while local_control
    clears the running-model marker on failure, so nothing tears it down —
    violating the one-local-model-at-a-time invariant this module exists
    to enforce."""
    from modelman.providers.lifecycle import LifecycleError

    plan = _mtplx_plan()
    with (
        patch("modelman.providers.lifecycle.MTPLX.resolve", return_value=plan),
        patch("modelman.providers.lifecycle.MTPLX.already_serving", return_value=False),
        patch("modelman.providers.lifecycle._stop_others"),
        patch("modelman.providers.lifecycle.MTPLX.start"),
        patch(
            "modelman.providers.lifecycle.MTPLX.wait_ready",
            side_effect=LifecycleError("timed out"),
        ),
        patch("modelman.providers.lifecycle.MTPLX.warm"),
        patch("modelman.providers.lifecycle.MTPLX.stop_and_wait") as mock_cleanup,
    ):
        result = isolate("mtplx", "Some/Model")
    mock_cleanup.assert_called_once()
    assert result.ok is False
    assert "timed out" in (result.error or "")


def test_isolate_fails_before_any_teardown_when_resolve_raises():
    """isolate('mtplx') must return an error envelope, not raise, when
    MTPLX.resolve() fails (e.g. no mtplx model configured) — and must not
    call _stop_others or MTPLX.stop_and_wait, since nothing was ever
    started."""
    from modelman.providers.lifecycle import LifecycleError

    with (
        patch(
            "modelman.providers.lifecycle.MTPLX.resolve",
            side_effect=LifecycleError("no mtplx model in the registry"),
        ),
        patch("modelman.providers.lifecycle._stop_others") as mock_stop_others,
        patch("modelman.providers.lifecycle.MTPLX.stop_and_wait") as mock_cleanup,
    ):
        result = isolate("mtplx")
    assert result.ok is False
    assert "no mtplx model" in (result.error or "")
    mock_stop_others.assert_not_called()
    mock_cleanup.assert_not_called()


def test_isolate_registry_error_returns_envelope_not_traceback():
    """A corrupt registry.toml surfaces from MTPLX.resolve() as a
    RegistryError (NOT a LifecycleError) — isolate()'s except clause must
    still catch it and return an ok=False envelope, never a raw
    traceback, since the bash shim and benchmark isolation parse stdout as
    JSON."""
    from modelman.registry import RegistryError

    with (
        patch(
            "modelman.providers.lifecycle.MTPLX.resolve",
            side_effect=RegistryError("corrupt"),
        ),
        patch("modelman.providers.lifecycle._stop_others") as mock_stop_others,
        patch("modelman.providers.lifecycle.MTPLX.stop_and_wait") as mock_cleanup,
    ):
        result = isolate("mtplx")
    assert result.ok is False
    assert "corrupt" in (result.error or "")
    # The failure happened before serve started: nothing to tear down,
    # and stop-others must not have run either (fail before any teardown).
    mock_stop_others.assert_not_called()
    mock_cleanup.assert_not_called()


def test_cli_registry_error_prints_json_envelope(capsys):
    """The CLI entry point must answer a corrupt registry with a JSON
    envelope on stdout and exit 1 — this is the bash-shim stdout contract
    that modelman/benchmark/isolation.py parses (invalid JSON there
    surfaces as the non-actionable 'isolation helper returned invalid
    JSON')."""
    from modelman.providers.lifecycle import _main
    from modelman.registry import RegistryError

    with patch(
        "modelman.providers.lifecycle.MTPLX.resolve",
        side_effect=RegistryError("corrupt"),
    ):
        code = _main(["isolate", "mtplx"])
    out = json.loads(capsys.readouterr().out)
    assert code == 1
    assert out["ok"] is False
    assert "corrupt" in out["error"]


def test_cli_prints_json_envelope(capsys):
    """The CLI's stdout on success must be exactly the JSON envelope and
    nothing else — stdout IS the contract the bash shim and
    modelman/benchmark/isolation.py parse. A stray traceback or empty
    stdout here surfaces downstream only as "isolation helper returned
    invalid JSON", not the real cause."""
    from modelman.providers.lifecycle import _main

    with patch(
        "modelman.providers.lifecycle.isolate",
        return_value=LifecycleResult("mtplx", "m", "http://localhost:8003/v1/chat/completions", True, None),
    ):
        rc = _main(["isolate", "mtplx"])
    out = capsys.readouterr().out
    assert rc == 0
    data = json.loads(out)
    assert data == {"provider": "mtplx", "model": "m", "direct_url": "http://localhost:8003/v1/chat/completions", "ok": True, "error": None}


# --- stop("mtplx") control flow --------------------------------------------


def test_stop_mtplx_returns_ok_true_on_clean_stop():
    with patch("modelman.providers.lifecycle.MTPLX.stop_and_wait", return_value=None):
        result = stop("mtplx")
    assert result.ok is True
    assert result.error is None
    assert result.provider == "mtplx"
    assert result.direct_url == "http://localhost:8003/v1/chat/completions"


def test_stop_mtplx_returns_ok_false_with_warning_as_error():
    """stop()'s mtplx branch must translate a MTPLX.stop_and_wait()
    warning into ok=False with that warning as the error text, matching
    the file-wide convention every other dispatch path
    (_delegate_isolate/_delegate_stop_all) already uses."""
    with patch(
        "modelman.providers.lifecycle.MTPLX.stop_and_wait",
        return_value="mtplx still listening on port 8003",
    ):
        result = stop("mtplx")
    assert result.ok is False
    assert result.error == "mtplx still listening on port 8003"


def test_stop_non_mtplx_returns_error_envelope_not_raise():
    """stop() must return a {"ok": false, "error": ...} LifecycleResult for
    any provider it doesn't (yet) implement, matching every other path in
    this module's JSON-envelope contract — not raise, which would crash
    _main() with an uncaught traceback instead of the envelope the CLI's
    usage string promises for `stop [provider]`."""
    result = stop("omlx")
    assert result.ok is False
    assert "omlx" in (result.error or "")


def test_stop_all_delegates_to_bash_helper_only():
    """stop_all() must delegate entirely to _delegate_stop_all() and return
    its result as-is, without also calling stop("mtplx") directly: the bash
    helper's stop-all mode already tears down mtplx via the shared
    mtplx_stop function, so a direct call here would double-stop it and
    (on a machine missing the mtplx binary) misreport overall failure even
    though every real provider was torn down cleanly. stop() itself is
    unaffected and is tested separately above."""
    with (
        patch("modelman.providers.lifecycle.stop") as mock_stop,
        patch("modelman.providers.lifecycle._delegate_stop_all", return_value=LifecycleResult("stop-all", "", "", True, None)) as mock_delegate,
    ):
        result = stop_all()
    mock_stop.assert_not_called()
    mock_delegate.assert_called_once()
    assert result.ok is True
    assert result.provider == "stop-all"


# --- non-mtplx delegation (unchanged by this task) -------------------------


def test_delegate_isolate_sets_env_var_for_mapped_providers():
    """ollama/omlx env overrides must use the exact variable names the bash
    helper reads; the magic-formula approach was wrong for providers like
    mlx_lm_server and never applied to MTPLX."""
    from modelman.providers.lifecycle import _delegate_isolate

    with patch("modelman.providers.lifecycle.shutil.which", return_value="/bin/llm-isolate-provider"), patch("modelman.providers.lifecycle.subprocess.run") as mock_run:
        mock_run.return_value.returncode = 0
        mock_run.return_value.stdout = json.dumps({"provider": "ollama", "model": "m", "direct_url": "http://localhost:11434/v1/chat/completions", "ok": True, "error": None})
        _delegate_isolate("ollama", "ornith-1.5:35b", ())
    env = mock_run.call_args.kwargs.get("env")
    assert env is not None
    assert env["LLM_ISOLATE_OLLAMA_MODEL"] == "ornith-1.5:35b"


def test_delegate_isolate_mtplx_uses_positional_arg_not_env_var():
    """MTPLX receives the model via the bash helper's positional arg, not an
    env var — this is the contract the shim now forwards to the lifecycle
    module."""
    from modelman.providers.lifecycle import _delegate_isolate

    with patch("modelman.providers.lifecycle.shutil.which", return_value="/bin/llm-isolate-provider"), patch("modelman.providers.lifecycle.subprocess.run") as mock_run:
        mock_run.return_value.returncode = 0
        mock_run.return_value.stdout = json.dumps({"provider": "mtplx", "model": "m", "direct_url": "http://localhost:8003/v1/chat/completions", "ok": True, "error": None})
        _delegate_isolate("mtplx", "org/repo", ())
    args = mock_run.call_args.args[0]
    assert args == ["/bin/llm-isolate-provider", "mtplx"]
    env = mock_run.call_args.kwargs.get("env")
    assert env is None


def test_delegate_isolate_forwards_extra_args():
    """mlx_lm_server target/draft positional args must pass through to the
    helper unchanged."""
    from modelman.providers.lifecycle import _delegate_isolate

    with patch("modelman.providers.lifecycle.shutil.which", return_value="/bin/llm-isolate-provider"), patch("modelman.providers.lifecycle.subprocess.run") as mock_run:
        mock_run.return_value.returncode = 0
        mock_run.return_value.stdout = json.dumps({"provider": "mlx_lm_server", "model": "m", "direct_url": "http://localhost:8001/v1/chat/completions", "ok": True, "error": None})
        _delegate_isolate("mlx_lm_server", None, ("org/target", "org/draft"))
    args = mock_run.call_args.args[0]
    assert args == ["/bin/llm-isolate-provider", "mlx_lm_server", "org/target", "org/draft"]
    assert mock_run.call_args.kwargs.get("env") is None


def test_stop_others_prefers_llm_isolate_helper_env(monkeypatch):
    """_stop_others must resolve the isolation helper via the
    LLM_ISOLATE_HELPER env var before PATH.

    The bash shim is routinely invoked by absolute path (the benchmark
    scripts resolve it via `dirname $0`) with bin/ NOT on PATH, so
    shutil.which() returns None there — without the env var, the mtplx
    isolate fails after the helper already stopped every other provider.
    The shim exports its own path so resolution never depends on PATH.

    shutil.which is stubbed to return a DIFFERENT path (not None): if
    `_stop_others` only fell back to PATH and happened to ignore the env
    var, this test would still see a call, just to the wrong helper —
    stubbing which() to a distinct value is what makes this test actually
    prove precedence rather than merely proving PATH-fallback works."""
    calls = []
    monkeypatch.setenv("LLM_ISOLATE_HELPER", "/abs/llm-isolate-provider")
    monkeypatch.setattr(
        "modelman.providers.lifecycle.shutil.which",
        lambda name: "/usr/local/bin/llm-isolate-provider",
    )

    def fake_run(cmd, **kwargs):
        calls.append(cmd)
        return MagicMock(returncode=0)

    monkeypatch.setattr("modelman.providers.lifecycle.subprocess.run", fake_run)
    from modelman.providers.lifecycle import _stop_others

    _stop_others()
    assert calls == [["/abs/llm-isolate-provider", "stop-all"]]


def test_stop_others_without_env_or_path_raises(monkeypatch):
    """No env var AND no PATH entry must surface a clean LifecycleError
    (envelope contract), never a None-subscript crash."""
    monkeypatch.delenv("LLM_ISOLATE_HELPER", raising=False)
    monkeypatch.setattr(
        "modelman.providers.lifecycle.shutil.which", lambda name: None
    )
    from modelman.providers.lifecycle import LifecycleError, _stop_others

    with pytest.raises(LifecycleError, match="not found"):
        _stop_others()
