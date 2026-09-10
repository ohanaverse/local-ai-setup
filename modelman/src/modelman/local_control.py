"""modelman-owned local-model lifecycle control (issue #65 — one local
model at a time). See docs/superpowers/specs/2026-09-10-one-local-model-
at-a-time-design.md.

`modelman start`/`modelman stop` (main.py) are the only place a local
model's process is started or stopped for normal (non-benchmark) usage.
Both delegate the actual stop/start to bin/llm-isolate-provider via
modelman.benchmark.isolation — the same subprocess contract `modelman
benchmark` uses — and record which model is running in modelman.toml's
`[local].running_model` (state.py's LocalState), the marker wt's model
picker reads read-only to filter its catalog to this one local model plus
cloud models.
"""

from __future__ import annotations

from dataclasses import dataclass

from .benchmark.errors import BenchmarkError
from .benchmark.isolation import (
    SUPPORTED_PROVIDER_IDS,
    isolate_provider,
    mlx_lm_server_pairing_args,
    stop_all_local_providers,
)
from .registry import Registry, model_has_local_artifact
from .state import StateStore

# Maps a registry provider_id to the LLM_ISOLATE_*_MODEL env var
# bin/llm-isolate-provider reads for that provider's model name (see that
# script's header comment). mlx_lm_server is absent here — it takes
# target/draft as positional args (mlx_lm_server_pairing_args), not an env
# var.
_ENV_VAR_BY_PROVIDER = {
    "ollama": "LLM_ISOLATE_OLLAMA_MODEL",
    "omlx": "LLM_ISOLATE_OMLX_4BIT_MODEL",
    "omlx-6bit": "LLM_ISOLATE_OMLX_6BIT_MODEL",
}


class LocalControlError(Exception):
    """Raised for user-facing `modelman start`/`modelman stop` failures."""


@dataclass
class StartResult:
    model_id: str
    already_running: bool
    direct_url: str | None = None


@dataclass
class StopResult:
    # The marker that was cleared, or None if nothing was running.
    stopped_model_id: str | None


def start_local_model(registry: Registry, state: StateStore, model_id: str) -> StartResult:
    """Stop whatever local model is running (if any) and start model_id,
    recording it as the new `[local].running_model` marker.

    Raises LocalControlError when model_id is unknown, not a local model,
    or its provider cannot be isolated by bin/llm-isolate-provider.
    Idempotent: if model_id is already the running marker, this is a no-op
    (no stop/restart cycle) — trusts modelman's own marker rather than
    re-probing the provider, since modelman is the one thing that writes it.
    """
    try:
        model = registry.model(model_id)
    except KeyError as exc:
        raise LocalControlError(f"unknown model: {model_id}") from exc

    provider = next((p for p in registry.providers if p.id == model.provider_id), None)
    if not model_has_local_artifact(model, provider):
        raise LocalControlError(f"{model_id} is a cloud model — modelman start only runs local models")

    if model.provider_id not in SUPPORTED_PROVIDER_IDS:
        raise LocalControlError(
            f"provider {model.provider_id!r} cannot be started/stopped by modelman "
            f"(supported: {sorted(SUPPORTED_PROVIDER_IDS)})"
        )

    if state.local.running_model == model_id:
        return StartResult(model_id=model_id, already_running=True)

    try:
        stop_all_local_providers()
    except BenchmarkError as exc:
        raise LocalControlError(f"failed to stop the currently-running local model: {exc}") from exc

    extra_args: tuple[str, ...] = ()
    env: dict[str, str] | None = None
    if model.provider_id == "mlx_lm_server":
        target, draft = mlx_lm_server_pairing_args(
            model.id,
            model.fetch.local_path if model.fetch else None,
            model.fetch.repo if model.fetch else None,
            model.draft.local_path if model.draft else None,
            model.draft.repo if model.draft else None,
        )
        extra_args = (target, draft)
    else:
        env = {_ENV_VAR_BY_PROVIDER[model.provider_id]: model.model_name}

    try:
        result = isolate_provider(model.provider_id, *extra_args, env=env)
    except BenchmarkError as exc:
        raise LocalControlError(f"failed to start {model_id}: {exc}") from exc
    if not result.ok:
        raise LocalControlError(f"failed to start {model_id}: {result.error or 'unknown error'}")

    state.local.running_model = model_id
    return StartResult(model_id=model_id, already_running=False, direct_url=result.direct_url or None)


def stop_local_model(state: StateStore) -> StopResult:
    """Stop the currently-running local model (if any) and clear the
    marker. No-op when nothing is running."""
    running = state.local.running_model
    if not running:
        return StopResult(stopped_model_id=None)
    try:
        stop_all_local_providers()
    except BenchmarkError as exc:
        raise LocalControlError(f"failed to stop {running}: {exc}") from exc
    state.local.running_model = None
    return StopResult(stopped_model_id=running)
