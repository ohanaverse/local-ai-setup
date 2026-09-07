# Model Form: Immutable Identity Fields Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the model add/edit dialog treat identity fields (provider, model name, location) as add-only by disabling them in edit mode, and promote the Family selector to the top of the dialog.

**Architecture:** Rework `ModelForm` in `modelman/src/modelman/screens/forms.py` only — no registry or state-store changes. Compute `editing` in `compose()` and set `disabled=editing` on the provider Select, model Input, and location Select (exactly as the provider Select already does). Reorder the composed DOM so Family comes before Provider. Change `_modal_on_mount` focus: add mode focuses `#provider-select`, edit mode focuses `#family-select`. The `on_select_changed` location-relock logic stays add-mode-only (it already early-returns in edit mode).

**Tech Stack:** Python 3.13, Textual (TUI), pytest + pytest-asyncio, `modelman` package (uv-managed).

---

## Scope Check

This is a single, self-contained subsystem (the `ModelForm` dialog). No sub-project split needed. The change is purely dialog behavior; the registry/state-store and the `ModelScreen` callers are untouched.

## File Structure

- **Modify:** `modelman/src/modelman/screens/forms.py` — the `ModelForm.compose()` field order + disabled flags, and `ModelForm._modal_on_mount()` focus logic. This is the only production file changed.
- **Modify:** `modelman/tests/screens/test_forms.py` — add new behavior tests; add a `_submit` helper; update existing tests that break (focus assertions + submit-enter pattern).
- **Modify:** `modelman/tests/screens/test_models.py` — rework `test_discard_reverts_immediately_saved_registry_edit` (it edits `location`, which is now immutable in edit mode).
- **Modify:** `modelman/tests/screens/test_app_navigation.py` — fix 4 edit-mode submit tests (submit via Save button; rework 2 location-edit tests to edit cost instead).

No new files. No changes to `models.py`, the registry, or the state store.

---

## Task 1: Implement the forms.py changes

**Files:**
- Modify: `modelman/src/modelman/screens/forms.py` (the `ModelForm.compose()` block and `ModelForm._modal_on_mount()`)

- [ ] **Step 1: Reorder fields and disable identity fields in `compose()`**

In `modelman/src/modelman/screens/forms.py`, find the `with Vertical():` block inside `ModelForm.compose()` that currently yields Provider, then Family, then Model, then Location. Replace the whole block from `with Vertical():` through the `#location-select` Select with this (Family first; Provider, Model, and Location get `disabled=editing`):

```python
        with Vertical():
            yield Label("Family:")
            yield Select(
                options=[(f, f) for f in self._families],
                value=(self._family if self._family in self._families else self._families[0]),
                allow_blank=False,
                id="family-select",
            )
            yield Label("Provider:")
            yield Select(
                options=[(p, p) for p in self._providers],
                value=initial_provider,
                allow_blank=False,
                disabled=editing,
                id="provider-select",
            )
            yield Label("Model:")
            yield Input(
                value=model_val,
                placeholder=placeholder,
                disabled=editing,
                id="model",
            )
            yield Label("", id="model-error")
            yield Label("Location:")
            yield Select(
                options=[("cloud", "cloud"), ("local", "local")],
                value=location_value,
                allow_blank=False,
                disabled=editing or location_locked,
                id="location-select",
            )
```

Note: `location_locked` is already computed earlier in `compose()` (the provider-kind lock). `editing or location_locked` means edit mode always disables location; add mode keeps the existing provider-kind locking.

- [ ] **Step 2: Change `_modal_on_mount` focus**

Replace the current `_modal_on_mount` body (which focuses `#model`) with:

```python
    def _modal_on_mount(self) -> None:
        # Focus the first enabled field: the provider Select in add mode
        # (so the user can pick a provider immediately), the family Select
        # in edit mode (provider/model/location are all disabled there).
        if self._variant is None:
            self.query_one("#provider-select", Select).focus()
        else:
            self.query_one("#family-select", Select).focus()
        # Apply initial visibility for the conditional pricing sections.
        per_token_cb = self.query_one("#per-token-checkbox", Checkbox)
        sub_cb = self.query_one("#subscription-checkbox", Checkbox)
        self._set_per_token_visibility(bool(per_token_cb.value))
        self._set_subscription_visibility(bool(sub_cb.value))
```

- [ ] **Step 3: Verify the source change compiles**

Run: `cd modelman && uv run python -c "import modelman.screens.forms"`

Expected: no output, exit code 0.

- [ ] **Step 4: Commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git add modelman/src/modelman/screens/forms.py
git commit -m "feat(forms): make identity fields add-only and promote family to top"
```

---

## Task 2: Add new behavior tests to test_forms.py

**Files:**
- Modify: `modelman/tests/screens/test_forms.py`

These tests pin the new behavior. Add them in the "Edit mode pre-fill" section (after `test_modelform_edit_prefills_ollama_name`).

- [ ] **Step 1: Add the field-order test**

```python
@pytest.mark.asyncio
async def test_modelform_field_order_family_provider_model_location():
    """The composed DOM orders Family before Provider before Model before
    Location (identity fields grouped, family promoted to top)."""
    form = ModelForm(providers=["ollama"], families=["ornith"], family="ornith")
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        identity_ids = {
            "family-select",
            "provider-select",
            "model",
            "location-select",
        }
        ids = [w.id for w in app.screen.query(Select, Input) if w.id in identity_ids]
        assert ids == ["family-select", "provider-select", "model", "location-select"]
```

- [ ] **Step 2: Add the edit-mode-disabled test**

```python
@pytest.mark.asyncio
async def test_modelform_edit_mode_disables_identity_fields():
    """Edit mode disables provider, model, and location; family and the
    pricing checkboxes remain enabled."""
    variant: VariantSpec = {
        "id": "ollama/glm-5.3:cloud",
        "provider": "ollama",
        "name": "glm-5.3:cloud",
        "location": "cloud",
    }
    form = ModelForm(
        providers=["ollama"],
        variant=variant,
        families=["glm"],
        family="glm",
        provider_kinds={"ollama": "ollama"},
    )
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        assert app.screen.query_one("#provider-select", Select).disabled
        assert app.screen.query_one("#model", Input).disabled
        assert app.screen.query_one("#location-select", Select).disabled
        assert not app.screen.query_one("#family-select", Select).disabled
        assert not app.screen.query_one("#per-token-checkbox", Checkbox).disabled
        assert not app.screen.query_one("#subscription-checkbox", Checkbox).disabled
```

- [ ] **Step 3: Add the add-mode-enabled + focus test**

```python
@pytest.mark.asyncio
async def test_modelform_add_mode_identity_fields_enabled_and_provider_focused():
    """Add mode leaves provider, model, and location enabled (location
    subject to provider-kind locking) and focuses the provider Select on
    mount."""
    form = ModelForm(providers=["ollama"], default_provider="ollama")
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        assert not app.screen.query_one("#provider-select", Select).disabled
        assert not app.screen.query_one("#model", Input).disabled
        assert not app.screen.query_one("#location-select", Select).disabled
        assert _focused_id(app) == "provider-select"
```

- [ ] **Step 4: Add the edit-mode-focus test**

```python
@pytest.mark.asyncio
async def test_modelform_edit_mode_focuses_family_select():
    """Edit mode focuses the family Select (the first enabled field) on
    mount; provider/model/location are disabled."""
    variant: VariantSpec = {
        "id": "ollama/glm-5.3:cloud",
        "provider": "ollama",
        "name": "glm-5.3:cloud",
        "location": "cloud",
    }
    form = ModelForm(providers=["ollama"], variant=variant, families=["glm"], family="glm")
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.push_screen(form)
        await pilot.pause()
        assert _focused_id(app) == "family-select"
```

- [ ] **Step 5: Run the new tests**

Run: `cd modelman && uv run pytest tests/screens/test_forms.py -k "field_order or edit_mode_disables or add_mode_identity or edit_mode_focuses" -v`

Expected: all 4 PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git add modelman/tests/screens/test_forms.py
git commit -m "test(forms): pin immutable-identity field order, disabled state, and focus"
```

---

## Task 3: Update existing test_forms.py tests that break

**Files:**
- Modify: `modelman/tests/screens/test_forms.py`

The implementation changes initial focus (add → `#provider-select`, edit → `#family-select`) and disables the model Input in edit mode. Existing tests that (a) assert focus is `#model` after mount, or (b) submit by pressing Enter while focus is on a Select, will break. Fix them with a shared `_submit` helper.

- [ ] **Step 1: Add the `_submit` helper**

After the existing `_fill_model` helper (around line 51), add:

```python
async def _submit(app: ModelmanApp, pilot) -> None:
    """Submit the form by focusing an enabled widget that triggers
    _submit() on Enter. Add mode: the model Input (enabled). Edit mode:
    the model Input is disabled, so focus the Save button instead."""
    model = app.screen.query_one("#model", Input)
    if model.disabled:
        app.screen.query_one("#save", Button).focus()
    else:
        model.focus()
    await pilot.pause()
    await pilot.press("enter")
    await pilot.pause()
```

- [ ] **Step 2: Update the two focus-assertion tests**

In `test_modelform_buttons_and_focus`, change the final assertion from `assert _focused_id(pilot.app) == "model"` to:

```python
        assert _focused_id(pilot.app) == "provider-select"
```

In `test_modelform_escape_from_input_dismisses`, replace the body so it explicitly focuses the model Input (add mode) before pressing Escape:

```python
async def test_modelform_escape_from_input_dismisses():
    """Escape must cancel the modal even when the model Input is focused."""
    form = ModelForm(providers=["ollama"])
    dismissed: list = []
    async with ModelmanApp().run_test() as pilot:
        await pilot.pause()
        pilot.app.push_screen(form, dismissed.append)
        await pilot.pause()
        # Add mode focuses the provider Select on mount; move focus to the
        # model Input to exercise the escape-from-input path.
        pilot.app.screen.query_one("#model", Input).focus()
        await pilot.pause()
        assert _focused_id(pilot.app) == "model"
        await pilot.press("escape")
        await pilot.pause()
    assert dismissed == [None]
```

- [ ] **Step 3: Replace submit-Enter with `_submit(app, pilot)`**

In each of the following test functions, replace the `await pilot.press("enter")` that submits the form with `await _submit(app, pilot)`. (Do NOT touch the `press("enter")` calls that navigate from the family screen into the ModelScreen — those are inside `test_add_model_dialog_inherits_last_used_provider` and `test_add_model_dialog_locks_location_for_native_provider` and are unrelated.)

Affected test functions (all in `test_forms.py`):

1. `test_submit_ollama_tag_produces_correct_spec`
2. `test_submit_hf_repo_only_produces_correct_spec`
3. `test_submit_hf_repo_and_file_produces_correct_spec`
4. `test_submit_ollama_rejects_slash_with_inline_error`
5. `test_submit_hf_rejects_single_segment_with_inline_error`
6. `test_submit_empty_model_does_not_dismiss`
7. `test_submit_after_fix_clears_error_and_dismisses` (two submits)
8. `test_submit_in_edit_mode_preserves_id`
9. `test_submit_in_edit_mode_preserves_quantizations`
10. `test_submit_returns_modelformresult_with_selected_family`
11. `test_submit_returns_family_switched_in_the_select`
12. `test_submit_native_blank_model_defaults_to_native_sentinel`
13. `test_submit_native_named_model`
14. `test_model_form_submit_carries_cost`
15. `test_model_form_untouched_edit_preserves_cost_and_model_info`
16. `test_model_form_llamacpp_edit_preserves_no_cost`
17. `test_model_form_edit_preserves_unset_cost`
18. `test_model_form_empty_per_token_section_shows_error`
19. `test_model_form_missing_subscription_price_shows_error`
20. `test_model_form_both_sections_combined`
21. `test_model_form_bad_price_shows_error_and_stays_open`

Representative examples of the exact replacement:

`test_submit_ollama_tag_produces_correct_spec` (add mode, fills model):
```python
        _fill_model(app, "ornith-1.5:35b")
        await pilot.pause()
        await _submit(app, pilot)
```

`test_submit_empty_model_does_not_dismiss` (add mode, no fill):
```python
        # Don't fill anything; just submit.
        await _submit(app, pilot)
```

`test_submit_in_edit_mode_preserves_id` (edit mode, model disabled → Save button):
```python
        # Replace with a new repo+file.
        _fill_model(app, "baz/quux/new.gguf")
        await _submit(app, pilot)
```

`test_model_form_bad_price_shows_error_and_stays_open` — also update the stale comment. Replace:
```python
        _fill_model(app, "test:1b")  # valid tag so the ONLY failure is the price
        app.screen.query_one("#input-price", Input).value = "abc"
        # Focus stays on #model, whose value is valid: Enter submits, and the
        # submit must fail on the cost field rather than the model name.
        await pilot.press("enter")
        await pilot.pause()
```
with:
```python
        _fill_model(app, "test:1b")  # valid tag so the ONLY failure is the price
        app.screen.query_one("#input-price", Input).value = "abc"
        # _submit focuses the model Input (add mode), whose value is valid:
        # Enter submits, and the submit must fail on the cost field rather
        # than the model name.
        await _submit(app, pilot)
```

- [ ] **Step 4: Run the full test_forms.py suite**

Run: `cd modelman && uv run pytest tests/screens/test_forms.py -q`

Expected: all tests PASS (including the 4 new ones from Task 2).

- [ ] **Step 5: Commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git add modelman/tests/screens/test_forms.py
git commit -m "test(forms): adapt submit/focus tests to immutable identity fields"
```

---

## Task 4: Update test_models.py discard test

**Files:**
- Modify: `modelman/tests/screens/test_models.py`

`test_discard_reverts_immediately_saved_registry_edit` edits the model's `location` in edit mode — but location is now disabled in edit mode. Rework it to edit the cost (subscription pricing) instead, which is still editable, and keep the same discard-revert purpose.

- [ ] **Step 1: Add `Checkbox` and `Button` to the widget imports**

At the top of `modelman/tests/screens/test_models.py`, change:

```python
from textual.widgets import DataTable, Input, Select, Static
```

to:

```python
from textual.widgets import Button, Checkbox, DataTable, Input, Select, Static
```

- [ ] **Step 2: Rework the edit step**

In `test_discard_reverts_immediately_saved_registry_edit`, replace the edit block:

```python
        # Edit the model: open ModelForm, change location, submit.
        await pilot.press("e")
        await pilot.pause()
        app.screen.query_one("#location-select", Select).value = "local"
        await pilot.pause()
        app.screen.query_one("#model", Input).focus()
        await pilot.press("enter")
        await pilot.pause()

        # The edit saved to disk immediately; confirm the file has the new location.
        reg_after_edit = load_registry(reg_path)
        assert reg_after_edit.model("ollama/glm-5.3:cloud").location == "local"
```

with:

```python
        # Edit the model: open ModelForm, add subscription pricing, submit.
        await pilot.press("e")
        await pilot.pause()
        app.screen.query_one("#subscription-checkbox", Checkbox).value = True
        await pilot.pause()
        app.screen.query_one("#subscription-price", Input).value = "20"
        # Submit via the Save button: the model Input is disabled in edit mode.
        app.screen.query_one("#save", Button).focus()
        await pilot.press("enter")
        await pilot.pause()

        # The edit saved to disk immediately; confirm the file has the new cost.
        reg_after_edit = load_registry(reg_path)
        assert reg_after_edit.model("ollama/glm-5.3:cloud").cost == Cost(
            subscription_price=20.0, subscription_period="month"
        )
```

- [ ] **Step 3: Update the discard assertion**

Replace the final assertion:

```python
    # After discarding, the registry file must be reverted to the original location.
    reg_after_discard = load_registry(reg_path)
    assert reg_after_discard.model("ollama/glm-5.3:cloud").location == "cloud"
```

with:

```python
    # After discarding, the registry file must be reverted to the original
    # (no-cost) state.
    reg_after_discard = load_registry(reg_path)
    assert reg_after_discard.model("ollama/glm-5.3:cloud").cost is None
```

- [ ] **Step 4: Run the reworked test**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -k "discard_reverts_immediately_saved_registry_edit" -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git add modelman/tests/screens/test_models.py
git commit -m "test(models): rework discard-revert test to edit cost (location is now immutable)"
```

---

## Task 5: Update test_app_navigation.py edit-mode tests

**Files:**
- Modify: `modelman/tests/screens/test_app_navigation.py`

Four edit-mode tests break. Two submit via Enter while focus is on the family Select (now the edit-mode focus); two edit `location` (now disabled in edit mode). Fix all four.

- [ ] **Step 1: Add `Button` and `Cost` to the imports**

At the top of `modelman/tests/screens/test_app_navigation.py`, change:

```python
from textual.widgets import DataTable
```

to:

```python
from textual.widgets import Button, DataTable
```

and change the `from modelman.registry import (` block to include `Cost`:

```python
from modelman.registry import (
    AuthConfig,
    Cost,
    FamilyEntry,
    ModelEntry,
    ProviderEntry,
    Registry,
    load_registry,
    save_registry,
)
```

- [ ] **Step 2: Fix `test_edit_model_move_to_other_family`**

Replace:

```python
        sel.value = "gemma4"
        await pilot.press("enter")  # submit the (prefilled) edit form
```

with:

```python
        sel.value = "gemma4"
        # Submit via the Save button: the model Input is disabled in edit mode.
        app.screen.query_one("#save", Button).focus()
        await pilot.press("enter")  # submit the (prefilled) edit form
```

- [ ] **Step 3: Fix `test_edit_model_same_family_drops_queued_move`**

Replace:

```python
        sel.value = "gemma4:26b-mlx"  # ...moved back to the screen family
        await pilot.press("enter")
```

with:

```python
        sel.value = "gemma4:26b-mlx"  # ...moved back to the screen family
        # Submit via the Save button: the model Input is disabled in edit mode.
        app.screen.query_one("#save", Button).focus()
        await pilot.press("enter")
```

- [ ] **Step 4: Rework `test_edit_model_location_change_persists_to_registry_on_back`**

Rename the test to `test_edit_model_cost_change_persists_to_registry_on_back` and replace the edit block:

```python
        from textual.widgets import Select

        # Ollama is the editable-location provider; flip local -> cloud.
        loc = app.screen.query_one("#location-select", Select)
        assert not loc.disabled
        assert str(loc.value) == "local"
        loc.value = "cloud"
        await pilot.press("enter")  # submit the (prefilled) edit form
        await pilot.pause()

        assert ms.registry.models[0].location == "cloud"
        # Nothing else queued: escape pops straight back to the previous
        # screen (this is the path that used to drop the edit).
        await pilot.press("escape")
        await pilot.pause()

    reloaded = load_registry(reg_path)
    assert reloaded.model("ollama/gemma4:26b-mlx").location == "cloud"
```

with:

```python
        from textual.widgets import Select

        # Location is immutable in edit mode; edit the cost instead.
        app.screen.query_one("#subscription-checkbox", Checkbox).value = True
        await pilot.pause()
        app.screen.query_one("#subscription-price", Input).value = "20"
        # Submit via the Save button: the model Input is disabled in edit mode.
        app.screen.query_one("#save", Button).focus()
        await pilot.press("enter")  # submit the (prefilled) edit form
        await pilot.pause()

        assert ms.registry.models[0].cost == Cost(
            subscription_price=20.0, subscription_period="month"
        )
        # Nothing else queued: escape pops straight back to the previous
        # screen (this is the path that used to drop the edit).
        await pilot.press("escape")
        await pilot.pause()

    reloaded = load_registry(reg_path)
    assert reloaded.model("ollama/gemma4:26b-mlx").cost == Cost(
        subscription_price=20.0, subscription_period="month"
    )
```

- [ ] **Step 5: Rework `test_location_edit_survives_family_screen_round_trip`**

Rename the test to `test_edit_survives_family_screen_round_trip` and replace the edit block:

```python
        from textual.widgets import Select

        loc = app.screen.query_one("#location-select", Select)
        loc.value = "cloud"
        await pilot.press("enter")
        await pilot.pause()
```

with:

```python
        from textual.widgets import Select

        # Location is immutable in edit mode; edit the cost instead.
        app.screen.query_one("#subscription-checkbox", Checkbox).value = True
        await pilot.pause()
        app.screen.query_one("#subscription-price", Input).value = "20"
        app.screen.query_one("#save", Button).focus()
        await pilot.press("enter")
        await pilot.pause()
```

Then replace the final assertion:

```python
        # LOC column renders the cloud icon for cloud-located models.
        assert rows[0][3] == "↗"
```

with:

```python
        # SUB column renders the subscription price the edit added.
        assert rows[0][7] == "$20.00/mo"
```

- [ ] **Step 6: Run the four reworked tests**

Run: `cd modelman && uv run pytest tests/screens/test_app_navigation.py -k "move_to_other_family or same_family_drops_queued_move or cost_change_persists or survives_family_screen_round_trip" -v`

Expected: all 4 PASS.

- [ ] **Step 7: Commit**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git add modelman/tests/screens/test_app_navigation.py
git commit -m "test(navigation): adapt edit-mode submit and rework location-edit tests to cost"
```

---

## Task 6: Full verification

- [ ] **Step 1: Run the full modelman test suite**

Run: `cd modelman && uv run pytest -q`

Expected: all tests PASS (no failures, no errors).

- [ ] **Step 2: Run the repo lint + link checks**

Run: `cd /Users/keith/github/ohanaverse/local-ai-setup && make lint`

Expected: PASS (shell lint + markdown link check). The plan doc itself is under `docs/superpowers/plans/` — confirm `bin/check-links` does not flag it (it should not, since it contains no repo-relative markdown links).

- [ ] **Step 3: Confirm no stale `litellm_exposed` guide drift**

This change touches no modelman state, so the guide snapshots are unaffected. Still, run the CLAUDE.md-mandated grep to be safe:

Run: `cd /Users/keith/github/ohanaverse/local-ai-setup && git grep -n "litellm_exposed = " docs/guides/`

Expected: output unchanged from before this work (no new drift introduced).

- [ ] **Step 4: Final commit (if any uncommitted changes remain)**

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git status
```

If clean, done. If not, commit the remaining changes with a descriptive message.

---

## Self-Review

**1. Spec coverage:**
- *Field order (family before provider before model before location)* → Task 1 Step 1 (reorder) + Task 2 Step 1 (test).
- *Edit mode: provider, model, location disabled; family and pricing enabled* → Task 1 Step 1 (`disabled=editing`) + Task 2 Step 2 (test).
- *Add mode: provider enabled and focused on mount; model/location enabled* → Task 1 Step 2 (focus) + Task 2 Step 3 (test).
- *Provider-kind location locking still applies in add mode* → Task 1 Step 1 keeps `disabled=editing or location_locked`; existing tests `test_modelform_location_locked_cloud_for_native_provider`, `test_modelform_location_locked_local_for_omlx`, `test_modelform_location_editable_for_ollama`, and `test_modelform_provider_change_updates_placeholder_and_location` already cover it and are untouched.
- *Regression: edit save does not change the model id* → existing `test_submit_in_edit_mode_preserves_id` (updated in Task 3 to use `_submit`) still asserts `spec["id"] == "old-hand-rolled-id`; the disabled model Input makes a new id impossible.
- *Edit-mode location prefill quirk for non-locked kinds stays, rendered disabled* → Task 1 Step 1 keeps the existing `location_value` computation and adds `disabled=editing`.

**2. Placeholder scan:** No TBD/TODO/placeholder patterns. Every code step shows the exact replacement. The Task 3 Step 3 list enumerates all 21 affected test functions by name with representative full examples for each distinct pattern (add-with-fill, add-no-fill, edit-mode, error-with-stale-comment).

**3. Type consistency:**
- `_submit(app, pilot)` helper signature is used consistently across all Task 3 replacements.
- `#save` Button focus is used consistently in Task 4 and Task 5 edit-mode submits.
- `Cost(subscription_price=20.0, subscription_period="month")` is used consistently in Task 4 and Task 5 reworked tests.
- The SUB column index `rows[0][7]` matches the 9-column layout (FAMILY=0, PROVIDER=1, MODEL=2, LOC=3, STATUS=4, EXPOSED=5, COST=6, SUB=7, SIZE=8) verified in `test_model_screen_columns_and_details_panel`.
- `_focused_id(app)` is used in the new Task 2 tests; it already exists in `test_forms.py` and takes the app (not pilot) — consistent with existing usage.

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-09-15-model-form-immutable-identity.md`. Two execution options:

**1. Subagent-Driven (recommended)** - I dispatch a fresh subagent per task, review between tasks, fast iteration

**2. Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints

**Which approach?**
