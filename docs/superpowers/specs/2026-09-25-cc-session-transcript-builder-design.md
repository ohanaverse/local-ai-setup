# Claude Code Local-Session Capture + Full-Context Transcript Builder — Design

Date: 2026-09-25
Worktree: `cc-session-transcripts`

## Problem

The `litellm-session-logs/` pipeline reconstructs transcripts from the
LiteLLM proxy's `LiteLLM_SpendLogs` table, but it only sees sessions that
routed through the local proxy — and even then, only when prompt storage was
on. Session `cce7a30d-9fc2-46d6-8388-bc4d55df6917` (a `/code-review low` run
in the `fix-config-toml-race-143` worktree) has **zero rows in the DB**: it
went straight to the provider. Claude Code's local record under
`~/.claude/projects/` is the only source for it — and per
[session-log-sources.md](../../../litellm-session-logs/session-log-sources.md),
that record is "the real record": full system prompt, instructions,
tool calls and results, thinking, and one file per sub-agent.

Nothing currently consumes that local record. This adds a capture step and a
builder that turn any Claude Code session's local files into a full-context
Markdown transcript.

## Goals

- Given a Claude Code session ID, snapshot ("capture") its raw local record
  into a stable, gitignored directory — protecting it against
  `~/.claude` retention cleanup (default 30 days) and against a live session
  appending mid-build.
- Build a structured, full-fidelity Markdown transcript from the snapshot:
  everything the harness injected plus everything the model said and did.
- Fit the existing pipeline's conventions: numbered export step +
  `build_*.py` builder, stdlib-only Python, `sh` for shell, artifacts
  gitignored via the `session_*` pattern.

## Non-goals

- Joining LiteLLM DB rows (tokens/spend per turn) — this builder is
  local-record-only by decision.
- A test framework for `litellm-session-logs/` (the directory is
  deliberately stdlib-only, manual-run; verification is against real
  sessions plus `make lint-shell`).
- Recovering content Claude Code itself truncated inline
  (`[truncated, showing last 8KiB]` etc.) — those markers are copied
  verbatim; the raw text is not recoverable from any source.

## Interface

```console
./03_capture_claude_code.sh <session-id> [snapshot-dir]
python3 build_cc_transcript.py <snapshot-dir> [output.md]
```

Defaults follow the existing pipeline (artifacts land in cwd):

- Snapshot: `session_cc_<id>/` containing
  - `<session-id>.jsonl` — the main session transcript
  - `<session-id>/` — the session's directory (subagents with their
    `.meta.json` and any forked-skill files), copied only if it exists
- Output: `session_cc_<id>-transcript.md`

Both names start with `session_`, so the existing `session_*` pattern in
`litellm-session-logs/.gitignore` keeps them untracked.

## Component 1 — `03_capture_claude_code.sh`

POSIX `#!/bin/sh`, listed in the root `Makefile`'s `SHELL_SCRIPTS`
(so `make lint-shell` covers it), same style as
`02_export_proxy_server_request.sh`.

Behavior:

1. Locate the session: `find ~/.claude/projects -maxdepth 2 -name
   '<session-id>.jsonl'`.
   - **Zero matches** → error with a hint that the session may not exist or
     ran with persistence disabled; exit 1.
   - **Multiple matches** (rare) → copy the newest by mtime, print a
     warning listing all matches so the user can intervene if the guess
     is wrong.
2. Copy with `cp -Rp` (preserves mtimes — stable snapshot):
   the main `.jsonl`, and the `<session-id>/` directory if present.
   `cp -Rp` into `session_cc_<id>/` keeps the internal layout, so the
   snapshot mirrors the source exactly.
3. Print an inventory (paths + sizes) when done.

## Component 2 — `build_cc_transcript.py`

Stdlib-only Python, mirroring the existing builders. Input: the snapshot
dir (must contain exactly one top-level `*.jsonl`). Output structure:

```markdown
# Claude Code session <id>
Source slug, capture info, file inventory, model(s), aggregate usage,
and a source-tally table: per-entry-type and per-content-block-type counts
computed from the raw files (doubles as a correctness check against jq).

## 1. Harness context
Attachments that appear before the first user message, in
first-appearance order, verbatim:
- System prompt (`prompt_snapshot` → `systemPrompt` array, ~29 KB)
- Instructions (CLAUDE.md / preference files, full contents)
- session_context, date, environment, and any other leading attachments,
  rendered generically (JSON pretty-printed in fenced blocks)
Later re-snapshots of the same attachment type: identical →
"(unchanged)"; different → rendered in full at its position.

## 2. Conversation
Entries in file order (= append order = chronological):
- **user turns**: content rendered verbatim when a string (including
  <task-notification> XML); tool_result blocks rendered under their
  matched tool_use call
- **assistant turns**: header line with model + per-turn usage (input,
  cache_read, cache_creation, output tokens); text blocks verbatim;
  thinking blocks as blockquotes; tool_use blocks as
  `tool name (id)` + full argument object in fenced JSON
- **mid-conversation attachments** (hook_success,
  hook_additional_context, …): rendered inline at their position
- **system entries** (stop_hook_summary, turn_duration, …): one-line
  metadata notes

## 3. Subagents
One section per `subagents/agent-<id>.jsonl`, headed by its `.meta.json`
(agentType, description, name, spawnDepth, requestShape) and any
forked-skill info; body rendered with the same rules as §2.

## Appendix: skipped harness bookkeeping
Table of non-transcript entry types (last-prompt, atis-latch, mode,
permission-mode, queue-operation, file-history-snapshot, cost-state,
…) with counts — acknowledged, not rendered.
```

Rendering rules (from `session-log-sources.md`'s verified data model):

- `message.content` is either a plain string or a list of blocks.
- Thinking blocks with text render as blockquotes; thinking blocks with
  only a `signature` render as `(encrypted thinking — signature kept)`.
- Image blocks render as `[image, N bytes omitted]`.
- Claude Code's own inline truncation markers are copied verbatim.
- `tool_result` blocks may reference a `tool_use` from the previous
  assistant turn by id; match and render together when possible, else
  render standalone with the raw id.

## Error handling

- Snapshot dir without a top-level `*.jsonl` → clear error, exit 1.
- Multiple top-level `*.jsonl` → error listing them, exit 1.
- Unparseable JSON line → visible `[unparseable line N]` marker in the
  output, build continues.
- Unknown entry or attachment types → counted in the appendix tally,
  never fatal.

## Docs & lint updates

- `litellm-session-logs/CLAUDE.md`: pipeline section gains the capture
  step and the builder; intro line updated to say the pipeline also
  builds transcripts from Claude Code's **local** record (the LiteLLM DB
  path is unchanged).
- `session-log-sources.md` (Claude Code section): pointer to the new
  consumer scripts.
- Root `CLAUDE.md`: `litellm-session-logs/` architecture blurb updated
  to match.
- Root `Makefile`: `litellm-session-logs/03_capture_claude_code.sh`
  added to `SHELL_SCRIPTS`.

## Testing & verification

No new test framework. Verification for this feature:

1. `make lint-shell` passes (covers the new shell script).
2. Capture + build against session
   `cce7a30d-9fc2-46d6-8388-bc4d55df6917`; confirm:
   - snapshot inventory matches the source files (44-entry main jsonl +
     `subagents/agent-a41b7612d0d197840.jsonl`, 35 entries);
   - the transcript header's source-tally table matches independent
     `jq` counts over the raw files;
   - spot-check each section (harness context, conversation, subagent,
     appendix) against the raw JSONL.