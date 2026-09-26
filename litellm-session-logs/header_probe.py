#!/usr/bin/env python3
"""Minimal HTTP listener that logs every request's headers/path/body,
then answers with a JSON shape permissive enough to satisfy an OpenAI
chat-completions OR legacy completions client, so the caller (Copilot CLI)
completes its turn instead of erroring out.

Used to inspect exactly what headers Copilot CLI sends when pointed at a
custom COPILOT_PROVIDER_BASE_URL, since the installed binary is a
compressed Node SEA blob that a plain `strings` grep can't be trusted
against (see litellm-session-logs/session-log-sources.md, Copilot section).
"""
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOG_PATH = "/tmp/copilot_headers.log"
PORT = 4199


class Handler(BaseHTTPRequestHandler):
    def _handle(self):
        length = int(self.headers.get("Content-Length", 0) or 0)
        body = self.rfile.read(length) if length else b""
        entry = {
            "method": self.command,
            "path": self.path,
            "headers": dict(self.headers.items()),
            "body": body.decode("utf-8", errors="replace")[:4000],
        }
        with open(LOG_PATH, "a") as f:
            f.write(json.dumps(entry) + "\n")
        print(f"[probe] {self.command} {self.path}", file=sys.stderr)
        for k, v in self.headers.items():
            print(f"    {k}: {v}", file=sys.stderr)

        resp = {
            "id": "probe-1",
            "object": "chat.completion",
            "created": 0,
            "model": "probe-model",
            "choices": [
                {
                    "index": 0,
                    "text": "ok",
                    "message": {"role": "assistant", "content": "ok"},
                    "finish_reason": "stop",
                }
            ],
            "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
        }
        payload = json.dumps(resp).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_POST(self):
        self._handle()

    def do_GET(self):
        self._handle()

    def log_message(self, fmt, *args):
        pass


if __name__ == "__main__":
    open(LOG_PATH, "w").close()
    print(f"[probe] listening on :{PORT}, logging to {LOG_PATH}", file=sys.stderr)
    ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
