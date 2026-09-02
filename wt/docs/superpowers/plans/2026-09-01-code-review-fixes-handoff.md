# Handoff: code-review fixes for feature/family-usage-sort

**Branch:** `feature/family-usage-sort` (worktree: `.worktrees/family-usage-sort`, module root: `wt/`)
**Status as of completion:** All 10 findings reviewed; the 8 actionable code-review fixes are done and pushed to origin as PR #16 (`1b4d6b8..c898f6a`). Working tree clean. Final verification passed:
```
go build ./...
go vet ./...
go test ./...
go test -race ./internal/tui/...
```

## Origin of this work

A `/code-review` background task (8 finder angles + Phase-2 empirical verification: reproducing tests written, run, then deleted; bubbles v1.0.0 semantics checked against the vendored source at `$(go env GOMODCACHE)/github.com/charmbracelet/bubbles@v1.0.0`) reviewed the diff on this branch and returned 10 ranked findings. The user said "fix issues" — all 10 are being fixed, one per commit, each preceded by a failing regression test (TDD) per `superpowers:test-driven-development`, and reviewed per `superpowers:receiving-code-review` (verify against actual code before implementing; the seam/API shape decisions below were cross-checked against `wt/CLAUDE.md`'s documented conventions, not taken on faith from the review text).

Commit messages all end with a `fix(code-review): address PR review finding on ...` line plus the required attribution trailer (Co-Authored-By + Claude-Session — see any commit in `git log` on this branch for the exact block to reuse).

## Completed (commits e50daeb, f3da88a, f06d330, 70fb1de, oldest→newest is 70fb1de→e50daeb)

1. **Finding 10 — seam shape** (`70fb1de`). `newUsageStore` in `internal/tui/model_list.go` was an inline closure; `wt/CLAUDE.md` documents seams as `var x = realX` + `func realX`. Converted to match, and added `newUsageStore` to the CLAUDE.md seam inventory sentence. Pure rename, no test needed (not a behavior change).

2. **Finding 2 — family-adjacency sort bug** (`f06d330`). `sortModelsByUsage` in `model_list.go` tie-broke only on composite score; on a tie (fresh install, all scores 0) it fell through to `sort.SliceStable`'s stability = registry order, which does **not** list one family's models contiguously — so `withFamilyDividers` could emit a duplicate "◈ family" header split across two non-adjacent runs. Fixed by adding a **family first-occurrence** tie-break (not alphabetical — alphabetical broke 4 existing tests by pushing the empty "other" family first; first-occurrence preserves registry order at the family level while keeping each family's models contiguous). New test: `TestSortModelsByUsageKeepsTiedFamiliesAdjacent` in `model_family_test.go`.

3. **Finding 4 — divider double-spacing** (`f3da88a`). `dividerItem.Render` wrote `label+"\n"`, but bubbles/list's `populatedView` already joins rows with `strings.Repeat("\n", Spacing()+1)`, and `DefaultDelegate.Render` (model rows) writes `"title\ndesc"` with **no** trailing newline. The extra `"\n"` doubled the blank-line gap under every family header. Fixed by dropping the trailing newline. New test: `TestModelListDelegateRenderHasNoTrailingNewline`.

4. **Findings 1 + 3 — filter/snap coordinate corruption** (`e50daeb`, the big one). Root cause verified against bubbles v1.0.0 source: `Index()`/`Select()`/`SelectedItem()` are all **visible-item-coordinate** once a filter is active; `Items()` always stays **unfiltered**. Two sub-bugs plus a keystroke-routing bug, all fixed together:
   - `snapSelectionOnModel` (`model_list.go`) and the phaseModel wrap block (`app.go`, the `tea.KeyMsg` case around line 257) both read `Items()` to find dividers/first/last-model-index but moved the cursor with `Select()` — filtered coordinate. Fixed: both now use `m.models.VisibleItems()`.
   - Second, independent bug found only by writing the regression test against real bubbles behavior: bubbles' `FilterMatchesMsg` handler (`case FilterMatchesMsg: m.filteredItems = ...; return m, nil`) does **not** clamp `Index()` when a filter narrows `VisibleItems()` — it skips the pagination/cursor clamp a keystroke would otherwise trigger. So a previously-valid cursor can end up **out of range** of the new, smaller filtered slice — not just "on a divider," but potentially not even a valid index to inspect. `snapSelectionOnModel` now detects and clamps this as its own case before the divider-avoidance walk.
   - `isTyping()` (`app.go`) had no `phaseModel` branch, so typing `q` while filtering the model picker quit the whole app. Added.
   - The `enter`/`phaseModel` case in `Update` ran the launch path (ollama check → launch) unconditionally, so Enter while filtering launched the highlighted model instead of applying the filter query (bubbles' own `AcceptWhileFiltering`, bound to Enter). Added a `if m.models.FilterState() == list.Filtering { break }` guard so it falls through to the passthrough `m.models.Update(msg)` at the bottom of `Update`, matching the existing `phaseList`/`phaseAgent` pattern.
   - New tests (all drive a **real** async filter Cmd chain rather than asserting on synthetic state — see `drainFilterMatches`/`typeFilterQuery` helpers added to `model_family_test.go`, which recursively execute `tea.Cmd`/`tea.BatchMsg` to resolve `list.FilterMatchesMsg`, mirroring what the real bubbletea runtime does): `TestFilterToSingleMatchKeepsSelectionValid`, `TestWrapAroundStaysValidAfterFilterApplied` (model_family_test.go), `TestQDoesNotQuitWhileFilteringModelList`, `TestEnterWhileFilteringAppliesFilterNotLaunch` (app_test.go).

★ Key technical fact worth re-deriving if context is lost: bubbles v1.0.0's `list.Model.Select(i)`/`.Index()`/`.SelectedItem()` operate in **VisibleItems()** coordinates once `FilterState() != Unfiltered` (both `Filtering` and `FilterApplied`); `.Items()` is always the raw unfiltered slice. Any code that indexes `.Items()` and then calls `.Select()`/reads `.Index()` is mixing coordinate spaces. Verified directly from `$(go env GOMODCACHE)/github.com/charmbracelet/bubbles@v1.0.0/list/list.go`.

5. **Findings 7 + 8 — single-pass usage scan** (`eda0740`). Deleted `Store.FamilyCounts`; `buildModelListWithFamilies` now runs ONE `Counts` pass over the full catalog's IDs and aggregates per-family totals in memory via the new `usage.AggregateByFamily` pure function. Preserved the `FamilyCounts` no-pre-seed contract, migrated its tests to the new shape, added a tui guard test (`TestBuildModelListWithFamiliesFamilyCountsUseFullCatalog`) that family totals stay full-catalog accurate under an eligible-subset filter, and documented `Counts`' best-effort truncation policy. Updated `wt/CLAUDE.md` and `docs/guides/06-wt-agents-and-models.md`.

6. **Finding 6 — test isolation from real usage.jsonl** (`d224dff`). Extracted shared `stubUsageStore(t)` test helper and replaced the 8 inline seam-swap copies in `model_family_test.go`. Rewired `phaseModelWithList` to read counts through the `newUsageStore` seam instead of `usage.NewStore()` directly. Isolated the 7 tests that were reaching the usage path without either seam stub or `XDG_CONFIG_HOME` isolation, also adding `tempStateDir(t)` where rotation state was being read unisolated.

7. **Finding 5 — delete production-dead `buildModelList`; migrate tests** (`6d62cc3`). Removed `buildModelList` and the now-unused `modelIDs` helper. Rewrote `phaseModelWithList` to route through `buildModelListWithFamilies` and position the cursor exactly like production `enterModelPhase`. Migrated the 5 direct `buildModelList` call sites in `app_test.go` to the family-aware builder with divider-aware index assertions.

8. **Finding 9 — stale test doc comments** (`c898f6a`). Updated the 5 stale top-level/in-line test comments in `agent_model_test.go` to match the divider-aware model picker layout (first model row is index 1, not 0; wrap targets shifted accordingly).

## Remaining (none)

All actionable findings are completed and pushed to PR #16.

### Finding 7 + 8 — single-pass usage scan (combine into one task/commit)
**Files:** `internal/usage/usage.go`, `internal/tui/model_list.go` (`buildModelListWithFamilies`), `internal/usage/usage_test.go`.

Problem: `buildModelListWithFamilies` calls both `store.Counts(modelIDs)` and `store.FamilyCounts(familyOf)` — two full file opens + JSONL scans of the same `usage.jsonl` per picker entry. `FamilyCounts` also copy-pastes `Counts`' entire scan/bucket loop (24h/7d/30d boundary checks, corrupt-line-skip policy) — a second place to drift if the boundary logic ever changes. Both also omit the `scanner.Err()` check that `pruneOlderThan` (line 119) has, so an over-long corrupt line silently truncates results.

I was mid-investigation when paused, reading `usage.go:125-220` (both `Counts` and `FamilyCounts` bodies, pasted below for reference) to decide the fix shape. Key constraint found: **`FamilyCounts` has a documented behavioral difference from `Counts`** — "Unlike Counts, families are not pre-seeded: only families with at least one event within the retention window appear as keys, so a missing family key means zero usage." (see doc comment at usage.go:184-186, and the test `TestFamilyCountsDoesNotPreSeedZeroFamilies` in usage_test.go). Any fix must preserve this — a naive "always aggregate every family key" would break that contract and its test.

**Design decision (not yet made — pick one when resuming):**
- **Option A** (my leaning, stated in-conversation before pausing): Delete `FamilyCounts` entirely. Make `buildModelListWithFamilies` call `Counts` ONCE with the **full catalog's** IDs (not just the eligible/filtered slice — needed so family totals stay accurate under a `-T`/`-F` filter, matching `FamilyCounts`'s existing `familyOf`-from-full-catalog design), then aggregate per-family in the tui caller by summing each model's `UsageCounts` into its family bucket. This eliminates the second file scan by construction and removes the duplicated loop, but changes the public API (`usage.FamilyCounts` — a documented, tested public method — goes away). Would need to migrate/delete `TestFamilyCountsAggregatesByFamily`, `TestFamilyCountsMissingFile`, `TestFamilyCountsDoesNotPreSeedZeroFamilies` in `usage_test.go`, and update `wt/CLAUDE.md`'s "Store.FamilyCounts aggregates..." sentence (Rotation (Go) section) plus `docs/guides/06-wt-agents-and-models.md:164` which also references `Store.FamilyCounts` by name (`git grep -n FamilyCounts` to find all mentions before deleting).
- **Option B** (the review's alternative, more conservative): Keep both methods but extract a shared internal scan/bucket helper (e.g. `countsByKey(path, asOf, keyFn func(event) (key string, ok bool))`) that both `Counts` and `FamilyCounts` call, so the boundary logic and `scanner.Err()` check live in one place — but this does NOT eliminate the double file-scan-per-picker-entry (still two calls, two file opens), only the code duplication. Cheaper diff, less complete fix.

Recommend Option A per the review's own preferred phrasing ("Cheaper: ... or call Counts once with the FULL catalog's IDs and sum per-model counts into per-family counts in the caller") — it actually fixes the "scans twice" performance problem, not just the duplication. But confirm this reads right before implementing; it's a real API deletion, more invasive than the other findings so far.

**TDD approach:** Write the aggregation test first against whatever the new `buildModelListWithFamilies`-side (or a new small helper function's) call shape will be — probably a test asserting a single `Counts` call over a full-catalog ID set produces correct per-family sums including the "family with zero events is absent from the map" contract. Then implement.

`Counts` body (for reference, current state):
```go
func (s *Store) Counts(modelIDs []string) map[string]UsageCounts {
	want := map[string]bool{}
	for _, id := range modelIDs {
		want[id] = true
	}
	out := map[string]UsageCounts{}
	for _, id := range modelIDs {
		out[id] = UsageCounts{}
	}
	f, err := os.Open(s.path())
	if err != nil {
		return out
	}
	defer f.Close()
	today := now().UTC()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var ev event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if !want[ev.ModelID] {
			continue
		}
		age := today.Sub(ev.Timestamp.UTC())
		c := out[ev.ModelID]
		if age < 24*time.Hour {
			c.OneDay++
		}
		if age < 7*24*time.Hour {
			c.SevenDay++
		}
		if age < retentionWindow {
			c.ThirtyDay++
		}
		out[ev.ModelID] = c
	}
	return out
}
```
Note: `Counts` also lacks the `scanner.Err()` check — fix that too regardless of which option is chosen (both this and `FamilyCounts` are missing it; `pruneOlderThan` at usage.go:119 has the pattern to copy: `if err := scanner.Err(); err != nil { return nil, err }`, adjusted for `Counts`'/`FamilyCounts`' non-error-returning signatures — probably just means the caller can't detect truncation, which may be acceptable for a read-only display path, but at minimum note this in a comment or match pruneOlderThan's error-returning shape if feasible).

Caller site to update: `internal/tui/model_list.go:169-172` (currently `store.Counts(modelIDs(models)); store.FamilyCounts(familyOf)`).

### Finding 6 — test isolation from real usage.jsonl
**File:** `internal/tui/agent_picker_test.go` (primarily `TestPhaseModelHonorsFilters` at line ~114), possibly `TestPhaseAgentEnterSkipsPickerWhenSingleModel` and `TestPinnedAgentSingleModelSkipsPicker` in the same file or `agent_model_test.go`.

Problem: since usage counts now drive picker sort order (this whole feature branch's point), any test that builds the picker via `enterModelPhase` without isolating the usage store reads the **developer's real `~/.config/agent-wt/usage.jsonl`**. The isolation commit earlier in this branch's history (`62ef5dd test(tui): isolate family-builder tests from real usage store`) only covered `model_family_test.go`'s direct `newUsageStore` stubbing pattern (`old := newUsageStore; newUsageStore = func() *usage.Store { return usage.NewStoreAt(t.TempDir()) }; defer func() { newUsageStore = old }()` — see any test in `model_family_test.go` for the exact pattern, now trivial to reuse since finding 10's seam-rename is done). `agent_picker_test.go`'s tests don't do this.

Fix: add the same `newUsageStore` stub (or a shared test helper wrapping it, e.g. `stubUsageStore(t *testing.T)` in a shared test-helpers file, since the pattern now repeats across `model_family_test.go`, and after this fix, `agent_picker_test.go` too — worth extracting once used 4+ times) to every `enterModelPhase`-driving test lacking it. Grep `enterModelPhase\|buildModelListWithFamilies` across `*_test.go` to find all call sites needing the stub; cross-check against `tempStateDir(t)` (agent_model_test.go:25) which isolates `XDG_CONFIG_HOME`/rotation state but does **NOT** stub `newUsageStore` (they're two different seams — confirmed by reading `tempStateDir`'s body, it only sets `XDG_CONFIG_HOME` and creates a rotation dir, it doesn't touch `newUsageStore`). Both may be needed together in some tests.

No TDD-red-test needed in the traditional sense here (it's a test-hygiene fix, not new behavior) — but per TDD spirit, confirm the fix matters by checking whether the current test would behave differently against a seeded fixture with many events for the test's fixture model ID (the review found the developer's real usage.jsonl has 867 events for `ollama/gemma4:9b`, the exact fixture ID some of these tests use — that's what makes this non-theoretical).

### Finding 5 — delete production-dead `buildModelList`; migrate tests
**Files:** `internal/tui/model_list.go` (delete `buildModelList` at line ~57, after the rename it's now offset slightly by finding 10/2/3's edits — re-locate by name), `internal/tui/agent_model_test.go` (`phaseModelWithList` helper at line ~62, `singleModelList` unaffected — different helper), `internal/tui/app_test.go` (`TestModelPickerFilterReceivesJKKeys`, `TestModelPickerWrapsFromTopToBottom`, `TestModelPickerWrapsFromBottomToTop` — all three call `buildModelList` directly).

Problem: after this branch's diff, `buildModelList` (the old, divider-less, unsorted list builder) is called **only by tests**, never by production code (production always goes through `buildModelListWithFamilies`). This means the wrap/filter-key tests exercise a list shape production never renders — which is *why* the finding-1 coordinate bug (already fixed in commit `e50daeb`) passed the whole suite despite being real: none of the tests that would have caught it were testing the divider-aware production path.

Fix: delete `buildModelList` (and `modelIDs` helper only if nothing else uses it — check first, it's likely still used by `buildModelListWithFamilies` itself for `store.Counts(modelIDs(models))`, so don't delete that). Migrate all test call sites to build lists via `buildModelListWithFamilies` (or via the full `enterModelPhase`/`phaseModelWithList` path) instead, updating index assertions since `buildModelListWithFamilies` inserts a leading divider (offset every "index N" assertion by however many dividers precede the target row — the existing family-aware tests in `model_family_test.go` and `agent_model_test.go` (post-`03f80b2`/`5b97fc3`) already show the "+1 for leading divider" pattern to follow, e.g. `TestSelectedEntryNoLastStartsAtZero`: "want 1 (first model row; index 0 is the leading divider)"). Also fix the stale comment at `agent_model_test.go:~608-610` (`TestPhaseModelWithListBuildsAndPositionsCursor`'s doc comment): "The production code uses the same list-build path" — no longer true once `buildModelList` is deleted and `phaseModelWithList` migrates to the family-aware builder (or is itself deleted/redirected).

This is the most mechanical of the remaining findings but touches the most call sites — budget the most time here. Since `buildModelList`/`phaseModelWithList` are widely used as test scaffolding (grep showed ~15+ call sites across `agent_model_test.go` and `app_test.go` before this session paused), consider whether `phaseModelWithList` itself should be rewritten to call `buildModelListWithFamilies` under the hood (keeping its name/signature so most callers don't change) rather than deleting it — that would satisfy the finding ("migrate the test helpers to the family-aware builder") with a much smaller diff than updating every call site individually. Re-verify this plan against the actual current file contents when resuming; do not assume the line numbers above are still exact — findings 1-4's edits shifted line numbers in `model_list.go`.

### Finding 9 — stale test doc comments
**File:** `internal/tui/agent_model_test.go`. Five comments contradicting their tests' current (divider-aware) assertions:
- `TestSelectedEntryNoLastStartsAtZero` (~line 705 pre-session-start, will have moved): comment said "lands the cursor at index 0" but assertion wants 1 — **already correct in the current file** (I read this file earlier in the session; the comment at the top of that test already says "the cursor at index 0" is WRONG framing — re ad the actual current text before trusting this description, the original review may have been describing an earlier commit state that a prior commit on this branch (`03f80b2`/`5b97fc3`) already partially fixed). **Re-verify all 5 against the current file before editing** — do not blindly apply the review's line numbers/quotes, they may already be stale or already fixed by earlier commits on this branch predating this session's fixes.
- Same caveat applies to `TestSelectedEntryLastMissingFallsBackToZero`, `TestSelectedEntryLastLastInListWrapsToZero`, `TestNextEntryAfterLaunchAdvancesCursor`, `TestNextEntryAfterManualPickAdvancesFromManualPick`.

Do this **last**, after finding 5's migration, since finding 5 will likely shift indices again (deleting `buildModelList` / rewriting `phaseModelWithList` to route through `buildModelListWithFamilies` may change which tests need comment fixes, or fix some automatically if the assertions get rewritten anyway during the finding-5 migration).

## Result

All 8 actionable findings were completed in this worktree and pushed to
origin as PR #16 (`1b4d6b8..c898f6a`). The final verification passed:

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/.worktrees/family-usage-sort/wt
go build ./...
go vet ./...
go test ./...
go test -race ./internal/tui/...
```

Pushed by explicit user request after the completion report.

## User instruction context

- **Pull Request Discipline** was respected: no push happened until the user explicitly asked for it. The work was reported as complete before PR #16 was updated.
- The session maintained one commit per finding; this handoff was updated and committed as a dated completion record.
- **Test doc comments**: every new/changed test has a comment describing what it tests and why it matters (global CLAUDE.md, "Test Documentation").
