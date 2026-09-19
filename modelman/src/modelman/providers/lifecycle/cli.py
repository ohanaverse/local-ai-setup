"""`modelman provider` — the CLI surface over `orchestrate.py`'s
isolate/stop/stop_all/restore.

This CLI replaced the `bin/llm-isolate-provider` and
`bin/llm-restore-providers` bash helpers (issue #79); both scripts are
deleted, and the benchmark scripts under `benchmarks/` now call these
commands via `uv run --directory modelman modelman provider ...`.
In-process callers (`modelman.benchmark.isolation`, `local_control.py`)
skip this layer entirely and call `orchestrate.py` directly.

Follows the same sub-app pattern as `modelman.benchmark.cli.benchmark_app`
and `modelman.usage.cli.usage_app`: one `typer.Typer()` per concern,
mounted onto `main.py`'s root `app`.

`lifecycle` is imported as a module object (not `from .orchestrate import
isolate, ...`) so tests can patch `modelman.providers.lifecycle.isolate`
(etc.) at the module-attribute level and have this file see the patch —
the same convention `modelman.benchmark.isolation` and
`modelman.benchmark.agent.suite` already use.
"""

from __future__ import annotations

import dataclasses
import json
from collections.abc import Callable

import typer

from .. import lifecycle
from .envelope import LifecycleResult

provider_app = typer.Typer(help="Local model provider lifecycle (isolate/stop/restore).")


def _emit(
    result: LifecycleResult,
    json_output: bool,
    *,
    success_message: Callable[[LifecycleResult], str],
) -> None:
    """Print `result` per the shared envelope contract.

    json_output=True: `json.dumps(dataclasses.asdict(result))` on stdout
    and NOTHING else on stdout — a hard requirement, since the benchmark
    scripts under `benchmarks/` (and anything else scripting this CLI)
    parse stdout as the exact 5-key envelope `{"provider", "model",
    "direct_url", "ok", "error"}`.
    json_output=False: one human-readable line — to stdout on success, to
    stderr (not stdout) on failure.

    `success_message` is required (no default): every command has a
    different human-mode line, and a fallback default would be dead code
    that silently hides a caller forgetting to pass one.
    """
    if json_output:
        typer.echo(json.dumps(dataclasses.asdict(result)))
        return
    if result.ok:
        typer.echo(success_message(result))
    else:
        typer.echo(f"error: {result.error}", err=True)


def _run_command(
    fn: Callable[[], LifecycleResult],
    *,
    provider: str,
    model: str = "",
) -> LifecycleResult:
    """Call `fn()` and turn any escaping exception into an ok=False
    envelope.

    Every `orchestrate.*` entry point is documented to never raise (it
    returns an ok=False `LifecycleResult` for every failure mode), so this
    is a defensive backstop, not the primary error path — a bug there
    still can't put a Python traceback on this CLI's stdout. Shared by all
    four stateful commands so the backstop's behavior can't drift between
    them.
    """
    try:
        return fn()
    except Exception as exc:  # noqa: BLE001 — envelope contract, never a traceback
        return LifecycleResult(provider, model, "", False, f"{type(exc).__name__}: {exc}")


@provider_app.command("isolate")
def isolate_cmd(
    provider_id: str,
    model: str | None = typer.Argument(None),
    draft: str | None = typer.Option(None, "--draft"),
    solo: bool = typer.Option(False, "--solo"),
    json_output: bool = typer.Option(False, "--json"),
) -> None:
    """Stop other local providers (unless --solo) and start+warm the
    requested one."""
    # `extra_args` is the POSITIONAL PAIR (target, draft) everywhere in this
    # codebase (`MlxLmServerBackend.resolve()`, `benchmark.isolation.
    # mlx_lm_server_pairing_args`, `orchestrate.isolate()`'s docstring), so
    # --draft must land at index 1, never index 0. This CLI's own `model`
    # argument already carries the target as the primary positional, so
    # index 0 is a deliberately-empty placeholder: `resolve()` reads it as
    # `extra_args[0] if extra_args and extra_args[0] else ""`, so an empty
    # string falls through to `model` (and then the env var) for the
    # target, exactly as intended. Passing `(draft,)` here instead — as an
    # earlier version did — silently handed the DRAFT repo id to the target
    # slot and left the draft unset.
    extra_args: tuple[str, ...] = ("", draft) if draft else ()
    result = _run_command(
        lambda: lifecycle.isolate(provider_id, model, extra_args=extra_args, solo=solo),
        provider=provider_id,
        model=model or "",
    )
    _emit(
        result,
        json_output,
        success_message=lambda r: f"isolated {r.provider} -> {r.model} at {r.direct_url}",
    )
    raise typer.Exit(0 if result.ok else 1)


@provider_app.command("stop")
def stop_cmd(
    provider_id: str,
    json_output: bool = typer.Option(False, "--json"),
) -> None:
    """Stop exactly one local provider, leaving every other one running."""
    result = _run_command(lambda: lifecycle.stop(provider_id), provider=provider_id)
    _emit(result, json_output, success_message=lambda r: f"stopped {r.provider}")
    raise typer.Exit(0 if result.ok else 1)


@provider_app.command("stop-all")
def stop_all_cmd(
    keep: str | None = typer.Option(None, "--keep"),
    json_output: bool = typer.Option(False, "--json"),
) -> None:
    """Stop every local provider, optionally keeping one occupancy
    domain's models loaded."""
    # `--keep` takes a PROVIDER ID — same as `isolate`/`stop`'s positional
    # provider_id — and stop_all() itself resolves it to the right
    # occupancy_key (and rejects an unknown id) rather than this CLI layer
    # doing that translation, matching how isolate_cmd/stop_cmd forward
    # their provider ids straight through to orchestrate.py.
    result = _run_command(lambda: lifecycle.stop_all(keep or ""), provider="stop-all")
    _emit(
        result,
        json_output,
        success_message=lambda r: (
            f"stopped all local providers (kept {keep})" if keep else "stopped all local providers"
        ),
    )
    raise typer.Exit(0 if result.ok else 1)


@provider_app.command("restore")
def restore_cmd(json_output: bool = typer.Option(False, "--json")) -> None:
    """Bring local providers back to their standing baseline after a
    benchmark run releases exclusivity."""
    result = _run_command(lifecycle.restore, provider="restore")
    _emit(result, json_output, success_message=lambda r: "restored providers")
    raise typer.Exit(0 if result.ok else 1)


@provider_app.command("list")
def list_cmd() -> None:
    """Print every known lifecycle provider id, its port/URL, default
    model, and env var, marking which are in SUPPORTED_PROVIDER_IDS vs.
    retired-only (present in BACKENDS but not SUPPORTED_PROVIDER_IDS)."""
    for provider_id in sorted(lifecycle.BACKENDS):
        backend = lifecycle.BACKENDS[provider_id]
        status = "supported" if provider_id in lifecycle.SUPPORTED_PROVIDER_IDS else "retired-only"
        occupancy = (
            "" if backend.occupancy_key == provider_id else f" occupancy={backend.occupancy_key}"
        )
        default_model = backend.default_model or "none"
        env_var = backend.env_var or "none"
        typer.echo(
            f"{provider_id}\t[{status}]{occupancy} health={backend.health_url} "
            f"default_model={default_model} env_var={env_var}"
        )


__all__ = ["provider_app"]
