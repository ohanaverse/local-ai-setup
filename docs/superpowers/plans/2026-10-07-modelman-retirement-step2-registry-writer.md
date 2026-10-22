# Modelman Retirement, Step 2 (wt Writes the Registry) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give wt one safe way to write `registry.toml` — byte-stable beside modelman's writer, locked, symlink-aware — with `wt model init` as its first caller, and make the three registry readers agree on a new env name, `WT_REGISTRY`.

**Architecture:** A new package, `wt/internal/tomlw`, decodes TOML into an ordered table tree (key order recovered from BurntSushi's `MetaData.Keys()`) and emits it in tomli-w's exact layout, so a wt write that changes nothing changes no byte. `config.UpdateRegistry(apply)` is the only write path: flock, symlink resolution, decode, a pure `apply` over a patch-shaped `RegistryDoc`, validation of the touched rows only, a re-check before an atomic rename. Seeding (`config.SeedRegistryDefaults`) is an operation on that document, called by `wt model init` now and by `wt model add` in Step 3. modelman gets one change: it refuses to save a snapshot over a file another program changed.

**Tech Stack:** Go 1.26.7 (module root `wt/`), `github.com/BurntSushi/toml` v1.6.0 (already pinned; decode only), cobra. Python 3 with uv for `modelman/` and `llmbench/`, `tomli-w` 1.2.0 (already a dependency of both), pytest. No new dependencies in any of the three.

**Spec:** [docs/superpowers/specs/2026-10-06-modelman-retirement-design.md](../specs/2026-10-06-modelman-retirement-design.md), section "Step 2 — wt writes the registry". That section is the authority: where this plan and the spec differ, the spec wins and the plan is the bug. This plan covers Step 2 only. Steps 0 and 1 have merged (`llmbench/` exists with its own registry reader; wt has the `Loading` flag). Step 3 (`wt model` CLI and the Models tab) and Step 4 (`wt cloud-sync`) build on the `RegistryDoc` API this plan ships and get their own plans.

## Global Constraints

- `config.UpdateRegistry(apply func(*RegistryDoc) error) (changed bool, err error)` is the only way wt writes the registry. Its six steps are the spec's: flock on `<registry path>.lock` (the path as named, not a symlink's target); resolve the path (a dangling symlink is `ErrRegistryLink`, never "missing"; a resolving symlink is written through); read and decode (an unknown top-level key refuses the write and names the keys; wt never adds a top-level key); run `apply`; validate only the rows `apply` touched; skip the write when the bytes are unchanged; otherwise re-check that the file has not changed since it was read (retry `apply` up to three times, then `ErrRegistryBusy`) and rename atomically. Two Decisions rows say how this plan reads that step: 'What is "unchanged" for the no-op skip?' and 'How many times does `apply` run before `ErrRegistryBusy`?'.
- `apply` must be pure: it may run more than once, and must not print, call ollama or mutate caller state.
- `RegistryDoc` is patch-shaped: `PatchModel(id, set, unset)`, `AddModel(table)`, `CloneModel(fromID, overrides)`, `RemoveModel(id)` returning the removed row, `SetTimePrices(id, rows)`, `AddProvider(table)`, `Models()`, `Providers()`; typed `ErrModelNotFound` and `ErrModelExists`. There is no whole-struct `SetModel`. Nothing writes a `config.Model`.
- `model_info` is opaque: written only when a caller passes it. A price key is written only when its value changes, so an integer price is not rewritten as a float.
- The stock `toml.Encoder` is never used on the registry: it sorts keys and formats local dates in UTC.
- `ErrRegistryRedirected` behaviour is unchanged: a registry write under a redirected registry succeeds, and the route sync that follows is refused unless `WT_LITELLM_CONFIG` is set.
- Env precedence, identical in all three readers and landed in one PR: `WT_REGISTRY` > `MODELMAN_REGISTRY` > `$XDG_CONFIG_HOME/local-ai/registry.toml` > `~/.config/local-ai/registry.toml`.
- One seeding implementation, `config.SeedRegistryDefaults`. Reads never seed. Its callers are `wt model init [--json]` (this plan) and `wt model add` (Step 3). A provider row is seeded for three reasons: a model references the provider, its command is installed, or a configured agent lists it. The third, the agent trigger, is an owner decision (2026-10-07) that modelman's seeding does not have: it is what makes wt's own config validation pass after `wt model init` on a machine with a `config.toml` and no registry. Seeding never writes a key or a `secret_ref`.
- Hint text: `modelman migrate` becomes `wt model init`, and the missing-registry line names the command once (owner decision, 2026-10-07). `loadRegistry`'s error in `internal/config/registry.go` carries `— seed it with `wt model init``; `configError` in `cmd/wt/helpers.go` no longer adds a second `(seed the registry with …)`.
- modelman is frozen to bug fixes. Its only source changes here are `WT_REGISTRY` in `_default_registry_path` (PR 2) and the stale-snapshot guard (PR 6): `load_registry` records the file's `mtime_ns`, size and inode, or that there was no file, `_write_registry` raises `RegistryError("registry.toml changed on disk; reload")` when the file no longer matches, and a successful write updates the record. The plan's guard also remembers on each loaded `Registry` whether a later load found another program's change (Decisions table). PR 6 must merge before PR 5, as the spec says: PR 5 ships wt's first registry write (`wt model init`). modelman's TUI is not disabled in this step (owner decision, 2026-10-07): the PR series that ships wt's Models tab in Step 3 disables it, and that is what ends the window in which an open TUI refuses every save after a wt write.
- The test guard is a package-level seam set by `IsolateConfigHomeForTest` and `TestMain`. No `import "testing"` in shipped code.
- In this step wt gains exactly one writer of the registry, `wt model init`. Docs must not claim more.
- `make test-all` (monorepo root) passes at the end of every PR, with modelman still working.
- Run every Go command from `wt/`, never the monorepo root.
- Every `Test*` function has a top-level `//` comment saying what it tests and why a regression matters to a user.
- Tests never touch the developer's machine: no real registry, no real `config.yaml`, no live provider, nothing under `~/.config`, and no decision taken from the developer's `PATH`.
- Never print, copy or commit a real `registry.toml`: it can hold API keys. Fixtures are `docs/contracts/registry.sample.toml`, `docs/contracts/registry.written.sample.toml` and the synthetic text in the tests below.
- Python changes pass `make check` in their package (ruff, ruff format, mypy): two blank lines between top-level functions, double quotes, lines of at most 100 characters. Where this plan shows two test functions in separate blocks, they are separate top-level functions.
- Before each Go commit: `test -z "$(gofmt -l .)"` (it fails when `gofmt -l` prints anything; `gofmt -l` alone exits 0) and `go vet ./...`.
- Commit messages follow the repo's conventional style (`feat(wt): …`, `fix(wt): …`, `test(wt): …`, `docs: …`), with the attribution trailer your session is told to add, if any.
- Work happens on a feature branch off `main`, one branch per PR. **Pushing a branch and opening a PR need the owner's OK.** Do not push, and do not run `gh pr create`, until the owner says so.
- Read `wt/CLAUDE.md`, `wt/docs/internals/config-and-registry.md` and `wt/docs/internals/testing.md` before starting.

## Review Focus

1. **A registry a person formatted by hand** (comments, their own layout), then a wt command that changes nothing. Expected: the file is byte-identical afterwards. The first write that does change something rewrites it in tomli-w's form and drops the comments, as every modelman save does. Pinned in Task 11 (`TestUpdateRegistryNoOpKeepsAHandFormattedFile`).
2. **modelman saves while wt is writing**, once or over and over. Expected: once, both edits survive; over and over, wt gives up with `ErrRegistryBusy` after three runs of `apply` and writes nothing. Pinned in Task 11 (`TestUpdateRegistryRetriesWhenTheFileChangesUnderIt`, `TestUpdateRegistryGivesUpAfterThreeAttempts`).
3. **A developer with `WT_REGISTRY` exported runs the test suites.** Expected: no test reads or writes that file. Pinned in Task 3 (`TestIsolateConfigHomeForTestClearsBothRegistryNames`, and the `WT_REGISTRY=… go test ./...` run in its Step 7), Task 4 (`test_conftest_clears_both_registry_names`), Task 5 (`test_default_config_paths_are_under_the_scratch_home`, and the `WT_REGISTRY=… uv run pytest` runs in its Step 5), Task 11 (`TestTestMainGuardsTheDevelopersRegistry`) and Task 15 (`TestRegistryWriteGuardIsArmedHere`).
4. **One key that is an inline array in one row and a `[[header]]` array in the next** (tomli-w picks the form per array, by row length). Expected: every row keeps its own key order. Pinned in Task 8 (`TestOneKeyInBothArrayFormsKeepsItsOrder`).
5. **A registry symlink that loops, chains through another link, or is relative.** Expected: a chain or a relative link resolves; a loop is `ErrRegistryLink`, like a dangling link. Pinned in Task 1 (`TestResolveRegistryFile`).
6. **First run on a machine that has a `config.toml` but no registry.** Expected: after `wt model init`, `wt` loads and validates — including when an agent lists a default provider that is not installed (`ollama`, which wt's own migration gives the `opencode` agent), when an agent lists `openrouter`, and for the `agy` agent that migration adds on the next load. Seeding reads the agents as that load will see them. The `openrouter` row is seeded with no key and no `secret_ref`. One case is left: a provider wt has no default row for (a `corp-gateway` of the user's own) is not seeded; `wt model init` names it in its output and it stays a hand edit of `registry.toml`. Pinned in Task 14 (`TestSeedMakesAFreshConfigLoad`, `TestSeedRowsForTheProvidersAgentsList`, `TestSeedAgentNamesFollowWhatLoadWillValidate`) and Task 15 (`TestModelInitMakesAFreshConfigUsable`).
7. **A bad row someone else left in the registry** (no `family`, a negative price), then an edit of a different row or the removal of the bad one. Expected: both succeed; only an edit that leaves a touched row invalid is refused. Pinned in Task 12 (`TestUpdateRegistryValidatesOnlyTheRowsItTouched`).
8. **modelman open, and the registry is deleted, or changed without changing its size, or reached through a symlink; or created by wt after modelman found none; or written by wt and then loaded by modelman's own background price refresh.** Expected: modelman's next save of the older snapshot is refused in every case. Pinned in Task 17 (`test_a_registry_deleted_since_it_was_loaded_is_refused`, `test_a_change_that_keeps_the_size_is_still_caught`, `test_a_symlinked_registry_is_judged_by_its_target`, `test_a_registry_created_after_modelman_found_none_is_not_overwritten`, `test_a_background_load_does_not_bless_an_older_snapshot`).

## Decisions This Plan Makes

The spec states the behaviour; these are the choices it leaves open. Each is pinned by a test named below, so a reviewer who disagrees changes one place. The two exceptions say so in their last column.

| Question | Decision | Why | Pinned by |
|---|---|---|---|
| How does the decoder tell an inline array of tables from a `[[header]]` one? | Per array, from the Go type BurntSushi decodes it to (`[]map[string]any` for headers, `[]any` for inline), not from `MetaData.Type`. One known exception: an inline array with an empty-string key in a later row also comes back as `[]map[string]any`; such a document is refused by the next row's rule, not written with guessed key order. | `MetaData.Type` is per key path, and one path can hold both forms in one file. The prototype used it and got 7 of 3,000 random documents wrong. No registry key is empty. | Task 8, `TestOneKeyInBothArrayFormsKeepsItsOrder`, `TestDecodeRefusesAnInlineRowWithAnEmptyKey` |
| What does `Decode` do when the decoder's key list names a key its decoded tree has no place for? | It fails: `tomlw: cannot place key <path>: the decoder's key list and its values disagree`. `UpdateRegistry` then refuses the write with `parse <path>: …`. | BurntSushi v1.6.0 drops a value when a key path is an array in one `[[row]]` and a dotted-key table in a later row. Writing the decoded tree back would delete that key from the user's file without a word. | Task 8, `TestDecodeRefusesADocumentTheDecoderLostAKeyFrom` |
| What is "unchanged" for the no-op skip? | The document encodes to the same bytes before and after `apply`. Not "the output equals the file". | A hand-formatted file is never equal to its tomli-w form, so comparing against the file would rewrite it on every no-op. | Task 11, `TestUpdateRegistryNoOpKeepsAHandFormattedFile` |
| What does a write to a missing registry do? | Any `apply` that succeeds creates the file, mode 0600, even when it adds nothing. An existing file keeps its mode. | The spec says seeding "creates the file when it is missing"; one rule is simpler than a create flag. 0600 is the mode modelman's `mkstemp` gives a new registry. | Task 11, `TestUpdateRegistryCreatesAMissingRegistry`, `TestUpdateRegistryKeepsTheFilesMode` |
| How are nested keys addressed in `PatchModel`? | Dotted paths inside the row: `cost.input_price_per_million`, `fetch.repo`. A key whose own name has a dot cannot be addressed; set the table that holds it. | Steps 3 and 4 patch `cost.*`, `fetch.*` and `draft.*`. No registry key has a dot in its name. | Task 10, `TestPatchModelChangesOnlyTheNamedKeys` |
| Can `PatchModel` change `id`? | No; it is an error. A row under a new id is `CloneModel`. | Usage history, rotation and profiles key on the id (spec, Step 3). | Task 10, `TestPatchModelRefusals` |
| Which keys does "written only when its value changes" cover? | Every key, not only prices. An integer and a float with the same numeric value count as the same value. | One rule; it is also what makes a repeated `PatchModel` a no-op. | Task 10, `TestPatchModelLeavesAnEqualValueAlone`; Task 7, `TestSameTreatsAnIntegerAndAFloatAlike` |
| Where does a new key that is not in the schema go? | Ahead of the first schema key the row has, after any other such keys. | That is where modelman's `{**extra, **d}` writes them, so modelman's next save does not move it. | Task 7, `TestSetAtPutsANewKeyAtItsSchemaPosition` |
| In what order are `set` keys applied? | Sorted by key. | Go map order is random; the result must not be. | Task 10, `TestPatchModelPutsNewKeysWhereModelmanWould` |
| What does a `PatchModel` that fails on one of several keys leave behind? | Nothing: the patch is applied to a copy of the row, which replaces the row only when every key applied. | An `apply` that tolerates a per-model error (a batch price refresh) would otherwise write the half-applied row, and unvalidated, since the row was never marked touched. | Task 10, `TestPatchModelRefusals` |
| What do `Models()` and `Providers()` return? | Deep copies, as `[]*tomlw.Table`. | A planner that edited a row in place would bypass the touched-row validation. | Task 10, `TestModelsAndProvidersAreCopies` |
| Where does `CloneModel` put the copy? | Last, like `AddModel`. | modelman's catalog mirror appends (`ollama_catalog.py:609`). | Task 10, `TestCloneModelCopiesEveryKey` |
| How does `CloneModel` get the copy's id? | From `overrides["id"]`. A clone that keeps the source's id is `ErrModelExists`. | It keeps the spec's two-argument signature. | Task 10, `TestCloneModelCopiesEveryKey` |
| What does `SetTimePrices` do with no rows? | Deletes `cost.time_prices`. | The spec says only "replace"; an empty array of tables has no `[[header]]` form, and modelman omits the key. | Task 10, `TestSetTimePricesReplacesTheRows` |
| What is left when the last model is removed, or a table's last key unset? | `models = []`, and the empty table. | modelman writes `models = []` and an empty `[models.cost]` too. | Task 10, `TestRemoveModelReturnsTheRow` |
| Which typed errors exist beyond the spec's two? | `ErrProviderExists`, `ErrRegistryTopLevel`, `ErrRegistryInvalid`, `ErrRegistryLink`, `ErrRegistryBusy`. | Step 3 maps each to an exit status and a message. | Task 1, `TestResolveRegistryFile`; Task 10, `TestAddProviderAppendsARow`, `TestNewRegistryDocRefusesAnUnexpectedTopLevel`; Task 11, `TestUpdateRegistryGivesUpAfterThreeAttempts`; Task 12, `TestUpdateRegistryValidatesOnlyTheRowsItTouched` |
| How does `ErrRegistryTopLevel` treat `models = "x"`? | Also refused: each of the three known keys must be an array of tables. | The operations would otherwise replace it. | Task 10, `TestNewRegistryDocRefusesAnUnexpectedTopLevel` |
| What exactly is validated on a touched row? | Model: `id`, `family`, `provider_id`, `model_name` non-empty strings; modelman's cost rules (numbers, finite, not negative; a subscription price needs `month` or `year`; `time_prices` rows, windows, days, `HH:MM`, IANA zone); then wt's typed decode. Provider: `id`, `auth.type`, typed decode. A legacy `cost.kind` is only checked to be one of the three kinds. Not checked: the two `Config.Validate` rules that need other rows, a `location` that resolves to `local` or `cloud` and a `provider_id` that names a provider row. | "wt's typed decode and modelman's required-field and cost rules" (spec). Non-empty is wt's own rule for `id` and `model_name`. The two cross-row rules are Step 3's to add before `wt model add` and `wt model edit` ship (see "What Step 2 leaves for later steps"); Step 2's only caller adds provider rows. | Task 12, `TestUpdateRegistryValidatesOnlyTheRowsItTouched` |
| How many times does `apply` run before `ErrRegistryBusy`? | Three in total: the first run and two retries. | The spec's "retry `apply` up to three times" can be read as three runs or four; three bounds the work. | Task 11, `TestUpdateRegistryGivesUpAfterThreeAttempts` |
| When is the lock file created, and where? | After the test guard is asked, beside the path as named. It is never removed. | A guarded write must create nothing; a symlinked registry must get no lock file in its checkout. | Task 11, `TestUpdateRegistryAsksTheWriteGuardFirst`, `TestUpdateRegistryWritesThroughASymlink` |
| What does the test guard protect? | The registry path the environment named before `IsolateConfigHomeForTest` redirected it, the default path under the home directory, and what each resolves to through symlinks — the file's own link or a linked directory above it. A write is compared as named and with its own symlinks followed. The list only grows. | Tests set their own `XDG_CONFIG_HOME` and `HOME` freely; the guard must refuse only the developer's real file, however a test spells its path (a linked config home, `/tmp` for `/private/tmp`). | Task 11, `TestTestMainGuardsTheDevelopersRegistry`, `TestUpdateRegistryAsksTheWriteGuardFirst` |
| How does a test binary that reaches the writer show its guard is armed? | `config.RegistryWriteGuardArmed()`, asserted by a test in that binary. Today that is `cmd/wt`. | The guard is off unless a `TestMain` arms it, so a binary that forgot would fail open. | Task 15, `TestRegistryWriteGuardIsArmedHere` |
| Is a symlink loop `ErrRegistryLink`? | Yes, with "cannot be followed" in place of "does not exist". | It is a broken pointer, not an absent registry. | Task 1, `TestResolveRegistryFile` |
| What hint does a broken link get? | `fix the link or move it aside`, from `RegistryFixHint`. | The default hint says "run `wt config`", which cannot repair a symlink. | Task 1, `TestRegistryFixHintForABrokenLink` |
| Is an empty `WT_REGISTRY` "set"? | No, in all three readers; `MODELMAN_REGISTRY` is then read. | That is how `MODELMAN_REGISTRY` already behaves. | Tasks 3, 4, 5 (the precedence tests) |
| Does the pre-XDG fallback in the Python readers apply under `WT_REGISTRY`? | No. Either name "names the file outright". | Same rule `MODELMAN_REGISTRY` has; a scratch run must not read the real registry. | Task 4, `test_load_registry_does_not_fall_back_past_wt_registry`; Task 5, `test_read_path_does_not_fall_back_past_wt_registry` |
| How do Go test packages without an isolating `TestMain` stay safe? | `internal/config` gets a `TestMain`. The five test sites elsewhere that set `MODELMAN_REGISTRY` set `WT_REGISTRY` to `""` on the line before. | `WT_REGISTRY` outranks what those tests set. Giving `litellm`, `lifecycle` and `configeditor` an isolating `TestMain` changes what their tests see and is not needed. | Task 3, Steps 6 and 7 |
| What do the Python conftests do? | modelman: an autouse fixture deletes both names. llmbench: its fixture already pins `MODELMAN_REGISTRY` to a missing file; it now also deletes `WT_REGISTRY`. | "The test helpers in all three clear both names"; llmbench's pin is stronger than clearing and other tests rely on it. | Task 4, Task 5 |
| Which top-level keys does wt write into a new registry? | Only the ones it has rows for. No `families = []`, no `models = []`. | modelman loads a file without them and adds them on its next save; wt has no reason to. | Task 14, `TestSeedCreatesAMissingRegistry` |
| What does a default provider row hold? | Exactly what modelman writes: `protocols` is left out when it is `["openai-chat"]`. | `_provider_to_dict` drops it; both tools default to it. | Task 14, `TestSeedWritesTheRowsModelmanWrites` |
| Is modelman's `backfill_provider_defaults` ported? | No. Seeding appends rows and never edits one. | The spec's seeding paragraph lists additions only, and a user's own `base_url` must never be reset. | Task 14, `TestSeedIsIdempotentAndNeverEditsARow` |
| Which agents get a native provider row? | Those in `config.toml` as the next `Load` will see it, which adds `agy` whenever `config.toml` exists. | `migrateConfigSchema` adds an `agy` agent on the next `Load`; without the row that load fails validation. | Task 14, `TestSeedAgentNamesFollowWhatLoadWillValidate` |
| Which providers does an agent's `supported_providers` list seed? (the agent trigger; owner decision, 2026-10-07) | A default local provider (ollama, omlx, mlx_lm_server, mtplx) gets its default row, installed or not, and beside an `omlx-6bit` row too. `openrouter` gets a default row. Any other id gets no row and is reported. | `Config.Validate` refuses an agent that lists a provider with no row, so without this trigger `wt model init` leaves a fresh machine with a config error. modelman's seeding has no such trigger; the spec's Seeding section now does. If it is wrong, it is one trigger to remove from `SeedRegistryDefaults`. | Task 14, `TestSeedAddsOnlyWhatTheMachineNeeds`, `TestSeedMakesAFreshConfigLoad` |
| What is in the seeded `openrouter` row? | `id`, `name = "OpenRouter"`, `location = "cloud"`, `auth.type = "api_key"`, `auth.base_url = "https://openrouter.ai/api/v1"`. No `secret_ref` and no key. It is seeded only when an agent lists it, never for a model or from PATH. | wt's validation and modelman's loader need `id` and `auth.type`. `name` is there so modelman's next save does not add one. The address is public and is what a direct route dials; it is the row guide 02 and the contract fixture show, less the key. Where the key is kept is the user's to say, so seeding writes no secret and guesses no variable name. Until the user adds `secret_ref`, a LiteLLM route built for one of the provider's models carries an empty `api_key`. | Task 14, `TestSeedRowsForTheProvidersAgentsList` |
| What happens to a listed provider wt has no default row for? | No row is seeded. `SeedRegistryDefaults` returns it in `unseeded`, on every run; `wt model init` prints `no default row for provider: <id> (an agent lists it; add it to registry.toml by hand)` and still exits 0. | wt cannot know a custom provider's address or how it authenticates, and a guessed row is worse than a named gap. | Task 14, `TestSeedRowsForTheProvidersAgentsList`; Task 15, `TestModelInitCreatesTheRegistryAndSaysWhatItAdded` |
| How does seeding know what the next `Load` will validate? | `seedAgents` reads `config.toml` and applies `migrateAgentRefs` to that read. `migrateAgentRefs` is the agent half of `migrateConfigSchema` (google becomes agy, an `agy` agent is added, `opencode` lists `ollama` only), split out so it touches no file and prints nothing. Seeding writes nothing to `config.toml`. | `Load` migrates `config.toml` only once the registry loads, so on a fresh machine the file still has its old agent lists when `wt model init` runs. One function, so seeding cannot drift from the migration. | Task 14, `TestSeedMakesAFreshConfigLoad` |
| What is `SeedRegistryDefaults`'s signature? | `SeedRegistryDefaults(d *RegistryDoc, env SeedEnv) (added, unseeded []string, err error)`. `SeedEnv` carries the agent names, the provider ids those agents list and an `OnPath` function, gathered before the lock. | It has to run inside the caller's own `UpdateRegistry` (Step 3's `wt model add`), and `apply` must be pure. | Task 14, `TestSeedWritesTheRowsModelmanWrites` |
| What does bare `wt model` do until Step 3? | Prints the group's help; an unknown word under it is an error. | Step 3 makes it open the Models tab. | Task 15, `TestModelCommandGroup` |
| Does `wt model init` sync routes? | Once, only when it changed the registry. A sync that cannot run is a warning and exit 0. With no `config.yaml` at all it is skipped silently, without probing providers. | Spec, Step 3: a writing verb does one `UpdateRegistry` and then one route sync, and exits 0 when the write succeeded. | Task 15, `TestModelInitUnderARedirectedRegistry` |
| What does `wt model init --json` print? | One object: `registry`, `created`, `changed`, `providers_added`, `providers_unseeded`, `warnings`. Arrays are never null. | The spec names the flag, not the shape. | Task 15, `TestModelInitJSON` |
| What does `wt model init` print as text? | `registry: <path>`, with ` (created)` after it when the file is new; then one `added provider: <id>` line per row, or `nothing to add`; then one `no default row for provider: <id> (an agent lists it; add it to registry.toml by hand)` line per listed provider that could not be seeded. Warnings go to stderr. | The spec names the command, not its output. | Task 15, `TestModelInitCreatesTheRegistryAndSaysWhatItAdded` |
| Does the missing-registry line keep both hints? | No (owner decision, 2026-10-07). It names the command once: `… — seed it with `wt model init``. `loadRegistry`'s error carries the hint and `configError` adds none. | Today the line says the same thing twice (`— seed it with `modelman migrate` (seed the registry with `modelman migrate`)`). The spec now says the hint appears once. If that is wrong, it is one string in `configError`. | Task 16, `TestConfigErrorForAMissingRegistryNamesModelInit` |
| Does the modelman guard have to precede `wt model init`? | Yes: PR 6 merges before PR 5. | The spec says so: `wt model init` is a wt write that an open modelman TUI would revert, and PR 5's docs state modelman's refusal as fact. | The "Branches and PRs" table |
| When is modelman's TUI disabled? | In Step 3, by the PR series that ships wt's Models tab (owner decision, 2026-10-07): bare `modelman` then prints where the Models tab is and exits non-zero. Not in this step. | Disabling it now would leave no model editor until Step 3. Until then the guard (PR 6) covers an open TUI, at the price that it refuses every save after a wt write until it is restarted; Step 3 ends that. The guard keeps covering modelman's non-interactive writers until Step 6. | Not pinned here; it is Step 3's plan to pin |
| Where is modelman's snapshot record kept? | Per process, keyed by the resolved path. A path this process never looked at has no record and is never refused; a path it found absent is recorded as absent, and so is the path a save will write when the load fell back to the pre-XDG file. A file deleted since it was loaded counts as changed. | modelman's own background save (`app.py:138`) must not make the screen's next save look stale. A registry wt creates after modelman found none (the fresh-machine flow `wt model init` exists for) must not be overwritten by modelman's empty snapshot. | Task 17, `test_modelmans_own_saves_never_trip_the_guard`, `test_a_first_save_is_never_refused`, `test_a_registry_created_after_modelman_found_none_is_not_overwritten`, `test_the_pre_xdg_fallback_guards_the_path_a_save_writes` |
| What stops modelman's own background load from making an older snapshot look current? | A per-path count of loads that found the file changed by another program. Each loaded `Registry` remembers the count it was loaded at (`Registry._loaded_at`); a save is refused when the file differs from the record or the count has moved on. | The TUI's price refresh loads the file on a worker (`app.py:111`, `:138`). With the record alone, that load would accept wt's write on behalf of the screen's older snapshot, and the screen's next save would revert it. | Task 17, `test_a_background_load_does_not_bless_an_older_snapshot` |
| What is in modelman's stamp of the file? | `(mtime_ns, size, inode)`. | The spec names the three. wt replaces the file by rename, so the inode changes on every wt write; it catches a same-size write inside one tick of a coarse file clock (modelman-ci's filesystem). | Task 17, `test_a_replacement_with_the_same_size_and_time_is_caught` |
| What does a load do when the file was replaced while it was being read? | It leaves the record as it was. | The price-refresh worker loads without the lock; recording the file it opened, after the screen's save replaced it, would make every later save in that TUI look stale. | Task 17, `test_a_save_during_a_load_does_not_poison_the_record` |
| Do `modelman migrate` and `modelman ollama-catalog sync` report a refusal cleanly? | No: a refusal there is a traceback ending in the reload message. Their handlers are not widened. | modelman is frozen to bug fixes, and both windows are a few milliseconds (a registry created while `migrate` runs; a write between `locked_registry`'s own load and save). The TUI and the three CLI saves that already catch `RegistryError` report it in one line. | Not pinned (accepted) |
| What is in the written fixture, and how is it made? | Synthetic rows, produced with `tomli_w.dumps`. It has no comments, because tomli-w writes none; its purpose is recorded in its three tests. Unknown keys are at every level below the top; an unknown top-level key cannot be in it, because modelman's loader and wt's writer both refuse one (#247). That refusal is pinned by Task 10, `TestNewRegistryDocRefusesAnUnexpectedTopLevel`. | A hand-typed file would not be in tomli-w form. The spec's "unknown keys at every level" cannot include the top level without a fixture neither writer accepts. | Task 9, `test_tomli_w_reproduces_the_written_fixture` |
| How is "adding a field to `config.Model` cannot change what a write touches" pinned? | A test builds a row from the struct tags of `Model` and `ModelCost`, each key at its zero value, patches one unrelated key and checks the rest survive. | A field added later is covered without editing the test. | Task 10, `TestAWriteNeverTouchesAKeyItWasNotAskedTo` |

## What the Code Looks Like Today

Line numbers are as of `main` at `4a7e10a`. Each step also names the function or quotes the text, and that is what to match on.

| Where | What is there |
|---|---|
| `wt/internal/config/registry.go:23` | `func RegistryPath() string` — reads `MODELMAN_REGISTRY`, then `baseConfigHome()` |
| `wt/internal/config/registry.go:90` | `func loadRegistry() ([]Provider, []Model, error)` — `os.ReadFile` and `os.IsNotExist`, so a dangling symlink reads as `ErrRegistryMissing`; the hint `seed it with modelman migrate` is at line 95 |
| `wt/internal/config/registry.go:119` | `func RegistryRedirected() bool` — compares paths, so it needs no change for a new env name |
| `wt/internal/config/lock.go:20` | `func WithLock(fn func() error) error` — flock hard-wired to `config.toml.lock` |
| `wt/internal/config/fortest.go:20` | `func IsolateConfigHomeForTest() (home string, cleanup func())` — sets `XDG_CONFIG_HOME`, unsets `MODELMAN_REGISTRY`. Called by the `TestMain`s of `cmd/wt` and `internal/tui` only |
| `wt/internal/config/config.go:921` | `func RegistryFixHint(err error) string` — one case, `ErrLocation` |
| `wt/internal/config/config.go:1038` | `func WriteFileAtomic(path string, data []byte, perm os.FileMode) (err error)` — temp file in the same directory, chmod, rename. Renaming onto a symlink would replace the link, so the writer hands it the resolved target |
| `wt/internal/config/config.go:1108` | `func readConfigFile() (cfg *Config, exists bool, err error)` — `config.toml` only, no migration; seeding reads agent names through it |
| `wt/internal/config/config.go:466`, `:456` | `type Model struct`, `type ModelCost struct` — prices are `*float64`, `ModelInfo` is `map[string]any`; no `fetch`, `draft`, `pricing_updated_at` |
| `wt/internal/config/migrate.go:341` | `func migrateConfigSchema(cfg *Config) (bool, error)` — always ensures an `agy` agent |
| `wt/internal/litellm/configfile.go:36`, `:72` | `ErrRegistryRedirected`, `func checkRegistryPairing(o Options) error` — asked by the route writers, never by a registry write |
| `wt/cmd/wt/helpers.go:105` | `func configError(err error) error` — the hint `(seed the registry with modelman migrate)` is at line 107 |
| `wt/cmd/wt/main.go:234` | The removed-name guard: `wt models` errors with `wt models is removed; use wt config to view models`. It runs in the root `RunE`, so a real `model` subcommand is dispatched before it |
| `wt/cmd/wt/main.go:431` | `cmd.AddCommand(rotateCmd(a), …, profileCmd(a))` — where `modelCmd(a)` is added |
| `wt/cmd/wt/litellm.go:130` | `func runLitellmSync(out, errOut io.Writer, cfg *config.Config, asJSON, dryRun bool) error` — probes the inventory through the `probeInventory` seam before it opens `config.yaml` |
| `modelman/src/modelman/registry.py:115` | `def _default_registry_path() -> Path` |
| `modelman/src/modelman/registry.py:492` | `def _registry_read_path(path)` — the pre-XDG fallback at line 511 is skipped when `MODELMAN_REGISTRY` is set |
| `modelman/src/modelman/registry.py:532`, `:562` | `_TOP_LEVEL_KEYS = frozenset({"providers", "families", "models"})`; `def load_registry(path)` rejects any other top-level key |
| `modelman/src/modelman/registry.py:794`, `:829`, `:844` | `def _write_registry(registry, path)`, `def save_registry(registry, path=None)`, `def locked_registry(path=None)` — the lock (`_REGISTRY_LOCK`, line 826) is a `threading.Lock`, in-process only |
| `modelman/src/modelman/registry.py:581`, `:766`, `:593` | `_provider_to_dict`, `_model_to_dict`, `_cost_to_dict` — the key orders `RegistryDoc`'s schemas copy. `_cost_to_dict` iterates a `set` (`_COST_FIELDS`, line 67), so modelman's own cost key order varies between runs; that is why modelman's contract test below calls `tomli_w` directly and not `_write_registry` |
| `modelman/src/modelman/registry.py:283`, `:333` | `_DEFAULT_PROVIDER_TEMPLATES`, `def sync_agent_providers(registry, wt_config_path=None)` |
| `modelman/src/modelman/sync.py:132`, `:170` | `def _ensure_provider_entries(registry)`, `def backfill_provider_defaults(registry)` |
| `modelman/src/modelman/_toml_io.py:78` | `def atomic_write_toml(payload, path)` — `tomli_w.dump` to a `mkstemp` file, then `os.replace` |
| `modelman/src/modelman/screens/models.py:850` | `def _save_registry(self) -> bool` — already catches `RegistryError` and shows `Registry not saved: …` |
| `llmbench/src/llmbench/registry.py:97`, `:116` | `def registry_path() -> Path`, `def registry_read_path(path=None)` — the same fallback rule at line 131 |
| `modelman/tests/conftest.py` | No fixture touches the registry variables today |
| `llmbench/tests/conftest.py:125` | `def _no_real_config(monkeypatch, tmp_path)` — sets `MODELMAN_REGISTRY` to a missing file |
| `.github/workflows/llmbench-ci.yml` | Already triggers on `docs/contracts/registry*.toml`. `wt-ci` and `modelman-ci` trigger on `docs/contracts/**` |

A working prototype of the document and the emitter was written before this plan. The code in PR 3 is that prototype, restructured into a package and with one defect fixed (the first row of the Decisions table).

## File Structure

| File | Change | PR | Responsibility |
|---|---|---|---|
| `wt/internal/config/registry.go` | modify | 1, 2, 5 | 1: `ErrRegistryLink`, `resolveRegistryFile`, `loadRegistry` uses it. 2: `registryEnvNames`, `RegistryPath`. 5: hint text |
| `wt/internal/config/config.go` | modify | 1, 5 | 1: `RegistryFixHint` knows a broken link. 5: comments that call the registry modelman's alone |
| `wt/internal/configeditor/editor.go` | modify | 5 | The same comment fix |
| `wt/internal/config/lock.go` | modify | 1 | `withFileLock(lockPath, fn)`; `WithLock` calls it |
| `wt/internal/config/registry_link_test.go`, `lock_file_test.go` | create | 1 | Their tests |
| `wt/cmd/wt/helpers_test.go`, `main_test.go` | modify | 1, 5 | 1: broken-link error and launch gate. 5: missing-registry hint |
| `wt/internal/config/fortest.go` | modify | 2, 4 | 2: clears both names. 4: arms the write guard; `RegistryWriteGuardArmed` |
| `wt/internal/config/testmain_test.go`, `registry_env_test.go` | create | 2 | Package isolation; precedence tests |
| `wt/internal/{configeditor,lifecycle,litellm}/*_test.go`, `wt/cmd/wt/helpers_test.go` | modify | 2 | Blank `WT_REGISTRY` where `MODELMAN_REGISTRY` is set |
| `wt/internal/litellm/configfile.go`, `proxyenv.go` | modify | 2 | Comments name the new variable |
| `modelman/src/modelman/registry.py` | modify | 2, 6 | 2: `_registry_override`, precedence. 6: stale-snapshot guard |
| `modelman/tests/conftest.py`, `test_registry.py`, `test_registry_path_parity.py` | modify | 2 | Clear both names; precedence tests (Task 4); parity tests (Task 5, with llmbench's half) |
| `llmbench/src/llmbench/registry.py` | modify | 2 | `_registry_override`, precedence |
| `llmbench/tests/conftest.py`, `test_conftest_guards.py`, `test_registry.py` | modify | 2, 3 | 2: clear `WT_REGISTRY`; precedence test. 3: the written-fixture test |
| `wt/internal/tomlw/table.go`, `decode.go`, `encode.go` | create | 3 | Ordered table; decode with order recovery; tomli-w emitter |
| `wt/internal/tomlw/*_test.go` | create | 3 | Unit tests, goldens, the fixture fixed point |
| `docs/contracts/registry.written.sample.toml` | create | 3 | The writer's contract fixture |
| `modelman/tests/contracts/test_registry_written_fixture.py` | create | 3 | `tomli_w` reproduces the fixture; `load_registry` accepts it |
| `wt/internal/config/registry_fixture_test.go` | modify | 3 | wt's typed reader loads the fixture |
| `wt/internal/config/registry_doc.go` | create | 4 | `RegistryDoc` and its operations; the key-order schemas |
| `wt/internal/config/registry_write.go` | create | 4 | `UpdateRegistry`, `ErrRegistryBusy`, the two seams |
| `wt/internal/config/registry_validate.go` | create | 4 | `validateTouched`, `ErrRegistryInvalid` |
| `wt/internal/config/registry_doc_test.go`, `registry_write_test.go`, `registry_validate_test.go` | create | 4 | Their tests |
| `wt/internal/config/registry_seed.go`, `registry_seed_test.go` | create | 5 | `SeedRegistryDefaults`, `SeedEnv`, `DefaultSeedEnv` |
| `wt/internal/config/migrate.go` | modify | 5 | `migrateAgentRefs` split out of `migrateConfigSchema`, so seeding reads the agents as `Load` will |
| `wt/cmd/wt/model.go`, `model_test.go` | create | 5 | `wt model` group, `wt model init`, the `seedEnv` and `syncRoutesAfterWrite` seams |
| `wt/cmd/wt/main.go`, `helpers.go`, `testmain_test.go` | modify | 5 | Register the group; hint text; stub the two seams |
| `modelman/tests/test_registry_stale_snapshot.py` | create | 6 | The guard's tests |
| `wt/CLAUDE.md`, `wt/docs/internals/config-and-registry.md`, `wt/docs/internals/testing.md`, `wt/CHANGELOG.md` | modify | 1–5 | Each PR documents what it ships |
| `CLAUDE.md`, `docs/guides/00-config-map.md`, `02-providers-and-models.md`, `04-litellm-config.md`, `modelman/CLAUDE.md`, `modelman/README.md`, `modelman/docs/internals/registry-and-state.md`, `llmbench/CLAUDE.md`, `wt/README.md`, `wt/docs/configuration.md`, `wt/docs/wt-agents/shell-wt.md` | modify | 2, 3, 5, 6 | Env name; the fixture; ownership wording; the stale-snapshot guard |

## Branches and PRs

Six PRs, as the spec lists them. Each is one branch off an up-to-date `main`.

| PR | Tasks | Branch | Needs merged first |
|---|---|---|---|
| 1 | 1, 2 | `fix/wt-registry-symlink-read` | nothing |
| 2 | 3, 4, 5, 6 | `feat/registry-env-wt-registry` | nothing |
| 3 | 7, 8, 9 | `feat/wt-tomlw` | nothing |
| 4 | 10, 11, 12, 13 | `feat/wt-registry-writer` | PR 1 (`resolveRegistryFile`, `withFileLock`, `linkedRegistry` in the tests), PR 2 (`fortest.go`, the `internal/config` `TestMain`), PR 3 (`tomlw`, the fixture) |
| 5 | 14, 15, 16 | `feat/wt-model-init` | PR 4, PR 6 |
| 6 | 17 | `fix/modelman-stale-registry-snapshot` | nothing |

**What can run in parallel.** PRs 1, 2, 3 and 6 are independent of each other and can be built at the same time in separate worktrees. PR 1 and PR 2 both edit `wt/internal/config/registry.go`, in different functions; the two diffs merge with no conflict (checked with a three-way merge of the two results against `main`). Both add an entry to `wt/CHANGELOG.md`, which needs a hand merge in whichever lands second. PR 6 and PR 2 both edit `modelman/src/modelman/registry.py`, in different functions. PR 4 starts when 1, 2 and 3 have merged. PR 5 starts when 4 and 6 have merged: PR 5 ships wt's first registry write (`wt model init`) and its docs state modelman's refusal, so the guard must already be on `main`.

**Before PR 1:** this plan is on the branch `docs/modelman-retirement-step2-plan`, not on `main`. Merge that branch first, with the owner's OK for the push and the merge. Until it has merged, a branch cut from `main` does not have this file; read it with:

```bash
git show docs/modelman-retirement-step2-plan:docs/superpowers/plans/2026-10-07-modelman-retirement-step2-registry-writer.md
```

Start each branch from the remote's `main`. This form also works inside a git worktree, where `git switch main` fails because `main` is checked out elsewhere:

```bash
git fetch origin
git switch -c <branch> origin/main
```

**Verification at the end of every PR**, as its last task says:

```bash
cd wt && test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check
```

Expected: every package `ok`, and `make check` ends without an error. Then `make check && make test` in `modelman/` and in `llmbench/` when the PR touched them, and from the monorepo root:

```bash
make test-all
```

Expected: exit 0.

**A flaky test that is not this work's.** `TestEnsureModelRouteToSendsItsOutputToTheCaller` in `wt/internal/lifecycle` fails now and then on `main` itself, on the order of two lines written by two goroutines (seen once in a full run while this plan was written, and reproduced on an untouched `main` with `go test -count=200 -run TestEnsureModelRouteToSendsItsOutputToTheCaller ./internal/lifecycle`). If it is the only failure, run the suite again. Do not change it in these PRs; tell the owner.

**How this plan's code was checked.** Every listing below was built and run in a scratch copy of the repository at `4a7e10a` before it was written here: the Go suite, `make check`, both Python suites and `make test-all` passed on the six PRs applied together; PRs 1, 2 and 3 were also run alone on `main` (PR 2 in both its Go and its Python half, with modelman's suite passing at Task 4's commit and again after Task 5), PR 4 on top of those three, and PR 6 alone on `main` with modelman's whole suite. The emitter was compared against `tomli_w.dumps(tomllib.loads(x))` on 9,000 generated documents in tomli-w's own form, on every contract fixture, and on the prototype's private copies of a real registry (compared only; never printed), with no difference. A second run used 24,000 documents, half of them rewritten the way a person types TOML (dotted keys, comments, inline tables, both array forms): no output differed, and `Decode` refused four. Those four are the documents on which the decoder alone had lost a key without an error (the Decisions row on `Decode`).

**Correction, later on 2026-10-07.** The owner settled the TUI timing the other way: modelman's TUI is disabled in Step 2, in PR 5, not in Step 3. Where this plan says the TUI stays enabled until Step 3 (Global Constraints, the Decisions row "When is modelman's TUI disabled?", Task 17's prose and "What Step 2 leaves for later steps"), read the spec's "Interim: two writers" section instead. PR 5 gained one commit for it: bare `modelman` prints where to go and exits non-zero, with its test and the guide edits that follow from it.

**The amendment of 2026-10-07.** The owner's decisions after review (the agent trigger in seeding, the hint said once, modelman's TUI disabled in Step 3) changed Tasks 14, 15 and 16 and the prose of Task 17. Those three tasks were replayed from this file's own text, in order, on the combined tree with PR 5 taken back out: each red state and each green state was observed, then the Go suite, `make check`, `make test-all` and Task 16's throwaway-home run with the built binary. One thing was not replayed: the `wt/CHANGELOG.md` edit of Task 16 was not applied, because that tree does not carry the PR 2 changelog entry it is anchored on.

---

## PR 1 — a symlinked registry on the read path, and the lock helper

Branch `fix/wt-registry-symlink-read`. No writer yet: this PR fixes what wt reads today and lays down two pieces the writer needs (`resolveRegistryFile`, `withFileLock`).

Today `loadRegistry` calls `os.ReadFile` and treats `os.IsNotExist` as "no registry". A symlink whose target is gone gives the same error, so wt reports `model registry not found`, tells the user to seed one, and lets an unconfigured agent launch with no model routing. modelman fixed the same confusion in #248; this is wt's half.

### Task 1: A registry link that leads nowhere is not a missing registry

**Files:**
- Create: `wt/internal/config/registry_link_test.go`
- Modify: `wt/internal/config/registry.go` (new block above the `loadRegistry` comment, line 81; `loadRegistry`, lines 90-99)
- Modify: `wt/internal/config/config.go:915-926` (`RegistryFixHint`)
- Modify: `wt/cmd/wt/helpers_test.go`, `wt/cmd/wt/main_test.go` (one test appended to each)

**Interfaces:**
- Consumes: `config.RegistryPath() string`, `config.ErrRegistryMissing`, `config.Load() (*Config, error)`, `configError(err error) error` in `cmd/wt/helpers.go`.
- Produces:
  - `var ErrRegistryLink = errors.New("registry link is broken")` (package `config`)
  - `func resolveRegistryFile(path string) (target string, exists bool, err error)` (unexported; PR 4's writer calls it)
  - `RegistryFixHint(err)` returns `"fix the link or move it aside"` for an error that wraps `ErrRegistryLink`
  - test helper `linkedRegistry(t *testing.T) (link, target string)` in `registry_link_test.go` (PR 4's tests reuse it)

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/config/registry_link_test.go`:

```go
package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// linkedRegistry points the registry path at a symlink under a fresh config
// home and returns the link and the path it points at (which the caller
// creates, or leaves missing).
func linkedRegistry(t *testing.T) (link, target string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("MODELMAN_REGISTRY", "")
	link = filepath.Join(home, "local-ai", "registry.toml")
	target = filepath.Join(t.TempDir(), "dotfiles", "registry.toml")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	return link, target
}

// TestResolveRegistryFile pins the symlink rule the registry's reader and
// (from the next step) its writer share: a link that leads to a file resolves
// to that file, so a write can rename onto it and keep the link; a link that
// leads nowhere is ErrRegistryLink; only a path with nothing at it is
// "missing".
func TestResolveRegistryFile(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.toml")
	if err := os.WriteFile(real, []byte("models = []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolvedReal, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name, dest string) string {
		p := filepath.Join(dir, name)
		if err := os.Symlink(dest, p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	direct := mk("direct.toml", real)
	chained := mk("chained.toml", direct)
	relative := mk("relative.toml", "real.toml")
	dangling := mk("dangling.toml", filepath.Join(dir, "gone", "registry.toml"))
	loopA := filepath.Join(dir, "loop-a.toml")
	loopB := mk("loop-b.toml", loopA)
	mk("loop-a.toml", loopB)

	ok := []struct {
		name, path, want string
		exists           bool
	}{
		{"a regular file", real, real, true},
		{"nothing there", filepath.Join(dir, "absent.toml"), filepath.Join(dir, "absent.toml"), false},
		{"a link to a file", direct, resolvedReal, true},
		{"a link to a link", chained, resolvedReal, true},
		{"a relative link", relative, resolvedReal, true},
	}
	for _, c := range ok {
		t.Run(c.name, func(t *testing.T) {
			got, exists, err := resolveRegistryFile(c.path)
			if err != nil || got != c.want || exists != c.exists {
				t.Errorf("resolveRegistryFile = (%q, %v, %v), want (%q, %v, nil)", got, exists, err, c.want, c.exists)
			}
		})
	}
	for name, path := range map[string]string{"a dangling link": dangling, "a link loop": loopA} {
		t.Run(name, func(t *testing.T) {
			_, exists, err := resolveRegistryFile(path)
			if !errors.Is(err, ErrRegistryLink) || exists {
				t.Fatalf("resolveRegistryFile = (exists %v, %v), want ErrRegistryLink", exists, err)
			}
			if errors.Is(err, ErrRegistryMissing) {
				t.Error("a broken link must not read as a missing registry")
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error %q should name the link %s", err, path)
			}
		})
	}
	if _, _, err := resolveRegistryFile(dangling); !strings.Contains(err.Error(), filepath.Join(dir, "gone", "registry.toml")) || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("a dangling link's error should name its target and say it does not exist, got %q", err)
	}
}

// TestLoadReadsARegistryThroughASymlink pins the half of #248 that already
// worked: a registry linked into a dotfiles checkout loads like any other.
func TestLoadReadsARegistryThroughASymlink(t *testing.T) {
	_, target := linkedRegistry(t)
	if err := os.WriteFile(target, []byte(minimalRegistry), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].ID != "ollama/gemma4:9b" {
		t.Errorf("models = %+v, want the linked registry's one model", cfg.Models)
	}
}

// TestLoadRefusesADanglingRegistrySymlink pins the read side of #248 in wt: a
// registry link whose target is gone (an unmounted volume, a dotfiles checkout
// not cloned yet) is a broken pointer, not an absent registry. Read as
// "missing", an unconfigured agent launches with no model routing and the
// user is told to seed a registry they already have. Load must also return no
// Config: only a missing registry keeps the parsed agents.
func TestLoadRefusesADanglingRegistrySymlink(t *testing.T) {
	link, target := linkedRegistry(t)
	cfg, err := Load()
	if !errors.Is(err, ErrRegistryLink) {
		t.Fatalf("Load error = %v, want ErrRegistryLink", err)
	}
	if errors.Is(err, ErrRegistryMissing) {
		t.Error("a dangling link must not be reported as ErrRegistryMissing")
	}
	if cfg != nil {
		t.Errorf("Load returned a Config (%+v) for a broken registry link; want nil, as for any unreadable registry", cfg)
	}
	for _, want := range []string{link, target} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %s", err, want)
		}
	}
	if strings.Contains(err.Error(), "seed it") {
		t.Errorf("error %q tells the user to seed a registry they already have", err)
	}
	if _, statErr := os.Lstat(target); !os.IsNotExist(statErr) {
		t.Errorf("reading must not create the link's target; Lstat error = %v", statErr)
	}
}

// TestRegistryFixHintForABrokenLink pins the hint a broken registry link
// gets. Without it the commands that refuse to run on a config error append
// "run `wt config` to repair", and `wt config` cannot repair a symlink.
func TestRegistryFixHintForABrokenLink(t *testing.T) {
	linkedRegistry(t)
	_, err := Load()
	const want = "fix the link or move it aside"
	if got := RegistryFixHint(err); got != want {
		t.Errorf("hint = %q, want %q", got, want)
	}
}
```

`minimalRegistry` is the constant already defined in `registry_test.go`.

Append to `wt/cmd/wt/helpers_test.go`:

```go
// TestConfigErrorForABrokenRegistryLink pins what the commands that refuse
// to run print when registry.toml is a symlink to a file that is not there:
// the link, its target, and a hint about the link — not "seed the registry"
// (there is one, behind the link) and not "run `wt config`" (which cannot
// repair a symlink).
func TestConfigErrorForABrokenRegistryLink(t *testing.T) {
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	link := filepath.Join(home, ".config", "local-ai", "registry.toml")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "unmounted", "registry.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, loadErr := config.Load()
	if !errors.Is(loadErr, config.ErrRegistryLink) {
		t.Fatalf("Load error = %v, want config.ErrRegistryLink", loadErr)
	}
	got := configError(loadErr).Error()
	want := "config error: registry link is broken: " + link + " is a symlink to " + target +
		", which does not exist (fix the link or move it aside)"
	if got != want {
		t.Errorf("configError =\n  %q\nwant\n  %q", got, want)
	}
}
```

Append to `wt/cmd/wt/main_test.go`:

```go
// TestBrokenRegistryLinkIsNotAPassthrough pins the launch gate for a registry
// path that is a dangling symlink. A missing registry lets an unconfigured
// agent launch with no model routing; a broken link must not, because the
// user has a registry — it is just not reachable right now — and a silent
// native launch would bill their own account instead of the routed model.
func TestBrokenRegistryLinkIsNotAPassthrough(t *testing.T) {
	dir := initTestRepo(t)
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	link := filepath.Join(home, ".config", "local-ai", "registry.toml")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "unmounted", "registry.toml"), link); err != nil {
		t.Fatal(err)
	}

	var called bool
	oldLaunchPassthrough := launchPassthrough
	launchPassthrough = func(agent, worktreePath string, yolo bool, extraArgs []string, cfg *config.Config, pp *precomputedProfiles) error {
		called = true
		return nil
	}
	defer func() { launchPassthrough = oldLaunchPassthrough }()

	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"-A", "claude", "-W", "my-feature"})
	err := root.Execute()
	if !errors.Is(err, config.ErrRegistryLink) {
		t.Fatalf("err = %v, want config.ErrRegistryLink", err)
	}
	if called {
		t.Error("launchPassthrough was called for a broken registry link")
	}
}
```

Both files already import everything these two tests use (`bytes`, `errors`, `os`, `path/filepath`, `config`).

- [ ] **Step 2: Run the tests to verify they fail**

Run, from `wt/`:

```bash
go test ./internal/config -run 'TestResolveRegistryFile|TestLoad.*Symlink|TestRegistryFixHintForABrokenLink'
```

Expected: FAIL — the package does not build: `undefined: resolveRegistryFile`, `undefined: ErrRegistryLink`.

```bash
go test ./cmd/wt -run 'TestConfigErrorForABrokenRegistryLink|TestBrokenRegistryLinkIsNotAPassthrough'
```

Expected: FAIL — the package does not build: `undefined: config.ErrRegistryLink`.

- [ ] **Step 3: Add `ErrRegistryLink` and `resolveRegistryFile`**

In `wt/internal/config/registry.go`, add this block directly above the comment that begins `// loadRegistry decodes modelman-owned registry.toml` (it must not go next to `RegistryPath`, which PR 2 rewrites):

```go
// ErrRegistryLink is returned when the registry path is a symbolic link that
// cannot be followed to a file: its target does not exist, or the chain of
// links loops. It is deliberately not ErrRegistryMissing, which the same path
// looks like to os.ReadFile. A link is the user's pointer at where their
// registry lives — a dotfiles checkout, a file on a volume that is not mounted
// right now — so wt must neither run as if there were no registry (the
// unconfigured-agent passthrough) nor create a new file in the link's place,
// which would shadow the real registry when its target comes back (#248).
var ErrRegistryLink = errors.New("registry link is broken")

// resolveRegistryFile applies the symlink rule the registry's reader and
// writer share (#248) to path, the registry path as named:
//
//   - nothing there: (path, false, nil) — the registry is missing.
//   - a regular file: (path, true, nil).
//   - a symlink that leads to a file: (the file it leads to, true, nil), so a
//     writer renames onto the real file and the link survives.
//   - a symlink that leads nowhere: ErrRegistryLink, naming the link and what
//     it points at.
func resolveRegistryFile(path string) (target string, exists bool, err error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return path, false, nil
	}
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return path, true, nil
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, true, nil
	}
	dest, _ := os.Readlink(path)
	if os.IsNotExist(err) {
		return "", false, fmt.Errorf("%w: %s is a symlink to %s, which does not exist", ErrRegistryLink, path, dest)
	}
	return "", false, fmt.Errorf("%w: %s is a symlink to %s, which cannot be followed: %v", ErrRegistryLink, path, dest, err)
}
```

- [ ] **Step 4: Make `loadRegistry` use it**

In the same file, in `loadRegistry`, replace:

```go
	path := RegistryPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil, fmt.Errorf(
			"%w at %s — seed it with `modelman migrate`", ErrRegistryMissing, path)
	}
	if err != nil {
		return nil, nil, err
	}
```

with:

```go
	path := RegistryPath()
	missing := fmt.Errorf("%w at %s — seed it with `modelman migrate`", ErrRegistryMissing, path)
	target, exists, err := resolveRegistryFile(path)
	if err != nil {
		return nil, nil, err
	}
	if !exists {
		return nil, nil, missing
	}
	data, err := os.ReadFile(target)
	if os.IsNotExist(err) {
		// Removed between the check and the read.
		return nil, nil, missing
	}
	if err != nil {
		return nil, nil, err
	}
```

The hint text stays `modelman migrate` in this PR; PR 5 changes it. In the comment above `loadRegistry`, replace:

```go
// Fail-closed: a missing or malformed registry is an error — wt has no
// editor for this file; seed it once with `modelman migrate`.
```

with:

```go
// Fail-closed: a missing or malformed registry is an error — wt has no
// editor for this file; seed it once with `modelman migrate`. A symlinked
// registry is read through; a link that leads nowhere is ErrRegistryLink,
// never "missing" (resolveRegistryFile).
```

- [ ] **Step 5: Give the broken link its hint**

In `wt/internal/config/config.go`, replace the whole of `RegistryFixHint` and its comment:

```go
// RegistryFixHint is the hint for a config error whose repair is in
// registry.toml — modelman's file, which `wt config` cannot edit — or "" for
// any other error (and nil). Today that is a location error (ErrLocation). It
// is the one source of the wording, so the commands that refuse to run on such
// an error and the editor that opens on it name the same file the same way
// (#209).
func RegistryFixHint(err error) string {
	if errors.Is(err, ErrLocation) {
		return "fix the entry in " + RegistryPath()
	}
	return ""
}
```

with:

```go
// RegistryFixHint is the hint for a config error whose repair is in
// registry.toml — modelman's file, which `wt config` cannot edit — or "" for
// any other error (and nil). Today that is a location error (ErrLocation) and
// a registry path that is a broken symlink (ErrRegistryLink). It is the one
// source of the wording, so the commands that refuse to run on such an error
// and the editor that opens on it name the same file the same way (#209).
func RegistryFixHint(err error) string {
	switch {
	case errors.Is(err, ErrLocation):
		return "fix the entry in " + RegistryPath()
	case errors.Is(err, ErrRegistryLink):
		return "fix the link or move it aside"
	}
	return ""
}
```

`cmd/wt/helpers.go`'s `configError` needs no change: it already asks `ErrRegistryMissing` first and `RegistryFixHint` second, and a broken link is not `ErrRegistryMissing`. Nor does `cmd/wt/main.go:334`, which lets only `ErrRegistryMissing` through to the passthrough launch.

- [ ] **Step 6: Run the tests to verify they pass**

Run, from `wt/`:

```bash
go test -count=1 ./internal/config ./cmd/wt
```

Expected: both packages `ok`.

- [ ] **Step 7: Commit**

```bash
test -z "$(gofmt -l .)" && go vet ./...
git add internal/config/registry.go internal/config/config.go internal/config/registry_link_test.go cmd/wt/helpers_test.go cmd/wt/main_test.go
git commit -m "fix(wt): a registry link that leads nowhere is not a missing registry (#248)"
```

### Task 2: One flock helper, and the docs for PR 1

`WithLock` is a blocking flock on `config.toml.lock`. The registry writer needs the same thing on `registry.toml.lock`. This task splits the body out so both use one helper, and changes no behaviour.

**Files:**
- Create: `wt/internal/config/lock_file_test.go`
- Modify: `wt/internal/config/lock.go:20-35`
- Modify: `wt/docs/internals/config-and-registry.md`, `wt/CHANGELOG.md`

**Interfaces:**
- Consumes: nothing new.
- Produces: `func withFileLock(lockPath string, fn func() error) error` (unexported, package `config`). `WithLock(fn func() error) error` keeps its signature and its lock file.

- [ ] **Step 1: Write the failing test**

Create `wt/internal/config/lock_file_test.go`:

```go
package config

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestWithFileLockSerializesCallersOnOneLockFile pins the helper WithLock is
// now built on, with a lock path of the caller's choosing: the registry
// writer will take it on registry.toml.lock. Two holders at once would let
// two wt processes interleave a read-modify-write and lose an edit.
func TestWithFileLockSerializesCallersOnOneLockFile(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "nested", "registry.toml.lock")
	var mu sync.Mutex
	active, maxActive := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := withFileLock(lock, func() error {
				mu.Lock()
				active++
				maxActive = max(maxActive, active)
				mu.Unlock()
				time.Sleep(10 * time.Millisecond)
				mu.Lock()
				active--
				mu.Unlock()
				return nil
			})
			if err != nil {
				t.Errorf("withFileLock: %v", err)
			}
		}()
	}
	wg.Wait()
	if maxActive != 1 {
		t.Errorf("max concurrent holders = %d, want 1", maxActive)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Errorf("the lock file (and its directory) should have been created: %v", err)
	}
	sentinel := errors.New("from fn")
	if err := withFileLock(lock, func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Errorf("withFileLock should return fn's error, got %v", err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run, from `wt/`: `go test ./internal/config -run TestWithFileLockSerializesCallersOnOneLockFile`

Expected: FAIL — the package does not build: `undefined: withFileLock`.

- [ ] **Step 3: Split the helper out**

In `wt/internal/config/lock.go`, replace the function `WithLock` (keep the comment above it as it is):

```go
func WithLock(fn func() error) error {
	lockPath := Path() + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return err
	}
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return fn()
}
```

with:

```go
func WithLock(fn func() error) error {
	return withFileLock(Path()+".lock", fn)
}

// withFileLock runs fn holding a blocking exclusive flock on lockPath,
// creating the lock file and its directory when they are missing. It is the
// one flock helper in this package: WithLock uses it for config.toml, and the
// registry writer for registry.toml. The lock file is never removed —
// removing it would let a waiter lock a file a newcomer no longer sees.
func withFileLock(lockPath string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return err
	}
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return fn()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run, from `wt/`: `go test -count=1 ./internal/config`

Expected: `ok`. The existing `TestWithLockSerializesConcurrentCallers` in `lock_test.go` still passes: `WithLock` behaves as before.

- [ ] **Step 5: Document the symlink rule**

In `wt/docs/internals/config-and-registry.md`, replace:

```text
**Missing registry → unconfigured-agent passthrough.**
```

with:

```text
**A symlinked registry.** `resolveRegistryFile` (`internal/config/registry.go`) is the one place that decides what a registry path is (#248). A symlink that leads to a file is read through. A symlink that leads nowhere — its target is gone, or the links loop — is `config.ErrRegistryLink`, never `ErrRegistryMissing`: a link is the user's pointer at where their registry lives (a dotfiles checkout, a volume that is not mounted), so wt neither launches as if there were no registry nor offers to seed one. `RegistryFixHint` gives it the hint `fix the link or move it aside`. Only a path with nothing at it is "missing".

**Missing registry → unconfigured-agent passthrough.**
```

In `wt/CHANGELOG.md`, replace:

```text
### Fixed

- The `wt` picker says which omlx models a start unloaded (#258).
```

with:

```text
### Fixed

- A registry path that is a symlink to a file that is not there is reported
  as a broken link, naming the link and its target, instead of `model
  registry not found` (#248). An unconfigured agent no longer launches with no
  model routing in that case, and the hint no longer says to seed a registry
  that exists behind the link.
- The `wt` picker says which omlx models a start unloaded (#258).
```

- [ ] **Step 6: Verify the PR and commit**

Run, from `wt/`:

```bash
test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check
```

Expected: every package `ok`; `make check` ends without an error.

Run, from the monorepo root: `make test-all`

Expected: exit 0.

```bash
git add wt/internal/config/lock.go wt/internal/config/lock_file_test.go wt/docs/internals/config-and-registry.md wt/CHANGELOG.md
git commit -m "refactor(wt): one flock helper for config.toml and the registry"
```

Stop here. Pushing `fix/wt-registry-symlink-read` and opening the PR need the owner's OK.

---

## PR 2 — `WT_REGISTRY` in the three readers

Branch `feat/registry-env-wt-registry`. One PR for all three tools, because a name only one of them reads is a split registry: wt would write one file while modelman and llmbench read another. Each reader gets a precedence test, and each test helper stops a `WT_REGISTRY` inherited from the developer's shell from outranking what a test sets.

### Task 3: wt reads `WT_REGISTRY` first

**Files:**
- Create: `wt/internal/config/registry_env_test.go`, `wt/internal/config/testmain_test.go`
- Modify: `wt/internal/config/registry.go:18-37` (`RegistryPath`), and three comments in the same file
- Modify: `wt/internal/config/fortest.go:12-13`, `:26`
- Modify: `wt/cmd/wt/helpers_test.go` (`withCleanConfigEnv`), `wt/internal/configeditor/editor_test.go`, `wt/internal/configeditor/save_test.go`, `wt/internal/lifecycle/routes_test.go`, `wt/internal/litellm/service_test.go`
- Modify: `wt/internal/litellm/configfile.go:66`, `wt/internal/litellm/proxyenv.go:53` (one comment each)

**Interfaces:**
- Consumes: `expandHome(path string) (string, error)`, `baseConfigHome() string`.
- Produces:
  - `var registryEnvNames = []string{"WT_REGISTRY", "MODELMAN_REGISTRY"}` (unexported, package `config`)
  - `RegistryPath()` — same signature, new precedence
  - `IsolateConfigHomeForTest()` — same signature; unsets both names
  - `internal/config` has a `TestMain` (PR 4 relies on it to arm the write guard)

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/config/registry_env_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRegistryPathPrecedence pins which file is the registry: WT_REGISTRY,
// then MODELMAN_REGISTRY, then $XDG_CONFIG_HOME/local-ai, then ~/.config.
// modelman (test_registry.py) and llmbench (test_registry.py) pin the same
// order. If wt alone resolved a different file, a scratch run would have wt
// write one registry while the Python tools read another.
func TestRegistryPathPrecedence(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	cases := []struct {
		name, wt, modelman, xdg, want string
	}{
		{"nothing set", "", "", "", filepath.Join(home, ".config", "local-ai", "registry.toml")},
		{"XDG_CONFIG_HOME", "", "", "/custom/xdg", "/custom/xdg/local-ai/registry.toml"},
		{"MODELMAN_REGISTRY beats XDG", "", "/old/registry.toml", "/custom/xdg", "/old/registry.toml"},
		{"WT_REGISTRY beats MODELMAN_REGISTRY", "/new/registry.toml", "/old/registry.toml", "/custom/xdg", "/new/registry.toml"},
		{"WT_REGISTRY alone", "/new/registry.toml", "", "", "/new/registry.toml"},
		{"WT_REGISTRY expands a tilde", "~/scratch/registry.toml", "/old/registry.toml", "", filepath.Join(home, "scratch", "registry.toml")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("WT_REGISTRY", c.wt)
			t.Setenv("MODELMAN_REGISTRY", c.modelman)
			t.Setenv("XDG_CONFIG_HOME", c.xdg)
			if got := RegistryPath(); got != c.want {
				t.Errorf("RegistryPath() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestRegistryRedirectedSeesWTRegistry pins that the new name counts as a
// redirect. The LiteLLM route writers ask RegistryRedirected before touching
// config.yaml; if WT_REGISTRY were invisible to it, a scratch registry named
// the new way would be reconciled onto the developer's real proxy config.
func TestRegistryRedirectedSeesWTRegistry(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("MODELMAN_REGISTRY", "")
	t.Setenv("WT_REGISTRY", filepath.Join(home, "scratch", "registry.toml"))
	if !RegistryRedirected() {
		t.Error("WT_REGISTRY naming another file should count as a redirected registry")
	}
	t.Setenv("WT_REGISTRY", "~/.config/local-ai/registry.toml")
	if RegistryRedirected() {
		t.Error("WT_REGISTRY spelling the default path is not a redirect")
	}
}

// TestIsolateConfigHomeForTestClearsBothRegistryNames pins the test helper
// every isolating TestMain relies on: after it runs, neither registry
// variable is set and the registry resolves under the throwaway home. A
// developer with WT_REGISTRY exported would otherwise have every wt test run
// read (and, once wt writes, write) their real registry.
func TestIsolateConfigHomeForTestClearsBothRegistryNames(t *testing.T) {
	// t.Setenv restores all three after the test; the helper's own
	// os.Setenv/os.Unsetenv calls are then undone with them.
	t.Setenv("XDG_CONFIG_HOME", "/somewhere/else")
	t.Setenv("WT_REGISTRY", "/dev/real/registry.toml")
	t.Setenv("MODELMAN_REGISTRY", "/dev/real/registry.toml")

	home, cleanup := IsolateConfigHomeForTest()
	defer cleanup()

	for _, name := range []string{"WT_REGISTRY", "MODELMAN_REGISTRY"} {
		if v, set := os.LookupEnv(name); set {
			t.Errorf("%s is still set (%q)", name, v)
		}
	}
	if got, want := RegistryPath(), filepath.Join(home, "local-ai", "registry.toml"); got != want {
		t.Errorf("RegistryPath() = %q, want %q", got, want)
	}
	if !strings.Contains(home, "wt-test-config-") {
		t.Errorf("home = %q, want the throwaway directory", home)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run, from `wt/`:

```bash
go test ./internal/config -run 'TestRegistryPathPrecedence|TestRegistryRedirectedSeesWTRegistry|TestIsolateConfigHomeForTestClearsBothRegistryNames'
```

Expected: FAIL. Three subtests of `TestRegistryPathPrecedence` fail (`WT_REGISTRY_beats_MODELMAN_REGISTRY`: `RegistryPath() = "/old/registry.toml", want "/new/registry.toml"`), `TestRegistryRedirectedSeesWTRegistry` fails with `WT_REGISTRY naming another file should count as a redirected registry`, and `TestIsolateConfigHomeForTestClearsBothRegistryNames` fails with `WT_REGISTRY is still set ("/dev/real/registry.toml")`.

- [ ] **Step 3: Read both names in `RegistryPath`**

In `wt/internal/config/registry.go`, replace the comment and function `RegistryPath` (from `// RegistryPath returns the modelman-owned registry.toml location.` through the function's closing brace) with:

```go
// registryEnvNames are the variables that name registry.toml outright, in
// precedence order. WT_REGISTRY is the name wt, llmbench and modelman share;
// MODELMAN_REGISTRY is the older name, kept as an alias.
var registryEnvNames = []string{"WT_REGISTRY", "MODELMAN_REGISTRY"}

// RegistryPath returns the registry.toml location: WT_REGISTRY, then
// MODELMAN_REGISTRY, then $XDG_CONFIG_HOME/local-ai/registry.toml, then
// ~/.config/local-ai/registry.toml. modelman's _default_registry_path and
// llmbench's registry_path use the same precedence, so the three tools agree
// on which file is the registry; each has a test of it.
func RegistryPath() string {
	// A named registry is the only branch with a side effect: it writes to
	// stderr on expandHome failure. Acceptable because the path-resolution
	// failure must be visible to the user, and there is no logger to inject
	// at this layer. See expandHome's docstring for the literal-fallback contract.
	for _, name := range registryEnvNames {
		override := os.Getenv(name)
		if override == "" {
			continue
		}
		expanded, err := expandHome(override)
		if err != nil {
			fmt.Fprintf(os.Stderr, "wt: cannot expand %s (%v); using literal path\n", name, err)
			return override
		}
		return expanded
	}
	return filepath.Join(baseConfigHome(), "local-ai", "registry.toml")
}
```

Three comments in the same file name only the old variable. Replace each:

```go
// tilde expansion), falling back to ~/.config. It honors NEITHER
// MODELMAN_REGISTRY NOR modelman's MODELMAN_STATE override — a deliberate
```

with:

```go
// tilde expansion), falling back to ~/.config. It honors NEITHER
// WT_REGISTRY or MODELMAN_REGISTRY NOR modelman's MODELMAN_STATE override — a deliberate
```

```go
// modelman's _default_registry_path so MODELMAN_REGISTRY behaves the same
// in both tools. Paths that don't start with "~" are returned unchanged.
```

with:

```go
// modelman's _default_registry_path so WT_REGISTRY and MODELMAN_REGISTRY
// behave the same in both tools. Paths that don't start with "~" are returned unchanged.
```

```go
// somewhere other than the default ~/.config/local-ai/registry.toml —
// MODELMAN_REGISTRY or XDG_CONFIG_HOME naming another place. LiteLLM's
```

with:

```go
// somewhere other than the default ~/.config/local-ai/registry.toml —
// WT_REGISTRY, MODELMAN_REGISTRY or XDG_CONFIG_HOME naming another place. LiteLLM's
```

`RegistryRedirected` itself does not change: it compares paths, not variables.

Five comments outside that file name only the old variable too. Each "replace" line occurs once in its file.

In `wt/internal/litellm/configfile.go`, replace:

```go
// registry follows MODELMAN_REGISTRY and XDG_CONFIG_HOME, config.yaml only
```

with:

```go
// registry follows WT_REGISTRY, MODELMAN_REGISTRY and XDG_CONFIG_HOME, config.yaml only
```

In `wt/internal/litellm/proxyenv.go`, replace:

```go
// MODELMAN_REGISTRY uses); on an error there the literal path stays, which
```

with:

```go
// WT_REGISTRY uses); on an error there the literal path stays, which
```

In `wt/internal/litellm/service_test.go`, replace:

```go
// MODELMAN_REGISTRY and XDG_CONFIG_HOME; config.yaml follows neither. So a run
```

with:

```go
// WT_REGISTRY, MODELMAN_REGISTRY and XDG_CONFIG_HOME; config.yaml follows none of them. So a run
```

In `wt/cmd/wt/helpers_test.go`, in the comment above `withCleanConfigEnv`, replace:

```go
// inherited MODELMAN_REGISTRY so RegistryPath() cannot short-circuit on the
```

with:

```go
// inherited WT_REGISTRY or MODELMAN_REGISTRY so RegistryPath() cannot short-circuit on the
```

and replace:

```go
// `export MODELMAN_REGISTRY=...` in the dev's env makes the test read their
```

with:

```go
// `export WT_REGISTRY=...` in the dev's env makes the test read their
```

- [ ] **Step 4: Clear both names in the test helper**

In `wt/internal/config/fortest.go`, replace:

```go
// MODELMAN_REGISTRY is cleared for the same reason: it would send Load to the
// developer's registry whatever XDG says. A test that sets its own
```

with:

```go
// WT_REGISTRY and MODELMAN_REGISTRY are cleared for the same reason: either
// would send Load to the developer's registry whatever XDG says. A test that sets its own
```

and replace:

```go
	os.Unsetenv("MODELMAN_REGISTRY")
```

with:

```go
	for _, name := range registryEnvNames {
		os.Unsetenv(name)
	}
```

- [ ] **Step 5: Isolate the `internal/config` test binary**

`internal/config` has no `TestMain`. Most of its tests set `XDG_CONFIG_HOME` or `MODELMAN_REGISTRY` and expect that to decide the registry path; an inherited `WT_REGISTRY` now outranks both. Create `wt/internal/config/testmain_test.go`:

```go
package config

import (
	"os"
	"testing"
)

// TestMain gives this package's tests a throwaway config home and clears both
// registry variables (IsolateConfigHomeForTest). Most tests here set their own
// XDG_CONFIG_HOME or MODELMAN_REGISTRY and still win; what this stops is a
// WT_REGISTRY exported in the developer's shell, which outranks both and would
// otherwise send every one of those tests to the developer's real registry.
func TestMain(m *testing.M) {
	_, cleanup := IsolateConfigHomeForTest()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
```

- [ ] **Step 6: Blank `WT_REGISTRY` where a test sets `MODELMAN_REGISTRY` without an isolating `TestMain`**

Six one-line additions. In each, add the `WT_REGISTRY` line shown; the line beside it is already there.

`wt/cmd/wt/helpers_test.go`, in `withCleanConfigEnv`:

```go
	t.Setenv("MODELMAN_REGISTRY", "")
	t.Setenv("WT_REGISTRY", "")
}
```

`wt/internal/configeditor/editor_test.go`, at both places that set `MODELMAN_REGISTRY` (lines 205 and 235):

```go
	t.Setenv("WT_REGISTRY", "")
	t.Setenv("MODELMAN_REGISTRY", "/tmp/somewhere/registry.toml")
```

`wt/internal/configeditor/save_test.go` (line 178):

```go
	t.Setenv("WT_REGISTRY", "")
	t.Setenv("MODELMAN_REGISTRY", "")
```

`wt/internal/lifecycle/routes_test.go` (line 1121):

```go
	t.Setenv("WT_REGISTRY", "")
	t.Setenv("MODELMAN_REGISTRY", filepath.Join(home, "scratch", "registry.toml"))
```

`wt/internal/litellm/service_test.go` (line 1289):

```go
	t.Setenv("WT_REGISTRY", "")
	t.Setenv("MODELMAN_REGISTRY", home+"/scratch/registry.toml")
```

- [ ] **Step 7: Run the suite, then run it again with `WT_REGISTRY` inherited**

Run, from `wt/`:

```bash
test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./...
```

Expected: every package `ok`.

```bash
WT_REGISTRY=/nonexistent/inherited/registry.toml go test -count=1 ./...
```

Expected: every package `ok`. A failure here names a test that an exported `WT_REGISTRY` hijacks; give its package the Step 6 treatment.

- [ ] **Step 8: Commit**

```bash
git add internal/config/registry.go internal/config/fortest.go internal/config/testmain_test.go internal/config/registry_env_test.go cmd/wt/helpers_test.go internal/configeditor/editor_test.go internal/configeditor/save_test.go internal/lifecycle/routes_test.go internal/litellm/service_test.go internal/litellm/configfile.go internal/litellm/proxyenv.go
git commit -m "feat(wt): WT_REGISTRY names the registry, ahead of MODELMAN_REGISTRY"
```

### Task 4: modelman reads `WT_REGISTRY` first

**Files:**
- Modify: `modelman/src/modelman/registry.py:115-126` (`_default_registry_path`), `:508-511` (`_registry_read_path`)
- Modify: `modelman/tests/conftest.py`, `modelman/tests/test_registry.py`

**Interfaces:**
- Consumes: nothing new.
- Produces: `_REGISTRY_ENV_NAMES = ("WT_REGISTRY", "MODELMAN_REGISTRY")`, `def _registry_override() -> str | None` (module `modelman.registry`). `_default_registry_path()` and `_registry_read_path()` keep their signatures.

`modelman/tests/test_registry_path_parity.py` compares modelman's reader with llmbench's, so its new tests need both halves. They are written in Task 5, with llmbench's change; this task leaves that file alone and ends with modelman's suite passing.

- [ ] **Step 1: Write the failing tests**

In `modelman/tests/test_registry.py`, add these three tests directly after `test_default_registry_path_modelman_registry_override_wins`:

```python
def test_default_registry_path_precedence(monkeypatch, tmp_path):
    """WT_REGISTRY > MODELMAN_REGISTRY > XDG_CONFIG_HOME > ~/.config: the
    order wt's config.RegistryPath and llmbench's registry_path use. wt writes
    the registry, so a modelman that resolved another file under WT_REGISTRY
    would show (and save over) a registry wt is not editing."""
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    monkeypatch.delenv("XDG_CONFIG_HOME", raising=False)
    assert _default_registry_path() == tmp_path / "home/.config/local-ai/registry.toml"
    monkeypatch.setenv("XDG_CONFIG_HOME", "/custom/xdg")
    assert _default_registry_path() == Path("/custom/xdg/local-ai/registry.toml")
    monkeypatch.setenv("MODELMAN_REGISTRY", "/old/registry.toml")
    assert _default_registry_path() == Path("/old/registry.toml")
    monkeypatch.setenv("WT_REGISTRY", "~/new/registry.toml")
    assert _default_registry_path() == tmp_path / "home/new/registry.toml"
    # An empty value is "not set", as it is for MODELMAN_REGISTRY.
    monkeypatch.setenv("WT_REGISTRY", "")
    assert _default_registry_path() == Path("/old/registry.toml")
```

```python
def test_load_registry_does_not_fall_back_past_wt_registry(tmp_path, monkeypatch):
    # WT_REGISTRY names the registry outright, exactly as MODELMAN_REGISTRY
    # does: a missing file there is "not found", never the one in ~/.config.
    home = tmp_path / "home"
    monkeypatch.setenv("HOME", str(home))
    monkeypatch.setenv("WT_REGISTRY", str(tmp_path / "elsewhere" / "registry.toml"))
    save_registry(
        Registry(
            providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))]
        ),
        home / ".config" / "local-ai" / "registry.toml",
    )
    with pytest.raises(RegistryNotFoundError):
        load_registry()
```

```python
def test_conftest_clears_both_registry_names():
    """The autouse fixture must leave neither name set, or a WT_REGISTRY
    exported in the developer's shell outranks every test's MODELMAN_REGISTRY."""
    assert "WT_REGISTRY" not in os.environ
    assert "MODELMAN_REGISTRY" not in os.environ
```

That test reads `os.environ`, and the file does not import `os` yet. Add `import os` above `from pathlib import Path` at the top of the file.

- [ ] **Step 2: Run them to verify they fail**

Run, from `modelman/`: `uv run pytest -q tests/test_registry.py`

Expected: 2 failed. `test_default_registry_path_precedence` fails with `AssertionError: assert PosixPath('/old/registry.toml') == …/home/new/registry.toml`, and `test_load_registry_does_not_fall_back_past_wt_registry` with `DID NOT RAISE`.

`test_conftest_clears_both_registry_names` passes in a shell that exports neither name, so run the file once more the way the fixture is for:

```bash
WT_REGISTRY=/nonexistent/x.toml uv run pytest -q tests/test_registry.py
```

Expected: 3 failed — the same two, and `test_conftest_clears_both_registry_names` with `AssertionError` on its first line.

- [ ] **Step 3: Read both names**

In `modelman/src/modelman/registry.py`, replace the function `_default_registry_path`:

```python
def _default_registry_path() -> Path:
    """Compute the registry path lazily so env overrides work in tests.

    Precedence: MODELMAN_REGISTRY > XDG_CONFIG_HOME > ~/.config. This must
    stay in sync with wt's config.RegistryPath (wt reads the
    registry read-only and has no independent default).
    """
    override = os.environ.get("MODELMAN_REGISTRY")
    if override:
        return Path(override).expanduser()
    base = os.environ.get("XDG_CONFIG_HOME") or "~/.config"
    return Path(base, "local-ai", "registry.toml").expanduser()
```

with:

```python
# The variables that name registry.toml outright, in precedence order.
# WT_REGISTRY is the name wt, llmbench and modelman share; MODELMAN_REGISTRY is
# the older name, kept as an alias.
_REGISTRY_ENV_NAMES = ("WT_REGISTRY", "MODELMAN_REGISTRY")


def _registry_override() -> str | None:
    """The registry path the environment names outright, or None."""
    for name in _REGISTRY_ENV_NAMES:
        value = os.environ.get(name)
        if value:
            return value
    return None


def _default_registry_path() -> Path:
    """Compute the registry path lazily so env overrides work in tests.

    Precedence: WT_REGISTRY > MODELMAN_REGISTRY > XDG_CONFIG_HOME > ~/.config.
    This must stay in sync with wt's config.RegistryPath and llmbench's
    registry_path: wt writes the registry and all three read it, so they must
    agree on which file it is.
    """
    override = _registry_override()
    if override:
        return Path(override).expanduser()
    base = os.environ.get("XDG_CONFIG_HOME") or "~/.config"
    return Path(base, "local-ai", "registry.toml").expanduser()
```

In `_registry_read_path`, replace:

```python
    # Not past MODELMAN_REGISTRY: that names the file outright, and a
    # missing one there is missing, not "use the one in ~/.config".
    legacy = Path("~/.config/local-ai/registry.toml").expanduser()
    if path is None and not os.environ.get("MODELMAN_REGISTRY") and registry_path != legacy:
```

with:

```python
    # Not past WT_REGISTRY or MODELMAN_REGISTRY: either names the file
    # outright, and a missing one there is missing, not "use the one in
    # ~/.config".
    legacy = Path("~/.config/local-ai/registry.toml").expanduser()
    if path is None and not _registry_override() and registry_path != legacy:
```

- [ ] **Step 4: Clear both names for every modelman test**

modelman's conftest has no fixture for the registry variables: its tests set `MODELMAN_REGISTRY` themselves, in 67 places. An inherited `WT_REGISTRY` would now outrank every one of them. In `modelman/tests/conftest.py`, add this fixture directly above the line `@pytest.fixture(autouse=True)` that decorates `_default_litellm_config` — above the decorator, not between it and the `def`, which would take the decorator away from `_default_litellm_config` — and follow it with two blank lines:

```python
@pytest.fixture(autouse=True)
def _no_inherited_registry_override(monkeypatch):
    """Clear both names that point modelman at a registry outright.

    WT_REGISTRY outranks MODELMAN_REGISTRY, so a developer who exports it (to
    aim wt at a scratch registry) would otherwise send every test that sets
    MODELMAN_REGISTRY to that scratch file instead of the test's own. Clearing
    MODELMAN_REGISTRY too makes the starting point the same on every machine.
    A test that sets either name still wins.
    """
    monkeypatch.delenv("WT_REGISTRY", raising=False)
    monkeypatch.delenv("MODELMAN_REGISTRY", raising=False)
```

- [ ] **Step 5: Run the tests**

Run, from `modelman/`:

```bash
uv run pytest -q tests/test_registry.py
WT_REGISTRY=/nonexistent/x.toml uv run pytest -q tests/test_registry.py
```

Expected: all pass, both times.

Run, from `modelman/`: `make check && make test`

Expected: ruff and mypy clean; every test passes. `tests/test_registry_path_parity.py` is unchanged and still passes: llmbench's reader does not know `WT_REGISTRY` yet, and no test there sets it.

- [ ] **Step 6: Commit**

```bash
git add modelman/src/modelman/registry.py modelman/tests/conftest.py modelman/tests/test_registry.py
git commit -m "feat(modelman): WT_REGISTRY names the registry, ahead of MODELMAN_REGISTRY"
```

### Task 5: llmbench reads `WT_REGISTRY` first

**Files:**
- Modify: `llmbench/src/llmbench/registry.py:97-106` (`registry_path`), `:128-131` (`registry_read_path`)
- Modify: `llmbench/tests/conftest.py:125-129`, `llmbench/tests/test_conftest_guards.py:102`, `llmbench/tests/test_registry.py`
- Modify: `modelman/tests/test_registry_path_parity.py` (the test that compares modelman's reader with llmbench's)

**Interfaces:**
- Consumes: nothing new.
- Produces: `_REGISTRY_ENV_NAMES`, `def _registry_override() -> str | None` (module `llmbench.registry`). `registry_path()` and `registry_read_path()` keep their signatures.

- [ ] **Step 1: Write the failing tests**

In `llmbench/tests/test_registry.py`, in the `home` fixture, add the `WT_REGISTRY` line:

```python
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    monkeypatch.delenv("WT_REGISTRY", raising=False)
    monkeypatch.delenv("MODELMAN_REGISTRY", raising=False)
```

Replace the whole of `test_registry_path_precedence` with:

```python
def test_registry_path_precedence(home, monkeypatch, tmp_path):
    """WT_REGISTRY > MODELMAN_REGISTRY > XDG_CONFIG_HOME > ~/.config: the
    precedence wt and modelman use, so the three tools never read three
    different files. With WT_REGISTRY ignored here, a benchmark run against a
    scratch registry would isolate and measure the real one's models."""
    assert registry_path() == home / ".config" / "local-ai" / "registry.toml"
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    assert registry_path() == tmp_path / "xdg" / "local-ai" / "registry.toml"
    monkeypatch.setenv("MODELMAN_REGISTRY", str(tmp_path / "named.toml"))
    assert registry_path() == tmp_path / "named.toml"
    monkeypatch.setenv("WT_REGISTRY", "~/wt-named.toml")
    assert registry_path() == home / "wt-named.toml"
    # An empty value is "not set", as it is for MODELMAN_REGISTRY.
    monkeypatch.setenv("WT_REGISTRY", "")
    assert registry_path() == tmp_path / "named.toml"
```

and add this test directly after `test_read_path_does_not_fall_back_past_a_named_registry`:

```python
def test_read_path_does_not_fall_back_past_wt_registry(home, monkeypatch, tmp_path):
    """WT_REGISTRY names the file outright, exactly as MODELMAN_REGISTRY does:
    a missing one is missing, never "use the one in ~/.config"."""
    _write(home / ".config" / "local-ai" / "registry.toml")
    monkeypatch.setenv("WT_REGISTRY", str(tmp_path / "scratch.toml"))
    with pytest.raises(RegistryError, match="Registry file not found: .*scratch.toml"):
        registry_read_path()
```

In `modelman/tests/test_registry_path_parity.py`, in the `home` fixture, add the `WT_REGISTRY` line:

```python
    monkeypatch.delenv("WT_REGISTRY", raising=False)
    monkeypatch.delenv("MODELMAN_REGISTRY", raising=False)
    monkeypatch.delenv("XDG_CONFIG_HOME", raising=False)
    return tmp_path / "home"
```

and append these two tests to the end of the file:

```python
def test_both_read_the_registry_wt_registry_names(home, monkeypatch, tmp_path):
    _write(home / ".config" / "local-ai" / "registry.toml")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(_write(tmp_path / "old.toml")))
    named = _write(tmp_path / "new.toml")
    monkeypatch.setenv("WT_REGISTRY", str(named))
    assert _both_read() == named
```

```python
def test_neither_falls_back_past_wt_registry(home, monkeypatch, tmp_path):
    _write(home / ".config" / "local-ai" / "registry.toml")
    monkeypatch.setenv("WT_REGISTRY", str(tmp_path / "scratch.toml"))
    with pytest.raises(modelman_registry.RegistryNotFoundError):
        modelman_registry._registry_read_path()
    with pytest.raises(bench_registry.RegistryError, match="Registry file not found"):
        bench_registry.registry_read_path()
```

- [ ] **Step 2: Run them to verify they fail**

Run, from `llmbench/`: `uv run pytest -q tests/test_registry.py`

Expected: 2 failed. `test_registry_path_precedence` fails with `AssertionError: assert …/named.toml == …/home/wt-named.toml`, and `test_read_path_does_not_fall_back_past_wt_registry` with `DID NOT RAISE`.

Run, from `modelman/`: `uv run pytest -q tests/test_registry_path_parity.py`

Expected: 2 failed, both on llmbench's half (modelman's was done in Task 4). `test_both_read_the_registry_wt_registry_names` fails in `_both_read` with `AssertionError: assert PosixPath('…/old.toml') == PosixPath('…/new.toml')`: llmbench still reads the file `MODELMAN_REGISTRY` names. `test_neither_falls_back_past_wt_registry` fails with `DID NOT RAISE RegistryError`.

- [ ] **Step 3: Read both names**

In `llmbench/src/llmbench/registry.py`, replace the function `registry_path`:

```python
def registry_path() -> Path:
    """Where the registry lives: MODELMAN_REGISTRY > XDG_CONFIG_HOME > ~/.config.

    The same precedence as wt's config.RegistryPath and modelman's
    _default_registry_path."""
    override = os.environ.get("MODELMAN_REGISTRY")
    if override:
        return Path(override).expanduser()
    base = os.environ.get("XDG_CONFIG_HOME") or "~/.config"
    return Path(base, "local-ai", "registry.toml").expanduser()
```

with:

```python
# The variables that name registry.toml outright, in precedence order.
# WT_REGISTRY is the name wt, modelman and llmbench share; MODELMAN_REGISTRY is
# the older name, kept as an alias.
_REGISTRY_ENV_NAMES = ("WT_REGISTRY", "MODELMAN_REGISTRY")


def _registry_override() -> str | None:
    """The registry path the environment names outright, or None."""
    for name in _REGISTRY_ENV_NAMES:
        value = os.environ.get(name)
        if value:
            return value
    return None


def registry_path() -> Path:
    """Where the registry lives: WT_REGISTRY > MODELMAN_REGISTRY >
    XDG_CONFIG_HOME > ~/.config.

    The same precedence as wt's config.RegistryPath and modelman's
    _default_registry_path."""
    override = _registry_override()
    if override:
        return Path(override).expanduser()
    base = os.environ.get("XDG_CONFIG_HOME") or "~/.config"
    return Path(base, "local-ai", "registry.toml").expanduser()
```

In `registry_read_path`, replace:

```python
    # Not past MODELMAN_REGISTRY or an explicit path: those name the file
    # outright, and a missing one is missing.
    legacy = Path("~/.config/local-ai/registry.toml").expanduser()
    if path is None and not os.environ.get("MODELMAN_REGISTRY") and wanted != legacy:
```

with:

```python
    # Not past WT_REGISTRY, MODELMAN_REGISTRY or an explicit path: those name
    # the file outright, and a missing one is missing.
    legacy = Path("~/.config/local-ai/registry.toml").expanduser()
    if path is None and not _registry_override() and wanted != legacy:
```

- [ ] **Step 4: Clear `WT_REGISTRY` in the conftest**

In `llmbench/tests/conftest.py`, in `_no_real_config`, replace:

```python
    monkeypatch.delenv("XDG_CONFIG_HOME", raising=False)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(tmp_path / "no-registry.toml"))
```

with:

```python
    monkeypatch.delenv("XDG_CONFIG_HOME", raising=False)
    # Both registry names: WT_REGISTRY outranks MODELMAN_REGISTRY, so one
    # inherited from the developer's shell would beat the scratch path below.
    monkeypatch.delenv("WT_REGISTRY", raising=False)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(tmp_path / "no-registry.toml"))
```

In `llmbench/tests/test_conftest_guards.py`, in `test_default_config_paths_are_under_the_scratch_home`, replace:

```python
    for name in ("MODELMAN_REGISTRY", "LLMBENCH_LATEST", "MODELMAN_STATE"):
```

with:

```python
    for name in ("WT_REGISTRY", "MODELMAN_REGISTRY", "LLMBENCH_LATEST", "MODELMAN_STATE"):
```

- [ ] **Step 5: Run both Python suites, then again with `WT_REGISTRY` inherited**

Run, from `llmbench/`: `make check && make test`

Expected: ruff and mypy clean; all tests pass; `Required test coverage of 90.0% reached`.

Run, from `modelman/`: `make check && make test`

Expected: ruff and mypy clean; all tests pass (the two parity tests from Step 1 included).

Run, from `llmbench/`, then from `modelman/`: `WT_REGISTRY=/nonexistent/x.toml uv run pytest -q`

Expected: all pass in both.

- [ ] **Step 6: Commit**

```bash
git add llmbench/src/llmbench/registry.py llmbench/tests/conftest.py llmbench/tests/test_conftest_guards.py llmbench/tests/test_registry.py modelman/tests/test_registry_path_parity.py
git commit -m "feat(llmbench): WT_REGISTRY names the registry, ahead of MODELMAN_REGISTRY"
```

### Task 6: Document the env name

**Files:**
- Modify: `wt/CLAUDE.md`, `wt/docs/internals/config-and-registry.md`, `wt/docs/internals/testing.md`, `wt/CHANGELOG.md`, `modelman/CLAUDE.md`, `modelman/README.md`, `modelman/docs/internals/registry-and-state.md`, `llmbench/CLAUDE.md`, `docs/guides/00-config-map.md`, `docs/guides/02-providers-and-models.md`, `docs/guides/04-litellm-config.md`, `CLAUDE.md`

**Interfaces:**
- Consumes: Tasks 3 to 5.
- Produces: nothing code reads.

- [ ] **Step 1: Make the edits**

Each block is an exact replacement; every "replace" text occurs once in its file.

In `wt/CLAUDE.md`, replace:

```text
Path precedence matches modelman's: `MODELMAN_REGISTRY` > `XDG_CONFIG_HOME` > `~/.config` — keep the two in sync.
```

with:

```text
Path precedence: `WT_REGISTRY` > `MODELMAN_REGISTRY` (the older name, kept as an alias) > `XDG_CONFIG_HOME` > `~/.config`. modelman's `_default_registry_path` and llmbench's `registry_path` use the same order — keep the three in sync; each has a precedence test.
```

In `wt/CLAUDE.md`, replace:

```text
- **Tests stay off the developer's machine.** The `TestMain`s of `cmd/wt`, `internal/tui`, `internal/litellm` and `internal/survey` point `XDG_CONFIG_HOME` at a throwaway directory, stub the inventory probe, hard-fail model starts, and no-op the route check.
```

with:

```text
- **Tests stay off the developer's machine.** The `TestMain`s of `cmd/wt`, `internal/tui` and `internal/config` call `config.IsolateConfigHomeForTest`, which points `XDG_CONFIG_HOME` at a throwaway directory and clears `WT_REGISTRY` and `MODELMAN_REGISTRY`; `cmd/wt` and `internal/tui` also stub the inventory probe, hard-fail model starts, and no-op the route check. A test elsewhere that sets `MODELMAN_REGISTRY` also blanks `WT_REGISTRY`, which outranks it.
```

In `wt/docs/internals/config-and-registry.md`, replace:

```text
Path precedence matches modelman's `_default_registry_path`: `MODELMAN_REGISTRY` > `XDG_CONFIG_HOME` > `~/.config` — keep the two in sync. A `MODELMAN_REGISTRY` starting with `~/` (or exactly `~`) is expanded via `expandHome` to match Python's `Path.expanduser()`; Go never expands `~` itself, so this parity is deliberate.
```

with:

```text
Path precedence (`config.RegistryPath`): `WT_REGISTRY` > `MODELMAN_REGISTRY` > `XDG_CONFIG_HOME` > `~/.config`. `WT_REGISTRY` is the name the three tools share; `MODELMAN_REGISTRY` is the older name and stays as an alias. modelman's `_default_registry_path` and llmbench's `registry_path` use the same order, and each of the three has a precedence test — keep them in sync. A value starting with `~/` (or exactly `~`) is expanded via `expandHome` to match Python's `Path.expanduser()`; Go never expands `~` itself, so this parity is deliberate.
```

In `wt/docs/internals/config-and-registry.md`, replace:

```text
`Dir()` and `RegistryPath()` both honor `XDG_CONFIG_HOME` (and `RegistryPath()` also `MODELMAN_REGISTRY`) but use
```

with:

```text
`Dir()` and `RegistryPath()` both honor `XDG_CONFIG_HOME` (and `RegistryPath()` also `WT_REGISTRY` and `MODELMAN_REGISTRY`) but use
```

In `wt/docs/internals/testing.md`, replace:

```text
so their `TestMain`s point `XDG_CONFIG_HOME` at a throwaway directory for the whole package (and clear `MODELMAN_REGISTRY`); a test that sets its own still wins.
```

with:

```text
so their `TestMain`s point `XDG_CONFIG_HOME` at a throwaway directory for the whole package (and clear `WT_REGISTRY` and `MODELMAN_REGISTRY`) through `config.IsolateConfigHomeForTest`; a test that sets its own still wins. `internal/config` does the same: most of its tests set `XDG_CONFIG_HOME` or `MODELMAN_REGISTRY` themselves, and a `WT_REGISTRY` inherited from the developer's shell outranks both. In a package with no isolating `TestMain`, a test that sets `MODELMAN_REGISTRY` sets `WT_REGISTRY` to `""` on the line before.
```

In `wt/CHANGELOG.md`, replace:

```text
## Unreleased

### Changed
```

with:

```text
## Unreleased

### Added

- `WT_REGISTRY` names the model registry file. It outranks `MODELMAN_REGISTRY`,
  which keeps working as an alias; modelman and llmbench read the same name.

### Changed
```

In `modelman/CLAUDE.md`, replace:

```text
path precedence `MODELMAN_REGISTRY` > `XDG_CONFIG_HOME` > `~/.config` (matches wt's `config.RegistryPath`).
```

with:

```text
path precedence `WT_REGISTRY` > `MODELMAN_REGISTRY` > `XDG_CONFIG_HOME` > `~/.config` (matches wt's `config.RegistryPath` and llmbench's `registry_path`).
```

In `modelman/CLAUDE.md`, replace:

```text
- Path redirects: `MODELMAN_REGISTRY`, `MODELMAN_STATE`,
```

with:

```text
- Path redirects: `WT_REGISTRY` > `MODELMAN_REGISTRY` (an autouse conftest fixture clears both, so one inherited from the shell cannot outrank a test's own), `MODELMAN_STATE`,
```

In `modelman/README.md`, replace:

```text
| `registry.toml` | Canonical model/provider definitions (shared, read-only by other tools) | `MODELMAN_REGISTRY` |
```

with:

```text
| `registry.toml` | Canonical model/provider definitions (shared, read-only by other tools) | `WT_REGISTRY` (legacy alias `MODELMAN_REGISTRY`) |
```

In `modelman/docs/internals/registry-and-state.md`, replace:

```text
Path precedence `MODELMAN_REGISTRY` > `XDG_CONFIG_HOME` > `~/.config` (matches wt's `config.RegistryPath`).
```

with:

```text
Path precedence `WT_REGISTRY` > `MODELMAN_REGISTRY` > `XDG_CONFIG_HOME` > `~/.config` (matches wt's `config.RegistryPath` and llmbench's `registry_path`).
```

In `llmbench/CLAUDE.md`, replace:

```text
| Registry (read-only) | `~/.config/local-ai/registry.toml` | `MODELMAN_REGISTRY`, then `XDG_CONFIG_HOME` |
```

with:

```text
| Registry (read-only) | `~/.config/local-ai/registry.toml` | `WT_REGISTRY`, then `MODELMAN_REGISTRY`, then `XDG_CONFIG_HOME` |
```

In `docs/guides/00-config-map.md`, replace:

```text
- **Env override:** `MODELMAN_REGISTRY`.
```

with:

```text
- **Env override:** `WT_REGISTRY` (legacy alias `MODELMAN_REGISTRY`, read after it). wt, modelman and llmbench all honor both.
```

In `docs/guides/00-config-map.md`, replace:

```text
This path does **not** follow `MODELMAN_REGISTRY` or `XDG_CONFIG_HOME`:
```

with:

```text
This path does **not** follow `WT_REGISTRY`, `MODELMAN_REGISTRY` or `XDG_CONFIG_HOME`:
```

In `docs/guides/02-providers-and-models.md`, replace:

```text
| `registry.toml` | Canonical model/provider definitions (shared, read-only by other tools) | `MODELMAN_REGISTRY` |
```

with:

```text
| `registry.toml` | Canonical model/provider definitions (shared, read-only by other tools) | `WT_REGISTRY` (legacy alias `MODELMAN_REGISTRY`) |
```

In `docs/guides/04-litellm-config.md`, replace:

```text
It is required whenever the registry is redirected (`MODELMAN_REGISTRY`, or an `XDG_CONFIG_HOME` other than `~/.config`):
```

with:

```text
It is required whenever the registry is redirected (`WT_REGISTRY`, its legacy alias `MODELMAN_REGISTRY`, or an `XDG_CONFIG_HOME` other than `~/.config`):
```

In `CLAUDE.md`, replace:

```text
The path follows neither `MODELMAN_REGISTRY` nor `XDG_CONFIG_HOME`, so wt refuses
```

with:

```text
The path follows none of `WT_REGISTRY`, `MODELMAN_REGISTRY` (its legacy alias) and `XDG_CONFIG_HOME`, so wt refuses
```

- [ ] **Step 2: Verify the PR and commit**

Run, from the monorepo root: `make test-all`

Expected: exit 0 (`ALL LINKS OK`, both Python suites pass, every Go package `ok`).

```bash
git add wt/CLAUDE.md wt/docs/internals/config-and-registry.md wt/docs/internals/testing.md wt/CHANGELOG.md modelman/CLAUDE.md modelman/README.md modelman/docs/internals/registry-and-state.md llmbench/CLAUDE.md docs/guides/00-config-map.md docs/guides/02-providers-and-models.md docs/guides/04-litellm-config.md CLAUDE.md
git commit -m "docs: WT_REGISTRY is the registry's env name in wt, modelman and llmbench"
```

Stop here. Pushing `feat/registry-env-wt-registry` and opening the PR need the owner's OK.

---

## PR 3 — `tomlw`, the written fixture and its three contract tests

Branch `feat/wt-tomlw`. A new package with no caller yet: an ordered TOML document and an emitter that writes what `tomli_w.dumps` writes. The contract is one checked-in file that both writers must reproduce byte for byte.

Why not the stock encoder: `toml.NewEncoder` sorts keys (every line of the registry would move) and formats a local date in UTC (`1979-05-27` becomes `1979-05-26` east of UTC). The decoder stays, and only the emitter is in-house. The decoder has one defect that matters here, and `Decode` guards against it: BurntSushi v1.6.0 drops a value when a key path is an array in one `[[row]]` and a dotted-key table in a later row, while still listing the key. `Decode` compares the key list with the decoded tree and refuses a document where they disagree, so a wt write can never make that loss permanent.

How the key order is recovered. BurntSushi's `MetaData.Keys()` lists every key in document order, as a path with no array indexes. For a `[[models]]` array the path `models` appears once per element, so each occurrence starts the next element. For an inline array of inline tables (`families = [ {…}, {…} ]`, the form tomli-w gives short rows) the array's path appears once and the elements' keys follow with no boundary. The boundary is recovered from the decoded elements: a key the current element was already given, or does not have, belongs to the next one.

### Task 7: An ordered table

**Files:**
- Create: `wt/internal/tomlw/table.go`
- Test: `wt/internal/tomlw/table_test.go`

**Interfaces:**
- Consumes: nothing from wt.
- Produces (package `tomlw`):
  - `type Table struct` — values are `bool`, `int64`, `float64`, `string`, `time.Time`, `[]any` or `*Table`
  - `func NewTable() *Table`
  - `func (t *Table) Len() int`, `Keys() []string`, `Has(k string) bool`, `Get(k string) (any, bool)`
  - `func (t *Table) Set(k string, v any)` — in place, or appended
  - `func (t *Table) SetAt(k string, v any, schema []string)` — in place, or at the schema position
  - `func (t *Table) Delete(k string) bool`
  - `func (t *Table) Clone() *Table` — deep copy
  - `func Value(v any) (any, error)` — converts `int`, `[]string`, `map[string]any`, `[]map[string]any`, `[]*Table`, `[]any`
  - `func Same(a, b any) bool` — tables by key regardless of order; an `int64` and a `float64` of equal value are the same

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/tomlw/table_test.go`:

```go
package tomlw

import (
	"slices"
	"testing"
	"time"
)

func tableOf(t *testing.T, kv ...any) *Table {
	t.Helper()
	out := NewTable()
	for i := 0; i < len(kv); i += 2 {
		out.Set(kv[i].(string), kv[i+1])
	}
	return out
}

// TestSetAtPutsANewKeyAtItsSchemaPosition pins where a key wt adds to a row
// lands. A new key in the wrong place is a row modelman reorders on its next
// save, so every wt edit would show up twice in the file's history.
func TestSetAtPutsANewKeyAtItsSchemaPosition(t *testing.T) {
	schema := []string{"id", "family", "location", "tags", "cost"}
	cases := []struct {
		name string
		have []string
		add  string
		want []string
	}{
		{"after the nearest earlier schema key", []string{"id", "family", "tags"}, "location", []string{"id", "family", "location", "tags"}},
		{"before the nearest later one when nothing earlier is there", []string{"tags", "cost"}, "id", []string{"id", "tags", "cost"}},
		{"last when it is the last schema key", []string{"id", "tags"}, "cost", []string{"id", "tags", "cost"}},
		{"into an empty table", nil, "family", []string{"family"}},
		{"an unlisted key goes ahead of the schema keys", []string{"id", "family"}, "catalog_name", []string{"catalog_name", "id", "family"}},
		{"and after the unlisted keys already there", []string{"x_note", "id"}, "catalog_name", []string{"x_note", "catalog_name", "id"}},
		{"an unlisted key in a table with no schema keys goes last", []string{"x_note"}, "x_more", []string{"x_note", "x_more"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tbl := NewTable()
			for _, k := range c.have {
				tbl.Set(k, "v")
			}
			tbl.SetAt(c.add, "new", schema)
			if got := tbl.Keys(); !slices.Equal(got, c.want) {
				t.Errorf("keys = %v, want %v", got, c.want)
			}
		})
	}
}

// TestSetAtAndSetKeepAnExistingKeysPlace pins that changing a value never
// moves its line: an edit to one field must be a one-line diff.
func TestSetAtAndSetKeepAnExistingKeysPlace(t *testing.T) {
	tbl := tableOf(t, "tags", "a", "id", "b", "family", "c")
	tbl.SetAt("id", "changed", []string{"id", "family", "tags"})
	tbl.Set("tags", "changed")
	if got, want := tbl.Keys(), []string{"tags", "id", "family"}; !slices.Equal(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
	if v, _ := tbl.Get("id"); v != "changed" {
		t.Errorf("id = %v, want the new value", v)
	}
}

// TestDeleteRemovesTheKeyAndItsPlace pins Delete: a deleted key must not be
// emitted, and deleting a key that is not there must say so.
func TestDeleteRemovesTheKeyAndItsPlace(t *testing.T) {
	tbl := tableOf(t, "a", int64(1), "b", int64(2))
	if !tbl.Delete("a") || tbl.Delete("a") {
		t.Fatal("Delete should report true once, then false")
	}
	if got := tbl.Keys(); !slices.Equal(got, []string{"b"}) || tbl.Has("a") || tbl.Len() != 1 {
		t.Errorf("after Delete: keys %v, Has(a) %v, Len %d", got, tbl.Has("a"), tbl.Len())
	}
}

// TestCloneSharesNothing pins that a copied row is independent of its
// source: CloneModel copies a row and then overrides keys on the copy, and a
// shared nested table would change the original model too.
func TestCloneSharesNothing(t *testing.T) {
	inner := tableOf(t, "x", int64(1))
	src := tableOf(t, "cost", inner, "tags", []any{"a", tableOf(t, "k", "v")})
	cp := src.Clone()
	cpCost, _ := cp.Get("cost")
	cpCost.(*Table).Set("x", int64(2))
	cpTags, _ := cp.Get("tags")
	cpTags.([]any)[0] = "changed"
	cpTags.([]any)[1].(*Table).Set("k", "changed")

	if v, _ := inner.Get("x"); v != int64(1) {
		t.Errorf("the source's nested table changed: x = %v", v)
	}
	srcTags, _ := src.Get("tags")
	if srcTags.([]any)[0] != "a" {
		t.Errorf("the source's array changed: %v", srcTags)
	}
	if v, _ := srcTags.([]any)[1].(*Table).Get("k"); v != "v" {
		t.Errorf("a table inside the source's array changed: k = %v", v)
	}
}

// TestValueConvertsGoValues pins the conversions callers rely on when they
// pass plain Go values to the registry writer, and that a value TOML cannot
// hold is refused when it is handed over, not when the file is written.
func TestValueConvertsGoValues(t *testing.T) {
	if v, err := Value(3); err != nil || v != int64(3) {
		t.Errorf("Value(3) = %v, %v; want int64(3)", v, err)
	}
	if v, err := Value([]string{"a", "b"}); err != nil || !Same(v, []any{"a", "b"}) {
		t.Errorf("Value([]string) = %v, %v", v, err)
	}
	v, err := Value(map[string]any{"b": 1, "a": []map[string]any{{"z": true}}})
	if err != nil {
		t.Fatal(err)
	}
	tbl := v.(*Table)
	if got := tbl.Keys(); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("a map's keys should be sorted, got %v", got)
	}
	rows, _ := tbl.Get("a")
	if _, ok := rows.([]any)[0].(*Table); !ok {
		t.Errorf("a nested map should become a table, got %T", rows.([]any)[0])
	}
	for _, bad := range []any{nil, uint8(1), float32(1), struct{}{}, []any{map[int]int{}}, (*Table)(nil)} {
		if _, err := Value(bad); err == nil {
			t.Errorf("Value(%#v) should be an error", bad)
		}
	}
}

// TestSameTreatsAnIntegerAndAFloatAlike pins the comparison behind "a price
// key is written only when its value changes": a file's `3` and a caller's
// 3.0 are the same price, and rewriting one as the other would change a line
// on every edit of that model.
func TestSameTreatsAnIntegerAndAFloatAlike(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	same := [][2]any{
		{int64(3), 3.0},
		{3.0, int64(3)},
		{"a", "a"},
		{true, true},
		{now, now},
		{[]any{int64(1), "x"}, []any{1.0, "x"}},
		{tableOf(t, "a", int64(1), "b", "x"), tableOf(t, "b", "x", "a", 1.0)},
	}
	for _, p := range same {
		if !Same(p[0], p[1]) {
			t.Errorf("Same(%v, %v) = false, want true", p[0], p[1])
		}
	}
	different := [][2]any{
		{int64(3), 3.5},
		{int64(3), "3"},
		{true, int64(1)},
		{[]any{"a", "b"}, []any{"b", "a"}},
		{[]any{"a"}, []any{"a", "b"}},
		{tableOf(t, "a", int64(1)), tableOf(t, "a", int64(1), "b", int64(2))},
		{tableOf(t, "a", int64(1)), tableOf(t, "b", int64(1))},
		{now, now.Add(time.Second)},
		{now, "2026-10-01"},
	}
	for _, p := range different {
		if Same(p[0], p[1]) {
			t.Errorf("Same(%v, %v) = true, want false", p[0], p[1])
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run, from `wt/`: `go test ./internal/tomlw`

Expected: FAIL — the package does not build: `undefined: Table`, `undefined: NewTable`, `undefined: Value`.

- [ ] **Step 3: Write the table**

Create `wt/internal/tomlw/table.go`:

```go
// Package tomlw is wt's writer for registry.toml: an ordered TOML document
// and an emitter that reproduces tomli-w's layout byte for byte.
//
// modelman writes registry.toml with tomli-w, and until modelman is retired
// both tools write the same file. Decode keeps every table's keys in document
// order, and Encode lays the document out as tomli_w.dumps does, so a wt
// write that changes nothing leaves the file byte-identical and a wt write
// that changes one row is a one-row diff.
//
// The stock BurntSushi encoder is not used: it sorts keys, which rewrites
// every line, and it formats local date and time values in UTC, which moves a
// bare date back a day on a machine east of UTC.
//
// The package imports nothing from wt and knows nothing about the registry.
package tomlw

import (
	"fmt"
	"slices"
	"sort"
	"time"
)

// Table is a TOML table whose keys keep their order. A value is one of bool,
// int64, float64, string, time.Time, []any (an array of values) or *Table.
// Value converts other Go values into these.
type Table struct {
	keys []string
	vals map[string]any
}

// NewTable returns an empty table.
func NewTable() *Table { return &Table{vals: map[string]any{}} }

// Len is the number of keys.
func (t *Table) Len() int { return len(t.keys) }

// Keys returns the keys in order. The slice is a copy.
func (t *Table) Keys() []string { return slices.Clone(t.keys) }

// Has reports whether k is set.
func (t *Table) Has(k string) bool {
	_, ok := t.vals[k]
	return ok
}

// Get returns the value of k.
func (t *Table) Get(k string) (any, bool) {
	v, ok := t.vals[k]
	return v, ok
}

// Set gives k the value v: in place when k is already set, else as the last
// key. v must be a document value (see Value).
func (t *Table) Set(k string, v any) {
	if _, ok := t.vals[k]; !ok {
		t.keys = append(t.keys, k)
	}
	t.vals[k] = v
}

// SetAt gives k the value v: in place when k is already set, else at the
// position schema gives it. schema lists the known keys of this kind of
// table in the order they are written. A new key goes after the nearest
// earlier schema key the table has, or failing that before the nearest later
// one. A key schema does not list goes before the first schema key the table
// has, after any other unlisted keys: that is where modelman writes the keys
// it does not model.
func (t *Table) SetAt(k string, v any, schema []string) {
	if _, ok := t.vals[k]; ok {
		t.vals[k] = v
		return
	}
	t.vals[k] = v
	t.keys = slices.Insert(t.keys, t.position(k, schema), k)
}

func (t *Table) position(k string, schema []string) int {
	at := slices.Index(schema, k)
	if at < 0 {
		for i, have := range t.keys {
			if slices.Contains(schema, have) {
				return i
			}
		}
		return len(t.keys)
	}
	for i := at - 1; i >= 0; i-- {
		if j := slices.Index(t.keys, schema[i]); j >= 0 {
			return j + 1
		}
	}
	for i := at + 1; i < len(schema); i++ {
		if j := slices.Index(t.keys, schema[i]); j >= 0 {
			return j
		}
	}
	return len(t.keys)
}

// Delete removes k and reports whether it was set.
func (t *Table) Delete(k string) bool {
	if _, ok := t.vals[k]; !ok {
		return false
	}
	delete(t.vals, k)
	t.keys = slices.DeleteFunc(t.keys, func(have string) bool { return have == k })
	return true
}

// Clone returns a deep copy: no table or array is shared with t.
func (t *Table) Clone() *Table {
	out := &Table{keys: slices.Clone(t.keys), vals: make(map[string]any, len(t.vals))}
	for k, v := range t.vals {
		out.vals[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch x := v.(type) {
	case *Table:
		return x.Clone()
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = cloneValue(x[i])
		}
		return out
	}
	return v
}

// Value converts a Go value into a document value. Integers become int64,
// string and table slices become []any, and a map becomes a *Table with its
// keys sorted (a Go map has no order to keep; pass a *Table to choose one).
// Anything TOML cannot hold is an error.
func Value(v any) (any, error) {
	switch x := v.(type) {
	case bool, int64, float64, string, time.Time:
		return x, nil
	case int:
		return int64(x), nil
	case *Table:
		if x == nil {
			return nil, fmt.Errorf("tomlw: nil table")
		}
		return x, nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		t := NewTable()
		for _, k := range keys {
			val, err := Value(x[k])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			t.Set(k, val)
		}
		return t, nil
	case []string:
		out := make([]any, len(x))
		for i := range x {
			out[i] = x[i]
		}
		return out, nil
	case []map[string]any:
		out := make([]any, len(x))
		for i := range x {
			val, err := Value(x[i])
			if err != nil {
				return nil, err
			}
			out[i] = val
		}
		return out, nil
	case []*Table:
		out := make([]any, len(x))
		for i := range x {
			out[i] = x[i]
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i := range x {
			val, err := Value(x[i])
			if err != nil {
				return nil, err
			}
			out[i] = val
		}
		return out, nil
	}
	return nil, fmt.Errorf("tomlw: %T is not a TOML value", v)
}

// Same reports whether a and b are the same document value. Tables are the
// same when they hold the same keys with the same values, in any order;
// arrays compare element by element. An integer and a float with the same
// numeric value are the same value — the rule that stops a caller's 3.0
// rewriting a file's `3` as `3.0`.
func Same(a, b any) bool {
	switch x := a.(type) {
	case int64:
		switch y := b.(type) {
		case int64:
			return x == y
		case float64:
			return float64(x) == y
		}
		return false
	case float64:
		switch y := b.(type) {
		case int64:
			return x == float64(y)
		case float64:
			return x == y
		}
		return false
	case time.Time:
		y, ok := b.(time.Time)
		return ok && x.Equal(y) && x.Location().String() == y.Location().String()
	case *Table:
		y, ok := b.(*Table)
		if !ok || len(x.vals) != len(y.vals) {
			return false
		}
		for k, v := range x.vals {
			w, ok := y.vals[k]
			if !ok || !Same(v, w) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !Same(x[i], y[i]) {
				return false
			}
		}
		return true
	}
	return a == b
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run, from `wt/`: `go test -count=1 ./internal/tomlw`

Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
test -z "$(gofmt -l .)" && go vet ./...
git add internal/tomlw/table.go internal/tomlw/table_test.go
git commit -m "feat(wt): tomlw, an ordered TOML table"
```

### Task 8: Decode with key order, and emit as tomli-w does

**Files:**
- Create: `wt/internal/tomlw/decode.go`, `wt/internal/tomlw/encode.go`
- Test: `wt/internal/tomlw/decode_test.go`, `wt/internal/tomlw/encode_test.go`

**Interfaces:**
- Consumes: `Table`, `NewTable`, `Same` from Task 7; `github.com/BurntSushi/toml` (`toml.Decode`, `MetaData.Keys`, `toml.Key`).
- Produces (package `tomlw`):
  - `func Decode(data []byte) (*Table, error)` — empty input is an empty table; a document whose key list names a key the decoded tree has no place for is an error (`tomlw: cannot place key <path>: the decoder's key list and its values disagree`)
  - `func Encode(root *Table) ([]byte, error)` — an empty table is zero bytes; an unknown value type is an error naming the key

Every expected string in these tests that claims to be tomli-w's output was produced by `tomli_w.dumps` (tomli-w 1.2.0).

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/tomlw/decode_test.go`:

```go
package tomlw

import (
	"slices"
	"strings"
	"testing"
)

func roundTrip(t *testing.T, src string) string {
	t.Helper()
	doc, err := Decode([]byte(src))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	out, err := Encode(doc)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return string(out)
}

// TestInlineRowsKeepTheirOwnKeyOrder is the golden test for the gap in the
// decoder's key list: an inline array of inline tables lists every row's keys
// with no boundary between rows. tomli-w writes `families` (and any other
// short rows) in exactly this form, so without the boundary recovery a wt
// write would reorder the keys of every row that differs from the first.
func TestInlineRowsKeepTheirOwnKeyOrder(t *testing.T) {
	const src = `families = [
    { name = "a", display_name = "A" },
    { display_name = "B", name = "b" },
    { x_rank = 2, name = "c" },
    {},
    { name = "e", x_rank = 5, display_name = "E" },
]
`
	if got := roundTrip(t, src); got != src {
		t.Errorf("the rows did not come back as written:\n%s", got)
	}
}

// TestHeaderRowsKeepTheirOwnKeyOrder pins the other array form: each
// [[models]] row keeps the key order it was written with, sub-tables
// included. A shared order would rewrite every hand-edited row. (The `tags`
// arrays keep the rows in header form: tomli-w writes rows this short inline.)
func TestHeaderRowsKeepTheirOwnKeyOrder(t *testing.T) {
	const src = `[[models]]
id = "1"
family = "x"
tags = [
    "code",
]

[models.model_info]
b = 1
a = 2

[[models]]
tags = [
    "code",
]
family = "y"
id = "2"

[models.model_info]
a = 2
c = 3
b = 1
`
	if got := roundTrip(t, src); got != src {
		t.Errorf("the rows did not come back as written:\n%s", got)
	}
}

// TestOneKeyInBothArrayFormsKeepsItsOrder pins the case the decoder's
// metadata cannot describe: one key path that is an inline array in one row
// and a [[header]] array in the next. tomli-w chooses the form per array by
// row length, so a real file can hold both.
func TestOneKeyInBothArrayFormsKeepsItsOrder(t *testing.T) {
	const src = `[[models]]
id = "short"
notes = [
    { b = 1, a = 2 },
    { a = 3, b = 4 },
]

[[models]]
id = "long"

[[models.notes]]
b = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
a = 2

[[models.notes]]
a = 3
b = 4
`
	if got := roundTrip(t, src); got != src {
		t.Errorf("the rows did not come back as written:\n%s", got)
	}
}

// TestNestedInlineRowsKeepTheirOwnKeyOrder pins the same recovery one level
// down: a time-price row's `windows` written inline, and an inline table
// inside an inline row.
func TestNestedInlineRowsKeepTheirOwnKeyOrder(t *testing.T) {
	const src = `rows = [
    { name = "a", sub = { y = 1, x = 2 }, inner = [
    { q = 1 },
    { p = 2, q = 3 },
] },
    { sub = { x = 1, y = 2 }, name = "b" },
]
`
	doc, err := Decode([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := doc.Get("rows")
	first := rows.([]any)[0].(*Table)
	second := rows.([]any)[1].(*Table)
	firstSub, _ := first.Get("sub")
	secondSub, _ := second.Get("sub")
	inner, _ := first.Get("inner")
	checks := []struct {
		name string
		got  []string
		want []string
	}{
		{"first row", first.Keys(), []string{"name", "sub", "inner"}},
		{"second row", second.Keys(), []string{"sub", "name"}},
		{"first row's inline table", firstSub.(*Table).Keys(), []string{"y", "x"}},
		{"second row's inline table", secondSub.(*Table).Keys(), []string{"x", "y"}},
		{"nested array, second row", inner.([]any)[1].(*Table).Keys(), []string{"p", "q"}},
	}
	for _, c := range checks {
		if !slices.Equal(c.got, c.want) {
			t.Errorf("%s: keys = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// TestHandWrittenFormsGetTomliWsOrder pins the forms tomli-w never writes
// but a person does: a dotted key, a table whose parents are never declared,
// and a table continued after another one. They must come out in the order
// Python's tomllib reads them, or the first wt write after a hand edit would
// differ from the first modelman write after the same edit.
func TestHandWrittenFormsGetTomliWsOrder(t *testing.T) {
	const src = `top.dotted = 1
plain = 2

[a.b.c]
z = 1

[other]
k = "v"

[a.d]
w = 2
`
	// tomli_w.dumps(tomllib.loads(src)), run with tomli-w 1.2.0.
	const want = `plain = 2

[top]
dotted = 1

[a.b.c]
z = 1

[a.d]
w = 2

[other]
k = "v"
`
	if got := roundTrip(t, src); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestDecodeReportsAParseError pins that a file that is not TOML is an
// error: the writer must never treat an unreadable registry as an empty one
// and write over it.
func TestDecodeReportsAParseError(t *testing.T) {
	if _, err := Decode([]byte("[[models]\nid = 1\n")); err == nil {
		t.Fatal("Decode of malformed TOML should be an error")
	}
}

// TestDecodeRefusesADocumentTheDecoderLostAKeyFrom pins that Decode fails
// closed. BurntSushi v1.6.0 lists `x_note.who` here but keeps only
// `x_note.why` in the decoded tree (the path is an array in one row and a
// dotted-key table in the next). Writing that tree back would delete a key
// from the user's registry without a word, so the document is refused.
func TestDecodeRefusesADocumentTheDecoderLostAKeyFrom(t *testing.T) {
	const src = `[[models]]
id = "a"
x_note = [1]

[[models]]
id = "b"
x_note.who = "me"
x_note.why = "test"
`
	_, err := Decode([]byte(src))
	if err == nil || !strings.Contains(err.Error(), "models.x_note.who") {
		t.Fatalf("Decode = %v, want an error naming models.x_note.who", err)
	}
}

// TestDecodeRefusesAnInlineRowWithAnEmptyKey pins a known limit of the order
// recovery. BurntSushi decodes this inline array as []map[string]any, the
// type it gives a [[header]] array, so the rows' keys cannot be told apart.
// No registry key is empty, so the document is refused rather than written
// back with its keys in a guessed order.
func TestDecodeRefusesAnInlineRowWithAnEmptyKey(t *testing.T) {
	const src = `t = [
    { n = 1 },
    { c = 1, "" = 2, a = 3 },
]
`
	_, err := Decode([]byte(src))
	if err == nil || !strings.Contains(err.Error(), "cannot place key") {
		t.Fatalf("Decode = %v, want a refusal", err)
	}
}
```

Create `wt/internal/tomlw/encode_test.go`:

```go
package tomlw

import (
	"math"
	"strings"
	"testing"
	"time"
)

func encodeOne(t *testing.T, key string, v any) string {
	t.Helper()
	doc := NewTable()
	doc.Set(key, v)
	out, err := Encode(doc)
	if err != nil {
		t.Fatalf("Encode(%v): %v", v, err)
	}
	return string(out)
}

// TestEncodeWritesValuesAsTomliWDoes pins each scalar against the text
// tomli-w 1.2.0 gives the same value (the expected strings were produced by
// tomli_w.dumps). One differing character in a float or an escape is a line
// that flips every time the other tool saves the file.
func TestEncodeWritesValuesAsTomliWDoes(t *testing.T) {
	cases := []struct {
		v    any
		want string
	}{
		{1.0, "v = 1.0\n"},
		{0.125, "v = 0.125\n"},
		{1e-07, "v = 1e-07\n"},
		{5e22, "v = 5e+22\n"},
		{1e16, "v = 1e+16\n"},
		{1e15, "v = 1000000000000000.0\n"},
		{0.0001, "v = 0.0001\n"},
		{0.00001, "v = 1e-05\n"},
		{math.Copysign(0, -1), "v = -0.0\n"},
		{123456.789, "v = 123456.789\n"},
		{math.Inf(1), "v = inf\n"},
		{math.Inf(-1), "v = -inf\n"},
		{math.NaN(), "v = nan\n"},
		{int64(9007199254740993), "v = 9007199254740993\n"},
		{int64(-1), "v = -1\n"},
		{true, "v = true\n"},
		{"plain", "v = \"plain\"\n"},
		{`q"uote`, "v = \"q\\\"uote\"\n"},
		{`back\slash`, "v = \"back\\\\slash\"\n"},
		{"tab\there", "v = \"tab\there\"\n"},
		{"nl\nx", "v = \"nl\\nx\"\n"},
		{"\b\f\r", "v = \"\\b\\f\\r\"\n"},
		{"\x1f", "v = \"\\u001f\"\n"},
		{"\x7f", "v = \"\\u007f\"\n"},
		{"ünï", "v = \"ünï\"\n"},
	}
	for _, c := range cases {
		if got := encodeOne(t, "v", c.v); got != c.want {
			t.Errorf("Encode(%#v) = %q, want %q", c.v, got, c.want)
		}
	}
}

// TestEncodeQuotesAKeyOnlyWhenItMust pins key formatting: a bare key stays
// bare, anything else is a quoted string — including a key with a dot, which
// unquoted would become a nested table.
func TestEncodeQuotesAKeyOnlyWhenItMust(t *testing.T) {
	cases := map[string]string{
		"bare-key_1": "bare-key_1 = 1\n",
		"sp ace":     "\"sp ace\" = 1\n",
		"":           "\"\" = 1\n",
		"dé":         "\"dé\" = 1\n",
		"a.b":        "\"a.b\" = 1\n",
	}
	for key, want := range cases {
		if got := encodeOne(t, key, int64(1)); got != want {
			t.Errorf("key %q: got %q, want %q", key, got, want)
		}
	}
}

// TestEncodeKeepsALocalTimesWallClock is why the stock BurntSushi encoder is
// not used: it prints a local date, datetime or time converted to UTC, so on
// a machine east of UTC `1979-05-27` is written back as `1979-05-26`. The
// values here carry the zone names BurntSushi's decoder gives the three local
// kinds, at Tokyo's offset, so the test does not depend on the machine's
// time zone.
func TestEncodeKeepsALocalTimesWallClock(t *testing.T) {
	const tokyo = 9 * 60 * 60
	cases := []struct {
		v    time.Time
		want string
	}{
		{time.Date(1979, 5, 27, 0, 0, 0, 0, time.FixedZone("date-local", tokyo)), "v = 1979-05-27\n"},
		{time.Date(1979, 5, 27, 7, 32, 0, 0, time.FixedZone("datetime-local", tokyo)), "v = 1979-05-27 07:32:00\n"},
		{time.Date(0, 1, 1, 7, 32, 0, 5000, time.FixedZone("time-local", tokyo)), "v = 07:32:00.000005\n"},
		{time.Date(2026, 10, 1, 12, 30, 0, 0, time.UTC), "v = 2026-10-01 12:30:00+00:00\n"},
		{time.Date(2026, 10, 1, 12, 30, 0, 250000000, time.FixedZone("", -(7*60+30)*60)), "v = 2026-10-01 12:30:00.250000-07:30\n"},
	}
	for _, c := range cases {
		if got := encodeOne(t, "v", c.v); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
	// And through the decoder, whatever zone this machine is in.
	const src = "ld = 1979-05-27\nldt = 1979-05-27 07:32:00\nlt = 07:32:00\nodt = 1979-05-27 07:32:00+00:00\n"
	if got := roundTrip(t, "ld = 1979-05-27\nldt = 1979-05-27T07:32:00\nlt = 07:32:00\nodt = 1979-05-27T07:32:00Z\n"); got != src {
		t.Errorf("decoded local times came back as:\n%s", got)
	}
}

// TestEncodeLaysArraysAndTablesOutAsTomliWDoes pins the layout rules: plain
// values before sub-tables, one array item per line with a trailing comma,
// empty arrays and tables in their short forms, and a header only for a
// table that has something to put under it.
func TestEncodeLaysArraysAndTablesOutAsTomliWDoes(t *testing.T) {
	doc := tableOf(t,
		"c", tableOf(t, "d", NewTable()),
		"a", []any{},
		"e", NewTable(),
		"b", []any{[]any{int64(1), int64(2)}, []any{}},
		"f", []any{NewTable()},
	)
	// tomli_w.dumps of the same document.
	const want = `a = []
b = [
    [
        1,
        2,
    ],
    [],
]
f = [
    {},
]

[c.d]

[e]
`
	out, err := Encode(doc)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
	if out, err := Encode(NewTable()); err != nil || len(out) != 0 {
		t.Errorf("an empty document should encode to nothing, got %q, %v", out, err)
	}
}

// TestEncodeBreaksARowOutAtOneHundredAndOneCharacters pins the one layout
// decision that depends on length: rows that fit in 100 characters (indent
// and trailing comma included) are written inline, and one row a character
// longer turns the whole array into [[header]] tables. Off by one here and a
// family with a long display name is rewritten on every save.
func TestEncodeBreaksARowOutAtOneHundredAndOneCharacters(t *testing.T) {
	row := func(n int) []any { return []any{tableOf(t, "k", strings.Repeat("x", n))} }
	fits := encodeOne(t, "rows", row(85))
	if want := "rows = [\n    { k = \"" + strings.Repeat("x", 85) + "\" },\n]\n"; fits != want {
		t.Errorf("a 100-character row should stay inline, got:\n%s", fits)
	}
	breaks := encodeOne(t, "rows", row(86))
	if want := "[[rows]]\nk = \"" + strings.Repeat("x", 86) + "\"\n"; breaks != want {
		t.Errorf("a 101-character row should become a header table, got:\n%s", breaks)
	}
	// Characters, not bytes: 85 two-byte letters are still 100 characters.
	wide := encodeOne(t, "rows", []any{tableOf(t, "k", strings.Repeat("é", 85))})
	if !strings.HasPrefix(wide, "rows = [\n") {
		t.Errorf("the limit counts characters, not bytes; got:\n%s", wide)
	}
}

// TestEncodeRefusesAValueItCannotWrite pins that an unknown value type is an
// error naming the key, never a guess: a silently dropped or mangled key in
// registry.toml is data loss the user finds weeks later.
func TestEncodeRefusesAValueItCannotWrite(t *testing.T) {
	doc := tableOf(t, "models", []any{tableOf(t, "id", "a", "bad", uint8(1))})
	_, err := Encode(doc)
	if err == nil || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("want an error naming the key, got %v", err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run, from `wt/`: `go test ./internal/tomlw`

Expected: FAIL — the package does not build: `undefined: Decode`, `undefined: Encode`.

- [ ] **Step 3: Write the decoder**

Create `wt/internal/tomlw/decode.go`:

```go
package tomlw

import (
	"fmt"
	"slices"
	"sort"

	"github.com/BurntSushi/toml"
)

// Decode parses a TOML document into an ordered table tree.
//
// The values come from BurntSushi/toml; the key order comes from
// MetaData.Keys(), which lists every key in document order. Three things in
// that list need care, and the order recovery below handles each:
//
//   - A [[header]] array repeats its own key once per element, so each
//     occurrence starts the next element.
//   - An inline array of inline tables lists its key once and then every
//     element's keys with no boundary between elements. The boundaries are
//     recovered from the decoded elements: a key the current element has
//     already been given, or does not have, starts the next element.
//   - A dotted key (`a.b = 1`) and an implicit super-table (`[a.b]` with no
//     `[a]`) never list the tables in between; those get their place the
//     first time a key passes through them.
//
// The result has the key order tomllib.loads gives the same text, which is
// what makes Encode(Decode(x)) equal tomli_w.dumps(tomllib.loads(x)).
//
// Decode fails closed. When the key list names a key the decoded tree has no
// place for, the two disagree about the document — BurntSushi v1.6.0 drops a
// value when a key path is an array in one [[row]] and a dotted-key table in
// a later one — and Decode returns an error instead of a tree that would
// lose the key when written back.
func Decode(data []byte) (*Table, error) {
	raw := map[string]any{}
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		return nil, err
	}
	o := &orderer{keys: md.Keys(), header: map[arrayRef]bool{}, current: map[arrayRef]int{}}
	root := o.fromRaw(raw).(*Table)
	for o.pos < len(o.keys) {
		o.step(root)
	}
	if o.lost != nil {
		return nil, fmt.Errorf("tomlw: cannot place key %s: the decoder's key list and its values disagree", o.lost)
	}
	finish(root)
	return root, nil
}

// arrayRef names one array: the table that holds it and its key there.
type arrayRef struct {
	parent *Table
	key    string
}

type orderer struct {
	keys []toml.Key
	pos  int
	// header marks the arrays written as [[header]] tables. BurntSushi
	// decodes those as []map[string]any and an inline array as []any, which
	// is the only place the two forms can be told apart per array: the
	// metadata's type is per key path, and one path can be a header array in
	// one row and an inline array in the next. One exception is known: an
	// inline array with an empty-string key in a later row also decodes as
	// []map[string]any. Its rows' keys then find no place, and Decode
	// refuses the document (lost) rather than guess their order.
	header map[arrayRef]bool
	// current is the index of the element a header array is on.
	current map[arrayRef]int
	// lost is a key the list named that the decoded tree has no place for.
	// Decode refuses such a document: the decoder dropped a value (writing
	// the tree back would delete it for good) or a row's key order cannot be
	// recovered.
	lost toml.Key
}

// fromRaw converts BurntSushi's decoded values into document values. Tables
// start with no key order; step fills it in.
func (o *orderer) fromRaw(v any) any {
	switch x := v.(type) {
	case map[string]any:
		t := &Table{vals: make(map[string]any, len(x))}
		for k, val := range x {
			if _, ok := val.([]map[string]any); ok {
				o.header[arrayRef{t, k}] = true
			}
			t.vals[k] = o.fromRaw(val)
		}
		return t
	case []map[string]any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = o.fromRaw(x[i])
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = o.fromRaw(x[i])
		}
		return out
	}
	return v
}

// finish gives a place to any key the key list never mentioned, so every key
// is emitted: after the ordered ones, sorted.
func finish(v any) {
	switch x := v.(type) {
	case *Table:
		if len(x.keys) < len(x.vals) {
			var rest []string
			for k := range x.vals {
				if !slices.Contains(x.keys, k) {
					rest = append(rest, k)
				}
			}
			sort.Strings(rest)
			x.keys = append(x.keys, rest...)
		}
		for _, k := range x.keys {
			finish(x.vals[k])
		}
	case []any:
		for _, e := range x {
			finish(e)
		}
	}
}

// note gives k the next place in t's order, the first time it is seen.
func (t *Table) note(k string) {
	if !slices.Contains(t.keys, k) {
		t.keys = append(t.keys, k)
	}
}

// step places the key at o.pos, which is not inside an inline array.
func (o *orderer) step(root *Table) {
	k := o.keys[o.pos]
	o.pos++
	cur := root
	for _, part := range k[:len(k)-1] {
		if !cur.Has(part) {
			o.lost = k
			return
		}
		cur.note(part)
		switch v := cur.vals[part].(type) {
		case *Table:
			cur = v
		case []any:
			ref := arrayRef{cur, part}
			n, started := o.current[ref]
			if !o.header[ref] || !started || n >= len(v) {
				return
			}
			cur = v[n].(*Table)
		default:
			return
		}
	}
	last := k[len(k)-1]
	if !cur.Has(last) {
		o.lost = k
		return
	}
	cur.note(last)
	ref := arrayRef{cur, last}
	if o.header[ref] {
		if n, started := o.current[ref]; started {
			o.current[ref] = n + 1
		} else {
			o.current[ref] = 0
		}
		return
	}
	if arr, ok := cur.vals[last].([]any); ok {
		o.inlineArray(arr, k)
	}
}

// inlineArray hands the keys that follow an inline array's own key to its
// elements, in order, then drops any key under prefix that no element took.
func (o *orderer) inlineArray(arr []any, prefix toml.Key) {
	o.elements(arr, prefix)
	for o.pos < len(o.keys) && under(o.keys[o.pos], prefix) {
		o.pos++
	}
}

func (o *orderer) elements(arr []any, prefix toml.Key) {
	for _, e := range arr {
		switch x := e.(type) {
		case *Table:
			o.inlineTable(x, prefix)
		case []any:
			o.elements(x, prefix)
		}
	}
}

// inlineTable takes keys for one element until the next key is not this
// element's: it is outside prefix, the element does not have it, or the
// element was already given it.
func (o *orderer) inlineTable(t *Table, prefix toml.Key) {
	for o.pos < len(o.keys) && under(o.keys[o.pos], prefix) {
		k := o.keys[o.pos]
		rel := k[len(prefix):]
		last := rel[len(rel)-1]
		owner, ok := descend(t, rel[:len(rel)-1])
		if !ok || !owner.Has(last) || slices.Contains(owner.keys, last) {
			return
		}
		cur := t
		for _, part := range rel[:len(rel)-1] {
			cur.note(part)
			cur = cur.vals[part].(*Table)
		}
		owner.note(last)
		o.pos++
		if arr, ok := owner.vals[last].([]any); ok {
			o.inlineArray(arr, k)
		}
	}
}

// descend follows path through nested tables of t.
func descend(t *Table, path []string) (*Table, bool) {
	for _, part := range path {
		sub, ok := t.vals[part].(*Table)
		if !ok {
			return nil, false
		}
		t = sub
	}
	return t, true
}

// under reports whether k is a key strictly inside prefix.
func under(k, prefix toml.Key) bool {
	return len(k) > len(prefix) && slices.Equal(k[:len(prefix)], prefix)
}
```

- [ ] **Step 4: Write the emitter**

Create `wt/internal/tomlw/encode.go`. It is a port of tomli-w's `gen_table_chunks`, `format_literal`, `format_inline_table`, `format_inline_array`, `format_string` and `format_key_part`:

```go
package tomlw

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The two layout constants of tomli-w 1.2.0 that decide where a line breaks.
const (
	arrayIndent   = "    "
	maxLineLength = 100
)

// Encode lays root out as tomli_w.dumps does (tomli-w 1.2.0, the version
// modelman pins): in each table the plain values first, in key order, then
// the sub-tables; an array of tables as one inline row per table when every
// row fits in 100 characters, else as [[header]] tables; every other array
// one item per line with a trailing comma; floats and datetimes as Python
// prints them. A value that is not a document value is an error, never a
// guess.
func Encode(root *Table) ([]byte, error) {
	var b strings.Builder
	if err := writeTable(&b, root, "", false); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

type subTable struct {
	key   string
	table *Table
	inAOT bool
}

// writeTable is tomli-w's gen_table_chunks.
func writeTable(b *strings.Builder, t *Table, name string, inAOT bool) error {
	var literals []string
	var tables []subTable
	for _, k := range t.keys {
		v := t.vals[k]
		if sub, ok := v.(*Table); ok {
			tables = append(tables, subTable{k, sub, false})
			continue
		}
		if rows, ok := tableRows(v); ok {
			inline, err := allRowsFitInline(rows)
			if err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
			if !inline {
				for _, row := range rows {
					tables = append(tables, subTable{k, row, true})
				}
				continue
			}
		}
		lit, err := literal(v, 0)
		if err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
		literals = append(literals, formatKey(k)+" = "+lit+"\n")
	}
	wrote := false
	if inAOT || (name != "" && (len(literals) > 0 || len(tables) == 0)) {
		wrote = true
		if inAOT {
			b.WriteString("[[" + name + "]]\n")
		} else {
			b.WriteString("[" + name + "]\n")
		}
	}
	if len(literals) > 0 {
		wrote = true
		for _, l := range literals {
			b.WriteString(l)
		}
	}
	for _, sub := range tables {
		if wrote {
			b.WriteString("\n")
		} else {
			wrote = true
		}
		display := formatKey(sub.key)
		if name != "" {
			display = name + "." + display
		}
		if err := writeTable(b, sub.table, display, sub.inAOT); err != nil {
			return err
		}
	}
	return nil
}

// tableRows reports whether v is a non-empty array holding only tables —
// tomli-w's is_aot.
func tableRows(v any) ([]*Table, bool) {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return nil, false
	}
	rows := make([]*Table, len(arr))
	for i, e := range arr {
		t, ok := e.(*Table)
		if !ok {
			return nil, false
		}
		rows[i] = t
	}
	return rows, true
}

// allRowsFitInline is tomli-w's is_suitable_inline_table over every row: the
// row, indented and with its trailing comma, is at most 100 characters and
// holds no newline (so no non-empty array, which always spans lines).
func allRowsFitInline(rows []*Table) (bool, error) {
	for _, row := range rows {
		s, err := inlineTable(row)
		if err != nil {
			return false, err
		}
		line := arrayIndent + s + ","
		if utf8.RuneCountInString(line) > maxLineLength || strings.Contains(line, "\n") {
			return false, nil
		}
	}
	return true, nil
}

func literal(v any, nest int) (string, error) {
	switch x := v.(type) {
	case bool:
		return strconv.FormatBool(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float64:
		return formatFloat(x), nil
	case string:
		return formatString(x), nil
	case time.Time:
		return formatTime(x), nil
	case *Table:
		return inlineTable(x)
	case []any:
		return inlineArray(x, nest)
	}
	return "", fmt.Errorf("tomlw: cannot encode %T", v)
}

func inlineTable(t *Table) (string, error) {
	if len(t.keys) == 0 {
		return "{}", nil
	}
	parts := make([]string, 0, len(t.keys))
	for _, k := range t.keys {
		// tomli-w formats an inline table's values at nest level 0.
		lit, err := literal(t.vals[k], 0)
		if err != nil {
			return "", fmt.Errorf("%s: %w", k, err)
		}
		parts = append(parts, formatKey(k)+" = "+lit)
	}
	return "{ " + strings.Join(parts, ", ") + " }", nil
}

func inlineArray(arr []any, nest int) (string, error) {
	if len(arr) == 0 {
		return "[]", nil
	}
	indent := strings.Repeat(arrayIndent, nest+1)
	var b strings.Builder
	b.WriteString("[\n")
	for _, item := range arr {
		lit, err := literal(item, nest+1)
		if err != nil {
			return "", err
		}
		b.WriteString(indent + lit + ",\n")
	}
	b.WriteString(strings.Repeat(arrayIndent, nest) + "]")
	return b.String(), nil
}

// formatFloat prints f as Python's str(float) does: the shortest digits that
// read back as f, exponent form below 1e-04 and from 1e+16 up, and always a
// ".0" on a whole number so the value stays a float when read back.
func formatFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	sci := strconv.FormatFloat(f, 'e', -1, 64)
	exp, _ := strconv.Atoi(sci[strings.IndexByte(sci, 'e')+1:])
	if f != 0 && (exp < -4 || exp >= 16) {
		return sci
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// formatTime prints t as Python's str() prints the matching datetime, date or
// time. BurntSushi marks the three local kinds with a named zone at the
// machine's offset; they are printed from their wall clock as parsed, never
// converted — converting to UTC is the stock encoder's bug.
func formatTime(t time.Time) string {
	frac := ""
	if ns := t.Nanosecond(); ns != 0 {
		if ns%1000 == 0 {
			frac = fmt.Sprintf(".%06d", ns/1000)
		} else {
			frac = fmt.Sprintf(".%09d", ns)
		}
	}
	switch t.Location().String() {
	case "date-local":
		return t.Format("2006-01-02")
	case "time-local":
		return t.Format("15:04:05") + frac
	case "datetime-local":
		return t.Format("2006-01-02 15:04:05") + frac
	}
	return t.Format("2006-01-02 15:04:05") + frac + t.Format("-07:00")
}

func isBareKey(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func formatKey(k string) string {
	if isBareKey(k) {
		return k
	}
	return formatString(k)
}

// formatString writes a basic string as tomli-w does: the six short escapes,
// \uXXXX for any other control character, a tab and everything else as is.
func formatString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\f':
			b.WriteString(`\f`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case (r < 32 && r != '\t') || r == 127:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run, from `wt/`: `go test -count=1 ./internal/tomlw`

Expected: `ok`.

Run it once more east of UTC, where the stock encoder's date bug shows:

```bash
TZ=Asia/Tokyo go test -count=1 ./internal/tomlw
```

Expected: `ok`.

- [ ] **Step 6: Commit**

```bash
test -z "$(gofmt -l .)" && go vet ./...
git add internal/tomlw/decode.go internal/tomlw/encode.go internal/tomlw/decode_test.go internal/tomlw/encode_test.go
git commit -m "feat(wt): tomlw decodes with key order and emits tomli-w's layout"
```

### Task 9: The written fixture and its three contract tests

One file, `docs/contracts/registry.written.sample.toml`, in the exact form tomli-w writes: keys neither tool models at every level below the top (provider, auth, family, model, cost, time-price row, window, fetch, draft), integer and float prices, an empty array, a short array of tables (inline) and long ones (`[[header]]`), `time_prices`, and offset and local datetimes. Three tests read it:

- Go (`wt/internal/tomlw`): decode, emit, same bytes.
- modelman: `tomli_w.dumps(tomllib.loads(text)) == text`, and `load_registry` accepts it.
- llmbench: its reader loads it.

The file has no comments, because tomli-w writes none and a comment would break the fixed point. What it is for is written in the three tests. All of its content is synthetic.

**Files:**
- Create: `docs/contracts/registry.written.sample.toml`
- Create: `wt/internal/tomlw/fixture_test.go`
- Create: `modelman/tests/contracts/test_registry_written_fixture.py`
- Modify: `wt/internal/config/registry_fixture_test.go` (one test appended)
- Modify: `llmbench/tests/test_registry.py` (one constant, one test)
- Modify: `CLAUDE.md`, `wt/CLAUDE.md`, `wt/docs/internals/config-and-registry.md`

**Interfaces:**
- Consumes: `tomlw.Decode`, `tomlw.Encode`, `tomlw.Same` (Task 8); `loadRegistry()` in `wt/internal/config`; `modelman.registry.load_registry`; `llmbench.registry.load_registry`.
- Produces: the fixture file. PR 4's `TestUpdateRegistryNoOpLeavesTheFileByteIdentical` reads it.

- [ ] **Step 1: Write the three failing tests**

Create `wt/internal/tomlw/fixture_test.go`:

```go
package tomlw

import (
	"os"
	"testing"
)

// writtenFixture is the writer's contract fixture at the monorepo root: one
// registry in the exact form tomli-w gives it. It holds no comments, because
// tomli-w writes none, so what it is for is recorded here and in its two
// Python readers: modelman/tests/contracts/test_registry_written_fixture.py
// asserts tomli_w reproduces the same bytes, and llmbench/tests/test_registry.py
// asserts its reader loads them.
const writtenFixture = "../../../docs/contracts/registry.written.sample.toml"

// TestWrittenFixtureIsAFixedPoint is the Go half of the writer contract:
// decoding the fixture and encoding it again reproduces every byte. modelman
// asserts the same of tomli-w on the same file, so the two writers are proved
// to agree without either CI job running the other language. If this fails,
// a wt write that changes nothing would still rewrite the user's registry.
func TestWrittenFixtureIsAFixedPoint(t *testing.T) {
	want, err := os.ReadFile(writtenFixture)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Decode(want)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	got, err := Encode(doc)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("decode-then-emit changed the fixture; got:\n%s", got)
	}
	again, err := Decode(got)
	if err != nil {
		t.Fatalf("the emitted text does not parse: %v", err)
	}
	if !Same(doc, again) {
		t.Error("the emitted text decodes to a different document")
	}
}
```

Append to `wt/internal/config/registry_fixture_test.go`:

```go
// TestTypedReaderLoadsTheWrittenFixture pins that wt's own reader accepts a
// registry in the form wt's writer produces: docs/contracts/
// registry.written.sample.toml, which wt/internal/tomlw re-emits byte for
// byte and modelman's tomli-w reproduces too. It holds what the hand-written
// sample cannot — integer prices, an empty tags array, keys wt does not model
// at every level below the top, [[header]] windows — and a reader that choked on any of
// them would fail on the user's real registry after the first wt write.
func TestTypedReaderLoadsTheWrittenFixture(t *testing.T) {
	t.Setenv("MODELMAN_REGISTRY", "../../../docs/contracts/registry.written.sample.toml")

	providers, models, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry() error: %v", err)
	}
	if len(providers) != 4 || len(models) != 4 {
		t.Fatalf("got %d providers and %d models, want 4 and 4", len(providers), len(models))
	}
	ints := models[0]
	if ints.ID != "ollama/written-fixture:int" || ints.Tags == nil || len(ints.Tags) != 0 {
		t.Errorf("first model decoded wrong: %+v", ints)
	}
	if ints.Cost.InputPricePerMillion == nil || *ints.Cost.InputPricePerMillion != 3 ||
		ints.Cost.OutputPricePerMillion == nil || *ints.Cost.OutputPricePerMillion != 15 {
		t.Errorf("integer prices should decode as 3 and 15, got %+v", ints.Cost)
	}
	if got := ints.ModelInfo["max_input_tokens"]; got != int64(131072) {
		t.Errorf("model_info.max_input_tokens = %v (%T), want 131072", got, got)
	}
	cloud := models[1]
	if len(cloud.Cost.TimePrices) != 1 || len(cloud.Cost.TimePrices[0].Windows) != 2 {
		t.Fatalf("time_prices decoded wrong: %+v", cloud.Cost.TimePrices)
	}
	if w := cloud.Cost.TimePrices[0].Windows[1]; len(w.Days) != 2 || w.Days[0] != "sat" || w.End != "24:00" {
		t.Errorf("second window decoded wrong: %+v", w)
	}
	if cloud.Cost.SubscriptionPrice == nil || *cloud.Cost.SubscriptionPrice != 20 || cloud.Cost.SubscriptionPeriod != "month" {
		t.Errorf("subscription decoded wrong: %+v", cloud.Cost)
	}
	cfg := &Config{DefaultTag: "code", Providers: providers, Models: models}
	if err := cfg.ValidateAll(); err != nil {
		t.Errorf("the written fixture should validate: %v", err)
	}
}
```

Create `modelman/tests/contracts/test_registry_written_fixture.py`:

```python
"""The writer half of the registry contract.

docs/contracts/registry.written.sample.toml is one registry in the exact form
tomli-w gives it. It carries no comments (tomli-w writes none), so its purpose
is recorded here: wt's Go writer asserts that decoding the file and emitting it
again reproduces every byte (wt/internal/tomlw/fixture_test.go), and this file
asserts the same of tomli-w. Two fixed points on the same bytes prove the two
writers lay a registry out identically without either CI job running the other
language. llmbench/tests/test_registry.py asserts its reader loads the file.

Deleted with modelman: once wt is the only writer there is nothing left for
its output to agree with.
"""

import datetime
import tomllib
from pathlib import Path

import tomli_w

from modelman.registry import load_registry

FIXTURE = (
    Path(__file__).resolve().parents[3] / "docs" / "contracts" / "registry.written.sample.toml"
)


def test_tomli_w_reproduces_the_written_fixture():
    """If this fails the fixture is no longer in tomli-w form (it was edited
    by hand, or tomli-w changed its layout), and wt's emitter is being held to
    bytes modelman itself would not write."""
    text = FIXTURE.read_text(encoding="utf-8")
    assert tomli_w.dumps(tomllib.loads(text)) == text


def test_load_registry_accepts_the_written_fixture():
    """A registry wt writes must load in modelman for as long as modelman
    exists, with the keys neither tool models kept for the next save."""
    registry = load_registry(path=FIXTURE)

    assert [p.id for p in registry.providers] == ["ollama", "openrouter", "mlx_lm_server", "agy"]
    assert registry.provider("ollama").extra == {"x_provider_note": "an unknown provider key"}
    assert registry.provider("ollama").auth.extra == {"x_auth_note": "an unknown auth key"}
    assert [f.name for f in registry.families] == ["written-fixture", "written-second"]
    assert registry.families[1].extra == {"x_rank": 2}

    ints = registry.model("ollama/written-fixture:int")
    assert ints.tags == []
    assert ints.cost is not None
    assert (ints.cost.input_price_per_million, ints.cost.output_price_per_million) == (3.0, 15.0)
    assert ints.extra["catalog_name"] == "written-fixture"
    assert ints.extra["x_added"] == datetime.date(2026, 10, 1)
    assert ints.model_info["max_input_tokens"] == 131072

    cloud = registry.model("openrouter/written-fixture:cloud")
    assert cloud.cost is not None
    assert cloud.cost.extra == {"x_cost_note": "an unknown cost key"}
    assert cloud.cost.subscription_price == 20.0
    (off_peak,) = cloud.cost.time_prices
    assert off_peak.extra == {"x_row_note": "an unknown time-price key"}
    assert off_peak.windows[1].extra == {"x_window_note": "an unknown window key"}
    assert cloud.pricing_updated_at == "2026-10-01T00:00:00+00:00"

    pair = registry.model("mlx_lm_server/written-fixture:pair")
    assert pair.fetch is not None and pair.fetch.extra == {"x_fetch_note": "an unknown fetch key"}
    assert pair.draft is not None and pair.draft.extra == {"x_draft_note": "an unknown draft key"}
    assert pair.quantization == "4bit"
```

In `llmbench/tests/test_registry.py`, add this constant directly under the line that defines `FIXTURE`:

```python
# One registry in the exact form wt (and, until it is retired, modelman's
# tomli-w) writes it; it holds no comments, so its purpose is recorded in its
# readers. wt asserts it re-emits these bytes (wt/internal/tomlw/fixture_test.go).
WRITTEN_FIXTURE = FIXTURE.with_name("registry.written.sample.toml")
```

and append this test to the end of the file:

```python
def test_load_registry_reads_the_written_contract_fixture():
    """docs/contracts/registry.written.sample.toml is what a registry looks
    like after wt has written it: tomli-w layout, keys this reader does not
    model at every level below the top, integer prices, local dates. wt is becoming the
    registry's writer, so if this reader cannot load wt's output every
    benchmark stops at "cannot read registry.toml"."""
    registry = load_registry(WRITTEN_FIXTURE)

    assert [p.id for p in registry.providers] == ["ollama", "openrouter", "mlx_lm_server", "agy"]
    assert registry.provider("ollama").location == "local"
    assert [m.id for m in registry.models] == [
        "ollama/written-fixture:int",
        "openrouter/written-fixture:cloud",
        "mlx_lm_server/written-fixture:pair",
        "agy/written-fixture:native",
    ]
    pair = registry.model("mlx_lm_server/written-fixture:pair")
    assert (pair.family, pair.provider_id, pair.model_name) == (
        "written-second",
        "mlx_lm_server",
        "org/written-fixture-target",
    )
    assert pair.fetch is not None and pair.fetch.repo == "org/written-fixture-target"
    assert pair.draft is not None and pair.draft.repo == "org/written-fixture-draft"
    cloud = registry.model("openrouter/written-fixture:cloud")
    assert cloud.location == "cloud" and not is_model_local(
        cloud.location, cloud.provider_id, registry
    )
```

- [ ] **Step 2: Run them to verify they fail**

Run, from `wt/`: `go test ./internal/tomlw -run TestWrittenFixtureIsAFixedPoint`

Expected: FAIL with `open ../../../docs/contracts/registry.written.sample.toml: no such file or directory`.

Run, from `modelman/`: `uv run pytest -q tests/contracts/test_registry_written_fixture.py`

Expected: 2 failed — the first with `FileNotFoundError` naming the fixture, the second with `RegistryNotFoundError: Registry file not found`.

Run, from `llmbench/`: `uv run pytest -q tests/test_registry.py -k written`

Expected: 1 failed with `RegistryError: Registry file not found`.

- [ ] **Step 3: Add the fixture**

Create `docs/contracts/registry.written.sample.toml` with exactly this content, ending in one newline after the last `]`:

```toml
families = [
    { name = "written-fixture", display_name = "Written Fixture" },
    { x_rank = 2, display_name = "Second Family", name = "written-second" },
]

[[providers]]
x_provider_note = "an unknown provider key"
id = "ollama"
name = "Ollama"
location = "local"
protocols = [
    "anthropic",
    "openai-chat",
]

[providers.auth]
x_auth_note = "an unknown auth key"
type = "none"
base_url = "http://localhost:11434"

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "OPENROUTER_API_KEY"
base_url = "https://openrouter.ai/api/v1"

[[providers]]
id = "mlx_lm_server"
name = "mlx-lm server (target+draft)"
location = "local"

[providers.auth]
type = "none"
base_url = "http://localhost:8001/v1"

[[providers]]
id = "agy"
name = "Agy"
location = "cloud"

[providers.auth]
type = "native"

[[models]]
catalog_name = "written-fixture"
x_added = 2026-10-01
x_local_checked = 2026-10-01 07:30:00
x_daily_at = 07:30:00
id = "ollama/written-fixture:int"
family = "written-fixture"
provider_id = "ollama"
model_name = "written-fixture:int"
location = "local"
tags = []

[models.cost]
input_price_per_million = 3
output_price_per_million = 15

[models.model_info]
supports_vision = false
supports_function_calling = true
max_input_tokens = 131072
x_aliases = [
    { alias = "wf" },
    { weight = 0.5, alias = "written" },
]

[[models]]
id = "openrouter/written-fixture:cloud"
family = "written-fixture"
provider_id = "openrouter"
model_name = "org/written-fixture-cloud"
location = "cloud"
source = "curated"
tags = [
    "code",
    "design",
]
pricing_updated_at = "2026-10-01T00:00:00+00:00"
x_checked_at = 2026-10-01 12:30:00+00:00

[models.cost]
x_cost_note = "an unknown cost key"
input_price_per_million = 0.5
cache_price_per_million = 0.125
output_price_per_million = 1.0
subscription_price = 20
subscription_period = "month"

[[models.cost.time_prices]]
x_row_note = "an unknown time-price key"
label = "off-peak"
timezone = "UTC"
input_price_per_million = 0.25
output_price_per_million = 1e-07

[[models.cost.time_prices.windows]]
days = [
    "mon",
    "tue",
]
start = "00:00"
end = "12:00"

[[models.cost.time_prices.windows]]
x_window_note = "an unknown window key"
days = [
    "sat",
    "sun",
]
start = "00:00"
end = "24:00"

[[models]]
id = "mlx_lm_server/written-fixture:pair"
family = "written-second"
provider_id = "mlx_lm_server"
model_name = "org/written-fixture-target"
tags = [
    "code",
]
quantization = "4bit"

[models.fetch]
x_fetch_note = "an unknown fetch key"
repo = "org/written-fixture-target"

[models.draft]
x_draft_note = "an unknown draft key"
repo = "org/written-fixture-draft"

[[models]]
id = "agy/written-fixture:native"
family = "written-second"
provider_id = "agy"
model_name = "written-fixture:native"
tags = [
    "native",
]
```

Indentation is four spaces. Do not add a comment or reorder a key: the file is the output of `tomli_w.dumps`, and modelman's test fails on anything else. If it ever has to change, change it by loading it with `tomllib`, editing the dictionary and writing it back with `tomli_w.dumps`.

- [ ] **Step 4: Run the three tests to verify they pass**

Run, from `wt/`: `go test -count=1 ./internal/tomlw ./internal/config`

Expected: both `ok`.

Run, from `modelman/`: `uv run pytest -q tests/contracts/test_registry_written_fixture.py`

Expected: 2 passed.

Run, from `llmbench/`: `uv run pytest -q tests/test_registry.py`

Expected: all pass.

- [ ] **Step 5: Confirm CI runs each test when the fixture changes**

Run, from the monorepo root:

```bash
grep -n 'docs/contracts' .github/workflows/llmbench-ci.yml .github/workflows/modelman-ci.yml .github/workflows/wt-ci.yml
```

Expected: `llmbench-ci.yml` lists `"docs/contracts/registry*.toml"` under both `push` and `pull_request`, and the other two list `"docs/contracts/**"`. All three already match the new file, so no workflow changes. If `llmbench-ci.yml` does not have that line, add `- "docs/contracts/registry*.toml"` to both of its `paths` lists.

- [ ] **Step 6: Document the package and the fixture**

In `CLAUDE.md`, replace:

```text
contracts/ (cross-language config-format fixtures, read by wt Go + modelman Python contract tests; llmbench reads `registry.sample.toml`)
```

with:

```text
contracts/ (cross-language config-format fixtures, read by wt Go + modelman Python contract tests; llmbench reads `registry.sample.toml` and `registry.written.sample.toml`, the registry in the exact form wt's writer and tomli-w give it — it holds no comments, so what it is for is recorded in its three tests)
```

In `wt/CLAUDE.md`, replace:

```text
Packages are `cmd/wt` plus `internal/{config,rotation,usage,refcount,survey,agents,profiles,guard,worktree,initseed,themes,tui,configeditor,ollamacheck,catalog,localmodels,lifecycle,litellm,smoke}`.
```

with:

```text
Packages are `cmd/wt` plus `internal/{config,tomlw,rotation,usage,refcount,survey,agents,profiles,guard,worktree,initseed,themes,tui,configeditor,ollamacheck,catalog,localmodels,lifecycle,litellm,smoke}`.
```

In `wt/CLAUDE.md`, replace:

```text
| `internal/rotation/` | global rotation state
```

with:

```text
| `internal/tomlw/` | ordered TOML document (`Decode`, `Table`) and an emitter (`Encode`) that reproduces tomli-w's layout byte for byte — what lets wt write `registry.toml` beside modelman without rewriting it. Imports nothing from wt; never use the stock `toml.Encoder` on the registry (it sorts keys and shifts local dates) |
| `internal/rotation/` | global rotation state
```

In `wt/docs/internals/config-and-registry.md`, replace:

```text
**Catalog membership.**
```

with:

```text
**The written form is pinned too.** [../docs/contracts/registry.written.sample.toml](../../../docs/contracts/registry.written.sample.toml) is one registry exactly as tomli-w lays it out, with keys neither tool models at every level below the top (both writers refuse an unknown top-level key), integer and float prices, an empty array, inline and `[[header]]` arrays of tables, `time_prices`, and offset and local datetimes. It has no comments (tomli-w writes none), so its purpose lives in its three tests: `internal/tomlw`'s `TestWrittenFixtureIsAFixedPoint` (decode, emit, same bytes), modelman's `tests/contracts/test_registry_written_fixture.py` (`tomli_w` gives the same bytes) and llmbench's `test_load_registry_reads_the_written_contract_fixture`. Regenerate it only with `tomli_w.dumps`; a hand edit that is not in tomli-w form fails the modelman test.

**Catalog membership.**
```

- [ ] **Step 7: Verify the PR and commit**

Run, from `wt/`:

```bash
test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check
```

Expected: every package `ok`; `make check` ends without an error.

Run, from `modelman/` and then from `llmbench/`: `make check && make test`

Expected: clean, all tests pass.

Run, from the monorepo root: `make test-all`

Expected: exit 0.

```bash
git add docs/contracts/registry.written.sample.toml wt/internal/tomlw/fixture_test.go wt/internal/config/registry_fixture_test.go modelman/tests/contracts/test_registry_written_fixture.py llmbench/tests/test_registry.py CLAUDE.md wt/CLAUDE.md wt/docs/internals/config-and-registry.md
git commit -m "test: a written-registry contract fixture, read by wt, modelman and llmbench"
```

Stop here. Pushing `feat/wt-tomlw` and opening the PR need the owner's OK.

---

## PR 4 — `RegistryDoc` and `UpdateRegistry`

Branch `feat/wt-registry-writer`, cut after PRs 1, 2 and 3 have merged. Still no command calls the writer; PR 5 adds the first.

Three files, three responsibilities:

- `registry_doc.go` — the document and the operations on it. The only code that knows registry key names on the write side.
- `registry_write.go` — `UpdateRegistry`: lock, resolve, read, decode, `apply`, encode, re-check, rename.
- `registry_validate.go` — what a touched row must satisfy before it is written.

### Task 10: `RegistryDoc`, a patch-shaped view of the registry

**Files:**
- Create: `wt/internal/config/registry_doc.go`
- Test: `wt/internal/config/registry_doc_test.go`

**Interfaces:**
- Consumes: `tomlw.Table`, `tomlw.NewTable`, `tomlw.Decode`, `tomlw.Encode`, `tomlw.Value`, `tomlw.Same`, `(*Table).SetAt`, `Clone`, `Delete` (PR 3); `config.Model`, `config.ModelCost` (for one test, by reflection only).
- Produces (package `config`):
  - `var ErrModelNotFound`, `ErrModelExists`, `ErrProviderExists`, `ErrRegistryTopLevel`
  - `type RegistryDoc struct` (fields unexported)
  - `func newRegistryDoc(root *tomlw.Table) (*RegistryDoc, error)` (unexported; `UpdateRegistry` calls it)
  - `func (d *RegistryDoc) PatchModel(id string, set map[string]any, unset []string) error`
  - `func (d *RegistryDoc) AddModel(table map[string]any) error`
  - `func (d *RegistryDoc) CloneModel(fromID string, overrides map[string]any) error`
  - `func (d *RegistryDoc) RemoveModel(id string) (*tomlw.Table, error)`
  - `func (d *RegistryDoc) SetTimePrices(id string, rows []map[string]any) error`
  - `func (d *RegistryDoc) AddProvider(table map[string]any) error`
  - `func (d *RegistryDoc) Models() []*tomlw.Table`, `Providers() []*tomlw.Table` (copies)
  - unexported helpers later tasks use: `d.rows(key string) []*tomlw.Table`, `rowID(row *tomlw.Table) string`, `tableArray(v any) ([]*tomlw.Table, bool)`, the fields `d.root`, `d.touchedModels`, `d.touchedProviders`
  - test helpers `parseDoc(t, text) *RegistryDoc`, `docText(t, doc) string` and the constant `docRegistry`

Keys in `set`, `unset` and `overrides` are dotted paths inside the row. Values may be `bool`, `string`, `int`, `int64`, `float64`, `time.Time`, `[]string`, `[]any`, `map[string]any`, `[]map[string]any` or `*tomlw.Table`.

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/config/registry_doc_test.go`:

```go
package config

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// docRegistry is a small registry in tomli-w form with keys wt does not model
// at the model, cost and fetch levels, an integer price, and a model with no
// tags key at all.
const docRegistry = `families = [
    { name = "fam", display_name = "Fam" },
]

[[providers]]
id = "ollama"
name = "Ollama"
location = "local"

[providers.auth]
type = "none"
base_url = "http://localhost:11434"

[[models]]
catalog_name = "alpha"
id = "ollama/alpha"
family = "fam"
provider_id = "ollama"
model_name = "alpha"
tags = [
    "code",
]

[models.cost]
x_cost_note = "kept"
input_price_per_million = 3
output_price_per_million = 15

[models.model_info]
supports_vision = false
max_input_tokens = 131072

[models.fetch]
x_fetch_note = "kept"
repo = "org/alpha"
local_path = "/models/alpha"

[[models]]
id = "ollama/beta"
family = "fam"
provider_id = "ollama"
model_name = "beta"
`

func parseDoc(t *testing.T, text string) *RegistryDoc {
	t.Helper()
	root, err := tomlw.Decode([]byte(text))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	doc, err := newRegistryDoc(root)
	if err != nil {
		t.Fatalf("newRegistryDoc: %v", err)
	}
	return doc
}

func docText(t *testing.T, doc *RegistryDoc) string {
	t.Helper()
	out, err := tomlw.Encode(doc.root)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return string(out)
}

// TestPatchModelChangesOnlyTheNamedKeys is the core promise of the writer: a
// patch sets and deletes the keys it names and every other line of the file,
// including keys wt does not model, comes back exactly as it was. A writer
// that rebuilt the row from wt's typed struct would drop catalog_name, fetch
// and the x_ keys here.
func TestPatchModelChangesOnlyTheNamedKeys(t *testing.T) {
	doc := parseDoc(t, docRegistry)
	err := doc.PatchModel("ollama/alpha",
		map[string]any{"family": "renamed", "cost.output_price_per_million": 12.5},
		[]string{"fetch.local_path", "model_info.supports_vision", "no_such_key", "draft.repo"})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.NewReplacer(
		"family = \"fam\"\nprovider_id = \"ollama\"\nmodel_name = \"alpha\"", "family = \"renamed\"\nprovider_id = \"ollama\"\nmodel_name = \"alpha\"",
		"output_price_per_million = 15", "output_price_per_million = 12.5",
		"supports_vision = false\n", "",
		"local_path = \"/models/alpha\"\n", "",
	).Replace(docRegistry)
	if got := docText(t, doc); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestPatchModelPutsNewKeysWhereModelmanWould pins where a key that was not
// there lands: at its schema position, with a table created on the way, and
// an unmodelled key ahead of the schema keys. modelman rewrites rows in this
// order on every save, so anything else would be moved back by modelman and
// show up as a second diff.
func TestPatchModelPutsNewKeysWhereModelmanWould(t *testing.T) {
	doc := parseDoc(t, docRegistry)
	err := doc.PatchModel("ollama/beta", map[string]any{
		"tags":                          []string{"design"},
		"location":                      "local",
		"cost.output_price_per_million": 2,
		"cost.input_price_per_million":  1,
		"pricing_updated_at":            "2026-10-07T00:00:00+00:00",
		"catalog_name":                  "beta",
		"draft":                         map[string]any{"local_path": "/d", "repo": "org/d", "x_note": "n"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	const wantRow = `[[models]]
catalog_name = "beta"
id = "ollama/beta"
family = "fam"
provider_id = "ollama"
model_name = "beta"
location = "local"
tags = [
    "design",
]
pricing_updated_at = "2026-10-07T00:00:00+00:00"

[models.cost]
input_price_per_million = 1
output_price_per_million = 2

[models.draft]
x_note = "n"
repo = "org/d"
local_path = "/d"
`
	got := docText(t, doc)
	if !strings.HasSuffix(got, "\n"+wantRow) {
		t.Errorf("the patched row should be:\n%s\ngot document:\n%s", wantRow, got)
	}
}

// TestPatchModelLeavesAnEqualValueAlone pins "a price key is written only
// when its value changes": asking for the value a key already has — 3.0 for
// an integer 3, the same tags, the same model_info — changes nothing and
// marks nothing as touched. Otherwise every edit of a model would rewrite
// `3` as `3.0` and reorder model_info.
func TestPatchModelLeavesAnEqualValueAlone(t *testing.T) {
	doc := parseDoc(t, docRegistry)
	err := doc.PatchModel("ollama/alpha", map[string]any{
		"cost.input_price_per_million":  3.0,
		"cost.output_price_per_million": 15.0,
		"tags":                          []string{"code"},
		"family":                        "fam",
		"model_info":                    map[string]any{"max_input_tokens": 131072, "supports_vision": false},
	}, []string{"cost.cache_price_per_million"})
	if err != nil {
		t.Fatal(err)
	}
	// A row with no tags key and no cost table stays that way: a writer that
	// went through the typed struct would add `tags = []` here.
	if err := doc.PatchModel("ollama/beta", map[string]any{"family": "fam", "model_name": "beta"}, []string{"tags", "cost.input_price_per_million"}); err != nil {
		t.Fatal(err)
	}
	if got := docText(t, doc); got != docRegistry {
		t.Errorf("an equal-valued patch changed the document:\n%s", got)
	}
	if len(doc.touchedModels) != 0 {
		t.Errorf("an equal-valued patch touched %d row(s), want 0", len(doc.touchedModels))
	}
}

// TestPatchModelRefusals pins the errors a caller can act on: an id that is
// not there is ErrModelNotFound (the CLI's "unknown id" exit), the id itself
// cannot be patched (history, rotation and profiles key on it), and a dotted
// key cannot go through a value that is not a table.
func TestPatchModelRefusals(t *testing.T) {
	doc := parseDoc(t, docRegistry)
	if err := doc.PatchModel("ollama/nope", map[string]any{"family": "x"}, nil); !errors.Is(err, ErrModelNotFound) || !strings.Contains(err.Error(), `"ollama/nope"`) {
		t.Errorf("unknown id: err = %v, want ErrModelNotFound naming the id", err)
	}
	if err := doc.PatchModel("ollama/alpha", map[string]any{"id": "ollama/other"}, nil); err == nil || !strings.Contains(err.Error(), "id cannot be patched") {
		t.Errorf("setting the id: err = %v", err)
	}
	if err := doc.PatchModel("ollama/alpha", nil, []string{"id"}); err == nil {
		t.Error("unsetting the id should be refused")
	}
	if err := doc.PatchModel("ollama/alpha", map[string]any{"family.name": "x"}, nil); err == nil || !strings.Contains(err.Error(), "family is not a table") {
		t.Errorf("a path through a string: err = %v", err)
	}
	if err := doc.PatchModel("ollama/alpha", map[string]any{"cost..x": 1}, nil); err == nil {
		t.Error("an empty path component should be refused")
	}
	if err := doc.PatchModel("ollama/alpha", map[string]any{"family": uint8(1)}, nil); err == nil || !strings.Contains(err.Error(), "family") {
		t.Errorf("a value TOML cannot hold: err = %v", err)
	}
	// A patch is all or nothing: the first key (sorted order) is valid and
	// the second fails, and neither is in the document afterwards.
	if err := doc.PatchModel("ollama/beta", map[string]any{"cost.input_price_per_million": -5, "family.name": "x"}, nil); err == nil || !strings.Contains(err.Error(), "family is not a table") {
		t.Errorf("a two-key patch whose second key fails: err = %v", err)
	}
	if got := docText(t, doc); got != docRegistry {
		t.Errorf("a refused patch changed the document:\n%s", got)
	}
}

// TestAddModelAppendsARowInSchemaOrder pins AddModel: the row goes last, its
// keys in modelman's order whatever order the Go map had, and a second row
// with the same id is ErrModelExists rather than a silent duplicate (which
// wt's own Validate then refuses to load).
func TestAddModelAppendsARowInSchemaOrder(t *testing.T) {
	doc := parseDoc(t, docRegistry)
	row := map[string]any{
		"tags":        []string{},
		"model_name":  "org/gamma",
		"provider_id": "ollama",
		"family":      "fam",
		"id":          "ollama/gamma",
		"source":      "discovered",
		"fetch":       map[string]any{"local_path": "/models/gamma", "repo": "org/gamma"},
		"cost": map[string]any{"time_prices": []map[string]any{{
			"windows":  []map[string]any{{"end": "24:00", "start": "00:00", "days": []string{"sat"}}},
			"timezone": "UTC", "label": "off-peak", "input_price_per_million": 0.5,
		}}},
	}
	if err := doc.AddModel(row); err != nil {
		t.Fatal(err)
	}
	const wantRow = `[[models]]
id = "ollama/gamma"
family = "fam"
provider_id = "ollama"
model_name = "org/gamma"
source = "discovered"
tags = []

[[models.cost.time_prices]]
label = "off-peak"
timezone = "UTC"
input_price_per_million = 0.5

[[models.cost.time_prices.windows]]
days = [
    "sat",
]
start = "00:00"
end = "24:00"

[models.fetch]
repo = "org/gamma"
local_path = "/models/gamma"
`
	if got := docText(t, doc); !strings.HasSuffix(got, "\n"+wantRow) || !strings.HasPrefix(got, docRegistry) {
		t.Errorf("the new row should be appended as:\n%s\ngot document:\n%s", wantRow, got)
	}
	if err := doc.AddModel(row); !errors.Is(err, ErrModelExists) {
		t.Errorf("adding the id again: err = %v, want ErrModelExists", err)
	}
	for _, bad := range []map[string]any{{"family": "fam"}, {"id": ""}, {"id": 7}} {
		if err := doc.AddModel(bad); err == nil {
			t.Errorf("AddModel(%v) should be refused: a row needs a string id", bad)
		}
	}
}

// TestCloneModelCopiesEveryKey pins CloneModel, which the catalog mirror uses
// to re-tag a model: the copy carries every key of the source — the ones wt
// does not model too — with only the overrides changed, and the source row is
// left exactly as it was.
func TestCloneModelCopiesEveryKey(t *testing.T) {
	doc := parseDoc(t, docRegistry)
	err := doc.CloneModel("ollama/alpha", map[string]any{
		"id": "ollama/alpha:cloud", "model_name": "alpha:cloud", "cost.input_price_per_million": 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	const wantRow = `[[models]]
catalog_name = "alpha"
id = "ollama/alpha:cloud"
family = "fam"
provider_id = "ollama"
model_name = "alpha:cloud"
tags = [
    "code",
]

[models.cost]
x_cost_note = "kept"
input_price_per_million = 4
output_price_per_million = 15

[models.model_info]
supports_vision = false
max_input_tokens = 131072

[models.fetch]
x_fetch_note = "kept"
repo = "org/alpha"
local_path = "/models/alpha"
`
	if got := docText(t, doc); !strings.HasSuffix(got, "\n"+wantRow) || !strings.HasPrefix(got, docRegistry) {
		t.Errorf("the clone should be appended as:\n%s\ngot document:\n%s", wantRow, got)
	}
	if err := doc.CloneModel("ollama/alpha", map[string]any{"model_name": "x"}); !errors.Is(err, ErrModelExists) {
		t.Errorf("a clone that keeps the source's id: err = %v, want ErrModelExists", err)
	}
	if err := doc.CloneModel("ollama/nope", map[string]any{"id": "x"}); !errors.Is(err, ErrModelNotFound) {
		t.Errorf("a clone of an unknown id: err = %v, want ErrModelNotFound", err)
	}
}

// TestRemoveModelReturnsTheRow pins RemoveModel: the row is gone from the
// document and handed back whole, because `wt model rm` never deletes weights
// and has to print where they are from the row it just removed.
func TestRemoveModelReturnsTheRow(t *testing.T) {
	doc := parseDoc(t, docRegistry)
	row, err := doc.RemoveModel("ollama/alpha")
	if err != nil {
		t.Fatal(err)
	}
	fetch, _ := row.Get("fetch")
	if path, _ := fetch.(*tomlw.Table).Get("local_path"); path != "/models/alpha" {
		t.Errorf("the removed row's fetch.local_path = %v, want /models/alpha", path)
	}
	got := docText(t, doc)
	if strings.Contains(got, "alpha") || !strings.Contains(got, `id = "ollama/beta"`) {
		t.Errorf("only ollama/alpha should be gone:\n%s", got)
	}
	if _, err := doc.RemoveModel("ollama/alpha"); !errors.Is(err, ErrModelNotFound) {
		t.Errorf("removing it again: err = %v, want ErrModelNotFound", err)
	}
	if _, err := doc.RemoveModel("ollama/beta"); err != nil {
		t.Fatal(err)
	}
	if got := docText(t, doc); !strings.Contains(got, "models = []\n") {
		t.Errorf("removing the last model should leave `models = []`, as modelman writes it:\n%s", got)
	}
}

// TestSetTimePricesReplacesTheRows pins the one operation on
// cost.time_prices: the rows are replaced as a set (the catalog mirror
// rewrites its off-peak row), rows equal to the ones there are left alone,
// and no rows deletes the key.
func TestSetTimePricesReplacesTheRows(t *testing.T) {
	rows := []map[string]any{{
		"label": "off-peak", "timezone": "UTC", "input_price_per_million": 1.5,
		"windows": []map[string]any{{"days": []string{"sat", "sun"}, "start": "00:00", "end": "24:00"}},
	}}
	doc := parseDoc(t, docRegistry)
	if err := doc.SetTimePrices("ollama/alpha", rows); err != nil {
		t.Fatal(err)
	}
	const wantCost = `[models.cost]
x_cost_note = "kept"
input_price_per_million = 3
output_price_per_million = 15

[[models.cost.time_prices]]
label = "off-peak"
timezone = "UTC"
input_price_per_million = 1.5

[[models.cost.time_prices.windows]]
days = [
    "sat",
    "sun",
]
start = "00:00"
end = "24:00"
`
	withRows := docText(t, doc)
	if !strings.Contains(withRows, wantCost) {
		t.Errorf("cost should read:\n%s\ngot document:\n%s", wantCost, withRows)
	}

	again := parseDoc(t, withRows)
	if err := again.SetTimePrices("ollama/alpha", rows); err != nil {
		t.Fatal(err)
	}
	if len(again.touchedModels) != 0 || docText(t, again) != withRows {
		t.Error("setting the same rows again should change nothing")
	}
	if err := again.SetTimePrices("ollama/alpha", nil); err != nil {
		t.Fatal(err)
	}
	if got := docText(t, again); got != docRegistry {
		t.Errorf("setting no rows should delete time_prices and nothing else:\n%s", got)
	}
	if err := again.SetTimePrices("ollama/nope", rows); !errors.Is(err, ErrModelNotFound) {
		t.Errorf("unknown id: err = %v, want ErrModelNotFound", err)
	}
}

// TestAddProviderAppendsARow pins the seeding operation: a provider row in
// modelman's key order, appended, with a duplicate id refused.
func TestAddProviderAppendsARow(t *testing.T) {
	doc := parseDoc(t, docRegistry)
	row := map[string]any{
		"auth": map[string]any{"base_url": "http://localhost:8003/v1", "type": "none"},
		"name": "MTPLX", "model_dir": "~/.mtplx/models", "id": "mtplx", "location": "local",
	}
	if err := doc.AddProvider(row); err != nil {
		t.Fatal(err)
	}
	const wantRow = `[[providers]]
id = "mtplx"
name = "MTPLX"
location = "local"
model_dir = "~/.mtplx/models"

[providers.auth]
type = "none"
base_url = "http://localhost:8003/v1"
`
	if got := docText(t, doc); !strings.Contains(got, wantRow) {
		t.Errorf("the provider should be written as:\n%s\ngot document:\n%s", wantRow, got)
	}
	if err := doc.AddProvider(row); !errors.Is(err, ErrProviderExists) {
		t.Errorf("adding the id again: err = %v, want ErrProviderExists", err)
	}
	if err := doc.AddProvider(map[string]any{"name": "x"}); err == nil {
		t.Error("a provider with no id should be refused")
	}
}

// TestOperationsCreateOnlyTheKnownTopLevelKeys pins "wt never adds a
// top-level key" from the other side: on an empty registry the operations
// create `providers` and `models`, at their places, and nothing else. A new
// top-level key is a file modelman refuses to load (#247).
func TestOperationsCreateOnlyTheKnownTopLevelKeys(t *testing.T) {
	doc := parseDoc(t, "")
	if err := doc.AddModel(map[string]any{"id": "ollama/a", "family": "f", "provider_id": "ollama", "model_name": "a"}); err != nil {
		t.Fatal(err)
	}
	if err := doc.AddProvider(map[string]any{"id": "ollama", "name": "Ollama", "auth": map[string]any{"type": "none"}}); err != nil {
		t.Fatal(err)
	}
	if got, want := doc.root.Keys(), []string{"providers", "models"}; !slices.Equal(got, want) {
		t.Errorf("top-level keys = %v, want %v", got, want)
	}
}

// TestModelsAndProvidersAreCopies pins the read views planners use: they see
// every row in file order, and changing what they were handed changes nothing
// in the document — a planner that edited a row in place would bypass the
// touched-row validation.
func TestModelsAndProvidersAreCopies(t *testing.T) {
	doc := parseDoc(t, docRegistry)
	models := doc.Models()
	if len(models) != 2 || rowID(models[0]) != "ollama/alpha" || rowID(models[1]) != "ollama/beta" {
		t.Fatalf("Models() = %d rows, want alpha then beta", len(models))
	}
	if name, _ := models[0].Get("catalog_name"); name != "alpha" {
		t.Errorf("a view should carry the keys wt does not model, got catalog_name = %v", name)
	}
	models[0].Set("family", "changed")
	cost, _ := models[0].Get("cost")
	cost.(*tomlw.Table).Set("input_price_per_million", int64(99))
	providers := doc.Providers()
	if len(providers) != 1 || rowID(providers[0]) != "ollama" {
		t.Fatalf("Providers() = %d rows, want ollama", len(providers))
	}
	providers[0].Delete("auth")
	if got := docText(t, doc); got != docRegistry {
		t.Errorf("changing a view changed the document:\n%s", got)
	}
}

// TestNewRegistryDocRefusesAnUnexpectedTopLevel pins the #247 rule on the
// write path: a top-level key that is not providers/families/models (the
// classic is [[model]] for [[models]], which parses and reads as no models)
// refuses the write and names the key, and so does one of the three that is
// not an array of tables.
func TestNewRegistryDocRefusesAnUnexpectedTopLevel(t *testing.T) {
	cases := []struct{ name, text, want string }{
		{"a misspelled section", "[[model]]\nid = \"a\"\n", "`model`"},
		{"two unknown keys, sorted", "zeta = 1\nalpha = 2\n[[models]]\nid = \"a\"\n", "`alpha`, `zeta`"},
		{"models is not an array of tables", "models = \"none\"\n", "`models` is not an array of tables"},
		{"providers is a table", "[providers]\nid = \"a\"\n", "`providers` is not an array of tables"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, err := tomlw.Decode([]byte(c.text))
			if err != nil {
				t.Fatal(err)
			}
			_, err = newRegistryDoc(root)
			if !errors.Is(err, ErrRegistryTopLevel) || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want ErrRegistryTopLevel naming %s", err, c.want)
			}
		})
	}
	for _, ok := range []string{"", "providers = []\nfamilies = []\nmodels = []\n"} {
		root, _ := tomlw.Decode([]byte(ok))
		if _, err := newRegistryDoc(root); err != nil {
			t.Errorf("%q should be accepted: %v", ok, err)
		}
	}
}

// tomlKeys lists the TOML key of every field of a struct type, skipping "-".
func tomlKeys(typ reflect.Type) []string {
	var keys []string
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("toml"), ",")
		if name != "" && name != "-" {
			keys = append(keys, name)
		}
	}
	return keys
}

// TestAWriteNeverTouchesAKeyItWasNotAskedTo pins the property that makes the
// writer safe to grow beside: what a write touches is decided by the keys the
// caller names, never by the fields of config.Model. The row here holds every
// key config.Model and config.ModelCost decode — read from the struct tags, so
// a field added later is covered without editing this test — each with the
// zero value an `omitempty` struct round trip would drop. A patch of one
// unrelated key must leave all of them in the file.
func TestAWriteNeverTouchesAKeyItWasNotAskedTo(t *testing.T) {
	row := tomlw.NewTable()
	for _, k := range tomlKeys(reflect.TypeOf(Model{})) {
		row.Set(k, "")
	}
	row.Set("id", "ollama/zero")
	cost := tomlw.NewTable()
	for _, k := range tomlKeys(reflect.TypeOf(ModelCost{})) {
		cost.Set(k, "")
	}
	row.Set("cost", cost)
	root := tomlw.NewTable()
	root.Set("models", []any{row})
	doc, err := newRegistryDoc(root)
	if err != nil {
		t.Fatal(err)
	}
	before := docText(t, doc)

	if err := doc.PatchModel("ollama/zero", map[string]any{"x_unrelated": true}, nil); err != nil {
		t.Fatal(err)
	}
	after := docText(t, doc)
	if want := strings.Replace(before, "[[models]]\n", "[[models]]\nx_unrelated = true\n", 1); after != want {
		t.Errorf("a patch of one key changed others.\nbefore:\n%s\nafter:\n%s", before, after)
	}
	for _, k := range append(tomlKeys(reflect.TypeOf(Model{})), tomlKeys(reflect.TypeOf(ModelCost{}))...) {
		if !strings.Contains(after, "\n"+k+" = ") && !strings.Contains(after, "[models."+k+"]") {
			t.Errorf("key %q, which config.Model decodes, is gone after an unrelated patch", k)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run, from `wt/`: `go test ./internal/config -run 'PatchModel|AddModel|CloneModel|RemoveModel|SetTimePrices|AddProvider|TopLevel|AreCopies|AWriteNever'`

Expected: FAIL — the package does not build: `undefined: RegistryDoc`, `undefined: newRegistryDoc`, `undefined: ErrModelNotFound`.

- [ ] **Step 3: Write `RegistryDoc`**

Create `wt/internal/config/registry_doc.go`:

```go
package config

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// Errors the registry operations return, each wrapped with the id or keys it
// is about. Callers match them with errors.Is.
var (
	// ErrModelNotFound: no model row has the id.
	ErrModelNotFound = errors.New("model not found")
	// ErrModelExists: a model row already has the id.
	ErrModelExists = errors.New("model already exists")
	// ErrProviderExists: a provider row already has the id.
	ErrProviderExists = errors.New("provider already exists")
	// ErrRegistryTopLevel: registry.toml holds a top-level key that is not
	// providers, families or models, or one of those is not an array of
	// tables. The write is refused: `[[model]]` for `[[models]]` parses, reads
	// as no models, and the Python tools refuse such a file outright (#247).
	ErrRegistryTopLevel = errors.New("registry.toml has an unexpected top level")
)

// The keys of each kind of registry table, in the order modelman writes them
// (registry.py's _provider_to_dict, _model_to_dict, _cost_to_dict and
// time_pricing.py's time_price_to_dict). A key wt adds to a row goes at its
// place in these lists, so a row wt edits looks like one modelman wrote and
// modelman's next save does not move it. They order keys; they are not a list
// of what a row may hold.
var (
	registryTopLevelKeys = []string{"providers", "families", "models"}

	providerSchemas = map[string][]string{
		"":     {"id", "name", "location", "model_dir", "protocols", "auth"},
		"auth": {"type", "secret_ref", "base_url"},
	}
	modelSchemas = map[string][]string{
		"": {"id", "family", "provider_id", "model_name", "location", "source", "tags", "cost",
			"model_info", "fetch", "quantization", "pricing_updated_at", "draft"},
		"cost": {"input_price_per_million", "cache_price_per_million", "output_price_per_million",
			"subscription_price", "subscription_period", "time_prices"},
		"cost.time_prices": {"label", "timezone", "input_price_per_million", "cache_price_per_million",
			"output_price_per_million", "windows"},
		"cost.time_prices.windows": {"days", "start", "end"},
		"fetch":                    {"repo", "files", "quantizations", "local_path"},
		"draft":                    {"repo", "local_path"},
	}
)

// RegistryDoc is registry.toml as UpdateRegistry hands it to a writer: the
// whole document, with every key in place, and a small set of operations on
// it. The operations are patch-shaped — each names the keys it sets or
// deletes — and nothing here reads or writes a config.Model or
// config.Provider. That is what keeps a key wt does not model (fetch, draft,
// catalog_name, anything hand-added) out of harm's way, and why adding a
// field to config.Model cannot change what a write touches.
//
// A key is addressed by its dotted path inside the row:
// "cost.input_price_per_million", "fetch.repo". A key whose own name holds a
// dot cannot be addressed; set the table that holds it instead.
//
// A value may be a bool, string, int, int64, float64, time.Time, []string,
// []any, map[string]any, []map[string]any or *tomlw.Table. A map's keys are
// written in schema order, unknown ones first and sorted.
type RegistryDoc struct {
	root *tomlw.Table
	// touched are the rows an operation changed or added, by kind. Only
	// these are validated before the write.
	touchedModels    []*tomlw.Table
	touchedProviders []*tomlw.Table
}

// newRegistryDoc wraps a decoded registry, refusing a top level a write
// cannot be trusted with.
func newRegistryDoc(root *tomlw.Table) (*RegistryDoc, error) {
	var unknown []string
	for _, k := range root.Keys() {
		if !slices.Contains(registryTopLevelKeys, k) {
			unknown = append(unknown, "`"+k+"`")
			continue
		}
		v, _ := root.Get(k)
		if _, ok := tableArray(v); !ok {
			return nil, fmt.Errorf("%w: `%s` is not an array of tables", ErrRegistryTopLevel, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("%w: unknown top-level key(s) %s: a registry holds providers/families/models only",
			ErrRegistryTopLevel, strings.Join(unknown, ", "))
	}
	return &RegistryDoc{root: root}, nil
}

// tableArray reads v as an array holding only tables.
func tableArray(v any) ([]*tomlw.Table, bool) {
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	rows := make([]*tomlw.Table, len(arr))
	for i, e := range arr {
		t, ok := e.(*tomlw.Table)
		if !ok {
			return nil, false
		}
		rows[i] = t
	}
	return rows, true
}

func (d *RegistryDoc) rows(key string) []*tomlw.Table {
	v, _ := d.root.Get(key)
	rows, _ := tableArray(v)
	return rows
}

func (d *RegistryDoc) setRows(key string, rows []*tomlw.Table) {
	arr := make([]any, len(rows))
	for i := range rows {
		arr[i] = rows[i]
	}
	d.root.SetAt(key, arr, registryTopLevelKeys)
}

func rowID(row *tomlw.Table) string {
	v, _ := row.Get("id")
	id, _ := v.(string)
	return id
}

func findRow(rows []*tomlw.Table, id string) int {
	return slices.IndexFunc(rows, func(r *tomlw.Table) bool { return rowID(r) == id })
}

func (d *RegistryDoc) touchModel(row *tomlw.Table) {
	if !slices.Contains(d.touchedModels, row) {
		d.touchedModels = append(d.touchedModels, row)
	}
}

// Models returns a copy of every model row, in file order. Changing a copy
// changes nothing in the document.
func (d *RegistryDoc) Models() []*tomlw.Table { return cloneRows(d.rows("models")) }

// Providers returns a copy of every provider row, in file order.
func (d *RegistryDoc) Providers() []*tomlw.Table { return cloneRows(d.rows("providers")) }

func cloneRows(rows []*tomlw.Table) []*tomlw.Table {
	out := make([]*tomlw.Table, len(rows))
	for i := range rows {
		out[i] = rows[i].Clone()
	}
	return out
}

// PatchModel sets and deletes named keys on the model row id; every other
// key stays as it is. A key whose value is already the one asked for is not
// rewritten — an integer price asked to be the same number as a float stays
// an integer. A new key goes at its schema position. The id itself cannot be
// patched: a row under a new id is CloneModel's job.
func (d *RegistryDoc) PatchModel(id string, set map[string]any, unset []string) error {
	rows := d.rows("models")
	i := findRow(rows, id)
	if i < 0 {
		return fmt.Errorf("%w: %q", ErrModelNotFound, id)
	}
	for key := range set {
		if key == "id" {
			return fmt.Errorf("model %q: the id cannot be patched", id)
		}
	}
	if slices.Contains(unset, "id") {
		return fmt.Errorf("model %q: the id cannot be patched", id)
	}
	// Patch a copy and swap it in only when every key applied: a patch that
	// fails on its second key must not leave the first one in the document,
	// where a caller that tolerates the error would write it unvalidated.
	work := rows[i].Clone()
	changed, err := patchRow(work, modelSchemas, set, unset)
	if err != nil {
		return fmt.Errorf("model %q: %w", id, err)
	}
	if changed {
		*rows[i] = *work
		d.touchModel(rows[i])
	}
	return nil
}

// AddModel appends a model row. The table needs a non-empty string id that
// no row has yet.
func (d *RegistryDoc) AddModel(table map[string]any) error {
	id, _ := table["id"].(string)
	if id == "" {
		return errors.New("a model needs a non-empty string id")
	}
	rows := d.rows("models")
	if findRow(rows, id) >= 0 {
		return fmt.Errorf("%w: %q", ErrModelExists, id)
	}
	row, err := orderedTable(table, modelSchemas, "")
	if err != nil {
		return fmt.Errorf("model %q: %w", id, err)
	}
	d.setRows("models", append(rows, row))
	d.touchModel(row)
	return nil
}

// CloneModel appends a copy of the model row fromID with overrides set on it
// (dotted keys, as PatchModel's set). overrides must give the copy an id no
// row has; everything else, unknown keys included, is carried over.
func (d *RegistryDoc) CloneModel(fromID string, overrides map[string]any) error {
	rows := d.rows("models")
	i := findRow(rows, fromID)
	if i < 0 {
		return fmt.Errorf("%w: %q", ErrModelNotFound, fromID)
	}
	row := rows[i].Clone()
	if _, err := patchRow(row, modelSchemas, overrides, nil); err != nil {
		return fmt.Errorf("clone of model %q: %w", fromID, err)
	}
	id := rowID(row)
	if id == "" {
		return fmt.Errorf("clone of model %q: the copy needs a non-empty string id", fromID)
	}
	if findRow(rows, id) >= 0 {
		return fmt.Errorf("%w: %q", ErrModelExists, id)
	}
	d.setRows("models", append(rows, row))
	d.touchModel(row)
	return nil
}

// RemoveModel deletes the model row id and returns it, so the caller can
// still read what it held (where its weights are, say).
func (d *RegistryDoc) RemoveModel(id string) (*tomlw.Table, error) {
	rows := d.rows("models")
	i := findRow(rows, id)
	if i < 0 {
		return nil, fmt.Errorf("%w: %q", ErrModelNotFound, id)
	}
	row := rows[i]
	d.setRows("models", slices.Delete(slices.Clone(rows), i, i+1))
	d.touchedModels = slices.DeleteFunc(d.touchedModels, func(r *tomlw.Table) bool { return r == row })
	return row, nil
}

// SetTimePrices replaces the model's cost.time_prices with rows; no rows
// deletes the key. Rows equal to the ones already there are not rewritten.
func (d *RegistryDoc) SetTimePrices(id string, rows []map[string]any) error {
	if len(rows) == 0 {
		return d.PatchModel(id, nil, []string{"cost.time_prices"})
	}
	return d.PatchModel(id, map[string]any{"cost.time_prices": rows}, nil)
}

// AddProvider appends a provider row. The table needs a non-empty string id
// that no row has yet. Seeding is its only caller.
func (d *RegistryDoc) AddProvider(table map[string]any) error {
	id, _ := table["id"].(string)
	if id == "" {
		return errors.New("a provider needs a non-empty string id")
	}
	rows := d.rows("providers")
	if findRow(rows, id) >= 0 {
		return fmt.Errorf("%w: %q", ErrProviderExists, id)
	}
	row, err := orderedTable(table, providerSchemas, "")
	if err != nil {
		return fmt.Errorf("provider %q: %w", id, err)
	}
	d.setRows("providers", append(rows, row))
	d.touchedProviders = append(d.touchedProviders, row)
	return nil
}

// patchRow applies set (in sorted key order, so the result does not depend on
// map iteration) and then unset to row, and reports whether anything changed.
func patchRow(row *tomlw.Table, schemas map[string][]string, set map[string]any, unset []string) (bool, error) {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	changed := false
	for _, key := range keys {
		did, err := setPath(row, schemas, key, set[key])
		if err != nil {
			return false, err
		}
		changed = changed || did
	}
	for _, key := range unset {
		did, err := unsetPath(row, key)
		if err != nil {
			return false, err
		}
		changed = changed || did
	}
	return changed, nil
}

func splitKey(key string) ([]string, error) {
	parts := strings.Split(key, ".")
	if slices.Contains(parts, "") {
		return nil, fmt.Errorf("%q is not a key", key)
	}
	return parts, nil
}

// setPath sets the dotted key on row, creating the tables on the way at
// their schema positions. It reports false when the key already had the
// value.
func setPath(row *tomlw.Table, schemas map[string][]string, key string, value any) (bool, error) {
	parts, err := splitKey(key)
	if err != nil {
		return false, err
	}
	val, err := orderedValue(value, schemas, key)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	cur := row
	for i, part := range parts[:len(parts)-1] {
		parent := strings.Join(parts[:i], ".")
		next, ok := cur.Get(part)
		if !ok {
			sub := tomlw.NewTable()
			cur.SetAt(part, sub, schemas[parent])
			cur = sub
			continue
		}
		sub, isTable := next.(*tomlw.Table)
		if !isTable {
			return false, fmt.Errorf("%s: %s is not a table", key, strings.Join(parts[:i+1], "."))
		}
		cur = sub
	}
	last := parts[len(parts)-1]
	if old, ok := cur.Get(last); ok && tomlw.Same(old, val) {
		return false, nil
	}
	cur.SetAt(last, val, schemas[strings.Join(parts[:len(parts)-1], ".")])
	return true, nil
}

// unsetPath deletes the dotted key from row and reports whether it was
// there. A table left empty by the delete stays: modelman writes an empty
// [models.cost] too, and removing it is the caller's to ask for.
func unsetPath(row *tomlw.Table, key string) (bool, error) {
	parts, err := splitKey(key)
	if err != nil {
		return false, err
	}
	cur := row
	for _, part := range parts[:len(parts)-1] {
		next, _ := cur.Get(part)
		sub, ok := next.(*tomlw.Table)
		if !ok {
			return false, nil
		}
		cur = sub
	}
	return cur.Delete(parts[len(parts)-1]), nil
}

// orderedValue converts a caller's value into a document value, giving every
// map the key order of the table at path.
func orderedValue(v any, schemas map[string][]string, path string) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		return orderedTable(x, schemas, path)
	case *tomlw.Table:
		if x == nil {
			return nil, errors.New("nil table")
		}
		return x.Clone(), nil
	case []map[string]any:
		out := make([]any, len(x))
		for i := range x {
			row, err := orderedTable(x[i], schemas, path)
			if err != nil {
				return nil, err
			}
			out[i] = row
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i := range x {
			item, err := orderedValue(x[i], schemas, path)
			if err != nil {
				return nil, err
			}
			out[i] = item
		}
		return out, nil
	}
	return tomlw.Value(v)
}

// orderedTable builds the table at path from m: the keys the schema does not
// list first, sorted (where modelman writes the keys it does not model), then
// the schema's keys in schema order.
func orderedTable(m map[string]any, schemas map[string][]string, path string) (*tomlw.Table, error) {
	schema := schemas[path]
	var unknown []string
	for k := range m {
		if !slices.Contains(schema, k) {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	t := tomlw.NewTable()
	for _, k := range append(unknown, schema...) {
		v, ok := m[k]
		if !ok {
			continue
		}
		child := k
		if path != "" {
			child = path + "." + k
		}
		val, err := orderedValue(v, schemas, child)
		if err != nil {
			return nil, err
		}
		t.Set(k, val)
	}
	return t, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run, from `wt/`: `go test -count=1 ./internal/config`

Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
test -z "$(gofmt -l .)" && go vet ./...
git add internal/config/registry_doc.go internal/config/registry_doc_test.go
git commit -m "feat(wt): RegistryDoc, patch-shaped operations on registry.toml"
```

### Task 11: `UpdateRegistry`, the one write path, and the test guard

**Files:**
- Create: `wt/internal/config/registry_write.go`
- Modify: `wt/internal/config/fortest.go` (whole file replaced)
- Test: `wt/internal/config/registry_write_test.go`

**Interfaces:**
- Consumes: `RegistryPath()`, `resolveRegistryFile(path) (target string, exists bool, err error)` and `withFileLock(lockPath string, fn func() error) error` (PR 1); `registryEnvNames` and the `internal/config` `TestMain` (PR 2); `WriteFileAtomic(path string, data []byte, perm os.FileMode) error`; `newRegistryDoc`, `RegistryDoc`, `docRegistry` (Task 10); `linkedRegistry(t)` from `registry_link_test.go` (PR 1); `tomlw.Decode`, `tomlw.Encode`.
- Produces (package `config`):
  - `func UpdateRegistry(apply func(*RegistryDoc) error) (changed bool, err error)`
  - `var ErrRegistryBusy`
  - seams `var registryWriteGuard func(named, target string) error` and `var registryBeforeRename = func() {}`
  - in `fortest.go`: `errRegistryGuarded`, `guardedRegistryPaths`, `developerRegistryPaths() []string`, `resolveExisting(p string) string`, `refuseRegistryPaths(protected []string) func(named, target string) error`; `IsolateConfigHomeForTest` arms the guard
  - `func RegistryWriteGuardArmed() bool` (exported, in `fortest.go`) — whether this process refuses writes to the developer's registry; a test binary that reaches the writer asserts it (Task 15)
  - test helpers `scratchRegistry(t, content) string`, `readFile(t, path) string`, `dirNames(t, dir) []string`, `setFamily(id, family)`, `otherWriter(t, path, times) *int`

In this task `UpdateRegistry` does not validate yet; Task 12 adds that step. Its doc comment already describes all six steps.

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/config/registry_write_test.go`:

```go
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// scratchRegistry gives the test its own config home holding registry.toml
// with content ("" for no file) and returns the registry path.
func scratchRegistry(t *testing.T, content string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("WT_REGISTRY", "")
	t.Setenv("MODELMAN_REGISTRY", "")
	path := filepath.Join(home, "local-ai", "registry.toml")
	if content != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// dirNames lists a directory, for asserting that nothing was left behind.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func setFamily(id, family string) func(*RegistryDoc) error {
	return func(d *RegistryDoc) error {
		return d.PatchModel(id, map[string]any{"family": family}, nil)
	}
}

// TestUpdateRegistryWritesAPatch is the write path end to end: a patch lands
// in the file, wt's own reader sees it, and the rest of the file is as it
// was. If this breaks, `wt model edit` reports success and changes nothing.
func TestUpdateRegistryWritesAPatch(t *testing.T) {
	path := scratchRegistry(t, docRegistry)
	changed, err := UpdateRegistry(setFamily("ollama/beta", "renamed"))
	if err != nil || !changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
	}
	want := strings.Replace(docRegistry, "id = \"ollama/beta\"\nfamily = \"fam\"", "id = \"ollama/beta\"\nfamily = \"renamed\"", 1)
	if got := readFile(t, path); got != want {
		t.Errorf("file after the write:\n%s\nwant:\n%s", got, want)
	}
	_, models, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[1].Family != "renamed" {
		t.Errorf("the reader should see the new family, got %+v", models)
	}
	if got, want := dirNames(t, filepath.Dir(path)), []string{"registry.toml", "registry.toml.lock"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("registry directory holds %v, want %v (no temp file left behind)", got, want)
	}
}

// TestUpdateRegistryNoOpLeavesTheFileByteIdentical is the byte-stability
// promise on the cross-language fixture: a patch that asks for the values a
// row already has — on the row with integer prices, an empty tags array and a
// model_info table — writes nothing, so the bytes and the modification time
// are untouched. A no-op that rewrote the file would make every `wt model
// init` look like a change to modelman's stale-snapshot guard.
func TestUpdateRegistryNoOpLeavesTheFileByteIdentical(t *testing.T) {
	fixture := readFile(t, "../../../docs/contracts/registry.written.sample.toml")
	path := scratchRegistry(t, fixture)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := UpdateRegistry(func(d *RegistryDoc) error {
		return d.PatchModel("ollama/written-fixture:int", map[string]any{
			"cost.input_price_per_million":  3.0,
			"cost.output_price_per_million": 15.0,
			"tags":                          []string{},
			"model_info": map[string]any{
				"max_input_tokens": 131072, "supports_function_calling": true, "supports_vision": false,
				"x_aliases": []map[string]any{{"alias": "wf"}, {"alias": "written", "weight": 0.5}},
			},
		}, []string{"cost.cache_price_per_million"})
	})
	if err != nil || changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want (false, nil)", changed, err)
	}
	if got := readFile(t, path); got != fixture {
		t.Errorf("a no-op write changed the file:\n%s", got)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("a no-op write replaced the file (its modification time changed)")
	}
}

// TestUpdateRegistryNoOpOnARowWithNoTagsKey pins the spec's byte-stability
// case: integer prices, a model_info table and no tags key at all. A writer
// that went through config.Model would add `tags = []` and rewrite 3 as 3.0
// on every edit, so every save by either tool would flip the same lines.
func TestUpdateRegistryNoOpOnARowWithNoTagsKey(t *testing.T) {
	const reg = "[[models]]\nid = \"ollama/a\"\nfamily = \"f\"\nprovider_id = \"ollama\"\nmodel_name = \"a\"\n\n[models.cost]\ninput_price_per_million = 3\noutput_price_per_million = 15\n\n[models.model_info]\nmax_input_tokens = 131072\n"
	path := scratchRegistry(t, reg)
	changed, err := UpdateRegistry(func(d *RegistryDoc) error {
		return d.PatchModel("ollama/a", map[string]any{
			"cost.input_price_per_million": 3.0, "cost.output_price_per_million": 15.0,
			"model_info": map[string]any{"max_input_tokens": 131072},
		}, []string{"cost.cache_price_per_million"})
	})
	if err != nil || changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want (false, nil)", changed, err)
	}
	if got := readFile(t, path); got != reg {
		t.Errorf("a no-op write changed the file:\n%s", got)
	}
}

// TestUpdateRegistryNoOpKeepsAHandFormattedFile pins the same promise for a
// registry a person formatted: comments, their own layout. wt cannot emit
// that form, so the rule is that it does not have to — a write that changes
// nothing leaves the file alone, and only the first real change lays it out
// in tomli-w's form (as every modelman save always has).
func TestUpdateRegistryNoOpKeepsAHandFormattedFile(t *testing.T) {
	const hand = `# my models
[[providers]]
id = "ollama"   # the local daemon
name = "Ollama"
location = "local"
auth = { type = "none" }

[[models]]
id = "ollama/a"
family = "f"
provider_id = "ollama"
model_name = "a"
tags = ["code"]
`
	path := scratchRegistry(t, hand)
	changed, err := UpdateRegistry(setFamily("ollama/a", "f"))
	if err != nil || changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want (false, nil)", changed, err)
	}
	if got := readFile(t, path); got != hand {
		t.Errorf("a no-op write reformatted a hand-written file:\n%s", got)
	}
	if changed, err := UpdateRegistry(setFamily("ollama/a", "g")); err != nil || !changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
	}
	if got := readFile(t, path); strings.Contains(got, "#") || !strings.Contains(got, "family = \"g\"") {
		t.Errorf("a real change should write the file in tomli-w form:\n%s", got)
	}
}

// TestUpdateRegistryCreatesAMissingRegistry pins the first-run case: no
// file, no directory. A successful apply creates both, private (0600, like
// the file modelman creates), even when it adds nothing — which is how `wt
// model init` leaves a registry behind on a machine with nothing installed.
func TestUpdateRegistryCreatesAMissingRegistry(t *testing.T) {
	path := scratchRegistry(t, "")
	changed, err := UpdateRegistry(func(*RegistryDoc) error { return nil })
	if err != nil || !changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the registry was not created: %v", err)
	}
	if info.Size() != 0 || info.Mode().Perm() != 0o600 {
		t.Errorf("new registry: size %d mode %v, want empty and 0600", info.Size(), info.Mode().Perm())
	}
	if _, _, err := loadRegistry(); err != nil {
		t.Errorf("the reader should load an empty registry: %v", err)
	}
	if changed, err := UpdateRegistry(func(*RegistryDoc) error { return nil }); err != nil || changed {
		t.Errorf("a second no-op = (%v, %v), want (false, nil)", changed, err)
	}
}

// TestUpdateRegistryKeepsTheFilesMode pins that a write does not tighten or
// loosen an existing registry's permissions: a temp file is created 0600, and
// renaming it over a 0644 file would silently change who can read it.
func TestUpdateRegistryKeepsTheFilesMode(t *testing.T) {
	path := scratchRegistry(t, docRegistry)
	for _, mode := range []os.FileMode{0o644, 0o600, 0o640} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := UpdateRegistry(setFamily("ollama/beta", "mode-"+mode.String())); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Errorf("mode after a write = %v, want %v", info.Mode().Perm(), mode)
		}
	}
}

// TestUpdateRegistryWritesThroughASymlink pins the write side of #248: a
// registry linked into a dotfiles checkout is written in the checkout, and
// the link is still a link afterwards. Renaming onto the link itself would
// replace it with a regular file and the checkout would silently stop
// receiving edits. The lock file belongs beside the link, not in the
// checkout.
func TestUpdateRegistryWritesThroughASymlink(t *testing.T) {
	link, target := linkedRegistry(t)
	if err := os.WriteFile(target, []byte(docRegistry), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := UpdateRegistry(setFamily("ollama/beta", "linked")); err != nil || !changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("the registry path is no longer a symlink")
	}
	if got := readFile(t, target); !strings.Contains(got, `family = "linked"`) {
		t.Errorf("the link's target was not written:\n%s", got)
	}
	if got := dirNames(t, filepath.Dir(target)); fmt.Sprint(got) != "[registry.toml]" {
		t.Errorf("the checkout directory holds %v, want only registry.toml (no lock, no temp file)", got)
	}
	if got := dirNames(t, filepath.Dir(link)); fmt.Sprint(got) != "[registry.toml registry.toml.lock]" {
		t.Errorf("the config directory holds %v, want the link and its lock", got)
	}
}

// TestUpdateRegistryRefusesADanglingSymlink pins the other half of #248: a
// registry link whose target is gone is an error, and nothing is created.
// Writing the target would recreate a registry on a volume that is not
// mounted, from an empty document, shadowing the real one when it comes back.
func TestUpdateRegistryRefusesADanglingSymlink(t *testing.T) {
	link, target := linkedRegistry(t)
	ran := false
	changed, err := UpdateRegistry(func(*RegistryDoc) error { ran = true; return nil })
	if !errors.Is(err, ErrRegistryLink) || changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want ErrRegistryLink", changed, err)
	}
	if ran {
		t.Error("apply ran against a registry that could not be read")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Errorf("the link's target must not be created; Lstat error = %v", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link itself must be left alone (err %v)", err)
	}
}

// TestUpdateRegistryRefusesAnUnknownTopLevelKey pins #247 for the writer: a
// registry with a section wt does not know is not written at all, the error
// names the key and the file, and apply never runs. Writing it would either
// carry forward a section the Python tools refuse to load, or drop it.
func TestUpdateRegistryRefusesAnUnknownTopLevelKey(t *testing.T) {
	content := "schema_version = 1\n" + docRegistry
	path := scratchRegistry(t, content)
	ran := false
	changed, err := UpdateRegistry(func(*RegistryDoc) error { ran = true; return nil })
	if !errors.Is(err, ErrRegistryTopLevel) || changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want ErrRegistryTopLevel", changed, err)
	}
	for _, want := range []string{"`schema_version`", path} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %s", err, want)
		}
	}
	if ran {
		t.Error("apply ran on a registry the writer refused")
	}
	if got := readFile(t, path); got != content {
		t.Error("the refused registry was modified")
	}
}

// TestUpdateRegistryRefusesAFileItCannotParse pins that a malformed registry
// is an error naming the file, never an empty document to write over — the
// data loss #240 was about.
func TestUpdateRegistryRefusesAFileItCannotParse(t *testing.T) {
	const broken = "[[models]\nid = \"a\"\n"
	path := scratchRegistry(t, broken)
	changed, err := UpdateRegistry(func(*RegistryDoc) error { return nil })
	if err == nil || changed || !strings.Contains(err.Error(), "parse "+path) {
		t.Fatalf("UpdateRegistry = (%v, %v), want a parse error naming the file", changed, err)
	}
	if got := readFile(t, path); got != broken {
		t.Error("the malformed registry was modified")
	}
}

// TestUpdateRegistryReturnsApplysErrorAndWritesNothing pins the failure path
// every caller relies on: an apply that fails (an unknown id, a duplicate)
// leaves the file as it was, with its error intact for errors.Is.
func TestUpdateRegistryReturnsApplysErrorAndWritesNothing(t *testing.T) {
	path := scratchRegistry(t, docRegistry)
	changed, err := UpdateRegistry(func(d *RegistryDoc) error {
		if err := d.PatchModel("ollama/beta", map[string]any{"family": "half-done"}, nil); err != nil {
			return err
		}
		return d.PatchModel("ollama/nope", map[string]any{"family": "x"}, nil)
	})
	if !errors.Is(err, ErrModelNotFound) || changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want ErrModelNotFound", changed, err)
	}
	if got := readFile(t, path); got != docRegistry {
		t.Errorf("a failed apply changed the file:\n%s", got)
	}
}

// otherWriter plays modelman saving the registry in the window between wt
// encoding its write and replacing the file: it rewrites the file the first
// `times` times the seam fires, appending a model each time.
func otherWriter(t *testing.T, path string, times int) *int {
	t.Helper()
	fired := 0
	old := registryBeforeRename
	registryBeforeRename = func() {
		if fired >= times {
			return
		}
		fired++
		extra := fmt.Sprintf("\n[[models]]\nid = \"ollama/other-%d\"\nfamily = \"fam\"\nprovider_id = \"ollama\"\nmodel_name = \"other-%d\"\n", fired, fired)
		if err := os.WriteFile(path, []byte(readFile(t, path)+extra), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { registryBeforeRename = old })
	return &fired
}

// TestUpdateRegistryRetriesWhenTheFileChangesUnderIt pins the re-check that
// covers the writer wt cannot lock out: modelman takes no cross-process lock,
// so it can save between wt's read and wt's rename. wt must then start over
// on the new file, so both edits survive — wt's blind rename would have
// reverted modelman's.
func TestUpdateRegistryRetriesWhenTheFileChangesUnderIt(t *testing.T) {
	path := scratchRegistry(t, docRegistry)
	fired := otherWriter(t, path, 1)
	runs := 0
	changed, err := UpdateRegistry(func(d *RegistryDoc) error {
		runs++
		return d.PatchModel("ollama/beta", map[string]any{"family": "mine"}, nil)
	})
	if err != nil || !changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
	}
	if runs != 2 || *fired != 1 {
		t.Errorf("apply ran %d time(s) with %d outside write(s), want 2 and 1", runs, *fired)
	}
	got := readFile(t, path)
	if !strings.Contains(got, `family = "mine"`) || !strings.Contains(got, `id = "ollama/other-1"`) {
		t.Errorf("both edits should survive:\n%s", got)
	}
}

// TestUpdateRegistryGivesUpAfterThreeAttempts pins the bound on that retry:
// apply runs at most three times, then the caller gets ErrRegistryBusy and
// the file is whatever the other writer left — never a mix, and never a wt
// write on top of a file wt has not read.
func TestUpdateRegistryGivesUpAfterThreeAttempts(t *testing.T) {
	path := scratchRegistry(t, docRegistry)
	fired := otherWriter(t, path, 99)
	runs := 0
	changed, err := UpdateRegistry(func(d *RegistryDoc) error {
		runs++
		return d.PatchModel("ollama/beta", map[string]any{"family": "mine"}, nil)
	})
	if !errors.Is(err, ErrRegistryBusy) || changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want ErrRegistryBusy", changed, err)
	}
	if runs != 3 || *fired != 3 {
		t.Errorf("apply ran %d time(s) with %d outside write(s), want 3 and 3", runs, *fired)
	}
	got := readFile(t, path)
	if strings.Contains(got, `family = "mine"`) || !strings.Contains(got, `id = "ollama/other-3"`) {
		t.Errorf("the file should be the other writer's, without wt's edit:\n%s", got)
	}
}

// TestUpdateRegistrySerializesConcurrentWriters pins the lock: eight writers
// at once each add one model and all eight are in the file. Without the flock
// two of them read the same bytes and the second rename drops the first's
// model — the lost update `wt cloud-sync` beside an open `wt config` would hit.
func TestUpdateRegistrySerializesConcurrentWriters(t *testing.T) {
	path := scratchRegistry(t, docRegistry)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("ollama/concurrent-%d", i)
			_, err := UpdateRegistry(func(d *RegistryDoc) error {
				return d.AddModel(map[string]any{"id": id, "family": "fam", "provider_id": "ollama", "model_name": id})
			})
			if err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	got := readFile(t, path)
	for i := 0; i < 8; i++ {
		if !strings.Contains(got, fmt.Sprintf(`id = "ollama/concurrent-%d"`, i)) {
			t.Errorf("writer %d's model is missing from the file", i)
		}
	}
}

// TestUpdateRegistryWritesARedirectedRegistry pins that the registry write
// itself is not subject to the ErrRegistryRedirected rule. That rule protects
// LiteLLM's config.yaml, which follows neither registry variable; writing a
// scratch registry is exactly what a redirect is for. The refusal belongs to
// the route sync a command runs afterwards, in internal/litellm.
func TestUpdateRegistryWritesARedirectedRegistry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MODELMAN_REGISTRY", "")
	t.Setenv("WT_LITELLM_CONFIG", "")
	t.Setenv("MODELMAN_LITELLM_CONFIG", "")
	path := filepath.Join(t.TempDir(), "scratch", "registry.toml")
	t.Setenv("WT_REGISTRY", path)
	if !RegistryRedirected() {
		t.Fatal("fixture error: the registry should read as redirected")
	}
	changed, err := UpdateRegistry(func(d *RegistryDoc) error {
		return d.AddModel(map[string]any{"id": "ollama/a", "family": "f", "provider_id": "ollama", "model_name": "a"})
	})
	if err != nil || !changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
	}
	if got := readFile(t, path); !strings.Contains(got, `id = "ollama/a"`) {
		t.Errorf("the redirected registry was not written:\n%s", got)
	}
}

// TestUpdateRegistryAsksTheWriteGuardFirst pins the seam that keeps a test
// binary off the developer's registry: a guarded path is refused before
// anything touches the disk — no lock file, no directory, no write — and
// apply never runs. The guarded path here is a temp file; the next test
// checks what TestMain really guards.
func TestUpdateRegistryAsksTheWriteGuardFirst(t *testing.T) {
	path := scratchRegistry(t, "")
	old := registryWriteGuard
	registryWriteGuard = refuseRegistryPaths([]string{path})
	t.Cleanup(func() { registryWriteGuard = old })

	ran := false
	changed, err := UpdateRegistry(func(*RegistryDoc) error { ran = true; return nil })
	if !errors.Is(err, errRegistryGuarded) || changed || ran {
		t.Fatalf("UpdateRegistry = (%v, %v), apply ran = %v; want the guard's refusal and no apply", changed, err, ran)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Errorf("a guarded write must not create the registry directory or its lock; Stat error = %v", err)
	}

	// Through a symlink too: the guard sees the file the link resolves to.
	link, target := linkedRegistry(t)
	if err := os.WriteFile(target, []byte(docRegistry), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	registryWriteGuard = refuseRegistryPaths([]string{resolved})
	if _, err := UpdateRegistry(setFamily("ollama/beta", "x")); !errors.Is(err, errRegistryGuarded) {
		t.Errorf("a link (%s) to a guarded file: err = %v, want the guard's refusal", link, err)
	}
	if got := readFile(t, target); got != docRegistry {
		t.Error("a guarded registry was written through its link")
	}

	// And through a linked directory: the registry is named under a symlink
	// to the guarded file's config home (a dotfiles layout, or /tmp for
	// /private/tmp), and does not exist yet.
	realHome, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realHome, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", alias)
	registryWriteGuard = refuseRegistryPaths([]string{filepath.Join(realHome, "local-ai", "registry.toml")})
	if _, err := UpdateRegistry(func(*RegistryDoc) error { return nil }); !errors.Is(err, errRegistryGuarded) {
		t.Errorf("a guarded registry named through a linked directory: err = %v, want the guard's refusal", err)
	}
	if names := dirNames(t, realHome); len(names) != 0 {
		t.Errorf("a guarded write created %v under the linked directory", names)
	}
}

// TestTestMainGuardsTheDevelopersRegistry pins what this package's TestMain
// arms through IsolateConfigHomeForTest: UpdateRegistry refuses the registry
// under the developer's real home, whatever a test does to the environment.
// It asks the guard directly and writes nothing.
func TestTestMainGuardsTheDevelopersRegistry(t *testing.T) {
	if registryWriteGuard == nil {
		t.Fatal("registryWriteGuard is not set: TestMain must call IsolateConfigHomeForTest")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	real := filepath.Join(home, ".config", "local-ai", "registry.toml")
	if err := registryWriteGuard(real, ""); !errors.Is(err, errRegistryGuarded) {
		t.Errorf("guard(%s) = %v, want a refusal", real, err)
	}
	if err := registryWriteGuard(filepath.Join(t.TempDir(), "registry.toml"), ""); err != nil {
		t.Errorf("a temp registry should be allowed, got %v", err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run, from `wt/`: `go test ./internal/config -run 'UpdateRegistry|TestTestMainGuards'`

Expected: FAIL — the package does not build: `undefined: UpdateRegistry`, `undefined: registryWriteGuard`, `undefined: refuseRegistryPaths`, `undefined: registryBeforeRename`, `undefined: ErrRegistryBusy`.

- [ ] **Step 3: Write `UpdateRegistry`**

Create `wt/internal/config/registry_write.go`:

```go
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// ErrRegistryBusy is returned by UpdateRegistry when registry.toml changed
// under it on every attempt: another program (modelman, an editor) kept
// writing the file. Nothing was written; running the command again is safe.
var ErrRegistryBusy = errors.New("registry.toml kept changing while wt was writing it")

// registryWriteAttempts is how many times UpdateRegistry runs apply before
// giving up with ErrRegistryBusy.
const registryWriteAttempts = 3

// registryWriteGuard, when set, is asked before a registry write goes ahead,
// with the registry path as named and (once known) the file it resolves to.
// Production never sets it. IsolateConfigHomeForTest sets it to refuse the
// developer's real registry, so a test that reaches UpdateRegistry without
// redirecting the registry fails instead of rewriting it. It is a seam, not
// an `import "testing"`: the shipped binary carries one nil check.
var registryWriteGuard func(named, target string) error

// registryBeforeRename runs after a write is encoded and before the file is
// re-checked and replaced. Tests swap it to play the part of another program
// writing the file in that window.
var registryBeforeRename = func() {}

// UpdateRegistry is the only way wt writes registry.toml. It reads the file,
// hands it to apply as a RegistryDoc, and writes the result back:
//
//  1. It takes a flock on <registry path>.lock — the path as named, not a
//     symlink's target — so two wt writers never interleave.
//  2. It resolves the path. A symlink that leads nowhere is ErrRegistryLink,
//     never "missing"; a symlink that resolves is written through, so the
//     link survives (#248).
//  3. It reads and decodes the file. A missing file is an empty document. An
//     unknown top-level key refuses the write and names the keys (#247).
//  4. It runs apply.
//  5. It validates the rows apply touched, and only those (ErrRegistryInvalid).
//  6. It skips the write when the file exists and apply changed nothing, so
//     a no-op leaves the file byte-identical, comments and all. Otherwise it
//     re-reads the file: if another program changed it since
//     step 3 it starts over from step 2, up to three runs of apply, then
//     returns ErrRegistryBusy. If not, it replaces the file atomically, with
//     the mode it had (0600 for a new file).
//
// apply must be pure. It may run more than once, so it must not print, run a
// command, call a server or change anything outside the document it is given.
// A result it hands back through a captured variable must be assigned afresh
// on every run, never appended to.
//
// changed reports whether the file was written. A registry that did not
// exist is created by any apply that succeeds, even one that adds nothing.
// The first write that does change something lays the whole file out in
// tomli-w's form, which drops comments — as every modelman save always has.
//
// UpdateRegistry never touches LiteLLM's config.yaml and does not ask whether
// the registry is redirected: a write to a redirected registry succeeds, and
// it is the route sync a caller runs afterwards that litellm refuses
// (ErrRegistryRedirected) unless WT_LITELLM_CONFIG names the config.yaml.
func UpdateRegistry(apply func(*RegistryDoc) error) (changed bool, err error) {
	path := RegistryPath()
	if registryWriteGuard != nil {
		// Before the lock: taking it creates the lock file and its directory.
		if err := registryWriteGuard(path, ""); err != nil {
			return false, err
		}
	}
	err = withFileLock(path+".lock", func() error {
		for range registryWriteAttempts {
			var retry bool
			var err error
			changed, retry, err = updateRegistryOnce(path, apply)
			if err != nil || !retry {
				return err
			}
		}
		return fmt.Errorf("%w (%s)", ErrRegistryBusy, path)
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

// readRegistryFile resolves and reads the registry: the file to write, its
// bytes, and whether it exists. A missing file reads as no bytes.
func readRegistryFile(path string) (target string, data []byte, exists bool, err error) {
	target, exists, err = resolveRegistryFile(path)
	if err != nil || !exists {
		return target, nil, false, err
	}
	data, err = os.ReadFile(target)
	if os.IsNotExist(err) {
		return target, nil, false, nil
	}
	return target, data, err == nil, err
}

// updateRegistryOnce is one attempt: steps 2 to 6 of UpdateRegistry. retry is
// true when the file changed between the read and the write.
func updateRegistryOnce(path string, apply func(*RegistryDoc) error) (changed, retry bool, err error) {
	target, before, exists, err := readRegistryFile(path)
	if err != nil {
		return false, false, err
	}
	if registryWriteGuard != nil {
		if err := registryWriteGuard(path, target); err != nil {
			return false, false, err
		}
	}
	root, err := tomlw.Decode(before)
	if err != nil {
		return false, false, fmt.Errorf("parse %s: %w", path, err)
	}
	doc, err := newRegistryDoc(root)
	if err != nil {
		return false, false, fmt.Errorf("%s: %w", path, err)
	}
	// What the document encodes to before apply runs. Comparing against this,
	// not against the file's bytes, is what lets a hand-formatted registry
	// (comments, its own layout) survive a write that changes nothing.
	unchanged, err := tomlw.Encode(root)
	if err != nil {
		return false, false, fmt.Errorf("encode %s: %w", path, err)
	}
	if err := apply(doc); err != nil {
		return false, false, err
	}
	after, err := tomlw.Encode(root)
	if err != nil {
		return false, false, fmt.Errorf("encode %s: %w", path, err)
	}
	if exists && bytes.Equal(unchanged, after) {
		return false, false, nil
	}

	registryBeforeRename()
	nowTarget, now, nowExists, err := readRegistryFile(path)
	if err != nil {
		return false, false, err
	}
	if nowTarget != target || nowExists != exists || !bytes.Equal(now, before) {
		return false, true, nil
	}
	mode := os.FileMode(0o600)
	if exists {
		info, err := os.Stat(target)
		if err != nil {
			return false, false, err
		}
		mode = info.Mode().Perm()
	}
	if err := WriteFileAtomic(target, after, mode); err != nil {
		return false, false, err
	}
	return true, false, nil
}
```

- [ ] **Step 4: Arm the guard in the test helper**

Replace the whole of `wt/internal/config/fortest.go` with:

```go
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// IsolateConfigHomeForTest points the test process's config environment at a
// throwaway directory, so Dir(), RegistryPath() and everything derived from
// them never resolve into the developer's real ~/.config/agent-wt. The test
// binaries that need it (cmd/wt, internal/tui and internal/config) call it
// from their TestMain: their tests launch stub agents, record usage, take
// profile locks and load the config, and without this every run rewrote the
// real rotation.state with a test model and appended wt-stub launches to the
// real usage.jsonl. WT_REGISTRY and MODELMAN_REGISTRY are cleared for the same
// reason: either would send Load to the developer's registry whatever XDG
// says. A test that sets its own XDG_CONFIG_HOME (t.Setenv) still wins.
//
// It also arms registryWriteGuard for the rest of the process: UpdateRegistry
// then refuses the registry this environment named before the redirect, and
// the default one under the home directory, so a test that undoes the
// redirect (an empty XDG_CONFIG_HOME with the real HOME) fails instead of
// rewriting the developer's registry.
//
// The returned home names the throwaway directory (its name is what
// TestConfigHomeIsNotTheDevelopersOwn pins on); the returned cleanup removes
// it and must run after m.Run(), before os.Exit. Production code never calls
// it — like internal/localmodels' OnDiskSnapshotForTest, it exists so
// cross-package tests share one definition instead of a copy each.
func IsolateConfigHomeForTest() (home string, cleanup func()) {
	// Before the environment changes: this is the developer's registry.
	guardedRegistryPaths = append(guardedRegistryPaths, developerRegistryPaths()...)
	registryWriteGuard = refuseRegistryPaths(guardedRegistryPaths)
	dir, err := os.MkdirTemp("", "wt-test-config-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CONFIG_HOME", dir)
	for _, name := range registryEnvNames {
		os.Unsetenv(name)
	}
	return dir, func() { os.RemoveAll(dir) }
}

// errRegistryGuarded is what registryWriteGuard returns in a test binary.
var errRegistryGuarded = errors.New("a test tried to write the developer's registry")

// guardedRegistryPaths is every path IsolateConfigHomeForTest has protected
// in this process. It only grows: a second call (a test of the helper itself)
// must not drop the paths the first one found.
var guardedRegistryPaths []string

// developerRegistryPaths lists the registry files a test must never write:
// the one the current environment names, the default one under the home
// directory, and what each of those resolves to through symlinks — the file's
// own link or a linked directory above it.
func developerRegistryPaths() []string {
	paths := []string{RegistryPath()}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, ".config", "local-ai", "registry.toml"))
	}
	for _, p := range slices.Clone(paths) {
		paths = append(paths, resolveExisting(p))
	}
	for i := range paths {
		paths[i] = filepath.Clean(paths[i])
	}
	return paths
}

// resolveExisting follows the symlinks in p as far as p exists: the longest
// leading part that is on disk is resolved and the rest is joined back on. A
// registry that has not been created yet, under a directory reached through a
// link, still resolves to where it would be written.
func resolveExisting(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for dir := p; ; {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return p
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

// refuseRegistryPaths builds a registryWriteGuard that refuses a write whose
// named path or resolved target is one of protected, as spelled or with its
// symlinks followed (a linked directory, or /tmp for /private/tmp).
func refuseRegistryPaths(protected []string) func(named, target string) error {
	return func(named, target string) error {
		for _, p := range []string{named, target} {
			if p == "" {
				continue
			}
			if slices.Contains(protected, filepath.Clean(p)) || slices.Contains(protected, resolveExisting(p)) {
				return fmt.Errorf("%w: %s", errRegistryGuarded, p)
			}
		}
		return nil
	}
}

// RegistryWriteGuardArmed reports whether this process refuses writes to the
// developer's registry. Test binaries assert it; production never calls it.
func RegistryWriteGuardArmed() bool { return registryWriteGuard != nil }
```

`fortest.go` is compiled into the wt binary, as it always was; it imports nothing from `testing`, and production code never calls `IsolateConfigHomeForTest`, so `registryWriteGuard` stays nil there.

- [ ] **Step 5: Run the tests to verify they pass**

Run, from `wt/`: `go test -count=1 ./internal/config`

Expected: `ok`.

Then with the race detector, for the concurrent-writers test:

```bash
go test -count=1 -race ./internal/config -run UpdateRegistry
```

Expected: `ok`.

- [ ] **Step 6: Confirm the suite left the real registry directory alone**

`bin/check-config-dirs-untouched` is the backstop in `wt-ci`, but it only means something on a fresh runner (on a developer's machine the directories exist and it always fails). Locally, compare a listing of the real directory from before and after the tests; the listing has sizes and times, so a rewritten registry or a new lock file shows. Run, from `wt/`:

```bash
ls -la ~/.config/local-ai/ > /tmp/local-ai.before 2>&1
go test -count=1 ./internal/config
ls -la ~/.config/local-ai/ 2>&1 | diff /tmp/local-ai.before - && echo untouched
```

Expected: the tests pass and the last line is `untouched`. Any diff means a test reached `UpdateRegistry` on the real path; find it before going on. (Only the listing is compared. Do not print the registry itself: it can hold API keys.)

- [ ] **Step 7: Commit**

```bash
test -z "$(gofmt -l .)" && go vet ./...
git add internal/config/registry_write.go internal/config/fortest.go internal/config/registry_write_test.go
git commit -m "feat(wt): UpdateRegistry, the one locked write path for registry.toml (#247, #248)"
```

### Task 12: Validate the rows a write touched, and only those

**Files:**
- Create: `wt/internal/config/registry_validate.go`
- Modify: `wt/internal/config/registry_write.go` (three lines in `updateRegistryOnce`)
- Test: `wt/internal/config/registry_validate_test.go`

**Interfaces:**
- Consumes: `RegistryDoc`'s `touchedModels` and `touchedProviders`, `rowID`, `tableArray` (Task 10); `UpdateRegistry`, `scratchRegistry`, `readFile`, `setFamily` (Task 11); `config.Model`, `config.Provider` (typed decode); `tomlw.Encode`.
- Produces (package `config`):
  - `var ErrRegistryInvalid`
  - `func (d *RegistryDoc) validateTouched() error` (unexported)

The rules are modelman's, because modelman still reads the file wt writes: `_parse_model` and `_parse_provider` (required keys), `_parse_cost` and `_validate_cost` (`registry.py`), and `parse_time_prices`, `Window` and `TimePrice` (`time_pricing.py:129`). On top of them, the row is run through the same struct decode wt's reader uses.

What this does not check: wt's `Config.Validate` also refuses a model whose `location` does not resolve to `local` or `cloud` (its own value, or its provider's) and one whose `provider_id` names no provider row. Both rules need other rows, the spec's step 5 does not list them, and Step 2's only caller adds provider rows. They are Step 3's to add, before `wt model add` and `wt model edit` can write such a row ("What Step 2 leaves for later steps").

- [ ] **Step 1: Write the failing test**

Create `wt/internal/config/registry_validate_test.go`:

```go
package config

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestUpdateRegistryValidatesOnlyTheRowsItTouched pins both halves of the
// validation rule. A row this write did not touch may be as broken as it
// likes — the write may be the edit or removal that repairs the file, and a
// hand-edited bad row must not block every price refresh. A row this write
// did touch must pass wt's typed decode and modelman's row rules, or nothing
// is written. (The rules that need other rows — a location that resolves, a
// provider_id that names a provider — are not checked here yet.)
func TestUpdateRegistryValidatesOnlyTheRowsItTouched(t *testing.T) {
	// ollama/broken has no family and a negative price: modelman refuses the
	// first, both tools the second.
	const withBadRow = docRegistry + `
[[models]]
id = "ollama/broken"
provider_id = "ollama"
model_name = "broken"

[models.cost]
input_price_per_million = -1
`
	t.Run("a bad row elsewhere does not block the write", func(t *testing.T) {
		path := scratchRegistry(t, withBadRow)
		if changed, err := UpdateRegistry(setFamily("ollama/beta", "fine")); err != nil || !changed {
			t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
		}
		if got := readFile(t, path); !strings.Contains(got, `family = "fine"`) || !strings.Contains(got, `id = "ollama/broken"`) {
			t.Errorf("the good row should be written and the bad row kept:\n%s", got)
		}
	})
	t.Run("and the bad row can be removed", func(t *testing.T) {
		path := scratchRegistry(t, withBadRow)
		_, err := UpdateRegistry(func(d *RegistryDoc) error {
			_, err := d.RemoveModel("ollama/broken")
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, path); got != docRegistry {
			t.Errorf("removing the bad row should leave the original registry:\n%s", got)
		}
	})
	t.Run("touching the bad row without fixing it is refused", func(t *testing.T) {
		path := scratchRegistry(t, withBadRow)
		_, err := UpdateRegistry(func(d *RegistryDoc) error {
			return d.PatchModel("ollama/broken", map[string]any{"tags": []string{"code"}}, nil)
		})
		if !errors.Is(err, ErrRegistryInvalid) || !strings.Contains(err.Error(), `model "ollama/broken": family is required`) {
			t.Fatalf("err = %v, want ErrRegistryInvalid naming the row and the missing key", err)
		}
		if got := readFile(t, path); got != withBadRow {
			t.Error("a refused write changed the file")
		}
	})

	invalid := []struct {
		name  string
		set   map[string]any
		unset []string
		want  string
	}{
		{"a required key deleted", nil, []string{"model_name"}, "model_name is required"},
		{"an empty model name", map[string]any{"model_name": ""}, nil, "model_name must be a non-empty string"},
		{"tags that are not a list", map[string]any{"tags": "code"}, nil, "tags"},
		{"cost that is not a table", map[string]any{"cost": "free"}, nil, "cost must be a table"},
		{"a negative price", map[string]any{"cost.input_price_per_million": -0.5}, nil, "input_price_per_million must be non-negative"},
		{"a price that is not a number", map[string]any{"cost.output_price_per_million": "3"}, nil, "output_price_per_million must be a number"},
		{"a boolean price", map[string]any{"cost.cache_price_per_million": true}, nil, "cache_price_per_million must be a number"},
		{"a subscription with no period", map[string]any{"cost.subscription_price": 20}, nil, "subscription_period must be month or year"},
		{"a subscription period that is not one", map[string]any{"cost.subscription_price": 20, "cost.subscription_period": "week"}, nil, "subscription_period must be month or year"},
		{"an unknown legacy cost kind", map[string]any{"cost.kind": "metered"}, nil, "kind must be free/per_token/subscription"},
		{"time_prices that are not rows", map[string]any{"cost.time_prices": "off-peak"}, nil, "time_prices must be an array of tables"},
		{"a time price with an unknown zone", map[string]any{"cost.time_prices": []map[string]any{{"timezone": "Mars/Olympus", "windows": []map[string]any{{"days": []string{"mon"}, "start": "00:00", "end": "01:00"}}}}}, nil, `timezone "Mars/Olympus" is not a known IANA timezone`},
		{"a time price with no zone", map[string]any{"cost.time_prices": []map[string]any{{"windows": []map[string]any{{"days": []string{"mon"}, "start": "00:00", "end": "01:00"}}}}}, nil, `timezone "" is not a known IANA timezone`},
		{"a time price with no windows", map[string]any{"cost.time_prices": []map[string]any{{"timezone": "UTC"}}}, nil, "windows must be a non-empty array of tables"},
		{"a window on an unknown day", map[string]any{"cost.time_prices": []map[string]any{{"timezone": "UTC", "windows": []map[string]any{{"days": []string{"funday"}, "start": "00:00", "end": "01:00"}}}}}, nil, "days must be drawn from"},
		{"a window that ends before it starts", map[string]any{"cost.time_prices": []map[string]any{{"timezone": "UTC", "windows": []map[string]any{{"days": []string{"mon"}, "start": "12:00", "end": "09:00"}}}}}, nil, "start must be before end"},
		{"a window time that is not HH:MM", map[string]any{"cost.time_prices": []map[string]any{{"timezone": "UTC", "windows": []map[string]any{{"days": []string{"mon"}, "start": "9am", "end": "12:00"}}}}}, nil, "start must be HH:MM"},
		{"a window past midnight", map[string]any{"cost.time_prices": []map[string]any{{"timezone": "UTC", "windows": []map[string]any{{"days": []string{"mon"}, "start": "00:00", "end": "24:30"}}}}}, nil, "end must be HH:MM between 00:00 and 24:00"},
	}
	for _, c := range invalid {
		t.Run(c.name, func(t *testing.T) {
			path := scratchRegistry(t, docRegistry)
			changed, err := UpdateRegistry(func(d *RegistryDoc) error {
				return d.PatchModel("ollama/beta", c.set, c.unset)
			})
			if !errors.Is(err, ErrRegistryInvalid) || changed || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("UpdateRegistry = (%v, %v), want ErrRegistryInvalid mentioning %q", changed, err, c.want)
			}
			if !strings.Contains(err.Error(), `model "ollama/beta"`) {
				t.Errorf("error %q should name the row", err)
			}
			if got := readFile(t, path); got != docRegistry {
				t.Error("a refused write changed the file")
			}
		})
	}
	t.Run("a provider row with no auth type", func(t *testing.T) {
		scratchRegistry(t, docRegistry)
		for _, row := range []map[string]any{
			{"id": "p1", "name": "P"},
			{"id": "p2", "name": "P", "auth": map[string]any{"base_url": "http://x"}},
			{"id": "p3", "name": "P", "auth": map[string]any{"type": "none"}, "protocols": "openai-chat"},
		} {
			_, err := UpdateRegistry(func(d *RegistryDoc) error { return d.AddProvider(row) })
			if !errors.Is(err, ErrRegistryInvalid) || !strings.Contains(err.Error(), fmt.Sprintf("provider %q", row["id"])) {
				t.Errorf("AddProvider(%v): err = %v, want ErrRegistryInvalid naming the row", row, err)
			}
		}
	})
}
```

- [ ] **Step 2: Run it to verify it fails**

Run, from `wt/`: `go test ./internal/config -run TestUpdateRegistryValidatesOnlyTheRowsItTouched`

Expected: FAIL — the package does not build: `undefined: ErrRegistryInvalid`.

- [ ] **Step 3: Write the validation**

Create `wt/internal/config/registry_validate.go`:

```go
package config

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// ErrRegistryInvalid is returned by UpdateRegistry when a row the write
// touched would not load: in wt's own typed reader, or in modelman's, whose
// required-field and cost rules are ported here because modelman still reads
// the file wt writes. Nothing is written.
var ErrRegistryInvalid = errors.New("invalid registry entry")

// validateTouched checks the rows this write changed or added, and only
// those. A bad row elsewhere in the file is not this write's doing and must
// not block it: the write may be the very edit or removal that repairs the
// file.
func (d *RegistryDoc) validateTouched() error {
	for _, row := range d.touchedProviders {
		if err := validateProviderRow(row); err != nil {
			return fmt.Errorf("%w: provider %q: %w", ErrRegistryInvalid, rowID(row), err)
		}
	}
	for _, row := range d.touchedModels {
		if err := validateModelRow(row); err != nil {
			return fmt.Errorf("%w: model %q: %w", ErrRegistryInvalid, rowID(row), err)
		}
	}
	return nil
}

// validateProviderRow is modelman's _parse_provider (an id, and an auth table
// with a type) plus wt's typed decode.
func validateProviderRow(row *tomlw.Table) error {
	if err := requireStrings(row, "id"); err != nil {
		return err
	}
	auth, _ := row.Get("auth")
	authTable, ok := auth.(*tomlw.Table)
	if !ok {
		return errors.New("auth must be a table with a `type`")
	}
	if err := requireStrings(authTable, "type"); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	return typedDecode("providers", row, &struct {
		Providers []Provider `toml:"providers"`
	}{})
}

// validateModelRow is modelman's _parse_model and _parse_cost plus wt's
// typed decode and its one rule of its own, a non-empty model_name.
func validateModelRow(row *tomlw.Table) error {
	if err := requireStrings(row, "id", "family", "provider_id", "model_name"); err != nil {
		return err
	}
	if cost, ok := row.Get("cost"); ok {
		table, isTable := cost.(*tomlw.Table)
		if !isTable {
			return errors.New("cost must be a table")
		}
		if err := validateCost(table); err != nil {
			return fmt.Errorf("cost: %w", err)
		}
	}
	return typedDecode("models", row, &struct {
		Models []Model `toml:"models"`
	}{})
}

// requireStrings checks that each key is a non-empty string. modelman only
// requires the keys to be present; an empty id or model_name is wt's own
// validation error (Config.validate), and an empty family or provider_id
// names nothing.
func requireStrings(row *tomlw.Table, keys ...string) error {
	for _, k := range keys {
		v, ok := row.Get(k)
		if !ok {
			return fmt.Errorf("%s is required", k)
		}
		if s, isString := v.(string); !isString || s == "" {
			return fmt.Errorf("%s must be a non-empty string", k)
		}
	}
	return nil
}

// typedDecode runs one row through the struct decode wt's reader uses, so a
// value of the wrong type (tags = "code", a string price) is caught before
// it is written rather than on the next load.
func typedDecode(key string, row *tomlw.Table, into any) error {
	doc := tomlw.NewTable()
	doc.Set(key, []any{row})
	text, err := tomlw.Encode(doc)
	if err != nil {
		return err
	}
	if _, err := toml.Decode(string(text), into); err != nil {
		return err
	}
	return nil
}

var (
	priceKeys           = []string{"input_price_per_million", "cache_price_per_million", "output_price_per_million"}
	subscriptionPeriods = []string{"month", "year"}
	legacyCostKinds     = []string{"free", "per_token", "subscription"}
	weekDays            = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
	hhmm                = regexp.MustCompile(`^([0-9]{2}):([0-9]{2})$`)
)

// validateCost ports modelman's cost rules (registry.py _parse_cost and
// _validate_cost, time_pricing.py parse_time_prices).
func validateCost(cost *tomlw.Table) error {
	if kind, ok := cost.Get("kind"); ok {
		// The legacy shape, which modelman still migrates on load. Its other
		// keys are read under different names; only the kind is checked.
		if s, _ := kind.(string); !slices.Contains(legacyCostKinds, s) {
			return fmt.Errorf("kind must be free/per_token/subscription, got %v", kind)
		}
	} else {
		for _, k := range append(slices.Clone(priceKeys), "subscription_price") {
			if err := checkPrice(cost, k); err != nil {
				return err
			}
		}
		period, hasPeriod := cost.Get("subscription_period")
		if _, isString := period.(string); hasPeriod && !isString {
			return errors.New("subscription_period must be a string")
		}
		if cost.Has("subscription_price") {
			if s, _ := period.(string); !slices.Contains(subscriptionPeriods, s) {
				return fmt.Errorf("subscription_period must be month or year when subscription_price is set, got %q", s)
			}
		}
	}
	raw, ok := cost.Get("time_prices")
	if !ok {
		return nil
	}
	rows, isRows := tableArray(raw)
	if !isRows {
		return errors.New("time_prices must be an array of tables")
	}
	for i, row := range rows {
		if err := validateTimePrice(row); err != nil {
			return fmt.Errorf("time_prices[%d]: %w", i, err)
		}
	}
	return nil
}

// checkPrice: a number (never a bool), finite, not negative.
func checkPrice(t *tomlw.Table, key string) error {
	v, ok := t.Get(key)
	if !ok {
		return nil
	}
	var f float64
	switch n := v.(type) {
	case int64:
		f = float64(n)
	case float64:
		f = n
	default:
		return fmt.Errorf("%s must be a number", key)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("%s must be finite", key)
	}
	if f < 0 {
		return fmt.Errorf("%s must be non-negative", key)
	}
	return nil
}

func validateTimePrice(row *tomlw.Table) error {
	tz, _ := row.Get("timezone")
	name, _ := tz.(string)
	// time.LoadLocation accepts "" (UTC) and "Local"; Python's ZoneInfo, which
	// modelman validates with, accepts neither.
	if name == "" || name == "Local" {
		return fmt.Errorf("timezone %q is not a known IANA timezone", name)
	}
	if _, err := time.LoadLocation(name); err != nil {
		return fmt.Errorf("timezone %q is not a known IANA timezone", name)
	}
	for _, k := range priceKeys {
		if err := checkPrice(row, k); err != nil {
			return err
		}
	}
	raw, _ := row.Get("windows")
	windows, ok := tableArray(raw)
	if !ok || len(windows) == 0 {
		return errors.New("windows must be a non-empty array of tables")
	}
	for i, w := range windows {
		if err := validateWindow(w); err != nil {
			return fmt.Errorf("windows[%d]: %w", i, err)
		}
	}
	return nil
}

func validateWindow(w *tomlw.Table) error {
	raw, _ := w.Get("days")
	days, ok := raw.([]any)
	if !ok || len(days) == 0 {
		return errors.New("days must be a non-empty list")
	}
	for _, d := range days {
		if s, _ := d.(string); !slices.Contains(weekDays, s) {
			return fmt.Errorf("days must be drawn from %v, got %v", weekDays, d)
		}
	}
	start, err := minutesOfDay(w, "start")
	if err != nil {
		return err
	}
	end, err := minutesOfDay(w, "end")
	if err != nil {
		return err
	}
	if start >= 24*60 {
		return errors.New("start must be before 24:00")
	}
	if start >= end {
		return errors.New("start must be before end")
	}
	return nil
}

// minutesOfDay reads an "HH:MM" key between 00:00 and 24:00.
func minutesOfDay(w *tomlw.Table, key string) (int, error) {
	v, _ := w.Get(key)
	s, _ := v.(string)
	m := hhmm.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%s must be HH:MM, got %v", key, v)
	}
	hours, _ := strconv.Atoi(m[1])
	minutes, _ := strconv.Atoi(m[2])
	total := hours*60 + minutes
	if minutes > 59 || total > 24*60 {
		return 0, fmt.Errorf("%s must be HH:MM between 00:00 and 24:00, got %q", key, s)
	}
	return total, nil
}
```

- [ ] **Step 4: Call it from the write path**

In `wt/internal/config/registry_write.go`, in `updateRegistryOnce`, replace:

```go
	if err := apply(doc); err != nil {
		return false, false, err
	}
	after, err := tomlw.Encode(root)
```

with:

```go
	if err := apply(doc); err != nil {
		return false, false, err
	}
	if err := doc.validateTouched(); err != nil {
		return false, false, err
	}
	after, err := tomlw.Encode(root)
```

- [ ] **Step 5: Run the tests to verify they pass**

Run, from `wt/`: `go test -count=1 ./internal/config`

Expected: `ok`.

- [ ] **Step 6: Commit**

```bash
test -z "$(gofmt -l .)" && go vet ./...
git add internal/config/registry_validate.go internal/config/registry_write.go internal/config/registry_validate_test.go
git commit -m "feat(wt): a registry write validates the rows it touched, and only those"
```

### Task 13: Document the writer

**Files:**
- Modify: `wt/docs/internals/config-and-registry.md`, `wt/docs/internals/testing.md`, `wt/CLAUDE.md`

**Interfaces:**
- Consumes: Tasks 10 to 12.
- Produces: nothing code reads.

No `CHANGELOG.md` entry: nothing a user runs changes in this PR.

- [ ] **Step 1: Make the edits**

In `wt/docs/internals/config-and-registry.md`, replace:

```text
**Missing registry → unconfigured-agent passthrough.**
```

with:

```text
**Writing the registry.** `config.UpdateRegistry(apply func(*RegistryDoc) error) (changed bool, err error)` (`internal/config/registry_write.go`) is the only way wt writes `registry.toml`. In order: flock on `<registry path>.lock` (the path as named, so a symlinked registry gets no lock file in its checkout); `resolveRegistryFile` (a resolving link is written through and survives, a broken one is `ErrRegistryLink`); read and decode with `internal/tomlw` (a missing file is an empty document; an unknown top-level key is `ErrRegistryTopLevel`, #247; a document whose key list and values disagree is a parse error, so a key the decoder lost is never written away); run `apply`; validate the rows `apply` touched and only those (`ErrRegistryInvalid` — wt's typed decode plus modelman's required-field and cost rules, `registry_validate.go`; not yet the two `Config.Validate` rules that need other rows, a resolvable `location` and a `provider_id` that names a provider); skip the write when `apply` changed nothing; otherwise re-read the file and, if another program changed it, start over (three runs of `apply`, then `ErrRegistryBusy`); replace the file atomically with its mode kept (0600 when new).

- **`apply` must be pure**: it may run up to three times, so no printing, no commands, no server calls, and a result handed back through a captured variable is assigned afresh on each run.
- **`RegistryDoc` is patch-shaped** (`registry_doc.go`): `PatchModel(id, set, unset)`, `AddModel`, `CloneModel`, `RemoveModel` (returns the row), `SetTimePrices`, `AddProvider`, and the copying read views `Models()`/`Providers()`. Keys are dotted paths inside a row (`cost.input_price_per_million`). A `PatchModel` is all or nothing: when one key fails, the row is as it was. Nothing writes a `config.Model`, so a key wt does not model is never touched and adding a field to `config.Model` cannot change a write (`TestAWriteNeverTouchesAKeyItWasNotAskedTo`). A value equal to the one already there is not rewritten — an integer price stays an integer. `model_info` is written only when a caller names it.
- **New keys go where modelman writes them** (`providerSchemas`/`modelSchemas`): at the key's schema position, unmodelled keys ahead of the schema keys.
- **A no-op leaves the file byte-identical**, hand-written comments included. The first write that changes something lays the whole file out in tomli-w's form and drops comments, as every modelman save does.
- **wt never adds a top-level key** beyond `providers`/`families`/`models`: modelman's loader refuses any other.
- **The registry write ignores `RegistryRedirected`**; the route sync a command runs afterwards is what `litellm.ErrRegistryRedirected` refuses.
- **A test binary cannot write the developer's registry**: `IsolateConfigHomeForTest` arms the `registryWriteGuard` seam with the registry paths the environment named before the redirect, and the guard follows symlinks on both sides (a linked file, a linked directory, `/tmp` for `/private/tmp`). It is a package variable, not an `import "testing"` in shipped code. The guard is off in a binary whose `TestMain` did not arm it; `config.RegistryWriteGuardArmed()` lets a test assert that it is on.

**Missing registry → unconfigured-agent passthrough.**
```

In `wt/docs/internals/testing.md`, replace:

```text
**Prefer asserting on unexported functions directly**
```

with:

```text
**Registry writes.** `internal/config` has two seams of its own: `registryWriteGuard` (set by `IsolateConfigHomeForTest`; `UpdateRegistry` asks it before taking the lock, so a guarded write creates nothing) and `registryBeforeRename` (a test swaps it to play another program writing the file between wt's read and its rename). A new package whose tests reach `config.UpdateRegistry` must call `config.IsolateConfigHomeForTest` from its `TestMain`, and assert `config.RegistryWriteGuardArmed()` in one test: the guard is off unless a `TestMain` arms it, so a binary that forgot fails open.

**Prefer asserting on unexported functions directly**
```

In `wt/CLAUDE.md`, replace:

```text
- **Read-side schemas are pinned by contract fixtures**
```

with:

```text
- **`config.UpdateRegistry` is the only registry write path**: flock, symlink write-through, touched-row validation, no-op skip, re-check before rename. Its `apply` must be pure (it may run up to three times). `RegistryDoc`'s operations are patch-shaped — never round-trip a `config.Model` into the file.
- **Read-side schemas are pinned by contract fixtures**
```

- [ ] **Step 2: Verify the PR and commit**

Run, from `wt/`:

```bash
test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check
```

Expected: every package `ok`; `make check` ends without an error.

Run, from the monorepo root: `make test-all`

Expected: exit 0.

```bash
git add wt/docs/internals/config-and-registry.md wt/docs/internals/testing.md wt/CLAUDE.md
git commit -m "docs(wt): how wt writes registry.toml"
```

Stop here. Pushing `feat/wt-registry-writer` and opening the PR need the owner's OK.

---

## PR 5 — seeding, `wt model init`, and the hint text

Branch `feat/wt-model-init`, cut after PRs 4 and 6 have merged. This is the PR that makes wt a writer of the registry, through one command, so modelman's stale-snapshot guard (PR 6) has to be on `main` first.

### Task 14: `SeedRegistryDefaults`

The Go port of modelman's default-provider logic: `_ensure_provider_entries` (`modelman/src/modelman/sync.py:132`) and `sync_agent_providers` (`modelman/src/modelman/registry.py:333`), with the rows of `_DEFAULT_PROVIDER_TEMPLATES` (`registry.py:283`) as `_provider_to_dict` writes them. `backfill_provider_defaults` (`sync.py:170`) is not ported: seeding appends rows and never edits one.

One trigger is added that modelman does not have (owner decision, 2026-10-07): a provider a configured agent lists in `supported_providers` gets a row. For a default local provider that is its default row, installed or not. For `openrouter` it is a default row with the provider's name, address and `auth.type`, and no key and no `secret_ref`. A listed provider with no default row is not seeded: it is returned in `unseeded` so the caller can name it. The reason is wt's own validation: `Config.Validate` refuses an agent that lists a provider with no row (`config.go:753`), so without this a machine with a `config.toml` and no registry still has a config error after `wt model init`.

Seeding has to see the agents as the next `Load` will. `Load` runs `migrateConfigSchema` only after the registry has loaded, so on a fresh machine `config.toml` is still unmigrated when `wt model init` runs: the `opencode` agent may list `opencode` where `Load` will make it list `ollama`, and the `agy` agent is not there yet. Step 3 splits the agent half of that migration into `migrateAgentRefs`, which seeding applies to its own read of the file.

**Files:**
- Create: `wt/internal/config/registry_seed.go`
- Modify: `wt/internal/config/migrate.go:341` (`migrateConfigSchema`: split out `migrateAgentRefs`)
- Test: `wt/internal/config/registry_seed_test.go`

**Interfaces:**
- Consumes: `RegistryDoc.AddProvider(table map[string]any) error`, `d.rows`, `rowID` (PR 4, Task 10); `UpdateRegistry`, `scratchRegistry`, `readFile` (Task 11); `readConfigFile() (cfg *Config, exists bool, err error)` (`config.go:1108`); `migrateConfigSchema(cfg *Config) (bool, error)` and `upsertAgent` (`migrate.go:341`, `:417`); `loadRegistry()`, `Load()`, `(*Config).ValidateAll()`.
- Produces (package `config`):
  - `type SeedEnv struct { Agents []string; AgentProviders []string; OnPath func(command string) bool }`
  - `func DefaultSeedEnv() SeedEnv`
  - `func SeedRegistryDefaults(d *RegistryDoc, env SeedEnv) (added, unseeded []string, err error)` — `unseeded` is the providers an agent lists that have no row and no default
  - unexported: `func migrateAgentRefs(cfg *Config) bool`, `func seedAgents() (names, providers []string)`

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/config/registry_seed_test.go`:

```go
package config

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func onPath(commands ...string) func(string) bool {
	return func(c string) bool { return slices.Contains(commands, c) }
}

func seed(t *testing.T, env SeedEnv) (added []string, changed bool) {
	t.Helper()
	added, _, changed = seedReport(t, env)
	return added, changed
}

// seedReport is seed with the providers seeding had no row for.
func seedReport(t *testing.T, env SeedEnv) (added, unseeded []string, changed bool) {
	t.Helper()
	changed, err := UpdateRegistry(func(d *RegistryDoc) error {
		var err error
		added, unseeded, err = SeedRegistryDefaults(d, env)
		return err
	})
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}
	return added, unseeded, changed
}

// TestSeedWritesTheRowsModelmanWrites pins every default row byte for byte
// against what modelman writes for the same machine (the expected rows were
// produced by modelman's default_provider_entry and sync_agent_providers
// through tomli-w), placed ahead of the models as modelman orders the file. A base_url or name that differs would give wt and
// modelman two ideas of the same provider for as long as both exist.
func TestSeedWritesTheRowsModelmanWrites(t *testing.T) {
	path := scratchRegistry(t, `[[models]]
id = "mlx_lm_server/pair"
family = "f"
provider_id = "mlx_lm_server"
model_name = "org/target"
`)
	added, changed := seed(t, SeedEnv{Agents: []string{"claude", "agy"}, OnPath: onPath("ollama", "omlx", "mtplx")})
	if want := []string{"ollama", "omlx", "mlx_lm_server", "mtplx", "claude", "agy"}; !slices.Equal(added, want) || !changed {
		t.Fatalf("added = %v (changed %v), want %v", added, changed, want)
	}
	const want = `[[providers]]
id = "ollama"
name = "Ollama"
location = "local"
protocols = [
    "anthropic",
    "openai-chat",
]

[providers.auth]
type = "none"
base_url = "http://localhost:11434"

[[providers]]
id = "omlx"
name = "oMLX"
location = "local"

[providers.auth]
type = "none"
base_url = "http://localhost:8000"

[[providers]]
id = "mlx_lm_server"
name = "mlx-lm server (target+draft)"
location = "local"

[providers.auth]
type = "none"
base_url = "http://localhost:8001/v1"

[[providers]]
id = "mtplx"
name = "MTPLX"
location = "local"
model_dir = "~/.mtplx/models"

[providers.auth]
type = "none"
base_url = "http://localhost:8003/v1"

[[providers]]
id = "claude"
name = "Claude"
location = "cloud"

[providers.auth]
type = "native"

[[providers]]
id = "agy"
name = "Agy"
location = "cloud"

[providers.auth]
type = "native"

[[models]]
id = "mlx_lm_server/pair"
family = "f"
provider_id = "mlx_lm_server"
model_name = "org/target"
`
	if got := readFile(t, path); got != want {
		t.Errorf("seeded registry:\n%s\nwant:\n%s", got, want)
	}
	// What wt wrote, wt reads and accepts.
	providers, models, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{DefaultTag: "code", Providers: providers, Models: models}
	if err := cfg.ValidateAll(); err != nil {
		t.Errorf("the seeded registry should validate: %v", err)
	}
}

// TestSeedAddsOnlyWhatTheMachineNeeds pins the rule for each row: a provider
// nobody references and nothing installs gets no row (wt would probe a server
// the machine does not have); mlx_lm_server never comes from PATH; an
// installed omlx stays out when an omlx-6bit row already stands for that
// server, but a model that references `omlx`, or an agent that lists it,
// still gets its row. A provider an agent lists gets its default row whether
// or not it is installed, because wt refuses a config whose agent lists a
// provider with no row; openrouter comes from an agent only, never from a
// model or from PATH.
func TestSeedAddsOnlyWhatTheMachineNeeds(t *testing.T) {
	const sixBit = `[[providers]]
id = "omlx-6bit"
name = "oMLX 6-bit"
location = "local"

[providers.auth]
type = "none"
base_url = "http://localhost:8000"
`
	const omlxModel = `
[[models]]
id = "omlx/m"
family = "f"
provider_id = "omlx"
model_name = "m"
`
	const openrouterModel = `[[models]]
id = "openrouter/m"
family = "f"
provider_id = "openrouter"
model_name = "org/m"
`
	cases := []struct {
		name     string
		registry string
		env      SeedEnv
		want     []string
	}{
		{"nothing installed, nothing referenced", "", SeedEnv{}, nil},
		{"a nil OnPath means nothing is installed", "", SeedEnv{Agents: []string{"claude"}}, []string{"claude"}},
		{"only what is on PATH", "", SeedEnv{OnPath: onPath("mtplx")}, []string{"mtplx"}},
		{"mlx_lm_server is never taken from PATH", "", SeedEnv{OnPath: func(string) bool { return true }}, []string{"ollama", "omlx", "mtplx"}},
		{"an installed omlx is covered by an omlx-6bit row", sixBit, SeedEnv{OnPath: onPath("omlx", "ollama")}, []string{"ollama"}},
		{"but a model that references omlx gets the row", sixBit + omlxModel, SeedEnv{OnPath: onPath("omlx")}, []string{"omlx"}},
		{"an agent whose name is already a provider is skipped", sixBit, SeedEnv{Agents: []string{"omlx-6bit", "", "pi"}}, []string{"pi"}},
		{"an agent named like a default provider gets the default row, once", "", SeedEnv{Agents: []string{"ollama"}, OnPath: onPath("ollama")}, []string{"ollama"}},
		{"an agent lists a default provider that is not installed", "", SeedEnv{AgentProviders: []string{"ollama", "mlx_lm_server"}}, []string{"ollama", "mlx_lm_server"}},
		{"an agent lists omlx beside an omlx-6bit row", sixBit, SeedEnv{AgentProviders: []string{"omlx"}}, []string{"omlx"}},
		{"an agent lists openrouter", "", SeedEnv{AgentProviders: []string{"openrouter"}}, []string{"openrouter"}},
		{"an agent lists a provider that has its row", sixBit, SeedEnv{AgentProviders: []string{"omlx-6bit"}}, nil},
		{"openrouter is not seeded for a model or from PATH", openrouterModel, SeedEnv{OnPath: func(string) bool { return true }}, []string{"ollama", "omlx", "mtplx"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			scratchRegistry(t, c.registry)
			added, _ := seed(t, c.env)
			if !slices.Equal(added, c.want) {
				t.Errorf("added = %v, want %v", added, c.want)
			}
		})
	}
}

// TestSeedIsIdempotentAndNeverEditsARow pins the two safety properties of
// seeding. Run twice it changes nothing the second time (so `wt model add`
// can seed on every call for free). And a row that exists is left exactly as
// it is, however incomplete: seeding appends, it does not repair — a user's
// own base_url must never be reset to the default.
func TestSeedIsIdempotentAndNeverEditsARow(t *testing.T) {
	const custom = `[[providers]]
id = "ollama"
name = "My Ollama"
location = "local"

[providers.auth]
type = "none"
base_url = "http://gpu-box:11434"

[[providers]]
id = "omlx"
name = "oMLX"

[providers.auth]
type = "none"
`
	path := scratchRegistry(t, custom)
	env := SeedEnv{Agents: []string{"claude"}, OnPath: onPath("ollama", "omlx")}
	added, changed := seed(t, env)
	if !slices.Equal(added, []string{"claude"}) || !changed {
		t.Fatalf("first run: added %v (changed %v), want [claude]", added, changed)
	}
	after := readFile(t, path)
	if !strings.HasPrefix(after, custom) {
		t.Errorf("existing rows were edited:\n%s", after)
	}
	added, changed = seed(t, env)
	if len(added) != 0 || changed {
		t.Errorf("second run: added %v (changed %v), want nothing", added, changed)
	}
	if got := readFile(t, path); got != after {
		t.Error("a second seeding changed the file")
	}
}

// TestSeedCreatesAMissingRegistry pins the first run on a new machine: no
// registry at all, one tool installed. Seeding creates the file with that
// tool's row; on a machine with nothing installed it still creates the file,
// empty, so the next `wt` run no longer reports a missing registry.
func TestSeedCreatesAMissingRegistry(t *testing.T) {
	path := scratchRegistry(t, "")
	added, changed := seed(t, SeedEnv{OnPath: onPath("ollama")})
	if !slices.Equal(added, []string{"ollama"}) || !changed {
		t.Fatalf("added %v (changed %v), want [ollama] and a write", added, changed)
	}
	if got := readFile(t, path); !strings.HasPrefix(got, "[[providers]]\nid = \"ollama\"\n") {
		t.Errorf("new registry:\n%s", got)
	}

	empty := scratchRegistry(t, "")
	added, changed = seed(t, SeedEnv{})
	if len(added) != 0 || !changed {
		t.Fatalf("nothing to add: added %v (changed %v), want an empty registry created", added, changed)
	}
	if info, err := os.Stat(empty); err != nil || info.Size() != 0 {
		t.Errorf("want an empty registry file, got err %v", err)
	}
}

// TestSeedAgentNamesFollowWhatLoadWillValidate pins which agents get a
// provider row. They come from config.toml; and when config.toml exists agy
// is always among them, because Load's schema migration adds an agy agent to
// every existing config.toml and a registry with no agy provider then fails
// validation with `unknown provider "agy"`. The end-to-end check is that a
// registry seeded from this list loads and validates. A config.toml that
// cannot be read names nothing, so seeding still works beside a broken one.
func TestSeedAgentNamesFollowWhatLoadWillValidate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("WT_REGISTRY", "")
	t.Setenv("MODELMAN_REGISTRY", "")
	if names, providers := seedAgents(); names != nil || providers != nil {
		t.Errorf("with no config.toml: agents = %v, providers = %v, want none", names, providers)
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("default_tag = \"code\"\n\n[[agents]]\nname = \"claude\"\nsupported_providers = [\"claude\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	names, providers := seedAgents()
	if want := []string{"claude", "agy"}; !slices.Equal(names, want) || !slices.Equal(providers, want) {
		t.Fatalf("agents = %v, providers = %v, want %v for both", names, providers, want)
	}
	env := DefaultSeedEnv()
	env.OnPath = nil // this machine's PATH is not the test's business
	if added, _ := seed(t, env); !slices.Equal(added, []string{"claude", "agy"}) {
		t.Fatalf("added = %v, want [claude agy]", added)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load after seeding: %v", err)
	}
	if err := cfg.ValidateAll(); err != nil {
		t.Errorf("the seeded registry should validate against config.toml: %v", err)
	}

	if err := os.WriteFile(Path(), []byte("this is not toml [[["), 0o644); err != nil {
		t.Fatal(err)
	}
	if names, providers := seedAgents(); names != nil || providers != nil {
		t.Errorf("with an unreadable config.toml: agents = %v, providers = %v, want none", names, providers)
	}
}

// TestSeedRowsForTheProvidersAgentsList pins the rows the agent trigger
// writes and what it reports. A default local provider gets the same row it
// would get if it were installed. openrouter gets a row with its address and
// no key: the key is the user's secret, and seeding must never write one or
// guess where it is kept. A provider wt has no default row for is left out
// and reported, on every run, so the command can tell the user which row to
// write by hand.
func TestSeedRowsForTheProvidersAgentsList(t *testing.T) {
	path := scratchRegistry(t, "")
	env := SeedEnv{AgentProviders: []string{"ollama", "corp-gateway", "openrouter", "", "corp-gateway"}}
	added, unseeded, changed := seedReport(t, env)
	if want := []string{"ollama", "openrouter"}; !slices.Equal(added, want) || !changed {
		t.Fatalf("added = %v (changed %v), want %v", added, changed, want)
	}
	if want := []string{"corp-gateway"}; !slices.Equal(unseeded, want) {
		t.Errorf("unseeded = %v, want %v", unseeded, want)
	}
	const want = `[[providers]]
id = "ollama"
name = "Ollama"
location = "local"
protocols = [
    "anthropic",
    "openai-chat",
]

[providers.auth]
type = "none"
base_url = "http://localhost:11434"

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
base_url = "https://openrouter.ai/api/v1"
`
	if got := readFile(t, path); got != want {
		t.Errorf("seeded registry:\n%s\nwant:\n%s", got, want)
	}

	added, unseeded, changed = seedReport(t, env)
	if len(added) != 0 || changed {
		t.Errorf("second run: added %v (changed %v), want nothing", added, changed)
	}
	if want := []string{"corp-gateway"}; !slices.Equal(unseeded, want) {
		t.Errorf("second run: unseeded = %v, want %v", unseeded, want)
	}
}

// TestSeedMakesAFreshConfigLoad is the first run on a machine that has a
// config.toml and no registry, end to end: nothing is installed, one agent
// lists openrouter, and the opencode agent is one Load's migration rewires to
// ollama. After seeding, Load succeeds and the config validates — without the
// agent trigger every wt command would stop on `unknown provider "ollama"`
// and `unknown provider "openrouter"` until the user wrote both rows by hand.
// A provider wt has no default row for is the one case seeding cannot fix:
// it is reported, and validation names it.
func TestSeedMakesAFreshConfigLoad(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("WT_REGISTRY", "")
	t.Setenv("MODELMAN_REGISTRY", "")
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	const agents = `default_tag = "code"

[[agents]]
name = "claude"
supported_providers = ["claude", "openrouter"]

[[agents]]
name = "opencode"
supported_providers = ["opencode"]
`
	if err := os.WriteFile(Path(), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	env := DefaultSeedEnv()
	env.OnPath = nil // nothing is installed
	if want := []string{"claude", "openrouter", "ollama", "agy"}; !slices.Equal(env.AgentProviders, want) {
		t.Fatalf("providers the agents list = %v, want %v", env.AgentProviders, want)
	}
	added, unseeded, _ := seedReport(t, env)
	if want := []string{"ollama", "openrouter", "claude", "opencode", "agy"}; !slices.Equal(added, want) {
		t.Fatalf("added = %v, want %v", added, want)
	}
	if len(unseeded) != 0 {
		t.Errorf("unseeded = %v, want none", unseeded)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load after seeding: %v", err)
	}
	if err := cfg.ValidateAll(); err != nil {
		t.Errorf("the seeded registry should validate against config.toml: %v", err)
	}
	if strings.Contains(readFile(t, RegistryPath()), "secret_ref") {
		t.Error("seeding wrote a secret_ref: the key is the user's to name")
	}

	// A provider with no default row: reported, not seeded, and still the
	// one thing validation complains about.
	if err := os.WriteFile(Path(), []byte(agents+"\n[[agents]]\nname = \"pi\"\nsupported_providers = [\"corp-gateway\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env = DefaultSeedEnv()
	env.OnPath = nil
	added, unseeded, _ = seedReport(t, env)
	if !slices.Equal(added, []string{"pi"}) || !slices.Equal(unseeded, []string{"corp-gateway"}) {
		t.Fatalf("added = %v, unseeded = %v; want [pi] and [corp-gateway]", added, unseeded)
	}
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	const wantErr = `agent "pi": unknown provider "corp-gateway"`
	if err := cfg.ValidateAll(); err == nil || err.Error() != wantErr {
		t.Errorf("ValidateAll = %v, want only %q", err, wantErr)
	}
}

// TestPyTitleMatchesPython pins the port of str.title() against Python's own
// answers, so the display name wt gives an agent's provider row is the one
// modelman would have given it.
func TestPyTitleMatchesPython(t *testing.T) {
	cases := map[string]string{
		"claude": "Claude", "agy": "Agy", "opencode": "Opencode", "my-agent": "My-Agent",
		"gpt4o": "Gpt4O", "ALLCAPS": "Allcaps", "x_y z": "X_Y Z", "": "",
	}
	for in, want := range cases {
		if got := pyTitle(in); got != want {
			t.Errorf("pyTitle(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run, from `wt/`: `go test ./internal/config -run 'Seed|PyTitle'`

Expected: FAIL — the package does not build. The compiler prints `undefined: SeedEnv` and `undefined: SeedRegistryDefaults` and stops after ten errors (`too many errors`), so `DefaultSeedEnv`, `seedAgents` and `pyTitle`, which are undefined too, may not be listed.

- [ ] **Step 3: Split the agent fixups out of `migrateConfigSchema`**

A refactor with no change in behaviour: the three fixups that rewrite `cfg.Agents` move into their own function, which reads no file and prints nothing, so seeding can apply them to its own read of `config.toml`. `migrateConfigSchema` calls it and keeps the fourth fixup (the legacy `[gateway]` notice), which does both.

In `wt/internal/config/migrate.go`, replace:

```go
func migrateConfigSchema(cfg *Config) (bool, error) {
	changed := false

	// ── Fixup 1: rename "google" → "agy" in agent references ────────
```

with:

```go
func migrateConfigSchema(cfg *Config) (bool, error) {
	changed := migrateAgentRefs(cfg)

	// ── Fixup 4: notice + drop wt's legacy [gateway] block ───────────
	// GatewayConfig was deleted (Task 9): LiteLLM routing is now a wt-owned
	// [litellm] table (managed with `wt litellm ...`). The decoded Config
	// simply has no Gateway field, so re-saving drops the block; this
	// fixup only detects its presence to point the user at the new
	// control surface. Self-extinguishing: after the triggered Save, the
	// block is gone and the probe no longer matches.
	if dropLegacyGateway(Path()) {
		changed = true
	}

	return changed, nil
}

// migrateAgentRefs is the part of migrateConfigSchema that rewrites
// cfg.Agents (fixups 1 to 3), with no file access and no output. Registry
// seeding calls it on its own read of config.toml to learn which providers
// the agents will name once Load has migrated the file. Reports whether it
// changed cfg.
func migrateAgentRefs(cfg *Config) bool {
	changed := false

	// ── Fixup 1: rename "google" → "agy" in agent references ────────
```

Then, lower in what is now `migrateAgentRefs`, replace:

```go
	if upsertAgent(cfg, "opencode", []string{"ollama"}, "ollama", false) {
		changed = true
	}

	// ── Fixup 4: notice + drop wt's legacy [gateway] block ───────────
	// GatewayConfig was deleted (Task 9): LiteLLM routing is now a wt-owned
	// [litellm] table (managed with `wt litellm ...`). The decoded Config
	// simply has no Gateway field, so re-saving drops the block; this
	// fixup only detects its presence to point the user at the new
	// control surface. Self-extinguishing: after the triggered Save, the
	// block is gone and the probe no longer matches.
	if dropLegacyGateway(Path()) {
		changed = true
	}

	return changed, nil
}
```

with:

```go
	if upsertAgent(cfg, "opencode", []string{"ollama"}, "ollama", false) {
		changed = true
	}

	return changed
}
```

The seeding tests still do not build, so check this step on the tests that already exist. Run, from `wt/`: `go build ./... && go vet ./internal/config`

Expected: `go build` prints nothing and exits 0; `go vet` stops on the one file that does not build yet, with `vet: internal/config/registry_seed_test.go:14:29: undefined: SeedEnv`. The migration's own tests (`TestMigrate…`, `TestLoad…` in `internal/config`, and `internal/configeditor`) run in Step 5 with the rest of the package.

- [ ] **Step 4: Write the seeding**

Create `wt/internal/config/registry_seed.go`:

```go
package config

import (
	"os/exec"
	"slices"
	"strings"
	"unicode"
)

// SeedEnv is what seeding needs to know about the machine. It is gathered
// before the registry is locked and passed in, so the apply function that
// seeds stays pure and tests can describe a machine without being one.
type SeedEnv struct {
	// Agents are the agent names wt is configured with, in config.toml order.
	Agents []string
	// AgentProviders are the provider ids those agents list in
	// supported_providers, as the next Load will validate them.
	AgentProviders []string
	// OnPath reports whether a command is installed. nil means none is.
	OnPath func(command string) bool
}

// DefaultSeedEnv describes the real machine: the agents in wt's config.toml,
// the providers they list, and what exec.LookPath finds.
func DefaultSeedEnv() SeedEnv {
	names, providers := seedAgents()
	return SeedEnv{
		Agents:         names,
		AgentProviders: providers,
		OnPath: func(command string) bool {
			_, err := exec.LookPath(command)
			return err == nil
		},
	}
}

// seedAgents lists the agents config.toml names and the providers they
// list, as the next Load will see them: Load's schema migration adds an agy
// agent to every existing config.toml and rewrites two older spellings
// (google becomes agy; the opencode agent lists ollama only), and it runs
// only once the registry loads, so on a fresh machine it has not run yet. A
// registry seeded from the file as written would then fail validation on the
// very next run. A missing or unreadable config.toml names nothing — seeding
// tolerates an absent wt setup, as modelman's sync_agent_providers does.
// Nothing is written: the migration is applied to this read only.
func seedAgents() (names, providers []string) {
	cfg, exists, err := readConfigFile()
	if err != nil || !exists {
		return nil, nil
	}
	migrateAgentRefs(cfg)
	for _, a := range cfg.Agents {
		names = append(names, a.Name)
		for _, id := range a.SupportedProviders {
			if !slices.Contains(providers, id) {
				providers = append(providers, id)
			}
		}
	}
	return names, providers
}

// defaultProviderIDs are the local providers with a default row, in the
// order the rows are added (modelman's DEFAULT_PROVIDER_IDS).
var defaultProviderIDs = []string{"ollama", "omlx", "mlx_lm_server", "mtplx"}

// installedProviderCommands maps a default provider to the command whose
// presence on PATH shows it is installed. mlx_lm_server has none — it is a
// module run from omlx's Python — so it gets a row only from a model that
// references it or an agent that lists it.
var installedProviderCommands = map[string]string{"ollama": "ollama", "omlx": "omlx", "mtplx": "mtplx"}

// defaultProviderRow is the row modelman writes for a default provider
// (registry.py's _DEFAULT_PROVIDER_TEMPLATES through _provider_to_dict, which
// leaves `protocols` out when it is just ["openai-chat"], the default both
// tools assume). A fresh map on every call.
func defaultProviderRow(id string) map[string]any {
	switch id {
	case "ollama":
		return map[string]any{
			"id": "ollama", "name": "Ollama", "location": "local",
			"protocols": []string{"anthropic", "openai-chat"},
			"auth":      map[string]any{"type": "none", "base_url": "http://localhost:11434"},
		}
	case "omlx":
		return map[string]any{
			"id": "omlx", "name": "oMLX", "location": "local",
			"auth": map[string]any{"type": "none", "base_url": "http://localhost:8000"},
		}
	case "mlx_lm_server":
		return map[string]any{
			"id": "mlx_lm_server", "name": "mlx-lm server (target+draft)", "location": "local",
			"auth": map[string]any{"type": "none", "base_url": "http://localhost:8001/v1"},
		}
	case "mtplx":
		return map[string]any{
			"id": "mtplx", "name": "MTPLX", "location": "local", "model_dir": "~/.mtplx/models",
			"auth": map[string]any{"type": "none", "base_url": "http://localhost:8003/v1"},
		}
	}
	return nil
}

// cloudProviderIDs are the cloud providers with a default row: the ones wt
// can route (internal/litellm's policy table).
var cloudProviderIDs = []string{"openrouter"}

// cloudProviderRow is the default row for a cloud provider: its name, its
// public address and how it authenticates. It names no key and no place a
// key is kept — auth.secret_ref is the user's to add, and seeding must never
// write or guess one. A fresh map on every call.
func cloudProviderRow(id string) map[string]any {
	if id == "openrouter" {
		return map[string]any{
			"id": "openrouter", "name": "OpenRouter", "location": "cloud",
			"auth": map[string]any{"type": "api_key", "base_url": "https://openrouter.ai/api/v1"},
		}
	}
	return nil
}

// SeedRegistryDefaults adds the provider rows a working registry needs and
// reports the ids it added, in the order it added them. It is the Go port of
// modelman's default-provider logic (sync.py's _ensure_provider_entries and
// registry.py's sync_agent_providers) plus one trigger modelman lacks, and
// the one seeding implementation in wt. It only ever appends rows: a row
// that exists is never edited.
//
//   - A default local provider (ollama, omlx, mlx_lm_server, mtplx) gets its
//     default row when a model references it, when an agent lists it, or —
//     for the three that have a command — when that command is on PATH. An
//     installed omlx gets no row when an `omlx-6bit` row exists: the two are
//     one server, and a second row would change which one discovery uses.
//   - A cloud provider with a default row (openrouter) gets it when an agent
//     lists it. The row holds no key.
//   - Every agent in env.Agents gets a native cloud provider row under its
//     own name.
//
// The agent trigger is what lets wt's own validation pass after seeding on a
// machine that has a config.toml and no registry: Config.Validate refuses an
// agent that lists a provider with no row. unseeded names the providers an
// agent lists that still have no row, because wt has no default for them;
// the caller tells the user, and those stay a hand edit of registry.toml.
//
// It runs on the document UpdateRegistry hands an apply function, so a caller
// can seed and make its own change in one locked write:
//
//	var added, unseeded []string
//	changed, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
//		var err error
//		added, unseeded, err = config.SeedRegistryDefaults(d, env)
//		return err
//	})
//
// Reads never seed: Load does not call this, and a missing registry stays
// ErrRegistryMissing until `wt model init` or a write creates one.
func SeedRegistryDefaults(d *RegistryDoc, env SeedEnv) (added, unseeded []string, err error) {
	existing := map[string]bool{}
	for _, row := range d.rows("providers") {
		existing[rowID(row)] = true
	}
	listed := map[string]bool{}
	for _, id := range env.AgentProviders {
		listed[id] = true
	}
	wanted := map[string]bool{}
	for _, row := range d.rows("models") {
		if v, _ := row.Get("provider_id"); v != nil {
			if id, ok := v.(string); ok {
				wanted[id] = true
			}
		}
	}
	for id, command := range installedProviderCommands {
		if env.OnPath == nil || !env.OnPath(command) {
			continue
		}
		if id == "omlx" && existing["omlx-6bit"] {
			continue
		}
		wanted[id] = true
	}
	add := func(row map[string]any) error {
		id, _ := row["id"].(string)
		if err := d.AddProvider(row); err != nil {
			return err
		}
		existing[id] = true
		added = append(added, id)
		return nil
	}
	for _, id := range defaultProviderIDs {
		if existing[id] || !(wanted[id] || listed[id]) {
			continue
		}
		if err := add(defaultProviderRow(id)); err != nil {
			return nil, nil, err
		}
	}
	for _, id := range cloudProviderIDs {
		if existing[id] || !listed[id] {
			continue
		}
		if err := add(cloudProviderRow(id)); err != nil {
			return nil, nil, err
		}
	}
	for _, name := range env.Agents {
		if name == "" || existing[name] {
			continue
		}
		row := map[string]any{
			"id": name, "name": pyTitle(name), "location": "cloud",
			"auth": map[string]any{"type": "native"},
		}
		if err := add(row); err != nil {
			return nil, nil, err
		}
	}
	for _, id := range env.AgentProviders {
		if id != "" && !existing[id] && !slices.Contains(unseeded, id) {
			unseeded = append(unseeded, id)
		}
	}
	return added, unseeded, nil
}

// pyTitle is Python's str.title(), which modelman names an agent's provider
// row with: a letter that follows a letter is lowercased, any other letter is
// uppercased ("claude" -> "Claude", "my-agent" -> "My-Agent", "gpt4o" ->
// "Gpt4O"). Ported so a row wt seeds is the row modelman would have seeded.
func pyTitle(s string) string {
	var b strings.Builder
	afterLetter := false
	for _, r := range s {
		if unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) {
			if afterLetter {
				b.WriteRune(unicode.ToLower(r))
			} else {
				b.WriteRune(unicode.ToTitle(r))
			}
			afterLetter = true
			continue
		}
		b.WriteRune(r)
		afterLetter = false
	}
	return b.String()
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run, from `wt/`: `go test -count=1 ./internal/config ./internal/configeditor`

Expected: both `ok`. `TestSeedAgentNamesFollowWhatLoadWillValidate`, `TestSeedMakesAFreshConfigLoad` and a few older tests print wt's one-time notice `wt: migrated config to native-provider alignment …` on stderr when run with `-v`; that is `Load` migrating a throwaway `config.toml`.

- [ ] **Step 6: Commit**

```bash
test -z "$(gofmt -l .)" && go vet ./...
git add internal/config/registry_seed.go internal/config/registry_seed_test.go internal/config/migrate.go
git commit -m "feat(wt): SeedRegistryDefaults, the default provider rows in Go"
```

### Task 15: `wt model init [--json]`

**Files:**
- Create: `wt/cmd/wt/model.go`
- Modify: `wt/cmd/wt/main.go:431` (register the group)
- Modify: `wt/cmd/wt/testmain_test.go` (stub the two new seams)
- Test: `wt/cmd/wt/model_test.go`

**Interfaces:**
- Consumes: `config.UpdateRegistry`, `config.SeedRegistryDefaults(d, env) (added, unseeded []string, err error)`, `config.SeedEnv`, `config.DefaultSeedEnv`, `config.Dir`, `config.Path`, `config.RegistryPath`, `config.RegistryFixHint`, `config.Load`, `config.RegistryWriteGuardArmed() bool` (Task 11); `runLitellmSync(out, errOut io.Writer, cfg *config.Config, asJSON, dryRun bool) error` (`cmd/wt/litellm.go:130`); `litellm.DefaultPath() string`, `litellm.ErrMissing`, `litellm.ErrRegistryRedirected`; test helpers `withCleanConfigEnv(t, home)` and `stubProbeInventory(t, snap)`.
- Produces (package `main`):
  - `func modelCmd(_ *app) *cobra.Command` — the `wt model` group (Step 3 adds `list`, `add`, `edit`, `rm` to it)
  - `func runModelInit(out, errOut io.Writer, asJSON bool) error`
  - seams `var seedEnv = config.DefaultSeedEnv` and `var syncRoutesAfterWrite = realSyncRoutesAfterWrite`, where `func realSyncRoutesAfterWrite(out, errOut io.Writer) string` returns a warning or `""` (Step 3's writing verbs call the same seam)
  - test helpers `stubSeedEnv(t, env)`, `stubRouteSync(t, warning) *int`, `realRouteSync(t)`

What the user sees. Text:

```
registry: /Users/me/.config/local-ai/registry.toml (created)
added provider: ollama
added provider: claude
```

A second run:

```
registry: /Users/me/.config/local-ai/registry.toml
nothing to add
```

When an agent lists a provider wt has no default row for, every run says so on its last line of stdout, and the exit status is still 0:

```
registry: /Users/me/.config/local-ai/registry.toml
nothing to add
no default row for provider: corp-gateway (an agent lists it; add it to registry.toml by hand)
```

`--json` prints one object: `{"registry":"…","created":true,"changed":true,"providers_added":["ollama","claude"],"providers_unseeded":[],"warnings":[]}`. `providers_unseeded` holds the ids the text form names in its `no default row` lines.

- [ ] **Step 1: Write the failing tests**

Create `wt/cmd/wt/model_test.go`:

```go
package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// stubSeedEnv makes `wt model init` seed for the machine env describes.
func stubSeedEnv(t *testing.T, env config.SeedEnv) {
	t.Helper()
	old := seedEnv
	seedEnv = func() config.SeedEnv { return env }
	t.Cleanup(func() { seedEnv = old })
}

// stubRouteSync replaces the route sync that follows a registry write with
// one that records its calls and returns warning.
func stubRouteSync(t *testing.T, warning string) *int {
	t.Helper()
	calls := 0
	old := syncRoutesAfterWrite
	syncRoutesAfterWrite = func(io.Writer, io.Writer) string {
		calls++
		return warning
	}
	t.Cleanup(func() { syncRoutesAfterWrite = old })
	return &calls
}

// realRouteSync lets a test run the real route sync after a registry write.
// It stays off the machine: no provider is probed (an empty inventory) and
// the proxy restart is `true`. The caller decides which config.yaml, if any,
// the sync can reach.
func realRouteSync(t *testing.T) {
	t.Helper()
	old := syncRoutesAfterWrite
	syncRoutesAfterWrite = realSyncRoutesAfterWrite
	t.Cleanup(func() { syncRoutesAfterWrite = old })
	stubProbeInventory(t, localmodels.Snapshot{})
	t.Setenv("WT_LITELLM_RESTART_CMD", "true")
}

func onPath(commands ...string) func(string) bool {
	return func(c string) bool {
		for _, have := range commands {
			if have == c {
				return true
			}
		}
		return false
	}
}

// TestModelInitCreatesTheRegistryAndSaysWhatItAdded pins the first run on a
// new machine, as the user sees it: the registry is created, each provider
// row added is named, a provider an agent lists that wt has no default row
// for is named too (on every run, since it stays a config error until the
// user adds it), and the routes are synced once. This is the command the
// "model registry not found" error sends people to, so it has to work with
// no registry and no config at all.
func TestModelInitCreatesTheRegistryAndSaysWhatItAdded(t *testing.T) {
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	registry := filepath.Join(home, ".config", "local-ai", "registry.toml")
	stubSeedEnv(t, config.SeedEnv{
		Agents: []string{"claude"}, AgentProviders: []string{"claude", "corp-gateway"}, OnPath: onPath("ollama"),
	})
	synced := stubRouteSync(t, "")
	const unseeded = "no default row for provider: corp-gateway (an agent lists it; add it to registry.toml by hand)\n"

	var out, errOut bytes.Buffer
	if err := runModelInit(&out, &errOut, false); err != nil {
		t.Fatalf("runModelInit: %v", err)
	}
	want := "registry: " + registry + " (created)\nadded provider: ollama\nadded provider: claude\n" + unseeded
	if out.String() != want || errOut.Len() != 0 {
		t.Errorf("stdout = %q, stderr = %q; want stdout %q and no stderr", out.String(), errOut.String(), want)
	}
	if *synced != 1 {
		t.Errorf("route sync ran %d time(s), want once", *synced)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load after init: %v", err)
	}
	if cfg.ProviderByID("ollama") == nil || cfg.ProviderByID("claude") == nil {
		t.Errorf("providers after init = %+v, want ollama and claude", cfg.Providers)
	}

	// A second run finds nothing to do, writes nothing and syncs nothing.
	before, _ := os.ReadFile(registry)
	out.Reset()
	if err := runModelInit(&out, &errOut, false); err != nil {
		t.Fatalf("second runModelInit: %v", err)
	}
	if want := "registry: " + registry + "\nnothing to add\n" + unseeded; out.String() != want {
		t.Errorf("second run stdout = %q, want %q", out.String(), want)
	}
	if after, _ := os.ReadFile(registry); !bytes.Equal(before, after) {
		t.Error("a second init changed the registry")
	}
	if *synced != 1 {
		t.Errorf("route sync ran again on a run that changed nothing (%d calls)", *synced)
	}
}

// TestModelInitJSON pins the machine-readable document: one object, every
// key always present, arrays never null — so a script can read
// `.providers_added | length` without guarding. The route sync's own lines
// stay out of it.
func TestModelInitJSON(t *testing.T) {
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	registry := filepath.Join(home, ".config", "local-ai", "registry.toml")
	stubSeedEnv(t, config.SeedEnv{AgentProviders: []string{"corp-gateway"}, OnPath: onPath("mtplx")})
	old := syncRoutesAfterWrite
	syncRoutesAfterWrite = func(out, _ io.Writer) string {
		io.WriteString(out, "some/model: routed\n")
		return "LiteLLM routes not synced: proxy down"
	}
	t.Cleanup(func() { syncRoutesAfterWrite = old })

	var out, errOut bytes.Buffer
	if err := runModelInit(&out, &errOut, true); err != nil {
		t.Fatal(err)
	}
	want := `{"registry":"` + registry + `","created":true,"changed":true,"providers_added":["mtplx"],"providers_unseeded":["corp-gateway"],"warnings":["LiteLLM routes not synced: proxy down"]}` + "\n"
	if out.String() != want || errOut.Len() != 0 {
		t.Errorf("stdout = %s stderr = %q\nwant stdout %s and no stderr", out.String(), errOut.String(), want)
	}
	out.Reset()
	if err := runModelInit(&out, &errOut, true); err != nil {
		t.Fatal(err)
	}
	want = `{"registry":"` + registry + `","created":false,"changed":false,"providers_added":[],"providers_unseeded":["corp-gateway"],"warnings":[]}` + "\n"
	if out.String() != want {
		t.Errorf("no-op stdout = %s\nwant %s", out.String(), want)
	}

	// With nothing unseeded the key is an empty array, like the others.
	stubSeedEnv(t, config.SeedEnv{OnPath: onPath("mtplx")})
	out.Reset()
	if err := runModelInit(&out, &errOut, true); err != nil {
		t.Fatal(err)
	}
	want = `{"registry":"` + registry + `","created":false,"changed":false,"providers_added":[],"providers_unseeded":[],"warnings":[]}` + "\n"
	if out.String() != want {
		t.Errorf("nothing unseeded: stdout = %s\nwant %s", out.String(), want)
	}
}

// TestModelInitMakesAFreshConfigUsable is the reason `wt model init` seeds
// for the providers agents list: on a machine with a config.toml and no
// registry, one run of the command must leave wt able to load and validate
// its config. Here ollama is not installed and no model uses it, and one
// agent lists openrouter — the two rows a user used to have to write by hand
// before any wt command would start.
func TestModelInitMakesAFreshConfigUsable(t *testing.T) {
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	registry := filepath.Join(home, ".config", "local-ai", "registry.toml")
	if err := os.MkdirAll(config.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	const agents = `default_tag = "code"

[[agents]]
name = "claude"
supported_providers = ["claude", "openrouter"]

[[agents]]
name = "pi"
supported_providers = ["ollama"]
`
	if err := os.WriteFile(config.Path(), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	// The real reading of config.toml, on a machine with nothing installed.
	env := config.DefaultSeedEnv()
	env.OnPath = nil
	stubSeedEnv(t, env)
	stubRouteSync(t, "")

	var out, errOut bytes.Buffer
	if err := runModelInit(&out, &errOut, false); err != nil {
		t.Fatalf("runModelInit: %v", err)
	}
	want := "registry: " + registry + " (created)\n" +
		"added provider: ollama\nadded provider: openrouter\n" +
		"added provider: claude\nadded provider: pi\nadded provider: agy\n"
	if out.String() != want || errOut.Len() != 0 {
		t.Errorf("stdout = %q, stderr = %q; want stdout %q and no stderr", out.String(), errOut.String(), want)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load after init: %v", err)
	}
	if err := cfg.ValidateAll(); err != nil {
		t.Errorf("the config should validate after init: %v", err)
	}
}

// TestModelInitUnderARedirectedRegistry is the scratch-registry safety test
// for the one command that writes the registry in this step. With the
// registry redirected, the registry write always succeeds; what happens to
// LiteLLM's config.yaml — which follows neither registry variable — depends
// on whether WT_LITELLM_CONFIG names one. Unnamed, the default config.yaml
// must not be touched and the user is told why; named, that file is synced.
func TestModelInitUnderARedirectedRegistry(t *testing.T) {
	setup := func(t *testing.T) (registry, defaultYAML string) {
		home := t.TempDir()
		registry = filepath.Join(home, "scratch", "registry.toml")
		defaultYAML = filepath.Join(home, ".config", "litellm", "config.yaml")
		if err := os.MkdirAll(filepath.Dir(defaultYAML), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(defaultYAML, []byte("model_list: []\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("MODELMAN_REGISTRY", "")
		t.Setenv("WT_REGISTRY", registry)
		t.Setenv("WT_LITELLM_CONFIG", "")
		t.Setenv("MODELMAN_LITELLM_CONFIG", "")
		stubSeedEnv(t, config.SeedEnv{OnPath: onPath("ollama")})
		realRouteSync(t)
		return registry, defaultYAML
	}

	t.Run("config.yaml not named: registry written, routes refused, exit 0", func(t *testing.T) {
		registry, defaultYAML := setup(t)
		var out, errOut bytes.Buffer
		if err := runModelInit(&out, &errOut, false); err != nil {
			t.Fatalf("the registry write succeeded, so the command must too: %v", err)
		}
		if data, err := os.ReadFile(registry); err != nil || !strings.Contains(string(data), `id = "ollama"`) {
			t.Errorf("the redirected registry was not written (err %v):\n%s", err, data)
		}
		wantWarn := "warning: LiteLLM routes not touched: the registry is " + registry +
			" but config.yaml is the default " + defaultYAML +
			" — set WT_LITELLM_CONFIG to the config.yaml that registry belongs to\n"
		if errOut.String() != wantWarn {
			t.Errorf("stderr = %q\nwant %q", errOut.String(), wantWarn)
		}
		if data, _ := os.ReadFile(defaultYAML); string(data) != "model_list: []\n" {
			t.Errorf("the default config.yaml was touched:\n%s", data)
		}
	})

	t.Run("config.yaml named: registry written, that file synced, no warning", func(t *testing.T) {
		registry, defaultYAML := setup(t)
		named := filepath.Join(t.TempDir(), "scratch-config.yaml")
		if err := os.WriteFile(named, []byte("model_list: []\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("WT_LITELLM_CONFIG", named)
		var out, errOut bytes.Buffer
		if err := runModelInit(&out, &errOut, false); err != nil {
			t.Fatal(err)
		}
		if errOut.Len() != 0 {
			t.Errorf("stderr = %q, want no warning when config.yaml is named", errOut.String())
		}
		if _, err := os.Stat(registry); err != nil {
			t.Errorf("the redirected registry was not written: %v", err)
		}
		if data, _ := os.ReadFile(defaultYAML); string(data) != "model_list: []\n" {
			t.Errorf("the default config.yaml was touched:\n%s", data)
		}
	})

	t.Run("no LiteLLM at all: registry written, nothing said", func(t *testing.T) {
		registry, defaultYAML := setup(t)
		if err := os.Remove(defaultYAML); err != nil {
			t.Fatal(err)
		}
		var out, errOut bytes.Buffer
		if err := runModelInit(&out, &errOut, false); err != nil {
			t.Fatal(err)
		}
		if errOut.Len() != 0 {
			t.Errorf("stderr = %q, want silence on a machine with no LiteLLM config", errOut.String())
		}
		if _, err := os.Stat(registry); err != nil {
			t.Errorf("the redirected registry was not written: %v", err)
		}
		if _, err := os.Stat(defaultYAML); !os.IsNotExist(err) {
			t.Error("init must not create a config.yaml")
		}
	})
}

// TestModelInitReportsABrokenRegistryLink pins the refusal for a registry
// path that is a dangling symlink: an error with the link, its target and a
// hint, nothing created at the target, and no route sync.
func TestModelInitReportsABrokenRegistryLink(t *testing.T) {
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	link := filepath.Join(home, ".config", "local-ai", "registry.toml")
	target := filepath.Join(home, "unmounted", "registry.toml")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	stubSeedEnv(t, config.SeedEnv{OnPath: onPath("ollama")})
	synced := stubRouteSync(t, "")

	err := runModelInit(io.Discard, io.Discard, false)
	if !errors.Is(err, config.ErrRegistryLink) {
		t.Fatalf("err = %v, want config.ErrRegistryLink", err)
	}
	want := "registry link is broken: " + link + " is a symlink to " + target +
		", which does not exist (fix the link or move it aside)"
	if err.Error() != want {
		t.Errorf("err = %q\nwant  %q", err, want)
	}
	if _, statErr := os.Lstat(target); !os.IsNotExist(statErr) {
		t.Error("init created the link's target")
	}
	if *synced != 0 {
		t.Error("the route sync ran after a refused write")
	}
}

// TestModelCommandGroup pins the `wt model` group as cobra serves it in this
// step: `wt model init` runs with no registry (the case it exists for — the
// root command would refuse on the same load error), bare `wt model` prints
// help, an unknown word under it is an error rather than a worktree name, and
// the removed `wt models` keeps its own message.
func TestModelCommandGroup(t *testing.T) {
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	stubSeedEnv(t, config.SeedEnv{OnPath: onPath("ollama")})
	stubRouteSync(t, "")
	run := func(args ...string) (string, error) {
		var buf bytes.Buffer
		root := rootCmd()
		root.SetOut(&buf)
		root.SetErr(&buf)
		root.SetArgs(args)
		err := root.Execute()
		return buf.String(), err
	}

	out, err := run("model", "init", "--json")
	if err != nil || !strings.Contains(out, `"providers_added":["ollama"]`) {
		t.Errorf("wt model init --json = %q, %v", out, err)
	}
	out, err = run("model")
	if err != nil || !strings.Contains(out, "init") || !strings.Contains(out, "Manage the model registry") {
		t.Errorf("bare `wt model` should print the group's help, got %q, %v", out, err)
	}
	if _, err = run("model", "bogus"); err == nil || !strings.Contains(err.Error(), `unknown command "bogus" for "wt model"`) {
		t.Errorf("wt model bogus: err = %v, want an unknown-command error", err)
	}
	if _, err = run("model", "init", "extra"); err == nil {
		t.Error("wt model init takes no arguments")
	}
	if _, err = run("models"); err == nil || !strings.Contains(err.Error(), "wt models is removed") {
		t.Errorf("wt models: err = %v, want the removed-subcommand message", err)
	}
}

// TestRegistryWriteGuardIsArmedHere pins that this test binary, whose tests
// reach config.UpdateRegistry through `wt model init`, cannot write the
// developer's real registry: TestMain must have called
// config.IsolateConfigHomeForTest. Without it one test that unsets
// XDG_CONFIG_HOME would seed provider rows into the registry on the machine
// running the suite.
func TestRegistryWriteGuardIsArmedHere(t *testing.T) {
	if !config.RegistryWriteGuardArmed() {
		t.Fatal("registry write guard is not armed: TestMain must call config.IsolateConfigHomeForTest")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run, from `wt/`: `go test ./cmd/wt -run 'ModelInit|ModelCommandGroup'`

Expected: FAIL — the package does not build: `undefined: seedEnv`, `undefined: syncRoutesAfterWrite`, `undefined: realSyncRoutesAfterWrite`. The compiler stops after ten errors (`too many errors`), so `runModelInit`, which is undefined too, may not be listed.

- [ ] **Step 3: Write the command**

Create `wt/cmd/wt/model.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/spf13/cobra"
)

// seedEnv describes the machine `wt model init` seeds for: the configured
// agents, the providers they list, and what is on PATH. A seam so tests do
// not depend on what the developer has installed or configured.
var seedEnv = config.DefaultSeedEnv

// syncRoutesAfterWrite runs the one LiteLLM route sync that follows a
// registry write and returns a warning for the user, or "" when the routes
// are in step. A seam: unstubbed, a test that writes the registry would go on
// to probe the developer's providers and rewrite their config.yaml.
var syncRoutesAfterWrite = realSyncRoutesAfterWrite

// realSyncRoutesAfterWrite reloads the config (the registry just changed) and
// reconciles the routes exactly as `wt litellm sync` does. It never fails the
// command that wrote the registry: the write succeeded, and a sync that could
// not run is a warning. No LiteLLM setup at all (no config.yaml) is not even
// that. A redirected registry with no WT_LITELLM_CONFIG is refused by the
// sync (litellm.ErrRegistryRedirected), and the refusal is the warning.
func realSyncRoutesAfterWrite(out, errOut io.Writer) string {
	if _, err := os.Stat(litellm.DefaultPath()); os.IsNotExist(err) {
		// No LiteLLM here: nothing to sync, and no reason to probe providers.
		return ""
	}
	cfg, err := config.Load()
	if err != nil {
		return "LiteLLM routes not synced: " + err.Error()
	}
	err = runLitellmSync(out, errOut, cfg, false, false)
	switch {
	case err == nil, errors.Is(err, litellm.ErrMissing):
		return ""
	case errors.Is(err, litellm.ErrRegistryRedirected):
		return err.Error()
	}
	return "LiteLLM routes not synced: " + err.Error()
}

// modelCmd is the `wt model` group: the commands that write registry.toml.
// This step ships `init`; add, edit, rm, list and the Models tab follow.
func modelCmd(_ *app) *cobra.Command {
	c := &cobra.Command{
		Use:   "model",
		Short: "Manage the model registry (registry.toml)",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	var initJSON bool
	initC := &cobra.Command{
		Use:   "init",
		Short: "Create registry.toml if it is missing and add the default provider rows",
		Long: "Create the model registry if it does not exist, and add the provider rows wt\n" +
			"needs and the registry lacks:\n\n" +
			"  - ollama, omlx, mtplx: when the command is installed, or a model or a\n" +
			"                         configured agent uses it\n" +
			"  - mlx_lm_server:       when a model or a configured agent uses it\n" +
			"  - openrouter:          when a configured agent uses it; the row has no key,\n" +
			"                         so add auth.secret_ref to it in registry.toml\n" +
			"  - each configured agent: a native provider row under the agent's name\n\n" +
			"A provider an agent lists that wt has no default row for is named in the\n" +
			"output and left for you to add to registry.toml.\n\n" +
			"A row that exists is never changed, so running it again is safe. When it\n" +
			"changes the registry it then syncs the LiteLLM routes once; a sync that\n" +
			"cannot run is a warning, and the exit status is still 0.",
		Example: "  wt model init\n  wt model init --json",
		Args:    cobra.NoArgs,
		// A registry that cannot be read or written is not a usage mistake.
		SilenceUsage: true,
		// No config gate: this is the command that repairs a missing
		// registry, and it reads config.toml itself, tolerating a broken one.
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runModelInit(cmd.OutOrStdout(), cmd.ErrOrStderr(), initJSON)
		},
	}
	initC.Flags().BoolVar(&initJSON, "json", false, "machine-readable output")
	c.AddCommand(initC)
	return c
}

// modelInitJSON is `wt model init --json`'s one document.
type modelInitJSON struct {
	Registry       string   `json:"registry"`
	Created        bool     `json:"created"`
	Changed        bool     `json:"changed"`
	ProvidersAdded []string `json:"providers_added"`
	// ProvidersUnseeded are providers an agent lists that have no row and no
	// default: wt reports a config error until the user adds them.
	ProvidersUnseeded []string `json:"providers_unseeded"`
	Warnings          []string `json:"warnings"`
}

func runModelInit(out, errOut io.Writer, asJSON bool) error {
	path := config.RegistryPath()
	_, statErr := os.Lstat(path)
	missing := os.IsNotExist(statErr)

	env := seedEnv()
	var added, unseeded []string
	changed, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
		// Assigned afresh on every run: apply may run more than once.
		var err error
		added, unseeded, err = config.SeedRegistryDefaults(d, env)
		return err
	})
	if err != nil {
		if hint := config.RegistryFixHint(err); hint != "" {
			return fmt.Errorf("%w (%s)", err, hint)
		}
		return err
	}
	doc := modelInitJSON{
		Registry: path, Created: missing && changed, Changed: changed,
		ProvidersAdded:    append([]string{}, added...),
		ProvidersUnseeded: append([]string{}, unseeded...),
		Warnings:          []string{},
	}
	// The sync's own lines follow the registry lines in text mode and are
	// left out of the JSON document; its probe warnings go to stderr either way.
	var syncOut bytes.Buffer
	if changed {
		if w := syncRoutesAfterWrite(&syncOut, errOut); w != "" {
			doc.Warnings = append(doc.Warnings, w)
		}
	}
	if asJSON {
		return json.NewEncoder(out).Encode(doc)
	}
	if doc.Created {
		fmt.Fprintf(out, "registry: %s (created)\n", path)
	} else {
		fmt.Fprintf(out, "registry: %s\n", path)
	}
	for _, id := range added {
		fmt.Fprintf(out, "added provider: %s\n", id)
	}
	if len(added) == 0 {
		fmt.Fprintln(out, "nothing to add")
	}
	for _, id := range unseeded {
		fmt.Fprintf(out, "no default row for provider: %s (an agent lists it; add it to registry.toml by hand)\n", id)
	}
	_, _ = io.Copy(out, &syncOut)
	for _, w := range doc.Warnings {
		fmt.Fprintf(errOut, "warning: %s\n", w)
	}
	return nil
}
```

- [ ] **Step 4: Register the group**

In `wt/cmd/wt/main.go`, replace:

```go
	cmd.AddCommand(rotateCmd(a), configCmd(a), statsCmd(a), smokeCmd(a), stopCmd(a), startCmd(a), servedCmd(a), warmCmd(a), litellmCmd(a), profileCmd(a))
```

with:

```go
	cmd.AddCommand(rotateCmd(a), configCmd(a), statsCmd(a), smokeCmd(a), stopCmd(a), startCmd(a), servedCmd(a), warmCmd(a), litellmCmd(a), profileCmd(a), modelCmd(a))
```

The removed-name guard a few lines up (`case "models":`, `main.go:235`) stays as it is: `wt models` is still not a command, and `TestModelCommandGroup` pins that it keeps its own message.

- [ ] **Step 5: Stub the seams for the whole test binary**

In `wt/cmd/wt/testmain_test.go`, in `TestMain`, replace:

```go
	confirmStop = func(string) (bool, error) { return false, nil }
	code := m.Run()
```

with:

```go
	confirmStop = func(string) (bool, error) { return false, nil }
	// Registry-write seams: no test may read the developer's PATH to decide
	// what to seed, and none may go on from a registry write to the real
	// route sync (which probes providers and can restart the proxy). Tests of
	// `wt model init` call stubSeedEnv, and realRouteSync for the sync itself.
	seedEnv = func() config.SeedEnv { return config.SeedEnv{} }
	syncRoutesAfterWrite = func(io.Writer, io.Writer) string {
		return "syncRoutesAfterWrite not stubbed in this test"
	}
	code := m.Run()
```

- [ ] **Step 6: Run the tests to verify they pass**

Run, from `wt/`: `go test -count=1 ./cmd/wt`

Expected: `ok`.

- [ ] **Step 7: Commit**

```bash
test -z "$(gofmt -l .)" && go vet ./...
git add cmd/wt/model.go cmd/wt/model_test.go cmd/wt/main.go cmd/wt/testmain_test.go
git commit -m "feat(wt): wt model init creates the registry and seeds provider rows"
```

### Task 16: The missing-registry hint names `wt model init`, and the docs

**Files:**
- Modify: `wt/cmd/wt/helpers.go:103-107`
- Modify: `wt/internal/config/registry.go` (the `missing` error in `loadRegistry`, the comment above the function, and one other comment)
- Modify: `wt/internal/config/config.go`, `wt/internal/configeditor/editor.go` (comments that call the registry modelman's alone)
- Modify: `wt/internal/config/registry_test.go:116-131` (`TestLoad_FailsClosedWithoutRegistry`)
- Modify: `wt/cmd/wt/helpers_test.go` (one test appended)
- Modify: `wt/CLAUDE.md`, `wt/docs/internals/config-and-registry.md`, `wt/docs/internals/testing.md`, `wt/README.md`, `wt/docs/configuration.md`, `wt/docs/wt-agents/shell-wt.md`, `wt/CHANGELOG.md`, `docs/guides/00-config-map.md`, `docs/guides/02-providers-and-models.md`, `modelman/README.md`, `CLAUDE.md`

**Interfaces:**
- Consumes: `wt model init` (Task 15).
- Produces: the exact text `config error: model registry not found at <path> — seed it with `wt model init``. The command is named once.

`modelman migrate` is a legacy-YAML import, and modelman is being retired; the command that creates a registry is now `wt model init`. Other mentions of `modelman migrate` in wt's Go comments (`config.go:549`, `migrate_test.go:15`) describe that legacy import and stay.

Today the line a user reads says the same thing twice: `loadRegistry`'s error carries `— seed it with …`, and `configError` adds `(seed the registry with …)`. The owner's decision (2026-10-07) is that the hint appears once. `loadRegistry`'s error keeps it, because that error is also what `wt litellm sync` and the commands that do not go through `configError` print; `configError` stops adding its own. The Decisions table has the row.

- [ ] **Step 1: Write the failing tests**

In `wt/internal/config/registry_test.go`, replace the comment and the last assertion of `TestLoad_FailsClosedWithoutRegistry`. Replace:

```go
// TestLoad_FailsClosedWithoutRegistry asserts Load() errors when
// registry.toml is absent — wrapping ErrRegistryMissing and pointing at
// `modelman migrate` — so wt never silently runs with zero providers/models
// before modelman has imported anything.
```

with:

```go
// TestLoad_FailsClosedWithoutRegistry asserts Load() errors when
// registry.toml is absent — wrapping ErrRegistryMissing and pointing at
// `wt model init`, the command that creates one — so wt never silently runs
// with zero providers/models, and never sends the user to a tool that is
// being retired.
```

and replace:

```go
	if !strings.Contains(err.Error(), "modelman migrate") {
		t.Errorf("error should point at `modelman migrate`, got: %v", err)
	}
```

with:

```go
	if !strings.Contains(err.Error(), "seed it with `wt model init`") {
		t.Errorf("error should point at `wt model init`, got: %v", err)
	}
	if strings.Contains(err.Error(), "modelman") {
		t.Errorf("error still names modelman: %v", err)
	}
```

Append to `wt/cmd/wt/helpers_test.go`:

```go
// TestConfigErrorForAMissingRegistryNamesModelInit pins the whole line a
// user reads when there is no registry: where it was looked for and the one
// command that creates it, named once. It used to name `modelman migrate`, a
// legacy import in a tool that is being retired, and to say it twice.
func TestConfigErrorForAMissingRegistryNamesModelInit(t *testing.T) {
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	_, loadErr := config.Load()
	if !errors.Is(loadErr, config.ErrRegistryMissing) {
		t.Fatalf("Load error = %v, want config.ErrRegistryMissing", loadErr)
	}
	registry := filepath.Join(home, ".config", "local-ai", "registry.toml")
	want := "config error: model registry not found at " + registry +
		" — seed it with `wt model init`"
	if got := configError(loadErr).Error(); got != want {
		t.Errorf("configError =\n  %q\nwant\n  %q", got, want)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run, from `wt/`:

```bash
go test ./internal/config -run TestLoad_FailsClosedWithoutRegistry
go test ./cmd/wt -run TestConfigErrorForAMissingRegistryNamesModelInit
```

Expected: both FAIL. The first prints `error should point at `wt model init``; the second prints the old line, ending `— seed it with `modelman migrate` (seed the registry with `modelman migrate`)`.

- [ ] **Step 3: Change the hint, and say it once**

In `wt/internal/config/registry.go`, in `loadRegistry`, replace:

```go
	missing := fmt.Errorf("%w at %s — seed it with `modelman migrate`", ErrRegistryMissing, path)
```

with:

```go
	missing := fmt.Errorf("%w at %s — seed it with `wt model init`", ErrRegistryMissing, path)
```

and in the comment above the function replace:

```go
// editor for this file; seed it once with `modelman migrate`. A symlinked
```

with:

```go
// editor for this file; `wt model init` creates it. A symlinked
```

In `wt/cmd/wt/helpers.go`, replace:

```go
// ErrRegistryMissing is a special case: the registry is missing entirely, so
// there's nothing to edit; the hint should say to seed it with modelman.
func configError(err error) error {
	if errors.Is(err, config.ErrRegistryMissing) {
		return fmt.Errorf("config error: %w (seed the registry with `modelman migrate`)", err)
	}
```

with:

```go
// ErrRegistryMissing is a special case: the registry is missing entirely, so
// there's nothing to edit, and the error itself already names the command
// that creates one (`wt model init`). No hint is added: it would say the
// same thing a second time.
func configError(err error) error {
	if errors.Is(err, config.ErrRegistryMissing) {
		return fmt.Errorf("config error: %w", err)
	}
```

- [ ] **Step 4: Correct the comments that call the registry modelman's alone**

wt writes the registry from this PR on, so nine Go comments are no longer true. Comments only; no behaviour changes. Each "replace" text occurs once in its file.

In `wt/internal/config/config.go`, replace:

```go
// wt-owned config.toml; Providers + Models come from modelman-owned
// registry.toml and are never persisted by wt (see Save).
```

with:

```go
// wt-owned config.toml; Providers + Models come from the shared
// registry.toml and are never persisted from a Config (see Save).
```

and replace:

```go
// modelman-owned registry.toml (Providers + Models) into one in-memory
```

with:

```go
// the shared registry.toml (Providers + Models) into one in-memory
```

and replace:

```go
	// Join modelman-owned registry LAST so wt never mutates registry data
```

with:

```go
	// Join the registry LAST so wt never mutates registry data
```

and replace:

```go
// registry.toml — modelman's file, which `wt config` cannot edit — or "" for
```

with:

```go
// registry.toml — a file `wt config` cannot edit — or "" for
```

and replace:

```go
// The registry is modelman's file and wt only reads it, so any other value —
```

with:

```go
// The registry is shared with modelman, so any other value —
```

and replace:

```go
// live in modelman-owned registry.toml and are never written by wt.
```

with:

```go
// live in registry.toml, which Save never writes (UpdateRegistry does).
```

In `wt/internal/config/registry.go`, replace:

```go
// ErrRegistryMissing is returned by loadRegistry when the modelman-owned
```

with:

```go
// ErrRegistryMissing is returned by loadRegistry when the shared
```

and replace:

```go
// loadRegistry decodes modelman-owned registry.toml into providers and
```

with:

```go
// loadRegistry decodes the shared registry.toml into providers and
```

In `wt/internal/configeditor/editor.go`, replace:

```go
// section of config.toml. Providers and models live in modelman-owned
// registry.toml and are read-only for wt, so the editor only manages agents.
```

with:

```go
// section of config.toml. Providers and models live in the shared
// registry.toml, which this editor never writes, so it only manages agents.
```

Run, from `wt/`: `test -z "$(gofmt -l .)" && go vet ./internal/config ./internal/configeditor`

Expected: no output, exit 0.

- [ ] **Step 5: Run the tests to verify they pass**

Run, from `wt/`: `go test -count=1 ./internal/config ./internal/configeditor ./cmd/wt`

Expected: all three `ok`.

- [ ] **Step 6: Update the docs**

wt is now a writer of the registry through one command. The wording below says that and no more: models are still added, edited and removed with modelman until Step 3.

In `wt/CLAUDE.md`, replace:

```text
## Registry (modelman-owned)

`~/.config/local-ai/registry.toml` holds the canonical Providers/Models. wt loads it read-only via `config.Load`, fail-closed, and joins it in memory with `config.toml`.
```

with:

```text
## Registry (shared with modelman until it is retired)

`~/.config/local-ai/registry.toml` holds the canonical Providers/Models. wt loads it via `config.Load`, fail-closed, and joins it in memory with `config.toml`. `config.Load` never writes it; wt's one writer today is `wt model init` (`config.SeedRegistryDefaults` inside `config.UpdateRegistry`), and modelman still writes it too.
```

In `wt/CLAUDE.md`, replace:

```text
`~/.config/agent-wt/config.toml` is wt-owned and holds **only Agents, DefaultTag, and the `[litellm]` routing table**. Providers/Models live in the registry; wt never writes them.
```

with:

```text
`~/.config/agent-wt/config.toml` is wt-owned and holds **only Agents, DefaultTag, and the `[litellm]` routing table**. Providers/Models live in the registry; nothing in this file's write path touches them.
```

In `wt/CLAUDE.md`, replace:

```text
Fix with `modelman sync`/`modelman migrate` on that machine (`sync` recreates default provider entries), not a code change here.
```

with:

```text
Fix with `wt model init` on that machine (it adds a default row for each provider that is installed, used by a model or listed by a configured agent, and a native row per configured agent; a provider it has no default row for is named in its output and is a hand edit of `registry.toml`), not a code change here.
```

In `wt/CLAUDE.md`, replace:

```text
| `cmd/wt/litellm.go` | `wt litellm ...` |
```

with:

```text
| `cmd/wt/litellm.go` | `wt litellm ...` |
| `cmd/wt/model.go` | `wt model` group; `wt model init [--json]` — creates the registry and seeds provider rows, then one route sync (`syncRoutesAfterWrite`) |
```

In `wt/CLAUDE.md`, replace:

```text
wt litellm list / sync / status      # routed ids, reconcile cloud + running local routes, routing state
```

with:

```text
wt litellm list / sync / status      # routed ids, reconcile cloud + running local routes, routing state
wt model init [--json]               # create registry.toml if missing; add default provider rows (safe to re-run)
```

In `wt/docs/internals/config-and-registry.md`, replace:

```text
Providers/Models live in the registry (below); wt never writes them.
```

with:

```text
Providers/Models live in the registry (below); nothing that writes `config.toml` touches them.
```

In `wt/docs/internals/config-and-registry.md`, replace:

```text
## Registry (modelman-owned)
```

with:

```text
## Registry (shared with modelman until it is retired)
```

In `wt/docs/internals/config-and-registry.md`, replace:

```text
wt loads it read-only via `config.Load` (fail-closed: missing/malformed registry is an error; seed with `modelman migrate`) and joins it in memory with `config.toml`.
```

with:

```text
wt loads it via `config.Load` (fail-closed: missing/malformed registry is an error; `wt model init` creates one) and joins it in memory with `config.toml`. `Load` never writes it and never seeds it.
```

In `wt/docs/internals/config-and-registry.md`, replace:

```text
Fix with `modelman sync`/`modelman migrate` on that machine (`sync` recreates default provider entries), not a code change here.
```

with:

```text
Fix with `wt model init` on that machine, not a code change here.

**Seeding.** `config.SeedRegistryDefaults(d *RegistryDoc, env SeedEnv)` (`registry_seed.go`) is the one seeding implementation, the Go port of modelman's `_ensure_provider_entries` and `sync_agent_providers` plus one trigger modelman lacks. It appends the default row for ollama, omlx, mtplx or mlx_lm_server when a model references it, when a configured agent lists it in `supported_providers`, or (the first three) when its command is on PATH — an installed omlx is skipped when an `omlx-6bit` row exists; a default row for openrouter when an agent lists it (name, address and `auth.type = "api_key"`, with no `secret_ref`: the key is the user's to add); and a native cloud row for each agent in `config.toml`. The agent trigger exists because `Config.Validate` refuses an agent that lists a provider with no row. The agents are read as the next `Load` will see them: `seedAgents` applies `migrateAgentRefs` (the agent half of `migrateConfigSchema`: an `agy` agent is added, `opencode` lists `ollama` only) to its own read of `config.toml`, because `Load` migrates that file only once the registry loads. It returns the ids it added and, separately, the providers an agent lists that still have no row because wt has no default for them; `wt model init` names those (`providers_unseeded` in `--json`). It never edits a row that exists; modelman's `backfill_provider_defaults` is deliberately not ported. It takes a `RegistryDoc`, so it runs inside the caller's own `UpdateRegistry`: `wt model init` today, `wt model add` next. `config.DefaultSeedEnv()` reads the real machine; `cmd/wt` reaches it through the `seedEnv` seam.
```

In `wt/docs/internals/testing.md`, replace:

```text
`stopCandidates`, `stopEntries`, `stopPickerAll`, `confirmStop`, `ensureModelRoute`, `loadProfileStore`, `confirmProfile`, `openTTY`, `profileApplier`, `loadProxyEnv`)
```

with:

```text
`stopCandidates`, `stopEntries`, `stopPickerAll`, `confirmStop`, `ensureModelRoute`, `loadProfileStore`, `confirmProfile`, `openTTY`, `profileApplier`, `loadProxyEnv`, `seedEnv`, `syncRoutesAfterWrite`)
```

In `wt/docs/internals/testing.md`, replace:

```text
Each package has a `stubEnsureRoute(t)` helper for tests that assert on the launch-time route check, and a new launch path must call the check through that seam.
```

with:

```text
Each package has a `stubEnsureRoute(t)` helper for tests that assert on the launch-time route check, and a new launch path must call the check through that seam. The same `TestMain` makes `seedEnv` describe a machine with nothing installed and makes `syncRoutesAfterWrite` return a "not stubbed" warning: a command that writes the registry (`wt model init`) would otherwise read the developer's PATH and go on to the real route sync. Tests use `stubSeedEnv`, `stubRouteSync` and, for the real sync against a scratch `config.yaml`, `realRouteSync` (`cmd/wt/model_test.go`).
```

In `wt/README.md`, replace:

```text
They live in `~/.config/local-ai/registry.toml`, owned by `modelman` and seeded with `modelman migrate`. `wt` loads that file read-only and joins it in memory with `config.toml`; `wt config` never writes providers or models.
```

with:

```text
They live in `~/.config/local-ai/registry.toml`. `wt model init` creates that file and adds the default provider rows; models are added with `modelman` for now. `wt` joins the registry in memory with `config.toml`; `wt config` never writes providers or models.
```

In `wt/README.md`, replace:

```text
`wt` itself only ever reads Providers/Models back out of the registry.
```

with:

```text
`wt` itself reads Providers/Models back out of the registry, and writes that file only through `wt model init`.
```

In `wt/docs/configuration.md`, replace:

```text
`modelman migrate` is the opt-in that seeds the registry and unlocks model routing/rotation.
```

with:

```text
`wt model init` creates the registry and its provider rows; models are added with `modelman` for now. A registry with models in it is what unlocks model routing/rotation.
```

In `docs/guides/02-providers-and-models.md` and again in `modelman/README.md` (the same table row is in both), replace:

```text
(shared, read-only by other tools)
```

with:

```text
(shared; `wt model init` also writes it)
```

In `docs/guides/02-providers-and-models.md` only, replace:

```text
`registry.toml` is canonical — add the provider block to `~/.config/local-ai/registry.toml` (hand-edit, or via the TUI's add flow; both write this file).
```

with:

```text
`registry.toml` is canonical — add the provider block to `~/.config/local-ai/registry.toml` (hand-edit, or via the TUI's add flow; both write this file). If `wt model init` already added an `openrouter` row (it does when a configured agent lists openrouter), do not add a second one: add `secret_ref` to the row that is there.
```

In `wt/docs/wt-agents/shell-wt.md`, replace:

```text
`shell-wt` works on a fresh machine without a `modelman` registry; model-driven agents still require `modelman migrate` to populate `registry.toml`.
```

with:

```text
`shell-wt` works on a fresh machine without a model registry; model-driven agents still need one (`wt model init` creates `registry.toml`).
```

In `docs/guides/00-config-map.md`, replace:

```text
| `~/.config/local-ai/registry.toml` | `modelman` (TUI add/edit, `modelman migrate`) | `wt` (read-only; source of the LiteLLM routes), `llmbench` (read-only) | Canonical providers + models |
```

with:

```text
| `~/.config/local-ai/registry.toml` | `modelman` (TUI add/edit, `modelman migrate`) and `wt` (`wt model init`: creates it, adds provider rows) | `wt` (source of the LiteLLM routes), `modelman`, `llmbench` (read-only) | Canonical providers + models |
```

In `docs/guides/00-config-map.md`, replace:

```text
- **Owner:** `modelman` — TUI queue applies on exit, and `modelman migrate`.
- **Consumers:** `wt` (read-only; joins it in memory with `~/.config/agent-wt/config.toml` and builds the LiteLLM `model_list` entries from it, copying each model's `model_info`).
```

with:

```text
- **Owner:** two writers until modelman is retired. `modelman` — TUI queue applies on exit, and `modelman migrate`. `wt` — `wt model init` only, for now: it creates the file when it is missing and appends provider rows, never editing a row that exists. wt takes a lock (`registry.toml.lock`, beside the file) and re-checks the file before replacing it; modelman refuses to save over a file another program changed since it loaded it (`Registry not saved: registry.toml changed on disk; reload` — restart modelman).
- **Consumers:** `wt` (joins it in memory with `~/.config/agent-wt/config.toml` and builds the LiteLLM `model_list` entries from it, copying each model's `model_info`), `llmbench` (read-only).
```

In `CLAUDE.md`, replace:

```text
Canonical owner of `registry.toml` and `modelman.toml` (download/ready/running state).
```

with:

```text
Writes `registry.toml` (still the tool that adds, edits and removes models) and owns `modelman.toml` (download/ready/running state); it refuses to save a registry another program changed since it loaded it.
```

In `CLAUDE.md`, replace:

```text
Reads modelman's `registry.toml` read-only and, from `modelman.toml`, only the global
```

with:

```text
Reads `registry.toml` and writes it through one path, `config.UpdateRegistry` (lock, symlink write-through, byte-stable output via `internal/tomlw`), whose only caller so far is `wt model init` (create the file, seed provider rows); from `modelman.toml` it reads only the global
```

In `wt/CHANGELOG.md`, replace:

```text
  which keeps working as an alias; modelman and llmbench read the same name.
```

with:

```text
  which keeps working as an alias; modelman and llmbench read the same name.
- `wt model init [--json]` creates the model registry when it is missing and
  adds the provider rows it lacks: ollama, omlx and mtplx when installed, used
  by a model or listed by a configured agent; mlx_lm_server when used by a
  model or listed by an agent; openrouter, without a key, when an agent lists
  it; and a native row for each configured agent. It never changes a row that
  exists, and it names any provider an agent lists that it has no default row
  for. The "model registry not found" error now names this command, once,
  where it named `modelman migrate` twice.
```

`docs/guides/01-initial-setup.md` still seeds a new machine with `modelman migrate`, which still works. It is rewritten with the other guides in the retirement's last step (the spec's Step 6, slice 1), not here.

- [ ] **Step 7: Try the command on a throwaway home**

This check uses a temporary `HOME` holding two fake provider commands and a `config.toml`. ollama is not installed there: its row has to come from the agent trigger, through the `opencode` agent that `Load` will rewire to ollama, and the `openrouter` row from the `claude` agent's list. Nothing under the real home is read or written, and no provider is contacted: with no LiteLLM `config.yaml` in that home the route sync is skipped before it probes anything.

Run, from `wt/`:

```bash
go build -o /tmp/wt-verify ./cmd/wt
H=$(mktemp -d)
mkdir -p "$H/fakebin" "$H/.config/agent-wt"
for c in omlx mtplx; do printf '#!/bin/sh\nexit 0\n' > "$H/fakebin/$c"; chmod +x "$H/fakebin/$c"; done
printf 'default_tag = "code"\n\n[[agents]]\nname = "claude"\nsupported_providers = ["claude", "openrouter"]\n\n[[agents]]\nname = "opencode"\nsupported_providers = ["opencode"]\n' > "$H/.config/agent-wt/config.toml"
env -i HOME="$H" PATH="$H/fakebin:/usr/bin:/bin" /tmp/wt-verify model init
```

Expected, with `$H` standing for the temporary directory:

```
registry: $H/.config/local-ai/registry.toml (created)
added provider: ollama
added provider: omlx
added provider: mtplx
added provider: openrouter
added provider: claude
added provider: opencode
added provider: agy
```

The seeded `openrouter` row must hold no key. Run `grep -c secret_ref "$H/.config/local-ai/registry.toml"`. Expected: `0`.

Then check that modelman reads what wt wrote, and that its own save changes only what it always adds. Run, from the monorepo root:

```bash
uv run --directory modelman python - "$H/.config/local-ai/registry.toml" <<'PY'
import sys, tomllib, tomli_w
from pathlib import Path
from modelman.registry import load_registry, save_registry
path = Path(sys.argv[1])
wrote = path.read_text()
print("tomli-w form:", tomli_w.dumps(tomllib.loads(wrote)) == wrote)
registry = load_registry(path)
print("providers:", [p.id for p in registry.providers])
save_registry(registry, path)
print("modelman added:", repr(path.read_text().removesuffix(wrote)))
PY
```

Expected:

```
tomli-w form: True
providers: ['ollama', 'omlx', 'mtplx', 'openrouter', 'claude', 'opencode', 'agy']
modelman added: 'families = []\nmodels = []\n\n'
```

modelman puts the two empty keys ahead of the provider rows and changes nothing else. Then run wt again:

```bash
env -i HOME="$H" PATH="$H/fakebin:/usr/bin:/bin" /tmp/wt-verify model init
```

Expected: on stderr, wt's one-time notice `wt: migrated config to native-provider alignment (renamed google→agy, rewired opencode to ollama-only)` (the registry now loads, so wt migrates the throwaway `config.toml`); on stdout:

```
registry: $H/.config/local-ai/registry.toml
nothing to add
```

That the migration ran without an error is the end-to-end check of the agent trigger: the migrated `config.toml` now has an `opencode` agent that lists `ollama` and an `agy` agent, and the registry seeded before the migration already had both rows. `grep -A2 'name = "opencode"' "$H/.config/agent-wt/config.toml"` shows `supported_providers = ["ollama"]`.

Clean up with `rm -r "$H" /tmp/wt-verify`. If any output differs, stop and report it; do not adjust the expected text.

- [ ] **Step 8: Verify the PR and commit**

Run, from `wt/`:

```bash
test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./... && make check
```

Expected: every package `ok`; `make check` ends without an error.

Run, from the monorepo root: `make test-all`

Expected: exit 0.

```bash
git add wt/cmd/wt/helpers.go wt/cmd/wt/helpers_test.go wt/internal/config/registry.go wt/internal/config/registry_test.go wt/internal/config/config.go wt/internal/configeditor/editor.go wt/CLAUDE.md wt/docs/internals/config-and-registry.md wt/docs/internals/testing.md wt/README.md wt/docs/configuration.md wt/docs/wt-agents/shell-wt.md wt/CHANGELOG.md docs/guides/00-config-map.md docs/guides/02-providers-and-models.md modelman/README.md CLAUDE.md
git commit -m "fix(wt): the missing-registry hint names wt model init"
```

Stop here. Pushing `feat/wt-model-init` and opening the PR need the owner's OK. A run of `wt model init` against the owner's real registry is the owner's to do, after `make install`; on a registry that already has its provider rows it prints `nothing to add` and leaves the file byte-identical.

---

## PR 6 — modelman refuses to save over another program's write

Branch `fix/modelman-stale-registry-snapshot`. It needs none of PRs 1 to 4 and can be built beside them. It must merge before PR 5, which ships wt's first registry write and whose docs describe this refusal.

modelman's registry lock is a `threading.Lock` (`registry.py:826`): it serialises modelman's own threads and nothing else. Its TUI loads the registry once and saves that whole snapshot on every edit (`screens/models.py:850`). Once wt writes the file too, an open modelman would silently revert a wt edit on its next save. wt cannot prevent that from its side — its flock and re-check cover wt's own writes — so modelman checks before it writes.

### Task 17: The stale-snapshot guard

**Files:**
- Modify: `modelman/src/modelman/registry.py:211-214` (`Registry`), `:562-573` (`load_registry`), `:794-823` (`_write_registry`)
- Create: `modelman/tests/test_registry_stale_snapshot.py`
- Modify: `modelman/CLAUDE.md`

**Interfaces:**
- Consumes: `load_registry(path)`, `save_registry(registry, path)`, `locked_registry(path)`, `_write_registry(registry, path)`, `RegistryError` (module `modelman.registry`).
- Produces (module-private): `_SEEN_ON_DISK: dict[str, tuple[int, int, int] | None]`, `_FOREIGN_CHANGES: dict[str, int]`, `_SEEN_LOCK`, `_stamp_of(st)`, `_disk_stamp(path)`, `_note_loaded(path, stamp) -> tuple[str, int]`, `_record_seen(path)`, `_refuse_stale_snapshot(registry, path)`; the field `Registry._loaded_at: tuple[str, int] | None`, set by `load_registry`. `_write_registry` raises `RegistryError("registry.toml changed on disk; reload")` for a file another program changed. No public signature changes.

How the guard decides. Two records are kept per process, keyed by the resolved path. `_SEEN_ON_DISK` is the file as this process last read or wrote it: `(mtime_ns, size, inode)`, or `None` for "looked, and there was no file". `_FOREIGN_CHANGES` counts the loads that found the file different from that record, which is another program's write. Each `Registry` that `load_registry` returns remembers the count at its load. A save is refused when the file on disk differs from the record, or when the registry being saved was loaded before the count last moved. The second test is what keeps modelman's own background load (the price refresh, `app.py:111` and `:138`) from accepting wt's write on behalf of the screen's older snapshot.

No caller changes. The screens' `_save_registry` already catches `RegistryError` and shows `Registry not saved: registry.toml changed on disk; reload`, and `PendingChanges._persist` (`queue.py:294`) reports it as `save:fail`. Three CLI saves already catch it too (`save_registry` at `main.py:591`, `:661` and `:696`, each with `except (OSError, RegistryError)` on the next line).

Two things this task leaves as they are, on purpose:

- **Two writers do not catch `RegistryError`**: `modelman migrate` (`main.py:507`, a bare `save_registry`) and `modelman ollama-catalog sync` (`ollama_catalog_cli.py:159`, whose `locked_registry` block catches `OSError` only, at line 176). A refusal there ends in a traceback whose last line is the reload message. Both windows are milliseconds wide, and modelman is frozen to bug fixes, so the handlers are not widened (Decisions table).
- **The TUI cannot reload a registry**: `ModelScreen.reload()` (`screens/models.py:475`) only redraws the table. "reload" in the message therefore means quit modelman and open it again, and after one write by another program every save in that session is refused. That includes the one save at the end of a queued Apply, whose deletes and downloads have already run by then. Step 5 says so in `modelman/CLAUDE.md`.

What bounds that. The owner's decision (2026-10-07) is that modelman's TUI is disabled when wt's Models tab ships, in Step 3: the PR series that ships the tab makes bare `modelman` print where the Models tab is and exit non-zero. It is not disabled in this step, which would leave no model editor until then. So an open TUI that refuses every save exists only between `wt model init` (PR 5) and Step 3, and in that window the one wt write is `wt model init`, which changes the registry only when a provider row is missing. This task ships the guard as planned, and nothing in it disables the TUI. After Step 3 the guard still protects the registry from modelman's non-interactive writers, until modelman is deleted in Step 6.

- [ ] **Step 1: Write the failing tests**

Create `modelman/tests/test_registry_stale_snapshot.py`:

```python
"""modelman must not save an old snapshot over a registry another program wrote.

wt writes registry.toml now. modelman's lock is in-process only and its TUI
saves the whole Registry it loaded at start, so without this guard an open
modelman would revert a `wt model` edit on its next save. Deleted with
modelman.
"""

import os
import tomllib
from pathlib import Path

import pytest

from modelman import registry as registry_module
from modelman.queue import PendingChanges
from modelman.registry import (
    AuthConfig,
    ModelEntry,
    ProviderEntry,
    Registry,
    RegistryError,
    RegistryNotFoundError,
    load_registry,
    locked_registry,
    save_registry,
)
from modelman.state import StateStore

STALE = "registry.toml changed on disk; reload"


def _seed(path: Path) -> Registry:
    """Write a one-provider registry at `path` and load it, as a TUI would."""
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\n\n[providers.auth]\ntype = "none"\n',
        encoding="utf-8",
    )
    return load_registry(path)


def _as_wt_would(path: Path) -> str:
    """Another program replaces the file (atomically, as wt does) with a
    registry holding one more model. Returns the new text."""
    text = path.read_text(encoding="utf-8") + (
        '\n[[models]]\nid = "ollama/from-wt"\nfamily = "f"\n'
        'provider_id = "ollama"\nmodel_name = "from-wt"\n'
    )
    tmp = path.with_name(path.name + ".tmp")
    tmp.write_text(text, encoding="utf-8")
    os.replace(tmp, path)
    return text


def _add_model(registry: Registry, name: str) -> None:
    registry.models.append(
        ModelEntry(id=f"ollama/{name}", family="f", provider_id="ollama", model_name=name)
    )


def test_a_save_over_another_programs_write_is_refused(tmp_path):
    """The case the guard exists for: modelman loaded, wt wrote, modelman
    saves. The save must fail with the reload message and leave wt's file."""
    path = tmp_path / "registry.toml"
    registry = _seed(path)
    wt_text = _as_wt_would(path)

    _add_model(registry, "from-modelman")
    with pytest.raises(RegistryError, match=STALE):
        save_registry(registry, path)
    assert path.read_text(encoding="utf-8") == wt_text
    assert not list(tmp_path.glob(".registry.toml.*")), "a temp file was left behind"


def test_a_change_that_keeps_the_size_is_still_caught(tmp_path):
    """Size alone would miss an edit that swaps one character; the
    modification time catches it."""
    path = tmp_path / "registry.toml"
    registry = _seed(path)
    st = path.stat()
    path.write_text(path.read_text(encoding="utf-8").replace("Ollama", "OLLAMA"), encoding="utf-8")
    os.utime(path, ns=(st.st_atime_ns, st.st_mtime_ns + 1_000_000_000))
    assert path.stat().st_size == st.st_size

    with pytest.raises(RegistryError, match=STALE):
        save_registry(registry, path)


def test_modelmans_own_saves_never_trip_the_guard(tmp_path):
    """A successful write records the new file as seen. Without that the
    second save in one TUI session would be refused — and so would the
    screen's save after the background price refresh (locked_registry) wrote."""
    path = tmp_path / "registry.toml"
    screen_registry = _seed(path)

    _add_model(screen_registry, "first")
    save_registry(screen_registry, path)
    _add_model(screen_registry, "second")
    save_registry(screen_registry, path)

    with locked_registry(path) as fresh:  # the price-refresh worker
        _add_model(fresh, "from-worker")
    _add_model(screen_registry, "third")
    save_registry(screen_registry, path)  # an older object, but no other program wrote

    assert [m.id for m in load_registry(path).models] == [
        "ollama/first",
        "ollama/second",
        "ollama/third",
    ]


def test_a_reload_clears_the_refusal(tmp_path):
    """The message says to reload, so that has to work: loading the registry
    again makes the next save acceptable, and it carries the other program's
    edit."""
    path = tmp_path / "registry.toml"
    registry = _seed(path)
    _as_wt_would(path)
    with pytest.raises(RegistryError, match=STALE):
        save_registry(registry, path)

    reloaded = load_registry(path)
    _add_model(reloaded, "from-modelman")
    save_registry(reloaded, path)
    assert [m.id for m in load_registry(path).models] == ["ollama/from-wt", "ollama/from-modelman"]


def test_locked_registry_applies_on_top_of_another_programs_write(tmp_path):
    """locked_registry re-reads the file before it writes, so it is never
    stale: the price refresh lands on top of wt's edit instead of failing."""
    path = tmp_path / "registry.toml"
    _seed(path)
    _as_wt_would(path)
    with locked_registry(path) as fresh:
        _add_model(fresh, "priced")
    assert [m.id for m in load_registry(path).models] == ["ollama/from-wt", "ollama/priced"]


def test_a_first_save_is_never_refused(tmp_path):
    """A path this process never looked at has no snapshot to be stale against:
    `modelman migrate` writing a new registry, or a save over a file that was
    never read, must work as before."""
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))]
    )
    new = tmp_path / "new" / "registry.toml"
    save_registry(registry, new)
    assert load_registry(new).provider("ollama").id == "ollama"

    never_loaded = tmp_path / "other.toml"
    never_loaded.write_text("providers = []\n", encoding="utf-8")
    save_registry(registry, never_loaded)
    assert load_registry(never_loaded).provider("ollama").id == "ollama"


def test_a_registry_deleted_since_it_was_loaded_is_refused(tmp_path):
    """A file that is gone has changed on disk too. Recreating it from an old
    snapshot would undo whoever removed or moved it."""
    path = tmp_path / "registry.toml"
    registry = _seed(path)
    path.unlink()
    with pytest.raises(RegistryError, match=STALE):
        save_registry(registry, path)
    assert not path.exists()


def test_a_symlinked_registry_is_judged_by_its_target(tmp_path):
    """wt writes through a symlinked registry, replacing the target file.
    The record is kept under the resolved path, so a load through the link
    and a write to the target are the same file to the guard."""
    target = tmp_path / "dotfiles" / "registry.toml"
    link = tmp_path / "config" / "registry.toml"
    link.parent.mkdir()
    registry = _seed(target)
    link.symlink_to(target)
    via_link = load_registry(link)

    wt_text = _as_wt_would(target)
    for stale in (registry, via_link):
        with pytest.raises(RegistryError, match=STALE):
            save_registry(stale, link)
    assert target.read_text(encoding="utf-8") == wt_text
    assert link.is_symlink()


def test_a_background_load_does_not_bless_an_older_snapshot(tmp_path):
    """The TUI's price refresh loads the registry on a worker thread while the
    screen still holds the snapshot from startup. If wt wrote in between, the
    worker's load must not make the screen's snapshot look current: the
    screen's save is still refused, and wt's row survives."""
    path = tmp_path / "registry.toml"
    screen_registry = _seed(path)
    _as_wt_would(path)
    with locked_registry(path):  # the worker: loads wt's file, saves it back
        pass

    _add_model(screen_registry, "from-modelman")
    with pytest.raises(RegistryError, match=STALE):
        save_registry(screen_registry, path)
    assert [m.id for m in load_registry(path).models] == ["ollama/from-wt"]


def test_a_registry_created_after_modelman_found_none_is_not_overwritten(tmp_path):
    """A fresh machine: modelman opens with no registry and holds an empty
    one, then `wt model init` creates the file. "No file" is a snapshot too,
    so modelman's save must not replace wt's registry with its empty one."""
    path = tmp_path / "registry.toml"
    with pytest.raises(RegistryNotFoundError):
        load_registry(path)
    path.write_text('[[providers]]\nid = "ollama"\nname = "Ollama"\n', encoding="utf-8")
    wt_text = _as_wt_would(path)

    with pytest.raises(RegistryError, match=STALE):
        save_registry(Registry(), path)
    assert path.read_text(encoding="utf-8") == wt_text


def test_a_registry_modelman_found_absent_can_still_be_created(tmp_path):
    """The other half of recording "no file": when nobody else creates it,
    modelman's first save does — the Add dialog on a fresh install."""
    path = tmp_path / "registry.toml"
    with pytest.raises(RegistryNotFoundError):
        load_registry(path)
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))]
    )
    save_registry(registry, path)
    assert load_registry(path).provider("ollama").id == "ollama"


def test_the_pre_xdg_fallback_guards_the_path_a_save_writes(tmp_path, monkeypatch):
    """With XDG_CONFIG_HOME set and the registry only under ~/.config,
    modelman reads the old file and saves to the XDG path. wt has no such
    fallback and creates the XDG file; modelman's save must not replace it."""
    home = tmp_path / "home"
    monkeypatch.setenv("HOME", str(home))
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    monkeypatch.delenv("WT_REGISTRY", raising=False)
    monkeypatch.delenv("MODELMAN_REGISTRY", raising=False)
    _seed(home / ".config" / "local-ai" / "registry.toml")
    registry = load_registry()

    canonical = tmp_path / "xdg" / "local-ai" / "registry.toml"
    canonical.parent.mkdir(parents=True)
    canonical.write_text('[[providers]]\nid = "ollama"\nname = "Ollama"\n', encoding="utf-8")
    wt_text = _as_wt_would(canonical)

    with pytest.raises(RegistryError, match=STALE):
        save_registry(registry)
    assert canonical.read_text(encoding="utf-8") == wt_text


def test_a_replacement_with_the_same_size_and_time_is_caught(tmp_path):
    """wt replaces the file by rename, so its inode changes even when the size
    and the modification time do not (a same-size edit inside one tick of a
    coarse file clock, as on CI's filesystem)."""
    path = tmp_path / "registry.toml"
    registry = _seed(path)
    st = path.stat()
    tmp = path.with_name("replacement.tmp")
    tmp.write_text(path.read_text(encoding="utf-8").replace("Ollama", "OLLAMA"), encoding="utf-8")
    os.utime(tmp, ns=(st.st_atime_ns, st.st_mtime_ns))
    os.replace(tmp, path)
    after = path.stat()
    assert (after.st_size, after.st_mtime_ns) == (st.st_size, st.st_mtime_ns)

    with pytest.raises(RegistryError, match=STALE):
        save_registry(registry, path)


def test_a_save_during_a_load_does_not_poison_the_record(tmp_path, monkeypatch):
    """The price-refresh worker loads without the lock. If the screen saves
    between the worker's open() and its stat, the worker must not record the
    file it opened (now replaced) as the current one: every later save in
    that TUI would be refused though no other program wrote anything."""
    path = tmp_path / "registry.toml"
    screen_registry = _seed(path)
    real_load = tomllib.load

    def load_while_the_screen_saves(f):
        raw = real_load(f)
        monkeypatch.setattr(registry_module.tomllib, "load", real_load)
        _add_model(screen_registry, "first")
        save_registry(screen_registry, path)
        return raw

    monkeypatch.setattr(registry_module.tomllib, "load", load_while_the_screen_saves)
    load_registry(path)  # the worker's load, with the screen's save in the middle

    _add_model(screen_registry, "second")
    save_registry(screen_registry, path)
    assert [m.id for m in load_registry(path).models] == ["ollama/first", "ollama/second"]


def test_a_refused_save_at_the_end_of_an_apply_is_reported(tmp_path):
    """The path a TUI user sees: a queued Apply ends with one save, and when
    that save is refused the run reports `save:fail` with the reload message
    instead of raising — and wt's file is left alone."""
    path = tmp_path / "registry.toml"
    registry = _seed(path)
    _add_model(registry, "queued")
    save_registry(registry, path)
    wt_text = _as_wt_would(path)

    events: list[str] = []
    pending = PendingChanges(
        registry=registry,
        state=StateStore(),
        registry_path=path,
        state_path=tmp_path / "modelman.toml",
        providers={},
        moves=[("ollama/queued", "other-family")],
    )
    pending.apply(on_event=events.append)

    assert [e for e in events if e.startswith("save:")] == ["save:start", f"save:fail|{STALE}"]
    assert path.read_text(encoding="utf-8") == wt_text
```

- [ ] **Step 2: Run them to verify they fail**

Run, from `modelman/`: `uv run pytest -q tests/test_registry_stale_snapshot.py`

Expected: 10 failed, 5 passed. The ten that fail are the ones that expect a refusal. Nine fail with `DID NOT RAISE` (`test_a_save_over_another_programs_write_is_refused`, `test_a_change_that_keeps_the_size_is_still_caught`, `test_a_reload_clears_the_refusal`, `test_a_registry_deleted_since_it_was_loaded_is_refused`, `test_a_symlinked_registry_is_judged_by_its_target`, `test_a_background_load_does_not_bless_an_older_snapshot`, `test_a_registry_created_after_modelman_found_none_is_not_overwritten`, `test_the_pre_xdg_fallback_guards_the_path_a_save_writes`, `test_a_replacement_with_the_same_size_and_time_is_caught`), and `test_a_refused_save_at_the_end_of_an_apply_is_reported` fails with `save:done` where it wants `save:fail|registry.toml changed on disk; reload`. The five that pass describe saves that must keep working.

- [ ] **Step 3: Record what was read, and check before writing**

In `modelman/src/modelman/registry.py`, give `Registry` the field the guard reads. Replace:

```python
    models: list[ModelEntry] = field(default_factory=list)

    def provider(self, provider_id: str) -> ProviderEntry:
```

with:

```python
    models: list[ModelEntry] = field(default_factory=list)
    # Set by load_registry for the stale-snapshot guard: (resolved path, how
    # many foreign changes loads had seen there). None for a Registry built in
    # memory.
    _loaded_at: tuple[str, int] | None = field(default=None, repr=False, compare=False)

    def provider(self, provider_id: str) -> ProviderEntry:
```

Replace the whole function `load_registry`:

```python
def load_registry(path: Path | None = None) -> Registry:
    registry_path = _registry_read_path(path)
    with open(registry_path, "rb") as f:
        raw = tomllib.load(f)
    _reject_unknown_top_level_keys(raw)
    registry = Registry(
        providers=[_parse_provider(p) for p in raw.get("providers", [])],
        families=[_parse_family(f) for f in raw.get("families", [])],
        models=[_parse_model(m) for m in raw.get("models", [])],
    )
    _derive_native(registry)
    return registry
```

with:

```python
# What registry.toml looked like the last time this process read or wrote it:
# resolved path -> (mtime_ns, size, inode), or None for "looked, and there was
# no file". modelman's lock is in-process only, and its TUI saves the whole
# Registry it loaded at start, so a save made after wt wrote the file would
# silently revert wt's edit. _write_registry compares the file against this
# record first and refuses when another program has changed it. Per process,
# not per Registry object, on purpose: modelman's own background save (the
# price refresh in app.py) must not make the screen's next save look stale.
_SEEN_ON_DISK: dict[str, tuple[int, int, int] | None] = {}
# How many times a load found the file changed by another program, per
# resolved path. A Registry remembers the count it was loaded at
# (Registry._loaded_at), so a snapshot older than such a load stays stale
# after modelman's own background load has recorded the new file.
_FOREIGN_CHANGES: dict[str, int] = {}
# Guards the two records: the price-refresh worker loads on its own thread.
_SEEN_LOCK = threading.Lock()


def _stamp_of(st: os.stat_result) -> tuple[int, int, int]:
    # The inode is more than the spec's mtime and size: wt replaces the file
    # by rename, so a same-size write inside one tick of a coarse file clock
    # still changes it.
    return (st.st_mtime_ns, st.st_size, st.st_ino)


def _disk_stamp(path: Path) -> tuple[int, int, int] | None:
    """(mtime_ns, size, inode) of the file at `path`, or None when there is none."""
    try:
        return _stamp_of(os.stat(path))
    except OSError:
        return None


def _note_loaded(path: Path, stamp: tuple[int, int, int] | None) -> tuple[str, int]:
    """Record what a load found at `path` (None: no file). Returns the
    (resolved path, foreign-change count) the loaded Registry carries."""
    key = os.path.realpath(path)
    with _SEEN_LOCK:
        if key in _SEEN_ON_DISK and _SEEN_ON_DISK[key] != stamp:
            _FOREIGN_CHANGES[key] = _FOREIGN_CHANGES.get(key, 0) + 1
        _SEEN_ON_DISK[key] = stamp
        return key, _FOREIGN_CHANGES.get(key, 0)


def _record_seen(path: Path) -> None:
    """Remember the file at `path` as this process just wrote it. The stat is
    by name, after the rename: another program's write landing in between is
    recorded as modelman's own. That window is one system call wide."""
    with _SEEN_LOCK:
        _SEEN_ON_DISK[os.path.realpath(path)] = _disk_stamp(path)


def _refuse_stale_snapshot(registry: Registry, path: Path) -> None:
    """Raise when the file at `path` is not the one this process last read or
    wrote, or when `registry` was loaded before a load that found another
    program's change. A path this process never looked at has no snapshot to
    be stale against, so a first save is never refused."""
    key = os.path.realpath(path)
    with _SEEN_LOCK:
        stale = key in _SEEN_ON_DISK and _disk_stamp(path) != _SEEN_ON_DISK[key]
        loaded = registry._loaded_at
        if loaded is not None and loaded[0] == key:
            stale = stale or loaded[1] != _FOREIGN_CHANGES.get(key, 0)
    if stale:
        raise RegistryError("registry.toml changed on disk; reload")


def load_registry(path: Path | None = None) -> Registry:
    write_path = Path(path) if path else _default_registry_path()
    try:
        registry_path = _registry_read_path(path)
    except RegistryNotFoundError:
        # "No registry" is a snapshot too: a file another program creates
        # before this process saves must not be overwritten.
        _note_loaded(write_path, None)
        raise
    with open(registry_path, "rb") as f:
        raw = tomllib.load(f)
        # The file that was read, stamped while it is open: a stat by name
        # afterwards could describe a file written in between.
        stamp = _stamp_of(os.fstat(f.fileno()))
    if _disk_stamp(registry_path) == stamp:
        loaded_at = _note_loaded(registry_path, stamp)
    else:
        # Replaced while it was being read (a save on another thread, or
        # another program): leave the record to whoever wrote it. Recording
        # the stamp just read would make every later save look stale. With
        # no record yet this stores "no file", so a save of what was read
        # here, which is already out of date, is refused.
        with _SEEN_LOCK:
            recorded = _SEEN_ON_DISK.get(os.path.realpath(registry_path))
        loaded_at = _note_loaded(registry_path, recorded)
    if registry_path != write_path:
        # The pre-XDG fallback: a save goes to write_path, not the file read.
        loaded_at = _note_loaded(write_path, _disk_stamp(write_path))
    _reject_unknown_top_level_keys(raw)
    registry = Registry(
        providers=[_parse_provider(p) for p in raw.get("providers", [])],
        families=[_parse_family(f) for f in raw.get("families", [])],
        models=[_parse_model(m) for m in raw.get("models", [])],
    )
    _derive_native(registry)
    registry._loaded_at = loaded_at
    return registry
```

In `_write_registry`, replace:

```python
    _refuse_dangling_symlink(path)
    if path.is_symlink():
        path = Path(os.path.realpath(path))
    payload = {
        "providers": [_provider_to_dict(p) for p in registry.providers],
        "families": [_family_to_dict(f) for f in registry.families],
        "models": [_model_to_dict(m) for m in registry.models],
    }
    atomic_write_toml(payload, path)
```

with:

```python
    _refuse_dangling_symlink(path)
    if path.is_symlink():
        path = Path(os.path.realpath(path))
    _refuse_stale_snapshot(registry, path)
    payload = {
        "providers": [_provider_to_dict(p) for p in registry.providers],
        "families": [_family_to_dict(f) for f in registry.families],
        "models": [_model_to_dict(m) for m in registry.models],
    }
    atomic_write_toml(payload, path)
    _record_seen(path)
```

and add this paragraph to the end of `_write_registry`'s docstring, before its closing quotes:

```python
    A registry another program changed since this process last read or wrote
    it is refused too (RegistryError "registry.toml changed on disk; reload"):
    wt writes this file now, and saving an older snapshot over its edit would
    revert it. A successful write records the new file as seen, so modelman's
    own saves never trip the check.
```

- [ ] **Step 4: Run the tests to verify they pass**

Run, from `modelman/`: `uv run pytest -q tests/test_registry_stale_snapshot.py`

Expected: 15 passed.

- [ ] **Step 5: Document it**

In `modelman/CLAUDE.md`, replace:

```text
- `registry.py` — `Registry` (providers + models + families);
```

with:

```text
- `registry.py` — stale-snapshot guard: `load_registry` records the file's `(mtime_ns, size, inode)` per process (or that there was no file), `_write_registry` raises `RegistryError("registry.toml changed on disk; reload")` when the file no longer matches or when the `Registry` being saved was loaded before another program's change was seen (`Registry._loaded_at`), and a successful write records the new file. wt writes the registry too; this is what stops an open TUI saving its old snapshot over a wt edit. The screens already report a refused save (`Registry not saved: …`). The TUI cannot reload a registry: after a refusal, quit and reopen modelman. A queued Apply whose final save is refused (`save:fail`) has already run its deletes and downloads, and neither `registry.toml` nor `modelman.toml` records them. `modelman migrate` and `modelman ollama-catalog sync` do not catch the refusal and end in a traceback.
- `registry.py` — `Registry` (providers + models + families);
```

- [ ] **Step 6: Verify the PR and commit**

Run, from `modelman/`: `make check && make test`

Expected: ruff and mypy clean; every test passes. No existing test loads a registry, changes the file by other means and then saves, so none needs editing; if one fails with `registry.toml changed on disk; reload`, it was relying on the lost update this guard removes — report it rather than weakening the guard.

Run, from the monorepo root: `make test-all`

Expected: exit 0.

```bash
git add modelman/src/modelman/registry.py modelman/tests/test_registry_stale_snapshot.py modelman/CLAUDE.md
git commit -m "fix(modelman): refuse to save a registry another program changed"
```

Stop here. Pushing `fix/modelman-stale-registry-snapshot` and opening the PR need the owner's OK.

---

## What Step 2 leaves for later steps

- `RegistryDoc` is the API Steps 3 and 4 build on. A new operation belongs in `registry_doc.go` with a test, not in a caller reaching into a `*tomlw.Table`.
- `validateTouched` does not check the two `Config.Validate` rules that need other rows: a `location` that resolves to `local` or `cloud` (the model's own, or its provider's), and a `provider_id` that names a provider row. A `PatchModel` that sets `location = "mars"` or `provider_id = "nope"` is written, and every wt command then reports a config error. Step 2's only caller cannot do that (it adds provider rows). Step 3 must add both checks to `registry_validate.go`, with cases in `TestUpdateRegistryValidatesOnlyTheRowsItTouched`, before `wt model add` or `wt model edit` ships.
- Step 3's `wt model add` calls `config.SeedRegistryDefaults(d, env)` inside its own `UpdateRegistry` apply, and its writing verbs call `syncRoutesAfterWrite`. It should print the `unseeded` ids the way `wt model init` does.
- The `openrouter` row seeding writes has no `secret_ref`. A LiteLLM route built for a model under such a row carries an empty `api_key` (`internal/litellm/entry.go`, `providerAPIKey`: an empty ref resolves to an empty key without an error). Step 2 adds no models, so nothing here builds that route. Step 3's `wt model add openrouter …` is where a keyless provider row should be refused or warned about.
- The hint in `wt litellm sync` and a few other commands wraps a load error as `(run `wt config` to repair)` without going through `configError` (`cmd/wt/litellm.go:564`), so on a missing registry it reads `… — seed it with `wt model init` (run `wt config` to repair)`. That wording predates this plan; the spec's Step 6 strings sweep is where it goes.
- `modelman/tests/commands/test_migrate.py:122` quotes wt's old hint in a docstring. modelman is frozen; it is deleted with modelman.
- modelman's guard makes an open TUI refuse every save after wt writes the registry, until it is restarted. That window closes in Step 3: the PR series that ships wt's Models tab also disables modelman's TUI (bare `modelman` prints where the Models tab is and exits non-zero) and leaves modelman's non-interactive commands working until Step 6 (owner decision, 2026-10-07; the spec's Step 3, "Models tab"). Step 3's plan owns that change.
- `modelman migrate` and `modelman ollama-catalog sync` report a refusal by the guard as a traceback. Accepted for the interim; it ends when modelman is removed (the spec's Step 6).
