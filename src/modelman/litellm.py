"""LiteLLM exposure — build model_list entries and update config.yaml.

modelman manages only the `model_list` section of LiteLLM's config.yaml,
keyed by registry model id as `model_name`. `general_settings` and any
unrecognized rows are preserved. See
docs/superpowers/specs/2026-08-28-modelman-litellm-exposure-design.md.
"""

from __future__ import annotations

import contextlib
import os
import tempfile
from dataclasses import dataclass, replace
from pathlib import Path
from typing import Any

import yaml

from .registry import ModelEntry, ProviderEntry, Registry
from .state import StateStore


@dataclass(frozen=True)
class ProviderPolicy:
    """How a provider maps onto LiteLLM's model_list.

    The single source of truth for provider-specific exposure rules: the
    config writer (build_model_list_entry) and the TUI's expose gate
    (screens/models.py) both consult this table, so a new provider needs
    exactly one edit here and both stay in agreement.

    - prefix       — LiteLLM `model` field prefix. llamacpp's points at
                     the fixed `openai/local-model` (its api_base is the
                     local server).
    - fixed_model  — use `prefix` verbatim as the model string instead of
                     prefixing the model name.
    - api_key      — literal api_key to write, or None to omit the field.
    - secret_ref   — api_key comes from provider.auth.secret_ref instead.
    - cloud        — the model lives remotely: exempt from the
                     "must be downloaded" expose gate.
    """

    prefix: str
    fixed_model: bool = False
    api_key: str | None = None
    secret_ref: bool = False
    cloud: bool = False


# Ordered by the registry's provider ids. ollama needs no api_key; omlx and
# llamacpp are local OpenAI-compatible servers that ignore the key but
# require the field; openrouter uses the configured secret_ref.
PROVIDER_POLICIES: dict[str, ProviderPolicy] = {
    "ollama": ProviderPolicy(prefix="ollama_chat/"),
    "omlx": ProviderPolicy(prefix="openai/", api_key="not-needed"),
    "llamacpp": ProviderPolicy(prefix="openai/local-model", fixed_model=True, api_key="dummy-key"),
    "openrouter": ProviderPolicy(prefix="openrouter/", secret_ref=True, cloud=True),
}


def provider_policy(provider_id: str) -> ProviderPolicy | None:
    """The ProviderPolicy for `provider_id`, or None if unmapped."""
    return PROVIDER_POLICIES.get(provider_id)


def is_cloud(provider_id: str) -> bool:
    """True when a provider's models live remotely (no local download).

    Unknown providers are treated as local so the TUI gate stays
    conservative; the writer rejects them outright anyway.
    """
    policy = provider_policy(provider_id)
    return policy.cloud if policy is not None else False


class LiteLLMConfigError(Exception):
    """Raised when LiteLLM's config.yaml is missing or malformed."""


class ExposeError(Exception):
    """Raised when a model cannot be exposed (not downloaded, unknown, etc.)."""


def default_litellm_config_path() -> Path:
    """Compute the LiteLLM config path lazily so env overrides work in tests."""
    return Path(
        os.environ.get("MODELMAN_LITELLM_CONFIG", "~/.config/litellm/config.yaml")
    ).expanduser()


def build_model_list_entry(model: ModelEntry, provider: ProviderEntry) -> dict[str, Any]:
    """Build a LiteLLM `model_list` row for a registry model.

    `model_name` is the registry model id; `litellm_params.model` uses the
    provider's prefix; `api_base` comes from the provider's auth config;
    `model_info` is copied from the model.
    """
    policy = provider_policy(provider.id)
    if policy is None:
        raise ExposeError(f"provider {provider.id!r} has no LiteLLM mapping")
    # fixed_model providers use the prefix verbatim; others prefix the
    # model name.
    litellm_model = policy.prefix if policy.fixed_model else f"{policy.prefix}{model.model_name}"
    params: dict[str, Any] = {
        "model": litellm_model,
        "api_base": provider.auth.base_url,
    }
    if policy.secret_ref:
        params["api_key"] = provider.auth.secret_ref
    elif policy.api_key is not None:
        params["api_key"] = policy.api_key
    entry: dict[str, Any] = {
        "model_name": model.id,
        "litellm_params": params,
    }
    if model.model_info:
        entry["model_info"] = dict(model.model_info)
    return entry


def load_litellm_config(path: Path) -> dict[str, Any]:
    """Read LiteLLM's config.yaml. Errors if missing, malformed, or not a mapping."""
    if not path.exists():
        raise LiteLLMConfigError(f"LiteLLM config not found: {path}")
    with open(path) as f:
        try:
            data = yaml.safe_load(f)
        except yaml.YAMLError as exc:
            # Hand-edited configs can be syntactically invalid; surface
            # that as a LiteLLMConfigError so the CLI prints "error: ..."
            # instead of a raw yaml traceback.
            raise LiteLLMConfigError(f"LiteLLM config is not valid YAML: {path}\n{exc}") from None
    if not isinstance(data, dict):
        raise LiteLLMConfigError(f"LiteLLM config is not a mapping: {path}")
    return data


def _model_list_rows(config: dict[str, Any]) -> list[Any] | None:
    """The model_list rows, tolerating a missing/None/non-list section.

    Returns None when there is no usable list (absent, null, or a scalar
    left by a hand edit); callers preserve the section untouched rather
    than crashing on rows they don't understand.
    """
    rows = config.get("model_list")
    if rows is None:
        # Absent, or an explicit null left by a hand edit — start a list.
        config["model_list"] = rows = []
    if not isinstance(rows, list):
        return None
    return rows


def set_exposed(config: dict[str, Any], model_id: str, entry: dict[str, Any]) -> None:
    """Add or replace the model_list row keyed by `model_id`.

    Rows that aren't dicts (hand-edited scalars etc.) are skipped, not
    crashed on — they're preserved on write as the module docstring
    promises.
    """
    rows = _model_list_rows(config)
    if rows is None:
        raise LiteLLMConfigError(
            f"model_list is not a list; refusing to edit {config.get('model_list')!r}"
        )
    for i, row in enumerate(rows):
        if isinstance(row, dict) and row.get("model_name") == model_id:
            rows[i] = entry
            return
    rows.append(entry)


def remove_exposed(config: dict[str, Any], model_id: str) -> None:
    """Remove the model_list row keyed by `model_id` (no-op if absent).

    Non-dict rows are never ours, so they're left in place.
    """
    rows = config.get("model_list")
    if not isinstance(rows, list):
        return
    config["model_list"] = [
        r for r in rows if not (isinstance(r, dict) and r.get("model_name") == model_id)
    ]


def save_litellm_config(config: dict[str, Any], path: Path) -> None:
    """Write config.yaml atomically (temp file + rename).

    NOTE: PyYAML does not preserve comments/formatting on round-trip.
    Unrecognized rows and general_settings are preserved as data; comments
    are not. This is an accepted limitation of the current implementation.

    The replacement file inherits the existing file's permission bits:
    mkstemp creates 0600 and os.replace preserves that mode, so without
    the chmod a config readable by a LiteLLM service running as another
    user would silently tighten to 0600 on the first write.
    """
    path.parent.mkdir(parents=True, exist_ok=True)
    try:
        existing_mode = path.stat().st_mode & 0o777
    except OSError:
        existing_mode = None
    fd, tmp_name = tempfile.mkstemp(dir=path.parent, prefix=f".{path.name}.", suffix=".tmp")
    try:
        if existing_mode is not None:
            os.fchmod(fd, existing_mode)
        with os.fdopen(fd, "w") as f:
            yaml.safe_dump(config, f, sort_keys=False, default_flow_style=False)
        os.replace(tmp_name, path)
    except BaseException:
        with contextlib.suppress(OSError):
            os.unlink(tmp_name)
        raise


def _set_exposed_flag(state: StateStore, model_id: str, exposed: bool) -> None:
    existing = state.get(model_id)
    if model_id not in state.models and not exposed:
        # Unexposing a model with no state entry (e.g. it was deleted
        # earlier in this apply) must not materialize an all-default
        # row in modelman.toml — there's nothing to clear.
        return
    state.set(model_id, replace(existing, litellm_exposed=exposed))


def _validated_entry(registry: Registry, state: StateStore, model_id: str) -> dict[str, Any]:
    """Resolve a registry model and build its config row, or raise ExposeError."""
    try:
        model = registry.model(model_id)
    except KeyError:
        raise ExposeError(f"model {model_id!r} not found in registry") from None
    try:
        provider = registry.provider(model.provider_id)
    except KeyError:
        # A hand-edited registry can reference a provider it doesn't
        # define; report that as an ExposeError (the CLI's caught type)
        # instead of an uncaught KeyError traceback.
        raise ExposeError(
            f"model {model_id!r} references unknown provider {model.provider_id!r}"
        ) from None
    policy = provider_policy(model.provider_id)
    if policy is None:
        raise ExposeError(f"provider {model.provider_id!r} has no LiteLLM mapping")
    if not policy.cloud and not state.get(model_id).downloaded:
        raise ExposeError(f"model {model_id!r} is not downloaded")
    return build_model_list_entry(model, provider)


def expose_model(
    registry: Registry,
    state: StateStore,
    model_id: str,
    litellm_path: Path,
) -> None:
    """Expose a model through LiteLLM: write its model_list entry and flip
    the modelman.toml flag. Errors if the model is unknown, its provider
    has no LiteLLM mapping, or it isn't downloaded (unless cloud)."""
    entry = _validated_entry(registry, state, model_id)
    config = load_litellm_config(litellm_path)
    set_exposed(config, model_id, entry)
    save_litellm_config(config, litellm_path)
    _set_exposed_flag(state, model_id, True)


def unexpose_model(
    state: StateStore,
    model_id: str,
    litellm_path: Path,
) -> None:
    """Remove a model's model_list entry and clear its modelman.toml flag.

    A no-op success when the model no longer exists in the registry —
    e.g. it was deleted earlier in the same apply() cycle, after which
    the config row (keyed by id) is the only thing left to clean up.
    """
    config = load_litellm_config(litellm_path)
    remove_exposed(config, model_id)
    save_litellm_config(config, litellm_path)
    _set_exposed_flag(state, model_id, False)


def apply_expose_queue(
    registry: Registry,
    state: StateStore,
    exposes: list[tuple[str, bool]],
    litellm_path: Path,
) -> list[tuple[str, bool, str | None]]:
    """Apply a queue of (model_id, target_exposed) pairs with a single
    config load and a single atomic save.

    This is the apply()-time path: one YAML parse and one rename for the
    whole queue instead of one per model, and config.yaml transitions
    from before to after in one step rather than risking a half-updated
    file if the process dies mid-queue. The standalone expose_model /
    unexpose_model remain for the CLI's one-model-at-a-time use.

    Returns one (model_id, target, error) outcome per item, error None
    when applied. Config-level failures (missing/unwritable file)
    propagate to the caller; per-model validation failures are reported
    per item and don't block the rest of the queue. Flags flip only
    after the save succeeds, so state never claims an exposure the
    config file lost.
    """
    if not exposes:
        return []
    config = load_litellm_config(litellm_path)
    outcomes: list[tuple[str, bool, str | None]] = []
    succeeded: list[tuple[str, bool]] = []
    for model_id, target in exposes:
        try:
            if target:
                set_exposed(config, model_id, _validated_entry(registry, state, model_id))
            else:
                remove_exposed(config, model_id)
        except ExposeError as exc:
            outcomes.append((model_id, target, str(exc)))
            continue
        outcomes.append((model_id, target, None))
        succeeded.append((model_id, target))
    if succeeded:
        save_litellm_config(config, litellm_path)
        for model_id, target in succeeded:
            _set_exposed_flag(state, model_id, target)
    return outcomes
