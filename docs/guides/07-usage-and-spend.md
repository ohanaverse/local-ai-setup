# Usage and spend — launches and LiteLLM spend per model with `wt stats`

> Use this to: see which models wt launched, and what LiteLLM logged for them in requests, tokens and dollars, over one window.
>
> Verified against: wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29 · revised 2026-10 (commands checked against `wt --help` and `llmbench --help`, not re-run live)

## Prerequisites

- **LiteLLM proxy running with Postgres spend logging** — the stack from [01-initial-setup](01-initial-setup.md) / [04-litellm-config](04-litellm-config.md). Spend rows land in the Postgres `LiteLLM_SpendLogs` table; without it the REQUESTS, PROMPT, COMPLETION and SPEND columns have nothing to read (launch counts still work).
- **wt launch history exists**: `~/.config/agent-wt/usage.jsonl` — wt appends one JSON line per TUI launch (guide 06 §7). Example line (captured 2026-08-29 — your model ids will differ):
  ```json
  {"model_id":"ollama/gemma4:9b","timestamp":"2026-08-22T15:00:03.102105Z"}
  ```
- `wt` on PATH (`make install` from the repo root) and `psql` on PATH.
- Everything in this guide is **read-only**: it reads `usage.jsonl` and the Postgres spend table; it mutates nothing.

## TL;DR

```bash
wt stats --window 7d
```

Two tables for one window: the survey table, then the usage table (launches and LiteLLM spend per model). For a copy to keep, `wt stats --json` prints the same report as one JSON line; append it to a file to build a history:

```bash
wt stats --json >> ~/notes/wt-stats.jsonl
```

## Steps

### 1. Run it

```bash
wt stats --window 7d
```

`--window` is `1d`, `7d` or `30d` (default `30d`); there are no other lengths. Example usage table (illustrative values — your models and numbers will differ; the survey table above it is left out here):

```
MODEL                        LAUNCHES  REQUESTS     PROMPT  COMPLETION    SPEND
ollama/gemma4:9b                    2     1,204  1,234,567         630  $0.0000
openrouter/qwen/qwen3.8-27b         0         5        517       3,135  $0.0085
```

### 2. Read the table

One row per model seen on either side, launch history or LiteLLM spend. Both columns cover the same window: after `as_of - window`, up to and including `as_of`.

- **Launches and requests both above 0**: agent traffic went through the LiteLLM proxy. Normal for a model routed through LiteLLM.
- **Launches with `0` requests**: the launch bypassed the proxy. A native or direct launch (ollama `:11434` or its cloud endpoints, oMLX `:8000`) never reaches LiteLLM and logs nothing to Postgres. When most of your launches are native, most rows look like this; it is not a bug.
- **Requests with `0` launches**: something used the proxy that wt did not launch in the window — `curl`, a script, another client. A session launched just before the window starts shows the same row.

After `wt litellm on`, non-native launches route through LiteLLM, so rows with both numbers become the norm.

### 3. Filters

| Flag | Narrows |
|---|---|
| `--model <id>` | both tables to one model id |
| `--family <family>` | the usage table to one model family |
| `--agent <agent>` | the survey table and the launch counts to one agent; spend is then not shown, and a note on stderr says so |

### 4. Where the data comes from

- `~/.config/agent-wt/usage.jsonl` — one JSON object per wt TUI launch, `model_id` + `timestamp` only (no tokens, no cost, no keys). Sample line quoted in Prerequisites.
- Postgres table `LiteLLM_SpendLogs` — LiteLLM's standard spend log (one row per proxy request: model, tokens, cost). Local Postgres allows passwordless access; the table lives in the `litellm` database, not the default `postgres` one. Probe:

  ```bash
  psql litellm -tAc 'select count(*) from "LiteLLM_SpendLogs"'
  ```

  Expected: a single integer, the number of logged proxy requests. `0` (or a `relation does not exist` error) means spend logging is not wired up. Stack setup: [01-initial-setup](01-initial-setup.md), [04-litellm-config](04-litellm-config.md).

wt finds the database with nothing exported. The connection string is the first of `WT_LITELLM_DATABASE_URL`; `MODELMAN_LITELLM_DATABASE_URL` (the older name, still read); `general_settings.database_url` in LiteLLM's `config.yaml`; and `DATABASE_URL`, when `config.yaml` names none. `DATABASE_URL`, and a `config.yaml` value written `os.environ/NAME`, are looked up in your shell and then in the proxy's LaunchAgent plist, as the proxy itself sees them.

Without `psql`, a reachable database or a configured URL, the launch counts still print, the spend cells show `-`, one note on stderr says why (naming the host and port wt tried, when it tried one), and the exit code is 0. Requests the proxy logged with no model are counted in a note on stderr. With a missing or unreadable `registry.toml` the report still prints; a model's family is then its id's provider prefix.

## What `modelman usage report` had that `wt stats` does not

`wt stats` replaced `modelman usage report` ([08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md), "Where modelman's commands went"). Four things were dropped:

- **An arbitrary `--days N`.** The windows are `1d`, `7d` and `30d`.
- **Markdown output.** `wt stats` prints a plain table, or one JSON document with `--json`.
- **The Reconciliation sections.** Read them off the table (Step 2).
- **The "Last wt launch" line.** `cat ~/.config/agent-wt/rotation.state` shows it: one global slot, the last TUI launch and nothing more (guide 06).

Totals can differ at the edges of a window from a report made with the old command: wt compares the proxy's timestamps as UTC, and leaves out a request logged exactly at the start of the window, as it leaves out a launch at that instant.

## Verification

- `wt stats --window 1d` exits 0. With launches or spend in the window, the usage table's header row starts `MODEL`; with none, it prints `no usage data`.
- The JSON form has four top-level keys:

  ```bash
  wt stats --json | python3 -c 'import json,sys; print(sorted(json.load(sys.stdin)))'
  ```

  Expected: `['as_of', 'survey', 'usage', 'window']`.
- No mutations: a run only reads `usage.jsonl` and Postgres.

## Gotchas

- **Only LiteLLM-routed traffic produces spend.** Native and direct launches (ollama cloud, oMLX `:8000`) never appear in LiteLLM spend: they are rows with launches and `0` requests. Expect many if most of your launches are native.
- **`rotation.state` is the *last* TUI launch, nothing more** — one global slot; `esc`/canceled prompts never touch it. It is not a usage summary (guide 06), and `wt stats` does not print it.
- **Point-in-time snapshot.** Every launch appends to `usage.jsonl` and LiteLLM logs to Postgres asynchronously — rerun tomorrow (or in a minute) and the numbers shift. There is no live/budget dashboard here.

## Going deeper

- Full reference for the command: [wt/docs/wt-stats.md](../../wt/docs/wt-stats.md).
- Design of the report `wt stats` replaced (history): `~/github/ohanaverse/local-ai-setup/modelman/docs/superpowers/specs/2026-08-28-modelman-usage-design.md` — data sources, window rules, and the SQL it ran against `LiteLLM_SpendLogs`.
- Launch/rotation side of the data: [06-wt-agents-and-models](06-wt-agents-and-models.md) (picker, `rotation.state` life cycle, `usage.jsonl` writer).
- LiteLLM wiring and spend logging setup: [01-initial-setup](01-initial-setup.md), [04-litellm-config](04-litellm-config.md).
- Raw request/response text for one specific session (not aggregate spend): `litellm-session-logs/` at the repo root — a standalone pipeline that pulls a session's rows from `LiteLLM_SpendLogs` and rebuilds a readable chat transcript; see `litellm-session-logs/CLAUDE.md`.
- This is a leaf guide — nothing further builds on it in `docs/guides/`.
