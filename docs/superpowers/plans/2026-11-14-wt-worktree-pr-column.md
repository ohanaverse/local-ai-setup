# wt worktree list PR Column Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The worktree/branch list's `PR#` column shows the PR and its state (`#383 merged`, `?` while unknown), and the PR badge leaves the row's verdict line.

**Architecture:** Three tight tasks in the Go `wt` module: (1) a `prCell` helper renders the PR fact, used by the table's PR column; (2) the `/` filter reads that same cell so `#383` still finds its row; (3) `cleanup.RowTags` drops its PR badge, its tests are retargeted to `prCell`, and the docs that described the badge are restated. The list's badges after the verdict remain the risk/fact set (`root`, `bare`, `+remote`, `dirty`, `unpushed`, `upstream gone`, `held by session …`, `locked`).

**Tech Stack:** Go 1.26, bubbletea/lipgloss TUI, standard library testing (`cd wt && go test ./...`).

**Spec:** `docs/superpowers/specs/2026-11-14-wt-worktree-pr-column-design.md` (approved; read it first — this plan argues from it).

## Global Constraints

- Cell values, exactly: `#<n> <state lower-cased>` for a known PR (`#555 merged`, `#3 open`, `#4 closed`), `?` when `row.PRUnknown`, `-` on a `Pending` row, `""` when there is no PR. Unknown means `LookupUnknown` (the lookup ran/skipped and gave no usable answer); no PR means `LookupNone` — never conflate the two.
- `PRInfo.State` is upper-case `OPEN`/`MERGED`/`CLOSED` (gh validates it); the cell lower-cases it. A hand-built fixture `PRInfo` must set `State` — an empty state would render `#383 ` with a trailing space. Do not add a guard.
- No draft (spec non-goal): `PRInfo` has no draft field; leave the gh query alone.
- No new `Row` fields, nothing newly serialised: `docs/contracts/cleanup-list.*`, `--json` and `--plain` untouched.
- Untouched (spec non-goals): `internal/launchwarn`'s `PR #N is merged` launch warning, and the JSON's `pr: {number, state}` (the verdict's own PR).
- House style: a what/why comment on every test; docs updated in the same change as the behavior; conventional commits (`feat(wt): …`).
- Verification commands: focused `cd wt && go test ./internal/tui -run <Pattern> -v`; package `cd wt && go test ./internal/tui ./internal/cleanup`; full `cd wt && go test ./...`; quality `cd wt && make check` (lint + format, does not run tests); repo root `make check-links` after doc edits.

## Review Focus

1. **Pending beats `?`.** A facts-only (stage-1) row runs no gh at all, yet `PRUnknown` can be set there; its PR cell must read `-`, not `?`, so nothing verdict-shaped shows early. Test: the `"pending wins"` case in `TestRowCellsPerKind` (Task 1).
2. **The filter must not stop finding a PR by number when the badge goes.** Users type `#383` into `/`; that matched through tags before this change. Tests: `TestFilterTextMatchesThePRColumn` (Task 2) plus the existing `TestFilterMatchesWorktreeBranchAndPR`, re-run in Task 3's full-package step.
3. **No PR stays blank, never `?`.** A `LookupNone` row (gh answered completely: no own-repo PR) and a non-GitHub-origin row must show an empty cell — dropping this distinction is the failure mode the `?` exists to prevent. Test: the `"no pr"` case in `TestRowCellsPerKind` (Task 1); `TestNonGitHubOriginIsNotPRUnknown` must stay green (Task 3).
4. **Stale-PR suppression (#451) now reads through the cell.** A reused branch name must not render its old merged PR — the pin moves from tag texts to `prCell`. Test: the retargeted `TestPickerStalePRTagSuppressed` (Task 3).
5. **The wider `#N merged` cell must not break the table.** Rows stay exactly one line at 40/80/120 (clip, never wrap), the drop ladder keeps its order, and the 120-wide fixture view keeps every column. Tests: `TestRowListRendersOneLinePerRow`, `TestRowListHeaderLineIsTheTableHeader`, `TestRowTableDropsColumnsInOrderAndWhole` — all green in Task 1's full-package step.

---

### Task 1: The PR column cell (`prCell`)

**Files:**
- Modify: `wt/internal/tui/rowtable.go` (`rowCells`, `rtDropOrder` comment)
- Test: `wt/internal/tui/rowtable_test.go` (`tableFixture`, `TestRowCellsPerKind`)
- Modify: `docs/superpowers/specs/2026-11-13-wt-worktree-table-design.md` (§1's `PR#` bullet only)

**Interfaces:**
- Consumes: `cleanup.Row.PRTag *cleanup.PRInfo`, `Row.PRUnknown bool`, `Row.Pending bool`; `cleanup.PRInfo{Number int, State string}`; the `rtPR` column index.
- Produces: `func prCell(r cleanup.Row) string` — package `tui`, in `rowtable.go`. Returns `""` (no PR, not unknown), `?` (unknown), `#<n> <state-lowercased>` (known). It renders the PR fact only; the `Pending → "-"` override stays in `rowCells`, which callers of `prCell` do not get.

- [ ] **Step 1: Write the failing test**

In `rowtable_test.go`, `tableFixture`'s first row: change `PRTag: &cleanup.PRInfo{Number: 383}` to `PRTag: &cleanup.PRInfo{Number: 383, State: "MERGED"}` (an unset State would render a trailing space).

In `TestRowCellsPerKind`, change the first-row want from `"#383"` to `"#383 merged"`, and append this block (what/why comment per house style):

```go
// The PR column carries the PR fact alone: the state lower-cased, "?" while
// the lookup could not answer, blank when there is no PR, and "-" while
// pending beats both (verdict-shaped cells wait for the fill).
for _, c := range []struct {
	name string
	row  cleanup.Row
	want string
}{
	{"merged", cleanup.Row{Kind: cleanup.KindBranch, PRTag: &cleanup.PRInfo{Number: 555, State: "MERGED"}}, "#555 merged"},
	{"open", cleanup.Row{Kind: cleanup.KindBranch, PRTag: &cleanup.PRInfo{Number: 3, State: "OPEN"}}, "#3 open"},
	{"closed", cleanup.Row{Kind: cleanup.KindBranch, PRTag: &cleanup.PRInfo{Number: 4, State: "CLOSED"}}, "#4 closed"},
	{"unknown", cleanup.Row{Kind: cleanup.KindBranch, PRUnknown: true}, "?"},
	{"no pr", rows[1], ""},
	{"pending wins", cleanup.Row{Kind: cleanup.KindBranch, Pending: true, PRUnknown: true}, "-"},
} {
	if got := rowCells(c.row)[rtPR]; got != c.want {
		t.Errorf("PR cell (%s) = %q, want %q", c.name, got, c.want)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd wt && go test ./internal/tui -run TestRowCellsPerKind -v`
Expected: FAIL — first row's cell is `#383`, want `#383 merged`; the `unknown` case is `""`, want `?`.

- [ ] **Step 3: Implement `prCell(r cleanup.Row) string` in `wt/internal/tui/rowtable.go`**

```go
// prCell is the PR column's fact: "#383 merged" from the row's PR (GitHub's
// state, lower-cased), "?" while the lookup gave no usable answer, "" when
// there is no PR. rowCells overrides it with "-" while the row is pending.
func prCell(r cleanup.Row) string
```

Unknown checks before `PRTag` (a row is never both, but unknown wins — `RowTags`' old precedence). In `rowCells`, the existing `else if r.PRTag != nil` branch becomes `else { pr = prCell(r) }` so the pending override is untouched. Also restate `rtDropOrder`'s comment ("The PR tag is the most redundant…" → the PR column now carries the state, and the detail block's sentence names it) and, if needed, `rowCells`' doc comment to say the PR cell is `#N state` / `?`.

- [ ] **Step 4: Run the test, then the whole package**

Run: `cd wt && go test ./internal/tui -run TestRowCellsPerKind -v` then `cd wt && go test ./internal/tui ./internal/cleanup`
Expected: PASS. The full run is the Review Focus check (lines 1, 3, 5): one line per row at 40/80/120, all headings at 120, drop order unchanged, `TestRowListTagsWarnStyled` still finds `#517 merged` (now via the column). If any of those fail from the wider cell, fix them here, in this task.

- [ ] **Step 5: Update the parent spec's `PR#` bullet**

In `docs/superpowers/specs/2026-11-13-wt-worktree-table-design.md` §1, replace "`PR#` — `#383` from `Row.PRTag`. Blank when there is no PR." with the new cell rule: `#383 merged` from `Row.PRTag` (state lower-cased), `?` while the lookup could not answer, blank when there is no PR, `-` while pending.

- [ ] **Step 6: Commit**

```bash
git add wt/internal/tui/rowtable.go wt/internal/tui/rowtable_test.go docs/superpowers/specs/
git commit -m "feat(wt): PR# column carries the PR state and the unknown ?"
```

---

### Task 2: The filter reads the PR column

**Files:**
- Modify: `wt/internal/tui/row_list_launch.go` (`filterText`)
- Test: `wt/internal/tui/cleanup_list_test.go` (new test beside `TestFilterMatchesWorktreeBranchAndPR`)
- Modify: `wt/internal/tui/cleanup_list.go` (header comment, lines ~38-39 only)

**Interfaces:**
- Consumes: `prCell(r cleanup.Row) string` (Task 1); `filterText(it cleanupItem) string` (unchanged signature).
- Produces: nothing new — `filterText` keeps its signature and now also matches the PR cell, so Task 3's badge removal cannot regress `/`.

- [ ] **Step 1: Write the failing test**

```go
// TestFilterTextMatchesThePRColumn pins that the filter finds a row by its
// PR column cell ("#517", "merged") even when the row carries no badges: the
// PR badge is leaving RowTags, so the column is what the filter must read.
func TestFilterTextMatchesThePRColumn(t *testing.T) {
	it := cleanupItem{row: cleanup.Row{Kind: cleanup.KindBranch, Branch: "b",
		PRTag: &cleanup.PRInfo{Number: 517, State: "MERGED"}}}
	got := filterText(it)
	for _, want := range []string{"#517", "merged"} {
		if !strings.Contains(got, want) {
			t.Errorf("filterText = %q, want it to contain %q", got, want)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd wt && go test ./internal/tui -run TestFilterTextMatchesThePRColumn -v`
Expected: FAIL — `filterText = "b"` (name and branch only).

- [ ] **Step 3: Append the PR cell to `filterText`**

In `wt/internal/tui/row_list_launch.go`: after the tags loop, `parts = append(parts, prCell(it.row))`, and restate `filterText`'s doc comment (name, branch, tags and the PR column cell — so `#383` finds its PR's row). In `wt/internal/tui/cleanup_list.go` header lines 38-39, restate "`/` filters the rows by name … and the PR tags arrive later (withReport)" to match: the filter also matches the PR column cell, and the PR lookups fill those cells later.

- [ ] **Step 4: Run the tests**

Run: `cd wt && go test ./internal/tui -run 'TestFilter' -v`
Expected: PASS — both the new test and the pre-existing `TestFilterMatchesWorktreeBranchAndPR` (which must keep matching `#517`).

- [ ] **Step 5: Commit**

```bash
git add wt/internal/tui/row_list_launch.go wt/internal/tui/cleanup_list.go wt/internal/tui/cleanup_list_test.go
git commit -m "feat(wt): the list filter matches the PR# column cell"
```

---

### Task 3: The PR badge leaves `RowTags`

**Files:**
- Modify: `wt/internal/cleanup/present.go` (`RowTags` PR case, `RowTags` + `TagInfo` doc comments)
- Test: `wt/internal/cleanup/present_test.go`, `wt/internal/cleanup/pr_test.go`
- Test: `wt/internal/tui/picker_rows_test.go` (`TestPickerStalePRTagSuppressed`)
- Modify: `wt/internal/tui/cleanup_list_test.go` (two comment restatements, no assertion changes)
- Docs: `wt/docs/wt-cleanup.md`, `wt/docs/internals/tui.md`, `docs/guides/08-maintenance-and-troubleshooting.md`, root `CLAUDE.md`, `wt/CLAUDE.md`, `wt/CHANGELOG.md`

**Interfaces:**
- Consumes: `prCell` (Tasks 1-2) for the picker test's retarget; `tagTexts(row Row) []string` (existing helper in `present_test.go`).
- Produces: `func hasPRTag(row Row) bool` — test helper in `wt/internal/cleanup/present_test.go` beside `tagTexts` (same package, so `pr_test.go` can call it): reports whether any tag is `PR ?` or starts with `#`.

- [ ] **Step 1: Retarget the cleanup tests to assert the badge's absence (red)**

In `present_test.go`, next to `tagTexts`, add `hasPRTag` (Imports `slices`/`strings` are already present). Rewrite `TestRowTagsShowsPRQuestionMarkWhileUnknown` as:

```go
// TestRowTagsCarriesNoPRInfo pins that no badge carries PR info: "PR ?" and
// "#N state" read from the PR# column alone (RowTags' old PR badges).
// Why: the verdict line keeps only risk and fact badges.
func TestRowTagsCarriesNoPRInfo(t *testing.T) {
	for _, row := range []Row{
		{PRUnknown: true},
		{PRTag: &PRInfo{Number: 7, State: "MERGED"}},
	} {
		if got := tagTexts(row); len(got) != 0 {
			t.Errorf("row %+v: tags %v, want no PR badge", row, got)
		}
	}
}
```

In `TestRowTagsEveryFact`, delete the four cases `pr merged`, `pr open`, `pr closed`, `pr unknown`.

In `pr_test.go`, replace the `RowTags` block in `TestPRUnknownIsNeverProvenWhenAsked` (the `!= 1 || Text != "PR ?"` check) and in `TestPRTagOnceKnown` (the `!= 1 || Text != "#5 merged"` check) with:

```go
if hasPRTag(row) {
	t.Fatalf("tags %v, want no PR badge (it reads in the PR# column)", RowTags(row))
}
```

Keep every other assertion in both tests (the `PRUnknown`/`PRTag.Number` fact checks stay).

- [ ] **Step 2: Run the cleanup tests to verify they fail**

Run: `cd wt && go test ./internal/cleanup -run 'TestRowTagsCarriesNoPRInfo|TestRowTagsEveryFact|TestPRUnknownIsNeverProvenWhenAsked|TestPRTagOnceKnown' -v`
Expected: FAIL — `RowTags` still emits the PR badges.

- [ ] **Step 3: Retarget the picker's stale-PR test to `prCell` (green-neutral)**

In `picker_rows_test.go`, `TestPickerStalePRTagSuppressed`: replace the tag-texts join loop and its `switch` with:

```go
switch it.row.Name {
case "reused":
	if got := prCell(it.row); got != "" {
		t.Errorf("reused branch's PR cell %q, want blank (the old PR is suppressed)", got)
	}
case "kept":
	if got := prCell(it.row); got != "#11 merged" {
		t.Errorf("kept branch's PR cell %q, want #11 merged", got)
	}
}
```

Restate the test's comment where it says "tags nothing" → the cell reads blank. (Passes before the removal — `prCell` exists since Task 1 — and keeps the #451 pin alive after it.)

- [ ] **Step 4: Remove the PR case from `RowTags`**

In `present.go`: delete the `switch { case row.PRUnknown: … case row.PRTag != nil: … }` block from `RowTags`. Update `RowTags`'s doc comment (drop `PR ("PR ?" while unknown, "#N state" once known),` and note the PR reads in the `PR#` column) and `TagInfo`'s comment (`(root, you are here, PR, +remote)` → `(root, you are here, +remote)`).

- [ ] **Step 5: Run the cleanup package**

Run: `cd wt && go test ./internal/cleanup`
Expected: PASS (including `TestNonGitHubOriginIsNotPRUnknown` — a no-PR row has no badges either way).

- [ ] **Step 6: Run the tui package**

Run: `cd wt && go test ./internal/tui`
Expected: PASS — the filter tests pass through the column (Task 2), `TestPickerStalePRTagSuppressed` through its retarget, `TestRowListTagsWarnStyled` finds `#517 merged` via the column. If anything else fails here, it consumed the PR badge; retarget it to `prCell` the same way (that is the design: the column is the one place the PR reads).

- [ ] **Step 7: Restate the two comments in `cleanup_list_test.go`**

`TestFilterMatchesWorktreeBranchAndPR`'s header ("its PR tag (`#517`)" → its PR column cell) and `TestRowListTagsWarnStyled`'s header ("an info tag (the PR, +remote)" → the info tag `+remote`; the `#517 merged` text is now pinned by the PR column). No assertion changes.

- [ ] **Step 8: Update the docs (same change as the behavior)**

- `wt/docs/wt-cleanup.md:49` — the tag list "`dirty`, `unpushed`, the PR, and `PR ?` while a PR lookup is unknown" → tags without PR info; the `PR#` column names the PR and its state, `?` while a PR lookup is unknown.
- `wt/docs/wt-cleanup.md:62` — after the `WORKTREE BRANCH PR# …` column list, note the `PR#` cell: `#N <state>`, `?` when the lookup could not answer, blank when there is no PR.
- `wt/docs/wt-cleanup.md:93-94` — "floors a row below proven, with a `PR ?` tag" → "with `?` in its `PR#` column".
- `wt/docs/internals/tui.md:9` — "The filled report has `PR ?` on every row whose PR is unknown" → a row whose PR is unknown reads `?` in its `PR#` column; and "`/` … matches the row's name, branch and tags (so `#383` finds its PR's row)" → name, branch, tags and PR column cell.
- `docs/guides/08-maintenance-and-troubleshooting.md:636` — drop `PR ?` from the tag examples (`root`, `held`, `dirty`). `:645` — "the row shows `PR ?`" → "the row's `PR#` column shows `?`".
- Root `CLAUDE.md:24` — "an unknown PR (`PR ?`; a non-GitHub origin is exempt)" → "an unknown PR (`?` in the `PR#` column; …)"; in the column-list parenthetical add `PR#` (the PR and its state, `?` when the lookup could not answer).
- `wt/CLAUDE.md:274` — "`cleanup.FillPRs` (`PR ?` until it lands)" → "(`?` in the `PR#` column until it lands)".
- `wt/CHANGELOG.md:18` — "floors a row below proven (`PR ?`)" → "floors a row below proven (`?` in the `PR#` column)".

- [ ] **Step 9: Full verification**

Run: `cd wt && go test ./... && make check`, then repo root `make check-links`
Expected: all PASS (tests + lint/format; links validated over the edited markdown).

- [ ] **Step 10: Commit**

```bash
git add wt/internal/cleanup wt/internal/tui wt/docs docs/guides CLAUDE.md
git commit -m "feat(wt): PR state reads in the PR# column, the verdict-line badge is gone"
```
