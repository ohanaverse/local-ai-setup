# Modelman — move models between families via the edit form

Date: 2026-08-29
Status: approved design, pending implementation

## Problem

The migrate/sync path gave every model a family that is 1:1 with its model
name (`family` == `model_name` for all 22 entries in `registry.toml`). This
is wrong: e.g. the four `gemma4:*` variants are four separate "families"
that should be one `gemma4` family with four models.

New families can be created on the family screen (`a`; they live in
`state.families` and may be empty), but there is no way to change which
family an existing model belongs to: `ModelForm` doesn't show `family` at
all, and `family` is otherwise only ever derived from the family screen the
model screen was opened from.

## Goal

Add a family selector to the model edit form so models can be moved into
the correct family from the TUI.

Key simplifying fact: `ModelEntry.id` is the global key everywhere —
`modelman.toml` state entries, LiteLLM `model_list` entries, benchmark
results — and `family` is just a string field on `ModelEntry`. Moving a
model therefore has zero downstream effects outside `registry.toml`.
Families are derived (`registry.families() ∪ state.families`), so a family
that loses its last model and was never explicitly created disappears from
the family list on its own; no cleanup step is needed.

## Decisions (from brainstorming)

1. **The selector appears on add as well as edit.** Add-mode default is the
   family whose screen you're in; choosing another family files the new
   model there directly.
2. **Moves are queued, not saved immediately.** A family change joins the
   model screen's existing queue → confirm-on-exit → apply → discard
   machinery, exactly like queued downloads/deletes/exposes. Discard must
   cleanly revert it; nothing hits disk until the exit confirm.
3. **Existing families only** in the selector (registry families plus
   explicitly created empty ones). No inline family creation; families are
   still created with `a` on the family screen.

Rejected alternative: a separate "move model" modal (`m` key). It keeps
`ModelForm` untouched but contradicts the edit-screen requirement and needs
the same queue plumbing anyway.

## Design

### 1. `ModelForm` (src/modelman/screens/forms.py)

- New arguments: `families: list[str]` (all families known to the TUI:
  registry families + `state.families` entries, sorted) and `family: str`
  (the family the model screen is showing — the current family).
- A Textual `Select` labelled `Family:` composes above the model-name
  `Input`, one option per known family, pre-selected to the current family.
  If the current family is not in `families` it is prepended defensively.
- **Re-edit rule:** in edit mode the pre-selection honors an already-queued
  move for this model (passed in by the screen) over the screen family —
  otherwise re-editing a moved model would silently drop the pending move.
- **Return type** changes from `VariantSpec | None` to
  `ModelFormResult(NamedTuple)` with fields `(spec: VariantSpec, family:
  str)`. `family` is read from the Select at save time. Deliberately not
  stuffed into `VariantSpec` — that dict is the provider-facing contract.
- Existing `ModelForm(...)` call sites (mostly tests) receive the new
  arguments. Defaults: `family=None` and `families=None`, in which case the
  selector shows exactly one entry, `[family]` (or the first provider-less
  fallback `"unknown"` if both are None) — the TUI always passes both, so
  only direct test callers rely on the defaults. The modal model screen
  (`modelman download <family>`) opens the same `ModelScreen`, so it gets
  the full behavior for free.

### 2. `ModelScreen` (src/modelman/screens/models.py)

- Fourth queue: `queued_moves: dict[str, str]` (model id → target family).
  Pure queue state — the in-memory registry entry's `family` is NOT
  mutated, so the model still renders in the current family's table until
  apply (same convention as a queued delete).
- `_on_edit_model`: rebuilds the entry as today (in-memory family stays
  `self.family`); if the form's returned family differs from
  `self.family`, records `queued_moves[id] = target`. If it equals
  `self.family`, any queued move for that id is dropped (move-back is a
  no-op).
- `_on_add_model`: the appended entry uses the form's family. If it differs
  from the screen family the row won't render in this family's table after
  reload (it lives elsewhere); its queued download still shows in the
  pending bar.
- Visible state:
  - Pending bar: `Pending: download N · delete N · move N · expose N`.
  - Status column gains a fifth state with priority
    `✗ delete > ↓ download > → move > ✓ downloaded > ○`; a model with a
    queued move stays in the table showing `→`.
- Exit flow keeps its shape: `action_back` pops only when all four queues
  are empty; `ConfirmExitDialog` gains a `moves` list rendered as
  `→ <id> → <target family>` lines and a `move N` count; y/n/d semantics
  unchanged.
- **Snapshot fixes** (`_restore_snapshot` — the only genuinely tricky
  part):
  1. Restore is keyed by **model id** rather than family membership so a
     moved model can never be duplicated between the snapshot and the
     live list.
  2. `ModelScreen` tracks `_added_ids: set[str]` (populated in
     `_on_add_model`); discard also removes session-added models —
     including ones filed into *other* families, which the old
     family-scoped filter never caught. Unrelated other-family models are
     untouched.

### 3. `PendingChanges` (src/modelman/queue.py)

- New field: `moves: list[tuple[str, str]]` (`model_id`, `new_family`).
- Ordering: deletes → moves → downloads → exposes → single
  `save_registry` + `save_state`. Moves sit right after deletes so a
  same-apply delete beats a move; downloads are unaffected (state is keyed
  by model id).
- The empty-queue early return includes `moves`, so a move-only session
  still runs apply and persists the save.
- Application: `registry.model(model_id).family = new_family`. A move for
  a model deleted earlier in the same apply is dropped (delete ran first).
  Any other failure records `move:fail` and continues — existing per-step
  failure isolation, nothing aborts the run.
- Event tags (4-part, mirroring download tags):
  - `move:start|<id>|<label>|<new_family>`
  - `move:done|<id>|<label>|<new_family>`
  - `move:fail|<id>|<label>|<reason>`

### 4. `StatusScreen` (src/modelman/screens/status.py)

- `move:` joins the lifecycle-verb prefix set; without this the tags would
  render as unformatted dim lines.
- Renderings: `· Moving <label> → <target>…` / `✓ Moved <label> →
  <target>` / `✗ Failed to move <label>` (+ reason).

### Non-goals / verified no-impact

- LiteLLM `model_list` (keyed by model id) — untouched.
- `modelman.toml` state entries (keyed by model id) — untouched.
- `sync` (no family logic) — untouched.
- `wt`'s read-only registry join (by id) — untouched.
- No batch-move UI: fixing gemma4 is per-model `e` → pick family. Four
  models, four edits.
- Out of scope: the pre-existing gap where a pure registry edit with an
  empty queue is lost on exit (moves introduced here do not have this
  issue — they are queued and persisted by apply).

## Usage walkthrough (the gemma4 fix)

1. Family screen: `a` → create `gemma4`.
2. Open each stale family (`gemma4:26b-mlx`, `gemma4:12b-mlx`,
   `gemma4:31b-mlx`, `gemma4:cloud`), `e` on the model, pick `gemma4`,
   Save.
3. Exit → confirm → `move` entries apply; the emptied stale families drop
   out of the list automatically (they were never in `state.families`).

## Testing

- `tests/screens/test_forms.py`
  - Select renders all families; defaults to the current family in add and
    edit modes; re-edit honors a queued move's target.
  - Result carries `(spec, family)`; existing call sites updated.
- `tests/screens/test_models.py`
  - Edit→change family: `→` glyph, `move 1` pending bar, registry family
    still unchanged in memory.
  - Move back: queue empties; escape pops with no exit dialog.
  - Exit dialog lists the move; apply (tmp registry/state paths) persists
    the new family.
  - Discard after move/add-with-different-family leaves the registry as at
    mount (no duplicates, no stray out-of-family adds).
- `tests/test_queue.py`
  - Moves applied after deletes; move for a deleted model dropped;
    move-only queue still saves; tags fire in order.
- Status screen test for the three `move:*` renderings.