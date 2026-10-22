# cloud-sync removals and manual models Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `wt cloud-sync` removes OpenRouter models that vanished from OpenRouter's list, and leaves hand-added (`source = "manual"`) cloud models alone until their source lists them, all removals gated by approval.

**Architecture:** `config.Source` gains `manual`. The two pure planners in `wt/internal/cloudsync` (`PlanCatalog` for ollama, `PlanPrices` for OpenRouter) learn three new plan lists: `Removals` (OpenRouter only), `Kept` (manual entries off the source) and `Graduations` (manual entries now on the source, flipped to `curated`). `wt/cmd/wt/cloudsync*.go` gates OpenRouter removals exactly like ollama's (mass-removal refusal, per-flow digest, exit codes 4/5), and `--approve-removals` takes a comma list so one `--yes` run can apply both flows. `wt model add` sets `manual`; `wt model edit --source` sets it on existing entries.

**Tech Stack:** Go 1.26.7 (module `wt/`), cobra, `internal/tomlw`, `config.UpdateRegistry`.

**Spec:** `docs/superpowers/specs/2026-10-22-cloud-sync-removals-and-manual-models-design.md` (read it first; its "Settled after the builder check" section holds decisions this plan relies on).

## Global Constraints

- Registry is written only through `config.UpdateRegistry` (patch-shaped `RegistryDoc` ops: `PatchModel`, `RemoveModel`); an `apply` function must be pure.
- Planners stay pure: no I/O, no clock; `Format()` is deterministic and is compared with the re-plan made under the registry lock.
- **A plan with no manual entries and no OpenRouter removals must format byte-identically to today** (existing golden tests and the removal-digest vectors in `internal/cloudsync/catalog_test.go` must pass unchanged).
- Every `Test*` gets a top-level `//` comment stating what it tests and why.
- Test seams already exist (`cloudFetch`, `confirmCloudSync`, `ollamaCLI`, `cloudSyncNow`); no test touches the real network, `ollama` or `~/.config`. Ad-hoc `wt` runs against a scratch registry also set `WT_LITELLM_CONFIG`.
- Behavior docs change with the behavior: `wt/docs/wt-cloud-sync.md`, `wt/.claude/skills/cloud-sync/SKILL.md`, `wt/docs/wt-model.md`, `wt/docs/internals/cloud-sync.md`, `wt/CHANGELOG.md`; `source` is not in `wt model list --json`, so no `docs/contracts` fixture changes unless Task 1 finds otherwise.
- Commit messages end with `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`. Run from `wt/`: `go test ./...`, `go vet ./...`, `make check`.

## Review Focus

1. A model OpenRouter lists but with a negative, missing or time-of-day price must count as **present** (never a removal). Verify `ParseOpenRouter` keeps such ids in its map; test in Task 3.
2. An empty or tiny OpenRouter list plans **no** removals (outage must not read as "everything is gone"). Test in Task 3.
3. A `manual` ollama entry whose `model_name` is not the tag ollama publishes is neither re-tagged, removed, graduated, nor its tag `ollama rm`'d. Test in Task 2.
4. `--yes --approve-removals A` where only one flow's digest is given: that flow applies, the other is refused (exit 4/5 of the refused flow), nothing leaks across. A comma list with spaces or empty elements is tolerated. Test in Task 4.
5. A registry with no manual entries and no vanished OpenRouter models behaves exactly as before (plan text, digest, exit codes). Tests in Tasks 2, 3 and 4 assert the unchanged goldens.

---

### Task 1: The `manual` source, `wt model add` and `wt model edit --source`

**Files:**
- Modify: `wt/internal/config/config.go:31-36`, `wt/internal/cloudsync/entry.go`, `wt/internal/modeladmin/fields.go`, `wt/internal/modeladmin/apply.go` (`planAdd`, `Edit`), `wt/cmd/wt/model_write.go` (edit command)
- Test: `wt/internal/cloudsync/entry_test.go` (create if absent), `wt/internal/modeladmin/*_test.go`, `wt/cmd/wt/model_write_test.go`, a `config` registry round-trip test beside the existing registry tests

**Interfaces:**
- Produces:
  - `config.SourceManual Source = "manual"`
  - `cloudsync.Entry.Source string` (read from the row's `source` key by `Entries`) and `func (e Entry) Manual() bool`
  - `func cloudsync.CoveredBySync(providerID, location, modelName string) bool`: true for provider `openrouter`, or provider `ollama` when `location == "cloud"` or `IsCloudTag(modelName)`
  - `modeladmin.FieldSource = "source"`; `Fields.Source *string`; `Fields.Empty()` accounts for it

- [ ] **Step 1: Commit the design and this plan** onto the feature branch (create the branch/worktree per `superpowers:using-git-worktrees`): `git add docs/superpowers/specs/2026-10-22-cloud-sync-removals-and-manual-models-design.md docs/superpowers/plans/2026-10-22-cloud-sync-removals-and-manual-models.md && git commit -m "docs: design and plan for cloud-sync removals and manual models"`.
- [ ] **Step 2: Write failing tests**
  - `TestEntriesReadsSource`: a row with `source = "manual"` gives `Entry.Source == "manual"` and `Manual() == true`; a row with no `source` gives `Manual() == false`.
  - `TestCoveredBySync`: `("openrouter","", "a/b")` true; `("ollama","cloud","beam")` true; `("ollama","","beam:cloud")` true; `("ollama","local","qwen3:8b")` false; `("omlx","", "x")` false.
  - `TestAddCloudModelIsManual` (modeladmin, using the package's registry fixture helpers): adding `openrouter vendor/m --family f` and `ollama beam:cloud --family beam` writes `source = "manual"`; adding `ollama qwen3:8b --family qwen3` (local) writes no `source` key.
  - `TestEditSourceManualAndCuratedOnCoveredOnly`: `Edit(id, Fields{Source: ptr("manual")})` on a covered row sets it, `ptr("curated")` sets it back, `ptr("discovered")` and `ptr("bogus")` return a `*FieldError` with `Field == FieldSource`; on an uncovered row (local ollama) both `manual` and `curated` are refused with a message saying `--source is for models cloud-sync covers (openrouter, or ollama cloud)`.
  - `TestManualSourceRoundTripsThroughTheRegistry`: a registry with `source = "manual"` loads (`config.Load` does not reject it) and `wt`'s writer keeps the value.
  - `TestModelEditSourceFlag` (cmd/wt): `wt model edit <id> --source manual` works; `wt model add` has no `--source` flag.
- [ ] **Step 3: Run them and confirm they fail:** `cd wt && go test ./internal/cloudsync ./internal/modeladmin ./internal/config ./cmd/wt -run 'Source|Manual|Covered' -v` → FAIL (undefined symbols).
- [ ] **Step 4: Implement** the interfaces above. `planAdd` sets `set["source"] = "manual"` when `CoveredBySync(req.ProviderID, locationOf(req.Fields), req.ModelName)` (`locationOf` = trimmed `*Fields.Location`, `""` when nil). `Fields.patch()` validates `Source` to `manual|curated` and sets `set["source"]`; `Edit` additionally refuses (inside its `UpdateRegistry` closure, after `d.Model(id)`, using the row's `provider_id`, `location`, `model_name`) a source change on an uncovered row. Register `--source` only on the edit command (not via `modelFieldFlags`, which `add` shares) and pass it into `Fields`. If `config.Load` or the registry validators reject an unknown `source`, accept `manual` there too.
- [ ] **Step 5: Run the tests** (same command) → PASS; then `go test ./...` → PASS.
- [ ] **Step 6: Commit:** `git commit -am "feat(wt): manual model source; add sets it, edit --source changes it"` (include new test files).

---

### Task 2: ollama planner: protect and graduate manual entries

**Files:**
- Modify: `wt/internal/cloudsync/catalog.go` (`CatalogPlan`, `PlanCatalog`, `Format`, `HasWork`, digest helper), `wt/internal/cloudsync/apply.go` (`CatalogPlan.Apply`, `CatalogApplied`), `wt/cmd/wt/cloudsync.go` (the `ollama: updated …` line)
- Test: `wt/internal/cloudsync/catalog_test.go`, `wt/cmd/wt/cloudsync_catalog_test.go`

**Interfaces:**
- Consumes: `Entry.Manual()` (Task 1).
- Produces:
  - `CatalogPlan.Graduations []string` and `CatalogPlan.Kept []string` (entry ids)
  - `CatalogApplied.Graduated int`
  - unexported `digestOf(items []string) string` (the existing digest body, shared with Task 3); `RemovalDigest()` output unchanged for every existing plan

- [ ] **Step 1: Write failing tests** (entries built with `Source: "manual"`):
  - `TestPlanKeepsAManualEntryOffThePage`: a manual ollama cloud entry the page does not list is in `Kept`, not `Removals`, is not in `StrayTags`, and `CloudEntries` does not count it (so `MassRemoval` ignores it).
  - `TestPlanGraduatesAManualEntryTheCatalogListsAndReprices`: the page lists it → it is in `Graduations` and also gets its normal `Updates`/`Unchanged` treatment.
  - `TestPlanManualEntryUnderAnUnpublishedTagIsNeitherRetaggedNorRemoved` (Review Focus 3): resolved tag differs from `entry.ModelName` → no `Replaced`, no `Removals`, no `Additions` for it, a warning names it, not in `Graduations`; the published tag is not turned into a `StrayTags` entry.
  - `TestPlanUnresolvedModelDoesNotGraduateAManualEntry`: the `tag == ""` branch protects the manual entry but does not graduate it.
  - `TestCatalogPlanFormatShowsManualLines`: `Format()` contains `Graduated (manual -> curated) (1):` followed by `  <id>`, and `Kept (manual, not on ollama.com/pricing) (1):` followed by `  <id>`; the sections appear only when non-empty.
  - `TestApplyGraduatesToCurated`: after `Apply`, the row's `source` is `curated`, `done.Graduated == 1`, and the row is not in `Removes`.
  - Existing tests (`TestRemovalDigestIsAFixedFormat`, `TestCatalogPlanFormat`, the removal/mass-removal tests) pass unmodified.
- [ ] **Step 2: Run:** `go test ./internal/cloudsync -run 'Manual|Graduat|Kept' -v` → FAIL.
- [ ] **Step 3: Implement.**
  - `PlanCatalog`: skip manual entries in the `CloudEntries` count; in the `entry != nil` path append the entry to `Graduations` when `entry.Manual()` (after the usual matching, once per entry); in the retag branch, a manual `retagged` entry is marked `matched`, gets a warning (`<id>: manual entry's tag "<x>" is not the one ollama publishes ("<y>"); kept as is`), has both tags set in `listed`, and is neither replaced nor graduated; in the final removal loop a manual `isOllamaCloud` entry not matched goes to `Kept`.
  - `Format`: add the two sections after the existing additions/removals sections, only when non-empty.
  - `HasWork` counts `Graduations`. `Apply` patches `{"source": "curated"}` for each graduation via `doc.PatchModel`, counts `Graduated`; `CatalogApplied.Changed()` is unchanged (graduation needs no route sync). The command's result line appends `, graduated N` only when N > 0.
  - Extract `digestOf` from `RemovalDigest`; behavior identical.
- [ ] **Step 4: Run** `go test ./internal/cloudsync ./cmd/wt -v -run 'Catalog|Plan|Digest|Ollama'` → PASS, then `go test ./...`.
- [ ] **Step 5: Commit:** `feat(wt): cloud-sync keeps manual ollama models until the pricing page lists them`.

---

### Task 3: OpenRouter planner: removals, kept and graduations

**Files:**
- Modify: `wt/internal/cloudsync/openrouter.go` (`ParseOpenRouter` if needed, `PricePlan`, `PlanPrices`, `Format`, `HasWork`), `wt/internal/cloudsync/apply.go` (`PricePlan.Apply`, `PricesApplied`)
- Test: `wt/internal/cloudsync/openrouter_test.go`

**Interfaces:**
- Consumes: `Entry.Manual()`, `digestOf` (Task 2).
- Produces:
  - `const MinListedForRemovals = 20` (documented: a list shorter than this is treated as an outage)
  - `PricePlan.Removals, Kept, Graduations []string`; `PricePlan.RemovableCandidates int` (non-manual OpenRouter-priced entries); `PricePlan.Withheld string` (reason removals were not planned, "" otherwise)
  - `func (p *PricePlan) EntriesGone() int`, `func (p *PricePlan) MassRemoval() bool` (`len(Removals)*2 > RemovableCandidates`), `func (p *PricePlan) RemovalDigest() string` (`digestOf` over sorted `Removals`, "" when none)
  - `PricesApplied.Removed, Graduated int`

- [ ] **Step 1: Write failing tests** (build `api` maps with `APIPrice`; pad with ≥20 unrelated ids where the test needs a "healthy" list):
  - `TestPlanPricesRemovesAModelOpenRouterNoLongerLists`: absent id → `Removals`, no `No OpenRouter match` warning for it.
  - `TestPlanPricesNeverRemovesAListedModelWithoutAUsablePrice` (Review Focus 1): ids present with `Unusable`, `Skipped`, or no price → no removal, today's warnings kept. Also a `ParseOpenRouter` test: a response containing a model with `"pricing": {"prompt": "-1"}` and one with no pricing object yields both ids as map keys; fix `ParseOpenRouter` if it drops them.
  - `TestPlanPricesWithholdsRemovalsOnATinyList` (Review Focus 2): `api` empty, and `api` with `MinListedForRemovals-1` entries → no `Removals`, `Withheld` non-empty, the old `No OpenRouter match` warnings remain, Format shows `Removals withheld: …`.
  - `TestPlanPricesKeepsAManualModelOffTheList`: manual + absent → `Kept`, not `Removals`, excluded from `RemovableCandidates`.
  - `TestPlanPricesGraduatesAManualModelOnTheList`: manual + present (including with `price.Skipped` set, i.e. free/unreadable) → `Graduations`; it still takes the normal price outcome.
  - `TestPricePlanMassRemoval`: 2 of 3 candidates removed → `MassRemoval()`; 1 of 3 → not.
  - `TestPricePlanRemovalDigestIsStable`: fixed vector for ids `["openrouter/b","openrouter/a"]` (sort-insensitive) equal to `digestOf(sorted ids)`, 12 hex chars; `""` for no removals.
  - `TestPricePlanFormatRemovalsSection`: contains `Registry removals — not on openrouter.ai/api/v1/models (1):`, the id line, and `Removal digest: <d> (apply non-interactively with `--yes --approve-removals <d>`)`; plus `Graduated (manual -> curated)` / `Kept (manual, not on openrouter.ai)` sections only when non-empty.
  - `TestPricePlanFormatUnchangedWithoutRemovals` (Review Focus 5): the existing format tests pass unmodified.
  - `TestPricePlanApplyRemovesAndGraduates`: after `Apply`, removed rows are gone, graduated rows have `source = "curated"`, counts set.
- [ ] **Step 2: Run:** `go test ./internal/cloudsync -run 'PricePlan|PlanPrices|ParseOpenRouter' -v` → FAIL.
- [ ] **Step 3: Implement.** In `PlanPrices`: count `RemovableCandidates` for non-manual candidates; on a missing id: manual → `Kept`; else if `len(api) < MinListedForRemovals` → keep today's warning and set `Withheld` once (`OpenRouter's list has <n> models (fewer than <MinListedForRemovals>), so no removals are planned`); else `Removals`. On a present id of a manual entry, append `Graduations` before the usual price outcomes. `Apply` patches `source` to `curated` for graduations and calls `doc.RemoveModel` for removals after the price patches (removal digest and Format cover removals).
- [ ] **Step 4: Run** the filter above, then `go test ./...` → PASS.
- [ ] **Step 5: Commit:** `feat(wt): cloud-sync removes OpenRouter models that vanished from OpenRouter's list`.

---

### Task 4: Command wiring: gates, digests, flags, exit codes

**Files:**
- Modify: `wt/cmd/wt/cloudsync.go` (flag validation and help text, `cloudSyncOutcome`, `planOpenRouterFlow`, `runCloudSync`, result lines), `wt/cmd/wt/cloudsync_catalog.go` (`ollamaGate` reads the digest list)
- Test: `wt/cmd/wt/cloudsync_test.go`, `wt/cmd/wt/cloudsync_catalog_test.go`, `wt/cmd/wt/cloudsync_flownames_test.go`

**Interfaces:**
- Consumes: `PricePlan.{Removals,Kept,Graduations,MassRemoval,EntriesGone,RemovalDigest}` and `PricesApplied.{Removed,Graduated}` (Task 3); `CatalogApplied.Graduated` (Task 2).
- Produces:
  - `func approvedDigests(flag string) []string`: splits on commas, trims spaces, drops empties
  - `func openrouterGate(errOut io.Writer, r *openRouterRun, o cloudSyncOpts, res *cloudSyncOutcome) bool`
  - `cloudSyncOutcome.openrouterCode int` (4 or 5) and `openrouterWhy string`

- [ ] **Step 1: Write failing tests** (cmd/wt tests use the existing fake `cloudFetch`, `confirmCloudSync` and registry fixtures):
  - `TestApprovedDigests`: `"a, b,,c"` → `[a b c]`; `""` → nil.
  - `TestOpenRouterRemovalNeedsApprovalUnderYes`: a vanished OpenRouter model, `--yes` with no digest → exit 5, message `cloud-sync: the openrouter flow changed nothing: removals not approved`, registry unchanged; with the digest from `--dry-run` → removed and `openrouter: refreshed N model(s); … removed 1`.
  - `TestOpenRouterMassRemovalRefusedWithoutForce`: exit 4, `--force` applies.
  - `TestInteractiveDeclineRemovesNothing`: `confirmCloudSync` returns false → registry unchanged, both flows `not applied (declined)`.
  - `TestYesWithOneFlowsDigestAppliesOnlyThatFlow` (Review Focus 4): both flows plan removals, `--approve-removals <ollamaDigest>` → ollama applied, openrouter refused with exit 5; `--approve-removals "<d1>, <d2>"` → both applied, exit 0.
  - `TestDryRunPrintsCombinedApproveLine`: with both digests, output contains `removals: approve both with --yes --approve-removals <d1>,<d2>`; absent with one digest.
  - `TestApproveAndForceAreAcceptedWithOnlyOpenrouter`: `--only openrouter --approve-removals x` and `--force` no longer error; `--only openrouter --html f` still errors with `--html is for the ollama flow, which --only openrouter leaves out`.
  - `TestRoutesSyncAfterAnOpenRouterRemoval`: a removal triggers the route sync even when no price changed.
  - `TestNoRefreshAdviceWhenEveryCandidateIsRemoved`: the `no model could be refreshed` advice is not printed when the plan has removals.
  - Unchanged-behavior tests (Review Focus 5): existing cloud-sync tests pass unmodified.
- [ ] **Step 2: Run:** `go test ./cmd/wt -run 'CloudSync|Approved|OpenRouter' -v` → FAIL.
- [ ] **Step 3: Implement.**
  - Flag validation: only `--html` stays ollama-only; `--approve-removals` and `--force` apply to both flows. Update flag help (`"with --yes: the removal digest(s) a reviewed --dry-run printed, comma-separated"`; `--force`: `"apply even if more than half the flow's entries would be removed"`), the command's Long text and the exit-status block (4 and 5 are either flow's; 2 and 3 stay ollama's).
  - `ollamaGate` and `openrouterGate` pass when `slices.Contains(approvedDigests(o.approve), digest)`. `openrouterGate` mirrors `ollamaGate`: mass-removal line `openrouter: error: N of M OpenRouter-priced models would be removed — check OpenRouter's list is complete, then re-run with --force. Nothing was changed by the openrouter flow.` (code 4); unapproved digest line with the same `--yes --approve-removals <digest>` advice (code 5).
  - `cloudSyncOutcome.err()`: the ollama code keeps priority; otherwise an `openrouterCode` yields `exitCodeError{code, "cloud-sync: the openrouter flow changed nothing: <why>"}`; a stale OpenRouter plan keeps its current exit 1 behavior.
  - `runCloudSync`: gate the OpenRouter flow (`HasWork() && openrouterGate(...)`), print the combined approve line after both plans when both digests exist, extend the `openrouter: refreshed …` line with `; removed N` / `; graduated N` only when > 0, trigger the route sync when `applied.openrouter.Removed > 0`.
  - `planOpenRouterFlow`: show the refresh advice only when `len(Matched) == 0 && len(Removals) == 0`.
- [ ] **Step 4: Run** the filter, then `go test ./...`, `go vet ./...`, `make check` → PASS.
- [ ] **Step 5: Commit:** `feat(wt): gate OpenRouter removals like ollama's; --approve-removals takes both digests`.

---

### Task 5: Docs, skill and changelog

**Files:**
- Modify: `wt/docs/wt-cloud-sync.md`, `wt/docs/wt-model.md`, `wt/docs/internals/cloud-sync.md`, `wt/.claude/skills/cloud-sync/SKILL.md`, `wt/CHANGELOG.md`, `wt/CLAUDE.md` (the "Exit codes 2 to 5 are the ollama flow's" bullet), `CLAUDE.md` (root `wt cloud-sync` command line and exit-code wording)

- [ ] **Step 1: Update `wt/docs/wt-cloud-sync.md`:** the openrouter section (a model absent from the list is removed after approval; listed-but-unpriced is not; the `MinListedForRemovals` outage rule; the replaced `No OpenRouter match` warning), a new "Models you add by hand" section (the `manual` lifecycle: set by `wt model add`, kept while off the source, graduates to `curated`, `wt model edit <id> --source manual` for existing entries), "Removals need approval" (both flows, per-flow digest, comma list, combined line), the flag table (`--approve-removals`, `--force` now both flows), and the exit-code table (4 and 5 either flow). Keep illustrative output labeled as such (guides never embed live state).
- [ ] **Step 2: Update `wt/docs/wt-model.md`** (`add` sets `source = "manual"` for covered cloud models; `edit --source manual|curated`, refused for uncovered models), `wt/docs/internals/cloud-sync.md` (new plan lists, `digestOf`, gate symmetry), the `cloud-sync` skill (digest step uses both flows' digests, the openrouter-flag restriction line, the exit table, a "manual entries" note), the two CLAUDE.md bullets, and `wt/CHANGELOG.md` with an entry that ends with the migration note: run `wt model edit <id> --source manual` on cloud models you added by hand **before** the next `wt cloud-sync`, or review the removal list.
- [ ] **Step 3: Verify:** `make -C .. check-links` (root) and `cd wt && make check && go test ./...` → PASS; `go build -o "$D/wt" ./cmd/wt` into a scratch dir and run `wt cloud-sync --dry-run` with `WT_REGISTRY`, `WT_LITELLM_CONFIG`, `WT_LITELLM_RESTART_CMD=true` redirected (see `wt/CLAUDE.md`) against a scratch registry holding a manual `ollama/beam:cloud` entry; the plan must show it under `Kept`.
- [ ] **Step 4: Commit:** `docs(wt): cloud-sync removals, manual models, and the second removal gate`.

---

## Self-review

- **Spec coverage:** `manual` source and add/edit (Task 1); protection, graduation, mass-removal exclusion, kept lines for ollama (Task 2) and OpenRouter (Task 3); OpenRouter removals with absent-only rule and outage floor (Task 3); one approval over both flows (Task 4: shared prompt already exists, per-flow digests, comma list, combined line); exit-code and flag changes, docs, skill, CHANGELOG migration note (Tasks 4 and 5); settled builder-check answers (digest from planned removals, apply-time re-plan comparison reuses existing `Format` equality, `edit --source` scope) are in Tasks 1, 3 and 4.
- **Calls made beyond the spec** (flag them to the reviewer): `MinListedForRemovals = 20`; a manual entry under an unpublished tag is kept without graduating; an unresolved-tag stem match protects but does not graduate; the digest stays per flow with a comma-separated `--approve-removals`; exit 4/5 are shared by both flows (2 and 3 stay ollama-only); `--source` exists on `edit` only.
- **Type consistency:** `Entry.Manual()`, `digestOf`, `PricePlan.{Removals,Kept,Graduations}`, `CatalogPlan.{Graduations,Kept}`, `PricesApplied.{Removed,Graduated}`, `CatalogApplied.Graduated`, `approvedDigests`, `openrouterGate` are defined once and used with the same names in later tasks.
