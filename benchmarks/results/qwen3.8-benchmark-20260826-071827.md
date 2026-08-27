# qwen3.8 Benchmark Results (ISOLATED runs)

**Date**: Wed Aug 26 07:18:27 EDT 2026
**max_tokens**: 200
**Mode**: Local backends run in isolation — before each test, all local models are unloaded (Ollama) or fully stopped (oMLX, llama.cpp). Only the one being tested has its model loaded. OpenRouter is cloud and unaffected.

## Prompt

```
Explain in detail the differences between REST and GraphQL APIs, including trade-offs in caching, partial responses, and tooling. Be thorough.
```

## Isolated local runs (bypassing LiteLLM)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| ollama (direct) | 533 | 186172 | 3612 | 19.46 |
| omlx (direct) | 42 | 11875 | 200 | 16.90 |
| llama.cpp (direct) | 616 | 20997 | 200 | 9.81 |

## Isolated local runs (via LiteLLM)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| ollama (litellm) | 497 | 8293 | 200 | 25.65 |
| omlx (litellm) | 71 | 14093 | 200 | 14.26 |
| llama.cpp (litellm) | 623 | 19194 | 200 | 10.77 |

## OpenRouter (cloud, no local service management)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| openrouter (litellm) | 10711 | 15333 | 200 | 43.27 |


## Notes

- **Local backends are run in isolation**: before each test, all local models are unloaded from RAM.
  - **Ollama**: `ollama stop qwen3.8:27b-mlx` — daemon keeps :11434 bound but no model in GPU/RAM
  - **oMLX**: `omlx stop` — service halts entirely
  - **llama.cpp**: `launchctl unload` — LaunchAgent halts, frees the ~16 GB Metal allocation
- **OpenRouter** is cloud-only; no local service management needed.
- All backends use the **same prompt** with `temperature=0.0`.
- `throughput` excludes TTFT (prefill) — it's generation-only.
- Run multiple times and average to reduce variance from cold caches.

Results saved to: /tmp/qwen3.8-benchmark-20260826-071827.md
