# Benchmarks — isolated `llmbench` runs, legacy scripts in `benchmarks/`

> Use this to: benchmark local models the one safe way — isolate a provider so it has Apple Silicon GPU/RAM to itself, run `llmbench`, restore the stack, and read the results.
>
> Verified against: wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29 · revised 2026-10 (commands checked against `wt --help` and `llmbench --help`, not re-run live)

## Prerequisites

- **No other local model loaded.** Local MLX/GGUF models share the Apple Silicon GPU/RAM and distort each other's timings — only one local model may be loaded during any benchmark. Isolation (Step 1) enforces this for the *known* models; see Gotchas for the ollama leftover-caveat.
- Models routed through LiteLLM per [04-litellm-config](04-litellm-config.md). **`llmbench run` has no default target set**: name models with `--model` (or a family with `--family`), or it exits 2 with `name models (--model) or pass --family`. A named model must sit on a local provider (`LOCAL_PROVIDERS`: ollama, omlx, mlx_lm_server, mtplx) — OpenRouter models are never picked, and `--family` also skips cloud-located models such as ollama `:cloud` stubs, which only an explicit `--model` selects (`discover_targets`, `~/github/ohanaverse/local-ai-setup/llmbench/src/llmbench/benchmark/runner.py`). To see the ids a `--family` run could pick:

  ```bash
  python3 -c "import tomllib,os;c=lambda f:tomllib.load(open(os.path.expanduser('~/.config/local-ai/'+f),'rb'));r=c('registry.toml');loc={p['id']:p.get('location') for p in r.get('providers',[])};[print(m['id']) for m in r.get('models',[]) if m['provider_id'] in ('ollama','omlx','mlx_lm_server','mtplx') and 'cloud' not in (m.get('location'),loc.get(m['provider_id']))]"
  ```

  (one id per line, still subject to the `--family` filter and to the model still being in `registry.toml`.)
- Backends healthy: the three-port block in Verification answers (oMLX `:8000`, ollama `:11434`, LiteLLM `:4000`). llama.cpp was retired 2026-09-07 — see [provider-artifacts.md](../reference/provider-artifacts.md).
- llmbench runnable from its directory (`uv run llmbench …` from `/Users/keith/github/ohanaverse/local-ai-setup/llmbench`; llmbench is not installed globally). Provider lifecycle CLI callable the same way:
  `uv run llmbench provider isolate <ollama|omlx|omlx-6bit>` and `uv run llmbench provider restore` from `/Users/keith/github/ohanaverse/local-ai-setup/llmbench` (or `uv run --directory llmbench llmbench provider ...` from the repo root — that's how the `benchmarks/` scripts invoke it).

## TL;DR

<!-- UNVERIFIED — not run end-to-end from this session: the isolate call stops live services and the benchmark run takes minutes and mutates model state. The usage-error paths of the CLI were run live (see Step 1). -->

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/llmbench
uv run llmbench provider isolate omlx --json
# → {"provider": "omlx", "model": "Ornith-1.5-35B-A3B-MLX-4bit",
#    "direct_url": "http://localhost:8000/v1/chat/completions", "ok": true, "error": null}

uv run llmbench run --family <family> --passes 3   # <family>: one of yours — grep '^family' ~/.config/local-ai/registry.toml
# → Benchmark complete: <YYYYMMDD-HHMMSS>
#   Results: /Users/keith/.config/local-ai/benchmarks/<YYYYMMDD-HHMMSS>

uv run llmbench provider restore
# → restored providers

uv run llmbench show-results --latest   # prints summary.md
```

Artifacts land in `/Users/keith/.config/local-ai/benchmarks/<run-id>/` (`summary.md`, `results.json`, `payload.json`; run-id = UTC `YYYYMMDD-HHMMSS` — from `src/llmbench/benchmark/results.py` `write_results`). Note: `llmbench run` calls the same `orchestrate.isolate`/`orchestrate.restore` functions internally, in-process (`src/llmbench/benchmark/isolation.py`) — it isolates before each target and restores in a `finally` block — so the manual isolate is a pre-flight and the manual restore is only needed if you isolated without running, or the run was hard-killed (SIGKILL/SIGTERM) — Ctrl-C still triggers llmbench's restore.

## Steps

### 1. Isolate one provider

`llmbench provider isolate` stops every *other* local provider, then starts + warms the target (warmup = one `max_tokens: 1` chat request, retried up to 90 attempts until the server answers). It's the CLI over `src/llmbench/providers/lifecycle/orchestrate.py`; per-provider start/stop/warmup logic lives in one `Backend` subclass per provider under `src/llmbench/providers/lifecycle/backends/` — this is an in-process port of the old `bin/llm-isolate-provider`/`bin/llm-restore-providers` bash scripts (issue #79; both deleted). Safe read-only probes, verified live from `/Users/keith/github/ohanaverse/local-ai-setup/llmbench`:

```bash
uv run llmbench provider isolate            # no arg
```

```text
Usage: llmbench provider isolate [OPTIONS] {provider_id} [model]
Try 'llmbench provider isolate --help' for help.
╭─ Error ──────────────────────────────────────────────────────────────────────╮
│ Missing argument 'provider_id'.                                              │
╰──────────────────────────────────────────────────────────────────────────────╯
```
(exit 2.)

```bash
uv run llmbench provider isolate notaprovider
```

```text
error: unknown provider: notaprovider
```
(exit 1; the unknown-arg and no-arg paths exit before any service is touched.)

Per-argument behavior (from `backends/ollama.py` and `backends/omlx.py`; unchanged from the bash version this replaced). The warmup models are code defaults — they need not be pulled or registered on your machine; override them as described below:

| Arg | Stops | Starts + warms (model) | Serves on |
|-----|-------|------------------------|-----------|
| `ollama` | oMLX (`omlx stop`) | ollama daemon via `launchctl kickstart` if down; warmup default `ornith-1.5:35b` (`DEFAULT_MODEL`, `backends/ollama.py`) | `http://localhost:11434/v1/chat/completions` |
| `omlx` | ollama (`ollama stop` + `ollama ps` poll) | `omlx start`; warmup default Ornith-1.5 4-bit (`Ornith-1.5-35B-A3B-MLX-4bit`, `DEFAULT_4BIT_MODEL` in `backends/omlx.py`) | `http://localhost:8000/v1/chat/completions` |
| `omlx-6bit` | ollama (`ollama stop` + `ollama ps` poll) | `omlx start`; warmup default Ornith-1.5 6-bit (`Ornith-1.5-35B-A3B-MLX-6bit`, `DEFAULT_6BIT_MODEL` in `backends/omlx.py`) | `http://localhost:8000/v1/chat/completions` |

Model names are env-overridable: `LLM_ISOLATE_OLLAMA_MODEL`, `LLM_ISOLATE_OMLX_4BIT_MODEL`, `LLM_ISOLATE_OMLX_6BIT_MODEL` — this still works (the CLI deliberately keeps the env-var fallback for compatibility with the old bash helpers), but the **now-preferred** form is the explicit positional argument: `uv run llmbench provider isolate ollama <model>` (or `--json` for the machine-readable envelope). With `--json`, it prints a JSON envelope (`provider`, `model`, `direct_url`, `ok`, `error`) — the same 5-key contract the bash script produced — which is what llmbench's own benchmark adapter reads in-process (`src/llmbench/benchmark/isolation.py`) without going through the CLI at all.

`uv run llmbench provider restore` restarts all three services in parallel (ollama, oMLX, LiteLLM), skips any already answering its health URL, and exits 1 if any fails to come back; on success it prints `restored providers` (or the JSON envelope with `--json`).

### 2. Run `llmbench`

<!-- UNVERIFIED — the commands below with a real target stop services via the internal isolation helper. Probe outputs pasted are live. -->

Built-in workloads (`uv run llmbench list-workloads`, verified live from `/Users/keith/github/ohanaverse/local-ai-setup/llmbench`):

```text
chat
code
long
short
```

(`chat` = default, streaming REST-vs-GraphQL prompt; `code` = merge-sorted-lists; `long` = 1024-token gen; `short` = `hi`. Definitions in `src/llmbench/benchmark/workloads/`.)

Flag semantics (from `uv run llmbench run --help` and `src/llmbench/benchmark/cli.py`):

- `--workload <name>` — default `chat`; `LLMBENCH_WORKLOAD` envvar overrides (its old name, `MODELMAN_BENCHMARK_WORKLOAD`, still works).
- `--model <id>` (repeatable) — registry ids (example: `ollama/qwen3.8:27b-mlx`; yours will differ).
- `--family <name>` — all registry models in a family. **`--model` and `--family` stack as an AND-filter** (both are applied in `discover_targets`; only `--direct`/`--litellm` are mutually exclusive). Family names come from `family =` in `~/.config/local-ai/registry.toml` — a family name must match that string exactly (no prefix matching), so check the spelling before running. List yours with `grep '^family' ~/.config/local-ai/registry.toml | sort -u`.
- `--direct` / `--litellm` — scope to one route; default benchmarks BOTH (direct URL + `http://localhost:4000/v1`), meaning every pass issues two requests per target.
- `--passes N` (default 1), `--cooldown <seconds>` (default 15.0) — sleep between passes, not between routes.
- `--results-dir <path>` — default `/Users/keith/.config/local-ai/benchmarks`.
- Targets come from local providers only (`ollama`, `omlx`, `mlx_lm_server`, `mtplx`; llamacpp retired 2026-09-07): OpenRouter rows are out of scope, and cloud-located models (ollama `:cloud`) are skipped unless named with `--model`. Which providers your targets can come from depends on the `[[providers]]` in your registry (`grep -A1 '^\[\[providers\]\]' ~/.config/local-ai/registry.toml`).

### 3. Multi-pass methodology

Single-pass numbers wobble (thermal state, cold weights, background system churn). The summary aggregates by **median** per (model, route) across passes (`_aggregate` in `results.py`), so more passes = stabler medians; the cooldown lets the machine cool between passes (the legacy multi-pass scripts default to the same 15 s). Recommended starting point:

<!-- UNVERIFIED — mutates live services — not driven; see verified error paths. -->
```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/llmbench
uv run llmbench run --workload chat --family <family> --passes 3
```

(For a family with one model: 3 passes × 2 routes × 1 target ≈ 6 requests, plus a 15 s cooldown after passes 1 and 2. Use `--passes 5` when comparing two configs; keep `--cooldown` at the default.)

### 4. Read results

<!-- UNVERIFIED — needs a completed run; only the error paths below were run live. -->

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/llmbench
uv run llmbench show-results --latest        # latest run pointer
uv run llmbench show-results --run-id 20260829-120000
```

`summary.md` shape (from `results.py::_render_markdown`): header with run-id/workload/record count, a **Summary (median per model / route)** table (`Model (route) | Passes | TTFT (ms) | Total (ms) | Throughput (tok/s)`), then a raw per-pass table. Error paths, all verified live:

```text
error: specify --latest or --run-id                       # no args
error: no latest run recorded                             # never ran a benchmark
error: results not found: /Users/keith/.config/local-ai/benchmarks/<run-id>/summary.md
```

Latest-run pointer: after a run, `cli.py` writes `last_run` / `last_run_dir` into `~/.config/local-ai/benchmarks/latest.toml` (override: `LLMBENCH_LATEST`), beside the results. Until a run has completed there is no such file (`cat ~/.config/local-ai/benchmarks/latest.toml` fails) and `--latest` errors as shown above. Gotcha: `--run-id` always resolves under the default dir even if you overrode `--results-dir` (hardcoded in `cli.py`); for custom-dir runs, open `summary.md` by hand.

### 5. Legacy scripts (superseded — kept for history)

`qwen3.8-benchmark*` / `ornith-1.5-benchmark*` bash scripts (also installed in `~/.local/bin/`) predate the benchmark CLI. One-liners, usage from the script headers:

<!-- UNVERIFIED — each script stops/starts live services around every backend. -->
```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/benchmarks
./qwen3.8-benchmark              # single pass, 200 max_tokens (arg 2 = custom prompt)
./qwen3.8-benchmark-multi 5 200 30    # 5 passes, 200 max_tokens, 30 s cooldown; one file per pass, no aggregation
./ornith-1.5-benchmark           # single pass, four Ornith-1.5-35B variants
./ornith-1.5-benchmark-multi 3   # 3 passes (PASSES [max_tokens] [cooldown])
```

Results are written to `/tmp/<script>-<timestamp>.md`; archive into `benchmarks/results/` (archived runs already there, e.g. `qwen3.8-benchmark-20260827-084908.md`, `ornith-1.5-benchmark-20260826-224834.md`):

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
cp /tmp/<script>-<timestamp>.md benchmarks/results/
```

Reference docs: `benchmarks/qwen3.8-benchmark.md`, `benchmarks/ornith-1.5-benchmark.md`. For new work use `llmbench` — the legacy scripts do not feed `show-results`/spend tooling.

## Verification

Backends back after a restore — four-port block (consistent with guides 01/04; verified live on 2026-08-29):

```bash
curl -s -m 2 http://localhost:11434/api/tags -o /dev/null -w "11434(ollama):%{http_code}\n"
curl -s -m 2 http://localhost:8000/health -o /dev/null -w "8000(omlx):%{http_code}\n"         # /health — plain / gives 404
curl -s -m 2 http://localhost:4000/v1/models -o /dev/null -w "4000(litellm):%{http_code}\n"
```

```text
11434(ollama):200
8000(omlx):200
4000(litellm):401
```

(401 on `:4000` = proxy up and demanding the master key; 200 elsewhere. See guide 01 Verification (master key from `local.litellm.proxy.plist`) or guide 04 Verification for key-authenticated checks.)

Nothing left loaded in ollama after isolation/restore cycles: `ollama ps` prints the header only (live):

```text
NAME    ID    SIZE    PROCESSOR    CONTEXT    UNTIL
```

Results file exists at the stated path:

<!-- UNVERIFIED — requires a completed benchmark run. -->
```bash
ls /Users/keith/.config/local-ai/benchmarks/
# → <YYYYMMDD-HHMMSS>/   (one dir per run; contains summary.md, results.json, payload.json)
```

## Gotchas

- **Isolation is mandatory.** Local models share Apple Silicon GPU/RAM; a second loaded model skews every number in the run (this repo's `CLAUDE.md`). llmbench enforces it internally — each target is isolated through `src/llmbench/providers/lifecycle/orchestrate.py` (called in-process, not via a subprocess or PATH lookup — issue #79) before its requests, and the whole stack is restored in a `finally` (`src/llmbench/benchmark/isolation.py`).
- **Per-backend stop mechanics differ.** Ollama: `ollama stop <model>` unloads the model but keeps the daemon on `:11434` (isolation polls `ollama ps`, not the port); oMLX: `omlx stop` halts the whole service.
- **oMLX serves 4-bit and 6-bit variants — name the exact one.** Manual isolation: `uv run llmbench provider isolate omlx` warms `Ornith-1.5-35B-A3B-MLX-4bit`, `... omlx-6bit` warms the 6-bit variant. llmbench always passes the provider id (`omlx`, never `omlx-6bit`), so an oMLX 6-bit target would be warmed as 4-bit — this bites only if your registry has an oMLX 6-bit model as a benchmark target; keep it in mind for future backends.
- **The isolate command only stops the *named* ollama model.** `ollama stop` targets the code default `ornith-1.5:35b` (`DEFAULT_MODEL` in `backends/ollama.py`; override with `LLM_ISOLATE_OLLAMA_MODEL`); a different ollama model you left loaded earlier survives isolation and will still fight for GPU/RAM. Unload it by hand or override the env var.
- **Fixed warmup model for `ollama` isolation.** `uv run llmbench provider isolate ollama` warms a FIXED model (`LLM_ISOLATE_OLLAMA_MODEL`, code default `ornith-1.5:35b` set in `backends/ollama.py`), not the benchmark target — benchmarking any other ollama model requires `export LLM_ISOLATE_OLLAMA_MODEL=<target-model>` before `llmbench run` (this also makes the `ollama stop`/poll path correct when isolating other backends). Two resident models = GPU/RAM contention = garbage timings.
- **`--run-id` ignores `--results-dir`** — it reads `/Users/keith/.config/local-ai/benchmarks/<run-id>/summary.md` only.
- **Shebang split.** `benchmarks/*` scripts use Homebrew bash (`#!/opt/homebrew/bin/bash`); `bin/*` (now just `check-links` and `mlx-quantize`) uses `#!/bin/bash` (`check-links` is Python, `#!/usr/bin/env python3`). Don't normalize one onto the other (this repo's `CLAUDE.md`, `make lint-shell` enforces style).
- **Two result homes.** Legacy script output goes to `/tmp/<script>-<timestamp>.md` and should be archived into `/Users/keith/github/ohanaverse/local-ai-setup/benchmarks/results/`; llmbench runs write under `/Users/keith/.config/local-ai/benchmarks/<run-id>/` — not inside this repo.
- **OpenRouter rows are N/A without an API key** (legacy scripts read `OPENROUTER_API_KEY` from `~/Library/LaunchAgents/local.litellm.proxy.plist`).
- **Run llmbench from the repo.** llmbench is not installed globally. Always run it with `uv run llmbench …` from `/Users/keith/github/ohanaverse/local-ai-setup/llmbench`.

## Going deeper

- Benchmark CLI design (isolation contract, workload spec, results shape): `~/github/ohanaverse/local-ai-setup/docs/superpowers/modelman/specs/2026-09-05-modelman-benchmark-design.md`
- Provider lifecycle CLI + orchestration, stop/start/warmup per backend: `~/github/ohanaverse/local-ai-setup/llmbench/src/llmbench/providers/lifecycle/` (`cli.py` the `llmbench provider` commands, `orchestrate.py` isolate/stop/stop-all/restore, `backends/` one module per provider), and `/Users/keith/github/ohanaverse/local-ai-setup/CLAUDE.md` (Key Gotchas)
- Legacy benchmark docs + archived numbers: `/Users/keith/github/ohanaverse/local-ai-setup/benchmarks/README.md`, `.../qwen3.8-benchmark.md`, `.../ornith-1.5-benchmark.md`
- llmbench source: `~/github/ohanaverse/local-ai-setup/llmbench/src/llmbench/benchmark/` (`cli.py` flags/pointer, `runner.py` target discovery, `results.py` markdown, `isolation.py` in-process lifecycle adapter)
- Launching `wt` agents against the benchmarked models: [06-wt-agents-and-models](06-wt-agents-and-models.md)
- Spend/usage data the proxy logs per benchmark request: [07-usage-and-spend](07-usage-and-spend.md)
- Agentic (not single-turn) coding benchmarks — real task, gates + judge: [09-agent-benchmarks](09-agent-benchmarks.md)
