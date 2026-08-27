# Ornith-1.5 Benchmark Results (ISOLATED runs)

**Date**: Wed Aug 26 22:44:26 EDT 2026
**max_tokens**: 200
**Mode**: Local backends run in isolation — before each test, all local models are unloaded (Ollama) or fully stopped (oMLX, llama.cpp). Only the one being tested has its model loaded.

## Prompt

```
Explain in detail the differences between REST and GraphQL APIs, including trade-offs in caching, partial responses, and tooling. Be thorough.
```

## Isolated local runs (bypassing LiteLLM)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| ollama (direct) | 219 | 50936 | 2296 | 45.27 |
| omlx 4-bit (direct) | 33 | 2356 | 200 | 86.10 |
| omlx 6-bit (direct) | 36 | 3272 | 200 | 61.80 |
| llama.cpp (direct) | 217 | 3090 | 200 | 69.61 |

## Isolated local runs (via LiteLLM)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| ollama (litellm) | 237 | 3088 | 200 | 70.15 |
| omlx 4-bit (litellm) | 35 | 2719 | 200 | 74.52 |
| omlx 6-bit (litellm) | 36 | 3397 | 200 | 59.51 |
| llama.cpp (litellm) | 246 | 3119 | 200 | 69.61 |

## Notes

- **Local backends are run in isolation**: before each test, all local models are unloaded from RAM.
  - **Ollama**: `ollama stop ornith-1.5:35b` — daemon keeps :11434 bound but no model in GPU/RAM
  - **oMLX**: `omlx stop` — service halts entirely (serves 4-bit and 6-bit variants)
  - **llama.cpp**: `launchctl unload` — LaunchAgent halts, frees the ~29 GB Metal allocation
- All backends use the **same prompt** with `temperature=0.0`.
- `throughput` excludes TTFT (prefill) — it's generation-only.
- Run multiple times and average to reduce variance from cold caches.

Results saved to: /tmp/ornith-1.5-benchmark-20260826-224426.md
