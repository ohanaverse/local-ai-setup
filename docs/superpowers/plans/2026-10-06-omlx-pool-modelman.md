# omlx Pool, Part 2 (modelman) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `modelman start` and `modelman stop` on an omlx model produce the same pool state and `running` flags as the wt commands, by delegating to wt.

**Architecture:** `wt_bridge.py` gains three calls over `wt start --plan/--json` and `wt stop`. `local_control.py` routes the omlx family's start and stop through them and keeps only the flag bookkeeping. The TUI's `s` confirm dialog takes its omlx text from wt's dry-run plan. modelman's omlx discovery follows omlx's two-level directory rule, as wt's now does.

**Tech Stack:** Python 3 (uv), Textual, pytest. No new dependencies. The `wt` binary on PATH is the build with PR #262 (`wt start --plan/--json`).

**Spec:** [docs/superpowers/specs/2026-10-06-omlx-pool-design.md](../specs/2026-10-06-omlx-pool-design.md), section "modelman". Part 1 (wt) merged as #262. This plan closes #213 and #261.

## Global Constraints

- Run every command from `modelman/`: `uv run pytest …`, `make check`, `make test`.
- The suite never runs the real `wt`, `omlx`, `mtplx` or `ollama`, and never reads `~/.omlx` or `~/.config`. `tests/conftest.py`'s autouse fixtures enforce this; new bridge calls get a stub there.
- modelman never writes LiteLLM's `config.yaml` and never restarts the proxy. It asks wt.
- Benchmark isolation is unchanged: `modelman provider isolate|stop|stop-all|restore` and `modelman benchmark` keep the Python omlx backend, which restarts omlx so exactly one model is loaded.
- mtplx, `mlx_lm_server` and ollama start/stop paths are unchanged, and their test assertions stay as they are.
- A probe that cannot say whether a model is running must never clear its `running` flag (#249).
- modelman's resolved model id and wt's id are the same string for registered and discovered omlx models; ids pass between the two unchanged.
- Docs under `docs/guides/` never embed live model state.
- Before each commit: `make check` passes (ruff, format check, mypy).
- Read `modelman/CLAUDE.md` and `modelman/docs/internals/local-model-lifecycle.md` before starting.

## Deviations from the spec, decided while planning

- **`modelman stop --all` is not changed.** The spec has it call `wt stop omlx`. It already halts the omlx service through `stop_all_local_providers()`, which is the same outcome, so no code changes there.
- **A failed delegated start does not report what omlx unloaded on the way.** wt prints nothing on stdout for a failed start (#260). modelman leaves the affected flags alone; the next `running_model_ids()` probe clears the stale ones.
- **#261 is in this PR** (Task 5). Without it `modelman start` cannot resolve a model stored inside an organization folder, so success criterion 5 fails for such a model.

## Review Focus

1. **wt cannot read the omlx pool (keyed server, no `secret_ref`) when the user stops a model.** Expected: the flag is kept and the error says how to proceed; the flag is never cleared on "cannot say". Pinned in Task 2 (`test_stop_omlx_keeps_the_flag_when_the_pool_cannot_be_read`).
2. **The model was already unloaded (evicted, or its TTL passed) when the user stops it.** Expected: success, flag cleared, no error. Pinned in Task 2 (`test_stop_omlx_clears_the_flag_of_a_model_wt_says_is_not_running`).
3. **omlx is not running at all when the user stops a flagged model.** Expected: success, flag cleared. Pinned in Task 2 (`test_stop_omlx_clears_the_flag_when_omlx_is_down`).
4. **wt returns output modelman cannot parse** (an older wt without `--json`, or empty stdout). Expected: a clear error naming `make install`, no flag change. Pinned in Task 1 (`test_start_rejects_output_it_cannot_parse`).
5. **A start unloads a model that has no flag in `modelman.toml`** (started by wt). Expected: no error and no stray state row. Pinned in Task 2 (`test_start_omlx_ignores_an_unloaded_model_with_no_flag`).

## File Structure

| File | Change | Responsibility |
|---|---|---|
| `modelman/src/modelman/wt_bridge.py` | modify | `start_plan`, `start`, `stop` and their parsers |
| `modelman/src/modelman/local_control.py` | modify | omlx start/stop delegation, flag bookkeeping, simpler `_omlx_loaded` |
| `modelman/src/modelman/main.py` | modify | print what a start unloaded |
| `modelman/src/modelman/screens/models.py` | modify | `s` dialog text from the wt plan |
| `modelman/src/modelman/providers/omlx.py` | modify | two-level discovery; nested lookup for a registered model |
| `modelman/tests/conftest.py` | modify | autouse stub for the new bridge seam |
| `modelman/tests/contracts/test_wt_start_cli_fixture.py` | create | Python half of the `wt start --json` contract |
| `docs/contracts/wt-start-cli.sample.json` | modify | comment names the Python test |

---

### Task 1: Bridge calls and the contract test

**Files:**
- Modify: `modelman/src/modelman/wt_bridge.py`, `modelman/tests/conftest.py`, `docs/contracts/wt-start-cli.sample.json` (the `_comment` only)
- Create: `modelman/tests/contracts/test_wt_start_cli_fixture.py`
- Test: `modelman/tests/test_wt_bridge.py`

**Interfaces:**
- Consumes: `wt start <id> --plan --json`, `wt start <id> [--replace] --json`, `wt stop <id|provider> --yes` (wt, merged in #262). Shapes: `docs/contracts/wt-start-cli.sample.json`.
- Produces:
  - `@dataclass(frozen=True) class PlanUnload: id: str; sessions: int`
  - `@dataclass(frozen=True) class StartPlan: id: str; status: str; would_unload: list[PlanUnload]` — status is `running`, `fits`, `would_unload` or `unknown`
  - `@dataclass(frozen=True) class StartOutcome: id: str; status: str; unloaded: list[str]` — status is `started` or `already_running`
  - `parse_start_plan(stdout: str) -> StartPlan`, `parse_start_outcome(stdout: str) -> StartOutcome` (raise `ValueError` on a shape they do not know)
  - `start_plan(model_id: str, timeout: float = PLAN_TIMEOUT) -> StartPlan | None`
  - `start(model_id: str, *, replace: bool, timeout: float = START_TIMEOUT) -> StartOutcome`
  - `stop(target: str, timeout: float = STOP_TIMEOUT) -> None`
  - seam `_run_wt(argv: list[str], timeout: float) -> subprocess.CompletedProcess[str]`

- [ ] **Step 1: Write the failing contract test**

Create `tests/contracts/test_wt_start_cli_fixture.py`:

```python
"""Cross-language contract: the `wt start <id> --json` shapes modelman parses.

Read together with wt/cmd/wt/start_json_test.go; both consume
docs/contracts/wt-start-cli.sample.json, so a schema change on either side
fails both CI jobs in the same PR."""

import json
from pathlib import Path

import pytest

from modelman.wt_bridge import PlanUnload, parse_start_outcome, parse_start_plan

FIXTURE = Path(__file__).resolve().parents[3] / "docs" / "contracts" / "wt-start-cli.sample.json"


def _shape(key: str) -> str:
    return json.dumps(json.loads(FIXTURE.read_text())[key])


def test_plan_shapes_parse():
    # Pins `wt start <id> --plan --json`: modelman's TUI builds its confirm
    # dialog from this. If wt renames a status or a field, the dialog would
    # stop naming the models a start is about to unload.
    assert parse_start_plan(_shape("plan_running")).status == "running"
    assert parse_start_plan(_shape("plan_fits")).status == "fits"
    assert parse_start_plan(_shape("plan_unknown")).status == "unknown"
    plan = parse_start_plan(_shape("plan_would_unload"))
    assert plan.id == "omlx/B"
    assert plan.status == "would_unload"
    assert plan.would_unload == [PlanUnload(id="omlx/A", sessions=1)]


def test_result_shapes_parse():
    # Pins `wt start <id> --json`: modelman clears the running flag of every
    # id in `unloaded`. A renamed field would leave evicted models reading as
    # running forever.
    started = parse_start_outcome(_shape("started"))
    assert (started.id, started.status, started.unloaded) == ("omlx/B", "started", ["omlx/A"])
    already = parse_start_outcome(_shape("already_running"))
    assert (already.status, already.unloaded) == ("already_running", [])


@pytest.mark.parametrize("bad", ["", "not json", "[]", '{"id": "x"}', '{"id": "x", "status": "nope"}'])
def test_unknown_shapes_are_rejected(bad):
    # An older wt, or one that failed before printing, must read as "no
    # usable answer", never as a start that succeeded or a plan that fits.
    with pytest.raises(ValueError):
        parse_start_plan(bad)
    with pytest.raises(ValueError):
        parse_start_outcome(bad)
```

- [ ] **Step 2: Write the failing bridge tests**

Append to `tests/test_wt_bridge.py`:

```python
def _wt(monkeypatch, returncode=0, stdout="", stderr=""):
    """Stub the bridge's non-litellm seam; returns the argv lists it saw."""
    calls: list[list[str]] = []

    def fake(argv, timeout):
        calls.append(list(argv))
        return subprocess.CompletedProcess(argv, returncode, stdout=stdout, stderr=stderr)

    monkeypatch.setattr(wt_bridge, "_run_wt", fake)
    return calls


def test_start_passes_replace_and_returns_the_outcome(monkeypatch):
    # modelman start delegates to wt; the ids wt says it unloaded are what
    # modelman clears flags for, so they must arrive intact.
    calls = _wt(monkeypatch, stdout='{"id": "omlx/B", "status": "started", "unloaded": ["omlx/A"]}')
    out = wt_bridge.start("omlx/B", replace=True)
    assert calls == [["start", "omlx/B", "--json", "--replace"]]
    assert (out.status, out.unloaded) == ("started", ["omlx/A"])


def test_start_without_replace_omits_the_flag(monkeypatch):
    calls = _wt(monkeypatch, stdout='{"id": "omlx/B", "status": "started", "unloaded": []}')
    wt_bridge.start("omlx/B", replace=False)
    assert calls == [["start", "omlx/B", "--json"]]


def test_start_failure_raises_with_wts_message(monkeypatch):
    # wt prints its error twice (cobra's "Error:" and its own "wt:"); the user
    # must see it once.
    _wt(monkeypatch, returncode=1, stderr="Error: omlx has no room for B\nwt: omlx has no room for B\n")
    with pytest.raises(wt_bridge.WtBridgeError) as exc:
        wt_bridge.start("omlx/B", replace=True)
    assert str(exc.value) == "omlx has no room for B"


def test_start_rejects_output_it_cannot_parse(monkeypatch):
    # An older wt without --json exits 0 and prints prose. Treating that as a
    # start would set a running flag for a model that may not be loaded.
    _wt(monkeypatch, stdout="wt: omlx/B is running\n")
    with pytest.raises(wt_bridge.WtBridgeError, match="make install"):
        wt_bridge.start("omlx/B", replace=True)


def test_start_plan_parses_a_plan_printed_with_exit_1(monkeypatch):
    # `--plan` always exits 0, but the same shape arrives with exit 1 from a
    # refused start; the plan read must not depend on the exit code.
    calls = _wt(
        monkeypatch,
        returncode=1,
        stdout='{"id": "omlx/B", "status": "would_unload", "would_unload": [{"id": "omlx/A", "sessions": 2}]}',
    )
    plan = wt_bridge.start_plan("omlx/B")
    assert calls == [["start", "omlx/B", "--plan", "--json"]]
    assert plan is not None and plan.would_unload[0].sessions == 2


def test_start_plan_is_none_when_wt_gives_no_answer(monkeypatch):
    # A read: every failure is "unknown", never an exception in the TUI.
    _wt(monkeypatch, returncode=1, stderr="wt: unknown model\n")
    assert wt_bridge.start_plan("omlx/B") is None

    def boom(argv, timeout):
        raise wt_bridge.WtNotFoundError("wt not found")

    monkeypatch.setattr(wt_bridge, "_run_wt", boom)
    assert wt_bridge.start_plan("omlx/B") is None


def test_stop_runs_wt_stop_with_yes(monkeypatch):
    # --yes: modelman already asked the user; wt must not prompt on a pipe.
    calls = _wt(monkeypatch, stdout="Stopping omlx/A... done\n")
    wt_bridge.stop("omlx/A")
    assert calls == [["stop", "omlx/A", "--yes"]]


def test_stop_failure_raises_with_wts_message(monkeypatch):
    _wt(monkeypatch, returncode=1, stderr='Error: model "omlx/A" is not running\nwt: model "omlx/A" is not running\n')
    with pytest.raises(wt_bridge.WtBridgeError, match="is not running"):
        wt_bridge.stop("omlx/A")
```

Add `import subprocess` and `import pytest` to that file's imports if they are missing.

- [ ] **Step 3: Run them and confirm they fail**

Run: `uv run pytest tests/contracts/test_wt_start_cli_fixture.py tests/test_wt_bridge.py -q`
Expected: collection error, `cannot import name 'PlanUnload'`.

- [ ] **Step 4: Implement in `wt_bridge.py`**

Update the module docstring's first line to `"""Subprocess bridge to wt: `wt litellm ...`, `wt served`, `wt warm`, and `wt start` / `wt stop` for the omlx pool (#213).` and keep the rest.

Append at the end of the file:

```python
# Above wt's own warmup budget (600s) plus its route write and proxy wait, so
# a model that never loads is wt's failure to report, not a kill from here.
START_TIMEOUT = 660.0
# A dry run: one inventory probe round.
PLAN_TIMEOUT = 30.0
# Above wt's unload budget (its loadTimeout, 300s).
STOP_TIMEOUT = 330.0

_PLAN_STATUSES = frozenset({"running", "fits", "would_unload", "unknown"})
_OUTCOME_STATUSES = frozenset({"started", "already_running"})


@dataclass(frozen=True)
class PlanUnload:
    id: str
    sessions: int


@dataclass(frozen=True)
class StartPlan:
    """`wt start <id> --plan --json`: what a start would do, nothing changed."""

    id: str
    status: str  # running | fits | would_unload | unknown
    would_unload: list[PlanUnload]


@dataclass(frozen=True)
class StartOutcome:
    """`wt start <id> --json` after a start."""

    id: str
    status: str  # started | already_running
    unloaded: list[str]


def _json_object(stdout: str) -> dict:
    try:
        doc = json.loads(stdout)
    except ValueError as exc:
        raise ValueError("not JSON") from exc
    if not isinstance(doc, dict):
        raise ValueError("not a JSON object")
    return doc


def parse_start_plan(stdout: str) -> StartPlan:
    """Parse a plan, raising ValueError for any shape this code does not know
    (pinned by docs/contracts/wt-start-cli.sample.json)."""
    doc = _json_object(stdout)
    status, model_id, rows = doc.get("status"), doc.get("id"), doc.get("would_unload")
    if status not in _PLAN_STATUSES or not isinstance(model_id, str) or not isinstance(rows, list):
        raise ValueError("not a wt start plan")
    unload: list[PlanUnload] = []
    for row in rows:
        if not isinstance(row, dict) or not isinstance(row.get("id"), str):
            raise ValueError("not a wt start plan")
        sessions = row.get("sessions", 0)
        unload.append(PlanUnload(id=row["id"], sessions=sessions if isinstance(sessions, int) else 0))
    return StartPlan(id=model_id, status=status, would_unload=unload)


def parse_start_outcome(stdout: str) -> StartOutcome:
    """Parse a start result, raising ValueError for any unknown shape."""
    doc = _json_object(stdout)
    status, model_id, unloaded = doc.get("status"), doc.get("id"), doc.get("unloaded")
    if (
        status not in _OUTCOME_STATUSES
        or not isinstance(model_id, str)
        or not isinstance(unloaded, list)
        or not all(isinstance(u, str) for u in unloaded)
    ):
        raise ValueError("not a wt start result")
    return StartOutcome(id=model_id, status=status, unloaded=list(unloaded))


def _run_wt(argv: list[str], timeout: float) -> subprocess.CompletedProcess[str]:
    """Run `wt <argv>` and return the finished process. The one seam the
    start/stop calls share, so the suite can keep the real binary out
    (conftest stubs it). Only the subcommand is named in errors."""
    ensure_wt()
    sub = argv[0] if argv else ""
    try:
        return subprocess.run(
            ["wt", *argv],
            capture_output=True,
            text=True,
            encoding="utf-8",
            timeout=timeout,
        )
    except subprocess.TimeoutExpired:
        raise WtBridgeTimeoutError(f"wt {sub} timed out after {timeout:g}s") from None
    except FileNotFoundError:
        raise WtNotFoundError("wt not found on PATH; install it with `make install`") from None
    except OSError as e:
        raise WtBridgeError(f"wt {sub} could not run: {type(e).__name__}") from None


def start_plan(model_id: str, timeout: float = PLAN_TIMEOUT) -> StartPlan | None:
    """What `wt start <model_id>` would do, or None when wt gives no usable
    answer (not installed, timed out, an older wt, an id wt does not know).

    wt is asked because the answer needs omlx's sizes and the registry's key,
    which only wt reads. A read, so nothing is raised: None means "could not
    tell", which no caller may read as "nothing would be unloaded"."""
    try:
        proc = _run_wt(["start", model_id, "--plan", "--json"], timeout)
    except WtBridgeError:
        return None
    try:
        return parse_start_plan(proc.stdout)
    except ValueError:
        return None


def start(model_id: str, *, replace: bool, timeout: float = START_TIMEOUT) -> StartOutcome:
    """Start `model_id` through wt (`wt start <id> --json`), which loads it
    into omlx's pool beside what is loaded and writes its LiteLLM route.

    replace=True lets wt go ahead when the load would unload other models;
    their ids come back in the outcome's `unloaded`. Raises WtBridgeError with
    wt's own message when the model did not start."""
    argv = ["start", model_id, "--json"]
    if replace:
        argv.append("--replace")
    proc = _run_wt(argv, timeout)
    if proc.returncode != 0:
        raise WtBridgeError(_msg(proc, f"wt exited {proc.returncode}"))
    try:
        return parse_start_outcome(proc.stdout)
    except ValueError:
        raise WtBridgeError(
            "wt start gave no usable answer; this wt may predate `wt start --json` "
            "— reinstall it with `make install`"
        ) from None


def stop(target: str, timeout: float = STOP_TIMEOUT) -> None:
    """Stop through wt: a model id unloads that one model from omlx's pool,
    a bare provider (`omlx`) stops the service. `--yes` because the caller
    has already asked the user. Raises WtBridgeError with wt's message."""
    proc = _run_wt(["stop", target, "--yes"], timeout)
    if proc.returncode != 0:
        raise WtBridgeError(_msg(proc, f"wt exited {proc.returncode}"))
```

`_msg` reads stderr first; when wt refuses a start without `--replace` it prints the plan on stdout and the reason on stderr, so the reason is what the user sees.

- [ ] **Step 5: Stub the seam for the whole suite**

In `tests/conftest.py`, inside `_never_call_real_wt`, after `monkeypatch.setattr(wt_bridge, "_run", fake)`:

```python
    def no_wt(argv, timeout):
        raise AssertionError(
            f"unexpected `wt {' '.join(argv[:1])}` in tests: stub wt_bridge.start / "
            "start_plan / stop (or wt_bridge._run_wt) in the test that needs it"
        )

    monkeypatch.setattr(wt_bridge, "_run_wt", no_wt)
```

Add one sentence to that fixture's docstring: `The start/stop calls go through wt_bridge._run_wt, which fails the test loudly unless the test stubs it.`

- [ ] **Step 6: Point the fixture's comment at the Python test**

In `docs/contracts/wt-start-cli.sample.json`, set `_comment` to:

```
"Shared fixture for cross-language contract tests of `wt start <id> --json`. Read by wt/cmd/wt/start_json_test.go (Go) and modelman/tests/contracts/test_wt_start_cli_fixture.py (Python). A schema change on either side fails both CI jobs in the same PR."
```

- [ ] **Step 7: Run and commit**

Run: `uv run pytest tests/contracts tests/test_wt_bridge.py -q && make check`
Expected: PASS.

```bash
git add src/modelman/wt_bridge.py tests/conftest.py tests/test_wt_bridge.py tests/contracts/test_wt_start_cli_fixture.py ../docs/contracts/wt-start-cli.sample.json
git commit -m "feat(modelman): bridge to wt start --plan/--json and wt stop (#213)"
```

---

### Task 2: Start and stop on omlx delegate to wt

**Files:**
- Modify: `modelman/src/modelman/local_control.py` (`StartResult`, `start_local_model`, `stop_local_model`, `same_provider_occupant`), `modelman/src/modelman/main.py` (the `start` command's output)
- Test: `modelman/tests/test_local_control.py`

**Interfaces:**
- Consumes: `wt_bridge.start`, `wt_bridge.stop`, `wt_bridge.served_ids`, `wt_bridge.StartOutcome`, `wt_bridge.WtBridgeError` (Task 1); existing `_clear_running_flag(fresh, model_id)`, `locked_state`, `sync_routes`, `_with_warnings`, `_http_json`, `_DEFAULT_BASE_ORIGIN`, `_OMLX_PROVIDER_IDS`.
- Produces:
  - `StartResult.unloaded: list[str]` — ids wt reported omlx unloaded
  - `_start_omlx_via_wt(resolved_id, state_path, litellm_path) -> StartResult`
  - `_stop_omlx_via_wt(model_id) -> None`
  - `same_provider_occupant(...)` returns `None` for the omlx family

- [ ] **Step 1: Write the failing tests**

Append to `tests/test_local_control.py`. Use the file's existing helpers (`_state_path`, the registry builders); the registry below mirrors the ones already there.

```python
def _omlx_pool_registry() -> Registry:
    return Registry(
        providers=[
            ProviderEntry(id="omlx", name="oMLX", location="local", auth=AuthConfig(type="none"))
        ],
        models=[
            ModelEntry(id="omlx/a", family="f", provider_id="omlx", model_name="org/A-4bit", location="local"),
            ModelEntry(id="omlx/b", family="f", provider_id="omlx", model_name="org/B-4bit", location="local"),
        ],
    )


def _stub_wt_start(monkeypatch, outcome=None, error=None):
    calls: list[tuple[str, bool]] = []

    def fake(model_id, *, replace, timeout=0.0):
        calls.append((model_id, replace))
        if error is not None:
            raise error
        return outcome

    monkeypatch.setattr(local_control.wt_bridge, "start", fake)
    return calls


def test_start_omlx_delegates_to_wt_and_keeps_the_sibling_flag(tmp_path, monkeypatch):
    # #213: omlx is a pool. Starting a second omlx model must leave the first
    # one loaded and flagged; modelman used to stop the omlx service first,
    # taking down a model another session was using.
    state_path = _state_path(tmp_path, {"omlx/a": True})
    calls = _stub_wt_start(monkeypatch, wt_bridge.StartOutcome(id="omlx/b", status="started", unloaded=[]))
    stop_provider = MagicMock()
    monkeypatch.setattr(local_control, "stop_provider", stop_provider)
    monkeypatch.setattr(local_control, "isolate_provider", MagicMock(side_effect=AssertionError("isolate")))

    result = start_local_model(_omlx_pool_registry(), "omlx/b", state_path)

    assert calls == [("omlx/b", True)]
    stop_provider.assert_not_called()
    state = load_state(state_path)
    assert state.get("omlx/a").running and state.get("omlx/b").running
    assert (result.already_running, result.unloaded, result.other_running) == (False, [], ["omlx/a"])


def test_start_omlx_clears_the_flags_of_models_wt_says_it_unloaded(tmp_path, monkeypatch):
    # When the new model does not fit, omlx unloads others and wt reports
    # them. Their flags must go, or they read as running forever and
    # `modelman stop` on them would try to unload a model that is not loaded.
    state_path = _state_path(tmp_path, {"omlx/a": True})
    _stub_wt_start(monkeypatch, wt_bridge.StartOutcome(id="omlx/b", status="started", unloaded=["omlx/a"]))

    result = start_local_model(_omlx_pool_registry(), "omlx/b", state_path)

    state = load_state(state_path)
    assert not state.get("omlx/a").running and state.get("omlx/b").running
    assert (result.unloaded, result.other_running) == (["omlx/a"], [])


def test_start_omlx_ignores_an_unloaded_model_with_no_flag(tmp_path, monkeypatch):
    # wt can unload a model modelman never flagged (one wt itself started).
    # That must not fail the start or leave an empty state row behind.
    state_path = _state_path(tmp_path)
    _stub_wt_start(monkeypatch, wt_bridge.StartOutcome(id="omlx/b", status="started", unloaded=["omlx/Stray-4bit"]))

    start_local_model(_omlx_pool_registry(), "omlx/b", state_path)

    assert "omlx/Stray-4bit" not in load_state(state_path).models


def test_start_omlx_reports_already_running_from_wt(tmp_path, monkeypatch):
    # wt is the one that knows whether the model is loaded; a model it calls
    # already running gets its flag set (it may have been started by wt).
    state_path = _state_path(tmp_path)
    _stub_wt_start(monkeypatch, wt_bridge.StartOutcome(id="omlx/b", status="already_running", unloaded=[]))

    result = start_local_model(_omlx_pool_registry(), "omlx/b", state_path)

    assert result.already_running and load_state(state_path).get("omlx/b").running


def test_start_omlx_failure_changes_no_flag(tmp_path, monkeypatch):
    # A start wt refuses (no room, a refused key) must not flag the model, and
    # must not clear anyone else's flag on a guess.
    state_path = _state_path(tmp_path, {"omlx/a": True})
    _stub_wt_start(monkeypatch, error=wt_bridge.WtBridgeError("omlx has no room for B-4bit"))

    with pytest.raises(LocalControlError, match="failed to start omlx/b: omlx has no room"):
        start_local_model(_omlx_pool_registry(), "omlx/b", state_path)

    state = load_state(state_path)
    assert state.get("omlx/a").running and not state.get("omlx/b").running


def _stub_wt_stop(monkeypatch, error=None, served=()):
    calls: list[str] = []

    def fake(target, timeout=0.0):
        calls.append(target)
        if error is not None:
            raise error

    monkeypatch.setattr(local_control.wt_bridge, "stop", fake)
    monkeypatch.setattr(local_control.wt_bridge, "served_ids", lambda provider, timeout=0.0: served)
    return calls


def test_stop_omlx_unloads_one_model_through_wt(tmp_path, monkeypatch):
    # #213: stopping one omlx model must leave its siblings loaded and
    # flagged. modelman used to run `omlx stop`, halting every loaded model.
    state_path = _state_path(tmp_path, {"omlx/a": True, "omlx/b": True})
    calls = _stub_wt_stop(monkeypatch)
    stop_provider = MagicMock()
    monkeypatch.setattr(local_control, "stop_provider", stop_provider)

    result = stop_local_model("omlx/a", state_path)

    assert calls == ["omlx/a"] and result.stopped_model_id == "omlx/a"
    stop_provider.assert_not_called()
    state = load_state(state_path)
    assert not state.get("omlx/a").running and state.get("omlx/b").running


def test_stop_omlx_clears_the_flag_of_a_model_wt_says_is_not_running(tmp_path, monkeypatch):
    # The model was evicted or timed out since it was flagged. wt can read
    # the pool and it is not there: the goal is met, so the flag goes.
    state_path = _state_path(tmp_path, {"omlx/a": True})
    _stub_wt_stop(monkeypatch, error=wt_bridge.WtBridgeError('model "omlx/a" is not running'), served=["B-4bit"])

    result = stop_local_model("omlx/a", state_path)

    assert result.stopped_model_id == "omlx/a"
    assert not load_state(state_path).get("omlx/a").running


def test_stop_omlx_keeps_the_flag_when_the_pool_cannot_be_read(tmp_path, monkeypatch):
    # #249's rule: "cannot say" is not "stopped". A keyed omlx whose key the
    # registry does not name makes wt call the model not running while it is
    # loaded; clearing the flag would leave a resident model nothing records.
    state_path = _state_path(tmp_path, {"omlx/a": True})
    _stub_wt_stop(monkeypatch, error=wt_bridge.WtBridgeError('model "omlx/a" is not running'), served=None)
    monkeypatch.setattr(local_control, "_http_json", lambda url: {"status": "healthy"})

    with pytest.raises(LocalControlError, match="secret_ref") as exc:
        stop_local_model("omlx/a", state_path)

    assert "modelman stop --all" in str(exc.value)
    assert load_state(state_path).get("omlx/a").running


def test_stop_omlx_clears_the_flag_when_omlx_is_down(tmp_path, monkeypatch):
    # omlx is not answering at all: nothing is loaded, so a flagged model is
    # stopped. The user must be able to clear a flag left by a crashed server.
    state_path = _state_path(tmp_path, {"omlx/a": True})
    _stub_wt_stop(monkeypatch, error=wt_bridge.WtBridgeError('model "omlx/a" is not running'), served=None)
    monkeypatch.setattr(local_control, "_http_json", lambda url: None)

    assert stop_local_model("omlx/a", state_path).stopped_model_id == "omlx/a"
    assert not load_state(state_path).get("omlx/a").running


def test_stop_omlx_surfaces_any_other_wt_failure(tmp_path, monkeypatch):
    # A refused key or a failed unload is a real failure: the model may still
    # be loaded, so the flag stays and the user sees wt's reason.
    state_path = _state_path(tmp_path, {"omlx/a": True})
    _stub_wt_stop(monkeypatch, error=wt_bridge.WtBridgeError("omlx still has A-4bit loaded"), served=["A-4bit"])

    with pytest.raises(LocalControlError, match="failed to stop omlx/a: omlx still has"):
        stop_local_model("omlx/a", state_path)

    assert load_state(state_path).get("omlx/a").running


def test_omlx_has_no_single_occupant(tmp_path):
    # The TUI asks this before a start to warn about a replacement. omlx
    # holds several models, so there is no occupant to name; what a start
    # would unload comes from wt's plan instead.
    state = load_state(_state_path(tmp_path, {"omlx/a": True}))
    assert same_provider_occupant(_omlx_pool_registry(), state, "omlx/b", "omlx") is None
```

Add any imports the file lacks (`wt_bridge` from `modelman`, `same_provider_occupant`, `MagicMock`, `AuthConfig`). If the file's `_state_path` helper takes its flags differently, adapt the calls, not the assertions.

- [ ] **Step 2: Run them and confirm they fail**

Run: `uv run pytest tests/test_local_control.py -q -k "omlx_delegates or wt_says or no_flag or already_running_from_wt or failure_changes_no_flag or stop_omlx or no_single_occupant"`
Expected: FAIL (the start tests hit the old isolate path; `StartResult` has no `unloaded`).

- [ ] **Step 3: Implement the start delegation**

In `local_control.py`:

1. Import the bridge beside the other package imports if it is not already imported: `from . import wt_bridge` (the module already uses `wt_bridge.served_ids`; reuse the existing import).

2. Add to `StartResult`:

```python
    # Ids omlx unloaded to make room for this start, as wt reported them
    # (empty for every provider but omlx).
    unloaded: list[str] = field(default_factory=list)
```

3. In `start_local_model`, directly after the `if model.provider_id == "ollama":` block that calls `_require_ollama_daemon` / `_require_ollama_pulled`, add:

```python
    if model.provider_id in _OMLX_PROVIDER_IDS:
        # omlx is a pool (#213): wt loads the model beside what is loaded,
        # predicts and reports evictions, holds the registry's key, and
        # writes the route. modelman keeps only the flags.
        return _start_omlx_via_wt(resolved_id, state_path, litellm_path)
```

4. Add below `start_local_model`:

```python
def _start_omlx_via_wt(
    resolved_id: str, state_path: Path | None, litellm_path: Path | None
) -> StartResult:
    """Start an omlx model through `wt start <id> --json --replace`.

    --replace because `modelman start` has always replaced without asking on
    the command line; the TUI asks first, from wt's plan. wt reports the ids
    omlx unloaded to make room, and their flags are cleared here — wt did the
    unloading, so this result is the only way modelman learns of it. A start
    wt refuses changes no flag: what omlx may have unloaded on the way is not
    reported for a failed start (#260), and the next running_model_ids()
    probe clears any flag that went stale."""
    try:
        outcome = wt_bridge.start(resolved_id, replace=True)
    except wt_bridge.WtBridgeError as exc:
        raise LocalControlError(
            _with_warnings(
                f"failed to start {resolved_id}: {exc}",
                sync_routes(litellm_path=litellm_path),
            )
        ) from exc
    # wt wrote the route itself; the sync is the same closing step every
    # other start takes, and a no-op here unless something else drifted.
    sync_warnings = sync_routes(litellm_path=litellm_path)
    try:
        with locked_state(state_path) as fresh:
            for unloaded_id in outcome.unloaded:
                if unloaded_id != resolved_id and unloaded_id in fresh.models:
                    _clear_running_flag(fresh, unloaded_id)
            existing = fresh.models.get(resolved_id, ModelState())
            fresh.models[resolved_id] = replace(existing, running=True)
            other_running = sorted(
                mid for mid, s in fresh.models.items() if mid != resolved_id and s.running
            )
    except OSError as exc:
        raise LocalControlError(
            _with_warnings(
                f"{resolved_id} started successfully but its running flag could not be "
                f"persisted: {exc} — wt's picker will not see it as running until this "
                "succeeds",
                sync_warnings,
            )
        ) from exc
    return StartResult(
        model_id=resolved_id,
        already_running=outcome.status == "already_running",
        warnings=sync_warnings,
        other_running=other_running,
        unloaded=list(outcome.unloaded),
    )
```

5. In `same_provider_occupant`, after the `if provider_id == "ollama": return None` line add:

```python
    if provider_id in _OMLX_PROVIDER_IDS:
        # A pool, not a slot (#213): nothing is replaced as a matter of
        # course. What a start would unload is wt's plan (wt_bridge.start_plan).
        return None
```

Update that function's docstring: omlx is no longer a single-port domain; say it returns None for ollama and omlx. In `start_local_model`, the remaining `if occupant is not None and model.provider_id in _OMLX_PROVIDER_IDS:` branch and the `not in _OMLX_PROVIDER_IDS` condition below it are now unreachable for omlx; delete the omlx branch and simplify the condition to `if occupant is not None:`, keeping the mtplx/mlx_lm_server comment. Update `start_local_model`'s docstring: omlx is started through wt and loads beside; mtplx and mlx_lm_server still replace their occupant.

- [ ] **Step 4: Implement the stop delegation**

In `stop_local_model`, replace the `else:` branch (the one that calls `stop_provider(provider_id)`) with:

```python
    elif provider_id in _OMLX_PROVIDER_IDS:
        _stop_omlx_via_wt(model_id)
    else:
        try:
            stop_provider(provider_id)
        except BenchmarkError as exc:
            raise LocalControlError(f"failed to stop {model_id}: {exc}") from exc
```

Add below `stop_local_model`:

```python
# The tail of wt's refusal for a model its inventory does not show running
# (wt/cmd/wt/model_cmds.go runStop: `model "<id>" is not running`, or
# `unknown model "<id>"` for an id with no registry row).
_WT_NOT_RUNNING = ("is not running", "unknown model")


def _stop_omlx_via_wt(model_id: str) -> None:
    """Unload one omlx model through `wt stop <id> --yes`; the service and
    its other loaded models stay up (#213).

    wt refusing because the model "is not running" has two meanings, told
    apart by whether wt can read the pool at all:

    - wt can read it (`wt served omlx` answers): the model really is not
      loaded — evicted, or timed out — so the goal is met.
    - wt cannot (a keyed omlx whose key the registry does not name): the
      model may well be loaded. That is "cannot say", which must never clear
      a flag (#249), so it is an error telling the user how to proceed.
      Unless omlx itself is not answering: then nothing is loaded.

    The health read uses omlx's default origin: this function has only the
    id, and a registry base_url override is rare enough not to load the
    registry for."""
    try:
        wt_bridge.stop(model_id)
        return
    except wt_bridge.WtBridgeError as exc:
        if not any(tail in str(exc) for tail in _WT_NOT_RUNNING):
            raise LocalControlError(f"failed to stop {model_id}: {exc}") from exc
    if wt_bridge.served_ids(_OMLX_FAMILY) is not None:
        return
    if _http_json(f"{_DEFAULT_BASE_ORIGIN[_OMLX_FAMILY]}/health") is None:
        return
    raise LocalControlError(
        f"cannot tell whether {model_id} is loaded: wt could not read the omlx pool. "
        "If the omlx server has an API key, set auth.secret_ref on the registry's omlx "
        "provider so one model can be unloaded; or run `modelman stop --all` to stop "
        "the omlx service."
    )
```

Update `stop_local_model`'s docstring to say an omlx model is unloaded through wt and its siblings stay loaded.

- [ ] **Step 5: Print what a start unloaded**

In `main.py`'s `start` command, after the `if result.already_running: … else: …` block:

```python
    if result.unloaded:
        typer.echo(f"omlx unloaded to make room: {', '.join(result.unloaded)}")
```

Update the command's help/docstring if it says starting an omlx model stops the one running.

- [ ] **Step 6: Update the legacy omlx tests**

Run `uv run pytest tests -q`. Tests that pinned the old omlx behavior now fail. Change them by these rules, and change nothing that asserts mtplx, `mlx_lm_server` or ollama behavior:

| Test pins | Change |
|---|---|
| a start on omlx calls `stop_provider` / `isolate_provider` for an occupant (for example `test_start_replaces_omlx_6bit_occupant_when_starting_plain_omlx`) | Stub `wt_bridge.start`; assert the delegation, that no `stop_provider` ran, and the flags per the stubbed `unloaded`. Keep the test's point where it still holds (an `omlx-6bit` row and an `omlx` row are one server: `unloaded` ids under either spelling are cleared). |
| a discovered omlx artifact is started under the family id (`…omlx_6bit_only_registry_uses_the_family_id`) | Stub `wt_bridge.start`; assert it is called with the family-prefixed id and the flag sits under that id. |
| `modelman stop <omlx id>` calls `stop_provider` | Stub `wt_bridge.stop`; assert it is called with the id. |
| the omlx warmup's keyed fallback (`wt_bridge.warm`) from `modelman start` | That path is now reached only from `modelman provider isolate omlx` and the benchmarks. Keep the test if it drives the backend directly; if it drives `start_local_model`, re-point it at the backend or `isolate_provider`. |
| `same_provider_occupant` returns an omlx occupant | Expect `None`; keep the mtplx and mlx_lm_server cases. |
| TUI tests whose dialog names an omlx occupant | Leave failing for Task 4 only if they are in `tests/screens/`; note them in the report. |

If a failing test fits no rule, stop and report it.

- [ ] **Step 7: Run and commit**

Run: `uv run pytest tests -q --deselect tests/screens && make check`
Expected: PASS. (`tests/screens` is fixed in Task 4; if no screen test fails, run the whole suite.)

```bash
git add src/modelman/local_control.py src/modelman/main.py tests
git commit -m "feat(modelman): omlx start and stop delegate to wt; siblings stay loaded (#213)"
```

---

### Task 3: The omlx probe asks status first

**Files:**
- Modify: `modelman/src/modelman/local_control.py` (`_omlx_loaded`, `_probe_running`'s docstring)
- Test: `modelman/tests/test_local_control.py`

**Interfaces:**
- Consumes: `_omlx_status_loaded(base) -> list[str] | None`, `wt_bridge.served_ids`, `_http_json` (existing).
- Produces: `_omlx_loaded(base, listed)` with the same signature and return type; new order of questions.

- [ ] **Step 1: Write the failing test**

```python
def test_omlx_probe_sees_a_model_that_is_still_loading(monkeypatch):
    # #213 item 6: /health's loaded_count counts finished loads only, so a
    # pool with one model mid-load read as "nothing loaded" and modelman
    # cleared the flag of a model that was on its way up. Status reports
    # is_loading, so it is asked first.
    def fake_json(url):
        if url.endswith("/v1/models/status"):
            return {"models": [{"id": "A-4bit", "loaded": False, "is_loading": True}]}
        if url.endswith("/health"):
            return {"engine_pool": {"loaded_count": 0, "model_count": 1}}
        return None

    monkeypatch.setattr(local_control, "_http_json", fake_json)
    assert local_control._omlx_loaded("http://x", ["A-4bit"]) == ["A-4bit"]


def test_omlx_probe_asks_wt_when_status_is_refused(monkeypatch):
    # A keyed omlx refuses the keyless status read; wt holds the registry's
    # key, so it is asked. When wt cannot say either the answer is None
    # (unknown), which leaves a running flag alone (#249).
    def fake_json(url):
        if url.endswith("/health"):
            return {"engine_pool": {"loaded_count": 1, "model_count": 2}}
        return {"error": {"message": "API key required"}}

    monkeypatch.setattr(local_control, "_http_json", fake_json)
    monkeypatch.setattr(local_control.wt_bridge, "served_ids", lambda p, timeout=0.0: ["B-4bit"])
    assert local_control._omlx_loaded("http://x", ["A-4bit", "B-4bit"]) == ["B-4bit"]
    monkeypatch.setattr(local_control.wt_bridge, "served_ids", lambda p, timeout=0.0: None)
    assert local_control._omlx_loaded("http://x", ["A-4bit", "B-4bit"]) is None


def test_omlx_probe_reads_the_list_on_an_omlx_without_status(monkeypatch):
    # An omlx old enough to have neither /health counts nor a status endpoint:
    # its /v1/models list is all there is, as before.
    monkeypatch.setattr(local_control, "_http_json", lambda url: None)
    assert local_control._omlx_loaded("http://x", ["A-4bit"]) == ["A-4bit"]
```

- [ ] **Step 2: Run and confirm the first fails**

Run: `uv run pytest tests/test_local_control.py -q -k omlx_probe`
Expected: the first test FAILS (returns `[]`).

- [ ] **Step 3: Implement**

Replace `_omlx_loaded`'s body and docstring:

```python
def _omlx_loaded(base: str, listed: list[str]) -> list[str] | None:
    """The omlx models that are loaded or loading, or None when the pool's
    state cannot be determined from here.

    omlx's /v1/models lists every model in its engine pool, loaded or not
    (wt#201), so on its own it reads every omlx model as running while the
    service is up. The questions, in order:

    - /v1/models/status without a key: per model, loaded and is_loading, by
      on-disk id. It answers on an omlx with no API key. A model mid-load
      counts (#213): /health's loaded_count cannot see it.
    - `wt served omlx`: the same read with the registry's key, for an omlx
      that refuses the keyless one. modelman resolves no secret_ref, so this
      is the one probe that shells out.
    - neither answers: an omlx that predates the pool endpoints (no pool
      counts in /health) is read from `listed`, as before. Otherwise None,
      reported as-is (#249): callers that guard a flag leave it alone, because
      a pool that will not say neither confirms nor refutes it."""
    by_status = _omlx_status_loaded(base)
    if by_status is not None:
        return by_status
    by_wt = wt_bridge.served_ids(_OMLX_FAMILY)
    if by_wt is not None:
        return by_wt
    health = _http_json(f"{base}/health")
    pool = health.get("engine_pool") if health else None
    if not isinstance(pool, dict) or not isinstance(pool.get("loaded_count"), int):
        return listed
    return None
```

Update the paragraph of `_probe_running`'s docstring that mentions the partly loaded pool so it matches: `start_local_model` no longer probes omlx (wt decides), and an unknown omlx probe still never clears a flag.

- [ ] **Step 4: Update legacy probe tests**

Tests that pinned the `/health`-first order (zero loaded settles it without asking status; all loaded reads the list) now see status asked first. For each failing test: give its fake a status answer consistent with the pool it models, and keep the assertion about which models read as running. A test whose only point was "status is not asked when the counts settle it" is deleted, with the deletion named in the report.

- [ ] **Step 5: Run and commit**

Run: `uv run pytest tests -q --deselect tests/screens && make check`
Expected: PASS.

```bash
git add src/modelman/local_control.py tests/test_local_control.py
git commit -m "fix(modelman): omlx probe asks status first, so a loading model counts (#213)"
```

---

### Task 4: The TUI asks from wt's plan

**Files:**
- Modify: `modelman/src/modelman/screens/models.py` (`action_toggle_running`, `_lifecycle_worker_busy`, `_do_start`)
- Test: `modelman/tests/screens/test_models.py`

**Interfaces:**
- Consumes: `wt_bridge.start_plan`, `wt_bridge.StartPlan`, `wt_bridge.PlanUnload` (Task 1); `StartResult.unloaded` (Task 2).
- Produces: module function `pool_start_note(plan: wt_bridge.StartPlan | None) -> str`; method `ModelScreen._confirm_start(mid, others, note)`.

- [ ] **Step 1: Write the failing tests**

Append to `tests/screens/test_models.py`:

```python
def test_pool_start_note_names_what_a_start_would_unload():
    # The user is about to give these models up; the dialog must name them,
    # and say when a live wt session is using one.
    plan = wt_bridge.StartPlan(
        id="omlx/b",
        status="would_unload",
        would_unload=[wt_bridge.PlanUnload("omlx/a", 2), wt_bridge.PlanUnload("omlx/c", 0)],
    )
    note = pool_start_note(plan)
    assert "omlx/a (in use by 2 wt session(s))" in note and "omlx/c" in note
    assert "unload" in note


def test_pool_start_note_is_empty_when_the_model_fits():
    # A start that fits beside the loaded models replaces nothing, so the
    # dialog must not warn about a replacement.
    assert pool_start_note(wt_bridge.StartPlan("omlx/b", "fits", [])) == ""
    assert pool_start_note(wt_bridge.StartPlan("omlx/b", "running", [])) == ""


def test_pool_start_note_says_so_when_wt_cannot_tell():
    # No plan, or an unknown one, is not "nothing will be unloaded".
    for plan in (None, wt_bridge.StartPlan("omlx/b", "unknown", [])):
        assert "could not tell" in pool_start_note(plan)
```

Add a pilot test modelled on the file's existing `s`-key tests (it has ones that press `s` and inspect the pushed `ConfirmModal`): an omlx model row, `wt_bridge.start_plan` monkeypatched to return the `would_unload` plan above, press `s`, wait for workers, and assert the modal's message contains `omlx/a` and `Start omlx/b anyway?`. Give it a comment saying it pins that the dialog text for omlx comes from wt's plan, since modelman can no longer name an occupant itself. Import `pool_start_note` from `modelman.screens.models` and `wt_bridge` from `modelman`.

- [ ] **Step 2: Run and confirm they fail**

Run: `uv run pytest tests/screens/test_models.py -q -k "pool_start_note or plan"`
Expected: import error for `pool_start_note`.

- [ ] **Step 3: Implement**

In `screens/models.py`, add at module level (after the imports):

```python
def pool_start_note(plan: wt_bridge.StartPlan | None) -> str:
    """The line the start dialog adds for an omlx model, from wt's dry-run
    plan: which loaded models the start would unload. Empty when the model
    fits beside them. A missing or unknown plan says so — "could not tell"
    is never shown as "nothing will be unloaded"."""
    if plan is None or plan.status == "unknown":
        return "\nwt could not tell which omlx models this would unload."
    if plan.status != "would_unload" or not plan.would_unload:
        return ""
    names = ", ".join(
        f"{u.id} (in use by {u.sessions} wt session(s))" if u.sessions else u.id
        for u in plan.would_unload
    )
    return f"\nStarting this will unload {names} to make room."
```

In `action_toggle_running`, replace everything from `others = [m for m in self.state.models …` to the end of the method with:

```python
        others = [m for m in self.state.models if m != mid and self.state.models[m].running]
        if entry.provider_id in ("omlx", "omlx-6bit"):
            # omlx is a pool (#213): what a start would unload depends on
            # sizes only wt reads, so ask wt for its plan first. It is a
            # subprocess and a probe round, so it runs in a worker.
            self.app.notify(f"Checking what starting {mid} would unload…")
            self.run_worker(
                lambda: self._plan_then_confirm(mid, others),
                thread=True,
                exclusive=False,
                name="model-start-plan",
                description=f"Planning the start of {mid}",
            )
            return
        # Design §4: a same-provider replacement is never a silent surprise.
        # mtplx and mlx_lm_server serve one model per process, so starting
        # this one stops that provider's occupant — name it explicitly.
        occupant = same_provider_occupant(self.registry, self.state, mid, entry.provider_id)
        note = (
            f"\nStarting this will also stop {occupant}, since {entry.provider_id} "
            "can only serve one model at a time."
            if occupant is not None
            else ""
        )
        self._confirm_start(mid, others, note)

    def _plan_then_confirm(self, mid: str, others: list[str]) -> None:
        note = pool_start_note(wt_bridge.start_plan(mid))
        self.app.call_from_thread(self._confirm_start, mid, others, note)

    def _confirm_start(self, mid: str, others: list[str], note: str) -> None:
        """Every start is confirmed (it is slow: a load, a proxy restart);
        the message adds who else is running and what the start displaces."""
        if others or note:
            running = (
                f"{len(others)} other local model(s) already running: "
                f"{', '.join(sorted(others))}."
                if others
                else ""
            )
            message = f"{running}{note}\nStart {mid} anyway?".lstrip("\n")
        else:
            message = f"Start {mid}?"
        self.app.push_screen(ConfirmModal(message), lambda ok: self._on_start_confirmed(mid, ok))
```

In `_lifecycle_worker_busy`, add `"model-start-plan"` to the tuple of worker names, so a second `s` is refused while a plan is being fetched.

In `_do_start`, after the started/already-running notify:

```python
        if result.unloaded:
            self.app.call_from_thread(
                self.app.notify,
                f"omlx unloaded to make room: {', '.join(result.unloaded)}",
                severity="warning",
            )
```

Check `action_back`'s running-worker handling (the force-quit dialog) still reads correctly with the new worker name; it inspects `self.workers` generally, so no change is expected.

- [ ] **Step 4: Fix the screen tests left from Task 2**

Any `tests/screens` test whose dialog named an omlx occupant: stub `wt_bridge.start_plan` and assert the plan-derived text. mtplx and mlx_lm_server dialog tests are unchanged.

- [ ] **Step 5: Run and commit**

Run: `uv run pytest tests -q && make check`
Expected: PASS.

```bash
git add src/modelman/screens/models.py tests/screens
git commit -m "feat(modelman): the start dialog names what omlx would unload, from wt's plan (#213)"
```

---

### Task 5: omlx discovery follows omlx's two-level layout (#261)

**Files:**
- Modify: `modelman/src/modelman/providers/omlx.py` (`list_local`, `_target_dir`)
- Test: `modelman/tests/providers/` (the existing omlx provider test file)

**Interfaces:**
- Produces: `omlx_model_dirs(md: Path) -> list[Path]` in `providers/omlx.py`; `list_local` returns one entry per model with `variant_id` = the model directory's own name.

omlx 0.7.0's rule (`omlx/model_discovery.py`, `discover_models`), for each entry of the model dir that is a directory and does not start with `.`: an entry with `adapter_config.json` is skipped; an entry with `config.json` is a model named after the directory; an HF-hub cache entry (`models--Org--Name/snapshots/…`) is resolved by omlx under its own rules and is not listed here; anything else is an organization folder whose child directories with `config.json` (and no `adapter_config.json`) are models named after the **child**. The first of two equal names wins. If nothing was found and the model dir itself has `config.json`, it is the one model. wt's scan (`wt/internal/localmodels/sources.go`) implements the same rule; the two must list the same names.

- [ ] **Step 1: Write the failing tests**

```python
def _model(path):
    path.mkdir(parents=True)
    (path / "config.json").write_text("{}")
    return path


def test_list_local_finds_models_inside_an_organization_folder(tmp_path):
    # #261, seen on a real machine: omlx stores a download at
    # models/mlx-community/<name>/ and serves it as <name>. modelman listed a
    # model called "mlx-community" and never saw the real one, so it could
    # not be started or registered.
    _model(tmp_path / "Flat-4bit")
    _model(tmp_path / "mlx-community" / "Nested-6bit")
    _model(tmp_path / "mlx-community" / "Other-8bit")
    (tmp_path / "mlx-community" / "notes").mkdir()
    (tmp_path / "empty-folder").mkdir()
    adapter = _model(tmp_path / "Adapter")
    (adapter / "adapter_config.json").write_text("{}")
    _model(tmp_path / ".cache" / "Hidden")
    (tmp_path / "models--Org--Cached" / "snapshots" / "abc").mkdir(parents=True)
    _model(tmp_path / "zz-org" / "Flat-4bit")  # duplicate name: the first wins

    found = {m["variant_id"]: m["path"] for m in OMLXProvider({"model_dir": str(tmp_path)}).list_local()}

    assert sorted(found) == ["Flat-4bit", "Nested-6bit", "Other-8bit"]
    assert found["Nested-6bit"] == str(tmp_path / "mlx-community" / "Nested-6bit")
    assert found["Flat-4bit"] == str(tmp_path / "Flat-4bit")


def test_list_local_reads_a_model_dir_that_is_itself_one_model(tmp_path):
    # omlx accepts a model_dir pointed straight at one model's folder.
    _model(tmp_path / "Solo-4bit")
    assert [m["variant_id"] for m in OMLXProvider({"model_dir": str(tmp_path / "Solo-4bit")}).list_local()] == ["Solo-4bit"]


def test_a_registered_model_inside_an_organization_folder_reads_as_downloaded(tmp_path):
    # A registry entry names a repo; its directory is looked up by the repo's
    # basename. When omlx put that directory inside an organization folder,
    # the entry read as not downloaded although omlx was serving it.
    _model(tmp_path / "mlx-community" / "Nested-6bit")
    provider = OMLXProvider({"model_dir": str(tmp_path)})
    variant = {"id": "omlx/x", "repo": "mlx-community/Nested-6bit"}
    assert provider.is_downloaded(variant)
    assert provider.path_of(variant) == str(tmp_path / "mlx-community" / "Nested-6bit")
```

Match the constructor and variant shapes the existing tests in that file use (`OMLXProvider(options)` and a `VariantSpec` dict).

- [ ] **Step 2: Run and confirm they fail**

Run: `uv run pytest tests/providers -q -k "organization_folder or itself_one_model"`
Expected: FAIL (`mlx-community` is listed; the nested model is not).

- [ ] **Step 3: Implement**

In `providers/omlx.py`, add after `_model_dir`:

```python
def _is_model_dir(path: Path) -> bool:
    return (path / "config.json").is_file() and not (path / "adapter_config.json").exists()


def omlx_model_dirs(md: Path) -> list[Path]:
    """The model directories omlx discovers under `md`, by its two-level rule
    (omlx 0.7.0, model_discovery.discover_models; wt's scan in
    wt/internal/localmodels/sources.go follows the same rule, and the two
    must list the same names):

    - an entry holding config.json is a model, named after the directory;
    - an entry holding adapter_config.json is a LoRA adapter, skipped;
    - any other entry is an organization folder: its child directories that
      hold config.json are models, named after the CHILD;
    - dot-directories are skipped, and the first of two equal names wins;
    - if nothing was found and `md` itself holds config.json, it is the one
      model.

    A Hugging Face cache entry (models--Org--Name/snapshots/...) is not
    listed: omlx names it by its own rules. A missing `md` is no models."""
    if not md.is_dir():
        return []

    def subdirs(path: Path) -> list[Path]:
        return sorted(d for d in path.iterdir() if d.is_dir() and not d.name.startswith("."))

    found: dict[str, Path] = {}
    for entry in subdirs(md):
        if (entry / "adapter_config.json").exists():
            continue
        if _is_model_dir(entry):
            found.setdefault(entry.name, entry)
            continue
        if entry.name.startswith("models--") and (entry / "snapshots").is_dir():
            continue
        for child in subdirs(entry):
            if _is_model_dir(child):
                found.setdefault(child.name, child)
    if not found and _is_model_dir(md):
        found[md.name] = md
    return [found[name] for name in sorted(found)]
```

Replace `list_local`'s body:

```python
    def list_local(self, runner: _Runner | None = None) -> list[LocalModel]:
        return [
            {"variant_id": d.name, "path": str(d), "size_bytes": None}
            for d in omlx_model_dirs(_model_dir(self.config))
        ]
```

In `_target_dir`, replace the `repo` branch:

```python
        repo = variant.get("repo")
        if repo:
            md = _model_dir(self.config)
            flat = md / repo_basename(repo)
            if flat.is_dir():
                return flat
            # omlx may have stored it inside an organization folder (#261):
            # the directory omlx serves under this name is the model's.
            for found in omlx_model_dirs(md):
                if found.name == flat.name:
                    return found
            # Not on disk: the flat path is where a download goes.
            return flat
        return None
```

Read every caller of `_target_dir` in the file (`is_downloaded`, `download`, `size_of`, `path_of`, `delete`, `removable_paths`/`artifact_paths` if present) and confirm each is right for a nested directory. In particular `delete` must remove the model's own directory and never the organization folder. List what you checked in the report.

- [ ] **Step 4: Update fixtures**

Existing tests that build an omlx model directory out of bare directories for `list_local` or discovery now need a `config.json` in each. Add it through a small helper; do not change what those tests assert. Tests of `is_downloaded` / `path_of` on a flat directory are unaffected (the flat path is still checked first).

- [ ] **Step 5: Run and commit**

Run: `uv run pytest tests -q && make check`
Expected: PASS.

```bash
git add src/modelman/providers/omlx.py tests
git commit -m "fix(modelman): find omlx models inside an organization folder (#261)"
```

---

### Task 6: Docs

**Files:**
- Modify: `modelman/docs/internals/local-model-lifecycle.md`, `modelman/CLAUDE.md`, `docs/guides/02-providers-and-models.md`, `docs/guides/04-litellm-config.md`, `CLAUDE.md` (root), `wt/docs/internals/local-models.md` (one clause), `docs/superpowers/specs/2026-10-06-omlx-pool-design.md` (the modelman section)

- [ ] **Step 1: `modelman/docs/internals/local-model-lifecycle.md`**

Edit in place, reading the code first:
- Per-provider limits (top paragraph): omlx is a pool handled through wt; mtplx and mlx_lm_server serve one model per process.
- **Start**: an omlx model goes to `_start_omlx_via_wt` (`wt start <id> --json --replace`); modelman sets the target's flag and clears the flags of `unloaded` ids; a failed start changes no flag (#260). Remove the statement that omlx calls `stop_provider()` first.
- **Stop**: an omlx model goes to `_stop_omlx_via_wt` (`wt stop <id> --yes`); the three meanings of wt's "is not running" and why "cannot say" keeps the flag. `stop --all` still halts the service.
- **Warmup on a keyed omlx**: reached from `modelman provider isolate omlx` and the benchmarks now, not from `modelman start`.
- **Probing**: `_omlx_loaded`'s new order (status, then `wt served`, then the list for an old omlx); `start_local_model` no longer probes omlx.
- **Discovery**: `omlx_model_dirs` and the two-level rule; `_target_dir`'s nested lookup.
- **TUI `s`**: for omlx the dialog text comes from `wt_bridge.start_plan` in a `model-start-plan` worker.

- [ ] **Step 2: `modelman/CLAUDE.md` and root `CLAUDE.md`**

- `modelman/CLAUDE.md`: the local-model lifecycle summary (omlx delegates to wt; needs `wt` with `wt start --json`), and the `conftest.py` note about the `_run_wt` stub.
- Root `CLAUDE.md`: "Benchmark isolation is still mandatory" gotcha's sentence about per-provider limits, and the "Stop mechanisms per backend" entry (`modelman stop <omlx model>` now unloads one model through wt).

- [ ] **Step 3: Guides 02 and 04**

PR #262 wrote that `modelman stop` on omlx still halts the service and that `modelman start` replaces. Both are now false. Find them (`grep -n -i 'modelman stop\|halts\|still' docs/guides/02-providers-and-models.md docs/guides/04-litellm-config.md`) and rewrite: `modelman start`/`stop` on an omlx model behave as `wt start`/`wt stop` do; `modelman stop --all` stops the service. Keep the keyless-pool caveat (wt cannot tell which model is loaded without the key) and name `auth.secret_ref` as the remedy. No live model state.

- [ ] **Step 4: Spec and wt doc**

- Spec, "modelman" section: record the three deviations listed at the top of this plan (`stop --all` unchanged; a failed start reports no `unloaded`; `_stop_omlx_via_wt`'s rule), and that #261 landed here.
- `wt/docs/internals/local-models.md`: where it says modelman's contract test "lands with its omlx delegation", say it exists (`modelman/tests/contracts/test_wt_start_cli_fixture.py`).

- [ ] **Step 5: Check and commit**

Run from the monorepo root: `make lint`
Expected: `ALL LINKS OK`.

```bash
git add -A
git commit -m "docs: modelman delegates omlx start and stop to wt (#213)"
```

---

### Task 7: Verify against the real omlx

The installed `wt` is the #262 build and the registry's omlx provider has a `secret_ref`. Use a scratch `XDG_CONFIG_HOME` holding copies of `agent-wt/config.toml`, `local-ai/registry.toml` and `local-ai/modelman.toml`, and `WT_LITELLM_CONFIG` pointing at a scratch copy of `config.yaml`, with `WT_LITELLM_RESTART_CMD=true`. Both variables are mandatory. Record the real `config.yaml` checksum before and after.

- [ ] **Step 1: Full local verification**

Run from the monorepo root: `make test-all`
Expected: PASS.

- [ ] **Step 2: Start, evict, stop through modelman**

```bash
uv run --directory modelman modelman start omlx/mlx-community--Qwen3.8-27B-4bit
wt served omlx --json                      # expect the 27B
uv run --directory modelman modelman start Qwen3.6-35B-A3B-6bit   # nested, unregistered
# expect "omlx unloaded to make room: omlx/mlx-community--Qwen3.8-27B-4bit"
grep -A2 'model_state' "$XDG_CONFIG_HOME/local-ai/modelman.toml"  # the 35B flagged, the 27B not
wt litellm list | grep omlx                # the 35B only
uv run --directory modelman modelman stop omlx/Qwen3.6-35B-A3B-6bit
wt served omlx --json                      # expect none; omlx /health still 200
```

Record each output. Confirm the pool is empty at the end and the real `config.yaml` checksum is unchanged.

- [ ] **Step 3: Report**

Summarise which of spec criterion 5's parts were verified live and which by tests only (the TUI dialog needs a check by eye), and anything that failed.
