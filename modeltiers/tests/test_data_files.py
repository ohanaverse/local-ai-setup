"""The one test that reads the committed data files. Every other test uses
the copies under tests/fixtures/, so calibration can change the committed
values without changing a test."""

from modeltiers import aliases


def test_the_committed_aliases_file_loads_and_validates() -> None:
    loaded = aliases.load_aliases()
    assert loaded.origin == "aliases.toml"
    assert loaded.aliases or loaded.ignores
    assert all("/" in alias.canonical for alias in loaded.aliases)
