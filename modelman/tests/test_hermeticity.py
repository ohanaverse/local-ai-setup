"""Guards on the autouse suite-hermeticity fixtures in tests/conftest.py.

The lifecycle backends drive real local-model binaries (`omlx stop`,
`mtplx stop`, `mlx_lm.server`) and real LaunchAgents. Until one global
allow-listing wrapper covered all of them, only per-test stubbing stood
between the suite and the developer's live models — one new test that
forgot to stub would tear down a model an agent was mid-request against.
These tests fail loudly if that wrapper stops covering a binary, or if it
ever stops delegating everything else to the real subprocess.run (which
would silently neuter git/pi/pytest subprocess calls elsewhere in the
suite).
"""

from __future__ import annotations

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
