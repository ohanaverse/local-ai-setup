"""Cross-language contract: the `wt start <id> --json` shapes modelman parses.

Read together with wt/cmd/wt/start_json_test.go; both consume
docs/contracts/wt-start-cli.sample.json, so a schema change on either side
fails both CI jobs in the same PR."""

import json
from pathlib import Path

import pytest

from modelman.wt_bridge import PlanUnload, parse_start_outcome, parse_start_plan

FIXTURE = Path(__file__).resolve().parents[3] / "docs" / "contracts" / "wt-start-cli.sample.json"


def _shape(key: str) -> str:
    return json.dumps(json.loads(FIXTURE.read_text())[key])


def test_plan_shapes_parse():
    # Pins `wt start <id> --plan --json`: modelman's TUI builds its confirm
    # dialog from this. If wt renames a status or a field, the dialog would
    # stop naming the models a start is about to unload.
    assert parse_start_plan(_shape("plan_running")).status == "running"
    assert parse_start_plan(_shape("plan_fits")).status == "fits"
    assert parse_start_plan(_shape("plan_unknown")).status == "unknown"
    plan = parse_start_plan(_shape("plan_would_unload"))
    assert plan.id == "omlx/B"
    assert plan.status == "would_unload"
    assert plan.would_unload == [PlanUnload(id="omlx/A", sessions=1)]


def test_result_shapes_parse():
    # Pins `wt start <id> --json`: modelman clears the running flag of every
    # id in `unloaded`. A renamed field would leave evicted models reading as
    # running forever.
    started = parse_start_outcome(_shape("started"))
    assert (started.id, started.status, started.unloaded) == ("omlx/B", "started", ["omlx/A"])
    already = parse_start_outcome(_shape("already_running"))
    assert (already.status, already.unloaded) == ("already_running", [])


@pytest.mark.parametrize(
    "bad",
    [
        "",
        "not json",
        "[]",
        '{"id": "x"}',
        '{"id": "x", "status": "nope"}',
        # An unhashable status must be a ValueError too, not a TypeError from
        # the set lookup: the callers catch only ValueError, and anything else
        # takes the TUI's start worker down.
        '{"id": "x", "status": [], "would_unload": [], "unloaded": []}',
        '{"id": "x", "status": {}, "would_unload": [], "unloaded": []}',
    ],
)
def test_unknown_shapes_are_rejected(bad):
    # An older wt, or one that failed before printing, must read as "no
    # usable answer", never as a start that succeeded or a plan that fits.
    with pytest.raises(ValueError):
        parse_start_plan(bad)
    with pytest.raises(ValueError):
        parse_start_outcome(bad)
