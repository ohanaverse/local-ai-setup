#!/usr/bin/env python3
"""Build a full-context Markdown transcript from a Claude Code session snapshot.

Input: the snapshot directory produced by 03_capture_claude_code.sh —
one top-level <session-id>.jsonl, optionally a <session-id>/ directory
(subagents/, meta files) and a capture.json manifest.

Output structure (see
docs/superpowers/specs/2026-09-25-cc-session-transcript-builder-design.md):

  # session header — source, metadata, files, models, aggregate usage, source tally
  ## 1. Harness context   — attachments before the first assistant turn
  ## 2. Conversation      — user/assistant/system turns in file order
  ## 3. Subagents         — one section per agent-*.jsonl, same rendering
  ## Appendix             — skipped harness bookkeeping, counts per type

Usage: python3 build_cc_transcript.py <snapshot-dir> [output.md]
"""

import json
import sys
from collections import Counter
from pathlib import Path

# Entry types that are harness bookkeeping, not transcript content.
# Counted in the Appendix, never rendered.
BOOKKEEPING = {
    "last-prompt": "header-only pointer to the newest prompt leaf",
    "atis-latch": "session-latch token",
    "mode": "current mode (e.g. normal)",
    "permission-mode": "current permission mode",
    "queue-operation": "queued harness payload (duplicated by user entries)",
    "file-history-snapshot": "file-history state for rewind",
    "cost-state": "running cost/usage totals",
}


def h(depth, text):
    """A markdown heading, capped at h6."""
    return "#" * min(depth, 6) + " " + text


def fenced_lines(text, lang=""):
    """Render text as a fenced code block (list of lines), widening the
    fence past any backtick run inside text."""
    text = str(text)
    run = longest = 0
    for ch in text:
        run = run + 1 if ch == "`" else 0
        longest = max(longest, run)
    fence = "`" * max(3, longest + 1)
    return [f"{fence}{lang}", *text.splitlines(), fence]


def read_jsonl(path):
    """Return [(line_number, entry_or_None), ...] for each non-blank line.

    None marks a line that failed to parse — rendered as an inline marker,
    never fatal.
    """
    out = []
    with open(path, errors="replace") as fh:
        for lineno, line in enumerate(fh, 1):
            if not line.strip():
                continue
            try:
                out.append((lineno, json.loads(line)))
            except json.JSONDecodeError:
                out.append((lineno, None))
    return out


class FileTranscript:
    """Renders one Claude Code JSONL transcript file (main or sub-agent)."""

    def __init__(self, path):
        self.path = Path(path)
        self.entries = read_jsonl(self.path)
        self.tally_entries = Counter()
        self.tally_blocks = Counter()
        self.tool_names = {}          # tool_use_id -> tool name
        self.seen_attachments = set()  # canonical attachment JSON
        self.first_assistant = next(
            (i for i, (_, e) in enumerate(self.entries)
             if e is not None and e.get("type") == "assistant"),
            None)
        self._count_tallies()

    def _count_tallies(self):
        for _, e in self.entries:
            if e is None:
                self.tally_entries["(unparseable)"] += 1
                continue
            self.tally_entries[e.get("type", "?")] += 1
            content = (e.get("message") or {}).get("content")
            if isinstance(content, str):
                self.tally_blocks["(string content)"] += 1
            elif isinstance(content, list):
                for b in content:
                    if isinstance(b, dict):
                        self.tally_blocks[b.get("type", "?")] += 1

    def is_preamble(self, idx):
        """Harness context = attachments before the first assistant turn."""
        return self.first_assistant is None or idx < self.first_assistant

    # ---- attachments ----

    def render_attachment(self, md, att, depth):
        """Render one attachment; identical repeats collapse to one line."""
        atype = att.get("type", "unknown")
        canonical = json.dumps(att, sort_keys=True, ensure_ascii=False)
        if canonical in self.seen_attachments:
            md.append(f"*{atype}: identical to an earlier attachment (skipped)*")
            md.append("")
            return
        self.seen_attachments.add(canonical)

        if atype == "prompt_snapshot":
            md.append(h(depth, "System prompt (prompt_snapshot)"))
            md.append("")
            sp = att.get("systemPrompt")
            if isinstance(sp, list):
                md.extend("\n".join(str(c) for c in sp).splitlines())
                md.append("")
            elif sp is not None:
                md.extend(str(sp).splitlines())
                md.append("")
            rest = {k: v for k, v in att.items()
                    if k not in ("type", "systemPrompt")}
            if rest:
                md.append("*(other prompt_snapshot fields)*")
                md.append("")
                md.extend(fenced_lines(
                    json.dumps(rest, indent=2, ensure_ascii=False), "json"))
                md.append("")
        elif atype == "instructions":
            md.append(h(depth, "Instructions (preference files)"))
            md.append("")
            for f in att.get("files", []):
                md.append(h(depth + 1, f.get("path", "?")))
                md.append("")
                md.extend(fenced_lines(f.get("content", "")))
                md.append("")
        else:
            md.append(h(depth, atype))
            md.append("")
            md.extend(fenced_lines(
                json.dumps(att, indent=2, ensure_ascii=False), "json"))
            md.append("")

    # ---- content blocks ----

    def render_block(self, md, b):
        t = b.get("type")
        if t == "text":
            md.extend(b.get("text", "").splitlines())
            md.append("")
        elif t == "thinking":
            text = b.get("text") or ""
            if text.strip():
                for line in text.splitlines():
                    md.append(f"> {line}" if line else ">")
                md.append("")
            else:
                note = "signature kept" if b.get("signature") else "no text"
                md.append(f"*(encrypted thinking — {note})*")
                md.append("")
        elif t == "tool_use":
            self.tool_names[b.get("id", "?")] = b.get("name", "?")
            md.append(f"**Tool call: {b.get('name', '?')}** `{b.get('id', '?')}`")
            md.append("")
            md.extend(fenced_lines(
                json.dumps(b.get("input", {}), indent=2, ensure_ascii=False),
                "json"))
            md.append("")
        elif t == "tool_result":
            tid = b.get("tool_use_id", "?")
            name = self.tool_names.get(tid, "?")
            err = " **ERROR**" if b.get("is_error") else ""
            md.append(f"**Tool result: {name}** `{tid}`{err}")
            md.append("")
            self._render_tool_result_content(md, b.get("content", ""))
        elif t == "image":
            data = (b.get("source") or {}).get("data", "")
            md.append(f"[image, {len(data)} base64 chars omitted]")
            md.append("")
        else:
            md.extend(fenced_lines(
                json.dumps(b, indent=2, ensure_ascii=False), "json"))
            md.append("")

    def _render_tool_result_content(self, md, content):
        if isinstance(content, str):
            md.extend(fenced_lines(content))
            md.append("")
        elif isinstance(content, list):
            for b in content:
                if isinstance(b, dict) and b.get("type") == "text":
                    md.extend(b.get("text", "").splitlines())
                    md.append("")
                elif isinstance(b, dict) and b.get("type") == "image":
                    data = (b.get("source") or {}).get("data", "")
                    md.append(f"[image, {len(data)} base64 chars omitted]")
                    md.append("")
                else:
                    md.extend(fenced_lines(
                        json.dumps(b, indent=2, ensure_ascii=False), "json"))
                    md.append("")
        elif content is not None:
            md.extend(fenced_lines(
                json.dumps(content, indent=2, ensure_ascii=False), "json"))
            md.append("")

    # ---- turns ----

    def render_user(self, md, e, depth):
        ts = e.get("timestamp", "")
        meta = " (meta)" if e.get("isMeta") else ""
        md.append(h(depth, f"User{meta} — {ts}"))
        md.append("")
        content = (e.get("message") or {}).get("content")
        if isinstance(content, str):
            md.extend(content.splitlines())
            md.append("")
        elif isinstance(content, list):
            for b in content:
                if isinstance(b, dict):
                    self.render_block(md, b)
        elif content is not None:
            md.extend(fenced_lines(
                json.dumps(content, indent=2, ensure_ascii=False), "json"))
            md.append("")
        if "toolUseResult" in e:
            md.append("*(entry-level toolUseResult)*")
            md.append("")
            md.extend(fenced_lines(
                json.dumps(e["toolUseResult"], indent=2, ensure_ascii=False),
                "json"))
            md.append("")

    def render_assistant(self, md, e, depth):
        msg = e.get("message") or {}
        ts = e.get("timestamp", "")
        model = msg.get("model", "?")
        md.append(h(depth, f"Assistant — {model} — {ts}"))
        md.append("")
        usage = msg.get("usage") or {}
        bits = []
        if usage:
            bits.append(
                f"in {usage.get('input_tokens', 0)}, "
                f"cache_read {usage.get('cache_read_input_tokens', 0)}, "
                f"cache_create {usage.get('cache_creation_input_tokens', 0)}, "
                f"out {usage.get('output_tokens', 0)}")
        if msg.get("stop_reason"):
            bits.append(f"stop: {msg['stop_reason']}")
        if bits:
            md.append(f"*({', '.join(bits)})*")
            md.append("")
        content = msg.get("content")
        if isinstance(content, str):
            md.extend(content.splitlines())
            md.append("")
        elif isinstance(content, list):
            for b in content:
                if isinstance(b, dict):
                    self.render_block(md, b)

    def render_system(self, md, e):
        """System entries are one-line metadata notes, not headings."""
        sub = e.get("subtype") or "?"
        ts = e.get("timestamp", "")
        bits = [f"{k}={e[k]}" for k in
                ("stopReason", "hookCount", "preventedContinuation",
                 "durationMs", "messageCount", "level")
                if k in e]
        extra = f" — {', '.join(bits)}" if bits else ""
        md.append(f"*system: {sub} — {ts}{extra}*")
        md.append("")
        if e.get("content"):
            md.extend(fenced_lines(e["content"]))
            md.append("")

    # ---- sections ----

    def render_harness_context(self, md, depth, title="Harness context"):
        md.append(h(depth, title))
        md.append("")
        found = False
        for i, (_, e) in enumerate(self.entries):
            if e is None or e.get("type") != "attachment":
                continue
            if not self.is_preamble(i):
                continue
            found = True
            self.render_attachment(md, e.get("attachment", {}), depth + 1)
        if not found:
            md.append("*(none)*")
            md.append("")

    def render_conversation(self, md, depth, title="Conversation"):
        md.append(h(depth, title))
        md.append("")
        for i, (lineno, e) in enumerate(self.entries):
            if e is None:
                md.append(f"`[unparseable line {lineno}]`")
                md.append("")
                continue
            t = e.get("type")
            if t == "attachment":
                if self.is_preamble(i):
                    continue
                self.render_attachment(md, e.get("attachment", {}), depth + 1)
            elif t == "user":
                self.render_user(md, e, depth + 1)
            elif t == "assistant":
                self.render_assistant(md, e, depth + 1)
            elif t == "system":
                self.render_system(md, e)
            # else: bookkeeping — counted in the Appendix


def render_header(md, snap, main_t, sub_ts, capture):
    session_id = main_t.path.stem
    md.append(f"# Claude Code session {session_id}")
    md.append("")
    if capture.get("source_dir"):
        md.append(f"- Source: `{capture['source_dir']}`")
    if capture.get("captured_at"):
        md.append(f"- Captured: {capture['captured_at']}")
    if capture.get("error"):
        md.append(f"- Capture manifest: *{capture['error']}*")
    meta = {}
    for _, e in main_t.entries:
        if e is None:
            continue
        for k in ("version", "entrypoint", "cwd", "gitBranch"):
            if k in e and k not in meta:
                meta[k] = e[k]
    for k, label in (("version", "CLI version"), ("entrypoint", "Entrypoint"),
                     ("cwd", "Working dir"), ("gitBranch", "Git branch")):
        if k in meta:
            md.append(f"- {label}: `{meta[k]}`")
    md.append("")

    md.append(h(2, "Files"))
    md.append("")
    for p in sorted(snap.rglob("*")):
        if p.is_file():
            md.append(f"- `{p.relative_to(snap)}` ({p.stat().st_size} bytes)")
    md.append("")

    models = set()
    for t in [main_t, *sub_ts]:
        for _, e in t.entries:
            if e is None or e.get("type") != "assistant":
                continue
            m = (e.get("message") or {}).get("model")
            if m:
                models.add(m)
    md.append(h(2, "Models & aggregate usage"))
    md.append("")
    md.append("- Models seen in turns: "
              + (", ".join(f"`{m}`" for m in sorted(models)) or "*(none)*"))
    last_cost = None
    for _, e in main_t.entries:
        if e is not None and e.get("type") == "cost-state":
            last_cost = e
    if last_cost:
        total = last_cost.get("totalCostUSD")
        if total is not None:
            md.append(f"- Total cost: ${total}")
        for extra in ("totalLinesAdded", "totalLinesRemoved", "totalDuration"):
            v = last_cost.get(extra)
            if v is not None:
                md.append(f"- {extra}: {v}")
        for model, u in (last_cost.get("modelUsage") or {}).items():
            md.append(f"- `{model}`: " + ", ".join(f"{k} {u[k]}" for k in sorted(u)))
    md.append("")

    md.append(h(2, "Source tally"))
    md.append("")
    md.append("| File | Entry types | Content blocks |")
    md.append("|---|---|---|")
    for t in [main_t, *sub_ts]:
        ents = ", ".join(f"{k}: {n}" for k, n in sorted(t.tally_entries.items()))
        blocks = ", ".join(f"{k}: {n}" for k, n in sorted(t.tally_blocks.items()))
        md.append(f"| `{t.path.name}` | {ents} | {blocks} |")
    md.append("")


def render_subagent(md, sf, t):
    agent_id = sf.stem[len("agent-"):]
    meta = {}
    extras = []
    for p in sorted(sf.parent.glob(sf.stem + "*.json")):
        if p.name.endswith(".meta.json"):
            try:
                meta = json.loads(p.read_text())
            except json.JSONDecodeError:
                meta = {"error": "unparseable meta"}
        elif p != sf:
            extras.append(p)
    title = agent_id
    if meta:
        label = meta.get("description") or meta.get("name") or "?"
        title = f"{agent_id} — {label} ({meta.get('agentType', '?')})"
    md.append(h(3, f"Subagent {title}"))
    md.append("")
    if meta:
        md.extend(fenced_lines(
            json.dumps(meta, indent=2, ensure_ascii=False), "json"))
        md.append("")
    for p in extras:
        md.extend(fenced_lines(p.read_text(errors="replace"), "json"))
        md.append("")
    t.render_harness_context(md, 4)
    md.append("---")
    md.append("")
    t.render_conversation(md, 4)
    md.append("---")
    md.append("")


def render_appendix(md, *transcripts):
    md.append(h(2, "Appendix: skipped harness bookkeeping"))
    md.append("")
    counts = Counter()
    for t in transcripts:
        counts.update(t.tally_entries)
    rows = []
    for t, n in sorted(counts.items()):
        if t in ("user", "assistant", "system", "attachment",
                 "(unparseable)"):
            continue
        desc = BOOKKEEPING.get(t, "unrecognized entry type — counted, not rendered")
        rows.append(f"| `{t}` | {n} | {desc} |")
    if rows:
        md.append("| Type | Count | What it is |")
        md.append("|---|---:|---|")
        md.extend(rows)
    else:
        md.append("*(none)*")
    md.append("")


def main(argv):
    if len(argv) not in (2, 3):
        print(__doc__)
        return 2
    snap = Path(argv[1])
    if not snap.is_dir():
        sys.exit(f"error: {snap} is not a directory")
    main_jsonls = sorted(snap.glob("*.jsonl"))
    if not main_jsonls:
        sys.exit(f"error: no top-level *.jsonl in {snap}")
    if len(main_jsonls) > 1:
        names = ", ".join(p.name for p in main_jsonls)
        sys.exit(f"error: multiple top-level *.jsonl in {snap}: {names}")
    main_file = main_jsonls[0]
    session_id = main_file.stem
    out_path = (Path(argv[2]) if len(argv) == 3
                else Path(f"session_cc_{session_id}-transcript.md"))

    capture = {}
    capture_path = snap / "capture.json"
    if capture_path.exists():
        try:
            capture = json.loads(capture_path.read_text())
        except json.JSONDecodeError:
            capture = {"error": "capture.json unparseable"}

    main_t = FileTranscript(main_file)
    sub_files = sorted((snap / session_id / "subagents").glob("agent-*.jsonl"))
    sub_ts = [FileTranscript(p) for p in sub_files]

    md = []
    render_header(md, snap, main_t, sub_ts, capture)
    md.append("---")
    md.append("")
    main_t.render_harness_context(md, 2, title="1. Harness context")
    md.append("---")
    md.append("")
    main_t.render_conversation(md, 2, title="2. Conversation")
    md.append("---")
    md.append("")
    md.append(h(2, "3. Subagents"))
    md.append("")
    if not sub_ts:
        md.append("*(none)*")
        md.append("")
    for sf, t in zip(sub_files, sub_ts):
        render_subagent(md, sf, t)
    render_appendix(md, main_t, *sub_ts)

    out_path.write_text("\n".join(md) + "\n")
    print(f"wrote {out_path} ({len(md)} lines)")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
