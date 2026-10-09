# CLAUDE.md

## Docs
- User playbooks: `docs/guides/` — canonical task guides (config map, setup, models, families, LiteLLM, benchmarks, wt, usage, maintenance, agent benchmarks, MLX quantization, capability eval). Read `docs/guides/00-config-map.md` first for config-file ownership.
- `./issues.md` — follow-ups from the 2026-08-29 guide-set review (items 1–5 FIXED, kept as a historical record), plus one open, deferred item: #6, `piCompat` full-pointer replacement in `wt/internal/agents/pi_models.go`.
- Package-level context: `llmbench/CLAUDE.md` (Python benchmarks + provider isolation) and `wt/CLAUDE.md` (Go worktree launcher) contain per-package commands, architecture, and gotchas.

## Commands
- `./benchmarks/qwen3.8-benchmark [max_tokens]` — single-pass benchmark (3 local backends — ollama/omlx/mtplx — + OpenRouter)
- `./benchmarks/qwen3.8-benchmark-multi N [max_tokens] [cooldown]` — multi-pass for stable medians
- `./benchmarks/ornith-1.5-benchmark [max_tokens]` — single-pass (3 Ornith-1.5-35B local variants: ollama, omlx 4-bit, omlx 6-bit; no OpenRouter)
- `./benchmarks/ornith-1.5-benchmark-multi N` — multi-pass
- `uv run --directory llmbench llmbench agent run --suite <path>` — agentic coding benchmark (real task, gates + judge); see `docs/guides/09-agent-benchmarks.md`. `--directory` makes `llmbench/` the working directory, so a relative `<path>` (here and in `eval run`, `--root`, `--results-dir`) resolves from there: `../benchmarks/suites/smoke.toml`, not `benchmarks/suites/smoke.toml`
- `uv run --directory llmbench llmbench eval run --suite <path>` — cross-category capability benchmark (reasoning/planning/coding/code_review/doc_summary, single-turn, judged + EvalPlus); see `docs/guides/11-capability-eval-benchmark.md`
- `uv run --directory llmbench llmbench provider isolate <ollama|omlx|omlx-6bit|mtplx>` — stop others, start+warmup one (for the benchmarks; llamacpp is retired-only, present in `BACKENDS` but excluded from `SUPPORTED_PROVIDER_IDS` — see `docs/reference/provider-artifacts.md`)
- `uv run --directory llmbench llmbench provider isolate mlx_lm_server <target> --draft <draft>` — isolate a target+draft speculative-decoding pairing on port 8001; no default pairing exists, target/draft must always be passed (positional `target` + `--draft`, or `LLM_ISOLATE_MLXLM_MODEL`/`LLM_ISOLATE_MLXLM_DRAFT_MODEL`)
- `uv run --directory llmbench llmbench provider restore` — bring all providers back up after a benchmark
- `uv run --directory llmbench llmbench provider stop <provider>` — stop one provider (e.g. `mtplx`, ~28GB resident); `wt smoke --json` (or non-TTY stdin) skips its exit stop prompt, so the server is left running
- `wt cloud-sync [--only prices,catalog] [--dry-run] [--yes --approve-removals DIGEST] [--force] [--html FILE]` — refresh OpenRouter prices and mirror ollama.com/pricing (cloud models, prices incl. off-peak, pulls and removals) into `registry.toml`, ollama and the LiteLLM routes; a flow whose provider the registry does not use is skipped (exit 0), and exit codes 2–5 say why the catalog flow changed nothing. Start with `--dry-run`; see the `cloud-sync` skill in `wt/.claude/skills/` and `wt/docs/wt-cloud-sync.md`.
- `bin/mlx-quantize <convert|dynamic-quant|dwq> --model <repo-or-path> [--mlx-path <out-dir>]` — thin wrapper around the omlx-bundled mlx_lm quantization tools; see `docs/guides/10-mlx-lm-quantization.md`
- `make lint-shell` — validate `bash -n` and `shellcheck --severity=error` across the Makefile's `SHELL_SCRIPTS` list (`bin/`, `benchmarks/`, wt shims, `litellm-session-logs/` script)
- `make lint` — umbrella target (`lint-shell` + `check-links`); lighter than `test-all`
- `make test-all` — one-stop local verification mirroring CI: lint + llmbench `make check`/`make test` + wt `go build`/`vet`/`test`
- `make install` — install all monorepo components (wt binary + shims via `wt/make install`, llmbench into its own venv); needs Go 1.26.7 on PATH — on a fresh box `mise use -g go@1.26.7` (matches wt-ci)
- `bin/check-links` (or `make check-links`) — validates repo-relative links in every git-tracked `*.md` (excludes `docs/superpowers/`, `.venv`); stdlib-only Python

## Architecture
- `benchmarks/` — bash benchmark scripts, docs, and `results/` (per-run markdown)
- `bin/` — monorepo-wide utilities not owned by any one package (`check-links`, `mlx-quantize` + its `lib/mlx-lm-resolve.sh`, `extract-claude-skills` — pulls embedded skill/prompt markdown out of Claude Code binaries; `check-config-dirs-untouched` — the CI step llmbench-ci and wt-ci run after their tests, failing if a test created `agent-wt`, `local-ai` or `litellm` under the real config home). Provider isolation used to live here as bash helpers (`llm-isolate-provider`/`llm-restore-providers`); that logic was ported to Python (issue #79) and now lives in `llmbench/src/llmbench/providers/lifecycle/` (`orchestrate.py` + `backends/`), exposed as the `llmbench provider isolate/stop/stop-all/restore/list` CLI. The benchmarks call the orchestrator in-process (no subprocess, no PATH lookup); the benchmark scripts under `benchmarks/` call the CLI via `uv run --directory llmbench llmbench provider ...`.
- `litellm-session-logs/` — standalone pipeline (psql SQL + sh + stdlib-only Python, own CLAUDE.md) that pulls one LiteLLM proxy session's request/response logs from the Postgres `LiteLLM_SpendLogs` table and rebuilds readable chat transcripts; not wired into wt, run manually per session
- `llmbench/` — the benchmarks and the provider isolation they need (Python/uv; `src/llmbench`, own `CLAUDE.md`, own `Makefile`). Three benchmarks under one package: throughput (`llmbench run`), the agentic coding benchmark (`benchmark/agent/`, `llmbench agent`) and the cross-category capability benchmark (`benchmark/eval/`, `llmbench eval`), plus `llmbench provider isolate|stop|stop-all|restore|list` (`providers/lifecycle/`). Reads `registry.toml` read-only; keeps its latest-run pointers in `~/.config/local-ai/benchmarks/latest.toml`.
- `wt/` — worktree agent launcher (Go module; `cmd/wt`, `internal/`, own `CLAUDE.md`, own `Makefile`). Reads `registry.toml` and writes it through one path, `config.UpdateRegistry` (lock, symlink write-through, byte-stable output via `internal/tomlw`), called by `wt model init` (create the file, seed provider rows) by `wt model add|edit|rm` and the Models tab of `wt config` (model rows, `internal/modeladmin`; `wt model list` shows them with live status) and by `wt cloud-sync` (cloud prices and the ollama cloud catalog); what is on disk and what is running come from live probes of the providers, never from a stored flag, and the stale-pricing notice takes its date from `registry.toml`'s `pricing_updated_at` stamps; owns `~/.config/agent-wt/config.toml` (including the `[litellm]` routing state: enabled/url/api_key), rotation + usage state, and **all LiteLLM management**: `wt litellm sync|list|providers|status|on|off|set` (`internal/litellm`) writes `config.yaml`, restarts the proxy, and `wt start`/`wt stop`/`wt smoke` keep local-model routes current automatically
- `Makefile` — lint target for shell scripts (root + wt), `check-links` (all tracked markdown), `test-all` (aggregates llmbench + wt)
- `docs/` — guides/ (user playbooks — see Docs above), reference/, contracts/ (cross-language config-format fixtures, read by wt's Go contract tests; llmbench reads `registry.sample.toml` and `registry.written.sample.toml`, the registry in the exact form wt's writer and tomli-w give it — it holds no comments, so what it is for is recorded in the tests that read it), archive/ (dated docs), superpowers/ (plans+specs)
- `.github/workflows/` — shell-ci (root lint), wt-ci (Go + wt lint), llmbench-ci (Python)
- LiteLLM config: `~/.config/litellm/config.yaml` — written only by wt (`WT_LITELLM_CONFIG` overrides the path; `WT_LITELLM_RESTART_CMD` overrides the proxy restart; the `MODELMAN_LITELLM_*` names are legacy aliases). The path follows none of `WT_REGISTRY`, `MODELMAN_REGISTRY` (its legacy alias) and `XDG_CONFIG_HOME`, so wt refuses every route write and dry run (`litellm.ErrRegistryRedirected`) when the registry is redirected and nothing names config.yaml — **any ad-hoc `wt` run against a scratch registry must set `WT_LITELLM_CONFIG` too**
- LaunchAgent plists: `~/Library/LaunchAgents/local.litellm.proxy.plist` (LiteLLM) — referenced by the isolation helpers. (The llama.cpp plist was retired 2026-09-07 — artifact + restore steps in `docs/reference/provider-artifacts.md`.)
- `mlx_lm_server` provider — one target+draft speculative-decoding pairing served by `mlx_lm.server --draft-model` as a plain backgrounded subprocess (pidfile `/tmp/local-ai-setup-mlx-lm-server.pid`, driven by `llmbench/src/llmbench/providers/lifecycle/pidproc.py`'s generic pidfile-tracked-process helper plus `backends/mlx_lm_server.py`), never a LaunchAgent (one model per process; sweeping many pairings means restarting it between them, not baking one into a plist). `local_path`-sourced artifacts (from `bin/mlx-quantize`, or the `omlx`/`mlx_lm_server` providers' local-path fields) are user-produced and wt never deletes them (`wt model rm` prints the path) — cleanup after a failed experiment is a manual `rm -rf`. See `docs/reference/provider-artifacts.md` and `docs/guides/10-mlx-lm-quantization.md`.

## Key Gotchas
- **Benchmark isolation is still mandatory**: `llmbench` and the legacy benchmark scripts still enforce full exclusivity (only one local model loaded at a time) for clean measurement — local MLX/GGUF models share Apple Silicon GPU/RAM and distort each other's results otherwise. Normal (non-benchmark) usage via `wt start` allows multiple local models to run concurrently — several omlx models can be loaded in its pool; mtplx and mlx_lm_server serve one model per process (see `wt/CLAUDE.md`, "Lifecycle") — advisory only, not enforced.
- **Stop mechanisms per backend**: Ollama `ollama stop <model>` (daemon stays up), oMLX `wt stop <model>` unloads one model (`POST /v1/models/{id}/unload`); `omlx stop` / `wt stop omlx` / `wt stop --all` (every running local model first) halts the service, MTPLX `mtplx stop --port 8003 --grace-seconds 10` (single-model-per-process, a plain backgrounded subprocess tracked by a pidfile, never a LaunchAgent). (llama.cpp — formerly `launchctl unload` — was retired 2026-09-07; see `docs/reference/provider-artifacts.md`.)
- **oMLX serves both 4-bit and 6-bit variants** — warmup must name the exact variant (`omlx` vs `omlx-6bit`).
- **Shebang split**: benchmark scripts use `#!/opt/homebrew/bin/bash` (Homebrew bash); `bin/` helpers use `#!/bin/bash`. Exceptions: `bin/check-links` and `bin/extract-claude-skills` use `#!/usr/bin/env python3` — regex/URL-decoding markdown link parsing isn't reasonable in bash.
- **Results go to `/tmp/<benchmark>-<timestamp>.md`**; archive into `benchmarks/results/`.
- **OpenRouter rows are skipped (N/A) without an API key**: `qwen3.8-benchmark` (the only script with OpenRouter rows) reads `OPENROUTER_API_KEY` from `~/Library/LaunchAgents/local.litellm.proxy.plist`; missing key → OpenRouter rows written as N/A.
- **Guides never embed live model state**: no counts, routing snapshots, or "today that's X, Y" inventories in `docs/guides/` — adding, removing, pulling or running a model must never require a guide edit. Show the command that reveals the state instead (`wt litellm list`, `wt model list`), and label any captured output as an illustrative or dated example.
- **Routing is derived, not stored**: no file marks a model as routed. `wt` builds `config.yaml`'s `model_list` from `registry.toml` plus live probes — a cloud model always, a local model while it runs (an ollama model while it is pulled). So whether a model is routed is `wt litellm list` (`config.yaml` membership), which `wt start`/`wt stop`/`wt litellm sync` and a launch through LiteLLM (of a running local model, or of a registry cloud model whose route is missing) change.

## Quick test commands

For focused test runs without live provider interference:
```bash
# llmbench (Python) — conftest.py runs the suite under a scratch HOME with provider binaries stubbed
cd llmbench && uv run pytest tests/providers/lifecycle/test_orchestrate.py -q

# wt (Go) — no special isolation needed
cd wt && go test ./cmd/wt -run TestStats
```
See `llmbench/CLAUDE.md` and `wt/CLAUDE.md` for package-specific test patterns.

## Adding a New Benchmark Backend
See the `adding-a-benchmark-backend` skill.
