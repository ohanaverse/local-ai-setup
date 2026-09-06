# Agent Benchmark Results — `smoke.toml` (live run)

**Date**: Fri Sep 5 16:03:01 EDT 2026
**Suite**: `benchmarks/suites/smoke.toml` — task `day31-drift`, 1 pass, 1 row
**Row**: `ollama/glm-5.3-flash:cloud`, `thinking=off`, `route=litellm`
**Judge**: `anthropic/claude-opus-5`, `route=openrouter`, 1 sample

**Mode**: Isolated, like the single-turn benchmarks — `bin/llm-isolate-provider
ollama` stopped the other local backends and warmed the target before the agent
ran; judging ran after `llm-restore-providers`. The run exited non-zero because
llama.cpp could not be restored (its LaunchAgent points at a GGUF that no longer
exists — issue #33); every artifact was already persisted when that happened.

**Reading this**: 5 of 6 hidden tests passed and the judge scored it 87/100
(`principled_fix`), but the regression test the agent added was vacuous, so gate
9 capped the composite at ×0.5 → 44. The same suite, same model, produced
`NO_DIFF` (composite 0) on a neighboring pass and `VACUOUS_TEST` 5/6 on another:
one pass of one row is a plumbing check, not a model measurement.

## Quality

| label | model | thinking | route | outcome | hidden | rubric | cap | composite | verdict |
|---|---|---|---|---|---|---|---|---|---|
| smoke | ollama/glm-5.3-flash:cloud | off | litellm | VACUOUS_TEST | 5/6 | 87 | 0.5 | 44 | principled_fix |

## Speed

| label | wall_s | gen_s | ttft_first_ms | ttft_median_ms | gen_tok_s | e2e_tok_s | in_tok | out_tok | cache_read | cache_write | reasoning_tok | tools | requests |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| smoke | 90.32 | 73.59 | 527.3 | 639.7 | 193.74 | 157.85 | 211758 | 14257 | 0 | 0 | 10434 | 20 | 21 |

## Two-axis

| label | composite | wall_s | pareto |
|---|---|---|---|
| smoke | 44 | 90.32 | * |

## Anomalies

| label | anomaly |
|---|---|
| smoke | cap applied: x0.5 |
| smoke | VACUOUS_TEST |
| smoke | thinking=off but 10434 reasoning tokens |
| smoke | REPEATED_FAILURE(bash) |
