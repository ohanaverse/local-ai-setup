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

Input side, from the same `message.usage` (also in `--csv` rows):

| Column | Definition | Reliability |
|--------|------------|-------------|
| `ctx_tokens` | `input_tokens` + `cache_read_input_tokens` + `cache_creation_input_tokens` | What the model read for the call. `None` when the transcript has no `input_tokens`. |
| `cache_hit` | `cache_read_input_tokens` / `ctx_tokens` | `None` unless both cache fields are present. `0.0` means the route returned them as zero, which is what a gateway that drops prompt caching would look like (a guess until seen on real data). |
| `first_main` | the first call of each model in a transcript that is not a subagent (`isSidechain`), if it is paired | Marks the call that approximates session startup size. Per model, so a small Haiku side call does not stand in for the main model. If a model's first call had no `api-request` record, none of its calls is flagged. |
| `requested_model` | `requestedModel` on the assistant record | Absent in older transcripts. On the author's data it is either missing or equal to `message.model`. |

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

### Comparing two environments (`--json`, `--compare`)

To test whether models run slower in one environment than another (say a gateway route against a direct
subscription), run `--json` on each machine and compare the two files on either one.

```bash
uv run python transcript_speed.py --json ~/.claude/projects > machine-a.json   # on machine A
uv run python transcript_speed.py --json ~/.claude/projects > machine-b.json   # on machine B
uv run python transcript_speed.py --compare machine-a.json machine-b.json
```

`--json` (`"schema": 2`) holds only aggregates, no session names or paths, so it is safe to copy between machines:

- **`bands`**: per model and output-size band (`<200`, `200-1000`, `1000-3000`, `3000+`; the unit is output
  tokens per call, thinking included, and calls under `--min-tokens` are dropped first, so `<200` means 50–199).
  Call count, `tps_total` p50/p90, `ttfb_s` and `total_s` p50/p90, `ctx_tokens` p50/p90 and `cache_hit` p50.
- **`ctx_bands`**: per model and context-size band (`<10k`, `10k-30k`, `30k-100k`, `100k+` tokens read per
  call): call count and `tps_total` p50/p90, `ttfb_s`/`total_s` p50.
- **`startup`**: per model, `ctx_tokens` p50/p90 of each session's first main-thread call, counted over every paired
  call, not only those above `--min-tokens` (a session's first call is often a short tool-use turn).
- **`fallbacks`**: how many calls recorded a `requestedModel`, how many were answered by a different model once a
  `[1m]` suffix, a `provider/` prefix, a trailing date and a bare alias (`sonnet` for `claude-sonnet-5-5`) are
  allowed for, and the requested → answered pairs. `<synthetic>` error turns are skipped. A planner alias such as
  `opusplan` still shows up as a pair; read the pairs before treating them as fallbacks.
  A zero does not prove there were no fallbacks. On a corporate machine that reported overload fallbacks from
  `claude-sonnet-5-5` to `claude-sonnet-5`, every `claude-sonnet-5` answer carried a matching `requestedModel`
  (`claude-sonnet-5` or `claude-sonnet-5[1m]`), so Claude Code appears to record the model it used, not the one
  first asked for. The only trace was the separate `claude-sonnet-5` rows in the bands, and those can also be a
  deliberate model choice; the OTel `attempt` field or a gateway log tells the two apart.
- **`min_tokens`** it was run with.

`--compare` prints the output-band table first, then context tokens and cache hit per band, speed by context
size, session startup, and requested-vs-answered counts, with the percent difference of B against A. It warns when
the two files used different `--min-tokens` (the smallest band then covers different ranges), notes a file with
no `min_tokens`, notes a file from before `schema` 2 (its new figures show `n/a` and its output bands may not
match the new ones; regenerate it), and lists model/band pairs that appear on only one side.

Reading the result:

- Compare within a model. An overall summary mixes models, and Opus runs slower than Sonnet, so a different
  Opus share on one side moves the overall number by itself.
- Compare within a band. `tps_total` includes prefill and network time, so short outputs look slow, and two
  environments with different mixes of short and long calls differ for that reason alone.
- Day-to-day spread on one machine was about 20% on the median (p50 79–102 for Sonnet 5.5 over two weeks), so a
  smaller gap than that is noise. Treat bands with fewer than a few hundred calls on either side as anecdotal.
- A gateway that buffers streaming makes `ttfb_s` approach `total_s`; compare `total_s` and `tps_total` first.
- Telling a bigger prompt from a slower route (these are readings to test, not established rules):
  a bigger prompt adds time before the first block (`ttfb_s`, via prefill) and shows up as higher `ctx_tokens`
  or a lower `cache_hit`; decode speed should barely depend on prompt size. So a gap that persists within one
  context band, especially in the time after the first block, points at the route; a gap that closes within
  a context band points at prompt size. Constant extra time before the first block in every output band fits
  either a network hop or extra prefill, and only the context columns tell them apart.
- Startup size is the cleanest bloat figure: it counts what plugins, MCP servers, skills and `CLAUDE.md` imports
  inject before the first message. It is one call per session, so look at `n` before trusting a percentile.
- This shows that one side is slower, and with the context columns, whether prompt size explains it. Machine,
  network, gateway and server-side limits still look the same from client timestamps. Both sides need Claude
  Code versions that write `api-request` records.
- The model name in transcripts can differ by route (for example a gateway alias); `--compare` matches names
  exactly, so rows with different spellings show up under "only in A/B".

### What this can't give you

True time-to-first-token and pure decode rate need data from the wire. Options, from least to most work:

1. **Claude Code OpenTelemetry traces**: the `claude_code.llm_request` span carries `ttft_ms`, `duration_ms`,
   `output_tokens`, `attempt` and `success`. See [OpenTelemetry traces](#opentelemetry-traces-otel_receiverpy-otel_speedpy).
2. **Local logging proxy**: run a proxy on localhost, set `ANTHROPIC_BASE_URL` to it, and have it forward to
   the gateway while logging time-to-first-byte, time-to-last-byte, and the SSE token events. This is the only
   local route to true decode rate measured on the wire, and it has to be built.

## Controlled probes: probe_runner.py

Organic sessions cannot say *why* one environment is slower, because real calls carry different context
sizes and output lengths. `probe_runner.py` runs the same fixed prompts through `claude -p` on any machine,
reads each run's transcript back with `transcript_speed`'s call timing, and writes an aggregate-only JSON
that can be copied between machines. It goes through `claude -p` on purpose, so Claude Code's own overhead
(startup, hooks, plugin and MCP loading, prompt assembly) is part of what is measured. Design:
[the spec](../../docs/superpowers/specs/2026-10-29-claude-code-environment-probes-design.md).

```bash
uv run python probe_runner.py run --label home                 # writes probe-home.json
uv run python probe_runner.py run --label corp                 # on the other machine
uv run python probe_runner.py --compare probe-home.json probe-corp.json
```

`run` options: `--runs N` (default 10 per case and variant), `--model` (default `claude-sonnet-5-5`, always
passed so a default like `opusplan` cannot change what is measured), `--variants`, `--cases`, `--out`.
Runs are sequential, interleaved as round → case → variant, and progress goes to stderr.

| Case | What it isolates |
|------|------------------|
| `tiny` | Fixed overhead: startup context, hooks, plugin and MCP load, minimum call time. |
| `count` | Decode speed at a known output length (about 630 tokens). |
| `fill20k`, `fill80k` | How first-token time grows with prompt size: fixed synthetic filler, then a one-word answer. The size is *added* to the normal startup context; compare the `ctx_tokens` each run reports, not the name. |

| Variant | Flags |
|---------|-------|
| `default` | The machine's normal configuration. |
| `lean` | `--setting-sources project --strict-mcp-config --disable-slash-commands`, from the empty probe directory: no user plugins, MCP servers or skills. Managed settings still load and cannot be disabled. |

`--bare` is not used: it never reads OAuth or keychain credentials, so a subscription login could not
authenticate. The output's `startup` section gives each variant's `tiny` context tokens, so you can see how
lean `lean` really got on that machine.

How a run is judged:

- **Unique request, same prefix.** Each prompt ends with a random `(ref N)` tag, so the gateway's response
  cache cannot answer it (an identical request was seen replayed in about 0.15 s), while the filler prefix stays
  identical and can still be prompt-cached. Every run uses one fixed empty directory, because the working
  directory is part of the prompt Claude Code builds.
- **Cold and warm.** Run 0 of each (case, variant) is reported as `cold`; runs 1.. drive the percentiles. A warm
  run with `cache_hit` under 0.5 was not warm: it is left out of the percentiles and counted as
  `cache_cold_warm`. If the *median* warm hit is itself under 0.5, the prompt changes every session (a hook
  that injects the time, for example), so nothing would be left: every warm run is kept and the group is
  flagged `cache_partial`, which `--compare` shows as "partial cache". Read such a cell as "a new session each
  time", not as a cached steady state. A gap over 5 minutes between runs of one (case, variant) warns on stderr.
- **Discards.** A run is dropped and counted by reason (`error`, `bad-json`, `timeout`, `no-transcript`,
  `no-call`, `extra-calls`). The measured call is the one answered by the pinned model; a side call to another
  model is ignored.

| Column | Definition | Reliability |
|--------|------------|-------------|
| `wall_s` | the whole `claude -p` process, timed by the harness | Measured. |
| `api_s` | `duration_api_ms` from the JSON | Claude Code's own figure. |
| `client_overhead_s` | `duration_ms` − `duration_api_ms` | Per-turn overhead (prompt assembly). On one real run it was about 50 ms, equal to the JSON's `time_to_request_ms`, so it is *not* process startup. |
| `startup_s` | `wall_s` − `duration_ms` | Estimate of process start, hooks, plugin and MCP loading. |
| `ttft_s` | `ttft_ms` from the JSON | Time to first token as Claude Code measures it. |
| `ttfb_s`, `total_s`, `tps_total` | as in the table above, from the transcript | Estimates, same caveats as `transcript_speed.py`. |
| `ctx_tokens`, `cache_hit` | as above | From `message.usage`. |

`--compare` refuses files with different pinned models or an unknown schema, warns (and still compares) when
Claude Code versions or run counts differ, compares only the (case, variant) pairs both files have, and
prints two derived estimates: `config cost` (default minus lean `wall_s` for `tiny`) and `prompt-size slope`
(`ttfb_s` growth per 10k context tokens, `tiny` to `fill80k`). The route difference is the B-versus-A
difference for the same case and variant.

Readings to test, not established rules:

| Pattern | Likely meaning |
|---------|----------------|
| `tiny` slower at one site under both variants | Fixed per-call route cost. |
| `tiny` default much slower than lean on one machine | Configuration (hooks, plugins, MCP) costs time. |
| One site's slope per 10k tokens is steeper | Large-context handling on that route. |
| `count` slower with equal `ctx_tokens` | Streaming rate of the route. |
| `startup_s` large and similar on both | Claude Code itself, not the route. |

Running it on another machine: copy `probe_runner.py` and `transcript_speed.py` into one directory and run with
any Python 3.11 or newer. Probe sessions are written to `~/.claude/projects` like any session, under a folder
carrying `claude-utilities-probe-cwd`; `transcript_speed.py` skips them unless given `--include-probes`.

On a managed or gateway machine, check once: the gateway accepts the pinned model name as passed to `--model`;
the cases raise no permission prompts; the lean flags really shrink the reported `startup`. A default run is
80 calls, mostly cached input, a few dollars at most, and it will show in the organisation's usage dashboards and
managed telemetry. The prompts are synthetic and contain nothing sensitive.

## OpenTelemetry traces: otel_receiver.py, otel_speed.py

`otel_receiver.py` is a loopback-only OTLP/HTTP server that appends each trace export to a JSONL file
(`~/.cache/claude-utilities/otel-traces.jsonl` by default). `otel_speed.py` reads that file and prints the same
kind of percentile table as `transcript_speed.py`, with `ttft_s` and `tps_decode` added. Both are stdlib-only.

```bash
uv run python otel_receiver.py --port 4318            # terminal 1; Ctrl-C to stop

# terminal 2: start Claude Code with traces pointed at the receiver
CLAUDE_CODE_ENABLE_TELEMETRY=1 CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1 \
OTEL_TRACES_EXPORTER=otlp OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/json \
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:4318/v1/traces claude

uv run python otel_speed.py ~/.cache/claude-utilities/otel-traces.jsonl
```

The receiver answers 415 to anything that is not JSON, so a wrong `OTEL_EXPORTER_OTLP_TRACES_PROTOCOL` shows up
on stderr instead of as an empty file. Traces are a Claude Code beta feature, so attribute names may change.

| Column | Definition | Reliability |
|--------|------------|-------------|
| `ttft_s` | `ttft_ms` / 1000 | Client-measured time to first token. Not checked against gateway logs. |
| `total_s` | `duration_ms` / 1000 | Wall time of the call. |
| `tps_total` | `output_tokens` / `total_s` | Lower bound on decode speed (includes ttft). |
| `tps_decode` | `output_tokens` / (`total_s` − `ttft_s`) | Closer to decode speed; the first token is counted without its time. Needs ≥ 1s after ttft. |

Failed calls and calls with `attempt` > 1 are skipped, because their duration covers more than one request.

### Status

- **Not run end-to-end.** The unit tests use payloads shaped from the docs. Parsing a real OTLP export has not
  been done. A console-exporter run on one machine did show a real `llm_request` span with all of these fields.
- **Managed settings can block it.** If your organisation's managed settings set `OTEL_EXPORTER_OTLP_ENDPOINT`,
  Claude Code removes developer-set per-signal endpoints at startup (per the Claude Code docs), and spans may go to
  the managed endpoint instead of the receiver. On the author's machine no trace reached a local receiver and
  none was captured, so the path is unconfirmed. Do not override managed settings; ask whoever owns them.
- **Spans include identity.** Resource and span attributes can carry `user.email`, `user.id` and `session.id`.
  Treat the JSONL as private, and don't commit it.
