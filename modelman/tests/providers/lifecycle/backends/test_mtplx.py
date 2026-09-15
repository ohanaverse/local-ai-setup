from unittest.mock import MagicMock, patch

import pytest

from modelman.providers.lifecycle import probe
from modelman.providers.lifecycle.backends import BACKENDS
from modelman.providers.lifecycle.backends.mtplx import (
    MTPLX,
    MTPLX_DIRECT_URL,
    MTPLX_LOG,
    MTPLX_PIDFILE,
    MtplxBackend,
)
from modelman.providers.lifecycle.envelope import LifecycleError


def _registry(models):
    from modelman.registry import Registry

    return Registry(providers=[], models=models)


def _mtplx_model_entry(model_name: str, n: int = 1):
    from modelman.registry import ModelEntry

    return ModelEntry(
        id=f"mtplx/{model_name}",
        family="qwen3.8",
        provider_id="mtplx",
        model_name=model_name,
    )


def test_mtplx_registered_in_backends_registry():
    """The bottom-of-module `MTPLX = MtplxBackend()` singleton must be the
    same object registered under "mtplx" in BACKENDS — a copy-paste bug
    that registers a fresh instance would silently diverge from the one
    __init__.py's isolate()/stop() actually call."""
    assert BACKENDS["mtplx"] is MTPLX


def test_class_attributes_match_todays_behavior():
    """mtplx is the one backend with cleanup_on_failure=True (a failed
    start tears the server back down) and restore_action="stop" (never
    part of the standing baseline restored after a benchmark)."""
    assert MTPLX.id == "mtplx"
    assert MTPLX.occupancy_key == "mtplx"
    assert MTPLX.env_var is None
    assert MTPLX.default_model is None
    assert MTPLX.cleanup_on_failure is True
    assert MTPLX.restore_action == "stop"
    assert MTPLX.chat_url == MTPLX_DIRECT_URL
    assert MTPLX.health_url == "http://localhost:8003/v1/models"


def test_pidfile_and_log_paths_unchanged():
    """The pidfile/log paths must move verbatim from __init__.py — a path
    change here would silently orphan a pidfile a previous install wrote
    to the old location, or break a running process's log tail."""
    assert MTPLX_PIDFILE == "/tmp/local-ai-setup-mtplx.pid"
    assert MTPLX_LOG == "/tmp/local-ai-setup-mtplx.log"


# --- check_available() ------------------------------------------------


def test_check_available_returns_none_when_binary_resolves():
    with patch(
        "modelman.providers.lifecycle.backends.mtplx.binaries.require_binary",
        return_value="/usr/local/bin/mtplx",
    ):
        assert MTPLX.check_available() is None


def test_check_available_returns_reason_when_binary_missing():
    """check_available()'s contract (backends/base.py) is "reason string or
    None, never raising" — require_binary's LifecycleError must be caught
    and its message returned."""
    with patch(
        "modelman.providers.lifecycle.backends.mtplx.binaries.require_binary",
        side_effect=LifecycleError("mtplx binary not found on PATH"),
    ):
        assert MTPLX.check_available() == "mtplx binary not found on PATH"


# --- resolve() ----------------------------------------------------------


def test_resolve_uses_explicit_model_over_registry():
    """An explicit model argument must be used verbatim, without even
    touching the registry — the positional arg is the only way a
    benchmark sweep selects which model gets served."""
    with patch("modelman.providers.lifecycle.backends.mtplx.load_registry") as mock_load:
        plan = MTPLX.resolve("Some/Explicit-Model", ())
    mock_load.assert_not_called()
    assert plan.model == "Some/Explicit-Model"
    assert plan.direct_url == MTPLX_DIRECT_URL


def test_resolve_falls_back_to_single_registry_match():
    entry = _mtplx_model_entry("Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality")
    with patch(
        "modelman.providers.lifecycle.backends.mtplx.load_registry",
        return_value=_registry([entry]),
    ):
        plan = MTPLX.resolve(None, ())
    assert plan.model == "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"


def test_resolve_no_model_in_registry_raises():
    with patch(
        "modelman.providers.lifecycle.backends.mtplx.load_registry",
        return_value=_registry([]),
    ), pytest.raises(LifecycleError, match="no mtplx model in the registry"):
        MTPLX.resolve(None, ())


def test_resolve_ambiguous_registry_raises_exact_message():
    """With no explicit model and more than one mtplx entry, resolve() must
    refuse, not silently pick the first match — a caller that failed to
    forward the model name would otherwise silently serve (and benchmark)
    the wrong weights with no error. The message text must match today's
    _resolve_mtplx_model exactly, since it's user-facing in the CLI
    envelope's error field."""
    entries = [_mtplx_model_entry(f"Org/m{n}") for n in (1, 2)]
    with patch(
        "modelman.providers.lifecycle.backends.mtplx.load_registry",
        return_value=_registry(entries),
    ), pytest.raises(
        LifecycleError,
        match=r"model required: registry holds 2 mtplx models \(Org/m1, Org/m2\)",
    ):
        MTPLX.resolve(None, ())


# --- already_serving() ---------------------------------------------------


def test_already_serving_delegates_to_probe_serving_model():
    plan = MTPLX.resolve("Org/Model", ())
    with patch(
        "modelman.providers.lifecycle.backends.mtplx.probe.serving_model",
        return_value=True,
    ) as mock_serving:
        assert MTPLX.already_serving(plan) is True
    mock_serving.assert_called_once_with(MTPLX.health_url, "Org/Model")


# --- replace_own_occupant() — the solo-restart early-raise ---------------


def test_replace_own_occupant_no_op_on_clean_stop():
    plan = MTPLX.resolve("Org/Model", ())
    with patch.object(MTPLX, "stop_and_wait", return_value=None) as mock_stop:
        MTPLX.replace_own_occupant(plan)  # must not raise
    mock_stop.assert_called_once()


def test_replace_own_occupant_raises_immediately_on_stop_warning():
    """A stop_and_wait() warning must raise immediately here rather than
    falling through to start()'s wait_for_port_closed poll, which would
    fail ~10s later with a generic "port still answering" message that
    hides the real stop failure."""
    plan = MTPLX.resolve("Org/Model", ())
    with (
        patch.object(MTPLX, "stop_and_wait", return_value="mtplx binary not found on PATH"),
        pytest.raises(LifecycleError, match="mtplx binary not found on PATH"),
    ):
        MTPLX.replace_own_occupant(plan)


# --- start() --------------------------------------------------------------


def test_start_waits_for_port_closed_then_spawns_with_pinned_model_id():
    """start() must wait for port 8003 to close before spawning, and must
    always pass --model-id explicitly: without it, mtplx derives its own
    slug for /v1/models that never matches the org/model repo id
    wait_ready polls for, so isolation times out even though the server is
    healthy (confirmed live 2026-09-10)."""
    plan = MTPLX.resolve("Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality", ())
    proc = MagicMock()
    with (
        patch(
            "modelman.providers.lifecycle.backends.mtplx.binaries.require_binary",
            return_value="/usr/local/bin/mtplx",
        ),
        patch(
            "modelman.providers.lifecycle.backends.mtplx.probe.wait_for_port_closed"
        ) as mock_wait_closed,
        patch("modelman.providers.lifecycle.backends.mtplx._PROC") as mock_proc,
    ):
        mock_proc.spawn.return_value = proc
        MTPLX.start(plan)
    mock_wait_closed.assert_called_once_with(MTPLX.health_url)
    mock_spawn = mock_proc.spawn
    mock_spawn.assert_called_once()
    argv = mock_spawn.call_args.args[0]
    assert argv[0] == "/usr/local/bin/mtplx"
    assert argv[1:8] == [
        "serve",
        "--model",
        "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality",
        "--port",
        "8003",
        "--host",
        "127.0.0.1",
    ]
    assert "--model-id" in argv
    assert argv[argv.index("--model-id") + 1] == "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"
    assert MTPLX._proc is proc


def test_start_raises_when_binary_missing():
    plan = MTPLX.resolve("Org/Model", ())
    with (
        patch(
            "modelman.providers.lifecycle.backends.mtplx.binaries.require_binary",
            side_effect=LifecycleError("mtplx binary not found on PATH"),
        ),
        patch("modelman.providers.lifecycle.backends.mtplx.probe.wait_for_port_closed") as mock_wait,
        patch("modelman.providers.lifecycle.backends.mtplx._PROC") as mock_proc,
        pytest.raises(LifecycleError, match="mtplx binary not found on PATH"),
    ):
        MTPLX.start(plan)
    mock_wait.assert_not_called()
    mock_proc.spawn.assert_not_called()


def test_start_propagates_port_still_answering():
    plan = MTPLX.resolve("Org/Model", ())
    with (
        patch(
            "modelman.providers.lifecycle.backends.mtplx.binaries.require_binary",
            return_value="/usr/local/bin/mtplx",
        ),
        patch(
            "modelman.providers.lifecycle.backends.mtplx.probe.wait_for_port_closed",
            side_effect=LifecycleError("port still answering"),
        ),
        patch("modelman.providers.lifecycle.backends.mtplx._PROC") as mock_proc,
        pytest.raises(LifecycleError, match="port still answering"),
    ):
        MTPLX.start(plan)
    mock_proc.spawn.assert_not_called()


# --- wait_ready() ----------------------------------------------------------


def test_wait_ready_forwards_the_start_time_popen_handle():
    """wait_ready() must forward the exact Popen handle start() stashed
    (self._proc), since probe.wait_for_model uses it to detect the serve
    process dying mid-load."""
    plan = MTPLX.resolve("Org/Model", ())
    proc = MagicMock()
    with (
        patch(
            "modelman.providers.lifecycle.backends.mtplx.binaries.require_binary",
            return_value="/usr/local/bin/mtplx",
        ),
        patch("modelman.providers.lifecycle.backends.mtplx.probe.wait_for_port_closed"),
        patch("modelman.providers.lifecycle.backends.mtplx._PROC") as mock_proc,
    ):
        mock_proc.spawn.return_value = proc
        MTPLX.start(plan)

    with patch(
        "modelman.providers.lifecycle.backends.mtplx.probe.wait_for_model"
    ) as mock_wait_model:
        MTPLX.wait_ready(plan)
    mock_wait_model.assert_called_once_with(
        MTPLX.health_url, "Org/Model", proc=proc, timeout=probe.MODEL_LOAD_TIMEOUT
    )


# --- warm() — mtplx's shorter timeout --------------------------------------


def test_warm_uses_120s_timeout_not_the_shared_warmup_timeout():
    """mtplx's warmup timeout is 120s, not probe.WARMUP_TIMEOUT (300s) —
    preserved unchanged from today's _warmup default; this must not be
    "simplified" back to the shared constant as part of the move."""
    plan = MTPLX.resolve("Org/Model", ())
    with patch("modelman.providers.lifecycle.backends.mtplx.probe.warmup") as mock_warmup:
        MTPLX.warm(plan)
    mock_warmup.assert_called_once_with(
        MTPLX.chat_url, "Org/Model", health_url=MTPLX.health_url, timeout=120.0
    )
    assert mock_warmup.call_args.kwargs["timeout"] != probe.WARMUP_TIMEOUT


# --- stop_and_wait() --------------------------------------------------------


def test_stop_and_wait_returns_none_when_stop_succeeds_and_port_closes():
    with (
        patch(
            "modelman.providers.lifecycle.backends.mtplx.binaries.require_binary",
            return_value="/usr/local/bin/mtplx",
        ),
        patch("modelman.providers.lifecycle.backends.mtplx.subprocess.run") as mock_run,
        patch(
            "modelman.providers.lifecycle.backends.mtplx.probe.port_closed_within",
            return_value=True,
        ) as mock_closed,
    ):
        mock_run.return_value.returncode = 0
        result = MTPLX.stop_and_wait()
    assert result is None
    args = mock_run.call_args.args[0]
    assert args[:2] == ["/usr/local/bin/mtplx", "stop"]
    assert "--port" in args and "8003" in args
    assert "--grace-seconds" in args and "10" in args
    mock_closed.assert_called_once()
    assert mock_closed.call_args.args[0] == MTPLX.health_url
    assert mock_closed.call_args.kwargs["timeout"] == probe.STOP_WAIT_TIMEOUT


def test_stop_and_wait_returns_reason_when_binary_missing():
    with patch(
        "modelman.providers.lifecycle.backends.mtplx.binaries.require_binary",
        side_effect=LifecycleError("mtplx binary not found on PATH"),
    ):
        result = MTPLX.stop_and_wait()
    assert result == "mtplx binary not found on PATH"


def test_stop_and_wait_returns_stderr_when_stop_command_fails():
    with (
        patch(
            "modelman.providers.lifecycle.backends.mtplx.binaries.require_binary",
            return_value="/usr/local/bin/mtplx",
        ),
        patch("modelman.providers.lifecycle.backends.mtplx.subprocess.run") as mock_run,
    ):
        mock_run.return_value.returncode = 1
        mock_run.return_value.stderr = "boom"
        mock_run.return_value.stdout = ""
        result = MTPLX.stop_and_wait()
    assert result == "boom"


def test_stop_and_wait_reports_failure_when_port_never_closes():
    """The bug fix: today's _stop_mtplx reports success as soon as the
    `mtplx stop` subprocess exits 0, with no check that port 8003 actually
    stopped answering — bash's `stop mtplx` case arm never captured a
    warning either, so a stop that reported success while mtplx kept the
    port bound went completely unnoticed. stop_and_wait() must now poll
    the port after a successful stop command and return a warning instead
    of None when it never closes."""
    with (
        patch(
            "modelman.providers.lifecycle.backends.mtplx.binaries.require_binary",
            return_value="/usr/local/bin/mtplx",
        ),
        patch("modelman.providers.lifecycle.backends.mtplx.subprocess.run") as mock_run,
        patch(
            "modelman.providers.lifecycle.backends.mtplx.probe.port_closed_within",
            return_value=False,
        ),
    ):
        mock_run.return_value.returncode = 0
        result = MTPLX.stop_and_wait()
    assert result == "mtplx still listening on port 8003"


# --- restore() is the inherited no-op --------------------------------------


def test_restore_is_the_inherited_no_op():
    """restore_action="stop" is a general marker interpreted by a later
    orchestration task — this task does not add an mtplx-specific
    restore() override; calling it must be a no-op with no side effects."""
    with (
        patch("modelman.providers.lifecycle.backends.mtplx._PROC") as mock_proc,
        patch("modelman.providers.lifecycle.backends.mtplx.subprocess.run") as mock_run,
    ):
        MTPLX.restore()
    mock_proc.stop.assert_not_called()
    mock_proc.spawn.assert_not_called()
    mock_run.assert_not_called()


def test_mtplx_backend_is_a_distinct_instance_type():
    """Sanity check that MtplxBackend can be constructed independently of
    the module-level singleton (used by tests above via MTPLX directly);
    guards against an accidental shared-mutable-default bug in __init__."""
    other = MtplxBackend()
    assert other is not MTPLX
    assert other._proc is None
