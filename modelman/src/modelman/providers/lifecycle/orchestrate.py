"""Cross-backend lifecycle orchestration: isolate / stop / stop-all / restore.

The in-process replacement for `bin/llm-isolate-provider` and
`bin/llm-restore-providers`. Everything here is driven off the `BACKENDS`
registry — no function below hardcodes a provider list, so registering a new
backend wires it into isolate/stop-all/restore at once.

Two invariants this module owns:

1. **Validate before teardown.** `isolate()` resolves the request (registry
   lookup, availability check, `Backend.resolve()`) BEFORE any teardown runs,
   for EVERY backend. bash does the opposite: each case arm runs
   `stop_all_local <provider>` first and only then validates, so an
   unsatisfiable request (e.g. `mlx_lm_server` with no target/draft) tore
   down every other local provider before failing. Keeping that ordering
   here is the structural fix — `Backend.resolve()` is documented as pure
   precisely so this is safe.

2. **Never a traceback.** Every public function returns a `LifecycleResult`
   envelope; nothing here raises past its own boundary. Callers
   (`modelman.benchmark.isolation`, `local_control.py`) branch on `ok`.
"""

from __future__ import annotations

import contextlib
import sys
import urllib.error
import urllib.request
from collections.abc import Callable
from concurrent.futures import ThreadPoolExecutor

from . import launchd, probe
from .backends import BACKENDS, SUPPORTED_PROVIDER_IDS
from .backends.base import Backend
from .envelope import LifecycleResult

# Replaces bash's "[llm-restore-providers]" prefix: these diagnostics come
# from modelman's own Python now, not from a shell script.
RESTORE_LOG_PREFIX = "[modelman provider restore]"


def _log(message: str) -> None:
    print(message, file=sys.stderr)


def _distinct_backends(keep: str = "") -> list[Backend]:
    """Every backend except those whose `occupancy_key` matches `keep`,
    deduped by `occupancy_key`.

    Dedup matters because `omlx` and `omlx-6bit` are two registry ids for
    ONE daemon on ONE port: stopping "both" would race `omlx stop` against
    itself, and restoring both would start the same daemon twice. Which of
    the two ids wins is arbitrary but must be deterministic, so this takes
    the first in `BACKENDS` iteration order (insertion order — the 4-bit
    instance, which is also the one carrying `restore_action="restart"`).
    """
    seen: set[str] = set()
    picked: list[Backend] = []
    for backend in BACKENDS.values():
        if backend.occupancy_key == keep or backend.occupancy_key in seen:
            continue
        seen.add(backend.occupancy_key)
        picked.append(backend)
    return picked


def _run_parallel(jobs: list[Callable[[], None]]) -> None:
    """Run independent per-backend jobs concurrently — matching bash's
    backgrounded stop/restart subshells, where isolating costs the slowest
    provider rather than the sum of all of them."""
    if not jobs:
        return
    with ThreadPoolExecutor(max_workers=len(jobs)) as pool:
        for future in [pool.submit(job) for job in jobs]:
            future.result()


def _stop_others(keep: str = "") -> None:
    """Stop every local provider except the one named by `keep`.

    The in-process replacement for bash's `stop_all_local`, and the shared
    teardown primitive both `isolate()` and `stop_all()` use (exactly as
    bash's `stop_all_local` is shared between its case arms and its
    `stop-all` verb).

    `keep` is matched against `occupancy_key`, not `id`, so keeping "omlx"
    also keeps "omlx-6bit" — they are one daemon. `llamacpp` IS included
    here even though it is outside `SUPPORTED_PROVIDER_IDS`, because bash's
    `stop_all_local` stops it too when its plist exists.

    A stop that times out (or raises) is a warning on stderr, never a
    failure: bash's subshells end in `true` for exactly this reason — one
    stuck provider must not abort the whole isolation.
    """

    def _stop_one(backend: Backend) -> Callable[[], None]:
        def _run() -> None:
            try:
                warning = backend.stop_and_wait()
            except Exception as exc:  # noqa: BLE001 — a stop is never fatal here
                _log(f"warning: {backend.id} stop failed: {exc}")
                return
            if warning:
                _log(f"warning: {warning}")

        return _run

    _run_parallel([_stop_one(backend) for backend in _distinct_backends(keep)])


def isolate(
    provider_id: str,
    model: str | None = None,
    *,
    extra_args: tuple[str, ...] = (),
    solo: bool = False,
) -> LifecycleResult:
    """Stop every other local provider and start the requested one.

    `solo=True` restricts teardown to this backend's OWN occupant, leaving
    sibling providers alone — modelman's same-provider-only local-model
    lifecycle (`local_control.py`). Never passed by `modelman benchmark`,
    which still needs full exclusivity for clean measurement.

    Phases, in this order for every backend:
      1. RESOLVE — look the backend up, check availability, resolve the
         plan. All pure; no teardown has happened yet, so an unknown
         provider / unavailable backend / invalid request fails without
         disturbing anything (invariant 1 in the module docstring).
      2. TEARDOWN — see the branches below.
      3. START — start, wait for readiness, warm.
    """
    backend = BACKENDS.get(provider_id)
    if backend is None:
        return LifecycleResult(provider_id, model or "", "", False, f"unknown provider: {provider_id}")

    started = False
    plan = None
    try:
        # check_available() is INSIDE the try on purpose. Its contract is
        # "reason string or None, never raises", but the module's
        # never-a-traceback invariant must hold structurally rather than
        # depend on every present and future backend honoring that — a
        # backend whose availability probe throws must still produce an
        # envelope, not escape as a raw traceback to callers that only
        # branch on `ok`.
        reason = backend.check_available()
        if reason is not None:
            return LifecycleResult(provider_id, model or "", "", False, reason)
        plan = backend.resolve(model, tuple(extra_args))
        if backend.already_serving(plan):
            # Keep semantics, matching bash's `stop_all_local $provider`
            # (which skips the target): re-isolating the model this backend
            # already serves pays only warmup, not a full stop + respawn +
            # multi-minute reload. solo skips even that, leaving siblings
            # untouched.
            if not solo:
                _stop_others(keep=backend.occupancy_key)
            # From here, `started` means "a live server exists that this
            # call is responsible for tearing down on failure" — not
            # literally "this call spawned it". A wedged server (answers
            # /v1/models but hangs or errors on chat completions) must not
            # be left running for the next isolate() call to retry warmup
            # against forever with no path to recovery. Set only AFTER
            # teardown succeeds: a failure there stopped the OTHER
            # providers, not this one, so it must not trigger teardown here.
            started = True
            backend.warm(plan)
        else:
            if solo and backend.respects_solo:
                # replace_own_occupant() raises immediately on a non-None
                # stop warning rather than falling through to start()'s own
                # port poll, which would fail ~10s later with a generic
                # "port still answering" message that hides the real cause.
                backend.replace_own_occupant(plan)
            else:
                _stop_others(keep=backend.occupancy_key)
                # Then this backend's OWN occupant. A documented no-op for
                # every backend but mtplx (ollama/omlx reuse their daemon;
                # mlx_lm_server self-replaces inside start()), which is
                # exactly bash's `stop_all_local <self>` keep behavior.
                # mtplx is the exception: it is single-model-per-process AND
                # its start() does not stop a predecessor, so bash reached
                # that teardown through a KEEP-LESS stop-all instead — i.e.
                # through a backgrounded subshell whose failure was
                # swallowed. Hence the suppress: `mtplx stop` exits non-zero
                # when nothing is listening, and an isolate on a machine
                # where mtplx simply isn't running yet must not fail because
                # of it. start()'s own port-closed poll is the real gate on
                # whether the predecessor actually let go. (The solo branch
                # above deliberately does NOT suppress: there the caller is
                # replacing a known occupant, and the real stop failure is
                # more useful than a generic port-timeout 10s later.)
                with contextlib.suppress(Exception):
                    backend.replace_own_occupant(plan)
            backend.start(plan)
            started = True
            backend.wait_ready(plan)
            backend.warm(plan)
    except Exception as exc:  # noqa: BLE001 — envelope contract, never a traceback
        # RegistryError/TOMLDecodeError from a backend's registry lookup are
        # NOT LifecycleError; anything escaping here must still come back as
        # an envelope, since callers parse `ok`, not exceptions.
        if started and backend.cleanup_on_failure:
            # Teardown: the server is up but load/warmup failed — a running
            # orphan holding its port and GPU/RAM breaks the exclusivity
            # this function exists to enforce. Best-effort: a cleanup
            # failure must not mask the original error.
            with contextlib.suppress(Exception):
                backend.stop_and_wait()
        return LifecycleResult(provider_id, model or "", backend.chat_url, False, str(exc))
    return LifecycleResult(provider_id, plan.model, plan.direct_url, True, None)


def stop(provider_id: str) -> LifecycleResult:
    """Stop exactly one local provider, leaving every other one running.

    Gated on `SUPPORTED_PROVIDER_IDS`, not `BACKENDS`, so `llamacpp` is
    rejected here even though it is a valid `BACKENDS` key — bash's `stop`
    verb likewise has no `llamacpp` case.

    Stricter than `stop_all()` by design: a timed-out stop is reported as a
    real failure rather than a warning, because a caller replacing a
    single-port provider's occupant needs to know whether the port actually
    closed before starting the next model on it (stderr is not part of the
    envelope such callers read).
    """
    if provider_id not in SUPPORTED_PROVIDER_IDS:
        # Two distinct cases, distinguishable to the caller: a registered
        # backend that `stop` nonetheless refuses (llamacpp), versus an id
        # nothing knows about at all.
        if provider_id in BACKENDS:
            return LifecycleResult(
                provider_id, "", "", False, f"provider not supported for stop: {provider_id}"
            )
        return LifecycleResult(provider_id, "", "", False, f"unknown provider: {provider_id}")
    backend = BACKENDS[provider_id]
    try:
        warning = backend.stop_and_wait()
    except Exception as exc:  # noqa: BLE001 — envelope contract, never a traceback
        return LifecycleResult(provider_id, "", backend.chat_url, False, str(exc))
    if warning is not None:
        _log(f"warning: {warning}")
        return LifecycleResult(provider_id, "", backend.chat_url, False, warning)
    return LifecycleResult(provider_id, "", backend.chat_url, True, None)


def stop_all(keep: str = "") -> LifecycleResult:
    """Stop every local provider, optionally keeping one occupancy domain's
    models loaded. Always ok=True — a stop-all's own warnings never fail the
    call (bash's `stop-all` verb prints `"ok": true` unconditionally too)."""
    _stop_others(keep)
    return LifecycleResult("stop-all", "", "", True, None)


def _restore_litellm() -> str | None:
    """Restore the LiteLLM proxy. Not a `Backend` (it is not a model
    provider), so it rides along as one more task in `restore()`'s pool.

    A missing plist is a non-fatal skip, matching bash's `restart_launchd`
    — unlike llamacpp's START path, which fails loudly on a missing plist.
    """
    try:
        # bash: `curl -s -m 2 "$url" >/dev/null 2>&1 && return 0` — one
        # 2s probe, not a poll loop.
        urllib.request.urlopen(launchd.LITELLM_HEALTH_URL, timeout=2.0)  # noqa: S310 — localhost
        return None
    except urllib.error.HTTPError:
        # ANSWERED, with an error status — the proxy is up. LiteLLM's
        # /v1/models returns 401 without a key, and `curl` exits 0 on a 401,
        # so bash returned early here. HTTPError is an OSError subclass, so
        # this must be caught BEFORE the OSError below (same ordering rule
        # probe.py documents) — otherwise every restore would needlessly
        # bounce a perfectly healthy proxy and kill in-flight agent traffic.
        return None
    except OSError:
        pass
    if not launchd.LITELLM_PLIST.exists():
        _log(f"{RESTORE_LOG_PREFIX} litellm not configured ({launchd.LITELLM_PLIST} missing); skipping")
        return None
    _log(f"{RESTORE_LOG_PREFIX} restarting litellm...")
    launchd.load(launchd.LITELLM_PLIST)
    if not probe.wait_for_port_open(
        launchd.LITELLM_HEALTH_URL, timeout=probe.RESTORE_WAIT_TIMEOUT
    ):
        return f"litellm did not come back up ({launchd.LITELLM_HEALTH_URL})"
    return None


def restore() -> LifecycleResult:
    """Bring local providers back to their standing baseline after a
    benchmark run releases exclusivity.

    Driven entirely off each backend's `restore_action`, read from
    `BACKENDS` — a hardcoded provider list here would drift the moment a
    backend is added:
      - "restart": `backend.restore()`; a failure fails the aggregate.
      - "stop": `backend.stop_and_wait()` — mlx_lm_server and mtplx are
        never part of the standing baseline, so their entry here is a stop,
        not a restart, and it must run on every invocation (a benchmark
        isolates per target but restores once, so this is the only place the
        last-isolated one gets torn down). Never fails the aggregate.
      - "skip": not called at all.
    Plus the LiteLLM proxy, which is not a Backend — see `_restore_litellm`.
    """
    errors: list[str] = []

    def _job(fn: Callable[[], str | None], *, counts: bool) -> Callable[[], None]:
        def _run() -> None:
            try:
                error = fn()
            except Exception as exc:  # noqa: BLE001 — aggregate, never a traceback
                error = str(exc)
            if error:
                _log(f"{RESTORE_LOG_PREFIX} {error}")
                if counts:
                    errors.append(error)

        return _run

    def _restart(backend: Backend) -> Callable[[], str | None]:
        def _run() -> str | None:
            # Backend.restore()'s contract is raise-on-failure, not
            # return-an-error — discard whatever it returns rather than
            # letting a non-None value masquerade as an error string.
            backend.restore()
            return None

        return _run

    def _stop_quietly(backend: Backend) -> Callable[[], str | None]:
        def _run() -> str | None:
            warning = backend.stop_and_wait()
            if warning:
                _log(f"{RESTORE_LOG_PREFIX} warning: {warning}")
            return None  # a "stop" task never fails the aggregate

        return _run

    jobs: list[Callable[[], None]] = []
    for backend in _distinct_backends():
        if backend.restore_action == "restart":
            jobs.append(_job(_restart(backend), counts=True))
        elif backend.restore_action == "stop":
            jobs.append(_job(_stop_quietly(backend), counts=False))
    jobs.append(_job(_restore_litellm, counts=True))

    _run_parallel(jobs)

    if errors:
        _log(f"{RESTORE_LOG_PREFIX} one or more providers failed to restore")
        return LifecycleResult("restore", "", "", False, "; ".join(errors))
    _log(f"{RESTORE_LOG_PREFIX} providers restored")
    return LifecycleResult("restore", "", "", True, None)
