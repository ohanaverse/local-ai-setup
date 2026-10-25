# Process-Server Engine (PR 2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the mtplx-only start/stop/loading code in `wt/internal/lifecycle` with a spec-driven process-server engine that mtplx runs on unchanged, and give it what rec needs: a key on every probe, command-JSON readiness, and machine-wide exclusivity with adoption.

**Architecture:** `internal/provider` gains a `ProcessSpec` (binary, spawn/stop argv templates, pidfile, how to recognise the server's process, readiness check) and a per-family exclusivity scope; mtplx's values are its first row. `lifecycle` gets one `processServer` backend (comparable: it holds only a family name and reads the spec on use) that replaces `mtplxBackend`, and the mtplx identity/loading code becomes family-generic. rec itself is PR 3; this PR proves the engine with mtplx and with synthetic specs in tests.

**Tech Stack:** Go 1.26.7 (`wt/`), standard `testing`, `httptest`.

**Spec:** `docs/superpowers/specs/2026-10-25-provider-interface-and-rec-design.md` (PR 2 of its Delivery section, plus the user decision on a foreign running rec: adopt if it already serves the requested model, otherwise ask first).

**Base:** stacked on `feat/provider-table` (PR #454). Create `feat/process-server` from it (or from `main` once #454 merges).

**Departure from the spec's PR 2 wording:** the registry overlay (a `[providers.process]` table in `registry.toml` that overrides a built-in row or defines a new provider) is not in this PR. It makes `Family()`, policies and backends depend on the loaded config instead of a static table, which is a separate change; PR 3 ships rec as a built-in row. Until the overlay lands, adding a process-kind provider is a data row in `internal/provider/table.go`, not a registry edit.

## Global Constraints

- mtplx behavior is unchanged: the existing mtplx tests (`mtplx_test.go`, `mtplx_loading_test.go`, `loading_darwin_test.go`, and every mtplx case in `evictions_test.go`, `confirm_test.go`, `routes_test.go`, `origin_test.go`, `wrappers_test.go`, `lifecycle_test.go`) keep their assertions; they may change only at call sites (renames).
- A provider with no `secret_ref` sends no `Authorization` header on any probe: ollama, omlx, mtplx probes stay byte-for-byte as they are.
- `internal/provider` stays standard-library only; `lifecycle` keeps exported signatures `LoadingServer(cfg, family) Loading`, `StopLoading(ctx, cfg, providerID, pid) error`, `Start`, `Stop`, `Startable`, `CanStop`, `TenancyOf`.
- The mtplx pidfile stays a bare pid at `/tmp/local-ai-setup-mtplx.pid` (llmbench shares it), `WT_MTPLX_PIDFILE` still redirects it and the log, and the `.wt` start record stays beside it.
- Process safety is unchanged: a pid is signalled only after identification (own file, uid, recorded start time, argv match on the port), rechecked before SIGKILL.
- `ollama` and `omlx` backends and the omlx pool/eviction code are not touched.
- Every `Test*` carries a top-level `//` comment saying what and why (`wt/CLAUDE.md`). Commit messages end with `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.
- rec facts the engine must handle (verified live, rec 0.1.0): the port stays closed ~10–12 s while loading; `/health` answers 200 before the server is ready; readiness is `rec status` printing JSON with `"ready":true`; `rec status` and `rec stop` exit 1 with `rec: no managed rec instance is reachable` when nothing runs; every route needs `Authorization: Bearer <key>`; one instance per machine even across ports; the served model id is exactly `flint` (`rec status` shows `"model":"flint"`).

## Review Focus

1. **mtplx unchanged**: the spawn argv stays `["serve","--model",M,"--port",P,"--host","127.0.0.1","--model-id",M]` and the stop argv `["stop","--port",P,"--grace-seconds","10"]`. Pinned in Task 3.
2. **No key, no header**: ollama, omlx and mtplx probes carry no `Authorization`. Pinned in Task 4.
3. **A 401 with a key is "key refused", not "down"**: the provider reads as unreachable-with-reason, never as an empty server a start can replace. Pinned in Task 4.
4. **A readiness command that fails**: exit 1 with no output, invalid JSON, a missing path, or a hang past its timeout reads as "not ready", never as a crash or as ready. Pinned in Task 5.
5. **Loading is not failure**: a closed port while the spawned pid is alive keeps waiting; the pid dying fails fast with the log tail. Pinned in Task 5.
6. **A foreign instance** (no pidfile) that already serves the requested model is adopted without a stop or spawn; any other foreign instance needs approval and is stopped with the stop command. Pinned in Task 6.

---

### Task 1: ProcessSpec and exclusivity scope in the provider table

**Files:**
- Modify: `wt/internal/provider/provider.go` (types, accessors), `wt/internal/provider/table.go` (mtplx row), `wt/internal/provider/provider_test.go`

**Interfaces:**
- Produces (exact):
  - `type ReadyKind string` with `ReadyModelsList ReadyKind = "models-lists"` and `ReadyCommandJSON ReadyKind = "command-json"`.
  - `type Ready struct { Kind ReadyKind; Command []string; Path string; Equals string; ModelPath string }` — `Command` is the argv after the binary for `command-json`; `Path` is a dot-separated JSON path whose scalar, rendered as text, must equal `Equals` (`"true"` for a bool); `ModelPath` is the JSON path of the model the instance serves.
  - `type ProcessSpec struct { Binary string; Spawn, Stop []string; PidfileDefault, LogDefault, PidfileEnv string; Subcommand, DaemonModule, PortFlag, ModelFlag string; Ready Ready }` — `Spawn` and `Stop` are argv templates after `Binary` with `{model}` and `{port}` placeholders; `Subcommand`, `DaemonModule` and `PortFlag` say how the running server's argv is recognised; `ModelFlag` is the argv flag whose value is the model name.
  - `type Scope int` with `ScopePort Scope = iota` and `ScopeMachine`; `FamilySpec` gains `Scope Scope` and `Process *ProcessSpec`.
  - `func ProcessFor(family string) (ProcessSpec, bool)` (a deep copy); `FamilyByName` now deep-copies `Process`; `func ProcessFamilies() []string` (sorted families with a `Process`).
- mtplx row: `Binary "mtplx"`, `Spawn ["serve","--model","{model}","--port","{port}","--host","127.0.0.1","--model-id","{model}"]`, `Stop ["stop","--port","{port}","--grace-seconds","10"]`, `PidfileDefault "/tmp/local-ai-setup-mtplx.pid"`, `LogDefault "/tmp/local-ai-setup-mtplx.log"`, `PidfileEnv "WT_MTPLX_PIDFILE"`, `Subcommand "serve"`, `DaemonModule "mtplx.server.openai"`, `PortFlag "--port"`, `ModelFlag "--model-id"`, `Ready{Kind: ReadyModelsList}`; family `Scope: ScopePort`.

- [ ] **Step 1: Write the failing tests** in `provider_test.go`: `TestProcessSpecMtplx` asserts every field of `ProcessFor("mtplx")` above; `TestProcessFamilies` asserts `ProcessFamilies()` is `[mtplx]` and `ProcessFor("omlx")`, `("ollama")`, `("mlx_lm_server")`, `("")` are absent; extend `TestTableInvariants` so every family with a `Process` has `Tenancy == Exclusive`, a non-empty `Binary`, a `Spawn` containing `{model}` and `{port}`, and `Ready.Kind` of `ReadyModelsList` or `ReadyCommandJSON` (with a non-empty `Command` and `Path` for the latter); extend `TestResultsAreCopies` to mutate `ProcessFor("mtplx").Spawn[0]` and `FamilyByName("mtplx").Process.Stop[0]` and assert a second call is unchanged.
- [ ] **Step 2: Run to verify failure** — `cd wt && go test ./internal/provider/...`. Expected: FAIL (undefined: ProcessFor, Scope, …).
- [ ] **Step 3: Implement** the types, accessors and the mtplx row; `FamilyByName` clones `Process` (slices included) so no caller shares table memory.
- [ ] **Step 4: Run to verify pass** — `cd wt && go test ./internal/provider/... && go vet ./internal/provider/...`. Expected: PASS.
- [ ] **Step 5: Commit** — `feat(wt): provider table carries a ProcessSpec and exclusivity scope`.

### Task 2: Server identity and loading state, generic over process families

**Files:**
- Rename: `wt/internal/lifecycle/mtplx_loading.go` → `server_loading.go`; `mtplx_loading_test.go` → `server_loading_test.go`
- Modify: `wt/internal/lifecycle/procinfo.go`, `env.go`, `pidproc.go`, `evictions.go`, `lifecycle.go` (the `r.PID` branch in `stopOthers`), `message.go` untouched here, and all lifecycle test files that name the renamed identifiers
- Test: the renamed and existing lifecycle suite

**Interfaces:**
- Consumes: Task 1's `provider.ProcessFor`, `provider.ProcessFamilies`.
- Produces (exact):
  - `func serverArgvMatches(spec provider.ProcessSpec, argv []string, port int) bool` — the logic of `isMtplxServer` with its constants taken from `spec`: `filepath.Base(argv[0]) == spec.Binary` with `argv[1] == spec.Subcommand`; or an interpreter (`python*`, `sh`, `bash`) running a file named `spec.Binary` with `spec.Subcommand`; or `python -m spec.DaemonModule`; then `spec.PortFlag` and the port as two separate arguments.
  - `type serverIdent struct { pid int; start string; group bool; model string }` (was `mtplxIdent`).
  - `func (e *env) loadingServer(cfg *config.Config, family string) (serverIdent, Loading)`, `func (e *env) stopLoadingServer(ctx context.Context, cfg *config.Config, family string, pid int) error`, `func (e *env) forgetServer(family string, pid int)`, `func (e *env) forgetStopped(ctx context.Context, family string, id serverIdent)`, `func (e *env) waitGone(ctx context.Context, id serverIdent, wait time.Duration, sent string) (bool, error)` (unchanged shape).
  - `var processSpecFor = provider.ProcessFor` in `server_loading.go`: the one seam through which `lifecycle` reads a process spec (a test swaps it to give a synthetic family such as `acme` a spec); every spec read in Tasks 2–6 goes through it, never `provider.ProcessFor` directly.
  - `env.serverProc map[string]pidProcess` (was `mtplxProc`), filled in `defaultEnv()` for each `provider.ProcessFamilies()` from the spec (`PidfileEnv` overrides the pidfile and derives the log, as `mtplxProcess()` does).
  - `func serverEndpoint(cfg *config.Config, family string) (origin, modelsURL string, port int)` is Task 3's; this task keeps using `mtplxEndpoint` until then.
  - `LoadingServer(cfg, family)` and `StopLoading(ctx, cfg, providerID, pid)` keep their signatures and now accept any process family (`provider.ProcessFor(family)` ok) instead of `"mtplx"`.
  - `StopInterruptedError.Error()` names the family's binary (`spec.Binary`) in place of the literal "mtplx"; the mtplx text is unchanged.

- [ ] **Step 1: Write the failing test `TestServerArgvMatchesSyntheticSpec`** in `server_loading_test.go`: with `spec := provider.ProcessSpec{Binary: "acme", Subcommand: "run", DaemonModule: "acme.srv", PortFlag: "-p"}` assert true for `["acme","run","-p","9000"]`, `["python3","/x/acme","run","-p","9000"]`, `["/usr/bin/python3","-m","acme.srv","-p","9000"]`; false for a different port, `["vim","acme","run","-p","9000"]`, `["acme","run","--port","9000"]` and `["acme","run","-p9000"]`. Add `TestLoadingServerGeneric` (swap `processSpecFor` so `acme` has that spec, restoring it in `t.Cleanup`): an `env` whose `serverProc["acme"]` pidfile holds a live pid whose stubbed `describe` returns the acme argv, expecting `loadingServer(cfg,"acme")` to return `Loading{PID: pid}`; with a different uid expecting a `Stray` containing "which another user owns".
- [ ] **Step 2: Run to verify failure** — `cd wt && go test ./internal/lifecycle -run 'TestServerArgvMatchesSyntheticSpec|TestLoadingServerGeneric'`. Expected: FAIL (undefined).
- [ ] **Step 3: Rename and generalise.** Declarations by hand (`mtplxIdent`→`serverIdent`, `loadingMtplx`→`loadingServer(family)`, `stopLoadingMtplx`→`stopLoadingServer(family)`, `forgetMtplx`→`forgetServer(family)`, `notMtplxServer`→`notServer(spec,…)`, `isMtplxServer`→`serverArgvMatches(spec,…)`, `mtplxProc`→`serverProc[family]`); call sites mechanically with `gofmt -r` (for example `gofmt -r 'a.loadingMtplx(b) -> a.loadingServer(b, "mtplx")' -w`), then `go build ./... && go vet ./...` until clean. `evictions.go`: `residents` loops over `provider.ProcessFamilies()` for families other than the target whose `snap.Down[f]` is true (the old `"mtplx"` literals), and `loadingMtplxEntry` becomes `loadingServerEntry(snap, family, model)` using `family` for the provider id, `ModelID` and `DiscoveredModelID`. `lifecycle.go`: the `r.PID > 0` branch calls `stopLoadingServer(ctx, cfg, localmodels.Family(r.Entry.ProviderID), r.PID)`.
- [ ] **Step 4: Run the whole lifecycle suite** — `cd wt && go test ./internal/lifecycle/... ./internal/survey/... ./cmd/wt/...`. Expected: PASS, with the old mtplx tests unchanged apart from renamed call sites.
- [ ] **Step 5: Commit** — `refactor(wt): server identity and loading state generic over process families`.

### Task 3: The processServer backend replaces mtplxBackend

**Files:**
- Rename: `wt/internal/lifecycle/mtplx.go` → `server.go`; `mtplx_test.go` → `server_test.go`
- Modify: `wt/internal/lifecycle/lifecycle.go` (`backendsByFamily`, `MtplxAddressError`), `message.go`, `message_test.go`, `provider_conformance_test.go`, tests that name `mtplxBackend{}`

**Interfaces:**
- Consumes: Task 1 specs, Task 2 helpers.
- Produces (exact):
  - `type processServer struct{ family string }` implementing `backend` (comparable on purpose: `maps.Equal` in the existing registry test needs it); `func (b processServer) tenancy() Tenancy` returns the family's `Tenancy`.
  - `func serverEndpoint(cfg *config.Config, family string) (origin, modelsURL string, port int)` and `func serverStartEndpoint(cfg *config.Config, family string) (origin, modelsURL string, port int, err error)` (the old `mtplxEndpoint`/`mtplxStartEndpoint` over `localmodels.FamilyOriginPort(cfg, family)`).
  - `type PortlessAddressError struct{ Provider, Origin string }` replacing `MtplxAddressError`; for provider `mtplx` its text is exactly the old one: `the registry's mtplx base_url (<origin>) names no port, and wt serves mtplx on 127.0.0.1 — name the port mtplx listens on in the base_url, or use http://127.0.0.1`.
  - `func expandArgs(tmpl []string, model string, port int) []string` replacing `{model}` and `{port}` in each element.
  - `backendsByFamily` = `{"ollama": ollamaBackend{}, "omlx": omlxBackend{}}` plus `processServer{family}` for every `provider.ProcessFamilies()`; the table-driven build keeps it a plain `map[string]backend`.
- Behavior (must equal today's mtplx): `start` = `lookPath(spec.Binary)` (else `*BinaryMissingError{Binary: spec.Binary}`), endpoint (else `*PortlessAddressError`), `report(StageStarting)`, `portClosedWithin(models, prebindTimeout)` (else `*PortBusyError{Port}`), `serverProc[family].spawn(bin, expandArgs(spec.Spawn, model, port))`, `recordStart(pid)`, kill on failure, `report(StageWaiting)`, ready wait (Task 5 generalises it; this task keeps `waitForModel` for `ReadyModelsList`) with the log-tail suffix on error, `report(StageWarming)`, `warmup(origin+"/v1/chat/completions", model, models, "", 120s)`. `stop` = `lookPath`, `loadingServer` before, `run(bin, expandArgs(spec.Stop, "", port)...)`, `portClosedWithin(models, stopTimeout)`, `forgetStopped` on closed, else the stop output as the error or `<binary> still listening on port <port>`. `stopModel` = `stop`.

- [ ] **Step 1: Write the failing tests** in `server_test.go`: `TestProcessServerMtplxArgvPinned` (a fake `run`/`spawn` seam records argv: spawn is `serve --model M --port 8003 --host 127.0.0.1 --model-id M`, stop is `stop --port 8003 --grace-seconds 10`), `TestExpandArgs` (`{model}` and `{port}` replaced in every element, others untouched), `TestPortlessAddressErrorKeepsMtplxText` (the exact sentence above), and `TestProcessServerIsComparable` (`maps.Equal(defaultEnv().backends, map[string]backend{"ollama": ollamaBackend{}, "omlx": omlxBackend{}, "mtplx": processServer{"mtplx"}})`).
- [ ] **Step 2: Run to verify failure** — `cd wt && go test ./internal/lifecycle -run 'TestProcessServer|TestExpandArgs|TestPortlessAddressError'`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement** `processServer` over the helpers named above, delete `mtplxBackend`, `mtplxStartEndpoint`, `mtplxEndpoint`, `MtplxAddressError`; update `StartErrorMessage` to match `*PortlessAddressError`; update the existing tests' call sites (`mtplxBackend{}`→`processServer{"mtplx"}`, `mtplxEndpoint(cfg)`→`serverEndpoint(cfg, "mtplx")`, `MtplxAddressError`→`PortlessAddressError{Provider: "mtplx", …}`).
- [ ] **Step 4: Run** — `cd wt && go test ./...`. Expected: PASS; `TestBackendsMatchProviderTable` still passes.
- [ ] **Step 5: Commit** — `refactor(wt): one spec-driven process-server backend; mtplx runs on it`.

### Task 4: A key on every probe

**Files:**
- Modify: `wt/internal/localmodels/match.go` (`FetchModelIDs`, `FetchModelIDsErr`), `served.go` (`ServedIDs`), `sources.go`/`inventory.go` probe callers, `wt/internal/lifecycle/probe.go` (`probe`, `portClosedWithin`, `waitPortOpen`, `waitForModel`, `liveServed`), `wt/internal/localmodels/inventory.go` (`UnregisteredAnswering`)
- Test: `wt/internal/localmodels/served_test.go`, `wt/internal/lifecycle/probe_test.go`

**Interfaces:**
- Consumes: `localmodels.FamilyAPIKey(cfg, family) string` (exists), `config.ResolveSecret`.
- Produces (exact):
  - `func FetchModelIDsKeyed(client *http.Client, url, key string) ([]string, error)`: `FetchModelIDsErr` with `Authorization: Bearer <key>` when `key != ""`; a 401 or 403 returns `*KeyRefusedError`-like `localmodels.ErrKeyRefused` (a sentinel wrapped with the URL); `FetchModelIDs`/`FetchModelIDsErr` become one-line calls with `key == ""`.
  - `ServedIDs(cfg, client, family)` passes `FamilyAPIKey(cfg, family)` for every family except omlx (omlx keeps its pool path and its own key handling).
  - `func (e *env) probeKeyed(ctx context.Context, url, key string, timeout time.Duration) (responded, timedOut bool)`; `probe` calls it with `""`. `portClosedWithin`, `waitPortOpen` and `waitForModel` gain a `key string` parameter (callers pass the family's key, `""` today for every provider).
  - A family whose key cannot be resolved (`ResolveSecret` error) is reported as `*KeyUnreadableError{Family, Err}` by `ServedIDs`, and inventory shows the family `StatusPartial` with `probeErr` set, never down.

- [ ] **Step 1: Write the failing tests.** `TestFetchModelIDsSendsNoHeaderWithoutAKey` (httptest asserts `Authorization` absent when key is `""`, for `FetchModelIDs`), `TestFetchModelIDsKeyedSendsBearer` (asserts `Bearer k`, returns the ids), `TestFetchModelIDsKeyedRefusal` (server answers 401: the error `errors.Is(err, ErrKeyRefused)`, no ids), `TestServedIDsUsesTheFamilysSecretRef` (a registry with an `mtplx` provider whose `auth.secret_ref` is a literal `k`, server requires `Bearer k`: `ServedIDs` returns the ids; with no secret_ref the same server gives `ErrKeyRefused`), and in lifecycle `TestProbeKeyedSendsBearerAndPlainProbeSendsNone`, `TestWaitForModelWithKey`. Add `TestInventoryShowsAKeyedFamilyPartialNotDown`: server answers 401 to the unkeyed request; the family is `StatusPartial`, not `Down`.
- [ ] **Step 2: Run to verify failure** — `cd wt && go test ./internal/localmodels ./internal/lifecycle -run 'Keyed|NoHeader|SecretRef|PartialNotDown|WithKey'`. Expected: FAIL.
- [ ] **Step 3: Implement** the keyed fetch/probe and thread `key` through the call sites in `server.go` (resolve once per start with `localmodels.FamilyAPIKey(cfg, family)`); a 401/403 with a key present ends `waitForModel` immediately with `*KeyRefusedError{URL, KeySent: true}` (the type `warmup` already returns).
- [ ] **Step 4: Run** — `cd wt && go test ./...`. Expected: PASS; every existing probe test unchanged (they pass `""`).
- [ ] **Step 5: Commit** — `feat(wt): probes carry the provider's key when it has one`.

### Task 5: Command-JSON readiness and the loading wait

**Files:**
- Create: `wt/internal/lifecycle/ready.go`, `wt/internal/lifecycle/ready_test.go`
- Modify: `wt/internal/lifecycle/server.go` (use `waitReady`), `probe.go` (`waitForModel` stays for `ReadyModelsList`)

**Interfaces:**
- Consumes: Task 1 `provider.Ready`, Task 3 `processServer`, `env.run`.
- Produces (exact):
  - `type readyState struct{ Ready bool; Model string }`.
  - `func (e *env) readyNow(ctx context.Context, cfg *config.Config, spec provider.ProcessSpec, bin, modelsURL, model, key string) (readyState, error)`: for `ReadyModelsList` it is `ServedIDs`-style (`Ready` true when the model is listed); for `ReadyCommandJSON` it runs `bin` with `spec.Ready.Command` under a 5 s timeout via `e.run`, parses stdout as a JSON object, and reports `Ready` when the value at `Path` renders as `Equals` (and fills `Model` from `ModelPath`); a non-zero exit, empty output, invalid JSON, a missing path or a timeout is `readyState{}` with a nil error (not ready), never a panic.
  - `func (e *env) waitReady(ctx context.Context, cfg *config.Config, family string, sp procWatch, bin, modelsURL, model, key string, timeout time.Duration) error`: polls `readyNow` every `pollInterval`; returns nil when ready; returns `serve process exited during model load: <err>` the moment `sp.exited()` is true; keeps waiting while the port is closed and the pid is alive; returns the timeout error naming the family on deadline; ctx errors pass through.

- [ ] **Step 1: Write the failing tests** in `ready_test.go` using a fake `e.run`: `TestReadyNowCommandJSON` table — stdout `{"ready":true,"model":"flint"}` → `{true,"flint"}`; `{"ready":false}` → not ready; `{"ready":"true"}` → ready (text compare); empty stdout with exit error → not ready; `not json` → not ready; `{}` → not ready; a run that blocks past the timeout (ctx deadline) → not ready. `TestWaitReadyKeepsWaitingWhilePortClosed`: command reports not ready twice then ready, `sp.exited()` false → nil, three runs. `TestWaitReadyFailsFastWhenProcessDies`: `sp.exited()` true on the second poll → error containing "exited during model load", and fewer than 10 runs. `TestWaitReadyTimeout` with a short `timeout`.
- [ ] **Step 2: Run to verify failure** — `cd wt && go test ./internal/lifecycle -run 'TestReadyNow|TestWaitReady'`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement** `ready.go`; `processServer.start` calls `waitReady` for every kind (its `ReadyModelsList` branch delegating to the existing `waitForModel` semantics so mtplx timing and errors are unchanged); the log-tail suffix on failure stays.
- [ ] **Step 4: Run** — `cd wt && go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** — `feat(wt): command-json readiness and a loading wait for process servers`.

### Task 6: Machine-wide exclusivity and adopting a foreign instance

**Files:**
- Modify: `wt/internal/lifecycle/server.go`, `evictions.go`, `lifecycle.go` (`start`), `wt/internal/provider/provider.go` (nothing new; `Scope` exists)
- Create: `wt/internal/lifecycle/machine_scope_test.go`

**Interfaces:**
- Consumes: Tasks 1, 2, 5.
- Produces (exact):
  - `func (e *env) foreignInstance(ctx context.Context, cfg *config.Config, family string, bin, key string) (st readyState, running bool)` — for a `ScopeMachine` family, runs the readiness command; `running` is true when the command says ready (any port), `st.Model` is the model it serves.
  - In `start`: for a `ScopeMachine` process family, before the port check, `foreignInstance` runs; if `running` and `SameModel(family, st.Model, t.ModelName)` the start is a no-op that returns nil (adopt: no stop, no spawn, no pidfile written); if `running` with another model it is a victim — returned as `OccupiedError` unless `opts.AllowReplace`, and then stopped with the spec's stop command (waiting for the readiness command to report not ready) before the spawn.
  - `Resident`/victims for such an instance use `Entry{ProviderID: <first id of the family>, ModelID: DiscoveredModelID(family, model), ModelName: st.Model, Running: true}`.
  - `TenancyOf`, `Startable` and routes are unchanged; `ScopePort` families (mtplx) never run the readiness command.

- [ ] **Step 1: Write the failing tests** in `machine_scope_test.go` with a synthetic family (test helper builds an `env` whose backend is `processServer` over a spec injected through the `processSpecFor` seam from Task 2): `TestMachineScopeAdoptsAnInstanceServingTheRequestedModel` (readiness command reports `{"ready":true,"model":"flint"}`, start of `flint` returns nil, `spawn` and `stop` never called); `TestMachineScopeAsksBeforeStoppingAnotherModel` (reports `model":"other"`: `start` without `AllowReplace` returns `*OccupiedError` whose occupant model is `other`, nothing stopped); `TestMachineScopeStopsForeignInstanceWhenAllowed` (with `AllowReplace` the stop command runs, then readiness reports not ready, then spawn); `TestPortScopeNeverRunsTheReadinessCommand` (mtplx start records zero readiness-command runs).- [ ] **Step 2: Run to verify failure** — `cd wt && go test ./internal/lifecycle -run 'TestMachineScope|TestPortScope'`. Expected: FAIL.
- [ ] **Step 3: Implement** `foreignInstance` and the three start branches above; `evictions` reports a machine-scope instance as a victim through the same `Resident`/`Occupant` types the port-scope path uses.
- [ ] **Step 4: Run** — `cd wt && go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** — `feat(wt): machine-wide exclusivity; adopt a foreign instance serving the requested model`.

### Task 7: Docs, skill and final verification

**Files:**
- Modify: `wt/docs/internals/local-models.md` (lifecycle section), `wt/docs/internals/packages.md` (new files: `server.go`, `server_loading.go`, `ready.go`), `wt/CLAUDE.md` (the Lifecycle bullets: the mtplx-specific identity rule becomes "a process server"), `wt/.claude/skills/adding-a-provider/SKILL.md` (a process-kind provider is a `Process` row, no backend code), `../docs/guides/08-maintenance-and-troubleshooting.md` only if it names `mtplx_loading.go`

- [ ] **Step 1: Update the docs** above; guides show commands, never live state.
- [ ] **Step 2: Verify no stale names remain** — `grep -rn "mtplxBackend\|MtplxAddressError\|loadingMtplx\|stopLoadingMtplx\|isMtplxServer\|mtplxProc\|mtplx_loading" wt/ ../docs --include='*.go' --include='*.md'` returns only dated plans/specs.
- [ ] **Step 3: Full verification** — `make -C .. lint` then `make -C .. test-all`. Expected: both exit 0.
- [ ] **Step 4: Compare behavior** — build `main`/the base branch and this branch into scratch and run both with every path redirected (`wt/CLAUDE.md` "A built wt that must not touch this machine"): `wt litellm providers`, `wt model list --json`, `wt served mtplx`, `wt stop bogus`; outputs identical apart from temp paths. With a fake `mtplx` script on `PATH` that listens on a scratch port, `wt start`/`wt stop` of an mtplx model behave as on the base branch (spawn argv, pidfile, stop).
- [ ] **Step 5: Commit** — `docs(wt): process-server engine`.
- [ ] **Step 6: Push and open the PR only when the user asks.**

---

## Self-review

- **Spec coverage:** PR 2's list (engine; mtplx migrated; generic loading state; key on every probe; command-json readiness; exclusive scope) → Tasks 1–6; the foreign-instance decision (adopt / ask first) → Task 6; the registry overlay is deferred and flagged in the header.
- **Type consistency:** `ProcessSpec`, `Ready`, `Scope`, `ProcessFor`, `ProcessFamilies` (Task 1) are the only provider names later tasks use; `serverIdent`/`loadingServer`/`stopLoadingServer`/`forgetServer`/`serverProc` (Task 2) and `processServer`/`serverEndpoint`/`PortlessAddressError` (Task 3) are used unchanged in Tasks 4–6; `readyNow`/`waitReady` (Task 5) in Task 6.
- **Proportion:** the plan holds signatures, tests and values; the 550-line mtplx loading code is moved by mechanical renames verified by the existing suite, not re-described.
