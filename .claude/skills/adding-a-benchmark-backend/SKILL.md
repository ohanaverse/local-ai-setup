---
name: adding-a-benchmark-backend
description: Steps to add a new local-model backend to the benchmark scripts and modelman benchmark. Use when asked to add a new benchmark backend or wire a new provider into the isolation/benchmark tooling.
---

## Adding a New Benchmark Backend

Isolation logic lives in **one place**: `bin/llm-isolate-provider`. The bash
benchmark scripts call it (with `LLM_ISOLATE_*_MODEL` env overrides for their
model names), and `modelman benchmark` delegates to it
(`modelman/src/modelman/benchmark/isolation.py`). Adding a backend:
1. Add to `DIRECT_URLS`, `DIRECT_MODELS`, `LITELLM_MODELS`, `ISOLATE_ID`, `ISOLATE_ENV` associative arrays in the benchmark script
2. Add the model to `~/.config/litellm/config.yaml`
3. Add a new branch in `bin/llm-isolate-provider`'s case statement (and matching `LLM_ISOLATE_*_MODEL` env var) — both the bash scripts and `modelman benchmark` isolate through it
4. modelman side: add a provider entry to `~/.config/local-ai/registry.toml` (via `modelman sync` or the TUI) and confirm the provider id is in `LOCAL_PROVIDERS` in `modelman/src/modelman/benchmark/runner.py` — a backend missing from that set is silently skipped by `modelman benchmark`
5. If the new backend should also be isolatable for `modelman benchmark agent`, add it to `SUPPORTED_PROVIDER_IDS` in `modelman/src/modelman/benchmark/isolation.py` — a backend missing from that set runs unisolated with no error
6. Smoke test: `./benchmarks/qwen3.8-benchmark 30` (and `bin/llm-isolate-provider <new-backend>`)
7. Update the benchmark doc with new numbers
