# Model Form: Immutable Identity Fields (Issue #52)

Date: 2026-09-15
Status: Approved

## Problem

The model add/edit dialog allows editing the model name on an existing
model, but changing a model's name should cascade through config,
downloads, exposure state, etc. — and does not. Renaming is broken.
Since the complexity of supporting renames is not worth it, the dialog
is changed so identity fields (provider, model name, location) can only
be set on add.

## Decision

Rework `ModelForm` in `modelman/src/modelman/screens/forms.py` only. No
registry or state-store changes; this is purely dialog behavior. The
issue's premise (renaming cascades badly) is sidestepped by making
name/provider/location add-only.

Locked fields are rendered **disabled inputs showing their current
values** (option A) — consistent with the existing edit-mode provider
Select behavior and the `EditFamilyModal` read-only pattern. Layout is
identical between add and edit.

## Field Order (both add and edit)

1. **Family** (Select) — display-only metadata, promoted to top
2. **Provider** (Select) — add: enabled, **focused on dialog entry**;
   edit: disabled, shows current value
3. **Model** (Input) — add: editable; edit: disabled with current value
4. **Location** (Select) — add: editable with existing provider-kind
   locking (native/cloud-only → cloud & locked; local-only → local &
   locked); edit: always disabled with current value
5. Per-token pricing section — unchanged
6. Subscription pricing section — unchanged

## Behavior Details

- `_modal_on_mount` focus: add → `#provider-select`; edit →
  `#family-select` (first enabled field). Pricing-section visibility
  toggles unchanged.
- `on_select_changed` (provider change → placeholder + location relock)
  continues to run only in add mode; logic unchanged.
- `_submit()` unchanged — edit mode already preserves `id`, provider,
  name, repo/files; only cost fields are re-read, and those remain
  editable. Disabling the model input means edit mode can never produce
  a new `id`, which is the bug fix.
- Edit-mode location prefill quirk for non-locked kinds
  (`v.get("location") or "local"`) stays, rendered disabled.

## Implementation Approach

Declarative per-field at compose time (option 1): compute `editing` in
`compose()` and set `disabled=editing` on the provider Select, model
Input, and location Select — exactly as the provider Select already
does. The `on_select_changed` location-relock logic stays add-mode-only.
No new state, minimal diff.

## Testing

Update/add tests in `modelman/tests/screens/test_models.py` (and any
forms tests) covering:

- Field order in composed DOM (family before provider before model
  before location)
- Edit mode: provider, model, location all disabled; family and pricing
  enabled
- Add mode: provider enabled and focused on mount; model/location
  enabled
- Provider-kind location locking still applies in add mode
- Regression: edit save does not change the model `id`
