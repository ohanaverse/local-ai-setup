"""The generic, spec-driven process-server backend (mtplx, rec and sushi run on it).

Synthetic specs and fakes only: every probe, the binary lookup, the stop
command and the pidfile process are patched where `process_server` imports
them, and the registry is a scratch file named by WT_REGISTRY (conftest
already points WT_REGISTRY into tmp_path).
"""

from __future__ import annotations

import dataclasses
import os
import subprocess
import urllib.error
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from llmbench.providers.lifecycle import orchestrate, probe
from llmbench.providers.lifecycle.backends import BACKENDS, process_server
from llmbench.providers.lifecycle.backends.mtplx import MTPLX
from llmbench.providers.lifecycle.backends.process_server import (
    INSTANCE_GONE_TIMEOUT,
    SIGNAL_STOP_TIMEOUT,
    ProcessServerBackend,
)
from llmbench.providers.lifecycle.backends.process_server import (
    _models_root as REAL_MODELS_ROOT,
)
from llmbench.providers.lifecycle.backends.process_server import (
    _wired_limit_mb as REAL_WIRED_LIMIT,
)
from llmbench.providers.lifecycle.envelope import LifecycleError
from llmbench.providers.lifecycle.pidproc import PidfileProcess
from llmbench.providers.process_spec import ProcessSpec, Ready, process_spec
from llmbench.providers.secret import SecretError

PS = "llmbench.providers.lifecycle.backends.process_server"
BIN = "/opt/bin/fake"


def _spec(
    *,
    provider: str = "fake",
    kind: str = "models-list",
    endpoint_path: str = "endpoint",
    model_path: str = "model",
    stop: tuple[str, ...] = ("stop", "--port", "{port}"),
    warmup_seconds: int = 120,
    wired_limit: bool = False,
    loaded_only: bool = False,
    model_dir: str = "",
) -> ProcessSpec:
    if kind == "models-list":
        ready = Ready(kind, (), "", "", "", "")
    elif kind == "http-get":
        ready = Ready("http-get", (), "/health", "", "", "")
    else:
        ready = Ready(kind, ("status",), "ready", "true", model_path, endpoint_path)
    return ProcessSpec(
        family=provider,
        provider=provider,
        binary="fake",
        spawn=("serve", "-m", "{model}", "-p", "{port}", "--id", "{model}"),
        stop=stop,
        pidfile="/tmp/fake.pid",
        log="/tmp/fake.log",
        pidfile_env="FAKE_PID",
        subcommand="serve",
        daemon_module="",
        port_flag="-p",
        model_flag="--id",
        scope="port",
        default_origin="http://127.0.0.1:9100",
        wt_port=9100,
        restore_action="stop",
        warmup_seconds=warmup_seconds,
        ready=ready,
        model_dir=model_dir,
        loaded_only=loaded_only,
        wired_limit=wired_limit,
    )


def _registry(text: str) -> None:
    Path(os.environ["WT_REGISTRY"]).write_text(text)


def _provider_row(provider: str = "fake", base_url: str = "", secret_ref: str = "") -> str:
    auth = []
    if base_url:
        auth.append(f'base_url = "{base_url}"')
    if secret_ref:
        auth.append(f'secret_ref = "{secret_ref}"')
    row = f'[[providers]]\nid = "{provider}"\nlocation = "local"\n'
    if auth:
        row += "auth = { " + ", ".join(auth) + " }\n"
    return row


def _model_row(provider: str, name: str) -> str:
    return (
        f'[[models]]\nid = "{provider}/{name}"\nfamily = "f"\n'
        f'provider_id = "{provider}"\nmodel_name = "{name}"\n'
    )


# --- construction ------------------------------------------------------------


def test_attributes_come_from_the_spec_and_the_constructor():
    backend = ProcessServerBackend(_spec())
    assert backend.id == "fake"
    assert backend.occupancy_key == "fake"
    assert backend.env_var is None
    assert backend.default_model is None
    assert backend.restore_action == "stop"
    assert backend.cleanup_on_failure is False
    assert ProcessServerBackend(_spec(), cleanup_on_failure=True).cleanup_on_failure is True


def test_only_mtplx_and_signal_stop_backends_clean_up_on_failure():
    """mtplx asks for it; a signal-stop backend (empty stop, sushi) gets it
    from its spec, because a failed start must not leave a server nothing
    else will stop. rec and every other backend leave a failed start alone."""
    assert [b.id for b in BACKENDS.values() if b.cleanup_on_failure] == ["mtplx", "sushi"]
    for backend in BACKENDS.values():
        if backend.id not in ("mtplx", "sushi"):
            assert backend.cleanup_on_failure is False, backend.id


def test_rec_is_registered_generically():
    rec = BACKENDS["rec"]
    assert isinstance(rec, ProcessServerBackend)
    assert type(rec) is type(MTPLX)
    assert rec.cleanup_on_failure is False
    assert rec.restore_action == "stop"
    assert rec.occupancy_key == "rec"


def test_sushi_is_registered_generically_with_a_signal_stop():
    """sushi has no module of its own: the contract entry registers it on the
    generic backend, stopped by signalling the verified pid (its stop is
    empty), and a failed start is cleaned up the same way."""
    sushi = BACKENDS["sushi"]
    assert type(sushi) is ProcessServerBackend
    assert sushi.restore_action == "stop"
    assert sushi.cleanup_on_failure is True
    assert sushi._signal_stop is True
    assert sushi.id == sushi.occupancy_key == "sushi"


def test_no_other_backend_is_a_signal_stop():
    """Only sushi's spec has an empty stop: mtplx and rec stop through their
    own command, and a backend that wrongly read as signal-stop would have
    its stop bypass that command."""
    signal_stop = [b.id for b in BACKENDS.values() if getattr(b, "_signal_stop", False)]
    assert signal_stop == ["sushi"]


# --- addresses ---------------------------------------------------------------


def test_no_registry_falls_back_to_the_default_origin():
    backend = ProcessServerBackend(_spec())
    assert backend.health_url == "http://127.0.0.1:9100/v1/models"
    assert backend.chat_url == "http://127.0.0.1:9100/v1/chat/completions"
    with patch(f"{PS}.binaries.require_binary", return_value=BIN):
        assert backend.check_available() is None


def test_registry_base_url_names_the_origin_and_port():
    _registry(_provider_row(base_url="http://127.0.0.1:9200/v1"))
    backend = ProcessServerBackend(_spec())
    assert backend.health_url == "http://127.0.0.1:9200/v1/models"
    assert backend.chat_url == "http://127.0.0.1:9200/v1/chat/completions"


def test_base_url_without_a_port_uses_wt_port_for_the_argv_and_every_probe():
    """wt's #348 rule: an http url on a host the spawned server answers on,
    naming no port, means the family's wt_port, for the argv AND the probes
    (a split would start on 9100 and poll :80)."""
    _registry(_provider_row(base_url="http://127.0.0.1/v1"))
    backend = ProcessServerBackend(_spec())
    assert backend.health_url == "http://127.0.0.1:9100/v1/models"
    assert backend.chat_url == "http://127.0.0.1:9100/v1/chat/completions"
    with (
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.probe.wait_for_port_closed") as wait_closed,
        patch.object(backend, "_pidproc") as pidproc,
    ):
        backend.start(backend.resolve("m", ()))
    assert pidproc.spawn.call_args.args[0][5] == "9100"
    wait_closed.assert_called_once_with("http://127.0.0.1:9100/v1/models", timeout=10.0)


@pytest.mark.parametrize(
    ("base_url", "origin", "port"),
    [
        # wt fills in wt_port: http, no port, a host the server answers on.
        ("http://localhost/v1", "http://localhost:9100", 9100),
        ("http://LOCALHOST./v1", "http://LOCALHOST.:9100", 9100),
        ("http://127.0.0.1", "http://127.0.0.1:9100", 9100),
        ("http://localhost:/v1", "http://localhost:9100", 9100),
        ("http://0.0.0.0/v1/", "http://0.0.0.0:9100", 9100),
        ("http://[::]/v1", "http://[::]:9100", 9100),
        # Everything else is left exactly as written.
        ("https://localhost/v1", "https://localhost", 443),
        ("http://gpu-box.lan/v1", "http://gpu-box.lan", 80),
        ("http://[::1]/v1", "http://[::1]", 80),
        ("http://127.0.0.2/v1", "http://127.0.0.2", 80),
        ("http://localhost:9555/v1", "http://localhost:9555", 9555),
        ("https://remote.example:8443/v1", "https://remote.example:8443", 8443),
        # BaseOrigin normalisation: trailing slashes and a /v1 suffix go.
        ("http://127.0.0.1:9200/v1/", "http://127.0.0.1:9200", 9200),
        ("http://127.0.0.1:9200/", "http://127.0.0.1:9200", 9200),
        ("http://127.0.0.1:9200", "http://127.0.0.1:9200", 9200),
    ],
)
def test_origin_follows_wts_rule(base_url, origin, port):
    _registry(_provider_row(base_url=base_url))
    backend = ProcessServerBackend(_spec())
    assert backend.health_url == f"{origin}/v1/models"
    assert backend.chat_url == f"{origin}/v1/chat/completions"
    with (
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.subprocess.run", return_value=_run_result()) as run,
        patch(f"{PS}.probe.port_closed_within", return_value=True) as closed,
    ):
        backend.stop_and_wait()
    assert run.call_args.args[0] == [BIN, "stop", "--port", str(port)]
    assert closed.call_args.args[0] == f"{origin}/v1/models"


def test_malformed_registry_falls_back_to_the_default_origin():
    """A registry load_registry cannot parse (a model row whose fetch is not
    a table raises AttributeError, not RegistryError) must not break the
    best-effort address lookup."""
    _registry(_provider_row(base_url="http://127.0.0.1:9200/v1") + _model_row_bad())
    backend = ProcessServerBackend(_spec())
    assert backend.health_url == "http://127.0.0.1:9100/v1/models"
    assert backend.chat_url == "http://127.0.0.1:9100/v1/chat/completions"


def test_malformed_registry_still_gives_mtplx_envelopes():
    from llmbench.providers.lifecycle import orchestrate

    _registry(
        _provider_row(provider="mtplx", base_url="http://localhost:8003/v1") + _model_row_bad()
    )
    with patch(f"{PS}.binaries.require_binary", return_value="/b/mtplx"):
        isolated = orchestrate.isolate("mtplx")
    assert isolated.ok is False
    assert isolated.direct_url == "http://localhost:8003/v1/chat/completions"
    with patch(
        f"{PS}.binaries.require_binary", side_effect=LifecycleError("mtplx binary not found")
    ):
        stopped = orchestrate.stop("mtplx")
    assert stopped.ok is True
    assert stopped.direct_url == "http://localhost:8003/v1/chat/completions"


def _model_row_bad() -> str:
    return (
        '[[models]]\nid = "x/y"\nfamily = "f"\nprovider_id = "x"\nmodel_name = "y"\nfetch = "x"\n'
    )


def test_check_available_reports_the_missing_binary():
    backend = ProcessServerBackend(_spec())
    with patch(
        f"{PS}.binaries.require_binary",
        side_effect=LifecycleError("fake binary not found on PATH"),
    ):
        assert backend.check_available() == "fake binary not found on PATH"


# --- start -------------------------------------------------------------------


def test_start_expands_model_and_port_into_the_spawn_argv():
    _registry(_provider_row(base_url="http://127.0.0.1:9300/v1"))
    backend = ProcessServerBackend(_spec())
    proc = MagicMock()
    with (
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.probe.wait_for_port_closed") as wait_closed,
        patch.object(backend, "_pidproc") as pidproc,
    ):
        pidproc.spawn.return_value = proc
        backend.start(backend.resolve("org/m", ()))
    wait_closed.assert_called_once_with("http://127.0.0.1:9300/v1/models", timeout=10.0)
    pidproc.spawn.assert_called_once_with(
        [BIN, "serve", "-m", "org/m", "-p", "9300", "--id", "org/m"]
    )
    assert backend._proc is proc


def test_pidfile_process_comes_from_the_spec():
    backend = ProcessServerBackend(_spec())
    assert backend._pidproc.name == "fake"
    assert backend._pidproc.pidfile == "/tmp/fake.pid"
    assert backend._pidproc.logfile == "/tmp/fake.log"


def test_unreadable_secret_raises_naming_the_provider_before_spawn():
    _registry(_provider_row(secret_ref="exec:fake-token"))
    backend = ProcessServerBackend(_spec())
    with (
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.resolve_secret", side_effect=SecretError("secret command failed")),
        patch(f"{PS}.probe.wait_for_port_closed") as wait_closed,
        patch.object(backend, "_pidproc") as pidproc,
        pytest.raises(LifecycleError, match="fake"),
    ):
        backend.start(backend.resolve("m", ()))
    pidproc.spawn.assert_not_called()
    wait_closed.assert_not_called()


def test_unrunnable_secret_command_raises_before_spawn():
    """The real resolver, with a command that cannot run."""
    _registry(_provider_row(secret_ref="exec:/nonexistent/fake-token"))
    backend = ProcessServerBackend(_spec())
    with (
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.probe.wait_for_port_closed"),
        patch.object(backend, "_pidproc") as pidproc,
        pytest.raises(LifecycleError, match="fake"),
    ):
        backend.start(backend.resolve("m", ()))
    pidproc.spawn.assert_not_called()


def test_unreadable_secret_raises_from_already_serving():
    """already_serving runs before any teardown in isolate(), so an unreadable
    key fails the isolation before anything else is touched."""
    _registry(_provider_row(secret_ref="exec:fake-token"))
    backend = ProcessServerBackend(_spec())
    with (
        patch(f"{PS}.resolve_secret", side_effect=SecretError("secret command failed")),
        patch(f"{PS}.probe.serving_model") as serving,
        pytest.raises(LifecycleError, match="fake"),
    ):
        backend.already_serving(backend.resolve("m", ()))
    serving.assert_not_called()


def test_resolve_never_runs_the_secret_command():
    _registry(_provider_row(secret_ref="exec:fake-token"))
    backend = ProcessServerBackend(_spec())
    with patch(f"{PS}.resolve_secret") as resolver:
        backend.resolve("m", ())
    resolver.assert_not_called()


# --- resolve: the legacy mtplx texts ------------------------------------------


def _mtplx() -> ProcessServerBackend:
    spec = process_spec("mtplx")
    assert spec is not None
    return ProcessServerBackend(spec, cleanup_on_failure=True)


def _reg(*names: str):
    from llmbench.registry import ModelEntry, Registry

    return Registry(
        providers=[],
        models=[
            ModelEntry(id=f"mtplx/{n}", family="f", provider_id="mtplx", model_name=n)
            for n in names
        ],
    )


def test_resolve_explicit_model_wins():
    with patch(f"{PS}.load_registry") as load:
        plan = _mtplx().resolve("Org/X", ("Org/Y",))
    load.assert_not_called()
    assert plan.model == "Org/X"
    assert plan.direct_url == "http://localhost:8003/v1/chat/completions"


def test_resolve_positional_then_single_registry_model():
    with patch(f"{PS}.load_registry", return_value=_reg("Org/a", "Org/b")):
        assert _mtplx().resolve(None, ("Org/b",)).model == "Org/b"
    with patch(f"{PS}.load_registry", return_value=_reg("Org/a")):
        assert _mtplx().resolve(None, ()).model == "Org/a"


def test_resolve_no_registry_model_text_is_the_legacy_one():
    with (
        patch(f"{PS}.load_registry", return_value=_reg()),
        pytest.raises(LifecycleError) as exc,
    ):
        _mtplx().resolve(None, ())
    assert str(exc.value) == "no mtplx model in the registry"


def test_resolve_ambiguous_text_is_the_legacy_one():
    with (
        patch(f"{PS}.load_registry", return_value=_reg("Org/m1", "Org/m2")),
        pytest.raises(LifecycleError) as exc,
    ):
        _mtplx().resolve(None, ())
    assert str(exc.value) == "model required: registry holds 2 mtplx models (Org/m1, Org/m2)"


def test_resolve_names_its_own_provider():
    backend = ProcessServerBackend(_spec(provider="other"))
    with (
        patch(f"{PS}.load_registry", return_value=_reg("Org/a")),
        pytest.raises(LifecycleError, match="^no other model in the registry$"),
    ):
        backend.resolve(None, ())


# --- already_serving ----------------------------------------------------------


def test_already_serving_models_list_sends_no_key_without_secret_ref():
    backend = ProcessServerBackend(_spec())
    with patch(f"{PS}.probe.serving_model", return_value=True) as serving:
        assert backend.already_serving(backend.resolve("m", ())) is True
    serving.assert_called_once_with("http://127.0.0.1:9100/v1/models", "m")


def test_already_serving_models_list_passes_the_key():
    _registry(_provider_row(secret_ref="sekrit-value"))
    backend = ProcessServerBackend(_spec())
    with patch(f"{PS}.probe.serving_model", return_value=False) as serving:
        assert backend.already_serving(backend.resolve("m", ())) is False
    found = serving.call_args.kwargs.get("key") == "sekrit-value"
    assert found


def test_empty_secret_sends_no_key(monkeypatch):
    monkeypatch.delenv("FAKE_UNSET_KEY", raising=False)
    _registry(_provider_row(secret_ref="FAKE_UNSET_KEY"))
    backend = ProcessServerBackend(_spec())
    with patch(f"{PS}.probe.serving_model", return_value=False) as serving:
        backend.already_serving(backend.resolve("m", ()))
    assert "key" not in serving.call_args.kwargs


def _status(values: dict[str, object]):
    def fake(argv, *, timeout=5.0):
        assert argv == [BIN, "status"]
        return ("answered", values)

    return fake


def _cj_backend(**kw) -> ProcessServerBackend:
    _registry(_provider_row(base_url="http://127.0.0.1:9100/v1"))
    return ProcessServerBackend(_spec(kind="command-json", **kw))


@pytest.mark.parametrize(
    ("values", "expected"),
    [
        ({"ready": True, "model": "m", "endpoint": "http://127.0.0.1:9100/v1"}, True),
        ({"ready": False, "model": "m", "endpoint": "http://127.0.0.1:9100/v1"}, False),
        ({"ready": True, "model": "other", "endpoint": "http://127.0.0.1:9100/v1"}, False),
        ({"ready": True, "model": "m", "endpoint": "http://127.0.0.1:9999/v1"}, False),
        ({"ready": True, "model": "m", "endpoint": "http://10.0.0.1:9100/v1"}, False),
        ({"ready": True, "model": "m"}, False),
        ({}, False),
    ],
)
def test_already_serving_command_json(values, expected):
    backend = _cj_backend()
    with (
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.probe.command_object", side_effect=_status(values)),
    ):
        assert backend.already_serving(backend.resolve("m", ())) is expected


def test_already_serving_command_json_without_endpoint_path_ignores_the_endpoint():
    backend = _cj_backend(endpoint_path="")
    with (
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(
            f"{PS}.probe.command_object",
            side_effect=_status({"ready": True, "model": "m", "endpoint": "http://x:1/v1"}),
        ),
    ):
        assert backend.already_serving(backend.resolve("m", ())) is True


def test_already_serving_command_json_missing_binary_is_not_serving():
    backend = _cj_backend()
    with (
        patch(
            f"{PS}.binaries.require_binary",
            side_effect=LifecycleError("fake binary not found on PATH"),
        ),
        patch(f"{PS}.probe.command_json") as cj,
    ):
        assert backend.already_serving(backend.resolve("m", ())) is False
    cj.assert_not_called()


# --- wait_ready / warm --------------------------------------------------------


def test_wait_ready_command_json_polls_the_status_command_with_the_proc():
    backend = _cj_backend()
    backend._proc = proc = MagicMock()
    with (
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.probe.wait_for_command_ready") as wait,
        patch(f"{PS}.probe.wait_for_model") as wait_model,
    ):
        backend.wait_ready(backend.resolve("m", ()))
    wait.assert_called_once_with(
        [BIN, "status"], path="ready", equals="true", proc=proc, timeout=probe.MODEL_LOAD_TIMEOUT
    )
    wait_model.assert_not_called()


def test_wait_ready_and_warm_models_list_pass_the_key():
    _registry(_provider_row(secret_ref="sekrit-value"))
    backend = ProcessServerBackend(_spec())
    plan = backend.resolve("m", ())
    with (
        patch(f"{PS}.probe.wait_for_model") as wait_model,
        patch(f"{PS}.probe.warmup") as warmup,
    ):
        backend.wait_ready(plan)
        backend.warm(plan)
    keyed = [
        c.kwargs.get("key") == "sekrit-value" for c in (wait_model.call_args, warmup.call_args)
    ]
    assert keyed == [True, True]
    assert warmup.call_args.kwargs["timeout"] == 120.0
    assert warmup.call_args.kwargs["request_timeout"] == 120.0


@pytest.mark.parametrize(
    ("failure", "tail", "expected"),
    [
        # models-list kind (mtplx's), process gone mid-load: tail appended,
        # the key masked, and a key fragment at the tail's cut start dropped.
        (
            LifecycleError("serve process exited during model load (exit 9)"),
            "rit-value\nauth sekrit-value\nout of memory\n",
            "serve process exited during model load (exit 9); log tail: auth ***\nout of memory",
        ),
        # An empty log leaves the error exactly as raised.
        (LifecycleError("timed out"), "", "timed out"),
        # A refused key is not a load failure: no tail.
        (probe.WarmupRefusedError("refused"), "out of memory\n", "refused"),
    ],
)
def test_wait_ready_failure_carries_the_masked_log_tail(tmp_path, failure, tail, expected):
    _registry(_provider_row(secret_ref="sekrit-value"))
    backend = ProcessServerBackend(_spec())
    log = tmp_path / "fake.log"
    log.write_text(tail)
    backend._pidproc = dataclasses.replace(backend._pidproc, logfile=str(log))
    with (
        patch(f"{PS}.probe.wait_for_model", side_effect=failure),
        pytest.raises(LifecycleError) as exc,
    ):
        backend.wait_ready(backend.resolve("m", ()))
    assert str(exc.value) == expected
    leaked = "sekrit" in str(exc.value)
    assert not leaked


def test_spawn_immediate_exit_tail_is_masked():
    """PidfileProcess.spawn() bakes the log tail into its immediate-exit
    error; start() masks the provider key in it like wait_ready() does."""
    _registry(_provider_row(secret_ref="sekrit-value"))
    backend = ProcessServerBackend(_spec())
    plan = backend.resolve("m", ())
    failure = LifecycleError("fake exited immediately (exit 1); log tail: auth sekrit-value boom")
    with (
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.probe.wait_for_port_closed"),
        pytest.raises(LifecycleError) as exc,
    ):
        backend._pidproc = MagicMock(spawn=MagicMock(side_effect=failure))
        backend.start(plan)
    assert str(exc.value) == "fake exited immediately (exit 1); log tail: auth *** boom"


def _logged(backend: ProcessServerBackend, tmp_path: Path, text: str) -> None:
    log = tmp_path / "fake.log"
    log.write_text(text)
    backend._pidproc = dataclasses.replace(backend._pidproc, logfile=str(log))


def test_load_failure_tail_is_masked_with_the_key_resolved_at_the_start_of_the_isolate(
    tmp_path,
):
    """The key a load-failure tail is masked with is the one the isolate
    resolved first: a `rec auth token` that fails the second time must not
    let the raw tail (key included) out."""
    backend = _cj_backend()
    _registry(_provider_row(base_url="http://127.0.0.1:9100/v1", secret_ref="exec:fake-token"))
    _logged(backend, tmp_path, "loading\nkey sekrit-value in use\nout of memory\n")
    plan = backend.resolve("m", ())
    with (
        patch(f"{PS}.resolve_secret", side_effect=["sekrit-value", SecretError("nope")]),
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.probe.command_status", return_value=("gone", None)),
        patch(f"{PS}.probe.wait_for_port_closed"),
        patch(
            f"{PS}.probe.wait_for_command_ready",
            side_effect=LifecycleError("serve process exited during model load: exit 9"),
        ),
        patch.object(type(backend._pidproc), "spawn"),
        pytest.raises(LifecycleError) as exc,
    ):
        backend.already_serving(plan)
        backend.start(plan)
        backend.wait_ready(plan)
    msg = str(exc.value)
    assert ("sekrit" in msg, "out of memory" in msg, "key *** in use" in msg) == (
        False,
        True,
        True,
    )


def test_load_failure_tail_is_withheld_when_the_key_cannot_be_read(tmp_path):
    """With a secret_ref and no key to mask with, the tail is withheld: the
    rule that a key never appears in a log tail holds unconditionally."""
    _registry(_provider_row(secret_ref="exec:fake-token"))
    backend = ProcessServerBackend(_spec())
    _logged(backend, tmp_path, "auth sekrit-value\nout of memory\n")
    with (
        patch(f"{PS}.resolve_secret", side_effect=SecretError("nope")),
        patch.object(
            backend,
            "_wait_ready",
            side_effect=LifecycleError("serve process exited during model load (exit 9)"),
        ),
        pytest.raises(LifecycleError) as exc,
    ):
        backend.wait_ready(backend.resolve("m", ()))
    msg = str(exc.value)
    assert msg == (
        "serve process exited during model load (exit 9); "
        "log tail withheld: provider key unreadable, cannot mask"
    )


def test_load_failure_tail_of_a_key_less_provider_is_unchanged(tmp_path):
    backend = ProcessServerBackend(_spec())
    _logged(backend, tmp_path, "out of memory\n")
    with (
        patch(f"{PS}.resolve_secret") as resolver,
        patch(f"{PS}.probe.wait_for_model", side_effect=LifecycleError("timed out")),
        pytest.raises(LifecycleError) as exc,
    ):
        backend.wait_ready(backend.resolve("m", ()))
    assert str(exc.value) == "timed out; log tail: out of memory"
    resolver.assert_not_called()


def test_the_secret_command_runs_once_per_isolate():
    """already_serving, start, wait_ready, warm and a stop within one
    isolate share one resolved key; the next isolate resolves it afresh
    (a rotated key is picked up)."""
    _registry(_provider_row(secret_ref="exec:fake-token"))
    backend = ProcessServerBackend(_spec())
    plan = backend.resolve("m", ())
    with (
        patch(f"{PS}.resolve_secret", return_value="tok") as resolver,
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.probe.serving_model", return_value=False),
        patch(f"{PS}.probe.wait_for_port_closed"),
        patch(f"{PS}.probe.wait_for_model"),
        patch(f"{PS}.probe.warmup"),
        patch(f"{PS}.probe.port_closed_within", return_value=True),
        patch(f"{PS}.subprocess.run", return_value=_run_result()),
        patch.object(backend, "_pidproc"),
    ):
        backend.already_serving(plan)
        backend.replace_own_occupant(plan)
        backend.start(plan)
        backend.wait_ready(plan)
        backend.warm(plan)
        assert resolver.call_count == 1
        backend.already_serving(plan)
        assert resolver.call_count == 2


def test_secret_resolves_once_per_call():
    _registry(_provider_row(secret_ref="exec:fake-token"))
    backend = ProcessServerBackend(_spec())
    plan = backend.resolve("m", ())
    with (
        patch(f"{PS}.resolve_secret", return_value="tok") as resolver,
        patch(f"{PS}.probe.warmup"),
    ):
        backend.warm(plan)
    assert resolver.call_count == 1


# --- stop_and_wait ------------------------------------------------------------


def _run_result(code: int = 0, stderr: str = "", stdout: str = "") -> MagicMock:
    result = MagicMock()
    result.returncode = code
    result.stderr = stderr
    result.stdout = stdout
    return result


def test_stop_returns_none_on_a_clean_stop_and_runs_the_expanded_argv():
    backend = ProcessServerBackend(_spec())
    with (
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.subprocess.run", return_value=_run_result()) as run,
        patch(f"{PS}.probe.port_closed_within", return_value=True) as closed,
        patch.object(backend, "_pidproc") as pidproc,
    ):
        assert backend.stop_and_wait() is None
    # A stop command is the stop: the pidfile is neither read nor signalled.
    pidproc.stop.assert_not_called()
    pidproc.live_pid.assert_not_called()
    run.assert_called_once_with(
        [BIN, "stop", "--port", "9100"], capture_output=True, text=True, check=False
    )
    closed.assert_called_once_with(
        "http://127.0.0.1:9100/v1/models", timeout=probe.STOP_WAIT_TIMEOUT
    )


def test_stop_missing_binary_warning():
    backend = ProcessServerBackend(_spec())
    with patch(
        f"{PS}.binaries.require_binary",
        side_effect=LifecycleError("fake binary not found on PATH"),
    ):
        # Not installed: nothing can be running, so a teardown has nothing to say.
        assert backend.stop_and_wait() is None


@pytest.mark.parametrize(
    ("result", "warning"),
    [
        (_run_result(1, stderr="boom\n"), "boom"),
        (_run_result(1, stdout="out\n"), "out"),
        (_run_result(1), "fake stop failed"),
        (_run_result(0), "fake still listening on port 9100"),
    ],
)
def test_stop_warnings_when_the_port_stays_open(result, warning):
    backend = ProcessServerBackend(_spec())
    with (
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.subprocess.run", return_value=result),
        patch(f"{PS}.probe.port_closed_within", return_value=False),
    ):
        assert backend.stop_and_wait() == warning


def test_stop_legacy_mtplx_texts_are_byte_identical():
    backend = _mtplx()
    with (
        patch(f"{PS}.binaries.require_binary", side_effect=LifecycleError("x")),
    ):
        assert backend.stop_and_wait() is None
    with (
        patch(f"{PS}.binaries.require_binary", return_value="/b/mtplx"),
        patch(f"{PS}.subprocess.run", return_value=_run_result(1)) as run,
        patch(f"{PS}.probe.port_closed_within", return_value=False),
    ):
        assert backend.stop_and_wait() == "mtplx stop failed"
    assert run.call_args.args[0] == [
        "/b/mtplx",
        "stop",
        "--port",
        "8003",
        "--grace-seconds",
        "10",
    ]
    with (
        patch(f"{PS}.binaries.require_binary", return_value="/b/mtplx"),
        patch(f"{PS}.subprocess.run", return_value=_run_result(0)),
        patch(f"{PS}.probe.port_closed_within", return_value=False),
    ):
        assert backend.stop_and_wait() == "mtplx still listening on port 8003"


def test_mtplx_spawn_argv_is_byte_identical():
    backend = _mtplx()
    with (
        patch(f"{PS}.binaries.require_binary", return_value="/b/mtplx"),
        patch(f"{PS}.probe.wait_for_port_closed"),
        patch.object(backend, "_pidproc") as pidproc,
    ):
        backend.start(backend.resolve("Org/M", ()))
    pidproc.spawn.assert_called_once_with(
        [
            "/b/mtplx",
            "serve",
            "--model",
            "Org/M",
            "--port",
            "8003",
            "--host",
            "127.0.0.1",
            "--model-id",
            "Org/M",
        ]
    )
    assert backend._pidproc.pidfile == "/tmp/local-ai-setup-mtplx.pid"
    assert backend._pidproc.logfile == "/tmp/local-ai-setup-mtplx.log"


def test_stop_resolves_no_key_and_polls_keyless():
    """The stop command needs no key and the port poll reads any HTTP answer
    (a 401 included) as still open, so a teardown never runs the secret
    command, readable or not."""
    _registry(_provider_row(secret_ref="exec:fake-token"))
    backend = ProcessServerBackend(_spec())
    with (
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.resolve_secret", side_effect=SecretError("nope")) as secret,
        patch(f"{PS}.subprocess.run", return_value=_run_result()) as run,
        patch(f"{PS}.probe.port_closed_within", return_value=True) as closed,
    ):
        assert backend.stop_and_wait() is None
    run.assert_called_once()
    secret.assert_not_called()
    assert "key" not in closed.call_args.kwargs


_STILL = "fake still reports an instance after stop (ready={}); it may still be shutting down"
_SILENT = (
    "fake still reports an instance after stop (the status command did not answer); "
    "it may still be shutting down"
)


def _outcome(ready: object):
    """A `command_status` stand-in: None means the status command failed
    (no instance); any other value is what it answered."""

    def fake(argv, *, path, timeout=5.0):
        assert (argv, path) == ([BIN, "status"], "ready")
        return ("gone", None) if ready is None else ("answered", ready)

    return fake


def _sequence(*outcomes):
    answers = iter(outcomes)
    return lambda argv, *, path, timeout=5.0: next(answers)


@pytest.fixture
def short_gone_budget():
    """A tiny budget and no pause for the wait until a command-json
    provider's status command reports no instance, so a status stuck on a
    value ends the wait in milliseconds."""
    with (
        patch(f"{PS}.INSTANCE_GONE_TIMEOUT", 0.05),
        patch(f"{PS}.INSTANCE_GONE_POLL", 0.0),
    ):
        yield


@pytest.mark.parametrize(
    ("closed", "ready", "code", "warning"),
    [
        (True, None, 0, None),
        (True, None, 1, None),
        (True, False, 0, _STILL.format("false")),
        (True, True, 0, _STILL.format("true")),
        (False, None, 0, "fake still listening on port 9100"),
        (True, True, 1, "fake stop failed"),
        (True, False, 1, "fake stop failed"),
    ],
)
def test_stop_command_json_confirms_with_status_and_port(
    short_gone_budget, closed, ready, code, warning
):
    """A command-json stop is confirmed only when the port is closed AND the
    status command reports no instance at all (it fails). A status that
    still answers — ready false included — is an instance that has not gone
    yet."""
    backend = _cj_backend()
    with (
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.subprocess.run", return_value=_run_result(code)) as run,
        patch(f"{PS}.probe.port_closed_within", return_value=closed),
        patch(f"{PS}.probe.command_status", side_effect=_outcome(ready)),
    ):
        assert backend.stop_and_wait() == warning
    assert run.call_args.args[0] == [BIN, "stop", "--port", "9100"]


def test_stop_command_json_waits_until_status_reports_no_instance():
    """rec's status exits 0 with ready:false while the instance shuts down
    (and while it loads); the process still holds its memory then. The stop
    is clean only once the status command fails, and not one poll sooner."""
    backend = _cj_backend()
    with (
        patch(f"{PS}.INSTANCE_GONE_POLL", 0.0),
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.subprocess.run", return_value=_run_result()),
        patch(f"{PS}.probe.port_closed_within", return_value=True),
        patch(
            f"{PS}.probe.command_status",
            side_effect=_sequence(*[("answered", False)] * 3, ("gone", None)),
        ) as status,
    ):
        assert backend.stop_and_wait() is None
    assert status.call_count == 4


def test_stop_command_json_a_hung_status_is_not_gone_until_it_exits_1():
    """A `rec status` that times out under a multi-GB teardown says nothing:
    the stop keeps waiting through it and is clean only once the status
    command exits non-zero."""
    backend = _cj_backend()
    with (
        patch(f"{PS}.INSTANCE_GONE_POLL", 0.0),
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.subprocess.run", return_value=_run_result()),
        patch(f"{PS}.probe.port_closed_within", return_value=True),
        patch(
            f"{PS}.probe.command_status",
            side_effect=_sequence(*[("unknown", None)] * 3, ("gone", None)),
        ) as status,
    ):
        assert backend.stop_and_wait() is None
    assert status.call_count == 4


def test_stop_command_json_a_status_that_never_answers_warns_so(short_gone_budget):
    backend = _cj_backend()
    with (
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.subprocess.run", return_value=_run_result()),
        patch(f"{PS}.probe.port_closed_within", return_value=True),
        patch(f"{PS}.probe.command_status", return_value=("unknown", None)),
    ):
        assert backend.stop_and_wait() == _SILENT


def test_stop_command_json_status_gone_at_once_is_one_poll():
    backend = _cj_backend()
    with (
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.subprocess.run", return_value=_run_result(1)),
        patch(f"{PS}.probe.port_closed_within", return_value=True),
        patch(f"{PS}.probe.command_status", return_value=("gone", None)) as status,
    ):
        assert backend.stop_and_wait() is None
    status.assert_called_once_with([BIN, "status"], path="ready")


def test_stop_a_loading_instance_is_not_reported_stopped(short_gone_budget):
    """A loading instance has its port closed and a status of ready:false —
    the exact reading the old check took for "stopped". It must warn."""
    backend = _cj_backend()
    with (
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.subprocess.run", return_value=_run_result()),
        patch(f"{PS}.probe.port_closed_within", return_value=True),
        patch(f"{PS}.probe.command_status", side_effect=_outcome(False)),
    ):
        warning = backend.stop_and_wait()
    assert warning == _STILL.format("false")


def test_stop_command_json_one_deadline_covers_the_port_and_the_instance():
    """One INSTANCE_GONE_TIMEOUT budget covers both conditions: a rec whose
    port stays open for 7 s while it drains is waited for, not reported
    "still listening" after probe.STOP_WAIT_TIMEOUT (6 s)."""
    backend = _cj_backend()
    with (
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.subprocess.run", return_value=_run_result()),
        patch(f"{PS}.probe.port_closed_within", return_value=True) as closed,
        patch(f"{PS}.probe.command_status", return_value=("gone", None)),
    ):
        assert backend.stop_and_wait() is None
    budget = closed.call_args.kwargs["timeout"]
    assert 7.0 < budget <= INSTANCE_GONE_TIMEOUT


def test_stop_models_list_never_runs_a_status_command():
    """mtplx's stop is unchanged: the port poll alone, with its old budget."""
    backend = _mtplx()
    with (
        patch(f"{PS}.binaries.require_binary", return_value="/b/mtplx"),
        patch(f"{PS}.subprocess.run", return_value=_run_result()),
        patch(f"{PS}.probe.port_closed_within", return_value=True) as closed,
        patch(f"{PS}.probe.command_status") as status,
        patch(f"{PS}.probe.command_json") as cj,
    ):
        assert backend.stop_and_wait() is None
    status.assert_not_called()
    cj.assert_not_called()
    closed.assert_called_once_with(
        "http://localhost:8003/v1/models", timeout=probe.STOP_WAIT_TIMEOUT
    )


def test_start_command_json_waits_for_no_instance_before_spawning():
    backend = _cj_backend()
    with (
        patch(f"{PS}.INSTANCE_GONE_POLL", 0.0),
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.probe.wait_for_port_closed"),
        patch(
            f"{PS}.probe.command_status",
            side_effect=_sequence(("unknown", None), ("answered", False), ("gone", None)),
        ) as status,
        patch.object(backend, "_pidproc") as pidproc,
    ):
        backend.start(backend.resolve("m", ()))
    assert status.call_count == 3
    pidproc.spawn.assert_called_once()


def test_start_command_json_refuses_to_spawn_beside_a_lingering_instance(short_gone_budget):
    """One instance per machine: spawning while the old one still reports
    itself (shutting down or loading) would put two beside each other."""
    backend = _cj_backend()
    with (
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.probe.wait_for_port_closed"),
        patch(f"{PS}.probe.command_status", side_effect=_outcome(False)),
        patch.object(backend, "_pidproc") as pidproc,
        pytest.raises(LifecycleError, match=r"^fake still reports an instance after stop"),
    ):
        backend.start(backend.resolve("m", ()))
    pidproc.spawn.assert_not_called()


def test_start_models_list_runs_no_status_command():
    backend = _mtplx()
    with (
        patch(f"{PS}.binaries.require_binary", return_value="/b/mtplx"),
        patch(f"{PS}.probe.wait_for_port_closed"),
        patch(f"{PS}.probe.command_status") as status,
        patch(f"{PS}.probe.command_json") as cj,
        patch.object(backend, "_pidproc"),
    ):
        backend.start(backend.resolve("Org/M", ()))
    status.assert_not_called()
    cj.assert_not_called()


def test_replace_own_occupant_raises_the_stop_warning():
    backend = ProcessServerBackend(_spec())
    with (
        patch.object(backend, "stop_and_wait", return_value="fake stop failed"),
        pytest.raises(LifecycleError, match="^fake stop failed$"),
    ):
        backend.replace_own_occupant(backend.resolve("m", ()))


def test_synthetic_spec_helper_is_a_real_spec():
    """Guard the fixture: a synthetic spec swaps fields of the same type."""
    real = process_spec("rec")
    assert real is not None
    assert {f.name for f in dataclasses.fields(real)} == {
        f.name for f in dataclasses.fields(_spec())
    }


def test_already_serving_runs_the_status_command_once():
    backend = _cj_backend()
    values = {"ready": True, "model": "m", "endpoint": "http://127.0.0.1:9100/v1"}
    with (
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.probe.command_object", side_effect=_status(values)) as status,
    ):
        assert backend.already_serving(backend.resolve("m", ())) is True
    status.assert_called_once()


def test_an_isolate_reads_the_registry_row_once():
    """Address and key of one isolate come from one read of the registry."""
    _registry(_provider_row(base_url="http://127.0.0.1:9100/v1", secret_ref="k"))
    backend = ProcessServerBackend(_spec())
    plan = backend.resolve("m", ())  # pure, outside the isolate's snapshot
    with patch(f"{PS}._provider_row", wraps=process_server._provider_row) as read:
        backend.already_serving(plan)
        _ = backend.health_url, backend.chat_url
    assert read.call_count == 1


# --- http-get readiness, loaded-aware serving, the spec's warmup budget -------
#
# A sushi-style entry (no contract entry yet): ready when an HTTP path answers
# 200, the model listed only once a request loads it, stopped by signalling
# the pid (empty `stop`), with a 400 s warmup budget.

MODELS = "http://127.0.0.1:9100/v1/models"
CHAT = "http://127.0.0.1:9100/v1/chat/completions"
HINT = "raise the GPU wired limit with `sudo sysctl iogpu.wired_limit_mb=59000` (resets on reboot)"


def _http_get(tmp_path: Path, log: str = "", **kw) -> ProcessServerBackend:
    """An http-get backend whose log is a scratch file holding `log`. The
    defaults are sushi's: signal stop, 400 s budget."""
    kw.setdefault("stop", ())
    kw.setdefault("warmup_seconds", 400)
    kw.setdefault("wired_limit", not kw["stop"])
    kw.setdefault("loaded_only", not kw["stop"])
    backend = ProcessServerBackend(_spec(kind="http-get", **kw))
    _logged(backend, tmp_path, log)
    return backend


def _warm(
    backend: ProcessServerBackend,
    model: str = "org/pack",
    *,
    listed: list[str] | None = None,
    warmup: Exception | None = None,
    loaded: bool = True,
):
    """Run `backend.warm` with the list (default: just `org/pack`), the
    warmup and the loaded check faked; returns the three mocks."""
    with (
        patch(
            f"{PS}.probe.listed_ids", return_value=["org/pack"] if listed is None else listed
        ) as listed_ids,
        patch(f"{PS}.probe.warmup", side_effect=warmup) as warm,
        patch(f"{PS}.probe.serving_model", return_value=loaded) as serving,
    ):
        backend.warm(backend.resolve(model, ()))
    return listed_ids, warm, serving


def test_already_serving_http_get_asks_the_loaded_aware_list(tmp_path):
    """A pack listed with loaded:false is not serving: http-get asks the
    loaded-only list, by the exact id (sushi loads only by its exact id)."""
    backend = _http_get(tmp_path)
    with (
        patch(f"{PS}.probe.wait_for_port_open", return_value=False),
        patch(f"{PS}.probe.serving_model", return_value=True) as serving,
    ):
        assert backend.already_serving(backend.resolve("org/pack", ())) is True
    serving.assert_called_once()
    assert serving.call_args.args == (MODELS, "org/pack")
    assert serving.call_args.kwargs == {"exact": True, "loaded_only": True}


def test_only_a_loaded_only_spec_reads_the_loaded_flag(tmp_path):
    """The `loaded: false` filter is the spec's opt-in (sushi): a spec without
    it asks the plain list, so no other server's listing changes meaning."""
    backend = _http_get(tmp_path, loaded_only=False)
    with (
        patch(f"{PS}.probe.wait_for_port_open", return_value=False),
        patch(f"{PS}.probe.serving_model", return_value=True) as serving,
    ):
        backend.already_serving(backend.resolve("org/pack", ()))
    assert serving.call_args.kwargs == {"exact": True}


def test_wait_ready_http_get_polls_the_ready_path_not_the_models_list(tmp_path):
    """The model is not listed until the first request loads it, so readiness
    is the ready path answering 200, not the model at /v1/models."""
    backend = _http_get(tmp_path)
    backend._proc = proc = MagicMock()
    with (
        patch(f"{PS}.probe.wait_for_http_ok") as ok,
        patch(f"{PS}.probe.wait_for_model") as wait_model,
        patch(f"{PS}.probe.wait_for_command_ready") as command_ready,
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
    ):
        backend.wait_ready(backend.resolve("org/pack", ()))
    ok.assert_called_once_with(
        "http://127.0.0.1:9100/health", proc=proc, timeout=probe.MODEL_LOAD_TIMEOUT
    )
    wait_model.assert_not_called()
    command_ready.assert_not_called()


def test_wait_ready_http_get_passes_the_key_only_when_the_row_names_one(tmp_path):
    backend = _http_get(tmp_path)
    with patch(f"{PS}.probe.wait_for_http_ok") as ok:
        backend.wait_ready(backend.resolve("org/pack", ()))
        assert "key" not in ok.call_args.kwargs
        _registry(_provider_row(secret_ref="sekrit-value"))
        backend._forget_key()
        backend.wait_ready(backend.resolve("org/pack", ()))
        assert ok.call_args.kwargs["key"] == "sekrit-value"


def test_warm_uses_the_spec_warmup_budget(tmp_path):
    """The spec's warmup_seconds bounds the whole warmup and each chat."""
    backend = _http_get(tmp_path)
    _, warm, _ = _warm(backend)
    warm.assert_called_once_with(
        CHAT, "org/pack", health_url=MODELS, timeout=400.0, request_timeout=400.0
    )


def test_warm_refuses_a_pack_id_the_server_does_not_list(tmp_path):
    """sushi answers an id it does not know with a 503 a warmup would only
    retry: refuse before any chat, naming what it does list."""
    backend = _http_get(tmp_path)
    with (
        patch(f"{PS}.probe.listed_ids", return_value=["org/other", "org/x"]) as listed,
        patch(f"{PS}.probe.warmup") as warm,
        pytest.raises(LifecycleError) as exc,
    ):
        backend.warm(backend.resolve("org/pack", ()))
    assert str(exc.value) == (
        "fake: 'org/pack' is not listed by the server (it lists: org/other, org/x)"
    )
    listed.assert_called_once_with(MODELS, strict=True)
    warm.assert_not_called()


def test_warm_pack_check_is_exact(tmp_path):
    """A bare `pack` does not match a listed `org/pack`: sushi loads only by
    its exact id, so the lenient suffix match must not pass it."""
    backend = _http_get(tmp_path)
    with (
        patch(f"{PS}.probe.listed_ids", return_value=["org/pack"]),
        patch(f"{PS}.probe.warmup") as warm,
        pytest.raises(LifecycleError, match=r"'pack' is not listed by the server"),
    ):
        backend.warm(backend.resolve("pack", ()))
    warm.assert_not_called()


def test_warm_skips_the_pack_check_when_the_list_is_empty(tmp_path):
    """A server that lists nothing at all is not judged (wt's
    requirePackListed): the warmup runs."""
    backend = _http_get(tmp_path)
    _, warm, serving = _warm(backend, listed=[])
    warm.assert_called_once()
    serving.assert_called_once_with(MODELS, "org/pack", exact=True, loaded_only=True)


def test_warm_fails_closed_when_the_model_list_cannot_be_read(tmp_path):
    """A list that cannot be fetched is not an empty one: the warm fails."""
    backend = _http_get(tmp_path)
    failure = LifecycleError(f"cannot read the model list at {MODELS}: refused")
    with (
        patch(f"{PS}.probe.listed_ids", side_effect=failure),
        patch(f"{PS}.probe.warmup") as warm,
        pytest.raises(LifecycleError) as exc,
    ):
        backend.warm(backend.resolve("org/pack", ()))
    assert exc.value is failure
    warm.assert_not_called()


def test_warm_requires_the_pack_to_be_listed_loaded_after_the_chat(tmp_path):
    """A chat that answered while the pack still reads loaded:false did not
    load the pack this isolate asked for."""
    backend = _http_get(tmp_path)
    with pytest.raises(LifecycleError) as exc:
        _warm(backend, loaded=False)
    assert str(exc.value) == (
        f"fake: org/pack answered the warmup but is not listed as loaded at {MODELS}"
    )


def test_warm_models_list_kind_has_no_pack_check_and_no_loaded_check():
    """mtplx's path: the warmup alone, with its 120 s budget from the spec."""
    backend = _mtplx()
    with (
        patch(f"{PS}.probe.listed_ids") as listed,
        patch(f"{PS}.probe.serving_model") as serving,
        patch(f"{PS}.probe.warmup") as warm,
    ):
        backend.warm(backend.resolve("Org/M", ()))
    listed.assert_not_called()
    serving.assert_not_called()
    warm.assert_called_once_with(
        backend.chat_url,
        "Org/M",
        health_url=backend.health_url,
        timeout=120.0,
        request_timeout=120.0,
    )


@pytest.mark.parametrize(
    "failure", [TimeoutError("timed out"), urllib.error.URLError(TimeoutError("timed out"))]
)
def test_warm_timeout_becomes_a_clear_lifecycle_error(tmp_path, failure):
    backend = _http_get(tmp_path)
    with pytest.raises(LifecycleError) as exc:
        _warm(backend, warmup=failure)
    assert str(exc.value) == "fake: warmup of org/pack timed out after 400s"
    assert exc.value.__cause__ is failure


def test_warm_connection_refused_is_not_called_a_timeout(tmp_path):
    backend = _http_get(tmp_path)
    failure = urllib.error.URLError(ConnectionRefusedError(61, "Connection refused"))
    with pytest.raises(urllib.error.URLError) as exc:
        _warm(backend, warmup=failure)
    assert exc.value is failure


def test_isolate_returns_an_ok_false_envelope_when_the_warmup_times_out(tmp_path):
    backend = _http_get(tmp_path)
    with (
        patch.dict(orchestrate.BACKENDS, {"fake": backend}),
        patch.object(orchestrate, "_stop_others"),
        patch.object(backend, "check_available", return_value=None),
        patch.object(backend, "already_serving", return_value=True),
        patch(f"{PS}.probe.listed_ids", return_value=["org/pack"]),
        patch(f"{PS}.probe.warmup", side_effect=TimeoutError("timed out")),
    ):
        result = orchestrate.isolate("fake", "org/pack")
    assert result.ok is False
    assert result.error == "fake: warmup of org/pack timed out after 400s"


@pytest.mark.parametrize(
    ("message", "log", "expected"),
    [
        # The phrase in the error itself.
        ("metal: out of memory", "", f"metal: out of memory; {HINT}"),
        # The phrase only in the log tail: matched on the combined text, the
        # tail first and the hint last.
        (
            "failed to warm up model org/pack",
            "Failed to allocate 48 GB\n",
            f"failed to warm up model org/pack; log tail: Failed to allocate 48 GB; {HINT}",
        ),
    ],
)
def test_memory_refusal_in_a_signal_stop_warmup_gets_the_wired_limit_hint(
    tmp_path, message, log, expected
):
    backend = _http_get(tmp_path, log)
    with pytest.raises(LifecycleError) as exc:
        _warm(backend, warmup=probe.WarmupRejectedError(message))
    assert str(exc.value) == expected


def test_the_hint_is_not_added_for_a_provider_with_a_stop_command(tmp_path):
    backend = _http_get(tmp_path, "iogpu wired limit reached\n", stop=("stop", "{port}"))
    original = LifecycleError("out of memory")
    with pytest.raises(LifecycleError) as exc:
        _warm(backend, warmup=original)
    assert exc.value is original


def test_a_non_memory_warmup_failure_keeps_its_text(tmp_path):
    backend = _http_get(tmp_path)
    original = LifecycleError(f"failed to warm up model org/pack at {CHAT}")
    with pytest.raises(LifecycleError) as exc:
        _warm(backend, warmup=original)
    assert exc.value is original


@pytest.mark.parametrize(
    ("kw", "expected"),
    [
        ({"warmup": TimeoutError()}, "fake: warmup of org/pack timed out after 400s"),
        (
            {"warmup": probe.WarmupRejectedError("rejected")},
            "rejected",
        ),
        ({"listed": ["org/x"]}, "fake: 'org/pack' is not listed by the server (it lists: org/x)"),
        (
            {"loaded": False},
            f"fake: org/pack answered the warmup but is not listed as loaded at {MODELS}",
        ),
    ],
)
def test_a_signal_stop_warmup_failure_carries_the_log_tail(tmp_path, kw, expected):
    """As wt does for a failed sushi start: the server's log says why."""
    _registry(_provider_row(secret_ref="sekrit-value"))
    backend = _http_get(tmp_path, "loading pack\nauth sekrit-value\n")
    with pytest.raises(LifecycleError) as exc:
        _warm(backend, **kw)
    assert str(exc.value) == f"{expected}; log tail: loading pack\nauth ***"
    if "warmup" in kw and isinstance(kw["warmup"], probe.WarmupRejectedError):
        assert type(exc.value) is probe.WarmupRejectedError


def test_a_stop_command_warmup_failure_has_no_log_tail(tmp_path):
    backend = _http_get(tmp_path, "loading pack\n", stop=("stop", "{port}"))
    original = LifecycleError("boom")
    with pytest.raises(LifecycleError) as exc:
        _warm(backend, warmup=original)
    assert exc.value is original


# --- empty stop: the verified pid, ownership, start-failure parity ------------
#
# An empty `stop` means "signal the pid in the pidfile": the bare binary is
# never run. A server on the port with no verified pidfile was started by
# neither llmbench nor wt, so its context size is unknowable and it is refused
# before anything else is stopped. Every probe and `_pidproc` is patched: no
# test dials a real port or reads a real pidfile.

UNOWNED = (
    "fake: a server answers on port 9100 that was not started by llmbench or wt "
    "(no verified pidfile at /tmp/fake.pid)"
)


# What a spawned fake server runs with: the spec's spawn template at port 9100
# with no model (a model-less server, as sushi is).
EXPECT = ["serve", "-m", "", "-p", "9100", "--id", ""]


def _signal_backend() -> ProcessServerBackend:
    return ProcessServerBackend(
        _spec(
            kind="http-get",
            stop=(),
            warmup_seconds=400,
            wired_limit=True,
            loaded_only=True,
            model_dir="~/.sushi/models",
        )
    )


def _pidproc_mock(
    live_pid: int | None = 4242, pid: int | None = 4242, gone: bool = True
) -> MagicMock:
    pidproc = MagicMock()
    pidproc.live_pid.return_value = live_pid
    pidproc.read_pid.return_value = pid
    pidproc.stop_verified.return_value = gone
    return pidproc


def test_empty_stop_stops_through_the_pidfile_and_never_runs_the_binary():
    backend = _signal_backend()
    with (
        patch(f"{PS}.binaries.require_binary", side_effect=AssertionError("binary looked up")),
        patch(f"{PS}.subprocess.run") as run,
        patch(f"{PS}.probe.port_closed_within", return_value=True) as closed,
        patch.object(backend, "_pidproc", _pidproc_mock()) as pidproc,
    ):
        assert backend.stop_and_wait() is None
    run.assert_not_called()
    pidproc.stop_verified.assert_called_once_with(EXPECT)
    pidproc.live_pid.assert_called_once_with(EXPECT)
    closed.assert_called_once_with(MODELS, timeout=SIGNAL_STOP_TIMEOUT)
    assert SIGNAL_STOP_TIMEOUT == 15.0


def test_empty_stop_with_nothing_running_is_a_clean_noop():
    backend = _signal_backend()
    with (
        patch(f"{PS}.subprocess.run") as run,
        patch(f"{PS}.probe.port_answers", return_value=False),
        patch.object(backend, "_pidproc", _pidproc_mock(None, None)) as pidproc,
    ):
        assert backend.stop_and_wait() is None
    run.assert_not_called()
    pidproc.stop_verified.assert_called_once_with(EXPECT)  # tolerates the missing pidfile


def test_empty_stop_reports_a_server_it_cannot_own():
    backend = _signal_backend()
    with (
        patch(f"{PS}.probe.port_answers", return_value=True),
        patch.object(backend, "_pidproc", _pidproc_mock(None, None)),
    ):
        assert backend.stop_and_wait() == f"{UNOWNED}; stop it yourself"


@pytest.mark.parametrize(("live", "pid"), [(None, None), (None, 4242)])
def test_empty_stop_warns_about_an_unowned_server_at_once(live, pid):
    """No verified pid means nothing of ours was signalled, so there is
    nothing to wait for: one probe and the warning, never the
    SIGNAL_STOP_TIMEOUT wait. Through `_stop_others` every other provider's
    isolate would otherwise pay 15 s while a hand-started server is up.
    The clock and the wait helper are patched so a regression fails fast
    instead of waiting."""
    backend = _signal_backend()
    with (
        patch(f"{PS}.probe.port_answers", return_value=True) as answers,
        patch(
            f"{PS}.probe.port_closed_within",
            side_effect=AssertionError("waited for a server it did not signal"),
        ) as closed,
        patch(f"{PS}.time.sleep", side_effect=AssertionError("slept")),
        patch.object(backend, "_pidproc", _pidproc_mock(live, pid)),
    ):
        assert backend.stop_and_wait() == f"{UNOWNED}; stop it yourself"
    answers.assert_called_once_with(MODELS)
    closed.assert_not_called()


def test_empty_stop_with_no_owned_server_and_a_closed_port_is_silent():
    """The same single probe finding nothing listening: a clean no-op."""
    backend = _signal_backend()
    with (
        patch(f"{PS}.probe.port_answers", return_value=False),
        patch(f"{PS}.probe.port_closed_within") as closed,
        patch.object(backend, "_pidproc", _pidproc_mock(None, None)),
    ):
        assert backend.stop_and_wait() is None
    closed.assert_not_called()


def test_empty_stop_reports_a_server_that_did_not_let_go():
    backend = _signal_backend()
    with (
        patch(f"{PS}.probe.port_closed_within", return_value=False),
        patch.object(backend, "_pidproc", _pidproc_mock()),
    ):
        assert backend.stop_and_wait() == "fake still listening on port 9100"


@pytest.mark.parametrize("pid", [1, os.getpid()])
def test_empty_stop_never_signals_pid_one_or_this_process(pid):
    """A pidfile naming launchd or llmbench itself is refused before
    `PidfileProcess.stop()` could signal it (as wt refuses them)."""
    backend = _signal_backend()
    with (
        patch(f"{PS}.probe.port_answers", return_value=True),
        patch.object(backend, "_pidproc", _pidproc_mock(None, pid)) as pidproc,
    ):
        assert backend.stop_and_wait() == f"{UNOWNED}; stop it yourself"
    pidproc.stop.assert_not_called()


def test_empty_stop_refuses_pid_one_and_this_process_with_a_real_pidfile(tmp_path):
    """The same guard end to end, over a real PidfileProcess: no os.kill."""
    backend = _signal_backend()
    pidfile = tmp_path / "fake.pid"
    backend._pidproc = dataclasses.replace(backend._pidproc, pidfile=str(pidfile))
    for pid in (1, os.getpid()):
        pidfile.write_text(str(pid))
        with (
            patch("llmbench.providers.lifecycle.pidproc.os.kill") as kill,
            patch(f"{PS}.probe.port_answers", return_value=False),
        ):
            assert backend.stop_and_wait() is None
        kill.assert_not_called()


def test_already_serving_refuses_a_server_nobody_verified():
    backend = _signal_backend()
    with (
        patch(f"{PS}.probe.wait_for_port_open", return_value=True) as port_open,
        patch(f"{PS}.probe.serving_model", return_value=True) as serving,
        patch.object(backend, "_pidproc", _pidproc_mock(None)),
        pytest.raises(LifecycleError) as exc,
    ):
        backend.already_serving(backend.resolve("org/pack", ()))
    assert str(exc.value) == (
        f"{UNOWNED}; its context size cannot be checked, so it is not reused. "
        "Stop it yourself and retry"
    )
    port_open.assert_called_once_with(MODELS, timeout=1.0, interval=0.2)
    serving.assert_not_called()


def test_already_serving_accepts_a_server_with_a_verified_pidfile():
    backend = _signal_backend()
    with (
        patch(f"{PS}.probe.wait_for_port_open", return_value=True),
        patch(f"{PS}.probe.serving_model", return_value=True) as serving,
        patch.object(backend, "_pidproc", _pidproc_mock()),
    ):
        assert backend.already_serving(backend.resolve("org/pack", ())) is True
    serving.assert_called_once_with(MODELS, "org/pack", exact=True, loaded_only=True)


def test_already_serving_does_not_object_when_nothing_answers():
    backend = _signal_backend()
    with (
        patch(f"{PS}.probe.wait_for_port_open", return_value=False),
        patch(f"{PS}.probe.serving_model", return_value=False),
        patch.object(backend, "_pidproc", _pidproc_mock(None, None)) as pidproc,
    ):
        assert backend.already_serving(backend.resolve("org/pack", ())) is False
    pidproc.live_pid.assert_not_called()


def test_a_provider_with_a_stop_command_never_asks_about_ownership():
    backend = ProcessServerBackend(_spec())
    with (
        patch(f"{PS}.probe.wait_for_port_open") as port_open,
        patch(f"{PS}.probe.serving_model", return_value=True),
        patch.object(backend, "_pidproc") as pidproc,
    ):
        assert backend.already_serving(backend.resolve("m", ())) is True
    port_open.assert_not_called()
    pidproc.live_pid.assert_not_called()


def test_the_refusal_happens_before_any_other_provider_is_stopped():
    backend = _signal_backend()
    with (
        patch.dict(orchestrate.BACKENDS, {"fake": backend}),
        patch.object(orchestrate, "_stop_others") as stop_others,
        patch.object(backend, "check_available", return_value=None),
        patch(f"{PS}.probe.wait_for_port_open", return_value=True),
        patch(f"{PS}.probe.serving_model", return_value=True),
        patch(f"{PS}.subprocess.run") as run,
        patch.object(backend, "_pidproc", _pidproc_mock(None)) as pidproc,
    ):
        result = orchestrate.isolate("fake", "org/pack")
    assert result.ok is False
    assert result.error is not None
    assert result.error.startswith(UNOWNED)
    stop_others.assert_not_called()
    pidproc.stop.assert_not_called()
    pidproc.spawn.assert_not_called()
    run.assert_not_called()


def _isolate_with_a_failed_warmup(backend: ProcessServerBackend):
    with (
        patch.dict(orchestrate.BACKENDS, {backend.id: backend}),
        patch.object(orchestrate, "_stop_others"),
        patch.object(backend, "check_available", return_value=None),
        patch.object(backend, "already_serving", return_value=False),
        patch.object(backend, "replace_own_occupant"),
        patch.object(backend, "start"),
        patch.object(backend, "wait_ready"),
        patch.object(backend, "warm", side_effect=LifecycleError("warm failed")),
        patch.object(backend, "stop_and_wait", return_value=None) as stop,
    ):
        result = orchestrate.isolate(backend.id, "org/pack")
    return result, stop


def test_a_failed_warmup_stops_a_signal_stop_backend():
    """wt parity: a half-loaded server and its pidfile do not outlive a
    failed start."""
    backend = _signal_backend()
    assert backend.cleanup_on_failure is True
    result, stop = _isolate_with_a_failed_warmup(backend)
    assert result.ok is False
    stop.assert_called_once_with()


def test_a_failed_warmup_does_not_stop_a_stop_command_backend():
    rec = ProcessServerBackend(process_spec("rec"))
    assert rec.cleanup_on_failure is False
    result, stop = _isolate_with_a_failed_warmup(rec)
    assert result.ok is False
    stop.assert_not_called()
    assert _mtplx().cleanup_on_failure is True
    assert ProcessServerBackend(process_spec("mtplx")).cleanup_on_failure is False


def _popen_kwargs(backend, tmp_path: Path) -> dict:
    """Run `backend.start` over a scratch-file PidfileProcess with Popen
    faked; the kwargs Popen got."""
    backend._pidproc = dataclasses.replace(
        backend._pidproc,
        pidfile=str(tmp_path / "x.pid"),
        logfile=str(tmp_path / "x.log"),
    )
    child = MagicMock(pid=4242, poll=MagicMock(return_value=None))
    with (
        patch(f"{PS}.binaries.require_binary", return_value=BIN),
        patch(f"{PS}.probe.wait_for_port_closed"),
        patch("llmbench.providers.lifecycle.pidproc.subprocess.Popen", return_value=child) as p,
        patch("llmbench.providers.lifecycle.pidproc.time.sleep"),
    ):
        backend.start(backend.resolve("org/pack", ()))
    return p.call_args.kwargs


def test_signal_stop_spawn_starts_a_new_session(tmp_path):
    """Setsid-equivalent, as wt spawns it: a terminal's SIGHUP cannot kill a
    46 GB server llmbench started."""
    assert _popen_kwargs(_signal_backend(), tmp_path)["start_new_session"] is True


def test_mtplx_and_mlx_lm_server_spawn_pass_no_session_kwarg(tmp_path, monkeypatch):
    assert "start_new_session" not in _popen_kwargs(_mtplx(), tmp_path)
    from llmbench.providers.lifecycle.backends import mlx_lm_server

    proc = PidfileProcess(
        name="mlx_lm_server",
        pidfile=str(tmp_path / "m.pid"),
        logfile=str(tmp_path / "m.log"),
    )
    child = MagicMock(pid=4242, poll=MagicMock(return_value=None))
    backend = mlx_lm_server.MLX_LM_SERVER
    monkeypatch.setattr(backend, "_proc", backend._proc)  # start() replaces it
    with (
        patch(
            "llmbench.providers.lifecycle.backends.mlx_lm_server.binaries.resolve_mlx_lm_bin",
            return_value="/bin/mlx_lm.server",
        ),
        patch.object(mlx_lm_server, "_PROC", proc),
        patch.object(mlx_lm_server.probe, "port_closed_within", return_value=True),
        patch("llmbench.providers.lifecycle.pidproc.subprocess.Popen", return_value=child) as p,
        patch("llmbench.providers.lifecycle.pidproc.time.sleep"),
    ):
        backend.start(backend.resolve("target", ("target", "draft")))
    assert "start_new_session" not in p.call_args.kwargs


# --- the wired-limit preflight -------------------------------------------------
#
# A sushi pack loads only when the GPU wired limit holds it. The check reads
# the pack's safetensors and one sysctl (never set), and fails an isolate with
# the fix before any other provider is stopped. tests/conftest.py makes the
# pack root and the sysctl harmless for every test; these patch them back.

PACK = "org/pack"
FIX = "sudo sysctl iogpu.wired_limit_mb=59000"


def _signal_backend_with_pack(tmp_path: Path, mib: int | None = 100) -> ProcessServerBackend:
    """A signal-stop backend whose registry row names tmp/models; the pack
    holds one `mib` MiB safetensors file (and a big ngram_table.bin that must
    not count). `mib=None` leaves the pack dir absent."""
    models = tmp_path / "models"
    _registry(_provider_row() + f'model_dir = "{models}"\n')
    if mib is not None:
        pack = models / PACK
        pack.mkdir(parents=True)
        with open(pack / "model.safetensors", "wb") as f:
            f.truncate(mib * 1048576)
        with open(pack / "ngram_table.bin", "wb") as f:
            f.truncate(10 * 1048576 * 1024)
    return _signal_backend()


def _serve_check(backend, *, limit, serving=False, port_open=False):
    """already_serving with the roots real and the limit `limit`; returns
    (result, limit mock)."""
    with (
        patch(f"{PS}._models_root", REAL_MODELS_ROOT),
        patch(f"{PS}._wired_limit_mb", return_value=limit) as read_limit,
        patch(f"{PS}.probe.wait_for_port_open", return_value=port_open),
        patch(f"{PS}.probe.serving_model", return_value=serving),
        patch.object(backend, "_pidproc", _pidproc_mock()),
    ):
        return backend.already_serving(backend.resolve(PACK, ())), read_limit


def test_safetensors_bytes_sums_only_safetensors(tmp_path):
    (tmp_path / "a.safetensors").write_bytes(b"x" * 10)
    (tmp_path / "b.safetensors").write_bytes(b"x" * 5)
    (tmp_path / "ngram_table.bin").write_bytes(b"x" * 1000)
    (tmp_path / "config.json").write_bytes(b"x" * 1000)
    assert process_server._safetensors_bytes(tmp_path) == 15


def test_safetensors_bytes_is_none_when_unreadable_or_empty(tmp_path):
    assert process_server._safetensors_bytes(tmp_path / "absent") is None
    (tmp_path / "ngram_table.bin").write_bytes(b"x")
    assert process_server._safetensors_bytes(tmp_path) is None


def test_need_mib_is_one_point_one_times_the_bytes():
    """The same figures as Go's TestWiredNeedIs110PercentFloored."""
    assert process_server._need_mib(49_506_491_792) == 51934
    assert process_server._need_mib(47213 * 1048576) == 51934
    assert process_server._need_mib(0) == 0


@pytest.mark.parametrize(
    ("out", "expected"),
    [
        ("59000\n68719476736\n", 59000),
        ("0\n68719476736\n", 49152),
        ("59000\n", None),
        ("", None),
        ("x\ny\n", None),
        ("-1\n68719476736\n", None),
        ("59000\n0\n", None),
    ],
)
def test_parse_wired_limit(out, expected):
    assert process_server._parse_wired_limit(out) == expected


def _sysctl(stdout="59000\n68719476736\n", returncode=0):
    return MagicMock(returncode=returncode, stdout=stdout)


def test_wired_limit_reads_one_sysctl_call():
    with (
        patch(f"{PS}.sys.platform", "darwin"),
        patch(f"{PS}.subprocess.run", return_value=_sysctl()) as run,
    ):
        assert REAL_WIRED_LIMIT() == 59000
    run.assert_called_once_with(
        ["sysctl", "-n", "iogpu.wired_limit_mb", "hw.memsize"],
        capture_output=True,
        text=True,
        check=False,
        timeout=5.0,
    )


def test_wired_limit_zero_means_three_quarters_of_memory():
    with (
        patch(f"{PS}.sys.platform", "darwin"),
        patch(f"{PS}.subprocess.run", return_value=_sysctl("0\n68719476736\n")),
    ):
        assert REAL_WIRED_LIMIT() == 49152


def test_wired_limit_is_unknown_off_darwin():
    with (
        patch(f"{PS}.sys.platform", "linux"),
        patch(f"{PS}.subprocess.run") as run,
    ):
        assert REAL_WIRED_LIMIT() is None
    run.assert_not_called()


@pytest.mark.parametrize(
    "result",
    [_sysctl("", 1), _sysctl(""), _sysctl("x\ny\n"), _sysctl("59000\n")],
)
def test_wired_limit_unreadable_results_are_none(result):
    with (
        patch(f"{PS}.sys.platform", "darwin"),
        patch(f"{PS}.subprocess.run", return_value=result),
    ):
        assert REAL_WIRED_LIMIT() is None


def test_wired_limit_oserror_is_none():
    with (
        patch(f"{PS}.sys.platform", "darwin"),
        patch(f"{PS}.subprocess.run", side_effect=OSError("no sysctl")),
    ):
        assert REAL_WIRED_LIMIT() is None


def test_already_serving_fails_with_the_fix_command_when_the_limit_is_too_low(tmp_path):
    backend = _signal_backend_with_pack(tmp_path, mib=100)  # needs 110
    with pytest.raises(LifecycleError) as err:
        _serve_check(backend, limit=50)
    message = str(err.value)
    assert FIX in message
    assert "resets on reboot" in message
    assert "50 MiB" in message
    assert "110 MiB" in message
    assert message.startswith("fake: the GPU wired limit is 50 MiB but org/pack needs about")


@pytest.mark.parametrize(("limit", "passes"), [(110, True), (109, False)])
def test_the_limit_passes_at_the_exact_need(tmp_path, limit, passes):
    backend = _signal_backend_with_pack(tmp_path, mib=100)
    if passes:
        assert _serve_check(backend, limit=limit)[0] is False
    else:
        with pytest.raises(LifecycleError):
            _serve_check(backend, limit=limit)


def test_a_zero_limit_reads_as_the_default_and_refuses_a_47213_mib_pack(tmp_path):
    """The reader turns 0 into 49152 MiB (64 GiB machine); 47213 MiB of
    safetensors need 51934 MiB, so that default refuses."""
    backend = _signal_backend_with_pack(tmp_path, mib=47213)
    with (
        patch(f"{PS}.sys.platform", "darwin"),
        patch(f"{PS}.subprocess.run", return_value=_sysctl("0\n68719476736\n")),
        patch(f"{PS}._models_root", REAL_MODELS_ROOT),
        patch(f"{PS}._wired_limit_mb", REAL_WIRED_LIMIT),
        patch(f"{PS}.probe.wait_for_port_open", return_value=False),
        patch(f"{PS}.probe.serving_model", return_value=False),
        patch.object(backend, "_pidproc", _pidproc_mock()),
        pytest.raises(LifecycleError, match=r"49152 MiB.*51934 MiB"),
    ):
        backend.already_serving(backend.resolve(PACK, ()))


def test_the_preflight_skips_when_the_pack_dir_is_unreadable(tmp_path, capsys):
    backend = _signal_backend_with_pack(tmp_path, mib=None)
    serving, read_limit = _serve_check(backend, limit=1)
    assert serving is False
    read_limit.assert_not_called()
    assert "skipping the memory check" in capsys.readouterr().err


def test_the_preflight_skips_when_the_limit_is_unknown(tmp_path, capsys):
    backend = _signal_backend_with_pack(tmp_path, mib=100)
    serving, _ = _serve_check(backend, limit=None)
    assert serving is False
    assert "skipping the memory check" in capsys.readouterr().err


@pytest.mark.parametrize(
    "failure",
    [
        subprocess.TimeoutExpired(["sysctl"], 5.0),
        UnicodeDecodeError("utf-8", b"\xff", 0, 1, "invalid start byte"),
    ],
    ids=["hung-sysctl", "garbage-bytes"],
)
def test_the_preflight_skips_when_sysctl_hangs_or_answers_garbage(tmp_path, capsys, failure):
    """A sysctl that hangs (the read is capped at 5 s, as in wt) or prints
    bytes text=True cannot decode must not hang or raise out of
    `already_serving` before any teardown: the preflight prints its skip
    line and the isolate goes on (not serving, so a start follows)."""
    backend = _signal_backend_with_pack(tmp_path, mib=100)
    with (
        patch(f"{PS}.sys.platform", "darwin"),
        patch(f"{PS}.subprocess.run", side_effect=failure) as run,
        patch(f"{PS}._models_root", REAL_MODELS_ROOT),
        patch(f"{PS}._wired_limit_mb", REAL_WIRED_LIMIT),
        patch(f"{PS}.probe.wait_for_port_open", return_value=False),
        patch(f"{PS}.probe.serving_model", return_value=False),
        patch.object(backend, "_pidproc", _pidproc_mock()),
    ):
        assert backend.already_serving(backend.resolve(PACK, ())) is False
    assert run.call_args.kwargs["timeout"] == 5.0
    assert (
        "fake: skipping the memory check (cannot read iogpu.wired_limit_mb)"
        in capsys.readouterr().err
    )


def test_a_row_without_model_dir_uses_the_default_pack_dir(tmp_path, monkeypatch):
    home = tmp_path / "home"
    pack = home / ".sushi" / "models" / PACK
    pack.mkdir(parents=True)
    with open(pack / "w.safetensors", "wb") as f:
        f.truncate(100 * 1048576)
    monkeypatch.setenv("HOME", str(home))
    _registry(_provider_row())
    backend = _signal_backend()
    with pytest.raises(LifecycleError, match="110 MiB"):
        _serve_check(backend, limit=50)


def test_the_preflight_is_skipped_when_the_pack_is_already_loaded(tmp_path):
    backend = _signal_backend_with_pack(tmp_path, mib=100)
    serving, read_limit = _serve_check(backend, limit=1, serving=True, port_open=True)
    assert serving is True
    read_limit.assert_not_called()


def test_a_provider_with_a_stop_command_never_runs_the_preflight(tmp_path):
    _signal_backend_with_pack(tmp_path, mib=100)
    backend = ProcessServerBackend(_spec(kind="http-get", warmup_seconds=400))
    serving, read_limit = _serve_check(backend, limit=1)
    assert serving is False
    read_limit.assert_not_called()


def test_the_unowned_refusal_comes_before_the_preflight(tmp_path):
    backend = _signal_backend_with_pack(tmp_path, mib=100)
    with (
        patch(f"{PS}._models_root", REAL_MODELS_ROOT),
        patch(f"{PS}._wired_limit_mb", return_value=1) as read_limit,
        patch(f"{PS}.probe.wait_for_port_open", return_value=True),
        patch.object(backend, "_pidproc", _pidproc_mock(None)),
        pytest.raises(LifecycleError, match="not started by llmbench or wt"),
    ):
        backend.already_serving(backend.resolve(PACK, ()))
    read_limit.assert_not_called()


def test_a_failed_preflight_stops_no_other_provider(tmp_path):
    backend = _signal_backend_with_pack(tmp_path, mib=100)
    with (
        patch.dict(orchestrate.BACKENDS, {"fake": backend}),
        patch.object(orchestrate, "_stop_others") as stop_others,
        patch.object(backend, "check_available", return_value=None),
        patch(f"{PS}._models_root", REAL_MODELS_ROOT),
        patch(f"{PS}._wired_limit_mb", return_value=50),
        patch(f"{PS}.probe.wait_for_port_open", return_value=False),
        patch(f"{PS}.probe.serving_model", return_value=False),
        patch.object(backend, "_pidproc", _pidproc_mock()) as pidproc,
    ):
        result = orchestrate.isolate("fake", PACK)
    assert result.ok is False
    assert result.error is not None
    assert "wired_limit_mb=59000" in result.error
    stop_others.assert_not_called()
    pidproc.stop.assert_not_called()
    pidproc.spawn.assert_not_called()


def test_the_memory_refusal_markers_are_pinned():
    """The same eight phrases as wt's memoryRefusalMarkers (pinned there too),
    so the two heuristics cannot drift. The bare word "iogpu" is not one: it
    shows up in unrelated startup lines and would send the user to raise a
    limit that was never the problem."""
    from llmbench.providers.lifecycle.backends.process_server import _MEMORY_REFUSAL

    phrases = [
        "out of memory",
        "insufficient memory",
        "not enough memory",
        "cannot allocate",
        "failed to allocate",
        "memory limit",
        "wired limit",
        "wired_limit",
    ]
    assert _MEMORY_REFUSAL.pattern == "|".join(phrases)
    for phrase in phrases:
        assert _MEMORY_REFUSAL.search(f"HTTP 500: {phrase.upper()} while loading")
    for unrelated in ("IOGPU command buffer", "iogpu family ready", "No default model configured"):
        assert _MEMORY_REFUSAL.search(unrelated) is None


def test_empty_stop_reports_a_server_that_outlived_sigterm_and_sigkill():
    """A server that is still running after both signals keeps its pidfile and
    says so, rather than reading as a clean stop or as one nobody owns."""
    backend = _signal_backend()
    with (
        patch(f"{PS}.probe.port_closed_within", side_effect=AssertionError("waited")),
        patch.object(backend, "_pidproc", _pidproc_mock(gone=False)),
    ):
        message = backend.stop_and_wait()
    assert message is not None
    assert "pid 4242" in message
    assert "did not exit" in message
    assert "pidfile was kept" in message
