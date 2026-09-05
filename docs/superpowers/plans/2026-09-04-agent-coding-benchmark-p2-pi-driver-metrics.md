# Agent coding benchmark — Phase 2: pi driver + metrics (plan extract)

> Part of [the agent-coding-benchmark plan index](2026-09-04-agent-coding-benchmark.md). Shared Goal / Architecture / Tech Stack / Global Constraints and the phase order live there.


## Phase 2: pi driver + metrics

**Listings in this file were rebuilt from the shipped code after Phase 2 executed.** The original plan carried five more defects, all of which would have surfaced as a green test suite and a broken production path — several of them exactly the "silently reports zero" failure mode the design warns about:

12. **Task 5's tests 6–8 resolved a `route=litellm` row against `live_models_path = tmp_path / "missing.json"`** and then expected a target back, but `resolve_pi_target` raises `BenchmarkError` without a LiteLLM apiKey — the behavior test 5 asserts. Three of seven tests could never pass. A `_live_litellm_path(tmp_path)` helper supplies a resolvable gateway entry, which also keeps the tests from reading the real `~/.pi/agent/models.json`.
13. **Task 6's fake agent wrote no session file, and its test helper passed `--session-dir` only sometimes.** Gate 1 (`SESSION_CONTINUITY`) globs for a `*.jsonl` under the run root, so the fake has to produce one and every invocation has to be given a directory; `_fake_agent_cmd(session_dir, extra_args)` always appends it now.
14. **`_read_stdout` referenced `proc` as a free variable.** It is a module-level function, so that name does not exist: the reader thread `NameError`s on the first line of the first real run, `events` stays empty, and every row reports zero tokens — the exact production failure this phase exists to prevent, reached through the one path no test exercised.
15. **The poll loop exited on `seen_message_end and proc.poll() is not None`,** so a run whose model never replied polled until the full wall-clock timeout: a 40-minute row that died at second three cost 40 minutes. Aborting the reader on that path also risked dropping `turn_end`/`agent_end`/`agent_settled`. It now exits when the child exits and lets the reader drain to EOF; `seen_message_end` stays as a reported fact, not an exit condition.
16. **`compute_metrics` timed off `event["ts_ms"]`, a field pi never emits,** with a fallback that scaled by event index. pi's JSONL carries no usable clock: `turn_start` has no timestamp at all, and an assistant `message_end` repeats the `message.timestamp` its `message_start` already carried (live capture: both `1788614844374`). Under that fallback a uniform turn shape yields a constant TTFT and a `gen_seconds` of `0.0` whenever start and end land in the same index bucket — and a constant zero `gen_seconds` hides a 200x throughput difference, which is the measurement the harness exists to make. `run_pi_process` now stamps `_ts` (seconds since run start) as the reader thread reads each line, and the metrics are labelled as arrival-based, not provider-side.

The tests that should have caught 14–16 were all missing: nothing asserted on the session file, nothing used a non-default timeout, and Task 7 pinned arithmetic against fields the implementation had invented. Global Constraints gained a rule that `compute_metrics` and `run_pi_process` are only trustworthy against a captured real stream; the capture is now the source for both the fixture and the tests.

**Ordering note:** the spec's module table lists `pidriver.py` as depending on `suite.py`. This plan builds `pidriver.py` first (Phase 2) and `suite.py` fourth (Phase 4) so Phase 2 is testable standalone, per the spec's own phasing ("Phase 1–2 are the only phases with genuine unknown risk"). `RowConfig` and `DirectRouteConfig` are therefore defined in `pidriver.py`, and `suite.py` imports them from there in Phase 4 — the reverse of the spec's table, but the same two types either way.

### Task 5: Route resolution + `models.json` generation

**Files:**
- Create: `modelman/src/modelman/benchmark/agent/pidriver.py`
- Test: `modelman/tests/benchmark/agent/test_pidriver.py`

**Interfaces:**
- Produces: `RowConfig` (`label, model_id, thinking, route, provider_id, direct_model=None`), `DirectRouteConfig` (`base_url, api`), `PiTarget` (`pi_provider, launch_id, base_url, api, api_key, context_window, reasoning`, property `.model_arg`), `resolve_pi_target(row, model_name, routes_direct, live_models_path=LIVE_PI_MODELS_PATH) -> PiTarget`, `build_models_json(target) -> dict`, `write_pi_config(target, config_dir) -> Path`, `build_pi_command(target, thinking, session_dir, prompt) -> list[str]`. `suite.py` (Task 12) imports `RowConfig`/`DirectRouteConfig`; `runner.py` (Task 14) imports everything else from this module.

- [ ] **Step 1: Write the failing test**

`modelman/tests/benchmark/agent/test_pidriver.py`:
```python
LITELLM_ID = "ollama/qwen3.8:27b-mlx"



def _live_litellm_path(tmp_path: Path) -> Path:
    """A stand-in for ~/.pi/agent/models.json.

    Never read the real file (hermetic), and never pass a *missing* one to a
    litellm row: resolve_pi_target raises without an apiKey, so tests that
    only care about the config shape still have to supply a resolvable
    gateway entry.
    """
    path = tmp_path / "models.json"
    path.write_text(
        json.dumps(
            {
                "providers": {
                    "litellm": {
                        "baseUrl": "http://localhost:4000/v1",
                        "apiKey": "sk-test-key",
                        "models": [],
                    }
                }
            }
        ),
        encoding="utf-8",
    )
    return path


def _litellm_row() -> RowConfig:
    return RowConfig(
        label="r1", model_id=LITELLM_ID, thinking="off", route="litellm", provider_id="ollama"
    )


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
                        "models": [
                            {"id": LITELLM_ID, "contextWindow": 131072, "reasoning": False}
                        ],
                    }
                }
            }
        ),
        encoding="utf-8",
    )

    target = resolve_pi_target(_litellm_row(), "qwen3.8:27b-mlx", routes_direct={}, live_models_path=live)

    assert target.pi_provider == "litellm"
    assert target.launch_id == LITELLM_ID
    assert target.model_arg == f"litellm/{LITELLM_ID}"
    assert target.api_key == "sk-live-key"
    assert target.context_window == 131072
    assert target.reasoning is False


def test_direct_route_keys_by_bare_model_name(tmp_path):
    """route=direct addresses the backend's own endpoint by its bare
    model_name, per [routes.direct.<provider>] in the suite."""
    row = RowConfig(
        label="r1",
        model_id="omlx/Ornith-1.5-35B-A3B-MLX-6bit",
        thinking="high",
        route="direct",
        provider_id="omlx",
    )
    routes_direct = {
        "omlx": DirectRouteConfig(base_url="http://localhost:8000/v1", api="openai-completions")
    }

    target = resolve_pi_target(
        row, "Ornith-1.5-35B-A3B-MLX-6bit", routes_direct, live_models_path=tmp_path / "missing.json"
    )

    assert target.pi_provider == "omlx"
    assert target.launch_id == "Ornith-1.5-35B-A3B-MLX-6bit"
    assert target.model_arg == "omlx/Ornith-1.5-35B-A3B-MLX-6bit"
    assert target.base_url == "http://localhost:8000/v1"
    assert target.api_key == "ollama"  # placeholder — pi rejects an empty apiKey


def test_direct_route_can_override_the_launch_id(tmp_path):
    """direct_model names what the backend actually serves when the registry
    model_name does not — omlx registers `mlx-community/X` but serves `X`. A
    wrong launch id is not a 404 the harness can tell apart from a model that
    failed the task, so the escape hatch has to exist."""
    row = RowConfig(
        label="r1",
        model_id="omlx/mlx-community--Qwen3.8-27B-4bit",
        thinking="off",
        route="direct",
        provider_id="omlx",
        direct_model="Qwen3.8-27B-4bit",
    )
    routes_direct = {
        "omlx": DirectRouteConfig(base_url="http://localhost:8000/v1", api="openai-completions")
    }

    target = resolve_pi_target(
        row, "mlx-community/Qwen3.8-27B-4bit", routes_direct, live_models_path=tmp_path / "missing.json"
    )

    assert target.launch_id == "Qwen3.8-27B-4bit"
    assert target.model_arg == "omlx/Qwen3.8-27B-4bit"


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
    target = resolve_pi_target(
        _litellm_row(), "qwen3.8:27b-mlx", routes_direct={}, live_models_path=_live_litellm_path(tmp_path)
    )
    doc = build_models_json(target)
    provider = doc["providers"]["litellm"]
    entry = provider["models"][0]
    assert entry["_launch"] is True
    assert entry["id"] == LITELLM_ID
    assert entry["input"] == ["text", "image"]
    assert provider["apiKey"] == "sk-test-key"  # written per-run, never into the user's file
    assert provider["baseUrl"] == "http://localhost:4000/v1"
    assert provider["api"] == "openai-completions"


def test_write_pi_config_writes_models_json(tmp_path):
    target = resolve_pi_target(
        _litellm_row(), "qwen3.8:27b-mlx", routes_direct={}, live_models_path=_live_litellm_path(tmp_path)
    )
    config_dir = tmp_path / "run-config"
    path = write_pi_config(target, config_dir)
    assert path == config_dir / "models.json"
    assert json.loads(path.read_text(encoding="utf-8"))["providers"]["litellm"]["models"][0]["id"] == LITELLM_ID


def test_build_pi_command_shape(tmp_path):
    row = RowConfig(
        label="r1", model_id="ollama/x", thinking="high", route="litellm", provider_id="ollama"
    )
    target = resolve_pi_target(
        row, "x", routes_direct={}, live_models_path=_live_litellm_path(tmp_path)
    )
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
    direct_model: str | None = None  # overrides the launch id for route=direct


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
        # Registry model_name is the launch id only when the backend serves
        # that exact string (true for ollama, whose model_names are bare).
        # omlx registers `mlx-community/Qwen3.8-27B-4bit` while serving the
        # basename, so a direct omlx row needs direct_model. A misaddressed
        # agent row is indistinguishable from a model that failed the task,
        # which is the one confusion this harness must not allow.
        launch_id = row.direct_model or model_name
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
Expected: PASS (8 tests)

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
- Produces: `PiRunResult` (`exit_code: int | None, timed_out: bool, aborted: bool, seen_message_end: bool, unparsed_lines: int, stderr_tail: str`); `run_pi_process(cmd: list[str], workspace_path: Path, timeout_seconds: float, poll_interval: float = 0.5, abort_event: threading.Event | None = None, idle_seconds: float | None = None) -> tuple[list[dict], PiRunResult]`. It returns pi's own event dicts with one field added: `_ts`, seconds since run start, stamped by the reader thread as each line is read. gates.py (Task 9) consumes that event list; `report.py` (Task 20) archives it.

The fake agent pins the exact JSONL event shape from the spec's "Verified against the live setup" section, which was re-captured from a real `pi --mode json` run while reviewing this plan. **The nesting is not guessable**, and a fixture written from memory is the one thing that can make this whole phase pass its tests while reporting zeros against real runs:

- deltas arrive as `message_update.assistantMessageEvent` with the text in a string field called `delta` — there is no top-level `delta` object and no `text` key.
- authoritative usage is `message_end.message.usage`, with **camelCase** `cacheRead`/`cacheWrite` — not `message_end.usage`, not `cache_read`.
- `message_start`/`message_end` fire for the **user** message as well as the assistant's, so anything that counts requests or watches for `message_end` must filter on `message.role == "assistant"`.
- tool events carry `toolCallId`/`toolName` (not `name`), and are paired by `toolCallId`.

The fixture emits that shape — `session, agent_start, turn_start`, the user `message_start`/`message_end` pair, the assistant `message_start`, `message_update` thinking/text deltas, a `tool_execution_start`/`end` pair, the assistant `message_end` with its nested `usage`, then `turn_end, agent_end, agent_settled` — so every later test builds on one fixed, known-good stream.

- [ ] **Step 1: Write the fake agent fixture**

`modelman/tests/benchmark/agent/fixtures/fake_agent.py`:
```python
#!/usr/bin/env python3
"""Deterministic stand-in for `pi --mode json`, used by pidriver tests.

Emits the event shape captured from a real `pi --mode json` run (see the
spec's "Verified against the live setup"): the user message gets its own
message_start/message_end pair, assistant deltas arrive as
message_update.assistantMessageEvent with a string `delta`, authoritative
usage is message_end.message.usage with camelCase cacheRead, and tool
events carry toolName. Written from the capture rather than from memory on
purpose — a fixture that guesses the nesting is what would let every
pidriver test pass while real rows report zero tokens.

--hang/--delay/--malformed-line/--no-assistant-reply exercise the timeout,
unparsed-line and gate-1 paths without a real multi-minute agent run.
"""

import argparse
import json
import time
from pathlib import Path


def _emit(event: dict) -> None:
    print(json.dumps(event), flush=True)


def _usage(**overrides) -> dict:
    usage = {
        "input": 0,
        "output": 0,
        "cacheRead": 0,
        "cacheWrite": 0,
        "totalTokens": 0,
        "cost": {"input": 0.0, "output": 0.0, "cacheRead": 0.0, "cacheWrite": 0.0, "total": 0.0},
    }
    usage.update(overrides)
    usage["totalTokens"] = usage["input"] + usage["output"] + usage.get("reasoning", 0)
    return usage


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--hang", action="store_true")
    parser.add_argument("--delay", type=float, default=0.0)
    parser.add_argument("--malformed-line", action="store_true")
    parser.add_argument("--no-assistant-reply", action="store_true")
    parser.add_argument("--session-dir", default=None)
    args, _ = parser.parse_known_args()

    session_id = "fake-session"

    def _write_session_file() -> None:
        """Real pi writes the transcript into --session-dir, creating the
        directory if needed. Gate 1 (SESSION_CONTINUITY) globs for exactly that
        file, so the fake has to produce it or the gate is never really tested."""
        if not args.session_dir:
            return
        session_dir = Path(args.session_dir)
        session_dir.mkdir(parents=True, exist_ok=True)
        (session_dir / f"{session_id}.jsonl").write_text(
            json.dumps({"type": "session", "id": session_id}) + "\n", encoding="utf-8"
        )

    _emit({
        "type": "session",
        "version": 3,
        "id": "fake-session",
        "timestamp": "2026-01-01T00:00:00.000Z",
        "cwd": "/tmp/does-not-matter",
    })
    _emit({"type": "agent_start"})
    _emit({"type": "turn_start"})

    # pi echoes the user message as its own start/end pair. Nothing here may
    # count it as a request or treat its message_end as "the agent replied".
    user_message = {"role": "user", "content": [{"type": "text", "text": "fake prompt"}], "timestamp": 0}
    _emit({"type": "message_start", "message": user_message})
    _emit({"type": "message_end", "message": user_message})
    if args.no_assistant_reply:
        _write_session_file()
        return

    _emit({
        "type": "message_start",
        "message": {
            "role": "assistant", "content": [], "api": "openai-completions", "provider": "litellm",
            "model": "litellm/fake", "usage": _usage(), "stopReason": "pending", "timestamp": 0,
        },
    })
    time.sleep(args.delay)

    def _update(assistant_event: dict) -> None:
        # message_update carries cumulative usage (zero until the provider
        # reports it) and the delta itself; there is no `message` key.
        _emit({"type": "message_update", "usage": _usage(), "assistantMessageEvent": assistant_event})

    _update({"type": "thinking_start", "contentIndex": 0})
    _update({"type": "thinking_delta", "contentIndex": 0, "delta": "..."})
    _update({"type": "thinking_end", "contentIndex": 0})
    _update({"type": "text_start", "contentIndex": 1})
    _update({"type": "text_delta", "contentIndex": 1, "delta": "Looking into it"})
    _update({"type": "text_end", "contentIndex": 1, "delta": "Looking into it"})
    if args.malformed_line:
        print("{not json", flush=True)

    _emit({"type": "tool_execution_start", "toolCallId": "tc-1", "toolName": "read", "args": {"path": "pkg/__init__.py"}})
    time.sleep(0.05)
    _emit({"type": "tool_execution_end", "toolCallId": "tc-1", "toolName": "read", "result": {"content": []}, "isError": False})

    final_message = {
        "role": "assistant",
        "content": [
            {"type": "thinking", "thinking": "..."},
            {"type": "text", "text": "Looking into it"},
        ],
        "api": "openai-completions",
        "provider": "litellm",
        "model": "litellm/fake",
        "usage": _usage(input=382, output=38, reasoning=33),
        "stopReason": "stop",
        "timestamp": 0,
    }
    _emit({"type": "message_end", "message": final_message})
    _emit({"type": "turn_end", "message": final_message, "toolResults": []})
    _emit({"type": "agent_end", "messages": [user_message, final_message], "willRetry": False})
    _emit({"type": "agent_settled"})
    _write_session_file()

    if args.hang:
        time.sleep(3600)


if __name__ == "__main__":
    main()
```

- [ ] **Step 2: Write the failing test**

Append to `modelman/tests/benchmark/agent/test_pidriver.py`:
```python
FAKE_AGENT = Path(__file__).parent / "fixtures" / "fake_agent.py"
_AGENT_FIXTURES_DIR = Path(__file__).parent / "fixtures" / "tasks" / "mini-drift"


def _fake_agent_cmd(session_dir: Path, extra_args: list[str] | None = None) -> list[str]:
    """Every invocation carries --session-dir, as the real command does: gate 1
    needs a session file on disk, and a fake that wrote none would leave the
    whole session-continuity path untested."""
    return [sys.executable, str(FAKE_AGENT), *(extra_args or []), "--session-dir", str(session_dir)]


def test_run_pi_process_completes_normally(tmp_path):
    # the session dir sits inside the run root, which is how the real runner lays it out
    events, result = run_pi_process(
        _fake_agent_cmd(tmp_path), workspace_path=tmp_path, timeout_seconds=10, poll_interval=0.01
    )
    assert result.timed_out is False
    assert result.exit_code == 0
    assert events[-1]["type"] == "agent_settled"
    assert any(e["type"] == "message_end" for e in events)
    # real pi writes the transcript into --session-dir; gate 1 needs that proof
    assert any(p.suffix == ".jsonl" for p in tmp_path.rglob("*.jsonl"))


def test_run_pi_process_ignores_the_user_message_end_when_checking_for_a_reply(tmp_path):
    """`--no-assistant-reply` ends the stream right after the *user* message's
    message_end. Real pi echoes the prompt as its own message_start/message_end
    pair, so a seen_message_end that fired on it would call a run that never
    produced a reply a clean one."""
    events, result = run_pi_process(
        _fake_agent_cmd(tmp_path / "session", ["--no-assistant-reply"]),
        workspace_path=tmp_path,
        timeout_seconds=10,
        poll_interval=0.01,
    )
    assert any(
        e["type"] == "message_end" and e["message"]["role"] == "user" for e in events
    ), "the fixture must actually emit the user message_end this test is about"
    assert result.timed_out is False
    assert result.seen_message_end is False
    assert result.exit_code == 0


def test_run_pi_process_counts_unparsed_lines(tmp_path):
    _, result = run_pi_process(
        _fake_agent_cmd(tmp_path / "session", ["--malformed-line"]),
        workspace_path=tmp_path, timeout_seconds=10, poll_interval=0.01,
    )
    assert result.unparsed_lines == 1


def test_run_pi_process_kills_process_group_on_hard_timeout(tmp_path):
    # --hang sleeps 3600s *after* already emitting agent_settled; the only way
    # the loop can still be waiting on the stdout reader thread — and therefore
    # the only way this test can prove the process-group kill path runs at all —
    # is an overall timeout below when stdout actually closes on its own.
    start = time.monotonic()
    _, result = run_pi_process(
        _fake_agent_cmd(tmp_path / "session", ["--hang"]),
        workspace_path=tmp_path, timeout_seconds=1.0, poll_interval=0.01,
    )
    elapsed = time.monotonic() - start
    assert result.timed_out is True
    assert elapsed < 10


def test_run_pi_process_writes_a_session_file_the_gates_can_find(tmp_path):
    """Gate 1 (SESSION_CONTINUITY) looks for a *.jsonl under the run's root, so
    a driver that never produced one could never pass a run. Mirrors wt's
    launchPi: session_dir goes in the command, the agent writes it."""
    from modelman.benchmark.agent.task import load_task

    # parents[4] is the monorepo root; see the note in test_day31_drift_bundle.py
    bundle = load_task(Path(__file__).resolve().parents[4] / "benchmarks" / "tasks" / "day31-drift")
    ws = create_workspace(bundle)
    session_dir = ws.root / "pi-session"
    try:
        target = PiTarget(
            pi_provider="litellm", launch_id="x", base_url="http://x",
            api="openai-completions", api_key="k", context_window=1000, reasoning=False,
        )
        cmd = build_pi_command(target, "off", session_dir, "do the task")
        assert cmd[cmd.index("--session-dir") + 1] == str(session_dir)
        # --session-dir takes exactly one argument and must not swallow the next flag
        assert cmd[cmd.index("--session-dir") + 2] == "--model"
        run_pi_process(
            _fake_agent_cmd(session_dir), workspace_path=ws.root, timeout_seconds=10, poll_interval=0.01
        )
        assert any(p.name.endswith(".jsonl") for p in session_dir.glob("*.jsonl"))
    finally:
        destroy_workspace(ws)
```

- [ ] **Step 3: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_pidriver.py -v`
Expected: FAIL — `run_pi_process` doesn't exist yet

- [ ] **Step 4: Write the implementation**

Append to `modelman/src/modelman/benchmark/agent/pidriver.py`, merging into the existing top-of-file import block: `contextlib`, `os`, `signal`, `subprocess`, `threading`, `time` (no `queue` — the reader drains into a list of `(arrival, line)` pairs, and `contextlib.suppress` is what `make check`'s ruff wants for the kill sites):
```python
@dataclass
class PiRunResult:
    exit_code: int | None
    timed_out: bool
    aborted: bool
    # the assistant's own final message_end; the user echo of the prompt also
    # arrives as a message_end and must not count here, or a run that never got
    # a reply is recorded as clean
    seen_message_end: bool
    unparsed_lines: int
    stderr_tail: str


def _read_stdout(
    proc: subprocess.Popen[bytes],
    lines: list[tuple[float, bytes]],
    stop: threading.Event,
) -> None:
    """Reader thread: drain stdout into `lines` as (monotonic arrival, text).

    The arrival time is recorded here, at the moment the line is read, not when
    the poll loop parses it — that gap is the whole basis for the timing
    metrics, since pi's own events carry no clock (see _arrival_s).

    `proc` is a parameter, not a free variable: this is a module-level function
    and would otherwise NameError the first time a real row ran it.
    """
    assert proc.stdout is not None
    for raw_line in proc.stdout:
        if stop.is_set():
            return
        if not raw_line.strip():
            continue
        lines.append((time.monotonic(), raw_line))


def run_pi_process(
    cmd: list[str],
    workspace_path: Path,
    timeout_seconds: float,
    poll_interval: float = 0.5,
    abort_event: threading.Event | None = None,
    idle_seconds: float | None = None,
) -> tuple[list[dict], PiRunResult]:
    """Run one pi session, streaming and parsing its JSONL event output.

    Mirrors wt/internal/agents/pi.go's launchPi (own process group via
    setsid, killpg on timeout) rather than reimplementing that logic in
    Python from scratch — the shell-escaping and process-tree-kill concerns
    there (commented outright as things that broke real runs) apply just as
    much to this driver.
    """
    abort = abort_event or threading.Event()
    stdout_lines: list[tuple[float, bytes]] = []
    stderr_chunks: list[bytes] = []
    events: list[dict] = []
    seen_message_end = False
    unparsed_lines = 0
    exit_code: int | None = None
    timed_out = False
    aborted = False

    started_monotonic = time.monotonic()
    proc = subprocess.Popen(
        cmd,
        cwd=workspace_path,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        stdin=subprocess.DEVNULL,
        start_new_session=True,
    )

    reader = threading.Thread(target=_read_stdout, args=(proc, stdout_lines, abort))
    reader.daemon = True
    reader.start()

    def _drain_new_lines() -> None:
        nonlocal unparsed_lines, seen_message_end
        consumed = len(events) + unparsed_lines
        for arrival, raw_line in stdout_lines[consumed:]:
            try:
                event = json.loads(raw_line)
            except json.JSONDecodeError:
                unparsed_lines += 1
                continue
            if not isinstance(event, dict):
                unparsed_lines += 1
                continue
            event["_ts"] = arrival - started_monotonic
            events.append(event)
            if event.get("type") == "message_end" and (
                (event.get("message") or {}).get("role") == "assistant"
            ):
                seen_message_end = True

    deadline = time.monotonic() + timeout_seconds
    idle_deadline: float | None = None

    def _reset_idle_deadline() -> None:
        nonlocal idle_deadline
        idle_deadline = time.monotonic() + idle_seconds if idle_seconds else None

    _reset_idle_deadline()

    while True:
        time.sleep(poll_interval)
        _drain_new_lines()
        now = time.monotonic()

        if abort.is_set():
            aborted = True
            break
        if proc.poll() is not None:
            # The child is gone, so stdout is closing: let the reader thread
            # reach EOF and finish the stream instead of aborting it, or the
            # tail (turn_end, agent_end, agent_settled) is lost. Exiting on
            # "process gone" rather than on "saw an assistant message_end" is
            # also what keeps a run that never got a reply from consuming its
            # entire wall-clock budget — a 40-minute row whose model died at
            # second three must fail in seconds.
            reader.join(timeout=2.0)
            break
        if now >= deadline or (idle_deadline is not None and now >= idle_deadline):
            timed_out = True
            break
        if events:
            # activity resets the idle clock; idle_seconds only fires on a
            # stream that has gone completely quiet
            _reset_idle_deadline()

    kill_grace = 5.0
    if timed_out or aborted:
        with contextlib.suppress(ProcessLookupError):
            os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
        # only a killed run stops its reader early; a clean exit lets it drain
        abort.set()
    try:
        proc.wait(timeout=kill_grace)
    except subprocess.TimeoutExpired:
        with contextlib.suppress(ProcessLookupError):
            os.killpg(os.getpgid(proc.pid), signal.SIGKILL)

    # Bounded join: a grandchild inheriting stdout could block this forever, and
    # the events already read are still usable.
    reader.join(timeout=poll_interval * 2 + 0.5)

    _drain_new_lines()
    if proc.stderr is not None:
        with contextlib.suppress(Exception):
            remaining = proc.stderr.read()
            if remaining:
                stderr_chunks.append(remaining)
    stderr_tail = b"".join(stderr_chunks).decode("utf-8", errors="replace")[-2000:]

    if exit_code is None:
        exit_code = proc.returncode

    return events, PiRunResult(
        exit_code=exit_code,
        timed_out=timed_out,
        aborted=aborted,
        seen_message_end=seen_message_end,
        unparsed_lines=unparsed_lines,
        stderr_tail=stderr_tail,
    )
```

- [ ] **Step 5: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_pidriver.py -v`
Expected: PASS (13 tests — 8 from Task 5, 5 from Task 6)

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
- Produces: `AgentMetrics` dataclass (`requests, turns, gen_seconds, input_tok, output_tok, cache_read_tok, cache_write_tok, reasoning_tok, tool_call_count, ttfts_ms, ttft_first_ms, ttft_subseq_median_ms, cold_first_token, thinking_off_reasoning, wall_seconds, final_text, anomaly`); `compute_metrics(events: Sequence[dict], start_wall: float, end_wall: float, log_fn: Callable[[str], None] | None = None, thinking: str = "off") -> AgentMetrics`. gates.py doesn't touch this; report.py (Task 20) and runner.py (Task 14) both consume `AgentMetrics` by these exact field names — the summary table's speed columns (Task 20) read them directly.

This closes out Phase 2 — the spec calls this "where `gen_tok_s`/`e2e_tok_s` semantics get frozen," so the test below pins the arithmetic with a synthetic, hand-computed event sequence rather than trusting the fake agent's real timing (which is not deterministic enough to assert exact numbers on).

- [ ] **Step 1: Write the failing test**

Append to `modelman/tests/benchmark/agent/test_pidriver.py` (merge `import os`,
`import sys` and the new `from modelman...` line into the file's existing
top-of-file import block — ruff's `E402` rejects imports placed after module
code, and `make check` runs `ruff check tests/`):
```python
# ---------------------------------------------------------------------------
# Metrics extraction
#
# pi's own JSONL carries no usable per-event clock: turn_start has no
# timestamp, and the assistant's message_end carries the same
# message.timestamp as its message_start (live capture, both 1788614844374).
# Every duration below therefore comes from `_ts` — the seconds-since-run-start
# the driver stamps on an event as it reads it off stdout (see run_pi_process) —
# with the tests setting it explicitly to stand in for that.
# ---------------------------------------------------------------------------

def _assistant_message_start(seq: int, ts_s: float | None = None) -> dict:
    event = {"type": "message_start", "seq": seq,
             "message": {"role": "assistant", "model": "fake", "usage": {}, "timestamp": 0}}
    if ts_s is not None:
        event["_ts"] = ts_s
    return event


def _user_message_echo(seq: int, ts_s: float | None = None) -> dict:
    # real pi echoes the prompt as its own message_start/message_end pair;
    # counting those would inflate requests and corrupt gen_seconds
    event = {"type": "message_start", "seq": seq,
             "message": {"role": "user", "content": [{"type": "text", "text": "prompt"}], "timestamp": 0}}
    if ts_s is not None:
        event["_ts"] = ts_s
    return event


def _user_message_end(seq: int, ts_s: float | None = None) -> dict:
    event = {"type": "message_end", "seq": seq,
             "message": {"role": "user", "content": [], "timestamp": 0}}
    if ts_s is not None:
        event["_ts"] = ts_s
    return event


def _assistant_message_end(seq: int, input_tok=100, output_tok=20, cache_read=5,
                           cache_write=2, reasoning=None, text="looks good",
                           ts_s: float | None = None) -> dict:
    usage = {"input": input_tok, "output": output_tok, "cacheRead": cache_read,
             "cacheWrite": cache_write}
    if reasoning is not None:
        usage["reasoning"] = reasoning
    event = {"type": "message_end", "seq": seq,
             "message": {"role": "assistant", "stopReason": "stop", "usage": usage,
                         "content": [{"type": "text", "text": text}], "timestamp": 0}}
    if ts_s is not None:
        event["_ts"] = ts_s
    return event


def _text_message_update(seq: int, delta: str, ts_s: float | None = None) -> dict:
    # real pi nests the delta under assistantMessageEvent; there is no
    # message.content[].text on a message_update event at all
    event = {"type": "message_update", "seq": seq,
             "assistantMessageEvent": {"type": "text_delta", "contentIndex": 0, "delta": delta}}
    if ts_s is not None:
        event["_ts"] = ts_s
    return event


def _tool_execution_start(seq: int, call_id: str, name: str) -> dict:
    return {"type": "tool_execution_start", "seq": seq, "toolCallId": call_id, "toolName": name}


def _tool_execution_end(seq: int, call_id: str, name: str) -> dict:
    return {"type": "tool_execution_end", "seq": seq, "toolCallId": call_id, "toolName": name}


def _session_event(seq: int = 0) -> dict:
    return {"type": "session", "seq": seq, "version": 3, "id": "s"}


def _turn(seq: int, at_s: float, ttft_ms: float, gen_ms: float, text: str = "x", **usage) -> list:
    """One assistant turn laid out in arrival seconds: turn_start, the
    message_start that answers it, a delta, and the message_end that closes it."""
    first_token_s = at_s + ttft_ms / 1000.0
    end_s = at_s + (ttft_ms + gen_ms) / 1000.0
    return [
        {"type": "turn_start", "seq": seq, "_ts": at_s},
        _assistant_message_start(seq + 1, first_token_s),
        _text_message_update(seq + 2, text, first_token_s),
        _assistant_message_end(seq + 3, ts_s=end_s, **usage),
        {"type": "turn_end", "seq": seq + 4, "_ts": end_s},
    ]


def _event_stream(**kw) -> list:
    return [_session_event(), {"type": "agent_start", "seq": 1}, *_turn(2, 0.0, 100.0, 400.0, **kw),
            {"type": "agent_end", "seq": 8, "willRetry": False}]


def test_compute_metrics_basic_counts():
    events = [
        _session_event(),
        {"type": "agent_start", "seq": 1},
        *_turn(2, 0.0, 1000.0, 1000.0, input_tok=382, output_tok=38, cache_read=100, cache_write=10),
        _user_message_echo(8, 2.0),
        _user_message_end(9, 2.5),
        *_turn(10, 2.0, 1000.0, 1000.0, input_tok=400, output_tok=20, cache_read=200, cache_write=5),
        {"type": "agent_end", "seq": 16, "willRetry": False},
    ]
    m = compute_metrics(events, start_wall=0.0, end_wall=4.0)
    assert m.requests == 2, "the user-role message pair must not count as requests"
    assert m.turns == 2
    assert m.input_tok == 782
    assert m.output_tok == 58
    assert m.cache_read_tok == 300
    assert m.cache_write_tok == 15
    assert m.tool_call_count == 0
    assert m.wall_seconds == 4.0
    assert m.ttfts_ms == [1000.0, 1000.0]


def test_compute_metrics_tool_call_count_pairs_start_end():
    events = [
        _session_event(),
        _tool_execution_start(1, "tc-1", "read"),
        _tool_execution_end(2, "tc-1", "read"),
        _tool_execution_start(3, "tc-2", "bash"),
        _tool_execution_end(4, "tc-2", "bash"),
        _tool_execution_start(5, "tc-3", "edit"),
        # tc-3 never gets a tool_execution_end: an unpaired start is still a
        # call the agent issued
    ]
    m = compute_metrics(events, start_wall=0.0, end_wall=1.0)
    assert m.tool_call_count == 3


def test_compute_metrics_text_delta_accumulates_across_message_updates():
    events = [
        _session_event(),
        _assistant_message_start(1),
        _text_message_update(2, "Looking"),
        _text_message_update(3, " into it"),
        _assistant_message_end(4),
    ]
    m = compute_metrics(events, start_wall=0.0, end_wall=1.0)
    assert m.final_text == "Looking into it"


def test_compute_metrics_message_end_text_does_not_double_count_with_deltas():
    events = [
        _session_event(),
        _assistant_message_start(1),
        _text_message_update(2, "Looking"),
        _text_message_update(3, " into it"),
        _assistant_message_end(4, text="Looking into it"),
    ]
    m = compute_metrics(events, start_wall=0.0, end_wall=1.0)
    assert m.final_text == "Looking into it"


def test_compute_metrics_falls_back_to_message_end_text_without_deltas():
    """A stream that never emitted an incremental delta still has to yield a
    closing message, or gate 6 fails a run that did answer."""
    events = [_session_event(), _assistant_message_start(1), _assistant_message_end(2, text="done")]
    m = compute_metrics(events, start_wall=0.0, end_wall=1.0)
    assert m.final_text == "done"


def test_compute_metrics_reasoning_tokens_when_present():
    events = [_session_event(), _assistant_message_start(1), _assistant_message_end(2, reasoning=33)]
    m = compute_metrics(events, start_wall=0.0, end_wall=1.0)
    assert m.reasoning_tok == 33


def test_compute_metrics_reasoning_tokens_absent_is_zero_not_error():
    events = [_session_event(), _assistant_message_start(1), _assistant_message_end(2)]
    m = compute_metrics(events, start_wall=0.0, end_wall=1.0)
    assert m.reasoning_tok == 0


def test_compute_metrics_flags_thinking_off_reasoning_leak():
    """Live probe: healthy backend, --thinking off, and still a thinking block
    plus usage.reasoning = 11. Whether thinking is actually off is a per-backend
    config fact, so it is a flag and a log line, never a gate."""
    events = [_session_event(), _assistant_message_start(1), _assistant_message_end(2, reasoning=11)]
    m = compute_metrics(events, start_wall=0.0, end_wall=1.0, thinking="off")
    assert m.thinking_off_reasoning is True
    assert m.anomaly == ""


def test_compute_metrics_no_reasoning_leak_flag_when_thinking_on():
    events = [_session_event(), _assistant_message_start(1), _assistant_message_end(2, reasoning=500)]
    m = compute_metrics(events, start_wall=0.0, end_wall=1.0, thinking="high")
    assert m.thinking_off_reasoning is False


def test_compute_metrics_no_reasoning_leak_flag_when_off_and_none_emitted():
    events = [_session_event(), _assistant_message_start(1), _assistant_message_end(2)]
    m = compute_metrics(events, start_wall=0.0, end_wall=1.0, thinking="off")
    assert m.thinking_off_reasoning is False


def test_compute_metrics_flags_cache_anomaly():
    events = _event_stream(cache_read=100_000, cache_write=0)
    m = compute_metrics(events, start_wall=0.0, end_wall=1.0)
    assert "CACHE_ANOMALY" in m.anomaly


def test_compute_metrics_flags_cold_first_token():
    # 10s to the first token, 1s to each one after it
    events = [_session_event(), {"type": "agent_start", "seq": 1},
              *_turn(2, 0.0, 10_000.0, 1_000.0), *_turn(8, 12.0, 1_000.0, 1_000.0),
              *_turn(14, 15.0, 1_000.0, 1_000.0)]
    m = compute_metrics(events, start_wall=0.0, end_wall=17.0)
    assert m.ttfts_ms == [10_000.0, 1_000.0, 1_000.0]
    assert m.ttft_first_ms == 10_000.0
    assert m.ttft_subseq_median_ms == 1_000.0
    assert m.cold_first_token is True
    assert "COLD_FIRST_TOKEN" in m.anomaly


def test_compute_metrics_does_not_flag_cold_first_token_for_a_single_request():
    """One request has no second request to compare against. Comparing the
    first request against all requests (a mean including itself) makes the
    threshold unreachable; comparing it against itself must not flag."""
    events = [_session_event(), {"type": "agent_start", "seq": 1}, *_turn(2, 0.0, 5_000.0, 1_000.0)]
    m = compute_metrics(events, start_wall=0.0, end_wall=6.0)
    assert m.ttfts_ms == [5_000.0]
    assert m.cold_first_token is False
    assert "COLD_FIRST_TOKEN" not in m.anomaly


def test_compute_metrics_flags_repeated_failure():
    events = [
        _session_event(), {"type": "agent_start", "seq": 1},
        _tool_execution_start(2, "tc-1", "read"), _tool_execution_end(3, "tc-1", "read"),
        _tool_execution_start(4, "tc-2", "read"), _tool_execution_end(5, "tc-2", "read"),
        _tool_execution_start(6, "tc-3", "read"), _tool_execution_end(7, "tc-3", "read"),
        _tool_execution_start(8, "tc-4", "read"), _tool_execution_end(9, "tc-4", "read"),
        {"type": "agent_end", "seq": 10, "willRetry": False},
    ]
    m = compute_metrics(events, start_wall=0.0, end_wall=1.0)
    assert m.tool_call_count == 4
    assert "REPEATED_FAILURE" in m.anomaly


def test_compute_metrics_empty_stream_is_all_zero_not_crash():
    m = compute_metrics([], start_wall=0.0, end_wall=1.0)
    assert m.requests == 0 and m.input_tok == 0 and m.output_tok == 0
    assert m.ttfts_ms == [] and m.tool_call_count == 0
    assert m.final_text == ""
    assert m.anomaly == ""


def test_compute_metrics_gen_seconds_ignores_the_user_message_pair():
    """The prompt echo lands between the assistant's two turns. Generation time
    is assistant message_start -> message_end only."""
    events = [
        _session_event(),
        *_turn(2, 0.0, 1_000.0, 1_000.0),
        _user_message_echo(8, 2.0),
        _user_message_end(9, 2.5),
        *_turn(10, 3.0, 1_000.0, 1_000.0),
    ]
    m = compute_metrics(events, start_wall=0.0, end_wall=5.0)
    assert m.requests == 2
    assert m.ttfts_ms == [1_000.0, 1_000.0]
    assert m.gen_seconds == 2.0, "must not include the time spent on the user echo"


def test_metrics_log_written_per_run(tmp_path):
    from modelman.benchmark.agent.pidriver import _log
    from modelman.benchmark.agent.task import load_task

    ws = create_workspace(load_task(_AGENT_FIXTURES_DIR))
    log_path = tmp_path / "metrics.log"
    try:
        compute_metrics(
            [_session_event(), _assistant_message_start(1), _assistant_message_end(2)],
            start_wall=0.0, end_wall=1.0,
            log_fn=lambda msg: _log(msg, log_path),
        )
        assert log_path.exists()
        content = log_path.read_text(encoding="utf-8")
        assert "[seq 0] session" in content
        assert "in=100 out=20" in content
    finally:
        destroy_workspace(ws)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_pidriver.py -v`
Expected: FAIL — `compute_metrics`/`AgentMetrics` don't exist yet

- [ ] **Step 3: Write the implementation**

Append to `modelman/src/modelman/benchmark/agent/pidriver.py`:
```python
# ---------------------------------------------------------------------------
# Metrics extraction
# ---------------------------------------------------------------------------


@dataclass
class AgentMetrics:
    requests: int = 0
    turns: int = 0
    gen_seconds: float = 0.0
    input_tok: int = 0
    output_tok: int = 0
    cache_read_tok: int = 0
    cache_write_tok: int = 0
    reasoning_tok: int = 0
    tool_call_count: int = 0
    ttfts_ms: list[float] = field(default_factory=list)
    ttft_first_ms: float = 0.0
    ttft_subseq_median_ms: float = 0.0
    cold_first_token: bool = False
    thinking_off_reasoning: bool = False
    wall_seconds: float = 0.0
    final_text: str = ""
    anomaly: str = ""


def _extract_text(message_end_event: dict) -> str:
    message = message_end_event.get("message") or {}
    parts = []
    for block in message.get("content", []):
        if isinstance(block, dict) and block.get("type") == "text":
            parts.append(block.get("text", ""))
    return "".join(parts)


def _summarize_event(event: dict) -> str:
    etype = event.get("type", "?")
    ame = event.get("assistantMessageEvent") or {}
    if etype == "message_update" and ame.get("type") == "text_delta":
        return f"text_delta {len(ame.get('delta', ''))!r} chars"
    if etype == "message_update" and ame.get("type") == "thinking_delta":
        return "thinking_delta"
    if etype == "message_update":
        return ame.get("type", "update")
    if etype == "message_end":
        message = event.get("message") or {}
        usage = message.get("usage") or {}
        parts = [f"role={message.get('role')}"]
        if usage:
            parts.append(f"in={usage.get('input', 0)} out={usage.get('output', 0)}")
        return " ".join(parts)
    if etype == "tool_execution_start":
        return event.get("toolName", "?")
    if etype == "tool_execution_end":
        return f"{event.get('toolName', '?')} err={event.get('isError')}"
    if etype == "turn_end":
        return f"tools={len(event.get('toolResults', []))}"
    if etype == "agent_end":
        return f"messages={len(event.get('messages', []))} willRetry={event.get('willRetry')}"
    return ""


def _arrival_s(event: dict, index: int, total: int, duration_s: float) -> float:
    """Seconds since run start for this event.

    pi puts no clock on its events — turn_start has no timestamp at all, and an
    assistant message_end repeats the message.timestamp its message_start
    already carried (verified against the live capture, both 1788614844374) —
    so timing comes from the `_ts` run_pi_process stamps as each line is read
    off stdout. The proportional fallback exists for hand-built event lists.
    """
    ts = event.get("_ts")
    if isinstance(ts, (int, float)):
        return float(ts)
    return duration_s * (index / max(total, 1))


def _log(message: str, log_path: Path) -> None:
    log_path.parent.mkdir(parents=True, exist_ok=True)
    with log_path.open("a", encoding="utf-8") as f:
        f.write(message + "\n")


def compute_metrics(
    events: Sequence[dict],
    start_wall: float,
    end_wall: float,
    log_fn: Callable[[str], None] | None = None,
    thinking: str = "off",
) -> AgentMetrics:
    """Scan a pi JSONL event stream and compute every metric the spec's
    Metrics table defines. Pure function of the event list — no I/O,
    trivially testable.

    Nesting follows the real pi stream (spec, "Verified against the live
    setup"): the user message gets its own message_start/message_end pair, so
    request counting and generation timing key off the assistant role or they
    overcount; text deltas arrive as message_update.assistantMessageEvent with
    a string `delta`; usage arrives on message_end.message.usage with camelCase
    cacheRead; tool events carry toolName.
    """
    m = AgentMetrics()
    m.wall_seconds = end_wall - start_wall

    gen_seconds = 0.0
    in_gen = False
    gen_start_s = 0.0
    turn_start_s: float | None = None
    tool_starts: dict[str, str] = {}
    tool_name_by_call: dict[str, str] = {}
    deltas: list[str] = []
    seq = 0
    log = log_fn or (lambda msg: None)

    total = len(events)
    for event in events:
        etype = event.get("type")
        ts_s = _arrival_s(event, seq, total, m.wall_seconds)
        summary = _summarize_event(event)
        log(f"[seq {seq}] {etype} {summary}".rstrip())
        seq += 1

        if etype == "turn_start":
            m.turns += 1
            turn_start_s = ts_s
        elif etype == "message_start":
            message = event.get("message") or {}
            if message.get("role") != "assistant":
                continue  # pi echoes the prompt as a user-role message pair
            m.requests += 1
            if turn_start_s is not None:
                m.ttfts_ms.append((ts_s - turn_start_s) * 1000.0)
            in_gen = True
            gen_start_s = ts_s
        elif etype == "message_update":
            ame = event.get("assistantMessageEvent") or {}
            if ame.get("type") == "text_delta":
                delta = ame.get("delta", "")
                if isinstance(delta, str):
                    deltas.append(delta)
        elif etype == "message_end":
            message = event.get("message") or {}
            if message.get("role") != "assistant":
                continue  # the user echo must not end generation or add text
            usage = message.get("usage") or {}
            m.input_tok += usage.get("input", 0)
            m.output_tok += usage.get("output", 0)
            m.cache_read_tok += usage.get("cacheRead", 0)
            m.cache_write_tok += usage.get("cacheWrite", 0)
            m.reasoning_tok += usage.get("reasoning", 0)
            if in_gen:
                gen_seconds += ts_s - gen_start_s
                in_gen = False
        elif etype == "tool_execution_start":
            tool_starts[event.get("toolCallId", "")] = event.get("toolName", "")
        elif etype == "tool_execution_end":
            call_id = event.get("toolCallId", "")
            tool_starts.pop(call_id, None)
            name = event.get("toolName") or tool_name_by_call.get(call_id, "")
            tool_name_by_call[call_id] = name

    m.gen_seconds = gen_seconds

    # A tool call is anything the agent issued, including one interrupted by a
    # timeout with no matching end event, so count starts and pair separately.
    m.tool_call_count = len(tool_name_by_call) + len(tool_starts)

    # final_text: incremental deltas concatenated, falling back to the last
    # assistant message_end's text blocks when a provider reports nothing
    # incrementally. Without the fallback a row could pass the reply
    # requirement in gate 6 while metrics recorded no closing message at all.
    m.final_text = "".join(deltas)
    if not m.final_text:
        for event in reversed(list(events)):
            if event.get("type") == "message_end" and (event.get("message") or {}).get("role") == "assistant":
                m.final_text = _extract_text(event)
                break

    if m.ttfts_ms:
        m.ttft_first_ms = m.ttfts_ms[0]
        m.ttft_subseq_median_ms = median(m.ttfts_ms[1:]) if len(m.ttfts_ms) > 1 else m.ttft_first_ms
        # A median over requests 2..n rather than a mean over all of them: the
        # mean includes the first request, so a slow cold start raises its own
        # comparison value and the flag can never fire below 4 requests. A
        # single-request run compares against itself and must not flag.
        if len(m.ttfts_ms) >= 2 and m.ttft_first_ms >= 3 * m.ttft_subseq_median_ms:
            m.cold_first_token = True

    # CACHE_ANOMALY — same relative check as compute_total_tokens() in
    # modelman/benchmark/runner.py, applied per-row.
    total_in = m.input_tok + m.cache_read_tok + m.cache_write_tok
    if total_in > 0:
        cache_ratio = (m.cache_read_tok + m.cache_write_tok) / total_in
        if m.input_tok > 0 and m.cache_read_tok > 10 * m.input_tok and cache_ratio > 0.9:
            m.anomaly = "CACHE_ANOMALY"

    # COLD_FIRST_TOKEN rides in the same field, joined rather than overwriting:
    # the spec lists both as flags and a row can genuinely have both problems.
    if m.cold_first_token:
        m.anomaly = f"{m.anomaly}+COLD_FIRST_TOKEN" if m.anomaly else "COLD_FIRST_TOKEN"

    # REPEATED_FAILURE — 4+ tool calls of one name, per the spec's "same tool,
    # same error 4 times". Counting starts (not ends) because a call interrupted
    # by a timeout never gets an end event.
    if m.tool_call_count:
        calls_per_name = Counter(tool_name_by_call.values())
        calls_per_name.update(tool_starts.values())
        for name, count in calls_per_name.items():
            if count >= 4:
                m.anomaly = f"{m.anomaly}+REPEATED_FAILURE({name})" if m.anomaly else f"REPEATED_FAILURE({name})"
                break

    # thinking=off but the backend still emitted reasoning tokens: a flag and a
    # log line, never a gate (comparing off vs high is the subject of the
    # measurement, so gating on it would gate on the thing being measured).
    if thinking == "off" and m.reasoning_tok > 0:
        m.thinking_off_reasoning = True
        log(
            f"THINKING_OFF_REASONING: --thinking off but {m.reasoning_tok} reasoning "
            "tokens emitted; the backend ignores the toggle and this row's thinking "
            "level is not what the label says"
        )

    return m
```

Also add `from statistics import median` to `pidriver.py`'s top-level import block, and `import pytest` to `test_pidriver.py`'s — **merge both into the existing top-of-file import blocks rather than appending them mid-file**: `make check` runs `ruff check src/ tests/` with `E` selected, so a mid-file import is an `E402` failure in CI.

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_pidriver.py -v`
Expected: PASS (30 tests — 8 + 5 + 17)

- [ ] **Step 5: Run the full pidriver+workspace+task test slice and commit**

Run: `uv run pytest tests/benchmark/agent/ -v`
Expected: PASS (42 tests from Tasks 1–7)

```bash
git add modelman/src/modelman/benchmark/agent/pidriver.py \
        modelman/tests/benchmark/agent/test_pidriver.py
git commit -m "feat(agent-bench): compute speed metrics from pi event stream - completes plan item #7"
```

---
