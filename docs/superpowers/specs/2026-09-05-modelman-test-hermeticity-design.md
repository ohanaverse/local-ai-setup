# modelman test hermeticity + location-aware cloud exemption — Design

**Status:** Approved (2026-09-04)
**Date:** 2026-09-05 (spec written)
**Branch:** `fix/modelman-exposure-status-readiness` (PR #27)
**Owner:** Keith Hartmann

## Problem

Two independent problems in the modelman test suite and the PR it lands in:

1. **Tests kill agent LLM connections.** Running the full suite (597 tests, ~85s)
   while pi/Claude are actively using the local LiteLLM proxy (localhost:4000):
   - **20 real LiteLLM proxy restarts per run.** Tests that apply queued exposes
     (`PendingChanges.apply()` → `apply_expose_queue()` → `restart_litellm_proxy()`)
     call `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy` unmocked.
     The proxy dies mid-run, killing in-flight requests from both agents.
   - **6,464 real `ollama` binary invocations** (`ollama show/list`) hammering the
     live daemon via module-level `_default_runner` in `providers/ollama.py` and
     `ollama_caps.py` during an 85-second suite. Per-file flood verified:
     `test_forms.py` 5,082; `test_family_edit.py` 504; `test_status.py` 336;
     `test_app_navigation.py` 290. Ollama serializes generations on the local
     GPU/RAM; these steal the generation slot from agent traffic and add latency.
   - Neither is quarantined today: the CI-container tests pass because that
     environment has no live daemon and no launchd proxy label.

2. **PR #27's cloud-exemption behavior doesn't match its docs/tests.** The PR
   makes the TUI EXPOSED column render `Y` only when `litellm_exposed AND
   (ready OR cloud-exempt)`, and claims the exemption covers `openrouter/*`
   **and** `ollama/<name>:cloud` rows (`location = "cloud"`). But the actual
   check is `is_cloud(provider_id)`, which is provider-scoped — only `openrouter`
   has `cloud: True` in `PROVIDER_POLICIES`. An exposed, not-ready
   `ollama/kimi-k3:cloud` row therefore renders `–` (not `Y`), and — worse —
   re-exposing it at the apply gate fails with `ExposeError("not ready")`.
   The new test's cloud case uses `provider_id="openrouter"`, so it never
   exercises the `:cloud` path the PR body explicitly claims to fix.

## Goals

1. **The full suite must never touch the live runtime:** zero LiteLLM proxy
   restarts, zero real `ollama` daemon hits, regardless of local daemon state.
2. **The TUI column and the apply gate must agree** on what "cloud" means:
   provider-scoped (`openrouter`) **or** location-scoped (`location = "cloud"`,
   the ollama `:cloud` case), and the apply gate must *accept* a not-ready
   `:cloud` row.
3. **Keep the PR's existing 5 doc changes accurate** (they already describe the
   location-aware exemption — the code just doesn't implement it yet).
4. No behavioral change to `restart_litellm_proxy`, `_default_runner`,
   `is_cloud(provider_id)`, or `ProviderRegistry`; no cross-file refactors.

Non-goals: deciding whether wt's picker should adopt the stricter EXPOSED rule
(already deferred by the PR). Not touching `location = "cloud"` semantics for
native/agent providers beyond what `model_has_local_artifact` already does.

## Section 1 — Shared location-aware cloud rule (code)

### 1.1 New helper in `src/modelman/litellm.py` (next to `is_cloud`)

```python
def is_cloud_effective(model, registry) -> bool:
    """True when an exposure-relevant cloud classification applies.

    Provider policy (openrouter) OR explicit location: a model whose
    `location = "cloud"` lives remotely regardless of its provider (the
    ollama `:cloud` case — local provider, remote model). A provider
    whose location is "cloud" (native/agent rows) is excluded, since
    such models are flag-only (never a local download, and never
    routed via LiteLLM) — mirroring `model_has_local_artifact`.
    """
    if is_cloud(model.provider_id):
        return True
    if model.location == "cloud":
        return True
    try:
        provider = registry.provider(model.provider_id)
    except KeyError:
        return False
    return provider.location == "cloud"
```

Semantics mirror `model_has_local_artifact` (`registry.py:294-313`) but express
the *positive* cloud classification (that function classifies local-artifact
reconcile-syncability, which excludes provider-cloud rows; the same reasoning
applies here — provider-cloud rows are flag-only and never routed via LiteLLM).

### 1.2 Enforcement points (both have registry/model access today)

- `src/modelman/screens/models.py` `_repopulate` (~line 350):
  `ready_or_cloud = ready or is_cloud_effective(m, self.registry)`
  (replaces `ready or is_cloud(m.provider_id)`; the `ModelEntry` is already in
  scope as `m`, and `self.registry` is available).
- `src/modelman/litellm.py` `_validated_entry` (~line 414):
  `if not is_cloud_effective(model, registry) and not state.get(model_id).ready:` →
  `raise ExposeError(f"model {model_id!r} is not ready")`
  (replaces the `policy.cloud` check; the `policy is None` mapping-error raise
  stays — a missing policy is simply "not effective cloud", so it falls through
  to the ready gate unchanged).

### 1.3 Behavior change this enables (the PR's claimed, previously-false intent)

- A not-ready `ollama/kimi-k3:cloud` row (`location="cloud"`, `exposed=true`)
  renders `Y` in the EXPOSED column.
- Re-exposing that row at the apply gate no longer errors "not ready" — the
  config row is written, the flag flips, the proxy restarts (subject to Section
  2's fixture only when running tests).

## Section 2 — Hermetic test suite (tests/ only)

### 2.1 Autouse fixtures in `modelman/tests/conftest.py`

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
```

```python
_FAKE_OLLAMA_RUNNER = ...  # returns CompletedProcess(returncode=1, stderr="not found")
# (module-level callable, reusable, introspectable)

@pytest.fixture(autouse=True)
def _never_call_real_ollama(monkeypatch):
    """The full suite must never shell out to the user's live `ollama`
    daemon. Redirect the module-level default runners in
    providers/ollama.py and ollama_caps.py to a closed 'not found'
    result. Unit tests that explicitly test runner-based paths pass an
    explicit `runner=` and are unaffected (they don't call the module
    default)."""
    monkeypatch.setattr("modelman.providers.ollama._default_runner", _FAKE_OLLAMA_RUNNER)
    monkeypatch.setattr("modelman.ollama_caps._default_runner", _FAKE_OLLAMA_RUNNER)
```

### 2.2 Fail-closed semantics (verified in source)

- `is_downloaded`: `returncode=1` + `stderr "not found"` → `False` (definitively
  absent, no exception). Same result as CI where no daemon exists.
- `list_local` → `[]`; `size_of` → `None`; `auto_detect_model_info` → `{}`.
- Tests asserting a model *is* on disk stub `ProviderRegistry.get` already, so
  they never reach `_default_runner` (no double-stub needed).
- Tests asserting restart behavior (test_litellm.py, 5) monkeypatch the env var
  themselves; the autouse fixture doesn't conflict (tested: they pass with it).

### 2.3 Why autouse defaults over per-file stubbing

The 4 flooding files plus contracts and first-run TUI flows hit the real daemon
at different seams; an autouse default guarantees hermeticity with zero per-file
boilerplate and stays working as new screen/contract tests mount
`ModelmanApp()`.

## Section 3 — Tests for the new rule, the PR-behavior pin, and docs

### 3.1 Source tests

1. **Extend** `test_exposed_column_requires_ready_but_exempts_cloud`
   (`tests/screens/test_models.py`) with a fourth row:
   - `ollama/ornith-1.5:cloud` — `provider_id="ollama"`, `location="cloud"`,
     `ready=False`, `litellm_exposed=True` → `"Y"` (location-based exemption).
     Pins the PR-behavior claim (`kimi-k3:cloud` stays `Y`). The seeded
     registry already includes the `openrouter` provider the existing
     openrouter case asserts against.
2. **New test** — `_validated_entry` accepts a not-ready `:cloud` model (the
   apply-gate half of the rule, currently untested):
   `test_apply_accepts_not_ready_location_cloud_model` in `tests/test_litellm.py`
   → `_validated_entry` (or `expose_model`) with `location="cloud"`,
   `ready=False` → no `ExposeError`.
3. **New test** — TUI first-run flows complete without a live daemon or proxy
   restarts, proving the hermetic fixtures leave screen flows intact:
   `test_tui_flows_work_without_live_daemon_or_restart` in a screens test,
   mounting `ModelmanApp()` with the autouse fixtures active (no env overrides),
   asserting: no exception on mount, reconcile completes, table renders, exit
   doesn't try to restart the proxy.

### 3.2 Docs (align the PR's 5 touched files to the now-true behavior)

- `docs/guides/00-config-map.md` — the `litellm_exposed` vs column paragraph;
  verify it matches "openrouter OR location=cloud".
- `docs/guides/02-providers-and-models.md` — widen "cloud models exempt" to
  mention location=cloud.
- `docs/guides/04-litellm-config.md` — same one-line widening.
- `docs/guides/08-maintenance-and-troubleshooting.md` — bullet already reads
  `provider_id in openrouter, or location = "cloud" for an ollama model`;
  verify it stays accurate.
- `modelman/CLAUDE.md` — EXPOSED-column contract paragraph: name the
  location-aware helper and note the apply gate now also honors
  `location="cloud"` (re-exposing a not-ready `:cloud` row no longer errors).

## Verification

1. `cd modelman && uv run pytest` — all tests pass **with** the autouse
   fixtures active; shim-sweep confirms 0 `launchctl` and 0 `ollama` calls:
   `PATH=<shims> uv run pytest -q` then `grep -c LAUNCHCTL /tmp/fakebin-mt/calls.log`
   and `grep -c OLLAMA /tmp/fakebin-mt/calls.log` → both `0`.
2. New/extended tests fail on the pre-change code: the `:cloud` row assertion
   (`"Y" in …`) fails without Section 1's helper; the apply-gate test fails
   without the `_validated_entry` change (currently raises "not ready").
3. `make test-all` still green (lint + modelman + wt).

## Open decisions / deferred

- Whether wt's model picker adopts the stricter EXPOSED rule — deferred by the
  PR, unchanged.
- Whether the ollama-flood background is worth deeper provider refactoring
  (e.g. always-injectable runners) — out of scope; the autouse default covers it.