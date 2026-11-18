# wt-session model-server configuration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A session stores how it reaches models (`native` | `local` | `remote`), asks for it when unset, and `wt config` gets a Model server tab that creates, checks and changes it, including a wt-managed LaunchAgent for `local`.

**Architecture:** Session-side resolution (`ResolveMode`, typed errors) lives in `internal/modelclient`; an admin-side, screen-free package `internal/modelsetup` observes, plans and applies setup through injected seams; `internal/configeditor` gets a third tab that is a thin view over it; `cmd/wt-session` wires the tab and a launch gate that opens it. A LiteLLM install/config guide ships first and is the authority for everything the tool does not do.

**Tech Stack:** Go 1.26.7 (`wt/`), Bubble Tea v1.3.10, bubbles v1.0.0, BurntSushi/toml v1.6.0, stdlib `encoding/xml` and `text/template` (no plist library), macOS launchd via `launchctl`.

**Spec:** `docs/superpowers/specs/2026-11-17-wt-session-model-server-config-design.md` (approved 2026-11-17). Read it first; this plan decides only what the spec leaves to the builder.

## Global Constraints

- Modes are exactly `native`, `local`, `remote`; stored as `mode` in the `[model-server]` table of `~/.config/agent-wt/config.toml` beside `url` and `token_file` (a path, never the secret). Env override `WT_MODEL_MODE`; `WT_MODEL_URL` + (`WT_MODEL_TOKEN_FILE` or `WT_MODEL_TOKEN`) act as `remote` over any stored mode.
- Effective-mode order, first match wins: valid `WT_MODEL_MODE`; env endpoint+token pair (`remote`); stored `mode`; inferred `remote` when url or token_file is stored; otherwise unset. An invalid `WT_MODEL_MODE` or stored mode is a problem that refuses.
- wt-model LaunchAgent: label `local.wt-model.serve`, plist `~/Library/LaunchAgents/local.wt-model.serve.plist`, `RunAtLoad` and `KeepAlive` true, listens on `127.0.0.1:8787` by default, token file `~/.config/wt-model/server.token` (32 random bytes, base64, mode 0600), `PATH` captured from the applying process, an XML comment `managed by wt` marks wt's plist. LiteLLM label `local.litellm.proxy`, port 4000, default config `~/.config/litellm/config.yaml`.
- Remote token written to `~/.config/agent-wt/remote-model-server.token` mode 0600.
- Config is written last in every plan; `config.toml` with a TOML syntax error is never overwritten or moved aside; no secret is ever written inline into `config.toml` or passed in argv.
- `internal/modelsetup` is admin side: add it to `adminBanned` in `internal/depcheck`; `ResolveMode` and the mode errors stay in `internal/modelclient`; `configeditor` stays off `internal/tui`.
- Per-call seams (struct fields), never package globals, for new machine access; every `Test*` has a top-level `//` comment saying what and why; per-package `TestMain` isolation (`config.IsolateConfigHomeForTest()`, plus the package's own stubs); no test touches `launchctl`, `lsof`, the real home or a real port.
- Messages follow the style guide (M3 one `wt: ...` line, lower-case, M6 what/why/way out, M7 what is still true, M9 `—` and `·` in prose/status only); the tab uses ASCII words, not glyphs (V5), and `themes` tokens only (V1/V2).
- Docs: `/Users/user_name` as the home placeholder, no live state in guides (D4), repo-relative links checked by `make check-links` (only git-tracked `*.md`).
- Refusals exit 1; PR bodies use `Refs`, never a closing keyword plus a number; merging needs `gh pr merge --squash --admin`.

## Review Focus

Failure modes the spec implies that no feature task's happy path exercises, most likely first. Each has a test in the owning task.

1. `config.toml` with a TOML syntax error: reads must report it, writes must refuse, bytes on disk unchanged (T3 `TestUpdateModelServerRefusesASyntaxBrokenFile…`, T13 `TestServerTabDisablesWritesOnAParseError`).
2. Environment conflicts: `WT_MODEL_MODE=native` with `WT_MODEL_URL` set; stored `native` with an env pair; env supplying only one of url/token (T4 `TestResolveModeTable`).
3. Something already listening on the chosen port that is not wt's LaunchAgent: refuse, name the pid, write nothing (T10 `TestBuildPlanRefusesAForeignListener`).
4. A plist at the target path without wt's marker (hand-written): refuse; with the marker: overwrite (T10 `TestBuildPlanRefusesAnUnmarkedPlist`).
5. A step failing mid-apply (bootstrap error, ping timeout): config must stay untouched and the result must say which steps ran (T11 `TestApplyStopsAtTheFirstFailureAndLeavesConfigUntouched`).

---

## Execution notes

Five PRs, in order, each branched from the latest `main`: **Stage 0** (docs: the spec, this plan, the LiteLLM guide, doc fixes), **Stage 1** (config, modelclient, litellm reader, `modelsetup`), **Stage 2** (the tab), **Stage 3** (resolution and launch gate: the only behaviour change), **Stage 4** (LiteLLM plist actions), then a docs sweep that rides with each stage and closes in T21. Work in a worktree per stage; verify each task with the commands it names, and run `cd wt && go test ./... > /tmp/<stage>.log 2>&1; echo GATE_EXIT=$? >> /tmp/<stage>.log` with `run_in_background` (no inner `&`) before each stage's PR. The spec and this plan commit into the Stage 0 PR.

---

# Stage 0 — documentation first

### Task 1: The LiteLLM install and configuration guide

**Files:**
- Create: `docs/guides/14-litellm-install-and-configuration.md`
- Modify: `README.md` (guide table after row 12; also add the missing row 13), `CLAUDE.md` (root, line 4 topic list: append "LiteLLM install")

**Interfaces:** Produces the guide path that later tool messages and docs link to.

- [ ] **Step 1: Read** guides 01 (§1, §5–§7), 04, 08, `docs/reference/litellm-admin-ui-setup.md`, `bin/update/litellm`, `wt/internal/litellm/restart.go` and `proxyenv.go` so the guide states what the code does, not what older docs say.
- [ ] **Step 2: Write the guide** following the guide template (H1, `> Use this to:`, `> Verified against:` with the LiteLLM version printed by `litellm --version` and today's date, Prerequisites, TL;DR, Steps, Verification, Gotchas, Going deeper). Required content, in this order: (a) install with `uv tool install --python 3.11 'litellm[proxy,extra-proxy]'` and why 3.11 and `extra-proxy`; (b) Postgres 16 install and `CREATE DATABASE litellm`; (c) Prisma client generation, migrations run at startup, **no `prisma db push`**; (d) seed `~/.config/litellm/config.yaml` and that wt owns it (`wt litellm sync`); (e) an **annotated plist**: a table of every `EnvironmentVariables` key (`DATABASE_URL`, `LITELLM_MASTER_KEY`, `LITELLM_SALT_KEY`, `OPENROUTER_API_KEY`, `UI_USERNAME`, `UI_PASSWORD`, `PATH`, the five `MAX_*` size/logging limits) saying what it is and whether it is optional, plus `SoftResourceLimits`, label, `--config`, `--port 4000`, `KeepAlive`, log paths; (f) generating the master key and salt key (`openssl rand -hex 32` with an `sk-` prefix for the master key), the **salt key must never change once the database holds encrypted data** with a backup note, file mode **0600**; (g) `launchctl bootstrap gui/$(id -u) <plist>`; (h) ports, logs, `WT_LITELLM_PLIST`, `WT_LITELLM_CONFIG`; (i) restart with `wt litellm restart` (graceful drain up to 3 minutes, `WT_LITELLM_RESTART_CMD`), upgrade with `bin/update/litellm`; (j) linking to wt-model: `wt-model litellm set --url http://localhost:4000 --api-key <master key>` and restart wt-model afterwards (the pair is read once); (k) verification: `/health/liveliness` (unauthenticated), `/health/readiness` (reports `db`), an authenticated `/v1/models` (401 means wrong key), `wt litellm status|list`; (l) what the `wt config` Model server tab will and will not do (links to guide 06 and the design doc); (m) a wt-model LaunchAgent template and token-file recipe; (n) troubleshooting table. Use `/Users/user_name` and no live model state.
- [ ] **Step 3: Index it** in `README.md` and root `CLAUDE.md`.
- [ ] **Step 4: Verify:** `git add docs/guides/14-*.md README.md CLAUDE.md && make check-links` prints `ALL LINKS OK`; `git grep -n "/Users/keith" docs/guides/14-*.md` prints nothing; `grep -n "db push" docs/guides/14-*.md` only inside a "do not" sentence.
- [ ] **Step 5: Commit:** `docs: add the LiteLLM install and configuration guide`.

### Task 2: Fix the stale LiteLLM docs the audit found

**Files:**
- Modify: `docs/guides/01-initial-setup.md` (malformed `<key>StandardErrorPath</string>` at the end of the plist template; add `chmod 600` after the heredoc; restart advice lines ~73 and ~436 → `wt litellm restart`; link to guide 14), `docs/guides/04-litellm-config.md` (restart blocks at ~38, ~244, ~279–282; add `WT_LITELLM_PLIST` to the §6 env table), `docs/guides/08-maintenance-and-troubleshooting.md` (restart advice ~97, ~214, ~740), `docs/reference/litellm-admin-ui-setup.md` (Step 9 and troubleshooting: remove `prisma db push`, say migrations run automatically on ≥1.98)
- Leave: `docs/guides/00-config-map.md` (its "Owner: you" wording changes in T20; modeltiers tests pin its sample blocks)

**Interfaces:** none.

- [ ] **Step 1:** Make the edits above. Keep `launchctl kickstart -k …` only where the text says it is the fallback when wt's own restart cannot run.
- [ ] **Step 2:** For each guide whose `> Verified against:` header you edit, set it to the version `litellm --version` prints and today's date **only** if you re-read every command in the edited sections.
- [ ] **Step 3: Verify:** `make check-links`; `cd modeltiers && uv run pytest tests/test_guide_samples.py -q` passes; `grep -rn "prisma db push" docs/guides docs/reference/litellm-admin-ui-setup.md` shows no instruction to run it.
- [ ] **Step 4: Commit:** `docs: fix stale LiteLLM install, restart and plist guidance`. Open the Stage 0 PR (spec, plan, guide, fixes).

---

# Stage 1 — storage, resolution and `modelsetup` (no behaviour change)

### Task 3: `[model-server]` mode field and a tolerant reader/writer

**Files:**
- Modify: `wt/internal/config/modelserver.go`, `wt/internal/config/modelserver_test.go`

**Interfaces:**
- Produces:
  - `ModelServerState` gains `Mode string` with every field tagged `,omitempty` (`mode`, `url`, `token_file`, `token`); strings only (it is compared with `==` in tests).
  - `type ModelServerFile struct { Path string; Exists bool; Table *ModelServerState; ParseErr error; TableErr error }`
  - `func ReadModelServerFile() ModelServerFile` — never errors; reads `Path()`; syntax is checked by decoding into `map[string]any`; the table by decoding into a struct holding only `ModelServer *ModelServerState \`toml:"model-server"\``, so errors elsewhere in the file never hide it.
  - `func UpdateModelServer(change func(cur ModelServerState) (ModelServerState, error)) error` — under `WithLock`, `readConfigFile` (a syntax error returns that error and writes nothing), apply `change`, set `ModelServerTable` (nil when the result is zero), `writeConfigFile`.

- [ ] **Step 1: Write the failing tests** in `modelserver_test.go` using the existing `sessionHome(t, body)` helper: `TestReadModelServerFileMissingFile` (`Exists` false, no errors); `TestReadModelServerFileSyntaxError` (`ParseErr` set, `Table` nil); `TestReadModelServerFileSemanticErrorElsewhereStillReadsTheTable` (an `[[agents]]` entry with a wrong-typed key plus a valid table: `ParseErr` nil, `Table.Mode` read); `TestReadModelServerFileTableTypeError` (`TableErr` set); `TestUpdateModelServerPreservesOtherTables` (agents, `[launch]` survive); `TestUpdateModelServerRefusesASyntaxBrokenFileAndLeavesItUntouched` (file bytes identical afterwards; Review Focus 1); `TestUpdateModelServerKeepsAnExistingStricterMode` (0600 stays); `TestNativeModeWritesOnlyTheModeKey` (file text has `mode = "native"` and no `url =`, `token_file =` or `token =`); `TestUpdateModelServerChangeErrorWritesNothing`.
- [ ] **Step 2:** `cd wt && go test ./internal/config -run 'ModelServer' -count=1` → FAIL (undefined).
- [ ] **Step 3: Implement** the signatures above in `modelserver.go`. `Problems()` additionally returns a problem for a non-empty `Mode` outside `native|local|remote` (case-insensitive, trimmed).
- [ ] **Step 4:** the command from Step 2 plus `go test ./internal/config -count=1` → PASS (the existing `TestModelServerTableSurvivesAnUnrelatedSave` must still pass).
- [ ] **Step 5: Commit:** `feat(config): store the model-server mode, tolerant reader and writer`.

### Task 4: `ResolveMode` and the mode errors in `modelclient`

**Files:**
- Create: `wt/internal/modelclient/mode.go`, `wt/internal/modelclient/mode_test.go`

**Interfaces:**
- Produces:
  - `type Mode string`; consts `ModeNative Mode = "native"`, `ModeLocal`, `ModeRemote`; `func ParseMode(s string) (Mode, bool)` (trimmed, case-insensitive).
  - `type ModeSource int`; consts `SourceNone` (0), `SourceEnvMode`, `SourceEnvEndpoint`, `SourceStored`, `SourceInferred`.
  - `type Resolution struct { Mode Mode; Source ModeSource; Problem string }` (`Mode == ""` and `Problem == ""` means unset).
  - `func ResolveMode(storedMode, storedURL, storedTokenFile string, env func(string) string) Resolution` — the spec's five rules in order.
  - Errors (pointer receivers): `type ModeUnsetError struct{}`, `type NativeModeError struct{ Source ModeSource }`, `type ModeProblemError struct{ Problem string }`; `func IsModeUnset(err error) bool`, `func IsNativeMode(err error) bool`, `func NoServer(err error) bool` (unset, native, or the legacy `NoServerConfigured`). `ModeUnsetError.Error()` contains `wt config` and `WT_MODEL_MODE`; `NativeModeError.Error()` says native mode has no model server and names `wt config`.

- [ ] **Step 1: Write the failing tests** in `mode_test.go`: `TestResolveModeTable` — a table of (env map, stored mode/url/token_file) → (mode, source, problem): env `WT_MODEL_MODE=native` + `WT_MODEL_URL` set → native/EnvMode (Review Focus 2); stored `native` + env url and `WT_MODEL_TOKEN` → remote/EnvEndpoint; env url only (no token) + stored `native` → native/Stored; `WT_MODEL_MODE=bogus` → Problem names `native, local, remote`; stored `LOCAL` → local/Stored; no mode, url+token_file → remote/Inferred; no mode, only token_file → remote/Inferred (half-configured; `ResolveEndpoint` later refuses); nothing anywhere → unset; env pair via `WT_MODEL_TOKEN_FILE` counts; `TestErrorsAreRecognisedThroughWrapping` (`errors.As` after `Still`); `TestNoServerCoversUnsetNativeAndLegacy`; `TestModeUnsetMessageNamesTheFix`.
- [ ] **Step 2:** `cd wt && go test ./internal/modelclient -run 'Mode|NoServer' -count=1` → FAIL.
- [ ] **Step 3: Implement** in `mode.go` (pure; reuses `Still` for wrapping; do not touch `endpoint.go`).
- [ ] **Step 4:** the same command, then `go test ./internal/modelclient -count=1` → PASS.
- [ ] **Step 5: Commit:** `feat(modelclient): resolve the model-server mode and its typed errors`.

### Task 5: A LiteLLM plist reader beside the environment reader

**Files:**
- Modify: `wt/internal/litellm/proxyenv.go`; Create: `wt/internal/litellm/proxyplist.go`, `wt/internal/litellm/proxyplist_test.go`, `wt/internal/litellm/testdata/proxy.plist`

**Interfaces:**
- Produces (exported): `const ProxyLabel = proxyLabel`; `type ProxyPlistInfo struct { Path, Label string; ProgramArguments []string; Port int; ConfigPath string; RunAtLoad, KeepAlive bool; EnvNames []string; Mode os.FileMode }` (`EnvNames` sorted, **names only**; `Port` 0 when no `--port`); `func ReadProxyPlist(path string) (ProxyPlistInfo, error)` (XML only; a `bplist` prefix returns an error naming `plutil -convert xml1`); `func ReadProxyPlistVar(path, name string) (value string, ok bool, err error)` (the single way to read one `EnvironmentVariables` value; callers must never print it).

- [ ] **Step 1: Write the failing tests:** `TestReadProxyPlistFixture` (label, arguments, port 4000, config path, `KeepAlive`, `EnvNames` includes `LITELLM_MASTER_KEY` and the struct has no value for it, `Mode` from `os.Stat`); `TestReadProxyPlistRejectsABinaryPlist`; `TestReadProxyPlistVarReturnsOnlyTheNamedValue`; `TestReadProxyPlistMissingFile`. The fixture is a synthetic plist with fake values.
- [ ] **Step 2:** `cd wt && go test ./internal/litellm -run ProxyPlist -count=1` → FAIL.
- [ ] **Step 3: Implement** with `encoding/xml` and the existing `eachDictEntry` helper, extending it to decode `array`, `integer`, `true`, `false` elements (the reader today decodes only strings).
- [ ] **Step 4:** the same command plus `go test ./internal/litellm -count=1` (its `TestMain` must still pass untouched) → PASS.
- [ ] **Step 5: Commit:** `feat(litellm): read the proxy plist's arguments, port and key names`.

### Task 6: The `modelsetup` package skeleton, seams and the ratchet entry

**Files:**
- Create: `wt/internal/modelsetup/env.go`, `wt/internal/modelsetup/testmain_test.go`, `wt/internal/modelsetup/fakes_test.go`
- Modify: `wt/internal/depcheck/depcheck_test.go` (add `"internal/modelsetup"` to `adminBanned`), `wt/docs/internals/packages.md` (a row after `internal/configeditor/`, and the admin list at "Package graph rules"), `wt/CLAUDE.md` (the ratchet paragraph's admin list and the "Go module" package list), `wt/docs/internals/testing.md` (isolation list), and every explicit package list that names `internal/hoststop` (find with `grep -rn "internal/hoststop" wt/Makefile .github/workflows`), adding `internal/modelsetup`

**Interfaces:**
- Produces in `env.go`: `const AgentLabel = "local.wt-model.serve"`, `DefaultListen = "127.0.0.1:8787"`; `type Env struct { FS FS; Launchd Launchd; Config ConfigStore; Net Net; Host Host; DesiredRoutes func(ctx context.Context) ([]string, error) }` and `func DefaultEnv() Env`. Seam interfaces (all methods take a `context.Context` first where they block):
  - `FS`: `ReadFile(path string) ([]byte, error)`, `Stat(path string) (fs.FileInfo, error)`, `WriteAtomic(path string, data []byte, perm os.FileMode) error`, `Remove(path string) error`, `Backup(path string) (string, error)` (dated copy beside the file, same mode).
  - `Launchd`: `Available() bool`, `Print(ctx, label string) (LaunchdJob, error)`, `Bootstrap(ctx, plistPath string) error`, `Bootout(ctx, label string) error`, `Kickstart(ctx, label string, kill bool) error`; `type LaunchdJob struct { Loaded, Running bool; PID int }`.
  - `ConfigStore`: `Read() config.ModelServerFile`, `Update(change func(config.ModelServerState) (config.ModelServerState, error)) error`, `Pair() (config.ProxyPair, error)`, `SetPair(url, key *string) error`.
  - `Net`: `Ping(ctx, ep modelclient.Endpoint) (modelapi.Ping, error)`, `Get(ctx, url, bearer string) (status int, body []byte, err error)`, `ListenerPID(ctx, port int) (pid int, ok bool, err error)` (new, `lsof -nP -sTCP:LISTEN -iTCP:<port> -F p`), `ProcessStart(ctx, pid int) (time.Time, error)` (`ps -o lstart= -p`).
  - `Host`: `Getenv(string) string`, `Home() (string, error)`, `Executable() (string, error)`, `LookPath(string) (string, error)`, `Now() time.Time`, `RandRead([]byte) error`, `GOOS() string`, `UID() int`.
  The real `Launchd` and `Net.ListenerPID` implementations refuse under `testing.Testing()`, like `litellm.realRunShell`.
- Test helpers (`fakes_test.go`): a `fakeEnv` struct recording every call in order, with settable canned results per method and an in-memory `FS`; `newFakeEnv(t) *fakeEnv` returns an `Env`.

- [ ] **Step 1: Write the failing tests:** `TestDefaultEnvRefusesLaunchdAndListenerLookupsUnderTest` (real `Launchd.Bootstrap` and `Net.ListenerPID` return an error mentioning "test binary"); creating the package makes the existing `TestEveryInternalPackageIsClassified` in `depcheck_test.go` fail until `internal/modelsetup` is added to `adminBanned` — that red-to-green is part of this task.
- [ ] **Step 2:** `cd wt && go test ./internal/depcheck ./internal/modelsetup -count=1` → FAIL.
- [ ] **Step 3: Implement** `env.go` and `testmain_test.go` (`config.IsolateConfigHomeForTest()`, `gitenv.IsolateForTest()` is **not** needed; restore and `os.Exit`), add the ratchet entry and every doc/list edit above.
- [ ] **Step 4:** `go build ./... && go vet ./... && go test ./internal/depcheck ./internal/modelsetup -count=1` → PASS; `make check-links` OK.
- [ ] **Step 5: Commit:** `feat(modelsetup): package skeleton, seams and the admin-side ratchet entry`.

### Task 7: The wt-model LaunchAgent plist (render and recognise)

**Files:**
- Create: `wt/internal/modelsetup/agent.go`, `wt/internal/modelsetup/agent_test.go`, `wt/internal/modelsetup/testdata/agent.golden.plist`

**Interfaces:**
- Produces: `type AgentSpec struct { Binary, Listen, TokenFile, Path, Home string }`; `func RenderAgentPlist(s AgentSpec) []byte` (deterministic; XML comment `managed by wt` after the DOCTYPE; label `AgentLabel`; arguments `[Binary serve --listen Listen --token-file TokenFile]`; `RunAtLoad`/`KeepAlive` true; `StandardOutPath`/`StandardErrorPath` `<Home>/.wt-model.log` and `.wt-model.err.log`; `EnvironmentVariables` with `PATH` only; XML-escaped); `type AgentPlist struct { Managed bool; Label string; Binary string; Listen string; TokenFile string }`; `func ParseAgentPlist(data []byte) (AgentPlist, error)` (`Managed` is true only with the marker **and** the label).

- [ ] **Step 1: Write the failing tests:** `TestRenderAgentPlistMatchesTheGolden` (update the golden deliberately); `TestParseAgentPlistRoundTrips`; `TestParseAgentPlistWithoutTheMarkerIsNotManaged` (Review Focus 4 input); `TestRenderEscapesXMLSpecials` (a `PATH` containing `&`); `TestParseAgentPlistRejectsBinary`.
- [ ] **Step 2:** `cd wt && go test ./internal/modelsetup -run 'AgentPlist' -count=1` → FAIL.
- [ ] **Step 3: Implement** with `text/template` plus `html/template`-free manual XML escaping via `encoding/xml.EscapeText`; parse with `encoding/xml` (`litellm.eachDictEntry` is unexported — write a small local walker).
- [ ] **Step 4:** same command → PASS. **Step 5: Commit:** `feat(modelsetup): render and recognise wt's wt-model LaunchAgent plist`.

### Task 8: `Observe`

**Files:**
- Create: `wt/internal/modelsetup/observe.go`, `wt/internal/modelsetup/observe_test.go`

**Interfaces:**
- Consumes: T3 `config.ReadModelServerFile`, T4 `modelclient.ResolveMode`, T5 `litellm.ReadProxyPlist`, T7 `ParseAgentPlist`, T6 `Env`.
- Produces: `type ObserveOptions struct { ListenPort int }`; `func Observe(ctx context.Context, env Env, opts ObserveOptions) State` (never errors; each part carries its own error text) and
  - `type State struct { Platform Platform; Resolution modelclient.Resolution; EnvOverride string; Config ConfigState; Token TokenState; Pair PairState; Agent AgentState; Listener ListenerState; Server ServerState; LiteLLM LiteLLMState }`
  - `Platform{OK bool; Reason string}` (OK only when `Host.GOOS()=="darwin"` and `Launchd.Available()`); `ConfigState{Path string; Exists bool; ParseErr, TableErr string; Table config.ModelServerState; Problems []string}`; `TokenState{Path string; Exists, Loose bool; Err string}`; `PairState{URL string; KeySet bool; Err string}`; `AgentState{PlistPath string; Present, Managed bool; Job LaunchdJob; Listen, Binary, TokenFile string; Err string}`; `ListenerState{Port, PID int; Ours bool}`; `type CheckStatus int` (`CheckSkipped`, `CheckOK`, `CheckWarn`, `CheckFail`), `type Check struct{ Status CheckStatus; Detail string }` and `type LiteLLMState struct { Checked bool; Reachable, Database, KeyAccepted, RoutesInSync, RestartOwed, PortMatches, PlistMode Check }` (declared here so `State` compiles; T9 fills them); `ServerState{Checked, OK bool; Kind ServerKind; Message string; API, Schema int}` with `ServerKind` consts `KindNone`, `KindUnreachable`, `KindUnauthorized`, `KindNotWtModel`, `KindSkew`, `KindSchema` mapped from `modelclient`'s error types; `EnvOverride` names the env vars in force (`"WT_MODEL_URL, WT_MODEL_TOKEN_FILE"`) or `""`.

- [ ] **Step 1: Write the failing tests** against `fakeEnv`: `TestObserveMissingConfigIsEmptyNotAnError`; `TestObserveReportsAParseErrorAndStillFillsTheRest` (Review Focus 1: `Config.ParseErr` set, `Token`/`Agent` still observed); `TestObserveClassifiesEachPingFailure` (one subtest per `ServerKind`); `TestObserveLoosenessOfTheTokenFile`; `TestObserveRecognisesAManagedAndAnUnmanagedPlist`; `TestObserveListenerOursVersusForeign` (listener pid equals the launchd job pid → `Ours`); `TestObserveOffMacOSDisablesLocal`; `TestObserveNamesEnvOverrides`; `TestObserveNeverPrintsASecret` (no field of `State` contains the fake token or pair key).
- [ ] **Step 2:** `cd wt && go test ./internal/modelsetup -run Observe -count=1` → FAIL. **Step 3: Implement.** **Step 4:** PASS. **Step 5: Commit:** `feat(modelsetup): observe the machine without writing anything`.

### Task 9: The LiteLLM checks

**Files:**
- Create: `wt/internal/modelsetup/litellmcheck.go`, `wt/internal/modelsetup/litellmcheck_test.go`; Modify: `observe.go` (call it when the effective mode is `local`)

**Interfaces:**
- Consumes: the `Check`/`LiteLLMState` types declared in T8. Produces: `func CheckLiteLLM(ctx context.Context, env Env, pair config.ProxyPair, plistPath, configPath string) LiteLLMState`.
  - Reachable: unauthenticated `GET <url>/health/liveliness` is 200. Database: `GET <url>/health/readiness` JSON `db == "connected"` (a failure is `CheckWarn`, informational). KeyAccepted: authenticated `GET <url>/v1/models` is 200 (`CheckFail` on 401, detail "rejected", never "is the master key"). RoutesInSync: model ids from `/v1/models` against `Env.DesiredRoutes` (skipped when nil; `cmd/wt-session` supplies the same function `wt litellm list` uses). RestartOwed: `config.yaml` mtime later than the proxy process start (`Launchd.Print(litellm.ProxyLabel)` pid, `Net.ProcessStart`). PortMatches: the plist's `--port` equals the port in `pair.URL`. PlistMode: `ReadProxyPlist(...).Mode` is `0600` (`CheckWarn` otherwise).

- [ ] **Step 1: Write the failing tests** (canned `Net.Get` responses): `TestCheckLiteLLMAllGood`; `TestKeyRejectedIsReportedAsRejectedNotAsWrongMasterKey`; `TestDatabaseDownIsAWarningNotAFailure`; `TestRoutesInSyncDetectsAMissingAndAnExtraRoute`; `TestRestartOwedWhenConfigIsNewerThanTheProxy`; `TestPortMismatch`; `TestLooseModePlistWarns`; `TestUnreachableSkipsTheAuthenticatedChecks`; `TestNothingPrintsTheKey`.
- [ ] **Step 2–5:** `go test ./internal/modelsetup -run CheckLiteLLM -count=1` FAIL → implement → PASS → commit `feat(modelsetup): check that LiteLLM is set up correctly for wt-model`.

### Task 10: `BuildPlan` and `Plan.Format`

**Files:**
- Create: `wt/internal/modelsetup/plan.go`, `wt/internal/modelsetup/plan_test.go`, `wt/internal/modelsetup/testdata/plan_local.golden`, `plan_remote.golden`, `plan_native.golden`

**Interfaces:**
- Consumes: T8 `State`. Produces:
  - `type Desired struct { Mode modelclient.Mode; URL, TokenFile, Listen, PastedToken, MasterKey string; SaveUnchecked, RemoveAgent bool }` (`MasterKey` is used only when the pair is unset and the LiteLLM plist has none).
  - `type StepKind string` consts `StepToken`, `StepPair`, `StepPlist`, `StepBootout`, `StepBootstrap`, `StepKickstart`, `StepWait`, `StepConfig`, `StepRemovePlist`; `type Step struct { Kind StepKind; Path, Detail string }`; `type Plan struct { Want Desired; Steps []Step; Notes []string }`; `type RefusalError struct{ Reasons []string }` (`Error()` joins them; each reason says what is still true and the way out).
  - `func BuildPlan(st State, want Desired) (Plan, error)` (pure); `func (p Plan) Format() string` (deterministic, no clock, ASCII; the text the user confirms).
  - Rules: **native** — config step only (plus `StepBootout`+`StepRemovePlist` when `RemoveAgent` and a managed plist exists). **remote** — requires url and a token (existing 0600 file path, or `PastedToken` written to `~/.config/agent-wt/remote-model-server.token`, a different existing file replaced only when the plan says so); when `st.Server.Checked && !OK && !SaveUnchecked` refuse naming the classified failure; steps: token, config (plus `StepBootout`+`StepRemovePlist` when `RemoveAgent` and a managed plist exists, as for native). **local** — refuse when `!Platform.OK`; refuse a foreign listener on the port (`Listener.PID != 0 && !Ours`) naming the pid and offering another port (Review Focus 3); refuse an existing plist that is not `Managed` naming its path (Review Focus 4); refuse when `Host.LookPath`/the binary beside `Host.Executable()` finds no `wt-model` ("run `make install`"); steps in order: token (keep an existing valid one), pair (keep a set one; else from the LiteLLM plist key; else `MasterKey`; else refuse), plist (omit when `ParseAgentPlist` already matches), bootout (when a loaded job's arguments differ), bootstrap (or `StepKickstart` when only the process must restart), wait, config **last**.

- [ ] **Step 1: Write the failing tests:** `TestBuildPlanNativeIsOnlyTheConfigStep`; `TestBuildPlanNativeOffersToRemoveAManagedAgent`; `TestBuildPlanLeavingLocalNeverRemovesTheTokenOrThePair` (no step touches the token file or the pair); `TestBuildPlanRemoteRefusesAFailedCheckUnlessSavedUnchecked`; `TestBuildPlanRemoteReplacesADifferentPastedTokenOnlyWhenTheTextSaysSo`; `TestBuildPlanLocalStepsInOrderWithConfigLast`; `TestBuildPlanLocalReusesAnExistingPairAndToken`; `TestBuildPlanRefusesAForeignListener`; `TestBuildPlanRefusesAnUnmarkedPlist`; `TestBuildPlanOverwritesAMarkedPlist`; `TestBuildPlanRefusesWhenWtModelIsMissingAndNamesMakeInstall`; `TestBuildPlanLocalIsEmptyOfWorkWhenAlreadyApplied` (idempotence: only the config step if the table differs, else no steps); `TestBuildPlanRefusesLocalOffMacOS`; `TestFormatMatchesTheGoldens` (three goldens) and `TestFormatIsDeterministic` (twice, equal).
- [ ] **Step 2–5:** `go test ./internal/modelsetup -run 'BuildPlan|Format' -count=1` FAIL → implement → PASS → commit `feat(modelsetup): plan each mode and say exactly what will change`.

### Task 11: `Apply` and `Control`

**Files:**
- Create: `wt/internal/modelsetup/apply.go`, `wt/internal/modelsetup/apply_test.go`

**Interfaces:**
- Produces: `type Result struct { Done []Step; Failed *Step; Err error; Warnings []string }`; `func Apply(ctx context.Context, env Env, p Plan, progress func(string)) Result` (executes `p.Steps` in order, stops at the first failure, calls `progress` with a one-line status per step; `StepWait` pings with a bounded retry until the server answers, then reports the classified error and the log path on timeout; `StepConfig` calls `Config.Update`, setting `mode`, `url`, `token_file` and clearing a stored inline `token` only when the plan says so; token files are written 0600 with `FS.WriteAtomic`; the plist is written with the marker; every file write goes through `FS`); `type ControlAction int` (`ControlStart`, `ControlStop`, `ControlRestart`); `func Control(ctx context.Context, env Env, a ControlAction) error` (`Start` = bootstrap if unloaded else kickstart; `Stop` = bootout; `Restart` = `Kickstart(…, true)`).

- [ ] **Step 1: Write the failing tests** with `fakeEnv`: `TestApplyRunsStepsInOrderAndWritesConfigLast` (recorded call order); `TestApplyStopsAtTheFirstFailureAndLeavesConfigUntouched` (bootstrap error, then a ping timeout: `Config.Update` never called, `Result.Failed` names the step, `Result.Done` lists the earlier ones; Review Focus 5); `TestApplyWritesTheTokenFile0600AndKeepsAnExistingOne`; `TestApplyIsIdempotent` (apply, `Observe`, `BuildPlan` again → no steps, no writes on the second apply); `TestApplyWaitTimesOutAndPointsAtTheLog`; `TestApplyRemoteWritesThePastedTokenBeforeTheConfig`; `TestControlStartStopRestart`; `TestApplyNeverCallsLaunchctlWhenThePlanHasNoLaunchdSteps`.
- [ ] **Step 2–5:** `go test ./internal/modelsetup -count=1` FAIL → implement → PASS (whole package) → commit `feat(modelsetup): apply a plan step by step, config last`.
- [ ] **Stage gate:** `cd wt && go build ./... && go vet ./... && make check && go test ./internal/config ./internal/modelclient ./internal/litellm ./internal/modelsetup ./internal/depcheck -count=1`; full suite in the background; open the Stage 1 PR (`Refs` the spec).

---

# Stage 2 — the Model server tab (additive)

### Task 12: Tab plumbing in `configeditor`

**Files:**
- Modify: `wt/internal/configeditor/tabs.go`, `editor.go`, `agents_tab.go` (the `tab models` help binding), `models_tab.go` (the `modelsHints` text, tab switching at the two `m.tab = TabAgents` sites)
- Test: `wt/internal/configeditor/models_tab_test.go` (the tab-literal tests), new `wt/internal/configeditor/server_tab_test.go`

**Interfaces:**
- Produces: `TabServer` appended after `TabModels`; `tabNames` becomes `{"Agents", "Models", "Model server"}`; `func nextTab(t Tab) Tab` (cycle) used by all three switch sites; `Options` gains `Server ServerDeps`; `type ServerDeps struct { Observe func(ctx context.Context, o modelsetup.ObserveOptions) modelsetup.State; Plan func(st modelsetup.State, want modelsetup.Desired) (modelsetup.Plan, error); Apply func(ctx context.Context, p modelsetup.Plan, progress func(string)) modelsetup.Result; Control func(ctx context.Context, a modelsetup.ControlAction) error }`; `func (d ServerDeps) withDefaults() ServerDeps` (built on `modelsetup.DefaultEnv()`); `Run` calls `o.Server = o.Server.withDefaults()`.

- [ ] **Step 1: Update the pinned tests first** (they fail for the right reason): `TestTabKeySwitchesTabsAndProbesOnce` now expects Agents→Models→Model server→Agents; the literal `"[Agents]   Models"` prefix and `" Agents   [Models]"` checks stay valid because the new name is appended; add `TestTabBarShowsThreeTabs` and `TestNextTabCycles`.
- [ ] **Step 2:** `cd wt && go test ./internal/configeditor -run 'Tab' -count=1` → FAIL. **Step 3: Implement** the constants, helper and the three switch-site edits; a placeholder `serverView()` returning the tab bar so the build passes. **Step 4:** PASS (`go test ./internal/configeditor -count=1`). **Step 5: Commit:** `feat(configeditor): add the Model server tab's plumbing`.

### Task 13: The tab's state, view and keys

**Files:**
- Create: `wt/internal/configeditor/server_tab.go`, `server_view.go`; Modify: `editor.go` (dispatch, `Init`, fit hook, `leave()` hold for an in-flight apply), `server_tab_test.go`

**Interfaces:**
- Consumes: T12 `ServerDeps`, T8–T11 `modelsetup` types. Produces `type serverTab struct` (own phase `serverView|serverEdit|serverConfirm|serverApplying`, `state modelsetup.State`, `want modelsetup.Desired`, `plan modelsetup.Plan`, `gen int`, `busy, writing bool`, `status string`, text inputs for url, token path, listen, pasted token (`EchoPassword`)); messages `serverObservedMsg{gen int; st modelsetup.State}`, `serverPlannedMsg`, `serverAppliedMsg{res modelsetup.Result}`; keys: `left/right/space` mode, `down/up/tab-in-form` fields, `a` plan then confirm (`Apply this plan? [y/N]`, default No: only `y`/`Y` confirms), `c` check, `r` re-read, `s`/`x`/`R` start/stop/restart via `Control` (local only), `tab` next tab, `q`/`ctrl+c` quit. All `ServerDeps` calls run in `tea.Cmd`s that capture deps and `gen` first; stale results are dropped. View: tab bar, blank, status block (mode, url, token path with `ok`/`loose`/`missing`, pair, agent, server check, LiteLLM checks, env override line, problems), form, plan text on the confirm screen, hint line via `fitHints`; ASCII only (`(x)`/`( )`); `tuilayout.WrapText`; nothing is wider than the terminal.

- [ ] **Step 1: Write the failing tests** using a `serverMachine` fake in the style of `tabMachine` (canned `State`, counters, recorded `Apply` calls) and the existing `send`/`keys`/`assertFits`/`tabSizes` helpers: `TestServerTabObservesOnFirstVisitOnly`; `TestServerTabShowsTheEffectiveModeAndEnvOverride`; `TestServerTabDisablesWritesOnAParseError` (state with `Config.ParseErr`: `a`, `s`, `x` only set status "nothing was changed"; `Apply` never called; message names the path; Review Focus 1); `TestServerTabConfirmIsDefaultNo` (every key but `y` leaves the prompt; `n`/`esc` cancel; `Apply` not called); `TestServerTabAppliesAfterY`; `TestServerTabOffersSaveAnywayDefaultNoAfterAFailedRemoteCheck` (a classified failure shows the message and `Save anyway? [y/N]`; only `y` sets `SaveUnchecked`); `TestServerTabDropsAStaleObservation`; `TestServerTabQuitWaitsForAnApplyInFlight` (quit held, result kept; mirrors `TestQuitWaitsForARegistryWriteInFlight`); `TestServerTabRefusalIsShownVerbatim` (a `RefusalError` from `Plan`); `TestServerTabLocalDisabledOffMacOS`; `TestServerTabFitsTheTerminal` (all `tabSizes`, every phase); `TestServerTabMasksThePastedToken`; `TestCtrlSOnTheServerTabDoesNothing`.
- [ ] **Step 2–5:** `go test ./internal/configeditor -count=1` FAIL → implement (follow `models_tab.go`'s probe/gen/apply pattern) → PASS → commit `feat(configeditor): the Model server tab`.

### Task 14: Wire the tab into `wt config`

**Files:**
- Modify: `wt/cmd/wt-session/commands_config.go` (`runConfigEditor` builds `Options.Server` from `modelsetup.DefaultEnv()` with `DesiredRoutes` set to the function `wt litellm list` uses — find it in `cmd/wt-session/litellm.go` and `internal/litellm`; do not reimplement; `configCmd`'s `Long` text: three tabs), `wt/cmd/wt-session/commands_config_test.go`

**Interfaces:** Consumes `configeditor.Options.Server`, `modelsetup.DefaultEnv`. Produces `runConfigEditor(out, errOut, a, start)` unchanged in signature (so `stubConfigEditor` stays valid).

- [ ] **Step 1: Write the failing tests:** `TestConfigEditorIsGivenTheModelServerDeps` (via `stubConfigEditor`, `editorCall.opts.Server.Observe != nil`); `TestConfigLongTextNamesThreeTabs`; `TestWtConfigOpensWhenConfigTomlIsSyntaxBroken` (config.toml garbage: `runWT(t, "config")` still reaches `configeditorRun` with a non-nil error; Review Focus 1).
- [ ] **Step 2–5:** `go test ./cmd/wt-session -run 'ConfigEditor|ConfigLong|WtConfigOpens' -count=1` FAIL → implement → PASS → commit `feat(wt): open the Model server tab from wt config`.
- [ ] **Stage gate:** `make check`; `go test ./internal/configeditor ./cmd/wt-session -count=1`; capture the tab at 80x24 through a pty (see memory `tui-screens-via-pty`) and look at it; full suite in the background; Stage 2 PR.

---

# Stage 3 — resolution and the launch gate (the behaviour change)

### Task 15: `resolveModels` consults the mode

**Files:**
- Modify: `wt/cmd/wt-session/models.go` (`resolveModels`, `nativeFallback`, `wouldLaunchNative`, a new `(*app).resetModelCaches()`), `wt/cmd/wt-session/stats_usage.go` and `model_cmds.go` (the two soft `NotConfiguredError` cases → `modelclient.NoServer`), `wt/internal/modelclient/endpoint.go` (reword `NotConfiguredError.Error()`: name `wt config` and the three modes, drop "wt never starts one" and the `[litellm]` clause), tests: `native_test.go`, `flip_test.go` (`noServerHome`), `models_test.go`, `internal/modelclient/endpoint_test.go`

**Interfaces:**
- Consumes: T3, T4. Produces: `resolveModels` returns `*modelclient.ModeProblemError`, `*modelclient.ModeUnsetError` or `*modelclient.NativeModeError` before any endpoint work; `nativeFallback(err, pinned, pinnedSet, tags, family) bool` is true only for `IsNativeMode(err)` with no `-M/-T/-F`; `func (a *app) resetModelCaches()` clears `sessCfg`, `sessCfgErr`, `sessCfgSet`, `built`, `builtErr`, `builtSet`, `models`.

- [ ] **Step 1: Update and add tests first.** `noServerHome` writes `[model-server]\nmode = "native"` so the seven `native_test.go` tests keep their meaning; add `TestUnsetModeRefusesALaunchWithoutAnything` (no `[model-server]`: exit 1, output names `wt config` and `WT_MODEL_MODE`, nothing launched: no marker file, no builds, `wantNothingCreated` for `-W`); `TestStoredNativeStillRefusesAPinnedModel` (`-M`, `-T`, `-F`, empty `-M`); `TestStoredNativeLaunchesNativelyOnEveryPath` (pre-flight, picker nil client, `launchFilteredRemote`, TUI); `TestEnvPairOverridesAStoredNative` and `TestWtModelModeEnvBeatsEverything` (Review Focus 2 at command level); `TestHalfConfiguredStoredRemoteStillRefuses`; `TestNotConfiguredErrorMessageNamesWtConfig` (replaces the pinned wording tests).
- [ ] **Step 2:** `cd wt && go test ./cmd/wt-session ./internal/modelclient -run 'Native|Unset|NotConfigured|Flip|Half' -count=1` → FAIL.
- [ ] **Step 3: Implement.** **Step 4:** `go test ./cmd/wt-session ./internal/modelclient -count=1` → PASS. **Step 5: Commit:** `feat(wt): sessions resolve their model-server mode`.

### Task 16: The first-run gate and the one-shot refusals

**Files:**
- Modify: `wt/cmd/wt-session/main.go` (the RunE pre-flight block that calls `a.buildModels(launchStill)` and `nativeFallback`), `wt/cmd/wt-session/models.go` (`func (a *app) ensureMode(cmd *cobra.Command) error`); tests in a new `wt/cmd/wt-session/mode_gate_test.go`

**Interfaces:**
- Produces: `ensureMode` runs only when `ResolveMode` is unset: with `stdinTTY()` true it calls `runConfigEditor(os.Stdout, os.Stderr, a, configeditor.TabServer)` (a separate program, run to completion), then `a.resetModelCaches()` and re-resolves; still unset → returns an error `no model-server mode chosen — nothing was launched` (exit 1); with no terminal it returns `*modelclient.ModeUnsetError` wrapped with `launchStill`. The pre-flight block calls `ensureMode` first (before the launch question, any worktree, branch or guard). Commands that need a server (`rotate`, `job`, `start`, `stop`, `model list --server`, `smoke`, `stats` soft, `profile … -M profile/`, `profile slots`) get the refusal through `openModels` and never open the editor.

- [ ] **Step 1: Write the failing tests:** `TestGateOpensTheModelServerTabOnATerminal` (`stubStdinTTY(t,true)`, `stubConfigEditor` recording `opts.StartTab == configeditor.TabServer` and writing `mode = "native"` to the fake config home as its side effect; afterwards the launch proceeds natively once); `TestGateDoesNothingWhenAModeIsStored`; `TestGateWithoutATerminalRefusesWithTheFix` (exit 1; no editor call; `wantNothingCreated`); `TestGateLeavingTheTabUnsetEndsTheLaunch`; `TestGateRunsBeforeAnyWorktreeIsCreated` (`-W newbranch`); `TestGateSkipsCommandAgents` (`-A shell`); `TestOneShotCommandsRefuseNamingWtConfigAndNeverOpenTheEditor` (table over the commands above: exit 1, "wt config" in the output, editor not called, each command's own still clause such as "nothing was started"); `TestBareWtWithoutATerminalKeepsThePickersOwnRefusal`; `TestResetModelCachesForgetsAFailure`.
- [ ] **Step 2–5:** `go test ./cmd/wt-session -run 'Gate|OneShot|ResetModel|BareWt' -count=1` FAIL → implement → PASS → commit `feat(wt): ask for a model-server mode before an unconfigured launch`.

### Task 17: Stage 3 docs and the #615 supersession note

**Files:**
- Modify: `wt/docs/wt-session.md` (§Configuration add `mode`; §Environment overrides add `WT_MODEL_MODE` and the env-pair rule; §The refusals: the new messages), `wt/docs/wt-config.md` (three tabs, the Model server tab, the syntax-error behaviour), `wt/CLAUDE.md` (the "Config (Go)" paragraph: `[model-server]` now carries `mode` and is written by `wt config`; the "LiteLLM routing state" paragraph: unset asks, native is a stored choice, no silent fallback; the Local-model/launch notes that mention #615), `wt/docs/internals/launch-flow.md` (the gate), `docs/superpowers/specs/2026-11-16-wt-native-launch-without-model-server-design.md` (a "Superseded in part" note linking the new spec), `docs/guides/06-wt-agents-and-models.md` and `docs/guides/13-deployment-use-cases.md` (reconcile token paths: local `~/.config/wt-model/server.token`, remote `~/.config/agent-wt/remote-model-server.token`; say the tab writes the table)

- [ ] **Step 1:** Make the edits; then `grep -rn "wt never starts one\|runs the agent natively\|does not write \[model-server\]\|not wt config settings" CLAUDE.md docs wt/docs wt/CLAUDE.md` and fix every remaining stale statement.
- [ ] **Step 2: Verify:** `make check-links`; `cd modeltiers && uv run pytest tests/test_guide_samples.py -q`. **Commit:** `docs: the stored model-server mode and the first-run gate`.
- [ ] **Stage gate:** `make check`, full suite in the background (`GATE_EXIT=0`), and a live check on a scratch config home with a built binary (`D=$(mktemp -d)`, the redirected-env recipe in `wt/CLAUDE.md`): no `[model-server]` → refusal naming the fix; `WT_MODEL_MODE=native wt --cwd -A claude` launches natively; Stage 3 PR with the #615 reversal stated plainly.

---

# Stage 4 — LiteLLM plist actions (a secrets file: its own review)

### Task 18: Create-when-absent and one-key rotation in `modelsetup`

**Files:**
- Create: `wt/internal/modelsetup/litellm_plist.go`, `litellm_plist_test.go`, `testdata/litellm.min.golden.plist`; Modify: `plan.go`, `apply.go`

**Interfaces:**
- Produces: `func GenerateKey(rand func([]byte) error) (string, error)` (`"sk-" + hex(32 bytes)`); `type LiteLLMSpec struct { Binary, ConfigPath, Path, Home, MasterKey, SaltKey string; Port int }`; `func RenderLiteLLMPlist(s LiteLLMSpec) []byte` (label `litellm.ProxyLabel`, arguments `[Binary --config ConfigPath --port Port]`, `EnvironmentVariables` = `LITELLM_MASTER_KEY`, `LITELLM_SALT_KEY`, `PATH`; `RunAtLoad`/`KeepAlive`; log paths `<Home>/.litellm.log` and `.litellm.err.log`; the `managed by wt` marker); `func ReplaceMasterKey(plist []byte, newKey string) ([]byte, error)` — a strict text edit: exactly one `<key>LITELLM_MASTER_KEY</key>` followed by a `<string>` element, binary (`bplist`) or ambiguous input is an error, the new value is XML-escaped, **every byte outside the replaced string element is unchanged**, and the secret never appears in argv or a log line. `Desired` gains `LiteLLM LiteLLMAction` (`LiteLLMNone`, `LiteLLMUseExistingKey` (default: read it into the pair, no plist edit), `LiteLLMCreate`, `LiteLLMRotateKey`); new `StepKind`s `StepLiteLLMPlist`, `StepBackup`, `StepChmod`, `StepMasterKey`, `StepKickstartLiteLLM`.
- Plan rules: **Create** only when no LiteLLM plist exists **and** a `litellm` binary and `config.yaml` exist (else refuse naming guide 14); the plan states it omits `DATABASE_URL`, UI login and provider keys. **Rotate** requires an existing readable XML plist; steps: backup (dated, same mode), master-key edit, chmod 0600 when looser (stated in the plan), pair update with the same key (`SetPair`), `kickstart` LiteLLM then wt-model; the plan lists what else may hold the old key. It never edits the salt key, `DATABASE_URL`, provider keys or UI login.

- [ ] **Step 1: Write the failing tests:** `TestReplaceMasterKeyChangesOnlyThatValue` (bytes before and after the string element identical; the result parses; a second key in the file untouched); `TestReplaceMasterKeyRefusesBinaryAmbiguousAndMissing`; `TestReplaceMasterKeyEscapesTheValue`; `TestRenderLiteLLMPlistMatchesTheGolden`; `TestGenerateKeyShape` (`sk-` + 64 hex, uses the injected reader); `TestBuildPlanCreateRefusesWhenLiteLLMIsNotInstalled`; `TestBuildPlanCreateOnlyWhenAbsent`; `TestBuildPlanRotateOrdersBackupEditPairKickstart`; `TestApplyRotateKeepsTheBackupAndTightensTheMode`; `TestApplyRotateFailureLeavesThePairUntouched`; `TestNoPlanOrResultTextContainsAKey` (secrets never in `Format()` or `Result`).
- [ ] **Step 2–5:** `go test ./internal/modelsetup -count=1` FAIL → implement → PASS → commit `feat(modelsetup): create the LiteLLM LaunchAgent when absent and rotate its master key`.

### Task 19: Tab actions for the master key and LiteLLM creation

**Files:** Modify `wt/internal/configeditor/server_tab.go`, `server_view.go`, `server_tab_test.go`

**Interfaces:** Consumes T18 `Desired.LiteLLM`. Produces key `k` (menu: use the plist's key / generate a new key / create the LiteLLM agent), always through the same plan-then-`[y/N]` confirm; the status shows the LiteLLM plist mode warning and offers "tighten to 0600".

- [ ] **Step 1: Write the failing tests:** `TestServerTabMasterKeyMenuOffersOnlyWhatTheStateAllows` (create only when absent; rotate only when present); `TestServerTabRotateShowsThePlanAndNeverTheKey`; `TestServerTabRotateIsDefaultNo`; `TestServerTabLooseLiteLLMPlistShowsAWarning`; `TestServerTabFitsTheTerminalWithTheMenu`.
- [ ] **Step 2–5:** `go test ./internal/configeditor -count=1` FAIL → implement → PASS → commit `feat(configeditor): master key and LiteLLM actions on the Model server tab`.

### Task 20: Ownership wording and the guide's tab section

**Files:** Modify `docs/guides/00-config-map.md` (plist table row ~32: owner "you, or wt (created when absent; one key rotated on request)"; add a row for `local.wt-model.serve.plist` (wt); reword the "Owner: you" line ~296; keep fenced sample blocks intact), `docs/guides/01-initial-setup.md` (the plist is no longer always hand-authored), `docs/guides/14-litellm-install-and-configuration.md` (the tab section now describes exactly what shipped)

- [ ] **Step 1:** Edit; **Step 2: Verify** `make check-links`, `uv run pytest tests/test_guide_samples.py -q` in `modeltiers`; **Step 3: Commit** `docs: LiteLLM plist ownership after the tab`.
- [ ] **Stage gate:** `make check`, full suite (`GATE_EXIT=0`), a manual live check on a **scratch** plist path (`WT_LITELLM_PLIST` pointing into a temp dir, never the real one): create and rotate against the scratch file, confirm the backup and mode; Stage 4 PR, called out as the stage that edits a secrets file.

---

# Stage 5 — closing sweep

### Task 21: Final docs and gates

**Files:** `wt/docs/wt-model-server.md` (setup now starts with the tab; `Where it runs`, `The token file`), `wt/docs/internals/config-and-registry.md` (`ModelServerState`, `UpdateModelServer`), `wt/docs/internals/tui.md` (the third tab), `wt/docs/internals/packages.md` (rows for `modelsetup` files), `docs/guides/06-wt-agents-and-models.md`, the spec (a "As built" note recording deviations: the single `Apply` result, plutil not used, ASCII tab), `CLAUDE.md` guide list

- [ ] **Step 1:** Rename/phrase check: `grep -rn "ThemedListDelegate\|wt never starts\|two tabs" CLAUDE.md docs wt/docs wt/CLAUDE.md` is clean; every new `Test*` has a top-level `//` comment (`grep -L` over the new test files' `^func Test` lines without a preceding comment).
- [ ] **Step 2: Gates:** `cd wt && make check && go vet ./... && go test ./... ` in the background (`GATE_EXIT=0`), `make check-links`, `make lint-skills`.
- [ ] **Step 3: Commit** `docs: close out the model-server configuration`; open the final PR; save a project memory noting the feature shipped and the stage PR numbers.

---

## Self-review (run against the spec)

- **Coverage:** modes and storage (T3, T4); env precedence (T4, T15); tolerant reader and syntax-error handling (T3, T13, T14); `modelsetup` Observe/Plan/Apply and seams (T6–T11); wt-model LaunchAgent, token, pair, port, binary, marker (T7, T10, T11); LiteLLM checks (T9) and narrow actions (T18, T19); the tab, its keys, confirm default-No, quit hold, ASCII, fit (T12, T13); first-run gate, non-TTY refusal, one-shot refusals, cache reset (T15, T16); message rewrites (T15, T17); guide and doc fixes (T1, T2, T17, T20, T21); depcheck and package docs (T6); stage order and PR boundaries (Execution notes). No gaps found.
- **Spec deviations recorded:** the tab uses ASCII words instead of the spec's glyph sketch (style guide V5); the master-key edit is a strict text edit, not `plutil -replace`, so the key never appears in argv and untouched bytes stay identical; `Apply` returns one `Result` (no separate error); `DesiredRoutes` is injected by `cmd/wt-session`.
- **Type consistency:** `Desired`, `Plan`, `Step`/`StepKind`, `State` and its parts, `Result`, `ControlAction`, `ServerDeps`, `Resolution`, `ModeSource` are defined once (T4, T8–T11, T12) and used under those names; `resetModelCaches` (T15) is used by `ensureMode` (T16); `TabServer` (T12) by the gate (T16).
- **Proportion:** signatures, test names and assertions only; bodies appear nowhere.
