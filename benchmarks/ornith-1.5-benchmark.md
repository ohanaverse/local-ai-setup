# Ornith-1.5 Benchmark — Comparing the Four 35B Variants

**Status**: Reference doc for benchmarking the four Ornith-1.5-35B-A3B variants on this machine. Tracks setup, the benchmark script, results, and the gotchas encountered.

---

## Why This Exists

The Ornith-1.5-35B-A3B model (a 35B MoE with 3B active parameters) is downloaded in four variants across three local runtimes:

| Backend | Model | Quantization | Size |
|---|---|---|---|
| **Ollama** | `ornith-1.5:35b` | Q4_K_M | 22 GB |
| **oMLX** | `Ornith-1.5-35B-A3B-MLX-4bit` | MLX 4-bit | ~19.5 GB |
| **oMLX** | `Ornith-1.5-35B-A3B-MLX-6bit` | MLX 6-bit | ~28 GB |
| **llama.cpp** | `Ornith-1.5-35B-Q6_K.gguf` | Q6_K GGUF | 29.2 GB |

These are *different quantizations and runtimes* of the same model. Benchmarking them side-by-side tells us which backend/quantization is fastest on this Apple Silicon hardware, and how much overhead the LiteLLM proxy adds.

---

## The Isolation Problem

The three local backends share Apple Silicon's GPU and unified memory. Running all of them with models loaded simultaneously means ~80 GB of contention for four ~20-29 GB models, plus thermal throttling. **The benchmark runs each variant in isolation**: before each test, all local models are unloaded (Ollama) or fully stopped (oMLX, llama.cpp).

| Backend | "Stop" mechanism | What's left running |
|---|---|---|
| **Ollama** | `ollama stop ornith-1.5:35b` | Daemon on `:11434`, model unloaded |
| **oMLX** | `omlx stop` | Nothing — service halts entirely |
| **llama.cpp** | `launchctl unload ~/Library/LaunchAgents/local.llamacpp.server.plist` | Nothing — LaunchAgent halts, frees the ~29 GB Metal allocation |

**oMLX two-model nuance**: oMLX is a multi-model server that serves *both* the 4-bit and 6-bit variants. The warmup request must name the exact variant so the correct model loads into GPU/RAM (oMLX uses LRU memory management). The 4-bit and 6-bit are benchmarked as two separate "backends", each with its own stop/start/warmup cycle.

---

## The Script

**Location**: `~/.local/bin/ornith-1.5-benchmark` (also in `benchmarks/`)

**Usage**:
```bash
ornith-1.5-benchmark               # default: 200 max_tokens
ornith-1.5-benchmark 100           # custom max_tokens
ornith-1.5-benchmark-multi 3       # 3 passes with cool-down
```

**What it does**:
1. Ensures all services are up
2. For each of the four variants (ollama, omlx 4-bit, omlx 6-bit, llama.cpp):
   - Stops all local services
   - Loads only the one being tested (warmup with a short "hi" request)
   - Runs the same prompt twice: once direct, once through LiteLLM
   - Measures TTFT, total time, tokens, throughput
3. Restores all local services

**Output**: Markdown file in `/tmp/ornith-1.5-benchmark-<timestamp>.md`

**The prompt** (same as qwen3.8, for cross-comparability):
```
Explain in detail the differences between REST and GraphQL APIs,
including trade-offs in caching, partial responses, and tooling.
Be thorough.
```

Plus `temperature=0.0`.

---

## Latest Results

Captured 2026-08-26 with **3 passes at `max_tokens=200`** (20s cool-down between passes, full isolation between variants). Each cell is the **median of 3 runs**.

### Throughput (tokens/sec) — median of 3 passes

| Backend | Direct | Via LiteLLM | LiteLLM overhead |
|---|---:|---:|---:|
| **ollama** (Q4_K_M) | 44.08* | 70.15 | +26.07* |
| **omlx 4-bit** | 77.52 | 74.52 | -3.00 |
| **omlx 6-bit** | 61.80 | 59.51 | -2.29 |
| **llama.cpp** (Q6_K) | 69.61 | 69.61 | 0.00 |

\* Ollama direct is confounded by reasoning tokens (see Caveats) — not directly comparable.

### TTFT (ms) — median of 3 passes

| Backend | Direct | Via LiteLLM |
|---|---:|---:|
| **ollama** | 239 | 237 |
| **omlx 4-bit** | 37 | 36 |
| **omlx 6-bit** | 36 | 36 |
| **llama.cpp** | 217 | 231 |

### Total time (ms) — median of 3 passes

| Backend | Direct | Via LiteLLM |
|---|---:|---:|
| **ollama** | 50,936* | 3,088 |
| **omlx 4-bit** | 2,617 | 2,719 |
| **omlx 6-bit** | 3,272 | 3,397 |
| **llama.cpp** | 3,090 | 3,119 |

\* Ollama direct includes ~50s of reasoning-token generation (see Caveats).

### Raw per-pass throughput (tok/s)

| Backend | Pass 1 | Pass 2 | Pass 3 | Median |
|---|---:|---:|---:|---:|
| ollama (direct) | 33.76 | 45.27 | 44.08 | **44.08** |
| ollama (litellm) | 52.10 | 70.15 | 75.22 | **70.15** |
| omlx 4-bit (direct) | 60.17 | 86.10 | 77.52 | **77.52** |
| omlx 4-bit (litellm) | 67.61 | 74.52 | 88.69 | **74.52** |
| omlx 6-bit (direct) | 56.51 | 61.80 | 61.86 | **61.80** |
| omlx 6-bit (litellm) | 57.67 | 59.51 | 69.98 | **59.51** |
| llama.cpp (direct) | 51.03 | 69.61 | 71.48 | **69.61** |
| llama.cpp (litellm) | 59.47 | 69.61 | 70.60 | **69.61** |

### Observations

- **oMLX 4-bit is the fastest variant** — median 77.52 tok/s direct, 74.52 via LiteLLM. The lighter 4-bit quantization wins on throughput, as expected.
- **oMLX 6-bit is slower than 4-bit** — 61.80 tok/s direct. The extra precision costs ~20% throughput with no obvious quality benefit for this benchmark prompt.
- **llama.cpp Q6_K is solid** — 69.61 tok/s direct, and the *only* backend with zero LiteLLM overhead (69.61 both ways). The Q6_K GGUF is heavier than the MLX 4-bit but still competitive.
- **oMLX has the lowest TTFT** — 36-37ms vs 217-239ms for llama.cpp and Ollama. This makes oMLX the best choice for low-latency interactive use.
- **LiteLLM overhead is small** — 2-3 tok/s penalty on the oMLX variants, zero on llama.cpp. Consistent with the qwen3.8 findings.

### Caveats

- **Ollama direct is confounded by reasoning tokens.** The Ornith model uses thinking/reasoning mode by default in Ollama. The direct endpoint reports `completion_tokens` including reasoning tokens (2,136-2,296 tokens) and takes ~50s, while the LiteLLM-routed request reports only the 200 visible output tokens and takes ~3s. The direct Ollama throughput (44.08 tok/s) is therefore *not* comparable to the other backends — it's measuring reasoning+output, not just output. This is the same behavior documented in the qwen3.8 benchmark.
- **Quantization mismatch**: the four variants use different quantizations (Q4_K_M, MLX 4-bit, MLX 6-bit, Q6_K), so this is a runtime+quantization comparison, not a pure runtime comparison.
- **Three passes is the minimum** for stable medians; the omlx 4-bit direct spread (60-86 tok/s) is still wide. Run 5+ passes for tighter bounds.
- **llama.cpp plist repoint**: the llama.cpp LaunchAgent now points at the Ornith Q6_K GGUF. The qwen3.8 benchmark's llama.cpp numbers can no longer be reproduced without repointing back (backup at `~/Library/LaunchAgents/local.llamacpp.server.plist.qwen3.8.bak`).

---

## Re-running the Benchmark

```bash
# Make sure all services are up
llm-restart

# Single 200-token pass
ornith-1.5-benchmark 200

# Multiple passes for stability
ornith-1.5-benchmark-multi 3 200 20
```

Results are timestamped in `/tmp/ornith-1.5-benchmark-<timestamp>.md`. The latest run:

```bash
ls -t /tmp/ornith-1.5-benchmark-*.md | head -1
```

---

## Related

- **qwen3.8 benchmark**: [`qwen3.8-benchmark.md`](./qwen3.8-benchmark.md) — the original benchmark this mirrors.
- **Main setup doc**: [`../docs/Local AI Setup 2026-08-25.md`](../docs/Local%20AI%20Setup%202026-08-25.md)
- **Service management**: `~/.local/bin/llm-restart`
- **LiteLLM config**: `~/.config/litellm/config.yaml` — includes the four Ornith entries.
