# Provider Table (PR 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace wt's scattered per-provider Go tables with one table in a new stdlib-only package `wt/internal/provider`, and add a conformance test over it, with no behavior change.

**Architecture:** `internal/provider` holds provider and family *facts* (family membership, LiteLLM policy, seed rows, default origins and directories, tenancy, flags) plus accessors. `config`, `localmodels`, `litellm`, `lifecycle` and `cmd/wt` read it instead of their own tables; their exported functions keep their signatures so call sites do not change. Behavior-bearing code (probe branches, `backend` start/stop, name matching) stays where it is.

**Tech Stack:** Go 1.26.7 (`wt/` module `github.com/ohanaverse/local-ai-setup/wt`), standard `testing`.

**Spec:** `docs/superpowers/specs/2026-10-25-provider-interface-and-rec-design.md` (PR 1 of its Delivery section).

**Departure from the spec's PR 1 wording:** the spec says "Provider interface + single table". `config` cannot import `localmodels`, and a behavioral interface (`Start`, `Snapshot`) takes `*config.Config`, so the package that defines it cannot be imported by `config`. PR 1 therefore delivers the fact table and accessors in a leaf package; the `Local` capability interface arrives in PR 2 with the process-server engine, in `lifecycle`, reading this table.

## Global Constraints

- Behavior unchanged: every existing test passes; an existing test may change only at its call site (rename), never loosen an assertion.
- `config` cannot import `localmodels` (it imports `config`); `internal/provider` imports the standard library only (not `config`, `tomlw` or anything else in the module).
- No new registry fields; existing `registry.toml` files load unchanged; seed rows are byte-identical to today's.
- Exported function signatures in `config`, `localmodels`, `litellm`, `lifecycle` stay as they are (callers untouched).
- `localmodels.Family(id)` returns `""` for ids outside every family (llamacpp, openrouter, native agents); `config.DiscoveredModelID` passes unknown ids through unchanged. Both stay true.
- Docs follow the repo rule: show commands, never live model state.
- Commit messages end with `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.
- Work on a branch `feat/provider-table` (create it first; `main` is the default branch).

## Review Focus

1. **Unknown and native ids** (`llamacpp`, `agy`, `claude`, `""`): `Family` is `""`, `PolicyFor` absent for natives, `DiscoveredModelID("llamacpp","x") == "llamacpp/x"`. Pinned in Task 1.
2. **A custom provider id in a user's registry** (not in the table): `Provider.Origin()` returns the plain origin, no panic, no port fill-in. Pinned in Task 3.
3. **omlx + omlx-6bit together**: `Family("omlx-6bit") == "omlx"`, `FamilyProviderIDs("omlx") == [omlx omlx-6bit]` in that order, and an existing `omlx-6bit` row suppresses omlx's PATH seeding (not its model-reference or agent seeding). Pinned in Tasks 1 and 3.
4. **Results are copies**: mutating the slices and maps returned by `Families`, `FamilyProviderIDs`, `All`, `SeedRow` never changes the table. Pinned in Task 2.
5. **mtplx port rule**: an `http` loopback `base_url` without a port gets `:8003`; an explicit port, an `https` URL or a remote host is left alone. Existing test `TestProviderOriginGivesMtplxThePortWtServesItOn` must pass unchanged. Pinned in Task 3.

---

### Task 1: Pin current behavior with characterization tests

**Files:**
- Create: `wt/internal/localmodels/family_table_test.go`
- Create: `wt/internal/litellm/policy_table_test.go`
- Create: `wt/internal/config/provider_table_test.go`
- Test: the same files (all must pass on unmodified `main` before any refactor)

**Interfaces:**
- Consumes: today's `localmodels.Family/Families/FamilyProviderIDs/RunningOnly/RoutesFollowArtifact/FamilyOrigin/ModelDir`, `litellm.PolicyFor/Providers`, `config.DiscoveredModelID/SeedableProviderIDs/Provider.Origin`.
- Produces: tests that stay green through Tasks 2-6.

- [ ] **Step 1: Create the branch** — `git switch -c feat/provider-table`.
- [ ] **Step 2: Write `TestFamilyTablePinned` (localmodels)** with a table over these ids and expected `Family`: `ollama→ollama`, `omlx→omlx`, `omlx-6bit→omlx`, `mtplx→mtplx`, `mlx_lm_server→mlx_lm_server`, `llamacpp→""`, `openrouter→""`, `agy→""`, `""→""`. Also assert: `Families()` is `[mlx_lm_server mtplx ollama omlx]`; `FamilyProviderIDs("omlx")` is `[omlx omlx-6bit]`; `RunningOnly` is true only for `mlx_lm_server`; `RoutesFollowArtifact` is true only for `ollama`.
- [ ] **Step 3: Write `TestFamilyOriginDefaultsPinned` and `TestModelDirDefaultsPinned` (localmodels)** with an empty `config.Config{}`: `FamilyOrigin` gives `http://localhost:11434` (ollama), `http://localhost:8000` (omlx), `http://localhost:8003` (mtplx), `http://localhost:8001` (mlx_lm_server), `""` (unknown family). `ModelDir(cfg,"omlx")` and `ModelDir(cfg,"ollama")` equal `config.ExpandHome("~/.omlx/models")` (the legacy fallback), and `ModelDir(cfg,"mtplx")` equals `config.ExpandHome("~/.mtplx/models")`.
- [ ] **Step 4: Write `TestPoliciesPinned` and `TestProvidersPinned` (litellm)**: `PolicyFor` returns exactly today's seven entries (copy the `policies` literal from `policy.go:28-40` as the expectation, including `llamacpp` and `openrouter`) and `ok == false` for `agy`, `claude`, `""`. `Providers()` equals `{ollama:false, omlx:false, omlx-6bit:false, mlx_lm_server:false, mtplx:false, llamacpp:false, openrouter:true}`.
- [ ] **Step 5: Write `TestDiscoveredModelIDPinned`, `TestSeedableProviderIDsPinned`, `TestSeedRowsPinned` (config)**: `DiscoveredModelID` gives `omlx/x` for `omlx` and `omlx-6bit`, `mtplx/x`, `llamacpp/x`. `SeedableProviderIDs()` is `[ollama omlx mlx_lm_server mtplx openrouter]`. Seed rows: through the existing seam (`defaultProviderRow(id)` and `cloudProviderRow("openrouter")`) assert `reflect.DeepEqual` against the four local literals and the openrouter literal copied from `registry_seed.go` (mtplx row has `model_dir "~/.mtplx/models"` and `base_url "http://localhost:8003/v1"`; only ollama has `protocols ["anthropic","openai-chat"]`; openrouter has `auth.type "api_key"`, `secret_ref "OPENROUTER_API_KEY"`).
- [ ] **Step 6: Run on unmodified code** — `cd wt && go test ./internal/localmodels ./internal/litellm ./internal/config`. Expected: PASS (these pin existing behavior; a failure means the expectation was copied wrong).
- [ ] **Step 7: Commit** — `git add wt/internal/*/…_table_test.go && git commit -m "test(wt): pin provider/family table behavior before consolidating it"`.

### Task 2: The `internal/provider` package

**Files:**
- Create: `wt/internal/provider/provider.go` (types + accessors), `wt/internal/provider/table.go` (the data), `wt/internal/provider/provider_test.go`

**Interfaces:**
- Produces (exact):
  - `type Tenancy int` with `NoTenancy Tenancy = iota; Exclusive; Shared; Pool` (same order as `lifecycle/tenancy.go:9-22`; move any methods the lifecycle type has).
  - `type Policy struct { Prefix string; FixedModel bool; APIKey string; SecretRef bool; Cloud bool; V1Base bool }`
  - `type Seed struct { Name, Location string; Protocols []string; AuthType, SecretRef, BaseURL, ModelDir, Command, PathSuppressedBy string }` — `Command` is the binary whose presence on PATH triggers seeding; `PathSuppressedBy` is a provider id whose existing row suppresses only that PATH trigger.
  - `type Spec struct { ID, Family string; Policy *Policy; Seed *Seed }`
  - `type FamilySpec struct { Name, DefaultOrigin, DefaultModelDir string; WtPort int; RunningOnly, RoutesFollowArtifact, Served bool; Tenancy Tenancy }` — `DefaultOrigin` has no `/v1`; `WtPort` is the port wt hands the server (0 = server-owned); `Served` marks families whose server reports what it serves (`wt served`).
  - `const OllamaOrigin = "http://localhost:11434"`, `const MtplxPort = 8003`.
  - `func Lookup(id string) (Spec, bool)`; `func All() []Spec` (copy, table order); `func FamilyOf(id string) string` (`""` if none); `func FamilyOr(id string) string` (the id itself if none); `func Families() []string` (sorted); `func FamilyProviderIDs(family string) []string` (copy, table order); `func FamilyByName(name string) (FamilySpec, bool)`; `func PolicyFor(id string) (Policy, bool)`; `func Policies() map[string]Policy` (copy); `func SeedableLocalIDs() []string`; `func SeedableCloudIDs() []string`; `func SeedRow(id string) map[string]any` (fresh map each call, nil if no seed); `func InstalledCommands() map[string]string`; `func PathSuppressedBy(id string) string`; `func WtPortFor(providerID string) int`; `func ServedFamilies() []string` (sorted).
- Table content (from today's code): providers in this order — `ollama`(family ollama; policy `{Prefix:"ollama_chat/"}`; seed Ollama/local/protocols anthropic,openai-chat/none/`http://localhost:11434`/command ollama), `omlx`(family omlx; policy `{openai/, APIKey not-needed, V1Base}`; seed oMLX/local/none/`http://localhost:8000`/command omlx/PathSuppressedBy `omlx-6bit`), `omlx-6bit`(family omlx; same policy; no seed), `mlx_lm_server`(family mlx_lm_server; same policy; seed "mlx-lm server (target+draft)"/local/none/`http://localhost:8001/v1`, no command), `mtplx`(family mtplx; same policy; seed MTPLX/local/none/`http://localhost:8003/v1`/ModelDir `~/.mtplx/models`/command mtplx), `llamacpp`(no family; policy `{Prefix:"openai/local-model", FixedModel, APIKey:"dummy-key"}`), `openrouter`(no family; policy `{openrouter/, SecretRef, Cloud}`; seed OpenRouter/cloud/api_key/SecretRef `OPENROUTER_API_KEY`/`https://openrouter.ai/api/v1`). Families: `ollama`{origin `OllamaOrigin`, RoutesFollowArtifact, Tenancy Shared}, `omlx`{`http://localhost:8000`, dir `~/.omlx/models`, Served, Pool}, `mtplx`{`http://localhost:8003` built from `MtplxPort`, dir `~/.mtplx/models`, WtPort `MtplxPort`, Served, Exclusive}, `mlx_lm_server`{`http://localhost:8001`, RunningOnly, Served, NoTenancy — wt cannot start it, so the table says NoTenancy to match `lifecycle.TenancyOf`}.

- [ ] **Step 1: Write `TestTableInvariants`** (provider_test.go): ids unique; every `Spec.Family` names a `FamilySpec`; every `Spec` with a non-empty `Family` has a `Policy` (the omlx-6bit gap class); every family has at least one provider; every `DefaultOrigin` is non-empty for families that are not NoTenancy-only, has no trailing `/` and no `/v1` suffix; every `Seed.Location` is `local` or `cloud`; every `PathSuppressedBy` names an existing provider; `Families()` is sorted.
- [ ] **Step 2: Write `TestAccessorsMatchLegacyTables`**: `FamilyOf`/`FamilyOr`/`Families`/`FamilyProviderIDs`/`PolicyFor`/`Policies` give the Task 1 expectations (including `FamilyOf("llamacpp") == ""` and `FamilyOr("llamacpp") == "llamacpp"`); `SeedableLocalIDs()` is `[ollama omlx mlx_lm_server mtplx]`; `SeedableCloudIDs()` is `[openrouter]`; `InstalledCommands()` is `{ollama:ollama, omlx:omlx, mtplx:mtplx}`; `ServedFamilies()` is `[mlx_lm_server mtplx omlx]`; `WtPortFor("mtplx") == 8003`, `WtPortFor("omlx") == 0`, `WtPortFor("custom") == 0`.
- [ ] **Step 3: Write `TestSeedRowsMatchLegacyLiterals`**: `SeedRow` deep-equals the five literals from Task 1 (only ollama has a `protocols` key; only mtplx has `model_dir`; `secret_ref` only on openrouter); `SeedRow("omlx-6bit")` and `SeedRow("llamacpp")` are nil.
- [ ] **Step 4: Write `TestResultsAreCopies`**: mutate the slice from `Families()`, `FamilyProviderIDs("omlx")`, `All()`, the map from `Policies()`, `InstalledCommands()` and `SeedRow("mtplx")`, then assert a second call returns the original values.
- [ ] **Step 5: Run to verify failure** — `cd wt && go test ./internal/provider/...`. Expected: FAIL (package does not compile).
- [ ] **Step 6: Implement** the types, `table.go` data and accessors above. Table data lives in two package-level slices/maps never exported; accessors clone. `SeedRow` builds the map from `Seed`, including a key only when its value is non-empty (`protocols`, `model_dir`, `secret_ref`) and nesting `auth` as `map[string]any{"type":…, "base_url":…}`.
- [ ] **Step 7: Run to verify pass** — `cd wt && go test ./internal/provider/... && go vet ./internal/provider/...`. Expected: PASS.
- [ ] **Step 8: Commit** — `feat(wt): internal/provider — one table of provider and family facts`.

### Task 3: `config` reads the table

**Files:**
- Modify: `wt/internal/config/config.go` (`OllamaBaseURL`, `MtplxPort`, `Provider.Origin`, `discoveredFamily`), `wt/internal/config/registry_seed.go` (`defaultProviderIDs`, `installedProviderCommands`, `defaultProviderRow`, `cloudProviderIDs`, `cloudProviderRow`, the omlx-6bit special case)
- Test: `wt/internal/config/provider_table_test.go` (Task 1) plus existing `config_test.go`, `registry_seed_test.go`, `registry_doc_test.go`

**Interfaces:**
- Consumes: Task 2's `provider.OllamaOrigin`, `provider.MtplxPort`, `provider.WtPortFor`, `provider.FamilyOr`, `provider.SeedableLocalIDs/SeedableCloudIDs/SeedRow/InstalledCommands/PathSuppressedBy`.
- Produces: unchanged exported API (`OllamaBaseURL`, `MtplxPort`, `SeedableProviderIDs`, `DiscoveredModelID`, `Provider.Origin`).

- [ ] **Step 1: Add the failing test `TestProviderOriginCustomAndPortCases`** to `provider_table_test.go`: for a provider with id `mtplx` and `base_url`: `http://localhost/v1`→`http://localhost:8003`; `http://127.0.0.1:9000/v1`→unchanged `http://127.0.0.1:9000`; `https://localhost/v1`→unchanged; `http://example.com/v1`→unchanged; and for ids `omlx`, `omlx-6bit` and a custom id `acme` with `http://localhost/v1`→`http://localhost` (no fill-in, no panic). Run it first on the current code — it should already pass (pin) — then keep it green.
- [ ] **Step 2: Replace constants and the port rule**: `OllamaBaseURL = provider.OllamaOrigin`, `MtplxPort = provider.MtplxPort`; in `Provider.Origin` replace `p.ID != "mtplx"` with `provider.WtPortFor(p.ID) == 0` and use that port instead of `MtplxPort` (keep `servesMtplxAt` and its checks as they are).
- [ ] **Step 3: Replace `discoveredFamily`** with `provider.FamilyOr` (delete the function; `DiscoveredModelID` calls it directly).
- [ ] **Step 4: Rewire seeding**: `defaultProviderRow`/`cloudProviderRow` become calls to `provider.SeedRow`; `defaultProviderIDs`/`cloudProviderIDs` become `provider.SeedableLocalIDs()`/`SeedableCloudIDs()`; `installedProviderCommands` becomes `provider.InstalledCommands()`; the `if id == "omlx" && existing["omlx-6bit"]` check becomes `if s := provider.PathSuppressedBy(id); s != "" && existing[s]`. `SeedableProviderIDs` keeps returning local ids then cloud ids. Rename the unexported helpers' call sites in `provider_table_test.go` mechanically if they were removed; expectations stay.
- [ ] **Step 5: Run** — `cd wt && go test ./internal/config/...`. Expected: PASS, including `TestSeedAddsOnlyWhatTheMachineNeeds` (omlx-6bit suppression) and `TestProviderOriginGivesMtplxThePortWtServesItOn` unmodified.
- [ ] **Step 6: Commit** — `refactor(wt): config derives its provider tables from internal/provider`.

### Task 4: `localmodels` reads the table

**Files:**
- Modify: `wt/internal/localmodels/inventory.go` (`familyIDs`, `Family`, `Families`, `familyProviderIDs`, `FamilyProviderIDs`, `RunningOnly`, `RoutesFollowArtifact`, default origin/dir constants, `FamilyOrigin`'s default switch, `ModelDir`'s default)
- Test: `wt/internal/localmodels/family_table_test.go` (Task 1) plus the package's existing tests

**Interfaces:**
- Consumes: `provider.FamilyOf`, `Families`, `FamilyProviderIDs`, `FamilyByName`.
- Produces: unchanged exported API.

- [ ] **Step 1: Rewire the accessors**: `Family(id)` = `provider.FamilyOf(id)`; `Families()` = `provider.Families()`; `familyProviderIDs(f)` = `provider.FamilyProviderIDs(f)`; `RunningOnly(id)` = the family's `RunningOnly` flag; `RoutesFollowArtifact(f)` = the family's flag. Delete `familyIDs`.
- [ ] **Step 2: Replace defaults**: delete `defaultOmlxOrigin`, `defaultMlxLMOrigin`, `defaultOmlxDir`, `defaultMtplxDir`, `defaultMtplxOrigin`. `FamilyOrigin` takes `def` from `FamilyByName(family).DefaultOrigin` (empty for unknown); `ModelDir` takes `FamilyByName(family).DefaultModelDir` and, when empty, falls back to `FamilyByName("omlx").DefaultModelDir` (the legacy fallback pinned in Task 1).
- [ ] **Step 3: Leave alone** `matchArtifact`, `isRunning`, `loadedSize`, `probeFamily`, `ownDirectory`, `ServedIDs` and the `mlx_lm_server` ambiguity rule: they are inventory behavior, not tables (PR 2 gives them drivers).
- [ ] **Step 4: Run** — `cd wt && go test ./internal/localmodels/...`. Expected: PASS.
- [ ] **Step 5: Commit** — `refactor(wt): localmodels derives families and defaults from internal/provider`.

### Task 5: `litellm` reads the table

**Files:**
- Modify: `wt/internal/litellm/policy.go` (the `policies` map and `Policy`), `wt/internal/litellm/service.go:991-999` (`Providers`)
- Test: `wt/internal/litellm/policy_table_test.go` (Task 1) plus existing litellm tests

**Interfaces:**
- Consumes: `provider.Policy`, `provider.PolicyFor`, `provider.Policies`.
- Produces: `type Policy = provider.Policy` (alias, so `entry.go` and `service.go` field reads compile unchanged); `PolicyFor(id)` and `Providers()` unchanged in behavior.

- [ ] **Step 1: Alias and delegate**: replace the struct with `type Policy = provider.Policy`, keep the field docs as the doc comment on the alias; `PolicyFor` returns `provider.PolicyFor(id)`; delete the `policies` map; `Providers()` builds `id → Cloud` from `provider.Policies()`.
- [ ] **Step 2: Move the omlx-6bit explanation** (the comment currently above its `policies` entry) onto that provider's row in `provider/table.go`.
- [ ] **Step 3: Run** — `cd wt && go test ./internal/litellm/...`. Expected: PASS (`wt litellm providers` output unchanged — `TestProvidersPinned`).
- [ ] **Step 4: Commit** — `refactor(wt): litellm policy comes from internal/provider`.

### Task 6: `lifecycle`, `cmd/wt` and `modeladmin`, plus the conformance test

**Files:**
- Modify: `wt/internal/lifecycle/tenancy.go` (alias), `wt/cmd/wt/model_cmds.go:747` (`servedFamilies`), `wt/internal/modeladmin/apply.go:359` (error text)
- Create: `wt/internal/lifecycle/provider_conformance_test.go`
- Test: existing lifecycle, cmd/wt, modeladmin tests

**Interfaces:**
- Consumes: `provider.Tenancy` and constants, `provider.ServedFamilies`, `provider.SeedableLocalIDs/SeedableCloudIDs`, `provider.FamilyByName`.
- Produces: `type Tenancy = provider.Tenancy` with `NoTenancy, Exclusive, Shared, Pool` re-exported as the same constants.

- [ ] **Step 1: Write `TestBackendsMatchProviderTable`** (lifecycle): for every family in `provider.Families()`: if its `Tenancy != NoTenancy` then `backendsByFamily[family]` exists and `backend.tenancy()` equals it, and `Startable(<each id of the family>)` is true; if `NoTenancy` then `backendsByFamily` has no entry and `Startable` is false. Every key of `backendsByFamily` is a family in the table. Run first: expected FAIL to compile until the alias exists.
- [ ] **Step 2: Alias the type**: in `tenancy.go` replace the enum with `type Tenancy = provider.Tenancy` and `const (NoTenancy = provider.NoTenancy; Exclusive = provider.Exclusive; Shared = provider.Shared; Pool = provider.Pool)`; keep `TenancyOf` as is (it still reads `backendsByFamily`; changing that is PR 2).
- [ ] **Step 3: Derive `servedFamilies`** in `model_cmds.go` from `provider.ServedFamilies()`; the error text `unknown provider %q: wt served answers for mlx_lm_server, mtplx, omlx` is pinned by `model_cmds_test.go` and must not change.
- [ ] **Step 4: Derive the seedable-providers sentence** at `apply.go:359` from `provider.SeedableLocalIDs()` and `SeedableCloudIDs()` joined with commas and "and" (the order becomes ollama, omlx, mlx_lm_server, mtplx and openrouter, which differs from the old hand-typed order; no test pins it).
- [ ] **Step 5: Run** — `cd wt && go vet ./... && go test ./...`. Expected: PASS.
- [ ] **Step 6: Commit** — `refactor(wt): lifecycle tenancy and command lists come from internal/provider; add conformance test`.

### Task 7: Docs, skill and final verification

**Files:**
- Modify: `wt/docs/internals/packages.md` (new `provider` row and the layering sentence), `wt/.claude/skills/adding-a-provider/SKILL.md` (steps now: add a row to `internal/provider/table.go`, then the backend), `wt/CLAUDE.md` if it lists packages
- Check: `docs/internals/*` and `docs/guides/*` mentions of "policies map", "familyIDs", "backendsByFamily" as editing instructions

- [ ] **Step 1: Update the three docs** above; any "edit the LiteLLM policies map / familyIDs / defaultProviderRow" instruction becomes "add a row to `wt/internal/provider/table.go`". Guides show no live model state.
- [ ] **Step 2: Verify no duplicates remain** — `grep -rn "familyIDs\|installedProviderCommands\|defaultProviderIDs\|cloudProviderIDs\|var policies\|discoveredFamily\|defaultOmlxOrigin\|defaultMlxLMOrigin\|defaultMtplxOrigin" wt/` returns nothing outside comments and tests.
- [ ] **Step 3: Full verification from the repo root** — `make lint` then `make test-all`. Expected: both exit 0.
- [ ] **Step 4: Confirm behavior is unchanged by hand** — run `wt litellm providers` and `wt model list --json`; compare to the output from `main` (build `main` in a scratch worktree first); expected: identical.
- [ ] **Step 5: Commit** — `docs(wt): document internal/provider and update the adding-a-provider skill`.
- [ ] **Step 6: Push and open the PR only when the user asks.**

---

## Self-review

- **Spec coverage:** PR 1's scope (single table; policies, familyIDs, discoveredFamily, seed tables, default origins derived; behavior unchanged; conformance test) → Tasks 2-6; the `Provider` interface itself is deferred to PR 2 and flagged above.
- **Type consistency:** `Spec`, `FamilySpec`, `Seed`, `Policy`, `Tenancy` and the accessors in Task 2's Interfaces are the only names later tasks use; `Policy` is aliased in Task 5, `Tenancy` in Task 6.
- **Proportion:** the plan has no function bodies; each step names signatures, values and the test that pins them.
