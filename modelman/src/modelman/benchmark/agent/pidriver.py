"""pi agent driver: route resolution, models.json generation, process
execution, and metrics extraction from pi's `--mode json` event stream."""

from __future__ import annotations

import contextlib
import json
import os
import signal
import subprocess
import threading
import time
from collections import Counter
from collections.abc import Callable, Sequence
from dataclasses import dataclass, field
from pathlib import Path
from statistics import median

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
