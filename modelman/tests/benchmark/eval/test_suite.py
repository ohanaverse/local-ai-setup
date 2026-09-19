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


def test_load_suite_rejects_unknown_model_even_with_explicit_provider(tmp_path):
    # An explicit `provider =` on a row must not bypass the unknown-model
    # check: the row's model_id is still looked up in the registry (to
    # build the RowConfig's other fields), so an unknown model_id paired
    # with an explicit provider must raise the same clean BenchmarkError as
    # the no-provider case, not a raw KeyError.
    body = SUITE_BODY.replace(
        'model = "ollama/a"\nroute = "litellm"',
        'model = "ollama/nope"\nroute = "litellm"\nprovider = "ollama"',
    )
    with pytest.raises(BenchmarkError, match="unknown model"):
        load_suite(_write(tmp_path, body), _registry())


def test_load_suite_rejects_missing_judge_table_with_clean_error(tmp_path):
    # A hand-written first suite that omits [judge] must fail with the same
    # clean one-line BenchmarkError every other suite malformation produces
    # (unknown model, duplicate label, unknown provider) — a raw
    # KeyError traceback past the CLI's BenchmarkError catch would be the
    # one hostile path in an otherwise uniform error surface.
    body = SUITE_BODY.replace(
        """[judge]
model = "anthropic/claude-opus-5"
temperature = 0.0
samples = 1
max_attempts = 2
route = "openrouter"

""",
        "",
    )
    with pytest.raises(BenchmarkError, match=r"missing the required \[judge\] table"):
        load_suite(_write(tmp_path, body), _registry())


def test_load_suite_rejects_missing_name_with_clean_error(tmp_path):
    # Same clean-error discipline for a missing top-level `name`: metrics
    # and summaries identify the run by it, and a hand-edited suite that
    # lost it should say so in one line, not KeyError: 'name'.
    body = SUITE_BODY.replace('name = "eval-test"\n', "")
    with pytest.raises(BenchmarkError, match="missing the required name field"):
        load_suite(_write(tmp_path, body), _registry())


def test_load_suite_rejects_judge_missing_model_or_route_with_clean_error(tmp_path):
    # A [judge] table that lost a required key (hand-edit or merge-conflict
    # resolution) must name the missing key in a clean error rather than
    # dying on raw dict access mid-construction.
    body = SUITE_BODY.replace('model = "anthropic/claude-opus-5"\n', "")
    with pytest.raises(BenchmarkError, match=r"\[judge\] is missing the required model key"):
        load_suite(_write(tmp_path, body), _registry())

    body = SUITE_BODY.replace('route = "openrouter"\n', "")
    with pytest.raises(BenchmarkError, match=r"\[judge\] is missing the required route key"):
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


def test_load_suite_rejects_duplicate_labels(tmp_path):
    # Two rows sharing an explicit label both write metrics.jsonl lines
    # keyed by that label; the rejudge reconstruction can only keep one
    # row's model_id/route for a colliding label, which misattributes
    # scores across rows. Reject the suite at load time instead.
    body = (
        SUITE_BODY
        + '\n\n[[rows]]\nmodel = "ollama/a"\nroute = "litellm"\nlabel = "dup"\n'
        + '[[rows]]\nmodel = "ollama/a"\nroute = "litellm"\nlabel = "dup"\n'
    )
    with pytest.raises(BenchmarkError, match="duplicate row label"):
        load_suite(_write(tmp_path, body), _registry())


def test_load_suite_rejects_unknown_explicit_provider(tmp_path):
    # An explicit `provider =` must name a known provider id (registry
    # providers or lifecycle BACKENDS): a typo'd id passes load, passes
    # preflight (BACKENDS.get misses → skipped), and in run_suite's
    # grouping never matches ISOLATABLE_PROVIDERS — so no isolation, no
    # error, and a litellm-routed row benchmarks against whatever is
    # currently loaded, silently violating the mandatory-isolation
    # invariant.
    body = SUITE_BODY.replace(
        'model = "ollama/a"\nroute = "litellm"',
        'model = "ollama/a"\nroute = "litellm"\nprovider = "omlqx"',
    )
    with pytest.raises(BenchmarkError, match="unknown provider"):
        load_suite(_write(tmp_path, body), _registry())


def test_preflight_scoped_rows_ignores_unselected_providers(tmp_path, monkeypatch):
    # Preflighting only the SELECTED rows (--row) must not fail because an
    # unselected row's provider is down or a direct block is missing — the
    # selection never touches them, so their preconditions don't apply.
    # The judge-route default branch (judge_route_active=None) calls the
    # real openrouter_key(), so seed a fake key via the env and point the
    # plist path at a nonexistent file — otherwise the test's pass/fail
    # depends on this host's live credentials (latent CI flake).
    monkeypatch.setenv("OPENROUTER_API_KEY", "test-key")
    monkeypatch.setattr("modelman.benchmark.eval.suite.LITELLM_PLIST", tmp_path / "missing.plist")
    suite = load_suite(_write(tmp_path, SUITE_BODY), _registry())

    class _DownBackend:
        @staticmethod
        def check_available():
            return "not running"

    import unittest.mock as mock

    with mock.patch("modelman.benchmark.eval.suite.lifecycle") as mock_lc:
        mock_lc.BACKENDS = {"ollama": _DownBackend, "omlx": None}
        # Row 2 (omlx/direct) selected; row 1's ollama backend is "down"
        # and must not matter.
        preflight(suite, _registry(), rows=[suite.rows[1]])


def test_preflight_judge_route_openrouter_ignored_for_coding_only_selection(tmp_path, monkeypatch):
    # preflight must mirror run_suite's needs_judge design: the judge's
    # OpenRouter key is only required when a selected row actually runs a
    # judged category. A coding-only selection (EvalPlus, purely local,
    # no judge call) must not be blocked on a missing OPENROUTER_API_KEY —
    # the repo's philosophy is degrade-to-N/A, not block a local run.
    monkeypatch.delenv("OPENROUTER_API_KEY", raising=False)
    monkeypatch.setattr("modelman.benchmark.eval.suite.LITELLM_PLIST", tmp_path / "missing.plist")
    suite = load_suite(_write(tmp_path, SUITE_BODY), _registry())
    coding_only = RowConfig(
        label="coding-only",
        model_id="ollama/a",
        route="litellm",
        provider_id="ollama",
        categories=["coding"],
    )
    preflight(suite, _registry(), rows=[coding_only], judge_route_active=False)


def test_load_suite_wraps_file_and_toml_errors_in_benchmark_error(tmp_path):
    # A mistyped --suite path or malformed TOML must surface as the clean
    # one-line BenchmarkError the CLI catches, not a FileNotFoundError /
    # TOMLDecodeError traceback.
    with pytest.raises(BenchmarkError, match="cannot read suite"):
        load_suite(tmp_path / "missing.toml", _registry())
    with pytest.raises(BenchmarkError, match="malformed TOML"):
        load_suite(_write(tmp_path, "name = [unterminated"), _registry())


def test_load_suite_rejects_wrongly_typed_fields_with_clean_errors(tmp_path):
    # Wrong-typed values used to raise bare TypeError/KeyError (a string
    # `samples` failing `"3" < 1`, a route block without base_url) or be
    # silently mis-read (a string `categories` iterated per character);
    # each must be a BenchmarkError naming the offending field.
    bad_bodies = {
        "samples": SUITE_BODY.replace("samples = 1", 'samples = "3"'),
        "base_url": SUITE_BODY.replace('base_url = "http://localhost:8000/v1"', "x = 1"),
        "categories": SUITE_BODY.replace('categories = ["coding"]', 'categories = "reasoning"'),
    }
    for key, body in bad_bodies.items():
        with pytest.raises(BenchmarkError, match=key):
            load_suite(_write(tmp_path, body), _registry())
