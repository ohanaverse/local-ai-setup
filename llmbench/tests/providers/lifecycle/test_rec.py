"""rec end to end through the orchestrator: isolate / stop / stop_all / restore.

rec has no Python of its own: it is the generic `ProcessServerBackend` run off
its entry in `docs/contracts/process-providers.toml`. These tests drive the
real `orchestrate` functions and the real rec backend against a fake `rec`
world: a fake `rec` CLI (`status` / `stop`, through `subprocess.run`), a fake
`rec serve` process (through `subprocess.Popen`, under a real
`PidfileProcess` writing into tmp_path) and a fake HTTP server at the
registry's address that refuses any request not carrying `Bearer k`. Every
other backend's `stop_and_wait` (and, for restore, `restore`/`unload_models`)
is a recorder, so nothing reaches a real provider.
"""

from __future__ import annotations

import io
import json
import os
import subprocess
import urllib.error
import urllib.request
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any
from unittest.mock import MagicMock

import pytest

from llmbench.providers.lifecycle import orchestrate
from llmbench.providers.lifecycle.backends import BACKENDS
from llmbench.providers.lifecycle.pidproc import PidfileProcess

PS = "llmbench.providers.lifecycle.backends.process_server"
ORCH = "llmbench.providers.lifecycle.orchestrate"
BIN = "/opt/fake/bin/rec"
REC_ENDPOINT = "http://127.0.0.1:11500/v1"
REC_CHAT_URL = "http://127.0.0.1:11500/v1/chat/completions"
KEY = "k"

REC = BACKENDS["rec"]


def _write_registry(secret_ref: str = KEY, *models: str) -> None:
    names = models or ("flint",)
    text = (
        '[[providers]]\nid = "rec"\nlocation = "local"\n'
        f'auth = {{ base_url = "{REC_ENDPOINT}", secret_ref = "{secret_ref}" }}\n'
    )
    for name in names:
        text += (
            f'[[models]]\nid = "rec/{name}"\nfamily = "f"\n'
            f'provider_id = "rec"\nmodel_name = "{name}"\n'
        )
    Path(os.environ["WT_REGISTRY"]).write_text(text)


class _Resp(io.BytesIO):
    def __enter__(self) -> _Resp:
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()


@dataclass
class FakeRec:
    """The rec CLI, its serve process and its HTTP port, as one state machine."""

    events: list[str] = field(default_factory=list)
    running: bool = False
    ready: bool = False
    model: str = "flint"
    endpoint: str = REC_ENDPOINT
    serve_model: str = "flint"
    polls_until_ready: int = 2
    stop_works: bool = True
    die_on_spawn: int | None = None
    die_after_polls: int | None = None
    spawn_argv: list[str] | None = None
    status_polls_after_spawn: int = 0
    stop_argvs: list[list[str]] = field(default_factory=list)
    # (url, Authorization header, parsed JSON body or None)
    requests: list[tuple[str, str | None, Any]] = field(default_factory=list)
    _pending: int = 0
    _spawned: bool = False

    def serving(self, *, model: str = "flint", endpoint: str = REC_ENDPOINT) -> FakeRec:
        self.running = self.ready = True
        self.model = model
        self.endpoint = endpoint
        return self

    # --- `rec status` / `rec stop` (subprocess.run) ---------------------

    def run(self, argv: list[str], **_kw: object) -> subprocess.CompletedProcess[str]:
        verb = argv[1] if len(argv) > 1 else ""
        if verb == "status":
            if self._spawned:
                self.status_polls_after_spawn += 1
            if not self.running:
                return subprocess.CompletedProcess(argv, 1, stdout="", stderr="no instance")
            if not self.ready:
                self._pending -= 1
                if self._pending <= 0:
                    self.ready = True
            body = {"ready": self.ready, "model": self.model, "endpoint": self.endpoint}
            return subprocess.CompletedProcess(argv, 0, stdout=json.dumps(body), stderr="")
        if verb == "stop":
            self.events.append("rec stop")
            self.stop_argvs.append(list(argv))
            was_running = self.running
            if self.stop_works:
                self.running = self.ready = False
            return subprocess.CompletedProcess(argv, 0 if was_running else 1, stdout="", stderr="")
        raise AssertionError(f"unexpected rec invocation: {argv}")

    # --- `rec serve` (subprocess.Popen) ----------------------------------

    def popen(self, argv: list[str], *, stdout: Any = None, **_kw: object) -> MagicMock:
        self.events.append("spawn")
        self.spawn_argv = list(argv)
        self._spawned = True
        proc = MagicMock()
        proc.pid = 4242
        if self.die_on_spawn is not None:
            stdout.write(b"fatal: cannot load flint weights\n")
            stdout.flush()
            proc.poll.return_value = self.die_on_spawn
            proc.returncode = self.die_on_spawn
            return proc
        self.running, self.ready = True, False
        self.model, self.endpoint = self.serve_model, REC_ENDPOINT
        self._pending = self.polls_until_ready
        if self.die_after_polls is not None:
            # poll() is None at spawn's own check, then dead once loading.
            codes: list[int | None] = [None] * self.die_after_polls
            proc.poll.side_effect = lambda: codes.pop(0) if codes else 9
            proc.returncode = 9
        else:
            proc.poll.return_value = None
        return proc

    # --- the HTTP port (urlopen) ------------------------------------------

    def urlopen(self, target: str | urllib.request.Request, timeout: float = 0) -> _Resp:
        if isinstance(target, urllib.request.Request):
            url, auth, data = target.full_url, target.get_header("Authorization"), target.data
        else:
            url, auth, data = target, None, None
        body = json.loads(data) if isinstance(data, bytes) else None
        self.requests.append((url, auth, body))
        if not url.startswith("http://127.0.0.1:11500/") or not (self.running and self.ready):
            raise urllib.error.URLError(ConnectionRefusedError(61, "Connection refused"))
        if auth != f"Bearer {KEY}":
            raise urllib.error.HTTPError(url, 401, "Unauthorized", {}, io.BytesIO(b"{}"))  # type: ignore[arg-type]
        if body is not None:
            return _Resp(json.dumps({"object": "chat.completion", "model": body["model"]}).encode())
        return _Resp(json.dumps({"data": [{"id": self.model}]}).encode())


@pytest.fixture
def fake_rec(monkeypatch, tmp_path):
    """A rec world plus recorders on every other backend's teardown."""
    fake = FakeRec()
    _write_registry()
    guarded_run = subprocess.run
    real_popen = subprocess.Popen

    def run(cmd, *args, **kwargs):
        argv = [str(a) for a in cmd] if isinstance(cmd, list | tuple) else [str(cmd)]
        if os.path.basename(argv[0]) == "rec":
            return fake.run(argv, **kwargs)
        return guarded_run(cmd, *args, **kwargs)

    def popen(cmd, *args, **kwargs):
        argv = [str(a) for a in cmd] if isinstance(cmd, list | tuple) else [str(cmd)]
        if os.path.basename(argv[0]) == "rec":
            return fake.popen(argv, **kwargs)
        return real_popen(cmd, *args, **kwargs)

    # subprocess and urllib.request are shared module objects: these reach
    # process_server, probe, pidproc and secret alike.
    monkeypatch.setattr(subprocess, "run", run)
    monkeypatch.setattr(subprocess, "Popen", popen)
    monkeypatch.setattr(urllib.request, "urlopen", fake.urlopen)
    monkeypatch.setattr(f"{PS}.binaries.require_binary", lambda name: f"/opt/fake/bin/{name}")
    # Readiness and port polls sleep between attempts; a fake answers at once.
    monkeypatch.setattr("llmbench.providers.lifecycle.probe.time.sleep", lambda _s: None)
    monkeypatch.setattr(f"{PS}.probe.STOP_WAIT_TIMEOUT", 0.3)
    monkeypatch.setattr(
        REC,
        "_pidproc",
        PidfileProcess(
            name="rec", pidfile=str(tmp_path / "rec.pid"), logfile=str(tmp_path / "rec.log")
        ),
    )
    monkeypatch.setattr(f"{ORCH}._log", lambda _m: None)
    for backend in BACKENDS.values():
        if backend is REC:
            continue
        monkeypatch.setattr(
            backend,
            "stop_and_wait",
            MagicMock(side_effect=lambda bid=backend.id: fake.events.append(f"stop {bid}")),
        )
    return fake


def _other_stops(fake: FakeRec) -> list[str]:
    return [e for e in fake.events if e.startswith("stop ")]


def _expected_other_stops() -> set[str]:
    keys = {b.occupancy_key for b in BACKENDS.values() if b is not REC}
    return {f"stop {k}" for k in keys}


# --- isolate ---------------------------------------------------------------


def test_isolate_rec_stops_others_then_spawns(fake_rec):
    result = orchestrate.isolate("rec")

    assert result.ok is True, result.error
    assert result.model == "flint"
    assert result.direct_url == REC_CHAT_URL
    # Every other occupancy domain stopped, all before the spawn.
    spawn_at = fake_rec.events.index("spawn")
    others = _other_stops(fake_rec)
    assert set(others) == _expected_other_stops()
    assert all(fake_rec.events.index(e) < spawn_at for e in others)
    assert fake_rec.spawn_argv == [BIN, "serve", "--port", "11500"]
    # Readiness was polled until `rec status` said ready (2 polls), with the
    # model and endpoint the fake reports.
    assert fake_rec.status_polls_after_spawn >= 2
    assert (fake_rec.ready, fake_rec.model, fake_rec.endpoint) == (True, "flint", REC_ENDPOINT)
    # The warmup chat went to rec's address, keyed, naming flint.
    chats = [(u, a, b) for u, a, b in fake_rec.requests if b is not None]
    assert len(chats) == 1
    url, auth, body = chats[0]
    assert url == REC_CHAT_URL
    keyed = auth == f"Bearer {KEY}"
    assert keyed
    assert body["model"] == "flint"
    # The warmup's health probe carried the key too (rec refuses every
    # route without it).
    health = [a for u, a, b in fake_rec.requests if b is None and u.endswith("/v1/models")]
    keyed_health = bool(health) and health[-1] == f"Bearer {KEY}"
    assert keyed_health


def test_isolate_rec_reuses_a_right_instance(fake_rec):
    fake_rec.serving(model="flint", endpoint=REC_ENDPOINT)

    result = orchestrate.isolate("rec")

    assert result.ok is True, result.error
    assert result.model == "flint"
    assert "spawn" not in fake_rec.events
    assert "rec stop" not in fake_rec.events
    # The keep path still stops every other provider.
    assert set(_other_stops(fake_rec)) == _expected_other_stops()
    chats = [b for _u, _a, b in fake_rec.requests if b is not None]
    assert [b["model"] for b in chats] == ["flint"]


def test_isolate_rec_replaces_another_model(fake_rec):
    _assert_replaced(fake_rec, model="granite", endpoint=REC_ENDPOINT)


def test_isolate_rec_replaces_the_right_model_on_another_address(fake_rec):
    _assert_replaced(fake_rec, model="flint", endpoint="http://127.0.0.1:11600/v1")


def _assert_replaced(fake_rec: FakeRec, *, model: str, endpoint: str) -> None:
    """A running instance that is not the one asked for is stopped (and its
    status confirmed not ready) before rec serve is spawned."""
    fake_rec.serving(model=model, endpoint=endpoint)

    result = orchestrate.isolate("rec")

    assert result.ok is True, result.error
    assert result.model == "flint"
    stop_at = fake_rec.events.index("rec stop")
    assert fake_rec.events.index("spawn") > stop_at
    assert fake_rec.stop_argvs == [[BIN, "stop"]]
    assert fake_rec.spawn_argv == [BIN, "serve", "--port", "11500"]


def _leaky_secret_command(tmp_path: Path) -> str:
    """An exec: reference to a command that prints a key-like text and then
    fails: the key text must surface nowhere."""
    script = tmp_path / "secret-cmd"
    script.write_text("#!/bin/sh\necho LEAKEDKEYTEXT\necho LEAKEDKEYTEXT >&2\nexit 3\n")
    script.chmod(0o755)
    return f"exec:{script}"


@pytest.mark.parametrize("ref_kind", ["missing-command", "failing-command"])
def test_isolate_rec_unreadable_key(fake_rec, tmp_path, capsys, ref_kind):
    secret_ref = (
        "exec:/nonexistent/k" if ref_kind == "missing-command" else _leaky_secret_command(tmp_path)
    )
    _write_registry(secret_ref)

    result = orchestrate.isolate("rec")

    assert result.ok is False
    assert (result.error or "").startswith("rec: cannot read the provider key")
    assert "spawn" not in fake_rec.events
    # Failed before any teardown and before any request.
    assert fake_rec.events == []
    assert fake_rec.requests == []
    out = capsys.readouterr()
    leaked = any("LEAKEDKEYTEXT" in s for s in (result.error or "", out.out, out.err))
    assert not leaked
    assert "Bearer" not in (result.error or "")


def test_isolate_rec_missing_binary(fake_rec, monkeypatch):
    def require(name: str) -> str:
        from llmbench.providers.lifecycle.envelope import LifecycleError

        raise LifecycleError(f"{name} binary not found on PATH")

    monkeypatch.setattr(f"{PS}.binaries.require_binary", require)

    result = orchestrate.isolate("rec")

    assert result.ok is False
    assert result.error == "rec binary not found on PATH"
    assert fake_rec.events == []


def test_rec_process_dying_fails_fast_with_log_tail(fake_rec, tmp_path):
    fake_rec.die_on_spawn = 3

    result = orchestrate.isolate("rec")

    assert result.ok is False
    error = result.error or ""
    assert error.startswith("rec exited immediately (exit 3)")
    assert "fatal: cannot load flint weights" in error
    # Fast: no readiness poll ever ran against the dead process, and rec is
    # not cleaned up afterwards (cleanup_on_failure is mtplx's alone).
    assert fake_rec.status_polls_after_spawn == 0
    assert fake_rec.events.count("rec stop") == 1  # the pre-spawn replace only


def test_rec_process_dying_during_load_fails_fast(fake_rec):
    fake_rec.die_after_polls = 1
    fake_rec.polls_until_ready = 10_000

    result = orchestrate.isolate("rec")

    assert result.ok is False
    assert "exited during model load" in (result.error or "")
    assert "exit 9" in (result.error or "")
    assert fake_rec.status_polls_after_spawn <= 1


# --- stop / stop_all / restore -----------------------------------------------


def test_stop_rec(fake_rec):
    fake_rec.serving()

    result = orchestrate.stop("rec")

    assert result.ok is True, result.error
    assert fake_rec.stop_argvs == [[BIN, "stop"]]
    assert fake_rec.running is False
    assert _other_stops(fake_rec) == []


def test_stop_rec_reports_an_instance_that_stays_up(fake_rec):
    fake_rec.serving()
    fake_rec.stop_works = False

    result = orchestrate.stop("rec")

    assert result.ok is False
    assert result.error == "rec still listening on port 11500"


def test_stop_all_includes_rec(fake_rec):
    fake_rec.serving()

    assert orchestrate.stop_all().ok is True
    assert fake_rec.stop_argvs == [[BIN, "stop"]]
    assert fake_rec.running is False
    assert set(_other_stops(fake_rec)) == _expected_other_stops()


def test_stop_all_keep_rec_leaves_it_running(fake_rec):
    fake_rec.serving()

    assert orchestrate.stop_all(keep="rec").ok is True
    assert fake_rec.stop_argvs == []
    assert fake_rec.running is True


@pytest.fixture
def quiet_restore(fake_rec, monkeypatch):
    """Recorders on every other backend's restore/unload and on LiteLLM."""
    for backend in BACKENDS.values():
        if backend is REC:
            continue
        monkeypatch.setattr(backend, "restore", MagicMock(return_value=None))
        monkeypatch.setattr(backend, "unload_models", MagicMock(return_value=None))
    monkeypatch.setattr(f"{ORCH}._restore_litellm", lambda: None)
    return fake_rec


@pytest.mark.parametrize("unload", [False, True])
def test_restore_stops_a_running_rec(quiet_restore, unload):
    fake = quiet_restore.serving()

    result = orchestrate.restore(unload=unload)

    assert result.ok is True, result.error
    assert fake.stop_argvs == [[BIN, "stop"]]
    assert fake.running is False
    assert "spawn" not in fake.events


def test_restore_never_fails_on_a_rec_that_stays_up(quiet_restore):
    fake = quiet_restore.serving()
    fake.stop_works = False

    assert orchestrate.restore().ok is True
    assert fake.stop_argvs == [[BIN, "stop"]]


def test_rec_is_never_in_the_standing_baseline(quiet_restore, monkeypatch):
    rec_restore = MagicMock()
    monkeypatch.setattr(REC, "restore", rec_restore)
    monkeypatch.setattr(REC, "start", MagicMock())

    assert orchestrate.restore().ok is True

    assert REC.restore_action == "stop"
    rec_restore.assert_not_called()
    REC.start.assert_not_called()  # type: ignore[attr-defined]
    assert "spawn" not in quiet_restore.events
    assert "rec" not in {b.id for b in BACKENDS.values() if b.restore_action == "restart"}


def test_patches_reach_the_rec_backend():
    """Guard the fixture: rec is the generic backend the patches target."""
    from llmbench.providers.lifecycle.backends.process_server import ProcessServerBackend

    assert isinstance(REC, ProcessServerBackend)
    assert REC.cleanup_on_failure is False
