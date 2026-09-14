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
from modelman.registry import (
    AuthConfig,
    ModelEntry,
    ProviderEntry,
    Registry,
    model_entry_to_variant,
    save_registry,
)
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
    # "a"'s completed download is now persisted even though "b" was
    # interrupted mid-download — only work that never finished (or never
    # started) is lost. See queue.py's PendingChanges._persist safety net.
    state = load_state(state_path)
    assert state.get("ollama/a").ready is True
    # The interrupted item itself must NOT be persisted as ready — only
    # fully-completed steps survive the safety net, not the in-flight one.
    assert state.get("ollama/b").ready is False


def test_run_queued_ops_handles_flag_only_provider_ready_on(tmp_path, monkeypatch, capsys):
    # A model under a flag-only provider (no Provider class, like openrouter
    # or native) queued for ready-on must not crash when apply() runs—it should
    # skip the provider instance lookup and apply the flag changes cleanly.
    entry = ModelEntry(id="openrouter/x", family="f", provider_id="openrouter", model_name="openrouter/x")
    reg_path, state_path = _seed(tmp_path, monkeypatch, models=[entry], providers=("ollama", "openrouter"))

    # Don't patch ProviderRegistry.get for openrouter—let it raise KeyError
    # to simulate a flag-only provider with no backing Provider class.
    failed = run_queued_ops(QueuedOps(ready={"openrouter/x": True}))

    assert failed is False
    state = load_state(state_path)
    assert state.get("openrouter/x").ready is True
    out = capsys.readouterr().out
    # No download message should appear (flag-only provider doesn't download)
    assert "Downloading" not in out
    assert "Marked openrouter/x ready" in out or "done:" in out


def test_run_queued_ops_missing_ready_model_id_does_not_crash_whole_run(tmp_path, monkeypatch, capsys):
    # A ready-on queued for a model id that's absent from a freshly-loaded
    # registry (e.g. deleted out-of-band, or a hand-edited registry.toml,
    # between the TUI queuing it and the post-exit runner loading fresh
    # state) must not raise an unhandled KeyError and take down the whole
    # run. It should degrade to a per-item recorded failure, exactly like
    # a missing id already does for deletes/moves/exposes, and every other
    # queued op in the same run must still complete.
    real_entry = ModelEntry(id="ollama/real", family="f", provider_id="ollama", model_name="real:7b")
    reg_path, state_path = _seed(tmp_path, monkeypatch, models=[real_entry])
    real_variant = model_entry_to_variant(real_entry)

    fake_provider = MagicMock()
    with patch("modelman.main.ProviderRegistry.get", return_value=fake_provider):
        failed = run_queued_ops(
            QueuedOps(
                ready={"ollama/missing": True},
                deletes={"ollama/real": real_variant},
            )
        )

    assert failed is True
    out = capsys.readouterr().out
    assert "ready ollama/missing: Unknown model: ollama/missing" in out
    # The queued delete for the real model still ran to completion despite
    # the bad ready id.
    fake_provider.delete.assert_called_once()
    state = load_state(state_path)
    assert "ollama/real" not in state.models


def test_run_queued_ops_missing_ready_model_id_prints_live_failure(tmp_path, monkeypatch, capsys):
    # A missing ready id must surface through the same live on_event
    # channel as every other queued-op failure type — deletes/moves/
    # exposes already do via queue.py's own emit() calls (see the moves
    # loop's identical KeyError case). Regression: the ready lookup
    # happens before PendingChanges even exists, so it was appending
    # straight to pending.failures with no live print, unlike every
    # other op type.
    reg_path, state_path = _seed(tmp_path, monkeypatch)
    failed = run_queued_ops(QueuedOps(ready={"ollama/missing": True}))

    assert failed is True
    out = capsys.readouterr().out
    assert "FAILED: ready ollama/missing: Unknown model" in out


def test_run_queued_ops_counts_cascaded_unexpose_in_total(tmp_path, monkeypatch, capsys):
    """Deleting a model that is currently exposed (with no explicit
    expose/unexpose queued through the TUI) makes queue.py's apply()
    append a cascaded unexpose to PendingChanges.exposes mid-run. The
    pre-apply `total` count must grow to include it, or the "N of M
    operations failed" summary undercounts real work — regression for a
    review finding where a failing cascade silently disappeared from the
    denominator."""
    from dataclasses import replace

    from modelman.state import locked_state

    entry = ModelEntry(id="ollama/x", family="f", provider_id="ollama", model_name="x:7b")
    reg_path, state_path = _seed(tmp_path, monkeypatch, models=[entry])
    with locked_state(state_path) as state:
        state.set("ollama/x", replace(state.get("ollama/x"), exposed=True))
    # queued.exposes is intentionally empty: the cascade must come only
    # from queue.py's delete-of-an-exposed-model logic, not a
    # user-queued unexpose.
    variant = model_entry_to_variant(entry)
    fake_provider = MagicMock()
    monkeypatch.setattr(
        "modelman.queue.apply_expose_queue",
        MagicMock(side_effect=RuntimeError("config unwritable")),
    )
    with patch("modelman.main.ProviderRegistry.get", return_value=fake_provider):
        failed = run_queued_ops(QueuedOps(deletes={"ollama/x": variant}))

    assert failed is True
    out = capsys.readouterr().out
    # 1 delete (succeeded) + 1 cascaded unexpose (failed) = 2, not 1.
    assert "1 of 2 operations failed." in out


def test_run_queued_ops_reports_provider_instantiation_error_without_crashing(
    tmp_path, monkeypatch, capsys
):
    """A provider whose constructor raises a real error (bad config, a
    broken __init__) must not crash the whole run with a raw traceback,
    and must not be silently treated as a flag-only provider either —
    that would flip ready=True without ever downloading anything.
    Regression for a review finding: the old StatusScreen caught this
    with a broad except Exception; the post-exit runner dropped it."""
    entry = ModelEntry(id="ollama/x", family="f", provider_id="ollama", model_name="x:7b")
    reg_path, state_path = _seed(tmp_path, monkeypatch, models=[entry])
    with patch("modelman.main.ProviderRegistry.get", side_effect=ValueError("bad config")):
        failed = run_queued_ops(QueuedOps(ready={"ollama/x": True}))

    assert failed is True
    out = capsys.readouterr().out
    assert "provider unavailable: bad config" in out
    state = load_state(state_path)
    # Must NOT have been silently marked ready — the provider never ran.
    assert state.get("ollama/x").ready is False


def test_run_queued_ops_reports_unexpected_apply_exception_without_crashing(
    tmp_path, monkeypatch, capsys
):
    """A genuine bug reaching all the way out of PendingChanges.apply()
    (not a per-step failure apply() already captures itself) must not
    crash the CLI with a raw traceback — apply()'s own exception safety
    net has already persisted whatever completed before the crash, and
    run_queued_ops must report the rest cleanly instead of losing it."""
    entry = ModelEntry(id="ollama/x", family="f", provider_id="ollama", model_name="x:7b")
    reg_path, state_path = _seed(tmp_path, monkeypatch, models=[entry])
    fake_provider = MagicMock()
    monkeypatch.setattr(
        "modelman.queue.find_shared_artifact_owner",
        MagicMock(side_effect=RuntimeError("registry corrupted")),
    )
    with patch("modelman.main.ProviderRegistry.get", return_value=fake_provider):
        failed = run_queued_ops(
            QueuedOps(deletes={"ollama/x": model_entry_to_variant(entry)})
        )

    assert failed is True
    out = capsys.readouterr().out
    assert "unexpected error: registry corrupted" in out


def test_run_queued_ops_builds_one_provider_instance_per_provider_id(
    tmp_path, monkeypatch, capsys
):
    """Two queued items on the same provider must construct that
    provider once, not once per item — matches the dedup-by-id pattern
    sync.py's _modeldir_providers already uses."""
    entry_a = ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a:7b")
    entry_b = ModelEntry(id="ollama/b", family="f", provider_id="ollama", model_name="b:7b")
    reg_path, state_path = _seed(tmp_path, monkeypatch, models=[entry_a, entry_b])
    fake_provider = MagicMock()
    fake_provider.download.return_value = "ollama:x"
    fake_provider.size_of.return_value = None
    with patch("modelman.main.ProviderRegistry.get", return_value=fake_provider) as get_mock:
        failed = run_queued_ops(QueuedOps(ready={"ollama/a": True, "ollama/b": True}))

    assert failed is False
    assert get_mock.call_count == 1


def test_print_event_formats_download_lifecycle(capsys):
    # print_event() must render queue.py's lifecycle tags as single human-readable
    # lines that replace StatusScreen's RichLog now that apply() runs in a plain terminal.
    print_event("download:start|ollama/x|x:7b")
    print_event("download:done|ollama/x|x:7b|21.7 GB")
    out = capsys.readouterr().out
    assert "Downloading x:7b..." in out
    assert "done: downloaded x:7b (21.7 GB)" in out


def test_print_event_warns_on_unrecognized_tag(capsys):
    # A future or misspelled event verb must not be silently dropped —
    # regression for a review finding where the if/elif chain had no
    # trailing else, so a new lifecycle tag would print nothing at all
    # and no test or runtime check would catch the gap.
    print_event("mystery:verb|x|y")
    err = capsys.readouterr().err
    assert "mystery:verb" in err


def test_print_event_stays_silent_for_known_no_op_tags(capsys):
    # apply:done / apply:cancelled / save:start are intentionally silent
    # (run_queued_ops prints its own summary for these) and must not be
    # flagged as unrecognized by the new catch-all branch.
    print_event("apply:done")
    print_event("apply:cancelled")
    print_event("save:start")
    out = capsys.readouterr()
    assert out.out == ""
    assert out.err == ""


def test_print_error_summary_prints_nothing_on_clean_run(capsys):
    # A clean run with no failures must print nothing and return False so
    # the TUI exits cleanly with code 0, not leaving confusing output on success.
    assert print_error_summary([], total=3) is False
    assert capsys.readouterr().out == ""
