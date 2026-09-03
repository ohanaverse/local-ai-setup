# Benchmarks — isolated `modelman benchmark` runs, legacy scripts in `benchmarks/`

> Use this to: benchmark local models the one safe way — isolate a provider so it has Apple Silicon GPU/RAM to itself, run `modelman benchmark`, restore the stack, and read the results.
>
> Verified against: modelman 0.1.0, wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29

## Prerequisites

- **No other local model loaded.** Local MLX/GGUF models share the Apple Silicon GPU/RAM and distort each other's timings — only one local model may be loaded during any benchmark. Isolation (Step 1) enforces this for the *known* models; see Gotchas for the ollama leftover-caveat.
- Models exposed through LiteLLM per [04-litellm-config](04-litellm-config.md). Default `modelman benchmark run` only picks local models with `litellm_exposed = true` in `~/.config/local-ai/modelman.toml` (`discover_targets`, `~/github/ohanaverse/local-ai-setup/modelman/src/modelman/benchmark/runner.py`); today that's `ollama/qwen3.8:27b-mlx` and `ollama/ornith-1.5:35b` (24 models are exposed in total — thirteen ollama, the two local MLX downloads plus eleven cloud — and eleven openrouter — but only those two are local MLX downloads). If a model you want isn't in that set, pass `--model`/`--family` to bypass the exposure filter, or `expose` it first (guide 04 §2).
- Backends healthy: the four-port block in Verification answers (llama.cpp `:8080`, oMLX `:8000`, ollama `:11434`, LiteLLM `:4000`).
- modelman runnable from its repo (`uv run modelman …` from `/Users/keith/github/ohanaverse/local-ai-setup/modelman`; modelman is not installed globally). Isolation helpers callable:
  `bin/llm-isolate-provider <ollama|llamacpp|omlx|omlx-6bit>` and `bin/llm-restore-providers` from `/Users/keith/github/ohanaverse/local-ai-setup`.

## TL;DR

<!-- UNVERIFIED — not run end-to-end from this session: the isolate call stops live services and the benchmark run takes minutes and mutates model state. The usage-error paths of both bin/ helpers were run live (see Step 1). -->

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
bin/llm-isolate-provider omlx
# → {"provider": "omlx", "model": "Ornith-1.5-35B-A3B-MLX-4bit",
#    "direct_url": "http://localhost:8000/v1/chat/completions", "ok": true, "error": null}

# from: /Users/keith/github/ohanaverse/local-ai-setup/modelman
uv run modelman benchmark run --family ornith-1.5:35b --passes 3
# → Benchmark complete: <YYYYMMDD-HHMMSS>
#   Results: /Users/keith/.config/local-ai/benchmarks/<YYYYMMDD-HHMMSS>

# from: /Users/keith/github/ohanaverse/local-ai-setup
bin/llm-restore-providers
# → [llm-restore-providers] providers restored

# from: /Users/keith/github/ohanaverse/local-ai-setup/modelman
uv run modelman benchmark show-results --latest   # prints summary.md
```

Artifacts land in `/Users/keith/.config/local-ai/benchmarks/<run-id>/` (`summary.md`, `results.json`, `payload.json`; run-id = UTC `YYYYMMDD-HHMMSS` — from `src/modelman/benchmark/results.py` `write_results`). Note: `modelman benchmark run` calls the same two `bin/` helpers internally — it isolates before each target and restores in a `finally` block — so the manual isolate is a pre-flight and the manual restore is only needed if you isolated without running, or the run was hard-killed (SIGKILL/SIGTERM) — Ctrl-C still triggers modelman's restore.

## Steps

### 1. Isolate one provider

`bin/llm-isolate-provider` stops every *other* local provider, then starts + warms the target (warmup = one `max_tokens: 1` chat request, retried up to 90 attempts until the server answers). Safe read-only probes, verified live from `/Users/keith/github/ohanaverse/local-ai-setup`:

```bash
bin/llm-isolate-provider            # no arg
```

```text
usage: llm-isolate-provider <provider-id>
```

```bash
bin/llm-isolate-provider notaprovider
```

```text
unknown provider: notaprovider
```
(exit 1; the unknown-arg and no-arg paths exit before any service is touched.)

Per-argument behavior (from the script itself, `bin/llm-isolate-provider`):

| Arg | Stops | Starts + warms (model) | Serves on |
|-----|-------|------------------------|-----------|
| `ollama` | oMLX (`omlx stop`), llama.cpp (`launchctl unload`) | ollama daemon via `launchctl kickstart` if down; warmup `ornith-1.5:35b` | `http://localhost:11434/v1/chat/completions` |
| `llamacpp` | ollama (`ollama stop` + `ollama ps` poll), oMLX (`omlx stop` + port poll) | `launchctl load -w ~/Library/LaunchAgents/local.llamacpp.server.plist`; warmup `local-llama` | `http://localhost:8080/v1/chat/completions` |
| `omlx` | ollama (`ollama stop` + `ollama ps` poll), llama.cpp | `omlx start`; warmup Ornith-1.5 4-bit (`Ornith-1.5-35B-A3B-MLX-4bit`) | `http://localhost:8000/v1/chat/completions` |
| `omlx-6bit` | ollama (`ollama stop` + `ollama ps` poll), llama.cpp | `omlx start`; warmup Ornith-1.5 6-bit (`Ornith-1.5-35B-A3B-MLX-6bit`) | `http://localhost:8000/v1/chat/completions` |

Model names are env-overridable: `LLM_ISOLATE_OLLAMA_MODEL`, `LLM_ISOLATE_LLAMACPP_MODEL`, `LLM_ISOLATE_OMLX_4BIT_MODEL`, `LLM_ISOLATE_OMLX_6BIT_MODEL`. On success it prints a JSON envelope (`provider`, `model`, `direct_url`, `ok`, `error`) — that contract is what modelman's adapter parses (`~/github/ohanaverse/local-ai-setup/modelman/src/modelman/benchmark/isolation.py`).

`bin/llm-restore-providers` restarts all four services in parallel (ollama, oMLX, llama.cpp, LiteLLM), skips any already answering its health URL, and exits 1 if any fails to come back.

### 2. Run `modelman benchmark`

<!-- UNVERIFIED — the commands below with a real target stop services via the internal isolation helper. Probe outputs pasted are live. -->

Built-in workloads (`uv run modelman benchmark list-workloads`, verified live from `/Users/keith/github/ohanaverse/local-ai-setup/modelman`):

```text
chat
code
long
short
```

(`chat` = default, streaming REST-vs-GraphQL prompt; `code` = merge-sorted-lists; `long` = 1024-token gen; `short` = `hi`. Definitions in `src/modelman/benchmark/workloads/`.)

Flag semantics (from `uv run modelman benchmark run --help` and `src/modelman/benchmark/cli.py`):

- `--workload <name>` — default `chat`; `MODELMAN_BENCHMARK_WORKLOAD` envvar overrides.
- `--model <id>` (repeatable) — registry ids, e.g. `ollama/qwen3.8:27b-mlx`.
- `--family <name>` — all registry models in a family. **`--model` and `--family` stack as an AND-filter** (both are applied in `discover_targets`; only `--direct`/`--litellm` are mutually exclusive). Family names come from `family =` in `~/.config/local-ai/registry.toml` — they currently mirror per-model ids (`qwen3.8:27b-mlx`, `ornith-1.5:35b`, …), so a bare `--family qwen3.8` matches nothing.
- `--direct` / `--litellm` — scope to one route; default benchmarks BOTH (direct URL + `http://localhost:4000/v1`), meaning every pass issues two requests per target.
- `--passes N` (default 1), `--cooldown <seconds>` (default 15.0) — sleep between passes, not between routes.
- `--results-dir <path>` — default `/Users/keith/.config/local-ai/benchmarks`.
- Targets are local providers only (`ollama`, `llamacpp`, `omlx`); OpenRouter/cloud rows are out of scope for modelman runs. This machine's registry currently carries only the `ollama` provider, so every target today is `ollama/*`.

### 3. Multi-pass methodology

Single-pass numbers wobble (thermal state, cold weights, background system churn). The summary aggregates by **median** per (model, route) across passes (`_aggregate` in `results.py`), so more passes = stabler medians; the cooldown lets the machine cool between passes (the legacy multi-pass scripts default to the same 15 s). Recommended starting point:

<!-- UNVERIFIED — mutates live services — not driven; see verified error paths. -->
```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/modelman
uv run modelman benchmark run --workload chat --family ornith-1.5:35b --passes 3
```

(3 passes × 2 routes × 1 target ≈ 6 requests, plus a 15 s cooldown after passes 1 and 2. Use `--passes 5` when comparing two configs; keep `--cooldown` at the default.)

### 4. Read results

<!-- UNVERIFIED — needs a completed run; only the error paths below were run live. -->

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/modelman
uv run modelman benchmark show-results --latest        # latest run pointer
uv run modelman benchmark show-results --run-id 20260829-120000
```

`summary.md` shape (from `results.py::_render_markdown`): header with run-id/workload/record count, a **Summary (median per model / route)** table (`Model (route) | Passes | TTFT (ms) | Total (ms) | Throughput (tok/s)`), then a raw per-pass table. Error paths, all verified live:

```text
error: specify --latest or --run-id                       # no args
error: no latest run recorded                             # never ran a benchmark
error: results not found: /Users/keith/.config/local-ai/benchmarks/<run-id>/summary.md
```

Latest-run pointer: after a run, `cli.py` writes `benchmarks.last_run` / `benchmarks.last_run_dir` into `~/.config/local-ai/modelman.toml` (state file, not the registry). Honest note: today's `modelman.toml` has only `[model_state]`/`[families]` — no `[benchmarks]` table yet, because no CLI run has completed on this machine. Gotcha: `--run-id` always resolves under the default dir even if you overrode `--results-dir` (hardcoded in `cli.py`); for custom-dir runs, open `summary.md` by hand.

### 5. Legacy scripts (superseded — kept for history)

`qwen3.8-benchmark*` / `ornith-1.5-benchmark*` bash scripts (also installed in `~/.local/bin/`) predate the modelman integration. One-liners, usage from the script headers:

<!-- UNVERIFIED — each script stops/starts live services around every backend. -->
```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/benchmarks
./qwen3.8-benchmark              # single pass, 200 max_tokens (arg 2 = custom prompt)
./qwen3.8-benchmark-multi 5 200 30    # 5 passes, 200 max_tokens, 30 s cooldown; one file per pass, no aggregation
./ornith-1.5-benchmark           # single pass, four Ornith-1.5-35B variants
./ornith-1.5-benchmark-multi 3   # 3 passes (PASSES [max_tokens] [cooldown])
```

Results are written to `/tmp/<script>-<timestamp>.md`; archive into `benchmarks/results/` (9 archived runs already there, e.g. `qwen3.8-benchmark-20260827-084908.md`, `ornith-1.5-benchmark-20260826-224834.md`):

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
cp /tmp/<script>-<timestamp>.md benchmarks/results/
```

Reference docs: `benchmarks/qwen3.8-benchmark.md`, `benchmarks/ornith-1.5-benchmark.md`. For new work use `modelman benchmark` — the legacy scripts do not feed `show-results`/spend tooling.

## Verification

Backends back after a restore — four-port block (consistent with guides 01/04; verified live on 2026-08-29):

```bash
curl -s -m 2 http://localhost:11434/api/tags -o /dev/null -w "11434(ollama):%{http_code}\n"
curl -s -m 2 http://localhost:8000/health -o /dev/null -w "8000(omlx):%{http_code}\n"         # /health — plain / gives 404
curl -s -m 2 http://localhost:8080/health -o /dev/null -w "8080(llama.cpp):%{http_code}\n"
curl -s -m 2 http://localhost:4000/v1/models -o /dev/null -w "4000(litellm):%{http_code}\n"
```

```text
11434(ollama):200
8000(omlx):200
8080(llama.cpp):200
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

- **Isolation is mandatory.** Local models share Apple Silicon GPU/RAM; a second loaded model skews every number in the run (this repo's `CLAUDE.md`). modelman enforces it internally — each target is isolated through `bin/llm-isolate-provider` before its requests and the whole stack is restored in a `finally` — which is why that helper must be on PATH — modelman locates it via `shutil.which` (`src/modelman/benchmark/isolation.py:23`).
- **Per-backend stop mechanics differ.** Ollama: `ollama stop <model>` unloads the model but keeps the daemon on `:11434` (isolation polls `ollama ps`, not the port); oMLX: `omlx stop` halts the whole service; llama.cpp: `launchctl unload ~/Library/LaunchAgents/local.llamacpp.server.plist`.
- **oMLX serves 4-bit and 6-bit variants — name the exact one.** Manual isolation: `bin/llm-isolate-provider omlx` warms `Ornith-1.5-35B-A3B-MLX-4bit`, `... omlx-6bit` warms the 6-bit variant. modelman always passes the provider id (`omlx`, never `omlx-6bit`), so an oMLX 6-bit target would be warmed as 4-bit — dormant today (no `omlx` provider in the registry yet), keep in mind for future backends.
- **The isolate helper only stops the *named* ollama model.** `ollama stop` targets `ornith-1.5:35b` by default (`LLM_ISOLATE_OLLAMA_MODEL`); a different ollama model you left loaded earlier survives isolation and will still fight for GPU/RAM. Unload it by hand or override the env var.
- **Fixed warmup model for `ollama` isolation.** `bin/llm-isolate-provider ollama` warms a FIXED model (`LLM_ISOLATE_OLLAMA_MODEL`, default `ornith-1.5:35b`), not the benchmark target — benchmarking any other ollama model requires `export LLM_ISOLATE_OLLAMA_MODEL=<target-model>` before `modelman benchmark run` (this also makes the `ollama stop`/poll path correct when isolating other backends). Two resident models = GPU/RAM contention = garbage timings.
- **`--run-id` ignores `--results-dir`** — it reads `/Users/keith/.config/local-ai/benchmarks/<run-id>/summary.md` only.
- **Shebang split.** `benchmarks/*` scripts use Homebrew bash (`#!/opt/homebrew/bin/bash`); `bin/*` helpers use `#!/bin/bash`. Don't normalize one onto the other (this repo's `CLAUDE.md`, `make lint-shell` enforces style).
- **Two result homes.** Legacy script output goes to `/tmp/<script>-<timestamp>.md` and should be archived into `/Users/keith/github/ohanaverse/local-ai-setup/benchmarks/results/`; modelman runs write under `/Users/keith/.config/local-ai/benchmarks/<run-id>/` — not inside this repo.
- **OpenRouter rows are N/A without an API key** (legacy scripts read `OPENROUTER_API_KEY` from `~/Library/LaunchAgents/local.litellm.proxy.plist`).
- **Run modelman from the repo.** modelman is not installed globally. Always run it with `uv run modelman …` from `/Users/keith/github/ohanaverse/local-ai-setup/modelman`.

## Going deeper

- Benchmark CLI design (isolation contract, workload spec, results shape): `~/github/ohanaverse/local-ai-setup/modelman/docs/superpowers/specs/2026-09-05-modelman-benchmark-design.md`
- Isolation helpers, stop/start/warmup per backend: `/Users/keith/github/ohanaverse/local-ai-setup/bin/llm-isolate-provider`, `.../bin/llm-restore-providers`, and `/Users/keith/github/ohanaverse/local-ai-setup/CLAUDE.md` (Key Gotchas)
- Legacy benchmark docs + archived numbers: `/Users/keith/github/ohanaverse/local-ai-setup/benchmarks/README.md`, `.../qwen3.8-benchmark.md`, `.../ornith-1.5-benchmark.md`
- modelman source: `~/github/ohanaverse/local-ai-setup/modelman/src/modelman/benchmark/` (`cli.py` flags/pointer, `runner.py` target discovery, `results.py` markdown, `isolation.py` helper adapter)
- Launching `wt` agents against the benchmarked models: [06-wt-agents-and-models](06-wt-agents-and-models.md)
- Spend/usage data the proxy logs per benchmark request: [07-usage-and-spend](07-usage-and-spend.md)
