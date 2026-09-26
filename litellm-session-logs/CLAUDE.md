# CLAUDE.md

Standalone pipeline (psql SQL + sh + stdlib-only Python) for reconstructing
readable Markdown chat transcripts from session logs, from two sources: the
LiteLLM proxy's `LiteLLM_SpendLogs` Postgres table (steps 1–2 + the
`build_transcript.py`/`build_full_transcript.py` builders), and Claude Code's
local session record under `~/.claude/projects/` (steps 3–4, whose builder
adds the harness context and subagent threads the proxy DB cannot see).
Not wired into modelman or wt — run manually, per session, from this
directory.

The pipeline can only see sessions whose harness sends a session id LiteLLM
recognizes. Which harnesses do, and where each keeps its own local session
record instead: [session-log-sources.md](session-log-sources.md). Five harnesses
are documented there (pi, Claude Code, Codex, Copilot, OpenCode — only Claude
Code's sessions group), Antigravity is a partial stub, and the exact rule
LiteLLM applies to a request's headers is quoted in that file under
"How LiteLLM decides a session id".

## Pipeline

1. **`01_export_session_logs.sql`** — runs against the live DB, dumps
   `request_id, startTime, status, request_duration_ms, prompt_tokens,
   completion_tokens, spend, messages, response` for one `session_id`,
   ordered by time, to `session_logs_<id>.json`.
   ```
   psql postgresql://keith@localhost:5432/litellm \
     -v session_id="'<session-id>'" \
     -f 01_export_session_logs.sql \
     > session_logs_<id>.json
   ```
   (`LITELLM_DATABASE_URL` overrides the connection string, as in step 2.)
2. **`02_export_proxy_server_request.sh <session_id> [output_file]`** —
   dumps the `proxy_server_request` column (the real request body: messages,
   system prompt, tool results) to newline-delimited JSON. Only useful if
   `store_prompts_in_spend_logs` was on at capture time; otherwise this
   column is `{}` too. Uses CSV format with control-character quote/delimiter
   (`\x01`/`\x02`) for `\copy` specifically to avoid Postgres's default TEXT
   format double-escaping backslashes inside the JSON text (which would break
   `json.loads`).
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
5. **`build_transcript.py <session_logs_<id>.json>`** — builds an
   **assistant-only** transcript from the JSON export alone (`response`
   column). Use when `messages` is empty (`{}`, the normal case for
   standard chat completions) and you don't have `proxy_server_request`.
6. **`build_full_transcript.py <session_logs.json> <proxy_requests.ndjson>
   [output.md]`** — builds a **two-sided** transcript by joining the JSON
   export's `response` column against the ndjson export's
   `proxy_server_request.messages`. Requires step 2's output.

`header_probe.py` is a separate one-off debugging tool, not part of the
numbered pipeline: a minimal `http.server` handler that logs every header a
client sends and answers with a JSON shape permissive enough to satisfy an
OpenAI chat-completions or legacy completions caller. Used to determine what
headers a harness actually sends when the binary can't be `strings`-grepped
(e.g. Copilot CLI's compressed Node SEA build) by pointing the harness's
provider-base-URL override at it instead of the real provider. See the
Copilot section of [session-log-sources.md](session-log-sources.md) for the
worked example.

Run scripts with `python3 <script>.py ...` — no venv/dependencies beyond the
stdlib.

## Key data-model caveats (baked into the scripts, not obvious from output alone)

- The `messages` column in the SQL export is **always `{}`** for standard
  chat completions (non-realtime); only `_arealtime` calls populate it. The
  user/tool-result side is only recoverable via `proxy_server_request`
  (step 2), and only if `store_prompts_in_spend_logs` was enabled when the
  session was captured.
- **Failure rows** (`status: failure`) have empty `response` and
  `request_duration_ms: 0` with no captured error message/exception type.
  Both transcript builders emit an `[ERROR]` marker; `build_transcript.py`
  additionally *infers* a likely cause from token/spend counts (zero
  everything → rejected before reaching the provider, e.g. a proxy-side
  budget/limit; nonzero → provider was reached, e.g. mid-stream timeout) —
  this inference is not logged fact, and `build_full_transcript.py` does
  not attempt it.
- **Truncation**: LiteLLM's `MAX_STRING_LENGTH_PROMPT_IN_DB` truncates long
  `content`/`reasoning_content` before DB write. Truncated spots carry an
  inline `(litellm_truncated skipped N chars ...)` marker copied verbatim
  from the source — don't treat it as a bug in these scripts.
- A single `session_id` can span **multiple interleaved agent threads**
  (a main Claude Code conversation, spawned sub-agents, and/or a separate
  per-action judge process making one-shot calls). `build_full_transcript.py`
  handles this by grouping turns into threads two ways, in priority order:
  1. **Ground truth**: the `x-claude-code-agent-id` request header (echoed
     into `proxy_server_request.litellm_metadata.headers`) is unique per
     spawned sub-agent and used directly as the thread key
     (`agent:<id>` in output).
  2. **Heuristic fallback** (calls without that header, e.g. a non-Claude-Code
     judge process): threads are inferred by matching tool-call IDs (stable
     across the Anthropic-request/OpenAI-response translation litellm does)
     or exact response text against previously-seen turns, walking newest
     message first (`find_anchor`).
  Threads with `>= MAIN_THREAD_MIN_SIZE` (5) turns render as full linear
  conversations; smaller ones are pooled into one "Isolated / short calls"
  section, since a short thread's full message list *is* the whole
  conversation rather than an incremental diff.

## Working with generated outputs

- Pipeline outputs (`session_logs_*.json`, `*-pretty.json`, `*.ndjson`,
  `*-transcript*.md`) are **gitignored** (`session_*` in `.gitignore`) —
  they are working artifacts for specific sessions being analyzed, not
  maintained files. Regenerate them by re-running the pipeline rather than
  patching the Markdown/JSON in place.
- Artifacts for the sessions already analyzed live in
  `~/tmp/litellm-session-logs/` (the pipeline moved from there into the
  repo on 2026-09-25; the artifacts stayed put). Point the builders at
  those files by path, or write fresh exports alongside the scripts — the
  `session_*` gitignore keeps them untracked.
- These files can be large (the `.ndjson` and `-transcript-full.md` files
  run tens to hundreds of MB) — prefer targeted `grep`/`jq`/line-range reads
  over loading a whole file when investigating a specific turn or thread.
- Related guide: [docs/guides/07-usage-and-spend.md](../docs/guides/07-usage-and-spend.md)
  covers the same `LiteLLM_SpendLogs` table via `modelman usage report`.