"""A broken process-provider contract file must not take every backend down."""

from __future__ import annotations

import os
import subprocess
import sys

_PROGRAM = (
    "from llmbench.providers.lifecycle import orchestrate\n"
    "from llmbench.providers.lifecycle.backends import BACKENDS, PROCESS_SPEC_ERROR\n"
    "print(sorted(BACKENDS))\n"
    "print(PROCESS_SPEC_ERROR is not None)\n"
    "print(orchestrate.isolate('rec').error)\n"
)


def test_a_missing_contract_file_leaves_the_other_backends(tmp_path):
    env = {**os.environ, "LLMBENCH_PROCESS_PROVIDERS": str(tmp_path / "absent.toml")}
    out = subprocess.run(  # noqa: S603 — this interpreter, a fixed program
        [sys.executable, "-c", _PROGRAM],
        capture_output=True,
        text=True,
        env=env,
        check=True,
    ).stdout.splitlines()
    assert out[0] == "['mlx_lm_server', 'ollama', 'omlx', 'omlx-6bit']"
    assert out[1] == "True"
    assert out[2].startswith("unknown provider: rec (process providers unavailable: cannot read")
