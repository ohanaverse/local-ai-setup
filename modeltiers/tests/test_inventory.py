import json
import subprocess
from pathlib import Path
from typing import Any

import pytest

from modeltiers import inventory
from modeltiers.errors import InventoryError
from modeltiers.records import InventoryRow

FIXTURE = Path(__file__).parent / "fixtures" / "inventory.json"


def _never_run_wt() -> str:
    raise AssertionError("wt must not be run when --inventory is given")


def test_reads_registry_providers_and_rows() -> None:
    inv = inventory.parse_inventory(FIXTURE.read_text())
    assert inv.registry == "/home/owner/.config/local-ai/registry.toml"
    assert inv.providers == {"mtplx": "partial", "ollama": "ok", "omlx": "ok"}
    assert inv.rows[0] == InventoryRow("ollama", "glm-5.3:cloud", "cloud", True)
    assert [(r.provider_id, r.model_name) for r in inv.rows] == [
        ("ollama", "glm-5.3:cloud"),
        ("ollama", "deepseek-v4-pro:cloud"),
        ("openrouter", "z-ai/glm-5.3"),
        ("ollama", "qwen3.8:27b-mlx"),
        ("ollama", "medgemma:27b"),
        ("ollama", "gemma4:31b-mlx"),
    ]


def test_native_rows_are_dropped_and_counted() -> None:
    inv = inventory.parse_inventory(FIXTURE.read_text())
    assert inv.native_dropped == 1
    assert all(r.model_name != "native" for r in inv.rows)


def test_discovered_and_missing_rows_are_kept() -> None:
    inv = inventory.parse_inventory(FIXTURE.read_text())
    by_name = {r.model_name: r for r in inv.rows}
    assert by_name["gemma4:31b-mlx"].registered is False  # discovered
    assert by_name["medgemma:27b"].location == "local"  # status "missing" in the fixture


def test_a_provider_that_is_not_ok_is_named_in_a_warning() -> None:
    inv = inventory.parse_inventory(FIXTURE.read_text())
    warnings = inventory.provider_warnings(inv)
    assert len(warnings) == 1
    assert "mtplx" in warnings[0]
    assert "partial" in warnings[0]


@pytest.mark.parametrize(
    ("text", "message"),
    [
        ("not json", "not JSON"),
        ("[]", "expected an object"),
        ('{"registry": "/r", "providers": {}}', "models is missing"),
        ('{"models": [], "providers": {}}', "registry is missing"),
        ('{"models": [], "registry": "/r"}', "providers is missing"),
        ('{"models": [], "registry": "/r", "providers": {"ollama": 1}}', "providers is missing"),
        ('{"models": [7], "registry": "/r", "providers": {}}', r"models\[0\] is not an object"),
        (
            '{"models": [{"model_name": "m", "location": "cloud", "registered": true}],'
            ' "registry": "/r", "providers": {}}',
            r"models\[0\]\.provider_id is missing",
        ),
        (
            '{"models": [{"provider_id": "p", "model_name": "m", "location": "cloud",'
            ' "registered": "yes"}], "registry": "/r", "providers": {}}',
            r"models\[0\]\.registered is missing or not a bool",
        ),
    ],
)
def test_a_document_in_another_shape_is_an_inventory_error(text: str, message: str) -> None:
    with pytest.raises(InventoryError, match=message):
        inventory.parse_inventory(text)


def test_inventory_file_is_read_and_wt_is_not_run() -> None:
    inv = inventory.load_inventory(path=FIXTURE, run_wt=_never_run_wt)
    assert len(inv.rows) == 6


def test_a_missing_inventory_file_is_an_inventory_error(tmp_path: Path) -> None:
    with pytest.raises(InventoryError, match="cannot read"):
        inventory.load_inventory(path=tmp_path / "absent.json", run_wt=_never_run_wt)


def test_without_a_file_the_runner_is_called() -> None:
    inv = inventory.load_inventory(path=None, run_wt=FIXTURE.read_text)
    assert len(inv.rows) == 6


class FakeRun:
    """Stands in for subprocess.run: records each argv, then returns or raises."""

    def __init__(self, result: Any) -> None:
        self.result = result
        self.calls: list[list[str]] = []

    def __call__(self, argv: list[str], **kwargs: Any) -> Any:
        self.calls.append(argv)
        if isinstance(self.result, BaseException):
            raise self.result
        return self.result


def test_run_wt_runs_exactly_model_list_json(monkeypatch: pytest.MonkeyPatch) -> None:
    done = subprocess.CompletedProcess(["wt"], 0, stdout=FIXTURE.read_text(), stderr="")
    fake = FakeRun(done)
    monkeypatch.setattr(subprocess, "run", fake)
    assert json.loads(inventory.run_wt())["registry"].endswith("registry.toml")
    assert fake.calls == [["wt", "model", "list", "--json"]]


def test_wt_not_on_path_is_an_inventory_error(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(subprocess, "run", FakeRun(FileNotFoundError("wt")))
    with pytest.raises(InventoryError, match="wt is not on PATH"):
        inventory.run_wt()


def test_wt_failing_is_an_inventory_error_with_its_stderr(monkeypatch: pytest.MonkeyPatch) -> None:
    done = subprocess.CompletedProcess(["wt"], 1, stdout="", stderr="registry.toml: bad\n")
    monkeypatch.setattr(subprocess, "run", FakeRun(done))
    with pytest.raises(InventoryError, match="exited 1: registry.toml: bad"):
        inventory.run_wt()


def test_wt_hanging_is_an_inventory_error(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(subprocess, "run", FakeRun(subprocess.TimeoutExpired("wt", 60)))
    with pytest.raises(InventoryError, match="gave no answer"):
        inventory.run_wt()
