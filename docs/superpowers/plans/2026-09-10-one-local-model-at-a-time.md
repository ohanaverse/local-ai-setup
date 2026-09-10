# One Local Model at a Time Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** modelman becomes the sole owner of the local-model lifecycle (explicit `modelman start <id>` / `modelman stop`), and wt's model picker offers only the single verified-running local model plus cloud models — a hard block by construction, with a clear exit when the marker is stale.

**Architecture:** modelman gains a `[local].running_model` marker in `modelman.toml` (written only by two new CLI commands that delegate the actual stop/start to the existing `bin/llm-isolate-provider` isolation helper). wt reads the marker read-only (extending the existing `loadModelmanState` reader), probes it with a new `internal/localgate` package, and applies a new pure `Config.FilterToRunningLocal` filter at the two places wt resolves a model list: the non-TUI `resolveModel` path and the TUI's `enterModelPhase`. A new `LocalGateActive` bool (true only for a `Config` built by `config.Load()`) makes the gate a no-op for the hundreds of pre-existing wt tests that build `Config{}` literals directly, confining this change's test blast radius to the code paths that actually exercise the gate.

**Tech Stack:** Python 3.13 / Typer / tomllib+tomli_w (modelman), Go 1.26 / Bubble Tea / BurntSushi/toml (wt), bash (`bin/llm-isolate-provider`).

**Spec:** `docs/superpowers/specs/2026-09-10-one-local-model-at-a-time-design.md`

## Global Constraints

- Marker value is the full registry model id `<provider_id>/<model_name>`; both languages split on the **first** `/` only (a model name may itself contain `/`, e.g. `openrouter/z-ai/glm-5.3-flash`).
- `modelman start <id>` rejects cloud models and models whose provider isn't isolatable (`SUPPORTED_PROVIDER_IDS` in `modelman/src/modelman/benchmark/isolation.py`).
- `modelman start` on an already-running model is idempotent (no stop/restart cycle; the marker equality check trusts modelman's own marker, not a fresh probe).
- `modelman stop` is a no-op when nothing is running (marker already clear).
- wt: no marker → only cloud models are offered. Marker present and verified → the marked local model plus cloud models are offered. Marker present and **not** verified (stale) → wt exits with a message naming the fix (`modelman start <id>`).
- A `-M`/`--model` pin naming a local model that isn't the verified running one is rejected with the same message, in both the non-TUI and TUI paths.
- Cloud models are never filtered by this gate.
- Out of scope: the `mtplx` provider (issue #66) — the probe switch is structured so adding it later is one new case; consolidating benchmark isolation into native modelman start/stop.

---

## Part A — modelman (Python)

### Task 1: `StateStore` gains the `[local]` table

**Files:**
- Modify: `modelman/src/modelman/state.py`
- Test: `modelman/tests/test_state.py`

**Interfaces:**
- Produces: `LocalState` dataclass (`running_model: str | None = None`); `StateStore.local: LocalState` field, read by `load_state`/written by `save_state` under the TOML `[local]` table.

- [ ] **Step 1: Write the failing tests**

Add to `modelman/tests/test_state.py` (extend the existing `from modelman.state import (...)` block with `LocalState`):

```python
def test_load_state_reads_local_running_model(tmp_path):
    # modelman.toml's [local].running_model marks the single local model
    # wt's picker may currently offer (issue #65). A missing table must
    # default to None, not raise, since a fresh install has nothing running.
    path = tmp_path / "modelman.toml"
    path.write_text('[local]\nrunning_model = "ollama/qwen3.8:27b-mlx"\n')
    store = load_state(path)
    assert store.local.running_model == "ollama/qwen3.8:27b-mlx"


def test_load_state_missing_local_table_defaults_to_none(tmp_path):
    store = load_state(tmp_path / "nonexistent.toml")
    assert store.local == LocalState()
    assert store.local.running_model is None


def test_save_state_round_trips_local_running_model(tmp_path):
    path = tmp_path / "modelman.toml"
    store = StateStore()
    store.local.running_model = "omlx/qwen3.8"
    save_state(store, path)
    reloaded = load_state(path)
    assert reloaded.local.running_model == "omlx/qwen3.8"


def test_save_state_writes_none_running_model_as_absent(tmp_path):
    # "absent/empty = none running" per the design doc — a cleared marker
    # must not round-trip as the literal string "None" or similar.
    path = tmp_path / "modelman.toml"
    store = StateStore()
    save_state(store, path)
    raw = path.read_text()
    assert "running_model" not in raw
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd modelman && uv run pytest tests/test_state.py -k local_running -v`
Expected: FAIL — `ImportError: cannot import name 'LocalState'` (or `AttributeError: 'StateStore' object has no attribute 'local'`).

- [ ] **Step 3: Implement `LocalState` and wire it into load/save**

In `modelman/src/modelman/state.py`, add the dataclass next to `LitellmState` (after its definition, before `ModelState`):

```python
@dataclass
class LocalState:
    # Registry model id (`<provider_id>/<model_name>`) of the local model
    # `modelman start` last started, or None if `modelman stop` was last
    # run (or neither has ever run). wt reads this read-only to filter its
    # model picker to this one local model plus cloud models — see
    # docs/superpowers/specs/2026-09-10-one-local-model-at-a-time-design.md.
    running_model: str | None = None
```

Add the field to `StateStore`:

```python
@dataclass
class StateStore:
    models: dict[str, ModelState] = field(default_factory=dict)
    families: dict[str, FamilyState] = field(default_factory=dict)
    litellm: LitellmState = field(default_factory=LitellmState)
    local: LocalState = field(default_factory=LocalState)
    extra: dict[str, Any] = field(default_factory=dict, repr=False)
```

In `load_state`, after the `litellm_raw`/`litellm` block:

```python
    local_raw = raw.get("local", {})
    local = LocalState(running_model=local_raw.get("running_model"))
    return StateStore(
        models=models,
        families=families,
        litellm=litellm,
        local=local,
        extra=unknown_keys(raw, {"model_state", "families", "litellm", "local"}),
    )
```

(This replaces the existing final `return StateStore(...)` call — the added arguments are `local=local` and the `"local"` entry in the `unknown_keys` exclusion set.)

In `save_state`, add a `"local"` key to `payload` alongside `"litellm"`:

```python
    payload = {
        "model_state": {
            model_id: drop_none(
                {
                    **s.extra,
                    "ready": s.ready,
                    "disk_path": s.disk_path,
                    "size_bytes": s.size_bytes,
                    "exposed": s.exposed,
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
        "local": drop_none({"running_model": store.local.running_model}),
    }
    atomic_write_toml({**store.extra, **payload}, state_path)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd modelman && uv run pytest tests/test_state.py -v`
Expected: PASS (all tests in the file, including the pre-existing ones).

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/state.py modelman/tests/test_state.py
git commit -m "$(cat <<'EOF'
feat(modelman): add [local].running_model marker to modelman.toml

Adds LocalState/StateStore.local so modelman can record which local model
is currently running (issue #65) - completes plan item #1.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_011vKFaBGEBbzYVoSWX8SxtX
EOF
)"
```

---

### Task 2: `bin/llm-isolate-provider` gains a `stop-all` mode; `isolation.py` gains `stop_all_local_providers()` and an `env` override

**Files:**
- Modify: `bin/llm-isolate-provider`
- Modify: `modelman/src/modelman/benchmark/isolation.py`
- Test: `modelman/tests/benchmark/test_isolation.py`

**Interfaces:**
- Consumes: `stop_all_local` (already defined in `bin/llm-isolate-provider`), `_helper_path`, `IsolateResult` (`modelman/src/modelman/benchmark/isolation.py`).
- Produces: `stop_all_local_providers() -> IsolateResult` and `isolate_provider(provider_id, *extra_args, env=None) -> IsolateResult` (the `env` kwarg is new and optional — existing call sites in `runner.py`/`agent/runner.py` are unaffected).

- [ ] **Step 1: Write the failing tests**

Add to `modelman/tests/benchmark/test_isolation.py`:

```python
def test_stop_all_local_providers_success():
    """`modelman stop` delegates to this to tear down whatever local model
    is running, regardless of which provider it's on — without a stop-all
    mode, modelman would have to know and stop each provider individually,
    duplicating bin/llm-isolate-provider's own stop_all_local logic."""
    with (
        patch("modelman.benchmark.isolation.subprocess.run") as mock_run,
        patch(
            "modelman.benchmark.isolation.shutil.which",
            return_value="/usr/local/bin/llm-isolate-provider",
        ),
    ):
        mock_run.return_value.returncode = 0
        mock_run.return_value.stdout = '{"provider":null,"model":null,"direct_url":null,"ok":true,"error":null}\n'
        mock_run.return_value.stderr = ""
        result = stop_all_local_providers()
        mock_run.assert_called_once_with(
            ["/usr/local/bin/llm-isolate-provider", "stop-all"],
            capture_output=True,
            text=True,
            check=False,
        )
        assert result.ok is True


def test_stop_all_local_providers_failure_raises():
    with (
        patch("modelman.benchmark.isolation.subprocess.run") as mock_run,
        patch(
            "modelman.benchmark.isolation.shutil.which",
            return_value="/usr/local/bin/llm-isolate-provider",
        ),
    ):
        mock_run.return_value.returncode = 1
        mock_run.return_value.stdout = ""
        mock_run.return_value.stderr = "stop failed"
        try:
            stop_all_local_providers()
            raise AssertionError("expected BenchmarkError")
        except BenchmarkError as exc:
            assert "stop failed" in str(exc)


def test_isolate_provider_passes_env_override():
    """`modelman start` sets LLM_ISOLATE_*_MODEL to the exact requested
    model name so the isolation helper warms up THAT model rather than its
    baked-in benchmark default. Without an env override, isolate_provider
    always warmed up whichever model the shell script's own default (or an
    externally-exported env var) named, regardless of the caller's target."""
    with (
        patch("modelman.benchmark.isolation.subprocess.run") as mock_run,
        patch(
            "modelman.benchmark.isolation.shutil.which",
            return_value="/usr/local/bin/llm-isolate-provider",
        ),
        patch.dict("modelman.benchmark.isolation.os.environ", {"EXISTING": "kept"}, clear=True),
    ):
        mock_run.return_value.returncode = 0
        mock_run.return_value.stdout = '{"provider":"ollama","model":"custom:tag","direct_url":"http://localhost:11434/v1/chat/completions","ok":true,"error":null}\n'
        mock_run.return_value.stderr = ""
        isolate_provider("ollama", env={"LLM_ISOLATE_OLLAMA_MODEL": "custom:tag"})
        called_env = mock_run.call_args.kwargs["env"]
        assert called_env["LLM_ISOLATE_OLLAMA_MODEL"] == "custom:tag"
        assert called_env["EXISTING"] == "kept"
```

Also add `stop_all_local_providers` to the `from modelman.benchmark.isolation import (...)` block at the top of the file.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd modelman && uv run pytest tests/benchmark/test_isolation.py -k "stop_all or env_override" -v`
Expected: FAIL — `ImportError: cannot import name 'stop_all_local_providers'`.

- [ ] **Step 3: Add the `stop-all` case arm to the bash script**

In `bin/llm-isolate-provider`, insert a new case arm as the FIRST branch of the existing `case "$PROVIDER" in` statement (before the `ollama)` arm):

```bash
case "$PROVIDER" in
    stop-all)
        # Tear down every local provider with no target kept running —
        # `modelman stop`'s delegate. stop_all_local's "keep" arg is
        # compared with `!=` against each provider name, so an empty
        # string never matches any of them and every branch runs.
        stop_all_local ""
        python3 - <<'PYEOF'
import json
print(json.dumps({"provider": None, "model": None, "direct_url": None, "ok": True, "error": None}))
PYEOF
        exit 0
        ;;
    ollama)
```

Also update the script's header comment (lines 2-4) to mention the new mode:

```bash
#!/bin/bash
# llm-isolate-provider — stop other local providers, start+warmup one.
# Usage: llm-isolate-provider <provider-id>
#        llm-isolate-provider stop-all   (stop every local provider; modelman stop)
# Supported: ollama, omlx, omlx-6bit, mlx_lm_server
```

- [ ] **Step 4: Add `stop_all_local_providers()` and the `env` kwarg to `isolation.py`**

In `modelman/src/modelman/benchmark/isolation.py`, modify `isolate_provider`:

```python
def isolate_provider(provider_id: str, *extra_args: str, env: dict[str, str] | None = None) -> IsolateResult:
    """Delegate service isolation to the local-ai-setup helper.

    `extra_args` is forwarded verbatim, after `provider_id`, to the shell
    helper's argv — e.g. `isolate_provider("mlx_lm_server", target, draft)`.
    mlx_lm_server has no default target/draft pairing in the shell script
    (unlike ollama/omlx, which fall back to a baked-in model name), so the
    pairing must be passed through explicitly on every call.

    `env`, when given, is merged over a copy of the current environment and
    passed to the subprocess — this is how a caller (modelman start) makes
    the helper warm up a *specific* model instead of its baked-in default
    (LLM_ISOLATE_OLLAMA_MODEL etc., see that script's header). Omitted
    entirely from the subprocess.run() call when None, so existing callers
    that never pass env see no change in behavior.
    """
    helper = _helper_path("llm-isolate-provider")
    run_kwargs: dict[str, Any] = {"capture_output": True, "text": True, "check": False}
    if env is not None:
        run_kwargs["env"] = {**os.environ, **env}
    result = subprocess.run([helper, provider_id, *extra_args], **run_kwargs)
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

Add `from typing import Any` to the imports if not already present (check the top of the file — it currently imports `json, os, shutil, subprocess, dataclass`; add `from typing import Any`).

Add the new function after `isolate_provider`, before `restore_providers`:

```python
def stop_all_local_providers() -> IsolateResult:
    """Stop every local provider via the isolation helper's `stop-all`
    mode. Used by `modelman stop` (and by `modelman start` before starting
    a different model) — the single place that knows how to tear down
    whichever local provider happens to be running, without modelman
    having to track that itself."""
    helper = _helper_path("llm-isolate-provider")
    result = subprocess.run(
        [helper, "stop-all"], capture_output=True, text=True, check=False
    )
    if result.returncode != 0:
        raise BenchmarkError(
            f"stop-all failed: {result.stderr.strip() or result.stdout.strip()}"
        )
    try:
        data = json.loads(result.stdout)
    except json.JSONDecodeError as exc:
        raise BenchmarkError(f"isolation helper returned invalid JSON for stop-all: {exc}") from exc
    return IsolateResult(
        provider=data.get("provider") or "stop-all",
        model=data.get("model") or "",
        direct_url=data.get("direct_url") or "",
        ok=data.get("ok", False),
        error=data.get("error"),
    )
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd modelman && uv run pytest tests/benchmark/test_isolation.py -v`
Expected: PASS (all tests, including pre-existing ones — `test_isolate_provider_forwards_extra_args`'s exact `assert_called_once_with` must still pass unchanged, since `env` defaults to `None` and is omitted from the call).

Also run the shell lint: `cd .. && make lint-shell` (from the repo root) — expected: PASS (`bash -n` + `shellcheck --severity=error` on `bin/llm-isolate-provider`).

- [ ] **Step 6: Commit**

```bash
git add bin/llm-isolate-provider modelman/src/modelman/benchmark/isolation.py modelman/tests/benchmark/test_isolation.py
git commit -m "$(cat <<'EOF'
feat(modelman): add stop-all isolation mode and per-call env override

bin/llm-isolate-provider stop-all tears down every local provider (needed
by modelman stop); isolate_provider()'s new env kwarg lets a caller warm up
a specific model instead of the script's baked-in default - completes plan
item #2.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_011vKFaBGEBbzYVoSWX8SxtX
EOF
)"
```

---

### Task 3: `local_control.py` — start/stop orchestration

**Files:**
- Create: `modelman/src/modelman/local_control.py`
- Test: `modelman/tests/test_local_control.py`

**Interfaces:**
- Consumes: `Registry`/`registry.model`/`registry.providers`/`model_has_local_artifact` (`modelman/src/modelman/registry.py`); `StateStore`/`StateStore.local` (Task 1); `SUPPORTED_PROVIDER_IDS`, `isolate_provider`, `stop_all_local_providers`, `mlx_lm_server_pairing_args` (Task 2, `modelman/src/modelman/benchmark/isolation.py`); `BenchmarkError` (`modelman/src/modelman/benchmark/errors.py`).
- Produces: `LocalControlError`, `StartResult(model_id, already_running, direct_url)`, `StopResult(stopped_model_id)`, `start_local_model(registry, state, model_id) -> StartResult`, `stop_local_model(state) -> StopResult`.

- [ ] **Step 1: Write the failing tests**

Create `modelman/tests/test_local_control.py`:

```python
"""Unit tests for modelman.local_control — the start/stop orchestration
behind `modelman start`/`modelman stop` (issue #65). Isolation subprocess
calls are mocked; these tests cover validation, idempotency, and marker
mutation, not bin/llm-isolate-provider itself (see
tests/benchmark/test_isolation.py for that)."""

from unittest.mock import patch

import pytest

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.isolation import IsolateResult
from modelman.local_control import (
    LocalControlError,
    start_local_model,
    stop_local_model,
)
from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry
from modelman.state import StateStore


def _registry() -> Registry:
    return Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", location="local", auth=AuthConfig(type="none")),
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


def test_start_unknown_model_raises():
    with pytest.raises(LocalControlError, match="unknown model"):
        start_local_model(_registry(), StateStore(), "ollama/not-in-registry")


def test_start_cloud_model_rejected():
    # A cloud model can never be "running locally" - modelman start only
    # manages the local-process lifecycle.
    with pytest.raises(LocalControlError, match="cloud model"):
        start_local_model(_registry(), StateStore(), "openrouter/z-ai/glm-5.3-flash")


def test_start_unsupported_provider_rejected():
    # llamacpp is local but retired (issue #33) - not in SUPPORTED_PROVIDER_IDS,
    # so bin/llm-isolate-provider can't isolate it.
    with pytest.raises(LocalControlError, match="cannot be started"):
        start_local_model(_registry(), StateStore(), "llamacpp/retired-model")


def test_start_already_running_is_idempotent():
    state = StateStore()
    state.local.running_model = "ollama/qwen3.8:27b-mlx"
    with patch("modelman.local_control.stop_all_local_providers") as mock_stop, patch(
        "modelman.local_control.isolate_provider"
    ) as mock_isolate:
        result = start_local_model(_registry(), state, "ollama/qwen3.8:27b-mlx")
    assert result.already_running is True
    mock_stop.assert_not_called()
    mock_isolate.assert_not_called()
    assert state.local.running_model == "ollama/qwen3.8:27b-mlx"


def test_start_stops_current_then_starts_requested():
    state = StateStore()
    state.local.running_model = "some/other-model"
    with (
        patch("modelman.local_control.stop_all_local_providers") as mock_stop,
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="ollama",
            model="qwen3.8:27b-mlx",
            direct_url="http://localhost:11434/v1/chat/completions",
            ok=True,
            error=None,
        )
        result = start_local_model(_registry(), state, "ollama/qwen3.8:27b-mlx")
    mock_stop.assert_called_once()
    mock_isolate.assert_called_once_with(
        "ollama", env={"LLM_ISOLATE_OLLAMA_MODEL": "qwen3.8:27b-mlx"}
    )
    assert result.already_running is False
    assert result.direct_url == "http://localhost:11434/v1/chat/completions"
    assert state.local.running_model == "ollama/qwen3.8:27b-mlx"


def test_start_isolate_failure_raises_and_does_not_write_marker():
    state = StateStore()
    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="ollama", model="", direct_url="", ok=False, error="warmup timed out"
        )
        with pytest.raises(LocalControlError, match="warmup timed out"):
            start_local_model(_registry(), state, "ollama/qwen3.8:27b-mlx")
    assert state.local.running_model is None


def test_stop_clears_marker_and_stops():
    state = StateStore()
    state.local.running_model = "ollama/qwen3.8:27b-mlx"
    with patch("modelman.local_control.stop_all_local_providers") as mock_stop:
        result = stop_local_model(state)
    mock_stop.assert_called_once()
    assert result.stopped_model_id == "ollama/qwen3.8:27b-mlx"
    assert state.local.running_model is None


def test_stop_noop_when_nothing_running():
    state = StateStore()
    with patch("modelman.local_control.stop_all_local_providers") as mock_stop:
        result = stop_local_model(state)
    mock_stop.assert_not_called()
    assert result.stopped_model_id is None


def test_start_mlx_lm_server_resolves_pairing_args():
    from modelman.registry import DraftSpec, Fetch

    registry = _registry()
    registry.providers.append(
        ProviderEntry(id="mlx_lm_server", name="mlx-lm server", location="local", auth=AuthConfig(type="none"))
    )
    registry.models.append(
        ModelEntry(
            id="mlx_lm_server/target-repo",
            family="pair",
            provider_id="mlx_lm_server",
            model_name="target-repo",
            fetch=Fetch(repo="org/target-repo"),
            draft=DraftSpec(repo="org/draft-repo"),
        )
    )
    state = StateStore()
    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="mlx_lm_server", model="org/target-repo", direct_url="http://localhost:8001/v1/chat/completions",
            ok=True, error=None,
        )
        start_local_model(registry, state, "mlx_lm_server/target-repo")
    mock_isolate.assert_called_once_with("mlx_lm_server", "org/target-repo", "org/draft-repo", env=None)
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd modelman && uv run pytest tests/test_local_control.py -v`
Expected: FAIL — `ModuleNotFoundError: No module named 'modelman.local_control'`.

- [ ] **Step 3: Implement `local_control.py`**

Create `modelman/src/modelman/local_control.py`:

```python
"""modelman-owned local-model lifecycle control (issue #65 — one local
model at a time). See docs/superpowers/specs/2026-09-10-one-local-model-
at-a-time-design.md.

`modelman start`/`modelman stop` (main.py) are the only place a local
model's process is started or stopped for normal (non-benchmark) usage.
Both delegate the actual stop/start to bin/llm-isolate-provider via
modelman.benchmark.isolation — the same subprocess contract `modelman
benchmark` uses — and record which model is running in modelman.toml's
`[local].running_model` (state.py's LocalState), the marker wt's model
picker reads read-only to filter its catalog to this one local model plus
cloud models.
"""

from __future__ import annotations

from dataclasses import dataclass

from .benchmark.errors import BenchmarkError
from .benchmark.isolation import (
    SUPPORTED_PROVIDER_IDS,
    isolate_provider,
    mlx_lm_server_pairing_args,
    stop_all_local_providers,
)
from .registry import Registry, model_has_local_artifact
from .state import StateStore

# Maps a registry provider_id to the LLM_ISOLATE_*_MODEL env var
# bin/llm-isolate-provider reads for that provider's model name (see that
# script's header comment). mlx_lm_server is absent here — it takes
# target/draft as positional args (mlx_lm_server_pairing_args), not an env
# var.
_ENV_VAR_BY_PROVIDER = {
    "ollama": "LLM_ISOLATE_OLLAMA_MODEL",
    "omlx": "LLM_ISOLATE_OMLX_4BIT_MODEL",
    "omlx-6bit": "LLM_ISOLATE_OMLX_6BIT_MODEL",
}


class LocalControlError(Exception):
    """Raised for user-facing `modelman start`/`modelman stop` failures."""


@dataclass
class StartResult:
    model_id: str
    already_running: bool
    direct_url: str | None = None


@dataclass
class StopResult:
    # The marker that was cleared, or None if nothing was running.
    stopped_model_id: str | None


def start_local_model(registry: Registry, state: StateStore, model_id: str) -> StartResult:
    """Stop whatever local model is running (if any) and start model_id,
    recording it as the new `[local].running_model` marker.

    Raises LocalControlError when model_id is unknown, not a local model,
    or its provider cannot be isolated by bin/llm-isolate-provider.
    Idempotent: if model_id is already the running marker, this is a no-op
    (no stop/restart cycle) — trusts modelman's own marker rather than
    re-probing the provider, since modelman is the one thing that writes it.
    """
    try:
        model = registry.model(model_id)
    except KeyError as exc:
        raise LocalControlError(f"unknown model: {model_id}") from exc

    provider = next((p for p in registry.providers if p.id == model.provider_id), None)
    if not model_has_local_artifact(model, provider):
        raise LocalControlError(f"{model_id} is a cloud model — modelman start only runs local models")

    if model.provider_id not in SUPPORTED_PROVIDER_IDS:
        raise LocalControlError(
            f"provider {model.provider_id!r} cannot be started/stopped by modelman "
            f"(supported: {sorted(SUPPORTED_PROVIDER_IDS)})"
        )

    if state.local.running_model == model_id:
        return StartResult(model_id=model_id, already_running=True)

    try:
        stop_all_local_providers()
    except BenchmarkError as exc:
        raise LocalControlError(f"failed to stop the currently-running local model: {exc}") from exc

    extra_args: tuple[str, ...] = ()
    env: dict[str, str] | None = None
    if model.provider_id == "mlx_lm_server":
        target, draft = mlx_lm_server_pairing_args(
            model.id,
            model.fetch.local_path if model.fetch else None,
            model.fetch.repo if model.fetch else None,
            model.draft.local_path if model.draft else None,
            model.draft.repo if model.draft else None,
        )
        extra_args = (target, draft)
    else:
        env = {_ENV_VAR_BY_PROVIDER[model.provider_id]: model.model_name}

    try:
        result = isolate_provider(model.provider_id, *extra_args, env=env)
    except BenchmarkError as exc:
        raise LocalControlError(f"failed to start {model_id}: {exc}") from exc
    if not result.ok:
        raise LocalControlError(f"failed to start {model_id}: {result.error or 'unknown error'}")

    state.local.running_model = model_id
    return StartResult(model_id=model_id, already_running=False, direct_url=result.direct_url or None)


def stop_local_model(state: StateStore) -> StopResult:
    """Stop the currently-running local model (if any) and clear the
    marker. No-op when nothing is running."""
    running = state.local.running_model
    if not running:
        return StopResult(stopped_model_id=None)
    try:
        stop_all_local_providers()
    except BenchmarkError as exc:
        raise LocalControlError(f"failed to stop {running}: {exc}") from exc
    state.local.running_model = None
    return StopResult(stopped_model_id=running)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd modelman && uv run pytest tests/test_local_control.py -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/local_control.py modelman/tests/test_local_control.py
git commit -m "$(cat <<'EOF'
feat(modelman): add start/stop local-model orchestration (local_control.py)

Validates and isolates one local model at a time, delegating stop/start to
bin/llm-isolate-provider and recording the marker - completes plan item #3.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_011vKFaBGEBbzYVoSWX8SxtX
EOF
)"
```

---

### Task 4: `modelman start` / `modelman stop` CLI commands

**Files:**
- Modify: `modelman/src/modelman/main.py`
- Test: `modelman/tests/commands/test_local_control.py`

**Interfaces:**
- Consumes: `start_local_model`, `stop_local_model`, `LocalControlError` (Task 3); `load_registry` (already imported in main.py); `locked_state` (already imported in main.py).

- [ ] **Step 1: Write the failing tests**

Create `modelman/tests/commands/test_local_control.py`:

```python
"""CLI wiring tests for `modelman start`/`modelman stop` (issue #65).
Orchestration logic itself is covered by tests/test_local_control.py; these
tests only cover argument parsing, exit codes, and that the CLI persists
the marker via the real state file."""

from unittest.mock import patch

from typer.testing import CliRunner

from modelman.local_control import StartResult, StopResult
from modelman.main import app
from modelman.state import load_state

runner = CliRunner()


def test_start_command_success_writes_marker(tmp_path, monkeypatch):
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\nlocation = "local"\n'
        'auth = { type = "none" }\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
    )
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    with patch("modelman.local_control.stop_all_local_providers"), patch(
        "modelman.local_control.isolate_provider"
    ) as mock_isolate:
        from modelman.benchmark.isolation import IsolateResult

        mock_isolate.return_value = IsolateResult(
            provider="ollama", model="x", direct_url="http://localhost:11434/v1/chat/completions",
            ok=True, error=None,
        )
        result = runner.invoke(app, ["start", "ollama/x"])
    assert result.exit_code == 0, result.stdout
    assert load_state(path=state_path).local.running_model == "ollama/x"


def test_start_command_unknown_model_exits_nonzero(tmp_path, monkeypatch):
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text("")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))

    result = runner.invoke(app, ["start", "ollama/does-not-exist"])
    assert result.exit_code == 1
    assert "unknown model" in result.stdout


def test_stop_command_clears_marker(tmp_path, monkeypatch):
    state_path = tmp_path / "modelman.toml"
    state_path.write_text('[local]\nrunning_model = "ollama/x"\n')
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    with patch("modelman.local_control.stop_all_local_providers") as mock_stop:
        result = runner.invoke(app, ["stop"])
    assert result.exit_code == 0, result.stdout
    mock_stop.assert_called_once()
    assert load_state(path=state_path).local.running_model is None


def test_stop_command_noop_message_when_nothing_running(tmp_path, monkeypatch):
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))
    result = runner.invoke(app, ["stop"])
    assert result.exit_code == 0
    assert "No local model is running" in result.stdout
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd modelman && uv run pytest tests/commands/test_local_control.py -v`
Expected: FAIL — `AssertionError` / non-zero exit because `start`/`stop` are not yet registered Typer commands (CliRunner reports "No such command").

- [ ] **Step 3: Wire the commands into `main.py`**

Add to the import block at the top of `modelman/src/modelman/main.py`:

```python
from .local_control import LocalControlError, start_local_model, stop_local_model
```

Add the commands after `unexpose` (before `refresh_prices`):

```python
@app.command()
def start(
    model_id: str = typer.Argument(..., help="Registry model id to run locally (<provider>/<name>)"),
) -> None:
    """Stop any running local model and start model_id, recording it as
    the single local model wt's picker may offer. Idempotent if model_id
    is already running."""
    registry = load_registry()
    try:
        with locked_state() as state:
            result = start_local_model(registry, state, model_id)
    except LocalControlError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
    if result.already_running:
        typer.echo(f"{model_id} is already running.")
    else:
        typer.echo(f"Started {model_id}.")


@app.command()
def stop() -> None:
    """Stop the currently-running local model and clear the marker.
    No-op when nothing is running."""
    try:
        with locked_state() as state:
            result = stop_local_model(state)
    except LocalControlError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
    if result.stopped_model_id is None:
        typer.echo("No local model is running.")
    else:
        typer.echo(f"Stopped {result.stopped_model_id}.")
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd modelman && uv run pytest tests/commands/test_local_control.py -v`
Expected: PASS.

Also run: `cd modelman && uv run pytest tests/test_local_control.py tests/benchmark/test_isolation.py tests/test_state.py -q` to confirm no regressions from Tasks 1-3.
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/main.py modelman/tests/commands/test_local_control.py
git commit -m "$(cat <<'EOF'
feat(modelman): add `modelman start`/`modelman stop` CLI commands

Explicit local-model lifecycle control (issue #65) - completes plan item #4.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_011vKFaBGEBbzYVoSWX8SxtX
EOF
)"
```

---

### Task 5: Contract fixture — `[local]` table in `modelman.sample.toml`

**Files:**
- Modify: `docs/contracts/modelman.sample.toml`
- Modify: `modelman/tests/contracts/test_modelman_fixture.py`

**Interfaces:**
- Produces: `[local] running_model = "ollama/contract-fixture:local"` in the shared fixture — a model id already present in `docs/contracts/registry.sample.toml` and already has a `[model_state]` entry in the fixture (`ready = true`), so no new registry/state fixture rows are needed.

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/contracts/test_modelman_fixture.py`, inside `test_load_state_matches_shared_fixture` (after the `[litellm]` assertions, before the price-refresh assertion):

```python
    # [local] running-model marker (issue #65): the single local model
    # wt's picker may currently offer.
    assert state.local.running_model == "ollama/contract-fixture:local"
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modelman && uv run pytest tests/contracts/test_modelman_fixture.py -v`
Expected: FAIL — `AssertionError: assert None == 'ollama/contract-fixture:local'`.

- [ ] **Step 3: Add the `[local]` table to the fixture**

In `docs/contracts/modelman.sample.toml`, add after the `[litellm]` block (before the first `[model_state...]` entry):

```toml
# Single local model wt's picker may currently offer (issue #65). Absent
# or empty = no local model is running; wt shows cloud models only.
[local]
running_model = "ollama/contract-fixture:local"
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd modelman && uv run pytest tests/contracts/test_modelman_fixture.py -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add docs/contracts/modelman.sample.toml modelman/tests/contracts/test_modelman_fixture.py
git commit -m "$(cat <<'EOF'
test(contracts): add [local].running_model to the shared modelman.toml fixture

Pins the marker schema both languages must agree on (issue #65) - completes
plan item #5. wt's matching assertion lands in plan item #11.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_011vKFaBGEBbzYVoSWX8SxtX
EOF
)"
```

---

## Part B — wt (Go)

### Task 6: `internal/config` reads the `[local].running_model` marker

**Files:**
- Modify: `wt/internal/config/modelman.go`
- Modify: `wt/internal/config/config.go`
- Modify: `wt/internal/config/modelman_test.go` (update 4 existing `loadModelmanState()` call sites for the new return arity)
- Modify: `wt/internal/config/modelman_fixture_test.go` (update the call site + new assertion)

**Interfaces:**
- Consumes: nothing new.
- Produces: `Config.LocalRunningModel() string`, `Config.LocalGateActive() bool`, `Config.SetLocalRunningForTest(id string)` (test-only). `loadModelmanState` now returns 4 values instead of 3: `(map[string]ExposureEntry, LitellmState, string, error)`.

- [ ] **Step 1: Write the failing tests**

Add to `wt/internal/config/config_test.go` (a new test — no existing test in this file covers the gate; confirm the file's package is `config` before appending):

```go
// TestLocalGateActiveDefaultsFalse asserts that a hand-built Config{}
// literal — the shape nearly every existing wt test uses — has the
// one-local-model-at-a-time gate (issue #65) inactive by default, so this
// feature does not change behavior for tests that predate it and never
// opt in via SetLocalRunningForTest.
func TestLocalGateActiveDefaultsFalse(t *testing.T) {
	cfg := &Config{}
	if cfg.LocalGateActive() {
		t.Error("LocalGateActive() = true for a hand-built Config, want false")
	}
	if cfg.LocalRunningModel() != "" {
		t.Errorf("LocalRunningModel() = %q, want empty", cfg.LocalRunningModel())
	}
}

// TestSetLocalRunningForTestActivatesGate asserts the test setter both
// records the marker and flips LocalGateActive() true, mirroring what
// finalizeCfg does for a real Load().
func TestSetLocalRunningForTestActivatesGate(t *testing.T) {
	cfg := &Config{}
	cfg.SetLocalRunningForTest("ollama/x")
	if !cfg.LocalGateActive() {
		t.Error("LocalGateActive() = false after SetLocalRunningForTest, want true")
	}
	if cfg.LocalRunningModel() != "ollama/x" {
		t.Errorf("LocalRunningModel() = %q, want ollama/x", cfg.LocalRunningModel())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./internal/config -run 'TestLocalGateActive|TestSetLocalRunningForTest' -v`
Expected: FAIL — build error (`cfg.LocalGateActive undefined`).

- [ ] **Step 3: Extend `modelmanState`/`loadModelmanState` in `modelman.go`**

In `wt/internal/config/modelman.go`, add a `Local` field to `modelmanState`:

```go
type modelmanState struct {
	// price_refresh_last_run is modelman's global "token pricing last
	// refreshed" date (YYYY-MM-DD), written by `modelman refresh-prices`.
	// wt reads it post-launch to print a stale-pricing notice.
	PriceRefreshLastRun string `toml:"price_refresh_last_run"`
	ModelState          map[string]struct {
		Exposed        bool `toml:"exposed"`
		LitellmExposed bool `toml:"litellm_exposed"` // back-compat read
		Ready          bool `toml:"ready"`
		Downloaded     bool `toml:"downloaded"`
	} `toml:"model_state"`
	Litellm LitellmState `toml:"litellm"`
	// Local mirrors modelman's [local] table (issue #65): the single
	// local model `modelman start` last started, or empty if none/
	// `modelman stop` was last run. wt reads it read-only to filter its
	// model picker — see internal/localgate and Config.FilterToRunningLocal.
	Local struct {
		RunningModel string `toml:"running_model"`
	} `toml:"local"`
}
```

Update `loadModelmanState`'s signature and body:

```go
// loadModelmanState reads modelman.toml and returns the exposure map, the
// [litellm] routing state, and the [local].running_model marker. A missing
// file returns empty values (every non-native model is unexposed; LiteLLM
// routing defaults to off; no local model is marked running).
func loadModelmanState() (map[string]ExposureEntry, LitellmState, string, error) {
	path := ModelmanPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]ExposureEntry{}, LitellmState{}, "", nil
	}
	if err != nil {
		return nil, LitellmState{}, "", fmt.Errorf("read modelman.toml: %w", err)
	}
	var s modelmanState
	if err := toml.Unmarshal(data, &s); err != nil {
		return nil, LitellmState{}, "", fmt.Errorf("parse modelman.toml: %w", err)
	}
	out := make(map[string]ExposureEntry, len(s.ModelState))
	for id, st := range s.ModelState {
		out[id] = ExposureEntry{Exposed: st.Exposed || st.LitellmExposed, Ready: st.Ready || st.Downloaded}
	}
	return out, s.Litellm, s.Local.RunningModel, nil
}
```

- [ ] **Step 4: Update `Config`/`finalizeCfg` and add the new accessors in `config.go`**

Add two unexported fields to the `Config` struct (next to `exposed`):

```go
type Config struct {
	DefaultTag string          `toml:"default_tag"`
	Providers  []Provider      `toml:"providers"`
	Models     []Model         `toml:"models"`
	Agents     []Agent         `toml:"agents"`
	litellm    LitellmState   `toml:"-"` // from modelman.toml
	exposed    map[string]ExposureEntry `toml:"-"` // from modelman.toml
	// localRunning is the raw [local].running_model marker from
	// modelman.toml; localGateActive is true only for a Config built by
	// Load() (see finalizeCfg) — a hand-built Config{} literal (nearly
	// every pre-issue-#65 test) leaves it false, so the gate this field
	// pair drives is a no-op for those tests unless they explicitly opt
	// in via SetLocalRunningForTest.
	localRunning    string `toml:"-"`
	localGateActive bool   `toml:"-"`
}
```

Update `finalizeCfg`:

```go
func finalizeCfg(cfg *Config, providers []Provider, models []Model) (*Config, error) {
	cfg.Providers, cfg.Models = providers, models
	deriveNative(cfg)
	exposed, litellm, localRunning, err := loadModelmanState()
	if err != nil {
		return nil, err
	}
	cfg.exposed = exposed
	cfg.litellm = litellm
	cfg.localRunning = localRunning
	cfg.localGateActive = true
	return cfg, nil
}
```

Add the new accessors near `SetExposedForTest`/`ExposeAllForTest`:

```go
// LocalRunningModel returns the raw `[local].running_model` marker from
// modelman.toml — the registry id of the local model `modelman start` last
// started, or "" if none (see docs/superpowers/specs/2026-09-10-one-local-
// model-at-a-time-design.md). This is the unverified marker; a caller that
// needs to know whether the marked model is actually serving right now
// should probe it (internal/localgate.Resolve).
func (c *Config) LocalRunningModel() string { return c.localRunning }

// LocalGateActive reports whether the one-local-model-at-a-time gate
// (FilterToRunningLocal, and the -M pin checks in cmd/wt/resolve.go and
// internal/tui) is live for this Config. True only for a Config built by
// Load() (production). A hand-built Config{} literal — the shape nearly
// every pre-issue-#65 test uses — defaults to false, so the gate is a
// no-op for those tests unless they call SetLocalRunningForTest.
func (c *Config) LocalGateActive() bool { return c.localGateActive }

// SetLocalRunningForTest activates the one-local-model-at-a-time gate (as
// Load() would) and sets the running-model marker, as if finalizeCfg had
// read it from modelman.toml. runningModelID == "" simulates "gate active,
// no local model marked running" (every local model gets filtered out by
// FilterToRunningLocal). Tests only.
func (c *Config) SetLocalRunningForTest(runningModelID string) {
	c.localGateActive = true
	c.localRunning = runningModelID
}
```

- [ ] **Step 5: Fix the 4 pre-existing `loadModelmanState()` call sites in `modelman_test.go`**

In `wt/internal/config/modelman_test.go`, update each call to destructure 4 return values instead of 3 (the added `_` sits between the litellm state and the error):

Line ~41 (`TestLoadModelmanStateMissingFileReturnsEmptySet`):
```go
	exposed, litellm, _, err := loadModelmanState()
```

Line ~66 (`TestLoadModelmanStateHonorsXDG`):
```go
	exposed, _, _, err := loadModelmanState()
```

Line ~88 (`TestLoadModelmanStateMalformedTOMLError`):
```go
	_, _, _, err := loadModelmanState()
```

Line ~113 (`TestLoadModelmanStateReadsLegacyDownloadedAsReady`):
```go
	exposed, _, _, err := loadModelmanState()
```

- [ ] **Step 6: Fix the `modelman_fixture_test.go` call site and add the marker assertion**

In `wt/internal/config/modelman_fixture_test.go`:

```go
	models, litellm, localRunning, err := loadModelmanState()
	if err != nil {
		t.Fatalf("loadModelmanState() error: %v", err)
	}

	if localRunning != "ollama/contract-fixture:local" {
		t.Errorf("localRunning = %q, want %q", localRunning, "ollama/contract-fixture:local")
	}
```

(Insert the new assertion right after the `if err != nil` check, before the existing `models[...]` assertions. This depends on Task 5's fixture already carrying the `[local]` table — run Task 5 before this task if the shared repo history diverges.)

- [ ] **Step 7: Run tests to verify they pass**

Run: `cd wt && go build ./... && go test ./internal/config/... -v`
Expected: PASS (build succeeds; every test in the package, including the two new ones and the four fixed call sites).

- [ ] **Step 8: Commit**

```bash
git add wt/internal/config/modelman.go wt/internal/config/config.go wt/internal/config/modelman_test.go wt/internal/config/modelman_fixture_test.go wt/internal/config/config_test.go
git commit -m "$(cat <<'EOF'
feat(wt): read [local].running_model marker; gate no-op for literal Configs

loadModelmanState returns the marker; Config.LocalGateActive() confines the
new one-local-model-at-a-time gate (issue #65) to Load()-built Configs, so
pre-existing hand-built-Config tests are unaffected - completes plan item #6.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_011vKFaBGEBbzYVoSWX8SxtX
EOF
)"
```

---

### Task 7: `Config.FilterToRunningLocal` pure filter

**Files:**
- Modify: `wt/internal/config/config.go`
- Modify: `wt/internal/config/config_test.go`

**Interfaces:**
- Consumes: `Config.ResolveLocation`, `Config.localGateActive` (Task 6).
- Produces: `Config.FilterToRunningLocal(models []Model, runningLocalID string) []Model`.

- [ ] **Step 1: Write the failing tests**

Add to `wt/internal/config/config_test.go`:

```go
func filterGateTestConfig() *Config {
	return &Config{
		Providers: []Provider{
			{ID: "claude", Location: LocationCloud, Auth: AuthConfig{Type: "native"}},
			{ID: "ollama", Location: LocationLocal, Auth: AuthConfig{Type: "none"}},
			{ID: "omlx", Location: LocationLocal, Auth: AuthConfig{Type: "none"}},
		},
	}
}

// TestFilterToRunningLocalInactiveGateIsNoop asserts the core safety
// property this task depends on: with the gate inactive (the default for
// a hand-built Config{}), FilterToRunningLocal returns its input
// unchanged, regardless of the runningLocalID argument.
func TestFilterToRunningLocalInactiveGateIsNoop(t *testing.T) {
	cfg := filterGateTestConfig()
	models := []Model{
		{ID: "claude/opus", ProviderID: "claude"},
		{ID: "ollama/a", ProviderID: "ollama"},
		{ID: "omlx/b", ProviderID: "omlx"},
	}
	got := cfg.FilterToRunningLocal(models, "")
	if len(got) != 3 {
		t.Fatalf("got %d models, want 3 (gate inactive = no filtering)", len(got))
	}
}

// TestFilterToRunningLocalNoMarkerDropsAllLocal asserts "no marker → cloud
// models only" — the picker-filter half of the design's hard block.
func TestFilterToRunningLocalNoMarkerDropsAllLocal(t *testing.T) {
	cfg := filterGateTestConfig()
	cfg.SetLocalRunningForTest("")
	models := []Model{
		{ID: "claude/opus", ProviderID: "claude"},
		{ID: "ollama/a", ProviderID: "ollama"},
		{ID: "omlx/b", ProviderID: "omlx"},
	}
	got := cfg.FilterToRunningLocal(models, "")
	if len(got) != 1 || got[0].ID != "claude/opus" {
		t.Fatalf("got %v, want only claude/opus", got)
	}
}

// TestFilterToRunningLocalKeepsOnlyTheRunningOne asserts a marked local
// model is kept and every OTHER local model is still dropped — only one
// local model is ever offered at a time.
func TestFilterToRunningLocalKeepsOnlyTheRunningOne(t *testing.T) {
	cfg := filterGateTestConfig()
	cfg.SetLocalRunningForTest("ollama/a")
	models := []Model{
		{ID: "claude/opus", ProviderID: "claude"},
		{ID: "ollama/a", ProviderID: "ollama"},
		{ID: "omlx/b", ProviderID: "omlx"},
	}
	got := cfg.FilterToRunningLocal(models, "ollama/a")
	ids := map[string]bool{}
	for _, m := range got {
		ids[m.ID] = true
	}
	if len(got) != 2 || !ids["claude/opus"] || !ids["ollama/a"] {
		t.Fatalf("got %v, want [claude/opus ollama/a]", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./internal/config -run TestFilterToRunningLocal -v`
Expected: FAIL — build error (`cfg.FilterToRunningLocal undefined`).

- [ ] **Step 3: Implement `FilterToRunningLocal` in `config.go`**

Add near `IsExposed` in `wt/internal/config/config.go`:

```go
// FilterToRunningLocal narrows models to those launchable under the
// one-local-model-at-a-time policy (issue #65): cloud and native models
// pass through unchanged; a local model is kept only when its id equals
// runningLocalID. A no-op (models returned unchanged) when the gate is
// not active (LocalGateActive) — every pre-issue-#65 test that builds a
// Config{} literal directly is unaffected.
func (c *Config) FilterToRunningLocal(models []Model, runningLocalID string) []Model {
	if !c.localGateActive {
		return models
	}
	out := make([]Model, 0, len(models))
	for _, m := range models {
		if loc, err := c.ResolveLocation(m); err == nil && loc == LocationLocal && m.ID != runningLocalID {
			continue
		}
		out = append(out, m)
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd wt && go test ./internal/config/... -v`
Expected: PASS (all tests in the package).

- [ ] **Step 5: Commit**

```bash
git add wt/internal/config/config.go wt/internal/config/config_test.go
git commit -m "$(cat <<'EOF'
feat(wt): add Config.FilterToRunningLocal (issue #65)

Pure filter narrowing a model list to cloud models plus the single verified-
running local model; a no-op when the gate is inactive - completes plan
item #7.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_011vKFaBGEBbzYVoSWX8SxtX
EOF
)"
```

---

### Task 8: `internal/localgate` — availability probe

**Files:**
- Create: `wt/internal/localgate/localgate.go`
- Create: `wt/internal/localgate/localgate_test.go`

**Interfaces:**
- Consumes: `config.Config`/`Config.LocalRunningModel` (Task 6); `ollamacheck.Available` (`wt/internal/ollamacheck/ollamacheck.go`).
- Produces: `Available(modelID string) bool`, `NotRunningError{ModelID string}` (implements `error`), `Resolve(cfg *config.Config) (string, error)`, plus test-only seams `SetOmlxProbeURLForTest(url string) (restore func())` and `SetMlxLMServerProbeURLForTest(url string) (restore func())`.

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/localgate/localgate_test.go`:

```go
package localgate

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

func TestProviderOf(t *testing.T) {
	provider, name, ok := providerOf("ollama/qwen3.8:27b-mlx")
	if !ok || provider != "ollama" || name != "qwen3.8:27b-mlx" {
		t.Errorf("providerOf = (%q, %q, %v), want (ollama, qwen3.8:27b-mlx, true)", provider, name, ok)
	}
}

// TestProviderOfKeepsFirstSlashOnly asserts the marker schema's "split on
// the FIRST / only" rule — the model name itself may contain a "/" (e.g.
// an openrouter org/model spelling).
func TestProviderOfKeepsFirstSlashOnly(t *testing.T) {
	provider, name, ok := providerOf("openrouter/z-ai/glm-5.3-flash")
	if !ok || provider != "openrouter" || name != "z-ai/glm-5.3-flash" {
		t.Errorf("providerOf = (%q, %q, %v), want (openrouter, z-ai/glm-5.3-flash, true)", provider, name, ok)
	}
}

func TestProviderOfNoSlash(t *testing.T) {
	if _, _, ok := providerOf("no-slash-here"); ok {
		t.Error("expected ok=false for an id with no provider prefix")
	}
}

func TestAvailableOmlxProbesConfiguredURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer SetOmlxProbeURLForTest(srv.URL)()

	if !Available("omlx/some-model") {
		t.Error("expected omlx model to be available when the probe URL responds 200")
	}
}

func TestAvailableOmlxDownReportsUnavailable(t *testing.T) {
	defer SetOmlxProbeURLForTest("http://127.0.0.1:1")() // nothing listens here
	if Available("omlx/some-model") {
		t.Error("expected omlx model to be unavailable when the probe URL is unreachable")
	}
}

func TestAvailableMlxLmServerProbesConfiguredURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer SetMlxLMServerProbeURLForTest(srv.URL)()

	if !Available("mlx_lm_server/target-repo") {
		t.Error("expected mlx_lm_server model to be available when the probe URL responds 200")
	}
}

// TestAvailableUnknownProviderIsUnavailable asserts that a marker naming a
// not-yet-wired-in provider (e.g. the future mtplx, issue #66) fails
// closed as "not running" rather than panicking or reporting healthy.
func TestAvailableUnknownProviderIsUnavailable(t *testing.T) {
	if Available("mtplx/some-model") {
		t.Error("expected an unknown provider to report unavailable")
	}
}

// TestAvailableOllamaDelegatesToOllamacheck mirrors
// internal/ollamacheck's own TestAvailable fixture: a fake `ollama` binary
// on PATH stands in for the real daemon.
func TestAvailableOllamaDelegatesToOllamacheck(t *testing.T) {
	tmpDir := t.TempDir()
	fakeOllama := filepath.Join(tmpDir, "ollama")
	script := "#!/bin/sh\necho \"NAME    ID    SIZE    MODIFIED\"\necho \"qwen3.8:27b-mlx   abc   5.0 GB   2 days ago\"\n"
	if err := exec.Command("sh", "-c", "cat > "+fakeOllama+" <<'EOF'\n"+script+"EOF\nchmod +x "+fakeOllama).Run(); err != nil {
		t.Fatalf("creating fake ollama: %v", err)
	}
	t.Setenv("PATH", tmpDir)

	if !Available("ollama/qwen3.8:27b-mlx") {
		t.Error("expected ollama/qwen3.8:27b-mlx to be available")
	}
	if Available("ollama/missing-model") {
		t.Error("expected ollama/missing-model to be unavailable")
	}
}

func TestNotRunningErrorMessage(t *testing.T) {
	err := &NotRunningError{ModelID: "ollama/qwen3.8:27b-mlx"}
	want := "local model \"ollama/qwen3.8:27b-mlx\" is not running — start it with `modelman start ollama/qwen3.8:27b-mlx`"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestResolveNoMarker(t *testing.T) {
	cfg := &config.Config{}
	id, err := Resolve(cfg)
	if err != nil || id != "" {
		t.Errorf("Resolve() = (%q, %v), want (\"\", nil)", id, err)
	}
}

func TestResolveMarkerVerified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer SetOmlxProbeURLForTest(srv.URL)()

	cfg := &config.Config{}
	cfg.SetLocalRunningForTest("omlx/qwen3.8")
	id, err := Resolve(cfg)
	if err != nil || id != "omlx/qwen3.8" {
		t.Errorf("Resolve() = (%q, %v), want (\"omlx/qwen3.8\", nil)", id, err)
	}
}

func TestResolveMarkerStale(t *testing.T) {
	defer SetOmlxProbeURLForTest("http://127.0.0.1:1")()

	cfg := &config.Config{}
	cfg.SetLocalRunningForTest("omlx/qwen3.8")
	id, err := Resolve(cfg)
	if id != "" {
		t.Errorf("Resolve() id = %q, want \"\"", id)
	}
	nre, ok := err.(*NotRunningError)
	if !ok {
		t.Fatalf("err = %v (%T), want *NotRunningError", err, err)
	}
	if nre.ModelID != "omlx/qwen3.8" {
		t.Errorf("NotRunningError.ModelID = %q, want omlx/qwen3.8", nre.ModelID)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./internal/localgate/... -v`
Expected: FAIL — `no such package` (the directory/package doesn't exist yet).

- [ ] **Step 3: Implement `localgate.go`**

Create `wt/internal/localgate/localgate.go`:

```go
// Package localgate probes whether modelman's marked "currently running"
// local model (issue #65's one-local-model-at-a-time policy) is actually
// serving right now. The marker itself — [local].running_model in
// ~/.config/local-ai/modelman.toml — is read via internal/config
// (Config.LocalRunningModel); this package only does the runtime
// availability probe, mirroring internal/ollamacheck's role for the
// pre-existing ollama-only availability check.
package localgate

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/ollamacheck"
)

// probeTimeout bounds each HTTP availability probe.
const probeTimeout = 2 * time.Second

// omlxModelsURL and mlxLMServerModelsURL are the endpoints
// bin/llm-isolate-provider's own warmup logic treats as "provider is up"
// for these two providers (see that script's start_omlx/start_mlx_lm_server
// functions). Package-level vars so tests can point them at an
// httptest.Server instead of the real localhost ports.
var (
	omlxModelsURL        = "http://localhost:8000/v1/models"
	mlxLMServerModelsURL = "http://localhost:8001/v1/models"
)

// httpClient is a seam so tests can rely on probeTimeout without waiting
// out a longer default transport timeout against an unreachable port.
var httpClient = &http.Client{Timeout: probeTimeout}

// SetOmlxProbeURLForTest overrides the omlx availability-probe URL and
// returns a func that restores the original. Tests only.
func SetOmlxProbeURLForTest(url string) (restore func()) {
	old := omlxModelsURL
	omlxModelsURL = url
	return func() { omlxModelsURL = old }
}

// SetMlxLMServerProbeURLForTest is SetOmlxProbeURLForTest's mlx_lm_server
// counterpart. Tests only.
func SetMlxLMServerProbeURLForTest(url string) (restore func()) {
	old := mlxLMServerModelsURL
	mlxLMServerModelsURL = url
	return func() { mlxLMServerModelsURL = old }
}

// providerOf splits a registry model id "<provider>/<name>" on the FIRST
// "/" — the model name itself may contain one (e.g.
// openrouter/z-ai/glm-5.3-flash), so only the first separator counts. This
// mirrors the marker schema in docs/superpowers/specs/2026-09-10-one-local-
// model-at-a-time-design.md.
func providerOf(modelID string) (provider, name string, ok bool) {
	idx := strings.Index(modelID, "/")
	if idx < 0 {
		return "", "", false
	}
	return modelID[:idx], modelID[idx+1:], true
}

// probeURL reports whether a GET to url succeeds with a 2xx status inside
// probeTimeout.
func probeURL(url string) bool {
	resp, err := httpClient.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// Available reports whether the local model identified by modelID
// ("<provider>/<name>") is actually serving right now. Unknown/unsupported
// providers (including a future mtplx before it is wired in here, issue
// #66) return false — a marker naming a provider this probe doesn't know
// about fails closed as "not running" rather than reporting stale state as
// healthy.
func Available(modelID string) bool {
	provider, name, ok := providerOf(modelID)
	if !ok {
		return false
	}
	switch provider {
	case "ollama":
		available, err := ollamacheck.Available(name)
		return err == nil && available
	case "omlx", "omlx-6bit":
		return probeURL(omlxModelsURL)
	case "mlx_lm_server":
		return probeURL(mlxLMServerModelsURL)
	default:
		return false
	}
}

// NotRunningError names the model that failed its availability probe (or
// was pinned via -M while a different, or no, local model is marked
// running) and the fix: `modelman start <id>`.
type NotRunningError struct {
	ModelID string
}

func (e *NotRunningError) Error() string {
	return fmt.Sprintf(
		"local model %q is not running — start it with `modelman start %s`",
		e.ModelID, e.ModelID,
	)
}

// Resolve reads cfg's [local].running_model marker (via
// Config.LocalRunningModel) and verifies it with Available. It returns
// ("", nil) when no marker is set — every local model should be filtered
// out of the picker (Config.FilterToRunningLocal handles that); (id, nil)
// when the marker is set and verified available; or ("", *NotRunningError)
// when the marker is set but the probe fails — a stale marker (the model
// was stopped or crashed outside modelman) that callers should treat as
// fatal rather than launching.
func Resolve(cfg *config.Config) (string, error) {
	marker := cfg.LocalRunningModel()
	if marker == "" {
		return "", nil
	}
	if !Available(marker) {
		return "", &NotRunningError{ModelID: marker}
	}
	return marker, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd wt && go build ./... && go vet ./... && go test ./internal/localgate/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/localgate/localgate.go wt/internal/localgate/localgate_test.go
git commit -m "$(cat <<'EOF'
feat(wt): add internal/localgate availability probe (issue #65)

Verifies modelman's [local].running_model marker against the provider's own
endpoint (ollama via ollamacheck, omlx/mlx_lm_server via /v1/models) -
completes plan item #8.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_011vKFaBGEBbzYVoSWX8SxtX
EOF
)"
```

---

### Task 9: Wire the gate into the non-TUI launch path

**Files:**
- Modify: `wt/cmd/wt/resolve.go`
- Modify: `wt/cmd/wt/main.go`
- Modify: `wt/cmd/wt/resolve_test.go`
- Modify: `wt/cmd/wt/main_test.go`

**Interfaces:**
- Consumes: `localgate.Resolve`, `localgate.NotRunningError` (Task 8); `Config.FilterToRunningLocal`, `Config.LocalGateActive`, `Config.SetLocalRunningForTest` (Tasks 6-7).
- Produces: `resolveModel` now applies the gate; `findModelByID(models []config.Model, id string) (config.Model, bool)` (new small helper in resolve.go); `runLaunchPath` exits immediately (propagates the error to `main()`'s existing catch-all) on a `*localgate.NotRunningError` instead of silently falling through to the TUI.

- [ ] **Step 1: Write the failing tests**

Add to `wt/cmd/wt/resolve_test.go`:

```go
// TestResolveModelNoMarkerHidesLocalModels asserts issue #65's picker-
// filter half for the non-TUI path: with the gate active and no marker,
// resolveModel treats every local model as ineligible — only cloud models
// can be auto-selected or listed.
func TestResolveModelNoMarkerHidesLocalModels(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
		},
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", Family: "opus", Tags: []string{"code"}},
			{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "pi", SupportedProviders: []string{"claude", "ollama"}}},
	}
	cfg.ExposeAllForTest()
	cfg.SetLocalRunningForTest("") // gate active, nothing running

	m, _, err := resolveModel("pi", cfg, "", "", "")
	if err != nil {
		t.Fatalf("resolveModel() error = %v, want nil (single eligible cloud model)", err)
	}
	if m.ID != "claude/opus" {
		t.Errorf("got %q, want claude/opus", m.ID)
	}
}

// TestResolveModelVerifiedMarkerAllowsLocalModel asserts a verified marker
// makes the marked local model eligible again.
func TestResolveModelVerifiedMarkerAllowsLocalModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer localgate.SetOmlxProbeURLForTest(srv.URL)()

	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models:    []config.Model{{ID: "omlx/qwen3.8", ProviderID: "omlx", Family: "qwen3.8", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	cfg.ExposeAllForTest()
	cfg.SetLocalRunningForTest("omlx/qwen3.8")

	m, _, err := resolveModel("pi", cfg, "", "", "")
	if err != nil || m.ID != "omlx/qwen3.8" {
		t.Errorf("resolveModel() = (%v, %v), want (omlx/qwen3.8, nil)", m, err)
	}
}

// TestResolveModelStaleMarkerErrors asserts the "exit with a message" half:
// a marker set but not verified is a fatal error for every launch through
// this agent, not just ones that would have used a local model.
func TestResolveModelStaleMarkerErrors(t *testing.T) {
	defer localgate.SetOmlxProbeURLForTest("http://127.0.0.1:1")() // nothing listens here

	cfg := &config.Config{
		Providers: []config.Provider{{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}}},
		Models:    []config.Model{{ID: "claude/opus", ProviderID: "claude", Family: "opus", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "claude", SupportedProviders: []string{"claude"}}},
	}
	cfg.ExposeAllForTest()
	cfg.SetLocalRunningForTest("omlx/qwen3.8")

	_, _, err := resolveModel("claude", cfg, "", "", "")
	var notRunning *localgate.NotRunningError
	if !errors.As(err, &notRunning) {
		t.Fatalf("err = %v, want *localgate.NotRunningError", err)
	}
	if notRunning.ModelID != "omlx/qwen3.8" {
		t.Errorf("NotRunningError.ModelID = %q, want omlx/qwen3.8", notRunning.ModelID)
	}
}

// TestResolveModelPinnedStaleLocalModelRejected asserts the pinned-model
// guard: -M pinning a local model that is not the currently-running one
// gets the specific "start it in modelman" message, not the generic
// "not in the eligible list" one.
func TestResolveModelPinnedStaleLocalModelRejected(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models: []config.Model{
			{ID: "ollama/a", ProviderID: "ollama", Family: "a", Tags: []string{"code"}},
			{ID: "ollama/b", ProviderID: "ollama", Family: "b", Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "pi", SupportedProviders: []string{"ollama"}}},
	}
	cfg.ExposeAllForTest()
	cfg.SetLocalRunningForTest("ollama/b") // b is running, pin a

	_, _, err := resolveModel("pi", cfg, "", "", "ollama/a")
	var notRunning *localgate.NotRunningError
	if !errors.As(err, &notRunning) {
		t.Fatalf("err = %v, want *localgate.NotRunningError", err)
	}
	if notRunning.ModelID != "ollama/a" {
		t.Errorf("NotRunningError.ModelID = %q, want ollama/a", notRunning.ModelID)
	}
}
```

Add `"net/http"`, `"net/http/httptest"`, and `"github.com/ohanaverse/local-ai-setup/wt/internal/localgate"` to `resolve_test.go`'s import block (`"errors"` is already imported).

Add to `wt/cmd/wt/main_test.go` (find the existing test file's package/import style first; this new test exercises `runLaunchPath` via the same seams other tests in this file already use — if `runLaunchPath` is not directly callable in this file's existing tests, follow the same construction pattern the nearest existing `runLaunchPath`-exercising test in this file uses):

```go
// TestRunLaunchPathExitsOnStaleLocalMarker asserts that a stale
// [local].running_model marker is fatal even on the "no agent pinned"
// fast path, which otherwise swallows resolveModelForLaunch's error and
// falls through to the TUI (resolveModelForLaunch's error is deliberately
// treated as "not resolved, show the picker" for the ordinary ambiguous-
// eligible-list case — a stale marker must NOT take that path, or the gate
// would silently degrade into "just open the TUI" instead of the design's
// required hard exit).
func TestRunLaunchPathExitsOnStaleLocalMarker(t *testing.T) {
	defer localgate.SetOmlxProbeURLForTest("http://127.0.0.1:1")()

	cfg := &config.Config{
		Providers: []config.Provider{{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}}},
		Models:    []config.Model{{ID: "claude/opus", ProviderID: "claude", Family: "opus", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "claude", SupportedProviders: []string{"claude"}}},
	}
	cfg.ExposeAllForTest()
	cfg.SetLocalRunningForTest("omlx/qwen3.8")

	_, eligible, err := resolveModelForLaunch("claude", cfg, "", "", "")
	var notRunning *localgate.NotRunningError
	if !errors.As(err, &notRunning) {
		t.Fatalf("resolveModelForLaunch error = %v, want *localgate.NotRunningError", err)
	}
	_ = eligible
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./cmd/wt -run 'TestResolveModel(NoMarker|VerifiedMarker|StaleMarker|Pinned)|TestRunLaunchPathExitsOnStaleLocalMarker' -v`
Expected: FAIL — either a build error (`localgate` unused/undefined in production code) or assertion failures, since `resolveModel` does not yet apply the gate.

- [ ] **Step 3: Wire the gate into `resolveModel` (`resolve.go`)**

Replace the body of `resolveModel` in `wt/cmd/wt/resolve.go`:

```go
package main

import (
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localgate"
)

// errCommandAgent is the sentinel returned by resolveModel when the
// resolved agent is a command (no model layer). Callers skip the model
// step and launch the command directly.
var errCommandAgent = fmt.Errorf("agent is a command")

// resolveModel computes the single model to launch for a non-TUI flow and
// returns the full eligible list so callers do not recompute it.
// ... (keep the existing doc comment above resolveModel unchanged) ...
func resolveModel(agent string, cfg *config.Config, tags, family, pinned string) (config.Model, []config.Model, error) {
	if agents.IsCommand(agent) {
		return config.Model{}, nil, errCommandAgent
	}
	eligible, err := cfg.EligibleModels(agent, tags, family)
	if err != nil {
		return config.Model{}, nil, err
	}

	// One-local-model-at-a-time gate (issue #65): reads modelman's
	// [local].running_model marker and verifies it. gateErr is non-nil
	// only when a marker IS set but fails its availability probe (a
	// stale marker) — that blocks every launch through this agent, not
	// just ones that would have picked a local model, until the operator
	// repairs it via `modelman start`/`modelman stop`.
	runningLocal, gateErr := localgate.Resolve(cfg)
	if gateErr != nil {
		return config.Model{}, eligible, gateErr
	}
	// A pinned local model that IS otherwise eligible (right agent/tags/
	// family) but isn't the currently-running one gets this specific
	// message instead of the generic "not in the eligible list" error
	// resolveModelFromEligible would produce once FilterToRunningLocal
	// drops it below. Guarded by LocalGateActive so a hand-built test
	// Config (the vast majority of this package's tests, which predate
	// issue #65 and never opt into the gate) never takes this branch.
	if pinned != "" && cfg.LocalGateActive() {
		if pm, ok := findModelByID(eligible, pinned); ok {
			if loc, lerr := cfg.ResolveLocation(pm); lerr == nil && loc == config.LocationLocal && pm.ID != runningLocal {
				return config.Model{}, eligible, &localgate.NotRunningError{ModelID: pm.ID}
			}
		}
	}
	eligible = cfg.FilterToRunningLocal(eligible, runningLocal)

	if len(eligible) == 0 {
		return config.Model{}, eligible, fmt.Errorf("no models match agent %q with tags %q and family %q", agent, tags, family)
	}
	m, err := resolveModelFromEligible(agent, eligible, pinned)
	return m, eligible, err
}

// findModelByID returns the model with the given id from models, and
// whether it was found.
func findModelByID(models []config.Model, id string) (config.Model, bool) {
	for _, m := range models {
		if m.ID == id {
			return m, true
		}
	}
	return config.Model{}, false
}

// resolveModelFromEligible ... (unchanged — keep the existing function body)
```

- [ ] **Step 4: Wire the stale-marker exit into `runLaunchPath` (`main.go`)**

Add `"github.com/ohanaverse/local-ai-setup/wt/internal/localgate"` to the import block in `wt/cmd/wt/main.go`.

Replace the `needsModelPicker` branch inside `runLaunchPath`:

```go
	if needsModelPicker(agent, pinned) {
		resolved, _, eligible, err := resolveModelForLaunch(agent, a.cfg, tags, family, pinned)
		// A stale/mismatched local-model marker (issue #65) is fatal
		// regardless of whether a TTY is available for the picker —
		// resolveModelForLaunch normally treats any resolveModel error as
		// "not resolved, fall through to the picker" (the ordinary
		// ambiguous-eligible-list case), but that fallback would silently
		// open the TUI instead of the hard exit the design requires here.
		var notRunning *localgate.NotRunningError
		if errors.As(err, &notRunning) {
			return err
		}
		if err == nil && resolved {
			return launchFiltered(agent, launchPath, a.cfg, yolo(cmd), tags, family, pinned, pinnedSupplied, args, eligible)
		}
		if !stdinTTY() {
			return pickerNeedsTTYError(agent)
		}
		return tuiRun(yolo(cmd), agent, pinned, tags, family, args, a.theme, launchPath, a.cfg)
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd wt && go build ./... && go vet ./... && go test ./cmd/wt/... -v`
Expected: PASS (all tests, including the pre-existing suite — confirming `LocalGateActive()`'s default-false guard keeps every hand-built-Config test unaffected).

- [ ] **Step 6: Commit**

```bash
git add wt/cmd/wt/resolve.go wt/cmd/wt/main.go wt/cmd/wt/resolve_test.go wt/cmd/wt/main_test.go
git commit -m "$(cat <<'EOF'
feat(wt): apply the one-local-model-at-a-time gate to the non-TUI launch path

resolveModel filters/rejects local models via localgate.Resolve +
Config.FilterToRunningLocal; runLaunchPath exits (instead of falling
through to the picker) on a stale marker - completes plan item #9.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_011vKFaBGEBbzYVoSWX8SxtX
EOF
)"
```

---

### Task 10: Wire the gate into the TUI model picker

**Files:**
- Modify: `wt/internal/tui/app.go`
- Create: `wt/internal/tui/local_gate_test.go`

**Interfaces:**
- Consumes: `localgate.Resolve`, `localgate.NotRunningError`, `localgate.SetOmlxProbeURLForTest` (Task 8); `Config.FilterToRunningLocal`, `Config.LocalGateActive`, `Config.SetLocalRunningForTest` (Tasks 6-7); `indexOfModelID` (`wt/internal/tui/model_list.go`, pre-existing).
- Produces: `model.fatalErr error` field; `enterModelPhase` applies the gate before building the list; `Run()` propagates `fatalErr` as its returned error.

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/tui/local_gate_test.go`:

```go
package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localgate"
)

func gateTestConfig() *config.Config {
	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
			{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
		},
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Family: "opus", Tags: []string{"code"}},
			{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8", Family: "qwen3.8", Tags: []string{"code"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude", "omlx"}},
		},
	}
	cfg.ExposeAllForTest()
	return cfg
}

// TestEnterModelPhaseNoMarkerHidesLocalModels asserts the picker-filter
// half of issue #65's gate: with no [local].running_model marker (the
// gate active but empty), a local model is not offered — only cloud
// models appear, matching "no marker → cloud only, by construction" in
// the design doc.
func TestEnterModelPhaseNoMarkerHidesLocalModels(t *testing.T) {
	cfg := gateTestConfig()
	cfg.SetLocalRunningForTest("") // gate active, nothing running
	m := model{cfg: cfg, width: 80, height: 24}
	models, _ := cfg.EligibleModels("claude", "", "")
	fullCatalog, _ := cfg.ModelsForAgent("claude")

	got, _ := m.enterModelPhase("claude", models, fullCatalog, "code")

	items := got.models.Items()
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1 (cloud model only)", len(items))
	}
	if items[0].(*modelItem).model.ID != "claude/opus" {
		t.Errorf("got model %q, want claude/opus", items[0].(*modelItem).model.ID)
	}
}

// TestEnterModelPhaseVerifiedMarkerShowsLocalModel asserts that once the
// marker names a model that verifies as actually running (an
// httptest.Server standing in for omlx's /v1/models probe), it appears in
// the picker alongside cloud models.
func TestEnterModelPhaseVerifiedMarkerShowsLocalModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer localgate.SetOmlxProbeURLForTest(srv.URL)()

	cfg := gateTestConfig()
	cfg.SetLocalRunningForTest("omlx/qwen3.8")
	m := model{cfg: cfg, width: 80, height: 24}
	models, _ := cfg.EligibleModels("claude", "", "")
	fullCatalog, _ := cfg.ModelsForAgent("claude")

	got, _ := m.enterModelPhase("claude", models, fullCatalog, "code")

	if len(got.models.Items()) != 2 {
		t.Fatalf("got %d items, want 2 (cloud + verified running local)", len(got.models.Items()))
	}
}

// TestEnterModelPhaseStaleMarkerQuits asserts the "exit with a message"
// half of the gate: a marker that fails its availability probe quits the
// whole TUI program (via tea.Quit + fatalErr, which Run() surfaces as its
// returned error) rather than silently falling back to cloud-only or
// showing the stale model.
func TestEnterModelPhaseStaleMarkerQuits(t *testing.T) {
	defer localgate.SetOmlxProbeURLForTest("http://127.0.0.1:1")() // nothing listens here

	cfg := gateTestConfig()
	cfg.SetLocalRunningForTest("omlx/qwen3.8")
	m := model{cfg: cfg, width: 80, height: 24}
	models, _ := cfg.EligibleModels("claude", "", "")
	fullCatalog, _ := cfg.ModelsForAgent("claude")

	got, cmd := m.enterModelPhase("claude", models, fullCatalog, "code")

	if got.fatalErr == nil {
		t.Fatal("expected fatalErr to be set")
	}
	var notRunning *localgate.NotRunningError
	if !errorsAsNotRunning(got.fatalErr, &notRunning) {
		t.Fatalf("fatalErr = %v, want *localgate.NotRunningError", got.fatalErr)
	}
	if cmd == nil {
		t.Fatal("expected a non-nil tea.Cmd (tea.Quit)")
	}
}

// TestEnterModelPhasePinnedStaleLocalModelRejected asserts the pinned-
// model guard: -M pinning a local model that is not the currently-running
// one is rejected with the same "start it in modelman" message the CLI
// path uses, not the generic "not in the eligible list" message.
func TestEnterModelPhasePinnedStaleLocalModelRejected(t *testing.T) {
	cfg := gateTestConfig()
	cfg.SetLocalRunningForTest("") // nothing running
	m := model{cfg: cfg, width: 80, height: 24, pinnedModel: "omlx/qwen3.8"}
	models, _ := cfg.EligibleModels("claude", "", "")
	fullCatalog, _ := cfg.ModelsForAgent("claude")

	got, _ := m.enterModelPhase("claude", models, fullCatalog, "code")

	if got.phase != phaseAgent {
		t.Fatalf("phase = %v, want phaseAgent", got.phase)
	}
	if !strings.Contains(got.status, "modelman start") {
		t.Errorf("status = %q, want it to mention `modelman start`", got.status)
	}
}

// errorsAsNotRunning is a tiny local errors.As wrapper so the test above
// reads plainly; avoids importing "errors" just for one call site.
func errorsAsNotRunning(err error, target **localgate.NotRunningError) bool {
	nre, ok := err.(*localgate.NotRunningError)
	if ok {
		*target = nre
	}
	return ok
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./internal/tui -run TestEnterModelPhase -v`
Expected: FAIL — build error (`m.fatalErr` undefined) or assertion failures.

- [ ] **Step 3: Add `fatalErr` to the `model` struct**

In `wt/internal/tui/app.go`, add a field to the `model` struct (near `listError`):

```go
	listError        string // reload error shown above the list when ready

	// fatalErr (issue #65) is set by enterModelPhase when modelman's
	// [local].running_model marker is stale — the probe in
	// internal/localgate failed. Paired with a tea.Quit return so the
	// whole program exits; Run() below surfaces it as the function's
	// returned error, matching the non-TUI path's exit-with-message
	// behavior (cmd/wt/main.go's runLaunchPath).
	fatalErr error
```

- [ ] **Step 4: Apply the gate at the top of `enterModelPhase`**

In `wt/internal/tui/app.go`, add `"github.com/ohanaverse/local-ai-setup/wt/internal/localgate"` to the import block, then modify `enterModelPhase`:

```go
func (m model) enterModelPhase(agent string, models, fullCatalog []config.Model, firstTag string) (model, tea.Cmd) {
	m.tag = firstTag

	// One-local-model-at-a-time gate (issue #65) — mirrors
	// cmd/wt/resolve.go's resolveModel. A stale marker (set but not
	// verified available) quits the whole program with a message; a
	// clean/empty marker narrows models to cloud-only, or cloud plus the
	// one verified-running local model.
	runningLocal, gateErr := localgate.Resolve(m.cfg)
	if gateErr != nil {
		m.fatalErr = gateErr
		return m, tea.Quit
	}
	pinRejected := ""
	if m.pinnedModel != "" && m.cfg.LocalGateActive() {
		if idx := indexOfModelID(models, m.pinnedModel); idx >= 0 {
			pm := models[idx]
			if loc, lerr := m.cfg.ResolveLocation(pm); lerr == nil && loc == config.LocationLocal && pm.ID != runningLocal {
				pinRejected = (&localgate.NotRunningError{ModelID: pm.ID}).Error()
			}
		}
	}
	models = m.cfg.FilterToRunningLocal(models, runningLocal)

	// Validate a -M pin BEFORE any list construction: a bad pin routes back to
	// the agent picker without scanning usage.jsonl or wrapping models. The
	// eligible slice is the source of truth; a pin that matches at all is one
	// of these models.
	if m.pinnedModel != "" && (pinRejected != "" || indexOfModelID(models, m.pinnedModel) < 0) {
		if pinRejected != "" {
			m.status = pinRejected
		} else {
			m.status = fmt.Sprintf("model %q is not in the eligible list for agent %q", m.pinnedModel, agent)
		}
		m.pinnedModel = ""
		items := buildAgentList(m.cfg)
		m.agentList = list.New(items, ThemedListDelegate(m.theme), m.width-2, m.height-2)
		m.agentList.Title = "Pick an agent or command"
		m.agentList.SetShowStatusBar(false)
		m.phase = phaseAgent
		return m, nil
	}

	// (the rest of the function — familyOf, rotation, buildModelItems,
	// pinned-model launch, unpinned rotation positioning, phase transition
	// — is unchanged; keep it as-is below this point)
	...
}
```

(Only the block up through the pinned-model-mismatch `return m, nil` changes; everything from `// Build the full-catalog family map...` onward stays exactly as it is today, now operating on the gate-filtered `models`.)

- [ ] **Step 5: Propagate `fatalErr` from `Run()`**

In `wt/internal/tui/app.go`, update `Run`:

```go
func Run(yolo bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config) error {
	p := tea.NewProgram(model{
		status:       "loading worktrees...",
		cfg:          cfg,
		theme:        theme,
		yolo:         yolo,
		initialAgent: agent,
		pinnedModel:  pinned,
		activeTags:   tags,
		activeFamily: family,
		extraArgs:    extraArgs,
		prePath:      prePath,
		repoRoot: func() string {
			if prePath == "" || prePath == "." {
				return ""
			}
			root, err := worktree.RepoRootAt(prePath)
			if err != nil {
				return ""
			}
			return root
		}(),
	}, tea.WithAltScreen())
	currentProgram = p
	// Reset any summary/survey state captured by a previous run (e.g.
	// from a test invocation sharing the process).
	pendingSummary = ""
	pendingSurveyState = pendingSurvey{}
	finalModel, err := p.Run()
	// The alt-screen is now torn down (p.Run() has returned and bubbletea
	// has called exitAltScreen), so stdout reaches the user's terminal
	// rather than a discarded buffer.
	printPendingSummaryAndSurvey()
	if err == nil {
		if fm, ok := finalModel.(model); ok && fm.fatalErr != nil {
			err = fm.fatalErr
		}
	}
	return err
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd wt && go build ./... && go vet ./... && go test ./internal/tui/... -v`
Expected: PASS (all tests, including the pre-existing suite — `enterModelPhase`'s pre-existing tests never call `SetLocalRunningForTest`, so `LocalGateActive()` stays false and the new gate block is a no-op for them).

- [ ] **Step 7: Commit**

```bash
git add wt/internal/tui/app.go wt/internal/tui/local_gate_test.go
git commit -m "$(cat <<'EOF'
feat(wt): apply the one-local-model-at-a-time gate to the TUI picker

enterModelPhase filters/rejects local models via localgate.Resolve +
Config.FilterToRunningLocal; a stale marker quits the whole program
(model.fatalErr, surfaced by Run()) instead of showing a broken model -
completes plan item #10.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_011vKFaBGEBbzYVoSWX8SxtX
EOF
)"
```

---

### Task 11: Cross-language contract coverage (Go side confirmation)

Task 6 (Step 6) already updated `wt/internal/config/modelman_fixture_test.go` to assert the marker from the shared fixture. This task is the final cross-check that both languages' contract tests pass together against the same fixture file, now that both sides of the schema exist.

**Files:**
- No new file changes — verification only.

- [ ] **Step 1: Run both contract test suites together**

Run:
```bash
cd modelman && uv run pytest tests/contracts/ -v
cd ../wt && go test ./internal/config -run Fixture -v
```
Expected: PASS for both. If either fails, the fixture and one language's reader have drifted — fix the reader (not the fixture, unless Task 5's `[local]` value itself was mistyped) before proceeding.

- [ ] **Step 2: No commit** — this task is verification-only (the changes it checks were already committed in Task 6 Step 8 and Task 5's commit). If Step 1 uncovers a drift, fix it as part of amending the relevant Task 6/Task 5 work before moving on, per this repo's plan-linkage convention (do not leave a broken contract test committed).

---

## Part C — Docs

### Task 12: Documentation + full-suite verification

**Files:**
- Modify: `modelman/CLAUDE.md`
- Modify: `wt/CLAUDE.md`

**Interfaces:** None (docs only).

- [ ] **Step 1: Document the new modelman commands**

In `modelman/CLAUDE.md`'s "Project overview" paragraph (first bullet-free paragraph, which currently lists CLI subcommands: "CLI subcommands: `download`... `litellm status|on|off|set`..."), append the two new commands:

```
CLI subcommands: `download` (TUI at a family), `migrate` (one-time import of legacy config), `sync` (reconcile state against providers), `expose`/`unexpose` (LiteLLM model_list), `litellm status|on|off|set` (the LiteLLM routing on/off switch wt reads), `start <model_id>`/`stop` (issue #65 — the single local model wt's picker may offer; delegates to bin/llm-isolate-provider).
```

Add a new subsection after "### Location semantics" (or the nearest logical spot under "## Configuration for end users"), named `### Local-model lifecycle (issue #65)`:

```markdown
### Local-model lifecycle (issue #65)

`modelman start <model_id>` / `modelman stop` are the only sanctioned way
to start or stop a local model for normal (non-benchmark) usage — see
`src/modelman/local_control.py` and
`docs/superpowers/specs/2026-09-10-one-local-model-at-a-time-design.md`.
Both delegate the actual process isolation to `bin/llm-isolate-provider`
(the same helper `modelman benchmark` uses) and record the running model's
id in `modelman.toml`'s `[local].running_model` table
(`src/modelman/state.py`'s `LocalState`). wt reads that marker read-only
(`wt/internal/config/modelman.go`, `wt/internal/localgate`) to filter its
model picker to cloud models plus this one verified-running local model —
see `wt/CLAUDE.md`'s "Local-model gate" section.
```

- [ ] **Step 2: Document the wt-side gate**

In `wt/CLAUDE.md`, add a new subsection after "## Registry (modelman-owned)" (before "## Config (themes)"), named `## Local-model gate (issue #65)`:

```markdown
## Local-model gate (issue #65)

wt offers at most one local model at a time, mirroring modelman's own
process-isolation constraint (Apple Silicon GPU/RAM is shared). The gate
reads modelman-owned `modelman.toml`'s `[local].running_model` marker
(`internal/config`'s `Config.LocalRunningModel()`/`LocalGateActive()`),
verifies it with `internal/localgate.Resolve` (ollama via
`internal/ollamacheck`, omlx/mlx_lm_server via their `/v1/models`
endpoints), and applies `Config.FilterToRunningLocal` at the two places wt
resolves a model list: `cmd/wt/resolve.go`'s `resolveModel` (non-TUI) and
`internal/tui`'s `enterModelPhase` (TUI). No marker → cloud models only; a
verified marker → cloud models plus the one running local model; a marker
that fails its probe (stale — stopped/crashed outside modelman) is fatal —
wt exits (non-TUI) or quits the whole program (TUI, via `model.fatalErr`)
with a message naming the fix: `modelman start <id>`. A `-M` pin naming a
local model that isn't the verified one gets the same message.

`LocalGateActive()` is true only for a `Config` built by `Load()`
(production); a hand-built `Config{}` literal — the shape nearly every
pre-issue-#65 test uses — defaults to false, making the gate a no-op there
unless a test opts in via `SetLocalRunningForTest`.

Start/stop the local model itself with modelman: `modelman start
<provider>/<name>` / `modelman stop` (see `modelman/CLAUDE.md`).
```

- [ ] **Step 3: Run the full verification suite**

Run: `make test-all` from the repo root (`/Users/keith/github/ohanaverse/local-ai-setup`).
Expected: PASS — `make lint-shell` (bash -n + shellcheck on `bin/llm-isolate-provider`), `make check-links` (validates the new doc cross-references resolve), modelman `make check`/`make test`, wt `go build`/`go vet`/`go test`.

- [ ] **Step 4: Commit**

```bash
git add modelman/CLAUDE.md wt/CLAUDE.md
git commit -m "$(cat <<'EOF'
docs: document modelman start/stop and wt's local-model gate (issue #65)

Completes plan item #12.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_011vKFaBGEBbzYVoSWX8SxtX
EOF
)"
```

---

## Self-Review Notes (for the executor)

- **Spec coverage:** marker schema → Tasks 1, 5, 6, 11. `modelman start`/`stop` CLI → Tasks 2-4. wt picker filter + verification (no marker / verified / stale) → Tasks 7-10. Pinned-model guard → Tasks 9-10. Edge cases (unknown model, already-running idempotency, nothing-to-stop no-op, provider not isolatable, marker to a model no longer in the registry — this last one is exercised implicitly: `localgate.Available` returns `false` for any id whose provider probe fails or is unrecognized, so a marker pointing at a deleted registry model still reaches the same "stale → exit" path without needing a registry lookup) → Tasks 3, 9-10. Out-of-scope items (mtplx, benchmark-isolation consolidation) are explicitly not touched by any task.
- **Test-blast-radius safety:** the `LocalGateActive` bool (Task 6) is the load-bearing design choice that keeps this plan from having to touch the ~15+ pre-existing wt tests that build local models into hand-written `Config{}` literals across `cmd/wt/resolve_test.go`, `cmd/wt/launch_test.go`, `internal/tui/agent_picker_test.go`, and `internal/tui/app_test.go` — confirmed none of those files call `config.Load()`. Do not skip Task 6's `localGateActive` guard as a "simplification"; without it, Tasks 9-10 would break a large, unscoped set of pre-existing tests.
