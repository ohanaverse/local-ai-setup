"""Shared fixtures: the guards that keep the suite off the network, off wt and
out of the real config and cache directories, plus the fixture-file helpers."""

import io
import json
import re
import subprocess
import urllib.error
import urllib.request
import zipfile
from collections.abc import Callable
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

import pytest
from fakes import FakeFetcher, Sleeps

from modeltiers import aliases as aliases_mod
from modeltiers import contracts as contracts_mod
from modeltiers import fetch, loader, output, pipeline
from modeltiers import inventory as inventory_mod
from modeltiers.aliases import Aliases
from modeltiers.contracts import Contracts
from modeltiers.loader import Sources, SourceSpecs
from modeltiers.pipeline import Assignment
from modeltiers.records import Inventory

FIXTURES = Path(__file__).parent / "fixtures"
EPOCH_MEMBER = "epoch_capabilities_index/eci_scores.csv"
T0 = datetime(2026, 10, 12, 15, 58, 0, tzinfo=UTC)
# The fixtures share three models between the intelligence index and each of
# the other two signals: the fewest an alignment is fitted on in the tests.
ALIGN_MIN = 3
# The cross-language fixtures at the repo root, beside wt's and llmbench's.
CONTRACTS_DIR = Path(__file__).parents[2] / "docs" / "contracts"
# The inventory contract: one `wt model list --json` document. wt's own tests
# hold the same file to what the command prints
# (wt/cmd/wt/model_list_contract_test.go), so a key wt renames fails there
# before it can reach a run here. Every test that needs the golden inventory
# reads it through this name.
INVENTORY = CONTRACTS_DIR / "model-list.sample.json"
# The output contract: what `run` writes for the golden fixtures.
SAMPLE = CONTRACTS_DIR / "tiers.sample.toml"
_STAMPS = re.compile(r'^(generated_at|fetched_at) = "[^"]*"$', re.MULTILINE)


def without_timestamps(text: str) -> str:
    """`tiers.toml` text with every timestamp replaced by one placeholder."""
    return _STAMPS.sub(r'\1 = "<time>"', text)


def scaled_openrouter(body: bytes, factor: float) -> bytes:
    """An OpenRouter body with every intelligence index multiplied."""
    doc = json.loads(body)
    for model in doc["data"]:
        indices = (model.get("benchmarks") or {}).get("artificial_analysis")
        if indices:
            indices["intelligence_index"] *= factor
    return json.dumps(doc).encode()


def _no_network(*args: Any, **kwargs: Any) -> Any:
    raise urllib.error.URLError("hermetic test: no network")


def _no_subprocess(*args: Any, **kwargs: Any) -> Any:
    raise AssertionError(f"hermetic test: subprocess.run called with {args!r}")


@pytest.fixture(autouse=True)
def _hermetic(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    """No test may fetch, run wt, or read or write the real output file or the
    real snapshot cache. `urllib.request` and `subprocess` are each one shared
    module object, so these two patches hold for the whole process. A test
    that needs either replaces it with its own fake."""
    monkeypatch.setattr(urllib.request, "urlopen", _no_network)
    monkeypatch.setattr(subprocess, "run", _no_subprocess)
    home = tmp_path / "home"
    home.mkdir()
    monkeypatch.setenv("HOME", str(home))
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg-config"))
    monkeypatch.setenv("XDG_CACHE_HOME", str(tmp_path / "xdg-cache"))
    monkeypatch.setenv("MODELTIERS_OUT", str(tmp_path / "out" / "tiers.toml"))
    monkeypatch.setenv("MODELTIERS_CACHE", str(tmp_path / "cache"))


@pytest.fixture
def out(tmp_path: Path) -> Path:
    """Where `_hermetic` points MODELTIERS_OUT."""
    return tmp_path / "out" / "tiers.toml"


@pytest.fixture
def read_fixture() -> Callable[[str], bytes]:
    """The bytes of one file under tests/fixtures/."""

    def read(name: str) -> bytes:
        return (FIXTURES / name).read_bytes()

    return read


@pytest.fixture
def epoch_zip(read_fixture: Callable[[str], bytes]) -> Callable[[str], bytes]:
    """A ZIP archive holding one CSV fixture as Epoch's scores file. The CSVs
    are kept as text so they can be read and diffed."""

    def build(csv_name: str = "eci_scores.csv") -> bytes:
        buffer = io.BytesIO()
        with zipfile.ZipFile(buffer, "w") as archive:
            archive.writestr(EPOCH_MEMBER, read_fixture(csv_name))
        return buffer.getvalue()

    return build


@pytest.fixture
def arena_doc(read_fixture: Callable[[str], bytes]) -> Callable[..., bytes]:
    """An Arena snapshot: the named page fixtures in one wrapper document."""

    def build(*page_names: str) -> bytes:
        names = page_names or ("arena_page1.json", "arena_page2.json")
        return fetch.wrap_arena_pages([read_fixture(name) for name in names])

    return build


@pytest.fixture
def specs() -> SourceSpecs:
    """The four sources with minimums that fit the golden fixtures."""
    return loader.build_specs(modelsdev_min=12, openrouter_min=6, arena_min=6, epoch_min=6)


@pytest.fixture
def good_answers(
    read_fixture: Callable[[str], bytes], epoch_zip: Callable[..., bytes]
) -> dict[str, list[bytes | Exception]]:
    """What a FakeFetcher needs for one clean run of all four sources: each
    URL answered once from the golden fixtures. A test edits a queue to make
    one source fail."""
    return {
        fetch.MODELSDEV_URL: [read_fixture("modelsdev.json")],
        fetch.OPENROUTER_URL: [read_fixture("openrouter.json")],
        fetch.arena_page_url(0): [read_fixture("arena_page1.json")],
        fetch.arena_page_url(3): [read_fixture("arena_page2.json")],
        fetch.EPOCH_URL: [epoch_zip()],
    }


@pytest.fixture
def rule() -> Contracts:
    """The fixture-local contracts.toml, never the committed one."""
    return contracts_mod.load_contracts(FIXTURES / "contracts.toml")


@pytest.fixture
def inventory() -> Inventory:
    """The golden inventory (the shared contract fixture), parsed."""
    return inventory_mod.parse_inventory(INVENTORY.read_text())


@pytest.fixture
def sources(
    specs: SourceSpecs, good_answers: dict[str, list[bytes | Exception]], tmp_path: Path
) -> Sources:
    """The four golden fixtures through the loader, fetched at T0."""
    return loader.load_all(
        specs,
        cache=tmp_path / "cache",
        fetcher=FakeFetcher(good_answers),
        sleep=Sleeps(),
        now=T0,
        offline=False,
        warn=lambda message: None,
    )


@pytest.fixture
def fixture_aliases() -> Aliases:
    return aliases_mod.load_aliases(FIXTURES / "aliases.toml")


@pytest.fixture
def assignment(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases, rule: Contracts
) -> Assignment:
    """The golden fixtures resolved and assigned: what `run` writes."""
    identity = pipeline.resolve_all(inventory, sources, fixture_aliases)
    return pipeline.assign_all(identity, rule, min_shared=ALIGN_MIN)


@pytest.fixture
def document(assignment: Assignment, inventory: Inventory, sources: Sources) -> dict[str, Any]:
    """`tiers.toml` for the golden fixtures, generated at T0, as a dict."""
    return output.build_document(assignment, inventory, sources, T0)
