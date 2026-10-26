"""The process-provider contract file, read through process_spec."""

from __future__ import annotations

from pathlib import Path

import pytest

from llmbench.providers import process_spec as ps

GOOD = """\
[[process]]
family = "x"
provider = "x"
binary = "x"
spawn = ["serve", "{port}"]
stop = ["stop"]
pidfile = "/tmp/x.pid"
log = "/tmp/x.log"
pidfile_env = "X_PID"
subcommand = "serve"
daemon_module = ""
port_flag = "--port"
model_flag = ""
scope = "machine"
default_origin = "http://127.0.0.1:1"
wt_port = 1
restore_action = "stop"

[process.ready]
kind = "models-list"
command = []
path = ""
equals = ""
model_path = ""
endpoint_path = ""
"""


def _use(monkeypatch, tmp_path: Path, text: str, name: str = "pp.toml") -> None:
    f = tmp_path / name
    f.write_text(text)
    monkeypatch.setenv("LLMBENCH_PROCESS_PROVIDERS", str(f))


def test_swapping_the_env_var_needs_no_cache_clear(monkeypatch, tmp_path):
    _use(monkeypatch, tmp_path, GOOD, "a.toml")
    assert set(ps.process_specs()) == {"x"}
    _use(monkeypatch, tmp_path, GOOD.replace('provider = "x"', 'provider = "y"'), "b.toml")
    assert set(ps.process_specs()) == {"y"}
    _use(monkeypatch, tmp_path, GOOD, "a.toml")
    assert set(ps.process_specs()) == {"x"}


def test_real_contract_file_exists_at_computed_path(monkeypatch):
    monkeypatch.delenv("LLMBENCH_PROCESS_PROVIDERS", raising=False)
    assert ps.contract_path().is_file()


def test_mtplx_values():
    s = ps.process_spec("mtplx")
    assert s is not None
    assert s.binary == "mtplx"
    assert s.spawn[:3] == ("serve", "--model", "{model}")
    assert s.stop == ("stop", "--port", "{port}", "--grace-seconds", "10")
    assert s.pidfile == "/tmp/local-ai-setup-mtplx.pid"
    assert s.log == "/tmp/local-ai-setup-mtplx.log"
    assert s.wt_port == 8003
    assert s.ready.kind == "models-list"


def test_rec_values():
    s = ps.process_spec("rec")
    assert s is not None
    assert s.spawn == ("serve", "--port", "{port}")
    assert s.stop == ("stop",)
    assert s.scope == "machine"
    assert s.wt_port == 11500
    assert s.ready.kind == "command-json"
    assert s.ready.command == ("status",)
    assert (s.ready.path, s.ready.equals) == ("ready", "true")
    assert (s.ready.model_path, s.ready.endpoint_path) == ("model", "endpoint")


def test_unknown_provider_is_none():
    assert ps.process_spec("nope") is None


def test_expand():
    s = ps.process_spec("mtplx")
    assert s is not None
    out = ps.expand(s.spawn, model="m1", port=8003)
    assert out[:5] == ["serve", "--model", "m1", "--port", "8003"]
    assert "{model}" not in out
    assert ps.expand(("stop",)) == ["stop"]


def test_synthetic_good_file(monkeypatch, tmp_path):
    _use(monkeypatch, tmp_path, GOOD)
    assert set(ps.process_specs()) == {"x"}


def test_missing_key_raises(monkeypatch, tmp_path):
    _use(monkeypatch, tmp_path, GOOD.replace('pidfile = "/tmp/x.pid"\n', ""))
    with pytest.raises(ps.ProcessSpecError, match="pidfile"):
        ps.process_specs()


def test_missing_ready_key_raises(monkeypatch, tmp_path):
    _use(monkeypatch, tmp_path, GOOD.replace('equals = ""\n', ""))
    with pytest.raises(ps.ProcessSpecError, match="equals"):
        ps.process_specs()


def test_unknown_ready_kind_raises(monkeypatch, tmp_path):
    _use(monkeypatch, tmp_path, GOOD.replace("models-list", "bogus"))
    with pytest.raises(ps.ProcessSpecError, match="bogus"):
        ps.process_specs()
