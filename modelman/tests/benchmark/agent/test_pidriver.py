"""Tests for modelman.benchmark.agent.pidriver — route resolution and pi
config generation.

Route resolution is the part of the design most likely to silently
misaddress a model (spec: "a registry id sent under the wrong provider
could never be addressed and its bare form would be sent upstream, a 400
at the gateway") — these tests pin the litellm-vs-direct addressing rules
exactly as the spec's Route resolution section states them.
"""

import json
import sys
import time
from pathlib import Path

import pytest

from modelman.benchmark.agent.pidriver import (
    DirectRouteConfig,
    PiTarget,
    RowConfig,
    build_models_json,
    build_pi_command,
    compute_metrics,
    resolve_pi_target,
    run_pi_process,
    write_pi_config,
)
from modelman.benchmark.agent.workspace import create_workspace, destroy_workspace
from modelman.benchmark.errors import BenchmarkError

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
