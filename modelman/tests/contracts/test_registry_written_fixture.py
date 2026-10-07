"""The writer half of the registry contract.

docs/contracts/registry.written.sample.toml is one registry in the exact form
tomli-w gives it. It carries no comments (tomli-w writes none), so its purpose
is recorded here: wt's Go writer asserts that decoding the file and emitting it
again reproduces every byte (wt/internal/tomlw/fixture_test.go), and this file
asserts the same of tomli-w. Two fixed points on the same bytes prove the two
writers lay a registry out identically without either CI job running the other
language. llmbench/tests/test_registry.py asserts its reader loads the file.

Deleted with modelman: once wt is the only writer there is nothing left for
its output to agree with.
"""

import datetime
import tomllib
from pathlib import Path

import tomli_w

from modelman.registry import load_registry

FIXTURE = (
    Path(__file__).resolve().parents[3] / "docs" / "contracts" / "registry.written.sample.toml"
)


def test_tomli_w_reproduces_the_written_fixture():
    """If this fails the fixture is no longer in tomli-w form (it was edited
    by hand, or tomli-w changed its layout), and wt's emitter is being held to
    bytes modelman itself would not write."""
    text = FIXTURE.read_text(encoding="utf-8")
    assert tomli_w.dumps(tomllib.loads(text)) == text


def test_load_registry_accepts_the_written_fixture():
    """A registry wt writes must load in modelman for as long as modelman
    exists, with the keys neither tool models kept for the next save."""
    registry = load_registry(path=FIXTURE)

    assert [p.id for p in registry.providers] == ["ollama", "openrouter", "mlx_lm_server", "agy"]
    assert registry.provider("ollama").extra == {"x_provider_note": "an unknown provider key"}
    assert registry.provider("ollama").auth.extra == {"x_auth_note": "an unknown auth key"}
    assert [f.name for f in registry.families] == ["written-fixture", "written-second"]
    assert registry.families[1].extra == {"x_rank": 2}

    ints = registry.model("ollama/written-fixture:int")
    assert ints.tags == []
    assert ints.cost is not None
    assert (ints.cost.input_price_per_million, ints.cost.output_price_per_million) == (3.0, 15.0)
    assert ints.extra["catalog_name"] == "written-fixture"
    assert ints.extra["x_added"] == datetime.date(2026, 10, 1)
    assert ints.model_info["max_input_tokens"] == 131072

    cloud = registry.model("openrouter/written-fixture:cloud")
    assert cloud.cost is not None
    assert cloud.cost.extra == {"x_cost_note": "an unknown cost key"}
    assert cloud.cost.subscription_price == 20.0
    (off_peak,) = cloud.cost.time_prices
    assert off_peak.extra == {"x_row_note": "an unknown time-price key"}
    assert off_peak.windows[1].extra == {"x_window_note": "an unknown window key"}
    assert cloud.pricing_updated_at == "2026-10-01T00:00:00+00:00"

    pair = registry.model("mlx_lm_server/written-fixture:pair")
    assert pair.fetch is not None and pair.fetch.extra == {"x_fetch_note": "an unknown fetch key"}
    assert pair.draft is not None and pair.draft.extra == {"x_draft_note": "an unknown draft key"}
    assert pair.quantization == "4bit"
