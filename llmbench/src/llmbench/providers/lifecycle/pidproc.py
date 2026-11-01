"""A pidfile-tracked background process: spawn, stop, log-tail.

Generalized from today's mtplx-only `_start_mtplx_serve`/`_log_tail`
helpers (`lifecycle/__init__.py`) and bash's `bin/lib/mlx-lm-server.sh`,
for any backend that runs as a plain backgrounded subprocess tracked by a
pidfile (never a LaunchAgent) — mtplx and mlx_lm_server both fit this
shape.
"""

from __future__ import annotations

import contextlib
import os
import re
import signal
import subprocess
import time
from collections.abc import Sequence
from dataclasses import dataclass

from .envelope import LifecycleError


def signalable(pid: int) -> bool:
    """Whether a pidfile's `pid` may ever be signalled: not 0 or a negative
    pid (this process's group, a broadcast), not 1 (launchd), not this
    process itself (a recycled pid can name it)."""
    return pid > 1 and pid != os.getpid()


@dataclass(frozen=True)
class PidfileProcess:
    name: str
    pidfile: str
    logfile: str

    def spawn(self, argv: list[str], *, new_session: bool = False) -> subprocess.Popen:
        """Open self.logfile for append, spawn argv with stdout=stderr=that
        file handle, write the child's pid to self.pidfile, sleep 0.2s, then
        check proc.poll() — if the process already exited, raise
        LifecycleError with a log tail (omitted if empty). Return the live
        Popen on success.

        `new_session` starts the child in its own session (wt's Setsid), so
        a SIGHUP to the caller's terminal cannot kill it. Without it Popen
        gets no session keyword at all."""
        with open(self.logfile, "ab") as log:
            if new_session:
                proc = subprocess.Popen(argv, stdout=log, stderr=log, start_new_session=True)
            else:
                proc = subprocess.Popen(argv, stdout=log, stderr=log)
        with open(self.pidfile, "w") as f:
            f.write(str(proc.pid))
        # Give the process a moment to fail (missing weights, port conflict,
        # bad CLI flag) before the caller commits to a full model-load poll.
        time.sleep(0.2)
        if proc.poll() is not None:
            log_tail = self.log_tail()
            raise LifecycleError(
                f"{self.name} exited immediately (exit {proc.returncode})"
                + (f"; log tail: {log_tail}" if log_tail else "")
            )
        return proc

    def stop(self) -> None:
        """Read self.pidfile (missing file = return, nothing to do). Parse
        the pid (unreadable/empty content, or a non-positive value = remove
        the pidfile and return, tolerant like bash — os.kill(0, ...) would
        signal this process's own group and os.kill(-1, ...) would broadcast
        to every process this user can signal, so a corrupt pidfile must
        never reach os.kill at all). If the process is alive AND still
        looks like the process this pidfile was written for (best-effort —
        see `_looks_like_this_process`), send SIGTERM — swallow
        ProcessLookupError/PermissionError. Always unlink the pidfile at the
        end, even if the process was already dead. No SIGKILL escalation —
        this matches bash's mlx_lm_server_stop exactly (SIGTERM-only, fire
        and forget). Followed by a bounded best-effort reap (`_reap`): bash
        has nothing equivalent to reap, because bash's `kill` doesn't hold a
        live child handle the way `spawn()`'s `subprocess.Popen` does — when
        `stop()` runs in the same process that called `spawn()` (e.g. the
        TUI replacing an occupant mid-session), the signaled process is a
        real child of this process, and never reaping it leaves a zombie
        pid-table entry for the rest of this process's lifetime."""
        try:
            with open(self.pidfile) as f:
                content = f.read().strip()
        except OSError:
            return
        try:
            pid = int(content)
        except ValueError:
            os.unlink(self.pidfile)
            return
        if pid <= 0:
            os.unlink(self.pidfile)
            return
        try:
            os.kill(pid, 0)  # liveness check; raises if not alive/permitted
            if self._looks_like_this_process(pid):
                os.kill(pid, signal.SIGTERM)
                self._reap(pid)
        except (ProcessLookupError, PermissionError):
            pass
        finally:
            with contextlib.suppress(OSError):
                os.unlink(self.pidfile)

    def stop_verified(
        self,
        expect_argv: Sequence[str],
        *,
        grace: float = 10.0,
        kill_wait: float = 5.0,
    ) -> bool:
        """Stop the process the pidfile names the way wt's signalVerifiedServer
        does, and report whether it is gone. Only a process that passes the
        strict identity check (`_matches_argv`: this user's, exactly
        `expect_argv`) is signalled: SIGTERM, then SIGKILL after `grace`
        seconds, then up to `kill_wait` more for it to go. The pidfile is
        removed only once the process is gone (or was never ours: absent,
        unusable, dead, or another process holding a recycled pid), so a
        server that outlives the signals stays findable by the next
        `isolate` / `stop`. False means it is still running.

        A pid `signalable` refuses is never signalled; an unreadable or
        non-positive pidfile is removed, as `stop()` does.
        """
        try:
            with open(self.pidfile) as f:
                content = f.read().strip()
        except OSError:
            return True
        try:
            pid = int(content)
        except ValueError:
            pid = 0
        if pid <= 0:
            with contextlib.suppress(OSError):
                os.unlink(self.pidfile)
            return True
        if not signalable(pid):
            return True
        if not self._alive(pid) or not self._matches_argv(pid, expect_argv):
            # Dead, or the pid now belongs to something else: the file is stale.
            with contextlib.suppress(OSError):
                os.unlink(self.pidfile)
            return True
        gone = self._signal_and_wait(pid, signal.SIGTERM, grace)
        if not gone and self._matches_argv(pid, expect_argv):
            gone = self._signal_and_wait(pid, signal.SIGKILL, kill_wait)
        if gone:
            with contextlib.suppress(OSError):
                os.unlink(self.pidfile)
        return gone

    @staticmethod
    def _alive(pid: int) -> bool:
        """Whether `pid` is a live process. A child of this process that has
        exited is reaped first (`os.kill(pid, 0)` succeeds on a zombie)."""
        try:
            if os.waitpid(pid, os.WNOHANG)[0] == pid:
                return False
        except ChildProcessError:
            pass
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return False
        except PermissionError:
            return True
        return True

    def _signal_and_wait(self, pid: int, sig: signal.Signals, timeout: float) -> bool:
        """Send `sig` to `pid`; True once the process is gone within `timeout`."""
        try:
            os.kill(pid, sig)
        except ProcessLookupError:
            return True
        except PermissionError:
            return False
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if not self._alive(pid):
                return True
            time.sleep(0.1)
        return not self._alive(pid)

    def _matches_argv(self, pid: int, expect_argv: Sequence[str]) -> bool:
        """Strict identity: `ps` reports this user as the owner and a command
        line that is the binary (by file name) followed by exactly
        `expect_argv`. A pid `ps` cannot describe is NOT a match, unlike the
        loose `_looks_like_this_process`. The command line is split on
        spaces, so an argument holding a space cannot match: the servers this
        is used for have none."""
        try:
            result = subprocess.run(
                ["ps", "-p", str(pid), "-o", "uid=,command="],
                capture_output=True,
                text=True,
                check=False,
            )
        except OSError:
            return False
        parts = result.stdout.split()
        if result.returncode != 0 or len(parts) < 2:
            return False
        try:
            uid = int(parts[0])
        except ValueError:
            return False
        return (
            uid == os.getuid()
            and os.path.basename(parts[1]) == self.name
            and (parts[2:] == list(expect_argv))
        )

    def read_pid(self) -> int | None:
        """The pid self.pidfile names, or None when the file is missing,
        unreadable or does not hold an integer. Not validated: see
        `signalable`."""
        try:
            with open(self.pidfile) as f:
                return int(f.read().strip())
        except (OSError, ValueError):
            return None

    def live_pid(self, expect_argv: Sequence[str] | None = None) -> int | None:
        """The pidfile's pid when it is a live process that looks like this
        one, else None. Read-only: never unlinks the pidfile and never
        signals (`os.kill(pid, 0)` only, and never for a pid `signalable`
        refuses).

        Without `expect_argv` the identity check is the loose
        `_looks_like_this_process`: `self.name` in the pid's `ps` line, and a
        pid `ps` cannot describe counts as a match. With it the check is the
        strict `_matches_argv` (this user's process, exactly that argv) and
        a pid `ps` cannot describe is not ours."""
        pid = self.read_pid()
        if pid is None or not signalable(pid):
            return None
        try:
            os.kill(pid, 0)
        except (ProcessLookupError, PermissionError):
            return None
        if expect_argv is None:
            return pid if self._looks_like_this_process(pid) else None
        return pid if self._matches_argv(pid, expect_argv) else None

    @staticmethod
    def _reap(pid: int, timeout: float = 2.0, interval: float = 0.1) -> None:
        """Best-effort, bounded `waitpid(pid, WNOHANG)` poll after signaling
        `pid`, so a child this process itself spawned doesn't linger as a
        zombie. `os.waitpid` raises `ChildProcessError` when `pid` isn't
        actually a child of this process — the common case when `stop()`
        runs in a fresh CLI invocation that never spawned it — so this is a
        quick no-op there, not a hang."""
        deadline = time.monotonic() + timeout
        try:
            while time.monotonic() < deadline:
                reaped_pid, _ = os.waitpid(pid, os.WNOHANG)
                if reaped_pid == pid:
                    return
                time.sleep(interval)
        except ChildProcessError:
            pass

    def _looks_like_this_process(self, pid: int) -> bool:
        """Best-effort guard against a dead process's pid being reassigned
        by the OS to an unrelated process before stop() runs (the liveness
        check above only proves SOME process now holds `pid`, not that it's
        still ours). Shells out to `ps -p <pid> -o command=` (macOS/BSD ps;
        this project is Darwin-only) and checks whether `self.name` appears
        in the reported command, ignoring punctuation so "mlx_lm_server"
        matches a reported "mlx_lm.server" binary name.

        Returns True (proceed with the signal) whenever identity can't be
        confirmed either way — `ps` failing, the pid already gone, or an
        empty/unrecognized command — since the previous behavior (signal
        on bare liveness) is the safer default absent real evidence of a
        mismatch; this only suppresses the signal on a *positive* mismatch.
        """
        try:
            result = subprocess.run(
                ["ps", "-p", str(pid), "-o", "command="],
                capture_output=True,
                text=True,
                check=False,
            )
        except OSError:
            return True
        if result.returncode != 0 or not result.stdout.strip():
            return True
        command = re.sub(r"[^a-z0-9]", "", result.stdout.lower())
        needle = re.sub(r"[^a-z0-9]", "", self.name.lower())
        return needle in command

    def log_tail(self, max_bytes: int = 1024) -> str:
        """Same as today's _log_tail: open self.logfile in binary mode,
        seek to the last max_bytes, decode with errors='replace', return
        the last 512 chars. Return "" on OSError (missing file)."""
        try:
            with open(self.logfile, "rb") as f:
                f.seek(0, 2)
                size = f.tell()
                f.seek(max(0, size - max_bytes))
                return f.read().decode(errors="replace")[-512:]
        except OSError:
            return ""
