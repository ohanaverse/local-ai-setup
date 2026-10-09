from unittest.mock import patch

import pytest

from llmbench.benchmark import isolation
from llmbench.benchmark.errors import BenchmarkError
from llmbench.benchmark.isolation import (
    isolate_provider,
    restore_providers,
)
from llmbench.local_process import ProcessResult

LIFECYCLE = "llmbench.benchmark.isolation.lifecycle"


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
    mock_isolate.assert_called_once_with("ollama", None, extra_args=())
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
    lifecycle, after the provider id. This is what lets llmbench
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
        "mlx_lm_server", None, extra_args=("target-repo", "draft-repo")
    )
    assert result.provider == "mlx_lm_server"
    assert result.ok is True


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
    """llmbench/providers/lifecycle/backends/__init__.py owns
    SUPPORTED_PROVIDER_IDS (this module re-exports it); the frozenset below
    is a deliberate hand-written COPY of that documented set, not a
    reference to it, so a backend added to BACKENDS without a decision about
    isolability is caught here rather than silently running unisolated. Every
    registered backend is in it. Any change to the supported set must update this
    literal and backends/__init__.py's constant together (issue #79
    retired the old bash helper's own "Supported:" header comment, which
    used to need the same update)."""
    assert (
        frozenset({"ollama", "omlx", "omlx-6bit", "mlx_lm_server", "mtplx"})
        == isolation.SUPPORTED_PROVIDER_IDS
    )
