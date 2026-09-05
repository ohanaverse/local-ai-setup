# Agent coding benchmark — Phase 4: Suite + runner + isolation loop (plan extract)

> Part of [the agent-coding-benchmark plan index](2026-09-04-agent-coding-benchmark.md). Shared Goal / Architecture / Tech Stack / Global Constraints and the phase order live there.


## Phase 4: Suite + runner + isolation loop

### Task 12: Suite parsing + cartesian row expansion

**Files:**
- Create: `modelman/src/modelman/benchmark/agent/suite.py`
- Test: `modelman/tests/benchmark/agent/test_suite.py`

**Interfaces:**
- Consumes: `RowConfig`, `DirectRouteConfig` from `pidriver.py` (Task 5).
- Produces: `JudgeConfig` (`model, thinking, temperature, samples, max_attempts, route`), `Suite` (`name, task_path, passes, cooldown_s, agent_timeout_s, judge, routes_direct, rows, repair_rounds`), `load_suite(path: Path, registry: Registry) -> Suite`. `runner.py` (Task 14) and `cli.py` (Task 15) both consume `Suite`/`load_suite` exactly as named here.

- [ ] **Step 1: Write the failing test**

`modelman/tests/benchmark/agent/test_suite.py`:
```python
"""Tests for modelman.benchmark.agent.suite — TOML parsing and cartesian
row expansion.

Hand-writing every model x thinking x route combination is how suites stop
getting maintained (spec); these tests pin the expansion arithmetic and the
two error cases most likely to silently misconfigure a run: an unknown
model reference and a missing [routes.direct.<provider>] block.
"""

from pathlib import Path

import pytest

from modelman.benchmark.agent.pidriver import DirectRouteConfig, RowConfig
from modelman.benchmark.agent.suite import load_suite
from modelman.benchmark.errors import BenchmarkError
from modelman.registry import ModelEntry, ProviderEntry, Registry

FIXTURE_ROOT = Path(__file__).parent / "fixtures"


def _registry() -> Registry:
    return Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local"), ProviderEntry(id="omlx", name="oMLX", location="local")],
        models=[
            ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a"),
            ModelEntry(id="omlx/b", family="f", provider_id="omlx", model_name="b-real-name"),
        ],
    )


def _write_suite(tmp_path: Path, body: str) -> Path:
    path = tmp_path / "suite.toml"
    path.write_text(body, encoding="utf-8")
    return path


def test_cartesian_expansion_produces_every_combination(tmp_path):
    body = """
name = "test suite"
task = "some/task"
passes = 1
cooldown_s = 5
agent_timeout_s = 60

[judge]
model = "openrouter/anthropic/claude-opus-4"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[[rows]]
models = ["ollama/a", "omlx/b"]
thinking = ["off", "high"]
routes = ["direct", "litellm"]
"""
    suite = load_suite(_write_suite(tmp_path, body), _registry())
    assert len(suite.rows) == 8
    assert {(r.model_id, r.thinking, r.route) for r in suite.rows} == {
        (m, t, rt) for m in ("ollama/a", "omlx/b") for t in ("off", "high") for rt in ("direct", "litellm")
    }
    assert all(r.provider_id in ("ollama", "omlx") for r in suite.rows)
    assert len({r.label for r in suite.rows}) == 8  # every label unique


def test_pinned_row_with_explicit_label(tmp_path):
    body = """
name = "test suite"
task = "some/task"
passes = 1
cooldown_s = 5
agent_timeout_s = 60

[judge]
model = "openrouter/anthropic/claude-opus-4"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[[rows]]
label = "baseline"
model = "ollama/a"
thinking = "off"
route = "direct"
"""
    suite = load_suite(_write_suite(tmp_path, body), _registry())
    assert len(suite.rows) == 1
    assert suite.rows[0].label == "baseline"
    assert suite.rows[0].provider_id == "ollama"


def test_unknown_model_reference_raises(tmp_path):
    body = """
name = "test suite"
task = "some/task"

[judge]
model = "x"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[[rows]]
models = ["ollama/does-not-exist"]
thinking = ["off"]
routes = ["direct"]
"""
    with pytest.raises(BenchmarkError, match="does-not-exist"):
        load_suite(_write_suite(tmp_path, body), _registry())


def test_provider_override_distinguishes_shared_backend_variants(tmp_path):
    """A row can override provider= so e.g. omlx 4-bit and 6-bit — served by
    one provider that must be isolated with the exact variant name — stay
    distinguishable, per the spec's route-resolution section."""
    body = """
name = "test suite"
task = "some/task"

[judge]
model = "x"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[[rows]]
label = "sixbit"
model = "omlx/b"
thinking = "off"
route = "direct"
provider = "omlx-6bit"
"""
    suite = load_suite(_write_suite(tmp_path, body), _registry())
    assert suite.rows[0].provider_id == "omlx-6bit"


def test_judge_route_direct_is_rejected(tmp_path):
    """[judge] supports route=litellm only in v1 — direct has no place
    judging cloud-sequenced rows (spec review resolution)."""
    body = """
name = "test suite"
task = "some/task"

[judge]
model = "x"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "direct"

[[rows]]
models = ["ollama/a"]
thinking = ["off"]
routes = ["direct"]
"""
    with pytest.raises(BenchmarkError, match="route"):
        load_suite(_write_suite(tmp_path, body), _registry())


def test_routes_direct_blocks_parsed(tmp_path):
    body = """
name = "test suite"
task = "some/task"

[judge]
model = "x"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[routes.direct.omlx]
base_url = "http://localhost:8000/v1"
api = "openai-completions"

[[rows]]
models = ["omlx/b"]
thinking = ["off"]
routes = ["direct"]
"""
    suite = load_suite(_write_suite(tmp_path, body), _registry())
    assert suite.routes_direct["omlx"] == DirectRouteConfig(base_url="http://localhost:8000/v1", api="openai-completions")
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_suite.py -v`
Expected: FAIL with `ModuleNotFoundError`

- [ ] **Step 3: Write the implementation**

`modelman/src/modelman/benchmark/agent/suite.py`:
```python
"""Suite TOML parsing and cartesian row expansion."""

from __future__ import annotations

import itertools
import tomllib
from dataclasses import dataclass, field
from pathlib import Path

from modelman.benchmark.agent.pidriver import DirectRouteConfig, RowConfig
from modelman.benchmark.errors import BenchmarkError
from modelman.registry import Registry


@dataclass
class JudgeConfig:
    model: str
    thinking: str
    temperature: float
    samples: int
    max_attempts: int
    route: str


@dataclass
class Suite:
    name: str
    task_path: Path
    passes: int
    cooldown_s: float
    agent_timeout_s: float
    judge: JudgeConfig
    routes_direct: dict[str, DirectRouteConfig] = field(default_factory=dict)
    rows: list[RowConfig] = field(default_factory=list)
    repair_rounds: int = 0


def _short_model(model_id: str) -> str:
    return model_id.split("/")[-1]


def _provider_for(model_id: str, registry: Registry) -> str:
    try:
        return registry.model(model_id).provider_id
    except KeyError as exc:
        raise BenchmarkError(f"suite row references unknown model: {model_id}") from exc


def _expand_rows(raw_rows: list[dict], registry: Registry) -> list[RowConfig]:
    rows: list[RowConfig] = []
    index = 0
    for raw in raw_rows:
        if "model" in raw:
            index += 1
            model_id = raw["model"]
            _provider_for(model_id, registry)  # validates the model exists
            provider_id = raw.get("provider") or _provider_for(model_id, registry)
            label = raw.get("label") or f"{index:02d}--{_short_model(model_id)}--{raw['thinking']}--{raw['route']}"
            rows.append(
                RowConfig(label=label, model_id=model_id, thinking=raw["thinking"], route=raw["route"], provider_id=provider_id)
            )
            continue

        models = raw.get("models", [])
        thinkings = raw.get("thinking", [])
        routes = raw.get("routes", [])
        for model_id, thinking, route in itertools.product(models, thinkings, routes):
            index += 1
            provider_id = raw.get("provider") or _provider_for(model_id, registry)
            label = f"{index:02d}--{_short_model(model_id)}--{thinking}--{route}"
            rows.append(RowConfig(label=label, model_id=model_id, thinking=thinking, route=route, provider_id=provider_id))
    return rows


def load_suite(path: Path, registry: Registry) -> Suite:
    path = Path(path)
    with path.open("rb") as f:
        raw = tomllib.load(f)

    judge_raw = raw["judge"]
    if judge_raw["route"] != "litellm":
        raise BenchmarkError(
            f"[judge] route must be 'litellm' in v1, got {judge_raw['route']!r} — "
            "the judge is a robust cloud model reached through the existing proxy"
        )
    judge = JudgeConfig(
        model=judge_raw["model"],
        thinking=judge_raw["thinking"],
        temperature=judge_raw["temperature"],
        samples=judge_raw.get("samples", 1),
        max_attempts=judge_raw.get("max_attempts", 2),
        route=judge_raw["route"],
    )

    routes_direct = {
        provider_id: DirectRouteConfig(base_url=cfg["base_url"], api=cfg["api"])
        for provider_id, cfg in raw.get("routes", {}).get("direct", {}).items()
    }

    repair_rounds = raw.get("repair_rounds", 0)
    if repair_rounds != 0:
        raise BenchmarkError("repair_rounds is not yet supported (v1 ships repair_rounds = 0 only)")

    return Suite(
        name=raw["name"],
        task_path=Path(raw["task"]),
        passes=raw.get("passes", 1),
        cooldown_s=raw.get("cooldown_s", 20),
        agent_timeout_s=raw.get("agent_timeout_s", 420),
        judge=judge,
        routes_direct=routes_direct,
        rows=_expand_rows(raw.get("rows", []), registry),
        repair_rounds=repair_rounds,
    )
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_suite.py -v`
Expected: PASS (6 tests)

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/suite.py \
        modelman/tests/benchmark/agent/test_suite.py
git commit -m "feat(agent-bench): add suite parsing + cartesian row expansion - completes plan item #12"
```

### Task 13: Preflight validation

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/suite.py`
- Modify: `modelman/tests/benchmark/agent/test_suite.py`

**Interfaces:**
- Produces: `preflight(suite: Suite, registry: Registry, task: TaskBundle, *, plist_path: Path = LITELLM_PLIST) -> None`. `runner.py` (Task 14) calls this as phase 0, before touching any provider.

Per the spec: preflight must fail fast on isolation helpers missing from `PATH`, a `route=direct` row with no matching `[routes.direct.<provider>]` block, `hidden/` leaking into `visible/`, and a missing judge API key — "rather than dying mid-run after paying for the agent rows."

- [ ] **Step 1: Write the failing tests**

Append to `modelman/tests/benchmark/agent/test_suite.py`:
```python
from modelman.benchmark.agent.task import load_task
from modelman.benchmark.agent.suite import preflight

MINI_DRIFT = Path(__file__).parent / "fixtures" / "tasks" / "mini-drift"


def _passing_suite_toml() -> str:
    return """
name = "test suite"
task = "some/task"

[judge]
model = "openrouter/anthropic/claude-opus-4"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[routes.direct.ollama]
base_url = "http://localhost:11434/v1"
api = "openai-completions"

[[rows]]
models = ["ollama/a"]
thinking = ["off"]
routes = ["direct"]
"""


def test_preflight_passes_when_everything_is_configured(tmp_path, monkeypatch):
    monkeypatch.setenv("OPENROUTER_API_KEY", "sk-test")
    monkeypatch.setattr("shutil.which", lambda name: f"/usr/local/bin/{name}")
    suite = load_suite(_write_suite(tmp_path, _passing_suite_toml()), _registry())
    task = load_task(MINI_DRIFT)
    preflight(suite, _registry(), task, plist_path=tmp_path / "missing.plist")


def test_preflight_missing_isolation_helper_raises(tmp_path, monkeypatch):
    monkeypatch.setenv("OPENROUTER_API_KEY", "sk-test")
    monkeypatch.setattr("shutil.which", lambda name: None)
    suite = load_suite(_write_suite(tmp_path, _passing_suite_toml()), _registry())
    task = load_task(MINI_DRIFT)
    with pytest.raises(BenchmarkError, match="llm-isolate-provider"):
        preflight(suite, _registry(), task, plist_path=tmp_path / "missing.plist")


def test_preflight_missing_direct_route_block_raises(tmp_path, monkeypatch):
    monkeypatch.setenv("OPENROUTER_API_KEY", "sk-test")
    monkeypatch.setattr("shutil.which", lambda name: f"/usr/local/bin/{name}")
    body = _passing_suite_toml().replace("[routes.direct.ollama]", "[routes.direct.something-else]")
    suite = load_suite(_write_suite(tmp_path, body), _registry())
    task = load_task(MINI_DRIFT)
    with pytest.raises(BenchmarkError, match="ollama"):
        preflight(suite, _registry(), task, plist_path=tmp_path / "missing.plist")


def test_preflight_hidden_leaked_into_visible_raises(tmp_path, monkeypatch):
    monkeypatch.setenv("OPENROUTER_API_KEY", "sk-test")
    monkeypatch.setattr("shutil.which", lambda name: f"/usr/local/bin/{name}")
    leaky_task_dir = tmp_path / "leaky-task"
    import shutil as _shutil

    _shutil.copytree(MINI_DRIFT, leaky_task_dir)
    _shutil.copy(leaky_task_dir / "hidden" / "test_hidden.py", leaky_task_dir / "visible" / "tests" / "test_hidden.py")
    suite = load_suite(_write_suite(tmp_path, _passing_suite_toml()), _registry())
    task = load_task(leaky_task_dir)
    with pytest.raises(BenchmarkError, match="test_hidden.py"):
        preflight(suite, _registry(), task, plist_path=tmp_path / "missing.plist")


def test_preflight_missing_openrouter_key_raises(tmp_path, monkeypatch):
    monkeypatch.delenv("OPENROUTER_API_KEY", raising=False)
    monkeypatch.setattr("shutil.which", lambda name: f"/usr/local/bin/{name}")
    suite = load_suite(_write_suite(tmp_path, _passing_suite_toml()), _registry())
    task = load_task(MINI_DRIFT)
    with pytest.raises(BenchmarkError, match="OPENROUTER_API_KEY"):
        preflight(suite, _registry(), task, plist_path=tmp_path / "missing.plist")
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_suite.py -v`
Expected: FAIL — `preflight` doesn't exist yet

- [ ] **Step 3: Write the implementation**

Add to the top of `modelman/src/modelman/benchmark/agent/suite.py` (new imports: `os`, `plistlib`, `shutil`; new import `from modelman.benchmark.agent.task import TaskBundle`):
```python
LITELLM_PLIST = Path.home() / "Library" / "LaunchAgents" / "local.litellm.proxy.plist"
```

Append to `modelman/src/modelman/benchmark/agent/suite.py`:
```python
def _openrouter_key_available(plist_path: Path) -> bool:
    if os.environ.get("OPENROUTER_API_KEY"):
        return True
    if not plist_path.exists():
        return False
    try:
        with plist_path.open("rb") as f:
            data = plistlib.load(f)
    except Exception:
        return False
    return bool(data.get("EnvironmentVariables", {}).get("OPENROUTER_API_KEY"))


def preflight(suite: Suite, registry: Registry, task: TaskBundle, *, plist_path: Path = LITELLM_PLIST) -> None:
    """Fail fast on everything that would otherwise die mid-run, after
    already paying for the agent rows."""
    missing_helpers = [
        name for name in ("llm-isolate-provider", "llm-restore-providers") if shutil.which(name) is None
    ]
    if missing_helpers:
        raise BenchmarkError(
            f"isolation helper(s) not found on PATH: {', '.join(missing_helpers)}. "
            "Ensure local-ai-setup/bin is on PATH."
        )

    for row in suite.rows:
        if row.route == "direct" and row.provider_id not in suite.routes_direct:
            raise BenchmarkError(
                f"row {row.label!r} uses route=direct for provider {row.provider_id!r} "
                f"but no [routes.direct.{row.provider_id}] block is configured"
            )

    if task.hidden_dir.is_dir():
        hidden_names = {p.name for p in task.hidden_dir.rglob("*") if p.is_file()}
        visible_names = {p.name for p in task.visible_dir.rglob("*") if p.is_file()}
        leaked = hidden_names & visible_names
        if leaked:
            raise BenchmarkError(f"hidden/ file name(s) also present under visible/: {', '.join(sorted(leaked))}")

    if suite.judge.model.split("/")[0] == "openrouter" and not _openrouter_key_available(plist_path):
        raise BenchmarkError(
            f"OPENROUTER_API_KEY not found for judge model {suite.judge.model!r}; "
            "check ~/Library/LaunchAgents/local.litellm.proxy.plist"
        )
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_suite.py -v`
Expected: PASS (11 tests)

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/suite.py \
        modelman/tests/benchmark/agent/test_suite.py
git commit -m "feat(agent-bench): add suite preflight validation - completes plan item #13"
```

### Task 14: Runner — phase 0 (preflight) + phase 1 (execute) + isolation loop

**Files:**
- Create: `modelman/src/modelman/benchmark/agent/runner.py`
- Test: `modelman/tests/benchmark/agent/test_runner.py`

**Interfaces:**
- Consumes: `Suite`/`preflight` (Task 13), `RowConfig` (Task 5), `TaskBundle`/`load_task` (Task 1), `Workspace`/`create_workspace`/`destroy_workspace` (Task 2), `GatesReport`/`evaluate` (Task 10), everything in `pidriver.py` (Tasks 5–7), `modelman.benchmark.isolation.isolate_provider`/`restore_providers` (existing module).
- Produces: `RowRunResult` (`row, pass_number, row_dir, gates, metrics, diff_raw, error`), `run_suite(suite, registry, *, row_filter=None, results_dir=None, live_models_path=pidriver.LIVE_PI_MODELS_PATH) -> tuple[Path, list[RowRunResult]]`. `cli.py` (Task 15) calls `run_suite`; `report.py` (Task 22) and `judge.py`-wiring (Task 21) both consume `RowRunResult` by these exact field names.

**Important:** call every `pidriver` function as `pidriver.some_function(...)` (module-qualified — `from modelman.benchmark.agent import pidriver`), not `from ... import run_pi_process`. A direct-name import binds a private copy in `runner.py`'s namespace that a test's `monkeypatch.setattr(pidriver_module, "run_pi_process", fake)` cannot reach; the existing `isolation` import already follows the module-qualified pattern for the same reason.

- [ ] **Step 1: Write the failing test**

`modelman/tests/benchmark/agent/test_runner.py`:
```python
"""Tests for modelman.benchmark.agent.runner — phase 0/1 orchestration.

The isolation loop's grouping-by-provider and its failure-containment (one
provider's isolation failure must not abort the rest of the suite,
matching modelman.benchmark.runner's existing per-target error pattern)
are the two behaviors most likely to silently waste a multi-hour local
sweep if they regress. run_pi_process itself is mocked here — Tasks 5-7
already cover its own correctness against a real subprocess.
"""

from pathlib import Path

import pytest

import modelman.benchmark.isolation as isolation_module
from modelman.benchmark.agent import pidriver as pidriver_module
from modelman.benchmark.agent.pidriver import PiRunResult
from modelman.benchmark.agent.runner import run_suite
from modelman.benchmark.agent.suite import load_suite
from modelman.benchmark.errors import BenchmarkError
from modelman.registry import ModelEntry, ProviderEntry, Registry

MINI_DRIFT = Path(__file__).parent / "fixtures" / "tasks" / "mini-drift"


def _registry() -> Registry:
    return Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )


def _suite_toml(task_path: Path, models: str = '["ollama/a"]') -> str:
    return f"""
name = "test suite"
task = "{task_path}"
passes = 1
cooldown_s = 0
agent_timeout_s = 5

[judge]
model = "some-cloud/model"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[routes.direct.ollama]
base_url = "http://localhost:11434/v1"
api = "openai-completions"

[[rows]]
models = {models}
thinking = ["off"]
routes = ["direct"]
"""


def _write_suite(tmp_path: Path, body: str, name: str = "suite.toml") -> Path:
    path = tmp_path / name
    path.write_text(body, encoding="utf-8")
    return path


def _no_diff_run(*args, **kwargs) -> PiRunResult:
    return PiRunResult(completed=True, timed_out=False, wall_ms=50, events=[], unparsed_lines=0, message_end_seen=True)


@pytest.fixture(autouse=True)
def _hermetic_preflight(monkeypatch):
    monkeypatch.setattr("shutil.which", lambda name: f"/usr/local/bin/{name}")


def test_run_suite_isolates_once_per_provider_group(tmp_path, monkeypatch):
    """Two rows on the same provider share one isolate_provider() call."""
    calls = []
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: calls.append(pid))
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: calls.append("restore"))
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    body = _suite_toml(MINI_DRIFT, models='["ollama/a", "ollama/a"]')
    suite = load_suite(_write_suite(tmp_path, body), _registry())
    run_dir, results = run_suite(
        suite, _registry(), results_dir=tmp_path / "results", live_models_path=tmp_path / "missing.json"
    )

    assert calls == ["ollama", "restore"]
    assert len(results) == 2
    assert all(r.error is None for r in results)
    assert all(r.gates is not None and r.gates.results[2].code == "NO_DIFF" for r in results)


def test_run_suite_contains_isolation_failure_to_its_group(tmp_path, monkeypatch):
    """An isolation failure marks that provider's rows with the error and
    the suite continues — matching the existing single-turn runner's
    per-target error containment."""

    def _fail_isolate(pid):
        raise BenchmarkError(f"isolation failed for {pid}")

    monkeypatch.setattr(isolation_module, "isolate_provider", _fail_isolate)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    run_dir, results = run_suite(
        suite, _registry(), results_dir=tmp_path / "results", live_models_path=tmp_path / "missing.json"
    )

    assert len(results) == 1
    assert results[0].error == "isolation failed for ollama"
    assert results[0].gates is None


def test_run_suite_row_filter_selects_by_label(tmp_path, monkeypatch):
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: None)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    body = _suite_toml(MINI_DRIFT, models='["ollama/a", "ollama/a"]')
    suite = load_suite(_write_suite(tmp_path, body), _registry())
    wanted_label = suite.rows[0].label
    run_dir, results = run_suite(
        suite,
        _registry(),
        row_filter=[wanted_label],
        results_dir=tmp_path / "results",
        live_models_path=tmp_path / "missing.json",
    )
    assert len(results) == 1
    assert results[0].row.label == wanted_label
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_runner.py -v`
Expected: FAIL with `ModuleNotFoundError`

- [ ] **Step 3: Write the implementation**

`modelman/src/modelman/benchmark/agent/runner.py`:
```python
"""Suite orchestration: phase 0 (preflight) and phase 1 (execute)."""

from __future__ import annotations

import itertools
import os
import shutil
import tempfile
import time
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path

from modelman.benchmark import isolation
from modelman.benchmark.agent import pidriver
from modelman.benchmark.agent.gates import GatesReport
from modelman.benchmark.agent.gates import evaluate as evaluate_gates
from modelman.benchmark.agent.suite import RowConfig, Suite, preflight
from modelman.benchmark.agent.task import TaskBundle, load_task
from modelman.benchmark.agent.workspace import create_workspace, destroy_workspace
from modelman.benchmark.errors import BenchmarkError
from modelman.registry import Registry

DEFAULT_RESULTS_DIR = Path.home() / ".config" / "local-ai" / "benchmarks"


@dataclass
class RowRunResult:
    row: RowConfig
    pass_number: int
    row_dir: Path
    gates: GatesReport | None
    metrics: pidriver.RowMetrics | None
    diff_raw: str
    error: str | None = None


def _row_dir(run_dir: Path, index: int, row: RowConfig, pass_number: int) -> Path:
    return run_dir / f"{index:02d}--{row.label}--p{pass_number}"


def _prompt_for(task: TaskBundle) -> str:
    return (
        f"{task.task_md}\n\n"
        "Work only inside this repository. When you believe the bug is fixed, "
        "add a regression test under tests/ and stop."
    )


def _run_single_row(
    row: RowConfig,
    pass_number: int,
    task: TaskBundle,
    suite: Suite,
    registry: Registry,
    row_dir: Path,
    live_models_path: Path,
) -> RowRunResult:
    row_dir.mkdir(parents=True, exist_ok=True)
    model = registry.model(row.model_id)
    target = pidriver.resolve_pi_target(row, model.model_name, suite.routes_direct, live_models_path=live_models_path)

    workspace = create_workspace(task)
    config_dir = Path(tempfile.mkdtemp(prefix="agent-bench-config-"))
    try:
        pidriver.write_pi_config(target, config_dir)
        cmd = pidriver.build_pi_command(target, row.thinking, row_dir, _prompt_for(task))
        env = {**os.environ, "PI_CODING_AGENT_DIR": str(config_dir)}
        run_result = pidriver.run_pi_process(cmd, cwd=workspace.root, env=env, timeout_s=suite.agent_timeout_s)
        metrics = pidriver.compute_metrics(run_result, thinking_level=row.thinking)
        session_present = any(row_dir.iterdir())
        gates = evaluate_gates(workspace, task, run_result, session_file_present=session_present)
        diff_raw = workspace.diff()
        return RowRunResult(
            row=row, pass_number=pass_number, row_dir=row_dir, gates=gates, metrics=metrics, diff_raw=diff_raw
        )
    finally:
        shutil.rmtree(config_dir, ignore_errors=True)
        destroy_workspace(workspace)


def _select_rows(rows: list[RowConfig], row_filter: list[str] | None) -> list[RowConfig]:
    if not row_filter:
        return list(rows)
    wanted = set(row_filter)
    return [r for i, r in enumerate(rows, start=1) if r.label in wanted or str(i) in wanted]


def run_suite(
    suite: Suite,
    registry: Registry,
    *,
    row_filter: list[str] | None = None,
    results_dir: Path | None = None,
    live_models_path: Path = pidriver.LIVE_PI_MODELS_PATH,
) -> tuple[Path, list[RowRunResult]]:
    """Phase 0 (preflight) + phase 1 (execute): group rows by provider,
    isolate once per group, run each row's agent + gates, restore at the
    end. Judging (phase 2) and the rendered report (phase 3) are added by
    Tasks 19-23."""
    task = load_task(suite.task_path)
    preflight(suite, registry, task)

    rows = _select_rows(suite.rows, row_filter)
    results_dir = results_dir or DEFAULT_RESULTS_DIR
    run_id = datetime.now(UTC).strftime("%Y%m%d-%H%M%S")
    run_dir = results_dir / run_id
    run_dir.mkdir(parents=True, exist_ok=True)

    results: list[RowRunResult] = []
    index = 0
    ordered = sorted(rows, key=lambda r: r.provider_id)
    for provider_id, group in itertools.groupby(ordered, key=lambda r: r.provider_id):
        group_rows = list(group)
        try:
            isolation.isolate_provider(provider_id)
        except BenchmarkError as exc:
            for row in group_rows:
                index += 1
                for pass_number in range(1, suite.passes + 1):
                    results.append(
                        RowRunResult(
                            row=row,
                            pass_number=pass_number,
                            row_dir=_row_dir(run_dir, index, row, pass_number),
                            gates=None,
                            metrics=None,
                            diff_raw="",
                            error=str(exc),
                        )
                    )
            continue

        for row in group_rows:
            index += 1
            for pass_number in range(1, suite.passes + 1):
                row_dir = _row_dir(run_dir, index, row, pass_number)
                results.append(_run_single_row(row, pass_number, task, suite, registry, row_dir, live_models_path))
                if pass_number < suite.passes:
                    time.sleep(suite.cooldown_s)

    isolation.restore_providers()
    return run_dir, results
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_runner.py -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/runner.py \
        modelman/tests/benchmark/agent/test_runner.py
git commit -m "feat(agent-bench): add runner phase 0/1 orchestration - completes plan item #14"
```

### Task 15: CLI (`run`/`list-tasks`/`list-suites`/`--dry-run`) + smoke suite

**Files:**
- Create: `modelman/src/modelman/benchmark/agent/cli.py`
- Modify: `modelman/src/modelman/benchmark/cli.py`
- Create: `benchmarks/suites/smoke.toml`
- Test: `modelman/tests/benchmark/agent/test_cli.py`

**Interfaces:**
- Consumes: `run_suite` (Task 14), `load_suite` (Task 12/13), `list_task_bundles` (Task 1).
- Produces: `agent_app` (Typer sub-app), mounted as `modelman benchmark agent`. This closes Phase 4 — the spec's "first end-to-end multi-row local run happens here."

- [ ] **Step 1: Write the smoke suite**

`benchmarks/suites/smoke.toml`:
```toml
name = "agent bench smoke test"
task = "benchmarks/tasks/day31-drift"
passes = 1
cooldown_s = 0
agent_timeout_s = 180

[judge]
model = "ollama/glm-5.3-flash:cloud"
thinking = "off"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[[rows]]
label = "smoke"
model = "ollama/glm-5.3-flash:cloud"
thinking = "off"
route = "litellm"
```

One pinned row, a fast `:cloud` model reached through LiteLLM — no local model is loaded, so no isolation is needed, matching the spec's live-verification note.

- [ ] **Step 2: Write the failing test**

`modelman/tests/benchmark/agent/test_cli.py`:
```python
"""Tests for modelman.benchmark.agent.cli — the modelman benchmark agent
subcommand surface.

--dry-run resolving the matrix without executing anything is the harness's
own safeguard against loading a 27B model six times because of a typo'd
suite (spec) — the test for it asserts run_suite is never called, not just
that the command exits 0.
"""

from pathlib import Path

from typer.testing import CliRunner

from modelman.benchmark.agent.cli import agent_app
import modelman.benchmark.agent.cli as cli_module
from modelman.registry import ModelEntry, ProviderEntry, Registry
from modelman.benchmark.agent.runner import RowRunResult
from modelman.benchmark.agent.pidriver import RowConfig

FIXTURE_TASKS = Path(__file__).parent / "fixtures" / "tasks"

runner = CliRunner()


def _registry() -> Registry:
    return Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )


def test_list_tasks_lists_bundles_under_root():
    result = runner.invoke(agent_app, ["list-tasks", "--root", str(FIXTURE_TASKS)])
    assert result.exit_code == 0
    assert "mini-drift" in result.output


def test_dry_run_never_calls_run_suite(tmp_path, monkeypatch):
    def _boom(*args, **kwargs):
        raise AssertionError("run_suite must not be called during --dry-run")

    monkeypatch.setattr(cli_module, "load_registry", lambda: _registry())
    monkeypatch.setattr(cli_module, "run_suite", _boom)

    suite_path = tmp_path / "suite.toml"
    suite_path.write_text(
        """
name = "s"
task = "some/task"

[judge]
model = "x"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[[rows]]
models = ["ollama/a"]
thinking = ["off"]
routes = ["direct"]
""",
        encoding="utf-8",
    )
    result = runner.invoke(agent_app, ["run", "--suite", str(suite_path), "--dry-run"])
    assert result.exit_code == 0
    assert "01" in result.output
    assert "dry run" in result.output


def test_run_records_agent_last_run_pointer(tmp_path, monkeypatch):
    row = RowConfig(label="r1", model_id="ollama/a", thinking="off", route="direct", provider_id="ollama")
    fake_run_dir = tmp_path / "results" / "20260101-000000"

    monkeypatch.setattr(cli_module, "load_registry", lambda: _registry())
    monkeypatch.setattr(
        cli_module,
        "run_suite",
        lambda *a, **k: (fake_run_dir, [RowRunResult(row=row, pass_number=1, row_dir=fake_run_dir / "01", gates=None, metrics=None, diff_raw="", error=None)]),
    )
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))

    suite_path = tmp_path / "suite.toml"
    suite_path.write_text(
        """
name = "s"
task = "some/task"

[judge]
model = "x"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[[rows]]
models = ["ollama/a"]
thinking = ["off"]
routes = ["direct"]
""",
        encoding="utf-8",
    )
    result = runner.invoke(agent_app, ["run", "--suite", str(suite_path)])
    assert result.exit_code == 0

    from modelman.state import load_state

    state = load_state()
    assert state.extra["benchmarks"]["agent_last_run"] == str(fake_run_dir)
```

- [ ] **Step 3: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_cli.py -v`
Expected: FAIL with `ModuleNotFoundError`

- [ ] **Step 4: Write the implementation**

`modelman/src/modelman/benchmark/agent/cli.py`:
```python
"""CLI for `modelman benchmark agent`."""

from __future__ import annotations

from pathlib import Path

import typer

from modelman.benchmark.agent.runner import run_suite
from modelman.benchmark.agent.suite import load_suite
from modelman.benchmark.agent.task import list_task_bundles
from modelman.benchmark.errors import BenchmarkError
from modelman.registry import load_registry
from modelman.state import load_state, save_state

agent_app = typer.Typer(help="Agentic coding benchmark harness (real coding task, gates + judge).")

DEFAULT_TASKS_DIR = Path("benchmarks/tasks")
DEFAULT_SUITES_DIR = Path("benchmarks/suites")


@agent_app.command("list-tasks")
def list_tasks_cmd(
    root: Path = typer.Option(DEFAULT_TASKS_DIR, "--root", help="Directory containing task bundles"),  # noqa: B008
) -> None:
    """List task bundles under root."""
    for task in list_task_bundles(root):
        hidden_count = len(task.gates_config.get("hidden", {}).get("files", []))
        typer.echo(f"{task.task_id}  (hidden files: {hidden_count})")


@agent_app.command("list-suites")
def list_suites_cmd(
    root: Path = typer.Option(DEFAULT_SUITES_DIR, "--root", help="Directory containing suite TOML files"),  # noqa: B008
) -> None:
    """List suite files under root, with their expanded row count."""
    if not root.is_dir():
        return
    registry = load_registry()
    for path in sorted(root.glob("*.toml")):
        try:
            suite = load_suite(path, registry)
        except BenchmarkError as exc:
            typer.echo(f"{path.name}: error: {exc}", err=True)
            continue
        typer.echo(f"{path.name}  ({suite.name}, {len(suite.rows)} rows)")


@agent_app.command("run")
def run_cmd(
    suite: Path = typer.Option(..., "--suite", help="Path to a suite TOML file"),  # noqa: B008
    row: list[str] = typer.Option([], "--row", help="Row label or index to run (repeatable)"),  # noqa: B008
    results_dir: Path | None = typer.Option(  # noqa: B008
        None, "--results-dir", help="Directory for run artifacts"
    ),
    dry_run: bool = typer.Option(False, "--dry-run", help="Resolve and print the row matrix; run nothing"),
) -> None:
    """Run a suite against a real coding task."""
    registry = load_registry()
    try:
        loaded_suite = load_suite(suite, registry)
    except BenchmarkError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc

    rows = loaded_suite.rows
    if row:
        wanted = set(row)
        rows = [r for i, r in enumerate(rows, start=1) if r.label in wanted or str(i) in wanted]

    if dry_run:
        for i, r in enumerate(rows, start=1):
            typer.echo(
                f"{i:02d}  {r.label}  model={r.model_id}  thinking={r.thinking}  "
                f"route={r.route}  provider={r.provider_id}"
            )
        typer.echo(f"{len(rows)} row(s) resolved, dry run — nothing executed")
        return

    try:
        run_dir, results = run_suite(loaded_suite, registry, row_filter=row or None, results_dir=results_dir)
    except BenchmarkError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc

    state = load_state()
    benchmarks = state.extra.setdefault("benchmarks", {})
    benchmarks["agent_last_run"] = str(run_dir)
    save_state(state)

    ok = sum(1 for r in results if r.error is None)
    typer.echo(f"Agent benchmark complete: {len(results)} row(s), {ok} ran without an isolation error")
    typer.echo(f"Results: {run_dir}")


__all__ = ["agent_app"]
```

In `modelman/src/modelman/benchmark/cli.py`, add the import and mount:
```python
from modelman.benchmark.agent.cli import agent_app
```
and, immediately after `benchmark_app = typer.Typer(help="Benchmark local LLM models.")`:
```python
benchmark_app.add_typer(agent_app, name="agent")
```

- [ ] **Step 5: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_cli.py -v`
Expected: PASS (3 tests)

- [ ] **Step 6: Run the full Phase 4 slice, verify `--dry-run` against the real bundle, and commit**

Run: `uv run pytest tests/benchmark/agent/ -v` — all tests from Tasks 1–15 pass.

Run (manual, from the monorepo root, not `modelman/`): `cd .. && uv run --project modelman modelman benchmark agent run --suite benchmarks/suites/smoke.toml --dry-run` (or `cd modelman && uv run modelman benchmark agent run --suite ../benchmarks/suites/smoke.toml --dry-run` if running from `modelman/`) — should print exactly one resolved row and "dry run — nothing executed", proving `list_task_bundles`/`load_suite`/the CLI wiring all agree on real paths, not just fixtures.

```bash
git add modelman/src/modelman/benchmark/agent/cli.py \
        modelman/src/modelman/benchmark/cli.py \
        modelman/tests/benchmark/agent/test_cli.py \
        benchmarks/suites/smoke.toml
git commit -m "feat(agent-bench): add agent CLI + smoke suite - completes plan item #15"
```

---
