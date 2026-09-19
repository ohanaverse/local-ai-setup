from unittest.mock import patch

import pytest

from modelman.benchmark import isolation
from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.isolation import (
    isolate_provider,
    restore_providers,
    stop_all_local_providers,
    stop_provider,
)
from modelman.local_process import ProcessResult

LIFECYCLE = "modelman.benchmark.isolation.lifecycle"


def _ok(
    provider="ollama", model="ornith-1.5:35b", url="http://localhost:11434/v1/chat/completions"
):
    return ProcessResult(provider=provider, model=model, direct_url=url, ok=True, error=None)


def _fail(provider="ollama", error="ollama not reachable"):
    return ProcessResult(provider=provider, model="", direct_url="", ok=False, error=error)


# --- isolate_provider --------------------------------------------------


def test_isolate_provider_success():
    """The lifecycle's envelope is returned to the caller as-is — benchmark
    runner.py reads .direct_url off it to address the isolated model."""
    with patch(f"{LIFECYCLE}.isolate", return_value=_ok()) as mock_isolate:
        result = isolate_provider("ollama")
    mock_isolate.assert_called_once_with("ollama", None, extra_args=(), solo=False)
    assert result.provider == "ollama"
    assert result.direct_url == "http://localhost:11434/v1/chat/completions"
    assert result.ok is True


def test_isolate_provider_failure_raises():
    """An ok=False envelope must become a BenchmarkError: this layer exists
    to translate the lifecycle's envelopes into the exception type every
    benchmark caller already handles per row."""
    with (
        patch(f"{LIFECYCLE}.isolate", return_value=_fail()),
        pytest.raises(BenchmarkError, match="ollama not reachable"),
    ):
        isolate_provider("ollama")


def test_isolate_provider_forwards_extra_args():
    """isolate_provider() must forward extra positional args (e.g. the
    target/draft pair mlx_lm_server needs) straight through to the
    lifecycle, after the provider id. This is what lets modelman benchmark
    agent isolate a target+draft pairing that has no default anywhere —
    without it, callers could only ever isolate providers whose model choice
    has a baked-in default, which mlx_lm_server deliberately has none of."""
    with patch(
        f"{LIFECYCLE}.isolate",
        return_value=_ok(
            "mlx_lm_server",
            "target-repo (+draft draft-repo)",
            "http://localhost:8001/v1/chat/completions",
        ),
    ) as mock_isolate:
        result = isolate_provider("mlx_lm_server", "target-repo", "draft-repo")
    mock_isolate.assert_called_once_with(
        "mlx_lm_server", None, extra_args=("target-repo", "draft-repo"), solo=False
    )
    assert result.provider == "mlx_lm_server"
    assert result.ok is True


def test_isolate_provider_solo_forwards_flag():
    """solo=True must reach the lifecycle, which is what lets modelman's
    same-provider-only local-model lifecycle start one provider without
    tearing down every other local provider first. Without this the new
    same-provider-only start path would silently fall back to full
    exclusivity."""
    with patch(f"{LIFECYCLE}.isolate", return_value=_ok("omlx")) as mock_isolate:
        isolate_provider("omlx", solo=True)
    assert mock_isolate.call_args.kwargs["solo"] is True


def test_isolate_provider_default_is_not_solo():
    """The default must stay solo=False — every EXISTING caller (modelman
    benchmark) never passes solo and still needs full exclusivity for clean
    measurement."""
    with patch(f"{LIFECYCLE}.isolate", return_value=_ok("omlx")) as mock_isolate:
        isolate_provider("omlx")
    assert mock_isolate.call_args.kwargs["solo"] is False


def test_isolate_provider_env_override_becomes_the_explicit_model():
    """`modelman start` passes env={LLM_ISOLATE_OLLAMA_MODEL: name} so the
    requested model is warmed instead of the provider's baked-in default.
    That value must be extracted and passed as the EXPLICIT model argument
    (top of the backend's precedence chain): `env` is a dict the caller
    built, so relying on the backend's own os.environ fallback would read
    this process's environment instead, and warm the wrong model."""
    with patch(f"{LIFECYCLE}.isolate", return_value=_ok(model="custom:tag")) as mock_isolate:
        isolate_provider("ollama", env={"LLM_ISOLATE_OLLAMA_MODEL": "custom:tag"})
    assert mock_isolate.call_args.args == ("ollama", "custom:tag")


def test_isolate_provider_env_ignored_for_providers_without_an_env_var():
    """mlx_lm_server takes its target+draft as positional args and has no
    single LLM_ISOLATE_*_MODEL variable (see local_process.
    ENV_VAR_BY_PROVIDER), so an unrelated env dict must not be mined for a
    model — the pairing in extra_args is the only source."""
    with patch(f"{LIFECYCLE}.isolate", return_value=_ok("mlx_lm_server")) as mock_isolate:
        isolate_provider("mlx_lm_server", "t", "d", env={"SOMETHING_ELSE": "x"})
    assert mock_isolate.call_args.args == ("mlx_lm_server", None)


# --- stop_provider / stop_all_local_providers --------------------------


def test_stop_provider_success():
    """stop_provider() must stop exactly ONE provider, leaving every other
    local provider running — the primitive the same-provider-only lifecycle
    needs to replace a single-port provider's occupant."""
    with patch(f"{LIFECYCLE}.stop", return_value=_ok("omlx", "", "")) as mock_stop:
        result = stop_provider("omlx")
    mock_stop.assert_called_once_with("omlx")
    assert result.ok is True


def test_stop_provider_failure_raises():
    """A stop whose port never closed comes back ok=False and must raise:
    local_control relies on this to refuse to start a replacement model on a
    port the previous occupant still holds."""
    with (
        patch(
            f"{LIFECYCLE}.stop", return_value=_fail("mtplx", "mtplx still listening on port 8003")
        ),
        pytest.raises(BenchmarkError, match="mtplx still listening on port 8003"),
    ):
        stop_provider("mtplx")


def test_stop_all_local_providers_success():
    """`modelman stop` delegates to this to tear down whatever local model
    is running, regardless of which provider it's on — without a stop-all,
    modelman would have to know and stop each provider individually,
    duplicating the lifecycle's own fan-out."""
    with patch(f"{LIFECYCLE}.stop_all", return_value=_ok("stop-all", "", "")) as mock_stop_all:
        result = stop_all_local_providers()
    mock_stop_all.assert_called_once_with()
    assert result.ok is True


def test_stop_all_local_providers_failure_raises():
    with (
        patch(f"{LIFECYCLE}.stop_all", return_value=_fail("stop-all", "stop failed")),
        pytest.raises(BenchmarkError, match="stop failed"),
    ):
        stop_all_local_providers()


# --- restore_providers -------------------------------------------------


def test_restore_providers_success():
    with patch(f"{LIFECYCLE}.restore", return_value=_ok("restore", "", "")) as mock_restore:
        restore_providers()
    mock_restore.assert_called_once_with()


def test_restore_providers_failure_raises():
    """A provider that never came back up must surface: benchmark runner.py
    reports it as a restore_error on an otherwise-complete run rather than
    leaving the machine silently missing a baseline service."""
    with (
        patch(f"{LIFECYCLE}.restore", return_value=_fail("restore", "ollama did not come back up")),
        pytest.raises(BenchmarkError, match="ollama did not come back up"),
    ):
        restore_providers()


# --- drift trip-wire ---------------------------------------------------


def test_supported_provider_ids_matches_the_backends_registry_documented_list():
    """modelman/providers/lifecycle/backends/__init__.py owns
    SUPPORTED_PROVIDER_IDS (this module re-exports it); the frozenset below
    is a deliberate hand-written COPY of that documented set, not a
    reference to it, so a backend added to BACKENDS without a decision about
    isolability is caught here rather than silently running unisolated. The
    llamacpp backend is registered in BACKENDS but retired (issue #33,
    2026-09-07 — see docs/reference/provider-artifacts.md), so it is
    deliberately absent. Any change to the supported set must update this
    literal and backends/__init__.py's constant together (issue #79
    retired the old bash helper's own "Supported:" header comment, which
    used to need the same update)."""
    assert (
        frozenset({"ollama", "omlx", "omlx-6bit", "mlx_lm_server", "mtplx"})
        == isolation.SUPPORTED_PROVIDER_IDS
    )
