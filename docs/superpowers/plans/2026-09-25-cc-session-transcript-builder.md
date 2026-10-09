# Claude Code Local-Session Transcript Builder — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Capture any Claude Code session's raw local record from `~/.claude/projects/` into a stable snapshot and build a full-context Markdown transcript (harness context + conversation + subagents) from it.

**Architecture:** Two new files in `litellm-session-logs/` following the existing export→build pipeline split: a POSIX `sh` capture script (numbered step 03) that snapshots the session's files, and a stdlib-only Python builder (`build_cc_transcript.py`) that renders the snapshot into structured Markdown. Spec: `docs/superpowers/specs/2026-09-25-cc-session-transcript-builder-design.md`.

**Tech Stack:** POSIX sh (`#!/bin/sh`, macOS `stat -f`), Python 3 stdlib only (no venv/deps — directory convention).

**Worktree:** `/Users/keith/github/ohanaverse/local-ai-setup/.worktrees/cc-session-transcripts` (branch `cc-session-transcripts`, created from `main` at `5509ad3`; the spec is committed at `7381eaf`+). **Run all commands from this worktree**, and the capture/builder commands from its `litellm-session-logs/` directory. Pipeline outputs (`session_*`) are gitignored there already.

**No test framework exists for this directory (spec non-goal).** Verification is against the real session `cce7a30d-9fc2-46d6-8388-bc4d55df6917` (a `/code-review low` run whose record lives under `~/.claude/projects/-Users-keith-github-ohanaverse-local-ai-setup--worktrees-fix-config-toml-race-143/`) plus `make lint-shell` / `make lint`. Every verification step below lists exact expected values pre-computed from that session.

---

### Task 1: Capture script `03_capture_claude_code.sh` + Makefile lint entry

**Files:**
- Create: `litellm-session-logs/03_capture_claude_code.sh` (executable)
- Modify: `Makefile` (add to `SHELL_SCRIPTS`)

- [ ] **Step 1: Write the capture script**

Create `litellm-session-logs/03_capture_claude_code.sh` with exactly:

```sh
#!/bin/sh
# Snapshot ("capture") one Claude Code session's raw local record from
# ~/.claude/projects into a stable directory, for build_cc_transcript.py
# to build a full-context Markdown transcript from.
#
# Why copy instead of reading in place: ~/.claude applies retention
# cleanup (default 30 days) that can delete the raw record, and a live
# session keeps appending to these same files.
#
# Copies:
#   <snapshot>/<session-id>.jsonl   the main session transcript
#   <snapshot>/<session-id>/        the session dir (subagents/ etc.), if present
#   <snapshot>/capture.json         manifest: source dir + capture time
#
# Usage: ./03_capture_claude_code.sh <session-id> [snapshot-dir]
#   snapshot-dir defaults to session_cc_<session-id> in the current directory.
#   CLAUDE_PROJECTS_DIR overrides the projects root (default: $HOME/.claude/projects).

set -eu

SESSION_ID="${1:?usage: $0 <session-id> [snapshot-dir]}"
SNAPSHOT_DIR="${2:-session_cc_${SESSION_ID}}"
PROJECTS_DIR="${CLAUDE_PROJECTS_DIR:-$HOME/.claude/projects}"

if [ ! -d "$PROJECTS_DIR" ]; then
  echo "error: $PROJECTS_DIR does not exist" >&2
  exit 1
fi

# Locate the session's main transcript: ~/.claude/projects/<slug>/<session-id>.jsonl
# (project slugs replace every character outside [a-zA-Z0-9-] with '-', so the
# paths below never contain whitespace — word-splitting in the for loop is safe)
matches=$(find "$PROJECTS_DIR" -maxdepth 2 -name "${SESSION_ID}.jsonl" 2>/dev/null || true)
if [ -z "$matches" ]; then
  echo "error: no ${SESSION_ID}.jsonl under $PROJECTS_DIR" >&2
  echo "hint: the session may not exist, or was run with persistence disabled" >&2
  exit 1
fi

main_file=$matches
match_count=$(printf '%s\n' "$matches" | grep -c .)
if [ "$match_count" -gt 1 ]; then
  echo "warning: multiple matches for ${SESSION_ID}:" >&2
  printf '%s\n' "$matches" >&2
  main_file=
  newest=0
  for f in $matches; do
    mtime=$(stat -f %m "$f") # macOS stat; this repo is macOS-only (LaunchAgents, flock)
    if [ "$mtime" -gt "$newest" ]; then
      newest=$mtime
      main_file=$f
    fi
  done
  echo "warning: using newest by mtime: $main_file" >&2
fi

slug_dir=$(dirname "$main_file")

mkdir -p "$SNAPSHOT_DIR"
cp -Rp "$main_file" "$SNAPSHOT_DIR/"
if [ -d "${slug_dir}/${SESSION_ID}" ]; then
  cp -Rp "${slug_dir}/${SESSION_ID}" "$SNAPSHOT_DIR/"
fi

printf '{"session_id":"%s","source_dir":"%s","captured_at":"%s"}\n' \
  "$SESSION_ID" "$slug_dir" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  > "${SNAPSHOT_DIR}/capture.json"

echo "captured session ${SESSION_ID} from ${slug_dir} into ${SNAPSHOT_DIR}/"
ls -lR "$SNAPSHOT_DIR"
```

- [ ] **Step 2: Make it executable**

Run: `chmod +x litellm-session-logs/03_capture_claude_code.sh`
Expected: no output.

- [ ] **Step 3: Add it to the root Makefile's SHELL_SCRIPTS**

In `Makefile`, the `SHELL_SCRIPTS := \` block, change:

```make
	litellm-session-logs/02_export_proxy_server_request.sh
```

to:

```make
	litellm-session-logs/02_export_proxy_server_request.sh \
	litellm-session-logs/03_capture_claude_code.sh
```

- [ ] **Step 4: Lint**

Run: `make lint-shell`
Expected: the bash -n loop lists the new script (`bash -n litellm-session-logs/03_capture_claude_code.sh`) and shellcheck (if installed) reports no errors at `--severity=error`.

- [ ] **Step 5: Verify error path on a nonexistent session**

Run (from `litellm-session-logs/`): `./03_capture_claude_code.sh no-such-session-id-000000000000; echo "exit=$?"`
Expected: `error: no no-such-session-id-000000000000.jsonl under /Users/keith/.claude/projects`, a hint line on stderr, and `exit=1`. No directory created.

- [ ] **Step 6: Capture the real session**

Run (from `litellm-session-logs/`):

```bash
./03_capture_claude_code.sh cce7a30d-9fc2-46d6-8388-bc4d55df6917
```

Expected: `captured session cce7a30d-9fc2-46d6-8388-bc4d55df6917 from /Users/keith/.claude/projects/-Users-keith-github-ohanaverse-local-ai-setup--worktrees-fix-config-toml-race-143 into session_cc_cce7a30d-9fc2-46d6-8388-bc4d55df6917/` followed by an `ls -lR` inventory containing exactly 6 files:

- `capture.json` (~130 bytes)
- `cce7a30d-9fc2-46d6-8388-bc4d55df6917.jsonl` (282273 bytes)
- `cce7a30d-9fc2-46d6-8388-bc4d55df6917/subagents/agent-a41b7612d0d197840.forked-skill.json` (77 bytes)
- `cce7a30d-9fc2-46d6-8388-bc4d55df6917/subagents/agent-a41b7612d0d197840.forked-skill.marker.json` (46 bytes)
- `cce7a30d-9fc2-46d6-8388-bc4d55df6917/subagents/agent-a41b7612d0d197840.jsonl` (310032 bytes)
- `cce7a30d-9fc2-46d6-8388-bc4d55df6917/subagents/agent-a41b7612d0d197840.meta.json` (157 bytes)

- [ ] **Step 7: Verify the snapshot is byte-identical to the source**

Run (from `litellm-session-logs/`):

```bash
SRC=~/.claude/projects/-Users-keith-github-ohanaverse-local-ai-setup--worktrees-fix-config-toml-race-143
SNAP=session_cc_cce7a30d-9fc2-46d6-8388-bc4d55df6917
cmp "$SNAP/cce7a30d-9fc2-46d6-8388-bc4d55df6917.jsonl" "$SRC/cce7a30d-9fc2-46d6-8388-bc4d55df6917.jsonl" \
  && diff -r "$SNAP/cce7a30d-9fc2-46d6-8388-bc4d55df6917" "$SRC/cce7a30d-9fc2-46d6-8388-bc4d55df6917" \
  && echo SNAPSHOT-OK
```

Expected: `SNAPSHOT-OK` (no diff output). Also `cat "$SNAP/capture.json"` shows the session id, source_dir, captured_at.

- [ ] **Step 8: Commit**

```bash
git add litellm-session-logs/03_capture_claude_code.sh Makefile
git commit -m "litellm-session-logs: add 03_capture_claude_code.sh (snapshot Claude Code session record)"
```

---

### Task 2: Builder `build_cc_transcript.py`

**Files:**
- Create: `litellm-session-logs/build_cc_transcript.py` (executable)

The builder is one cohesive stdlib script (~300 lines); it is written complete in this task and verified in Task 3. Data-model facts baked into the code (verified against the real session):

- Entries are JSONL, one object per line; `type` ∈ {`user`, `assistant`, `system`, `attachment`, plus bookkeeping: `last-prompt`, `atis-latch`, `mode`, `permission-mode`, `queue-operation`, `file-history-snapshot`, `cost-state`}.
- `message.content` is either a plain string or a list of blocks (`text`, `thinking`, `tool_use`, `tool_result`, `image`). Tool results reference their call by `tool_use_id`; the call appears in an earlier assistant turn.
- Attachments live under `.attachment` as objects with a `type` field. `prompt_snapshot` has `systemPrompt` (array of strings); `instructions` has `files` (list of `{content, path, type}`).
- Harness context = attachments before the first `assistant` entry (in the main session the heavyweight context lands between the queued user prompt and the first assistant reply; in a subagent file it lands right after the task prompt).

- [ ] **Step 1: Write the builder**

Create `litellm-session-logs/build_cc_transcript.py` with exactly:

```python
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
        if t in ("user", "assistant", "attachment", "(unparseable)"):
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
```

- [ ] **Step 2: Make it executable and run it**

Run (from `litellm-session-logs/`):

```bash
chmod +x build_cc_transcript.py
python3 build_cc_transcript.py session_cc_cce7a30d-9fc2-46d6-8388-bc4d55df6917
```

Expected: `wrote session_cc_cce7a30d-9fc2-46d6-8388-bc4d55df6917-transcript.md (<N> lines)` — no traceback. (On this session N is on the order of a few thousand lines; the exact value is not asserted.)

- [ ] **Step 3: Structural greps**

Run (from `litellm-session-logs/`):

```bash
OUT=session_cc_cce7a30d-9fc2-46d6-8388-bc4d55df6917-transcript.md
for s in "# Claude Code session cce7a30d-9fc2-46d6-8388-bc4d55df6917" \
         "## Files" "## Models & aggregate usage" "## Source tally" \
         "## 1. Harness context" "## 2. Conversation" "## 3. Subagents" \
         "## Appendix: skipped harness bookkeeping" \
         "### Subagent a41b7612d0d197840 — /code-review low (general-purpose)"; do
  grep -qF "$s" "$OUT" && echo "OK: $s" || echo "MISSING: $s"
done
```

Expected: `OK:` for all nine strings, `MISSING:` for none.

Also:

```bash
grep -c "^# " "$OUT"                      # → 1
grep -c "^> " "$OUT"                      # → 0 (all thinking in this session is encrypted)
grep -c "encrypted thinking — signature kept" "$OUT"   # → 3
grep -c "Tool call: Bash" "$OUT"          # → 2
grep -c "Tool result: Bash" "$OUT"        # → 2
grep -c "identical to an earlier attachment" "$OUT"     # → 0 (no duplicate attachments in this session)
grep -c "<task-notification>" "$OUT"      # → 1 (rendered once, from the user entry; the queue-operation copy is appendix-counted)
grep -c "System prompt (prompt_snapshot)" "$OUT"       # → 4 (2 in main, 2 in subagent — all distinct, all rendered)
grep -c "Instructions (preference files)" "$OUT"       # → 2 (main + subagent)
grep -cF "CLI version: \`2.1.283\`" "$OUT" # → 1
grep -cF "Working dir: \`/Users/keith/github/ohanaverse/local-ai-setup/.worktrees/fix-config-toml-race-143\`" "$OUT"  # → 1
grep -cF "Git branch: \`fix-config-toml-race-143\`" "$OUT"  # → 1
grep -c "Total cost: \$" "$OUT"            # → 1
grep -c "Models seen in turns: \`claude-sonnet-5\`" "$OUT"   # → 1
```

- [ ] **Step 4: Commit**

```bash
git add litellm-session-logs/build_cc_transcript.py
git commit -m "litellm-session-logs: add build_cc_transcript.py (full-context local-session transcript builder)"
```

---

### Task 3: End-to-end verification against independent jq counts

**Files:** none (verification only; commit only if a fix was needed)

- [ ] **Step 1: Cross-check the Source tally table against jq**

Run (from `litellm-session-logs/`):

```bash
SNAP=session_cc_cce7a30d-9fc2-46d6-8388-bc4d55df6917
OUT=session_cc_cce7a30d-9fc2-46d6-8388-bc4d55df6917-transcript.md
echo "--- jq entry counts:"
jq -r '.type' "$SNAP/cce7a30d-9fc2-46d6-8388-bc4d55df6917.jsonl" | sort | uniq -c
jq -r '.type' "$SNAP/cce7a30d-9fc2-46d6-8388-bc4d55df6917/subagents/agent-a41b7612d0d197840.jsonl" | sort | uniq -c
echo "--- jq block counts:"
for f in "$SNAP/cce7a30d-9fc2-46d6-8388-bc4d55df6917.jsonl" \
         "$SNAP/cce7a30d-9fc2-46d6-8388-bc4d55df6917/subagents/agent-a41b7612d0d197840.jsonl"; do
  echo "string: $(jq -r 'select(.message.content? != null) | select(.message.content|type=="string") | "x"' "$f" | grep -c x)"
  jq -r 'select(.message.content? != null) | select(.message.content|type=="array") | .message.content[]?.type' "$f" | sort | uniq -c
done
echo "--- builder tally rows:"
grep -A5 "^## Source tally" "$OUT" | grep "^|"
```

Expected — main file entries (44 total): `attachment 15, assistant 1, atis-latch 3, cost-state 2, file-history-snapshot 2, last-prompt 5, mode 4, permission-mode 4, queue-operation 2, system 3, user 3`; subagent file entries (35 total): `attachment 26, assistant 6, user 3`; main blocks: `(string content) 3, text 1`; subagent blocks: `(string content) 1, text 1, thinking 3, tool_result 2, tool_use 2`. The builder's two tally rows must match these counts exactly.

- [ ] **Step 2: Cross-check the Appendix table**

Run: `grep -A12 "^## Appendix" "$OUT"`

Expected rows (from the main file only — the subagent file has no bookkeeping types): `last-prompt 5, atis-latch 3, mode 4, permission-mode 4, queue-operation 2, file-history-snapshot 2, cost-state 2` — one row per type with its BOOKKEEPING description.

- [ ] **Step 3: Spot-check the conversation against the raw JSONL**

Run: `sed -n '/## 2. Conversation/,/^---/p' "$OUT" | head -120`

Expected, in order: a `### User (meta) — 2026-09-26T02:31:26.239Z` turn containing the `<local-command-caveat>` text and `/code-review low`; a second `### User — 2026-09-26T02:31:26.235Z` turn with the bare `/code-review low`; a `*system: local_command — …*` note with the `<local-command-stdout>`/`<forked-skill-launch>` content; a `### User — 2026-09-26T02:32:39.892Z` turn containing the `<task-notification>`; then `### Assistant — claude-sonnet-5 — 2026-09-26T02:32:47.437Z` with the usage line `*(in 2, cache_read 36956, cache_create 12017, out 311, stop: end_turn)*` and the final review summary text; then the post-turn inline attachments — a second `prompt_snapshot` (different from the preamble one, so rendered in full: system prompt + `(other prompt_snapshot fields)` with the `tools` array), two Stop-hook `hook_success` attachments, and the `stop_hook_summary`/`turn_duration` system notes. (The preamble's heavyweight context — instructions, session_context, prompt_snapshot #1 — sits in `## 1. Harness context`, not here.)

- [ ] **Step 4: Spot-check the subagent section**

Run: `sed -n '/^### Subagent/,/^## Appendix/p' "$OUT" | head -120`

Expected: the subagent heading + meta JSON block (`agentType: general-purpose`, `description: /code-review low`, `spawnDepth: 1`), the forked-skill JSON (`skillName: code-review`), a `#### Harness context` section with `##### System prompt (prompt_snapshot)`, `##### Instructions (preference files)` (5 files incl. the worktree `CLAUDE.md`), `session_context`, `date`, etc.; then `#### Conversation` with the task-prompt user turn (`` `low effort → 1 diff pass …``), three `*(encrypted thinking — signature kept)*` markers, two `**Tool call: Bash**` blocks, two `**Tool result: Bash**` blocks (one short, one ~22k chars with the diff), and a final assistant text turn.

- [ ] **Step 5: Commit only if a fix was needed**

If (and only if) verification forced a code change:

```bash
git add -A litellm-session-logs/build_cc_transcript.py
git commit -m "litellm-session-logs: fix build_cc_transcript.py on real-session verification"
```

---

### Task 4: Docs updates

**Files:**
- Modify: `litellm-session-logs/CLAUDE.md`
- Modify: `litellm-session-logs/session-log-sources.md`
- Modify: `CLAUDE.md` (root)

- [ ] **Step 1: Update `litellm-session-logs/CLAUDE.md` intro**

Change:

```markdown
Standalone pipeline (psql SQL + sh + stdlib-only Python) for pulling one
LiteLLM proxy session's logs out of the `LiteLLM_SpendLogs` Postgres table
and reconstructing a readable Markdown chat transcript from them. Not wired
into modelman or wt — run manually, per session, from this directory.
```

to:

```markdown
Standalone pipeline (psql SQL + sh + stdlib-only Python) for reconstructing
readable Markdown chat transcripts from session logs, from two sources: the
LiteLLM proxy's `LiteLLM_SpendLogs` Postgres table (steps 1–2 + the
`build_transcript.py`/`build_full_transcript.py` builders), and Claude Code's
local session record under `~/.claude/projects/` (steps 3–4, whose builder
adds the harness context and subagent threads the proxy DB cannot see).
Not wired into modelman or wt — run manually, per session, from this
directory.
```

- [ ] **Step 2: Add steps 3–4 to the Pipeline list and renumber the builders**

In `litellm-session-logs/CLAUDE.md`, after the item for `02_export_proxy_server_request.sh` (which ends with "…`json.loads`)."), insert:

```markdown
3. **`03_capture_claude_code.sh <session-id> [snapshot-dir]`** — snapshots
   a Claude Code session's local record (the main `<sessionId>.jsonl` plus
   the `<sessionId>/` directory with subagent transcripts) from
   `~/.claude/projects/<slug>/` into `session_cc_<id>/`, with a
   `capture.json` manifest. The snapshot is stable against a still-live
   session and survives `~/.claude`'s retention cleanup (default 30 days).
4. **`build_cc_transcript.py <snapshot-dir> [output.md]`** — builds a
   **full-context** transcript from a step-3 snapshot: harness context
   (system prompt, instructions, environment), the conversation with
   thinking, tool calls and results, subagent transcripts, and an appendix
   of skipped bookkeeping entry types. Design:
   `docs/superpowers/specs/2026-09-25-cc-session-transcript-builder-design.md`.
```

Then renumber the two existing builder items `3.` → `5.` and `4.` → `6.` (their bodies are unchanged).

- [ ] **Step 3: Point `session-log-sources.md` at the new consumer scripts**

In `litellm-session-logs/session-log-sources.md`, at the end of the "Working with Claude Code transcripts" section (right before its `Caveats:` line), insert:

```markdown
The local record can also be snapshotted and rendered into a full-context
transcript with `03_capture_claude_code.sh` + `build_cc_transcript.py` —
see [CLAUDE.md](CLAUDE.md) in this directory.
```

- [ ] **Step 4: Update the root `CLAUDE.md` architecture blurb**

In `CLAUDE.md` (root), change:

```markdown
- `litellm-session-logs/` — standalone pipeline (psql SQL + sh + stdlib-only Python, own CLAUDE.md) that pulls one LiteLLM proxy session's request/response logs from the Postgres `LiteLLM_SpendLogs` table and rebuilds readable chat transcripts; not wired into modelman/wt, run manually per session
```

to:

```markdown
- `litellm-session-logs/` — standalone pipeline (psql SQL + sh + stdlib-only Python, own CLAUDE.md) that rebuilds readable chat transcripts from session logs — either the Postgres `LiteLLM_SpendLogs` table (proxy-captured sessions) or Claude Code's local `~/.claude/projects/` record (via `03_capture_claude_code.sh` + `build_cc_transcript.py`); not wired into modelman/wt, run manually per session
```

- [ ] **Step 5: Lint everything (shell + markdown links)**

Run: `make lint`
Expected: all `bash -n` lines pass, `ALL LINKS OK`.

- [ ] **Step 6: Commit**

```bash
git add CLAUDE.md litellm-session-logs/CLAUDE.md litellm-session-logs/session-log-sources.md
git commit -m "docs: document Claude Code local-session capture + builder"
```

---

## Post-completion

After all tasks: the worktree holds the capture script, the builder, and doc updates; both pipeline artifacts for session `cce7a30d…` exist under `litellm-session-logs/` (gitignored). Merge/PR per superpowers:finishing-a-development-branch. If more sessions need transcripts (the "first session" wording implies more), the pipeline is now reusable per session with no further code changes.