"""Guards on the autouse suite-hermeticity fixtures in tests/conftest.py.

The lifecycle backends drive real local-model binaries (`omlx stop`,
`mtplx stop`, `rec stop`, `sushi serve`, `mlx_lm.server`) and real
LaunchAgents. Until one global allow-listing wrapper covered all of them,
only per-test stubbing stood between the suite and the developer's live
models — one new test that forgot to stub would tear down a model an agent
was mid-request against. These tests fail loudly if that wrapper stops
covering a binary, or if it ever stops delegating everything else to the
real subprocess.run (which would silently neuter git/pi/pytest subprocess
calls elsewhere in the suite). The same holds for a direct `subprocess.Popen`
(`PidfileProcess.spawn`), guarded by the autouse `_GuardedPopen`.
"""

from __future__ import annotations

import dataclasses
import subprocess

import pytest


@pytest.mark.parametrize(
    "argv",
    [
        ["launchctl", "kickstart", "-k", "gui/501/local.litellm.proxy"],
        ["omlx", "stop"],
        ["mtplx", "stop", "--port", "8003", "--grace-seconds", "10"],
        # Absolute paths, because binaries.require_binary() and
        # resolve_mlx_lm_bin() both hand the backends a resolved path —
        # the allow-list has to match on the BASENAME, not argv[0] whole.
        ["/opt/homebrew/bin/mtplx", "stop"],
        # rec is installed on the developer's machine; `rec stop` would tear
        # down a live model and `rec serve` load one. A flag the real rec
        # rejects, plus an absolute path, as for mtplx.
        ["rec", "--llmbench-hermetic-no-such-flag"],
        ["/Users/nobody/.local/bin/rec", "stop"],
        # sushi is installed on the developer's machine and `sushi serve`
        # loads a 46 GB pack, so no case may name a path that resolves to it:
        # unfaked, a path that does not exist raises FileNotFoundError and
        # the test fails without starting anything.
        ["/Users/nobody/.local/bin/sushi", "serve"],
        ["/Users/nobody/.local/bin/sushi", "--llmbench-hermetic-no-such-flag"],
        ["/opt/homebrew/Cellar/omlx/0.10.0/libexec/bin/mlx_lm.server", "--model", "x"],
    ],
)
def test_live_provider_binaries_are_never_actually_executed(argv):
    """Each argv is either a flag the real binary would REJECT (non-zero
    exit) or a path that does not exist on most machines (raising
    FileNotFoundError) — so a canned returncode 0 is only possible if the
    autouse wrapper short-circuited before touching the real process."""
    result = subprocess.run(argv, capture_output=True, check=False)
    assert result.returncode == 0


def test_unrelated_commands_still_reach_the_real_subprocess_run():
    """The wrapper must delegate everything outside the allow-list: the
    suite legitimately shells out to git (benchmark/agent/workspace.py),
    `pi --version`, and the agent-benchmark gate runners, and a blanket
    mock here would silently break all of them."""
    result = subprocess.run(["/bin/echo", "hermetic"], capture_output=True, text=True, check=False)
    assert result.stdout.strip() == "hermetic"


def test_signal_stop_backends_get_scratch_pidfiles(monkeypatch, tmp_path):
    """llmbench has no pidfile override variable, so the autouse
    `_redirect_signal_stop_pidfiles` swaps every signal-stop backend's
    PidfileProcess for one over scratch files: an unpatched stop or
    ownership check reads (and may remove) only those. Proven on a synthetic
    signal-stop backend beside a stop-command one, which is left alone."""
    from llmbench.providers.lifecycle.backends import BACKENDS
    from llmbench.providers.lifecycle.backends.process_server import ProcessServerBackend
    from llmbench.providers.process_spec import process_spec
    from tests.conftest import redirect_signal_stop_pidfiles

    rec_spec = process_spec("rec")
    assert rec_spec is not None
    signal_stop = ProcessServerBackend(
        dataclasses.replace(
            rec_spec, provider="sushix", stop=(), pidfile="/tmp/local-ai-setup-sushix.pid"
        )
    )
    stop_command = ProcessServerBackend(rec_spec)
    kept = stop_command._pidproc
    redirect_signal_stop_pidfiles(
        {"sushix": signal_stop, "rec": stop_command}, monkeypatch, tmp_path
    )
    assert signal_stop._pidproc.pidfile == str(tmp_path / "sushix.pid")
    assert signal_stop._pidproc.logfile == str(tmp_path / "sushix.log")
    assert signal_stop._pidproc.name == "sushix"
    assert stop_command._pidproc is kept
    # The fixture itself already ran over the real registry of backends.
    for backend in BACKENDS.values():
        if getattr(backend, "_signal_stop", False):
            assert backend._pidproc.pidfile.startswith(str(tmp_path))


def test_the_suite_redirects_the_sushi_pidfile(tmp_path):
    """The registered sushi backend is a signal-stop backend whose pidfile
    names the real `/tmp/local-ai-setup-sushi.pid` (shared with wt), and
    llmbench has no override variable for it. The autouse
    `_redirect_signal_stop_pidfiles` must swap its frozen `_pidproc` for one
    under this test's tmp_path, so no unpatched test can read, signal by or
    remove the developer's real sushi pidfile."""
    from llmbench.providers.lifecycle.backends import BACKENDS

    sushi = BACKENDS["sushi"]
    assert sushi.spec.pidfile == "/tmp/local-ai-setup-sushi.pid"
    assert sushi._pidproc.pidfile == str(tmp_path / "sushi.pid")
    assert sushi._pidproc.logfile == str(tmp_path / "sushi.log")
    assert "local-ai-setup" not in sushi._pidproc.pidfile


def test_pidproc_popen_refuses_a_faked_binary(tmp_path):
    """`PidfileProcess.spawn` calls `subprocess.Popen` directly, which the
    subprocess.run fake never sees: the autouse `_GuardedPopen` must refuse
    it for every faked basename, so a test that forgot to patch `spawn`
    cannot exec the real `sushi serve` (46 GB). The path does not exist:
    were the guard ever gone this fails with FileNotFoundError, starting
    nothing. No pidfile is written."""
    from llmbench.providers.lifecycle.pidproc import PidfileProcess

    proc = PidfileProcess(
        name="sushi", pidfile=str(tmp_path / "sushi.pid"), logfile=str(tmp_path / "sushi.log")
    )
    for new_session in (True, False):
        with pytest.raises(AssertionError, match="blocked binary"):
            proc.spawn(
                ["/Users/nobody/.local/bin/sushi", "serve", "--port", "12345"],
                new_session=new_session,
            )
    assert not (tmp_path / "sushi.pid").exists()


def test_pidproc_popen_still_spawns_a_binary_that_is_not_faked(tmp_path):
    """The positive control: the guard delegates everything else to the real
    Popen, so a fake-script test still gets a live child (and its pid in
    the pidfile). The child exits on its own: `os.kill` is a no-op in this
    suite, so `Popen.kill()` could not end it."""
    from llmbench.providers.lifecycle.pidproc import PidfileProcess

    script = tmp_path / "fake-server"
    script.write_text("#!/bin/sh\nexec sleep 1\n")
    script.chmod(0o755)
    proc = PidfileProcess(
        name="fake-server", pidfile=str(tmp_path / "f.pid"), logfile=str(tmp_path / "f.log")
    )
    child = proc.spawn([str(script)], new_session=True)
    try:
        assert isinstance(child, subprocess.Popen)
        assert child.poll() is None
        assert (tmp_path / "f.pid").read_text() == str(child.pid)
    finally:
        child.wait(timeout=10)
