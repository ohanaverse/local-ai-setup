"""Loads the eval suites shipped under benchmarks/suites so a malformed one fails CI.

Hermetic: an in-memory Registry (no registry file, HOME or network); the suite is
only parsed and its rows resolved, never run.
"""

import tomllib
from dataclasses import asdict
from pathlib import Path

from llmbench.benchmark.eval.suite import JUDGE_ROUTES, Suite, load_suite, resolve_row_endpoint
from llmbench.registry import ModelEntry, ProviderEntry, Registry

SUITE = Path(__file__).resolve().parents[4] / "benchmarks" / "suites" / "eval-sushi-vs-ollama.toml"
SUSHI_ID = "sushi/beamster--Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw"
SUSHI_NAME = "beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw"
OLLAMA_ID = "ollama/qwen3.8:27b-mlx"


def _registry() -> Registry:
    return Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", location="local"),
            ProviderEntry(id="sushi", name="Sushi", location="local"),
        ],
        models=[
            ModelEntry(
                id=OLLAMA_ID, family="qwen3.8", provider_id="ollama", model_name="qwen3.8:27b-mlx"
            ),
            ModelEntry(id=SUSHI_ID, family="qwen3.8", provider_id="sushi", model_name=SUSHI_NAME),
        ],
    )


def _loadable(tmp_path: Path) -> Path:
    # The shipped suite judges through claude-cli (PR #505). While this branch's
    # JUDGE_ROUTES lacks it, load a temp copy that judges through openrouter.
    if "claude-cli" in JUDGE_ROUTES:
        return SUITE
    copy = tmp_path / SUITE.name
    text = SUITE.read_text(encoding="utf-8")
    assert 'route = "claude-cli"' in text
    copy.write_text(text.replace('route = "claude-cli"', 'route = "openrouter"'), encoding="utf-8")
    return copy


def _suite(tmp_path: Path) -> Suite:
    assert SUITE.parent.is_dir()
    return load_suite(_loadable(tmp_path), _registry())


def test_the_suite_judges_through_the_claude_cli_route():
    # The shipped file must keep the subscription judge route with one sample (quota);
    # the temp-copy substitution in _loadable must never leak into the shipped file.
    judge = tomllib.loads(SUITE.read_text(encoding="utf-8"))["judge"]
    assert judge["route"] == "claude-cli"
    assert judge["samples"] == 1


def test_rows_are_sushi_then_ollama_and_group_serially(tmp_path):
    # Sushi first so its restore (stop) runs before the claude-cli judge and the
    # ollama row; the order is the run order.
    suite = _suite(tmp_path)
    assert suite.name == "eval-sushi-vs-ollama"
    assert [r.model_id for r in suite.rows] == [SUSHI_ID, OLLAMA_ID]
    assert [r.provider_id for r in suite.rows] == ["sushi", "ollama"]
    assert [r.route for r in suite.rows] == ["direct", "litellm"]


def test_both_rows_pin_the_same_explicit_sampling(tmp_path):
    # Sampling is suite-wide, so both rows get identical explicit values; a fair
    # comparison must not depend on each backend's defaults.
    suite = _suite(tmp_path)
    got = asdict(suite.generation)
    assert {k: got[k] for k in ("temperature", "top_p", "top_k")} == {
        "temperature": 1.0,
        "top_p": 0.95,
        "top_k": 20,
    }


def test_the_sushi_direct_route_sends_the_pack_id(tmp_path):
    # The direct route targets Sushi's listener and sends the registry model_name
    # verbatim (no direct_model override), which is the id Sushi serves.
    suite = _suite(tmp_path)
    assert suite.routes_direct["sushi"].base_url == "http://localhost:12345/v1"
    row = suite.rows[0]
    assert row.direct_model is None
    _, model, _ = resolve_row_endpoint(row, SUSHI_NAME, suite.routes_direct)
    assert model == SUSHI_NAME


def test_the_suite_never_names_a_second_big_model(tmp_path):
    # One 59 GB wired-limit budget: only the two compared models may appear, so a
    # third big row cannot be added to this suite by accident.
    suite = _suite(tmp_path)
    assert {r.model_id for r in suite.rows} == {SUSHI_ID, OLLAMA_ID}
