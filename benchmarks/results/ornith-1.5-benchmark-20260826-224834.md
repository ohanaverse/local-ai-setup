# Ornith-1.5 Benchmark Results (ISOLATED runs)

**Date**: Wed Aug 26 22:48:34 EDT 2026
**max_tokens**: 200
**Mode**: Local backends run in isolation — before each test, all local models are unloaded (Ollama) or fully stopped (oMLX, llama.cpp). Only the one being tested has its model loaded.

## Prompt

```
Explain in detail the differences between REST and GraphQL APIs, including trade-offs in caching, partial responses, and tooling. Be thorough.
```

## Isolated local runs (bypassing LiteLLM)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| ollama (direct) | 239 | 48694 | 2136 | 44.08 |
| omlx 4-bit (direct) | 37 | 2617 | 200 | 77.52 |
| omlx 6-bit (direct) | 36 | 3269 | 200 | 61.86 |
| llama.cpp (direct) | 219 | 3017 | 200 | 71.48 |

## Isolated local runs (via LiteLLM)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| ollama (litellm) | 248 | 2907 | 200 | 75.22 |
| omlx 4-bit (litellm) | 36 | 2291 | 200 | 88.69 |
| omlx 6-bit (litellm) | 34 | 2892 | 200 | 69.98 |
| llama.cpp (litellm) | 218 | 3051 | 200 | 70.60 |

## Notes

- **Local backends are run in isolation**: before each test, all local models are unloaded from RAM.
  - **Ollama**: `ollama stop ornith-1.5:35b` — daemon keeps :11434 bound but no model in GPU/RAM
  - **oMLX**: `omlx stop` — service halts entirely (serves 4-bit and 6-bit variants)
  - **llama.cpp**: `launchctl unload` — LaunchAgent halts, frees the ~29 GB Metal allocation
- All backends use the **same prompt** with `temperature=0.0`.
- `throughput` excludes TTFT (prefill) — it's generation-only.
- Run multiple times and average to reduce variance from cold caches.

Results saved to: /tmp/ornith-1.5-benchmark-20260826-224834.md
