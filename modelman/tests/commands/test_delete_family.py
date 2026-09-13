"""`modelman delete-family` removes the lingering [[families]] registry entry
that queue.py's apply() leaves behind once a family's last model is deleted
or moved out ("stickiness"). This is the command wiring; the stickiness
behavior itself is covered by tests/test_queue.py."""

from typer.testing import CliRunner

from modelman.main import app
from modelman.registry import (
    AuthConfig,
    FamilyEntry,
    ModelEntry,
    ProviderEntry,
    Registry,
    load_registry,
    save_registry,
)
from modelman.state import FamilyState, StateStore, load_state, save_state


def _seed(tmp_path, monkeypatch, *, families=(), models=(), legacy_families=()):
    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    save_registry(
        Registry(
            providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))],
            families=list(families),
            models=list(models),
        ),
        registry_path,
    )
    store = StateStore()
    for name in legacy_families:
        store.families[name] = FamilyState()
    save_state(store, state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    return registry_path, state_path


def test_delete_family_removes_empty_entry(tmp_path, monkeypatch):
    # The primary case a review finding flagged: nothing removed an emptied
    # family's [[families]] entry once FamilyScreen (the old family-list
    # screen) was deleted, so it lingered in registry.toml forever.
    registry_path, _ = _seed(tmp_path, monkeypatch, families=[FamilyEntry(name="solo")])

    runner = CliRunner()
    result = runner.invoke(app, ["delete-family", "solo"])

    assert result.exit_code == 0
    assert "Deleted family 'solo'" in result.stdout
    assert load_registry(registry_path).family("solo") is None


def test_delete_family_refuses_when_family_has_models(tmp_path, monkeypatch):
    # Mirrors the old FamilyScreen behavior: a family with models must be
    # emptied (moved/deleted) before its entry can be removed.
    entry = ModelEntry(id="ollama/x", family="ornith", provider_id="ollama", model_name="x")
    registry_path, _ = _seed(
        tmp_path, monkeypatch, families=[FamilyEntry(name="ornith")], models=[entry]
    )

    runner = CliRunner()
    result = runner.invoke(app, ["delete-family", "ornith"])

    assert result.exit_code == 1
    assert "1 model" in result.output
    assert load_registry(registry_path).family("ornith") is not None


def test_delete_family_errors_when_no_such_family(tmp_path, monkeypatch):
    _seed(tmp_path, monkeypatch)

    runner = CliRunner()
    result = runner.invoke(app, ["delete-family", "missing"])

    assert result.exit_code == 1
    assert "no family entry" in result.output


def test_delete_family_drops_legacy_state_entry(tmp_path, monkeypatch):
    # A family known only through the legacy state.families table (never
    # promoted to a first-class [[families]] entry) must also be
    # deletable, and its legacy row cleared.
    _, state_path = _seed(tmp_path, monkeypatch, legacy_families=["legacy"])

    runner = CliRunner()
    result = runner.invoke(app, ["delete-family", "legacy"])

    assert result.exit_code == 0
    assert "legacy" not in load_state(state_path).families
