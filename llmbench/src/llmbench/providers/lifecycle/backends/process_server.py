"""One spec-driven backend for every process-style provider (mtplx, rec).

A `ProcessSpec` from `docs/contracts/process-providers.toml` says how to run
the server: the binary, the spawn and stop argv templates, the pidfile and
log, and how to tell it is ready (`models-list`: the model appears at
/v1/models; `command-json`: a status command's JSON says so). The server is a
plain backgrounded subprocess tracked by a pidfile, never a LaunchAgent.

Addresses come from the registry provider row's `auth.base_url` when it names
one, else the spec's `default_origin`; the port in the argv is that URL's
port (`wt_port` when it has none). The registry is read at call time, and a
missing registry or row falls back to the default rather than failing.

A provider row with a `secret_ref` has its key resolved once per lifecycle
call and passed to every HTTP probe; without one no probe carries a key (the
probe calls are then exactly the key-less ones). `resolve()` never resolves
the key: it must stay pure (an `exec:` reference runs a command).
"""

from __future__ import annotations

import ipaddress
import subprocess
from dataclasses import dataclass
from typing import TypedDict
from urllib.parse import urlsplit, urlunsplit

from .... import registry as _registry
from ....registry import ProviderEntry, load_registry
from ...process_spec import ProcessSpec, expand
from ...secret import resolve_secret
from .. import binaries, probe
from ..envelope import LifecycleError
from ..pidproc import PidfileProcess
from .base import PidfileTrackedBackend, StartPlan

# The warmup budget mtplx always had; shorter than probe.WARMUP_TIMEOUT.
WARM_TIMEOUT = 120.0
# The pre-spawn wait for the previous occupant to release the port.
PREBIND_TIMEOUT = 10.0


class _KeyArg(TypedDict, total=False):
    key: str


def _key_arg(key: str | None) -> _KeyArg:
    """`key=` for a probe call, or nothing at all: a key-less provider's
    probe calls stay exactly the calls made before keys existed."""
    return {} if key is None else {"key": key}


def _provider_row(provider_id: str) -> ProviderEntry | None:
    """The registry row for `provider_id`, or None when the registry or the
    row is missing or the file cannot be read or parsed. Best-effort by
    design: the address falls back to the spec's default origin, and a
    `chat_url` read inside orchestrate's envelope handler must never raise
    (a malformed model row raises AttributeError, not RegistryError, from
    `load_registry`). Reads through the module (not the `load_registry` name
    this module imports for model resolution) so an address lookup is
    independent of the model lookup."""
    try:
        return _registry.load_registry().provider(provider_id)
    except Exception:  # noqa: BLE001 — best-effort lookup, see docstring
        return None


def _base_origin(url: str) -> str:
    """wt's config.BaseOrigin: trailing slashes and one `/v1` suffix off."""
    return url.rstrip("/").removesuffix("/v1").rstrip("/")


def _serves_at(host: str) -> bool:
    """wt's servesMtplxAt: a host the server spawned with `--host 127.0.0.1`
    answers on — "localhost" (any case, optional root dot), 127.0.0.1, or the
    unspecified address (0.0.0.0, ::). Not ::1 nor the rest of 127/8."""
    if host.removesuffix(".").lower() == "localhost":
        return True
    try:
        ip = ipaddress.ip_address(host)
    except ValueError:
        return False
    return ip == ipaddress.IPv4Address("127.0.0.1") or ip.is_unspecified


_SCHEME_PORTS = {"http": 80, "https": 443}


def _origin_key(url: str) -> tuple[str, str, int | None] | None:
    """(scheme, host, port) of `url` for an origin comparison, the port
    defaulted by scheme; None when `url` is not an absolute URL."""
    try:
        parts = urlsplit(url)
        port = parts.port
    except ValueError:
        return None
    if not parts.scheme or not parts.hostname:
        return None
    scheme = parts.scheme.lower()
    if port is None:
        port = {"http": 80, "https": 443}.get(scheme)
    return scheme, parts.hostname.lower(), port


def _as_text(value: object) -> str | None:
    """A JSON scalar as the text a contract `equals` is compared with."""
    if value is None:
        return None
    if isinstance(value, bool):
        return "true" if value else "false"
    return str(value)


@dataclass(frozen=True)
class _Address:
    origin: str  # wt's Provider.Origin(): BaseOrigin, port filled per #348
    port: int

    @property
    def health_url(self) -> str:
        return f"{self.origin}/v1/models"

    @property
    def chat_url(self) -> str:
        return f"{self.origin}/v1/chat/completions"


class ProcessServerBackend(PidfileTrackedBackend):
    """A process provider's lifecycle, read off its `ProcessSpec`.

    `cleanup_on_failure` is a constructor argument, not a spec field: mtplx
    passes True and is the only backend that does (a failed start tears its
    server back down); every other spec is registered with False.
    """

    env_var = None
    default_model = None

    def __init__(self, spec: ProcessSpec, *, cleanup_on_failure: bool = False) -> None:
        super().__init__()
        self.spec = spec
        self.id = spec.provider
        self.occupancy_key = spec.provider
        self.restore_action = spec.restore_action
        self.cleanup_on_failure = cleanup_on_failure
        self._pidproc = PidfileProcess(name=spec.provider, pidfile=spec.pidfile, logfile=spec.log)

    # --- addresses and key -------------------------------------------------

    def _address(self) -> _Address:
        """Where this provider is: wt's `Provider.Origin()` of the row's
        `auth.base_url`, else of the spec's `default_origin`. An http url
        naming no port, on a host the spawned server answers on, gets the
        spec's `wt_port` (the server is spawned there; read as written it
        would mean port 80, #348). Any other url is left exactly as written,
        and the port the argv gets is the one the probes use: the url's own,
        else its scheme's (80, 443)."""
        row = _provider_row(self.id)
        for url in (row.base_url if row else "", self.spec.default_origin):
            if not url:
                continue
            origin = _base_origin(url)
            try:
                parts = urlsplit(origin)
                port = parts.port
            except ValueError:
                continue
            host = parts.hostname
            if not parts.scheme or not host:
                continue
            if port is None and parts.scheme == "http" and _serves_at(host):
                port = self.spec.wt_port
                # The netloc as written (case kept, IPv6 brackets kept), as
                # wt's u.Hostname() + JoinHostPort spells it.
                userinfo, at, written = parts.netloc.rpartition("@")
                origin = urlunsplit(
                    parts._replace(netloc=f"{userinfo}{at}{written.rstrip(':')}:{port}")
                )
            if port is None:
                port = _SCHEME_PORTS.get(parts.scheme.lower(), self.spec.wt_port)
            return _Address(origin, port)
        raise LifecycleError(
            f"{self.id}: no usable address (default_origin {self.spec.default_origin!r})"
        )

    # Read-only properties over Backend's plain attributes: derived from the
    # registry at call time.
    @property
    def health_url(self) -> str:  # type: ignore[override]
        return self._address().health_url

    @property
    def chat_url(self) -> str:  # type: ignore[override]
        return self._address().chat_url

    def _key(self, *, strict: bool = True) -> str | None:
        """The provider's bearer key, resolved now, or None when its row
        names no `secret_ref` (or the reference resolves to ""). An
        unreadable reference raises a LifecycleError naming the provider
        (never the value) when `strict`, and reads as no key otherwise."""
        row = _provider_row(self.id)
        if row is None or not row.secret_ref:
            return None
        try:
            key = resolve_secret(row.secret_ref)
        except LifecycleError as exc:
            if not strict:
                return None
            raise LifecycleError(f"{self.id}: cannot read the provider key: {exc}") from None
        return key or None

    # --- lifecycle ---------------------------------------------------------

    def check_available(self) -> str | None:
        try:
            binaries.require_binary(self.spec.binary)
        except LifecycleError as exc:
            return str(exc)
        return None

    def resolve(self, model: str | None, extra_args: tuple[str, ...]) -> StartPlan:
        """The model to serve: the explicit `model`, else the first
        positional `extra_args` entry (how the runners forward it), else the
        single model of this provider in the registry. With no explicit model
        and more than one registry entry, refuse rather than guess — a caller
        that failed to forward the model name would otherwise silently serve
        (and benchmark) the wrong weights. Pure: reads the registry, runs
        nothing."""
        resolved = model or self._positional_arg(extra_args, 0)
        if not resolved:
            registry = load_registry()
            matches = [m.model_name for m in registry.models if m.provider_id == self.id]
            if len(matches) == 1:
                resolved = matches[0]
            elif not matches:
                raise LifecycleError(f"no {self.id} model in the registry")
            else:
                raise LifecycleError(
                    f"model required: registry holds {len(matches)} {self.id} models "
                    f"({', '.join(matches)})"
                )
        return StartPlan(model=resolved, direct_url=self.chat_url)

    def _status(self, bin_path: str, path: str) -> object | None:
        return probe.command_json([bin_path, *self.spec.ready.command], path=path)

    def _reports_ready(self, bin_path: str) -> bool:
        ready = self.spec.ready
        return _as_text(self._status(bin_path, ready.path)) == ready.equals

    def already_serving(self, plan: StartPlan) -> bool:
        """`models-list`: /v1/models lists the model. `command-json`: the
        status command reports ready, with the requested model, and (when
        the spec names an endpoint path) an endpoint at this backend's
        origin. Resolves the key first, so an unreadable one fails an
        isolation before any teardown."""
        key = self._key()
        addr = self._address()
        ready = self.spec.ready
        if ready.kind == "models-list":
            return probe.serving_model(addr.health_url, plan.model, **_key_arg(key))
        try:
            bin_path = binaries.require_binary(self.spec.binary)
        except LifecycleError:
            return False
        if not self._reports_ready(bin_path):
            return False
        if ready.model_path and _as_text(self._status(bin_path, ready.model_path)) != plan.model:
            return False
        if ready.endpoint_path:
            endpoint = _as_text(self._status(bin_path, ready.endpoint_path))
            if endpoint is None or _origin_key(endpoint) != _origin_key(addr.origin):
                return False
        return True

    def replace_own_occupant(self, plan: StartPlan) -> None:
        stop_warning = self.stop_and_wait()
        if stop_warning is not None:
            # Surface the real cause here rather than falling through to
            # start()'s wait_for_port_closed poll, which would fail ~10s
            # later with a generic "port still answering" message.
            raise LifecycleError(stop_warning)

    def start(self, plan: StartPlan) -> None:
        bin_path = binaries.require_binary(self.spec.binary)
        self._key()  # an unreadable key fails here, before anything is spawned
        addr = self._address()
        # Wait for the OS to reclaim the port before spawning: spawning into
        # a still-held port can leave the old process answering warmup, or
        # the new one may die at once and leave a stale pidfile. A fast poll
        # (returns as soon as the port is closed) kept as a safety net.
        probe.wait_for_port_closed(addr.health_url, timeout=PREBIND_TIMEOUT)
        self._proc = self._pidproc.spawn(
            [bin_path, *expand(self.spec.spawn, model=plan.model, port=addr.port)]
        )

    def wait_ready(self, plan: StartPlan) -> None:
        ready = self.spec.ready
        if ready.kind == "models-list":
            key = self._key()
            probe.wait_for_model(
                self._address().health_url,
                plan.model,
                proc=self._proc,
                timeout=probe.MODEL_LOAD_TIMEOUT,
                **_key_arg(key),
            )
            return
        bin_path = binaries.require_binary(self.spec.binary)
        probe.wait_for_command_ready(
            [bin_path, *ready.command],
            path=ready.path,
            equals=ready.equals,
            proc=self._proc,
            timeout=probe.MODEL_LOAD_TIMEOUT,
        )

    def warm(self, plan: StartPlan) -> None:
        key = self._key()
        addr = self._address()
        probe.warmup(
            addr.chat_url,
            plan.model,
            health_url=addr.health_url,
            timeout=WARM_TIMEOUT,
            **_key_arg(key),
        )

    def stop_and_wait(self) -> str | None:
        """Run the spec's stop command, then confirm the server let go:
        `models-list` polls the port closed; `command-json` also needs the
        status command to stop reporting ready.

        The confirmation runs whatever the stop command's exit code: a stop
        command may exit non-zero when nothing is running (`mtplx stop` with
        nothing on its port, `rec stop` with no instance), exactly the state
        on a first start, and that must read as success. The command's own
        stderr is surfaced only when the server genuinely stayed up.

        An unreadable key does not block a teardown: the stop command needs
        none, and a key-less port poll reads a 401 as still open.
        """
        binary = self.spec.binary
        try:
            bin_path = binaries.require_binary(binary)
        except LifecycleError:
            return f"{binary} binary not found on PATH"
        addr = self._address()
        key = self._key(strict=False)
        result = subprocess.run(
            [bin_path, *expand(self.spec.stop, port=addr.port)],
            capture_output=True,
            text=True,
            check=False,
        )
        stopped = probe.port_closed_within(
            addr.health_url, timeout=probe.STOP_WAIT_TIMEOUT, **_key_arg(key)
        )
        if stopped and self.spec.ready.kind == "command-json":
            stopped = not self._reports_ready(bin_path)
        if stopped:
            return None
        if result.returncode != 0:
            return result.stderr.strip() or result.stdout.strip() or f"{binary} stop failed"
        return f"{binary} still listening on port {addr.port}"
