"""The sample blocks in the user guide and the config map are output of this
package over the golden fixtures, word for word. Guides must not embed a real
machine's models, so a sample is either a placeholder or a slice of what
these tests can reproduce."""

import re
from collections.abc import Callable

import pytest
from conftest import ALIGN_MIN, FIXTURES, INVENTORY, SAMPLE, T0, scaled_openrouter
from fakes import FakeFetcher, Sleeps

from modeltiers import cli, fetch
from modeltiers.loader import SourceSpecs

GUIDES = SAMPLE.parent.parent / "guides"
GUIDE = GUIDES / "12-model-tiers.md"
CONFIG_MAP = GUIDES / "00-config-map.md"
RUN = ["run", "--inventory", str(INVENTORY)]
Answers = dict[str, list[bytes | Exception]]


def _blocks(text: str, language: str) -> list[str]:
    """The body of every fenced block of one language, each ending in a
    newline. A block inside a list item is indented, fence and body alike."""
    return re.findall(rf"^[ \t]*```{language}\n(.*?^)[ \t]*```$", text, re.M | re.S)


def _deps(specs: SourceSpecs, answers: Answers) -> cli.Deps:
    return cli.Deps(
        fetcher=FakeFetcher(answers),
        sleep=Sleeps(),
        now=lambda: T0,
        specs=specs,
        aliases_path=FIXTURES / "aliases.toml",
        contracts_path=FIXTURES / "contracts.toml",
        align_min=ALIGN_MIN,
    )


@pytest.fixture
def printed(
    specs: SourceSpecs,
    good_answers: Answers,
    read_fixture: Callable[[str], bytes],
    capsys: pytest.CaptureFixture[str],
) -> str:
    """Everything the fixtures make the three commands print: a run, `explain`
    of one row, and a run whose intelligence indices dropped by a fifth."""
    assert cli.main(RUN, deps=_deps(specs, dict(good_answers))) == 0
    assert cli.main(["explain", "ollama/minimax-m3:cloud"], deps=_deps(specs, {})) == 0
    lowered = scaled_openrouter(read_fixture("openrouter.json"), 0.8)
    drifted = {**good_answers, fetch.OPENROUTER_URL: [lowered]}
    assert cli.main([*RUN, "--dry-run"], deps=_deps(specs, drifted)) == 3
    return capsys.readouterr().out


def test_every_output_sample_in_the_guide_is_fixture_output(printed: str) -> None:
    samples = _blocks(GUIDE.read_text(), "text")
    # The report's head, the table, one explain, one failed sentinel table.
    assert len(samples) == 4
    for sample in samples:
        assert sample in printed


def test_the_config_map_example_is_the_committed_sample() -> None:
    section = CONFIG_MAP.read_text().split("### `~/.config/local-ai/tiers.toml`\n")[1]
    (example,) = _blocks(section.split("\n### ")[0], "toml")
    head, entry = example.split("\n\n", 1)
    sample = SAMPLE.read_text()
    assert sample.startswith(head + "\n")
    assert "\n" + entry in sample
    assert entry.startswith("[[models]]\n")


def test_no_command_in_the_guide_names_a_fixture_model() -> None:
    names = re.compile(r"glm-|deepseek|qwen|medgemma|minimax|gemma|claude-|nomic", re.I)
    for block in _blocks(GUIDE.read_text(), "bash"):
        assert not names.search(block), block
