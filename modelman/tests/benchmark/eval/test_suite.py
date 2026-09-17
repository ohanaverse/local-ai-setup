"""Tests for modelman.benchmark.eval.suite — suite TOML parsing, row
expansion, and (model, route) -> endpoint resolution.
"""

import json
from pathlib import Path

import pytest

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.eval.suite import (
    DirectRouteConfig,
    RowConfig,
    load_suite,
    preflight,
    resolve_row_endpoint,
)
from modelman.registry import ModelEntry, ProviderEntry, Registry


def _registry() -> Registry:
    return Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", location="local"),
            ProviderEntry(id="omlx", name="oMLX", location="local"),
        ],
        models=[
            ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a"),
            ModelEntry(id="omlx/b", family="f", provider_id="omlx", model_name="b-real-name"),
        ],
    )


def _write(tmp_path: Path, body: str) -> Path:
    path = tmp_path / "suite.toml"
    path.write_text(body, encoding="utf-8")
    return path


SUITE_BODY = """
name = "eval-test"
cooldown_s = 5

[judge]
model = "anthropic/claude-opus-5"
temperature = 0.0
samples = 1
max_attempts = 2
route = "openrouter"

[coding]
dataset = "humaneval"
limit = 5

[routes.direct.omlx]
base_url = "http://localhost:8000/v1"

[[rows]]
model = "ollama/a"
route = "litellm"

[[rows]]
model = "omlx/b"
route = "direct"
direct_model = "b-server-name"
categories = ["coding"]
"""


def test_load_suite_expands_rows_and_categories(tmp_path):
    suite = load_suite(_write(tmp_path, SUITE_BODY), _registry())
    assert suite.name == "eval-test"
    assert suite.coding.dataset == "humaneval"
    assert suite.coding.limit == 5
    assert len(suite.rows) == 2
    row_b = next(r for r in suite.rows if r.model_id == "omlx/b")
    assert row_b.categories == ["coding"]
    assert row_b.direct_model == "b-server-name"


def test_load_suite_rejects_unknown_model(tmp_path):
    body = SUITE_BODY.replace('model = "ollama/a"', 'model = "ollama/nope"')
    with pytest.raises(BenchmarkError, match="unknown model"):
        load_suite(_write(tmp_path, body), _registry())


def test_preflight_rejects_direct_row_missing_route_block(tmp_path):
    body = SUITE_BODY.replace("[routes.direct.omlx]\n", "").replace(
        'base_url = "http://localhost:8000/v1"\n', ""
    )
    suite = load_suite(_write(tmp_path, body), _registry())
    with pytest.raises(BenchmarkError, match="routes.direct.omlx"):
        preflight(suite, _registry())


def test_resolve_row_endpoint_direct_route_uses_direct_model_override():
    row = RowConfig(
        label="r", model_id="omlx/b", route="direct", provider_id="omlx", direct_model="server-name"
    )
    base_url, model, api_key = resolve_row_endpoint(
        row, "b-real-name", {"omlx": DirectRouteConfig(base_url="http://localhost:8000/v1")}
    )
    assert base_url == "http://localhost:8000/v1"
    assert model == "server-name"
    assert api_key == "ollama"


def test_resolve_row_endpoint_litellm_route_reads_live_models_json(tmp_path):
    live_path = tmp_path / "models.json"
    live_path.write_text(
        json.dumps(
            {"providers": {"litellm": {"baseUrl": "http://localhost:4000/v1", "apiKey": "sk-x"}}}
        ),
        encoding="utf-8",
    )
    row = RowConfig(label="r", model_id="ollama/a", route="litellm", provider_id="ollama")
    base_url, model, api_key = resolve_row_endpoint(row, "a", {}, live_models_path=live_path)
    assert base_url == "http://localhost:4000/v1"
    assert model == "ollama/a"
    assert api_key == "sk-x"
