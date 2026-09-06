# Design: Extract `is_effectively_exposed` Helper

Date: 2026-09-12
Issue: #29
Context: `modelman` LiteLLM exposure logic.

## Goal

Consolidate the "effective exposure" predicate—the rule that determines if a model is actually available to the LiteLLM proxy—into a single canonical helper function. This eliminates duplication between the TUI's projected state and the backend's validation gate.

## Current State

The logic `exposed_flag AND (ready OR cloud)` is currently implemented in three distinct ways:
1.  **Backend Validation**: `_validated_entry` in `litellm.py` raises an `ExposeError` if a non-cloud model is not ready.
2.  **TUI Column**: `ModelScreen._load_models` computes a "projected" effective exposure for the `EXPOSED` column using `queued_exposes` and `_projected_ready`.
3.  **TUI Queue Invariants**: `_enforce_expose_ready_rule` and `action_toggle_expose` implement the same gate to keep the pending queue consistent.

## Design

### 1. The Helper Function

A new pure function `is_effectively_exposed` will be added to `modelman/src/modelman/litellm.py`.

**Signature:**
`is_effectively_exposed(model_id: str, registry: Registry, state: StateStore, exposed_override: bool | None = None) -> bool`

**Logic:**
1.  Attempt to resolve `model = registry.model(model_id)`. If not found, return `False`.
2.  `exposed = exposed_override if exposed_override is not None else state.get(model_id).litellm_exposed`
3.  `ready = state.get(model_id).ready`
4.  `cloud = is_cloud_effective(model)`
5.  Return `exposed and (ready or cloud)`

### 2. Integration Points

#### A. `modelman/src/modelman/litellm.py`
*   **`_validated_entry`**:
    Replace the manual check with:
    ```python
    if not is_effectively_exposed(model_id, registry, state):
        raise ExposeError(f"model {model_id!r} is not ready")
    ```

#### B. `modelman/src/modelman/screens/models.py`
*   **`_load_models`**:
    Replace the `ready_or_cloud` and `exposed_str` calculations.
    ```python
    # Use the queued value as the override for projected state
    exposed_override = self.queued_exposes.get(m.id)
    # We must also handle the 'projected ready' state. 
    # Since the helper uses state.get(mid).ready, we temporarily 
    # inject the projected ready value into a mock state or 
    # update the helper to accept a ready_override.
    ```
    *Refinement*: To fully support "projected" state in the TUI, the helper should also accept a `ready_override`.

    **Revised Signature:**
    `is_effectively_exposed(model_id, registry, state, exposed_override=None, ready_override=None)`
    `ready = ready_override if ready_override is not None else state.get(model_id).ready`

*   **`_enforce_expose_ready_rule`**:
    Use the helper with `exposed_override=True` and `ready_override=self._projected_ready(mid)`.

*   **`action_toggle_expose`**:
    Use the helper to determine if the `ready` cascade is necessary.

## Verification Plan

1.  **Unit Tests**: Add tests to `modelman` ensuring the helper correctly handles:
    *   `exposed=True, ready=True` $\rightarrow$ `True`
    *   `exposed=True, ready=False, cloud=False` $\rightarrow$ `False`
    *   `exposed=True, ready=False, cloud=True` $\rightarrow$ `True`
    *   `exposed=False, ready=True` $\rightarrow$ `False`
    *   Unknown `model_id` $\rightarrow$ `False`
2.  **TUI Smoke Test**:
    *   Toggle a local model to `exposed=True` but `ready=False`. The `EXPOSED` column should show `–`.
    *   Toggle `ready=True`. The `EXPOSED` column should immediately switch to `Y`.
    *   Verify that `action_toggle_expose` still correctly cascades the `ready` flag for local models.
3.  **Regression**: Run `make test-all` to ensure no existing tests are broken.
