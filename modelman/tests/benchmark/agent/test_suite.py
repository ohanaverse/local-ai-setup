"""Tests for modelman.benchmark.agent.suite — TOML parsing and cartesian
row expansion.

Hand-writing every model x thinking x route combination is how suites stop
getting maintained (spec); these tests pin the expansion arithmetic and the
two error cases most likely to silently misconfigure a run: an unknown
model reference and a missing [routes.direct.<provider>] block.
"""

from pathlib import Path

import pytest

from modelman.benchmark.agent.pidriver import DirectRouteConfig
from modelman.benchmark.agent.suite import load_suite, preflight
from modelman.benchmark.agent.task import load_task
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


def test_direct_model_override_is_parsed(tmp_path):
    """direct_model carries the name a backend actually serves for a
    route=direct row. The registry model_name is org-prefixed for omlx
    (`mlx-community/X`) while the server knows only `X`, and resolve_pi_target
    (Task 5) needs the override to address it."""
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
label = "omlx-direct"
model = "omlx/b"
thinking = "off"
route = "direct"
direct_model = "X"
"""
    suite = load_suite(_write_suite(tmp_path, body), _registry())
    assert suite.rows[0].direct_model == "X"


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
