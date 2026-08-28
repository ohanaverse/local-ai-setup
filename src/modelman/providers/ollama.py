"""Ollama provider — uses `ollama pull`, `ollama show`, `ollama list`."""

from __future__ import annotations

import contextlib
import re
import subprocess
import threading
from collections.abc import Callable
from typing import Any, Protocol

from .base import LocalModel, Provider, VariantSpec
from .registry import ProviderRegistry


class _Runner(Protocol):
    def __call__(self, args: list[str], **kwargs: Any) -> Any: ...


def _default_runner(args: list[str], **kwargs: Any):
    """Default subprocess runner using subprocess.run (blocking).

    The Ollama provider's download path uses its own Popen-based runner
    (see `_tracked_popen_runner`) so it can be terminated on cancel.
    """
    return subprocess.run(args, **kwargs)


# Matches ANSI control sequences (cursor moves, color, etc.) plus carriage
# returns and backspaces. Used to clean ollama's progress output before
# forwarding it to the UI log.
_ANSI_RE = re.compile(r"\x1b\[[\?]?[0-9;]*[a-zA-Z]|\x1b\][^\x07]*\x07|\r|\x08")


def _strip_ansi(text: str) -> str:
    """Remove ANSI escape codes and stray CR/BS characters from a line."""
    return _ANSI_RE.sub("", text).strip()


def _tracked_popen_runner(
    provider: OllamaProvider,
    args: list[str],
    *,
    on_progress: Callable[[str], None] | None = None,
    **kwargs: Any,
):
    """Spawn `args` via Popen, stream stdout/stderr to `on_progress`, and wait.

    Stores the Popen on `provider._current_proc` so `cancel_current()` can
    terminate it. If `on_progress` is provided, spawns a reader thread that
    consumes line-buffered output and invokes the callback with each
    meaningful (non-empty, ANSI-stripped) line. Returns a CompletedProcess.
    """
    # Force line-buffered, decoded text pipes for streaming.
    kwargs.setdefault("stdout", subprocess.PIPE)
    kwargs.setdefault("stderr", subprocess.STDOUT)
    kwargs.setdefault("bufsize", 1)
    kwargs.setdefault("text", True)
    proc = subprocess.Popen(args, **kwargs)
    provider._current_proc = proc

    reader_thread: threading.Thread | None = None
    if on_progress is not None and proc.stdout is not None:

        def _pump() -> None:
            try:
                for raw_line in proc.stdout:  # type: ignore[union-attr]
                    cleaned = _strip_ansi(raw_line)
                    if cleaned:
                        with contextlib.suppress(Exception):
                            on_progress(cleaned)
            except Exception:
                pass

        reader_thread = threading.Thread(target=_pump, daemon=True)
        reader_thread.start()

    try:
        proc.wait()
    finally:
        if reader_thread is not None:
            reader_thread.join(timeout=1.0)
        provider._current_proc = None
    return subprocess.CompletedProcess(args, proc.returncode, "", "")


def _parse_ollama_list_sizes(stdout: str) -> dict[str, int]:
    """Parse `ollama list` output into {model_name: size_bytes}.

    Each row is: NAME ID <number> <UNIT> MODIFIED... (SIZE column is two tokens).
    """
    sizes: dict[str, int] = {}
    units = {"B": 1, "KB": 1024, "MB": 1024**2, "GB": 1024**3, "TB": 1024**4}
    for line in stdout.splitlines():
        line = line.strip()
        if not line or line.startswith("NAME"):
            continue
        parts = line.split()
        if len(parts) < 4:
            continue
        name = parts[0]
        num_str, unit = parts[2], parts[3].upper()
        if unit not in units:
            continue
        try:
            value = float(num_str)
        except ValueError:
            continue
        sizes[name] = int(value * units[unit])
    return sizes


class OllamaProvider(Provider):
    name = "ollama"

    def __init__(self, config: dict) -> None:
        super().__init__(config)
        # Set to the active Popen while a download is running so that
        # cancel_current() can terminate it. None otherwise.
        self._current_proc: subprocess.Popen[bytes] | None = None

    def cancel_current(self) -> None:
        """Terminate the active subprocess, escalating to SIGKILL if needed.

        Called by the apply-queue cancellation flow when the user
        presses Cancel mid-download. Safe to call from any thread.
        Spawns a watchdog thread that escalates to `kill()` if the
        proc hasn't exited within ~1s of SIGTERM. The watchdog is a
        daemon so it never blocks process exit.
        """
        proc = self._current_proc
        if proc is None or proc.poll() is not None:
            return
        with contextlib.suppress(Exception):
            proc.terminate()

        # Watchdog: if SIGTERM doesn't take effect within a short window,
        # escalate to SIGKILL so the user actually sees the download stop.
        def _watchdog() -> None:
            import time as _time

            for _ in range(10):  # up to ~1s in 100ms ticks
                _time.sleep(0.1)
                if proc.poll() is not None:
                    return
            with contextlib.suppress(Exception):
                proc.kill()

        threading.Thread(target=_watchdog, daemon=True).start()

    def is_downloaded(self, variant: VariantSpec, runner: _Runner | None = None) -> bool:
        r = (runner or _default_runner)(
            ["ollama", "show", variant["name"]], capture_output=True, text=True
        )
        return r.returncode == 0

    def download(
        self,
        variant: VariantSpec,
        runner: _Runner | None = None,
        on_progress: Callable[[str], None] | None = None,
    ) -> str:
        if runner is None:
            # Real subprocess path: use a tracked Popen so we can cancel it
            # and stream its output to on_progress (if given).
            r = _tracked_popen_runner(
                self,
                ["ollama", "pull", variant["name"]],
                on_progress=on_progress,
            )
        else:
            r = runner(["ollama", "pull", variant["name"]])
        if r.returncode != 0:
            raise RuntimeError(f"`ollama pull {variant['name']}` failed (exit {r.returncode})")
        return f"ollama:{variant['name']}"

    def size_of(self, variant: VariantSpec, runner: _Runner | None = None) -> int | None:
        r = (runner or _default_runner)(["ollama", "list"], capture_output=True, text=True)
        if r.returncode != 0:
            return None
        sizes = _parse_ollama_list_sizes(r.stdout)
        return sizes.get(variant["name"])

    def list_local(self, runner: _Runner | None = None) -> list[LocalModel]:
        r = (runner or _default_runner)(["ollama", "list"], capture_output=True, text=True)
        models: list[LocalModel] = []
        for line in r.stdout.splitlines():
            line = line.strip()
            if not line or line.startswith("NAME"):
                continue
            parts = line.split()
            if len(parts) >= 1:
                models.append(
                    {
                        "variant_id": parts[0],
                        "path": f"ollama:{parts[0]}",
                        "size_bytes": None,
                    }
                )
        return models


ProviderRegistry.register(OllamaProvider)
