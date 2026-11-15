# wt-model Server (Phase 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `wt-model serve`: one HTTP daemon, on one explicit address, that serves the model catalog, start/stop jobs, a session registry with auto-stop, and a streaming gateway to LiteLLM, all behind one bearer token.

**Architecture:** A new binary `cmd/wt-model` in the existing `wt` Go module wraps the existing packages (`catalog`, `localmodels`, `lifecycle`, `litellm`, `tiers`, `config`) behind a JSON API whose wire types live in a dependency-free package `internal/modelapi`. Four new internal packages, each with one job: `modelapi` (wire types), `sessionreg` (who is using which model), `modeljobs` (one-at-a-time background jobs), `modelgateway` (reverse proxy); `internal/modelserver` assembles them with per-call seams. `wt` itself is **not** changed in this phase.

**Tech Stack:** Go 1.26.7, stdlib `net/http` (`ServeMux` method+wildcard patterns, `httputil.ReverseProxy`), cobra (already a dependency). No new module dependencies.

**Spec:** `docs/superpowers/specs/2026-11-15-wt-model-session-split-design.md` (Phase 1 of "Migration"). Later phases (client in `wt-session`, TUI split, command moves, guides) are separate plans that consume the API this plan defines.

## Global Constraints

- API path prefix `/control/v1/`; gateway at `/v1/*`; `modelapi.APIMajor = 1`, `modelapi.SchemaVersion = 1`.
- **No default listen address.** `wt-model serve` requires `--listen`; a non-loopback `--listen` without `--token-file` refuses to start; a token file that is group- or world-readable refuses to start; token compared with `subtle.ConstantTimeCompare`.
- The gateway **drops any client `Authorization` / `x-api-key` header** and sends the host's LiteLLM key; it sets **no idle timeout and no body-size limit** on streams; while the proxy is unreachable it returns `503` with `Retry-After`.
- Heartbeat interval 20 s, session TTL 60 s, idle grace default 10 min (server setting `--idle-grace`).
- Auto-stop touches only **local** models that some session has registered; never cloud models, never a model no session ever used.
- Only one start/stop job runs at a time (FIFO); jobs survive client disconnect; job state is in memory.
- Tests: every `Test*` has a top-level `//` comment (what and why); per-call seams, not package globals; every new package whose tests reach config, git or lifecycle has a `TestMain` with the isolation helpers (`config.IsolateConfigHomeForTest`, `gitenv.IsolateForTest()` as a top-level statement, `tiers.IsolateForTest`, `lifecycle.IsolateProcessesForTest`) **[call: apply all four to `internal/modelserver` and `cmd/wt-model`; the dependency-free packages need none]**; no test binds a fixed port or dials the real proxy (`httptest`, `127.0.0.1:0`).
- Never `os.Exit` outside `main`; call `lifecycle.WaitPendingRoutes()` before process exit and before reporting a started model as ready.
- `wt-model` never writes `registry.toml` or `config.yaml` in this phase; it only reads them and calls `lifecycle`.
- Run Go from `wt/`. Scope test runs with `-run`; the full suite is ~10 min.

## Review Focus

Inputs and conditions the spec implies that no task's happy path covers; each has a pinning test in the owning task.

1. A client heartbeats after its session expired (server restarted, or a network gap > TTL): the server answers "unknown session" so the client re-registers, never silently accepts. (Task 2, Task 7)
2. Two clients ask to start two different models at once: they run one after another, the second shows `queued`, neither corrupts the route writes. (Task 3, Task 10)
3. The client disconnects mid-start: the job keeps running and its result is still fetchable by id. (Task 3, Task 10)
4. The proxy is mid-restart when an agent request arrives: `503` + `Retry-After`, not a 502 with a Go error text or a hang. (Task 5)
5. A model on an Exclusive provider (mtplx, rec, sushi) stops as a whole: a session using any model of that family blocks its auto-stop. (Task 6)

---

### Task 1: `modelapi` wire types and contract fixture

**Files:**
- Create: `wt/internal/modelapi/modelapi.go`
- Create: `wt/internal/modelapi/modelapi_test.go`
- Create: `docs/contracts/model-api.sample.json`

**Interfaces:**
- Produces (imported by every later task; the package imports only the standard library):
  - `const APIMajor = 1`, `const SchemaVersion = 1`
  - `type Ping struct { APIMajor int \`json:"api_major"\`; Schema int \`json:"schema"\`; Server string \`json:"server"\` }`
  - `type Price struct { Input, Cache, Output *float64 }` (json keys `input`, `cache`, `output`, each `omitempty`; same unit as `config.PriceInForce`)
  - `type Row struct { ID, GatewayID, Family, ProviderID, ModelName, Location string; Tags []string; Registered, Discovered bool; State, Action, BlockReason string; Tier, TierCode, Confidence string; Aligned *float64; Context int; Protocols []string; Price *Price; Sessions int }` with snake_case json keys; `State` is one of `StateRunning|StateLoading|StateStopped|StateBlocked|StateUnknown` (string constants `"running"…`); `Action` is `ActionLaunch|ActionStart|ActionBlock`; `BlockReason`, `Tier*`, `Confidence`, `Aligned`, `Context`, `Price` are `omitempty`.
  - `type Catalog struct { Schema int; Generated string; Providers map[string]string; Rows []Row }`
  - Requests: `type OthersAnswer struct { Stop []string }`; `type StartRequest struct { Model string; AllowReplace bool; Others *OthersAnswer }`; `type StopRequest struct { Model string; ConfirmInUse bool }`; `type JobRef struct { ID string }`
  - Jobs: `type JobStatus struct { ID, Kind, Model, State, Step, Created, Updated string; Result *JobResult }` with `State` one of `JobQueued|JobRunning|JobSucceeded|JobFailed|JobCancelled`; `type JobResult struct { OK bool; Error *Problem }`
  - `type Problem struct { Code, Message string; Occupants []string; Others []Resident; OthersUnknown []string; Sessions []string }` with codes `CodeOccupied|CodeOthers|CodeOccupancyUnknown|CodeStillFinishing|CodeInUse|CodeStopped|CodeFailed|CodeCancelled|CodeUnknownModel|CodeBlocked`; `type Resident struct { ID, Label string; Stoppable bool }`
  - Sessions: `type SessionRegister struct { ID string; Models []string }`; `type SessionBeat struct { Models []string }`
  - `type APIError struct { Error string \`json:"error"\`; Code string \`json:"code,omitempty"\` }` (the body of every non-2xx control response)

- [ ] **Step 1: Write the failing test `TestFixtureMatchesTypes`** in `modelapi_test.go`. It reads `../../../docs/contracts/model-api.sample.json` (a document `{"ping":Ping,"catalog":Catalog,"job":JobStatus,"start_request":StartRequest,"problem":Problem}`), decodes each member with `DisallowUnknownFields` into its type, re-marshals, and asserts `reflect.DeepEqual` with the decoded original JSON. The comment must contain the literal `docs/contracts` (CI maps contract changes to tests by that string).
- [ ] **Step 2: Write `TestEveryProblemCodeIsDocumented`**: the fixture's `problem` plus a table in the test of all `Code*` constants; assert each constant's value is a lowercase snake_case string and unique.
- [ ] **Step 3: Run** `go test ./internal/modelapi -run 'TestFixture|TestEveryProblem' -v`. Expected: FAIL (package does not exist).
- [ ] **Step 4: Implement the types above in `modelapi.go`; write the fixture** with one catalog row of every `State` value across three rows (a running local, a cloud launchable, a blocked row with `block_reason`), a `job` in state `failed` carrying a `Problem` of code `occupied` with one `Occupants` entry, and one `Others` entry.
- [ ] **Step 5: Run the tests.** Expected: PASS.
- [ ] **Step 6: Commit** `feat(wt): modelapi wire types and contract fixture`.

### Task 2: `sessionreg` session registry

**Files:**
- Create: `wt/internal/sessionreg/sessionreg.go`
- Test: `wt/internal/sessionreg/sessionreg_test.go`

**Interfaces:**
- Produces:
  - `func New(ttl time.Duration, now func() time.Time) *Registry` (safe for concurrent use)
  - `func (r *Registry) Register(id string, models []string) error` — error if `id` is empty; re-registering an id replaces its model set and refreshes its beat; empty and duplicate model ids are dropped
  - `func (r *Registry) Heartbeat(id string, models []string) (known bool)` — adds `models` to the session's set and refreshes it; `false` when the id is unknown or its TTL has lapsed (the caller must re-register)
  - `func (r *Registry) End(id string)`
  - `func (r *Registry) Counts(ids []string) map[string]int` — number of **live** sessions per model, zero-filled; liveness (`now - lastBeat <= ttl`) is evaluated at read time
  - `func (r *Registry) Live() map[string]int` — every model with at least one live session, id-agnostic
  - `func (r *Registry) Users(model string) []string` — live session ids using it, sorted
  - `func (r *Registry) IdleSince(model string) (since time.Time, seen bool)` — `seen` is false for a model no session ever registered; `since` is the later of the last beat or end of any session that used it, meaningful only when its live count is 0
  - `func (r *Registry) Seen() []string` — every model ever registered, sorted

- [ ] **Step 1: Write failing tests** (each with a what/why comment): `TestSessionCountsOncePerModel` (a session listing `a` twice counts 1; two sessions on `a` count 2); `TestExpiredSessionStopsCounting` (fake clock advanced past TTL: `Counts` is 0 with no explicit `End`); `TestHeartbeatAfterExpiryIsUnknown` (Review Focus 1: `Heartbeat` returns `false` after TTL, and the session is not resurrected — `Counts` stays 0); `TestEndedSessionStartsIdleClock` (after `End` at t0, `IdleSince(m)` is `(t0,true)`); `TestUnseenModelIsNotSeen` (`IdleSince` → `(zero,false)`); `TestLiveIsIdAgnostic` (a model id no registry knows still appears in `Live()`).
- [ ] **Step 2: Run** `go test ./internal/sessionreg -v`. Expected: FAIL.
- [ ] **Step 3: Implement** with a mutex-guarded map of sessions (`models set`, `lastBeat`) and a `lastUsed map[string]time.Time` updated on register, heartbeat and end.
- [ ] **Step 4: Run.** Expected: PASS, including `-race`.
- [ ] **Step 5: Commit** `feat(wt): sessionreg, an in-memory session registry with TTL`.

### Task 3: `modeljobs` one-at-a-time job manager

**Files:**
- Create: `wt/internal/modeljobs/modeljobs.go`
- Test: `wt/internal/modeljobs/modeljobs_test.go`

**Interfaces:**
- Consumes: `modelapi.JobStatus`, `modelapi.JobResult`, the `Job*` state constants (Task 1).
- Produces:
  - `type RunFunc func(ctx context.Context, step func(string)) modelapi.JobResult`
  - `func NewManager(now func() time.Time) *Manager`
  - `func (m *Manager) Submit(kind, model string, run RunFunc) string` — returns a random 128-bit hex id immediately; the job is `queued`, then `running` when it reaches the front of the single FIFO worker, then `succeeded`/`failed` (from `Result.OK`) or `cancelled`
  - `func (m *Manager) Get(id string) (modelapi.JobStatus, bool)`
  - `func (m *Manager) Cancel(id string) bool` — cancels a queued job (never runs) or a running job's context; `false` for an unknown or finished id
  - `func (m *Manager) Shutdown(ctx context.Context) error` — stops accepting, cancels the queue, waits for the running job or `ctx`
  - The job's context derives from `context.Background()`, **not** from any request. The manager keeps the 100 most recent finished jobs.

- [ ] **Step 1: Write failing tests:** `TestJobsRunOneAtATime` (Review Focus 2: submit A, B; while A blocks on a channel B reports `queued`; releasing A runs B); `TestJobOutlivesItsSubmitter` (Review Focus 3: the `RunFunc`'s ctx is not cancelled when the code that called `Submit` returns or its own context is cancelled; `Get` later shows `succeeded`); `TestCancelQueuedNeverRuns`; `TestCancelRunningCancelsContext` (the `RunFunc` sees `ctx.Done()`, state ends `cancelled`); `TestStepIsVisibleWhileRunning` (`Get().Step` equals the last `step(...)` call); `TestFinishedJobsAreCapped` (101 finished jobs: the oldest id is gone).
- [ ] **Step 2: Run** `go test ./internal/modeljobs -race -v`. Expected: FAIL.
- [ ] **Step 3: Implement** with one goroutine draining a queue; `Get` returns a copy.
- [ ] **Step 4: Run.** Expected: PASS under `-race`.
- [ ] **Step 5: Commit** `feat(wt): modeljobs, a FIFO background job manager`.

### Task 4: Catalog assembly

**Files:**
- Create: `wt/internal/modelserver/catalog.go`
- Create: `wt/internal/modelserver/testmain_test.go`
- Test: `wt/internal/modelserver/catalog_test.go`

**Interfaces:**
- Consumes: `modelapi.Catalog/Row`, `sessionreg.Registry.Counts`, `catalog.Build`/`catalog.Row` (existing: `Status`, `Running`, `Loading`, `Discovered`, `Action()`, `BlockReason()`, `IsProfile()`), `tiers.Index.Rating(providerID, modelName) tiers.Entry`, `config.ModelCost.PriceAt(time.Time) config.PriceInForce`, `config.Provider.EffectiveProtocols()`, `localmodels.Snapshot`.
- Produces: `func BuildCatalog(cfg *config.Config, snap localmodels.Snapshot, ix tiers.Index, sessions *sessionreg.Registry, now time.Time) modelapi.Catalog`.
  - Rows come from `catalog.Build(catalog.Input{Config: cfg, Models: cfg.Models, Inventory: &snap})` (discovered models included, **no** slot-profile rows, no agent filtering); skip any row with `IsProfile()`.
  - `GatewayID` = the row's `Model.ID` (the id an agent sends through the gateway). `State`: `running` when `Ready()`, `loading` when `Loading`, `blocked` when `Action()==ActionBlock`, `unknown` when `Status==StatusUnknown`, else `stopped`. `Protocols` are the provider's `EffectiveProtocols()` as strings; `Tier*`/`Aligned`/`Context` from `ix.Rating(...)` only when the entry `Rated()`; `Context` only when `ContextBasis == tiers.ContextBasisHost` (the rule the claude driver uses); `Price` from `PriceAt(now)`; `Sessions` = `sessions.Counts([]string{id})[id]`.
  - `Providers` copies the snapshot's per-family status as strings; `Generated` is `now` in RFC3339 UTC; rows sorted by `ID`.

- [ ] **Step 1: Write `testmain_test.go`** with the four isolation calls (see Global Constraints) in the order used by `cmd/wt/testmain_test.go`: config home, `gitenv.IsolateForTest()` top-level, `tiers.IsolateForTest()`, `lifecycle.IsolateProcessesForTest()`, then `m.Run()`, cleanup, `os.Exit`.
- [ ] **Step 2: Write failing tests** against hand-built `config.Config` and `localmodels.Snapshot` values: `TestCatalogStatesAndActions` (running local → `running`/`launch`; stopped local → `stopped`/`start`; a cloud model → `launch`; a row whose route is refused → `blocked` with a reason); `TestCatalogSkipsProfileRows`; `TestCatalogIncludesDiscoveredLocalModels` (an inventory entry with `Registered=false` appears with `Discovered=true`); `TestCatalogContextOnlyWithHostBasis` (a `largest_row` window yields `Context==0`); `TestCatalogSessionsCount` (registry with 2 live sessions on a model → `Sessions==2`); `TestCatalogJSONMatchesFixture` (marshal a catalog, check its row key set is a superset of the required keys in `docs/contracts/model-api.sample.json`'s catalog row).
- [ ] **Step 3: Run** `go test ./internal/modelserver -run TestCatalog -v`. Expected: FAIL.
- [ ] **Step 4: Implement `BuildCatalog`** as specified.
- [ ] **Step 5: Run.** Expected: PASS.
- [ ] **Step 6: Commit** `feat(wt): modelserver catalog assembly`.

### Task 5: `modelgateway` streaming reverse proxy

**Files:**
- Create: `wt/internal/modelgateway/gateway.go`
- Test: `wt/internal/modelgateway/gateway_test.go`

**Interfaces:**
- Produces:
  - `type Config struct { Upstream *url.URL; UpstreamKey string; Token string }`
  - `func New(c Config) http.Handler` — serves `/v1/*` only
  - Behavior: authenticates the caller by `Authorization: Bearer <Token>` **or** `x-api-key: <Token>` (constant-time), `401` otherwise; removes both headers and sets `Authorization: Bearer <UpstreamKey>` (also `x-api-key` is **not** forwarded); preserves path and query (`/v1/messages`, `/v1/chat/completions`); `FlushInterval: -1`; transport with a dial timeout but **no** response-header, idle or body limits; an upstream connection failure (refused, EOF before any response byte) → `503` with `Retry-After: 5` and body `modelapi.APIError{Error:"model proxy is restarting", Code:"proxy_unavailable"}`.

- [ ] **Step 1: Write failing tests** with `httptest` upstreams: `TestGatewayRejectsMissingOrWrongToken` (401, upstream never called); `TestGatewaySwapsCredentials` (upstream sees only `Authorization: Bearer <UpstreamKey>` and no `x-api-key`, whichever header the client used); `TestGatewayAcceptsXAPIKey`; `TestGatewayStreamsWithoutBuffering` (upstream writes a chunk, flushes, waits; the client reads the first chunk before upstream finishes); `TestGatewayPreservesPaths` (both `/v1/messages` and `/v1/chat/completions` arrive intact with query); `TestGatewayRestartingIs503` (Review Focus 4: upstream listener closed → 503, `Retry-After` header `5`, JSON body); `TestGatewayIgnoresClientDisconnectCleanly` (cancel client mid-stream: upstream's request context is cancelled, no panic).
- [ ] **Step 2: Run** `go test ./internal/modelgateway -race -v`. Expected: FAIL.
- [ ] **Step 3: Implement** using `httputil.ReverseProxy` with a `Rewrite` func (not `Director`, so hop headers are handled by the library) and an `ErrorHandler` for the 503.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): modelgateway, a streaming proxy that swaps credentials`.

### Task 6: Start/stop drivers and auto-stop

**Files:**
- Create: `wt/internal/modelserver/deps.go`, `wt/internal/modelserver/start.go`, `wt/internal/modelserver/stop.go`, `wt/internal/modelserver/autostop.go`
- Test: `wt/internal/modelserver/start_test.go`, `stop_test.go`, `autostop_test.go`

**Interfaces:**
- Consumes: `modeljobs.RunFunc`, `modelapi.*`, `sessionreg.Registry`, `catalog.Find`, `lifecycle.{Target, Options, OthersAnswer, Stage, Resident, StoppableIDs, SettleStart, BeginStops, StopBatch, SettleRoutesTo, OccupiedError, OccupancyUnknownError, StillFinishingError, StoppedError, StartErrorMessage, CanStop, TenancyOf}`.
- Produces:
  - `type StopBatcher interface { Stop(ctx context.Context, en localmodels.Entry) (restartOwed bool, err error) }`
  - `type Deps struct { Config *config.Config; Inventory func(*config.Config) localmodels.Snapshot; Tiers func() tiers.Index; Now func() time.Time; Start func(context.Context, *config.Config, lifecycle.Target, lifecycle.Options) error; Settle func(context.Context, io.Writer, *config.Config, lifecycle.Target) error; BeginStops func(context.Context, *config.Config, []localmodels.Entry) StopBatcher; SettleRoutes func(context.Context, io.Writer, *config.Config); Routed func(ctx context.Context, gatewayID string) error }` and `func DefaultDeps(cfg *config.Config, upstream *url.URL, upstreamKey string) Deps` wiring the real functions (`Routed` polls `GET <upstream>/v1/models` with the key every 500 ms for up to `litellm.ProxyReadyTimeout` until the id is listed).
  - `func (d Deps) StartJob(req modelapi.StartRequest) modeljobs.RunFunc`
  - `func (d Deps) StopJob(req modelapi.StopRequest, sessions *sessionreg.Registry) modeljobs.RunFunc`
  - `type AutoStopper struct { Deps Deps; Sessions *sessionreg.Registry; Grace time.Duration; Jobs *modeljobs.Manager }` with `func (a *AutoStopper) Tick(ctx context.Context)` (one scan) and `func (a *AutoStopper) Run(ctx context.Context, every time.Duration)` (loops `Tick`).

**Behavior pinned by the tests:**
- `StartJob`: resolve the row from a fresh catalog (`catalog.Find`; unknown → `CodeUnknownModel`, a `blocked` row → `CodeBlocked` with its reason); build `lifecycle.Options{AllowReplace, Others (Decided with Stop), Progress (→ step(StageLabel)), Out (a mutex-guarded buffer)}`; call `Start` **once** — never loop; map `*OccupiedError` → `CodeOccupied` with `Occupants`/`Others`/`OthersUnknown` filled (`Resident.ID` = `Entry.ModelID`), `*OccupancyUnknownError` → `CodeOccupancyUnknown`, context cancel → `CodeCancelled`, anything else → `CodeFailed` with `lifecycle.StartErrorMessage`. On success call `Settle`, map `*StoppedError` → `CodeStopped`, then call `Routed` (step `"waiting for the gateway"`); only then `OK`. A refusal that asks only what the request already answered is `CodeFailed`, never `occupied` again.
- `StopJob`: find the running `localmodels.Entry` for the id; refuse `CodeInUse` (with `Sessions` = `Users`) when the **family-wide** live count is > 0 (Exclusive tenancy sums every model of the provider family; Pool/Shared count the model alone) unless `ConfirmInUse`; use one-entry `BeginStops` → `Stop` → one `SettleRoutes` when `restartOwed`; `*StillFinishingError` is success with a step note.
- `AutoStopper.Tick`: for every model in `Sessions.Seen()` whose live count (family-wide for Exclusive) is 0, whose `IdleSince` is at least `Grace` ago, that is a **running local** model in a fresh inventory, and has no queued/running job for it, submit a `kind:"auto-stop"` job using `StopJob` with `ConfirmInUse=false`. Cloud models and unseen models are never touched.

- [ ] **Step 1: Write failing tests with a fake `Deps`** (all function fields are closures recording calls): `TestStartSuccessWaitsForRoute` (order of calls: Start, Settle, Routed; job `OK`); `TestStartOccupiedIsReportedNotLooped` (Start returns `*OccupiedError`; `Start` called once; problem has the occupants); `TestStartCarriesAnswersIntoOptions` (`AllowReplace` and `Others.Stop` reach `Options`); `TestStartUnknownAndBlockedRows`; `TestStartStoppedDuringSettle`; `TestStopRefusesInUseUnlessConfirmed`; `TestStopExclusiveFamilySumsSessions` (Review Focus 5: a session on a sibling model of the same mtplx provider blocks the stop); `TestStopStillFinishingIsSuccess`; `TestAutoStopOnlyIdleSeenLocalModels` (table: idle past grace → job submitted; idle under grace; live session; cloud; never seen — only the first submits); `TestAutoStopSkipsModelWithPendingJob`.
- [ ] **Step 2: Run** `go test ./internal/modelserver -run 'TestStart|TestStop|TestAutoStop' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the three files; `DefaultDeps` is the only place real `lifecycle` functions are named.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): modelserver start/stop jobs and auto-stop`.

### Task 7: HTTP control API and auth

**Files:**
- Create: `wt/internal/modelserver/server.go`, `wt/internal/modelserver/auth.go`
- Test: `wt/internal/modelserver/server_test.go`, `auth_test.go`

**Interfaces:**
- Consumes: Tasks 1–6 and `modelgateway.New`.
- Produces:
  - `type Options struct { Listen string; Token string; IdleGrace time.Duration; Upstream *url.URL; UpstreamKey string }`
  - `func NewServer(o Options, d Deps) (*Server, error)` — builds the `ServeMux`, the session registry (TTL 60 s), job manager and auto-stopper; errors with the messages in Global Constraints for a non-loopback `Listen` with an empty `Token`
  - `func (s *Server) Handler() http.Handler` (for `httptest`), `func (s *Server) Serve(ctx context.Context) error` (listens on `Options.Listen`, runs the auto-stop loop every 30 s, shuts down gracefully on `ctx` done: `Shutdown`, `Jobs.Shutdown`, then `lifecycle.WaitPendingRoutes()`)
  - `func LoadToken(path string) (string, error)` in `auth.go` — trims whitespace; refuses (error naming the path and the `chmod 600` fix) when the mode has any group/other bit; empty file is an error
  - Routes (all under `/control/v1/`, bearer-token middleware except none): `GET ping` → `modelapi.Ping`; `GET catalog` → `modelapi.Catalog` (fresh inventory per call); `GET resolve?model=ID` → one `Row` or `404 APIError{Code:"unknown_model"}` carrying `catalog.MissingReason` as `Error`; `POST start` (`StartRequest`) and `POST stop` (`StopRequest`) → `202 JobRef`; `GET jobs/{id}` → `JobStatus` or 404; `POST jobs/{id}/cancel` → 204 or 404; `POST sessions` (`SessionRegister`) → 204; `POST sessions/{id}/heartbeat` (`SessionBeat`) → 204, or **`410 Gone` `APIError{Code:"unknown_session"}`** when `Heartbeat` returns false; `DELETE sessions/{id}` → 204 (idempotent). Unknown JSON fields in a request body → 400. The gateway is mounted at `/v1/`. A request with a missing or wrong token → `401 APIError`, handler never runs.

- [ ] **Step 1: Write failing tests** with `httptest.NewServer(s.Handler())` and fake `Deps`: `TestControlRequiresToken`; `TestPingReportsAPIMajor`; `TestCatalogEndpointShowsSessionCounts` (register a session, see `sessions:1`); `TestStartReturnsJobRefAndJobCompletes` (poll `jobs/{id}` until `succeeded`); `TestHeartbeatUnknownSessionIs410` (Review Focus 1); `TestEndSessionIsIdempotent`; `TestResolveUnknownCarriesMissingReason`; `TestUnknownBodyFieldIs400`; `TestNewServerRefusesNonLoopbackWithoutToken`; `TestLoadTokenRefusesGroupReadable` (file mode 0640 → error mentioning `chmod 600`; 0600 → token); `TestGatewayIsMountedAndAuthenticated`.
- [ ] **Step 2: Run** `go test ./internal/modelserver -run 'TestControl|TestPing|TestCatalogEndpoint|TestStartReturns|TestHeartbeat|TestEnd|TestResolve|TestUnknownBody|TestNewServer|TestLoadToken|TestGatewayIsMounted' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** server and auth (stdlib `ServeMux` patterns such as `"POST /control/v1/sessions/{id}/heartbeat"`).
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): modelserver HTTP control API with token auth`.

### Task 8: `cmd/wt-model` binary, build and CI wiring

**Files:**
- Create: `wt/cmd/wt-model/main.go`, `wt/cmd/wt-model/serve.go`, `wt/cmd/wt-model/exitcode.go`, `wt/cmd/wt-model/testmain_test.go`
- Test: `wt/cmd/wt-model/serve_test.go`
- Modify: `wt/Makefile` (build, install, uninstall, clean, `single-p` package list, help text), `wt/.gitignore`, `wt/docs/internals/packages.md`, `wt/docs/internals/testing.md` (the list of git-isolating packages if the isolation test needs it), `wt/CLAUDE.md` (the "Go module" package list and a short "wt-model" section), `CLAUDE.md` (Architecture: `wt-model`, the API fixture), `docs/guides/` is **not** touched in this phase.

**Interfaces:**
- Produces: command `wt-model serve --listen ADDR [--token-file PATH] [--idle-grace 10m]`; `func serveCmd() *cobra.Command`; `func loadServeConfig() (*config.Config, *url.URL, string, error)` returns the host config plus the LiteLLM base URL and key from `cfg.LitellmBaseURL()` / `cfg.LitellmAPIKey()` and fails with a message naming `wt litellm set --url --api-key` when either is empty; `exitcode.go` carries its own `reportExit(err error, w io.Writer) int` printing `wt-model: <err>` (copy of the `cmd/wt` shape; exit 1 on error).
- Behavior: `--listen` is required (cobra `MarkFlagRequired`); `main` runs `Execute`, then `lifecycle.WaitPendingRoutes()`, then exits with `reportExit`; SIGINT/SIGTERM cancel the serve context. On start it prints one line `wt-model: listening on <addr> (gateway /v1, control /control/v1)` to stderr.

- [ ] **Step 1: Write `testmain_test.go`** (same four isolations) and failing tests: `TestServeRequiresListen` (running without `--listen` fails with cobra's required-flag error); `TestServeRefusesNonLoopbackWithoutToken` (`--listen 0.0.0.0:0`, no token file → error naming `--token-file`); `TestServeRefusesUnconfiguredLiteLLM` (empty `[litellm]` → message naming `wt litellm set`); `TestServeAnswersPingOnEphemeralPort` (`--listen 127.0.0.1:0`, fake `Deps`, ping with the token, cancel context, server returns nil).
- [ ] **Step 2: Run** `go test ./cmd/wt-model -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the command. In `Makefile` add `go build -o bin/wt-model ./cmd/wt-model` plus its `codesign --force --sign -` in **both** `build` and `install` (the `cp` invalidates the signature), add `bin/wt-model` to `uninstall` and `clean`, add `./internal/modelserver ./internal/modeljobs ./internal/modelgateway ./internal/sessionreg ./cmd/wt-model` to the `single-p` package list, and `bin/wt-model` to `.gitignore`. Add rows for `cmd/wt-model/` and each new `internal/` package to `packages.md`; add the packages to the `wt/CLAUDE.md` list.
- [ ] **Step 4: Run** `go test ./cmd/wt-model -v`, then `go build ./... && go vet ./... && make check` and `go test ./internal/gitenv -run TestEveryPackageThatRunsGitIsolatesIt`. Expected: all PASS.
- [ ] **Step 5: Run** `make -C .. lint` (link check over the edited docs). Expected: PASS.
- [ ] **Step 6: Commit** `feat(wt): wt-model serve binary and build wiring`.

### Task 9: End-to-end test through real HTTP with fakes

**Files:**
- Test: `wt/internal/modelserver/e2e_test.go`

**Interfaces:** consumes `NewServer`, a fake `Deps` (as in Task 6 tests), an `httptest` fake LiteLLM upstream that lists model ids at `/v1/models` and streams a reply at `/v1/chat/completions`.

- [ ] **Step 1: Write `TestEndToEndSessionLifecycle`** (comment: the one place the whole server is exercised together, because each unit passing does not show they agree on the wire). With a fake clock: (1) ping; (2) catalog shows model `m` as `stopped`/`start`; (3) register session `s1` on `m`; (4) `POST start`, poll the job to `succeeded` while the fake `Routed` waits for `m` to appear in the fake upstream's list; (5) catalog shows `m` `running`, `sessions:1`; (6) a streamed chat completion through `/v1/chat/completions` with the client token reaches the upstream with only the upstream key; (7) heartbeat succeeds; (8) `DELETE` the session; (9) advance the clock past `IdleGrace`, call the auto-stopper's `Tick`, poll the resulting job to `succeeded`; fake `BeginStops` saw exactly `m`; (10) a second heartbeat on `s1` is `410`.
- [ ] **Step 2: Write `TestTwoStartsRunInOrderAndSurviveDisconnect`** (Review Focus 2 and 3 over HTTP): post two starts for different models, cancel the first request's context immediately, assert the second job reports `queued` while the first runs and both end `succeeded`.
- [ ] **Step 3: Run** `go test ./internal/modelserver -run 'TestEndToEnd|TestTwoStarts' -race -v`. Expected: PASS (write whatever small fix the e2e exposes in the owning task's code, with its own failing unit test first).
- [ ] **Step 4: Run the package gate:** `go test ./internal/modelapi ./internal/sessionreg ./internal/modeljobs ./internal/modelgateway ./internal/modelserver ./cmd/wt-model -race -count=1`. Expected: PASS.
- [ ] **Step 5: Commit** `test(wt): end-to-end test of the wt-model server`.

---

## Self-Review

- **Spec coverage (Phase 1 of the design):** control API catalog/resolve/start/stop/jobs/sessions (Tasks 1, 4, 6, 7); pollable jobs (3, 7); sessions with UUID/heartbeat/end/TTL and "no heartbeat = done" (2, 7); explicit start + auto-stop with grace and family-wide Exclusive rule (6); gateway with credential swap, streaming, 503-on-restart (5); explicit listen, token file mode, non-loopback guard, constant-time compare (7, 8); API versioning (1, ping in 7); serialization of route changes via the single job worker (3); no registry/price/tier endpoints (none defined); deployment of the binary in Makefile/CI/docs (8). **Not in this plan, by design:** the client in `wt-session`, TUI split, command moves, retirement of `refcount`/direct routes (Phases 2–4), style guide (Phase 0).
- **Gaps accepted and stated:** `wt-model check` (server-side sanity command) is deferred to Phase 4 with the other host commands; alias rows that share one provider-side model (`lifecycle.SameModel`) are counted per id as sent by the client, as the design records ids as clients register them (revisit in Phase 2 when the client chooses the ids).
- **Type consistency:** `modelapi` names (`StartRequest`, `StopRequest`, `JobStatus`, `Problem`, `Code*`, `Job*`, `State*`, `Action*`) are used unchanged in Tasks 3, 4, 6, 7, 9; `RunFunc` is defined in Task 3 and consumed in Task 6; `Deps` is defined in Task 6 and consumed in 7–9; `sessionreg` method names match their uses in Tasks 4, 6, 7.
- **Proportion:** the plan states interfaces, behavior and test names; no function bodies.
