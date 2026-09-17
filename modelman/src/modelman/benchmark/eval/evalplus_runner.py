"""Subprocess wrapper around EvalPlus for the `coding` eval category.

The exact CLI invocation here is the research doc's cited
`--backend openai --base-url` form, NOT yet verified against a real EvalPlus
install (see plan Task 11) — treat `_build_command`'s flags as the first
draft, and update them there (one place) once verified. EvalPlus itself does
both generation and grading and runs the generated code locally, same trust
model the agent benchmark already applies to a model's diff via gates.py.
"""

from __future__ import annotations

import os
import re
import subprocess
from dataclasses import dataclass
from tempfile import TemporaryDirectory

_PASS_AT_1_RE = re.compile(r"pass@1(?!\d)[^\d]*([\d.]+)", re.IGNORECASE)


@dataclass
class CodingResult:
    dataset: str
    pass_at_1: float | None
    raw_output: str
    error: str | None = None


def _build_command(
    *, dataset: str, base_url: str, model: str, limit: int | None, workdir: str
) -> list[str]:
    cmd = [
        "uvx",
        "--from",
        "evalplus",
        "evalplus.evaluate",
        "--dataset",
        dataset,
        "--backend",
        "openai",
        "--base-url",
        base_url,
        "--model",
        model,
        "--root",
        workdir,
    ]
    if limit is not None:
        cmd += ["--n-samples", str(limit)]
    return cmd


def _parse_pass_at_1(stdout: str) -> float | None:
    match = _PASS_AT_1_RE.search(stdout)
    if not match:
        return None
    try:
        return float(match.group(1))
    except ValueError:
        return None


def run_coding_category(
    *,
    base_url: str,
    model: str,
    api_key: str,
    dataset: str,
    limit: int | None,
    run_cmd=subprocess.run,
) -> CodingResult:
    """Invoke EvalPlus against the row's resolved OpenAI-compatible endpoint
    and parse its pass@1 result. Runs in a scratch directory per call so
    concurrent/sequential rows never collide on EvalPlus's own output files.
    """
    with TemporaryDirectory(prefix="modelman-evalplus-") as tmp:
        cmd = _build_command(
            dataset=dataset, base_url=base_url, model=model, limit=limit, workdir=tmp
        )
        result = run_cmd(
            cmd,
            capture_output=True,
            text=True,
            timeout=1800,
            env={**os.environ, "OPENAI_API_KEY": api_key},
        )
        if result.returncode != 0:
            return CodingResult(
                dataset=dataset,
                pass_at_1=None,
                raw_output=result.stdout + result.stderr,
                error=f"evalplus exited {result.returncode}: {result.stderr[:300]}",
            )
        pass_at_1 = _parse_pass_at_1(result.stdout)
        if pass_at_1 is None:
            return CodingResult(
                dataset=dataset,
                pass_at_1=None,
                raw_output=result.stdout,
                error="could not parse pass@1 from evalplus output",
            )
        return CodingResult(dataset=dataset, pass_at_1=pass_at_1, raw_output=result.stdout)
