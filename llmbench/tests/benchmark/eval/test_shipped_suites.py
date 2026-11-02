"""Loads the eval suites shipped under benchmarks/suites so a malformed one fails CI.

Hermetic: the suite is parsed against an in-memory Registry and its rows resolved,
never run. resolve_row_endpoint on a direct row calls provider_key, which reads the
registry from disk (not the in-memory one), so this module's autouse fixture points
every registry env name at a nonexistent file under tmp_path; no HOME or network.
"""

import tomllib
from dataclasses import asdict
from pathlib import Path

import pytest

from llmbench.benchmark.eval.suite import Suite, load_suite, resolve_row_endpoint
from llmbench.providers.secret import provider_key
from llmbench.registry import ModelEntry, ProviderEntry, Registry, registry_path

SUITE = Path(__file__).resolve().parents[4] / "benchmarks" / "suites" / "eval-sushi-vs-ollama.toml"
FOUR_WAY = Path(__file__).resolve().parents[4] / "benchmarks" / "suites" / "eval-four-way.toml"
SUSHI_ID = "sushi/beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw"
SUSHI_NAME = "beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw"
OLLAMA_ID = "ollama/qwen3.8:27b-mlx"
REC_ID = "rec/flint"
MTPLX_QUALITY_ID = "mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"
MTPLX_BALANCE_ID = "mtplx/Youssofal--Qwen3.6-35B-A3B-MTPLX-Optimized-Balance"


@pytest.fixture(autouse=True)
def _isolated_registry(monkeypatch, tmp_path):
    # Independent of tests/conftest.py's global guard: WT_REGISTRY outranks the other
    # names, which are cleared so none can win if WT_REGISTRY is ever removed.
    missing = tmp_path / "no-registry.toml"
    monkeypatch.setenv("WT_REGISTRY", str(missing))
    monkeypatch.delenv("MODELMAN_REGISTRY", raising=False)
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    assert registry_path() == missing


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


def _suite(tmp_path: Path) -> Suite:
    assert SUITE.parent.is_dir()
    return load_suite(SUITE, _registry())


def test_the_suite_judges_through_the_claude_cli_route():
    # The shipped file must keep the subscription judge route with one sample (quota);
    # a test edit must never change the shipped file.
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
    # One 59 GB wired-limit budget: only the two compared models may appear.
    assert {r.model_id for r in suite.rows} == {SUSHI_ID, OLLAMA_ID}


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
    assert provider_key("sushi") is None  # the registry read hits the scratch path
    suite = _suite(tmp_path)
    assert suite.routes_direct["sushi"].base_url == "http://localhost:12345/v1"
    row = suite.rows[0]
    assert row.direct_model is None
    _, model, _ = resolve_row_endpoint(row, SUSHI_NAME, suite.routes_direct)
    assert model == SUSHI_NAME


def _four_way_registry() -> Registry:
    return Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", location="local"),
            ProviderEntry(id="mtplx", name="MTPLX", location="local"),
            ProviderEntry(id="rec", name="rec", location="local"),
        ],
        models=[
            ModelEntry(
                id=OLLAMA_ID, family="qwen3.8", provider_id="ollama", model_name="qwen3.8:27b-mlx"
            ),
            ModelEntry(id=REC_ID, family="flint", provider_id="rec", model_name="flint"),
            ModelEntry(
                id=MTPLX_QUALITY_ID,
                family="qwen3.8",
                provider_id="mtplx",
                model_name="Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality",
            ),
            ModelEntry(
                id=MTPLX_BALANCE_ID,
                family="qwen3.6",
                provider_id="mtplx",
                model_name="Youssofal/Qwen3.6-35B-A3B-MTPLX-Optimized-Balance",
            ),
        ],
    )


def test_the_four_way_suite_loads_and_names_its_rows():
    # Renamed from eval-five-way when the Sushi row was dropped. Loading it here
    # (against an in-memory Registry) catches a mistyped model id or a dropped
    # row in CI instead of on a real run, and pins the direct routes its rows need.
    assert FOUR_WAY.parent.is_dir()
    suite = load_suite(FOUR_WAY, _four_way_registry())
    assert suite.name == "eval-four-way"
    assert [r.model_id for r in suite.rows] == [
        OLLAMA_ID,
        REC_ID,
        MTPLX_QUALITY_ID,
        MTPLX_BALANCE_ID,
    ]
    assert [r.provider_id for r in suite.rows] == ["ollama", "rec", "mtplx", "mtplx"]
    direct_providers = {r.provider_id for r in suite.rows if r.route == "direct"}
    assert direct_providers <= set(suite.routes_direct)
    assert suite.judge.route == "claude-cli"
