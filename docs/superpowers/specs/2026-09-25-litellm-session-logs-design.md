# Design: Move the LiteLLM session-log pipeline into the repo

**Date:** 2026-09-25
**Status:** Approved

## Problem

The scripts for pulling one LiteLLM proxy session's logs out of the
`LiteLLM_SpendLogs` Postgres table and rebuilding readable chat transcripts
live ad hoc in `~/tmp/litellm-session-logs/` — outside version control,
undocumented in the repo, and invisible to the repo's lint and link checks.
`~/tmp` is also purge territory on macOS, so the only copies of working
tooling sit in a scratch directory.

## Source inventory

`~/tmp/litellm-session-logs/` contains four scripts, one context file, and
generated output:

| File | Role |
|---|---|
| `01_export_session_logs.sql` | psql dump of one `session_id`'s `LiteLLM_SpendLogs` rows (metadata + `response` column) |
| `02_export_proxy_server_request.sh` | `\copy` of the `proxy_server_request` column (real request bodies) to ndjson, CSV-format with `\x01`/`\x02` control chars to avoid Postgres TEXT double-escaping |
| `build_transcript.py` | assistant-only transcript from the SQL export alone (stdlib-only) |
| `build_full_transcript.py` | two-sided transcript joining `response` against `proxy_server_request.messages`, with multi-thread grouping (sub-agent header ground truth, tool-call-ID heuristic fallback) |
| `CLAUDE.md` | pipeline how-to plus hard-won data-model caveats |
| `session_*` files | generated outputs (~378 MB) for three already-analyzed sessions — working artifacts, not tooling |

## Chosen approach

Option B from the brainstorm: move the tooling into a new top-level
`litellm-session-logs/` directory, applying repo conventions during the move.
Rejected alternatives: verbatim move (keeps a hardcoded historical-session
default, no lint coverage, no gitignore) and a full numbered guide
(`docs/guides/12-*`) duplicating the colocated CLAUDE.md — overkill for a
personal ad-hoc pipeline (YAGNI).

## Changes

### New directory `litellm-session-logs/`

| File | Change |
|---|---|
| `01_export_session_logs.sql` | Verbatim except the usage comment gains the `LITELLM_DATABASE_URL` note (matching the sh script's env override) |
| `02_export_proxy_server_request.sh` | Verbatim (`#!/bin/sh` kept — POSIX, shellcheck-clean) |
| `build_transcript.py` | JSON path becomes a required arg; the hardcoded `session_logs_08cd3034.json` default is removed |
| `build_full_transcript.py` | Verbatim (already requires args) |
| `CLAUDE.md` | Kept as package-level context (matches `modelman/CLAUDE.md`, `wt/CLAUDE.md`); intro rewritten from "ad-hoc scripts in ~/tmp" to repo-component framing; pipeline commands updated to repo-relative paths; data-model caveats preserved verbatim |
| `.gitignore` (new) | `session_*` — pipeline outputs keep landing next to the scripts (same workflow as today) but stay untracked |

### Repo wiring

- **Makefile** — add `litellm-session-logs/02_export_proxy_server_request.sh`
  to `SHELL_SCRIPTS` so `make lint-shell` covers it.
- **Root `CLAUDE.md`** — one Architecture bullet: standalone stdlib-only
  pipeline that pulls one session's request/response logs from
  `LiteLLM_SpendLogs` Postgres and rebuilds transcripts; has its own CLAUDE.md.
- **README.md** — repo-layout tree line, and the root-scope table row mentions
  the directory.
- **`docs/guides/07-usage-and-spend.md`** — 1–2 line cross-link from the
  spend-logs guide: for raw request/response text of one session, use
  `litellm-session-logs/`.
- **`make check-links`** — no wiring needed; targets derive from
  `git ls-files`, so tracked markdown in the new directory is covered
  automatically.

### What stays in `~/tmp`

The generated session dumps and transcripts stay in
`~/tmp/litellm-session-logs/` (session-specific working artifacts,
regenerable by re-running the pipeline). Source scripts are moved — deleted
from `~/tmp/litellm-session-logs/` after the copy.

## Non-goals

- No new numbered guide in `docs/guides/` — the colocated CLAUDE.md is the doc.
- No parameterization beyond the required-arg fix in `build_transcript.py`
  (DB URL override already exists in the sh script; ET timezone offset in the
  timestamp formatter stays as-is).
- No changes to LiteLLM proxy config (`store_prompts_in_spend_logs` remains
  off; the scripts document the gate rather than enabling it).

## Verification

1. `make lint-shell` and `make check-links` pass.
2. Pipeline smoke test against existing `~/tmp` artifacts, outputs to /tmp:
   `build_transcript.py` on a `session_logs_*.json`, and
   `build_full_transcript.py` with its matching ndjson — proves the arg
   change did not break the pipeline.
3. Read-only run of `02_export_proxy_server_request.sh` against the live DB
   with a known session id, output to /tmp.