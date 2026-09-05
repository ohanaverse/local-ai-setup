# Agent coding benchmark — Phase 2: pi driver + metrics (plan extract)

> Part of [the agent-coding-benchmark plan index](2026-09-04-agent-coding-benchmark.md). Shared Goal / Architecture / Tech Stack / Global Constraints and the phase order live there.


## Phase 2: pi driver + metrics

**Ordering note:** the spec's module table lists `pidriver.py` as depending on `suite.py`. This plan builds `pidriver.py` first (Phase 2) and `suite.py` fourth (Phase 4) so Phase 2 is testable standalone, per the spec's own phasing ("Phase 1–2 are the only phases with genuine unknown risk"). `RowConfig` and `DirectRouteConfig` are therefore defined in `pidriver.py`, and `suite.py` imports them from there in Phase 4 — the reverse of the spec's table, but the same two types either way.

### Task 5: Route resolution + `models.json` generation

**Files:**
- Create: `modelman/src/modelman/benchmark/agent/pidriver.py`
- Test: `modelman/tests/benchmark/agent/test_pidriver.py`

**Interfaces:**
- Produces: `RowConfig` (`label, model_id, thinking, route, provider_id`), `DirectRouteConfig` (`base_url, api`), `PiTarget` (`pi_provider, launch_id, base_url, api, api_key, context_window, reasoning`, property `.model_arg`), `resolve_pi_target(row, model_name, routes_direct, live_models_path=LIVE_PI_MODELS_PATH) -> PiTarget`, `build_models_json(target) -> dict`, `write_pi_config(target, config_dir) -> Path`, `build_pi_command(target, thinking, session_dir, prompt) -> list[str]`. `suite.py` (Task 14) imports `RowConfig`/`DirectRouteConfig`; `runner.py` (Task 16) imports everything else from this module.

- [ ] **Step 1: Write the failing test**

`modelman/tests/benchmark/agent/test_pidriver.py`:
```python
"""Tests for modelman.benchmark.agent.pidriver — route resolution and pi
config generation.

Route resolution is the part of the design most likely to silently
misaddress a model (spec: "a registry id sent under the wrong provider
could never be addressed and its bare form would be sent upstream, a 400
at the gateway") — these tests pin the litellm-vs-direct addressing rules
exactly as the spec's Route resolution section states them.
"""

import json
from pathlib import Path

import pytest

from modelman.benchmark.agent.pidriver import (
    DirectRouteConfig,
    RowConfig,
    build_models_json,
    build_pi_command,
    resolve_pi_target,
    write_pi_config,
)
from modelman.benchmark.errors import BenchmarkError


def test_litellm_route_keys_by_full_registry_id(tmp_path):
    """route=litellm addresses pi's litellm provider by the full registry
    model id — the LiteLLM model_list is keyed on it, not the bare name."""
    live = tmp_path / "models.json"
    live.write_text(
        json.dumps(
            {
                "providers": {
                    "litellm": {
                        "baseUrl": "http://localhost:4000/v1",
                        "apiKey": "sk-live-key",
                        "models": [{"id": "ollama/qwen3.8:27b-mlx", "contextWindow": 131072, "reasoning": False}],
                    }
                }
            }
        ),
        encoding="utf-8",
    )
    row = RowConfig(label="r1", model_id="ollama/qwen3.8:27b-mlx", thinking="off", route="litellm", provider_id="ollama")

    target = resolve_pi_target(row, "qwen3.8:27b-mlx", routes_direct={}, live_models_path=live)

    assert target.pi_provider == "litellm"
    assert target.launch_id == "ollama/qwen3.8:27b-mlx"
    assert target.model_arg == "litellm/ollama/qwen3.8:27b-mlx"
    assert target.api_key == "sk-live-key"
    assert target.context_window == 131072
    assert target.reasoning is False


def test_direct_route_keys_by_bare_model_name(tmp_path):
    """route=direct addresses the backend's own endpoint by its bare
    model_name, per [routes.direct.<provider>] in the suite."""
    row = RowConfig(label="r1", model_id="omlx/Ornith-1.5-35B-A3B-MLX-6bit", thinking="high", route="direct", provider_id="omlx")
    routes_direct = {"omlx": DirectRouteConfig(base_url="http://localhost:8000/v1", api="openai-completions")}

    target = resolve_pi_target(row, "Ornith-1.5-35B-A3B-MLX-6bit", routes_direct, live_models_path=tmp_path / "missing.json")

    assert target.pi_provider == "omlx"
    assert target.launch_id == "Ornith-1.5-35B-A3B-MLX-6bit"
    assert target.model_arg == "omlx/Ornith-1.5-35B-A3B-MLX-6bit"
    assert target.base_url == "http://localhost:8000/v1"
    assert target.api_key == "ollama"  # placeholder — pi rejects an empty apiKey


def test_direct_route_missing_config_block_raises(tmp_path):
    """A direct row with no matching [routes.direct.<provider>] block fails
    at resolution with the provider name, not a KeyError deep in dict access."""
    row = RowConfig(label="r1", model_id="omlx/x", thinking="off", route="direct", provider_id="omlx")
    with pytest.raises(BenchmarkError, match="omlx"):
        resolve_pi_target(row, "x", routes_direct={}, live_models_path=tmp_path / "missing.json")


def test_litellm_route_missing_key_raises(tmp_path):
    """No working LiteLLM apiKey anywhere means the row can't be judged or
    run through the gateway — fail clearly instead of sending an empty key."""
    row = RowConfig(label="r1", model_id="ollama/x", thinking="off", route="litellm", provider_id="ollama")
    with pytest.raises(BenchmarkError, match="apiKey"):
        resolve_pi_target(row, "x", routes_direct={}, live_models_path=tmp_path / "missing.json")


def test_build_models_json_matches_wt_pi_models_shape(tmp_path):
    """Provider/entry shape mirrors wt/internal/agents/pi_models.go exactly
    (id/contextWindow/input/reasoning/_launch) so an agent row behaves like
    a wt-launched pi session."""
    row = RowConfig(label="r1", model_id="ollama/x", thinking="off", route="litellm", provider_id="ollama")
    target = resolve_pi_target(row, "x", routes_direct={}, live_models_path=tmp_path / "missing.json")
    doc = build_models_json(target)
    entry = doc["providers"]["litellm"]["models"][0]
    assert entry["_launch"] is True
    assert entry["id"] == "ollama/x"
    assert entry["input"] == ["text", "image"]


def test_write_pi_config_writes_models_json(tmp_path):
    row = RowConfig(label="r1", model_id="ollama/x", thinking="off", route="litellm", provider_id="ollama")
    target = resolve_pi_target(row, "x", routes_direct={}, live_models_path=tmp_path / "missing.json")
    config_dir = tmp_path / "run-config"
    path = write_pi_config(target, config_dir)
    assert path == config_dir / "models.json"
    assert json.loads(path.read_text(encoding="utf-8"))["providers"]["litellm"]["models"][0]["id"] == "ollama/x"


def test_build_pi_command_shape(tmp_path):
    row = RowConfig(label="r1", model_id="ollama/x", thinking="high", route="litellm", provider_id="ollama")
    target = resolve_pi_target(row, "x", routes_direct={}, live_models_path=tmp_path / "missing.json")
    cmd = build_pi_command(target, "high", tmp_path / "session", "do the task")
    assert cmd[0] == "pi"
    assert "--mode" in cmd and "json" in cmd
    assert cmd[cmd.index("--model") + 1] == "litellm/ollama/x"
    assert cmd[cmd.index("--thinking") + 1] == "high"
    assert cmd[-1] == "do the task"
    assert "--no-approve" in cmd
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_pidriver.py -v`
Expected: FAIL with `ModuleNotFoundError`

- [ ] **Step 3: Write the implementation**

`modelman/src/modelman/benchmark/agent/pidriver.py`:
```python
"""pi agent driver: route resolution, models.json generation, process
execution, and metrics extraction from pi's `--mode json` event stream."""

from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path

from modelman.benchmark.errors import BenchmarkError

DEFAULT_CONTEXT_WINDOW = 262144
LIVE_PI_MODELS_PATH = Path.home() / ".pi" / "agent" / "models.json"

PI_BASE_ARGS = [
    "--mode", "json",
    "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-context-files",
    "--no-approve",
]


@dataclass
class DirectRouteConfig:
    base_url: str
    api: str


@dataclass
class RowConfig:
    label: str
    model_id: str
    thinking: str
    route: str  # "direct" | "litellm"
    provider_id: str


@dataclass
class PiTarget:
    pi_provider: str
    launch_id: str
    base_url: str
    api: str
    api_key: str
    context_window: int
    reasoning: bool

    @property
    def model_arg(self) -> str:
        return f"{self.pi_provider}/{self.launch_id}"


def _load_live_models(path: Path) -> dict:
    if not path.exists():
        return {}
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError:
        return {}


def _lookup_live_entry(live: dict, pi_provider: str, launch_id: str) -> dict | None:
    provider = live.get("providers", {}).get(pi_provider, {})
    for entry in provider.get("models", []):
        if entry.get("id") == launch_id:
            return entry
    return None


def resolve_pi_target(
    row: RowConfig,
    model_name: str,
    routes_direct: dict[str, DirectRouteConfig],
    live_models_path: Path = LIVE_PI_MODELS_PATH,
) -> PiTarget:
    """Map (model, route) to a pi provider/launch id.

    litellm route keys by the full registry model id (the LiteLLM
    model_list is keyed on it); direct route keys by the backend's own bare
    model_name — mirrors wt's addressing convention exactly, since pi
    splits --model on the first slash and a registry id under the wrong
    provider "could never be addressed" (spec, Verified against the live
    setup).
    """
    live = _load_live_models(live_models_path)

    if row.route == "litellm":
        pi_provider = "litellm"
        launch_id = row.model_id
        litellm_entry = live.get("providers", {}).get("litellm", {})
        base_url = litellm_entry.get("baseUrl", "http://localhost:4000/v1")
        api = "openai-completions"
        api_key = litellm_entry.get("apiKey")
        if not api_key:
            raise BenchmarkError(
                "no LiteLLM apiKey found in ~/.pi/agent/models.json; launch a wt "
                "pi session in litellm mode at least once to seed it"
            )
    elif row.route == "direct":
        direct_cfg = routes_direct.get(row.provider_id)
        if direct_cfg is None:
            raise BenchmarkError(
                f"row {row.label!r} uses route=direct for provider {row.provider_id!r} "
                f"but no [routes.direct.{row.provider_id}] block is configured"
            )
        pi_provider = row.provider_id
        launch_id = model_name
        base_url = direct_cfg.base_url
        api = direct_cfg.api
        api_key = "ollama"  # pi rejects an empty apiKey even for keyless local backends
    else:
        raise BenchmarkError(f"row {row.label!r} has unknown route: {row.route!r}")

    live_entry = _lookup_live_entry(live, pi_provider, launch_id) or {}
    context_window = live_entry.get("contextWindow", DEFAULT_CONTEXT_WINDOW)
    reasoning = live_entry.get("reasoning", True)

    return PiTarget(
        pi_provider=pi_provider,
        launch_id=launch_id,
        base_url=base_url,
        api=api,
        api_key=api_key,
        context_window=context_window,
        reasoning=reasoning,
    )


def build_models_json(target: PiTarget) -> dict:
    """Provider ids and entry shape mirror wt/internal/agents/pi_models.go
    exactly (id/contextWindow/input/reasoning/_launch) so an agent row
    behaves like a wt-launched session."""
    return {
        "providers": {
            target.pi_provider: {
                "api": target.api,
                "apiKey": target.api_key,
                "baseUrl": target.base_url,
                "models": [
                    {
                        "_launch": True,
                        "contextWindow": target.context_window,
                        "id": target.launch_id,
                        "input": ["text", "image"],
                        "reasoning": target.reasoning,
                    }
                ],
            }
        }
    }


def write_pi_config(target: PiTarget, config_dir: Path) -> Path:
    """Write models.json into a per-run PI_CODING_AGENT_DIR. The caller
    removes config_dir in a finally block — it contains the LiteLLM key."""
    config_dir.mkdir(parents=True, exist_ok=True)
    path = config_dir / "models.json"
    path.write_text(json.dumps(build_models_json(target), indent=2), encoding="utf-8")
    return path


def build_pi_command(target: PiTarget, thinking: str, session_dir: Path, prompt: str) -> list[str]:
    return [
        "pi",
        *PI_BASE_ARGS,
        "--session-dir", str(session_dir),
        "--model", target.model_arg,
        "--thinking", thinking,
        "-p", prompt,
    ]
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_pidriver.py -v`
Expected: PASS (7 tests)

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/pidriver.py \
        modelman/tests/benchmark/agent/test_pidriver.py
git commit -m "feat(agent-bench): add pi route resolution + config gen - completes plan item #5"
```

### Task 6: Fake agent fixture + process streaming with hard timeout

**Files:**
- Create: `modelman/tests/benchmark/agent/fixtures/fake_agent.py`
- Modify: `modelman/src/modelman/benchmark/agent/pidriver.py`
- Modify: `modelman/tests/benchmark/agent/test_pidriver.py`

**Interfaces:**
- Produces: `PiRunResult` (`completed: bool, timed_out: bool, wall_ms: int, events: list[dict], unparsed_lines: int, message_end_seen: bool`); `run_pi_process(cmd: list[str], cwd: Path, env: dict[str, str], timeout_s: float) -> PiRunResult`. `gates.py` (Task 10) and `runner.py` (Task 16) consume `PiRunResult` exactly as named here; Task 7's `compute_metrics` consumes it too.

The fake agent pins the exact JSONL event shape from the spec's "Verified against the live setup" section (`session, agent_start, turn_start, message_start, message_update` with `thinking_delta`/`text_delta`, `message_end` with `usage.input/output/reasoning`, `tool_execution_start`/`tool_execution_end`, `turn_end, agent_end, agent_settled`) so every later test builds on one fixed, known-good fixture.

- [ ] **Step 1: Write the fake agent fixture**

`modelman/tests/benchmark/agent/fixtures/fake_agent.py`:
```python
#!/usr/bin/env python3
"""Deterministic stand-in for `pi --mode json`, used by pidriver tests.

Emits exactly the event shape pidriver.py parses (see the spec's "Verified
against the live setup" section): session, agent_start, turn_start,
message_start, message_update (thinking_delta/text_delta), message_end
with usage.input/output/reasoning, tool_execution_start/end, turn_end,
agent_end, agent_settled. --hang/--delay/--malformed-line exercise the
timeout and unparsed-line paths without a real multi-minute agent run.
"""

import argparse
import json
import sys
import time


def _emit(event: dict) -> None:
    print(json.dumps(event), flush=True)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--hang", action="store_true")
    parser.add_argument("--delay", type=float, default=0.0)
    parser.add_argument("--malformed-line", action="store_true")
    args, _ = parser.parse_known_args()

    _emit({"type": "session", "id": "fake-session"})
    _emit({"type": "agent_start"})
    _emit({"type": "turn_start"})
    _emit({"type": "message_start", "role": "assistant"})
    time.sleep(args.delay)
    _emit({"type": "message_update", "delta": {"type": "thinking_delta", "text": "..."}})
    _emit({"type": "message_update", "delta": {"type": "text_delta", "text": "Looking into it"}})
    if args.malformed_line:
        print("{not json", flush=True)
    _emit({"type": "tool_execution_start", "name": "read_file"})
    time.sleep(0.05)
    _emit({"type": "tool_execution_end", "name": "read_file"})
    _emit(
        {
            "type": "message_end",
            "usage": {"input": 382, "output": 38, "reasoning": 33, "cache_read": 0, "cost": {"total": 0.0}},
        }
    )
    _emit({"type": "turn_end"})
    _emit({"type": "agent_end"})
    _emit({"type": "agent_settled"})

    if args.hang:
        time.sleep(3600)


if __name__ == "__main__":
    main()
```

- [ ] **Step 2: Write the failing test**

Append to `modelman/tests/benchmark/agent/test_pidriver.py`:
```python
import os
import sys

from modelman.benchmark.agent.pidriver import run_pi_process

FAKE_AGENT = Path(__file__).parent / "fixtures" / "fake_agent.py"


def test_run_pi_process_completes_normally():
    """A normal run parses every event and reports completed/not-timed-out."""
    result = run_pi_process([sys.executable, str(FAKE_AGENT)], cwd=Path.cwd(), env=os.environ.copy(), timeout_s=10)
    assert result.completed is True
    assert result.timed_out is False
    assert result.message_end_seen is True
    assert result.unparsed_lines == 0
    assert len(result.events) >= 8


def test_run_pi_process_counts_unparsed_lines_without_failing():
    """A malformed JSONL line is skipped and counted, never fatal (spec:
    'A line that fails JSON parsing is counted in unparsed_lines and
    skipped')."""
    result = run_pi_process(
        [sys.executable, str(FAKE_AGENT), "--malformed-line"], cwd=Path.cwd(), env=os.environ.copy(), timeout_s=10
    )
    assert result.completed is True
    assert result.unparsed_lines == 1


def test_run_pi_process_kills_process_group_on_timeout():
    """A hung agent is killed (process group, not just the parent) well
    before its own 3600s sleep — proves killpg actually fires rather than
    the harness just waiting the child out."""
    result = run_pi_process(
        [sys.executable, str(FAKE_AGENT), "--hang"], cwd=Path.cwd(), env=os.environ.copy(), timeout_s=0.5
    )
    assert result.timed_out is True
    assert result.completed is False
    assert result.wall_ms < 3000
```

- [ ] **Step 3: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_pidriver.py -v`
Expected: FAIL — `run_pi_process` doesn't exist yet

- [ ] **Step 4: Write the implementation**

Append to `modelman/src/modelman/benchmark/agent/pidriver.py` (add these imports to the existing `import` block at the top: `os`, `queue`, `signal`, `subprocess`, `threading`, `time`):
```python
@dataclass
class PiRunResult:
    completed: bool
    timed_out: bool
    wall_ms: int
    events: list[dict]
    unparsed_lines: int
    message_end_seen: bool


def _reader_thread(pipe, q: "queue.Queue[str | None]") -> None:
    for line in iter(pipe.readline, ""):
        q.put(line)
    q.put(None)


def run_pi_process(cmd: list[str], cwd: Path, env: dict[str, str], timeout_s: float) -> PiRunResult:
    """Spawn the agent process, timestamp + parse each JSONL line, and
    enforce a hard timeout by killing the whole process group. pi spawns
    child shell processes for tool execution; killing only the parent
    would orphan a child into the next row's timing and GPU usage."""
    start = time.monotonic()
    proc = subprocess.Popen(
        cmd, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
        text=True, start_new_session=True,
    )
    q: queue.Queue[str | None] = queue.Queue()
    reader = threading.Thread(target=_reader_thread, args=(proc.stdout, q), daemon=True)
    reader.start()

    events: list[dict] = []
    unparsed_lines = 0
    message_end_seen = False
    deadline = start + timeout_s
    timed_out = False

    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            timed_out = True
            break
        try:
            line = q.get(timeout=remaining)
        except queue.Empty:
            timed_out = True
            break
        if line is None:
            break
        raw = line.rstrip("\n")
        if not raw:
            continue
        try:
            event = json.loads(raw)
        except json.JSONDecodeError:
            unparsed_lines += 1
            continue
        if event.get("type") == "message_end":
            message_end_seen = True
        events.append({"ts": time.time(), "event": event})

    if timed_out:
        try:
            os.killpg(os.getpgid(proc.pid), signal.SIGKILL)
        except ProcessLookupError:
            pass
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            pass
        completed = False
    else:
        proc.wait()
        completed = proc.returncode == 0

    wall_ms = int((time.monotonic() - start) * 1000)
    return PiRunResult(
        completed=completed,
        timed_out=timed_out,
        wall_ms=wall_ms,
        events=events,
        unparsed_lines=unparsed_lines,
        message_end_seen=message_end_seen,
    )
```

- [ ] **Step 5: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_pidriver.py -v`
Expected: PASS (10 tests)

- [ ] **Step 6: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/pidriver.py \
        modelman/tests/benchmark/agent/test_pidriver.py \
        modelman/tests/benchmark/agent/fixtures/fake_agent.py
git commit -m "feat(agent-bench): stream pi process with hard timeout kill - completes plan item #6"
```

### Task 7: Metrics — TTFT, gen/e2e throughput, tool time, anomaly flags

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/pidriver.py`
- Modify: `modelman/tests/benchmark/agent/test_pidriver.py`

**Interfaces:**
- Consumes: `PiRunResult` (Task 6).
- Produces: `RowMetrics` dataclass (`wall_ms, requests, turns, ttft_first_ms, ttft_mean_ms, ttft_max_ms, gen_tok_s, e2e_tok_s, in_tok, out_tok, cache_read_tok, reasoning_tok, tool_ms, cost_usd, unparsed_lines, thinking_noop, cold_first_token`); `compute_metrics(run_result: PiRunResult, *, thinking_level: str) -> RowMetrics`. `gates.py` doesn't touch this; `report.py` (Task 22) and `runner.py` (Task 16) both consume `RowMetrics` by these exact field names — the summary table's Speed columns (Task 23) read them directly.

This closes out Phase 2 — the spec calls this "where `gen_tok_s`/`e2e_tok_s` semantics get frozen," so the test below pins the arithmetic with a synthetic, hand-computed event sequence rather than trusting the fake agent's real timing (which is not deterministic enough to assert exact numbers on).

- [ ] **Step 1: Write the failing test**

Append to `modelman/tests/benchmark/agent/test_pidriver.py`:
```python
from modelman.benchmark.agent.pidriver import PiRunResult, compute_metrics


def _synthetic_run(*, reasoning_tok: int = 33) -> PiRunResult:
    """A hand-timed two-request run: request 1 takes 1.0s wall (0.2s to
    first token), request 2 takes 0.5s wall (0.1s to first token); one
    0.3s tool call sits between them. Total wall clock is fixed at 2000ms
    so e2e_tok_s has a known denominator."""
    events = [
        {"ts": 0.0, "event": {"type": "message_start"}},
        {"ts": 0.2, "event": {"type": "message_update", "delta": {"type": "text_delta", "text": "a"}}},
        {"ts": 1.0, "event": {"type": "message_end", "usage": {"input": 100, "output": 20, "reasoning": reasoning_tok}}},
        {"ts": 1.0, "event": {"type": "tool_execution_start"}},
        {"ts": 1.3, "event": {"type": "tool_execution_end"}},
        {"ts": 1.3, "event": {"type": "message_start"}},
        {"ts": 1.4, "event": {"type": "message_update", "delta": {"type": "text_delta", "text": "b"}}},
        {"ts": 1.8, "event": {"type": "message_end", "usage": {"input": 50, "output": 10, "reasoning": 0}}},
        {"ts": 1.8, "event": {"type": "turn_start"}},
    ]
    return PiRunResult(
        completed=True, timed_out=False, wall_ms=2000, events=events, unparsed_lines=0, message_end_seen=True
    )


def test_compute_metrics_ttft_and_throughput():
    """gen_tok_s excludes tool time (busy throughput); e2e_tok_s divides by
    wall clock (spec: the gap between the two is diagnostic on its own)."""
    metrics = compute_metrics(_synthetic_run(), thinking_level="high")

    assert metrics.requests == 2
    assert metrics.ttft_first_ms == 200
    assert metrics.ttft_mean_ms == pytest.approx(150.0)
    assert metrics.ttft_max_ms == 200
    assert metrics.in_tok == 150
    assert metrics.out_tok == 30
    assert metrics.reasoning_tok == 33
    assert metrics.tool_ms == 300
    # gen_seconds = (1.0-0.0) + (1.8-1.3) = 1.5s busy; 30 output tokens
    assert metrics.gen_tok_s == pytest.approx(30 / 1.5, rel=1e-3)
    # e2e uses the full 2000ms wall clock, not just busy time
    assert metrics.e2e_tok_s == pytest.approx(30 / 2.0, rel=1e-3)


def test_compute_metrics_flags_thinking_noop():
    """thinking != 'off' but reasoning_tok == 0 means the backend silently
    ignored the level — the row is not comparable to its partner row."""
    metrics = compute_metrics(_synthetic_run(reasoning_tok=0), thinking_level="high")
    assert metrics.thinking_noop is True


def test_compute_metrics_thinking_off_is_never_flagged_as_noop():
    metrics = compute_metrics(_synthetic_run(reasoning_tok=0), thinking_level="off")
    assert metrics.thinking_noop is False


def test_compute_metrics_flags_cold_first_token():
    """ttft_first_ms >= 3x ttft_mean_ms means the model was still loading
    despite warmup — flagged, not silently averaged into the mean."""
    events = [
        {"ts": 0.0, "event": {"type": "message_start"}},
        {"ts": 3.0, "event": {"type": "message_update", "delta": {"type": "text_delta", "text": "a"}}},
        {"ts": 3.1, "event": {"type": "message_end", "usage": {"input": 10, "output": 5, "reasoning": 0}}},
        {"ts": 3.1, "event": {"type": "message_start"}},
        {"ts": 3.15, "event": {"type": "message_update", "delta": {"type": "text_delta", "text": "b"}}},
        {"ts": 3.2, "event": {"type": "message_end", "usage": {"input": 10, "output": 5, "reasoning": 0}}},
    ]
    run_result = PiRunResult(completed=True, timed_out=False, wall_ms=3200, events=events, unparsed_lines=0, message_end_seen=True)
    metrics = compute_metrics(run_result, thinking_level="off")
    assert metrics.cold_first_token is True


def test_compute_metrics_end_to_end_with_fake_agent():
    """Sanity check against the real subprocess path (Task 6's fixture),
    not just synthetic event lists — proves compute_metrics accepts exactly
    what run_pi_process actually produces."""
    result = run_pi_process([sys.executable, str(FAKE_AGENT)], cwd=Path.cwd(), env=os.environ.copy(), timeout_s=10)
    metrics = compute_metrics(result, thinking_level="off")
    assert metrics.in_tok == 382
    assert metrics.out_tok == 38
    assert metrics.reasoning_tok == 33
    assert metrics.tool_ms >= 40  # fixture sleeps 0.05s between tool start/end
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_pidriver.py -v`
Expected: FAIL — `compute_metrics`/`RowMetrics` don't exist yet

- [ ] **Step 3: Write the implementation**

Append to `modelman/src/modelman/benchmark/agent/pidriver.py`:
```python
@dataclass
class RowMetrics:
    wall_ms: int
    requests: int
    turns: int
    ttft_first_ms: int | None
    ttft_mean_ms: float | None
    ttft_max_ms: int | None
    gen_tok_s: float | None
    e2e_tok_s: float | None
    in_tok: int
    out_tok: int
    cache_read_tok: int
    reasoning_tok: int
    tool_ms: int
    cost_usd: float
    unparsed_lines: int
    thinking_noop: bool
    cold_first_token: bool


def compute_metrics(run_result: PiRunResult, *, thinking_level: str) -> RowMetrics:
    requests = turns = 0
    in_tok = out_tok = cache_read_tok = reasoning_tok = 0
    cost_usd = 0.0
    tool_ms = 0
    gen_seconds = 0.0
    gen_tokens = 0
    ttfts: list[float] = []

    msg_start_ts: float | None = None
    first_text_seen = False
    tool_start_ts: float | None = None

    for entry in run_result.events:
        ts = entry["ts"]
        ev = entry["event"]
        etype = ev.get("type")

        if etype == "turn_start":
            turns += 1
        elif etype == "message_start":
            requests += 1
            msg_start_ts = ts
            first_text_seen = False
        elif etype == "message_update":
            delta = ev.get("delta", {})
            if delta.get("type") == "text_delta" and not first_text_seen and msg_start_ts is not None:
                first_text_seen = True
                ttfts.append(ts - msg_start_ts)
        elif etype == "message_end":
            if msg_start_ts is not None:
                gen_seconds += max(ts - msg_start_ts, 0.0)
            usage = ev.get("usage", {})
            in_tok += usage.get("input", 0)
            out_tok += usage.get("output", 0)
            reasoning_tok += usage.get("reasoning", 0)
            cache_read_tok += usage.get("cache_read", 0)
            cost_usd += usage.get("cost", {}).get("total", 0.0)
            gen_tokens += usage.get("output", 0)
            msg_start_ts = None
        elif etype == "tool_execution_start":
            tool_start_ts = ts
        elif etype == "tool_execution_end" and tool_start_ts is not None:
            tool_ms += int((ts - tool_start_ts) * 1000)
            tool_start_ts = None

    ttft_first_ms = int(ttfts[0] * 1000) if ttfts else None
    ttft_mean_ms = round(sum(ttfts) / len(ttfts) * 1000, 2) if ttfts else None
    ttft_max_ms = int(max(ttfts) * 1000) if ttfts else None

    gen_tok_s = round(gen_tokens / gen_seconds, 2) if gen_seconds > 0 else None
    e2e_tok_s = round(out_tok / (run_result.wall_ms / 1000), 2) if run_result.wall_ms > 0 else None

    thinking_noop = thinking_level != "off" and reasoning_tok == 0
    cold_first_token = bool(
        ttft_first_ms is not None and ttft_mean_ms and ttft_first_ms >= 3 * ttft_mean_ms
    )

    return RowMetrics(
        wall_ms=run_result.wall_ms,
        requests=requests,
        turns=turns,
        ttft_first_ms=ttft_first_ms,
        ttft_mean_ms=ttft_mean_ms,
        ttft_max_ms=ttft_max_ms,
        gen_tok_s=gen_tok_s,
        e2e_tok_s=e2e_tok_s,
        in_tok=in_tok,
        out_tok=out_tok,
        cache_read_tok=cache_read_tok,
        reasoning_tok=reasoning_tok,
        tool_ms=tool_ms,
        cost_usd=round(cost_usd, 6),
        unparsed_lines=run_result.unparsed_lines,
        thinking_noop=thinking_noop,
        cold_first_token=cold_first_token,
    )
```

Also add `import pytest` at the top of `test_pidriver.py` (used by `pytest.approx` in this task's tests).

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_pidriver.py -v`
Expected: PASS (15 tests)

- [ ] **Step 5: Run the full pidriver+workspace+task test slice and commit**

Run: `uv run pytest tests/benchmark/agent/ -v`
Expected: PASS (all tests from Tasks 1–7)

```bash
git add modelman/src/modelman/benchmark/agent/pidriver.py \
        modelman/tests/benchmark/agent/test_pidriver.py
git commit -m "feat(agent-bench): compute speed metrics from pi event stream - completes plan item #7"
```

---
