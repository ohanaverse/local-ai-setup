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
from pathlib import Path

from modeltiers import inventory as inventory_mod
from modeltiers import loader, paths
from modeltiers.errors import ModelTiersError
from modeltiers.fetch import Fetcher, Sleep, http_fetcher
from modeltiers.inventory import RunWt
from modeltiers.loader import SourceSpecs

EXIT_OK = 0
EXIT_ERROR = 1
EXIT_STALE = 2
STAMP = "%Y-%m-%dT%H:%M:%SZ"


def _utc_now() -> datetime:
    return datetime.now(UTC)


@dataclass(frozen=True)
class Deps:
    """Everything `run` takes from outside the process."""

    fetcher: Fetcher = http_fetcher
    sleep: Sleep = time.sleep
    now: Callable[[], datetime] = _utc_now
    run_wt: RunWt = inventory_mod.run_wt
    specs: SourceSpecs = field(default_factory=loader.build_specs)


def _warn(message: str) -> None:
    print(f"warning: {message}", file=sys.stderr)


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="modeltiers",
        description="Advisory capability tiers for the models wt knows.",
    )
    commands = parser.add_subparsers(dest="command", required=True)
    run = commands.add_parser(
        "run", help="read the inventory and the four sources and report on them"
    )
    run.add_argument("--dry-run", action="store_true", help="write no output file")
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
    return parser


def _run(args: argparse.Namespace, deps: Deps) -> int:
    inventory = inventory_mod.load_inventory(path=args.inventory, run_wt=deps.run_wt)
    for line in inventory_mod.provider_warnings(inventory):
        _warn(line)
    sources = loader.load_all(
        deps.specs,
        cache=paths.cache_dir(),
        fetcher=deps.fetcher,
        sleep=deps.sleep,
        now=deps.now(),
        offline=args.offline,
        warn=_warn,
    )
    print(
        f"inventory: {len(inventory.rows)} models "
        f"({inventory.native_dropped} native rows dropped), registry {inventory.registry}"
    )
    for result in sources.results():
        print(
            f"{result.name}: {result.status}, {result.records} records, "
            f"fetched {result.fetched_at.strftime(STAMP)}"
        )
    print("nothing written: this version stops after parsing the sources")
    return EXIT_STALE if any(r.fallback for r in sources.results()) else EXIT_OK


def main(argv: list[str] | None = None, *, deps: Deps | None = None) -> int:
    args = _parser().parse_args(argv)
    try:
        return _run(args, deps or Deps())
    # OSError covers the cache and the output paths: an unwritable, unreadable
    # or non-directory MODELTIERS_CACHE (or a full disk) is a user-facing
    # condition like any other, not a traceback.
    except (ModelTiersError, OSError) as exc:
        print(f"modeltiers: {exc}", file=sys.stderr)
        return EXIT_ERROR
