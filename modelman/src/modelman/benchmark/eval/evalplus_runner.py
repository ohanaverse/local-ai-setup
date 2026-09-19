"""Subprocess wrapper around EvalPlus for the `coding` eval category.

Verified live 2026-09-17 (plan Task 12) against a real evalplus==0.3.1
install driving generation through a local ollama server
(`qwen3.8:27b-mlx`, `--backend openai --base-url http://localhost:11434/v1`).
Two corrections vs. the Task 6 draft, both confirmed by the live run:

1. `--greedy` is required. Without it, `run_codegen`'s default
   `temperature=0.0` combined with `do_sample=not greedy` (True by default)
   trips `OpenAIChatDecoder.codegen`'s own
   `assert self.temperature > 0, "Temperature must be positive for
   sampling"` — every invocation failed immediately before the draft's fix.
   `--greedy` forces `temperature=0`, `bs=1`, `n_samples=1`, `do_sample=False`,
   which also happens to match the pass@1-only use case here.
2. `limit` is passed as `--id-range 0,<limit>`, not `--n-samples`.
   `evalplus.evaluate`'s `--n-samples` is samples-PER-PROBLEM (for pass@10/
   pass@100 estimation), not a dataset-subset size — passing `limit` there
   would multiply generation calls by `limit` across the FULL dataset (164
   problems for humaneval) instead of shrinking it, and could hang past
   `run_coding_category`'s 1800s subprocess timeout uncaught.
   `--id-range` at least bounds how many problems get GENERATED. KNOWN GAP
   (not fixed here, needs a follow-up design decision): EvalPlus's grading
   step in this version still asserts every problem in the FULL dataset has
   a sample before computing pass@k (`evaluate()` in evalplus/evaluate.py:
   `assert len(completion_id) == len(problems), "Missing problems in
   samples"`), so any `limit` smaller than the dataset's full size (164 for
   humaneval) makes evalplus.evaluate exit non-zero with that assertion —
   surfaced cleanly here as a `CodingResult.error`, not a crash, but the
   `coding` category will not produce a real pass@1 score whenever a subset
   `limit` is configured until that gap is addressed.

The `pass@1` output format itself matches the Task 6 draft's regex exactly:
confirmed against evalplus/evaluate.py's own `cprint(f"{k}:\\t{v:.3f}", ...)`
and reproduced live (offline, using ground-truth solutions to get a full,
gradeable run) as `pass@1:\\t0.994` — colon, a literal tab, then a 3-decimal
float; no ANSI color codes came through when stdout was captured
non-interactively. EvalPlus itself does both generation and grading and
runs the generated code locally, same trust model the agent benchmark
already applies to a model's diff via gates.py.
"""

from __future__ import annotations

import os
import re
import subprocess
from dataclasses import dataclass
from tempfile import TemporaryDirectory

_PASS_AT_1_RE = re.compile(r"pass@1(?!\d)[^\d]*([\d.]+)", re.IGNORECASE)
# EvalPlus prints the base-tests-only block first, then (when the "+"/extra
# tests pass) a second block headed like "humaneval+ (base + extra tests)".
# This marker locates that second block so _parse_pass_at_1 can prefer it.
_PLUS_BLOCK_MARKER = "(base + extra tests)"


@dataclass
class CodingResult:
    dataset: str
    # The "+" (base + extra tests) pass@1 when EvalPlus reports one,
    # falling back to the base-only score only when no "+" block is present
    # (e.g. --base-only, or a dataset without an extra-tests variant). Do
    # not "fix" this back to the base score — the whole point of using
    # EvalPlus over vanilla HumanEval is the hardened "+" grading, and
    # reporting the base score under this field would be misleadingly
    # optimistic. See _parse_pass_at_1.
    pass_at_1: float | None
    raw_output: str
    error: str | None = None


def _build_command(
    *, dataset: str, base_url: str, model: str, limit: int | None, workdir: str
) -> list[str]:
    # Bare console-script name (resolved via PATH), not `uvx --from evalplus`:
    # modelman itself runs via `uv run` from within its own venv, which puts
    # that venv's bin/ (containing evalplus.evaluate once `uv sync --extra
    # eval` has run) first on PATH — the same resolution `uv run --extra eval
    # evalplus.evaluate ...` used during live verification (plan Task 12).
    # `uvx --from evalplus` would instead run EvalPlus in an ephemeral,
    # separate uv-managed tool environment, bypassing the project's own
    # `[eval]` extra entirely — the extra is the deliberate opt-in gate for
    # local code execution (EvalPlus runs model-generated code), so `uvx`
    # would let anyone trigger a live EvalPlus run without it.
    cmd = [
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
        "--greedy",
    ]
    if limit is not None:
        cmd += ["--id-range", f"0,{limit}"]
    return cmd


def _parse_pass_at_1(stdout: str) -> float | None:
    """Prefer the "+" (base + extra tests) block's pass@1 over the base-only
    block's. EvalPlus's real output prints both blocks when the "+" tests
    pass:

        humaneval (base tests)
        pass@1:    0.732
        humaneval+ (base + extra tests)
        pass@1:    0.658

    A plain `re.search` for the first `pass@1` match would silently pick
    the base score — this project's whole reason for using EvalPlus over
    vanilla HumanEval is the hardened "+" grading, so the base score alone
    would be misleadingly optimistic. Falls back to searching the full
    output (i.e. the base block) when no "+" block marker is present.
    """
    plus_idx = stdout.find(_PLUS_BLOCK_MARKER)
    segment = stdout[plus_idx:] if plus_idx != -1 else stdout
    match = _PASS_AT_1_RE.search(segment)
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
        try:
            result = run_cmd(
                cmd,
                capture_output=True,
                text=True,
                timeout=1800,
                env={**os.environ, "OPENAI_API_KEY": api_key},
            )
        except subprocess.TimeoutExpired:
            return CodingResult(
                dataset=dataset,
                pass_at_1=None,
                raw_output="",
                error="evalplus timed out after 1800s",
            )
        except FileNotFoundError as exc:
            return CodingResult(
                dataset=dataset,
                pass_at_1=None,
                raw_output="",
                error=f"evalplus not runnable (is the `eval` extra installed?): {exc}",
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
