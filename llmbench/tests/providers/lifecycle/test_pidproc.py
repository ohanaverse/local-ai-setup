import os
from unittest.mock import MagicMock, mock_open, patch

import pytest

from llmbench.providers.lifecycle.envelope import LifecycleError
from llmbench.providers.lifecycle.pidproc import PidfileProcess


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
            "llmbench.providers.lifecycle.pidproc.subprocess.Popen", return_value=proc
        ) as mock_popen,
        patch("llmbench.providers.lifecycle.pidproc.time.sleep"),
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
        patch("llmbench.providers.lifecycle.pidproc.subprocess.Popen", return_value=proc),
        patch("llmbench.providers.lifecycle.pidproc.time.sleep"),
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
        patch("llmbench.providers.lifecycle.pidproc.os.unlink") as mock_unlink,
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
        patch("llmbench.providers.lifecycle.pidproc.os.kill") as mock_kill,
        patch("llmbench.providers.lifecycle.pidproc.os.unlink") as mock_unlink,
        patch("llmbench.providers.lifecycle.pidproc.subprocess.run", _ps_matching()),
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
        patch("llmbench.providers.lifecycle.pidproc.os.kill") as mock_kill,
        patch(
            "llmbench.providers.lifecycle.pidproc.os.waitpid", return_value=(1234, 0)
        ) as mock_waitpid,
        patch("llmbench.providers.lifecycle.pidproc.os.unlink") as mock_unlink,
        patch("llmbench.providers.lifecycle.pidproc.subprocess.run", _ps_matching()),
    ):
        _proc().stop()
    mock_waitpid.assert_called_once_with(1234, os.WNOHANG)
    mock_kill.assert_called()  # SIGTERM still sent before the reap attempt
    mock_unlink.assert_called_once_with("/tmp/x.pid")


def test_stop_tolerates_reaping_a_process_it_did_not_spawn():
    """When stop() runs in a fresh process that never spawned the pid it
    read from the pidfile (the common case: a separate `llmbench provider
    stop` CLI invocation), `os.waitpid` raises ChildProcessError — this
    must be swallowed as a quick no-op, not propagate or hang."""
    m = mock_open(read_data="1234")
    with (
        patch("builtins.open", m),
        patch("llmbench.providers.lifecycle.pidproc.os.kill") as mock_kill,
        patch(
            "llmbench.providers.lifecycle.pidproc.os.waitpid",
            side_effect=ChildProcessError(),
        ),
        patch("llmbench.providers.lifecycle.pidproc.os.unlink") as mock_unlink,
        patch("llmbench.providers.lifecycle.pidproc.subprocess.run", _ps_matching()),
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
            "llmbench.providers.lifecycle.pidproc.os.kill",
            side_effect=ProcessLookupError(),
        ),
        patch("llmbench.providers.lifecycle.pidproc.os.unlink") as mock_unlink,
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
            "llmbench.providers.lifecycle.pidproc.os.kill",
            side_effect=[None, PermissionError()],
        ),
        patch("llmbench.providers.lifecycle.pidproc.os.unlink") as mock_unlink,
        patch("llmbench.providers.lifecycle.pidproc.subprocess.run", _ps_matching()),
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
        patch("llmbench.providers.lifecycle.pidproc.os.kill") as mock_kill,
        patch("llmbench.providers.lifecycle.pidproc.os.unlink") as mock_unlink,
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
        patch("llmbench.providers.lifecycle.pidproc.os.kill") as mock_kill,
        patch("llmbench.providers.lifecycle.pidproc.os.unlink") as mock_unlink,
        patch("llmbench.providers.lifecycle.pidproc.subprocess.run", ps_mismatch),
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
        patch("llmbench.providers.lifecycle.pidproc.os.kill") as mock_kill,
        patch("llmbench.providers.lifecycle.pidproc.os.unlink"),
        patch(
            "llmbench.providers.lifecycle.pidproc.subprocess.run",
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


# --- live_pid: the read-only ownership check ----------------------------------

PP = "llmbench.providers.lifecycle.pidproc"


def _sushi(tmp_path, content: str | None = "4242"):
    pidfile = tmp_path / "sushi.pid"
    if content is not None:
        pidfile.write_text(content)
    return PidfileProcess(name="sushi", pidfile=str(pidfile), logfile=str(tmp_path / "sushi.log"))


def test_live_pid_returns_the_pid_of_a_live_matching_process(tmp_path):
    """A live pid whose `ps` line names the process is the verified pid; the
    check is read-only, so the pidfile is still there afterwards."""
    proc = _sushi(tmp_path, "4242\n")
    with (
        patch(f"{PP}.os.kill") as kill,
        patch(f"{PP}.subprocess.run", _ps_matching("sushi")),
    ):
        assert proc.live_pid() == 4242
    kill.assert_called_once_with(4242, 0)
    assert os.path.exists(proc.pidfile)


def test_live_pid_is_none_without_a_pidfile(tmp_path):
    with patch(f"{PP}.os.kill") as kill:
        assert _sushi(tmp_path, None).live_pid() is None
    kill.assert_not_called()


@pytest.mark.parametrize("content", ["", "abc", "0", "-7"])
def test_live_pid_is_none_for_unusable_content(tmp_path, content):
    """Unparsable or non-positive content never reaches os.kill (0 is this
    process's group, a negative pid a broadcast) and the file is kept."""
    proc = _sushi(tmp_path, content)
    with patch(f"{PP}.os.kill") as kill:
        assert proc.live_pid() is None
    kill.assert_not_called()
    assert os.path.exists(proc.pidfile)


def test_live_pid_is_none_for_pid_one_and_this_process(tmp_path):
    """pid 1 (launchd) and this process itself are never a server llmbench
    started: refused before any os.kill, as wt refuses them."""
    for content in ("1", str(os.getpid())):
        proc = _sushi(tmp_path, content)
        with (
            patch(f"{PP}.os.kill") as kill,
            patch(f"{PP}.subprocess.run", _ps_matching("sushi")),
        ):
            assert proc.live_pid() is None
        kill.assert_not_called()


@pytest.mark.parametrize("error", [ProcessLookupError(), PermissionError()])
def test_live_pid_is_none_when_the_pid_cannot_be_signalled(tmp_path, error):
    proc = _sushi(tmp_path)
    with patch(f"{PP}.os.kill", side_effect=error):
        assert proc.live_pid() is None
    assert os.path.exists(proc.pidfile)


def test_live_pid_is_none_when_the_process_is_something_else(tmp_path):
    """A recycled pid now held by another program is not ours."""
    proc = _sushi(tmp_path)
    ps_other = MagicMock(return_value=MagicMock(returncode=0, stdout="/usr/bin/sleep 60\n"))
    with patch(f"{PP}.os.kill"), patch(f"{PP}.subprocess.run", ps_other):
        assert proc.live_pid() is None
    assert os.path.exists(proc.pidfile)


# --- spawn's session ----------------------------------------------------------


def _spawned_kwargs(tmp_path, **spawn_kw) -> dict:
    proc = _sushi(tmp_path, None)
    child = MagicMock(pid=4242, poll=MagicMock(return_value=None))
    with (
        patch(f"{PP}.subprocess.Popen", return_value=child) as popen,
        patch(f"{PP}.time.sleep"),
    ):
        # A path that does not exist: should the Popen patch ever miss, the
        # spawn fails instead of starting the installed sushi.
        proc.spawn(["/Users/nobody/.local/bin/sushi", "serve"], **spawn_kw)
    return popen.call_args.kwargs


def test_spawn_new_session_starts_a_new_session(tmp_path):
    assert _spawned_kwargs(tmp_path, new_session=True)["start_new_session"] is True


def test_spawn_by_default_passes_no_session_kwarg(tmp_path):
    assert set(_spawned_kwargs(tmp_path)) == {"stdout", "stderr"}


# --- stop_verified and the strict identity ------------------------------------

ARGV = ["serve", "--port", "12345", "--host", "127.0.0.1"]


def _ps_line(uid=None, command="/Users/u/.local/bin/sushi serve --port 12345 --host 127.0.0.1"):
    """A subprocess.run mock for `ps -p <pid> -o uid=,command=`."""
    uid = os.getuid() if uid is None else uid
    return MagicMock(return_value=MagicMock(returncode=0, stdout=f"  {uid} {command}\n"))


def test_strict_identity_wants_this_user_and_the_exact_argv(tmp_path):
    """Owner, binary file name and the full argument list must all match:
    a process that merely mentions sushi is not it."""
    proc = _sushi(tmp_path)
    with patch(f"{PP}.subprocess.run", _ps_line()):
        assert proc._matches_argv(4242, ARGV) is True
    wrong = {
        "another user": _ps_line(uid=os.getuid() + 1),
        "another port": _ps_line(command="/x/sushi serve --port 9 --host 127.0.0.1"),
        "an extra argument": _ps_line(command="/x/sushi serve --port 12345 --host 127.0.0.1 -v"),
        "a pager on its log": _ps_line(command="tail -f /tmp/local-ai-setup-sushi.log"),
        "an editor on its models": _ps_line(command="vim /Users/u/.sushi/models/x/config.json"),
        "ps cannot say": MagicMock(return_value=MagicMock(returncode=1, stdout="")),
        "garbage": _ps_line(command=""),
    }
    for why, ps in wrong.items():
        with patch(f"{PP}.subprocess.run", ps):
            assert proc._matches_argv(4242, ARGV) is False, why


def test_live_pid_with_an_argv_fails_closed_when_ps_cannot_answer(tmp_path):
    """The loose check treats an unanswerable `ps` as a match; the strict one
    must not, or a stale pidfile would make a foreign server look owned."""
    proc = _sushi(tmp_path)
    ps_down = MagicMock(return_value=MagicMock(returncode=1, stdout=""))
    with patch(f"{PP}.os.kill"), patch(f"{PP}.subprocess.run", ps_down):
        assert proc.live_pid() == 4242  # loose: inconclusive counts as ours
        assert proc.live_pid(ARGV) is None  # strict: it does not
    with patch(f"{PP}.os.kill"), patch(f"{PP}.subprocess.run", _ps_line()):
        assert proc.live_pid(ARGV) == 4242


def test_stop_verified_signals_a_matching_process_and_removes_the_pidfile_once_gone(tmp_path):
    proc = _sushi(tmp_path)
    alive = iter([True, False])  # alive for the identity check, gone after SIGTERM
    sent = []
    with (
        patch.object(PidfileProcess, "_alive", side_effect=lambda pid: next(alive)),
        patch(f"{PP}.os.kill", side_effect=lambda pid, sig: sent.append(sig)),
        patch(f"{PP}.subprocess.run", _ps_line()),
        patch(f"{PP}.time.sleep"),
    ):
        assert proc.stop_verified(ARGV) is True
    assert [int(s) for s in sent] == [15]  # SIGTERM only: it went
    assert not os.path.exists(proc.pidfile)


def test_stop_verified_escalates_to_sigkill_when_sigterm_is_ignored(tmp_path):
    proc = _sushi(tmp_path)
    sent = []
    state = {"killed": False}

    def kill(pid, sig):
        sent.append(int(sig))
        state["killed"] = int(sig) == 9

    with (
        patch.object(PidfileProcess, "_alive", side_effect=lambda pid: not state["killed"]),
        patch(f"{PP}.os.kill", side_effect=kill),
        patch(f"{PP}.subprocess.run", _ps_line()),
        patch(f"{PP}.time.sleep"),
        patch(f"{PP}.time.monotonic", side_effect=iter([0.0, 0.0, 99.0, 100.0, 100.0, 100.5])),
    ):
        assert proc.stop_verified(ARGV, grace=10.0) is True
    assert sent == [15, 9]
    assert not os.path.exists(proc.pidfile)


def test_stop_verified_keeps_the_pidfile_when_the_process_will_not_die(tmp_path):
    """A server that outlives SIGTERM and SIGKILL stays findable: the next
    isolate or stop still has its pidfile instead of an unowned-looking port."""
    proc = _sushi(tmp_path)
    ticks = iter(range(0, 10_000, 100))
    with (
        patch.object(PidfileProcess, "_alive", return_value=True),
        patch(f"{PP}.os.kill"),
        patch(f"{PP}.subprocess.run", _ps_line()),
        patch(f"{PP}.time.sleep"),
        patch(f"{PP}.time.monotonic", side_effect=lambda: float(next(ticks))),
    ):
        assert proc.stop_verified(ARGV, grace=1.0, kill_wait=1.0) is False
    assert os.path.exists(proc.pidfile)


def test_stop_verified_never_signals_a_recycled_pid(tmp_path):
    """A pid now held by something else is a stale pidfile: removed, not killed."""
    proc = _sushi(tmp_path)
    with (
        patch.object(PidfileProcess, "_alive", return_value=True),
        patch(f"{PP}.os.kill") as kill,
        patch(f"{PP}.subprocess.run", _ps_line(command="vim /Users/u/.sushi/models/x")),
    ):
        assert proc.stop_verified(ARGV) is True
    kill.assert_not_called()
    assert not os.path.exists(proc.pidfile)


@pytest.mark.parametrize("content", ["", "x", "0", "-1"])
def test_stop_verified_removes_an_unusable_pidfile_without_signalling(tmp_path, content):
    proc = _sushi(tmp_path, content)
    with patch(f"{PP}.os.kill") as kill:
        assert proc.stop_verified(ARGV) is True
    kill.assert_not_called()
    assert not os.path.exists(proc.pidfile)


def test_stop_verified_without_a_pidfile_has_nothing_to_do(tmp_path):
    proc = _sushi(tmp_path, None)
    with patch(f"{PP}.os.kill") as kill:
        assert proc.stop_verified(ARGV) is True
    kill.assert_not_called()
