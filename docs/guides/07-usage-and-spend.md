# Usage and spend — reconcile wt launch history against LiteLLM spend with `modelman usage report`

> Use this to: see which models wt actually launched, what LiteLLM logged in tokens and dollars, and where the two disagree — one read-only Markdown report.
>
> Verified against: modelman 0.1.0, wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29

## Prerequisites

- **LiteLLM proxy running with Postgres spend logging** — the stack from [01-initial-setup](01-initial-setup.md) / [04-litellm-config](04-litellm-config.md). Spend rows land in the Postgres `LiteLLM_SpendLogs` table; without it, the Requests/tokens/Spend columns and the reconciliation have nothing to read (launch counts still work).
- **wt launch history exists**: `/Users/keith/.config/agent-wt/usage.jsonl` — wt appends one JSON line per TUI launch (guide 06 §7). First line here:
  ```json
  {"model_id":"ollama/gemma4:9b","timestamp":"2026-08-22T15:00:03.102105Z"}
  ```
- **modelman component** at `/Users/keith/github/ohanaverse/local-ai-setup/modelman`. Always invoke as `uv run modelman` from there — modelman is not installed globally (Gotchas).
- Everything in this guide is **read-only**: it reads `usage.jsonl`, `rotation.state`, and the Postgres spend table; it mutates nothing.

## TL;DR

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/modelman
uv run modelman usage report --days 7 | tee /tmp/usage-$(date +%F).md
```

Full Markdown report to stdout, saved copy in `/tmp/usage-2026-08-29.md` (same file every day-of-run, overwritten on rerun).

For a recurring snapshot, tee to a persistent path (e.g. `~/notes/usage-$(date +%F).md`) — `/tmp` is purged by macOS every 3 days.

## Steps

### 1. Run the report

Check the flags first (they changed here — always `uv run modelman`):

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/modelman
uv run modelman usage report --help
```

Expected (live 2026-08-29, minus uv's unrelated `VIRTUAL_ENV` stderr warning):

```
 Usage: modelman usage report [OPTIONS]

 Print a usage report reconciling wt launches and LiteLLM spend.

╭─ Options ────────────────────────────────────────────────────────────────────╮
│ --days          <int range> [x>=1]  Number of days to report on [default: 7] │
│ --model         <str>               Filter to a registry model id            │
│ --family        <str>               Filter to a model family                 │
│ --help                              Show this message and exit.              │
╰──────────────────────────────────────────────────────────────────────────────╯
```

Run the default 7-day report:

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/modelman
uv run modelman usage report --days 7
```

Real output shape (2026-08-29): all section headings, verbatim rows — one WT-only ollama row, the one matched row, and one LiteLLM-only openrouter row (spend logs contain no keys, nothing redacted):

```markdown
# Usage Report — 2026-08-22 to 2026-08-29

## Summary

| Family | Model | WT launches (1d/7d/30d) | Requests | Prompt tokens | Completion tokens | Spend |
|---|---|---:|---:|---:|---:|---:|
| deepseek-v4-flash:cloud | ollama/deepseek-v4-flash:cloud | 7 / 15 / 17 | 0 | 0 | 0 | $0.0000 |
| qwen3.8:27b-mlx | ollama/qwen3.8:27b-mlx | 0 / 4 / 5 | 4 | 152 | 630 | $0.0000 |
| openrouter | openrouter/qwen/qwen3.8-27b | 0 / 0 / 0 | 5 | 517 | 3,135 | $0.0085 |

⋮ — 18 more real rows in between, same shape; 21 total, one per model seen on either side

## Reconciliation

### WT-only launches
- ollama/deepseek-v4-flash:cloud — 15 launches in the last 7 days, no LiteLLM spend
- ⋮ 13 more rows (14 today) ⋮

### LiteLLM-only spend
- openrouter/qwen/qwen3.8-27b — $0.0085 spend, 0 wt launches
- ⋮ ⋮

## Last wt launch

**ollama/glm-5.3-flash:cloud**
```

### 2. Reading the report

- **Summary table** — one row per model found on *either* side (launch history or LiteLLM spend), sorted by family. Launch columns `1d/7d/30d` are fixed buckets from `usage.jsonl`, independent of `--days`. `Requests/tokens/Spend` come from `LiteLLM_SpendLogs` within the `--days` window. A row with both launches and requests = the model actually went through LiteLLM for agent traffic (the `ollama/qwen3.8:27b-mlx` row above). There is **no separate "matched" section** — a match is just a row with both numbers populated.
- **`## Reconciliation`** has exactly two subsections:
  - `### WT-only launches` — models with launches in the fixed 7-day bucket but zero `LiteLLM_SpendLogs` rows inside the `--days` window.
  - `### LiteLLM-only spend` — models LiteLLM logged spend for with no wt launch.
- **`## Last wt launch`** — the bare model id from `/Users/keith/.config/agent-wt/rotation.state` (single global slot, written only by the wt TUI launch path — see [06-wt-agents-and-models](06-wt-agents-and-models.md)). Live both read `ollama/glm-5.3-flash:cloud`.

### 3. Interpreting mismatches

- **Matched rows** (launches > 0 *and* requests > 0): agent traffic flowed through the LiteLLM proxy. Normal state for models exposed via LiteLLM.
- **WT-only launches**: wt launches with no LiteLLM spend — the agent reached the model's native API directly (ollama `:11434` or its cloud endpoints, llama.cpp `:8080`, oMLX `:8000`) and those requests never log to Postgres. On this box that is *most* rows (all the `ollama/...` ones), not a bug. Spends only reconcile for models that went through LiteLLM.

  After enabling `[gateway]` in `wt`, non-native launches route through LiteLLM,
  so matched rows should become the norm. Remaining `WT-only` rows are usually:

  - native models (unmetered subscriptions)
  - OpenCode launches (not routed through the gateway in this release)
  - traffic that bypassed wt entirely (e.g. direct `curl` to Ollama/llama.cpp/oMLX)

- **LiteLLM-only spend**: something used the proxy without a wt launch in the window — ad-hoc `curl`/scripts/other clients. The live `openrouter/qwen/*` rows ($0.0003–$0.0085) are exactly this.
- **Filters narrow but don't warn.** Live tests (both exit 0):
  - `--family openrouter` → Summary table and reconciliation shrink to that family. `## Last wt launch` is *not* affected by any filter.
  - `--model openrouter/qwen/qwen3.8-27b` → single row.
  - Zero-match output looks **different** — no tables at all, just:
    ```markdown
    # Usage Report — 2026-08-22 to 2026-08-29

    No usage data found in the requested window.

    ## Last wt launch

    **ollama/glm-5.3-flash:cloud**
    ```
    Don't script against an expected empty table; it's a sentence instead.

### 4. Where the data comes from

- `/Users/keith/.config/agent-wt/usage.jsonl` — one JSON object per wt TUI launch, `model_id` + `timestamp` only (no tokens, no cost, no keys). Sample line quoted in Prerequisites.
- `/Users/keith/.config/agent-wt/rotation.state` — one line, bare model id; source of `Last wt launch`.
- Postgres table `LiteLLM_SpendLogs` — LiteLLM's standard spend log (rows per proxy request: model, tokens, cost). modelman connects with the same `DATABASE_URL` the LiteLLM LaunchAgent uses, taken from the env block of `/Users/keith/Library/LaunchAgents/local.litellm.proxy.plist` (verified present 2026-08-29; value deliberately not printed here). Local Postgres allows passwordless access — the table lives in the `litellm` database (the path component of `DATABASE_URL`), not the default `postgres` one. Probe:

  ```bash
  psql litellm -tAc 'select count(*) from "LiteLLM_SpendLogs"'
  ```

  Expected: `77` rows (live 2026-08-29 — rerun it yourself; the current count grows with every LiteLLM-routed request). Stack setup: [01-initial-setup](01-initial-setup.md), [04-litellm-config](04-litellm-config.md).

## Verification

- Exit code 0 and Markdown on stdout:

  ```bash
  # from: /Users/keith/github/ohanaverse/local-ai-setup/modelman
  uv run modelman usage report --days 7 | head -5
  ```

  Expected (live): `# Usage Report — <from> to <today>`, blank line, `## Summary`, blank line, then the `|`-delimited header row starting `| Family | Model | WT launches (1d/7d/30d) | …`.
- `--days 1` narrows the spend window (live): header becomes `# Usage Report — 2026-08-28 to 2026-08-29` (from the 7-day range), and rows whose only spend is older drop to `Requests 0` (live: `ollama/qwen3.8:27b-mlx` went 4 requests → 0). Caveat: the WT-only bullet counts stay on their fixed 7-day window and membership may shift — see Gotchas.
- No mutations: report runs only read `usage.jsonl`, `rotation.state`, and Postgres (the design spec pins the command read-only — Going deeper). `git status` in `/Users/keith/github/ohanaverse/local-ai-setup/modelman` shows nothing after running.

## Gotchas

- **Only LiteLLM-routed traffic produces spend.** Native/direct launches (ollama cloud, llama.cpp `:8080`, oMLX `:8000`) never appear in LiteLLM spend — they surface as `WT-only launches`. Expect that section to be long on this box.
- **Run modelman from the `modelman/` directory.** `modelman usage report --days 1` from a globally installed binary would fail with `No such command 'usage'`. Always `uv run modelman` from `/Users/keith/github/ohanaverse/local-ai-setup/modelman` (same trap as guide 02 Gotchas).
- **`--days` doesn't move every window.** It re-scopes the header range and the LiteLLM spend matching; launch columns stay fixed `1d/7d/30d` buckets and WT-only bullet *counts* stay on the fixed 7-day window — but bullet *membership* shifts: a model whose spend falls inside the 7-day default but outside the smaller `--days` window drops out of "matched" and becomes a WT-only bullet (live: `ollama/qwen3.8:27b-mlx` matched at `--days 7`, a WT-only bullet at `--days 1`).
- **`rotation.state` is the *last* TUI launch, nothing more** — one global slot; `esc`/canceled prompts never touch it. It is not a usage summary (guide 06).
- **Point-in-time snapshot.** Every launch appends to `usage.jsonl` and LiteLLM logs to Postgres asynchronously — rerun tomorrow (or in a minute) and numbers shift. There is no live/budget dashboard here.
- uv prints a one-line `VIRTUAL_ENV=…does not match the project environment path` pyenv warning to stderr on every invocation — unrelated noise, ignore it.

## Going deeper

- Design of the whole reconciliation: `/Users/keith/github/ohanaverse/local-ai-setup/modelman/docs/superpowers/specs/2026-08-28-modelman-usage-design.md` — data sources, window rules, non-goals (read-only), and the SQL it runs against `LiteLLM_SpendLogs`.
- Launch/rotation side of the data: [06-wt-agents-and-models](06-wt-agents-and-models.md) (picker, `rotation.state` life cycle, `usage.jsonl` writer).
- LiteLLM wiring and spend logging setup: [01-initial-setup](01-initial-setup.md), [04-litellm-config](04-litellm-config.md).
- This is a leaf guide — nothing further builds on it in `docs/guides/`.
