"""Collection config for the agent-benchmark tests.

fixtures/tasks/ holds *task bundles* — miniature repositories that are data
for these tests, not tests. Their files are named test_pkg.py /
test_hidden.py on purpose (that is the layout unittest discovery expects
inside a seeded workspace, and the same layout as the real
benchmarks/tasks/day31-drift bundle), which is exactly the name pytest
collects. Collected in place they import `pkg`, a module that only exists
inside a seeded temp workspace, so the entire suite fails at collection
rather than just these fixtures.
"""

import json
from pathlib import Path

import pytest

collect_ignore = ["fixtures"]


@pytest.fixture
def litellm_models_json(tmp_path: Path) -> Path:
    """A stand-in for ~/.pi/agent/models.json with a gateway entry.

    A litellm-route row resolves its target from that file and raises without
    an apiKey, so a test that only cares about isolation still has to supply a
    resolvable one. Mirrors test_pidriver's helper; never read the real file.
    """
    path = tmp_path / "models.json"
    path.write_text(
        json.dumps(
            {
                "providers": {
                    "litellm": {
                        "baseUrl": "http://localhost:4000/v1",
                        "apiKey": "sk-test-key",
                        "models": [],
                    }
                }
            }
        ),
        encoding="utf-8",
    )
    return path
