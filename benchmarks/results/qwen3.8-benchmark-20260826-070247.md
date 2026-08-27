# qwen3.8 Benchmark Results (ISOLATED runs)

**Date**: Wed Aug 26 07:02:47 EDT 2026
**max_tokens**: 200
**Mode**: Local backends run in isolation — before each test, all local models are unloaded (Ollama) or fully stopped (oMLX, llama.cpp). Only the one being tested has its model loaded. OpenRouter is cloud and unaffected.

## Prompt

```
Explain in detail the differences between REST and GraphQL APIs, including trade-offs in caching, partial responses, and tooling. Be thorough.
```

## Isolated local runs (bypassing LiteLLM)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| ollama (direct) | 444 | 251686 | 4345 | 17.29 |
| omlx (direct) | 39 | 36701 | 200 | 5.46 |
| llama.cpp (direct) | 2121 | 52963 | 200 | 3.93 |

## Isolated local runs (via LiteLLM)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| ollama (litellm) | 788 | 15876 | 200 | 13.26 |
| omlx (litellm) | 1379 | 20117 | 200 | 10.67 |
| llama.cpp (litellm) | 835 | 28354 | 200 | 7.27 |

## OpenRouter (cloud, no local service management)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| openrouter (litellm) | 526 | 3781 | 200 | 61.44 |


## Notes

- **Local backends are run in isolation**: before each test, all local models are unloaded from RAM.
  - **Ollama**: `ollama stop qwen3.8:27b-mlx` — daemon keeps :11434 bound but no model in GPU/RAM
  - **oMLX**: `omlx stop` — service halts entirely
  - **llama.cpp**: `launchctl unload` — LaunchAgent halts, frees the ~16 GB Metal allocation
- **OpenRouter** is cloud-only; no local service management needed.
- All backends use the **same prompt** with `temperature=0.0`.
- `throughput` excludes TTFT (prefill) — it's generation-only.
- Run multiple times and average to reduce variance from cold caches.

Results saved to: /tmp/qwen3.8-benchmark-20260826-070247.md
