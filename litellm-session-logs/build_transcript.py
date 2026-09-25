"""
Build an assistant-only chat transcript from a LiteLLM_SpendLogs JSON export
(see 01_export_session_logs.sql). Reads response/model/tool-call data only —
the `messages` column in that export is always `{}` for standard chat
completions, so the user/tool-result side is not reconstructable from it.
For a two-sided transcript, see build_full_transcript.py instead.

Usage: python3 build_transcript.py <session_logs_<id>.json>
"""
import json
import sys
from datetime import datetime, timedelta
from pathlib import Path

if len(sys.argv) < 2:
    print(__doc__)
    sys.exit(1)
SRC = Path(sys.argv[1])
OUT = SRC.with_name(SRC.stem + "-transcript.md")

with open(SRC) as f:
    data = json.load(f)

# already ordered by startTime per the original query, but sort defensively
data.sort(key=lambda r: r["startTime"])

def fmt_ts(ts):
    # ts like 2026-09-18T00:45:47.48  (UTC per the capture notes)
    dt = datetime.fromisoformat(ts)
    et = dt - timedelta(hours=4)
    return f"{dt.strftime('%Y-%m-%d %H:%M:%S')} UTC ({et.strftime('%H:%M:%S')} ET)"

def fmt_args(args_str):
    try:
        parsed = json.loads(args_str)
        return json.dumps(parsed, indent=2, ensure_ascii=False)
    except Exception:
        return args_str

lines = []
lines.append(f"# Reconstructed Chat Transcript — {SRC.stem}\n")
lines.append(
    "> **Data limitation:** the `messages` column (the request body — user turns, tool-result "
    "content, system prompt) is **empty (`{}`)** on every row in this capture. "
    "Only the `response` column is populated. That means this transcript can only reconstruct "
    "the **assistant/model side** of the conversation — text content, reasoning, and tool calls "
    "the model made — turn by turn, in order. User prompts and tool-result outputs fed back to "
    "the model are not present in this DB dump and cannot be recovered from it. If you have DB "
    "access, check whether `proxy_server_request` is populated for this session instead (see "
    "build_full_transcript.py) — it holds the real request body when "
    "`store_prompts_in_spend_logs` was active at capture time.\n"
)
lines.append(
    "> **Failure rows** (`status: failure`) also have an empty `response` (`{}`) and "
    "`request_duration_ms: 0` — the DB capture does not include an error message or exception "
    "type for these. They're marked below as `[ERROR]` with what's known (timestamp, request id, "
    "spend/token counts if any); the specific cause (timeout vs. budget-exceeded vs. other) is "
    "**not recoverable from this data** and is inferred only where the surrounding context makes "
    "it obvious.\n"
)
lines.append(
    "> **Truncation:** LiteLLM's own DB-storage safeguard (`MAX_STRING_LENGTH_PROMPT_IN_DB`) "
    "truncates long `content`/`reasoning_content` strings before they're written to the DB. "
    "Where it hit, you'll see an inline marker like `(litellm_truncated skipped N chars ...)` "
    "inside the text below, left as-is from the source data.\n"
)
lines.append("---\n")

last_model = None
turn_no = 0
error_count = 0
model_switch_count = 0

for r in data:
    ts = r["startTime"]
    status = r["status"]
    resp = r.get("response") or {}

    if status == "failure":
        error_count += 1
        lines.append(f"\n### ⚠️ [ERROR] {fmt_ts(ts)}\n")
        lines.append(f"- request_id: `{r['request_id']}`")
        lines.append(f"- duration: {r['request_duration_ms']}ms, prompt_tokens: {r['prompt_tokens']}, "
                      f"completion_tokens: {r['completion_tokens']}, spend: ${r['spend']}")
        note = "request failed before/without a usable response; no error text captured in the DB log"
        if r["prompt_tokens"] == 0 and r["completion_tokens"] == 0 and r["spend"] == 0:
            note += " — zero tokens/spend suggests it was rejected before reaching the provider (consistent with a proxy-side budget/limit rejection)"
        elif r["completion_tokens"] > 0 or r["spend"] > 0:
            note += " — nonzero spend/tokens suggests the provider was reached before the failure (consistent with a mid-stream timeout/cutoff)"
        lines.append(f"- likely cause (inferred, not logged): {note}\n")
        continue

    model = resp.get("model")
    if model and model != last_model:
        model_switch_count += 1
        if last_model is not None:
            lines.append(f"\n### 🔄 [MODEL CHANGE] {fmt_ts(ts)}: `{last_model}` → `{model}`\n")
        else:
            lines.append(f"\n### ▶️ [SESSION START — model: `{model}`] {fmt_ts(ts)}\n")
        last_model = model

    turn_no += 1
    choice = (resp.get("choices") or [{}])[0]
    msg = choice.get("message") or {}
    content = msg.get("content")
    reasoning = msg.get("reasoning_content")
    tool_calls = msg.get("tool_calls") or []
    finish_reason = choice.get("finish_reason")

    lines.append(f"\n#### Turn {turn_no} — {fmt_ts(ts)} — `{model}`")
    lines.append(f"*({r['request_duration_ms']}ms, {r['prompt_tokens']} prompt / {r['completion_tokens']} completion tokens, ${r['spend']:.6f}, finish_reason={finish_reason})*\n")

    if reasoning:
        lines.append(f"**[reasoning]**\n> {reasoning}\n")

    if content:
        lines.append(f"**Assistant:**\n{content}\n")

    for tc in tool_calls:
        fn = tc.get("function", {})
        name = fn.get("name")
        args = fmt_args(fn.get("arguments", ""))
        lines.append(f"**[tool call: `{name}`]**")
        lines.append(f"```json\n{args}\n```\n")

with open(OUT, "w") as f:
    f.write("\n".join(lines))

print("wrote", OUT)
print("turns:", turn_no, "errors:", error_count, "model switches:", model_switch_count)
