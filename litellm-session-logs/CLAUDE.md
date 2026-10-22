# CLAUDE.md

Standalone pipeline (psql SQL + sh + stdlib-only Python) for pulling one
LiteLLM proxy session's logs out of the `LiteLLM_SpendLogs` Postgres table
and reconstructing a readable Markdown chat transcript from them. Not wired
into wt — run manually, per session, from this directory.

The pipeline can only see sessions whose harness sends a session id LiteLLM
recognizes. Which harnesses do, and where each keeps its own local session
record instead: [session-log-sources.md](session-log-sources.md). Five harnesses
are documented there (pi, Claude Code, Codex, Copilot, OpenCode — only Claude
Code's sessions group), Antigravity is a partial stub, and the exact rule
LiteLLM applies to a request's headers is quoted in that file under
"How LiteLLM decides a session id".

## Pipeline

1. **`01_export_session_logs.sql`** — the query for one `session_id`,
   ordered by time: `request_id, startTime, status, request_duration_ms,
   prompt_tokens, completion_tokens, spend, messages, response`. The file
   is a plain `SELECT`, so `psql -f 01_export_session_logs.sql` prints an
   aligned text table, which is good for a look at the rows and is not
   what steps 3-4 read. Both builders `json.load` a JSON array of row
   objects; to write that as `session_logs_<id>.json`, run the same query
   wrapped in `json_agg`, unaligned and tuples-only (`-At`), from stdin
   (psql does not substitute `:session_id` in a `-c` command):
   ```
   psql "${LITELLM_DATABASE_URL:-postgresql://keith@localhost:5432/litellm}" \
     -At -v session_id="'<session-id>'" > session_logs_<id>.json <<'SQL'
   SELECT coalesce(json_agg(t ORDER BY t."startTime"), '[]')
   FROM (
     SELECT request_id, "startTime", status, request_duration_ms,
            prompt_tokens, completion_tokens, spend, messages, response
     FROM "LiteLLM_SpendLogs"
     WHERE session_id = :session_id
   ) t;
   SQL
   ```
2. **`02_export_proxy_server_request.sh <session_id> [output_file]`** —
   dumps the `proxy_server_request` column (the real request body: messages,
   system prompt, tool results) to newline-delimited JSON. Only useful if
   `store_prompts_in_spend_logs` was on at capture time; otherwise this
   column is `{}` too. Uses CSV format with control-character quote/delimiter
   (`\x01`/`\x02`) for `\copy` specifically to avoid Postgres's default TEXT
   format double-escaping backslashes inside the JSON text (which would break
   `json.loads`).
3. **`build_transcript.py <session_logs_<id>.json>`** — builds an
   **assistant-only** transcript from the JSON export alone (`response`
   column). Use when `messages` is empty (`{}`, the normal case for
   standard chat completions) and you don't have `proxy_server_request`.
4. **`build_full_transcript.py <session_logs.json> <proxy_requests.ndjson>
   [output.md]`** — builds a **two-sided** transcript by joining the JSON
   export's `response` column against the ndjson export's
   `proxy_server_request.messages`. Requires step 2's output.

`header_probe.py` is a separate one-off tool (not in the numbered pipeline): an
`http.server` that logs every header a client sends, to learn what a harness
sends; see [session-log-sources.md](session-log-sources.md#transcript-builder-details).

Run scripts with `python3 <script>.py ...` — no venv/dependencies beyond the
stdlib. Exception: `antigravity/extract_descriptors.py` (one-off: extracts
protobuf FileDescriptorProtos from the `agy` binary to decode Antigravity
`step_payload` blobs) needs `protobuf`; see the Antigravity section of
[session-log-sources.md](session-log-sources.md).

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
  (main conversation, sub-agents, a judge process). `build_full_transcript.py`
  keys threads on the `x-claude-code-agent-id` header, else infers them by
  tool-call-ID/response-text matching; threads under `MAIN_THREAD_MIN_SIZE`
  (5) turns are pooled as "Isolated / short calls". Details:
  [session-log-sources.md](session-log-sources.md#transcript-builder-details).

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
  covers the same `LiteLLM_SpendLogs` table via `wt stats`.