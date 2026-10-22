# llmbench Carve-out (Modelman Retirement, Step 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The three benchmarks and provider isolation live in a standalone Python package, `llmbench`, that imports nothing from modelman, while every `modelman` command keeps working.

**Architecture:** `git mv` moves `modelman/src/modelman/benchmark/` and `modelman/src/modelman/providers/lifecycle/` (and their tests) into a new uv package at `llmbench/`, keeping the sub-package shape so relative imports and repo-relative path depths do not change. Eight small support modules (the spec's five, plus `state.py`, `providers/registry.py` and `_env.py`) replace what the moved code imported from the rest of modelman. modelman takes an editable path dependency on llmbench, imports the lifecycle from it, and keeps `modelman benchmark` and `modelman provider` mounted from it until Step 6.

**Tech Stack:** Python 3.13, uv, Typer, tomli-w, requests, pytest + pytest-cov, ruff, mypy, hatchling. No new third-party dependency.

**Spec:** [docs/superpowers/specs/2026-10-06-modelman-retirement-design.md](../specs/2026-10-06-modelman-retirement-design.md), section "Step 1 — carve out llmbench". This plan covers Step 1 only, as the spec's two PR slices: **PR 1 = Tasks 1 to 7** (the move, support modules, rewiring, CI) and **PR 2 = Tasks 8 to 10** (callers and docs). Step 0 has its own plan and is independent of this one.

## Global Constraints

- Work on a feature branch off `main`: `feat/llmbench-carve-out` for PR 1, and `feat/llmbench-callers-docs` (off `main`, after PR 1 merges) for PR 2. **Do not push and do not open a PR without the owner's OK.** Ask at the end of Task 7 and of Task 10.
- `llmbench` imports nothing from modelman. modelman may import llmbench.
- Keep the sub-package shape: `llmbench.benchmark.*` and `llmbench.providers.lifecycle.*`. Do not move a module up or down a level (`benchmark/eval/cli.py` reaches `benchmarks/` through `parents[5]`, the tests through `parents[4]`).
- Commands: `llmbench run|list-workloads|show-results`, `llmbench agent …`, `llmbench eval …`, `llmbench provider isolate|stop|stop-all|restore|list`. Flags, exit codes and the provider `--json` envelope are unchanged. `--solo` is kept.
- `modelman benchmark` and `modelman provider` stay mounted from llmbench until Step 6; modelman's `eval` extra forwards to `llmbench[eval]`.
- modelman is frozen to bug fixes. Change only the modelman files this plan names.
- The registry reader is read-only, tolerates unknown top-level keys, and resolves the same file as modelman's loader until Step 6. It does **not** read `WT_REGISTRY`: that name arrives in all three readers in Step 2.
- Latest-run pointers live in `~/.config/local-ai/benchmarks/latest.toml` (override: `LLMBENCH_LATEST`), with a fallback read of modelman.toml's `[benchmarks]` keys while `latest.toml` is absent.
- Env: `LLMBENCH_WORKLOAD` and `LLMBENCH_AGENT_DEBUG`, with `MODELMAN_BENCHMARK_WORKLOAD` and `MODELMAN_AGENT_DEBUG` kept as aliases. The `LLM_ISOLATE_*` names are unchanged.
- Tests never touch the developer's machine: no real `~/.config`, no live provider, no real `wt`, `ollama`, `omlx`, `mtplx`, `launchctl` or `mlx_lm.*` process. **A failing assertion must never print a credential** (compare a boolean, not the value).
- Never run a benchmark, `provider isolate`, `provider stop` or `provider restore` against the live machine while executing this plan. `provider list`, `list-workloads` and `--help` are the only commands safe to run for a smoke check.
- Python `==3.13.*`. Before each commit: `make check` passes in every package the task touched.
- `llmbench/uv.lock` keeps modelman's versions of every shared package (Task 1 Step 7 seeds it from `modelman/uv.lock`); the expected counts in this plan were measured on them.
- `docs/guides/` never embed live model state.
- `make test-all` passes at the end of each PR slice, `git status` is clean after each commit (it is clean before the first one too: Task 1 Step 1), and nothing is created under `~/.config`.
- Commit messages use the repo's conventional style (`feat(llmbench): …`, `refactor(modelman): …`, `docs: …`).
- Run commands from the repository root unless a step starts with `cd`.

## Review Focus

1. **Pointers recorded under modelman.** A user who ran benchmarks through modelman has `agent_last_run` and friends in modelman.toml. Expected: the first `llmbench agent show --latest` finds that run. Pinned in Task 4 (`test_first_read_falls_back_to_modelmans_benchmarks_table`, `test_the_fallback_is_read_once`).
2. **A dangling symlink at the pre-XDG path while `XDG_CONFIG_HOME` is set and holds no registry.** Expected: refused with the link named, as modelman does, not "Registry file not found". Pinned in Task 3 (`test_read_path_refuses_a_dangling_legacy_symlink`).
3. **A wrapper script that exports the old variable name.** Expected: `MODELMAN_BENCHMARK_WORKLOAD` and `MODELMAN_AGENT_DEBUG` still take effect, and the new name wins when both are set. Pinned in Task 5 (`test_run_takes_the_workload_from_the_environment`, `test_row_dir_has_a_metrics_log_under_debug`).
4. **A torn or hand-broken `latest.toml`.** Expected: the pointer write after a run that took hours does not crash; the file is replaced. Pinned in Task 4 (`test_a_corrupt_latest_toml_reads_as_no_pointers_and_is_replaced`).
5. **`XDG_CONFIG_HOME` set.** Expected: `latest.toml` stays beside the results in `~/.config/local-ai/benchmarks` (the results directory ignores XDG too), while the one-time fallback still finds modelman.toml under XDG. Pinned in Task 4 (`test_latest_path_ignores_xdg_config_home`, `test_the_fallback_finds_modelman_toml_where_modelman_kept_it`).

## File Structure

| File | Change | Responsibility |
|---|---|---|
| `llmbench/pyproject.toml`, `Makefile`, `.gitignore`, `README.md`, `uv.lock` | create | The package, its pytest/coverage/ruff/mypy config and targets |
| `llmbench/src/llmbench/benchmark/**` | move from `modelman/src/modelman/benchmark/**` | The three benchmarks (unchanged code, renamed imports) |
| `llmbench/src/llmbench/providers/lifecycle/**` | move from `modelman/src/modelman/providers/lifecycle/**` | Provider isolation (unchanged code, renamed imports) |
| `llmbench/src/llmbench/main.py` | create | Root Typer app: benchmark commands at the top level, `provider` mounted |
| `llmbench/src/llmbench/registry.py` | create | Read-only registry reader, and where it looks |
| `llmbench/src/llmbench/state.py` | create | Latest-run pointers in `latest.toml`, with the modelman.toml fallback |
| `llmbench/src/llmbench/_toml_io.py` | create | Atomic TOML write |
| `llmbench/src/llmbench/local_process.py` | create | `ProcessResult`, `ENV_VAR_BY_PROVIDER`, `http_models_ids` |
| `llmbench/src/llmbench/wt_bridge.py` | create | `warm`, `ensure_wt`, `_msg`, the three exception classes, `WARM_TIMEOUT` |
| `llmbench/src/llmbench/_env.py` | create | `env_first`: a renamed variable and its alias |
| `llmbench/src/llmbench/providers/mtplx.py`, `providers/registry.py` | create | The mtplx port constants; the `omlx-6bit` alias |
| `llmbench/tests/benchmark/**`, `llmbench/tests/providers/lifecycle/**` | move from `modelman/tests/…` | The 575 moved tests |
| `llmbench/tests/conftest.py` | create | Scratch HOME and the autouse guards |
| `llmbench/tests/test_{main,registry,state,wt_bridge,toml_io,conftest_guards}.py` | create | Tests of the new modules and the guards; `test_main.py` also fails on any `import modelman` |
| `docs/contracts/registry.sample.toml` | modify (header comment) | Names llmbench's test among the fixture's readers |
| `modelman/pyproject.toml`, `modelman/uv.lock` | modify | Path dependency on llmbench; `eval` extra forwards |
| `modelman/src/modelman/{local_control,main}.py` | modify | Import the lifecycle and the two Typer apps from llmbench |
| `modelman/src/modelman/{local_process,wt_bridge}.py` | modify | Re-export `ProcessResult` and the bridge exceptions |
| `modelman/tests/{conftest,test_local_control}.py`, `modelman/tests/commands/test_local_control.py` | modify | Patch targets under their new names |
| `modelman/tests/test_registry_path_parity.py`, `test_llmbench_reexports.py`, `commands/test_llmbench_mounts.py` | create | The three seams between the packages; deleted with modelman |
| `modelman/tests/test_hermeticity.py` | create (a copy of the moved file) | Pins modelman's own subprocess allow-list, which still guards its start/stop tests; deleted with modelman |
| `.github/workflows/llmbench-ci.yml` | create | `make check && make test` on Linux, then `check-config-dirs-untouched` |
| `.github/workflows/modelman-ci.yml`, `Makefile`, `bin/check-config-dirs-untouched` | modify | modelman-ci also runs on `llmbench/**`; `install` and `test-all` gain llmbench; the script's header names its third caller |
| `benchmarks/lib/benchmark-common.sh`, `benchmarks/qwen3.8-benchmark`, `benchmarks/ornith-1.5-benchmark` | modify (PR 2) | Call `llmbench provider` |
| `llmbench/CLAUDE.md` | create (PR 2) | Package context, taking modelman's two sections |
| guides, reference, root, modelman and wt docs, two skills | modify (PR 2) | The `llmbench` spellings, and llmbench in each component inventory |

## Decisions this plan makes

The spec leaves these open; each was rehearsed in a scratch clone, and the counts in this plan come from that rehearsal.

- **`state.py` keeps `load_state` / `save_state` and the `extra["benchmarks"]` shape.** The three moved CLIs and their tests then need no edit. Only the file behind it changes.
- **The plist and `models.json` redirect is a scratch `HOME`,** set in `tests/conftest.py` before llmbench is imported. The paths are computed at import and bound as default arguments, so a per-test patch cannot reach them all.
- **`make test` runs with coverage and `fail_under = 90`.** Measured: 91.47% after Task 1, 92.34% after Task 5. modelman's own `make test` runs without `--cov`, so its 80 was never enforced; here the floor is a gate.
- **The path-parity test lives in `modelman/tests/`,** the only place that may import both packages. It is deleted with modelman.
- **Three support modules beyond the spec's list:** `state.py` (the pointer file the spec's "Latest-run pointers" paragraph needs), `providers/registry.py` (the `omlx-6bit` alias `benchmark/runner.py` resolves) and `_env.py` (the alias lookup). `ProviderEntry` keeps a `name` field the spec's reader does not list, because the moved tests construct it with one. An empty `LLMBENCH_WORKLOAD` or `MODELMAN_BENCHMARK_WORKLOAD` now counts as unset (it used to select a workload named "").
- **`llmbench/uv.lock` starts as a copy of `modelman/uv.lock`,** so both packages lint, type-check and test the moved code with one resolved toolchain (ruff, mypy, pytest, coverage, Typer). A fresh resolution picks newer tools the moved code never passed under.
- **The constants that exist in both packages until Step 6** (`DEFAULT_PROVIDER_IDS`, `ENV_VAR_BY_PROVIDER`, `MTPLX_PORT`, the `omlx-6bit` alias, `WARM_TIMEOUT`) **are held together by a modelman test** (Task 6), and modelman keeps a copy of the hermeticity test for its own subprocess wrapper (Task 1).
- **PR 2 edits every non-historical file that names `modelman benchmark`, `modelman provider` or a moved path,** not only guides 05, 09 and 11: after the move those paths do not exist. modelman's own README keeps the two commands, because they still work there.
- **`MODELMAN_PROVIDER` becomes `LLMBENCH_PROVIDER`** in `benchmark-common.sh`, which also touches the one line that uses it in each of `qwen3.8-benchmark` and `ornith-1.5-benchmark`.

---

## PR 1: the move, support modules, rewiring, CI

### Task 1: Move the two trees into a new `llmbench` package

The move is mechanical: the gate is the moved test suite, unchanged in number. The one new behaviour in this task is the command tree (`llmbench run`, not `llmbench benchmark run`), which is written test-first in Steps 8 to 10.

**Files:**
- Move: `modelman/src/modelman/benchmark/` → `llmbench/src/llmbench/benchmark/`; `modelman/src/modelman/providers/lifecycle/` → `llmbench/src/llmbench/providers/lifecycle/`; `modelman/tests/benchmark/` → `llmbench/tests/benchmark/`; `modelman/tests/providers/` → `llmbench/tests/providers/`
- Create: `llmbench/pyproject.toml`, `llmbench/Makefile`, `llmbench/.gitignore`, `llmbench/README.md`, `llmbench/uv.lock` (generated), `llmbench/src/llmbench/{__init__,main,registry,state,_toml_io,local_process,wt_bridge}.py`, `llmbench/src/llmbench/providers/{__init__,mtplx,registry}.py`, `llmbench/tests/{__init__,conftest,test_main,test_wt_bridge,test_toml_io}.py`, `modelman/tests/commands/test_llmbench_mounts.py`, `modelman/tests/test_hermeticity.py` (a copy)
- Modify: `modelman/pyproject.toml:10-26`, `modelman/uv.lock` (generated), `modelman/src/modelman/local_control.py:7-9,46-59,1478`, `modelman/src/modelman/main.py:16,34`, `modelman/src/modelman/local_process.py:3-4` (comment), `modelman/src/modelman/providers/mlx_lm_server.py:32` (comment), `modelman/tests/conftest.py:130,261,309,317,321`, `modelman/tests/test_local_control.py:13,380,904,921,2008`, `modelman/tests/commands/test_local_control.py:51`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `llmbench.main.app` (Typer): `run`, `list-workloads`, `show-results`, `agent`, `eval`, `provider`
  - `llmbench.benchmark.cli.benchmark_app`, `llmbench.providers.lifecycle.cli.provider_app` (mounted by modelman)
  - `llmbench.registry`: `DEFAULT_PROVIDER_IDS`, `RegistryError`, `ProviderEntry(id, name="", location=None)`, `Fetch(repo=None, local_path=None)`, `DraftSpec(repo=None, local_path=None)`, `ModelEntry(id, family, provider_id, model_name, location=None, fetch=None, draft=None)`, `Registry(providers=[], models=[])` with `.provider(id)` / `.model(id)`, `is_model_local(location, provider_id, registry, *, missing_provider_is_local=False) -> bool`, `registry_path() -> Path`, `load_registry(path: Path | None = None) -> Registry`
  - `llmbench.state`: `StateStore(extra: dict)`, `latest_path() -> Path`, `load_state(path=None) -> StateStore`, `save_state(store, path=None) -> None`
  - `llmbench.local_process`: `ProcessResult(provider, model, direct_url, ok, error)`, `ENV_VAR_BY_PROVIDER`, `http_models_ids(url, timeout=2.0) -> list[str]`
  - `llmbench.wt_bridge`: `WtBridgeError`, `WtNotFoundError`, `WtBridgeTimeoutError`, `ensure_wt()`, `_msg(proc, fallback) -> str`, `WARM_TIMEOUT`, `warm(provider, model, timeout=WARM_TIMEOUT) -> None`
  - `llmbench._toml_io`: `atomic_write(path, write_fn, *, binary=False)`, `atomic_write_toml(payload, path)`
  - `llmbench.providers.mtplx`: `MTPLX_PORT`, `MTPLX_BASE`, `MTPLX_V1_BASE`; `llmbench.providers.registry.ProviderRegistry.resolve(name) -> str`

- [ ] **Step 1: Branch and record the baseline**

Precondition: the working tree has no untracked or modified file.

```bash
git status --short
```

Expected: no output. Every `git status --short` expectation below (a count, or "prints nothing") assumes this. The two plan documents under `docs/superpowers/plans/` were written on `docs/modelman-retirement-design`; if they show as `??`, commit them on that branch first. They are not part of this work.

```bash
git switch -c feat/llmbench-carve-out main
```

If the spec's branch (`docs/modelman-retirement-design`) has not merged into `main` yet, branch from it instead; the code is the same. If you are already in a worktree created by superpowers:using-git-worktrees, skip the `git switch` and stay on the worktree's branch (created from `main`, or from `docs/modelman-retirement-design` if that has not merged).

```bash
cd modelman && uv sync
uv run pytest tests/benchmark tests/providers --collect-only -q -o addopts="" | tail -1
uv run pytest --collect-only -q -o addopts="" | tail -1
```

Expected: `575 tests collected in …` and `1788 tests collected in …`. These are the two numbers the move must preserve: 575 tests leave, 1213 stay.

- [ ] **Step 2: Move the trees**

```bash
mkdir -p llmbench/src/llmbench/providers llmbench/tests/providers
git mv modelman/src/modelman/benchmark llmbench/src/llmbench/benchmark
git mv modelman/src/modelman/providers/lifecycle llmbench/src/llmbench/providers/lifecycle
git mv modelman/tests/benchmark llmbench/tests/benchmark
git mv modelman/tests/providers/lifecycle llmbench/tests/providers/lifecycle
git mv modelman/tests/providers/__init__.py llmbench/tests/providers/__init__.py
rm -rf modelman/tests/providers
find llmbench -name __pycache__ -type d -prune -exec rm -rf {} +
git status --short | awk '{print $1}' | sort | uniq -c
```

Expected: ` 119 R`. (`modelman/tests/providers` holds only a `__pycache__` by now; the `find` removes the stale bytecode that travelled with the moved directories.)

- [ ] **Step 3: Rename the package inside the moved files**

One ordered substitution over every moved file that mentions modelman:

```bash
git grep -lIz -e modelman -e MODELMAN_STATE -- llmbench | xargs -0 perl -pi -e '
  s/--directory modelman modelman/--directory llmbench llmbench/g;
  s/`modelman provider \.\.\.`, `modelman benchmark`, and `local_control\.py`/`llmbench provider ...`, the benchmarks, and modelman\x27s `local_control.py`/g;
  s/modelman benchmark/llmbench/g;
  s/modelman-evalplus-/llmbench-evalplus-/g;
  s/MODELMAN_STATE", str\(tmp_path \/ "modelman\.toml"\)/LLMBENCH_LATEST", str(tmp_path \/ "latest.toml")/g;
  s/\bmodelman(?!\x27?s?\s*$| (?:start|stop)\b|\.toml\b|\.usage\b|\x27s same-provider|\x27s \x60local_control| may have loaded| already has loaded| would have to know| having to track)/llmbench/g;
'
perl -pi -e 's/\["benchmark", "/["/g' llmbench/tests/benchmark/test_cli.py llmbench/tests/benchmark/test_state_pointer.py
```

What each rule does, in order:

| Rule | Before | After |
|---|---|---|
| 1 | `uv run --directory modelman modelman provider ...` | `uv run --directory llmbench llmbench provider ...` |
| 2 | reached from `` `modelman provider ...`, `modelman benchmark`, and `local_control.py` `` | reached from `` `llmbench provider ...`, the benchmarks, and modelman's `local_control.py` `` |
| 3 | `` `modelman benchmark eval` ``, `modelman benchmark` | `` `llmbench eval` ``, `llmbench` |
| 4 | `TemporaryDirectory(prefix="modelman-evalplus-")` | `TemporaryDirectory(prefix="llmbench-evalplus-")` |
| 5 | `monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))` | `monkeypatch.setenv("LLMBENCH_LATEST", str(tmp_path / "latest.toml"))` |
| 6 | `from modelman.benchmark.errors import …`, `patch("modelman.providers.lifecycle.stop")`, `from modelman import wt_bridge`, `modelman/providers/lifecycle/backends/__init__.py`, `[modelman provider restore]` | the same with `llmbench` |
| last line | `runner.invoke(app, ["benchmark", "run", …])` | `runner.invoke(app, ["run", …])` |

Rule 6 leaves `modelman` alone where the text names modelman the tool: `modelman start`, `modelman stop`, `modelman.toml`, `modelman.usage`, "modelman's same-provider-only", "modelman's `local_control.py`" (the text rule 2 has just written into three backend docstrings; `local_control.py` stays in modelman), "modelman may have loaded", "modelman already has loaded", "modelman would have to know", "modelman having to track", and a `modelman` that ends its line.

Check the result:

```bash
git grep -nE '(from|import) modelman|"modelman\.|MODELMAN_STATE' -- llmbench
git grep -nE 'modelman|MODELMAN' -- llmbench | wc -l
git status --short | awk '{print $1}' | sort | uniq -c
```

Expected: the first command prints nothing; the second prints `29` (prose that names modelman's own commands, the three "modelman's `local_control.py`" docstring lines, the comment naming `MODELMAN_LITELLM_RESTART_CMD`, and the two variable names Task 5 replaces: `MODELMAN_BENCHMARK_WORKLOAD` in `benchmark/cli.py:68` and `MODELMAN_AGENT_DEBUG` in `benchmark/agent/runner.py:194` and `tests/benchmark/agent/test_runner.py:728`); the third prints `  39 R` and `  80 RM`.

- [ ] **Step 4: Write the package files**

`llmbench/pyproject.toml`. The pytest and coverage sections are written for this package, not copied: no `asyncio_mode` (nothing here is async, and pytest-asyncio is not a dependency), coverage source `src/llmbench`, and `fail_under = 90`. The floor comes from a measurement: this task's suite covers 91.47% and the suite at the end of Task 5 covers 92.34%, so 90 leaves two points of slack. `make test` enforces it (see the Makefile below).

```toml
[project]
name = "llmbench"
version = "0.1.0"
description = "Benchmarks for local LLM models, with the provider isolation they need"
readme = "README.md"
requires-python = "==3.13.*"
authors = [
    { name = "Keith Hartmann", email = "keith.hartmann@nytimes.com" }
]
dependencies = [
    "typer>=0.15.3",
    "tomli-w>=1.2.0",
    "requests>=2.32.0",
]

[project.optional-dependencies]
eval = [
    "evalplus>=0.3.1",
]

[project.scripts]
llmbench = "llmbench.main:app"

[build-system]
requires = ["hatchling"]
build-backend = "hatchling.build"

[tool.hatch.build.targets.wheel]
packages = ["src/llmbench"]

[dependency-groups]
dev = [
    "pytest>=8.0.0",
    "pytest-cov>=5.0.0",
    "mypy>=1.10.0",
    "ruff>=0.6.0",
]

[tool.pytest.ini_options]
testpaths = ["tests"]
addopts = "-v --tb=short"

[tool.coverage.run]
source = ["src/llmbench"]
omit = ["*/tests/*", "*/__init__.py"]

[tool.coverage.report]
fail_under = 90
exclude_lines = [
    "pragma: no cover",
    "raise NotImplementedError",
    "if __name__ == .__main__.:",
]

[tool.ruff]
line-length = 100
target-version = "py313"
extend-exclude = [".venv", "build", "dist", ".worktrees"]

[tool.ruff.lint]
select = [
    "E",   # pycodestyle errors
    "F",   # pyflakes
    "W",   # pycodestyle warnings
    "I",   # isort
    "UP",  # pyupgrade
    "B",   # flake8-bugbear
    "SIM", # flake8-simplify
    "C4",  # flake8-comprehensions
]
ignore = [
    "E501",   # line too long (ruff format handles this)
]

[tool.ruff.lint.isort]
known-first-party = ["llmbench"]

[tool.ruff.format]
quote-style = "double"
indent-style = "space"
line-ending = "lf"

[tool.mypy]
python_version = "3.13"
ignore_missing_imports = true
```

`llmbench/Makefile`. modelman's, with one change: `test` runs with `--cov`, so the floor above is a gate, not decoration. A focused `uv run pytest tests/x.py` is not subject to it.

```make
SHELL := /bin/bash
.PHONY: help install test lint format format-check typecheck check all clean

help:  ## Show this help.
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

install:  ## Install dev dependencies into the venv.
	uv sync

test:  ## Run the test suite; fails under the coverage floor in pyproject.toml.
	uv run pytest --cov

lint:  ## Lint with ruff (no fixes).
	uv run ruff check src/ tests/

format-check:  ## Verify ruff formatting (no changes).
	uv run ruff format --check src/ tests/

format:  ## Auto-format with ruff.
	uv run ruff format src/ tests/
	uv run ruff check --fix src/ tests/

typecheck:  ## Run mypy on the package.
	uv run mypy src/

check: lint format-check typecheck  ## Lint + format check + typecheck (no auto-fixes).

all: format test check  ## Format, run tests, then lint + format check + typecheck.

clean:  ## Remove caches and build artifacts.
	rm -rf .pytest_cache .mypy_cache .ruff_cache htmlcov .coverage
	find . -type d -name __pycache__ -exec rm -rf {} +
```

`llmbench/.gitignore` (the same entries as `modelman/.gitignore`):

```text
__pycache__/
*.py[cod]
*.egg-info/
.venv/
dist/
build/
.pytest_cache/
.mypy_cache/
.coverage
htmlcov/
.superpowers/
.worktrees/
# Machine-local Claude context (personal only)
.claude.local.md
```

`llmbench/README.md` (`pyproject.toml` names it; hatchling refuses to build without it):

````markdown
# llmbench

Benchmarks for local LLM models, and the provider isolation they need to measure cleanly.

```bash
uv run --directory llmbench llmbench --help
```

User guides: [throughput benchmarks](../docs/guides/05-benchmarks.md), [agent benchmarks](../docs/guides/09-agent-benchmarks.md), [capability eval](../docs/guides/11-capability-eval-benchmark.md).
````

`llmbench/src/llmbench/__init__.py`:

```python
"""llmbench: benchmarks for local LLM models, and the provider isolation they need."""
```

Two empty files:

```bash
: > llmbench/src/llmbench/providers/__init__.py
: > llmbench/tests/__init__.py
```

- [ ] **Step 5a: Write the four leaf support modules**

Steps 5a to 5d write the seven modules that replace what the moved code imported from the rest of modelman (the eighth, `_env.py`, arrives in Task 5). Each sits at the same relative position as its modelman original, so the moved code's relative imports (`from ....registry import load_registry`, `from ...local_process import …`, `from ...mtplx import …`, `from .... import wt_bridge`) resolve unchanged.

`llmbench/src/llmbench/_toml_io.py` (modelman's `atomic_write` and `atomic_write_toml`, without the helpers only a writer of registry.toml needs):

```python
"""Atomic TOML write (temp file + rename).

llmbench writes a run's `run.toml` and the latest-run pointers. A crash or
Ctrl-C mid-write must leave the previous file intact, never a truncated one.
"""

from __future__ import annotations

import contextlib
import os
import tempfile
from collections.abc import Callable
from pathlib import Path
from typing import IO, Any

import tomli_w


def atomic_write(path: Path, write_fn: Callable[[IO[Any]], None], *, binary: bool = False) -> None:
    """Write to `path` atomically via temp file + rename.

    `write_fn` receives the open temp file handle and writes the content. On
    any failure the temp file is removed and `path` is left untouched.
    """
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(dir=path.parent, prefix=f".{path.name}.", suffix=".tmp")
    try:
        with os.fdopen(fd, "wb" if binary else "w") as f:
            write_fn(f)
        os.replace(tmp_name, path)
    except BaseException:
        with contextlib.suppress(OSError):
            os.unlink(tmp_name)
        raise


def atomic_write_toml(payload: dict[str, Any], path: Path) -> None:
    """Write `payload` to `path` as TOML via temp file + rename."""
    atomic_write(path, lambda f: tomli_w.dump(payload, f), binary=True)
```

`llmbench/src/llmbench/local_process.py` (the three names the moved code uses; modelman's other HTTP helpers stay in modelman):

```python
"""Shared local-provider process types and HTTP probe helper.

Neutral home for code that both `llmbench.benchmark.isolation` (isolating a
provider for a benchmark run) and `llmbench.providers.lifecycle` (the
lifecycle itself) need, so neither imports the other for it.
"""

from __future__ import annotations

import json
import urllib.error
import urllib.request
from dataclasses import dataclass


@dataclass
class ProcessResult:
    provider: str
    model: str
    direct_url: str
    ok: bool
    error: str | None


# Provider ids whose lifecycle backend resolves its model from a single
# LLM_ISOLATE_*_MODEL env var (backends/base.py's _resolve_model).
# mlx_lm_server is deliberately absent: it takes target+draft as positional args.
ENV_VAR_BY_PROVIDER = {
    "ollama": "LLM_ISOLATE_OLLAMA_MODEL",
    "omlx": "LLM_ISOLATE_OMLX_4BIT_MODEL",
    "omlx-6bit": "LLM_ISOLATE_OMLX_6BIT_MODEL",
}


def http_models_ids(url: str, timeout: float = 2.0) -> list[str]:
    """Model ids from an OpenAI-compatible /v1/models response, or [] on any
    error (connection refused, timeout, non-JSON body)."""
    try:
        with urllib.request.urlopen(url, timeout=timeout) as resp:  # noqa: S310 — localhost probe
            data = json.loads(resp.read().decode())
    except (OSError, ValueError):
        return []
    items = data.get("data") if isinstance(data, dict) else None
    if not isinstance(items, list):
        return []
    return [
        item["id"] for item in items if isinstance(item, dict) and isinstance(item.get("id"), str)
    ]
```

`llmbench/src/llmbench/providers/mtplx.py`:

```python
"""The mtplx serve port and the base URLs derived from it.

modelman keeps its own copy in `modelman/providers/mtplx.py` until it is
retired; wt has one in `wt/internal/localmodels` (FamilyOrigin's default-port
table). The three agree through registry.toml's `auth.base_url`, not imports.
"""

MTPLX_PORT = 8003
MTPLX_BASE = f"http://localhost:{MTPLX_PORT}"
MTPLX_V1_BASE = f"{MTPLX_BASE}/v1"
```

`llmbench/src/llmbench/providers/registry.py` (`benchmark/runner.py` calls `ProviderRegistry.resolve`):

```python
"""Provider-id aliases: a registry row that is a second name for one server."""

from __future__ import annotations

# `omlx-6bit` is the omlx server reached through its own registry row (wt's
# localmodels.Family maps it the same way). Two rows that resolve to the same
# name are one server, so the benchmark runner isolates them as one.
_ALIASES: dict[str, str] = {"omlx-6bit": "omlx"}


class ProviderRegistry:
    @classmethod
    def resolve(cls, name: str) -> str:
        """`name`, or the provider it is an alias for."""
        return _ALIASES.get(name, name)
```

- [ ] **Step 5b: Write the wt bridge**

`llmbench/src/llmbench/wt_bridge.py` (copied from `modelman/src/modelman/wt_bridge.py`: the three exception classes at lines 21-31, `ensure_wt` at 66-69, `_msg` at 103-119, `WARM_TIMEOUT` and `warm` at 267-294):

```python
"""Subprocess bridge to wt: `wt warm`, the one wt call llmbench makes (#256).

wt resolves the registry's secret_ref, which an omlx with an API key wants
before it loads a model. modelman's own bridge (`modelman/wt_bridge.py`, the
`wt litellm ...`, `wt start` and `wt stop` calls) re-exports the exception
classes below, so one `except WtBridgeError` catches a failure from either.
"""

from __future__ import annotations

import shutil
import subprocess


class WtBridgeError(Exception):
    """wt could not process the request (nothing was changed)."""


class WtNotFoundError(WtBridgeError):
    """The wt binary is not on PATH."""


class WtBridgeTimeoutError(WtBridgeError):
    """wt timed out before it answered."""


def ensure_wt() -> None:
    """Raise WtNotFoundError unless the wt binary is on PATH."""
    if shutil.which("wt") is None:
        raise WtNotFoundError("wt not found on PATH; install it with `make install`")


def _msg(proc: subprocess.CompletedProcess[str], fallback: str) -> str:
    """One clean line for the user from wt's output.

    wt (cobra) prints both `Error: <msg>` and its own `wt: <msg>`; drop blank
    lines, strip those prefixes, de-duplicate and join with "; ". Only wt's
    own output is used (never argv), so no api key can leak through here.
    """
    raw = (proc.stderr or "").strip() or (proc.stdout or "").strip()
    lines: list[str] = []
    for line in raw.splitlines():
        line = line.strip()
        for prefix in ("Error: ", "wt: "):
            if line.startswith(prefix):
                line = line[len(prefix) :].strip()
        if line and line not in lines:
            lines.append(line)
    return "; ".join(lines) or fallback


# Above wt's own warmup budget (lifecycle's warmupTimeout, 600s), so a model
# that never loads is wt's failure to report, not a kill from here.
WARM_TIMEOUT = 630.0


def warm(provider: str, model: str, timeout: float = WARM_TIMEOUT) -> None:
    """Have wt load `model` into `provider`'s running server (`wt warm`).

    wt is asked because it resolves the registry's secret_ref: an omlx with
    an API key refuses the keyless warmup llmbench sends itself (#256).
    Raises WtBridgeError with wt's own message when the model did not load.
    """
    ensure_wt()
    try:
        proc = subprocess.run(
            ["wt", "warm", provider, model],
            capture_output=True,
            text=True,
            encoding="utf-8",
            timeout=timeout,
        )
    except subprocess.TimeoutExpired:
        raise WtBridgeTimeoutError(f"wt warm timed out after {timeout:g}s") from None
    except FileNotFoundError:
        raise WtNotFoundError("wt not found on PATH; install it with `make install`") from None
    except OSError as e:
        raise WtBridgeError(f"wt warm could not run: {type(e).__name__}") from None
    if proc.returncode != 0:
        raise WtBridgeError(_msg(proc, f"wt exited {proc.returncode}"))
```

- [ ] **Step 5c: Write the registry reader**

`llmbench/src/llmbench/registry.py`. Only the fields the benchmarks read. Task 3 adds the read-path rules:

```python
"""Read-only view of registry.toml: the fields the benchmarks read, no more.

wt owns the file; modelman still writes it until it is retired. llmbench
never writes it, so this reader keeps no unknown keys, parses no prices and
accepts a top-level table it does not know.
"""

from __future__ import annotations

import os
import tomllib
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

# The local providers the benchmarks can isolate and run against.
DEFAULT_PROVIDER_IDS: tuple[str, ...] = ("ollama", "omlx", "mlx_lm_server", "mtplx")


class RegistryError(Exception):
    """registry.toml is missing or cannot be read."""


@dataclass
class ProviderEntry:
    id: str
    name: str = ""
    location: str | None = None  # "local" | "cloud"


@dataclass
class Fetch:
    repo: str | None = None
    local_path: str | None = None


@dataclass
class DraftSpec:
    """The draft model paired with a target's `fetch` (mlx_lm_server pairings)."""

    repo: str | None = None
    local_path: str | None = None


@dataclass
class ModelEntry:
    id: str
    family: str
    provider_id: str
    model_name: str
    location: str | None = None
    fetch: Fetch | None = None
    draft: DraftSpec | None = None


@dataclass
class Registry:
    providers: list[ProviderEntry] = field(default_factory=list)
    models: list[ModelEntry] = field(default_factory=list)

    def provider(self, provider_id: str) -> ProviderEntry:
        for p in self.providers:
            if p.id == provider_id:
                return p
        raise KeyError(f"Unknown provider: {provider_id}")

    def model(self, model_id: str) -> ModelEntry:
        for m in self.models:
            if m.id == model_id:
                return m
        raise KeyError(f"Unknown model: {model_id}")


def _is_local_location(location: str | None) -> bool:
    return location is None or location == "" or location == "local"


def is_model_local(
    location: str | None,
    provider_id: str,
    registry: Registry,
    *,
    missing_provider_is_local: bool = False,
) -> bool:
    """Whether a model counts as local: its own `location` when set, else its
    provider's. A provider missing from the registry is not local unless
    `missing_provider_is_local`."""
    if location is not None:
        return _is_local_location(location)
    try:
        provider = registry.provider(provider_id)
    except KeyError:
        return missing_provider_is_local
    return _is_local_location(provider.location)


def registry_path() -> Path:
    """Where the registry lives: MODELMAN_REGISTRY > XDG_CONFIG_HOME > ~/.config.

    The same precedence as wt's config.RegistryPath and modelman's
    _default_registry_path."""
    override = os.environ.get("MODELMAN_REGISTRY")
    if override:
        return Path(override).expanduser()
    base = os.environ.get("XDG_CONFIG_HOME") or "~/.config"
    return Path(base, "local-ai", "registry.toml").expanduser()


def _parse_provider(raw: dict[str, Any]) -> ProviderEntry:
    if "id" not in raw:
        raise RegistryError(f"Provider entry missing required `id` field: {raw}")
    return ProviderEntry(
        id=raw["id"], name=raw.get("name", raw["id"]), location=raw.get("location")
    )


def _parse_model(raw: dict[str, Any]) -> ModelEntry:
    missing = {"id", "family", "provider_id", "model_name"} - set(raw)
    if missing:
        raise RegistryError(f"Model entry missing required fields {missing}: {raw}")
    fetch = raw.get("fetch")
    draft = raw.get("draft")
    return ModelEntry(
        id=raw["id"],
        family=raw["family"],
        provider_id=raw["provider_id"],
        model_name=raw["model_name"],
        location=raw.get("location"),
        fetch=Fetch(repo=fetch.get("repo"), local_path=fetch.get("local_path"))
        if fetch is not None
        else None,
        draft=DraftSpec(repo=draft.get("repo"), local_path=draft.get("local_path"))
        if draft is not None
        else None,
    )


def load_registry(path: Path | None = None) -> Registry:
    registry_file = Path(path) if path else registry_path()
    try:
        with open(registry_file, "rb") as f:
            raw = tomllib.load(f)
    except FileNotFoundError:
        raise RegistryError(f"Registry file not found: {registry_file}") from None
    except (OSError, tomllib.TOMLDecodeError) as exc:
        raise RegistryError(f"cannot read {registry_file}: {exc}") from exc
    return Registry(
        providers=[_parse_provider(p) for p in raw.get("providers", [])],
        models=[_parse_model(m) for m in raw.get("models", [])],
    )
```

- [ ] **Step 5d: Write the pointer store**

`llmbench/src/llmbench/state.py`. The moved CLIs call `load_state()` / `save_state()` and read `state.extra["benchmarks"]`; this module keeps those names and that shape over the new file, so `benchmark/cli.py`, `benchmark/agent/cli.py` and `benchmark/eval/cli.py` need no edit. It must never default to modelman.toml: a flat pointer file written there would destroy modelman's state. Task 4 adds the fallback read:

```python
"""The latest-run pointers behind `--latest`.

One flat TOML file beside the results it points at:
`~/.config/local-ai/benchmarks/latest.toml` (override: LLMBENCH_LATEST), with
the keys `last_run`, `last_run_dir`, `agent_last_run` and `eval_last_run`.

`StateStore` keeps the pointers under `extra["benchmarks"]`, the shape they
had as modelman.toml's `[benchmarks]` table, so the three CLIs read and write
them as they always did.
"""

from __future__ import annotations

import os
import tomllib
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from ._toml_io import atomic_write_toml


@dataclass
class StateStore:
    extra: dict[str, Any] = field(default_factory=dict)


def latest_path() -> Path:
    override = os.environ.get("LLMBENCH_LATEST")
    if override:
        return Path(override).expanduser()
    return Path.home() / ".config" / "local-ai" / "benchmarks" / "latest.toml"


def load_state(path: Path | None = None) -> StateStore:
    """The recorded pointers; an empty store when none are recorded."""
    latest = Path(path) if path else latest_path()
    if not latest.exists():
        return StateStore()
    with open(latest, "rb") as f:
        return StateStore(extra={"benchmarks": tomllib.load(f)})


def save_state(store: StateStore, path: Path | None = None) -> None:
    atomic_write_toml(store.extra.get("benchmarks", {}), Path(path) if path else latest_path())
```

- [ ] **Step 6: Write the test guards**

`llmbench/tests/conftest.py`. The first fixture is modelman's `_never_touch_live_providers` (`modelman/tests/conftest.py:234-323`) with the ollama guard from line 129, under the new module names. The second keeps the registry and the pointer file off the real config home. Task 2 adds the scratch HOME.

```python
"""Shared pytest fixtures: the guards that keep the suite off this machine's
live providers and real config."""

import os
import subprocess
import urllib.error
from typing import Any
from unittest.mock import MagicMock

import pytest


def _fake_ollama_runner(args: list[str], **kwargs: Any) -> subprocess.CompletedProcess[str]:
    """Closed, deterministic `ollama`: "not found", as on a runner with no
    ollama binary installed."""
    return subprocess.CompletedProcess(
        args=args,
        returncode=1,
        stdout="",
        stderr="Error: model not found",
    )


_real_subprocess_run = subprocess.run

# argv[0] basenames (plus the "mlx_lm.*" family) the suite must NEVER
# actually execute: every one of them either drives a live local model
# provider on this machine or bounces a real LaunchAgent. `launchctl`
# would restart the user's LiteLLM proxy; `omlx stop` / `mtplx stop` /
# `mlx_lm.server` would tear down or spawn a real multi-GB local model
# an agent may be using mid-request; `wt warm` would load one.
_FAKE_BINARIES = frozenset({"launchctl", "omlx", "mtplx", "ollama", "wt"})
_FAKE_BINARY_PREFIXES = ("mlx_lm.",)


def _should_fake(argv0: str) -> bool:
    """Match on the BASENAME, not the whole argv[0]: mtplx and mlx_lm.*
    are invoked through `binaries.require_binary()`/`resolve_mlx_lm_bin()`,
    which return absolute paths (e.g.
    /opt/homebrew/Cellar/omlx/0.10.0/libexec/bin/mlx_lm.server)."""
    name = os.path.basename(argv0)
    return name in _FAKE_BINARIES or name.startswith(_FAKE_BINARY_PREFIXES)


def _fake_provider_run(cmd, *args, **kwargs):
    """Intercept every live-provider binary invocation; delegate everything
    else to the real subprocess.run.

    `subprocess` is one shared module object, so `monkeypatch.setattr` on ANY
    dotted path that resolves through it replaces `subprocess.run` for the
    whole interpreter. That cuts both ways:

    - A flat `MagicMock(return_value=...)` here would silently neuter every
      other module's real subprocess.run call (git in
      benchmark/agent/workspace.py, `pi --version` in runner.py, gates.py's
      test runners), hence the real-delegating fallback below.
    - ONE global patch is all the interception there is, so the allow-list
      has to name every binary any backend shells out to, not just launchctl.

    `ollama` gets the closed "not found" answer instead of the generic
    success.
    """
    argv = list(cmd) if isinstance(cmd, list | tuple) else [cmd]
    argv0 = str(argv[0]) if argv else ""
    if _should_fake(argv0):
        if os.path.basename(argv0) == "ollama":
            return _fake_ollama_runner(argv, **kwargs)
        return subprocess.CompletedProcess(cmd, 0, stdout="", stderr="")
    return _real_subprocess_run(cmd, *args, **kwargs)


@pytest.fixture(autouse=True)
def _never_touch_live_providers(monkeypatch):
    """The suite must never poll a real localhost port, bounce a real
    LaunchAgent, signal a real pid, or drive a real provider binary."""
    # `urllib.request`, `subprocess` and `os` are each one shared module
    # object: these three patches are process-wide, whatever dotted path
    # reaches them.
    monkeypatch.setattr(
        "llmbench.providers.lifecycle.probe.urllib.request.urlopen",
        MagicMock(side_effect=urllib.error.URLError("hermetic test")),
    )
    monkeypatch.setattr(
        "llmbench.providers.lifecycle.launchd.subprocess.run",
        _fake_provider_run,
    )
    monkeypatch.setattr(
        "llmbench.providers.lifecycle.pidproc.os.kill",
        lambda *a, **k: None,
    )
    monkeypatch.setattr(
        "llmbench.providers.lifecycle.backends.ollama._loaded_model_names",
        lambda: [],
    )


@pytest.fixture(autouse=True)
def _no_real_config(monkeypatch, tmp_path):
    """Keep the registry and the latest-run pointers off the developer's real
    config home. A test that needs either sets the variable itself and wins."""
    monkeypatch.delenv("XDG_CONFIG_HOME", raising=False)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(tmp_path / "no-registry.toml"))
    monkeypatch.setenv("LLMBENCH_LATEST", str(tmp_path / "no-latest.toml"))
```

- [ ] **Step 7: Sync the environment on modelman's pins**

Seed the lock from modelman's before the first resolution, so every package the two share keeps the version the moved code was last linted, type-checked and tested under:

```bash
cp modelman/uv.lock llmbench/uv.lock
cd llmbench && uv lock && uv sync
```

Expected: `uv lock` prints `Removed …` lines for the packages only modelman needs (modelman itself among them) and `Added llmbench v0.1.0`, and no `Updated` line. A `.venv` is created and `llmbench==0.1.0` is installed from the working directory. `llmbench/uv.lock` is committed in Step 14.

Confirm the shared tools did not move:

```bash
cd llmbench
for p in typer ruff mypy pytest pytest-cov coverage tomli-w requests; do
  a=$(awk -v p="$p" '$0=="name = \""p"\"" {getline; print}' uv.lock)
  b=$(awk -v p="$p" '$0=="name = \""p"\"" {getline; print}' ../modelman/uv.lock)
  [ "$a" = "$b" ] || echo "DRIFT $p: llmbench $a / modelman $b"
done
```

Expected: no output. On a `DRIFT` line, pin that package back with `uv lock --upgrade-package '<name>==<modelman's version>'`, run `uv sync`, and repeat the loop. Do not go on with a drifted ruff, mypy, pytest, coverage or Typer: the expected outputs below were measured on modelman's versions.

Then check that the seven support modules import:

```bash
cd llmbench && uv run python -c "import llmbench.registry, llmbench.state, llmbench.wt_bridge, llmbench.local_process, llmbench._toml_io, llmbench.providers.mtplx, llmbench.providers.registry"
```

Expected: no output. An `ImportError` or `SyntaxError` names the module from Steps 5a to 5d to fix.

- [ ] **Step 8: Write the failing test for the command tree**

`llmbench/tests/test_main.py`:

```python
"""The `llmbench` command tree, and the one rule about what it imports."""

import ast
from pathlib import Path

import pytest
from typer.testing import CliRunner

from llmbench.main import app


def test_benchmark_commands_sit_at_the_top_level():
    """`llmbench run`, not `llmbench benchmark run`: the tool is the
    benchmark, so the extra word modelman needed is gone."""
    result = CliRunner().invoke(app, ["list-workloads"])
    assert result.exit_code == 0, result.output
    assert result.output.split() == ["chat", "code", "long", "short"]


@pytest.mark.parametrize(
    ("argv", "subcommands"),
    [
        (["agent", "--help"], ["list-tasks", "list-suites", "run", "show", "judge"]),
        (["eval", "--help"], ["list-categories", "list-items", "run", "show", "judge"]),
        (["provider", "--help"], ["isolate", "stop", "stop-all", "restore", "list"]),
    ],
    ids=["agent", "eval", "provider"],
)
def test_sub_apps_are_mounted(argv, subcommands):
    result = CliRunner().invoke(app, argv)
    assert result.exit_code == 0, result.output
    for name in subcommands:
        assert name in result.output


def test_provider_list_names_every_backend():
    """The bash benchmark scripts and the docs reach isolation through
    `llmbench provider`; `list` is its one command that touches nothing."""
    result = CliRunner().invoke(app, ["provider", "list"])
    assert result.exit_code == 0, result.output
    assert [line.split("\t")[0] for line in result.output.splitlines()] == [
        "llamacpp",
        "mlx_lm_server",
        "mtplx",
        "ollama",
        "omlx",
        "omlx-6bit",
    ]


def test_there_is_no_benchmark_level():
    result = CliRunner().invoke(app, ["benchmark", "run"])
    assert result.exit_code == 2
    assert "No such command 'benchmark'" in result.output


def test_llmbench_never_imports_modelman():
    """llmbench outlives modelman. A module-level import would fail in this
    venv, where modelman is not installed; a lazy one inside a function, in a
    branch no test reaches, would pass every other test here and fail the day
    modelman is deleted."""
    src = Path(__file__).resolve().parents[1] / "src" / "llmbench"
    offenders = []
    for path in sorted(src.rglob("*.py")):
        for node in ast.walk(ast.parse(path.read_text(encoding="utf-8"))):
            if isinstance(node, ast.Import):
                names = [alias.name for alias in node.names]
            elif isinstance(node, ast.ImportFrom) and node.level == 0:
                names = [node.module or ""]
            else:
                continue
            if any(name.split(".")[0] == "modelman" for name in names):
                offenders.append(f"{path.relative_to(src)}:{node.lineno}")
    assert offenders == []
```

Also add the tests of the two copied modules that had none in the moved suite. They pass as soon as they exist; they pin the copies.

`llmbench/tests/test_wt_bridge.py`:

```python
"""The `wt warm` bridge: the one wt call llmbench makes."""

import subprocess

import pytest

from llmbench import wt_bridge


def _cp(stdout="", returncode=0, stderr=""):
    return subprocess.CompletedProcess(args=[], returncode=returncode, stdout=stdout, stderr=stderr)


@pytest.fixture
def wt_on_path(monkeypatch):
    monkeypatch.setattr(wt_bridge.shutil, "which", lambda name: "/usr/local/bin/wt")


def test_warm_runs_wt_warm_and_reports_its_failure(monkeypatch, wt_on_path):
    """`wt warm <provider> <model>` is the keyed warmup llmbench cannot do
    itself (#256). A failure raises with wt's own line, which names the fix,
    and the timeout outlasts wt's warmup budget."""
    seen = []
    result = {"cp": _cp("Qwen-4bit is loaded on omlx\n")}

    def run(argv, **kwargs):
        seen.append((argv, kwargs.get("timeout")))
        return result["cp"]

    monkeypatch.setattr(wt_bridge.subprocess, "run", run)

    wt_bridge.warm("omlx", "Qwen-4bit")
    assert seen == [(["wt", "warm", "omlx", "Qwen-4bit"], wt_bridge.WARM_TIMEOUT)]
    assert wt_bridge.WARM_TIMEOUT > 600

    result["cp"] = _cp(returncode=1, stderr="Error: x wants an API key\nwt: x wants an API key\n")
    with pytest.raises(wt_bridge.WtBridgeError, match="^x wants an API key$"):
        wt_bridge.warm("omlx", "Qwen-4bit")


def test_warm_without_wt_on_path(monkeypatch):
    monkeypatch.setattr(wt_bridge.shutil, "which", lambda name: None)
    with pytest.raises(wt_bridge.WtNotFoundError, match="make install"):
        wt_bridge.warm("omlx", "Qwen-4bit")


@pytest.mark.parametrize(
    ("exc", "want", "message"),
    [
        (
            subprocess.TimeoutExpired(cmd=["wt"], timeout=630),
            wt_bridge.WtBridgeTimeoutError,
            "wt warm timed out after 630s",
        ),
        (FileNotFoundError("wt"), wt_bridge.WtNotFoundError, "wt not found on PATH"),
        (PermissionError("denied"), wt_bridge.WtBridgeError, "wt warm could not run"),
    ],
    ids=["timeout", "vanished", "not-executable"],
)
def test_warm_turns_a_subprocess_failure_into_a_bridge_error(
    monkeypatch, wt_on_path, exc, want, message
):
    """Every way the subprocess can fail reaches the caller as a
    WtBridgeError: the omlx backend catches that one type and turns it into
    the lifecycle's error envelope instead of a traceback."""

    def run(argv, **kwargs):
        raise exc

    monkeypatch.setattr(wt_bridge.subprocess, "run", run)
    with pytest.raises(want, match=message):
        wt_bridge.warm("omlx", "Qwen-4bit")


def test_msg_falls_back_when_wt_printed_nothing():
    assert wt_bridge._msg(_cp(returncode=3), "wt exited 3") == "wt exited 3"
    assert wt_bridge._msg(_cp(stdout="only stdout\n", returncode=1), "x") == "only stdout"
```

`llmbench/tests/test_toml_io.py`:

```python
"""Atomic TOML writes: a crash mid-write never leaves a truncated file."""

import tomllib

import pytest

import llmbench._toml_io as toml_io
from llmbench._toml_io import atomic_write_toml


def test_atomic_write_toml_round_trips_and_creates_parent_dirs(tmp_path):
    target = tmp_path / "nested" / "latest.toml"

    atomic_write_toml({"last_run_dir": "/results"}, target)

    with open(target, "rb") as f:
        assert tomllib.load(f) == {"last_run_dir": "/results"}


def test_atomic_write_toml_leaves_no_tmp_file_on_dump_failure(tmp_path, monkeypatch):
    def _boom(*args, **kwargs):
        raise ValueError("dump failed")

    monkeypatch.setattr(toml_io.tomli_w, "dump", _boom)
    target = tmp_path / "latest.toml"

    with pytest.raises(ValueError, match="dump failed"):
        atomic_write_toml({"a": 1}, target)

    assert not target.exists()
    assert list(tmp_path.glob(f".{target.name}.*")) == []
```

- [ ] **Step 9: Run the command-tree test and confirm it fails**

Run: `cd llmbench && uv run pytest tests/test_main.py -q -o addopts="--tb=line"`
Expected: a collection error, `ModuleNotFoundError: No module named 'llmbench.main'`.

- [ ] **Step 10: Write `main.py`**

`llmbench/src/llmbench/main.py`:

```python
"""llmbench CLI entry point."""

from __future__ import annotations

import typer

from .benchmark.cli import benchmark_app
from .providers.lifecycle.cli import provider_app

app = typer.Typer(help="Benchmark local LLM models.")
# No name: the benchmark commands (run, list-workloads, show-results, agent,
# eval) sit at the top level here. modelman mounts the same sub-app under
# `benchmark` until it is retired.
app.add_typer(benchmark_app)
app.add_typer(provider_app, name="provider")
```

`app.add_typer(benchmark_app)` with no `name` merges the sub-app's commands into the parent (the locked Typer is 0.27.1, the version modelman locks). Do not write `app = benchmark_app` and add `provider` to it: modelman mounts `benchmark_app` too, and would grow a `modelman benchmark provider`.

Run: `cd llmbench && uv run pytest tests/test_main.py -q -o addopts=""`
Expected: `7 passed`.

- [ ] **Step 11: Run the llmbench gate**

```bash
cd llmbench && make check && make test
```

Expected: `make check` ends with `Success: no issues found in 58 source files`. `make test` ends with:

```
Required test coverage of 90.0% reached. Total coverage: 91.47%
============================= 590 passed in …s =============================
```

590 = the 575 moved tests + 7 (`test_main.py`) + 6 (`test_wt_bridge.py`) + 2 (`test_toml_io.py`). If the count is lower, a moved test failed to collect: run `uv run pytest --collect-only -q -o addopts="" | tail -3` and fix the import the error names before going on. The suite takes about 15 seconds.

- [ ] **Step 12: Write the failing test for modelman's mounts**

`modelman/tests/commands/test_llmbench_mounts.py`:

```python
"""`modelman benchmark` and `modelman provider` keep working, served by
llmbench, until modelman is retired. Deleted with modelman."""

from typer.testing import CliRunner

from modelman.main import app


def test_benchmark_is_mounted_from_llmbench():
    result = CliRunner().invoke(app, ["benchmark", "list-workloads"])
    assert result.exit_code == 0, result.output
    assert result.output.split() == ["chat", "code", "long", "short"]


def test_provider_is_mounted_from_llmbench():
    result = CliRunner().invoke(app, ["provider", "list"])
    assert result.exit_code == 0, result.output
    assert "omlx-6bit\t[supported] occupancy=omlx" in result.output


def test_provider_is_not_nested_under_benchmark():
    """llmbench's own root app mounts `provider` beside the benchmark
    commands. That must not leak a second `modelman benchmark provider`."""
    result = CliRunner().invoke(app, ["benchmark", "provider", "list"])
    assert result.exit_code == 2
    assert "No such command 'provider'" in result.output
```

Run: `cd modelman && uv run pytest tests/commands/test_llmbench_mounts.py -q -o addopts="--tb=line"`
Expected: a collection error, `ModuleNotFoundError: No module named 'modelman.benchmark'` (modelman's `main.py` still imports the tree that moved).

Also keep a copy of the hermeticity test in modelman. The original moved to llmbench in Step 2, where it guards llmbench's conftest; modelman's own `_never_touch_live_providers` / `_fake_provider_run` wrapper (`modelman/tests/conftest.py:234-323`) still stands between modelman's start/stop tests and the live `omlx`, `mtplx`, `launchctl` and `mlx_lm.*` binaries, and nothing else in modelman's suite fails if it stops covering one. The file imports only `subprocess` and `pytest`, so the pre-move text needs no edit:

```bash
git show HEAD:modelman/tests/providers/lifecycle/test_hermeticity.py > modelman/tests/test_hermeticity.py
```

(`HEAD` is still the commit before the move; the move is committed in Step 14.) It cannot run until Steps 13a to 13c have rewired modelman's conftest; Step 14 runs it.

- [ ] **Step 13a: Make llmbench a dependency of modelman**

In `modelman/pyproject.toml`, add the dependency (after line 20), forward the extra (line 25) and name the source (before `[build-system]`, line 31):

```toml
    "ruamel-yaml>=0.18",
    "llmbench",
]

[project.optional-dependencies]
eval = [
    "llmbench[eval]",
]

[project.scripts]
modelman = "modelman.main:app"

[tool.uv.sources]
llmbench = { path = "../llmbench", editable = true }

[build-system]
```

Regenerate the lock and install:

```bash
cd modelman && uv lock && uv sync
git diff --stat uv.lock
```

Expected: `uv lock` prints `Added llmbench v0.1.0`, and the diff stat shows `36 insertions(+), 2 deletions(-)`: the `llmbench` package entry and modelman's two references to it, and no version change to any other package. If another package's version changed, the lock was re-resolved: `git checkout modelman/uv.lock` and run `uv lock` again without `--upgrade`.

- [ ] **Step 13b: Point modelman's imports and patch targets at llmbench**

The imports, the patch targets and four stale comments:

```bash
perl -pi -e 's/^from \.benchmark\./from llmbench.benchmark./; s/^(\s+)from \.providers\.lifecycle import stop as lifecycle_stop/$1from llmbench.providers.lifecycle import stop as lifecycle_stop/; s/^from \.providers\.lifecycle\.cli import provider_app/from llmbench.providers.lifecycle.cli import provider_app/' modelman/src/modelman/local_control.py modelman/src/modelman/main.py
perl -pi -e 's/modelman\.(benchmark|providers\.lifecycle)\b/llmbench.$1/g' modelman/src/modelman/local_control.py modelman/src/modelman/local_process.py modelman/src/modelman/providers/mlx_lm_server.py modelman/tests/conftest.py modelman/tests/test_local_control.py modelman/tests/commands/test_local_control.py
git grep -nE 'modelman\.(benchmark|providers\.lifecycle)|from \.benchmark\.|from \.providers\.lifecycle' -- modelman/src modelman/tests
```

Expected: the `git grep` prints nothing. The test edits are these targets, under their new names:

| File | Lines | Before | After |
|---|---|---|---|
| `modelman/tests/conftest.py` | 130, 261, 309, 317, 321 | `"modelman.providers.lifecycle.…"` | `"llmbench.providers.lifecycle.…"` |
| `modelman/tests/test_local_control.py` | 13, 380 | `from modelman.benchmark.… import …` | `from llmbench.benchmark.… import …` |
| `modelman/tests/test_local_control.py` | 904, 921, 2008 | `patch("modelman.providers.lifecycle.stop")` | `patch("llmbench.providers.lifecycle.stop")` |
| `modelman/tests/commands/test_local_control.py` | 51 | `from modelman.benchmark.isolation import IsolateResult` | `from llmbench.benchmark.isolation import IsolateResult` |

- [ ] **Step 13c: Let ruff re-sort the imports**

llmbench is third-party to modelman, so its imports move above the relative ones:

```bash
cd modelman && uv run ruff check --fix src/ tests/
```

Expected: `Found 3 errors (3 fixed, 0 remaining).`

The import blocks after ruff. `modelman/src/modelman/local_control.py`:

```python
from pathlib import Path

from llmbench.benchmark.errors import BenchmarkError
from llmbench.benchmark.isolation import (
    SUPPORTED_PROVIDER_IDS,
    isolate_provider,
    mlx_lm_server_pairing_args,
    stop_all_local_providers,
    stop_provider,
)

# Import the providers package to ensure ProviderRegistry is populated —
# mirrors sync.py's identical defensive import.
from . import (
    providers,  # noqa: F401
    wt_bridge,
)
from .litellm import sync_routes
```

and, in `stop_local_model` (line 1478):

```python
        from llmbench.providers.lifecycle import stop as lifecycle_stop
```

`modelman/src/modelman/main.py` (the two `app.add_typer(…)` calls at lines 54 and 57 are unchanged):

```python
import typer
from llmbench.benchmark.cli import benchmark_app
from llmbench.providers.lifecycle.cli import provider_app

# Import providers package to trigger registration of all providers.
from . import (
    providers,  # noqa: F401
    wt_bridge,
)
from .config import default_config_path
```

- [ ] **Step 14: Run the modelman gate and commit**

```bash
cd modelman && make check && make test
```

Expected: `Success: no issues found in 43 source files`, then `1222 passed` (the 1213 that stayed + 3 mount tests + the 6 in `tests/test_hermeticity.py`) in about 90 seconds.

```bash
git add llmbench modelman
git status --short | grep -v '^[RAMD] ' ; git status --short | awk '{print $1}' | sort | uniq -c
git commit -m "refactor: move the benchmarks and provider lifecycle into llmbench"
```

Expected before the commit: the `grep` prints nothing (everything is staged), and the counts are `23 A`, `1 D`, `9 M`, `118 R`. Git reports one of the 119 moved files, `benchmark/__init__.py`, as a delete plus an add, because the rename changed most of a short file. After the commit `git status --short` prints nothing.

---

### Task 2: A scratch HOME for the test suite

The moved code computes its machine-level inputs from `Path.home()` when its modules are imported, and binds several as default arguments (`plist_path: Path = LITELLM_PLIST`). Today each test avoids them by passing a path or patching one module's copy of the name (`tests/benchmark/eval/test_suite.py:230`, `tests/providers/lifecycle/test_orchestrate.py:690`); a test that forgets reads the developer's real OpenRouter key. This task makes the redirect automatic.

**Files:**
- Create: `llmbench/tests/test_conftest_guards.py`
- Modify: `llmbench/tests/conftest.py:1-12`

**Interfaces:**
- Consumes: `llmbench.registry.registry_path`, `load_registry`, `RegistryError`; `llmbench.state.latest_path`, `load_state` (Task 1).
- Produces: every `Path.home()`-derived constant in llmbench resolves under a scratch directory for the whole test session. Later tasks' tests rely on it.

- [ ] **Step 1: Write the failing tests**

`llmbench/tests/test_conftest_guards.py`:

```python
"""Guards on tests/conftest.py's scratch home.

The moved code computes its machine-level inputs from `Path.home()` at import
time: the LiteLLM LaunchAgent plist (it holds the OpenRouter key),
`~/.pi/agent/models.json` (it holds the LiteLLM key), the results directory
and the latest-run pointers. Several are also bound as default arguments, so
no per-test monkeypatch can redirect them. conftest.py points HOME at a
scratch directory before llmbench is imported; these tests fail if it stops.
"""

from __future__ import annotations

import inspect
import os
import pwd
from pathlib import Path

import pytest

from llmbench import registry, state
from llmbench.benchmark import _routes
from llmbench.benchmark import runner as workload_runner
from llmbench.benchmark.agent import pidriver
from llmbench.benchmark.agent import runner as agent_runner
from llmbench.benchmark.agent import suite as agent_suite
from llmbench.benchmark.eval import runner as eval_runner
from llmbench.benchmark.eval import suite as eval_suite
from llmbench.providers.lifecycle import launchd

REAL_HOME = Path(pwd.getpwuid(os.getuid()).pw_dir)

HOME_PATHS = {
    "_routes.LITELLM_PLIST": _routes.LITELLM_PLIST,
    "_routes.LIVE_PI_MODELS_PATH": _routes.LIVE_PI_MODELS_PATH,
    "agent.suite.LITELLM_PLIST": agent_suite.LITELLM_PLIST,
    "agent.pidriver.LIVE_PI_MODELS_PATH": pidriver.LIVE_PI_MODELS_PATH,
    "eval.suite.LITELLM_PLIST": eval_suite.LITELLM_PLIST,
    "eval.suite.LIVE_PI_MODELS_PATH": eval_suite.LIVE_PI_MODELS_PATH,
    "eval.runner.LITELLM_PLIST": eval_runner.LITELLM_PLIST,
    "eval.runner.LIVE_PI_MODELS_PATH": eval_runner.LIVE_PI_MODELS_PATH,
    "launchd.LITELLM_PLIST": launchd.LITELLM_PLIST,
    "launchd.LLAMACPP_PLIST": launchd.LLAMACPP_PLIST,
    "runner.DEFAULT_RESULTS_DIR": workload_runner.DEFAULT_RESULTS_DIR,
    "agent.runner.DEFAULT_RESULTS_DIR": agent_runner.DEFAULT_RESULTS_DIR,
    "eval.runner.DEFAULT_RESULTS_DIR": eval_runner.DEFAULT_RESULTS_DIR,
}

# Parameters whose default was bound to one of those paths when the function
# was defined: a monkeypatch of the module constant does not reach them.
BOUND_DEFAULTS = {
    "_routes.openrouter_key": (_routes.openrouter_key, "plist_path"),
    "_routes.load_live_models": (_routes.load_live_models, "path"),
    "_routes.litellm_credentials": (_routes.litellm_credentials, "live_models_path"),
    "agent.suite.preflight": (agent_suite.preflight, "plist_path"),
    "agent.pidriver.resolve_pi_target": (pidriver.resolve_pi_target, "live_models_path"),
    "agent.runner.run_suite": (agent_runner.run_suite, "live_models_path"),
    "eval.suite.resolve_row_endpoint": (eval_suite.resolve_row_endpoint, "live_models_path"),
}


def test_home_is_a_scratch_directory():
    assert Path.home() != REAL_HOME
    assert not Path.home().is_relative_to(REAL_HOME)
    assert "XDG_CONFIG_HOME" not in os.environ


@pytest.mark.parametrize("name", sorted(HOME_PATHS))
def test_import_time_path_is_under_the_scratch_home(name):
    assert HOME_PATHS[name].is_relative_to(Path.home()), HOME_PATHS[name]


@pytest.mark.parametrize("name", sorted(BOUND_DEFAULTS))
def test_bound_default_is_under_the_scratch_home(name):
    func, parameter = BOUND_DEFAULTS[name]
    default = inspect.signature(func).parameters[parameter].default
    assert default.is_relative_to(Path.home()), default


def test_credentials_are_not_read_from_this_machine(monkeypatch):
    """With nothing passed and nothing patched, the two credential readers
    see an empty home: no OpenRouter key from the LaunchAgent plist, no
    LiteLLM key from pi's models.json."""
    monkeypatch.delenv("OPENROUTER_API_KEY", raising=False)
    # Booleans, so a failure never prints a real key into the test log.
    found_openrouter_key = _routes.openrouter_key() is not None
    found_live_models = bool(_routes.load_live_models())
    assert not found_openrouter_key, "read an OpenRouter key from the real LaunchAgent plist"
    assert not found_live_models, "read the real ~/.pi/agent/models.json"


def test_default_config_paths_are_under_the_scratch_home(monkeypatch):
    """Even with every override removed, the registry, the pointer file and
    the modelman.toml fallback resolve under the scratch home."""
    for name in ("MODELMAN_REGISTRY", "LLMBENCH_LATEST", "MODELMAN_STATE"):
        monkeypatch.delenv(name, raising=False)
    assert registry.registry_path().is_relative_to(Path.home())
    assert state.latest_path().is_relative_to(Path.home())
    found_pointers = bool(state.load_state().extra)
    assert not found_pointers, "read latest-run pointers from the real config home"
    with pytest.raises(registry.RegistryError, match="Registry file not found"):
        registry.load_registry()
```

- [ ] **Step 2: Run them and confirm they fail**

Run: `cd llmbench && uv run pytest tests/test_conftest_guards.py -q -o addopts="--tb=no"`

Expected on a developer machine that has a LaunchAgent plist and a registry: `3 failed, 20 passed`, the failures being `test_home_is_a_scratch_directory`, `test_credentials_are_not_read_from_this_machine` and `test_default_config_paths_are_under_the_scratch_home`. On a machine with no such files only `test_home_is_a_scratch_directory` fails. The 20 path tests pass already: the paths are under `Path.home()`, which is still the real home.

Use `--tb=no` here. The assertions compare booleans, so even a traceback prints no key, but there is no reason to print one.

Run this with your normal environment. Where `$HOME` is already not the passwd home (a sandboxed executor, CI), all 23 pass before the conftest change: `test_home_is_a_scratch_directory` compares `Path.home()` against the passwd entry, and the redirect it asks for is already in place. That is not a failure of this step; go on to Step 3.

- [ ] **Step 3: Set the scratch HOME before llmbench is imported**

In `llmbench/tests/conftest.py`, replace the top of the file:

```python
"""Shared pytest fixtures: the guards that keep the suite off this machine's
live providers and real config."""

import os
import subprocess
import urllib.error
from typing import Any
from unittest.mock import MagicMock

import pytest
```

with:

```python
"""Shared pytest fixtures: the guards that keep the suite off this machine's
live providers and real config."""

import atexit
import os
import shutil
import subprocess
import sys
import tempfile
import urllib.error
from typing import Any
from unittest.mock import MagicMock

import pytest

# A scratch HOME, set before anything imports llmbench.
#
# llmbench computes its machine-level inputs from Path.home() when its modules
# are imported: the LiteLLM LaunchAgent plist (benchmark/_routes.py and
# providers/lifecycle/launchd.py; it holds the OpenRouter key),
# ~/.pi/agent/models.json (the LiteLLM key), the three DEFAULT_RESULTS_DIR
# constants and the latest-run pointer file. Several are also bound as default
# arguments (`plist_path: Path = LITELLM_PLIST`), which no later monkeypatch
# of the constant reaches. Pointing HOME at an empty directory first redirects
# all of them at once, so a test that forgets to pass a path reads nothing and
# writes into a directory that is deleted at exit.
#
# tests/test_conftest_guards.py fails if this stops working.
if any(name == "llmbench" or name.startswith("llmbench.") for name in sys.modules):
    raise pytest.UsageError(
        "llmbench was imported before tests/conftest.py set the scratch HOME; "
        "its Path.home() constants already point at the real home directory"
    )
_SCRATCH_HOME = tempfile.mkdtemp(prefix="llmbench-test-home-")
atexit.register(shutil.rmtree, _SCRATCH_HOME, ignore_errors=True)
os.environ["HOME"] = _SCRATCH_HOME
os.environ.pop("XDG_CONFIG_HOME", None)
```

This must be module-level code, not a fixture: the constants are computed when llmbench is first imported, which happens while pytest collects, before any fixture runs.

- [ ] **Step 4: Run the tests and the gate**

Run: `cd llmbench && uv run pytest tests/test_conftest_guards.py -q -o addopts=""`
Expected: `23 passed`.

Run: `cd llmbench && make check && make test`
Expected: `613 passed`, coverage `91.74%`. Every moved test still passes under the scratch HOME: none of them needed the real one.

- [ ] **Step 5: Commit**

```bash
git add llmbench/tests/conftest.py llmbench/tests/test_conftest_guards.py
git commit -m "test(llmbench): run the suite under a scratch HOME"
```

---

### Task 3: The registry reader resolves the file modelman's does

`modelman start <mtplx model>` loads the registry twice: modelman's loader picks the model, then the moved mtplx backend (`llmbench/src/llmbench/providers/lifecycle/backends/mtplx.py:65`) loads it again through llmbench. The two must read one file. modelman's rule is `_registry_read_path` (`modelman/src/modelman/registry.py:492-519`): refuse a dangling symlink, and fall back to the pre-XDG `~/.config` file when `XDG_CONFIG_HOME` is set but holds no registry.

**Files:**
- Create: `llmbench/tests/test_registry.py`, `modelman/tests/test_registry_path_parity.py`
- Modify: `llmbench/src/llmbench/registry.py` (add two functions before `_parse_provider`; replace `load_registry`), `docs/contracts/registry.sample.toml:21-25` (header comment)

**Interfaces:**
- Consumes: `llmbench.registry` from Task 1; `modelman.registry._registry_read_path`, `_default_registry_path`, `RegistryNotFoundError`, `RegistryPathError` (existing).
- Produces: `llmbench.registry.registry_read_path(path: Path | None = None) -> Path`, raising `RegistryError` for a missing file (`"Registry file not found: <path>"`) and for a dangling symlink (`"<link> is a symlink to <target>, which does not exist"`). `load_registry` reads through it.

- [ ] **Step 1: Write the failing tests**

`llmbench/tests/test_registry.py`:

```python
"""The read-only registry reader: where it looks, and what it reads."""

import os
from pathlib import Path

import pytest

from llmbench.registry import (
    RegistryError,
    is_model_local,
    load_registry,
    registry_path,
    registry_read_path,
)

FIXTURE = Path(__file__).resolve().parents[2] / "docs" / "contracts" / "registry.sample.toml"

MINIMAL = """\
[[providers]]
id = "ollama"

[[models]]
id = "ollama/a"
family = "f"
provider_id = "ollama"
model_name = "a"
"""


@pytest.fixture
def home(monkeypatch, tmp_path):
    """A scratch home with no registry override in the environment."""
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    monkeypatch.delenv("MODELMAN_REGISTRY", raising=False)
    monkeypatch.delenv("XDG_CONFIG_HOME", raising=False)
    return tmp_path / "home"


def _write(path: Path, text: str = MINIMAL) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")
    return path


def test_registry_path_precedence(home, monkeypatch, tmp_path):
    """MODELMAN_REGISTRY > XDG_CONFIG_HOME > ~/.config: the precedence wt and
    modelman use, so the three tools never read three different files."""
    assert registry_path() == home / ".config" / "local-ai" / "registry.toml"
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    assert registry_path() == tmp_path / "xdg" / "local-ai" / "registry.toml"
    monkeypatch.setenv("MODELMAN_REGISTRY", str(tmp_path / "named.toml"))
    assert registry_path() == tmp_path / "named.toml"


def test_read_path_falls_back_to_the_pre_xdg_registry(home, monkeypatch, tmp_path):
    """A registry created before XDG_CONFIG_HOME was set still lives in
    ~/.config. modelman reads it there, so llmbench must too: otherwise
    `modelman start <mtplx model>` (which loads the registry through
    llmbench's mtplx backend) reports "no registry" on a machine modelman's
    own commands read fine."""
    legacy = _write(home / ".config" / "local-ai" / "registry.toml")
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    assert registry_read_path() == legacy
    assert [m.id for m in load_registry().models] == ["ollama/a"]

    canonical = _write(tmp_path / "xdg" / "local-ai" / "registry.toml")
    assert registry_read_path() == canonical


def test_read_path_does_not_fall_back_past_a_named_registry(home, monkeypatch, tmp_path):
    """MODELMAN_REGISTRY names the file outright: a missing one is missing,
    never "use the one in ~/.config" — a scratch run must not read the
    developer's real registry by accident."""
    _write(home / ".config" / "local-ai" / "registry.toml")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(tmp_path / "scratch.toml"))
    with pytest.raises(RegistryError, match="Registry file not found: .*scratch.toml"):
        registry_read_path()
    with pytest.raises(RegistryError, match="Registry file not found"):
        load_registry(tmp_path / "also-missing.toml")


def test_read_path_refuses_a_dangling_symlink(home, monkeypatch, tmp_path):
    """A link to a registry that is not there (a dotfiles checkout, an
    unmounted volume) is refused, not fallen back from: reading the pre-XDG
    file instead would benchmark a registry the user has replaced (#248)."""
    _write(home / ".config" / "local-ai" / "registry.toml")
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    link = tmp_path / "xdg" / "local-ai" / "registry.toml"
    link.parent.mkdir(parents=True)
    os.symlink(tmp_path / "gone.toml", link)
    with pytest.raises(RegistryError, match="is a symlink to .*gone.toml, which does not exist"):
        registry_read_path()


def test_read_path_refuses_a_dangling_legacy_symlink(home, monkeypatch, tmp_path):
    """The same refusal for the file being fallen back to. XDG_CONFIG_HOME is
    set and holds no registry, and the pre-XDG path is a link to nothing:
    modelman names the link, so llmbench must not say "not found" about the
    XDG path and send the user looking in the wrong directory."""
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    link = home / ".config" / "local-ai" / "registry.toml"
    link.parent.mkdir(parents=True)
    os.symlink(tmp_path / "gone.toml", link)
    with pytest.raises(RegistryError, match="is a symlink to .*gone.toml, which does not exist"):
        registry_read_path()


def test_load_registry_tolerates_unknown_top_level_keys(tmp_path):
    """llmbench never writes the registry, so a top-level table it does not
    know costs it nothing — unlike modelman, whose save would drop it (#247).
    A registry wt has extended must still benchmark."""
    path = _write(tmp_path / "registry.toml", MINIMAL + '\n[future]\nkey = "v"\n')
    assert [m.id for m in load_registry(path).models] == ["ollama/a"]


def test_load_registry_reports_a_file_it_cannot_parse(tmp_path):
    path = _write(tmp_path / "registry.toml", "[[models]\n")
    with pytest.raises(RegistryError, match=f"cannot read {path}"):
        load_registry(path)


def test_load_registry_names_a_row_missing_a_required_field(tmp_path):
    path = _write(tmp_path / "registry.toml", '[[models]]\nid = "x"\n')
    with pytest.raises(RegistryError, match="missing required fields"):
        load_registry(path)
    path = _write(tmp_path / "registry.toml", '[[providers]]\nname = "x"\n')
    with pytest.raises(RegistryError, match="missing required `id` field"):
        load_registry(path)


def test_load_registry_reads_the_shared_contract_fixture():
    """docs/contracts/registry.sample.toml is the schema wt's Go decoder and
    modelman's loader are pinned to. llmbench reads the same file, so a
    schema change that breaks a benchmark fails here in the same PR."""
    registry = load_registry(FIXTURE)

    # Membership, not the whole list: a row a later step adds to the fixture
    # is not this reader's business.
    assert {"ollama", "openrouter", "pinned-cloud", "mlx_lm_server", "mtplx"} <= {
        p.id for p in registry.providers
    }
    assert registry.provider("ollama").location == "local"
    assert registry.provider("openrouter").location == "cloud"
    assert len(registry.models) >= 7

    cloud = registry.model("openrouter/contract-fixture:cloud")
    assert (cloud.family, cloud.provider_id, cloud.model_name, cloud.location) == (
        "contract-fixture",
        "openrouter",
        "org/contract-fixture-cloud",
        "cloud",
    )
    pair = registry.model("mlx_lm_server/contract-fixture:pair")
    assert pair.fetch is not None and pair.fetch.repo == "org/contract-fixture-target"
    assert pair.draft is not None and pair.draft.repo == "org/contract-fixture-draft"
    assert pair.fetch.local_path is None and pair.draft.local_path is None
    assert registry.model("ollama/contract-fixture:local").fetch is None

    # Location: a model's own value wins, else its provider's.
    inherit = registry.model("pinned-cloud/contract-fixture:inherit")
    assert inherit.location is None
    assert not is_model_local(inherit.location, inherit.provider_id, registry)
    local = registry.model("mtplx/org--contract-fixture-dashed")
    assert is_model_local(local.location, local.provider_id, registry)
    assert not is_model_local(None, "no-such-provider", registry)
    assert is_model_local(None, "no-such-provider", registry, missing_provider_is_local=True)
```

`modelman/tests/test_registry_path_parity.py` (modelman can import both packages; llmbench must not import modelman, so the parity test lives here and is deleted with modelman in Step 6):

```python
"""modelman and llmbench must read the same registry.toml.

`modelman start <mtplx model>` loads the registry twice: modelman's loader
picks the model, then llmbench's mtplx backend loads it again to resolve the
name. If the two resolve different files, the start acts on one registry and
serves a model from another. Deleted with modelman.
"""

import os
from pathlib import Path

import pytest
from llmbench import registry as bench_registry

from modelman import registry as modelman_registry

REGISTRY = '[[models]]\nid = "mtplx/a"\nfamily = "f"\nprovider_id = "mtplx"\nmodel_name = "a"\n'


@pytest.fixture
def home(monkeypatch, tmp_path):
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    monkeypatch.delenv("MODELMAN_REGISTRY", raising=False)
    monkeypatch.delenv("XDG_CONFIG_HOME", raising=False)
    return tmp_path / "home"


def _write(path: Path) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(REGISTRY, encoding="utf-8")
    return path


def _both_read() -> Path:
    """The file both loaders read; fails when they differ."""
    ours = modelman_registry._registry_read_path()
    theirs = bench_registry.registry_read_path()
    assert theirs == ours
    assert bench_registry.registry_path() == modelman_registry._default_registry_path()
    assert [m.id for m in bench_registry.load_registry().models] == ["mtplx/a"]
    return ours


def test_both_read_the_xdg_registry_when_it_is_there(home, monkeypatch, tmp_path):
    _write(home / ".config" / "local-ai" / "registry.toml")
    xdg = _write(tmp_path / "xdg" / "local-ai" / "registry.toml")
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    assert _both_read() == xdg


def test_both_fall_back_to_the_pre_xdg_registry(home, monkeypatch, tmp_path):
    legacy = _write(home / ".config" / "local-ai" / "registry.toml")
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    assert _both_read() == legacy


def test_both_read_the_default_registry_without_xdg(home):
    default = _write(home / ".config" / "local-ai" / "registry.toml")
    assert _both_read() == default


def test_both_report_no_registry_at_the_xdg_path(home, monkeypatch, tmp_path):
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    wanted = tmp_path / "xdg" / "local-ai" / "registry.toml"
    with pytest.raises(modelman_registry.RegistryNotFoundError, match=str(wanted)):
        modelman_registry._registry_read_path()
    with pytest.raises(bench_registry.RegistryError, match=str(wanted)):
        bench_registry.registry_read_path()


def test_neither_falls_back_past_a_named_registry(home, monkeypatch, tmp_path):
    _write(home / ".config" / "local-ai" / "registry.toml")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(tmp_path / "scratch.toml"))
    with pytest.raises(modelman_registry.RegistryNotFoundError):
        modelman_registry._registry_read_path()
    with pytest.raises(bench_registry.RegistryError, match="Registry file not found"):
        bench_registry.registry_read_path()


def test_both_refuse_a_dangling_symlink(home, monkeypatch, tmp_path):
    _write(home / ".config" / "local-ai" / "registry.toml")
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    link = tmp_path / "xdg" / "local-ai" / "registry.toml"
    link.parent.mkdir(parents=True)
    os.symlink(tmp_path / "gone.toml", link)
    with pytest.raises(modelman_registry.RegistryPathError):
        modelman_registry._registry_read_path()
    with pytest.raises(bench_registry.RegistryError, match="is a symlink to"):
        bench_registry.registry_read_path()
```

llmbench is now a third reader of the contract fixture. Name it in the fixture's own header, which is how a wt or modelman author learns who breaks when the file changes. In `docs/contracts/registry.sample.toml`, after this line of the `# Read by:` list:

```toml
#   - modelman/tests/contracts/test_registry_fixture.py (Python)
```

add:

```toml
#   - llmbench/tests/test_registry.py (Python; reads id/family/provider_id/
#     model_name/location/fetch/draft only)
```

- [ ] **Step 2: Run them and confirm they fail**

Run: `cd llmbench && uv run pytest tests/test_registry.py -q -o addopts="--tb=line"`
Expected: a collection error, `ImportError: cannot import name 'registry_read_path' from 'llmbench.registry'`.

Run: `cd modelman && uv run pytest tests/test_registry_path_parity.py -q -o addopts="--tb=no"`
Expected: `6 failed` (`AttributeError: module 'llmbench.registry' has no attribute 'registry_read_path'`).

- [ ] **Step 3: Implement the read path**

In `llmbench/src/llmbench/registry.py`, add these two functions between `registry_path` and `_parse_provider`:

```python
def _refuse_dangling_symlink(path: Path) -> None:
    """A link to a file that is not there is a pointer at where the registry
    lives, not an absent registry: refuse it (#248)."""
    if path.is_symlink() and not path.exists():
        raise RegistryError(f"{path} is a symlink to {os.readlink(path)}, which does not exist")


def registry_read_path(path: Path | None = None) -> Path:
    """The file load_registry reads. Not always registry_path(): a registry
    created before XDG_CONFIG_HOME was set is still read from ~/.config.

    Must resolve the file modelman's `_registry_read_path` does until modelman
    is retired: `modelman start <mtplx model>` loads the registry through
    both (modelman/tests/test_registry_path_parity.py holds them together).
    """
    wanted = Path(path) if path else registry_path()
    _refuse_dangling_symlink(wanted)
    if wanted.exists():
        return wanted
    # Not past MODELMAN_REGISTRY or an explicit path: those name the file
    # outright, and a missing one is missing.
    legacy = Path("~/.config/local-ai/registry.toml").expanduser()
    if path is None and not os.environ.get("MODELMAN_REGISTRY") and wanted != legacy:
        _refuse_dangling_symlink(legacy)
        if legacy.exists():
            return legacy
    raise RegistryError(f"Registry file not found: {wanted}")
```

and replace `load_registry`:

```python
def load_registry(path: Path | None = None) -> Registry:
    registry_file = Path(path) if path else registry_path()
    try:
        with open(registry_file, "rb") as f:
            raw = tomllib.load(f)
    except FileNotFoundError:
        raise RegistryError(f"Registry file not found: {registry_file}") from None
    except (OSError, tomllib.TOMLDecodeError) as exc:
        raise RegistryError(f"cannot read {registry_file}: {exc}") from exc
    return Registry(
        providers=[_parse_provider(p) for p in raw.get("providers", [])],
        models=[_parse_model(m) for m in raw.get("models", [])],
    )
```

with:

```python
def load_registry(path: Path | None = None) -> Registry:
    registry_file = registry_read_path(path)
    try:
        with open(registry_file, "rb") as f:
            raw = tomllib.load(f)
    except (OSError, tomllib.TOMLDecodeError) as exc:
        raise RegistryError(f"cannot read {registry_file}: {exc}") from exc
    return Registry(
        providers=[_parse_provider(p) for p in raw.get("providers", [])],
        models=[_parse_model(m) for m in raw.get("models", [])],
    )
```

The reader still accepts any top-level key (`raw.get("providers", [])`, `raw.get("models", [])`, nothing else inspected): it never writes, so there is nothing for it to drop. `test_load_registry_tolerates_unknown_top_level_keys` pins that; do not port modelman's `_reject_unknown_top_level_keys`.

- [ ] **Step 4: Run the tests and the gates**

Run: `cd llmbench && uv run pytest tests/test_registry.py -q -o addopts=""`
Expected: `9 passed`.

Run: `cd modelman && uv run pytest tests/test_registry_path_parity.py tests/test_local_control.py -q -o addopts=""`
Expected: `163 passed`.

Run: `cd llmbench && make check && make test`
Expected: `622 passed`, coverage `92.21%`.

Run: `cd modelman && make check`
Expected: `Success: no issues found in 43 source files`.

- [ ] **Step 5: Commit**

```bash
git add llmbench/src/llmbench/registry.py llmbench/tests/test_registry.py modelman/tests/test_registry_path_parity.py docs/contracts/registry.sample.toml
git commit -m "feat(llmbench): read the registry file modelman reads, pre-XDG fallback included"
```

---

### Task 4: `latest.toml`, with a one-time read of modelman.toml's pointers

**Files:**
- Create: `llmbench/tests/test_state.py`
- Modify: `llmbench/src/llmbench/state.py` (whole file), `llmbench/tests/conftest.py` (`_no_real_config`)

**Interfaces:**
- Consumes: `llmbench._toml_io.atomic_write_toml` (Task 1).
- Produces: `load_state()` returns modelman.toml's `[benchmarks]` pointers (the four keys, strings only) while `latest.toml` does not exist; a corrupt `latest.toml` reads as an empty store. `StateStore`, `latest_path`, `save_state` keep their Task 1 signatures. modelman.toml is found by `MODELMAN_STATE` > `XDG_CONFIG_HOME` > `~/.config`, modelman's own rule (`modelman/src/modelman/state.py:67-78`).

- [ ] **Step 1: Write the failing tests**

`llmbench/tests/test_state.py`:

```python
"""The latest-run pointers: their own file, and the one-time read of the
keys modelman.toml used to hold."""

import tomllib
from pathlib import Path

import pytest

from llmbench.state import StateStore, latest_path, load_state, save_state

MODELMAN_TOML = """\
price_refresh_last_run = "2026-10-01"

[benchmarks]
last_run = "2026-08-28T14:32:00+00:00"
last_run_dir = "/results"
agent_last_run = "/results/agent-1"
eval_last_run = "/results/eval-1"
some_other_key = "not a pointer"

[model_state."ollama/a"]
ready = true
"""


@pytest.fixture
def paths(monkeypatch, tmp_path):
    """(latest.toml, modelman.toml) under tmp_path, neither written yet."""
    latest = tmp_path / "benchmarks" / "latest.toml"
    legacy = tmp_path / "modelman.toml"
    monkeypatch.setenv("LLMBENCH_LATEST", str(latest))
    monkeypatch.setenv("MODELMAN_STATE", str(legacy))
    return latest, legacy


def test_latest_path_default_and_override(monkeypatch, tmp_path):
    monkeypatch.delenv("LLMBENCH_LATEST", raising=False)
    assert latest_path() == Path.home() / ".config" / "local-ai" / "benchmarks" / "latest.toml"
    monkeypatch.setenv("LLMBENCH_LATEST", str(tmp_path / "x.toml"))
    assert latest_path() == tmp_path / "x.toml"


def test_latest_path_ignores_xdg_config_home(monkeypatch, tmp_path):
    """latest.toml sits beside the results it points at, and the results
    directory is ~/.config/local-ai/benchmarks whatever XDG_CONFIG_HOME says.
    (The fallback read of modelman.toml does honour XDG: see below.)"""
    monkeypatch.delenv("LLMBENCH_LATEST", raising=False)
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    assert latest_path() == Path.home() / ".config" / "local-ai" / "benchmarks" / "latest.toml"


def test_pointers_round_trip_through_a_flat_file(paths):
    """latest.toml is flat: the four keys at the top level, no [benchmarks]
    table. The file is read by hand when a run goes missing."""
    latest, _ = paths
    store = load_state()
    assert store.extra == {}
    store.extra.setdefault("benchmarks", {})["agent_last_run"] = "/results/agent-2"
    save_state(store)

    assert tomllib.loads(latest.read_text(encoding="utf-8")) == {
        "agent_last_run": "/results/agent-2"
    }
    assert load_state().extra["benchmarks"] == {"agent_last_run": "/results/agent-2"}


def test_first_read_falls_back_to_modelmans_benchmarks_table(paths):
    """A user who ran benchmarks through modelman has their --latest pointers
    in modelman.toml. `llmbench agent show --latest` must still find that run
    the first time, not report "no latest run recorded"."""
    latest, legacy = paths
    legacy.write_text(MODELMAN_TOML, encoding="utf-8")

    assert load_state().extra["benchmarks"] == {
        "last_run": "2026-08-28T14:32:00+00:00",
        "last_run_dir": "/results",
        "agent_last_run": "/results/agent-1",
        "eval_last_run": "/results/eval-1",
    }
    assert not latest.exists()  # a read never writes


def test_the_fallback_is_read_once(paths):
    """The first recorded run carries the old pointers into latest.toml;
    after that modelman.toml is never consulted, so a pointer modelman.toml
    still holds cannot shadow a newer one."""
    latest, legacy = paths
    legacy.write_text(MODELMAN_TOML, encoding="utf-8")

    store = load_state()
    store.extra["benchmarks"]["eval_last_run"] = "/results/eval-2"
    save_state(store)

    legacy.write_text(MODELMAN_TOML.replace("agent-1", "agent-STALE"), encoding="utf-8")
    assert load_state().extra["benchmarks"] == {
        "last_run": "2026-08-28T14:32:00+00:00",
        "last_run_dir": "/results",
        "agent_last_run": "/results/agent-1",
        "eval_last_run": "/results/eval-2",
    }
    assert legacy.read_text(encoding="utf-8").count("agent-STALE") == 1  # never written


@pytest.mark.parametrize(
    "legacy_text",
    [
        None,  # no modelman.toml at all: a machine that never ran modelman
        "[benchmarks\n",  # unreadable
        'benchmarks = "not a table"\n',
        "[benchmarks]\nlast_run_dir = 7\n",  # not a string: not a pointer
        "[model_state]\n",  # no [benchmarks] table
    ],
    ids=["absent", "corrupt", "not-a-table", "wrong-type", "no-table"],
)
def test_an_unusable_modelman_toml_reads_as_no_pointers(paths, legacy_text):
    _, legacy = paths
    if legacy_text is not None:
        legacy.write_text(legacy_text, encoding="utf-8")
    assert load_state().extra.get("benchmarks", {}) == {}


def test_a_corrupt_latest_toml_reads_as_no_pointers_and_is_replaced(paths):
    """The pointer file is disposable. A torn or hand-broken one must not
    crash the pointer write that follows a benchmark run that took hours."""
    latest, legacy = paths
    legacy.write_text(MODELMAN_TOML, encoding="utf-8")
    latest.parent.mkdir(parents=True)
    latest.write_text("last_run = \n", encoding="utf-8")

    store = load_state()
    assert store.extra.get("benchmarks", {}) == {}  # present: no fallback either
    store.extra.setdefault("benchmarks", {})["last_run_dir"] = "/results"
    save_state(store)
    assert load_state().extra["benchmarks"] == {"last_run_dir": "/results"}


def test_the_fallback_finds_modelman_toml_where_modelman_kept_it(monkeypatch, tmp_path):
    """MODELMAN_STATE > XDG_CONFIG_HOME > ~/.config, modelman's own rule."""
    monkeypatch.setenv("LLMBENCH_LATEST", str(tmp_path / "latest.toml"))
    monkeypatch.delenv("MODELMAN_STATE", raising=False)
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    home_file = tmp_path / "home" / ".config" / "local-ai" / "modelman.toml"
    home_file.parent.mkdir(parents=True)
    home_file.write_text('[benchmarks]\nlast_run_dir = "/from-home"\n', encoding="utf-8")
    assert load_state().extra["benchmarks"] == {"last_run_dir": "/from-home"}

    xdg_file = tmp_path / "xdg" / "local-ai" / "modelman.toml"
    xdg_file.parent.mkdir(parents=True)
    xdg_file.write_text('[benchmarks]\nlast_run_dir = "/from-xdg"\n', encoding="utf-8")
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    assert load_state().extra["benchmarks"] == {"last_run_dir": "/from-xdg"}


def test_an_explicit_path_never_falls_back(paths, tmp_path):
    _, legacy = paths
    legacy.write_text(MODELMAN_TOML, encoding="utf-8")
    assert load_state(tmp_path / "elsewhere.toml") == StateStore()
```

- [ ] **Step 2: Run them and confirm they fail**

Run: `cd llmbench && uv run pytest tests/test_state.py -q -o addopts="--tb=no"`
Expected: `4 failed, 9 passed`. The failures are `test_first_read_falls_back_to_modelmans_benchmarks_table`, `test_the_fallback_is_read_once`, `test_a_corrupt_latest_toml_reads_as_no_pointers_and_is_replaced` and `test_the_fallback_finds_modelman_toml_where_modelman_kept_it`. The nine that pass describe behaviour Task 1's module already has (the path, the flat file, no pointers when there is nothing to read).

- [ ] **Step 3: Implement the fallback**

Replace `llmbench/src/llmbench/state.py` with:

```python
"""The latest-run pointers behind `--latest`.

One flat TOML file beside the results it points at:
`~/.config/local-ai/benchmarks/latest.toml` (override: LLMBENCH_LATEST), with
the keys `last_run`, `last_run_dir`, `agent_last_run` and `eval_last_run`.

`StateStore` keeps the pointers under `extra["benchmarks"]`, the shape they
had as modelman.toml's `[benchmarks]` table, so the three CLIs read and write
them as they always did. Until latest.toml exists, that table is where the
pointers are read from: the first recorded run carries them over.
"""

from __future__ import annotations

import os
import tomllib
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from ._toml_io import atomic_write_toml

_POINTER_KEYS = ("last_run", "last_run_dir", "agent_last_run", "eval_last_run")


@dataclass
class StateStore:
    extra: dict[str, Any] = field(default_factory=dict)


def latest_path() -> Path:
    override = os.environ.get("LLMBENCH_LATEST")
    if override:
        return Path(override).expanduser()
    return Path.home() / ".config" / "local-ai" / "benchmarks" / "latest.toml"


def _modelman_state_path() -> Path:
    """Where modelman kept modelman.toml: MODELMAN_STATE > XDG_CONFIG_HOME >
    ~/.config (modelman/state.py's _default_state_path)."""
    override = os.environ.get("MODELMAN_STATE")
    if override:
        return Path(override).expanduser()
    base = os.environ.get("XDG_CONFIG_HOME") or "~/.config"
    return Path(base, "local-ai", "modelman.toml").expanduser()


def _read_toml(path: Path) -> dict[str, Any]:
    """The file's table, or {} when it is absent or cannot be read. A pointer
    file is disposable: a broken one must not fail the run that replaces it."""
    try:
        with open(path, "rb") as f:
            return tomllib.load(f)
    except (OSError, tomllib.TOMLDecodeError):
        return {}


def _modelman_pointers() -> dict[str, str]:
    """The pointers modelman.toml's `[benchmarks]` table still holds."""
    table = _read_toml(_modelman_state_path()).get("benchmarks")
    if not isinstance(table, dict):
        return {}
    return {k: table[k] for k in _POINTER_KEYS if isinstance(table.get(k), str)}


def load_state(path: Path | None = None) -> StateStore:
    """The recorded pointers; an empty store when none are recorded.

    While latest.toml does not exist, the pointers come from modelman.toml,
    which held them before llmbench was carved out. Read-only: the next
    save_state writes them to latest.toml, and modelman.toml is not consulted
    again."""
    latest = Path(path) if path else latest_path()
    if latest.exists():
        pointers = _read_toml(latest)
    elif path is None:
        pointers = _modelman_pointers()
    else:
        pointers = {}
    return StateStore(extra={"benchmarks": pointers} if pointers else {})


def save_state(store: StateStore, path: Path | None = None) -> None:
    atomic_write_toml(store.extra.get("benchmarks", {}), Path(path) if path else latest_path())
```

"Once" needs no marker file: `load_state()` consults modelman.toml only while `latest.toml` is absent, and each CLI's record step is load, set one key, save. The first save writes `latest.toml` with the old pointers plus the new one.

In `llmbench/tests/conftest.py`, keep the fallback off the developer's real modelman.toml. After this line of `_no_real_config`:

```python
    monkeypatch.setenv("LLMBENCH_LATEST", str(tmp_path / "no-latest.toml"))
```

add:

```python
    # ...and the one-time fallback read of modelman.toml's [benchmarks] table.
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "no-modelman.toml"))
```

- [ ] **Step 4: Run the tests and the gate**

Run: `cd llmbench && uv run pytest tests/test_state.py -q -o addopts=""`
Expected: `13 passed`.

Run: `cd llmbench && make check && make test`
Expected: `635 passed`, coverage `92.25%`.

- [ ] **Step 5: Commit**

```bash
git add llmbench/src/llmbench/state.py llmbench/tests/test_state.py llmbench/tests/conftest.py
git commit -m "feat(llmbench): latest-run pointers in latest.toml, read once from modelman.toml"
```

---

### Task 5: `LLMBENCH_WORKLOAD` and `LLMBENCH_AGENT_DEBUG`, with the old names as aliases

**Files:**
- Create: `llmbench/src/llmbench/_env.py`
- Modify: `llmbench/src/llmbench/benchmark/cli.py:5-10,68`, `llmbench/src/llmbench/benchmark/agent/runner.py:18,194`, `llmbench/tests/benchmark/test_cli.py:1-8` and its end, `llmbench/tests/benchmark/agent/test_runner.py:728`

**Interfaces:**
- Consumes: nothing.
- Produces: `llmbench._env.env_first(*names: str) -> str | None`, the value of the first name that is set and not empty.

- [ ] **Step 1: Write the failing tests**

In `llmbench/tests/benchmark/test_cli.py`, replace the imports:

```python
from typer.testing import CliRunner

from llmbench.benchmark.results import BenchmarkRun
```

with:

```python
import pytest
from typer.testing import CliRunner

from llmbench.benchmark.errors import BenchmarkError
from llmbench.benchmark.results import BenchmarkRun
```

and append to the end of the file (two blank lines above it):

```python
@pytest.mark.parametrize(
    ("env", "want"),
    [
        ({}, "code"),
        ({"LLMBENCH_WORKLOAD": "long"}, "long"),
        ({"MODELMAN_BENCHMARK_WORKLOAD": "short"}, "short"),
        ({"LLMBENCH_WORKLOAD": "long", "MODELMAN_BENCHMARK_WORKLOAD": "short"}, "long"),
        ({"LLMBENCH_WORKLOAD": "", "MODELMAN_BENCHMARK_WORKLOAD": "short"}, "short"),
    ],
    ids=["flag", "llmbench", "modelman-alias", "llmbench-wins", "empty-is-unset"],
)
def test_run_takes_the_workload_from_the_environment(monkeypatch, env, want):
    """LLMBENCH_WORKLOAD overrides --workload. MODELMAN_BENCHMARK_WORKLOAD,
    the name it had under modelman, keeps working: a wrapper script that
    exports the old name must not silently fall back to the flag's default
    and benchmark a different workload."""
    for name in ("LLMBENCH_WORKLOAD", "MODELMAN_BENCHMARK_WORKLOAD"):
        monkeypatch.delenv(name, raising=False)
    for name, value in env.items():
        monkeypatch.setenv(name, value)
    seen = []

    def _get_workload(name):
        seen.append(name)
        raise BenchmarkError("stop here")

    with patch("llmbench.benchmark.cli.get_workload", _get_workload):
        result = CliRunner().invoke(app, ["run", "--workload", "code"])
    assert result.exit_code == 1
    assert seen == [want]
```

In `llmbench/tests/benchmark/agent/test_runner.py`, in `test_row_dir_has_no_metrics_log_unless_debug`, replace:

```python
    monkeypatch.delenv("MODELMAN_AGENT_DEBUG", raising=False)
```

with:

```python
    monkeypatch.delenv("LLMBENCH_AGENT_DEBUG", raising=False)
    monkeypatch.delenv("MODELMAN_AGENT_DEBUG", raising=False)
```

and add this test after that function (before `def _mlx_lm_registry`, two blank lines on each side):

```python
@pytest.mark.parametrize("name", ["LLMBENCH_AGENT_DEBUG", "MODELMAN_AGENT_DEBUG"])
def test_row_dir_has_a_metrics_log_under_debug(tmp_path, monkeypatch, litellm_models_json, name):
    """LLMBENCH_AGENT_DEBUG asks for the per-event metrics trace.
    MODELMAN_AGENT_DEBUG, its name under modelman, keeps working: someone
    debugging a metric with the old name must not get a run with no trace
    and have to repeat it."""
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: None)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)
    monkeypatch.delenv("LLMBENCH_AGENT_DEBUG", raising=False)
    monkeypatch.delenv("MODELMAN_AGENT_DEBUG", raising=False)
    monkeypatch.setenv(name, "1")

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    _, results = run_suite(
        suite,
        _registry(),
        results_dir=tmp_path / "results",
        live_models_path=litellm_models_json,
        skip_judge=True,
    )
    assert (results[0].row_dir / "metrics.log").exists()
```

- [ ] **Step 2: Run them and confirm they fail**

Run: `cd llmbench && uv run pytest tests/benchmark/test_cli.py tests/benchmark/agent/test_runner.py -q -o addopts="--tb=no"`
Expected: `3 failed, 28 passed`. The failures are `test_run_takes_the_workload_from_the_environment[llmbench]`, `…[llmbench-wins]` and `test_row_dir_has_a_metrics_log_under_debug[LLMBENCH_AGENT_DEBUG]`: the new names are not read yet. The alias cases pass already, because the old names are what the code reads today.

- [ ] **Step 3: Implement**

`llmbench/src/llmbench/_env.py`:

```python
"""Environment variables that were renamed when llmbench left modelman."""

from __future__ import annotations

import os


def env_first(*names: str) -> str | None:
    """The value of the first of `names` that is set and not empty.

    Callers list the LLMBENCH_ name first and the MODELMAN_ name it replaced
    second, so the old name keeps working and the new one wins when both are
    set.
    """
    for name in names:
        value = os.environ.get(name)
        if value:
            return value
    return None
```

In `llmbench/src/llmbench/benchmark/cli.py`, replace the imports (`os` has no other use in this file):

```python
import os
from pathlib import Path

import typer

from llmbench.benchmark.agent.cli import agent_app
```

with:

```python
from pathlib import Path

import typer

from llmbench._env import env_first
from llmbench.benchmark.agent.cli import agent_app
```

and line 68:

```python
    workload_name = os.environ.get("MODELMAN_BENCHMARK_WORKLOAD", workload)
```

with:

```python
    workload_name = env_first("LLMBENCH_WORKLOAD", "MODELMAN_BENCHMARK_WORKLOAD") or workload
```

In `llmbench/src/llmbench/benchmark/agent/runner.py`, add the import above line 18 (`os` stays: line 178 uses it):

```python
from llmbench._env import env_first
from llmbench.benchmark import isolation
```

and replace line 194:

```python
            if os.environ.get("MODELMAN_AGENT_DEBUG")
```

with:

```python
            if env_first("LLMBENCH_AGENT_DEBUG", "MODELMAN_AGENT_DEBUG")
```

- [ ] **Step 4: Run the tests and the gate**

Run: `cd llmbench && uv run pytest tests/benchmark/test_cli.py tests/benchmark/agent/test_runner.py -q -o addopts=""`
Expected: `31 passed`.

Run: `cd llmbench && make check && make test`
Expected: `Success: no issues found in 59 source files`, then `642 passed`, coverage `92.34%`.

- [ ] **Step 5: Commit**

```bash
git add llmbench/src/llmbench/_env.py llmbench/src/llmbench/benchmark/cli.py llmbench/src/llmbench/benchmark/agent/runner.py llmbench/tests/benchmark/test_cli.py llmbench/tests/benchmark/agent/test_runner.py
git commit -m "feat(llmbench): LLMBENCH_WORKLOAD and LLMBENCH_AGENT_DEBUG, old names kept as aliases"
```

---

### Task 6: One `ProcessResult` and one bridge-exception hierarchy

After Task 1 there are two copies of each: modelman's own, and llmbench's. An `except wt_bridge.WtBridgeError` in modelman does not catch llmbench's `WtNotFoundError`, so a missing `wt` during `modelman start` of a keyed omlx model would surface as a traceback.

Five constants also exist twice after Task 1 and stay that way, because modelman is frozen: `DEFAULT_PROVIDER_IDS`, `ENV_VAR_BY_PROVIDER`, `MTPLX_PORT`, the `omlx-6bit` alias table and `WARM_TIMEOUT`. A backend added to one `DEFAULT_PROVIDER_IDS` and not the other is silently skipped by one of the two tools. This task does not unify them; it adds the test that fails when a pair differs.

**Files:**
- Create: `modelman/tests/test_llmbench_reexports.py`
- Modify: `modelman/src/modelman/local_process.py:13-25`, `modelman/src/modelman/wt_bridge.py:17-31`

**Interfaces:**
- Consumes: `llmbench.local_process.ProcessResult`; `llmbench.wt_bridge.WtBridgeError`, `WtNotFoundError`, `WtBridgeTimeoutError` (Task 1).
- Produces: `modelman.local_process.ProcessResult` and `modelman.wt_bridge.{WtBridgeError, WtNotFoundError, WtBridgeTimeoutError}` are those same objects; `test_duplicated_constants_agree` holds the five duplicated constants equal. `modelman.wt_bridge.WtRegistryRedirectedError` stays defined in modelman, as a subclass of the shared `WtBridgeError`.

- [ ] **Step 1: Write the failing tests**

`modelman/tests/test_llmbench_reexports.py`:

```python
"""modelman and llmbench share one ProcessResult and one bridge-exception
hierarchy.

llmbench owns the provider lifecycle modelman's start and stop call. A second
copy of either class would make `except wt_bridge.WtBridgeError` in modelman
miss a failure llmbench raised, and turn a missing `wt` into a traceback.
Deleted with modelman.
"""

import llmbench.local_process
import llmbench.providers.mtplx
import llmbench.providers.registry
import llmbench.registry
import llmbench.wt_bridge
import pytest

from modelman import local_process, wt_bridge
from modelman import registry as mm_registry
from modelman.providers import mtplx as mm_mtplx
from modelman.providers import registry as mm_providers


def test_process_result_is_llmbenchs_class():
    assert local_process.ProcessResult is llmbench.local_process.ProcessResult


@pytest.mark.parametrize("name", ["WtBridgeError", "WtNotFoundError", "WtBridgeTimeoutError"])
def test_bridge_exception_is_llmbenchs_class(name):
    assert getattr(wt_bridge, name) is getattr(llmbench.wt_bridge, name)


def test_modelman_catches_a_bridge_failure_llmbench_raised(monkeypatch):
    """The real path: llmbench's `wt warm` with no wt on PATH, caught by the
    name modelman's callers use."""
    monkeypatch.setattr(llmbench.wt_bridge.shutil, "which", lambda name: None)
    with pytest.raises(wt_bridge.WtBridgeError, match="wt not found on PATH"):
        llmbench.wt_bridge.warm("omlx", "Qwen-4bit")


def test_modelmans_own_bridge_errors_stay_in_the_one_hierarchy():
    assert issubclass(wt_bridge.WtRegistryRedirectedError, llmbench.wt_bridge.WtBridgeError)


def test_duplicated_constants_agree():
    """These are copies, not re-exports (modelman is frozen). `modelman sync`
    and the TUI seed provider rows from modelman's DEFAULT_PROVIDER_IDS while
    `llmbench run` filters on llmbench's: a backend added to one and not the
    other is silently skipped by one tool."""
    assert llmbench.registry.DEFAULT_PROVIDER_IDS == mm_registry.DEFAULT_PROVIDER_IDS
    assert llmbench.local_process.ENV_VAR_BY_PROVIDER == local_process.ENV_VAR_BY_PROVIDER
    assert llmbench.providers.mtplx.MTPLX_PORT == mm_mtplx.MTPLX_PORT
    assert llmbench.providers.registry._ALIASES == mm_providers._ALIASES
    assert llmbench.wt_bridge.WARM_TIMEOUT == wt_bridge.WARM_TIMEOUT
```

- [ ] **Step 2: Run them and confirm they fail**

Run: `cd modelman && uv run pytest tests/test_llmbench_reexports.py -q -o addopts="--tb=no"`
Expected: `6 failed, 1 passed`. The one that passes is `test_duplicated_constants_agree`: the copies agree today, and the test is there for the day one is edited alone.

- [ ] **Step 3: Re-export**

In `modelman/src/modelman/local_process.py`, replace:

```python
import json
import urllib.error
import urllib.request
from dataclasses import dataclass


@dataclass
class ProcessResult:
    provider: str
    model: str
    direct_url: str
    ok: bool
    error: str | None


# Provider ids whose lifecycle backend resolves its model from a single
```

with (`import X as X` is ruff's explicit re-export form; one blank line before the comment):

```python
import json
import urllib.error
import urllib.request

# One ProcessResult for both packages: the lifecycle that builds it lives in
# llmbench, and modelman's start/stop read what it returns.
from llmbench.local_process import ProcessResult as ProcessResult

# Provider ids whose lifecycle backend resolves its model from a single
```

In `modelman/src/modelman/wt_bridge.py`, replace:

```python
from dataclasses import dataclass
from pathlib import Path


class WtBridgeError(Exception):
    """wt could not process the request (nothing was changed)."""


class WtNotFoundError(WtBridgeError):
    """The wt binary is not on PATH."""


class WtBridgeTimeoutError(WtBridgeError):
    """wt timed out; a write it was doing (`sync`) may have already applied
    to config.yaml before the kill."""

```

with:

```python
from dataclasses import dataclass
from pathlib import Path

# One exception hierarchy for both packages: llmbench's lifecycle raises these
# from its own `wt warm` call, and modelman's callers catch them by these
# names. WtBridgeTimeoutError on a `sync` means the write may have already
# applied to config.yaml before the kill.
from llmbench.wt_bridge import WtBridgeError as WtBridgeError
from llmbench.wt_bridge import WtBridgeTimeoutError as WtBridgeTimeoutError
from llmbench.wt_bridge import WtNotFoundError as WtNotFoundError

```

modelman's own `ensure_wt`, `_msg` and `warm` stay as they are (modelman is frozen; its other bridge calls use the first two).

- [ ] **Step 4: Run the tests and the gate**

Run: `cd modelman && uv run pytest tests/test_llmbench_reexports.py -q -o addopts=""`
Expected: `7 passed`.

Run: `cd modelman && make check && make test`
Expected: `Success: no issues found in 43 source files`, then `1235 passed` (1222 + 6 parity tests from Task 3 + 7 here).

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/local_process.py modelman/src/modelman/wt_bridge.py modelman/tests/test_llmbench_reexports.py
git commit -m "refactor(modelman): re-export ProcessResult and the wt bridge errors from llmbench"
```

---

### Task 7: CI, the root Makefile, and the PR 1 gate

**Files:**
- Create: `.github/workflows/llmbench-ci.yml`
- Modify: `.github/workflows/modelman-ci.yml:7,13`, `Makefile:35-41`, `bin/check-config-dirs-untouched:4` (comment)

**Interfaces:**
- Consumes: `llmbench/Makefile`'s `check`, `test`, `install` targets (Task 1).
- Produces: `make test-all` and `make install` cover llmbench.

- [ ] **Step 1: Write the llmbench workflow**

`.github/workflows/llmbench-ci.yml`, modelled on `modelman-ci.yml`. It keeps the Linux run the spec asks for. The path filter adds what the suite reads outside its own directory: the task bundles under `benchmarks/tasks/` and the registry contract fixtures (Step 2 of the spec adds a second one, `registry.written.sample.toml`, which the glob already covers).

```yaml
name: llmbench-ci

on:
  push:
    branches: [main]
    paths:
      - "llmbench/**"
      - "benchmarks/tasks/**"
      - "docs/contracts/registry*.toml"
      - "bin/check-config-dirs-untouched"
      - ".github/workflows/llmbench-ci.yml"
  pull_request:
    paths:
      - "llmbench/**"
      - "benchmarks/tasks/**"
      - "docs/contracts/registry*.toml"
      - "bin/check-config-dirs-untouched"
      - ".github/workflows/llmbench-ci.yml"

jobs:
  test:
    runs-on: ubuntu-26.04
    defaults:
      run:
        working-directory: llmbench
    steps:
      - uses: actions/checkout@v7
      # setup-uv publishes no floating major tag past v7, so pin the exact release.
      - uses: astral-sh/setup-uv@v10.2.0
      - run: uv sync
      - run: make check
      - run: make test
      # No test may create the real config directories; see the script for
      # why. Run right after the tests, before any later step could create
      # them for its own reasons.
      - name: Tests left the real config directories alone
        run: ../bin/check-config-dirs-untouched
```

The script that last step runs names its callers in its header. In `bin/check-config-dirs-untouched`, replace line 4:

```bash
# CI runs this right after a test suite (modelman-ci, wt-ci), before any
```

with:

```bash
# CI runs this right after a test suite (llmbench-ci, modelman-ci, wt-ci), before any
```

- [ ] **Step 2: Make modelman-ci run on llmbench changes**

modelman's `start` and `stop` execute llmbench code, so a change there must run modelman's suite. In `.github/workflows/modelman-ci.yml`, add `- "llmbench/**"` under both `paths:` lists:

```yaml
on:
  push:
    branches: [main]
    paths:
      - "modelman/**"
      - "llmbench/**"
      - "docs/contracts/**"
      - "bin/check-config-dirs-untouched"
      - ".github/workflows/modelman-ci.yml"
  pull_request:
    paths:
      - "modelman/**"
      - "llmbench/**"
      - "docs/contracts/**"
      - "bin/check-config-dirs-untouched"
      - ".github/workflows/modelman-ci.yml"
```

Its `uv sync` step runs in `modelman/` and resolves `../llmbench` from the same checkout; no other change is needed.

- [ ] **Step 3: Add llmbench to the root Makefile**

In `Makefile`, replace:

```make
test-all: lint
	cd modelman && uv sync && make check && make test
	cd wt && go build ./... && go vet ./... && go test -count=1 ./...

install: ## Install all monorepo components (wt + modelman).
	cd wt && make install
	cd modelman && make install
```

with (llmbench first: modelman's environment installs it):

```make
test-all: lint
	cd llmbench && uv sync && make check && make test
	cd modelman && uv sync && make check && make test
	cd wt && go build ./... && go vet ./... && go test -count=1 ./...

install: ## Install all monorepo components (wt + llmbench + modelman).
	cd wt && make install
	cd llmbench && make install
	cd modelman && make install
```

- [ ] **Step 4: Run the whole gate**

```bash
make test-all
```

Expected, in order: `ALL LINKS OK` (after `lint-shell`, which now reads the edited `bin/check-config-dirs-untouched`); llmbench `642 passed` with `Total coverage: 92.34%`; modelman `1235 passed`; every `wt` package `ok`. About four minutes.

`bin/check-config-dirs-untouched` cannot be run locally (it fails on any machine that has a `~/.config/local-ai`). Check the same thing by hand, before and after the test run:

```bash
ls -la ~/.config/local-ai ~/.config/local-ai/benchmarks ~/.config/litellm 2>/dev/null | md5
```

Run it before `make test-all` and again after: the two hashes must match. (On Linux use `md5sum`.) If they differ, list the directories again and find the file: a change from a `wt` or `modelman` session the owner had open at the time is not yours, anything else is a test writing to the real home and must be fixed before the commit.

- [ ] **Step 5: Smoke-check both command trees**

These three commands read nothing and start nothing:

```bash
uv run --directory llmbench llmbench list-workloads
uv run --directory llmbench llmbench provider list
uv run --directory modelman modelman benchmark list-workloads
```

Expected: `chat`, `code`, `long`, `short` (one per line) from the first and third; six lines from the second, starting `llamacpp	[retired-only]` and ending with the `omlx-6bit` row.

- [ ] **Step 6: Commit**

```bash
git add .github/workflows/llmbench-ci.yml .github/workflows/modelman-ci.yml Makefile bin/check-config-dirs-untouched
git commit -m "ci: llmbench-ci, and llmbench in make install and make test-all"
git status --short
```

Expected: `git status --short` prints nothing.

- [ ] **Step 7: Stop and ask**

PR 1 is ready. Tell the owner what was verified (the counts above) and ask for the OK before `git push` or `gh pr create`. In the PR description, say that after this PR `modelman benchmark` and `modelman provider` are unchanged for users, that pointers move to `latest.toml` with a fallback read, and that the bash benchmark scripts and the docs switch in PR 2.

---

## PR 2: callers and docs

Start after PR 1 has merged:

```bash
git switch main && git pull --ff-only
git switch -c feat/llmbench-callers-docs
```

### Task 8: The benchmark scripts call `llmbench provider`

**Files:**
- Modify: `benchmarks/lib/benchmark-common.sh:17-18,21,51,70,74,77,85`, `benchmarks/qwen3.8-benchmark:136`, `benchmarks/ornith-1.5-benchmark:123`

**Interfaces:**
- Consumes: `llmbench provider isolate|restore` (PR 1).
- Produces: the shell array `LLMBENCH_PROVIDER` (was `MODELMAN_PROVIDER`), used by the three scripts.

- [ ] **Step 1: Enumerate the lines**

```bash
git grep -nE 'MODELMAN_PROVIDER|MODELMAN_DIR|modelman' -- benchmarks/lib benchmarks/qwen3.8-benchmark benchmarks/qwen3.8-benchmark-multi benchmarks/ornith-1.5-benchmark benchmarks/ornith-1.5-benchmark-multi
```

Expected: ten lines, eight in `benchmark-common.sh` (17, 18, 21, 51, 70, 74, 77, 85) and one each in `qwen3.8-benchmark:136` and `ornith-1.5-benchmark:123`.

- [ ] **Step 2: Switch them**

```bash
perl -pi -e 's/MODELMAN_PROVIDER/LLMBENCH_PROVIDER/g; s/MODELMAN_DIR/LLMBENCH_DIR/g; s{\.\./\.\./modelman"}{../../llmbench"}; s/modelman provider/llmbench provider/g' benchmarks/lib/benchmark-common.sh benchmarks/qwen3.8-benchmark benchmarks/ornith-1.5-benchmark
```

| Pattern | Before | After |
|---|---|---|
| the directory | `MODELMAN_DIR="$(cd "$BENCHMARK_LIB_DIR/../../modelman" && pwd)"` | `LLMBENCH_DIR="$(cd "$BENCHMARK_LIB_DIR/../../llmbench" && pwd)"` |
| the command | `MODELMAN_PROVIDER=(uv run --directory "$MODELMAN_DIR" modelman provider)` | `LLMBENCH_PROVIDER=(uv run --directory "$LLMBENCH_DIR" llmbench provider)` |
| a call | `"${MODELMAN_PROVIDER[@]}" restore` | `"${LLMBENCH_PROVIDER[@]}" restore` |
| a message or comment | `(calls modelman provider via uv run)` | `(calls llmbench provider via uv run)` |

- [ ] **Step 3: Verify**

```bash
git grep -nE 'MODELMAN_|modelman' -- benchmarks/lib benchmarks/qwen3.8-benchmark benchmarks/ornith-1.5-benchmark
make lint-shell
```

Expected: the grep prints nothing; `make lint-shell` ends without an error.

Check that the command the array now holds runs, using the one provider command that touches nothing:

```bash
grep -n 'LLMBENCH_DIR=\|LLMBENCH_PROVIDER=' benchmarks/lib/benchmark-common.sh
uv run --directory llmbench llmbench provider list
```

Expected: lines 17 and 18 as in the table above, then the six `provider list` lines. Do not run the benchmark scripts: they isolate live providers. The owner runs `./benchmarks/qwen3.8-benchmark 30` when the machine is free.

- [ ] **Step 4: Commit**

```bash
git add benchmarks/lib/benchmark-common.sh benchmarks/qwen3.8-benchmark benchmarks/ornith-1.5-benchmark
git commit -m "refactor(benchmarks): the bash scripts isolate through llmbench provider"
```

---

### Task 9: `llmbench/CLAUDE.md`, and pointers where modelman's sections were

**Files:**
- Create: `llmbench/CLAUDE.md`
- Modify: `llmbench/README.md:9`, `modelman/CLAUDE.md:20-21,24,29,107-114,129-137`, `modelman/README.md:378,423`, `modelman/docs/internals/providers.md:16-21`, `.claude/skills/adding-a-benchmark-backend/SKILL.md` (whole file)

**Interfaces:**
- Consumes: everything PR 1 produced; this task describes it.
- Produces: `llmbench/CLAUDE.md`, which Task 10's guide edits link to by name.

- [ ] **Step 1: Write `llmbench/CLAUDE.md`**

It takes the "Provider lifecycle" and "Benchmark subsystem" sections from `modelman/CLAUDE.md` (lines 107-114 and 129-137) and the lifecycle section of `modelman/docs/internals/providers.md` (lines 16-21), with the paths and names of the new package, and adds what PR 1 introduced.

```markdown
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

Run it from this directory (`uv run llmbench ...`) or from the repo root (`uv run --directory llmbench llmbench ...`). It is not installed globally.

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
| Registry (read-only) | `~/.config/local-ai/registry.toml` | `MODELMAN_REGISTRY`, then `XDG_CONFIG_HOME` |
| Results | `~/.config/local-ai/benchmarks/<run-id>/` | `--results-dir` |
| Latest-run pointers | `~/.config/local-ai/benchmarks/latest.toml` | `LLMBENCH_LATEST` |
| OpenRouter key | `OPENROUTER_API_KEY`, else the LiteLLM LaunchAgent plist | |
| LiteLLM url and key | `~/.pi/agent/models.json` | |

- `registry.py` is a reader for the fields the benchmarks use (model `id`, `family`, `provider_id`, `model_name`, `location`, `fetch`, `draft`; provider `id`, `location`). It accepts unknown top-level keys, because it never writes. **`registry_read_path()` must resolve the same file as modelman's `_registry_read_path()`** (the pre-XDG fallback and the dangling-symlink refusal included) until modelman is retired; `../modelman/tests/test_registry_path_parity.py` pins it.
- `state.py` holds the four pointers (`last_run`, `last_run_dir`, `agent_last_run`, `eval_last_run`). While `latest.toml` does not exist it reads them from the `[benchmarks]` table of modelman.toml (`MODELMAN_STATE`, then `XDG_CONFIG_HOME`); the first save writes `latest.toml` and the old table is not read again. A corrupt `latest.toml` reads as no pointers.
- Renamed variables keep their old names as aliases, read second (`_env.py::env_first`): `LLMBENCH_WORKLOAD` (`MODELMAN_BENCHMARK_WORKLOAD`), `LLMBENCH_AGENT_DEBUG` (`MODELMAN_AGENT_DEBUG`). The `LLM_ISOLATE_*` names are unchanged.

## Architecture

### Provider lifecycle (`src/llmbench/providers/lifecycle/`)

Isolate/stop/stop-all/restore for local providers (ported from bash, issue #79). `orchestrate.py` holds the operations; `backends/` has one backend per provider, registered in `backends/__init__.py`'s `BACKENDS`, with `SUPPORTED_PROVIDER_IDS` (`ollama`, `omlx`, `omlx-6bit`, `mlx_lm_server`, `mtplx`) excluding the retired-only `llamacpp`. mtplx and mlx_lm_server subclass `PidfileTrackedBackend` (`backends/base.py`); `pidproc.py`'s `PidfileProcess` is the spawn/stop/log-tail primitive; `probe.py`/`launchd.py`/`binaries.py` are shared primitives; `cli.py` is `llmbench provider ...`. `src/llmbench/local_process.py` is the neutral home for process/probe types shared with `benchmark/isolation.py` (living under either package would make the other's import backwards).

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
- `main.py` mounts `benchmark_app` with no name, so its commands sit at the top level. modelman mounts the same app as `benchmark`; do not add `provider` to `benchmark_app` itself, or `modelman benchmark provider` appears.

## Testing patterns

- **`tests/conftest.py` points `HOME` at a scratch directory before llmbench is imported.** The package computes its machine-level paths from `Path.home()` at import time and binds several as default arguments, so this is the only redirect that reaches all of them: the LaunchAgent plist, `~/.pi/agent/models.json`, the results directory, the pointer file. `tests/test_conftest_guards.py` fails if a path escapes. Do not import llmbench from a pytest plugin or a `-p` module: conftest raises a usage error when it finds llmbench already imported.
- **Autouse guards** in the same file: `subprocess.run` is wrapped so `launchctl`, `omlx`, `mtplx`, `ollama`, `wt` and `mlx_lm.*` never execute (everything else, such as git, runs for real); `urlopen` raises; `os.kill` is a no-op; the registry, the pointer file and modelman.toml are redirected into `tmp_path`. `tests/providers/lifecycle/test_hermeticity.py` pins the allow-list (modelman keeps a copy, `../modelman/tests/test_hermeticity.py`, for the wrapper in its own conftest).
- **Backend tests patch the name as imported into the backend module** (`patch("llmbench.providers.lifecycle.backends.mtplx.subprocess.run")`, `...backends.mtplx.probe.wait_for_port_closed`, `...backends.mtplx._PROC`), never the origin module.
- CLI tests use `typer.testing.CliRunner` against `llmbench.main.app` (`["run", ...]`, `["provider", ...]`) or a sub-app directly (`agent_app`, `eval_app`).
- Tests mirror modules: `tests/benchmark/` (incl. `test_routes.py`, `test_judge_core.py`), `tests/benchmark/agent/` (plus `fixtures/`), `tests/benchmark/eval/` (plus `test_rejudge.py`), `tests/providers/lifecycle/`. `tests/benchmark/agent/fixtures/tasks/` holds task bundles whose files are named `test_*.py` on purpose; `collect_ignore` keeps pytest out of them.
- A failing assertion must never print a credential: compare a boolean (`found = key is not None`), not the value.
- **Ruff B905 is enforced:** `zip()` needs explicit `strict=`. CI runs `ruff format --check` on `src/` and `tests/`; run `make format`.
- **Run `make check` before calling any task done**: mypy flags things pytest cannot.
```

- [ ] **Step 2: Rewrite the benchmark-backend skill**

Replace `.claude/skills/adding-a-benchmark-backend/SKILL.md` with:

```markdown
---
name: adding-a-benchmark-backend
description: Steps to add a new local-model backend to the benchmark scripts and llmbench. Use when asked to add a new benchmark backend or wire a new provider into the isolation/benchmark tooling.
---

## Adding a New Benchmark Backend

Isolation logic lives in **one place**: `llmbench/src/llmbench/providers/lifecycle/`
(issue #79 ported this from the old bash `bin/llm-isolate-provider`/
`bin/llm-restore-providers` scripts, both deleted). `orchestrate.py` drives
isolate/stop/stop-all/restore over a `BACKENDS` registry; each provider is
one `Backend` subclass in `backends/<id>.py`. The bash benchmark scripts
call the `llmbench provider` CLI over this
(`uv run --directory llmbench llmbench provider isolate <id> <model>`, with
`LLM_ISOLATE_*_MODEL` env overrides still honored as a fallback), and the
benchmarks call `orchestrate.py` directly, in-process, via
`llmbench/src/llmbench/benchmark/isolation.py`. `modelman start`/`stop` reach
the same code through modelman's path dependency on llmbench, so run
modelman's suite too. Paths below are relative to `llmbench/` unless they
start with another directory. Adding a backend:

1. **Add the backend module.** Create
   `src/llmbench/providers/lifecycle/backends/<id>.py` subclassing
   `Backend` (see `backends/base.py` for the interface, and
   `backends/mtplx.py` or `backends/ollama.py` for a worked example) —
   implement start/stop/warmup and set `id`, `occupancy_key`, `env_var`,
   `default_model`, `health_url`, and `restore_action` ("restart", "stop",
   or "skip"; see `backends/base.py`'s docstring for what each means).
2. **Register it.** Add the module's singleton instance to `BACKENDS` in
   `backends/__init__.py`, and — if it's a fully-supported (not
   retired-only) backend — add its id to `SUPPORTED_PROVIDER_IDS` in the
   same file. This is the one place both `llmbench provider isolate` and
   the benchmarks check isolability from
   (`llmbench.benchmark.isolation.SUPPORTED_PROVIDER_IDS` just re-exports
   it).
3. **Add an env var if it needs one.** If the backend resolves its model
   from a single env var (like ollama/omlx do), add it to
   `ENV_VAR_BY_PROVIDER` in `src/llmbench/local_process.py`. A
   backend that takes a target+draft pairing (like `mlx_lm_server`) uses
   its own two env vars defined in its own backend module instead — see
   `backends/mlx_lm_server.py`'s `TARGET_ENV_VAR`/`DRAFT_ENV_VAR`.
4. **Add a test file.** Model it on
   `tests/providers/lifecycle/backends/test_mtplx.py`. Follow this
   package's established `unittest.mock.patch` convention: patch the
   module-attribute *as imported into your backend module's own
   namespace* (e.g. `patch("llmbench.providers.lifecycle.backends.<id>.subprocess.run")`,
   `patch("llmbench.providers.lifecycle.backends.<id>.probe.wait_for_port_closed")`),
   not the origin module (`probe.py`, `pidproc.py`) directly. If the
   backend shells out to a binary the suite must never run, add its
   basename to `_FAKE_BINARIES` in `tests/conftest.py` **and** in
   `modelman/tests/conftest.py`, and to the parametrized list in
   `tests/providers/lifecycle/test_hermeticity.py` **and** its copy,
   `modelman/tests/test_hermeticity.py`.
5. **Update the registry.** Add a provider entry to
   `~/.config/local-ai/registry.toml` (via `modelman sync` or the TUI) and
   add the provider id to `DEFAULT_PROVIDER_IDS` in
   `src/llmbench/registry.py` (`LOCAL_PROVIDERS =
   set(DEFAULT_PROVIDER_IDS)` in `src/llmbench/benchmark/runner.py`) — a
   backend missing from that set is silently skipped by `llmbench run` —
   and, until modelman is retired, in `modelman/src/modelman/registry.py`,
   which `modelman sync` and the TUI seed provider rows from
   (`modelman/tests/test_llmbench_reexports.py` fails if the two differ).
6. **Update the drift trip-wire.** `tests/benchmark/test_isolation.py`'s
   `test_supported_provider_ids_matches_the_backends_registry_documented_list`
   hand-writes a literal copy of `SUPPORTED_PROVIDER_IDS` specifically so a
   backend added to `BACKENDS` without an isolability decision fails a
   test instead of silently running unisolated — update that literal
   alongside `backends/__init__.py`'s constant. `tests/test_main.py`'s
   `test_provider_list_names_every_backend` lists every backend id too.
7. **Wire up the benchmark scripts.** Add entries to `DIRECT_URLS`,
   `DIRECT_MODELS`, `LITELLM_MODELS`, and `ISOLATE_ID` (the associative
   array mapping the script's own backend key to the `llmbench provider`
   CLI's provider id — see `benchmarks/qwen3.8-benchmark` and
   `benchmarks/lib/benchmark-common.sh`'s `isolate_one`/
   `ensure_all_local_started`) in the benchmark script(s) you want it to
   appear in.
8. Add the model to `~/.config/litellm/config.yaml`.
9. **Smoke test:** `./benchmarks/qwen3.8-benchmark 30` and
   `uv run llmbench provider isolate <new-backend>` (from `llmbench/`, or
   `uv run --directory llmbench llmbench provider isolate <new-backend>`
   from the repo root).
10. Update the benchmark doc with new numbers.
```

Against the old file: every `modelman/src/modelman/…` path and `modelman.…` module name becomes its llmbench equivalent, `modelman provider` becomes `llmbench provider`, and three facts PR 1 created are added (step 4's allow-list in two conftests and two hermeticity tests, step 5's `DEFAULT_PROVIDER_IDS` in both packages, step 6's second literal list in `tests/test_main.py`).

- [ ] **Step 3: Point modelman's own docs at llmbench**

modelman's `CLAUDE.md`, `README.md` and `docs/internals/providers.md` describe code that has moved. The two sections become pointers, the command table says the two apps are mounted, and llmbench's README links its new `CLAUDE.md`. `modelman benchmark …` and `modelman provider …` stay in modelman's README as commands: they still work.

Save this as a scratch file outside the repository (for example `/tmp/llmbench-docs-sections.py`) and run it from the repository root with `python3 /tmp/llmbench-docs-sections.py`. Every edit requires its target text to occur exactly once. The script checks all of them in memory and writes only when every one matched, so a file that has drifted fails loudly with nothing half-edited, and the script can be re-run after the fix.

```python
"""PR 2, Task 9: modelman's own docs point at llmbench. Each target must occur exactly once."""
import pathlib
import re
import sys


texts: dict[str, str] = {}  # path -> edited text, written only if every edit matched
errors: list[str] = []


def load(path: str) -> str:
    if path not in texts:
        texts[path] = pathlib.Path(path).read_text(encoding="utf-8")
    return texts[path]


def replace_section(path: str, heading: str, body: str) -> None:
    """Replace `heading` and everything up to the next heading of the same
    or a higher level (or the end of the file) with `body`."""
    text = load(path)
    level = len(heading) - len(heading.lstrip("#"))
    if text.count(heading) != 1:
        errors.append(f"{path}: heading found {text.count(heading)} times, expected 1: {heading!r}")
        return
    start = text.find(heading)
    nxt = re.compile(rf"^#{{1,{level}}} ", re.M).search(text, start + len(heading))
    end = nxt.start() if nxt else len(text)
    texts[path] = text[:start] + body.rstrip("\n") + "\n" + ("\n" if nxt else "") + text[end:]


replace_section(
    "modelman/CLAUDE.md",
    "### Benchmark subsystem",
    "### Benchmark subsystem\n\n"
    "Moved to llmbench (`../llmbench/src/llmbench/benchmark/`): see `../llmbench/CLAUDE.md`, "
    "\"Benchmark subsystem\". `modelman benchmark ...` still runs it, mounted from llmbench in `main.py`; "
    "its tests live in `../llmbench/tests/benchmark/`.\n",
)
replace_section(
    "modelman/docs/internals/providers.md",
    "## Provider lifecycle (`src/modelman/providers/lifecycle/`)",
    "## Provider lifecycle\n\n"
    "Moved to llmbench (`../llmbench/src/llmbench/providers/lifecycle/`). Its module map, the `stop_all()` "
    "`keep` rule and the \"patch where it's used\" testing pattern are in "
    "[`llmbench/CLAUDE.md`](../../../llmbench/CLAUDE.md), \"Provider lifecycle\".\n",
)

EDITS = [
    # --- llmbench README: link the CLAUDE.md this task adds ---
    (
        'llmbench/README.md',
        'User guides: [throughput benchmarks]',
        'Commands, internals and test conventions: [CLAUDE.md](CLAUDE.md). User guides: [throughput benchmarks]',
    ),
    # --- modelman/CLAUDE.md: the commands are mounted, the lifecycle moved ---
    (
        'modelman/CLAUDE.md',
        '| `provider isolate\\|stop\\|stop-all\\|restore\\|list` | Low-level per-provider lifecycle (`providers/lifecycle/cli.py`) |\n| `benchmark ...` | `benchmark/cli.py`, plus `benchmark agent` / `benchmark eval` sub-apps |\n',
        "| `provider isolate\\|stop\\|stop-all\\|restore\\|list` | llmbench's `provider_app`, mounted here until modelman is retired; prefer `llmbench provider ...` |\n| `benchmark ...` | llmbench's `benchmark_app`, mounted here until modelman is retired; prefer `llmbench ...` |\n",
    ),
    (
        'modelman/CLAUDE.md',
        'Sub-Typer apps mounted in `main.py`: `benchmark`, `usage`, `provider`, `litellm`.',
        'Sub-Typer apps mounted in `main.py`: `usage`, `litellm`, and from llmbench `benchmark` and `provider`.',
    ),
    (
        'modelman/CLAUDE.md',
        '- `modelman benchmark` and `local_control.py` call `providers/lifecycle/orchestrate.py` in-process (issue #79). Nothing in modelman shells out to `bin/`.',
        "- **`../llmbench/` owns the benchmarks and the provider lifecycle** (carved out 2026-10; modelman has an editable path dependency on it, `[tool.uv.sources]` in `pyproject.toml`). `local_control.py` calls `llmbench.benchmark.isolation` and `llmbench.providers.lifecycle` in-process (issue #79); `local_process.ProcessResult` and `wt_bridge`'s `WtBridgeError`/`WtNotFoundError`/`WtBridgeTimeoutError` are re-exports of llmbench's classes; `tests/test_registry_path_parity.py` keeps the two registry readers on one file. Change that code in `../llmbench/` (see `../llmbench/CLAUDE.md`), then run both suites. Nothing in modelman shells out to `bin/`.",
    ),
    (
        'modelman/CLAUDE.md',
        '### Provider lifecycle (`src/modelman/providers/lifecycle/`)\n\nIsolate/stop/stop-all/restore for local providers. `orchestrate.py` holds the operations; `backends/` has one backend per provider (`BACKENDS`), with `SUPPORTED_PROVIDER_IDS` excluding retired `llamacpp`; `cli.py` is `modelman provider ...`.\n\n- **Backend tests patch the name as imported into the backend module** (`patch("modelman.providers.lifecycle.backends.mtplx.subprocess.run")`).\n- `stop_all()`\'s `keep` takes a provider id, resolved to its `occupancy_key`.\n\nModule map and primitives: `docs/internals/providers.md`.\n',
        '### Provider lifecycle\n\nMoved to llmbench (`../llmbench/src/llmbench/providers/lifecycle/`): see `../llmbench/CLAUDE.md`, "Provider lifecycle". modelman\'s tests patch it under its new name (`patch("llmbench.providers.lifecycle.stop")`), and `tests/conftest.py`\'s autouse guards patch `llmbench.providers.lifecycle.*`.\n',
    ),
    # --- modelman/README.md ---
    (
        'modelman/README.md',
        '- `src/modelman/providers/lifecycle/` — start/stop/isolate/restore of local provider servers (`modelman provider ...`); `src/modelman/local_control.py` — `modelman start`/`stop` and the TUI `s` key.',
        '- `../llmbench/src/llmbench/providers/lifecycle/` — start/stop/isolate/restore of local provider servers (`llmbench provider ...`, still mounted as `modelman provider ...`); it moved to the `llmbench` package, which modelman depends on. `src/modelman/local_control.py` — `modelman start`/`stop` and the TUI `s` key.',
    ),
    (
        'modelman/README.md',
        'Compare local model backends side-by-side:\n',
        'Compare local model backends side-by-side. The benchmarks now live in the `llmbench` package (`../llmbench/`); `modelman benchmark ...` still runs them, and `uv run --directory llmbench llmbench ...` is the same commands without the `benchmark` word:\n',
    ),
]
for path, old, new in EDITS:
    text = load(path)
    if text.count(old) != 1:
        errors.append(f"{path}: expected exactly 1 match, found {text.count(old)}: {old[:70]!r}")
        continue
    texts[path] = text.replace(old, new)
if errors:
    sys.exit("\n".join(errors) + "\nnothing was written")
for path, text in texts.items():
    pathlib.Path(path).write_text(text, encoding="utf-8")
print("replaced 2 sections, applied", len(EDITS), "edits")
```

Expected: `replaced 2 sections, applied 7 edits`. On a mismatch the script lists every one, ends with `nothing was written` and exits 1: fix the entry it names against the file's current text and run it again.

- [ ] **Step 4: Verify and commit**

```bash
git add llmbench/CLAUDE.md
make check-links
git grep -n 'src/modelman/providers/lifecycle\|src/modelman/benchmark' -- modelman/CLAUDE.md modelman/README.md modelman/docs/internals
```

Expected: `ALL LINKS OK` (`check-links` reads `git ls-files`, so the new file must be staged first), and the grep prints nothing.

```bash
git add llmbench/CLAUDE.md llmbench/README.md modelman/CLAUDE.md modelman/README.md modelman/docs/internals/providers.md
git add -u .claude/skills/adding-a-benchmark-backend/SKILL.md
git commit -m "docs: llmbench/CLAUDE.md takes the lifecycle and benchmark sections from modelman"
```

(`.claude/` is ignored by a global gitignore on the owner's machine; `git add -u` stages a change to a file that is already tracked.)

---

### Task 10: The guides, the reference and the root docs use the `llmbench` spellings

**Files:**
- Modify: `docs/guides/00-config-map.md`, `01-initial-setup.md`, `02-providers-and-models.md`, `04-litellm-config.md`, `05-benchmarks.md`, `08-maintenance-and-troubleshooting.md`, `09-agent-benchmarks.md`, `10-mlx-lm-quantization.md`, `11-capability-eval-benchmark.md`; `docs/reference/provider-artifacts.md`; `README.md`; `CLAUDE.md`; `wt/CLAUDE.md`; `benchmarks/README.md`; `.claude/skills/mlx-lm-quantization/SKILL.md`; `.gitignore`

**Interfaces:**
- Consumes: `llmbench/CLAUDE.md` (Task 9).
- Produces: no guide, reference page or root doc names `modelman benchmark`, `modelman provider` or a path that moved, and every component inventory (root `README.md`, root `CLAUDE.md`, `wt/CLAUDE.md`) lists llmbench. Historical records (`docs/superpowers/`, `modelman/docs/superpowers/`, `wt/docs/superpowers/`, `docs/archive/`) are not edited.

- [ ] **Step 1: Enumerate the lines**

```bash
git grep -nE 'modelman benchmark|modelman provider|src/modelman/(benchmark|providers/lifecycle)|modelman\.(benchmark|providers\.lifecycle)|MODELMAN_BENCHMARK_WORKLOAD|MODELMAN_AGENT_DEBUG' -- docs/guides docs/reference README.md CLAUDE.md benchmarks/README.md .claude/skills/mlx-lm-quantization .gitignore | wc -l
git grep -cE 'modelman benchmark|modelman provider|src/modelman/(benchmark|providers/lifecycle)|modelman\.(benchmark|providers\.lifecycle)|MODELMAN_BENCHMARK_WORKLOAD|MODELMAN_AGENT_DEBUG' -- docs/guides docs/reference README.md CLAUDE.md benchmarks/README.md .claude/skills/mlx-lm-quantization .gitignore
```

Expected: `106`, spread as `05-benchmarks.md:30`, `09-agent-benchmarks.md:15`, `provider-artifacts.md:15`, `10-mlx-lm-quantization.md:10`, `11-capability-eval-benchmark.md:10`, `CLAUDE.md:10`, `08-maintenance-and-troubleshooting.md:4`, `01-initial-setup.md:3`, `README.md:3`, and one each in `00-config-map.md`, `02-providers-and-models.md`, `04-litellm-config.md`, `benchmarks/README.md`, the `mlx-lm-quantization` skill and `.gitignore`. The three benchmark guides also name the working directory:

```bash
git grep -c 'local-ai-setup/modelman' -- docs/guides/05-benchmarks.md docs/guides/09-agent-benchmarks.md docs/guides/11-capability-eval-benchmark.md
```

Expected: `12`, `1`, `1`.

- [ ] **Step 2: Run the sweep**

Run from the repository root (the heredoc keeps zsh from touching the perl):

````bash
bash <<'SWEEP'
set -euo pipefail
perl -pi -e '
  s/uv run --directory modelman modelman provider/uv run --directory llmbench llmbench provider/g;
  s/modelman benchmark (agent|eval)\b/llmbench $1/g;
  s/modelman benchmark\b/llmbench/g;
  s/modelman provider\b/llmbench provider/g;
  s{modelman/src/modelman/(benchmark|providers/lifecycle)}{llmbench/src/llmbench/$1}g;
  s{src/modelman/(benchmark|providers/lifecycle)}{src/llmbench/$1}g;
  s{modelman\.(benchmark|providers\.lifecycle)\b}{llmbench.$1}g;
  s/MODELMAN_BENCHMARK_WORKLOAD/LLMBENCH_WORKLOAD/g;
  s/MODELMAN_AGENT_DEBUG/LLMBENCH_AGENT_DEBUG/g;
' \
  docs/guides/00-config-map.md \
  docs/guides/01-initial-setup.md \
  docs/guides/02-providers-and-models.md \
  docs/guides/04-litellm-config.md \
  docs/guides/05-benchmarks.md \
  docs/guides/08-maintenance-and-troubleshooting.md \
  docs/guides/09-agent-benchmarks.md \
  docs/guides/10-mlx-lm-quantization.md \
  docs/guides/11-capability-eval-benchmark.md \
  docs/reference/provider-artifacts.md \
  README.md \
  CLAUDE.md \
  benchmarks/README.md \
  .claude/skills/mlx-lm-quantization/SKILL.md \
  .gitignore
# The three benchmark guides run their commands from the package directory.
perl -pi -e '
  s{local-ai-setup/modelman(?![/\w-])}{local-ai-setup/llmbench}g;
  s/uv run modelman …/uv run llmbench …/g;
' docs/guides/05-benchmarks.md docs/guides/09-agent-benchmarks.md docs/guides/11-capability-eval-benchmark.md
SWEEP
````

Each rule, with a real line it changes:

| Rule | Before | After |
|---|---|---|
| 1 | `uv run --directory modelman modelman provider restore` | `uv run --directory llmbench llmbench provider restore` |
| 2 | `uv run modelman benchmark agent show --latest` | `uv run llmbench agent show --latest` |
| 3 | `uv run modelman benchmark run --family <family> --passes 3` | `uv run llmbench run --family <family> --passes 3` |
| 4 | `uv run modelman provider isolate omlx --json` | `uv run llmbench provider isolate omlx --json` |
| 5 | `` `modelman/src/modelman/providers/lifecycle/pidproc.py` `` | `` `llmbench/src/llmbench/providers/lifecycle/pidproc.py` `` |
| 6 | `` `src/modelman/benchmark/cli.py` `` | `` `src/llmbench/benchmark/cli.py` `` |
| 7 | `` (`modelman.benchmark.isolation.SUPPORTED_PROVIDER_IDS` `` | `` (`llmbench.benchmark.isolation.SUPPORTED_PROVIDER_IDS` `` |
| 8 | `` `MODELMAN_BENCHMARK_WORKLOAD` envvar overrides `` | `` `LLMBENCH_WORKLOAD` envvar overrides `` |
| 9 | ``Set `MODELMAN_AGENT_DEBUG=1` to also get `metrics.log` `` | ``Set `LLMBENCH_AGENT_DEBUG=1` to also get `metrics.log` `` |
| 10 (three guides) | `# from: /Users/keith/github/ohanaverse/local-ai-setup/modelman` | `# from: /Users/keith/github/ohanaverse/local-ai-setup/llmbench` |
| 11 (three guides) | `` `uv run modelman …` `` | `` `uv run llmbench …` `` |

Rule 10 leaves `local-ai-setup/modelman/docs/…` alone (guide 05's link to the benchmark design spec, which stays where it is until Step 6). It runs over the three benchmark guides only, because the `# from: …/modelman` lines in guides 01, 02, 03, 07 and 08 precede real modelman commands. One block is the exception: guide 08's `provider restore`, whose command rule 4 has just rewritten. Step 3 moves its working directory. The relative paths in the guides' commands (`--suite ../benchmarks/suites/smoke.toml`, `--root ../benchmarks/tasks/eval`) need no change: `llmbench/` is a sibling of `modelman/`.

- [ ] **Step 3: Make the prose edits the sweep cannot**

The sweep rewrites spellings. These edits change statements: who runs, where the pointers live, who owns what, which components exist. Save this as a scratch file outside the repository (for example `/tmp/llmbench-docs-edits.py`) and run it from the repository root with `python3 /tmp/llmbench-docs-edits.py`. Each entry is (file, exact text before, exact text after), and each "before" must occur exactly once. As in Task 9, every entry is checked in memory first and nothing is written unless all of them matched. The spec flags PR #165 as touching root `CLAUDE.md`, the file 15 of these entries match against: if it has merged, expect mismatches there and fix those entries against the new text.

```python
"""PR 2 prose edits the sweep cannot make. Each `old` must occur exactly once."""
import pathlib
import sys

EDITS = [
    # --- guide 00: the pointer file, and the registry's new reader ---
    (
        'docs/guides/00-config-map.md',
        '| `~/.config/local-ai/registry.toml` | `modelman` (TUI add/edit, `modelman migrate`) | `wt` (read-only; source of the LiteLLM routes) | Canonical providers + models |\n',
        '| `~/.config/local-ai/registry.toml` | `modelman` (TUI add/edit, `modelman migrate`) | `wt` (read-only; source of the LiteLLM routes), `llmbench` (read-only) | Canonical providers + models |\n',
    ),
    (
        'docs/guides/00-config-map.md',
        '| `~/.config/local-ai/settings.yaml` | `modelman` | `modelman` | User preferences (theme) |\n',
        '| `~/.config/local-ai/benchmarks/latest.toml` | `llmbench` | `llmbench` | Latest-run pointers behind `--latest`; the run directories sit beside it |\n| `~/.config/local-ai/settings.yaml` | `modelman` | `modelman` | User preferences (theme) |\n',
    ),
    (
        'docs/guides/00-config-map.md',
        '- **Env override:** `MODELMAN_STATE`.\n',
        '- **Env override:** `MODELMAN_STATE`.\n- **Benchmark pointers moved out.** The `[benchmarks]` table (`last_run`, `last_run_dir`, `agent_last_run`, `eval_last_run`) is no longer written. `llmbench` keeps those four keys in `~/.config/local-ai/benchmarks/latest.toml` (override: `LLMBENCH_LATEST`) and reads the old table only while that file does not exist yet.\n',
    ),
    # --- guide 05: who runs, and where the pointers live ---
    (
        'docs/guides/05-benchmarks.md',
        '- modelman runnable from its repo (`uv run llmbench …` from `/Users/keith/github/ohanaverse/local-ai-setup/llmbench`; modelman is not installed globally).',
        '- llmbench runnable from its directory (`uv run llmbench …` from `/Users/keith/github/ohanaverse/local-ai-setup/llmbench`; llmbench is not installed globally).',
    ),
    (
        'docs/guides/05-benchmarks.md',
        '- `--workload <name>` — default `chat`; `LLMBENCH_WORKLOAD` envvar overrides.',
        '- `--workload <name>` — default `chat`; `LLMBENCH_WORKLOAD` envvar overrides (its old name, `MODELMAN_BENCHMARK_WORKLOAD`, still works).',
    ),
    (
        'docs/guides/05-benchmarks.md',
        "Latest-run pointer: after a run, `cli.py` writes `benchmarks.last_run` / `benchmarks.last_run_dir` into `~/.config/local-ai/modelman.toml` (state file, not the registry). Until a CLI run has completed, `modelman.toml` has no `[benchmarks]` table (`grep -A2 '^\\[benchmarks\\]' ~/.config/local-ai/modelman.toml` prints nothing) and `--latest` errors as shown above.",
        'Latest-run pointer: after a run, `cli.py` writes `last_run` / `last_run_dir` into `~/.config/local-ai/benchmarks/latest.toml` (override: `LLMBENCH_LATEST`), beside the results. Until a run has completed there is no such file (`cat ~/.config/local-ai/benchmarks/latest.toml` fails) and `--latest` errors as shown above — unless modelman recorded a run earlier: while `latest.toml` is absent, the pointers are read from the `[benchmarks]` table of `~/.config/local-ai/modelman.toml`, and the next recorded run copies them into `latest.toml`.',
    ),
    (
        'docs/guides/05-benchmarks.md',
        'predate the modelman integration.',
        'predate the benchmark CLI.',
    ),
    (
        'docs/guides/05-benchmarks.md',
        "(this repo's `CLAUDE.md`). modelman enforces it internally",
        "(this repo's `CLAUDE.md`). llmbench enforces it internally",
    ),
    (
        'docs/guides/05-benchmarks.md',
        'warms the 6-bit variant. modelman always passes the provider id',
        'warms the 6-bit variant. llmbench always passes the provider id',
    ),
    (
        'docs/guides/05-benchmarks.md',
        '; modelman runs write under `/Users/keith/.config/local-ai/benchmarks/<run-id>/`',
        '; llmbench runs write under `/Users/keith/.config/local-ai/benchmarks/<run-id>/`',
    ),
    (
        'docs/guides/05-benchmarks.md',
        '- **Run modelman from the repo.** modelman is not installed globally. Always run it with',
        '- **Run llmbench from the repo.** llmbench is not installed globally. Always run it with',
    ),
    (
        'docs/guides/05-benchmarks.md',
        '- modelman source: `~/github/ohanaverse/local-ai-setup/llmbench/src/llmbench/benchmark/`',
        '- llmbench source: `~/github/ohanaverse/local-ai-setup/llmbench/src/llmbench/benchmark/`',
    ),
    (
        'docs/guides/05-benchmarks.md',
        "Ctrl-C still triggers modelman's restore.",
        "Ctrl-C still triggers llmbench's restore.",
    ),
    (
        'docs/guides/05-benchmarks.md',
        "which is what modelman's own benchmark adapter reads in-process",
        "which is what llmbench's own benchmark adapter reads in-process",
    ),
    # --- guide 08: the one restore block runs from llmbench/ ---
    (
        'docs/guides/08-maintenance-and-troubleshooting.md',
        '# from: /Users/keith/github/ohanaverse/local-ai-setup/modelman\nuv run llmbench provider restore\n',
        '# from: /Users/keith/github/ohanaverse/local-ai-setup/llmbench\nuv run llmbench provider restore\n',
    ),
    # --- guides 09, 10, 11: module map and working directory ---
    (
        'docs/guides/09-agent-benchmarks.md',
        '- Module map: `modelman/CLAUDE.md` (Benchmark subsystem)',
        '- Module map: `llmbench/CLAUDE.md` (Benchmark subsystem)',
    ),
    (
        'docs/guides/10-mlx-lm-quantization.md',
        '`llmbench provider` CLI runnable from `modelman/`).',
        '`llmbench provider` CLI runnable from `llmbench/`).',
    ),
    (
        'docs/guides/10-mlx-lm-quantization.md',
        '- Module map: `modelman/CLAUDE.md` (Provider plugin system, Benchmark subsystem)',
        '- Module map: `modelman/CLAUDE.md` (Provider plugin system), `llmbench/CLAUDE.md` (Provider lifecycle, Benchmark subsystem)',
    ),
    (
        'docs/guides/11-capability-eval-benchmark.md',
        'backends healthy, modelman runnable via `uv run`',
        'backends healthy, llmbench runnable via `uv run`',
    ),
    (
        'docs/guides/11-capability-eval-benchmark.md',
        '- `uv sync --extra eval` from `modelman/` —',
        '- `uv sync --extra eval` from `llmbench/` —',
    ),
    (
        'docs/guides/11-capability-eval-benchmark.md',
        '- Module map: `modelman/CLAUDE.md`\n',
        '- Module map: `llmbench/CLAUDE.md`\n',
    ),
    # --- provider-artifacts: DEFAULT_PROVIDER_IDS now lives in two readers ---
    (
        'docs/reference/provider-artifacts.md',
        '(`modelman/src/modelman/registry.py`), and the two litellm rows.',
        '(`modelman/src/modelman/registry.py` and `llmbench/src/llmbench/registry.py`),\nand the two litellm rows.',
    ),
    (
        'docs/reference/provider-artifacts.md',
        '   - `llamacpp` back in `DEFAULT_PROVIDER_IDS` (`modelman/src/modelman/registry.py`)\n',
        '   - `llamacpp` back in `DEFAULT_PROVIDER_IDS` (`modelman/src/modelman/registry.py`\n     and `llmbench/src/llmbench/registry.py`)\n',
    ),
    # --- root CLAUDE.md ---
    (
        'CLAUDE.md',
        '- Package-level context: `modelman/CLAUDE.md` (Python TUI/CLI) and `wt/CLAUDE.md` (Go worktree launcher) contain per-package commands, architecture, and gotchas.',
        '- Package-level context: `modelman/CLAUDE.md` (Python TUI/CLI), `llmbench/CLAUDE.md` (Python benchmarks + provider isolation) and `wt/CLAUDE.md` (Go worktree launcher) contain per-package commands, architecture, and gotchas.',
    ),
    (
        'CLAUDE.md',
        '- `llmbench agent run --suite <path>` — agentic coding benchmark',
        '- `uv run --directory llmbench llmbench agent run --suite <path>` — agentic coding benchmark',
    ),
    (
        'CLAUDE.md',
        '- `llmbench eval run --suite <path>` — cross-category capability benchmark',
        '- `uv run --directory llmbench llmbench eval run --suite <path>` — cross-category capability benchmark',
    ),
    (
        'CLAUDE.md',
        'stop others, start+warmup one (for `llmbench`; llamacpp is retired-only',
        'stop others, start+warmup one (for the benchmarks; llamacpp is retired-only',
    ),
    (
        'CLAUDE.md',
        'lint + modelman `make check`/`make test` + wt `go build`/`vet`/`test`',
        'lint + llmbench and modelman `make check`/`make test` + wt `go build`/`vet`/`test`',
    ),
    (
        'CLAUDE.md',
        '(wt binary + shims via `wt/make install`, modelman into its venv)',
        '(wt binary + shims via `wt/make install`, llmbench and modelman each into its own venv)',
    ),
    (
        'CLAUDE.md',
        'the CI step both modelman-ci and wt-ci run after their tests',
        'the CI step llmbench-ci, modelman-ci and wt-ci run after their tests',
    ),
    (
        'CLAUDE.md',
        '`llmbench` calls the orchestrator in-process (no subprocess, no PATH lookup); the benchmark scripts',
        'The benchmarks call the orchestrator in-process (no subprocess, no PATH lookup), as do `modelman start`/`stop`; the benchmark scripts',
    ),
    (
        'CLAUDE.md',
        '- `modelman/` — model registry TUI/CLI (Python/uv; `src/modelman`, own `CLAUDE.md`, own `Makefile`). Canonical owner of `registry.toml`, `modelman.toml` (download/ready/running state) and the `llmbench` tool. It never writes',
        '- `llmbench/` — the benchmarks and the provider isolation they need (Python/uv; `src/llmbench`, own `CLAUDE.md`, own `Makefile`), carved out of modelman. Three benchmarks under one package: throughput (`llmbench run`), the agentic coding benchmark (`benchmark/agent/`, `llmbench agent`) and the cross-category capability benchmark (`benchmark/eval/`, `llmbench eval`), plus `llmbench provider isolate|stop|stop-all|restore|list` (`providers/lifecycle/`). Reads `registry.toml` read-only; keeps its latest-run pointers in `~/.config/local-ai/benchmarks/latest.toml`. Imports nothing from modelman\n- `modelman/` — model registry TUI/CLI (Python/uv; `src/modelman`, own `CLAUDE.md`, own `Makefile`). Canonical owner of `registry.toml` and `modelman.toml` (download/ready/running state). It depends on `llmbench` (an editable path dependency) for the provider lifecycle its `start`/`stop` use, and still serves `modelman benchmark` and `modelman provider` from it until modelman is retired. It never writes',
    ),
    (
        'CLAUDE.md',
        ' (`make install`); the agentic coding benchmark (`benchmark/agent/`) and the cross-category capability benchmark (`benchmark/eval/`, `llmbench eval`) are separate module trees under the same package',
        ' (`make install`)',
    ),
    (
        'CLAUDE.md',
        '`test-all` (aggregates modelman + wt)',
        '`test-all` (aggregates llmbench + modelman + wt)',
    ),
    (
        'CLAUDE.md',
        'read by wt Go + modelman Python contract tests)',
        'read by wt Go + modelman Python contract tests; llmbench reads `registry.sample.toml`)',
    ),
    (
        'CLAUDE.md',
        'wt-ci (Go + wt lint), modelman-ci (Python)',
        'wt-ci (Go + wt lint), modelman-ci (Python; also runs on `llmbench/**`), llmbench-ci (Python)',
    ),
    (
        'CLAUDE.md',
        '# modelman (Python) — conftest.py autouse fixtures prevent live LiteLLM/ollama calls\ncd modelman && uv run pytest tests/test_routes_sync.py -q\n',
        '# modelman (Python) — conftest.py autouse fixtures prevent live LiteLLM/ollama calls\ncd modelman && uv run pytest tests/test_routes_sync.py -q\n\n# llmbench (Python) — conftest.py runs the suite under a scratch HOME with provider binaries stubbed\ncd llmbench && uv run pytest tests/providers/lifecycle/test_orchestrate.py -q\n',
    ),
    (
        'CLAUDE.md',
        'See `modelman/CLAUDE.md` and `wt/CLAUDE.md` for package-specific test patterns.',
        'See `modelman/CLAUDE.md`, `llmbench/CLAUDE.md` and `wt/CLAUDE.md` for package-specific test patterns.',
    ),
    # --- root README.md and wt/CLAUDE.md: the component inventories ---
    (
        'README.md',
        '| `modelman/` | model registry TUI/CLI — canonical source of truth for providers/models, download state, benchmarks, usage |\n',
        '| `modelman/` | model registry TUI/CLI — canonical source of truth for providers/models, download state, usage |\n| `llmbench/` | benchmarks (throughput, agent, eval) and the provider isolation they need (`llmbench` CLI) |\n',
    ),
    (
        'README.md',
        '├── modelman/           # model registry TUI/CLI (Python/uv) — has its own CLAUDE.md\n',
        '├── modelman/           # model registry TUI/CLI (Python/uv) — has its own CLAUDE.md\n├── llmbench/           # benchmarks + provider isolation (Python/uv) — has its own CLAUDE.md\n',
    ),
    (
        'README.md',
        'CI: shell-ci, wt-ci, modelman-ci',
        'CI: shell-ci, wt-ci, modelman-ci, llmbench-ci',
    ),
    (
        'README.md',
        '(read by wt Go + modelman Python tests)',
        '(read by wt Go + modelman and llmbench Python tests)',
    ),
    (
        'wt/CLAUDE.md',
        '(root lint + modelman + wt build/vet/test)',
        '(root lint + llmbench + modelman + wt build/vet/test)',
    ),
]

texts: dict[str, str] = {}  # path -> edited text, written only if every edit matched
errors: list[str] = []
for path, old, new in EDITS:
    if path not in texts:
        texts[path] = pathlib.Path(path).read_text(encoding="utf-8")
    text = texts[path]
    if text.count(old) != 1:
        errors.append(f"{path}: expected exactly 1 match, found {text.count(old)}: {old[:70]!r}")
        continue
    texts[path] = text.replace(old, new)
if errors:
    sys.exit("\n".join(errors) + "\nnothing was written")
for path, text in texts.items():
    pathlib.Path(path).write_text(text, encoding="utf-8")
print(f"applied {len(EDITS)} edits")
```

Expected: `applied 43 edits`. On a mismatch the script lists every one, ends with `nothing was written` and exits 1.

- [ ] **Step 4: Verify nothing was missed**

```bash
git grep -nE 'modelman benchmark|modelman provider|src/modelman/(benchmark|providers/lifecycle)|modelman\.(benchmark|providers\.lifecycle)|MODELMAN_BENCHMARK_WORKLOAD|MODELMAN_AGENT_DEBUG' -- docs/guides docs/reference README.md CLAUDE.md benchmarks/README.md .claude/skills/mlx-lm-quantization .gitignore | cut -c1-120
```

Expected: exactly two lines, both intended. `CLAUDE.md:32` says modelman "still serves `modelman benchmark` and `modelman provider`" from llmbench, and `docs/guides/05-benchmarks.md:100` names `MODELMAN_BENCHMARK_WORKLOAD` as the old name that still works.

```bash
git grep -n 'modelman' -- docs/guides/05-benchmarks.md docs/guides/09-agent-benchmarks.md docs/guides/11-capability-eval-benchmark.md | cut -c1-110
```

Expected: three lines, all in guide 05. Line 5 (the "Verified against: modelman 0.1.0 … on 2026-08-29" stamp, a dated record), line 138 (the fallback read of modelman.toml) and line 209 (the design spec's path under `modelman/docs/superpowers/`).

No guide block may run an `llmbench` command from `modelman/` (it would work only while modelman's venv has llmbench's console script, and break when modelman is deleted):

```bash
git grep -n -A1 'local-ai-setup/modelman$' -- docs/guides | grep 'uv run llmbench'
```

Expected: no output.

Read guide 05's Steps 1 to 4 once from top to bottom as a user would, and confirm every command is `uv run llmbench …` from `…/local-ai-setup/llmbench` and that no captured output was altered beyond the command name in its `Usage:` lines.

Across the whole repository, the old spellings now remain only where they are true or historical:

```bash
git grep -lE 'modelman benchmark|modelman provider' -- . ':!docs/superpowers' ':!modelman/docs/superpowers' ':!wt/docs/superpowers' ':!docs/archive'
```

Expected: `CLAUDE.md`, `llmbench/CLAUDE.md`, `modelman/CLAUDE.md`, `modelman/README.md`, `modelman/docs/internals/local-model-lifecycle.md`, `modelman/docs/internals/providers.md`, `modelman/src/modelman/local_control.py`, `modelman/tests/commands/test_llmbench_mounts.py`. Each describes modelman's mounts, which exist until Step 6 (`providers.md` matches only on its title, "modelman providers").

- [ ] **Step 5: Run the gates**

```bash
git add -u
make check-links
make test-all
git status --short | grep -v '^M ' ; true
```

Expected: `ALL LINKS OK`; `make test-all` as at the end of PR 1 (llmbench `642 passed`, modelman `1235 passed`, every wt package `ok`); the last command prints nothing (every change is a staged modification). Repeat Task 7 Step 4's before-and-after check of `~/.config`.

- [ ] **Step 6: Commit**

```bash
git commit -m "docs: guides, reference and root docs use the llmbench commands"
git status --short
```

Expected: `git status --short` prints nothing.

- [ ] **Step 7: Stop and ask**

PR 2 is ready. Tell the owner what was verified and what was not: the bash benchmark scripts were linted and their provider command resolved, but no benchmark was run. Ask for the OK before `git push` or `gh pr create`.
