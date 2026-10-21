"""CLI for `llmbench probe`."""

from __future__ import annotations

import math
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
    prepare_out_dir,
    resolve_api_key,
    run_probe,
    write_outputs,
)

probe_app = typer.Typer(
    help="Diagnostic probes against servers you already started. "
    "They never start, stop, isolate or warm a provider."
)

# Usage errors (bad --target, unset key variable, no prompts, an --out that
# cannot be written) exit 2, before any request; a run in which no target
# answered exits 1; Ctrl-C exits 130 (128 + SIGINT) after writing what finished.
_USAGE_EXIT = 2
_INTERRUPTED_EXIT = 130


def _usage_error(message: str) -> typer.Exit:
    typer.echo(f"error: {message}", err=True)
    return typer.Exit(_USAGE_EXIT)


# Every penalty flag's help ends with this: a server may drop a field it does
# not know without a word, so a set penalty proves nothing on its own.
_PENALTY_CAVEAT = (
    " Default: not sent (the server's own default applies). Servers differ in which "
    "penalty fields they honor: an unknown or unsupported field may be ignored silently "
    "(no error) or rejected with a 400, and the probe cannot tell which. A result with a "
    "penalty set means something only if changing the value changes the output, or the "
    "server's log shows it was applied; check your server."
)


def _check_penalty(flag: str, value: float | None, *, low: float, high: float | None) -> None:
    """Raise ProbeError naming `flag` when a set value is NaN, infinite or out
    of range: [low, high] inclusive, or above `low` when `high` is None."""
    if value is None:
        return
    if not math.isfinite(value):
        raise ProbeError(f"{flag} must be a finite number, not {value}")
    if high is None and value <= low:
        raise ProbeError(f"{flag} must be greater than {low:g}, not {value:g}")
    if high is not None and not low <= value <= high:
        raise ProbeError(f"{flag} must be between {low:g} and {high:g}, not {value:g}")


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
    presence_penalty: float | None = typer.Option(
        None,
        "--presence-penalty",
        help="presence_penalty, -2.0 to 2.0 (OpenAI's range): penalizes a token that has "
        "appeared at all." + _PENALTY_CAVEAT,
    ),
    frequency_penalty: float | None = typer.Option(
        None,
        "--frequency-penalty",
        help="frequency_penalty, -2.0 to 2.0 (OpenAI's range): penalizes a token by how "
        "often it has appeared." + _PENALTY_CAVEAT,
    ),
    repetition_penalty: float | None = typer.Option(
        None,
        "--repetition-penalty",
        help="repetition_penalty, greater than 0 (a vLLM/mlx-style extension; 1.0 means "
        "none): scales down a repeated token's score." + _PENALTY_CAVEAT,
    ),
    timeout: float = typer.Option(
        900.0,
        "--timeout",
        help="Seconds to wait, per connect and per read (thinking can take minutes); a "
        "reply that arrives in pieces can take longer in all.",
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
    ...`); this command only sends chat completions. report.md and
    results.json are rewritten after every finished request, so Ctrl-C keeps
    what finished. Exit 0 when at least one target answered, 1 when none
    did, 2 on a usage error, 130 when interrupted.
    """
    if not target:
        raise _usage_error(f"give at least one --target {TARGET_FORMAT}")
    try:
        _check_penalty("--presence-penalty", presence_penalty, low=-2.0, high=2.0)
        _check_penalty("--frequency-penalty", frequency_penalty, low=-2.0, high=2.0)
        _check_penalty("--repetition-penalty", repetition_penalty, low=0.0, high=None)
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
        ids = [p.id for p in prompts]
        repeated = sorted({i for i in ids if ids.count(i) > 1})
        if repeated:
            raise ProbeError(
                f"duplicate prompt id(s): {', '.join(repeated)} (a --prompt-file's id is "
                "its file name without the extension; rename the file)"
            )
        out_dir = out if out is not None else default_out_dir()
        prepare_out_dir(out_dir)
    except ProbeError as exc:
        raise _usage_error(str(exc)) from exc

    sampling = Sampling(
        temperature=temperature,
        top_p=top_p,
        top_k=top_k,
        max_tokens=max_tokens,
        presence_penalty=presence_penalty,
        frequency_penalty=frequency_penalty,
        repetition_penalty=repetition_penalty,
    )
    run = ProbeRun(
        started_at=datetime.now(UTC).isoformat(timespec="seconds"),
        targets=targets,
        prompts=prompts,
        sampling=sampling,
        timeout_s=timeout,
    )
    in_flight: list[tuple[Target, Prompt]] = []

    def started(t: Target, p: Prompt) -> None:
        in_flight[:] = [(t, p)]

    def finished(result: ProbeResult) -> None:
        in_flight.clear()
        run.results.append(result)
        try:
            write_outputs(run, out_dir)
        except OSError as exc:
            # A full disk or a removed --out must not end a long run: the
            # next result retries, and the progress line below still prints.
            typer.echo(f"warning: could not write the report: {exc}", err=True)
        _progress(result)

    md_path, json_path = write_outputs(run, out_dir)
    try:
        run_probe(keyed, prompts, sampling, timeout_s=timeout, on_start=started, on_result=finished)
    except KeyboardInterrupt:
        md_path, json_path = write_outputs(run, out_dir)
        where = f" during {in_flight[0][0].name} / {in_flight[0][1].id}" if in_flight else ""
        typer.echo(
            f"interrupted{where}; {len(run.results)} finished request(s) kept in "
            f"{md_path} and {json_path}",
            err=True,
        )
        raise typer.Exit(_INTERRUPTED_EXIT) from None
    typer.echo(f"report: {md_path}")
    typer.echo(f"json:   {json_path}")
    if not any(r.answered for r in run.results):
        typer.echo("error: no target answered", err=True)
        raise typer.Exit(1)
