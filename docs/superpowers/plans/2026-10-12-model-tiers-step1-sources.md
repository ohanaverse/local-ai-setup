# modeltiers Step 1: Skeleton, Inventory, Snapshots and Parsers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A new package, `modeltiers/`, whose `modeltiers run --dry-run [--offline] [--inventory FILE]` reads the wt inventory, downloads and parses models.dev, OpenRouter, Arena and Epoch AI through a snapshot store with fallback, and prints the inventory row count and each source's status and record count.

**Architecture:** One module per pipeline stage with frozen dataclasses in and out: `inventory.py` (wt JSON to rows), `fetch.py` (downloads behind a `Fetcher` seam, one retry), `snapshots.py` (per-source files, good and bad, pruning), `sources/*.py` (four pure parsers), `loader.py` (fetch, save, parse, fall back) and `cli.py` (argparse, a `Deps` dataclass that carries every seam). Nothing resolves a name, assigns a tier or writes `tiers.toml` in this step; steps 2 to 4 add those on the interfaces fixed here.

**Tech Stack:** Python `==3.13.*`, uv, hatchling, src layout. Runtime dependency: `tomli-w` (declared now, first imported in step 3). Standard library only otherwise: `argparse`, `urllib.request`, `json`, `csv`, `zipfile`, `subprocess`. Dev: pytest, pytest-cov, ruff, mypy (`strict = true` on `src/`).

**Spec:** docs/superpowers/specs/2026-10-12-model-tiers-design.md

**Verified against:** main at 51887c0, and a working prototype of this step. Every code block below was extracted from this file into a fresh clone and run in task order: each "Expected: FAIL" failed with the quoted message and each "Expected: PASS" passed. The four parsers were also run with their default minimums over the 2026-10-12 downloads (8,466 models.dev rows with 446 canonical keys, 284 OpenRouter records, 52 Arena rows, 274 Epoch records, the spec's numbers), and the finished command was run once against the live sources and the real `wt model list --json` with `MODELTIERS_CACHE` pointed at a scratch directory (all four `ok`, exit 0).

## Global Constraints

- Python is `==3.13.*`, uv-managed, src layout, console script `modeltiers = "modeltiers.cli:main"`.
- The only runtime dependency is `tomli-w`. The CLI uses `argparse`. Dev dependencies are pytest, pytest-cov, ruff and mypy.
- The tool never writes `registry.toml`, `config.yaml` or any wt state, and never reads `registry.toml`. The inventory comes only from `wt model list --json` or from `--inventory FILE`.
- wt is run only as `wt model list --json`. No other wt command appears in code, tests or steps.
- The join key for everything downstream is the pair (`provider_id`, `model_name`), never the registry `id`. Rows whose `model_name` is `native` are dropped. Discovered rows (`registered = false`) and rows whose weights are missing are kept.
- No test touches the network, the real config home or the real cache. The autouse `_hermetic` fixture makes `urllib.request.urlopen` and `subprocess.run` raise and points `MODELTIERS_OUT`, `MODELTIERS_CACHE`, `HOME`, `XDG_CONFIG_HOME` and `XDG_CACHE_HOME` under `tmp_path`.
- Every function that needs the network, the clock, a wait or wt takes it as a parameter: `Fetcher = Callable[[str], bytes]`, `Sleep = Callable[[float], None]`, `now: datetime`, `RunWt = Callable[[], str]`.
- A request times out after 30 seconds and is retried once after 2 seconds on a timeout, a 5xx status or an Arena `error` body. Nothing else is retried.
- Parser row minimums: models.dev 1,000, OpenRouter 100, Arena 20, Epoch 100. Each parser takes `min_rows` defaulting to its constant. A parser raises `SourceParseError` on a shape it does not recognise and never returns a short list.
- Snapshots: `<cache>/<source>/<UTC stamp>.<ext>`, stamp `%Y%m%dT%H%M%SZ`, saved before parsing. A snapshot that does not parse or is under its minimum is renamed `<UTC stamp>.bad.<ext>`, its path is printed, and it is never read again. Pruning keeps the four newest good and the two newest bad per source.
- Paths: output `MODELTIERS_OUT`, else `$XDG_CONFIG_HOME/local-ai/tiers.toml`, else `~/.config/local-ai/tiers.toml`; snapshots `MODELTIERS_CACHE`, else `$XDG_CACHE_HOME/local-ai/modeltiers/`, else `~/.cache/local-ai/modeltiers/`.
- Exit codes in this step: 0 every source `ok`, or `stale` because `--offline` was given; 1 inventory error, or a source failed with no good snapshot; 2 a source fell back to a snapshot. Exit 3 (sentinels) arrives in step 3.
- Quality bar: `make check` (ruff lint, `ruff format --check`, mypy) and `make test` (pytest, coverage floor 90), as in llmbench.
- Guides never embed live model state. This step adds no guide; every model name in `modeltiers/CLAUDE.md` is one from the test fixtures.
- **Not in this step:** `src/modeltiers/data/contracts.toml` and `aliases.toml`, `tokens.py`, `identity.py`, `assign.py`, `output.py`, `show`, `explain`, `docs/contracts/tiers.sample.toml`, `docs/guides/12-model-tiers.md`, the `docs/guides/00-config-map.md` rows. `snapshots.prune` exists and is tested; no command calls it until step 3 has a successful write to follow.
- **Chosen where the spec is silent** (each is pinned by a test named in the task):
  - Plain `modeltiers run` does exactly what `run --dry-run` does in this step and ends with the line `nothing written: this version stops after parsing the sources`. Step 3 removes the line and makes plain `run` write.
  - OpenRouter's minimum of 100 counts scored records (models with an `intelligence_index`), not models. A payload that keeps its 458 models and loses its benchmarks is then a failed parse and falls back, instead of silently removing a voting signal.
  - A models.dev row with no `limit.context` or no `canonical_model_id` is kept with `None`; a missing `tool_call` or `open_weights` reads as `false`. A wrong type (a string where a boolean belongs) is `SourceParseError`.
  - Arena's effort parenthesis is removed wherever it stands, not only at the end: the live data has `DeepSeek V4 Pro (High) (0813)`, which must become `DeepSeek V4 Pro (0813)` for the spec's alias to match.
  - Epoch records are keyed on the `Display name` column. A CSV row with no name or no `eci` value gives no record; an empty `eci_ci_low` reads as `None` (two rows on 2026-10-12).
  - The Arena snapshot is `{"pages": ["<page body>", ...]}`, each page a JSON string exactly as received.
  - When a source falls back, or under `--offline`, a snapshot not yet marked bad that fails to parse is renamed `.bad` and the next newest is tried. `--offline` therefore can rename a file; it still fetches, saves and prunes nothing.
  - All four sources are attempted even after one is found unavailable, so one run reports every failed source and still saves the good downloads.
  - `http_fetcher` sends `User-Agent: modeltiers/0.1`. A 4xx status, a refused connection and a DNS failure are not retried.
  - `wt model list --json` gets 60 seconds.
- Commits use the repo's conventional style (`feat(modeltiers): …`, lowercase after the type, no trailing period), with the attribution trailer your session is told to add, if any.
- Work on one feature branch off `main`, for example `feat/modeltiers-step1-sources`. **Pushing the branch and opening the pull request need the owner's OK.**
- **Never, in any step of this plan:** read or print `~/.config/local-ai/registry.toml` (it holds a secret); write under `~/.config` or `~/.cache`; run a wt command other than `wt model list --json`. The one optional live command (Task 10) sets `MODELTIERS_CACHE` to a scratch directory.
- Commands are run from the repo root unless a step says otherwise.

## Review Focus

1. **Arena answers HTTP 200 with a JSON body that has an `error` key** ("the dataset index is loading", seen during design), or answers with a real 5xx status such as 502. Expected: the page is fetched once more after 2 seconds; a second error is a failed download; the run uses Arena's newest good snapshot, prints `arena: stale` with that snapshot's time and exits 2; with no snapshot it exits 1 naming `arena`. An HTTP 200 whose body is not JSON (a gateway's "502 Bad Gateway" HTML page served with status 200) is a failed download at once and is not retried: only a timeout, a 5xx status and the `error` body are retryable. An `error` page inside a saved snapshot is a `SourceParseError`, never a short list. Pinned in Task 3 (`test_an_arena_error_body_is_retried_once`, `test_two_arena_error_bodies_are_a_failed_download`; `test_http_fetcher_marks_only_timeouts_and_5xx_retryable` and `test_a_retryable_failure_is_retried_once_after_two_seconds` for the 5xx status; `test_an_arena_page_in_another_shape_is_a_failed_download` for the 200 with a non-JSON body, whose fake fetcher holds one answer, so a second fetch would fail the test), Task 6 (`test_an_error_body_is_a_failed_download`, `test_fewer_rows_than_num_rows_total_raises`) and Task 8 (`test_a_source_that_falls_back_is_stale_and_exits_2`).
2. **A models.dev row with `limit.context` missing, with no `canonical_model_id`, or with a canonical key in mixed case** (`minimax/MiniMax-M3`). Expected: the row is kept; `context` and `canonical` are `None`; the key keeps models.dev's spelling exactly. One odd row must not turn 8,466 rows into a failed source. Pinned in Task 5 (`test_golden`, `test_a_row_without_limit_context_has_no_context`, `test_missing_flags_read_as_false`).
3. **An inventory whose provider probe is not `ok`, that holds native rows, discovered rows and rows with missing weights**, as the owner's does today (`mtplx` is `partial`). Expected: one warning line naming the provider and its status on stderr, the run continues and exits 0; native rows are dropped and counted; every other row is kept in wt's order. A document in another shape, a missing `--inventory` file, wt absent from PATH and wt exiting non-zero are each exit 1 before any fetch. Pinned in Task 2 (`test_a_provider_that_is_not_ok_is_named_in_a_warning`, `test_native_rows_are_dropped_and_counted`, `test_discovered_and_missing_rows_are_kept`, `test_wt_not_on_path_is_an_inventory_error`) and Task 8 (`test_dry_run_prints_each_source_and_the_inventory_count`, `test_an_inventory_error_exits_1_before_any_fetch`).
4. **An OpenRouter payload that is valid JSON with every model present and the benchmarks gone or null** (160 models have no `benchmarks` key and 14 have `artificial_analysis: null` today). Expected: such models give no record; when fewer than the minimum have a score the parse fails, the snapshot is renamed `.bad` with its path printed, and the run falls back. Pinned in Task 5 (`test_golden`, `test_a_null_intelligence_index_gives_no_record`, `test_too_few_rows_counts_scored_records`) and Task 7 (`test_a_snapshot_under_its_row_minimum_is_bad`).
5. **A cache directory with history: a snapshot an older version saved as good in a shape this parser refuses, a `.bad` file newer than every good one, and files that are not snapshots** (`.DS_Store`, a name that is not a date). Expected: the stray files are ignored; a `.bad` file is never read; a good-named file that fails to parse is renamed `.bad` and the next newest good one is used, online and under `--offline`; `fetched_at` is the used snapshot's own time, not this run's. Pinned in Task 4 (`test_files_that_are_not_snapshots_are_ignored`, `test_good_snapshots_never_include_a_bad_one`) and Task 7 (`test_offline_skips_a_bad_snapshot`, `test_a_good_snapshot_that_no_longer_parses_is_set_aside_and_the_next_is_used`, `test_a_failed_download_falls_back_to_the_newest_good_snapshot`).

## File Structure

Created:

| File | Responsibility |
|---|---|
| `modeltiers/pyproject.toml` | Package metadata, the one runtime dependency, the console script, pytest, coverage, ruff and mypy settings |
| `modeltiers/Makefile` | `install`, `test`, `lint`, `format-check`, `format`, `typecheck`, `check`, `all`, `clean`; the same targets as `llmbench/Makefile` |
| `modeltiers/.gitignore` | Virtualenv, caches, coverage data |
| `modeltiers/uv.lock` | Written by `uv sync`; committed |
| `modeltiers/CLAUDE.md` | Commands, pipeline, paths and test seams as they stand after this step |
| `modeltiers/src/modeltiers/__init__.py` | Package docstring |
| `modeltiers/src/modeltiers/errors.py` | `ModelTiersError` and its five subclasses |
| `modeltiers/src/modeltiers/paths.py` | `output_path`, `cache_dir`, the two override variable names |
| `modeltiers/src/modeltiers/records.py` | `InventoryRow`, `Inventory`, `ModelsDevRow`, `ModelsDev`, `ScoreRecord` |
| `modeltiers/src/modeltiers/inventory.py` | `run_wt`, `parse_inventory`, `load_inventory`, `provider_warnings` |
| `modeltiers/src/modeltiers/fetch.py` | `Fetcher`, `http_fetcher`, `fetch_with_retry`, one fetch function per source, Arena paging and its wrapper document |
| `modeltiers/src/modeltiers/snapshots.py` | `Snapshot`, `save`, `mark_bad`, `list_snapshots`, `good_snapshots`, `prune` |
| `modeltiers/src/modeltiers/sources/__init__.py` | Package docstring |
| `modeltiers/src/modeltiers/sources/_shape.py` | Shape checks the parsers share |
| `modeltiers/src/modeltiers/sources/modelsdev.py` | models.dev `api.json` to `ModelsDev` |
| `modeltiers/src/modeltiers/sources/openrouter.py` | OpenRouter models API to `ScoreRecord`s |
| `modeltiers/src/modeltiers/sources/arena.py` | Arena wrapper document to `ScoreRecord`s; `split_effort` |
| `modeltiers/src/modeltiers/sources/epoch.py` | Epoch ZIP to `ScoreRecord`s |
| `modeltiers/src/modeltiers/loader.py` | `SourceSpec`, `SourceSpecs`, `SourceResult`, `Sources`, `build_specs`, `load_source`, `load_all` |
| `modeltiers/src/modeltiers/cli.py` | `Deps`, `main`, the `run` subcommand, exit codes |
| `modeltiers/tests/conftest.py` | The autouse `_hermetic` guard; `read_fixture`, `epoch_zip`, `arena_doc`, `specs`, `good_answers` |
| `modeltiers/tests/fakes.py` | `FakeFetcher`, `Sleeps` |
| `modeltiers/tests/test_conftest_guards.py` | The guard holds |
| `modeltiers/tests/test_paths.py` | Defaults, XDG, overrides |
| `modeltiers/tests/test_inventory.py` | Inventory parsing, the wt runner, warnings |
| `modeltiers/tests/test_fetch.py` | Retry, Arena paging, `http_fetcher` failure classes |
| `modeltiers/tests/test_snapshots.py` | Save, bad, listing, pruning |
| `modeltiers/tests/test_source_modelsdev.py`, `test_source_openrouter.py`, `test_source_arena.py`, `test_source_epoch.py` | Golden, malformed and too-few-rows cases per parser |
| `modeltiers/tests/test_loader.py` | Fallback, stale, offline, unavailable |
| `modeltiers/tests/test_cli.py` | The printed report and exit codes 0, 1 and 2 |
| `modeltiers/tests/fixtures/inventory.json` | Seven rows of `wt model list --json`, one native |
| `modeltiers/tests/fixtures/modelsdev.json`, `modelsdev_too_few.json`, `modelsdev_malformed.json` | models.dev fixtures |
| `modeltiers/tests/fixtures/openrouter.json`, `openrouter_too_few.json`, `openrouter_malformed.json` | OpenRouter fixtures |
| `modeltiers/tests/fixtures/arena_page1.json`, `arena_page2.json`, `arena_too_few.json`, `arena_malformed.json`, `arena_error.json` | Arena page bodies |
| `modeltiers/tests/fixtures/eci_scores.csv`, `eci_scores_too_few.csv`, `eci_scores_malformed.csv` | Epoch CSVs; tests zip them in memory |
| `modeltiers/tests/fixtures/README.md` | Credits each source with its licence; states the files are trimmed copies |
| `.github/workflows/modeltiers-ci.yml` | `uv sync`, `make check`, `make test`, `bin/check-config-dirs-untouched` |

Modified:

| File | Change |
|---|---|
| `Makefile` (lines 39-44) | `test-all` and `install` call the package's `make check`, `make test` and `make install` |
| `bin/check-config-dirs-untouched` (line 4) | The comment names modeltiers-ci among its callers |
| `CLAUDE.md` (lines 6, 19, 23, 24, 30, 33, 34, 36) | Package-context list, a Commands line, the `test-all` and `install` lines, an Architecture bullet, the `Makefile` and workflows bullets |

---

### Task 1: Package skeleton, errors, paths and the hermetic test guard

**Files:**
- Create: `modeltiers/pyproject.toml`, `modeltiers/Makefile`, `modeltiers/.gitignore`, `modeltiers/uv.lock` (generated), `modeltiers/src/modeltiers/__init__.py`, `modeltiers/src/modeltiers/errors.py`, `modeltiers/src/modeltiers/paths.py`
- Test: `modeltiers/tests/conftest.py`, `modeltiers/tests/test_conftest_guards.py`, `modeltiers/tests/test_paths.py`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `modeltiers.errors`: `ModelTiersError(Exception)`; `ConfigError`, `InventoryError`, `SourceParseError` (plain subclasses); `FetchError(message: str, *, retryable: bool = False)` with attribute `retryable: bool`; `SourceUnavailable(names: list[str])` with attribute `names: list[str]` and message `no usable data and no good snapshot for: <names joined by ", ">`.
  - `modeltiers.paths`: `OUT_ENV = "MODELTIERS_OUT"`, `CACHE_ENV = "MODELTIERS_CACHE"`, `output_path(env: Mapping[str, str] | None = None) -> Path`, `cache_dir(env: Mapping[str, str] | None = None) -> Path`.
  - `tests/conftest.py`: `FIXTURES: Path`, the autouse fixture `_hermetic`, the fixture `read_fixture -> Callable[[str], bytes]`.

- [ ] **Step 1: Create the package files**

Write `modeltiers/pyproject.toml`:

```toml
[project]
name = "modeltiers"
version = "0.1.0"
description = "Advisory capability tiers for the models wt knows, from public benchmark sources"
requires-python = "==3.13.*"
dependencies = [
    "tomli-w>=1.2.0",
]

[project.scripts]
modeltiers = "modeltiers.cli:main"

[build-system]
requires = ["hatchling"]
build-backend = "hatchling.build"

[tool.hatch.build.targets.wheel]
packages = ["src/modeltiers"]

[dependency-groups]
dev = [
    "pytest>=8.0.0",
    "pytest-cov>=5.0.0",
    "mypy>=1.10.0",
    "ruff>=0.6.0",
]

[tool.pytest.ini_options]
testpaths = ["tests"]
pythonpath = ["tests"]
addopts = "-v --tb=short"

[tool.coverage.run]
source = ["src/modeltiers"]
omit = ["*/tests/*"]

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
known-first-party = ["modeltiers"]

[tool.ruff.format]
quote-style = "double"
indent-style = "space"
line-ending = "lf"

[tool.mypy]
python_version = "3.13"
strict = true
```

Write `modeltiers/Makefile`:

```makefile
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

The `Makefile` recipes are indented with one tab, as in `llmbench/Makefile`.

Write `modeltiers/.gitignore`:

```text
__pycache__/
*.py[cod]
*.egg-info/
.venv/
dist/
build/
.pytest_cache/
.mypy_cache/
.ruff_cache/
.coverage
htmlcov/
```

Write `modeltiers/src/modeltiers/__init__.py`:

```python
"""modeltiers: advisory capability tiers for the models wt knows."""
```

- [ ] **Step 2: Create the virtualenv and the lock file**

Run: `make -C modeltiers install`
Expected: PASS. uv creates `modeltiers/.venv` and `modeltiers/uv.lock` and installs `modeltiers`, `tomli-w`, `pytest`, `pytest-cov`, `mypy` and `ruff`.

- [ ] **Step 3: Write the guard and the failing tests**

Write `modeltiers/tests/conftest.py`:

```python
"""Shared fixtures: the guards that keep the suite off the network, off wt and
out of the real config and cache directories, plus the fixture-file helpers."""

import subprocess
import urllib.error
import urllib.request
from collections.abc import Callable
from pathlib import Path
from typing import Any

import pytest

FIXTURES = Path(__file__).parent / "fixtures"


def _no_network(*args: Any, **kwargs: Any) -> Any:
    raise urllib.error.URLError("hermetic test: no network")


def _no_subprocess(*args: Any, **kwargs: Any) -> Any:
    raise AssertionError(f"hermetic test: subprocess.run called with {args!r}")


@pytest.fixture(autouse=True)
def _hermetic(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    """No test may fetch, run wt, or read or write the real output file or the
    real snapshot cache. `urllib.request` and `subprocess` are each one shared
    module object, so these two patches hold for the whole process. A test
    that needs either replaces it with its own fake."""
    monkeypatch.setattr(urllib.request, "urlopen", _no_network)
    monkeypatch.setattr(subprocess, "run", _no_subprocess)
    home = tmp_path / "home"
    home.mkdir()
    monkeypatch.setenv("HOME", str(home))
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg-config"))
    monkeypatch.setenv("XDG_CACHE_HOME", str(tmp_path / "xdg-cache"))
    monkeypatch.setenv("MODELTIERS_OUT", str(tmp_path / "out" / "tiers.toml"))
    monkeypatch.setenv("MODELTIERS_CACHE", str(tmp_path / "cache"))


@pytest.fixture
def read_fixture() -> Callable[[str], bytes]:
    """The bytes of one file under tests/fixtures/."""

    def read(name: str) -> bytes:
        return (FIXTURES / name).read_bytes()

    return read
```

Write `modeltiers/tests/test_conftest_guards.py`:

```python
"""The autouse guard in conftest.py does what the suite relies on."""

import os
import subprocess
import urllib.error
import urllib.request
from pathlib import Path

import pytest

from modeltiers import paths


def test_urlopen_is_blocked() -> None:
    with pytest.raises(urllib.error.URLError, match="hermetic"):
        urllib.request.urlopen("https://models.dev/api.json")


def test_subprocess_run_is_blocked() -> None:
    with pytest.raises(AssertionError, match="hermetic"):
        subprocess.run(["wt", "model", "list", "--json"])


def test_every_path_is_under_tmp_path(tmp_path: Path) -> None:
    for name in ("HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "MODELTIERS_OUT", "MODELTIERS_CACHE"):
        assert Path(os.environ[name]).is_relative_to(tmp_path), name
    assert paths.output_path().is_relative_to(tmp_path)
    assert paths.cache_dir().is_relative_to(tmp_path)
    assert Path.home().is_relative_to(tmp_path)
```

Write `modeltiers/tests/test_paths.py`:

```python
from pathlib import Path

from modeltiers import paths


def test_output_defaults_to_config_home() -> None:
    assert paths.output_path({"HOME": "/h"}) == Path("/h/.config/local-ai/tiers.toml")


def test_output_follows_xdg_config_home() -> None:
    env = {"HOME": "/h", "XDG_CONFIG_HOME": "/x"}
    assert paths.output_path(env) == Path("/x/local-ai/tiers.toml")


def test_output_override_wins() -> None:
    env = {"HOME": "/h", "XDG_CONFIG_HOME": "/x", "MODELTIERS_OUT": "/o/t.toml"}
    assert paths.output_path(env) == Path("/o/t.toml")


def test_cache_defaults_to_cache_home() -> None:
    assert paths.cache_dir({"HOME": "/h"}) == Path("/h/.cache/local-ai/modeltiers")


def test_cache_follows_xdg_cache_home() -> None:
    env = {"HOME": "/h", "XDG_CACHE_HOME": "/c"}
    assert paths.cache_dir(env) == Path("/c/local-ai/modeltiers")


def test_cache_override_wins() -> None:
    env = {"HOME": "/h", "XDG_CACHE_HOME": "/c", "MODELTIERS_CACHE": "/m"}
    assert paths.cache_dir(env) == Path("/m")


def test_an_empty_variable_counts_as_unset() -> None:
    env = {"HOME": "/h", "XDG_CONFIG_HOME": "", "XDG_CACHE_HOME": "", "MODELTIERS_OUT": ""}
    assert paths.output_path(env) == Path("/h/.config/local-ai/tiers.toml")
    assert paths.cache_dir(env) == Path("/h/.cache/local-ai/modeltiers")


def test_the_process_environment_is_the_default(tmp_path: Path) -> None:
    # conftest points both overrides under tmp_path.
    assert paths.output_path() == tmp_path / "out" / "tiers.toml"
    assert paths.cache_dir() == tmp_path / "cache"


def test_home_falls_back_to_the_user_database_entry(tmp_path: Path) -> None:
    # No HOME in the mapping: Path.home() is asked, and conftest's HOME answers.
    assert paths.output_path({}) == tmp_path / "home" / ".config" / "local-ai" / "tiers.toml"
```

- [ ] **Step 4: Run the tests and see them fail**

Run: `uv run --directory modeltiers pytest tests/test_paths.py tests/test_conftest_guards.py -q`
Expected: FAIL at collection of both files with `ImportError: cannot import name 'paths' from 'modeltiers'`.

- [ ] **Step 5: Write the errors and the paths**

Write `modeltiers/src/modeltiers/errors.py`:

```python
"""Every failure modeltiers reports. The CLI turns each into exit code 1."""


class ModelTiersError(Exception):
    """Base class: the CLI prints the message and exits 1."""


class ConfigError(ModelTiersError):
    """A committed data file, a path or a flag is invalid."""


class InventoryError(ModelTiersError):
    """`wt model list --json` could not be run or read."""


class FetchError(ModelTiersError):
    """A download failed. `retryable` is true for a timeout, a 5xx status or
    an Arena `error` body: the cases that get one more attempt."""

    def __init__(self, message: str, *, retryable: bool = False) -> None:
        super().__init__(message)
        self.retryable = retryable


class SourceParseError(ModelTiersError):
    """A snapshot is not in the shape its parser knows, or is under its
    minimum row count."""


class SourceUnavailable(ModelTiersError):
    """One or more sources failed and have no good snapshot to fall back on."""

    def __init__(self, names: list[str]) -> None:
        super().__init__("no usable data and no good snapshot for: " + ", ".join(names))
        self.names = names
```

Write `modeltiers/src/modeltiers/paths.py`:

```python
"""Where modeltiers reads and writes.

Both functions take the environment as a mapping, so a test passes its own
and never depends on the process environment.
"""

import os
from collections.abc import Mapping
from pathlib import Path

OUT_ENV = "MODELTIERS_OUT"
CACHE_ENV = "MODELTIERS_CACHE"


def _base(env: Mapping[str, str], xdg_var: str, home_dir: str) -> Path:
    """The XDG base directory, or its default under HOME when the variable is
    unset or empty."""
    xdg = env.get(xdg_var, "")
    if xdg:
        return Path(xdg)
    home = env.get("HOME", "")
    return (Path(home) if home else Path.home()) / home_dir


def output_path(env: Mapping[str, str] | None = None) -> Path:
    """`tiers.toml`: MODELTIERS_OUT, else `$XDG_CONFIG_HOME/local-ai/tiers.toml`,
    else `~/.config/local-ai/tiers.toml`."""
    env = os.environ if env is None else env
    override = env.get(OUT_ENV, "")
    if override:
        return Path(override)
    return _base(env, "XDG_CONFIG_HOME", ".config") / "local-ai" / "tiers.toml"


def cache_dir(env: Mapping[str, str] | None = None) -> Path:
    """The snapshot directory: MODELTIERS_CACHE, else
    `$XDG_CACHE_HOME/local-ai/modeltiers`, else `~/.cache/local-ai/modeltiers`."""
    env = os.environ if env is None else env
    override = env.get(CACHE_ENV, "")
    if override:
        return Path(override)
    return _base(env, "XDG_CACHE_HOME", ".cache") / "local-ai" / "modeltiers"
```

- [ ] **Step 6: Run the tests and see them pass**

Run: `uv run --directory modeltiers pytest tests/test_paths.py tests/test_conftest_guards.py -q`
Expected: PASS, `12 passed`.

- [ ] **Step 7: Lint, format check and type check**

Run: `make -C modeltiers check`
Expected: PASS. `All checks passed!`, every file already formatted, `Success: no issues found in 3 source files`.

- [ ] **Step 8: Commit**

```bash
git add modeltiers/pyproject.toml modeltiers/Makefile modeltiers/.gitignore modeltiers/uv.lock modeltiers/src/modeltiers/__init__.py modeltiers/src/modeltiers/errors.py modeltiers/src/modeltiers/paths.py modeltiers/tests/conftest.py modeltiers/tests/test_conftest_guards.py modeltiers/tests/test_paths.py
git commit -m "feat(modeltiers): package skeleton, paths and a hermetic test guard"
```

---

### Task 2: Records and the inventory reader

**Files:**
- Create: `modeltiers/src/modeltiers/records.py`, `modeltiers/src/modeltiers/inventory.py`
- Test: `modeltiers/tests/test_inventory.py`, `modeltiers/tests/fixtures/inventory.json`

**Interfaces:**
- Consumes: `modeltiers.errors.InventoryError` (Task 1).
- Produces:
  - `modeltiers.records` (all `@dataclass(frozen=True)`): `InventoryRow(provider_id: str, model_name: str, location: str, registered: bool)`; `Inventory(rows: tuple[InventoryRow, ...], registry: str, providers: Mapping[str, str], native_dropped: int = 0)`; `ModelsDevRow(provider: str, model_id: str, canonical: str | None, tool_call: bool, open_weights: bool, context: int | None)`; `ModelsDev(rows: tuple[ModelsDevRow, ...])`; `ScoreRecord(source: str, source_id: str, score: float, ci_lower: float | None = None, effort: str | None = None, coding_index: float | None = None, agentic_index: float | None = None)`.
  - `modeltiers.inventory`: `RunWt = Callable[[], str]`, `WT_COMMAND = ("wt", "model", "list", "--json")`, `run_wt() -> str`, `parse_inventory(text: str) -> Inventory`, `load_inventory(*, path: Path | None, run_wt: RunWt) -> Inventory`, `provider_warnings(inventory: Inventory) -> list[str]`.

- [ ] **Step 1: Write the fixture**

Seven rows in the shape `wt model list --json` prints (`wt/cmd/wt/model_list.go`, `modelListJSON`): one native row, three cloud rows, two registered local rows (one with `status` `missing`) and one discovered row. `mtplx` reports `partial`.

Write `modeltiers/tests/fixtures/inventory.json`:

```json
{
 "registry": "/home/owner/.config/local-ai/registry.toml",
 "models": [
  {"id": "claude/native", "family": "claude", "provider_id": "claude", "model_name": "native", "location": "cloud", "tags": ["code", "design"], "registered": true, "status": "ok", "running": "", "size_bytes": null, "path": null, "malformed": []},
  {"id": "ollama/glm-5.3:cloud", "family": "glm-5.3", "provider_id": "ollama", "model_name": "glm-5.3:cloud", "location": "cloud", "tags": [], "registered": true, "status": "ok", "running": "", "size_bytes": null, "path": null, "malformed": []},
  {"id": "ollama/deepseek-v4-pro:cloud", "family": "deepseek-v4", "provider_id": "ollama", "model_name": "deepseek-v4-pro:cloud", "location": "cloud", "tags": [], "registered": true, "status": "ok", "running": "", "size_bytes": null, "path": null, "malformed": []},
  {"id": "openrouter/z-ai--glm-5.3", "family": "glm-5.3", "provider_id": "openrouter", "model_name": "z-ai/glm-5.3", "location": "cloud", "tags": [], "registered": true, "status": "ok", "running": "", "size_bytes": null, "path": null, "malformed": []},
  {"id": "ollama/qwen3.8:27b-mlx", "family": "qwen3.8", "provider_id": "ollama", "model_name": "qwen3.8:27b-mlx", "location": "local", "tags": [], "registered": true, "status": "ok", "running": "", "size_bytes": null, "path": null, "malformed": []},
  {"id": "ollama/medgemma:27b", "family": "medgemma", "provider_id": "ollama", "model_name": "medgemma:27b", "location": "local", "tags": [], "registered": true, "status": "missing", "running": "", "size_bytes": null, "path": null, "malformed": []},
  {"id": "ollama/gemma4:31b-mlx", "family": "", "provider_id": "ollama", "model_name": "gemma4:31b-mlx", "location": "local", "tags": [], "registered": false, "status": "new", "running": "", "size_bytes": null, "path": null, "malformed": []}
 ],
 "providers": {"mtplx": "partial", "ollama": "ok", "omlx": "ok"}
}
```

- [ ] **Step 2: Write the failing test**

Write `modeltiers/tests/test_inventory.py`:

```python
import json
import subprocess
from pathlib import Path
from typing import Any

import pytest

from modeltiers import inventory
from modeltiers.errors import InventoryError
from modeltiers.records import InventoryRow

FIXTURE = Path(__file__).parent / "fixtures" / "inventory.json"


def _never_run_wt() -> str:
    raise AssertionError("wt must not be run when --inventory is given")


def test_reads_registry_providers_and_rows() -> None:
    inv = inventory.parse_inventory(FIXTURE.read_text())
    assert inv.registry == "/home/owner/.config/local-ai/registry.toml"
    assert inv.providers == {"mtplx": "partial", "ollama": "ok", "omlx": "ok"}
    assert inv.rows[0] == InventoryRow("ollama", "glm-5.3:cloud", "cloud", True)
    assert [(r.provider_id, r.model_name) for r in inv.rows] == [
        ("ollama", "glm-5.3:cloud"),
        ("ollama", "deepseek-v4-pro:cloud"),
        ("openrouter", "z-ai/glm-5.3"),
        ("ollama", "qwen3.8:27b-mlx"),
        ("ollama", "medgemma:27b"),
        ("ollama", "gemma4:31b-mlx"),
    ]


def test_native_rows_are_dropped_and_counted() -> None:
    inv = inventory.parse_inventory(FIXTURE.read_text())
    assert inv.native_dropped == 1
    assert all(r.model_name != "native" for r in inv.rows)


def test_discovered_and_missing_rows_are_kept() -> None:
    inv = inventory.parse_inventory(FIXTURE.read_text())
    by_name = {r.model_name: r for r in inv.rows}
    assert by_name["gemma4:31b-mlx"].registered is False  # discovered
    assert by_name["medgemma:27b"].location == "local"  # status "missing" in the fixture


def test_a_provider_that_is_not_ok_is_named_in_a_warning() -> None:
    inv = inventory.parse_inventory(FIXTURE.read_text())
    warnings = inventory.provider_warnings(inv)
    assert len(warnings) == 1
    assert "mtplx" in warnings[0]
    assert "partial" in warnings[0]


@pytest.mark.parametrize(
    ("text", "message"),
    [
        ("not json", "not JSON"),
        ("[]", "expected an object"),
        ('{"registry": "/r", "providers": {}}', "models is missing"),
        ('{"models": [], "providers": {}}', "registry is missing"),
        ('{"models": [], "registry": "/r"}', "providers is missing"),
        ('{"models": [], "registry": "/r", "providers": {"ollama": 1}}', "providers is missing"),
        ('{"models": [7], "registry": "/r", "providers": {}}', r"models\[0\] is not an object"),
        (
            '{"models": [{"model_name": "m", "location": "cloud", "registered": true}],'
            ' "registry": "/r", "providers": {}}',
            r"models\[0\]\.provider_id is missing",
        ),
        (
            '{"models": [{"provider_id": "p", "model_name": "m", "location": "cloud",'
            ' "registered": "yes"}], "registry": "/r", "providers": {}}',
            r"models\[0\]\.registered is missing or not a bool",
        ),
    ],
)
def test_a_document_in_another_shape_is_an_inventory_error(text: str, message: str) -> None:
    with pytest.raises(InventoryError, match=message):
        inventory.parse_inventory(text)


def test_inventory_file_is_read_and_wt_is_not_run() -> None:
    inv = inventory.load_inventory(path=FIXTURE, run_wt=_never_run_wt)
    assert len(inv.rows) == 6


def test_a_missing_inventory_file_is_an_inventory_error(tmp_path: Path) -> None:
    with pytest.raises(InventoryError, match="cannot read"):
        inventory.load_inventory(path=tmp_path / "absent.json", run_wt=_never_run_wt)


def test_without_a_file_the_runner_is_called() -> None:
    inv = inventory.load_inventory(path=None, run_wt=FIXTURE.read_text)
    assert len(inv.rows) == 6


class FakeRun:
    """Stands in for subprocess.run: records each argv, then returns or raises."""

    def __init__(self, result: Any) -> None:
        self.result = result
        self.calls: list[list[str]] = []

    def __call__(self, argv: list[str], **kwargs: Any) -> Any:
        self.calls.append(argv)
        if isinstance(self.result, BaseException):
            raise self.result
        return self.result


def test_run_wt_runs_exactly_model_list_json(monkeypatch: pytest.MonkeyPatch) -> None:
    done = subprocess.CompletedProcess(["wt"], 0, stdout=FIXTURE.read_text(), stderr="")
    fake = FakeRun(done)
    monkeypatch.setattr(subprocess, "run", fake)
    assert json.loads(inventory.run_wt())["registry"].endswith("registry.toml")
    assert fake.calls == [["wt", "model", "list", "--json"]]


def test_wt_not_on_path_is_an_inventory_error(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(subprocess, "run", FakeRun(FileNotFoundError("wt")))
    with pytest.raises(InventoryError, match="wt is not on PATH"):
        inventory.run_wt()


def test_wt_failing_is_an_inventory_error_with_its_stderr(monkeypatch: pytest.MonkeyPatch) -> None:
    done = subprocess.CompletedProcess(["wt"], 1, stdout="", stderr="registry.toml: bad\n")
    monkeypatch.setattr(subprocess, "run", FakeRun(done))
    with pytest.raises(InventoryError, match="exited 1: registry.toml: bad"):
        inventory.run_wt()


def test_wt_hanging_is_an_inventory_error(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(subprocess, "run", FakeRun(subprocess.TimeoutExpired("wt", 60)))
    with pytest.raises(InventoryError, match="gave no answer"):
        inventory.run_wt()
```

- [ ] **Step 3: Run the test and see it fail**

Run: `uv run --directory modeltiers pytest tests/test_inventory.py -q`
Expected: FAIL at collection with `ImportError: cannot import name 'inventory' from 'modeltiers'`.

- [ ] **Step 4: Write the records**

All five record types land now, so later tasks only import them.

Write `modeltiers/src/modeltiers/records.py`:

```python
"""The plain data every stage passes on. All frozen; no behaviour."""

from collections.abc import Mapping
from dataclasses import dataclass


@dataclass(frozen=True)
class InventoryRow:
    """One model wt reported. The join key is (provider_id, model_name)."""

    provider_id: str
    model_name: str
    location: str  # "cloud" | "local"
    registered: bool


@dataclass(frozen=True)
class Inventory:
    """`wt model list --json`, without its native rows."""

    rows: tuple[InventoryRow, ...]
    registry: str  # the registry path wt read
    providers: Mapping[str, str]  # provider family -> probe status
    native_dropped: int = 0


@dataclass(frozen=True)
class ModelsDevRow:
    """One provider-model row of models.dev."""

    provider: str  # models.dev provider id, e.g. "ollama-cloud"
    model_id: str  # the host's own id, e.g. "glm-5.3"
    canonical: str | None  # canonical_model_id, in models.dev's spelling
    tool_call: bool
    open_weights: bool
    context: int | None  # limit.context


@dataclass(frozen=True)
class ModelsDev:
    """Every provider-model row of one models.dev snapshot."""

    rows: tuple[ModelsDevRow, ...]


@dataclass(frozen=True)
class ScoreRecord:
    """One scored row of a benchmark source.

    `score` is the voting signal: OpenRouter's intelligence index, Arena's
    agent score, Epoch's capabilities index. `source_id` is the id as the
    source spells it; Arena's has its effort parenthesis removed and kept in
    `effort`.
    """

    source: str  # "openrouter" | "arena" | "epoch"
    source_id: str
    score: float
    ci_lower: float | None = None
    effort: str | None = None
    coding_index: float | None = None  # OpenRouter only; evidence, no vote
    agentic_index: float | None = None  # OpenRouter only; evidence, no vote
```

- [ ] **Step 5: Write the inventory reader**

Write `modeltiers/src/modeltiers/inventory.py`:

```python
"""The wt inventory: `wt model list --json`, or a saved copy of its output."""

import json
import subprocess
from collections.abc import Callable
from pathlib import Path
from typing import Any

from modeltiers.errors import InventoryError
from modeltiers.records import Inventory, InventoryRow

# Returns the stdout of `wt model list --json`, or raises InventoryError.
RunWt = Callable[[], str]

WT_COMMAND = ("wt", "model", "list", "--json")
WT_TIMEOUT_SECONDS = 60


def run_wt() -> str:
    """Run `wt model list --json`, the only wt command modeltiers ever runs."""
    command = " ".join(WT_COMMAND)
    try:
        done = subprocess.run(
            list(WT_COMMAND),
            capture_output=True,
            text=True,
            check=False,
            timeout=WT_TIMEOUT_SECONDS,
        )
    except FileNotFoundError as exc:
        raise InventoryError("wt is not on PATH; install it or pass --inventory FILE") from exc
    except subprocess.TimeoutExpired as exc:
        raise InventoryError(f"`{command}` gave no answer in {WT_TIMEOUT_SECONDS} s") from exc
    if done.returncode != 0:
        detail = done.stderr.strip() or "no message"
        raise InventoryError(f"`{command}` exited {done.returncode}: {detail}")
    return done.stdout


def _field(row: dict[str, Any], key: str, kind: type, index: int) -> Any:
    value = row.get(key)
    if not isinstance(value, kind):
        raise InventoryError(
            f"inventory: models[{index}].{key} is missing or not a {kind.__name__}"
        )
    return value


def parse_inventory(text: str) -> Inventory:
    """Read the document: an object with `models`, `registry` and `providers`.

    Rows whose model_name is `native` are dropped and counted. Every other
    row is kept, registered or discovered, weights present or missing.
    """
    try:
        doc = json.loads(text)
    except json.JSONDecodeError as exc:
        raise InventoryError(f"inventory: not JSON: {exc}") from exc
    if not isinstance(doc, dict):
        raise InventoryError("inventory: expected an object with models, registry and providers")
    models = doc.get("models")
    registry = doc.get("registry")
    providers = doc.get("providers")
    if not isinstance(models, list):
        raise InventoryError("inventory: models is missing or not a list")
    if not isinstance(registry, str):
        raise InventoryError("inventory: registry is missing or not a string")
    if not isinstance(providers, dict) or not all(isinstance(v, str) for v in providers.values()):
        raise InventoryError("inventory: providers is missing or not a table of strings")
    rows: list[InventoryRow] = []
    native = 0
    for index, row in enumerate(models):
        if not isinstance(row, dict):
            raise InventoryError(f"inventory: models[{index}] is not an object")
        model_name = _field(row, "model_name", str, index)
        if model_name == "native":
            native += 1
            continue
        rows.append(
            InventoryRow(
                provider_id=_field(row, "provider_id", str, index),
                model_name=model_name,
                location=_field(row, "location", str, index),
                registered=_field(row, "registered", bool, index),
            )
        )
    return Inventory(
        rows=tuple(rows), registry=registry, providers=dict(providers), native_dropped=native
    )


def load_inventory(*, path: Path | None, run_wt: RunWt) -> Inventory:
    """Read `path` when given (and never run wt), else run wt."""
    if path is None:
        return parse_inventory(run_wt())
    try:
        text = path.read_text(encoding="utf-8")
    except OSError as exc:
        raise InventoryError(f"inventory: cannot read {path}: {exc.strerror}") from exc
    return parse_inventory(text)


def provider_warnings(inventory: Inventory) -> list[str]:
    """One line per provider whose probe status is not `ok`."""
    return [
        f"provider {name} probe status is {status}; "
        "models it would have discovered may be absent from this run"
        for name, status in sorted(inventory.providers.items())
        if status != "ok"
    ]
```

- [ ] **Step 6: Run the test and see it pass**

Run: `uv run --directory modeltiers pytest tests/test_inventory.py -q`
Expected: PASS, `20 passed`.

- [ ] **Step 7: Lint, format check and type check**

Run: `make -C modeltiers check`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add modeltiers/src/modeltiers/records.py modeltiers/src/modeltiers/inventory.py modeltiers/tests/test_inventory.py modeltiers/tests/fixtures/inventory.json
git commit -m "feat(modeltiers): read the wt inventory from wt or a saved file"
```

---

### Task 3: Fetch, with one retry and Arena paging

**Files:**
- Create: `modeltiers/src/modeltiers/fetch.py`
- Test: `modeltiers/tests/fakes.py`, `modeltiers/tests/test_fetch.py`, `modeltiers/tests/fixtures/arena_page1.json`, `modeltiers/tests/fixtures/arena_page2.json`, `modeltiers/tests/fixtures/arena_too_few.json`, `modeltiers/tests/fixtures/arena_error.json`

**Interfaces:**
- Consumes: `modeltiers.errors.FetchError` (Task 1); the `read_fixture` fixture (Task 1).
- Produces:
  - `modeltiers.fetch`: `Fetcher = Callable[[str], bytes]`, `Sleep = Callable[[float], None]`, `BodyCheck = Callable[[bytes], None]`, `TIMEOUT_SECONDS = 30`, `RETRY_DELAY_SECONDS = 2`, `USER_AGENT = "modeltiers/0.1"`, `MODELSDEV_URL`, `OPENROUTER_URL`, `EPOCH_URL`, `ARENA_PAGE_LENGTH = 100`, `ARENA_URL`, `http_fetcher(url: str) -> bytes`, `fetch_with_retry(fetcher: Fetcher, url: str, sleep: Sleep, check: BodyCheck | None = None) -> bytes`, `fetch_modelsdev(fetcher: Fetcher, sleep: Sleep) -> bytes`, `fetch_openrouter(fetcher: Fetcher, sleep: Sleep) -> bytes`, `fetch_epoch(fetcher: Fetcher, sleep: Sleep) -> bytes`, `arena_page_url(offset: int) -> str`, `wrap_arena_pages(pages: list[bytes]) -> bytes`, `fetch_arena(fetcher: Fetcher, sleep: Sleep) -> bytes`.
  - `tests/fakes.py`: `FakeFetcher(answers: dict[str, list[bytes | Exception]])` with `calls: list[str]`; `Sleeps()` with `calls: list[float]`. Test files import them as `from fakes import FakeFetcher, Sleeps` (`pythonpath = ["tests"]` in `pyproject.toml`).

- [ ] **Step 1: Write the Arena page fixtures**

Each file is one body of the Hugging Face rows API. Pages 1 and 2 together hold the five rows `num_rows_total` announces. `arena_too_few.json` is a complete one-row dataset. `arena_error.json` is the body the service returned during design.

Write `modeltiers/tests/fixtures/arena_page1.json`:

```json
{
 "rows": [
  {"row_idx": 0, "row": {"model_name": "Claude Opus 5.5 (High)", "organization": "anthropic", "score": 0.14331280058415052, "score_ci_lower": 0.12150663912969831, "score_ci_upper": 0.16511896203860274, "rank": 1, "category": "overall"}, "truncated_cells": []},
  {"row_idx": 1, "row": {"model_name": "GLM 5.3 (Max)", "organization": "zai", "score": 0.02147446416824893, "score_ci_lower": 0.014306504009895325, "score_ci_upper": 0.028642424326602536, "rank": 24, "category": "overall"}, "truncated_cells": []},
  {"row_idx": 2, "row": {"model_name": "Qwen3.8 Max", "organization": "alibaba", "score": 0.023467932612357068, "score_ci_lower": 0.017532412700631165, "score_ci_upper": 0.02940345252408297, "rank": 23, "category": "overall"}, "truncated_cells": []}
 ],
 "num_rows_total": 5,
 "num_rows_per_page": 100,
 "partial": false
}
```

Write `modeltiers/tests/fixtures/arena_page2.json`:

```json
{
 "rows": [
  {"row_idx": 3, "row": {"model_name": "DeepSeek V4 Pro (High) (0813)", "organization": "deepseek", "score": 0.013425874696272922, "score_ci_lower": -0.0031749129284354416, "score_ci_upper": 0.030026662320981286, "rank": 26, "category": "overall"}, "truncated_cells": []},
  {"row_idx": 4, "row": {"model_name": "Minimax M3", "organization": "minimax", "score": -0.06954648731246331, "score_ci_lower": -0.07855233102193303, "score_ci_upper": -0.060540643602993584, "rank": 44, "category": "overall"}, "truncated_cells": []}
 ],
 "num_rows_total": 5,
 "num_rows_per_page": 100,
 "partial": false
}
```

Write `modeltiers/tests/fixtures/arena_too_few.json`:

```json
{
 "rows": [
  {"row_idx": 0, "row": {"model_name": "Claude Opus 5.5 (High)", "organization": "anthropic", "score": 0.14331280058415052, "score_ci_lower": 0.12150663912969831, "score_ci_upper": 0.16511896203860274, "rank": 1, "category": "overall"}, "truncated_cells": []}
 ],
 "num_rows_total": 1,
 "num_rows_per_page": 100,
 "partial": false
}
```

Write `modeltiers/tests/fixtures/arena_error.json`:

```json
{"error":"the dataset index is loading, this may take longer than usual"}
```

- [ ] **Step 2: Write the fakes**

Write `modeltiers/tests/fakes.py`:

```python
"""Fakes for the seams: a Fetcher that answers from a queue, and a sleep that
only records."""


class FakeFetcher:
    """Answers each URL from a queue of bodies or exceptions, in order."""

    def __init__(self, answers: dict[str, list[bytes | Exception]]) -> None:
        self.answers = {url: list(queue) for url, queue in answers.items()}
        self.calls: list[str] = []

    def __call__(self, url: str) -> bytes:
        self.calls.append(url)
        answer = self.answers[url].pop(0)
        if isinstance(answer, Exception):
            raise answer
        return answer


class Sleeps:
    def __init__(self) -> None:
        self.calls: list[float] = []

    def __call__(self, seconds: float) -> None:
        self.calls.append(seconds)
```

- [ ] **Step 3: Write the failing test**

Write `modeltiers/tests/test_fetch.py`:

```python
import io
import json
import urllib.error
import urllib.request
from collections.abc import Callable
from typing import Any

import pytest
from fakes import FakeFetcher, Sleeps

from modeltiers import fetch
from modeltiers.errors import FetchError


def test_a_first_success_is_returned_without_waiting() -> None:
    fetcher = FakeFetcher({"u": [b"body"]})
    sleeps = Sleeps()
    assert fetch.fetch_with_retry(fetcher, "u", sleeps) == b"body"
    assert fetcher.calls == ["u"]
    assert sleeps.calls == []


def test_a_retryable_failure_is_retried_once_after_two_seconds() -> None:
    fetcher = FakeFetcher({"u": [FetchError("HTTP 502", retryable=True), b"body"]})
    sleeps = Sleeps()
    assert fetch.fetch_with_retry(fetcher, "u", sleeps) == b"body"
    assert fetcher.calls == ["u", "u"]
    assert sleeps.calls == [2]


def test_a_second_retryable_failure_is_raised() -> None:
    failures: list[bytes | Exception] = [
        FetchError("HTTP 502", retryable=True),
        FetchError("HTTP 503", retryable=True),
    ]
    fetcher = FakeFetcher({"u": failures})
    with pytest.raises(FetchError, match="HTTP 503"):
        fetch.fetch_with_retry(fetcher, "u", Sleeps())
    assert fetcher.calls == ["u", "u"]


def test_a_failure_that_is_not_retryable_is_raised_at_once() -> None:
    fetcher = FakeFetcher({"u": [FetchError("HTTP 404"), b"never read"]})
    sleeps = Sleeps()
    with pytest.raises(FetchError, match="HTTP 404"):
        fetch.fetch_with_retry(fetcher, "u", sleeps)
    assert fetcher.calls == ["u"]
    assert sleeps.calls == []


@pytest.mark.parametrize(
    ("function", "url"),
    [
        (fetch.fetch_modelsdev, "https://models.dev/api.json"),
        (fetch.fetch_openrouter, "https://openrouter.ai/api/v1/models"),
        (fetch.fetch_epoch, "https://epoch.ai/data/benchmark_data.zip"),
    ],
)
def test_each_single_request_source_fetches_its_url(
    function: Callable[[fetch.Fetcher, fetch.Sleep], bytes], url: str
) -> None:
    fetcher = FakeFetcher({url: [b"body"]})
    assert function(fetcher, Sleeps()) == b"body"
    assert fetcher.calls == [url]


def test_arena_page_url_names_the_agent_config_and_the_offset() -> None:
    assert fetch.arena_page_url(100) == (
        "https://datasets-server.huggingface.co/rows"
        "?dataset=lmarena-ai/leaderboard-dataset&config=agent&split=latest"
        "&offset=100&length=100"
    )


def test_arena_is_paged_to_num_rows_total_into_one_document(
    read_fixture: Callable[[str], bytes],
) -> None:
    page1 = read_fixture("arena_page1.json")  # 3 rows of 5
    page2 = read_fixture("arena_page2.json")  # 2 rows of 5
    fetcher = FakeFetcher({fetch.arena_page_url(0): [page1], fetch.arena_page_url(3): [page2]})
    doc = json.loads(fetch.fetch_arena(fetcher, Sleeps()))
    assert fetcher.calls == [fetch.arena_page_url(0), fetch.arena_page_url(3)]
    # Each page body is kept unparsed, byte for byte.
    assert doc == {"pages": [page1.decode(), page2.decode()]}


def test_an_arena_error_body_is_retried_once(read_fixture: Callable[[str], bytes]) -> None:
    error = read_fixture("arena_error.json")
    page = read_fixture("arena_too_few.json")  # 1 row of 1
    fetcher = FakeFetcher({fetch.arena_page_url(0): [error, page]})
    sleeps = Sleeps()
    doc = json.loads(fetch.fetch_arena(fetcher, sleeps))
    assert doc == {"pages": [page.decode()]}
    assert sleeps.calls == [2]


def test_two_arena_error_bodies_are_a_failed_download(
    read_fixture: Callable[[str], bytes],
) -> None:
    error = read_fixture("arena_error.json")
    fetcher = FakeFetcher({fetch.arena_page_url(0): [error, error]})
    with pytest.raises(FetchError, match="the dataset index is loading"):
        fetch.fetch_arena(fetcher, Sleeps())


@pytest.mark.parametrize(
    ("body", "message"),
    [
        (b"<html>502 Bad Gateway</html>", "not JSON"),
        (b"[]", "not a JSON object"),
        (b'{"rows": []}', "no rows list or no num_rows_total"),
        (b'{"rows": [], "num_rows_total": 5}', "an empty page at offset 0 of 5"),
    ],
)
def test_an_arena_page_in_another_shape_is_a_failed_download(body: bytes, message: str) -> None:
    fetcher = FakeFetcher({fetch.arena_page_url(0): [body]})
    with pytest.raises(FetchError, match=message):
        fetch.fetch_arena(fetcher, Sleeps())


class FakeResponse(io.BytesIO):
    def __enter__(self) -> "FakeResponse":
        return self

    def __exit__(self, *args: Any) -> None:
        self.close()


def _urlopen(answer: Any, seen: list[Any]) -> Callable[..., Any]:
    def urlopen(request: urllib.request.Request, timeout: float) -> Any:
        seen.append((request.full_url, request.get_header("User-agent"), timeout))
        if isinstance(answer, Exception):
            raise answer
        return FakeResponse(answer)

    return urlopen


def test_http_fetcher_reads_the_body_with_a_30_second_timeout(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    seen: list[Any] = []
    monkeypatch.setattr(urllib.request, "urlopen", _urlopen(b"body", seen))
    assert fetch.http_fetcher("https://example.test/x") == b"body"
    assert seen == [("https://example.test/x", "modeltiers/0.1", 30)]


def _http_error(code: int) -> urllib.error.HTTPError:
    return urllib.error.HTTPError("https://example.test/x", code, "status", {}, None)  # type: ignore[arg-type]


@pytest.mark.parametrize(
    ("failure", "retryable", "message"),
    [
        (_http_error(502), True, "HTTP 502"),
        (_http_error(500), True, "HTTP 500"),
        (_http_error(404), False, "HTTP 404"),
        (_http_error(429), False, "HTTP 429"),
        (TimeoutError("timed out"), True, "timed out after 30 s"),
        (urllib.error.URLError(TimeoutError("timed out")), True, "timed out"),
        (urllib.error.URLError("name not known"), False, "name not known"),
        (ConnectionResetError("reset by peer"), False, "reset by peer"),
    ],
)
def test_http_fetcher_marks_only_timeouts_and_5xx_retryable(
    monkeypatch: pytest.MonkeyPatch, failure: Exception, retryable: bool, message: str
) -> None:
    monkeypatch.setattr(urllib.request, "urlopen", _urlopen(failure, []))
    with pytest.raises(FetchError, match=message) as caught:
        fetch.http_fetcher("https://example.test/x")
    assert caught.value.retryable is retryable
```

- [ ] **Step 4: Run the test and see it fail**

Run: `uv run --directory modeltiers pytest tests/test_fetch.py -q`
Expected: FAIL at collection with `ImportError: cannot import name 'fetch' from 'modeltiers'`.

- [ ] **Step 5: Write the fetch module**

Write `modeltiers/src/modeltiers/fetch.py`:

```python
"""Downloads. One function per source, each behind the `Fetcher` seam.

A `Fetcher` takes a URL and returns the body, or raises FetchError. Tests pass
a fake; the CLI passes `http_fetcher`. Nothing here parses a source beyond
what paging Arena needs.
"""

import json
import urllib.error
import urllib.request
from collections.abc import Callable

from modeltiers.errors import FetchError

Fetcher = Callable[[str], bytes]
Sleep = Callable[[float], None]
# Raises FetchError(retryable=True) when a body that arrived is a failed download.
BodyCheck = Callable[[bytes], None]

TIMEOUT_SECONDS = 30
RETRY_DELAY_SECONDS = 2
USER_AGENT = "modeltiers/0.1"

MODELSDEV_URL = "https://models.dev/api.json"
OPENROUTER_URL = "https://openrouter.ai/api/v1/models"
EPOCH_URL = "https://epoch.ai/data/benchmark_data.zip"
ARENA_PAGE_LENGTH = 100
ARENA_URL = (
    "https://datasets-server.huggingface.co/rows"
    "?dataset=lmarena-ai/leaderboard-dataset&config=agent&split=latest"
    "&offset={offset}&length={length}"
)


def http_fetcher(url: str) -> bytes:
    """GET `url` with a 30-second timeout. A timeout or a 5xx status is
    retryable; any other failure is not."""
    request = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
    try:
        with urllib.request.urlopen(request, timeout=TIMEOUT_SECONDS) as response:
            body: bytes = response.read()
            return body
    except urllib.error.HTTPError as exc:
        raise FetchError(f"{url}: HTTP {exc.code}", retryable=500 <= exc.code < 600) from exc
    except TimeoutError as exc:
        raise FetchError(f"{url}: timed out after {TIMEOUT_SECONDS} s", retryable=True) from exc
    except urllib.error.URLError as exc:
        timed_out = isinstance(exc.reason, TimeoutError)
        raise FetchError(f"{url}: {exc.reason}", retryable=timed_out) from exc
    except OSError as exc:
        raise FetchError(f"{url}: {exc}") from exc


def fetch_with_retry(
    fetcher: Fetcher, url: str, sleep: Sleep, check: BodyCheck | None = None
) -> bytes:
    """Fetch `url`; after a retryable failure wait 2 seconds and try once more."""
    for attempt in (1, 2):
        try:
            body = fetcher(url)
            if check is not None:
                check(body)
            return body
        except FetchError as exc:
            if attempt == 2 or not exc.retryable:
                raise
            sleep(RETRY_DELAY_SECONDS)
    raise AssertionError("unreachable")  # pragma: no cover


def fetch_modelsdev(fetcher: Fetcher, sleep: Sleep) -> bytes:
    return fetch_with_retry(fetcher, MODELSDEV_URL, sleep)


def fetch_openrouter(fetcher: Fetcher, sleep: Sleep) -> bytes:
    return fetch_with_retry(fetcher, OPENROUTER_URL, sleep)


def fetch_epoch(fetcher: Fetcher, sleep: Sleep) -> bytes:
    return fetch_with_retry(fetcher, EPOCH_URL, sleep)


def arena_page_url(offset: int) -> str:
    return ARENA_URL.format(offset=offset, length=ARENA_PAGE_LENGTH)


def _arena_page(body: bytes) -> tuple[int, int]:
    """(rows on this page, num_rows_total). An `error` body is retryable."""
    try:
        page = json.loads(body)
    except ValueError as exc:
        raise FetchError("arena: a page is not JSON") from exc
    if not isinstance(page, dict):
        raise FetchError("arena: a page is not a JSON object")
    if "error" in page:
        raise FetchError(
            f"arena: the rows API answered with an error: {page['error']}", retryable=True
        )
    rows = page.get("rows")
    total = page.get("num_rows_total")
    if not isinstance(rows, list) or not isinstance(total, int) or isinstance(total, bool):
        raise FetchError("arena: a page has no rows list or no num_rows_total")
    return len(rows), total


def _check_arena_page(body: bytes) -> None:
    _arena_page(body)


def wrap_arena_pages(pages: list[bytes]) -> bytes:
    """The one document an Arena snapshot is: each page body, unparsed."""
    doc = {"pages": [page.decode("utf-8") for page in pages]}
    return json.dumps(doc, indent=1).encode("utf-8")


def fetch_arena(fetcher: Fetcher, sleep: Sleep) -> bytes:
    """Page the rows API until `num_rows_total` rows are read, and return the
    pages as one wrapper document (`wrap_arena_pages`)."""
    pages: list[bytes] = []
    offset = 0
    while True:
        body = fetch_with_retry(fetcher, arena_page_url(offset), sleep, _check_arena_page)
        rows, total = _arena_page(body)
        pages.append(body)
        offset += rows
        if offset >= total:
            return wrap_arena_pages(pages)
        if rows == 0:
            raise FetchError(f"arena: an empty page at offset {offset} of {total} rows")
```

- [ ] **Step 6: Run the test and see it pass**

Run: `uv run --directory modeltiers pytest tests/test_fetch.py -q`
Expected: PASS, `24 passed`.

- [ ] **Step 7: Lint, format check and type check**

Run: `make -C modeltiers check`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add modeltiers/src/modeltiers/fetch.py modeltiers/tests/fakes.py modeltiers/tests/test_fetch.py modeltiers/tests/fixtures/arena_page1.json modeltiers/tests/fixtures/arena_page2.json modeltiers/tests/fixtures/arena_too_few.json modeltiers/tests/fixtures/arena_error.json
git commit -m "feat(modeltiers): fetch the four sources behind a seam, with one retry"
```

---

### Task 4: The snapshot store

**Files:**
- Create: `modeltiers/src/modeltiers/snapshots.py`
- Test: `modeltiers/tests/test_snapshots.py`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `modeltiers.snapshots`: `STAMP_FORMAT = "%Y%m%dT%H%M%SZ"`, `KEEP_GOOD = 4`, `KEEP_BAD = 2`, `Snapshot(path: Path, fetched_at: datetime, bad: bool)` (frozen), `save(cache: Path, source: str, ext: str, data: bytes, now: datetime) -> Snapshot`, `mark_bad(snapshot: Snapshot) -> Snapshot`, `list_snapshots(cache: Path, source: str) -> list[Snapshot]` (newest first), `good_snapshots(cache: Path, source: str) -> list[Snapshot]` (newest first), `prune(cache: Path, source: str, *, keep_good: int = KEEP_GOOD, keep_bad: int = KEEP_BAD) -> list[Path]`.

- [ ] **Step 1: Write the failing test**

Write `modeltiers/tests/test_snapshots.py`:

```python
from datetime import UTC, datetime, timedelta, timezone
from pathlib import Path

from modeltiers import snapshots

T0 = datetime(2026, 10, 12, 15, 58, 0, tzinfo=UTC)


def _save_many(cache: Path, count: int, *, source: str = "arena") -> list[snapshots.Snapshot]:
    """`count` snapshots one minute apart, oldest first."""
    return [
        snapshots.save(cache, source, "json", b"{}", T0 + timedelta(minutes=i))
        for i in range(count)
    ]


def test_save_writes_the_bytes_under_the_source_and_the_utc_stamp(tmp_path: Path) -> None:
    snap = snapshots.save(tmp_path, "arena", "json", b"abc", T0)
    assert snap.path == tmp_path / "arena" / "20261012T155800Z.json"
    assert snap.path.read_bytes() == b"abc"
    assert snap.fetched_at == T0
    assert snap.bad is False


def test_the_stamp_is_utc_whatever_zone_now_is_in(tmp_path: Path) -> None:
    local = T0.astimezone(timezone(timedelta(hours=-7)))
    snap = snapshots.save(tmp_path, "epoch", "zip", b"", local)
    assert snap.path.name == "20261012T155800Z.zip"


def test_mark_bad_renames_and_keeps_the_bytes(tmp_path: Path) -> None:
    snap = snapshots.save(tmp_path, "arena", "json", b"abc", T0)
    bad = snapshots.mark_bad(snap)
    assert bad.path == tmp_path / "arena" / "20261012T155800Z.bad.json"
    assert bad.path.read_bytes() == b"abc"
    assert bad.bad is True
    assert bad.fetched_at == T0
    assert not snap.path.exists()


def test_list_is_newest_first_and_reads_bad_from_the_name(tmp_path: Path) -> None:
    first, second, third = _save_many(tmp_path, 3)
    snapshots.mark_bad(second)
    listed = snapshots.list_snapshots(tmp_path, "arena")
    assert [s.path.name for s in listed] == [
        "20261012T160000Z.json",
        "20261012T155900Z.bad.json",
        "20261012T155800Z.json",
    ]
    assert [s.bad for s in listed] == [False, True, False]


def test_good_snapshots_never_include_a_bad_one(tmp_path: Path) -> None:
    first, second = _save_many(tmp_path, 2)
    snapshots.mark_bad(second)
    assert [s.path for s in snapshots.good_snapshots(tmp_path, "arena")] == [first.path]


def test_a_source_with_no_directory_has_no_snapshots(tmp_path: Path) -> None:
    assert snapshots.list_snapshots(tmp_path, "arena") == []
    assert snapshots.good_snapshots(tmp_path, "arena") == []


def test_files_that_are_not_snapshots_are_ignored(tmp_path: Path) -> None:
    snap = snapshots.save(tmp_path, "arena", "json", b"{}", T0)
    (tmp_path / "arena" / "notes.txt").write_text("x")
    (tmp_path / "arena" / ".DS_Store").write_text("x")
    (tmp_path / "arena" / "20261399T999999Z.json").write_text("x")  # not a date
    assert snapshots.list_snapshots(tmp_path, "arena") == [snap]


def test_each_source_has_its_own_directory(tmp_path: Path) -> None:
    snapshots.save(tmp_path, "arena", "json", b"{}", T0)
    assert snapshots.list_snapshots(tmp_path, "epoch") == []


def test_prune_keeps_four_good_and_two_bad(tmp_path: Path) -> None:
    saved = _save_many(tmp_path, 10)
    for snap in saved[:4]:  # the four oldest become bad
        snapshots.mark_bad(snap)
    removed = snapshots.prune(tmp_path, "arena")
    left = snapshots.list_snapshots(tmp_path, "arena")
    assert [s.path.name for s in left if not s.bad] == [
        "20261012T160700Z.json",
        "20261012T160600Z.json",
        "20261012T160500Z.json",
        "20261012T160400Z.json",
    ]
    assert [s.path.name for s in left if s.bad] == [
        "20261012T160100Z.bad.json",
        "20261012T160000Z.bad.json",
    ]
    assert sorted(p.name for p in removed) == [
        "20261012T155800Z.bad.json",
        "20261012T155900Z.bad.json",
        "20261012T160200Z.json",
        "20261012T160300Z.json",
    ]


def test_prune_touches_one_source_only(tmp_path: Path) -> None:
    _save_many(tmp_path, 6, source="arena")
    _save_many(tmp_path, 6, source="epoch")
    snapshots.prune(tmp_path, "arena")
    assert len(snapshots.list_snapshots(tmp_path, "arena")) == 4
    assert len(snapshots.list_snapshots(tmp_path, "epoch")) == 6


def test_prune_with_nothing_to_remove_removes_nothing(tmp_path: Path) -> None:
    _save_many(tmp_path, 2)
    assert snapshots.prune(tmp_path, "arena") == []
    assert snapshots.prune(tmp_path, "epoch") == []
```

- [ ] **Step 2: Run the test and see it fail**

Run: `uv run --directory modeltiers pytest tests/test_snapshots.py -q`
Expected: FAIL at collection with `ImportError: cannot import name 'snapshots' from 'modeltiers'`.

- [ ] **Step 3: Write the snapshot store**

Write `modeltiers/src/modeltiers/snapshots.py`:

```python
"""The snapshot store: `<cache>/<source>/<UTC stamp>.<ext>`.

A snapshot that failed to parse is renamed `<UTC stamp>.bad.<ext>` and is
never read again. Any other file in a source's directory is ignored.
"""

import re
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path

STAMP_FORMAT = "%Y%m%dT%H%M%SZ"
KEEP_GOOD = 4
KEEP_BAD = 2
_NAME = re.compile(r"^(\d{8}T\d{6}Z)(\.bad)?\.[a-z0-9]+$")


@dataclass(frozen=True)
class Snapshot:
    path: Path
    fetched_at: datetime  # UTC, whole seconds: the stamp in the file name
    bad: bool


def _read_name(path: Path) -> Snapshot | None:
    match = _NAME.match(path.name)
    if match is None:
        return None
    try:
        fetched_at = datetime.strptime(match.group(1), STAMP_FORMAT).replace(tzinfo=UTC)
    except ValueError:
        return None
    return Snapshot(path=path, fetched_at=fetched_at, bad=match.group(2) is not None)


def save(cache: Path, source: str, ext: str, data: bytes, now: datetime) -> Snapshot:
    """Write the fetched bytes, before anything parses them."""
    stamp = now.astimezone(UTC).strftime(STAMP_FORMAT)
    directory = cache / source
    directory.mkdir(parents=True, exist_ok=True)
    path = directory / f"{stamp}.{ext}"
    path.write_bytes(data)
    snapshot = _read_name(path)
    assert snapshot is not None
    return snapshot


def mark_bad(snapshot: Snapshot) -> Snapshot:
    """Rename `<stamp>.<ext>` to `<stamp>.bad.<ext>`."""
    stamp, ext = snapshot.path.name.split(".", 1)
    target = snapshot.path.with_name(f"{stamp}.bad.{ext}")
    snapshot.path.replace(target)
    return Snapshot(path=target, fetched_at=snapshot.fetched_at, bad=True)


def list_snapshots(cache: Path, source: str) -> list[Snapshot]:
    """Every snapshot of one source, newest first."""
    directory = cache / source
    if not directory.is_dir():
        return []
    found = [s for s in map(_read_name, directory.iterdir()) if s is not None]
    return sorted(found, key=lambda s: s.path.name, reverse=True)


def good_snapshots(cache: Path, source: str) -> list[Snapshot]:
    """The snapshots not marked bad, newest first."""
    return [s for s in list_snapshots(cache, source) if not s.bad]


def prune(
    cache: Path, source: str, *, keep_good: int = KEEP_GOOD, keep_bad: int = KEEP_BAD
) -> list[Path]:
    """Delete all but the newest `keep_good` good and `keep_bad` bad snapshots
    of one source. Returns the deleted paths."""
    snapshots = list_snapshots(cache, source)
    good = [s for s in snapshots if not s.bad]
    bad = [s for s in snapshots if s.bad]
    removed = [s.path for s in good[keep_good:] + bad[keep_bad:]]
    for path in removed:
        path.unlink()
    return removed
```

- [ ] **Step 4: Run the test and see it pass**

Run: `uv run --directory modeltiers pytest tests/test_snapshots.py -q`
Expected: PASS, `11 passed`.

- [ ] **Step 5: Lint, format check and type check**

Run: `make -C modeltiers check`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add modeltiers/src/modeltiers/snapshots.py modeltiers/tests/test_snapshots.py
git commit -m "feat(modeltiers): a snapshot store with good, bad and pruning"
```

---

### Task 5: The models.dev and OpenRouter parsers

**Files:**
- Create: `modeltiers/src/modeltiers/sources/__init__.py`, `modeltiers/src/modeltiers/sources/_shape.py`, `modeltiers/src/modeltiers/sources/modelsdev.py`, `modeltiers/src/modeltiers/sources/openrouter.py`
- Test: `modeltiers/tests/test_source_modelsdev.py`, `modeltiers/tests/test_source_openrouter.py`, `modeltiers/tests/fixtures/modelsdev.json`, `modeltiers/tests/fixtures/modelsdev_too_few.json`, `modeltiers/tests/fixtures/modelsdev_malformed.json`, `modeltiers/tests/fixtures/openrouter.json`, `modeltiers/tests/fixtures/openrouter_too_few.json`, `modeltiers/tests/fixtures/openrouter_malformed.json`

**Interfaces:**
- Consumes: `modeltiers.errors.SourceParseError` (Task 1); `modeltiers.records.ModelsDev`, `ModelsDevRow`, `ScoreRecord` (Task 2); the `read_fixture` fixture (Task 1).
- Produces:
  - `modeltiers.sources._shape`: `load_json(source: str, data: bytes) -> Any`, `number(source: str, where: str, value: Any) -> float`, `optional_number(source: str, where: str, value: Any) -> float | None`, `require_rows(source: str, count: int, min_rows: int) -> None`. Each raises `SourceParseError`.
  - `modeltiers.sources.modelsdev`: `SOURCE = "modelsdev"`, `MIN_ROWS = 1000`, `parse(data: bytes, *, min_rows: int = MIN_ROWS) -> ModelsDev`.
  - `modeltiers.sources.openrouter`: `SOURCE = "openrouter"`, `MIN_ROWS = 100`, `parse(data: bytes, *, min_rows: int = MIN_ROWS) -> tuple[ScoreRecord, ...]`.

- [ ] **Step 1: Write the models.dev fixtures**

Eight provider-model rows across three providers. Two `ollama-cloud` rows have no `canonical_model_id`, as the real ones do.

Write `modeltiers/tests/fixtures/modelsdev.json`:

```json
{
 "ollama-cloud": {
  "id": "ollama-cloud",
  "name": "Ollama Cloud",
  "models": {
   "glm-5.3": {"id": "glm-5.3", "name": "GLM-5.3", "tool_call": true, "open_weights": true, "limit": {"context": 1048576, "output": 131072}, "canonical_model_id": "zhipuai/glm-5.3"},
   "deepseek-v4-pro": {"id": "deepseek-v4-pro", "name": "deepseek-v4-pro", "tool_call": true, "open_weights": true, "limit": {"context": 1048576, "output": 1048576}},
   "deepseek-v4-pro:0813": {"id": "deepseek-v4-pro:0813", "name": "DeepSeek V4 Pro 0813", "tool_call": true, "open_weights": true, "limit": {"context": 1048576, "output": 1048576}, "canonical_model_id": "deepseek/deepseek-v4-pro-0813"},
   "minimax-m3": {"id": "minimax-m3", "name": "minimax-m3", "tool_call": true, "open_weights": true, "limit": {"context": 512000, "output": 131072}}
  }
 },
 "openrouter": {
  "id": "openrouter",
  "name": "OpenRouter",
  "models": {
   "z-ai/glm-5.3": {"id": "z-ai/glm-5.3", "name": "GLM-5.3", "tool_call": true, "open_weights": true, "limit": {"context": 1048576, "output": 943718}, "canonical_model_id": "zhipuai/glm-5.3"},
   "anthropic/claude-opus-5.5": {"id": "anthropic/claude-opus-5.5", "name": "Claude Opus 5.5", "tool_call": true, "open_weights": false, "limit": {"context": 1000000, "output": 128000}, "canonical_model_id": "anthropic/claude-opus-5-5"},
   "minimax/minimax-m3": {"id": "minimax/minimax-m3", "name": "MiniMax-M3", "tool_call": true, "open_weights": true, "limit": {"context": 1048576, "output": 512000}, "canonical_model_id": "minimax/MiniMax-M3"}
  }
 },
 "zhipuai": {
  "id": "zhipuai",
  "name": "Zhipu AI",
  "models": {
   "glm-5.3": {"id": "glm-5.3", "name": "GLM-5.3", "tool_call": true, "open_weights": true, "limit": {"context": 1000000, "output": 131072}, "canonical_model_id": "zhipuai/glm-5.3"}
  }
 }
}
```

Write `modeltiers/tests/fixtures/modelsdev_too_few.json`:

```json
{
 "zhipuai": {
  "id": "zhipuai",
  "name": "Zhipu AI",
  "models": {
   "glm-5.3": {"id": "glm-5.3", "name": "GLM-5.3", "tool_call": true, "open_weights": true, "limit": {"context": 1000000, "output": 131072}, "canonical_model_id": "zhipuai/glm-5.3"}
  }
 }
}
```

Write `modeltiers/tests/fixtures/modelsdev_malformed.json`:

```json
{
 "ollama-cloud": {
  "id": "ollama-cloud",
  "name": "Ollama Cloud",
  "models": ["glm-5.3", "deepseek-v4-pro"]
 }
}
```

- [ ] **Step 2: Write the failing models.dev test**

Write `modeltiers/tests/test_source_modelsdev.py`:

```python
import json
from collections.abc import Callable
from typing import Any

import pytest

from modeltiers.errors import SourceParseError
from modeltiers.records import ModelsDevRow
from modeltiers.sources import modelsdev


def _doc(row: dict[str, Any]) -> bytes:
    return json.dumps({"ollama-cloud": {"models": {"m": row}}}).encode()


def test_golden(read_fixture: Callable[[str], bytes]) -> None:
    parsed = modelsdev.parse(read_fixture("modelsdev.json"), min_rows=8)
    assert len(parsed.rows) == 8
    assert parsed.rows[0] == ModelsDevRow(
        provider="ollama-cloud",
        model_id="glm-5.3",
        canonical="zhipuai/glm-5.3",
        tool_call=True,
        open_weights=True,
        context=1048576,
    )
    by_host = {(r.provider, r.model_id): r for r in parsed.rows}
    # A host row with no canonical_model_id is kept, with canonical None.
    assert by_host[("ollama-cloud", "deepseek-v4-pro")].canonical is None
    assert by_host[("ollama-cloud", "deepseek-v4-pro")].context == 1048576
    # The canonical key keeps models.dev's own spelling, capitals included.
    assert by_host[("openrouter", "minimax/minimax-m3")].canonical == "minimax/MiniMax-M3"
    assert by_host[("openrouter", "anthropic/claude-opus-5.5")].open_weights is False


def test_a_row_without_limit_context_has_no_context() -> None:
    for row in ({"tool_call": True}, {"tool_call": True, "limit": {"output": 8192}}):
        (parsed,) = modelsdev.parse(_doc(row), min_rows=1).rows
        assert parsed.context is None
        assert parsed.tool_call is True


def test_missing_flags_read_as_false() -> None:
    (parsed,) = modelsdev.parse(_doc({"limit": {"context": 4096}}), min_rows=1).rows
    assert parsed.tool_call is False
    assert parsed.open_weights is False
    assert parsed.canonical is None


def test_malformed_fixture_raises(read_fixture: Callable[[str], bytes]) -> None:
    with pytest.raises(SourceParseError, match="provider ollama-cloud has no models object"):
        modelsdev.parse(read_fixture("modelsdev_malformed.json"), min_rows=1)


def test_too_few_rows_raises(read_fixture: Callable[[str], bytes]) -> None:
    with pytest.raises(SourceParseError, match="1 rows, under the minimum of 2"):
        modelsdev.parse(read_fixture("modelsdev_too_few.json"), min_rows=2)


def test_the_default_minimum_is_the_specs() -> None:
    assert modelsdev.MIN_ROWS == 1000
    with pytest.raises(SourceParseError, match="under the minimum of 1000"):
        modelsdev.parse(_doc({"tool_call": True}))


@pytest.mark.parametrize(
    ("data", "message"),
    [
        (b'{"ollama-cloud": ', "not JSON"),
        (b"[]", "expected an object of providers"),
        (b'{"p": "x"}', "provider p has no models object"),
        (b'{"p": {"models": {"m": 7}}}', "p/m is not an object"),
        (_doc({"canonical_model_id": 7}), "canonical_model_id is not a string"),
        (_doc({"tool_call": "yes"}), "tool_call is not a boolean"),
        (_doc({"limit": []}), "limit is not an object"),
        (_doc({"limit": {"context": "1M"}}), "limit.context is not an integer"),
    ],
)
def test_an_unrecognised_shape_raises(data: bytes, message: str) -> None:
    with pytest.raises(SourceParseError, match=message):
        modelsdev.parse(data, min_rows=1)
```

- [ ] **Step 3: Run the test and see it fail**

Run: `uv run --directory modeltiers pytest tests/test_source_modelsdev.py -q`
Expected: FAIL at collection with `ModuleNotFoundError: No module named 'modeltiers.sources'`.

- [ ] **Step 4: Write the shared shape checks and the models.dev parser**

Write `modeltiers/src/modeltiers/sources/__init__.py`:

```python
"""One parser per public source: snapshot bytes in, records out."""
```

Write `modeltiers/src/modeltiers/sources/_shape.py`:

```python
"""Shape checks the parsers share. Each raises SourceParseError."""

import json
from typing import Any

from modeltiers.errors import SourceParseError


def load_json(source: str, data: bytes) -> Any:
    try:
        return json.loads(data)
    except ValueError as exc:
        raise SourceParseError(f"{source}: not JSON: {exc}") from exc


def number(source: str, where: str, value: Any) -> float:
    """A JSON number as a float. A bool, a string or null is not a number."""
    if isinstance(value, bool) or not isinstance(value, int | float):
        raise SourceParseError(f"{source}: {where} is not a number: {value!r}")
    return float(value)


def optional_number(source: str, where: str, value: Any) -> float | None:
    return None if value is None else number(source, where, value)


def require_rows(source: str, count: int, min_rows: int) -> None:
    if count < min_rows:
        raise SourceParseError(f"{source}: {count} rows, under the minimum of {min_rows}")
```

Write `modeltiers/src/modeltiers/sources/modelsdev.py`:

```python
"""models.dev `api.json`: an object of providers, each with a `models` object
keyed by the host's own model id."""

from modeltiers.errors import SourceParseError
from modeltiers.records import ModelsDev, ModelsDevRow
from modeltiers.sources._shape import load_json, require_rows

SOURCE = "modelsdev"
MIN_ROWS = 1000


def _flag(where: str, row: dict[str, object], key: str) -> bool:
    value = row.get(key, False)
    if not isinstance(value, bool):
        raise SourceParseError(f"{SOURCE}: {where}.{key} is not a boolean: {value!r}")
    return value


def _context(where: str, row: dict[str, object]) -> int | None:
    limit = row.get("limit")
    if limit is None:
        return None
    if not isinstance(limit, dict):
        raise SourceParseError(f"{SOURCE}: {where}.limit is not an object")
    context = limit.get("context")
    if context is None:
        return None
    if isinstance(context, bool) or not isinstance(context, int):
        raise SourceParseError(f"{SOURCE}: {where}.limit.context is not an integer: {context!r}")
    return context


def parse(data: bytes, *, min_rows: int = MIN_ROWS) -> ModelsDev:
    """Every provider-model row. A missing `tool_call` or `open_weights` reads
    as false, a missing `canonical_model_id` or `limit.context` as None."""
    doc = load_json(SOURCE, data)
    if not isinstance(doc, dict):
        raise SourceParseError(f"{SOURCE}: expected an object of providers")
    rows: list[ModelsDevRow] = []
    for provider, entry in doc.items():
        models = entry.get("models") if isinstance(entry, dict) else None
        if not isinstance(models, dict):
            raise SourceParseError(f"{SOURCE}: provider {provider} has no models object")
        for model_id, row in models.items():
            where = f"{provider}/{model_id}"
            if not isinstance(row, dict):
                raise SourceParseError(f"{SOURCE}: {where} is not an object")
            canonical = row.get("canonical_model_id")
            if canonical is not None and not isinstance(canonical, str):
                raise SourceParseError(f"{SOURCE}: {where}.canonical_model_id is not a string")
            rows.append(
                ModelsDevRow(
                    provider=provider,
                    model_id=model_id,
                    canonical=canonical or None,
                    tool_call=_flag(where, row, "tool_call"),
                    open_weights=_flag(where, row, "open_weights"),
                    context=_context(where, row),
                )
            )
    require_rows(SOURCE, len(rows), min_rows)
    return ModelsDev(rows=tuple(rows))
```

- [ ] **Step 5: Run the test and see it pass**

Run: `uv run --directory modeltiers pytest tests/test_source_modelsdev.py -q`
Expected: PASS, `14 passed`.

- [ ] **Step 6: Write the OpenRouter fixtures**

Six models. Four have an intelligence index; `z-ai/glm-5.3-prime` has no `benchmarks` key and `mistralai/mistral-large-4-0` has `artificial_analysis: null`.

Write `modeltiers/tests/fixtures/openrouter.json`:

```json
{
 "data": [
  {"id": "z-ai/glm-5.3", "name": "Z.ai: GLM 5.3", "context_length": 1048576, "benchmarks": {"artificial_analysis": {"intelligence_index": 44.8, "coding_index": 74.8, "agentic_index": 53.1}}},
  {"id": "z-ai/glm-5.3:batch", "name": "Z.ai: GLM 5.3 (batch)", "context_length": 1048576, "benchmarks": {"artificial_analysis": {"intelligence_index": 44.8, "coding_index": 74.8, "agentic_index": 53.1}}},
  {"id": "anthropic/claude-opus-5.5", "name": "Anthropic: Claude Opus 5.5", "context_length": 1000000, "benchmarks": {"artificial_analysis": {"intelligence_index": 57.6, "coding_index": null, "agentic_index": null}}},
  {"id": "liquid/lfm-2.5-2.6b:free", "name": "LiquidAI: LFM2.5-2.6B (free)", "context_length": 65536, "benchmarks": {"artificial_analysis": {"intelligence_index": 8.4, "coding_index": 7.7, "agentic_index": null}}},
  {"id": "z-ai/glm-5.3-prime", "name": "Z.ai: GLM 5.3 Prime", "context_length": 1000000},
  {"id": "mistralai/mistral-large-4-0", "name": "Mistral: Mistral Large 4", "context_length": 1048576, "benchmarks": {"artificial_analysis": null}}
 ]
}
```

Write `modeltiers/tests/fixtures/openrouter_too_few.json`:

```json
{
 "data": [
  {"id": "z-ai/glm-5.3", "name": "Z.ai: GLM 5.3", "context_length": 1048576, "benchmarks": {"artificial_analysis": {"intelligence_index": 44.8, "coding_index": 74.8, "agentic_index": 53.1}}},
  {"id": "z-ai/glm-5.3-prime", "name": "Z.ai: GLM 5.3 Prime", "context_length": 1000000}
 ]
}
```

Write `modeltiers/tests/fixtures/openrouter_malformed.json`:

```json
{
 "data": {
  "z-ai/glm-5.3": {"benchmarks": {"artificial_analysis": {"intelligence_index": 44.8}}}
 }
}
```

- [ ] **Step 7: Write the failing OpenRouter test**

Write `modeltiers/tests/test_source_openrouter.py`:

```python
import json
from collections.abc import Callable
from typing import Any

import pytest

from modeltiers.errors import SourceParseError
from modeltiers.records import ScoreRecord
from modeltiers.sources import openrouter


def _doc(model: Any) -> bytes:
    return json.dumps({"data": [model]}).encode()


def _scored(indices: Any) -> bytes:
    return _doc({"id": "v/m", "benchmarks": {"artificial_analysis": indices}})


def test_golden(read_fixture: Callable[[str], bytes]) -> None:
    records = openrouter.parse(read_fixture("openrouter.json"), min_rows=4)
    assert records[0] == ScoreRecord(
        source="openrouter",
        source_id="z-ai/glm-5.3",
        score=44.8,
        coding_index=74.8,
        agentic_index=53.1,
    )
    # Six models; the one with no benchmarks and the one whose
    # artificial_analysis is null have no score and give no record.
    assert [r.source_id for r in records] == [
        "z-ai/glm-5.3",
        "z-ai/glm-5.3:batch",
        "anthropic/claude-opus-5.5",
        "liquid/lfm-2.5-2.6b:free",
    ]


def test_null_coding_and_agentic_indices_are_none(read_fixture: Callable[[str], bytes]) -> None:
    records = openrouter.parse(read_fixture("openrouter.json"), min_rows=4)
    opus = records[2]
    assert (opus.score, opus.coding_index, opus.agentic_index) == (57.6, None, None)
    lfm = records[3]
    assert (lfm.coding_index, lfm.agentic_index) == (7.7, None)


def test_a_null_intelligence_index_gives_no_record() -> None:
    data = _scored({"intelligence_index": None, "coding_index": 50.0})
    with pytest.raises(SourceParseError, match="0 rows, under the minimum of 1"):
        openrouter.parse(data, min_rows=1)


def test_an_integer_index_is_a_float() -> None:
    (record,) = openrouter.parse(_scored({"intelligence_index": 25}), min_rows=1)
    assert record.score == 25.0
    assert isinstance(record.score, float)


def test_malformed_fixture_raises(read_fixture: Callable[[str], bytes]) -> None:
    with pytest.raises(SourceParseError, match="expected an object with a data list"):
        openrouter.parse(read_fixture("openrouter_malformed.json"), min_rows=1)


def test_too_few_rows_counts_scored_records(read_fixture: Callable[[str], bytes]) -> None:
    # Two models in the file, one with a score.
    with pytest.raises(SourceParseError, match="1 rows, under the minimum of 2"):
        openrouter.parse(read_fixture("openrouter_too_few.json"), min_rows=2)


def test_the_default_minimum_is_the_specs(read_fixture: Callable[[str], bytes]) -> None:
    assert openrouter.MIN_ROWS == 100
    with pytest.raises(SourceParseError, match="under the minimum of 100"):
        openrouter.parse(read_fixture("openrouter.json"))


@pytest.mark.parametrize(
    ("data", "message"),
    [
        (b"<html>", "not JSON"),
        (b"[]", "expected an object with a data list"),
        (_doc("z-ai/glm-5.3"), r"data\[0\] has no string id"),
        (_doc({"name": "no id"}), r"data\[0\] has no string id"),
        (_doc({"id": "v/m", "benchmarks": []}), "v/m.benchmarks is not an object"),
        (_scored([]), "v/m.artificial_analysis is not an object"),
        (_scored({"intelligence_index": "44.8"}), "v/m.intelligence_index is not a number"),
        (_scored({"intelligence_index": True}), "v/m.intelligence_index is not a number"),
        (
            _scored({"intelligence_index": 44.8, "coding_index": "x"}),
            "v/m.coding_index is not a number",
        ),
    ],
)
def test_an_unrecognised_shape_raises(data: bytes, message: str) -> None:
    with pytest.raises(SourceParseError, match=message):
        openrouter.parse(data, min_rows=1)
```

- [ ] **Step 8: Run the test and see it fail**

Run: `uv run --directory modeltiers pytest tests/test_source_openrouter.py -q`
Expected: FAIL at collection with `ImportError: cannot import name 'openrouter' from 'modeltiers.sources'`.

- [ ] **Step 9: Write the OpenRouter parser**

Write `modeltiers/src/modeltiers/sources/openrouter.py`:

```python
"""OpenRouter's models API: `data[].benchmarks.artificial_analysis`."""

from modeltiers.errors import SourceParseError
from modeltiers.records import ScoreRecord
from modeltiers.sources._shape import load_json, number, optional_number, require_rows

SOURCE = "openrouter"
MIN_ROWS = 100


def parse(data: bytes, *, min_rows: int = MIN_ROWS) -> tuple[ScoreRecord, ...]:
    """One record per model that has an intelligence index. A model with no
    `benchmarks`, no `artificial_analysis` or a null index has no score and
    gives no record. The minimum counts records, not models."""
    doc = load_json(SOURCE, data)
    models = doc.get("data") if isinstance(doc, dict) else None
    if not isinstance(models, list):
        raise SourceParseError(f"{SOURCE}: expected an object with a data list")
    records: list[ScoreRecord] = []
    for index, model in enumerate(models):
        if not isinstance(model, dict) or not isinstance(model.get("id"), str):
            raise SourceParseError(f"{SOURCE}: data[{index}] has no string id")
        model_id: str = model["id"]
        benchmarks = model.get("benchmarks")
        if benchmarks is None:
            continue
        if not isinstance(benchmarks, dict):
            raise SourceParseError(f"{SOURCE}: {model_id}.benchmarks is not an object")
        indices = benchmarks.get("artificial_analysis")
        if indices is None:
            continue
        if not isinstance(indices, dict):
            raise SourceParseError(f"{SOURCE}: {model_id}.artificial_analysis is not an object")
        if indices.get("intelligence_index") is None:
            continue
        records.append(
            ScoreRecord(
                source=SOURCE,
                source_id=model_id,
                score=number(
                    SOURCE, f"{model_id}.intelligence_index", indices["intelligence_index"]
                ),
                coding_index=optional_number(
                    SOURCE, f"{model_id}.coding_index", indices.get("coding_index")
                ),
                agentic_index=optional_number(
                    SOURCE, f"{model_id}.agentic_index", indices.get("agentic_index")
                ),
            )
        )
    require_rows(SOURCE, len(records), min_rows)
    return tuple(records)
```

- [ ] **Step 10: Run both tests and see them pass**

Run: `uv run --directory modeltiers pytest tests/test_source_modelsdev.py tests/test_source_openrouter.py -q`
Expected: PASS, `30 passed`.

- [ ] **Step 11: Lint, format check and type check**

Run: `make -C modeltiers check`
Expected: PASS.

- [ ] **Step 12: Commit**

```bash
git add modeltiers/src/modeltiers/sources/__init__.py modeltiers/src/modeltiers/sources/_shape.py modeltiers/src/modeltiers/sources/modelsdev.py modeltiers/src/modeltiers/sources/openrouter.py modeltiers/tests/test_source_modelsdev.py modeltiers/tests/test_source_openrouter.py modeltiers/tests/fixtures/modelsdev.json modeltiers/tests/fixtures/modelsdev_too_few.json modeltiers/tests/fixtures/modelsdev_malformed.json modeltiers/tests/fixtures/openrouter.json modeltiers/tests/fixtures/openrouter_too_few.json modeltiers/tests/fixtures/openrouter_malformed.json
git commit -m "feat(modeltiers): parse models.dev and the openrouter models api"
```

---

### Task 6: The Arena and Epoch parsers

**Files:**
- Create: `modeltiers/src/modeltiers/sources/arena.py`, `modeltiers/src/modeltiers/sources/epoch.py`
- Modify: `modeltiers/tests/conftest.py` (whole file replaced; adds the `epoch_zip` and `arena_doc` fixtures)
- Test: `modeltiers/tests/test_source_arena.py`, `modeltiers/tests/test_source_epoch.py`, `modeltiers/tests/fixtures/arena_malformed.json`, `modeltiers/tests/fixtures/eci_scores.csv`, `modeltiers/tests/fixtures/eci_scores_too_few.csv`, `modeltiers/tests/fixtures/eci_scores_malformed.csv`

**Interfaces:**
- Consumes: `modeltiers.errors.SourceParseError` (Task 1); `modeltiers.records.ScoreRecord` (Task 2); `modeltiers.fetch.wrap_arena_pages(pages: list[bytes]) -> bytes` (Task 3); `modeltiers.sources._shape.load_json`, `number`, `require_rows` (Task 5); the Arena page fixtures (Task 3).
- Produces:
  - `modeltiers.sources.arena`: `SOURCE = "arena"`, `MIN_ROWS = 20`, `EFFORTS: frozenset[str]` (`none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`), `split_effort(name: str) -> tuple[str, str | None]`, `parse(data: bytes, *, min_rows: int = MIN_ROWS) -> tuple[ScoreRecord, ...]` (takes the wrapper document).
  - `modeltiers.sources.epoch`: `SOURCE = "epoch"`, `MIN_ROWS = 100`, `MEMBER = "epoch_capabilities_index/eci_scores.csv"`, `parse(data: bytes, *, min_rows: int = MIN_ROWS) -> tuple[ScoreRecord, ...]` (takes the ZIP bytes).
  - `tests/conftest.py`: fixtures `epoch_zip -> Callable[[str], bytes]` (default `"eci_scores.csv"`) and `arena_doc -> Callable[..., bytes]` (default pages 1 and 2).

- [ ] **Step 1: Write the fixtures**

`arena_malformed.json` is a page body with no `rows`.

Write `modeltiers/tests/fixtures/arena_malformed.json`:

```json
{
 "features": [{"feature_idx": 0, "name": "model_name"}],
 "num_rows_total": 5
}
```

The Epoch fixtures are CSV text; the `epoch_zip` fixture zips one in memory. The last row of `eci_scores.csv` has an empty interval, as two real rows do.

Write `modeltiers/tests/fixtures/eci_scores.csv`:

```text
Model,Display name,eci,eci_ci_low,eci_ci_high,date,Organization,Country (of organization),Model accessibility,Accessibility group,model_versions
Claude Opus 5.5,Claude Opus 5.5,167.33,164.06,171.68,2026-09-22,Anthropic,United States of America,API access,Closed weights,
GLM-5.3,GLM-5.3,155.61,153.6,157.92,2026-08-14,Z.ai (Zhipu AI),China,API access,Closed weights,
DeepSeek V4 Pro 0813,DeepSeek V4 Pro 0813,155.31,153.48,157.45,2026-08-13,DeepSeek,China,Open weights (unrestricted),Open weights,
MiniMax-M3,MiniMax-M3,146.95,142.59,149.44,2026-06-01,MiniMax,China,Open weights (restricted use),Open weights,
GPT-5,GPT-5,150.0,,,2025-08-07,OpenAI,United States of America,API access,Closed weights,
```

Write `modeltiers/tests/fixtures/eci_scores_too_few.csv`:

```text
Model,Display name,eci,eci_ci_low,eci_ci_high,date,Organization,Country (of organization),Model accessibility,Accessibility group,model_versions
Claude Opus 5.5,Claude Opus 5.5,167.33,164.06,171.68,2026-09-22,Anthropic,United States of America,API access,Closed weights,
```

Write `modeltiers/tests/fixtures/eci_scores_malformed.csv`:

```text
Model,Display name,score,date
Claude Opus 5.5,Claude Opus 5.5,167.33,2026-09-22
GLM-5.3,GLM-5.3,155.61,2026-08-14
```

- [ ] **Step 2: Add the two fixture helpers to conftest**

Replace the whole of `modeltiers/tests/conftest.py` with:

```python
"""Shared fixtures: the guards that keep the suite off the network, off wt and
out of the real config and cache directories, plus the fixture-file helpers."""

import io
import subprocess
import urllib.error
import urllib.request
import zipfile
from collections.abc import Callable
from pathlib import Path
from typing import Any

import pytest

from modeltiers import fetch

FIXTURES = Path(__file__).parent / "fixtures"
EPOCH_MEMBER = "epoch_capabilities_index/eci_scores.csv"


def _no_network(*args: Any, **kwargs: Any) -> Any:
    raise urllib.error.URLError("hermetic test: no network")


def _no_subprocess(*args: Any, **kwargs: Any) -> Any:
    raise AssertionError(f"hermetic test: subprocess.run called with {args!r}")


@pytest.fixture(autouse=True)
def _hermetic(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    """No test may fetch, run wt, or read or write the real output file or the
    real snapshot cache. `urllib.request` and `subprocess` are each one shared
    module object, so these two patches hold for the whole process. A test
    that needs either replaces it with its own fake."""
    monkeypatch.setattr(urllib.request, "urlopen", _no_network)
    monkeypatch.setattr(subprocess, "run", _no_subprocess)
    home = tmp_path / "home"
    home.mkdir()
    monkeypatch.setenv("HOME", str(home))
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg-config"))
    monkeypatch.setenv("XDG_CACHE_HOME", str(tmp_path / "xdg-cache"))
    monkeypatch.setenv("MODELTIERS_OUT", str(tmp_path / "out" / "tiers.toml"))
    monkeypatch.setenv("MODELTIERS_CACHE", str(tmp_path / "cache"))


@pytest.fixture
def read_fixture() -> Callable[[str], bytes]:
    """The bytes of one file under tests/fixtures/."""

    def read(name: str) -> bytes:
        return (FIXTURES / name).read_bytes()

    return read


@pytest.fixture
def epoch_zip(read_fixture: Callable[[str], bytes]) -> Callable[[str], bytes]:
    """A ZIP archive holding one CSV fixture as Epoch's scores file. The CSVs
    are kept as text so they can be read and diffed."""

    def build(csv_name: str = "eci_scores.csv") -> bytes:
        buffer = io.BytesIO()
        with zipfile.ZipFile(buffer, "w") as archive:
            archive.writestr(EPOCH_MEMBER, read_fixture(csv_name))
        return buffer.getvalue()

    return build


@pytest.fixture
def arena_doc(read_fixture: Callable[[str], bytes]) -> Callable[..., bytes]:
    """An Arena snapshot: the named page fixtures in one wrapper document."""

    def build(*page_names: str) -> bytes:
        names = page_names or ("arena_page1.json", "arena_page2.json")
        return fetch.wrap_arena_pages([read_fixture(name) for name in names])

    return build
```

- [ ] **Step 3: Write the failing Arena test**

Write `modeltiers/tests/test_source_arena.py`:

```python
import json
from collections.abc import Callable
from typing import Any

import pytest

from modeltiers import fetch
from modeltiers.errors import SourceParseError
from modeltiers.records import ScoreRecord
from modeltiers.sources import arena


def _wrap(*pages: Any) -> bytes:
    return fetch.wrap_arena_pages([json.dumps(page).encode() for page in pages])


def _row(name: Any = "M", score: Any = 0.1, low: Any = 0.05) -> dict[str, Any]:
    return {"row": {"model_name": name, "score": score, "score_ci_lower": low}}


@pytest.mark.parametrize(
    ("name", "kept", "effort"),
    [
        ("Claude Opus 5.5 (High)", "Claude Opus 5.5", "high"),
        ("GLM 5.3 (Max)", "GLM 5.3", "max"),
        ("GPT 5.6 Sol (xHigh)", "GPT 5.6 Sol", "xhigh"),
        ("Qwen3.8 Max", "Qwen3.8 Max", None),  # max outside a parenthesis is the name
        ("DeepSeek V4 Pro (0813)", "DeepSeek V4 Pro (0813)", None),  # not an effort word
        ("DeepSeek V4 Pro (High) (0813)", "DeepSeek V4 Pro (0813)", "high"),
        ("Hy4 preview", "Hy4 preview", None),
        ("M (none)", "M", "none"),
        ("M (minimal)", "M", "minimal"),
        ("M (low)", "M", "low"),
        ("M ( Medium )", "M", "medium"),
    ],
)
def test_split_effort(name: str, kept: str, effort: str | None) -> None:
    assert arena.split_effort(name) == (kept, effort)


def test_golden_reads_both_pages_to_num_rows_total(arena_doc: Callable[..., bytes]) -> None:
    records = arena.parse(arena_doc(), min_rows=5)
    assert records[0] == ScoreRecord(
        source="arena",
        source_id="Claude Opus 5.5",
        score=0.14331280058415052,
        ci_lower=0.12150663912969831,
        effort="high",
    )
    assert [(r.source_id, r.effort) for r in records] == [
        ("Claude Opus 5.5", "high"),
        ("GLM 5.3", "max"),
        ("Qwen3.8 Max", None),
        ("DeepSeek V4 Pro (0813)", "high"),
        ("Minimax M3", None),
    ]
    assert records[4].score < 0  # the score is centred near zero


def test_fewer_rows_than_num_rows_total_raises(arena_doc: Callable[..., bytes]) -> None:
    # Page 1 alone: 3 rows, and it says the dataset has 5.
    with pytest.raises(SourceParseError, match="read 3 rows of num_rows_total 5"):
        arena.parse(arena_doc("arena_page1.json"), min_rows=1)


def test_an_error_body_is_a_failed_download(arena_doc: Callable[..., bytes]) -> None:
    with pytest.raises(SourceParseError, match="error body: the dataset index is loading"):
        arena.parse(arena_doc("arena_error.json"), min_rows=1)
    with pytest.raises(SourceParseError, match=r"pages\[1\] is an error body"):
        arena.parse(arena_doc("arena_page1.json", "arena_error.json"), min_rows=1)


def test_malformed_fixture_raises(arena_doc: Callable[..., bytes]) -> None:
    with pytest.raises(SourceParseError, match="no rows list or no num_rows_total"):
        arena.parse(arena_doc("arena_malformed.json"), min_rows=1)


def test_too_few_rows_raises(arena_doc: Callable[..., bytes]) -> None:
    with pytest.raises(SourceParseError, match="1 rows, under the minimum of 2"):
        arena.parse(arena_doc("arena_too_few.json"), min_rows=2)


def test_the_default_minimum_is_the_specs(arena_doc: Callable[..., bytes]) -> None:
    assert arena.MIN_ROWS == 20
    with pytest.raises(SourceParseError, match="under the minimum of 20"):
        arena.parse(arena_doc())


@pytest.mark.parametrize(
    ("data", "message"),
    [
        (b"<html>", "not JSON"),
        (b'{"rows": []}', "expected an object with a non-empty pages list"),
        (b'{"pages": []}', "expected an object with a non-empty pages list"),
        (b'{"pages": [{"rows": []}]}', r"pages\[0\] is not a string"),
        (b'{"pages": ["<html>"]}', "not JSON"),
        (b'{"pages": ["[]"]}', r"pages\[0\] is not a JSON object"),
        (_wrap({"rows": [7], "num_rows_total": 1}), "a row with no model_name"),
        (_wrap({"rows": [_row(name=None)], "num_rows_total": 1}), "a row with no model_name"),
        (_wrap({"rows": [_row(score=None)], "num_rows_total": 1}), "M.score is not a number"),
        (
            _wrap({"rows": [_row(low="0.05")], "num_rows_total": 1}),
            "M.score_ci_lower is not a number",
        ),
    ],
)
def test_an_unrecognised_shape_raises(data: bytes, message: str) -> None:
    with pytest.raises(SourceParseError, match=message):
        arena.parse(data, min_rows=1)
```

- [ ] **Step 4: Run the test and see it fail**

Run: `uv run --directory modeltiers pytest tests/test_source_arena.py -q`
Expected: FAIL at collection with `ImportError: cannot import name 'arena' from 'modeltiers.sources'`.

- [ ] **Step 5: Write the Arena parser**

Write `modeltiers/src/modeltiers/sources/arena.py`:

```python
"""Arena's agent leaderboard, as saved by `fetch.fetch_arena`: one JSON
document, `{"pages": [...]}`, holding each rows-API page body unparsed."""

import re

from modeltiers.errors import SourceParseError
from modeltiers.records import ScoreRecord
from modeltiers.sources._shape import load_json, number, require_rows

SOURCE = "arena"
MIN_ROWS = 20
EFFORTS = frozenset({"none", "minimal", "low", "medium", "high", "xhigh", "max"})
_PAREN = re.compile(r"\(([^()]*)\)")


def split_effort(name: str) -> tuple[str, str | None]:
    """Remove each parenthesis whose content is an effort word, whatever its
    case, and return the name left and the effort in lower case.

    `GLM 5.3 (Max)` gives (`GLM 5.3`, `max`); `DeepSeek V4 Pro (High) (0813)`
    gives (`DeepSeek V4 Pro (0813)`, `high`); `Qwen3.8 Max` is unchanged.
    """
    efforts: list[str] = []

    def drop(match: re.Match[str]) -> str:
        word = match.group(1).strip().lower()
        if word not in EFFORTS:
            return match.group(0)
        efforts.append(word)
        return ""

    kept = " ".join(_PAREN.sub(drop, name).split())
    return kept, (efforts[0] if efforts else None)


def parse(data: bytes, *, min_rows: int = MIN_ROWS) -> tuple[ScoreRecord, ...]:
    """One record per leaderboard row, across every page. The pages together
    must hold exactly `num_rows_total` rows; a page with an `error` key is a
    failed download."""
    doc = load_json(SOURCE, data)
    pages = doc.get("pages") if isinstance(doc, dict) else None
    if not isinstance(pages, list) or not pages:
        raise SourceParseError(f"{SOURCE}: expected an object with a non-empty pages list")
    records: list[ScoreRecord] = []
    total: int | None = None
    for index, body in enumerate(pages):
        if not isinstance(body, str):
            raise SourceParseError(f"{SOURCE}: pages[{index}] is not a string")
        page = load_json(SOURCE, body.encode("utf-8"))
        if not isinstance(page, dict):
            raise SourceParseError(f"{SOURCE}: pages[{index}] is not a JSON object")
        if "error" in page:
            raise SourceParseError(f"{SOURCE}: pages[{index}] is an error body: {page['error']}")
        rows = page.get("rows")
        page_total = page.get("num_rows_total")
        if not isinstance(rows, list) or not isinstance(page_total, int):
            raise SourceParseError(
                f"{SOURCE}: pages[{index}] has no rows list or no num_rows_total"
            )
        total = page_total
        for item in rows:
            row = item.get("row") if isinstance(item, dict) else None
            if not isinstance(row, dict) or not isinstance(row.get("model_name"), str):
                raise SourceParseError(f"{SOURCE}: pages[{index}] has a row with no model_name")
            name, effort = split_effort(row["model_name"])
            where = row["model_name"]
            records.append(
                ScoreRecord(
                    source=SOURCE,
                    source_id=name,
                    score=number(SOURCE, f"{where}.score", row.get("score")),
                    ci_lower=number(SOURCE, f"{where}.score_ci_lower", row.get("score_ci_lower")),
                    effort=effort,
                )
            )
    if len(records) != total:
        raise SourceParseError(f"{SOURCE}: read {len(records)} rows of num_rows_total {total}")
    require_rows(SOURCE, len(records), min_rows)
    return tuple(records)
```

- [ ] **Step 6: Run the test and see it pass**

Run: `uv run --directory modeltiers pytest tests/test_source_arena.py -q`
Expected: PASS, `27 passed`.

- [ ] **Step 7: Write the failing Epoch test**

Write `modeltiers/tests/test_source_epoch.py`:

```python
import io
import zipfile
from collections.abc import Callable

import pytest

from modeltiers.errors import SourceParseError
from modeltiers.records import ScoreRecord
from modeltiers.sources import epoch


def _zip(member: str, content: bytes) -> bytes:
    buffer = io.BytesIO()
    with zipfile.ZipFile(buffer, "w") as archive:
        archive.writestr(member, content)
    return buffer.getvalue()


def _csv(*lines: str) -> bytes:
    text = "\n".join(("Display name,eci,eci_ci_low", *lines)) + "\n"
    return _zip(epoch.MEMBER, text.encode())


def test_golden(epoch_zip: Callable[..., bytes]) -> None:
    records = epoch.parse(epoch_zip(), min_rows=5)
    assert records[0] == ScoreRecord(
        source="epoch", source_id="Claude Opus 5.5", score=167.33, ci_lower=164.06
    )
    assert [(r.source_id, r.score) for r in records] == [
        ("Claude Opus 5.5", 167.33),
        ("GLM-5.3", 155.61),
        ("DeepSeek V4 Pro 0813", 155.31),
        ("MiniMax-M3", 146.95),
        ("GPT-5", 150.0),
    ]
    assert all(r.effort is None for r in records)


def test_an_empty_interval_is_none(epoch_zip: Callable[..., bytes]) -> None:
    records = epoch.parse(epoch_zip(), min_rows=5)
    assert records[4].ci_lower is None


def test_a_byte_order_mark_is_ignored() -> None:
    data = _zip(epoch.MEMBER, b"\xef\xbb\xbfDisplay name,eci,eci_ci_low\nM,150.5,149\n")
    (record,) = epoch.parse(data, min_rows=1)
    assert (record.source_id, record.score, record.ci_lower) == ("M", 150.5, 149.0)


def test_a_row_without_a_name_or_a_score_gives_no_record() -> None:
    records = epoch.parse(_csv("A,150,149", ",151,150", "C,,"), min_rows=1)
    assert [r.source_id for r in records] == ["A"]


def test_malformed_fixture_raises(epoch_zip: Callable[..., bytes]) -> None:
    with pytest.raises(SourceParseError, match="has no 'eci' column"):
        epoch.parse(epoch_zip("eci_scores_malformed.csv"), min_rows=1)


def test_too_few_rows_raises(epoch_zip: Callable[..., bytes]) -> None:
    with pytest.raises(SourceParseError, match="1 rows, under the minimum of 2"):
        epoch.parse(epoch_zip("eci_scores_too_few.csv"), min_rows=2)


def test_the_default_minimum_is_the_specs(epoch_zip: Callable[..., bytes]) -> None:
    assert epoch.MIN_ROWS == 100
    with pytest.raises(SourceParseError, match="under the minimum of 100"):
        epoch.parse(epoch_zip())


@pytest.mark.parametrize(
    ("data", "message"),
    [
        (b"<html>502 Bad Gateway</html>", "not a ZIP archive"),
        (_zip("README.md", b"x"), "the archive has no epoch_capabilities_index/eci_scores.csv"),
        (_zip(epoch.MEMBER, b"\xff\xfe\x00"), "is not UTF-8"),
        (_zip(epoch.MEMBER, b""), "has no 'Display name' column"),
        (_csv("M,high,149"), "M.eci is not a number"),
        (_csv("M,150,low"), "M.eci_ci_low is not a number"),
    ],
)
def test_an_unrecognised_shape_raises(data: bytes, message: str) -> None:
    with pytest.raises(SourceParseError, match=message):
        epoch.parse(data, min_rows=1)
```

- [ ] **Step 8: Run the test and see it fail**

Run: `uv run --directory modeltiers pytest tests/test_source_epoch.py -q`
Expected: FAIL at collection with `ImportError: cannot import name 'epoch' from 'modeltiers.sources'`.

- [ ] **Step 9: Write the Epoch parser**

Write `modeltiers/src/modeltiers/sources/epoch.py`:

```python
"""Epoch AI's benchmark ZIP: `epoch_capabilities_index/eci_scores.csv`."""

import csv
import io
import zipfile

from modeltiers.errors import SourceParseError
from modeltiers.records import ScoreRecord
from modeltiers.sources._shape import require_rows

SOURCE = "epoch"
MIN_ROWS = 100
MEMBER = "epoch_capabilities_index/eci_scores.csv"
NAME_COLUMN = "Display name"
SCORE_COLUMN = "eci"
CI_LOW_COLUMN = "eci_ci_low"


def _float(where: str, text: str) -> float:
    try:
        return float(text)
    except ValueError as exc:
        raise SourceParseError(f"{SOURCE}: {where} is not a number: {text!r}") from exc


def parse(data: bytes, *, min_rows: int = MIN_ROWS) -> tuple[ScoreRecord, ...]:
    """One record per CSV row that has a name and an `eci` value. An empty
    `eci_ci_low` reads as None."""
    try:
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            text = archive.read(MEMBER).decode("utf-8-sig")
    except zipfile.BadZipFile as exc:
        raise SourceParseError(f"{SOURCE}: not a ZIP archive") from exc
    except KeyError as exc:
        raise SourceParseError(f"{SOURCE}: the archive has no {MEMBER}") from exc
    except UnicodeDecodeError as exc:
        raise SourceParseError(f"{SOURCE}: {MEMBER} is not UTF-8") from exc
    reader = csv.DictReader(io.StringIO(text))
    columns = reader.fieldnames or []
    for column in (NAME_COLUMN, SCORE_COLUMN, CI_LOW_COLUMN):
        if column not in columns:
            raise SourceParseError(f"{SOURCE}: {MEMBER} has no {column!r} column")
    records: list[ScoreRecord] = []
    for row in reader:
        name = (row[NAME_COLUMN] or "").strip()
        score = (row[SCORE_COLUMN] or "").strip()
        if not name or not score:
            continue
        ci_low = (row[CI_LOW_COLUMN] or "").strip()
        records.append(
            ScoreRecord(
                source=SOURCE,
                source_id=name,
                score=_float(f"{name}.eci", score),
                ci_lower=_float(f"{name}.eci_ci_low", ci_low) if ci_low else None,
            )
        )
    require_rows(SOURCE, len(records), min_rows)
    return tuple(records)
```

- [ ] **Step 10: Run both tests and see them pass**

Run: `uv run --directory modeltiers pytest tests/test_source_arena.py tests/test_source_epoch.py -q`
Expected: PASS, `40 passed`.

- [ ] **Step 11: Lint, format check and type check**

Run: `make -C modeltiers check`
Expected: PASS.

- [ ] **Step 12: Commit**

```bash
git add modeltiers/src/modeltiers/sources/arena.py modeltiers/src/modeltiers/sources/epoch.py modeltiers/tests/conftest.py modeltiers/tests/test_source_arena.py modeltiers/tests/test_source_epoch.py modeltiers/tests/fixtures/arena_malformed.json modeltiers/tests/fixtures/eci_scores.csv modeltiers/tests/fixtures/eci_scores_too_few.csv modeltiers/tests/fixtures/eci_scores_malformed.csv
git commit -m "feat(modeltiers): parse the arena agent board and the epoch capabilities index"
```

---

### Task 7: The source loader, with snapshot fallback

**Files:**
- Create: `modeltiers/src/modeltiers/loader.py`
- Modify: `modeltiers/tests/conftest.py` (one import line replaced; two fixtures appended)
- Test: `modeltiers/tests/test_loader.py`

**Interfaces:**
- Consumes: `modeltiers.fetch.Fetcher`, `Sleep`, `fetch_modelsdev`, `fetch_openrouter`, `fetch_arena`, `fetch_epoch`, `MODELSDEV_URL`, `OPENROUTER_URL`, `EPOCH_URL`, `arena_page_url` (Task 3); `modeltiers.snapshots.save`, `mark_bad`, `good_snapshots`, `list_snapshots` (Task 4); `parse`, `SOURCE` and `MIN_ROWS` of the four `modeltiers.sources` modules (Tasks 5 and 6); `modeltiers.errors.FetchError`, `SourceParseError`, `SourceUnavailable` (Task 1); `modeltiers.records.ModelsDev`, `ScoreRecord` (Task 2); `fakes.FakeFetcher`, `fakes.Sleeps` (Task 3); the `read_fixture`, `epoch_zip` and `arena_doc` fixtures (Tasks 1 and 6).
- Produces:
  - `modeltiers.loader`: `Warn = Callable[[str], None]`; `ScoreRecords = tuple[ScoreRecord, ...]`; `SourceSpec[T](name: str, ext: str, fetch: Callable[[Fetcher, Sleep], bytes], parse: Callable[[bytes], T], count: Callable[[T], int])`; `SourceSpecs(modelsdev: SourceSpec[ModelsDev], openrouter: SourceSpec[ScoreRecords], arena: SourceSpec[ScoreRecords], epoch: SourceSpec[ScoreRecords])`; `SourceResult[T](name: str, status: str, fallback: bool, fetched_at: datetime, records: int, data: T, snapshot: Path | None)`; `Sources(modelsdev: SourceResult[ModelsDev], openrouter: SourceResult[ScoreRecords], arena: SourceResult[ScoreRecords], epoch: SourceResult[ScoreRecords])` with `results() -> tuple[SourceResult[ModelsDev] | SourceResult[ScoreRecords], ...]`; all four frozen dataclasses.
  - `build_specs(*, modelsdev_min: int = 1000, openrouter_min: int = 100, arena_min: int = 20, epoch_min: int = 100) -> SourceSpecs`.
  - `load_source[T](spec: SourceSpec[T], *, cache: Path, fetcher: Fetcher, sleep: Sleep, now: datetime, offline: bool, warn: Warn) -> SourceResult[T]`; raises `SourceUnavailable([spec.name])`.
  - `load_all(specs: SourceSpecs, *, cache: Path, fetcher: Fetcher, sleep: Sleep, now: datetime, offline: bool, warn: Warn) -> Sources`; raises one `SourceUnavailable` naming every failed source.
  - `tests/conftest.py`: fixtures `specs -> SourceSpecs` and `good_answers -> dict[str, list[bytes | Exception]]`.
  - `SourceResult.status` is `"ok"` or `"stale"`. `fallback` is `True` only when this run's download or parse failed; an `--offline` read is `stale` with `fallback` `False`.

- [ ] **Step 1: Add the `specs` and `good_answers` fixtures to conftest**

In `modeltiers/tests/conftest.py`, replace:

```python
from modeltiers import fetch
```

with:

```python
from modeltiers import fetch, loader
from modeltiers.loader import SourceSpecs
```

Append to the end of `modeltiers/tests/conftest.py`, after two blank lines:

```python
@pytest.fixture
def specs() -> SourceSpecs:
    """The four sources with minimums that fit the golden fixtures."""
    return loader.build_specs(modelsdev_min=8, openrouter_min=4, arena_min=5, epoch_min=5)


@pytest.fixture
def good_answers(
    read_fixture: Callable[[str], bytes], epoch_zip: Callable[..., bytes]
) -> dict[str, list[bytes | Exception]]:
    """What a FakeFetcher needs for one clean run of all four sources: each
    URL answered once from the golden fixtures. A test edits a queue to make
    one source fail."""
    return {
        fetch.MODELSDEV_URL: [read_fixture("modelsdev.json")],
        fetch.OPENROUTER_URL: [read_fixture("openrouter.json")],
        fetch.arena_page_url(0): [read_fixture("arena_page1.json")],
        fetch.arena_page_url(3): [read_fixture("arena_page2.json")],
        fetch.EPOCH_URL: [epoch_zip()],
    }
```

- [ ] **Step 2: Write the failing test**

Write `modeltiers/tests/test_loader.py`:

```python
from collections.abc import Callable
from datetime import UTC, datetime, timedelta
from pathlib import Path

import pytest
from fakes import FakeFetcher, Sleeps

from modeltiers import fetch, loader, snapshots
from modeltiers.errors import FetchError, SourceParseError, SourceUnavailable
from modeltiers.loader import SourceSpecs

T0 = datetime(2026, 10, 12, 15, 58, 0, tzinfo=UTC)
T1 = T0 + timedelta(days=7)
Answers = dict[str, list[bytes | Exception]]


class Warnings:
    def __init__(self) -> None:
        self.lines: list[str] = []

    def __call__(self, line: str) -> None:
        self.lines.append(line)


def _load_openrouter(
    specs: SourceSpecs,
    cache: Path,
    answers: list[bytes | Exception],
    *,
    now: datetime = T0,
    offline: bool = False,
) -> tuple[loader.SourceResult[loader.ScoreRecords], Warnings, FakeFetcher]:
    fetcher = FakeFetcher({fetch.OPENROUTER_URL: answers})
    warnings = Warnings()
    result = loader.load_source(
        specs.openrouter,
        cache=cache,
        fetcher=fetcher,
        sleep=Sleeps(),
        now=now,
        offline=offline,
        warn=warnings,
    )
    return result, warnings, fetcher


def test_a_clean_fetch_is_ok_and_saved(
    specs: SourceSpecs, tmp_path: Path, read_fixture: Callable[[str], bytes]
) -> None:
    body = read_fixture("openrouter.json")
    result, warnings, _ = _load_openrouter(specs, tmp_path, [body])
    assert (result.name, result.status, result.fallback) == ("openrouter", "ok", False)
    assert result.fetched_at == T0
    assert result.records == 4
    assert result.data[0].source_id == "z-ai/glm-5.3"
    assert result.snapshot == tmp_path / "openrouter" / "20261012T155800Z.json"
    assert result.snapshot.read_bytes() == body
    assert warnings.lines == []


def test_a_failed_download_falls_back_to_the_newest_good_snapshot(
    specs: SourceSpecs, tmp_path: Path, read_fixture: Callable[[str], bytes]
) -> None:
    _load_openrouter(specs, tmp_path, [read_fixture("openrouter.json")], now=T0)
    result, warnings, _ = _load_openrouter(specs, tmp_path, [FetchError("HTTP 404")], now=T1)
    assert (result.status, result.fallback) == ("stale", True)
    assert result.fetched_at == T0  # the snapshot's own time, not this run's
    assert result.records == 4
    assert result.snapshot == tmp_path / "openrouter" / "20261012T155800Z.json"
    assert warnings.lines == ["openrouter: download failed: HTTP 404"]
    # A failed download saves nothing.
    assert len(snapshots.list_snapshots(tmp_path, "openrouter")) == 1


def test_a_snapshot_that_does_not_parse_is_set_aside_and_the_run_falls_back(
    specs: SourceSpecs, tmp_path: Path, read_fixture: Callable[[str], bytes]
) -> None:
    _load_openrouter(specs, tmp_path, [read_fixture("openrouter.json")], now=T0)
    malformed = read_fixture("openrouter_malformed.json")
    result, warnings, _ = _load_openrouter(specs, tmp_path, [malformed], now=T1)
    assert (result.status, result.fallback, result.fetched_at) == ("stale", True, T0)
    bad = tmp_path / "openrouter" / "20261019T155800Z.bad.json"
    assert bad.read_bytes() == malformed  # the bytes are kept for a look
    assert warnings.lines == [
        f"openrouter: expected an object with a data list; snapshot set aside as {bad}"
    ]


def test_a_snapshot_under_its_row_minimum_is_bad(
    specs: SourceSpecs, tmp_path: Path, read_fixture: Callable[[str], bytes]
) -> None:
    _load_openrouter(specs, tmp_path, [read_fixture("openrouter.json")], now=T0)
    result, warnings, _ = _load_openrouter(
        specs, tmp_path, [read_fixture("openrouter_too_few.json")], now=T1
    )
    assert (result.status, result.fallback) == ("stale", True)
    assert (tmp_path / "openrouter" / "20261019T155800Z.bad.json").exists()
    assert "1 rows, under the minimum of 4" in warnings.lines[0]


def test_a_failure_with_no_good_snapshot_is_unavailable(
    specs: SourceSpecs, tmp_path: Path, read_fixture: Callable[[str], bytes]
) -> None:
    with pytest.raises(SourceUnavailable, match="openrouter") as caught:
        _load_openrouter(specs, tmp_path, [FetchError("HTTP 404")])
    assert caught.value.names == ["openrouter"]
    with pytest.raises(SourceUnavailable):
        _load_openrouter(specs, tmp_path, [read_fixture("openrouter_malformed.json")])


def test_offline_reads_the_newest_good_snapshot_and_fetches_nothing(
    specs: SourceSpecs, tmp_path: Path, read_fixture: Callable[[str], bytes]
) -> None:
    _load_openrouter(specs, tmp_path, [read_fixture("openrouter.json")], now=T0)
    result, warnings, fetcher = _load_openrouter(specs, tmp_path, [], now=T1, offline=True)
    # Stale, and not a fallback: nothing failed, so this is not exit code 2.
    assert (result.status, result.fallback, result.fetched_at) == ("stale", False, T0)
    assert fetcher.calls == []
    assert warnings.lines == []
    assert len(snapshots.list_snapshots(tmp_path, "openrouter")) == 1


def test_offline_skips_a_bad_snapshot(
    specs: SourceSpecs, tmp_path: Path, read_fixture: Callable[[str], bytes]
) -> None:
    _load_openrouter(specs, tmp_path, [read_fixture("openrouter.json")], now=T0)
    _load_openrouter(specs, tmp_path, [read_fixture("openrouter_malformed.json")], now=T1)
    result, _, _ = _load_openrouter(specs, tmp_path, [], offline=True)
    assert result.fetched_at == T0
    assert result.snapshot == tmp_path / "openrouter" / "20261012T155800Z.json"


def test_offline_with_no_good_snapshot_is_unavailable(specs: SourceSpecs, tmp_path: Path) -> None:
    with pytest.raises(SourceUnavailable, match="openrouter"):
        _load_openrouter(specs, tmp_path, [], offline=True)


def test_a_good_snapshot_that_no_longer_parses_is_set_aside_and_the_next_is_used(
    specs: SourceSpecs, tmp_path: Path, read_fixture: Callable[[str], bytes]
) -> None:
    # A snapshot saved as good by an older version, in a shape this parser refuses.
    _load_openrouter(specs, tmp_path, [read_fixture("openrouter.json")], now=T0)
    snapshots.save(tmp_path, "openrouter", "json", b'{"models": []}', T1)
    result, warnings, _ = _load_openrouter(specs, tmp_path, [], offline=True)
    assert result.fetched_at == T0
    assert (tmp_path / "openrouter" / "20261019T155800Z.bad.json").exists()
    assert len(warnings.lines) == 1


def test_load_all_reads_the_four_sources(
    specs: SourceSpecs, tmp_path: Path, good_answers: Answers
) -> None:
    fetcher = FakeFetcher(good_answers)
    sources = loader.load_all(
        specs,
        cache=tmp_path,
        fetcher=fetcher,
        sleep=Sleeps(),
        now=T0,
        offline=False,
        warn=Warnings(),
    )
    assert [(r.name, r.status, r.records) for r in sources.results()] == [
        ("modelsdev", "ok", 8),
        ("openrouter", "ok", 4),
        ("arena", "ok", 5),
        ("epoch", "ok", 5),
    ]
    assert sources.modelsdev.data.rows[0].canonical == "zhipuai/glm-5.3"
    assert sources.arena.data[0].effort == "high"
    assert sources.epoch.snapshot == tmp_path / "epoch" / "20261012T155800Z.zip"
    # Arena's two pages are one snapshot file.
    assert [s.path.name for s in snapshots.list_snapshots(tmp_path, "arena")] == [
        "20261012T155800Z.json"
    ]


def test_load_all_names_every_unavailable_source_and_still_saves_the_rest(
    specs: SourceSpecs, tmp_path: Path, good_answers: Answers
) -> None:
    good_answers[fetch.OPENROUTER_URL] = [FetchError("HTTP 404")]
    good_answers[fetch.EPOCH_URL] = [b"<html>not a zip</html>"]
    warnings = Warnings()
    with pytest.raises(SourceUnavailable) as caught:
        loader.load_all(
            specs,
            cache=tmp_path,
            fetcher=FakeFetcher(good_answers),
            sleep=Sleeps(),
            now=T0,
            offline=False,
            warn=warnings,
        )
    assert caught.value.names == ["openrouter", "epoch"]
    assert str(caught.value) == "no usable data and no good snapshot for: openrouter, epoch"
    # The sources that did answer are saved, so the next run can fall back on them.
    assert len(snapshots.good_snapshots(tmp_path, "modelsdev")) == 1
    assert len(snapshots.good_snapshots(tmp_path, "arena")) == 1


def test_build_specs_defaults_to_the_specs_minimums(
    read_fixture: Callable[[str], bytes],
    arena_doc: Callable[..., bytes],
    epoch_zip: Callable[..., bytes],
) -> None:
    defaults = loader.build_specs()
    golden = {
        "modelsdev": (defaults.modelsdev, "json", read_fixture("modelsdev.json"), 1000),
        "openrouter": (defaults.openrouter, "json", read_fixture("openrouter.json"), 100),
        "arena": (defaults.arena, "json", arena_doc(), 20),
        "epoch": (defaults.epoch, "zip", epoch_zip(), 100),
    }
    for name, (spec, ext, body, minimum) in golden.items():
        assert (spec.name, spec.ext) == (name, ext)
        with pytest.raises(SourceParseError, match=f"under the minimum of {minimum}"):
            spec.parse(body)
```

- [ ] **Step 3: Run the test and see it fail**

Run: `uv run --directory modeltiers pytest tests/test_loader.py -q`
Expected: FAIL while loading `tests/conftest.py` with `ImportError: cannot import name 'loader' from 'modeltiers'`.

- [ ] **Step 4: Write the loader**

Write `modeltiers/src/modeltiers/loader.py`:

```python
"""One source from download to records: fetch, save the snapshot, parse, and
fall back to the newest good snapshot when either step fails."""

from collections.abc import Callable
from dataclasses import dataclass
from datetime import datetime
from functools import partial
from pathlib import Path

from modeltiers import fetch, snapshots
from modeltiers.errors import FetchError, SourceParseError, SourceUnavailable
from modeltiers.fetch import Fetcher, Sleep
from modeltiers.records import ModelsDev, ScoreRecord
from modeltiers.sources import arena, epoch, modelsdev, openrouter

Warn = Callable[[str], None]
ScoreRecords = tuple[ScoreRecord, ...]


@dataclass(frozen=True)
class SourceSpec[T]:
    """Everything the loader needs to know about one source."""

    name: str  # the snapshot directory and the key in [sources.*]
    ext: str  # the snapshot file extension
    fetch: Callable[[Fetcher, Sleep], bytes]
    parse: Callable[[bytes], T]
    count: Callable[[T], int]


@dataclass(frozen=True)
class SourceSpecs:
    modelsdev: SourceSpec[ModelsDev]
    openrouter: SourceSpec[ScoreRecords]
    arena: SourceSpec[ScoreRecords]
    epoch: SourceSpec[ScoreRecords]


@dataclass(frozen=True)
class SourceResult[T]:
    name: str
    status: str  # "ok" | "stale"
    fallback: bool  # stale because this run's download or parse failed
    fetched_at: datetime  # when the bytes in `data` were downloaded
    records: int
    data: T
    snapshot: Path | None  # the file the data was read from or saved to


@dataclass(frozen=True)
class Sources:
    modelsdev: SourceResult[ModelsDev]
    openrouter: SourceResult[ScoreRecords]
    arena: SourceResult[ScoreRecords]
    epoch: SourceResult[ScoreRecords]

    def results(self) -> tuple[SourceResult[ModelsDev] | SourceResult[ScoreRecords], ...]:
        """The four results, in the order the file and the report list them."""
        return (self.modelsdev, self.openrouter, self.arena, self.epoch)


def _count_rows(data: ModelsDev) -> int:
    return len(data.rows)


def build_specs(
    *,
    modelsdev_min: int = modelsdev.MIN_ROWS,
    openrouter_min: int = openrouter.MIN_ROWS,
    arena_min: int = arena.MIN_ROWS,
    epoch_min: int = epoch.MIN_ROWS,
) -> SourceSpecs:
    """The four sources. The minimums default to the spec's constants; a test
    passes small ones to match its fixtures."""
    return SourceSpecs(
        modelsdev=SourceSpec(
            name=modelsdev.SOURCE,
            ext="json",
            fetch=fetch.fetch_modelsdev,
            parse=partial(modelsdev.parse, min_rows=modelsdev_min),
            count=_count_rows,
        ),
        openrouter=SourceSpec(
            name=openrouter.SOURCE,
            ext="json",
            fetch=fetch.fetch_openrouter,
            parse=partial(openrouter.parse, min_rows=openrouter_min),
            count=len,
        ),
        arena=SourceSpec(
            name=arena.SOURCE,
            ext="json",
            fetch=fetch.fetch_arena,
            parse=partial(arena.parse, min_rows=arena_min),
            count=len,
        ),
        epoch=SourceSpec(
            name=epoch.SOURCE,
            ext="zip",
            fetch=fetch.fetch_epoch,
            parse=partial(epoch.parse, min_rows=epoch_min),
            count=len,
        ),
    )


def _from_snapshots[T](
    spec: SourceSpec[T], cache: Path, *, fallback: bool, warn: Warn
) -> SourceResult[T]:
    """The newest good snapshot that still parses. One that does not is
    renamed `.bad` and the next is tried."""
    for snapshot in snapshots.good_snapshots(cache, spec.name):
        try:
            data = spec.parse(snapshot.path.read_bytes())
        except SourceParseError as exc:
            bad = snapshots.mark_bad(snapshot)
            warn(f"{exc}; snapshot set aside as {bad.path}")
            continue
        return SourceResult(
            name=spec.name,
            status="stale",
            fallback=fallback,
            fetched_at=snapshot.fetched_at,
            records=spec.count(data),
            data=data,
            snapshot=snapshot.path,
        )
    raise SourceUnavailable([spec.name])


def load_source[T](
    spec: SourceSpec[T],
    *,
    cache: Path,
    fetcher: Fetcher,
    sleep: Sleep,
    now: datetime,
    offline: bool,
    warn: Warn,
) -> SourceResult[T]:
    """Load one source.

    Online: download, save the bytes, parse. A snapshot that does not parse is
    renamed `.bad` and its path is passed to `warn`. When the download or the
    parse fails, the newest good snapshot is used and the result is `stale`
    with `fallback` set. Offline: nothing is fetched or saved; the newest good
    snapshot is used and the result is `stale` without `fallback`. With no
    good snapshot either way, SourceUnavailable is raised.
    """
    if offline:
        return _from_snapshots(spec, cache, fallback=False, warn=warn)
    try:
        body = spec.fetch(fetcher, sleep)
    except FetchError as exc:
        warn(f"{spec.name}: download failed: {exc}")
        return _from_snapshots(spec, cache, fallback=True, warn=warn)
    snapshot = snapshots.save(cache, spec.name, spec.ext, body, now)
    try:
        data = spec.parse(body)
    except SourceParseError as exc:
        bad = snapshots.mark_bad(snapshot)
        warn(f"{exc}; snapshot set aside as {bad.path}")
        return _from_snapshots(spec, cache, fallback=True, warn=warn)
    return SourceResult(
        name=spec.name,
        status="ok",
        fallback=False,
        fetched_at=snapshot.fetched_at,
        records=spec.count(data),
        data=data,
        snapshot=snapshot.path,
    )


def load_all(
    specs: SourceSpecs,
    *,
    cache: Path,
    fetcher: Fetcher,
    sleep: Sleep,
    now: datetime,
    offline: bool,
    warn: Warn,
) -> Sources:
    """Load the four sources. Every source is attempted; when any is
    unavailable, one SourceUnavailable names them all."""
    missing: list[str] = []

    def load[T](spec: SourceSpec[T]) -> SourceResult[T] | None:
        try:
            return load_source(
                spec, cache=cache, fetcher=fetcher, sleep=sleep, now=now, offline=offline, warn=warn
            )
        except SourceUnavailable as exc:
            missing.extend(exc.names)
            return None

    md = load(specs.modelsdev)
    orr = load(specs.openrouter)
    ar = load(specs.arena)
    ep = load(specs.epoch)
    if md is None or orr is None or ar is None or ep is None:
        raise SourceUnavailable(missing)
    return Sources(modelsdev=md, openrouter=orr, arena=ar, epoch=ep)
```

- [ ] **Step 5: Run the test and see it pass**

Run: `uv run --directory modeltiers pytest tests/test_loader.py -q`
Expected: PASS, `12 passed`.

- [ ] **Step 6: Run the whole suite, since conftest changed**

Run: `uv run --directory modeltiers pytest -q`
Expected: PASS, `149 passed`.

- [ ] **Step 7: Lint, format check and type check**

Run: `make -C modeltiers check`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add modeltiers/src/modeltiers/loader.py modeltiers/tests/conftest.py modeltiers/tests/test_loader.py
git commit -m "feat(modeltiers): load each source through its snapshots, with fallback"
```

---

### Task 8: The `run` command

**Files:**
- Create: `modeltiers/src/modeltiers/cli.py`
- Test: `modeltiers/tests/test_cli.py`

**Interfaces:**
- Consumes: `modeltiers.inventory.load_inventory`, `provider_warnings`, `run_wt`, `RunWt` (Task 2); `modeltiers.loader.build_specs`, `load_all`, `SourceSpecs` (Task 7); `modeltiers.paths.cache_dir` (Task 1); `modeltiers.fetch.Fetcher`, `Sleep`, `http_fetcher` (Task 3); `modeltiers.errors.ModelTiersError` (Task 1); the `specs` and `good_answers` fixtures (Task 7); `fakes.FakeFetcher`, `fakes.Sleeps` (Task 3).
- Produces:
  - `modeltiers.cli`: `EXIT_OK = 0`, `EXIT_ERROR = 1`, `EXIT_STALE = 2`, `STAMP = "%Y-%m-%dT%H:%M:%SZ"`.
  - `Deps(fetcher: Fetcher = http_fetcher, sleep: Sleep = time.sleep, now: Callable[[], datetime] = _utc_now, run_wt: RunWt = inventory.run_wt, specs: SourceSpecs = field(default_factory=loader.build_specs))`, a frozen dataclass.
  - `main(argv: list[str] | None = None, *, deps: Deps | None = None) -> int`.
  - The `run` subcommand with `--dry-run`, `--offline` and `--inventory FILE`. Plain `run` does what `--dry-run` does in this step; `test_plain_run_does_what_dry_run_does` pins it and step 3 replaces it.
  - The report on stdout: one `inventory:` line, one line per source in the order `modelsdev`, `openrouter`, `arena`, `epoch`, then `nothing written: this version stops after parsing the sources`. Warnings on stderr as `warning: <text>`; an error as `modeltiers: <text>`.

- [ ] **Step 1: Write the failing test**

Write `modeltiers/tests/test_cli.py`:

```python
from collections.abc import Callable
from datetime import UTC, datetime, timedelta
from pathlib import Path

import pytest
from fakes import FakeFetcher, Sleeps

from modeltiers import cli, fetch, snapshots
from modeltiers.errors import FetchError, InventoryError
from modeltiers.loader import SourceSpecs

T0 = datetime(2026, 10, 12, 15, 58, 0, tzinfo=UTC)
T1 = T0 + timedelta(days=7)
INVENTORY = Path(__file__).parent / "fixtures" / "inventory.json"
Answers = dict[str, list[bytes | Exception]]

CLEAN_REPORT = """\
inventory: 6 models (1 native rows dropped), registry /home/owner/.config/local-ai/registry.toml
modelsdev: ok, 8 records, fetched 2026-10-12T15:58:00Z
openrouter: ok, 4 records, fetched 2026-10-12T15:58:00Z
arena: ok, 5 records, fetched 2026-10-12T15:58:00Z
epoch: ok, 5 records, fetched 2026-10-12T15:58:00Z
nothing written: this version stops after parsing the sources
"""


def _never_run_wt() -> str:
    raise AssertionError("wt must not be run when --inventory is given")


def _deps(
    specs: SourceSpecs,
    answers: Answers,
    *,
    now: datetime = T0,
    run_wt: Callable[[], str] = _never_run_wt,
) -> tuple[cli.Deps, FakeFetcher]:
    fetcher = FakeFetcher(answers)
    deps = cli.Deps(fetcher=fetcher, sleep=Sleeps(), now=lambda: now, run_wt=run_wt, specs=specs)
    return deps, fetcher


def test_dry_run_prints_each_source_and_the_inventory_count(
    specs: SourceSpecs, good_answers: Answers, capsys: pytest.CaptureFixture[str]
) -> None:
    argv = ["run", "--dry-run", "--inventory", str(INVENTORY)]
    code = cli.main(argv, deps=_deps(specs, good_answers)[0])
    captured = capsys.readouterr()
    assert code == 0
    assert captured.out == CLEAN_REPORT
    # The fixture's mtplx probe is "partial".
    assert captured.err == (
        "warning: provider mtplx probe status is partial; "
        "models it would have discovered may be absent from this run\n"
    )


def test_plain_run_does_what_dry_run_does(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    code = cli.main(["run", "--inventory", str(INVENTORY)], deps=_deps(specs, good_answers)[0])
    assert code == 0
    assert capsys.readouterr().out == CLEAN_REPORT
    assert not (tmp_path / "out").exists()  # MODELTIERS_OUT's directory: nothing written


def test_snapshots_go_under_modeltiers_cache(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path
) -> None:
    cli.main(
        ["run", "--dry-run", "--inventory", str(INVENTORY)], deps=_deps(specs, good_answers)[0]
    )
    assert sorted(p.name for p in (tmp_path / "cache").iterdir()) == [
        "arena",
        "epoch",
        "modelsdev",
        "openrouter",
    ]


def test_without_inventory_flag_wt_is_asked(
    specs: SourceSpecs, good_answers: Answers, capsys: pytest.CaptureFixture[str]
) -> None:
    deps, _ = _deps(specs, good_answers, run_wt=INVENTORY.read_text)
    assert cli.main(["run", "--dry-run"], deps=deps) == 0
    assert capsys.readouterr().out.startswith("inventory: 6 models")


def test_a_source_that_falls_back_is_stale_and_exits_2(
    specs: SourceSpecs,
    good_answers: Answers,
    epoch_zip: Callable[..., bytes],
    read_fixture: Callable[[str], bytes],
    capsys: pytest.CaptureFixture[str],
) -> None:
    argv = ["run", "--dry-run", "--inventory", str(INVENTORY)]
    assert cli.main(argv, deps=_deps(specs, good_answers)[0]) == 0
    capsys.readouterr()
    second: Answers = {
        fetch.MODELSDEV_URL: [read_fixture("modelsdev.json")],
        fetch.OPENROUTER_URL: [read_fixture("openrouter.json")],
        # Arena: the rows API says its index is loading, twice.
        fetch.arena_page_url(0): [read_fixture("arena_error.json")] * 2,
        fetch.EPOCH_URL: [epoch_zip()],
    }
    assert cli.main(argv, deps=_deps(specs, second, now=T1)[0]) == 2
    captured = capsys.readouterr()
    assert "arena: stale, 5 records, fetched 2026-10-12T15:58:00Z\n" in captured.out
    assert "openrouter: ok, 4 records, fetched 2026-10-19T15:58:00Z\n" in captured.out
    assert (
        "warning: arena: download failed: arena: the rows API answered with an error"
        in captured.err
    )


def test_a_source_with_no_good_snapshot_exits_1_and_names_it(
    specs: SourceSpecs, good_answers: Answers, capsys: pytest.CaptureFixture[str]
) -> None:
    good_answers[fetch.EPOCH_URL] = [FetchError("HTTP 404")]
    argv = ["run", "--dry-run", "--inventory", str(INVENTORY)]
    assert cli.main(argv, deps=_deps(specs, good_answers)[0]) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err.endswith("modeltiers: no usable data and no good snapshot for: epoch\n")


def test_offline_reads_snapshots_fetches_nothing_and_exits_0(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    argv = ["run", "--dry-run", "--inventory", str(INVENTORY)]
    cli.main(argv, deps=_deps(specs, good_answers)[0])
    capsys.readouterr()
    deps, fetcher = _deps(specs, {}, now=T1)
    assert cli.main([*argv, "--offline"], deps=deps) == 0
    out = capsys.readouterr().out
    assert out == CLEAN_REPORT.replace(": ok,", ": stale,")
    assert fetcher.calls == []
    # Nothing saved by the offline run: still one snapshot per source.
    assert len(snapshots.list_snapshots(tmp_path / "cache", "arena")) == 1


def test_offline_with_an_empty_cache_exits_1(
    specs: SourceSpecs, capsys: pytest.CaptureFixture[str]
) -> None:
    argv = ["run", "--dry-run", "--offline", "--inventory", str(INVENTORY)]
    assert cli.main(argv, deps=_deps(specs, {})[0]) == 1
    assert capsys.readouterr().err.endswith(
        "modeltiers: no usable data and no good snapshot for: modelsdev, openrouter, arena, epoch\n"
    )


def test_an_inventory_error_exits_1_before_any_fetch(
    specs: SourceSpecs, good_answers: Answers, capsys: pytest.CaptureFixture[str]
) -> None:
    def no_wt() -> str:
        raise InventoryError("wt is not on PATH; install it or pass --inventory FILE")

    deps, fetcher = _deps(specs, good_answers, run_wt=no_wt)
    assert cli.main(["run", "--dry-run"], deps=deps) == 1
    assert capsys.readouterr().err == (
        "modeltiers: wt is not on PATH; install it or pass --inventory FILE\n"
    )
    assert fetcher.calls == []


def test_a_missing_inventory_file_exits_1(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    argv = ["run", "--inventory", str(tmp_path / "absent.json")]
    assert cli.main(argv, deps=_deps(specs, good_answers)[0]) == 1
    assert "cannot read" in capsys.readouterr().err


def test_no_command_is_a_usage_error() -> None:
    with pytest.raises(SystemExit) as caught:
        cli.main([])
    assert caught.value.code == 2


def test_default_deps_are_the_real_seams() -> None:
    deps = cli.Deps()
    assert deps.fetcher is fetch.http_fetcher
    assert deps.specs.arena.name == "arena"
    assert deps.now().tzinfo is UTC
```

- [ ] **Step 2: Run the test and see it fail**

Run: `uv run --directory modeltiers pytest tests/test_cli.py -q`
Expected: FAIL at collection with `ImportError: cannot import name 'cli' from 'modeltiers'`.

- [ ] **Step 3: Write the command line**

The inventory is read before anything is fetched, so an inventory error costs no request.

Write `modeltiers/src/modeltiers/cli.py`:

```python
"""The `modeltiers` command line.

`main` builds the real dependencies; a test passes its own `Deps`, so no test
reaches the network, the clock or wt.
"""

import argparse
import sys
import time
from collections.abc import Callable
from dataclasses import dataclass, field
from datetime import UTC, datetime
from pathlib import Path

from modeltiers import inventory as inventory_mod
from modeltiers import loader, paths
from modeltiers.errors import ModelTiersError
from modeltiers.fetch import Fetcher, Sleep, http_fetcher
from modeltiers.inventory import RunWt
from modeltiers.loader import SourceSpecs

EXIT_OK = 0
EXIT_ERROR = 1
EXIT_STALE = 2
STAMP = "%Y-%m-%dT%H:%M:%SZ"


def _utc_now() -> datetime:
    return datetime.now(UTC)


@dataclass(frozen=True)
class Deps:
    """Everything `run` takes from outside the process."""

    fetcher: Fetcher = http_fetcher
    sleep: Sleep = time.sleep
    now: Callable[[], datetime] = _utc_now
    run_wt: RunWt = inventory_mod.run_wt
    specs: SourceSpecs = field(default_factory=loader.build_specs)


def _warn(message: str) -> None:
    print(f"warning: {message}", file=sys.stderr)


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="modeltiers",
        description="Advisory capability tiers for the models wt knows.",
    )
    commands = parser.add_subparsers(dest="command", required=True)
    run = commands.add_parser(
        "run", help="read the inventory and the four sources and report on them"
    )
    run.add_argument("--dry-run", action="store_true", help="write no output file")
    run.add_argument(
        "--offline",
        action="store_true",
        help="fetch nothing; read each source's newest good snapshot",
    )
    run.add_argument(
        "--inventory",
        metavar="FILE",
        type=Path,
        help="read saved `wt model list --json` output instead of running wt",
    )
    return parser


def _run(args: argparse.Namespace, deps: Deps) -> int:
    inventory = inventory_mod.load_inventory(path=args.inventory, run_wt=deps.run_wt)
    for line in inventory_mod.provider_warnings(inventory):
        _warn(line)
    sources = loader.load_all(
        deps.specs,
        cache=paths.cache_dir(),
        fetcher=deps.fetcher,
        sleep=deps.sleep,
        now=deps.now(),
        offline=args.offline,
        warn=_warn,
    )
    print(
        f"inventory: {len(inventory.rows)} models "
        f"({inventory.native_dropped} native rows dropped), registry {inventory.registry}"
    )
    for result in sources.results():
        print(
            f"{result.name}: {result.status}, {result.records} records, "
            f"fetched {result.fetched_at.strftime(STAMP)}"
        )
    print("nothing written: this version stops after parsing the sources")
    return EXIT_STALE if any(r.fallback for r in sources.results()) else EXIT_OK


def main(argv: list[str] | None = None, *, deps: Deps | None = None) -> int:
    args = _parser().parse_args(argv)
    try:
        return _run(args, deps or Deps())
    except ModelTiersError as exc:
        print(f"modeltiers: {exc}", file=sys.stderr)
        return EXIT_ERROR
```

- [ ] **Step 4: Run the test and see it pass**

Run: `uv run --directory modeltiers pytest tests/test_cli.py -q`
Expected: PASS, `12 passed`.

- [ ] **Step 5: Run the installed console script, offline, against an empty cache**

This reaches no network and no wt: `--offline` fetches nothing and `--inventory` replaces wt. The path after `--inventory` is relative to `modeltiers/`, which `--directory` makes the working directory.

Run: `MODELTIERS_CACHE="$(mktemp -d)" uv run --directory modeltiers modeltiers run --dry-run --offline --inventory tests/fixtures/inventory.json`
Expected: FAIL with exit code 1. stderr has the `mtplx` warning, then `modeltiers: no usable data and no good snapshot for: modelsdev, openrouter, arena, epoch`. stdout is empty.

- [ ] **Step 6: Lint, format check and type check**

Run: `make -C modeltiers check`
Expected: PASS, `Success: no issues found in 15 source files`.

- [ ] **Step 7: Commit**

```bash
git add modeltiers/src/modeltiers/cli.py modeltiers/tests/test_cli.py
git commit -m "feat(modeltiers): run prints each source's status and record count"
```

---

### Task 9: Repo wiring and docs

**Files:**
- Create: `.github/workflows/modeltiers-ci.yml`, `modeltiers/CLAUDE.md`, `modeltiers/tests/fixtures/README.md`
- Modify: `Makefile:39-44`, `bin/check-config-dirs-untouched:4`, `CLAUDE.md` (lines 6, 19, 23, 24, 30, 33, 34, 36)

**Interfaces:**
- Consumes: the package's `make install`, `make check` and `make test` targets (Task 1); `bin/check-config-dirs-untouched` (existing).
- Produces: `make test-all` and `make install` at the repo root cover `modeltiers/`; the `modeltiers-ci` workflow.

- [ ] **Step 1: Add the CI workflow**

The same job as `.github/workflows/llmbench-ci.yml`. `docs/contracts/tiers.sample.toml` does not exist until step 3; a path filter that matches nothing is harmless, and the spec lists it.

Write `.github/workflows/modeltiers-ci.yml`:

```yaml
name: modeltiers-ci

on:
  push:
    branches: [main]
    paths:
      - "modeltiers/**"
      - "docs/contracts/tiers.sample.toml"
      - "bin/check-config-dirs-untouched"
      - ".github/workflows/modeltiers-ci.yml"
  pull_request:
    paths:
      - "modeltiers/**"
      - "docs/contracts/tiers.sample.toml"
      - "bin/check-config-dirs-untouched"
      - ".github/workflows/modeltiers-ci.yml"

jobs:
  test:
    runs-on: ubuntu-26.04
    defaults:
      run:
        working-directory: modeltiers
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

- [ ] **Step 2: Wire the root Makefile**

In `Makefile`, replace:

```makefile
	cd llmbench && uv sync && make check && make test
```

with:

```makefile
	cd llmbench && uv sync && make check && make test
	cd modeltiers && uv sync && make check && make test
```

In `Makefile`, replace:

```makefile
install: ## Install all monorepo components (wt + llmbench).
	cd wt && make install
	cd llmbench && make install
```

with:

```makefile
install: ## Install all monorepo components (wt + llmbench + modeltiers).
	cd wt && make install
	cd llmbench && make install
	cd modeltiers && make install
```

- [ ] **Step 3: Name the new caller in the CI guard's comment**

In `bin/check-config-dirs-untouched`, replace:

```text
# CI runs this right after a test suite (llmbench-ci, wt-ci), before any
```

with:

```text
# CI runs this right after a test suite (llmbench-ci, modeltiers-ci, wt-ci), before any
```

- [ ] **Step 4: Write the package's CLAUDE.md**

Write `modeltiers/CLAUDE.md`:

```markdown
# CLAUDE.md

Guidance for Claude Code when working in `modeltiers/`.

## Project overview

`modeltiers` is a Python 3.13 CLI (argparse) that will write an advisory capability tier for every model wt lists, from four public sources. Design: `../docs/superpowers/specs/2026-10-12-model-tiers-design.md`. The spec's Delivery section lists four steps; this package is at step 1: it reads the inventory, downloads and parses the sources, and reports on them. It resolves no names, assigns no tiers and writes no output file yet.

It never writes `registry.toml`, `config.yaml` or any wt state, and it never reads `registry.toml`: the inventory comes only from `wt model list --json`.

## Commands

Run from the repo root as `uv run --directory modeltiers modeltiers <command>`, or from this directory as `uv run modeltiers <command>`. `--directory` makes `modeltiers/` the working directory, so a relative `--inventory` path resolves from here.

| Command | Effect |
|---|---|
| `modeltiers run [--dry-run] [--offline] [--inventory FILE]` | Read the inventory, load the four sources, print one line for the inventory and one per source (status, record count, download time). `--offline` fetches nothing and reads each source's newest good snapshot. `--inventory FILE` reads saved `wt model list --json` output and does not run wt. At this step `run` writes no output file with or without `--dry-run`, and says so on its last line. |

Exit codes: 0 every source `ok` (or `stale` because of `--offline`); 1 the inventory could not be read, or a source failed and has no good snapshot; 2 a source failed and its last good snapshot was used.

Development: `make install` (`uv sync`), `make check` (ruff lint, `ruff format --check`, mypy strict on `src/`), `make test` (pytest with coverage; fails under the floor in `pyproject.toml`), `make all`. One test: `uv run pytest tests/test_loader.py::test_a_clean_fetch_is_ok_and_saved`.

## Files and environment

| Thing | Default | Override |
|---|---|---|
| Output file (not written yet) | `$XDG_CONFIG_HOME/local-ai/tiers.toml`, else `~/.config/local-ai/tiers.toml` | `MODELTIERS_OUT` |
| Snapshots | `$XDG_CACHE_HOME/local-ai/modeltiers/`, else `~/.cache/local-ai/modeltiers/` | `MODELTIERS_CACHE` |

## Pipeline

One module per stage, plain frozen dataclasses (`records.py`) in and out.

| Module | Job |
|---|---|
| `inventory.py` | `wt model list --json` or `--inventory FILE` to an `Inventory`. Drops rows whose `model_name` is `native`; keeps discovered rows and rows with missing weights. The join key downstream is (`provider_id`, `model_name`), never the registry `id`. `provider_warnings` names each provider whose probe status is not `ok`. |
| `fetch.py` | One function per source behind `Fetcher` (`Callable[[str], bytes]`). `http_fetcher` has a 30-second timeout; `fetch_with_retry` tries once more after 2 seconds on a `FetchError` with `retryable` set (timeout, 5xx, Arena `error` body). `fetch_arena` pages the Hugging Face rows API to `num_rows_total` and returns one wrapper document, `{"pages": [...]}`, each page body unparsed. |
| `snapshots.py` | `<cache>/<source>/<UTC stamp>.<ext>`; `mark_bad` renames to `<stamp>.bad.<ext>`; `good_snapshots` newest first; `prune` keeps 4 good and 2 bad. `prune` is not called by any command yet. |
| `sources/*.py` | Pure parsers, bytes to records. An unrecognised shape or a count under `min_rows` raises `SourceParseError`. `min_rows` defaults to the spec's floor (models.dev 1,000, OpenRouter 100, Arena 20, Epoch 100). |
| `loader.py` | `load_source`: fetch, save, parse; on a failure fall back to the newest good snapshot (`status = "stale"`, `fallback = True`); with none, `SourceUnavailable`. `load_all` loads the four and names every unavailable source in one error. |
| `cli.py` | `main(argv, *, deps=None)`. `Deps` carries every seam. |

Parser details worth knowing:

- **OpenRouter** gives one record per model that has an `intelligence_index`. A model with no `benchmarks` or a null `artificial_analysis` is not a record, and the row minimum counts records.
- **Arena** removes every parenthesis whose content is an effort word and keeps the word in `ScoreRecord.effort`: `DeepSeek V4 Pro (High) (0813)` becomes `DeepSeek V4 Pro (0813)` with effort `high`. `Qwen3.8 Max` is untouched.
- **Epoch** reads `epoch_capabilities_index/eci_scores.csv` from the ZIP, keyed on the `Display name` column.
- **models.dev** keeps `canonical_model_id` in its own spelling (`minimax/MiniMax-M3`); a host row without one is kept with `canonical = None`.

## Testing patterns

- **Nothing live.** The autouse `_hermetic` fixture in `tests/conftest.py` makes `urllib.request.urlopen` and `subprocess.run` raise and points `HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`, `MODELTIERS_OUT` and `MODELTIERS_CACHE` under `tmp_path`. `tests/test_conftest_guards.py` fails if that stops holding. CI's `bin/check-config-dirs-untouched` covers the config home only; nothing but this fixture protects the real cache.
- **Seams are parameters.** The network is a `Fetcher`, the wait is a `Sleep`, the clock is a `now`, wt is a `RunWt`. Tests pass `fakes.FakeFetcher` and `fakes.Sleeps` (`tests/fakes.py`), or a whole `cli.Deps`.
- **Small fixtures.** `tests/fixtures/` holds a few rows per source, so tests pass small minimums: the `specs` fixture is `loader.build_specs(modelsdev_min=8, openrouter_min=4, arena_min=5, epoch_min=5)`. The Epoch fixtures are CSV text; the `epoch_zip` fixture zips one in memory. The Arena fixtures are single page bodies; `arena_doc` wraps them as `fetch_arena` would.
- `tests/fixtures/README.md` credits each source. Keep it current when a fixture is added.
```

- [ ] **Step 5: Credit the sources of the fixtures**

Write `modeltiers/tests/fixtures/README.md`:

```markdown
# Test fixtures

Every file here is a trimmed copy of a public download made on 2026-10-12, cut down to the rows the tests name, or a hand-made broken variant of one. None is a complete or current copy of its source, and no score here should be read as that source's present value.

| Files | Source | Licence and credit |
|---|---|---|
| `modelsdev.json`, `modelsdev_too_few.json`, `modelsdev_malformed.json` | models.dev, `https://models.dev/api.json` | MIT. models.dev, `https://github.com/anomalyco/models.dev` |
| `openrouter.json`, `openrouter_too_few.json`, `openrouter_malformed.json` | OpenRouter models API, `https://openrouter.ai/api/v1/models` | Source: Artificial Analysis (artificialanalysis.ai) via OpenRouter (openrouter.ai/rankings). |
| `arena_page1.json`, `arena_page2.json`, `arena_too_few.json`, `arena_malformed.json`, `arena_error.json` | Arena agent leaderboard, Hugging Face dataset `lmarena-ai/leaderboard-dataset`, config `agent`, split `latest`, read through the dataset viewer's rows API | CC BY 4.0. Arena (arena.ai), dataset lmarena-ai/leaderboard-dataset |
| `eci_scores.csv`, `eci_scores_too_few.csv`, `eci_scores_malformed.csv` | Epoch AI benchmark data, `https://epoch.ai/data/benchmark_data.zip`, member `epoch_capabilities_index/eci_scores.csv` | CC BY 4.0. Epoch AI, 'Capabilities & benchmarking', https://epoch.ai/benchmarks |
| `inventory.json` | `wt model list --json` on the owner's machine | This repository. The registry path is made up; `size_bytes` and `path` are nulled. |

Changes made to the copies:

- **Rows removed.** Each file keeps between one and eight rows.
- **Fields removed.** models.dev rows keep `id`, `name`, `tool_call`, `open_weights`, `limit` and `canonical_model_id`. OpenRouter rows keep `id`, `name`, `context_length` and `benchmarks.artificial_analysis`. Arena rows keep `model_name`, `organization`, `score`, `score_ci_lower`, `score_ci_upper`, `rank` and `category`, and the page's `features` list is dropped. The Epoch CSV keeps every column.
- **Page counts rewritten.** `num_rows_total` in the Arena pages is the fixture's own row count (5, or 1 in `arena_too_few.json`), and `row_idx` is renumbered.
- **Broken on purpose.** The `*_malformed.*` files are in a shape the parser must refuse. `arena_error.json` is the error body the rows API returned during design, unchanged.

The Epoch fixtures are CSV text so they can be read and diffed; the `epoch_zip` fixture in `tests/conftest.py` puts one in a ZIP archive in memory.
```

- [ ] **Step 6: Point the root CLAUDE.md at the package**

Eight replacements. Each "replace" text occurs exactly once in `CLAUDE.md`; where it is part of a longer line, the rest of the line stays as it is.

In `CLAUDE.md`, replace:

```markdown
`llmbench/CLAUDE.md` (Python benchmarks + provider isolation), `wt/CLAUDE.md` (Go worktree launcher) and
```

with:

```markdown
`llmbench/CLAUDE.md` (Python benchmarks + provider isolation), `modeltiers/CLAUDE.md` (Python capability tiers from public sources), `wt/CLAUDE.md` (Go worktree launcher) and
```

In `CLAUDE.md`, replace:

```markdown
- `wt cloud-sync [--only openrouter,ollama]
```

with:

```markdown
- `uv run --directory modeltiers modeltiers run [--dry-run] [--offline] [--inventory FILE]` — read the wt inventory (`wt model list --json`, or a saved copy with `--inventory`) and four public sources (models.dev, OpenRouter, Arena, Epoch AI), and print each source's status and record count. It writes no output file yet: tier assignment and `tiers.toml` are later steps of `docs/superpowers/specs/2026-10-12-model-tiers-design.md`. Exit 0 clean, 1 on an inventory error or a source with no usable data, 2 when a source fell back to its last good snapshot. Snapshots go under `~/.cache/local-ai/modeltiers/` (`MODELTIERS_CACHE` overrides)
- `wt cloud-sync [--only openrouter,ollama]
```

In `CLAUDE.md`, replace:

```markdown
mirroring CI: lint + llmbench `make check`/`make test` + wt `go build`/`vet`/`test`
```

with:

```markdown
mirroring CI: lint + llmbench `make check`/`make test` + modeltiers `make check`/`make test` + wt `go build`/`vet`/`test`
```

In `CLAUDE.md`, replace:

```markdown
(wt binary + shims via `wt/make install`, llmbench into its own venv)
```

with:

```markdown
(wt binary + shims via `wt/make install`, llmbench and modeltiers each into its own venv)
```

In `CLAUDE.md`, replace:

```markdown
the CI step llmbench-ci and wt-ci run after their tests
```

with:

```markdown
the CI step llmbench-ci, modeltiers-ci and wt-ci run after their tests
```

In `CLAUDE.md`, replace:

```markdown
- `wt/` — worktree agent launcher (Go module;
```

with:

```markdown
- `modeltiers/` — advisory capability tiers for the models wt lists (Python/uv; `src/modeltiers`, own `CLAUDE.md`, own `Makefile`; one runtime dependency, `tomli-w`). Reads the inventory only through `wt model list --json` and never writes `registry.toml`, `config.yaml` or any wt state. Keeps each source's downloads as snapshots under `~/.cache/local-ai/modeltiers/` and falls back to the newest good one when a download or a parse fails. Design: `docs/superpowers/specs/2026-10-12-model-tiers-design.md`
- `wt/` — worktree agent launcher (Go module;
```

In `CLAUDE.md`, replace:

```markdown
`test-all` (aggregates llmbench + wt)
```

with:

```markdown
`test-all` (aggregates llmbench + modeltiers + wt)
```

In `CLAUDE.md`, replace:

```markdown
shell-ci (root lint), wt-ci (Go + wt lint), llmbench-ci (Python)
```

with:

```markdown
shell-ci (root lint), wt-ci (Go + wt lint), llmbench-ci (Python), modeltiers-ci (Python)
```

- [ ] **Step 7: Check the shell scripts and the markdown links**

`bin/check-links` reads `git ls-files`, so stage the new markdown first.

```bash
git add modeltiers/CLAUDE.md modeltiers/tests/fixtures/README.md
```

Run: `make lint`
Expected: PASS. One `bash -n` line per shell script, no shellcheck output, then `ALL LINKS OK`.

- [ ] **Step 8: Commit**

```bash
git add .github/workflows/modeltiers-ci.yml Makefile bin/check-config-dirs-untouched CLAUDE.md modeltiers/CLAUDE.md modeltiers/tests/fixtures/README.md
git commit -m "feat(modeltiers): wire the package into make, ci and the docs"
```

---

### Task 10: Full verification

**Files:** none.

**Interfaces:**
- Consumes: everything above.
- Produces: nothing.

- [ ] **Step 1: Lint, format check and type check**

Run: `make -C modeltiers check`
Expected: PASS, with this output:

```
uv run ruff check src/ tests/
All checks passed!
uv run ruff format --check src/ tests/
29 files already formatted
uv run mypy src/
Success: no issues found in 15 source files
```

- [ ] **Step 2: The suite, with the coverage floor**

Run: `make -C modeltiers test`
Expected: PASS. The last lines are:

```
TOTAL                                    585      0   100%
Required test coverage of 90.0% reached. Total coverage: 100.00%
============================= 161 passed in 0.24s ==============================
```

The time varies; the counts do not.

- [ ] **Step 3: The repo's lint**

Run: `make lint`
Expected: PASS, ending `ALL LINKS OK`.

- [ ] **Step 4: The tests left the real directories alone**

Run: `git status --short`
Expected: PASS with no output: nothing untracked, nothing modified. `~/.cache/local-ai/modeltiers` was not created by any step of this plan.

- [ ] **Step 5 (optional, needs the network and wt): one live run, by hand**

It makes four kinds of request (models.dev, OpenRouter, one Arena page, the Epoch ZIP) and runs `wt model list --json`. The snapshots go to a scratch directory, not to `~/.cache`.

Run (live, by hand): `MODELTIERS_CACHE="$(mktemp -d)" uv run --directory modeltiers modeltiers run --dry-run`

Expected: exit 0 and six lines on stdout: `inventory: N models (3 native rows dropped), registry <path>`, then `modelsdev: ok, …`, `openrouter: ok, …`, `arena: ok, …`, `epoch: ok, …`, each with a record count at or above its minimum (1,000, 100, 20, 100), then `nothing written: this version stops after parsing the sources`. A provider whose probe is not `ok` adds a `warning:` line on stderr. Exit 2 with a `stale` line means a source failed and an earlier snapshot was used, which cannot happen with an empty scratch cache; exit 1 names the source that failed.

**Done when**

- `modeltiers run --dry-run` prints each source's status and parsed record count, and the inventory row count (spec Delivery item 1).
- A run with one source failing uses that source's last good snapshot, prints it as `stale` and exits 2; with no good snapshot it names the source and exits 1 (success criterion 6, as far as it applies before a file is written; `test_a_source_that_falls_back_is_stale_and_exits_2`, `test_a_source_with_no_good_snapshot_exits_1_and_names_it`).
- No test touches the network, the real config home or the real cache (success criterion 7; `tests/test_conftest_guards.py`, and `bin/check-config-dirs-untouched` in `modeltiers-ci`).
- The tool writes nothing but snapshot files under its cache directory: no `registry.toml`, no `config.yaml`, no wt state, no `tiers.toml` (success criterion 8).
- Each parser has a golden, a malformed and a too-few-rows fixture; the Arena parser reads a two-page fixture to `num_rows_total` and refuses an `error` body (spec Testing, Parsers).
- A snapshot that fails to parse is renamed `.bad` and skipped by a later `--offline`; pruning keeps four good and two bad per source (spec Testing, Snapshots).
- `make -C modeltiers check`, `make -C modeltiers test` and `make lint` pass; `make test-all` and `make install` cover `modeltiers/`; `modeltiers-ci` exists.

**For step 2 (identity)**

- Signatures to build on are in this plan's Interfaces blocks. Nothing in step 2 should need to change a step-1 signature; `cli.Deps` grows by new defaulted fields.
- Records are not deduplicated: `z-ai/glm-5.3` and `z-ai/glm-5.3:batch` are two OpenRouter records, and two effort rows of one Arena model are two records with one `source_id`.
- `ScoreRecord.source_id` for Arena already has the effort parenthesis removed, so an alias such as `DeepSeek V4 Pro (0813)` is compared with it directly.
- `ModelsDev` has no lookup methods; build the host-row and canonical-key indexes in `identity.py`.
- Adding a row to a golden fixture changes the counts pinned in `tests/test_source_*.py`, `tests/test_loader.py::test_load_all_reads_the_four_sources`, `tests/test_cli.py` (`CLEAN_REPORT`) and the `specs` fixture's minimums.
