# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in `llmbench/`.

## Project overview

`llmbench` is a Python 3.13 CLI (Typer) that benchmarks local LLM models and owns the provider isolation a clean measurement needs. It was carved out of modelman (design: `../docs/superpowers/specs/2026-10-06-modelman-retirement-design.md`, Step 1) and imports nothing from it. It reads `registry.toml` and never writes it.

CLI (`src/llmbench/main.py`):

| Command | Purpose |
|---|---|
| `run`, `list-workloads`, `show-results` | Throughput benchmark of local backends (`benchmark/cli.py`) |
| `agent list-tasks\|list-suites\|run\|show\|judge` | Agentic coding benchmark (`benchmark/agent/cli.py`) |
| `eval list-categories\|list-items\|run\|show\|judge` | Cross-category capability benchmark (`benchmark/eval/cli.py`) |
| `provider isolate\|stop\|stop-all\|restore\|list` | Per-provider lifecycle (`providers/lifecycle/cli.py`) |

Run it from this directory (`uv run llmbench ...`) or from the repo root (`uv run --directory llmbench llmbench ...`). Either way the working directory is `llmbench/`, so relative path arguments (`--suite`, `--root`, `--results-dir`) resolve from here: `../benchmarks/suites/smoke.toml`. It is not installed globally.

User guides: `../docs/guides/05-benchmarks.md` (throughput and isolation), `../docs/guides/09-agent-benchmarks.md` (agent), `../docs/guides/11-capability-eval-benchmark.md` (eval).

## Monorepo context

- **modelman depends on llmbench** (an editable path dependency) until modelman is retired: `modelman start`/`stop` call `llmbench.benchmark.isolation` and `llmbench.providers.lifecycle` in-process, and `modelman benchmark` / `modelman provider` are these same Typer apps mounted under modelman's names. A change here runs in modelman too, which is why `modelman-ci` also triggers on `llmbench/**`. Never import modelman from llmbench.
- `modelman.local_process.ProcessResult` and `modelman.wt_bridge`'s `WtBridgeError`, `WtNotFoundError` and `WtBridgeTimeoutError` are re-exports of llmbench's classes. Keep those four names importable from `llmbench.local_process` and `llmbench.wt_bridge`.
- Five constants exist in both packages until modelman is retired: `DEFAULT_PROVIDER_IDS` (`registry.py`), `ENV_VAR_BY_PROVIDER` (`local_process.py`), `MTPLX_PORT` (`providers/mtplx.py`), the `omlx-6bit` alias (`providers/registry.py`) and `WARM_TIMEOUT` (`wt_bridge.py`). Change both copies together; `../modelman/tests/test_llmbench_reexports.py` fails if a pair differs.
- llmbench imports nothing from modelman, a function-local import included: `tests/test_main.py::test_llmbench_never_imports_modelman` parses every module for one.
- The bash scripts under `../benchmarks/` call `uv run --directory llmbench llmbench provider ...` (`../benchmarks/lib/benchmark-common.sh`).
- `wt` owns LiteLLM routing and the omlx key. llmbench's one call into wt is `wt warm <provider> <model>` (`wt_bridge.py`), which the omlx backend uses when a keyed omlx refuses the keyless warmup (#256).

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

- `registry.py` is a reader for the fields the benchmarks use (model `id`, `family`, `provider_id`, `model_name`, `location`, `fetch`, `draft`; provider `id`, `location`). It accepts unknown top-level keys, because it never writes. **`registry_read_path()` must resolve the same file as modelman's `_registry_read_path()`** (the pre-XDG fallback and the dangling-symlink refusal included) until modelman is retired; `../modelman/tests/test_registry_path_parity.py` pins it.
- `state.py` holds the four pointers (`last_run`, `last_run_dir`, `agent_last_run`, `eval_last_run`). While `latest.toml` does not exist it reads them from the `[benchmarks]` table of modelman.toml (`MODELMAN_STATE`, then `XDG_CONFIG_HOME`); the first save writes `latest.toml` and the old table is not read again. A corrupt `latest.toml` reads as no pointers, and from either file only those four keys with string values are kept.
- Renamed variables keep their old names as aliases, read second (`_env.py::env_first`): `LLMBENCH_WORKLOAD` (`MODELMAN_BENCHMARK_WORKLOAD`), `LLMBENCH_AGENT_DEBUG` (`MODELMAN_AGENT_DEBUG`). The `LLM_ISOLATE_*` names are unchanged.

## Architecture

### Provider lifecycle (`src/llmbench/providers/lifecycle/`)

Isolate/stop/stop-all/restore for local providers (ported from bash, issue #79). `orchestrate.py` holds the operations; `backends/` has one backend per provider, registered in `backends/__init__.py`'s `BACKENDS`, with `SUPPORTED_PROVIDER_IDS` (`ollama`, `omlx`, `omlx-6bit`, `mlx_lm_server`, `mtplx`) excluding the retired-only `llamacpp`. mtplx and mlx_lm_server subclass `PidfileTrackedBackend` (`backends/base.py`); `pidproc.py`'s `PidfileProcess` is the spawn/stop/log-tail primitive; `probe.py`/`launchd.py`/`binaries.py` are shared primitives; `envelope.py` holds the two types every lifecycle module returns or raises (`LifecycleResult`, the `--json` envelope, and `LifecycleError`); `cli.py` is `llmbench provider ...`. `src/llmbench/local_process.py` is the neutral home for process/probe types shared with `benchmark/isolation.py` (living under either package would make the other's import backwards).

- `stop_all()`'s `keep` takes a **provider id** (like `isolate()`/`stop()`), resolved internally to its `occupancy_key` — `--keep omlx-6bit` keeps both omlx variants — and an unknown id returns `ok=False` before any teardown.
- `isolate(..., solo=True)` (`provider isolate --solo`) restricts teardown to the backend's own occupant. The benchmarks never pass it; it is how an mlx_lm_server pairing is started beside other models.
- The mtplx backend loads the registry itself to resolve its model name, which is why the registry path rule above matters to `modelman start <mtplx model>`.
- Adding a backend: the `adding-a-benchmark-backend` skill (`../.claude/skills/adding-a-benchmark-backend/SKILL.md`). Artifacts and the retired llamacpp backend: `../docs/reference/provider-artifacts.md`.

### Benchmark subsystem (`src/llmbench/benchmark/`)

- `benchmark/` — `cli.py` (`benchmark_app`: `list-workloads`, `run`, `show-results`), `isolation.py`, `runner.py`, `results.py`, `workloads/`; shared helpers `runmeta.py` (`git_sha()`), `errors.py` (`RunSavedButRestoreFailed`), `judge_core.py` (strict-JSON rubric scoring, retry-on-malformed, multi-sample median), and `_routes.py` — the **single source** for OpenRouter key resolution (env, then the LiteLLM LaunchAgent plist) and LiteLLM apiKey/baseUrl from `~/.pi/agent/models.json`; fix a credential-format change there, once.
- `benchmark/agent/` — `suite.py`, `task.py`, `workspace.py`, `pidriver.py`, `gates.py` (nine-gate taxonomy), `judge.py`, `report.py`, `runner.py`, `cli.py`.
  - `gates.toml`'s `tests_dir` must name a real subdirectory, never `"."`/`""` — `task.py::load_task` rejects them because gates 6/7 compare `Path.parts` tuples and an empty tuple is a prefix of every path.
- `benchmark/eval/` — `category.py`, `suite.py` (deliberately parallel to `agent/suite.py`, not shared), `judged_runner.py`, `evalplus_runner.py` (reports EvalPlus's "+"/hardened pass@1, not base), `runner.py` (incl. `rejudge_run` from persisted `response.txt`), `report.py`, `cli.py`.
- **Repo-relative paths depend on this package's depth.** `benchmark/eval/cli.py` finds `../benchmarks/` through `Path(__file__).resolve().parents[5]`, and the tests reach `../benchmarks/tasks/` through `parents[4]`. Moving a module up or down a level breaks both.
- `main.py` mounts `benchmark_app` with no name, so its commands sit at the top level. Do not add `provider` to `benchmark_app` itself: it is mounted beside it.

## Testing patterns

- **`tests/conftest.py` points `HOME` at a scratch directory before llmbench is imported.** The package computes its machine-level paths from `Path.home()` at import time and binds several as default arguments, so this is the only redirect that reaches all of them: the LaunchAgent plist, `~/.pi/agent/models.json`, the results directory, the pointer file. `tests/test_conftest_guards.py` fails if a path escapes. Do not import llmbench from a pytest plugin or a `-p` module: conftest raises a usage error when it finds llmbench already imported.
- **Autouse guards** in the same file: `subprocess.run` is wrapped so `launchctl`, `omlx`, `mtplx`, `ollama`, `wt` and `mlx_lm.*` never execute (everything else, such as git, runs for real); `urlopen` raises; `os.kill` is a no-op; the registry, the pointer file and modelman.toml are redirected into `tmp_path`. `tests/providers/lifecycle/test_hermeticity.py` pins the allow-list (modelman keeps a copy, `../modelman/tests/test_hermeticity.py`, for the wrapper in its own conftest).
- **Backend tests patch the name as imported into the backend module** (`patch("llmbench.providers.lifecycle.backends.mtplx.subprocess.run")`, `...backends.mtplx.probe.wait_for_port_closed`, `...backends.mtplx._PROC`), never the origin module.
- CLI tests use `typer.testing.CliRunner` against `llmbench.main.app` (`["run", ...]`, `["provider", ...]`) or a sub-app directly (`agent_app`, `eval_app`).
- Tests mirror modules: `tests/benchmark/` (incl. `test_routes.py`, `test_judge_core.py`), `tests/benchmark/agent/` (plus `fixtures/`), `tests/benchmark/eval/` (plus `test_rejudge.py`), `tests/providers/lifecycle/`. `tests/benchmark/agent/fixtures/tasks/` holds task bundles whose files are named `test_*.py` on purpose; `collect_ignore` keeps pytest out of them.
- A failing assertion must never print a credential: compare a boolean (`found = key is not None`), not the value.
- **Ruff B905 is enforced:** `zip()` needs explicit `strict=`. CI runs `ruff format --check` on `src/` and `tests/`; run `make format`.
- **Run `make check` before calling any task done**: mypy flags things pytest cannot.
