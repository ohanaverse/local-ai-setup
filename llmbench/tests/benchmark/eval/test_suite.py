"""Tests for llmbench.benchmark.eval.suite — suite TOML parsing, row
expansion, and (model, route) -> endpoint resolution.
"""

import json
from pathlib import Path

import pytest

from llmbench.benchmark.errors import BenchmarkError
from llmbench.benchmark.eval.suite import (
    DirectRouteConfig,
    RowConfig,
    load_suite,
    preflight,
    resolve_row_endpoint,
)
from llmbench.registry import ModelEntry, ProviderEntry, Registry


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
    # Happy path: a full suite TOML expands into rows carrying the per-row category
    # scope and direct_model override, plus the suite-level coding config — the shape
    # every run/dry-run consumes.
    suite = load_suite(_write(tmp_path, SUITE_BODY), _registry())
    assert suite.name == "eval-test"
    assert suite.coding.dataset == "humaneval"
    assert suite.coding.limit == 5
    assert len(suite.rows) == 2
    row_b = next(r for r in suite.rows if r.model_id == "omlx/b")
    assert row_b.categories == ["coding"]
    assert row_b.direct_model == "b-server-name"


def test_load_suite_rejects_unknown_model(tmp_path):
    # A row naming a model absent from the registry must fail at load time with a clean
    # BenchmarkError rather than mid-run after providers were isolated.
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
    # A route=direct row needs a [routes.direct.<provider>] base_url; preflight must
    # catch its absence before any isolation work is paid for.
    body = SUITE_BODY.replace("[routes.direct.omlx]\n", "").replace(
        'base_url = "http://localhost:8000/v1"\n', ""
    )
    suite = load_suite(_write(tmp_path, body), _registry())
    with pytest.raises(BenchmarkError, match="routes.direct.omlx"):
        preflight(suite, _registry())


def test_resolve_row_endpoint_direct_route_uses_direct_model_override():
    # Direct routing sends the row's direct_model (omlx knows only the basename, not the
    # registry id) to the configured base_url; sending the registry name would 404.
    row = RowConfig(
        label="r", model_id="omlx/b", route="direct", provider_id="omlx", direct_model="server-name"
    )
    base_url, model, api_key = resolve_row_endpoint(
        row, "b-real-name", {"omlx": DirectRouteConfig(base_url="http://localhost:8000/v1")}
    )
    assert base_url == "http://localhost:8000/v1"
    assert model == "server-name"
    assert api_key == "ollama"


def _keyed_rec_registry(secret_ref: str = "rec-test-key") -> None:
    import os
    from pathlib import Path

    Path(os.environ["WT_REGISTRY"]).write_text(
        f'[[providers]]\nid = "rec"\nlocation = "local"\nauth = {{ secret_ref = "{secret_ref}" }}\n'
    )


def test_resolve_row_endpoint_direct_route_of_a_keyed_provider_returns_its_key():
    # rec refuses every route without its bearer key; the direct route must
    # send the registry row's key, not the keyless placeholder.
    _keyed_rec_registry()
    row = RowConfig(label="r", model_id="rec/flint", route="direct", provider_id="rec")
    _, model, api_key = resolve_row_endpoint(
        row, "flint", {"rec": DirectRouteConfig(base_url="http://127.0.0.1:11500/v1")}
    )
    assert model == "flint"
    found = api_key == "rec-test-key"
    assert found


def test_resolve_row_endpoint_direct_route_unreadable_key_is_a_row_error(tmp_path):
    script = tmp_path / "keycmd"
    script.write_text("#!/bin/sh\necho SECRETVALUE\nexit 1\n")
    script.chmod(0o755)
    _keyed_rec_registry(f"exec:{script}")
    row = RowConfig(label="r", model_id="rec/flint", route="direct", provider_id="rec")
    with pytest.raises(BenchmarkError) as ei:
        resolve_row_endpoint(
            row, "flint", {"rec": DirectRouteConfig(base_url="http://127.0.0.1:11500/v1")}
        )
    msg = str(ei.value)
    assert ("cannot read the provider key" in msg, "SECRETVALUE" in msg) == (True, False)


def test_resolve_row_endpoint_litellm_route_reads_live_models_json(tmp_path):
    # LiteLLM routing reads base URL and key from the live pi models.json and sends the
    # full registry id (the LiteLLM model_list is keyed on it).
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
    monkeypatch.setattr("llmbench.benchmark.eval.suite.LITELLM_PLIST", tmp_path / "missing.plist")
    suite = load_suite(_write(tmp_path, SUITE_BODY), _registry())

    class _DownBackend:
        @staticmethod
        def check_available():
            return "not running"

    import unittest.mock as mock

    with mock.patch("llmbench.benchmark.eval.suite.lifecycle") as mock_lc:
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
    monkeypatch.setattr("llmbench.benchmark.eval.suite.LITELLM_PLIST", tmp_path / "missing.plist")
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


def test_load_suite_reads_and_validates_coding_timeout(tmp_path):
    # [coding] timeout_s lets a suite lengthen the whole-run EvalPlus budget;
    # it must round-trip when valid and be a clean BenchmarkError when not.
    ok = load_suite(
        _write(tmp_path, SUITE_BODY.replace("limit = 5", "timeout_s = 9000")), _registry()
    )
    assert ok.coding.timeout_s == 9000
    with pytest.raises(BenchmarkError, match="timeout_s"):
        load_suite(
            _write(tmp_path, SUITE_BODY.replace("limit = 5", 'timeout_s = "9"')), _registry()
        )


def test_load_suite_generation_defaults_to_greedy(tmp_path):
    # A suite with no [generation] table keeps the rows' historical greedy
    # sampling and sends no top_p/top_k, so older suites score as before.
    suite = load_suite(_write(tmp_path, SUITE_BODY), _registry())
    assert suite.generation.temperature == 0.0
    assert suite.generation.extra_params() == {}


def test_load_suite_reads_generation_table(tmp_path):
    body = SUITE_BODY + "\n[generation]\ntemperature = 0.6\ntop_p = 0.95\ntop_k = 20\n"
    suite = load_suite(_write(tmp_path, body), _registry())
    assert suite.generation.temperature == 0.6
    assert suite.generation.extra_params() == {"top_p": 0.95, "top_k": 20}


@pytest.mark.parametrize(
    ("table", "message"),
    [
        ("temp = 0.6", "unknown key"),
        ("temperature = -1", "temperature must be"),
        ('temperature = "0.6"', "temperature must be"),
        ("top_p = 0", "top_p must be"),
        ("top_p = 1.5", "top_p must be"),
        ("top_k = 0", "top_k must be"),
        ("top_k = 2.5", "top_k must be"),
        ("top_k = true", "top_k must be"),
    ],
)
def test_load_suite_refuses_bad_generation(tmp_path, table, message):
    body = SUITE_BODY.replace(
        "[routes.direct.omlx]", f"[generation]\n{table}\n\n[routes.direct.omlx]"
    )
    with pytest.raises(BenchmarkError, match=message):
        load_suite(_write(tmp_path, body), _registry())


# --- claude-cli judge route -------------------------------------------------

CLAUDE_CLI_BODY = SUITE_BODY.replace(
    'route = "openrouter"\n\n[coding]', 'route = "claude-cli"\n\n[coding]'
)


def _claude_cli_suite(tmp_path):
    return load_suite(_write(tmp_path, CLAUDE_CLI_BODY), _registry())


def _local_row() -> RowConfig:
    return RowConfig(
        label="r", model_id="ollama/a", route="litellm", provider_id="ollama", categories=None
    )


def test_load_suite_accepts_claude_cli_judge_route(tmp_path):
    assert _claude_cli_suite(tmp_path).judge.route == "claude-cli"


def test_load_suite_rejects_unknown_judge_route_listing_claude_cli(tmp_path):
    body = SUITE_BODY.replace('route = "openrouter"\n\n[coding]', 'route = "bogus"\n\n[coding]')
    with pytest.raises(BenchmarkError, match=r"\[judge\] route must be one of.*claude-cli"):
        load_suite(_write(tmp_path, body), _registry())


def test_preflight_claude_cli_needs_no_openrouter_key(tmp_path, monkeypatch):
    # The subscription route must not be blocked on (or read) an OpenRouter key.
    monkeypatch.delenv("OPENROUTER_API_KEY", raising=False)
    monkeypatch.setattr("llmbench.benchmark.eval.suite.LITELLM_PLIST", tmp_path / "missing.plist")
    monkeypatch.setattr("llmbench.benchmark.eval.suite.shutil.which", lambda name: "/x/claude")
    monkeypatch.setattr("llmbench.benchmark.eval.suite._claude_logged_in", lambda exe: True)
    suite = _claude_cli_suite(tmp_path)
    preflight(suite, _registry(), rows=[_local_row()], judge_route_active=True)
    preflight(suite, _registry(), rows=[_local_row()])  # None: conservatively active


def test_preflight_claude_cli_missing_cli_raises(tmp_path, monkeypatch):
    monkeypatch.setattr("llmbench.benchmark.eval.suite.shutil.which", lambda name: None)
    suite = _claude_cli_suite(tmp_path)
    with pytest.raises(BenchmarkError, match="claude CLI on PATH"):
        preflight(suite, _registry(), rows=[_local_row()], judge_route_active=True)


def test_preflight_claude_cli_not_logged_in_raises(tmp_path, monkeypatch):
    monkeypatch.setattr("llmbench.benchmark.eval.suite.shutil.which", lambda name: "/x/claude")
    monkeypatch.setattr("llmbench.benchmark.eval.suite._claude_logged_in", lambda exe: False)
    suite = _claude_cli_suite(tmp_path)
    with pytest.raises(BenchmarkError, match="claude auth login"):
        preflight(suite, _registry(), rows=[_local_row()])


def test_preflight_claude_cli_ignored_for_coding_only_selection(tmp_path, monkeypatch):
    # A coding-only sweep never calls the judge, so it must not need the CLI.
    def boom(*args, **kwargs):
        raise AssertionError("claude CLI must not be consulted")

    monkeypatch.setattr("llmbench.benchmark.eval.suite.shutil.which", boom)
    suite = _claude_cli_suite(tmp_path)
    preflight(suite, _registry(), rows=[_local_row()], judge_route_active=False)


def test_preflight_claude_cli_judge_with_openrouter_row_still_needs_key(tmp_path, monkeypatch):
    monkeypatch.delenv("OPENROUTER_API_KEY", raising=False)
    monkeypatch.setattr("llmbench.benchmark.eval.suite.LITELLM_PLIST", tmp_path / "missing.plist")
    monkeypatch.setattr("llmbench.benchmark.eval.suite.shutil.which", lambda name: "/x/claude")
    monkeypatch.setattr("llmbench.benchmark.eval.suite._claude_logged_in", lambda exe: True)
    suite = _claude_cli_suite(tmp_path)
    row = RowConfig(
        label="or", model_id="ollama/a", route="openrouter", provider_id="ollama", categories=None
    )
    with pytest.raises(BenchmarkError, match="OPENROUTER_API_KEY"):
        preflight(suite, _registry(), rows=[row], judge_route_active=True)


def _fake_status(tmp_path, body: str, *, mode: str = "echo"):
    # A real executable (not named `claude`, which the hermetic guard fakes)
    # standing in for `claude auth status`; records the env it was given.
    script = tmp_path / "fake_cli.py"
    script.write_text(
        "#!/usr/bin/env python3\n"
        "import os, sys, time\n"
        f"open({str(tmp_path / 'env.json')!r}, 'w').write(\n"
        "    repr(sorted(k for k in os.environ if k.startswith(('ANTHROPIC_', 'CLAUDE_CODE_')))))\n"
        f"if {mode!r} == 'slow':\n    time.sleep(30)\n"
        f"sys.stdout.write({body!r})\n"
        f"sys.exit({1 if mode == 'fail' else 0})\n",
        encoding="utf-8",
    )
    script.chmod(0o755)
    return str(script)


@pytest.mark.parametrize(
    ("body", "mode", "expected"),
    [
        ('{"loggedIn": true, "authMethod": "claude.ai"}', "echo", True),
        ('{"loggedIn": false}', "echo", False),
        ("", "echo", False),
        ("not json", "echo", False),
        ("[1]", "echo", False),
        ('{"loggedIn": "yes"}', "echo", False),
    ],
)
def test_claude_logged_in_parses_auth_status(tmp_path, body, mode, expected):
    from llmbench.benchmark.eval.suite import _claude_logged_in

    assert _claude_logged_in(_fake_status(tmp_path, body, mode=mode)) is expected


def test_claude_logged_in_scrubs_env_and_handles_missing_executable(tmp_path, monkeypatch):
    from llmbench.benchmark.eval.suite import _claude_logged_in

    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-not-real")
    monkeypatch.setenv("CLAUDE_CODE_USE_BEDROCK", "1")
    exe = _fake_status(tmp_path, '{"loggedIn": true}')
    assert _claude_logged_in(exe) is True
    assert (tmp_path / "env.json").read_text() == "[]"
    assert _claude_logged_in(str(tmp_path / "does-not-exist")) is False


def test_claude_logged_in_timeout_is_not_logged_in(tmp_path, monkeypatch):
    import subprocess
    from unittest import mock

    from llmbench.benchmark.eval import suite as suite_mod

    with mock.patch.object(
        suite_mod.subprocess, "run", side_effect=subprocess.TimeoutExpired("claude", 30)
    ):
        assert suite_mod._claude_logged_in("claude") is False
