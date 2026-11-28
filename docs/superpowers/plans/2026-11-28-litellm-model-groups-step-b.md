# LiteLLM model groups, Step B: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `wt` writes LiteLLM's session-affinity `router_settings` itself (B1), and `wt-model alias apply <file>` plus `alias list --toml` make the six model groups reproducible from one file (B2).

**Architecture:** B1 adds an only-when-absent `router_settings` step beside `ensureCancelOnDisconnect` in `internal/litellm/configfile.go`'s `EnsureSettings`, a text line in the dry run, and two pinning tests. B2 adds a batch alias setter in `internal/modeladmin`, the `apply` command and a `--toml` printer in `cmd/wt-model/alias.go`. The two parts ship as two PRs: Tasks 1 to 3 (B1) on this branch first; Tasks 4 to 7 (B2) on a new worktree off the `main` that has B1.

**Tech Stack:** Go 1.26 (module `wt/`), `gopkg.in/yaml.v3` node trees (config.yaml), `internal/tomlw` (registry TOML), cobra, `go test`.

**Spec:** `docs/superpowers/specs/2026-11-28-litellm-model-groups-step-b-design.md` (parent: `2026-11-27-litellm-model-groups-design.md`).

## Global Constraints

- Values written by B1, verbatim: `router_settings.optional_pre_call_checks: [session_affinity]`, `deployment_affinity_ttl_seconds: 14400`, `session_affinity_repin_on_failover: true`. Each only when its key is absent or null; a present value (any value, `false` included) is the opt-out and is never changed; a present `optional_pre_call_checks` is never extended.
- `router_settings` absent or null: created as a mapping at the end of the file; a whole-value YAML alias or a root merge key: skipped with a sync warning. Warnings are said by `Sync` and the dry run only (`settingsWarnings`), never by start/stop jobs.
- Only `config.yaml` is read; nothing reads LiteLLM's database for settings. `EnsureSettings` does no I/O. A sync with nothing to change must not rewrite the file (`File.Changed()`); a config that already matches (the live Step A block) causes no write and no restart.
- The dry run's reason line is text only: **no new JSON field**, `docs/contracts/litellm-cli.sample.json` and its contract test do not change.
- `alias apply`: file holds `[[aliases]]` tables only (any other key or table refused; `notes` accepted and ignored); judged by the registry reader (`config.Alias.UnmarshalTOML`); all-or-nothing in **one** `config.UpdateRegistry` (apply pure, up to three runs) and **one** route sync (`syncAndWarn`); never removes an alias absent from the file; an id that is currently a redirect refuses the file; "no change" is judged by effective targets (the set of (model, effective order) pairs from `Config.AliasTargets`; omitted order = 1-based position) and an equal alias is not rewritten; `alias set`'s own change test is untouched. Exit codes as `alias set`: 0 done (also when routes could not be written; a warning says so), 1 plain error or refused file, 3 approval missing (shrink, no terminal or answered no; nothing changed). One shrink question for all aliases; `--accept-smaller-window` answers yes.
- `alias list --toml`: prints only aliases `apply` would accept, explicit `order` for every target, same order as `alias list`; redirects, malformed aliases and aliases failing `config.AliasFault` are skipped with one stderr note naming how many and why; exclusive with `--json`.
- `wt/CLAUDE.md`: every `Test*` has a top-level `//` comment saying what and why; tests stay off the developer's machine (scratch registries via `WT_REGISTRY`, LiteLLM paths named via `WT_LITELLM_CONFIG`/`litellm.Options.Path`); history under `docs/superpowers/**`, `docs/archive/**` and dated CHANGELOG entries is never edited; guides embed no live model state.
- Run Go as `go -C wt ...`. In this worktree use repo-rooted git pathspecs (`':/wt/...'`) when the shell's cwd is below the root. Gate: `make pre-pr` on an otherwise idle machine, run with `run_in_background`; if it fails only in `internal/modelclient` timing tests, rerun idle; never push on red. Commits end with `Co-Authored-By: Claude Code <noreply@anthropic.com>`.

## Review Focus

- `optional_pre_call_checks` present as a non-list (a scalar string) or null, and a `router_settings` that is itself null: a scalar is left alone, null is treated as absent (Task 1).
- A hand-written block with comments, an anchor or unusual key order survives a save and a sync byte-for-byte in meaning, and causes no write at all when nothing is missing (Task 1).
- `alias apply` with duplicate ids in the file, an empty file (no `[[aliases]]`), an unknown top-level key, and one bad alias among good ones: each refuses the whole file and changes nothing (Task 5).
- `alias apply` when the route sync cannot reach the database: the registry is still written, one warning, exit 0 (Task 5).
- `alias list --toml` then `alias apply` on a registry holding an alias set without explicit orders, one redirect and one faulty alias: "no change" for every printed alias, stderr note for the two skipped (Task 6).

---

## Part B1 (PR 1)

### Task 1: `wt` writes the affinity `router_settings`

**Files:**
- Modify: `wt/internal/litellm/configfile.go` (new helpers, `EnsureSettings`, `settingsWarnings`), `wt/internal/litellm/service.go` (`PlanSync`/`planSync` fills the new plan field before `EnsureSettings`), `wt/internal/litellm/report.go` (the dry-run text line)
- Test: `wt/internal/litellm/configfile_test.go`, `wt/internal/litellm/report_test.go` (or the file that tests `ReportSyncPlan`), and every settled-config fixture that fails (list below)

**Interfaces:**
- Produces (all in package `litellm`, unexported):
  - `const affinityTTLSeconds = 14400`
  - `func routerSettingsToAdd(root *yaml.Node) []string`: the key names `ensureRouterSettings` would write now, in the order `optional_pre_call_checks`, `deployment_affinity_ttl_seconds`, `session_affinity_repin_on_failover`; empty when nothing is missing or the block is skipped.
  - `func ensureRouterSettings(root *yaml.Node)`: writes exactly those keys (creating the block with `ensureMapping`).
  - `func routerSettingsSkipped(root *yaml.Node) string`: why the block is left alone (`"is a YAML alias"` / `"comes from a root merge key (<<)"`), else `""`; same shape as `cancelOnDisconnectSkipped`.
  - `SyncPlan.affinityKeys []string` (unexported field): `routerSettingsToAdd` of the document before `EnsureSettings`, set by the plan.
- Consumes: `mapGet`, `mergedGet`, `aliasValue`, `isNull`, `boolNode`, `ensureMapping`, `mapSet`, `isTrue` (all existing in `configfile.go`/`dburl.go`).

- [ ] **Step 1: Write the failing tests** (each with the what-and-why comment), building the document the way the neighbouring `EnsureSettings` tests do:
  - `TestEnsureSettingsAddsTheAffinityBlockToAFreshConfig`: no `router_settings` -> after `EnsureSettings` the block is the last top-level key and holds `optional_pre_call_checks: [session_affinity]`, `deployment_affinity_ttl_seconds: 14400`, `session_affinity_repin_on_failover: true`.
  - `TestEnsureSettingsLeavesTheStepABlockAlone`: the live Step A block (`optional_pre_call_checks: [session_affinity]`, ttl 14400, repin true, with a comment line) -> `Changed()` is false.
  - `TestEnsureSettingsFillsOnlyTheMissingAffinityKeys`: a block with only the list -> the other two keys added, the list untouched.
  - `TestEnsureSettingsKeepsAnAffinityOptOut`: `session_affinity_repin_on_failover: false` and `deployment_affinity_ttl_seconds: 3600` stay as written.
  - `TestEnsureSettingsNeverExtendsTheChecksList`: `optional_pre_call_checks: [prompt_caching]` stays exactly that, and `settingsWarnings()` then contains one line saying session affinity is off because the list has no `session_affinity`.
  - `TestEnsureSettingsAffinityNullAndScalarCases`: `router_settings:` (null) becomes a mapping with the three keys; `optional_pre_call_checks:` (null) becomes the list; `optional_pre_call_checks: session_affinity` (a scalar) is left alone.
  - `TestEnsureSettingsSkipsAnAliasedOrMergedRouterSettings`: a `router_settings` that is a YAML alias, and one from a root merge key, are not written and `settingsWarnings()` says why (text names `router_settings` and `session affinity`).
  - `TestSaveKeepsAHandWrittenRouterSettingsMeaning`: a block with comments, an anchor and an extra unrelated key (`num_retries: 2`) survives `EnsureSettings` + `Save` + reopen with every key and value intact and nothing added.
  - `TestSyncKeepsAHandWrittenRouterSettings` (service level, using the harness the sync tests use): a config holding the Step A block and nothing else to change is not rewritten (file bytes and mtime unchanged, no restart requested).
  - `TestPlanSyncSaysWhyItRestartsForAffinity`: a plan whose `affinityKeys` is non-empty prints one line `config.yaml: would add router_settings.<keys> (session affinity); the proxy would restart once` (text rendering); the JSON rendering is identical to before (the existing JSON contract test stays green).
- [ ] **Step 2: Run them to verify they fail**
  Run: `go -C wt test ./internal/litellm -run 'Affinity|RouterSettings|StepABlock' -count=1`
  Expected: FAIL (undefined helpers, or assertions failing).
- [ ] **Step 3: Implement** the Interfaces block. `EnsureSettings` calls `ensureRouterSettings(root)` after `ensureStoreModelInDB(root)`. The three rules, per the Global Constraints: block absent/null -> `ensureMapping`; per key absent/null -> write; a present non-null value (a scalar `optional_pre_call_checks` too) -> untouched. `settingsWarnings` appends the skipped-block line and the affinity-off line (only when the block is a real mapping whose `optional_pre_call_checks` is a list without `session_affinity`). In `PlanSync`'s plan path set `plan.affinityKeys = routerSettingsToAdd(f.root())` before `f.EnsureSettings()`; in `ReportSyncPlan`'s text branch print the line after the existing `EnableDB`/`Move` switch; do not touch `SyncPlanJSON`.
- [ ] **Step 4: Make the settled fixtures carry the block.** Run `go -C wt test ./internal/litellm ./cmd/wt-model ./internal/lifecycle -count=1` and fix every failing "settled config needs no change" fixture by adding the Step A block (the three keys) to the shared constants first (`settledSettings` in `service_db_test.go`, `preStoreSettings` in `migrate_db_test.go`) and then to each inline fixture the run lists (known sites: `service_test.go`, `discovered_test.go`, `configfile_test.go`, `ollama_base_test.go`, `proxyenv_test.go`, `cmd/wt-model/litellm_sync_test.go`, `litellm_db_test.go`, `alias_test.go`, `internal/lifecycle/{routes_db,confirm,wrappers}_test.go`; `go test` is the authoritative list). A fixture that asserts a restart *because of* a missing setting keeps its expectation; one that only contains the settings strings gets the block.
  Expected at the end: those three packages PASS.
- [ ] **Step 5: Commit** (`feat(wt): wt writes the session-affinity router_settings`).

### Task 2: pin that the gateway forwards session headers

**Files:**
- Test: `wt/internal/modelgateway/gateway_test.go`

**Interfaces:** Consumes the existing test harness in that file (the one `TestGatewaySwapsCredentials` uses). Produces nothing.

- [ ] **Step 1: Write `TestGatewayForwardsSessionHeadersUnchanged`** (what and why in the comment: LiteLLM's session affinity reads these headers, the gateway has only a credential deny list, and nothing pinned that they pass): send a request with `x-claude-code-session-id`, `x-litellm-session-id`, `x-session-id`, `session-id`, `thread-id` and `User-Agent` set to distinct values plus the client's wt token; assert the upstream sees each header value unchanged, and that `Authorization` is the swapped LiteLLM key (not the client's token).
- [ ] **Step 2: Run it**
  Run: `go -C wt test ./internal/modelgateway -run TestGatewayForwardsSessionHeadersUnchanged -count=1`
  Expected: PASS at once (it pins existing behavior). Then prove it can fail: temporarily add `session-id` to the gateway's deleted-header list, rerun, expect FAIL, revert.
- [ ] **Step 3: Commit** (`test(wt): pin that the gateway forwards session-id headers`).

### Task 3: B1 docs, gate and PR

**Files:**
- Modify: `docs/guides/04-litellm-config.md` (section 7: the "Session affinity" paragraph that says wt does not write the setting yet), `wt/CLAUDE.md` ("LiteLLM routes", the `EnsureSettings` bullet), `wt/docs/internals/litellm-routes.md`, `wt/CHANGELOG.md` (Unreleased)

**Interfaces:** Consumes Task 1's behavior; produces docs.

- [ ] **Step 1: Edit the four places** to state: `wt` writes the three keys into `router_settings` when absent (values as in Global Constraints); a hand-written value is the opt-out; a list is never extended and, if it lacks `session_affinity`, a sync warns that affinity is off; an aliased or merged block is skipped with a warning; a key in `config.yaml` shadows a value kept in LiteLLM's database, so an opt-out must be written in the file; the first write on a machine without the keys restarts the proxy once, and a machine that already has them sees no change; the TTL slides and a pin moves to the fallback only while the original deployment is in cooldown (so a session that failed over stays on the fallback up to the TTL); whether pins survive a proxy restart is not known yet. Guide 04 keeps its no-live-state rule.
- [ ] **Step 2: Check**
  Run: `bin/check-links`
  Expected: `ALL LINKS OK`.
- [ ] **Step 3: Gate** Run `make pre-pr > <log> 2>&1` from the worktree root with `run_in_background`, nothing else running. Expected: `pre-pr: all green`.
- [ ] **Step 4: Commit** (`docs(wt): wt owns the affinity router_settings`), then push and open the PR (body: what, the opt-out rules, the one-restart note, "Refs #751").

---

## Part B2 (PR 2, a new worktree off `main` after PR 1 merges)

### Task 4: `modeladmin.SetAliases`

**Files:**
- Modify: `wt/internal/modeladmin/alias.go`
- Test: `wt/internal/modeladmin/alias_test.go`

**Interfaces:**
- Consumes: `AliasRequest`, `CheckAlias`, `SetAlias`'s body (`config.UpdateRegistry`, `RegistryDoc.AddAlias`, `SetAliasTargets`, `Aliases`, `RefConfig`), `Config.AliasTargets`.
- Produces: `func SetAliases(reqs []AliasRequest) (changed []string, err error)`: one `config.UpdateRegistry` for all requests; each request is checked (`CheckAlias`, then `Guard`) against `d.RefConfig()` inside the write; a request whose effective targets already equal the registry's alias (same set of (model, effective order) pairs, omitted order = 1-based position, list order among equal orders ignored) is skipped without touching the file; any failure refuses all and changes nothing; `changed` lists the ids written, in file order. Also `func AliasTargetsEqual(cfg *config.Config, id string, targets []config.AliasTarget) bool` (the comparison, exported for the printer test and the dry run).

- [ ] **Step 1: Write the failing tests** (`TestSetAliases...`, each with a what-and-why comment), on a scratch registry (`WT_REGISTRY` via the package's existing test helper):
  - `TestSetAliasesWritesAllInOneUpdate`: three new aliases -> all three in the registry, `changed` has three ids, one write.
  - `TestSetAliasesRefusesAllWhenOneFails`: the second alias names a missing model -> error naming it, the registry byte-identical to before.
  - `TestSetAliasesSkipsAnAliasWhoseEffectiveTargetsMatch`: a registry alias written as `[{model="x"},{model="y"}]` and a request `[{x,1},{y,2}]` -> not in `changed`, registry bytes unchanged (spelling kept); a request with a different order for `y` is in `changed`.
  - `TestSetAliasesIgnoresListOrderAmongEqualOrders`: `[{x,1},{y,1}]` equals `[{y,1},{x,1}]`.
  - `TestSetAliasesRefusesARedirect`: a request for an id that is currently a redirect refuses the whole call.
  - `TestSetAliasesRefusesDuplicateIDsInTheRequests`.
  - `TestSetAliasesGuardRunsInsideTheWrite`: a `Guard` that errors for one request refuses all and writes nothing.
- [ ] **Step 2: Run them to verify they fail**
  Run: `go -C wt test ./internal/modeladmin -run 'SetAliases|AliasTargetsEqual' -count=1`
  Expected: FAIL (undefined).
- [ ] **Step 3: Implement** `AliasTargetsEqual` and `SetAliases` per the Interfaces block (reuse `CheckAlias`; a duplicate id or a redirect error is a plain error naming the id).
- [ ] **Step 4: Run the package**
  Run: `go -C wt test ./internal/modeladmin -count=1`
  Expected: PASS.
- [ ] **Step 5: Commit** (`feat(wt): modeladmin.SetAliases writes several aliases in one registry update`).

### Task 5: `wt-model alias apply`

**Files:**
- Modify: `wt/cmd/wt-model/alias.go` (register `aliasApplyCmd` in `aliasCmd`; `runAliasApply`; `parseAliasFile`)
- Create: `docs/contracts/alias-apply.sample.toml` (generic ids: `alias/example-pro` over two placeholder models, one with and one without explicit orders)
- Test: `wt/cmd/wt-model/alias_apply_test.go` (new), plus the contract test that parses the sample

**Interfaces:**
- Consumes: `modeladmin.SetAliases`, `modeladmin.AliasWindowChange`, `modeladmin.CheckAlias`, `h.hostConfig()`, `h.confirmRemove`, `h.syncAndWarn`, `h.printAliasDryRun`, `aliasShrinkGuard`, `h.tiersIndex()`, `cli.NoTerminalRefusal`, `cli.Declined`, `tomlw.Decode`, `config.Alias.UnmarshalTOML`.
- Produces: `func parseAliasFile(data []byte) ([]modeladmin.AliasRequest, error)` (refuses: invalid TOML, any top-level key other than `aliases`, a non-table `aliases` entry, an entry the reader judges malformed, duplicate ids, zero aliases); `func (h *host) runAliasApply(ctx context.Context, out, errOut io.Writer, rec servestate.Store, path string, acceptSmaller, dryRun bool) error`; the command `apply <file>` with flags `--dry-run`, `--accept-smaller-window`.

- [ ] **Step 1: Write the failing tests** (`TestAliasApply...`, what-and-why comments; use the package's `testHost()`/`rootCmdWith` seams and a scratch registry with the needed models registered):
  - `CreatesAllFromAFile`, `IsIdempotent` (second run prints `no change:` per alias and runs no sync), `DryRunChangesNothing` (registry bytes and sync-call count unchanged, each alias's dry-run block printed), `SyncsOnce` (a fake sync counter is 1 for three changed aliases), `RefusesAnUnknownTopLevelKey`, `RefusesDuplicateIDsInTheFile`, `RefusesAnEmptyFile` (no `[[aliases]]`: exit 1, text names the file), `RefusesTheWholeFileOnOneBadAlias` (registry unchanged), `RefusesARedirectID`, `IgnoresNotes`, `AsksOnceForSeveralShrinks` (two aliases shrink -> one question listing both; no terminal -> exit 3 and nothing changed), `AcceptSmallerWindowAnswersYes`, `RoutesUnavailableStillWritesTheRegistry` (the fake sync returns the "database cannot be reached" warning: registry written, warning printed, exit 0).
  - `TestAliasApplySampleFileParses`: `docs/contracts/alias-apply.sample.toml` parses through `parseAliasFile` and applies cleanly to a scratch registry holding its placeholder models.
- [ ] **Step 2: Run them to verify they fail**
  Run: `go -C wt test ./cmd/wt-model -run 'AliasApply' -count=1`
  Expected: FAIL (command and helpers undefined).
- [ ] **Step 3: Implement** per Interfaces and the Global Constraints. Flow of `runAliasApply`: read the file, `parseAliasFile`, `hostConfig`, `CheckAlias` for every request (collect and refuse the file at the first failure), compute each request's `AliasWindowChange` and which shrink; `--dry-run` prints `printAliasDryRun` per alias (or `no change:` for an equal one) and returns; otherwise one shrink question if any shrink and not `--accept-smaller-window` (words from `ShrinkSentence`, one per shrinking alias); set `Guard` per request with `aliasShrinkGuard`; call `modeladmin.SetAliases`; print `set alias: X` / `no change: X`; if any changed, `h.syncAndWarn(out, errOut, nil)` once.
- [ ] **Step 4: Run the package**
  Run: `go -C wt test ./cmd/wt-model -count=1`
  Expected: PASS.
- [ ] **Step 5: Commit** (`feat(wt): wt-model alias apply creates several aliases from one file`).

### Task 6: `wt-model alias list --toml`

**Files:**
- Modify: `wt/cmd/wt-model/alias.go` (`aliasListCmd` flag, `runAliasList`), `wt/internal/modeladmin/alias.go` (the printer)
- Test: `wt/cmd/wt-model/alias_test.go` (or the file holding the `alias list` tests), `wt/internal/modeladmin/alias_test.go`

**Interfaces:**
- Consumes: `Config.Aliases`, `Config.AliasTargets`, `config.AliasFault`, `tomlw.NewTable`/`tomlw.Encode`, `AliasTargetsEqual` (Task 4).
- Produces: `func AliasesTOML(cfg *config.Config) (text string, skipped []SkippedAlias, err error)` where `type SkippedAlias struct{ ID, Why string }`; the `--toml` flag on `alias list` (exclusive with `--json`; skipped aliases counted in one stderr note).

- [ ] **Step 1: Write the failing tests:**
  - `TestAliasesTOMLPrintsExplicitOrders`: an alias set without orders prints `order = 1`, `order = 2` per target.
  - `TestAliasesTOMLSkipsRedirectsMalformedAndFaulty`: skipped list names each with its reason; printed text holds only the good aliases.
  - `TestAliasListTOMLRoundTripsThroughApply`: registry with an alias without explicit orders, a redirect and a faulty alias -> `alias list --toml` output fed to `alias apply` reports `no change:` for every printed alias and exit 0; stderr of the list names two skipped.
  - `TestAliasListTOMLIsExclusiveWithJSON`: both flags -> exit 1 with a usage-style error.
  - `TestAliasListTOMLWithNoAliasesPrintsNothing`.
- [ ] **Step 2: Run them to verify they fail**
  Run: `go -C wt test ./internal/modeladmin ./cmd/wt-model -run 'AliasesTOML|AliasListTOML' -count=1`
  Expected: FAIL.
- [ ] **Step 3: Implement** `AliasesTOML` (alias order as `alias list`; each alias a table with `id` and `targets` as inline tables with `model` and an explicit `order` from `AliasTargets`; encode through `tomlw.Encode` so the output is `[[aliases]]` tables) and wire the flag.
- [ ] **Step 4: Run both packages** `go -C wt test ./internal/modeladmin ./cmd/wt-model -count=1`. Expected: PASS.
- [ ] **Step 5: Commit** (`feat(wt): wt-model alias list --toml prints aliases in the shape apply reads`).

### Task 7: B2 docs, gate and PR

**Files:**
- Modify: `wt/docs/wt-model.md` (the alias section: `apply` and `--toml`; also fix its stale line "at most one target is local", false since #690), `docs/guides/04-litellm-config.md` (section 7: a fresh-machine sequence: register models with `wt-model model add` / `wt-model cloud-sync`, then `wt-model alias apply <file>`, then `wt profile slots add` for `claude-fallback`, then the check commands; the windows caveat; the file captured with `alias list --toml`), `wt/CLAUDE.md` and root `CLAUDE.md` (the alias command lists), `wt/CHANGELOG.md` (Unreleased)

- [ ] **Step 1: Edit** the places above (no live model counts; example ids generic).
- [ ] **Step 2: Check** `bin/check-links` -> `ALL LINKS OK`; `grep -rn "at most one target is local" wt docs` -> no hit outside history.
- [ ] **Step 3: Gate** `make pre-pr` on an idle machine -> `pre-pr: all green`.
- [ ] **Step 4: Commit** (`docs(wt): alias apply and the fresh-machine sequence`), push, open PR 2 (body: what, the semantics of "no change", no prune, "Refs #751").

---

## Self-review

- Spec coverage: B1 keys, opt-out, no-list-extension, skip-with-warning, no I/O, text-only dry-run line, sync-keeps test, gateway test, docs (Tasks 1 to 3); B2 apply semantics, effective "no change", one write and one sync, shrink question, `--toml` round trip with skips, sample fixture, docs (Tasks 4 to 7). Non-goals (no header, no prune, no model creation, no pi) appear only in the docs wording.
- Names are used consistently: `routerSettingsToAdd`, `ensureRouterSettings`, `routerSettingsSkipped`, `SyncPlan.affinityKeys`, `SetAliases`, `AliasTargetsEqual`, `parseAliasFile`, `runAliasApply`, `AliasesTOML`, `SkippedAlias`.
- Proportion: seven tasks for two PRs; code appears only where a signature or an exact assertion fixes a choice.
