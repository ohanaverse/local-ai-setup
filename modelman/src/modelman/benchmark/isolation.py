"""Subprocess contract with local-ai-setup isolation helpers."""

from __future__ import annotations

import json
import os
import shutil
import subprocess
from dataclasses import dataclass
from typing import Any

from modelman.benchmark.errors import BenchmarkError

# What bin/llm-isolate-provider can actually isolate, mirroring that script's
# own "Supported:" header comment — the shell script is the real source of
# truth (it owns the case statement), and this constant is the one place
# modelman code checks isolability, kept here next to the rest of the
# subprocess contract with that script rather than rebuilt ad hoc elsewhere.
# llamacpp retired 2026-09-07 (issue #33): kept as a case branch in
# bin/llm-isolate-provider but not isolatable. Re-enable steps:
# docs/reference/provider-artifacts.md
SUPPORTED_PROVIDER_IDS: frozenset[str] = frozenset(
    {"ollama", "omlx", "omlx-6bit", "mlx_lm_server", "mtplx"}
)


@dataclass
class IsolateResult:
    provider: str
    model: str
    direct_url: str
    ok: bool
    error: str | None


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
    """Resolve the target+draft extra args bin/llm-isolate-provider requires
    for an mlx_lm_server isolate call.

    mlx_lm_server has no baked-in default pairing in the helper (unlike
    ollama/omlx, which fall back to a baked-in model name), so the pairing
    must be passed through explicitly on every call. Resolution order is
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


def _helper_path(name: str) -> str:
    path = shutil.which(name)
    if path is None:
        raise BenchmarkError(
            f"isolation helper '{name}' not found on PATH. Ensure local-ai-setup/bin is on PATH."
        )
    return path


def isolate_provider(provider_id: str, *extra_args: str, env: dict[str, str] | None = None) -> IsolateResult:
    """Delegate service isolation to the local-ai-setup helper.

    `extra_args` is forwarded verbatim, after `provider_id`, to the shell
    helper's argv — e.g. `isolate_provider("mlx_lm_server", target, draft)`.
    mlx_lm_server has no default target/draft pairing in the shell script
    (unlike ollama/omlx, which fall back to a baked-in model name), so the
    pairing must be passed through explicitly on every call.

    `env`, when given, is merged over a copy of the current environment and
    passed to the subprocess — this is how a caller (modelman start) makes
    the helper warm up a *specific* model instead of its baked-in default
    (LLM_ISOLATE_OLLAMA_MODEL etc., see that script's header). Omitted
    entirely from the subprocess.run() call when None, so existing callers
    that never pass env see no change in behavior.
    """
    helper = _helper_path("llm-isolate-provider")
    run_kwargs: dict[str, Any] = {"capture_output": True, "text": True, "check": False}
    if env is not None:
        run_kwargs["env"] = {**os.environ, **env}
    result = subprocess.run([helper, provider_id, *extra_args], **run_kwargs)
    if result.returncode != 0:
        raise BenchmarkError(
            f"isolation failed for {provider_id}: {result.stderr.strip() or result.stdout.strip()}"
        )
    try:
        data = json.loads(result.stdout)
    except json.JSONDecodeError as exc:
        raise BenchmarkError(
            f"isolation helper returned invalid JSON for {provider_id}: {exc}"
        ) from exc
    return IsolateResult(
        provider=data.get("provider", provider_id),
        model=data.get("model", ""),
        direct_url=data.get("direct_url", ""),
        ok=data.get("ok", False),
        error=data.get("error"),
    )


def stop_all_local_providers() -> IsolateResult:
    """Stop every local provider via the isolation helper's `stop-all`
    mode. Used by `modelman stop` (and by `modelman start` before starting
    a different model) — the single place that knows how to tear down
    whichever local provider happens to be running, without modelman
    having to track that itself."""
    helper = _helper_path("llm-isolate-provider")
    result = subprocess.run(
        [helper, "stop-all"], capture_output=True, text=True, check=False
    )
    if result.returncode != 0:
        raise BenchmarkError(
            f"stop-all failed: {result.stderr.strip() or result.stdout.strip()}"
        )
    try:
        data = json.loads(result.stdout)
    except json.JSONDecodeError as exc:
        raise BenchmarkError(f"isolation helper returned invalid JSON for stop-all: {exc}") from exc
    return IsolateResult(
        provider=data.get("provider") or "stop-all",
        model=data.get("model") or "",
        direct_url=data.get("direct_url") or "",
        ok=data.get("ok", False),
        error=data.get("error"),
    )


def restore_providers() -> None:
    """Restore all local providers via the local-ai-setup helper."""
    helper = _helper_path("llm-restore-providers")
    result = subprocess.run([helper], capture_output=True, text=True, check=False)
    if result.returncode != 0:
        raise BenchmarkError(
            f"failed to restore providers: {result.stderr.strip() or result.stdout.strip() or 'unknown error'}"
        )
