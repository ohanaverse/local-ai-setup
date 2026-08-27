# qwen3.8 Benchmark Results (ISOLATED runs)

**Date**: Thu Aug 27 08:41:05 EDT 2026
**max_tokens**: 200
**Mode**: Local backends run in isolation — before each test, all local models are unloaded (Ollama) or fully stopped (oMLX, llama.cpp). Only the one being tested has its model loaded. OpenRouter is cloud and unaffected.

## Prompt

```
Explain in detail the differences between REST and GraphQL APIs, including trade-offs in caching, partial responses, and tooling. Be thorough.
```

## Isolated local runs (bypassing LiteLLM)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| ollama (direct) | 495 | 179019 | 4701 | 26.33 |
| omlx (direct) | 36 | 12199 | 200 | 16.44 |
| llama.cpp (direct) | 654 | 23964 | 200 | 8.58 |

## Isolated local runs (via LiteLLM)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| ollama (litellm) | 616 | 13702 | 200 | 15.28 |
| omlx (litellm) | 78 | 25556 | 200 | 7.85 |
| llama.cpp (litellm) | 1093 | 41957 | 200 | 4.89 |

## OpenRouter (cloud, direct + LiteLLM)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| openrouter qwen3.8-flash (direct) | 918 | 3916 | 200 | 66.71 |
| openrouter qwen3.8-flash (litellm) | 13808 | 16482 | 200 | 74.79 |
| openrouter qwen3.8-27b (direct) | 611 | 4059 | 200 | 58.00 |
| openrouter qwen3.8-27b (litellm) | 10702 | 16278 | 200 | 35.87 |
| openrouter qwen3.8-2.4t-a95b (direct) | 5992 | 10006 | 200 | 49.83 |
| openrouter qwen3.8-2.4t-a95b (litellm) | 790 | 4231 | 200 | 58.12 |
| openrouter qwen3.8-max (direct) | 2163 | 6887 | 202 | 42.76 |
| openrouter qwen3.8-max (litellm) | 1432 | 6408 | 202 | 40.59 |


## Notes

- **Local backends are run in isolation**: before each test, all local models are unloaded from RAM.
  - **Ollama**: `ollama stop qwen3.8:27b-mlx` — daemon keeps :11434 bound but no model in GPU/RAM
  - **oMLX**: `omlx stop` — service halts entirely
  - **llama.cpp**: `launchctl unload` — LaunchAgent halts, frees the ~16 GB Metal allocation
- **OpenRouter** is cloud-only; no local service management needed. Each of the four models is tested both directly against the OpenRouter API and via LiteLLM.
- All backends use the **same prompt** with `temperature=0.0`.
- `throughput` excludes TTFT (prefill) — it's generation-only.
- Run multiple times and average to reduce variance from cold caches.

Results saved to: /tmp/qwen3.8-benchmark-20260827-084105.md
