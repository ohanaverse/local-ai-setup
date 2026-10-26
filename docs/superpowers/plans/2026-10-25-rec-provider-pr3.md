# rec Provider (PR 3) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `wt start rec/flint` brings rec up, `wt stop rec` takes it down, and flint is routed through LiteLLM while it runs: rec as a built-in provider on the process-server engine, with an inventory probe, a seeded registry row, an updater, and docs.

**Architecture:** rec is one new family row plus one provider row in `internal/provider/table.go` (a `Process` spec, `ScopeMachine`, a LiteLLM policy that takes its key from `secret_ref`, a seed row) and a "served-only" inventory probe in `localmodels` (HTTP `GET /v1/models` with the key; artifacts are never listed, but the model is startable). Three of PR 2's known limits close here: an adopted instance must be on the registry's address, a rec on the registry port is a resident when another family starts, and a replaced instance's route uses the registry id.

**Tech Stack:** Go 1.26.7 (`wt/`), bash (`bin/update/rec`), standard `testing`/`httptest`.

**Spec:** `docs/superpowers/specs/2026-10-25-provider-interface-and-rec-design.md` (PR 3 of its Delivery section; the rec specifics and the foreign-instance decision).

**Base:** stacked on `feat/process-server` (PR #455). Create `feat/rec-provider` from it, or from `main` once #455 merges.

**Out of this PR:** the registry overlay (`[providers.process]`), `llmbench` reading the provider description and its rec isolation backend (PR 4), a `modeltiers` alias for flint (added only with public-source evidence; until then flint is listed `unrated`), and verifying that LiteLLM passes rec's `reasoning` field through (stated as unverified in the docs).

## Global Constraints

- rec facts (verified live, rec 0.1.0): `rec serve [--port N (default 11500)] [--context TOKENS]` runs in the foreground and wt detaches it; `rec stop` stops the managed instance whichever port; `rec status` prints one JSON line (`"ready":true`, `"model":"flint"`, `"endpoint":"http://127.0.0.1:11500/v1"`), exit 0 even when `ready:false`, exit 1 with `rec: no managed rec instance is reachable` when nothing runs; one instance per machine even across ports; the port is closed ~10–12 s while loading and `/health` answers 200 before readiness; every route needs `Authorization: Bearer <key>` (`rec auth token` prints it); the served model id is exactly `flint` (any other `model` is a 400); OpenAI chat completions only (no `/v1/messages`); thinking comes back as `reasoning`, not `reasoning_content`; data under `~/.local/share/rec`; `rec update [-y]`, `rec --version`.
- Registry values (exact): provider id `rec`, display name `rec`, `auth.type = "none"`, `auth.base_url = "http://127.0.0.1:11500/v1"`, `auth.secret_ref = "exec:rec auth token"`, no `protocols` key, no `model_dir`; model `rec/flint` with `model_name = "flint"`.
- Process spec (exact): `Binary "rec"`, `Spawn ["serve","--port","{port}"]`, `Stop ["stop"]`, `PidfileDefault "/tmp/local-ai-setup-rec.pid"`, `LogDefault "/tmp/local-ai-setup-rec.log"`, `PidfileEnv "WT_REC_PIDFILE"`, `Subcommand "serve"`, `PortFlag "--port"`, no `DaemonModule`, no `ModelFlag`, `Ready{Kind: ReadyCommandJSON, Command: ["status"], Path: "ready", Equals: "true", ModelPath: "model", EndpointPath: "endpoint"}`. Family: `Tenancy Exclusive`, `Scope ScopeMachine`, `WtPort 11500`, `DefaultOrigin "http://127.0.0.1:11500"`, `Served true`, `ServedOnly true`, `RunningOnly false`.
- LiteLLM policy (exact): `{Prefix: "openai/", SecretRef: true, V1Base: true}` (the key is resolved from the row's `secret_ref`).
- Behavior of every other provider is unchanged: mlx_lm_server stays running-only and unstartable; ollama, omlx, mtplx probes and routes are untouched; a provider with no `secret_ref` sends no `Authorization` header.
- `internal/provider` stays standard-library only. Every `Test*` carries a top-level `//` comment saying what and why (`wt/CLAUDE.md`). Commit messages end with `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.
- Tests never dial a real server or port: use `httptest`, fake `run`/`lookPath`/spawn seams and registry `base_url`s pointing at them (never 11500 or 8003); only the manual live check in Task 6 starts the real rec.
- Guides show commands, never live model state.

## Review Focus

1. **A key that cannot be read** (`rec auth token` fails): `wt start rec/flint` fails at once with the key error, spawns nothing; the family shows `partial`, never `down`. Pinned in Task 4.
2. **rec not on PATH**: `wt start rec/flint` is a `*BinaryMissingError{Binary: "rec"}`; `wt model init` seeds no rec row. Pinned in Tasks 1 and 4.
3. **A stopped registered flint is a start row** (not blocked, not hidden), a running one is a launch row, and mlx_lm_server rows behave exactly as before. Pinned in Task 2.
4. **An instance on the wrong address is not adopted**: a rec already serving flint on a port other than the registry's is asked about (and replaced), because wt would route to the registry's address. Pinned in Task 3.
5. **Another family's start sees a running rec**: starting an mtplx or omlx model while rec serves on the registry port lists rec among the others to keep or stop. Pinned in Task 2.
6. **The seeded row is byte-exact** and a registry written by an older wt (no rec row) keeps loading. Pinned in Task 1.

---

### Task 1: rec in the provider table

**Files:**
- Modify: `wt/internal/provider/provider.go` (`FamilySpec.ServedOnly`, `Ready.EndpointPath`), `wt/internal/provider/table.go` (rec rows; `mlx_lm_server` gets `ServedOnly: true`), `wt/internal/provider/provider_test.go`
- Modify (expectations only, new rec entries): `wt/internal/localmodels/family_table_test.go`, `wt/internal/litellm/policy_table_test.go`, `wt/internal/config/provider_table_test.go`, `wt/internal/lifecycle/provider_conformance_test.go` and any other existing test that enumerates families or providers and fails (`go test ./...` names them)

**Interfaces:**
- Produces (exact): `FamilySpec.ServedOnly bool` (the family's probe is `GET /v1/models` only; wt lists no artifacts); `Ready.EndpointPath string` (JSON path of the instance's endpoint URL); the rec rows in Global Constraints; `ProcessSpec` invariants relaxed so `Spawn` must contain `{port}` when the family has a `WtPort` but `{model}` is optional.
- Order: providers `… mtplx, rec, llamacpp, openrouter`; `Families()` becomes `[mlx_lm_server mtplx ollama omlx rec]`; `SeedableLocalIDs()` becomes `[ollama omlx mlx_lm_server mtplx rec]`; `InstalledCommands()` gains `rec: rec`; `ServedFamilies()` becomes `[mlx_lm_server mtplx omlx rec]`; `ProcessFamilies()` becomes `[mtplx rec]`; `Policies()` gains `rec`.

- [ ] **Step 1: Write the failing tests.** In `provider_test.go`: `TestRecRows` asserts every value in Global Constraints (the `ProcessSpec`, the family, the policy, the `Seed` with `Name "rec"`, `Location "local"`, `AuthType "none"`, `BaseURL "http://127.0.0.1:11500/v1"`, `SecretRef "exec:rec auth token"`, `Command "rec"`, no `Protocols`, no `ModelDir`); `TestSeedRowRec` asserts `SeedRow("rec")` deep-equals `{"id":"rec","name":"rec","location":"local","auth":{"type":"none","base_url":"http://127.0.0.1:11500/v1","secret_ref":"exec:rec auth token"}}`; `TestServedOnlyFlags` asserts `ServedOnly` is true for exactly `mlx_lm_server` and `rec` and `RunningOnly` for exactly `mlx_lm_server`; extend `TestTableInvariants` (`{model}` optional, `{port}` required for a family with `WtPort`), `TestAccessorsMatchLegacyTables` and `TestResultsAreCopies` (mutate `ProcessFor("rec").Ready.Command[0]`) with the new rec values above.
- [ ] **Step 2: Run to verify failure** — `cd wt && go test ./internal/provider/...`. Expected: FAIL.
- [ ] **Step 3: Implement** the two fields and the rec rows (a `Seed` for the seeded row; `recOrigin = "http://127.0.0.1:11500"` and `recPort = 11500` named constants used by both the family and the seed URL so they cannot drift). Update the existing expectations listed under Files; do not loosen any assertion, only add rec.
- [ ] **Step 4: Run** — `cd wt && go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** — `feat(wt): rec in the provider table`.

### Task 2: served-only inventory for rec

**Files:**
- Modify: `wt/internal/localmodels/inventory.go` (`probeFamily` served-only branch, `source.isRunning`), `wt/internal/localmodels/inventory_test.go`, `wt/internal/catalog/catalog_test.go`, `wt/internal/lifecycle/evictions_test.go`

**Interfaces:**
- Consumes: Task 1 `FamilySpec.ServedOnly`; `ServedIDs` (keyed); `NameMatches`.
- Produces: the `mlx_lm_server` branch of `probeFamily` becomes the branch for every `ServedOnly` family (selected through `provider.FamilyByName(family).ServedOnly`, not by name); `source.isRunning` for a `ServedOnly` family that is not `RunningOnly` returns `NameMatches(loaded, name)` for any loaded id (rec serves one model); mlx_lm_server's `isRunning` rule (including the single-registered-model heuristic) and its ambiguity rule are untouched.

- [ ] **Step 1: Write the failing tests** (`httptest` servers; a registry whose `rec` provider `base_url` is the server's URL and whose `secret_ref` is a literal key): `TestRecInventoryRunning` (server requires `Bearer k`, lists `flint`: the registered `rec/flint` entry is `Running`, family `StatusOK`); `TestRecInventoryStoppedIsAStartRow` (listener closed: the entry is not running, `ArtifactKnown` false, family `Down`; in `catalog_test.go` the built row has `Action() == ActionStart` and `Status == StatusUnknown`, not blocked, not hidden); `TestRecInventoryRefusedKeyIsPartial` (server answers 401: family `StatusPartial`, not `Down`, `ProbeFailures["rec"]` wraps `ErrKeyRefused`); `TestRecInventoryUnreadableKeyIsPartial` (`secret_ref = "exec:/nonexistent/key"`); `TestMlxLmServerStillRunningOnly` (a stopped mlx_lm_server pairing is not listed and `Action()` is block, exactly as before). In `evictions_test.go`, `TestRunningRecIsAResidentWhenAnotherFamilyStarts`: a snapshot with a running `rec/flint` entry makes `residentOthers` for an mtplx target list it, `Stoppable` true (rec is `CanStop`).
- [ ] **Step 2: Run to verify failure** — `cd wt && go test ./internal/localmodels ./internal/catalog ./internal/lifecycle -run 'Rec|StillRunningOnly'`. Expected: FAIL.
- [ ] **Step 3: Implement** the branch generalisation and the `isRunning` case.
- [ ] **Step 4: Run** — `cd wt && go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** — `feat(wt): served-only inventory probe; rec rows are startable`.

### Task 3: adoption only at the registry's address; the occupant's route

**Files:**
- Modify: `wt/internal/provider/provider.go` (nothing new beyond Task 1), `wt/internal/lifecycle/ready.go` (`readyState.Endpoint`), `wt/internal/lifecycle/lifecycle.go` and `evictions.go` (`machineOccupant`, the occupant entry), `wt/internal/lifecycle/machine_scope_test.go`

**Interfaces:**
- Produces (exact): `readyState` gains `Endpoint string` (filled from `spec.Ready.EndpointPath`, `""` when the path is empty or absent); `foreignInstance` additionally reports the endpoint; `func serverEntry(snap localmodels.Snapshot, family, model string, loading bool) localmodels.Entry` generalises `loadingServerEntry` (the snapshot's own entry for the model when `SameModel`, else a discovered entry, `Running: true`, `Loading: loading`); `loadingServerEntry` becomes a call to it with `loading == true`.
- Behavior: an instance serving the requested model is adopted only when `config.BaseOrigin(st.Endpoint)` equals `localmodels.FamilyOrigin(cfg, family)` (an empty `Endpoint` counts as equal: a spec with no `EndpointPath` keeps PR 2's behavior); otherwise it is a victim exactly like an instance serving another model (ask first, stop with the stop command, then spawn on the registry's port). The victim's `Entry` comes from `serverEntry(snap, family, st.Model, false)`, so the route dropped after a replace is the registry id's.

- [ ] **Step 1: Write the failing tests** in `machine_scope_test.go` using the synthetic machine-scope family from PR 2: `TestAdoptRequiresTheRegistryAddress` (status reports the requested model with `endpoint` on another port: `start` without `AllowReplace` returns `*OccupiedError`, nothing stopped; with `AllowReplace` the sequence is status, stop, status, spawn); `TestAdoptWhenTheEndpointMatches` (same origin: adopted, no stop, no spawn); `TestAdoptWithNoEndpointPath` (spec without `EndpointPath`: adopted as in PR 2); `TestMachineOccupantUsesTheRegistryID` (a snapshot with the registered entry for the same model: the `OccupiedError` occupant's `ModelID` is the registry id, not the discovered one; with no registry entry it is the discovered id).
- [ ] **Step 2: Run to verify failure** — `cd wt && go test ./internal/lifecycle -run 'TestAdopt|TestMachineOccupant'`. Expected: FAIL.
- [ ] **Step 3: Implement** the field, the origin comparison and `serverEntry`.
- [ ] **Step 4: Run** — `cd wt && go test ./...` (the PR 2 tests `TestMachineScope*`, `TestPortScope*` unchanged). Expected: PASS.
- [ ] **Step 5: Commit** — `feat(wt): adopt a foreign instance only at the registry's address`.

### Task 4: rec lifecycle end to end (fake binary, fake server)

**Files:**
- Create: `wt/internal/lifecycle/rec_test.go`
- Modify: `wt/internal/lifecycle/server.go` only if a test shows a defect (report it; do not widen scope)

**Interfaces:**
- Consumes: Tasks 1–3 and the PR 2 seams (`processSpecFor`, `familyOriginPort`, `env.spawn`, `env.run`, `env.lookPath`, `readyCmdTimeout`); the real `rec` rows from the table (no synthetic spec).

- [ ] **Step 1: Write the failing tests** with a registry whose `rec` provider has `base_url` pointing at an `httptest` server that requires `Bearer k` on `/v1/models` and `/v1/chat/completions` (and answers 401 otherwise), `secret_ref = "k"`, fake `lookPath`/`run`/`spawn`: `TestRecStartSpawnsWaitsWarmsWithKey` (the spawn argv is `serve --port <port of the httptest server>`; `rec status` stub reports `{"ready":false}` twice then `{"ready":true,"model":"flint","endpoint":"<server url>/v1"}`; the warmup request carries `Bearer k` and model `flint`; stages are Starting, Waiting, Warming in order); `TestRecStartFailsFastWithoutTheBinary` (`lookPath` fails: `*BinaryMissingError{Binary: "rec"}`, no spawn); `TestRecStartFailsFastWhenTheKeyCannotBeRead` (`secret_ref = "exec:/nonexistent/k"`: `*KeyUnreadableError`, no spawn, under 1 s); `TestRecStopRunsRecStop` (the stop command is `rec stop`, then `rec status` reports not ready; no signal sent); `TestRecStartWhileAnotherRecServesOtherModelAsksFirst` (status reports `other`: `*OccupiedError`; with `AllowReplace`, stop then spawn); `TestRecProcessIdentity` (a pidfile naming a pid whose stubbed argv is `["/Users/x/.local/bin/rec","serve","--port","<port>"]` reads as `Loading{PID}`; the supervisor's argv `rec _supervise …` and `["vim","rec","serve","--port","<port>"]` are strays).
- [ ] **Step 2: Run to verify failure** — `cd wt && go test ./internal/lifecycle -run 'TestRec'`. Expected: FAIL where the rows or behavior are missing (a test that passes on first run is fine if it proves a guarantee the code already has; say so in the report).
- [ ] **Step 3: Implement** only what a failing test shows is missing.
- [ ] **Step 4: Run** — `cd wt && go test ./... && go test -race ./internal/lifecycle/...`. Expected: PASS.
- [ ] **Step 5: Commit** — `test(wt): rec lifecycle end to end`.

### Task 5: bin/update/rec

**Files:**
- Create: `bin/update/rec`
- Modify: `bin/update-tools` (the `TOOLS` array and its help text), `Makefile` only if `SHELL_SCRIPTS` lists updaters by name

**Interfaces:**
- Follows `bin/lib/update-common.sh`'s contract (`update_init`, `update_note`, `update_finish`, `finish_against_published`, `require_cmd`, `run`, `dry_run_stop`) exactly as `bin/update/mtplx` and `bin/update/ollama` do; prints `[update:rec] <status> — <detail>`; exit 0 ok, 1 failed, 3 needs a manual step.

- [ ] **Step 1: Write the script**: `require_cmd rec`; installed = `rec --version` (`rec 0.1.0` → `0.1.0`, `unknown` when unreadable); latest = the `tag_name` of `https://api.github.com/repos/recursive-oss/rec/releases/latest` (`curl -fsSL -m 8`, leading `v` stripped, `--no-network` skips it); if `rec status` reports `"ready":true` (a server is running) the status is `manual` with "stop rec first (`wt stop rec`), then rerun"; otherwise the plan is `rec update -y` run through `run` (so `--dry-run`/`--check` change nothing: `--check` reports `current` or `manual … available`), and after a real update re-read `rec --version` for the trailer (`upgraded`/`current` per the contract's statuses). Add `rec` to `TOOLS` after `mtplx` and a help line `rec  the rec/flint local model engine (self-updating binary)`.
- [ ] **Step 2: Verify** — `make -C . lint-shell` (or `make lint` from the repo root; expected exit 0); `bin/update/rec --check --no-network` prints a `[update:rec]` trailer and exits 0 or 3 without changing anything; `bin/update/rec --dry-run --no-network` changes nothing; `bin/update-tools --check --only rec --dry-run` lists rec.
- [ ] **Step 3: Commit** — `feat: bin/update/rec`.

### Task 6: docs, skill and final verification

**Files:**
- Modify: `docs/guides/02-providers-and-models.md` (a rec section: install, `wt model init`, `wt model add rec flint`, supported-providers note), `docs/guides/06-wt-agents-and-models.md` (agents must list `rec` in `supported_providers` to use it), `docs/guides/08-maintenance-and-troubleshooting.md` (nine updaters, rec's updater, the key and `rec status` checks), `docs/reference/provider-artifacts.md` (rec artifacts: `~/.local/share/rec`, never deleted by wt, `rec uninstall`), `wt/docs/internals/local-models.md` (rec specifics, served-only probe, the closed known limits, the remaining ones), `wt/docs/internals/packages.md` if files changed, `CLAUDE.md` (root: update-tools list; wt: lifecycle bullets mention rec), `wt/.claude/skills/adding-a-provider/SKILL.md` (served-only families)

- [ ] **Step 1: Update the docs** above. The known limits that remain after this PR: a foreign rec on a port the registry does not name is not listed as a resident when another family starts; LiteLLM's handling of rec's `reasoning` field is unverified (rec returns `reasoning`, not `reasoning_content`); `wt` cannot start rec before `rec auth token` works (a fresh install without a key); flint is `unrated` in `modeltiers` until public-source evidence exists. No live state in guides.
- [ ] **Step 2: Verify** — `grep -rn "Eight tools\|eight tools" docs wt/docs` returns nothing; `make -C .. lint` then `make -C .. test-all` exit 0.
- [ ] **Step 3: Compare behavior** — build the base branch and this branch into scratch and run both fully redirected (`wt/CLAUDE.md`: every path redirected, scratch registry): `wt litellm providers` differs only by the new `rec cloud=false` line; `wt model list --json`, `wt served mtplx`, `wt stop bogus` unchanged; `wt model init` on a scratch registry with a fake `rec` on `PATH` seeds the byte-exact rec row and, with no `rec` on `PATH`, no rec row.
- [ ] **Step 4: Live check with the real rec** (the user allowed live starts and stops of rec). Redirect every wt path to scratch except rec's own data; first run `rec status` and, if an instance is already running, `wt` must adopt it only if it serves flint on `127.0.0.1:11500`. Then `wt start rec/flint`, `curl` with the key a chat completion to `flint` and a streamed one, `wt served rec`, `wt model list`, then `wt stop rec` and confirm `rec status` exits 1 and nothing listens on 11500. Record in the report whether `rec auth token` works with the key file present, how long the start took, and the chat's `reasoning` field. Leave rec stopped. If anything differs from the facts above, report it with evidence; do not change Go source in this task.
- [ ] **Step 5: Commit** — `docs: rec provider`.
- [ ] **Step 6: Push and open the PR only when the user asks.**

---

## Self-review

- **Spec coverage:** PR 3's list (rec built-in row with defaults and seeding, LiteLLM route, picker/list integration, `bin/update/rec`, docs) → Tasks 1, 2, 5, 6; the foreign-instance decision's address condition and the PR 2 limits it closes → Tasks 2 and 3; the live check the design asked for → Task 6.
- **Type consistency:** `FamilySpec.ServedOnly` and `Ready.EndpointPath` (Task 1) are used in Tasks 2–4; `readyState.Endpoint` and `serverEntry` (Task 3) are used in Task 4's tests only through behavior.
- **Proportion:** rows, signatures and tests only; the lifecycle engine, inventory and updater contract are existing code the plan points at.
