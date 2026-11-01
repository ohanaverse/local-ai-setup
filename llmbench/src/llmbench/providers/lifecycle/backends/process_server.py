"""One spec-driven backend for every process-style provider in the contract
(mtplx, rec, sushi).

A `ProcessSpec` from `docs/contracts/process-providers.toml` says how to run
the server: the binary, the spawn and stop argv templates, the pidfile and
log, the warmup budget (`warmup_seconds`), and how to tell it is ready
(`models-list`: the model appears at /v1/models; `command-json`: a status
command's JSON says so; `http-get`: a path answers 200, the model listed
as loaded only once the first request loads it). The server is a plain
backgrounded subprocess tracked by a pidfile, never a LaunchAgent.

An `http-get` server lists every pack it can load, flagging the ones not in
memory `loaded: false`: serving means listed as loaded, by the exact id, and
the warm checks the pack is listed before its chat and loaded after it. A
spec with an empty `stop` (stopped by signalling its pid) gets the server
log's tail on a failed warm, and the GPU wired-limit fix when the failure
reads as macOS refusing the memory.

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
import re
import subprocess
import sys
import time
import urllib.error
from dataclasses import dataclass
from pathlib import Path
from typing import TypedDict
from urllib.parse import urlsplit, urlunsplit

from .... import registry as _registry
from ....registry import ProviderEntry, load_registry
from ... import secret as _secret
from ...process_spec import ProcessSpec, expand
from .. import binaries, probe
from ..envelope import LifecycleError
from ..pidproc import PidfileProcess
from .base import PidfileTrackedBackend, StartPlan

# The pre-spawn wait for the previous occupant to release the port.
PREBIND_TIMEOUT = 10.0
# A command-json provider's instance is gone only when its status command
# reports none at all (it fails). rec's status still answers, ready:false,
# while an instance loads and while it shuts down, and an instance holding
# several GB takes seconds to let go of them. The models-list kind keeps
# probe.STOP_WAIT_TIMEOUT unchanged.
INSTANCE_GONE_TIMEOUT = 30.0
INSTANCE_GONE_POLL = 1.0
# A signal-stop server (empty `stop`) gets SIGTERM and this long to close
# its port: wt's 10 s grace before SIGKILL, plus margin for the port to go.
SIGNAL_STOP_TIMEOUT = 15.0

# The last status outcome ("answered" or "unknown") and value of an instance
# that had not gone by the deadline.
_Lingering = tuple[str, object | None]


class _KeyArg(TypedDict, total=False):
    key: str


def resolve_secret(ref: str) -> str:
    """`providers.secret.resolve_secret`, looked up at call time. Importing
    the module (not the name) breaks an import cycle: `providers.secret`
    imports `lifecycle.envelope`, whose package imports these backends, so
    whichever of the two is imported first must not need the other's names
    at import time. Tests patch this name."""
    return _secret.resolve_secret(ref)


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
        port = _SCHEME_PORTS.get(scheme)
    return scheme, parts.hostname.lower(), port


_TAIL_WITHHELD = "log tail withheld: provider key unreadable, cannot mask"

# The fix for a warmup macOS refused the memory for, and the phrases that make
# a failed warmup (its log tail included) read as one. A pinned heuristic, the
# same list as wt's memoryRefusalMarkers (each side's test pins it). The bare
# word "iogpu" is left out: it appears in unrelated startup lines of the
# server log and would send the user to raise a limit that was never the
# problem.
_WIRED_LIMIT_HINT = (
    "raise the GPU wired limit with `sudo sysctl iogpu.wired_limit_mb=59000` (resets on reboot)"
)
_MEMORY_REFUSAL = re.compile(
    r"out of memory|insufficient memory|not enough memory|cannot allocate|failed to allocate"
    r"|memory limit|wired limit|wired_limit",
    re.IGNORECASE,
)


# The wired-limit preflight (a spec with `wired_limit` only; the same integer
# rule as wt's wiredNeedMiB/parseWiredLimit). A pack loads only when macOS
# lets the GPU wire about 1.1 x its safetensors; llmbench reads the sysctl and
# never sets it (that needs sudo).


def _models_root(row: ProviderEntry | None, spec: ProcessSpec) -> Path:
    """The directory holding the packs: the provider row's `model_dir`, else
    the spec's `model_dir` (the contract's copy of wt's family default), `~`
    expanded."""
    return Path((row.model_dir if row else "") or spec.model_dir).expanduser()


def _safetensors_bytes(pack_dir: Path) -> int | None:
    """The bytes of the `*.safetensors` files directly in `pack_dir` (what
    stays resident; `ngram_table.bin` and the rest do not count). None when
    the directory cannot be read or holds none."""
    try:
        sizes = [p.stat().st_size for p in pack_dir.glob("*.safetensors") if p.is_file()]
    except OSError:
        return None
    return sum(sizes) if sizes else None


def _need_mib(total_bytes: int) -> int:
    """110% of the safetensors in MiB, floored. Integer only."""
    return total_bytes * 11 // 10 // 1048576


def _parse_wired_limit(out: str) -> int | None:
    """`sysctl -n iogpu.wired_limit_mb hw.memsize` output to the limit in
    MiB. A limit of 0 is macOS's default, 75% of RAM. None unless both
    numbers are there."""
    lines = out.strip().split("\n")
    if len(lines) != 2:
        return None
    try:
        limit, memsize = int(lines[0].strip()), int(lines[1].strip())
    except ValueError:
        return None
    if limit < 0 or memsize <= 0:
        return None
    return memsize * 3 // 4 // 1048576 if limit == 0 else limit


# wt caps the same sysctl read at 5 s (internal/lifecycle/wiredlimit_darwin.go).
_SYSCTL_TIMEOUT = 5.0


def _wired_limit_mb() -> int | None:
    """The GPU wired limit in MiB, read in one sysctl call; None when it
    cannot be read (not macOS, sysctl failed, hung past _SYSCTL_TIMEOUT or
    answered oddly, undecodable bytes included). It runs in
    `already_serving`, before any teardown, so it must neither hang nor
    raise: None prints the preflight's skip line instead."""
    if sys.platform != "darwin":
        return None
    try:
        done = subprocess.run(
            ["sysctl", "-n", "iogpu.wired_limit_mb", "hw.memsize"],
            capture_output=True,
            text=True,
            check=False,
            timeout=_SYSCTL_TIMEOUT,
        )
    except (OSError, ValueError, subprocess.TimeoutExpired):
        # ValueError covers UnicodeDecodeError from text=True on garbage.
        return None
    return _parse_wired_limit(done.stdout) if done.returncode == 0 else None


def _masked_tail(tail: str, key: str | None) -> str:
    """A log tail (already bounded by `PidfileProcess.log_tail`) with `key`
    masked the way probe masks an echoed body, plus a trailing part of the
    key left at the tail's cut start dropped, so no fragment survives."""
    if key:
        for n in range(len(key) - 1, 0, -1):
            if tail.startswith(key[-n:]):
                tail = tail[n:]
                break
        tail = probe._scrub(tail, key)  # noqa: SLF001 — the one masking rule
    return tail.strip()


@dataclass(frozen=True)
class _Address:
    origin: str  # wt's Provider.Origin(): BaseOrigin, port filled per #348
    port: int

    def url(self, path: str) -> str:
        return f"{self.origin}{path}"

    @property
    def health_url(self) -> str:
        return self.url("/v1/models")

    @property
    def chat_url(self) -> str:
        return self.url("/v1/chat/completions")


class ProcessServerBackend(PidfileTrackedBackend):
    """A process provider's lifecycle, read off its `ProcessSpec`.

    `cleanup_on_failure` is a constructor argument, not a spec field: mtplx
    passes True and is the only backend that does (a failed start tears its
    server back down); every other spec is registered with False. A
    signal-stop spec (empty `stop`) always cleans up, as wt does: its
    half-loaded server and pidfile do not outlive a failed start.
    """

    env_var = None
    default_model = None

    def __init__(self, spec: ProcessSpec, *, cleanup_on_failure: bool = False) -> None:
        super().__init__()
        self.spec = spec
        self.id = spec.provider
        self.occupancy_key = spec.provider
        self.restore_action = spec.restore_action
        self.cleanup_on_failure = cleanup_on_failure or not spec.stop
        self._pidproc = PidfileProcess(name=spec.provider, pidfile=spec.pidfile, logfile=spec.log)
        # The key resolved for the current isolate: `(key,)` once resolved,
        # None before. Reset by `already_serving`, the first key-using step
        # of every isolate, so the `exec:` command runs once per isolate and
        # a rotated key is picked up by the next one.
        self._resolved_key: tuple[str | None] | None = None
        # True after a resolution of a named `secret_ref` failed: nothing to
        # mask a log tail with, so the tail is withheld.
        self._key_unreadable = False
        # The registry provider row read for the current isolate, so its
        # address and key come from one read and cannot differ between steps.
        # Dropped with the key (`_forget_key`) and at each stop.
        self._row: tuple[ProviderEntry | None] | None = None

    # --- addresses and key -------------------------------------------------

    def _address(self) -> _Address:
        """Where this provider is: wt's `Provider.Origin()` of the row's
        `auth.base_url`, else of the spec's `default_origin`. An http url
        naming no port, on a host the spawned server answers on, gets the
        spec's `wt_port` (the server is spawned there; read as written it
        would mean port 80, #348). Any other url is left exactly as written,
        and the port the argv gets is the one the probes use: the url's own,
        else its scheme's (80, 443)."""
        row = self._provider_row()
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
        """The provider's bearer key for this isolate, or None when its row
        names no `secret_ref` (or the reference resolves to ""). Resolved
        once and reused until `_forget_key()`. An unreadable reference is
        not remembered: it raises a LifecycleError naming the provider
        (never the value) when `strict`, and reads as no key otherwise, and
        either way marks the key unreadable for the log-tail rule."""
        if self._resolved_key is not None:
            return self._resolved_key[0]
        row = self._provider_row()
        if row is None or not row.secret_ref:
            self._resolved_key = (None,)
            return None
        try:
            key = resolve_secret(row.secret_ref)
        except LifecycleError as exc:
            self._key_unreadable = True
            if not strict:
                return None
            raise LifecycleError(f"{self.id}: cannot read the provider key: {exc}") from None
        self._key_unreadable = False
        self._resolved_key = (key or None,)
        return key or None

    def _forget_key(self) -> None:
        self._resolved_key = None
        self._key_unreadable = False
        self._forget_row()

    def _provider_row(self) -> ProviderEntry | None:
        if self._row is None:
            self._row = (_provider_row(self.id),)
        return self._row[0]

    def _forget_row(self) -> None:
        self._row = None

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

    def already_serving(self, plan: StartPlan) -> bool:
        """`models-list`: /v1/models lists the model. `http-get`: it lists
        the exact id as loaded (a pack listed `loaded: false` is not
        serving). `command-json`: the
        status command reports ready, with the requested model, and (when
        the spec names an endpoint path) an endpoint at this backend's
        origin. The first key-using step of every isolate: forgets the
        previous isolate's key and resolves it afresh, so an unreadable one
        fails an isolation before any teardown.

        A signal-stop spec first refuses a server on its port with no
        verified pidfile: neither llmbench nor wt started it, so the context
        size it runs with is unknown. Raised here, before `isolate` stops
        any other provider. Then, when the pack is not already loaded, the
        wired-limit preflight (same reason, same place)."""
        self._forget_key()
        key = self._key()
        addr = self._address()
        if (
            self._signal_stop
            and probe.wait_for_port_open(
                addr.health_url, timeout=1.0, interval=0.2, **_key_arg(key)
            )
            and self._pidproc.live_pid(self._expect_argv(addr)) is None
        ):
            raise LifecycleError(
                f"{self._unowned(addr)}; its context size cannot be checked, so it is not "
                "reused. Stop it yourself and retry"
            )
        serving = self._serving(plan, addr, key)
        if self.spec.wired_limit and not serving:
            self._preflight_wired_limit(plan)
        return serving

    def _serving(self, plan: StartPlan, addr: _Address, key: str | None) -> bool:
        """Whether the server already serves `plan.model` (see
        `already_serving`)."""
        ready = self.spec.ready
        # Only a spec that opts in passes the flag: every other spec's probe
        # call stays exactly what it was.
        loaded: dict[str, bool] = {"loaded_only": True} if self.spec.loaded_only else {}
        if ready.kind == "models-list":
            return probe.serving_model(addr.health_url, plan.model, **loaded, **_key_arg(key))
        if ready.kind == "http-get":
            return probe.serving_model(
                addr.health_url, plan.model, exact=True, **loaded, **_key_arg(key)
            )
        try:
            bin_path = binaries.require_binary(self.spec.binary)
        except LifecycleError:
            return False
        # One run of the status command, read three ways: the fields cannot
        # disagree the way three separate runs of a flapping status could.
        _, snapshot = probe.command_object([bin_path, *ready.command])
        if snapshot is None:
            return False

        def field(path: str) -> str | None:
            return probe.as_text(probe.scalar_at(snapshot, path))

        if field(ready.path) != ready.equals:
            return False
        if ready.model_path and field(ready.model_path) != plan.model:
            return False
        if ready.endpoint_path:
            endpoint = field(ready.endpoint_path)
            if endpoint is None or _origin_key(endpoint) != _origin_key(addr.origin):
                return False
        return True

    def _preflight_wired_limit(self, plan: StartPlan) -> None:
        """Fail early, before anything is stopped, when the GPU wired limit
        cannot hold the pack. What cannot be measured is not judged: one
        skip line, and the isolate goes on."""

        def skip(why: str) -> None:
            print(f"{self.id}: skipping the memory check ({why})", file=sys.stderr)

        pack_bytes = _safetensors_bytes(_models_root(self._provider_row(), self.spec) / plan.model)
        if pack_bytes is None:
            skip(f"cannot read the safetensors of {plan.model}")
            return
        limit = _wired_limit_mb()
        if limit is None:
            skip("cannot read iogpu.wired_limit_mb")
            return
        need = _need_mib(pack_bytes)
        if limit < need:
            raise LifecycleError(
                f"{self.id}: the GPU wired limit is {limit} MiB but {plan.model} needs about "
                f"{need} MiB (1.1 x its safetensors); {_WIRED_LIMIT_HINT}"
            )

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
        if self.spec.ready.kind == "command-json":
            # A closed port is not enough: an instance that is still loading
            # or still shutting down has its port closed too, and spawning
            # beside it would put two instances (and their memory) side by
            # side.
            lingering = self._lingering_status(bin_path)
            if lingering is not None:
                raise LifecycleError(self._lingering_warning(lingering))
        argv = [bin_path, *expand(self.spec.spawn, model=plan.model, port=addr.port)]
        try:
            # Only a signal-stop spec gets its own session (wt's Setsid): the
            # other specs' spawn call is unchanged.
            if self._signal_stop:
                self._proc = self._pidproc.spawn(argv, new_session=True)
            else:
                self._proc = self._pidproc.spawn(argv)
        except LifecycleError as exc:
            # spawn()'s immediate-exit error carries the log's tail: mask the
            # key in it the way wait_ready() does.
            head, sep, raw = str(exc).partition("; log tail: ")
            if not sep:
                raise
            raise LifecycleError(self._with_log_tail(head, raw)) from None

    def wait_ready(self, plan: StartPlan) -> None:
        """Wait for the spawned server to be ready. A load that fails (the
        process exited, or the wait timed out) carries the server log's tail,
        as wt's readiness-wait failure does and as spawn()'s immediate-exit
        check already did; the key is masked in it. A row that names a key
        which cannot be read leaves nothing to mask with, so the tail is
        withheld. A refused key is not a load failure and is raised as it
        is."""
        try:
            self._wait_ready(plan)
        except probe.WarmupRefusedError:
            raise
        except LifecycleError as exc:
            message = self._with_log_tail(str(exc), self._pidproc.log_tail())
            if message == str(exc):
                raise
            raise LifecycleError(message) from exc

    def _with_log_tail(self, message: str, raw: str) -> str:
        """`message` with the log tail `raw` appended, the key masked in it;
        a row that names a key which cannot be read leaves nothing to mask
        with, so the tail is withheld. `message` alone for an empty tail."""
        key = self._key(strict=False)
        if key is None and self._key_unreadable and raw.strip():
            return f"{message}; {_TAIL_WITHHELD}"
        tail = _masked_tail(raw, key)
        return f"{message}; log tail: {tail}" if tail else message

    def _wait_ready(self, plan: StartPlan) -> None:
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
        if ready.kind == "http-get":
            probe.wait_for_http_ok(
                self._address().url(ready.path),
                proc=self._proc,
                timeout=probe.MODEL_LOAD_TIMEOUT,
                **_key_arg(self._key()),
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

    @property
    def _signal_stop(self) -> bool:
        """Stopped by signalling its verified pid: the spec names no stop
        command."""
        return not self.spec.stop

    def warm(self, plan: StartPlan) -> None:
        """Warm the model within the spec's `warmup_seconds`. On a signal-stop
        spec a failed warm carries the server log's tail (masked, as
        `wait_ready`'s does), and, with `wired_limit`, the wired-limit fix when the combined text
        reads as a memory refusal; the warmup's own error types are kept
        unless the fix is added. Any other spec's failure is raised as it
        is."""
        try:
            self._warm(plan)
        except probe.WarmupRefusedError:
            # Not a load failure (as in wait_ready): no log tail, no memory hint.
            raise
        except LifecycleError as exc:
            if not self._signal_stop:
                raise
            message = self._with_log_tail(str(exc), self._pidproc.log_tail())
            if self.spec.wired_limit and _MEMORY_REFUSAL.search(message):
                raise LifecycleError(f"{message}; {_WIRED_LIMIT_HINT}") from exc
            if message == str(exc):
                raise
            raise type(exc)(message) from exc

    def _warm(self, plan: StartPlan) -> None:
        """The warmup chat. `http-get` first refuses a pack id a non-empty
        list lacks (exact, against every listed id, loaded or not; a list
        that cannot be read fails) and afterwards requires the pack listed
        as loaded. A chat that outlasts the budget is a LifecycleError; any
        other connection failure is raised as it is."""
        key = self._key()
        addr = self._address()
        spec = self.spec
        http_get = spec.ready.kind == "http-get"
        budget = float(spec.warmup_seconds)
        if http_get:
            ids = probe.listed_ids(addr.health_url, strict=True, **_key_arg(key))
            if ids and plan.model not in ids:
                raise LifecycleError(
                    f"{self.id}: {plan.model!r} is not listed by the server "
                    f"(it lists: {', '.join(ids)})"
                )
        timed_out = f"{self.id}: warmup of {plan.model} timed out after {spec.warmup_seconds}s"
        try:
            probe.warmup(
                addr.chat_url,
                plan.model,
                health_url=addr.health_url,
                timeout=budget,
                request_timeout=budget,
                **_key_arg(key),
            )
        except TimeoutError as exc:
            raise LifecycleError(timed_out) from exc
        except urllib.error.URLError as exc:
            if not isinstance(exc.reason, TimeoutError):
                raise
            raise LifecycleError(timed_out) from exc
        if http_get and not probe.serving_model(
            addr.health_url, plan.model, exact=True, loaded_only=True, **_key_arg(key)
        ):
            raise LifecycleError(
                f"{self.id}: {plan.model} answered the warmup but is not listed as loaded "
                f"at {addr.health_url}"
            )

    def _unowned(self, addr: _Address) -> str:
        return (
            f"{self.id}: a server answers on port {addr.port} that was not started by "
            f"llmbench or wt (no verified pidfile at {self.spec.pidfile})"
        )

    def stop_and_wait(self) -> str | None:
        """Run the spec's stop command, then confirm the server let go:
        `models-list` polls the port closed; `command-json` also needs the
        status command to report no instance at all (`_lingering_status`):
        ready:false still means an instance, shutting down or loading.

        The confirmation runs whatever the stop command's exit code: a stop
        command may exit non-zero when nothing is running (`mtplx stop` with
        nothing on its port, `rec stop` with no instance), exactly the state
        on a first start, and that must read as success. The command's own
        stderr is surfaced only when the server genuinely stayed up.

        No key is involved: the stop command needs none, and the port poll
        counts any HTTP answer, a 401 included, as still open. A binary that
        is not installed has nothing running to stop (None), so a teardown
        fan-out over every backend does not warn about providers the machine
        never had.

        An empty `stop` never runs the binary: `_signal_stop_and_wait`.
        """
        if self._signal_stop:
            return self._signal_stop_and_wait()
        binary = self.spec.binary
        try:
            bin_path = binaries.require_binary(binary)
        except LifecycleError:
            return None
        self._forget_row()
        addr = self._address()
        result = subprocess.run(
            [bin_path, *expand(self.spec.stop, port=addr.port)],
            capture_output=True,
            text=True,
            check=False,
        )
        if self.spec.ready.kind != "command-json":
            stopped = probe.port_closed_within(addr.health_url, timeout=probe.STOP_WAIT_TIMEOUT)
            if stopped:
                return None
            if result.returncode != 0:
                return result.stderr.strip() or result.stdout.strip() or f"{binary} stop failed"
            return f"{binary} still listening on port {addr.port}"
        # command-json: one INSTANCE_GONE_TIMEOUT deadline over both the port
        # closing and the instance going (a draining instance may hold its
        # port open for longer than probe.STOP_WAIT_TIMEOUT).
        deadline = time.monotonic() + INSTANCE_GONE_TIMEOUT
        lingering: _Lingering | None = None
        stopped = probe.port_closed_within(addr.health_url, timeout=INSTANCE_GONE_TIMEOUT)
        if stopped:
            lingering = self._lingering_status(bin_path, deadline=deadline)
            stopped = lingering is None
        if stopped:
            return None
        if result.returncode != 0:
            return result.stderr.strip() or result.stdout.strip() or f"{binary} stop failed"
        if lingering is not None:
            return self._lingering_warning(lingering)
        return f"{binary} still listening on port {addr.port}"

    def _expect_argv(self, addr: _Address) -> list[str]:
        """The arguments a server this backend spawned runs with (the spec's
        spawn template at `addr`'s port): what the strict identity check
        compares a pidfile's process against, as wt's serverArgvMatches does."""
        return expand(self.spec.spawn, model="", port=addr.port)

    def _signal_stop_and_wait(self) -> str | None:
        """Stop by the pidfile: signal the pid it names when that pid is a
        live process of this user running exactly this server's argv
        (`PidfileProcess.stop_verified`: SIGTERM, SIGKILL after 10 s, the
        pidfile kept until the process is gone), then wait up to
        SIGNAL_STOP_TIMEOUT for the port to close. A pidfile naming pid 1 or
        this process is never signalled. A server still answering is
        reported as one llmbench cannot own when no verified pid was found,
        else as one that did not let go.

        With no verified pid nothing of ours was signalled, so there is
        nothing to wait for: one probe (`probe.port_answers`) and the
        "stop it yourself" warning at once. Otherwise every other
        provider's isolate (`_stop_others`) would pay SIGNAL_STOP_TIMEOUT
        while a hand-started server is up.

        Identity is wt's: owner uid and exact argv (no start-time check: a
        sushi llmbench starts has no `.wt` record), so a recycled pid whose
        command line merely mentions sushi is never signalled."""
        self._forget_row()
        addr = self._address()
        expect = self._expect_argv(addr)
        owned = self._pidproc.live_pid(expect) is not None
        gone = self._pidproc.stop_verified(expect)
        if not owned:
            if probe.port_answers(addr.health_url):
                return f"{self._unowned(addr)}; stop it yourself"
            return None
        if not gone:
            return (
                f"{self.spec.binary} (pid {self._pidproc.read_pid()}) did not exit after "
                "SIGTERM and SIGKILL; its pidfile was kept"
            )
        if probe.port_closed_within(addr.health_url, timeout=SIGNAL_STOP_TIMEOUT):
            return None
        return f"{self.spec.binary} still listening on port {addr.port}"

    def _lingering_status(
        self, bin_path: str, *, deadline: float | None = None
    ) -> _Lingering | None:
        """Poll the status command until it reports no instance and return
        None; at the deadline (default: INSTANCE_GONE_TIMEOUT from now)
        return the last outcome that did not say so. Only a failing command
        (non-zero exit, or one that cannot run) is "gone": an answer — ready
        false included — is an instance still loading or shutting down and
        holding its memory, and a status that times out or says nothing
        readable ("unknown") may be one too, so neither ends the wait."""
        if deadline is None:
            deadline = time.monotonic() + INSTANCE_GONE_TIMEOUT
        argv = [bin_path, *self.spec.ready.command]
        while True:
            outcome, value = probe.command_status(argv, path=self.spec.ready.path)
            if outcome == "gone":
                return None
            if time.monotonic() >= deadline:
                return (outcome, value)
            time.sleep(INSTANCE_GONE_POLL)

    def _lingering_warning(self, lingering: _Lingering) -> str:
        outcome, value = lingering
        said = (
            f"ready={probe.as_text(value)}"
            if outcome == "answered"
            else "the status command did not answer"
        )
        return (
            f"{self.spec.binary} still reports an instance after stop ({said}); "
            "it may still be shutting down"
        )
