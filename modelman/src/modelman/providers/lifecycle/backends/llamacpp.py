"""llama.cpp backend — ported from `bin/llm-isolate-provider`'s `llamacpp)`
case arm, `start_llamacpp()`, and the llamacpp branch of `stop_all_local()`.

RETIRED (2026-09-07, issue #33) but not removed: the bash script keeps the
branch with no guard on it, so a direct `llm-isolate-provider llamacpp`
invocation still works end to end. This port preserves that exactly —
`LLAMACPP` is registered in `BACKENDS` (so `orchestrate.isolate()` can start
it and `orchestrate.stop_all()`/`_stop_others()` tear it down) but is
deliberately absent from `SUPPORTED_PROVIDER_IDS`, so `orchestrate.stop()`
rejects it just as bash's `stop` verb has no `llamacpp` case. Re-enable
steps: docs/reference/provider-artifacts.md.

Two asymmetric plist behaviors are carried over deliberately, because both
are in bash today:
  - START (`check_available`) fails loudly when the plist is missing
    (`start_llamacpp`'s `echo ... >&2; exit 1`).
  - STOP (`stop_and_wait`) is a silent no-op when the plist is missing —
    `stop_all_local`'s branch is guarded by `[ -f ~/Library/LaunchAgents/
    local.llamacpp.server.plist ]`, so it never runs, and never warns.
"""

from __future__ import annotations

from .. import launchd, probe
from .base import Backend, StartPlan

LLAMACPP_PORT = 8080
LLAMACPP_BASE = f"http://localhost:{LLAMACPP_PORT}"
LLAMACPP_HEALTH_URL = f"{LLAMACPP_BASE}/v1/models"
LLAMACPP_CHAT_URL = f"{LLAMACPP_BASE}/v1/chat/completions"
DEFAULT_MODEL = "local-llama"
ENV_VAR = "LLM_ISOLATE_LLAMACPP_MODEL"


class LlamaCppBackend(Backend):
    id = "llamacpp"
    occupancy_key = "llamacpp"
    env_var = ENV_VAR
    default_model = DEFAULT_MODEL
    health_url = LLAMACPP_HEALTH_URL
    chat_url = LLAMACPP_CHAT_URL
    # The ONE backend whose bash start branch ignores --solo: every other
    # provider's case arm guards its teardown with `[ -n "$SOLO" ] ||`,
    # while `llamacpp)` runs `stop_all_local llamacpp` unconditionally.
    respects_solo = False
    # bin/llm-restore-providers never restarts llamacpp (retired 2026-09-07).
    restore_action = "skip"

    def check_available(self) -> str | None:
        # bash: `[ ! -f ~/Library/LaunchAgents/local.llamacpp.server.plist ]
        # && { echo "llamacpp service not configured: ... missing" >&2; exit
        # 1; }` — a plist check, NOT a binary-on-PATH check (llama.cpp is
        # LaunchAgent-managed, there is no CLI to look for).
        if not launchd.LLAMACPP_PLIST.exists():
            return f"llamacpp service not configured: {launchd.LLAMACPP_PLIST} missing"
        return None

    def resolve(self, model: str | None, extra_args: tuple[str, ...]) -> StartPlan:
        return StartPlan(model=self._resolve_model(model), direct_url=LLAMACPP_CHAT_URL)

    def start(self, plan: StartPlan) -> None:
        # bash: `launchctl load -w ~/Library/.../local.llamacpp.server.plist
        # 2>/dev/null || true` then warmup_or_die.
        launchd.load(launchd.LLAMACPP_PLIST)
        self.warm(plan)

    def stop_and_wait(self) -> str | None:
        # Silent no-op on a missing plist — see the module docstring: bash's
        # stop_all_local SKIPS this branch entirely (`[ -f <plist> ]`) rather
        # than warning, which is the inverse of check_available()'s loud
        # failure on the START path. Both are intentional; preserve both.
        if not launchd.LLAMACPP_PLIST.exists():
            return None
        launchd.unload(launchd.LLAMACPP_PLIST)
        if probe.port_closed_within(LLAMACPP_HEALTH_URL, timeout=probe.STOP_WAIT_TIMEOUT):
            return None
        # Literal port number, matching bash's hardcoded warning text.
        return "llama.cpp still listening on port 8080"


LLAMACPP = LlamaCppBackend()
