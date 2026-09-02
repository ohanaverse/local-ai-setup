# Compact One-Line Model Picker (wt) — Implementation Plan

**Date:** 2026-09-01
**Spec:** `docs/superpowers/specs/2026-09-01-wt-compact-model-picker-design.md`
**Branch:** `wt-compact-model-picker`
**Working directory:** `wt/`

## Context for a fresh executor

This plan is for an agent that has never seen this repo. Read this whole section
before starting.

**What `wt` is.** A Go TUI for picking a local model and launching a worktree
agent against it. The picker is one screen of the app; the user picks a model,
`wt` spawns a `wt` worktree and runs an agent CLI against the chosen model.

**Repo layout.**

- Repo root: `/Users/keith/github/ohanaverse/local-ai-setup/`
- `wt/` — Go module. Own CLAUDE.md, own Makefile, own docs/.
- `wt/internal/tui/` — bubbletea TUI. Files we'll touch:
  - `model_list.go` — item/builder/delegate types for the model picker
  - `app.go` — the bubbletea program; calls into model_list.go
  - `delegate.go` — `ThemedListDelegate(theme) list.DefaultDelegate`
  - `*_test.go` — model_list_test.go, model_family_test.go, agent_model_test.go
    (plus possibly more discovered by grep; ~80 lines of `◈`/`divider` refs)
- `wt/internal/usage/store.go` — read-only here. Exposes
  `Counts(modelID string) map[string]int` and `Family(modelID string) string`.
  - **Key behaviour**: `Counts(modelID)` treats `modelID` as a family name first
    and aggregates launches across every model in that family. So
    `s.Counts("glm-5.3")["30d"]` is the family's 30-day launch count — exactly
    the number the current divider header `◈ glm-5.3 · 30d:131` shows.

**What we're building.** Today the model picker renders each model as
three lines (ID / metadata / blank spacing) and each family header as two
(header / blank), via bubbles' `DefaultDelegate` (Height 2, Spacing 1).
Compact target: one line per model, family inline, no blank lines. Example
for the current registry:

```
  glm-5.3     131  ollama/glm-5.3-flash:cloud      cloud  65/127/127
  glm-5.3     131  ollama/glm-5.3:cloud            cloud  2/4/4
  deepseek-v4  57  ollama/deepseek-v4-flash:cloud  cloud  6/30/32
  deepseek-v4  57  ollama/deepseek-v4-pro:cloud    cloud  1/21/25
  kimi-k2.7    35  ollama/kimi-k2.7-code:cloud     cloud  6/33/35
  ornith-1.5   14  ollama/ornith-1.5:35b           local  0/5/14
  qwen3.8       5  ollama/qwen3.8:27b-mlx          local  0/0/5
```

Column rules:

- family name left-padded to the widest family name in the current eligible list
- `fam30d` right-aligned, width 3
- ID left-padded to the widest ID in the current list
- location left-padded to width 5 (`local`/`cloud`)
- counts `1d/7d/30d` left-padded to width 11 (max `999/999/999`)
- tags appended as ` [a,b]` only when non-empty
- empty family renders as `-` in the family column
- leading 2-space indent comes from `list.DefaultDelegate`'s default styles —
  not something we add

**How bubbles/list will render this.** Configure the model list's delegate
with `ShowDescription = false` (Height becomes 1) and `SetSpacing(0)` (no
blank lines between rows). The stock `DefaultDelegate` then renders one line
per item — exactly what we want — and gives us free ellipsis truncation on
narrow terminals and free filter-match underlining (because we set
`Title() == FilterValue()`).

**Filter behaviour change.** Today dividers vanish under filtering (their
`FilterValue` is ""), so family context disappears the moment the user
starts typing. New behaviour: `FilterValue` is the full line, so typing
`glm` or `deepseek` narrows to a family — a new capability. Typing an ID
substring works as today. Digit queries can match count columns; harmless.

**Things explicitly NOT to change** (so you don't "improve" them out of
recognition):

- `sortModelsByUsage` and therefore launch rotation. Sort key still uses
  `otherFamily` (the empty-family sentinel string) for stable grouping of
  empty-family models — the const stays; only its *divider label* use is
  removed.
- `phaseModelView` header/footer layout.
- `agent_picker.go`'s `NewAgentPicker` and `internal/ollamaconfig`'s use of
  `ThemedListDelegate` — both keep the two-line default.
- `ThemedListDelegate` itself. We configure fields on the returned copy,
  we don't modify the function.

**Commands** (from `wt/`):

- `go build ./...`
- `go vet ./...`
- `go test ./...`
- `make vet`, `make test`, `make lint` — repo Makefile umbrella
- Manual smoke after all phases: build, then `go run ./cmd/wt` (or whatever
  the existing run command is per `wt/CLAUDE.md`), exercise the model picker.

---

## Phase 1 — New line-builder + new modelItem field (parallel to old)

**Goal:** introduce the new line-building logic and a `line` field on
`modelItem`, without yet removing anything. Old `Title()`/`Description()` /
dividers still work, so nothing visibly changes after this phase. Tests for
the new line format go from red to green during this phase.

**Files touched:**

- `wt/internal/tui/model_list.go` — add new builder + new field
- `wt/internal/tui/model_list_test.go` (or a new `model_line_test.go`) — new
  unit tests for the line-builder

**Inputs to the line-builder** (already available inside
`withFamilyDividers` today — we'll refactor in place, not call a new
function from new code):

- the post-sort `items []*modelItem` slice
- `s usage.Store` for `Counts(family)["30d"]`

**Steps:**

1. Read `wt/internal/tui/model_list.go` lines 1-230 end-to-end. Confirm the
   structure described in the spec's Context section.

2. Add a `line string` field to `modelItem` (it's already a struct with
   private fields — just add the field next to the existing ones).

3. Replace the body of `withFamilyDividers` with a function that:
   - Sorts items (keep the existing `sortModelsByUsage(items)` call — sort
     key still uses `otherFamily`).
   - Computes `famWidth = max(len(it.family) for it in items)` and
     `idWidth = max(len(it.id) for it in items)`.
   - Builds each item's `line` string using `fmt.Sprintf` with the
     formatting in the spec's "Line format rules":
       ```
       "%-*s  %3d  %-*s  %-5s  %-*s"
       ```
       where the args are `(famWidth, fam, fam30d, idWidth, id, location,
       countsWidth, counts)`. `fam30d = s.Counts(it.family)["30d"]` (or 0
       when family is empty — `Counts("")` is undefined; handle by
       treating empty family as `-` with count 0). `counts = fmt.Sprintf(
       "%d/%d/%d", c["1d"], c["7d"], c["30d"])`.
       Append `fmt.Sprintf(" [%s]", strings.Join(it.tags, ","))` only when
       `len(it.tags) > 0`.
   - Returns `items` (still []*modelItem — same shape as today).
   - Renames to `buildModelItems` if you prefer; either name is fine, just
     keep the signature `func buildModelItems(items []*modelItem, s
     usage.Store) []*modelItem`.

4. **TDD: write tests first, then make them pass.** Add a test
   `TestModelItemLineFormat` that:
   - Builds a small in-memory `usage.Store` (stub) with `Counts(family)`
     returning canned values.
   - Calls `buildModelItems` on 2-3 modelItems with different family
     names/IDs/locations/tags.
   - Asserts each item's `.line` string equals the exact expected formatted
     string with right-aligned fam30d, left-padded family and ID, slash
     counts, optional `[tags]` suffix, `-` for empty family.
   - Run `go test ./internal/tui/ -run TestModelItemLineFormat` → red,
     then implement → green.

5. **Keep `Title()` and `Description()` returning the OLD values** during
   this phase so the picker renders identically. The `line` field exists
   but nothing reads it yet. (You'll wire it in Phase 2.)

**Verify gate for end of phase 1:**

- `cd wt && go build ./...` succeeds.
- `go test ./internal/tui/ -run TestModelItemLineFormat -v` passes.
- Full `go test ./...` still passes (nothing visibly changed).
- Manually skim: `go run` the picker (or `make test` if it runs the
  installed binary) — model picker still shows the old 3-line layout.

---

## Phase 2 — Switch the delegate, retire dividers

**Goal:** make the picker render the new one-line rows; remove all
divider-related code; slim `snapSelectionOnModel` to a pure clamp.

**Files touched:**

- `wt/internal/tui/model_list.go` — modelItem methods, delegate config,
  delete divider machinery
- `wt/internal/tui/app.go` — three call sites (wrap-around nav, snap
  capture, initial Select — see below)
- `wt/internal/tui/model_list_test.go`, `model_family_test.go`,
  `agent_model_test.go` — rewrite divider/format assertions to match the
  new line format

**Steps:**

1. In `wt/internal/tui/model_list.go`:

   a. Change `modelItem.Title()` to return `m.line`.

   b. Change `modelItem.Description()` to return `""`. Keep the method —
      `list.DefaultDelegate.Render` casts to `DefaultItem` and calls
      `Description()` even when the description is hidden. Add a one-line
      comment explaining why it's empty.

   c. Change `modelItem.FilterValue()` to return `m.line` (was `m.id`).
      Now `Title() == FilterValue()` and the stock delegate's filter-match
      underlining works free.

   d. Delete these symbols entirely (they have no remaining callers):
      - `dividerItem` struct
      - `familyDividerLabel` const (or var — whichever it is)
      - `modelListDelegate` struct + its `Height`/`Spacing`/`Render`
        methods (the custom Render exists solely to render dividers)
      - The `withFamilyDividers` function — replaced in Phase 1 by
        `buildModelItems` which is already in place

   e. **Important nuance about `otherFamily`.** The spec lists it as
      "deleted", but `sortModelsByUsage` uses `otherFamily` as a stable
      group key for empty-family models. **Keep the const** so sorting
      doesn't regress. Only the *divider label* use goes away (and that
      use was inside `withFamilyDividers`, already removed). If you find
      a cleaner name (e.g. `familyOther`), rename consistently — but
      keep one symbol for the sort key.

   f. Replace the existing model-list setup in `app.go` (the place that
      currently does `delegate := modelListDelegate{...}; list.New(...
      delegate ...)` — find it by searching for `modelListDelegate` in
      `app.go`) with:

       ```go
       delegate := ThemedListDelegate(theme)
       delegate.ShowDescription = false
       delegate.SetSpacing(0)
       list := list.New(items, delegate, width, height)
       list.SetShowHelp(false)
       // ... other list config
       ```

      `ThemedListDelegate` returns `list.DefaultDelegate` *by value*, so
      configuring fields on the local `delegate` variable does not affect
      `agent_picker.go`'s use.

   g. Slim `snapSelectionOnModel(m *models)` to a pure out-of-range
      clamp:

       ```go
       // clampModelSelection guards against bubbles v1.0.0 leaving
       // m.Index() outside [0, len(VisibleItems())) after a filter
       // narrows the list. With dividers gone there is no
       // direction-of-travel walk or divider-skipping — just the clamp.
       func clampModelSelection(m *models) tea.Cmd {
           visible := m.VisibleItems()
           if len(visible) == 0 {
               return nil
           }
           if i := m.Index(); i < 0 || i >= len(visible) {
               m.Select(0)
           }
           return nil
       }
       ```

      Rename or keep the old name; whatever the surrounding code does.

   h. Delete `firstModelIndex` and `lastModelIndex` helpers.

2. In `wt/internal/tui/app.go`, three call sites:

   a. **Wrap-around navigation (around line 276-284).** Today:
       ```go
       if key := msg.String(); key == "up" || key == "k" {
           if m.models.Index() == firstModelIndex(m.models) {
               m.models.Select(lastModelIndex(m.models))
               return nil
           }
       }
       if key := msg.String(); key == "down" || key == "j" {
           if m.models.Index() == lastModelIndex(m.models) {
               m.models.Select(0)
               return nil
           }
       }
       ```
      Replace with direct index math:
       ```go
       if key := msg.String(); key == "up" || key == "k" {
           if m.models.Index() == 0 {
               items := m.models.VisibleItems()
               m.models.Select(len(items) - 1)
               return nil
           }
       }
       if key := msg.String(); key == "down" || key == "j" {
           items := m.models.VisibleItems()
           if m.models.Index() == len(items)-1 {
               m.models.Select(0)
               return nil
           }
       }
       ```
      (Or use whichever bubbles/list API exposes the item count — check
      `list.Model` for `VisibleItems()` vs `Items()` vs a `Len()` method.
      Use whichever the existing code already uses for `firstModelIndex`.)

   b. **The `prev := m.models.Index()` capture before `m.models.Update`**
      (around line 494). Delete the `prev := ...` line. The new
      `clampModelSelection` doesn't need direction.

   c. **Initial cursor positioning** (around line 709): unchanged. The
      `idIndex[id]` value is a list index; with dividers gone it's now
      dense 0..n-1, but the call `m.models.Select(idx)` is identical.

3. **Test rewrite.** The five test files containing ~80 lines of `◈` /
   `divider` / `firstModelIndex` / `lastModelIndex` / `modelListDelegate` /
   `withFamilyDividers` references are:

   - `wt/internal/tui/model_list_test.go`
   - `wt/internal/tui/model_family_test.go`
   - `wt/internal/tui/agent_model_test.go`

   For each file:
   - **Find** any assertion about `modelItem.Description()` returning the
     metadata blob → rewrite to assert `modelItem.Title()` contains the
     new line format (family prefix, slash counts, location, optional
     tags).
   - **Find** any assertion that constructs / counts `dividerItem`
     entries → remove the assertion (no dividers exist any more). If the
     test's *purpose* was "dividers appear between families", rewrite it
     to "adjacent models share a family prefix on their line" (assertion:
     the first chars of two adjacent items' `.line` are equal up to the
     family column width).
   - **Find** any test that references `modelListDelegate`,
     `withFamilyDividers`, `firstModelIndex`, `lastModelIndex`, or
     `snapSelectionOnModel` → rewrite for `buildModelItems`,
     `clampModelSelection`, direct index math.
   - **Add** new tests:
     - `TestModelLineFormat` (in `model_list_test.go`) — exact line
       strings for 3-4 representative modelItems (different family
       widths, with and without tags, with and without empty family).
     - `TestFilterByFamilyName` — set `FilterValue` to a family substring,
       confirm `VisibleItems()` narrows correctly.
     - `TestClampOnFilterNarrow` — exercise the bubbles v1.0.0
       filter-narrowing bug path through `clampModelSelection`: position
       cursor at index 5, narrow the filter so only 2 items remain, call
       the clamp, confirm `m.Index() == 0`.
     - `TestWrapNavigation` — confirm up-at-0 → last, down-at-last → 0.

4. Run `go test ./... -v` from `wt/`. Expect red on the rewritten tests
   until the production code in step 1-2 is complete. Iterate.

**Verify gate for end of phase 2:**

- `go vet ./...` clean.
- `go test ./... -count=1` passes.
- `go build ./...` succeeds.
- Manually run the picker (build + run per `wt/CLAUDE.md`):
  - Each model renders as one line, no blank lines between rows.
  - Family name appears as the leftmost column on every row.
  - Counts render as `N/N/N` with no `1d:`/`7d:`/`30d:` labels.
  - Tags show only when present.
  - Typing a family name narrows the list.
  - Up at first row wraps to last; down at last wraps to first.
  - Selecting a model still launches correctly (rotation unchanged).

---

## Phase 3 — Docs

**Goal:** CHANGELOG entry. (Confirmed during planning: no prose in
`wt/docs/`, `wt/CLAUDE.md`, or root `docs/guides/` / `docs/reference/`
describes the model picker layout, so the spec's "Sweep docs" step has
nothing to update.)

**Files touched:**

- `wt/CHANGELOG.md`

**Steps:**

1. Add a CHANGELOG entry at the top following whatever convention
   `wt/CHANGELOG.md` already uses (read the top of the file). Suggested
   shape, adapted to its style:

   > Compact one-line model picker: family name + family 30d count now
   > lead every row; provider/fam-count/tags-when-empty dropped from the
   > trailing metadata. Family headers removed (now inline). Counts render
   > as `1d/7d/30d`. Typing a family name filters to that family.

2. No other doc edits — verified above.

**Verify gate for end of phase 3:**

- `git diff wt/CHANGELOG.md` shows a sensible new entry.

---

## End of plan

After Phase 3 the plan is complete. Run verification (per the writing-plans
skill: "Verification is the final step of the entire plan, not its own
phase"):

- `cd wt && make vet`
- `cd wt && make test`
- `cd .. && make lint` (repo-root umbrella; exercises `make lint-shell` +
  `check-links` from the root Makefile)
- Manual picker smoke (Phase 2 verify gate covers this).

Then commit and PR per `wt/CLAUDE.md`.
