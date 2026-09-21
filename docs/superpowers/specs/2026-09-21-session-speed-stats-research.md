# Measured session speed stats — research spike (issue #136)

Date: 2026-09-21. Status: **research findings only; no design approved, no code written.**

Question (issue #136, "can speed survey be replaced with measured stats"): can agents, model
providers, or LiteLLM supply measured speed (tokens/sec, time to first token, latency) for a wt
session, instead of the 1–5 speed rating in the post-session survey? The solution must work for
every agent × model combination, but the method may differ per agent or per provider.

Evidence labels used below: **[live]** = observed on this machine, **[docs]** = from
documentation or memory only, **[unverified]** = a claim that still needs a test.

## 1. Where speed is recorded today

- Survey speed rating: `wt/internal/survey/prompt.go` (question), `wt/internal/survey/stats.go`
  (`SpeedAvg`). Shown as `s<speed>` in the model picker's SURVEY column and in `wt stats`.
  Store: `~/.config/agent-wt/survey.jsonl`.
- wt usage store (`wt/internal/usage/usage.go`) is only a launch counter
  (`event{ModelID, Agent, Timestamp}` in `usage.jsonl`). It does not read LiteLLM or any timing.
- modelman reads LiteLLM spend from Postgres: `modelman/src/modelman/usage/db.py:70-104`
  (`PostgresSpendStore.query`). It selects request_id, model_group, tokens, spend and
  `"startTime"`. It does **not** select `"endTime"`, `"completionStartTime"` or
  `request_duration_ms`. Guide: `docs/guides/07-usage-and-spend.md`.
- modelman benchmarks already measure speed client-side: `modelman/src/modelman/benchmark/workloads/base.py`
  (`_streaming_run`) and `chat_streaming.py` time the HTTP stream (TTFT = first content chunk,
  tok/s = completion_tokens / (total − TTFT), using `stream_options.include_usage`);
  `benchmark/agent/pidriver.py` stamps TTFT per turn. Nothing uses provider-native timing fields,
  so those numbers include network and proxy overhead.

## 2. LiteLLM

### Data available [live]

Table `LiteLLM_SpendLogs` (Postgres db `litellm`, LiteLLM healthy with db connected) has:
`"startTime"`, `"endTime"`, `request_duration_ms`, `"completionStartTime"`, `prompt_tokens`,
`completion_tokens`, `model_group`, `model`, `custom_llm_provider`, `api_base`, `session_id`,
`end_user`, `user`, `request_tags` (jsonb), `metadata` (jsonb), `status`, `agent_id`.
All 43,053 rows had `completionStartTime`, `request_duration_ms` and `session_id` non-NULL, but
some recent rows have `request_duration_ms = 0`, empty `model_group` and 0 completion tokens, so
they must be filtered.

Formulas:
- total latency = `endTime − startTime`
- TTFT = `completionStartTime − startTime`
- decode tok/s = `completion_tokens / (endTime − completionStartTime)`
- end-to-end tok/s = `completion_tokens / total`

Other surfaces [docs]: `/spend/logs` API (needs master key), response headers
`x-litellm-response-duration-ms`, `x-litellm-overhead-duration-ms`, `x-litellm-response-cost`,
`x-litellm-model-id`, `x-litellm-call-id`; Prometheus histograms
(`litellm_request_total_latency_metric`, `litellm_llm_api_time_to_first_token_metric`, …) which
need the `prometheus` callback (not in `~/.config/litellm/config.yaml`) and are aggregated, not
per request. Postgres is the best fit. Sources: docs.litellm.ai/docs/proxy/logging_spec,
/prometheus, /response_headers, /request_headers.

### Caveats

- Non-streaming requests have TTFT = total, so decode tok/s cannot be derived.
- Reasoning tokens may be hidden by some backends, so `completion_tokens` can be off.
- Tiny outputs and tool-call turns give noisy tok/s. Filter to `completion_tokens ≥ ~30`, use
  medians per model.
- Latency includes proxy overhead (small) and network time for OpenRouter and cloud models, so
  it is not pure model speed.
- **Coverage gap:** guide 07 (lines ~102-104) says most launches on this machine are native or
  direct (Ollama `:11434` cloud models, oMLX `:8000`) and never appear in spend logs. The mtplx
  model does appear in recent rows (routed).
- wt is Go with no Postgres dependency. Reading spend logs means shelling out to modelman, adding
  a driver, or calling `/spend/logs` with a master key.

### Session identification [live]

Checked 2026-09-21 against the last 7 days (2,103 rows, 496 distinct `session_id`):

- **Claude Code: exact.** Claude Code sends `metadata.user_id` as JSON
  `{"device_id","account_uuid","session_id"}`. LiteLLM stores that in the `user` column and
  uses its `session_id` as the row's `session_id`. 47 sessions had more than one row; the
  largest had 803 rows over ~3.5h. Two concurrent Claude sessions on the same model therefore get
  different `session_id`s. `device_id` is the same for every session on this machine, so only
  `session_id` separates them.
- **pi: not identifiable today.** 9,595 rows tagged `User-Agent: pi (…)` had 9,595 distinct
  `session_id`s, so every request has its own random id (LiteLLM's default when the client sends
  none). opencode is the same (38 rows, 38 ids).
- Codex and curl rows also carry only a `User-Agent` tag (`codex_exec/…`, `curl/…`), no session.
- LiteLLM does not keep request headers in `metadata.proxy_server_request` for pi rows, so what
  pi really sends could not be confirmed from the logs.
- LiteLLM's session-attribution request headers [docs, unverified on this proxy]:
  `x-litellm-session-id` (or `x-litellm-trace-id`), `x-litellm-tags` (comma-separated →
  `request_tags`), `x-litellm-spend-logs-metadata` (JSON → `metadata`), `x-litellm-end-user-id`
  (→ `end_user`).

## 3. Model providers (independent of LiteLLM)

Only Ollama and MTPLX were checked live (as of the run, oMLX, mlx_lm.server and OpenRouter
were not running: ports 8000-8003 and 8080 refused).

| Provider | Stats available | Passive vs active | Evidence |
|---|---|---|---|
| Ollama (`:11434`) native `/api/generate`, `/api/chat` | Final object: `total_duration`, `load_duration`, `prompt_eval_count`, `prompt_eval_cached_count`, `prompt_eval_duration`, `eval_count`, `eval_duration` (ns). Gen tok/s = `eval_count/eval_duration`. TTFT is not a field, roughly `load_duration + prompt_eval_duration`. | Active only (response body). `/metrics` is 404; `~/.ollama/logs/server.log` has only GIN status/latency lines, no tokens. | [live] `ornith-1.5:9b`, 16 tokens: load 4.35s, prompt eval 0.18s / 11 tokens, eval 0.58s |
| Ollama `/v1/chat/completions` | OpenAI `usage` only; streaming needs `include_usage`. No timings. | Active | [live] |
| oMLX (`omlx`, `omlx-6bit`) | OpenAI-style `usage`. Admin dashboard/stats API unknown. | Possibly passive via admin endpoints or service logs | [docs/unverified] |
| mlx_lm.server | OpenAI `usage` only, no server timing. Log: `/tmp/local-ai-setup-mlx-lm-server.log` (`backends/mlx_lm_server.py`); verbose output may hold per-request stats. | Log passive but unverified | [unverified] |
| MTPLX (`:8003`) | Server log `/tmp/local-ai-setup-mtplx.log` gets JSON per request: `{"event":"mtplx_openai_generation", prompt_tokens, completion_tokens, elapsed_s, tok_s, end_to_end_tok_s, finish_reason, …}`; also cache events and warmup `tok_s`. | Passive (tailable) | [live] from an older run; API body extras unchecked |
| OpenRouter | Response `usage` (tokens, cost). `GET /api/v1/generation?id=<gen-id>`: `generation_time`, native/normalized token counts, `streamed`, cost, `created_at` (a `latency` field is mentioned in some docs but not in the example seen). | Active, two steps; stats can lag | [docs] not run |

Takeaways: the only uniform source is `usage` plus client-side timing. Native timing is
Ollama-specific (per response only). MTPLX is the only provider with a passive per-request log
carrying `tok_s`.

## 4. Agent CLIs (wt configures claude, codex, pi, copilot, opencode, agy)

Field names inspected on real local files; conversation content was not read.

- **Copilot [live] — best.** `~/.copilot/session-store.db`, table `assistant_usage_events`
  (~3,968 rows), one row per request: `model`, `input_tokens`, `output_tokens`,
  `cache_read_tokens`, `cache_write_tokens`, `reasoning_tokens`, `duration_ms`,
  `time_to_first_token_ms`, `inter_token_latency_ms`, `output_ttft_ms`, `finish_reason`,
  `api_endpoint`, `turn_index`, `session_id`, `created_at`. tok/s = `output_tokens/duration_ms`.
  `~/.copilot/session-state/<id>/events.jsonl` adds `session.shutdown` totals. Works with
  non-GitHub models (a `deepseek-v4.1-flash:cloud` row was seen).
- **Codex [live].** `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl`, `event_msg` records:
  `task_started.started_at`, `token_count` (`last_token_usage` per request, `total_token_usage`),
  `task_complete` with `duration_ms` and `time_to_first_token_ms` per turn. Per turn, so
  multi-step turns are only approximate unless `token_count` records are paired by timestamp.
  Codex OTel config not checked.
- **opencode [live].** `~/.local/share/opencode/opencode.db`, table `message`, JSON `data` for
  assistant messages: `tokens` {input, output, reasoning, cache.read, cache.write, total},
  `time.created`, `time.completed` (ms), `modelID`, `providerID`. Duration includes prefill and
  TTFT, so tok/s is approximate; no TTFT field.
- **pi [live].** `~/.pi/agent/sessions/<cwd>/<ts>_<id>.jsonl`, assistant `message` records with
  `timestamp`, `usage` {input, output, cacheRead, cacheWrite, reasoning, totalTokens, cost},
  `provider`, `model`, `stopReason`. Tokens only; no duration or TTFT. Latency could only be
  approximated from timestamp gaps, contaminated by tool-result time.
- **Claude Code [partly live].** `~/.claude/projects/<cwd>/<sessionId>.jsonl`: assistant records
  have `timestamp` and `message.usage`; a multi-tool turn can split into several records sharing
  one `message.id` (dedupe before summing; how usage repeats across them was not confirmed).
  `system` records with `subtype: turn_duration` give `durationMs` and `messageCount` per user
  turn [live, 1,735 in history] — coarse. Per-request latency needs OpenTelemetry:
  `CLAUDE_CODE_ENABLE_TELEMETRY=1` plus an OTLP/console exporter emits `api_request` events with
  `duration_ms`, token counts, model [docs, unverified; no OTel config in `~/.claude/settings.json`].
  Statusline stdin JSON has session-level `total_duration_ms`/`total_api_duration_ms` [docs,
  unverified].
- **agy:** not investigated.

None of these need LiteLLM: they are written by the client and work with local or direct models.

## 5. pi in detail (LiteLLM session tagging)

How wt launches pi: `wt/internal/agents/pi.go`. Protocol `openai_chat`, `--model`
`litellm/<registry-id>` in litellm mode or `<provider>/<model>` in direct mode. wt syncs models
into `~/.pi/agent/models.json` (`SyncModels`); only providers marked `_wtOwned` (mtplx, omlx,
openrouter) are wt-managed, the `litellm` and `ollama` providers are not. The `litellm`
provider is `openai-completions` at `http://localhost:4000/v1`.

pi 0.86.1 docs (`docs/models.md` in the pi package):
- Providers accept a `headers` map; values support `$ENV_VAR`/`${ENV_VAR}` interpolation and
  `!command`, resolved **at request time** for `models.json`.
- `sendSessionAffinityHeaders` (default false for `openai-completions`) sends the pi session id
  as `session_id`/`x-client-request-id`/`x-session-affinity`; `sessionAffinityFormat` picks the
  header shape. Whether LiteLLM reads any of these into `session_id` is **unverified** (probably
  not).

Proposed fix (idea only, not built or tested): wt exports a per-launch id (for example
`WT_SESSION_ID`); the litellm provider in `models.json` gets
`"headers": {"x-litellm-session-id": "$WT_SESSION_ID"}` (optionally also `x-litellm-tags`).

Open risks:
- Docs say a missing env var makes the value "unresolved". Behavior when pi runs outside wt
  (header dropped vs request fails) is untested.
- Requires confirming LiteLLM here honors `x-litellm-session-id`.
- The `litellm` provider is not `_wtOwned`, so editing it needs a new ownership rule.
- Direct-mode pi (ollama/omlx/mtplx providers) bypasses LiteLLM entirely.

## 6. Coverage matrix (agent × route)

| Agent | Via LiteLLM | Direct to provider |
|---|---|---|
| Claude Code | Exact TTFT/tok/s, sessions separable by its own session id | `turn_duration` (coarse) or OTel `api_request` (unverified) |
| Codex | Timing exact but no session id (needs a header/tag or time-window join) | Rollout JSONL: TTFT + duration per turn |
| Copilot | Same as codex via LiteLLM; sessions via its own store | `assistant_usage_events`: TTFT + duration per request |
| opencode | No session id today | Message duration + tokens (approximate tok/s) |
| pi | Needs injected header for sessions | Tokens only; speed roughly guessable from timestamps |
| agy | Unknown | Unknown |

## 7. Candidate approach (not approved)

Tiered, keyed on agent × route:
1. LiteLLM spend logs where traffic is routed (extend modelman's query with `endTime`,
   `completionStartTime`, `request_duration_ms`; decide how wt reads it).
2. Agent-native logs for direct traffic (Copilot and Codex nearly free; opencode and Claude Code
   coarse; pi weak).
3. Provider-specific readers only where they add real accuracy (MTPLX log; Ollama native fields
   only if wt ever times requests itself).
4. Keep the survey speed question as a fallback where no measured value exists, and show a
   measured value beside it rather than removing it.
5. For agents without a session id in LiteLLM, have wt inject a per-launch header or tag.

## 8. Open questions / next steps

- Design a unified per-session speed record and where it lives (likely wt-owned, next to
  `survey.jsonl`); whether it replaces or accompanies the survey rating.
- Test header injection per agent CLI (pi documented; codex, copilot, opencode unchecked).
- Verify LiteLLM honors `x-litellm-session-id` on this proxy.
- Verify Claude Code OTel `api_request` events and the statusline JSON fields locally.
- Live-check oMLX, mlx_lm.server and OpenRouter stats; investigate agy.
- Decide how wt gets Postgres data (modelman CLI vs `/spend/logs` vs driver).
- Decide aggregation rules (min completion tokens, streaming-only, median per model, filtering
  zero-duration rows).
