import subprocess
from unittest.mock import patch

import pytest

from modelman.providers.lifecycle.backends import BACKENDS
from modelman.providers.lifecycle.backends.omlx import (
    DEFAULT_4BIT_MODEL,
    DEFAULT_6BIT_MODEL,
    ENV_VAR_4BIT,
    ENV_VAR_6BIT,
    OMLX_4BIT,
    OMLX_6BIT,
    OMLX_CHAT_URL,
    OMLX_HEALTH_URL,
    OmlxBackend,
)
from modelman.providers.lifecycle.envelope import LifecycleError


def test_omlx_variants_registered_in_backends_registry():
    """The bottom-of-module OMLX_4BIT/OMLX_6BIT singletons must be the same
    objects registered in BACKENDS under their own ids — a copy-paste bug
    that registers fresh instances would silently diverge from the ones
    imported elsewhere."""
    assert BACKENDS["omlx"] is OMLX_4BIT
    assert BACKENDS["omlx-6bit"] is OMLX_6BIT


def test_omlx_variants_share_one_occupancy_key():
    """Both omlx variants are served by ONE physical daemon on ONE port —
    this is the load-bearing invariant a later orchestration task depends
    on to dedupe stop-all/restore fan-out by occupancy_key instead of by
    id, so `omlx stop` is never issued twice concurrently for the two
    registered ids."""
    assert OMLX_4BIT.occupancy_key == "omlx"
    assert OMLX_6BIT.occupancy_key == "omlx"


def test_only_4bit_instance_restores():
    """Only the 4-bit instance restores after a benchmark releases
    exclusivity — starting the shared daemon twice (once per instance)
    during a restore fan-out would be redundant, since they share one
    process."""
    assert OMLX_4BIT.restore_action == "restart"
    assert OMLX_6BIT.restore_action == "skip"


# --- check_available() -------------------------------------------------


def test_check_available_returns_message_when_binary_missing():
    """No `omlx` binary on PATH must return the exact bash error message —
    matches `command -v omlx >/dev/null 2>&1 || { echo "omlx binary not
    found on PATH" >&2; exit 1; }`."""
    with patch("modelman.providers.lifecycle.backends.omlx.shutil.which", return_value=None):
        assert OMLX_4BIT.check_available() == "omlx binary not found on PATH"


def test_check_available_returns_none_when_binary_present():
    """When `omlx` is on PATH, check_available() must return None (available)."""
    with patch(
        "modelman.providers.lifecycle.backends.omlx.shutil.which",
        return_value="/usr/local/bin/omlx",
    ):
        assert OMLX_4BIT.check_available() is None


# --- resolve() / model precedence, per instance -------------------------


def test_resolve_4bit_uses_explicit_model_when_given(monkeypatch):
    """An explicit model argument must win over both the env var and the
    default for the 4-bit instance."""
    monkeypatch.setenv(ENV_VAR_4BIT, "env-4bit-model")
    plan = OMLX_4BIT.resolve("explicit-model", ())
    assert plan.model == "explicit-model"
    assert plan.direct_url == OMLX_CHAT_URL


def test_resolve_4bit_env_var_wins_over_default(monkeypatch):
    """With no explicit model, LLM_ISOLATE_OMLX_4BIT_MODEL must win over the
    baked-in 4-bit default."""
    monkeypatch.setenv(ENV_VAR_4BIT, "env-4bit-model")
    plan = OMLX_4BIT.resolve(None, ())
    assert plan.model == "env-4bit-model"


def test_resolve_4bit_falls_back_to_default_model(monkeypatch):
    """With neither an explicit model nor the env var set, resolve() must
    use DEFAULT_4BIT_MODEL."""
    monkeypatch.delenv(ENV_VAR_4BIT, raising=False)
    plan = OMLX_4BIT.resolve(None, ())
    assert plan.model == DEFAULT_4BIT_MODEL


def test_resolve_6bit_uses_explicit_model_when_given(monkeypatch):
    """An explicit model argument must win over both the env var and the
    default for the 6-bit instance."""
    monkeypatch.setenv(ENV_VAR_6BIT, "env-6bit-model")
    plan = OMLX_6BIT.resolve("explicit-model", ())
    assert plan.model == "explicit-model"
    assert plan.direct_url == OMLX_CHAT_URL


def test_resolve_6bit_env_var_wins_over_default(monkeypatch):
    """With no explicit model, LLM_ISOLATE_OMLX_6BIT_MODEL must win over the
    baked-in 6-bit default."""
    monkeypatch.setenv(ENV_VAR_6BIT, "env-6bit-model")
    plan = OMLX_6BIT.resolve(None, ())
    assert plan.model == "env-6bit-model"


def test_resolve_6bit_falls_back_to_default_model(monkeypatch):
    """With neither an explicit model nor the env var set, resolve() must
    use DEFAULT_6BIT_MODEL."""
    monkeypatch.delenv(ENV_VAR_6BIT, raising=False)
    plan = OMLX_6BIT.resolve(None, ())
    assert plan.model == DEFAULT_6BIT_MODEL


def test_resolve_instances_do_not_cross_read_each_others_env_var(monkeypatch):
    """Setting only the 6-bit env var must not leak into the 4-bit
    instance's resolution (and vice versa) — each OmlxBackend instance must
    only ever read its own `env_var`, never the other's, even though they
    share one physical daemon."""
    monkeypatch.delenv(ENV_VAR_4BIT, raising=False)
    monkeypatch.setenv(ENV_VAR_6BIT, "env-6bit-model")
    plan = OMLX_4BIT.resolve(None, ())
    assert plan.model == DEFAULT_4BIT_MODEL

    monkeypatch.delenv(ENV_VAR_6BIT, raising=False)
    monkeypatch.setenv(ENV_VAR_4BIT, "env-4bit-model")
    plan = OMLX_6BIT.resolve(None, ())
    assert plan.model == DEFAULT_6BIT_MODEL


# --- start() -------------------------------------------------------------


def test_start_invokes_omlx_start_swallowing_failure():
    """bash's start_omlx() runs `omlx start >/dev/null 2>&1 || true` — a
    non-zero return code must not raise. A failed subprocess.run() call
    (mocked to return a non-zero code) must not propagate."""
    plan = OMLX_4BIT.resolve("m", ())
    with patch("modelman.providers.lifecycle.backends.omlx.subprocess.run") as mock_run:
        mock_run.return_value = subprocess.CompletedProcess(["omlx", "start"], 1)
        OmlxBackend("omlx", ENV_VAR_4BIT, DEFAULT_4BIT_MODEL, restore_action="restart").start(plan)
    mock_run.assert_called_once_with(["omlx", "start"], capture_output=True, check=False)


def test_start_does_not_warm():
    """warm() is orchestrate.isolate()'s job (called once after
    start()+wait_ready() for every backend) — start() calling it too would
    warm the model twice per isolate(). Regression test for that bug."""
    plan = OMLX_4BIT.resolve("m", ())
    with (
        patch("modelman.providers.lifecycle.backends.omlx.subprocess.run"),
        patch("modelman.providers.lifecycle.backends.base.warmup") as mock_warmup,
    ):
        OMLX_4BIT.start(plan)
    mock_warmup.assert_not_called()


# --- stop_and_wait() -------------------------------------------------------


def test_stop_and_wait_invokes_omlx_stop_and_returns_none_on_clean_close():
    """`omlx stop` is invoked (output/failure swallowed) and stop_and_wait()
    returns None when the port closes within the wait budget."""
    with (
        patch("modelman.providers.lifecycle.backends.omlx.subprocess.run") as mock_run,
        patch(
            "modelman.providers.lifecycle.backends.omlx.probe.port_closed_within",
            return_value=True,
        ) as mock_closed,
    ):
        result = OMLX_4BIT.stop_and_wait()
    assert result is None
    mock_run.assert_called_once_with(["omlx", "stop"], capture_output=True, check=False)
    mock_closed.assert_called_once()
    assert mock_closed.call_args.args[0] == OMLX_HEALTH_URL


def test_stop_and_wait_returns_exact_warning_when_port_stays_open():
    """If the port never closes within the wait budget, stop_and_wait() must
    return the exact hardcoded warning bash prints (`omlx still listening on
    port 8000`) — literal port number, matching bash's hardcoded message
    since there is only ever one omlx port."""
    with (
        patch("modelman.providers.lifecycle.backends.omlx.subprocess.run"),
        patch(
            "modelman.providers.lifecycle.backends.omlx.probe.port_closed_within",
            return_value=False,
        ),
    ):
        result = OMLX_4BIT.stop_and_wait()
    assert result == "omlx still listening on port 8000"


def test_stop_and_wait_swallows_subprocess_failure():
    """A non-zero `omlx stop` exit must not raise — bash's `silence_stdout
    omlx stop || true` swallows it."""
    with (
        patch("modelman.providers.lifecycle.backends.omlx.subprocess.run") as mock_run,
        patch(
            "modelman.providers.lifecycle.backends.omlx.probe.port_closed_within",
            return_value=True,
        ),
    ):
        mock_run.return_value = subprocess.CompletedProcess(["omlx", "stop"], 1)
        result = OMLX_6BIT.stop_and_wait()
    assert result is None


def test_stop_and_wait_tolerates_missing_binary():
    """`omlx stop` itself missing from PATH must not raise — bash's
    `silence_stdout omlx stop || true` swallows a "command not found" exit
    just as tolerantly as any other nonzero exit; subprocess.run raises
    FileNotFoundError for a missing binary instead, so that must be caught
    explicitly to preserve the same tolerance."""
    with (
        patch(
            "modelman.providers.lifecycle.backends.omlx.subprocess.run",
            side_effect=FileNotFoundError("omlx"),
        ),
        patch(
            "modelman.providers.lifecycle.backends.omlx.probe.port_closed_within",
            return_value=True,
        ),
    ):
        result = OMLX_6BIT.stop_and_wait()
    assert result is None


# --- restore() -------------------------------------------------------------


def test_restore_no_ops_for_6bit_instance():
    """OMLX_6BIT.restore_action == "skip" — restore() must do nothing at
    all (no probe, no subprocess call, no wait) since the 4-bit instance's
    restore already covers the shared daemon."""
    with (
        patch("modelman.providers.lifecycle.backends.omlx.urllib.request.urlopen") as mock_urlopen,
        patch("modelman.providers.lifecycle.backends.omlx.subprocess.run") as mock_run,
        patch("modelman.providers.lifecycle.backends.omlx.probe.wait_for_port_open") as mock_wait,
    ):
        OMLX_6BIT.restore()
    mock_urlopen.assert_not_called()
    mock_run.assert_not_called()
    mock_wait.assert_not_called()


def test_restore_4bit_no_ops_when_already_up():
    """restore() must not start omlx at all when it already answers its
    health check — a no-op restart would be pointless churn."""
    with (
        patch("modelman.providers.lifecycle.backends.omlx.urllib.request.urlopen") as mock_urlopen,
        patch("modelman.providers.lifecycle.backends.omlx.subprocess.run") as mock_run,
        patch("modelman.providers.lifecycle.backends.omlx.probe.wait_for_port_open") as mock_wait,
    ):
        mock_urlopen.return_value.__enter__ = lambda self: self
        mock_urlopen.return_value.__exit__ = lambda *a: None
        OMLX_4BIT.restore()
    mock_run.assert_not_called()
    mock_wait.assert_not_called()


def test_restore_4bit_starts_and_waits_when_down():
    """When the health probe fails, restore() must run `omlx start` and
    then poll for it to come back up, matching bash's `restart_omlx`
    fallback path."""
    with (
        patch(
            "modelman.providers.lifecycle.backends.omlx.urllib.request.urlopen",
            side_effect=OSError("refused"),
        ),
        patch("modelman.providers.lifecycle.backends.omlx.subprocess.run") as mock_run,
        patch(
            "modelman.providers.lifecycle.backends.omlx.probe.wait_for_port_open",
            return_value=True,
        ) as mock_wait,
    ):
        OMLX_4BIT.restore()
    mock_run.assert_called_once_with(["omlx", "start"], capture_output=True, check=False)
    mock_wait.assert_called_once()
    assert mock_wait.call_args.args[0] == OMLX_HEALTH_URL


def test_restore_4bit_raises_lifecycle_error_when_it_never_comes_back():
    """If omlx never answers within RESTORE_WAIT_TIMEOUT after `omlx
    start`, restore() must raise LifecycleError with a message naming the
    health URL — matching bash's `wait_for()` failure message shape and
    ollama's restore() error shape from Task 2."""
    with (
        patch(
            "modelman.providers.lifecycle.backends.omlx.urllib.request.urlopen",
            side_effect=OSError("refused"),
        ),
        patch("modelman.providers.lifecycle.backends.omlx.subprocess.run"),
        patch(
            "modelman.providers.lifecycle.backends.omlx.probe.wait_for_port_open",
            return_value=False,
        ),
        pytest.raises(LifecycleError, match="omlx did not come back up"),
    ):
        OMLX_4BIT.restore()
