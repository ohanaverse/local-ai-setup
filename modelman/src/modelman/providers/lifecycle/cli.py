"""`modelman provider` — the CLI surface over `orchestrate.py`'s
isolate/stop/stop_all/restore.

This is the last piece before `bin/llm-isolate-provider` and
`bin/llm-restore-providers` can be retired: Task 8 rewrites the bash
benchmark scripts to call this instead of shelling out to those scripts.

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
    success_message: Callable[[LifecycleResult], str] = lambda r: "ok",
) -> None:
    """Print `result` per the shared envelope contract.

    json_output=True: `json.dumps(dataclasses.asdict(result))` on stdout
    and NOTHING else on stdout — a hard requirement, since Task 8's
    benchmark scripts (and anything else scripting this CLI) parse stdout
    as the exact 5-key envelope `{"provider", "model", "direct_url",
    "ok", "error"}`.
    json_output=False: one human-readable line — to stdout on success, to
    stderr (not stdout) on failure.
    """
    if json_output:
        typer.echo(json.dumps(dataclasses.asdict(result)))
        return
    if result.ok:
        typer.echo(success_message(result))
    else:
        typer.echo(f"error: {result.error}", err=True)


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
    extra_args = (draft,) if draft else ()
    try:
        # orchestrate.isolate() is documented to never raise (it returns an
        # ok=False LifecycleResult for every failure mode) — this try/except
        # is a defensive backstop, not the primary error path, so a bug
        # there still can't put a Python traceback on this CLI's stdout.
        result = lifecycle.isolate(provider_id, model, extra_args=extra_args, solo=solo)
    except Exception as exc:  # noqa: BLE001 — envelope contract, never a traceback
        result = LifecycleResult(provider_id, model or "", "", False, f"{type(exc).__name__}: {exc}")
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
    try:
        result = lifecycle.stop(provider_id)
    except Exception as exc:  # noqa: BLE001 — envelope contract, never a traceback
        result = LifecycleResult(provider_id, "", "", False, f"{type(exc).__name__}: {exc}")
    _emit(result, json_output, success_message=lambda r: f"stopped {r.provider}")
    raise typer.Exit(0 if result.ok else 1)


@provider_app.command("stop-all")
def stop_all_cmd(
    keep: str | None = typer.Option(None, "--keep"),
    json_output: bool = typer.Option(False, "--json"),
) -> None:
    """Stop every local provider, optionally keeping one occupancy
    domain's models loaded."""
    try:
        result = lifecycle.stop_all(keep or "")
    except Exception as exc:  # noqa: BLE001 — envelope contract, never a traceback
        result = LifecycleResult("stop-all", "", "", False, f"{type(exc).__name__}: {exc}")
    _emit(
        result,
        json_output,
        success_message=lambda r: f"stopped all local providers (kept {keep})"
        if keep
        else "stopped all local providers",
    )
    raise typer.Exit(0 if result.ok else 1)


@provider_app.command("restore")
def restore_cmd(json_output: bool = typer.Option(False, "--json")) -> None:
    """Bring local providers back to their standing baseline after a
    benchmark run releases exclusivity."""
    try:
        result = lifecycle.restore()
    except Exception as exc:  # noqa: BLE001 — envelope contract, never a traceback
        result = LifecycleResult("restore", "", "", False, f"{type(exc).__name__}: {exc}")
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
        occupancy = "" if backend.occupancy_key == provider_id else f" occupancy={backend.occupancy_key}"
        default_model = backend.default_model or "none"
        env_var = backend.env_var or "none"
        typer.echo(
            f"{provider_id}\t[{status}]{occupancy} health={backend.health_url} "
            f"default_model={default_model} env_var={env_var}"
        )


__all__ = ["provider_app"]
