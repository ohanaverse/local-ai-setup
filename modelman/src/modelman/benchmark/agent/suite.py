"""Suite TOML parsing and cartesian row expansion."""

from __future__ import annotations

import itertools
import os
import plistlib
import shutil
import tomllib
from dataclasses import dataclass, field
from pathlib import Path

from modelman.benchmark.agent.pidriver import DirectRouteConfig, RowConfig
from modelman.benchmark.agent.task import TaskBundle
from modelman.benchmark.errors import BenchmarkError
from modelman.registry import Registry

LITELLM_PLIST = Path.home() / "Library" / "LaunchAgents" / "local.litellm.proxy.plist"

# What [judge].route accepts, and where the direct route goes. The gateway is
# the default because it is already running and already holds a key pi uses;
# "openrouter" exists because a local gateway carries no frontier model, and
# the judge is the one role in this harness that requires one.
JUDGE_ROUTES = ("litellm", "openrouter")
OPENROUTER_BASE_URL = "https://openrouter.ai/api/v1"


@dataclass
class JudgeConfig:
    model: str
    thinking: str
    temperature: float
    samples: int
    max_attempts: int
    route: str


@dataclass
class Suite:
    name: str
    task_path: Path
    passes: int
    cooldown_s: float
    agent_timeout_s: float
    judge: JudgeConfig
    routes_direct: dict[str, DirectRouteConfig] = field(default_factory=dict)
    rows: list[RowConfig] = field(default_factory=list)
    repair_rounds: int = 0


def _short_model(model_id: str) -> str:
    return model_id.split("/")[-1]


def _provider_for(model_id: str, registry: Registry) -> str:
    try:
        return registry.model(model_id).provider_id
    except KeyError as exc:
        raise BenchmarkError(f"suite row references unknown model: {model_id}") from exc


def _expand_rows(raw_rows: list[dict], registry: Registry) -> list[RowConfig]:
    rows: list[RowConfig] = []
    index = 0
    for raw in raw_rows:
        if "model" in raw:
            index += 1
            model_id = raw["model"]
            _provider_for(model_id, registry)  # validates the model exists
            provider_id = raw.get("provider") or _provider_for(model_id, registry)
            label = raw.get("label") or f"{index:02d}--{_short_model(model_id)}--{raw['thinking']}--{raw['route']}"
            rows.append(
                RowConfig(
                    label=label,
                    model_id=model_id,
                    thinking=raw["thinking"],
                    route=raw["route"],
                    provider_id=provider_id,
                    direct_model=raw.get("direct_model"),
                )
            )
            continue

        models = raw.get("models", [])
        thinkings = raw.get("thinking", [])
        routes = raw.get("routes", [])
        for model_id, thinking, route in itertools.product(models, thinkings, routes):
            index += 1
            provider_id = raw.get("provider") or _provider_for(model_id, registry)
            label = f"{index:02d}--{_short_model(model_id)}--{thinking}--{route}"
            rows.append(
                RowConfig(
                    label=label,
                    model_id=model_id,
                    thinking=thinking,
                    route=route,
                    provider_id=provider_id,
                    direct_model=raw.get("direct_model"),
                )
            )
    return rows


def _resolve_task_path(raw: str, suite_path: Path) -> Path:
    """Resolve a suite's `task` path, which suites write repo-relative
    (`benchmarks/tasks/day31-drift`).

    The CLI is run from `modelman/` — as the guide, CI and `uv run` all require
    — so a path relative to the caller's cwd does not exist there, and every
    real suite would die at "task bundle not found". Falling back to the suite
    file's own ancestors keeps a suite portable: it lives in the same repository
    as the bundles it names. An absolute path, or one that resolves from the cwd,
    still wins, and an unresolvable path is returned untouched so `load_task`
    raises its own canonical error."""
    candidate = Path(raw)
    if candidate.is_absolute() or candidate.exists():
        return candidate
    for parent in suite_path.resolve().parents:
        ancestor_candidate = parent / candidate
        if ancestor_candidate.exists():
            return ancestor_candidate
    return candidate


def load_suite(path: Path, registry: Registry) -> Suite:
    path = Path(path)
    with path.open("rb") as f:
        raw = tomllib.load(f)

    judge_raw = raw["judge"]
    if judge_raw["route"] not in JUDGE_ROUTES:
        raise BenchmarkError(
            f"[judge] route must be one of {list(JUDGE_ROUTES)}, got {judge_raw['route']!r} — "
            "'litellm' reaches the judge through the local proxy, 'openrouter' calls "
            "OpenRouter directly, which is what a gateway with no frontier model needs"
        )
    judge = JudgeConfig(
        model=judge_raw["model"],
        thinking=judge_raw["thinking"],
        temperature=judge_raw["temperature"],
        samples=judge_raw.get("samples", 1),
        max_attempts=judge_raw.get("max_attempts", 2),
        route=judge_raw["route"],
    )

    routes_direct = {
        provider_id: DirectRouteConfig(base_url=cfg["base_url"], api=cfg["api"])
        for provider_id, cfg in raw.get("routes", {}).get("direct", {}).items()
    }

    repair_rounds = raw.get("repair_rounds", 0)
    if repair_rounds != 0:
        raise BenchmarkError("repair_rounds is not yet supported (v1 ships repair_rounds = 0 only)")

    if raw.get("judge", {}).get("route", "litellm") not in JUDGE_ROUTES:
        raise BenchmarkError(
            f"[judge].route = {raw['judge'].get('route')!r} is not one of {JUDGE_ROUTES}. "
            "The field used to be accepted and ignored, which is worse than rejecting it: "
            "a suite asking for a judge the gateway cannot reach scores JUDGE_FAIL on every row."
        )

    return Suite(
        name=raw["name"],
        task_path=_resolve_task_path(str(raw["task"]), path),
        passes=raw.get("passes", 1),
        cooldown_s=raw.get("cooldown_s", 20),
        agent_timeout_s=raw.get("agent_timeout_s", 420),
        judge=judge,
        routes_direct=routes_direct,
        rows=_expand_rows(raw.get("rows", []), registry),
        repair_rounds=repair_rounds,
    )

def openrouter_key(plist_path: Path = LITELLM_PLIST) -> str | None:
    """The OpenRouter key, from the environment or the LiteLLM LaunchAgent.

    Same two places preflight already looks; a judge on route=openrouter needs
    the value, not just the knowledge that one exists."""
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


def _openrouter_key_available(plist_path: Path) -> bool:
    return openrouter_key(plist_path) is not None


def preflight(suite: Suite, registry: Registry, task: TaskBundle, *, plist_path: Path = LITELLM_PLIST) -> None:
    """Fail fast on everything that would otherwise die mid-run, after
    already paying for the agent rows."""
    missing_helpers = [
        name for name in ("llm-isolate-provider", "llm-restore-providers") if shutil.which(name) is None
    ]
    if missing_helpers:
        raise BenchmarkError(
            f"isolation helper(s) not found on PATH: {', '.join(missing_helpers)}. "
            "Ensure local-ai-setup/bin is on PATH."
        )

    for row in suite.rows:
        if row.route == "direct" and row.provider_id not in suite.routes_direct:
            raise BenchmarkError(
                f"row {row.label!r} uses route=direct for provider {row.provider_id!r} "
                f"but no [routes.direct.{row.provider_id}] block is configured"
            )

    if task.hidden_dir.is_dir():
        hidden_names = {p.name for p in task.hidden_dir.rglob("*") if p.is_file()}
        visible_names = {p.name for p in task.visible_dir.rglob("*") if p.is_file()}
        leaked = hidden_names & visible_names
        if leaked:
            raise BenchmarkError(f"hidden/ file name(s) also present under visible/: {', '.join(sorted(leaked))}")

    needs_openrouter = (
        suite.judge.route == "openrouter" or suite.judge.model.split("/")[0] == "openrouter"
    )
    if needs_openrouter and not _openrouter_key_available(plist_path):
        raise BenchmarkError(
            f"OPENROUTER_API_KEY not found for judge model {suite.judge.model!r} "
            f"(route={suite.judge.route!r}); check the environment or "
            "~/Library/LaunchAgents/local.litellm.proxy.plist"
        )
