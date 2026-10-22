# llmbench internals

Module maps and mechanism detail moved out of `../CLAUDE.md`, which keeps the rules and gotchas. Paths are relative to `src/llmbench/`.

## Registry and state readers

- `registry.py` is a reader for the fields the benchmarks use (model `id`, `family`, `provider_id`, `model_name`, `location`, `fetch`, `draft`; provider `id`, `location`). It accepts unknown top-level keys, because it never writes. `registry_read_path()` reads the file `registry_path()` names and nowhere else (a dangling symlink there is refused, not treated as absent).
- `state.py` holds the four pointers (`last_run`, `last_run_dir`, `agent_last_run`, `eval_last_run`) in `latest.toml`, and reads them from nowhere else. A corrupt `latest.toml` reads as no pointers, and only those four keys with string values are kept.
- Renamed variables keep their old names as aliases, read second (`_env.py::env_first`): `LLMBENCH_WORKLOAD` (`MODELMAN_BENCHMARK_WORKLOAD`), `LLMBENCH_AGENT_DEBUG` (`MODELMAN_AGENT_DEBUG`). The `LLM_ISOLATE_*` names are unchanged.

## Provider lifecycle module map (`providers/lifecycle/`)

`orchestrate.py` holds the operations; `backends/` has one backend per provider, registered in `backends/__init__.py`'s `BACKENDS`, and `SUPPORTED_PROVIDER_IDS` holds the same ids (`ollama`, `omlx`, `omlx-6bit`, `mlx_lm_server`, `mtplx`). mtplx and mlx_lm_server subclass `PidfileTrackedBackend` (`backends/base.py`); `pidproc.py`'s `PidfileProcess` is the spawn/stop/log-tail primitive; `probe.py`/`launchd.py`/`binaries.py` are shared primitives; `envelope.py` holds the two types every lifecycle module returns or raises (`LifecycleResult`, the `--json` envelope, and `LifecycleError`); `cli.py` is `llmbench provider ...`. `local_process.py` (one level up) is the neutral home for process/probe types shared with `benchmark/isolation.py` (living under either package would make the other's import backwards). The mtplx backend loads the registry itself to resolve its model name.

## Benchmark subsystem module map (`benchmark/`)

- `benchmark/` — `cli.py` (`benchmark_app`: `list-workloads`, `run`, `show-results`), `isolation.py`, `runner.py`, `results.py`, `workloads/`; shared helpers `runmeta.py` (`git_sha()`), `errors.py` (`RunSavedButRestoreFailed`), `judge_core.py` (strict-JSON rubric scoring, retry-on-malformed, multi-sample median), and `_routes.py` (OpenRouter key resolution, then the LiteLLM LaunchAgent plist; LiteLLM apiKey/baseUrl from `~/.pi/agent/models.json`).
- `benchmark/agent/` — `suite.py`, `task.py`, `workspace.py`, `pidriver.py`, `gates.py` (nine-gate taxonomy), `judge.py`, `report.py`, `runner.py`, `cli.py`. `gates.toml`'s `tests_dir` must name a real subdirectory, never `"."`/`""`: `task.py::load_task` rejects them because gates 6/7 compare `Path.parts` tuples and an empty tuple is a prefix of every path.
- `benchmark/eval/` — `category.py`, `suite.py` (deliberately parallel to `agent/suite.py`, not shared), `judged_runner.py`, `evalplus_runner.py` (reports EvalPlus's "+"/hardened pass@1, not base), `runner.py` (incl. `rejudge_run` from persisted `response.txt`), `report.py`, `cli.py`.

## Test layout

Tests mirror modules: `tests/benchmark/` (incl. `test_routes.py`, `test_judge_core.py`), `tests/benchmark/agent/` (plus `fixtures/`), `tests/benchmark/eval/` (plus `test_rejudge.py`), `tests/providers/lifecycle/`, `tests/probe/` (its `conftest.py` holds a stdlib `http.server` fake OpenAI endpoint on a free localhost port; reuse it for any test that needs real HTTP, since `urlopen` is patched to raise). `tests/benchmark/agent/fixtures/tasks/` holds task bundles whose files are named `test_*.py` on purpose; `collect_ignore` keeps pytest out of them. CLI tests use `typer.testing.CliRunner` against `llmbench.main.app` (`["run", ...]`, `["provider", ...]`) or a sub-app directly (`agent_app`, `eval_app`).
