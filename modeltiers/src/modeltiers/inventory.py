"""The wt inventory: `wt model list --json`, or a saved copy of its output."""

import json
import subprocess
from collections.abc import Callable
from pathlib import Path
from typing import Any

from modeltiers.errors import InventoryError
from modeltiers.records import Inventory, InventoryRow

# Returns the stdout of `wt model list --json`, or raises InventoryError.
RunWt = Callable[[], str]

WT_COMMAND = ("wt", "model", "list", "--json")
WT_TIMEOUT_SECONDS = 60


def run_wt() -> str:
    """Run `wt model list --json`, the only wt command modeltiers ever runs."""
    command = " ".join(WT_COMMAND)
    try:
        done = subprocess.run(
            list(WT_COMMAND),
            capture_output=True,
            text=True,
            encoding="utf-8",
            check=False,
            timeout=WT_TIMEOUT_SECONDS,
        )
    except FileNotFoundError as exc:
        raise InventoryError("wt is not on PATH; install it or pass --inventory FILE") from exc
    except subprocess.TimeoutExpired as exc:
        raise InventoryError(f"`{command}` gave no answer in {WT_TIMEOUT_SECONDS} s") from exc
    if done.returncode != 0:
        detail = done.stderr.strip() or "no message"
        raise InventoryError(f"`{command}` exited {done.returncode}: {detail}")
    return done.stdout


def _field(row: dict[str, Any], key: str, kind: type, index: int) -> Any:
    value = row.get(key)
    if not isinstance(value, kind):
        raise InventoryError(
            f"inventory: models[{index}].{key} is missing or not a {kind.__name__}"
        )
    return value


def parse_inventory(text: str) -> Inventory:
    """Read the document: an object with `models`, `registry` and `providers`.

    Rows whose model_name is `native` are dropped and counted. Every other
    row is kept, registered or discovered, weights present or missing.
    """
    try:
        doc = json.loads(text)
    except json.JSONDecodeError as exc:
        raise InventoryError(f"inventory: not JSON: {exc}") from exc
    if not isinstance(doc, dict):
        raise InventoryError("inventory: expected an object with models, registry and providers")
    models = doc.get("models")
    registry = doc.get("registry")
    providers = doc.get("providers")
    if not isinstance(models, list):
        raise InventoryError("inventory: models is missing or not a list")
    if not isinstance(registry, str):
        raise InventoryError("inventory: registry is missing or not a string")
    if not isinstance(providers, dict) or not all(isinstance(v, str) for v in providers.values()):
        raise InventoryError("inventory: providers is missing or not a table of strings")
    rows: list[InventoryRow] = []
    native = 0
    for index, row in enumerate(models):
        if not isinstance(row, dict):
            raise InventoryError(f"inventory: models[{index}] is not an object")
        model_name = _field(row, "model_name", str, index)
        if model_name == "native":
            native += 1
            continue
        rows.append(
            InventoryRow(
                provider_id=_field(row, "provider_id", str, index),
                model_name=model_name,
                location=_field(row, "location", str, index),
                registered=_field(row, "registered", bool, index),
            )
        )
    return Inventory(
        rows=tuple(rows), registry=registry, providers=dict(providers), native_dropped=native
    )


def load_inventory(*, path: Path | None, run_wt: RunWt) -> Inventory:
    """Read `path` when given (and never run wt), else run wt."""
    if path is None:
        return parse_inventory(run_wt())
    try:
        text = path.read_text(encoding="utf-8")
    except UnicodeDecodeError as exc:
        raise InventoryError(f"inventory: {path} is not UTF-8 text: {exc}") from exc
    except OSError as exc:
        raise InventoryError(f"inventory: cannot read {path}: {exc.strerror}") from exc
    return parse_inventory(text)


def provider_warnings(inventory: Inventory) -> list[str]:
    """One line per provider whose probe status is not `ok`."""
    return [
        f"provider {name} probe status is {status}; "
        "models it would have discovered may be absent from this run"
        for name, status in sorted(inventory.providers.items())
        if status != "ok"
    ]
