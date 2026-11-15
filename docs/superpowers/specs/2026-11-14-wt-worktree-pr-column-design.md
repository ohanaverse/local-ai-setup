# wt worktree/branch list: the PR column carries the status

Status: small follow-up to 2026-11-13-wt-worktree-table-design.md. Size: small change.

## Intent and why

In the worktree/branch list the pull-request fact is shown twice: once as a
bare number in the `PR#` column (`#383`) and again as a badge after the
verdict (`#383 merged`, or `PR ?` while the lookup is unknown). The status
word is the useful half, and the badge repeats the column. Put the status in
the column and drop the badge, so the PR reads once, in one place, and the
verdict line keeps only its risk and fact badges (`root`, `dirty`,
`unpushed`, `+remote`, `held …`, `locked`).

## Goals

- The `PR#` column shows the PR and its state, lower-cased as the badge does:
  `#383 merged`, `#3 open`, `#4 closed`.
- The unknown case (the lookup ran or was skipped and gave no usable answer —
  `row.PRUnknown`, `LookupUnknown`) reads `?` in the column, so "cannot be
  determined" stays distinct from "no PR" (a genuinely no-PR row,
  `LookupNone`, stays blank). This is settled: `?`, not blank, not `PR ?`.
- The PR badge is removed from `cleanup.RowTags` (both `#N state` and `PR ?`);
  it is no longer drawn after the verdict.
- The `/` filter still finds a row by its PR number/state, now through the
  column's text instead of the tag it matched before.

## Non-goals / anti-goals

- `draft` is not tracked (`PRInfo` has no draft field; the gh query requests no
  draft): a draft PR shows its `open` state. Settled: skip draft.
- The launch gate's `launchwarn` "PR #N is merged" warning and the JSON
  contract's `pr: {number, state}` (the verdict's *own* PR, not the badge) are
  untouched — out of scope, both read `PRTag`/`row.PR` directly, not the badge.
- The `VERDICT` column, the drop order, and the detail sentence are untouched:
  the sentence already names the PR through its evidence and reasons, so it
  carries the status when the column is dropped on a narrow terminal.

## Approaches considered

**Where the status lives.**

- **A (chosen): the column carries `#N state`, the badge is gone.** The PR fact
  is one glanceable cell; the badge is redundant with it. The verdict line
  loses its most-derivable fact, which is why the PR column is still the first
  one a narrow terminal gives up.
- B: keep the badge and just add the state to it. Rejected by the request
  ("remove PR info from the verdict"); it keeps the fact in two places.

**The unknown case in the column.**

- **`?` (chosen):** one char, in the same slot, keeps "can't determine"
  distinct from "no PR" (blank). The detail sentence still says "PR lookup
  failed."
- blank: simplest but folds "no PR" and "couldn't check" together — a silent
  loss of the glanceable flag.
- `PR ?`: the full phrase, widest; no wider signal is worth the column.

**Keeping the filter.**

- **A (chosen):** `filterText` includes the PR column's text, so `#383` still
  finds its row. The tag it matched is gone, so the column must stand in.
- B: accept the filter losing PR-by-number. Rejected: a silent regression the
  `TestFilterMatchesWorktreeBranchAndPR` pin exists to prevent.

## Design

1. **PR column cell** (`internal/tui/rowtable.go`, `rowCells`): factor the PR
   cell out so the column and the filter render it identically — `#<n>
   <state>` when `PRTag != nil` (state lower-cased), `?` when `PRUnknown`, `-`
   while `Pending`, blank otherwise. It stays a right-aligned number column and
   the first one `rtDropOrder` gives up; the drop-order comment is updated to
   say the column now carries the state.
2. **Badge removed** (`internal/cleanup/present.go`, `RowTags`): delete the
   `PR ?` / `#N state` tag; update the `RowTags` doc comment and the "info tag
   (the PR, +remote)" notes. `RowTags` is drawn only in the list; the confirm
   screen pulls only the `held by session` tag out of it, so nothing else
   changes.
3. **Filter** (`internal/tui/row_list_launch.go`, `filterText`): append the PR
   column's text to what it searches, so `#383` still matches.

## Tests and docs that move

- `internal/cleanup/pr_test.go`: `TestPRUnknownIsNeverProvenWhenAsked` and
  `TestPRTagOnceKnown` assert the tag text via `RowTags`; drop those
  `RowTags` assertions (keep the `PRUnknown`/`PRTag.Number` fact checks). The
  "column shows `?`" / "column shows `#5 merged`" behaviour is pinned in the
  tui package instead (`rowtable_test.go`). `TestNonGitHubOriginIsNotPRUnknown`
  (expects 0 tags on a no-PR row) still passes.
- `internal/cleanup/present_test.go`: `TestRowTagsShowsPRQuestionMarkWhileUnknown`
  and the `pr merged`/`pr open`/`pr closed`/`pr unknown` cases of
  `TestRowTagsEveryFact` are removed (the badge is gone).
- `internal/tui/cleanup_list_test.go`: `TestFilterMatchesWorktreeBranchAndPR`
  and `TestRowListTagsWarnStyled` keep `#517` matching (now via the column) and
  the info-tag style note is restated.
- `internal/tui/rowtable_test.go`: pin the PR column's four cells (`#N state`,
  `?`, `-` while pending, blank on a no-PR row) and that a no-PR row keeps a
  blank column.
- Docs: `wt/docs/wt-cleanup.md` (the `PR ?` tag lines at 49, 93-94 become a
  `?` in the `PR#` column), `wt/docs/internals/tui.md` (the row's badges no
  longer include the PR), and this spec's parent
  (`2026-11-13-wt-worktree-table-design.md` §1 `PR#` line) gets the status
  noted. Verify no other `RowTags` consumer (e.g. `picker_bare_test.go`)
  depended on the PR tag.

## Left to the builder

- The exact helper shape (a `prCell(row)` shared by `rowCells` and
  `filterText`, versus a `prCellText` string helper) and where it lives.
- Whether the `?` unknown cell is right-aligned like the number cells (it is a
  single char, so it sits on the column's right edge either way).

## Decided in the conversation

- The PR column carries the state (`#383 merged`); the PR badge is removed.
- Draft is not shown.
- The unknown case reads `?` in the column, keeping it distinct from "no PR".
- The filter keeps matching the PR by number, through the column.
