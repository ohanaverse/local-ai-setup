# Native-Provider Invariant Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the native-provider exposure rules unambiguous and enforced: native models are unconditionally exposed (override documented as non-native-only), and a native provider with a stale `PROVIDER_POLICIES` entry can never produce a LiteLLM row.

**Architecture:** Two localized changes in `modelman/src/modelman/litellm.py` — (1) docstring clarification + precedence-pinning tests for `is_effectively_exposed`; (2) an early native guard in `_validated_entry` (the shared apply gate used by `expose_model` and `apply_expose_queue`) so the native⇒no-policy invariant holds at the root of every write path. `provider_policy()` stays a pure lookup; `load_registry` is untouched.

**Tech Stack:** Python (Textual app), pytest, ruff + mypy (`make check`).

**Spec:** `docs/superpowers/specs/2026-09-15-native-invariant-hardening-design.md`

**Worktree:** `/Users/keith/github/ohanaverse/las-native-invariant` (branch `fix/native-invariant-hardening`). All commands run from `modelman/` inside that worktree. Python commands use `uv run` from `modelman/`.

---

### Task 1: Pin native precedence in `is_effectively_exposed` (#48)

**Files:**
- Modify: `modelman/src/modelman/litellm.py` (docstring of `is_effectively_exposed`, ~lines 145-168)
- Test: `modelman/tests/test_litellm.py` (near existing `test_is_effectively_exposed_native_not_exposed_not_ready`, ~line 778)

- [ ] **Step 1: Write the failing tests**

Add to `tests/test_litellm.py`, next to the existing native test (reuse its `_model` helper and `ModelState` patterns):

```python
def test_is_effectively_exposed_native_ignores_exposed_override():
    """Native models are unconditionally exposed: exposed_override=False
    must NOT hide a native model (issue #48 precedence pin)."""
    model = _model("agy/contract-fixture:native", "agy", "contract-fixture:native")
    model.native = True
    state = _state()  # reuse the module's StateStore fixture/helper pattern
    state.set("agy/contract-fixture:native", ModelState(ready=False, litellm_exposed=False))
    assert is_effectively_exposed(model, state, exposed_override=False) is True
    assert is_effectively_exposed(model, state, ready_override=False) is True


def test_is_effectively_exposed_override_false_hides_non_native():
    """For non-native models, exposed_override=False overrides a persisted
    true flag (the override path works where it is documented to)."""
    model = _model("ollama/glm-5", "ollama", "glm-5")
    model.location = "cloud"  # passes ready gate via cloud exemption
    state = _state()
    state.set("ollama/glm-5", ModelState(ready=False, litellm_exposed=True))
    assert is_effectively_exposed(model, state, exposed_override=False) is False
```

Match the actual helper names/signatures already used in the file (`_model`, state construction) — read the existing native test first and copy its setup style exactly. If a `_state` helper doesn't exist, construct `StateStore` the same way neighboring tests do.

- [ ] **Step 2: Run the new tests — they must pass already**

```bash
cd modelman && uv run pytest tests/test_litellm.py -k "exposed_override or native" -q
```

These are precedence-*pinning* tests: the code is already correct; the tests fail only if someone reorders later. Confirm all pass (including the pre-existing native test). If either new test fails, stop and re-read `is_effectively_exposed` before touching anything.

- [ ] **Step 3: Update the docstring**

In `is_effectively_exposed`, replace the Args entries for the overrides:

```python
    Args:
        model: The registry model entry to check.
        state: StateStore for ready/exposed flags.
        exposed_override: Override the persisted litellm_exposed flag.
            Applies to non-native models only — native models are
            unconditionally exposed and ignore this override (and the
            persisted flag) entirely.
        ready_override: Override the persisted ready flag. Likewise
            ignored for native models.
```

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "test(modelman): pin native-model precedence in is_effectively_exposed (#48)"
```

---

### Task 2: Enforce native ⇒ no policy at the apply gate (#47)

**Files:**
- Modify: `modelman/src/modelman/litellm.py` (`_validated_entry`, ~line 479)
- Test: `modelman/tests/test_litellm.py`

- [ ] **Step 1: Write the failing test**

The scenario: a native provider that (incorrectly, e.g. by hand-edit) has a `PROVIDER_POLICIES` entry. `provider_policy()` must stay a pure lookup, so the test monkeypatches the table:

```python
def test_apply_gate_rejects_native_model_with_stale_policy(monkeypatch):
    """Native ⇒ no LiteLLM policy invariant (#47): even if a provider has
    a stale PROVIDER_POLICIES entry, a native model is rejected at the
    apply gate instead of producing an unroutable LiteLLM row."""
    model = _model("agy/contract-fixture:native", "agy", "contract-fixture:native")
    model.native = True
    registry = _registry_with(model)  # reuse the file's registry-construction pattern
    state = _state()
    state.set(model.id, ModelState(ready=True, litellm_exposed=False))
    from modelman.litellm import PROVIDER_POLICIES, ProviderPolicy
    monkeypatch.setitem(PROVIDER_POLICIES, "agy", ProviderPolicy(cloud=False))
    with pytest.raises(ExposeError, match="native"):
        expose_model(registry, state, model.id, tmp_path / "config.yaml")
```

Read `ProviderPolicy`'s actual definition (top of `litellm.py`, near `PROVIDER_POLICIES` at line 67) and construct it with its real field names. Also read how existing tests call `expose_model` (tmp config path setup) and copy that. If `apply_expose_queue` has an existing test harness, add a second test reusing it to assert the same rejection; if its harness is heavy, asserting through `expose_model` (which shares `_validated_entry`) is sufficient — note that in the test docstring.

- [ ] **Step 2: Run it — must fail**

```bash
uv run pytest tests/test_litellm.py -k "stale_policy" -q
```

Expected failure: no `ExposeError` (the current code finds the policy and proceeds). If it fails differently (setup error), fix the test setup first.

- [ ] **Step 3: Implement the guard**

In `_validated_entry`, after the `model.native` determination is possible — insert the native guard **before** the `policy = provider_policy(...)` lookup:

```python
    if model.native:
        # Native ⇒ no LiteLLM policy invariant (#47): a native provider
        # must never produce a LiteLLM row, even if a stale
        # PROVIDER_POLICIES entry exists for it (hand-edited registry).
        raise ExposeError(
            f"provider {model.provider_id!r} is native and cannot be exposed through LiteLLM"
        )
    policy = provider_policy(model.provider_id)
```

(ModelEntry.native is a precomputed field; if it's derived from `provider.auth.type`, the field is still authoritative here — check `ModelEntry.native`'s population in `registry.py` if unsure.)

- [ ] **Step 4: Run the new test and the full litellm suite**

```bash
uv run pytest tests/test_litellm.py -q
```

All pass — the guard must not change behavior for any non-native path (existing tests confirm).

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat(modelman): enforce native=no-policy invariant at the apply gate (#47)"
```

---

### Task 3: Reconcile docstrings + full verification

**Files:**
- Modify: `modelman/src/modelman/litellm.py` (docstring near `PROVIDER_POLICIES`, ~line 41-67)

- [ ] **Step 1: Fix the "single source of truth" docstring**

Find the docstring calling `PROVIDER_POLICIES` "the single source of truth for provider-specific exposure rules" and amend it:

```python
    PROVIDER_POLICIES is the source of truth for non-native provider
    exposure rules. Native providers (auth.type == "native") are exempt
    by rule: they have no LiteLLM mapping, are always shown exposed by
    is_effectively_exposed, and are rejected at the apply gate
    (_validated_entry) even if a stale entry exists here.
```

- [ ] **Step 2: Full verification**

```bash
cd modelman && uv run make check && uv run pytest tests/ -q
```

ruff + mypy clean; all tests pass (baseline 859+ from main, plus new tests).

- [ ] **Step 3: Commit and push, open PR**

```bash
git add -A && git commit -m "docs(modelman): reconcile PROVIDER_POLICIES docstring with native exemption (#47)"
git push -u origin fix/native-invariant-hardening
gh pr create --title "modelman: native-provider invariant hardening" --body "Fixes #47, fixes #48 ..." --repo ohanaverse/local-ai-setup
```

The PR body should summarize: native unconditional + documented override precedence (#48); apply-gate guard rejecting native models even with a stale PROVIDER_POLICIES entry (#47); docstring reconciliation; no registry-loader or wt changes.
