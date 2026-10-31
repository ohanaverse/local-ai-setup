"""Guards on the autouse suite-hermeticity fixtures in tests/conftest.py.

The lifecycle backends drive real local-model binaries (`omlx stop`,
`mtplx stop`, `rec stop`, `sushi serve`, `mlx_lm.server`) and real
LaunchAgents. Until one global allow-listing wrapper covered all of them,
only per-test stubbing stood between the suite and the developer's live
models — one new test that forgot to stub would tear down a model an agent
was mid-request against. These tests fail loudly if that wrapper stops
covering a binary, or if it ever stops delegating everything else to the
real subprocess.run (which would silently neuter git/pi/pytest subprocess
calls elsewhere in the suite).
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
        # sushi is installed on the developer's machine; `sushi serve` loads a
        # 46 GB pack. A flag the real sushi rejects, plus its Homebrew path.
        ["sushi", "--llmbench-hermetic-no-such-flag"],
        ["/opt/homebrew/bin/sushi", "serve"],
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
