# CLAUDE.md

## Docs
- User playbooks: `docs/guides/` — canonical task guides (config map, setup, models, families, LiteLLM, benchmarks, wt, usage, maintenance). Read `docs/guides/00-config-map.md` first for config-file ownership.
- `./issues.md` — follow-ups from the 2026-08-29 guide-set review; all 5 items are marked FIXED, kept as a historical record.
- Package-level context: `modelman/CLAUDE.md` (Python TUI/CLI) and `wt/CLAUDE.md` (Go worktree launcher) contain per-package commands, architecture, and gotchas.

## Commands
- `./benchmarks/qwen3.8-benchmark [max_tokens]` — single-pass benchmark (4 qwen3.8 backends)
- `./benchmarks/qwen3.8-benchmark-multi N [max_tokens] [cooldown]` — multi-pass for stable medians
- `./benchmarks/ornith-1.5-benchmark [max_tokens]` — single-pass (4 Ornith-1.5-35B variants)
- `./benchmarks/ornith-1.5-benchmark-multi N` — multi-pass
- `bin/llm-isolate-provider <ollama|llamacpp|omlx|omlx-6bit>` — stop others, start+warmup one (for `modelman benchmark`)
- `bin/llm-restore-providers` — bring all providers back up after a benchmark
- `make lint-shell` — validate `bash -n` and `shellcheck --severity=error` across `bin/` and `benchmarks/`
- `make lint` — umbrella target (`lint-shell` + `check-links`)
- `make test-all` — one-stop local verification mirroring CI: lint + modelman `make check`/`make test` + wt `go build`/`vet`/`test`
- `bin/check-links` (or `make check-links`) — validates repo-relative markdown links across README, CLAUDE.md, docs/guides, docs/reference, docs/archive, benchmarks

## Architecture
- `benchmarks/` — bash benchmark scripts, docs, and `results/` (per-run markdown)
- `bin/` — isolation helpers (`llm-isolate-provider`, `llm-restore-providers`) invoked by both `modelman benchmark` (via `modelman/src/modelman/benchmark/isolation.py`, by PATH) and the legacy benchmark scripts. These are monorepo-wide utilities; the modelman package calls them through `$PATH`, not by importing Python code.
- `modelman/` — model registry TUI/CLI (Python/uv; `src/modelman`, own `CLAUDE.md`, own `Makefile`). Canonical owner of `registry.toml`, `modelman.toml` (exposure state), LiteLLM config writes, and the `modelman benchmark` tool
- `wt/` — worktree agent launcher (Go module; `cmd/wt`, `internal/`, own `CLAUDE.md`, own `Makefile`). Reads modelman's `registry.toml` and `modelman.toml` (exposure) read-only; owns `~/.config/agent-wt/config.toml`, rotation + usage state
- `Makefile` — lint target for shell scripts (root + wt), `check-links` (all tracked markdown), `test-all` (aggregates modelman + wt)
- `docs/` — guides/ (user playbooks — see Docs above), reference/, contracts/ (cross-language config-format fixtures, read by wt Go + modelman Python contract tests), archive/ (dated docs), superpowers/ (plans+specs)
- `.github/workflows/` — shell-ci (root lint), wt-ci (Go + wt lint), modelman-ci (Python)
- LiteLLM config: `~/.config/litellm/config.yaml`
- LaunchAgent plists: `~/Library/LaunchAgents/local.llamacpp.server.plist` (llama.cpp), `local.litellm.proxy.plist` (LiteLLM) — referenced by the isolation helpers

## Key Gotchas
- **Isolation is mandatory**: local MLX/GGUF models share Apple Silicon GPU/RAM and distort each other's benchmarks. Only one local model loaded at a time.
- **Stop mechanisms per backend**: Ollama `ollama stop <model>` (daemon stays up), oMLX `omlx stop` (halts service), llama.cpp `launchctl unload` (halts LaunchAgent).
- **oMLX serves both 4-bit and 6-bit variants** — warmup must name the exact variant (`omlx` vs `omlx-6bit`).
- **Shebang split**: benchmark scripts use `#!/opt/homebrew/bin/bash` (Homebrew bash); `bin/` helpers use `#!/bin/bash`. Exception: `bin/check-links` uses `#!/usr/bin/env python3` — regex/URL-decoding markdown link parsing isn't reasonable in bash.
- **Results go to `/tmp/<benchmark>-<timestamp>.md`**; archive into `benchmarks/results/`.
- **OpenRouter rows are skipped (N/A) without an API key**: the benchmark reads `OPENROUTER_API_KEY` from `~/Library/LaunchAgents/local.litellm.proxy.plist`; missing key → OpenRouter rows written as N/A.
- **Guide docs embed live `litellm_exposed` snapshots**: guides 00, 02, 04, 05, and 08 all show live `grep`/TOML output of `~/.config/local-ai/modelman.toml` exposure flags. Exposing/unexposing a model makes all five go stale at once — `git grep -n "litellm_exposed = " docs/guides/` before and after touching modelman state to catch drift.

## Adding a New Benchmark Backend
Isolation logic lives in **one place**: `bin/llm-isolate-provider`. The bash
benchmark scripts call it (with `LLM_ISOLATE_*_MODEL` env overrides for their
model names), and `modelman benchmark` delegates to it
(`modelman/src/modelman/benchmark/isolation.py`). Adding a backend:
1. Add to `DIRECT_URLS`, `DIRECT_MODELS`, `LITELLM_MODELS`, `ISOLATE_ID`, `ISOLATE_ENV` associative arrays in the benchmark script
2. Add the model to `~/.config/litellm/config.yaml`
3. Add a new branch in `bin/llm-isolate-provider`'s case statement (and matching `LLM_ISOLATE_*_MODEL` env var) — both the bash scripts and `modelman benchmark` isolate through it
4. modelman side: add a provider entry to `~/.config/local-ai/registry.toml` (via `modelman sync` or the TUI) and confirm the provider id is in `LOCAL_PROVIDERS` in `modelman/src/modelman/benchmark/runner.py` — a backend missing from that set is silently skipped by `modelman benchmark`
5. Smoke test: `./benchmarks/qwen3.8-benchmark 30` (and `bin/llm-isolate-provider <new-backend>`)
6. Update the benchmark doc with new numbers
