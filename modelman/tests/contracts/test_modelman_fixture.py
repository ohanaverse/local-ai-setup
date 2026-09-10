from pathlib import Path

from modelman.state import load_state

FIXTURE = Path(__file__).resolve().parents[3] / "docs" / "contracts" / "modelman.sample.toml"


def test_load_state_matches_shared_fixture():
    """Pins modelman's read of the shared docs/contracts/modelman.sample.toml
    fixture: both languages must decode `exposed`, the one legacy
    `litellm_exposed`-only entry, and the new `[litellm]` table identically,
    or a schema drift between modelman and wt ships silently."""
    state = load_state(path=FIXTURE)

    # Fully-populated model entry: every field modelman writes round-trips.
    local = state.get("ollama/contract-fixture:local")
    assert local.ready is True
    assert local.disk_path == "ollama:contract-fixture:local"
    assert local.size_bytes == 2147483648
    assert local.exposed is False

    sub = state.get("ollama/contract-fixture:subscription")
    assert sub.exposed is True
    assert sub.ready is True

    # Legacy spelling: `downloaded` must still be accepted as `ready`
    # (pre-registry files keep working; wt only reads the exposure flag).
    # Legacy `litellm_exposed` only (no `exposed` key) must read as exposed.
    legacy = state.get("llamacpp/legacy-contract-fixture")
    assert legacy.ready is True
    assert legacy.disk_path == "/hf/cache/legacy-contract-fixture.q4.gguf"
    assert legacy.exposed is True

    # Bare entry with no keys: all defaults, not an error.
    cloud = state.get("openrouter/contract-fixture:cloud")
    assert cloud.ready is False
    assert cloud.disk_path is None
    assert cloud.size_bytes is None
    assert cloud.exposed is False

    # New case: local model with flag on but ready off
    local_not_ready = state.get("ollama/contract-fixture:local-not-ready")
    assert local_not_ready is not None
    assert local_not_ready.ready is False
    assert local_not_ready.exposed is True

    # New case: cloud model with flag on (no ready key)
    cloud_exposed = state.get("openrouter/contract-fixture:cloud-exposed")
    assert cloud_exposed is not None
    assert cloud_exposed.ready is False  # defaults to false
    assert cloud_exposed.exposed is True

    # Legacy families table stays loadable (display names moved to
    # registry.toml [[families]], but old entries must not break loads).
    family = state.families.get("contract-fixture")
    assert family is not None
    assert family.display_name == "Contract Fixture (legacy)"

    # [litellm] routing table
    assert state.litellm.enabled is True
    assert state.litellm.url == "http://localhost:4000"
    assert state.litellm.api_key == "sk-litellm-CONTRACT-FIXTURE-NOT-A-REAL-KEY"

    # Global price-refresh timestamp (issue #69): modelman writes this
    # top-level key; wt reads it to notify on stale pricing.
    assert state.extra.get("price_refresh_last_run") == "2026-09-14"
