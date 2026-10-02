"""Cross-language contract for the OpenRouter-priced predicate (#180).

wt's config.OpenRouterPriced asserts the same expected list
(wt/internal/config/catalog_predicates_fixture_test.go), so a one-sided
change to the refresh-candidate rule fails both CI jobs.
"""

import json
from pathlib import Path

from modelman.pricing import _is_openrouter_priced
from modelman.registry import load_registry

CONTRACTS = Path(__file__).resolve().parents[3] / "docs" / "contracts"


def test_openrouter_priced_matches_shared_fixture():
    registry = load_registry(path=CONTRACTS / "catalog-predicates.sample.toml")
    expected = json.loads((CONTRACTS / "catalog-predicates.expected.json").read_text())
    priced = [m.id for m in registry.models if _is_openrouter_priced(registry, m)]
    assert priced == expected["openrouter_priced"]
