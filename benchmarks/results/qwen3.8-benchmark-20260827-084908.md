# qwen3.8 Benchmark Results (ISOLATED runs)

**Date**: Thu Aug 27 08:49:08 EDT 2026
**max_tokens**: 200
**Mode**: Local backends run in isolation — before each test, all local models are unloaded (Ollama) or fully stopped (oMLX, llama.cpp). Only the one being tested has its model loaded. OpenRouter is cloud and unaffected.

## Prompt

```
Explain in detail the differences between REST and GraphQL APIs, including trade-offs in caching, partial responses, and tooling. Be thorough.
```

## Isolated local runs (bypassing LiteLLM)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| ollama (direct) | 629 | 219134 | 4485 | 20.53 |
| omlx (direct) | 42 | 14138 | 200 | 14.19 |
| llama.cpp (direct) | 721 | 24191 | 200 | 8.52 |

## Isolated local runs (via LiteLLM)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| ollama (litellm) | 581 | 10384 | 200 | 20.40 |
| omlx (litellm) | 33 | 14336 | 200 | 13.98 |
| llama.cpp (litellm) | 683 | 21881 | 200 | 9.43 |

## OpenRouter (cloud, direct + LiteLLM)

| Backend | TTFT (ms) | Total (ms) | Tokens | Throughput (tok/s) |
|---------|----------:|-----------:|-------:|-------------------:|
| openrouter qwen3.8-flash (direct) | N/A | N/A | N/A | N/A |
| openrouter qwen3.8-flash (litellm) | 14771 | 17141 | 200 | 84.39 |
| openrouter qwen3.8-27b (direct) | 696 | 4410 | 200 | 53.85 |
| openrouter qwen3.8-27b (litellm) | 1136 | 3227 | 200 | 95.65 |
| openrouter qwen3.8-2.4t-a95b (direct) | 986 | 2711 | 200 | 115.94 |
| openrouter qwen3.8-2.4t-a95b (litellm) | 1243 | 3206 | 200 | 101.88 |
| openrouter qwen3.8-max (direct) | 1376 | 7061 | 202 | 35.53 |
| openrouter qwen3.8-max (litellm) | 1411 | 7559 | 202 | 32.86 |


## Notes

- **Local backends are run in isolation**: before each test, all local models are unloaded from RAM.
  - **Ollama**: `ollama stop qwen3.8:27b-mlx` — daemon keeps :11434 bound but no model in GPU/RAM
  - **oMLX**: `omlx stop` — service halts entirely
  - **llama.cpp**: `launchctl unload` — LaunchAgent halts, frees the ~16 GB Metal allocation
- **OpenRouter** is cloud-only; no local service management needed. Each of the four models is tested both directly against the OpenRouter API and via LiteLLM.
- All backends use the **same prompt** with `temperature=0.0`.
- `throughput` excludes TTFT (prefill) — it's generation-only.
- Run multiple times and average to reduce variance from cold caches.

Results saved to: /tmp/qwen3.8-benchmark-20260827-084908.md
