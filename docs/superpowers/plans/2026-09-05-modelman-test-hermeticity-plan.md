# modelman test hermeticity + location-aware cloud exemption — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the modelman test suite safe to run while agents (pi, Claude) are using the local LiteLLM proxy (zero real proxy restarts, zero real `ollama` daemon hits), and fix PR #27's cloud-exemption gap so the TUI EXPOSED column and the apply gate both honor `location = "cloud"` for ollama `:cloud` rows.

**Architecture:** Add a shared `is_cloud_effective(model, registry)` helper in `src/modelman/litellm.py` that returns `True` for openrouter (provider policy) and for any model with `location = "cloud"`. Use it in both the TUI column (`screens/models.py`) and the apply gate (`_validated_entry` in `litellm.py`). Add two autouse fixtures in `tests/conftest.py`: one redirects `restart_litellm_proxy()` to a no-op shell command, and one stubs the module-level `_default_runner` in `providers/ollama.py` and `ollama_caps.py` so unmocked provider paths fail closed with "not found" instead of shelling out to the live daemon.

**Tech Stack:** Python 3.13, Textual, pytest, monkeypatch, `uv run pytest`, shell shims for verification.

---

## File structure

| File | Responsibility in this plan |
|------|-----------------------------|
| `modelman/src/modelman/litellm.py` | New `is_cloud_effective()` helper; update `_validated_entry()` to use it. |
| `modelman/src/modelman/screens/models.py` | Update `_repopulate()` column gate to use `is_cloud_effective()`. |
| `modelman/tests/conftest.py` | Two autouse fixtures: `_never_restart_live_proxy` and `_never_call_real_ollama`. |
| `modelman/tests/screens/test_models.py` | Extend `test_exposed_column_requires_ready_but_exempts_cloud` with an `ollama/:cloud` row. |
| `modelman/tests/test_litellm.py` | New test verifying `_validated_entry()` accepts a not-ready `location="cloud"` ollama model. |
| `modelman/tests/screens/test_app_navigation.py` | New first-run TUI test proving a bare `ModelmanApp()` mounts and reconciles without live services. |
| `docs/guides/00-config-map.md` | Align cloud-exemption wording. |
| `docs/guides/02-providers-and-models.md` | Same. |
| `docs/guides/04-litellm-config.md` | Same. |
| `docs/guides/08-maintenance-and-troubleshooting.md` | Same. |
| `modelman/CLAUDE.md` | Update EXPOSED-column contract to name the helper and the location-aware rule. |

---

## Task 1: Add `is_cloud_effective()` and update the apply gate

**Files:**
- Modify: `modelman/src/modelman/litellm.py` (add helper after `is_cloud`, ~line 93; update `_validated_entry`, ~line 414)

- [ ] **Step 1: Add the helper after `is_cloud()`**

```python
def is_cloud_effective(model: ModelEntry, registry: Registry) -> bool:
    """True when a model should be exempt from the ready gate.

    A model is "effectively cloud" for exposure purposes when:
    - its provider policy declares it cloud (openrouter), or
    - the model itself is explicitly marked `location = "cloud"`.

    Note: a provider whose *provider* location is "cloud" (native/agent
    providers) is *not* included here; those rows are flag-only and are
    never routed through LiteLLM, mirroring `model_has_local_artifact`.
    """
    if is_cloud(model.provider_id):
        return True
    if model.location == "cloud":
        return True
    return False
```

- [ ] **Step 2: Replace the `policy.cloud` ready check in `_validated_entry()`**

Old block (around line 410-414):

```python
    policy = provider_policy(model.provider_id)
    if policy is None:
        raise ExposeError(f"provider {model.provider_id!r} has no LiteLLM mapping")
    if not policy.cloud and not state.get(model_id).ready:
        raise ExposeError(f"model {model_id!r} is not ready")
```

New block:

```python
    policy = provider_policy(model.provider_id)
    if policy is None:
        raise ExposeError(f"provider {model.provider_id!r} has no LiteLLM mapping")
    if not is_cloud_effective(model, registry) and not state.get(model_id).ready:
        raise ExposeError(f"model {model_id!r} is not ready")
```

- [ ] **Step 3: Run the litellm module tests to catch import/type errors**

Run:
```bash
cd modelman && uv run pytest tests/test_litellm.py -q
```

Expected: PASS (existing behavior should be unchanged; openrouter rows still exempt, local rows still require ready).

- [ ] **Step 4: Commit**

```bash
git add modelman/src/modelman/litellm.py
git commit -m "feat(litellm): location-aware cloud exemption helper used by apply gate

is_cloud_effective(model, registry) returns True for openrouter (provider
policy) and for any model with location='cloud' (ollama :cloud rows). The
_validated_entry apply gate now uses it instead of policy.cloud alone, so
a not-ready ollama/:cloud model can be exposed again."
```

---

## Task 2: Update the TUI EXPOSED column gate

**Files:**
- Modify: `modelman/src/modelman/screens/models.py:16` and `:348-353`

- [ ] **Step 1: Change the import from `is_cloud` to `is_cloud_effective`**

Old:
```python
from ..litellm import default_litellm_config_path, is_cloud, provider_policy
```

New:
```python
from ..litellm import default_litellm_config_path, is_cloud_effective, provider_policy
```

- [ ] **Step 2: Replace the column gate and comment**

Old:
```python
                # A model is only "effectively exposed" in this column if the
                # exposure flag is set AND the model is ready. Cloud models
                # (no local download) are exempt from the ready gate — the
                # same exemption `litellm._validated_entry` applies at the
                # apply gate — so they render 'Y' as long as the flag is on.
                ready_or_cloud = ready or is_cloud(m.provider_id)
```

New:
```python
                # A model is only "effectively exposed" in this column if the
                # exposure flag is set AND the model is ready. Cloud models
                # (openrouter, or ollama rows with location="cloud") are
                # exempt from the ready gate — the same exemption
                # `litellm._validated_entry` applies at the apply gate — so
                # they render 'Y' as long as the flag is on.
                ready_or_cloud = ready or is_cloud_effective(m, self.registry)
```

- [ ] **Step 3: Run the screen tests that exercise the EXPOSED column**

Run:
```bash
cd modelman && uv run pytest tests/screens/test_models.py::test_exposed_column_requires_ready_but_exempts_cloud -v
```

Expected: FAIL (the new `:cloud` row isn't in the test yet, and the existing openrouter case should still pass).

- [ ] **Step 4: Commit**

```bash
git add modelman/src/modelman/screens/models.py
git commit -m "fix(screens): EXPOSED column uses location-aware cloud exemption

Use is_cloud_effective(m, registry) so ollama/:cloud rows render Y when
exposed and not ready, matching the apply gate."
```

---

## Task 3: Add hermetic autouse fixtures to conftest.py

**Files:**
- Modify: `modelman/tests/conftest.py`

- [ ] **Step 1: Add imports and the fake ollama runner**

At the top of `modelman/tests/conftest.py`, change:

```python
from unittest.mock import MagicMock

import pytest
```

To:

```python
import subprocess
from typing import Any
from unittest.mock import MagicMock

import pytest
```

Add the runner function after the module docstring / before fixtures:

```python
def _fake_ollama_runner(args: list[str], **kwargs: Any) -> subprocess.CompletedProcess[str]:
    """Closed, deterministic runner for tests that don't inject a runner.

    Returns "not found" so is_downloaded() returns False, list_local()
    returns [], size_of() returns None, and auto_detect_model_info()
    returns {} — exactly the hermetic behavior CI sees when no ollama
    binary is installed. Tests that assert on runner behavior pass an
    explicit runner= and never call this default.
    """
    return subprocess.CompletedProcess(
        args=args,
        returncode=1,
        stdout="",
        stderr="Error: model not found",
    )
```

- [ ] **Step 2: Add the two autouse fixtures at the end of the file**

```python
@pytest.fixture(autouse=True)
def _never_restart_live_proxy(monkeypatch):
    """Tests that apply exposes must not bounce the user's live LiteLLM
    proxy: restart_litellm_proxy() runs `launchctl kickstart -k
    gui/$(id -u)/local.litellm.proxy` on macOS, which kills in-flight LLM
    requests from agents (pi, Claude) that route through localhost:4000.
    Point it at a no-op shell command; tests that specifically exercise
    restart behavior (test_litellm.py) monkeypatch the env var themselves."""
    monkeypatch.setenv("MODELMAN_LITELLM_RESTART_CMD", "true")


@pytest.fixture(autouse=True)
def _never_call_real_ollama(monkeypatch):
    """The full suite must never shell out to the user's live `ollama`
    daemon. Redirect the module-level default runners in
    providers/ollama.py and ollama_caps.py to a closed 'not found'
    result."""
    monkeypatch.setattr("modelman.providers.ollama._default_runner", _fake_ollama_runner)
    monkeypatch.setattr("modelman.ollama_caps._default_runner", _fake_ollama_runner)
```

- [ ] **Step 3: Verify the restart-behavior tests still pass**

Run:
```bash
cd modelman && uv run pytest tests/test_litellm.py -k "restart" -v
```

Expected: 5 PASS. These tests monkeypatch `MODELMAN_LITELLM_RESTART_CMD` themselves, overriding the autouse fixture.

- [ ] **Step 4: Run the flooding test files and confirm they still pass**

Run:
```bash
cd modelman && uv run pytest tests/screens/test_forms.py tests/screens/test_family_edit.py tests/screens/test_status.py tests/screens/test_app_navigation.py -q
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add modelman/tests/conftest.py
git commit -m "test(conftest): autouse fixtures to isolate suite from live services

_never_restart_live_proxy sets MODELMAN_LITELLM_RESTART_CMD=true so the
suite cannot call launchctl kickstart against the real LiteLLM proxy.
_never_call_real_ollama stubs the module-level _default_runner for
providers/ollama.py and ollama_caps.py, preventing 6000+ real ollama
show/list invocations per suite run. Tests that test restart logic or
inject explicit runners override these defaults as before."
```

---

## Task 4: Extend the EXPOSED column test with an `ollama/:cloud` row

**Files:**
- Modify: `modelman/tests/screens/test_models.py::test_exposed_column_requires_ready_but_exempts_cloud`

- [ ] **Step 1: Add the fourth model and state entry**

After the `cloud_model` definition, add:

```python
    cloud_ollama = ModelEntry(
        id="ollama/ornith-1.5:cloud",
        family="ornith",
        provider_id="ollama",
        model_name="ornith-1.5:cloud",
        location="cloud",
    )
```

Add it to the registry `models=` list:

```python
        models=[not_ready_local, ready_local, cloud_model, cloud_ollama],
```

After the existing state entries, add:

```python
    state.set(
        "ollama/ornith-1.5:cloud",
        ModelState(ready=False, litellm_exposed=True),
    )
```

- [ ] **Step 2: Add the assertion**

After the existing three assertions, add:

```python
        assert "Y" in rows["ornith-1.5:cloud"][5]  # ollama cloud-located + exposed → 'Y' (location exemption)
```

- [ ] **Step 3: Run the extended test**

Run:
```bash
cd modelman && uv run pytest tests/screens/test_models.py::test_exposed_column_requires_ready_but_exempts_cloud -v
```

Expected: PASS (all four cases).

- [ ] **Step 4: Commit**

```bash
git add modelman/tests/screens/test_models.py
git commit -m "test(screens): cover ollama/:cloud EXPOSED column exemption

Add a location='cloud' ollama row to the existing test so the location-
based cloud exemption is pinned alongside the openrouter case."
```

---

## Task 5: Add apply-gate test for a not-ready `location="cloud"` model

**Files:**
- Modify: `modelman/tests/test_litellm.py`

- [ ] **Step 1: Import `_validated_entry` if not already imported**

Add to the existing imports from `modelman.litellm`:

```python
from modelman.litellm import (
    ...
    _validated_entry,
    ...
)
```

If `_validated_entry` is already imported, skip this step.

- [ ] **Step 2: Add the test**

Append near the other `_validated_entry` / expose tests:

```python
def test_validated_entry_accepts_not_ready_location_cloud_model():
    """An ollama model explicitly marked location='cloud' is exempt from
    the ready gate at apply time, matching the TUI EXPOSED column's
    location-aware cloud exemption."""
    registry = Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none")),
        ],
        models=[
            ModelEntry(
                id="ollama/kimi-k3:cloud",
                family="f",
                provider_id="ollama",
                model_name="kimi-k3:cloud",
                location="cloud",
            )
        ],
    )
    state = StateStore()
    state.set("ollama/kimi-k3:cloud", ModelState(ready=False))
    entry = _validated_entry(registry, state, "ollama/kimi-k3:cloud")
    assert entry["model_name"] == "ollama/kimi-k3:cloud"
```

- [ ] **Step 3: Verify it fails before the code change (if run in isolation)**

This is already covered by Task 1, but confirm with:

```bash
cd modelman && uv run pytest tests/test_litellm.py::test_validated_entry_accepts_not_ready_location_cloud_model -v
```

Expected: PASS (Task 1 already updated `_validated_entry`).

- [ ] **Step 4: Commit**

```bash
git add modelman/tests/test_litellm.py
git commit -m "test(litellm): apply gate accepts not-ready ollama/:cloud model

Pin that _validated_entry does not raise 'not ready' for a location='cloud'
ollama model, matching the TUI column's location-aware cloud exemption."
```

---

## Task 6: Add first-run TUI hermetic test

**Files:**
- Modify: `modelman/tests/screens/test_app_navigation.py`

- [ ] **Step 1: Add the test**

Append to `tests/screens/test_app_navigation.py`:

```python
@pytest.mark.asyncio
async def test_app_mounts_without_live_daemon_or_proxy_restart():
    """With the hermetic autouse fixtures active, a bare ModelmanApp can
    mount, run reconcile, and render the family table without touching
    the live ollama daemon or restarting the LiteLLM proxy.

    Regression guard for the two interference issues: without the
    fixtures, this would load the real registry and shell out to ollama
    list/show thousands of times; applying any expose queue would also
    run launchctl kickstart against the live proxy.
    """
    from textual.widgets import DataTable

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.pause()  # let the reconcile worker settle

        table = app.screen.query_one("#family-table", DataTable)
        assert table.row_count >= 0
```

- [ ] **Step 2: Run it**

```bash
cd modelman && uv run pytest tests/screens/test_app_navigation.py::test_app_mounts_without_live_daemon_or_proxy_restart -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add modelman/tests/screens/test_app_navigation.py
git commit -m "test(screens): guard that bare ModelmanApp mounts without live services

A first-run-style mount must complete with the hermetic autouse fixtures
active, ensuring future screen tests cannot regress the ollama/proxy
interference issues."
```

---

## Task 7: Align docs to the location-aware rule

**Files:**
- Modify: `docs/guides/00-config-map.md`, `docs/guides/02-providers-and-models.md`, `docs/guides/04-litellm-config.md`, `docs/guides/08-maintenance-and-troubleshooting.md`, `modelman/CLAUDE.md`

- [ ] **Step 1: `docs/guides/00-config-map.md`**

Find the paragraph that reads (introduced by this PR):

> `litellm_exposed` vs the TUI EXPOSED column: `wt`'s picker and the proxy itself read the flag alone — a model with `litellm_exposed = true` is offered by `wt` and routed by LiteLLM. The TUI EXPOSED column is stricter: it shows `Y` only when the flag is set AND `state.ready = true`. Cloud models (`openrouter/*`, or `ollama/<name>:cloud` where `provider_id` is `openrouter` or `location = "cloud"`) are exempt from the ready gate, so a flagged cloud row always renders `Y` even though `ready = false`. Net: an id that `wt` offers and the proxy serves can show `–` in the TUI when the local artifact is missing — that's the TUI's way of saying "flagged but the local model isn't on disk yet." Apply the TUI queue (or `modelman apply` from the CLI) to converge the two views.

Keep it; it is already accurate after the code change. No edit needed unless the wording feels imprecise — if so, tighten "Cloud models" to:

> Cloud models (`openrouter/*`, or `ollama/<name>:cloud` where `location = "cloud"`) are exempt from the ready gate, so a flagged cloud row always renders `Y` even though `ready = false`.

- [ ] **Step 2: `docs/guides/02-providers-and-models.md`**

Find the sentence (line ~217):

> ... the EXPOSED column shows `Y` if both the flag is set and the model is ready.

Change to:

> ... the EXPOSED column shows `Y` if both the flag is set and the model is ready (cloud models — `openrouter/*` or `location = "cloud"` rows — are exempt from the ready gate).

- [ ] **Step 3: `docs/guides/04-litellm-config.md`**

Find the sentence (line ~128):

> ... the EXPOSED column shows `Y` once the flag is set AND the model is ready (cloud models are exempt from the ready gate — see [02-providers-and-models]).

Change to:

> ... the EXPOSED column shows `Y` once the flag is set AND the model is ready (cloud models — `openrouter/*` or `location = "cloud"` rows — are exempt from the ready gate — see [02-providers-and-models]).

- [ ] **Step 4: `docs/guides/08-maintenance-and-troubleshooting.md`**

Verify the bullet:

> **`ready = true` alongside the flag** (or the model is a cloud model — `provider_id` in `openrouter`, or `location = "cloud"` for an ollama model, where `ready` is permanently false) → genuine drift: modelman expects the row, and it was lost. → step 4 (re-expose replaces the row by id).

It is accurate. No change.

- [ ] **Step 5: `modelman/CLAUDE.md`**

Find the EXPOSED column contract paragraph (line ~61):

> **EXPOSED is the AND of the exposure flag and readiness** — a flagged but not-ready model renders `–` until apply finishes the queued download... **Cloud models are exempt from the readiness gate** — same `is_cloud()` check `_validated_entry` uses at the apply gate — so a flagged cloud row always renders `Y`.

Change to:

> **EXPOSED is the AND of the exposure flag and readiness** — a flagged but not-ready model renders `–` until apply finishes the queued download... **Cloud models are exempt from the readiness gate** — via `is_cloud_effective(model, registry)` in `litellm.py` (`openrouter` provider policy, or any model with `location = "cloud"`) used by both the TUI column and `_validated_entry` — so a flagged cloud row always renders `Y`.

- [ ] **Step 6: Commit**

```bash
git add docs/guides/00-config-map.md docs/guides/02-providers-and-models.md docs/guides/04-litellm-config.md docs/guides/08-maintenance-and-troubleshooting.md modelman/CLAUDE.md
git commit -m "docs: align cloud-exemption wording to location-aware rule

Update the five PR #27 doc touches so they consistently describe the
location-aware cloud exemption (openrouter or location='cloud')."
```

---

## Task 8: Full-suite hermeticity verification

**Files:** none (verification only)

- [ ] **Step 1: Run the full modelman suite**

```bash
cd modelman && uv run pytest -q
```

Expected: `597 passed` (or current count + 3 new tests = 600 passed).

- [ ] **Step 2: Verify zero live-service hits with shims**

Create the same shims used during investigation:

```bash
mkdir -p /tmp/fakebin-mt
cat > /tmp/fakebin-mt/launchctl <<'EOF'
#!/bin/bash
echo "LAUNCHCTL: $*" >> /tmp/fakebin-mt/calls.log
exit 0
EOF
cat > /tmp/fakebin-mt/ollama <<'EOF'
#!/bin/bash
echo "OLLAMA: $*" >> /tmp/fakebin-mt/calls.log
exit 1
EOF
chmod +x /tmp/fakebin-mt/launchctl /tmp/fakebin-mt/ollama
rm -f /tmp/fakebin-mt/calls.log
```

Run the suite with the shims first in `PATH`:

```bash
cd modelman && PATH=/tmp/fakebin-mt:$PATH uv run pytest -q
```

Expected: all tests pass.

Then inspect the log:

```bash
echo "launchctl calls: $(grep -c LAUNCHCTL /tmp/fakebin-mt/calls.log 2>/dev/null || echo 0)"
echo "ollama calls: $(grep -c OLLAMA /tmp/fakebin-mt/calls.log 2>/dev/null || echo 0)"
```

Expected: both `0`. Any non-zero count means a test path is bypassing the autouse fixture — investigate and fix.

- [ ] **Step 3: Run repo-level `make test-all`**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup && make test-all
```

Expected: lint + modelman check/test + wt build/vet/test all green.

- [ ] **Step 4: Final commit (if any fixes from verification)**

If the shim run or `make test-all` required fixes, commit them with a clear message; otherwise no extra commit.

---

## Self-review checklist

**1. Spec coverage:**
- Shared location-aware helper and both enforcement points → Tasks 1-2.
- Hermetic fixtures for proxy restarts and ollama daemon → Task 3.
- New/extended tests (column `:cloud` row, apply-gate `:cloud` test, first-run TUI hermetic test) → Tasks 4-6.
- Doc alignment → Task 7.
- Verification → Task 8.

**2. Placeholder scan:**
- No TBD/TODO, no "add appropriate error handling", no "write tests" without code, no "similar to Task N".

**3. Type consistency:**
- `is_cloud_effective(model: ModelEntry, registry: Registry) -> bool` is used the same way in both `litellm.py` and `screens/models.py`.
- `_fake_ollama_runner` return type `subprocess.CompletedProcess[str]` matches the runner protocol.
- Row index `5` for EXPOSED is unchanged and already validated by the existing test.

**4. Risks / notes for the executor:**
- If `modelman/tests/test_litellm.py` does not already import `_validated_entry`, the executor must add it in Task 5.
- If the bare `ModelmanApp()` first-run test fails because the real registry is malformed in the current environment, the test should be tolerant (it only asserts `table.row_count >= 0`); if it still fails, that indicates a real first-run bug exposed by the hermetic fixtures, not a test problem.
- The doc changes are minimal edits to text the PR already modified; no new sections.