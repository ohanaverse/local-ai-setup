"""resolve_secret: wt's three reference forms, and no secret in any error."""

from __future__ import annotations

import stat
import subprocess
from pathlib import Path

import pytest

from llmbench.providers import secret
from llmbench.providers.lifecycle.envelope import LifecycleError


def _script(tmp_path: Path, body: str) -> Path:
    p = tmp_path / "keycmd"
    p.write_text("#!/bin/sh\n" + body + "\n")
    p.chmod(p.stat().st_mode | stat.S_IXUSR)
    return p


def test_exec_form_strips_stdout(tmp_path):
    p = _script(tmp_path, 'echo "  tok-$1  "')
    assert secret.resolve_secret(f"exec:{p} abc") == "tok-abc"


def test_exec_failure_names_ref_not_output(tmp_path):
    p = _script(tmp_path, "echo SECRETVALUE; echo SECRETERR >&2; exit 3")
    ref = f"exec:{p}"
    with pytest.raises(secret.SecretError) as ei:
        secret.resolve_secret(ref)
    msg = str(ei.value)
    assert isinstance(ei.value, LifecycleError)
    assert (ref in msg, "SECRETVALUE" in msg, "SECRETERR" in msg) == (True, False, False)


def test_exec_missing_binary_is_secret_error(tmp_path):
    with pytest.raises(secret.SecretError):
        secret.resolve_secret(f"exec:{tmp_path}/does-not-exist")


def test_exec_empty_command_is_secret_error():
    with pytest.raises(secret.SecretError):
        secret.resolve_secret("exec:")


@pytest.mark.parametrize("body", ["exit 0", 'echo "   "'])
def test_exec_no_output_is_secret_error(tmp_path, body):
    p = _script(tmp_path, body)
    ref = f"exec:{p}"
    with pytest.raises(secret.SecretError) as ei:
        secret.resolve_secret(ref)
    assert ref in str(ei.value)


def test_exec_timeout(tmp_path, monkeypatch):
    # tests/conftest.py makes os.kill a no-op (os is one shared module), so
    # subprocess.run's kill at the timeout does nothing here and the call waits
    # out the script's sleep; production kills the child at the timeout (this
    # body takes ~0.2 s outside pytest). Keep the sleep short.
    p = _script(tmp_path, "sleep 1")
    monkeypatch.setattr(secret, "EXEC_TIMEOUT_S", 0.2)
    ref = f"exec:{p}"
    with pytest.raises(secret.SecretError) as ei:
        secret.resolve_secret(ref)
    assert ref in str(ei.value)
    assert isinstance(ei.value.__cause__, subprocess.TimeoutExpired)


def test_env_name_form(monkeypatch):
    monkeypatch.setenv("MY_KEY_1", "val")
    assert secret.resolve_secret("MY_KEY_1") == "val"
    monkeypatch.delenv("MY_KEY_1")
    assert secret.resolve_secret("MY_KEY_1") == ""


def test_os_environ_form(monkeypatch):
    monkeypatch.setenv("some_key", "v2")
    assert secret.resolve_secret("os.environ/some_key") == "v2"
    monkeypatch.delenv("some_key")
    assert secret.resolve_secret("os.environ/some_key") == ""


def test_literal():
    assert secret.resolve_secret("sk-abc123") == "sk-abc123"
    assert secret.resolve_secret("lower") == "lower"
    assert secret.resolve_secret("") == ""
