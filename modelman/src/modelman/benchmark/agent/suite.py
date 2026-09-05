"""Suite TOML parsing and cartesian row expansion."""

from __future__ import annotations

import itertools
import tomllib
from dataclasses import dataclass, field
from pathlib import Path

from modelman.benchmark.agent.pidriver import DirectRouteConfig, RowConfig
from modelman.benchmark.errors import BenchmarkError
from modelman.registry import Registry


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


def load_suite(path: Path, registry: Registry) -> Suite:
    path = Path(path)
    with path.open("rb") as f:
        raw = tomllib.load(f)

    judge_raw = raw["judge"]
    if judge_raw["route"] != "litellm":
        raise BenchmarkError(
            f"[judge] route must be 'litellm' in v1, got {judge_raw['route']!r} — "
            "the judge is a robust cloud model reached through the existing proxy"
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

    return Suite(
        name=raw["name"],
        task_path=Path(raw["task"]),
        passes=raw.get("passes", 1),
        cooldown_s=raw.get("cooldown_s", 20),
        agent_timeout_s=raw.get("agent_timeout_s", 420),
        judge=judge,
        routes_direct=routes_direct,
        rows=_expand_rows(raw.get("rows", []), registry),
        repair_rounds=repair_rounds,
    )
