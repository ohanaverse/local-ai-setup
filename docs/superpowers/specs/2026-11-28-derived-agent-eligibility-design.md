# Derived agent↔provider eligibility (#763)

Date: 2026-11-28. Size: a project, shipped as two PRs. Starts after the small LiteLLM follow-ups PR (`2026-11-28-litellm-merge-followups-design.md`); the two touch different packages and do not depend on each other.

## Intent and why

The owner's reason, in their words: "just to simplify and align code with the session/model split." There is no user-facing problem behind it.

After the split, the model server knows nothing about agents; a session applies eligibility to the rows the server sends (`catalog.EligibleWire`), and every non-native model goes through the gateway, where LiteLLM translates between the anthropic and openai wires. Any agent can reach any provider, so the per-agent `supported_providers` list in `~/.config/agent-wt/config.toml` no longer decides connectivity. It only decides what a user is offered, and it is the last piece of hand-maintained, host-flavoured state in the session's file. It has already needed two bolted-on special cases (`SushiAgents`, `SubscriptionAgents`), and `wt-model` still writes it (`enableSushiForAgents`), the one place the host edits a session-owned file.

## What the recon found (it changes the issue's recommendation)

Issue #763 recommends "option B": derive by default, keep `supported_providers` as an optional narrowing list that may be empty. Recon over the code says B would simplify nothing:

- `migrate.go` writes a non-empty list into every real config (`[<agent>, ollama]`); fixup 2 pins agy to `[agy]` and fixup 3 pins opencode to `[ollama]` on every load. Under B every existing config stays narrow forever, so B is a second mode that no real config reaches, unless a migration silently clears the lists in a user file.
- The derived default the issue calls "already there" exists only for session-synthesized agents (`AllProviders`) and only for drivers the migration does not pin.
- Most of the machinery is already dead or redundant: `Config.ModelsForAgent`, `EligibleModels`, `EligibleModelsIn` and `AgentSupportsProvider` have no production caller; `smoke.go:705` re-applies a filter `EligibleWire` already applied; `deriveSushiAgents` never runs on a session (`LoadSession` does not call `finalizeCfg`), so the session's only sushi gate is `wire.go:118`.

So the simplification is to delete the lists' effect (option A), staged so nothing breaks a machine that has not updated.

### opencode, verified (2026-11-28)

Fixup 3 pins opencode to ollama. Whether opencode can run other rows was unverified, so it was checked: the driver (`internal/agents/opencode.go`) declares one custom provider (`@ai-sdk/openai-compatible`, chat-completions) at the gateway and names the model by its gateway id, whatever the provider. A live `opencode run` through the running model server with an OpenRouter row (`openrouter/qwen--qwen3.8-flash`, opencode 1.18.34) answered a plain prompt and executed a bash tool call whose output came back correctly. The pin is a leftover from the 2026-08-31 consolidation (the `opencode is ollama-only in normal use` comment in the driver), not a capability limit, and goes.

## Goals, non-goals, anti-goals

Goals:
- One derived eligibility rule in one place (`catalog.providerEligible`), no per-agent list consulted.
- Delete everything that existed only to maintain the lists: sushi special case, `wt-model`'s write to `config.toml`, the editor inputs, the dead helpers.
- Stage it so an old binary on another machine keeps parsing the file until both are updated.

Non-goals:
- The model server, slot profiles, gateway and LiteLLM routing do not change.
- `[[agents]]` stays: it still marks an agent as configured (`agents.IssueFor`, `ListEntries`). Retiring the table and the Agents tab is a separate product decision, not part of this.
- No per-model or per-row allow-list in the registry (a possible later follow-up if narrowing is missed).
- `SubscriptionAgents` is auth, not protocol, and stays as is.

Anti-goals (a failure even if it technically works):
- agy offered rows it cannot run (its driver ignores the model argument);
- a claude-subscription row reaching any agent but claude;
- an older `wt-model` failing to load a `config.toml` a newer `wt` rewrote;
- a silent write to a user's `config.toml` beyond what a normal save does.

## The rule

`providerEligible(a, row)`, in order:
1. A claude-subscription row: claude only (unchanged, first).
2. An agent in `config.NativeOnlyAgents` (agy): only its own native row.
3. A native row (`auth.type = "native"`): only the agent whose name equals the provider id.
4. Anything else: eligible, for every agent, synthesized or configured.

`NativeOnlyAgents` and the native-capable set live in `internal/config`, a leaf, like `SubscriptionAgents` already does, because `catalog` and `wt-model` may not import `internal/agents` (the import-graph ratchet in `wt/CLAUDE.md`). A test in `internal/agents` pins both tables to the drivers so they cannot drift.

## Approaches considered

1. **B as written in the issue** (derived default, optional non-empty list narrows, empty becomes legal). Rejected: no real config reaches the derived path, so it adds a mode and changes no behavior (see the recon above).
2. **A in two PRs (chosen).** PR 1 swaps the rule and deletes dead code with the field kept decoded but ignored; PR 2 removes the field and decouples the host.
3. **A in one PR.** Same end state, but a roughly 55-test-file mechanical diff mixed with a behavior change, and an older `wt-model` fails `Load` ("must have at least one supported provider") on a file the new `wt` rewrote without the key.

## PR 1: behavior and dead code (field kept, inert)

1. Rewrite `providerEligible` (`catalog/wire.go`) to the rule above; drop the list, `AllProviders` and sushi clauses; fix the comments at `wire.go:73-81,108-111`.
2. Delete the sushi machinery: `SushiAgents`, `deriveSushiAgents` and its `finalizeCfg` call, `EnsureAgentProvider`, `enableSushiForAgents` with its call and its `added sushi to supported_providers of:` output line, and their tests.
3. Delete the production-dead helpers: `ModelsForAgent`, `EligibleModels`, `EligibleModelsIn`, `AgentSupportsProvider`, `CatalogInput.Agent` and its branch, the redundant filter at `smoke.go:705`, and the tests that only exercised them.
4. Make an agent entry valid with no list: drop the non-empty errors (`validateAgents`, `UpsertAgent`) and the `default_provider` checks; keep the unknown-id check for ids still listed. Replace `AllProviders` with a non-persisted `synthesized` marker so a session-synthesized agent is never written to `config.toml`.
5. `migrateAgentRefs`: drop fixup 2 (agy is covered by `NativeOnlyAgents`) and fixup 3 (opencode pin, per the verification above); keep fixup 1 (google to agy). Remove the "rewired opencode to ollama-only" text.
6. Editor: remove the Supported Providers and Default Provider inputs from the agent form and tab.
7. Keep `Agent.SupportedProviders` and `DefaultProvider` decoded and written back untouched (comment: ignored, kept so older binaries still parse the file). Print one stderr note per process, but only for a list a person edited, not one wt itself wrote (amended after plan verification: wt wrote `[<agent>, ollama]`, `[agy]`, `[ollama]` for pi and opencode, and appended `sushi` to claude's and pi's lists, so all of those are stock; a note for them would fire on every run on every machine). A list with any other shape is the case where the user's intent is being dropped. Precedent: `legacyGatewayNotice`, `IgnoredLitellmKeys`. The note lives only until PR 2.
8. Docs and changelog in the same change: `docs/guides/02` (the stale protocol sentence at line 106 and the list instructions), `06`, `03`; `wt/docs/wt-smoke.md`, `wt-config.md`, `internals/config-and-registry.md`, `internals/local-models.md`; the sushi bullet in `wt/CLAUDE.md`; a `**Breaking:**` bullet in `wt/CHANGELOG.md` Unreleased saying narrowing lists are no longer read and that every agent is offered every non-native row, paid OpenRouter rows included.

## PR 2: remove the field, decouple the host

1. Delete `Agent.SupportedProviders` and `DefaultProvider` from the struct and from `migrate.go` (`nativeAgent`, fixup 1, `upsertAgent`), and fix the fixtures. A normal save then re-encodes `config.toml` without the keys (the `[gateway]` precedent), and the PR 1 note is deleted with the field. **Gate: both machines run PR 1's build or later before PR 2's build is installed**, since a binary older than PR 1 rejects an agent entry with no list; the PR says so at the top.
2. `wt-model` stops reading `agent-wt/config.toml`: delete `seedAgents`, `SeedEnv.Agents/AgentProviders/ConfigErr`, the "listed" seeding trigger and the "unseeded" output of `wt-model model init`. Native rows are seeded for the fixed native-capable set (claude, codex, copilot, agy; not pi or opencode) whether or not the agent's binary is installed: the host cannot see the session's `PATH`, and an unused native row is offered only to the agent it is named for, so it is inert. No command removes a provider row (`wt-model model rm` removes models only), so a user who wants no such row deletes its `[[providers]]` entry from `registry.toml` by hand; a provider-removal command is a separate follow-up. The `providers_unseeded` fixture and `model_init_test.go` expectations change in content as well as key. This changes the pinned `providers_unseeded` key, so the fixture in `docs/contracts/`, `model_init_test.go` and the docs move in the same change. Existing native rows named pi or opencode stay in a registry (seeding only appends) and are inert. If this half proves large, it becomes a third PR.
3. Verify `modeladmin.Add` can still create a native provider row for `wt-model model add <agent> native`.

## Constraints

`wt/CLAUDE.md` rules apply: every PR gate-green on `make pre-pr`, run with `run_in_background` and the exit read from the log; contract-covered shapes move with their fixture; docs state no live model counts. Two machines work this repo, so re-check remote state before pushing and never force.

## Acceptance for PR 1 (beyond the gate)

The gateway-translation premise (any agent reaches any provider) is verified live only for opencode on an OpenRouter row. PR 1's first task, **before** the rule is rewritten, runs `wt smoke --tools <model> --only <agent>` through the live model server and records the results in the PR:

- a cheap or free OpenRouter row for each of claude, codex, copilot, pi and opencode;
- the registered sushi pack for codex and copilot, the two agents that newly gain sushi. Sushi's old claude+pi limit is stale: its comment ties it to openai-chat only plus "pi direct", and direct routes are gone; the guide says the two were just the agents it was "set up for". The pack is 46 GB, so the owner is asked before it loads, and the model is unloaded afterwards.

If any pair fails, **stop and bring it to the owner**: the rule stays at four clauses until a failing pair is understood, and the likely outcome (a narrow exclusion clause, or a narrowed claim in the Breaking bullet) is theirs to pick. Nothing is built speculatively for that case.

## My calls (marked as mine, not agreed)

- The leaf tables are named `NativeOnlyAgents` and a native-capable set in `internal/config`.
- `DefaultProvider` goes with the list rather than getting its own treatment: nothing reads it on a launch path.
- One stderr note per process for an ignored customized list, not a rewrite or a prompt.
- The acceptance run precedes the rule rewrite, and a failing pair stops the work and goes to the owner (answers a builder-check question).
- PR 2 seeds the fixed native-capable set regardless of what is installed, and its save path may drop the old keys, gated on both machines running PR 1 or later (answers two builder-check questions).
- New agents added in the editor after PR 1 are written without a list, so an older `wt-model` on another machine fails to load that file until it is updated: update both machines (`make install`) before adding an agent in the editor.
- The `UpsertAgent` provider-existence check reads `cfg.Providers`, which a session leaves empty; if that is a live bug the new code must not carry it, so the check goes with the list.

## Left to the builder

Names of the new marker and tables, the wording of the ignored-key note, how the ~55 test files are split between deleted, rewritten and surviving, and whether PR 2's host half is one PR or two.
