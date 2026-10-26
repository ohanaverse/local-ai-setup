"""A fake OpenAI-compatible endpoint for the thinking-probe tests.

A stdlib ThreadingHTTPServer on a free 127.0.0.1 port, one per test. The
request's `model` field picks the reply, so one server can play every engine
behavior the probe has to survive. Every request is recorded (path, headers,
JSON body) for the assertions on what the probe sends.
"""

from __future__ import annotations

import json
import socket
import threading
from collections.abc import Iterator
from contextlib import contextmanager
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

import pytest

# How long the "slow" model holds its reply. Far beyond the timeouts the
# tests pass (well under a second), and cut short at teardown.
SLOW_REPLY_S = 5.0

ANSWER = "# Plan\n\n- one\n- two\n\n| a | b |\n|---|---|\n"


def _completion(message: dict[str, Any], usage: dict[str, Any] | None = None) -> dict[str, Any]:
    body: dict[str, Any] = {
        "id": "chatcmpl-fake",
        "object": "chat.completion",
        "choices": [{"index": 0, "message": message, "finish_reason": "stop"}],
    }
    if usage is not None:
        body["usage"] = usage
    return body


# Wherever this marker appears in a reply, the fake server puts the value of
# the request's Authorization header (empty when none was sent): a server that
# echoes the request back, which the probe must never let into a report.
ECHO_AUTH = "@@AUTHORIZATION@@"

# model id -> (status, body). A str body is sent as-is (the non-JSON case).
REPLIES: dict[str, tuple[int, Any]] = {
    "reasoning-content": (
        200,
        _completion(
            {"role": "assistant", "content": ANSWER, "reasoning_content": "We need answer."},
            {"prompt_tokens": 10, "completion_tokens": 42, "total_tokens": 52},
        ),
    ),
    "reasoning-field": (
        200,
        _completion(
            {"role": "assistant", "content": "391", "reasoning": "17*23 = 391, so 391."},
            {"completion_tokens": 9},
        ),
    ),
    "think-tags": (
        200,
        _completion({"role": "assistant", "content": "<think>\ncarry the 2\n</think>\n\n391"}),
    ),
    "think-close-only": (
        200,
        _completion({"role": "assistant", "content": "carry the 2</think>391"}),
    ),
    "think-unclosed": (
        200,
        _completion({"role": "assistant", "content": "<think>still thinking"}),
    ),
    "no-reasoning": (200, _completion({"role": "assistant", "content": "391"})),
    "reasoning-tokens": (
        200,
        _completion(
            {"role": "assistant", "content": "391", "reasoning_content": "think think"},
            {"completion_tokens": 30, "completion_tokens_details": {"reasoning_tokens": 24}},
        ),
    ),
    "server-error": (500, {"error": {"message": "boom"}}),
    "non-json": (200, "<html>not json</html>"),
    "no-choices": (200, {"object": "chat.completion", "choices": []}),
    "echo-auth-500": (500, {"error": {"message": f"rejected header {ECHO_AUTH}"}}),
    "echo-auth-non-json": (200, f"<html>you sent {ECHO_AUTH}</html>"),
    "echo-auth-completion": (
        200,
        _completion(
            {
                "role": "assistant",
                "content": f"The header was {ECHO_AUTH}.",
                "reasoning_content": f"They sent {ECHO_AUTH}; repeat it.",
            }
        ),
    ),
    "multiline-500": (500, "Traceback (most recent call last):\n  File x\n\n# Boom\n- detail"),
    # Some OpenAI-compatible servers report an error with status 200.
    "error-with-200": (200, {"error": {"message": "model not loaded", "code": 503}}),
    "echo-auth-error-with-200": (200, {"error": {"message": f"bad header {ECHO_AUTH}"}}),
}


# The sampling penalties a strict server may refuse. The "rejects-penalties"
# model answers 400 with an OpenAI-style message when the request carries any
# of them, and answers like "no-reasoning" when it carries none.
PENALTY_FIELDS = ("presence_penalty", "frequency_penalty", "repetition_penalty")
REJECTED_FIELD_MESSAGE = "Unrecognized request argument supplied: {field}"


# The models the fake lists on GET /v1/models (the lifecycle probe tests).
LISTED_MODELS = ("flint",)


@dataclass
class FakeServer:
    base_url: str
    requests: list[dict[str, Any]] = field(default_factory=list)
    # When set, every route answers 401 unless the request carries
    # `Authorization: Bearer <required_key>` (rec's rule: health included).
    required_key: str | None = None


def _handler(fake: FakeServer, release: threading.Event) -> type[BaseHTTPRequestHandler]:
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args: Any) -> None:  # keep pytest output clean
            pass

        def _refused(self) -> bool:
            """Answer 401 when a key is required and not carried."""
            if fake.required_key is None:
                return False
            if self.headers.get("Authorization") == f"Bearer {fake.required_key}":
                return False
            self._send(401, json.dumps({"error": {"message": "API key required"}}))
            return True

        def _send(self, status: int, payload: str) -> None:
            data = payload.encode()
            try:
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)
            except OSError:  # the client gave up (the timeout case)
                pass

        def do_GET(self) -> None:  # noqa: N802 (stdlib name)
            fake.requests.append({"path": self.path, "headers": dict(self.headers), "body": None})
            if self._refused():
                return
            if self.path.endswith("/models"):
                listed = [{"id": m, "object": "model"} for m in LISTED_MODELS]
                self._send(200, json.dumps({"object": "list", "data": listed}))
            else:
                self._send(200, json.dumps({"status": "ok"}))

        def do_POST(self) -> None:  # noqa: N802 (stdlib name)
            length = int(self.headers.get("Content-Length", "0"))
            raw = self.rfile.read(length)
            body = json.loads(raw)
            fake.requests.append({"path": self.path, "headers": dict(self.headers), "body": body})
            if self._refused():
                return
            model = body.get("model", "")
            if model == "slow":
                release.wait(SLOW_REPLY_S)
                status, reply = REPLIES["no-reasoning"]
            elif model == "rejects-penalties":
                sent = [f for f in PENALTY_FIELDS if f in body]
                if sent:
                    message = REJECTED_FIELD_MESSAGE.format(field=sent[0])
                    status, reply = 400, {"error": {"message": message}}
                else:
                    status, reply = REPLIES["no-reasoning"]
            else:
                status, reply = REPLIES.get(model, (404, {"error": f"unknown model {model}"}))
            payload = reply if isinstance(reply, str) else json.dumps(reply)
            payload = payload.replace(ECHO_AUTH, self.headers.get("Authorization", ""))
            self._send(status, payload)

    return Handler


@contextmanager
def serve_fake() -> Iterator[FakeServer]:
    """The fake endpoint as a context manager, for test packages outside
    tests/probe/ that cannot see this conftest's fixtures."""
    release = threading.Event()
    fake = FakeServer(base_url="")
    server = ThreadingHTTPServer(("127.0.0.1", 0), _handler(fake, release))
    server.daemon_threads = True
    fake.base_url = f"http://127.0.0.1:{server.server_address[1]}/v1"
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield fake
    finally:
        release.set()
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


@pytest.fixture
def fake_openai() -> Iterator[FakeServer]:
    with serve_fake() as fake:
        yield fake


@pytest.fixture
def closed_port_url() -> str:
    """A localhost URL nothing listens on: bind a free port, then close it."""
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    return f"http://127.0.0.1:{port}/v1"


@pytest.fixture(autouse=True)
def _plain_typer_output(monkeypatch: pytest.MonkeyPatch) -> None:
    """CI sets GITHUB_ACTIONS, which makes Typer force ANSI styling into
    CliRunner output and split `--flag` names across escape codes."""
    monkeypatch.setattr("typer.rich_utils.FORCE_TERMINAL", False)
