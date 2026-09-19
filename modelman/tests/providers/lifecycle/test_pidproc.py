import os
from unittest.mock import MagicMock, mock_open, patch

import pytest

from modelman.providers.lifecycle.envelope import LifecycleError
from modelman.providers.lifecycle.pidproc import PidfileProcess


def _proc(name="mtplx", pidfile="/tmp/x.pid", logfile="/tmp/x.log"):
    return PidfileProcess(name=name, pidfile=pidfile, logfile=logfile)


def test_spawn_writes_pidfile_and_returns_live_popen():
    """A successful spawn must write the child's pid to the pidfile and
    return the live Popen handle so the caller can watch for process
    death during a model-load poll."""
    proc = MagicMock(pid=1234, poll=MagicMock(return_value=None))
    m = mock_open()
    with (
        patch(
            "modelman.providers.lifecycle.pidproc.subprocess.Popen", return_value=proc
        ) as mock_popen,
        patch("modelman.providers.lifecycle.pidproc.time.sleep"),
        patch("builtins.open", m),
    ):
        result = _proc().spawn(["mtplx", "serve"])
    assert result is proc
    mock_popen.assert_called_once()
    assert mock_popen.call_args.args[0] == ["mtplx", "serve"]
    # The pidfile write is the second `open()` call (first is the logfile).
    handle = m()
    handle.write.assert_any_call("1234")


def test_spawn_raises_with_log_tail_when_process_exits_immediately():
    """A bad CLI flag or missing weights can make the child exit before
    spawn() returns; this must raise LifecycleError including a log tail
    instead of returning a Popen that's already dead."""
    proc = MagicMock()
    proc.pid = 1234
    proc.poll.return_value = 1
    proc.returncode = 1
    # mock_open() doesn't simulate real file position tracking — .tell()
    # must be configured explicitly so the log-tail read (f.seek(0, 2);
    # size = f.tell(); f.seek(...); f.read().decode(...)) behaves like a
    # real (here, non-empty) file instead of returning a bare MagicMock
    # from .tell() that can't be compared against an int.
    log_mock = mock_open(read_data=b"boom: out of memory")
    log_mock.return_value.tell.return_value = 20
    with (
        patch("modelman.providers.lifecycle.pidproc.subprocess.Popen", return_value=proc),
        patch("modelman.providers.lifecycle.pidproc.time.sleep"),
        patch("builtins.open", log_mock),
        pytest.raises(LifecycleError, match="exited immediately"),
    ):
        _proc().spawn(["mtplx", "serve"])


def test_stop_tolerates_missing_pidfile():
    """A stop() call when nothing was ever started (no pidfile) must be a
    silent no-op, not raise."""
    with patch("builtins.open", side_effect=FileNotFoundError()):
        _proc().stop()  # must not raise


def test_stop_tolerates_unreadable_pid_content():
    """A pidfile with unparseable content (empty or corrupt) must be
    removed and treated as nothing-to-do, tolerant like bash."""
    m = mock_open(read_data="not-a-pid")
    with (
        patch("builtins.open", m),
        patch("modelman.providers.lifecycle.pidproc.os.unlink") as mock_unlink,
    ):
        _proc().stop()
    mock_unlink.assert_called_once_with("/tmp/x.pid")


def _ps_matching(name="mtplx"):
    """A subprocess.run mock standing in for `ps -p <pid> -o command=`
    reporting a command that identity-matches PidfileProcess(name=name)."""
    return MagicMock(return_value=MagicMock(returncode=0, stdout=f"/usr/local/bin/{name} serve\n"))


def test_stop_sends_sigterm_when_process_alive():
    """A live process that still identity-matches the pidfile's name must
    be sent SIGTERM (never SIGKILL) — this matches bash's
    mlx_lm_server_stop exactly: fire-and-forget, no escalation."""
    m = mock_open(read_data="1234")
    with (
        patch("builtins.open", m),
        patch("modelman.providers.lifecycle.pidproc.os.kill") as mock_kill,
        patch("modelman.providers.lifecycle.pidproc.os.unlink") as mock_unlink,
        patch("modelman.providers.lifecycle.pidproc.subprocess.run", _ps_matching()),
    ):
        _proc().stop()
    # First call is the liveness check (signal 0), second is the real SIGTERM.
    assert mock_kill.call_args_list[0].args == (1234, 0)
    import signal

    assert mock_kill.call_args_list[1].args == (1234, signal.SIGTERM)
    mock_unlink.assert_called_once_with("/tmp/x.pid")


def test_stop_reaps_child_process_after_sigterm():
    """A process this same Python process spawned (e.g. the TUI replacing
    an occupant mid-session) must be waitpid-reaped after SIGTERM, not left
    as a zombie pid-table entry for the rest of this process's lifetime —
    the bug a prior review caught: stop() signaled by raw pid but never
    called waitpid/proc.wait() on the child it (or a sibling call) spawned."""
    m = mock_open(read_data="1234")
    with (
        patch("builtins.open", m),
        patch("modelman.providers.lifecycle.pidproc.os.kill") as mock_kill,
        patch(
            "modelman.providers.lifecycle.pidproc.os.waitpid", return_value=(1234, 0)
        ) as mock_waitpid,
        patch("modelman.providers.lifecycle.pidproc.os.unlink") as mock_unlink,
        patch("modelman.providers.lifecycle.pidproc.subprocess.run", _ps_matching()),
    ):
        _proc().stop()
    mock_waitpid.assert_called_once_with(1234, os.WNOHANG)
    mock_kill.assert_called()  # SIGTERM still sent before the reap attempt
    mock_unlink.assert_called_once_with("/tmp/x.pid")


def test_stop_tolerates_reaping_a_process_it_did_not_spawn():
    """When stop() runs in a fresh process that never spawned the pid it
    read from the pidfile (the common case: a separate `modelman provider
    stop` CLI invocation), `os.waitpid` raises ChildProcessError — this
    must be swallowed as a quick no-op, not propagate or hang."""
    m = mock_open(read_data="1234")
    with (
        patch("builtins.open", m),
        patch("modelman.providers.lifecycle.pidproc.os.kill") as mock_kill,
        patch(
            "modelman.providers.lifecycle.pidproc.os.waitpid",
            side_effect=ChildProcessError(),
        ),
        patch("modelman.providers.lifecycle.pidproc.os.unlink") as mock_unlink,
        patch("modelman.providers.lifecycle.pidproc.subprocess.run", _ps_matching()),
    ):
        _proc().stop()  # must not raise
    import signal

    assert mock_kill.call_args_list[1].args == (1234, signal.SIGTERM)
    mock_unlink.assert_called_once_with("/tmp/x.pid")


def test_stop_tolerates_stale_dead_pid():
    """A stale pidfile pointing at an already-dead pid must not raise —
    the liveness check (os.kill(pid, 0)) raising ProcessLookupError means
    there's nothing left to signal, and the pidfile must still be
    removed."""
    m = mock_open(read_data="1234")
    with (
        patch("builtins.open", m),
        patch(
            "modelman.providers.lifecycle.pidproc.os.kill",
            side_effect=ProcessLookupError(),
        ),
        patch("modelman.providers.lifecycle.pidproc.os.unlink") as mock_unlink,
    ):
        _proc().stop()  # must not raise
    mock_unlink.assert_called_once_with("/tmp/x.pid")


def test_stop_always_removes_pidfile_even_on_permission_error():
    """A PermissionError on the signal call (e.g. pid reused by another
    user's process) must be swallowed and the pidfile still removed —
    there's nothing more this process can safely do about it."""
    m = mock_open(read_data="1234")
    with (
        patch("builtins.open", m),
        patch(
            "modelman.providers.lifecycle.pidproc.os.kill",
            side_effect=[None, PermissionError()],
        ),
        patch("modelman.providers.lifecycle.pidproc.os.unlink") as mock_unlink,
        patch("modelman.providers.lifecycle.pidproc.subprocess.run", _ps_matching()),
    ):
        _proc().stop()  # must not raise
    mock_unlink.assert_called_once_with("/tmp/x.pid")


@pytest.mark.parametrize("content", ["0", "-1", "-1234"])
def test_stop_treats_non_positive_pid_as_corrupt(content):
    """A pidfile holding "0" or a negative number must never reach
    os.kill: signal 0 (this process's own process group) and negative pids
    (broadcast to every process this user can signal) are both far more
    dangerous than the ValueError case right above, which this mirrors —
    remove the pidfile and treat it as nothing-to-do."""
    m = mock_open(read_data=content)
    with (
        patch("builtins.open", m),
        patch("modelman.providers.lifecycle.pidproc.os.kill") as mock_kill,
        patch("modelman.providers.lifecycle.pidproc.os.unlink") as mock_unlink,
    ):
        _proc().stop()
    mock_kill.assert_not_called()
    mock_unlink.assert_called_once_with("/tmp/x.pid")


def test_stop_skips_signal_when_pid_identity_mismatches():
    """If the OS has reassigned the pidfile's pid to an unrelated process
    (this process died and the pid was recycled), the liveness check alone
    can't tell — `ps` reporting a command with no relation to this
    PidfileProcess's name must suppress the SIGTERM rather than signal
    whatever now holds that pid."""
    m = mock_open(read_data="1234")
    ps_mismatch = MagicMock(
        return_value=MagicMock(
            returncode=0, stdout="/Applications/Safari.app/Contents/MacOS/Safari\n"
        )
    )
    with (
        patch("builtins.open", m),
        patch("modelman.providers.lifecycle.pidproc.os.kill") as mock_kill,
        patch("modelman.providers.lifecycle.pidproc.os.unlink") as mock_unlink,
        patch("modelman.providers.lifecycle.pidproc.subprocess.run", ps_mismatch),
    ):
        _proc().stop()
    # Only the liveness check (signal 0) fires; no SIGTERM to the wrong process.
    assert mock_kill.call_args_list == [((1234, 0),)]
    mock_unlink.assert_called_once_with("/tmp/x.pid")


def test_stop_signals_when_identity_check_is_inconclusive():
    """When `ps` itself can't be run (missing, sandboxed away), the
    identity check must not block the stop — proceeding on bare liveness
    is the pre-existing, safer default absent positive evidence of a
    mismatch."""
    m = mock_open(read_data="1234")
    with (
        patch("builtins.open", m),
        patch("modelman.providers.lifecycle.pidproc.os.kill") as mock_kill,
        patch("modelman.providers.lifecycle.pidproc.os.unlink"),
        patch(
            "modelman.providers.lifecycle.pidproc.subprocess.run",
            side_effect=OSError("ps not found"),
        ),
    ):
        _proc().stop()
    import signal

    assert mock_kill.call_args_list[1].args == (1234, signal.SIGTERM)


def test_log_tail_returns_last_512_chars():
    """log_tail must decode the trailing bytes and cap the result at 512
    chars, matching today's _log_tail behavior exactly."""
    tail_source = "x" * 600
    log_mock = mock_open(read_data=tail_source.encode())
    log_mock.return_value.tell.return_value = 600
    with patch("builtins.open", log_mock):
        result = _proc().log_tail()
    assert len(result) == 512


def test_log_tail_returns_empty_string_on_missing_file():
    """A missing logfile must return "" rather than raise, so callers can
    unconditionally include it in an error message."""
    with patch("builtins.open", side_effect=OSError("no such file")):
        assert _proc().log_tail() == ""
