# Sushi Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Sushi (`beamivalice/tap/sushi` 1.4.1) a first-class local provider: `wt start|stop|list` and `wt -A claude|pi -M sushi/...` work for the Qwen3.8-Flash-Next pack; `llmbench provider isolate|stop sushi`, `restore` and `llmbench eval|run|agent` rows work serially with ollama; the context is always 131072; a start that cannot work fails early with the fix command before anything else is stopped; mtplx, rec, omlx and ollama stay byte-identical.

**Architecture:** One model-less `sushi serve` per machine as a rec-style process family (`Exclusive`, `ServedOnly`, `ScopePort`, port 12345), declared once in `wt/internal/provider/table.go` and mirrored in `docs/contracts/process-providers.toml` (llmbench auto-registers a `ProcessServerBackend` from it). Four generic engine extensions carry it: E1 loaded-aware served list, E2 `http-get` ready kind, E3 empty `Stop` means "signal the verified pid", E4 per-spec `warmup_seconds`. Grafts: wired-limit preflight, pack-id check, unowned-server handling. The engine extensions land and are proven on synthetic specs FIRST; the sushi row and contract block go live in one commit (Task 16) only when both languages can run them safely.

**Tech Stack:** Go (module root `wt/`, `go 1.26.7`), Python 3.13 (`llmbench/`, uv, ruff, mypy, pytest), TOML contract, Markdown docs.

**Spec:** `docs/superpowers/specs/2026-10-31-sushi-provider-design.md` (approved; its names and values are binding).

---

## Global Constraints

Spec values, verbatim. Any deviation must be raised, not silently chosen.

- Provider id and family id: `sushi`. WtPort `12345`. DefaultOrigin `http://127.0.0.1:12345`. DefaultModelDir `~/.sushi/models`.
- Seed: Name `Sushi`, local, AuthType `none`, BaseURL `http://127.0.0.1:12345/v1`, ModelDir `~/.sushi/models`, Command `sushi`; no Protocols, no secret_ref. Provider Policy `&openaiLocal` (`Prefix "openai/"`, `APIKey "not-needed"`, `V1Base true`).
- Family: Served, ServedOnly, Tenancy `Exclusive`, Scope `ScopePort`, ServeArgs false, RunningOnly false, RoutesFollowArtifact false.
- Process: Binary `sushi`, Subcommand `serve`, PortFlag `--port`, ModelFlag `""`, DaemonModule `""`.
- **Spawn literal** (identical in `table.go`, the contract block and `TestSushiRows`): `serve --port {port} --host 127.0.0.1 --ctx-size 131072 --max-resident-models 1 --no-update-check --max-tokens 32768`. No `{model}`.
- Stop: empty (`nil` in Go, `stop = []` in TOML) = signal the verified pid. Never run the bare binary.
- Pidfile `/tmp/local-ai-setup-sushi.pid`; log `/tmp/local-ai-setup-sushi.log`; PidfileEnv `WT_SUSHI_PIDFILE`.
- Ready: Go `ReadyHTTP` = contract kind `http-get`, `Path "/health"`, no Command. `restore_action = "stop"`.
- `ProcessSpec.WarmupSeconds` / contract key `warmup_seconds`: mtplx 120, rec 120, sushi 400 (explicit, required, positive on every entry).
- Verified-pid stop: SIGTERM to the group, 10 s grace (`e.stopGrace`), SIGKILL only if the same start time remains, wait for port close, then remove pidfile and `.wt`. llmbench: `SIGNAL_STOP_TIMEOUT = 15.0` port-close wait.
- Wired-limit: one integer rule in both languages: `need_MiB = floor(total_safetensors_bytes * 11 / 10 / 1048576)` over the pack dir's `*.safetensors` (49,506,491,792 bytes -> 51,934 MiB; 47,213 MiB of bytes -> 51,934 MiB; no float, no rounding up), `ngram_table.bin` excluded; passes when `limit >= need`. A sysctl value of `0` means the macOS default and reads as `floor(0.75 * hw.memsize / 1048576)` in BOTH languages (read together: `sysctl -n iogpu.wired_limit_mb hw.memsize`); this rule is an addition beyond the design (the design does not say macOS reports 0), confirmed in the acceptance run. Fix text `sudo sysctl iogpu.wired_limit_mb=59000` and "resets on reboot"; every skip path (pack dir unreadable or no safetensors, limit unreadable) prints a one-line `skipping the memory check (<why>)`; never set the sysctl; darwin only.
- Registry identity: model id `sushi/beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw` (wt's derived id: a local id keeps the slash, only cloud ids spell it `--`), `model_name = "beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw"`.
- Seed BaseURL vs registry base_url: the Spec section binds the seed to `http://127.0.0.1:12345/v1`; the Model-identity section and the suite use `http://localhost:12345/v1`. Both address one listener. Code and tests follow the Spec section; the suite file follows Model identity. Docs copy the seed value from `table.go`.
- Hermeticity (both languages): tests never run the real `sushi`, never dial port 12345, never touch `/tmp/local-ai-setup-sushi.pid`. Go uses the package `TestMain` isolation (`lifecycle.IsolateProcessesForTest`, which sets every `ProcessFamilies()` `PidfileEnv`), httptest listeners, registry ports from `provCfg`, and the `processSpecFor`/`familySpecFor` seams via `stubFamily`. Python patches `_pidproc`, `subprocess.run`, `_wired_limit_mb`, and `tests/conftest.py` `_FAKE_BINARIES` gains `sushi`.
- Go: every `Test*` has a top-level `//` comment saying what it tests and why. `gofmt -l .` empty, `go vet ./...` clean. Run from `wt/`: `go test ./... -count=1`.
- Python: 3.13, `ruff format` + `ruff check`, `zip(..., strict=True)` (B905), mypy clean (`make check`), hermetic. Run from `llmbench/`: `uv run pytest -q` and `make check`.
- Commits: conventional (`feat(wt):`, `feat(llmbench):`, `refactor(wt):`, `test(wt):`, `docs:`), imperative, never to `main` (work stays on the sushi-provider branch). Every commit message ends with the trailer line `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.
- Out of scope (do not do): `context_window` registry field (separate PR), `bin/update/sushi`, modeltiers alias, setting the sysctl, `anthropic` protocol, `/v1/unload-model`, editing judge code or `JUDGE_ROUTES` (PR #505), the optional `bytes_resident` improvement.

## Review Focus

The six failure modes most likely to bite that ordinary task tests would not otherwise exercise. Each is pinned by a named test in the owning task.

1. **mtplx/rec regression from the shared engine.** `warm` budget, `tryChat` cap, error text, stop path, and `loaded` filtering must be byte-identical for families without the new facts. Pins: `TestProcessServerWarmupBudgetComesFromTheSpec` + mtplx/rec at 120 (Task 3); `TestMtplxWarmupFailureHasNoLogTailAdded` (Task 7); `TestMtplxWarmupMemoryRefusalGetsNoHint` (Task 9); `TestNonEmptyStopStillRunsTheBinary` (Task 6); `test_a_spec_with_a_stop_command_still_runs_it` (Task 14); `TestServedIDsLoadedFieldPerFamily` (Task 2).
2. **Never execute the bare binary and never signal an unverified pid.** Pins: `TestEmptyStopSignalsTheVerifiedPid`, `...NeverKillsAWrongArgvPid`, `...RefusesAPidOfAnotherUserOrReusedStart`, `...EscalatesToKillOnlyTheSameProcess`, `...WithoutAPidfileSaysNotStartedByWt` (Task 6); `test_empty_stop_stops_through_the_pidfile_and_never_runs_the_binary`, `test_live_pid_*` (Task 14). The Python guarantee is weaker than Go's and is documented as such: `PidfileProcess` checks only that "sushi" appears in the pid's `ps` line (and is fail-open when `ps` cannot answer); there is no uid/start-time/argv check on the llmbench side.
3. **The `loaded` flag semantics.** Only a literal JSON `false` drops an entry; absent, `null` and a non-bool such as `"false"` do not (Go decodes `loaded` as `json.RawMessage` so one odd entry cannot fail the whole list; Python tests `is False`). The pack-id check must use an UNFILTERED list, or an unloaded pack looks missing. Pins: `TestFetchModelIDsKeyedLoadedFilter` (Task 2), `test_http_models_ids_drops_only_entries_explicitly_not_loaded` (Task 12), `TestSushiStartRejectsAnUnlistedPackBeforeWarmup` (Task 7).
4. **Contract coupling across languages.** A contract file with the sushi block auto-registers a backend and flips every literal provider list; an unknown ready kind or a missing key disables ALL llmbench process providers; the Go contract test and the Python parser change together. Pins: `test_one_malformed_entry_disables_every_process_provider` (Task 11), `TestProcessProvidersContractMatchesTheProviderTable` (Tasks 1, 16), single-commit rule of Task 16.
5. **Hermeticity with a real sushi installed on the dev machine.** Pins: `TestMainIsolatesEveryProcessPidfile` (Task 16), `TestSushiNeverTouchesTheRealPidfile` (Task 17), hermeticity cases + `_FAKE_BINARIES` + the autouse sushi-pidfile redirect `test_the_suite_redirects_the_sushi_pidfile` (Task 14), the recording-shim proof (Task 24). llmbench has no pidfile override env, so the autouse fixture replaces the frozen `_pidproc` of every signal-stop backend with one on a tmp path.
6. **Fail-early ordering.** The wired-limit failure and the unowned-server refusal must precede any stop of another provider. Pins: `TestPreflightRunsBeforeAnyOtherProviderIsStopped` (Task 8), `test_a_failed_preflight_stops_no_other_provider` and `test_the_refusal_happens_before_any_other_provider_is_stopped` (Tasks 14, 15).

## Task order and why

Engine extensions are proven with synthetic specs (`sushix` family through `stubFamily`; `_spec()` in Python) before the sushi row exists, so every commit is green and nothing can execute a bare `sushi`. Task 16 is the only commit where sushi becomes real.

| # | Task | Area |
|---|---|---|
| 1 | Provider spec: WarmupSeconds, ReadyHTTP, invariants | wt |
| 2 | Loaded-aware served-list decoder (E1) | wt |
| 3 | tryChat timeout and per-spec warmup budget (E4) | wt |
| 4 | `ReadyHTTP` readiness (E2) | wt |
| 5 | Extract the verified-pid signal helper | wt |
| 6 | Empty Stop signals the verified pid (E3) | wt |
| 7 | Pack-id validation, failed-start cleanup, fake sushi helper | wt |
| 8 | Wired-limit preflight | wt |
| 9 | Memory-refusal hint | wt |
| 10 | Unowned-server warning | wt |
| 11 | llmbench contract parser | llmbench |
| 12 | llmbench probe primitives | llmbench |
| 13 | llmbench backend: http-get, loaded, warm budget | llmbench |
| 14 | llmbench empty stop, ownership, hermetic guards | llmbench |
| 15 | llmbench wired-limit preflight | llmbench |
| 16 | Sushi goes live (wt row + contract + llmbench registration) | both |
| 17 | Loaded confirmation and real-row conformance | wt |
| 18 | Sushi loaded-flag pins (inventory, start, `wt served`) | wt |
| 19 | Help strings | wt |
| 20 | Eval suite and shipped-suite test | llmbench |
| 21 | Engineering docs | docs |
| 22 | User docs | docs |
| 23 | Skills and changelog | docs |
| 24 | Final verification | all |

---

## Task 1: Provider spec fields (WarmupSeconds, ReadyHTTP) and invariants

No behaviour change for existing providers; no sushi row.

**Files:**
- Modify `wt/internal/provider/provider.go` lines 134-171: add `ReadyHTTP ReadyKind = "http-get"` to the const block (137-141); document `Ready.Path` as also "URL path (http-get)" (144-157); add `WarmupSeconds int` to `ProcessSpec` (159-171) with a comment (budget in seconds for the readiness-then-warmup request; contract key `warmup_seconds`). `clone()` (212-221) copies by value: no change.
- Modify `wt/internal/provider/table.go`: `WarmupSeconds: 120,` on the mtplx Process (after Ready, ~127) and the rec Process (after the Ready block, ~153).
- Modify `wt/internal/provider/provider_test.go`: TestTableInvariants (46-72), TestProcessSpecMtplx (326-353), TestRecRows (369-394).
- Modify `docs/contracts/process-providers.toml` (mtplx entry 13-33, rec entry 35-55): `warmup_seconds = 120` as the last scalar key, BEFORE `[process.ready]` (a key after the table header lands in the ready table). Add a header-comment sentence: "warmup_seconds is the per-spec budget for the readiness and warmup request."
- Modify `wt/internal/config/process_providers_contract_test.go` (26-45 struct, 118-128 comparisons).

**Interfaces:**
- Consumes: nothing.
- Produces: `provider.ProcessSpec.WarmupSeconds int`; `provider.ReadyHTTP ReadyKind = "http-get"` (`Ready.Path` starts with `/`, `Ready.Command` empty); contract key `warmup_seconds` (int); test helper `processSpecProblems(pr ProcessSpec) []string` in `provider_test.go`.

- [ ] **Step 1: Write the failing tests.** In `provider_test.go` add (each Test with a top-level comment):
  - `processSpecProblems(pr ProcessSpec) []string` returning: `"WarmupSeconds must be positive"` when `<= 0`; for `Ready.Kind == ReadyHTTP`: `"http-get ready needs a Path starting with /"` when Path lacks the `/` prefix, `"http-get ready takes no Command"` when Command non-empty; `"empty Stop needs PidfileDefault, Subcommand and PortFlag"` when `len(Stop)==0` and any of the three is empty.
  - `TestProcessSpecProblems` (synthetic specs, since no table row has http-get or an empty Stop until Task 16). Base `ok := ProcessSpec{WarmupSeconds: 400, PidfileDefault: "/tmp/x.pid", Subcommand: "serve", PortFlag: "--port", Ready: Ready{Kind: ReadyHTTP, Path: "/health"}}` (Stop nil). Cases and expected problem counts: valid=0; WarmupSeconds 0 =1; Path "" =1; Path "health" =1; Command `[]string{"status"}` =1; PidfileDefault "" =1; Subcommand and PortFlag "" =1; `Stop=[]string{"stop"}` with those three cleared =0. Assert `len(processSpecProblems(p)) == want`.
  - In `TestTableInvariants`, after the `pr.PidfileDefault == ""` check (line ~55) add `for _, p := range processSpecProblems(*pr) { t.Errorf("family %q: %s", f, p) }` and a `case ReadyHTTP:` arm (empty body) in the Ready switch (63-71) so the `default:` arm still rejects unknown kinds.
  - Add `WarmupSeconds: 120,` to the `want` literals in `TestProcessSpecMtplx` and `TestRecRows`.
  - In the contract test struct add `WarmupSeconds int \`toml:"warmup_seconds"\`` after `RestoreAction`, and after the `restore_action` comparison (~125) `str("warmup_seconds", strconv.Itoa(e.WarmupSeconds), strconv.Itoa(spec.WarmupSeconds))`.
- [ ] **Step 2: Run to see failure.** `cd wt && go test ./internal/provider ./internal/config -run 'TestProcessSpecProblems|TestTableInvariants|TestProcessSpecMtplx|TestRecRows|TestProcessProvidersContract' -count=1`. Expected FAIL: `undefined: ReadyHTTP`, `unknown field WarmupSeconds in struct literal`.
- [ ] **Step 3: Implement** the const, the field, `WarmupSeconds: 120` on mtplx and rec, and `warmup_seconds = 120` in both TOML entries (same commit as the Go table, as the contract header demands). No sushi row.
- [ ] **Step 4: Run green.** `cd wt && gofmt -l . && go vet ./... && go test ./... -count=1`; then `cd ../llmbench && uv run pytest tests/providers -q` (the Python parser reads named keys only, so the extra key is ignored until Task 11).
- [ ] **Step 5: Commit** `feat(wt): per-spec warm-up budget and an http-get readiness kind in the process spec`

**Review focus:** `warmup_seconds` sits before `[process.ready]` in both entries; mtplx/rec `ProcessFor` otherwise unchanged.

---

## Task 2: Loaded-aware served-list decoder (E1)

One production change in `FetchModelIDsKeyed`. Audit result (verified): it is the only Go decoder of `/v1/models` ids; `ServedIDs` (inventory, `liveServed`, `startedGone`, `runServed`), `readyNow`, `waitForModel`, and the omlx fallback all reach it, so no call site changes. `inventory.go:184` discards ids and tests only the error.

**Files:**
- Modify `wt/internal/localmodels/match.go` 33-96: the anonymous `Data` struct (~79-83) gains `Loaded json.RawMessage \`json:"loaded"\`` (not `*bool`: a typed bool would fail the whole body on one malformed entry and break mtplx/ollama/omlx listings); the loop skips `d.ID == "" || strings.TrimSpace(string(d.Loaded)) == "false"`. Update the doc comments of `FetchModelIDs` (33-36) and `FetchModelIDsKeyed` (59-62). Signature unchanged.
- Modify tests: `wt/internal/localmodels/match_test.go`, `served_test.go` (after 203-215), `wt/internal/lifecycle/probe_test.go` (after TestWaitForModel, 125-160), `ready_test.go` (next to TestReadyNowCommandJSON), `confirm_test.go` (39-82).

**Interfaces:**
- Consumes: nothing.
- Produces: `localmodels.FetchModelIDsKeyed(client *http.Client, url, key string) ([]string, error)` (unchanged signature; entries with explicit `loaded:false` are omitted; absent/null/true kept in listed order); lifecycle test helper `loadedSrv(t *testing.T, loaded map[string]bool) string` in `confirm_test.go`.

- [ ] **Step 1: Failing tests.**
  - `TestFetchModelIDsKeyedLoadedFilter` (comment: pins E1; explicit false is not served, absent/null/true are). Table over bodies, `slices.Equal(ids, want)`, err nil: `{"data":[{"id":"a"},{"id":"b"}]}` -> [a b]; `loaded:true` -> [a]; `loaded:false` -> [] (len 0, err nil: answered, serving nothing); mixed `[a f, b t, c absent, "" t]` -> [b c]; `{"id":"a","loaded":false,"state":"loading"}` -> []; `{"loaded":true,"state":"ready"}` -> [a]; `{"loaded":false,"state":"unloaded"}` -> []; `{"state":"unloaded"}` without loaded -> [a] (state is never consulted); `"loaded":null` -> [a]; `"loaded":"false"` (a string, not a bool) -> [a] (only a literal `false` filters; the body still decodes); `"loaded":0` -> [a].
  - `TestFetchModelIDsLoadedFilterReachesEveryWrapper`: body `[on t, off f]` -> `FetchModelIDs` and `FetchModelIDsErr` both [on].
  - `TestServedIDsLoadedFieldPerFamily` (served_test.go): mtplx, mlx_lm_server, rec with cfg built as in `TestServedIDsOtherFamiliesReadTheList`: body without `loaded` -> [Org/Q]; with `loaded:true` -> [Org/Q]; with `loaded:false` -> [] (the filter is generic, documented in the test comment); add an ollama case (without `loaded` -> its ids unchanged). omlx via existing `fakeOmlx{listed: []string{"A"}, pool: map[string]bool{"A": true}}` -> [A] (branch unchanged).
  - `TestWaitForModelIgnoresUnloadedEntries` (probe_test.go): two hits `loaded:false,state:loading`, then `loaded:true,state:ready`; `waitForModel(ctx, srv.URL, "Qwen", nil, "", time.Second) == nil`, hits >= 3; always-false server -> error containing `timed out` (60 ms timeout).
  - `TestReadyNowModelsListIgnoresUnloadedEntries` (ready_test.go; construct the spec exactly like neighbouring cases): `loaded:false` -> `readyState{}` nil err; `loaded:true` -> Ready true, Model `org/Qwen`; absent -> Ready true.
  - confirm_test.go: add `loadedSrv` and table rows to `TestStartedGoneDecidesPerFamily`: mtplx `loaded:true` -> `""`; mtplx `loaded:false` -> `"is no longer serving it"`.
- [ ] **Step 2: Run.** `cd wt && go test ./internal/localmodels/ ./internal/lifecycle/ -run 'LoadedFilter|LoadedFieldPerFamily|IgnoresUnloaded|StartedGoneDecides' -count=1`. Expected FAIL on the false/mixed/loading/unloaded cases and the lifecycle tests.
- [ ] **Step 3: Implement** as above. Do not touch `served.go`, `inventory.go`, `ready.go`, `probe.go`, `confirm.go`, `model_cmds.go`.
- [ ] **Step 4: Run green.** `cd wt && gofmt -l . && go vet ./... && go test ./... -count=1`.
- [ ] **Step 5: Commit** `feat(wt): skip unloaded entries in the served-models decoder`

**Review focus:** only a literal false drops an entry; an answer listing only `loaded:false` packs yields an empty slice with nil error (family OK, not Partial).

---

## Task 3: tryChat timeout and per-spec warmup budget (E4)

**Files:**
- Modify `wt/internal/lifecycle/probe.go` 154-229; `wt/internal/lifecycle/server.go` 147-156 (the line-155 `120*time.Second`); tests `probe_test.go` (265-283), `server_test.go` (near TestMtplxStartSpawnsWaitsWarms ~136).

**Interfaces:**
- Consumes: `provider.ProcessSpec.WarmupSeconds` (Task 1).
- Produces: `const chatRequestTimeout = 120 * time.Second`; `func (e *env) tryChat(ctx context.Context, chatURL, key string, payload []byte, timeout time.Duration) (ok, refused bool, reason string)`; `func (e *env) warmupFor(ctx context.Context, chatURL, model, healthURL, key string, timeout, requestTimeout time.Duration) error`; `warmup(...)` keeps its signature and equals `warmupFor(..., timeout, chatRequestTimeout)` (so `ollama.go:204` and `omlx.go:55` are untouched); `func warmupBudget(spec provider.ProcessSpec) (time.Duration, error)` = `WarmupSeconds` seconds, or `fmt.Errorf("%s: process spec has no warmup_seconds", spec.Binary)` when `<= 0`; helper env var `LIFECYCLE_HELPER_CHAT_STATUS` (default 200) read in `helperMtplx`'s chat handler.

- [ ] **Step 1: Failing tests.**
  - `TestTryChatHonoursItsTimeout` (probe_test.go): handler sleeps 300 ms then writes `{"object":"chat.completion"}`; 50 ms cap -> `ok=false`, reason mentions deadline/timeout; 5 s cap -> `ok=true`. Update the existing call at :277 to pass `chatRequestTimeout`.
  - `TestProcessServerWarmupBudgetComesFromTheSpec` (server_test.go): `stubFamily(t, "sushix", spec, provider.FamilySpec{Name:"sushix", Tenancy: provider.Exclusive, Scope: provider.ScopePort}, port)` with `spec = {Binary:"sushix", Subcommand:"serve", PortFlag:"--port", Spawn:["serve","--model","{model}","--port","{port}","--host","127.0.0.1","--model-id","{model}"], Ready:{Kind: ReadyModelsList}, WarmupSeconds: 1}`; copy `mtplxEnv`, keyed `sushix`, `LIFECYCLE_HELPER=mtplx`, `LIFECYCLE_HELPER_CHAT_STATUS=503`, register `processServer{"sushix"}` in `e.backends` as `mtplxEnv` does. Under a 20 s context assert start returns an error containing `failed to warm up model` in under ~5 s. Second case `WarmupSeconds = 0`: error containing `no warmup_seconds`, and no pidfile or `.wt` left after cleanup.
- [ ] **Step 2: Run.** `cd wt && go test ./internal/lifecycle/ -run 'TestTryChat|TestProcessServerWarmupBudget' -count=1`. Expected FAIL: `too many arguments in call to e.tryChat` / `undefined: chatRequestTimeout`.
- [ ] **Step 3: Implement.** In `server.go` after `report(StageWarming)`: `budget, berr := warmupBudget(spec); if berr != nil { return berr }; return e.warmupFor(ctx, origin+"/v1/chat/completions", t.ModelName, models, key, budget, budget)`. The named result keeps `defer sp.kill()` firing. The budget check is at warmup time, NOT at the top of `start` (machine_scope tests with specs lacking WarmupSeconds must still end at `errSpawnStub`). mtplx and rec: 120/120, identical to before.
- [ ] **Step 4: Run green.** `cd wt && go vet ./internal/lifecycle/ && go test ./... -count=1`.
- [ ] **Step 5: Commit** `feat(wt): per-spec warmup budget and tryChat timeout parameter`

**Review focus:** ollama/omlx still go through `warmup` with the 120 s per-request cap; `WarmupSeconds <= 0` fails closed at warmup only.

---

## Task 4: ReadyHTTP readiness (E2)

**Files:**
- Modify `wt/internal/lifecycle/ready.go` 26-79 (add `case provider.ReadyHTTP` to `readyNow` before `default`; update the doc comment 26-37); tests in `ready_test.go` (reuse `stubReadySpec` 30-40 and `countingProc` 42-55).

**Interfaces:**
- Consumes: `provider.ReadyHTTP`, `Ready.Path` (Task 1).
- Produces: `readyNow` handles `ReadyHTTP`; `func healthURL(modelsURL, path string) (string, error)` in `ready.go` (parse, set `u.Path = path`, clear `RawQuery` and `Fragment`). Semantics: GET it with `e.probeClient`, `Authorization: Bearer <key>` only when key non-empty; status exactly 200 -> `readyState{Ready:true}`; any other status or connection error -> `readyState{}` with nil error; 401/403 with a key -> `*KeyRefusedError` like the models-list case. `waitReady` needs no change (empty `Ready.ModelPath` means success on Ready).

- [ ] **Step 1: Failing tests.**
  - `TestReadyNowHTTPGet`: family `httpx`, spec `{Binary:"httpx", Ready:{Kind: ReadyHTTP, Path:"/health"}}` via a stubbed `processSpecFor`. `/health` 200 -> ready even though `/v1/models` lists nothing; `/health` 503 -> not ready; only `/v1/models` served (404 on `/health`) -> not ready; closed port -> not ready nil error; the models URL's path/query are ignored.
  - `TestReadyNowHTTPGetSendsTheKey`: `Authorization: Bearer k` with key `"k"`, header absent with `""`.
  - `TestWaitReadyHTTPGetPollsUntil200`: 503, 503, 200 with `countingProc{exitAt:0}` returns nil; with `countingProc{exitAt:2}` and an always-503 server ends at once with `serve process exited during model load`.
- [ ] **Step 2: Run.** `cd wt && go test ./internal/lifecycle/ -run 'TestReadyNowHTTPGet|TestWaitReadyHTTPGet' -count=1`. Expected FAIL: `unknown readiness kind "http-get"`.
- [ ] **Step 3: Implement** the case and `healthURL`; leave models-list and command-json untouched.
- [ ] **Step 4: Run green.** `cd wt && go test ./internal/lifecycle/ -count=1`.
- [ ] **Step 5: Commit** `feat(wt): http-get readiness kind for process families`

---

## Task 5: Extract the verified-pid signal helper (pure refactor)

**Files:**
- Modify `wt/internal/lifecycle/server_loading.go` 282-345: split `stopLoadingServer` (289) into the pid-matching preamble (290-301) and a new helper holding 302-344. Test in `server_loading_test.go`.

**Interfaces:**
- Consumes: nothing.
- Produces: `func (e *env) signalVerifiedServer(ctx context.Context, cfg *config.Config, spec provider.ProcessSpec, family string, id serverIdent) error`. Moved verbatim: target `-pid` when `id.group` else `pid`; SIGTERM (ESRCH tolerated); `waitGone(id, e.stopGrace, "SIGTERM")`; if the same start time remains and not a zombie, re-describe and `notServer(spec, info, serverPort(cfg,family), id.start)` must still pass before SIGKILL (group only if `id.group && info.pgid==pid`); an `exiting` process gets the second wait with no SIGKILL; second `waitGone(e.stopTimeout)`; on confirmed gone `e.forgetServer(family, id.pid)` and nil; every other outcome keeps its error text and leaves the pidfile. `stopLoadingServer(ctx, cfg, family string, pid int) error` keeps its signature: spec lookup (UnsupportedError), `loadingServer`, `l.Err`, the `pid <= 0 || l.PID != pid` refusal (same message), then `return e.signalVerifiedServer(...)`.

- [ ] **Step 1: Characterisation pin.** Existing guards: the `TestStopLoadingMtplx*` suite in `server_loading_test.go` and `loading_darwin_test.go`. Add `TestSignalVerifiedServerIsWhatStopLoadingServerRuns` (comment: the helper signals exactly the group of a recorded verified server and removes pidfile and start record): `loadingEnv(t, true)`, `id, _ := e.loadingServer(loadCfg(), "mtplx")`, `spec, _ := processSpecFor("mtplx")`, call the helper; assert `pt.signals == ["-4242:15"]` and `filesLeft` reports both gone.
- [ ] **Step 2: Run.** `cd wt && go test ./internal/lifecycle/ -run TestSignalVerifiedServerIsWhatStopLoadingServerRuns -count=1`. Expected FAIL: `e.signalVerifiedServer undefined`.
- [ ] **Step 3: Move the code.** Do not change messages, grace values, or the order of describe/signal calls.
- [ ] **Step 4: Run green.** `cd wt && go test ./internal/lifecycle/ ./cmd/wt/ ./internal/survey/ -count=1` (cmd/wt and survey call `StopLoading`), then `go test ./... -count=1`.
- [ ] **Step 5: Commit** `refactor(wt): extract the verified-pid signal path from stopLoadingServer`

**Review focus:** error strings and ordering byte-identical so no existing StopLoading test is edited; SIGKILL stays gated by a fresh `notServer` check.

---

## Task 6: Empty Stop signals the verified pid (E3, Go)

**Files:**
- Modify `wt/internal/lifecycle/server_loading_test.go` (the `...For` helpers). Modify `wt/internal/lifecycle/server.go` 158-187 (`processServer.stop`: new branch BEFORE the `lookPath` at :163); `server_loading.go` (add `stopByPid` next to `forgetStopped`, 363-379); `lifecycle.go` ~228-232 (new `NotStartedByWtError` beside `PortBusyError`); tests `server_loading_test.go` (after `TestMtplxStopThatFailsLeavesThePidfile` ~868; fixture next to `stopEnv` ~735).

**Interfaces:**
- Consumes: `signalVerifiedServer` (Task 5).
- Produces: `type NotStartedByWtError struct{ Provider string; Port int; Detail string }` with `Error()` = `"<Provider> at port <Port> was not started by wt; stop it yourself"` plus `": <Detail>"` when Detail is non-empty (Detail carries `Loading.Stray`); `func (e *env) stopByPid(ctx context.Context, cfg *config.Config, spec provider.ProcessSpec, family string) error`: (1) `id, l := e.loadingServer(cfg, family)`; `l.Err != nil` -> return it; (2) `l.PID == 0`: one `probeKeyed(ctx, modelsURL, key, e.stopTimeout)`; port closed (not responded, not timed out) -> nil (idempotent, pidfile untouched), else `&NotStartedByWtError{...Detail: l.Stray}`; (3) `l.PID > 0`: `signalVerifiedServer`, then `portClosedWithin(ctx, models, key, e.stopTimeout)`; not closed -> `fmt.Errorf("%s still listening on port %d", spec.Binary, port)`. `processServer.stop`: after `spec, err := b.spec()`, `if len(spec.Stop) == 0 { _, models, port := serverEndpoint(cfg, b.family); return b.stopByPid(...) }`. mtplx/rec (non-empty Stop) take the unchanged path. `stopModel` delegates to `stop`, so Exclusive replacement, `wt stop`, StopBatch and restore all get it.

- [ ] **Step 1: Failing tests.** Family-aware helpers (the existing `filesLeft`, `writePidfile`, `writeStartRecord` in `server_loading_test.go` are hard-wired to `e.serverProc["mtplx"]` and would pass vacuously for `sushix`): add `filesLeftFor(e *env, family string) (pidfile, record bool)`, `writePidfileFor(t, e, family, content)` and `writeStartRecordFor(t, e, family, content)` (same bodies, keyed by `family`) and turn the three mtplx helpers into one-line wrappers passing `"mtplx"`. Tasks 6, 7 and 17 use ONLY the `...For` helpers with their family. Fixture `sigEnv(t, recorded bool) (*procTable, *env, *config.Config, int)` (when `recorded`, seed the pidfile and `.wt` record with `writePidfileFor`/`writeStartRecordFor(..., "sushix", ...)`): `stubFamily(t, "sushix", spec, facts, port)` with `spec = {Binary:"sushix", Subcommand:"serve", PortFlag:"--port", Spawn:["serve","--port","{port}"], Stop:nil, Ready:{Kind: ReadyHTTP, Path:"/health"}, WarmupSeconds:5}`, Exclusive/ScopePort; `procTable.env(t)` plus `e.serverProc["sushix"] = pidProcess{...}` under the test dir; `procs[4242] = mine(4242,"100.5",[]string{"/opt/homebrew/bin/sushix","serve","--port",<port>,"--host","127.0.0.1"})`; `e.lookPath` and `e.run` are set to `t.Fatal("the binary must never be looked up or run")`; an httptest server on the port closes in `procTable.onSignal`. Tests (each with a top-level comment):
  - `TestEmptyStopSignalsTheVerifiedPid`: recorded -> `signals == ["-4242:15"]`, port closed, `filesLeftFor(e,"sushix")` is false,false (and a precondition check asserts it was true,true before the stop, so the assertion is not vacuous), `e.run` never called.
  - `TestEmptyStopWithPidOnlyPidfileSignalsThePid`: not recorded -> `["4242:15"]` (documented consequence: `wt stop` also stops a llmbench-started sushi, without start-time protection against pid reuse).
  - `TestEmptyStopNeverKillsAWrongArgvPid`: argv `vim sushix serve --port N` or wrong port -> error text contains `left alone`, no signals, files kept.
  - `TestEmptyStopRefusesAPidOfAnotherUserOrReusedStart`: uid+1, and start `999.9` vs record `100.5` -> no signal.
  - `TestEmptyStopWithoutAPidfileSaysNotStartedByWt`: server answering, no pidfile -> `errors.As(&NotStartedByWtError)`, message contains `not started by wt; stop it yourself`, no signals.
  - `TestEmptyStopWithNothingRunningIsANoOp`: no pidfile, port closed -> nil.
  - `TestEmptyStopEscalatesToKillOnlyTheSameProcess`: SIGTERM ignored, deleted on 9; signals `["-4242:15","-4242:9"]` (pattern of `TestStopLoadingMtplxKillsOnlyTheSameProcess` :434).
  - `TestEmptyStopThatLeavesThePortOpenFails`: process gone, foreign listener still answering -> `still listening on port`.
  - `TestNonEmptyStopStillRunsTheBinary`: real mtplx spec via `stopEnv`; `e.run` called with `stop --port N --grace-seconds 10`, signals empty.
- [ ] **Step 2: Run.** `cd wt && go test ./internal/lifecycle/ -run 'TestEmptyStop|TestNonEmptyStop' -count=1`. Expected FAIL: the `t.Fatal` from `e.run` (stop currently runs `sushix` with empty args).
- [ ] **Step 3: Implement** `NotStartedByWtError`, `stopByPid`, and the branch exactly per the Interfaces block.
- [ ] **Step 4: Run green.** `cd wt && go test ./internal/lifecycle/ ./cmd/wt/ ./internal/survey/ -count=1`; then `go test ./... -count=1`. Also grep `cmd/wt` and `survey` for type switches over lifecycle error types so the new error prints its text.
- [ ] **Step 5: Commit** `feat(wt): an empty stop signals the verified pid instead of running the binary`

**Review focus:** the branch precedes `lookPath` (a sushi that left PATH can still be stopped, and is never executed); no pidfile + open port is an error, no pidfile + closed port is nil.

---

## Task 7: Pack-id validation, failed-start cleanup, log tail; fake sushi helper

**Files:**
- Create `wt/internal/lifecycle/sushi_pack.go`; modify `server.go` 147-156 (between `report(StageWarming)` and `warmupFor`); modify `server_test.go` 28-90 (TestMain gains `if os.Getenv("LIFECYCLE_HELPER") == "sushi" { helperSushi(); return }`; `helperSushi` beside `helperMtplx`); create `sushi_test.go`.

**Interfaces:**
- Consumes: `warmupFor`, `warmupBudget` (Task 3); `readyNow` ReadyHTTP (Task 4).
- Produces: `var ErrPackNotListed` sentinel and `type PackNotListedError struct{ Provider, Model string; Listed []string }` (Error: `<model> is not listed by <provider> at <modelsURL> (it lists: a, b); register the pack's exact id as model_name`); `func listedPackIDs(client *http.Client, modelsURL, key string) ([]string, error)` (local decoder returning EVERY id regardless of `loaded`; 401/403 -> `*KeyRefusedError`); `func (e *env) requirePackListed(ctx context.Context, provider, modelsURL, model, key string) error` using EXACT string equality (Sushi loads by exact id and answers 503 for anything else, so the lenient `localmodels.NameMatches` suffix rule would let `Qwen3.8-...` pass against a listed `beamster/Qwen3.8-...`). In `processServer.start`, ONLY when `spec.Ready.Kind == provider.ReadyHTTP`: call `requirePackListed` before `warmupFor`; on a `warmupFor` or `requirePackListed` failure (not `context.Canceled`) append `; log tail: <proc.logTailLines(512) trimmed>` exactly as the waitReady failure does (server.go:149-151). `helperSushi()` (selected by `LIFECYCLE_HELPER=sushi`): prints the line `sushi helper ready` to stderr at startup (so the log tail is non-empty; the log tail is only appended when `logTailLines(512) != ""`); scans `os.Args` for `--port` and `--host` (the sushi argv has bare flags, so `helperMtplx`'s pair parser is not reusable); `helperMtplx` gains the same stderr line when `LIFECYCLE_HELPER_LOG_LINE` is set (default unset, so existing tests are unchanged), writes pid + argv to `LIFECYCLE_ARGV_FILE`; serves `GET /health` 200 `{"status":"ok"}`, `GET /v1/models` `{"data":[{"id":<LIFECYCLE_SUSHI_PACK>,"loaded":<LIFECYCLE_SUSHI_LOADED, default true>}]}`, `POST /v1/chat/completions` with status `LIFECYCLE_HELPER_CHAT_STATUS` (default 200; body `{"object":"chat.completion"}` on 200, else `{"error":"No default model configured"}`) and appends `chat` to `LIFECYCLE_SUSHI_LOG` on each POST; exits 0 on SIGTERM. Fixture `sushiEnv(t, pack string, chatStatus int) (*env, *config.Config, int, string)`: the `mtplxEnv` pattern keyed `sushix`, spec as in Task 6 plus Spawn `serve --port {port} --host 127.0.0.1 --ctx-size 131072 --max-resident-models 1 --no-update-check --max-tokens 32768`, `WarmupSeconds: 2`, `e.lookPath` returning `os.Args[0]`.

- [ ] **Step 1: Failing tests** (`sushi_test.go`, top-level comments):
  - `TestSushiStartHappyPath`: stages `starting`, `waiting-for-model`, `warming`; helper argv line equals `serve --port <p> --host 127.0.0.1 --ctx-size 131072 --max-resident-models 1 --no-update-check --max-tokens 32768`; pidfile holds the helper pid.
  - `TestSushiStartRejectsAnUnlistedPackBeforeWarmup`: helper lists X, `Target.ModelName` Y, chat would return 503: error is `*PackNotListedError` listing X; the `LIFECYCLE_SUSHI_LOG` file is absent (no POST was made).
  - `TestSushiWarmupFailureKillsOnlyItsOwnProcessAndCleans`: chat 503, `WarmupSeconds` 1 -> error contains `HTTP 503` and `log tail`; helper pid gone (`pidGone`); `filesLeftFor(e,"sushix")` false,false; a second unrelated `exec.Command("sleep","60")` started in-test (killed in Cleanup) is still alive afterward.
  - `TestSushiCancelledStartTearsDown`: cancel during warmup, same cleanup assertions.
  - `TestSushiRequirePackListedIsExact`: helper lists `beamster/Q`, `Target.ModelName` is the bare `Q` (and the reverse: listed `Q`, name `beamster/Q`): both give `*PackNotListedError`, no chat POST made.
  - `TestMtplxWarmupFailureHasNoLogTailAdded`: `mtplxEnv` with `LIFECYCLE_HELPER_CHAT_STATUS=503` and `LIFECYCLE_HELPER_LOG_LINE=mtplx helper ready` (so the log is non-empty and only the ReadyHTTP gating keeps the tail out); the error does not contain `log tail`.
- [ ] **Step 2: Run.** `cd wt && go test ./internal/lifecycle/ -run 'TestSushi|TestMtplxWarmupFailure' -count=1`. Expected FAIL: compile errors for `PackNotListedError`/`helperSushi`, then a generic `failed to warm up model` instead of `*PackNotListedError`.
- [ ] **Step 3: Implement** `sushi_pack.go`, the helper, and the two gated additions in `start()`.
- [ ] **Step 4: Run green.** `cd wt && go test ./internal/lifecycle/ -count=1`.
- [ ] **Step 5: Commit** `feat(wt): validate the pack id against /v1/models before the sushi warmup`

**Review focus:** the pack check and log tail are gated on `ReadyHTTP` so mtplx/rec text is unchanged; the unfiltered decoder is deliberate (Review Focus 3).

---

## Task 8: Wired-limit preflight

**Files:**
- Create `wt/internal/lifecycle/wiredlimit.go` (pure logic), `wiredlimit_darwin.go` (`//go:build darwin`, sysctl reader), `wiredlimit_other.go` (`!darwin`: returns `(0,false,nil)`), `wiredlimit_test.go`.
- Modify `env.go` 21-116 (field `wiredLimit func() (mb int64, ok bool, err error)` wired to `readWiredLimit` in `defaultEnv`); `procinfo.go` 93-110 (`IsolateProcessesForTest` also closes the seam: `readWiredLimit` returns an error); `lifecycle.go` 475-511 (call the optional preflight right after the adopt check, 498-502, before residents 503-511, so before the OccupiedError question, `stopOthers` 537 and the occupant stop 540-552); `probe_test.go` `testEnv()` sets `e.wiredLimit = func() (int64,bool,error){return 0,false,nil}`.

**Interfaces:**
- Consumes: `localmodels.ModelDir(cfg, family) (string, error)`; `stubFamily`.
- Produces: `var readWiredLimit = realReadWiredLimit` (darwin: `realReadWiredLimit`); `func packResidentBytes(packDir string) (bytes int64, ok bool)` (sum of regular-file sizes of `*.safetensors` directly in the dir; `ok=false` when unreadable or none); `func wiredNeedMiB(packBytes int64) int64` = `packBytes * 11 / 10 / (1 << 20)` (integer floor; 49,506,491,792 -> 51934; same as the Python rule); `func parseWiredLimit(out string) (mb int64, ok bool, err error)` (pure: two lines `<iogpu.wired_limit_mb>` then `<hw.memsize bytes>`; a first line of `0` means the macOS default and returns `(memsize*3/4)>>20`, an error if memsize is missing; unparsable -> error); `realReadWiredLimit` runs ONE call `sysctl -n iogpu.wired_limit_mb hw.memsize` (5 s timeout) and returns `parseWiredLimit(out)`; `type WiredLimitError struct{ Pack string; LimitMB, NeedMB int64 }` with `Error()` = `iogpu.wired_limit_mb is %d MiB but %s needs about %d MiB resident; raise it with: sudo sysctl iogpu.wired_limit_mb=59000 (it resets on reboot)`; `func wiredLimitHint() string` = `sudo sysctl iogpu.wired_limit_mb=59000 (resets on reboot)`; `var wiredLimitFamilies = map[string]bool{"sushi": true}` (the design names no spec flag, so the gate is by family id); `type preflighter interface{ preflight(ctx context.Context, e *env, cfg *config.Config, t Target) error }` (optional; `fakeBackend` does not implement it). `processServer.preflight`: not in `wiredLimitFamilies` -> nil; `lookPath(spec.Binary)` failure -> `*BinaryMissingError`; `localmodels.ModelDir` error -> nil; `packResidentBytes(filepath.Join(dir, t.ModelName))` not ok -> `routePrintf(ctx, "wt: skipping the memory check (cannot read the safetensors of %s)\n", t.ModelName)` and nil; `e.wiredLimit()` error -> `routePrintf(ctx, "wt: skipping the memory check (cannot read iogpu.wired_limit_mb: %v)\n", err)` and nil; `!ok` -> nil; `limit < wiredNeedMiB(bytes)` -> `&WiredLimitError{...}`. `lifecycle.start()`: `if pf, ok := b.(preflighter); ok { if err := pf.preflight(ctx, e, cfg, t); err != nil { return err } }`.

- [ ] **Step 1: Failing tests** (hermetic: pack dirs in `t.TempDir()` with sparse files via `os.Truncate`, MiB-scale if a filesystem lacks sparse support; injected `e.wiredLimit`; no sysctl):
  - `TestPackResidentBytesCountsOnlySafetensors`: 30000 + 17213 MiB `.safetensors` + a 32 GiB `ngram_table.bin` + `config.json` -> `47213<<20`; a second case sized to exactly 49,506,491,792 bytes (the real pack) -> that number; missing dir -> ok=false.
  - `TestWiredNeedIs110PercentFloored`: `47213<<20` -> 51934; 49,506,491,792 -> 51934 (the cross-language figure); `1<<20` -> 1; 0 -> 0.
  - `TestParseWiredLimit`: `"59000\n68719476736\n"` -> 59000; `"0\n68719476736\n"` -> 49152; `"0\n"` -> error; `""`, `"x\ny\n"` -> error.
  - `TestWiredLimitErrorNamesTheFix`: contains `sudo sysctl iogpu.wired_limit_mb=59000`, `resets on reboot`, both numbers.
  - `TestSushiPreflightFailsBelowNeedAndPassesAbove`: `stubFamily` `sushix` plus `wiredLimitFamilies["sushix"]=true` restored in Cleanup; pack bytes `47213<<20`; limit 49152 -> `*WiredLimitError{LimitMB:49152, NeedMB:51934}`; 51933 -> error; 59000 and 51934 -> nil; unreadable pack dir -> nil plus a `skipping the memory check` line; limit error -> nil plus the same line (capture as `omlxpool_test.go` captureRoutes does); `ok=false` -> nil; `e.lookPath` failing -> `*BinaryMissingError` (`TestSushiPreflightMissingBinary`).
  - `TestPreflightRunsBeforeAnyOtherProviderIsStopped`: `lifecycle.start()` with `e.backends["sushix"]=processServer{"sushix"}`, an inventory snapshot with a running other model, `opts.Others = OthersAnswer{Decided:true, Stop:["ollama/x"]}`, a recording `fakeBackend` for the other family; limit 49152 -> `*WiredLimitError`, the other backend's `calls` empty, and no `OccupiedError`.
  - `TestPreflightIsSkippedForOtherFamilies`: mtplx start with an `e.wiredLimit` that calls `t.Fatal`.
  - `TestIsolateProcessesForTestClosesTheSysctl`: `readWiredLimit()` returns an error in the test binary.
- [ ] **Step 2: Run.** `cd wt && go test ./internal/lifecycle/ -run 'TestPackResident|TestWiredNeed|TestWiredLimitError|TestSushiPreflight|TestPreflight|TestIsolateProcessesForTestClosesTheSysctl' -count=1`. Expected FAIL: `undefined: packResidentMiB, WiredLimitError, readWiredLimit`.
- [ ] **Step 3: Implement** the three files, env field, `IsolateProcessesForTest` line, `start()` hook, `testEnv()` default.
- [ ] **Step 4: Run green.** `cd wt && go test ./internal/lifecycle/ ./cmd/wt/ -count=1`, then `go test ./... -count=1`.
- [ ] **Step 5: Commit** `feat(wt): refuse a sushi start whose wired limit cannot hold the pack`

---

## Task 9: Memory-refusal hint on a warmup failure

**Files:** modify `wt/internal/lifecycle/wiredlimit.go`, `server.go` 147-156 (inside the ReadyHTTP-gated failure handling from Task 7, after the log tail), `wiredlimit_test.go`; extend `helperSushi` with `LIFECYCLE_SUSHI_CHAT_BODY` (default `{"error":"No default model configured"}`).

**Interfaces:**
- Consumes: `wiredLimitHint()` (Task 8); the gated failure path (Task 7).
- Produces: `func looksLikeMemoryRefusal(msg string) bool`: lower-cased substring match on `out of memory`, `insufficient memory`, `not enough memory`, `cannot allocate`, `failed to allocate`, `memory limit`, `wired limit`, `wired_limit`, `iogpu` (a pinned heuristic; Sushi's real wording is unmeasured, the acceptance run may extend it; the Python `_MEMORY_REFUSAL` uses the SAME phrase list). In `start()`, ReadyHTTP only, never for `context.Canceled`: a failure whose text (including the appended log tail) matches becomes `fmt.Errorf("%w\n%s", err, wiredLimitHint())` so `errors.Is/As` survive.

- [ ] **Step 1: Failing tests:** `TestLooksLikeMemoryRefusal` (`HTTP 500: Out of memory allocating Metal buffer` true; `IOGPU command buffer` true; `No default model configured` false; `""` false); `TestSushiWarmupMemoryRefusalAppendsTheHint` (chat 500, body containing `out of memory`: error contains `sudo sysctl iogpu.wired_limit_mb=59000`, `resets on reboot`, and still `HTTP 500`; `filesLeftFor(e,"sushix")` false,false); `TestSushiWarmup503GetsNoHint`; `TestMtplxWarmupMemoryRefusalGetsNoHint` (mtplx helper 500 `out of memory` -> no `sudo sysctl`).
- [ ] **Step 2: Run.** `cd wt && go test ./internal/lifecycle/ -run 'TestLooksLikeMemoryRefusal|TestSushiWarmup|TestMtplxWarmupMemory' -count=1`. Expected FAIL: `undefined looksLikeMemoryRefusal`, then the hint assertions.
- [ ] **Step 3: Implement** the function and the wrap.
- [ ] **Step 4: Run green.** `cd wt && go test ./internal/lifecycle/ -count=1`.
- [ ] **Step 5: Commit** `feat(wt): add the wired-limit fix to a sushi warmup that looks like a memory refusal`

---

## Task 10: Unowned-server warning (wt)

**Files:** modify `wt/internal/lifecycle/lifecycle.go` 486-489 (isRunning no-op branch of `start()`); create `unowned.go`, `unowned_test.go`.

**Interfaces:**
- Consumes: `loadingServer`; the empty-Stop family test `len(spec.Stop)==0`.
- Produces: `func (e *env) warnUnowned(ctx context.Context, cfg *config.Config, family string, t Target)`: no-op unless the family's spec has an empty Stop; `l := e.loadingServer(cfg, family)`; when `l.PID == 0` print via `routePrintf` a line containing `wt did not start it` and `cannot see its flags` (full text: `wt: %s is already serving %s on port %d but wt did not start it; wt cannot see its flags (context size, max tokens), so a long prompt may be refused or truncated; stop it yourself to have wt start it with its own flags\n`). `start()`: `if isRunning(snap, family, t) { e.warnUnowned(ctx, cfg, family, t); return nil }` (still a no-op returning nil). A foreign IDLE server needs no new code: `portClosedWithin` is false at server.go:128, so `*PortBusyError` is returned before spawning; a test pins it.

- [ ] **Step 1: Failing tests:** `TestStartWarnsWhenAForeignSushiAlreadyServesTheTarget` (stubbed `sushix`, snapshot `Running:true`, no pidfile: captured warning contains both phrases, start returns nil, `e.spawn` is `t.Fatal`); `TestStartDoesNotWarnForAServerWtStarted` (verified pidfile + `.wt` -> no output); `TestStartDoesNotWarnForMtplx` (real mtplx Running snapshot, no pidfile -> no output); `TestStartAgainstAnIdleForeignSushiIsPortBusy` (httptest on the registry port answering `/health` 200 and `/v1/models` `loaded:false`; nothing Running; `errors.As(&PortBusyError)` with the port and the message containing `stop it`; no pidfile created; `e.signal` never called; this one already passes and pins existing behaviour).
- [ ] **Step 2: Run.** `cd wt && go test ./internal/lifecycle/ -run 'TestStartWarnsWhenAForeign|TestStartDoesNotWarn|TestStartAgainstAnIdleForeign' -count=1`. Expected FAIL: no warning text.
- [ ] **Step 3: Implement** `warnUnowned` and the one-line call.
- [ ] **Step 4: Run green.** `cd wt && go test ./internal/lifecycle/ -count=1`.
- [ ] **Step 5: Commit** `feat(wt): warn when a sushi wt did not start already serves the target`

---

## Task 11: llmbench contract parser (warmup_seconds, http-get, empty stop)

Depends on Task 1 (mtplx/rec already carry `warmup_seconds`). Real files: `llmbench/src/llmbench/providers/process_spec.py`, tests under `llmbench/tests/providers/`.

**Files:**
- Modify `process_spec.py` 16 (`READY_KINDS`), 33-51 (`ProcessSpec`), 65-69 (`_get`), 80-94 (`_parse_ready`), 97-121 (`_parse`); `tests/providers/test_process_spec.py` (GOOD 11-37, value tests 60-83); `tests/providers/lifecycle/backends/test_process_server.py` (`_spec` helper 33-62; field-set guard 1017-1023); `tests/providers/lifecycle/backends/test_contract_failure.py`.

**Interfaces:**
- Consumes: contract key `warmup_seconds` on mtplx/rec (Task 1).
- Produces: `READY_KINDS == ("models-list", "command-json", "http-get")`; `ProcessSpec.warmup_seconds: int` (required, after `restore_action`); `_int(raw: dict[str, Any], key: str, where: str, *, minimum: int = 1) -> int` raising `ProcessSpecError(f"{where}: `{key}` must be a positive integer")` (rejects `bool`, floats, strings, values below minimum); `wt_port` and `warmup_seconds` both parsed with `_int` (closes a raw `ValueError` hole: `backends/__init__.py` catches only `ProcessSpecError`); `http-get` needs a non-empty `path` and an empty `command`; `_spec()` in `test_process_server.py` gains `stop: tuple[str, ...] = ("stop","--port","{port}")`, `warmup_seconds: int = 120`, and `kind == "http-get"` building `Ready("http-get", (), "/health", "", "", "")`.

- [ ] **Step 1: Failing tests.** GOOD gains `warmup_seconds = 120` after `restore_action`. Add:
  - `test_mtplx_and_rec_warm_for_120_seconds`; `test_missing_warmup_seconds_raises` (match ``process\[0\]: missing key `warmup_seconds` ``); parametrized `test_warmup_seconds_must_be_a_positive_integer` over `'"120"'`, `0`, `-5`, `true`, `1.5`, `'"abc"'`; `test_wt_port_that_is_not_an_integer_is_a_spec_error_not_a_value_error`; `test_empty_stop_list_parses_to_an_empty_tuple`; `test_http_get_ready_kind_parses` (kind, path `/health`, command `()`); `test_http_get_needs_a_path`; `test_http_get_takes_no_command`.
  - `test_contract_failure.py`: factor the subprocess call into `_run(env_file) -> list[str]` and add `test_one_malformed_entry_disables_every_process_provider`: copy the real contract, replace the FIRST `warmup_seconds = 120` with `"abc"`; output line 1 is `['mlx_lm_server', 'ollama', 'omlx', 'omlx-6bit']`, line 2 `True`, line 3 starts with `unknown provider: rec (process providers unavailable: process[0]: ` `warmup_seconds` ` must be a positive integer`. Verify the repo-root depth (`parents[5]`) against the existing real-file test.
- [ ] **Step 2: Run.** `cd llmbench && uv run pytest tests/providers/test_process_spec.py tests/providers/lifecycle/backends/test_contract_failure.py -q`. Expected FAIL: `AttributeError ... warmup_seconds`; ``unknown kind `http-get` ``; raw `ValueError`.
- [ ] **Step 3: Implement** per the Interfaces block; update the module docstring's ready-kind list. Do not catch the error anywhere new.
- [ ] **Step 4: Run green.** `cd llmbench && uv run pytest -q && make check`.
- [ ] **Step 5: Commit** `feat(llmbench): parse warmup_seconds, http-get readiness and empty stop in the process contract`

**Review focus:** `isinstance(True, int)` is true in Python: bool must be rejected; only `_parse` and the test `_spec` construct `ProcessSpec`.

---

## Task 12: llmbench probe primitives

Real files: `llmbench/src/llmbench/local_process.py` (35-48), `llmbench/src/llmbench/providers/lifecycle/probe.py` (the design's `providers/probe.py` does not exist; 28 import, 118-147, 232-260, 263-341 with the hard-coded `timeout=120` at 314); tests `tests/test_local_process.py` (21-82), `tests/providers/lifecycle/test_probe.py` (17-23, 155-232).

**Interfaces:**
- Consumes: nothing.
- Produces: `local_process.models_body_ids(data: object, *, loaded_only: bool = True) -> list[str]` (drops an item only when `item.get("loaded") is False`, the Go rule: a literal false); `probe.serving_model(models_url, model, *, key=None, exact: bool = False)` (`exact=True` tests plain membership instead of the lenient `_lists` suffix match; the http-get paths pass it); `local_process.http_models_ids(url, timeout=2.0, *, loaded_only=True)`; `probe._model_ids(models_url, key, *, refused_is_final, loaded_only=True)`; `probe.listed_ids(models_url: str, *, key: str | None = None) -> list[str]` (UNFILTERED, `[]` on any failure, never raises); `probe.wait_for_http_ok(url: str, *, proc: subprocess.Popen | None = None, timeout: float = MODEL_LOAD_TIMEOUT, key: str | None = None) -> None` (poll `_deadline_loop`; process died -> `LifecycleError("serve process exited during startup (exit N)")`; 200 returns; HTTPError/OSError sleeps 1.0 and retries; timeout -> `LifecycleError("timed out waiting for <url> to answer 200")`; key sent as Bearer only when given); `probe.warmup(..., request_timeout: float = 120.0)` (the per-chat-request urlopen cap; the overall `timeout` already existed). `wait_for_model`/`serving_model` keep their signatures and inherit the filter.

- [ ] **Step 1: Failing tests.**
  - `test_http_models_ids_drops_only_entries_explicitly_not_loaded`: body `a t, b f, c absent, d None, e "false"` -> default `[a, c, d, e]`; `loaded_only=False` -> `[a, b, c, d, e]`.
  - test_probe.py (module `BODY` = `org/a loaded True`, `org/b False`, `org/c` absent): parametrized over `key in (None, "k")`: `test_serving_model_ignores_a_pack_that_is_listed_but_not_loaded` (a True, b False, c True), `test_listed_ids_is_unfiltered` (all three); `test_listed_ids_is_empty_on_a_dead_server`; `test_serving_model_exact_rejects_a_suffix_match` (`org/a` listed, `a` asked: lenient True, `exact=True` False); `test_wait_for_http_ok_returns_on_200`; `..._keeps_polling_through_a_503_then_succeeds` (two urlopen calls, sleep patched); `..._raises_when_the_process_died`; `..._times_out_naming_the_url` (monotonic patched); `..._sends_the_key_only_when_given`; `test_warmup_request_timeout_defaults_to_120_and_is_overridable` (chat-call timeouts `[120, 400.0]`, the 2.0 s health probe excluded).
- [ ] **Step 2: Run.** `cd llmbench && uv run pytest tests/test_local_process.py tests/providers/lifecycle/test_probe.py -q`. Expected FAIL: ImportError for `listed_ids`/`wait_for_http_ok`; unexpected keyword `loaded_only`/`request_timeout`.
- [ ] **Step 3: Implement.** The existing test_probe tests that patch `probe.http_models_ids` with a bare `return_value` must keep working with the new kwarg.
- [ ] **Step 4: Run green.** `cd llmbench && uv run pytest -q && make check`.
- [ ] **Step 5: Commit** `feat(llmbench): probe honors a models-list loaded flag and gains http-get readiness and a per-request warmup timeout`

---

## Task 13: llmbench ProcessServerBackend: http-get readiness, loaded-aware serving, warm budget

**Files:** modify `llmbench/tests/providers/lifecycle/backends/test_mtplx.py` (307-317: the assertion becomes `assert_called_once_with(MTPLX.chat_url, "Org/Model", health_url=MTPLX.health_url, timeout=120.0, request_timeout=120.0)`); modify `llmbench/src/llmbench/providers/lifecycle/backends/process_server.py` (docstring 1-18; imports 20-36 add `re`, `urllib.error`; delete `WARM_TIMEOUT` 38-39 (used only here); `_Address` 142-153; `already_serving` 305-339; `_wait_ready` 406-425; `warm` 427-436); tests `tests/providers/lifecycle/backends/test_process_server.py` (line 479-495 test gains `assert warmup.call_args.kwargs["request_timeout"] == 120.0`; new tests after 1045; imports `orchestrate`, `urllib.error`).

**Interfaces:**
- Consumes: `ProcessSpec.warmup_seconds` (Task 11); `probe.wait_for_http_ok`, `listed_ids`, `warmup(request_timeout=)` (Task 12); `_spec(kind="http-get", stop=..., warmup_seconds=...)`.
- Produces: `_Address.url(path: str) -> str`; `ProcessServerBackend._signal_stop` property = `not self.spec.stop`; `_WIRED_LIMIT_HINT = "raise the GPU wired limit with `sudo sysctl iogpu.wired_limit_mb=59000` (resets on reboot)"`; `_MEMORY_REFUSAL = re.compile(r"out of memory|insufficient memory|not enough memory|cannot allocate|failed to allocate|memory limit|wired limit|wired_limit|iogpu", re.IGNORECASE)` (the same phrase list as Go's `looksLikeMemoryRefusal`). Behaviour: `already_serving` treats kind `http-get` like `models-list` (asks `probe.serving_model(..., exact=True)` for http-get, loaded-aware); `_wait_ready` for `http-get` calls `probe.wait_for_http_ok(self._address().url(ready.path), proc=self._proc, timeout=probe.MODEL_LOAD_TIMEOUT, **_key_arg(self._key()))`; `warm`: `budget = float(spec.warmup_seconds)`; for `http-get` first `ids = probe.listed_ids(...)` and if `ids` is non-empty and the model is not an EXACT member raise `LifecycleError(f"{self.id}: {plan.model!r} is not listed by the server (it lists: {', '.join(ids)})")`; then `probe.warmup(addr.chat_url, plan.model, health_url=addr.health_url, timeout=budget, request_timeout=budget, **_key_arg(key))`; `TimeoutError` (or `URLError` whose `reason` is a `TimeoutError`) -> `LifecycleError(f"{self.id}: warmup of {plan.model} timed out after {spec.warmup_seconds}s")`; other `URLError` (e.g. connection refused) re-raised untouched; a `LifecycleError` whose text matches `_MEMORY_REFUSAL` on a `_signal_stop` backend becomes `LifecycleError(f"{exc}; {_WIRED_LIMIT_HINT}")`, otherwise bare `raise`; after a successful `http-get` warmup require `probe.serving_model(..., exact=True)` true else `LifecycleError(f"{self.id}: {plan.model} answered the warmup but is not listed as loaded at {addr.health_url}")`. Any warmup failure on a `_signal_stop` backend (timeout, refusal, not-listed-as-loaded) has the log tail appended via `self._with_log_tail(message, self._pidproc.log_tail())`, as `_wait_ready` does (mtplx/rec text unchanged). `WarmupRefusedError`/`WarmupRejectedError` keep their types unless the memory rule rewraps them.

- [ ] **Step 1: Failing tests** (synthetic `_spec()`, `PS`-patched; spec port 9100): `test_already_serving_http_get_asks_the_loaded_aware_list` (`serving_model` called once with `("http://127.0.0.1:9100/v1/models", "org/pack")`); `test_wait_ready_http_get_polls_the_ready_path_not_the_models_list`; `..._passes_the_key_only_when_the_row_names_one`; `test_warm_uses_the_spec_warmup_budget` (400 for both `timeout` and `request_timeout`); `test_warm_refuses_a_pack_id_the_server_does_not_list` (`warmup` not called); `test_warm_skips_the_pack_check_when_the_list_is_empty`; `test_warm_requires_the_pack_to_be_listed_loaded_after_the_chat`; `test_warm_models_list_kind_has_no_pack_check_and_no_loaded_check` (mtplx path; `timeout == 120.0`); `test_warm_timeout_becomes_a_clear_lifecycle_error` (parametrized `TimeoutError`, `URLError(TimeoutError)`); `test_warm_connection_refused_is_not_called_a_timeout`; `test_isolate_returns_an_ok_false_envelope_when_the_warmup_times_out`; `test_memory_refusal_in_a_signal_stop_warmup_gets_the_wired_limit_hint`; `test_the_hint_is_not_added_for_a_provider_with_a_stop_command` (`exc.value is original`); `test_a_non_memory_warmup_failure_keeps_its_text`; `test_a_signal_stop_warmup_failure_carries_the_log_tail` (and `test_a_stop_command_warmup_failure_has_no_log_tail`); `test_warm_pack_check_is_exact` (listed `org/pack`, plan model `pack` -> LifecycleError).
- [ ] **Step 2: Run.** `cd llmbench && uv run pytest tests/providers/lifecycle/backends/test_process_server.py -q`. Expected FAIL: `wait_ready` falls to command-json; `warm` has no `request_timeout`.
- [ ] **Step 3: Implement** per Interfaces.
- [ ] **Step 4: Run green.** `cd llmbench && uv run pytest -q && make check`.
- [ ] **Step 5: Commit** `feat(llmbench): process backend supports http-get readiness, loaded-aware serving and a per-spec warmup budget`

**Review focus:** mtplx/rec `warm` differs only by the added `request_timeout=120.0` (the old literal); the pack check is exact-match against the unfiltered list, the loaded check against the filtered one.

---

## Task 14: llmbench empty stop, ownership and hermetic guards

**Files:**
- Modify `providers/lifecycle/pidproc.py` 29-48 (`spawn(self, argv, *, new_session: bool = False)`: `Popen(..., **({"start_new_session": True} if new_session else {}))`, so mtplx/mlx_lm_server call kwargs are byte-identical) and 50-91 (add `live_pid` before `_reap` at 93); `process_server.py` (constants 36-47 add `SIGNAL_STOP_TIMEOUT`; `__init__` 167-173; the `spawn` call at 367; `already_serving`; `stop_and_wait` 438-491); `tests/conftest.py` 52-60 (`_FAKE_BINARIES` += `"sushi"`, extend the comment: `sushi serve` loads a 46 GB pack) plus a new autouse fixture `_redirect_signal_stop_pidfiles` (llmbench has no pidfile override env: `PidfileEnv` is parsed in `process_spec.py` but read nowhere in `src`): for every backend in `BACKENDS` with `_signal_stop`, `monkeypatch.setattr(backend, "_pidproc", PidfileProcess(name=backend.spec.provider, pidfile=str(tmp_path/"x.pid"), logfile=str(tmp_path/"x.log")))` so an unpatched `stop_and_wait`/`live_pid` can only ever read a scratch file; tests that patch `_pidproc` themselves still override it; `tests/providers/lifecycle/test_hermeticity.py` 18-33; `test_pidproc.py`; `test_process_server.py`.

**Interfaces:**
- Consumes: `_signal_stop` (Task 13); `probe.wait_for_port_open`, `probe.port_closed_within`.
- Produces: `PidfileProcess.live_pid(self) -> int | None` (read-only: pidfile pid > 0 parsing, `os.kill(pid, 0)` alive, `_looks_like_this_process(pid)`; never unlinks, never signals, never calls `os.kill` with 0 or negative); `SIGNAL_STOP_TIMEOUT = 15.0`; `stop_and_wait`: first statement `if self._signal_stop: return self._signal_stop_and_wait()`, before any binary lookup. `_signal_stop_and_wait() -> str | None`: `_forget_row()`; `owned = self._pidproc.live_pid() is not None`; `self._pidproc.stop()`; `probe.port_closed_within(addr.health_url, timeout=SIGNAL_STOP_TIMEOUT)` -> None; not owned -> `f"{self.id}: a server answers on port {port} that was not started by llmbench or wt (no verified pidfile at {spec.pidfile}); stop it yourself"`; owned -> `f"{spec.binary} still listening on port {port}"`. `already_serving` (signal-stop only, before the readiness branch): `if self._signal_stop and probe.wait_for_port_open(addr.health_url, timeout=1.0, interval=0.2, **_key_arg(key)) and self._pidproc.live_pid() is None: raise LifecycleError(<same text, ending "; its context size cannot be checked, so it is not reused. Stop it yourself and retry">)`. `already_serving` is the pre-teardown gate (`orchestrate.isolate` calls it before `_stop_others`), so no new Backend hook. Start-failure parity with wt (design steps 3 and 6): `ProcessServerBackend.__init__` sets `self.cleanup_on_failure = cleanup_on_failure or not spec.stop` (so `orchestrate.isolate` calls `stop_and_wait`, the pidfile stop, after a failed or timed-out warmup; the half-loaded 46 GB server and its pidfile are removed) and `start` passes `new_session=self._signal_stop` to `self._pidproc.spawn` (Setsid-equivalent: a terminal SIGHUP cannot kill the server). `backends/__init__.py` is not touched. The refusal phrase `not started by llmbench or wt` is shared.

- [ ] **Step 1: Failing tests.** conftest `_FAKE_BINARIES` and hermeticity cases first (`["sushi", "--llmbench-hermetic-no-such-flag"]`, `["/opt/homebrew/bin/sushi", "serve"]`). `test_pidproc.py` (`PidfileProcess(name="sushi", pidfile=tmp, logfile=tmp)`): `test_live_pid_returns_the_pid_of_a_live_matching_process` (pidfile still exists after); `..._none_without_a_pidfile`; parametrized unusable content `''`, `abc`, `0`, `-7` (`os.kill` never called); parametrized `ProcessLookupError`/`PermissionError` -> None; `..._none_when_the_process_is_something_else`. `test_process_server.py` (spec `_spec(kind="http-get", stop=())`): `test_empty_stop_stops_through_the_pidfile_and_never_runs_the_binary` (`subprocess.run` not called, `require_binary` raises if touched, `port_closed_within` called with `timeout=SIGNAL_STOP_TIMEOUT`); `test_empty_stop_with_nothing_running_is_a_clean_noop`; `test_empty_stop_reports_a_server_it_cannot_own`; `test_empty_stop_reports_a_server_that_did_not_let_go`; in the existing stop-with-command test assert `_pidproc.stop` is not used; `test_already_serving_refuses_a_server_nobody_verified` (message names `/tmp/fake.pid` and port 9100); `..._accepts_a_server_with_a_verified_pidfile`; `..._does_not_object_when_nothing_answers`; `test_a_provider_with_a_stop_command_never_asks_about_ownership`; `test_the_refusal_happens_before_any_other_provider_is_stopped` (`orchestrate.isolate` -> `ok is False`, `_stop_others` not called); `test_the_suite_redirects_the_sushi_pidfile` (`BACKENDS["sushi"]._pidproc.pidfile != "/tmp/local-ai-setup-sushi.pid"`); `test_a_failed_warmup_stops_a_signal_stop_backend` (`orchestrate.isolate` with `warm` raising after `start`: `stop_and_wait` called once, `ok is False`) and `test_a_failed_warmup_does_not_stop_a_stop_command_backend` (rec: not called; mtplx keeps its own `True`); `test_signal_stop_spawn_starts_a_new_session` (`subprocess.Popen` patched: kwargs contain `start_new_session=True`) and `test_mtplx_and_mlx_lm_server_spawn_pass_no_session_kwarg` (`"start_new_session" not in kwargs`). The new tests that reach `already_serving` or `stop_and_wait` of sushi (here and Task 16's orchestrate tests) patch `probe.wait_for_port_open`/`port_closed_within` and `_pidproc` explicitly, never the live port 12345.
- [ ] **Step 2: Run.** `cd llmbench && uv run pytest tests/providers/lifecycle/test_hermeticity.py tests/providers/lifecycle/test_pidproc.py tests/providers/lifecycle/backends/test_process_server.py -q`. Expected FAIL: no `live_pid`; empty-stop tests hit `subprocess.run`/`require_binary`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run green.** `cd llmbench && uv run pytest -q && make check`.
- [ ] **Step 5: Commit** `feat(llmbench): an empty stop signals the verified pid and an unowned server is refused`

**Review focus:** `live_pid` is fail-open when `ps` cannot answer and only checks that "sushi" appears in the `ps` line (existing `_looks_like_this_process` contract), so ownership is fail-closed only for a missing, unparsable, dead or mismatching pidfile; this weaker-than-wt guarantee is stated in the docs (Task 21). PR #505 edits the same `_FAKE_BINARIES` line and hermeticity block; see Handoff.

---

## Task 15: llmbench wired-limit preflight

**Files:** modify `llmbench/src/llmbench/registry.py` 28-34 (`ProviderEntry`), 140-152 (`_parse_provider`); `process_server.py` (imports `sys`, `pathlib.Path`; constants; `already_serving`); tests `tests/test_registry.py` (224-244), `test_process_server.py` (`_provider_row` helper 69-79 gains `model_dir`).

**Interfaces:**
- Consumes: `_WIRED_LIMIT_HINT`, `_signal_stop` (Task 13); the unowned refusal order (Task 14: refusal first, then preflight).
- Produces: `ProviderEntry.model_dir: str = ""` (top-level `model_dir` of the provider row, not expanded by the reader); `_DEFAULT_PACK_DIR = "~/.sushi/models"` (mirrors the family's `DefaultModelDir`, used when the provider row has no `model_dir`, as Go's `localmodels.ModelDir` does); `_wired_limit_mb() -> int | None` (darwin only; ONE call `["sysctl","-n","iogpu.wired_limit_mb","hw.memsize"]` with `capture_output=True, text=True, check=False`; limit `0` means the macOS default and reads as `(memsize * 3 // 4) // 1048576`, exactly Go's `parseWiredLimit`; None on non-darwin, nonzero exit, `OSError`, or unparsable output; the 0 rule is an addition beyond the design, in both languages, to be confirmed in acceptance); `_safetensors_bytes(pack_dir: Path) -> int | None` (sum of `*.safetensors` directly in the dir; None if unreadable or none; `ngram_table.bin` excluded); `_need_mib(total_bytes: int) -> int` = `total_bytes * 11 // 10 // 1048576` (integer, no float; 49_506_491_792 -> 51934, identical to Go's `wiredNeedMiB`); `_preflight_wired_limit(self, plan) -> None` raising `LifecycleError(f"{self.id}: the GPU wired limit is {limit} MiB but {plan.model} needs about {need} MiB (1.1 x its safetensors); {_WIRED_LIMIT_HINT}")`. Wired into `already_serving` for `_signal_stop` only, after the unowned refusal, only when the pack is NOT already serving; every skip path (no safetensors or unreadable pack dir, unknown limit) prints `skipping the memory check (<why>)` to stderr (restructure the models-list/http-get branch to assign `serving`, then `if self._signal_stop and not serving: self._preflight_wired_limit(plan)`; return `serving`).

- [ ] **Step 1: Failing tests.** `test_registry.py`: `test_provider_model_dir_is_read_from_the_row` (`("~/.sushi/models", "")`, not expanded). `test_process_server.py` (helper `_signal_backend(tmp_path)` writing a registry with `model_dir=tmp/models` and `org/pack` files sized via `truncate`): `test_safetensors_bytes_sums_only_safetensors`; `test_need_mib_is_one_point_one_times_the_bytes` (49_506_491_792 -> 51934; `47213 * 1048576` -> 51934; the same figures as Go's `TestWiredNeedIs110PercentFloored`); `test_wired_limit_reads_one_sysctl_call`; `..._zero_means_three_quarters_of_memory` (`'0\n68719476736\n'` -> 49152); `..._unknown_off_darwin`; parametrized unreadable results (code 1, `''`, `'x\ny\n'`, `'59000\n'`) -> None; `..._oserror_is_none`; `test_already_serving_fails_with_the_fix_command_when_the_limit_is_too_low` (100 MiB pack -> need 110; limit 50; message has `sudo sysctl iogpu.wired_limit_mb=59000`, `resets on reboot`, `50 MiB`, `110 MiB`); `..._passes_at_the_exact_need`; preflight skipped (with a `skipping the memory check` line asserted via `capsys`) when the pack dir is unreadable (limit not consulted), when the limit is unknown; `test_a_row_without_model_dir_uses_the_default_pack_dir` (patched `Path.expanduser`/home); when the pack is already loaded (`_wired_limit_mb` not called); `test_a_provider_with_a_stop_command_never_runs_the_preflight`; `test_a_failed_preflight_stops_no_other_provider` (`orchestrate.isolate` -> `ok False`, `'wired_limit_mb=59000'` in the error, `_stop_others` not called). `sysctl` is never run unpatched.
- [ ] **Step 2: Run.** `cd llmbench && uv run pytest tests/test_registry.py tests/providers/lifecycle/backends/test_process_server.py -q`. Expected FAIL: `ProviderEntry.model_dir`, `_wired_limit_mb`, `_need_mib`, `_safetensors_bytes` missing.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run green.** `cd llmbench && uv run pytest -q && make check`.
- [ ] **Step 5: Commit** `feat(llmbench): fail early with the sysctl fix when the wired limit cannot hold the pack`

---

## Task 16: Sushi goes live (wt row + contract block + llmbench registration), ONE commit

Everything before this task is proven on synthetic specs. This commit makes sushi real in both languages and must be a single commit, because the contract block auto-registers a llmbench backend and flips every enumerating test.

**Files (wt):**
- `wt/internal/provider/table.go`: consts `sushiPort = 12345`, `sushiOrigin = "http://127.0.0.1:12345"`, `sushiModelDir = "~/.sushi/models"` (the spec names only the first two; the third lets the family row and seed share one literal like `omlxModelDir`) in a const block (23-36); Provider Spec inserted after rec and before llamacpp (82-91 area): `{ID: "sushi", Family: "sushi", Policy: &openaiLocal, Seed: &Seed{Name: "Sushi", Location: "local", AuthType: "none", BaseURL: sushiOrigin + "/v1", ModelDir: sushiModelDir, Command: "sushi"}}`; family row after rec (136-155): `Name: "sushi", DefaultOrigin: sushiOrigin, DefaultModelDir: sushiModelDir, WtPort: sushiPort, Served: true, ServedOnly: true, Tenancy: Exclusive, Scope: ScopePort`, with a short comment (model-less `sushi serve`, one process per machine, loads a pack on the first request, stopped by signalling the verified pid, ctx 131072 is a Spawn literal); Process: `&ProcessSpec{Binary: "sushi", Spawn: <the literal>, Stop: nil, PidfileDefault: "/tmp/local-ai-setup-sushi.pid", LogDefault: "/tmp/local-ai-setup-sushi.log", PidfileEnv: "WT_SUSHI_PIDFILE", Subcommand: "serve", PortFlag: "--port", Ready: Ready{Kind: ReadyHTTP, Path: "/health"}, WarmupSeconds: 400}`.
- `docs/contracts/process-providers.toml`: append the sushi block (all keys; sushi last): `family = "sushi"`, `provider = "sushi"`, `binary = "sushi"`, `spawn = [...]`, `stop = []`, `pidfile = "/tmp/local-ai-setup-sushi.pid"`, `log = "/tmp/local-ai-setup-sushi.log"`, `pidfile_env = "WT_SUSHI_PIDFILE"`, `subcommand = "serve"`, `daemon_module = ""`, `port_flag = "--port"`, `model_flag = ""`, `scope = "port"`, `default_origin = "http://127.0.0.1:12345"`, `wt_port = 12345`, `restore_action = "stop"`, `warmup_seconds = 400`, then `[process.ready]` with `kind = "http-get"`, `command = []`, `path = "/health"`, `equals = ""`, `model_path = ""`, `endpoint_path = ""`. Header comment: "stop = [] means wt and llmbench stop the family by signalling the verified pid".
- Pinned-set tests (apply the exact edits):
  - `wt/internal/provider/provider_test.go`: `TestAccessorsMatchLegacyTables` (130-225): families `"sushi": "sushi"`; `Families()` want `[mlx_lm_server mtplx ollama omlx rec sushi]`; wantPolicies `"sushi": {Prefix: "openai/", APIKey: "not-needed", V1Base: true}`; `SeedableLocalIDs` want `[ollama omlx mlx_lm_server mtplx rec sushi]`; `InstalledCommands` gains `"sushi": "sushi"`; `ServedFamilies` want `[mlx_lm_server mtplx omlx rec sushi]`; `WtPortFor` `"sushi": 12345`; flags `"sushi": {false, false, true}`; tenancy `Exclusive`; defaults `{"http://127.0.0.1:12345", "~/.sushi/models"}`. `TestSeedRowsMatchLegacyLiterals` (227-268): `"sushi": {"id":"sushi","name":"Sushi","location":"local","model_dir":"~/.sushi/models","auth":{"type":"none","base_url":"http://127.0.0.1:12345/v1"}}`. `TestProcessFamilies` (355-366) want `[mtplx rec sushi]` (message and comment say "mtplx, rec and sushi"). `TestRecRows` order (369-425): `ollama omlx omlx-6bit mlx_lm_server mtplx rec sushi llamacpp openrouter`. `TestServedOnlyFlags` (440-455): `want := f == "mlx_lm_server" || f == "rec" || f == "sushi"`.
  - New in `provider_test.go`: `TestSushiRows` (comment: pins every value; a drifted value starts the wrong command, a wrong `--ctx-size` silently runs the benchmark at the wrong window, dials the wrong port, or writes a different row) asserting `ProcessFor("sushi")` equals the pinned spec (compare `Stop` with `len(got.Stop)==0`), the family values above, `PolicyFor("sushi")`, `Lookup("sushi")` Family and Seed (`Protocols == nil`, `SecretRef == ""`), `processSpecProblems(got)` empty, `OwnedFlags()` contains `--ctx-size --max-tokens --max-resident-models --no-update-check --host --port`; and `TestSeedRowSushi` (`SeedRow("sushi")` deep-equals the map, no `protocols`, no `secret_ref`).
  - `wt/internal/config/provider_table_test.go` 28-34: `[ollama omlx mlx_lm_server mtplx rec sushi openrouter]`.
  - `wt/internal/config/registry_seed_test.go` 172 and 181: both "OnPath always true" cases want `[]string{"ollama","omlx","mtplx","sushi"}`.
  - `wt/internal/configeditor/models_form_test.go` 159-161 and 640: provider choices `"mtplx,ollama,omlx,openrouter,rec,sushi"` (comment: ollama, omlx, openrouter and the seedable mtplx, rec and sushi).
  - `wt/internal/localmodels/family_table_test.go` 32-34: `Families()` gains `sushi`; cases map `"sushi": "sushi"`. `inventory_test.go` 958: want `[mlx_lm_server mtplx omlx rec sushi]`.
  - `wt/internal/litellm/policy_table_test.go` ~20-43: PolicyFor want gains the sushi policy; `TestProvidersPinned` want gains `"sushi": false`.
  - `wt/internal/lifecycle/lifecycle_test.go` 285-289 and `server_test.go` 629-633: expected maps gain `"sushi": processServer{"sushi"}` (message "and sushi").
  - `wt/cmd/wt/stop_loading_test.go` after 394-399: `TestMainIsolatesEveryProcessPidfile` (comment: TestMain's `lifecycle.IsolateProcessesForTest()` redirects every process family's pidfile env var to a scratch path that is not the real `/tmp/local-ai-setup-*` default; a cmd/wt test must never read or signal a developer's real server, and sushi's stop reads its pidfile to find the pid it signals): for each `provider.ProcessFamilies()` the `PidfileEnv` value is non-empty, differs from `PidfileDefault`, and contains `wt-test-no-<family>-`; `WT_SUSHI_PIDFILE` is set. No TestMain edit needed (`procinfo.go:103-107` loops over `ProcessFamilies`).
- **Files (llmbench):**
  - `llmbench/src/llmbench/registry.py:21`: `DEFAULT_PROVIDER_IDS = ("ollama","omlx","mlx_lm_server","mtplx","rec","sushi")`.
  - `llmbench/src/llmbench/benchmark/isolation.py` 26-33: `POSITIONAL_MODEL_PROVIDERS = frozenset({"mtplx","rec","sushi"})`; reword the comment (sushi serves several packs, each row warms its own by name).
  - Tests: `tests/benchmark/test_isolation.py:130` literal set gains `"sushi"` plus `test_positional_model_providers_are_pinned`; `tests/test_main.py` 47-56 list `[mlx_lm_server, mtplx, ollama, omlx, omlx-6bit, rec, sushi]`; `tests/providers/lifecycle/test_orchestrate.py` 600-760: add `patch.object(BACKENDS["sushi"], "stop_and_wait", return_value=None)` beside every `BACKENDS["rec"]` patch (623, 656, 676, 695, 714, 735, 751) (otherwise restore runs the real sushi stop path against the real pidfile), assert it is called once in the restart/stop-actions test, add `test_isolating_another_provider_stops_sushi` and `test_isolating_sushi_stops_the_others_and_keeps_itself` (both patch `BACKENDS["sushi"].already_serving`/`stop_and_wait` and `_pidproc`, never the live pidfile or port); `tests/providers/test_process_spec.py`: `test_sushi_values` (every value of the contract block incl. `stop == ()`, `warmup_seconds == 400`, ready `("http-get","/health",())`, no `{model}`) and `test_expand_sushi_spawn_has_no_model_placeholder_left`; `tests/test_registry.py`: `test_sushi_is_a_default_provider`; `test_process_server.py` 102-115: `test_sushi_is_registered_generically_with_a_signal_stop` (`type(b) is ProcessServerBackend`, `restore_action == "stop"`, `cleanup_on_failure is True` (signal-stop specs clean up a failed start), `_signal_stop is True`, `id == occupancy_key == "sushi"`) and `test_no_other_backend_is_a_signal_stop`.

**Interfaces:**
- Consumes: Tasks 1-15 (ProcessSpec fields, engine behaviour, parser, backend, `processSpecProblems`).
- Produces: provider/family id `sushi`; `provider.ProcessFor("sushi")`; `ProcessFamilies() == [mtplx rec sushi]`; `SeedableLocalIDs()` ends `rec, sushi`; env var `WT_SUSHI_PIDFILE`; contract entry `family = "sushi"`; `BACKENDS["sushi"]`; unexported consts `sushiPort`, `sushiOrigin`, `sushiModelDir`.

- [ ] **Step 1: Write all the test edits above** (wt and llmbench).
- [ ] **Step 2: Run to see failures.** `cd wt && go test ./... -count=1 2>&1 | grep -v migrated` Expected FAIL: `TestSushiRows`/`TestSeedRowSushi` (`ProcessFor(sushi) absent`), `TestMainIsolatesEveryProcessPidfile` (`WT_SUSHI_PIDFILE unset`), every edited pinned set. `cd llmbench && uv run pytest -q` Expected FAIL: `test_sushi_values`, `test_sushi_is_a_default_provider`, `test_positional_model_providers_are_pinned`, `test_main` provider list.
- [ ] **Step 3: Implement** the table rows, the contract block (same commit), and the llmbench registry/isolation edits. Do not touch `backends/__init__.py` (generic registration) or `eval/conftest.py` `_BINARY_BACKENDS`.
- [ ] **Step 4: Run green.** `cd wt && gofmt -l . && go vet ./... && go test ./... -count=1`, including `TestProcessProvidersContractMatchesTheProviderTable`; `cd llmbench && uv run pytest -q && make check`. Confirm `ls -la /tmp/local-ai-setup-sushi.pid` is unchanged/absent before and after.
- [ ] **Step 5: Commit** `feat: sushi as a built-in process provider (serve --ctx-size 131072, stopped by verified pid, http-get ready)`

**Review focus:** the Spawn literal is identical in `table.go`, the contract block and `TestSushiRows`; no `Stop`, no `{model}`; every enumerating test updated (no test still expects the 5-family world); a green wt suite does not prove llmbench is green.

---

## Task 17: Loaded confirmation and real-row conformance

**Files:** modify `wt/internal/lifecycle/server.go` 147-156 (after a successful warmup, ReadyHTTP-gated), `sushi_pack.go`; create `sushi_real_test.go`.

**Interfaces:**
- Consumes: `provider.ProcessFor("sushi")` (Task 16); the E1 decoder `localmodels.FetchModelIDsKeyed` (Task 2; if Task 2 shipped under another exported name, use that one and say so in the commit body); `helperSushi`/`sushiEnv` (Task 7).
- Produces: `func (e *env) confirmLoaded(ctx context.Context, modelsURL, model, key string) error`: requires an id EXACTLY equal to `model` in the loaded-aware list (`localmodels.FetchModelIDsKeyed`; not `NameMatches`), else `fmt.Errorf("%s answered the warmup but /v1/models does not list %s as loaded", modelsURL, model)`; a keyed refusal is `*KeyRefusedError`. Failure takes the same `defer sp.kill()` cleanup and log-tail as a warmup failure.

- [ ] **Step 1: Failing tests** (`sushi_real_test.go`; real table row, registry via `provCfg("sushi", "http://"+addr+"/v1")` so the registry port overrides 12345):
  - `TestSushiRowDrivesTheEngine`: argv written by the helper equals the Spawn literal; `WarmupSeconds == 400`; `Ready.Kind == ReadyHTTP`; `len(Stop)==0`; `e.run` never called on stop.
  - `TestSushiStartFailsWhenTheListNeverShowsLoaded` (`LIFECYCLE_SUSHI_LOADED=false`): after a 200 warmup, error contains `does not list`, process killed, `filesLeftFor(e,"sushi")` false,false (family-aware helper from Task 6).
  - `TestSushiPidfileEnvIsIsolated`: `os.Getenv("WT_SUSHI_PIDFILE")` contains `wt-test-no-sushi-`.
  - `TestWiredLimitGateCoversTheSushiRow`: `wiredLimitFamilies["sushi"]` is true; and end to end with the real row (registry `model_dir` = a temp dir holding the pack's sparse safetensors, `e.wiredLimit` returning 49152): `lifecycle.start` returns `*WiredLimitError` before `stopOthers` (recording `fakeBackend` for another family sees no calls) and before any spawn.
  - `TestSushiConfirmLoadedIsExact`: helper lists loaded `beamster/Q`, model `Q` -> `does not list` error.
  - `TestSushiNeverTouchesTheRealPidfile`: `defaultEnv`'s `serverProc["sushi"].pidfile != "/tmp/local-ai-setup-sushi.pid"` in the test binary.
- [ ] **Step 2: Run.** `cd wt && go test ./internal/lifecycle/ -run 'TestSushiRow|TestSushiStartFailsWhenTheList|TestSushiPidfileEnv|TestSushiNeverTouches|TestWiredLimitGate|TestSushiConfirm' -count=1`. Expected FAIL: the loaded test returns nil; the gate test may already pass.
- [ ] **Step 3: Implement** `confirmLoaded` and the gated call after `warmupFor` success.
- [ ] **Step 4: Run green.** `cd wt && go test ./... -count=1`.
- [ ] **Step 5: Commit** `feat(wt): confirm the sushi pack is listed loaded after the warmup`

---

## Task 18: Sushi loaded-flag pins (inventory, start, `wt served`)

Tests only; no production change (any failure means the dependency order is wrong; do not add production code here).

**Files:** modify `wt/internal/localmodels/inventory_test.go` (helper `sushiModelsServer(t, packs map[string]string) *httptest.Server`: entries `{id, loaded: state=="ready", state}`), `served_test.go`, `wt/internal/lifecycle/confirm_test.go` (rows in `TestStartedGoneDecidesPerFamily`), `wt/internal/lifecycle/lifecycle_test.go`, `wt/cmd/wt/model_cmds_test.go` (525-560).

- [ ] **Step 1: Write the tests** (top-level comments):
  - `TestInventorySushiRunningFollowsTheLoadedFlag`: registry sushi provider + model `sushi/qwen3.8-flash-next` / `beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw`, built like the neighbouring rec tests. `unloaded` -> `Running false`, family `StatusOK`, entry still present as a start row; `loading` -> false; `ready` -> true; a foreign pack loaded but ours not -> false; ours loaded among others -> true; `{"data":[]}` -> false, StatusOK; closed port -> false and `snap.Down["sushi"]` per the served-only rule (same as rec).
  - `TestServedIDsSushiReturnsOnlyLoadedPacks`: `p/a` unloaded, `p/b` ready, `p/c` loading -> `[p/b]`.
  - `TestStartedGoneDecidesPerFamily` rows: sushi pack loaded -> `""`; listed unloaded -> `is no longer serving it`; port refuses -> `sushi no longer answers at <closed>`.
  - `TestStartIsANoOpOnlyWhenTheSushiPackIsLoaded`: mirror how the existing mtplx already-serving start test counts spawns; `loaded:true` -> nil and 0 spawns; `loaded:false` asserts only that start does NOT report "already serving" (it reaches spawn or returns `*PortBusyError` when no verified pidfile; the comment says so).
  - `TestRunServedSushiListsOnlyLoadedPacks`: `runServed` prints only the loaded id; `--json` gives `{"provider":"sushi","served":["p/b"]}`; all unloaded -> `"served":[]` (not null).
- [ ] **Step 2: Run.** `cd wt && go test ./internal/localmodels/ ./internal/lifecycle/ ./cmd/wt/ -run 'Sushi|StartedGoneDecides' -count=1`. Expected PASS (Tasks 2 and 16 are in). TDD proof: confirm in a scratch copy of `wt/` under the scratchpad (never the worktree; no stash) that reverting the `Loaded` filter makes the inventory test fail.
- [ ] **Step 3: Run the module.** `cd wt && go test ./... -count=1 && go vet ./... && gofmt -l .`
- [ ] **Step 4: Commit** `test(wt): pin the sushi loaded-flag behaviour in inventory, start and wt served`

---

## Task 19: Help strings name sushi

**Files:** modify `wt/cmd/wt/model.go` 79-80 (`  - ollama, omlx, mtplx, rec, sushi: when the command is installed, or a` re-wrapped under 80 columns with the 25-column continuation indent used by the other bullets), `wt/cmd/wt/model_write.go` 92 (`(ollama, omlx, mtplx, rec, sushi, openrouter)`, re-wrap 92-93), `wt/cmd/wt/model_cmds.go` 74 (`stopCmd` Long: `(ollama, omlx, omlx-6bit, mtplx, rec, sushi)`), `wt/cmd/wt/model_cmds_test.go` (after `TestNoHelpTextNamesModelman` ~1040).

- [ ] **Step 1: Failing test** `TestHelpNamesSushiWhereItListsProviders` (comment: the three help texts that enumerate the providers wt seeds or stops name sushi; without it a new built-in provider is silently missing from the help): obtain the command tree as `TestNoHelpTextNamesModelman` does; for paths `{"model","init"}`, `{"model","add"}`, `{"stop"}` assert `strings.Contains(c.Long, "sushi")`.
- [ ] **Step 2: Run.** `cd wt && go test ./cmd/wt -run TestHelpNamesSushiWhereItListsProviders -count=1`. Expected FAIL: `Long does not name sushi` three times.
- [ ] **Step 3: Edit** the three strings; no behaviour change. Grep found no golden help file under `wt/` or `docs/` pinning them.
- [ ] **Step 4: Run green.** `cd wt && gofmt -l . && go vet ./... && go test ./... -count=1`.
- [ ] **Step 5: Commit** `docs(wt): name sushi in the model init, model add and stop help`

---

## Task 20: Eval suite and shipped-suite test

`JUDGE_ROUTES` on this branch is `("litellm","openrouter")` (`eval/suite.py:32`), so a suite with `route = "claude-cli"` does not load until PR #505 merges. The suite ships with `claude-cli` as the spec requires; the test and the dry run substitute `openrouter` in a temp copy only while `claude-cli` is absent from `JUDGE_ROUTES`.

**Files:** create `benchmarks/suites/eval-sushi-vs-ollama.toml` (model on `eval-quant-tier.toml`); create `llmbench/tests/benchmark/eval/test_shipped_suites.py` (style of `test_suite.py`: in-memory `Registry`, `tmp_path`, no registry file, network or home).

**Interfaces:**
- Consumes: registry id/`model_name` above; base_url `http://localhost:12345/v1`; `load_suite`, `resolve_row_endpoint` (read the real signature in `llmbench/src/llmbench/benchmark/eval/suite.py` ~312-340 first; JUDGE_ROUTES is at ~32 in the same file).
- Produces: suite `name = "eval-sushi-vs-ollama"`.

- [ ] **Step 1: Failing tests** (`SUITE = Path(__file__).resolve().parents[4] / "benchmarks" / "suites" / "eval-sushi-vs-ollama.toml"`, assert `SUITE.parent.is_dir()`; `_registry()` has providers `ollama`, `sushi` and models `ollama/qwen3.8:27b-mlx` (`qwen3.8:27b-mlx`) and `sushi/beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw` (`beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw`), family `qwen3.8`; `_loadable(tmp_path)` returns SUITE unchanged if `claude-cli` is in `JUDGE_ROUTES`, else a copy with exactly `route = "claude-cli"` replaced by `route = "openrouter"`): `test_the_suite_judges_through_the_claude_cli_route` (raw TOML `judge.route == "claude-cli"`, `judge.samples == 1`); `test_rows_are_sushi_then_ollama_and_group_serially` (model ids, provider ids `["sushi","ollama"]`, routes `["direct","litellm"]`); `test_both_rows_pin_the_same_explicit_sampling` (`generation == {"temperature":1.0,"top_p":0.95,"top_k":20}`); `test_the_sushi_direct_route_sends_the_pack_id` (`routes_direct["sushi"].base_url == "http://localhost:12345/v1"` and the resolved endpoint model is `model_name` verbatim); `test_the_suite_never_names_a_second_big_model`.
- [ ] **Step 2: Run.** `cd llmbench && uv run pytest tests/benchmark/eval/test_shipped_suites.py -q`. Expected FAIL: missing suite file.
- [ ] **Step 3: Create the suite.** Header comment: purpose; prerequisite `sudo sysctl iogpu.wired_limit_mb=59000` (resets on reboot; wt and llmbench refuse below ~51,934 MiB); wt built from this change installed and the registry rows from guide 02 Step 4 in place BEFORE load (a row naming an unknown model fails `load_suite`); samples = 1 first; context fixed at 131072 by wt's Spawn literal; thinking at defaults on both sides (decision 15); low worker counts (`--max-concurrent 1`, 300 s stall timeout); rows serial, sushi first, restore stops Sushi before the claude-cli judge. Keys: `name`; `cooldown_s = 15.0`; `[judge] model = "anthropic/claude-opus-5", temperature = 0.0, samples = 1, max_attempts = 2, route = "claude-cli"` with the comment `# needs PR #505 (eval-judge-claude-cli): until it merges JUDGE_ROUTES only accepts litellm/openrouter and this suite fails to load. Switch to route = "openrouter" to run before then.`; `[generation] temperature = 1.0, top_p = 0.95, top_k = 20`; `[coding] dataset = "humaneval"`; `[routes.direct.sushi] base_url = "http://localhost:12345/v1"`; `[[rows]] model = "sushi/beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw", route = "direct"` (no `provider=`, no `direct_model`); `[[rows]] model = "ollama/qwen3.8:27b-mlx", route = "litellm"`.
- [ ] **Step 4: Run green.** `cd llmbench && uv run pytest tests/benchmark/eval/test_shipped_suites.py -q && make check && make test`.
- [ ] **Step 5: Scratch dry run** (never the real registry): `S=$(mktemp -d)`; write `$S/registry.toml` (fields as in `docs/contracts/registry.sample.toml`) with the two providers and models; if `claude-cli` is not a judge route, `sed` a copy of the suite to `openrouter` in `$S`. Then `cd llmbench && HOME=$S/home WT_REGISTRY=$S/registry.toml uv run llmbench eval run --suite <suite> --root ../benchmarks/tasks/eval --dry-run`. Expected: two rows (sushi direct, then ollama litellm), `dry run - nothing executed`, exit 0, nothing created under the real `~/.config`.
- [ ] **Step 6: Commit** `feat(llmbench): eval suite comparing Sushi Qwen3.8-Flash-Next with ollama qwen3.8:27b-mlx`

---

## Task 21: Engineering docs

Docs describe landed behaviour: re-read the landed code for every name before writing, and copy literals from `table.go` and the contract file, not from the spec. Check-before: `git grep -c -i sushi -- CLAUDE.md wt/CLAUDE.md llmbench/CLAUDE.md llmbench/docs/internals.md wt/docs/internals/local-models.md wt/docs/wt-start-stop.md wt/docs/wt-model.md` prints nothing.

**Files and edits:**
- `CLAUDE.md` (root) lines 17, 19, 45-46, 50: isolate list `<ollama|omlx|omlx-6bit|mtplx|rec|sushi>`; restore `stop mtplx, rec, sushi and mlx_lm_server`; a `sushi` architecture bullet after the rec bullet (one model-less `sushi serve` on 12345, `ScopePort`/`Exclusive`/`ServedOnly`; the Spawn literal; changing ctx means editing `table.go` and the contract in one commit; pidfile/log/`WT_SUSHI_PIDFILE`; ready `http-get /health`; `stop = []` means wt signals the verified pid; `warmup_seconds = 400`; needs `sudo sysctl iogpu.wired_limit_mb=59000`, checked never set; Running = `loaded:true`; registration `wt model add sushi beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw --family <f>`); the stop-mechanisms gotcha gains sushi (SIGTERM to the verified pid, never the bare binary). Fix any "only mtplx positional" claim.
- `wt/CLAUDE.md` 138, 146-147, 283: "mtplx, rec and sushi"; new bullets: empty `Stop` = verified-pid stop (shared helper with `stopLoadingServer`; no pidfile is an error; `TestTableInvariants` requires PidfileDefault, Subcommand, PortFlag); `ProcessSpec.WarmupSeconds`/`ReadyHTTP`; loaded-aware served list (`FetchModelIDsKeyed` drops explicit `loaded:false`; omlx keeps its branch); wired-limit preflight (`sysctl -n iogpu.wired_limit_mb`, 1.1 x safetensors MiB, 47,213 -> 51,934, `sudo sysctl iogpu.wired_limit_mb=59000`, wt never sets it, gated by `wiredLimitFamilies`); isolation env block gains `WT_SUSHI_PIDFILE="$D/sushi.pid"` after `WT_REC_PIDFILE` and notes cmd/wt TestMain isolates it.
- `llmbench/CLAUDE.md` 61, 62, 79: process-style providers are mtplx, rec, sushi; signal-stop specs spawn in a new session and `cleanup_on_failure` is true for them; keys `warmup_seconds`, kind `http-get`, `stop = []` (`PidfileProcess.stop()` with the `sushi`-in-`ps` guard, never `subprocess.run`); a malformed entry still disables all process providers; sushi sends no header (loopback exempt from `--api-key`); `sushi` in the autouse-guard binary list; bullet: sushi isolation fails closed (`ok=False`) for an unowned server; `POSITIONAL_MODEL_PROVIDERS` = mtplx, rec, sushi.
- `llmbench/docs/internals.md` 13: `SUPPORTED_PROVIDER_IDS` gains sushi; "mtplx, rec, sushi" and "no rec or sushi modules"; per-kind readiness, loaded-aware `_lists`/`already_serving`, `warm_timeout` from the spec, `TimeoutError` -> `ok=False`, `SIGNAL_STOP_TIMEOUT`, unowned refusal, wired-limit preflight in `already_serving`.
- `wt/docs/internals/local-models.md` 11, 14, 16, 29-34, 49-72, 99: sushi in the start list, `wt served` (only `loaded:true` ids), Tenancy; Spec fields gain `WarmupSeconds`/`ReadyHTTP`/empty `Stop`; a `**sushi**` bullet with the full spec and the start sequence (binary on PATH; preflight; port closed within 10 s else `PortBusyError`; Setsid spawn; `/health` 200; warmup validates the id then a 1-token chat naming `model_name`; confirm `loaded:true`; on failure kill only the wt-spawned process, remove pidfile and `.wt`); known limits (effective ctx invisible: `/v1/models` shows 1048576; foreign idle server is `PortBusyError`; a foreign server with the pack loaded shows Running; SIGTERM during an in-flight load unmeasured; during a cold load Running is false while the port is open); a llmbench-started sushi has a bare-pid pidfile with no `.wt` record: `wt stop` still stops it (the argv `sushi serve --port 12345` and uid check decide), but without start-time protection against pid reuse; llmbench's own stop checks only that "sushi" appears in the pid's `ps` line (weaker than wt's uid/start/argv checks, fail-open when `ps` cannot answer); line 99 notes the shared verified-pid helper.
- `wt/docs/wt-start-stop.md` 99-115: sushi in the bare-provider list (read the stop provider resolution in `cmd/wt` first and list what it accepts); the signal-stop paragraph (checks uid, `.wt` record, argv `sushi serve --port 12345`; SIGTERM to the group, 10 s, SIGKILL only for the same process, wait for port close; a hand-started Sushi has no pidfile: wt says so and stops nothing; stopping unloads the pack; one model per process; a bare-pid pidfile written by llmbench is accepted when the argv matches).
- `wt/docs/wt-model.md` 95-100, 131-135: sushi among seeded providers with the exact seed (`base_url` copied from `table.go`: `http://127.0.0.1:12345/v1`; `model_dir ~/.sushi/models`); `wt model add sushi <org/pack> --family <f>` writes `model_name` verbatim and the derived id `sushi/<org>/<pack>` (local ids keep the slash); `model_info` flags set by hand (wt detects them only for ollama).

- [ ] **Step 1:** run the check-before grep (no output).
- [ ] **Step 2:** apply the edits.
- [ ] **Step 3:** `git grep -c -i sushi -- <seven files>` each >= 1; `make check-links` exit 0; `git grep -n -F 'serve --port {port} --host 127.0.0.1 --ctx-size 131072 --max-resident-models 1 --no-update-check --max-tokens 32768' wt/internal/provider/table.go docs/contracts/process-providers.toml` returns both files.
- [ ] **Step 4: Commit** `docs: document the Sushi provider in CLAUDE.md files, internals and wt command docs`

**Review focus:** every quoted literal matches `table.go` and the contract byte for byte; no command is claimed run unless it was; unrun commands carry `<!-- UNVERIFIED -->`.

---

## Task 22: User docs (guides 02, 05, 06, 08, 11 and the provider-artifacts reference)

Check-before: `git grep -c -i sushi -- docs/guides/02-providers-and-models.md docs/guides/05-benchmarks.md docs/guides/06-wt-agents-and-models.md docs/guides/08-maintenance-and-troubleshooting.md docs/guides/11-capability-eval-benchmark.md docs/reference/provider-artifacts.md` prints nothing. Re-read the landed seed values in `table.go` before copying.

**Edits:**
- **02** (57, 166, new Sushi block after the rec block at 259, downloads table 269-273, 305, 322): install `brew install beamivalice/tap/sushi` (1.4.1 installed here); `sushi pull beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw` (75.9 GB: 49.5 GB safetensors + 32 GB `ngram_table.bin`; 46 GB resident) marked `<!-- UNVERIFIED -->`; packs under `~/.sushi/models/<org>/<dir>`; commands `wt model add sushi ... --family <f>`, `wt model init`, `wt start sushi/beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw` (about 115 s cold, about 10 s cached, budget 400 s), `wt stop sushi`; registry row example (provider with `model_dir`, seeded `base_url`; model with `model_name` beamster/..., `model_info` flags true); bullets: `model_name` must be Sushi's exact id (unknown id returns 503 "No default model configured", not 404), the 4096 trap (wt always passes `--ctx-size 131072`; a hand-started Sushi runs at its own default; wt warns, llmbench refuses), wired limit (need 51,934 MiB; macOS default about 49,152 fails; `sudo sysctl iogpu.wired_limit_mb=59000` resets on reboot; wt and llmbench check and tell, never set), one pack at a time (`--max-resident-models 1`, Exclusive), Running only when `loaded:true`, claude reaches it through LiteLLM and pi direct or via the proxy, interim `CLAUDE_CODE_MAX_CONTEXT_TOKENS=131072` in a slot-profile env `<!-- UNVERIFIED -->`, pi told 262144 until the `context_window` follow-up; downloads-table row `| sushi | sushi pull <org>/<dir> | <org>/<dir> under ~/.sushi/models | rm -rf that directory |`; a discovered id is NOT available for sushi (no artifact scan).
- **05** (19, table 78-83, 89, 114, 212-213): isolate list gains sushi; a `sushi` Stops row and sushi added to every other Stops column; restore stops mtplx, rec, sushi, mlx_lm_server; gotchas: rec and sushi are positional (`POSITIONAL_MODEL_PROVIDERS` = mtplx, rec, sushi), low worker counts (`--max-concurrent 1`, 300 s stall timeout), isolate fails closed for a hand-started Sushi, wired limit checked before anything else is stopped, `llmbench provider stop sushi` of a hand-started server reports "not started by llmbench or wt" and leaves it running, and either tool can stop a server started by the other (a llmbench-started bare-pid server is stopped by `wt stop` when its argv matches).
- **06** (192): `supported_providers` for claude and pi must list `sushi`; claude via LiteLLM, pi direct; verification `wt smoke --tools sushi/beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw --only claude,pi` `<!-- UNVERIFIED -->` (confirm flags against `wt/docs/wt-smoke.md`).
- **08** (log table 249-256, a "`wt start sushi/...` fails?" block after 303, gotchas 611-623): logs `/tmp/local-ai-setup-sushi.log` plus `~/.sushi/logs`; triage commands `sysctl -n iogpu.wired_limit_mb` (needs >= 51934), `curl -s http://127.0.0.1:12345/health`, `curl -s http://127.0.0.1:12345/v1/models`, `tail -50 /tmp/local-ai-setup-sushi.log`, `ls ~/.sushi/models/beamster/`; 503 "No default model configured" means `model_name` is not an exact served id; up to 400 s in the warmup is the cold load; a second `wt start` during the load meets `PortBusyError`; port answering with no pidfile means a hand-started Sushi (wt will not kill it); a prompt over ctx gets 400 `exceeds ctx_size`; `sushi update`/`brew upgrade` replaces the binary under a running server, stop first. Unrun commands marked `<!-- UNVERIFIED -->`.
- **11** (Step 3 36-45, gotchas 127-135): a "Sushi against ollama" recipe: dry run first (`uv run llmbench eval run --suite ../benchmarks/suites/eval-sushi-vs-ollama.toml --root ../benchmarks/tasks/eval --dry-run`), prerequisites (wired limit, registry rows, wt installed before the registry names sushi, claude-cli judge needs PR #505 else switch the suite to openrouter), samples = 1 first, serial rows (sushi first), restore stops Sushi before the judge, thinking at defaults, `--max-tokens 32768` is in Spawn because the eval transport sends no `max_tokens`; the full run `<!-- UNVERIFIED -->` (loads 46 GB); gotchas: low worker counts, eval takes `message.content` only and raises on null.
- **`docs/reference/provider-artifacts.md`** (title line 1, inventory table 12-30, new `## sushi` section after 254 modelled on `## rec` 219-254): tap binary (not a LaunchAgent), the Spawn literal, pidfile/log paths, `WT_SUSHI_PIDFILE`, stop by verified pid, data under `~/.sushi` (`models/`, `logs/`, `pi/`), pack sizes, wt never deletes anything (`wt model rm` removes the registry entry only), llmbench isolates it from the same contract entry (shares pidfile and log; no sushi module), contract path `docs/contracts/process-providers.toml`.

- [ ] **Step 1:** run the check-before grep (no output).
- [ ] **Step 2:** apply the edits.
- [ ] **Step 3:** `git grep -c -i sushi` on the six files each >= 1; `make check-links` exit 0 (new links such as `../benchmarks/suites/eval-sushi-vs-ollama.toml` resolve, since Task 20 landed); `git grep -n UNVERIFIED` shows a marker next to every pull/start/smoke/eval command.
- [ ] **Step 4: Commit** `docs: add Sushi to provider, benchmark, agent, troubleshooting and eval guides`

**Review focus:** registry `model_name` exactly `beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw` and the slash id form `sushi/beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw` (wt's derived id; `--` is the cloud spelling); no `sudo` command is presented as something wt runs.

---

## Task 23: Skills and changelog

Check-before: `git grep -n -E 'only for mtplx|\(mtplx, rec\)' -- .claude/skills/adding-a-benchmark-backend/SKILL.md` prints the stale lines (~15 and ~50).

**Files:**
- `wt/.claude/skills/adding-a-provider/SKILL.md` 33-48 and 61-81: Stop may be empty (sushi): stopped by signalling the recorded pid after the `stopLoadingServer` checks; `TestTableInvariants` then requires PidfileDefault, Subcommand and PortFlag; never leave a Stop that would run the bare binary; Ready list `ReadyModelsList`, `ReadyHTTP` (`http-get`: 200 on `Path`, no Command; use it when `/v1/models` cannot list the model before the first request), `ReadyCommandJSON`; `WarmupSeconds` explicit on every Process (mtplx 120, rec 120, sushi 400; contract key `warmup_seconds`); sushi as a model row (ScopePort, ServedOnly, one model-less `serve`, `--ctx-size` fixed in Spawn, `http-get /health`, empty Stop); a family whose `/v1/models` lists unloaded models reports `loaded` and `FetchModelIDsKeyed` drops explicit `loaded:false`; step 4: the contract file changes in the same commit.
- `.claude/skills/adding-a-benchmark-backend/SKILL.md` 8-16, 34-53: `(mtplx, rec, sushi)`; replace the stale positional sentence with "The benchmark runners pass a positional model for the providers in `POSITIONAL_MODEL_PROVIDERS` (`benchmark/isolation.py`): mtplx, rec and sushi" (verify against `isolation.py`/`runner.py`); readiness is `models-list`, `command-json` or `http-get`; empty `stop = []` is signalled by pid via `PidfileProcess`; `warmup_seconds` required; sushi as the example for the `DEFAULT_PROVIDER_IDS` and `_FAKE_BINARIES` reminders.
- `wt/CHANGELOG.md` lines 3-5: first bullet under `## Unreleased` / `### Added`, wrapped like its neighbours: Sushi is a local provider (`wt start|stop|list` and `wt -A claude|pi -M sushi/...`); one model-less `sushi serve` on 12345 with ctx fixed at 131072 in the spawn literal; waits on `GET /health` then loads the pack with a 400 s warmup; listed Running only while `/v1/models` shows `loaded:true`; `wt stop sushi` signals the verified pid, never runs the bare binary, never kills a server it did not start; a start checks `iogpu.wired_limit_mb` and prints `sudo sysctl iogpu.wired_limit_mb=59000` when too low (never sets it); process specs gain `WarmupSeconds` (mtplx, rec stay 120 s), the `http-get` ready kind and an empty `Stop`; the served list ignores `loaded:false` entries; register with `wt model add sushi <org>/<pack> --family <f>`, see `docs/guides/02-providers-and-models.md`. Edit to match landed code.

- [ ] **Step 1:** run the check-before grep.
- [ ] **Step 2:** apply the edits.
- [ ] **Step 3:** `make check-links` exit 0; `git grep -n 'only for mtplx' -- .claude` empty; `git grep -c -i sushi` on the three files >= 1.
- [ ] **Step 4: Commit** `docs(wt): changelog entry and skill updates for the Sushi provider (stop-by-pid, http-get, positional rec and sushi)`

---

## Task 24: Final verification (no repository changes, no commit)

Everything runs with a scratch HOME and a recording shim so a leaked `sushi` call is recorded, not executed. If a step fails, fix it in the owning task.

- [ ] **Step 1: Setup.** `S=$(mktemp -d)` (export it for the following steps); `rm -rf $S/home $S/shim $S/shim.log; mkdir -p $S/home $S/shim`; capture `GOCACHE`, `GOMODCACHE`, `UV_CACHE_DIR` from the real environment first. Shim `$S/shim/sushi`: `#!/bin/sh`, append `sushi $*` to `$S/shim.log`, `exit 99`; `chmod +x`. Record the before state: `ls -la /tmp/local-ai-setup-sushi.pid > $S/pid.before 2>&1`, `(lsof -nP -iTCP:12345 -sTCP:LISTEN || true) > $S/port.before`, `ls ~/.config/local-ai ~/.config/agent-wt > $S/config.before 2>&1`.
- [ ] **Step 2: wt.** `cd wt && env HOME=$S/home PATH=$S/shim:$PATH <caches> go vet ./... && env HOME=$S/home PATH=$S/shim:$PATH <caches> go test -count=1 ./... && make check` (needs shellcheck/shfmt: if absent record that, not green). Doc-comment scan for added tests: `git diff --name-only main...HEAD -- '*_test.go'` then `awk 'prev !~ /^\/\// && /^func Test/ {print FILENAME":"FNR": "$0} {prev=$0}' <file>` prints nothing for added tests.
- [ ] **Step 3: llmbench.** `cd llmbench && env PATH=$S/shim:$PATH UV_CACHE_DIR=... make check && make test` (ruff format --check, B905, mypy, coverage floor).
- [ ] **Step 4: Contract both directions.** `cd wt && go test -count=1 -run TestProcessProvidersContractMatchesTheProviderTable -v ./internal/config` (PASS: table to toml and toml to table incl. `warmup_seconds` and sushi); `cd llmbench && uv run pytest tests/providers/test_process_spec.py -q`. Negative control in a scratch copy only: copy the contract to `$S/pp.toml`, set sushi's `warmup_seconds = 401`, run the Python spec test against it (`LLMBENCH_PROCESS_PROVIDERS=$S/pp.toml`; confirm the override variable name in `process_spec.py` first) and record whether Python or only the Go test catches the drift.
- [ ] **Step 5: Repo targets.** `make check-links`; `make lint`; `make test-all` with `PATH=$S/shim:$PATH HOME=$S/home` plus caches (it also runs modeltiers, untouched).
- [ ] **Step 6: Config dirs.** `env HOME=$S/home bin/check-config-dirs-untouched` (the script fails on any machine where the real `~/.config/local-ai` exists, so it is run against the scratch HOME the suites used). Expected exit 0.
- [ ] **Step 7: No real sushi ran.** `test -s $S/shim.log && echo FAIL || echo OK`; then diff the pidfile, listener and config listings against the `.before` files (all empty); `ps -axo pid,command | grep -c '[s]ushi serve'` equals the before state. Scan the llmbench tests for unpatched sushi stop paths: run `cd llmbench && uv run pytest -q -p no:randomly` once while `printf 99999999 > /tmp/local-ai-setup-sushi.pid` is NOT present, then confirm the autouse redirect test passes and `git grep -n 'BACKENDS\["sushi"\]' tests` hits all patch `stop_and_wait`/`already_serving`. Scan added tests for `12345`: `git diff main...HEAD -- '*_test.go' '*.py' | grep -n 12345`; every hit must be a string compared against the spec or Spawn, never a dial or urlopen target.
- [ ] **Step 8: Record** in the PR description (not a repo file): commands, exit codes, whether the #505 `openrouter` substitution was needed, shellcheck/shfmt presence, the shim.log result.

---

## Handoff (not executor tasks)

Needs Keith's go-ahead: it replaces `~/.local/bin/wt`, writes registry rows to the real config and loads the 46 GB pack. Not CI. The executor stops after Task 24 and reports this list.

1. Precondition: the branch is reviewed and merged (or Keith says to install from the worktree). Keith runs `sudo sysctl iogpu.wired_limit_mb=59000` himself (resets on reboot); the pack is under `~/.sushi/models/beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw` (else `sushi pull beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw`, 75.9 GB).
2. `make install` BEFORE any registry row names sushi (a registry written first breaks provider routing). Check `wt model list`.
3. `wt model init`; `wt model add sushi beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw --family <qwen3.8 family id>`; hand-set `model_info` `supports_function_calling = true` and `supports_vision = true`; add sushi to the claude and pi agents' `supported_providers` (guide 06); `wt litellm sync`.
4. `wt start sushi/beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw`: expect the replacement prompt for other local models, `/health` within about 1 s, the pack loading 10 to 115 s, then Running; confirm `curl -s http://127.0.0.1:12345/v1/models` shows `loaded:true` and `context_length` 131072. Record the Exclusive replacement flow.
5. `wt smoke --tools sushi/beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw --only claude,pi` (confirm flags in `wt/docs/wt-smoke.md`). Record tool-call behaviour: the acceptance test for tool use.
6. `wt stop sushi`: exit within about 2 s, port 12345 closed, pidfile and `.wt` gone, no leftover `sushi serve`. Optionally SIGTERM during an in-flight load (unmeasured). Also check the real `ps` argv of the server matches `serverArgvMatches` (a brew wrapper changing argv[0] would make the stop refuse), the real memory-refusal wording (extend `looksLikeMemoryRefusal` if it differs), and, on a machine at the macOS default limit, whether `sysctl -n iogpu.wired_limit_mb` really prints 0 (both `parseWiredLimit` and `_wired_limit_mb` read 0 as 75% of `hw.memsize`; drop the rule in both if it prints a real number).
7. One cheap eval request: copy the suite, keep `samples = 1`, run `uv run --directory llmbench llmbench eval run --suite benchmarks/suites/eval-sushi-vs-ollama.toml --root benchmarks/tasks/eval --row 0 --category reasoning` (judge route `claude-cli` needs PR #505; otherwise point the copy at openrouter). Expect isolate sushi, one answer in `message.content`, restore stopping Sushi. Then clear the `<!-- UNVERIFIED -->` markers this covered.
8. **PR #505 rebase note.** #505 (`eval-judge-claude-cli`) edits `llmbench/tests/conftest.py` (the `_FAKE_BINARIES` line and comment, line ~60), `tests/providers/lifecycle/test_hermeticity.py` (parametrize block), `eval/suite.py` (`JUDGE_ROUTES`), `eval/runner.py`, `llmbench/CLAUDE.md`, `docs/internals.md`, `benchmarks/suites/eval-sweep.toml`, guide 11. This plan is built on the tree without #505 (Tasks 14, 21, 22 touch the overlapping spots). The second PR to merge resolves by keeping both: `_FAKE_BINARIES = frozenset({"launchctl", "omlx", "mtplx", "rec", "ollama", "wt", "claude", "sushi"})` and both sets of hermeticity cases; then `test_shipped_suites.py` stops substituting `openrouter`.
9. Follow-ups, out of this PR: the `context_window` registry field (own PR; `pi_models.go`, `claude_window.go`); optional `bytes_resident` for the `--others` memory prompt.

## Self-review record

1. **Design coverage.** Spec section to task: Spec/row/contract -> 1, 16; E1 -> 2, 12, 13, 18; E2 -> 1, 4, 11, 13; E3 -> 5, 6, 14; E4 -> 1, 3, 11, 13; wired-limit preflight -> 8, 9, 15; unowned server -> 10, 14; pack-id check -> 7, 13; model identity, context size, start/stop sequence -> 16, 17, docs 21-22; llmbench registration -> 16; eval suite -> 20; tests list -> in each task; docs list -> 21-23; acceptance run and #505 seam -> Handoff. Deliberately not covered: `context_window` (decision 16), `bytes_resident` (optional), `bin/update/sushi` (decision 13).
2. **Gaps not closed here** (documented, not coded):
   - A llmbench-started sushi has a bare-pid pidfile and no `.wt` record. `wt stop` signals it when its argv matches (no start-time protection against pid reuse); Task 14 closes the other design gaps (Setsid-equivalent `new_session` for signal-stop specs only, `cleanup_on_failure`, log tail on a warmup failure).
   - Warmup retries any non-2xx chat answer until the budget ends, so a memory-refusal 5xx shows its hint only after up to 400 s. The design does not ask for an early exit.
   - Sushi's real memory-refusal wording and whether macOS reports `iogpu.wired_limit_mb` as 0 at the default are unmeasured (Handoff step 6); the 0 -> 75% rule is implemented identically in both languages as an addition beyond the design.
   - llmbench's stop identity check is weaker than wt's (see Review Focus 2); accepted, documented.
   - Task 9/17/19 and others stay separate small commits (bisectable); not merged.
   - The seed uses `127.0.0.1`, the registry/suite example uses `localhost` (a design inconsistency; both reach one listener).
