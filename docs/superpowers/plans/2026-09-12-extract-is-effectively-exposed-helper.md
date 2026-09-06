# Extract `is_effectively_exposed` Helper Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Consolidate the "effective exposure" predicate (`exposed_flag AND (ready OR cloud)`) into a single canonical helper function in `litellm.py` to eliminate duplication between TUI projected state and backend validation.

**Architecture:** Create a pure function `is_effectively_exposed()` in `litellm.py` that accepts optional overrides for projected state testing. Refactor `_validated_entry()` to use it. Refactor TUI's `_load_models()`, `_enforce_expose_ready_rule()`, and `action_toggle_expose()` to use it. Add unit tests covering all combinations.

**Tech Stack:** Python 3.13, pytest, existing modelman types (`Registry`, `StateStore`, `ModelEntry`, `ModelState`).

---

### Task 1: Create `is_effectively_exposed` helper function in `litellm.py`

**Files:**
- Modify: `modelman/src/modelman/litellm.py`
- Test: `modelman/tests/test_litellm.py`

- [ ] **Step 1: Add the helper function after `is_cloud_effective()`**

```python
def is_effectively_exposed(
    model_id: str,
    registry: Registry,
    state: StateStore,
    exposed_override: bool | None = None,
    ready_override: bool | None = None,
) -> bool:
    """Determine if a model is effectively exposed through LiteLLM.

    A model is effectively exposed when:
    - its `litellm_exposed` flag is True (or `exposed_override` is True), AND
    - it is ready (or `ready_override` is True), OR
    - it is a cloud model (exempt from the ready gate).

    Args:
        model_id: Registry model ID to check.
        registry: Registry to resolve model/provider.
        state: StateStore for ready/exposed flags.
        exposed_override: Override the persisted litellm_exposed flag.
        ready_override: Override the persisted ready flag.

    Returns:
        True if the model would be exposed through LiteLLM, False otherwise.
        Unknown model IDs return False.
    """
    try:
        model = registry.model(model_id)
    except KeyError:
        return False

    exposed = exposed_override if exposed_override is not None else state.get(model_id).litellm_exposed
    if not exposed:
        return False

    ready = ready_override if ready_override is not None else state.get(model_id).ready
    if ready:
        return True

    # Cloud models are exempt from the ready gate.
    return is_cloud_effective(model)
```

- [ ] **Step 2: Run existing tests to ensure no regression**

```bash
cd modelman && uv run pytest tests/test_litellm.py -v
```
Expected: All existing tests pass.

- [ ] **Step 3: Commit**

```bash
git add modelman/src/modelman/litellm.py
git commit -m "refactor: add is_effectively_exposed helper function"
```

---

### Task 2: Add unit tests for `is_effectively_exposed`

**Files:**
- Modify: `modelman/tests/test_litellm.py`

- [ ] **Step 1: Add test for exposed=True, ready=True → True**

```python
def test_is_effectively_exposed_exposed_and_ready():
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True, litellm_exposed=True))
    from modelman.litellm import is_effectively_exposed

    assert is_effectively_exposed("ollama/a", registry, state) is True
```

- [ ] **Step 2: Add test for exposed=True, ready=False, cloud=False → False**

```python
def test_is_effectively_exposed_exposed_not_ready_local():
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    state = StateStore()
    state.set("ollama/a", ModelState(ready=False, litellm_exposed=True))

    from modelman.litellm import is_effectively_exposed

    assert is_effectively_exposed("ollama/a", registry, state) is False
```

- [ ] **Step 3: Add test for exposed=True, ready=False, cloud=True → True**

```python
def test_is_effectively_exposed_exposed_not_ready_cloud():
    registry = Registry(
        providers=[
            ProviderEntry(
                id="openrouter",
                name="OpenRouter",
                auth=AuthConfig(type="api_key", secret_ref="sk-or-v1-abc"),
            )
        ],
        models=[
            ModelEntry(
                id="openrouter/qwen",
                family="f",
                provider_id="openrouter",
                model_name="qwen",
                location="cloud",
            )
        ],
    )
    state = StateStore()
    state.set("openrouter/qwen", ModelState(ready=False, litellm_exposed=True))

    from modelman.litellm import is_effectively_exposed

    assert is_effectively_exposed("openrouter/qwen", registry, state) is True
```

- [ ] **Step 4: Add test for exposed=False, ready=True → False**

```python
def test_is_effectively_exposed_not_exposed_ready():
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True, litellm_exposed=False))

    from modelman.litellm import is_effectively_exposed

    assert is_effectively_exposed("ollama/a", registry, state) is False
```

- [ ] **Step 5: Add test for unknown model_id → False**

```python
def test_is_effectively_exposed_unknown_model():
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))],
        models=[],
    )
    state = StateStore()

    from modelman.litellm import is_effectively_exposed

    assert is_effectively_exposed("ollama/unknown", registry, state) is False
```

- [ ] **Step 6: Add test for exposed_override parameter**

```python
def test_is_effectively_exposed_exposed_override():
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True, litellm_exposed=False))

    from modelman.litellm import is_effectively_exposed

    # Override exposed=False to True
    assert is_effectively_exposed("ollama/a", registry, state, exposed_override=True) is True
    # Without override, returns False
    assert is_effectively_exposed("ollama/a", registry, state) is False
```

- [ ] **Step 7: Add test for ready_override parameter**

```python
def test_is_effectively_exposed_ready_override():
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )
    state = StateStore()
    state.set("ollama/a", ModelState(ready=False, litellm_exposed=True))

    from modelman.litellm import is_effectively_exposed

    # Override ready=False to True
    assert is_effectively_exposed("ollama/a", registry, state, ready_override=True) is True
    # Without override, returns False (local model, not ready)
    assert is_effectively_exposed("ollama/a", registry, state) is False
```

- [ ] **Step 8: Run the new tests**

```bash
cd modelman && uv run pytest tests/test_litellm.py::test_is_effectively_exposed_exposed_and_ready tests/test_litellm.py::test_is_effectively_exposed_exposed_not_ready_local tests/test_litellm.py::test_is_effectively_exposed_exposed_not_ready_cloud tests/test_litellm.py::test_is_effectively_exposed_not_exposed_ready tests/test_litellm.py::test_is_effectively_exposed_unknown_model tests/test_litellm.py::test_is_effectively_exposed_exposed_override tests/test_litellm.py::test_is_effectively_exposed_ready_override -v
```
Expected: All 7 new tests pass.

- [ ] **Step 9: Commit**

```bash
git add modelman/tests/test_litellm.py
git commit -m "test: add unit tests for is_effectively_exposed helper"
```

---

### Task 3: Refactor `_validated_entry` to use `is_effectively_exposed`

**Files:**
- Modify: `modelman/src/modelman/litellm.py`

- [ ] **Step 1: Replace the manual check in `_validated_entry`**

Current code:
```python
if not is_cloud_effective(model) and not state.get(model_id).ready:
    raise ExposeError(f"model {model_id!r} is not ready")
```

Replace with:
```python
if not is_effectively_exposed(model_id, registry, state):
    raise ExposeError(f"model {model_id!r} is not ready")
```

Full function after change:
```python
def _validated_entry(registry: Registry, state: StateStore, model_id: str) -> dict[str, Any]:
    """Resolve a registry model and build its config row, or raise ExposeError."""
    try:
        model = registry.model(model_id)
    except KeyError:
        raise ExposeError(f"model {model_id!r} not found in registry") from None
    try:
        provider = registry.provider(model.provider_id)
    except KeyError:
        # A hand-edited registry can reference a provider it doesn't
        # define; report that as an ExposeError (the CLI's caught type)
        # instead of an uncaught KeyError traceback.
        raise ExposeError(
            f"model {model_id!r} references unknown provider {model.provider_id!r}"
        ) from None
    policy = provider_policy(model.provider_id)
    if policy is None:
        raise ExposeError(f"provider {model.provider_id!r} has no LiteLLM mapping")
    if not is_effectively_exposed(model_id, registry, state):
        raise ExposeError(f"model {model_id!r} is not ready")
    return build_model_list_entry(model, provider)
```

- [ ] **Step 2: Run existing tests to verify no regression**

```bash
cd modelman && uv run pytest tests/test_litellm.py -v
```
Expected: All tests pass, including `test_validated_entry_accepts_not_ready_location_cloud_model`.

- [ ] **Step 3: Commit**

```bash
git add modelman/src/modelman/litellm.py
git commit -m "refactor: use is_effectively_exposed in _validated_entry"
```

---

### Task 4: Refactor TUI `ModelScreen._load_models` to use helper

**Files:**
- Modify: `modelman/src/modelman/screens/models.py`

- [ ] **Step 1: Import `is_effectively_exposed` at the top**

Current import:
```python
from ..litellm import default_litellm_config_path, is_cloud_effective, provider_policy
```

Replace with:
```python
from ..litellm import (
    default_litellm_config_path,
    is_cloud_effective,
    is_effectively_exposed,
    provider_policy,
)
```

- [ ] **Step 2: Replace `ready_or_cloud` and `exposed_str` calculations**

Current code:
```python
exposed = self.state.get(m.id).litellm_exposed
if m.id in self.queued_exposes:
    exposed = self.queued_exposes[m.id]
# Effective exposure = the (queued or persisted) flag AND the
# *projected* ready value — the same gate `_validated_entry`
# applies at apply time, so the column shows what the model
# will be after apply, not what it was before the queue.
# Cloud rows are exempt from the ready gate.
ready_or_cloud = self._projected_ready(m.id) or is_cloud_effective(m)
exposed_str = "Y" if (exposed and ready_or_cloud) else "–"
```

Replace with:
```python
# Use the queued expose value as the override for projected state.
exposed_override = self.queued_exposes.get(m.id)
# Use the projected ready value for the EXPOSED column preview.
ready_override = self._projected_ready(m.id)
# Effective exposure = the (queued or persisted) flag AND the
# *projected* ready value — the same gate `_validated_entry`
# applies at apply time, so the column shows what the model
# will be after apply, not what it was before the queue.
exposed_str = (
    "Y"
    if is_effectively_exposed(
        m.id, self.registry, self.state, exposed_override=exposed_override, ready_override=ready_override
    )
    else "–"
)
```

- [ ] **Step 3: Run TUI smoke test**

```bash
cd modelman && uv run pytest tests/test_litellm.py tests/ -k "not benchmark" -v
```
Expected: All tests pass.

- [ ] **Step 4: Manual smoke test (document for user)**

```
1. Toggle a local model to exposed=True but ready=False. The EXPOSED column should show –.
2. Toggle ready=True. The EXPOSED column should immediately switch to Y.
3. Verify that action_toggle_expose still correctly cascades the ready flag for local models.
```

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/screens/models.py
git commit -m "refactor: use is_effectively_exposed in _load_models"
```

---

### Task 5: Refactor `_enforce_expose_ready_rule` to use helper

**Files:**
- Modify: `modelman/src/modelman/screens/models.py`

- [ ] **Step 1: Replace the manual check**

Current code:
```python
def _enforce_expose_ready_rule(self, mid: str, entry: ModelEntry) -> None:
    """Single invariant: expose depends on ready. A queued expose=True
    cannot survive a model whose projected ready is False — apply()
    enforces the same rule at the gate (_validated_entry rejects the
    expose with 'model is not ready'); this keeps the queue consistent
    with it instead of leaving a doomed entry for apply() to fail on.
    Cloud rows are exempt, matching _validated_entry. Drops the expose
    with a notification rather than silently overwriting the user's
    request."""
    if is_cloud_effective(entry):
        return
    if self.queued_exposes.get(mid) is True and not self._projected_ready(mid):
        self.queued_exposes.pop(mid, None)
        self.app.notify(f"Expose cancelled: {mid} will not be ready")
```

Replace with:
```python
def _enforce_expose_ready_rule(self, mid: str, entry: ModelEntry) -> None:
    """Single invariant: expose depends on ready. A queued expose=True
    cannot survive a model whose projected ready is False — apply()
    enforces the same rule at the gate (_validated_entry rejects the
    expose with 'model is not ready'); this keeps the queue consistent
    with it instead of leaving a doomed entry for apply() to fail on.
    Cloud rows are exempt, matching _validated_entry. Drops the expose
    with a notification rather than silently overwriting the user's
    request."""
    # Use the helper with exposed_override=True to check if the queued
    # expose would be valid. Cloud rows are exempt per is_cloud_effective.
    if is_cloud_effective(entry):
        return
    if self.queued_exposes.get(mid) is True and not is_effectively_exposed(
        mid, self.registry, self.state, exposed_override=True, ready_override=self._projected_ready(mid)
    ):
        self.queued_exposes.pop(mid, None)
        self.app.notify(f"Expose cancelled: {mid} will not be ready")
```

- [ ] **Step 2: Run tests**

```bash
cd modelman && uv run pytest tests/ -k "not benchmark" -v
```
Expected: All tests pass.

- [ ] **Step 3: Commit**

```bash
git add modelman/src/modelman/screens/models.py
git commit -m "refactor: use is_effectively_exposed in _enforce_expose_ready_rule"
```

---

### Task 6: Refactor `action_toggle_expose` to use helper

**Files:**
- Modify: `modelman/src/modelman/screens/models.py`

- [ ] **Step 1: Replace the manual ready check in the cascade logic**

Current code (in the cascade section):
```python
if target and not self._projected_ready(mid) and not is_cloud_effective(entry):
    # Exposing requires ready — the same gate _validated_entry
    # applies at apply time. If the user has a ready toggle queued
    # that leaves the model not-ready, refuse rather than overwrite
    # their request; otherwise cascade the download in (apply runs
    # the ready loop before the expose loop, so the order works).
    if mid in self.queued_ready:
        self.app.notify(
            "Model is queued to be made not ready — cancel that before exposing"
        )
        return
    self._ready_cascade_for_expose.add(mid)
    self.queued_ready[mid] = True
```

Replace with:
```python
if target and not is_effectively_exposed(
    mid, self.registry, self.state, exposed_override=True, ready_override=self._projected_ready(mid)
):
    # Exposing requires ready — the same gate _validated_entry
    # applies at apply time. If the user has a ready toggle queued
    # that leaves the model not-ready, refuse rather than overwrite
    # their request; otherwise cascade the download in (apply runs
    # the ready loop before the expose loop, so the order works).
    if mid in self.queued_ready:
        self.app.notify(
            "Model is queued to be made not ready — cancel that before exposing"
        )
        return
    self._ready_cascade_for_expose.add(mid)
    self.queued_ready[mid] = True
```

Note: The `is_cloud_effective(entry)` check is now implicit in `is_effectively_exposed()`.

- [ ] **Step 2: Run tests**

```bash
cd modelman && uv run pytest tests/ -k "not benchmark" -v
```
Expected: All tests pass.

- [ ] **Step 3: Commit**

```bash
git add modelman/src/modelman/screens/models.py
git commit -m "refactor: use is_effectively_exposed in action_toggle_expose"
```

---

### Task 7: Run full test suite and verify

**Files:**
- No file changes expected.

- [ ] **Step 1: Run full modelman test suite**

```bash
cd modelman && uv run pytest tests/ -v
```
Expected: All tests pass.

- [ ] **Step 2: Run lint checks**

```bash
cd modelman && uv run ruff check src/ tests/
```
Expected: No lint errors.

- [ ] **Step 3: Commit (if any lint fixes were needed)**

```bash
git add modelman/
git commit -m "fix: address lint issues"
```

---

### Task 8: Documentation update

**Files:**
- Modify: `modelman/src/modelman/litellm.py` (docstring already added in Task 1)

- [ ] **Step 1: Verify the module docstring mentions the helper**

The module docstring at the top of `litellm.py` should briefly mention the helper. Add after the existing description:

Current first paragraph:
```python
"""LiteLLM exposure — build model_list entries and update config.yaml.
...
"""
```

No change needed — the function docstring is sufficient.

- [ ] **Step 2: Commit (if any doc changes were made)**

```bash
git add modelman/src/modelman/litellm.py
git commit -m "docs: document is_effectively_exposed helper"
```

---

## Verification Checklist

After completing all tasks:

1. **Spec coverage:**
   - ✅ Helper function created with correct signature
   - ✅ `_validated_entry` uses the helper
   - ✅ TUI `_load_models` uses the helper with overrides
   - ✅ `_enforce_expose_ready_rule` uses the helper
   - ✅ `action_toggle_expose` uses the helper
   - ✅ Unit tests cover all specified cases

2. **Test results:**
   - Run: `cd modelman && uv run pytest tests/test_litellm.py -v`
   - Expected: All tests pass including new `is_effectively_exposed_*` tests

3. **TUI smoke test:**
   - Toggle local model to exposed=True, ready=False → EXPOSED column shows `–`
   - Toggle ready=True → EXPOSED column shows `Y`
   - Verify cascade behavior still works

4. **Regression:**
   - Run: `make test-all`
   - Expected: All tests pass

---

Plan complete and saved to `docs/superpowers/plans/2026-09-12-extract-is-effectively-exposed-helper.md`. Two execution options:

**1. Subagent-Driven (recommended)** - I dispatch a fresh subagent per task, review between tasks, fast iteration

**2. Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints

**Which approach?**
