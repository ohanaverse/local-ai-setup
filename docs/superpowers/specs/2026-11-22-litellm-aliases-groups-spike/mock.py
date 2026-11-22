#!/usr/bin/env python3
"""Mock model backends for the LiteLLM alias spike: one server, one path
prefix per backend (/b/<name>/...), speaking OpenAI chat and Anthropic
messages, with a per-backend failure mode and a request log that records the
credential headers each backend received. Fake tokens only."""
import json, sys, threading, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

MODE = {}          # backend -> ok | 429 | 500 | slow
LOG = []           # request records
LOCK = threading.Lock()
KEEP = ("authorization", "x-api-key", "anthropic-beta", "anthropic-version", "x-litellm-api-key", "user-agent", "x-test")

class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def log_message(self, *a): pass

    def send_json(self, code, obj):
        b = json.dumps(obj).encode()
        self.send_response(code); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)

    def do_GET(self):
        if self.path == "/ctl/log":
            with LOCK: return self.send_json(200, LOG)
        self.send_json(404, {"error": "no"})

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        body = json.loads(self.rfile.read(n) or b"{}")
        if self.path.startswith("/ctl/"):
            _, _, name, what = self.path.split("/", 3)
            if what == "mode": MODE[name] = body["mode"]
            if what == "clear":
                with LOCK: LOG.clear()
            return self.send_json(200, {"ok": True})
        parts = self.path.split("/")      # ['', 'b', name, 'v1', 'chat'|'messages', ...]
        if len(parts) < 5 or parts[1] != "b":
            return self.send_json(404, {"error": "bad path " + self.path})
        name, kind = parts[2], parts[-1]
        if kind == "responses":
            return self.resp_wire(name, body)
        def mask(k, v):
            # credentials are never kept whole: first 12 characters and the length
            return (v[:12] + "...(len %d)" % len(v)) if k in ("authorization", "x-api-key", "x-litellm-api-key") and len(v) > 12 else v
        rec = {"backend": name, "path": self.path, "model": body.get("model"), "stream": bool(body.get("stream")),
               "hdr": {k: mask(k, v) for k, v in ((k.lower(), v) for k, v in self.headers.items()) if k in KEEP},
               "keys": sorted(body.keys())}
        with LOCK: LOG.append(rec)
        mode = MODE.get(name, "ok")
        anth = kind == "messages"
        if mode in ("429", "500"):
            code = int(mode)
            err = ({"type": "error", "error": {"type": "rate_limit_error" if code == 429 else "api_error", "message": f"[{name}] {mode}"}}
                   if anth else {"error": {"message": f"[{name}] {mode}", "type": "rate_limit_error" if code == 429 else "server_error", "code": str(code)}})
            return self.send_json(code, err)
        words = [f"[{name}]"] + (["tick%d" % i for i in range(10)] if mode == "slow" else ["ok"])
        delay = 0.5 if mode == "slow" else 0
        if not body.get("stream"):
            text = " ".join(words)
            if anth:
                return self.send_json(200, {"id": "msg_1", "type": "message", "role": "assistant", "model": body.get("model"),
                    "content": [{"type": "text", "text": text}], "stop_reason": "end_turn", "stop_sequence": None,
                    "usage": {"input_tokens": 5, "output_tokens": 3}})
            return self.send_json(200, {"id": "c1", "object": "chat.completion", "created": 0, "model": body.get("model"),
                "choices": [{"index": 0, "message": {"role": "assistant", "content": text}, "finish_reason": "stop"}],
                "usage": {"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}})
        self.send_response(200); self.send_header("Content-Type", "text/event-stream"); self.send_header("Transfer-Encoding", "chunked"); self.end_headers()
        def chunk(s):
            b = s.encode(); self.wfile.write(b"%x\r\n" % len(b) + b + b"\r\n"); self.wfile.flush()
        if anth:
            ev = lambda t, d: chunk(f"event: {t}\ndata: {json.dumps(d)}\n\n")
            ev("message_start", {"type": "message_start", "message": {"id": "msg_1", "type": "message", "role": "assistant", "model": body.get("model"), "content": [], "stop_reason": None, "usage": {"input_tokens": 5, "output_tokens": 1}}})
            ev("content_block_start", {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}})
            for w in words:
                ev("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": w + " "}}); time.sleep(delay)
            ev("content_block_stop", {"type": "content_block_stop", "index": 0})
            ev("message_delta", {"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence": None}, "usage": {"output_tokens": len(words)}})
            ev("message_stop", {"type": "message_stop"})
        else:
            for i, w in enumerate(words):
                chunk("data: " + json.dumps({"id": "c1", "object": "chat.completion.chunk", "created": 0, "model": body.get("model"),
                      "choices": [{"index": 0, "delta": ({"role": "assistant"} if i == 0 else {}) | {"content": w + " "}, "finish_reason": None}]}) + "\n\n"); time.sleep(delay)
            chunk("data: " + json.dumps({"id": "c1", "object": "chat.completion.chunk", "created": 0, "model": body.get("model"),
                  "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]}) + "\n\n")
            chunk("data: [DONE]\n\n")
        self.wfile.write(b"0\r\n\r\n"); self.wfile.flush()

def _responses(self, name, body):
    """Minimal OpenAI Responses wire (what Codex speaks): non-stream object or SSE."""
    rec = {"backend": name, "path": self.path, "model": body.get("model"), "stream": bool(body.get("stream")),
           "hdr": {k: v for k, v in ((k.lower(), v) for k, v in self.headers.items()) if k in KEEP and k != "authorization"}, "keys": sorted(body.keys())}
    with LOCK: LOG.append(rec)
    mode = MODE.get(name, "ok")
    if mode in ("429", "500"):
        return self.send_json(int(mode), {"error": {"message": f"[{name}] {mode}", "type": "rate_limit_error" if mode == "429" else "server_error", "code": mode}})
    text = f"[{name}] ok"
    item = {"id": "msg_1", "type": "message", "status": "completed", "role": "assistant", "content": [{"type": "output_text", "text": text, "annotations": []}]}
    resp = {"id": "resp_1", "object": "response", "created_at": 0, "status": "completed", "model": body.get("model"), "output": [item],
            "usage": {"input_tokens": 5, "output_tokens": 3, "total_tokens": 8}, "parallel_tool_calls": True, "tool_choice": "auto", "tools": []}
    if not body.get("stream"):
        return self.send_json(200, resp)
    self.send_response(200); self.send_header("Content-Type", "text/event-stream"); self.send_header("Transfer-Encoding", "chunked"); self.end_headers()
    def chunk(s):
        b = s.encode(); self.wfile.write(b"%x\r\n" % len(b) + b + b"\r\n"); self.wfile.flush()
    def ev(t, d): chunk(f"event: {t}\ndata: {json.dumps({'type': t, **d})}\n\n")
    inprog = {**resp, "status": "in_progress", "output": []}
    ev("response.created", {"response": inprog}); ev("response.in_progress", {"response": inprog})
    ev("response.output_item.added", {"output_index": 0, "item": {**item, "status": "in_progress", "content": []}})
    ev("response.content_part.added", {"item_id": "msg_1", "output_index": 0, "content_index": 0, "part": {"type": "output_text", "text": "", "annotations": []}})
    ev("response.output_text.delta", {"item_id": "msg_1", "output_index": 0, "content_index": 0, "delta": text})
    ev("response.output_text.done", {"item_id": "msg_1", "output_index": 0, "content_index": 0, "text": text})
    ev("response.content_part.done", {"item_id": "msg_1", "output_index": 0, "content_index": 0, "part": item["content"][0]})
    ev("response.output_item.done", {"output_index": 0, "item": item})
    ev("response.completed", {"response": resp})
    self.wfile.write(b"0\r\n\r\n"); self.wfile.flush()
H.resp_wire = _responses

if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1]) if len(sys.argv) > 1 else 9100), H).serve_forever()
