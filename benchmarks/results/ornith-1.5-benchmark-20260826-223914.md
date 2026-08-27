# Ornith-1.5 Benchmark Results (ISOLATED runs)

**Date**: Wed Aug 26 22:39:14 EDT 2026
**max_tokens**: 200
**Mode**: Local backends run in isolation — before each test, all local models are unloaded (Ollama) or fully stopped (oMLX, llama.cpp). Only the one being tested has its model loaded.

## Prompt

```
Explain in detail the differences between REST and GraphQL APIs, including trade-offs in caching, partial responses, and tooling. Be thorough.
```

## Isolated local runs (bypassing LiteLLM)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| ollama (direct) | 276 | 68225 | 2294 | 33.76 |
| omlx 4-bit (direct) | 64 | 3388 | 200 | 60.17 |
| omlx 6-bit (direct) | 37 | 3576 | 200 | 56.51 |
| llama.cpp (direct) | 216 | 4135 | 200 | 51.03 |

## Isolated local runs (via LiteLLM)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| ollama (litellm) | 226 | 4065 | 200 | 52.10 |
| omlx 4-bit (litellm) | 36 | 2994 | 200 | 67.61 |
| omlx 6-bit (litellm) | 67 | 3535 | 200 | 57.67 |
| llama.cpp (litellm) | 231 | 3594 | 200 | 59.47 |

## Notes

- **Local backends are run in isolation**: before each test, all local models are unloaded from RAM.
  - **Ollama**: `ollama stop ornith-1.5:35b` — daemon keeps :11434 bound but no model in GPU/RAM
  - **oMLX**: `omlx stop` — service halts entirely (serves 4-bit and 6-bit variants)
  - **llama.cpp**: `launchctl unload` — LaunchAgent halts, frees the ~29 GB Metal allocation
- All backends use the **same prompt** with `temperature=0.0`.
- `throughput` excludes TTFT (prefill) — it's generation-only.
- Run multiple times and average to reduce variance from cold caches.

Results saved to: /tmp/ornith-1.5-benchmark-20260826-223914.md
