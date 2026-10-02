"""LiteLLM helpers: `sync_routes` (the one write path, via `wt litellm sync`)
and read-only config.yaml helpers for `modelman usage`.

wt owns LiteLLM management (config.yaml routes, proxy restart, routing
state) since 2026-09-21; since #179 what is configured in registry.toml is
what gets routed, so modelman keeps no per-model exposure flag and never
writes config.yaml itself. See
docs/superpowers/specs/2026-09-21-wt-litellm-ownership-design.md.
"""

from __future__ import annotations

import os
from pathlib import Path
from typing import Any

from ruamel.yaml import YAML
from ruamel.yaml.error import YAMLError

from . import wt_bridge


class LiteLLMConfigError(Exception):
    """Raised when LiteLLM's config.yaml is missing or malformed."""


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


def sync_routes(*, litellm_path: Path | None = None) -> list[str]:
    """Run `wt litellm sync` once and return warnings to show the user.

    modelman's only LiteLLM write path since #179: every command that changes
    the registry or local-model state calls this once at the end. It never
    raises — the registry is the source of truth and the next sync converges —
    so a failure becomes a warning naming the command that fixes it.
    """
    try:
        result = wt_bridge.sync(litellm_path=litellm_path)
    except wt_bridge.WtBridgeError as exc:
        return [f"LiteLLM routes may not be synced: {exc} — run `wt litellm sync`"]
    return list(result.warnings) + _grouped_outcome_errors(result)


def _grouped_outcome_errors(result: wt_bridge.BridgeResult) -> list[str]:
    """One warning per distinct per-id error text, in first-seen order.

    wt reports each failed id as `model "<id>": <text>`; a shared cause (e.g.
    an OpenRouter secret_ref resolving empty) would otherwise repeat once per
    model on every sync. Strip that prefix, group ids by the remaining text,
    and name the single id or the count + ids of the group.
    """
    groups: dict[str, list[str]] = {}
    for o in result.outcomes:
        if not o.error:
            continue
        prefix = f'model "{o.id}": '
        text = o.error[len(prefix) :] if o.error.startswith(prefix) else o.error
        groups.setdefault(text, []).append(o.id)
    return [
        f"{ids[0]}: {text}" if len(ids) == 1 else f"{text} ({len(ids)} models: {', '.join(ids)})"
        for text, ids in groups.items()
    ]
