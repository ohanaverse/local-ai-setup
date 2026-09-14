# Multi-model Local Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bring local-model start/stop into the modelman TUI, replace the single global `[local].running_model` marker with a per-model `running` flag verified by live probes, allow multiple local models to run concurrently (subject to real per-provider process limits), and update wt's picker to offer every verified-running local model instead of at most one.

**Architecture:** Python (`modelman/`) owns the per-model `running` flag in `modelman.toml` and the start/stop orchestration; Go (`wt/`) reads that flag read-only and layers a live probe on top before trusting it, exactly mirroring today's issue #65 pattern but generalized from one marker to a set. A prerequisite discovered during planning (not covered by the spec at the shell-script level): `bin/llm-isolate-provider`'s per-provider start branches unconditionally stop every *other* local provider before starting — the actual mechanism behind today's global exclusivity, shared with `modelman benchmark`'s isolation path. Task 1 adds a `--solo` start mode and a `stop <provider>` verb so the new same-provider-only lifecycle can be built without touching sibling providers, while every existing benchmark call site keeps today's behavior unchanged (solo defaults off).

**Tech Stack:** Python 3.13 (Typer CLI, Textual TUI, pytest), Go 1.26 (cobra CLI, Bubble Tea TUI, `go test`), bash (`bin/llm-isolate-provider` and `bin/lib/*.sh`).

**Spec:** `docs/superpowers/specs/2026-09-14-local-model-lifecycle-design.md`

## Global Constraints

- Existing `modelman benchmark` isolation behavior (full stop-everyone-else) must not change — every new solo/single-provider primitive is additive, called only from the new local-lifecycle path.
- `modelman.toml`'s per-model `running` field replaces `[local].running_model` outright — no migration step; a pre-upgrade `[local]` table is discarded on next save (self-healing per the spec, section "Restart handling").
- Cross-language contract: `docs/contracts/modelman.sample.toml` must stay in lockstep with `modelman/tests/contracts/test_modelman_fixture.py` (Python) and `wt/internal/config/modelman_fixture_test.go` (Go, `TestLoadModelmanStateMatchesSharedFixture`).
- Every new subprocess-shelling function must accept an injectable runner/seam the same way existing ones do (`modelman/CLAUDE.md`: "New Provider methods that shell out must accept an optional runner arg").
- Go: every new/changed seam follows the existing `var x = realX` + `realX` pattern (`wt/CLAUDE.md`, "Test seams").
- Go: every `Test*` carries a `//` comment stating what it tests and why (`wt/CLAUDE.md`).

---

## Task 1: `bin/llm-isolate-provider` — solo start mode + single-provider stop

**Files:**
- Modify: `bin/llm-isolate-provider`
- Modify: `modelman/src/modelman/benchmark/isolation.py`
- Modify: `modelman/src/modelman/providers/lifecycle.py`
- Test: `modelman/tests/benchmark/test_isolation.py`
- Test: `modelman/tests/test_providers/test_lifecycle.py` (create if it does not already cover `isolate`/`stop`; check first)

**Interfaces:**
- Produces: `isolate_provider(provider_id, *extra_args, env=None, solo=False)` (adds `solo` — when `True`, prepends `--solo` to the helper's argv); `stop_provider(provider_id) -> IsolateResult` (new); `lifecycle.isolate(provider_id, model=None, *, extra_args=(), solo=False)` (adds `solo`); `lifecycle.stop(provider_id)` (unchanged signature, still mtplx-only — Task 3 does not need it extended, since omlx/mlx_lm_server single-provider stop goes through the new bash `stop <provider>` verb via `stop_provider()`, not through `lifecycle.stop`).

- [ ] **Step 1: Add `--solo` parsing and a `stop <provider>` verb to the bash helper**

Edit `bin/llm-isolate-provider`. Insert right after the existing usage-check block (after the `PROVIDER="$1"` / empty-check lines near the top):

```bash
SOLO=""
if [ "$1" = "--solo" ]; then
    SOLO=1
    shift
fi
```

Move `PROVIDER="$1"` to after this block (it already reads `$1`, just ensure the solo-shift runs first — reorder so `SOLO` detection precedes the `PROVIDER="$1"` / empty-check lines instead of following them).

Then, in each of these three case branches, replace the unconditional `stop_all_local <self>` call with a solo-guarded one (leave the `mtplx` and `llamacpp` branches untouched — mtplx's solo handling is Step 3's job in `lifecycle.py`, and llamacpp is retired):

```bash
    ollama)
        [ -n "$SOLO" ] || stop_all_local ollama
        start_ollama
        model="$OLLAMA_MODEL"
        direct_url="http://localhost:11434/v1/chat/completions"
        ;;
```

```bash
    omlx)
        [ -n "$SOLO" ] || stop_all_local omlx
        start_omlx "$OMLX_4BIT_MODEL"
        model="$OMLX_4BIT_MODEL"
        direct_url="http://localhost:8000/v1/chat/completions"
        ;;
    omlx-6bit)
        [ -n "$SOLO" ] || stop_all_local omlx
        start_omlx "$OMLX_6BIT_MODEL"
        model="$OMLX_6BIT_MODEL"
        direct_url="http://localhost:8000/v1/chat/completions"
        ;;
```

```bash
    mlx_lm_server)
        [ -n "$SOLO" ] || stop_all_local mlx_lm_server
        target="${2:-$MLXLM_MODEL}"
        draft="${3:-$MLXLM_DRAFT_MODEL}"
        if [ -z "$target" ] || [ -z "$draft" ]; then
            echo "mlx_lm_server requires target+draft: pass as positional args or set LLM_ISOLATE_MLXLM_MODEL/LLM_ISOLATE_MLXLM_DRAFT_MODEL" >&2
            exit 1
        fi
        start_mlx_lm_server "$target" "$draft"
        model="$target (+draft $draft)"
        direct_url="http://localhost:8001/v1/chat/completions"
        ;;
```

Add a new case, before the `mtplx)` case (order doesn't matter functionally, but keep the file's existing top-to-bottom provider ordering: `stop-all`, `ollama`, `llamacpp`, `omlx`, `omlx-6bit`, `mlx_lm_server`, then this new `stop)` verb, then `mtplx)`):

```bash
    stop)
        # Stop exactly ONE local provider, leaving every other one alone —
        # the primitive the new same-provider-only local-model lifecycle
        # (modelman/src/modelman/local_control.py) needs to replace a
        # single-port provider's occupant without touching siblings.
        # Reuses the exact stop bodies stop_all_local's per-provider
        # branches already run, just without the "skip the target"
        # exemption and without backgrounding (one provider, no need for
        # parallel wait).
        TARGET="$2"
        case "$TARGET" in
            ollama)
                while IFS= read -r loaded; do
                    [ -n "$loaded" ] || continue
                    silence_stdout ollama stop "$loaded" || true
                done < <(ollama ps 2>/dev/null | tail -n +2 | awk 'NF {print $1}')
                wait_for_ollama_unloaded 5 \
                    || echo "warning: ollama still has models loaded" >&2
                ;;
            omlx|omlx-6bit)
                silence_stdout omlx stop || true
                wait_for_port_closed http://localhost:8000/v1/models 5 \
                    || echo "warning: omlx still listening on port 8000" >&2
                ;;
            mlx_lm_server)
                mlx_lm_server_stop
                wait_for_port_closed http://localhost:8001/v1/models 5 \
                    || echo "warning: mlx_lm_server still listening on port 8001" >&2
                ;;
            mtplx)
                mtplx_stop
                ;;
            *)
                echo "unknown provider: $TARGET" >&2
                exit 1
                ;;
        esac
        python3 - <<'PYEOF'
import json
print(json.dumps({"provider": None, "model": None, "direct_url": None, "ok": True, "error": None}))
PYEOF
        exit 0
        ;;
```

Update the file's header comment (the `# Usage:` block near the top) to document both additions:

```bash
# Usage: llm-isolate-provider [--solo] <provider-id> [model-or-target] [draft]
#        llm-isolate-provider stop-all [keep]  (stop every local provider, or
#        keep one provider's models loaded; modelman stop passes no keep)
#        llm-isolate-provider stop <provider-id>  (stop exactly one local
#        provider, leaving every other one running)
# --solo (start mode only): skip stopping every OTHER local provider before
#   starting — used by modelman's same-provider-only local-model lifecycle
#   (local_control.py) so starting one provider never tears down another.
#   Never passed by modelman benchmark, which still needs full exclusivity.
```

- [ ] **Step 2: `bash -n` and shellcheck the edited script**

Run: `bash -n bin/llm-isolate-provider && shellcheck --severity=error bin/llm-isolate-provider`
Expected: both exit 0, no output.

- [ ] **Step 3: Thread `solo` through `isolation.py`**

Edit `modelman/src/modelman/benchmark/isolation.py`. Change `isolate_provider`'s signature and the argv it builds:

```python
def isolate_provider(
    provider_id: str, *extra_args: str, env: dict[str, str] | None = None, solo: bool = False
) -> IsolateResult:
    """Delegate service isolation to the local-ai-setup helper.

    `solo=True` prepends `--solo` to the helper's argv, which skips
    stopping every OTHER local provider before starting `provider_id` —
    used by modelman's same-provider-only local-model lifecycle
    (local_control.py). Never passed by modelman benchmark, which still
    needs full exclusivity for clean measurement.
    """
    helper = _helper_path("llm-isolate-provider")
    run_kwargs: dict[str, Any] = {"capture_output": True, "text": True, "check": False}
    if env is not None:
        run_kwargs["env"] = {**os.environ, **env}
    argv = [helper, *(["--solo"] if solo else []), provider_id, *extra_args]
    result = subprocess.run(argv, **run_kwargs)
    if result.returncode != 0:
        raise BenchmarkError(
            f"isolation failed for {provider_id}: {result.stderr.strip() or result.stdout.strip()}"
        )
    try:
        data = json.loads(result.stdout)
    except json.JSONDecodeError as exc:
        raise BenchmarkError(
            f"isolation helper returned invalid JSON for {provider_id}: {exc}"
        ) from exc
    return IsolateResult(
        provider=data.get("provider", provider_id),
        model=data.get("model", ""),
        direct_url=data.get("direct_url", ""),
        ok=data.get("ok", False),
        error=data.get("error"),
    )
```

Add a new function right after `stop_all_local_providers`:

```python
def stop_provider(provider_id: str) -> IsolateResult:
    """Stop exactly one local provider via the isolation helper's `stop`
    mode, leaving every other local provider running. Used by the
    same-provider-only local-model lifecycle to replace a single-port
    provider's (omlx/omlx-6bit) occupant before starting a different
    model on it — mlx_lm_server and mtplx never need this (mlx_lm_server
    self-replaces inside its own start function; mtplx's replace logic
    lives in providers/lifecycle.py's isolate())."""
    helper = _helper_path("llm-isolate-provider")
    result = subprocess.run(
        [helper, "stop", provider_id], capture_output=True, text=True, check=False
    )
    if result.returncode != 0:
        raise BenchmarkError(
            f"stop failed for {provider_id}: {result.stderr.strip() or result.stdout.strip()}"
        )
    try:
        data = json.loads(result.stdout)
    except json.JSONDecodeError as exc:
        raise BenchmarkError(f"isolation helper returned invalid JSON for stop {provider_id}: {exc}") from exc
    return IsolateResult(
        provider=data.get("provider") or provider_id,
        model=data.get("model") or "",
        direct_url=data.get("direct_url") or "",
        ok=data.get("ok", False),
        error=data.get("error"),
    )
```

- [ ] **Step 4: Write failing tests for the new Python wrappers**

Add to `modelman/tests/benchmark/test_isolation.py` (open it first to match its existing mocking style — it almost certainly patches `subprocess.run`; follow that pattern):

```python
def test_isolate_provider_solo_prepends_flag(monkeypatch):
    calls = []

    def fake_run(argv, **kwargs):
        calls.append(argv)
        return SimpleNamespace(
            returncode=0,
            stdout=json.dumps({"provider": "omlx", "model": "x", "direct_url": "u", "ok": True, "error": None}),
            stderr="",
        )

    monkeypatch.setattr("modelman.benchmark.isolation.subprocess.run", fake_run)
    monkeypatch.setattr("modelman.benchmark.isolation.shutil.which", lambda name: f"/fake/{name}")
    isolate_provider("omlx", solo=True)
    assert calls[0][1] == "--solo"
    assert calls[0][2] == "omlx"


def test_isolate_provider_default_omits_flag(monkeypatch):
    calls = []

    def fake_run(argv, **kwargs):
        calls.append(argv)
        return SimpleNamespace(
            returncode=0,
            stdout=json.dumps({"provider": "omlx", "model": "x", "direct_url": "u", "ok": True, "error": None}),
            stderr="",
        )

    monkeypatch.setattr("modelman.benchmark.isolation.subprocess.run", fake_run)
    monkeypatch.setattr("modelman.benchmark.isolation.shutil.which", lambda name: f"/fake/{name}")
    isolate_provider("omlx")
    assert "--solo" not in calls[0]


def test_stop_provider_success(monkeypatch):
    def fake_run(argv, **kwargs):
        assert argv[-2:] == ["stop", "omlx"]
        return SimpleNamespace(
            returncode=0,
            stdout=json.dumps({"provider": None, "model": None, "direct_url": None, "ok": True, "error": None}),
            stderr="",
        )

    monkeypatch.setattr("modelman.benchmark.isolation.subprocess.run", fake_run)
    monkeypatch.setattr("modelman.benchmark.isolation.shutil.which", lambda name: f"/fake/{name}")
    result = stop_provider("omlx")
    assert result.ok is True
```

Add the needed imports at the top of the test file if not already present: `import json`, `from types import SimpleNamespace`, and extend the existing `from modelman.benchmark.isolation import ...` line with `isolate_provider, stop_provider`.

- [ ] **Step 5: Run the new tests to see them fail**

Run: `cd modelman && uv run pytest tests/benchmark/test_isolation.py -k "solo or stop_provider" -v`
Expected: FAIL — `stop_provider` not defined / `solo` unexpected keyword argument.

- [ ] **Step 6: Confirm the tests pass against Steps 1–3's implementation**

Run: `cd modelman && uv run pytest tests/benchmark/test_isolation.py -v`
Expected: PASS, full file.

- [ ] **Step 7: Add `solo` to `providers/lifecycle.py`'s `isolate()` for mtplx**

Edit `modelman/src/modelman/providers/lifecycle.py`. Change `isolate`'s signature and body:

```python
def isolate(
    provider_id: str, model: str | None = None, *, extra_args: tuple[str, ...] = (), solo: bool = False
) -> LifecycleResult:
    """Stop every other local provider and start the requested one —
    unless solo=True, in which case only THIS provider's own occupant (if
    serving a different model) is stopped, and every sibling provider is
    left alone. solo is used by modelman's same-provider-only local-model
    lifecycle (local_control.py); never passed by modelman benchmark."""
    if provider_id != "mtplx":
        # Transition: delegate non-mtplx providers to the bash helper.
        return _delegate_isolate(provider_id, model, extra_args, solo=solo)
    started = False
    try:
        resolved = _resolve_mtplx_model(model)
        if _serving_model(resolved):
            if not solo:
                _stop_others(keep="mtplx")
            started = True
            _warmup(resolved)
        else:
            if solo:
                _stop_mtplx()
            else:
                _stop_others()
            proc = _start_mtplx_serve(resolved)
            started = True
            _wait_for_model(resolved, proc)
            _warmup(resolved)
    except Exception as exc:  # noqa: BLE001 — envelope contract, never a traceback
        if started:
            with contextlib.suppress(Exception):  # noqa: BLE001
                _stop_mtplx()
        return LifecycleResult("mtplx", model or "", MTPLX_DIRECT_URL, False, str(exc))
    return LifecycleResult("mtplx", resolved, MTPLX_DIRECT_URL, True, None)


def _delegate_isolate(
    provider_id: str, model: str | None, extra_args: tuple[str, ...], *, solo: bool = False
) -> LifecycleResult:
    helper = shutil.which("llm-isolate-provider")
    if helper is None:
        return LifecycleResult(provider_id, model or "", "", False, "isolation helper not found on PATH")
    env = None
    if model and provider_id in _ENV_VAR_BY_PROVIDER:
        env = {**os.environ, _ENV_VAR_BY_PROVIDER[provider_id]: model}
    argv = [helper, *(["--solo"] if solo else []), provider_id, *extra_args]
    result = subprocess.run(argv, capture_output=True, text=True, check=False, env=env)
    if result.returncode != 0:
        return LifecycleResult(provider_id, model or "", "", False, result.stderr.strip() or result.stdout.strip())
    try:
        data = json.loads(result.stdout)
    except json.JSONDecodeError:
        return LifecycleResult(provider_id, model or "", "", False, "invalid JSON from isolation helper")
    return LifecycleResult(
        provider=data.get("provider", provider_id),
        model=data.get("model", ""),
        direct_url=data.get("direct_url", ""),
        ok=data.get("ok", False),
        error=data.get("error"),
    )
```

Also update `_main`'s `isolate` argv parsing to accept a trailing `--solo` token:

```python
        if cmd == "isolate":
            provider = argv[1] if len(argv) > 1 else ""
            rest = argv[2:]
            solo = "--solo" in rest
            rest = [a for a in rest if a != "--solo"]
            model = rest[0] if rest else None
            extra_args = tuple(rest[1:])
            result = isolate(provider, model, extra_args=extra_args, solo=solo)
```

- [ ] **Step 8: Add and run a failing-then-passing test for mtplx solo behavior**

First check whether `modelman/tests/test_providers/test_lifecycle.py` already exists (`ls modelman/tests/test_providers/`). If it does, add these two tests to it following its existing mocking conventions (it will already patch `_stop_others`, `_stop_mtplx`, `_start_mtplx_serve`, `_wait_for_model`, `_warmup`, `_serving_model` — match that style). If it does not exist, create it importing those same private helpers from `modelman.providers.lifecycle` and patch them with `unittest.mock.patch`.

```python
def test_isolate_solo_different_model_stops_only_mtplx(monkeypatch):
    with (
        patch("modelman.providers.lifecycle._resolve_mtplx_model", return_value="org/new"),
        patch("modelman.providers.lifecycle._serving_model", return_value=False),
        patch("modelman.providers.lifecycle._stop_mtplx") as mock_stop_mtplx,
        patch("modelman.providers.lifecycle._stop_others") as mock_stop_others,
        patch("modelman.providers.lifecycle._start_mtplx_serve"),
        patch("modelman.providers.lifecycle._wait_for_model"),
        patch("modelman.providers.lifecycle._warmup"),
    ):
        result = isolate("mtplx", "org/new", solo=True)
    mock_stop_mtplx.assert_called_once()
    mock_stop_others.assert_not_called()
    assert result.ok is True


def test_isolate_non_solo_different_model_stops_everyone(monkeypatch):
    with (
        patch("modelman.providers.lifecycle._resolve_mtplx_model", return_value="org/new"),
        patch("modelman.providers.lifecycle._serving_model", return_value=False),
        patch("modelman.providers.lifecycle._stop_mtplx") as mock_stop_mtplx,
        patch("modelman.providers.lifecycle._stop_others") as mock_stop_others,
        patch("modelman.providers.lifecycle._start_mtplx_serve"),
        patch("modelman.providers.lifecycle._wait_for_model"),
        patch("modelman.providers.lifecycle._warmup"),
    ):
        result = isolate("mtplx", "org/new")
    mock_stop_others.assert_called_once_with()
    mock_stop_mtplx.assert_not_called()
    assert result.ok is True
```

Run: `cd modelman && uv run pytest tests/test_providers/test_lifecycle.py -k solo -v` — expect FAIL first (helper functions don't accept/branch on `solo` yet — actually Step 7 already implements it, so run this BEFORE Step 7's edit is saved if following strict TDD; since Step 7 is already written above, apply Step 7 first, then this step should PASS immediately). Run again and expect PASS.

- [ ] **Step 9: Commit**

```bash
git add bin/llm-isolate-provider modelman/src/modelman/benchmark/isolation.py modelman/src/modelman/providers/lifecycle.py modelman/tests/benchmark/test_isolation.py modelman/tests/test_providers/test_lifecycle.py
git commit -m "feat(isolation): add solo start mode and single-provider stop - completes plan item #1"
```

---

## Task 2: modelman data model — per-model `running` flag

**Files:**
- Modify: `modelman/src/modelman/state.py`
- Modify: `docs/contracts/modelman.sample.toml`
- Modify: `modelman/tests/contracts/test_modelman_fixture.py`
- Test: `modelman/tests/test_state.py` (open first to match existing style/fixtures)

**Interfaces:**
- Produces: `ModelState.running: bool` (new field, default `False`); `StateStore` no longer has a `.local` attribute or `LocalState` type — any remaining reference is a bug from here on.

- [ ] **Step 1: Write failing tests for the new field and the retired `[local]` table**

Add to `modelman/tests/test_state.py`:

```python
def test_model_state_running_defaults_false():
    store = StateStore()
    assert store.get("ollama/x").running is False


def test_running_round_trips_through_save_and_load(tmp_path):
    path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/x", ModelState(ready=True, running=True))
    save_state(store, path)
    reloaded = load_state(path)
    assert reloaded.get("ollama/x").running is True
    assert reloaded.get("ollama/x").ready is True


def test_stale_local_table_is_dropped_on_load_and_save(tmp_path):
    # Pre-upgrade files may still carry [local].running_model - it must be
    # silently discarded, not preserved as inert extra data, so an old
    # marker never round-trips back into a fresh file.
    path = tmp_path / "modelman.toml"
    path.write_text('[local]\nrunning_model = "ollama/x"\n\n[model_state."ollama/x"]\nready = true\n')
    store = load_state(path)
    assert not hasattr(store, "local")
    save_state(store, path)
    assert "[local]" not in path.read_text()


def test_state_store_has_no_local_attribute():
    store = StateStore()
    assert not hasattr(store, "local")
```

- [ ] **Step 2: Run to verify failure**

Run: `cd modelman && uv run pytest tests/test_state.py -k "running or local_table or no_local" -v`
Expected: FAIL — `ModelState.__init__() got an unexpected keyword argument 'running'` (and the no-local-attribute tests currently fail because `store.local` DOES exist today).

- [ ] **Step 3: Implement the schema change**

Edit `modelman/src/modelman/state.py`:

1. Remove the `LocalState` dataclass entirely (the current 8-line block right before `ModelState`).
2. Add `running: bool = False` to `ModelState`:

```python
@dataclass
class ModelState:
    ready: bool = False
    disk_path: str | None = None
    size_bytes: int | None = None
    exposed: bool = False  # was litellm_exposed
    running: bool = False
    extra: dict[str, Any] = field(default_factory=dict, repr=False)
```

3. Remove `local: LocalState = field(default_factory=LocalState)` from `StateStore`.
4. In `load_state`, add `running` to the `ModelState(...)` construction and its `unknown_keys` exclusion set, and delete the `local_raw`/`local` lines (the `[local]` table is read implicitly as part of `unknown_keys(raw, {...})`'s top-level pass, which will now correctly drop it since `"local"` stays in that set):

```python
    models = {
        model_id: ModelState(
            ready=entry.get("ready", entry.get("downloaded", False)),
            disk_path=entry.get("disk_path"),
            size_bytes=entry.get("size_bytes"),
            exposed=entry.get("exposed", entry.get("litellm_exposed", False)),
            running=entry.get("running", False),
            extra=unknown_keys(
                entry,
                {"ready", "downloaded", "disk_path", "size_bytes", "exposed", "litellm_exposed", "running"},
            ),
        )
        for model_id, entry in raw.get("model_state", {}).items()
    }
    families = {
        family: FamilyState(
            display_name=entry.get("display_name"),
            extra=unknown_keys(entry, {"display_name"}),
        )
        for family, entry in raw.get("families", {}).items()
    }
    litellm_raw = raw.get("litellm", {})
    litellm = LitellmState(
        enabled=litellm_raw.get("enabled", False),
        url=litellm_raw.get("url"),
        api_key=litellm_raw.get("api_key"),
    )
    return StateStore(
        models=models,
        families=families,
        litellm=litellm,
        extra=unknown_keys(raw, {"model_state", "families", "litellm", "local"}),
    )
```

(Note `"local"` stays in the final `unknown_keys` call's exclusion set — this is what makes a stale `[local]` table vanish on load instead of landing in `extra` and round-tripping back out on the next save.)

5. In `save_state`, remove the `"local": drop_none(...)` line from `payload` and add `"running"` to each model_state dict:

```python
def save_state(store: StateStore, path: Path | None = None) -> None:
    state_path = Path(path) if path else _default_state_path()
    payload = {
        "model_state": {
            model_id: drop_none(
                {
                    **s.extra,
                    "ready": s.ready,
                    "disk_path": s.disk_path,
                    "size_bytes": s.size_bytes,
                    "exposed": s.exposed,
                    "running": s.running,
                }
            )
            for model_id, s in store.models.items()
        },
        "families": {
            family: drop_none({**s.extra, "display_name": s.display_name})
            for family, s in store.families.items()
        },
        "litellm": drop_none(
            {
                "enabled": store.litellm.enabled,
                "url": store.litellm.url,
                "api_key": store.litellm.api_key,
            }
        ),
    }
    atomic_write_toml({**store.extra, **payload}, state_path)
```

- [ ] **Step 4: Run tests to verify pass**

Run: `cd modelman && uv run pytest tests/test_state.py -v`
Expected: PASS, full file.

- [ ] **Step 5: Update the shared contract fixture and both contract tests**

Edit `docs/contracts/modelman.sample.toml`: delete the `[local]` table and its comment block entirely, and add `running = true` to the local, ready, exposed model (so the fixture exercises a genuinely-running entry):

```toml
[model_state."ollama/contract-fixture:local"]
ready = true
disk_path = "ollama:contract-fixture:local"
size_bytes = 2147483648
exposed = false
running = true
```

Update the file's header comment block (currently describing the single-marker rule) — replace:

```
# Single local model wt's picker may currently offer (issue #65). Absent
# or empty = no local model is running; wt shows cloud models only.
[local]
running_model = "ollama/contract-fixture:local"
```

with nothing (delete outright) and add one line to the top-of-file docstring comment noting the schema change:

```
# Local-model running state (multi-model local lifecycle design,
# 2026-09-14): each model_state entry carries its own `running` flag,
# verified live by both readers before being trusted — see
# docs/superpowers/specs/2026-09-14-local-model-lifecycle-design.md.
# The single [local].running_model marker (issue #65) is retired.
```

Edit `modelman/tests/contracts/test_modelman_fixture.py`: replace the `[local]` assertion block:

```python
    # [local] running-model marker (issue #65): the single local model
    # wt's picker may currently offer.
    assert state.local.running_model == "ollama/contract-fixture:local"
```

with:

```python
    # Per-model running flag (2026-09-14 multi-model design): replaces
    # the single [local].running_model marker.
    assert local.running is True
    assert sub.running is False
```

- [ ] **Step 6: Run the Python contract test**

Run: `cd modelman && uv run pytest tests/contracts/test_modelman_fixture.py -v`
Expected: PASS.

Note: `go test ./internal/config/...` in `wt/` will now FAIL (it still asserts the old `[local]` schema) — this is expected and intentional until Task 6 updates the Go loader. Do not attempt to fix it here.

- [ ] **Step 7: Run the full modelman focused suite touched so far**

Run: `cd modelman && uv run pytest tests/test_state.py tests/contracts/ -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add modelman/src/modelman/state.py docs/contracts/modelman.sample.toml modelman/tests/contracts/test_modelman_fixture.py modelman/tests/test_state.py
git commit -m "feat(state): replace [local] marker with per-model running flag - completes plan item #2"
```

---

## Task 3: `local_control.py` — same-provider-only lifecycle

**Files:**
- Modify: `modelman/src/modelman/local_control.py`
- Test: `modelman/tests/test_local_control.py`

**Interfaces:**
- Consumes: `state.py`'s `ModelState.running` (Task 2); `benchmark.isolation.isolate_provider(..., solo=True)` / `stop_provider(provider_id)` (Task 1); `providers.lifecycle.isolate(..., solo=True)` (Task 1).
- Produces: `start_local_model(registry, model_id, state_path=None, *, family=None, registry_path=None, litellm_path=None) -> StartResult` (same signature, new same-provider-only behavior); `StartResult` gains `other_running: list[str]` (ids of OTHER local models already running at the moment this call started, for the CLI/TUI warning); `stop_local_model(model_id, state_path=None) -> StopResult` (now takes a required `model_id`); `stop_all_local_models(state_path=None) -> list[str]` (new — stops every running local model, returns the ids stopped); `running_model_ids(registry, state, state_path=None) -> list[str]` (new — flagged AND probe-verified, for CLI/TUI display and the "other running" warning).

- [ ] **Step 1: Write failing tests for the new same-provider-only start behavior**

Read `modelman/tests/test_local_control.py`'s existing `_registry()`/`_state_path()` helpers (already shown above) and extend `_registry()` in place to add an `omlx` provider + two omlx models, since the same-provider-replace tests need two models on one single-port provider:

```python
def _registry() -> Registry:
    return Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", location="local", auth=AuthConfig(type="none")),
            ProviderEntry(id="omlx", name="oMLX", location="local", auth=AuthConfig(type="none")),
            ProviderEntry(
                id="openrouter", name="OpenRouter", location="cloud", auth=AuthConfig(type="api_key")
            ),
            ProviderEntry(
                id="llamacpp", name="llama.cpp", location="local", auth=AuthConfig(type="none")
            ),
        ],
        models=[
            ModelEntry(
                id="ollama/qwen3.8:27b-mlx",
                family="qwen3.8",
                provider_id="ollama",
                model_name="qwen3.8:27b-mlx",
            ),
            ModelEntry(
                id="omlx/model-a",
                family="model-a",
                provider_id="omlx",
                model_name="model-a",
                fetch=Fetch(repo="org/model-a"),
            ),
            ModelEntry(
                id="omlx/model-b",
                family="model-b",
                provider_id="omlx",
                model_name="model-b",
                fetch=Fetch(repo="org/model-b"),
            ),
            ModelEntry(
                id="openrouter/z-ai/glm-5.3-flash",
                family="glm",
                provider_id="openrouter",
                model_name="z-ai/glm-5.3-flash",
                location="cloud",
            ),
            ModelEntry(
                id="llamacpp/retired-model",
                family="retired",
                provider_id="llamacpp",
                model_name="retired-model",
            ),
        ],
    )
```

Replace `_state_path` (it wrote a single `running_model` marker; the new version writes per-model `running` flags):

```python
def _state_path(tmp_path: Path, running: dict[str, bool] | None = None) -> Path:
    """Write an initial modelman.toml with the given per-model running
    flags and return its path."""
    path = tmp_path / "modelman.toml"
    store = StateStore()
    for model_id, is_running in (running or {}).items():
        store.set(model_id, ModelState(ready=True, running=is_running))
    save_state(store, path)
    return path
```

Update every existing call site in the file that passed a bare string to `_state_path` (e.g. `_state_path(tmp_path, "ollama/qwen3.8:27b-mlx")`) to the new dict shape: `_state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True})`. Grep the file for `_state_path(tmp_path,` to find every call site and update each one the same way.

Update every assertion of the old shape `load_state(state_path).local.running_model == "..."` to the new shape `load_state(state_path).get("...").running is True` (or `is False` / a specific id check as appropriate) — grep for `.local.running_model` in the test file and convert each occurrence.

Add new tests for cross-provider concurrency and same-provider replacement:

```python
def test_start_does_not_stop_a_different_provider(tmp_path):
    # Cross-provider concurrency: starting an omlx model while an ollama
    # model is already flagged running must leave ollama's flag alone and
    # never call the global stop-everyone-else path.
    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True})
    with (
        patch("modelman.local_control._probe_running", return_value=False),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_all_local_providers") as mock_stop_all,
        patch("modelman.local_control.stop_provider") as mock_stop_one,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="omlx", model="model-a", direct_url="http://localhost:8000/v1/chat/completions",
            ok=True, error=None,
        )
        result = start_local_model(_registry(), "omlx/model-a", state_path)
    mock_stop_all.assert_not_called()
    mock_stop_one.assert_not_called()  # nothing else was running on omlx
    assert mock_isolate.call_args.kwargs["solo"] is True
    assert result.other_running == ["ollama/qwen3.8:27b-mlx"]
    state = load_state(state_path)
    assert state.get("omlx/model-a").running is True
    assert state.get("ollama/qwen3.8:27b-mlx").running is True  # untouched


def test_start_replaces_same_provider_occupant(tmp_path):
    # omlx/mtplx/mlx_lm_server are single-model-per-process: starting a
    # DIFFERENT model on the same provider must stop the old one first.
    state_path = _state_path(tmp_path, {"omlx/model-a": True})
    with (
        patch("modelman.local_control._probe_running", return_value=False),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_provider") as mock_stop_one,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="omlx", model="model-b", direct_url="http://localhost:8000/v1/chat/completions",
            ok=True, error=None,
        )
        result = start_local_model(_registry(), "omlx/model-b", state_path)
    mock_stop_one.assert_called_once_with("omlx")
    state = load_state(state_path)
    assert state.get("omlx/model-b").running is True
    assert state.get("omlx/model-a").running is False


def test_start_ollama_is_flag_only(tmp_path):
    # Ollama never gets a process call on start - lazy-loads on first
    # request. Only the flag flips.
    state_path = _state_path(tmp_path, {})
    with (
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_provider") as mock_stop_one,
        patch("modelman.local_control.stop_all_local_providers") as mock_stop_all,
    ):
        result = start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)
    mock_isolate.assert_not_called()
    mock_stop_one.assert_not_called()
    mock_stop_all.assert_not_called()
    assert result.already_running is False
    assert load_state(state_path).get("ollama/qwen3.8:27b-mlx").running is True


def test_stop_local_model_stops_one_and_clears_its_flag_only(tmp_path):
    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True, "omlx/model-a": True})
    with patch("modelman.local_control._stop_ollama_model") as mock_stop_ollama:
        result = stop_local_model("ollama/qwen3.8:27b-mlx", state_path)
    mock_stop_ollama.assert_called_once_with("qwen3.8:27b-mlx")
    assert result.stopped_model_id == "ollama/qwen3.8:27b-mlx"
    state = load_state(state_path)
    assert state.get("ollama/qwen3.8:27b-mlx").running is False
    assert state.get("omlx/model-a").running is True  # untouched


def test_stop_local_model_noop_when_not_running(tmp_path):
    state_path = _state_path(tmp_path, {})
    with patch("modelman.local_control._stop_ollama_model") as mock_stop_ollama:
        result = stop_local_model("ollama/qwen3.8:27b-mlx", state_path)
    mock_stop_ollama.assert_not_called()
    assert result.stopped_model_id is None


def test_stop_all_local_models_stops_every_running_one(tmp_path):
    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True, "omlx/model-a": True})
    with patch("modelman.local_control.stop_all_local_providers") as mock_stop_all:
        stopped = stop_all_local_models(state_path)
    mock_stop_all.assert_called_once()
    assert sorted(stopped) == ["ollama/qwen3.8:27b-mlx", "omlx/model-a"]
    state = load_state(state_path)
    assert state.get("ollama/qwen3.8:27b-mlx").running is False
    assert state.get("omlx/model-a").running is False


def test_running_model_ids_filters_to_probe_verified(tmp_path):
    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True, "omlx/model-a": True})
    registry = _registry()
    state = load_state(state_path)
    with patch(
        "modelman.local_control._probe_running",
        side_effect=lambda provider_id, model_name, base: provider_id == "ollama",
    ):
        ids = running_model_ids(registry, state, state_path)
    assert ids == ["ollama/qwen3.8:27b-mlx"]
```

- [ ] **Step 2: Run to verify failure**

Run: `cd modelman && uv run pytest tests/test_local_control.py -v`
Expected: many FAILs (`stop_provider` not imported/mocked target doesn't exist yet, `stop_local_model()` signature mismatch, `stop_all_local_models`/`running_model_ids` undefined, `_stop_ollama_model` doesn't exist, `StartResult.other_running` doesn't exist).

- [ ] **Step 3: Implement the rewrite**

Edit `modelman/src/modelman/local_control.py`.

Update the imports at the top:

```python
from .benchmark.isolation import (
    SUPPORTED_PROVIDER_IDS,
    isolate_provider,
    mlx_lm_server_pairing_args,
    stop_all_local_providers,
    stop_provider,
)
```

Add `other_running` to `StartResult`:

```python
@dataclass
class StartResult:
    model_id: str
    already_running: bool
    direct_url: str | None = None
    warnings: list[str] = field(default_factory=list)
    other_running: list[str] = field(default_factory=list)
```

Replace `_clear_stale_marker` with a per-model equivalent:

```python
def _clear_stale_running_flag(model_id: str, state_path: Path | None) -> None:
    """Best-effort: clear ONE model's running flag when it's still True —
    used when a probe finds it not actually serving (stale flag), or after
    a failed start/stop for that specific model. Never touches any other
    model's flag."""
    try:
        with locked_state(state_path) as fresh:
            existing = fresh.models.get(model_id)
            if existing is not None and existing.running:
                fresh.models[model_id] = replace(existing, running=False)
    except OSError:
        pass
```

(Add `from dataclasses import replace` to the imports if not already present — check first; `ModelState` construction elsewhere in this file may already import it.)

Add a helper that finds another model currently flagged running on the same provider:

```python
def _same_provider_occupant(state: StateStore, provider_id: str, exclude_model_id: str) -> str | None:
    """The id of another model on `provider_id` currently flagged
    running, or None. Single-port providers (omlx, mtplx, mlx_lm_server)
    can only ever serve one model — this finds the one that needs
    replacing before starting a different model on the same provider."""
    for model_id, model_state in state.models.items():
        if model_id != exclude_model_id and model_state.running:
            # provider_id is embedded in the id's prefix by convention
            # (<provider_id>/<name>), but comparing against the actual
            # registry entry (not string-splitting the id) is the
            # correct source of truth when a model name itself contains
            # a "/". Callers pass a pre-filtered id set instead — see
            # start_local_model, which only calls this with ids already
            # known to belong to `provider_id`.
            return model_id
    return None
```

Add ollama's direct per-model stop (no bash helper involved — `ollama stop <name>` is a standalone primitive):

```python
def _stop_ollama_model(model_name: str, runner=_default_runner) -> None:
    """Stop exactly one loaded ollama model (`ollama stop <name>`),
    tolerating any failure (model already unloaded, daemon down) the same
    way the bash helper's stop-all path does."""
    try:
        runner(["ollama", "stop", model_name], capture_output=True, text=True, check=False)
    except OSError:
        pass
```

Rewrite `start_local_model`:

```python
def start_local_model(
    registry: Registry,
    model_id: str,
    state_path: Path | None = None,
    *,
    family: str | None = None,
    registry_path: Path | None = None,
    litellm_path: Path | None = None,
) -> StartResult:
    """Start model_id as a running local model, alongside any other local
    models already running (cross-provider concurrency is unrestricted —
    see docs/superpowers/specs/2026-09-14-local-model-lifecycle-design.md).
    If model_id's OWN provider is already running a DIFFERENT model (a
    single-port provider: omlx, mtplx, mlx_lm_server), that occupant is
    stopped first — those providers can only ever serve one model.
    Ollama never gets a process call: the flag flips, and ollama lazy-
    loads on first request.

    model_id may be a registry id, an existing model's native
    provider-side name, or (with `family` set) the native name of an
    on-disk artifact with no registry.toml entry yet — see
    _resolve_or_register. Raises DiscoveredModelNeedsFamily when the third
    case needs a family the caller hasn't supplied yet.

    Raises LocalControlError when model_id is unknown, not a local model,
    its provider cannot be isolated, or an mlx_lm_server pairing can't be
    resolved.

    Idempotent: when model_id is already flagged running, it is PROBED
    before trusting the flag. A dead flag is cleared and a full start
    runs — modelman start is the recovery command wt's own "not running"
    message prescribes, and must not no-op on a flag whose process died.
    """
    state = load_state(state_path)
    model, registration_warnings = _resolve_or_register(
        registry, state, model_id, family, registry_path, state_path, litellm_path
    )
    resolved_id = model.id

    provider = _provider_entry(registry, model.provider_id)
    if not model_has_local_artifact(model, provider):
        raise LocalControlError(f"{resolved_id} is a cloud model — modelman start only runs local models")

    if model.provider_id not in SUPPORTED_PROVIDER_IDS:
        raise LocalControlError(
            f"provider {model.provider_id!r} cannot be started/stopped by modelman "
            f"(supported: {sorted(SUPPORTED_PROVIDER_IDS)})"
        )

    extra_args: tuple[str, ...] = ()
    env: dict[str, str] | None = None
    if model.provider_id == "mlx_lm_server":
        try:
            target, draft = mlx_lm_server_pairing_args(
                model.id,
                model.fetch.local_path if model.fetch else None,
                model.fetch.repo if model.fetch else None,
                model.draft.local_path if model.draft else None,
                model.draft.repo if model.draft else None,
            )
        except BenchmarkError as exc:
            raise LocalControlError(str(exc)) from exc
        extra_args = (target, draft)
    elif model.provider_id == "mtplx":
        extra_args = (model.model_name,)
    elif model.provider_id != "ollama":
        env = {_ENV_VAR_BY_PROVIDER[model.provider_id]: model.model_name}

    probe_origin = base_origin(provider.auth.base_url) if provider and provider.auth else None

    # Re-read rather than reusing the state loaded above: _resolve_or_register()
    # may have done a real LiteLLM config write that takes non-trivial wall
    # time, long enough for a concurrent start/stop to change flags meanwhile.
    fresh_state = load_state(state_path)
    other_running = sorted(
        mid for mid, s in fresh_state.models.items() if mid != resolved_id and s.running
    )

    already = fresh_state.get(resolved_id).running
    if already:
        if _probe_running(model.provider_id, model.model_name, probe_origin):
            return StartResult(
                model_id=resolved_id, already_running=True,
                warnings=registration_warnings, other_running=other_running,
            )
        _clear_stale_running_flag(resolved_id, state_path)

    if model.provider_id == "ollama":
        # Flag-only: no process action, ollama lazy-loads on request.
        direct_url = None
    else:
        occupant = _same_provider_occupant(
            fresh_state,
            model.provider_id,
            exclude_model_id=resolved_id,
        ) if model.provider_id != "mtplx" else None  # mtplx's own isolate() handles its occupant internally
        # Only consider an occupant that is actually THIS provider's model
        # (the state dict has no provider field, so filter by prefix match
        # against every model this provider owns in the registry).
        provider_model_ids = {m.id for m in registry.models if m.provider_id == model.provider_id}
        if occupant is not None and occupant not in provider_model_ids:
            occupant = None
        if occupant is not None and model.provider_id in ("omlx", "omlx-6bit"):
            try:
                stop_provider(model.provider_id)
            except BenchmarkError as exc:
                raise LocalControlError(f"failed to stop {occupant} before starting {resolved_id}: {exc}") from exc
            _clear_stale_running_flag(occupant, state_path)

        try:
            result = isolate_provider(model.provider_id, *extra_args, env=env, solo=True)
        except BenchmarkError as exc:
            _clear_stale_running_flag(resolved_id, state_path)
            raise LocalControlError(f"failed to start {resolved_id}: {exc}") from exc
        if not result.ok:
            _clear_stale_running_flag(resolved_id, state_path)
            raise LocalControlError(f"failed to start {resolved_id}: {result.error or 'unknown error'}")
        direct_url = result.direct_url or None

    try:
        with locked_state(state_path) as fresh:
            existing = fresh.models.get(resolved_id, ModelState())
            fresh.models[resolved_id] = replace(existing, running=True)
    except OSError as exc:
        raise LocalControlError(
            f"{resolved_id} started successfully but its running flag could not be "
            f"persisted: {exc} — wt's picker will not see it as running until this succeeds"
        ) from exc
    return StartResult(
        model_id=resolved_id, already_running=False, direct_url=direct_url,
        warnings=registration_warnings, other_running=other_running,
    )
```

Replace `stop_local_model` and add `stop_all_local_models`:

```python
def stop_local_model(model_id: str, state_path: Path | None = None) -> StopResult:
    """Stop exactly one running local model and clear its flag. No-op
    when it isn't running. Raises LocalControlError for an unknown
    provider id embedded in model_id's prefix."""
    state = load_state(state_path)
    current = state.get(model_id)
    if not current.running:
        return StopResult(stopped_model_id=None)

    provider_id = model_id.split("/", 1)[0]
    if provider_id == "ollama":
        model_name = model_id.split("/", 1)[1]
        _stop_ollama_model(model_name)
    elif provider_id == "mtplx":
        from .providers.lifecycle import stop as lifecycle_stop

        result = lifecycle_stop("mtplx")
        if not result.ok:
            raise LocalControlError(f"failed to stop {model_id}: {result.error}")
    else:
        try:
            stop_provider(provider_id)
        except BenchmarkError as exc:
            raise LocalControlError(f"failed to stop {model_id}: {exc}") from exc

    with locked_state(state_path) as fresh:
        existing = fresh.models.get(model_id)
        if existing is not None and existing.running:
            fresh.models[model_id] = replace(existing, running=False)
    return StopResult(stopped_model_id=model_id)


def stop_all_local_models(state_path: Path | None = None) -> list[str]:
    """Stop every currently-running local model and clear all their
    flags. Returns the sorted list of model ids that were stopped."""
    state = load_state(state_path)
    running_ids = sorted(mid for mid, s in state.models.items() if s.running)
    if not running_ids:
        return []
    try:
        stop_all_local_providers()
    except BenchmarkError as exc:
        raise LocalControlError(f"failed to stop local models: {exc}") from exc
    with locked_state(state_path) as fresh:
        for mid in running_ids:
            existing = fresh.models.get(mid)
            if existing is not None and existing.running:
                fresh.models[mid] = replace(existing, running=False)
    return running_ids
```

Add `running_model_ids`, appended near `inventory_local_models`:

```python
def running_model_ids(registry: Registry, state: StateStore, state_path: Path | None = None) -> list[str]:
    """Every local model flagged running AND confirmed by a live probe —
    the same self-healing rule every other consumer (the TUI indicator,
    wt's picker) applies. A flagged-but-dead entry is opportunistically
    cleared. Sorted for stable display."""
    providers_by_id = _provider_by_id(registry)
    models_by_id = {m.id: m for m in registry.models}
    verified: list[str] = []
    for model_id, model_state in state.models.items():
        if not model_state.running:
            continue
        model = models_by_id.get(model_id)
        if model is None:
            continue
        provider = providers_by_id.get(model.provider_id)
        probe_origin = base_origin(provider.auth.base_url) if provider and provider.auth else None
        if _probe_running(model.provider_id, model.model_name, probe_origin):
            verified.append(model_id)
        else:
            _clear_stale_running_flag(model_id, state_path)
    return sorted(verified)
```

Update `inventory_local_models`'s `running` computation (currently reads the single `running_marker`) to use the same probe-per-model rule instead:

```python
    downloaded: list[InventoryEntry] = []
    not_downloaded: list[str] = []
    for model in sorted(local_models, key=lambda m: m.id):
        if model.id not in presence.on_disk:
            not_downloaded.append(model.id)
            continue
        running = False
        if state.get(model.id).running:
            provider = providers_by_id.get(model.provider_id)
            probe_origin = base_origin(provider.auth.base_url) if provider and provider.auth else None
            running = _probe_running(model.provider_id, model.model_name, probe_origin)
        size = presence.on_disk[model.id]
        if size is None:
            size = state.get(model.id).size_bytes
        downloaded.append(InventoryEntry(model_id=model.id, running=running, size_bytes=size))
```

(Delete the now-unused `running_marker = state.local.running_model` line above this loop.)

- [ ] **Step 4: Run tests to verify pass**

Run: `cd modelman && uv run pytest tests/test_local_control.py -v`
Expected: PASS, full file. If any pre-existing test still references the retired single-marker API, fix that test's assertions to the new per-model shape (do not skip or delete a test to make it pass — every test that exercised real behavior must keep exercising equivalent behavior under the new schema).

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/local_control.py modelman/tests/test_local_control.py
git commit -m "feat(local-control): same-provider-only start/stop, per-model running flags - completes plan item #3"
```

---

## Task 4: modelman CLI — `start` warns, `stop <id>`/`stop --all`

**Files:**
- Modify: `modelman/src/modelman/main.py`
- Test: `modelman/tests/commands/test_local_control.py`

**Interfaces:**
- Consumes: `local_control.stop_local_model(model_id, state_path=None)`, `stop_all_local_models(state_path=None)`, `running_model_ids(registry, state)`, `StartResult.other_running` (Task 3).

- [ ] **Step 1: Write failing CLI tests**

Add to `modelman/tests/commands/test_local_control.py` (follow the file's existing `CliRunner`/`monkeypatch.setenv` style shown above):

```python
def test_start_command_warns_but_proceeds_when_others_running(tmp_path, monkeypatch):
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\nlocation = "local"\n'
        'auth = { type = "none" }\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n\n'
        '[[models]]\nid = "ollama/y"\nfamily = "y"\nprovider_id = "ollama"\nmodel_name = "y"\n'
    )
    state_path = tmp_path / "modelman.toml"
    state_path.write_text('[model_state."ollama/y"]\nready = true\nrunning = true\n')
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    result = runner.invoke(app, ["start", "ollama/x"])
    assert result.exit_code == 0, result.stdout
    assert "ollama/y" in result.output  # warning names the other running model
    assert load_state(path=state_path).get("ollama/x").running is True


def test_stop_command_with_id_stops_one(tmp_path, monkeypatch):
    state_path = tmp_path / "modelman.toml"
    state_path.write_text(
        '[model_state."ollama/x"]\nready = true\nrunning = true\n\n'
        '[model_state."ollama/y"]\nready = true\nrunning = true\n'
    )
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    with patch("modelman.local_control._stop_ollama_model") as mock_stop:
        result = runner.invoke(app, ["stop", "ollama/x"])
    assert result.exit_code == 0, result.stdout
    mock_stop.assert_called_once_with("x")
    state = load_state(path=state_path)
    assert state.get("ollama/x").running is False
    assert state.get("ollama/y").running is True


def test_stop_command_all_stops_everything(tmp_path, monkeypatch):
    state_path = tmp_path / "modelman.toml"
    state_path.write_text(
        '[model_state."ollama/x"]\nready = true\nrunning = true\n\n'
        '[model_state."ollama/y"]\nready = true\nrunning = true\n'
    )
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    with patch("modelman.local_control.stop_all_local_providers"):
        result = runner.invoke(app, ["stop", "--all"])
    assert result.exit_code == 0, result.stdout
    state = load_state(path=state_path)
    assert state.get("ollama/x").running is False
    assert state.get("ollama/y").running is False


def test_stop_command_bare_errors(tmp_path, monkeypatch):
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))
    result = runner.invoke(app, ["stop"])
    assert result.exit_code == 1
    assert "model id" in result.output.lower() or "--all" in result.output
```

- [ ] **Step 2: Run to verify failure**

Run: `cd modelman && uv run pytest tests/commands/test_local_control.py -k "warns_but_proceeds or stops_one or all_stops or bare_errors" -v`
Expected: FAIL — `stop` doesn't accept a `model_id` argument yet, `--all` unrecognized.

- [ ] **Step 3: Implement the CLI changes**

Edit `modelman/src/modelman/main.py`. Update the import line to pull in the new functions:

```python
from .local_control import (
    DiscoveredModelNeedsFamily,
    LocalControlError,
    LocalModelInventory,
    inventory_local_models,
    start_local_model,
    stop_all_local_models,
    stop_local_model,
)
```

Replace the `start` command's success-reporting tail (after the `while True:` loop that calls `start_local_model`) to print the other-running warning:

```python
    if result.already_running:
        typer.echo(f"{result.model_id} is already running.")
    else:
        typer.echo(f"Started {result.model_id}.")
    if result.other_running:
        typer.echo(
            f"warning: {len(result.other_running)} other local model(s) already running: "
            f"{', '.join(result.other_running)}",
            err=True,
        )
    for warning in result.warnings:
        typer.echo(f"warning: {warning}", err=True)
```

Replace the `stop` command entirely:

```python
@app.command()
def stop(
    model_id: str | None = typer.Argument(None, help="Registry model id to stop."),
    all_: bool = typer.Option(False, "--all", help="Stop every currently-running local model."),
) -> None:
    """Stop one running local model (by id), or every one of them with
    --all. Bare `modelman stop` with neither is a usage error."""
    if model_id is None and not all_:
        typer.echo("error: pass a model id, or --all to stop every running local model", err=True)
        raise typer.Exit(1)
    if model_id is not None and all_:
        typer.echo("error: pass either a model id or --all, not both", err=True)
        raise typer.Exit(1)

    if all_:
        try:
            stopped = stop_all_local_models()
        except LocalControlError as exc:
            typer.echo(f"error: {exc}", err=True)
            raise typer.Exit(1) from exc
        if not stopped:
            typer.echo("No local model is running.")
        else:
            typer.echo(f"Stopped {len(stopped)} model(s): {', '.join(stopped)}.")
        return

    try:
        result = stop_local_model(model_id)
    except LocalControlError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
    if result.stopped_model_id is None:
        typer.echo(f"{model_id} is not running.")
    else:
        typer.echo(f"Stopped {result.stopped_model_id}.")
```

- [ ] **Step 4: Run tests to verify pass**

Run: `cd modelman && uv run pytest tests/commands/test_local_control.py -v`
Expected: PASS, full file. Fix any pre-existing test in this file that called `modelman stop` expecting the old bare-stops-everything behavior — update it to pass `--all` (per the approved spec, bare `stop` is now a usage error, not a silent behavior change).

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/main.py modelman/tests/commands/test_local_control.py
git commit -m "feat(cli): modelman stop <id>/--all, start warns on other running models - completes plan item #4"
```

---

## Task 5: modelman TUI — RUNNING column, `s` keybinding, confirm dialog

**Files:**
- Modify: `modelman/src/modelman/screens/models.py`
- Test: `modelman/tests/screens/test_models.py` (open first to match its `App.run_test()`/`pilot` conventions, shown in `modelman/CLAUDE.md`'s "Testing patterns")

**Interfaces:**
- Consumes: `local_control.start_local_model`, `stop_local_model`, `running_model_ids` (Task 3); `forms.ConfirmModal(message: str) -> bool | None` (existing).
- Produces: `ModelScreen.action_toggle_running()` (new); a `RUNNING` table column.

**Architecture note for this task:** unlike `ready`/`expose`, which are queued and only take effect when the TUI exits (`ConfirmExitDialog` → `apply()`), start/stop have real, immediate side effects — a subprocess warmup that can take up to several minutes. This task follows the `_run_reconcile` background-worker pattern (`run_worker(..., thread=True)`), not the queue pattern: pressing `s` runs the start/stop call on a background thread and updates the table when it completes, with a "starting…"/"stopping…" notification in between. It is deliberately NOT added to `queued_ready`/`queued_exposes` or `ConfirmExitDialog`'s pending-changes summary.

- [ ] **Step 1: Write failing tests for the new column and keybinding**

The test file is `modelman/tests/screens/test_models.py` (not `test_models_screen.py`). It already has `_open_model_screen(pilot)` (waits for `ModelScreen` to mount) and `_seed_registry_and_state(tmp_path, monkeypatch, *, models=())` (writes registry.toml/modelman.toml and redirects `MODELMAN_REGISTRY`/`MODELMAN_STATE`) — reuse both rather than inventing new fixture helpers. `DataTable` rows are read with `mt.get_row_at(i)`, a plain list indexed by the `add_columns` order — after Task 5 Step 3 adds RUNNING, the index order is `FAMILY=0, PROVIDER=1, MODEL=2, LOC=3, STATUS=4, EXPOSED=5, RUNNING=6, COST=7, SIZE=8`.

Add to `modelman/tests/screens/test_models.py`:

```python
@pytest.mark.asyncio
async def test_running_column_shows_dash_when_not_running(tmp_path, monkeypatch):
    # RUNNING column must default to "-" for a local model that has never
    # been started, so the operator can tell at a glance what's live.
    model = ModelEntry(id="ollama/a", family="ornith", provider_id="ollama", model_name="a", location="local")
    _seed_registry_and_state(tmp_path, monkeypatch, models=[model])

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        mt = app.screen.query_one("#model-table", DataTable)
        row = [str(c) for c in mt.get_row_at(0)]
        assert row[6] == "-"


@pytest.mark.asyncio
async def test_action_toggle_running_starts_a_stopped_ready_model(tmp_path, monkeypatch):
    from modelman.local_control import StartResult

    model = ModelEntry(id="ollama/a", family="ornith", provider_id="ollama", model_name="a", location="local")
    reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[model])
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True, running=False))
    save_state(state, state_path)

    started = {}

    def fake_start(registry, model_id, state_path=None, **kwargs):
        started["id"] = model_id
        return StartResult(model_id=model_id, already_running=False, other_running=[])

    monkeypatch.setattr("modelman.screens.models.start_local_model", fake_start)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        await pilot.pause()  # let reconcile settle
        await pilot.press("s")
        for _ in range(200):
            await pilot.pause()
            if started.get("id") == "ollama/a":
                break
    assert started["id"] == "ollama/a"


@pytest.mark.asyncio
async def test_action_toggle_running_shows_confirm_dialog_when_others_running(tmp_path, monkeypatch):
    from modelman.screens.forms import ConfirmModal

    model = ModelEntry(id="ollama/a", family="ornith", provider_id="ollama", model_name="a", location="local")
    reg_path, state_path = _seed_registry_and_state(tmp_path, monkeypatch, models=[model])
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True, running=False))
    state.set("ollama/other", ModelState(ready=True, running=True))
    save_state(state, state_path)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        await pilot.pause()  # let reconcile settle
        await pilot.press("s")
        await pilot.pause()
        assert isinstance(app.screen, ConfirmModal)
```

Add `StateStore, save_state` to this test file's existing `from modelman.state import ModelState, StateStore, load_state, save_state` import line if any of those names are not already imported there (check first — `ModelState`/`load_state` are very likely already present given earlier tests in this file use them; `save_state` may not be).

- [ ] **Step 2: Run to verify failure**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -k running -v`
Expected: FAIL — no `RUNNING` column (index error or wrong value), no `s` binding, `action_toggle_running` doesn't exist, `modelman.screens.models.start_local_model` not patchable (not imported there yet).

- [ ] **Step 3: Implement the TUI changes**

Edit `modelman/src/modelman/screens/models.py`.

Add the new import:

```python
from ..local_control import LocalControlError, running_model_ids, start_local_model, stop_local_model
```

Add the binding:

```python
    BINDINGS = [
        ("escape", "back", "Back"),
        ("a", "add_model", "Add"),
        ("d", "delete_model", "Delete"),
        ("e", "edit_model", "Edit"),
        Binding("enter", "select_row", "Edit", priority=True),
        ("l", "toggle_litellm", "LiteLLM"),
        ("r", "toggle_ready", "Toggle ready"),
        ("x", "toggle_expose", "Toggle exposed"),
        ("s", "toggle_running", "Start/stop"),
    ]
```

Add the column, in `on_mount`:

```python
        mt.add_columns(
            "FAMILY",
            "PROVIDER",
            "MODEL",
            "LOC",
            "STATUS",
            "EXPOSED",
            "RUNNING",
            "COST",
            "SIZE",
        )
```

In `_load_models`'s `_repopulate` closure, compute the RUNNING cell and add it to the row (insert right after computing `exposed_str`, and add the new positional value to `mt.add_row`):

```python
                is_local = is_local_location(m.location) or is_local_location(
                    self.registry.provider(m.provider_id).location
                    if any(p.id == m.provider_id for p in self.registry.providers)
                    else None
                )
                running_str = "●" if (is_local and self.state.get(m.id).running) else "-"
```

```python
                mt.add_row(
                    m.family,
                    m.provider_id,
                    m.model_name,
                    _format_location(m.location),
                    status,
                    exposed_str,
                    running_str,
                    _format_per_token(m.cost),
                    size_str,
                    key=m.id,
                )
```

Note: this cell shows the persisted flag, not a fresh probe — probing every local row on every table repaint would be slow and blocking on the main thread. `_run_reconcile`'s existing background worker is the natural place to also refresh running flags; extend it (Step 3a below) so the column self-heals the same way `ready`/`disk_path` already do.

**Step 3a — extend `_run_reconcile` to refresh running flags:**

```python
    def _run_reconcile(self) -> None:
        reconcile_model_state(self.registry.models, self.registry, self.state)
        verified = set(running_model_ids(self.registry, self.state, self.state_path))
        for model_id, model_state in list(self.state.models.items()):
            if model_state.running and model_id not in verified:
                self.state.models[model_id] = replace(model_state, running=False)
        self.app.call_from_thread(self.reload)
```

(Add `from dataclasses import replace` to the top of the file if not already imported.)

**Step 3b — the toggle action itself**, appended after `action_toggle_expose`:

```python
    def action_toggle_running(self) -> None:
        entry = self._current_entry()
        if entry is None:
            return
        if not is_local_location(
            entry.location
            or next((p.location for p in self.registry.providers if p.id == entry.provider_id), None)
        ):
            self.app.notify("Only local models can be started/stopped")
            return
        if not self._is_ready(entry.id):
            self.app.notify("Model is not ready — mark it ready first")
            return
        mid = entry.id
        currently_running = self.state.get(mid).running

        if currently_running:
            self.app.notify(f"Stopping {mid}…")
            self.run_worker(lambda: self._do_stop(mid), thread=True, exclusive=False)
            return

        others = [m for m in self.state.models if m != mid and self.state.models[m].running]
        if others:
            message = (
                f"{len(others)} other local model(s) already running: {', '.join(sorted(others))}.\n"
                f"Start {mid} anyway?"
            )
            self.app.push_screen(ConfirmModal(message), lambda ok: self._on_start_confirmed(mid, ok))
        else:
            self._on_start_confirmed(mid, True)

    def _on_start_confirmed(self, model_id: str, confirmed: bool | None) -> None:
        if not confirmed:
            return
        self.app.notify(f"Starting {model_id}…")
        self.run_worker(lambda: self._do_start(model_id), thread=True, exclusive=False)

    def _do_start(self, model_id: str) -> None:
        try:
            result = start_local_model(self.registry, model_id, self.state_path)
        except LocalControlError as exc:
            self.app.call_from_thread(self.app.notify, f"Failed to start {model_id}: {exc}", severity="error")
            return
        self.state = load_state(self.state_path)
        message = f"{model_id} is already running." if result.already_running else f"Started {model_id}."
        self.app.call_from_thread(self.app.notify, message)
        self.app.call_from_thread(self.reload)

    def _do_stop(self, model_id: str) -> None:
        try:
            result = stop_local_model(model_id, self.state_path)
        except LocalControlError as exc:
            self.app.call_from_thread(self.app.notify, f"Failed to stop {model_id}: {exc}", severity="error")
            return
        self.state = load_state(self.state_path)
        message = f"Stopped {model_id}." if result.stopped_model_id else f"{model_id} was not running."
        self.app.call_from_thread(self.app.notify, message)
        self.app.call_from_thread(self.reload)
```

Add the needed imports: `from ..state import load_state` (check whether `load_state` is already imported at the top — it is, per the file's existing `from ..state import ModelState, StateStore, load_state, locked_state` line — no change needed there) and `from .forms import ConfirmModal` alongside the existing `from .forms import default_form_kind` import.

- [ ] **Step 4: Run tests to verify pass**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -v`
Expected: PASS, full file.

- [ ] **Step 5: Run the full modelman test suite**

Run: `cd modelman && make check && uv run pytest -q`
Expected: `make check` (lint+typecheck) exits 0; the full pytest run passes (~1056+ tests, allow a couple minutes).

- [ ] **Step 6: Commit**

```bash
git add modelman/src/modelman/screens/models.py modelman/tests/screens/test_models.py
git commit -m "feat(tui): RUNNING column and s keybinding for local model start/stop - completes plan item #5"
```

---

## Task 6: wt `internal/config` — plural running-model schema

**Files:**
- Modify: `wt/internal/config/modelman.go`
- Modify: `wt/internal/config/config.go`
- Test: `wt/internal/config/modelman_test.go`
- Test: `wt/internal/config/modelman_fixture_test.go`
- Test: `wt/internal/config/config_test.go`

**Interfaces:**
- Produces: `Config.RunningLocalModelIDs() []string` (replaces `LocalRunningModel() string`); `Config.FilterToRunningLocal(models []Model, verifiedRunningIDs []string) []Model` (signature change: second param is now a slice of ids, kept as running iff `m.ID` is IN that slice, instead of equal to one id); `Config.SetLocalRunningForTest(runningModelIDs ...string)` (signature change: variadic; a lone `""` still means "gate active, nothing running" since empty strings are filtered out when building the set).

- [ ] **Step 1: Write failing tests for the new schema decode**

Edit `wt/internal/config/modelman_fixture_test.go`'s `TestLoadModelmanStateMatchesSharedFixture`: replace the `localRunning` assertion block:

```go
	if localRunning != "ollama/contract-fixture:local" {
		t.Errorf("localRunning = %q, want %q", localRunning, "ollama/contract-fixture:local")
	}
```

with an assertion against the new per-model field (the fixture, updated in Task 2, now carries `running = true` on `ollama/contract-fixture:local`):

```go
	if !models["ollama/contract-fixture:local"].Running {
		t.Error("expected ollama/contract-fixture:local to have running=true")
	}
	if models["ollama/contract-fixture:subscription"].Running {
		t.Error("expected ollama/contract-fixture:subscription to have running=false (default)")
	}
```

Update the function's signature-matching call site: `models, litellm, localRunning, err := loadModelmanState()` becomes `models, litellm, err := loadModelmanState()` (the function no longer returns a third string — see Step 3).

Add tests to `wt/internal/config/modelman_test.go` (open it first to match its existing table/fixture style) for a model_state entry with no `running` key (defaults false) and one with `running = true`.

- [ ] **Step 2: Run to verify failure**

Run: `cd wt && go test ./internal/config/... -run TestLoadModelmanStateMatchesSharedFixture -v`
Expected: FAIL — `models["ollama/contract-fixture:local"].Running` doesn't compile (`ExposureEntry` has no `Running` field yet) / `loadModelmanState` still returns 4 values.

- [ ] **Step 3: Implement the schema change**

Edit `wt/internal/config/modelman.go`.

Add `Running` to `ExposureEntry`:

```go
// ExposureEntry is the decoded-in-memory representation of a single model
// state entry for wt's exposure predicate and the local-running gate.
type ExposureEntry struct {
	Exposed bool
	Ready   bool
	// Running is modelman's per-model "I started this and haven't stopped
	// it" flag (2026-09-14 multi-model local lifecycle design). It is a
	// HINT, never ground truth by itself — internal/localgate verifies it
	// with a live probe before trusting it. Meaningless for cloud models
	// (modelman never sets it there).
	Running bool
}
```

Update `modelmanState`'s `ModelState` struct field and drop `Local` entirely:

```go
type modelmanState struct {
	PriceRefreshLastRun string `toml:"price_refresh_last_run"`
	ModelState          map[string]struct {
		Exposed        bool `toml:"exposed"`
		LitellmExposed bool `toml:"litellm_exposed"` // back-compat read
		Ready          bool `toml:"ready"`
		Downloaded     bool `toml:"downloaded"`
		Running        bool `toml:"running"`
	} `toml:"model_state"`
	Litellm LitellmState `toml:"litellm"`
}
```

(Delete the `Local struct { RunningModel string \`toml:"running_model"\` } \`toml:"local"\`` field and its doc comment entirely — a stale `[local]` table in an old file simply decodes into nothing now, matching the Python side's behavior.)

Update `loadModelmanState`'s signature and body:

```go
// loadModelmanState reads modelman.toml and returns the exposure map (now
// carrying each model's Running flag) and the [litellm] routing state. A
// missing file returns empty values.
func loadModelmanState() (map[string]ExposureEntry, LitellmState, error) {
	path := ModelmanPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]ExposureEntry{}, LitellmState{}, nil
	}
	if err != nil {
		return nil, LitellmState{}, fmt.Errorf("read modelman.toml: %w", err)
	}
	var s modelmanState
	if err := toml.Unmarshal(data, &s); err != nil {
		return nil, LitellmState{}, fmt.Errorf("parse modelman.toml: %w", err)
	}
	out := make(map[string]ExposureEntry, len(s.ModelState))
	for id, st := range s.ModelState {
		out[id] = ExposureEntry{
			Exposed: st.Exposed || st.LitellmExposed,
			Ready:   st.Ready || st.Downloaded,
			Running: st.Running,
		}
	}
	return out, s.Litellm, nil
}
```

Now find every caller of `loadModelmanState()` (`grep -n "loadModelmanState(" wt/internal/config/*.go`) — this will include `finalizeCfg` in `config.go`. Update that call site to match the new 3-value return and to populate the new running-ids field (Step 4 below defines it).

- [ ] **Step 4: Update `config.go`'s running-model storage and accessors**

Edit `wt/internal/config/config.go`.

Replace the `localRunning`/`localGateActive` fields on `Config`:

```go
	// runningLocal is the set of local model ids modelman's per-model
	// `running` flag names (UNVERIFIED — internal/localgate probes each
	// one before trusting it). localGateActive is true only for a Config
	// built by Load() (see finalizeCfg) — a hand-built Config{} literal
	// (nearly every pre-issue-#65 test) leaves it false, so the gate this
	// field pair drives is a no-op for those tests unless they explicitly
	// opt in via SetLocalRunningForTest.
	runningLocal    map[string]bool `toml:"-"`
	localGateActive bool            `toml:"-"`
```

Find `finalizeCfg` (`grep -n "func finalizeCfg" wt/internal/config/config.go`) and update the block that currently sets `cfg.localRunning`/`cfg.localGateActive` from `loadModelmanState`'s old 3-return-value call — change it to build the map from every `ExposureEntry` with `Running: true`:

```go
	models, litellm, err := loadModelmanState()
	if err != nil {
		return nil, err
	}
	cfg.exposed = models
	cfg.litellm = litellm
	cfg.runningLocal = make(map[string]bool)
	for id, entry := range models {
		if entry.Running {
			cfg.runningLocal[id] = true
		}
	}
	cfg.localGateActive = true
```

(The exact surrounding lines will differ slightly from this sketch — open `finalizeCfg`'s current body first and adapt this block into its existing structure and error-handling shape rather than replacing the whole function.)

Replace `FilterToRunningLocal`:

```go
// FilterToRunningLocal narrows models to those launchable under the
// multi-model local lifecycle (2026-09-14 design): cloud and native
// models pass through unchanged; a local model is kept only when its id
// is in verifiedRunningIDs — the set internal/localgate has already
// live-probed, not the raw persisted flag. A model whose location cannot
// be resolved (a registry data gap) is dropped — the gate must fail
// closed. A no-op (models returned unchanged) when the gate is not
// active (LocalGateActive) — every pre-issue-#65 test that builds a
// Config{} literal directly is unaffected.
func (c *Config) FilterToRunningLocal(models []Model, verifiedRunningIDs []string) []Model {
	if !c.localGateActive {
		return models
	}
	running := make(map[string]bool, len(verifiedRunningIDs))
	for _, id := range verifiedRunningIDs {
		running[id] = true
	}
	out := make([]Model, 0, len(models))
	for _, m := range models {
		if loc, err := c.ResolveLocation(m); err != nil || (loc == LocationLocal && !running[m.ID]) {
			continue
		}
		out = append(out, m)
	}
	return out
}
```

Replace `LocalRunningModel`/`LocalGateActive`/`SetLocalRunningForTest`:

```go
// RunningLocalModelIDs returns the UNVERIFIED set of local model ids
// modelman's per-model `running` flag names — the registry ids
// `modelman start` has flagged and `modelman stop` hasn't cleared. A
// caller that needs to know whether any of them is actually serving
// right now should probe them (internal/localgate.ResolveAll).
func (c *Config) RunningLocalModelIDs() []string {
	ids := make([]string, 0, len(c.runningLocal))
	for id := range c.runningLocal {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// LocalGateActive reports whether the local-model running gate
// (FilterToRunningLocal, and the -M pin checks in cmd/wt/resolve.go and
// internal/tui) is live for this Config. True only for a Config built by
// Load() (production). A hand-built Config{} literal — the shape nearly
// every pre-issue-#65 test uses — defaults to false, so the gate is a
// no-op for those tests unless they call SetLocalRunningForTest.
func (c *Config) LocalGateActive() bool { return c.localGateActive }

// SetLocalRunningForTest activates the local-model running gate (as
// Load() would) and sets the (unverified) running-model id set, as if
// finalizeCfg had read modelman.toml's per-model running flags. No
// arguments (or only empty strings) simulates "gate active, nothing
// running". Tests only.
func (c *Config) SetLocalRunningForTest(runningModelIDs ...string) {
	c.localGateActive = true
	c.runningLocal = make(map[string]bool)
	for _, id := range runningModelIDs {
		if id != "" {
			c.runningLocal[id] = true
		}
	}
}
```

Add `"sort"` to the file's imports if not already present.

- [ ] **Step 5: Run to verify pass**

Run: `cd wt && go build ./... && go test ./internal/config/... -v`
Expected: compile succeeds; existing tests that used the old single-id `FilterToRunningLocal`/`SetLocalRunningForTest` signatures will now FAIL TO COMPILE — fix each one now (this step and the next are one unit of work since Go won't let you run tests with a compile error):

In `wt/internal/config/config_test.go`, update every `FilterToRunningLocal(models, "ollama/a")`-shaped call to `FilterToRunningLocal(models, []string{"ollama/a"})`, and every `FilterToRunningLocal(models, "")` to `FilterToRunningLocal(models, nil)`. Update `TestSetLocalRunningForTestActivatesGate`'s and `TestLocalRunningModel`-shaped assertions from `cfg.LocalRunningModel() != "ollama/x"` to `cfg.RunningLocalModelIDs()` containing `"ollama/x"` (e.g. `!slices.Contains(cfg.RunningLocalModelIDs(), "ollama/x")` — add `"slices"` to the test file's imports if not already present).

Run: `cd wt && go build ./... && go test ./internal/config/... -v`
Expected: PASS, full package.

- [ ] **Step 6: Commit**

```bash
git add wt/internal/config/modelman.go wt/internal/config/config.go wt/internal/config/modelman_test.go wt/internal/config/modelman_fixture_test.go wt/internal/config/config_test.go
git commit -m "feat(config): plural running-local-model schema - completes plan item #6"
```

---

## Task 7: wt `internal/localgate` — verify-all, silent-drop semantics

**Files:**
- Modify: `wt/internal/localgate/localgate.go`
- Test: `wt/internal/localgate/localgate_test.go`

**Interfaces:**
- Consumes: `config.Config.RunningLocalModelIDs()`, `FilterToRunningLocal(models, verifiedIDs)` (Task 6).
- Produces: `ResolveAll(cfg *config.Config) []string` (replaces `Resolve`, returns every verified-running id, never errors — a flagged-but-dead id is simply excluded); `Apply(cfg *config.Config, models []config.Model, pinned string) Result` (drops the `error` return — see rationale below); `Result.PinnedRejected` is now the ONLY failure signal callers need to check.

**Design change from the spec, made concrete here:** issue #65's `Resolve`/`Apply` returned a fatal `error` whenever the single marker was stale, because ANY drift meant "the one local model wt could offer is gone." With multiple models, one drifting out should not block launches that don't need it (per the approved spec's wt section, "silently excluded... not fatal"). So `Apply` no longer has a fatal-error return path at all — only `PinnedRejected` remains as a per-launch rejection, exactly the same as today's pinned-model behavior.

- [ ] **Step 1: Write failing tests for the new plural probe and non-fatal semantics**

Read `wt/internal/localgate/localgate_test.go` first to copy its exact `httptest.Server`/`SetOmlxProbeURLForTest` fixture conventions (shown partially above). Add:

```go
// TestResolveAllReturnsEveryVerifiedRunningModel checks that ResolveAll
// probes every flagged model independently and returns only the ones
// that actually answer - the foundation of multi-model concurrency: one
// drifted flag must not hide a model that's genuinely still serving.
func TestResolveAllReturnsEveryVerifiedRunningModel(t *testing.T) {
	omlxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"model-a"}]}`)
	}))
	defer omlxSrv.Close()
	defer SetOmlxProbeURLForTest(omlxSrv.URL)()

	cfg := &config.Config{}
	cfg.SetLocalRunningForTest("omlx/model-a", "omlx/model-b")
	models := []config.Model{
		{ID: "omlx/model-a", ProviderID: "omlx", ModelName: "model-a", Location: config.LocationLocal},
		{ID: "omlx/model-b", ProviderID: "omlx", ModelName: "model-b", Location: config.LocationLocal},
	}
	cfg.SetModelsForTest(models) // add this test setter if the package needs one - check config.go for an existing equivalent first

	got := ResolveAll(cfg)
	if len(got) != 1 || got[0] != "omlx/model-a" {
		t.Errorf("ResolveAll() = %v, want [omlx/model-a] (model-b flagged but not verified)", got)
	}
}

// TestApplyNeverErrorsOnDriftedFlag checks the deliberate relaxation from
// issue #65: a flagged-but-unverified local model is silently excluded
// from the eligible list, never a fatal error for the whole launch.
func TestApplyNeverErrorsOnDriftedFlag(t *testing.T) {
	defer SetOmlxProbeURLForTest("http://127.0.0.1:1")() // nothing listens here
	cfg := &config.Config{}
	cfg.SetLocalRunningForTest("omlx/qwen3.8")
	models := []config.Model{{ID: "omlx/qwen3.8", ProviderID: "omlx", Location: config.LocationLocal}}
	result := Apply(cfg, models, "")
	if result.PinnedRejected != nil {
		t.Errorf("PinnedRejected = %v, want nil (no pin was set)", result.PinnedRejected)
	}
	if len(result.Eligible) != 0 {
		t.Errorf("Eligible = %v, want empty (the only local model failed its probe)", result.Eligible)
	}
}

// TestApplyPinnedRejectedStillFatalForThatLaunch checks the one surviving
// failure mode: a -M pin naming a specific local model that fails its
// probe is still rejected, even though a non-pinned launch would just
// silently drop it - an explicit request can't be silently substituted.
func TestApplyPinnedRejectedStillFatalForThatLaunch(t *testing.T) {
	defer SetOmlxProbeURLForTest("http://127.0.0.1:1")()
	cfg := &config.Config{}
	cfg.SetLocalRunningForTest("omlx/qwen3.8")
	models := []config.Model{{ID: "omlx/qwen3.8", ProviderID: "omlx", Location: config.LocationLocal}}
	result := Apply(cfg, models, "omlx/qwen3.8")
	var notRunning *NotRunningError
	if !errors.As(result.PinnedRejected, &notRunning) {
		t.Fatalf("PinnedRejected = %v, want *NotRunningError", result.PinnedRejected)
	}
}
```

(If `config.Config` does not already have a way to inject a models list for a hand-built test `Config{}` in this package's existing tests — check `localgate_test.go`'s current tests for how they already construct `cfg`/`models` for `Apply` calls, shown partially above; they pass `models` as `Apply`'s second argument directly rather than through the Config, so `SetModelsForTest` is very likely unnecessary — drop that line and construct `models` as a plain local slice passed straight to `ResolveAll`/`Apply`, matching the existing file's pattern exactly.)

- [ ] **Step 2: Run to verify failure**

Run: `cd wt && go build ./... 2>&1 | head -30`
Expected: compile errors — `Resolve`/`Apply`'s old signatures, `ResolveAll` undefined.

- [ ] **Step 3: Implement the rewrite**

Edit `wt/internal/localgate/localgate.go`. Replace `Resolve` with `ResolveAll`:

```go
// ResolveAll returns every local model id modelman's per-model running
// flag names (Config.RunningLocalModelIDs) that ALSO passes a live
// availability probe right now. A flagged-but-dead id is simply excluded
// — never an error — since one drifted model must not block a launch
// that doesn't need it. A flagged id absent from cfg.Models (a registry
// data gap) is excluded the same way: an unidentifiable model can't be
// name-checked-probed.
func ResolveAll(cfg *config.Config) []string {
	flagged := cfg.RunningLocalModelIDs()
	verified := make([]string, 0, len(flagged))
	for _, id := range flagged {
		idx := config.IndexModelByID(cfg.Models, id)
		if idx < 0 {
			continue
		}
		if Available(cfg.Models[idx]) {
			verified = append(verified, id)
		}
	}
	return verified
}
```

Replace `Result` and `Apply`:

```go
// Result is Apply's outcome: the verified-running local model ids, the
// eligible list after the gate's filter, and — when pinned (-M) named an
// otherwise-eligible local model that isn't in the verified set — the
// pinned-local rejection. Callers map PinnedRejected to their own UX (the
// non-TUI path treats it as fatal; the TUI routes back to the agent
// picker with the message as status).
type Result struct {
	VerifiedRunning []string
	Eligible        []config.Model
	PinnedRejected  error
}

// Apply is the multi-model local-running gate policy (2026-09-14 design)
// in one place, shared by cmd/wt/resolve.go (non-TUI launch) and
// internal/tui's enterModelPhase so the two launch paths can never
// diverge: verify every flagged local model, reject a pinned local model
// that isn't among the verified ones, then narrow models to
// cloud-plus-every-verified-running-local. Never returns a fatal error —
// a flagged-but-unverified model is silently excluded (see
// TestApplyNeverErrorsOnDriftedFlag); PinnedRejected is the only rejection
// a caller must still handle. pinned is the -M flag value ("" = not
// pinned).
func Apply(cfg *config.Config, models []config.Model, pinned string) Result {
	verified := ResolveAll(cfg)
	res := Result{VerifiedRunning: verified, Eligible: models}
	if !cfg.LocalGateActive() {
		return res
	}
	if pinned != "" {
		if idx := config.IndexModelByID(models, pinned); idx >= 0 {
			pm := models[idx]
			isVerified := false
			for _, id := range verified {
				if id == pm.ID {
					isVerified = true
					break
				}
			}
			// Fail closed on an unresolvable location (a registry data
			// gap): treat the model as local, so a pin it names is
			// rejected rather than silently allowed.
			if loc, lerr := cfg.ResolveLocation(pm); (lerr != nil || loc == config.LocationLocal) && !isVerified {
				res.PinnedRejected = &NotRunningError{ModelID: pm.ID}
			}
		}
	}
	res.Eligible = cfg.FilterToRunningLocal(models, verified)
	return res
}
```

`NotRunningError` and `Available` stay unchanged.

- [ ] **Step 4: Run tests to verify pass**

Run: `cd wt && go build ./... && go test ./internal/localgate/... -v`
Expected: compiles; every OTHER existing test in this file that called the old `Resolve`/single-error-returning `Apply` will now fail to compile — fix each one now (same "one unit of work" note as Task 6 Step 5): every `gate, gateErr := Apply(...)` call site in this test file becomes `gate := Apply(...)` (no second return value), and any assertion on a returned `error` from `Apply` moves to asserting on `gate.PinnedRejected` instead where that was the intent, or is deleted where the old assertion covered the now-removed "stale single marker is fatal" behavior (that case no longer applies — replace it with an assertion that `gate.Eligible` is simply empty, matching `TestApplyNeverErrorsOnDriftedFlag` above).

Run again: `cd wt && go test ./internal/localgate/... -v`
Expected: PASS, full package.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/localgate/localgate.go wt/internal/localgate/localgate_test.go
git commit -m "feat(localgate): verify every running local model, silent-drop on drift - completes plan item #7"
```

---

## Task 8: wt call sites — `cmd/wt/resolve.go`, `internal/tui/app.go`

**Files:**
- Modify: `wt/cmd/wt/resolve.go`
- Modify: `wt/internal/tui/app.go`
- Test: `wt/cmd/wt/resolve_test.go`
- Test: `wt/cmd/wt/main_test.go`
- Test: `wt/internal/tui/local_gate_test.go`

**Interfaces:**
- Consumes: `localgate.Apply(cfg, eligible, pinned) Result` (Task 7, no longer returns a second `error`).

- [ ] **Step 1: Update `resolve.go`**

Edit `wt/cmd/wt/resolve.go`. Replace the gate-application block inside `resolveModel`:

```go
	// Local-model running gate (2026-09-14 design) — the policy lives in
	// localgate.Apply, shared with the TUI's enterModelPhase. Apply never
	// returns a fatal error now: a flagged-but-unverified local model is
	// silently excluded, and only a -M pin naming a specific unverified
	// local model is rejected (gate.PinnedRejected).
	gate := localgate.Apply(cfg, eligible, pinned)
	if gate.PinnedRejected != nil {
		return config.Model{}, gate.Eligible, gate.PinnedRejected
	}
	eligible = gate.Eligible
```

(This replaces the old five-line block that computed `gate, gateErr := localgate.Apply(...)` and returned early on `gateErr != nil` — delete that early-return branch entirely, since there is no longer a `gateErr` to check.)

The rest of `resolveModel` (the `len(eligible) == 0` branch, `LocalGateActive()` check, error message) is unchanged — it already only depends on `eligible`/`cfg.LocalGateActive()`, not on the old `gateErr`.

- [ ] **Step 2: Update `internal/tui/app.go`**

Edit `wt/internal/tui/app.go`. Replace the gate-application block inside `enterModelPhase`:

```go
	// Local-model running gate (2026-09-14 design) — the policy lives in
	// localgate.Apply, shared with cmd/wt/resolve.go's resolveModel. Apply
	// never returns a fatal error now — a flagged-but-unverified local
	// model is silently excluded from the eligible list rather than
	// quitting the whole program; only a -M pin naming an unverified
	// local model routes back to the agent picker with a message.
	gate := localgate.Apply(m.cfg, models, m.pinnedModel)
	models = gate.Eligible
```

(This replaces the old `gate, gateErr := localgate.Apply(...)` plus the `if gateErr != nil { m.fatalErr = gateErr; return m, tea.Quit }` block — delete that fatal-quit branch entirely. The `model.fatalErr` field itself may still be used elsewhere in this file for other genuinely-fatal conditions; do not remove the field, only this one call site's use of it.)

The rest of `enterModelPhase` (the `routeBack` closure, the `gate.PinnedRejected != nil` check, the pinned-not-in-eligible check, the `len(models) == 0` check) is unchanged.

- [ ] **Step 3: Run to see compile/test failures across the three test files**

Run: `cd wt && go build ./... && go vet ./...`
Expected: compiles clean (production code). Now:

Run: `cd wt && go test ./cmd/wt/... ./internal/tui/... -v 2>&1 | head -100`
Expected: several FAILs in `resolve_test.go`, `main_test.go`, `local_gate_test.go` — every `var notRunning *localgate.NotRunningError; if !errors.As(err, &notRunning)`-shaped assertion that used to check `resolveModel`'s returned `error` for a stale-marker case must move to checking `gate.PinnedRejected` via the function's actual return, and every `cfg.SetLocalRunningForTest("omlx/qwen3.8")` single-arg call already compiles fine under Task 6's new variadic signature (no change needed there) — but any test that asserted the OLD fatal-on-drift behavior (a non-pinned launch erroring because the single marker was stale) needs its expectation flipped to "silently empties the eligible list, no error" per Task 7's `TestApplyNeverErrorsOnDriftedFlag`.

- [ ] **Step 4: Fix each failing test**

Work through `wt/cmd/wt/resolve_test.go`, `wt/cmd/wt/main_test.go`, and `wt/internal/tui/local_gate_test.go` one function at a time:

- Any test named around "stale marker fatal" / asserting `resolveModel`'s returned error `errors.As`s to `*localgate.NotRunningError` for a NON-pinned scenario: change the assertion to `err == nil` and check the model list is empty instead (the "all eligible models are local and no local model is running" error from `resolveModel`'s own `len(eligible) == 0` branch still fires in that case — assert on THAT message, which is unchanged from today, not on a `NotRunningError`).
- Any test for the PINNED scenario (a `-M` pin naming an unverified/not-running local model): keep the `errors.As(err, &notRunning)` assertion — this path is unchanged; only the pin case still produces `*localgate.NotRunningError`, now via `gate.PinnedRejected` surfacing through `resolveModel`'s existing `if gate.PinnedRejected != nil { return ..., gate.PinnedRejected }` line from Step 1.
- `main_test.go`'s equivalent (around line 716-729 per the earlier grep) follows the same split: check whether that specific test pins a model (keep the `NotRunningError` assertion) or doesn't (flip to "empty eligible list, no error").

- [ ] **Step 5: Run the full wt test suite**

Run: `cd wt && go build ./... && go vet ./... && go test ./... -v 2>&1 | tail -80`
Expected: `go build`/`go vet` clean; every package PASS.

- [ ] **Step 6: Commit**

```bash
git add wt/cmd/wt/resolve.go wt/internal/tui/app.go wt/cmd/wt/resolve_test.go wt/cmd/wt/main_test.go wt/internal/tui/local_gate_test.go
git commit -m "feat(wt): adopt non-fatal multi-model local gate at both launch call sites - completes plan item #8"
```

---

## Task 9: Docs — CLAUDE.md updates

**Files:**
- Modify: `modelman/CLAUDE.md`
- Modify: `wt/CLAUDE.md`
- Modify: `CLAUDE.md` (repo root)

**Interfaces:** none (documentation only).

- [ ] **Step 1: Update `modelman/CLAUDE.md`**

In the "Project overview" paragraph and the "Local-model lifecycle (issue #65)" section, replace every reference to "the single local model wt's picker may offer" / "`[local].running_model`" with a description of the per-model `running` flag and same-provider-only start/stop, and update the pointer from the retired 2026-09-10 spec to the new one:

```markdown
### Local-model lifecycle (multi-model design, 2026-09-14)

`modelman start <model_id>` / `modelman stop <model_id>` / `modelman stop
--all`, and the modelman TUI's `s` keybinding, are the sanctioned ways to
start or stop a local model for normal (non-benchmark) usage — see
`src/modelman/local_control.py` and
`../docs/superpowers/specs/2026-09-14-local-model-lifecycle-design.md`
(monorepo-root docs; supersedes the retired single-marker design in
`../docs/superpowers/specs/2026-09-10-one-local-model-at-a-time-design.md`).
Multiple local models may run concurrently, subject to real per-provider
process limits: ollama is multi-tenant (many models may be flagged
running at once); omlx, mtplx, and mlx_lm_server are single-model-per-
process, so starting a different model on one of them replaces whatever
it was already running. Each model's `running` flag lives in
`modelman.toml`'s per-model `[model_state."<id>"]` block (`src/modelman/
state.py`'s `ModelState.running`) — a HINT, never trusted by itself: every
reader (the TUI's RUNNING column, `modelman start`'s other-running
warning, wt's picker) verifies it with a live probe first. wt reads the
flags read-only (`wt/internal/config/modelman.go`,
`wt/internal/localgate`) to filter its model picker to cloud models plus
every verified-running local model — see `wt/CLAUDE.md`'s "Local-model
gate" section.
```

Also update the "Project overview" paragraph's parenthetical describing `start`/`stop` (currently "the single local model wt's picker may offer") to "a local model wt's picker may offer alongside any other currently-running local model."

- [ ] **Step 2: Update `wt/CLAUDE.md`**

Replace the "Local-model gate (issue #65)" section:

```markdown
## Local-model gate (multi-model design, 2026-09-14)

wt offers cloud models plus every LOCAL model that is both flagged
`running` in modelman-owned `modelman.toml` and confirmed by a live
probe right now — replacing issue #65's single-marker,
one-model-at-a-time gate. The gate policy lives in ONE place —
`internal/localgate.Apply` — shared by `cmd/wt/resolve.go`'s
`resolveModel` (non-TUI) and `internal/tui`'s `enterModelPhase` (TUI), so
the two launch paths cannot diverge. Apply reads modelman-owned
`modelman.toml`'s per-model `running` flags (`internal/config`'s
`Config.RunningLocalModelIDs()`/`LocalGateActive()`), verifies each one
with `internal/localgate.ResolveAll`'s NAME-CHECKED probe (ollama via
`ollamacheck.Loaded` — `ollama ps`, the loaded set, not `ollama list`'s
downloaded-but-idle catalog; omlx/omlx-6bit via a name-checked
`/v1/models` — 4-bit and 6-bit variants share port 8000 and differ
exactly in the variant tail; mlx_lm_server via a non-empty `/v1/models`,
exact names unreconstructable since one process serves one target+draft
pairing; mtplx via a name-checked `/v1/models` on port 8003), rejects a
`-M` pin naming a local model that isn't among the verified set, and
narrows the list with `Config.FilterToRunningLocal`, which fails closed
on unresolvable locations (a registry data gap drops the model rather
than keeping a possibly-local one). Callers map the outcome to their own
UX:

- No flags set → cloud models only.
- One or more verified flags → cloud models plus every verified-running
  local model.
- A flagged-but-unverified model (crashed, stopped outside modelman, a
  benchmark run tore it down) → silently excluded from the eligible
  list — never fatal, since one drifted model must not block a launch
  that doesn't need it.
- Pinned local model that isn't among the verified set → `*NotRunningError`
  is fatal for THAT launch (non-TUI fatal; TUI routes back to the agent
  picker with the message as status, clearing the bad pin) — the one
  surviving fatal case, since a pin is an explicit request that can't be
  silently substituted.
- Gate empties a non-empty eligible list (every eligible model was local,
  none verified running) → a gate-specific error ("all of agent X's
  eligible models are local and no local model is running — start one
  with `modelman start <id>`"), not the generic "no models match"
  wording; the TUI likewise routes back to the agent picker instead of
  showing a silent empty model list.

`LocalGateActive()` is true only for a `Config` built by `Load()`
(production); a hand-built `Config{}` literal — the shape nearly every
pre-issue-#65 test uses — defaults to false, making the gate a no-op
there unless a test opts in via `SetLocalRunningForTest`.

Start/stop local models with modelman: `modelman start <provider>/<name>`
/ `modelman stop <provider>/<name>` / `modelman stop --all`, or the TUI's
`s` keybinding (see `modelman/CLAUDE.md`).
```

- [ ] **Step 3: Update root `CLAUDE.md`**

In the "Architecture" section's `wt/` bullet, replace "the `[local].running_model` marker behind the one-local-model-at-a-time gate" with "the per-model `running` flags behind the multi-model local-running gate." In the "Key Gotchas" section, replace the "Isolation is mandatory... Only one local model loaded at a time" line to distinguish benchmark isolation (unchanged, still exclusive) from normal usage (now advisory/concurrent):

```markdown
- **Benchmark isolation is still mandatory**: `modelman benchmark` and the legacy benchmark scripts still enforce full exclusivity (only one local model loaded at a time) for clean measurement — local MLX/GGUF models share Apple Silicon GPU/RAM and distort each other's results otherwise. Normal (non-benchmark) usage via `modelman start`/the TUI's `s` keybinding allows multiple local models to run concurrently, subject to real per-provider process limits (see `modelman/CLAUDE.md`'s "Local-model lifecycle" section) — advisory only, not enforced.
```

- [ ] **Step 4: Validate links**

Run: `make check-links`
Expected: exits 0 — confirms the new spec-doc link (`docs/superpowers/specs/2026-09-14-local-model-lifecycle-design.md`) referenced from both CLAUDE.md files resolves.

- [ ] **Step 5: Commit**

```bash
git add modelman/CLAUDE.md wt/CLAUDE.md CLAUDE.md
git commit -m "docs: describe multi-model local lifecycle in CLAUDE.md files - completes plan item #9"
```

---

## Task 10: Final integration verification

**Files:** none (verification only).

**Interfaces:** none.

- [ ] **Step 1: Run the full monorepo verification**

Run: `make test-all`
Expected: exits 0 — `make lint` (shell lint including the Task 1 bash edits, check-links), modelman `make check`/`make test`, wt `go build`/`go vet`/`go test` all pass.

- [ ] **Step 2: Manual smoke check of the bash helper's new verbs**

Run (no live services required to check the arg-parsing/usage-error paths):
```bash
bin/llm-isolate-provider stop unknown-provider; echo "exit=$?"
```
Expected: prints `unknown provider: unknown-provider` to stderr, `exit=1`.

```bash
bin/llm-isolate-provider --solo; echo "exit=$?"
```
Expected: prints the usage line (`usage: llm-isolate-provider [--solo] <provider-id> ...`) to stderr, `exit=1` (no provider given after `--solo` is shifted off).

- [ ] **Step 3: If everything passes, report done — no further commit needed unless Step 1 or 2 required a fix**

If either step required a code change, stage and commit it with a message describing what the final verification caught, e.g.:

```bash
git add -A
git commit -m "fix: address make test-all findings from final integration pass"
```

---
