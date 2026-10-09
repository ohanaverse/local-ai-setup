# Family / Model Screen Fixes

**Date:** 2026-08-30
**Status:** Approved design

## Overview

Four fixes from post-redesign use of the TUI, plus two thin shared pieces so
the fixes stay fixed:

1. **Family screen cursor** — returning from the model screen, the background
   size refresh resets the table cursor to row 0 if the user scrolls while
   sizes refresh.
2. **Family screen refresh lock** — the family screen should not accept
   interaction while its metadata (sizes / ready state) is refreshing.
3. **Model screen dropdowns** — provider, family, and location selects should
   list alphabetically.
4. **Delete any model** — native models currently cannot be deleted unless
   ready; delete should work on any model.

Plus a conventions fix applied across every dialog: buttons ordered
default-first / cancel-last, with keyboard shortcuts available on all of them.

Two decisions taken during design:

- **Dialog ordering vs. focus.** Layout is consistent everywhere (cancel
  button rightmost in the pair, with the primary action left of it;
  three-button dialogs put the destructive/secondary action leftmost).
  Destructive dialogs give initial keyboard focus to the safe button,
  so a reflexive Enter is never destructive.
- **Delete semantics for missing artifacts.** "Delete" means: remove the
  model from modelman's registry and state, and remove on-disk artifacts if
  they are present. When a provider-backed model is not on disk, apply skips
  the provider's delete command instead of failing and leaving the row behind.

Out of scope: the model screen gets cursor-preserving reloads but *not* the
interaction lock (its reconcile runs on every mount/resume; locking it would
add a wait on every open). No unrelated refactoring.

## Shared helper: cursor-preserving reload

`src/modelman/screens/__init__.py` (currently empty) gains one function:

```python
def reload_preserving_cursor(table: DataTable, repopulate: Callable[[], None]) -> None
```

- Snapshot the row **key** under the cursor before `repopulate()` runs
  (`DataTable.clear()` resets the cursor to row 0 — the root cause of the
  reported bug).
- After repopulating, restore the cursor onto that key via
  `DataTable.move_cursor(row=...)` (available in the pinned Textual version,
  verified: 8.2.8 has `move_cursor` / `get_row_index`).
- If the key no longer exists (family or model deleted elsewhere), fall back
  to row 0. Empty-table-before or empty-table-after states are no-ops.

Both `FamilyScreen.reload()` and `ModelScreen._load_models()` route their
`clear()` + repopulate through this helper.

## FamilyScreen: interaction lock while refreshing

New `_reconciling: bool` flag on `FamilyScreen`:

- Set True where the reconcile worker starts (`on_mount`,
  `_refresh_from_disk`); cleared in a completion callback via
  `app.call_from_thread`, wrapped in `try/finally` so a worker exception can
  never leave the screen stuck. An `exclusive=True` worker superseded by a
  newer one is cancelled and does not clear the flag — the superseding worker
  clears it when it lands.
- While `_reconciling` is True:
  - the DataTable is `disabled` (no scrolling, clicking, or cursor keys);
  - `action_add_family`, `action_edit_family`, `action_delete_family`,
    `action_open_family`, `action_reconcile`,
    `on_data_table_row_selected` return early;
  - a small "Refreshing sizes…" indicator (`Static`, toggled via `display`)
    is shown so the freeze is legible;
  - `q` (quit) remains live.
- When the worker lands: the flag clears, the indicator hides, the table
  re-enables, and the reload restores the cursor to the family that was
  selected when ModelScreen was pushed (FamilyScreen persists across
  push/pop, so its cursor is the pre-push selection).

## ModelScreen

- **Cursor adoption only** (no lock): `_load_models()` uses
  `reload_preserving_cursor`. The now-redundant
  `mt.cursor_coordinate = Coordinate(0, 0)` in `on_mount` is removed — fresh
  tables already start at row 0.
- **Dropdowns sort alphabetically.** `_provider_list()` returns
  `sorted(...)` of the configured providers — sorting at the dialog boundary;
  `available_providers` storage is untouched.
  `ModelForm.__init__` drops the `self._families.insert(0, self._family)`
  line, so the family Select shows `known_families()`'s plain sorted order
  while its `value` still pre-selects the current family. The location Select
  is already alphabetical (`cloud`, `local`) — no change; noted here so it is
  not "fixed" twice.
- **Delete works on any model.** `action_delete_model` loses the
  `if not self._is_ready(mid)` early-return entirely. Native providers are
  flag-only at apply time, so this alone unblocks them.

## PendingChanges: skip absent artifacts on delete

In `queue.py`'s delete step, when a provider instance exists, check
`provider.is_downloaded(variant)` first:

- Artifact missing → skip the provider delete/unlink, but still emit the
  usual `delete:start` / `delete:done` events and run the existing registry +
  state cleanup (unexpose queueing, family stickiness) unchanged.
- `is_downloaded` raises → keep today's behavior: attempt the artifact
  delete and surface real failures in the status log. An exception means we
  cannot know the artifact is absent, so deleting conservatively is right.

## Dialog conventions: `ModelmanModal`

A thin `ModelmanModal(ModalScreen[T])` base in `forms.py`:

- Standardized button-row construction: default action leftmost, cancel
  rightmost, shared CSS.
- `escape → cancel` binding.
- Destructive dialogs declare a safe-focus button id, focused in `on_mount`;
  form modals keep focusing their input. Nothing is bound per-button — all
  shortcuts are screen-level bindings, which the footer displays
  automatically.

Per dialog:

| Dialog | Buttons (left → right) | Initial focus | Keys |
|---|---|---|---|
| `AddFamilyModal` | Cancel, Create (primary) | family-name Input | escape → cancel |
| `EditFamilyModal` | Cancel, Save (primary) | display-name Input | escape → cancel |
| `ModelForm` | Cancel, Save (primary) | model Input | escape → cancel |
| `ConfirmModal` (destructive) | Yes (warning), No (default) | **No** | y / n / escape (existing) |
| `ConfirmExitDialog` (destructive) | Cancel (default), Discard (warning), Apply (primary) | **Cancel** | y / n / d / escape (existing) |
| `CancelApplyDialog` | Cancel (warning), Wait (primary) | **Wait** | w / c / escape (existing) |

The convention: the **default/cancel button is rightmost** in the pair, the
**primary action sits left of it**, and the **destructive/secondary action
sits leftmost** in three-button dialogs. Destructive prompts focus the safe
button on open. Labels stay as-is; the footer displays the keyboard
shortcuts.

Edge case: with an `Input` focused, Textual may deliver Escape to the input
rather than the screen binding. The implementation must verify escape
cancels from inside inputs (including the disabled family-name input in
`EditFamilyModal`) and adjust (e.g. handle `Input` key events or focus
rules) if the binding does not fire.

## Error handling summary

- Reconcile flag clears via `try/finally`; superseded workers are safe.
- `is_downloaded` exception during delete → conservative artifact-delete
  attempt (today's behavior).
- Cursor restore falls back to row 0 when the snapshot key vanished.
- Form validation paths are untouched.

## Testing

- **Helper unit tests:** cursor restored to the same key after repopulate;
  key-vanished fallback lands on row 0; empty-table no-ops on both ends.
- **Family lock:** while `_reconciling`, actions no-op and the table is
  disabled; after the worker lands, the table is enabled and the cursor sits
  on the originally selected family.
- **Forms:** for all six modals — button order (default first, cancel last),
  initial focus, and alphabetical provider/family select options (family
  entry no longer moved to the front).
- **Models screen:** delete-queued on a not-ready model (gate removed).
- **Queue:** a delete for a not-downloaded provider-backed model skips the
  artifact call and still cleans registry/state/events; a raising
  `is_downloaded` still attempts artifact delete and records failure
  (existing behavior preserved).