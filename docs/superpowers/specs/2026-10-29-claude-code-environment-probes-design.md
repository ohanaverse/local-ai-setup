# Claude Code environment probes — design

Date: 2026-10-29. Status: draft for approval.

## Intent

The author runs Claude Code in two environments and believes the corporate one is slower:

- **Corporate:** Claude models on AWS Bedrock behind a LiteLLM gateway, with org-managed settings (some of which cannot be changed).
- **Home:** a Claude subscription direct to Anthropic, plus non-anthropic models through ollama, OpenRouter and a local LiteLLM proxy.

The question is *why*. Three suspects: the gateway/Bedrock route is slower, the corporate setup injects more context (plugins, MCP, skills, `CLAUDE.md` imports), or something else.

`transcript_speed.py` can already tell whether one environment is slower, from organic sessions. Organic sessions cannot answer the "why" on their own because real calls always carry a large, uneven context and different output lengths. This design adds a **controlled probe**: the same fixed prompts run through `claude -p` on both machines, read back with the same call timing as the organic data, so the two environments can be compared like for like and the cause narrowed down.

Probes go through `claude -p`, not raw HTTP, on purpose: the author wants Claude Code's own overhead (startup, hooks, plugin and MCP loading, prompt assembly) included in the measurement.

## What the organic data already shows (2026-10-29, aggregate only)

Recorded so the builder knows what the probe must be able to confirm or overturn. Figures are rounded; the compare of the author's two machines is the source.

- Same model, same output-size band: corporate Sonnet is about 20% slower in tokens/second, and the gap holds at equal context size. Corporate Opus is slower by output band but has few calls, and its context-size comparison is confounded by output mix.
- The extra time is roughly a constant ~0.85 s before the first block plus a 15–19% slower stream on long outputs.
- Prompt caching works through the gateway (cache hit about 97–99% on both sides), so cache loss is ruled out. No silent model fallbacks (requested model equals answering model).
- Per-call context is *smaller* at corporate than at home, but session-startup context is larger (about 71k vs 45k tokens for Sonnet at the median). So bloat exists at startup, and is cheap after the first call because of caching.
- A direct curl of the gateway with a tiny prompt showed: gateway software overhead about 26 ms, connection setup about 0.12 s, an unattributed ~0.27 s between the edge proxy and LiteLLM, Bedrock itself about 0.6 s to first byte, and streaming at roughly 260 tokens/s. A clean call is fast; whatever slows real use shows up with large prompts, thinking or tool turns.
- The gateway has a response cache: an identical request repeated answered in about 0.15 s with the full output. See "Requirements that come from this".

## Goals

1. Run the same fixed set of prompts on any machine with `claude -p`, and produce a small aggregate-only JSON file that can be copied between machines.
2. Compare two such files and print, per case: wall time, API time, Claude Code's own overhead, time to first block, total call time, tokens/second, context size and cache hit.
3. Make each of the three suspects testable:
   - **Route:** the same case, same variant, corporate vs home.
   - **Config bloat:** the same machine, default vs lean variant.
   - **Prompt-size scaling:** the same machine and variant, at several added prompt sizes.
4. Reuse the call-timing code in `transcript_speed.py` so probe numbers and organic numbers mean the same thing.

## Non-goals

- OpenTelemetry. Managed settings send corporate telemetry to the org's collector; this design never tries to redirect it.
- Changing, overriding or working around managed settings.
- Wire-level capture or a logging proxy at home.
- Statistical significance testing. Report medians and p90 with the run count; the reader judges.
- Benchmarking quality or correctness of answers.
- Non-anthropic models. The probe pins one Claude model so both machines run the same thing.

## Anti-goals (a failure even if it technically works)

- A probe that quietly measures the gateway's response cache instead of the model route.
- A "cold vs warm" split where nothing is ever warm because every run changed the cache prefix.
- Output files that carry session ids, paths, usernames, hostnames or prompt text.
- A probe expensive enough to need budget approval. All prompts are short in output; the large ones are cached input.
- Numbers presented as measurements that are really client-side estimates. The same labelling rule as the rest of `agent-utilities/` applies.

## Constraints

- Lives in `agent-utilities/claude-utilities/` beside `transcript_speed.py`. Stdlib only, read-only against Claude Code's own records, Python >= 3.11 (the directory pins 3.13 for `uv run`).
- **Builds on PR #499.** That change extends `transcript_speed.calls_from_transcript` / `derive` (backward compatible, with aggregate `schema: 2`) to carry `ctx_tokens`, `cache_hit`, `first_main` and `requested_model` per call. The probe imports those rows and does not parse transcripts a second time. Merge #499 first, or branch the build from it.
- **Probe runs are visible to the organisation.** On the corporate machine, managed settings send telemetry (including tool details) to the org's collector and the gateway tracks usage and spend per key. A default run (4 cases x 2 variants x 10 runs = 80 calls, mostly cached input) is small, a few dollars at most, but it will appear in those dashboards. Prompts are synthetic and contain nothing sensitive.
- Must be usable on the corporate machine by copying the two files (`transcript_speed.py`, `probe_runner.py`) and running with any Python >= 3.11. No install step.
- Corporate permission rules apply to `claude -p` as to any run. The default cases use no tools. Any case that needs a tool must allow exactly that tool and nothing else.
- Probes spend real model usage. Defaults must stay small (see Cases).
- Transcripts are private session data and are never copied or committed.

## Design

### The harness: `probe_runner.py`

`probe_runner.py run --label <name> [--runs N] [--model M] [--variants default,lean] [--out FILE]` executes the case list and writes the aggregate JSON. `probe_runner.py --compare A.json B.json` prints the side-by-side. The model defaults to `claude-sonnet-5-5` and is always passed with `--model`, so the corporate default (`opusplan`) cannot change what is measured.

Each run is one `claude -p --output-format json --model <M>` process, run sequentially (never in parallel). The JSON result supplies `session_id`, `duration_ms` and `duration_api_ms`; the harness also times the process itself (`wall_s`). The session's transcript is then found by `session_id` under `~/.claude/projects/` and read with `transcript_speed.calls_from_transcript`, so first-block time, total time, tokens/second, context tokens and cache hit are computed the way organic data is. Do not pass `--no-session-persistence`; the transcript is the measurement.

### Requirements that come from the evidence above

1. **Unique request per run, nonce at the end.** Every prompt ends with a short per-run tag (random, e.g. `(ref 48213907)`), placed after the fixed text. The full request is unique, so the gateway's response cache cannot answer it. The tag sits after the filler so the filler prefix stays identical and can still be served from Anthropic prompt caching.
2. **Same working directory for every run.** Use one fixed, empty probe directory (for example under `~/.cache/claude-utilities/`), not a fresh temp directory per run. The working directory is part of the prompt Claude Code assembles; changing it changes the cache prefix and the "warm" runs would never be warm.
3. **Round order, so cache stays live.** Interleave as: for each round, for each case, for each variant. Time between two runs of the same (case, variant) must stay under the prompt-cache lifetime (5 minutes is the safe assumption). The harness records the largest gap in `meta`, warns on stderr when it is exceeded, and carries on; the cache-cold exclusion in requirement 5 handles the consequence. Default and lean have different settings, so each (case, variant) has its own cold run, and each fill case's cold run is a one-time cache write. That cost is expected. (Designer's call.)
4. **Which call is the measurement, and which runs are discarded.** A `claude -p` session can make side calls to other models (for example a title or summary call). The measured call is the call answered by the pinned model. Non-tool cases are expected to make exactly one such call; a run with zero, or with more than one (a retry), is discarded and counted. Side calls to other models are ignored and counted separately. A run with `is_error`, or whose transcript does not appear, is also discarded and counted. (Designer's call.)
5. **"Warm" means served from cache.** A warm run (2..N) whose `cache_hit` is below 0.5 is excluded from the warm percentiles and counted as "cache-cold warm runs", which the output and the compare table show prominently. A prompt prefix that changes between runs (for example a hook that injects the time) would otherwise silently turn warm figures into cold ones. If many runs are excluded the reader sees it and can look at the per-case `cache_hit`. The probe directory is empty and not a git repository, so no git state enters the prompt. (Designer's call.)
6. **Prompts go on stdin.** An 80k-token filler (about 300 KB) is too close to command-line size limits to pass as an argument. (Designer's call.)

### Cases

Each case has a fixed prompt body, a fixed expected output size, and the nonce.

| Case | What it isolates |
|---|---|
| `tiny` | Fixed overhead: startup context, hooks and plugin load, minimum call time. Prompt: reply with one word. |
| `count` | Decode speed at a known output length (about 630 tokens): "Count from 1 to 300, one number per line. No other text." No thinking dependency. |
| `fill20k`, `fill80k` | How time to first block grows with prompt size. Fixed synthetic filler (deterministic, nothing sensitive), then a one-line answer. "20k" and "80k" are tokens *added* to the normal startup context; the output records the actual context tokens read, which is what to compare. |
| `tool` (optional, may be deferred) | Round-trip overhead of one tool call: read a fixed small file, then answer. Allow only that tool. |

Default `--runs` is 10 per case and variant. The first run of each (case, variant) is reported separately as **cold** (the filler prefix is written to cache); runs 2..N are **warm** and drive the percentiles.

### Variants

- **default:** the machine's normal configuration.
- **lean:** `--setting-sources project --strict-mcp-config --disable-slash-commands`, run from the empty probe directory, which drops user-level plugins, MCP servers and skills. The exact flag set is a starting point (see Open items).

`--bare` is deliberately not used: Claude Code's own help says it never reads OAuth or keychain credentials, so the home subscription login could not authenticate.

Managed settings still load in both variants and cannot be disabled. The harness reports each variant's session-startup context (context tokens of the first call of each run), so the reader sees how lean "lean" really got on that machine instead of assuming.

### Output file (aggregate only)

`schema: 1`, then: `meta` (Claude Code version, pinned model, runs, variants, date, label; no hostname or username), and one entry per (case, variant) with run counts (ok, discarded), the cold run's figures, and warm p50/p90 for: `wall_s`, `api_s` (`duration_api_ms`), `client_overhead_s` (`duration_ms` minus `duration_api_ms`, see Open items), `ttfb_s`, `total_s`, `tps_total`, `ctx_tokens`, `cache_hit`. No session ids, prompts or paths.

### Compare output

For each (case, variant): both sides' warm p50 and p90 with the percent difference of B against A, plus the cold figure. Below the table, derived lines (labelled as estimates):

- **Config cost** per environment: default minus lean for `tiny`.
- **Prompt-size slope** per environment and variant: how `ttfb_s` grows per 10k context tokens between `tiny` and `fill80k`.
- **Gateway/route residue:** the corporate vs home difference for the same case and variant.

### When the two files differ

`--compare` refuses (clear error) if the pinned models differ or either file's `schema` is unknown. It prints a warning, and still compares, when Claude Code versions or run counts differ. It compares the intersection of (case, variant) and lists the ones present on only one side, as `transcript_speed.py --compare` does. (Designer's call.)

### Probe sessions must not pollute organic stats

Probe runs write transcripts under `~/.claude/projects/` like any session, so `transcript_speed.py`'s organic summary and `--json` would otherwise count them. The probe directory has a fixed, recognisable name (`.../claude-utilities/probe-cwd`), which appears in the encoded project folder name. `transcript_speed.py` skips transcripts in project folders carrying that marker unless `--include-probes` is passed, and the probe harness reads its own transcripts by path. This changes the existing script's default input slightly, so it needs a test and a README line. (Designer's call.)

### How to read it (readings to test, not established rules)

| Pattern | Likely meaning |
|---|---|
| `tiny` slower at corporate, lean and default alike | Fixed per-call route cost. |
| `tiny` default much slower than lean on one machine | Configuration (hooks, plugins, MCP) costs time. |
| Corporate slope per 10k tokens steeper than home | Large-context handling on the Bedrock route is the problem. |
| `count` slower at corporate with equal context | Streaming rate of the route. |
| `client_overhead_s` large and similar on both | Claude Code itself, not the route. |

## Decisions made by the author

- Probes use `claude -p`, so Claude Code's own overhead is included.
- Report extension first, probes after (the extension is shipped separately).
- Aggregate-only files that can be copied between machines.
- Managed settings and corporate telemetry are never touched.

## Calls made by the designer (author to check)

- Pin `claude-sonnet-5-5`; 10 runs per case and variant; sequential only.
- Two variants, with the lean flag set above; no `--bare`.
- Cold = first run per (case, variant); warm = the rest.
- A separate script rather than extending `transcript_speed.py`, sharing its parsing by import.
- One fixed probe directory, nonce at the end of the prompt.
- `tool` case optional for v1.
- Prompts on stdin; measured call = the pinned-model call; cache-cold "warm" runs excluded and counted; warn-and-continue on cache-lifetime gaps; compare refuses on model or schema mismatch and warns on version or run-count mismatch.
- Probe sessions excluded from organic stats by a marker in the probe directory name (changes `transcript_speed.py`'s default input, with `--include-probes` to opt back in).

## Left to the builder

- CLI argument names and defaults beyond the above.
- Filler generator (any deterministic text is fine) and how it is sized to the token targets.
- How the transcript is located and how long to wait for it to appear.
- Handling of timeouts and a hung `claude -p`.
- Test strategy. A fake `claude` executable on `PATH` that writes a canned JSON result and transcript is the likely approach; no test may call the real model or touch the real `~/.claude`.
- README section and the `agent-utilities/CLAUDE.md` lines.

## Open items to confirm before or during the build

These are facts the designer has not verified:

1. **First task of the build, gating the rest.** One real `claude -p --output-format json --model <M>` run of the `tiny` case (a few cents) to record the real JSON shape and check the field names (`duration_ms`, `duration_api_ms`, `session_id`) and what `duration_ms` minus `duration_api_ms` actually covers. Save the observed shape as a test fixture. The builder may make this one real call; the test suite itself still never calls the model. If the difference does not cover Claude Code overhead, drop `client_overhead_s` and its row in "How to read it", and say so in the README.
2. Whether the lean flags really shrink startup context on the corporate machine. Managed plugin settings or managed MCP servers may still load; the reported startup context shows the effect, and the flag set may need adjusting.
3. Whether the gateway accepts the pinned model name exactly as passed to `--model`, and whether Claude Code adds a context-window suffix or beta header to it. A curl of the bare name already worked.
4. Whether the corporate machine's `claude -p` calls trigger permission prompts for the chosen cases.
