"""
Build a full, two-sided chat transcript from a LiteLLM session, using the
live DB's `proxy_server_request` column (real request bodies) joined against
a JSON export's `response` column (see 01_export_session_logs.sql and
02_export_proxy_server_request.sh).

Handles sessions where session_id is shared across multiple interleaved
agent threads (e.g. a main conversation plus a separate per-action judge
making one-shot calls): each turn's true parent is found by matching
tool-call IDs (stable across the Anthropic-shaped request / OpenAI-shaped
response translation litellm does) or exact response text, rather than
assuming strict chronological continuation. Threads with >= MAIN_THREAD_MIN_SIZE
turns are rendered as full linear conversations; shorter ones are grouped
into one "Isolated / short calls" section.

Usage: python3 build_full_transcript.py <session_logs.json> <proxy_requests.ndjson> [output.md]
"""
import json
import sys
from datetime import datetime, timedelta
from pathlib import Path

if len(sys.argv) < 3:
    print(__doc__)
    sys.exit(1)

MAIN_JSON = Path(sys.argv[1])
PSR_NDJSON = Path(sys.argv[2])
OUT = Path(sys.argv[3]) if len(sys.argv) > 3 else MAIN_JSON.with_name(MAIN_JSON.stem + "-transcript-full.md")
SESSION_LABEL = MAIN_JSON.stem.replace("session_logs_", "")

MAIN_THREAD_MIN_SIZE = 5

with open(MAIN_JSON) as f:
    main_rows = {r["request_id"]: r for r in json.load(f)}

psr_by_id = {}
with open(PSR_NDJSON) as f:
    for line in f:
        d = json.loads(line)
        psr_by_id[d["request_id"]] = d.get("proxy_server_request")

ordered = sorted(main_rows.values(), key=lambda r: r["startTime"])


def fmt_ts(ts):
    dt = datetime.fromisoformat(ts)
    et = dt - timedelta(hours=4)
    return f"{dt.strftime('%Y-%m-%d %H:%M:%S')} UTC ({et.strftime('%H:%M:%S')} ET)"


def fmt_args(args_str):
    try:
        parsed = json.loads(args_str)
        return json.dumps(parsed, indent=2, ensure_ascii=False)
    except Exception:
        return args_str


def find_anchor(cur_messages, registry):
    """
    Search cur_messages (newest-first) for an assistant message whose
    tool_use ids or text content match a previously-seen turn's response
    signature. Returns (anchor_index, thread_id) or (None, None) if this
    turn starts a new thread (no continuation found).
    """
    for i in range(len(cur_messages) - 1, -1, -1):
        m = cur_messages[i]
        if m.get("role") != "assistant":
            continue
        content = m.get("content")
        if not isinstance(content, list):
            continue
        ids_here = {b.get("id") for b in content if isinstance(b, dict) and b.get("type") == "tool_use"}
        if ids_here:
            for kind, val, tid in registry:
                if kind == "tool" and val & ids_here:
                    return i, tid
        texts_here = {b.get("text") for b in content if isinstance(b, dict) and b.get("type") == "text"}
        if texts_here:
            for kind, val, tid in registry:
                if kind == "text" and val in texts_here:
                    return i, tid
    return None, None


def agent_id_of(psr):
    """
    Ground-truth thread identifier for Claude-Code-CLI-originated calls: the
    `x-claude-code-agent-id` request header, echoed into
    proxy_server_request.litellm_metadata.headers. Differs per spawned
    sub-agent; `x-claude-code-session-id` (not used here) stays constant for
    the whole top-level session. Calls that don't carry this header (e.g. a
    separate non-Claude-Code judge/classifier process) fall back to the
    heuristic tool-call-ID anchor matching below.
    """
    headers = ((psr or {}).get("litellm_metadata") or {}).get("headers") or {}
    return headers.get("x-claude-code-agent-id") or None


def sig_of(resp):
    msg = (resp.get("choices") or [{}])[0].get("message") or {}
    tcs = msg.get("tool_calls") or []
    if tcs:
        return ("tool", frozenset(t["id"] for t in tcs if t.get("id")))
    return ("text", msg.get("content"))


def render_content_block(block, lines):
    bt = block.get("type")
    if bt == "text":
        lines.append(block.get("text", ""))
    elif bt == "tool_result":
        content = block.get("content")
        is_error = block.get("is_error")
        tag = "tool_result (ERROR)" if is_error else "tool_result"
        lines.append(f"**[{tag}: `{block.get('tool_use_id')}`]**")
        if isinstance(content, str):
            lines.append(f"```\n{content}\n```")
        elif isinstance(content, list):
            for c in content:
                if isinstance(c, dict) and c.get("type") == "text":
                    lines.append(f"```\n{c.get('text', '')}\n```")
                else:
                    lines.append(f"*(non-text tool_result block: {c.get('type') if isinstance(c, dict) else type(c)})*")
        else:
            lines.append(f"*(tool_result content: {content!r})*")
    elif bt == "tool_use":
        lines.append("*(assistant tool_use block re-appended into history — already shown as the tool call above)*")
    elif bt == "thinking":
        lines.append("*(assistant thinking block re-appended into history — already shown as reasoning above)*")
    elif bt == "image":
        lines.append("*(image block, not rendered)*")
    else:
        lines.append(f"*(unhandled content block type: {bt})*")


def render_new_message(msg, lines):
    role = msg.get("role")
    content = msg.get("content")
    lines.append(f"\n**[new `{role}` message]**\n")
    if isinstance(content, str):
        lines.append(content)
    elif isinstance(content, list):
        for block in content:
            if isinstance(block, dict):
                render_content_block(block, lines)
            else:
                lines.append(str(block))
    else:
        lines.append(str(content))


def render_turn(r, lines, turn_label):
    ts = r["startTime"]
    resp = r.get("response") or {}
    model = resp.get("model")
    choice = (resp.get("choices") or [{}])[0]
    msg = choice.get("message") or {}
    content = msg.get("content")
    reasoning = msg.get("reasoning_content")
    tool_calls = msg.get("tool_calls") or []
    finish_reason = choice.get("finish_reason")

    lines.append(f"\n#### {turn_label} — {fmt_ts(ts)} — `{model}`")
    lines.append(
        f"*({r['request_duration_ms']}ms, {r['prompt_tokens']} prompt / {r['completion_tokens']} completion "
        f"tokens, ${r['spend']:.6f}, finish_reason={finish_reason})*\n"
    )
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
    return model


# ---- Pass 1: assign each successful turn to a thread ----
registry = []  # (kind, value, thread_id)
next_thread_id = 0
turn_info = []  # per successful-turn: dict(r=..., thread_id=..., anchor_idx=..., cur_messages=...)

for r in ordered:
    if r["status"] != "success":
        continue
    psr = psr_by_id.get(r["request_id"]) or {}
    cur_messages = psr.get("messages") or []
    anchor_idx, heuristic_tid = find_anchor(cur_messages, registry)

    ground_truth_agent = agent_id_of(psr)
    if ground_truth_agent:
        tid = f"agent:{ground_truth_agent}"
    elif heuristic_tid is not None:
        tid = heuristic_tid
    else:
        tid = next_thread_id
        next_thread_id += 1

    kind, val = sig_of(r["response"])
    registry.append((kind, val, tid))
    turn_info.append({"r": r, "thread_id": tid, "anchor_idx": anchor_idx, "cur_messages": cur_messages})

threads = {}
for info in turn_info:
    threads.setdefault(info["thread_id"], []).append(info)

thread_sizes = sorted(threads.items(), key=lambda kv: -len(kv[1]))
main_threads = [(tid, infos) for tid, infos in thread_sizes if len(infos) >= MAIN_THREAD_MIN_SIZE]
singleton_and_small = [(tid, infos) for tid, infos in thread_sizes if len(infos) < MAIN_THREAD_MIN_SIZE]
singleton_infos = sorted(
    (info for _, infos in singleton_and_small for info in infos),
    key=lambda info: info["r"]["startTime"],
)

# ---- Build output ----
out = []
out.append(f"# Reconstructed FULL Chat Transcript — {SESSION_LABEL}\n")
out.append(
    "> Two-sided reconstruction, built directly from the live database's "
    "**`proxy_server_request`** column (the real request bodies — populated "
    "if `store_prompts_in_spend_logs` was active at capture time), joined "
    "against a JSON export's `response` column. The `messages` column in "
    "that export is always `{}` for standard chat completions and cannot be "
    "used for this.\n"
)
out.append(
    "> **If `session_id` is shared across multiple interleaved agent "
    "threads** (e.g. a main Claude Code conversation plus spawned "
    "sub-agents, plus a separate per-action judge making one-shot calls), "
    "naively diffing each request against the previous *chronological* one "
    "produces nonsense. This script groups turns into threads two ways: "
    "**(1) ground truth** — the `x-claude-code-agent-id` request header, "
    "echoed into `proxy_server_request.litellm_metadata.headers`, differs "
    "per spawned sub-agent and is used directly as the thread key when "
    "present (labeled `agent:<id>` below); **(2) heuristic fallback** — for "
    "calls without that header (e.g. a separate judge/classifier process "
    "not made via the Claude Code CLI), threads are inferred by matching "
    "tool-call IDs (stable across the Anthropic-shaped request / "
    "OpenAI-shaped response translation) or exact response text (labeled "
    "with a bare number below). Either way, the matched anchor position is "
    "used to extract only the *new* messages per turn.\n"
)
out.append(
    f"> **Result: {len(threads)} distinct thread(s).** "
    f"{len(main_threads)} substantial (≥{MAIN_THREAD_MIN_SIZE} turns), "
    f"shown in full below; {len(singleton_and_small)} short/single-shot "
    f"({sum(len(i) for _, i in singleton_and_small)} turns total), grouped "
    "into one section.\n"
)
out.append(
    "> **Caveat:** strings here were still subject to whatever "
    "`MAX_STRING_LENGTH_PROMPT_IN_DB` was active *at capture time*. Long "
    "blocks may carry an inline `(litellm_truncated skipped N chars ...)` "
    "marker.\n"
)
out.append("---\n")

out.append("## Thread summary\n")
out.append("| Thread | Turns | First seen | Models used |")
out.append("|---|---:|---|---|")
for tid, infos in thread_sizes:
    models = sorted({(i["r"].get("response") or {}).get("model") for i in infos} - {None})
    out.append(f"| {tid} | {len(infos)} | {fmt_ts(infos[0]['r']['startTime'])} | {', '.join(models)} |")
out.append("")

for rank, (tid, infos) in enumerate(main_threads, start=1):
    infos_sorted = sorted(infos, key=lambda i: i["r"]["startTime"])
    models = sorted({(i["r"].get("response") or {}).get("model") for i in infos_sorted} - {None})
    out.append(f"\n## Thread {tid} ({len(infos_sorted)} turns, models: {', '.join(models)})\n")
    for n, info in enumerate(infos_sorted, start=1):
        cur_messages = info["cur_messages"]
        anchor_idx = info["anchor_idx"]
        new_messages = cur_messages if anchor_idx is None else cur_messages[anchor_idx + 1:]
        for m in new_messages:
            if m.get("role") == "assistant":
                out.append("\n*(assistant's own prior turn re-appended into history — already shown above)*\n")
                continue
            render_new_message(m, out)
        render_turn(info["r"], out, f"Turn {n}")

out.append(f"\n## Isolated / short calls ({len(singleton_infos)} turns, chronological)\n")
out.append(
    "Each of these starts fresh (no continuation found from any other "
    "turn) — the full `proxy_server_request.messages` for each is shown, "
    "since for a one-shot call that *is* the whole thing (typically an "
    "embedded excerpt of context, not a growing history).\n"
)
for n, info in enumerate(singleton_infos, start=1):
    cur_messages = info["cur_messages"]
    for m in cur_messages:
        if m.get("role") == "assistant":
            continue
        render_new_message(m, out)
    render_turn(info["r"], out, f"Call {n}")

error_rows = [r for r in ordered if r["status"] == "failure"]
out.append(f"\n## Errors ({len(error_rows)} total, chronological)\n")
for r in error_rows:
    out.append(f"\n### ⚠️ [ERROR] {fmt_ts(r['startTime'])}\n")
    out.append(f"- request_id: `{r['request_id']}`")
    out.append(
        f"- duration: {r['request_duration_ms']}ms, prompt_tokens: {r['prompt_tokens']}, "
        f"completion_tokens: {r['completion_tokens']}, spend: ${r['spend']}"
    )
    out.append("- no error text captured in the DB log for this row\n")

with open(OUT, "w") as f:
    f.write("\n".join(out))

print("wrote", OUT)
print("total successful turns:", len(turn_info))
print("total threads:", len(threads))
print("main threads (>=%d turns):" % MAIN_THREAD_MIN_SIZE, [(tid, len(i)) for tid, i in main_threads])
print("singleton/small turns:", len(singleton_infos))
print("errors:", len(error_rows))
