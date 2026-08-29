# CLAUDE.md

## Commands
- `./benchmarks/qwen3.8-benchmark [max_tokens]` — single-pass benchmark (4 qwen3.8 backends)
- `./benchmarks/qwen3.8-benchmark-multi N [max_tokens] [cooldown]` — multi-pass for stable medians
- `./benchmarks/ornith-1.5-benchmark [max_tokens]` — single-pass (4 Ornith-1.5-35B variants)
- `./benchmarks/ornith-1.5-benchmark-multi N` — multi-pass
- `bin/llm-isolate-provider <ollama|llamacpp|omlx|omlx-6bit>` — stop others, start+warmup one (for `modelman benchmark`)
- `bin/llm-restore-providers` — bring all providers back up after a benchmark

## Architecture
- `benchmarks/` — bash benchmark scripts, docs, and `results/` (per-run markdown)
- `bin/` — standalone isolation helpers for the external `modelman benchmark` tool (must be on PATH)
- `docs/` — setup guides, reference docs, superpowers plans/specs
- LiteLLM config: `~/.config/litellm/config.yaml`

## Key Gotchas
- **Isolation is mandatory**: local MLX/GGUF models share Apple Silicon GPU/RAM and distort each other's benchmarks. Only one local model loaded at a time.
- **Stop mechanisms per backend**: Ollama `ollama stop <model>` (daemon stays up), oMLX `omlx stop` (halts service), llama.cpp `launchctl unload` (halts LaunchAgent).
- **oMLX serves both 4-bit and 6-bit variants** — warmup must name the exact variant (`omlx` vs `omlx-6bit`).
- **Shebang split**: benchmark scripts use `#!/opt/homebrew/bin/bash` (Homebrew bash); `bin/` helpers use `#!/bin/bash`.
- **Results go to `/tmp/<benchmark>-<timestamp>.md`**; archive into `benchmarks/results/`.

## Adding a New Benchmark Backend
The isolation logic exists in **two places** (legacy + CLI helper). Both must
be updated, otherwise `modelman benchmark` and the bash script will drift.
1. Add to `DIRECT_URLS`, `DIRECT_MODELS`, `LITELLM_MODELS` associative arrays in the benchmark script
2. Add the model to `~/.config/litellm/config.yaml`
3. Add stop/start logic to `stop_all_local` / `start_one_local` in the benchmark script
4. Add a new branch in `bin/llm-isolate-provider`'s case statement (and matching `*_MODEL` env var) so `modelman benchmark` can isolate it
5. Smoke test: `./benchmarks/qwen3.8-benchmark 30` (and `bin/llm-isolate-provider <new-backend>`)
6. Update the benchmark doc with new numbers
