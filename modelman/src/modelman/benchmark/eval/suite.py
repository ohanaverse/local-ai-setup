"""Suite TOML parsing, row expansion, and (model, route) -> endpoint
resolution for `modelman benchmark eval`.

Deliberately parallel to (not shared with) modelman.benchmark.agent.suite:
eval rows carry no `thinking` (no pi driver here) and instead carry an
optional per-row `categories` override; the direct-route config needs no
`api` protocol discriminator because every eval transport speaks the same
OpenAI-compatible chat-completions wire format. The suite PARSING stays
duplicated by design; the credential helpers those routes resolve through
(LITELLM_PLIST, LIVE_PI_MODELS_PATH, OPENROUTER_BASE_URL, openrouter_key,
load_live_models) are shared via modelman.benchmark._routes.
"""

from __future__ import annotations

import tomllib
from dataclasses import dataclass, field
from pathlib import Path

from modelman.benchmark._routes import (  # noqa: F401 — re-exports; suite-only consumers patch them here
    LITELLM_PLIST,
    LIVE_PI_MODELS_PATH,
    OPENROUTER_BASE_URL,
    litellm_credentials,
    load_live_models,
    openrouter_key,
)
from modelman.benchmark.errors import BenchmarkError
from modelman.providers import lifecycle
from modelman.registry import Registry

JUDGE_ROUTES = ("litellm", "openrouter")
ROW_ROUTES = ("direct", "litellm", "openrouter")


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
    # 1-based position of the row within the suite's [[rows]] list. Set at
    # load time (and re-stamped by runner._select_rows over whatever list it
    # was handed, so it also holds for hand-built suites in tests); runner
    # numbers each run's row directories by it so `judge --row N` resolves
    # to the same row `run --row N` selected even though execution order
    # (sorted by provider/model for isolation grouping) differs from suite
    # order on any multi-provider suite.
    suite_index: int = 1


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
        if not isinstance(raw, dict):
            raise BenchmarkError(f"suite row {index} must be a table")
        row_categories = raw.get("categories")
        if row_categories is not None and (
            not isinstance(row_categories, list)
            or not all(isinstance(c, str) for c in row_categories)
        ):
            # A bare string would otherwise be iterated character by
            # character and reported as unknown categories "r, e, a, ...".
            raise BenchmarkError(
                f"suite row {index} categories must be a list of strings, got {row_categories!r}"
            )
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
                suite_index=index,
            )
        )
    return rows


def load_suite(path: Path, registry: Registry) -> Suite:
    path = Path(path)
    try:
        with path.open("rb") as f:
            raw = tomllib.load(f)
    except OSError as exc:
        raise BenchmarkError(f"cannot read suite {path}: {exc}") from exc
    except tomllib.TOMLDecodeError as exc:
        raise BenchmarkError(f"malformed TOML in suite {path}: {exc}") from exc

    # Missing top-level keys raise BenchmarkError, not KeyError: every other
    # suite malformation (unknown model, duplicate label, unknown provider)
    # surfaces as a clean one-line error the CLI catches, and a hand-written
    # first suite missing [judge] or name must not be the one case that
    # traceback instead.
    judge_raw = raw.get("judge")
    if not isinstance(judge_raw, dict):
        raise BenchmarkError(f"suite {path.name} is missing the required [judge] table")
    name = raw.get("name")
    if not name:
        raise BenchmarkError(f"suite {path.name} is missing the required name field")
    judge_route = judge_raw.get("route")
    if not judge_route:
        raise BenchmarkError(f"suite {path.name} [judge] is missing the required route key")
    if judge_route not in JUDGE_ROUTES:
        raise BenchmarkError(
            f"[judge] route must be one of {list(JUDGE_ROUTES)}, got {judge_route!r}"
        )
    judge_model = judge_raw.get("model")
    if not judge_model:
        raise BenchmarkError(f"suite {path.name} [judge] is missing the required model key")
    judge = JudgeConfig(
        model=judge_model,
        temperature=judge_raw.get("temperature", 0.0),
        samples=judge_raw.get("samples", 1),
        max_attempts=judge_raw.get("max_attempts", 2),
        route=judge_route,
    )
    # Type-check before the >= 1 comparison: `samples = "3"` would otherwise
    # raise a bare TypeError from `"3" < 1`.
    for key, value in (("samples", judge.samples), ("max_attempts", judge.max_attempts)):
        if isinstance(value, bool) or not isinstance(value, int):
            raise BenchmarkError(f"suite {path.name} [judge] {key} must be an integer, got {value!r}")
    if not isinstance(judge.temperature, int | float) or isinstance(judge.temperature, bool):
        raise BenchmarkError(
            f"suite {path.name} [judge] temperature must be a number, got {judge.temperature!r}"
        )
    if judge.samples < 1 or judge.max_attempts < 1:
        raise BenchmarkError(
            f"suite {path.name} [judge] samples and max_attempts must be >= 1, got "
            f"{judge.samples}/{judge.max_attempts}"
        )
    coding_raw = raw.get("coding", {})
    coding = CodingConfig(dataset=coding_raw.get("dataset"), limit=coding_raw.get("limit"))
    routes_direct: dict[str, DirectRouteConfig] = {}
    for provider_id, cfg in raw.get("routes", {}).get("direct", {}).items():
        if not isinstance(cfg, dict) or not cfg.get("base_url"):
            raise BenchmarkError(
                f"suite {path.name} [routes.direct.{provider_id}] is missing the required "
                "base_url key"
            )
        routes_direct[provider_id] = DirectRouteConfig(base_url=cfg["base_url"])
    return Suite(
        name=name,
        cooldown_s=raw.get("cooldown_s", 15.0),
        judge=judge,
        coding=coding,
        routes_direct=routes_direct,
        rows=_expand_rows(raw.get("rows", []), registry),
    )


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
        base_url, api_key = litellm_credentials(live_models_path)
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
