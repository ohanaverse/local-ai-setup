# Family-Aware Usage Sort for the Model Picker

Date: 2026-09-01

## Problem

The `wt` model picker lists eligible models in registry order with per-model
1d/7d/30d usage counts, but there is no notion of *family* usage and no
usage-based ordering. The user cannot tell at a glance which families (base
models) they rely on most, which makes it hard to consciously vary between
families. This work counts usage by family *in addition to* model, and
offers the picker sorted descending by family usage then model usage, so the
ranking surfaces heavily-used families first.

## Goals

- Report 1d/7d/30d usage by **family** in addition to by model.
- Sort the model picker's eligible rows **descending** by family usage, then
  model usage, using a recency-biased composite score.
- Visually group same-family model rows so "choose a different family"
  scanning is easy.
- Keep existing per-model counts and rotation-based cursor behavior intact.

## Non-Goals

- No change to the non-TUI `resolve.go` path (it never offers a list; multi-
  match falls through to rotation).
- No schema change to `usage.jsonl` — family is derived from the registry at
  query time, so existing history stays valid.
- No change to which models are eligible, only to their presentation order.

## Design

### 1. Usage layer — `internal/usage/usage.go`

Keep `Store.Counts(modelIDs)` unchanged (per-model 1d/7d/30d). Add two new
pieces:

- `FamilyCounts(familyOf map[string]string) map[string]UsageCounts` — one
  pass over `usage.jsonl`, attributing every event to its family via a
  model-id → family map, returning per-family 1d/7d/30d buckets. The
  `familyOf` map is built from the **full catalog** (`cfg.Models`), not the
  currently-eligible subset, so a family's total usage is accurate even when
  the active tag/family filter exposes only some of its models.
- `CompositeScore(UsageCounts) int` — the recency-biased sort key:
  `6·OneDay` (today, ≈3×) + `3·(SevenDay−OneDay)` (1–7d, ≈1.5×) +
  `2·(ThirtyDay−SevenDay)` (8–30d, ≈1×). Integer math (×2) with each launch
  counted exactly once, weighted by freshness.

### 2. Picker layer — `internal/tui/model_list.go` + `internal/tui/app.go`

**Sorting.** In `enterModelPhase` (and the `phaseAgent` Enter path), stably
sort the eligible models by `CompositeScore(familyCounts[m.Family])` desc,
then `CompositeScore(modelCounts[m.ID])` desc. Same-family models end up
adjacent, higher-used families at the top.

**Grouping (dividers).** Before rendering, insert a **non-selectable family
divider row** at the top of each distinct family group, rendered as
`◈ <family> · 30d:<count>`. Models with an empty `Family` group under
`— other`.

**Row description.** Add a `fam` column to each model row (e.g. `fam:3`)
next to the existing 1d/7d/30d counts, so the sort is self-evident while
scanning.

**Rotation preserved.** After the sorted list is built, `rotation.Next`
still moves the cursor to the next-to-use model (the "spread your usage"
behavior is kept). The sort only reorders the rows.

**Selection integrity.** Divider rows are headers only — they are never
launchable. After any navigation the current selection is snapped off a
divider to the nearest model row. `proceedToLaunch`'s
`SelectedItem().(modelItem)` type assert already rejects a divider (it would
surface "no model selected"); the snap prevents that from ever being seen
by the user.

### 3. Data flow

```
enterModelPhase(models, ...)
  modelCounts  = usage.NewStore().Counts(modelIDs(models))         // per-model
  familyOf     = buildModelFamilyMap(cfg.Models)                    // full catalog
  familyCounts = usage.NewStore().FamilyCounts(familyOf)            // per-family
  sorted = sortDesc(models, key = CompositeScore(family) desc, CompositeScore(model) desc)
  items  = withFamilyDividers(sorted, familyCounts)                 // divider rows inserted
  list   = buildModelList(items, modelCounts, familyCounts, ...)
  rotation.Next(...) → select that model's row (skipping dividers)
```

### 4. Error handling

- Missing/malformed `usage.jsonl` already returns zero counts (`Counts`);
  the family path behaves the same (zero buckets, order falls back to equal
  scores, stable).
- A model with no entry in `familyOf` (shouldn't happen, since families come
  from the same catalog) is treated as family `""` → the `— other` group.

## Testing

- **Usage:** `FamilyCounts` aggregates correctly across multiple families
  and preserves 1d/7d/30d bucket semantics; `CompositeScore` matches the
  weighted formula and stays integer.
- **Picker:** sorted order is (family score desc, model score desc); stable
  for ties; same-family models are adjacent; a divider is inserted before
  each distinct group; empty-family models land under `— other`.
- **Selection:** a regression test proves arrow navigation can never leave
  the selection on a divider, and that a divider is never the model used for
  launch, rotation recording, or cursor positioning.
- **Rotation:** cursor still lands on the next-to-use model after the sort
  is applied.

## Compatibility

- `usage.jsonl` schema unchanged; existing history continues to work.
- `Counts` unchanged; existing callers and tests are unaffected.
- TUI-only; non-TUI resolve path untouched.
