# LiteLLM Session-Log Pipeline Move — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the ad-hoc LiteLLM session-log extraction pipeline from `~/tmp/litellm-session-logs/` into the repo as a new top-level `litellm-session-logs/` directory, wired into lint/docs per spec `docs/superpowers/specs/2026-09-25-litellm-session-logs-design.md`.

**Architecture:** Four standalone scripts (psql SQL, POSIX sh export, two stdlib-only Python transcript builders) plus a package-level CLAUDE.md, in their own directory. Generated `session_*` outputs stay untracked via a dir-local `.gitignore`. No package manager, no new dependencies.

**Tech Stack:** psql (Postgres), POSIX sh, Python 3 stdlib only. Working branch: `litellm-session-logs` (main is hook-blocked).

**Source-of-truth note:** the source files still exist in `~/tmp/litellm-session-logs/`. Tasks 1–2 copy and verify them against the original generated artifacts there; originals are deleted only in Task 7, after everything passes.

---

### Task 1: Create `litellm-session-logs/` with verbatim-copied scripts

**Files:**
- Create: `litellm-session-logs/01_export_session_logs.sql` (copy + one comment edit)
- Create: `litellm-session-logs/02_export_proxy_server_request.sh` (verbatim copy)
- Create: `litellm-session-logs/build_transcript.py` (verbatim copy; arg fix is Task 2)
- Create: `litellm-session-logs/build_full_transcript.py` (verbatim copy)
- Create: `litellm-session-logs/.gitignore`

- [ ] **Step 1: Copy the four scripts**

```bash
mkdir -p litellm-session-logs
cp ~/tmp/litellm-session-logs/01_export_session_logs.sql litellm-session-logs/
cp ~/tmp/litellm-session-logs/02_export_proxy_server_request.sh litellm-session-logs/
cp ~/tmp/litellm-session-logs/build_transcript.py litellm-session-logs/
cp ~/tmp/litellm-session-logs/build_full_transcript.py litellm-session-logs/
```

- [ ] **Step 2: Apply the SQL comment edit** (the only change to the SQL; adds the `LITELLM_DATABASE_URL` note matching the sh script's env override)

In `litellm-session-logs/01_export_session_logs.sql`, replace:

```
-- Usage:
--   psql postgresql://keith@localhost:5432/litellm \
```

with:

```
-- Usage (LITELLM_DATABASE_URL overrides the connection string, as in
-- 02_export_proxy_server_request.sh):
--   psql "${LITELLM_DATABASE_URL:-postgresql://keith@localhost:5432/litellm}" \
```

- [ ] **Step 3: Write `.gitignore`**

Create `litellm-session-logs/.gitignore` with exactly:

```
# Pipeline outputs (session dumps, transcripts) stay untracked.
session_*
```

- [ ] **Step 4: Verify copies**

Run:
```bash
bash -n litellm-session-logs/02_export_proxy_server_request.sh && echo SH-OK
shellcheck --severity=error litellm-session-logs/02_export_proxy_server_request.sh && echo SHELLCHECK-OK
python3 -c "import ast; ast.parse(open('litellm-session-logs/build_transcript.py').read()); ast.parse(open('litellm-session-logs/build_full_transcript.py').read()); print('PY-OK')"
```
Expected: `SH-OK`, `SHELLCHECK-OK`, `PY-OK` (if shellcheck is not installed, note it and rely on Task 4's lint run).

- [ ] **Step 5: Commit**

```bash
git add litellm-session-logs/
git commit -m "litellm-session-logs: add session-log extraction pipeline (moved from ~/tmp)"
```

---

### Task 2: Require the input path in `build_transcript.py`

**Files:**
- Modify: `litellm-session-logs/build_transcript.py` (docstring usage line; arg handling at lines 12–14)

- [ ] **Step 1: Save a before-copy of the existing transcript for the equivalence check**

```bash
cp ~/tmp/litellm-session-logs/session_logs_08cd3034-transcript.md /tmp/transcript-before-change.md
```

- [ ] **Step 2: Apply the edit**

In `litellm-session-logs/build_transcript.py`, replace:

```python
Usage: python3 build_transcript.py [session_logs_<id>.json]
Defaults to session_logs_08cd3034.json in this directory.
"""
import json
import sys
from datetime import datetime, timedelta
from pathlib import Path

HERE = Path(__file__).resolve().parent
SRC = Path(sys.argv[1]) if len(sys.argv) > 1 else HERE / "session_logs_08cd3034.json"
```

with:

```python
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
```

- [ ] **Step 3: Verify no-args exits with usage**

```bash
python3 litellm-session-logs/build_transcript.py; echo "exit: $?"
```
Expected: the docstring is printed, then `exit: 1`.

- [ ] **Step 4: Verify output is byte-identical to the pre-change script**

```bash
python3 litellm-session-logs/build_transcript.py ~/tmp/litellm-session-logs/session_logs_08cd3034.json
diff /tmp/transcript-before-change.md ~/tmp/litellm-session-logs/session_logs_08cd3034-transcript.md && echo IDENTICAL
```
Expected: `wrote ...transcript.md` plus the turns/errors/model-switches summary, then `IDENTICAL` (the change only touched arg handling; same input → same output as the original script's default-arg run from Sep 18).

- [ ] **Step 5: Commit**

```bash
git add litellm-session-logs/build_transcript.py
git commit -m "litellm-session-logs: require input path for build_transcript.py"
```

---

### Task 3: Package `CLAUDE.md` for the new directory

**Files:**
- Create: `litellm-session-logs/CLAUDE.md`

- [ ] **Step 1: Write the file**

Create `litellm-session-logs/CLAUDE.md` with exactly:

````markdown
# CLAUDE.md

Standalone pipeline (psql SQL + sh + stdlib-only Python) for pulling one
LiteLLM proxy session's logs out of the `LiteLLM_SpendLogs` Postgres table
and reconstructing a readable Markdown chat transcript from them. Not wired
into modelman or wt — run manually, per session, from this directory.

## Pipeline

1. **`01_export_session_logs.sql`** — runs against the live DB, dumps
   `request_id, startTime, status, request_duration_ms, prompt_tokens,
   completion_tokens, spend, messages, response` for one `session_id`,
   ordered by time, to `session_logs_<id>.json`.
   ```
   psql postgresql://keith@localhost:5432/litellm \
     -v session_id="'<session-id>'" \
     -f 01_export_session_logs.sql
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
3. **`build_transcript.py <session_logs_<id>.json>`** — builds an
   **assistant-only** transcript from the JSON export alone (`response`
   column). Use when `messages` is empty (`{}`, the normal case for
   standard chat completions) and you don't have `proxy_server_request`.
4. **`build_full_transcript.py <session_logs.json> <proxy_requests.ndjson>
   [output.md]`** — builds a **two-sided** transcript by joining the JSON
   export's `response` column against the ndjson export's
   `proxy_server_request.messages`. Requires step 2's output.

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
  Both transcript builders emit an `[ERROR]` marker and *infer* a likely
  cause from token/spend counts (zero everything → rejected before reaching
  the provider, e.g. a proxy-side budget/limit; nonzero → provider was
  reached, e.g. mid-stream timeout) — this inference is not logged fact.
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
  `~/tmp/litellm-session-logs/` (moved here from there 2026-09-25); point
  the builders at those files by path, or write fresh exports into /tmp.
- These files can be large (the `.ndjson` and `-transcript-full.md` files
  run tens to hundreds of MB) — prefer targeted `grep`/`jq`/line-range reads
  over loading a whole file when investigating a specific turn or thread.
- Related guide: [docs/guides/07-usage-and-spend.md](../docs/guides/07-usage-and-spend.md)
  covers the same `LiteLLM_SpendLogs` table via `modelman usage report`.
````

- [ ] **Step 2: Commit**

```bash
git add litellm-session-logs/CLAUDE.md
git commit -m "litellm-session-logs: package CLAUDE.md"
```

---

### Task 4: Wire the export script into `make lint-shell`

**Files:**
- Modify: `Makefile:3-12` (`SHELL_SCRIPTS` list)

- [ ] **Step 1: Add the script to `SHELL_SCRIPTS`**

In `Makefile`, replace:

```make
	benchmarks/lib/benchmark-multi.sh \
	wt/bin/*-wt \
	wt/scripts/agents-smoke.sh
```

with:

```make
	benchmarks/lib/benchmark-multi.sh \
	wt/bin/*-wt \
	wt/scripts/agents-smoke.sh \
	litellm-session-logs/02_export_proxy_server_request.sh
```

- [ ] **Step 2: Run lint**

Run: `make lint-shell`
Expected: every listed file passes `bash -n`; shellcheck (if installed) exits 0; no errors.

- [ ] **Step 3: Commit**

```bash
git add Makefile
git commit -m "Makefile: lint litellm-session-logs export script"
```

---

### Task 5: Register the directory in root `CLAUDE.md` and README

**Files:**
- Modify: `CLAUDE.md` (Architecture list)
- Modify: `README.md` (component table + repo-layout tree)

- [ ] **Step 1: Add the Architecture bullet**

In `CLAUDE.md`, replace:

```markdown
- `modelman/` — model registry TUI/CLI (Python/uv; `src/modelman`, own `CLAUDE.md`, own `Makefile`). Canonical owner
```

with:

```markdown
- `litellm-session-logs/` — standalone pipeline (psql SQL + sh + stdlib-only Python, own CLAUDE.md) that pulls one LiteLLM proxy session's request/response logs from the Postgres `LiteLLM_SpendLogs` table and rebuilds readable chat transcripts; not wired into modelman/wt, run manually per session
- `modelman/` — model registry TUI/CLI (Python/uv; `src/modelman`, own `CLAUDE.md`, own `Makefile`). Canonical owner
```

- [ ] **Step 2: Update the README component table**

In `README.md`, replace:

```markdown
| Root (`bin/`, `benchmarks/`, `docs/`) | backends (LiteLLM proxy, Ollama, oMLX) + LaunchAgents + benchmarks + user guides |
```

with:

```markdown
| Root (`bin/`, `benchmarks/`, `docs/`, `litellm-session-logs/`) | backends (LiteLLM proxy, Ollama, oMLX) + LaunchAgents + benchmarks + user guides + session-log extraction |
```

- [ ] **Step 3: Add the README tree line**

In `README.md`, replace:

```markdown
├── benchmarks/         # legacy benchmark scripts + write-ups
│   └── results/        # benchmark output artifacts
```

with:

```markdown
├── benchmarks/         # legacy benchmark scripts + write-ups
│   └── results/        # benchmark output artifacts
├── litellm-session-logs/ # pull one LiteLLM session's request/response logs from Postgres, rebuild transcripts — has its own CLAUDE.md
```

- [ ] **Step 4: Verify links**

Run: `make check-links`
Expected: `ALL LINKS OK` (check-links auto-covers the new CLAUDE.md via `git ls-files`).

- [ ] **Step 5: Commit**

```bash
git add CLAUDE.md README.md
git commit -m "docs: register litellm-session-logs in CLAUDE.md and README"
```

---

### Task 6: Cross-link from guide 07

**Files:**
- Modify: `docs/guides/07-usage-and-spend.md` ("Going deeper" section)

- [ ] **Step 1: Add the cross-link**

In `docs/guides/07-usage-and-spend.md`, replace:

```markdown
- LiteLLM wiring and spend logging setup: [01-initial-setup](01-initial-setup.md), [04-litellm-config](04-litellm-config.md).
- This is a leaf guide — nothing further builds on it in `docs/guides/`.
```

with:

```markdown
- LiteLLM wiring and spend logging setup: [01-initial-setup](01-initial-setup.md), [04-litellm-config](04-litellm-config.md).
- Raw request/response text for one specific session (not aggregate spend): `litellm-session-logs/` at the repo root — a standalone pipeline that pulls a session's rows from `LiteLLM_SpendLogs` and rebuilds a readable chat transcript; see `litellm-session-logs/CLAUDE.md`.
- This is a leaf guide — nothing further builds on it in `docs/guides/`.
```

- [ ] **Step 2: Verify link**

Run: `make check-links`
Expected: `ALL LINKS OK` (the bullet references the CLAUDE.md as inline code, not a markdown link, so no link target needed).

- [ ] **Step 3: Commit**

```bash
git add docs/guides/07-usage-and-spend.md
git commit -m "docs: cross-link guide 07 to litellm-session-logs"
```

---

### Task 7: Final verification and cleanup of `~/tmp` originals

**Files:** none in the repo (final verification + deleting the moved originals from `~/tmp/litellm-session-logs/`)

- [ ] **Step 1: Full-repo lint and links**

Run: `make lint`
Expected: `bash -n`/shellcheck pass on all `SHELL_SCRIPTS`, then `ALL LINKS OK`.

- [ ] **Step 2: Equivalence check for `build_full_transcript.py`**

```bash
python3 litellm-session-logs/build_full_transcript.py \
  ~/tmp/litellm-session-logs/session_logs_d50322e1.json \
  ~/tmp/litellm-session-logs/session_d50322e1_proxy_requests.ndjson \
  /tmp/transcript-full-check.md
diff /tmp/transcript-full-check.md ~/tmp/litellm-session-logs/session_logs_d50322e1-transcript-full.md && echo IDENTICAL
```
Expected: the script's summary output (wrote, threads, errors), then `IDENTICAL`.

- [ ] **Step 3: Read-only smoke of the sh script against the live DB**

First find a real session id (read-only):
```bash
psql postgresql://keith@localhost:5432/litellm -t -c \
  'SELECT DISTINCT session_id FROM "LiteLLM_SpendLogs" ORDER BY session_id DESC LIMIT 5;'
```
Expected: up to 5 session-id rows (UUID-shaped strings). If the DB is unreachable, note it and move on — the script is a verbatim copy and its logic is already exercised by Task 7 Step 2's inputs.

Then run the export with an explicit output path (never into the repo dir):
```bash
litellm-session-logs/02_export_proxy_server_request.sh <session-id-from-step-2> /tmp/psr-smoke.ndjson
wc -l /tmp/psr-smoke.ndjson
head -c 300 /tmp/psr-smoke.ndjson
```
Expected: `wrote /tmp/psr-smoke.ndjson`, a nonzero line count (or `0` if `store_prompts_in_spend_logs` was off when that session was captured — the column is `{}` per the CLAUDE.md caveat; empty-but-valid JSON objects per line still prove the query works), and a JSON line starting `{"request_id": ...`.

- [ ] **Step 4: Delete the moved originals from `~/tmp` (keep generated outputs)**

```bash
rm ~/tmp/litellm-session-logs/01_export_session_logs.sql \
   ~/tmp/litellm-session-logs/02_export_proxy_server_request.sh \
   ~/tmp/litellm-session-logs/build_transcript.py \
   ~/tmp/litellm-session-logs/build_full_transcript.py \
   ~/tmp/litellm-session-logs/CLAUDE.md
ls ~/tmp/litellm-session-logs/
```
Expected: only generated artifacts remain (`session_*` files plus the unrelated `code-review-skill-inHIGH.md` analysis note, which is not part of the pipeline and stays).

- [ ] **Step 5: Confirm repo state clean**

```bash
git status --short
git log --oneline main..HEAD
```
Expected: no unexpected modified/untracked repo files (untracked `smoke_*.log` files at repo root predate this work and stay); six commits on the branch.

---

## Self-review notes (already applied)

- Spec coverage: SQL comment note → Task 1; required-arg fix → Task 2; package CLAUDE.md → Task 3; Makefile → Task 4; root CLAUDE.md + README → Task 5; guide 07 → Task 6; lint/check-links/smoke/`~/tmp` deletion → Task 7. Non-goals (no numbered guide, no extra parameterization, no proxy-config changes) appear in no task, as intended.
- Equivalence checks (Tasks 2 and 7) prove the copies behave identically to the originals before the originals are deleted.