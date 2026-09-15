"""Backend abstract base class and the plan it resolves down to.

Every concrete backend (ollama, omlx, mtplx, mlx_lm_server, llamacpp — each
added in a later task) subclasses `Backend`. Exact method names matter:
later tasks subclass this seeing only this file and its docstrings, not
the discussion that produced them, so every docstring here must be
self-sufficient.
"""

from __future__ import annotations

import os
import subprocess
import urllib.request
from abc import ABC, abstractmethod
from collections.abc import Callable
from dataclasses import dataclass, field

from ..envelope import LifecycleError
from ..probe import RESTORE_WAIT_TIMEOUT, WARMUP_TIMEOUT, wait_for_port_open, warmup


@dataclass(frozen=True)
class StartPlan:
    """The resolved, backend-specific plan for starting one model. Produced
    by `Backend.resolve()` (a pure function) and consumed by every other
    lifecycle method (`start`, `wait_ready`, `warm`, ...)."""

    model: str  # the envelope's "model" string
    direct_url: str
    argv: list[str] | None = None  # backend-private spawn argv (mlx_lm_server, mtplx)
    extra: dict[str, str] = field(default_factory=dict)


class Backend(ABC):
    """One local-provider backend's lifecycle: availability, resolving a
    start request into a plan, starting/waiting/warming it, stopping it,
    and restoring it after a benchmark releases exclusivity.

    Class attributes (set by each concrete subclass):
      id: the provider id (e.g. "ollama", "omlx", "mtplx").
      occupancy_key: the id of the physical process/port this backend
        shares with another id — equal to `id` unless shared (e.g. omlx
        and omlx-6bit share one process/port).
      env_var: the LLM_ISOLATE_*_MODEL env var this backend's model
        resolution honors, or None if it doesn't use one.
      default_model: the model to use when no explicit model is given and
        no env var is set, or None if there is no default.
      health_url: a cheap liveness-check URL for this backend.
      chat_url: the OpenAI-compatible chat-completions URL for this
        backend.
      respects_solo: whether solo=True leaves this backend's own occupant
        alone when it's a *different* backend being isolated. False only
        for llamacpp (a later task).
      cleanup_on_failure: whether a failed start should tear this backend
        back down. True only for mtplx (a later task).
      restore_action: what `restore()` should do after a benchmark run
        releases exclusivity — "restart", "stop", or "skip" (default).
    """

    id: str
    occupancy_key: str
    env_var: str | None
    default_model: str | None
    health_url: str
    chat_url: str
    respects_solo: bool = True
    cleanup_on_failure: bool = False
    restore_action: str = "skip"

    @abstractmethod
    def check_available(self) -> str | None:
        """Return a reason string if this backend is unavailable on this
        machine (e.g. a required binary is missing), or None if it's
        available. Must have no side effects."""
        ...

    @abstractmethod
    def resolve(self, model: str | None, extra_args: tuple[str, ...]) -> StartPlan:
        """Validate `model`/`extra_args` and resolve them into a
        `StartPlan`, raising LifecycleError on invalid input (e.g. no
        model resolvable and none required-default).

        PURE: must never have side effects (no subprocess calls, no
        teardown) — later tasks depend on this being safe to call before
        any teardown happens, so a bad request can be rejected before
        anything else is touched.
        """
        ...

    def already_serving(self, plan: StartPlan) -> bool:
        """True when this backend's live process is already serving
        `plan.model` (so start()/wait_ready() can be skipped and only
        warm() need run). Default: never — mtplx overrides in a later
        task to keep an already-loaded model instead of paying a full
        restart."""
        return False

    def replace_own_occupant(self, plan: StartPlan) -> None:  # noqa: B027 — intentional default no-op
        """Tear down this backend's own previous occupant (a different
        model on the same single-model-per-process backend) before
        starting `plan`. Default: no-op — mtplx overrides in a later
        task."""

    @abstractmethod
    def start(self, plan: StartPlan) -> None:
        """Start this backend serving `plan`."""
        ...

    def wait_ready(self, plan: StartPlan) -> None:  # noqa: B027 — intentional default no-op
        """Block until `plan` is ready to receive warmup traffic (e.g.
        the model is loaded and listed in /v1/models). Default: no-op —
        mtplx overrides in a later task."""

    def warm(self, plan: StartPlan) -> None:
        """Force `plan.model` into GPU/RAM with a 1-token chat completion,
        polling `self.health_url` for liveness first. Default
        implementation delegates to `probe.warmup`; concrete backends
        should not need to override this."""
        warmup(plan.direct_url, plan.model, health_url=self.health_url, timeout=WARMUP_TIMEOUT)

    @abstractmethod
    def stop_and_wait(self) -> str | None:
        """Stop this backend and wait for it to fully release its
        resources (port, GPU/RAM). Return a warning string if the stop
        didn't fully complete within its wait budget, or None on a clean
        stop."""
        ...

    def restore(self) -> None:  # noqa: B027 — intentional default no-op
        """Restore this backend to its normal (non-benchmark) state after
        a benchmark run releases exclusivity. Default: no-op — backends
        with restore_action="restart" override in later tasks."""

    def _restart_if_down(self, *, restart: Callable[[], object]) -> None:
        """Shared shape for a restore_action="restart" backend's restore():
        one one-shot 2s probe of self.health_url (matching bash's single
        `curl -m 2`, not a poll loop) — return immediately if already up;
        otherwise call `restart()` and poll self.health_url via
        `wait_for_port_open`, raising LifecycleError if it never comes
        back. ollama/omlx/llamacpp's restore() bodies were otherwise
        identical copies of this shape, differing only in how they
        restart (`launchd.kickstart`, `omlx start`, `launchd.load`)."""
        try:
            urllib.request.urlopen(self.health_url, timeout=2.0)  # noqa: S310 — localhost probe
            return
        except OSError:
            pass
        restart()
        if not wait_for_port_open(self.health_url, timeout=RESTORE_WAIT_TIMEOUT):
            raise LifecycleError(f"{self.id} did not come back up ({self.health_url})")

    def _resolve_model(
        self, explicit: str | None, *, required: bool = False, required_message: str = ""
    ) -> str:
        """The one place the explicit-arg > env-var > default precedence is
        implemented; every backend's resolve() must call this rather than
        reimplementing the precedence.

        Returns `explicit` if truthy, else `os.environ.get(self.env_var,
        "")` if `self.env_var` is set and holds a non-empty string, else
        `self.default_model` if that is not None, else "". If `required`
        is True and the resolved result is empty, raises LifecycleError
        with `required_message` (supplied by the caller, since only the
        caller knows the right error text for its own missing-model
        case).
        """
        if explicit:
            resolved = explicit
        elif self.env_var and os.environ.get(self.env_var):
            resolved = os.environ[self.env_var]
        elif self.default_model is not None:
            resolved = self.default_model
        else:
            resolved = ""
        if required and not resolved:
            raise LifecycleError(required_message)
        return resolved


class PidfileTrackedBackend(Backend):
    """Shared base for a backend that runs as a single plain backgrounded
    subprocess tracked by a pidfile (never a LaunchAgent) — mtplx and
    mlx_lm_server both fit this shape (see `pidproc.PidfileProcess`).

    Factors out the one field both backends otherwise duplicated
    verbatim: the Popen handle `start()` spawns, so `wait_ready()` can
    watch for the process dying mid-load. Safe as a plain instance
    attribute (each concrete subclass is a module-level singleton) only
    because these backends are single-occupancy — there is never more
    than one concurrent isolate() call in flight for a given backend.
    """

    def __init__(self) -> None:
        self._proc: subprocess.Popen | None = None
