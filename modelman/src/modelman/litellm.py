"""LiteLLM exposure — build model_list entries and update config.yaml.

modelman manages the `model_list` section of LiteLLM's config.yaml,
keyed by registry model id as `model_name`, plus a small curated set of
launcher-required settings it *owns* and ensures on every write (see
`ensure_litellm_settings`). Everything else — `general_settings`,
`router_settings`, unknown keys, hand-written comments — passes through
untouched (ruamel round-trip keeps layout byte-identical), and a write
that changes nothing saves nothing. Known limitation: comments attached
to a specific `model_list` row (a comment on the line before a row) are
best-effort — they survive when the list is untouched, but a comment
positioned next to a row that modelman removes or replaces can be
dropped. Top-level and section-level comments (e.g. the hand-written
`drop_params` note) always survive. See
docs/superpowers/specs/2026-08-31-litellm-settings-persistence-design.md
and docs/superpowers/specs/2026-08-28-modelman-litellm-exposure-design.md.
"""

from __future__ import annotations

import contextlib
import copy
import os
import subprocess
import tempfile
from dataclasses import dataclass, replace
from pathlib import Path
from typing import Any

from ruamel.yaml import YAML
from ruamel.yaml.error import YAMLError

from .registry import Cost, ModelEntry, ProviderEntry, Registry
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

# Launcher-required settings modelman owns and ensures on every config
# write (settings-persistence spec, 2026-08-31).
_BRIDGE_PREFIX = "ollama_chat/"  # LiteLLM's chat-completions→ollama bridge
_BRIDGE_DROP_PARAMS = ("reasoning_effort",)  # BerriAI/litellm#37452 workaround
_TOKENS_PER_MILLION = 1_000_000


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


def is_cloud_effective(model: ModelEntry) -> bool:
    """True when a model should be exempt from the ready gate.

    A model is "effectively cloud" for exposure purposes when:
    - its provider policy declares it cloud (openrouter), or
    - the model itself is explicitly marked `location = "cloud"`.

    Note: a provider whose *provider* location is "cloud" (native/agent
    providers) is *not* included here; those rows are flag-only and are
    never routed through LiteLLM, mirroring `model_has_local_artifact`.
    """
    return is_cloud(model.provider_id) or model.location == "cloud"


class LiteLLMConfigError(Exception):
    """Raised when LiteLLM's config.yaml is missing or malformed."""


def _rt_yaml() -> YAML:
    """A round-trip YAML instance: preserves comments, layout, and quote
    style across the load→mutate→save cycle (settings-persistence spec,
    Decision 5). The wide `width` keeps long values (api keys) on one
    line instead of folding them across lines the way PyYAML would."""
    yaml = YAML(typ="rt")
    yaml.preserve_quotes = True
    yaml.width = 4096
    return yaml


class ExposeError(Exception):
    """Raised when a model cannot be exposed (not ready, unknown, etc.)."""


def default_litellm_config_path() -> Path:
    """Compute the LiteLLM config path lazily so env overrides work in tests."""
    return Path(
        os.environ.get("MODELMAN_LITELLM_CONFIG", "~/.config/litellm/config.yaml")
    ).expanduser()


def _pricing_model_info(cost: Cost) -> dict[str, Any]:
    """Derive LiteLLM `model_info` pricing keys from a registry Cost.

    Per-token prices are converted from "per million" to "per token".
    Cache prices are written to both creation and read keys. When a price
    is absent, an explicit zero is emitted so LiteLLM bypasses budget
    checks for free or partially-specified models. Subscription pricing
    is intentionally not passed through.
    """
    info: dict[str, Any] = {}

    if cost.input_price_per_million is not None:
        info["input_cost_per_token"] = cost.input_price_per_million / _TOKENS_PER_MILLION
    else:
        info["input_cost_per_token"] = 0

    if cost.output_price_per_million is not None:
        info["output_cost_per_token"] = cost.output_price_per_million / _TOKENS_PER_MILLION
    else:
        info["output_cost_per_token"] = 0

    if cost.cache_price_per_million is not None:
        per_token = cost.cache_price_per_million / _TOKENS_PER_MILLION
        info["cache_creation_input_token_cost"] = per_token
        info["cache_read_input_token_cost"] = per_token

    return info


def build_model_list_entry(model: ModelEntry, provider: ProviderEntry) -> dict[str, Any]:
    """Build a LiteLLM `model_list` row for a registry model.

    `model_name` is the registry model id; `litellm_params.model` uses the
    provider's prefix; `api_base` comes from the provider's auth config;
    `model_info` starts with derived pricing and is then merged with the
    model's own `model_info`, so hand-written keys can override derived
    pricing when needed.
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
    cost = model.cost if model.cost is not None else Cost()
    info = _pricing_model_info(cost)
    if model.model_info:
        info.update(model.model_info)
    entry["model_info"] = info
    return entry


def load_litellm_config(path: Path) -> dict[str, Any]:
    """Read LiteLLM's config.yaml. Errors if missing, malformed, or not a mapping.

    Round-trip mode so comments and formatting survive modelman's own
    writes; a CommentedMap is a dict subclass, so callers see the
    document they've always seen.
    """
    if not path.exists():
        raise LiteLLMConfigError(f"LiteLLM config not found: {path}")
    with open(path) as f:
        try:
            data = _rt_yaml().load(f)
        except YAMLError as exc:
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


def set_exposed(config: dict[str, Any], model_id: str, entry: dict[str, Any]) -> None:
    """Add or replace the model_list row keyed by `model_id`.

    Rows that aren't dicts (hand-edited scalars etc.) are skipped, not
    crashed on — they're preserved on write as the module docstring
    promises.

    When replacing an existing row, any user-managed
    `litellm_params.additional_drop_params` is preserved. The
    settings-persistence spec treats that key as presence-based (Decision
    3), so re-exposing a model must not reset an extended list or a
    deliberate `[]` back to the default.
    """
    rows = _model_list_rows(config)
    if rows is None:
        raise LiteLLMConfigError(
            f"model_list is not a list; refusing to edit {config.get('model_list')!r}"
        )
    for i, row in enumerate(rows):
        if isinstance(row, dict) and row.get("model_name") == model_id:
            old_params = row.get("litellm_params")
            new_params = entry.get("litellm_params")
            if (
                isinstance(old_params, dict)
                and isinstance(new_params, dict)
                and "additional_drop_params" in old_params
                and "additional_drop_params" not in new_params
            ):
                entry = copy.deepcopy(entry)
                entry["litellm_params"]["additional_drop_params"] = old_params[
                    "additional_drop_params"
                ]
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


def ensure_litellm_settings(config: dict[str, Any]) -> bool:
    """Ensure launcher-required LiteLLM settings are present.

    Returns True iff the document changed. Two curated rules
    (settings-persistence spec, Decision 3):

    - `litellm_settings.drop_params` is **value-enforced** — set to
      `True` when missing or not `true`. copilot sends
      `parallel_tool_calls`, which the ollama backend rejects with
      `400 UnsupportedParamsError` unless LiteLLM drops it.
    - `additional_drop_params: ["reasoning_effort"]` is
      **presence-based** — added to every model_list row whose
      `litellm_params.model` starts with `ollama_chat/` and lacks the
      key. codex's `/v1/responses` `reasoning` object crashes LiteLLM
      1.98.x's bridge (BerriAI/litellm#37452); an existing key of any
      value (an extended list, a deliberate `[]`) marks the row
      user-managed and is left untouched.

    Tolerates hand-edited degenerate shapes the way the rest of the
    module does: a scalar `litellm_settings` or non-dict `model_list`
    rows are skipped, never crashed on.
    """
    changed = False
    settings = config.get("litellm_settings")
    if settings is None:
        settings = config["litellm_settings"] = {}
    if isinstance(settings, dict) and settings.get("drop_params") is not True:
        settings["drop_params"] = True
        changed = True
    rows = config.get("model_list")
    if isinstance(rows, list):
        for row in rows:
            if not isinstance(row, dict):
                continue
            params = row.get("litellm_params")
            if not isinstance(params, dict):
                continue
            model = params.get("model")
            if (
                isinstance(model, str)
                and model.startswith(_BRIDGE_PREFIX)
                and "additional_drop_params" not in params
            ):
                params["additional_drop_params"] = list(_BRIDGE_DROP_PARAMS)
                changed = True
    return changed


def save_litellm_config(config: dict[str, Any], path: Path) -> None:
    """Write config.yaml atomically (temp file + rename).

    Round-trip mode preserves the comments and layout of content
    modelman did not touch; only targeted mutations re-serialize.

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
            _rt_yaml().dump(config, f)
        os.replace(tmp_name, path)
    except BaseException:
        with contextlib.suppress(OSError):
            os.unlink(tmp_name)
        raise


def _set_exposed_flag(state: StateStore, model_id: str, exposed: bool) -> bool:
    """Flip a model's litellm_exposed flag; return True if it changed.

    Returns False when the flag was already at the target value, or when
    unexposing a model with no state entry (e.g. it was deleted earlier in
    this apply) — in that case there's nothing to clear and we must not
    materialize an all-default row in modelman.toml. Callers use the
    return value to decide whether a proxy restart is warranted.
    """
    existing = state.get(model_id)
    if model_id not in state.models and not exposed:
        return False
    if existing.litellm_exposed == exposed:
        return False
    state.set(model_id, replace(existing, litellm_exposed=exposed))
    return True


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
    if not is_cloud_effective(model) and not state.get(model_id).ready:
        raise ExposeError(f"model {model_id!r} is not ready")
    return build_model_list_entry(model, provider)


def expose_model(
    registry: Registry,
    state: StateStore,
    model_id: str,
    litellm_path: Path,
) -> list[str]:
    """Expose a model through LiteLLM: write its model_list entry and flip
    the modelman.toml flag. Errors if the model is unknown, its provider
    has no LiteLLM mapping, or it isn't ready (unless cloud).

    Read-modify-write with settings-ensure: the config is saved only when
    the operation or the ensure actually changed it, and the proxy is
    restarted only when the config or the flag changed — an idempotent
    re-expose of a byte-identical entry writes nothing and does not
    bounce the proxy.

    Returns non-fatal proxy-restart warnings (command unset or failed) so
    the CLI can surface them; the TUI path uses apply_expose_queue instead.
    """
    entry = _validated_entry(registry, state, model_id)
    config = load_litellm_config(litellm_path)
    before = copy.deepcopy(config)
    set_exposed(config, model_id, entry)
    ensure_litellm_settings(config)
    changed = config != before
    if changed:
        save_litellm_config(config, litellm_path)
    flag_changed = _set_exposed_flag(state, model_id, True)
    if changed or flag_changed:
        return restart_litellm_proxy()
    return []


def unexpose_model(
    state: StateStore,
    model_id: str,
    litellm_path: Path,
) -> list[str]:
    """Remove a model's model_list entry and clear its modelman.toml flag.

    A true no-op — row absent, flag already false, settings already
    ensured — saves nothing and does not bounce the proxy. A stale row
    (in config but not exposed per state) is still a real change and
    does restart. A model missing from state (e.g. deleted earlier in
    the same apply cycle) unexposes cleanly without materializing a
    state row for the corpse.

    Returns non-fatal proxy-restart warnings (command unset or failed) so
    the CLI can surface them; the TUI path uses apply_expose_queue instead.
    """
    config = load_litellm_config(litellm_path)
    before = copy.deepcopy(config)
    remove_exposed(config, model_id)
    ensure_litellm_settings(config)
    changed = config != before
    if changed:
        save_litellm_config(config, litellm_path)
    flag_changed = _set_exposed_flag(state, model_id, False)
    if changed or flag_changed:
        return restart_litellm_proxy()
    return []


def apply_expose_queue(
    registry: Registry,
    state: StateStore,
    exposes: list[tuple[str, bool]],
    litellm_path: Path,
) -> tuple[list[tuple[str, bool, str | None]], list[str]]:
    """Apply a queue of (model_id, target_exposed) pairs with a single
    config load and a single atomic save.

    This is the apply()-time path: one YAML parse and one rename for the
    whole queue instead of one per model, and config.yaml transitions
    from before to after in one step rather than risking a half-updated
    file if the process dies mid-queue. The standalone expose_model /
    unexpose_model remain for the CLI's one-model-at-a-time use.

    The owned-settings ensure runs over the whole parsed document before
    the save, and the save (and proxy restart) happen only when the
    document — the queue's rows or the ensured settings — actually
    changed: an idempotent re-expose writes nothing and does not bounce
    the proxy, while a fully-failed queue still persists a settings fix.

    Returns (outcomes, warnings). `outcomes` is one (model_id, target,
    error) per item, error None when applied. Config-level failures
    (missing/unwritable file) propagate to the caller; per-model
    validation failures are reported per item and don't block the rest
    of the queue. Flags flip only after the save succeeds, so state
    never claims an exposure the config file lost. `warnings` carries
    non-fatal proxy-restart notices (command unset or failed) so the
    caller can surface them on the UI thread instead of printing to
    stderr from a worker thread.
    """
    if not exposes:
        return [], []
    config = load_litellm_config(litellm_path)
    before = copy.deepcopy(config)
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
    ensure_litellm_settings(config)
    changed = config != before
    if changed:
        save_litellm_config(config, litellm_path)
    flags_changed = False
    for model_id, target in succeeded:
        if _set_exposed_flag(state, model_id, target):
            flags_changed = True
    if changed or flags_changed:
        return outcomes, restart_litellm_proxy()
    return outcomes, []


# Canonical restart command for the launchd-managed proxy, used when
# MODELMAN_LITELLM_RESTART_CMD is unset. The env var is typically
# exported only from an interactive shell (e.g. ~/.zshrc), so
# non-interactive launches (scripts, agents, worktree launchers) would
# otherwise silently skip the restart and leave the proxy stale — the
# config write is the source of truth, but clients keep 400ing until a
# manual kickstart.
_FALLBACK_LITELLM_RESTART_CMD = "launchctl kickstart -k gui/$(id -u)/local.litellm.proxy"


def default_litellm_restart_cmd() -> str | None:
    """The shell command used to restart the LiteLLM proxy, or None when
    unset (reconcile is a no-op with a warning). Read lazily so env
    overrides work in tests."""
    return os.environ.get("MODELMAN_LITELLM_RESTART_CMD")


def restart_litellm_proxy() -> list[str]:
    """Best-effort reconcile of the running LiteLLM proxy after a config
    write. Runs the configured restart command, falling back to the
    canonical launchctl kickstart when MODELMAN_LITELLM_RESTART_CMD is
    unset. Never raises: the config write is the source of truth, and a
    failed restart just leaves the proxy stale.

    Returns a list of warning strings (empty on success) rather than
    printing to stderr, so callers can surface them on the UI thread —
    a direct stderr write from a TUI worker thread would interleave with
    and garble Textual's rendering. Only warning case left: the command
    itself failed (timeout, non-zero exit, missing binary).
    """
    cmd = default_litellm_restart_cmd() or _FALLBACK_LITELLM_RESTART_CMD
    try:
        subprocess.run(cmd, shell=True, check=True, timeout=30)
    except Exception as exc:  # noqa: BLE001
        return [f"failed to restart LiteLLM proxy ({exc}); restart it manually."]
    return []
