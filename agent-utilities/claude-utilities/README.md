# Claude Code utilities

## transcript_speed.py — tokens/second from local session transcripts

Estimates Claude Code output speed per API call using only files already on disk: the session
transcripts in `~/.claude/projects/*/*.jsonl`. Read-only, stdlib-only Python 3.13, managed with uv like the repo's other Python packages.

```bash
uv run python transcript_speed.py ~/.claude/projects            # summary per model
uv run python transcript_speed.py --csv path/to/session.jsonl   # per-call rows
uv run python transcript_speed.py --min-tokens 200 ~/.claude/projects
```

Arguments are `.jsonl` files or directories (searched recursively). `--min-tokens N` skips calls with
fewer output tokens (default 50), because tiny calls are dominated by latency.

### Why this works when LiteLLM isn't local

When `ANTHROPIC_BASE_URL` points at a remote gateway, the gateway's spend logs and Prometheus
metrics aren't reachable from the machine. The client-side transcript still records, per call:

- an `api-request` record with the time the request was sent
- one `assistant` line per content block (thinking / text / tool_use), each with a timestamp
- `message.usage.output_tokens`

### Columns

| Column | Definition | Reliability |
|--------|------------|-------------|
| `tps_total` | final `output_tokens` / (last block time − request send time) | Lower bound on decode speed: includes network, queueing, and prefill. **Use this one.** |
| `ttfb_s` | first block time − request send time | Not a clean time-to-first-token: includes the whole first content block, which can be long for thinking. |
| `total_s` | last block time − request send time | Wall time of the call. |
| `tps_window` | `output_tokens` / (last block time − first block time), multi-block calls with a window ≥ 1s | **Overstated; ignore.** Blocks are timestamped when written, not as tokens arrive, and the first block's tokens are counted without its time. |

`output_tokens` includes thinking tokens.

### Gotchas

- `usage.output_tokens` is the **final total**, repeated on every content block of a message. It is not a running count, so per-block speed can't be derived from it.
- Timestamps are client-side. They were not checked against gateway logs.
- Records are grouped by consecutive `message.id`; each is paired with the most recent preceding `api-request`.
  Calls with no preceding `api-request` are dropped. On the author's data that was 39% of messages
  (3,376 of 8,754), but the cause is the transcript format, not the kind of call: `api-request` records
  first appear on 2026-10-14, so every older transcript contributes nothing. From 2026-10-15 on, at most a
  handful of messages a day are dropped. Results therefore cover only recent sessions, and a Claude Code
  version that predates `api-request` records yields an empty summary.
- Retries are not a practical concern. Only 0.5% of requests (27 of 5,405) were followed by another request
  before any response, about 5 minutes later (likely timeouts). The later request is the one that produced the
  response, so the retried call is timed from its last send and `tps_total` is not inflated.
- Percentiles use nearest-rank (`ceil(p·n)−1`). Lines that are not JSON objects, or have unparseable
  timestamps, are skipped.

### Sample result (2026-10-26, 961 transcripts, 5,307 calls ≥ 50 output tokens)

| Model | Calls | `tps_total` p50 | `tps_total` p90 | `ttfb_s` p50 |
|-------|------:|----------------:|----------------:|-------------:|
| claude-sonnet-5-5 | 4,987 | 97.6 | 137.5 | 3.7 |
| claude-opus-5-5 | 245 | 86.4 | 102.0 | 7.1 |
| claude-haiku-5-5 | 34 | 189.8 | 207.4 | 16.8 |
| claude-sonnet-5 | 41 | 53.1 | 91.5 | 4.0 |

Haiku has few calls and a slow median first block, so its row is likely dominated by a few odd calls.

### What this can't give you

True time-to-first-token and pure decode rate need data from the wire. Options, from least to most work:

1. **Claude Code OpenTelemetry**: set `OTEL_METRICS_EXPORTER=console` and `OTEL_LOGS_EXPORTER=console`
   (or point `OTEL_EXPORTER_OTLP_ENDPOINT` at a local collector). Gives token counts, cost, and per-request
   `duration_ms`. Not time-to-first-token. Not verified on this machine.
2. **Local logging proxy**: run a proxy on localhost, set `ANTHROPIC_BASE_URL` to it, and have it forward to
   the gateway while logging time-to-first-byte, time-to-last-byte, and the SSE token events. This is the only
   local route to true TTFT and decode rate, and it has to be built.
