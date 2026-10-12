"""The one test that reads the committed data files. Every other test uses
the copies under tests/fixtures/, so calibration can change the committed
values without changing a test."""

from modeltiers import aliases, contracts


def test_the_committed_aliases_file_loads_and_validates() -> None:
    loaded = aliases.load_aliases()
    assert loaded.origin == "aliases.toml"
    assert loaded.aliases or loaded.ignores
    assert all("/" in alias.canonical for alias in loaded.aliases)
    # Calibration's rule: an alias states the evidence it rests on.
    assert [alias.canonical for alias in loaded.aliases if not alias.note.strip()] == []


def test_the_committed_contracts_file_loads_and_validates() -> None:
    loaded = contracts.load_contracts()
    assert loaded.origin == "contracts.toml"
    assert loaded.version
    assert set(loaded.tiers) == set(contracts.TIERS)
    assert set(loaded.signals) == set(contracts.SIGNALS)
    assert set(loaded.attribution) == set(contracts.SOURCES)
    assert loaded.sentinels
    assert all("/" in sentinel.canonical for sentinel in loaded.sentinels)
