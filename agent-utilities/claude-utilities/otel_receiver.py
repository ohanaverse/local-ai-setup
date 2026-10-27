#!/usr/bin/env python3
"""Minimal local OTLP/HTTP receiver: appends Claude Code trace exports to a JSONL file.

Claude Code must export OTLP as JSON (http/protobuf and grpc are rejected with 415, so a
misconfigured protocol shows up as an error instead of an empty file):

  export CLAUDE_CODE_ENABLE_TELEMETRY=1
  export CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1
  export OTEL_TRACES_EXPORTER=otlp
  export OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/json
  export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:4318/v1/traces

One line is written per export request. Metrics and logs posted to other paths are
acknowledged and discarded. Read the file with otel_speed.py.

Usage: otel_receiver.py [--port 4318] [--out ~/.cache/claude-utilities/otel-traces.jsonl]
"""

import argparse
import contextlib
import gzip
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

DEFAULT_OUT = Path("~/.cache/claude-utilities/otel-traces.jsonl").expanduser()
TRACES_PATH = "/v1/traces"


def make_handler(out_path: Path):
    lock = threading.Lock()

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            body = self.rfile.read(int(self.headers.get("Content-Length") or 0))
            if self.headers.get("Content-Encoding") == "gzip":
                body = gzip.decompress(body)
            if self.path != TRACES_PATH:
                return self._reply(200)
            try:
                payload = json.loads(body)
            except ValueError:
                print(
                    "rejected non-JSON export; set OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/json",
                    file=sys.stderr,
                )
                return self._reply(415)
            with lock, out_path.open("a", encoding="utf-8") as fh:
                fh.write(json.dumps(payload, separators=(",", ":")) + "\n")
            self._reply(200)

        def _reply(self, status):
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", "2")
            self.end_headers()
            self.wfile.write(b"{}")

        def log_message(self, *args):
            pass

    return Handler


def make_server(port: int, out_path: Path) -> ThreadingHTTPServer:
    out_path.parent.mkdir(parents=True, exist_ok=True)
    # Loopback only: telemetry can contain private session details.
    return ThreadingHTTPServer(("127.0.0.1", port), make_handler(out_path))


def main():
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument("--port", type=int, default=4318)
    ap.add_argument("--out", type=Path, default=DEFAULT_OUT)
    args = ap.parse_args()
    out = args.out.expanduser()
    server = make_server(args.port, out)
    print(f"listening on http://127.0.0.1:{args.port}{TRACES_PATH}, appending to {out}")
    with contextlib.suppress(KeyboardInterrupt):
        server.serve_forever()


if __name__ == "__main__":
    main()
