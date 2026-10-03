from pathlib import Path

from modelman.state import load_state

FIXTURE = Path(__file__).resolve().parents[3] / "docs" / "contracts" / "modelman.sample.toml"


def test_load_state_matches_shared_fixture():
    """Pins modelman's read of the shared docs/contracts/modelman.sample.toml
    fixture: both languages must decode `ready` (including the legacy
    `downloaded` spelling) and the `[litellm]` table identically, or a schema
    drift between modelman and wt ships silently. #179: the legacy
    `exposed`/`litellm_exposed` keys some rows still carry (wt reads them
    until PR 3) load without error and are ignored by modelman."""
    state = load_state(path=FIXTURE)

    # Fully-populated model entry: every field modelman writes round-trips.
    local = state.get("ollama/contract-fixture:local")
    assert local.ready is True
    assert local.disk_path == "ollama:contract-fixture:local"
    assert local.size_bytes == 2147483648

    sub = state.get("ollama/contract-fixture:subscription")
    assert sub.ready is True

    # Legacy spelling: `downloaded` must still be accepted as `ready`
    # (pre-registry files keep working).
    legacy = state.get("llamacpp/legacy-contract-fixture")
    assert legacy.ready is True
    assert legacy.disk_path == "/hf/cache/legacy-contract-fixture.q4.gguf"

    # #179: modelman has no exposure flag at all — the legacy keys are
    # neither a field nor preserved as unknown extras (so the next save
    # drops them).
    for row in (local, sub, legacy):
        assert not hasattr(row, "exposed")
        assert "exposed" not in row.extra and "litellm_exposed" not in row.extra

    # Bare entry with no keys: all defaults, not an error.
    cloud = state.get("openrouter/contract-fixture:cloud")
    assert cloud.ready is False
    assert cloud.disk_path is None
    assert cloud.size_bytes is None

    # Local model that is not ready.
    local_not_ready = state.get("ollama/contract-fixture:local-not-ready")
    assert "ollama/contract-fixture:local-not-ready" in state.models
    assert local_not_ready.ready is False

    # Legacy families table stays loadable (display names moved to
    # registry.toml [[families]], but old entries must not break loads).
    family = state.families.get("contract-fixture")
    assert family is not None
    assert family.display_name == "Contract Fixture (legacy)"

    # [litellm] routing table
    # (kept verbatim in extra: wt owns it, modelman only preserves it)
    assert state.extra["litellm"]["enabled"] is True
    assert state.extra["litellm"]["url"] == "http://localhost:4000"
    assert state.extra["litellm"]["api_key"] == "sk-litellm-CONTRACT-FIXTURE-NOT-A-REAL-KEY"

    # Per-model running flag (2026-09-14 multi-model design): replaces
    # the single [local].running_model marker.
    assert local.running is True
    assert sub.running is False

    # Global price-refresh timestamp (issue #69): modelman writes this
    # top-level key; wt reads it to notify on stale pricing.
    assert state.extra.get("price_refresh_last_run") == "2026-09-14"
