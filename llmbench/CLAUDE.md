# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in `llmbench/`.

## Project overview

`llmbench` is a Python 3.13 CLI (Typer) that benchmarks local LLM models and owns the provider isolation a clean measurement needs. It reads `registry.toml` and never writes it. (Its origin in the retired modelman tool: `../docs/superpowers/specs/2026-10-06-modelman-retirement-design.md`.)

CLI (`src/llmbench/main.py`):

| Command | Purpose |
|---|---|
| `run`, `list-workloads`, `show-results` | Throughput benchmark of local backends (`benchmark/cli.py`) |
| `agent list-tasks\|list-suites\|run\|show\|judge` | Agentic coding benchmark (`benchmark/agent/cli.py`) |
| `eval list-categories\|list-items\|run\|show\|judge` | Cross-category capability benchmark (`benchmark/eval/cli.py`) |
| `provider isolate\|stop\|stop-all\|restore\|list` | Per-provider lifecycle (`providers/lifecycle/cli.py`) |
| `probe thinking` | Same prompts to several running OpenAI-compatible servers; compares reasoning length/opening and answer shape (`probe/thinking.py`, `probe/cli.py`). Optional `--presence-penalty`/`--frequency-penalty`/`--repetition-penalty` sent only when given. Manages no provider, reads no registry, writes only `--out` (default: new temp dir) |

Run it from this directory (`uv run llmbench ...`) or from the repo root (`uv run --directory llmbench llmbench ...`). Either way the working directory is `llmbench/`, so relative path arguments (`--suite`, `--root`, `--results-dir`) resolve from here: `../benchmarks/suites/smoke.toml`. It is not installed globally.

User guides: `../docs/guides/05-benchmarks.md` (throughput and isolation), `../docs/guides/09-agent-benchmarks.md` (agent), `../docs/guides/11-capability-eval-benchmark.md` (eval).

## Monorepo context

- The bash scripts under `../benchmarks/` call `uv run --directory llmbench llmbench provider ...` (`../benchmarks/lib/benchmark-common.sh`).
- `wt` owns LiteLLM routing and the omlx key. llmbench calls into wt only through `wt_bridge.py`, from the omlx backend: `wt warm <provider> <model>` when a keyed omlx refuses the keyless warmup (#256), and `wt served omlx` plus `wt stop omlx/<name>` to unload the other models omlx holds before an isolation warms its own (a keyed omlx refuses keyless management calls).

## Common development commands

`uv` for packaging; Python `==3.13.*`. Run `make help` for targets.

- `make install` (`uv sync`); `uv sync --extra eval` adds EvalPlus for the `eval` benchmark's `coding` category.
- `make test` runs the suite with coverage and fails under the floor in `pyproject.toml` (`fail_under`). Single test: `uv run pytest tests/path/to/test.py::test_name`.
- `make check` = lint + `ruff format --check` + mypy; `make all` = format + test + check.

The suite runs in about 15 seconds and is safe to run while agents use the local providers (see "Testing patterns").

## Files and environment

| Thing | Where | Override |
|---|---|---|
| Registry (read-only) | `~/.config/local-ai/registry.toml` | `WT_REGISTRY`, then `MODELMAN_REGISTRY`, then `XDG_CONFIG_HOME` |
| Results | `~/.config/local-ai/benchmarks/<run-id>/` | `--results-dir` |
| Latest-run pointers | `~/.config/local-ai/benchmarks/latest.toml` | `LLMBENCH_LATEST` |
| OpenRouter key | `OPENROUTER_API_KEY`, else the LiteLLM LaunchAgent plist | |
| LiteLLM url and key | `~/.pi/agent/models.json` | |

- `registry.py` only reads (accepts unknown top-level keys; refuses a dangling registry symlink). `state.py` is the only reader of the four pointers in `latest.toml`; a corrupt file reads as no pointers.
- Renamed variables keep old aliases, read second (`_env.py::env_first`): `LLMBENCH_WORKLOAD` (`MODELMAN_BENCHMARK_WORKLOAD`), `LLMBENCH_AGENT_DEBUG` (`MODELMAN_AGENT_DEBUG`). `LLM_ISOLATE_*` unchanged. Details: `docs/internals.md#registry-and-state-readers`.

## Architecture

Module maps (every file and what it owns): `docs/internals.md`.

### Provider lifecycle (`src/llmbench/providers/lifecycle/`)

Isolate/stop/stop-all/restore for local providers (ported from bash, issue #79): `orchestrate.py` operations, one backend per provider in `backends/` (registered in `BACKENDS`; `SUPPORTED_PROVIDER_IDS` holds the same ids), `cli.py` is `llmbench provider ...`. Module map: `docs/internals.md#provider-lifecycle-module-map-providerslifecycle`.

- `stop_all()`'s `keep` takes a **provider id** (like `isolate()`/`stop()`), resolved internally to its `occupancy_key` — `--keep omlx-6bit` keeps both omlx variants — and an unknown id returns `ok=False` before any teardown.
- `isolate(..., solo=True)` (`provider isolate --solo`) restricts teardown to the backend's own occupant. The benchmarks never pass it; it is how an mlx_lm_server pairing is started beside other models.
- `src/llmbench/local_process.py` is the neutral home for process/probe types shared with `benchmark/isolation.py`; do not move them under either package (the other's import would run backwards).
- Adding a backend: the `adding-a-benchmark-backend` skill (`../.claude/skills/adding-a-benchmark-backend/SKILL.md`). Artifacts and the retired llamacpp provider: `../docs/reference/provider-artifacts.md`.

### Benchmark subsystem (`src/llmbench/benchmark/`)

Throughput (`benchmark/`), agent (`benchmark/agent/`) and eval (`benchmark/eval/`) subpackages; file lists: `docs/internals.md#benchmark-subsystem-module-map-benchmark`.

- `benchmark/_routes.py` is the **single source** for OpenRouter key resolution and LiteLLM apiKey/baseUrl; fix a credential-format change there, once.
- `gates.toml`'s `tests_dir` must name a real subdirectory, never `"."`/`""` (`task.py::load_task` rejects them: gates 6/7 compare `Path.parts` tuples and an empty tuple is a prefix of every path).
- `eval/suite.py` is deliberately parallel to `agent/suite.py`, not shared. `evalplus_runner.py` reports EvalPlus's "+"/hardened pass@1, not base.
- **Repo-relative paths depend on this package's depth.** `benchmark/eval/cli.py` finds `../benchmarks/` through `Path(__file__).resolve().parents[5]`, and the tests reach `../benchmarks/tasks/` through `parents[4]`. Moving a module up or down a level breaks both.
- `main.py` mounts `benchmark_app` with no name, so its commands sit at the top level. Do not add `provider` to `benchmark_app` itself: it is mounted beside it.

## Testing patterns

- **`tests/conftest.py` points `HOME` at a scratch directory before llmbench is imported.** The package computes its machine-level paths from `Path.home()` at import time and binds several as default arguments, so this is the only redirect that reaches all of them: the LaunchAgent plist, `~/.pi/agent/models.json`, the results directory, the pointer file. `tests/test_conftest_guards.py` fails if a path escapes. Do not import llmbench from a pytest plugin or a `-p` module: conftest raises a usage error when it finds llmbench already imported.
- **Autouse guards** in the same file: `subprocess.run` is wrapped so `launchctl`, `omlx`, `mtplx`, `ollama`, `wt` and `mlx_lm.*` never execute (everything else, such as git, runs for real); `urlopen` raises; `os.kill` is a no-op; the registry and the pointer file are redirected into `tmp_path`. `tests/providers/lifecycle/test_hermeticity.py` pins the allow-list.
- **Backend tests patch the name as imported into the backend module** (`patch("llmbench.providers.lifecycle.backends.mtplx.subprocess.run")`, `...backends.mtplx.probe.wait_for_port_closed`, `...backends.mtplx._PROC`), never the origin module.
- Tests mirror modules; CLI tests use `typer.testing.CliRunner`. `tests/probe/conftest.py` has a stdlib fake OpenAI endpoint on localhost; reuse it for any real-HTTP test (`urlopen` is patched to raise). `tests/benchmark/agent/fixtures/tasks/` has `test_*.py` files on purpose; `collect_ignore` skips them. Layout: `docs/internals.md#test-layout`.
- A failing assertion must never print a credential: compare a boolean (`found = key is not None`), not the value.
- **Ruff B905 is enforced:** `zip()` needs explicit `strict=`. CI runs `ruff format --check` on `src/` and `tests/`; run `make format`.
- **Run `make check` before calling any task done**: mypy flags things pytest cannot.
