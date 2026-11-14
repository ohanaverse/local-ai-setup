# Row detail screen and a flatter worktree list (design)

Date: 2026-11-14. Scope: `wt` TUI — the launch selector (`wt`) and the manage list (`wt worktrees` / `wt cleanup`). Code: `wt/internal/tui/cleanup_list.go`, `cleanup_confirm.go`, `row_keys.go`, `rowtable.go`, `wt/internal/cleanup/present.go`.

## Intent

The list is hard to scan (section headings, long verdicts, tags that say nothing once a PR is merged) and its row actions are inconsistent: `x`/`d` confirm in the TUI, `r` quits to a separate program, and the confirm text shows the worktree name twice and says little about what is at stake. The user wants to look at one row in full, then decide, on one screen.

## Goals

1. One flat list, no section headings.
2. A short VERDICT column that says something only when it matters.
3. One shared **detail screen** opened by `i`, `x`, `d`, `r` (and `R`, `o`). It shows everything about the row and offers every action that applies, with a plain sentence for each.
4. `x`/`d` confirm on that screen (typed phrase where required). The detail block is identical for `i` and for the confirmation.

## Non-goals / anti-goals

- No change to what removal, rescue or restore-root *do*, or to their safety rules (`internal/removal`, `internal/rescue`, `internal/rootmove`, hard stops, approvals).
- No per-row sync with a remote (fetch, pull, push): the list's `F` stays the only refresh. Deferred by the user.
- Not running the rescue inside the TUI (see Approaches): `r` keeps its hand-off.
- No loss of information: anything dropped from the list line must be reachable on the detail screen.
- The manage list's tick (`space`/`a`) and `enter` batch removal stay as they are; its batch confirm reuses the shared detail renderer per entry.

## Decisions (by the user)

**List**
- Order: the root first; then local worktrees and branches together, alphabetically; then remote branches, alphabetically. No headings (`KindGroups`/`Heading*` go away as a display concept).
- The first column is the session digit (blank or 1-9). It already exists (`rowPrefix`); keep it.

**Verdict and tags** (user's questions, with my proposed answers marked *proposal*)
- "in use" is covered by the session digit; "current", "default", "root", "+remote" tags are dropped.
- *Proposal:* verdict words `safe` / `check` / `work` replace `proven` / `less sure` / `unique work`. A row with a hard stop and no other verdict shows `locked`.
- Why `wt-pr-column` read "less sure": its one ignored file triggers the soft reason `ignored_files`, which floors a proven row at less sure because ignored files are lost with the directory. This stays (it protects `.env`-style files), but the detail screen now lists those files.
- *Proposal:* `unpushed` and `upstream gone` tags show only when the row's status is not `safe`. On a row proven by a merged PR they carry no information (a squash merge leaves the original commits on no remote ref). They are always listed on the detail screen.

**Detail screen** shows, in this order:
- worktree name, branch, full absolute path (no repeated name; the old `shortPath` echo goes);
- status/verdict sentence, PR, age, session count and who holds it;
- local-only commits (hash + subject), if any;
- uncommitted files that are not git-ignored (status letter + path);
- git-ignored files (path; count and size);
- "will lose" lines and warnings the removal preview already produces;
- the actions below.

**Actions** are listed only when they apply to the row; an action that cannot apply is not shown at all, on the detail screen or in the list footer. The reason a row cannot be removed (root, locked, current) is a fact in the Verdict line, not an action row. Each has one plain sentence:
- `x` remove — deletes the worktree directory; keeps the branch unless it is proven merged; lists what is lost; unproven rows need the typed word `delete`.
- `d` remove + branch — as `x`, and also deletes the unmerged branch (box pre-ticked, `tab` toggles). On a branch-only row it equals `x`.
- `r` rescue — copies uncommitted and unpushed work into a new `rescued/<name>` branch and worktree; the original is untouched; leaves the TUI to run `wt rescue`, then returns to the list. Offered only when `rescue.Offered`.
- `R` restore root — root only, when it is off the default branch (`rootmove.Offered`): moves its branch and work into a new worktree and puts the root back on the default branch.
- `o` delete origin — local row with an origin ref; typed `delete origin`.
- `esc` back to the list.

On the root: no `x`, `d` or `r`; the Verdict line says "the root cannot be removed"; `R` is offered when applicable.

## Approaches considered

- **A. New `detail` phase inside `cleanupListModel` (chosen by the user).** `i`/`x`/`d`/`r`/`R`/`o` enter it; `x`/`d`/`o` then run the existing preview and confirm machinery on the same screen; `r`/`R` keep the quit-and-hand-off flow. Smallest change; reuses tested code.
- B. As A, but run the rescue inside the TUI. Uniform flow, but embeds the rescue engine's progress and refusals in a Bubble Tea screen; much larger. Possible later.
- C. Drop ticking and send every removal through the detail screen. Simplest model, but removes batch cleanup. Rejected.

## Constraints

- Both lists must behave the same on the detail screen (the launch selector answers `x d o r R` through the same `RemoveFuncs`; `/` filter owns printable keys while open).
- While a fill is in flight (`m.filling()`) removal actions stay unavailable; the detail screen may open (`i`) but shows what is loaded and says the rest is still loading.
- The detail data for local-only commits, uncommitted and ignored file lists is new: it must be read off the update goroutine, with git run through `cleanup.Git` (`GIT_OPTIONAL_LOCKS=0`), never writing refs or the index. Sizes are bounded (cap lists, say "and N more").
- The list contract (`docs/contracts/cleanup-list.*`) and `--json`/`--plain` output do not change unless a new field is strictly needed; verdict words in the TUI are a display mapping only. Frozen wire strings (`proven`, `less_sure`, ...) stay.
- Must fit 80x24; the detail screen scrolls. Follow `tuilayout` rules (`TestEveryListPhaseFitsTheTerminal`).
- Footer key hints (`row_keys.go`, `TestRowKeysMatchTheHandlers`) must list `i` and match handlers.
- Docs change with behavior: `wt/CLAUDE.md` TUI keys paragraph, `docs/wt-cleanup.md`, guide 08 §9, root `CLAUDE.md` `wt worktrees` entry (headings, columns, verdict words).

## Calls made while writing this up (mine, not the user's; marked for review)

- **Keys open, then act.** `i`, `x`, `d`, `r`, `R`, `o` pressed on the list all open the detail screen unarmed; nothing is confirmed or handed off until an action key is pressed on the screen. (The user asked that these keys "move to a shared screen" which then "offers" the options.) `x`/`d`/`o` on the screen start the existing preview + confirm as a sub-state of the detail phase; `esc` leaves the confirm first, then the screen, then (on the list) quits as today. `r`/`R` on the screen hand off at once; the screen's sentence is the explanation, and `r`'s hand-off keeps its own prompts.
- **`i` runs the removal preview.** The preview is read-only (`Engine.Preview` changes nothing), so "will lose" and warnings read the same for `i` and `x`. Refusal text (the Verdict-line reason, and the status line if a key is pressed anyway) comes from `cleanupLocked`/the preview's refusal, `rescue.Offered` and `rootmove.Offered`.
- **Batch confirm** (manage list, `enter` on ticked rows) keeps its compact per-entry layout, using the shared renderer only for the header lines (kind, name, path once, will-lose lines); it does not load the commit and file lists for every entry.
- **Remote rows** show remote/branch, tip, PR, verdict and the origin-removal sentence; no path, commit or file sections.
- **Stale screen.** After an `r`/`R` return or a refresh the list is re-read; if the detail row no longer exists the screen closes with a status line, otherwise it reloads.
- **Verdict words and tag hiding** are the proposals above; the user said "go" to them but has not seen the final words on screen. Change them in one table (`verdictText`) if they dislike them.

## Left to the builder

- Layout of the detail screen, colours, scroll keys, caps on list lengths.
- Where the file/commit listing code lives (likely `internal/cleanup`, read-only) and how it is stubbed in tests.
- Exact wording of the one-line action descriptions, within the sentences above.
- Whether `locked` and the row's remaining tags (`dirty`, `held by session`, root-drift badge) stay on the list line; keep root drift visible on the root row.
- Test plan; existing tests that assert headings, `verdictText` or tag text will need updating.

## As built (deviations from the text above)

Recorded after implementation, so the spec does not describe a screen the code
does not draw. Each is a rendering detail; the decisions the spec fixes are as
written.

- **The single-row confirmation is the block plus the decisions.** One row's
  confirmation renders `detailBlock` (identical to `i`) and then the entry's
  decision lines — the `[ ] also delete unmerged branch <name> (tab)` toggle,
  `approvals: ...`, an origin ref's restore hint, or `not removed: <why>`. The
  block already carries the losses, the holder and the notes; the toggle and
  the approvals exist only in the per-entry layout, and dropping them would
  hide the one choice the confirmation offers.
- **A refused key answers on the detail screen.** A key the screen does not
  list still reaches its handler, which writes the list's own refusal on the
  status line; the detail screen draws that line under its title, so a key the
  footer does not offer reads as refused rather than ignored.
- **The footer wraps onto two balanced lines.** Adding `i` pushed the 40-column
  footer past one line; `fitHints` now splits it into two lines of even width
  instead of leaving the last key alone on the second line.
- **The verdict cell reads `-` while a row is pending**, like the other
  verdict-shaped cells, rather than guessing a word for a row nothing has
  judged yet.
- **A branch or remote row shows the commit it points at.** The sketch's
  label list is a worktree's; a row with no path is named by its ref and its
  tip (`tip       d4e5f6a7`, the eight-character abbreviation the listings
  use), which is the whole of what it identifies. A worktree's HEAD stays
  implicit: its commits are the first listing.
- **A listing entry wraps under its own indent.** A deep path or a long
  commit subject is two lines at 40 columns rather than a line past the
  terminal's edge; every line of the block fits the width (the layout sweep
  covers the screen at every size now).
