# Benchmarks

Performance and accuracy benchmarks for the local AI setup.

## Agentic coding benchmarks

`modelman benchmark agent` runs a real coding task through the `pi` agent
across a model/thinking/route matrix and grades the result on deterministic
gates plus a blind LLM rubric — see [docs/guides/09-agent-benchmarks.md](../docs/guides/09-agent-benchmarks.md).

- **`tasks/`** — task bundles (`day31-drift` ships first)
- **`suites/`** — suite TOML files (`smoke.toml`, `q4-agent-sweep.toml`)

## Contents

- **`qwen3.8-benchmark.md`** — Main benchmark doc covering the four qwen3.8 variants (Ollama, oMLX, llama.cpp, OpenRouter). Includes latest results, methodology, and bug-fix history.
- **`qwen3.8-benchmark`** — Single-pass benchmark script (isolated local runs).
- **`qwen3.8-benchmark-multi`** — Multi-pass wrapper for stable medians.
- **`ornith-1.5-benchmark.md`** — Benchmark doc for the four Ornith-1.5-35B variants (Ollama Q4_K_M, oMLX 4-bit, oMLX 6-bit, llama.cpp Q6_K).
- **`ornith-1.5-benchmark`** — Single-pass benchmark script for Ornith-1.5.
- **`ornith-1.5-benchmark-multi`** — Multi-pass wrapper for Ornith-1.5.
- **`results/`** — Per-pass markdown output from each benchmark run.

## Quick start

The scripts are also installed in `~/.local/bin/` and on the user's PATH, so they can be run from anywhere:

```bash
qwen3.8-benchmark               # single pass, 200 max_tokens
qwen3.8-benchmark-multi 3       # 3 passes with cool-down
ornith-1.5-benchmark            # single pass, 200 max_tokens
ornith-1.5-benchmark-multi 3    # 3 passes with cool-down
```

Or run directly from this directory:

```bash
./qwen3.8-benchmark
./qwen3.8-benchmark-multi 5 200 30    # 5 passes, 200 max_tokens, 30s cool-down
```

Results are written to `/tmp/qwen3.8-benchmark-<timestamp>.md` and can be moved/archived into `results/` for reference.

## Latest run

See **[qwen3.8-benchmark.md](./qwen3.8-benchmark.md)** and **[ornith-1.5-benchmark.md](./ornith-1.5-benchmark.md)** for the most recent numbers and methodology.

- Why isolation matters (Apple Silicon GPU/RAM contention between local models)
- Service-stop mechanism per backend (`ollama stop`, `omlx stop`, `launchctl unload`)
- Per-backend setup quirks (bash version traps, array subscript gotchas, model-load detection)
- Median throughput across 3 passes
- TTFT and total-time tables

## Adding new benchmarks

For new model variants or providers:

1. Edit `qwen3.8-benchmark` to add the backend to:
   - `DIRECT_URLS`, `DIRECT_MODELS`, `LITELLM_MODELS` associative arrays
   - The main `for backend in` loop
   - `stop_all_local` (if it has a model to unload)
   - `start_one_local` (with service-start + warmup logic)
2. Add the model to `~/.config/litellm/config.yaml` first
3. Run a smoke test with `./qwen3.8-benchmark 30` (small max_tokens for speed)
4. Update the main benchmark doc with the new numbers

## Related

- **[docs/Local AI Setup 2026-08-25.md](../docs/archive/Local%20AI%20Setup%202026-08-25.md)** — Main setup doc covering LiteLLM, Ollama, oMLX, llama.cpp, OpenRouter configuration, auto-start, and restart scripts.
- **Service management**: `~/.local/bin/llm-restart` — restart all four providers in one command.
- **LiteLLM config**: `~/.config/litellm/config.yaml` — the four model entries.
