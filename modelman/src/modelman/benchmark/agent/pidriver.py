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
    proc: subprocess.Popen[bytes], lines: list[bytes], stop: threading.Event
) -> None:
    """Reader thread: drain stdout into `lines`, exiting promptly on `stop`
    instead of blocking forever on readline() if the child wedged.

    `proc` is a parameter, not a free variable: this is a module-level function
    and would otherwise NameError the first time a real row ran it.
    """
    assert proc.stdout is not None
    for raw_line in proc.stdout:
        if stop.is_set():
            return
        if not raw_line.strip():
            continue
        lines.append(raw_line)


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
    stdout_lines: list[bytes] = []
    stderr_chunks: list[bytes] = []
    events: list[dict] = []
    seen_message_end = False
    unparsed_lines = 0
    exit_code: int | None = None
    timed_out = False
    aborted = False

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
        for raw_line in stdout_lines[consumed:]:
            try:
                event = json.loads(raw_line)
            except json.JSONDecodeError:
                unparsed_lines += 1
                continue
            if not isinstance(event, dict):
                unparsed_lines += 1
                continue
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
