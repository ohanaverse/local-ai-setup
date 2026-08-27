# qwen3.8 Benchmark Results (ISOLATED runs)

**Date**: Wed Aug 26 07:11:36 EDT 2026
**max_tokens**: 200
**Mode**: Local backends run in isolation — before each test, all local models are unloaded (Ollama) or fully stopped (oMLX, llama.cpp). Only the one being tested has its model loaded. OpenRouter is cloud and unaffected.

## Prompt

```
Explain in detail the differences between REST and GraphQL APIs, including trade-offs in caching, partial responses, and tooling. Be thorough.
```

## Isolated local runs (bypassing LiteLLM)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| ollama (direct) | 583 | 193322 | 3580 | 18.57 |
| omlx (direct) | 43 | 14008 | 200 | 14.32 |
| llama.cpp (direct) | 823 | 29497 | 200 | 6.97 |

## Isolated local runs (via LiteLLM)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| ollama (litellm) | 664 | 12570 | 200 | 16.80 |
| omlx (litellm) | 43 | 16743 | 200 | 11.98 |
| llama.cpp (litellm) | 808 | 29346 | 200 | 7.01 |

## OpenRouter (cloud, no local service management)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| openrouter (litellm) | 878 | 2938 | 200 | 97.09 |


## Notes

- **Local backends are run in isolation**: before each test, all local models are unloaded from RAM.
  - **Ollama**: `ollama stop qwen3.8:27b-mlx` — daemon keeps :11434 bound but no model in GPU/RAM
  - **oMLX**: `omlx stop` — service halts entirely
  - **llama.cpp**: `launchctl unload` — LaunchAgent halts, frees the ~16 GB Metal allocation
- **OpenRouter** is cloud-only; no local service management needed.
- All backends use the **same prompt** with `temperature=0.0`.
- `throughput` excludes TTFT (prefill) — it's generation-only.
- Run multiple times and average to reduce variance from cold caches.

Results saved to: /tmp/qwen3.8-benchmark-20260826-071136.md
