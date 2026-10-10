# Benchmarks

Performance and accuracy benchmarks for the local AI setup.

## Agentic coding benchmarks

`llmbench agent` runs a real coding task through the `pi` agent
across a model/thinking/route matrix and grades the result on deterministic
gates plus a blind LLM rubric — see [docs/guides/09-agent-benchmarks.md](../docs/guides/09-agent-benchmarks.md).

- **`tasks/`** — task bundles (`day31-drift` ships first)
- **`suites/`** — suite TOML files (`smoke.toml`, `q4-agent-sweep.toml`)

## Capability eval benchmark

`llmbench eval` runs single-turn tasks across five categories (reasoning,
planning, coding, code_review, doc_summary) — see [docs/guides/11-capability-eval-benchmark.md](../docs/guides/11-capability-eval-benchmark.md).

- **`tasks/eval/`** — one directory of task bundles per category
- **`suites/eval-sweep.toml`** — the suite

## Contents

- **`qwen3.8-benchmark.md`** — Main benchmark doc covering the qwen3.8 variants (Ollama, oMLX, MTPLX, OpenRouter; llama.cpp retired 2026-09-07). Includes latest results, methodology, and bug-fix history.
- **`qwen3.8-benchmark`** — Single-pass benchmark script (isolated local runs).
- **`qwen3.8-benchmark-multi`** — Multi-pass wrapper for stable medians.
- **`ornith-1.5-benchmark.md`** — Benchmark doc for the three Ornith-1.5-35B variants (Ollama Q4_K_M, oMLX 4-bit, oMLX 6-bit; llama.cpp Q6_K retired 2026-09-07).
- **`ornith-1.5-benchmark`** — Single-pass benchmark script for Ornith-1.5.
- **`ornith-1.5-benchmark-multi`** — Multi-pass wrapper for Ornith-1.5.
- **`results/`** — Per-pass markdown output from each benchmark run.

## Quick start

The scripts are not installed on PATH; run them by path. The commands below are run from this directory, and each script finds the helper it sources in `lib/` relative to its own location:

```bash
./qwen3.8-benchmark               # single pass, 200 max_tokens
./qwen3.8-benchmark-multi 3       # 3 passes with cool-down
./qwen3.8-benchmark-multi 5 200 30    # 5 passes, 200 max_tokens, 30s cool-down
./ornith-1.5-benchmark            # single pass, 200 max_tokens
./ornith-1.5-benchmark-multi 3    # 3 passes with cool-down
```

Results are written to `/tmp/<script>-<timestamp>.md` and can be moved/archived into `results/` for reference.

## Latest run

See **[qwen3.8-benchmark.md](./qwen3.8-benchmark.md)** and **[ornith-1.5-benchmark.md](./ornith-1.5-benchmark.md)** for the most recent numbers and methodology.

- Why isolation matters (Apple Silicon GPU/RAM contention between local models)
- Service-stop mechanism per backend (`ollama stop <model>`, `omlx stop`, `mtplx stop --port 8003 --grace-seconds 10`)
- Per-backend setup quirks (bash version traps, array subscript gotchas, model-load detection)
- Median throughput across 3 passes
- TTFT and total-time tables

## Adding new benchmarks

For new model variants or providers:

1. Edit `qwen3.8-benchmark` to add the backend to:
   - `DIRECT_URLS`, `DIRECT_MODELS`, `LITELLM_MODELS` associative arrays
   - `ISOLATE_ID` (the `llmbench provider` id that isolates it)
   - Both `for backend in` loops (direct rows and LiteLLM rows)

   The script has no start/stop/warmup code of its own: `isolate_one` in `lib/benchmark-common.sh` calls `llmbench provider isolate`. A new provider also needs a `Backend` registered in `llmbench/src/llmbench/providers/lifecycle/backends/` (see the `adding-a-benchmark-backend` skill).
2. Give the model a LiteLLM route: register it (`wt model add`) and run `wt litellm sync` while it is running, or start it through wt. Don't hand-add the row to `~/.config/litellm/config.yaml` — wt builds the managed rows from `registry.toml` plus live probes ([docs/guides/00-config-map.md](../docs/guides/00-config-map.md))
3. Run a smoke test with `./qwen3.8-benchmark 30` (small max_tokens for speed)
4. Update the main benchmark doc with the new numbers

## Related

- **[docs/Local AI Setup 2026-08-25.md](../docs/archive/Local%20AI%20Setup%202026-08-25.md)** — Main setup doc covering LiteLLM, Ollama, oMLX, llama.cpp, OpenRouter configuration, auto-start, and restart scripts.
- **Service management**: `uv run --directory llmbench llmbench provider restore` (from the repo root) — brings ollama, oMLX and LiteLLM back up and stops mtplx and mlx_lm_server. The benchmark scripts run it themselves at start.
- **LiteLLM config**: `~/.config/litellm/config.yaml` — the routes wt manages; read them with `wt litellm list`.
