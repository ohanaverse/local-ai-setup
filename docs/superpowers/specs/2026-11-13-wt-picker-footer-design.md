# wt worktree picker and list: row-aware key footer (short design)

Status: draft for approval, 2026-11-13. Size: small change.

## Intent and why

The worktree/branch screens (the launch picker and `wt worktrees`) show their
keys in a title line at the top, and that line lists only some of them
(`enter`, `n`, `/`, `q`; the manage list adds `space a x r R F`). The row
actions `x d o r R` are named nowhere in launch mode, and the title is the
first thing but the report header that a short terminal drops. The model
screen puts its keys in a footer that is dropped last. The user wants the
same on the worktree screens: the available commands in the footer.

## Goals

- A footer on both screens listing the keys that work on the highlighted row,
  and only those (row-aware, the user's call).
- The footer is the last thing a short terminal gives up, as on the model screen.
- Fits 80 columns: at most two lines, wording shortened in steps like
  `manageTitles` today, never dropping a key that applies.

## Non-goals / anti-goals

- No change to what any key does, to its refusal status lines, or to
  `RemoveFuncs`.
- No dimmed or greyed "unavailable" keys; an unavailable key is absent.
- A footer that lists a key its handler then refuses (or omits one that works)
  is a failure even if it renders well.

## Approaches considered

- **A (chosen):** a `rowKeys(item)` function builds the hints from the same
  predicates the handlers call (`cleanupLocked`, `originTarget`,
  `rescue.Offered`, `rootmove.Offered`, bare-row and new-worktree-item checks).
  Handlers are untouched. A parity test keeps the two from drifting. Smallest
  change.
- B: refactor the handlers to share one `availability(row, key)` that returns
  ok plus reason. No drift possible, but it rewrites five pinned handlers.
- C: show all keys, dim the unavailable ones. Rejected: stable layout, but it
  does not fit 80 columns and is less row-aware than asked.

## Design

- `rowKeys(it cleanupItem) []keyHint` in `internal/tui` (a new small file, or
  next to `titleLine`). Always-on keys: `↑/↓`, `q`; launch mode adds `enter`
  (not on a bare row), `n`, `/`, `F`, and `esc` while the input has kept a
  query (esc clears it; with none it falls through to the quit binding);
  manage mode adds `space`/`a` (not on a
  locked row), `enter remove ticked`, `F`. Row keys: `x` (not locked, not the
  new-worktree item), `d` (as `x`; on a worktree row named for the branch it
  also deletes, elsewhere `d remove`), `o`
  (an unlocked origin target exists), `r` (`rescue.Offered`), `R`
  (`rootmove.Offered`). The `+ New worktree…` item and an empty or filtered-out
  cursor get only the always-on keys that apply.
- `layout()` in `cleanup_list.go` gains the footer after the detail block. The
  frames are tried fullest first: title+header, footer, detail; then the header
  goes, then the title, then the detail, and the footer last.
- The footer wraps to at most two lines; each key has a long and a short form,
  chosen by width (the `manageTitles` pattern), so a key is shortened before
  it is dropped.
- `titleLine` keeps only the name (`wt` / `wt worktrees`); the key legends move
  to the footer. `manageTitles` and the "no form names `d o esc`" comment are
  replaced.
- Docs: `wt/docs/wt-cleanup.md` key table and guide 08 §9 where they quote the
  title line; grep the old legend strings before finishing.

## Constraints

- Every `Test*` has a what/why comment. Parity test: for each row kind in the
  existing fixtures, a key is in `rowKeys` exactly when its handler does not
  refuse; asserted on unexported functions, not rendered ANSI.
- `TestEveryListPhaseFitsTheTerminal` must still pass: the view never exceeds
  the terminal height.

## Amendments

- 2026-11-13 (code review of the built footer): the launch picker lists
  `esc clear filter` whenever it has kept a query and the input is closed,
  because `filterKey` takes esc then and only then — with no query, esc falls
  through to the quit binding. The long form of `d` is row-conditional, the
  same rule as `startRowAction`'s seeding: `d +branch` on a worktree row,
  `d remove` on a branch or origin row, where the branch is the row itself.

## Left to the builder

- Exact key labels and the short forms; the split between a new file and
  `cleanup_list.go`; which existing tests quote the old title legend and need
  updating.
