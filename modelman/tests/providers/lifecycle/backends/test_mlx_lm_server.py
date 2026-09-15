from unittest.mock import patch

import pytest

from modelman.providers.lifecycle import probe
from modelman.providers.lifecycle.backends import BACKENDS
from modelman.providers.lifecycle.backends.mlx_lm_server import (
    DRAFT_ENV_VAR,
    MLX_LM_SERVER,
    MLX_LM_SERVER_CHAT_URL,
    MLX_LM_SERVER_HEALTH_URL,
    MLX_LM_SERVER_PORT,
    TARGET_ENV_VAR,
)
from modelman.providers.lifecycle.envelope import LifecycleError


def test_mlx_lm_server_registered_in_backends_registry():
    """The bottom-of-module `MLX_LM_SERVER = MlxLmServerBackend()` singleton
    must be the same object registered under "mlx_lm_server" in BACKENDS —
    a copy-paste bug that registers a fresh instance would silently
    diverge from the one imported elsewhere."""
    assert BACKENDS["mlx_lm_server"] is MLX_LM_SERVER


def test_no_default_model():
    """Unlike ollama/omlx, this backend has no baked-in pairing — bash has
    no default target or draft for mlx_lm_server."""
    assert MLX_LM_SERVER.default_model is None
    assert MLX_LM_SERVER.env_var is None


def test_restore_action_is_stop():
    """mlx_lm_server is never part of the standing baseline restored after
    a benchmark run releases exclusivity."""
    assert MLX_LM_SERVER.restore_action == "stop"


# --- check_available() -------------------------------------------------


def test_check_available_returns_reason_when_binary_resolution_fails():
    """check_available()'s contract (backends/base.py) is "reason string or
    None, never raising" — a LifecycleError from resolve_mlx_lm_bin must be
    caught and its message returned, not propagated."""
    with patch(
        "modelman.providers.lifecycle.backends.mlx_lm_server.binaries.resolve_mlx_lm_bin",
        side_effect=LifecycleError("omlx not found — install via 'brew install omlx'"),
    ):
        reason = MLX_LM_SERVER.check_available()
    assert reason == "omlx not found — install via 'brew install omlx'"


def test_check_available_returns_none_when_binary_resolves():
    with patch(
        "modelman.providers.lifecycle.backends.mlx_lm_server.binaries.resolve_mlx_lm_bin",
        return_value="/opt/homebrew/Cellar/omlx/0.10.0/libexec/bin/mlx_lm.server",
    ):
        assert MLX_LM_SERVER.check_available() is None


# --- resolve() — the bug-fix invariant ----------------------------------


@pytest.mark.parametrize(
    "model,extra_args,env",
    [
        # both missing entirely (no positionals, no env vars)
        (None, (), {}),
        # target given, draft missing (no second positional, no env var)
        ("target-repo", ("target-repo",), {}),
        # target missing, draft given via positional
        (None, ("", "draft-repo"), {}),
    ],
)
def test_resolve_validates_target_and_draft_with_no_side_effects(
    model, extra_args, env, monkeypatch
):
    """The bug this task exists to fix: bash's start path calls
    `stop_all_local mlx_lm_server` (tearing down every OTHER local
    provider) BEFORE validating that target+draft were supplied. resolve()
    must raise LifecycleError on any missing target/draft AND must not
    touch the binary resolver, the pidfile process, or any probe as a
    side effect — proving it validates before anything else runs, so a
    caller can safely call resolve() first and reject a bad request
    before any teardown happens."""
    monkeypatch.delenv(TARGET_ENV_VAR, raising=False)
    monkeypatch.delenv(DRAFT_ENV_VAR, raising=False)
    for key, value in env.items():
        monkeypatch.setenv(key, value)

    with (
        patch(
            "modelman.providers.lifecycle.backends.mlx_lm_server.binaries.resolve_mlx_lm_bin"
        ) as mock_resolve_bin,
        patch("modelman.providers.lifecycle.backends.mlx_lm_server._PROC") as mock_proc,
        patch(
            "modelman.providers.lifecycle.backends.mlx_lm_server.probe.port_closed_within"
        ) as mock_port_closed,
        pytest.raises(
            LifecycleError,
            # The message must name the CLI's `--draft` flag (the only way a
            # CLI user can supply a draft — there is no second positional),
            # the env-var fallback, and the extra_args pair in-process
            # callers forward.
            match=(
                r"mlx_lm_server requires target\+draft: run `modelman provider isolate "
                r"mlx_lm_server <target> --draft <draft>`, or set "
                r"LLM_ISOLATE_MLXLM_MODEL/LLM_ISOLATE_MLXLM_DRAFT_MODEL "
                r"\(in-process callers forward the pair as extra_args=\(target, draft\)\)"
            ),
        ),
    ):
        MLX_LM_SERVER.resolve(model, extra_args)

    mock_resolve_bin.assert_not_called()
    mock_proc.stop.assert_not_called()
    mock_proc.spawn.assert_not_called()
    mock_port_closed.assert_not_called()


# --- resolve() — precedence ----------------------------------------------


def test_resolve_target_explicit_model_wins(monkeypatch):
    """The explicit `model` param (the CLI's first positional) wins over
    extra_args[0] and the env var for target resolution."""
    monkeypatch.setenv(TARGET_ENV_VAR, "env-target")
    with patch(
        "modelman.providers.lifecycle.backends.mlx_lm_server.binaries.resolve_mlx_lm_bin",
        return_value="/bin/mlx_lm.server",
    ):
        plan = MLX_LM_SERVER.resolve("explicit-target", ("positional-target", "draft-repo"))
    assert plan.extra["target"] == "explicit-target"


def test_resolve_target_falls_back_to_extra_args_first_element(monkeypatch):
    """With no explicit `model`, extra_args[0] is used for target ahead of
    the env var."""
    monkeypatch.setenv(TARGET_ENV_VAR, "env-target")
    with patch(
        "modelman.providers.lifecycle.backends.mlx_lm_server.binaries.resolve_mlx_lm_bin",
        return_value="/bin/mlx_lm.server",
    ):
        plan = MLX_LM_SERVER.resolve(None, ("positional-target", "draft-repo"))
    assert plan.extra["target"] == "positional-target"


def test_resolve_target_falls_back_to_env_var(monkeypatch):
    """With no explicit `model` and no positional target, the
    LLM_ISOLATE_MLXLM_MODEL env var is used."""
    monkeypatch.setenv(TARGET_ENV_VAR, "env-target")
    with patch(
        "modelman.providers.lifecycle.backends.mlx_lm_server.binaries.resolve_mlx_lm_bin",
        return_value="/bin/mlx_lm.server",
    ):
        plan = MLX_LM_SERVER.resolve(None, ("", "draft-repo"))
    assert plan.extra["target"] == "env-target"


def test_resolve_draft_falls_back_to_extra_args_second_element(monkeypatch):
    """draft resolution prefers extra_args[1] over the env var."""
    monkeypatch.setenv(DRAFT_ENV_VAR, "env-draft")
    with patch(
        "modelman.providers.lifecycle.backends.mlx_lm_server.binaries.resolve_mlx_lm_bin",
        return_value="/bin/mlx_lm.server",
    ):
        plan = MLX_LM_SERVER.resolve("target-repo", ("target-repo", "positional-draft"))
    assert plan.argv[4] == "positional-draft"


def test_resolve_draft_falls_back_to_env_var(monkeypatch):
    """With no positional draft, LLM_ISOLATE_MLXLM_DRAFT_MODEL is used."""
    monkeypatch.setenv(DRAFT_ENV_VAR, "env-draft")
    with patch(
        "modelman.providers.lifecycle.backends.mlx_lm_server.binaries.resolve_mlx_lm_bin",
        return_value="/bin/mlx_lm.server",
    ):
        plan = MLX_LM_SERVER.resolve("target-repo", ())
    assert plan.argv[4] == "env-draft"


def test_resolve_returns_start_plan_shape(monkeypatch):
    """resolve() must return a StartPlan with extra["target"] set to the
    bare target repo id and model formatted as "<target> (+draft
    <draft>)" — the envelope's human-readable display string."""
    with patch(
        "modelman.providers.lifecycle.backends.mlx_lm_server.binaries.resolve_mlx_lm_bin",
        return_value="/bin/mlx_lm.server",
    ):
        plan = MLX_LM_SERVER.resolve("target-repo", ("target-repo", "draft-repo"))
    assert plan.model == "target-repo (+draft draft-repo)"
    assert plan.direct_url == MLX_LM_SERVER_CHAT_URL
    assert plan.extra["target"] == "target-repo"
    assert plan.argv == [
        "/bin/mlx_lm.server",
        "--model",
        "target-repo",
        "--draft-model",
        "draft-repo",
        "--port",
        str(MLX_LM_SERVER_PORT),
    ]


def test_resolve_lets_lifecycle_error_from_bin_resolution_propagate():
    """resolve() is allowed to raise LifecycleError from binary resolution
    (just not to have side effects) — a missing omlx install must surface
    as a LifecycleError from resolve() itself."""
    with (
        patch(
            "modelman.providers.lifecycle.backends.mlx_lm_server.binaries.resolve_mlx_lm_bin",
            side_effect=LifecycleError("omlx not found"),
        ),
        pytest.raises(LifecycleError, match="omlx not found"),
    ):
        MLX_LM_SERVER.resolve("target-repo", ("target-repo", "draft-repo"))


# --- start() ---------------------------------------------------------------


def test_start_stops_prior_instance_before_prebind_wait():
    """bash's mlx_lm_server_start calls mlx_lm_server_stop unconditionally
    before spawning — _PROC.stop() must run before the pre-bind wait."""
    with (
        patch(
            "modelman.providers.lifecycle.backends.mlx_lm_server.binaries.resolve_mlx_lm_bin",
            return_value="/bin/mlx_lm.server",
        ),
    ):
        plan = MLX_LM_SERVER.resolve("target-repo", ("target-repo", "draft-repo"))

    with (
        patch("modelman.providers.lifecycle.backends.mlx_lm_server._PROC") as mock_proc,
        patch(
            "modelman.providers.lifecycle.backends.mlx_lm_server.probe.port_closed_within",
            return_value=True,
        ) as mock_closed,
    ):
        MLX_LM_SERVER.start(plan)

    assert mock_proc.method_calls[0][0] == "stop"
    mock_closed.assert_called_once()
    assert mock_closed.call_args.args[0] == MLX_LM_SERVER_HEALTH_URL
    mock_proc.spawn.assert_called_once_with(plan.argv)


def test_start_raises_fatal_error_when_port_never_closes():
    """The pre-bind wait is FATAL: spawning into a still-bound port loses
    the bind race, warmup then silently succeeds against the OLD server,
    and the benchmark measures stale weights — so a timed-out wait must
    raise LifecycleError with the exact bash-derived message, and must
    never call _PROC.spawn()."""
    with patch(
        "modelman.providers.lifecycle.backends.mlx_lm_server.binaries.resolve_mlx_lm_bin",
        return_value="/bin/mlx_lm.server",
    ):
        plan = MLX_LM_SERVER.resolve("target-repo", ("target-repo", "draft-repo"))

    with (
        patch("modelman.providers.lifecycle.backends.mlx_lm_server._PROC") as mock_proc,
        patch(
            "modelman.providers.lifecycle.backends.mlx_lm_server.probe.port_closed_within",
            return_value=False,
        ),
        pytest.raises(
            LifecycleError,
            match=(
                r"port 8001 still in use after stopping prior mlx_lm_server — "
                r"refusing to spawn a replacement that cannot bind"
            ),
        ),
    ):
        MLX_LM_SERVER.start(plan)
    mock_proc.spawn.assert_not_called()


def test_start_spawns_with_exact_argv_when_port_closes_cleanly():
    with patch(
        "modelman.providers.lifecycle.backends.mlx_lm_server.binaries.resolve_mlx_lm_bin",
        return_value="/bin/mlx_lm.server",
    ):
        plan = MLX_LM_SERVER.resolve("target-repo", ("target-repo", "draft-repo"))

    with (
        patch("modelman.providers.lifecycle.backends.mlx_lm_server._PROC") as mock_proc,
        patch(
            "modelman.providers.lifecycle.backends.mlx_lm_server.probe.port_closed_within",
            return_value=True,
        ),
    ):
        MLX_LM_SERVER.start(plan)
    mock_proc.spawn.assert_called_once_with(
        [
            "/bin/mlx_lm.server",
            "--model",
            "target-repo",
            "--draft-model",
            "draft-repo",
            "--port",
            "8001",
        ]
    )


# --- warm() ------------------------------------------------------------


def test_warm_uses_bare_target_not_display_string():
    """This backend's warmup payload must use plan.extra["target"] (the
    bare target repo id), NOT plan.model (the "<target> (+draft <draft>)"
    display string the base class's warm() would send) — a real
    behavioral requirement, since mlx_lm.server would not recognize the
    display string as a model name."""
    with patch(
        "modelman.providers.lifecycle.backends.mlx_lm_server.binaries.resolve_mlx_lm_bin",
        return_value="/bin/mlx_lm.server",
    ):
        plan = MLX_LM_SERVER.resolve("target-repo", ("target-repo", "draft-repo"))
    assert plan.model == "target-repo (+draft draft-repo)"

    with patch("modelman.providers.lifecycle.backends.mlx_lm_server.probe.warmup") as mock_warmup:
        MLX_LM_SERVER.warm(plan)

    mock_warmup.assert_called_once_with(
        plan.direct_url,
        "target-repo",
        health_url=MLX_LM_SERVER.health_url,
        timeout=probe.WARMUP_TIMEOUT,
    )
    # Explicit, unambiguous assertion on the model argument actually sent.
    assert mock_warmup.call_args.args[1] == "target-repo"
    assert mock_warmup.call_args.args[1] != plan.model


# --- stop_and_wait() -----------------------------------------------------


def test_stop_and_wait_returns_none_on_clean_close():
    with (
        patch("modelman.providers.lifecycle.backends.mlx_lm_server._PROC") as mock_proc,
        patch(
            "modelman.providers.lifecycle.backends.mlx_lm_server.probe.port_closed_within",
            return_value=True,
        ) as mock_closed,
    ):
        result = MLX_LM_SERVER.stop_and_wait()
    assert result is None
    mock_proc.stop.assert_called_once()
    mock_closed.assert_called_once()
    assert mock_closed.call_args.args[0] == MLX_LM_SERVER_HEALTH_URL


def test_stop_and_wait_returns_exact_warning_when_port_stays_open():
    """If the port never closes within the wait budget, stop_and_wait()
    must return the exact hardcoded warning bash prints
    ("mlx_lm_server still listening on port 8001")."""
    with (
        patch("modelman.providers.lifecycle.backends.mlx_lm_server._PROC"),
        patch(
            "modelman.providers.lifecycle.backends.mlx_lm_server.probe.port_closed_within",
            return_value=False,
        ),
    ):
        result = MLX_LM_SERVER.stop_and_wait()
    assert result == "mlx_lm_server still listening on port 8001"


# --- restore() / wait_ready() / already_serving() are inherited no-ops ---


def test_restore_is_the_inherited_no_op():
    """restore_action="stop" is a general marker interpreted by Task 6's
    orchestrate.restore() — this task does not add an mlx_lm_server-
    specific restore() override; calling it must be a no-op with no side
    effects."""
    with (
        patch("modelman.providers.lifecycle.backends.mlx_lm_server._PROC") as mock_proc,
        patch(
            "modelman.providers.lifecycle.backends.mlx_lm_server.probe.port_closed_within"
        ) as mock_port_closed,
        patch("modelman.providers.lifecycle.backends.mlx_lm_server.probe.warmup") as mock_warmup,
    ):
        MLX_LM_SERVER.restore()
    mock_proc.stop.assert_not_called()
    mock_proc.spawn.assert_not_called()
    mock_port_closed.assert_not_called()
    mock_warmup.assert_not_called()


def test_wait_ready_and_already_serving_are_inherited_defaults():
    """No keep-loaded-weights fast path exists for this backend (that's
    mtplx-only) — already_serving() is always False and wait_ready() is a
    no-op, matching the base class defaults."""
    with patch(
        "modelman.providers.lifecycle.backends.mlx_lm_server.binaries.resolve_mlx_lm_bin",
        return_value="/bin/mlx_lm.server",
    ):
        plan = MLX_LM_SERVER.resolve("target-repo", ("target-repo", "draft-repo"))
    assert MLX_LM_SERVER.already_serving(plan) is False
    MLX_LM_SERVER.wait_ready(plan)  # must not raise
