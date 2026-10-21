"""CLI for `llmbench probe`."""

from __future__ import annotations

import os
from datetime import UTC, datetime
from pathlib import Path

import typer

from llmbench.benchmark.eval.cli import DEFAULT_CATEGORIES_ROOT
from llmbench.probe.thinking import (
    TARGET_FORMAT,
    ProbeError,
    ProbeResult,
    ProbeRun,
    Prompt,
    Sampling,
    Target,
    builtin_prompts,
    default_out_dir,
    load_prompt_file,
    parse_target,
    resolve_api_key,
    run_probe,
    write_outputs,
)

probe_app = typer.Typer(
    help="Diagnostic probes against servers you already started. "
    "They never start, stop, isolate or warm a provider."
)

# Usage errors (bad --target, unset key variable, no prompts) exit 2, before
# any request; a run in which no target answered exits 1.
_USAGE_EXIT = 2


def _usage_error(message: str) -> typer.Exit:
    typer.echo(f"error: {message}", err=True)
    return typer.Exit(_USAGE_EXIT)


def _progress(result: ProbeResult) -> None:
    if result.answered:
        outcome = (
            f"{result.status}: reasoning {result.reasoning_chars} chars, "
            f"answer {result.answer_chars} chars"
        )
    else:
        outcome = result.error or "error"
    typer.echo(
        f"{result.target} / {result.prompt_id} ({result.elapsed_s:.1f}s) {outcome}", err=True
    )


@probe_app.command("thinking")
def thinking_cmd(
    target: list[str] = typer.Option(  # noqa: B008
        [],
        "--target",
        help=f"{TARGET_FORMAT}, repeatable. API_KEY_ENV is the NAME of an environment "
        "variable holding the key (never the key itself); leave it out for a keyless server.",
    ),
    prompt_file: list[Path] = typer.Option(  # noqa: B008
        [], "--prompt-file", help="A file whose whole text is one prompt, repeatable."
    ),
    builtin: bool = typer.Option(
        True,
        "--builtin/--no-builtin",
        help="Send the two built-in prompts: 'What is 17*23? Answer briefly.' and the eval "
        "planning item flaky-test-suite.",
    ),
    root: Path = typer.Option(  # noqa: B008
        DEFAULT_CATEGORIES_ROOT,
        "--root",
        help="Eval categories root the built-in planning prompt is read from.",
    ),
    temperature: float = typer.Option(
        1.0,
        "--temperature",
        help="Sampling temperature. The defaults (1.0, top_p 0.95, top_k 20) mirror "
        "Qwen3.x's generation_config.json and the eval-quant-tier suite.",
    ),
    top_p: float = typer.Option(0.95, "--top-p", help="top_p; 0 leaves it out of the request."),
    top_k: int = typer.Option(
        20,
        "--top-k",
        help="top_k; 0 leaves it out of the request (for a server that rejects it).",
    ),
    max_tokens: int | None = typer.Option(
        None, "--max-tokens", help="max_tokens; unset leaves the server's cap (as eval does)."
    ),
    timeout: float = typer.Option(
        900.0, "--timeout", help="Seconds to wait for each reply (thinking can take minutes)."
    ),
    out: Path | None = typer.Option(  # noqa: B008
        None,
        "--out",
        help="Directory for report.md and results.json. Default: a new timestamped "
        "directory under the system temp dir.",
    ),
) -> None:
    """Send the same prompts to each target and compare its reasoning
    (length, opening) and its answer (length, markdown shape).

    Start and isolate each server yourself first (`llmbench provider isolate
    ...`); this command only sends chat completions. Exit 0 when at least one
    target answered, 1 when none did, 2 on a usage error.
    """
    if not target:
        raise _usage_error(f"give at least one --target {TARGET_FORMAT}")
    try:
        targets: list[Target] = [parse_target(spec) for spec in target]
        names = [t.name for t in targets]
        duplicates = sorted({n for n in names if names.count(n) > 1})
        if duplicates:
            raise ProbeError(f"duplicate target name(s): {', '.join(duplicates)}")
        keyed = [(t, resolve_api_key(t, os.environ)) for t in targets]
        prompts: list[Prompt] = builtin_prompts(root) if builtin else []
        prompts += [load_prompt_file(path) for path in prompt_file]
        if not prompts:
            raise ProbeError("no prompts: drop --no-builtin or pass --prompt-file")
    except ProbeError as exc:
        raise _usage_error(str(exc)) from exc

    sampling = Sampling(temperature=temperature, top_p=top_p, top_k=top_k, max_tokens=max_tokens)
    run = ProbeRun(
        started_at=datetime.now(UTC).isoformat(timespec="seconds"),
        targets=targets,
        prompts=prompts,
        sampling=sampling,
        timeout_s=timeout,
    )
    run.results = run_probe(keyed, prompts, sampling, timeout_s=timeout, on_result=_progress)
    md_path, json_path = write_outputs(run, out if out is not None else default_out_dir())
    typer.echo(f"report: {md_path}")
    typer.echo(f"json:   {json_path}")
    if not any(r.answered for r in run.results):
        typer.echo("error: no target answered", err=True)
        raise typer.Exit(1)
