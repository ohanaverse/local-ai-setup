# Usage and spend — launches and LiteLLM spend per model with `wt stats`

> Use this to: see which models wt launched, and what LiteLLM logged for them in requests, tokens and dollars, over one window.
>
> Verified against: wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29 · revised 2026-10 (commands checked against `wt --help` and `llmbench --help`, not re-run live)

## Prerequisites

- **LiteLLM proxy running with Postgres spend logging** — the stack from [01-initial-setup](01-initial-setup.md) / [04-litellm-config](04-litellm-config.md). Spend rows land in the Postgres `LiteLLM_SpendLogs` table and reach `wt stats` through the model server (`wt-model serve`); without the table the REQUESTS, PROMPT, COMPLETION and SPEND columns have nothing to read (launch counts still work).
- **wt launch history exists**: `~/.config/agent-wt/usage.jsonl` — wt appends one JSON line per model launch, from the picker or not (guide 06 §8). Example line (captured 2026-08-29 — your model ids will differ, and a current line also carries an `agent` field):
  ```json
  {"model_id":"ollama/gemma4:9b","timestamp":"2026-08-22T15:00:03.102105Z"}
  ```
- **Survey history exists** (optional — only the survey table needs it): `~/.config/agent-wt/survey.jsonl` — the post-session survey's answers, one JSON line each, 30-day retention. **The survey is switched off** (#136), so no new answers are recorded: the survey table reports what is already in the file, and reads `no survey data` on a fresh install or once the last answers age out.
- `wt` on PATH (`make install` from the repo root) and a model server configured (`[model-server]` in `~/.config/agent-wt/config.toml`, [wt-session.md](../../wt/docs/wt-session.md)); `psql` is needed only on the machine that runs `wt-model serve`, where the spend rows are read.
- Everything in this guide is **read-only**: it reads `usage.jsonl`, `survey.jsonl` and the model server's spend query (which reads the Postgres spend table on the server's host); it mutates nothing.

## TL;DR

```bash
wt stats --window 7d
```

Two tables for one window: the survey table, then the usage table (launches and LiteLLM spend per model). The survey table covers the answers already in `survey.jsonl`; the survey is switched off (#136), so expect `no survey data` unless answers were recorded while it was on. For a copy to keep, `wt stats --json` prints the same report as one JSON line; append it to a file to build a history:

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
- **Launches with `0` requests**: the launch bypassed the proxy. A native launch (the agent's own subscription model) never reaches LiteLLM and logs nothing to Postgres; every non-native launch goes through the model server's gateway and so through LiteLLM. When most of your launches are native, most rows look like this; it is not a bug.
- **Requests with `0` launches**: something used the proxy that wt did not launch in the window — `curl`, a script, another client. A session launched just before the window starts shows the same row.

Every non-native launch goes through the model server's gateway and so through LiteLLM, so rows with both numbers are the norm.

### 3. Filters

| Flag | Narrows |
|---|---|
| `--model <id>` | both tables to one model id |
| `--family <family>` | the usage table to one model family |
| `--agent <agent>` | the survey table and the launch counts to one agent; spend is then not shown, and a note on stderr says so |

### 4. Where the data comes from

One source per table — the survey table's first, then the usage table's two:

- `~/.config/agent-wt/survey.jsonl` — the **survey table's** only source: one JSON object per answered post-session survey (agent, model, verdict, speed, quality, task). wt writes it at exit when the survey asks; the survey is switched off (#136), so it holds only older answers and thins out as they pass 30 days.
- `~/.config/agent-wt/usage.jsonl` — one JSON object per model launch by wt: `model_id`, `agent` and `timestamp` (no tokens, no cost, no keys). Each write drops lines older than 30 days; a line written before the `agent` field existed has none. Sample line quoted in Prerequisites.
- Postgres table `LiteLLM_SpendLogs`, read for `wt stats` by the model server (`GET /control/v1/spend`) — LiteLLM's standard spend log (one row per proxy request: model, tokens, cost), on the machine that runs `wt-model serve`. Local Postgres allows passwordless access; the table lives in the `litellm` database, not the default `postgres` one. Probe (on that machine):

  ```bash
  psql litellm -tAc 'select count(*) from "LiteLLM_SpendLogs"'
  ```

  Expected: a single integer, the number of logged proxy requests. `0` (or a `relation does not exist` error) means spend logging is not wired up. Stack setup: [01-initial-setup](01-initial-setup.md), [04-litellm-config](04-litellm-config.md).

The model server finds the database with nothing exported. The connection string is the first of `WT_LITELLM_DATABASE_URL`; `MODELMAN_LITELLM_DATABASE_URL` (the older name, still read); `general_settings.database_url` in LiteLLM's `config.yaml`; and `DATABASE_URL`, when `config.yaml` names none. `DATABASE_URL`, and a `config.yaml` value written `os.environ/NAME`, are looked up in the server's environment and then in the proxy's LaunchAgent plist, as the proxy itself sees them. The address is read on the host and never sent to a client.

With no model server configured the spend cells show `-` and one note says why (spend status `not_configured`); a server with no database named reads the same, and a database it could not query (or a server that is not answering) is spend status `unavailable`. For either status that one note reads `spend unavailable: <reason>`, and the exit code is 0 either way. Requests the proxy logged with no model are counted in a note on stderr. With no catalog row for a model the report still prints; its family is then its id's provider prefix.

## What `modelman usage report` had that `wt stats` does not

`wt stats` replaced `modelman usage report` ([08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md), "Where modelman's commands went"). Four things were dropped:

- **An arbitrary `--days N`.** The windows are `1d`, `7d` and `30d`.
- **Markdown output.** `wt stats` prints a plain table, or one JSON document with `--json`.
- **The Reconciliation sections.** Read them off the table (Step 2).
- **The "Last wt launch" line.** `cat ~/.config/agent-wt/last-picked.state` shows it: the row launched last, a model or a slot profile, and nothing more. `rotation.state` beside it is the last single-model launch (guide 06).

Totals can differ at the edges of a window from a report made with the old command: wt compares the proxy's timestamps as UTC, and leaves out a request logged exactly at the start of the window, as it leaves out a launch at that instant.

## Verification

- `wt stats --window 1d` exits 0. With launches or spend in the window, the usage table's header row starts `MODEL`; with none, it prints `no usage data`. The survey table above it does the same: rows for the answers in the window, `no survey data` when there are none — which is the usual state while the survey is switched off.
- The JSON form has four top-level keys:

  ```bash
  wt stats --json | python3 -c 'import json,sys; print(sorted(json.load(sys.stdin)))'
  ```

  Expected: `['as_of', 'survey', 'usage', 'window']`.
- No mutations: a run only reads `survey.jsonl`, `usage.jsonl` and the model server's spend query (Postgres on the server's host, read-only).

## Gotchas

- **Only LiteLLM-routed traffic produces spend.** A native launch (the agent's own subscription model) never appears in LiteLLM spend: it is a row with launches and `0` requests. Expect those when some of your launches are native.
- **`rotation.state` is the *last* single-model launch, nothing more** — one global slot; `esc`/canceled prompts and slot-profile launches never touch it. It is not a usage summary (guide 06), and `wt stats` does not print it.
- **A slot profile is a row with launches and no spend.** A launch of `profile/<name>` (guide 06 §10) is counted on its own row, and the spend columns there read `-`. The proxy logs the session's requests under the slot models, so the spend is on their rows, which can show requests without a launch. wt does not attribute spend back to a profile.
- **Point-in-time snapshot.** Every model launch appends to `usage.jsonl` and LiteLLM logs to Postgres asynchronously — rerun tomorrow (or in a minute) and the numbers shift. There is no live/budget dashboard here.

## Going deeper

- Full reference for the command: [wt/docs/wt-stats.md](../../wt/docs/wt-stats.md).
- Design of the report `wt stats` replaced (history): `~/github/ohanaverse/local-ai-setup/docs/superpowers/modelman/specs/2026-08-28-modelman-usage-design.md` — data sources, window rules, and the SQL it ran against `LiteLLM_SpendLogs`.
- Launch/rotation side of the data: [06-wt-agents-and-models](06-wt-agents-and-models.md) (picker, `rotation.state` life cycle, `usage.jsonl` writer).
- LiteLLM wiring and spend logging setup: [01-initial-setup](01-initial-setup.md), [04-litellm-config](04-litellm-config.md).
- Raw request/response text for one specific session (not aggregate spend): `litellm-session-logs/` at the repo root — a standalone pipeline that pulls a session's rows from `LiteLLM_SpendLogs` and rebuilds a readable chat transcript; see `litellm-session-logs/CLAUDE.md`.
- This is a leaf guide — nothing further builds on it in `docs/guides/`.
