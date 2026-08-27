# qwen3.8 Benchmark Results (ISOLATED runs)

**Date**: Thu Aug 27 08:31:25 EDT 2026
**max_tokens**: 200
**Mode**: Local backends run in isolation — before each test, all local models are unloaded (Ollama) or fully stopped (oMLX, llama.cpp). Only the one being tested has its model loaded. OpenRouter is cloud and unaffected.

## Prompt

```
Explain in detail the differences between REST and GraphQL APIs, including trade-offs in caching, partial responses, and tooling. Be thorough.
```

## Isolated local runs (bypassing LiteLLM)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| ollama (direct) | 731 | 248382 | 3981 | 16.08 |
| omlx (direct) | 42 | 15369 | 200 | 13.05 |
| llama.cpp (direct) | 675 | 22704 | 200 | 9.08 |

## Isolated local runs (via LiteLLM)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| ollama (litellm) | 561 | 9272 | 200 | 22.96 |
| omlx (litellm) | 34 | 14881 | 200 | 13.47 |
| llama.cpp (litellm) | 639 | 20941 | 200 | 9.85 |

## OpenRouter (cloud, direct + LiteLLM)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| openrouter qwen3.8-flash (direct) | N/A | N/A | N/A | N/A |
| openrouter qwen3.8-flash (litellm) | 6321 | 9173 | 200 | 70.13 |
| openrouter qwen3.8-27b (direct) | 525 | 16136 | 200 | 12.81 |
| openrouter qwen3.8-27b (litellm) | 919 | 76445 | 2700 | 35.75 |
| openrouter qwen3.8-2.4t-a95b (direct) | 1172 | 3350 | 200 | 91.83 |
| openrouter qwen3.8-2.4t-a95b (litellm) | 3236 | 5782 | 200 | 78.55 |
| openrouter qwen3.8-max (direct) | 1019 | 6137 | 202 | 39.47 |
| openrouter qwen3.8-max (litellm) | 924 | 5960 | 202 | 40.11 |


## Notes

- **Local backends are run in isolation**: before each test, all local models are unloaded from RAM.
  - **Ollama**: `ollama stop qwen3.8:27b-mlx` — daemon keeps :11434 bound but no model in GPU/RAM
  - **oMLX**: `omlx stop` — service halts entirely
  - **llama.cpp**: `launchctl unload` — LaunchAgent halts, frees the ~16 GB Metal allocation
- **OpenRouter** is cloud-only; no local service management needed. Each of the four models is tested both directly against the OpenRouter API and via LiteLLM.
- All backends use the **same prompt** with `temperature=0.0`.
- `throughput` excludes TTFT (prefill) — it's generation-only.
- Run multiple times and average to reduce variance from cold caches.

Results saved to: /tmp/qwen3.8-benchmark-20260827-083125.md
