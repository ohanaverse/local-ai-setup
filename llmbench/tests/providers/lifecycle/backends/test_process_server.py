"""The generic, spec-driven process-server backend (mtplx and rec run on it).

Synthetic specs and fakes only: every probe, the binary lookup, the stop
command and the pidfile process are patched where `process_server` imports
them, and the registry is a scratch file named by WT_REGISTRY (conftest
already points WT_REGISTRY into tmp_path).
"""

from __future__ import annotations

import dataclasses
import os
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from llmbench.providers.lifecycle import probe
from llmbench.providers.lifecycle.backends import BACKENDS, process_server
from llmbench.providers.lifecycle.backends.mtplx import MTPLX
from llmbench.providers.lifecycle.backends.process_server import (
    INSTANCE_GONE_TIMEOUT,
    ProcessServerBackend,
)
from llmbench.providers.lifecycle.envelope import LifecycleError
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
) -> ProcessSpec:
    if kind == "models-list":
        ready = Ready(kind, (), "", "", "", "")
    else:
        ready = Ready(kind, ("status",), "ready", "true", model_path, endpoint_path)
    return ProcessSpec(
        family=provider,
        provider=provider,
        binary="fake",
        spawn=("serve", "-m", "{model}", "-p", "{port}", "--id", "{model}"),
        stop=("stop", "--port", "{port}"),
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
        ready=ready,
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


def test_mtplx_is_the_only_backend_that_cleans_up_on_failure():
    assert [b.id for b in BACKENDS.values() if b.cleanup_on_failure] == ["mtplx"]


def test_rec_is_registered_generically():
    rec = BACKENDS["rec"]
    assert isinstance(rec, ProcessServerBackend)
    assert type(rec) is type(MTPLX)
    assert rec.cleanup_on_failure is False
    assert rec.restore_action == "stop"
    assert rec.occupancy_key == "rec"


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
    ):
        assert backend.stop_and_wait() is None
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
