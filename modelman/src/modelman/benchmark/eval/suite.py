"""Suite TOML parsing, row expansion, and (model, route) -> endpoint
resolution for `modelman benchmark eval`.

Deliberately parallel to (not shared with) modelman.benchmark.agent.suite:
eval rows carry no `thinking` (no pi driver here) and instead carry an
optional per-row `categories` override; the direct-route config needs no
`api` protocol discriminator because every eval transport speaks the same
OpenAI-compatible chat-completions wire format.
"""

from __future__ import annotations

import json
import os
import plistlib
import tomllib
from dataclasses import dataclass, field
from pathlib import Path

from modelman.benchmark.errors import BenchmarkError
from modelman.providers import lifecycle
from modelman.registry import Registry

LITELLM_PLIST = Path.home() / "Library" / "LaunchAgents" / "local.litellm.proxy.plist"
LIVE_PI_MODELS_PATH = Path.home() / ".pi" / "agent" / "models.json"
JUDGE_ROUTES = ("litellm", "openrouter")
ROW_ROUTES = ("direct", "litellm", "openrouter")
OPENROUTER_BASE_URL = "https://openrouter.ai/api/v1"


@dataclass
class DirectRouteConfig:
    base_url: str


@dataclass
class JudgeConfig:
    model: str
    temperature: float
    samples: int
    max_attempts: int
    route: str


@dataclass
class CodingConfig:
    dataset: str | None = None
    limit: int | None = None


@dataclass
class RowConfig:
    label: str
    model_id: str
    route: str
    provider_id: str
    direct_model: str | None = None
    categories: list[str] | None = None
    target_local_path: str | None = None
    target_repo: str | None = None
    draft_local_path: str | None = None
    draft_repo: str | None = None
    mtplx_model_name: str | None = None


@dataclass
class Suite:
    name: str
    cooldown_s: float
    judge: JudgeConfig
    coding: CodingConfig
    routes_direct: dict[str, DirectRouteConfig] = field(default_factory=dict)
    rows: list[RowConfig] = field(default_factory=list)


def _short_model(model_id: str) -> str:
    return model_id.split("/")[-1]


def _expand_rows(raw_rows: list[dict], registry: Registry) -> list[RowConfig]:
    rows: list[RowConfig] = []
    seen_labels: set[str] = set()
    for index, raw in enumerate(raw_rows, start=1):
        model_id = raw.get("model")
        route = raw.get("route")
        if not model_id or not route:
            missing = [k for k in ("model", "route") if not raw.get(k)]
            raise BenchmarkError(
                f"suite row {index} is missing required key(s): {', '.join(missing)}"
            )
        if route not in ROW_ROUTES:
            raise BenchmarkError(f"suite row {index} has unknown route: {route!r}")
        # One registry.model() lookup instead of up to three (it's a linear
        # scan) — this also covers the unknown-model check even when
        # `provider =` is set explicitly, since that no longer short-circuits
        # an `or` around the lookup.
        try:
            model_entry = registry.model(model_id)
        except KeyError as exc:
            raise BenchmarkError(f"suite row references unknown model: {model_id}") from exc
        provider_id = raw.get("provider") or model_entry.provider_id
        if raw.get("provider"):
            # An explicit provider must name a real provider id (registry
            # providers — e.g. cloud openrouter — or a lifecycle backend).
            # A typo'd id would silently skip provider isolation for a
            # litellm-routed row (not in ISOLATABLE_PROVIDERS, not in
            # BACKENDS), benchmarking against whatever is currently loaded
            # and violating the mandatory-isolation invariant.
            known = {p.id for p in registry.providers} | set(lifecycle.BACKENDS)
            if raw["provider"] not in known:
                raise BenchmarkError(
                    f"suite row {index} names unknown provider: {raw['provider']!r} "
                    f"(model {model_id!r} belongs to {model_entry.provider_id!r})"
                )
        label = raw.get("label") or f"{index:02d}--{_short_model(model_id)}--{route}"
        if label in seen_labels:
            # metrics.jsonl rows are keyed by label, and the rejudge
            # reconstruction recovers model_id/route from those lines — a
            # duplicate label would misattribute one row's scores to
            # another. Refuse the suite at load time.
            raise BenchmarkError(f"suite rows have duplicate row label: {label!r}")
        seen_labels.add(label)
        rows.append(
            RowConfig(
                label=label,
                model_id=model_id,
                route=route,
                provider_id=provider_id,
                direct_model=raw.get("direct_model"),
                categories=raw.get("categories"),
                target_local_path=model_entry.fetch.local_path if model_entry.fetch else None,
                target_repo=model_entry.fetch.repo if model_entry.fetch else None,
                draft_local_path=model_entry.draft.local_path if model_entry.draft else None,
                draft_repo=model_entry.draft.repo if model_entry.draft else None,
                mtplx_model_name=model_entry.model_name,
            )
        )
    return rows


def load_suite(path: Path, registry: Registry) -> Suite:
    path = Path(path)
    with path.open("rb") as f:
        raw = tomllib.load(f)

    judge_raw = raw["judge"]
    if judge_raw["route"] not in JUDGE_ROUTES:
        raise BenchmarkError(
            f"[judge] route must be one of {list(JUDGE_ROUTES)}, got {judge_raw['route']!r}"
        )
    judge = JudgeConfig(
        model=judge_raw["model"],
        temperature=judge_raw["temperature"],
        samples=judge_raw.get("samples", 1),
        max_attempts=judge_raw.get("max_attempts", 2),
        route=judge_raw["route"],
    )
    coding_raw = raw.get("coding", {})
    coding = CodingConfig(dataset=coding_raw.get("dataset"), limit=coding_raw.get("limit"))
    routes_direct = {
        provider_id: DirectRouteConfig(base_url=cfg["base_url"])
        for provider_id, cfg in raw.get("routes", {}).get("direct", {}).items()
    }
    return Suite(
        name=raw["name"],
        cooldown_s=raw.get("cooldown_s", 15.0),
        judge=judge,
        coding=coding,
        routes_direct=routes_direct,
        rows=_expand_rows(raw.get("rows", []), registry),
    )


def load_live_models(path: Path) -> dict:
    if not path.exists():
        return {}
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError:
        return {}


def openrouter_key(plist_path: Path = LITELLM_PLIST) -> str | None:
    env_key = os.environ.get("OPENROUTER_API_KEY")
    if env_key:
        return env_key
    if not plist_path.exists():
        return None
    try:
        with plist_path.open("rb") as f:
            data = plistlib.load(f)
    except Exception:
        return None
    key = data.get("EnvironmentVariables", {}).get("OPENROUTER_API_KEY")
    return str(key) if key else None


def resolve_row_endpoint(
    row: RowConfig,
    model_name: str,
    routes_direct: dict[str, DirectRouteConfig],
    live_models_path: Path = LIVE_PI_MODELS_PATH,
    plist_path: Path = LITELLM_PLIST,
) -> tuple[str, str, str]:
    """(base_url, model_to_send, api_key) for one eval row.

    litellm keys by the full registry model id (the LiteLLM model_list is
    keyed on it); direct keys by the backend's own bare model_name unless
    overridden by direct_model (needed for omlx, whose registry ids are
    org-prefixed while the server knows only the basename); openrouter
    strips any leading 'openrouter/' prefix, since OpenRouter itself does
    not know that prefix.
    """
    if row.route == "litellm":
        live = load_live_models(live_models_path)
        litellm_entry = live.get("providers", {}).get("litellm", {})
        api_key = litellm_entry.get("apiKey")
        if not api_key:
            raise BenchmarkError(
                "no LiteLLM apiKey found in ~/.pi/agent/models.json; launch a wt "
                "pi session in litellm mode at least once to seed it"
            )
        base_url = litellm_entry.get("baseUrl", "http://localhost:4000/v1")
        return base_url, row.model_id, api_key
    if row.route == "direct":
        direct_cfg = routes_direct.get(row.provider_id)
        if direct_cfg is None:
            raise BenchmarkError(
                f"row {row.label!r} uses route=direct for provider {row.provider_id!r} "
                f"but no [routes.direct.{row.provider_id}] block is configured"
            )
        return direct_cfg.base_url, (row.direct_model or model_name), "ollama"
    if row.route == "openrouter":
        key = openrouter_key(plist_path)
        if not key:
            raise BenchmarkError(
                f"row {row.label!r} uses route=openrouter but OPENROUTER_API_KEY "
                f"was not found (environment or {plist_path})"
            )
        model = row.model_id
        if model.startswith("openrouter/"):
            model = model[len("openrouter/") :]
        return OPENROUTER_BASE_URL, model, key
    raise BenchmarkError(f"row {row.label!r} has unknown route: {row.route!r}")


def preflight(
    suite: Suite,
    registry: Registry,
    rows: list[RowConfig] | None = None,
    *,
    judge_route_active: bool | None = None,
) -> None:
    """Fail fast on what the SELECTED rows would actually hit mid-run.

    `rows` defaults to the full suite; run_suite passes its post-_select_rows
    selection so a scoped run (--row/--category) is never blocked by an
    unselected row's provider being down, a missing direct-route block, or a
    missing openrouter key.

    `judge_route_active`: run_suite computes whether any selected row
    actually runs a judged category (its needs_judge) and passes that here,
    so the judge key is demanded only when the judge will really be called
    (a coding-only EvalPlus run never invokes it). None keeps the
    conservative default: a judge on route=openrouter implies the key."""
    if rows is None:
        rows = suite.rows
    unavailable = []
    for provider_id in dict.fromkeys(row.provider_id for row in rows):
        backend = lifecycle.BACKENDS.get(provider_id)
        if backend is None:
            continue
        reason = backend.check_available()
        if reason is not None:
            unavailable.append(f"{provider_id}: {reason}")
    if unavailable:
        raise BenchmarkError(f"provider(s) unavailable: {'; '.join(unavailable)}")

    for row in rows:
        if row.route == "direct" and row.provider_id not in suite.routes_direct:
            raise BenchmarkError(
                f"row {row.label!r} uses route=direct for provider {row.provider_id!r} "
                f"but no [routes.direct.{row.provider_id}] block is configured"
            )

    if judge_route_active is None:
        judge_needs_key = suite.judge.route == "openrouter"
    else:
        judge_needs_key = judge_route_active and suite.judge.route == "openrouter"
    needs_openrouter = any(row.route == "openrouter" for row in rows) or judge_needs_key
    # Call openrouter_key(LITELLM_PLIST) EXPLICITLY with the module-global
    # name (not the bare openrouter_key()) so a monkeypatch of
    # modelman.benchmark.eval.suite.LITELLM_PLIST takes effect — a
    # default-parameter plist path is bound at def time and patching the
    # module attribute would not steer it.
    if needs_openrouter and openrouter_key(LITELLM_PLIST) is None:
        raise BenchmarkError(
            f"OPENROUTER_API_KEY not found (environment or {LITELLM_PLIST}); "
            "needed for the judge and/or an openrouter row"
        )
