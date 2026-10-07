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
            providers=[
                ProviderEntry(id=p, name=p, auth=AuthConfig(type="none")) for p in providers
            ],
            models=list(models),
        ),
        reg_path,
    )
    save_state(StateStore(), state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    return reg_path, state_path


def test_no_args_does_not_open_the_tui_and_says_where_to_go():
    # The TUI is disabled now that wt writes registry.toml: bare `modelman`
    # opens nothing, names wt, and exits non-zero.
    with patch("modelman.main.run_tui") as run_tui:
        runner = CliRunner()
        result = runner.invoke(app, [])
        assert result.exit_code == 1
        run_tui.assert_not_called()
    assert "TUI is disabled" in result.output
    assert "wt model init" in result.output
    assert "wt litellm sync" in result.output
    assert "modelman --help" in result.output
    # wt downloads nothing, so the notice names each provider's own tool.
    assert "ollama pull" in result.output
    assert "hf download" in result.output
    assert "mtplx pull" in result.output
    assert "wt start" in result.output
    # wt has no backend for an mlx_lm_server pairing; modelman still starts it.
    assert "modelman start" in result.output


def test_a_subcommand_still_runs_with_the_tui_disabled():
    # Only the bare invocation is refused; the callback lets subcommands through.
    runner = CliRunner()
    result = runner.invoke(app, ["litellm", "--help"])
    assert result.exit_code == 0
    assert "TUI is disabled" not in result.output


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


def test_run_queued_ops_reports_an_unreadable_registry_and_fails(tmp_path, monkeypatch, capsys):
    """The queued apply reloads the registry after the TUI has closed, so it
    is reachable with a registry that went unreadable while the user was in
    the TUI. That is a failure like any other operation failing — report it
    and exit non-zero, rather than ending the session in a traceback."""
    reg_path, state_path = _seed(tmp_path, monkeypatch)
    reg_path.write_text("models = 3\n")

    failed = run_queued_ops(QueuedOps(ready={"ollama/x": True}))

    assert failed is True
    err = capsys.readouterr().err
    assert f"error: cannot read {reg_path}" in err
    assert reg_path.read_text() == "models = 3\n"


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
    entry = ModelEntry(
        id="openrouter/x", family="f", provider_id="openrouter", model_name="openrouter/x"
    )
    reg_path, state_path = _seed(
        tmp_path, monkeypatch, models=[entry], providers=("ollama", "openrouter")
    )

    # Don't patch ProviderRegistry.get_class for openrouter—get_class
    # returns None (no backing Provider class), which short-circuits before
    # .get() is ever called, simulating a flag-only provider.
    failed = run_queued_ops(QueuedOps(ready={"openrouter/x": True}))

    assert failed is False
    state = load_state(state_path)
    assert state.get("openrouter/x").ready is True
    out = capsys.readouterr().out
    # No download message should appear (flag-only provider doesn't download)
    assert "Downloading" not in out
    assert "Marked openrouter/x ready" in out or "done:" in out


def test_run_queued_ops_missing_ready_model_id_does_not_crash_whole_run(
    tmp_path, monkeypatch, capsys
):
    # A ready-on queued for a model id that's absent from a freshly-loaded
    # registry (e.g. deleted out-of-band, or a hand-edited registry.toml,
    # between the TUI queuing it and the post-exit runner loading fresh
    # state) must not raise an unhandled KeyError and take down the whole
    # run. It should degrade to a per-item recorded failure, exactly like
    # a missing id already does for deletes/moves/exposes, and every other
    # queued op in the same run must still complete.
    real_entry = ModelEntry(
        id="ollama/real", family="f", provider_id="ollama", model_name="real:7b"
    )
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


def test_run_queued_ops_syncs_routes_once(tmp_path, monkeypatch, wt_calls):
    """#179: one `wt litellm sync` after an applied queue — no per-model
    expose/unexpose calls, whatever the queue held."""
    entry = ModelEntry(id="ollama/x", family="f", provider_id="ollama", model_name="x:7b")
    _seed(tmp_path, monkeypatch, models=[entry])
    with patch("modelman.main.ProviderRegistry.get", return_value=MagicMock()):
        failed = run_queued_ops(QueuedOps(deletes={"ollama/x": model_entry_to_variant(entry)}))
    assert failed is False
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]]
    assert not [c for c in wt_calls if c[:1] in (["expose"], ["unexpose"])]


def test_run_queued_ops_saves_registry_before_syncing(tmp_path, monkeypatch):
    """wt reads registry.toml itself, so the sync must run only after the
    queue's registry change is on disk: the deleted model is already gone
    when the sync is called."""
    from modelman.registry import load_registry

    entry = ModelEntry(id="ollama/x", family="f", provider_id="ollama", model_name="x:7b")
    reg_path, _ = _seed(tmp_path, monkeypatch, models=[entry])
    seen: list[list[str]] = []

    def recording_sync(**kwargs):
        seen.append([m.id for m in load_registry(reg_path).models])
        return []

    monkeypatch.setattr("modelman.main.sync_routes", recording_sync)
    with patch("modelman.main.ProviderRegistry.get", return_value=MagicMock()):
        failed = run_queued_ops(QueuedOps(deletes={"ollama/x": model_entry_to_variant(entry)}))
    assert failed is False
    assert seen == [[]]


def test_run_queued_ops_syncs_once_after_unexpected_apply_exception(
    tmp_path, monkeypatch, wt_calls
):
    """apply()'s safety net has persisted completed work before an
    unexpected exception escapes, so the routes must still be brought in
    line with it — exactly one sync."""
    entry = ModelEntry(id="ollama/x", family="f", provider_id="ollama", model_name="x:7b")
    _seed(tmp_path, monkeypatch, models=[entry])
    monkeypatch.setattr(
        "modelman.queue.find_shared_artifact_owner",
        MagicMock(side_effect=RuntimeError("registry corrupted")),
    )
    with patch("modelman.main.ProviderRegistry.get", return_value=MagicMock()):
        failed = run_queued_ops(QueuedOps(deletes={"ollama/x": model_entry_to_variant(entry)}))
    assert failed is True
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]]


def test_run_queued_ops_keyboard_interrupt_does_not_sync(tmp_path, monkeypatch, wt_calls):
    """Ctrl+C is a request to stop: no sync (the next one converges)."""
    entry = ModelEntry(id="ollama/x", family="f", provider_id="ollama", model_name="x:7b")
    _seed(tmp_path, monkeypatch, models=[entry])
    fake_provider = MagicMock()
    fake_provider.download.side_effect = KeyboardInterrupt()
    with patch("modelman.main.ProviderRegistry.get", return_value=fake_provider):
        failed = run_queued_ops(QueuedOps(ready={"ollama/x": True}))
    assert failed is True
    assert not [c for c in wt_calls if c[:1] == ["sync"]]


def test_run_tui_syncs_when_registry_changed_without_queue(tmp_path, monkeypatch, wt_calls):
    """An add/edit in the TUI writes registry.toml immediately and queues
    nothing; the exit must still sync so the new cloud model gets its route."""
    reg = tmp_path / "registry.toml"
    reg.write_text("models = []\n")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg))

    class FakeApp:
        def run(self):
            reg.write_text("models = []\n# edited\n")
            return None

    monkeypatch.setattr("modelman.app.ModelmanApp", FakeApp)
    from modelman.main import run_tui

    run_tui()
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]]


def test_run_tui_exits_nonzero_when_the_app_refused_the_registry(
    tmp_path, monkeypatch, wt_calls, capsys
):
    """#240: the app stops on a registry it cannot read; the command must
    carry that out as a failure with the reason on stderr, and sync nothing."""
    import pytest
    import typer

    reg = tmp_path / "registry.toml"
    reg.write_text("models = 3\n")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg))

    class FakeApp:
        return_code = 1
        registry_error = "cannot read registry.toml: boom"

        def run(self):
            return None

    monkeypatch.setattr("modelman.app.ModelmanApp", FakeApp)
    from modelman.main import run_tui

    with pytest.raises(typer.Exit) as excinfo:
        run_tui()
    assert excinfo.value.exit_code == 1
    assert "error: cannot read registry.toml: boom" in capsys.readouterr().err
    assert wt_calls == []


def test_run_tui_exits_nonzero_when_the_app_crashed(tmp_path, monkeypatch, wt_calls):
    """A crashed app returns None like a Discard does, with Textual's return
    code set. The command reported that as success. A registry the session
    had already written still gets its route sync first."""
    import pytest
    import typer

    reg = tmp_path / "registry.toml"
    reg.write_text("models = []\n")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg))

    class FakeApp:
        return_code = 1

        def run(self):
            reg.write_text("models = []\n# edited\n")
            return None

    monkeypatch.setattr("modelman.app.ModelmanApp", FakeApp)
    from modelman.main import run_tui

    with pytest.raises(typer.Exit) as excinfo:
        run_tui()
    assert excinfo.value.exit_code == 1
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]]


def test_run_tui_registry_edit_plus_queue_syncs_once(tmp_path, monkeypatch, wt_calls):
    """An add/edit AND an applied queue in one session: run_queued_ops'
    sync covers both — no second sync from the registry-changed branch."""
    entry = ModelEntry(id="ollama/x", family="f", provider_id="ollama", model_name="x:7b")
    reg_path, _ = _seed(tmp_path, monkeypatch, models=[entry])

    class FakeApp:
        def run(self):
            reg_path.write_text(reg_path.read_text() + "\n# edited\n")
            return QueuedOps(deletes={"ollama/x": model_entry_to_variant(entry)})

    monkeypatch.setattr("modelman.app.ModelmanApp", FakeApp)
    from modelman.main import run_tui

    with patch("modelman.main.ProviderRegistry.get", return_value=MagicMock()):
        run_tui()
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]]


def test_run_tui_no_change_no_sync(tmp_path, monkeypatch, wt_calls):
    reg = tmp_path / "registry.toml"
    reg.write_text("models = []\n")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg))

    class FakeApp:
        def run(self):
            return None

    monkeypatch.setattr("modelman.app.ModelmanApp", FakeApp)
    from modelman.main import run_tui

    run_tui()
    assert not [c for c in wt_calls if c[:1] == ["sync"]]


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
        failed = run_queued_ops(QueuedOps(deletes={"ollama/x": model_entry_to_variant(entry)}))

    assert failed is True
    out = capsys.readouterr().out
    assert "unexpected error: registry corrupted" in out


def test_run_queued_ops_builds_one_provider_instance_per_provider_id(tmp_path, monkeypatch, capsys):
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


def test_run_queued_ops_provider_constructor_keyerror_is_not_flag_only(
    tmp_path, monkeypatch, capsys
):
    """A provider WITH a registered class whose constructor raises
    KeyError (e.g. a required config key is missing) must be treated as
    a real instantiation failure, not silently folded into the
    flag-only path the way an unmapped provider (like openrouter) is —
    that would flip ready=True without ever downloading anything.
    Regression for a review finding: a blanket `except KeyError`
    around the whole ProviderRegistry.get() call can't tell these two
    cases apart; ProviderRegistry.get_class(provider_id) is None can."""
    entry = ModelEntry(id="ollama/x", family="f", provider_id="ollama", model_name="x:7b")
    reg_path, state_path = _seed(tmp_path, monkeypatch, models=[entry])
    with (
        patch(
            "modelman.main.ProviderRegistry.get_class",
            return_value=object,  # any non-None value: "a class IS registered"
        ),
        patch(
            "modelman.main.ProviderRegistry.get",
            side_effect=KeyError("required_field"),
        ),
    ):
        failed = run_queued_ops(QueuedOps(ready={"ollama/x": True}))

    assert failed is True
    out = capsys.readouterr().out
    assert "provider unavailable:" in out
    state = load_state(state_path)
    assert state.get("ollama/x").ready is False


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


def test_print_event_recognizes_every_tag_queue_emits(capsys):
    """queue.py's module docstring and its emit() call sites are the
    source of truth for the lifecycle-tag vocabulary; print_event's
    dispatch and run_queued_ops's done_verbs set both hard-code that same
    vocabulary independently, with nothing enforcing agreement. This
    enumerates every verb queue.py actually emits — via two patterns,
    since queue.py builds tags two different ways: a literal passed
    straight to emit(), and a literal built as _finish_ready's `done_tag=`
    keyword argument (emit() itself just does `emit(done_tag)` on a
    variable) — and asserts print_event handles each without falling
    into the "(unhandled event: ...)" catch-all. A future verb added to
    queue.py but missed here, or in print_event, now fails a test instead
    of only a runtime stderr line."""
    import re
    from pathlib import Path

    import modelman.queue as queue_module

    source = Path(queue_module.__file__).read_text()
    verbs = set(re.findall(r'emit\(f?"([a-z]+:[a-z]+)', source))
    verbs |= set(re.findall(r'done_tag=f"([a-z]+:[a-z]+)', source))
    # The exact vocabulary today (#179 removed the expose/unexpose verbs).
    # A mismatch means queue.py gained/lost a verb, or its emit() call
    # sites moved behind a helper these regexes can no longer see.
    assert verbs == {
        "apply:cancelled",
        "apply:done",
        "delete:done",
        "delete:fail",
        "delete:start",
        "download:cancelled",
        "download:done",
        "download:fail",
        "download:start",
        "move:done",
        "move:fail",
        "move:start",
        "ready:done",
        "ready:start",
        "save:done",
        "save:fail",
        "save:start",
    }, sorted(verbs)

    for verb in sorted(verbs):
        print_event(f"{verb}|a|b|c")
    err = capsys.readouterr().err
    assert "unhandled event" not in err, err


def test_run_tui_syncs_after_a_discard_that_reverted_the_registry(tmp_path, monkeypatch, wt_calls):
    """#194 (#17): add a model, start it, Discard. The start routed the model
    under its new registry id; Discard restores registry.toml byte for byte,
    so the before/after comparison sees no change and the route for an id the
    registry no longer has stayed until some later sync. The app says a sync
    is owed and the exit runs it — once."""
    reg = tmp_path / "registry.toml"
    reg.write_text("models = []\n")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg))

    class FakeApp:
        route_sync_owed = True

        def run(self):
            return None

    monkeypatch.setattr("modelman.app.ModelmanApp", FakeApp)
    from modelman.main import run_tui

    run_tui()
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]]


def test_run_tui_changed_registry_and_owed_sync_is_still_one_sync(tmp_path, monkeypatch, wt_calls):
    reg = tmp_path / "registry.toml"
    reg.write_text("models = []\n")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg))

    class FakeApp:
        route_sync_owed = True

        def run(self):
            reg.write_text("models = []\n# edited\n")
            return None

    monkeypatch.setattr("modelman.app.ModelmanApp", FakeApp)
    from modelman.main import run_tui

    run_tui()
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]]
