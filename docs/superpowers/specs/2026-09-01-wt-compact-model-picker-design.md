# Compact One-Line Model Picker (wt) — Design

**Date:** 2026-09-01
**Status:** Approved
**Scope:** `wt/internal/tui/` (model picker only; agent/worktree pickers untouched)

## Problem

The `wt` model picker renders each model as three lines (ID title, metadata
description, blank spacing) and each family header as two (header, blank), via
bubbles' `DefaultDelegate` (Height 2 / Spacing 1). A 12-model catalog burns
roughly 40 rows, most of it blank space and redundant metadata. The user wants
a dense, one-line-per-model list with the family inline on every row.

## Decision

Every model row becomes one self-contained line carrying its family and the
family's 30-day launch count up front:

```
<family> <fam30d> <ID> <location> <1d>/<7d>/<30d> [tags]
```

Rendered with a stock themed `DefaultDelegate` configured for
`ShowDescription = false` and `SetSpacing(0)` — Height 1, no blank lines. The
full formatted line is the item's `Title()` **and** `FilterValue()`, so the
stock delegate's filter-match underlining and ellipsis truncation work with no
custom rendering code. Family divider rows are removed entirely.

Example (from the current registry):

```
  glm-5.3     131  ollama/glm-5.3-flash:cloud      cloud  65/127/127
  glm-5.3     131  ollama/glm-5.3:cloud            cloud  2/4/4
  deepseek-v4  57  ollama/deepseek-v4-flash:cloud  cloud  6/30/32
  deepseek-v4  57  ollama/deepseek-v4-pro:cloud    cloud  1/21/25
  kimi-k2.7    35  ollama/kimi-k2.7-code:cloud     cloud  6/33/35
  ornith-1.5   14  ollama/ornith-1.5:35b           local  0/5/14
  qwen3.8       5  ollama/qwen3.8:27b-mlx          local  0/0/5
```

### Line format rules

- **Columns are dynamically padded** to the widest value in the current
  eligible list (family name, ID, location, counts) — no hardcoded widths, so
  `nemotron-3.5-lightning` widening the family column still aligns everything.
- `fam30d` is right-aligned (reads as a column: `131`, ` 14`, `  5`).
- Model counts are bare `65/127/127` in 1d/7d/30d order — no `1d:` labels.
- Tags are appended as ` [a,b]` **only when non-empty**.
- Empty family renders `-` in the family column, but its right-aligned `fam30d`
  still shows the real 30-day aggregate for the unnamed ("other") bucket — the
  display matches the sort key (composite of the family's totals), never a
  hardcoded 0.
- Dropped from the old description line: **provider** (always the ID's
  prefix) and the per-model `fam:` count (moved up front next to the family).

## Changes

### `wt/internal/tui/model_list.go`

- `modelItem` gains a prebuilt `line` string, composed at list-build time in
  a helper replacing `withFamilyDividers` (usage counts and column widths are
  known there). `Title()` and `FilterValue()` return it; `Description()`
  returns `""` (the `DefaultItem` interface requires the method — stock
  `DefaultDelegate.Render` calls it even when the description is hidden).
- `buildModelListWithFamilies` keeps its sorting, usage reads (single
  `usage.Store.Counts` pass), and `idIndex` cursor-positioning contract; it
  stops inserting dividers and instead computes column widths and builds
  model items with their `line`.
- The model list's delegate becomes `ThemedListDelegate(theme)` with
  `ShowDescription = false` and `SetSpacing(0)` (configured on the returned
  copy; the exported helper itself is unchanged — other pickers and
  `internal/ollamaconfig` keep their two-line defaults).
- **Deleted:** `dividerItem`, `familyDividerLabel`, `otherFamily`,
  `withFamilyDividers`, `modelListDelegate`, and the header style it carried.
- `snapSelectionOnModel` slims to the out-of-range cursor clamp only. The
  bubbles v1.0.0 filter-narrowing bug it guards (Index() not clamped when a
  filter shrinks VisibleItems()) still needs that clamp, so the function and
  its coordinate-space commentary survive; the divider-skipping walk and
  direction-of-travel logic do not.

### `wt/internal/tui/app.go`

- Wrap-around navigation on the model screen (up-at-first → last,
  down-at-last → first) keeps its behavior but simplifies to direct `0` /
  `len(VisibleItems())-1` checks; `firstModelIndex` and `lastModelIndex`
  (in `model_list.go`) are deleted.
- The `prev := m.models.Index()` capture before `m.models.Update` goes away
  (only the divider walk needed it); the clamp is called without it.
- Initial cursor positioning on the rotation's next-to-use model is
  unchanged (idIndex values shift because dividers are gone, but the
  contract is identical).

### Unchanged on purpose

- **Sort order** (`sortModelsByUsage`) and therefore launch rotation —
  same-family models still end up adjacent (cosmetic grouping now, no longer
  load-bearing for dividers).
- `phaseModelView` header/footer/layout.
- Pagination needs no code: `PerPage = availHeight / (Height + Spacing)`
  becomes the full height, so the whole catalog typically fits one page.

## Filtering

`FilterValue` = the full line, so typing `glm` or `deepseek` narrows to a
family (new capability — dividers used to vanish under filtering, taking all
family context with them) and typing an ID substring works as today. The
stock delegate's matched-rune underlining stays aligned because
`Title() == FilterValue()`. Digit queries can match count columns —
harmless.

## Testing

- Rewrite render assertions in `model_list_test.go`, `model_family_test.go`,
  and `agent_model_test.go` (~80 lines of divider/render references across
  five test files): line format (family prefix, right-aligned fam count,
  slash counts, location, tags only when present, `-` for empty family),
  dynamic column padding, no divider items in the built list, clamp behavior
  on filter narrowing, wrap-around navigation.
- `make vet`, `make test` (exercises the installed binary), `make lint`
  (repo umbrella).
- Manual `wt` visual check on a real terminal.

## Docs

- `wt/CHANGELOG.md` entry.
- Sweep `wt/docs/` and `wt/CLAUDE.md` for model-picker layout mentions and
  update any that describe the two-line rows or family dividers.