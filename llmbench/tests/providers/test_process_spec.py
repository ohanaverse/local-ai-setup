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
warmup_seconds = 120

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


@pytest.mark.parametrize(
    ("old", "new", "key"),
    [
        ('spawn = ["serve", "{port}"]', 'spawn = "serve"', "spawn"),
        ('stop = ["stop"]', 'stop = "stop"', "stop"),
        ("command = []", 'command = "status"', "command"),
        ('stop = ["stop"]', "stop = [1]", "stop"),
    ],
)
def test_an_argv_must_be_a_list_of_strings(monkeypatch, tmp_path, old, new, key):
    """A bare string would otherwise become a tuple of its characters."""
    _use(monkeypatch, tmp_path, GOOD.replace(old, new))
    with pytest.raises(ps.ProcessSpecError, match=f"`{key}` must be a list of strings"):
        ps.process_specs()


def test_mtplx_and_rec_warm_for_120_seconds():
    for provider in ("mtplx", "rec"):
        s = ps.process_spec(provider)
        assert s is not None
        assert s.warmup_seconds == 120


def test_missing_warmup_seconds_raises(monkeypatch, tmp_path):
    _use(monkeypatch, tmp_path, GOOD.replace("warmup_seconds = 120\n", ""))
    with pytest.raises(ps.ProcessSpecError, match=r"process\[0\]: missing key `warmup_seconds`"):
        ps.process_specs()


@pytest.mark.parametrize("bad", ['"120"', "0", "-5", "true", "1.5", '"abc"'])
def test_warmup_seconds_must_be_a_positive_integer(monkeypatch, tmp_path, bad):
    _use(monkeypatch, tmp_path, GOOD.replace("warmup_seconds = 120", f"warmup_seconds = {bad}"))
    with pytest.raises(
        ps.ProcessSpecError, match=r"process\[0\]: `warmup_seconds` must be a positive integer"
    ):
        ps.process_specs()


def test_wt_port_that_is_not_an_integer_is_a_spec_error_not_a_value_error(monkeypatch, tmp_path):
    _use(monkeypatch, tmp_path, GOOD.replace("wt_port = 1", 'wt_port = "abc"'))
    with pytest.raises(
        ps.ProcessSpecError, match=r"process\[0\]: `wt_port` must be a positive integer"
    ):
        ps.process_specs()


def test_empty_stop_list_parses_to_an_empty_tuple(monkeypatch, tmp_path):
    _use(monkeypatch, tmp_path, GOOD.replace('stop = ["stop"]', "stop = []"))
    assert ps.process_specs()["x"].stop == ()


HTTP_GET = GOOD.replace(
    'kind = "models-list"\ncommand = []\npath = ""',
    'kind = "http-get"\ncommand = []\npath = "/health"',
)


def test_http_get_ready_kind_parses(monkeypatch, tmp_path):
    _use(monkeypatch, tmp_path, HTTP_GET)
    ready = ps.process_specs()["x"].ready
    assert ready.kind == "http-get"
    assert ready.path == "/health"
    assert ready.command == ()


def test_http_get_needs_a_path(monkeypatch, tmp_path):
    _use(monkeypatch, tmp_path, HTTP_GET.replace('path = "/health"', 'path = ""'))
    with pytest.raises(ps.ProcessSpecError, match=r"process\[0\].ready: .*`path`"):
        ps.process_specs()


def test_http_get_takes_no_command(monkeypatch, tmp_path):
    _use(monkeypatch, tmp_path, HTTP_GET.replace("command = []", 'command = ["status"]'))
    with pytest.raises(ps.ProcessSpecError, match=r"process\[0\].ready: .*`command`"):
        ps.process_specs()


@pytest.mark.parametrize("path", ["health", "x"])
def test_http_get_path_must_start_with_a_slash(monkeypatch, tmp_path, path):
    _use(monkeypatch, tmp_path, HTTP_GET.replace('path = "/health"', f'path = "{path}"'))
    with pytest.raises(ps.ProcessSpecError, match=r"process\[0\].ready: .*starting with /"):
        ps.process_specs()


def test_http_get_path_with_a_slash_is_accepted(monkeypatch, tmp_path):
    _use(monkeypatch, tmp_path, HTTP_GET)
    assert ps.process_specs()["x"].ready.path == "/health"


EMPTY_STOP = GOOD.replace('stop = ["stop"]', "stop = []")


@pytest.mark.parametrize(
    "line, field",
    [
        ('pidfile = "/tmp/x.pid"', "pidfile"),
        ('subcommand = "serve"', "subcommand"),
        ('port_flag = "--port"', "port_flag"),
    ],
)
def test_empty_stop_requires_pidfile_subcommand_and_port_flag(monkeypatch, tmp_path, line, field):
    _use(monkeypatch, tmp_path, EMPTY_STOP.replace(line, f'{field} = ""'))
    with pytest.raises(ps.ProcessSpecError, match=rf"process\[0\]: empty `stop` needs .*`{field}`"):
        ps.process_specs()


def test_empty_stop_with_its_companions_is_accepted(monkeypatch, tmp_path):
    _use(monkeypatch, tmp_path, EMPTY_STOP)
    assert ps.process_specs()["x"].stop == ()


def test_stop_must_not_contain_empty_arguments(monkeypatch, tmp_path):
    _use(monkeypatch, tmp_path, GOOD.replace('stop = ["stop"]', 'stop = [""]'))
    with pytest.raises(ps.ProcessSpecError, match=r"process\[0\]: `stop` must not contain empty"):
        ps.process_specs()
