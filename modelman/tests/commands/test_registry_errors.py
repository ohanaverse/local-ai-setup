"""Every CLI command that reads registry.toml reports one it cannot read the
way the TUI does (#240): name the file and why, change nothing. A bare
load_registry() ends the command in a Python traceback, which tells the user
neither which file to fix nor that nothing was changed."""

import pytest
from typer.testing import CliRunner

from modelman.main import app


@pytest.fixture
def unreadable_registry(tmp_path, monkeypatch, wt_calls):
    """A registry that is there and cannot be read: valid TOML of the wrong
    shape, which fails in the parser with a TypeError rather than a
    RegistryError — the #240 case, not the missing-file one."""
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text("models = 3\n")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))
    return registry_path, wt_calls


@pytest.mark.parametrize(
    "argv",
    [["sync"], ["refresh-prices"], ["delete-family", "qwen3"], ["start"]],
    ids=["sync", "refresh-prices", "delete-family", "start"],
)
def test_commands_report_an_unreadable_registry_instead_of_a_traceback(unreadable_registry, argv):
    registry_path, wt_calls = unreadable_registry

    result = CliRunner().invoke(app, argv)

    assert result.exit_code == 1, result.output
    assert f"error: cannot read {registry_path}" in result.output
    assert result.exception is None or isinstance(result.exception, SystemExit)
    # Nothing was done on the way out: no route sync, and the file the user
    # still has to repair is untouched.
    assert wt_calls == []
    assert registry_path.read_text() == "models = 3\n"


def test_a_dangling_registry_symlink_is_reported_by_commands_too(tmp_path, monkeypatch, wt_calls):
    """The #248 path, through the CLI: the report names the link (and its
    target, in the message the load error carries)."""
    link = tmp_path / "registry.toml"
    target = tmp_path / "moved-away" / "registry.toml"
    link.symlink_to(target)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(link))
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))

    result = CliRunner().invoke(app, ["sync"])

    assert result.exit_code == 1
    assert f"error: cannot read {link}" in result.output
    assert str(target) in result.output
    assert link.is_symlink()
    assert wt_calls == []


def test_a_save_refused_for_a_link_that_went_dangling_is_reported_too(
    tmp_path, monkeypatch, wt_calls
):
    """#248's write-side refusal, through the CLI: the registry was readable
    when the command loaded it and its link dangles by the time it saves (the
    volume unmounted in between). The save raises a RegistryError, not an
    OSError, so a handler written for a failed write let it through as a
    traceback — the one thing these commands were changed to stop doing."""
    import modelman.main as main

    target = tmp_path / "volume" / "registry.toml"
    target.parent.mkdir()
    target.write_text('[[families]]\nname = "qwen3"\n')
    link = tmp_path / "registry.toml"
    link.symlink_to(target)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(link))
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))
    real_load = main.load_registry

    def load_then_unmount():
        registry = real_load()
        target.unlink()
        return registry

    monkeypatch.setattr(main, "load_registry", load_then_unmount)

    result = CliRunner().invoke(app, ["delete-family", "qwen3"])

    assert result.exit_code == 1, result.output
    assert "error: failed to save registry" in result.output
    assert str(target) in result.output
    assert result.exception is None or isinstance(result.exception, SystemExit)
    assert link.is_symlink() and not target.exists()
    assert wt_calls == []


@pytest.mark.parametrize(
    "argv",
    [["sync"], ["refresh-prices"], ["delete-family", "qwen3"], ["start"], ["start", "qwen3"]],
    ids=["sync", "refresh-prices", "delete-family", "start", "start-id"],
)
def test_commands_read_a_missing_registry_as_empty(tmp_path, monkeypatch, wt_calls, argv):
    """No registry.toml yet is not the unreadable case: there is nothing to
    protect, so a command runs against an empty registry — as the TUI and
    migrate do — and ends in its own answer, not in a RegistryNotFoundError
    traceback."""
    monkeypatch.setenv("MODELMAN_REGISTRY", str(tmp_path / "registry.toml"))
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))

    result = CliRunner().invoke(app, argv)

    assert result.exception is None or isinstance(result.exception, SystemExit), result.output
    assert "cannot read" not in result.output
