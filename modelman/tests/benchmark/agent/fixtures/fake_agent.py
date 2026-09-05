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
import os
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
        # PI_CODING_AGENT_DIR lands in the session file so a test can confirm
        # the driver actually forwarded env to the child.
        (session_dir / f"{session_id}.jsonl").write_text(
            json.dumps(
                {
                    "type": "session",
                    "id": session_id,
                    "agent_dir": os.environ.get("PI_CODING_AGENT_DIR", ""),
                }
            )
            + "\n",
            encoding="utf-8",
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
