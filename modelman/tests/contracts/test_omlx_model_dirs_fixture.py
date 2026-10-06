"""Cross-language contract for the omlx model-directory scan (#263).

wt's scanOmlxModels asserts the same cases
(wt/internal/localmodels/omlx_model_dirs_fixture_test.go), so a one-sided
change to the discovery rule fails both CI jobs. A regression here means
modelman lists a model omlx does not serve, or hides one it does.
"""

import json
from pathlib import Path

import pytest

from modelman.providers.omlx import omlx_model_dirs

CONTRACTS = Path(__file__).resolve().parents[3] / "docs" / "contracts"
CASES = json.loads((CONTRACTS / "omlx-model-dirs.sample.json").read_text())["cases"]


@pytest.mark.parametrize("case", CASES, ids=[c["name"] for c in CASES])
def test_omlx_model_dirs_matches_shared_fixture(case, tmp_path):
    for rel in case["files"]:
        path = tmp_path / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.touch()
    names = [d.name for d in omlx_model_dirs(tmp_path / case["model_dir"])]
    assert names == case["expect"], f"case {case['name']!r}"
