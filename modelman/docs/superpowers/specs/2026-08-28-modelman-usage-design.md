# Sub-project 3 — Usage/Spend Tracking Design

**Scope:** Add a `modelman usage` subcommand that reconciles `wt`'s launch
history (`usage.jsonl` + `rotation.state`) with LiteLLM's Postgres spend logs
and produces a single combined report.

**Status:** Design approved in chat; awaiting implementation plan.

**Companion tracker:**
`agent-worktree/docs/superpowers/plans/2026-08-27-model-management-consolidation-status.md`

---

## Background

Sub-project 1 established a shared model registry owned by `modelman` and
consumed read-only by `wt`. Sub-project 2 added a registry-aware benchmark
tool to `modelman`.

There are still two independent views of actual model usage:

1. **`wt` local state** — `~/.config/agent-wt/usage.jsonl` records which
   model was launched and when (model_id + timestamp). `rotation.state`
   tracks the single most recent launch. These exist because `wt` needs
   launch counts for its model picker badges and rotation.

2. **LiteLLM spend DB** — when requests are routed through the LiteLLM
   proxy, it logs `LiteLLM_SpendLogs` to Postgres with spend, tokens,
   provider, timestamps, and a `model_name` that maps back to the
   `model_list` entry.

The two views disagree by design: `wt` records intent ("I launched this
model"), while LiteLLM records execution ("this model actually served
requests"). A request may go direct to a backend, bypassing LiteLLM
entirely. A request may also hit LiteLLM from a source other than `wt`.

Sub-project 3 exposes that difference rather than hiding it.

---

## Goals

1. Provide a single `modelman usage report` command that joins `wt` launch
   history with LiteLLM spend data.
2. Show spend, token counts, and request counts per registry model/family.
3. Highlight mismatches between `wt` launches and LiteLLM spend.
4. Read-only: do not modify `usage.jsonl`, `rotation.state`, or the LiteLLM
   DB.
5. Keep the implementation in `modelman`, the registry owner, and follow the
   sub-project 2 package pattern.

---

## Non-goals

- Real-time enrichment of `usage.jsonl` as `wt` launches.
- Replacing `wt`'s `usage.jsonl` with queries against LiteLLM.
- Writing spend data back into `modelman.toml` or `registry.toml`.
- Predicting or estimating cost when LiteLLM has no data.
- Supporting non-Postgres LiteLLM storage backends in v1.

---

## Architecture

### Module layout (modelman)

```
src/modelman/
├── usage/
│   ├── __init__.py
│   ├── cli.py            # typer wiring for `modelman usage`
│   ├── db.py             # LiteLLM Postgres spend-log adapter
│   ├── wt_state.py       # read usage.jsonl + rotation.state
│   ├── reconcile.py      # join wt launches, LiteLLM spend, registry models
│   ├── report.py         # Markdown output
│   └── errors.py         # UsageError
├── registry.py           # model/family metadata
├── litellm.py            # read model_list from config.yaml
└── main.py               # add `usage` subcommand
```

### Data sources

1. **`~/.config/agent-wt/usage.jsonl`** — `model_id` + `timestamp`.
2. **`~/.config/agent-wt/rotation.state`** — single last-launched model id.
3. **`~/.config/litellm/config.yaml`** — `general_settings.database_url`.
4. **`registry.toml`** — canonical model/family metadata.
5. **`LiteLLM_SpendLogs`** — actual spend and token usage.

### Environment overrides

- `MODELMAN_LITELLM_DATABASE_URL` — override the database URL from
  `config.yaml`.
- `MODELMAN_WT_DIR` — override `~/.config/agent-wt` for tests.

---

## CLI surface

```bash
modelman usage report                  # report last 7 days
modelman usage report --days 1           # last 24 hours
modelman usage report --days 30          # last 30 days
modelman usage report --model-family qwen3.8
modelman usage report --model ollama/qwen3.8:27b-mlx
```

Output is Markdown to stdout.

---

## Database adapter

### Connection source

Read `general_settings.database_url` from `~/.config/litellm/config.yaml`.
Allow override via `MODELMAN_LITELLM_DATABASE_URL`.

### Protocol

```python
@dataclass
class SpendLogRow:
    request_id: str | None
    model_name: str | None      # LiteLLM model_list key, ideally registry id
    litellm_model: str | None   # e.g. "ollama_chat/qwen3.8:27b-mlx"
    provider: str | None
    spend: float
    prompt_tokens: int
    completion_tokens: int
    total_tokens: int
    start_time: datetime


class SpendStore(Protocol):
    def query(
        self,
        *,
        start: datetime,
        end: datetime,
        model_names: list[str] | None = None,
    ) -> list[SpendLogRow]: ...
```

### Postgres implementation

Default query:

```sql
SELECT
    request_id,
    model_name,
    model AS litellm_model,
    custom_llm_provider AS provider,
    spend,
    prompt_tokens,
    completion_tokens,
    total_tokens,
    startTime AS start_time
FROM "LiteLLM_SpendLogs"
WHERE startTime >= %s AND startTime <= %s
ORDER BY startTime DESC
```

If `model_names` is provided, add:

```sql
AND model_name = ANY(%s)
```

### Mapping fallback

Some spend rows have `model_name = NULL`. Build a reverse index from the
`model_list` in LiteLLM config:

```python
litellm_model_to_model_name: dict[str, str]
# e.g. "ollama_chat/qwen3.8:27b-mlx" -> "ollama/qwen3.8:27b-mlx"
```

For rows with missing `model_name`, use `litellm_model` to recover the
`model_name`, then match that to a registry model id.

---

## wt state reader

### usage.jsonl

Read all lines within the report window. Return per-model launch counts
for 1d, 7d, and 30d windows (reusing the same windows `wt` uses for its
picker badges, but scoped to the report's end date).

### rotation.state

Return the last-launched model id, if any.

---

## Reconciliation

For each registry model in scope, produce:

```python
@dataclass
class ModelUsage:
    registry_model_id: str
    family: str
    wt_launches_1d: int
    wt_launches_7d: int
    wt_launches_30d: int
    litellm_requests: int
    prompt_tokens: int
    completion_tokens: int
    spend: float
```

Scope rules:

- If `--model` is given, report only that model.
- If `--model-family` is given, report all models in that family.
- Otherwise, report all models in `registry.toml` that have either a `wt`
  launch in the 30-day window or LiteLLM spend in the report window.

### Reconciliation sections

- **Matched** — models present in both `wt` launches and LiteLLM spend.
- **WT-only launches** — `wt` launched the model but LiteLLM has no spend
  in the report window. Indicates direct backend access, LiteLLM offline,
  or cached responses.
- **LiteLLM-only spend** — LiteLLM has spend for the model but `wt` shows
  no launches in the 30-day window. Indicates another client used the
  proxy.

---

## Report format

Markdown output:

```markdown
# Usage Report — 2026-08-21 to 2026-08-28

## Summary
| Family | Model | WT launches (1d/7d/30d) | Requests | Prompt tokens | Completion tokens | Spend |
|---|---|---:|---:|---:|---:|---:|
| qwen3.8 | ollama/qwen3.8:27b-mlx | 5 / 12 / 34 | 48 | 5,432 | 6,602 | $0.0342 |
| qwen3.8 | openrouter/qwen/qwen3.8-flash | 2 / 5 / 8 | 7 | 1,200 | 4,500 | $0.1200 |
...

## Reconciliation

### WT-only launches
- ollama/gemma4:9b — 3 launches in the last 7 days, no LiteLLM spend

### LiteLLM-only spend
- openrouter/qwen/qwen3.8-27b — $0.08 spend, 0 wt launches

## Last wt launch
**ollama/kimi-k2.6:cloud**
```

---

## Testing strategy

- Unit-test `wt_state.py` with canned `usage.jsonl` and `rotation.state`
  files.
- Unit-test `db.py` with a SQLite in-memory store that implements the
  same `SpendStore` protocol. The Postgres adapter itself is exercised with
  an optional integration test behind an env flag.
- Unit-test `reconcile.py` join logic with fake `ModelUsage` inputs.
- Unit-test `report.py` output with a small reconciled report and assert
  expected Markdown lines.
- CLI test invokes `modelman usage report` with mocks for DB and wt state.

---

## Dependencies

- `psycopg2-binary` (or `psycopg[binary]`) for Postgres connectivity.
- `pyyaml` already present for LiteLLM config parsing.

---

## Future extensions (out of scope for v1)

- Filter by agent (claude/codex/copilot/etc.) if `wt` ever records agent
  in `usage.jsonl`.
- Export to CSV or JSON.
- Daily rollup instead of per-request query.
- Support LiteLLM's Admin API as an alternative backend.
- Cached/background sync so reports are instantaneous.
