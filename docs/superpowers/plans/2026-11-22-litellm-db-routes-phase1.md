# LiteLLM database routes (phase 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A local model start or stop adds or removes its LiteLLM route through LiteLLM's model API, with no config rewrite and no proxy restart (#598).

**Architecture:** `internal/litellm` gains an admin-API client and a pure reconciler. Local (start/stop-driven) rows move out of `config.yaml` into LiteLLM's database, each with the deterministic `model_info.id` `wt:<registry id>`; cloud rows and every setting stay in `config.yaml`. When the proxy has no database models enabled or the API is unreachable, every write falls back to today's file-plus-restart path, unchanged.

**Tech Stack:** Go 1.26.7 (`wt/` module), `net/http`, `httptest`, `gopkg.in/yaml.v3` (already used), LiteLLM 1.109.1 `/model/new`, `/model/update`-by-id, `/model/delete`, `/model/info`.

**Spec:** `docs/superpowers/specs/2026-11-22-litellm-aliases-groups-design.md` (Approach A, phase 1; evidence in `2026-11-22-litellm-aliases-groups-spike/`).

## Global Constraints

- Rows written through the API carry `model_info.wt_managed: true` and `model_info.id` equal to `wt:` plus the registry id (the spec's "deterministic `model_info.id`"). wt touches no database row without that marker.
- Rows in `config.yaml` cannot be edited through the API; cloud rows therefore stay in the file and keep today's restart path.
- The proxy's key comes only from the `~/.config/wt-model/` home (`wt-model litellm set`); never logged, never in an error string, never in a test fixture other than the literal `sk-test-master`.
- Fallback is silent-correct, not silent-wrong: when the API path is unavailable the file path runs and a warning says why.
- `store_model_in_db: true` is written only when `general_settings` lacks the key (an explicit `false` is the opt-out, as with `cancel_on_disconnect`).
- Tests stay off the machine (`wt/CLAUDE.md` "Go tests"): the admin client is exercised against `httptest.Server`; no test dials port 4000; per-call seams, no new package vars.
- Docs that quote changed behavior move in the same change: `wt/docs/internals/litellm-routes.md`, `wt/docs/wt-model-server.md`, `docs/guides/14-litellm-install-and-configuration.md`; `docs/guides/` embeds no live model state.
- Commit trailer: `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`; PR body uses `Refs #598`, never a closing keyword.

## Review Focus

1. The proxy restarts or loses its DB between add and remove: a stop for a route that is already gone must be a no-op, not an error.
2. A model whose id is also an unmarked hand-written `config.yaml` row: wt must not add a DB twin (it would create a silent two-deployment group).
3. First sync after upgrade: managed local rows still in `config.yaml` must move to the database without ever leaving a window where the model is unrouted.
4. Two wt-model processes (serve job and CLI `sync`) reconcile at once: the same id added twice must converge, not duplicate.
5. LiteLLM answers 200 but the row is missing from `/model/info` afterwards (database off): treat as unavailable and fall back, never report success.

---

### Task 1: Admin API client

**Files:**
- Create: `wt/internal/litellm/adminapi.go`
- Test: `wt/internal/litellm/adminapi_test.go`

**Interfaces:**
- Produces:
  - `type DBModel struct { ID, ModelName string; Params map[string]any; Info map[string]any }`
  - `type AdminClient struct { BaseURL, Key string; HTTP *http.Client }` (nil `HTTP` means a client with a 10 s timeout)
  - `var ErrDBModelsOff = errors.New("litellm: database-stored models are off (general_settings.store_model_in_db)")`
  - `func (c *AdminClient) List(ctx context.Context) ([]DBModel, error)` — `GET /model/info`, returns only rows whose `model_info.db_model` is true
  - `func (c *AdminClient) Add(ctx context.Context, m DBModel) error` — `POST /model/new`
  - `func (c *AdminClient) Delete(ctx context.Context, id string) error` — `POST /model/delete`; a 404 or "not found" body is nil
  - Authentication is `Authorization: Bearer <Key>` on every call.

- [ ] **Step 1: Write failing tests** against an `httptest.Server`: `TestAddSendsModelNameParamsAndInfoID` (request body `model_name`, `litellm_params`, `model_info.id == "wt:ollama/x"`, `Authorization: Bearer sk-test-master`); `TestAddMapsStoreOffToErrDBModelsOff` (server answers 500 with a body containing `store_model_in_db`; assert `errors.Is(err, ErrDBModelsOff)`); `TestDeleteMissingIsNil` (404); `TestListKeepsOnlyDatabaseRows`; `TestErrorsNeverContainTheKey` (every error string from a 401 run lacks `sk-test-master`).
- [ ] **Step 2: Run** `cd wt && go test ./internal/litellm -run 'TestAdd|TestDelete|TestList|TestErrors' -v`. Expected: FAIL, undefined `AdminClient`.
- [ ] **Step 3: Implement** the type and three methods in `adminapi.go`; decode `/model/info`'s `data[]` (`model_name`, `litellm_params`, `model_info`); build errors from status and a truncated body with the key redacted.
- [ ] **Step 4: Run** the same command. Expected: PASS.
- [ ] **Step 5: Commit** `feat(litellm): admin API client for database-stored models (#598)`.

### Task 2: Row to payload conversion

**Files:**
- Modify: `wt/internal/litellm/adminapi.go` (or create `wt/internal/litellm/dbrow.go`)
- Test: `wt/internal/litellm/dbrow_test.go`

**Interfaces:**
- Consumes: `BuildEntry(m config.Model, p config.Provider) (*yaml.Node, error)` and `IsManaged`, existing.
- Produces: `func DBModelFromRow(row *yaml.Node) (DBModel, error)` — copies `model_name`, `litellm_params`, merges the row's `model_info` and sets `wt_managed: true` and `id: "wt:"+model_name`; `func DBID(registryID string) string` returns `"wt:"+registryID`.

- [ ] **Step 1: Write failing tests**: `TestDBModelFromRowOllama` (build a row with `BuildEntry` for an ollama local model, assert `Params["model"]`, `Params["api_base"]`, `Info["wt_managed"] == true`, `Info["id"] == "wt:"+id`); `TestDBModelFromRowKeepsOsEnvReferences` (an `os.environ/X` api_key stays that literal string, matching the rule that wt never resolves it into the file); `TestDBIDIsStable`.
- [ ] **Step 2: Run** `go test ./internal/litellm -run 'TestDBModelFromRow|TestDBID' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** `DBModelFromRow` by decoding the yaml node to `map[string]any`; no field is dropped.
- [ ] **Step 4: Run** again. Expected: PASS.
- [ ] **Step 5: Commit** `feat(litellm): build a database model from a managed row (#598)`.

### Task 3: Pure reconciler

**Files:**
- Create: `wt/internal/litellm/dbreconcile.go`
- Test: `wt/internal/litellm/dbreconcile_test.go`

**Interfaces:**
- Consumes: `DBModel`, `AdminClient`.
- Produces:
  - `type DBPlan struct { Add, Update []DBModel; Delete []string }`
  - `func PlanDB(desired, current []DBModel) DBPlan` — pure; matches by `Info["id"]`; considers only `current` rows with `wt_managed`; equal rows (compare `Params` and `ModelName`) produce nothing; the output is sorted by id.
  - `func (c *AdminClient) ApplyDB(ctx context.Context, p DBPlan) (added, updated, deleted int, err error)` — an Update is Delete then Add (LiteLLM's update endpoint takes the row id and is not idempotent for params); an Add that finds the id already present counts as converged, not an error.

- [ ] **Step 1: Write failing tests**: `TestPlanDBAddsMissing`, `TestPlanDBDeletesManagedStrays`, `TestPlanDBIgnoresUnmanagedDatabaseRows`, `TestPlanDBUpdatesChangedParams`, `TestPlanDBIsEmptyWhenEqual`, and (Review Focus 4) `TestApplyDBTwoClientsConverge` (two goroutines apply the same plan to one fake server that rejects duplicate ids with the real "already exists" shape; both return nil and the server holds one row). Review Focus 1: `TestApplyDBDeleteOfAbsentRowIsNoop`.
- [ ] **Step 2: Run** `go test ./internal/litellm -run 'TestPlanDB|TestApplyDB' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** both.
- [ ] **Step 4: Run.** Expected: PASS, also under `-race`.
- [ ] **Step 5: Commit** `feat(litellm): reconcile managed database models (#598)`.

### Task 4: Route writes choose the API for local rows

**Files:**
- Modify: `wt/internal/litellm/service.go` (`Options`, `ApplyChange`, `Sync`)
- Test: `wt/internal/litellm/service_db_test.go`

**Interfaces:**
- Consumes: `AdminClient`, `DBModelFromRow`, `PlanDB`, `ApplyDB`.
- Produces: `Options.Admin *AdminClient` (nil keeps today's behavior exactly); `Result.Via string` (`"db"` or `"file"`); a local row's `Outcome.Action` values are unchanged (`routed`, `unrouted`, `adopted`, `rewritten`) so `wt-model litellm sync --json` consumers see no new action.

Behavior, stated once: with `Options.Admin` set, `ApplyChange`/`Sync` split the planned rows into local (family from `RowFamily`) and the rest. The rest follow the existing locked file write and restart. Local rows are applied through the API and cost no restart. A local add whose id has an unmarked `config.yaml` row is skipped with a warning (Review Focus 2). Any `ErrDBModelsOff`, connection error or post-add `List` that lacks the row (Review Focus 5) sends that whole local batch down the file path and adds one warning line `database routes unavailable (<reason>); used config.yaml`.

- [ ] **Step 1: Write failing tests** using a fake proxy (`httptest`) and a scratch `config.yaml`: `TestStartAddsLocalRouteThroughAPIWithoutRestart` (assert the fake saw `POST /model/new`, `Options.Restart` was never called, `config.yaml` unchanged on disk); `TestStopDeletesThroughAPI`; `TestCloudRowStillWritesFileAndRestarts`; `TestAPIOffFallsBackToFileAndWarns`; `TestUnmarkedYamlRowBlocksDBTwin`; `TestSuccessThatIsNotListedFallsBack`.
- [ ] **Step 2: Run** `go test ./internal/litellm -run 'TestStart|TestStop|TestCloudRow|TestAPIOff|TestUnmarked|TestSuccessThat' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the split in `applyPlanned`'s callers; keep `applyPlanned` for the file side; take no new lock for API calls (LiteLLM serializes its own writes; convergence is Task 3's job).
- [ ] **Step 4: Run** the new tests, then `go test ./internal/litellm` whole. Expected: PASS.
- [ ] **Step 5: Commit** `feat(litellm): local routes go through the model API, no restart (#598)`.

### Task 5: Settings and the one-time migration

**Files:**
- Modify: `wt/internal/litellm/configfile.go` (`EnsureSettings`), `wt/internal/litellm/service.go` (`planSync`)
- Test: `wt/internal/litellm/migrate_db_test.go`; fixtures that fail until they carry the key: `wt/cmd/wt-model/litellm_sync_test.go`, `wt/internal/litellm/{discovered,service}_test.go` (the repo's CLAUDE.md names them; `go test ./...` lists them)

**Interfaces:**
- Produces: `ensureStoreModelInDB(root *yaml.Node)` called from `EnsureSettings`, mirroring `ensureCancelOnDisconnect`: sets `general_settings.store_model_in_db: true` only when absent. First sync with `Options.Admin` set and managed local rows still in the file: the plan adds them through the API first, and only after every add is confirmed (`List`) removes them from the file, one restart total (which also loads `store_model_in_db`).

- [ ] **Step 1: Write failing tests**: `TestEnsureSettingsAddsStoreModelInDBWhenAbsent`, `TestEnsureSettingsKeepsExplicitFalse`, `TestMigrationAddsToDBBeforeRemovingFromFile` (fake proxy records order; Review Focus 3: kill the fake after the adds and assert the rows are still in the file), `TestMigrationIsIdempotent`.
- [ ] **Step 2: Run** `go test ./internal/litellm -run 'TestEnsureSettings|TestMigration' -v`. Expected: FAIL.
- [ ] **Step 3: Implement**, then update the named fixtures until `go test ./... ` in `wt/` lists no "settled config needs no change" failure.
- [ ] **Step 4: Run** `cd wt && go test ./internal/litellm ./cmd/wt-model`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(litellm): enable database models and migrate managed local rows (#598)`.

### Task 6: Wire the admin client into wt-model; startup reconcile; list

**Files:**
- Modify: `wt/cmd/wt-model/litellm.go` (`sync`, `list`), the `wt-model serve` construction in `wt/internal/modelserver` where `lifecycle`'s route hook gets its `litellm.Options` (find with `grep -rn "litellm.Options{" wt/internal wt/cmd`), `wt/internal/config/proxyhome.go` (read the key through the existing proxy-pair reader, no new file)
- Test: `wt/cmd/wt-model/litellm_db_test.go`

**Interfaces:**
- Consumes: the proxy pair (`url`, `api_key`) from the wt-model home.
- Produces: a helper `func adminFromPair(url, key string) *litellm.AdminClient` (nil when either is empty); `wt-model litellm list` gains a `STORE` column (`db` or `file`) and the `--json` shape gains `"store"` per row, pinned in `docs/contracts/litellm-cli.sample.json` in the same change; `serve` runs one `Sync` at start so a restarted proxy (or one whose database was wiped) is repopulated.

- [ ] **Step 1: Write failing tests**: `TestListShowsStoreColumn`, `TestListJSONCarriesStore` (contract fixture), `TestServeStartReconcilesDatabaseRoutes` (fake proxy missing a running model's route; after serve start the fake has it).
- [ ] **Step 2: Run** `cd wt && go test ./cmd/wt-model -run 'TestListShowsStore|TestListJSONCarriesStore|TestServeStartReconciles' -v`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./cmd/wt-model ./internal/modelserver`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt-model): database routes in sync, list and serve start (#598)`.

### Task 7: Docs, live check, PR

**Files:**
- Modify: `wt/docs/internals/litellm-routes.md`, `wt/docs/wt-model-server.md`, `docs/guides/14-litellm-install-and-configuration.md`, `wt/CLAUDE.md` ("LiteLLM routes" bullets: local rows are database rows when the API is reachable; `Result.Via`), `docs/contracts/litellm-cli.sample.json`

- [ ] **Step 1: Update the docs** to describe: which rows live where, the fallback and its warning line, the one-time migration restart, the opt-out (`store_model_in_db: false`). Guide 14 shows the commands (`wt-model litellm list`) and no live inventory.
- [ ] **Step 2: Run** `make lint` from the repo root. Expected: exit 0, `ALL LINKS OK`.
- [ ] **Step 3: Live check on the real proxy (owner approves the one restart)**: `wt-model litellm sync --dry-run` shows the migration plan; run it; then `wt start <a small local model>` and `wt stop <it>` while `curl` polls `/health/liveliness` every second and `~/.litellm.err.log` is tailed. Expected: no restart (the proxy pid is unchanged), `wt-model litellm list` shows `db` for the local row while running and no row after the stop, and a fresh `wt smoke <model>` passes.
- [ ] **Step 4: Run** `make pre-pr` with `run_in_background` (about 12.5 minutes); read the exit from the log. Expected: exit 0.
- [ ] **Step 5: Open the PR** with `gh pr create --body-file`, body ending with `Refs #598` and the generated-with line; list the live-check numbers in it.

---

## Self-review notes

- Spec coverage: phase 1 of the design (database routes, deterministic ids, `store_model_in_db`, reconcile at serve start) is Tasks 1 to 6; aliases, groups, subscription and header mode are phases 2 and 3 and get their own plans. The `order` field and `DBModel.Params` are already general enough for phase 2 to reuse `PlanDB` unchanged.
- Names used across tasks: `DBModel`, `AdminClient`, `ErrDBModelsOff`, `DBID`, `DBModelFromRow`, `PlanDB`, `ApplyDB`, `Options.Admin`, `Result.Via`, `adminFromPair` are defined once each above.
- Unverified before execution (check in Task 1's first test run against the scratch LiteLLM from the spike harness, not only the fake): the exact `/model/delete` body shape, whether `/model/new` honors a caller-supplied `model_info.id` on 1.109.1 (the spike used it), and the "already exists" error text.
