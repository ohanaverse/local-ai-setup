# MTPLX Code-Review Fixes Round 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix all 10 findings from the `/code-review` run on the `mtplx-provider` branch (2026-09-11, high effort): one deterministic TUI apply crash, one self-contained-bash contract break, six lifecycle robustness/efficiency gaps, and three cleanups (port-constant scatter, missing test comments).

**Architecture:** Each finding is fixed in place with the minimal targeted change. Two small new seams are introduced: a `manages_own_cache` carve-out in `PendingChanges.apply()`'s ready loop (mirroring the screen's `_provider_can_download` rule via `ProviderRegistry.get_class`), and a keep/probe short-circuit in `lifecycle.isolate()` (mirroring the bash providers' `stop_all_local` keep arg).

**Tech Stack:** Python 3.13 (modelman, pytest/ruff/mypy via `uv run`), Bash (`bin/` — validated with `bash -n` + `shellcheck`), Go (wt — `go build`/`go vet` only if touched).

**Spec:** No separate spec doc — this plan's spec is the 10 verified findings already reported to the user in this conversation and re-verified in-session against the code (see each task's **Finding** line).

## Global Constraints

- Work happens in the current worktree (`mtplx-provider` branch) — a linked worktree, never the primary checkout.
- Every Python change must pass `cd modelman && make check` (ruff + mypy) and the focused tests named in each task before commit.
- Every Bash change must pass `make lint-shell` (`bash -n` + `shellcheck --severity=error`) before commit.
- Follow existing docstring/comment density in each file (this repo writes explanatory "why" comments; match it).
- Run focused tests per change, not the full suite; the final task runs `make test-all`.
- Every commit ends with `Co-Authored-By: Claude Code <noreply@anthropic.com>` and references the plan item it completes (this is plan execution, per user workflow rules), e.g. `fix(lifecycle): ... - completes plan item 4`.
- Never create PRs or push without explicit user approval (user CLAUDE.md).
- Tests written in this plan carry what/why docstrings per the user's test-documentation rule — no bare `def test_` additions anywhere.

---

### Task 1: queue.py — route a `manages_own_cache` ready-on through the flag flip

**Finding (crash, most severe):** `modelman/src/modelman/queue.py:397` — an mtplx ready-on is queued into `PendingChanges` (because `screens/models.py:650`'s `_provider_can_download` returns False for `manages_own_cache` providers) while `_run_apply` (`screens/models.py:1052`) still puts a live `MTPLXProvider` in the `providers` dict (mtplx IS registered, `providers/mtplx.py:122`), so the assert `not (provider is not None and target)` fires and aborts the entire apply mid-run — deletes/moves already applied, remaining steps and the final registry/state save lost. mtplx is the first provider that is both Provider-mapped AND flag-only.

**Files:**
- Modify: `modelman/src/modelman/queue.py:389-401` (ready loop head)
- Test: `modelman/tests/test_queue.py` (append near `test_apply_no_longer_downloads_ready_on_entries`, ~line 1851)

**Interfaces:**
- Consumes: `ProviderRegistry.get_class(provider_id)` (same seam `screens/models.py:647` uses); `manages_own_cache` is a class attribute on `Provider` (`providers/base.py:61`, default False; `MTPLXProvider` sets True at `providers/mtplx.py:51`).
- Produces: no signature changes. `PendingChanges.apply()` behavior: ready-on against a `manages_own_cache` provider now flips `state.ready` (like a flag-only provider) instead of asserting; ready-off (and deletes) keep using the real provider (`provider.delete()` → rmtree of the cache dir).

- [ ] **Step 1: Write the failing test**

Append to `modelman/tests/test_queue.py` (it already defines the `_registry_with`, `_entry`, `_make_state` helpers used by neighboring tests):

```python
def test_apply_ready_on_manages_own_cache_provider_flips_flag(tmp_path):
    """A ready-on against a manages_own_cache provider (MTPLX) is a queued
    flag flip, not a download.

    MTPLX caches its own weights via the `mtplx` CLI, so the TUI queues its
    ready-on into PendingChanges — but a Provider class IS present, so the
    providers dict holds a live MTPLXProvider. mtplx is the first provider
    that is both mapped and flag-only; without the carve-out the ready
    loop's DownloadManager assert fires and aborts the whole apply
    mid-run, losing every remaining queued change."""
    reg, reg_path = _registry_with(
        tmp_path,
        _entry(id="mtplx/x", family="f", provider="mtplx", name="Org/x"),
    )
    state_path = tmp_path / "modelman.toml"
    provider = MagicMock()
    # Neither download nor delete may run for a ready-on: the flag flip is
    # the whole operation (the user cached the weights via the mtplx CLI).
    provider.download.side_effect = AssertionError("download() must not be called")
    provider.delete.side_effect = AssertionError("delete() must not be called")

    pending = PendingChanges(
        registry=reg,
        state=_make_state(),
        family="f",
        registry_path=reg_path,
        state_path=state_path,
        providers={"mtplx": provider},
        ready=[("mtplx/x", {"id": "mtplx/x", "provider": "mtplx", "name": "Org/x"}, True)],
    )
    pending.apply()
    provider.download.assert_not_called()
    provider.delete.assert_not_called()
    assert state.get("mtplx/x").ready is True
    assert pending.failures == []
```

(If the exact helper signatures differ — check the file's existing tests first — adapt the fixture construction, not the assertions.)

- [ ] **Step 2: Run it to verify it fails**

```bash
cd modelman && uv run pytest tests/test_queue.py::test_apply_ready_on_manages_own_cache_provider_flips_flag -q
```

Expected: FAIL with `AssertionError: ready-on for 'mtplx/x' must go through DownloadManager` — the crash from the finding, reproduced.

- [ ] **Step 3: Implement**

In `modelman/src/modelman/queue.py`, first ensure the import exists at the top (merge with existing imports — do not duplicate):

```python
from .providers.registry import ProviderRegistry
```

Then replace the ready-loop head (currently lines 389-401):

```python
            label = _label(variant)
            provider_id = variant["provider"]
            provider = self.providers.get(provider_id)
            # Real downloads no longer run inside apply() — the caller
            # (ModelScreen) must route a ready-on against a real provider
            # through DownloadManager.start() instead of queuing it here.
            # A target=True entry reaching this point with a downloadable
            # provider present is a caller bug, not a runtime condition.
            assert not (provider is not None and target), (
                f"ready-on for {model_id!r} must go through DownloadManager, "
                "not PendingChanges.apply()"
            )
            if provider is None:
                # Flag-only provider (native or unmapped): no provider call
                # exists — but ready-off still means "remove the artifact",
```

with:

```python
            label = _label(variant)
            provider_id = variant["provider"]
            provider = self.providers.get(provider_id)
            # A provider that manages its own cache (MTPLX via the mtplx
            # CLI) has no download for apply() to drive — its ready-on is
            # a queued flag flip (the user cached the weights themselves),
            # mirroring the screen's _provider_can_download rule. mtplx is
            # the first provider that is BOTH Provider-mapped and
            # flag-only: without this carve-out the assert below fires and
            # aborts the whole apply. Class-level lookup via
            # ProviderRegistry, not instance getattr — test mocks
            # auto-create any attribute touched on an instance, which
            # would misroute every mocked provider.
            provider_cls = ProviderRegistry.get_class(provider_id)
            manages_own_cache = bool(
                provider_cls is not None and provider_cls.manages_own_cache
            )
            # Real downloads no longer run inside apply() — the caller
            # (ModelScreen) must route a ready-on against a real provider
            # through DownloadManager.start() instead of queuing it here.
            # A target=True entry reaching this point with a downloadable
            # provider present is a caller bug, not a runtime condition.
            assert not (provider is not None and not manages_own_cache and target), (
                f"ready-on for {model_id!r} must go through DownloadManager, "
                "not PendingChanges.apply()"
            )
            if provider is None or (manages_own_cache and target):
                # Flag-only flip (native/unmapped provider, or a
                # manages_own_cache ready-on): no provider call exists for
                # the ready-on itself — but a provider-is-None ready-off
                # still means "remove the artifact",
```

The rest of the flag-only branch (the inner `if not target:` / `else:` at current lines 407-436) needs no change: a `manages_own_cache` entry only enters this branch with `target=True`, so it takes the `else: state.set(..., ready=target)` flag flip; ready-off keeps taking the mapped `else:` branch below, which clears the artifact via `provider.delete()` (rmtree of the mtplx cache dir — correct) and cascades the unexpose through the config writer (mtplx IS LiteLLM-mappable, and the cascade at current line 473 checks `provider is None`, so a mapped mtplx ready-off still appends to `self.exposes` — correct).

- [ ] **Step 4: Run the tests**

```bash
cd modelman && uv run pytest tests/test_queue.py -q && make check
```

Expected: all pass, including the pre-existing `test_apply_no_longer_downloads_ready_on_entries` (its `MagicMock` provider with provider_id "ollama" resolves `get_class("ollama").manages_own_cache == False`, so the assert still fires for it) and the two `wrong-id` assert tests.

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/queue.py modelman/tests/test_queue.py
git commit -m "fix(queue): route manages_own_cache ready-ons through the flag flip, not the DownloadManager assert - completes plan item 1

mtplx is the first provider that is both Provider-mapped and flag-only:
the screen queues its ready-on (manages_own_cache makes
_provider_can_download False) while _run_apply still puts a live
MTPLXProvider in the providers dict, so the ready loop's assert aborted
the entire apply mid-run. Mirrors the screen's classification via
ProviderRegistry.get_class.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 2: bash shim — make the mtplx leg self-contained again

**Finding:** `bin/llm-isolate-provider:262` — the new mtplx leg breaks the helper's self-contained-bash contract: (a) it requires the ambient `python3` to import modelman, and (b) `lifecycle._stop_others` re-resolves the helper via `shutil.which` — but the benchmark scripts invoke the helper by absolute path (`benchmarks/qwen3.8-benchmark:68`: `ISOLATE_HELPER="$(cd "$(dirname "$0")/../bin" && pwd)/llm-isolate-provider"`) with no PATH guarantee, so `shutil.which` returns None → ok:false envelope → `set -e` kills the benchmark with all providers stopped. Worse (verified live): this machine's pyenv `.pth` makes ambient `python3` import the MAIN checkout's modelman, so running the worktree's shim silently executes main-branch lifecycle code.

**Files:**
- Modify: `bin/llm-isolate-provider:250-276` (mtplx case)
- Modify: `modelman/src/modelman/providers/lifecycle.py:54-64` (`_stop_others`)
- Test: `modelman/tests/test_lifecycle.py` (append)

**Interfaces:**
- Consumes: `modelman/.venv/bin/python` (created by `make install`/`make dev`; lifecycle's import chain pulls third-party `tomli_w` via `registry.py` → `_toml_io`, so ambient python3 + PYTHONPATH alone is not enough).
- Produces: env var `LLM_ISOLATE_HELPER` — the shim's own absolute path, exported before invoking the lifecycle module; `_stop_others` reads it with a `shutil.which` fallback for non-shim callers (`modelman start`).

- [ ] **Step 1: Write the failing test**

Append to `modelman/tests/test_lifecycle.py` (it already patches lifecycle internals with `unittest.mock.patch` / `monkeypatch`):

```python
def test_stop_others_prefers_llm_isolate_helper_env(monkeypatch):
    """_stop_others must resolve the isolation helper via the
    LLM_ISOLATE_HELPER env var before PATH.

    The bash shim is routinely invoked by absolute path (the benchmark
    scripts resolve it via `dirname $0`) with bin/ NOT on PATH, so
    shutil.which() returns None there — without the env var, the mtplx
    isolate fails after the helper already stopped every other provider.
    The shim exports its own path so resolution never depends on PATH."""
    import subprocess as sp

    calls = []
    monkeypatch.setenv("LLM_ISOLATE_HELPER", "/abs/llm-isolate-provider")
    monkeypatch.setattr(
        "modelman.providers.lifecycle.shutil.which", lambda name: None
    )
    monkeypatch.setattr(
        "modelman.providers.lifecycle.subprocess.run",
        lambda cmd, **kw: calls.append(cmd),
    )
    from modelman.providers.lifecycle import _stop_others

    _stop_others()
    assert calls == [["/abs/llm-isolate-provider", "stop-all"]]


def test_stop_others_without_env_or_path_raises(monkeypatch):
    """No env var AND no PATH entry must surface a clean LifecycleError
    (envelope contract), never a None-subscript crash."""
    monkeypatch.delenv("LLM_ISOLATE_HELPER", raising=False)
    monkeypatch.setattr(
        "modelman.providers.lifecycle.shutil.which", lambda name: None
    )
    from modelman.providers.lifecycle import LifecycleError, _stop_others

    with pytest.raises(LifecycleError, match="not found"):
        _stop_others()
```

- [ ] **Step 2: Run it to verify it fails**

```bash
cd modelman && uv run pytest tests/test_lifecycle.py::test_stop_others_prefers_llm_isolate_helper_env -q
```

Expected: FAIL — current `_stop_others` ignores the env var and raises LifecycleError because `shutil.which` returns None.

- [ ] **Step 3: Implement the Python side**

Replace `_stop_others` in `modelman/src/modelman/providers/lifecycle.py` (current lines 54-64):

```python
def _stop_others() -> None:
    """Stop every local provider except the one about to start, by
    delegating to the bash helper's stop-all mode (transition period).

    The helper's own absolute path (exported by the bash shim as
    LLM_ISOLATE_HELPER) wins over PATH: the shim is routinely invoked by
    absolute path with bin/ absent from PATH, where shutil.which() would
    return None and fail an isolate that already stopped everything."""
    helper = os.environ.get("LLM_ISOLATE_HELPER") or shutil.which("llm-isolate-provider")
    if helper is None:
        raise LifecycleError("isolation helper 'llm-isolate-provider' not found on PATH")
    result = subprocess.run([helper, "stop-all"], capture_output=True, text=True, check=False)
    if result.returncode != 0:
        raise LifecycleError(
            f"failed to stop other providers: {result.stderr.strip() or result.stdout.strip()}"
        )
```

- [ ] **Step 4: Implement the bash side**

Replace the `mtplx)` case in `bin/llm-isolate-provider` (current lines 250-276):

```bash
    mtplx)
        # MTPLX start/stop/warmup lives in the Python lifecycle module; the
        # shim delegates entirely and passes the module's JSON through.
        # Forward an explicit model arg so callers (modelman start, benchmark
        # sweeps) can select a specific mtplx model instead of defaulting to
        # the registry's single mtplx entry.
        #
        # Self-contained like every other branch of this script: the
        # benchmark scripts resolve this helper by absolute path with no
        # bin/ on PATH, so nothing here may depend on ambient PATH or on
        # ambient python3 being able to import modelman — a pyenv .pth can
        # make the ambient interpreter import a DIFFERENT checkout's
        # modelman (e.g. the main repo while a worktree's script runs).
        # Prefer the repo venv's interpreter (the lifecycle import chain
        # pulls third-party tomli_w, which ambient python3 usually lacks),
        # pinned relative to this script's own location; fall back to
        # ambient python3 with PYTHONPATH pointing at this repo's src.
        MTPLX_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
        MTPLX_PY="$MTPLX_ROOT/modelman/.venv/bin/python"
        if [ ! -x "$MTPLX_PY" ]; then
            MTPLX_PY=""
            if ! PYTHONPATH="$MTPLX_ROOT/modelman/src${PYTHONPATH:+:$PYTHONPATH}" \
                python3 -c 'import modelman.providers.lifecycle' 2>/dev/null; then
                echo "modelman is not importable and $MTPLX_ROOT/modelman/.venv is missing." >&2
                echo "Run 'make install' (creates modelman/.venv), or install the" >&2
                echo "lifecycle deps into the ambient python3." >&2
                exit 1
            fi
        fi
        # Thread this script's own absolute path to the lifecycle module:
        # its _stop_others must not re-resolve the helper via PATH (see
        # above — this shim is routinely invoked with bin/ off PATH).
        export LLM_ISOLATE_HELPER="$MTPLX_ROOT/bin/llm-isolate-provider"
        if [ -n "$MTPLX_PY" ]; then
            "$MTPLX_PY" -m modelman.providers.lifecycle isolate mtplx ${2:+"$2"}
        else
            PYTHONPATH="$MTPLX_ROOT/modelman/src${PYTHONPATH:+:$PYTHONPATH}" \
                python3 -m modelman.providers.lifecycle isolate mtplx ${2:+"$2"}
        fi
        exit $?
        ;;
```

Also update the usage comment at the top of the file (line 4) to note the mtplx arg semantics stay as they are (no change needed to the wording there unless it mentions ambient python3 — check and fix if stale).

- [ ] **Step 5: Run tests and lint**

```bash
cd modelman && uv run pytest tests/test_lifecycle.py -q && make check
cd .. && make lint-shell
```

Expected: all pass. shellcheck may flag nothing; if it flags the `export` inside a `case`, keep the export where it is (it must be set before the python invocation, and only this branch needs it).

- [ ] **Step 6: Commit**

```bash
git add bin/llm-isolate-provider modelman/src/modelman/providers/lifecycle.py modelman/tests/test_lifecycle.py
git commit -m "fix(mtplx): make the shim's mtplx leg self-contained again - completes plan item 2

Resolve the venv interpreter and the helper's own path from the script's
location (benchmarks invoke the helper by absolute path, off PATH, and
the ambient python3 on some machines imports a different checkout's
modelman via pyenv .pth). _stop_others now prefers LLM_ISOLATE_HELPER.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 3: isolate() — teardown on failure + envelope contract for non-Lifecycle exceptions

**Finding (two related):** `lifecycle.py:235` — if `_start_mtplx_serve` succeeds but `_wait_for_model`/`_warmup` raises, the except returns ok=False without stopping the spawned `mtplx serve`: an orphan holds port 8003 and GPU/RAM, invisible to wt's localgate. And `lifecycle.py:234/285` — `_resolve_mtplx_model` calls `load_registry()` which raises `RegistryError`/`TOMLDecodeError` (not `LifecycleError`), so a missing/corrupt registry produces a raw traceback and no JSON envelope, breaking the CLI contract the bash shim and `benchmark/isolation.py` parse.

**Files:**
- Modify: `modelman/src/modelman/providers/lifecycle.py:223-236` (`isolate`) and `285-304` (`_main`)
- Test: `modelman/tests/test_lifecycle.py` (append)

**Interfaces:**
- Consumes: `_stop_mtplx()` (already returns a `LifecycleResult`, never raises for missing binary; treat as best-effort anyway).
- Produces: `isolate()` never raises — every failure returns an ok=False `LifecycleResult` (callers: `_main`, tests). `_main` never prints a traceback on stdout/stderr for command dispatch — always one JSON envelope on stdout.

- [ ] **Step 1: Write the failing tests**

Append to `modelman/tests/test_lifecycle.py`:

```python
def test_isolate_stops_serve_when_model_wait_fails():
    """A half-started isolate (serve up, model load/warmup failed) must
    stop the spawned mtplx serve.

    Otherwise the orphan holds port 8003 and GPU/RAM while local_control
    clears the [local].running_model marker on failure, so nothing tears
    it down — violating the one-local-model-at-a-time invariant this
    module exists to enforce."""
    with (
        patch("modelman.providers.lifecycle._stop_others"),
        patch("modelman.providers.lifecycle._start_mtplx_serve"),
        patch(
            "modelman.providers.lifecycle._wait_for_model",
            side_effect=LifecycleError("timed out"),
        ),
        patch("modelman.providers.lifecycle._warmup"),
        patch("modelman.providers.lifecycle._stop_mtplx") as mock_cleanup,
    ):
        result = isolate("mtplx", "Some/Model")
    mock_cleanup.assert_called_once()
    assert result.ok is False
    assert "timed out" in (result.error or "")


def test_isolate_registry_error_returns_envelope_not_traceback():
    """A corrupt registry.toml (RegistryError, not LifecycleError) must
    surface as an ok=False envelope, never a raw traceback — the bash
    shim and benchmark isolation parse stdout as JSON."""
    from modelman.registry import RegistryError

    with (
        patch("modelman.providers.lifecycle._stop_others") as mock_stop,
        patch(
            "modelman.providers.lifecycle.load_registry",
            side_effect=RegistryError("corrupt"),
        ),
        patch("modelman.providers.lifecycle._stop_mtplx") as mock_cleanup,
    ):
        result = isolate("mtplx")
    assert result.ok is False
    assert "corrupt" in (result.error or "")
    # The failure happened before serve started: nothing to tear down,
    # and stop-others must not have run either (fail before any teardown).
    mock_stop.assert_not_called()
    mock_cleanup.assert_not_called()


def test_cli_registry_error_prints_json_envelope(capsys):
    """The CLI entry point must answer a corrupt registry with a JSON
    envelope on stdout and exit 1 — this is the bash-shim stdout contract
    that modelman/benchmark/isolation.py parses (invalid JSON there
    surfaces as the non-actionable 'isolation helper returned invalid
    JSON')."""
    from modelman.providers.lifecycle import _main
    from modelman.registry import RegistryError

    with patch(
        "modelman.providers.lifecycle.load_registry",
        side_effect=RegistryError("corrupt"),
    ):
        code = _main(["isolate", "mtplx"])
    import json

    out = json.loads(capsys.readouterr().out)
    assert code == 1
    assert out["ok"] is False
    assert "corrupt" in out["error"]
```

(If `RegistryError`'s constructor/import site differs — check `modelman/src/modelman/registry.py:426` — adjust the import, not the assertions.)

- [ ] **Step 2: Run them to verify they fail**

```bash
cd modelman && uv run pytest tests/test_lifecycle.py::test_isolate_stops_serve_when_model_wait_fails tests/test_lifecycle.py::test_isolate_registry_error_returns_envelope_not_traceback tests/test_lifecycle.py::test_cli_registry_error_prints_json_envelope -q
```

Expected: FAIL — first test: `mock_cleanup` not called (no teardown today). Second/third: `RegistryError` propagates (isolate catches only `LifecycleError`), so the test errors rather than returning a result.

- [ ] **Step 3: Implement**

Replace `isolate()` (current lines 223-236):

```python
def isolate(provider_id: str, model: str | None = None, *, extra_args: tuple[str, ...] = ()) -> LifecycleResult:
    """Stop every other local provider and start the requested one."""
    if provider_id != "mtplx":
        # Transition: delegate non-mtplx providers to the bash helper.
        return _delegate_isolate(provider_id, model, extra_args)
    started = False
    try:
        resolved = _resolve_mtplx_model(model)
        _stop_others()
        _start_mtplx_serve(resolved)
        started = True
        _wait_for_model(resolved)
        _warmup(resolved)
    except Exception as exc:  # noqa: BLE001 — envelope contract, never a traceback
        # RegistryError/TOMLDecodeError from load_registry() are NOT
        # LifecycleError; anything escaping here must still return the
        # JSON envelope the bash shim and benchmark isolation parse.
        if started:
            # Teardown: serve is up but load/warmup failed — a running
            # orphan holding port 8003 and GPU/RAM breaks the one-local-
            # model invariant this module exists to enforce. Best-effort:
            # a cleanup failure must not mask the original error.
            try:
                _stop_mtplx()
            except Exception:  # noqa: BLE001
                pass
        return LifecycleResult("mtplx", model or "", MTPLX_DIRECT_URL, False, str(exc))
    return LifecycleResult("mtplx", resolved, MTPLX_DIRECT_URL, True, None)
```

And give `_main` a dispatch-level catch-all (current lines 285-304) — wrap the command dispatch so any unexpected exception from a path other than isolate (stop/stop-all) still yields one envelope:

```python
def _main(argv: list[str]) -> int:
    if not argv:
        print("usage: lifecycle <isolate|stop|stop-all> [provider] [model] ...", file=sys.stderr)
        return 1
    cmd = argv[0]
    try:
        if cmd == "isolate":
            provider = argv[1] if len(argv) > 1 else ""
            model = argv[2] if len(argv) > 2 else None
            extra_args = tuple(argv[3:])
            result = isolate(provider, model, extra_args=extra_args)
        elif cmd == "stop":
            provider = argv[1] if len(argv) > 1 else ""
            result = stop(provider)
        elif cmd == "stop-all":
            result = stop_all()
        else:
            print(f"unknown command: {cmd}", file=sys.stderr)
            return 1
    except Exception as exc:  # noqa: BLE001 — CLI contract: one JSON envelope or nothing
        result = LifecycleResult(cmd, "", "", False, f"{type(exc).__name__}: {exc}")
    print(json.dumps(asdict(result)))
    return 0 if result.ok else 1
```

- [ ] **Step 4: Run tests**

```bash
cd modelman && uv run pytest tests/test_lifecycle.py -q && make check
```

Expected: all pass, including the pre-existing `test_isolate_mtplx_*` tests (their patches make `started=True` succeed and return ok — the new except path is not taken).

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/providers/lifecycle.py modelman/tests/test_lifecycle.py
git commit -m "fix(lifecycle): stop orphaned mtplx serve on failure; keep the JSON envelope contract for non-Lifecycle errors - completes plan item 3

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 4: `_wait_for_port_closed` — a read timeout is "still open", not "closed"

**Finding:** `lifecycle.py:84` — the `except OSError: return` treats a read `TimeoutError` (an OSError subclass on this repo's Python 3.13) from a listening-but-unresponsive process as "port closed", contradicting the docstring. A hung/draining mtplx still holding port 8003 mid-stop-grace would get a new `mtplx serve` spawned into the busy port, dying with the misleading "exited immediately (address already in use)" error.

**Files:**
- Modify: `modelman/src/modelman/providers/lifecycle.py:67-87` (`_wait_for_port_closed`)
- Test: `modelman/tests/test_lifecycle.py` (append)

**Interfaces:**
- Produces: unchanged signature/semantics for existing callers (`_start_mtplx_serve`); only the OSError taxonomy inside changes.

- [ ] **Step 1: Write the failing test**

Append to `modelman/tests/test_lifecycle.py` (it already has `_mock_urlopen` and the `test_wait_for_port_closed_*` tests to mirror — see lines ~250-282):

```python
def test_wait_for_port_closed_read_timeout_means_still_open():
    """A read TimeoutError means the listener accepted the connection and
    then stalled (hung server / still draining under the 10s stop grace)
    — the port is still held. Declaring it closed spawns a new mtplx
    serve into the busy port, which dies as the misleading 'exited
    immediately (address already in use)' instead of the intended 'port
    still held' error."""
    from modelman.providers.lifecycle import _wait_for_port_closed

    with patch(
        "modelman.providers.lifecycle.urllib.request.urlopen",
        side_effect=TimeoutError,
    ):
        with pytest.raises(LifecycleError, match="still answering"):
            _wait_for_port_closed("http://localhost:8003/v1/models", timeout=0.3)
```

- [ ] **Step 2: Run it to verify it fails**

```bash
cd modelman && uv run pytest tests/test_lifecycle.py::test_wait_for_port_closed_read_timeout_means_still_open -q
```

Expected: FAIL — the current `except OSError: return` swallows the TimeoutError and returns (test expects a raise).

- [ ] **Step 3: Implement**

Replace `_wait_for_port_closed` (current lines 67-87):

```python
def _wait_for_port_closed(url: str, timeout: float = 10.0) -> None:
    """Poll a localhost URL until it stops responding, or raise on timeout.

    A response — success or HTTP error status — means something is still
    listening. `HTTPError` is a `URLError`/`OSError` subclass, so it must
    be caught before the `URLError` catch below, or a server merely
    answering with a non-2xx status (e.g. 404 for a not-yet-ready path)
    would be misread as the port having closed. A read `TimeoutError` is
    also NOT a closed port: the listener accepted the connection and then
    stalled (hung, or draining under load) — exactly the "still holds the
    port" case this poll exists to catch. Only a connection-level failure
    (refused, reset, no route) means the port actually closed.
    """
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=1.0) as resp:  # noqa: S310 — localhost probe
                resp.read()
        except urllib.error.HTTPError:
            pass  # server answered (with an error status) — still open
        except urllib.error.URLError as exc:
            # urlopen wraps connection-level failures in URLError. A
            # connect timeout (reason is a TimeoutError) is ambiguous —
            # treat it as still open rather than risk spawning into a
            # held port.
            if isinstance(exc.reason, TimeoutError):
                pass
            else:
                return  # refused / reset / no route — port closed
        except TimeoutError:
            pass  # accepted the connection, then stalled mid-read — still open
        except ConnectionError:
            return  # reset at read time — the listener is dying or gone
        time.sleep(0.2)
    raise LifecycleError(f"port still answering at {url} after {timeout}s")
```

- [ ] **Step 4: Run tests**

```bash
cd modelman && uv run pytest tests/test_lifecycle.py -q && make check
```

Expected: all pass, including the pre-existing `test_wait_for_port_closed_returns_on_connection_refused` (a `URLError` wrapping `ConnectionRefusedError` now returns via the new `URLError` arm) and `test_wait_for_port_closed_treats_http_error_as_still_open`. If the refused test constructs the error differently (e.g. a bare `ConnectionRefusedError` or `URLError` with a different reason), adapt the test only if its documented intent (refused ⇒ closed) still holds.

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/providers/lifecycle.py modelman/tests/test_lifecycle.py
git commit -m "fix(lifecycle): treat read timeouts as port-still-open in _wait_for_port_closed - completes plan item 4

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 5: `_wait_for_model` — detect a serve process dying mid-load

**Finding:** `lifecycle.py:153` — `_wait_for_model` polls only the HTTP endpoint; the `Popen` handle is discarded (only a 0.2s early-exit check inside `_start_mtplx_serve`). A serve process that crashes 30s into loading a 20GB model (OOM kill, late-missing weights) is polled for the full 300s deadline with a generic timeout, while the real crash cause sits unread in the log.

**Files:**
- Modify: `modelman/src/modelman/providers/lifecycle.py` (`_start_mtplx_serve` returns `proc`; new `_log_tail()`; `_wait_for_model` signature)
- Test: `modelman/tests/test_lifecycle.py` (append; update affected pre-existing tests)

**Interfaces:**
- Produces: `_start_mtplx_serve(model: str) -> subprocess.Popen`; `_log_tail() -> str` (last ~512 chars of `MTPLX_LOG`, "" on read failure); `_wait_for_model(model: str, proc: subprocess.Popen, timeout: float = 300.0) -> None`. `isolate()` (Task 3's shape) passes the returned `proc` through.

- [ ] **Step 1: Write the failing test**

Append to `modelman/tests/test_lifecycle.py`:

```python
def test_wait_for_model_raises_promptly_when_serve_dies():
    """A serve process that dies mid-load (OOM kill, missing weights
    found late) must fail the wait immediately with the log tail, not
    poll a dead port for the full 300s deadline — the real crash cause
    would otherwise sit unread in the mtplx log."""
    from modelman.providers.lifecycle import LifecycleError, _wait_for_model

    proc = MagicMock()
    proc.poll.return_value = 137  # SIGKILL'd (OOM) on first check
    proc.returncode = 137
    with patch(
        "modelman.providers.lifecycle._http_models_ids",
        side_effect=AssertionError("must not poll HTTP after process death"),
    ):
        with pytest.raises(LifecycleError, match="exited during model load"):
            _wait_for_model("Org/Model", proc, timeout=300.0)
```

- [ ] **Step 2: Run it to verify it fails**

```bash
cd modelman && uv run pytest tests/test_lifecycle.py::test_wait_for_model_raises_promptly_when_serve_dies -q
```

Expected: FAIL with a TypeError — `_wait_for_model` today takes no `proc` argument.

- [ ] **Step 3: Implement**

In `modelman/src/modelman/providers/lifecycle.py`:

(a) Add `_log_tail` after the constants block:

```python
def _log_tail(max_bytes: int = 1024) -> str:
    """The last ~512 chars of the mtplx serve log — the crash cause for
    'serve died' error messages (empty string when unreadable)."""
    try:
        with open(MTPLX_LOG, "rb") as f:
            f.seek(0, 2)
            size = f.tell()
            f.seek(max(0, size - max_bytes))
            return f.read().decode(errors="replace")[-512:]
    except OSError:
        return ""
```

(b) In `_start_mtplx_serve`: change the signature to return the process, replace the inline log-tail block (current lines 137-150) with `_log_tail()`, and `return proc` at the end:

```python
def _start_mtplx_serve(model: str) -> subprocess.Popen:
    """Start `mtplx serve` in the background, tracked by a pidfile, and
    return the Popen handle so the caller can watch for process death
    during the model-load poll.

    Assumes the caller (isolate()) has already stopped any prior mtplx
    instance via _stop_others() — stopping it again here would pay a second
    full `mtplx stop --grace-seconds 10` for no benefit, since _stop_others()
    already tore it down as part of the same isolate call."""
    ...  # (unchanged through the Popen + pidfile write)
    time.sleep(0.2)
    if proc.poll() is not None:
        log_tail = _log_tail()
        raise LifecycleError(
            f"mtplx serve exited immediately (exit {proc.poll()})"
            + (f"; log tail: {log_tail}" if log_tail else "")
        )
    return proc
```

(c) Replace `_wait_for_model` (current lines 153-161):

```python
def _wait_for_model(model: str, proc: subprocess.Popen, timeout: float = 300.0) -> None:
    """Poll /v1/models until it lists `model`, the serve process dies, or
    the deadline passes."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if proc.poll() is not None:
            # The serve process died mid-load (OOM kill, missing weights
            # discovered late): without this check the HTTP poll below
            # runs the full 300s against a dead port while the real
            # crash cause stays buried in the log.
            log_tail = _log_tail()
            raise LifecycleError(
                f"mtplx serve exited during model load (exit {proc.returncode})"
                + (f"; log tail: {log_tail}" if log_tail else "")
            )
        ids = _http_models_ids(f"{MTPLX_BASE}/v1/models")
        if any(served == model or served.endswith("/" + model) for served in ids):
            return
        time.sleep(1.0)
    raise LifecycleError(f"timed out waiting for mtplx to serve {model}")
```

(d) In `isolate()`, capture and forward the handle (Task 3's shape, one line each):

```python
        proc = _start_mtplx_serve(resolved)
        started = True
        _wait_for_model(resolved, proc)
```

(e) Sweep the pre-existing tests that call these directly or assert on them: `test_start_mtplx_serve_pins_model_id`, `test_start_mtplx_serve_does_not_stop_mtplx_again`, `test_start_mtplx_serve_raises_when_process_exits_immediately` (their Popen/poll mocks may need `return_value`/`returncode` tweaks — keep their documented intent), and `test_isolate_mtplx_starts_serve_and_warmup` (its `_wait_for_model` patch absorbs the new argument automatically; the `_start_mtplx_serve` patch must now `return_value=MagicMock()` — configure it).

- [ ] **Step 4: Run tests**

```bash
cd modelman && uv run pytest tests/test_lifecycle.py -q && make check
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/providers/lifecycle.py modelman/tests/test_lifecycle.py
git commit -m "fix(lifecycle): detect a serve process dying mid-load instead of polling 300s - completes plan item 5

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 6: keep semantics — a same-model mtplx re-isolate keeps the loaded weights

**Finding:** `lifecycle.py:230` — `isolate('mtplx')` always runs the bash stop-all with no keep arg, so re-isolating the SAME mtplx model pays a full stop + respawn + multi-minute reload + warmup, where the bash providers' `stop_all_local $provider` skips the target and pays nothing (e.g. back-to-back mtplx rows in a benchmark sweep).

**Files:**
- Modify: `bin/llm-isolate-provider:202-213` (stop-all case) and the usage comment (line 4)
- Modify: `modelman/src/modelman/providers/lifecycle.py` (`_stop_others(keep)`, new `_serving_model()`, `isolate()` probe branch)
- Test: `modelman/tests/test_lifecycle.py` (append)

**Interfaces:**
- Consumes: `stop_all_local` (bash) — its `keep` arg is compared `!=` per provider, so `stop_all_local mtplx` already skips the mtplx stop branch; `http_models_ids` returns `[]` on any error (safe probe — a down server reads as "not serving").
- Produces: bash CLI `llm-isolate-provider stop-all [keep]` (keep optional, default empty = stop everything); `_stop_others(keep: str = "")`; `_serving_model(resolved: str) -> bool`.

- [ ] **Step 1: Write the failing tests**

Append to `modelman/tests/test_lifecycle.py`:

```python
def test_isolate_mtplx_same_model_keeps_loaded():
    """Re-isolating the model mtplx is already serving must not pay a
    full stop + reload: the bash providers keep an already-loaded target
    (stop_all_local's keep arg), and the mtplx path now does the same by
    name-checking /v1/models before tearing down — mtplx is
    single-model-per-process, so 'same provider' is only keepable when
    it is the same model."""
    with (
        patch(
            "modelman.providers.lifecycle._serving_model", return_value=True
        ) as mock_probe,
        patch("modelman.providers.lifecycle._stop_others") as mock_stop,
        patch("modelman.providers.lifecycle._start_mtplx_serve") as mock_start,
        patch("modelman.providers.lifecycle._wait_for_model"),
        patch("modelman.providers.lifecycle._warmup") as mock_warmup,
    ):
        result = isolate("mtplx", "Some/Model")
    mock_probe.assert_called_once_with("Some/Model")
    mock_stop.assert_called_once_with(keep="mtplx")
    mock_start.assert_not_called()
    mock_warmup.assert_called_once_with("Some/Model")
    assert result.ok is True


def test_isolate_mtplx_different_model_restarts():
    """A different model (or no server) must take the full restart path:
    mtplx is single-model-per-process, so serving model B means a stop +
    respawn, and _stop_others must tear mtplx down too (no keep)."""
    with (
        patch(
            "modelman.providers.lifecycle._serving_model", return_value=False
        ),
        patch("modelman.providers.lifecycle._stop_others") as mock_stop,
        patch("modelman.providers.lifecycle._start_mtplx_serve") as mock_start,
        patch("modelman.providers.lifecycle._wait_for_model"),
        patch("modelman.providers.lifecycle._warmup"),
    ):
        result = isolate("mtplx", "Some/Model")
    mock_stop.assert_called_once_with()
    mock_start.assert_called_once_with("Some/Model")
    assert result.ok is True
```

- [ ] **Step 2: Run them to verify they fail**

```bash
cd modelman && uv run pytest tests/test_lifecycle.py::test_isolate_mtplx_same_model_keeps_loaded -q
```

Expected: FAIL — `_serving_model` does not exist (AttributeError on the patch target), and today `isolate` calls `_stop_others()` with no kwargs and always starts serve.

- [ ] **Step 3: Implement the bash side**

In `bin/llm-isolate-provider`, update the usage comment (line 4) to:

```bash
#        llm-isolate-provider stop-all [keep]  (stop every local provider, or
#        keep one provider's models loaded; modelman stop passes no keep)
```

and the `stop-all)` case (current lines 202-213):

```bash
    stop-all)
        # Tear down every local provider, optionally keeping one
        # provider's models loaded — `modelman stop`'s delegate (no
        # keep), and the Python lifecycle's mtplx path (keep=mtplx when
        # the already-running instance serves the target model).
        # stop_all_local's "keep" arg is compared with `!=` against each
        # provider name, so an empty string never matches any of them
        # and every branch runs.
        stop_all_local "${2:-}"
```

- [ ] **Step 4: Implement the Python side**

In `lifecycle.py`, extend `_stop_others` (Task 2's shape) and add the probe:

```python
def _stop_others(keep: str = "") -> None:
    """Stop every local provider except the one about to start, by
    delegating to the bash helper's stop-all mode (transition period).
    `keep` names one provider whose models stay loaded — mirroring the
    per-provider branches' stop_all_local keep arg (the helper's own
    helper path resolution note from Task 2 stays as written)."""
    helper = os.environ.get("LLM_ISOLATE_HELPER") or shutil.which("llm-isolate-provider")
    if helper is None:
        raise LifecycleError("isolation helper 'llm-isolate-provider' not found on PATH")
    cmd = [helper, "stop-all"] + ([keep] if keep else [])
    result = subprocess.run(cmd, capture_output=True, text=True, check=False)
    if result.returncode != 0:
        raise LifecycleError(
            f"failed to stop other providers: {result.stderr.strip() or result.stdout.strip()}"
        )


def _serving_model(resolved: str) -> bool:
    """True when the mtplx instance on MTPLX_PORT already lists
    `resolved` (same lenient match as _wait_for_model): a same-model
    re-isolate can keep the loaded weights instead of paying a full
    stop + reload. http_models_ids returns [] on any error, so a down
    or wedged server reads as "not serving" — the safe fallback."""
    ids = _http_models_ids(f"{MTPLX_BASE}/v1/models")
    return any(served == resolved or served.endswith("/" + resolved) for served in ids)
```

and restructure `isolate()`'s try body (Task 3 + Task 5 shape):

```python
    started = False
    try:
        resolved = _resolve_mtplx_model(model)
        if _serving_model(resolved):
            # Keep semantics, matching the bash providers (stop_all_local
            # $provider skips the target): re-isolating the model mtplx
            # is already serving pays only warmup, not a full stop +
            # respawn + multi-minute reload. mtplx is
            # single-model-per-process, so only the SAME model is
            # keepable — a different model takes the full restart below.
            _stop_others(keep="mtplx")
            _warmup(resolved)
        else:
            _stop_others()
            proc = _start_mtplx_serve(resolved)
            started = True
            _wait_for_model(resolved, proc)
            _warmup(resolved)
    except Exception as exc:  # noqa: BLE001 — envelope contract, never a traceback
        ...  # (Task 3's except body stays as written)
```

Note: the keep-path `_warmup` doubles as the health check — a wedged server fails warmup, and Task 3's except path then tears it down.

- [ ] **Step 5: Run tests and lint**

```bash
cd modelman && uv run pytest tests/test_lifecycle.py -q && make check
cd .. && make lint-shell
```

Expected: all pass, including Task 2's `_stop_others` tests (their `calls == [["/abs/llm-isolate-provider", "stop-all"]]` assertion is the default-`keep=""` path — unchanged).

- [ ] **Step 6: Commit**

```bash
git add bin/llm-isolate-provider modelman/src/modelman/providers/lifecycle.py modelman/tests/test_lifecycle.py
git commit -m "fix(lifecycle): keep an already-serving mtplx model loaded on re-isolate - completes plan item 6

stop-all gains an optional keep arg (mirroring stop_all_local), and
isolate() probes /v1/models for the exact model before tearing down.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 7: refuse ambiguous no-model mtplx resolution

**Finding:** `lifecycle.py:42` — `_resolve_mtplx_model` silently falls back to the FIRST mtplx model in the registry (docstring claims "the single") when no explicit model is given. A caller that fails to forward the model name serves — and benchmarks — the wrong weights with no error. (This branch already had to patch four such forwarding gaps: `benchmark/runner.py:173`, `agent/runner.py:521`, `agent/suite.py:100`, `local_control.py:196` — a fifth would today be silent.)

**Files:**
- Modify: `modelman/src/modelman/providers/lifecycle.py:42-51` (`_resolve_mtplx_model`)
- Modify: `bin/llm-isolate-provider:7-9` (usage comment: "else the first mtplx model in the registry" → the new refusal semantics)
- Test: `modelman/tests/test_lifecycle.py` (append)

**Interfaces:**
- Produces: unchanged signature/return. New failure mode: no explicit model + >1 registry mtplx entry → LifecycleError "model required: registry holds N mtplx models (...)" → ok=False envelope.

- [ ] **Step 1: Write the failing test**

Append to `modelman/tests/test_lifecycle.py`:

```python
def test_isolate_mtplx_ambiguous_registry_refuses_instead_of_guessing():
    """With no explicit model and more than one mtplx entry, isolate()
    must refuse, not silently serve the first registry match.

    This branch already had to patch four callers that failed to
    forward the model name; without the refusal a fifth such gap would
    serve (and benchmark) the wrong weights with ok=true — the
    silent-wrong-weights failure mode."""
    from modelman.registry import ModelEntry, Registry

    two = Registry(
        providers=[],
        models=[
            ModelEntry(
                id=f"mtplx/Org/m{n}",
                family="qwen3.8",
                provider_id="mtplx",
                model_name=f"Org/m{n}",
            )
            for n in (1, 2)
        ],
    )
    with (
        patch("modelman.providers.lifecycle._stop_others") as mock_stop,
        patch("modelman.providers.lifecycle.load_registry", return_value=two),
    ):
        result = isolate("mtplx")
    assert result.ok is False
    assert "model required" in (result.error or "")
    mock_stop.assert_not_called()  # refuse before any teardown
```

- [ ] **Step 2: Run it to verify it fails**

```bash
cd modelman && uv run pytest tests/test_lifecycle.py::test_isolate_mtplx_ambiguous_registry_refuses_instead_of_guessing -q
```

Expected: FAIL — today the resolve picks the first model and the isolate proceeds (result.ok ends up True, or a later mocked step fails).

- [ ] **Step 3: Implement**

Replace `_resolve_mtplx_model` (current lines 42-51):

```python
def _resolve_mtplx_model(model: str | None) -> str:
    """The MTPLX repo id to serve: the explicit `model`, else the single
    mtplx model in the registry. With no explicit model and more than one
    mtplx entry, refuse rather than guess — a caller that failed to
    forward the model name would otherwise silently serve (and
    benchmark) the wrong weights with no error."""
    if model:
        return model
    registry = load_registry()
    matches = [m.model_name for m in registry.models if m.provider_id == "mtplx"]
    if len(matches) == 1:
        return matches[0]
    if not matches:
        raise LifecycleError("no mtplx model in the registry")
    raise LifecycleError(
        f"model required: registry holds {len(matches)} mtplx models "
        f"({', '.join(matches)})"
    )
```

Update the shim's usage comment (`bin/llm-isolate-provider`, lines 7-9): change "mtplx accepts an optional [model-or-target] positional arg (else the first mtplx model in the registry)" to "(else the single mtplx model in the registry; an ambiguous registry — more than one mtplx entry — fails loudly rather than guessing)".

Then sweep for stale claims about the old fallback:

```bash
git grep -n "first mtplx model" -- bin/ docs/ modelman/ CLAUDE.md modelman/CLAUDE.md benchmarks/
```

Fix every hit (comment or doc) to the new semantics. (Verified: `bin/llm-isolate-provider:9` is the known one; check for doc drift in `docs/guides/` and both CLAUDE.md files.)

- [ ] **Step 4: Run tests**

```bash
cd modelman && uv run pytest tests/test_lifecycle.py tests/test_local_control.py -q && make check
cd .. && make lint-shell && make check-links
```

Expected: all pass. The pre-existing `test_isolate_mtplx_no_model_in_registry_returns_error` (zero matches) and `test_isolate_mtplx_starts_serve_and_warmup` (single match, `_mtplx_registry`) keep their documented behavior.

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/providers/lifecycle.py bin/llm-isolate-provider modelman/tests/test_lifecycle.py
git commit -m "fix(lifecycle): refuse ambiguous no-model mtplx resolution instead of silently guessing - completes plan item 7

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 8: single-source the mtplx port constants inside modelman

**Finding (cleanup):** port 8003 is re-spelled in five places: `lifecycle.py:31-33` (three constants), `registry.py:289` (provider template `auth.base_url`), `local_control.py:50` (`_DEFAULT_BASE_ORIGIN`), `bin/lib/mtplx.sh:20`, and `wt/internal/localgate/localgate.go:32`. Scope: consolidate the three Python sites to one shared constant home (imports verified acyclic — no provider module imports top-level `registry.py`); bash and Go each already carry the number exactly once per file, so they only gain a pointer comment (cross-language sharing flows through `registry.toml`'s `auth.base_url`, not imports).

**Files:**
- Modify: `modelman/src/modelman/providers/mtplx.py` (constants home)
- Modify: `modelman/src/modelman/providers/lifecycle.py:31-33`
- Modify: `modelman/src/modelman/registry.py:289` and imports
- Modify: `modelman/src/modelman/local_control.py:50` and imports
- Modify: `bin/lib/mtplx.sh` (comment only), `wt/internal/localgate/localgate.go` (comment only)

**Interfaces:**
- Produces: `modelman.providers.mtplx.MTPLX_PORT` (int), `MTPLX_BASE` (str), `MTPLX_V1_BASE` (str) — imported by lifecycle, registry (template), and local_control. Values unchanged (8003 / http://localhost:8003 / …/v1), so all existing tests that pin the literal strings stay green.

- [ ] **Step 1: Define the constants**

In `modelman/src/modelman/providers/mtplx.py`, after the module docstring and imports, add:

```python
# The mtplx serve port and derived base URLs — the single source of
# truth inside modelman: lifecycle.py (server management), registry.py
# (the provider template's auth.base_url), and local_control.py (probe
# fallback) all import from here. The bash (bin/lib/mtplx.sh) and Go
# (wt/internal/localgate) sides each keep their own single in-file
# constant — cross-language sharing happens through registry.toml's
# auth.base_url, not imports.
MTPLX_PORT = 8003
MTPLX_BASE = f"http://localhost:{MTPLX_PORT}"
MTPLX_V1_BASE = f"{MTPLX_BASE}/v1"
```

- [ ] **Step 2: Rewire the three Python consumers**

In `lifecycle.py`, replace lines 31-33 with:

```python
from .mtplx import MTPLX_BASE, MTPLX_PORT, MTPLX_V1_BASE

MTPLX_DIRECT_URL = f"{MTPLX_V1_BASE}/chat/completions"
```

(keep `MTPLX_PIDFILE` / `MTPLX_LOG` where they are; merge the import into the existing import block rather than a mid-file import — ruff will flag a misplaced one).

In `registry.py`, add to the import block:

```python
from .providers.mtplx import MTPLX_V1_BASE
```

and change the mtplx template entry (current line 289):

```python
        auth=AuthConfig(type="none", base_url=MTPLX_V1_BASE),
```

In `local_control.py`, add the import and change the dict entry (current line 50):

```python
from .providers.mtplx import MTPLX_BASE
    ...
    "mtplx": MTPLX_BASE,
```

- [ ] **Step 3: Pointer comments in bash and Go**

In `bin/lib/mtplx.sh`, extend the `mtplx_stop` comment above `local port=8003`:

```bash
    # Port 8003 is mtplx's fixed serve port. modelman's single source for
    # it is modelman/providers/mtplx.py (MTPLX_PORT); this bash constant
    # and wt's localgate.go each carry the number once — keep the three
    # in lockstep when it ever moves.
    local port=8003
```

In `wt/internal/localgate/localgate.go`, add the same pointer comment above `mtplxModelsURL`:

```go
	// Port 8003 is mtplx's fixed serve port; modelman's single source is
	// modelman/providers/mtplx.py (MTPLX_PORT). Bash (bin/lib/mtplx.sh) and
	// Go each carry the number once — keep them in lockstep if it moves.
	mtplxModelsURL       = "http://localhost:8003/v1/models"
```

- [ ] **Step 4: Run tests and builds**

```bash
cd modelman && uv run pytest tests/test_lifecycle.py tests/test_registry.py tests/test_local_control.py tests/test_providers -q && make check
cd .. && make lint-shell
cd wt && go build ./... && go vet ./...
```

Expected: all pass — values are unchanged; this is pure consolidation. (Watch for a circular-import surprise in `registry.py`: if it appears, the fallback is to import inside a small `_mtplx_template_base()` helper — but the import graph was verified acyclic: `providers/__init__.py` imports only provider modules, none of which import top-level `registry.py`.)

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/providers/mtplx.py modelman/src/modelman/providers/lifecycle.py modelman/src/modelman/registry.py modelman/src/modelman/local_control.py bin/lib/mtplx.sh wt/internal/localgate/localgate.go
git commit -m "refactor(mtplx): single-source the mtplx port constants in modelman - completes plan item 8

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 9: what/why docstrings for the bare tests this branch added

**Finding (conventions):** 13 tests added by this branch have no what/why comment or docstring, violating the user-level rule ("always add a comment describing what the test does and why it is important"). Per the rule's own test — can a reader understand the intent without reading the implementation? — `test_download_raises` doesn't say the NotImplementedError is a deliberate manages-own-cache contract (not an unfinished stub), and `test_cli_prints_json_envelope` isn't marked as the bash-shim stdout contract `isolation.py` parses.

**Files:**
- Modify: `modelman/tests/test_providers/test_mtplx.py` (7 bare tests — every `def test_` lacking a comment above or docstring below; `grep -n "def test_" modelman/tests/test_providers/test_mtplx.py` and check each)
- Modify: `modelman/tests/test_lifecycle.py` (4 bare tests: `test_isolate_mtplx_starts_serve_and_warmup`, `test_isolate_mtplx_uses_explicit_model`, `test_isolate_mtplx_no_model_in_registry_returns_error`, `test_cli_prints_json_envelope`)
- Modify: `modelman/tests/test_registry.py:1231` (`test_mtplx_provider_has_default_template`)
- Modify: `modelman/tests/test_local_control.py:318` (`test_start_mtplx_isolates_without_env_var`)

**Interfaces:** none — test documentation only.

- [ ] **Step 1: Add a one-to-two-sentence docstring to each listed test**

Style: match the tests this plan already added (docstring first line = what, second part = why it matters / what breaks if the behavior regresses). Required content notes:

- `test_download_raises` (test_mtplx.py): the NotImplementedError is the deliberate manages-own-cache contract — MTPLX caches via its own CLI and modelman must never drive a download for it; a future reader must not "fix" this as an unfinished stub.
- `test_cli_prints_json_envelope` (test_lifecycle.py): stdout IS the contract the bash shim and `modelman/benchmark/isolation.py` parse — a traceback or empty stdout there surfaces downstream as "isolation helper returned invalid JSON".
- The rest: one sentence of behavior + one of consequence (e.g. for `test_isolate_mtplx_uses_explicit_model`: the positional arg is the only way a benchmark sweep selects the model; a forwarding gap silently serves the registry default).
- Note: tests touched by Tasks 1-7 may already carry docstrings from those tasks — only add what's still bare. Re-grep rather than trusting the finding's list blindly.

- [ ] **Step 2: Verify no bare tests remain in the touched files**

```bash
cd modelman && for f in tests/test_providers/test_mtplx.py tests/test_lifecycle.py; do
  python3 - "$f" <<'EOF'
import ast, sys
tree = ast.parse(open(sys.argv[1]).read())
bare = [n.name for n in ast.walk(tree)
        if isinstance(n, ast.FunctionDef) and n.name.startswith("test_")
        and not (ast.get_docstring(n) or any(
            isinstance(s, ast.Expr) and isinstance(s.value, ast.Constant) and isinstance(s.value.value, str)
            for s in n.body))]
print(sys.argv[1], bare)
EOF
done
```

Expected: both print `[]` (or only pre-existing non-mtplx tests outside this branch's scope — the finding is about THIS branch's additions). For `test_registry.py` and `test_local_control.py`, verify just the two named tests visually.

- [ ] **Step 3: Run the touched test files**

```bash
cd modelman && uv run pytest tests/test_providers/test_mtplx.py tests/test_lifecycle.py tests/test_registry.py tests/test_local_control.py -q && make check
```

Expected: all pass (docstrings change no behavior).

- [ ] **Step 4: Commit**

```bash
git add modelman/tests/test_providers/test_mtplx.py modelman/tests/test_lifecycle.py modelman/tests/test_registry.py modelman/tests/test_local_control.py
git commit -m "test(mtplx): add what/why docstrings to the bare tests from this branch - completes plan item 9

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 10: final verification

**Files:** none — verification only.

- [ ] **Step 1: Full local verification mirroring CI**

```bash
make test-all
```

(lint + modelman `make check`/`make test` + wt `go build`/`vet`/`test`). Expected: all pass.

- [ ] **Step 2: Check for doc drift on changed behavior**

```bash
git grep -n "first mtplx model" -- . || true
git grep -n "shutil.which" -- modelman/src/modelman/providers/lifecycle.py bin/llm-isolate-provider
```

Expected: no stale "first mtplx model" claims; `shutil.which` in lifecycle only as the env-var fallback.

- [ ] **Step 3: Report**

Summarize to the user: findings fixed, test counts, any behavior changes worth calling out (mtplx ready-on is now a flag flip in apply; ambiguous no-model mtplx isolate now fails loudly; same-model re-isolate keeps loaded weights; stop-all grew an optional keep arg).

---

## Self-Review Notes

- **Spec coverage:** all 10 findings map to tasks — finding 1 → Task 1; finding 2 → Task 2; findings 3+5 → Task 3; finding 4 → Task 4; finding 6 → Task 5; finding 7 → Task 6; finding 8 → Task 7; finding 9 → Task 8; finding 10 → Task 9. The refuted `_name_matches` finding is deliberately absent.
- **Type consistency:** `_wait_for_model(model, proc, timeout)` is used identically in Tasks 5 and 6; `_stop_others(keep="")` in Tasks 2→6; `LifecycleResult` envelope shape is unchanged everywhere.
- **Task ordering note:** Tasks 3, 5, 6 all reshape `isolate()` — each task's code shows the cumulative shape, and each is independently committable because its tests patch the internals it depends on.