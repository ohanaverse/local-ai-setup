# Derived agent eligibility, PR 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **GATE, state it at the top of the PR body: both machines run PR 1's build or later (`make install` from a root on `main`) before PR 2's build is installed on either.** A binary older than PR 1 rejects an agent entry with no `supported_providers` ("must have at least one supported provider"), and PR 2's first save writes exactly that.

**Goal:** Delete `Agent.SupportedProviders` and `DefaultProvider`, and make `wt-model` seed provider rows without reading `~/.config/agent-wt/config.toml`: a fixed native-capable set instead of the session's agent list.

**Architecture:** PR 1 left the two keys decoded, written back untouched and ignored. PR 2 removes them from the struct, so a normal save re-encodes `config.toml` without them (the `[gateway]` precedent), and removes the code that read them: the "agent lists it" seeding trigger, the `unseeded` report, `seedAgents`, `SeedEnv.Agents/AgentProviders/ConfigErr`, the PR 1 ignored-list note. `wt-model model init` seeds one native row per `config.NativeCapableAgents` member; a model that references a native-capable id seeds that row too.

**Tech Stack:** Go 1.26.7 (module `wt/`), BurntSushi/toml, `internal/tomlw` registry writer, Markdown docs.

**Spec:** `docs/superpowers/specs/2026-11-28-derived-agent-eligibility-design.md` ("PR 2: remove the field, decouple the host"; issue #763). Builds on PR 1, whose plan is `docs/superpowers/plans/2026-11-28-derived-eligibility-pr1.md`; the names below marked (PR 1) exist only once PR 1 has merged: `config.NativeOnlyAgents`, `config.NativeCapableAgents` (`["claude", "codex", "copilot", "agy"]`), `Agent.Synthesized`, `(*Config).IgnoredAgentLists`, `warnIgnoredAgentLists`, `ignoredAgentListsNoted`, `providerEligible`. PR 1 put `IgnoredAgentLists` either in `config.go` or in a new `agent_lists.go`: find it with `grep -rn IgnoredAgentLists wt`. Branch from a `main` that has PR 1; if a name differs there, the merged code wins. Line numbers are left out on purpose: PR 1 shifts them in `config.go`, `migrate.go`, `registry_seed.go` and most docs, so find each place by the function, test or heading named.

## Global Constraints

- Work only in a worktree under `.worktrees/` or `.claude/worktrees/` (`wt -W derived-eligibility-pr2`); never create a branch or commit in the primary checkout.
- GitHub CI is disabled, so the gate is `make pre-pr` from the repo root (about 5 minutes): run it with `run_in_background` and read the exit from the log; never push on red; a failure only in `internal/modelclient` timing tests is load, rerun idle.
- Commit messages end with the line `Co-Authored-By: Claude Code <noreply@anthropic.com>`; the PR body ends with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
- PR 2 completes #763: write `Fixes #763` in the PR body only when this is the last PR (the whole of PR 2, or PR 2b if split); PR 2a writes `Refs #763`. Never put a closing keyword and an issue number anywhere else in a body, including a "then close" step. Every `wt/CHANGELOG.md` bullet ends `(Refs #763)`, whichever PR it is in.
- Contract-covered shapes (`docs/contracts/` fixtures) change in the same commit as the code. Verified: no fixture mentions `supported_providers` or `providers_unseeded`; `wt-model model init --json` is pinned only by inline JSON in `cmd/wt-model/model_init_test.go`. Re-run `grep -rn "supported_providers\|providers_unseeded" docs/contracts` before the last commit.
- Guides embed no live model state (no counts, routing snapshots, "today that's X"); docs write home paths as `/Users/user_name`; `make check-links` must pass when docs change.
- Two machines work this repo: re-check remote state (`git fetch`, `gh pr list`) before pushing; never force-push.
- Go commands run from `wt/`; `make pre-pr` handles the scratch-HOME concerns. Every `Test*` carries a top-level `//` comment saying what it tests and why it matters. Test seams are per-call values, not package globals.
- Import-graph ratchet (`internal/depcheck`): `cmd/wt-model` and `internal/catalog` may not import `internal/agents`; the native-capable table lives in `internal/config` for that reason. Never add an allow-list entry.
- Anti-goals (spec): an older `wt-model` failing to load a `config.toml` a newer `wt` rewrote (the gate above is the mitigation); a silent write to a user's `config.toml` beyond what a normal save does (the dropped keys are the one intended difference and go in the Breaking bullet); agy offered rows it cannot run; a claude-subscription row reaching any agent but claude (PR 2 touches neither rule).
- `[[agents]]` stays and marks an agent as configured; `SubscriptionAgents`, `NativeOnlyAgents`, `providerEligible`, the model server, slot profiles, the gateway and LiteLLM routing do not change.
- A renamed or deleted function is quoted in prose: before the last commit `grep -rn <name>` over `CLAUDE.md`, `wt/CLAUDE.md`, `docs/`, `wt/docs/` (not `superpowers/` or dated `docs/reference/` write-ups) and test comments.

## Symbol check (spec and PR 1 plan against the tree at 870572b2; deviations)

- **No `providers_unseeded` fixture exists under `docs/contracts/`**, although the spec says so. The key is pinned only by inline JSON in `cmd/wt-model/model_init_test.go` (`TestModelInitJSON`) and described in `wt/docs/internals/config-and-registry.md` (the **Seeding** paragraph). Task 1 changes those; no fixture file moves.
- **No command removes a provider row; the spec's remedy is wrong.** The spec says "a user who wants no row removes it with `wt-model model rm`", but `wt-model model rm` removes models only ("Nothing else is touched"; `RegistryDoc` has `RemoveModel` and `RemoveAlias`, no provider remover). A user who does not want a seeded native row deletes its `[[providers]]` block from `registry.toml` by hand, and the next `wt-model model init` puts it back. The help text, the docs and the test below say that; the spec sentence needs the same amendment (hand-back note in Task 4).
- **Task order differs from the spec's (a) then (b), inside the spec's own staging.** `seedAgents` reads `a.SupportedProviders` and calls `migrateAgentRefs`, so deleting the field forces removing the list-driven seeding in the same PR. Task 1 therefore removes the "agent lists it" trigger, `AgentProviders` and `unseeded` first (the part of (b) that reads the field); Task 2 deletes the field; Task 3 is the rest of (b) (fixed native set, `seedAgents`, `SeedEnv.Agents`, `ConfigErr`) and (c). Tasks 1 and 2 never ship apart: after Task 1 alone, `Load` still validates a listed provider that a fresh registry has no row for.
- `wt-model` does not stop reading `config.toml` altogether: `hostConfig()`/`config.Load` still decode it (agents, `DefaultTag`, the `[litellm]` read-only fallback). What stops is **seeding**; Task 3 pins that with a broken `config.toml` that no longer produces a warning.
- `modeladmin.Add` never created a provider row by itself: a native row came from `SeedEnv.Agents` (the agents in `config.toml`). Verifying (c) is therefore a code change (Task 3), not only a test: a model that references a native-capable id must seed that id's row.
- After PR 1, `UpsertAgent` already checks only the name and the rename collision, and `migrateAgentRefs` already keeps only the `google` rename: PR 2 has no `UpsertAgent` code to change, only fixtures. `upsertAgent` (lower case) has two call sites left, both in the legacy `Migrate()` (the `noNativeAgents` loop and the agy seeding), and `nativeAgent` one; the legacy `Migrate()` still needs both, so they are reshaped (`ensureAgent`, `Agent{Name: n}`), not deleted. `noNativeAgents` stays for the same function.
- `SeedRegistryDefaults` also runs on every `wt-model model add` and the Models tab's add (`modeladmin.Add`), so what it seeds is what an add seeds.
- Existing native rows named `pi` or `opencode` stay in a registry (seeding only appends; no command removes a provider row) and are inert.

## Review Focus

Inputs and conditions the spec implies that no task's main tests would otherwise exercise, most likely first. Each has its test in the named task.

1. A `config.toml` that still carries the old keys, with a list naming a provider the registry has no row for (the stock `[claude, ollama]` on a machine without ollama): it must load, validate and seed, where before the field's removal it stopped with `unknown provider "ollama"`. Task 2, `TestLoadIgnoresLegacyAgentKeys` and `TestSeedMakesAFreshConfigWithOldKeysLoad`.
2. A native row the user deleted from `registry.toml` by hand must not come back from an unrelated `wt-model model add` (only `model init`, or a model that names that native provider, seeds natives). Task 3, `TestAddDoesNotReseedARemovedNativeRow`.
3. `wt-model model init` on a machine whose `config.toml` is unreadable, absent or lists nothing: same four native rows, no warning, no dependence on the file. Task 3, `TestModelInitIgnoresConfigToml`.
4. A registry that already holds `pi` or `opencode` native rows (seeded by older versions from the agent list): init adds nothing for them and removes nothing. Task 3, `TestSeedNativeProvidersLeavesExistingRowsAlone`.
5. A script reading `wt-model model init --json`: the `providers_unseeded` key is gone and every other key keeps its position and type. Task 1, `TestModelInitJSON`.

---

### Task 1: Seeding stops taking providers from agent lists

The part of spec (b) that reads the field: it must go before the field can. After it, `SeedEnv` still carries the agent names (Task 3 removes them).

**Files:**
- Modify: `wt/internal/config/registry_seed.go` (`SeedEnv.AgentProviders`, `seedAgents`, the `listed` map's `AgentProviders` loop and the trailing `unseeded` loop in `SeedRegistryDefaults`, its signature and doc comment, `DefaultSeedEnv`)
- Modify: `wt/internal/modeladmin/apply.go` (`AddResult.ProvidersUnseeded` and its doc comment, the `SeedRegistryDefaults` call in `Add`)
- Modify: `wt/cmd/wt-model/model_init.go` (`modelInitJSON.ProvidersUnseeded`, the `unseeded` text line, the `Long` help text paragraphs about a provider an agent lists), `wt/cmd/wt-model/model_write.go` (the `no default row for provider:` loop in `runModelAdd`)
- Test: `wt/internal/config/registry_seed_test.go`, `wt/cmd/wt-model/model_init_test.go`
- Docs: `wt/docs/internals/config-and-registry.md` (the **Seeding** paragraph), `wt/CLAUDE.md` (the `unknown provider "X"` blockquote), `docs/guides/02-providers-and-models.md` (the first numbered setup step, the OpenRouter paragraph, the `rec` comment), `wt/CHANGELOG.md`

**Interfaces:**
- Consumes: PR 1's state (field still decoded, `Agent.Synthesized`).
- Produces: `config.SeedRegistryDefaults(d *RegistryDoc, env SeedEnv) (added []string, err error)` (the `unseeded` result is gone); `SeedEnv{Agents []string; OnPath func(string) bool; ConfigErr error}` (no `AgentProviders`); `seedAgents() (names []string, err error)` reading names only, with no schema migration; `modeladmin.AddResult{ID string; ProvidersAdded []string; Warnings []string}`; `modelInitJSON` without `providers_unseeded`, the other keys in their order.

- [ ] **Step 1: Write the failing tests**

`model_init_test.go`:
- `TestModelInitJSON` (rewrite): two documents, the first run and the no-op run (the third, which only showed an empty `providers_unseeded`, goes). Stub `SeedEnv{OnPath: onPath("mtplx")}`; assert `!strings.Contains(out, "providers_unseeded")` and compare against `{"registry":"<path>","created":true,"changed":true,"providers_added":["mtplx"],"warnings":["LiteLLM routes not synced: proxy down"]}` and, for the second run, `{"registry":"<path>","created":false,"changed":false,"providers_added":[],"warnings":[]}`.
- `TestModelInitCreatesTheRegistryAndSaysWhatItAdded`: stub `SeedEnv{Agents: []string{"claude"}, OnPath: onPath("ollama")}` (no `AgentProviders`, no `unseeded` constant); stdout is exactly `registry: <path> (created)\nadded provider: ollama\nadded provider: claude\n`, the second run `registry: <path>\nnothing to add\n`; fix its doc comment, which still talks about unseeded providers.
- `TestModelInitMakesAFreshConfigUsable`: the fixture `config.toml` has agents `claude` and `pi` with no `supported_providers`; nothing lists a provider any more, so the expected stdout is `registry: <path> (created)\nadded provider: claude\nadded provider: pi\n` (the interim name-based native seeding; no ollama or openrouter, no agy: PR 1 stopped `Load` adding one), and `Load` plus `ValidateAll` pass. Fix its doc comment (it explains the old agent trigger).
- `TestModelInitSeedsAnOpenRouterRowThatFailsLoudlyWithoutItsKey`: seed openrouter by a model reference instead of by a list: `modelHome(t, content)` with content one `[[models]]` block (`id = "openrouter/m"`, `family = "f"`, `provider_id = "openrouter"`, `model_name = "org/m"`, `tags = []`) and no providers, and `stubSeedEnv(h, config.SeedEnv{})`.

`registry_seed_test.go`:
- Delete `TestSeedRowsForTheProvidersAgentsList`.
- `seed` already returns `(added, changed)`: delete `seedReport` and fix its remaining caller `TestSeedAnAgentNamedAfterAProviderGetsThatProvidersRow` (drop its `unseeded` assertions; Task 3 deletes the test).
- `TestSeedAddsOnlyWhatTheMachineNeeds`: delete the four table rows that use `AgentProviders` ("an agent lists a default provider that is not installed", "an agent lists omlx beside an omlx-6bit row", "an agent lists openrouter", "an agent lists a provider that has its row") and the sentences of its doc comment about an agent listing a provider.
- `TestSeedAgentNamesFollowWhatLoadWillValidate` becomes `TestSeedAgentNamesAreTheNamesInConfigToml`: no `config.toml` gives nil names and nil error; a `config.toml` naming only `claude` (no list) gives `[claude]` (no agy; PR 1's `TestSeedAgentsNamesNoAgyWhenConfigHasNone` is folded into this test and deleted); seeding with `DefaultSeedEnv()` and `OnPath = nil` adds `[claude]`, then `Load` and `ValidateAll` pass; an unreadable one gives nil names and a `parse config` error, and `DefaultSeedEnv().ConfigErr` is that error.
- `TestSeedMakesAFreshConfigLoad`: fixture agents `claude` and `opencode` with no list; after seeding with `DefaultSeedEnv()` and `OnPath = nil`, `added` is `[claude opencode]` (no `ollama`, no `openrouter`) and `Load` plus `ValidateAll` pass; drop the `secret_ref` assertions and the second half (`corp-gateway`), which exist only for the removed trigger; fix the doc comment.

- [ ] **Step 2: Run to verify they fail**

Run, from `wt/`: `go test ./internal/config -run 'TestSeed' && go test ./cmd/wt-model -run 'TestModelInit' && go test ./internal/modeladmin`
Expected: FAIL (compile errors on the new return shapes; the JSON still has the key).

- [ ] **Step 3: Implement**

Delete the `listed` map's `AgentProviders` loop (keep the agent-name entries for now, they are Task 3's), the final `unseeded` loop and result, and `SeedEnv.AgentProviders`. `seedAgents` returns `(names []string, err error)`: `readConfigFile`, then the names in file order, no `migrateAgentRefs` (the google rename it did only touched lists; Task 2 deletes the function). Update `DefaultSeedEnv`. Drop `ProvidersUnseeded` from `AddResult`, `modelInitJSON`, and the two printing sites; let the compiler list the imports `registry_seed.go` no longer needs. Rewrite the `SeedRegistryDefaults` doc comment (the agent trigger paragraph that talks about `Config.Validate` refusing an agent that lists a provider goes, and so does its `unseeded` code example) and the `model init` `Long` help text: the "or a configured agent" clauses and the paragraph about a provider an agent lists go; the "each configured agent" bullet stays until Task 3.

- [ ] **Step 4: Fix the docs**

`wt/docs/internals/config-and-registry.md` **Seeding** paragraph: no agent trigger for providers, no `providers_unseeded`, `seedAgents` reads names only. `wt/CLAUDE.md`, the blockquote starting `unknown provider "X"` errors: drop "a provider it has no default row for is named in its output" (the remedy is `wt-model model init`, or a hand edit of `registry.toml` for a provider wt has no default row for); the same blockquote in `config-and-registry.md` needs no change. `docs/guides/02-providers-and-models.md`: the first numbered step ("`wt-model model init` creates `registry.toml` ..."), the OpenRouter paragraph ("adds it when a model or a configured agent names openrouter") and the `rec` comment (`adds the rec provider row once a model or agent references rec`) say "a model", not "a model or a configured agent". `wt/CHANGELOG.md` `### Changed`: a `**Breaking:**` bullet: `wt-model model init` and `model add` no longer add a provider row because an agent's `supported_providers` lists it (only a model that references it, or an installed command, does), `wt-model model init --json` no longer has `providers_unseeded`, and `wt-model model init`/`add` no longer print `no default row for provider: … (an agent lists it …)`. End it `(Refs #763)`.

- [ ] **Step 5: Run to verify they pass**

Run: `cd wt && go build ./... && go test ./internal/config ./internal/modeladmin ./internal/modeleditor ./cmd/wt-model` then `make check-links` (repo root).
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add wt docs
git commit -m "refactor(wt): registry seeding no longer reads the agents' provider lists (#763)

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 2: Delete the field, its migrations and the PR 1 note

**Files:**
- Modify: `wt/internal/config/config.go` (`Agent`; `validateAgents` and its two callers, `ValidateSession` and `validate`, with their doc comments; the `migrated config to native-provider alignment (…)` stderr line in `Load`)
- Modify: `wt/internal/config/migrate.go` (`nativeAgent`, `migrateAgentRefs`, `upsertAgent`, `migrateConfigSchema` and its doc comment, the `noNativeAgents` comment, the legacy `Migrate()` call sites)
- Modify: `wt/internal/config/modelserver.go` (`LoadSession` and its doc comment)
- Delete: PR 1's ignored-list note: `(*Config).IgnoredAgentLists` and its stock-shape judge (and `agent_lists.go`/`agent_lists_test.go` if PR 1 made them), `warnIgnoredAgentLists`, `ignoredAgentListsNoted` and their wiring in `wt/cmd/wt-session/app.go`, the note's tests in `app_test.go`
- Modify: `wt/internal/catalog/wire.go` (comments naming the field), `wt/internal/sessioneditor/` (delete `TestAgentForm_EditKeepsTheStoredListUntouched`: with no list on the struct there is nothing left to keep)
- Modify (fixtures): every `_test.go` that sets `SupportedProviders`/`DefaultProvider` or writes `supported_providers`/`default_provider` into a TOML string (about 38 files; `grep -rln "SupportedProviders\|DefaultProvider\|supported_providers\|default_provider" wt --include='*_test.go'`)
- Test: `wt/internal/config/config_test.go`, `validate_session_test.go`, `migrate_test.go`, `modelserver_test.go`, `registry_seed_test.go`
- Docs: `docs/guides/06-wt-agents-and-models.md` (the `config.toml` shape paragraph in §6 and the provider-visibility paragraph PR 1 rewrote), `docs/guides/08-maintenance-and-troubleshooting.md` (the `unknown provider "agy"` advice after `wt-model litellm set`), `wt/docs/wt-config.md` (Agents tab), `wt/docs/wt-smoke.md` (Eligibility), `wt/docs/wt-session.md` (the paragraph on agents with no `[[agents]]` entry), `wt/docs/internals/config-and-registry.md` (**Schema migrations**), `wt/CHANGELOG.md`

**Interfaces:**
- Consumes: Task 1 (nothing reads a list for seeding); PR 1's `Agent.Synthesized` and `validateAgents(known map[string]bool) []error`.
- Produces: `config.Agent{Name string \`toml:"name"\`; Synthesized bool \`toml:"-"\`}`; `(*Config).validateAgents() []error` (reports an empty or duplicate name, skips `Synthesized` agents; the `known` parameter and the unknown-provider check are gone, update both callers); `ensureAgent(cfg *Config, name string) bool` (appends `Agent{Name: name}` when absent, reports whether it did) replacing `upsertAgent`; `migrateConfigSchema(cfg *Config) (bool, error)` (same signature, `lockedApply` takes it) runs only the legacy-`[gateway]` probe; `migrateAgentRefs`, `nativeAgent`, `warnIgnoredAgentLists` and `IgnoredAgentLists` no longer exist; `(*Config).UpsertAgent` is unchanged.

- [ ] **Step 1: Write the failing tests**

`TestLoadIgnoresLegacyAgentKeys` (config_test.go): a `config.toml` with `[[agents]]` entries `claude` (`supported_providers = ["claude", "ollama"]`, `default_provider = "claude"`), `agy` (`["agy"]`) and `codex` (`["no-such-provider"]`), and a registry with no `ollama` row, loads with `Load`, `LoadSession` and `ValidateAll` all nil, the three agents named in file order.

`TestPatchSaveDropsTheOldAgentKeysOnly` (config_test.go): the same file, one `PatchSave` that changes `DefaultTag`; the bytes after contain no `supported_providers` and no `default_provider`, still hold the three `[[agents]]` names in order, `default_tag` with the new value, and a `[model-server]` table with an inline `token` keeps mode 0600.

`TestSeedMakesAFreshConfigWithOldKeysLoad` (registry_seed_test.go): the fixture of `TestSeedMakesAFreshConfigLoad` with its old lists put back (`claude: [claude, openrouter]`, `opencode: [opencode]`): after seeding with `DefaultSeedEnv()` (`OnPath` nil), `Load` and `ValidateAll` succeed with no `ollama` or `openrouter` row.

`TestMigrateConfigSchemaOnlyProbesTheGatewayBlock` (migrate_test.go, replacing `TestMigrateConfigSchema`, whatever PR 1 left of it): a file with a legacy `[gateway]` block reports changed `true`, one with none reports `false`, and neither adds or edits an agent (an agent whose old list holds `google` is still read as an agent named as written: nothing reads the list). `TestEnsureAgent`: appends once, then reports `false`.

Rewrite for the shape without lists: `TestMigrate_AgySeedingIsIdempotent` (the agy agent exists once, by name, with exactly one agy provider and one `agy/native` model) and the other legacy `Migrate()` tests (pi, opencode and agy agents exist by name; `TestMigrate_EndToEnd` if it asserts a list). Delete: PR 1's note tests, `TestAgentWithoutAListRoundTrips` (superseded by the two config tests above), `TestMigrateAgentRefsAddsNoAgyAgent`, `TestMigrateAgentRefsLeavesOpencodeAlone`, `TestMigrateAgentRefsRenamesGoogle`, `TestLoadSessionMigratesAgentRefsInMemory`, `TestValidateAllStillRefusesAnUnknownListedProvider`, `TestValidate_DefaultProviderNotInSupported`; in `TestUpsertAgentNeedsOnlyANameAndNoCollision` keep the name and collision cases and drop the ones that carry a list.

- [ ] **Step 2: Run to verify they fail**

Run: `cd wt && go test ./internal/config -run 'TestLoadIgnoresLegacyAgentKeys|TestPatchSaveDropsTheOld|TestSeedMakesAFreshConfigWithOldKeysLoad|TestMigrateConfigSchemaOnly|TestEnsureAgent'`
Expected: FAIL (`unknown provider "ollama"`, the keys survive the save, the `google` list is still rewritten, `ensureAgent` undefined).

- [ ] **Step 3: Implement**

Remove the two fields and the PR 1 comment on them; let the compiler list every use. In `migrate.go` delete `migrateAgentRefs` (callers: `migrateConfigSchema`, which now runs only the gateway probe, and `LoadSession`) and `nativeAgent` (the legacy loop appends `Agent{Name: n}`); rewrite `upsertAgent` as `ensureAgent` and its two call sites; fix the doc comments of `migrateConfigSchema` and `noNativeAgents` (opencode is "no native provider", not "pinned to ollama"). In `config.go` delete the `migrated config to native-provider alignment (…)` stderr line in `Load`: `dropLegacyGateway` already prints its own notice when it is the only thing that changed. `validateAgents` drops its `known` parameter and the unknown-provider check (`validate` no longer passes `provIDs` to it; keep `provIDs` for the model checks). `LoadSession` stops migrating (its doc comment says so). Delete the PR 1 note and its wiring; `warnIgnoredLitellmKeys` stays. Then fix fixtures: struct literals drop the fields; TOML fixtures drop the lines, except the four tests above that keep an old key on purpose. A fixture that needs a provider row only because an agent listed it loses that row. The `RestErr` tests (`TestReadModelServerFileReportsADecodeErrorElsewhere`, `TestReadModelServerFileReportsAKeyElsewhereAsRestErr`) already use `name = 5` as the wrong-typed key: leave them.

- [ ] **Step 4: Run to verify they pass**

Run: `cd wt && go build ./... && go vet ./... && go test ./internal/config ./internal/catalog ./internal/sessioneditor ./internal/smoke ./internal/tui ./internal/localmodels ./internal/agents ./internal/depcheck ./cmd/wt-session ./cmd/wt-model`
Expected: PASS. Then `grep -rn "SupportedProviders\|DefaultProvider\|IgnoredAgentLists\|migrateAgentRefs" wt --include='*.go'` finds nothing, and `grep -rln "supported_providers" wt --include='*_test.go'` lists only the four tests that keep an old key on purpose.

- [ ] **Step 5: Docs and changelog**

Find the places with `grep -rn "supported_providers\|default_provider" CLAUDE.md wt/CLAUDE.md docs wt/docs --include='*.md'` (not `docs/superpowers/`, `docs/archive/`, `docs/reference/`, and not older `wt/CHANGELOG.md` entries). Docs say: `supported_providers` and `default_provider` are no longer read or written; a file that still has them loads, and the keys disappear at the next save of `config.toml` (the Agents tab, the Mode tab, `wt-model setup`); the Agents tab edits the name only; PR 1's "customized list note" is gone. Guide 08: delete the `unknown provider "agy"` advice (the error cannot occur: no agent names a provider). `config-and-registry.md` **Schema migrations**: the only fixup left is the legacy `[gateway]` probe. Check `wt/CLAUDE.md`'s "Fixture gotcha" no longer says `migrateConfigSchema` ensures an agy agent (PR 1 rewrote it; fix it if not). `wt/CHANGELOG.md` `### Changed`: a `**Breaking:**` bullet: `Agent.SupportedProviders` and `DefaultProvider` are removed; wt no longer writes `supported_providers`/`default_provider`, so a `config.toml` re-saved by this build has agent entries with only `name`, which a build older than the eligibility change (PR 1 of #763) cannot load: run `make install` from a root on `main` on every machine at that PR 1 build before installing this one; the one-line stderr note PR 1 printed for a customized list is gone with the field. End `(Refs #763)`.

- [ ] **Step 6: Run the checks and commit**

Run `make check-links` (repo root) and the stale-name grep of Global Constraints.

```bash
git add wt docs
git commit -m "refactor(wt): remove supported_providers and default_provider from the agent entry (#763)

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 3: Seed natives from a fixed set; `wt-model` stops reading `config.toml` to seed (spec (b) rest and (c))

**Files:**
- Modify: `wt/internal/config/registry_seed.go` (delete `seedAgents`, `SeedEnv.Agents`, `SeedEnv.ConfigErr`, the agent-name loops; add `SeedNativeProviders`; `DefaultSeedEnv`)
- Modify: `wt/cmd/wt-model/model_init.go` (the `ConfigErr` warning, the `Long` text, the apply function), `wt/cmd/wt-model/model_write.go` (the `model add` `Long` help paragraph on default provider rows), `wt/cmd/wt-model/seams.go` (the `seedEnv` field comment), `wt/internal/modeladmin/apply.go` (the comment on `describe`)
- Test: `wt/internal/config/registry_seed_test.go`, `wt/cmd/wt-model/model_init_test.go`, `wt/internal/modeladmin/apply_test.go`, `wt/cmd/wt-model/model_write_test.go`
- Docs: `wt/docs/wt-model.md` (the `wt-model model init` section and the `model add` paragraph "and any other row `wt-model model init` would add"), `wt/docs/internals/config-and-registry.md` (**Seeding**), `docs/guides/02-providers-and-models.md` (the first numbered setup step), `wt/CHANGELOG.md`

**Interfaces:**
- Consumes: `config.NativeCapableAgents` (PR 1); Task 1's `SeedRegistryDefaults(d, env) (added []string, err error)`.
- Produces: `config.SeedEnv{OnPath func(command string) bool}`; `config.DefaultSeedEnv() SeedEnv` (PATH lookup only, reads no file); `config.SeedNativeProviders(d *RegistryDoc) (added []string, err error)` appends the native cloud row (`auth.type = "native"`, `location = "cloud"`, `name = pyTitle(id)`, as before) for each `NativeCapableAgents` member that has no row, in table order, never editing a row that exists; `SeedRegistryDefaults` also appends the native row of a native-capable id that a registry model references (the "a model references it" trigger the default providers already have), after the default rows. `runModelInit` calls both in one `UpdateRegistry` apply and reports `added` from both, defaults first, each in the order it was appended.

- [ ] **Step 1: Write the failing tests**

`registry_seed_test.go`:
- `TestSeedNativeProvidersAddsTheFixedSet`: an empty registry gets rows `claude`, `codex`, `copilot`, `agy` in that order, each written as `[[providers]]` `id`, `name` (`Claude`, `Codex`, `Copilot`, `Agy`), `location = "cloud"` and `[providers.auth]` `type = "native"` (pin the text as `TestSeedWritesTheDefaultRows` does); no `pi`, no `opencode`; a second call adds nothing and `changed` is false. Drive it through a helper beside `seed` that runs the function inside `UpdateRegistry`.
- `TestSeedNativeProvidersLeavesExistingRowsAlone`: a registry with a hand-edited `claude` row and a `pi` native row keeps both byte for byte, gains `codex`, `copilot`, `agy` only.
- `TestSeedAddsTheNativeRowAModelReferences`: `SeedRegistryDefaults` over a registry with one model of provider `claude` and no providers adds `claude` and no other native; with a model on `pi` it adds nothing for `pi`.
- Delete `TestSeedAnAgentNamedAfterAProviderGetsThatProvidersRow` and `TestSeedAgentNamesAreTheNamesInConfigToml`; every remaining `Agents:` literal (the compiler lists them) goes: `TestSeedWritesTheDefaultRows` keeps its default-provider rows and drops `claude` and `agy` from its input and expected text; `TestSeedIsIdempotentAndNeverEditsARow` and the `SeedEnv{Agents: ...}` rows of `TestSeedAddsOnlyWhatTheMachineNeeds` (an agent that "is already a provider", "named like a default provider", `nil OnPath means nothing is installed`) are dropped or rewritten to seed by a model reference; `TestSeedMakesAFreshConfigLoad` and `TestSeedMakesAFreshConfigWithOldKeysLoad` seed with `SeedNativeProviders` after `SeedRegistryDefaults` and still must `Load` and `ValidateAll`.

`model_init_test.go`:
- `TestModelInitIgnoresConfigToml` (replaces `TestModelInitWarnsWhenConfigTomlCannotBeRead`): with `config.toml` containing `this is not toml [[[`, and `config.DefaultSeedEnv()` with `OnPath = nil`, init prints `registry: <path> (created)\nadded provider: claude\nadded provider: codex\nadded provider: copilot\nadded provider: agy\n`, stderr is empty, `--json` has `"warnings":[]`; with no `config.toml` at all the output is identical.
- `TestModelInitCreatesTheRegistryAndSaysWhatItAdded`: stub `SeedEnv{OnPath: onPath("ollama")}` (no `Agents`) gives `ollama` then the four natives; the second run is `nothing to add`.
- Update `TestModelInitJSON` (the first run's `providers_added` is `["mtplx","claude","codex","copilot","agy"]`, the no-op run's `[]`), `TestModelInitMakesAFreshConfigUsable` (a `config.toml` naming only `pi` and `claude` seeds exactly the four natives and no `pi` row; `Load` plus `ValidateAll` pass) and `TestModelCommandGroupOnWtModel` (its `"providers_added":["ollama"]` check becomes `["ollama","claude","codex","copilot","agy"]`). `TestModelInitUnderARedirectedRegistry` and `load_error_hint_test.go` assert no row counts and need no change.

`modeladmin/apply_test.go` `TestAddNativeModelCreatesItsProviderRow` (table over `claude`, `codex`, `copilot`, `agy`; registry from `scratchRegistry(t, "")`): `Add(AddRequest{ProviderID: id, ModelName: "native", Fields: Fields{Family: ptr(id)}}, config.SeedEnv{})` returns `ID id+"/native"` and `ProvidersAdded == []string{id}`, and `config.Load` plus `ValidateAll` pass with the native row and the model (set `XDG_CONFIG_HOME` to a temp dir first, as the neighbouring test does). An `Add` of `pi`/`native` (pi has no native provider) is refused with `config.ErrNoProviderRow`, with `describe`'s existing hint, and writes nothing: that is the pinned behavior.

`model_write_test.go` `TestAddDoesNotReseedARemovedNativeRow`: `modelHome(t, writeRegistry + claude, copilot and agy native provider blocks)` (the registry of a user who deleted the `codex` block by hand; no command removes a provider row); an `openrouter` add through `runModelAdd` prints no `added provider: codex` and the registry still has no `codex` block; a following `runModelInit` with `SeedEnv{}` adds exactly `codex`.

- [ ] **Step 2: Run to verify they fail**

Run: `cd wt && go test ./internal/config -run 'TestSeed' && go test ./internal/modeladmin -run TestAddNative && go test ./cmd/wt-model -run 'TestModelInit|TestModelCommandGroup|TestAddDoesNotReseed'`
Expected: FAIL (`SeedNativeProviders` undefined; init still warns on a broken `config.toml`; `Add` of `claude/native` on an empty registry is refused with `ErrNoProviderRow`).

- [ ] **Step 3: Implement**

Add `SeedNativeProviders` and one unexported helper that builds the native row, shared with `SeedRegistryDefaults`' referenced-native branch (a `wanted[id]` check over `NativeCapableAgents` after the default-provider loop); delete `seedAgents`, the `Agents` and `ConfigErr` fields, the agent-name `listed` entries and loops, and the `readConfigFile` use here (let the compiler list the imports: `os/exec` and `unicode` stay in use). In `runModelInit` call `SeedRegistryDefaults` then `SeedNativeProviders` inside the one apply function (both results assigned afresh: apply may run up to three times); delete the `ConfigErr` warning. Update the comment on `modeladmin.describe` (a model's reference now also seeds the native row of claude, codex, copilot or agy) and the `seedEnv` field comment in `seams.go` (the machine's PATH only). Rewrite the `model init` `Long` help text: the bullet "each configured agent: a native provider row" becomes "claude, codex, copilot, agy: a native provider row each, whether or not the agent is installed on this machine (an unused one is offered only to the agent it is named for; delete its [[providers]] block from registry.toml if you do not want it, `model init` adds it back)"; the paragraph about an unreadable `config.toml` goes. The `model add` `Long` help adds that a model of claude, codex, copilot or agy also gets that native row.

- [ ] **Step 4: Run to verify they pass**

Run: `cd wt && go build ./... && go vet ./... && go test ./internal/config ./internal/modeladmin ./internal/modeleditor ./internal/depcheck ./cmd/wt-model`
Expected: PASS (`cmd/wt-model` stays off `internal/agents`).

- [ ] **Step 5: Docs and changelog**

`wt/docs/wt-model.md` `model init` section: add one paragraph naming the four native rows, that init reads no `config.toml`, and that a row you do not want is deleted from `registry.toml` by hand (no command removes a provider row; init adds it back); in the `model add` section replace "and any other row `wt-model model init` would add" with the native-row-of-a-model-that-names-it rule. `config-and-registry.md` **Seeding** and guide 02's first numbered step likewise (a native row per fixed-set agent, installed or not; `model add` seeds a native row only for a model that names it). `wt/CHANGELOG.md` `### Changed`: a `**Breaking:**` bullet: `wt-model model init` no longer reads `config.toml` and no longer warns when it cannot be read; it adds a native row for each of claude, codex, copilot and agy whether or not that agent is installed (an unused native row is inert) and never one for pi or opencode, existing rows stay; `wt-model model add` no longer re-adds the native rows of the agents in `config.toml`, only the one a model names. End `(Refs #763)`.

- [ ] **Step 6: Commit**

```bash
git add wt docs
git commit -m "feat(wt): wt-model seeds the fixed native set and stops reading config.toml (#763)

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 4: Gate, split decision and PR

**Files:** none new.

**Interfaces:**
- Consumes: Tasks 1 to 3.
- Produces: one PR (or two, 2a and 2b) per the trigger below.

- [ ] **Step 1: Measure and decide the split (spec: task (b) may become its own PR)**

From the repo root: `git diff --name-only main...HEAD -- 'wt/**/*.go' ':!*_test.go' | wc -l`. The estimate is about 12 non-test Go files (Tasks 1 to 3 together); the trigger is **more than 18** non-test Go files, or `git diff --name-only main...HEAD | wc -l` over 80. Over the trigger: split at the Task 2 commit. **PR 2a** is Tasks 1 and 2 (the field and the list-driven seeding, together: they cannot ship apart) and carries the install gate at the top of its body and `Refs #763`. **PR 2b** is Task 3 (the fixed native set, `seedAgents` and the `wt-model` decoupling, (c)), branched from the merged 2a, needs no install gate (the file format is unchanged by it) and carries `Fixes #763`. At or under the trigger: one PR, gate at the top, `Fixes #763`.

- [ ] **Step 2: Re-check names and contracts**

`grep -rn "supported_providers\|providers_unseeded" docs/contracts` (expected: no hit); `cd wt && gofmt -l . && go vet ./...` (expected: empty); the renamed-symbol grep of Global Constraints for `seedAgents`, `AgentProviders`, `ProvidersUnseeded`, `IgnoredAgentLists`, `migrateAgentRefs`, `upsertAgent` (the lower-case name; `UpsertAgent` stays).

- [ ] **Step 3: Run the gate**

From the repo root: `make pre-pr > <scratchpad>/pre-pr-763b.log 2>&1; echo GATE_EXIT=$? >> <scratchpad>/pre-pr-763b.log` with `run_in_background` (no inner `&`); read `GATE_EXIT` from the log. Expected `GATE_EXIT=0`. A red gate is not pushed; a failure only in `internal/modelclient` timing tests is load, rerun idle and run the test alone. A split runs the gate once per PR.

- [ ] **Step 4: Re-check remote state and open the PR**

`git fetch`, `gh pr list` (the other machine may have opened or merged related work; never force-push). Confirm PR 1 is merged to `main`. Open with `gh pr create --body-file <file>` (the isolation guard refuses a body that quotes git commands inline). The body starts with the install gate in bold ("both machines run PR 1's build or later before this build is installed"), then what changed and why (the owner's reason: "just to simplify and align code with the session/model split"), the Breaking bullets, and ends with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`. It also says the spec's remedy "remove it with `wt-model model rm`" does not exist (that command removes models only) and what the help and docs say instead.

- [ ] **Step 5: Hand back**

Stop at the opened PR. The merge (`gh pr merge --squash --admin`) and `make install` on each machine, PR 1's build first, are the owner's call; the root must be on `main` before an install. Tell the owner the spec sentence about `wt-model model rm` needs amending, and that nothing was built to remove a provider row. A live check the owner may want after install: `wt-model model init --json` on the host shows the four native rows added or none to add, and `wt config` on a session machine saves an Agents tab edit with agent entries holding only `name`.
