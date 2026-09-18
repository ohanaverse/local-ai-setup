"""Tests for modelman.benchmark._routes — the shared credential helpers
both benchmark subsystems (agent/ and eval/) resolve their routes
through."""

import json

import pytest

from modelman.benchmark._routes import litellm_credentials, load_live_models
from modelman.benchmark.errors import BenchmarkError


def test_load_live_models_missing_path_returns_empty(tmp_path):
    # A missing models.json must read as "no providers" rather than raising
    # — the first wt/pi session hasn't necessarily happened yet, and every
    # caller treats "no litellm entry" as the actionable error, not
    # "file absent".
    assert load_live_models(tmp_path / "nope.json") == {}


def test_litellm_credentials_returns_base_url_and_key(tmp_path):
    # Both benchmarks' judge transports (and eval's litellm route) key off
    # the same (base_url, apiKey) pair from pi's models.json — one shared
    # extraction, so a format change is fixed once.
    live_path = tmp_path / "models.json"
    live_path.write_text(
        json.dumps(
            {"providers": {"litellm": {"baseUrl": "http://localhost:4000/v1", "apiKey": "sk-x"}}}
        ),
        encoding="utf-8",
    )
    assert litellm_credentials(live_path) == ("http://localhost:4000/v1", "sk-x")


def test_litellm_credentials_defaults_base_url_when_unspecified(tmp_path):
    # models.json may omit baseUrl (pi writes only what it needs); the
    # canonical local gateway default must apply, not a None/blank base_url.
    live_path = tmp_path / "models.json"
    live_path.write_text(json.dumps({"providers": {"litellm": {"apiKey": "sk-x"}}}), encoding="utf-8")
    assert litellm_credentials(live_path) == ("http://localhost:4000/v1", "sk-x")


def test_litellm_credentials_missing_key_raises(tmp_path):
    # No apiKey means no way to authenticate to the gateway — the shared
    # error must name the seeding remedy (run a wt pi session in litellm
    # mode) so users know how to fix it, matching the pre-refactor callers.
    live_path = tmp_path / "models.json"
    live_path.write_text(json.dumps({"providers": {}}), encoding="utf-8")
    with pytest.raises(BenchmarkError, match="apiKey"):
        litellm_credentials(live_path)
