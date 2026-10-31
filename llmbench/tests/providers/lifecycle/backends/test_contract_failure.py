"""A broken process-provider contract file must not take every backend down."""

from __future__ import annotations

import os
import subprocess
import sys
from pathlib import Path

_PROGRAM = (
    "from llmbench.providers.lifecycle import orchestrate\n"
    "from llmbench.providers.lifecycle.backends import BACKENDS, PROCESS_SPEC_ERROR\n"
    "print(sorted(BACKENDS))\n"
    "print(PROCESS_SPEC_ERROR is not None)\n"
    "print(orchestrate.isolate('rec').error)\n"
)


def _run(env_file) -> list[str]:
    env = {**os.environ, "LLMBENCH_PROCESS_PROVIDERS": str(env_file)}
    return subprocess.run(  # noqa: S603 — this interpreter, a fixed program
        [sys.executable, "-c", _PROGRAM],
        capture_output=True,
        text=True,
        env=env,
        check=True,
    ).stdout.splitlines()


def test_a_missing_contract_file_leaves_the_other_backends(tmp_path):
    out = _run(tmp_path / "absent.toml")
    assert out[0] == "['mlx_lm_server', 'ollama', 'omlx', 'omlx-6bit']"
    assert out[1] == "True"
    assert out[2].startswith("unknown provider: rec (process providers unavailable: cannot read")


def test_one_malformed_entry_disables_every_process_provider(tmp_path):
    real = Path(__file__).resolve().parents[5] / "docs" / "contracts" / "process-providers.toml"
    broken = tmp_path / "broken.toml"
    broken.write_text(real.read_text().replace("warmup_seconds = 120", 'warmup_seconds = "abc"', 1))
    out = _run(broken)
    assert out[0] == "['mlx_lm_server', 'ollama', 'omlx', 'omlx-6bit']"
    assert out[1] == "True"
    assert out[2].startswith(
        "unknown provider: rec (process providers unavailable: process[0]: "
        "`warmup_seconds` must be a positive integer"
    )
