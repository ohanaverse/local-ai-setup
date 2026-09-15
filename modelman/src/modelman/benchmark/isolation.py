"""Benchmark-facing wrapper around modelman's local-provider lifecycle.

Formerly the subprocess contract with `bin/llm-isolate-provider` /
`bin/llm-restore-providers`; those calls are gone (issue #79) and every
function here now drives `modelman.providers.lifecycle` in-process. The five
public names and their signatures are unchanged, so no caller
(`local_control.py`, `benchmark/runner.py`, `benchmark/agent/runner.py`)
needed edits: this layer's job is still to translate the lifecycle's
`ok=False` envelopes into the `BenchmarkError` its callers expect.
"""

from __future__ import annotations

import os

from modelman.benchmark.errors import BenchmarkError
from modelman.local_process import ProcessResult as IsolateResult

from ..providers import lifecycle

# The isolable provider set now lives with the backends that implement it
# (modelman/providers/lifecycle/backends/__init__.py) — re-exported here
# under its established name so existing importers (local_control.py,
# benchmark/agent/runner.py) are unaffected.
SUPPORTED_PROVIDER_IDS = lifecycle.SUPPORTED_PROVIDER_IDS

# IsolateResult is modelman.local_process.ProcessResult under its
# established name here — shared with modelman.providers.lifecycle
# (imported there as LifecycleResult) so a future field addition applies to
# both isolation paths at once instead of drifting between two duplicate
# dataclasses.


def _require_ok(result: IsolateResult, message: str) -> IsolateResult:
    """Raise BenchmarkError(f"{message}: {result.error}") when
    `result.ok` is False, else return `result` unchanged — the
    ok=False-envelope-to-exception translation every function below needs
    at its one lifecycle call."""
    if not result.ok:
        raise BenchmarkError(f"{message}: {result.error}")
    return result


def _normalize_pairing_arg(value: str, *, is_path: bool) -> str:
    """Normalize one isolate extra-arg: expand and absolutize local_path
    values (so the isolation key is stable across equivalent spellings —
    relative vs absolute paths, trailing slashes, ~ expansion — without
    requiring the path to exist), but forward HF repo ids verbatim:
    mlx_lm.server accepts both forms for --model/--draft-model, and
    abspath'ing a repo id like "org/model" would mangle it into a
    nonexistent cwd-prefixed filesystem path.
    """
    if not is_path:
        return value
    return os.path.normpath(os.path.abspath(os.path.expanduser(value)))


def mlx_lm_server_pairing_args(
    model_id: str,
    target_local_path: str | None,
    target_repo: str | None,
    draft_local_path: str | None,
    draft_repo: str | None,
) -> tuple[str, str]:
    """Resolve the target+draft extra args an mlx_lm_server isolate call
    requires.

    mlx_lm_server has no baked-in default pairing (unlike ollama/omlx, which
    fall back to a baked-in model name), so the pairing must be passed
    through explicitly on every call. Resolution order is
    local_path over repo, matching the providers' own resolution order. The
    local_path-vs-repo fields are the discriminator: only a local_path value
    is a filesystem path that may be normalized; a repo value is an HF repo
    id and passes through verbatim.

    Raises BenchmarkError when either side has no source.
    """
    target_str = target_local_path or target_repo
    draft_str = draft_local_path or draft_repo
    if not target_str or not draft_str:
        raise BenchmarkError(
            f"mlx_lm_server model {model_id!r} is missing a target or "
            "draft repo/local_path in the registry"
        )
    return (
        _normalize_pairing_arg(target_str, is_path=target_local_path is not None),
        _normalize_pairing_arg(draft_str, is_path=draft_local_path is not None),
    )


def isolate_provider(
    provider_id: str, *extra_args: str, env: dict[str, str] | None = None, solo: bool = False
) -> IsolateResult:
    """Isolate one local provider: stop the others, start+warm this one.

    `extra_args` is forwarded verbatim to the backend's `resolve()` as its
    positional args — e.g. `isolate_provider("mlx_lm_server", target,
    draft)`. mlx_lm_server has no default target/draft pairing, so the
    pairing must be passed through explicitly on every call.

    `env`, when given, is the caller's own dict of LLM_ISOLATE_*_MODEL
    overrides (see `local_process.ENV_VAR_BY_PROVIDER`) — this is how
    `modelman start` warms up a *specific* model instead of the provider's
    baked-in default. The named variable's value is extracted and passed as
    the EXPLICIT model argument (highest precedence in the backend's
    resolution order) rather than left to the backend's own os.environ
    fallback: `env` is a dict the caller built, not necessarily equal to
    this process's real environment.

    `solo=True` skips stopping every OTHER local provider before starting
    `provider_id` — used by modelman's same-provider-only local-model
    lifecycle (local_control.py). Never passed by modelman benchmark, which
    still needs full exclusivity for clean measurement.
    """
    model: str | None = None
    if env:
        from ..local_process import ENV_VAR_BY_PROVIDER

        var = ENV_VAR_BY_PROVIDER.get(provider_id)
        if var:
            model = env.get(var)
    result = lifecycle.isolate(provider_id, model, extra_args=extra_args, solo=solo)
    return _require_ok(result, f"isolation failed for {provider_id}")


def stop_all_local_providers() -> IsolateResult:
    """Stop every local provider. Used by `modelman stop` (and by `modelman
    start` before starting a different model) — the single place that knows
    how to tear down whichever local provider happens to be running, without
    modelman having to track that itself."""
    return _require_ok(lifecycle.stop_all(), "stop-all failed")


def stop_provider(provider_id: str) -> IsolateResult:
    """Stop exactly one local provider, leaving every other one running.
    Used by the same-provider-only local-model lifecycle to replace a
    single-port provider's (omlx/omlx-6bit) occupant before starting a
    different model on it — mlx_lm_server and mtplx never need this
    (mlx_lm_server self-replaces inside its own start(); mtplx's replace
    logic lives in the lifecycle's isolate())."""
    return _require_ok(lifecycle.stop(provider_id), f"stop failed for {provider_id}")


def restore_providers() -> None:
    """Restore all local providers to their standing baseline."""
    _require_ok(lifecycle.restore(), "failed to restore providers")
