# Modelman — first-class families in registry.toml

Date: 2026-08-29
Status: approved design, pending implementation

## Problem

The family screen lists a family only if it has ≥1 model in the registry or a
`state.families` entry in `modelman.toml` (created only by the Add/Edit Family
modals). When a family's models are moved out or deleted — e.g. consolidating
the migrate-era 1:1 families into shared ones, the workflow the family-move
feature (#22) was built for — a never-state-touched family simply vanishes
from the screen. There is then no way to delete it explicitly, and the
emptied-by-move behavior was an accident of derivation, not a choice.

The same gap makes the family-delete dialog's own advice unactionable: it
blocks deletion of a family with downloaded models and says "Remove downloads
first" — but deleting that download (model `d`) also removes the model entry,
so a single-model family vanishes before the user can ever press `d` on it.

Notably, families that already vanished leave no residue anywhere (no
registry entry, no state entry), so this fix applies going forward only; it
makes emptied families *linger* so deletion is an explicit act.

## Goal

A family that had models stays visible as a 0-variant row after its models
are moved out or deleted, until the user deletes it with the existing
`d` flow. Mechanism: families become first-class `[[families]]` entries in
the shared `registry.toml`, with display names living there too.

## Decisions (from brainstorming)

1. **Emptied families linger when emptied by moves *and* deletes.** One
   consistent rule: a family that had models sticks around until explicitly
   deleted. This also makes "remove downloads first, then delete the
   family" work end to end.
2. **First-class `[[families]]` in `registry.toml`** (chosen over
   `state.families` tombstones). Families are real entities in the canonical
   shared file; delete removes the entry. Verified safe for the read-only
   consumer: agent-worktree's non-strict BurntSushi decode ignores the new
   section.
3. **Tolerant union, not a strict canonical list.** Models may reference
   families without entries; derived families remain visible. Rejected the
   strict model because it forces entry-creation into sync/migrate and turns
   hand-edited registries into errors.
4. **Display names move into registry entries.** `state.families` becomes a
   read-side legacy fallback, drained lazily: any TUI write path that touches
   a family writes the registry entry and drops that family's state entry in
   the same save ("promotion"). No startup-time migration.

## Design

### 1. `registry.py` — schema and data model

New dataclass and list field, following the `ProviderEntry`/`ModelEntry`
conventions (unknown keys preserved via `extra`, atomic save):

```toml
[[families]]
name = "deepseek-v4"
display_name = "DeepSeek V4"   # optional; omitted when unset
```

- `FamilyEntry(name: str, display_name: str | None = None, extra: dict)`.
- `Registry.families: list[FamilyEntry]` — **the new field replaces the
  existing `families()` method**, which is renamed `derived_families()` and
  keeps its behavior (`sorted({m.family for m in self.models})`). Callers:
  `FamilyScreen.reload()`, `ModelScreen._families_list()`, two asserts in
  `tests/test_registry.py`.
- `Registry.family(name) -> FamilyEntry | None` — first-match lookup;
  absence is normal (unlike `provider()`/`model()`, which raise). Duplicate
  names are tolerated, not validated — same tolerance as duplicate model
  ids today (first match wins).
- `_parse_family` requires `name` (missing → `RegistryError`, mirroring
  `_parse_provider`); `_family_to_dict` drops unset `display_name`.
- `save_registry` writes `providers`, then `families`, then `models`.
  A registry without the section loads as `[]` — every existing file is
  valid.

Two module-level helpers (with a `TYPE_CHECKING`-only import of
`StateStore`; runtime access is duck-typed, so no import cycle):

```python
def known_families(registry, state) -> list[str]:
    """Sorted union: derived families | entry names | legacy state keys."""

def family_display_name(registry, state, family) -> str | None:
    """Registry entry display_name, else legacy state display_name, else
    None. Callers decide the fallback (table column: ""; edit prefill:
    the family name)."""
```

### 2. Visibility rule

`FamilyScreen.reload()` iterates `known_families(registry, state)`;
`ModelScreen._families_list()` (the add/edit dialog's family selector)
returns the same. A family is visible iff it has ≥1 model, or a
`[[families]]` entry, or a legacy `state.families` entry.

### 3. Family screen actions

- **Add (`a`)** — modal result `(name, display or name)` as today. Update
  the entry's `display_name` if it exists, else append
  `FamilyEntry(name, display)`. Promotion: `state.forget_family(name)`.
  Save registry + state immediately (same immediacy as today, different
  file), then `reload()`.
- **Edit (`e`)** — prefill `family_display_name(...) or family` (preserves
  today's fallback-to-name prefill). On save: update-or-create the entry
  with the new display name, promotion, save both files, then
  `_refresh_from_disk()` as today.
- **Delete (`d`)** — confirm flow unchanged ("is empty. Delete?" /
  variants / downloaded guards all keep their texts). `_delete_family`
  additionally removes the `FamilyEntry` when present and saves the
  registry alongside state.
- The DISPLAY column shows `family_display_name(...) or ""` — identical
  rendering to today for every existing case.

### 4. `PendingChanges.apply()` — stickiness (the core fix)

- During the **deletes** phase, after a successful provider delete, record
  the model's family (read from the registry before the entry is removed;
  failed deletes record nothing — the model stays).
- During the **moves** phase, record each moved model's source family
  before assignment.
- After the moves loop (family membership is final there; downloads and
  exposes don't affect it), for every recorded family that now has zero
  models in the registry:

  ```python
  entry = registry.family(f)
  legacy = state.families.get(f)
  if entry is None:
      registry.families.append(
          FamilyEntry(name=f, display_name=legacy.display_name if legacy else None))
  elif entry.display_name is None and legacy is not None and legacy.display_name:
      entry.display_name = legacy.display_name   # promote into the entry
  state.forget_family(f)                          # promotion either way
  ```

  Persisted by the existing `save_registry` + `save_state` at the end of
  apply. Placement before downloads is safe under cancellation: a cancelled
  apply saves nothing, and FamilyScreen resumes from disk.
- Families emptied by a same-apply move *in* (another model moved into
  them) are not touched — the zero-models check runs after all membership
  changes. The empty-queue early return is unchanged (nothing to empty).

### 5. Error handling and edge cases

- Missing `[[families]]` section → `[]`; missing `name` in an entry →
  `RegistryError`; unknown keys preserved round-trip.
- Cancelled apply → nothing saved (existing behavior); in-memory touches
  are discarded on FamilyScreen resume-from-disk.
- Move back to the same family → never queued (existing behavior).
- A deleted model's queued move is dropped (delete wins — existing
  behavior); its family is still a stickiness candidate via the delete
  record.
- wt compatibility: verified — `internal/config/registry.go` decodes only
  `providers`/`models`, non-strict.

### Non-goals

- No strict validation of dangling family references.
- sync/migrate unchanged (migrate continues to ignore legacy manifest
  display names — pre-existing behavior, not a regression).
- No retroactive resurrection of families that already vanished (no
  residue exists).
- No `state.families` schema change: entries stay loadable; writers stop
  creating them and drain them via promotion.
- No new UI: the existing row rendering and `d` confirm flow serve as-is.

## Usage walkthrough

1. If the target family doesn't exist yet, create it on the family screen
   with `a` (this now writes a `[[families]]` entry).
2. Open family `deepseek-v4`, `e` each model, pick the target family in
   the selector (the gemma4-style consolidation).
3. Apply. Back on the family screen, `deepseek-v4` still shows —
   `0` variants, `0` downloaded — because apply recorded a `[[families]]`
   entry.
4. `d` on it → "Family 'deepseek-v4' is empty. Delete?" → entry removed,
   registry saved. Gone for good.

## Testing

- `tests/test_registry.py`: `FamilyEntry` round-trip; missing section →
  `[]`; missing `name` → `RegistryError`; unknown-key preservation;
  `derived_families()` (renamed asserts); `known_families()` union;
  `family_display_name()` resolution order; `Registry.family()` lookup.
- `tests/test_queue.py`: move-empties → entry created with display name
  promoted and state entry dropped; delete-empties → entry created;
  family with surviving models → no entry; existing entry not duplicated;
  entry-without-display gains the legacy name before the state entry is
  dropped; cancelled apply persists nothing.
- `tests/screens/test_family_edit.py`: Add writes a registry entry;
  Edit updates it and drops the legacy state entry; Delete removes it.
- `tests/screens/test_models.py`: after an apply that moves the last
  model out, FamilyScreen shows the emptied family with `0` variants
  (extend the existing apply pilot test).
- README: `registry.toml` section gains `[[families]]`; the `modelman.toml`
  config-table row drops "family display names" (now registry data);
  `registry.py` and `state.py` docstrings updated (families now part of
  the shared schema; `state.families` marked legacy read-side fallback).