"""The process-provider facts wt describes, read from the contract file.

`docs/contracts/process-providers.toml` mirrors the `Process` specs in
wt/internal/provider/table.go; this module is llmbench's only reader of it.
"""

from __future__ import annotations

import functools
import os
import tomllib
from dataclasses import dataclass
from pathlib import Path
from typing import Any

READY_KINDS: tuple[str, ...] = ("models-list", "command-json")


class ProcessSpecError(Exception):
    """The process-provider contract file is missing or malformed."""


@dataclass(frozen=True)
class Ready:
    kind: str
    command: tuple[str, ...]
    path: str
    equals: str
    model_path: str
    endpoint_path: str


@dataclass(frozen=True)
class ProcessSpec:
    family: str
    provider: str
    binary: str
    spawn: tuple[str, ...]
    stop: tuple[str, ...]
    pidfile: str
    log: str
    pidfile_env: str
    subcommand: str
    daemon_module: str
    port_flag: str
    model_flag: str
    scope: str
    default_origin: str
    wt_port: int
    restore_action: str
    ready: Ready


def contract_path() -> Path:
    """The contract file: LLMBENCH_PROCESS_PROVIDERS, else the repo's copy.

    Depth-dependent: this module is llmbench/src/llmbench/providers/, so
    parents[4] is the repo root."""
    override = os.environ.get("LLMBENCH_PROCESS_PROVIDERS")
    if override:
        return Path(override).expanduser()
    return Path(__file__).resolve().parents[4] / "docs" / "contracts" / "process-providers.toml"


def _get(raw: dict[str, Any], key: str, where: str) -> Any:
    if key not in raw:
        raise ProcessSpecError(f"{where}: missing key `{key}`")
    return raw[key]


def _argv(raw: dict[str, Any], key: str, where: str) -> tuple[str, ...]:
    """An argv template: a list of strings. A bare string would otherwise
    become a tuple of its characters and run as `rec s t a t u s`."""
    value = _get(raw, key, where)
    if not isinstance(value, list) or not all(isinstance(v, str) for v in value):
        raise ProcessSpecError(f"{where}: `{key}` must be a list of strings")
    return tuple(value)


def _parse_ready(raw: Any, where: str) -> Ready:
    if not isinstance(raw, dict):
        raise ProcessSpecError(f"{where}: missing table `ready`")
    w = f"{where}.ready"
    kind = str(_get(raw, "kind", w))
    if kind not in READY_KINDS:
        raise ProcessSpecError(f"{w}: unknown kind `{kind}` (known: {', '.join(READY_KINDS)})")
    return Ready(
        kind=kind,
        command=_argv(raw, "command", w),
        path=str(_get(raw, "path", w)),
        equals=str(_get(raw, "equals", w)),
        model_path=str(_get(raw, "model_path", w)),
        endpoint_path=str(_get(raw, "endpoint_path", w)),
    )


def _parse(raw: dict[str, Any], index: int) -> ProcessSpec:
    where = f"process[{index}]"

    def s(key: str) -> str:
        return str(_get(raw, key, where))

    return ProcessSpec(
        family=s("family"),
        provider=s("provider"),
        binary=s("binary"),
        spawn=_argv(raw, "spawn", where),
        stop=_argv(raw, "stop", where),
        pidfile=s("pidfile"),
        log=s("log"),
        pidfile_env=s("pidfile_env"),
        subcommand=s("subcommand"),
        daemon_module=s("daemon_module"),
        port_flag=s("port_flag"),
        model_flag=s("model_flag"),
        scope=s("scope"),
        default_origin=s("default_origin"),
        wt_port=int(_get(raw, "wt_port", where)),
        restore_action=s("restore_action"),
        ready=_parse_ready(raw.get("ready"), where),
    )


def process_specs() -> dict[str, ProcessSpec]:
    """Every process provider in the contract file, keyed by provider id."""
    return _load(contract_path())


@functools.cache
def _load(path: Path) -> dict[str, ProcessSpec]:
    """Parse one contract file; cached per path so swapping the file works."""
    try:
        with open(path, "rb") as f:
            data = tomllib.load(f)
    except (OSError, ValueError) as exc:
        raise ProcessSpecError(f"cannot read {path}: {exc}") from exc
    specs = [_parse(raw, i) for i, raw in enumerate(data.get("process", []))]
    return {sp.provider: sp for sp in specs}


def process_spec(provider_id: str) -> ProcessSpec | None:
    return process_specs().get(provider_id)


def expand(template: tuple[str, ...], *, model: str = "", port: int = 0) -> list[str]:
    """Replace {model} and {port} in each argv element."""
    return [t.replace("{model}", model).replace("{port}", str(port)) for t in template]
