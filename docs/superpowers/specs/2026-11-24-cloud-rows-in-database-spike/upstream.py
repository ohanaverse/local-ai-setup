# Stand-in OpenAI-compatible upstream for the #694 spike. It records only the port, the path and whether the
# Authorization header equalled the sentinel (a boolean and a kind label); it never writes a key to its log.
import json, sys, time, http.server

PORT, SENT, LOG = int(sys.argv[1]), sys.argv[2], sys.argv[3]


class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length") or 0))
        auth = self.headers.get("Authorization") or ""
        kind = ("sentinel" if auth == "Bearer " + SENT else
                "os.environ-ref-literal" if auth.startswith("Bearer os.environ/") else
                "empty" if auth in ("", "Bearer ") else "other")
        with open(LOG, "a") as f:
            f.write(json.dumps({"t": round(time.time(), 3), "port": PORT, "path": self.path,
                                "auth_is_sentinel": auth == "Bearer " + SENT, "auth_kind": kind}) + "\n")
        body = json.dumps({"id": "x", "object": "chat.completion", "created": 0, "model": "stand-in",
                           "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok from %d" % PORT},
                                        "finish_reason": "stop"}],
                           "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


http.server.ThreadingHTTPServer(("127.0.0.1", PORT), H).serve_forever()
