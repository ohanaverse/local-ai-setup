# Derived agent eligibility, PR 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the per-agent `supported_providers` list with one derived eligibility rule (`catalog.providerEligible`), delete the code that only maintained the lists, and keep the field decoded and written back untouched (inert) so an older binary still parses the file.

**Architecture:** Two leaf tables in `internal/config` (`NativeOnlyAgents`, `NativeCapableAgents`, pinned to the drivers by a test in `internal/agents`) feed a four-clause `providerEligible` in `internal/catalog`. Everything that existed to maintain the lists (sushi special case, `wt-model`'s write to `config.toml`, the editor inputs, the dead `Config` helpers, two migration fixups) is deleted; a non-persisted `Synthesized` marker replaces `AllProviders`. One per-process stderr note covers a customized list that is now ignored. PR 2 (not in this plan) removes the field and decouples `wt-model`'s seeding.

**Tech Stack:** Go 1.26.7 (module `wt/`), BurntSushi/toml, Bubble Tea (the Agents tab editor), Markdown docs.

**Spec:** `docs/superpowers/specs/2026-11-28-derived-agent-eligibility-design.md` ("PR 1: behavior and dead code" and "Acceptance for PR 1"; issue #763). The sibling spec `docs/superpowers/specs/2026-11-28-litellm-merge-followups-design.md` ships first and touches other packages; the two do not depend on each other.

## Global Constraints

- Work only in a worktree under `.worktrees/` or `.claude/worktrees/` (`wt -W derived-eligibility-pr1`); never create a branch or commit in the primary checkout.
- GitHub CI is disabled, so the gate is `make pre-pr` from the repo root (about 5 minutes): run it with `run_in_background` and read the exit from the log; never push on red; a failure only in `internal/modelclient` timing tests is load, rerun idle.
- Commit messages end with the line `Co-Authored-By: Claude Code <noreply@anthropic.com>`; the PR body ends with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
- PR 1 is not the whole of #763: write `Refs #763` in the PR body and no closing keyword (`Fixes`, `Closes`, `Resolves`) followed by an issue number anywhere in it.
- Contract-covered shapes (`docs/contracts/` fixtures) change in the same commit as the code. PR 1 touches none (verified: no fixture mentions `supported_providers`); re-run `grep -rn "supported_providers\|added sushi" docs/contracts` before the last commit.
- Guides embed no live model state (no counts, routing snapshots, "today that's X"); docs write home paths as `/Users/user_name`; `make check-links` must pass when docs change.
- Two machines work this repo: re-check remote state (`git fetch`, `gh pr list`) before pushing; never force-push.
- Go commands run from `wt/`. Every `Test*` carries a top-level `//` comment saying what it tests and why it matters. Test seams are per-call values, not package globals (the one exception below mirrors `ignoredLitellmNoted`).
- Import-graph ratchet (`internal/depcheck`): `internal/catalog` and `cmd/wt-model` may not import `internal/agents`; the tables live in `internal/config` for that reason. Never add an allow-list entry.
- The rule, in order (spec "The rule"): (1) a claude-subscription row: claude only (`config.SubscriptionAgents`, unchanged, first); (2) an agent in `config.NativeOnlyAgents` (agy): only its own native row; (3) a native row (`auth.type = "native"`): only the agent whose name equals the provider id; (4) anything else: eligible for every agent, synthesized or configured.
- Anti-goals (spec): agy offered rows it cannot run; a claude-subscription row reaching any agent but claude; an older `wt-model` failing to load a `config.toml` a newer `wt` rewrote; a silent write to a user's `config.toml` beyond what a normal save does.
- `Agent.SupportedProviders` and `Agent.DefaultProvider` stay in the struct, decoded and written back untouched, with a comment saying they are ignored and kept so older binaries parse the file. `SubscriptionAgents`, the model server, slot profiles, the gateway and LiteLLM routing do not change. `[[agents]]` stays.
- A renamed or deleted function is quoted in prose: before the last commit `grep -rn <name>` over `CLAUDE.md`, `wt/CLAUDE.md`, `docs/`, `wt/docs/` (not `superpowers/` or dated `docs/reference/` write-ups) and test comments.

## Symbol check (the spec was written from recon; deviations found)

Line numbers in this plan are at base commit `870572b2` and shift as earlier tasks land; locate each by symbol.

- `providerEligible` takes `(a config.Agent, providerID string, native bool)`, not `(a, row)`; the signature stays (alias targets are tested with `native=false`).
- `deriveSushiAgents` runs from `finalizeCfg` (`internal/config/config.go:896`), as the spec says; `SushiAgents`, `SubscriptionAgents` are in `config.go:903,913`; `EnsureAgentProvider` is in `migrate.go:473`; `enableSushiForAgents` is in `cmd/wt-model/model_write.go:221` (called at line 212).
- The spec's "non-persisted `synthesized` marker" must be an exported field (`Synthesized`): `internal/sessioneditor/save_test.go` reads it from another package.
- `UpsertAgent` is `(*Config).UpsertAgent(a Agent, oldName string) error` (`config.go:1411`); its provider-existence check reads `c.Providers`, which `LoadSession` leaves empty, so the Agents tab can never save an agent with a list today (a live bug the new code must not carry).
- `tui`'s test helper `phaseModelWithList` (`internal/tui/agent_model_test.go:67`) and `TestEligibleModelsIncludesUnflagged` call `cfg.EligibleModels`; they are the only non-`config` callers and are rewritten in Task 4.
- Migration-seeded shapes are wider than the spec says: `nativeAgent` writes `[<agent>, ollama]`; `Migrate()` pins pi and opencode to `[ollama]` (`noNativeAgents`) and agy to `[agy]`; fixup 3 wrote `[ollama]` for opencode on every load; `wt-model model add sushi` wrote a trailing `sushi` into claude's and pi's lists. Task 8 treats all of these as stock. This widens the spec's two stock shapes (`[<agent>, ollama]`, `[agy]`) on purpose: the spec's own example of a customized list ("opencode pinned to ollama", "sushi added") is what wt itself wrote on every load or every sushi add, so noting it would fire on every run of every machine, which the spec's point 7 says it must not. A list a person edited (an extra provider, a dropped one) still gets the note.
- Of the spec's tables, `NativeCapableAgents` has no non-test reader in PR 1 (PR 2's `wt-model` seeding reads it); it is added now because the spec places both tables in `internal/config` and pins them to the drivers by one test.
- Running the rule change alone in a scratch copy of the tree fails exactly four existing tests, all in `internal/catalog` (`TestEligibleWireByProviderTagFamily`, `TestEligibleWireSushiForClaudeAndPi`, `TestEligibleWireOrdersByOrder`, `TestEligibleWireAliasNeedsEveryTargetProvider`); `internal/smoke`, `internal/tui`, `internal/localmodels`, `cmd/wt-session`, `internal/agents`, `internal/config`, `internal/modelserver`, `cmd/wt-model` and `internal/sessioneditor` stay green. Dropping fixups 2 and 3 alone fails seven existing tests (listed in Task 6).
- Dropping fixup 2 also changes `seedAgents` (`registry_seed.go:56`), which uses `migrateAgentRefs`: a `config.toml` with no agy entry no longer gets an `agy` native row from `wt-model model init`. PR 2 reseeds the fixed native-capable set; Task 6 pins the interim behavior.

## Review Focus

Inputs and conditions the spec implies that no task's main tests would otherwise exercise, most likely first. Each has its test in the named task.

1. An alias whose targets include a sushi or openrouter row, offered to agy: agy must not see it (agy takes only its own native row, and an alias target is never native). Task 2, `TestProviderEligibleAliasTargets`.
2. An `[[agents]]` entry with no `supported_providers` key at all (what an editor-created agent and PR 2's file look like) must load, validate, and survive an unrelated `PatchSave` without gaining `supported_providers = []` (an older binary would reject the empty list). Task 5, `TestAgentWithoutAListRoundTrips`.
3. The ignored-list note must stay silent for every shape wt itself wrote (opencode `[ollama]`, claude `[claude, ollama, sushi]`, agy `[agy]`), or it fires on every run of every machine. Task 8, `TestIgnoredAgentListsStaysSilentForStockShapes`.
4. A fresh or hand-trimmed `config.toml` with no agy entry: `Load` must not add one and `wt-model model init` must seed no agy row from it, while `wt -A agy` still resolves through the session's synthesized agent. Task 6, `TestMigrateAgentRefsAddsNoAgyAgent`, the rewritten `TestSeedAgentNamesFollowWhatLoadWillValidate` and `TestModelInitMakesAFreshConfigUsable`; `TestLoadSessionSynthesizesDriverAgents` (existing) pins the session side.
5. `wt-model model add sushi` with no `config.toml` (a host that is not a session) must create nothing and print nothing about agents; with one, it must leave the bytes alone. Task 3, `TestSushiAddLeavesConfigTomlAlone`.

---

### Task 1: Live acceptance run (a runbook, no code, before any change)

The gateway-translation premise (any agent reaches any provider) is verified live only for opencode on an OpenRouter row. This task proves it for the other agents on the base commit. It changes no file and makes no commit; its results go in the PR body (Task 10). Read-only except for two effects: each `wt smoke` run sends one short prompt (a few tokens on a cheap or free row) and registers a client session with the model server for its duration, and the sushi run loads a 46 GB pack, which happens only after the owner says yes (Step 5).

**Files:** none (results go to a scratch file outside the repo, for example `<scratchpad>/acceptance-763.md`).

**Interfaces:**
- Consumes: the running model server and its catalog; the five agent binaries on `PATH`.
- Produces: a PASS/FAIL table per (agent, model) pair that Task 10 pastes into the PR, and a go/stop decision.

- [ ] **Step 1: Build the baseline binary and scratch the session config**

Run `D=$(mktemp -d)` then, from `wt/`, `go build -o "$D/wt" ./cmd/wt-session`. Make `$D/xdg` a config home that has no `agent-wt/config.toml` of its own but keeps everything else: for every entry of the real config home (`${XDG_CONFIG_HOME:-$HOME/.config}`) except `agent-wt`, create a symlink of the same name in `$D/xdg`, and create an empty `$D/xdg/agent-wt`. A session with no `config.toml` synthesizes every driver as an agent that accepts every provider (the old `AllProviders` path), which behaves like the new rule for non-native rows, so the run is not blocked by the real per-agent lists. The symlinks matter: copilot, opencode and others keep their own login and settings under the config home, and an empty one would turn a pair into a spurious FAIL. Read, never print, the `url`, `token_file` and (if set) `ca_file` keys of `[model-server]` in the real `~/.config/agent-wt/config.toml`.

- [ ] **Step 2: Run every wt command in this task with the scratch environment**

Prefix each command with `env XDG_CONFIG_HOME="$D/xdg" WT_MODEL_MODE=remote WT_MODEL_URL=<url> WT_MODEL_TOKEN_FILE=<token_file>` (plus `WT_MODEL_CA_FILE=<ca_file>` when the server speaks https). This keeps `usage.jsonl`, `last-picked.state` and the real `config.toml` untouched.

- [ ] **Step 3: Choose the models from the live catalog, hardcoding nothing**

Run `"$D/wt" model list --server --json` and read the document's shape before filtering. Pick the cheapest or a free OpenRouter row that is `launch`-actionable (`provider_id` openrouter, lowest `price.output`, `action` launch, `state` running or cloud). If no OpenRouter row is cheap enough to be reasonable, stop and ask the owner which cloud row to use. Find the registered sushi pack row (`provider_id` sushi); do not start it yet.

- [ ] **Step 4: Run the OpenRouter row through each agent**

For each of `claude`, `codex`, `copilot`, `pi`, `opencode` run `"$D/wt" smoke --tools <openrouter-model-id> --only <agent> --json` (one agent per command so one failure does not hide another). Record agent, model id, PASS/FAIL/SKIP and the reason line. A FAIL for a reason outside wt (an agent not logged in, a binary missing) is still a failing pair for this task: record it and go to Step 7.

- [ ] **Step 5: Ask the owner before loading the sushi pack**

The pack is 46 GB and `wt smoke` of it starts it through the model server. Ask the owner in plain words whether to load it now; do not start it without a yes. On a yes, run `"$D/wt" smoke --tools <sushi-model-id> --only codex --json`, then the same with `--only copilot` (the two agents that newly gain sushi; the old claude+pi limit is stale). Record both.

- [ ] **Step 6: Unload the sushi pack**

After the sushi runs, free the memory: `"$D/wt" stop <sushi-model-id>` (through the model server; the server also stops it after its idle grace), or `wt-model stop sushi` on the host. Confirm with `"$D/wt" model list --server --json` that the row's `state` is no longer running. Free memory between tested models: unload each model when its runs are done.

- [ ] **Step 7: Decide**

All pairs PASS: continue to Task 2. Any pair FAILs: **STOP and bring it to the owner** with the table and the failing reason line. The rule stays at four clauses until a failing pair is understood; a narrow exclusion clause or a narrowed Breaking bullet is the owner's choice, and nothing is built speculatively for that case. Do not start Task 2 until the owner answers.

---

### Task 2: `providerEligible`, the four-clause rule

**Files:**
- Modify: `wt/internal/config/config.go` (add tables beside `SubscriptionAgents`, line 913)
- Modify: `wt/internal/catalog/wire.go` (`providerEligible` lines 108-119; comments at 73-81 and 108-111)
- Test: `wt/internal/config/agent_tables_test.go` (create), `wt/internal/agents/native_tables_test.go` (create), `wt/internal/catalog/wire_test.go`; `wt/internal/catalog/wire_subscription_test.go` needs no change here (its claude-only table passes under the new rule; Task 5 renames its `AllProviders` uses)

**Interfaces:**
- Consumes: `config.SubscriptionAgents`, `config.IsSubscriptionProvider(id string) bool`, `config.Agent{Name string}`.
- Produces: `config.NativeOnlyAgents []string` (`["agy"]`), `config.NativeCapableAgents []string` (`["claude", "codex", "copilot", "agy"]`; pi and opencode have no native provider); `providerEligible(a config.Agent, providerID string, native bool) bool` with the rule order in Global Constraints. The lists on an agent are no longer read by it.

- [ ] **Step 1: Write the failing table tests**

In `agent_tables_test.go`: `TestNativeTablesAreConsistent` asserts `NativeOnlyAgents` is a subset of `NativeCapableAgents`, neither has duplicates, and `SubscriptionAgents` is a subset of `NativeCapableAgents`.

In `internal/agents/native_tables_test.go`: `TestNativeTablesMatchTheDrivers` asserts (1) every name in `config.NativeCapableAgents` is a registered, non-command driver (`agents.ByName`, `agents.IsCommand`); (2) the registered non-command drivers (`agents.Names()` less the commands; nothing in this package registers a stub) outside the set are exactly `{"pi", "opencode"}`, with a failure message telling whoever adds a driver to decide whether it has a native provider (add it to the set and to the seeded natives, or extend this list); (3) for each name in `config.NativeOnlyAgents`, `Build` is model-independent: `ByName(name).Build(m, false, r)` returns a `LaunchCmd` that is `reflect.DeepEqual` for a native model with the zero `Route` and for `gwCloudModel()` with `GatewayRoute(testGW(), gwCloudModel())` (agy's driver ignores the model argument); (4) for `claude`, `codex` and `copilot`, a registered native model (`ID "<name>/native"`, `ProviderID <name>`, `Native true`) built through `BuildLaunchCmdGateway(name, m, t.TempDir(), false, testGW(), nil, nil)` after `fakeBin(t, name)` carries neither `testGWToken` nor `testGWOrigin` in its args or env (helpers from `gateway_test.go`; this is what makes "an agent's own native row" safe to offer).

In `wire_test.go` add `TestProviderEligible`, a table over `providerEligible(a, providerID, native)`:
- subscription row (`config.SubscriptionProvider`): true for `claude`; false for `codex`, `pi`, `opencode`, `copilot`, `agy`, and false for `Agent{Name: "codex", SupportedProviders: []string{config.SubscriptionProvider}}` (a list never admits it);
- agy: true only for `("agy", native=true)`; false for `("claude", true)`, `("ollama", false)`, `("sushi", false)`, `("openrouter", false)`, and `("agy", false)`;
- native rows: `("claude", true)` true for claude, false for codex, pi, opencode; `("copilot", true)` true for copilot only; `("codex", true)` true for codex only;
- everything else (`ollama`, `openrouter`, `sushi`, `omlx`, `mtplx`, `rec`, native=false): true for `claude`, `codex`, `copilot`, `pi`, `opencode`, for `Agent{Name: "codex", SupportedProviders: []string{"ollama"}}` (a list never narrows), and for `Agent{Name: "pi", AllProviders: true}` (a synthesized agent; the field is renamed in Task 5).

Add `TestProviderEligibleAliasTargets` over `EligibleWire`: an alias row (`Kind: modelapi.KindAlias`) with targets `[ollama, sushi]` is eligible for `claude`, `codex`, `pi`, `opencode` and not for `agy`; an alias with a subscription target is eligible for `claude` only; an alias with no targets is eligible for nobody.

Rewrite the four existing tests the new rule fails (the rest of `wire_test.go` and all of `wire_subscription_test.go` pass unchanged). With `eligRows()` (`c` claude native order 3, `b` ollama order 1, `a` sushi order 0, `d` copilot native order 2):
- `TestEligibleWireByProviderTagFamily`: agent `codex` with no list; providers `[a b]`, tag `y` `[a b]`, tag `x` `[a]`, family `f1` `[a]`.
- `TestEligibleWireSushiForClaudeAndPi` becomes `TestEligibleWireSushiForEveryAgent`: row `a` is in the result for `claude`, `pi`, `codex`, `opencode`, `copilot`, and not for `agy`, whatever list the agent carries.
- `TestEligibleWireOrdersByOrder`: agent `claude`, result `[a b c]` (its own native row `c` stays, copilot's `d` goes); the old agent `x` with a four-provider list no longer means anything.
- `TestEligibleWireAliasNeedsEveryTargetProvider` (keep its `rows`): `codex` gets `[a b alias/both alias/one alias/sushi]` whatever list it carries; `claude` (synthesized) gets `[a b c alias/both alias/one alias/sushi]`; `agy` gets none; the no-target alias still reaches nobody. Drop its `wide` and `pi` list cases.

`TestEligibleWireAllProvidersExcludesOthersNative` passes as written; keep it unchanged (Task 5 renames the field it uses).

- [ ] **Step 2: Run to verify they fail**

Run, each from `wt/`: `go test ./internal/config -run TestNativeTables`, `go test ./internal/agents -run TestNativeTables`, `go test ./internal/catalog -run 'TestProviderEligible|TestEligibleWire'`.
Expected: FAIL (`undefined: config.NativeOnlyAgents`, and the catalog cases fail on the old rule).

- [ ] **Step 3: Add the tables in `internal/config/config.go`**

Two `var` slices beside `SubscriptionAgents`, each with a comment saying why it lives in a leaf (the ratchet) and that `internal/agents`' `TestNativeTablesMatchTheDrivers` pins it.

- [ ] **Step 4: Rewrite `providerEligible` in `internal/catalog/wire.go`**

Keep the signature; the body is the four clauses in the order of Global Constraints, in that order: the subscription test (unchanged), then `if slices.Contains(config.NativeOnlyAgents, a.Name) { return native && providerID == a.Name }`, then `if native { return providerID == a.Name }`, then `return true`. Rewrite both doc comments (the `EligibleWire` one still says "Config.EligibleModelsIn", "SushiAgents" and "AllProviders"; keep its note that the `ProviderID == a.Name` test relies on a native provider id equalling its driver name, and that an alias is eligible when it has targets and every target passes; `providerEligible`'s says the rule and that no list is read).

- [ ] **Step 5: Run the touched packages**

Run `cd wt && go test ./internal/catalog ./internal/agents ./internal/config`, then `go test ./internal/smoke ./internal/localmodels ./internal/modelserver ./cmd/wt-model ./internal/sessioneditor` (all stay green with the rule change alone; `internal/tui` and `cmd/wt-session` take 2 to 5 minutes each and also stay green, so they wait for Task 10's gate). If one of them does fail, the failing test depended on a list narrowing eligibility: rewrite its expectation to the derived rule, never delete its coverage. Expected: PASS everywhere.

- [ ] **Step 6: Commit**

```bash
git add wt/internal
git commit -m "feat(wt): derive agent eligibility from one rule, not per-agent lists (#763)

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 3: Delete the sushi machinery

**Files:**
- Modify: `wt/internal/config/config.go` (`SushiAgents` 901-903, `deriveSushiAgents` 919-942 and its call in `finalizeCfg`, line 896)
- Modify: `wt/internal/config/migrate.go` (`EnsureAgentProvider`, 462-488)
- Modify: `wt/cmd/wt-model/model_write.go` (`enableSushiForAgents` 216-233 and its call, line 212; drop the now-unused imports)
- Test: `wt/internal/config/sushi_agents_test.go` (delete), `wt/cmd/wt-model/model_write_test.go` (replace `TestSushiAddListsSushiForClaudeAndPi`), `wt/internal/config/config_test.go`

**Interfaces:**
- Consumes: Task 2 (nothing in `catalog` references `SushiAgents` any more).
- Produces: `config.SushiAgents`, `config.EnsureAgentProvider`, `deriveSushiAgents` and `enableSushiForAgents` no longer exist; `finalizeCfg` leaves agents untouched; `wt-model model add sushi ...` never reads or writes `config.toml` and prints no `added sushi to supported_providers of:` line.

- [ ] **Step 1: Write the failing tests**

In `model_write_test.go` replace `TestSushiAddListsSushiForClaudeAndPi` with `TestSushiAddLeavesConfigTomlAlone` (keep the `agentsToml`/`writeAgentsToml` helpers): subtests (a) with `agentsToml` present, a sushi add leaves the file's bytes identical, stdout has no `supported_providers`, stderr is empty; (b) a second add changes nothing; (c) with no `config.toml`, none is created (`os.Stat` reports not-exist), stdout has no `supported_providers`.

In `config_test.go` add `TestFinalizeCfgLeavesAgentListsAlone`: a registry with a sushi provider and model and a `claude` agent listing `[claude, ollama]` finalizes (`finalizeCfg(cfg, providers, models, nil)`, called directly; `mustFinalize` dies with `sushi_agents_test.go`) with the list unchanged (no `sushi` appended).

- [ ] **Step 2: Run to verify they fail**

Run: `cd wt && go test ./cmd/wt-model -run TestSushiAddLeavesConfigTomlAlone && go test ./internal/config -run TestFinalizeCfgLeavesAgentListsAlone`
Expected: FAIL (the add still rewrites `config.toml`; `finalizeCfg` still appends `sushi`).

- [ ] **Step 3: Delete the machinery**

Remove the four symbols and their call sites listed above; delete `sushi_agents_test.go`; in `runModelAdd` keep the `warnings` slice and drop only the `enableSushiForAgents` append. `grep -rn "SushiAgents\|EnsureAgentProvider\|enableSushiForAgents\|deriveSushiAgents" wt` must find only prose to fix in Task 9 (plus the `SushiAgents` mentions in `wt/internal/catalog/wire_test.go` comments, which Task 2's rewrite of those tests already removed).

- [ ] **Step 4: Run to verify they pass**

Run: `cd wt && go build ./... && go test ./internal/config ./cmd/wt-model ./internal/catalog`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add wt
git commit -m "refactor(wt): delete the sushi supported_providers machinery (#763)

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 4: Delete the production-dead helpers

A deletion task: there is no failing test to write first. The safety net is the compiler plus an explicit check that no production code calls each helper.

**Files:**
- Modify: `wt/internal/config/config.go` (`AgentSupportsProvider` 1231-1239, `ModelsForAgent` 1612-1635, `EligibleModels` and `EligibleModelsIn` 1675-1731)
- Modify: `wt/internal/localmodels/catalog.go` (`CatalogInput.Agent` line 31 and the branch at 91-93)
- Modify: `wt/cmd/wt-session/smoke.go` (the redundant filter at line 705 and its comment; drop unused imports)
- Test: `wt/internal/config/eligible_test.go` (delete the two tests; delete the file if nothing else in it survives), `wt/internal/config/config_test.go` (`TestModelsForAgentFiltersByProvider`, `TestModelsForAgentUnknownAgent`, `TestAgentSupportsProvider`), `wt/internal/localmodels/catalog_rows_test.go`, `wt/internal/tui/agent_model_test.go`

**Interfaces:**
- Consumes: Task 3 (`AgentSupportsProvider` has one remaining caller, `localmodels/catalog.go:91`, removed here).
- Produces: none of the six symbols exist; `localmodels.CatalogInput` has fields `Config`, `Models`, `Inventory`, `HideDiscovered`.

- [ ] **Step 1: Confirm there is no production caller**

Run `grep -rn "ModelsForAgent\|EligibleModelsIn\|EligibleModels(\|AgentSupportsProvider" wt --include='*.go' | grep -v _test.go`. Expected: only the definitions and `localmodels/catalog.go:91`. Production never sets `CatalogInput.Agent` (every `CatalogInput{` literal outside tests omits it).

- [ ] **Step 2: Delete the helpers and the redundant filter**

At `smoke.go:705` the subscription row's `eligible` already comes from `smoke.CandidatesWire` through `catalog.EligibleWire`, whose first clause is the subscription rule; delete the `slices.DeleteFunc` line and its comment, keeping the `usesSub`/`subscription` handling around it.

- [ ] **Step 3: Repair the tests that only exercised them**

Delete the config tests named above. In `catalog_rows_test.go` drop `Agent: "claude"` from every literal (production never set it: the model server builds rows with `CatalogInput{Config, Models, Inventory}`). `TestCatalogRowsHidesLocalModelsNotOnDisk` is the one test whose fixture leaned on the filter: delete its `mtplx/x` inventory entry (the one commented "claude does not support mtplx") and the sentence in its doc comment that says agent `supported_providers` stays a hard constraint for discovered rows; its expected rows stay `omlx/here,ollama/ok`, and `HideDiscovered` still gives `omlx/here`. In `internal/tui`, `phaseModelWithList` (agent_model_test.go:67) replaces its `cfg.EligibleModels(agent, tag, "")` call with a loop over `cfg.Models` keeping those for which `m.HasTag(tag) && cfg.InCatalog(m)` (the tag and membership filters the deleted helper applied; the agent list no longer filters); `TestEligibleModelsIncludesUnflagged` is deleted (`TestInCatalogPredicate` in `internal/config/catalog_membership_test.go` already pins that no stored flag gates membership, #179). Fix the stale comments that name `EligibleModels` at `agent_model_test.go:211` and `agent_picker_test.go:103,185`, and `cmd/wt-session/main_test.go:659`.

- [ ] **Step 4: Run to verify**

Run: `cd wt && go build ./... && go vet ./... && go test ./internal/config ./internal/localmodels ./internal/tui ./cmd/wt-session -run 'Subscription|Smoke|Catalog|Phase|InCatalog|Eligible'` and then the five packages in full.
Expected: PASS. `TestSmokeSubscriptionRunsOnlyClaude` (cmd/wt-session/smoke_subscription_test.go:225) staying green is the proof the smoke filter was redundant.

- [ ] **Step 5: Commit**

```bash
git add wt
git commit -m "refactor(wt): delete the helpers nothing in production calls (#763)

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 5: An agent entry is valid with no list; a `Synthesized` marker replaces `AllProviders`

**Files:**
- Modify: `wt/internal/config/config.go` (`Agent` 740-748; `validateAgents` 1064-1093; `UpsertAgent` 1411-1440; `writeConfigFile` 1504-1514; doc comments on `ValidateSession` 957-967)
- Modify: `wt/internal/config/modelserver.go` (`LoadSession`, lines 77 and 89)
- Modify (rename only): every `AllProviders` use in `wt/internal/config`, `catalog`, `smoke`, `tui`, `sessioneditor` and `cmd/wt-session` tests
- Test: `wt/internal/config/validate_session_test.go`, `wt/internal/config/mutation_test.go`, `wt/internal/config/modelserver_test.go`

**Interfaces:**
- Consumes: Tasks 2 to 4.
- Produces: `config.Agent{Name; SupportedProviders; DefaultProvider; Synthesized bool}` with `Synthesized` tagged `toml:"-"` ("an in-memory agent `LoadSession` made for a driver `config.toml` does not list; never persisted"); `(*Config).UpsertAgent(a Agent, oldName string) error` checks only the name and the rename collision; `validateAgents(known map[string]bool) []error` reports an empty name, a duplicate name, and, only when `known != nil`, an id still listed that is not in `known`.

- [ ] **Step 1: Write the failing tests**

`TestValidateAcceptsAnAgentWithNoList` (validate_session_test.go): `ValidateSession` and `ValidateAll` (registry provider ids known) both accept `Agent{Name: "x"}`. `TestValidateAllStillRefusesAnUnknownListedProvider`: an agent listing `nope` still fails with `unknown provider "nope"`, with the same text as today. `TestUpsertAgentNeedsOnlyANameAndNoCollision` (mutation_test.go): with `cfg.Providers` empty (a session config), `UpsertAgent(Agent{Name: "a1"}, "")` succeeds, so does one carrying `SupportedProviders: []string{"missing"}` and a `DefaultProvider` outside it; an empty name and a rename onto an existing name still fail. Rewrite `TestUpsertAgent_Validation` and `TestValidateSessionChecksOnlyWhatConfigTomlOwns` to match (the `must have at least one supported provider` and `default provider ... not in supported_providers` expectations go).

`TestAgentWithoutAListRoundTrips` (modelserver_test.go, beside the other `LoadSession` save tests, using its `sessionHome` helper): a `config.toml` holding `[[agents]]` with only `name = "x"` plus a second agent with a list and a default provider loads with `LoadSession`, passes `ValidateSession`, and after an unrelated `PatchSave` (change `DefaultTag`) the file has no `supported_providers` text for `x` (BurntSushi/toml omits a nil slice, so the field's tag stays as it is; checked) and the other agent's `supported_providers` and `default_provider` lines are unchanged.

- [ ] **Step 2: Run to verify they fail**

Run: `cd wt && go test ./internal/config -run 'TestValidateAccepts|TestValidateAllStillRefuses|TestUpsertAgentNeedsOnly|TestAgentWithoutAList'`
Expected: FAIL (the list is still required).

- [ ] **Step 3: Implement**

Rename the field `AllProviders` to `Synthesized` everywhere (a mechanical rename; `gofmt -r 'AllProviders -> Synthesized'` or gopls). Drop the non-empty error, the `default_provider` check and (in `UpsertAgent`) the provider-existence loop; keep the `Synthesized` skip when `known == nil` in `validateAgents` and in `writeConfigFile`. Add the comment on `SupportedProviders` and `DefaultProvider`: ignored by every reader, kept decoded and written back untouched so an older binary still parses the file (PR 2 deletes them). Update the `LoadSession` and `validateAgents` doc comments (they say "accepts every provider").

- [ ] **Step 4: Run to verify they pass**

Run: `cd wt && go test ./internal/config ./internal/catalog ./internal/smoke ./internal/tui ./internal/sessioneditor ./cmd/wt-session`
Expected: PASS (`TestPatchSaveNeverWritesASynthesizedAgent` and `TestWholeFileSaveNeverWritesSynthesizedAgents` pass under the new name).

- [ ] **Step 5: Commit**

```bash
git add wt
git commit -m "refactor(wt): an agent entry needs no provider list; Synthesized replaces AllProviders (#763)

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 6: `migrateAgentRefs` keeps only fixup 1

**Files:**
- Modify: `wt/internal/config/migrate.go` (`migrateConfigSchema` comment 311-323, `migrateAgentRefs` 340-380; keep `upsertAgent` and `noNativeAgents`, which the legacy `Migrate()` still uses)
- Modify: `wt/internal/config/config.go` (the migration message at line 862: `renamed google→agy` only)
- Modify: `wt/internal/config/registry_seed.go` (`seedAgents` comment, 46-55)
- Modify: `wt/internal/agents/opencode.go` (the comment at lines 49-53: opencode is no longer described as ollama-only)
- Test: `wt/internal/config/migrate_test.go`, `wt/internal/config/registry_seed_test.go`, `wt/internal/config/lock_test.go`, `wt/internal/config/config_test.go`, `wt/cmd/wt-model/model_init_test.go`

**Interfaces:**
- Consumes: Task 5.
- Produces: `migrateAgentRefs(cfg *Config) bool` rewrites only `google` to `agy` in `SupportedProviders` and `DefaultProvider`; it adds no agent and rewrites no list.

- [ ] **Step 1: Write the failing tests and rewrite the seven existing ones this change breaks**

New, in `migrate_test.go`: `TestMigrateAgentRefsAddsNoAgyAgent` (a config with only a claude agent is returned unchanged and reports `false`), `TestMigrateAgentRefsLeavesOpencodeAlone` (an opencode agent listing `[claude, openrouter]` keeps it), `TestMigrateAgentRefsRenamesGoogle` (`google` becomes `agy` in a list and in a default provider and reports `true`; a second call reports `false`).

Existing tests that fail once fixups 2 and 3 are gone (found by running the package with them dropped), each rewritten to the new behavior, not deleted:
- `TestMigrateConfigSchema`: delete the `strip opencode native` subtest (`TestMigrateAgentRefsLeavesOpencodeAlone` replaces it); `rename google to agy` and `idempotent` stay; fix its doc comment (one fixup is left besides the gateway notice).
- `TestLoad_ValidConfig` (config_test.go): expects one agent, `claude`, not two.
- `TestSeedAgentNamesFollowWhatLoadWillValidate` (registry_seed_test.go): a `config.toml` with only claude names `[claude]` for agents and providers, and `seed` adds `[claude]`; it no longer names agy (the interim behavior the Symbol check section describes; `Load` plus `ValidateAll` after seeding still pass). Fix its doc comment.
- `TestSeedMakesAFreshConfigLoad` (registry_seed_test.go): its opencode entry listed `["opencode"]` for fixup 3 to rewire; drop that entry from the fixture and expect `env.AgentProviders` `[claude openrouter]` and `added` `[openrouter claude]`.
- `TestModelInitMakesAFreshConfigUsable` (`cmd/wt-model/model_init_test.go`): the expected stdout loses `added provider: agy`.
- `TestEditorSavePreservesConcurrentLockedApplyChange` and `TestLockedApplyPreservesConcurrentEditorChange` (lock_test.go) used the agy fixup as the change `lockedApply` persists. Their trigger becomes the rename: the `config.toml` fixture carries an agent `claude` listing `["google"]`, `lockedApply(migrateConfigSchema)` reports a change, and the assertion that `agy` survives the stale save becomes an assertion that claude's list reads `["agy"]` in the reloaded file.

`TestLoadSessionMigratesAgentRefsInMemory` and `TestMigrate_AgySeedingIsIdempotent` (the legacy `models.conf` path still seeds agy) pass unchanged.

- [ ] **Step 2: Run to verify the new tests fail**

Run: `cd wt && go test ./internal/config -run 'TestMigrateAgentRefs'`
Expected: FAIL (fixups 2 and 3 still run).

- [ ] **Step 3: Implement**

Delete fixups 2 and 3 and rewrite the doc comments on `migrateConfigSchema`, `migrateAgentRefs` and `seedAgents` to say fixup 1 is the only agent rewrite. Remove the `rewired opencode to ollama-only` text from the migration message and the opencode driver comment.

- [ ] **Step 4: Run to verify they pass**

Run: `cd wt && go test ./internal/config ./cmd/wt-model ./internal/agents ./internal/sessioneditor`
Expected: PASS. (`cmd/wt-session` and `internal/tui` are green with this change and wait for Task 10's gate.) A registry fixture with agents no longer needs an `agy` provider (the `wt/CLAUDE.md` "Fixture gotcha", fixed in Task 9); a fixture that still has one is harmless.

- [ ] **Step 5: Commit**

```bash
git add wt
git commit -m "refactor(wt): migrateAgentRefs keeps the google-to-agy rename only (#763)

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 7: The editor drops the provider inputs

**Files:**
- Modify: `wt/internal/sessioneditor/agent_form.go` (inputs 23-24, `formCursor % 4` in the key handling, `focusAgentField`, `saveAgentForm`, `agentFormView`)
- Modify: `wt/internal/sessioneditor/agents_tab.go` (`agentItem.Title` and `Description`, lines 38-50)
- Modify: `wt/internal/sessioneditor/editor.go` (fields at 95-96, widths at 409-410)
- Test: `wt/internal/sessioneditor/agent_form_test.go`, `agents_tab_test.go`, `editor_test.go`, `save_test.go`

**Interfaces:**
- Consumes: Task 5 (`UpsertAgent` needs only a name).
- Produces: the agent form has two fields, `Name` and the read-only `Installed`; saving an edited agent keeps the entry's stored `SupportedProviders` and `DefaultProvider` as they were (`saveAgentForm` starts from `m.agEdit`).

- [ ] **Step 1: Write the failing tests**

`TestAgentForm_FieldsAreNameAndInstalled`: the form's `View()` has no `Supported Providers` or `Default Provider`, and Tab cycles through two fields. `TestAgentForm_Add_NeedsOnlyAName`: a new agent with just a name saves and has no list. `TestAgentForm_EditKeepsTheStoredListUntouched`: editing (renaming) an agent that has `SupportedProviders` and `DefaultProvider` saves both unchanged. `TestAgentsTabShowsNoProviderColumns`: an item's title and description carry neither the list nor `default:`. Delete `TestAgentForm_DefaultProvider_Constrained` and `TestAgentForm_NoProviders_BlocksSave`; rewrite `TestAgentForm_Add_Success`, `TestAgentForm_RenameExisting` and `TestAgentForm_InstalledReadOnly` (the Installed field is now cursor 1), and any title or description assertion in `agents_tab_test.go`, `editor_test.go` and `save_test.go` that names a provider list. Run the package to find them; keep each test's intent.

- [ ] **Step 2: Run to verify they fail**

Run: `cd wt && go test ./internal/sessioneditor -run 'TestAgentForm|TestAgentsTab'`
Expected: FAIL (compile errors on the removed inputs, or the old fields still render).

- [ ] **Step 3: Implement**

Remove the two inputs, their key handling, focus, width and view code, and the error-cursor `switch` in `saveAgentForm` (an error is now always about the name, so the cursor goes to 0). `formCursor` wraps at 2. `saveAgentForm` still starts from `m.agEdit` and leaves its `Synthesized` flag as it finds it. In the tab, `Title` is `name  marker`; `Description` returns only the issue.

- [ ] **Step 4: Run to verify they pass**

Run: `cd wt && go test ./internal/sessioneditor ./internal/depcheck`
Expected: PASS (the editors stay off `tui`; nothing new imported).

- [ ] **Step 5: Commit**

```bash
git add wt
git commit -m "feat(wt): the Agents tab no longer edits provider lists (#763)

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 8: The ignored-customized-list note

**Files:**
- Modify: `wt/internal/config/config.go` or a new `wt/internal/config/agent_lists.go` (`(*Config).IgnoredAgentLists`)
- Modify: `wt/cmd/wt-session/app.go` (`warnIgnoredAgentLists`, the wiring in `newApp` after `warnIgnoredLitellmKeys`, line 336)
- Test: `wt/internal/config/agent_lists_test.go` (create), `wt/cmd/wt-session/app_test.go`

**Interfaces:**
- Consumes: Task 5 (`Agent.Synthesized`), Task 6 (`noNativeAgents` is unexported in `config`; the stock-shape check lives there).
- Produces: `(*Config).IgnoredAgentLists() []string` returns the names, in file order, of non-synthesized agents whose non-empty `SupportedProviders` is not a stock shape; `warnIgnoredAgentLists(w io.Writer, cfg *config.Config)` prints one `wt: note:` line per such agent, once per process (`ignoredAgentListsNoted atomic.Bool`, as `ignoredLitellmNoted`), and nothing when none. The note names `supported_providers` only; `default_provider` is not noted separately.

Stock shapes (everything wt itself wrote, which narrows nothing the new rule would not offer): for any agent `[name, ollama]` and `[name, ollama, sushi]`; for agy `[agy]`; for `pi` and `opencode` `[ollama]` and `[ollama, sushi]`.

- [ ] **Step 1: Write the failing tests**

`TestIgnoredAgentListsStaysSilentForStockShapes`: claude `[claude, ollama]`, claude `[claude, ollama, sushi]`, pi `[ollama, sushi]`, opencode `[ollama]`, agy `[agy]`, an agent with no list, and a `Synthesized` agent give an empty result. `TestIgnoredAgentListsNamesACustomizedList`: codex `[codex, ollama, openrouter]`, opencode `[claude, openrouter]` and claude `[openrouter]` are each named, in file order. `TestWarnIgnoredAgentListsPrintsOneNotePerAgentOncePerProcess` (app_test.go, resetting the atomic with a `t.Cleanup` as `litellm_proxy_test.go:556` does): two calls print each agent's note once; each line contains `wt: note:`, the agent name, `supported_providers` and `ignored`; a config with only stock lists prints nothing. `TestNewAppNotesACustomizedAgentList`: a `config.toml` fixture with a customized list starts wt with the note on the injected stderr (model on `TestNewAppWarnsAboutLegacyExitPrompt`).

- [ ] **Step 2: Run to verify they fail**

Run: `cd wt && go test ./internal/config -run TestIgnoredAgentLists && go test ./cmd/wt-session -run 'TestWarnIgnoredAgentLists|TestNewAppNotesACustomized'`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement**

Add `IgnoredAgentLists` and the stock-shape judge next to `noNativeAgents`' knowledge (config package), and the printer and its wiring in `app.go`. The round trip of the kept field is not touched here: `TestAgentWithoutAListRoundTrips` and the existing save tests already pin it.

- [ ] **Step 4: Run to verify they pass**

Run: `cd wt && go test ./internal/config ./cmd/wt-session`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add wt
git commit -m "feat(wt): one note per process for an ignored customized supported_providers list (#763)

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 9: Docs and changelog

**Files:**
- Modify: `docs/guides/02-providers-and-models.md` (the stale protocol sentence, line 106, and lines 276 and 329), `docs/guides/06-wt-agents-and-models.md` (line 141 and lines 179-191, including the UNVERIFIED comment on 191), `docs/guides/03-model-families.md` (lines 82 and 85: the discovered-model bullet and the `EligibleModels` source pointer)
- Modify: `wt/docs/wt-smoke.md` (lines 31, 108-120), `wt/docs/wt-session.md` (line 341), `wt/docs/wt-config.md` (lines 117-145: the Agents tab description, the editable fields and the two validation bullets), `wt/docs/internals/config-and-registry.md` (lines 15 and 97), `wt/docs/internals/local-models.md` (line 89, which names `cfg.ModelsForAgent` and `cfg.EligibleModelsIn`), `wt/README.md` (line 114)
- Modify: `wt/CLAUDE.md` (delete the sushi bullet, line 161; fix the "Fixture gotcha" on line 131, which says `migrateConfigSchema` always ensures an agy agent)
- Modify: `wt/CHANGELOG.md` (Unreleased)

**Interfaces:**
- Consumes: Tasks 2 to 8 (the docs describe their behavior).
- Produces: no doc, skill or CLAUDE.md names a deleted symbol or says the lists decide what an agent sees.

- [ ] **Step 1: Find every statement to fix**

Run `grep -rnE "supported_providers|supported providers|SushiAgents|AllProviders|EnsureAgentProvider|deriveSushi|added sushi to|ollama-only|ollama only|Supported Providers|Default Provider|default provider|AgentSupportsProvider|EligibleModels|ModelsForAgent|agent supports|agent can use" CLAUDE.md wt/CLAUDE.md wt/README.md docs wt/docs wt/.claude .claude --include='*.md' | grep -v 'docs/superpowers\|docs/archive\|docs/reference'` and fix each hit (the file list above is what this grep found when the plan was written; a hit it does not list is still fixed). Dated write-ups under `docs/reference/` and `docs/superpowers/` stay as written.

- [ ] **Step 2: Rewrite the statements**

Content to carry, in each place's own style: every agent is offered every non-native row, sushi and paid OpenRouter rows included; an agent's own native row is offered to it alone, another agent's never; agy sees only its own native row; a claude-subscription row reaches claude only; `[[agents]]` marks an agent as configured and nothing else; `supported_providers` and `default_provider` are still parsed and written back but ignored (older binaries need them); a customized list prints one note per process; the Agents tab edits the name only; new agents added in the editor are written without a list, so update both machines (`make install`) before adding one. Guide 02 line 106: delete the sentence that says wt compares protocols to pick direct-versus-LiteLLM routing (there are no direct routes) and say every non-native model goes through the model server's gateway. Guide 06 §6: replace the "An agent sees a provider's models only when its `supported_providers` lists the provider" paragraph, its `grep -n supported_providers` command and the sushi exception paragraph (the sushi model works for every agent; keep the smoke command for checking tool calls; the UNVERIFIED comment must stop saying `--only` needs `sushi` in a list).

- [ ] **Step 3: Add the changelog bullet**

Under `## Unreleased` / `### Changed` in `wt/CHANGELOG.md` add a `**Breaking:**` bullet: the per-agent `supported_providers` lists are no longer read; every agent is offered every non-native model row, paid OpenRouter rows and sushi included; an agent's own native row is still offered to it only (agy sees nothing else); a Claude-subscription row still reaches claude only; the keys stay in `config.toml` untouched, ignored; the Agents tab no longer edits them; `wt-model model add sushi` no longer edits `config.toml`; opencode is no longer pinned to ollama; a customized list prints one stderr note per process; an agent added in the editor is written without a list, which a `wt-model` older than this change cannot load (run `make install` on every machine first). End the bullet with `(Refs #763)`.

- [ ] **Step 4: Run the link check and the stale-name grep**

Run: `make check-links` (repo root) and the Step 1 grep again.
Expected: links pass; no hit outside the excluded dated paths that still describes the lists as deciding what an agent sees or names a deleted symbol (an unrelated "default provider" is fine).

- [ ] **Step 5: Commit**

```bash
git add docs wt/docs wt/CLAUDE.md wt/CHANGELOG.md
git commit -m "docs(wt): eligibility is derived; supported_providers is ignored (#763)

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 10: Gate and PR

**Files:** none new.

**Interfaces:**
- Consumes: Tasks 1 to 9; Task 1's results table.
- Produces: one PR, `Refs #763`, with the acceptance results in its body.

- [ ] **Step 1: Re-check the contract and name greps**

Run `grep -rn "supported_providers\|added sushi" docs/contracts` (expected: no hit) and `cd wt && gofmt -l . && go vet ./...` (expected: empty output, no findings).

- [ ] **Step 2: Run the gate**

From the repo root, run `make pre-pr > <scratchpad>/pre-pr-763a.log 2>&1; echo GATE_EXIT=$? >> <scratchpad>/pre-pr-763a.log` with `run_in_background` (no inner `&`). Read `GATE_EXIT` from the log. Expected: `GATE_EXIT=0`. A red gate is not pushed. A failure only in `internal/modelclient` timing tests is load: rerun idle, and run the test alone before believing it.

- [ ] **Step 3: Re-check remote state and open the PR**

`git fetch`, `gh pr list` (another machine may have opened or merged related work; never force-push). Push the branch and open the PR with `gh pr create --body-file <file>` (the isolation guard refuses a body that quotes git commands inline). The body starts with: what changed and why (the owner's reason, "just to simplify and align code with the session/model split"); a note that PR 2 follows and **both machines must run this build before PR 2's is installed**; the acceptance table from Task 1 (which agent, which model id, PASS/FAIL, the date); the Breaking bullet; `Refs #763`; and ends with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.

- [ ] **Step 4: Hand back**

Stop at the opened PR; the merge (`gh pr merge --squash --admin`) and `make install` on each machine are the owner's call, and the root must be on `main` before an install.
