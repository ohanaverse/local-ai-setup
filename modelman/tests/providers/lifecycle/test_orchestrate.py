"""Orchestration-level tests: the cross-backend control flow in
orchestrate.py, plus the two bug fixes Tasks 4/5 built into the backends and
this layer is responsible for actually surfacing.

Per-backend internals (mtplx's registry-ambiguity resolve, mlx_lm_server's
target/draft validation itself, omlx's stop command, ...) are covered in
tests/providers/lifecycle/backends/; what is proven here is the wiring.
"""

from unittest.mock import MagicMock, patch

import pytest

from modelman.providers.lifecycle import orchestrate
from modelman.providers.lifecycle.backends import BACKENDS
from modelman.providers.lifecycle.backends.base import StartPlan
from modelman.providers.lifecycle.backends.mtplx import MTPLX, MTPLX_DIRECT_URL
from modelman.providers.lifecycle.backends.omlx import OMLX_4BIT, OMLX_6BIT
from modelman.providers.lifecycle.envelope import LifecycleError

ORCH = "modelman.providers.lifecycle.orchestrate"


@pytest.fixture
def stub_stops():
    """Replace every backend's stop_and_wait with a clean-stop MagicMock.

    _stop_others() fans out over the real BACKENDS registry, so without this
    a test that reaches it would run `omlx stop`, signal real pids and poll
    real ports on the developer's machine."""
    with patch(f"{ORCH}._log"):  # keep the expected warnings off the test log
        patchers = [
            patch.object(backend, "stop_and_wait", MagicMock(return_value=None))
            for backend in BACKENDS.values()
        ]
        for patcher in patchers:
            patcher.start()
        try:
            yield {backend.id: backend.stop_and_wait for backend in BACKENDS.values()}
        finally:
            # Stop only the patchers this fixture started — patch.stopall()
            # would also kill any patcher another fixture or test started.
            for patcher in patchers:
                patcher.stop()


def _plan(model: str = "some/model", url: str = MTPLX_DIRECT_URL) -> StartPlan:
    return StartPlan(model=model, direct_url=url)


# --- isolate(): resolve phase, before any teardown ---------------------


def test_isolate_unknown_provider_returns_error_envelope_not_raise():
    """An unknown provider id must come back as an ok=False envelope, never
    a raise: every caller (benchmark isolation, local_control) branches on
    `ok`, and a traceback here would escape as an unhandled crash instead of
    a reported isolation failure."""
    with patch(f"{ORCH}._stop_others") as mock_stop_others:
        result = orchestrate.isolate("not-a-provider")
    assert result.ok is False
    assert "not-a-provider" in (result.error or "")
    mock_stop_others.assert_not_called()


def test_isolate_unavailable_backend_fails_without_teardown():
    """check_available() runs BEFORE teardown: a machine missing the
    provider's binary/plist must not have every other local provider torn
    down on the way to discovering that."""
    with (
        patch.object(
            BACKENDS["omlx"], "check_available", return_value="omlx binary not found on PATH"
        ),
        patch(f"{ORCH}._stop_others") as mock_stop_others,
        patch.object(BACKENDS["omlx"], "start") as mock_start,
    ):
        result = orchestrate.isolate("omlx")
    assert result.ok is False
    assert result.error == "omlx binary not found on PATH"
    mock_stop_others.assert_not_called()
    mock_start.assert_not_called()


def test_isolate_returns_envelope_when_check_available_raises():
    """check_available()'s contract is "reason string or None, never raises",
    but orchestrate.py's never-a-traceback invariant is stated
    unconditionally, so it must hold structurally rather than depend on
    every current and future backend honoring that contract. A backend whose
    availability probe throws must still yield an ok=False envelope —
    callers only branch on `ok`, so an escaping exception would surface as
    an unhandled crash."""
    with (
        patch.object(BACKENDS["omlx"], "check_available", side_effect=OSError("boom")),
        patch(f"{ORCH}._stop_others") as mock_stop_others,
        patch.object(BACKENDS["omlx"], "start") as mock_start,
    ):
        result = orchestrate.isolate("omlx")
    assert result.ok is False
    assert "boom" in (result.error or "")
    mock_stop_others.assert_not_called()
    mock_start.assert_not_called()


@pytest.mark.parametrize("provider_id", ["ollama", "omlx", "mlx_lm_server", "mtplx"])
def test_isolate_resolve_failure_returns_error_envelope_without_teardown(provider_id):
    """BUG FIX (b), proven at the orchestration level for EVERY backend.

    bash validates each provider's request only AFTER `stop_all_local`
    already tore down every other local provider, so an unsatisfiable
    request (mlx_lm_server with no target/draft being the real-world case)
    left the machine with nothing running and an error. orchestrate.isolate()
    resolves first, so a resolve() failure must produce an error envelope
    with NO backend's stop_and_wait() or start() ever called — including the
    sibling backends _stop_others would have torn down."""
    backend = BACKENDS[provider_id]
    stop_calls = []
    with (
        patch.object(backend, "check_available", return_value=None),
        patch.object(backend, "resolve", side_effect=LifecycleError("bad request")),
        patch.object(backend, "start") as mock_start,
    ):
        stoppers = [
            patch.object(
                other,
                "stop_and_wait",
                MagicMock(side_effect=lambda oid=other.id: stop_calls.append(oid)),
            )
            for other in BACKENDS.values()
        ]
        for p in stoppers:
            p.start()
        try:
            result = orchestrate.isolate(provider_id)
        finally:
            for p in stoppers:
                p.stop()
    assert result.ok is False
    assert result.error == "bad request"
    assert stop_calls == []
    mock_start.assert_not_called()


def test_isolate_mlx_lm_server_validates_before_teardown():
    """BUG FIX (b) against the REAL mlx_lm_server.resolve(), through the
    real isolate() entry point: no target/draft anywhere (no positional
    args, no env vars) must fail before _stop_others() runs. This is the
    exact invocation bash mishandled — it stopped ollama/omlx/mtplx first
    and only then discovered it had no pairing to serve."""
    with (
        patch(
            "modelman.providers.lifecycle.backends.mlx_lm_server.binaries.resolve_mlx_lm_bin",
            return_value="/fake/mlx_lm.server",
        ),
        patch.dict("os.environ", {}, clear=True),
        patch(f"{ORCH}._stop_others") as mock_stop_others,
        patch.object(BACKENDS["mlx_lm_server"], "start") as mock_start,
    ):
        result = orchestrate.isolate("mlx_lm_server", None)
    assert result.ok is False
    assert "requires target+draft" in (result.error or "")
    mock_stop_others.assert_not_called()
    mock_start.assert_not_called()


def test_isolate_registry_error_returns_envelope_not_traceback():
    """A corrupt registry.toml surfaces from a backend's resolve() as a
    RegistryError, NOT a LifecycleError — the catch-all must still turn it
    into an envelope, since callers parse `ok`, not exception types."""
    from modelman.registry import RegistryError

    with (
        patch.object(MTPLX, "check_available", return_value=None),
        patch.object(MTPLX, "resolve", side_effect=RegistryError("corrupt")),
        patch(f"{ORCH}._stop_others") as mock_stop_others,
        patch.object(MTPLX, "stop_and_wait") as mock_cleanup,
    ):
        result = orchestrate.isolate("mtplx")
    assert result.ok is False
    assert "corrupt" in (result.error or "")
    mock_stop_others.assert_not_called()
    mock_cleanup.assert_not_called()


# --- isolate(): teardown phase -----------------------------------------


def test_isolate_already_serving_warms_without_restarting():
    """The keep path: when the backend already serves the resolved model,
    stop every OTHER provider (keep=own occupancy_key) and only warm — never
    a stop + respawn + multi-minute model reload."""
    plan = _plan()
    with (
        patch.object(MTPLX, "check_available", return_value=None),
        patch.object(MTPLX, "resolve", return_value=plan),
        patch.object(MTPLX, "already_serving", return_value=True),
        patch(f"{ORCH}._stop_others") as mock_stop_others,
        patch.object(MTPLX, "start") as mock_start,
        patch.object(MTPLX, "wait_ready") as mock_wait_ready,
        patch.object(MTPLX, "warm") as mock_warm,
    ):
        result = orchestrate.isolate("mtplx", "some/model")
    mock_stop_others.assert_called_once_with(keep="mtplx")
    mock_start.assert_not_called()
    mock_wait_ready.assert_not_called()
    mock_warm.assert_called_once_with(plan)
    assert result.ok is True
    assert result.model == "some/model"
    assert result.direct_url == MTPLX_DIRECT_URL


def test_isolate_already_serving_solo_leaves_siblings_alone():
    """solo=True on the keep path must skip _stop_others entirely: starting
    a model modelman already has loaded must not tear down an unrelated
    provider running alongside it."""
    plan = _plan()
    with (
        patch.object(MTPLX, "check_available", return_value=None),
        patch.object(MTPLX, "resolve", return_value=plan),
        patch.object(MTPLX, "already_serving", return_value=True),
        patch(f"{ORCH}._stop_others") as mock_stop_others,
        patch.object(MTPLX, "warm"),
    ):
        result = orchestrate.isolate("mtplx", "some/model", solo=True)
    mock_stop_others.assert_not_called()
    assert result.ok is True


def test_isolate_restart_path_stops_siblings_and_own_occupant():
    """The full restart path (non-solo, model not already served): stop the
    siblings AND this backend's own occupant. replace_own_occupant() is a
    no-op for every backend but mtplx, which is single-model-per-process and
    whose start() does not stop a predecessor — bash reached that teardown
    through a keep-less stop-all; here it goes through the backend's own
    method, which surfaces the real failure reason instead of a generic
    'port still answering' 10s later."""
    plan = _plan("org/new")
    with (
        patch.object(MTPLX, "check_available", return_value=None),
        patch.object(MTPLX, "resolve", return_value=plan),
        patch.object(MTPLX, "already_serving", return_value=False),
        patch(f"{ORCH}._stop_others") as mock_stop_others,
        patch.object(MTPLX, "replace_own_occupant") as mock_replace,
        patch.object(MTPLX, "start") as mock_start,
        patch.object(MTPLX, "wait_ready") as mock_wait_ready,
        patch.object(MTPLX, "warm") as mock_warm,
    ):
        result = orchestrate.isolate("mtplx", "org/new")
    mock_replace.assert_called_once_with(plan)
    mock_stop_others.assert_called_once_with(keep="mtplx")
    mock_start.assert_called_once_with(plan)
    mock_wait_ready.assert_called_once_with(plan)
    mock_warm.assert_called_once_with(plan)
    assert result.ok is True


def test_isolate_non_solo_stops_siblings_before_own_occupant():
    """Order matters, so pin it: the siblings go down first, then this
    backend's own occupant, then start(). Running replace_own_occupant()
    before _stop_others() would hand mtplx's freed port 8003 back over a
    window where the siblings still hold GPU/RAM, and start()'s port poll
    would be racing a teardown that hasn't happened yet."""
    plan = _plan("org/new")
    calls = []
    with (
        patch.object(MTPLX, "check_available", return_value=None),
        patch.object(MTPLX, "resolve", return_value=plan),
        patch.object(MTPLX, "already_serving", return_value=False),
        patch(f"{ORCH}._stop_others", side_effect=lambda **kw: calls.append("stop_others")),
        patch.object(
            MTPLX, "replace_own_occupant", side_effect=lambda p: calls.append("replace_own")
        ),
        patch.object(MTPLX, "start", side_effect=lambda p: calls.append("start")),
        patch.object(MTPLX, "wait_ready"),
        patch.object(MTPLX, "warm"),
    ):
        result = orchestrate.isolate("mtplx", "org/new")
    assert calls == ["stop_others", "replace_own", "start"]
    assert result.ok is True


def test_isolate_non_solo_own_occupant_stop_failure_is_not_fatal():
    """Non-solo only: a failed own-occupant stop must NOT fail the isolate.

    bash reached mtplx's own teardown through a keep-less stop-all — a
    backgrounded subshell whose failure was swallowed — and `mtplx stop`
    exits non-zero when nothing is listening at all. Failing here would
    break every isolate of a provider that simply isn't running yet
    (`modelman benchmark` against an mtplx target on a cold machine).
    start()'s own port-closed poll is the real gate on whether the port is
    free."""
    plan = _plan("org/new")
    with (
        patch.object(MTPLX, "check_available", return_value=None),
        patch.object(MTPLX, "resolve", return_value=plan),
        patch.object(MTPLX, "already_serving", return_value=False),
        patch(f"{ORCH}._stop_others"),
        patch.object(
            MTPLX,
            "replace_own_occupant",
            side_effect=LifecycleError("No MTPLX server is listening on port 8003."),
        ),
        patch.object(MTPLX, "start") as mock_start,
        patch.object(MTPLX, "wait_ready"),
        patch.object(MTPLX, "warm"),
    ):
        result = orchestrate.isolate("mtplx", "org/new")
    mock_start.assert_called_once_with(plan)
    assert result.ok is True


def test_isolate_solo_restart_stops_only_own_occupant():
    """solo=True on the restart path must replace only this backend's own
    occupant and never call _stop_others — the whole point of the
    same-provider-only local-model lifecycle."""
    plan = _plan("org/new")
    with (
        patch.object(MTPLX, "check_available", return_value=None),
        patch.object(MTPLX, "resolve", return_value=plan),
        patch.object(MTPLX, "already_serving", return_value=False),
        patch.object(MTPLX, "replace_own_occupant") as mock_replace,
        patch(f"{ORCH}._stop_others") as mock_stop_others,
        patch.object(MTPLX, "start") as mock_start,
        patch.object(MTPLX, "wait_ready"),
        patch.object(MTPLX, "warm"),
    ):
        result = orchestrate.isolate("mtplx", "org/new", solo=True)
    mock_replace.assert_called_once_with(plan)
    mock_stop_others.assert_not_called()
    mock_start.assert_called_once_with(plan)
    assert result.ok is True


def test_isolate_solo_ignored_for_llamacpp():
    """llamacpp is the one backend whose bash start branch runs
    `stop_all_local llamacpp` unconditionally — respects_solo=False, so even
    solo=True must fall through to the sibling teardown."""
    with (
        patch.object(BACKENDS["llamacpp"], "check_available", return_value=None),
        patch(f"{ORCH}._stop_others") as mock_stop_others,
        patch.object(BACKENDS["llamacpp"], "start"),
        patch.object(BACKENDS["llamacpp"], "warm"),
    ):
        result = orchestrate.isolate("llamacpp", "local-llama", solo=True)
    mock_stop_others.assert_called_once_with(keep="llamacpp")
    assert result.ok is True


def test_isolate_solo_replace_failure_prevents_start():
    """A replace_own_occupant() failure must abort before start(): falling
    through to spawn a replacement into a port the predecessor still holds
    is exactly the silent wrong-model measurement this teardown exists to
    prevent."""
    plan = _plan("org/new")
    with (
        patch.object(MTPLX, "check_available", return_value=None),
        patch.object(MTPLX, "resolve", return_value=plan),
        patch.object(MTPLX, "already_serving", return_value=False),
        patch.object(
            MTPLX,
            "replace_own_occupant",
            side_effect=LifecycleError("mtplx still listening on port 8003"),
        ),
        patch(f"{ORCH}._stop_others"),
        patch.object(MTPLX, "start") as mock_start,
    ):
        result = orchestrate.isolate("mtplx", "org/new", solo=True)
    mock_start.assert_not_called()
    assert result.ok is False
    assert result.error == "mtplx still listening on port 8003"


# --- isolate(): failure cleanup ----------------------------------------


def test_isolate_teardown_failure_does_not_tear_down_the_target():
    """`started` is set only AFTER teardown succeeds. A teardown failure
    stopped (or failed to stop) the OTHER providers — it says nothing about
    this backend's health, so it must not trigger cleanup_on_failure
    teardown of a server that is running fine."""
    plan = _plan()
    with (
        patch.object(MTPLX, "check_available", return_value=None),
        patch.object(MTPLX, "resolve", return_value=plan),
        patch.object(MTPLX, "already_serving", return_value=True),
        patch(f"{ORCH}._stop_others", side_effect=LifecycleError("stop-all blew up")),
        patch.object(MTPLX, "warm") as mock_warm,
        patch.object(MTPLX, "stop_and_wait") as mock_cleanup,
    ):
        result = orchestrate.isolate("mtplx", "some/model")
    mock_warm.assert_not_called()
    mock_cleanup.assert_not_called()
    assert result.ok is False


def test_isolate_tears_down_wedged_server_on_warmup_failure():
    """cleanup_on_failure: a server that answers /v1/models but hangs on
    chat completions must be torn down, not left running. Otherwise every
    future isolate() re-probes it as 'serving', retries the same doomed
    warmup and fails again with no path to recovery short of a manual
    stop."""
    plan = _plan()
    with (
        patch.object(MTPLX, "check_available", return_value=None),
        patch.object(MTPLX, "resolve", return_value=plan),
        patch.object(MTPLX, "already_serving", return_value=True),
        patch(f"{ORCH}._stop_others"),
        patch.object(MTPLX, "warm", side_effect=LifecycleError("failed to warm up")),
        patch.object(MTPLX, "stop_and_wait") as mock_cleanup,
    ):
        result = orchestrate.isolate("mtplx", "some/model")
    mock_cleanup.assert_called_once()
    assert result.ok is False
    assert "failed to warm up" in (result.error or "")


def test_isolate_tears_down_a_half_started_server_when_wait_ready_fails():
    """A half-started isolate (process spawned, model load failed) must stop
    the server it spawned. Otherwise the orphan holds its port and GPU/RAM
    while local_control clears the running flag on failure, so nothing ever
    tears it down — exactly the one-local-model invariant isolation
    exists to enforce."""
    plan = _plan()
    with (
        patch.object(MTPLX, "check_available", return_value=None),
        patch.object(MTPLX, "resolve", return_value=plan),
        patch.object(MTPLX, "already_serving", return_value=False),
        patch.object(MTPLX, "replace_own_occupant"),
        patch(f"{ORCH}._stop_others"),
        patch.object(MTPLX, "start"),
        patch.object(MTPLX, "wait_ready", side_effect=LifecycleError("timed out")),
        patch.object(MTPLX, "warm"),
        patch.object(MTPLX, "stop_and_wait") as mock_cleanup,
    ):
        result = orchestrate.isolate("mtplx", "some/model")
    mock_cleanup.assert_called_once()
    assert result.ok is False
    assert "timed out" in (result.error or "")


def test_isolate_does_not_tear_down_backends_without_cleanup_on_failure():
    """cleanup_on_failure is False for every backend but mtplx: ollama's
    daemon is multi-tenant and shared, so a failed warmup must NOT unload
    whatever else the daemon is serving for other callers."""
    plan = _plan("some:tag", "http://localhost:11434/v1/chat/completions")
    ollama = BACKENDS["ollama"]
    with (
        patch.object(ollama, "check_available", return_value=None),
        patch.object(ollama, "resolve", return_value=plan),
        patch(f"{ORCH}._stop_others"),
        patch.object(ollama, "start", side_effect=LifecycleError("failed to warm up")),
        patch.object(ollama, "stop_and_wait") as mock_cleanup,
    ):
        result = orchestrate.isolate("ollama", "some:tag")
    mock_cleanup.assert_not_called()
    assert result.ok is False


# --- stop() ------------------------------------------------------------


@pytest.mark.parametrize("provider_id", ["llamacpp", "totally-made-up"])
def test_stop_rejects_provider_outside_supported_ids(provider_id):
    """stop() gates on SUPPORTED_PROVIDER_IDS, not BACKENDS — llamacpp is a
    valid BACKENDS key (stop_all still tears it down) but bash's `stop` verb
    has no llamacpp case, so stopping it by name must be refused rather than
    silently reviving a retired provider's control path."""
    with patch.object(BACKENDS["llamacpp"], "stop_and_wait") as mock_stop:
        result = orchestrate.stop(provider_id)
    assert result.ok is False
    assert provider_id in (result.error or "")
    mock_stop.assert_not_called()


def test_stop_returns_ok_on_clean_stop():
    with patch.object(BACKENDS["omlx"], "stop_and_wait", return_value=None):
        result = orchestrate.stop("omlx")
    assert result.ok is True
    assert result.error is None


def test_stop_mtplx_reports_failure_when_port_never_closes():
    """BUG FIX (a), proven through the real dispatch. Task 5 fixed
    MtplxBackend.stop_and_wait() to poll for the port actually closing and
    return a warning when it doesn't; before that, any `mtplx stop` that
    exited 0 was reported as success even with port 8003 still bound.
    stop() must surface that warning as a hard ok=False — a caller about to
    start the next model on that port needs to know, and stderr is not part
    of the envelope it reads."""
    with patch.object(
        BACKENDS["mtplx"],
        "stop_and_wait",
        return_value="mtplx still listening on port 8003",
    ):
        result = orchestrate.stop("mtplx")
    assert result.ok is False
    assert result.error == "mtplx still listening on port 8003"


def test_stop_returns_envelope_when_backend_raises():
    """Even an unexpected exception out of a backend must come back as an
    envelope — stop() is on the same never-a-traceback contract as
    isolate()."""
    with patch.object(BACKENDS["omlx"], "stop_and_wait", side_effect=OSError("boom")):
        result = orchestrate.stop("omlx")
    assert result.ok is False
    assert "boom" in (result.error or "")


# --- _stop_others() / stop_all() ---------------------------------------


def test_stop_others_dedupes_shared_occupancy_key(stub_stops):
    """omlx and omlx-6bit are two registry ids for ONE daemon on ONE port.
    Stopping "both" would race `omlx stop` against itself, so exactly one of
    the two instances may be invoked."""
    orchestrate._stop_others()
    invoked = [stub_stops[bid].called for bid in ("omlx", "omlx-6bit")]
    assert invoked.count(True) == 1


def test_stop_others_covers_every_other_backend_including_llamacpp(stub_stops):
    """bash's stop_all_local has a llamacpp branch (guarded only by the
    plist's existence), so llamacpp must be torn down here too even though
    it is outside SUPPORTED_PROVIDER_IDS — a stale llama.cpp holding port
    8080 and GPU/RAM would skew every benchmark that followed."""
    orchestrate._stop_others()
    for provider_id in ("ollama", "mlx_lm_server", "mtplx", "llamacpp"):
        assert stub_stops[provider_id].called, provider_id


def test_stop_others_keep_excludes_by_occupancy_key_not_id(stub_stops):
    """keep is matched on occupancy_key: keeping "omlx" must also spare
    "omlx-6bit", since stopping the 6-bit id would halt the very daemon the
    4-bit model is loaded in."""
    orchestrate._stop_others(keep="omlx")
    assert not stub_stops["omlx"].called
    assert not stub_stops["omlx-6bit"].called
    assert stub_stops["ollama"].called


def test_stop_others_warns_but_never_raises_on_a_stuck_provider(stub_stops):
    """A timed-out stop is a warning, never fatal — bash's backgrounded
    subshells end in `true` for exactly this reason: one stuck provider must
    not abort the whole isolation."""
    stub_stops["ollama"].return_value = "ollama still has models loaded"
    stub_stops["mtplx"].side_effect = OSError("mtplx exploded")
    orchestrate._stop_others()  # must not raise
    assert stub_stops["omlx"].called or stub_stops["omlx-6bit"].called


def test_stop_all_always_reports_ok(stub_stops):
    """stop_all's own warnings never fail the call (bash's `stop-all` verb
    prints ok:true unconditionally) — `modelman stop --all` must not error
    out because one provider was slow to let go."""
    stub_stops["ollama"].return_value = "ollama still has models loaded"
    result = orchestrate.stop_all()
    assert result.ok is True
    assert result.provider == "stop-all"


def test_stop_all_keep_excludes_by_occupancy_key(stub_stops):
    """stop_all(keep=...) shares _stop_others with isolate()'s teardown, so
    the same occupancy-key semantics apply: keep="omlx" spares both omlx
    ids."""
    orchestrate.stop_all(keep="omlx")
    assert not stub_stops["omlx"].called
    assert not stub_stops["omlx-6bit"].called
    assert stub_stops["llamacpp"].called


def test_stop_all_keep_resolves_provider_id_to_occupancy_key(stub_stops):
    """stop_all(), like isolate()/stop(), takes a PROVIDER ID for `keep` —
    not an occupancy_key — and resolves it internally. "omlx-6bit"'s
    occupancy_key is "omlx" (one daemon, one port, two registry ids), so
    keeping it must keep BOTH omlx variants. Passing the raw id through to
    _stop_others() unresolved — as an earlier version did from the CLI
    layer — matched no occupancy key at all and therefore STOPPED omlx:
    the exact opposite of what was asked."""
    orchestrate.stop_all(keep="omlx-6bit")
    assert not stub_stops["omlx"].called
    assert not stub_stops["omlx-6bit"].called
    assert stub_stops["ollama"].called


def test_stop_all_rejects_unknown_keep_id_instead_of_keeping_nothing(stub_stops):
    """An unrecognized `keep` id must fail fast with ok=False before any
    teardown runs, not match no occupancy key and silently stop everything
    while still reporting ok=true — a typo could otherwise kill the very
    provider the caller meant to protect, with no signal at all."""
    result = orchestrate.stop_all(keep="bogus-id")
    assert result.ok is False
    assert "bogus-id" in result.error
    for backend_id in ("ollama", "omlx", "omlx-6bit", "mlx_lm_server", "mtplx", "llamacpp"):
        assert not stub_stops[backend_id].called, backend_id


# --- restore() ---------------------------------------------------------


def test_restore_runs_restart_and_stop_actions_but_never_skip_actions():
    """restore() is driven entirely off each backend's restore_action, read
    from BACKENDS — no hardcoded provider list (which would drift the moment
    a backend is added). "restart" backends come back up, "stop" backends
    (never part of the standing baseline) are torn down, "skip" backends
    (retired llamacpp, the duplicate omlx-6bit id) are not touched at all."""
    with (
        patch.object(BACKENDS["ollama"], "restore") as mock_ollama_restore,
        patch.object(OMLX_4BIT, "restore") as mock_omlx_restore,
        patch.object(OMLX_6BIT, "restore") as mock_omlx6_restore,
        patch.object(
            BACKENDS["mlx_lm_server"], "stop_and_wait", return_value=None
        ) as mock_mlx_stop,
        patch.object(BACKENDS["mtplx"], "stop_and_wait", return_value=None) as mock_mtplx_stop,
        patch.object(BACKENDS["llamacpp"], "restore") as mock_llamacpp_restore,
        patch.object(BACKENDS["llamacpp"], "stop_and_wait") as mock_llamacpp_stop,
        patch(f"{ORCH}._restore_litellm", return_value=None),
    ):
        result = orchestrate.restore()
    mock_ollama_restore.assert_called_once()
    mock_omlx_restore.assert_called_once()
    mock_omlx6_restore.assert_not_called()
    mock_mlx_stop.assert_called_once()
    mock_mtplx_stop.assert_called_once()
    mock_llamacpp_restore.assert_not_called()
    mock_llamacpp_stop.assert_not_called()
    assert result.ok is True
    assert result.provider == "restore"


def test_restore_restart_failure_fails_the_aggregate():
    """A provider that is supposed to be part of the standing baseline and
    did not come back up is a real failure — the machine is left without a
    service the user expects to be running."""
    with (
        patch.object(
            BACKENDS["ollama"], "restore", side_effect=LifecycleError("ollama did not come back up")
        ),
        patch.object(OMLX_4BIT, "restore"),
        patch.object(BACKENDS["mlx_lm_server"], "stop_and_wait", return_value=None),
        patch.object(BACKENDS["mtplx"], "stop_and_wait", return_value=None),
        patch(f"{ORCH}._restore_litellm", return_value=None),
    ):
        result = orchestrate.restore()
    assert result.ok is False
    assert "ollama did not come back up" in (result.error or "")


def test_restore_stop_action_failure_never_fails_the_aggregate():
    """mlx_lm_server/mtplx are stopped, not restored — a stubborn stop there
    must not fail a restore whose real job (ollama/omlx/litellm back up)
    succeeded."""
    with (
        patch.object(BACKENDS["ollama"], "restore"),
        patch.object(OMLX_4BIT, "restore"),
        patch.object(
            BACKENDS["mlx_lm_server"],
            "stop_and_wait",
            return_value="mlx_lm_server still listening on port 8001",
        ),
        patch.object(BACKENDS["mtplx"], "stop_and_wait", side_effect=OSError("mtplx exploded")),
        patch(f"{ORCH}._restore_litellm", return_value=None),
    ):
        result = orchestrate.restore()
    assert result.ok is True


def test_restore_litellm_failure_fails_the_aggregate():
    """The LiteLLM proxy is not a Backend but is part of the same baseline:
    a benchmark that leaves it down breaks every agent routing through
    localhost:4000, so its failure must surface."""
    with (
        patch.object(BACKENDS["ollama"], "restore"),
        patch.object(OMLX_4BIT, "restore"),
        patch.object(BACKENDS["mlx_lm_server"], "stop_and_wait", return_value=None),
        patch.object(BACKENDS["mtplx"], "stop_and_wait", return_value=None),
        patch(f"{ORCH}._restore_litellm", return_value="litellm did not come back up"),
    ):
        result = orchestrate.restore()
    assert result.ok is False
    assert "litellm did not come back up" in (result.error or "")


def test_restore_litellm_missing_plist_is_a_non_fatal_skip(tmp_path):
    """bash's restart_launchd treats a missing plist as "not configured;
    skipping", NOT an error — unlike llamacpp's START path, which fails
    loudly on a missing plist. A machine with no LiteLLM installed must not
    have every benchmark end in a restore failure."""
    missing = tmp_path / "nope.plist"
    with (
        patch(f"{ORCH}.launchd.LITELLM_PLIST", missing),
        patch(f"{ORCH}.probe.wait_for_port_open") as mock_wait,
        patch(f"{ORCH}.launchd.load") as mock_load,
    ):
        # urlopen is stubbed suite-wide to raise (hermetic), so the one-shot
        # liveness probe reads as "down" and the plist check is reached.
        assert orchestrate._restore_litellm() is None
    mock_load.assert_not_called()
    mock_wait.assert_not_called()


def test_restore_litellm_treats_an_http_error_status_as_up(tmp_path):
    """LiteLLM's /v1/models answers 401 without a key, and urlopen raises
    HTTPError for that — but the proxy IS up, and bash's `curl -s -m 2`
    exits 0 on a 401 and returned early. Misreading it as down makes every
    restore bounce a healthy proxy and kill in-flight agent traffic through
    localhost:4000 (caught live during this task's verification)."""
    import urllib.error

    plist = tmp_path / "local.litellm.proxy.plist"
    plist.write_text("")
    http_401 = urllib.error.HTTPError(
        "http://localhost:4000/v1/models", 401, "Unauthorized", {}, None
    )
    with (
        patch(f"{ORCH}.launchd.LITELLM_PLIST", plist),
        patch(f"{ORCH}.urllib.request.urlopen", side_effect=http_401),
        patch(f"{ORCH}.launchd.load") as mock_load,
        patch(f"{ORCH}.probe.wait_for_port_open") as mock_wait,
    ):
        assert orchestrate._restore_litellm() is None
    mock_load.assert_not_called()
    mock_wait.assert_not_called()


def test_restore_litellm_loads_plist_and_waits_when_down(tmp_path):
    plist = tmp_path / "local.litellm.proxy.plist"
    plist.write_text("")
    with (
        patch(f"{ORCH}.launchd.LITELLM_PLIST", plist),
        patch(f"{ORCH}.launchd.load") as mock_load,
        patch(f"{ORCH}.probe.wait_for_port_open", return_value=True) as mock_wait,
    ):
        assert orchestrate._restore_litellm() is None
    mock_load.assert_called_once_with(plist)
    mock_wait.assert_called_once()
