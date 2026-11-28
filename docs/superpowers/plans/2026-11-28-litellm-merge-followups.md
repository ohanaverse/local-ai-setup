# LiteLLM config reads: the three PR #759 follow-ups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `wt`'s reads of `router_settings` and `general_settings` in `config.yaml` match LiteLLM's YAML loader (PyYAML) for a merge that aliases a list (#760), a literal block with repeated `<<` keys (#761) and a literal block that is not a mapping (#762), and no write lands in a block wt cannot read.

**Architecture:** One shared `mergeSources` helper makes `mergedGetDepth` and `mergeAmbiguous` resolve a merge value's alias before testing for a sequence (#760). The two skip predicates (`cancelOnDisconnectSkipped`, `routerSettingsSkipped`) gain two literal-block cases, "repeated merge keys" and "not a mapping" (#761, #762), so the existing skip warning, the write guards and `dbRoutesOffWarning` all follow. The now-dead `general_settings is not a mapping` branch of `dbRoutesOffWarning` is deleted. All code is in `wt/internal/litellm/configfile.go`; no caller changes.

**Tech Stack:** Go 1.26 (module `wt/`), `gopkg.in/yaml.v3` node trees, `go test`.

**Spec:** `docs/superpowers/specs/2026-11-28-litellm-merge-followups-design.md` (sibling: `2026-11-28-litellm-model-groups-step-b-design.md`).

## Global Constraints

- One PR for #760, #761 and #762, in a fresh worktree off current `main` under `.worktrees/` or `.claude/worktrees/` (for example `wt -W litellm-merge-followups`); never create a branch or commit in the primary checkout.
- Go commands run from `wt/` (`go -C wt test ./internal/litellm ...`); in the worktree use repo-rooted git pathspecs (`':/wt/...'`) when the shell's cwd is below the root.
- GitHub CI is disabled: the gate is `make pre-pr` (about 5 minutes), run with `run_in_background`, exit read from the log; never push on red; a failure only in `internal/modelclient` timing tests is load, so rerun idle.
- Commit messages end with the line `Co-Authored-By: Claude Code <noreply@anthropic.com>`; the PR body ends with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
- PR body: `Fixes #760`, `Fixes #761`, `Fixes #762` (meant to close) and `Refs #758` (no closing keyword followed by `#758` anywhere); write it to a file and pass `gh pr create --body-file` (the worktree guard refuses commands whose prose quotes git mutations).
- Contract-covered shapes (`docs/contracts` fixtures) change in the same commit as the code: this change touches none (no JSON field, no registry key); `docs/contracts/litellm-cli.sample.json` stays as is.
- No change to which keys wt writes, to the no-warning-when-keys-present rule from #759, or to `store_model_in_db` beyond what `cancelOnDisconnectSkipped` already gives it. Never overwrite a value a merge supplies; never claim affinity is off when wt cannot tell.
- Skip-warning wording is unchanged except the reason: the new reasons are exactly `is read through repeated merge keys (<<)` and `is not a mapping`; the existing two stay `is a YAML alias` and `comes from a root merge key (<<)`. Switch order in both predicates: root merge, whole-value alias, not a mapping, repeated merge keys (an alias is also non-mapping, so it goes first).
- `wt/CLAUDE.md`: every `Test*` has a top-level `//` comment saying what it tests and why; tests assert on unexported functions directly; test configs go through `writeConfig`/`opts` (scratch paths, never the real `config.yaml`).
- Docs: guides embed no live model state; home paths are written `/Users/user_name`; `make check-links` must pass when docs change; dated history under `docs/archive/**` and old plans is never edited (the spec names the one sibling spec bullet to amend).
- Two machines work this repo: before pushing, `git fetch origin` and re-check that the branch and the issues are still as expected; never force-push.

## Review Focus

Spec-implied inputs no task's headline tests would otherwise cover, most likely to bite first; each has its test in the task named.

- A merge list whose element is a dangling alias (anchor gone) or a self-referential list (`l: &l [*l]`) must read as "key absent" and "not ambiguous", never panic or loop (Task 1).
- The root mapping has repeated `<<` keys but `router_settings` is a plain literal block: it is not read through the root's merges, so it is filled and draws no warning; `literalBlockAmbiguous` must not consult the root (Task 2).
- A literal block with exactly one `<<` whose list sources overlap or are disjoint stays readable: filled where a key is missing, no warning (Task 2).
- A literal repeated-merge `general_settings` also blocks `store_model_in_db` (the shared predicate) and `dbRoutesOffWarning` names the new reason; every key written explicitly still warns, accepted as the false alarm the spec chose (Task 2).
- `router_settings: ""` (an empty string, not null) is a non-mapping and warns, while `router_settings:` (null) and `{}` stay silent and are filled; `PlanSync` and `Sync` print the same new warnings (Task 3).

---

### Task 1: #760, a merge that aliases a list is followed

**Files:**
- Modify: `wt/internal/litellm/configfile.go` (`mergedGetDepth` at 475-497, `mergeAmbiguous` at 819-846, new `mergeSources` beside `mergedGet`; update the doc comments of `mergedGet` and `mergeAmbiguous`)
- Test: `wt/internal/litellm/configfile_test.go`, `wt/internal/litellm/service_test.go`

**Interfaces:**
- Consumes: `aliasValue(n *yaml.Node) *yaml.Node` (`dburl.go`), `mapGet`, `mergeDepth`, `routerSettingsToAdd`, `settingsWarnings`.
- Produces: `func mergeSources(v *yaml.Node) []*yaml.Node` in `configfile.go`: v resolved with `aliasValue`; when the result is a sequence, its elements each resolved with `aliasValue`, else a one-element slice of the result. Elements are returned as they are (a dangling alias comes back as the alias node; callers ignore anything that is not a mapping). Used by `mergedGetDepth` and `mergeAmbiguous`; no other signature changes.

Note for every test here: `yaml.v3` decodes a node tree fine but `decode(t, n)` / `routerSettingsOf` fail on `<<: *list` ("map merge requires map or sequence of maps"), so assert with `mapGet`, `mergedGet`, `routerSettingsToAdd`, `enc` and `settingsWarnings`, never with `decode`.

- [ ] **Step 1: Write the failing tests** (config bodies are Go string literals opened with `Open(writeConfig(t, body))`):
  - `TestMergedGetFollowsAMergeThatAliasesAList`. Body `"lst: &lst [{x: 1}, {y: 2, x: 9}]\ninner: &inner {z: 3}\nlst2: &lst2 [*inner]\nr: {<<: *lst}\ns: {<<: *lst2}\nu: {<<: [{y: 2}]}\n"`. Assert `mergedGet(mapGet(root,"r"), "x").Value == "1"` (first source wins), `"y"` gives `"2"`, `"nope"` gives nil; `mergedGet(s, "z").Value == "3"` (alias inside the aliased list); control `mergedGet(u, "y").Value == "2"` (inline list, worked before). Subtests with a hand-broken or looping list (the Review Focus line): "dangling element alias" sets `mapGet(root,"lst2").Content[0].Alias = nil` after `Open` and asserts `mergedGet(s, "z") == nil` and `mergeAmbiguous(s, mergeDepth) == false`; "self-referential list" opens `"l: &l [*l]\nq: {<<: *l}\nr: &r {<<: *r}\n"` and asserts `mergedGet(q, "x") == nil`, `mergedGet(r, "x") == nil`, both `mergeAmbiguous` false, no panic.
  - `TestMergeAmbiguousSeesInsideAnAliasedList`. Body `"a: &a {x: 1}\nb: &b {x: 2}\nm: &m {<<: *a, <<: *b}\nlst: &lst [*m]\nr: {<<: *lst}\nlst2: &lst2 [*a, *b]\nok: {<<: *lst2}\n"`. Assert `mergeAmbiguous(mapGet(root,"r"), mergeDepth)` is true (the aliased list holds a mapping with two merges) and `mergeAmbiguous(mapGet(root,"ok"), mergeDepth)` is false (one `<<` with a list of sources is not ambiguous).
  - `TestEnsureSettingsReadsAnAliasedMergeListInBothBlocks`: table `name -> {body, rest}` where the config is `body + "model_list: []\n" + rest`, with `settledWithoutAffinity` as rest for router cases and `litellmSettingsDone + affinitySettings` for general cases. Cases: `"router_settings block merge"`: `"o: &o [{optional_pre_call_checks: [session_affinity], deployment_affinity_ttl_seconds: 14400}, {session_affinity_repin_on_failover: false}]\nrouter_settings:\n  <<: *o\n"`; `"router_settings root merge"`: `"o: &o [{router_settings: {optional_pre_call_checks: [session_affinity], deployment_affinity_ttl_seconds: 14400, session_affinity_repin_on_failover: false}}]\n<<: *o\n"`; `"general_settings block merge"`: `"o: &o [{cancel_on_disconnect: false}, {store_model_in_db: true}]\ngeneral_settings:\n  <<: *o\n"`; `"general_settings root merge"`: `"o: &o [{general_settings: {cancel_on_disconnect: false, store_model_in_db: true}}]\n<<: *o\n"`. Each asserts `f.settingsWarnings()` is empty and `f.EnsureSettings()` leaves `f.Changed()` false (before the fix the root-merge cases write a literal block over the list's, the block-merge cases write the keys again).
  - `TestEnsureSettingsDoesNotWriteOverAnOptOutAnAliasedListSupplies` (the #760 scenario). Three subtests on `"overrides: &o\n  - {session_affinity_repin_on_failover: false}\nrouter_settings:\n  <<: *o\nmodel_list: []\n" + settledWithoutAffinity`: this body ("aliased list") and the control with the list written inline (`router_settings:\n  <<: [{session_affinity_repin_on_failover: false}]\n`) both assert `routerSettingsToAdd(f.root())` equals `[]string{"optional_pre_call_checks", "deployment_affinity_ttl_seconds"}`, that after `EnsureSettings` `mapGet(mapGet(f.root(), "router_settings"), "session_affinity_repin_on_failover")` is nil and `enc(t, f)` does not contain `session_affinity_repin_on_failover: true`; the third ("list without the key", `overrides: &o\n  - {num_retries: 2}`) asserts all three keys are in `routerSettingsToAdd`.
  - In `service_test.go`, `TestSyncDoesNotWriteOverAnOptOutFromAnAliasedList`: `o, restarts, p := opts(t, <the aliased-list body above>)`; `PlanSync(&config.Config{}, nil, o)` has `plan.affinityKeys` equal to the two keys and `plan.Restart` true; `Sync` gives `res.Changed`, `*restarts == 1`, and the file at `p` contains `optional_pre_call_checks: [session_affinity]`, `deployment_affinity_ttl_seconds: 14400` and `session_affinity_repin_on_failover: false` but not `session_affinity_repin_on_failover: true`. (`Sync` decodes rows only, not the whole document, so the aliased list does not trip it; checked against the current code.)

- [ ] **Step 2: Run them to verify they fail**

Run: `go -C wt test ./internal/litellm -run 'TestMergedGetFollowsAMergeThatAliasesAList|TestMergeAmbiguousSeesInsideAnAliasedList|TestEnsureSettingsReadsAnAliasedMergeListInBothBlocks|TestEnsureSettingsDoesNotWriteOverAnOptOutAnAliasedListSupplies|TestSyncDoesNotWriteOverAnOptOutFromAnAliasedList' -count=1`
Expected: FAIL (the aliased-list reads return nil/false, so `mergedGet(r, "x")` is nil, `mergeAmbiguous` is false for `r`, the four `TestEnsureSettingsReadsAnAliasedMergeListInBothBlocks` cases report `Changed()` true, and the scenario and `Sync` tests write `session_affinity_repin_on_failover: true` over the aliased `false`). These already pass and are controls: the inline-list subtest, the "list without the key" subtest, the dangling-alias and self-referential subtests.

- [ ] **Step 3: Implement `mergeSources(v *yaml.Node) []*yaml.Node` and use it** in `mergedGetDepth` (replace the `sources := ...` block and the per-source `AliasNode` unwrap; recurse on each returned source) and in `mergeAmbiguous` (replace its `sources` block). Keep the `depth == 0` and `merges > 1` logic as it is. Update both doc comments to say a merge value that aliases a list is followed like an inline one.

- [ ] **Step 4: Run the new tests, then the package**

Run: `go -C wt test ./internal/litellm -run 'TestMergedGet|TestMergeAmbiguous|TestEnsureSettings|TestSync' -count=1` then `go -C wt test ./internal/litellm -count=1`
Expected: PASS (the whole package, including the `subscription_*`, `dburl_test.go` and ollama `api_base` tests that read through `mergedGet`).

- [ ] **Step 5: Commit**

```bash
git add wt/internal/litellm/configfile.go wt/internal/litellm/configfile_test.go wt/internal/litellm/service_test.go
git commit -m "fix(wt): follow a YAML merge that aliases a list of mappings (#760)"   # body ends with the Co-Authored-By line
```

---

### Task 2: #761, a literal block with repeated `<<` is unreadable

**Files:**
- Modify: `wt/internal/litellm/configfile.go` (`cancelOnDisconnectSkipped` at 704, `routerSettingsSkipped` at 746, new `literalBlockAmbiguous`; fix the `sharedBlockAmbiguous` comment that says literal blocks do not ask it)
- Test: `wt/internal/litellm/configfile_test.go`

**Interfaces:**
- Consumes: `mergeAmbiguous(m *yaml.Node, depth int) bool` and `mergeSources` (Task 1), `mapGet`, `mergeDepth`.
- Produces: `const reasonRepeatedMerge = "is read through repeated merge keys (<<)"`; `func literalBlockAmbiguous(root *yaml.Node, block string) bool` (true when `mapGet(root, block)` is a mapping node, not an alias, that `mergeAmbiguous` reports; the root's own merges are not consulted). Both predicates return `reasonRepeatedMerge` after the alias case. Test-side: `const whyRepeated = "is read through repeated merge keys (<<)"` beside `whyAlias`/`whyMerge` (a deliberate literal copy, like them).

- [ ] **Step 1: Write the failing tests**
  - Extend the `TestSkipWarningsFailClosedOnRepeatedMergeKeys` table. Add an `explicit` string to each `b` entry (router: `"  optional_pre_call_checks: [session_affinity]\n  deployment_affinity_ttl_seconds: 14400\n  session_affinity_repin_on_failover: true\n"`, general: `"  cancel_on_disconnect: true\n  store_model_in_db: true\n"`) and four cases, all with `why = whyRepeated`, where `K` is `b.key`: `"literal: full then partial"` `"a: &a "+b.full+"\nb: &b "+b.partial+"\n"+K+":\n  <<: *a\n  <<: *b\n"`; `"literal: partial then full"` (merges swapped); `"literal: source with two merges"` `"a: ...\nb: ...\nc: &c {<<: *a, <<: *b}\n"+K+":\n  <<: *c\n"`; `"literal: every key written explicitly"` `"a: &a "+b.full+"\nb: &b "+b.partial+"\n"+K+":\n  <<: *a\n  <<: *b\n"+b.explicit` (the accepted block-level false alarm). The existing assertions (`settingsWarnings` equals `[]string{b.line(why)}`, `EnsureSettings` changes nothing) apply unchanged.
  - `TestEnsureSettingsNeverWritesOverAValueASecondMergeSupplies`: the real #761 write. Router body `"a: &a {session_affinity_repin_on_failover: null}\nb: &b {session_affinity_repin_on_failover: false}\nrouter_settings:\n  <<: *a\n  <<: *b\nmodel_list: []\n" + settledWithoutAffinity`: `routerSettingsToAdd` is empty and `EnsureSettings` leaves `Changed()` false. General body `"a: &a {cancel_on_disconnect: null}\nb: &b {cancel_on_disconnect: false}\ngeneral_settings:\n  <<: *a\n  <<: *b\nmodel_list: []\n" + litellmSettingsDone + affinitySettings`: `EnsureSettings` leaves `Changed()` false (so `store_model_in_db` is not written either) and `f.dbRoutesOffWarning()` equals `"general_settings is read through repeated merge keys (<<), so wt did not write store_model_in_db: true; database routes are off and local routes stay in config.yaml with a restart per change"`.
  - `TestRepeatedMergeBlockMakesNoAffinityOffClaimInEitherOrder`: `"k: &k {optional_pre_call_checks: [prompt_caching], deployment_affinity_ttl_seconds: 14400, session_affinity_repin_on_failover: true}\nn: &n {optional_pre_call_checks: [session_affinity]}\nrouter_settings:\n  <<: *k\n  <<: *n\n"` and the same with the merges swapped, each `+ "model_list: []\n" + settledWithoutAffinity`: `settingsWarnings` equals `[]string{affinitySkipLine(whyRepeated)}` (no `affinityOffLine`; mergedGet would claim "off" in one order and stay silent in the other).
  - `TestAReadableLiteralBlockIsStillFilled` (Review Focus controls). Subtests: `"one merge with a list of sources"` router `"a: &a {optional_pre_call_checks: [session_affinity]}\nb: &b {deployment_affinity_ttl_seconds: 14400}\nrouter_settings:\n  <<: [*a, *b]\n"` gives `routerSettingsToAdd` equal to `[]string{"session_affinity_repin_on_failover"}` and no warnings; `"root has repeated merges, the block is plain"` router `"a: &a {x: 1}\nb: &b {y: 2}\n<<: *a\n<<: *b\nrouter_settings: {num_retries: 2}\n"` gives all three keys in `routerSettingsToAdd` and no warnings; `"one merge, overlapping sources"` router `"a: &a {optional_pre_call_checks: [session_affinity], deployment_affinity_ttl_seconds: 14400}\nb: &b {deployment_affinity_ttl_seconds: 3600}\nrouter_settings:\n  <<: [*a, *b]\n"` gives `[]string{"session_affinity_repin_on_failover"}` and no warnings; general `"a: &a {cancel_on_disconnect: false}\nb: &b {database_url: postgresql://u@localhost/db}\ngeneral_settings:\n  <<: [*a, *b]\n"` gives no warnings, and after `EnsureSettings` `mapGet(gs, "cancel_on_disconnect")` is nil (the merge still supplies `false`) while `mapGet(gs, "store_model_in_db")` is the literal `true`.
  - `TestLiteralBlockAmbiguous`: `literalBlockAmbiguous(f.root(), "router_settings")` is true for a literal `{<<: *a, <<: *b}`, false for one `<<`, for a null block, an absent key, a scalar, and a whole-value alias to an ambiguous mapping (the alias is the shared-block case).

- [ ] **Step 2: Run them to verify they fail**

Run: `go -C wt test ./internal/litellm -run 'TestSkipWarningsFailClosedOnRepeatedMergeKeys|TestEnsureSettingsNeverWritesOverAValueASecondMergeSupplies|TestRepeatedMergeBlockMakesNoAffinityOffClaimInEitherOrder|TestAReadableLiteralBlockIsStillFilled|TestLiteralBlockAmbiguous' -count=1`
Expected: the package does not build (`literalBlockAmbiguous` undefined in `TestLiteralBlockAmbiguous`); with a stub returning false the others fail as follows. Every new "literal:" table case fails (no warning at all today, since `mergedGet` finds every key through the first source); `TestEnsureSettingsNeverWritesOverAValueASecondMergeSupplies` fails (`Changed()` true: wt writes `true` over the null the first merge supplies, and `dbRoutesOffWarning` is empty); `TestRepeatedMergeBlockMakesNoAffinityOffClaimInEitherOrder` fails in both orders (the first order draws the false affinity-off claim, the second no warning). `TestAReadableLiteralBlockIsStillFilled` passes before and after: it is the control that the new case does not over-fire.

- [ ] **Step 3: Implement** `reasonRepeatedMerge`, `literalBlockAmbiguous(root *yaml.Node, block string) bool` and the new switch case in `cancelOnDisconnectSkipped` and `routerSettingsSkipped` (after the alias case; keep their existing two literals as they are). `ensureCancelOnDisconnect`, `ensureStoreModelInDB`, `routerSettingsToAdd`, `settingsWarnings` and `dbRoutesOffWarning` need no edit: they all read the predicates, and `sharedBlockLacks` already says "lacks" for a block `mergeAmbiguous` reports. Rewrite the `sharedBlockAmbiguous`, `sharedBlockLacks`, `cancelOnDisconnectSkipped` and `routerSettingsSkipped` comments to name the literal case (a skipped block is no longer only a whole-value alias or a root merge; `sharedBlockAmbiguous` and `sharedBlockLacks` already read a literal block correctly, since `resolvedBlock` returns it).

- [ ] **Step 4: Run the new tests, then the package**

Run: the Step 2 command, then `go -C wt test ./internal/litellm -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/litellm/configfile.go wt/internal/litellm/configfile_test.go
git commit -m "fix(wt): a literal router_settings or general_settings with repeated << keys is unreadable (#761)"   # body ends with the Co-Authored-By line
```

---

### Task 3: #762, a literal non-mapping block is skipped with a warning

**Files:**
- Modify: `wt/internal/litellm/configfile.go` (the two predicates; delete the `general_settings is not a mapping` branch of `dbRoutesOffWarning` at 998-1000; update the doc comments of `routerSettingsToAdd` and `dbRoutesOffWarning`)
- Test: `wt/internal/litellm/configfile_test.go`, `wt/internal/litellm/service_test.go`

**Interfaces:**
- Consumes: Task 2's predicates and `whyRepeated`.
- Produces: `const reasonNotMapping = "is not a mapping"`; both predicates return it for a block that is present, not null, not an alias and not a mapping node (after the alias case, before the repeated-merge case). Test-side: `const whyNotMapping = "is not a mapping"`.

- [ ] **Step 1: Write the tests**
  - `TestDBRoutesOffWarningTextIsUnchangedForANonMappingGeneralSettings` (a pin: write it first, it passes before and after because the deleted branch and the new skip case produce the same bytes). For `general_settings: 42`, `[a, b]`, `plain` (each `"model_list: []\n" + litellmSettingsDone + affinitySettings` after it) `f.dbRoutesOffWarning()` equals `"general_settings is not a mapping, so wt did not write store_model_in_db: true; database routes are off and local routes stay in config.yaml with a restart per change"`.
  - `TestSkipWarningsNameALiteralBlockThatIsNotAMapping`: for both blocks (rest `settledWithoutAffinity` for router, `litellmSettingsDone + affinitySettings` for general; line builders `affinitySkipLine` / `cancelSkipLine`) and values `42`, `[a, b]`, `plain`, `""`: body `key + ": " + value + "\nmodel_list: []\n" + rest`; `settingsWarnings` equals `[]string{line(whyNotMapping)}`, `routerSettingsToAdd` is empty (router), `EnsureSettings` leaves `Changed()` false and `enc(t, f)` is unchanged.
  - `TestANullOrMappingBlockStaysSilentAndIsFilled`: for each block, values `key:` (null), `{}` and a mapping with an unrelated key (`{num_retries: 2}` for router, `{database_url: postgresql://u@localhost/db}` for general): `settingsWarnings` is empty; after `EnsureSettings` the router block (`routerSettingsOf`) holds the three affinity keys, and the general block holds `cancel_on_disconnect: true` and `store_model_in_db: true` (decode `mapGet(root, "general_settings")`).
  - In `service_test.go`, `TestSyncSurfacesAnUnreadableLiteralBlockSkipWarning`, modelled on `TestSyncSurfacesASharedBlockSkipWarning`: table `body -> want` with `"model_list: []\n" + settledWithoutAffinity + "router_settings: 42\n"` giving `[]string{affinitySkipLine(whyNotMapping)}`; `"model_list: []\n" + litellmSettingsDone + "general_settings: [a, b]\n" + affinitySettings` giving `[]string{cancelSkipLine(whyNotMapping)}`; `"a: &a {num_retries: 2}\nb: &b {num_retries: 3}\nrouter_settings:\n  <<: *a\n  <<: *b\nmodel_list: []\n" + settledWithoutAffinity` giving `[]string{affinitySkipLine(whyRepeated)}`; and the general twin (`a: &a {store_model_in_db: true}`, `b: &b {x: 1}`, `general_settings:\n  <<: *a\n  <<: *b\n`) giving `[]string{cancelSkipLine(whyRepeated)}`. Assert `plan.Warnings` (`PlanSync`) and `res.Warnings` (`Sync`) both equal `want`, `res.Changed` false, `*restarts == 0`, and the file bytes unchanged.

- [ ] **Step 2: Run them**

Run: `go -C wt test ./internal/litellm -run 'TestDBRoutesOffWarningTextIsUnchangedForANonMappingGeneralSettings|TestSkipWarningsNameALiteralBlockThatIsNotAMapping|TestANullOrMappingBlockStaysSilentAndIsFilled|TestSyncSurfacesAnUnreadableLiteralBlockSkipWarning' -count=1`
Expected: the pin and the null/mapping test PASS (controls); `TestSkipWarningsNameALiteralBlockThatIsNotAMapping` FAILS (no warning today); `TestSyncSurfacesAnUnreadableLiteralBlockSkipWarning` FAILS: the two non-mapping cases get no warning, and the two repeated-merge cases are written through (`res.Changed` true, one restart) as well as drawing no warning.

- [ ] **Step 3: Implement** `reasonNotMapping` and the new case in `cancelOnDisconnectSkipped` and `routerSettingsSkipped` (`gs != nil && !isNull(gs) && gs.Kind != yaml.MappingNode`, after the alias case); delete the dead `general_settings is not a mapping` branch from `dbRoutesOffWarning` (the skip case now fires first with the same text) and fix the comments that say a non-mapping block is skipped silently (`routerSettingsToAdd`, `dbRoutesOffWarning`). Leave the now-unreachable defensive guards (`routerSettingsToAdd`'s non-mapping return, the `Kind != yaml.MappingNode` returns in `ensureCancelOnDisconnect` and `ensureStoreModelInDB`) as they are: the spec deletes only the one `dbRoutesOffWarning` branch.

- [ ] **Step 4: Run the tests, then the package**

Run: the Step 2 command, then `go -C wt test ./internal/litellm -count=1`, then `go -C wt vet ./internal/litellm` and `make -C wt check`
Expected: PASS everywhere, including the pin and the existing `TestEnsureSettingsToleratesAMalformedGeneralSettings` and `TestSkipWarningsFailClosedOnAnUnreadableSharedBlock` (a whole-value alias to a scalar or list keeps `is a YAML alias`).

- [ ] **Step 5: Commit**

```bash
git add wt/internal/litellm/configfile.go wt/internal/litellm/configfile_test.go wt/internal/litellm/service_test.go
git commit -m "fix(wt): warn when a literal router_settings or general_settings is not a mapping (#762)"   # body ends with the Co-Authored-By line
```

---

### Task 4: docs

**Files:**
- Modify: `docs/superpowers/specs/2026-11-28-litellm-model-groups-step-b-design.md` (line 58, the Block bullet), `docs/guides/04-litellm-config.md` (section 7's shared-block paragraph, about line 367, and the `cancel_on_disconnect` bullet, about line 459), `wt/docs/internals/litellm-routes.md` (the session-affinity bullet, line 31), `wt/CLAUDE.md` (the `EnsureSettings` paragraph, line 239), `wt/CHANGELOG.md` (under `## Unreleased` / `### Fixed`, after the #758 bullet)

**Interfaces:** none (prose only). Names to quote exactly: `mergeSources`, `literalBlockAmbiguous`, `routerSettingsSkipped`, `cancelOnDisconnectSkipped`, the reasons `is read through repeated merge keys (<<)` and `is not a mapping`.

- [ ] **Step 1: Edit each file** with the same facts, in each file's own voice and length:
  - Step B spec Block bullet: append that a merge that aliases a list is followed like an inline list (#760), and that a literal block with more than one `<<` key, or one that is not a mapping, is skipped too, with the same warning and the reasons above (#761, #762); a null block and a mapping stay writable.
  - Guide 04 section 7: a literal `router_settings` that is not a mapping, or has more than one `<<` key, is left alone with the same warning (reason `is not a mapping` / `is read through repeated merge keys (<<)`); to have wt fill the keys, fold the merges into one list (`<<: [*a, *b]`); a merge whose value aliases a list of mappings (`<<: *overrides`) is read like an inline list. The `cancel_on_disconnect` bullet gets the same two sentences for `general_settings`. No model names or counts.
  - `litellm-routes.md` and `wt/CLAUDE.md`: extend the skip description (`routerSettingsSkipped` / `cancelOnDisconnectSkipped` also cover a literal block that is not a mapping or has repeated `<<` keys, `literalBlockAmbiguous`; `mergeSources` resolves an aliased merge list; `dbRoutesOffWarning`'s text is unchanged, its `not a mapping` branch now arrives through the skip predicate).
  - `CHANGELOG.md`: one `Fixed` bullet: a `<<` that aliases a list of mappings is now read (it was ignored, so wt could write `session_affinity_repin_on_failover: true` over a `false` the list supplied), a literal `router_settings` or `general_settings` with repeated `<<` keys is no longer written and draws the skip warning, and a literal non-mapping block now draws it too (#760, #761, #762; `Refs #758`).

- [ ] **Step 2: Check for stale prose and links**

Run: `grep -rnE "sharedBlockAmbiguous|not a mapping|Literal blocks and the write paths|repeated .?<<" wt/CLAUDE.md wt/docs docs/guides wt/CHANGELOG.md wt/internal/litellm/configfile.go` (the CHANGELOG #758 bullet wraps lines, so also read it by eye) and read each hit for a claim the change made false; then `make check-links`.
Expected: no stale claim; `make check-links` exit 0.

- [ ] **Step 3: Commit**

```bash
git add docs/superpowers/specs/2026-11-28-litellm-model-groups-step-b-design.md docs/guides/04-litellm-config.md wt/docs/internals/litellm-routes.md wt/CLAUDE.md wt/CHANGELOG.md
git commit -m "docs(wt): the three merge-read follow-ups to the affinity skip warnings (#760, #761, #762)"   # body ends with the Co-Authored-By line
```

---

### Task 5: gate and PR

**Files:** none changed (a PR body file in the scratchpad directory).

- [ ] **Step 1: Run the gate in the background**, never with an inner `&`: `run_in_background` with `(make pre-pr; echo "GATE_EXIT=$?") > <scratchpad>/pre-pr.log 2>&1` from the worktree root. Wait for the notification, then read `GATE_EXIT=` from the log. Expected `GATE_EXIT=0`. On red: fix, or rerun idle if only `internal/modelclient` timing tests failed; never push on red.
- [ ] **Step 2: Re-check remote state** (`git fetch origin`; `git log --oneline origin/main..HEAD`; `gh issue view 760`, `761`, `762` are still open), then `git push -u origin HEAD` (no force).
- [ ] **Step 3: Open the PR** with `gh pr create --title "fix(wt): the three #759 merge-read follow-ups (#760, #761, #762)" --body-file <file>`. The body: what each issue was and what changed (one short paragraph each), the test list, the line `Fixes #760, Fixes #761, Fixes #762`, the line `Refs #758`, and the closing line `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
- [ ] **Step 4: Report to the user** the PR link and the one manual step left: `#758` (fixed by #759) is closed as housekeeping only with the user's say; this plan does not close it.

---

## Self-Review (run before handing off)

- Spec coverage: #760 shared helper, both walkers, alias-of-list reads for block and root merge in both blocks, the opt-out scenario with control, `PlanSync` (Task 1); literal repeated-merge both orders, source with two merges, both blocks, table extended, `cancelOnDisconnectSkipped` wiring (Task 2); `42`, `[a, b]`, `plain`, null and mapping stay silent and filled, dead branch deleted with its text pinned (Task 3); every doc the spec lists (Task 4); gate, PR wording, #758 left to the user (Task 5).
- Names used across tasks: `mergeSources`, `literalBlockAmbiguous`, `reasonRepeatedMerge`, `reasonNotMapping`, `whyRepeated`, `whyNotMapping` are each defined in one task and consumed only by later ones.
- Verified against the code: `mergedGetDepth` (configfile.go 475), `mergeAmbiguous` (819), `cancelOnDisconnectSkipped` (704), `routerSettingsSkipped` (746), `sharedBlockLacks` (871), `settingsWarnings` (915), `dbRoutesOffWarning` (986, dead branch 998-1000), `aliasValue` (dburl.go 38), fixtures `settledWithoutAffinity`/`affinitySettings` (service_db_test.go), `litellmSettingsDone`, `whyAlias`, `whyMerge`, `affinitySkipLine`, `cancelSkipLine` (configfile_test.go). The spec's names all exist as written; `literalBlockAmbiguous` and the reason constants are new, the spec leaves their names to the builder.
