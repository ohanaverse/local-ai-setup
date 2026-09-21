"""LiteLLM exposure — modelman's gates, wt's writes.

Since 2026-09-21 **wt owns LiteLLM management**: building `model_list`
rows, editing config.yaml (comments and untouched sections preserved),
enforcing the launcher-required settings, and restarting the proxy all
live in `wt/internal/litellm` and are reached through
`modelman.wt_bridge` (`wt litellm expose|unexpose`). What stays here is:

- the **gates** modelman applies against its own IN-MEMORY state before
  delegating (model/provider exist, not native, provider mapped, ready or
  cloud) — wt reads registry.toml and modelman.toml from disk and cannot
  see a queued ready toggle or an unsaved flag, so modelman keeps the
  gate and tells wt `--skip-ready-gate`;
- the **display predicates** the TUI renders with (`is_effectively_exposed`,
  `passes_ready_gate`, `is_cloud`, `is_cloud_effective`), whose provider
  table comes from `wt litellm providers` via
  `wt_bridge.provider_cloud_flags()`;
- the **exposed flag** in modelman.toml, which modelman still owns and
  flips only after wt reports the route was written;
- read-only helpers over config.yaml used by `modelman usage`
  (`load_litellm_config`, `_database_url_from_config`,
  `_reverse_model_index`).

See docs/superpowers/specs/2026-09-21-wt-litellm-ownership-design.md.
"""

from __future__ import annotations

import os
from collections.abc import Callable
from dataclasses import dataclass, replace
from pathlib import Path
from typing import Any

from ruamel.yaml import YAML
from ruamel.yaml.error import YAMLError

from . import wt_bridge
from .registry import LOCATION_CLOUD, ModelEntry, Registry
from .state import StateStore


@dataclass(frozen=True)
class ProviderPolicy:
    """Minimal view of wt's provider mapping.

    wt owns the real table (prefix, api_key, fixed_model — everything the
    `model_list` row needs); modelman only needs to know that a mapping
    exists at all (an unmapped provider cannot be exposed) and whether the
    provider is cloud (exempt from the ready gate).
    """

    cloud: bool = False


class LiteLLMConfigError(Exception):
    """Raised when LiteLLM's config.yaml is missing or malformed, or when
    wt — which owns every write to it — could not be reached."""


class ExposeError(Exception):
    """Raised when a model cannot be exposed (not ready, unknown, etc.)."""


def provider_policy(provider_id: str) -> ProviderPolicy | None:
    """The ProviderPolicy for `provider_id`, or None if unmapped.

    Reached from TUI render code for every row, so it never raises: when
    wt cannot be run, `provider_cloud_flags()` returns {} (warning once on
    stderr) and every provider reads as unmapped and non-cloud. That is
    the accepted degraded display behavior; the write paths below refuse
    outright instead (see `_provider_flags_for_write`).
    """
    flags = wt_bridge.provider_cloud_flags()
    return ProviderPolicy(cloud=flags[provider_id]) if provider_id in flags else None


def is_cloud(provider_id: str) -> bool:
    """True when a provider's models live remotely (no local download).

    Unknown providers are treated as local so the TUI gate stays
    conservative; the writer rejects them outright anyway.
    """
    policy = provider_policy(provider_id)
    return policy.cloud if policy is not None else False


def is_cloud_effective(model: ModelEntry, registry: Registry) -> bool:
    """True when a model should be exempt from the ready gate.

    A model is "effectively cloud" for exposure purposes when any of:
    - its provider policy declares it cloud (openrouter), or
    - the model itself is explicitly marked `location = "cloud"`, or
    - its provider's registry entry is `location = "cloud"` — the same
      model-then-provider resolution wt's `ResolveLocation` applies
      (issue #46 parity).

    An unknown provider id (hand-edited registry) is treated as
    not-cloud, mirroring `is_cloud`'s conservative fallback.

    Note: native providers are excluded upstream — `is_effectively_exposed`
    short-circuits on native models before this predicate runs.
    """
    if is_cloud(model.provider_id) or model.location == LOCATION_CLOUD:
        return True
    try:
        return registry.provider(model.provider_id).location == LOCATION_CLOUD
    except KeyError:
        return False


def passes_ready_gate(
    model: ModelEntry,
    state: StateStore,
    registry: Registry,
    ready_override: bool | None = None,
) -> bool:
    """Whether a model passes the expose-time readiness gate.

    The gate apply-time validation enforces (`_validate_locally` rejects an
    expose with "model is not ready"): ready is required unless the model
    is effectively cloud — cloud rows are exempt from the ready gate.

    This is a routing gate, distinct from the catalog/display predicate
    `is_effectively_exposed`. In particular it does NOT include the native
    exemption: native providers have no LiteLLM mapping and are rejected by
    `_validate_locally` before this gate is reached.

    modelman keeps this gate instead of leaving it to wt because it runs
    against the IN-MEMORY state — a queued ready toggle, or a flag not yet
    merged into modelman.toml, that wt's on-disk read cannot see. wt is
    therefore always called with `skip_ready_gate=True`.

    Args:
        model: The registry model entry to check.
        state: StateStore for the persisted ready flag.
        registry: The model registry, for provider-location resolution.
        ready_override: Override the persisted ready flag (used by the TUI
            to project a queued ready toggle before apply runs).

    Returns:
        True if the model would pass the ready gate, False otherwise.
    """
    ready = ready_override if ready_override is not None else state.get(model.id).ready
    return ready or is_cloud_effective(model, registry)


def is_effectively_exposed(
    model: ModelEntry,
    state: StateStore,
    registry: Registry,
    exposed_override: bool | None = None,
    ready_override: bool | None = None,
) -> bool:
    """Determine if a model is effectively exposed (catalog/display predicate).

    A model is effectively exposed when ANY of these hold:
    - it is a native model (provider auth.type == "native"); native providers
      cannot route through LiteLLM, so they are always considered exposed, OR
    - its `exposed` flag is True (or `exposed_override` is True) AND
      it passes the ready gate: it is ready (or `ready_override` is True) or
      it is a cloud model (exempt from the ready gate).

    This predicate is intentionally distinct from the apply/routing gate
    (`passes_ready_gate` / `_validate_locally`): the apply gate governs whether
    a model can be written into LiteLLM's config and may flip the flag it is
    checking. Native models have no LiteLLM mapping, so they are catalog-only
    and are rejected earlier by `_validate_locally`'s native guard — even if
    wt's provider table happens to map their provider (hand-edited registry).

    Args:
        model: The registry model entry to check.
        state: StateStore for ready/exposed flags.
        registry: The model registry, for provider-location resolution.
        exposed_override: Override the persisted exposed flag.
            Applies to non-native models only — native models are
            unconditionally exposed and ignore this override (and the
            persisted flag) entirely.
        ready_override: Override the persisted ready flag. Likewise
            ignored for native models.

    Returns:
        True if the model should show as exposed in the catalog, False otherwise.
    """
    if model.native:
        return True
    exposed = exposed_override if exposed_override is not None else state.get(model.id).exposed
    if not exposed:
        return False
    return passes_ready_gate(model, state, registry, ready_override=ready_override)


def default_litellm_config_path() -> Path:
    """Compute the LiteLLM config path lazily so env overrides work in tests."""
    return Path(
        os.environ.get("MODELMAN_LITELLM_CONFIG", "~/.config/litellm/config.yaml")
    ).expanduser()


def load_litellm_config(path: Path) -> dict[str, Any]:
    """Read LiteLLM's config.yaml. Errors if missing, malformed, or not a mapping.

    Read-only: wt owns every write to this file, so comments and layout do
    not have to survive a round-trip here and a plain safe load is enough.
    Callers are `modelman usage` (`_database_url_from_config`,
    `_reverse_model_index`).
    """
    if not path.exists():
        raise LiteLLMConfigError(f"LiteLLM config not found: {path}")
    with open(path) as f:
        try:
            data = YAML(typ="safe").load(f)
        except YAMLError as exc:
            # Hand-edited configs can be syntactically invalid; surface
            # that as a LiteLLMConfigError so the CLI prints "error: ..."
            # instead of a raw yaml traceback.
            raise LiteLLMConfigError(f"LiteLLM config is not valid YAML: {path}\n{exc}") from None
    if not isinstance(data, dict):
        raise LiteLLMConfigError(f"LiteLLM config is not a mapping: {path}")
    return data


def _database_url_from_config(config: dict[str, Any]) -> str | None:
    """Read general_settings.database_url from a parsed LiteLLM config."""
    general = config.get("general_settings") or {}
    return general.get("database_url")


def _reverse_model_index(model_list: list[dict[str, Any]]) -> dict[str, str]:
    """Map litellm_params.model -> model_list.model_name.

    Used to recover the registry model id when LiteLLM_SpendLogs.model_name
    is NULL but the litellm_model field is present. If two entries share the
    same litellm_params.model, the first one in the list wins.
    """
    index: dict[str, str] = {}
    for entry in model_list:
        if not isinstance(entry, dict):
            continue
        model_name = entry.get("model_name")
        litellm_params = entry.get("litellm_params")
        if not isinstance(litellm_params, dict):
            continue
        litellm_model = litellm_params.get("model")
        if model_name and litellm_model and litellm_model not in index:
            index[litellm_model] = model_name
    return index


def _set_exposed_flag(state: StateStore, model_id: str, exposed: bool) -> bool:
    """Flip a model's exposed flag; return True if it changed.

    Returns False when the flag was already at the target value, or when
    unexposing a model with no state entry (e.g. it was deleted earlier in
    this apply) — in that case there's nothing to clear and we must not
    materialize an all-default row in modelman.toml.
    """
    existing = state.get(model_id)
    if model_id not in state.models and not exposed:
        return False
    if existing.exposed == exposed:
        return False
    state.set(model_id, replace(existing, exposed=exposed))
    return True


def _provider_flags_for_write() -> dict[str, bool]:
    """wt's provider table, or LiteLLMConfigError when it can't be read.

    The write paths must not silently degrade the way the display paths do:
    an empty table (wt missing, or its `providers --json` failed — see
    `wt_bridge.provider_cloud_flags`) would otherwise read as "this
    provider has no LiteLLM mapping" and blame the registry for a missing
    binary. wt always maps at least ollama, so empty means unavailable.
    """
    flags = wt_bridge.provider_cloud_flags()
    if not flags:
        raise LiteLLMConfigError(
            "cannot read wt's LiteLLM provider table (wt owns LiteLLM config writes); "
            "install it with `make install`"
        )
    return flags


def _validate_locally(registry: Registry, state: StateStore, model_id: str) -> None:
    """The gates modelman applies against its own in-memory state before
    handing an expose to wt: the model exists, its provider exists, it is
    not native, its provider has a LiteLLM mapping, and it is ready or
    cloud. wt re-checks everything except readiness (modelman's state may
    hold ready/exposed toggles not yet on disk — hence skip_ready_gate).
    """
    try:
        model = registry.model(model_id)
    except KeyError:
        raise ExposeError(f"model {model_id!r} not found in registry") from None
    try:
        registry.provider(model.provider_id)
    except KeyError:
        # A hand-edited registry can reference a provider it doesn't
        # define; report that as an ExposeError (the CLI's caught type)
        # instead of an uncaught KeyError traceback.
        raise ExposeError(
            f"model {model_id!r} references unknown provider {model.provider_id!r}"
        ) from None
    if model.native:
        # Native ⇒ no LiteLLM policy invariant (#47): a native provider
        # must never produce a LiteLLM row, even if wt's provider table
        # happens to map it (hand-edited registry).
        raise ExposeError(
            f"provider {model.provider_id!r} is native and cannot be exposed through LiteLLM"
        )
    if model.provider_id not in _provider_flags_for_write():
        # Provider is absent from wt's table (a new provider not yet
        # mapped, or a hand-edited registry). Native providers never reach
        # this check — the native guard above rejects them first.
        raise ExposeError(f"provider {model.provider_id!r} has no LiteLLM mapping")
    if not passes_ready_gate(model, state, registry):
        raise ExposeError(f"model {model_id!r} is not ready")


def _bridge(
    call: Callable[..., wt_bridge.BridgeResult], *args: Any, **kwargs: Any
) -> wt_bridge.BridgeResult:
    """Run a wt_bridge call, turning a bridge-level failure (wt missing,
    unreadable config.yaml, unparseable output) into the LiteLLMConfigError
    every caller already handles. Raised BEFORE any flag is flipped:
    nothing was applied on the wt side either."""
    try:
        return call(*args, **kwargs)
    except wt_bridge.WtBridgeError as exc:
        raise LiteLLMConfigError(str(exc)) from None


def _outcome_error(result: wt_bridge.BridgeResult, model_id: str) -> str | None:
    """wt's per-id rejection reason for `model_id`, or None when it applied."""
    for outcome in result.outcomes:
        if outcome.id == model_id:
            return outcome.error
    return None


def expose_model(
    registry: Registry,
    state: StateStore,
    model_id: str,
    litellm_path: Path,
) -> list[str]:
    """Expose a model through LiteLLM: validate against modelman's
    in-memory state, have wt write the `model_list` row and restart the
    proxy, then flip the modelman.toml flag.

    Errors if the model is unknown, its provider has no LiteLLM mapping,
    or it isn't ready (unless cloud) — `ExposeError`, as before — or if wt
    itself could not run (`LiteLLMConfigError`). The flag flips only after
    wt reports success, so state never claims an exposure config.yaml
    lost. wt writes nothing and does not bounce the proxy when the row is
    already byte-identical.

    Returns non-fatal proxy-restart warnings (from wt) so the CLI can
    surface them; the TUI path uses apply_expose_queue instead.
    """
    _validate_locally(registry, state, model_id)
    result = _bridge(wt_bridge.expose, [model_id], litellm_path=litellm_path, skip_ready_gate=True)
    error = _outcome_error(result, model_id)
    if error is not None:
        raise ExposeError(error)
    _set_exposed_flag(state, model_id, True)
    return result.warnings


def unexpose_model(
    state: StateStore,
    model_id: str,
    litellm_path: Path,
) -> list[str]:
    """Remove a model's `model_list` row (wt) and clear its modelman.toml
    flag.

    No registry validation: unexposing never needed one, so a model
    deleted earlier in the same apply cycle still has its row removed, and
    no state row is materialized for the corpse. A true no-op — row
    absent, flag already false — writes nothing and does not bounce the
    proxy (wt's own changed-detection).

    Returns non-fatal proxy-restart warnings (from wt) so the CLI can
    surface them; the TUI path uses apply_expose_queue instead.
    """
    result = _bridge(wt_bridge.unexpose, [model_id], litellm_path=litellm_path)
    error = _outcome_error(result, model_id)
    if error is not None:
        # wt's remove path has no per-id rejection today; if one ever
        # appears, refuse to clear a flag whose row may still be routed.
        # LiteLLMConfigError (not ExposeError) because that is what every
        # unexpose caller catches.
        raise LiteLLMConfigError(error)
    _set_exposed_flag(state, model_id, False)
    return result.warnings


def apply_expose_queue(
    registry: Registry,
    state: StateStore,
    exposes: list[tuple[str, bool]],
    litellm_path: Path,
) -> tuple[list[tuple[str, bool, str | None]], list[str]]:
    """Apply a queue of (model_id, target_exposed) pairs through wt.

    The queue is split into an expose batch and an unexpose batch, so the
    whole queue costs at most two `wt litellm` calls — and, worst case,
    two proxy restarts. Accepted: wt restarts only when it actually
    changed the document, the two batches are independent operations, and
    a single combined call would mean teaching the bridge a
    both-directions verb for a cost of one extra bounce.

    An id that fails modelman's own gate never reaches wt.

    Returns (outcomes, warnings). `outcomes` is one (model_id, target,
    error) per queue item, in the queue's order, error None when applied.
    Bridge-level failures (wt missing, unreadable config.yaml) propagate
    to the caller as LiteLLMConfigError with no flag touched; per-model
    validation failures — modelman's or wt's — are reported per item and
    don't block the rest of the queue. Flags flip only after wt reports
    success for that id. `warnings` carries wt's non-fatal proxy-restart
    notices from both batches so the caller can surface them on the UI
    thread instead of printing to stderr from a worker thread.
    """
    if not exposes:
        return [], []
    errors: dict[str, str] = {}
    to_add: list[str] = []
    to_remove: list[str] = []
    for model_id, target in exposes:
        if not target:
            to_remove.append(model_id)
            continue
        try:
            _validate_locally(registry, state, model_id)
        except ExposeError as exc:
            errors[model_id] = str(exc)
            continue
        to_add.append(model_id)

    warnings: list[str] = []
    if to_add:
        result = _bridge(wt_bridge.expose, to_add, litellm_path=litellm_path, skip_ready_gate=True)
        warnings += result.warnings
        for outcome in result.outcomes:
            if outcome.error:
                errors[outcome.id] = outcome.error
    if to_remove:
        result = _bridge(wt_bridge.unexpose, to_remove, litellm_path=litellm_path)
        warnings += result.warnings
        for outcome in result.outcomes:
            if outcome.error:
                errors[outcome.id] = outcome.error

    outcomes: list[tuple[str, bool, str | None]] = []
    for model_id, target in exposes:
        error = errors.get(model_id)
        outcomes.append((model_id, target, error))
        if error is None:
            _set_exposed_flag(state, model_id, target)
    return outcomes, warnings


def apply_unexpose_queue(
    state: StateStore,
    model_ids: list[str],
    litellm_path: Path,
) -> list[str]:
    """Un-expose every id in `model_ids` in one `wt litellm unexpose` call
    — the un-expose counterpart to apply_expose_queue, used by
    stop_all_local_models() so stopping N exposed models bounces the
    LiteLLM proxy once instead of N times.

    Unlike apply_expose_queue, removing a route never validates against
    the registry and cannot fail per-model, so this takes no Registry and
    has no per-item outcome to report: it either applies the whole batch
    or raises. Bridge-level failures (wt missing, unreadable config.yaml)
    propagate to the caller, same as apply_expose_queue and
    unexpose_model — the caller decides how to turn that into a warning.
    Flags flip only after wt reports success.

    Returns wt's proxy-restart warnings (empty on success or when nothing
    changed), matching apply_expose_queue's warnings contract.
    """
    if not model_ids:
        return []
    result = _bridge(wt_bridge.unexpose, model_ids, litellm_path=litellm_path)
    warnings = list(result.warnings)
    for model_id in model_ids:
        error = _outcome_error(result, model_id)
        if error is not None:
            # No per-item outcome channel here; surface it as a warning
            # and leave the flag alone rather than claiming an un-exposure
            # wt refused.
            warnings.append(f"{model_id} could not be un-exposed: {error}")
            continue
        _set_exposed_flag(state, model_id, False)
    return warnings
