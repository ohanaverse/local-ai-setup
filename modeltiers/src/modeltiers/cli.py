"""The `modeltiers` command line.

`main` builds the real dependencies; a test passes its own `Deps`, so no test
reaches the network, the clock or wt.
"""

import argparse
import sys
import time
from collections.abc import Callable
from dataclasses import dataclass, field
from datetime import UTC, datetime
from functools import partial
from math import inf
from pathlib import Path

from modeltiers import aliases as aliases_mod
from modeltiers import (
    changes,
    identity,
    ladder,
    loader,
    output,
    page,
    paths,
    pipeline,
    report,
    show,
    snapshots,
)
from modeltiers import contracts as contracts_mod
from modeltiers import inventory as inventory_mod
from modeltiers.errors import ConfigError, ModelTiersError
from modeltiers.fetch import Fetcher, Sleep, http_fetcher
from modeltiers.inventory import RunWt
from modeltiers.loader import ScoreRecords, SourceResult, SourceSpec, SourceSpecs
from modeltiers.records import Inventory, ModelsDev

EXIT_OK = 0
EXIT_ERROR = 1
EXIT_STALE = 2
EXIT_SENTINEL = 3
STAMP = contracts_mod.STAMP


def _utc_now() -> datetime:
    return datetime.now(UTC)


@dataclass(frozen=True)
class Deps:
    """Everything a command takes from outside the process."""

    fetcher: Fetcher = http_fetcher
    sleep: Sleep = time.sleep
    now: Callable[[], datetime] = _utc_now
    run_wt: RunWt = inventory_mod.run_wt
    specs: SourceSpecs = field(default_factory=loader.build_specs)
    aliases_path: Path | None = None  # None: the committed data/aliases.toml
    contracts_path: Path | None = None  # None: the committed data/contracts.toml


def _warn(message: str) -> None:
    print(f"warning: {message}", file=sys.stderr)


def _source_line(result: SourceResult[ModelsDev] | SourceResult[ScoreRecords]) -> str:
    return (
        f"{result.name}: {result.status}, {result.records} records, "
        f"fetched {result.fetched_at.strftime(STAMP)}"
    )


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="modeltiers",
        description="Advisory capability tiers for the models wt knows.",
    )
    commands = parser.add_subparsers(dest="command", required=True)
    run = commands.add_parser(
        "run", help="fetch, resolve, assign, print the change report, write tiers.toml"
    )
    run.add_argument(
        "--dry-run",
        action="store_true",
        help=(
            "do everything except write the output file; the exit code is the real run's, "
            "except that a redirected registry, which the real run refuses with 1, only warns"
        ),
    )
    run.add_argument(
        "--offline",
        action="store_true",
        help="fetch nothing; read each source's newest good snapshot",
    )
    run.add_argument(
        "--inventory",
        metavar="FILE",
        type=Path,
        help="read saved `wt model list --json` output instead of running wt",
    )
    shown = commands.add_parser("show", help="print the tier table from tiers.toml")
    shown.add_argument("--candidates", action="store_true", help="list the candidates too")
    shown.add_argument(
        "--sort",
        choices=show.SORT_COLUMNS,
        metavar="COLUMN",
        help=(
            f"order the rows by one of {', '.join(show.SORT_COLUMNS)}: highest first, "
            "rows with no value last; `model` is alphabetical"
        ),
    )
    shown.add_argument(
        "--wide",
        action="store_true",
        help="add open weights, context and the scores to the table",
    )
    shown.add_argument(
        "--html",
        metavar="FILE",
        type=Path,
        help=(
            "write the table as one self-contained page, every model and candidate with "
            "every column, to sort and filter in a browser; prints only where it wrote"
        ),
    )
    explain = commands.add_parser(
        "explain", help="print how one model was identified and why it has its tier"
    )
    explain.add_argument("model", help="provider_id/model_name, or a canonical key")
    rungs = commands.add_parser(
        "ladder",
        help=(
            "print one signal's resolved scores from the newest snapshots, highest first, "
            "with the gap under each: where a floor can go"
        ),
    )
    rungs.add_argument("signal", choices=ladder.CHOICES)
    rungs.add_argument(
        "--low", type=float, default=-inf, metavar="SCORE", help="leave out lower scores"
    )
    rungs.add_argument(
        "--high", type=float, default=inf, metavar="SCORE", help="leave out higher scores"
    )
    return parser


def _check_registry(inventory: Inventory, *, dry_run: bool) -> None:
    """Refuse a run whose inventory came from a redirected registry: wt
    follows WT_REGISTRY and the output path does not, so a scratch inventory
    would replace the real tiers.toml. `--dry-run` writes nothing, so it goes
    on and says that the same run without it would stop here."""
    if paths.out_is_overridden() or paths.registry_is_beside_output(inventory.registry):
        return
    out = paths.output_path()
    found = (
        f"the inventory came from {inventory.registry}, which is not {out.parent / 'registry.toml'}"
    )
    remedy = f"Set {paths.OUT_ENV} to write somewhere else"
    if not dry_run:
        raise ConfigError(f"{found}; refusing to replace {out} from it. {remedy}")
    _warn(f"{found}; without --dry-run this run is refused and exits 1. {remedy}")


def _run(args: argparse.Namespace, deps: Deps) -> int:
    # Before the inventory is read and before any fetch: a bad data file
    # costs no download.
    aliases = aliases_mod.load_aliases(deps.aliases_path)
    contracts = contracts_mod.load_contracts(deps.contracts_path)
    inventory = inventory_mod.load_inventory(path=args.inventory, run_wt=deps.run_wt)
    _check_registry(inventory, dry_run=args.dry_run)
    for line in inventory_mod.provider_warnings(inventory):
        _warn(line)
    now = deps.now()
    cache = paths.cache_dir()
    sources = loader.load_all(
        deps.specs,
        cache=cache,
        fetcher=deps.fetcher,
        sleep=deps.sleep,
        now=now,
        offline=args.offline,
        warn=_warn,
    )
    identity = pipeline.resolve_all(inventory, sources, aliases)
    for line in identity.warnings:
        _warn(line)
    assignment = pipeline.assign_all(identity, contracts)
    print(
        f"inventory: {len(inventory.rows)} models "
        f"({inventory.native_dropped} native rows dropped), registry {inventory.registry}"
    )
    for result in sources.results():
        print(_source_line(result))
    for line in (
        *report.identity_lines(identity),
        *report.unresolved_lines(assignment),
        *report.sentinel_lines(assignment),
    ):
        print(line)
    if not assignment.sentinels_ok:
        print(
            f"modeltiers: a sentinel moved; nothing written. If the move is real, "
            f"edit {contracts.origin}",
            file=sys.stderr,
        )
        return EXIT_SENTINEL
    out = paths.output_path()
    document = output.build_document(assignment, inventory, sources, now)
    previous = output.read_previous(out)
    if previous is None and out.exists():
        _warn(f"{out} cannot be read as TOML; it is treated as absent")
    for line in (
        *changes.change_lines(previous, document, out),
        *show.attribution_lines(document),
    ):
        print(line)
    if args.dry_run:
        print(f"dry run: {out} not written")
    else:
        output.write_document(out, document)
        if not args.offline:
            for result in sources.results():
                snapshots.prune(cache, result.name)
        print(f"wrote {out}")
    return EXIT_STALE if any(r.fallback for r in sources.results()) else EXIT_OK


def _show(args: argparse.Namespace, deps: Deps) -> int:
    document = output.read_document(paths.output_path())
    now = deps.now()
    warning = show.stale_warning(document, now)
    if warning is not None:
        _warn(warning)
    if args.html is not None:
        target: Path = args.html.resolve()
        # The same file, not the same spelling: on a file system that ignores
        # case `TIERS.toml` is tiers.toml, and the two paths differ.
        if target.exists() and target.samefile(paths.output_path()):
            raise ConfigError(
                f"--html {target} is tiers.toml itself; name another file for the page"
            )
        output.write_text(target, page.page_html(document, now, sort=args.sort))
        print(f"wrote {target}")
        return EXIT_OK
    for line in (
        show.age_line(document, now),
        *show.table_lines(document, candidates=args.candidates, sort=args.sort, wide=args.wide),
        *show.attribution_lines(document),
    ):
        print(line)
    return EXIT_OK


def _explain(args: argparse.Namespace, deps: Deps) -> int:
    document = output.read_document(paths.output_path())
    lines = show.explain_lines(document, args.model)
    if lines is None:
        print(
            f"modeltiers: no model or canonical key {args.model!r} in the file generated "
            f"{document.get('generated_at', 'at an unknown time')}",
            file=sys.stderr,
        )
        return EXIT_ERROR
    for line in lines:
        print(line)
    return EXIT_OK


def _ladder(args: argparse.Namespace, deps: Deps) -> int:
    """Reads the newest good snapshot of models.dev and of the signal's own
    source, and nothing else: no fetch, no wt, no output file."""
    aliases = aliases_mod.load_aliases(deps.aliases_path)
    source = contracts_mod.SIGNAL_SOURCE[args.signal.removesuffix(ladder.LOWER)]
    spec: SourceSpec[ScoreRecords] = getattr(deps.specs, source)
    load = partial(
        loader.load_source,
        cache=paths.cache_dir(),
        fetcher=deps.fetcher,
        sleep=deps.sleep,
        now=deps.now(),
        offline=True,
        warn=_warn,
    )
    modelsdev = load(deps.specs.modelsdev)
    scored = load(spec)
    index = pipeline.checked_index(modelsdev.data, aliases)
    best = identity.resolve_source(source, scored.data, index, aliases).best.values()
    lines = ladder.ladder_lines(
        best, lower_bound=args.signal.endswith(ladder.LOWER), low=args.low, high=args.high
    )
    if not lines:
        print(
            f"modeltiers: no resolved {args.signal} score from {args.low} to {args.high}",
            file=sys.stderr,
        )
        return EXIT_ERROR
    for line in (_source_line(modelsdev), _source_line(scored), *lines):
        print(line)
    return EXIT_OK


_COMMANDS: dict[str, Callable[[argparse.Namespace, Deps], int]] = {
    "run": _run,
    "show": _show,
    "explain": _explain,
    "ladder": _ladder,
}


def main(argv: list[str] | None = None, *, deps: Deps | None = None) -> int:
    args = _parser().parse_args(argv)
    try:
        return _COMMANDS[args.command](args, deps or Deps())
    # OSError covers the cache and the output paths: an unwritable, unreadable
    # or non-directory MODELTIERS_CACHE (or a full disk) is a user-facing
    # condition like any other, not a traceback.
    except (ModelTiersError, OSError) as exc:
        print(f"modeltiers: {exc}", file=sys.stderr)
        return EXIT_ERROR
