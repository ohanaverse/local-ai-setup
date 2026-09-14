"""Tests for the post-exit queued-ops runner: main.py applies a
QueuedOps returned by the TUI against fresh on-disk state, after the
TUI has already exited, printing provider progress and lifecycle
events straight to stdout. See
docs/superpowers/specs/2026-09-13-downloads-on-exit-design.md.
"""

from __future__ import annotations

from unittest.mock import MagicMock, patch

from typer.testing import CliRunner

from modelman.main import app, print_error_summary, print_event, run_queued_ops
from modelman.queue import QueuedOps
from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry, save_registry
from modelman.state import StateStore, load_state, save_state


def _seed(tmp_path, monkeypatch, *, models=(), providers=("ollama",)):
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    save_registry(
        Registry(
            providers=[ProviderEntry(id=p, name=p, auth=AuthConfig(type="none")) for p in providers],
            models=list(models),
        ),
        reg_path,
    )
    save_state(StateStore(), state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    return reg_path, state_path


def test_no_args_invokes_run_tui():
    # `modelman` with no subcommand is the TUI entry point; run_tui takes
    # no arguments now that the `download <family>` scroll-to-startup
    # shortcut is gone.
    with patch("modelman.main.run_tui") as run_tui:
        runner = CliRunner()
        result = runner.invoke(app, [])
        assert result.exit_code == 0
        run_tui.assert_called_once_with()


def test_download_command_is_gone():
    # The `download <family>` command has been removed now that the TUI
    # queues all changes for post-exit apply instead of scroll-to-startup.
    runner = CliRunner()
    result = runner.invoke(app, ["download", "ornith"])
    assert result.exit_code != 0


def test_run_tui_discard_or_empty_does_not_apply(tmp_path, monkeypatch):
    # A None QueuedOps (Discard, or Escape with an empty queue) must not
    # touch the registry/state at all.
    reg_path, state_path = _seed(tmp_path, monkeypatch)
    before = reg_path.read_text()
    with patch("modelman.app.ModelmanApp") as app_cls:
        app_cls.return_value.run.return_value = None
        from modelman.main import run_tui

        run_tui()
    assert reg_path.read_text() == before


def test_run_queued_ops_downloads_a_queued_ready_on(tmp_path, monkeypatch, capsys):
    """A queued ready-on against a mapped provider downloads for real —
    the behavior apply() lost when DownloadManager took it over, and
    gets back now that the TUI queues everything instead."""
    entry = ModelEntry(id="ollama/x", family="f", provider_id="ollama", model_name="x:7b")
    reg_path, state_path = _seed(tmp_path, monkeypatch, models=[entry])

    fake_provider = MagicMock()
    fake_provider.download.return_value = "ollama:x:7b"
    fake_provider.size_of.return_value = None
    with patch("modelman.main.ProviderRegistry.get", return_value=fake_provider):
        failed = run_queued_ops(QueuedOps(ready={"ollama/x": True}))

    assert failed is False
    fake_provider.download.assert_called_once()
    state = load_state(state_path)
    assert state.get("ollama/x").ready is True
    assert state.get("ollama/x").disk_path == "ollama:x:7b"
    out = capsys.readouterr().out
    assert "Downloading x:7b..." in out
    assert "done: downloaded x:7b" in out


def test_run_queued_ops_reports_failures_and_returns_true(tmp_path, monkeypatch, capsys):
    # When a queued operation fails (e.g., download error), run_queued_ops
    # must report the failure, print an error summary, and return True for exit code 1.
    entry = ModelEntry(id="ollama/x", family="f", provider_id="ollama", model_name="x:7b")
    reg_path, state_path = _seed(tmp_path, monkeypatch, models=[entry])
    fake_provider = MagicMock()
    fake_provider.download.side_effect = RuntimeError("connection refused")
    with patch("modelman.main.ProviderRegistry.get", return_value=fake_provider):
        failed = run_queued_ops(QueuedOps(ready={"ollama/x": True}))

    assert failed is True
    out = capsys.readouterr().out
    assert "Completed with errors:" in out
    assert "download ollama/x: connection refused" in out
    assert "1 of 1 operations failed." in out


def test_run_queued_ops_keyboard_interrupt_prints_cancelled_summary(tmp_path, monkeypatch, capsys):
    # Ctrl+C during a real multi-item apply must cancel cleanly and report
    # how many operations finished vs. were skipped—there's no TUI Cancel button
    # now that apply() runs after the TUI exits, so this is the only way to stop.
    entry_a = ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a:7b")
    entry_b = ModelEntry(id="ollama/b", family="f", provider_id="ollama", model_name="b:7b")
    reg_path, state_path = _seed(tmp_path, monkeypatch, models=[entry_a, entry_b])
    fake_provider = MagicMock()
    fake_provider.download.side_effect = ["ollama:a:7b", KeyboardInterrupt()]
    fake_provider.size_of.return_value = None
    with patch("modelman.main.ProviderRegistry.get", return_value=fake_provider):
        failed = run_queued_ops(QueuedOps(ready={"ollama/a": True, "ollama/b": True}))

    assert failed is True
    out = capsys.readouterr().out
    assert "Cancelled: 1 steps completed, 1 remaining skipped." in out
    # Cancel semantics: nothing saved for this run.
    state = load_state(state_path)
    assert state.get("ollama/a").ready is False


def test_print_event_formats_download_lifecycle(capsys):
    # print_event() must render queue.py's lifecycle tags as single human-readable
    # lines that replace StatusScreen's RichLog now that apply() runs in a plain terminal.
    print_event("download:start|ollama/x|x:7b")
    print_event("download:done|ollama/x|x:7b|21.7 GB")
    out = capsys.readouterr().out
    assert "Downloading x:7b..." in out
    assert "done: downloaded x:7b (21.7 GB)" in out


def test_print_error_summary_prints_nothing_on_clean_run(capsys):
    # A clean run with no failures must print nothing and return False so
    # the TUI exits cleanly with code 0, not leaving confusing output on success.
    assert print_error_summary([], total=3) is False
    assert capsys.readouterr().out == ""
