# Session log sources by harness

> **What this is:** a per-harness catalog of the session records each agent keeps
> *on this machine*, and how (or whether) each one can be tied back to the
> `LiteLLM_SpendLogs` rows this directory's pipeline consumes.
>
> **Status:** five of the seven `wt` harnesses are documented below. `pi` was
> verified 2026-09-25 against pi `@earendil-works/pi-coding-agent` as installed
> at `/opt/homebrew/lib/node_modules/`; Claude Code, Codex CLI, GitHub Copilot
> CLI and OpenCode were verified the same day against the versions in the
> [harness index](#harness-index) and the live `LiteLLM_SpendLogs` table —
> 46,534 rows at the time. Copilot's header set was confirmed the same day by a
> live capture ([Copilot → the LiteLLM pipeline](#copilot--the-litellm-pipeline)).
> Antigravity is a **partial stub**: its store is located, its payload format
> identified, and payload decoding now works (validated against every local
> `step_payload`, 0 failures) — but whether any session header reaches the
> proxy is still unverified ([Antigravity CLI](#antigravity-cli)). `shell`
> keeps no session record at all.
>
> **Why this exists:** the pipeline in [CLAUDE.md](CLAUDE.md) reconstructs a
> transcript from what the *proxy* saw. The proxy only sees what the client
> chose to send it, and it stores headers/prompts only when prompt storage was
> on at capture time. Each harness also writes its own record locally, which is
> usually richer (thinking blocks, full tool arguments, local paths, errors) and
> always present. Knowing both sides lets you cross-check one against the other
> — or fall back to the harness's own record when the proxy side is empty.

## Harness index

| Harness | wt agent name | Own session record | Location | Session id → `SpendLogs.session_id` | Status |
|---|---|---|---|---|---|
| pi | `pi` (`pi-wt`) | JSONL transcript tree, v3 | `~/.pi/agent/sessions/<cwd-slug>/<ts>_<uuid>.jsonl` | **No** — 0 of 847 | documented below |
| Claude Code | `claude` (`claude-wt`) | JSONL transcript tree + one file per sub-agent | `~/.claude/projects/<slug>/<sessionId>.jsonl` | **Yes** — header captured on 1,277 rows and equal to the column | documented below |
| Codex CLI | `codex` (`codex-wt`) | JSONL rollout, linear | `~/.codex/sessions/YYYY/MM/DD/rollout-<ISO>-<uuid>.jsonl` | **No** — 0 of 211 | documented below |
| GitHub Copilot CLI | `copilot` (`copilot-wt`) | JSONL event tree | `~/.copilot/session-state/<uuid>/events.jsonl` | **No** — 1 of 262, and that one is LiteLLM's random fallback | documented below |
| OpenCode | `opencode` (`opencode-wt`) | SQLite, relational | `~/.local/share/opencode/opencode.db` | **No** — 0 of 47 | documented below |
| Antigravity CLI | `agy` (`agy-wt`) | SQLite, one DB per conversation, protobuf payloads | `~/.gemini/antigravity-cli/conversations/<uuid>.db` | **Unverified** | partial stub |
| Shell | `shell` (`shell-wt`) | none | — | n/a | n/a |

The three leads the stub rows used to carry — Codex printing `session id: <uuid>`,
Copilot printing `Resume copilot --resume=<id>`, OpenCode's `x-opencode-session`
header — have all now been run down, and two of them did not survive contact.
Codex's id is never sent; OpenCode's header exists but does not match LiteLLM's
pattern ([OpenCode → the LiteLLM pipeline](#opencode--the-litellm-pipeline)); and
Copilot's resume line names a session the CLI does not deliver to the proxy
([Copilot → the LiteLLM pipeline](#copilot--the-litellm-pipeline)). Those leads
came from `wt smoke` output captured in `smoke_gptoss.json.log` at the repo root,
a transient artifact that is no longer present.

---

## How LiteLLM decides a session id

Every verdict below reduces to one question: does the harness's session identity
reach the proxy in a shape LiteLLM recognises? The rule chain is read from the
installed proxy (`…/litellm/proxy/litellm_pre_call_utils.py`,
`get_chain_id_from_headers`) and is the contract each harness is measured
against, so it is worth being exact.

1. **The explicit LiteLLM headers**, highest priority: `x-litellm-trace-id`, then
   `x-litellm-session-id`.
2. **Any `x-<vendor>-session-id` header** whose value matches
   `^[a-zA-Z0-9_\-]{8,}$`, matched case-insensitively by `^x-.+-session-id$`. The
   `.+` needs **at least one character**, so `x-session-id` does *not* match
   (nothing between `x-` and `-session-id`) while `x-claude-code-session-id`
   does.
3. **Anthropic `metadata.user_id`** — a `user_id` object's `session_id` field, or
   the substring after `_session_` in a string `user_id`.
4. **W3C propagation** — the trace-id inside `traceparent`, then a session id
   inside `baggage`.
5. **A random UUID.** `_get_session_id_for_spend_log()` returns
   `str(uuid.uuid4())` when nothing above fired.

Point 5 is the trap. A non-null `session_id` does **not** imply the harness sent
one: for an unrecognised client the column is filled with a fresh random UUID
*per request*, which looks exactly like a session id and groups nothing. Before
trusting a `session_id`, confirm it also appears in the harness's own record.

Check what a run actually sent:

```bash
psql postgresql://keith@localhost:5432/litellm -tAc "
select k, count(*) from (
  select jsonb_object_keys(proxy_server_request::jsonb->'litellm_metadata'->'headers') as k
  from \"LiteLLM_SpendLogs\"
  where proxy_server_request::text like '%headers%'
) t group by k order by 2 desc"
```

Note the ceiling on all of it: only **1,595 of 46,534** spend rows have
`proxy_server_request` captured at all, and every one of those carries a
`claude-cli/*` or `Python-urllib/3.13` user agent. Header questions about any
other harness therefore cannot be answered from the database — which is why the
sections below lean on the clients' own on-disk records and code.

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

## Claude Code

Verified 2026-09-25 against Claude Code 2.1.282 and the live
`LiteLLM_SpendLogs` table.

### Where Claude Code writes

The anchor is `~/.claude` (no relocation variable was found).

| Path | What it is | When written |
|---|---|---|
| `~/.claude/projects/<slug>/<sessionId>.jsonl` | **The session transcript** — the real record | Continuously |
| `~/.claude/projects/<slug>/<sessionId>/subagents/agent-<agentId>.jsonl` | **A sub-agent's transcript**, one file per spawned agent | While that agent runs |
| `~/.claude/projects/<slug>/<sessionId>/subagents/agent-<agentId>.meta.json` | Sub-agent metadata — `agentType`, `description`, `name`, `spawnDepth`, `requestShape` | At spawn |
| `~/.claude/projects/<slug>/<sessionId>/auto-mode-classifier-error.txt` | Classifier failure text for that session | Only on failure |
| `~/.claude/history.jsonl` | Prompt history — `display`, `pastedContents`, `timestamp`, `project`, `sessionId` | Per submitted prompt |
| `~/.claude/file-history/<uuid>/` | Per-file history, for rewind | As files change |
| `~/.claude/tasks/<uuid>/` | Background-task state | While tasks run |
| `~/.claude/stats-cache.json` | Aggregate counts (`totalSessions`, `totalMessages`, `dailyActivity`, …) | Periodically |
| `~/.claude/sessions/` | **Exists but is empty** on this machine — the old stub lead (`~/.claude/sessions/*.json`), disproven | — |
| `~/.claude/debug/latest`, `~/.claude/state/mcp-discover-verdicts.json` | Debug output and discovery state | On demand |

The path `wt` resumes from (`internal/agents/claude.go`) is exactly
`projects/<slug>/<sessionId>.jsonl`, where `<slug>` is the absolute worktree path
with every character outside `[a-zA-Z0-9-]` replaced by `-` — the separating
slashes are *kept as dashes with a leading one*, so this repo is
`-Users-keith-github-ohanaverse-local-ai-setup`. (Contrast pi, which strips the
leading separator and wraps the result in `--`.)

### Transcript layout

File name: `<sessionId>.jsonl` — the UUID is both the file stem and the
`sessionId` field on every entry. Sub-agents get their own directory and file,
`<sessionId>/subagents/agent-<agentId>.jsonl`, where `<agentId>` is a
17-hex-character id.

Format: **JSONL forming a tree** via `uuid`/`parentUuid` (`isSidechain`,
`agentId`, `promptId`, `requestId` also appear per entry). Compact JSON — no
space after `:`.

Entry types across a 400-file sample (63,375 entries):

| `type` | Count | Notes |
|---|---:|---|
| `attachment` | 28,041 | Harness-injected context — where the *prompt* lives (below) |
| `assistant` | 16,532 | |
| `user` | 10,083 | |
| `last-prompt` | 1,687 | Header-only line: `{type, leafUuid, sessionId}` |
| `atis-latch` | 1,657 | |
| `mode`, `permission-mode` | 1,164 / 1,162 | |
| `queue-operation` | 847 | |
| `ai-title` | 702 | |
| `system` | 568 | Hook outcomes, e.g. `stop_hook_summary` |
| `file-history-snapshot`, `file-history-delta` | 218 / 200 | |
| `bridge-session` | 205 | |
| `pr-link`, `cost-state` | 156 / 146 | |
| `agent-name` | 7 | |

Content blocks inside `message.content`: `tool_use` 9,092, `tool_result` 9,092,
`thinking` 5,457, `text` 2,060.

### What the transcript contains

- **User messages**, verbatim, as a plain string or a list of blocks.
- **The full system prompt** — not as one string but as a `prompt_snapshot`
  attachment (`systemPrompt` array; ~29 KB in this repo), alongside `instructions`
  (the `CLAUDE.md`/preference files with their full contents, ~18 KB),
  `nested_memory`, `skill_listing`, `environment` (cwd, platform, shell,
  scratchpad), `session_context` (git status, user email), `date`, `model`, and
  `agent_listing_delta`. Replaying these in order reconstructs the live prompt.
- **Assistant output** — `text` blocks and `thinking` blocks. Of 5,457 thinking
  blocks, 3,058 carry text, and 2,406 carry a `signature` — in 2,399 of the
  signature-bearing ones the text is empty, which is Anthropic's opaque/encrypted
  thinking (only the signature is kept).
- **Tool calls** — name, id, and the **full argument object**; 9,092 observed,
  with one `tool_result` per call.
- **Tool results** — the block content plus a separate entry-level
  `toolUseResult`.
- **Provider metadata** — `requestId`, `model`, `stop_reason`, and a full `usage`
  object including `cache_creation_input_tokens`,
  `cache_read_input_tokens` and `output_tokens_details.thinking_tokens`.
- **Hook and error outcomes** — hook `stdout`/`stderr`/`exitCode`/`durationMs` in
  `attachment` entries, and a `system` entry summarising the stop hooks.

### What the transcript does **not** contain

- **Full tool output.** Truncation is inline and explicit:
  `[truncated, showing last 8KiB]`, `[N lines truncated]`,
  `[N characters truncated]`, `[Truncated: PARTIAL view`. The largest
  `tool_result` observed is ~116 KB of JSON.
- **Request parameters** — no temperature, max_tokens, top_p or tool_choice. The
  `usage` block is accounting, not the request.
- **Raw provider payloads** — only the normalised message survives; streaming
  deltas are discarded.
- **Anything from a session run with persistence disabled** — nothing is written.

### Claude Code → the LiteLLM pipeline

**Conclusion: Claude Code is the reference case — its sessions group and
cross-index cleanly.** This is what makes the pipeline's ground-truth threading
possible at all.

- `x-claude-code-session-id` matches `^x-.+-session-id$` (rule 2 of
  [How LiteLLM decides a session id](#how-litellm-decides-a-session-id)).
- Of the 1,595 rows with a captured body, **1,277 carry the header, and in all
  1,277 the header value equals the `session_id` column**.
- `x-claude-code-agent-id` is captured on **633 rows** with **26 distinct
  values** — and **all 26** exist locally as
  `projects/<slug>/<sessionId>/subagents/agent-<agentId>.jsonl`. That is a
  verified two-way key between a proxy row and the sub-agent transcript that
  produced it, and it is what `build_full_transcript.py` uses as ground truth
  for thread grouping.
- Of the 1,126 local top-level session ids, **247 appear in the `session_id`
  column**. The remainder never went through the proxy (direct provider calls, or
  prompt storage was off), which is expected rather than a defect.

Reproduce the strongest check:

```bash
# Sub-agent ids on disk
find ~/.claude/projects -path '*/subagents/agent-*.jsonl' -exec basename {} \; \
  | sed 's/^agent-//;s/\.jsonl$//' | sort -u > /tmp/cc-agentids.txt

# Sub-agent ids the proxy captured (needs prompt storage; 26 at the time)
psql postgresql://keith@localhost:5432/litellm -tAc \
 "select distinct proxy_server_request::jsonb->'litellm_metadata'->'headers'->>'x-claude-code-agent-id'
  from \"LiteLLM_SpendLogs\" where proxy_server_request::text like '%agent-id%'" \
  | grep -v '^$' | sort -u > /tmp/ll-agentids.txt

comm -12 /tmp/cc-agentids.txt /tmp/ll-agentids.txt | wc -l   # 26 of 26
```

### Working with Claude Code transcripts

```bash
D=~/.claude/projects/-Users-keith-github-ohanaverse-local-ai-setup

# Newest session in this repo
ls -t "$D"/*.jsonl | head -1

# User + assistant text of the newest session, in order
python3 - "$D" <<'EOF'
import json, sys, glob, os
newest = max(glob.glob(os.path.join(sys.argv[1], "*.jsonl")), key=os.path.getmtime)
for line in open(newest, errors="replace"):
    if not line.strip(): continue
    e = json.loads(line)
    m = e.get("message")
    if not isinstance(m, dict) or m.get("role") not in ("user", "assistant"): continue
    c = m.get("content")
    if isinstance(c, str):
        print(f"[{m['role']}] {c[:400]}")
    elif isinstance(c, list):
        for b in c:
            if isinstance(b, dict) and b.get("type") == "text":
                print(f"[{m['role']}] {b['text'][:400]}")
EOF

# Sessions that hit tool-output truncation
grep -l 'showing last 8KiB' "$D"/*.jsonl
```

Caveats: entries are a **tree** (`uuid`/`parentUuid`), so walk parents from a
leaf for the live conversation; `subagents/` holds sub-agent turns, not the main
thread; and the file stem is the session id for a main file but the *agent* id
for a sub-agent file.

### Reference

- Store path and slug: `wt/internal/agents/claude.go`,
  `wt/internal/session/session.go`
- Resume: `--resume <sessionId>` (`claudeDriver.ResumeFlag`)

---

## Codex CLI

Verified 2026-09-25 against codex-cli 0.155.1 and the live
`LiteLLM_SpendLogs` table.

### Where Codex writes

The anchor is `~/.codex`.

| Path | What it is | When written |
|---|---|---|
| `~/.codex/sessions/YYYY/MM/DD/rollout-<ISO>-<uuid>.jsonl` | **The session transcript** | Continuously |
| `~/.codex/state_5.sqlite` → `threads` | **The index**: one row per session — `id`, `rollout_path`, `cwd`, `title`, `model`, `model_provider`, `source`, `tokens_used`, `git_branch`, `git_sha`, `cli_version`, `archived` | Per session |
| `~/.codex/thread_history_1.sqlite` → `thread_items`, `thread_turns` | Projection of turns for the history UI (1,590 / 107 rows) | Continuously |
| `~/.codex/session_index.jsonl` | `{id, thread_name, updated_at}` | Per session |
| `~/.codex/history.jsonl` | Prompt history — `{session_id, ts, text}` | Per prompt |
| `~/.codex/logs_2.sqlite` → `logs` | Diagnostic log table (`ts`, `level`, `target`, `feedback_log_body`, `thread_id`) — **0 rows on this machine**, 197 MB of free pages | — |
| `~/.codex/memories_1.sqlite` | `stage1_outputs`, `jobs` — **0 rows** | — |
| `~/.codex/goals_1.sqlite`, `queue_1.sqlite` | Goal and queue state | As used |
| `~/.codex/shell_snapshots/`, `~/.codex/log/` | Shell snapshots and log output | As used |

The `threads` table is the fastest way to answer "which session ran in this
worktree" — it stores `cwd` and `rollout_path` together.

### Transcript layout

File name: `rollout-<ISO8601 with dashes>-<uuid>.jsonl`, e.g.
`rollout-2026-09-25T15-08-25-01a0d9f8-05bb-7ab1-8a9d-d253c2effb56.jsonl`. The
`15-08-25` is **local** time while the `timestamp` inside is UTC
(`19:08:25.526Z`) — do not compare the two directly. The uuid in the name is also
`payload.session_id` (and `payload.id`) in the `session_meta` line.

Format: **JSONL, linear** — there is no `parentId`; every line is
`{timestamp, ordinal, type, payload}`. Top-level types across all 211 files
(28,347 entries):

| `type` | Count | Notes |
|---|---:|---|
| `response_item` | 13,864 | The model-facing conversation |
| `event_msg` | 13,802 | The UI/event stream — carries usage |
| `turn_context` | 332 | Per-turn model, cwd, approval and sandbox policy |
| `session_meta` | 211 | Header: session id, cwd, version, **system prompt** |
| `world_state` | 96 | |
| `token_usage_record` | 31 | |
| `compacted` | 2 | Context-compaction markers |

`response_item.payload.type`: `function_call` 4,585, `function_call_output`
4,584, `reasoning` 3,401, `message` 1,294 (roles `user` 644, `assistant` 424,
`developer` 226 — `developer` carries skills and instructions).

`event_msg.payload.type`: `token_count` 5,963, `agent_reasoning` 4,466,
`item_completed` 1,597, `task_started` 381, `exec_command_end` 354,
`task_complete` 346, `user_message` 288, `agent_message` 251, `turn_aborted` 28,
plus `collab_*`, `entered_review_mode`, `context_compacted` and
`thread_rolled_back`.

### What the transcript contains

- **The full system prompt**, as `session_meta.payload.base_instructions.text`.
- **User and developer messages**.
- **The model's reasoning — but only as a summary plus an opaque blob**:
  `reasoning.payload.summary` (`summary_text`) on 3,399 items and
  `encrypted_content` on 3,371, with plaintext `content` on only 2.
- **Tool calls with full arguments** — `function_call.payload.arguments`.
  Observed tools include `exec_command` and `apply_patch` bodies.
- **Tool results** — `function_call_output.payload.output`, plus
  `exec_command_end` events with exit codes and aggregated output.
- **Usage, per request** — `event_msg.token_count` carries
  `info.last_token_usage` / `info.total_token_usage` (`input_tokens`,
  `cached_input_tokens`, `output_tokens`, `reasoning_output_tokens`) and
  `model_context_window`. This is what lines up with the proxy's token counts.
- **Turn policy context** — `turn_context` records `approval_policy`,
  `sandbox_policy`, `permission_profile` and `timezone`.

### What the transcript does **not** contain

- **Full tool output.** `function_call_output` carries inline truncation (178
  `truncated`, 48 `chars truncated`, 7 `Truncated` markers); the largest observed
  output is ~42 KB of JSON.
- **Plaintext reasoning** — encrypted or summarised only (above).
- **Request parameters** — no temperature or top_p. `reasoning_effort` appears in
  `state_5.threads`, not in the rollout.
- **The diagnostics** the `logs` table exists for — it is empty.

### Codex → the LiteLLM pipeline

**Conclusion: Codex sessions cannot be isolated by this pipeline.**

- **0 of the 211 session uuids** appear in `session_id` — nor in `agent_id`,
  `metadata`, `proxy_server_request` or `response`.
- No Codex user agent appears among the 1,595 rows with a captured body.
- Mechanically, the installed binary has **no `x-<vendor>-session-id` header on
  the Responses/chat path**. The only session-shaped header string it contains,
  `x-session-id`, belongs to the *realtime websocket* path
  (`codex-api/src/endpoint/realtime_websocket/`) and would not match
  `^x-.+-session-id$` in any case.
- The `session_id` values on Codex's spend rows are therefore **LiteLLM's
  last-resort random UUIDs** (rule 5). Observed directly: the 2026-09-25
  `wt smoke` run recorded `session_meta.session_id`
  `01a0d9f8-05bb-7ab1-8a9d-d253c2effb56` with its own `token_count` of
  `total_tokens: 10354`; the matching spend row at 19:08:25 carries
  `session_id = ddb9b6da-d9c2-407b-8097-ecb889e00c0a`, a value that appears
  nowhere in the session record.

The rollout file is therefore the only per-session record for Codex, and must be
read directly. The `threads` table is how you find the right one:

```bash
sqlite3 "file:$HOME/.codex/state_5.sqlite?immutable=1" \
  "select id, rollout_path, model, datetime(created_at,'unixepoch') from threads
   where cwd like '%local-ai-setup%' order by updated_at desc limit 5"
```

### Working with Codex transcripts

```bash
# Newest rollout
f=$(find ~/.codex/sessions -name '*.jsonl' | sort | tail -1)

# User + assistant text, in order
python3 - "$f" <<'EOF'
import json, sys
for line in open(sys.argv[1], errors="replace"):
    if not line.strip(): continue
    d = json.loads(line)
    p = d.get("payload")
    if d.get("type") == "response_item" and isinstance(p, dict) and p.get("type") == "message":
        for c in p.get("content") or []:
            if c.get("type") in ("input_text", "output_text"):
                print(f"[{p['role']}] {c['text'][:400]}")
EOF

# Per-request usage
python3 -c "
import json
for line in open('$f'):
    d=json.loads(line)
    if d.get('type')=='event_msg' and d['payload'].get('type')=='token_count':
        print(d['payload']['info']['total_token_usage'])"
```

Caveats: the file is **linear**, but a session can be `compacted` and later
resumed; `event_msg` and `response_item` are two views of the same turn (one for
the UI, one for the model) — do not double-count them; and the filename timestamp
is local while the payload timestamps are UTC.

### Reference

- `state_5.sqlite`'s `threads` schema is the cwd/session-id → `rollout_path` map.
- `wt/internal/agents/codex.go` — wt has **no Codex resume support**, despite the
  `session id: <uuid>` line on stdout.

---

## GitHub Copilot CLI

Verified 2026-09-25 against GitHub Copilot CLI 1.0.88 and the live
`LiteLLM_SpendLogs` table.

### Where Copilot writes

The anchor is `~/.copilot`.

| Path | What it is | When written |
|---|---|---|
| `~/.copilot/session-state/<uuid>/events.jsonl` | **The session transcript** — present in 235 of 262 session dirs | Continuously |
| `~/.copilot/session-state/<uuid>/workspace.yaml` | Session header: `id`, `cwd`, `git_root`, `repository`, `branch`, `name`, `created_at`, `updated_at` | At start, updated |
| `~/.copilot/session-state/<uuid>/checkpoints/index.md` | Human-readable checkpoints | On checkpoint |
| `~/.copilot/session-state/<uuid>/rewind-file-snapshots/tracking.json` | File snapshots for rewind | On edits |
| `~/.copilot/session-state/<uuid>/{files,research}/`, `vscode.metadata.json` | Sidecar artifacts; the VSCode metadata is absent for CLI-only sessions | — |
| `~/.copilot/session-store.db` | **A derived index**, not the transcript (below) | Continuously |
| `~/.copilot/logs/process-*.log` | CLI process logs (27 files) | Per process |

`session-store.db` holds `sessions` (259), `turns` (269) and
`assistant_usage_events` (4,070 — the richest usage source: `model`,
`input_tokens`, `output_tokens`, `reasoning_tokens`, `duration_ms`,
`time_to_first_token_ms`, `finish_reason`, `api_endpoint`), plus `session_files`,
`session_refs`, `checkpoints`, `dynamic_context_items` and an FTS5
`search_index`. The transcript itself lives in `events.jsonl`; the DB is rebuilt
from it, so treat the JSONL as authoritative when they disagree.

### Transcript layout

One directory per session, named by the session UUID, with `events.jsonl` inside
it — newline-delimited compact JSON.

Format: **JSONL forming a tree** via `id`/`parentId`, with a uniform envelope:

```json
{"type": "...", "data": {...}, "id": "<uuid>", "timestamp": "<ISO>", "parentId": "<uuid|null>"}
```

Types across all 235 transcripts (~23,700 events):

| `type` | Count | Notes |
|---|---:|---|
| `tool.execution_start` | 4,638 | Full tool arguments |
| `tool.execution_complete` | 4,634 | Result, success flag, telemetry |
| `assistant.message` | 4,381 | Text, reasoning, tool requests |
| `assistant.turn_start`, `assistant.turn_end` | 4,063 / 4,051 | |
| `permission.requested`, `permission.completed` | 474 / 473 | |
| `hook.start`, `hook.end` | 391 / 391 | |
| `user.message` | 294 | |
| `system.message` | 265 | **The system prompt** |
| `session.start`, `session.shutdown` | 235 / 230 | |
| `session.model_change` | 83 | |
| `skill.invoked` | 44 | |
| `subagent.started`, `subagent.completed` | 38 / 36 | Linked by `parentToolCallId` |
| `session.usage_checkpoint` | 38 | Per-call model and request ids |
| `session.error` | 27 | |
| `session.compaction_start`, `session.compaction_complete` | 16 / 16 | |
| `model.model_call_started`, `model.model_call_failure` | 7 / 7 | |

### What the transcript contains

- **The full system prompt**, as `system.message.data.content` (with
  `contentBlocks`), role `system`.
- **User messages** — `user.message.data.content`, plus `transformedContent`.
- **Assistant output** — `assistant.message.data`: `content`, **`reasoningText`**
  (plaintext reasoning), `reasoningOpaque` / `encryptedContent`, `model`,
  `apiCallId`, and `toolRequests` carrying each call's **full `arguments` object**
  and an `intentionSummary`.
- **Tool results** — `tool.execution_complete.data`: `result`, `success`,
  `toolTelemetry`, or `error`.
- **Session context** — `session.start` records `cwd`, `gitRoot`, `repository`,
  `branch`, `headCommit`, `baseCommit`, `selectedModel` and `copilotVersion`.
- **Usage** — `session.shutdown` carries `agentMetrics`, `conversationTokens`,
  `modelMetrics` and `codeChanges`; per-call detail is in
  `session.usage_checkpoint` (which also names the `api_endpoint`, e.g.
  `/v1/messages` or `/chat/completions`, and a `github_request_id`).
- **Sub-agent structure** — `subagent.*` events and `parentToolCallId` on
  assistant and tool events.

### What the transcript does **not** contain

- **Full tool output.** File reads are truncated with an inline
  `<note>Content truncated. Call the fetch tool …` marker; the largest observed
  `result` is ~91 KB of JSON.
- **Request parameters** — no temperature or max_tokens. `reasoning_effort`
  appears only in the derived DB's `assistant_usage_events`.
- **The raw HTTP request or response** — only normalised events.

### Copilot → the LiteLLM pipeline

**Conclusion: Copilot sends no session-shaped header at all — treat Copilot
sessions as unlinkable.** This was ambiguous as of the first pass (header name
unverified against the compressed binary); it is now resolved by a live
capture.

- **No Copilot user agent appears** among the 1,595 rows with a captured request
  body (all are `claude-cli/*` or `Python-urllib/3.13`).
- **Exactly one** of the 262 local session ids
  (`956f3299-09c3-49c2-b083-ece1f87068b5`) appears in `session_id` — on three
  `status: failure` rows dated 2026-09-02 for model `gpt-5.4-nano`, which the
  proxy config did not contain (`ProxyModelNotFoundError`). That match is now
  understood to be coincidental rather than a sent header: see below.
- **Not in the 2026-09-25 `wt smoke` run.** Copilot's row at 19:08:31
  (`ollama_chat/gpt-oss:20b`, `total_tokens: 34329`) carries
  `session_id = f2c2deea-30af-4dbc-be0a-618d16ef162e`, a LiteLLM-assigned random
  UUID that appears nowhere in the session record. That row is Copilot's and not
  Codex's: the session's own `session.shutdown` reports exactly
  `inputTokens: 34278, outputTokens: 51`.
- **The header set is now VERIFIED by direct capture**, since `strings` against
  the compressed Node SEA binary proves nothing either way (see the prior
  UNVERIFIED note this replaces). Copilot routes local/custom providers through
  `COPILOT_PROVIDER_BASE_URL`, which made it possible to stand in for the
  provider and log the raw request:

  ```bash
  # terminal 1 — logs every header to /tmp/copilot_headers.log
  python3 header_probe.py   # trivial http.server handler, see below

  # terminal 2
  env COPILOT_PROVIDER_BASE_URL=http://127.0.0.1:4199/v1 \
      COPILOT_PROVIDER_API_KEY=dummy-key \
      COPILOT_PROVIDER_WIRE_API=completions \
      COPILOT_MODEL=probe-model \
      copilot -p "say ok"
  ```

  The captured `POST /v1/chat/completions` (OpenAI JS SDK 5.20.1, confirmed by
  `x-stainless-*` headers and `user-agent: OpenAI/JS 5.20.1`) carries **18
  headers total**: `authorization`, `content-type`, `accept`,
  `accept-encoding`, `content-length`, `host`, `user-agent`, seven
  `x-stainless-*` SDK-internal headers, plus `x-interaction-type` and
  `x-initiator` (both fixed values — `conversation-user` / `user` — not
  identifiers). **None matches `^x-.+-session-id$`, none is
  `x-litellm-trace-id`/`x-litellm-session-id`, and there is no `traceparent` or
  `baggage`.** No session identity is sent in headers, full stop — this puts
  Copilot alongside Codex and OpenCode rather than in an ambiguous middle
  state. The body's `messages[0]` is the full Copilot CLI system prompt, which
  also confirms the probe intercepted a real, complete Copilot turn rather than
  an aborted or malformed one.

Practical consequence: a Copilot `session_id` cannot be trusted to mean
anything. Read `events.jsonl` directly, and find a session by `cwd` in
`workspace.yaml` or the `sessions` table:

```bash
for f in ~/.copilot/session-state/*/workspace.yaml; do
  grep -q 'local-ai-setup' "$f" && echo "$f"
done
```

### Working with Copilot transcripts

```bash
S=~/.copilot/session-state/<uuid>

# User + assistant text of one session, in order
python3 - "$S/events.jsonl" <<'EOF'
import json, sys
for line in open(sys.argv[1], errors="replace"):
    if not line.strip(): continue
    e = json.loads(line); t = e.get("type"); d = e.get("data") or {}
    if t == "user.message":
        print(f"[user] {str(d.get('content'))[:400]}")
    elif t == "assistant.message":
        if d.get("content"): print(f"[assistant] {str(d['content'])[:400]}")
        for tr in d.get("toolRequests") or []:
            print(f"  [tool] {tr.get('name')} {json.dumps(tr.get('arguments'))[:200]}")
EOF

# Per-call usage from the derived DB
sqlite3 "file:$HOME/.copilot/session-store.db?immutable=1" \
  "select turn_index, model, input_tokens, output_tokens, duration_ms, finish_reason
   from assistant_usage_events where session_id='<uuid>' order by id"
```

Caveats: opening a live WAL store needs `file:…?immutable=1`, or SQLite reports
`unable to open database file`; the DB is derived, so trust the JSONL.

### Reference

- `wt/internal/agents/copilot.go` — wt has **no Copilot resume support**, even
  though the CLI prints `Resume copilot --resume=<id>`.

---

## OpenCode

Verified 2026-09-25 against opencode 1.18.26 and the live `LiteLLM_SpendLogs`
table.

### Where OpenCode writes

The anchor is `~/.local/share/opencode` (also check `~/.config/opencode`).

| Path | What it is | When written |
|---|---|---|
| `~/.local/share/opencode/opencode.db` | **The session store** — SQLite, every conversation | Continuously |
| `~/.local/share/opencode/storage/session_diff/<session-id>.json` | Per-session file diffs | On edits |
| `~/.local/share/opencode/storage/migration/` | Migration bookkeeping | Once |
| `~/.local/share/opencode/log/`, `snapshot/`, `repos/`, `tool-output/` | Logs, git snapshots, repo cache, externalised tool output (`tool-output/` was empty here) | Various |

**Important, and a behaviour change:** OpenCode has **migrated off its old
`storage/session/<projectID>/*.json` layout to SQLite**. On this machine
`storage/session`, `storage/message` and `storage/part` **do not exist**.
`wt`'s resume support still looks for `storage/session/<root-commit>/*.json`
(`wt/internal/agents/opencode.go` via `session.OpenCodeProjectID`), and
`LatestByExt` silently returns "no sessions" for a missing directory — so
`opencode-wt`'s resume prompt **no longer finds anything** on opencode 1.18.26.
That is a wt bug, not an OpenCode defect.

### Transcript layout

The store is relational, not files-per-message:

| Table | Rows here | Holds |
|---|---:|---|
| `project` | 6 | `id`, `worktree`, `vcs` |
| `project_directory` | 6 | Additional directories per project |
| `session` | 47 | `id`, `project_id`, `parent_id`, `title`, `version`, `model` (JSON), `cost`, `tokens_input`/`output`/`reasoning`, `time_created`/`updated`, `summary_*`, `directory`, `agent` |
| `message` | 172 | `id`, `session_id`, and `data` (JSON: `role`, `time`, `model`, `cost`, `tokens`, `finish`, `parentID`, `agent`, `path`) |
| `part` | 463 | `id`, `message_id`, `session_id`, and `data` (JSON: `type`, …) |
| `event` | 282 | `aggregate_id`, `seq`, `type`, `data` — `message.part.updated.1`, `message.updated.1`, `session.updated.1`, `session.created.1` |

Session ids look like `ses_f2688a3b5ffe2tTqzo` — **not** UUIDs. A conversation is
`session` → `message` (by `session_id`) → `part` (by `message_id`). Sub-agent
sessions are `session` rows with a `parent_id` (2 here), and a `task` tool part
records both ids in its `state.metadata`.

`message.data.role` is only ever `assistant` (118) or `user` (54) — **there is no
system-prompt row anywhere**, so unlike Claude Code, Codex and Copilot the prompt
is not recoverable from the store.

`part.data.type`: `text` 120, `step-start` 110, `step-finish` 108, `tool` 77,
`reasoning` 40, `patch` 7, `subtask` 1.

### What the transcript contains

- **User and assistant text** — `text` parts.
- **Assistant reasoning** — `reasoning` parts (`text`, `time`).
- **Tool calls with full arguments and results** — a `tool` part carries `tool`,
  `callID` and `state`: `status`, `input` (the complete arguments), `output`,
  `title`, `metadata`, `time`. `metadata` reports `exit`, `count`, and an
  explicit **`truncated`** boolean.
- **Cost and token usage** — per assistant message (`cost`, `tokens` =
  `{total, input, output, reasoning, cache{read, write}}`) and again per
  `step-finish` part, alongside the `finish` reason.
- **Patch and file-change records** — `patch` parts list the files touched and a
  snapshot hash; `step-start`/`step-finish` carry the snapshot ids.
- **Sub-task records** — `subtask` parts name the sub-agent, its model and its
  prompt.

### What the transcript does **not** contain

- **The system prompt** — not persisted in any table.
- **Truncated tool output** — the part records `truncated: true` rather than
  keeping the rest; `tool-output/` may hold externalised payloads but was empty
  here.
- **Request parameters** — no temperature, max_tokens or top_p.
- **Raw provider payloads.**

### OpenCode → the LiteLLM pipeline

**Conclusion: OpenCode sessions are not isolatable — and the near-miss is the
interesting part.**

- **0 of the 47 session ids** appear in `session_id`; there is no `ses_`-prefixed
  value in the column at all.
- No OpenCode user agent appears among the 1,595 rows with a captured body.
- The `x-opencode-session` lead in the old stub was **real but useless**. The
  installed 1.18.26 binary builds its request headers like this:

  ```js
  headers: {
    ...(e.model.providerID.startsWith("opencode") ? {
      ...(u ? {"x-opencode-project": u} : {}),
      "x-opencode-session": e.sessionID,
      "x-opencode-request": e.user.id,
      "x-opencode-client": e.flags.client,
      "User-Agent": _i
    } : {
      "x-session-affinity": e.sessionID,
      "X-Session-Id": e.sessionID,
      "User-Agent": _i
    }),
    ...(e.parentSessionID ? {"x-parent-session-id": e.parentSessionID} : {}),
    ...e.model.headers,
    ...g
  }
  ```

  Against LiteLLM's `^x-.+-session-id$`:
  - `x-opencode-session` (the vendor-gateway path) — **no match** (it ends
    `-session`);
  - `x-session-affinity` — **no match**;
  - `X-Session-Id` — **no match** (`x-session-id` has nothing between `x-` and
    `-session-id`, and `.+` requires at least one character);
  - `x-parent-session-id` (child sessions only) — **matches, but it carries the
    *parent's* id.** So the one header that would be captured would attribute a
    sub-agent's calls to its parent session, which is worse than capturing
    nothing.

Read the `session`/`message`/`part` tables directly; `project.worktree` is how
you find a repo's sessions:

```bash
sqlite3 "file:$HOME/.local/share/opencode/opencode.db?immutable=1" \
  "select s.id, s.title, datetime(s.time_created/1000,'unixepoch'), s.tokens_input, s.tokens_output
   from session s join project p on p.id = s.project_id
   where p.worktree = '/Users/keith/github/ohanaverse/local-ai-setup'
   order by s.time_created desc limit 5"
```

### Working with OpenCode transcripts

```bash
SID=ses_f268ec6ffffeaPf2iX

# Full conversation, in order (message data + its parts)
sqlite3 "file:$HOME/.local/share/opencode/opencode.db?immutable=1" "
select json_extract(m.data,'\$.role'), json_extract(p.data,'\$.type'), substr(p.data,1,300)
from message m join part p on p.message_id = m.id
where m.session_id = '$SID' order by m.time_created, p.time_created limit 40"

# Per-message cost and tokens
sqlite3 "file:$HOME/.local/share/opencode/opencode.db?immutable=1" "
select json_extract(data,'\$.tokens.total'), json_extract(data,'\$.cost'), json_extract(data,'\$.finish')
from message where session_id = '$SID'"
```

Caveats: open the DB read-only (`?immutable=1` — it has live `-wal`/`-shm`
sidecars); `message.data` and `part.data` are JSON *strings* inside SQLite, so use
`json_extract`; order by the millisecond time fields, not by id.

### Reference

- `wt/internal/agents/opencode.go`, `wt/internal/session/session.go` —
  `OpenCodeProjectID()` (root commit) and the now-stale `storage/session` path.

---

## Antigravity CLI

**Partial stub — locations, format and payload decoding verified 2026-09-25
against `agy` 1.2.4; proxy linkage (whether any session header reaches
LiteLLM) is still NOT verified.**

The stub's assumed root, `~/.antigravity`, **does not exist**. Antigravity keeps
its CLI state under `~/.gemini/antigravity-cli/` (Google/Gemini lineage).
`~/.codeium/` and `~/.cache/antigravity/` also exist but held no sessions.

| Path | What it is |
|---|---|
| `~/.gemini/antigravity-cli/conversations/<conversation-uuid>.db` | **One SQLite DB per conversation** — 54 here, 22 MB total |
| `~/.gemini/antigravity-cli/conversation_summaries.db` | Index of conversations — 13 rows: `conversation_id`, `title`, `preview`, `step_count`, `workspace_uris`, `project_id`, `parent_conversation_id`, `nesting_depth`, `battle_id` |
| `~/.gemini/antigravity-cli/history.jsonl` | Prompt history — 78 lines of `{display, timestamp, workspace}` |
| `~/.gemini/antigravity-cli/cli.log` | Symlink to `log/cli-<timestamp>.log` |
| `~/.gemini/antigravity-cli/{brain,annotations,implicit,presence,knowledge,scratch,updater}` | Agent state, annotations, caches |

Inside a conversation DB the tables are `trajectory_meta`, `steps`,
`gen_metadata`, `executor_metadata`, `parent_references`,
`trajectory_metadata_blob` and `battle_mode_infos`. The transcript is the `steps`
table — but **its payload is a protobuf blob, not text**:

```
steps(idx, step_type, status, has_subtrajectory, metadata BLOB, error_details BLOB,
      permissions BLOB, task_details BLOB, render_info BLOB, step_payload BLOB, step_format)
```

A `step_payload` does not decode as JSON (a sample read back as raw bytes) —
it's a serialized `gemini_coder.Step` protobuf message, and `agy` ships no
`.proto` files on disk to decode it with. Largest observed: 185 steps.

### Decoding `step_payload`

The schema is embedded *inside the `agy` binary itself*: protoc-gen-go
compiles each generated file's `FileDescriptorProto` in as a raw byte blob,
one per Go package, with no length-prefixed container grouping them.
[`antigravity/extract_descriptors.py`](antigravity/extract_descriptors.py)
locates and reassembles them:

1. Every `FileDescriptorProto` serializes its `name` field (field 1) first,
   and every name ends in `.proto` — so it scans the binary for
   `<0x0a><varint length><...>.proto` and keeps candidates whose captured
   string looks like a plausible file path. Against the installed 1.2.4
   binary this finds **333 candidates**.
2. Each blob's length isn't stored anywhere, so the script walks the
   generic protobuf wire format (tag/wire-type only — no message-specific
   schema needed) from the candidate start until a tag byte that isn't a
   valid continuation is hit. That's what naturally happens at the true
   end of one embedded blob, where either zero-byte alignment padding or
   unrelated binary content follows. **277 of 333 candidates parse cleanly**
   this way (41 fail outright; 15 names occur twice at different sizes —
   in every case checked, the larger was a strict superset of the smaller's
   message/enum types, not a different schema version, so the larger is
   kept).
3. Building the `gemini_coder.Step` closure from
   `third_party/gemini_coder/proto/trajectory.proto` (package
   `gemini_coder`, `Step` has 134 top-level fields) needs **37 files**, all
   present except one: `third_party/jetski/browser_pb/browser.proto`. Only
   three enums from it are actually referenced (`ClickType`,
   `ScrollDirection`, `WindowState`, all on browser-tool config fields), so
   the script stubs a minimal `exa.browser_pb` file defining just those
   three enums with a zero value each, rather than leaving the closure
   unresolved.
4. The 37 files assemble into a `FileDescriptorSet` (~500 KB), loadable via
   `descriptor_pool` (Python) or `protoc --descriptor_set_in=<path>
   --decode=gemini_coder.Step` (CLI).

```bash
python3 antigravity/extract_descriptors.py ~/.local/bin/agy \
  third_party/gemini_coder/proto/trajectory.proto antigravity/step.desc
```

**Validated against every `step_payload` in every readable local conversation
DB: 1,049 of 1,049 decoded successfully, 0 failures.** (A handful of DBs
failed to open at all — `database disk image is malformed` — likely stale
live-WAL files; unrelated to descriptor correctness.) Decoded output is
fully readable, e.g. `type: CORTEX_STEP_TYPE_PLANNER_RESPONSE status:
CORTEX_STEP_STATUS_DONE metadata { created_at { seconds: ... } source:
CORTEX_STEP_SOURCE_MODEL ... }` — named enums and all, confirming the
extracted schema matches the binary's actual encoder.

The generated `.desc` file is gitignored (`antigravity/*.desc`) and
regenerated from the locally-installed `agy` binary rather than committed —
it's extracted internal Google schema *metadata* (message/field/enum names
and numbers), used here only to decode the current user's own local
conversation data, not redistributed.

The CLI does support resume (`agy --continue`, `agy --conversation <id>`,
`--log-file`), so conversations have stable ids — but **whether any session
header reaches LiteLLM is still unverified**, and `wt`'s `agyDriver` has no
resume support at all.

To confirm the store against a fresh run:

```bash
ls -lt ~/.gemini/antigravity-cli/conversations/*.db | head -3
sqlite3 "file:$HOME/.gemini/antigravity-cli/conversation_summaries.db?immutable=1" \
  "select conversation_id, title, step_count, last_modified_time
   from conversation_summaries order by last_modified_time desc limit 5"
```

What's left to finish this harness: proxy linkage (one `agy` session run
while watching `proxy_server_request` headers — the same technique used for
[Copilot](#copilot--the-litellm-pipeline) applies here), and, optionally, a
small helper that walks a conversation DB's `steps` table end-to-end and
prints each decoded `Step` (deferred — not built here).

---

## shell

`shell-wt` (`wt --agent shell`) launches a plain shell command. It keeps **no
session record**, and wt's agent table marks it neither rotating nor resuming.
Nothing to document.

---

## Filling in a harness

The method used for pi and the four harnesses above, in order (each step is cheap
and falsifiable):

1. **Launch the agent once with a trivial prompt** (`wt smoke --json` does this
   for several harnesses at once) and watch for a session identifier on stdout.
   Note that the stdout line and the store are different things: Copilot prints
   `Resume copilot --resume=<id>` and Codex prints `session id: <uuid>`, but
   neither *sends* that id to the proxy (see their sections above).
2. **Find files the run created**: `find ~ -maxdepth 4 -mmin -2 -type f -name
   '*.json*' 2>/dev/null | grep -v Library/Caches`, then narrow to the harness's
   own config directory. (Keep `-maxdepth` — an unbounded scan of `$HOME` is not
   cheap.) Check more than the obvious root: Antigravity's sessions are under
   `~/.gemini/antigravity-cli/`, not `~/.antigravity/`.
3. **Check whether the harness's identity reaches the proxy** — the rule chain is
   in [How LiteLLM decides a session id](#how-litellm-decides-a-session-id):
   ```bash
   psql postgresql://keith@localhost:5432/litellm -tAc "select
     proxy_server_request::jsonb->'litellm_metadata'->'headers'->>'user-agent',
     proxy_server_request::jsonb->'litellm_metadata'->'headers'->>'x-claude-code-session-id'
    from \"LiteLLM_SpendLogs\"
    where proxy_server_request::text ilike '%headers%' limit 5"
   ```
   A header that does not match `^x-.+-session-id$` means the harness's sessions
   are not isolatable through this pipeline — **but do not conclude from a
   non-null `session_id` that one matched**: LiteLLM fills the column with a
   random UUID when nothing did, so always confirm the value also appears in the
   harness's own record.
4. **Only trust a header grep when the strings are visible.** A grep that finds
   nothing proves nothing for a compressed single-file bundle — Copilot's Node SEA
   build hides every one of its own JS strings. Cross-check with a string you
   know the harness contains.
5. **Characterize coverage**: what it stores, what it truncates, and what it
   omits — the same headings used for pi above.
6. **For SQLite stores, open them read-only**:
   `sqlite3 "file:/abs/path.db?immutable=1"`. A live `-wal`/`-shm` pair makes
   plain `-readonly` fail with `unable to open database file`. Never write, never
   `VACUUM`.
7. **Add the row to the index table** and mark it verified with a date.

## Related

- Pipeline overview and data-model caveats: [CLAUDE.md](CLAUDE.md)
- The same `LiteLLM_SpendLogs` table viewed as aggregate spend:
  [docs/guides/07-usage-and-spend.md](../docs/guides/07-usage-and-spend.md)
- Which harnesses wt can launch, and how they are routed:
  [docs/guides/06-wt-agents-and-models.md](../docs/guides/06-wt-agents-and-models.md)
- The session-id rule chain in the proxy itself: `…/litellm/proxy/litellm_pre_call_utils.py`
  (`get_chain_id_from_headers`, `_GENERIC_SESSION_ID_HEADER_RE`) and
  `…/litellm/proxy/spend_tracking/spend_tracking_utils.py`
  (`_get_session_id_for_spend_log`, the random-UUID fallback).
