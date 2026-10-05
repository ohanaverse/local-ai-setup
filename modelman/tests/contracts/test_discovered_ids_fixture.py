"""Cross-language contract: the id of an on-disk local model with no
registry entry.

Read together with wt/internal/litellm/discovered_ids_fixture_test.go; both
consume docs/contracts/discovered-ids.sample.json, so a format change on
either side fails both CI jobs in the same PR."""

import json
from pathlib import Path

from modelman.local_control import DiscoveredModel, _discovered_id

FIXTURE = Path(__file__).resolve().parents[3] / "docs" / "contracts" / "discovered-ids.sample.json"


def test_discovered_ids_match_wt():
    # modelman flags a discovered model running, stops it and names it in its
    # listings under this id; wt writes its LiteLLM route under the id it
    # derives itself. Each side pinned the format with its own literals, so
    # they could drift with both suites green. The prefix is the FAMILY: an
    # artifact found through an omlx-6bit row is "omlx/<name>".
    cases = json.loads(FIXTURE.read_text())["cases"]
    assert cases, "fixture has no cases"
    for c in cases:
        assert _discovered_id(c["provider"], c["artifact"]) == c["id"], c
        found = DiscoveredModel(
            provider_id=c["provider"], variant_id=c["artifact"], path="x", size_bytes=None
        )
        assert found.model_id == c["id"], c
