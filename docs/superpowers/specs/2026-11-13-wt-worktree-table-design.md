# wt worktree/branch screens: table layout, async fill, content-in-base signal (full design)

Status: draft for approval, 2026-11-13. Size: project.

## Intent and why

Choosing where to work means comparing the worktrees and branches at a glance:
how much uncommitted work is sitting in each one, whether that work is already
in main, and how many wt sessions are already running in it. The worktree/branch
screens — the launch picker (`wt`) and `wt worktrees` — draw one wrapping prose
row per entry (`verdict · tags · sentence  age`), which cannot be scanned like a
table, and they wait for the whole repository gather (per-worktree gates, the
proof ladder per tip, unpushed counts, ref ages) before anything is painted.
The model selector (`internal/tui/modeltable.go`) already shows the shape wanted:
a fixed-column table, one line per entry, columns given up one at a time on a
narrow terminal, a live-session count as a digit prefix, and a command footer at
the bottom edge.

This design brings that table to both worktree/branch screens; makes the list
paint from the cheap git calls immediately and fill every detail in
asynchronously (the `git-wt` behaviour); adds uncommitted change counts, ignored
counts and a session count per row; and adds a content-containment signal so
squash-merged work reads as merged even though no commit hash matches ("all the
changes are in main but the commits don't match").

The footer's content rules are 2026-11-13-wt-picker-footer-design.md's and stay
exactly as built (row-aware keys, dropped last on a short terminal). Only where
the footer sits changes: anchored to the bottom edge.

## Goals

- One table, one line per entry, on both screens. Row prefix (left to right):
  session digit, cursor marker, and — `wt worktrees` only — the checkbox. Then
  the columns `WORKTREE BRANCH PR# DIFF IGN STATUS VERDICT`.
- The same rendering code on both screens; only the checkbox in the prefix
  differs between launch and manage mode.
- First paint from the cheap git calls alone (`git worktree list --porcelain`,
  `for-each-ref`, base resolution, the session-claims directory). Every
  per-worktree and per-tip fact fills in after the list is on screen.
- `DIFF` counts uncommitted changes diffstat-style: `+99/-55`, added/deleted
  lines, staged + unstaged net vs HEAD, untracked non-ignored files counted as
  their lines. Git-ignored work never counts anywhere (it is `IGN`'s subject
  only).
- `IGN` counts ignored entries (a wholly ignored directory counts once — the
  count `Losses.IgnoredEntries` already gathers).
- A live-session count per worktree row: the model picker's issue-#73 digit
  prefix over `wt-sessions` claims. The browsing session never counts itself; a
  claim exists exactly while an agent or command session runs in the chosen
  directory (the launch's `HoldLaunch`/release pair), so only running sessions
  are counted.
- `STATUS` is one of `clean`, `dirty`, `in main`, `conflicted`, `-`.
  `VERDICT` keeps today's words: `proven`, `less sure`, `unique work`, `in use`.
- A squash merge reads as merged: when every change a row introduced is present
  in main's content, the row is `proven` (new evidence `changes-in-base`) and a
  worktree whose uncommitted work is all in main reads `STATUS in main` —
  regardless of commit hashes.
- The footer sits at the bottom edge of the screen, with blank filler between
  the last row and the detail block when rows are few.
- The cursor wraps top to bottom and back in the row list, the model selector
  and the agent/command picker.

## Non-goals

- `wt worktrees --json` / `--plain` shapes and `docs/contracts/cleanup-list.*`:
  unchanged. Every new `Row` fact stays unserialised
  (`TestCleanupJSONUnchangedByNewRowFields` keeps passing). (My call: the new
  numbers stay TUI-only until someone asks for them in JSON.)
- What any key does: row keys, refusal status lines, `RemoveFuncs`, the removal
  engine's re-checks and typed confirmations, the rescue/restore-root hand-offs.
- The row-aware footer's key set and short forms (that design's contract).
- The model selector's columns, sorting and route logic.

## Anti-goals

- A false `proven`. Every new signal fails closed: a git failure, a change-set
  over the caps, or an unreadable file reads as unknown — never as contained.
- A slower first paint than today, or rows reordering under the cursor when the
  details land.
- Any frame that lets the view exceed the terminal height
  (`TestEveryListPhaseFitsTheTerminal`).

## Constraints

- House style: a what/why comment on every `Test*`; the classifier's rulings
  documented beside the code they govern (`internal/cleanup`'s Ruling comments,
  e.g. Ruling 17) and extended, not weakened.
- `internal/tui/cleanup_list.go` is one component with two modes and several
  phases beyond the list (confirm, running, results). Everything documented in
  its header comment survives except where this design says otherwise.
- New gather/proof behaviour gets real-git fixtures (`internal/gittest`,
  `internal/cleanup`'s fixture tests) and truth-table extensions to
  `classify_test.go`.
- Docs updated in the same change: `wt/docs/internals/tui.md`,
  `wt/docs/wt-cleanup.md`, `docs/guides/08-maintenance-and-troubleshooting.md`
  §9, and root `CLAUDE.md`'s `wt worktrees` blurb where it describes the screen.

## Approaches considered

**Table rendering.**

- **A (chosen): one table renderer inside the existing row list.** A `rowTable`
  on `tuilayout.Columns` — the model table's machinery: fixed columns,
  rune-measured widths, a drop order, `Header()`/`Line()` — replaces only how a
  row becomes lines. Filter, ticks, row keys, the confirm/running/results
  phases, launch mode's `+ New worktree…` item and the keyed cursor
  preservation across rebuilds (`withReport`) are untouched.
- B: rebuild both screens on a bubbles `list.Model` like the model selector.
  Same pixels, but the removal phases and the tick bookkeeping (ticks keyed by
  index, preserved across re-reads) would re-host onto bubbles' list model and
  its own fuzzy filter for no visible gain. Rejected: blast radius around the
  removal flows.
- C: keep the wrapping prose rows and align with tab stops. Rejected by "one
  line per entry".

**Async fill.**

- **A (chosen): a two-stage gather, swapped in through the existing `withReport`
  mechanism.** Stage 1 gathers only the facts the always-shown cells need and
  paints at once; stage 2 runs the gates, diffstat, proofs and PR lookups and
  repaints through the generation-numbered swap the PR fill already uses
  (`rowsLoadedMsg`/`rowPRsLoadedMsg`, `rowsGen`, `withReport`).
- B: per-row background updates with no generation guard. Rejected: a stale
  fill can overwrite a newer screen.
- C: keep the gather synchronous. Rejected by the request (`git-wt` paints in
  milliseconds; wt waits for the whole repository).

**The "this work is in main" signal.**

- **A (chosen): containment — the row's change-set must reverse-apply cleanly
  onto main's tree.** The change-set is the row's own delta: for a branch or a
  worktree's tip, `git diff --binary <merge-base> <tip>`; for a worktree's
  uncommitted work, `git diff --binary HEAD` (staged + unstaged net) plus its
  untracked non-ignored files. Contained means: the tracked part reverse-applies
  cleanly against base's tree through a throwaway index (`GIT_INDEX_FILE`,
  `git read-tree base^{tree}`, `git apply -R --check --cached`, with
  `--unidiff-zero` for zero-context hunks), and every untracked file's blob
  equals `base:<path>`'s. Ignored files never enter a change-set. Verified on a
  reproduced squash failure: it proves the case wt misses today (after `main`
  edits a file the branch touched, `merge-tree` conflicts and the row drops to
  `unique work`), and it fails when main later *reverted* the change — the exact
  reason `cherry`/`patch-id` stay weak signals ("a later revert on main defeats
  them").
- B: a plain content diff of the row against main over non-ignored files (empty
  = in main). Rejected: false alarms whenever main holds unrelated commits
  after the squash — the row's work is fully in main but the contents differ.
  (The user's instinct — compare content, not hashes, and never let ignored
  files count — is kept; the comparison becomes containment instead of
  equality.)
- C: promote `cherry-equivalent`/`patch-id` to proofs. Rejected: both are
  defeated by a later revert on main; promoting them would permit a false
  `proven`, which the ladder's own comments forbid.

## Design

### 1. The table (`internal/tui`, new `rowtable.go`)

Row prefix, left to right: the session digit (a clamped `9`, blank at 0 — the
model list's `refColumn`/`markerMarked`/`markerBlank`, plain ASCII for the same
width-stability reason), the cursor (`"> "` / `"  "`), and in manage mode the
checkbox (`"[x]"` / `"[ ]"` / `"[-]"`) with its space. Prefix width 4 in launch
mode, 8 in manage mode (the `rowPrefixWidth` the columns are built with).

Columns and their cells:

- `WORKTREE` — `cleanup.DisplayName(row)`. Blank on branch and remote rows.
  A detached worktree row shows its directory name (its `Name`).
- `BRANCH` — the branch name. A remote row shows `remote/name` (e.g.
  `origin/feat/x`), which keeps it distinguishable from the same-named local
  branch without a kind column. Blank on a detached worktree row.
- `PR#` — `#383` from `Row.PRTag`. Blank when there is no PR.
- `DIFF` — `+99/-55`, added/deleted lines of the uncommitted change-set (below).
  `+0/-0` for a clean worktree, `-` when it could not be read or is over the
  caps. Blank on rows with no worktree.
- `IGN` — the ignored-entry count. Blank on rows with no worktree.
- `STATUS` — `clean`, `dirty`, `in main`, `conflicted`, `-` (below).
- `VERDICT` — `verdictText` as today: `proven`, `less sure`, `unique work`,
  `in use`.

Layout rules are the model table's: columns separated by two spaces, padded to
the widest cell (headings included) measured in runes; numbers right-aligned in
their column (the TIER-score rule). Drop order when the list is narrow: `PR#`,
then `IGN`, then `STATUS`, then `DIFF`; `WORKTREE`, `BRANCH` and `VERDICT` never
drop. When `DIFF` is dropped the detail block names the highlighted row's
`+N/-N · N ignored` — the `modeLine` pattern: shown whole or not at all.

While a cell is unknown (fill still running) it shows `-`; a cell that does not
apply to the row (no worktree, no branch, no PR) is blank. A bare-repository row
shows its name in `WORKTREE`, `-` in `STATUS`, and keeps today's detail text
(cannot be launched in or removed).

The kind group headings (`WORKTREES`, `LOCAL BRANCHES`, `REMOTE BRANCHES`) stay
heading lines, not entries. `+ New worktree…` stays the list's first item, its
own line. The evidence sentence and age leave the row: the detail block under
the list keeps showing them for the highlighted row, exactly as `detail()` does
today, and gains the dropped `DIFF`/`IGN` numbers as above.

### 2. The gather's new facts (`internal/cleanup`)

- Per worktree, alongside `dirty`/`ignored`: a diffstat — `git diff --numstat
  HEAD` (staged + unstaged net vs HEAD) summed to added/deleted lines, plus each
  non-ignored untracked file's line count as additions. Git's own diffstat rule
  for binaries: a binary or unreadable file contributes no lines. Caps are
  `DirtMaxFiles`/`DirtMaxBytes` (equal to the rescue caps, as today's dirt check
  is); over the caps the counts are unknown. Ignored files are never read.
- Sessions per worktree row: `len(sessionclaim.Holders(commonDir, claim)[path])`
  from the claims the gather already reads for the `in use` verdict. `Holders`
  already excludes this process and its ancestors (a nested `wt` must not count
  the session that holds its checkout), a dead pid's leftover holds nothing, and
  a claim is written at launch and released when the agent or command session
  ends — so the count is running sessions by construction, and the session doing
  the selecting is never among them. Branch and remote rows show no digit: no
  directory, no session.
- New `Row` fields, unserialised: the diffstat (`{Added, Removed int, Unknown
  bool}`), `Sessions int`, and `State` (the STATUS word, produced by the
  classifier below). `IGN` reads `Losses.IgnoredEntries`, which is already
  gathered.

### 3. STATUS words (classifier)

Precedence, for a worktree row:

1. `conflicted` — an operation is in progress (the gather's operation markers)
   or the index has unmerged entries.
2. `clean` — no tracked changes and no non-ignored untracked files (today's
   `Dirty` gate false). Ignored files alone leave a row `clean`; they have their
   own column.
3. `in main` — `dirty` would apply, but the whole uncommitted change-set is
   contained in base (§4). `in main` describes the uncommitted work only;
   whether the tip's commits are in base is `VERDICT`'s business.
4. `dirty` — otherwise.

`-` on a branch or remote row, on a bare row, and whenever the state could not
be read.

### 4. The containment check (`internal/cleanup`: `ladder.go` plus a new small file)

- New evidence `changes-in-base`, the ladder's rung 6 (tried only when rung 5
  `merge-adds-nothing` declines): the tip's change-set vs the merge-base is
  contained in base. Verdict `Merged`. A detached worktree's HEAD goes through
  the same rung (the ladder already runs detached heads).
- The same check answers the uncommitted question for `STATUS in main` and for
  the worktree rule below: the worktree's change-set vs HEAD (staged + unstaged
  net, plus untracked non-ignored files) contained in base.
- Fail-closed: any git failure, a `git apply` refusal that is not a plain "does
  not apply", a change-set over the caps, or an untracked file that cannot be
  read → unknown, never contained. `--unidiff-zero` only relaxes git's
  "at least one context line" requirement for tiny-file hunks; the hunk's own
  preimage must still match, so a coincidental match proves only that the same
  text is in main — which is exactly the claim being made.
- Verdict consequences, same class as `merge-adds-nothing`: `unpushedOf`
  returns 0 for it (already true for any Merged that is not a `treeProof`),
  `netZeroUnpushed` does not apply (it is `treeProof`-only), and `weak_signal`
  stays for rows nothing proves. The PR-side rules are untouched: an open PR
  still vetoes, a moved PR head still raises `unique work`, and a pending PR
  lookup still downgrades a git proof to `less sure` (the existing fail-closed
  rule). Every existing soft demotion applies unchanged to a row the new rung
  proves, exactly as it does for `merge-adds-nothing`: a local base
  (`base_is_local`), the min-age rules, a held label, an in-use claim and the
  rest still lower `proven` to `less sure`/`in use`. `changes-in-base` changes
  only what the ladder answers, never what the ladder's answer is then weighed
  against.
- Worktree rows: when the branch is proven (by any rung) and the uncommitted
  change-set is contained, the dirty gate no longer raises `unique work`: the
  row reads `proven`, its detail says the uncommitted changes are in main. Any
  part not contained keeps today's verdict untouched.

Behaviour this changes beyond the TUI, deliberately: `wt worktrees --json`
verdict *values* for squash-merged rows become `proven` (the shape is
unchanged), and `wt remove --yes` then removes such rows without the typed
phrase — approved in the conversation as the meaning of "treat it as a proof".

### 5. Two-stage load (the picker and `wt worktrees`)

- Stage 1 ("facts"): `git worktree list --porcelain`, `for-each-ref` (heads and
  remotes), base resolution, and the session-claims directory. It paints
  immediately: `WORKTREE`, `BRANCH`, the session digits, the headings and
  `+ New worktree…`; the other cells show `-`/blank as above.
- Stage 2 ("fill"): the gates per worktree (dirty, ignored, diffstat,
  conflicts), the proof ladder/unpushed/ref-ages per tip, then the PR lookups as
  today. At most two repaints (gates + proofs, then PRs), each keyed to
  `rowsGen` and swapped in with `withReport`, so a fill for a superseded load is
  dropped.
- Row order is stable across stages (kind groups, then display name, as today)
  and `withReport` keeps the highlighted row by key.
- During the fill, choosing and launching work immediately (they never needed
  the verdict). Removal actions (`x d o r R`, and manage mode's enter-remove)
  stay unavailable until the fill lands — today's nil-`RemoveFuncs` behaviour,
  reused — so nothing is ever removed from a half-read repository. `F` replaces
  an in-flight fill (the existing `rowsRefreshing`/`reloadGen` rule).
- `wt worktrees --json`/`--plain` build one complete report, synchronously, as
  today.

### 6. Footer anchoring and cursor wrap

- The screen is laid out like the model screen's `listFrame`: a top block (title,
  table header line, filter/status lines), the list, and a bottom block (detail
  + key footer) pinned to the bottom edge, with blank filler between the last
  row and the bottom block when rows are few. The short-terminal give-up ladder
  from the row-aware-footer design stays: header, then title, then detail, then
  the footer last.
- Cursor wrap: moving down past the last item lands on the first, and up past
  the first on the last, over exactly the items the cursor can sit on (headings
  and filter-hidden rows skipped; `+ New worktree…` included). The same in the
  model selector — both `app.go`'s model phase and the standalone
  `pick_model.go` — and in the agent/command picker (my call: the same flow's
  third list should not behave differently).

## What's left to the builder

- Exact heading text and cell formatting: `DIFF`/`IGN` headings, `+N/-N`
  padding, right-alignment details.
- Whether facts/fill is two functions or one with a mode; the file split inside
  `internal/cleanup` and `internal/tui`.
- The containment check's mechanics: temp-index path and cleanup, the untracked
  line-count read, binary detection (git's NUL rule).
- Which existing tests pin the wrapping row layout and the synchronous first
  paint, and how to restate them.

## Settled by me during the builder check (my calls, flagged for review)

- Unknown cells show `-`; inapplicable cells are blank.
- `in main` covers the uncommitted change-set only; tip containment is
  `VERDICT`'s (a clean worktree on a squash-merged branch reads
  `STATUS clean` + `VERDICT proven`).
- Detached worktree rows follow the same rules (their change-set is HEAD's,
  since the merge-base); bare rows keep today's detail and show `-`.
- The session digit appears on `wt worktrees` too (same layout means same
  columns).
- The JSON/`--plain` contract and its fixtures are untouched; new facts stay
  unserialised.

## Decided in the conversation

- Approach A for the table (a renderer inside the existing row list), the
  `VERDICT` column kept on both screens, containment promoted to a proof.
- `DIFF` is diffstat-style over uncommitted changes, untracked non-ignored files
  counted as their lines, ignored work never counted.
- The same layout for every worktree/branch screen including `wt worktrees`.
- The footer anchored to the bottom edge; the cursor wraps in the row list and
  the model selector.
- The first paint is fast and the details fill in asynchronously, like `git-wt`.
