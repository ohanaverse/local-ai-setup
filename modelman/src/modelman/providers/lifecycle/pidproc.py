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
import signal
import subprocess
import time
from dataclasses import dataclass

from .envelope import LifecycleError


@dataclass(frozen=True)
class PidfileProcess:
    name: str
    pidfile: str
    logfile: str

    def spawn(self, argv: list[str]) -> subprocess.Popen:
        """Open self.logfile for append, spawn argv with stdout=stderr=that
        file handle, write the child's pid to self.pidfile, sleep 0.2s, then
        check proc.poll() — if the process already exited, raise
        LifecycleError with a log tail (omitted if empty). Return the live
        Popen on success."""
        with open(self.logfile, "ab") as log:
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
        the pid (unreadable/empty content = remove the pidfile and return,
        tolerant like bash). If the process is alive, send SIGTERM —
        swallow ProcessLookupError/PermissionError. Always unlink the
        pidfile at the end, even if the process was already dead. No
        SIGKILL escalation, no waitpid/reaping — this matches bash's
        mlx_lm_server_stop exactly (SIGTERM-only, fire and forget)."""
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
        try:
            os.kill(pid, 0)  # liveness check; raises if not alive/permitted
            os.kill(pid, signal.SIGTERM)
        except (ProcessLookupError, PermissionError):
            pass
        finally:
            with contextlib.suppress(OSError):
                os.unlink(self.pidfile)

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
