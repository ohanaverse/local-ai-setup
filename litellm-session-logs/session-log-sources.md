# Session log sources by harness

> **What this is:** a per-harness catalog of the session records each agent keeps
> *on this machine*, and how (or whether) each one can be tied back to the
> `LiteLLM_SpendLogs` rows this directory's pipeline consumes.
>
> **Status:** `pi` is documented below (verified 2026-09-25 against pi
> `@earendil-works/pi-coding-agent` as installed at
> `/opt/homebrew/lib/node_modules/`, and the live `LiteLLM_SpendLogs` table —
> 45,998 rows at the time). Every other harness is a stub — see
> [Filling in a harness](#filling-in-a-harness).
>
> **Why this exists:** the pipeline in [CLAUDE.md](CLAUDE.md) reconstructs a
> transcript from what the *proxy* saw. The proxy only sees what the client
> chose to send it, and it stores headers/prompts only when prompt storage was
> on at capture time. Each harness also writes its own record locally, which is
> usually richer (thinking blocks, full tool arguments, local paths, errors) and
> always present. Knowing both sides lets you cross-check one against the other
> — or fall back to the harness's own record when the proxy side is empty.

## Harness index

| Harness | wt agent name | Own session record | Location | Status |
|---|---|---|---|---|
| pi | `pi` (`pi-wt`) | JSONL transcript tree, v3 | `~/.pi/agent/sessions/<cwd-slug>/<ts>_<uuid>.jsonl` | **documented below** |
| Claude Code | `claude` (`claude-wt`) | JSONL transcripts | TODO — lead: `~/.claude/sessions/*.json` (observed written during a run, unverified) | stub |
| Codex CLI | `codex` (`codex-wt`) | TODO (prints `session id: <uuid>` on stdout) | TODO | stub |
| GitHub Copilot CLI | `copilot` (`copilot-wt`) | TODO (prints `Resume copilot --resume=<id>`) | TODO | stub |
| OpenCode | `opencode` (`opencode-wt`) | TODO (sends an `x-opencode-session` header) | TODO | stub |
| Antigravity CLI | `agy` (`agy-wt`) | TODO | TODO | stub |

The leads in the stub rows — Codex printing `session id:`, Copilot printing
`Resume copilot --resume=<id>` (both from the `wt smoke` output captured in
`smoke_gptoss.json.log` at the repo root, 2026-09-25), and OpenCode's
`x-opencode-session` header (seen in pi's provider-attribution table) — are
*leads*, not verified locations.

---

## pi

### Where pi writes

The anchor is the **agent directory**: `~/.pi/agent` by default, relocatable
with `PI_CODING_AGENT_DIR` (or the SDK's `agentDir` option). Everything below is
expressed relative to it.

| Path | What it is | When written |
|---|---|---|
| `<agent-dir>/sessions/<cwd-slug>/<timestamp>_<uuid>.jsonl` | **The session transcript** — the real record | Continuously, appended as the conversation proceeds |
| `<agent-dir>/crashes.json` | Last 5 unhandled crashes, pruned after 7 days (`timestamp`, `version`, `kind`, `message`, `stack`, `sessionFile`, `cwd`) | Only on a crash |
| `<agent-dir>/pi-debug.log` | Rendered terminal lines + current session messages | Only when you run `/debug` |
| `<agent-dir>/pi-tui-debug.log` | Full-redraw reasons for the TUI | Only with `PI_TUI_DEBUG_REDRAW=1` |
| `/tmp/tui/render-<ts>-<rand>.log` | Per-render layout dump (hardcoded path) | Only with `PI_TUI_DEBUG=1` |
| `<agent-dir>/pi-tui-crash.log` | Dump of all rendered lines when one exceeded terminal width | Only on that crash (falls back to `os.tmpdir()`) |
| `$TMPDIR/pi-bash-*.log` | Full untruncated output of a bash tool call that was truncated for the model (the path in `details.fullOutputPath`; on macOS `$TMPDIR` is `/var/folders/…/T`, not `/tmp`) | Only when output exceeded the truncation limit |

**There is no general application log.** Pi does not write a rolling log of HTTP
requests, retries, or provider responses. The session transcript *is* the log;
`crashes.json` is the only other file that accumulates across runs.

Two flags disable session persistence entirely, in which case nothing above is
written: CLI `--no-session` (in-memory session) and the SessionManager `persist`
option. The `sessionDir` setting (and `PI_CODING_AGENT_SESSION_DIR` /
`--session-dir`) moves the transcript directory; relative paths resolve from the
working directory.

### Transcript layout

Path encoding: the working directory with its leading separator removed and
`/`, `\`, and `:` replaced by `-`, wrapped in `--`. So this repo's sessions live
in `~/.pi/agent/sessions/--Users-keith-github-ohanaverse-local-ai-setup--/`.

File name: `<ISO-ish timestamp>_<session-uuid>.jsonl`, e.g.
`2026-09-25T19-57-37-827Z_01a0da25-12a3-76a4-8dbf-1b66cdfce0e4.jsonl`. The
session id is a generated UUID (UUIDv7-shaped in every local example) unless a
caller supplied one via the SDK or `--session-id`.

Format: **JSONL, one entry per line, forming a tree** via `id`/`parentId`.
Line 1 is always a `session` header (metadata only, no `id`/`parentId`). The
current version is 3; v1 (linear sequence) and v2 (tree) are auto-migrated on
load.

Entry types actually present in this repo's 234 sessions:

| `type` | Count | Notes |
|---|---:|---|
| `session` | 234 | Header |
| `message` | 16,522 | Conversation — the bulk of the file |
| `thinking_level_change` | 244 | |
| `model_change` | 234 | |
| `custom_message` | 21 | Extension-injected, participates in LLM context |
| `session_info` | 9 | `/name`, `--name` |
| `context_edit` | 9 | Append-only edit affecting *future* model context only |
| `compaction` | 6 | Summary replacing earlier context |

Message roles inside `message` entries: `system`, `user`, `assistant`,
`toolResult`. Content blocks: `text`, `thinking`, `toolCall` (and `image` when
an image was attached).

### What the transcript contains

Complete, verbatim, and replayable:

- **User messages** — text as typed, images as base64 `ImageContent`.
- **The full system prompt** — not a rendered string but the component
  `sections` (`preamble`, `tools`, `rules`, `docs`, `project_context`, `skills`,
  `cwd`) plus the `toolsAdded` tool declarations. The current session's first
  system entry holds **33,166 characters** across those sections. Later prompt
  or tool changes are appended as patch system messages that mutate sections by
  name and list `toolsAdded`/`toolsRemoved`; replaying them in order yields the
  live prompt. There is no separate "prompt state" entry.
- **Assistant output** — `text` blocks, plus `thinking` blocks with their
  `thinkingSignature` (5,166 signature-bearing thinking blocks across this
  repo's sessions).
- **Tool calls** — name, id, and the **full argument object**.
- **Tool results** — content plus tool-specific `details`.
- **Provider metadata** — `api`, `provider`, `model`, `responseId`,
  `rawStopReason`, `stopReason`, and `usage` (tokens + cost).
- **Errors and aborts.** 129 assistant entries with `stopReason: "error"`
  (carrying `errorMessage`) and 24 with `stopReason: "aborted"` are persisted in
  this repo's sessions. An aborted response keeps the partial text streamed
  before the abort.
- **Structural events** — compaction summaries, branch summaries, labels,
  session names, model/thinking-level changes, extension state (`custom`).
- **Raw history is never rewritten.** Compaction and `context_edit` change the
  *model context* only; the original entries remain in the file untouched.

### What the transcript does **not** contain

- **Full untruncated tool output.** bash/grep/find cap at **2,000 lines or
  50 KB**, whichever hits first. The entry stores the truncated text plus
  `details.truncation` and a `details.fullOutputPath` pointing at
  `$TMPDIR/pi-bash-*.log`. Observed worst case in this repo:
  `totalBytes: 1,079,213` stored as `outputBytes: 27,169` (31 lines → 6). The
  rest exists only in the temp file, which is not part of the session and is
  subject to OS cleanup. Custom tools are not covered by this limit — a
  `web_search` result of **150,910 characters** is stored in full.
- **Request parameters** — temperature, max_tokens, top_p, tool_choice, seed,
  headers, endpoint, API key. There is no request log to read them from.
- **Raw provider payloads** — only the normalized message survives. Streaming
  deltas are discarded; `"pending"` (in-flight) assistant messages are never
  persisted, so the file holds one final message per turn.
- **Redacted thinking** — a `redacted` thinking block may carry empty
  `thinking` text with only an encrypted `thinkingSignature`. (None observed in
  the local data; documented behavior.)
- **Anything never sent** — queued/undelivered steering messages, prompts typed
  but not submitted, and sessions run with `--no-session`.

### Verified figures (2026-09-25)

Machine-wide: **847 session files across 133 working-directory slugs, 251 MB.**
Largest directories: this repo 234, `agent-worktree` 138, `mymem` 71,
`agent-toolkit` 59, `gitbuddy` 33, `modelman` 29.

```bash
# Machine-wide inventory
find ~/.pi/agent/sessions -name '*.jsonl' | wc -l          # 847
find ~/.pi/agent/sessions -maxdepth 1 -type d | wc -l      # 134 (133 slugs + root)
du -sh ~/.pi/agent/sessions                                # 251M

# Per-workdir counts (this repo: 234)
find ~/.pi/agent/sessions -maxdepth 1 -mindepth 1 -type d \
  | while read -r d; do printf '%5d %s\n' "$(find "$d" -name '*.jsonl' | wc -l)" "$d"; done \
  | sort -rn | head -6
```

### pi → the LiteLLM pipeline

**Conclusion: pi sessions cannot currently be grouped or exported by this
pipeline.** Both pipeline steps key on `LiteLLM_SpendLogs.session_id`
(`01_export_session_logs.sql`, `02_export_proxy_server_request.sh`), and pi's
session UUID never lands in that column.

Three independent observations, in increasing order of strength:

1. **No pi request body has been captured.** Of 45,998 spend rows, 1,595 have a
   populated `proxy_server_request`, and every one of those carries a
   `claude-cli/2.1.275…277` or `Python-urllib/3.13` user agent. Searches for
   both plausible pi user-agent strings (`pi (darwin`, the value
   `getPiUserAgent()` produces for provider calls, and `pi-coding-agent`)
   return **0 rows**. Caveat: this says nothing about the ~44k rows with no
   captured body.
2. **No pi session UUID appears anywhere in the `session_id` column** — 0 of
   847, checked against all 15,658 distinct non-null values, covering all
   45,998 rows.
3. **Mechanically, pi cannot deliver a session id the proxy will accept.** pi
   passes its session id via *headers*, never in the request body, and the
   header name never matches what LiteLLM reads:
   - `openai-completions` (pi's API for the local providers) and
     `anthropic-messages` send them only when the model's compat says so:
     `compat.sendSessionAffinityHeaders`, default `isOpenRouter`. The `litellm`
     provider in `~/.pi/agent/models.json` (`http://localhost:4000/v1`) declares
     no `compat`, so **no session header is sent at all**. Names when enabled:
     `x-session-id` (openrouter format), or `session_id` +
     `x-client-request-id` + `x-session-affinity` (openai format).
   - `openai-responses` sends them whenever a session id is present at all,
     with the same names.
   - LiteLLM's generic extractor matches `^x-.+-session-id$` and its explicit
     list is `{x-litellm-trace-id, x-litellm-session-id}`. Checked against that
     regex: `x-claude-code-session-id` → match; `x-session-id`, `session_id`,
     `x-session-affinity`, `x-client-request-id` → **no match** (`x-session-id`
     has nothing between `x-` and `-session-id`).

   Provider catalogs can flip `sendSessionAffinityHeaders` per model (the
   bundled Cloudflare and Baseten catalogs do), and `models.json` can override
   `compat` — so the mechanism exists; it is simply not active for any provider
   configured on this box. For contrast, Claude Code sends
   `x-claude-code-session-id`, which matches, and that is why it groups
   correctly today.

Reproduce (2) — the check that covers every row:

```bash
find ~/.pi/agent/sessions -name '*.jsonl' -exec basename {} .jsonl \; \
  | sed 's/^[^_]*_//' | sort -u > /tmp/pi-ids.txt          # 847 ids
psql litellm -tAc 'select session_id from "LiteLLM_SpendLogs"
                   where session_id is not null group by 1' \
  | sort -u > /tmp/ll-ids.txt                              # 15658 ids
comm -12 /tmp/pi-ids.txt /tmp/ll-ids.txt | wc -l           # 0
```

Implications for the pipeline:

- pi turns, if routed through the proxy and captured, fall into
  `build_full_transcript.py`'s **heuristic** thread-grouping path
  (`find_anchor`), not the ground-truth `x-claude-code-agent-id` path — which it
  should, since pi has no equivalent sub-agent header either.
- More importantly there is no proxy-side key to *isolate* a pi session with, so
  `01_export_session_logs.sql` has nothing to filter on. Until pi's request
  carries a header LiteLLM recognizes (`x-pi-session-id` /
  `x-litellm-session-id` would both match), the pi transcript in
  `~/.pi/agent/sessions/` is the only per-session record available, and it
  should be read directly instead of exported from Postgres.

Open items (not investigated, no fix verified):

- Whether pi can be made to emit a recognized header. `models.json` supports a
  provider-level `headers` map whose values may use env interpolation or a
  leading `!command` that runs at request time — a per-session value would have
  to be derived there, which is unproven and adds a subprocess per request.
- Whether any pi traffic has ever gone through the proxy at all. The `litellm`
  provider exists in `~/.pi/agent/models.json` with 49 models, but the spend
  table shows no trace of it.

### Working with pi transcripts

```bash
D=~/.pi/agent/sessions/--Users-keith-github-ohanaverse-local-ai-setup--

# Most recent session in this repo
ls -t "$D"/*.jsonl | head -1

# User + assistant text of the newest session, in order (skips thinking and tool plumbing)
python3 - "$D" <<'EOF'
import json, sys, glob, os
newest = max(glob.glob(os.path.join(sys.argv[1], "*.jsonl")), key=os.path.getmtime)
for line in open(newest, errors="replace"):
    if not line.strip(): continue
    e = json.loads(line)
    m = e.get("message")
    if not isinstance(m, dict) or m.get("role") not in ("user", "assistant"): continue
    for b in (m.get("content") or []):
        if isinstance(b, dict) and b.get("type") == "text":
            print(f"[{m['role']}] {b['text'][:400]}")
EOF

# Sessions that hit tool-output truncation (JSONL is compact: no space after ':')
grep -l '"truncated":true' "$D"/*.jsonl

# Crash history
python3 -m json.tool ~/.pi/agent/crashes.json
```

Caveats when parsing: entries are a **tree**, not a list, so a naive read
includes abandoned branches — walk `parentId` from the leaf (or from the last
entry) for the active conversation. `context_edit` and `compaction` mean the
raw file is *not* the model's context; it is the superset. Timestamps appear
twice: ISO 8601 on the entry, Unix milliseconds inside the message.

### Reference

- Full format spec: `/opt/homebrew/lib/node_modules/@earendil-works/pi-coding-agent/docs/session-format.md`
- Message/content-block types: `…/docs/message-types.md`
- Agent directory layout: `…/docs/configuration.md`
- The `/debug` command and its log: `…/docs/usage.md`

---

## Filling in a harness

The method used for pi, in order (each step is cheap and falsifiable):

1. **Launch the agent once with a trivial prompt** (`wt smoke --json` does this
   for several harnesses at once) and watch for a session identifier in its
   stdout — Codex prints `session id: <uuid>`, Copilot prints
   `Resume copilot --resume=<id>`.
2. **Find files the run created**: `find ~ -maxdepth 4 -mmin -2 -type f -
   name '*.json*' 2>/dev/null | grep -v Library/Caches`, then narrow to the
   harness's own config directory. (Keep `-maxdepth` — an unbounded scan of
   `$HOME` is not cheap.)
3. **Check whether the harness's identity reaches the proxy**:
   ```bash
   psql litellm -tAc "select proxy_server_request::jsonb->'litellm_metadata'->'headers'->>'user-agent',
                            proxy_server_request::jsonb->'litellm_metadata'->'headers'->>'x-claude-code-session-id'
                     from \"LiteLLM_SpendLogs\"
                     where proxy_server_request::text ilike '%headers%' limit 5"
   ```
   If the harness's session header does not match LiteLLM's
   `^x-.+-session-id$` pattern, record that in the harness's section — it means
   its sessions are not isolatable through this pipeline.
4. **Characterize coverage**: what it stores, what it truncates, and what it
   omits — the same two headings used for pi above.
5. **Add the row to the index table** and mark it verified with a date.

## Related

- Pipeline overview and data-model caveats: [CLAUDE.md](CLAUDE.md)
- The same `LiteLLM_SpendLogs` table viewed as aggregate spend:
  [docs/guides/07-usage-and-spend.md](../docs/guides/07-usage-and-spend.md)
- Which harnesses wt can launch, and how they are routed:
  [docs/guides/06-wt-agents-and-models.md](../docs/guides/06-wt-agents-and-models.md)
