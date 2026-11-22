import json, urllib.request
P="http://127.0.0.1:4099"; M="sk-spike-master"; MOCK="http://127.0.0.1:9100"
def call(path, body, hdr=None, base=P, auth=True):
    h={"Content-Type":"application/json"}
    if auth: h["Authorization"]="Bearer "+M
    h.update(hdr or {})
    r=urllib.request.Request(base+path, json.dumps(body).encode(), h)
    try:
        with urllib.request.urlopen(r, timeout=60) as f: return f.status, json.loads(f.read())
    except urllib.error.HTTPError as e: return e.code, e.read().decode()[:400]
def mock(path, body=None):
    r=urllib.request.Request(MOCK+path, json.dumps(body).encode() if body is not None else None, {"Content-Type":"application/json"})
    return json.loads(urllib.request.urlopen(r).read())
def txt(o):
    if isinstance(o,str): return o[:160]
    if "choices" in o: return o["choices"][0]["message"]["content"]
    if "content" in o and isinstance(o["content"],list): return o["content"][0].get("text")
    return json.dumps(o)[:160]
def msg(model, hdr=None, **kw):
    return call("/v1/messages",{"model":model,"max_tokens":50,"messages":[{"role":"user","content":"hi"}],**kw},hdr)
def last(n=1): return mock("/ctl/log")[-n:]
def clear(): mock("/ctl/x/clear",{})
