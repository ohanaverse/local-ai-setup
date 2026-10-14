"""Ollama backend — ported from `bin/llm-isolate-provider`'s `ollama)` case
arm, `start_ollama()`, `stop_ollama_and_wait()`, and
`wait_for_ollama_unloaded()`.

Live: registered in `backends.BACKENDS`, which `orchestrate.py` reads to
drive `isolate()`/`stop()`/`stop_all()`/`restore()`, reached from
`llmbench provider ...` and the benchmarks.
"""

from __future__ import annotations

import shutil
import subprocess
import time
import urllib.request

from .. import launchd
from .base import Backend, StartPlan, StillFinishing

OLLAMA_PORT = 11434
OLLAMA_BASE = f"http://localhost:{OLLAMA_PORT}"
OLLAMA_HEALTH_URL = f"{OLLAMA_BASE}/api/tags"
OLLAMA_CHAT_URL = f"{OLLAMA_BASE}/v1/chat/completions"
DEFAULT_MODEL = "ornith-1.5:35b"
ENV_VAR = "LLM_ISOLATE_OLLAMA_MODEL"
# `ollama stop` exits 0 at once, and the model stays in `ollama ps` until the
# request it is serving ends: 6 to 20 seconds as observed, and 55 behind a
# long generation. Three minutes covers that with margin, and is the bound
# wt's own ollama stop uses (`ollamaUnloadTimeout`, wt/internal/lifecycle).
UNLOAD_TIMEOUT = 180.0
UNLOAD_INTERVAL = 1.0
UNLOAD_TRIES = int(UNLOAD_TIMEOUT / UNLOAD_INTERVAL)


def _loaded_model_names() -> list[str]:
    """Names ollama currently has LOADED, via `ollama ps`. Returns [] on any
    failure (ollama binary missing, non-zero exit, daemon down) — a down
    daemon has nothing loaded. Python port of bash's
    `ollama ps 2>/dev/null | tail -n +2 | awk 'NF {print $1}'`."""
    if shutil.which("ollama") is None:
        return []
    try:
        result = subprocess.run(["ollama", "ps"], capture_output=True, text=True, check=False)
    except OSError:
        return []
    if result.returncode != 0:
        return []
    names = []
    for line in result.stdout.splitlines()[1:]:  # drop the NAME/ID/... header
        fields = line.split()
        if fields:
            names.append(fields[0])
    return names


class OllamaBackend(Backend):
    id = "ollama"
    occupancy_key = "ollama"
    env_var = ENV_VAR
    default_model = DEFAULT_MODEL
    health_url = OLLAMA_HEALTH_URL
    chat_url = OLLAMA_CHAT_URL
    restore_action = "restart"

    def check_available(self) -> str | None:
        return None  # bash has no binary-on-PATH gate on ollama's start path — preserve that

    def resolve(self, model: str | None, extra_args: tuple[str, ...]) -> StartPlan:
        return StartPlan(model=self._resolve_model(model), direct_url=OLLAMA_CHAT_URL)

    def start(self, plan: StartPlan) -> None:
        # bash: `curl -s -m 2 http://localhost:11434/api/tags >/dev/null
        # 2>&1 || launchctl kickstart -k gui/$(id -u)/com.ollama.ollama
        # 2>/dev/null || true` — a single one-shot 2s probe, not a poll
        # loop, so a single urlopen (not probe.wait_for_port_open) matches
        # bash's behavior exactly.
        try:
            urllib.request.urlopen(OLLAMA_HEALTH_URL, timeout=2.0)  # noqa: S310 — localhost probe
        except OSError:
            launchd.kickstart(launchd.OLLAMA_LABEL)
        # warm() is NOT called here — orchestrate.isolate() calls it once
        # after start()+wait_ready() for every backend; calling it here too
        # would warm the model twice per isolate() (a real bug fixed
        # alongside this port: see orchestrate.isolate()'s docstring).

    def stop_and_wait(self) -> str | None:
        # Stop EVERY model `ollama ps` reports loaded (not just the default
        # model — something else may have been loaded, with `wt start` or
        # `ollama run`), matching bash's `stop_ollama_and_wait`. Every stop
        # is sent before the wait, so the models finish their requests side
        # by side under one bound.
        accepted = set()
        for name in _loaded_model_names():
            result = subprocess.run(["ollama", "stop", name], capture_output=True, check=False)
            if result.returncode == 0:
                accepted.add(name)
        # `ollama stop` unloads the model but leaves the daemon (port 11434)
        # up, so poll `ollama ps` for an empty model list, not the port. The
        # wait ends on a poll, not a sleep: a model may unload in the last
        # interval.
        for _ in range(UNLOAD_TRIES):
            if not _loaded_model_names():
                return None
            time.sleep(UNLOAD_INTERVAL)
        left = _loaded_model_names()
        if not left:
            return None
        waited = UNLOAD_TRIES * UNLOAD_INTERVAL
        if set(left) <= accepted:
            # ollama accepted every stop still pending: each model unloads
            # when its request in flight ends. Not a failed stop, but not a
            # clean one either — the model is still loaded (StillFinishing).
            return StillFinishing(
                f"ollama is still finishing a request on {', '.join(left)} after waiting "
                f"{waited:.0f}s; it unloads them when that ends"
            )
        return f"ollama still has models loaded after waiting {waited:.0f}s"

    def restore(self) -> None:
        self._restart_if_down(restart=lambda: launchd.kickstart(launchd.OLLAMA_LABEL))


OLLAMA = OllamaBackend()
